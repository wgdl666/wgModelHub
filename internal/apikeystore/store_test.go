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

func insertTestKey(t *testing.T, client *ent.Client, secret string, expiresAt *time.Time, revokedAt *time.Time) (keyID string) {
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
		SetNillableExpiresAt(expiresAt).
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

func futureExpiry() *time.Time {
	t := time.Now().UTC().Add(24 * time.Hour)
	return &t
}

func pastExpiry() *time.Time {
	t := time.Now().UTC().Add(-time.Hour)
	return &t
}

func TestAuthenticateActiveKey(t *testing.T) {
	client := openTestClient(t)
	store := New(client)
	secret := "active-secret"
	keyID := insertTestKey(t, client, secret, futureExpiry(), nil)
	principal, err := store.Authenticate(context.Background(), bearerToken(keyID, secret))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if principal.KeyID != keyID {
		t.Fatalf("unexpected principal: %+v", principal)
	}
}

func TestAuthenticateUnlimitedKey(t *testing.T) {
	client := openTestClient(t)
	store := New(client)
	secret := "unlimited-secret"
	// expires_at NULL = 永不过期，未吊销应通过。
	keyID := insertTestKey(t, client, secret, nil, nil)
	principal, err := store.Authenticate(context.Background(), bearerToken(keyID, secret))
	if err != nil {
		t.Fatalf("Authenticate unlimited: %v", err)
	}
	if principal.KeyID != keyID {
		t.Fatalf("unexpected principal: %+v", principal)
	}
}

func TestAuthenticateWrongSecret(t *testing.T) {
	client := openTestClient(t)
	store := New(client)
	keyID := insertTestKey(t, client, "real-secret", futureExpiry(), nil)
	if _, err := store.Authenticate(context.Background(), bearerToken(keyID, "wrong-secret")); err != ErrInvalid {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestAuthenticateExpiredKey(t *testing.T) {
	client := openTestClient(t)
	store := New(client)
	secret := "expired-secret"
	keyID := insertTestKey(t, client, secret, pastExpiry(), nil)
	if _, err := store.Authenticate(context.Background(), bearerToken(keyID, secret)); err != ErrInvalid {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestAuthenticateRevokedKey(t *testing.T) {
	client := openTestClient(t)
	store := New(client)
	secret := "revoked-secret"
	revoked := time.Now().UTC().Add(-time.Minute)
	keyID := insertTestKey(t, client, secret, futureExpiry(), &revoked)
	if _, err := store.Authenticate(context.Background(), bearerToken(keyID, secret)); err != ErrInvalid {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestAuthenticateRevokedUnlimitedKey(t *testing.T) {
	client := openTestClient(t)
	store := New(client)
	secret := "revoked-unlimited"
	revoked := time.Now().UTC().Add(-time.Minute)
	// 吊销优先于永不过期：NULL expires_at 仍应拒绝。
	keyID := insertTestKey(t, client, secret, nil, &revoked)
	if _, err := store.Authenticate(context.Background(), bearerToken(keyID, secret)); err != ErrInvalid {
		t.Fatalf("expected ErrInvalid for revoked unlimited key, got %v", err)
	}
}

func TestAuthenticateMissingBearerScheme(t *testing.T) {
	client := openTestClient(t)
	store := New(client)
	secret := "bare-secret"
	keyID := insertTestKey(t, client, secret, futureExpiry(), nil)
	if _, err := store.Authenticate(context.Background(), "wgmh_"+keyID+"_"+secret); err != ErrInvalid {
		t.Fatalf("expected ErrInvalid for bare key, got %v", err)
	}
}
