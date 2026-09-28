-- 跨 Pod 续查恢复调用上下文；显式迁移，不在服务启动时修改结构。
ALTER TABLE modelhub.generation_task ADD COLUMN IF NOT EXISTS metadata JSONB;
