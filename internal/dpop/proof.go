package dpop

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// newProof builds and signs an RFC 9449 §4.2 DPoP proof JWS. The header embeds
// the PUBLIC half of key as its jwk; the private parameters never leave the
// caller's runtime — that asymmetry is the entire point of DPoP.
func newProof(key crypto.Signer, htm, htu, accessToken string, iat time.Time) (string, error) {
	var alg string
	var pub map[string]string
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		a, j, err := ecPublicJWK(&k.PublicKey)
		if err != nil {
			return "", err
		}
		alg, pub = a, j
	case *rsa.PrivateKey:
		alg, pub = "RS256", rsaPublicJWK(&k.PublicKey)
	default:
		return "", fmt.Errorf("dpop: unsupported key type %T", key)
	}

	hdr := struct {
		Typ string            `json:"typ"`
		Alg string            `json:"alg"`
		JWK map[string]string `json:"jwk"`
	}{Typ: "dpop+jwt", Alg: alg, JWK: pub}

	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("dpop: jti entropy: %w", err)
	}
	claims := map[string]any{
		"jti": base64.RawURLEncoding.EncodeToString(jti),
		"htm": htm,
		"htu": htu,
		"iat": iat.Unix(),
	}
	if accessToken != "" {
		claims["ath"] = accessTokenHash(accessToken)
	}

	hb, err := json.Marshal(hdr)
	if err != nil {
		return "", fmt.Errorf("dpop: marshal header: %w", err)
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("dpop: marshal claims: %w", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	digest := sha256.Sum256([]byte(signingInput))

	var sig []byte
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		// JWS ES* signatures are the raw fixed-width r||s pair, not ASN.1.
		r, s, err := ecdsa.Sign(rand.Reader, k, digest[:])
		if err != nil {
			return "", fmt.Errorf("dpop: ecdsa sign: %w", err)
		}
		size := (k.Curve.Params().BitSize + 7) / 8
		sig = make([]byte, 2*size)
		r.FillBytes(sig[:size])
		s.FillBytes(sig[size:])
	case *rsa.PrivateKey:
		sig, err = rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, digest[:])
		if err != nil {
			return "", fmt.Errorf("dpop: rsa sign: %w", err)
		}
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ecPublicJWK returns the JWS alg and public JWK members for an EC key.
func ecPublicJWK(pub *ecdsa.PublicKey) (string, map[string]string, error) {
	var alg, crv string
	switch pub.Curve.Params().Name {
	case "P-256":
		alg, crv = "ES256", "P-256"
	case "P-384":
		alg, crv = "ES384", "P-384"
	case "P-521":
		alg, crv = "ES512", "P-521"
	default:
		return "", nil, fmt.Errorf("dpop: unsupported curve %q", pub.Curve.Params().Name)
	}
	size := (pub.Curve.Params().BitSize + 7) / 8
	x := make([]byte, size)
	y := make([]byte, size)
	pub.X.FillBytes(x)
	pub.Y.FillBytes(y)
	return alg, map[string]string{
		"kty": "EC",
		"crv": crv,
		"x":   base64.RawURLEncoding.EncodeToString(x),
		"y":   base64.RawURLEncoding.EncodeToString(y),
	}, nil
}

// rsaPublicJWK returns the public JWK members for an RSA key.
func rsaPublicJWK(pub *rsa.PublicKey) map[string]string {
	e := big3(pub.E)
	return map[string]string{
		"kty": "RSA",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(e),
	}
}

// big3 encodes a small public exponent as its minimal big-endian bytes.
func big3(e int) []byte {
	if e == 0 {
		return []byte{0}
	}
	var out []byte
	for v := e; v > 0; v >>= 8 {
		out = append([]byte{byte(v)}, out...)
	}
	return out
}
