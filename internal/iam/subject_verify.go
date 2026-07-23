package iam

// Subject-token verification implementation (RFC 8693 delegation, P1).
//
// This mirrors internal/auth/oauth.go's JWTAuthenticator — the reference
// JWKS-based verifier — with the same security properties: an explicit
// RS*/ES* algorithm allowlist (no "none", no HMAC, so an attacker cannot
// sign with the public key), RSA PKCS#1v1.5 and ECDSA raw r||s signature
// checks, exp/nbf with 60s leeway, and rotation-aware JWKS refresh limited
// to once per minute. auth's helpers are unexported so the ones needed here
// are reimplemented privately.

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
)

// subjectLeeway tolerates clock skew between us and the external IdP on
// exp/nbf, matching internal/auth's default.
const subjectLeeway = 60 * time.Second

// subjectCacheMu guards TrustRegistry.keys (the per-issuer JWKS cache).
// The locked subject.go API gives keyCache no mutex and TrustRegistry no
// mutex field to add, so the lock lives here at package level. Verify can
// be called concurrently from grant handlers; a package-level mutex over
// every registry is coarser than per-registry locking but correct, and in
// practice a process holds exactly one TrustRegistry.
var subjectCacheMu sync.Mutex

// verifySubject implements TrustRegistry.Verify. Every rejection wraps
// ErrSubjectRejected — one sentinel, no oracle for attackers.
func verifySubject(ctx context.Context, r *TrustRegistry, token string) (SubjectClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return SubjectClaims{}, fmt.Errorf("%w: not a JWS", ErrSubjectRejected)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	hb, err := b64d(parts[0])
	if err != nil || json.Unmarshal(hb, &hdr) != nil {
		return SubjectClaims{}, fmt.Errorf("%w: bad header", ErrSubjectRejected)
	}
	// Explicit allowlist: no "none", no HMAC (which would let an attacker
	// sign with the public JWKS key), no alg confusion.
	if !subjectAlgAllowed(hdr.Alg) {
		return SubjectClaims{}, fmt.Errorf("%w: unsupported alg", ErrSubjectRejected)
	}

	// Decode claims BEFORE verifying: the `iss` claim selects which trusted
	// issuer's JWKS to verify against. Nothing from the payload is believed
	// until the signature check below passes.
	pb, err := b64d(parts[1])
	if err != nil {
		return SubjectClaims{}, fmt.Errorf("%w: bad payload", ErrSubjectRejected)
	}
	var claims map[string]any
	if err := json.Unmarshal(pb, &claims); err != nil {
		return SubjectClaims{}, fmt.Errorf("%w: bad payload", ErrSubjectRejected)
	}
	iss, _ := claims["iss"].(string)
	trusted, ok := r.issuers[iss]
	if !ok {
		// The trust-registry gate: an untrusted issuer is rejected before any
		// network fetch, so unknown IdPs cannot make us hit their JWKS URL.
		return SubjectClaims{}, fmt.Errorf("%w: untrusted issuer", ErrSubjectRejected)
	}

	key, err := subjectKeyFor(ctx, r, trusted, hdr.Kid)
	if err != nil {
		return SubjectClaims{}, err
	}
	sig, err := b64d(parts[2])
	if err != nil {
		return SubjectClaims{}, fmt.Errorf("%w: bad signature encoding", ErrSubjectRejected)
	}
	if err := verifyJWS(hdr.Alg, key, []byte(parts[0]+"."+parts[1]), sig); err != nil {
		return SubjectClaims{}, fmt.Errorf("%w: bad signature", ErrSubjectRejected)
	}

	// Registered claims. NOTE on aud: deliberately NOT checked. The subject
	// token was minted for the human's own IdP audience, not for agentiam;
	// audience narrowing applies to the ISSUED token (the delegation token we
	// mint), not to the incoming proof of who the human is.
	now := r.now()
	exp, ok := numericDate(claims["exp"])
	if !ok {
		return SubjectClaims{}, fmt.Errorf("%w: no exp", ErrSubjectRejected)
	}
	if now.After(exp.Add(subjectLeeway)) {
		return SubjectClaims{}, fmt.Errorf("%w: expired", ErrSubjectRejected)
	}
	if nbf, ok := numericDate(claims["nbf"]); ok && now.Add(subjectLeeway).Before(nbf) {
		return SubjectClaims{}, fmt.Errorf("%w: not yet valid", ErrSubjectRejected)
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return SubjectClaims{}, fmt.Errorf("%w: no sub", ErrSubjectRejected)
	}

	return SubjectClaims{Issuer: iss, Subject: sub, Expiry: exp, Raw: claims}, nil
}

// subjectKeyFor resolves the signing key for kid from the issuer's cached
// JWKS, refreshing on an unknown kid at most once per minute (key rotation,
// same pattern as auth.JWTAuthenticator.keyFor). A single-key JWKS may omit
// kid. All cache access happens under subjectCacheMu; the JWKS fetch itself
// is performed while holding the lock, which serialises concurrent fetches
// for the same issuer instead of stampeding the IdP.
func subjectKeyFor(ctx context.Context, r *TrustRegistry, trusted TrustedIssuer, kid string) (crypto.PublicKey, error) {
	subjectCacheMu.Lock()
	defer subjectCacheMu.Unlock()

	if r.keys.byIssuer == nil {
		r.keys.byIssuer = make(map[string]issuerKeys)
	}
	cached := r.keys.byIssuer[trusted.Issuer]
	if k, ok := lookupKey(cached.keys, kid); ok {
		return k, nil
	}
	// Unknown kid: refresh, but not more than once a minute per issuer.
	if len(cached.keys) > 0 && r.now().Sub(cached.fetchedAt) <= time.Minute {
		return nil, fmt.Errorf("%w: unknown signing key", ErrSubjectRejected)
	}
	keys, err := fetchJWKS(ctx, r.client, trusted.JWKSURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubjectRejected, err)
	}
	r.keys.byIssuer[trusted.Issuer] = issuerKeys{keys: keys, fetchedAt: r.now()}
	if k, ok := lookupKey(keys, kid); ok {
		return k, nil
	}
	return nil, fmt.Errorf("%w: unknown signing key", ErrSubjectRejected)
}

// lookupKey finds kid in a key map; a single-key JWKS may omit kid.
func lookupKey(keys map[string]any, kid string) (crypto.PublicKey, bool) {
	if k, ok := keys[kid]; ok {
		return k, true
	}
	if kid == "" && len(keys) == 1 {
		for _, only := range keys {
			return only, true
		}
	}
	return nil, false
}

// fetchJWKS downloads and parses a JWKS document into kid -> public key.
func fetchJWKS(ctx context.Context, client *http.Client, url string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch jwks: status %d", res.StatusCode)
	}
	var doc struct {
		Keys []subjectJWK `json:"keys"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode jwks: %w", err)
	}
	keys := make(map[string]any, len(doc.Keys))
	for _, k := range doc.Keys {
		pub, err := k.publicKey()
		if err != nil {
			continue // skip keys we cannot use rather than failing the set
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("jwks contained no usable keys")
	}
	return keys, nil
}

// subjectJWK is the subset of a JSON Web Key we accept from external IdPs.
// (The package's own `jwk` type in keys.go is the RSA-only shape agentiam
// PUBLISHES; this one also parses EC keys we CONSUME.)
type subjectJWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Crv string `json:"crv"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (k subjectJWK) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		nb, err := b64d(k.N)
		if err != nil {
			return nil, err
		}
		eb, err := b64d(k.E)
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
		xb, err := b64d(k.X)
		if err != nil {
			return nil, err
		}
		yb, err := b64d(k.Y)
		if err != nil {
			return nil, err
		}
		curve, err := subjectCurve(k.Crv)
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

// --- crypto helpers (private mirrors of internal/auth's unexported ones) ---

func subjectAlgAllowed(alg string) bool {
	switch alg {
	case "RS256", "RS384", "RS512", "ES256", "ES384", "ES512":
		return true
	}
	return false
}

// verifyJWS checks a JWS signature: RSA PKCS#1v1.5 for RS*, ECDSA over the
// raw r||s pair (not ASN.1) for ES*.
func verifyJWS(alg string, key crypto.PublicKey, signing, sig []byte) error {
	h, hash := subjectHasher(alg)
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

func subjectHasher(alg string) (interface {
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

// b64d decodes base64url without padding (the encode half, b64u, lives in
// jwt.go).
func b64d(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// numericDate reads a NumericDate claim (seconds since epoch).
func numericDate(v any) (time.Time, bool) {
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

// subjectCurve maps a JWK curve name to its elliptic curve.
func subjectCurve(crv string) (elliptic.Curve, error) {
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

// parseTrustedIssuers implements ParseTrustedIssuers (subject.go).
func parseTrustedIssuers(s string) ([]TrustedIssuer, error) {
	var out []TrustedIssuer
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		iss, jwks, ok := strings.Cut(pair, "=")
		if !ok || iss == "" || jwks == "" {
			return nil, fmt.Errorf("bad trusted-issuer entry %q (want issuer=jwks_url)", pair)
		}
		out = append(out, TrustedIssuer{Issuer: iss, JWKSURL: jwks})
	}
	return out, nil
}
