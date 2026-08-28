# wgModelHub

`wgModelHub` 是 WG 服务内部统一的大模型协议适配层。它只负责：

- 依据 `request.model`（真实供应商模型 ID，含版本）路由到唯一 provider 实例；
- 托管供应商凭据并完成文本、多模态、图片、视频和同步 TTS 协议转换；
- 把供应商错误统一映射为带 `ErrorInfo.reason` 的 gRPC status；
- 传播 OpenTelemetry TraceContext，但不记录 Prompt、媒体正文或密钥。

Prompt 编排、业务重试、质量验收、任务状态、OSS、数据库和 MQ 始终由 `wgHub` 或
`wgWardrobe` 持有。仓库只部署一个 `wg-model-hub` 服务，单个 `ModelHubService` 暴露：

```text
rpc Generate(GenerateRequest) returns (stream GenerateEvent);
rpc SynthesizeSpeech(SynthesizeSpeechRequest) returns (SynthesizeSpeechResponse);
```

`SynthesizeSpeech` 是独立 unary TTS：一次请求完整成功或 gRPC error。成功只表示供应商
合成且完整音频已收集完成，不表示 Mirror 已播放。第一版输出固定为 `audio/mpeg`
（MP3 / 16kHz / mono）。

## 公网 API Key（可选）

内网 ACK 调用仍走 `wg-model-hub:50053`，无需 API Key。若需公网暴露，由 Deployment 注入
`WG_SERVER_PUBLIC_GRPC_PORT=50054`，独立 gRPC Server 强制 Bearer 鉴权：

```text
authorization: Bearer <从工程平台复制的 API Key>
```

平台创建/轮换返回的原始 Key 格式为 `wgmh_<key_id>_<secret>`（**不含** `Bearer ` 前缀）。
调用方在 gRPC metadata 中自行拼接，例如 `authorization: Bearer wgmh_...`。

- 目标公网域名为 `modelhub.dev.wgdl.tech`；公网鉴权端口 **50054**。
- Nacos 不拥有监听端口；公网前置**只能**转发到 **50054**，**绝不能**转发到未鉴权的 **50053**。
- 建议反代：gRPC over HTTP/2、透传 `Authorization`、单消息 ≥ 64MiB、超时 ≥ 15 分钟（长视频任务）。
- Key 数据存 `modelhub_api_key` 表（见 `migrations/002` / `003`）；DDL 仅由部署 migration 身份执行。
- 明文 secret 仅创建/轮换时通过工程平台返回一次（JSON 字段 `api_key`）。
- 鉴权成功后 caller 固定为 `public:<principal_id>`。

## 调用示例

### 内网 ClusterIP（无鉴权）

```bash
# 需在可访问 ACK ClusterIP / VPC 的环境执行；示例不含真实密钥。
grpcurl -plaintext \
  -d '{"model":"speech-2.8-turbo","text":"你好镜子"}' \
  wg-model-hub.default.svc.cluster.local:50053 \
  wg_model_hub.v2.ModelHubService/SynthesizeSpeech
```

### 公网 TLS + API Key

```bash
grpcurl -d '{"model":"speech-2.8-turbo","text":"你好镜子"}' \
  -H 'authorization: Bearer wgmh_<key_id>_<secret>' \
  modelhub.dev.wgdl.tech:443 \
  wg_model_hub.v2.ModelHubService/SynthesizeSpeech
```

无 `Authorization` 的公网请求应返回 `Unauthenticated`。

`GenerateRequest` 顶层恰好三个业务字段：`model` / `input` / `output`。
system 指令、用户任务、对话历史、媒体与 tool 回执一律按序放入 `Input.items`；
capability 由 `OutputSpec` oneof（text / image / video）决定；TTS 走独立
`SynthesizeSpeech`，不进入 `OutputSpec`。供应商地址与密钥不会进入 RPC。

配置中每个 provider 实例声明 `models: [...]`；启动时建立「真实模型 ID →
provider」路由。同一模型仅被一个实例声明时可隐式选定；被多个实例声明时必须在
`model_routes` 显式选定，改配置后重启生效。调用方应引用
`github.com/wgdl666/wgModelHub/models` 常量，例如 `models.Speech28Turbo`。
