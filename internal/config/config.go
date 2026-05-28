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
	// DatabaseURL is the Postgres DSN (authoritative store).
	DatabaseURL string
	// NATSURL is the NATS server URL. When empty, a no-op bus is used.
	NATSURL string
	// PresenceTTL is how recently a member must have been seen to be listed as
	// present.
	PresenceTTL time.Duration
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
}

// Load reads configuration from environment variables, applying defaults.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:    env("AGENTMESH_HTTP_ADDR", ":8080"),
		DatabaseURL: env("AGENTMESH_DATABASE_URL", "postgres://agentmesh:agentmesh@localhost:5432/agentmesh?sslmode=disable"),
		NATSURL:     env("AGENTMESH_NATS_URL", ""),
		PresenceTTL: 60 * time.Second,
		LogLevel:    env("AGENTMESH_LOG_LEVEL", "info"),
	}
	if v := os.Getenv("AGENTMESH_PRESENCE_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("AGENTMESH_PRESENCE_TTL: %w", err)
		}
		cfg.PresenceTTL = d
	}
	return cfg, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
