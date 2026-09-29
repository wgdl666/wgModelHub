-- 仅处理已审计的 CN/dev 2026-09-29 镜子记录；由运维显式执行，禁止启动时或其他区域自动运行。
-- 只补空标签，不改已标注数据、调用次数、状态、耗时或原始请求快照。
-- 旧视觉/生图/向量记录缺少可靠阶段证据时保留能力级名称，不能凭模型猜测具体步骤。
WITH candidates AS (
 SELECT call_id, business_scene, business_subscene AS old_subscene,
 CASE
 WHEN caller_service='wg-hub' AND business_scene='agent_chat' THEN 'intent_recognition'
 WHEN caller_service='wg-wardrobe' AND business_scene IN ('closet_search','recommend') AND model='qwen3-vl-embedding' THEN 'recall'
 WHEN caller_service='wg-wardrobe' AND business_scene IN ('closet_search','recommend') AND model='qwen3.7-text-rerank' THEN 'ranking'
 WHEN caller_service='wg-wardrobe' AND business_scene IN ('photo','recommend') AND model='human-yolo' THEN 'person_detection'
 WHEN caller_service='wg-wardrobe' AND business_scene IN ('photo','recommend') AND model='human-parser' THEN 'human_parsing'
 WHEN caller_service='wg-wardrobe' AND business_scene IN ('photo','recommend') AND model='segment-person-bria' THEN 'person_extraction'
 WHEN caller_service='wg-wardrobe' AND business_scene='photo' AND model='segment-subject-bria' THEN 'subject_extraction'
 WHEN caller_service='wg-wardrobe' AND business_scene='photo' AND model='gemini-3.8-flash' THEN 'visual_analysis'
 WHEN caller_service='wg-wardrobe' AND business_scene='photo' AND model IN ('gpt-image-2','gpt-image-2.5-flare') THEN 'image_generation'
 WHEN caller_service='wg-wardrobe' AND business_scene='photo' AND model='qwen3-vl-embedding' THEN 'cloth_embedding'
 WHEN caller_service='wg-wardrobe' AND business_scene='recommend' AND model='facebody-compare-face' THEN 'face_comparison'
 WHEN caller_service='wg-wardrobe' AND business_scene='recommend' AND model IN ('gemini-2.5-flash-image','gemini-3.1-flash-image','gpt-image-2') THEN 'tryon_generation'
 WHEN caller_service='wg-wardrobe' AND business_scene='recommend' AND model='ltx' THEN 'video_generation'
 WHEN caller_service='wg-wardrobe' AND business_scene='recommend' AND model='gemini-3.7-flash' THEN 'recommendation_analysis'
 WHEN caller_service='wg-wardrobe' AND business_scene='recommend' AND model='qwen3-vl-plus' THEN 'tryon_quality_check'
 END AS new_subscene
 FROM modelhub.model_call
 WHERE business_line='mirror' AND business_subscene IN ('','unknown')
 AND started_at >= '2026-09-29 00:00:00+08' AND started_at < '2026-09-30 00:00:00+08'
 AND status IN ('succeeded','failed','cancelled')
), changed AS (
 UPDATE modelhub.model_call m SET business_subscene=c.new_subscene, updated_at=now()
 FROM candidates c WHERE m.call_id=c.call_id AND c.new_subscene IS NOT NULL
 AND m.business_subscene=c.old_subscene
 RETURNING m.call_id, c.old_subscene, c.new_subscene, m.business_scene, m.model
)
SELECT * FROM changed ORDER BY business_scene,new_subscene,call_id;
