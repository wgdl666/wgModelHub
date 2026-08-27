-- ModelHub 公网 gRPC API Key；明文 secret 仅创建/轮换时一次性返回，库内只存 SHA-256。
-- DDL 仅由部署 migration 身份执行；wgDevPlatform 受限账号仅 SELECT / INSERT / UPDATE(revoked_at)。
CREATE TABLE IF NOT EXISTS modelhub_api_key (
    id text PRIMARY KEY,
    principal_id text NOT NULL,
    key_id text NOT NULL UNIQUE,
    secret_sha256 text NOT NULL,
    name text NOT NULL DEFAULT '',
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz NULL
);

CREATE INDEX IF NOT EXISTS modelhub_api_key_created_by_idx ON modelhub_api_key (created_by);
CREATE INDEX IF NOT EXISTS modelhub_api_key_principal_id_idx ON modelhub_api_key (principal_id);
