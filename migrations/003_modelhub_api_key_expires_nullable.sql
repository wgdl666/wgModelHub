-- 允许 expires_at 为 NULL，语义为永不过期（非哨兵时间）。
-- 已有行的非空到期时间保持不变，不做回填。
-- 管理面归属 wgOpsPlatform；DDL 仍仅由部署 migration 身份执行。
ALTER TABLE modelhub_api_key
    ALTER COLUMN expires_at DROP NOT NULL;
