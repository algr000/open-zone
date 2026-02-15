//go:build windows

package dp8shim

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

type Shim struct {
	dll         *syscall.LazyDLL
	startServer *syscall.LazyProc
	stopServer  *syscall.LazyProc
	popEvent    *syscall.LazyProc
	sendTo      *syscall.LazyProc
	queueDepth  *syscall.LazyProc
}

type Event struct {
	MsgID    uint32
	DPNID    uint32
	DataLen  uint32
	Flags    uint32
	TSUnixMS uint64
}

func Load(path string) (*Shim, error) {
	d := syscall.NewLazyDLL(path)
	s := &Shim{
		dll:         d,
		startServer: d.NewProc("DP8_StartServer"),
		stopServer:  d.NewProc("DP8_StopServer"),
		popEvent:    d.NewProc("DP8_PopEvent"),
		sendTo:      d.NewProc("DP8_SendTo"),
		queueDepth:  d.NewProc("DP8_GetQueueDepth"),
	}
	// Force-load now so we fail fast.
	if err := d.Load(); err != nil {
		return nil, err
	}

	// Validate required exports up front so we don't panic later on LazyProc.Call().
	// Some older shim builds only export Start/Stop (or have different names).
	required := []struct {
		name string
		p    *syscall.LazyProc
	}{
		{"DP8_StartServer", s.startServer},
		{"DP8_StopServer", s.stopServer},
		{"DP8_PopEvent", s.popEvent},
		{"DP8_SendTo", s.sendTo},
	}
	var missing []string
	for _, r := range required {
		if r.p == nil {
			missing = append(missing, r.name)
			continue
		}
		if err := r.p.Find(); err != nil {
			missing = append(missing, r.name)
		}
	}
	// Optional export. If missing, QueueDepth() will return 0.
	if s.queueDepth != nil {
		_ = s.queueDepth.Find()
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf(
			"dp8shim %s is missing required exports: %s (rebuild dp8shim.dll from open-zone/dp8shim/dp8shim.cpp)",
			path,
			strings.Join(missing, ", "),
		)
	}

	return s, nil
}

func (s *Shim) StartServer(port uint16) error {
	if s == nil || s.startServer == nil {
		return errors.New("dp8shim not loaded")
	}
	// Windows can return a non-zero last-error even when HRESULT is ok; prefer HRESULT.
	r1, _, _ := s.startServer.Call(uintptr(port))
	hr := int32(r1)
	if hr != 0 {
		return fmt.Errorf("DP8_StartServer failed hr=0x%08x (port=%d)", uint32(hr), port)
	}
	return nil
}

func (s *Shim) StopServer() {
	if s == nil || s.stopServer == nil {
		return
	}
	_, _, _ = s.stopServer.Call()
}

func (s *Shim) PopEvent(buf []byte) (Event, []byte, bool, error) {
	if s == nil || s.popEvent == nil {
		return Event{}, nil, false, errors.New("dp8shim not loaded")
	}
	var evt Event
	var outPtr uintptr
	var outCap uintptr
	if len(buf) > 0 {
		outPtr = uintptr(unsafe.Pointer(&buf[0]))
		outCap = uintptr(len(buf))
	}
	r1, _, callErr := s.popEvent.Call(
		uintptr(unsafe.Pointer(&evt)),
		outPtr,
		outCap,
	)
	_ = callErr
	n := int32(r1)
	if n < 0 {
		return Event{}, nil, false, fmt.Errorf("DP8_PopEvent failed rc=%d", n)
	}
	if n == 0 {
		return Event{}, nil, false, nil
	}
	if evt.DataLen > uint32(len(buf)) {
		evt.DataLen = uint32(len(buf))
	}
	return evt, buf[:evt.DataLen], true, nil
}

func (s *Shim) SendTo(dpnid uint32, payload []byte, flags uint32) error {
	if s == nil || s.sendTo == nil {
		return errors.New("dp8shim not loaded")
	}
	if len(payload) == 0 {
		return errors.New("empty payload")
	}
	r1, _, _ := s.sendTo.Call(
		uintptr(dpnid),
		uintptr(unsafe.Pointer(&payload[0])),
		uintptr(uint32(len(payload))),
		uintptr(flags),
	)
	// HRESULT success includes S_OK (0) and DPNSUCCESS_PENDING (0x0015800e).
	// Failure is indicated by the high bit (0x80000000).
	hr := uint32(r1)
	if (hr & 0x80000000) != 0 {
		return fmt.Errorf("DP8_SendTo failed hr=0x%08x", hr)
	}
	return nil
}

func (s *Shim) QueueDepth() uint32 {
	if s == nil || s.queueDepth == nil {
		return 0
	}
	// queueDepth is optional; Find() can fail on older builds.
	if err := s.queueDepth.Find(); err != nil {
		return 0
	}
	r1, _, _ := s.queueDepth.Call()
	return uint32(r1)
}
