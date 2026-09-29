-- CN/dev 专用：按已核验的调用端、注册场景和请求模板回填，不从模型名猜测业务线。
-- 仅改已终态记录的标签，原始请求、状态、次数和耗时保持不变；RETURNING 保存旧值便于审计。
WITH source AS (
 SELECT *, jsonb_path_query_array(input_payload, '$.input.items[*].message.parts[*].text')::text AS prompt
 FROM modelhub.model_call
 WHERE started_at >= '2026-09-29 00:00:00+08' AND started_at < '2026-09-30 00:00:00+08'
 AND status IN ('succeeded','failed','cancelled')
 AND (business_line IN ('','unknown') OR (business_line='mirror' AND business_scene='register' AND business_subscene IN ('','unknown')))
), classified AS (
 SELECT call_id, business_line AS old_line, business_scene AS old_scene, business_subscene AS old_subscene,
 CASE
 WHEN business_line='mirror' AND business_scene='register' THEN 'mirror'
 -- 唯一已核验的注册取消卸库调用，源码 cleanupIncompleteUser 丢失了上下文。
 WHEN call_id='bb013c0d-b6af-4b27-8a1c-43c1ede588de' AND caller_service='wg-user-center' AND model='facebody-face-library' THEN 'mirror'
 WHEN caller_service='wg-ops-platform' AND (prompt LIKE '%验收模型%' OR prompt LIKE '%语义验收器%' OR prompt LIKE '%你是测试会话里的当前用户%') THEN 'ops'
 WHEN caller_service='wg-user-memory' AND prompt LIKE '%你是 Mirror 的长期记忆决策模型%' THEN 'mirror'
 WHEN caller_service='wg-hub' AND (prompt LIKE '%薇光星球智能全身镜%' OR prompt LIKE '%你只写下一轮意图识别要用的会话备忘%') THEN 'mirror'
 END AS new_line,
 CASE
 WHEN business_line='mirror' AND business_scene='register' THEN 'register'
 WHEN call_id='bb013c0d-b6af-4b27-8a1c-43c1ede588de' THEN 'register'
 WHEN caller_service='wg-ops-platform' THEN 'platform_test'
 WHEN caller_service='wg-user-memory' THEN 'memory'
 WHEN caller_service='wg-hub' THEN 'agent_chat'
 END AS new_scene,
 CASE
 WHEN caller_service='wg-ops-platform' AND (prompt LIKE '%验收模型%' OR prompt LIKE '%语义验收器%') THEN 'semantic_evaluation'
 WHEN caller_service='wg-ops-platform' AND prompt LIKE '%你是测试会话里的当前用户%' THEN 'simulated_user'
 WHEN caller_service='wg-user-memory' AND prompt LIKE '%你是 Mirror 的长期记忆决策模型%' THEN 'memory_extraction'
 WHEN caller_service='wg-hub' THEN 'intent_recognition'
 WHEN call_id='bb013c0d-b6af-4b27-8a1c-43c1ede588de' THEN 'face_removal'
 WHEN business_scene='register' THEN CASE
   WHEN model='facebody-compare-face' THEN 'face_comparison'
   WHEN model='facebody-detect-face' THEN 'face_detection'
   WHEN model='human-yolo' THEN 'person_detection'
   WHEN model='human-parser' THEN 'human_parsing'
   WHEN model='segment-person-bria' THEN 'person_extraction'
   WHEN model='segment-subject-bria' THEN 'subject_extraction'
   WHEN prompt LIKE '%Tomirro luxury avatar portrait%' THEN 'avatar_generation'
   WHEN prompt LIKE '%Identity-Locked Full-Body Base Outfit Conversion%' THEN 'body_model_generation'
   WHEN prompt LIKE '%Visual gender recognition%' THEN 'visual_gender_recognition'
   WHEN prompt LIKE '%用户风格画像生成器%' THEN 'profile_generation'
   WHEN prompt LIKE '%你在给用户起一个%' THEN 'nickname_generation'
   WHEN prompt LIKE '%Body model 明显废图校验%' THEN 'body_model_quality_check'
   WHEN prompt LIKE '%minimalist fashion campaign poster%' OR prompt LIKE '%时尚杂志 VLIGHT%' THEN 'artwork_generation'
   WHEN prompt LIKE '%把参考图中的衣物重建为同一件真实商品%' THEN 'cloth_image_generation'
   WHEN prompt LIKE '%穿搭分析%' THEN 'outfit_describe'
   WHEN prompt LIKE '%只列出这张全身图里真正穿着%' THEN 'worn_garment_inventory'
   WHEN prompt LIKE '%识别图片中的唯一服饰单品%' THEN 'cloth_describe'
   WHEN prompt LIKE '%识别图片中这件单品的品牌%' THEN 'cloth_brand_describe'
   WHEN prompt LIKE '%识别透明背景单品图中已确认的那件衣物%' THEN 'clothing_attributes'
   -- 注册录衣同时包含去重与建索引，旧图片向量缺少阶段标识，保留真实能力粒度。
   WHEN model='qwen3-vl-embedding' THEN 'cloth_embedding'
 END END AS new_subscene
 FROM source
), changed AS (
 UPDATE modelhub.model_call m SET business_line=c.new_line, business_scene=c.new_scene,
 business_subscene=c.new_subscene, updated_at=now()
 FROM classified c WHERE m.call_id=c.call_id
 AND c.new_line IS NOT NULL AND c.new_scene IS NOT NULL AND c.new_subscene IS NOT NULL
 AND m.business_line=c.old_line AND m.business_scene=c.old_scene AND m.business_subscene=c.old_subscene
 RETURNING m.call_id,c.old_line,c.old_scene,c.old_subscene,c.new_line,c.new_scene,c.new_subscene,m.model
)
SELECT * FROM changed ORDER BY new_line,new_scene,new_subscene,call_id;
