# Gofer CLI 日常易用 + job 标签检索 — 设计方案

> 一句话：补齐 gofer **日常 CLI 摩擦**（`job list`/`watch`/`rerun` + 配置脚手架/校验 + shell 补全）并给 job 加 **tags + 多维检索**（tag/agent/runner/时间），让"跑了一堆 job 之后"也用得顺手、找得到。
> 合并 roadmap [`../2026-06-20-enhancements-roadmap.md`](../2026-06-20-enhancements-roadmap.md) 的 **E2 CLI 日常补全 + E3 引导/配置校验 + E5 job 标签+搜索**（B1+E5 批次，强内聚：共用 CLI 命令层 / `internal/client` / job 列表查询面 / jobs schema 加列模板）。承接已完成的「产出与审计」epic（E1/E6/E12/E15，`example-project-dhk`）。bd epic 待建。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-20 | Claude | 初版：范围/分期 + E2/E3/E5 模块详设 + §10 决策点。 |

## 1. 概览

### 1.1 背景与缺口（事实，带 file:line）
「产出与审计」epic 补齐了 job **产出可见**，但**日常操作**仍单薄：
- **CLI 缺常用动作**：`internal/commands/job.go` 仅 `run/show/logs/cancel`，**无 `list`**（只能开 Web 或 curl）、**无 `watch`**（实时跟 job 进展）、**无 `rerun`**（复跑一个历史 job）；无 shell 补全。
- **客户端缺方法**：`internal/client/client.go` 有 Submit/Get/GetLogs/Cancel/OpenStream，**无 `ListJobs`**、**无 SSE 帧消费助手**（`OpenStream` 只返回裸 `*http.Response`，帧解析现仅在 web `sse.ts`）。
- **rerun 取不到原始请求**：`JobResult.RequestJSON` 入库（`jobs.request_json`）但 tag 是 `json:"-"`（`model.go:81`），**API 不暴露** → 无从复跑。
- **上手门槛**：无 `gofer init` 脚手架；全局校验仅 `validate(cfg)` 查 `host_path`（`loader.go:191`），细校验只有 `project validate <key>`；example `config/gofer.example.yaml` **缺 `callers`/`workers`/`runner_probe`/`retention`/worker-runner 段**，copy 不能即用。
- **检索维度少**：`ListQuery` 仅 `project`/`status`/`caller`+`limit`（`jobstore/jobs.go:65`），`handleListJobs` 只映射 `project`/`status`/`caller`/`limit`；Web `Board.vue` 仅 status+project 过滤。job 一多就**找不到**；**无 tags 概念**。

### 1.2 目标
| 编号 | 目标 | 轴 |
|---|---|---|
| G-E2 | `job list` / `watch <id>` / `rerun <id>` + shell 补全 | 易用 |
| G-E3 | `gofer init` 脚手架 + `gofer config validate` + example 补全 | 易用 / 上手 |
| G-E5 | job 提交带 `tags`，列表/看板按 **tag/agent/runner/时间** 检索 | 易用 / 组织 |

### 1.3 非目标
- **不做** 工作流编排（E7）/ 通知外发（E14）/ 事件时间线（E13）/ 模板库（E4）——各自独立。
- `rerun` v1 仅"按原请求复跑"，**不做** 参数编辑/批量重跑。
- 补全 v1 仅静态子命令/选项补全，**不做** 动态 `<id>`/`<project>` 候选补全。
- `gofer init` v1 写注释化 starter 配置，**不做** 全交互式向导。

## 2. 名词
- **tag**：提交 job 时附带的自由文本标签（如 `nightly`/`refactor`/`exp-a`），用于事后分组检索。多值。
- **rerun**：以某历史 job 的**原始 JobRequest** 重新提交一个**新** job（新 id、不带旧幂等 key）。
- **watch**：CLI 端消费 job SSE 流（`/v1/jobs/{id}/stream`），实时打印状态/日志直至终态。
- **starter 配置**：`gofer init` 生成的、含注释与各段占位的可即改即用 YAML。

## 3. 范围与分期

| 阶段 | 内容 | 依赖 | 风险 |
|---|---|---|---|
| **P1** | **E5 tags**（schema 加 `tags_json` 列 + `JobRequest.Tags` + submit 贯通 + 检索维度 tag/agent/runner/since 后端 + Web Board 过滤器） | 无 | 低 |
| **P2** | **E2 CLI**（`client.ListJobs`/`StreamJob` 助手 + `job list`/`watch`/`rerun` + `GET /v1/jobs/{id}/request` 端点 + 补全） | P1（list 含 tag 过滤/列） | 低-中 |
| **P3** | **E3**（`gofer init` + `gofer config validate` + `config/gofer.example.yaml` 补全 callers/workers/runner_probe/retention/worker-runner） | 无（可与 P1/P2 并行，置末以免干扰） | 低 |

> **顺序**：P1 → P2 → P3。P1 打 tags 地基（让 P2 的 `job list` 直接带 tag 列/过滤）。每阶段绿灯即提交（SR1202）。local-only，无远端面新增。

## 4. 架构与关键改动

三块各自收敛、共用既有面：

```txt
E5 tags     : JobRequest.Tags ─▶ jobs.tags_json(加列,5处贯通) ─▶ ListQuery WHERE(tag/agent/runner/since)
                                                              ├─▶ GET /v1/jobs?tag=&agent=&runner=&since=
                                                              └─▶ Web Board 过滤器 + 列表 tag 徽标

E2 CLI      : internal/client +ListJobs(opts) +StreamJob(ctx,id,from,onEvent[SSE帧])
              internal/commands/job.go +list(调 ListJobs) +watch(调 StreamJob) +rerun(GET /request→改 id/清幂等→SubmitJobSync)
              新端点 GET /v1/jobs/{id}/request ─▶ 回原始 JobRequest(rerun 取数)
              commands/app.go +completion(gcli 内置生成)

E3 引导     : commands +init(写 starter yaml) +config validate(全局 load+validate+逐项目 reg.Validate)
              config/gofer.example.yaml 补 callers/workers/runner_probe/retention/worker-runner 段
```

**改动面**：
- 低：E5 加 1 列（`tags_json`）走既有 5 处贯通模板（`store.go` DDL+migrate / `jobs.go` selectCols·scanJob·UpsertJob / `model.go` JobResult·JobRequest / `service.go` toRecord·fromRecord）；ListQuery/handler 加 4 个可选过滤；Board 加过滤器。
- 低-中：E2 client 加 2 方法（ListJobs + SSE 消费助手，**SSE 帧解析参考 `runner/peerhttp` 既有 Go 端消费**，不重造）；3 个子命令；1 个只读端点；补全接线。
- 低：E3 两个命令 + example 文本补全（无逻辑风险）。

## 5. 模块详设

### 5.1 E5 tags + 多维检索（P1）
**数据**：`JobRequest` 加 `Tags []string`（`json/yaml:"tags,omitempty"`，**入库**——区别于 `WorkerLabels` 的不入库 `model.go:27`）。jobs 表加列 `tags_json TEXT`（JSON 数组，与 `artifacts_json` 同形态先例）。贯通五处照搬 source 列模板（recon 已列 `store.go`/`jobs.go`/`model.go`/`service.go`）。submit 时 `tags_json = json.Marshal(req.Tags)`。
**检索**：`ListQuery` + `ListOpts` 加 `Tag string`（单 tag 过滤 v1）、`Agent string`、`Runner string`、`Since int64`（agent/runner/started_at 都是既有列，纯加 WHERE）。SQL：tag 过滤用 `tags_json LIKE ?`（`%"<tag>"%`，cheap，v1 接受子串近似，§10-D2 注）；agent/runner exact、since `started_at>=`。`handleListJobs` 加 query 映射 `tag`/`agent`/`runner`/`since`。
**Web**：`Board.vue` 过滤器加 tag（输入）/agent（下拉）/runner（下拉）/since（时间），并把后端已支持的 caller 也补上 UI；列表行渲染 tag 徽标。`api/types.ts` `ListJobsOpts` 加 `tag/agent/runner/since`、`Job` 加 `tags?: string[]`；`client.ts listJobs` 加参数。
**验收**：提交带 tags 的 job→`GET /v1/jobs?tag=x` 命中、`?agent=&runner=&since=` 生效；旧 job `tags` 为空不报错；Board 过滤可用。

### 5.2 E2 CLI（P2）
**client 新方法**（`internal/client/client.go`）：
- `ListJobs(opts ListOpts) ([]JobResult, error)` → `GET /v1/jobs?project=&status=&caller=&tag=&agent=&runner=&since=&limit=`。
- `StreamJob(ctx, id string, from int, onEvent func(SSEEvent)) error` → 封装 `OpenStream` + **Go 端 SSE 帧解析**（按 `\n\n` 切帧、`event:`/`data:` 行；事件类型 status/log/log-rotated/interaction/end，见 `stream_handler.go`）。**复用 `runner/peerhttp` 既有 SSE 消费逻辑**——抽到 client 共享助手，peerhttp 可后续切换（不强制本期重构）。
**子命令**（`internal/commands/job.go`，仿现有 gcli.Command 形）：
- `job list`：选项 `--project/--status/--caller/--tag/--agent/--runner/--since/--limit`，调 `ListJobs`，表格输出（id/status/agent/runner/project/tags/started）。
- `job watch <id>`：调 `StreamJob`，实时打印状态变更 + 增量日志，终态退出（exit code 映射 job 终态）；`--from` 续传。
- `job rerun <id>`：`GET /v1/jobs/{id}/request` 取原 `JobRequest` → **清 `RequestID`（幂等 key，否则会被去重命中原 job）**、按需 `--watch` → `SubmitJobSync`，打印新 job id。
**新端点**（只读）：`GET /v1/jobs/{id}/request`（`job_handler.go`）→ 取 job 的 `RequestJSON` 原文回（404 if !ok / 空）。**不**改 `get_job` 主响应（避免 list 包体膨胀，§10-D1）。
**补全**：`commands/app.go` 接 gcli 内置补全生成（`gofer completion bash|zsh`）。
> **现状更新（2026-06-25）**：gcli v3.8 升级后（`6e36b8b` 更新 deps，测试适配 `bbd8a2d`），completion 从子命令改为内置 **`--gen-completion` 全局 flag**，现用 `gofer --gen-completion bash|zsh`（README 已对齐）。本段保留当时记录。
**验收**：`job list` 各过滤生效、表格正确；`job watch` 实时跟到终态、exit code 对；`job rerun` 起新 job（新 id、无旧幂等冲突）；`completion` 脚本可 source。

### 5.3 E3 引导 / 校验 / example（P3）
- `gofer init [--config <path>] [--force]`：写一份**注释化 starter** 配置（默认 `./.gofer.yaml`，存在则需 `--force`），内容取自补全后的 example 模板。
- `gofer config validate [--config]`：全局 `Load` + `validate(cfg)` + **遍历所有 project 调 `reg.Validate(key)`**（路径存在/agent 可用/runner 存在），逐条 `[OK/FAIL]` 输出，有 FAIL 则非零退出。复用 `project validate` 的 `reg.Validate` 逻辑，不重造。
- `config/gofer.example.yaml` 补：`server.callers`（多 caller C2）、`server.workers`（ws-worker 注册）、`server.runner_probe`、`storage.retention`/`storage.db_path`、`runners` 加 `type: worker` 样例。每段带注释说明。
**验收**：`gofer init` 生成可被 `config validate` 通过的 starter；`config validate` 对坏配置报 FAIL 非零退出；example copy 后即可 `serve`（关键段齐全）。

## 6. 数据模型
`jobs` 表加 1 列（`migrate()` additive，模板同 source 列）：
```sql
ALTER TABLE jobs ADD COLUMN tags_json TEXT;  -- JSON 数组 ["a","b"]（E5）；旧 job COALESCE 空
```
- `JobResult` 加 `Tags []string json:"tags,omitempty"`（fromRecord 时 `json.Unmarshal(tags_json)`）；`JobRequest` 加 `Tags []string`。
- tag 过滤 v1 用 `tags_json LIKE '%"tag"%'`，不建额外索引（job 量级 + LIKE 足够；如需精确再上 normalize 表，留 v2）。

## 7. API
| 方法 | 路径 | 变更 | 说明 |
|---|---|---|---|
| GET | `/v1/jobs` | 改 | query 加 `tag`/`agent`/`runner`/`since`（皆可选，omit 即不过滤） |
| GET | `/v1/jobs/{id}/request` | 新 | 回该 job 原始 `JobRequest`（rerun 取数；只读，鉴权同 `/v1`） |
| GET | `/v1/jobs/{id}` | 不变 | 响应**不**加 request_json（避免膨胀，rerun 走专门端点） |

## 8. 安全
- `GET /v1/jobs/{id}/request` 回原始请求，**含 cmd/prompt** —— 仍在 `/v1` 鉴权 + caller token 内；request 里**不含 secret**（secret 走 agent env 配置、不入 request，SR403）。
- `gofer init` 不写任何 token 明文，仅写 `token_env` 占位（引导用户走环境变量）。
- tags 为自由文本，仅作检索；`LIKE` 过滤参数走预编译占位符（防注入）。

## 9. 部署
无新部署面：纯 CLI + 既有 HTTP + 一列 additive 迁移 + Web 前端。retention 一并清理（tags 随 job 行）。

## 10. 待确认事项（决策点，附推荐）
- **D1（rerun 取数）**：新增只读端点 `GET /v1/jobs/{id}/request`（推荐，list 响应不膨胀）；还是把 `request_json` 暴露进 `get_job`？（推荐：专门端点）
- **D2（tags 存储/过滤）**：`tags_json` JSON 列 + `LIKE '%"tag"%'` 单 tag 过滤（推荐，cheap，子串近似可接受）；还是 normalize `job_tags` 关联表做精确多 tag AND/OR（留 v2）？
- **D3（检索维度）**：E5 后端/Board 过滤加 **tag+agent+runner+since**，并补 caller 的 Web UI（后端已支持）——认可这套维度？（推荐：是）
- **D4（watch SSE 消费）**：抽 `client.StreamJob` 共享助手供 CLI watch 用，`runner/peerhttp` **本期不强制重构**（留后续统一）；可否？（推荐：是）
- **D5（rerun 幂等）**：rerun 复跑时**清空 `RequestID`**（否则命中原 job 的幂等唯一索引被去重）——确认这是期望语义？（推荐：是，rerun=新 job）
- **D6（init 落点）**：`gofer init` 默认写 `./.gofer.yaml`（config 加载链优先项）、存在则需 `--force`；非交互式注释模板（交互式向导留后续）。认可？（推荐：是）
- **D7（bd 拆分）**：建 epic `gofer CLI 易用+tags` + 子任务 P1/P2/P3；还是各 E2/E3/E5 独立 issue？（推荐：一个 epic 三阶段，对齐本设计）

## 11. 结论
E2/E3/E5 共用 **CLI 命令层 + `internal/client` + job 列表查询面 + jobs 加列模板**，强内聚合一份方案分 P1（tags 地基）→P2（CLI 动作）→P3（引导/校验/example）。最大复用：加列 5 处贯通模板、`reg.Validate`/`validate(cfg)` 既有校验、`runner/peerhttp` 既有 Go 端 SSE 消费、`Board.vue` 既有过滤器骨架、gcli 内置补全。最大新面是只读 `/request` 端点（鉴权内、无 secret）。

**下一步**：审核（重点过 §10 决策）→ 通过后出分阶段 `plan`（P1–P3，细到列/方法/handler/命令/Web 与验收），再按 SUPMODE 实施。
