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
