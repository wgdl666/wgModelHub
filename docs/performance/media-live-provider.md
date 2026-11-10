# 图片请求内存优化：真实供应商联调

日期：2026-11-10。候选为本 PR 的本地代码；没有合并、部署或修改线上配置。

## 结果

六条真实模型链路最终返回 HTTP 200，客户端均收到有效结果和唯一 final。此次真实联调发现了初版 mock 语义对照遗漏的 AIG multipart 引号兼容问题，已修复并复测。

| 模型 / 渠道 | 请求路径 | 本次耗时 | 返回验证 |
|---|---|---:|---|
| GPT Image 2 / AIG | `/v1/images/edits` | 修复后 30.10 s | 1024×1024 PNG，1,135,515 bytes；usage 回传 |
| GPT Image 2.5 Flare / AIG | `/v1/images/edits` | 修复后 37.49 s | 1024×1024 PNG，1,254,116 bytes；usage 回传 |
| GPT 5.6 Luna / AIG | `/v1/chat/completions` | 29.90 s | 流式回复 `White T-shirt`；final 与 usage 正常 |
| Cohere embed-v4.0 | `/v2/embed` | 8.09 s | 有效 1024 维向量 |
| Photoroom | `/v1/segment` | 修复后 6.40 s | 1280×2774 RGBA PNG，alpha 范围 0–255 |
| 现网 legacy human-parser | `/human_parser/predict` | 0.79 s | success=true，18 类标签及解析数据 |

这是每条链路单次成功样本的耗时，不能据此比较模型速度或推断稳定容量。JSON 路径在初轮通过；后续修改仅涉及 multipart 头，图片编辑和 Photoroom 在恢复引号后重新调用成功。

共发起 **10 个真实 HTTP 请求：8 个 200、2 个修复前的 403**，包含一次旧版本控制实验；没有额外自动重试。不是“初版全部通过”。逐次状态、耗时、字节数、usage、输出摘要和本地 call_id 见 [原始联调结果](media-live-provider-results.json)。图片/向量原件保留在本机输出目录，没有提交到仓库。

## 发现并修复的兼容问题

初版 `httpbody.Field/File` 使用 `mime.FormatMediaType`。对于简单参数，它输出：

```http
Content-Disposition: form-data; name=model
```

旧版 `multipart.Writer` 输出的是：

```http
Content-Disposition: form-data; name="model"
```

虽然两种写法符合 MIME 参数语法，真实 AIG 图片编辑对初版的请求返回了 403；GPT Image 2.5 Flare 的错误消息为 `model error`。相同素材/提示/尺寸/质量下，控制结果为：

1. 旧版 dev `f41f1463`：GPT Image 2 返回 **200**。
2. 初版优化代码：GPT Image 2 和 GPT Image 2.5 Flare 均返回 **403**。
3. 保留分段 Reader，只恢复标准参数引号后：两种图片模型均返回 **200**，收到有效 PNG；Photoroom 再测也返回 **200**。

这是客户端对照证据，未查看 AIG 服务端解析实现，不能进一步断言其内部用了哪种解析器。

修复位置：`internal/infra/httpbody/multipart.go`。文件参数直接复用标准库 `multipart.FileContentDisposition`；字段参数与 `Writer.CreateFormField` 一样保留双引号和转义。没有恢复完整请求体缓冲，也没有增加重试或供应商专用分支。

新增 `TestMultipartDispositionMatchesStandardWriter`，逐字比较字段/文件 Content-Disposition，覆盖普通名称、引号/反斜杠和 CR/LF 转义。此前“按语义相同即可”的验证不足以证明厂商兼容，这个限制已在本次联调中纠正。

## 测试配置和素材

- 配置来自只读查询的美东路演当前 AppConfig：`modelhub / ppe_exhibition / config-ppe_exhibition`，生效部署 61，配置版本 **67**。凭据只在本地受限文件和测试进程内使用，没有写入仓库或报告。
- 图片复用美东 System Baseline 的既有 OOTD 用例 `tc_113402c4130391862e20cd1af9dbfae0`，素材 `492057f1b08b80269e0ef04001803102`，348,212 bytes，SHA-256 `aa2a0c5e179aa02edaca5159b97e3cbf2bb7a5c4152ac474292de023525a7c01`。
- 已检索 Cursor 的 ModelHub 用例（无匹配）和 System Baseline OOTD 用例（命中上述素材）。平台业务 Runner 固定调用线上部署，不能指向未合并的本地候选版本。因此只复用图片，使用本地供应商协议探针；不声称重新运行或通过了平台 OOTD 业务用例。
- 实际调用链：本工作树的 gRPC 客户端 → 本工作树 Service / factory / 真实适配器 → 真实供应商 HTTP → gRPC 客户端。gRPC 使用 bufconn HTTP/2 传输；没有本地 mock 上游。
- 输入/输出账本任务保持异步执行；存储写到本地文件，避免修改生产数据库或对象存储。供应商凭据、渠道地址来自当前配置。
- GPT 图片编辑使用一张参考图，1024×1024、quality=low；看图只要求短回答，最多 1024 completion tokens。本次是兼容性验证，没有做图片质量评测。

## 未覆盖

美东 G6 的 `172.31.43.7:18280` 从本机 TCP 连接超时，当前 AWS 身份没有 EC2 查询权限。没有为了本次测试发布服务或放开端口。因此 SegmentPerson/FaceSim 的 **Base64 multipart 真实链路**、G6 HumanYOLO/ClothYOLO 和新 HumanParse 没有验证；它们的 mock/重试测试不能替代真实联调。

VMind Go gateway、VWorld 直连和 LTX 视频未做真实生成；Google SDK 实现未修改，本次也未单独测试。没有验证线上数据库/OSS/S3、线上 OOTD 完整业务链路或容量。

可重跑的脚本、素材来源及期待结果随 PR 保存；平台能按候选版本路由供应商协议测试后，再迁入对应测试集合，不将本次临时探针标为平台 verified 用例。

## 复现入口

```sh
# providers.yaml 必须是仓库外的私有配置，只放要测的实例，勿提交凭据。
GOTOOLCHAIN=go1.26.0 go run ./scripts/provider-smoke \
  -config /private/path/providers.yaml \
  -image /private/path/portrait.jpg \
  -out /private/path/results \
  -models embed-v4.0,photoroom-segment,gpt-5.6-luna,gpt-image-2,human-parser
```

只运行列出的模型，每个模型一个 gRPC 请求，沿用 Service/适配器原有重试策略；脚本在任何请求失败时返回非零退出码。`results.json` 会在每个请求结束后落盘，失败也保留，不应通过反复重提覆盖失败记录。

修复后完成了串行全量测试、相关 `-race`、smoke 脚本和 httpbody 的 `go vet`，并重新执行了同规模 Docker 新旧性能对照，见 [性能报告](media-memory.md)。
