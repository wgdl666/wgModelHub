# wgModelHub 仓库边界

- `wgModelHub` 是 WG 内部统一的大模型协议适配层，只负责真实供应商模型 ID（`request.model`）路由、供应商凭据托管、协议转换与错误归一化。
- Prompt 编排、业务重试、质量验收、业务任务终态、调用方 OSS 始终留在 `wgHub` 或 `wgWardrobe`；ModelHub 成功只表示供应商结果可读，不表示业务任务终态。
- 单个 Deployment、单个 gRPC `ModelHubService`、端口 `50053`；不提供任意路径代理、万能 RPC 或调用方注入供应商地址/密钥。

# 配置与模型路由

- Nacos Data ID：`wg.mirror.modelHub`；bootstrap 只保存 Nacos 定位信息，凭据与模型映射在 YAML 正文中。
- 每个 provider 实例用互斥嵌套字段表达 Gemini/VertexAI/Ark/OpenAI/LTX，并声明 `models: [真实模型 ID...]`。
- OpenAI 实例同时承接 chat/completions 与 Images API；`gpt-image-2` 必须单独绑到 OpenAI 实例（无参考图走 `/v1/images/generations`，有参考图走 `/v1/images/edits`），不能并入 OminiLink Gemini generateContent 生图实例。
- 启动时建立「真实模型 ID → 唯一 provider 实例」路由；空模型或重复模型 ID 直接配置错误。无 profile、alias、fallback 或双路径。
- 全部真实模型 ID 是 `models` 包常量（`github.com/wgdl666/wgModelHub/models`）；Nacos / example YAML / 调用方 `request.model` 必须引用这些常量，不能另起业务名。
- `database.dsn` 仅服务视频长任务跨 Pod 查询；启动不自动 DDL，须显式执行 `migrations/`。

# 协议不变量

- `GenerateRequest` 顶层恰好 `model` / `input` / `output`；system/任务/历史/媒体/tool 一律按序进 `Input.items`，不得另设顶层 prompt。
- capability 由 `OutputSpec` oneof 决定；`stream` 位于 `OutputSpec`。
- `Media` 使用 `mime_type` + `oneof bytes/uri`；ModelHub 不通过额外 HEAD 猜测类型。
- 图片输出用有序 `OutputItem` 展开图片与诊断文字，不能只返回首张图；完全空响应为 `INVALID_RESPONSE`，仅诊断文本或 blocked 仍合法。
- 文本必须保留 `ThoughtSignature`、`response_id`、`previous_response_id`、工具调用历史、usage、finish reason、JSON Schema、thinking 与流式取消。
- 文本 stream：非 final 为增量，唯一 final 只带终态元数据；non-stream：仅一个 final，含完整文本/工具调用与元数据。
- `optional` 标量（含 `temperature=0`）已设置时必须原样下发供应商。
- 文字与图片继续走 `Generate`。视频长任务走 `SubmitGeneration` / `GetGeneration`；迁移期旧 `Generate(video)` 仍可用，并复用同一套 provider Submit/Get/ReadResult，禁止第二套供应商协议。
- `SubmitGeneration` 第一阶段只接受 `output.video`；幂等键为 `(x-wg-caller-service, request_id)`，caller 缺失不拒绝，只用于归因与命名空间，不做鉴权。
- `GetGeneration` 对外只表达 ModelHub 任务状态（UNSPECIFIED/PENDING/RUNNING/SUCCEEDED/FAILED）；PENDING/RUNNING/FAILED 只回一个 status 事件，SUCCEEDED 先 status 再流式 `GenerateEvent`。不暴露 provider/supplier 字段，不做 Cancel/List/Watch/Delete/Webhook/进度百分比/后台轮询 worker。
- 视频结果从上游响应流按 1MiB `GenerateEvent` 分块，并增量执行 0 字节 / 200MiB 上限检查；禁止先整文件 `ReadAll` 再分块。
- `VideoOutput` 除 `resolution` 外支持 `duration_seconds` 与 `aspect_ratio`；`GenerateEvent.generation_elapsed_ms` 保留 Gemini 等供应商生成耗时。

# 视频长任务持久化（技术表，非业务任务）

- PostgreSQL 表 `generation_task` 只存跨 Pod 查询所需最小事实：`task_id`、`caller`、`request_id`、`request_hash`、`model`、`provider`、`provider_task_id`、`state`、归一化错误、时间戳；不存 prompt、媒体正文或最终视频。
- 唯一键 `(caller, request_id)`：同哈希返回原任务，不同哈希返回 `ALREADY_EXISTS`。先落库再调上游；上游可能已接受但本地未拿到确定结果时不得自动重提。
- 前台 `Generate`（含文字/图片）不经过任务表。

# 错误与观测

- 供应商错误统一映射为 gRPC status + `google.rpc.ErrorInfo.reason`（domain=`wg.modelhub`）。
- OpenTelemetry/Logfire 只记录主机、状态码、字节数等元数据，禁止记录 Prompt、媒体正文或密钥。

# 发布

- 正式发布走 `workspace` 统一入口，不绕过它手改运行环境。
- ACK 清单见 `deploy/ack.yaml`：两副本、RollingUpdate、`terminationGracePeriodSeconds=180`、非 root `10001`。
