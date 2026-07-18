// Command agentiam is AgentMesh's Agent-IAM: an OAuth 2.1 authorization server
// that issues access tokens for agents. Its tokens are RS256 JWTs the AgentMesh
// resource server validates unchanged (see docs/agentiam.md).
//
// Usage:
//
//	agentiam serve                 run the authorization server
//	agentiam client register ...   register an agent client (prints secret once)
//	agentiam client list ...       list registered clients
//	agentiam client disable --id   revoke a client (reversible)
//
// Configuration (env):
//
//	AGENTIAM_ISSUER        public URL of this server (token `iss`); required
//	AGENTIAM_HTTP_ADDR     listen address (default :8090)
//	AGENTIAM_SIGNING_KEY   path to an RSA private-key PEM; if unset, an
//	                       ephemeral key is generated (demo only — tokens stop
//	                       validating on restart)
//	AGENTIAM_TOKEN_TTL     default access-token lifetime (default 15m)
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/iam"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agentiam: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("a command is required")
	}
	switch args[0] {
	case "serve":
		return runServe(args[1:])
	case "client":
		return runClient(args[1:])
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `agentiam — AgentMesh Agent-IAM (OAuth 2.1 authorization server for agents)

Usage:
  agentiam serve                          run the authorization server
  agentiam client register --workspace W --member M [--scopes s1,s2] [--ttl 15m]
  agentiam client list [--workspace W]
  agentiam client disable --id agt_...    (use --enable to re-enable)

Env: AGENTIAM_ISSUER (required), AGENTIAM_HTTP_ADDR (:8090),
     AGENTIAM_SIGNING_KEY (PEM path; ephemeral if unset),
     AGENTIAM_TOKEN_TTL (15m),
     AGENTIAM_DATABASE_URL (Postgres; in-memory if unset — demo only)
`)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadKeySet loads the signing key from AGENTIAM_SIGNING_KEY, or generates an
// ephemeral one for the demo (with a loud warning, mirroring the in-memory
// store warning on the server).
func loadKeySet(log *slog.Logger) (*iam.KeySet, error) {
	if path := os.Getenv("AGENTIAM_SIGNING_KEY"); path != "" {
		pemBytes, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read signing key: %w", err)
		}
		key, err := iam.LoadSigningKeyPEM(pemBytes)
		if err != nil {
			return nil, err
		}
		log.Info("loaded signing key", "kid", key.Kid)
		return iam.NewKeySet(key), nil
	}
	key, err := iam.GenerateSigningKey()
	if err != nil {
		return nil, err
	}
	log.Warn("no AGENTIAM_SIGNING_KEY set; generated an EPHEMERAL signing key — " +
		"tokens will stop validating when this process restarts. Set a PEM key for real use.")
	return iam.NewKeySet(key), nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", env("AGENTIAM_HTTP_ADDR", ":8090"), "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	issuer := os.Getenv("AGENTIAM_ISSUER")
	if issuer == "" {
		return fmt.Errorf("AGENTIAM_ISSUER is required (the public URL of this server)")
	}
	ttl := 15 * time.Minute
	if v := os.Getenv("AGENTIAM_TOKEN_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("AGENTIAM_TOKEN_TTL: %w", err)
		}
		ttl = d
	}

	keys, err := loadKeySet(log)
	if err != nil {
		return err
	}
	store, closeStore, err := openStore(context.Background(), log)
	if err != nil {
		return err
	}
	defer closeStore()

	srv, err := iam.NewServer(iam.Config{Issuer: issuer, DefaultTTL: ttl, Logger: log}, keys, store)
	if err != nil {
		return err
	}

	log.Info("agent-iam listening", "addr", *addr, "issuer", issuer,
		"jwks", issuer+"/.well-known/jwks.json", "default_ttl", ttl.String())
	return listenAndServe(*addr, srv.Handler())
}
