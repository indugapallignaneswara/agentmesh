package iam

import (
	"strings"
	"testing"
)

func TestHashSecretDeterministic(t *testing.T) {
	a := HashSecret("ags_example-secret")
	b := HashSecret("ags_example-secret")
	if a != b {
		t.Fatalf("HashSecret not deterministic: %q vs %q", a, b)
	}
	if a == HashSecret("ags_other-secret") {
		t.Fatal("different secrets hash identically")
	}
	if len(a) != 64 { // hex SHA-256
		t.Fatalf("hash length = %d, want 64 hex chars", len(a))
	}
}

func TestVerifySecret(t *testing.T) {
	hash := HashSecret("ags_right")
	if !verifySecret("ags_right", hash) {
		t.Error("verifySecret rejected the correct secret")
	}
	if verifySecret("ags_wrong", hash) {
		t.Error("verifySecret accepted a wrong secret")
	}
	if verifySecret("", hash) {
		t.Error("verifySecret accepted an empty secret")
	}
}

func TestGrantScopes(t *testing.T) {
	c := Client{AllowedScopes: []string{"mesh:send", "mesh:read"}}

	// Empty request grants the full allowance.
	got, err := c.grantScopes("")
	if err != nil {
		t.Fatalf("grantScopes(\"\"): %v", err)
	}
	if len(got) != 2 || got[0] != "mesh:send" || got[1] != "mesh:read" {
		t.Fatalf("empty request granted %v, want full allowance", got)
	}

	// Subset request grants exactly the subset.
	got, err = c.grantScopes("mesh:read")
	if err != nil {
		t.Fatalf("grantScopes(subset): %v", err)
	}
	if len(got) != 1 || got[0] != "mesh:read" {
		t.Fatalf("subset request granted %v, want [mesh:read]", got)
	}

	// A non-allowed scope is an error, never a silent downgrade.
	if _, err = c.grantScopes("mesh:read mesh:admin"); err == nil {
		t.Fatal("grantScopes granted a scope outside the allowance")
	}

	// A client with no allowance grants nothing on empty request...
	none := Client{}
	got, err = none.grantScopes("")
	if err != nil {
		t.Fatalf("grantScopes on empty allowance: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty allowance granted %v, want none", got)
	}
	// ...and errors on any explicit request.
	if _, err := none.grantScopes("mesh:read"); err == nil {
		t.Fatal("client with no allowance was granted a scope")
	}
}

func TestGenerateClientCredentials(t *testing.T) {
	id, secret, hash, err := GenerateClientCredentials()
	if err != nil {
		t.Fatalf("GenerateClientCredentials: %v", err)
	}
	if !strings.HasPrefix(id, "agt_") {
		t.Errorf("client id %q lacks agt_ prefix", id)
	}
	if !strings.HasPrefix(secret, "ags_") {
		t.Errorf("secret %q lacks ags_ prefix", secret)
	}
	if !verifySecret(secret, hash) {
		t.Error("returned hash does not verify the returned secret")
	}

	id2, secret2, _, err := GenerateClientCredentials()
	if err != nil {
		t.Fatalf("GenerateClientCredentials: %v", err)
	}
	if id == id2 || secret == secret2 {
		t.Error("two credential generations collided")
	}
}

func TestParseScopeList(t *testing.T) {
	got := ParseScopeList("a,b c ,, d")
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("ParseScopeList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseScopeList = %v, want %v", got, want)
		}
	}
}
