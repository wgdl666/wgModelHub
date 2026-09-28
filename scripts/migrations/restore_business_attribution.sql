-- 只还原仍等于本批清洗结果的行，避免覆盖后续真实请求或人工修正。
BEGIN;
UPDATE modelhub.model_call c SET business_line=b.old_business_line, business_scene=b.old_business_scene
FROM modelhub.model_call_attribution_backup_20260928 b
WHERE c.call_id=b.call_id AND c.business_line=b.new_business_line AND c.business_scene=b.new_business_scene;
COMMIT;
