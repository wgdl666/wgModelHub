# wgModelHub

`wgModelHub` 是 WG 服务内部统一的大模型协议适配层。它只负责：

- 依据 `request.model`（真实供应商模型 ID，含版本）路由到唯一 provider 实例；
- 托管供应商凭据并完成文本、多模态、图片和 LTX 视频协议转换；
- 把供应商错误统一映射为带 `ErrorInfo.reason` 的 gRPC status；
- 传播 OpenTelemetry TraceContext，但不记录 Prompt、媒体正文或密钥。

Prompt 编排、业务重试、质量验收、任务状态、OSS、数据库和 MQ 始终由 `wgHub` 或
`wgWardrobe` 持有。仓库只部署一个 `wg-model-hub` 服务，单个
`ModelHubService` 暴露 server-streaming `Generate` 等 RPC：

```text
rpc Generate(GenerateRequest) returns (stream GenerateEvent);
```

## 配置源与 AWS dev 部署

配置源由环境变量 `WG_CONFIG_SOURCE` 选择：未设置或设为 `nacos` 时，继续读取
`/etc/wg-model-hub/bootstrap.json` 并监听既有 Nacos Data ID；设为 `appconfig` 时，
只从本机 AWS AppConfig Agent 启动加载一次，加载失败直接退出，不回退到 Nacos 或本地文件。

AWS dev 使用固定身份：

```text
WG_CONFIG_SOURCE=appconfig
APP_NAME=modelhub
ENV=dev
SERVICE_NAME=config-dev
REGION=us-east-2
WG_SERVER_GRPC_PORT=50053
```

AppConfig 资源为 `modelhub / dev / config-dev`，运行时通过 Agent 的
`127.0.0.1:2772` 端点获取。配置更新后由 ECS 重新部署任务，不在进程内热更新。
Gemini 的 `proxy_url` 留空即直连，AWS dev 不需要额外代理开关。

AWS 新加坡 dev 仅通过 VPC 内的 `modelhub.internal.dev:50053` 提供 gRPC；不映射
`50054`，不创建公网负载均衡、证书或公网 DNS。数据库表也不会在服务启动时创建；
首次发布必须显式运行独立的 `migration` 镜像，该镜像只执行
`migrations/001_generation_task.sql` 以创建 `modelhub.generation_task`。所有 ModelHub
关系表均显式限定在 `modelhub` schema；本次内网部署不得执行 `002` 或 `003`。

## 公网 API Key（可选）

以下能力仅保留给阿里云/Nacos 的独立部署方案，不属于 AWS dev。内网 ACK 调用仍走
`wg-model-hub:50053`，无需 API Key。若未来需公网暴露，在 Nacos 配置
`server.public_listen_address: ":50054"`，由独立 gRPC Server 监听并强制 Bearer 鉴权：

```text
authorization: Bearer <从工程平台复制的 API Key>
```

平台创建/轮换返回的原始 Key 格式为 `wgmh_<key_id>_<secret>`（**不含** `Bearer ` 前缀）。
调用方在 gRPC metadata 中自行拼接，例如 `authorization: Bearer wgmh_...`。

- 目标公网域名为 `modelhub.dev.wgdl.tech`；当前代码与 `deploy/ack.yaml` 声明公网鉴权端口 **50054**（`public_listen_address`）。
- 真正可用仍需：部署清单生效、Nacos 配置、`migrations/002_modelhub_api_key.sql` + `003`（expires_at 可空）、受限 DB 账号，以及 `8.129.236.215` 上 TLS/SNI + gRPC HTTP/2 反代实际生效并完成验收。
- Nacos 开启 `public_listen_address` 后，公网前置**只能**转发到 **50054**（鉴权 gRPC），**绝不能**转发到未鉴权的 **50053**。
- 建议反代：gRPC over HTTP/2、透传 `Authorization`、单消息 ≥ 64MiB、超时 ≥ 15 分钟（长视频任务）。
- Key 数据存 `modelhub.modelhub_api_key` 表（见 `migrations/002` / `003`）；DDL 仅由部署 migration 身份执行，Ops 受限账号仅 `SELECT` / `INSERT` / `UPDATE(revoked_at)`。
- 明文 secret 仅创建/轮换时通过工程平台返回一次（JSON 字段 `api_key`）。
- 吊销阻止后续 RPC 鉴权，不中断已经开始的流式 RPC。
- 鉴权成功后 caller 固定为 `public:<principal_id>`，覆盖客户端自报的 `x-wg-caller-service`。
- Key 生命周期由工程开发平台（`wgOpsPlatform`，页面 `/modelhub-api-keys`）管理，需配置 `MODEL_HUB_DATABASE_URL` 受限库连接。
- `expires_at IS NULL` 表示永不过期；新建/轮换未指定到期时间时默认写入 NULL。已有非空到期时间不回填。
- **轮换**在同一 `principal` 下签发新 Key；旧 Key 在调用方显式吊销前仍有效，便于切换窗口（轮换不会自动吊销旧 Key）。

`GenerateRequest` 顶层恰好三个业务字段：`model` / `input` / `output`。
system 指令、用户任务、对话历史、媒体与 tool 回执一律按序放入 `Input.items`；
capability 由 `OutputSpec` oneof（text / image / video）决定；供应商地址与密钥
不会进入 RPC。

配置中每个 provider 实例声明 `models: [...]`；启动时建立「真实模型 ID →
provider」路由。同一模型仅被一个实例声明时可隐式选定；被多个实例声明时必须在
`model_routes` 显式选定，改配置后重启生效。调用方应引用
`github.com/wgdl666/wgModelHub/models` 常量，例如 `models.GPTImage2`。
