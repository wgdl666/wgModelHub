# wgModelHub 仓库边界

- `wgModelHub` 是 WG 内部统一的大模型协议适配层，只负责 `model_profile` 路由、供应商凭据托管、协议转换与错误归一化。
- Prompt 编排、业务重试、质量验收、任务状态、OSS/DB/MQ 始终留在 `wgHub` 或 `wgAsyncTask`；ModelHub 成功只表示供应商调用完成，不表示业务任务终态。
- 单个 Deployment、单个 gRPC `ModelHubService`、端口 `50053`；不提供任意路径代理、万能 RPC 或调用方注入供应商地址/密钥。

# 配置与 profile

- Nacos Data ID：`wg.mirror.modelHub`；bootstrap 只保存 Nacos 定位信息，凭据与模型映射在 YAML 正文中。
- 稳定 profile 名称集中在 `internal/profile/names.go`；`config.Config.Validate` 要求全部 `profile.Required()` 在启动前配齐。
- 每个 profile 一对一绑定 `provider + model + capability`；首版无候选列表、权重或自动降级。

# 协议不变量

- `Media` 使用 `mime_type` + `oneof bytes/uri`；ModelHub 不通过额外 HEAD 猜测类型。
- 图片 `ContentPart` 必须严格保留调用方顺序；响应用 `parts` 展开图片与诊断文字，不能只返回首张图。
- 文本必须保留 `ThoughtSignature`、`response_id`、`previous_response_id`、工具调用历史、usage、finish reason、JSON Schema、thinking 与流式取消。
- `optional` 标量（含 `temperature=0`）已设置时必须原样下发供应商。
- LTX 在 ModelHub 内同步提交/轮询/下载，再按 1MiB 分块返回；最大 200MiB，超限必须失败；不持久化 job。

# 错误与观测

- 供应商错误统一映射为 gRPC status + `google.rpc.ErrorInfo.reason`（domain=`wg.modelhub`）。
- OpenTelemetry/Logfire 只记录主机、状态码、字节数等元数据，禁止记录 Prompt、媒体正文或密钥。

# 发布

- 正式发布走 `workspace` 统一入口，不绕过它手改运行环境。
- ACK 清单见 `deploy/ack.yaml`：两副本、RollingUpdate、`terminationGracePeriodSeconds=180`、非 root `10001`。
