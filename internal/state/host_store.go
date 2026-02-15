package state

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GameRow is the "PageRes -> Row" representation:
// - Rid is the primary key used by the UI for Games browse (`Vid=101`).
// - Items maps header token -> string value (encoded as `<Row Token="Value" .../>`).
type GameRow struct {
	Rid   string
	Items map[string]string
}

type HostStore struct {
	mu    sync.Mutex
	hosts map[uint32]*hostSession

	// nextRid is a server-assigned, UI-friendly row id (fits in signed 32-bit).
	// Do not use DPNID directly: it is a uint32 and can exceed INT_MAX, which the client
	// parses into a signed int and will clamp/normalize (breaking Join).
	nextRid uint32
}

type hostSession struct {
	// last update time for debugging / eviction.
	lastUpdate time.Time

	// server-assigned row id (decimal string in payloads); must be <= INT_MAX.
	rid uint32

	// Free-form location string set by SetLoc.
	location string

	// observedRemoteIP is the best-effort remote/public IP as seen by the server.
	// This is preferred over client-published interface IPs for internet join.
	observedRemoteIP string

	// SERVER_ITEM_ID == 0: game/session metadata.
	server map[string]string

	// Player items keyed by ItemId string ("2", ...).
	players map[string]map[string]string
}

func NewHostStore() *HostStore {
	return &HostStore{
		hosts:   map[uint32]*hostSession{},
		nextRid: 1,
	}
}

func (s *HostStore) getOrCreateLocked(from uint32) *hostSession {
	h := s.hosts[from]
	if h == nil {
		h = &hostSession{
			server:  map[string]string{},
			players: map[string]map[string]string{},
		}
		// Assign a stable, small rid for this host session.
		// Keep it below INT_MAX to match the game's use of `int rowId`.
		if s.nextRid == 0 || s.nextRid >= 0x7fffffff {
			s.nextRid = 1
		}
		h.rid = s.nextRid
		s.nextRid++
		s.hosts[from] = h
	}
	return h
}

func (s *HostStore) SetLoc(from uint32, location string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.getOrCreateLocked(from)
	h.location = location
	h.lastUpdate = time.Now().UTC()
}

func (s *HostStore) SetObservedRemoteIP(from uint32, ip string) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.getOrCreateLocked(from)
	h.observedRemoteIP = ip
	h.lastUpdate = time.Now().UTC()
}

func (s *HostStore) ApplyHostData(from uint32, payload string) {
	// payload is the full raw `<HostData ...> ...` string (NUL trimmed).
	items := scanSelfClosingElements(payload, "Item")
	if len(items) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.getOrCreateLocked(from)
	h.lastUpdate = time.Now().UTC()

	for _, attrs := range items {
		itemID := attrs["ItemId"]
		if itemID == "" {
			// Delete-style payloads can omit ItemId and carry the identifier in `Num`:
			// `<Del><Item Num="0" /><Item Num="2" /></Del>`
			// In that case, ItemId is not present and the identifier is carried in `Num`.
			//
			// We only treat this as a delete when the element is "id-only" (no Str, no other attrs),
			// to avoid mixing this with other Item encodings like `<Item Num="i" Str="..."/>`.
			if num, ok := attrs["Num"]; ok && len(attrs) == 1 {
				if num == "0" {
					// Deleting the server item implies the hosted game is gone.
					h.server = map[string]string{}
				} else {
					delete(h.players, num)
				}
				if len(h.server) == 0 && len(h.players) == 0 {
					delete(s.hosts, from)
				}
			}
			continue
		}
		if itemID == "0" {
			// SERVER_ITEM_ID (game/session metadata)
			for k, v := range attrs {
				if k == "ItemId" {
					continue
				}
				h.server[k] = v
			}
			continue
		}
		p := h.players[itemID]
		if p == nil {
			p = map[string]string{}
			h.players[itemID] = p
		}
		for k, v := range attrs {
			if k == "ItemId" {
				continue
			}
			p[k] = v
		}
	}
}

// parseHostIpList splits the host-provided IP list into (primary, secondary).
//
// The on-wire format seen in practice is space-separated, but we also tolerate commas
// because some parts of the protocol use comma-separated lists for other fields.
func parseHostIpList(raw string) (ipAddr string, ip2 string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	ips := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
	if len(ips) == 0 {
		return "", ""
	}
	ipAddr = ips[0]
	ip2 = ips[0]
	for i := 1; i < len(ips); i++ {
		if ips[i] != "" && ips[i] != ipAddr {
			ip2 = ips[i]
			break
		}
	}
	return ipAddr, ip2
}

func hostAdvertisedIPs(server map[string]string) (ipAddr, ip2 string) {
	// Prefer explicit fields if present.
	ipAddr = strings.TrimSpace(server["IpAddr"])
	raw2 := strings.TrimSpace(server["Ip2"])
	if raw2 == "" && ipAddr == "" {
		return "", ""
	}

	// "Ip2" is sometimes a space-separated list of IPs. If IpAddr is absent, treat
	// the first IP as primary. If IpAddr is present, treat Ip2 as a secondary hint.
	p, s := parseHostIpList(raw2)
	switch {
	case ipAddr == "" && p != "":
		ipAddr = p
		ip2 = s
	case ipAddr != "" && raw2 != "":
		// Keep the first distinct secondary IP when possible.
		if s != "" && s != ipAddr {
			ip2 = s
		} else if p != "" && p != ipAddr {
			ip2 = p
		} else {
			ip2 = ipAddr
		}
	case ipAddr != "":
		ip2 = ipAddr
	}
	return ipAddr, ip2
}

// isPrivateIP returns true for loopback and RFC 1918 private addresses.
// Used so we never expose a private IP in browse rows when the host is reachable via a public observed IP.
func isPrivateIP(s string) bool {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil {
		return true // treat unparseable as private to avoid leaking
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

func hostBrowseIPs(h *hostSession) (ipAddr, ip2 string) {
	if h == nil {
		return "", ""
	}
	adv1, adv2 := hostAdvertisedIPs(h.server)

	// Prefer observed remote IP for the primary (server-seen address; works across NAT).
	if strings.TrimSpace(h.observedRemoteIP) != "" {
		ipAddr = h.observedRemoteIP
		// Use client-advertised secondary only if it is a public IP; otherwise other players would try to join a private IP and timeout.
		if adv1 != "" && adv1 != ipAddr && !isPrivateIP(adv1) {
			ip2 = adv1
		} else if adv2 != "" && adv2 != ipAddr && !isPrivateIP(adv2) {
			ip2 = adv2
		} else {
			ip2 = ipAddr
		}
		return ipAddr, ip2
	}

	// Fallback: use client-advertised IPs (e.g. same LAN).
	return adv1, adv2
}

func (s *HostStore) VisibleGamesCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := 0
	for _, h := range s.hosts {
		if h == nil {
			continue
		}
		// Require at least a game name OR map OR ip2; otherwise it's just a transient session.
		if h.server["GName"] == "" && h.server["Map"] == "" && h.server["Ip2"] == "" {
			continue
		}
		n++
	}
	return n
}

func (s *HostStore) GamesRows(maxRows int, headers []string) []GameRow {
	s.mu.Lock()
	defer s.mu.Unlock()

	// maxRows <= 0 means "no cap".

	// Deterministic order: sort by DPNID.
	keys := make([]uint32, 0, len(s.hosts))
	for k := range s.hosts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	outCap := len(keys)
	if maxRows > 0 {
		outCap = min(maxRows, len(keys))
	}
	out := make([]GameRow, 0, outCap)
	for _, k := range keys {
		if maxRows > 0 && len(out) >= maxRows {
			break
		}
		h := s.hosts[k]
		if h == nil {
			continue
		}
		// Require at least a game name OR map OR ip2; otherwise it's just a transient session.
		if h.server["GName"] == "" && h.server["Map"] == "" && h.server["Ip2"] == "" {
			continue
		}

		rid := strconv.FormatUint(uint64(h.rid), 10)
		items := map[string]string{}

		// Populate known columns strictly from observed HostData keys.
		items["Rid"] = rid
		copyIfNonEmpty(items, "GName", h.server["GName"])
		copyIfNonEmpty(items, "GameV", h.server["GameV"])
		copyIfNonEmpty(items, "Locale", h.server["Locale"])
		if ipAddr, ip2 := hostBrowseIPs(h); ipAddr != "" {
			items["IpAddr"] = ipAddr
			items["Ip2"] = ip2
		}
		copyIfNonEmpty(items, "SFlags", h.server["SFlags"])
		copyIfNonEmpty(items, "Flags", h.server["Flags"])
		copyIfNonEmpty(items, "Map", h.server["Map"])
		copyIfNonEmpty(items, "World", h.server["World"])
		copyIfNonEmpty(items, "NumP", h.server["NumP"])
		copyIfNonEmpty(items, "MaxP", h.server["MaxP"])
		copyIfNonEmpty(items, "Difficulty", h.server["Difficulty"])
		copyIfNonEmpty(items, "Time", h.server["Time"])
		copyIfNonEmpty(items, "TimeL", h.server["TimeL"])

		// Fill anything missing with empty string; encoder will output empty Str="".
		_ = headers

		out = append(out, GameRow{Rid: rid, Items: items})
	}
	return out
}

func (s *HostStore) RowByRid(rid string, headers []string) (GameRow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, h := range s.hosts {
		if h == nil {
			continue
		}
		if strconv.FormatUint(uint64(h.rid), 10) != rid {
			continue
		}

		items := map[string]string{}
		items["Rid"] = rid
		copyIfNonEmpty(items, "GName", h.server["GName"])
		copyIfNonEmpty(items, "GameV", h.server["GameV"])
		copyIfNonEmpty(items, "Locale", h.server["Locale"])
		if ipAddr, ip2 := hostBrowseIPs(h); ipAddr != "" {
			items["IpAddr"] = ipAddr
			items["Ip2"] = ip2
		}
		copyIfNonEmpty(items, "SFlags", h.server["SFlags"])
		copyIfNonEmpty(items, "Flags", h.server["Flags"])
		copyIfNonEmpty(items, "Map", h.server["Map"])
		copyIfNonEmpty(items, "World", h.server["World"])
		copyIfNonEmpty(items, "NumP", h.server["NumP"])
		copyIfNonEmpty(items, "MaxP", h.server["MaxP"])
		copyIfNonEmpty(items, "Difficulty", h.server["Difficulty"])
		copyIfNonEmpty(items, "Time", h.server["Time"])
		copyIfNonEmpty(items, "TimeL", h.server["TimeL"])

		_ = headers
		return GameRow{Rid: rid, Items: items}, true
	}
	return GameRow{}, false
}

func copyIfNonEmpty(dst map[string]string, k, v string) {
	if v == "" {
		return
	}
	dst[k] = v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// scanSelfClosingElements finds `<name ... />` elements and returns their attributes.
// This is intentionally narrow and ASCII-focused (matches on-wire payloads).
func scanSelfClosingElements(payload, name string) []map[string]string {
	needle := "<" + name
	out := []map[string]string{}

	for i := 0; i < len(payload); {
		j := strings.Index(payload[i:], needle)
		if j < 0 {
			break
		}
		j += i

		// Find the end of this tag.
		k := strings.IndexByte(payload[j:], '>')
		if k < 0 {
			break
		}
		k += j

		tag := payload[j+1 : k] // without '<' and '>'
		if !strings.HasPrefix(tag, name) {
			i = k + 1
			continue
		}

		// Only self-closing `<Item .../>` counts (ignore `<Item>...</Item>`).
		if !strings.Contains(tag, "/") {
			i = k + 1
			continue
		}

		attrs := parseAttrs(tag[len(name):])
		if len(attrs) > 0 {
			out = append(out, attrs)
		}
		i = k + 1
	}
	return out
}

func parseAttrs(s string) map[string]string {
	attrs := map[string]string{}
	rest := strings.TrimSpace(s)
	rest = strings.TrimSuffix(rest, "/")
	rest = strings.TrimSpace(rest)
	for rest != "" {
		eq := strings.Index(rest, "=\"")
		if eq < 0 {
			break
		}
		key := strings.TrimSpace(rest[:eq])
		rest = rest[eq+2:]
		q := strings.IndexByte(rest, '"')
		if q < 0 {
			break
		}
		val := rest[:q]
		rest = strings.TrimSpace(rest[q+1:])
		if key != "" {
			attrs[key] = val
		}
	}
	return attrs
}
