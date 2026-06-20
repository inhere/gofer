# Gofer 产出与审计 — 实施计划（总纲）

> 设计依据：[`../../design/2026-06-20-job-outcomes-audit-design.md`](../../design/2026-06-20-job-outcomes-audit-design.md)（v0.1，§10 决策默认全部采纳）。
> bd epic：`example-project-dhk`。本文件只保留**总纲 + 进度跟进 + 阶段简述**（SR1105）；阶段详情见子文档。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-20 | Claude | 初版：P1–P4 拆分 + 全局验收门 + 进度跟进骨架。 |

## 已采纳决策（design §10）

- **D1** 远端范围：**local-first**（P1–P3 本机/共享盘），远端 worker/peer 回传隔离为 **P4**。
- **D2** 产物声明：约定 **`<result_dir>/artifacts/`**（无需 agent 协议改动）。
- **D3** 结构化结果：约定 **`<result_dir>/result.json`**，入库上限 **256KB**。
- **D4** diff 基线：v1 用 `git diff`（工作树 vs HEAD/index，即未提交改动）；"开始打基线 ref" 留 v2。
- **D5** diff 开关：cwd 是 git 仓**默认开**、项目级 `capture_diff:false` 可关；子进程 ctx **超时 5s**、`--stat` 摘要入库 **32KB** 截断、全量写 `<result_dir>/changes.diff` 文件。
- **D6** 远端大产物（P4）：v1 仅回 **清单 + 小结果（result.json/diff 摘要/渲染命令）**；大文件留 worker 侧（标注 `source`）或共享盘直读，专用拉取通道留后续。
- **D7** MCP：顺带加 `bridge_get_artifacts` / `bridge_get_result` 读 tool（为 E7 工作流铺垫）。

## 范围与分期

| 阶段 | 子文档 | 内容 | 依赖 | 风险 |
|---|---|---|---|---|
| **P1** | [`P1-foundation-cmd-result-plan.md`](./P1-foundation-cmd-result-plan.md) | schema 加列 + `captureOutcomes` 钩子 + **E15 渲染命令** + **E6 结构化结果**（local） | 无 | 低 |
| **P2** | [`P2-artifacts-plan.md`](./P2-artifacts-plan.md) | **E1 产物**（清单入库 + list/下载 API + Web，local） | P1 | 低 |
| **P3** | [`P3-diff-plan.md`](./P3-diff-plan.md) | **E12 diff 快照**（git diff at finish + `/diff` API + Web，按项目开关） | P1 | 中 |
| **P4** | [`P4-remote-capture-plan.md`](./P4-remote-capture-plan.md) | **远端捕获回传**（worker WS `Outcome` 帧 + peer SSE `outcome` 事件） | P1–P3 | 中 |

**顺序**：P1 → P2 → P3 → P4。每阶段绿灯即 Git 提交（SR1202）。

## 进度跟进

- [ ] **P1-a** schema 加 4 列（`rendered_command`/`result_json`/`artifacts_json`/`diff_summary`）+ `JobResult/JobRecord/toRecord/fromRecord/selectCols/Upsert` 贯通
- [ ] **P1-b** `captureOutcomes` 钩子（execute 内 run.Run 后、finish 前，best-effort）
- [ ] **P1-c** E15 渲染命令（local 捕获 runReq.Command/Args + env keys）→ get_job 回 + Web 面板
- [ ] **P1-d** E6 结构化结果（读 `<result_dir>/result.json`，256KB 上限）→ get_job 回 + Web 面板
- [ ] **P2-a** E1 产物清单（扫 `<result_dir>/artifacts/`）+ `GET /v1/jobs/{id}/artifacts`
- [ ] **P2-b** 产物下载 `GET /v1/jobs/{id}/artifacts/{name}`（`safeJoinUnder` 路径校验）+ Web 下载
- [ ] **P2-c** MCP `bridge_get_artifacts` / `bridge_get_result`（D7）
- [ ] **P3-a** git diff 封装（探仓/超时/截断/降级）+ `captureDiff` 接入 + 项目 `capture_diff` 开关
- [ ] **P3-b** `GET /v1/jobs/{id}/diff[?full=1]` + Web diff 面板
- [ ] **P4-a** worker：worker 侧 captureOutcomes + `Outcome` WS 帧回传 + host sink 落字段
- [ ] **P4-b** peer-http：SSE `outcome` 事件 + handleFrame 分支 + 产物下载代理

## 全局验收门（每阶段收尾必过）

```bash
cd tools/gofer
go build ./... && go vet ./... && go test ./... && gofmt -l internal/ cmd/   # 后端
pnpm -C web build                                                            # 含前端阶段
```

- **回归**：现有 job 提交/日志/SSE/交互/worker 派发全绿；新字段全 `omitempty`，旧库经 `migrate()` 自动补列、旧 job 读出新字段为空。
- **真机冒烟**：local 跑一个写 `artifacts/`+`result.json`、并改动 git 仓的 exec job，详情页四面板（渲染命令/结构化结果/产物下载/diff）齐全；P4 再起 worker 验远端回传。

## 安全要点（贯穿）

- 产物下载 `name` 强校验（`safeJoinUnder(<result_dir>/artifacts, name)`：Clean + 限定前缀 + 拒 `..`/绝对/软链逃逸）——本计划最大对外新面。
- 渲染命令 **env 只存 key 名、不存值**；result.json/diff 不回显 secret（agent 责任 + 文档提示）。
- diff 子进程 ctx 超时 + 输出上限；全量 diff/产物随 retention 清理。

## 结论

四项共用 `captureOutcomes`（execute 内统一点）+ jobs 加 4 列 + 详情 API/Web 面板，分 P1→P4，local-first 先见效、远端 P4 隔离。最大复用 finish 统一终态、result_dir 写文件、`migrate()` 加列、`request_json` 大 JSON 入列。本总纲随阶段更新进度与「阶段实施结果」。
