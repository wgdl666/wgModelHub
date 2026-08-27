package migrations

import (
	"strings"
	"testing"
)

func TestGenerationTaskSQLTargetsModelhubSchema(t *testing.T) {
	for _, fragment := range []string{
		"CREATE SCHEMA IF NOT EXISTS modelhub",
		"CREATE TABLE IF NOT EXISTS modelhub.generation_task",
	} {
		if !strings.Contains(GenerationTaskSQL, fragment) {
			t.Fatalf("001 migration missing %q", fragment)
		}
	}
	if strings.Contains(GenerationTaskSQL, "CREATE TABLE IF NOT EXISTS generation_task") {
		t.Fatal("001 migration must not create an unqualified generation_task table")
	}
}
