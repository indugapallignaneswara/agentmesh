// Package config loads runtime configuration from the environment with sane
// defaults for local development.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds all runtime settings.
type Config struct {
	// HTTPAddr is the listen address for the Streamable-HTTP MCP server.
	HTTPAddr string
	// Store selects the backing store: "postgres" (default, durable) or
	// "memory" (ephemeral, zero-dependency — for demos, local trials, and the
	// loopback validation). Memory state is lost on restart.
	Store string
	// Auth selects the authentication mode: "off" (default; trusted-network
	// mode, today's behaviour) or "token" (bearer tokens required on /mcp and
	// /ui/api; issue them with `agentmesh token create`). Token mode requires
	// the postgres store so credentials survive restarts and can be issued
	// out-of-process.
	Auth string
	// DatabaseURL is the Postgres DSN (authoritative store).
	DatabaseURL string
	// NATSURL is the NATS server URL. When empty, a no-op bus is used.
	NATSURL string
	// PresenceTTL is how recently a member must have been seen to be listed as
	// present.
	PresenceTTL time.Duration
	// TaskLease is how long a task claim is held before another agent may steal
	// it (work-stealing on a dead assignee).
	TaskLease time.Duration
	// ImplicitWorkspaces controls whether joining a non-existent room auto-
	// creates it. True (default) preserves the zero-setup demo; false requires
	// rooms to be created explicitly with room_create.
	ImplicitWorkspaces bool
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
}

// Load reads configuration from environment variables, applying defaults.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:           env("AGENTMESH_HTTP_ADDR", ":8080"),
		Store:              env("AGENTMESH_STORE", "postgres"),
		Auth:               env("AGENTMESH_AUTH", "off"),
		DatabaseURL:        env("AGENTMESH_DATABASE_URL", "postgres://agentmesh:agentmesh@localhost:5432/agentmesh?sslmode=disable"),
		NATSURL:            env("AGENTMESH_NATS_URL", ""),
		PresenceTTL:        60 * time.Second,
		TaskLease:          5 * time.Minute,
		ImplicitWorkspaces: envBool("AGENTMESH_IMPLICIT_WORKSPACES", true),
		LogLevel:           env("AGENTMESH_LOG_LEVEL", "info"),
	}
	switch cfg.Store {
	case "postgres", "memory":
	default:
		return Config{}, fmt.Errorf("AGENTMESH_STORE must be 'postgres' or 'memory', got %q", cfg.Store)
	}
	switch cfg.Auth {
	case "off", "token":
	default:
		return Config{}, fmt.Errorf("AGENTMESH_AUTH must be 'off' or 'token', got %q", cfg.Auth)
	}
	if cfg.Auth == "token" && cfg.Store != "postgres" {
		return Config{}, fmt.Errorf("AGENTMESH_AUTH=token requires AGENTMESH_STORE=postgres (tokens must survive restarts and be issuable out-of-process)")
	}
	if v := os.Getenv("AGENTMESH_PRESENCE_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("AGENTMESH_PRESENCE_TTL: %w", err)
		}
		cfg.PresenceTTL = d
	}
	if v := os.Getenv("AGENTMESH_TASK_LEASE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("AGENTMESH_TASK_LEASE: %w", err)
		}
		cfg.TaskLease = d
	}
	return cfg, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envBool reads a boolean env var; "false"/"0"/"no" are false, anything else
// non-empty is true, and unset yields def.
func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	switch v {
	case "false", "0", "no", "off":
		return false
	default:
		return true
	}
}
