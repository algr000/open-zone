package state

import "testing"

func TestParseHostIpList(t *testing.T) {
	ip1, ip2 := parseHostIpList(" 192.0.2.10  198.51.100.11 ")
	if ip1 != "192.0.2.10" || ip2 != "198.51.100.11" {
		t.Fatalf("ip1=%q ip2=%q", ip1, ip2)
	}
	ip1, ip2 = parseHostIpList("203.0.113.9")
	if ip1 != "203.0.113.9" || ip2 != "203.0.113.9" {
		t.Fatalf("single ip ip1=%q ip2=%q", ip1, ip2)
	}
}

func TestHostStore_ApplyHostData_AndGamesRows(t *testing.T) {
	s := NewHostStore()
	from := uint32(0x11111111)

	// Minimal HostData with a server/session item (ItemId="0") carrying fields used by GamesRows.
	payload := `<HostData Cx="0x0"><HostData><New>` +
		`<Item ItemId="0" GName="Test Game" Map="Test Map" Ip2="192.0.2.10 198.51.100.11" Locale="1033" GameV="1.11.0.1462" NumP="1" MaxP="8" />` +
		`</New></HostData></HostData>`
	s.ApplyHostData(from, payload)

	rows := s.GamesRows(1, nil)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	r := rows[0]
	if r.Rid == "" || r.Rid == "0" {
		t.Fatalf("rid=%q", r.Rid)
	}
	if r.Items["GName"] != "Test Game" {
		t.Fatalf("GName=%q", r.Items["GName"])
	}
	if r.Items["Map"] != "Test Map" {
		t.Fatalf("Map=%q", r.Items["Map"])
	}
	if r.Items["IpAddr"] != "192.0.2.10" || r.Items["Ip2"] != "198.51.100.11" {
		t.Fatalf("IpAddr=%q Ip2=%q", r.Items["IpAddr"], r.Items["Ip2"])
	}
	if got := s.VisibleGamesCount(); got != 1 {
		t.Fatalf("VisibleGamesCount=%d", got)
	}
}

func TestHostStore_ObservedIPOverridesPrivateIp2(t *testing.T) {
	// When the server observes a public IP for the host, browse rows must not expose private IPs from HostData (e.g. 172.x).
	// Otherwise remote joiners would try to connect to the host's private IP and timeout.
	s := NewHostStore()
	from := uint32(0x33333333)
	s.SetObservedRemoteIP(from, "203.0.113.1")
	payload := `<HostData Cx="0x0"><HostData><New>` +
		`<Item ItemId="0" GName="LAN Host" Map="Test" Ip2="172.25.96.1  10.0.0.186" Locale="1033" GameV="1.11.0.1462" NumP="1" MaxP="8" />` +
		`</New></HostData></HostData>`
	s.ApplyHostData(from, payload)
	rows := s.GamesRows(1, nil)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	if got := rows[0].Items["IpAddr"]; got != "203.0.113.1" {
		t.Fatalf("IpAddr=%q want observed public", got)
	}
	if got := rows[0].Items["Ip2"]; got != "203.0.113.1" {
		t.Fatalf("Ip2=%q want observed public (no private IP in row)", got)
	}
}

func TestHostStore_DeleteStyleRemovesHost(t *testing.T) {
	s := NewHostStore()
	from := uint32(0x22222222)

	payload := `<HostData><HostData><New>` +
		`<Item ItemId="0" GName="x" Map="y" Ip2="203.0.113.10" />` +
		`</New></HostData></HostData>`
	s.ApplyHostData(from, payload)
	if got := len(s.GamesRows(10, nil)); got != 1 {
		t.Fatalf("pre-delete rows=%d", got)
	}

	// Delete-style payload: server item + player item.
	s.ApplyHostData(from, `<Del><Item Num="0" /><Item Num="2" /></Del>`)
	if got := len(s.GamesRows(10, nil)); got != 0 {
		t.Fatalf("post-delete rows=%d", got)
	}
	if got := s.VisibleGamesCount(); got != 0 {
		t.Fatalf("VisibleGamesCount=%d", got)
	}
}
