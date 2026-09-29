-- 先迁移各环境账本，再发布读取子场景的服务；不推断历史模型的业务用途。
ALTER TABLE modelhub.model_call ADD COLUMN IF NOT EXISTS business_subscene text NOT NULL DEFAULT 'unknown';
CREATE INDEX IF NOT EXISTS model_call_business_subscene_started_at
 ON modelhub.model_call (business_line, business_scene, business_subscene, started_at);

-- 异步任务记录提交者的固定业务参数；旧任务不从历史 headers 猜测。
ALTER TABLE modelhub.generation_task ADD COLUMN IF NOT EXISTS business_metadata jsonb;
