package auth_test

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// TestChallengeHeaderIsCanonicalOnTheWire guards the registered spelling of the
// challenge header. Header names are case-insensitive per RFC 7230, but Go's
// Header.Set canonicalises to "Www-Authenticate" while strict clients look for
// "WWW-Authenticate", so we write the canonical form into the map directly.
//
// This must be asserted on the RAW BYTES: Go's HTTP *client* re-canonicalises
// header keys when parsing a response, so http.Response.Header can never show
// what was actually sent. We speak HTTP/1.1 over a raw TCP conn instead.
func TestChallengeHeaderIsCanonicalOnTheWire(t *testing.T) {
	authn := &auth.TokenAuthenticator{Store: store.NewMemory()}
	h := auth.Middleware(authn)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handler reached without credentials")
	}))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	addr := strings.TrimPrefix(srv.URL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET /mcp HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatal(err)
	}

	var raw strings.Builder
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		line := sc.Text()
		raw.WriteString(line + "\n")
		if line == "" { // end of headers
			break
		}
	}
	got := raw.String()
	if !strings.Contains(got, "401") {
		t.Fatalf("want a 401 challenge, got:\n%s", got)
	}
	if !strings.Contains(got, "WWW-Authenticate: Bearer") {
		t.Fatalf("canonical WWW-Authenticate missing from the wire:\n%s", got)
	}
	if strings.Contains(got, "Www-Authenticate:") {
		t.Fatalf("non-canonical Www-Authenticate sent on the wire:\n%s", got)
	}
}
