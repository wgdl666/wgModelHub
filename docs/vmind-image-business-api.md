# VMind 图片模型调用

适配器统一调用 `POST /v1/gateway/requests/{model_key}`，使用网关 Bearer 凭据和 `application/json`。`model_key` 自带版本。请求体的 `input` 包含业务字段和有序 `images` 数组，每张图使用 `mime_type` 与 base64 `data`。追踪信息单独放入 `metadata`。调用方不传业务能力、后端路径或额外版本。模型标识和 Generate RPC 的消息结构保持不变。

| 模型标识 | 业务输入 | JSON 业务字段 | images 顺序 |
|---|---|---|---|
| Vmind_wardrobe_v1.0 | 商品图 | garment_name、可选 garment_role | 原图 |
| Vmind_wardrobe_v1.0 | 解析载荷 | garment_name、可选 garment_role 和 parse_labels | 原图、可选解析掩码 |
| Vmind_wardrobe_v1.0 | 人物图与框 | garment_name、bbox、可选 bbox_format 和 garment_role | 人物原图 |
| Vmind_outfit_v1.0 | 人物图与衣物拼图 | prompt | 人物图、衣物拼图 |
| Vmind_bodymodel_v1.0 | 人物图与性别 | gender | 人物图 |

衣橱模型由网关按业务输入判断操作。输入带 `bbox`、`parse_labels` 或第二张解析掩码时使用拆衣能力，其余使用商品图能力。两种操作共享一个带版本的模型标识。模型服务的文件字段名只保存在网关 `image_fields` 中。

适配器在有效追踪上下文中添加 `metadata.trace_id`。衣物元数据沿用文本片段中的 JSON。`bbox` 使用 JSON 数组 `[x1,y1,x2,y2]`，`parse_labels` 使用 JSON 对象。空 `garment_role` 不发送。性别从 `input.model_traits` 读取。换衣保留提示词内容与人物、衣物图片顺序。响应仍将 `b64_json` 解码为图片字节。

适配器不发送 `model`、LoRA、尺寸、seed、n、`response_format`、`use_crop`、`input_mode` 或 `negative_prompt` 等推理控制字段，不推断衣物角色，也不验收 `crop.applied`。模型服务负责业务字段与推理约束。

配置使用 `vworld_image.base_url` 和 `vworld_image.token`。后端路径、图片字段映射和模型服务凭据由网关配置持有。每次提交带固定的 `Idempotency-Key`；一次提交在 HTTP 传输层重放时沿用该身份。地址可热更新，凭据变更需要重启。旧 multipart 和旧模型 ID 映射已删除。

图片 HTTP 调用上限为 120 秒。网关从入队开始共享 120 秒预算，排队耗时会扣除。视频提交与限流重试也共用 120 秒；视频受理后的查询和成片下载继续受各自调用方上下文管理。

图片在提交连接中同步返回结果。视频持久入队即返回 HTTP 202，响应只确认原 `idempotency_key`。ModelHub 保存并复用这个幂等 ID，后续请求 `GET /v1/gateway/requests`，只带 Bearer 凭据和 `Idempotency-Key`，不传模型标识或另一个任务 ID。网关状态只有 queued、processing、succeeded 和 failed；成功时读取 `result.video_url` 并流式下载，不再读取旧 `/jobs/{id}` 或兼容旧状态。

发布需要分别更新网关路由、PG 任务表和 ModelHub 配置。原有图片与视频生成透传已移除，不保留旧入口回退。任务查询和结果下载继续通过网关读取原实例。
