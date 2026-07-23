package iam_test

// RFC 8693 token-exchange (delegation) tests: an agent authenticates as
// itself and presents a subject_token from a TRUSTED human IdP; agentiam
// issues a token whose `sub` stays the agent and whose `act` names the human.
//
// The fake human IdP here is self-contained (own RSA key + httptest JWKS), so
// these tests do not depend on any other test file's fixtures. Tests marked
// "gated" call delSkipIfVerifyStub: while TrustRegistry.Verify is still the
// parallel-build stub they skip with an explicit message; once the real
// implementation lands they run in full.

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/iam"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

const (
	delResource     = "https://mesh.example/mcp"
	delGrant        = "urn:ietf:params:oauth:grant-type:token-exchange"
	delTypeJWT      = "urn:ietf:params:oauth:token-type:jwt"
	delTypeAccess   = "urn:ietf:params:oauth:token-type:access_token"
	delHumanIssuer  = "https://login.corp.example"
	delHumanSubject = "priya@corp.example"
	delHumanKid     = "human-idp-key-1"
)

// delIDP is a fake human IdP: an RSA key and an httptest JWKS endpoint.
type delIDP struct {
	key *rsa.PrivateKey
	srv *httptest.Server
}

func delNewIDP(t *testing.T) *delIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	i := &delIDP{key: key}
	i.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pub := key.Public().(*rsa.PublicKey)
		doc := map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": delHumanKid,
			"n":   delB64(pub.N.Bytes()),
			"e":   delB64(big.NewInt(int64(pub.E)).Bytes()),
		}}}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(i.srv.Close)
	return i
}

// mint signs an RS256 JWT with arbitrary claims — the human's subject token.
func (i *delIDP) mint(t *testing.T, claims map[string]any) string {
	t.Helper()
	hdr := map[string]string{"alg": "RS256", "typ": "JWT", "kid": delHumanKid}
	hb, _ := json.Marshal(hdr)
	cb, _ := json.Marshal(claims)
	signing := delB64(hb) + "." + delB64(cb)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign subject token: %v", err)
	}
	return signing + "." + delB64(sig)
}

// humanClaims is a valid subject token's claim set expiring at exp.
func delHumanClaims(exp time.Time) map[string]any {
	now := time.Now().UTC()
	return map[string]any{
		"iss": delHumanIssuer,
		"sub": delHumanSubject,
		"aud": "https://agentiam.example",
		"iat": now.Unix(),
		"nbf": now.Unix(),
		"exp": exp.Unix(),
	}
}

// delFixture is a live agentiam server that trusts the fake human IdP, plus a
// registered agent client.
type delFixture struct {
	ts     *httptest.Server
	store  iam.Store
	keys   *iam.KeySet
	idp    *delIDP
	id     string
	secret string
}

// delNew builds the fixture. clientTTL <= 0 keeps the server default (15m).
func delNew(t *testing.T, clientTTL time.Duration) *delFixture {
	t.Helper()
	key, err := iam.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	keys := iam.NewKeySet(key)
	store := iam.NewMemStore()
	idp := delNewIDP(t)

	var srv *iam.Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	srv, err = iam.NewServer(iam.Config{
		Issuer:         ts.URL,
		SubjectIssuers: []iam.TrustedIssuer{{Issuer: delHumanIssuer, JWKSURL: idp.srv.URL}},
	}, keys, store)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	id, secret, err := iam.RegisterClient(context.Background(), store, iam.Client{
		Workspace:     "team",
		Subject:       "reviewer-agent",
		Kind:          "agent",
		AllowedScopes: []string{"mesh:send", "mesh:read"},
		TokenTTL:      clientTTL,
	})
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	return &delFixture{ts: ts, store: store, keys: keys, idp: idp, id: id, secret: secret}
}

// delExchange POSTs a token-exchange request and returns status + decoded body.
func delExchange(t *testing.T, f *delFixture, form url.Values) (int, map[string]any) {
	t.Helper()
	res, err := f.ts.Client().PostForm(f.ts.URL+"/token", form)
	if err != nil {
		t.Fatalf("POST /token: %v", err)
	}
	defer res.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return res.StatusCode, body
}

// delForm is a well-formed exchange request; tests mutate it per case.
func delForm(f *delFixture, subjectToken string) url.Values {
	return url.Values{
		"grant_type":         {delGrant},
		"client_id":          {f.id},
		"client_secret":      {f.secret},
		"subject_token":      {subjectToken},
		"subject_token_type": {delTypeJWT},
		"resource":           {delResource},
	}
}

// delSkipIfVerifyStub gates the happy-path tests: it verifies a known-good
// subject token directly against a registry trusting the fake IdP. If Verify
// is still the parallel build's stub the test skips loudly; if Verify is real
// but rejects a good token, that's a hard failure, not a skip.
func delSkipIfVerifyStub(t *testing.T, f *delFixture, goodToken string) {
	t.Helper()
	reg := iam.NewTrustRegistry(
		[]iam.TrustedIssuer{{Issuer: delHumanIssuer, JWKSURL: f.idp.srv.URL}}, nil)
	_, err := reg.Verify(context.Background(), goodToken)
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "not implemented") {
		t.Skip("PENDING: TrustRegistry.Verify is still the stub (parallel delegation-verify build); " +
			"this test exercises the full happy path once it lands")
	}
	t.Fatalf("TrustRegistry.Verify rejected a valid subject token: %v", err)
}

// delClaims decodes a JWT payload into the delegated-claims shape under test.
type delClaims struct {
	Iss       string `json:"iss"`
	Sub       string `json:"sub"`
	Aud       string `json:"aud"`
	Workspace string `json:"workspace"`
	Kind      string `json:"kind"`
	ClientID  string `json:"client_id"`
	Scope     string `json:"scope"`
	Act       *struct {
		Sub string `json:"sub"`
		Iss string `json:"iss"`
	} `json:"act"`
	Exp int64  `json:"exp"`
	Iat int64  `json:"iat"`
	JTI string `json:"jti"`
}

func delDecode(t *testing.T, token string) delClaims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var c delClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	return c
}

// TestDelegationHappyPath (gated on real Verify): a human's subject token is
// exchanged for a delegated access token — sub stays the agent, act names the
// human and their IdP, and the unchanged resource server accepts the token.
func TestDelegationHappyPath(t *testing.T) {
	f := delNew(t, 0)
	humanExp := time.Now().UTC().Add(time.Hour)
	subjectToken := f.idp.mint(t, delHumanClaims(humanExp))
	delSkipIfVerifyStub(t, f, subjectToken)

	form := delForm(f, subjectToken)
	form.Set("scope", "mesh:send")
	status, body := delExchange(t, f, form)
	if status != http.StatusOK {
		t.Fatalf("exchange failed: status %d, body %v", status, body)
	}
	if got := body["issued_token_type"]; got != delTypeAccess {
		t.Errorf("issued_token_type = %v, want %s", got, delTypeAccess)
	}
	if got := body["token_type"]; got != "Bearer" {
		t.Errorf("token_type = %v, want Bearer", got)
	}
	token, _ := body["access_token"].(string)
	if token == "" {
		t.Fatal("no access_token in response")
	}

	c := delDecode(t, token)
	if c.Sub != "reviewer-agent" {
		t.Errorf("sub = %q, want reviewer-agent (the AGENT stays the subject)", c.Sub)
	}
	if c.Act == nil {
		t.Fatal("delegated token carries no act claim")
	}
	if c.Act.Sub != delHumanSubject {
		t.Errorf("act.sub = %q, want %q", c.Act.Sub, delHumanSubject)
	}
	if c.Act.Iss != delHumanIssuer {
		t.Errorf("act.iss = %q, want %q", c.Act.Iss, delHumanIssuer)
	}
	if c.Aud != delResource {
		t.Errorf("aud = %q, want %q", c.Aud, delResource)
	}
	if c.Scope != "mesh:send" {
		t.Errorf("scope = %q, want mesh:send", c.Scope)
	}
	// exp <= min(now + client TTL (default 15m), human exp): here the 15m TTL
	// is the binding bound. Allow a little slack for test runtime.
	maxExp := time.Now().UTC().Add(15*time.Minute + 5*time.Second)
	if got := time.Unix(c.Exp, 0); got.After(maxExp) {
		t.Errorf("exp = %v, beyond min(client TTL, human exp) bound %v", got, maxExp)
	}

	// The unchanged resource server must accept the delegated token and map it
	// to the AGENT principal; act rides along as an opaque audit claim.
	authn, err := auth.NewJWTAuthenticator(auth.OAuthConfig{
		Issuer:   f.ts.URL,
		Audience: delResource,
		JWKSURL:  f.ts.URL + "/.well-known/jwks.json",
	})
	if err != nil {
		t.Fatalf("NewJWTAuthenticator: %v", err)
	}
	p, err := authn.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("resource server rejected the delegated token: %v", err)
	}
	want := auth.Principal{Workspace: "team", Member: "reviewer-agent", Kind: model.KindAgent}
	if p != want {
		t.Fatalf("Principal = %+v, want %+v", p, want)
	}
}

// TestDelegationExpiryCappedByHumanAuthorization (gated on real Verify): the
// standards §3 rule — a delegated token must not outlive the human's own
// authorization. Human token expires in 30s; client TTL is 2h; delegated exp
// must equal the human's exp.
func TestDelegationExpiryCappedByHumanAuthorization(t *testing.T) {
	f := delNew(t, 2*time.Hour)
	humanExp := time.Now().UTC().Add(30 * time.Second).Truncate(time.Second)
	subjectToken := f.idp.mint(t, delHumanClaims(humanExp))
	delSkipIfVerifyStub(t, f, subjectToken)

	status, body := delExchange(t, f, delForm(f, subjectToken))
	if status != http.StatusOK {
		t.Fatalf("exchange failed: status %d, body %v", status, body)
	}
	token, _ := body["access_token"].(string)
	c := delDecode(t, token)
	if c.Exp != humanExp.Unix() {
		t.Errorf("delegated exp = %d, want the human's exp %d (not the 2h client TTL)",
			c.Exp, humanExp.Unix())
	}
	if ei, ok := body["expires_in"].(float64); ok && ei > 31 {
		t.Errorf("expires_in = %v, want <= 30s (human authorization bound)", ei)
	}
}

// TestDelegationUntrustedIssuer: a subject token signed by an IdP that is NOT
// in the trust registry must be rejected with invalid_grant. (Stub-safe: the
// stub rejects everything, the real Verify rejects the unknown issuer.)
func TestDelegationUntrustedIssuer(t *testing.T) {
	f := delNew(t, 0)
	rogue := delNewIDP(t) // separate key, never registered as a SubjectIssuer
	claims := delHumanClaims(time.Now().UTC().Add(time.Hour))
	claims["iss"] = "https://rogue.example"
	subjectToken := rogue.mint(t, claims)

	status, body := delExchange(t, f, delForm(f, subjectToken))
	if status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("untrusted issuer: status %d, error %v; want 400 invalid_grant", status, body["error"])
	}
}

// TestDelegationDisabledWithoutIssuers: a server configured with no
// SubjectIssuers refuses every exchange (delegation off by default).
func TestDelegationDisabledWithoutIssuers(t *testing.T) {
	key, err := iam.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	store := iam.NewMemStore()
	srv, err := iam.NewServer(iam.Config{Issuer: "https://iam.example"}, iam.NewKeySet(key), store)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	id, secret, err := iam.RegisterClient(context.Background(), store, iam.Client{
		Workspace: "team", Subject: "reviewer-agent", AllowedScopes: []string{"mesh:send"},
	})
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}

	idp := delNewIDP(t)
	subjectToken := idp.mint(t, delHumanClaims(time.Now().UTC().Add(time.Hour)))
	res, err := ts.Client().PostForm(ts.URL+"/token", url.Values{
		"grant_type":         {delGrant},
		"client_id":          {id},
		"client_secret":      {secret},
		"subject_token":      {subjectToken},
		"subject_token_type": {delTypeJWT},
		"resource":           {delResource},
	})
	if err != nil {
		t.Fatalf("POST /token: %v", err)
	}
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	if res.StatusCode != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("delegation-disabled server: status %d, error %v; want 400 invalid_grant",
			res.StatusCode, body["error"])
	}
}

// TestDelegationMissingSubjectToken: invalid_request per RFC 8693 §2.1.
func TestDelegationMissingSubjectToken(t *testing.T) {
	f := delNew(t, 0)
	form := delForm(f, "ignored")
	form.Del("subject_token")
	status, body := delExchange(t, f, form)
	if status != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Fatalf("missing subject_token: status %d, error %v; want 400 invalid_request", status, body["error"])
	}
}

// TestDelegationWrongSubjectTokenType: only jwt / access_token are accepted.
func TestDelegationWrongSubjectTokenType(t *testing.T) {
	f := delNew(t, 0)
	subjectToken := f.idp.mint(t, delHumanClaims(time.Now().UTC().Add(time.Hour)))
	form := delForm(f, subjectToken)
	form.Set("subject_token_type", "urn:ietf:params:oauth:token-type:saml2")
	status, body := delExchange(t, f, form)
	if status != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Fatalf("wrong subject_token_type: status %d, error %v; want 400 invalid_request", status, body["error"])
	}

	form.Del("subject_token_type") // absent is also invalid
	status, body = delExchange(t, f, form)
	if status != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Fatalf("absent subject_token_type: status %d, error %v; want 400 invalid_request", status, body["error"])
	}
}

// TestDelegationUnsupportedRequestedTokenType: this AS only issues access tokens.
func TestDelegationUnsupportedRequestedTokenType(t *testing.T) {
	f := delNew(t, 0)
	subjectToken := f.idp.mint(t, delHumanClaims(time.Now().UTC().Add(time.Hour)))
	form := delForm(f, subjectToken)
	form.Set("requested_token_type", "urn:ietf:params:oauth:token-type:refresh_token")
	status, body := delExchange(t, f, form)
	if status != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Fatalf("unsupported requested_token_type: status %d, error %v; want 400 invalid_request",
			status, body["error"])
	}
}

// TestDelegationBadClientSecret: the agent must still prove ITSELF; a valid
// human subject token cannot compensate for bad client credentials.
func TestDelegationBadClientSecret(t *testing.T) {
	f := delNew(t, 0)
	subjectToken := f.idp.mint(t, delHumanClaims(time.Now().UTC().Add(time.Hour)))
	form := delForm(f, subjectToken)
	form.Set("client_secret", "ags_wrong")
	status, body := delExchange(t, f, form)
	if status != http.StatusBadRequest || body["error"] != "invalid_client" {
		t.Fatalf("bad client secret: status %d, error %v; want 400 invalid_client", status, body["error"])
	}
}

// TestDelegationScopeNotAllowed: delegation can never broaden the client's own
// allowance — requesting a scope outside AllowedScopes is invalid_scope.
func TestDelegationScopeNotAllowed(t *testing.T) {
	f := delNew(t, 0)
	subjectToken := f.idp.mint(t, delHumanClaims(time.Now().UTC().Add(time.Hour)))
	form := delForm(f, subjectToken)
	form.Set("scope", "admin:everything")
	status, body := delExchange(t, f, form)
	if status != http.StatusBadRequest || body["error"] != "invalid_scope" {
		t.Fatalf("disallowed scope: status %d, error %v; want 400 invalid_scope", status, body["error"])
	}
}

// TestDelegationAdvertisedInMetadata: discovery lists the token-exchange grant.
func TestDelegationAdvertisedInMetadata(t *testing.T) {
	f := delNew(t, 0)
	res, err := f.ts.Client().Get(f.ts.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatalf("GET metadata: %v", err)
	}
	defer res.Body.Close()
	var md struct {
		Grants []string `json:"grant_types_supported"`
	}
	if err := json.NewDecoder(res.Body).Decode(&md); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	for _, g := range md.Grants {
		if g == delGrant {
			return
		}
	}
	t.Fatalf("grant_types_supported = %v, want to contain %s", md.Grants, delGrant)
}

func delB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
