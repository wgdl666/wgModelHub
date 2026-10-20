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

## Cohere 批量查询向量

`Generate` 的 `model=embed-v4.0` 支持显式批量 query：在 `input.model_traits` 中传
`{"embedding_batch":true,"input_type":"search_query"}` JSON 字符串，在 `input.items` 中按序放置 USER message，
每条恰好一个非空 text part。批量不接受图片、工具内容或文档模式。Wardrobe 去重后一次提交完整逻辑请求，
不感知供应商单批限制，也不在超限后拆小重发。

ModelHub 先校验完整批量请求的条数/原始文本字节数，再按 `cohere_embedding.max_batch_size` 顺序拆批，
调用 Cohere `/v2/embed` 的 `texts[]`，并按输入顺序合并。500条默认拆为96×5+20，依次调用6次；
任一批失败就停止，不发布部分结果。一个逻辑请求同一时刻最多执行一个批次，不放大调用方并发。
不同RPC之间不设共享并发池；请求并发、业务排队及总deadline由调用方管理。

配置位于每个 Cohere provider 的 `cohere_embedding`：

```yaml
max_batch_size: 96               # 缺省96，范围1..96；供应商单次请求容量
max_request_texts: 512           # 缺省512，范围1..1024；单个逻辑批量请求准入上限
max_request_text_bytes: 1048576  # 缺省1 MiB，最大4 MiB；批量文本的原始UTF-8总字节
```

配置由Provider启动时读取，修改需重启。超出整单条数或文本字节上限时，出站前直接拒绝。
512条1024维向量按每个数32字节保守估算约16 MiB，最大1024条约32 MiB，为64 MiB RPC保留包装空间。
每批HTTP响应限4 MiB，完整结果限32 MiB；校验行数、1024维、有限值和null，避免文本与向量错位。
返回文本 JSON 为 `{"embeddings":[[...],[...]]}`，单元素批次也保留复数键。

Provider不新增分片重试、退避、工作协程或整单超时配置。保留原有单次HTTP 20秒超时及调用方deadline。
瞬态失败仍由已有Service文本重试机制处理，可能重新执行整个逻辑请求，包括之前成功的分片；
取消、超时和非瞬态错误遵守原Service规则。这里不承诺分片续算或失败后仅重算失败片。
`cohere.embedding.batch_request`记录条数、批次数，原有`provider.http`记录每次上游耗时，
不记录文本、向量或密钥。

只有显式批量标志才拆分输入。普通多段文本仍合成单个输入，以 `{` 开头的商品文本仍走
`search_document`，图片保留单图路径，继续返回 `{"embedding":[...]}`；单条调用不新增并发和总超时预算。
批量容量限制只作用于批量输入；`model_traits`若非空仍须是合法JSON对象。
未新增RPC、模型ID、凭据或自动切换向量空间；只接入官方Cohere Provider，Bedrock/DashScope保持原实现。
应先发布此协议，再发布Wardrobe并开启其默认关闭的 `deep_recommend.batch_embedding_enabled`。
关闭调用方开关后新冻结恢复单条协议。本PR不修改线上配置、不部署。

## VWorld/LTX 视频动作参数

`SubmitGeneration` 调用 `VWorld_outfit_video_1.0` 时，调用方可在 `request.input.model_traits` 中传 JSON 字符串 `{"motion_tips":"Slowly turn the body 30 degrees."}`。LTX 适配器读取 `motion_tips`，去除首尾空白后写入 `POST /vton` 的独立 multipart 字段；`image`、`prompt` 和上游 `model=ltx` 保持原协议。

该参数可省略；缺失、空串或仅空白时不发送表单字段。非法 JSON、非对象或非字符串值在出站前拒绝；其他模型特性键忽略。ModelHub 只透传调用方的动作文本，不追加业务动作规则，也不把它合并到 `prompt`。动作内容参与已有请求哈希：同一 caller/request_id 改动作会返回 `ALREADY_EXISTS`。

上线须先确认上游 `/vton` 消费该字段，再发布本适配器和调用方。此行为只适用于 VWorld/LTX。

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

### AWS-G6 视觉服务

`config/aws-g6.providers.yaml` 是待合并的 provider 和路由配置，不是完整的 Nacos/AppConfig 配置；合并时保留其他模型、数据库、日志等设置。部署前须从实际 ModelHub 运行环境验证 `172.31.43.7:18280` 可达，连接资源变化须滚动重启。

美东目标按 `wgPlatformInfra/config/services-tomirro/modelhub.yaml` 核对：代码分支是 `dev`，运行身份是 `US/ppe_exhibition`，ECS 为 `us-east-2` 的 `tomirro/modelhub-service`。AppConfig 为 `modelhub / ppe_exhibition / config-ppe_exhibition`（application `u8e7hop`、environment `em4ujml`、profile `n1chtur`），不是新加坡使用的 `modelhub / dev / config-dev`。发布前再从实际 ECS task 和 AppConfig metadata 核实身份；备份当前完整配置，合并 overlay 后执行 `ParseAndValidateYAML`。先让支持新 provider 的代码发布成功，再部署新配置并滚动重启；验收失败时恢复原配置版本和原健康镜像。仓库现有美东发布流程是 `.github/workflows/deploy-aws-exhibition.yml` → `tomirro-modelhub` 托管流水线，按正式 `dev` 的精确 SHA 发布。

美东路演的 `deepseek-v4.1-flash` 加在已有实例 `阿里云_MaaS_ws-qnsgk3r40cvpyn30` 的 `models` 列表里，地址保持该工作空间的 `us-east-1.maas.aliyuncs.com`。不要另建 Fireworks 实例，也不要把密钥写进仓库。只改 `models` 可热更新；国内方舟示例不要收录这条。该模型在弗吉尼亚的部署范围是 Global。

| 服务 | ModelHub ID | G6 路径 | 输出 |
| --- | --- | --- | --- |
| HumanParser | `human-parser`、`human_parse` | `/human-parser/human_parser/predict` | 解析 JSON |
| SegmentPerson | `segment-person-bria` | `/segment-person/api/segment` | PNG，人像 Portrait |
| SegmentPerson | `segment-subject-bria` | `/segment-person/api/segment_subject` | PNG，主体 BRIA |
| HumanYolo | `human-yolo` | `/human-yolo/predict` | 检测 JSON |
| ClothYolo | `cloth-yolo` | `/cloth-yolo/predict` | 检测 JSON |
| FaceSim | `facesim` | `/facesim/compare` | 比对 JSON |

人像 ID 是历史调用 ID，实际算法由 `segment_person.person_method: person-birefnet-portrait` 选择；主体用 `subject_method: person-bria-rmbg`。原有 `method` 仍可作为兼容回退。旧 `human-parser` provider 的 base URL 包含 `/human_parser`，新 `human_parse` provider 则只包含服务前缀，两者最终请求同一预测接口。

这些上游可省略用户名和密码；配置 Basic Auth 时必须两项同时填写。FaceSim 输入两张图片，`similarity` 保持 0–1，`cosine_similarity` 保留上游带符号的余弦分数。ClothYolo 是独立服装检测能力，不参与人像抠图过滤。

在可访问 G6 的环境运行真实图片验证：

```bash
go run ./cmd/g6-smoke -image /path/to/portrait.jpg -out artifacts/g6-smoke
```

该命令在进程内启动候选 ModelHub gRPC 服务，检查上述七个 ID 的真实上游结果，并保存 JSON 和透明 PNG；不修改线上部署或 Nacos。FaceSim 使用同图自比。耗时包含适配器和上游请求，不能代表跨区域调用延迟。

`models.Flux2Klein9B` 当前仅支持带参考图的 image edit（i2i），须绑定独立 OpenAI Images 实例。
`models.GPTImage2` / `models.GPTImage25Flare` / `models.GPTImage25Sunburst` 走 OpenAI Images API（generations/edits），示例与审计目标实例键都是 `AIG_openai`（`https://api.aig-ai.com/v1`），不能并入 Gemini generateContent 生图实例。
`models.GeminiNanoBanana21`（`gemini-nano-banana-2.1`）走 Vertex 的现有 Generate 图片链路，示例实例是 `Vertex_AI_nano_banana`。调用方使用 `request.model` 加 `OutputSpec.image`。Vertex 生图固定 `locations/global`，必须显式配置 `vertexai.project`，不从上游错误文案中探测项目。分辨率原样传 `1K` / `2K` / `4K`；思考推荐 `MINIMAL` / `MEDIUM` / `HIGH`，`LOW` 和 `temperature` 原样下发，由上游决定是否接受。

### Vertex 文本、生图与 Google rerank 统一 ADC

Vertex 只使用 ADC，不再支持 `vertexai.api_key`，`vertexai.project` 必填。文本、流式文本、缓存创建和生图共用一个 `locations/global` Vertex 客户端，Google SDK 的鉴权库自动获取、缓存和刷新 access token。模型名、输入输出协议、工具和生图参数保持原样。

```yaml
providers:
  Vertex_AI:
    models: [gemini-2.5-flash, gemini-nano-banana-2.1]
    vertexai:
      project: "your-google-cloud-project"
      # proxy_url: http://your-existing-proxy:1081
```

运行环境设置 `GOOGLE_APPLICATION_CREDENTIALS=/etc/google/credentials.json`，将服务账号或联合身份凭据以只读文件提供给容器 UID `10001`。它可以与下面 `google_rerank` 读取同一份凭据；token 由各自的鉴权库实例缓存，不要求两条链路共用同一条 token 字符串。私钥与 token 不放入 AppConfig/Nacos。`vertexai.proxy_url` 可空；配置后换 token 与模型请求共用该出口。

统一身份需要项目启用 `aiplatform.googleapis.com`，并给服务账号授予 `aiplatform.endpoints.predict` 和配额项目的 `serviceusage.services.use`；调用缓存创建 RPC 时还需要 `aiplatform.cachedContents.create`。Google rerank 的 `discoveryengine.rankingConfigs.rank` 不能替代 Vertex 权限。

这是一次需要同步运行环境的鉴权迁移：发布前先提供运行时凭据和 Vertex 权限，更新线上 AppConfig Validator 的 Vertex 规则，再从所有 Vertex 实例配置中删除 `api_key` 并显式配置 `project`，通过正式发布入口滚动重启。仅修改仓库 schema 不会自动更新线上 Validator。旧的 API key 配置不能继续提供 Vertex 鉴权；没有项目或 ADC 凭据时启动失败。项目/代理变化不能热更新；模型调用不会使用 `GOOGLE_API_KEY` 或 `GEMINI_API_KEY` 环境变量中的 key。

参考：[Vertex ADC](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/start/gcp-auth)、[Vertex IAM 权限](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/access-control)。

## Google 文本精排（Ranking API）

Google 文本精排通过独立 `google_rerank` provider 调用 Discovery Engine
`rankingConfigs/default_ranking_config:rank`，固定 `locations/global`。它继续使用已有
`Generate` RPC 和 `output.text`，不经过 Vertex 的 Gemini `generateContent`，身份与 Vertex 一样使用运行环境 ADC。

```yaml
providers:
  Vertex_AI_google_rerank:
    models: [semantic-ranker-default-005, semantic-ranker-fast-005]
    google_rerank:
      project: "your-google-cloud-project"
      # 国内填写已有出口；换 token 和 Ranking API 共用该代理。
      # proxy_url: http://your-existing-proxy:1081
```

AppConfig / Nacos 的 `providers.<实例>.models` 控制开放哪些真实模型 ID；调用方通过
`request.model` 选择其中一个，适配器原样传给 Google。仅调整列表可热更新，不重建客户端。
例如请求 `semantic-ranker-fast-005` 就调用快速版，未在列表声明的模型在出站前拒绝。
也已注册 Google 官方的 `semantic-ranker-default@latest`、`semantic-ranker-fast@latest`；
需要跟随 Google 更新时再将相应 ID 加入列表。列表不设置调用方默认模型，也不将旧 ID 改写为新 ID。

用户消息的文本 part 放 JSON：

```json
{"query":"白色通勤衬衫","documents":["黑色休闲裤","白色长袖通勤衬衫"],"instruction_key":"look"}
```

返回文本格式保持 `{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}]}`。
下标对应输入 `documents`；顺序沿用 Google 的相关性排序，分数由 Google 返回，示例值不代表线上打分。
Google 不支持独立 `instruct`，按共享 `rerank_templates[look].query_template` 渲染 Query。每次请求接受 1–1000 段非空文本，
请求所有候选的分数；记录超长按 Google 模型的 token 限制截断。流式请求发送一段完整结果和唯一的 metadata final。

上线前必须启用 `discoveryengine.googleapis.com`，并给服务身份授予
`discoveryengine.rankingConfigs.rank` 及配额项目的 `serviceusage.services.use` 权限。
provider 使用 Google 标准 ADC（Application Default Credentials）自动缓存、刷新 OAuth access token。
ACK 中应将专用服务账号凭据或 Workload Identity Federation 配置以只读文件挂载，设置
`GOOGLE_APPLICATION_CREDENTIALS=/path/to/credentials.json`。联合身份文件引用的 Kubernetes token
也必须挂载并可轮换。凭据文件对容器 UID `10001` 可读；私钥、用户 refresh token、access token
不得提交仓库，也不填进 Nacos 模型配置。开发可另行执行 `gcloud auth application-default login`；
普通 `gcloud auth login` 并不会自动为 Go 客户端提供 ADC。

凭据缺失会在启动建立 provider 时返回配置错误；token 交换与模型请求共享取消和总计 20 秒超时。
API key 不会作为鉴权降级路径。项目、代理或 provider 增删须滚动重启；模型列表/路由仍按现有规则热更新。
本变更不自动创建云端账号、修改 Nacos 或部署；审核后配置运行身份，再通过 `workspace` 正式发布并验收。

参考：[Google Ranking API](https://docs.cloud.google.com/generative-ai-app-builder/docs/ranking)、
[ADC](https://docs.cloud.google.com/docs/authentication/application-default-credentials)、
[Kubernetes 工作负载身份联合](https://docs.cloud.google.com/iam/docs/workload-identity-federation-with-kubernetes)。

### 共享精排指令模板

调用方在现有 Generate 用户文本 JSON 中传 `query`、`documents`、`instruction_key`（`color` 或 `look`）。真实模型仍由外层 `request.model` 选择，protobuf 不变；ModelHub 不负责竞速、分数门槛或业务验收。

`config/example.modelHub.yaml` 的顶层 `rerank_templates` 同时适用于 Nacos 与 AppConfig：每个键必须配置 `instruct` 和含 `{{query}}` 的 `query_template`，不按厂商分模板。

- Qwen 文本和 VL 支持独立指令：保留原 Query，分别发送 `instruct` / `parameters.instruct`。
- Google、Mixedbread、Cohere 官网、Bedrock Cohere：将原 Query 替换进 `query_template`，只向下游发送渲染后的 Query。
- 文档、真实模型 ID 和结果下标不变；内部 `instruction_key` 不发给供应商。原 Query 是文本，不执行其中的模板占位或 JSON。

模板每次请求读取同一份配置快照，可热更新；格式错误整单拒绝并保留旧配置。未知键、缺少模板或同时传 `instruct` 与 `instruction_key` 时在出站前报错，不默认为颜色模板。

迁移先上线 ModelHub 与模板配置，再升级 Wardrobe。无 `instruction_key` 的旧请求保持原行为：Qwen 发送调用方指令，Google/Cohere 丢弃指令，Mixedbread 保留历史主色包装。新协议不再依靠英文指令匹配判断用途，也不会二次包装 Mixedbread look。旧协议待调用方迁移完成后可单独移除。

共享颜色模板采用原始门槛评测问法 `检索主色是「{{query}}」的单品`（无句号），与 Mixedbread 旧主色包装一致。look 使用 Google 已测的统一模板；Mixedbread 旧 look 使用原始短 Query，因此旧 look 门槛须在新模板下重新核验。本分支不改线上模板、业务门槛或竞速配置。

本次按两组 review：Wardrobe `codex/deep-recommend-rerank-race` 负责竞速和业务门槛；参数对齐由 Wardrobe `codex/rerank-instruction-key` 与本仓库 `codex/rerank-instruction-templates` 配套完成。本仓库 AppConfig 只新增 `rerank_templates`，沿用现有 Google/Mixedbread provider 与模型路由。模板不放在 Wardrobe，模型门槛不放在 ModelHub；当前只提供草案，不发布 AppConfig、不合并 dev。
