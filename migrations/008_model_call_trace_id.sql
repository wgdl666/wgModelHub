-- 先加列再发服务。没有 span 的历史行留空，不回填。
ALTER TABLE modelhub.model_call ADD COLUMN IF NOT EXISTS trace_id text;
CREATE INDEX IF NOT EXISTS model_call_trace_id_idx ON modelhub.model_call (trace_id);
