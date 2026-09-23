-- 全模型调用账本：只记录实际触达供应商的生成调用；入站校验/路由失败与 provider 本地拒识不落此表。
-- 输入/输出/用量详情走 JSONB；筛选与聚合用结构化列。不在进程启动时自动执行。
CREATE SCHEMA IF NOT EXISTS modelhub;

CREATE TABLE IF NOT EXISTS modelhub.model_call (
    call_id text PRIMARY KEY,
    -- 异步视频与 generation_task.task_id 对齐；同步调用为 NULL。UNIQUE 保证重复 Submit/Get 只保留一条。
    generation_task_id text UNIQUE,
    caller_service text NOT NULL DEFAULT 'unknown',
    business_scene text NOT NULL DEFAULT 'unknown',
    operation text NOT NULL,
    capability text NOT NULL,
    model text NOT NULL,
    provider text NOT NULL,
    -- pending=已受理未完成（无人 Get 可长期停留）；succeeded/failed/cancelled 为终态。
    status text NOT NULL,
    -- ok=调用方已收到；client_send_failed=供应商结果已产生但最终流式/unary 回传失败。
    delivery_status text NOT NULL DEFAULT 'ok',
    error_category text NOT NULL DEFAULT '',
    error_code text NOT NULL DEFAULT '',
    error_reason text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    -- token 列在供应商未返回 usage 时保持 NULL；返回 0 也是合法观测值，不得与缺失混淆。
    input_tokens bigint NULL,
    output_tokens bigint NULL,
    total_tokens bigint NULL,
    cached_tokens bigint NULL,
    reasoning_tokens bigint NULL,
    image_count integer NULL,
    image_size text NOT NULL DEFAULT '',
    image_aspect_ratio text NOT NULL DEFAULT '',
    video_count integer NULL,
    video_resolution text NOT NULL DEFAULT '',
    video_duration_seconds integer NULL,
    video_aspect_ratio text NOT NULL DEFAULT '',
    latency_ms bigint NOT NULL DEFAULT 0,
    started_at timestamptz NOT NULL,
    finished_at timestamptz NULL,
    input_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    output_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    usage_detail jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS model_call_started_at_idx
    ON modelhub.model_call (started_at DESC);
CREATE INDEX IF NOT EXISTS model_call_caller_started_idx
    ON modelhub.model_call (caller_service, started_at DESC);
CREATE INDEX IF NOT EXISTS model_call_scene_started_idx
    ON modelhub.model_call (business_scene, started_at DESC);
CREATE INDEX IF NOT EXISTS model_call_model_started_idx
    ON modelhub.model_call (model, started_at DESC);
CREATE INDEX IF NOT EXISTS model_call_status_started_idx
    ON modelhub.model_call (status, started_at DESC);
