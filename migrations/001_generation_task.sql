-- 视频长任务最小技术表：只存可跨 Pod 查询的归因与上游定位信息。
-- 不保存 prompt、媒体正文或最终视频；不在进程启动时自动执行本文件。
CREATE SCHEMA IF NOT EXISTS modelhub;

CREATE TABLE IF NOT EXISTS modelhub.generation_task (
    task_id text PRIMARY KEY,
    caller text NOT NULL,
    request_id text NOT NULL,
    request_hash text NOT NULL,
    model text NOT NULL,
    provider text NOT NULL,
    provider_task_id text NOT NULL DEFAULT '',
    state text NOT NULL,
    error_code integer NOT NULL DEFAULT 0,
    error_message text NOT NULL DEFAULT '',
    error_reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (caller, request_id)
);
