package main

import (
	"net/http"
	"time"
)

// listenAndServe runs the HTTP server with sane timeouts. TLS termination is
// expected at a reverse proxy in production (same posture as AgentMesh), or add
// AGENTIAM_TLS_* later mirroring the server.
func listenAndServe(addr string, h http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
}
