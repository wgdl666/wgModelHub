# wgModelHub

`wgModelHub` 是 WG 服务内部统一的大模型协议适配层。它只负责：

- 依据 `model_profile` 选择一个确定的供应商模型；
- 托管供应商凭据并完成文本、多模态、图片和 LTX 视频协议转换；
- 把供应商错误统一映射为带 `ErrorInfo.reason` 的 gRPC status；
- 传播 OpenTelemetry TraceContext，但不记录 Prompt、媒体正文或密钥。

Prompt、业务重试、质量验收、任务状态、OSS、数据库和 MQ 始终由 `wgHub` 或
`wgAsyncTask` 持有。仓库只部署一个 `wg-model-hub` 服务，单个
`ModelHubService` 暴露四个强类型 RPC，不提供任意路径代理或万能扩展字段。
