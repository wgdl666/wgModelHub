-- 业务归属与技术调用方独立；先建字段，再按审计证据清洗历史。
ALTER TABLE modelhub.model_call ADD COLUMN IF NOT EXISTS business_line text NOT NULL DEFAULT 'unknown';
CREATE INDEX IF NOT EXISTS model_call_business_line_scene_started_at ON modelhub.model_call (business_line, business_scene, started_at);
