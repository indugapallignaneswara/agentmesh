package auth

import (
	"net/http"
	"strings"
)

// Middleware gates an HTTP handler behind bearer-token authentication.
// Requests to paths in passthrough skip authentication entirely (health
// checks, the dashboard shell page). Credentials are taken from
// "Authorization: Bearer <secret>" or, for browser fetches, a "token" query
// parameter. Failures answer 401 with a WWW-Authenticate challenge per the
// MCP authorization spec's resource-server behaviour.
func Middleware(authn Authenticator, passthrough ...string) func(http.Handler) http.Handler {
	open := make(map[string]bool, len(passthrough))
	for _, p := range passthrough {
		open[p] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if open[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			secret := bearerSecret(r)
			if secret == "" {
				challenge(w, "missing credentials")
				return
			}
			p, err := authn.Authenticate(r.Context(), secret)
			if err != nil {
				challenge(w, "invalid credentials")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

func bearerSecret(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if rest, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(rest)
		}
		return ""
	}
	return r.URL.Query().Get("token")
}

func challenge(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="agentmesh", error="invalid_token"`)
	http.Error(w, msg, http.StatusUnauthorized)
}
