# wgModelHub 仓库边界

- `wgModelHub` 是 WG 内部统一的大模型协议适配层，只负责真实供应商模型 ID（`request.model`）路由、供应商凭据托管、协议转换与错误归一化。
- Prompt 编排、业务重试、质量验收、业务任务终态、调用方 OSS 始终留在 `wgHub` 或 `wgWardrobe`；ModelHub 成功只表示供应商结果可读，不表示业务任务终态。
- 单个 Deployment、单个 gRPC `ModelHubService`、端口 `50053`；不提供任意路径代理、万能 RPC 或调用方注入供应商地址/密钥。

# 配置与模型路由

- Nacos Data ID：`wg.mirror.modelHub`；bootstrap 只保存 Nacos 定位信息，凭据与模型映射在 YAML 正文中。共享业务 YAML 忽略未知字段，但仍须拒绝多文档和缺少的必需配置；启动与热更新共用 `ParseAndValidateYAML`。
- 每个 provider 实例用互斥嵌套字段表达 Gemini/VertexAI/Ark/OpenAI/Photoroom/SegmentPerson/LTX 以及各类视频供应商，并声明 `models: [真实模型 ID...]`。
- `ark_video` 承接方舟 Seedance 2.5（`doubao-seedance-2-5-260628`）文生视频与首帧图生视频；示例实例键是 `火山方舟_ark_video`，与文本实例 `火山方舟_ark_chat` 分绑，不能共用一套能力。任务形态由 Input 推断，不另开模型 ID 或模式枚举。
- OpenAI 实例同时承接 chat/completions 与 Images API；无参考图走 `/v1/images/generations`，有参考图走 `/v1/images/edits`。`gpt-image-2` / `gpt-image-2.5-flare` / `gpt-image-2.5-sunburst` 复用 AIG OpenAI-compatible Images。示例与审计目标实例键都是 `AIG_openai`（`https://api.aig-ai.com/v1`），不能并入 Gemini generateContent 生图实例。
- `gemini-nano-banana-2.1` 是 Vertex 上的 Nano Banana 2.1，走现有 Generate 图片链路，不并入 Gemini Developer API 生图实例。Vertex 生图固定 `projects/{project}/locations/global`，`vertexai.project` 必须显式给出；缺 project 时生图返回配置错误，不出站。Vertex 文本/流式/缓存/生图只用 ADC 和同一个 global 客户端，project 在启动校验时必填；可与 `google_rerank` 读取同一运行时身份文件。Vertex 配置不再有 api_key，迁移时须先提供运行时凭据及权限、更新配置，再滚动重启。`vertexai.proxy_url` 留空直连，配置后换 token 与模型请求共用同一代理。token/私钥禁止写进 YAML 或日志。思考推荐 MINIMAL/MEDIUM/HIGH，LOW 原样下发并由上游决定是否接受；temperature 已设置时仍原样下发。
- `photoroom` 承接官方 Remove Background（`POST /v1/segment`）；`photoroom-segment` 仅作路由常量、不下发上游。恰好一图入、透明 PNG 出；裁剪/填充/存储/业务重试留在调用方。
- `segment_person` 承接国内自建抠图。`segment-person-bria` 走 `POST /api/segment`，`segment-subject-bria` 走 `POST /api/segment_subject`；`method` 与 Basic Auth 只在供应商配置，模型 ID 不下发上游。两个 ID 不能互相替代。恰好一图入、透明 PNG 出；`max_side` 裁剪、OSS 与业务重试留在衣橱。
- `human_yolo` 承接自建人体检测 `POST /predict`，`human-parser` 承接自建人体解析。`rekognition_detect` 承接 DetectLabels 的 Person 框，模型 ID 是 `rekognition-detect-labels`。三者都返回文本 JSON；裁图和单件提取留在衣橱。
- `facebody_compare` 承接阿里云 `CompareFace`，模型 ID 是 `facebody-compare-face`。`rekognition_compare` 承接 `CompareFaces`，模型 ID 是 `rekognition-compare-faces`。两者都是恰好两张内联图入、文本 JSON `{"similarity":0到1}` 出。
- `dashscope_embedding` / `cohere_embedding` / `bedrock_embedding` 承接 1024 维向量，模型 ID 分别是 `qwen3-vl-embedding`、`embed-v4.0`、`us.cohere.embed-v4:0`。一图或一段文本入，`{"embedding":[...]}` 出。仅官方 Cohere `embed-v4.0` 在 `input.model_traits` 显式设置 `embedding_batch=true,input_type=search_query` 时接受受资源预算约束的单文本 USER message；由 ModelHub 按 `cohere_embedding.max_batch_size`（≤96）顺序拆批，不设共享并发池或分片重试；调用并发由调用方管理，已有Service重试可能重跑整单，按输入顺序返回 `{"embeddings":[[...],...]}`；普通多段消息不能自动变成批量。换模型会换向量空间，调用方必须先重嵌再切读。
- 精排新协议通过 Generate 用户文本 JSON 传 `query` / `documents` / `instruction_key`，ModelHub 从顶层 `rerank_templates` 按键读取共享模板，不按英文句子猜用途。`dashscope_rerank` 的 Qwen 文本/VL 保留原 Query 并发原生 instruct；Google、Mixedbread、Cohere 官网与 Bedrock Cohere 无独立指令字段，统一渲染 query_template。模板可热更新；分数门槛、竞速与质量验收仍归调用方。无 instruction_key 的线上旧客户端暂留既有行为（Cohere/Google 丢弃 instruct，Mixedbread 主色包装），迁移后另行移除。
- `google_rerank` 承接 Google 标准版/快速版文本精排，开放的真实模型 ID 由配置 `models` 控制，`request.model` 原样下发；已注册 default/fast 的 005 与官方 @latest ID。固定 Discovery Engine `locations/global` Ranking API；沿用文本 Generate 的 query/documents 与 index/relevance_score。instruction_key 对应模板渲染为 query，不向下游发 instruct。身份通过运行环境 ADC 提供（服务账号或联合身份文件），自动缓存刷新 OAuth token；身份配置是运行时 Secret，可与 Vertex 共用，不把 token/私钥写进 Nacos。`project` 与可选 `proxy_url` 在 provider YAML；token 交换和模型请求共用代理。
- `facebody_detect` / `rekognition_faces` 只回人脸框 `{"faces":[{"left","top","width","height"}]}`。扩框裁锚点和主体脸选择留在 UserCenter。`facebody_library` / `rekognition_library` 承接查重、录脸、删脸；库名在供应商配置，不在请求里。
- `FLUX.2-klein-9B` 必须单独绑到 OpenAI Images 实例，不能与 `gpt-image-2` / 2.5 共用。当前 SeeTacloud 部署只开放 i2i edits：无参考图在发 HTTP 前拒绝；edits 必须带 `response_format=b64_json`，因为 worker 默认回 `127.0.0.1` 下载地址。GPT Image（含 2 / 2.5）当前接入沿标准请求且默认返回可解析结果，ModelHub 不下发该字段；`response_format=b64_json` 仅是 FLUX.2 私有兼容特例。
- 智谱实例键是 `智谱`，承接 `glm-5.3-flash`（`https://open.bigmodel.cn/api/paas/v4`）。思考字段走 `thinking.type=enabled`，禁止下发 `disabled` 或 DashScope 的 `enable_thinking`；统一协议 `DISABLED` 只映射为 `reasoning_effort=low`。
- 美东路演 `deepseek-v4.1-flash` 挂在已有实例 `阿里云_MaaS_ws-qnsgk3r40cvpyn30`，地址是该工作空间的 `us-east-1.maas.aliyuncs.com`。对外 ID 与上游一致，不改写 `upstream_model`，也不另建 Fireworks 实例。只发 `reasoning_effort`：关为 `none`，HIGH 为 `max`。调用方的 JSON Schema 对这条改发 `json_object`，因为该工作空间目前拒绝 `json_schema`。这条只加在路演该实例的 `models` 上，不能并进国内方舟。该模型在弗吉尼亚的部署范围是 Global。
- 启动时建立「真实模型 ID → provider 实例」路由：单 provider 声明可隐式选定；多 provider 声明同一模型时必须在顶层 `model_routes` 显式选定其一，不能依赖 map 顺序。无 profile、alias 或自动 failover。同名同资源 provider 仅 `models` 列表和/或顶层 `model_routes` 变化可通过 Nacos ListenConfig 或海外 AppConfig Agent 轮询热更新原子切换；provider 实例增删、类型、凭据、BaseURL/Endpoint 等连接资源参数变化仍须滚动重启，不得部分应用。
- 全部真实模型 ID 是 `models` 包常量（`github.com/wgdl666/wgModelHub/models`）；Nacos / example YAML / 调用方 `request.model` 必须引用这些常量，不能另起业务名。穿搭视频对外 ID 只认 `VWorld_outfit_video_1.0`，经 `ltx` provider 路由，示例与审计目标实例键是 `wgdl_video`；海外旧模型 ID `ltx` 原样保留。VWorld 视频上游使用 `/v2/videos/vton`，只传首帧、prompt、可选 motion_tips 与 trace_id，模型和推理规格由服务端固定；旧 `ltx` 路由仍使用 `/vton` 兼容协议。`VWorld_bodymodel_1.0` 与拆衣服、换衣同属 `vworld_image`，示例与审计目标实例键是 `wgdl_image`，但只打 `POST /v2/images/body_model`：恰好一张人物图，提示词和 `1024x1536` 由上游固定，禁止下发本 ID、`FLUX.2-klein-9B` 或调用方 prompt。性别不进文本 part，放在 `Input.model_traits` 的 JSON 键 `gender`，原样写入上游表单，由 VMind-Image 校验。VWorld 生图使用 `/v2/images` 最小业务接口；不下发 LoRA、尺寸、seed、n、response_format、negative_prompt 等推理配置。录衣商品图走 `/product_generation`，带解析载荷的人物提取走 `/garment_extraction`，VTON 走 `/vton`；Wardrobe 提供 garment_role，网关不再映射 inventory_slot，也不验收 crop.applied。人物/商品抠图实例键是 `wgdl_segment_person`。这三类展示渠道都是薇光点亮。G7 `human_parse` 按目标 host 单独成渠道，不归入薇光点亮。
- 渠道由实际模型 API 目标 hostname 在 ModelHub 内派生，经内部 `/metrics` 的 `wg_modelhub_channel_info` 暴露（instance、channel、host、protocol）。出网 HTTP proxy 不另算渠道。不进公开 RPC，也不另建渠道表。实例键用下划线分词。整批改名必须滚动重启后由新进程读取；连接资源相同的实例无法靠现有热更新一一配对，热更新会整单拒绝并保留旧配置。
- `database.dsn` 服务视频长任务的跨 Pod 持久化；当启用公网 listener 时，同一 Ent client 还服务 API Key 鉴权。所有 ModelHub 关系表都显式限定在 `modelhub` schema；启动不自动 DDL，须显式执行 `migrations/`。

# 协议不变量

- `GenerateRequest` 顶层固定 `model` / `input` / `output` / `business_metadata`；system/任务/历史/媒体/tool 一律按序进 `Input.items`，不得另设顶层 prompt。
- capability 由 `OutputSpec` oneof 决定；`stream` 位于 `OutputSpec`。
- `Media` 使用 `mime_type` + `oneof bytes/uri`；ModelHub 不通过额外 HEAD 猜测类型。
- 图片输出用有序 `OutputItem` 展开图片与诊断文字，不能只返回首张图；完全空响应为 `INVALID_RESPONSE`，仅诊断文本或 blocked 仍合法。
- 文本必须保留 `ThoughtSignature`、`response_id`、`previous_response_id`、工具调用历史、usage、finish reason、JSON Schema、thinking 与流式取消。
- 文本 stream：非 final 为增量，唯一 final 只带终态元数据；non-stream：仅一个 final，含完整文本/工具调用与元数据。
- `optional` 标量（含 `temperature=0`）已设置时必须原样下发供应商。
- 文字与图片继续走 `Generate`。视频长任务走 `SubmitGeneration` / `GetGeneration`；`Generate(video)` 已移除，调用方必须用 Submit/Get。
- 同步 TTS 走独立 unary `SynthesizeSpeech`，不进入 `OutputSpec`；成功只表示完整音频已收集，不表示 Mirror 播放。
- `SubmitGeneration` 第一阶段只接受 `output.video`；幂等键为 `(x-wg-caller-service, request_id)`，caller 缺失不拒绝，只用于归因与命名空间，不做鉴权。
- `GetGeneration` 对外只表达 ModelHub 任务状态（UNSPECIFIED/PENDING/RUNNING/SUCCEEDED/FAILED）；PENDING/RUNNING/FAILED 只回一个 status 事件，SUCCEEDED 先 status 再流式 `GenerateEvent`。不暴露 provider/supplier 字段，不做 Cancel/List/Watch/Delete/Webhook/进度百分比/后台轮询 worker。
- 视频结果从上游响应流按 1MiB `GenerateEvent` 分块，并增量执行 0 字节 / 200MiB 上限检查；禁止先整文件 `ReadAll` 再分块。
- `VideoOutput` 除 `resolution` 外支持 `duration_seconds` 与 `aspect_ratio`；`GenerateEvent.generation_elapsed_ms` 保留 Gemini 等供应商生成耗时。

# 视频长任务持久化（技术表，非业务任务）

- PostgreSQL 表 `modelhub.generation_task` 只存跨 Pod 查询所需最小事实：`task_id`、`caller`、`request_id`、`request_hash`、`model`、`provider`、`provider_task_id`、`state`、归一化错误、时间戳；不存 prompt、媒体正文或最终视频。
- 唯一键 `(caller, request_id)`：同哈希返回原任务，不同哈希返回 `ALREADY_EXISTS`。先落库再调上游；上游可能已接受但本地未拿到确定结果时不得自动重提。
- 运行时经 Ent builder/predicate 访问；业务代码禁止手写 SQL。历史 `migrations/001_generation_task.sql` 保留且须显式执行；禁止 `Schema.Create` 或启动自动 DDL。`go generate ./ent` 只再生 Ent 代码。
- 前台 `Generate`（含文字/图片）不经过任务表。

# 错误与观测

- 供应商错误统一映射为 gRPC status + `google.rpc.ErrorInfo.reason`（domain=`wg.modelhub`）。
- OpenTelemetry/Logfire 只记录主机、状态码、字节数等元数据，禁止记录 Prompt、媒体正文或密钥。

# 发布

- 正式发布走 `workspace` 统一入口，不绕过它手改运行环境。
- ACK 清单见 `deploy/ack.yaml`：两副本、RollingUpdate、`terminationGracePeriodSeconds=180`、非 root `10001`。
