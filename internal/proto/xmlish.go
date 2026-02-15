package proto

import "strings"

type Msg struct {
	Tag   string
	Attrs map[string]string

	// Raw is the full inbound payload as text (NULs trimmed), not just the first element.
	// We keep this so handlers can parse nested tags (ex HostData).
	Raw string
}

func Parse(s string) (Msg, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s[0] != '<' {
		return Msg{}, false
	}
	// Drop any trailing NULs (client uses NUL termination).
	s = strings.TrimRight(s, "\x00")

	end := strings.IndexByte(s, '>')
	if end < 0 {
		return Msg{}, false
	}
	head := strings.TrimSpace(s[1:end])
	if head == "" {
		return Msg{}, false
	}
	head = strings.TrimSuffix(head, "/")
	head = strings.TrimSpace(head)
	if head == "" {
		return Msg{}, false
	}

	tag := head
	if i := strings.IndexAny(head, " \t\r\n"); i >= 0 {
		tag = head[:i]
		head = head[i+1:]
	} else {
		head = ""
	}
	if tag == "" {
		return Msg{}, false
	}

	attrs := map[string]string{}
	rest := strings.TrimSpace(head)
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
	return Msg{Tag: tag, Attrs: attrs, Raw: s}, true
}

func MakeZText(s string) []byte {
	// NUL-terminated UTF-8 (matches observed inbound messages).
	//
	// Important: do NOT append '\n'. Protocol frames are
	// `... />\0` (no newline). Adding a newline can change parsing behavior.
	s = strings.TrimRight(s, "\r\n")
	b := []byte(s)
	return append(b, 0)
}
