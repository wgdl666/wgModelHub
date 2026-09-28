-- 中国开发环境一次性历史归属清洗：先查询审计表核对，再执行事务。
-- 旧记录缺失场景时无法可靠区分用户功能，只补镜子专属服务的业务线；muse 和 Ops 测试不猜归属。
-- 该脚本不在服务启动或常规迁移中执行，必须在指定环境单独运行。
BEGIN;
CREATE TABLE IF NOT EXISTS modelhub.model_call_attribution_backup_20260928 (
 call_id text PRIMARY KEY,
 old_business_line text NOT NULL,
 old_business_scene text NOT NULL,
 new_business_line text NOT NULL,
 new_business_scene text NOT NULL,
 backed_up_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO modelhub.model_call_attribution_backup_20260928
 (call_id,old_business_line,old_business_scene,new_business_line,new_business_scene)
SELECT call_id,business_line,business_scene,'mirror',business_scene
FROM modelhub.model_call
WHERE business_line='unknown'
 AND started_at < TIMESTAMPTZ '2026-09-28 20:00:00+08'
 AND caller_service IN ('wg-hub','wg-wardrobe','wg-user-center','wg-user-memory')
ON CONFLICT (call_id) DO NOTHING;
UPDATE modelhub.model_call c SET business_line=b.new_business_line, business_scene=b.new_business_scene
FROM modelhub.model_call_attribution_backup_20260928 b
WHERE c.call_id=b.call_id AND c.business_line=b.old_business_line AND c.business_scene=b.old_business_scene;
COMMIT;
