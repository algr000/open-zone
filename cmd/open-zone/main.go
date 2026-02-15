// Command open-zone runs the Open ZoneMatch server runtime.
//
// It starts:
// - a DirectPlay8 server via the native shim (transport),
// - the app-protocol handler loop, and
// - auxiliary HTTP endpoints like News and the AutoUpdate sink.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"open-zone/internal/autoupdate"
	"open-zone/internal/config"
	"open-zone/internal/dp8"
	"open-zone/internal/dp8shim"
	"open-zone/internal/news"
	"open-zone/internal/packetlog"
	"open-zone/internal/proto"
	"open-zone/internal/state"
)

func fatal(msg string, err error, attrs ...any) {
	args := make([]any, 0, 2+len(attrs))
	args = append(args, "err", err)
	args = append(args, attrs...)
	slog.Error(msg, args...)
	os.Exit(1)
}

func preflightPort(port int) error {
	addr := fmt.Sprintf(":%d", port)

	// Check TCP.
	tcpLn, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("port %d unavailable for tcp listen: %w", port, err)
	}
	_ = tcpLn.Close()

	// Check UDP.
	udpConn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("port %d unavailable for udp listen: %w", port, err)
	}
	_ = udpConn.Close()

	return nil
}

func main() {
	// Set up logging first so early failures are captured consistently.
	runID := proto.MakeRunID()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})).With("run_id", runID))

	cfg, err := config.Load()
	if err != nil {
		fatal("config load failed", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Shutdown watch: once a shutdown signal is received, allow a bounded window
	// for goroutines to exit cleanly before forcing termination.
	go func() {
		<-ctx.Done()
		t := time.NewTimer(60 * time.Second)
		defer t.Stop()
		<-t.C
		slog.Error("shutdown timed out after 60s, forcing exit")
		os.Exit(2)
	}()

	slog.Info(
		"starting open-zone",
		"dp8_port", cfg.DP8Port,
		"news_port", cfg.NewsPort,
		"autoupdate_port", cfg.AutoPort,
		"shim", cfg.ShimPath,
	)

	var pl *packetlog.Logger
	if cfg.DP8LogPath != "" {
		var err error
		pl, err = packetlog.New(cfg.DP8LogPath)
		if err != nil {
			fatal("open ndjson telemetry file failed", err, "path", cfg.DP8LogPath)
		}
		defer func() { _ = pl.Close() }()
		slog.Info("ndjson telemetry enabled", "path", cfg.DP8LogPath)
	} else {
		slog.Info("ndjson telemetry disabled (default); set OZ_DP8_NDJSON to enable")
	}

	// Best-effort: AutoUpdate uses port 80 with no explicit port field in DS configs.
	// We accept and immediately close to avoid long timeouts.
	if cfg.AutoPort != 0 {
		if err := autoupdate.StartSink(ctx, fmt.Sprintf(":%d", cfg.AutoPort), runID, pl); err != nil {
			slog.Warn("autoupdate sink disabled (listen failed)", "port", cfg.AutoPort, "err", err)
		}
	}

	// Fail fast with a clear message if dp8.port is already bound by another process.
	// dpnet will otherwise return a less obvious HRESULT from DP8_StartServer.
	if err := preflightPort(cfg.DP8Port); err != nil {
		fatal("dp8 port preflight failed", err, "port", cfg.DP8Port)
	}

	if _, err := os.Stat(cfg.ShimPath); err != nil {
		fatal("dp8shim not found (required)", err, "path", cfg.ShimPath)
	}
	shim, err := dp8shim.Load(cfg.ShimPath)
	if err != nil {
		fatal("dp8shim load failed", err, "path", cfg.ShimPath)
	}
	if err := shim.StartServer(uint16(cfg.DP8Port)); err != nil {
		fatal("dp8shim start failed", err, "port", cfg.DP8Port, "path", cfg.ShimPath)
	}
	defer shim.StopServer()
	slog.Info("dp8shim started DirectPlay8Server", "port", cfg.DP8Port, "path", cfg.ShimPath)

	hostStore := state.NewHostStore()
	playerStore := state.NewPlayerStore()
	protoEngine := proto.NewEngine(cfg.Proto, hostStore, playerStore)

	engine, err := dp8.NewEngine(cfg, runID, shim, pl, protoEngine, playerStore)
	if err != nil {
		fatal("dp8 engine init error", err)
	}

	_, err = news.Start(ctx, fmt.Sprintf(":%d", cfg.NewsPort), func() news.Data {
		return news.Data{
			Tagline:       cfg.ServerTagline,
			CreatedBy:     cfg.ServerCreatedBy,
			Version:       cfg.ServerVersion,
			ServerTime:    time.Now().UTC().Format(time.RFC3339),
			PlayersOnline: playerStore.Count(),
			GamesHosted:   hostStore.VisibleGamesCount(),
		}
	})
	if err != nil {
		fatal("news server start failed", err, "port", cfg.NewsPort)
	}

	if err := engine.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fatal("dp8 engine error", err)
	}
	slog.Info("shutdown requested")
}
