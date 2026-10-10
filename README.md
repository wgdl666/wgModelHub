# wgModelHub

`wgModelHub` 是 WG 服务内部统一的大模型协议适配层。它只负责：

- 依据 `request.model`（真实供应商模型 ID，含版本）路由到唯一 provider 实例；
- 托管供应商凭据并完成文本、多模态、图片、视频和 TTS 协议转换；
- 把供应商错误统一映射为带 `ErrorInfo.reason` 的 gRPC status；
- 传播 OpenTelemetry TraceContext，但不记录 Prompt、媒体正文或密钥。

Prompt 编排、业务重试、质量验收、任务状态、OSS、数据库和 MQ 始终由 `wgHub` 或
`wgWardrobe` 持有。仓库只部署一个 `wg-model-hub` 服务，单个 `ModelHubService` 暴露：

```text
rpc Generate(GenerateRequest) returns (stream GenerateEvent);
rpc SynthesizeSpeech(SynthesizeSpeechRequest) returns (SynthesizeSpeechResponse);
rpc SynthesizeSpeechStream(SynthesizeSpeechRequest) returns (stream SynthesizeSpeechResponse);
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
WG_SERVER_HTTP_PORT=51053
```

AppConfig 资源为 `modelhub / dev / config-dev`，运行时通过 Agent 的
`127.0.0.1:2772` 端点获取。配置更新后由 ECS 重新部署任务，不在进程内热更新。
Gemini 的 `proxy_url` 留空即直连，AWS dev 不需要额外代理开关。

AWS 新加坡 dev 仅通过 VPC 内的 `modelhub.internal.dev:50053` 提供 gRPC；不映射
`50054`，不创建公网负载均衡、证书或公网 DNS。数据库表也不会在服务启动时创建；
首次发布必须显式运行独立的 `migration` 镜像，该镜像只执行
`migrations/001_generation_task.sql` 以创建 `modelhub.generation_task`。所有 ModelHub
关系表均显式限定在 `modelhub` schema；本次内网部署不得执行 `002` 或 `003`。

## GPT Image 2 / 2.5 internal example

调用方必须已连接 dev VPC/VPN，并且输出文件的父目录必须已存在。内部端口 `50053`
不需要 authorization header。下面的命令默认超时为五分钟；如要替换已有输出文件，
追加 `--force`。每次成功到达服务的调用都会产生一次供应商图片生成费用。
`--model` 可选值为 `gpt-image-2`、`gpt-image-2.5-flare`、
`gpt-image-2.5-sunburst`，省略时保持兼容并使用 `gpt-image-2`。

```bash
./scripts/examples/gpt-image-2.sh \
  --address modelhub.internal.dev:50053 \
  --model gpt-image-2.5-flare \
  --prompt "A small red paper boat floating on calm water" \
  --output ./gpt-image-2.5.png
```

`SynthesizeSpeech` 是独立 unary TTS：一次请求完整成功或 gRPC error。成功只表示供应商
合成且完整音频已收集完成，不表示 Mirror 已播放。输出为 `audio/mpeg`，采样率由供应商适配器确定。

`SynthesizeSpeechStream` 接收同样的请求，逐块返回 `audio.data` 中的增量 MP3 字节。
分块可以跨越 MP3 编码帧，调用方应按顺序交给同一个解码器。只有正常 EOF 表示合成完成；
收到音频后仍可能返回 gRPC error，此时不能把半截音频记为成功或自动重放。
取消 RPC 会关闭供应商 HTTP 请求。

当前流式能力由 ElevenLabs 的 `/v1/text-to-speech/{voice_id}/stream` 提供，使用
`mp3_22050_32`。模型和音色继续由原有路由与配置决定。没有流式能力的 provider 返回
`FailedPrecondition`，不会悄悄退回整段合成。原有 unary 接口保留。

调用账本的 operation 为 `synthesize_speech_stream`，输出只记总字节数、分块数和
首块耗时，不记录音频正文。总耗时包括 gRPC 发送背压，不能当作纯供应商生成耗时。
部署时先升级 ModelHub，再升级调用新 RPC 的 Hub；旧 ModelHub 不认识新 RPC。

## 公网 API Key（可选）

以下能力不属于 AWS dev 内网部署。内网 ACK 调用仍走 `wg-model-hub:50053`，无需 API Key。
若其他环境需要公网暴露，由 Deployment 注入 `WG_SERVER_PUBLIC_GRPC_PORT=50054`，独立
gRPC Server 强制 Bearer 鉴权：

```text
authorization: Bearer <从工程平台复制的 API Key>
```

平台创建/轮换返回的原始 Key 格式为 `wgmh_<key_id>_<secret>`（**不含** `Bearer ` 前缀）。
调用方在 gRPC metadata 中自行拼接，例如 `authorization: Bearer wgmh_...`。

- 目标公网域名为 `modelhub.dev.wgdl.tech`；公网鉴权端口 **50054**。
- Nacos 不拥有监听端口；公网前置**只能**转发到 **50054**，**绝不能**转发到未鉴权的 **50053**。
- 建议反代：透传 `Authorization`、单消息 ≥ 64MiB、超时 ≥ 15 分钟（长视频任务）。公网口同时接原生 gRPC（HTTP/2）与运营台浏览器 grpc-web（HTTP/1.1，`application/grpc-web+proto`）；CORS 只放行 `https://ops.wgdl.tech` 与本地 Vite。
- Key 数据存 `modelhub.modelhub_api_key` 表（见 `migrations/002` / `003`）；DDL 仅由部署 migration 身份执行，Ops 受限账号仅 `SELECT` / `INSERT` / `UPDATE(revoked_at)`。
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
`SynthesizeSpeech` 或 `SynthesizeSpeechStream`，不进入 `OutputSpec`。供应商地址与密钥不会进入 RPC。

配置中每个 provider 实例声明 `models: [...]`；启动时建立「真实模型 ID →
provider」路由。同一模型仅被一个实例声明时可隐式选定；被多个实例声明时必须在
`model_routes` 显式选定。同名同资源 provider 仅 `models` / `model_routes` 可经
Nacos 热更新；实例增删或凭据/端点等资源参数变化须滚动重启。调用方应引用
`github.com/wgdl666/wgModelHub/models` 常量，例如 `models.Speech28Turbo`。
`models.Flux2Klein9B` 当前仅支持带参考图的 image edit（i2i），须绑定独立 OpenAI Images 实例。
`models.GPTImage2` / `models.GPTImage25Flare` / `models.GPTImage25Sunburst` 走 OpenAI Images API（generations/edits），复用现网已实测的 AIG 实例 `async_gpt_image`（`https://api.aig-ai.com/v1`），不能并入 Gemini generateContent 生图实例。
`models.GeminiNanoBanana21`（`gemini-nano-banana-2.1`）走 Vertex Express 的现有 Generate 图片链路，示例实例是 `vertex_nano_banana`。调用方使用 `request.model` 加 `OutputSpec.image`。分辨率传 `1K` / `2K` / `4K`。该模型只在 `locations/global`。官方 Express 短路径不带 project，会落到调用方区域，所以实例必须显式配置 `vertexai.project`；生图从第一次就打 `projects/{project}/locations/global`，密钥仍是原来的 API key，不走 ADC。缺 project 时该模型返回配置错误，不出站。文本请求保持无 project 的 Express 短路径。思考推荐 `MINIMAL` / `MEDIUM` / `HIGH`；`LOW` 会原样下发，由上游拒绝。官方不支持 `temperature`，设置了会原样下发并返回上游错误。
