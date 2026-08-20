# wgModelHub 仓库边界

- `wgModelHub` 是 WG 内部统一的大模型协议适配层，只负责真实供应商模型 ID（`request.model`）路由、供应商凭据托管、协议转换与错误归一化。
- Prompt 编排、业务重试、质量验收、任务状态、OSS/DB/MQ 始终留在 `wgHub` 或 `wgWardrobe`；ModelHub 成功只表示供应商调用完成，不表示业务任务终态。
- 单个 Deployment、单个 gRPC `ModelHubService.Generate`、端口 `50053`；不提供任意路径代理、万能 RPC 或调用方注入供应商地址/密钥。

# 配置与模型路由

- Nacos Data ID：`wg.mirror.modelHub`；bootstrap 只保存 Nacos 定位信息，凭据与模型映射在 YAML 正文中。
- 每个 provider 实例用互斥嵌套字段表达 Gemini/VertexAI/Ark/OpenAI/LTX，并声明 `models: [真实模型 ID...]`。
- OpenAI 实例同时承接 chat/completions 与 Images API；`gpt-image-2` 必须单独绑到 OpenAI 实例走 `/v1/images/generations`，不能并入 OminiLink Gemini generateContent 生图实例。
- 启动时建立「真实模型 ID → 唯一 provider 实例」路由；空模型或重复模型 ID 直接配置错误。无 profile、alias、fallback 或双路径。
- 全部真实模型 ID 是 `models` 包常量（`github.com/wgdl666/wgModelHub/models`）；Nacos / example YAML / 调用方 `request.model` 必须引用这些常量，不能另起业务名。

# 协议不变量

- `GenerateRequest` 顶层恰好 `model` / `input` / `output`；system/任务/历史/媒体/tool 一律按序进 `Input.items`，不得另设顶层 prompt。
- capability 由 `OutputSpec` oneof 决定；`stream` 位于 `OutputSpec`。
- `Media` 使用 `mime_type` + `oneof bytes/uri`；ModelHub 不通过额外 HEAD 猜测类型。
- 图片输出用有序 `OutputItem` 展开图片与诊断文字，不能只返回首张图；完全空响应为 `INVALID_RESPONSE`，仅诊断文本或 blocked 仍合法。
- 文本必须保留 `ThoughtSignature`、`response_id`、`previous_response_id`、工具调用历史、usage、finish reason、JSON Schema、thinking 与流式取消。
- 文本 stream：非 final 为增量，唯一 final 只带终态元数据；non-stream：仅一个 final，含完整文本/工具调用与元数据。
- `optional` 标量（含 `temperature=0`）已设置时必须原样下发供应商。
- LTX 在 ModelHub 内同步提交/轮询/下载，再按 1MiB 视频 `OutputItem` 分块返回；最大 200MiB，0 字节下载与超限必须失败；不持久化 job。

# 错误与观测

- 供应商错误统一映射为 gRPC status + `google.rpc.ErrorInfo.reason`（domain=`wg.modelhub`）。
- OpenTelemetry/Logfire 只记录主机、状态码、字节数等元数据，禁止记录 Prompt、媒体正文或密钥。

# 发布

- 正式发布走 `workspace` 统一入口，不绕过它手改运行环境。
- ACK 清单见 `deploy/ack.yaml`：两副本、RollingUpdate、`terminationGracePeriodSeconds=180`、非 root `10001`。
