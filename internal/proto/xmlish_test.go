package proto

import "testing"

func TestParse_TrimsNULAndParsesAttrs(t *testing.T) {
	in := "<Connect Cx=\"0x123\" ProtoVer=\"3.3\" />\x00\x00"
	m, ok := Parse(in)
	if !ok {
		t.Fatalf("Parse ok=false")
	}
	if m.Tag != "Connect" {
		t.Fatalf("tag=%q", m.Tag)
	}
	if m.Attrs["Cx"] != "0x123" {
		t.Fatalf("Cx=%q", m.Attrs["Cx"])
	}
	if m.Attrs["ProtoVer"] != "3.3" {
		t.Fatalf("ProtoVer=%q", m.Attrs["ProtoVer"])
	}
	if m.Raw == "" || m.Raw[len(m.Raw)-1] == 0 {
		t.Fatalf("Raw should be NUL-trimmed, got %q", m.Raw)
	}
}

func TestMakeZText_AppendsNULAndTrimsNewlines(t *testing.T) {
	b := MakeZText("<X />\r\n")
	if len(b) == 0 || b[len(b)-1] != 0 {
		t.Fatalf("expected NUL terminator")
	}
	if string(b[:len(b)-1]) != "<X />" {
		t.Fatalf("payload=%q", string(b[:len(b)-1]))
	}
}

