# 统一 Gateway 整合检查

本轮保留 PR #30 的协议转换与模型路由，不新增通用 RPC、不重复在 ModelHub 定义 Gateway 的业务参数范围。图片继续 `Generate`，视频继续 `SubmitGeneration` / `GetGeneration`；产品/算法通用生成使用 Gateway 的 Images API。

- 业务图片继续由 vworldimage 把 protobuf 图片和 model_traits 转为统一 input。体模性别、试衣 prompt、商品名等业务约束由 Gateway 数据库 Schema 检查；ModelHub 仍校验输入结构并归一化上游错误。
- 保留现有图片顺序、原始参数值、视频 motion_tips、错误码映射及按块下载，不增加重试、PE 或业务质量判断。
- #30 的同环境 `vision_gateway` 引用、模型绑定和 Gateway 地址必须在发布前完成配置校验。连接资源变化按既有规则滚动重启。
- 先部署完成 Schema 迁移的整合 Gateway，再切换 Hub。dev 分支 push 会触发部署；候选 PR 审查与生产发布应按此顺序安排。

新增 `internal/infra/vworldimage/gateway_contract_test.go` 由 Gateway 的 `TestModelHubAgainstUnifiedGateway` 启动真实 HTTP/数据库后调用。它验证非法 gender 和空试衣 prompt 到达真实 Gateway 后归一化为非本地 INVALID_ARGUMENT，且未创建生成任务。运行 Gateway 跨仓库检查时将 `MODELHUB_CONTRACT_DIR` 指向此检出；不要只运行缺环境变量而 Skip 的 Hub 测试并宣称联调通过。

本仓回归命令：

```sh
go test -race -p 1 ./config ./internal/infra/vmindgateway ./internal/infra/vworldimage ./internal/infra/ltx ./internal/infra/factory ./internal/service/modelhub
```

完整合并、发布及回退步骤见 modelGateway 的 `docs/INTEGRATION_ROLLOUT.md`。

## 视觉配置迁移门槛（2026-11-17）

生产 AppConfig 中五个视觉 provider 已有 `vision_gateway`，但还保留原生连接字段；候选进程会拒绝启动。迁移时移除 `human_parser`、`human_yolo`、`facesim`、`lens_distortion_correction` 内的连接配置，保留空对象 `{}`；`segment_person` 只保留 `method` / `person_method` / `subject_method`。本地 `schema/appconfig.schema.json` 已同步此规则，并要求五类适配器声明非空 `vision_gateway`。跨 provider 引用有效性仍由 `ParseAndValidateYAML` 检查。仓库 Schema 不会自动绑定或更新云端 AppConfig Validator。

另外，`human-parser.model.wgdl.tech` 对应国内 **L20-1** 的 `https://human-parser.model.wgdl.tech/human_parser`（用户确认），不是 AWS G6 解析服务。保留 `human-parser` 模型键及原服务语义，由同环境 Gateway 管理实际后端地址、鉴权和健康检查；不得重写为 `Vmind_human_parse_v1.0`。当前 Gateway 的人体解析实现仅支持 G6 前置服务 `/_dispatch/{port}/human-parser/human_parser/predict`，因此国内服务还需原生转发支持及真实链路验收，不能只添加一条实例记录就宣称迁移完成。示例中的 Gateway 引用是目标配置，不是已上线证据。

发布前先验证国内解析的 Gateway 转发和独立模型登记，再用目标环境完整配置运行 `ParseAndValidateYAML`，最后发布 Hub 并验证两个解析模型分别到达原目标。连接配置变化必须滚动重启；回退同时恢复旧镜像和旧配置。已完成的图像/视频生成 GPU 验收不覆盖国内解析。
