// Command agentmesh runs the coordination workspace server: a Streamable-HTTP
// MCP endpoint backed by Postgres (authoritative store) and, optionally, NATS
// JetStream (real-time fan-out).
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/config"
	"github.com/indugapallignaneswara/agentmesh/internal/dashboard"
	"github.com/indugapallignaneswara/agentmesh/internal/discovery"
	"github.com/indugapallignaneswara/agentmesh/internal/mcpserver"
	"github.com/indugapallignaneswara/agentmesh/internal/metrics"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/usage"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// version is the server version reported over MCP. Override at build time with
// -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	// `agentmesh token ...` is the credential admin CLI; everything else runs
	// the server.
	if len(os.Args) > 1 && os.Args[1] == "token" {
		if err := runTokenCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "agentmesh token: "+err.Error())
			os.Exit(1)
		}
		return
	}
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
		workspace.WithTaskLease(cfg.TaskLease),
		workspace.WithAckVisibility(cfg.AckVisibility),
		workspace.WithImplicitRooms(cfg.ImplicitWorkspaces),
		rateLimitOption(cfg.RateLimit),
		workspace.WithUsageBytesPerToken(cfg.UsageBytesPerToken),
		workspace.WithLogger(logger),
	)

	reg := metrics.New()

	// Usage metering (M6): measure-only, async, best-effort. Record never
	// blocks a tool call; drops are counted, never silent. Off = a true no-op
	// (no middleware installed at all).
	var rec *usage.Recorder
	if cfg.Usage {
		rec = usage.NewRecorder(st, usage.Options{
			OnDrop: reg.AddUsageDropped,
			Logger: logger,
		})
		defer rec.Close()
	}

	mux := http.NewServeMux()
	// Liveness: the process is up. Readiness: it can actually serve — the
	// store is reachable. Load balancers should gate traffic on /readyz.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := st.Ping(ctx); err != nil {
			http.Error(w, "store unavailable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	mux.Handle("GET /metrics", reg.Handler())
	mux.Handle("/mcp", mcpserver.HandlerWithObservability(svc, version, reg, rec))
	mux.Handle("/ui", dashboard.Handler(svc))
	mux.Handle("/ui/", dashboard.Handler(svc))
	mux.Handle(discovery.WellKnownPath, discovery.Handler(version, cfg.Auth))

	// Authentication: in token mode every endpoint except the health check and
	// the dashboard shell page requires a bearer token; the page itself is an
	// empty shell whose data calls (/ui/api) are gated.
	var handler http.Handler = mux
	if cfg.Auth != "off" {
		// Agents always keep opaque tokens: a machine has no interactive login,
		// which is exactly what opaque credentials are for.
		var authn auth.Authenticator = &auth.TokenAuthenticator{Store: st}

		if cfg.Auth == "oauth" {
			jwtAuth, err := auth.NewJWTAuthenticator(auth.OAuthConfig{
				Issuer:   cfg.OAuthIssuer,
				Audience: cfg.OAuthAudience,
				JWKSURL:  cfg.OAuthJWKSURL,
			})
			if err != nil {
				return err
			}
			// Humans arrive with IdP-issued JWTs, agents with amt_ tokens; try
			// the JWT path first, then fall back.
			authn = &auth.ChainAuthenticator{
				Authenticators: []auth.Authenticator{jwtAuth, authn},
			}
			// Publish RFC 9728 metadata and point the 401 challenge at it, so
			// spec-conformant MCP clients can discover where to get a token.
			mux.Handle("GET "+discovery.ProtectedResourcePath,
				discovery.ProtectedResourceHandler(cfg.OAuthAudience, cfg.OAuthIssuer))
			auth.ResourceMetadataURL = strings.TrimSuffix(cfg.OAuthAudience, "/mcp") + discovery.ProtectedResourcePath
			logger.Info("authentication enabled", "mode", "oauth",
				"issuer", cfg.OAuthIssuer, "audience", cfg.OAuthAudience)
		} else {
			logger.Info("authentication enabled", "mode", "token")
		}

		// Open endpoints: the agent card and the OAuth metadata (how clients
		// discover the security scheme in the first place), the health/readiness
		// probes and the metrics scrape — infrastructure must reach these
		// without a workspace credential. /metrics exposes no message content.
		handler = auth.Middleware(authn,
			"/healthz", "/readyz", "/metrics", "/ui",
			discovery.WellKnownPath, discovery.ProtectedResourcePath)(mux)
	} else {
		logger.Warn("authentication is OFF; anyone who can reach this address can join — use only on a trusted network")
	}

	// Count every HTTP response (outermost, so auth rejections are counted too).
	handler = reg.HTTPMiddleware(handler)

	tlsOn := cfg.TLSCert != "" && cfg.TLSKey != ""

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if tlsOn {
		// Modern baseline: TLS 1.2+ only.
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	// Bearer tokens over plaintext are recoverable by anyone on the path. If
	// auth is on without TLS, the operator must be terminating TLS upstream —
	// say so loudly, since silently shipping credentials in the clear is the
	// classic self-hosting mistake.
	if cfg.Auth != "off" && !tlsOn {
		logger.Warn("auth is enabled but TLS is not: bearer tokens will be sent in plaintext — " +
			"set AGENTMESH_TLS_CERT/AGENTMESH_TLS_KEY, or terminate TLS at a trusted reverse proxy")
	}

	errCh := make(chan error, 1)
	go func() {
		scheme := "http"
		if tlsOn {
			scheme = "https"
		}
		logger.Info("listening", "addr", cfg.HTTPAddr, "scheme", scheme, "mcp_endpoint", "/mcp")
		var err error
		if tlsOn {
			err = srv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
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

// rateLimitOption enables production rate-limit budgets, or a no-op when
// limiting is off.
func rateLimitOption(on bool) workspace.Option {
	if !on {
		return func(*workspace.Service) {}
	}
	return workspace.WithRateLimits(workspace.DefaultRateLimits())
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
