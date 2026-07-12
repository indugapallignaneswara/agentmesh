package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

// OAuth 2.1 resource-server authentication (MCP authorization spec).
//
// The MCP spec makes a protected server an OAuth 2.1 *resource server*: it
// validates a bearer JWT issued by an external authorization server (Okta,
// Entra, Keycloak, Auth0, …), verifies the signature against the issuer's
// JWKS, and — critically, per RFC 8707 — checks the token was minted for THIS
// resource (audience binding). A token for another service must not work here,
// which is what makes token passthrough attacks fail.
//
// Identity mapping: the token's subject is the member name and a configurable
// claim carries the workspace. Humans authenticate through their IdP; agents
// keep opaque amt_ tokens (ChainAuthenticator tries both), because machine
// credentials with no interactive login are exactly what opaque tokens are for.
//
// JWT verification is implemented directly rather than pulling in a library:
// AgentMesh ships as a single self-hosted binary, and the supported algorithm
// set is deliberately narrow (RS256/384/512, ES256/384/512 — no HMAC, no
// "none", no alg confusion).

// OAuthConfig configures the JWT resource-server authenticator.
type OAuthConfig struct {
	// Issuer is the expected `iss` claim (the authorization server).
	Issuer string
	// Audience is this resource's canonical URI; the token's `aud` must
	// contain it (RFC 8707 resource indicators).
	Audience string
	// JWKSURL is where the issuer publishes its signing keys.
	JWKSURL string
	// WorkspaceClaim names the claim carrying the workspace (default
	// "workspace"). A token without it cannot act in any room.
	WorkspaceClaim string
	// KindClaim names the claim carrying human/agent (default "kind"); when
	// absent the principal is treated as a human, since IdP-issued tokens
	// represent people.
	KindClaim string
	// HTTPClient fetches the JWKS (defaults to a 10s-timeout client).
	HTTPClient *http.Client
	// Now overrides the clock (tests).
	Now func() time.Time
	// Leeway tolerates clock skew on exp/nbf (default 60s).
	Leeway time.Duration
}

// JWTAuthenticator validates OAuth 2.1 access tokens against an issuer's JWKS.
// It satisfies Authenticator, so it drops into the existing middleware and
// every service-layer check unchanged — the extraction seam working as designed.
type JWTAuthenticator struct {
	cfg OAuthConfig

	mu        sync.RWMutex
	keys      map[string]crypto.PublicKey // kid -> key
	fetchedAt time.Time
}

// NewJWTAuthenticator builds a resource-server authenticator. Keys are fetched
// lazily on first use and refreshed when an unknown kid appears (key rotation).
func NewJWTAuthenticator(cfg OAuthConfig) (*JWTAuthenticator, error) {
	if cfg.Issuer == "" || cfg.Audience == "" || cfg.JWKSURL == "" {
		return nil, errors.New("oauth: issuer, audience and jwks_url are required")
	}
	if cfg.WorkspaceClaim == "" {
		cfg.WorkspaceClaim = "workspace"
	}
	if cfg.KindClaim == "" {
		cfg.KindClaim = "kind"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Leeway == 0 {
		cfg.Leeway = 60 * time.Second
	}
	return &JWTAuthenticator{cfg: cfg, keys: map[string]crypto.PublicKey{}}, nil
}

// Authenticate verifies a bearer JWT and maps it to a Principal.
func (a *JWTAuthenticator) Authenticate(ctx context.Context, token string) (Principal, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Principal{}, ErrUnauthenticated // not a JWS
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	hb, err := b64(parts[0])
	if err != nil || json.Unmarshal(hb, &hdr) != nil {
		return Principal{}, ErrUnauthenticated
	}
	// Explicit allowlist: no "none", no HMAC (which would let an attacker sign
	// with the public key), no alg confusion.
	if !supportedAlg(hdr.Alg) {
		return Principal{}, fmt.Errorf("%w: unsupported alg %q", ErrUnauthenticated, hdr.Alg)
	}

	key, err := a.keyFor(ctx, hdr.Kid)
	if err != nil {
		return Principal{}, err
	}
	signing := []byte(parts[0] + "." + parts[1])
	sig, err := b64(parts[2])
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	if err := verify(hdr.Alg, key, signing, sig); err != nil {
		return Principal{}, fmt.Errorf("%w: bad signature", ErrUnauthenticated)
	}

	pb, err := b64(parts[1])
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	var claims map[string]any
	if err := json.Unmarshal(pb, &claims); err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return a.principalFrom(claims)
}

// principalFrom validates the registered claims and maps the token to an
// AgentMesh principal.
func (a *JWTAuthenticator) principalFrom(c map[string]any) (Principal, error) {
	now := a.cfg.Now()

	if iss, _ := c["iss"].(string); iss != a.cfg.Issuer {
		return Principal{}, fmt.Errorf("%w: issuer %q not trusted", ErrUnauthenticated, iss)
	}
	// RFC 8707: the token must have been minted for THIS resource. Without
	// this check a token stolen from (or issued for) another service would be
	// accepted here — the token-passthrough attack the MCP spec calls out.
	if !audienceContains(c["aud"], a.cfg.Audience) {
		return Principal{}, fmt.Errorf("%w: token audience does not include %q", ErrUnauthenticated, a.cfg.Audience)
	}
	if exp, ok := numeric(c["exp"]); ok && now.After(exp.Add(a.cfg.Leeway)) {
		return Principal{}, fmt.Errorf("%w: token expired", ErrUnauthenticated)
	} else if !ok {
		return Principal{}, fmt.Errorf("%w: token has no exp", ErrUnauthenticated)
	}
	if nbf, ok := numeric(c["nbf"]); ok && now.Add(a.cfg.Leeway).Before(nbf) {
		return Principal{}, fmt.Errorf("%w: token not yet valid", ErrUnauthenticated)
	}

	sub, _ := c["sub"].(string)
	if sub == "" {
		return Principal{}, fmt.Errorf("%w: token has no sub", ErrUnauthenticated)
	}
	ws, _ := c[a.cfg.WorkspaceClaim].(string)
	if ws == "" {
		return Principal{}, fmt.Errorf("%w: token has no %q claim", ErrUnauthenticated, a.cfg.WorkspaceClaim)
	}

	// IdP-issued tokens represent people unless they say otherwise.
	kind := model.KindHuman
	if k, _ := c[a.cfg.KindClaim].(string); k != "" {
		kind = model.Kind(k)
		if !kind.Valid() {
			return Principal{}, fmt.Errorf("%w: invalid %q claim", ErrUnauthenticated, a.cfg.KindClaim)
		}
	}
	return Principal{Workspace: ws, Member: sub, Kind: kind}, nil
}

// keyFor returns the signing key for a kid, refreshing the JWKS if the kid is
// unknown (handles rotation) but no more than once a minute.
func (a *JWTAuthenticator) keyFor(ctx context.Context, kid string) (crypto.PublicKey, error) {
	a.mu.RLock()
	k, ok := a.keys[kid]
	stale := time.Since(a.fetchedAt) > time.Minute
	a.mu.RUnlock()
	if ok {
		return k, nil
	}
	if !stale && len(a.keys) > 0 {
		return nil, fmt.Errorf("%w: unknown signing key", ErrUnauthenticated)
	}
	if err := a.refresh(ctx); err != nil {
		return nil, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if k, ok := a.keys[kid]; ok {
		return k, nil
	}
	// A single-key JWKS may omit kid.
	if kid == "" && len(a.keys) == 1 {
		for _, only := range a.keys {
			return only, nil
		}
	}
	return nil, fmt.Errorf("%w: unknown signing key", ErrUnauthenticated)
}

func (a *JWTAuthenticator) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.JWKSURL, nil)
	if err != nil {
		return err
	}
	res, err := a.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch jwks: status %d", res.StatusCode)
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		return fmt.Errorf("decode jwks: %w", err)
	}
	keys := make(map[string]crypto.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		pub, err := k.publicKey()
		if err != nil {
			continue // skip keys we cannot use rather than failing the set
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return errors.New("jwks contained no usable keys")
	}
	a.mu.Lock()
	a.keys = keys
	a.fetchedAt = time.Now()
	a.mu.Unlock()
	return nil
}

// jwk is the subset of a JSON Web Key we support.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Crv string `json:"crv"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (k jwk) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		nb, err := b64(k.N)
		if err != nil {
			return nil, err
		}
		eb, err := b64(k.E)
		if err != nil {
			return nil, err
		}
		e := 0
		for _, b := range eb {
			e = e<<8 | int(b)
		}
		if e == 0 {
			e = 65537
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}, nil
	case "EC":
		xb, err := b64(k.X)
		if err != nil {
			return nil, err
		}
		yb, err := b64(k.Y)
		if err != nil {
			return nil, err
		}
		curve, err := curveFor(k.Crv)
		if err != nil {
			return nil, err
		}
		return &ecdsa.PublicKey{
			Curve: curve,
			X:     new(big.Int).SetBytes(xb),
			Y:     new(big.Int).SetBytes(yb),
		}, nil
	}
	return nil, fmt.Errorf("unsupported kty %q", k.Kty)
}

// ChainAuthenticator tries each authenticator in order, returning the first
// success. It is how humans (OIDC JWTs) and agents (opaque amt_ tokens) coexist
// on one endpoint: machine credentials have no interactive login, so agents
// keep opaque tokens while people authenticate through the IdP.
type ChainAuthenticator struct {
	Authenticators []Authenticator
}

func (c *ChainAuthenticator) Authenticate(ctx context.Context, secret string) (Principal, error) {
	var lastErr error = ErrUnauthenticated
	for _, a := range c.Authenticators {
		p, err := a.Authenticate(ctx, secret)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, ErrUnauthenticated) {
			return Principal{}, err // a real failure (e.g. store down), not "wrong credential type"
		}
		lastErr = err
	}
	return Principal{}, lastErr
}

// --- crypto helpers ---

func supportedAlg(alg string) bool {
	switch alg {
	case "RS256", "RS384", "RS512", "ES256", "ES384", "ES512":
		return true
	}
	return false
}

func verify(alg string, key crypto.PublicKey, signing, sig []byte) error {
	h, hash := hasherFor(alg)
	h.Write(signing)
	digest := h.Sum(nil)

	switch {
	case strings.HasPrefix(alg, "RS"):
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return errors.New("key type mismatch")
		}
		return rsa.VerifyPKCS1v15(pub, hash, digest, sig)
	case strings.HasPrefix(alg, "ES"):
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("key type mismatch")
		}
		// JWS ES* signatures are the raw r||s pair, not ASN.1.
		n := len(sig) / 2
		if n == 0 || len(sig)%2 != 0 {
			return errors.New("bad ecdsa signature length")
		}
		r := new(big.Int).SetBytes(sig[:n])
		s := new(big.Int).SetBytes(sig[n:])
		if !ecdsa.Verify(pub, digest, r, s) {
			return errors.New("ecdsa verify failed")
		}
		return nil
	}
	return errors.New("unsupported alg")
}

func hasherFor(alg string) (interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}, crypto.Hash) {
	switch alg[2:] {
	case "384":
		return sha512.New384(), crypto.SHA384
	case "512":
		return sha512.New(), crypto.SHA512
	default:
		return sha256.New(), crypto.SHA256
	}
}

func b64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// audienceContains handles `aud` as either a string or an array of strings.
func audienceContains(aud any, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// numeric reads a NumericDate claim (seconds since epoch).
func numeric(v any) (time.Time, bool) {
	switch n := v.(type) {
	case float64:
		return time.Unix(int64(n), 0), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(i, 0), true
	}
	return time.Time{}, false
}

// curveFor maps a JWK curve name to its elliptic curve.
func curveFor(crv string) (elliptic.Curve, error) {
	switch crv {
	case "P-256":
		return elliptic.P256(), nil
	case "P-384":
		return elliptic.P384(), nil
	case "P-521":
		return elliptic.P521(), nil
	}
	return nil, fmt.Errorf("unsupported curve %q", crv)
}
