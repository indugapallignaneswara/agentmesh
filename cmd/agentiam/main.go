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
//	agentiam token revoke ...      revoke one issued token (RFC 7009)
//
// Configuration (env):
//
//	AGENTIAM_ISSUER        public URL of this server (token `iss`); required
//	AGENTIAM_HTTP_ADDR     listen address (default :8090)
//	AGENTIAM_SIGNING_KEY   path to an RSA private-key PEM; if unset, an
//	                       ephemeral key is generated (demo only — tokens stop
//	                       validating on restart)
//	AGENTIAM_TOKEN_TTL     default access-token lifetime (default 15m)
//	AGENTIAM_SUBJECT_ISSUERS
//	                       comma-separated issuer=jwks_url pairs naming the
//	                       human IdPs trusted as RFC 8693 subject_token
//	                       sources, e.g.
//	                       "https://idp.corp=https://idp.corp/jwks"; if
//	                       unset, the token-exchange (delegation) grant is
//	                       disabled
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
	case "token":
		return runToken(args[1:])
	case "audit":
		return runAudit(args[1:])
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
  agentiam token revoke --client ID --secret SECRET --token JWT [--endpoint URL]
  agentiam audit query [--client ID] [--workspace W] [--type T] [--from RFC3339] [--to RFC3339] [--limit N] [--jsonl] [--endpoint URL] [--admin-token TOKEN]

Env: AGENTIAM_ISSUER (required), AGENTIAM_HTTP_ADDR (:8090),
     AGENTIAM_SIGNING_KEY (PEM path; ephemeral if unset),
     AGENTIAM_TOKEN_TTL (15m),
     AGENTIAM_DATABASE_URL (Postgres; in-memory if unset — demo only),
     AGENTIAM_SUBJECT_ISSUERS (issuer=jwks_url,... — human IdPs trusted for
     RFC 8693 delegation; delegation disabled if unset)
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

	var subjectIssuers []iam.TrustedIssuer
	if v := os.Getenv("AGENTIAM_SUBJECT_ISSUERS"); v != "" {
		parsed, err := iam.ParseTrustedIssuers(v)
		if err != nil {
			return fmt.Errorf("AGENTIAM_SUBJECT_ISSUERS: %w", err)
		}
		subjectIssuers = parsed
	}
	if len(subjectIssuers) > 0 {
		log.Info("delegation enabled", "trusted_subject_issuers", len(subjectIssuers))
	} else {
		log.Info("delegation disabled (no AGENTIAM_SUBJECT_ISSUERS configured)")
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
	revocations, closeRevocations, err := openRevocations(context.Background(), log)
	if err != nil {
		return err
	}
	defer closeRevocations()
	audit, closeAudit, err := openAudit(context.Background(), log)
	if err != nil {
		return err
	}
	defer closeAudit()

	adminToken := os.Getenv("AGENTIAM_ADMIN_TOKEN")
	if adminToken == "" {
		log.Warn("no AGENTIAM_ADMIN_TOKEN set; the audit API and admin console are DISABLED " +
			"(they expose the whole fleet's activity and must be gated by a token)")
	}

	srv, err := iam.NewServer(iam.Config{
		Issuer:         issuer,
		DefaultTTL:     ttl,
		Logger:         log,
		SubjectIssuers: subjectIssuers,
		Revocations:    revocations,
		Audit:          audit,
		AdminToken:     adminToken,
	}, keys, store)
	if err != nil {
		return err
	}

	log.Info("agent-iam listening", "addr", *addr, "issuer", issuer,
		"jwks", issuer+"/.well-known/jwks.json", "default_ttl", ttl.String(),
		"admin_console", adminToken != "")
	return listenAndServe(*addr, srv.Handler())
}

// openAudit returns the AuditStore backing the audit trail: Postgres when
// AGENTIAM_DATABASE_URL is set, otherwise in-memory (resets on restart).
func openAudit(ctx context.Context, log *slog.Logger) (iam.AuditStore, func(), error) {
	if dsn := os.Getenv("AGENTIAM_DATABASE_URL"); dsn != "" {
		pg, err := iam.NewPGAuditStore(ctx, dsn)
		if err != nil {
			return nil, func() {}, err
		}
		return pg, pg.Close, nil
	}
	log.Warn("no AGENTIAM_DATABASE_URL set; using in-memory audit store — " +
		"the audit trail is lost on restart. Set a database URL for real use.")
	return iam.NewMemAuditStore(), func() {}, nil
}

// openRevocations returns the RevocationStore backing /revoke and
// /revocations: Postgres when AGENTIAM_DATABASE_URL is set (same database as
// the client store), otherwise in-memory — revocations then reset on restart,
// which is acceptable only for the demo.
func openRevocations(ctx context.Context, log *slog.Logger) (iam.RevocationStore, func(), error) {
	if dsn := os.Getenv("AGENTIAM_DATABASE_URL"); dsn != "" {
		pg, err := iam.NewPGRevocationStore(ctx, dsn)
		if err != nil {
			return nil, func() {}, err
		}
		return pg, pg.Close, nil
	}
	log.Warn("no AGENTIAM_DATABASE_URL set; using in-memory revocation store — " +
		"revocations are lost on restart. Set a database URL for real use.")
	return iam.NewMemRevocationStore(), func() {}, nil
}
