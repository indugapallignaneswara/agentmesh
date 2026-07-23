package dpop

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash"
	"math/big"
	"net/url"
	"strings"
	"time"
)

// verifyProof implements Verify (see dpop.go for the contract). All failures
// collapse to ErrInvalidProof — a short reason is wrapped in for logs, but the
// sentinel is the only signal callers may branch on (no verification oracle).
func verifyProof(proofJWT string, p Params) (Proof, error) {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	leeway := p.Leeway
	if leeway == 0 {
		leeway = DefaultLeeway
	}

	parts := strings.Split(proofJWT, ".")
	if len(parts) != 3 {
		return Proof{}, fmt.Errorf("%w: not a compact JWS", ErrInvalidProof)
	}

	hb, err := b64d(parts[0])
	if err != nil {
		return Proof{}, fmt.Errorf("%w: bad header encoding", ErrInvalidProof)
	}
	var hdr struct {
		Typ string          `json:"typ"`
		Alg string          `json:"alg"`
		JWK json.RawMessage `json:"jwk"`
	}
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return Proof{}, fmt.Errorf("%w: bad header JSON", ErrInvalidProof)
	}
	// RFC 9449 §4.3: exactly this typ — a proof is never any other kind of JWT.
	if hdr.Typ != "dpop+jwt" {
		return Proof{}, fmt.Errorf("%w: typ %q is not dpop+jwt", ErrInvalidProof, hdr.Typ)
	}
	// Asymmetric allowlist only. An HMAC "proof" would be forgeable by anyone
	// holding the (public!) embedded jwk — i.e. by whoever stole the token.
	if !allowedAlg(hdr.Alg) {
		return Proof{}, fmt.Errorf("%w: alg %q not allowed", ErrInvalidProof, hdr.Alg)
	}
	if len(hdr.JWK) == 0 {
		return Proof{}, fmt.Errorf("%w: header has no jwk", ErrInvalidProof)
	}
	var raw map[string]any
	if err := json.Unmarshal(hdr.JWK, &raw); err != nil {
		return Proof{}, fmt.Errorf("%w: bad jwk JSON", ErrInvalidProof)
	}
	// The jwk MUST be public-only; private material here means a client is
	// leaking its key on the wire.
	for _, priv := range []string{"d", "p", "q", "dp", "dq", "qi", "k", "oth"} {
		if _, ok := raw[priv]; ok {
			return Proof{}, fmt.Errorf("%w: jwk contains private member %q", ErrInvalidProof, priv)
		}
	}
	var key proofJWK
	if err := json.Unmarshal(hdr.JWK, &key); err != nil {
		return Proof{}, fmt.Errorf("%w: bad jwk", ErrInvalidProof)
	}
	pub, err := key.publicKey()
	if err != nil {
		return Proof{}, fmt.Errorf("%w: unusable jwk: %v", ErrInvalidProof, err)
	}

	sig, err := b64d(parts[2])
	if err != nil {
		return Proof{}, fmt.Errorf("%w: bad signature encoding", ErrInvalidProof)
	}
	if err := verifySig(hdr.Alg, pub, []byte(parts[0]+"."+parts[1]), sig); err != nil {
		return Proof{}, fmt.Errorf("%w: bad signature", ErrInvalidProof)
	}

	cb, err := b64d(parts[1])
	if err != nil {
		return Proof{}, fmt.Errorf("%w: bad claims encoding", ErrInvalidProof)
	}
	var claims struct {
		JTI string   `json:"jti"`
		HTM string   `json:"htm"`
		HTU string   `json:"htu"`
		IAT *float64 `json:"iat"`
		ATH string   `json:"ath"`
	}
	if err := json.Unmarshal(cb, &claims); err != nil {
		return Proof{}, fmt.Errorf("%w: bad claims JSON", ErrInvalidProof)
	}

	if strings.ToUpper(claims.HTM) != strings.ToUpper(p.HTM) {
		return Proof{}, fmt.Errorf("%w: htm mismatch", ErrInvalidProof)
	}
	if normalizeHTU(claims.HTU) != normalizeHTU(p.HTU) {
		return Proof{}, fmt.Errorf("%w: htu mismatch", ErrInvalidProof)
	}
	if claims.IAT == nil {
		return Proof{}, fmt.Errorf("%w: missing iat", ErrInvalidProof)
	}
	iat := time.Unix(int64(*claims.IAT), 0)
	n := now()
	if iat.Before(n.Add(-leeway)) || iat.After(n.Add(leeway)) {
		return Proof{}, fmt.Errorf("%w: iat outside acceptance window", ErrInvalidProof)
	}
	if claims.JTI == "" {
		return Proof{}, fmt.Errorf("%w: missing jti", ErrInvalidProof)
	}
	if p.ExpectedATH != "" {
		if subtle.ConstantTimeCompare([]byte(claims.ATH), []byte(p.ExpectedATH)) != 1 {
			return Proof{}, fmt.Errorf("%w: ath mismatch", ErrInvalidProof)
		}
	}

	// Replay check LAST: an otherwise-invalid proof must never consume a jti,
	// or an attacker could burn a victim's in-flight proof by replaying a
	// mangled copy of it first.
	if p.Replay != nil && p.Replay.SeenBefore(claims.JTI, n) {
		return Proof{}, fmt.Errorf("%w: replayed jti", ErrInvalidProof)
	}

	jkt, err := key.thumbprint()
	if err != nil {
		return Proof{}, fmt.Errorf("%w: thumbprint: %v", ErrInvalidProof, err)
	}
	return Proof{
		JKT: jkt,
		JTI: claims.JTI,
		HTM: claims.HTM,
		HTU: claims.HTU,
		IAT: iat,
		ATH: claims.ATH,
	}, nil
}

// accessTokenHash implements AccessTokenHash: base64url(SHA-256(token)), no
// padding (RFC 9449 §4.2 `ath`).
func accessTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func memReplaySeenBefore(g *MemReplayGuard, jti string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if exp, ok := g.seen[jti]; ok && now.Before(exp) {
		return true
	}
	g.seen[jti] = now.Add(g.window)
	return false
}

// normalizeHTU canonicalizes an htu for comparison: scheme and host lowercased,
// query and fragment stripped (RFC 9449 §4.3 compares the URI without them).
func normalizeHTU(htu string) string {
	u, err := url.Parse(htu)
	if err != nil {
		return htu
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

// proofJWK is the subset of a JSON Web Key a DPoP proof may embed. It mirrors
// internal/auth's jwk (unexported there, so reimplemented here).
type proofJWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (k proofJWK) publicKey() (crypto.PublicKey, error) {
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
		curve, err := curveByName(k.Crv)
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

// thumbprint computes the RFC 7638 SHA-256 JWK thumbprint: base64url-nopad of
// the SHA-256 of the canonical JSON — REQUIRED members only, lexicographic
// order, no whitespace. Issuer and resource server both derive cnf.jkt from
// this, so the byte layout must never drift.
func (k proofJWK) thumbprint() (string, error) {
	var canonical string
	switch k.Kty {
	case "EC":
		if k.Crv == "" || k.X == "" || k.Y == "" {
			return "", fmt.Errorf("EC jwk missing required members")
		}
		canonical = `{"crv":"` + k.Crv + `","kty":"EC","x":"` + k.X + `","y":"` + k.Y + `"}`
	case "RSA":
		if k.N == "" || k.E == "" {
			return "", fmt.Errorf("RSA jwk missing required members")
		}
		canonical = `{"e":"` + k.E + `","kty":"RSA","n":"` + k.N + `"}`
	default:
		return "", fmt.Errorf("unsupported kty %q", k.Kty)
	}
	sum := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// --- crypto helpers (mirroring internal/auth/oauth.go, unexported there) ---

func allowedAlg(alg string) bool {
	switch alg {
	case "RS256", "RS384", "RS512", "ES256", "ES384", "ES512":
		return true
	}
	return false
}

func verifySig(alg string, key crypto.PublicKey, signing, sig []byte) error {
	h, hs := hashFor(alg)
	h.Write(signing)
	digest := h.Sum(nil)

	switch {
	case strings.HasPrefix(alg, "RS"):
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("key type mismatch")
		}
		return rsa.VerifyPKCS1v15(pub, hs, digest, sig)
	case strings.HasPrefix(alg, "ES"):
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("key type mismatch")
		}
		// JWS ES* signatures are the raw r||s pair, not ASN.1.
		n := len(sig) / 2
		if n == 0 || len(sig)%2 != 0 {
			return fmt.Errorf("bad ecdsa signature length")
		}
		r := new(big.Int).SetBytes(sig[:n])
		s := new(big.Int).SetBytes(sig[n:])
		if !ecdsa.Verify(pub, digest, r, s) {
			return fmt.Errorf("ecdsa verify failed")
		}
		return nil
	}
	return fmt.Errorf("unsupported alg")
}

func hashFor(alg string) (hash.Hash, crypto.Hash) {
	switch alg[len(alg)-3:] {
	case "384":
		return sha512.New384(), crypto.SHA384
	case "512":
		return sha512.New(), crypto.SHA512
	default:
		return sha256.New(), crypto.SHA256
	}
}

func curveByName(crv string) (elliptic.Curve, error) {
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

func b64d(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
