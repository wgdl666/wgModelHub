# 美东生成接口分批整合

本批从当前 dev 拆出生成适配，保留生产配置及视觉/对话行为。与全量统一入口草稿的区别是：不删除 human_parser 等适配器的直连配置，不强制迁移国内 human-parser，不改变对话/决策调用地址。

## 改动

图片 Generate 的 protobuf 保持不变。ModelHub 读取 Gateway 已登记协议，再将现有调用方输入转换为规范 JSON：

| 协议 | 转换 |
|---|---|
| body_model | 一张人物图，model_traits.gender → gender |
| product_generation | 一张商品图，文本 JSON 中 garment_name、garment_role 展开 |
| garment_extraction | 人物图及 bbox，或人物图 + mask + parse_labels；按已有文本 JSON 展开 |
| vton | 两张图按原顺序提交，文本 → prompt，保留原提示词空白 |

ModelHub 负责协议结构、媒体转换及错误归一化；参数范围和模型权限仍由 Gateway 的模型 Schema 校验，真实图片/运行能力由模型服务校验。业务 Prompt、业务重试和业务任务终态不进入 ModelHub。

视频仍走 SubmitGeneration / GetGeneration；不改变现有持久化、请求幂等和 S3 结果流。产品/算法的通用生成使用 Gateway 的 OpenAI-compatible Images API，不新增一套 protobuf 或业务流程。

## 发布约束

先完成生产 ModelHub 基线验收，再发布保留旧图片信封与视觉/对话入口的 Gateway，最后滚动发布本候选。既有 AppConfig（包括国内 human-parser 直连）应通过本版本 ParseAndValidateYAML；无需套用全量整合草稿的视觉配置迁移。

G7e GPU2/3 已更新的是 FLUX 服务中调用 PE 的代码和提示词配置；实际 PE 推理仍在 GPU0。本批不迁移 PE，也不自动替换 Qwen/Video。

## 验证

- vworldimage：规范字段映射、错误输入、动态模型协议和结果读取。
- factory/service：模型路由热更新、能力不匹配和原视觉传输行为。
- Gateway 跨仓库：非法业务输入不入账；旧/新 Hub 输入在滚动期间可重放同一请求。
- 生产门槛：真实 Hub → Gateway → 模型 → 生产 S3 → Hub 媒体返回，并核对 PE applied。网络连通失败或仅构建成功均不能记为生产验收通过。

2026-11-17 本机验证：服务、配置及适配器 race 测试通过；真实生产 AppConfig 结构（以占位值模拟运行时注入 Token，不出站）通过；Gateway 跨仓库测试、go vet、go mod verify 通过。全量 macOS 测试中，未修改的 examples/gpt-image-2 信号退出测试超过 1 秒断言，正式 Linux Dockerfile 构建需再核验；不能将本机全量测试记为通过。
