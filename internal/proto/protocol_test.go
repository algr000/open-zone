package proto

import (
	"strings"
	"testing"
	"time"

	"open-zone/internal/state"
)

func TestEngine_ConnectBundle(t *testing.T) {
	e := NewEngine(EngineConfig{Port: 2300}, nil, nil)
	outs := e.Handle(time.Unix(1700000000, 0).UTC(), 0, "", Msg{
		Tag:   "Connect",
		Attrs: map[string]string{"Cx": "0x123", "ProtoVer": "3.3"},
	})
	if len(outs) != 3 {
		t.Fatalf("outs=%d", len(outs))
	}
	if outs[0].Tag != "ConnectRes" || outs[1].Tag != "ConInfoRes" || outs[2].Tag != "ConnectEv" {
		t.Fatalf("tags=%v,%v,%v", outs[0].Tag, outs[1].Tag, outs[2].Tag)
	}
	if !strings.Contains(outs[1].PayloadXML, `Port="2300"`) {
		t.Fatalf("ConInfoRes payload=%s", outs[1].PayloadXML)
	}
}

func TestEngine_HdrRow_Vid101(t *testing.T) {
	e := NewEngine(EngineConfig{Port: 2300}, nil, nil)
	outs := e.Handle(time.Now().UTC(), 0, "", Msg{
		Tag:   "HdrRow",
		Attrs: map[string]string{"Cx": "0x65", "Vid": "101"},
	})
	if len(outs) != 1 || outs[0].Tag != "HdrRowRes" {
		t.Fatalf("outs=%v", outs)
	}
	p := outs[0].PayloadXML
	if !strings.Contains(p, `<HdrRowRes HR="0x00000000" Cx="0x65" Vid="101">`) {
		t.Fatalf("payload=%s", p)
	}
	if !strings.Contains(p, `<Hdrs`) || !strings.Contains(p, `H0="Rid"`) || !strings.Contains(p, `H15="InGame"`) {
		t.Fatalf("payload=%s", p)
	}
	// Ensure we don't inject a leading Num attribute.
	if strings.Contains(p, ` Num="`) {
		t.Fatalf("unexpected Num attr in header encoding: %s", p)
	}
}

func TestEngine_Page_OneRowFromHostStore(t *testing.T) {
	host := state.NewHostStore()
	e := NewEngine(EngineConfig{Port: 2300}, host, nil)

	// Seed host state via HostData so the page contains a real row.
	e.Handle(time.Now().UTC(), 0xabcdef01, "", Msg{
		Tag: "HostData",
		Attrs: map[string]string{
			"Cx": "0x0",
		},
		Raw: `<HostData><HostData><New>` +
			`<Item ItemId="0" GName="Test Game" Map="Test Map" Ip2="192.0.2.10 198.51.100.11" Locale="1033" GameV="1.11.0.1462" NumP="1" MaxP="8" />` +
			`</New></HostData></HostData>`,
	})

	outs := e.Handle(time.Now().UTC(), 0, "", Msg{
		Tag: "Page",
		Attrs: map[string]string{
			"Cx":     "0x0",
			"Vid":    "101",
			"PageNo": "0",
			"Num":    "0",
			"Str":    "",
		},
	})
	if len(outs) != 1 || outs[0].Tag != "PageRes" {
		t.Fatalf("outs=%v", outs)
	}
	p := outs[0].PayloadXML
	if !strings.Contains(p, `Count="1"`) {
		t.Fatalf("payload=%s", p)
	}
	if !strings.Contains(p, `<Row `) || !strings.Contains(p, `GName="Test Game"`) {
		t.Fatalf("payload=%s", p)
	}

	// Basic attribute-order sanity for the first few fields.
	iRid := strings.Index(p, `Rid="`)
	iGName := strings.Index(p, `GName="`)
	iGameV := strings.Index(p, `GameV="`)
	if iRid < 0 || iGName < 0 || iGameV < 0 {
		t.Fatalf("missing expected attrs: %s", p)
	}
	if !(iRid < iGName && iGName < iGameV) {
		t.Fatalf("attr order unexpected (Rid,GName,GameV): %d,%d,%d payload=%s", iRid, iGName, iGameV, p)
	}
}
