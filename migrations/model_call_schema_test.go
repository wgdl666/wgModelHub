package migrations

import (
	"os"
	"strings"
	"testing"
)

func TestModelCallMigrationTargetsModelhubSchema(t *testing.T) {
	raw, err := os.ReadFile("004_model_call.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	body := string(raw)
	for _, fragment := range []string{
		"CREATE SCHEMA IF NOT EXISTS modelhub",
		"CREATE TABLE IF NOT EXISTS modelhub.model_call",
		"ON modelhub.model_call",
		"generation_task_id text UNIQUE",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("migration missing %q", fragment)
		}
	}
	if strings.Contains(body, "CREATE TABLE IF NOT EXISTS model_call") {
		t.Fatal("migration must not create unqualified model_call")
	}
	if ModelCallSQL == "" {
		t.Fatal("ModelCallSQL embed empty")
	}
}
