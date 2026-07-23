package dpop

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testHTM = "POST"
	testHTU = "https://mesh.example.com/v1/rooms/abc/messages"
)

func testECKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate EC key: %v", err)
	}
	return k
}

func testRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return k
}

func params(now time.Time) Params {
	return Params{HTM: testHTM, HTU: testHTU, Now: func() time.Time { return now }}
}

func mustProof(t *testing.T, key crypto.Signer, htm, htu, at string, iat time.Time) string {
	t.Helper()
	p, err := NewProof(key, htm, htu, at, iat)
	if err != nil {
		t.Fatalf("NewProof: %v", err)
	}
	return p
}

func wantInvalid(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("%s: want ErrInvalidProof, got %v", what, err)
	}
}

func TestRoundTripES256(t *testing.T) {
	key := testECKey(t)
	now := time.Now()
	proof := mustProof(t, key, testHTM, testHTU, "", now)

	got, err := Verify(proof, params(now))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.JKT == "" {
		t.Fatal("JKT is empty")
	}
	if got.JTI == "" {
		t.Fatal("JTI is empty")
	}
	if got.HTM != testHTM || got.HTU != testHTU {
		t.Fatalf("HTM/HTU = %q/%q", got.HTM, got.HTU)
	}
	if got.IAT.Unix() != now.Unix() {
		t.Fatalf("IAT = %v, want %v", got.IAT.Unix(), now.Unix())
	}
}

func TestRoundTripRS256(t *testing.T) {
	key := testRSAKey(t)
	now := time.Now()
	proof := mustProof(t, key, testHTM, testHTU, "", now)

	got, err := Verify(proof, params(now))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.JKT == "" {
		t.Fatal("JKT is empty")
	}
}

func TestJKTStableAcrossProofsAndDistinctAcrossKeys(t *testing.T) {
	key := testECKey(t)
	other := testECKey(t)
	now := time.Now()

	p1, err := Verify(mustProof(t, key, testHTM, testHTU, "", now), params(now))
	if err != nil {
		t.Fatalf("Verify p1: %v", err)
	}
	p2, err := Verify(mustProof(t, key, testHTM, testHTU, "", now), params(now))
	if err != nil {
		t.Fatalf("Verify p2: %v", err)
	}
	if p1.JKT != p2.JKT {
		t.Fatalf("same key produced different JKTs: %q vs %q", p1.JKT, p2.JKT)
	}
	p3, err := Verify(mustProof(t, other, testHTM, testHTU, "", now), params(now))
	if err != nil {
		t.Fatalf("Verify p3: %v", err)
	}
	if p3.JKT == p1.JKT {
		t.Fatal("different keys produced the same JKT")
	}

	r1, err := Verify(mustProof(t, testRSAKey(t), testHTM, testHTU, "", now), params(now))
	if err != nil {
		t.Fatalf("Verify rsa: %v", err)
	}
	if r1.JKT == p1.JKT {
		t.Fatal("RSA and EC keys produced the same JKT")
	}
}

// TestThumbprintRFC7638KAT pins the RSA thumbprint against the known-answer
// example in RFC 7638 §3.1.
func TestThumbprintRFC7638KAT(t *testing.T) {
	k := proofJWK{
		Kty: "RSA",
		N: "0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMst" +
			"n64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbI" +
			"SD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw",
		E: "AQAB",
	}
	jkt, err := k.thumbprint()
	if err != nil {
		t.Fatalf("thumbprint: %v", err)
	}
	const want = "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs"
	if jkt != want {
		t.Fatalf("RFC 7638 KAT: got %q, want %q", jkt, want)
	}
}

// TestThumbprintCanonicalJSONPinned pins the exact canonical byte layouts
// (member set, lexicographic order, no whitespace) so they cannot silently
// change: the issuer's cnf.jkt and the RS's check both depend on them.
func TestThumbprintCanonicalJSONPinned(t *testing.T) {
	ec := proofJWK{Kty: "EC", Crv: "P-256", X: "xVAL", Y: "yVAL"}
	wantECJSON := `{"crv":"P-256","kty":"EC","x":"xVAL","y":"yVAL"}`
	sum := sha256.Sum256([]byte(wantECJSON))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	got, err := ec.thumbprint()
	if err != nil {
		t.Fatalf("EC thumbprint: %v", err)
	}
	if got != want {
		t.Fatalf("EC canonical layout drifted: thumbprint %q != hash of %s", got, wantECJSON)
	}

	rsaK := proofJWK{Kty: "RSA", N: "nVAL", E: "eVAL"}
	wantRSAJSON := `{"e":"eVAL","kty":"RSA","n":"nVAL"}`
	sum = sha256.Sum256([]byte(wantRSAJSON))
	want = base64.RawURLEncoding.EncodeToString(sum[:])
	got, err = rsaK.thumbprint()
	if err != nil {
		t.Fatalf("RSA thumbprint: %v", err)
	}
	if got != want {
		t.Fatalf("RSA canonical layout drifted: thumbprint %q != hash of %s", got, wantRSAJSON)
	}
}

// signCustom builds a compact JWS with arbitrary header and claims, ES256-signed
// by key — for forging structurally wrong proofs the public API cannot produce.
func signCustom(t *testing.T, key *ecdsa.PrivateKey, hdr, claims map[string]any) string {
	t.Helper()
	hb, err := json.Marshal(hdr)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	input := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func publicJWKMap(t *testing.T, key *ecdsa.PrivateKey) map[string]any {
	t.Helper()
	_, j, err := ecPublicJWK(&key.PublicKey)
	if err != nil {
		t.Fatalf("ecPublicJWK: %v", err)
	}
	m := map[string]any{}
	for k, v := range j {
		m[k] = v
	}
	return m
}

func baseClaims(now time.Time) map[string]any {
	return map[string]any{
		"jti": "test-jti-1",
		"htm": testHTM,
		"htu": testHTU,
		"iat": now.Unix(),
	}
}

func TestRejects(t *testing.T) {
	key := testECKey(t)
	now := time.Now()
	jwkMap := publicJWKMap(t, key)
	goodHdr := func() map[string]any {
		return map[string]any{"typ": "dpop+jwt", "alg": "ES256", "jwk": publicJWKMap(t, key)}
	}

	t.Run("tampered signature", func(t *testing.T) {
		proof := mustProof(t, key, testHTM, testHTU, "", now)
		i := strings.LastIndex(proof, ".")
		sig := []byte(proof[i+1:])
		if sig[0] == 'A' {
			sig[0] = 'B'
		} else {
			sig[0] = 'A'
		}
		_, err := Verify(proof[:i+1]+string(sig), params(now))
		wantInvalid(t, err, "tampered signature")
	})

	t.Run("alg none", func(t *testing.T) {
		hdr := goodHdr()
		hdr["alg"] = "none"
		_, err := Verify(signCustom(t, key, hdr, baseClaims(now)), params(now))
		wantInvalid(t, err, "alg none")
	})

	t.Run("alg HS256", func(t *testing.T) {
		hdr := goodHdr()
		hdr["alg"] = "HS256"
		_, err := Verify(signCustom(t, key, hdr, baseClaims(now)), params(now))
		wantInvalid(t, err, "alg HS256")
	})

	t.Run("wrong typ", func(t *testing.T) {
		hdr := goodHdr()
		hdr["typ"] = "JWT"
		_, err := Verify(signCustom(t, key, hdr, baseClaims(now)), params(now))
		wantInvalid(t, err, "wrong typ")
	})

	t.Run("jwk with private d", func(t *testing.T) {
		hdr := goodHdr()
		leaky := publicJWKMap(t, key)
		leaky["d"] = base64.RawURLEncoding.EncodeToString(key.D.Bytes())
		hdr["jwk"] = leaky
		_, err := Verify(signCustom(t, key, hdr, baseClaims(now)), params(now))
		wantInvalid(t, err, "private jwk")
	})

	t.Run("missing jwk", func(t *testing.T) {
		_, err := Verify(signCustom(t, key, map[string]any{"typ": "dpop+jwt", "alg": "ES256"}, baseClaims(now)), params(now))
		wantInvalid(t, err, "missing jwk")
	})

	t.Run("wrong htm", func(t *testing.T) {
		proof := mustProof(t, key, "GET", testHTU, "", now)
		_, err := Verify(proof, params(now))
		wantInvalid(t, err, "wrong htm")
	})

	t.Run("wrong htu", func(t *testing.T) {
		proof := mustProof(t, key, testHTM, "https://other.example.com/x", "", now)
		_, err := Verify(proof, params(now))
		wantInvalid(t, err, "wrong htu")
	})

	t.Run("htu with query still matches", func(t *testing.T) {
		proof := mustProof(t, key, testHTM, testHTU+"?cursor=42#frag", "", now)
		if _, err := Verify(proof, params(now)); err != nil {
			t.Fatalf("query/fragment must be stripped by normalization: %v", err)
		}
	})

	t.Run("htu case-insensitive scheme and host", func(t *testing.T) {
		proof := mustProof(t, key, testHTM, "HTTPS://MESH.example.com/v1/rooms/abc/messages", "", now)
		if _, err := Verify(proof, params(now)); err != nil {
			t.Fatalf("scheme/host case must be normalized: %v", err)
		}
	})

	t.Run("iat too old", func(t *testing.T) {
		proof := mustProof(t, key, testHTM, testHTU, "", now.Add(-2*DefaultLeeway))
		_, err := Verify(proof, params(now))
		wantInvalid(t, err, "iat too old")
	})

	t.Run("iat too far future", func(t *testing.T) {
		proof := mustProof(t, key, testHTM, testHTU, "", now.Add(2*DefaultLeeway))
		_, err := Verify(proof, params(now))
		wantInvalid(t, err, "iat future")
	})

	t.Run("missing iat", func(t *testing.T) {
		c := baseClaims(now)
		delete(c, "iat")
		_, err := Verify(signCustom(t, key, map[string]any{"typ": "dpop+jwt", "alg": "ES256", "jwk": jwkMap}, c), params(now))
		wantInvalid(t, err, "missing iat")
	})

	t.Run("missing jti", func(t *testing.T) {
		c := baseClaims(now)
		delete(c, "jti")
		_, err := Verify(signCustom(t, key, map[string]any{"typ": "dpop+jwt", "alg": "ES256", "jwk": jwkMap}, c), params(now))
		wantInvalid(t, err, "missing jti")
	})

	t.Run("garbage", func(t *testing.T) {
		_, err := Verify("not.a.jws.at.all", params(now))
		wantInvalid(t, err, "garbage")
	})
}

func TestAccessTokenHash(t *testing.T) {
	const token = "amt_example_access_token"
	sum := sha256.Sum256([]byte(token))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if got := AccessTokenHash(token); got != want {
		t.Fatalf("AccessTokenHash = %q, want %q", got, want)
	}
	if AccessTokenHash(token) != AccessTokenHash(token) {
		t.Fatal("AccessTokenHash is not stable")
	}
}

func TestATHBinding(t *testing.T) {
	key := testECKey(t)
	now := time.Now()
	const token = "the-access-token"

	proof := mustProof(t, key, testHTM, testHTU, token, now)

	p := params(now)
	p.ExpectedATH = AccessTokenHash(token)
	got, err := Verify(proof, p)
	if err != nil {
		t.Fatalf("Verify with matching ath: %v", err)
	}
	if got.ATH != AccessTokenHash(token) {
		t.Fatalf("ATH = %q, want %q", got.ATH, AccessTokenHash(token))
	}

	p.ExpectedATH = AccessTokenHash("some-other-token")
	_, err = Verify(proof, p)
	wantInvalid(t, err, "wrong ath")

	// A proof with NO ath must fail when the verifier expects one.
	bare := mustProof(t, key, testHTM, testHTU, "", now)
	p.ExpectedATH = AccessTokenHash(token)
	_, err = Verify(bare, p)
	wantInvalid(t, err, "absent ath")
}

func TestReplay(t *testing.T) {
	key := testECKey(t)
	now := time.Now()
	guard := NewMemReplayGuard(DefaultLeeway)
	p := params(now)
	p.Replay = guard

	proof := mustProof(t, key, testHTM, testHTU, "", now)
	if _, err := Verify(proof, p); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	_, err := Verify(proof, p)
	wantInvalid(t, err, "replayed proof")
}

func TestInvalidProofDoesNotConsumeJTI(t *testing.T) {
	key := testECKey(t)
	now := time.Now()
	guard := NewMemReplayGuard(DefaultLeeway)
	p := params(now)
	p.Replay = guard

	proof := mustProof(t, key, testHTM, testHTU, "", now)

	// Tamper the signature: this invalid copy must NOT record the jti.
	i := strings.LastIndex(proof, ".")
	sig := []byte(proof[i+1:])
	if sig[0] == 'A' {
		sig[0] = 'B'
	} else {
		sig[0] = 'A'
	}
	_, err := Verify(proof[:i+1]+string(sig), p)
	wantInvalid(t, err, "tampered copy")

	// The genuine proof with the same jti must still pass.
	if _, err := Verify(proof, p); err != nil {
		t.Fatalf("valid proof after invalid attempt: %v", err)
	}
}

func TestLeewayOverride(t *testing.T) {
	key := testECKey(t)
	now := time.Now()
	proof := mustProof(t, key, testHTM, testHTU, "", now.Add(-30*time.Second))

	p := params(now)
	p.Leeway = 10 * time.Second
	_, err := Verify(proof, p)
	wantInvalid(t, err, "outside custom leeway")

	p.Leeway = 45 * time.Second
	if _, err := Verify(proof, p); err != nil {
		t.Fatalf("within custom leeway: %v", err)
	}
}
