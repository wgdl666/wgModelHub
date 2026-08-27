package apikeystore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/wgdl666/wgModelHub/ent"
	"github.com/wgdl666/wgModelHub/ent/enttest"

	_ "modernc.org/sqlite"
)

func openTestClient(t *testing.T) *ent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", "file:api-key-"+t.Name()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(ent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func insertTestKey(t *testing.T, client *ent.Client, secret string, expiresAt time.Time, revokedAt *time.Time) (keyID string) {
	t.Helper()
	keyID = uuid.NewString()
	_, err := client.ModelhubAPIKey.Create().
		SetID(uuid.NewString()).
		SetPrincipalID(uuid.NewString()).
		SetKeyID(keyID).
		SetSecretSha256(HashSecret(secret)).
		SetName("demo").
		SetCreatedBy("alice").
		SetCreatedAt(time.Now().UTC()).
		SetExpiresAt(expiresAt).
		SetNillableRevokedAt(revokedAt).
		Save(context.Background())
	if err != nil {
		t.Fatalf("insert test key: %v", err)
	}
	return keyID
}

func bearerToken(keyID, secret string) string {
	return "Bearer wgmh_" + keyID + "_" + secret
}

func TestAuthenticateActiveKey(t *testing.T) {
	client := openTestClient(t)
	store := New(client)
	secret := "active-secret"
	keyID := insertTestKey(t, client, secret, time.Now().UTC().Add(24*time.Hour), nil)
	principal, err := store.Authenticate(context.Background(), bearerToken(keyID, secret))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if principal.KeyID != keyID {
		t.Fatalf("unexpected principal: %+v", principal)
	}
}

func TestAuthenticateWrongSecret(t *testing.T) {
	client := openTestClient(t)
	store := New(client)
	keyID := insertTestKey(t, client, "real-secret", time.Now().UTC().Add(24*time.Hour), nil)
	if _, err := store.Authenticate(context.Background(), bearerToken(keyID, "wrong-secret")); err != ErrInvalid {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestAuthenticateExpiredKey(t *testing.T) {
	client := openTestClient(t)
	store := New(client)
	secret := "expired-secret"
	keyID := insertTestKey(t, client, secret, time.Now().UTC().Add(-time.Hour), nil)
	if _, err := store.Authenticate(context.Background(), bearerToken(keyID, secret)); err != ErrInvalid {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestAuthenticateRevokedKey(t *testing.T) {
	client := openTestClient(t)
	store := New(client)
	secret := "revoked-secret"
	revoked := time.Now().UTC().Add(-time.Minute)
	keyID := insertTestKey(t, client, secret, time.Now().UTC().Add(24*time.Hour), &revoked)
	if _, err := store.Authenticate(context.Background(), bearerToken(keyID, secret)); err != ErrInvalid {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestAuthenticateMissingBearerScheme(t *testing.T) {
	client := openTestClient(t)
	store := New(client)
	secret := "bare-secret"
	keyID := insertTestKey(t, client, secret, time.Now().UTC().Add(24*time.Hour), nil)
	if _, err := store.Authenticate(context.Background(), "wgmh_"+keyID+"_"+secret); err != ErrInvalid {
		t.Fatalf("expected ErrInvalid for bare key, got %v", err)
	}
}
