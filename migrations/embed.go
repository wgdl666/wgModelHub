package migrations

import _ "embed"

// GenerationTaskSQL is the only migration used by the internal ModelHub rollout.
//
//go:embed 001_generation_task.sql
var GenerationTaskSQL string

// ModelCallSQL 创建 modelhub.model_call 调用账本；须显式 migration，禁止启动自动 DDL。
//
//go:embed 004_model_call.sql
var ModelCallSQL string

// GenerationTaskMetadataSQL 为持久任务补充统一传播上下文。
//
//go:embed 005_generation_task_metadata.sql
var GenerationTaskMetadataSQL string
