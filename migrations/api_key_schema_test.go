package migrations

import (
	"os"
	"strings"
	"testing"
)

func TestAPIKeyMigrationsTargetModelhubSchema(t *testing.T) {
	migration002, err := os.ReadFile("002_modelhub_api_key.sql")
	if err != nil {
		t.Fatalf("read 002 migration: %v", err)
	}
	migration003, err := os.ReadFile("003_modelhub_api_key_expires_nullable.sql")
	if err != nil {
		t.Fatalf("read 003 migration: %v", err)
	}

	for _, fragment := range []string{
		"CREATE SCHEMA IF NOT EXISTS modelhub",
		"CREATE TABLE IF NOT EXISTS modelhub.modelhub_api_key",
		"ON modelhub.modelhub_api_key",
	} {
		if !strings.Contains(string(migration002), fragment) {
			t.Fatalf("002 migration missing %q", fragment)
		}
	}
	if !strings.Contains(string(migration003), "ALTER TABLE modelhub.modelhub_api_key") {
		t.Fatal("003 migration must alter modelhub.modelhub_api_key")
	}
	if strings.Contains(string(migration002), "CREATE TABLE IF NOT EXISTS modelhub_api_key") {
		t.Fatal("002 migration must not create an unqualified modelhub_api_key table")
	}
	if strings.Contains(string(migration003), "ALTER TABLE modelhub_api_key") {
		t.Fatal("003 migration must not alter an unqualified modelhub_api_key table")
	}
}
