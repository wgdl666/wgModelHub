# ModelHub 图片请求内存优化：本地 Docker 对照

日期：2026-11-10。基线：最新 `origin/dev` 的 `f41f14631351a340280e5aca7ed359b003fbc8b7`；优化版本：本 PR。没有合并或部署。

## 结论

本次短测里，8 并发的容器峰值内存平均下降 **24%～56%**；每请求累计分配下降 **33%～49%**。账本输入上传结束后不再由 `Call` 持有图片，在等待模型期间，8 个请求的账本场景存活堆增量从 **37.15 MiB 降到 18.59 MiB**。

CPU 改善没有内存那么确定：单并发四场景均值下降 12%～26%；8 并发 multipart/JSON 的 CPU 均值略降，但两轮有重叠；Base64 表单接近持平，纯账本从 0.708 增到 0.807 ms/请求（+14%，绝对增加 0.10 ms，GC 次数从 7 到 8）。因此不能宣称所有链路 CPU 都下降。单并发 Base64 JSON 的 GC 次数也增加（27→32）：存活堆变小会影响 GC 触发预算，分配下降不保证 GC 次数下降。

这些数字是适配器和账本代码的局部对照，不是 OOTD 整链路、线上容量或 OOM 修复结论。不会省掉模型推理时间或 API 调用次数。

## 改动范围

- 输入账本保留 `proto.Clone` 隔离，直接接管快照中的图片/视频字节，省掉第二次复制；输出和视频分块仍隔离复制。
- `Call` 移除 `blobs` 成员，将其作为后台 `insert` 参数；上传与初始记录完成后，后台函数退出即可解除引用，不等模型返回，也不手动清 nil。
- 新增 `internal/infra/httpbody`：小块 multipart 头尾引用 + 图片/文本 Reader，经 `io.MultiReader` 发送；保留精确 `ContentLength` 和 `GetBody` 重放。没有 Pipe、额外发送 goroutine 或新依赖。
- 9 个 multipart 构造点统一：OpenAI Images edits、HumanYOLO、ClothYOLO、Photoroom、VWorldImage、LTX、VMind Decision、SegmentPerson、FaceSim。最后两个字段仍按厂商协议使用 Base64。
- 人体解析、Cohere embedding、OpenAI 看图/chat、VMind Go gateway 的 JSON 使用分段请求体，避免把已有的大 Base64 字符串再编码进完整 JSON 缓冲；其他 JSON 叶子仍交给标准库，保留各适配器 HTML 转义策略。Base64 编码及 Data URI 本身仍有分配，没有改成逐块 Base64 编码。
- 厂商 endpoint、字段、鉴权、重试次数、超时、错误归类、模型选择保持原逻辑。Google GenAI SDK、Wardrobe、RPC/数据库结构没有修改。

## 测试方法与边界

同一个 `scripts/media-perf/main.go` 复制到基线和优化工作树，分别编译，再在独立 Docker 容器中运行；不是分别手写两套“模拟新旧实现”。

- Go **1.26.0**，`CGO_ENABLED=0 GOOS=linux GOARCH=arm64`；Docker/Colima 原生 Linux arm64，无 QEMU。
- 每个容器限制 **1 CPU / 2 GiB**；`GOMAXPROCS=1 GOGC=100`，`--network none`。上游在容器内本进程的 loopback HTTP server；不连接真实厂商、数据库、OSS 或线上服务。
- 输入：本地已归档的真实 PNG，1024×1536，2,428,916 bytes（2.316 MiB）。SHA-256：`a08ec61962ea9362494ea1604330c754aec7e3d455c00eb31aac512e049d8dc5`。图片没有提交仓库。
- 四场景分别调用真实 HumanYOLO、SegmentPerson、HumanParser 适配器；纯账本场景只测 `BuildGenerateInput→Open→Finish`。前三个场景也包含账本，因此是组合收益，不能把它们的百分比相加。
- 每请求克隆一次输入，模拟独立 RPC 请求各自拥有图片；没有实际运行 gRPC、完整服务入口、中间件或业务调用链。
- HTTP mock 用 `io.Copy(io.Discard, r.Body)` 完整接收请求，核对 ContentLength 后固定等待 **50 ms**，返回极小固定响应。语义正确性由适配器测试和新增协议测试核对；性能 mock 不把完整请求读进另一个内存缓冲。
- 账本/对象存储 mock 不保留媒体；每次调用验证初始写入和结果更新均完成，HTTP 请求数严格等于预期，无重试。对象上传未模拟真实存储的缓冲和耗时。
- 每组先预热 2 请求，再完成 **32 请求**；并发分别 **1 / 8**，重复 **2 轮**。第二轮交换新旧顺序。合计 **1,024 个测量请求，全部成功**；预热及快照请求不计入每请求 CPU/分配/耗时分母。
- 另做一次等待状态快照：所有请求均已完成输入上传、HTTP mock 已接收完整 body，但模型响应尚未返回；强制 GC，再记录 HeapAlloc。该快照不计入 CPU、分配和自动 GC 次数统计。
- 已按测试用例复用流程检索平台资产：Cursor 的 ModelHub 检索无命中，System Baseline multipart 检索超时；平台业务 Runner 不能表达局部 Reader/GC 对照。本次复用仓库适配器用例和已有图片，补齐可重跑的本地测试，未冒充平台/OOTD 功能回归。

## 两轮均值

容器峰值是 cgroup v2 `memory.peak`，含预热、测量、等待快照；每个组合都启动新容器，因此不会沿用前一次峰值。它包括堆、运行时、网络及容器记账，并不等于 HeapAlloc。

累计分配是测量窗口 `TotalAlloc` 增量÷32，包含真实适配器、账本以及本进程 HTTP mock 的分配。CPU 是本进程 user+system CPU 时间增量÷32（包含 mock），不是 Docker 瞬时 CPU 百分比；50 ms 等待会影响墙钟耗时，不应把它当生产吞吐。

| 场景 | 并发 | 容器峰值 MiB（旧→新） | 峰值下降 | 累计分配 MiB/请求（旧→新） | CPU ms/请求（旧→新） | 自动 GC 次数（旧→新） | p95 ms（旧→新） |
|---|---:|---:|---:|---:|---:|---:|---:|
| 原始 multipart | 1 | 41.83 → 30.46 | 27.2% | 9.34 → 4.73 | 4.392 → 3.773 | 20.0 → 16.0 | 57.71 → 56.88 |
| 原始 multipart | 8 | 167.33 → 93.09 | 44.4% | 9.35 → 4.74 | 1.601 → 1.483 | 7.0 → 6.0 | 70.57 → 65.33 |
| Base64 表单 | 1 | 48.92 → 30.71 | 37.2% | 19.39 → 10.92 | 8.606 → 7.441 | 34.0 → 32.0 | 62.11 → 60.74 |
| Base64 表单 | 8 | 210.83 → 154.71 | 26.6% | 19.42 → 10.94 | 2.907 → 2.933 | 13.0 → 9.0 | 77.05 → 74.32 |
| Base64 JSON | 1 | 57.53 → 31.08 | 46.0% | 16.49 → 10.92 | 11.957 → 10.505 | 27.0 → 32.0 | 65.87 → 65.57 |
| Base64 JSON | 8 | 208.08 → 157.47 | 24.3% | 16.51 → 10.94 | 4.752 → 4.672 | 11.0 → 9.0 | 98.95 → 90.94 |
| 纯账本 | 1 | 31.49 → 21.66 | 31.2% | 6.97 → 4.65 | 2.339 → 1.725 | 18.5 → 16.0 | 60.51 → 59.49 |
| 纯账本 | 8 | 129.02 → 57.00 | 55.8% | 6.97 → 4.65 | 0.708 → 0.807 | 7.0 → 8.0 | 57.53 → 60.21 |

下表仅是“等待状态 HeapAlloc − 空闲 HeapAlloc”的增量，隔离常驻开销后更容易观察图片生命周期。它与容器峰值是不同指标。

| 场景 | 1 并发，旧→新 MiB | 8 并发，旧→新 MiB |
|---|---:|---:|
| 原始 multipart | 6.97 → 2.33 | 55.83 → 18.72 |
| Base64 表单 | 7.75 → 5.42 | 62.12 → 43.53 |
| Base64 JSON | 7.75 → 5.43 | 62.06 → 43.51 |
| 纯账本 | 4.64 → 2.32 | 37.15 → 18.59 |

原始 multipart 在等待状态少持有一份账本快照和一份完整 multipart 图片缓冲，8 请求共减少约 **37.1 MiB**。纯账本少持有一份图片快照，共减少约 **18.6 MiB**。Base64 场景仍保留协议要求的编码字符串，不能按原始 multipart 的比例估算。

每组只有两轮、每轮 32 请求；CPU 存在调度/频率/GC 噪声，不给置信区间或稳定容量结论。完整原始数据见 [media-memory-results.jsonl](media-memory-results.jsonl)，包含每轮 CPU、p50/p95、分配次数、GC 暂停、存活堆和容器峰值。

## 正确性验证

- 新增请求体测试：标准库 JSON 字节对照（HTML 转义、Unicode、RawMessage、嵌套/空值/字节字段）、multipart 重复字段/文件/MIME、ContentLength、GetBody、HTTP 307 重放。
- 新增账本测试：输入与调用方隔离、已克隆输入不再二次复制、输出仍保持复制隔离。
- 新增 gateway 测试：图片 JSON 与原协议一致、nil/空数组、调用方 map 不被修改、GET 空 body。
- 复用相关适配器原有字段、鉴权、重试、取消、错误测试。
- 全量并行运行 `go test ./...`（Go1.27.1 / Go1.26.0）均在既有 shell wrapper 的 `<1s` 信号退出断言失败；该测试在基线和优化分支独立运行均通过。完整串行回归 `GOTOOLCHAIN=go1.26.0 go test -p 1 ./...` **通过**；`GOTOOLCHAIN=go1.26.0 go test -race ./internal/infra/httpbody ./internal/callledger ./internal/infra/vmindgateway` **通过**；对这三个包和 `scripts/media-perf` 的 `go vet` **通过**。没有修改 wrapper 代码或阈值。

## 复现

需要本机 Go 1.26.0、Docker context `colima` 和同一 PNG。`$PERF_ROOT` 指向用于临时文件的绝对路径，`$IMAGE` 指向该 PNG；无需任何厂商密钥。

```sh
# 在优化分支仓库根目录执行。
BASE=f41f14631351a340280e5aca7ed359b003fbc8b7
PERF_ROOT=/absolute/path/to/media-perf-work
IMAGE=/absolute/path/to/cover-1.png
mkdir -p "$PERF_ROOT/docker"
git worktree add --detach "$PERF_ROOT/baseline" "$BASE"
mkdir -p "$PERF_ROOT/baseline/scripts/media-perf"
cp scripts/media-perf/main.go "$PERF_ROOT/baseline/scripts/media-perf/main.go"
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$PERF_ROOT/docker/new" ./scripts/media-perf
(cd "$PERF_ROOT/baseline" && GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$PERF_ROOT/docker/old" ./scripts/media-perf)
cp "$IMAGE" "$PERF_ROOT/docker/fixture.png"
cp scripts/media-perf/Dockerfile "$PERF_ROOT/docker/Dockerfile"
docker --context colima build --build-arg VARIANT=old -t modelhub-media-perf:old "$PERF_ROOT/docker"
docker --context colima build --build-arg VARIANT=new -t modelhub-media-perf:new "$PERF_ROOT/docker"
python3 scripts/media-perf/run.py --output "$PERF_ROOT/results.jsonl"
```

测试容器退出即删除。没有操作本地已有业务容器，没有合并、发布或在线压测。
