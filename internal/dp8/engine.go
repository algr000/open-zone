package dp8

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"open-zone/internal/config"
	"open-zone/internal/dp8shim"
	"open-zone/internal/packetlog"
	"open-zone/internal/proto"
	"open-zone/internal/state"
)

// DirectPlay send flags (from dplay8.h):
// - DPNSEND_SYNC == 0x80000000
// - DPNSEND_NOCOMPLETE == 0x0002
// - DPNSEND_GUARANTEED == 0x0008
//
// Strategy:
//   - Avoid SYNC sends except for the initial connect bundle (ConnectRes/ConInfoRes/ConnectEv),
//     which dpnet rejects unless it is SYNC|GUARANTEED (historically observed 0x80070057).
const (
	dpnSendSync       uint32 = 0x80000000
	dpnSendGuaranteed uint32 = 0x0008

	dpnSendSyncGuaranteed = dpnSendSync | dpnSendGuaranteed

	dpnMsgIDOffset uint32 = 0xffff0000

	dpnMsgIDConnectComplete  uint32 = dpnMsgIDOffset | 0x0005
	dpnMsgIDCreatePlayer     uint32 = dpnMsgIDOffset | 0x0007
	dpnMsgIDDestroyPlayer    uint32 = dpnMsgIDOffset | 0x0009
	dpnMsgIDIndicateConnect  uint32 = dpnMsgIDOffset | 0x000e
	dpnMsgIDReceive          uint32 = dpnMsgIDOffset | 0x0011
	dpnMsgIDTerminateSession uint32 = dpnMsgIDOffset | 0x0016
)

type outMsg struct {
	dpnid      uint32
	tag        string
	exp        string
	payloadXML string
	tail       []byte
	flags      uint32
}

type Engine struct {
	cfg   config.Config
	runID string

	shim    *dp8shim.Shim
	log     *packetlog.Logger
	proto   *proto.Engine
	players *state.PlayerStore

	buf  []byte
	outQ chan outMsg

	mu sync.RWMutex

	// Best-effort: dpnid -> remote address summary recorded at connect time (when available).
	clientRemote map[uint32]remoteSummary

	// Some DP8 events do not include a DPNID. Keep the last seen remote summary so the
	// next CREATE_PLAYER can pick it up if needed.
	lastIndicate remoteSummary
}

type Stats struct {
	PlayersOnline int
	GamesHosted   int
}

const (
	maxPlayerOnlineAge = 12 * time.Hour
	playerSweepEvery   = 10 * time.Minute
)

func (e *Engine) Stats() Stats {
	var out Stats
	if e.players != nil {
		out.PlayersOnline = e.players.Count()
	} else {
		e.mu.RLock()
		out.PlayersOnline = len(e.clientRemote)
		e.mu.RUnlock()
	}
	if e.proto != nil {
		out.GamesHosted = e.proto.Stats().GamesHosted
	}
	return out
}

func dp8MsgName(id uint32) string {
	switch id {
	case dpnMsgIDConnectComplete:
		return "CONNECT_COMPLETE"
	case dpnMsgIDCreatePlayer:
		return "CREATE_PLAYER"
	case dpnMsgIDDestroyPlayer:
		return "DESTROY_PLAYER"
	case dpnMsgIDIndicateConnect:
		return "INDICATE_CONNECT"
	case dpnMsgIDReceive:
		return "RECEIVE"
	case dpnMsgIDTerminateSession:
		return "TERMINATE_SESSION"
	default:
		return "UNKNOWN"
	}
}

func summarizeLocation(loc string) (kind string, n int) {
	if loc == "" {
		return "", 0
	}
	kind = loc
	if i := strings.IndexByte(loc, '='); i >= 0 {
		kind = loc[:i]
	}
	kind = strings.TrimSpace(kind)
	if len(kind) > 32 {
		kind = kind[:32]
	}
	return kind, len(loc)
}

type hostDataSummary struct {
	itemCount int
	hasNew    bool
	hasDel    bool
	itemIDs   []string
}

func summarizeHostData(raw string) hostDataSummary {
	// Do not log raw HostData XML (it can contain user-entered strings).
	// We only log structural hints needed for lifecycle understanding.
	s := hostDataSummary{
		itemCount: strings.Count(raw, "<Item"),
		hasNew:    strings.Contains(raw, "<New"),
		hasDel:    strings.Contains(raw, "<Del"),
	}

	seen := map[string]struct{}{}
	needle := `ItemId="`
	for i := 0; i >= 0 && i < len(raw); {
		j := strings.Index(raw[i:], needle)
		if j < 0 {
			break
		}
		j += i + len(needle)
		k := strings.IndexByte(raw[j:], '"')
		if k < 0 {
			break
		}
		id := raw[j : j+k]
		if id != "" {
			seen[id] = struct{}{}
		}
		i = j + k + 1
	}

	for id := range seen {
		s.itemIDs = append(s.itemIDs, id)
	}
	sort.Strings(s.itemIDs)
	if len(s.itemIDs) > 8 {
		s.itemIDs = s.itemIDs[:8]
	}
	return s
}

type remoteSummary struct {
	ip   string
	port string

	// When hostname isn't an IP literal, do not log it. Only keep length for diagnostics.
	hostLen int
}

func parseRemoteFromDP8URL(url string) remoteSummary {
	// IDirectPlay8Address URLs typically include semicolon-separated key/values.
	// Avoid logging hostnames (often machine names). Prefer IP literals only.
	//
	// Example keys: hostname=..., port=...
	var out remoteSummary
	host := findDP8URLKV(url, "hostname")
	out.port = findDP8URLKV(url, "port")

	if host != "" && looksLikeIPv4(host) {
		out.ip = host
	} else if host != "" {
		out.hostLen = len(host)
	}
	if out.ip == "" {
		if ip, port := findIPv4AndPort(url); ip != "" {
			out.ip = ip
			if out.port == "" {
				out.port = port
			}
		}
	}
	return out
}

func findDP8URLKV(url, key string) string {
	needle := key + "="
	i := strings.Index(url, needle)
	if i < 0 {
		return ""
	}
	i += len(needle)
	j := strings.IndexAny(url[i:], ";& \t\r\n")
	if j < 0 {
		return url[i:]
	}
	return url[i : i+j]
}

func looksLikeIPv4(s string) bool {
	// Very small check: only digits and dots, and at least one dot.
	if strings.Count(s, ".") < 1 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || c == '.' {
			continue
		}
		return false
	}
	return true
}

func findIPv4AndPort(s string) (ip string, port string) {
	// Best-effort scan for an IPv4 literal anywhere in the URL.
	// If immediately followed by ":<digits>", treat that as a port.
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			continue
		}
		j := i
		dots := 0
		for j < len(s) {
			c := s[j]
			if c >= '0' && c <= '9' {
				j++
				continue
			}
			if c == '.' {
				dots++
				j++
				continue
			}
			break
		}
		cand := s[i:j]
		if dots < 3 || !looksLikeIPv4(cand) {
			i = j
			continue
		}
		ip = cand
		if j < len(s) && s[j] == ':' {
			k := j + 1
			for k < len(s) && s[k] >= '0' && s[k] <= '9' {
				k++
			}
			if k > j+1 {
				port = s[j+1 : k]
			}
		}
		return ip, port
	}
	return "", ""
}

func NewEngine(cfg config.Config, runID string, shim *dp8shim.Shim, log *packetlog.Logger, p *proto.Engine, players *state.PlayerStore) (*Engine, error) {
	if shim == nil {
		return nil, errors.New("dp8shim nil")
	}
	return &Engine{
		cfg:          cfg,
		runID:        runID,
		shim:         shim,
		log:          log,
		proto:        p,
		players:      players,
		buf:          make([]byte, 64*1024),
		outQ:         make(chan outMsg, 2048),
		clientRemote: make(map[uint32]remoteSummary),
	}, nil
}

func (e *Engine) Run(ctx context.Context) error {
	if e.log != nil {
		e.log.Log(packetlog.Record{
			RunID:      e.runID,
			Timestamp:  proto.NowTS(),
			Type:       "startup",
			ReplyMode:  "dp8shim",
			Experiment: "dp8-engine",
			Message: fmt.Sprintf(
				"dp8 engine start port=%d shim_queue_depth=%d",
				e.cfg.DP8Port,
				e.shim.QueueDepth(),
			),
		})
	}

	go e.sendWorker(ctx)
	go e.playerSweeper(ctx)

	for {
		select {
		case <-ctx.Done():
			return context.Canceled
		default:
		}

		evt, payload, ok, err := e.shim.PopEvent(e.buf)
		if err != nil {
			return err
		}
		if !ok {
			time.Sleep(5 * time.Millisecond)
			continue
		}

		if err := e.handleEvent(evt, payload); err != nil {
			return err
		}
	}
}

func (e *Engine) playerSweeper(ctx context.Context) {
	if e.players == nil {
		return
	}
	t := time.NewTicker(playerSweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			evicted := e.players.SweepEvict(now.UTC(), maxPlayerOnlineAge)
			for _, dpnid := range evicted {
				slog.Warn("player evicted due to max online age", "dpnid", fmt.Sprintf("0x%08x", dpnid), "max_age_h", 12)
			}
		}
	}
}

func (e *Engine) sendWorker(ctx context.Context) {
	const burstDelay = 2 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return
		case out := <-e.outQ:
			b := proto.MakeZText(out.payloadXML)
			if len(out.tail) > 0 {
				// Trailer is appended after the NUL terminator.
				b = append(b, out.tail...)
			}

			sendErr := e.shim.SendTo(out.dpnid, b, out.flags)
			tailNote := ""
			if len(out.tail) > 0 {
				tailNote = fmt.Sprintf(" tail=%d", len(out.tail))
			}
			if e.log != nil {
				e.log.Log(packetlog.Record{
					RunID:       e.runID,
					Timestamp:   proto.NowTS(),
					Type:        "dp8",
					Direction:   "out",
					Source:      "dpnid=0x00000000",
					Destination: fmt.Sprintf("dpnid=0x%08x", out.dpnid),
					Length:      len(b),
					ReplyMode:   "dp8shim",
					Tag:         out.tag,
					Experiment:  out.exp,
					Message:     fmt.Sprintf("err=%v payload=%s%s", sendErr, out.payloadXML, tailNote),
				})
			}
			time.Sleep(burstDelay)
		}
	}
}

func (e *Engine) handleEvent(evt dp8shim.Event, payload []byte) error {
	switch evt.MsgID {
	case dpnMsgIDCreatePlayer:
		var rs remoteSummary
		if len(payload) > 0 {
			rs = parseRemoteFromDP8URL(string(payload))
		}
		e.mu.Lock()
		if rs.ip == "" && rs.port == "" && rs.hostLen == 0 && (e.lastIndicate.ip != "" || e.lastIndicate.port != "" || e.lastIndicate.hostLen != 0) {
			rs = e.lastIndicate
		}
		if rs.ip != "" || rs.port != "" || rs.hostLen != 0 {
			e.clientRemote[evt.DPNID] = rs
		}
		e.mu.Unlock()
		if e.players != nil {
			e.players.Upsert(evt.DPNID, time.Now().UTC())
		}
		attrs := []any{"dpnid", fmt.Sprintf("0x%08x", evt.DPNID)}
		if rs.ip != "" {
			attrs = append(attrs, "remote_ip", rs.ip)
		}
		if rs.port != "" {
			attrs = append(attrs, "remote_port", rs.port)
		}
		if rs.ip == "" && rs.hostLen > 0 {
			attrs = append(attrs, "remote_host_len", rs.hostLen)
		}
		slog.Info("dp8 client connected", attrs...)
	case dpnMsgIDDestroyPlayer:
		e.mu.Lock()
		rs := e.clientRemote[evt.DPNID]
		delete(e.clientRemote, evt.DPNID)
		e.mu.Unlock()
		if e.players != nil && !e.players.Remove(evt.DPNID) {
			slog.Warn("dp8 client disconnected but not present in PlayerStore", "dpnid", fmt.Sprintf("0x%08x", evt.DPNID))
		}
		attrs := []any{"dpnid", fmt.Sprintf("0x%08x", evt.DPNID)}
		if rs.ip != "" {
			attrs = append(attrs, "remote_ip", rs.ip)
		}
		if rs.port != "" {
			attrs = append(attrs, "remote_port", rs.port)
		}
		if rs.ip == "" && rs.hostLen > 0 {
			attrs = append(attrs, "remote_host_len", rs.hostLen)
		}
		slog.Info("dp8 client disconnected", attrs...)
	case dpnMsgIDTerminateSession:
		slog.Info("dp8 session terminated", "dpnid", fmt.Sprintf("0x%08x", evt.DPNID))
	case dpnMsgIDIndicateConnect, dpnMsgIDConnectComplete:
		if evt.MsgID == dpnMsgIDIndicateConnect && len(payload) > 0 {
			e.mu.Lock()
			e.lastIndicate = parseRemoteFromDP8URL(string(payload))
			e.mu.Unlock()
		}
		// These are useful for troubleshooting but can be noisy; keep them at debug.
		slog.Debug("dp8 connect state", "msg", dp8MsgName(evt.MsgID), "dpnid", fmt.Sprintf("0x%08x", evt.DPNID))
	}

	rec := packetlog.Record{
		RunID:      e.runID,
		Timestamp:  proto.NowTS(),
		Type:       "dp8",
		Direction:  "in",
		Source:     fmt.Sprintf("dpnid=0x%08x", evt.DPNID),
		Length:     len(payload),
		ReplyMode:  "dp8shim",
		Experiment: "event",
		Message:    fmt.Sprintf("msg=%s msg_id=0x%08x flags=0x%08x ts_unix_ms=%d", dp8MsgName(evt.MsgID), evt.MsgID, evt.Flags, evt.TSUnixMS),
	}

	// App protocol: NUL-terminated XML-ish messages.
	if len(payload) > 0 && payload[0] == '<' {
		if e.players != nil && e.players.IsEvicted(evt.DPNID) {
			// Hard session cap: do not process or respond to app-protocol messages for evicted sessions.
			slog.Warn("dropping proto message from evicted player", "dpnid", fmt.Sprintf("0x%08x", evt.DPNID), "len", len(payload), "tag_hint", safeTagHint(payload))
			if e.log != nil {
				e.log.Log(rec)
			}
			return nil
		}
		msg, ok := proto.Parse(string(payload))
		if !ok {
			slog.Warn(
				"proto message parse failed",
				"dpnid", fmt.Sprintf("0x%08x", evt.DPNID),
				"msg", dp8MsgName(evt.MsgID),
				"len", len(payload),
				"tag_hint", safeTagHint(payload),
			)
		} else {
			rec.Tag = msg.Tag

			remoteAttrs := func(dpnid uint32) []any {
				e.mu.RLock()
				rs := e.clientRemote[dpnid]
				e.mu.RUnlock()
				attrs := make([]any, 0, 4)
				if rs.ip != "" {
					attrs = append(attrs, "remote_ip", rs.ip)
				}
				if rs.port != "" {
					attrs = append(attrs, "remote_port", rs.port)
				}
				if rs.ip == "" && rs.hostLen > 0 {
					attrs = append(attrs, "remote_host_len", rs.hostLen)
				}
				return attrs
			}

			// Structured lifecycle logging (sanitized; do not log raw strings).
			switch msg.Tag {
			case "Connect":
				attrs := []any{
					"dpnid", fmt.Sprintf("0x%08x", evt.DPNID),
					"cx", msg.Attrs["Cx"],
					"proto_ver", msg.Attrs["ProtoVer"],
				}
				attrs = append(attrs, remoteAttrs(evt.DPNID)...)
				slog.Info(
					"client connect request",
					attrs...,
				)
			case "HostData":
				hs := summarizeHostData(msg.Raw)
				if hs.itemCount == 0 {
					attrs := []any{
						"dpnid", fmt.Sprintf("0x%08x", evt.DPNID),
						"cx", msg.Attrs["Cx"],
					}
					attrs = append(attrs, remoteAttrs(evt.DPNID)...)
					slog.Warn("host state update with 0 items", attrs...)
				}
				attrs := []any{
					"dpnid", fmt.Sprintf("0x%08x", evt.DPNID),
					"cx", msg.Attrs["Cx"],
					"items", hs.itemCount,
					"has_new", hs.hasNew,
					"has_del", hs.hasDel,
					"item_ids", strings.Join(hs.itemIDs, ","),
				}
				attrs = append(attrs, remoteAttrs(evt.DPNID)...)
				slog.Info("host state update", attrs...)
			case "SetLoc":
				kind, n := summarizeLocation(msg.Attrs["Location"])
				attrs := []any{
					"dpnid", fmt.Sprintf("0x%08x", evt.DPNID),
					"cx", msg.Attrs["Cx"],
					"flags", msg.Attrs["Flags"],
					"kind", kind,
					"len", n,
				}
				attrs = append(attrs, remoteAttrs(evt.DPNID)...)
				slog.Info("location update", attrs...)
			case "HdrRow":
				attrs := []any{
					"dpnid", fmt.Sprintf("0x%08x", evt.DPNID),
					"cx", msg.Attrs["Cx"],
					"vid", msg.Attrs["Vid"],
				}
				attrs = append(attrs, remoteAttrs(evt.DPNID)...)
				slog.Info("header row request", attrs...)
			case "Page":
				attrs := []any{
					"dpnid", fmt.Sprintf("0x%08x", evt.DPNID),
					"cx", msg.Attrs["Cx"],
					"vid", msg.Attrs["Vid"],
					"page_no", msg.Attrs["PageNo"],
				}
				attrs = append(attrs, remoteAttrs(evt.DPNID)...)
				slog.Info("page request", attrs...)
			case "RowPg":
				// Details refresh for a selected row.
				attrs := []any{
					"dpnid", fmt.Sprintf("0x%08x", evt.DPNID),
					"cx", msg.Attrs["Cx"],
					"vid", msg.Attrs["Vid"],
					"rid", msg.Attrs["Rid"],
				}
				attrs = append(attrs, remoteAttrs(evt.DPNID)...)
				slog.Info("game details request", attrs...)
			default:
				// Unknown message: still handled by proto engine fallback to keep the UI moving,
				// but log at warn level for visibility.
				attrs := []any{
					"dpnid", fmt.Sprintf("0x%08x", evt.DPNID),
					"tag", msg.Tag,
					"attr_keys", strings.Join(sortedAttrKeys(msg.Attrs), ","),
				}
				attrs = append(attrs, remoteAttrs(evt.DPNID)...)
				slog.Warn("unrecognized proto message", attrs...)
			}

			// NDJSON (optional) keeps full attribute details for debugging.
			rec.Message = fmt.Sprintf("%s attrs=%v", rec.Message, msg.Attrs)

			outs := e.proto.Handle(time.Now().UTC(), evt.DPNID, msg)
			for _, out := range outs {
				switch out.Exp {
				case "send-fallback":
					attrs := []any{
						"dpnid", fmt.Sprintf("0x%08x", evt.DPNID),
						"tag", msg.Tag,
						"resp_tag", out.Tag,
					}
					attrs = append(attrs, remoteAttrs(evt.DPNID)...)
					slog.Warn("proto fallback response used", attrs...)
				case "send-rowpg-miss":
					attrs := []any{
						"dpnid", fmt.Sprintf("0x%08x", evt.DPNID),
						"vid", msg.Attrs["Vid"],
						"rid", msg.Attrs["Rid"],
					}
					attrs = append(attrs, remoteAttrs(evt.DPNID)...)
					slog.Warn("game details request for unknown rid", attrs...)
				}
			}
			for _, out := range outs {
				flags := dpnSendGuaranteed
				switch out.Tag {
				case "ConnectRes", "ConInfoRes", "ConnectEv":
					flags = dpnSendSyncGuaranteed
				}
				select {
				case e.outQ <- outMsg{
					dpnid:      evt.DPNID,
					tag:        out.Tag,
					exp:        out.Exp,
					payloadXML: out.PayloadXML,
					tail:       out.Tail,
					flags:      flags,
				}:
				default:
					slog.Warn(
						"dp8 send queue full; dropping outbound",
						"dpnid", fmt.Sprintf("0x%08x", evt.DPNID),
						"tag", out.Tag,
						"exp", out.Exp,
					)
					if e.log != nil {
						e.log.Log(packetlog.Record{
							RunID:      e.runID,
							Timestamp:  proto.NowTS(),
							Type:       "event",
							ReplyMode:  "dp8shim",
							Experiment: "sendq",
							Tag:        out.Tag,
							Message:    "drop: send queue full",
						})
					}
				}
			}
		}
	}

	if e.log != nil {
		e.log.Log(rec)
	}
	return nil
}

func sortedAttrKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func safeTagHint(payload []byte) string {
	// Extract a best-effort `<Tag` hint without logging any user-entered values.
	// Only returns characters from a safe set.
	if len(payload) == 0 || payload[0] != '<' {
		return ""
	}
	// Read until whitespace or '>' or '/'.
	i := 1
	for i < len(payload) && i < 64 {
		c := payload[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '>' || c == '/' {
			break
		}
		i++
	}
	if i <= 1 {
		return ""
	}
	raw := string(payload[1:i])
	if raw == "" {
		return ""
	}
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return ""
	}
	return raw
}
