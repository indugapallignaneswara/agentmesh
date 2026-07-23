package iam

// Tests for subject-token verification (RFC 8693 delegation, P1). Internal
// test file (package iam): it exercises unexported verifySubject and drives
// TrustRegistry's unexported clock for cache-timing cases. The fake-IdP
// pattern mirrors internal/auth/oauth_test.go.

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// subjIDP is a fake human IdP: signing keys plus a JWKS endpoint with a hit
// counter and a swappable published key set (rotation).
type subjIDP struct {
	rsaKey *rsa.PrivateKey
	ecKey  *ecdsa.PrivateKey
	srv    *httptest.Server
	hits   atomic.Int64
	jwks   atomic.Value // []map[string]string — the currently published keys
}

func newSubjIDP(t *testing.T) *subjIDP {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	i := &subjIDP{rsaKey: rk, ecKey: ek}
	i.jwks.Store([]map[string]string{i.rsaJWK("kid-1", rk)})
	i.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i.hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": i.jwks.Load()})
	}))
	t.Cleanup(i.srv.Close)
	return i
}

func (i *subjIDP) rsaJWK(kid string, k *rsa.PrivateKey) map[string]string {
	pub := k.Public().(*rsa.PublicKey)
	return map[string]string{
		"kty": "RSA", "kid": kid,
		"n": b64u(pub.N.Bytes()),
		"e": b64u(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func (i *subjIDP) ecJWK(kid string) map[string]string {
	pub := i.ecKey.Public().(*ecdsa.PublicKey)
	size := (pub.Curve.Params().BitSize + 7) / 8
	return map[string]string{
		"kty": "EC", "kid": kid, "crv": "P-256",
		"x": b64u(pub.X.FillBytes(make([]byte, size))),
		"y": b64u(pub.Y.FillBytes(make([]byte, size))),
	}
}

// mintRS signs an RS256 JWT with the given RSA key and kid.
func (i *subjIDP) mintRS(t *testing.T, kid string, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	cb, _ := json.Marshal(claims)
	signing := b64u(hb) + "." + b64u(cb)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64u(sig)
}

// mintES signs an ES256 JWT (raw r||s signature, not ASN.1).
func (i *subjIDP) mintES(t *testing.T, kid string, claims map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT", "kid": kid})
	cb, _ := json.Marshal(claims)
	signing := b64u(hb) + "." + b64u(cb)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, i.ecKey, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	size := (i.ecKey.Curve.Params().BitSize + 7) / 8
	sig := append(r.FillBytes(make([]byte, size)), s.FillBytes(make([]byte, size))...)
	return signing + "." + b64u(sig)
}

// mintRaw builds an unsigned-style token with an arbitrary header (alg=none,
// HS256, ...) and a garbage signature segment.
func mintRaw(t *testing.T, hdr map[string]string, claims map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(hdr)
	cb, _ := json.Marshal(claims)
	return b64u(hb) + "." + b64u(cb) + "." + b64u([]byte("sig"))
}

const subjTestIssuer = "https://idp.example.com"

func subjClaims(exp time.Time) map[string]any {
	return map[string]any{
		"iss": subjTestIssuer,
		"sub": "alice@example.com",
		"aud": "https://idp.example.com/some-other-app", // deliberately NOT agentiam: aud is not checked here
		"exp": float64(exp.Unix()),
	}
}

func (i *subjIDP) registry() *TrustRegistry {
	return NewTrustRegistry(
		[]TrustedIssuer{{Issuer: subjTestIssuer, JWKSURL: i.srv.URL}},
		i.srv.Client(),
	)
}

func TestSubjectHappyPath(t *testing.T) {
	i := newSubjIDP(t)
	r := i.registry()
	exp := time.Now().Add(time.Hour).Truncate(time.Second)

	got, err := r.Verify(context.Background(), i.mintRS(t, "kid-1", i.rsaKey, subjClaims(exp)))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Issuer != subjTestIssuer || got.Subject != "alice@example.com" {
		t.Errorf("claims = %+v", got)
	}
	if !got.Expiry.Equal(exp) {
		t.Errorf("Expiry = %v, want %v", got.Expiry, exp)
	}
	if got.Raw["sub"] != "alice@example.com" {
		t.Errorf("Raw not carried: %v", got.Raw)
	}
}

func TestSubjectUntrustedIssuerNoFetch(t *testing.T) {
	i := newSubjIDP(t)
	r := i.registry()
	c := subjClaims(time.Now().Add(time.Hour))
	c["iss"] = "https://evil.example.com"

	_, err := r.Verify(context.Background(), i.mintRS(t, "kid-1", i.rsaKey, c))
	if !errors.Is(err, ErrSubjectRejected) {
		t.Fatalf("err = %v, want ErrSubjectRejected", err)
	}
	// The trust-registry gate must fire BEFORE any network call: an untrusted
	// issuer must never cause a JWKS fetch.
	if n := i.hits.Load(); n != 0 {
		t.Errorf("JWKS endpoint hit %d times for untrusted issuer, want 0", n)
	}
}

func TestSubjectBadSignature(t *testing.T) {
	i := newSubjIDP(t)
	r := i.registry()
	tok := i.mintRS(t, "kid-1", i.rsaKey, subjClaims(time.Now().Add(time.Hour)))

	// Tamper one byte of the signature segment.
	b := []byte(tok)
	b[len(b)-1] ^= 0x01
	if _, err := r.Verify(context.Background(), string(b)); !errors.Is(err, ErrSubjectRejected) {
		t.Fatalf("tampered sig: err = %v, want ErrSubjectRejected", err)
	}

	// And a token signed by a key the issuer never published.
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tok = i.mintRS(t, "kid-1", other, subjClaims(time.Now().Add(time.Hour)))
	if _, err := r.Verify(context.Background(), tok); !errors.Is(err, ErrSubjectRejected) {
		t.Fatalf("wrong key: err = %v, want ErrSubjectRejected", err)
	}
}

func TestSubjectExpired(t *testing.T) {
	i := newSubjIDP(t)
	r := i.registry()
	tok := i.mintRS(t, "kid-1", i.rsaKey, subjClaims(time.Now().Add(-2*time.Minute))) // beyond 60s leeway
	if _, err := r.Verify(context.Background(), tok); !errors.Is(err, ErrSubjectRejected) {
		t.Fatalf("err = %v, want ErrSubjectRejected", err)
	}
}

func TestSubjectMissingExp(t *testing.T) {
	i := newSubjIDP(t)
	r := i.registry()
	c := subjClaims(time.Now())
	delete(c, "exp")
	tok := i.mintRS(t, "kid-1", i.rsaKey, c)
	if _, err := r.Verify(context.Background(), tok); !errors.Is(err, ErrSubjectRejected) {
		t.Fatalf("err = %v, want ErrSubjectRejected", err)
	}
}

func TestSubjectRejectsNoneAndHMAC(t *testing.T) {
	i := newSubjIDP(t)
	r := i.registry()
	c := subjClaims(time.Now().Add(time.Hour))
	for _, alg := range []string{"none", "HS256"} {
		tok := mintRaw(t, map[string]string{"alg": alg, "typ": "JWT", "kid": "kid-1"}, c)
		if _, err := r.Verify(context.Background(), tok); !errors.Is(err, ErrSubjectRejected) {
			t.Errorf("alg %s: err = %v, want ErrSubjectRejected", alg, err)
		}
	}
	// Neither alg may reach the network.
	if n := i.hits.Load(); n != 0 {
		t.Errorf("JWKS endpoint hit %d times for none/HS256, want 0", n)
	}
}

func TestSubjectMissingSub(t *testing.T) {
	i := newSubjIDP(t)
	r := i.registry()
	c := subjClaims(time.Now().Add(time.Hour))
	delete(c, "sub")
	tok := i.mintRS(t, "kid-1", i.rsaKey, c)
	if _, err := r.Verify(context.Background(), tok); !errors.Is(err, ErrSubjectRejected) {
		t.Fatalf("err = %v, want ErrSubjectRejected", err)
	}
}

func TestSubjectKeyRotationAndRefreshRateLimit(t *testing.T) {
	i := newSubjIDP(t)
	r := i.registry()

	// Controllable clock (internal test: r.now is settable in-package).
	now := time.Now().UTC()
	r.now = func() time.Time { return now }
	exp := func() time.Time { return now.Add(time.Hour) }

	// Prime the cache with the kid-1 JWKS.
	if _, err := r.Verify(context.Background(), i.mintRS(t, "kid-1", i.rsaKey, subjClaims(exp()))); err != nil {
		t.Fatalf("prime: %v", err)
	}
	if n := i.hits.Load(); n != 1 {
		t.Fatalf("hits after prime = %d, want 1", n)
	}

	// Rotate: the IdP now signs with kid-2 and publishes both keys.
	rotated, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i.jwks.Store([]map[string]string{i.rsaJWK("kid-1", i.rsaKey), i.rsaJWK("kid-2", rotated)})

	// Advance past the refresh rate limit; the unknown kid triggers exactly
	// one refetch and then verifies.
	now = now.Add(2 * time.Minute)
	if _, err := r.Verify(context.Background(), i.mintRS(t, "kid-2", rotated, subjClaims(exp()))); err != nil {
		t.Fatalf("rotated kid: %v", err)
	}
	if n := i.hits.Load(); n != 2 {
		t.Fatalf("hits after rotation = %d, want 2", n)
	}

	// A second unknown kid within a minute of that fetch must NOT refetch.
	if _, err := r.Verify(context.Background(), i.mintRS(t, "kid-3", rotated, subjClaims(exp()))); !errors.Is(err, ErrSubjectRejected) {
		t.Fatalf("unknown kid: err = %v, want ErrSubjectRejected", err)
	}
	if n := i.hits.Load(); n != 2 {
		t.Errorf("hits after rate-limited miss = %d, want 2 (refresh must be rate-limited)", n)
	}

	// Known kids keep verifying from cache with no further fetches.
	if _, err := r.Verify(context.Background(), i.mintRS(t, "kid-1", i.rsaKey, subjClaims(exp()))); err != nil {
		t.Fatalf("cached kid: %v", err)
	}
	if n := i.hits.Load(); n != 2 {
		t.Errorf("hits after cached verify = %d, want 2", n)
	}
}

func TestSubjectES256(t *testing.T) {
	i := newSubjIDP(t)
	i.jwks.Store([]map[string]string{i.ecJWK("ec-1")})
	r := i.registry()

	got, err := r.Verify(context.Background(), i.mintES(t, "ec-1", subjClaims(time.Now().Add(time.Hour))))
	if err != nil {
		t.Fatalf("ES256 Verify: %v", err)
	}
	if got.Subject != "alice@example.com" {
		t.Errorf("Subject = %q", got.Subject)
	}

	// Tampered ES256 payload must fail (proves the EC verify path bites).
	tok := i.mintES(t, "ec-1", subjClaims(time.Now().Add(time.Hour)))
	parts := []byte(tok)
	parts[len(parts)-3] ^= 0x01
	if _, err := r.Verify(context.Background(), string(parts)); !errors.Is(err, ErrSubjectRejected) {
		t.Errorf("tampered ES256: err = %v, want ErrSubjectRejected", err)
	}
}

func TestSubjectMalformed(t *testing.T) {
	i := newSubjIDP(t)
	r := i.registry()
	for _, tok := range []string{"", "abc", "a.b", "a.b.c.d", "!!!.@@@.###"} {
		if _, err := r.Verify(context.Background(), tok); !errors.Is(err, ErrSubjectRejected) {
			t.Errorf("token %q: err = %v, want ErrSubjectRejected", tok, err)
		}
	}
}
