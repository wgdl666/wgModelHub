package apikeystore

import (
	"testing"
)

func TestParseBearerToken(t *testing.T) {
	keyID, secret, ok := ParseBearerToken("Bearer wgmh_abc-def_sekret")
	if !ok || keyID != "abc-def" || secret != "sekret" {
		t.Fatalf("parse failed: ok=%v keyID=%q secret=%q", ok, keyID, secret)
	}
	if _, _, ok := ParseBearerToken("Bearer bad"); ok {
		t.Fatal("expected invalid token")
	}
	if _, _, ok := ParseBearerToken("wgmh_abc-def_sekret"); ok {
		t.Fatal("bare key without Bearer scheme must be rejected")
	}
}

func TestSecretsMatchConstantTime(t *testing.T) {
	secret := "abc123"
	stored := HashSecret(secret)
	if !SecretsMatch(stored, secret) {
		t.Fatal("expected match")
	}
	if SecretsMatch(stored, "wrong") {
		t.Fatal("expected mismatch")
	}
}
