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

- [ ] `go build ./... && go test ./...` 绿
- [ ] 导入导出复现工作流；secret 剥离
- [ ] md-per-step 解析正确
- [ ] Web 展示 fan-out/子工作流/重试/事件；CLI workflow events 可用
- [ ] `/metrics` 见 `gofer_workflows_terminal_total` 等
- [ ] **D23 全程向后兼容回归**：v1 工作流端到端零改动跑通
- [ ] git 提交：`feat(workflow-v2): P4 导入导出 + md-per-step + 展示/指标`
- [ ] 回填主纲 §5 实施结果 + 勾选全进度
