package iam

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
)

// TestKidStableAcrossPEMReload proves the kid is a pure function of the key
// material: marshal a generated key to PKCS#8 PEM, reload it, same Kid. This is
// what keeps issued tokens verifiable across server restarts.
func TestKidStableAcrossPEMReload(t *testing.T) {
	orig, err := GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(orig.private)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	reloaded, err := LoadSigningKeyPEM(pemBytes)
	if err != nil {
		t.Fatalf("LoadSigningKeyPEM: %v", err)
	}
	if reloaded.Kid != orig.Kid {
		t.Fatalf("kid changed across reload: %q -> %q", orig.Kid, reloaded.Kid)
	}
	if orig.Kid == "" {
		t.Fatal("kid is empty")
	}
}

// TestKidStableAcrossPKCS1Reload covers the "RSA PRIVATE KEY" PEM branch too.
func TestKidStableAcrossPKCS1Reload(t *testing.T) {
	orig, err := GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(orig.private),
	})
	reloaded, err := LoadSigningKeyPEM(pemBytes)
	if err != nil {
		t.Fatalf("LoadSigningKeyPEM(PKCS#1): %v", err)
	}
	if reloaded.Kid != orig.Kid {
		t.Fatalf("kid changed across PKCS#1 reload: %q -> %q", orig.Kid, reloaded.Kid)
	}
}

func TestGenerateSigningKeyDistinctKids(t *testing.T) {
	a, err := GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey a: %v", err)
	}
	b, err := GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey b: %v", err)
	}
	if a.Kid == b.Kid {
		t.Fatalf("two fresh keys share kid %q", a.Kid)
	}
}

// TestJWKSRoundTrip decodes the published n/e back into a public key and checks
// it equals the signing key's public half, plus the metadata the resource
// server's jwk parser relies on.
func TestJWKSRoundTrip(t *testing.T) {
	key, err := GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	set := NewKeySet(key).JWKS()
	if len(set.Keys) != 1 {
		t.Fatalf("JWKS has %d keys, want 1", len(set.Keys))
	}
	j := set.Keys[0]
	if j.Kty != "RSA" {
		t.Errorf("kty = %q, want RSA", j.Kty)
	}
	if j.Kid != key.Kid {
		t.Errorf("kid = %q, want %q", j.Kid, key.Kid)
	}
	if j.Alg != "RS256" || j.Use != "sig" {
		t.Errorf("alg/use = %q/%q, want RS256/sig", j.Alg, j.Use)
	}

	nb, err := base64.RawURLEncoding.DecodeString(j.N)
	if err != nil {
		t.Fatalf("decode n: %v", err)
	}
	eb, err := base64.RawURLEncoding.DecodeString(j.E)
	if err != nil {
		t.Fatalf("decode e: %v", err)
	}
	gotN := new(big.Int).SetBytes(nb)
	gotE := int(new(big.Int).SetBytes(eb).Int64())

	pub := key.private.PublicKey
	if gotN.Cmp(pub.N) != 0 {
		t.Error("JWKS n does not round-trip to the public modulus")
	}
	if gotE != pub.E {
		t.Errorf("JWKS e = %d, want %d", gotE, pub.E)
	}
}

func TestLoadSigningKeyPEMRejectsGarbage(t *testing.T) {
	cases := map[string][]byte{
		"not pem at all":  []byte("this is not a pem block"),
		"empty":           nil,
		"pem, wrong type": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk")}),
		"pem, junk pkcs8": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("junk")}),
		"pem, junk pkcs1": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("junk")}),
	}
	for name, in := range cases {
		if _, err := LoadSigningKeyPEM(in); err == nil {
			t.Errorf("%s: LoadSigningKeyPEM accepted invalid input", name)
		}
	}
}

func TestLoadSigningKeyPEMRejectsNonRSA(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatalf("marshal ecdsa pkcs8: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := LoadSigningKeyPEM(pemBytes); err == nil {
		t.Fatal("LoadSigningKeyPEM accepted an ECDSA key; want RSA-only")
	}
}
