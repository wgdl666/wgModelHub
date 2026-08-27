package migrations

import _ "embed"

// GenerationTaskSQL is the only migration used by the internal ModelHub rollout.
//
//go:embed 001_generation_task.sql
var GenerationTaskSQL string
