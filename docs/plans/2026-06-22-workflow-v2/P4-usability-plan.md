# P4 — 导入导出 + md-per-step + 展示/指标（实施细化）

> 主纲：[`2026-06-22-workflow-v2-plan.md`](2026-06-22-workflow-v2-plan.md) · 设计 §5/§9（E18 + E7 尾巴）。依赖 P3。
> 目标：让 v2 能力顺手用、看得见。

## T4.1 工作流导入导出（E18）

**改动**：`spec_json` 已存，近现成。
- **导出**：`GET /v1/workflows/{id}/export` 回 WorkflowSpec JSON（从 `spec_json`）；CLI `workflow export <id> [-o file]`。**secret 剥离**：导出前扫 prompt/cmd/env，若命中 secret 模式（或显式标记字段）剥离/占位（SR403）。
- **导入**：`workflow run <file.json>`（复用现有 `parseWorkflowFile`，扩 json 分支）或 `POST /v1/workflows`（已支持 JSON body，本就是导入）。
- 命名保存（工作流模板雏形，接 E4）：可选 `workflow save <id> --name X` 存本地模板目录，`workflow run --template X`。

**验收**：export 一个工作流→import 复现跑通；secret 字段被剥离；json/yaml 双格式。

## T4.2 md-per-step 提交（E7 尾巴）

**改动**：扩提交格式——每 step 可用 md+yaml（frontmatter 定 step 参数、正文即 prompt），复用 E14 的 md 解析（`Content-Type: text/markdown` 分支）。工作流文件支持 `steps:` 引用外部 md 文件，或多文档 yaml。

**验收**：md-per-step 工作流文件解析为 WorkflowSpec 正确；正文→prompt。

## T4.3 CLI/Web 展示 + workflow 指标

**改动**：
- **Web**（`web/` 前端 + `workflow_handler.go` 详情）：workflow 详情展示 v2 新维度——fan-out（同 step N 个并行 job 横向展示）、子工作流（嵌套展开/链入子 wf 详情）、重试（step 的 attempt 历史）、`workflow_events` 时间线（P1 已加 API）。
- **CLI**（`commands/workflow.go`）：`workflow show` 输出含 fan/attempt/子 wf；`workflow events <id>` 新子命令。
- **workflow 指标**（E16 延伸，`internal/metrics` + `job.MetricsSink`）：`gofer_workflows_terminal_total{status}` + `gofer_workflow_duration_seconds` + `gofer_workflow_steps`（分布）。挂点：setWorkflowDone/Failed。

**验收**：Web 详情正确显示 fan-out/嵌套/重试 attempt/事件时间线；`workflow events` CLI 可用；`/metrics` 见 workflow 指标族。

## T4.4 验收清单（全绿即收尾）

- [x] `go build ./... && go test ./...` 绿（全量无过滤通过；job/metrics/commands/client 另跑 -race 通过）
- [x] 导入导出复现工作流；secret 剥离（client 集成测试 export→re-import 跑通；job 层 secret 剥离 + 递归子 wf 测试）
- [x] md-per-step 解析正确（commands 层 frontmatter→参数 + 正文→prompt + 内联覆盖 + JSON 导入测试）
- [x] CLI workflow events / export 可用（client+httpapi 集成测试 + 子命令注册测试）；Web 展示 fan-out/子工作流/重试/事件（**仅 vue-tsc+vite build 验证编译过，展示逻辑未目视——需人工眼检**）
- [x] `/metrics` 见 `gofer_workflows_terminal_total` 等（metrics 包 scrape 断言 + job 层埋点 Inc 测试）
- [x] **D23 全程向后兼容回归**：v1 工作流端到端零改动跑通（既有 workflow 测试全通过；新增 File 字段 json:"-" 不入序列化）
- [ ] git 提交：`feat(workflow-v2): P4 导入导出 + md-per-step + 展示/指标`（按指示本轮不提交）
- [ ] 回填主纲 §5 实施结果 + 勾选全进度（待提交时一并回填）
