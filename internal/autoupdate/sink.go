package autoupdate

import (
	"context"
	"fmt"
	"net"
	"time"

	"open-zone/internal/packetlog"
	"open-zone/internal/proto"
)

// StartSink starts a best-effort TCP listener that accepts and immediately closes connections.
// This prevents long UI timeouts if the client attempts to contact an AutoUpdate endpoint.
func StartSink(ctx context.Context, addr string, runID string, log *packetlog.Logger) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if log != nil {
		log.Log(packetlog.Record{
			RunID:      runID,
			Timestamp:  proto.NowTS(),
			Type:       "startup",
			Experiment: "autoupdate-sink",
			Message:    fmt.Sprintf("listening addr=%s", addr),
		})
	}

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Close immediately; do not read/write any bytes.
			_ = c.SetDeadline(time.Now().Add(10 * time.Millisecond))
			_ = c.Close()
			if log != nil {
				log.Log(packetlog.Record{
					RunID:      runID,
					Timestamp:  proto.NowTS(),
					Type:       "autoupdate",
					Direction:  "in",
					Experiment: "autoupdate-sink",
					Message:    "accept+close",
				})
			}
		}
	}()

	return nil
}
