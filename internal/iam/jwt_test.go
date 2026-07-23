package iam

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func jwtTestClaims(now time.Time) Claims {
	return Claims{
		Issuer:    "https://iam.example",
		Subject:   "deployer",
		Audience:  "https://mesh.example/mcp",
		Workspace: "team",
		Kind:      "agent",
		Scope:     "mesh:send mesh:read",
		IssuedAt:  now.Unix(),
		NotBefore: now.Unix(),
		Expiry:    now.Add(15 * time.Minute).Unix(),
		JTI:       "jti-123",
	}
}

func TestSignShapeHeaderAndClaims(t *testing.T) {
	key, err := GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	ks := NewKeySet(key)
	now := time.Now().UTC()
	in := jwtTestClaims(now)

	token, err := ks.Sign(in)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3", len(parts))
	}

	// Header: alg RS256, typ at+jwt (RFC 9068), the active kid.
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hb, &hdr); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if hdr.Alg != "RS256" {
		t.Errorf("alg = %q, want RS256", hdr.Alg)
	}
	if hdr.Typ != "at+jwt" {
		t.Errorf("typ = %q, want at+jwt", hdr.Typ)
	}
	if hdr.Kid != key.Kid {
		t.Errorf("kid = %q, want %q", hdr.Kid, key.Kid)
	}

	// Claims round-trip through the payload segment.
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var got Claims
	if err := json.Unmarshal(pb, &got); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	// Claims now contains map/pointer fields (Entitlements, Act, Cnf) that are
	// not == comparable; reflect.DeepEqual handles the whole struct.
	if !reflect.DeepEqual(got, in) {
		t.Errorf("claims did not round-trip:\n got: %+v\nwant: %+v", got, in)
	}
}

func TestSignSignatureVerifies(t *testing.T) {
	key, err := GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	ks := NewKeySet(key)
	token, err := ks.Sign(jwtTestClaims(time.Now().UTC()))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	pub := &key.private.PublicKey
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("signature does not verify with the public key: %v", err)
	}

	// Sanity: a flipped payload byte must fail verification.
	tampered := []byte(parts[1])
	tampered[0] ^= 0x01
	badDigest := sha256.Sum256([]byte(parts[0] + "." + string(tampered)))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, badDigest[:], sig); err == nil {
		t.Fatal("signature verified over a tampered payload")
	}
}

func TestNewJTIUnique(t *testing.T) {
	a, err := newJTI()
	if err != nil {
		t.Fatalf("newJTI: %v", err)
	}
	b, err := newJTI()
	if err != nil {
		t.Fatalf("newJTI: %v", err)
	}
	if a == b {
		t.Fatalf("two JTIs collided: %q", a)
	}
}

func TestNormalizeScopes(t *testing.T) {
	got := normalizeScopes("  a b a  c b ")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("normalizeScopes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeScopes = %v, want %v", got, want)
		}
	}
	if out := normalizeScopes("   "); out != nil {
		t.Fatalf("normalizeScopes(blank) = %v, want nil", out)
	}
}
