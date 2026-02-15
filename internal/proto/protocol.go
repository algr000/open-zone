package proto

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"open-zone/internal/state"
)

type Outbound struct {
	Tag        string
	PayloadXML string // without trailing NUL
	Tail       []byte // optional bytes appended after the trailing NUL
	Exp        string
}

type EngineConfig struct {
	Port int
}

type Engine struct {
	port int

	host    *state.HostStore
	players *state.PlayerStore
}

type Stats struct {
	PlayersOnline int // DP8 sessions, not accounts.
	GamesHosted   int
}

func NewEngine(cfg EngineConfig, host *state.HostStore, players *state.PlayerStore) *Engine {
	return &Engine{
		port:    cfg.Port,
		host:    host,
		players: players,
	}
}

func (p *Engine) Stats() Stats {
	var out Stats
	if p.host != nil {
		out.GamesHosted = p.host.VisibleGamesCount()
	}
	if p.players != nil {
		out.PlayersOnline = p.players.Count()
	}
	return out
}

func (p *Engine) Handle(now time.Time, fromDPNID uint32, in Msg) []Outbound {
	switch in.Tag {
	case "Connect":
		return p.handleConnect(now, in)
	case "HdrRow":
		return p.handleHdrRow(in)
	case "Page":
		return p.handlePage(in)
	case "RowPg":
		return p.handleRowPg(in)
	case "HostData":
		return p.handleHostData(fromDPNID, in)
	case "SetLoc":
		return p.handleSetLoc(fromDPNID, in)
	default:
		return p.handleFallback(in)
	}
}

func (p *Engine) handleRowPg(in Msg) []Outbound {
	// Client sends `RowPg Vid="301" Rid="<rowId>" Num="0" Str="" Cx="0x16"`.
	// This is a details refresh step prior to any transport-level join.
	//
	// Requirement: return `HR=0` and a row payload for the requested Rid, otherwise the UI
	// treats the selection as unavailable.
	cx := in.Attrs["Cx"]
	if cx == "" {
		cx = "0x0"
	}
	vid := in.Attrs["Vid"]
	if vid == "" {
		vid = "0"
	}
	rid := in.Attrs["Rid"]
	if rid == "" {
		rid = "0"
	}
	num := in.Attrs["Num"]
	if num == "" {
		num = "0"
	}
	str := in.Attrs["Str"]

	headers := headerTokensForView(vid)
	if p.host == nil {
		out := fmt.Sprintf(`<RowPgRes HR="0x80004005" Cx="%s" Vid="%s" Rid="%s" Num="%s" Str="%s" Count="0" />`,
			cx, vid, rid, xmlEscapeAttr(num), xmlEscapeAttr(str),
		)
		return []Outbound{{Tag: "RowPgRes", PayloadXML: out, Exp: "send-safe-fail"}}
	}

	row, ok := p.host.RowByRid(rid, headers)
	if !ok {
		// Not found: return success with 0 rows (client will show "no longer available").
		out := fmt.Sprintf(`<RowPgRes HR="0x00000000" Cx="%s" Vid="%s" Rid="%s" Num="%s" Str="%s" Count="0" />`,
			cx, vid, rid, xmlEscapeAttr(num), xmlEscapeAttr(str),
		)
		return []Outbound{{Tag: "RowPgRes", PayloadXML: out, Exp: "send-rowpg-miss"}}
	}

	// IMPORTANT: mirror the same "Row as attributes" encoding as PageRes.
	rowAttrs := make([]string, 0, len(headers))
	for i, h := range headers {
		val := row.Items[h]
		if i == 0 && val == "" {
			val = row.Rid
		}
		rowAttrs = append(rowAttrs, fmt.Sprintf(`%s="%s"`, h, xmlEscapeAttr(val)))
	}

	out := fmt.Sprintf(
		`<RowPgRes HR="0x00000000" Cx="%s" Vid="%s" Rid="%s" Num="%s" Str="%s" Count="1"><Row %s /></RowPgRes>`,
		cx, vid, rid, xmlEscapeAttr(num), xmlEscapeAttr(str), strings.Join(rowAttrs, " "),
	)
	return []Outbound{{Tag: "RowPgRes", PayloadXML: out, Exp: "send-rowpg-hit"}}
}

func (p *Engine) handleConnect(now time.Time, in Msg) []Outbound {
	cx := in.Attrs["Cx"]
	if cx == "" {
		cx = "0x0"
	}
	pv := in.Attrs["ProtoVer"]
	if pv == "" {
		pv = "3.3"
	}

	t2000 := SecondsSince2000UTC(now.UTC())
	siid := uint32(now.UnixNano())
	lid := uint32(now.UnixNano() >> 32)
	randv := uint32(now.UnixNano() ^ int64(now.Unix()))
	appGuid := "77E2D9C2-504E-459F-8416-0848130BBE1E"
	locale := "0x0409"

	msg1 := fmt.Sprintf(
		`<ConnectRes HR="0x00000000" Cx="%s" ProtoVer="%s" SIId="0x%08x" LId="0x%08x" ConSId="0x%08x" ConLId="0x%08x" Time="%d" Locale="%s" Random="0x%08x" AppGuid="%s" />`,
		cx, pv, siid, lid, siid, lid, t2000, locale, randv, appGuid,
	)
	msg2 := fmt.Sprintf(`<ConInfoRes HR="0x00000000" Cx="%s" IpAddr="127.0.0.1" Port="%d" />`, cx, p.port)
	msg3 := fmt.Sprintf(`<ConnectEv HR="0x00000000" Cx="%s" Time="%d" />`, cx, t2000)

	return []Outbound{
		{Tag: "ConnectRes", PayloadXML: msg1, Exp: "send"},
		{Tag: "ConInfoRes", PayloadXML: msg2, Exp: "send"},
		{Tag: "ConnectEv", PayloadXML: msg3, Exp: "send"},
	}
}

func (p *Engine) handleSetLoc(fromDPNID uint32, in Msg) []Outbound {
	// Hosting flow emits `<SetLoc ... Location="STAGING AREA=..."/>` prior to HostData.
	if p.host != nil {
		p.host.SetLoc(fromDPNID, in.Attrs["Location"])
	}

	cx := in.Attrs["Cx"]
	if cx == "" {
		cx = "0x0"
	}
	flags := in.Attrs["Flags"]
	loc := in.Attrs["Location"]
	out := fmt.Sprintf(`<SetLocRes HR="0x00000000" Cx="%s" Flags="%s" Location="%s" />`, cx, xmlEscapeAttr(flags), xmlEscapeAttr(loc))
	return []Outbound{{Tag: "SetLocRes", PayloadXML: out, Exp: "send-host"}}
}

func (p *Engine) handleHostData(fromDPNID uint32, in Msg) []Outbound {
	// `<HostData ...>` carries nested `<Item .../>` elements describing a session (ItemId="0")
	// and players (other ItemId values).
	if p.host != nil {
		p.host.ApplyHostData(fromDPNID, in.Raw)
	}

	cx := in.Attrs["Cx"]
	if cx == "" {
		cx = "0x0"
	}
	out := fmt.Sprintf(`<HostDataRes HR="0x00000000" Cx="%s" />`, cx)
	return []Outbound{{Tag: "HostDataRes", PayloadXML: out, Exp: "send-host"}}
}

func (p *Engine) handleHdrRow(in Msg) []Outbound {
	cx := in.Attrs["Cx"]
	if cx == "" {
		cx = "0x0"
	}
	vid := in.Attrs["Vid"]
	if vid == "" {
		vid = "0"
	}

	// NOTE: the client requests header rows for many views in a burst on entering the Games UI.
	// Responding consistently across view ids reduces partial-initialization states.

	headers := headerTokensForView(vid)

	// Header encoding: `<Hdrs H0="Rid" H1="GName" ... H15="InGame" />` (no Num attr).
	var b strings.Builder
	fmt.Fprintf(&b, `<HdrRowRes HR="0x00000000" Cx="%s" Vid="%s">`, cx, vid)
	b.WriteString(`<Hdrs`)
	for i, h := range headers {
		fmt.Fprintf(&b, ` H%d="%s"`, i, xmlEscapeAttr(h))
	}
	b.WriteString(` /></HdrRowRes>`)
	return []Outbound{{Tag: "HdrRowRes", PayloadXML: b.String(), Exp: "send"}}
}

func (p *Engine) handlePage(in Msg) []Outbound {
	cx := in.Attrs["Cx"]
	if cx == "" {
		cx = "0x0"
	}
	vid := in.Attrs["Vid"]
	if vid == "" {
		vid = "0"
	}
	pageNo := in.Attrs["PageNo"]
	if pageNo == "" {
		pageNo = "0"
	}
	num := in.Attrs["Num"]
	if num == "" {
		num = "0"
	}
	str := in.Attrs["Str"]

	// Minimal empty page response:
	// - Count=0 rows, with view meta populated so the UI can stop showing refresh text.
	// Non-empty page response:
	// - Rows are encoded as repeated `<Row .../>` elements under `<PageRes ...>`.
	// - Per-row values are carried as attributes on the `<Row .../>` element.
	headers := headerTokensForView(vid)

	rows := []state.GameRow(nil)
	if p.host != nil && vid == "101" {
		// Return all hosted rows (no artificial cap).
		rows = p.host.GamesRows(0, headers)
	}

	if len(rows) == 0 {
		out := fmt.Sprintf(
			`<PageRes HR="0x00000000" Cx="%s" Vid="%s" ViewId="%s" PageNo="%s" PageNumber="%s" VType="0" ViewType="0" VIdx="0" ViewIndex="0" VTotal="0" ViewTotal="0" Count="0" Num="%s" Str="%s" />`,
			cx, vid, vid, pageNo, pageNo, xmlEscapeAttr(num), xmlEscapeAttr(str),
		)
		return []Outbound{{Tag: "PageRes", PayloadXML: out, Exp: "send"}}
	}

	var b strings.Builder
	// Tag tokens:
	// - The client expects rows under `<PageRes ...>` encoded as `<Row .../>`.
	//
	// Practical conclusion:
	// - For the Games list view (`Vid=101`), rows must be encoded as repeated `<Row ...>...</Row>`
	//   elements directly under `<PageRes ...>`. Wrapping in `<MPageRes>` (or `<List>`) has caused
	//   regressions where the UI renders 0 rows or fails to populate row string arrays.
	fmt.Fprintf(&b, `<PageRes HR="0x00000000" Cx="%s" Vid="%s" ViewId="%s" PageNo="%s" PageNumber="%s" VType="0" ViewType="0" VIdx="0" ViewIndex="0" VTotal="0" ViewTotal="0" Count="%d" Num="%s" Str="%s">`,
		cx, vid, vid, pageNo, pageNo, len(rows), xmlEscapeAttr(num), xmlEscapeAttr(str),
	)

	for _, r := range rows {
		// IMPORTANT:
		// - emit EXACTLY `len(headers)` attributes
		// - keep attribute order matching `headerTokensForView(vid)` order
		// - do NOT include extra attrs like `Num="16"` (it shifts columns)
		rowAttrs := make([]string, 0, len(headers))
		for i, h := range headers {
			val := r.Items[h]
			if i == 0 && val == "" {
				val = r.Rid
			}
			rowAttrs = append(rowAttrs, fmt.Sprintf(`%s="%s"`, h, xmlEscapeAttr(val)))
		}

		fmt.Fprintf(&b, `<Row %s />`, strings.Join(rowAttrs, " "))
	}
	b.WriteString(`</PageRes>`)

	return []Outbound{{Tag: "PageRes", PayloadXML: b.String(), Exp: "send-page-rows"}}
}

func headerTokensForView(vid string) []string {
	switch vid {
	case "501":
		// Player listing view (GAMEVIEW_GAME_PLAYERS)
		return []string{"User", "PTeam", "PChar", "PLev"}
	default:
		// Game listing + details views.
		// Keep the tokens the UI explicitly checks for. Extras should be harmless if ignored.
		return []string{
			"Rid",
			"GName",
			"GameV",
			"Locale",
			"IpAddr",
			"Ip2",
			"SFlags",
			"Flags",
			"Map",
			"World",
			"NumP",
			"MaxP",
			"Difficulty",
			"Time",
			"TimeL",
			"InGame",
		}
	}
}

func xmlEscapeAttr(s string) string {
	// This is not a full XML serializer; it is the minimal escaping required to keep
	// our attribute values well-formed for basic parsers.
	repl := strings.NewReplacer(
		"&", "&amp;",
		"\"", "&quot;",
		"<", "&lt;",
		">", "&gt;",
	)
	return repl.Replace(s)
}

func xmlEscapeText(s string) string {
	// Minimal escaping for element text content.
	repl := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return repl.Replace(s)
}

func (p *Engine) handleFallback(in Msg) []Outbound {
	// Generic fallback: many messages appear to follow request `<X .../>`
	// and response `<XRes .../>`. Responding avoids hard stalls and often prevents
	// UI-side error paths for message families not explicitly handled.
	if in.Tag == "" || strings.ContainsAny(in.Tag, "<>\"' /\\") {
		return nil
	}
	attrs := make([]string, 0, len(in.Attrs))
	for k, v := range in.Attrs {
		attrs = append(attrs, fmt.Sprintf(`%s="%s"`, k, v))
	}
	sort.Strings(attrs) // deterministic logs
	parts := make([]string, 0, len(attrs)+1)
	parts = append(parts, `HR="0x00000000"`)
	parts = append(parts, attrs...)
	out := fmt.Sprintf("<%sRes %s />", in.Tag, strings.Join(parts, " "))
	return []Outbound{{Tag: in.Tag + "Res", PayloadXML: out, Exp: "send-fallback"}}
}
