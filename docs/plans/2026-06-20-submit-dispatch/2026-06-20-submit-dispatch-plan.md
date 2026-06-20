# Gofer 提交与调度增强 — 实施计划（总纲）

> 设计依据：[`../../design/2026-06-20-submit-dispatch-design.md`](../../design/2026-06-20-submit-dispatch-design.md)（v0.1，§10 决策默认全部采纳）。
> bd epic：`hyy-ai-inspect-myi`。本文件只保留**总纲 + 进度跟进 + 阶段简述**（SR1105）；阶段详情见子文档。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-20 | Claude | 初版：P1/P2/P3 拆分 + 全局验收门 + 进度跟进骨架。 |

## 已采纳决策（design §10）

- **D1** 同步：`POST /v1/jobs` 加 `sync`（body）/`?wait=1`；默认等待 30s、硬上限 60s；超时返 `202`+id（`X-Gofer-Async:1`）；对**任意 agent** 开放。
- **D2** md：按 `Content-Type: text/markdown` 分支；frontmatter→`JobRequest`，正文→`prompt`，**仅 cli-agent**（md 指定 `agent=exec` 报 400）。
- **D3** 调度：`worker_labels` 全包含 AND；候选排序 `in_flight↑ → heartbeat_age↑`；无合格候选 `503`；`worker_id` 与 `labels` 同给则 worker_id 胜。
- **D4** 兼容：worker runner 保留 `rc.WorkerID` 作兜底默认（未给 worker_id 且无 labels 时回落）。
- **D5** 表单：首版覆盖 cli-agent + exec 主路径，labels 选机为可选高级项。

## 范围与分期

| 阶段 | 子文档 | 内容 | 依赖 | 风险 |
|---|---|---|---|---|
| **P1** | [`P1-sync-md-plan.md`](./P1-sync-md-plan.md) | exec/任意 job 同步等待 + md+yaml 提交 | 无 | 低 |
| **P2** | [`P2-label-dispatch-plan.md`](./P2-label-dispatch-plan.md) | 按 worker labels 自动调度（含 worker runner 动态路由重构） | P1 字段 | 中 |
| **P3** | [`P3-web-form-plan.md`](./P3-web-form-plan.md) | 控制台提交表单 + `GET /v1/meta` | P1+P2 | 低 |

**顺序**：P1 → P2 → P3。每阶段绿灯即 Git 提交（SR1202）；P1 两项独立可并行。

## 进度跟进

- [x] **P1-a** exec/任意 job 同步等待（`Service.WaitFor` + handler sync 分支 + CLI `--sync`）— commit `730b6bb`
- [x] **P1-b** md+yaml 提交（content-type 分支 + `mdreq.go` 解析 + `JobRequest` 加 yaml tag + CLI `-f`）— commit `730b6bb`
- [ ] **P2-a** `JobRequest.WorkerLabels` 字段 + `Forward.WorkerID` 动态路由（worker runner 解绑，保 rc.WorkerID 兜底）
- [ ] **P2-b** Submit 内 labels 选机（hub 注册表快照过滤+排序）+ 无候选 503
- [ ] **P3-a** `GET /v1/meta` 表单选项聚合接口
- [ ] **P3-b** web 提交表单视图（消费 sync/labels，复用浅色主题/状态色板）

### 阶段实施结果

- **P1 ✅（commit `730b6bb`）**：同步等待复用 `Service.Wait` 加超时封顶（默认 30s/顶 60s，超时 202+`X-Gofer-Async`，不杀 job）；md 提交按 `Content-Type` 分支 + `mdreq.go` frontmatter 解析（exec 报 400）；client 加 `SubmitJobSync`/`SubmitMarkdown`（`SubmitJob` 签名不变），CLI `--sync`/`-f`。全量 `go build/vet/test/gofmt` 绿。自主决策：P1-a/P1-b 合一 commit（三文件交错增改）；`IsTerminal` 复用现有导出；wait 字段以 plan 的 `WaitTimeoutSec` 为准。还原了一处子代理越界的 README 措辞改动。

## 全局验收门（每阶段收尾必过）

```bash
cd tools/gofer
go build ./...                 # 编译
go vet ./...                   # 静态检查
go test ./...                  # 全量单测（含新增）
gofmt -l internal/ cmd/        # 输出为空（格式）
pnpm -C web build              # 仅 P3：前端 vue-tsc + 构建绿
```

- 真机冒烟（P1/P2）：起 `gofer serve` + 一个本地 worker，按各子文档「验收」节手验同步/md/labels 路径。
- 不破坏现状：现有 JSON 异步提交、显式 worker_id 路由、`--wait` 客户端轮询全部回归通过。

## 风险与回退

- **P2 worker runner 解绑**是唯一较重重构：`Forward` 加 `WorkerID`、worker runner 由 `r.workerID` 改读 `f.WorkerID`（回落 `r.workerID`）。回退点清晰（保留兜底即不改变现网行为）。各阶段独立提交，必要时可只上 P1。
- 同步等待**不改 job 生命周期**（超时不杀 job），与业务 `timeout_sec` 正交，避免引入新状态机分支。

## 结论

按 P1→P2→P3 推进，最大复用 `Service.Wait`/`goccy/go-yaml`/`/v1/runners` 注册表。本总纲随阶段完成更新进度勾选与「阶段实施结果」简述；详情留各子文档。
