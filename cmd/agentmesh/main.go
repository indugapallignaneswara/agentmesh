// Command agentmesh runs the coordination workspace server: a Streamable-HTTP
// MCP endpoint backed by Postgres (authoritative store) and, optionally, NATS
// JetStream (real-time fan-out).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/config"
	"github.com/indugapallignaneswara/agentmesh/internal/mcpserver"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// version is the server version reported over MCP. Override at build time with
// -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(cfg.LogLevel),
	}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Authoritative store.
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var st store.Store
	switch cfg.Store {
	case "memory":
		st = store.NewMemory()
		logger.Warn("using in-memory store; all state is lost on restart")
	default:
		pg, err := store.NewPostgres(initCtx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		st = pg
		logger.Info("connected to postgres")
	}
	defer st.Close()

	// Optional real-time bus.
	var b bus.Bus = bus.NewNoop()
	if cfg.NATSURL != "" {
		nb, err := bus.NewNATS(initCtx, cfg.NATSURL)
		if err != nil {
			return err
		}
		b = nb
		logger.Info("connected to nats", "url", cfg.NATSURL)
	} else {
		logger.Info("no NATS_URL set; running with no-op bus")
	}
	defer b.Close()

	svc := workspace.New(st, b,
		workspace.WithPresenceTTL(cfg.PresenceTTL),
		workspace.WithLogger(logger),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/mcp", mcpserver.Handler(svc, version))

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.HTTPAddr, "mcp_endpoint", "/mcp")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
