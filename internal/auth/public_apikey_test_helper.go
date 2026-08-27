package auth

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
	"github.com/wgdl666/wgModelHub/internal/apikeystore"

	_ "modernc.org/sqlite"
)

type testKeyMaterial struct {
	PrincipalID string
	KeyID       string
	Secret      string
	Bearer      string
}

func openAuthTestStore(t *testing.T) (*apikeystore.Store, testKeyMaterial) {
	store, mat, _ := openAuthTestStoreEx(t)
	return store, mat
}

func openAuthTestStoreEx(t *testing.T) (*apikeystore.Store, testKeyMaterial, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:auth-key-"+t.Name()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(ent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	secret := "test-secret-" + uuid.NewString()
	keyID := uuid.NewString()
	principalID := uuid.NewString()
	_, err = client.ModelhubAPIKey.Create().
		SetID(uuid.NewString()).
		SetPrincipalID(principalID).
		SetKeyID(keyID).
		SetSecretSha256(apikeystore.HashSecret(secret)).
		SetName("").
		SetCreatedBy("tester").
		SetCreatedAt(time.Now().UTC()).
		SetExpiresAt(time.Now().UTC().Add(24 * time.Hour)).
		Save(context.Background())
	if err != nil {
		t.Fatalf("insert test key: %v", err)
	}
	mat := testKeyMaterial{
		PrincipalID: principalID,
		KeyID:       keyID,
		Secret:      secret,
		Bearer:      "Bearer wgmh_" + keyID + "_" + secret,
	}
	return apikeystore.New(client), mat, db
}
