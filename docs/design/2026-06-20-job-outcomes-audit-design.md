# Gofer 产出与审计 — 设计方案

> 一句话：job 跑完后，把它**产了什么（产物文件）/ 返回了什么（结构化结果）/ 改了什么（diff）/ 实际跑了什么（渲染命令）** 统一捕获、入库、在详情（API + Web）暴露——让"会产文件/产结构化输出的 agent"真正可用，且 agent 的工作**看得见、审得清**。
> 合并 [`../2026-06-20-enhancements-roadmap.md`](../2026-06-20-enhancements-roadmap.md) 的 **E1 产物回取 + E6 结构化结果 + E12 改了什么(diff) + E15 渲染命令**（强内聚，共用同一捕获钩子/schema/详情面）。E13 事件时间线机制不同，作相邻项不并入。bd epic `hyy-ai-inspect-dhk`。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-20 | Claude | 初版：范围/分期 + 统一捕获钩子 + 数据模型/API/Web + §10 决策点。 |

## 1. 概览

### 1.1 背景与缺口
现状一个 job 跑完**只回日志**（`stdout.log`/`stderr.log`）。三个洞：① agent 产的**文件**（构建物/生成代码/报告）取不回；② agent **改了哪些文件**完全看不见（代码类 agent 审计盲区）；③ 结果**非结构化**（只有 exit_code + stdout），"总结失败用例"返回裸文本无法程序化消费；④ "实际跑了什么命令"未直观展示。

### 1.2 目标
| 编号 | 目标 | 轴 |
|---|---|---|
| G-E1 | job **产物文件**可列举 + 下载（API + Web） | 易用 / 用好 agent |
| G-E6 | agent 可返回**结构化结果**（约定 `result.json`），入库 + 展示 | 用好 agent / 审计 |
| G-E12 | 捕获 job 对项目目录的 **git 改动 diff**，详情展示"改了什么" | 审计 |
| G-E15 | 详情展示 job **实际渲染/执行的命令** argv | 审计 |

### 1.3 非目标
- **不做** E13 事件时间线（生命周期 append 事件流，机制不同，相邻独立）。
- **不做** 工作流编排（E7）/ 通知外发（E14）/ 配额（E17）。
- diff **不做**任意非 git 项目的全文件快照对比（仅 git 仓 `git diff`，非 git 优雅跳过）。

## 2. 名词
- **产物 (artifact)**：job 执行产生、需被取回的文件。约定落在 `<result_dir>/artifacts/`。
- **结构化结果 (structured result)**：agent 写到 `<result_dir>/result.json` 的机器可读结果。
- **diff 快照**：job 终态时对其 cwd（git 仓）执行 `git diff` 得到的改动（`--stat` 摘要 + 全量文件）。
- **渲染命令 (rendered command)**：cli-agent 模板渲染后 / exec 的实际 argv（`agent.Resolved{Command,Args}`）。
- **捕获钩子**：`job.Service.finish()`（local/worker/peer 统一终态点）里新增的"采集产出"一步。

## 3. 范围与分期

| 阶段 | 内容 | 依赖 | 风险 |
|---|---|---|---|
| **P1** | 模型地基（schema 加列 + 捕获钩子）+ **E15 渲染命令** + **E6 结构化结果**（local） | 无 | 低 |
| **P2** | **E1 产物回取**（list + 下载 API + Web，local） | P1 | 低 |
| **P3** | **E12 diff 快照**（git diff at finish，local；按项目开关） | P1 | 中 |
| **P4** | **远端捕获回传**（worker WS 帧 + peer SSE 事件：让 worker/peer job 也带回产物/结果/diff/命令） | P1–P3 | 中 |

> P1–P3 **local-first** 先端到端见效（本机/容器共享盘场景即覆盖大多数）；P4 把同样的产出从远端执行机带回，隔离风险单独做。每阶段绿灯即提交（SR1202）。

## 4. 现状关键点（事实，带 file:line）

| 环节 | 位置 | 说明 |
|---|---|---|
| **统一终态钩子** | `internal/job/service.go:402` `finish()` | local/worker/peer 三路最终都汇到此（`run.Run` 返回 → `classify` → `finish`，`service.go:384-387`）；`persist(snap)` 在 `:422`，**捕获插在 finish 入口、persist 之前** |
| 结果目录 / cwd | `entry.result.ResultDir` / `entry.result.Cwd` | ResultDir=`<host_path>/tmp/gofer/<job_id>/`；Cwd=SafeJoin 后的绝对 workdir（git diff 用）。生成见 `project/path.go:90`、`store/filestore.go:32` |
| 日志写入 | `store/filestore.go:57` `LogWriter` | 现有"在 result_dir 写文件"先例，产物/diff 全量文件可复用同目录 |
| 持久化 schema | `jobstore/store.go:59`(DDL) + `:156` `migrate()` | 加列走 `migrate()` 的 `ALTER TABLE jobs ADD COLUMN`（additive）；`request_json` 是"大 JSON 入列"先例（`service.go:219` marshal → `jobs.go` 入列） |
| 渲染命令来源 | `agent/adapter.go:27` `Build()` → `Resolved{Command,Args,Env}` | `service.go:250` 处 host 渲染（**仅 local**；worker 在 worker 侧 Build）。运行时有、**不入库** |
| 远端镜像 | `wsproto/frames.go`(Log/Result/...) + `runner/worker/runner.go:204` sink / `runner/peerhttp/runner.go:141` handleFrame | 日志经帧/SSE 镜像回 host log 文件；产物/结果/diff 回传需扩帧/事件（P4） |
| 详情读路径 | `httpapi/job_handler.go:136` `handleGetJob` / logs / `stream_handler.go` | handler 风格：`s.jobs.Get(id)` + `c.JSON`/`http.ServeFile`；router 注册 `server.go:200`+ |
| Web 详情 | `web/src/views/JobDetail.vue`（meta 区 + interactions + LogTape） | 新"产出与审计"面板插在 interactions 与 LogTape 之间 |
| 子进程 | `runner/local/runner.go:52` `exec.CommandContext(...,Dir=WorkDir)` | git diff 子进程可同款起（`exec.CommandContext(ctx,"git",...,Dir=cwd)`） |

> **无现成 git 集成**——P3 自带最小 git 调用封装（探 `git rev-parse` → `git diff`）。

## 5. 架构与关键改动

统一收敛到 **finish() 捕获钩子 → store 新列/文件 → 详情 API → Web 面板**：

```txt
run.Run(ctx,req) 返回 ──▶ classify ──▶ finish(entry,...)            ← service.go:384-387/402
                                          │
                                  ┌── captureOutcomes(entry) ──┐    ← 新增一步（persist 前）
                                  │  E15 渲染命令(local: 捕获 Resolved)
                                  │  E6  读 <result_dir>/result.json
                                  │  E1  扫 <result_dir>/artifacts/ → 清单
                                  │  E12 git diff <cwd> → changes.diff + --stat 摘要
                                  └────────────┬───────────────┘
                                   写 entry.result 新字段 → persist(snap)  ← schema 加列
                                                │
   GET /v1/jobs/{id}            ── 含 rendered_command / result_json / diff_summary（小）
   GET /v1/jobs/{id}/artifacts  ── 清单（name/size/mtime）
   GET /v1/jobs/{id}/artifacts/{name} ── 下载（path-safe）
   GET /v1/jobs/{id}/diff[?full=1] ── 摘要 / 全量
        └─▶ Web JobDetail「产出与审计」面板（产物下载 / 结构化结果 / diff / 渲染命令）

远端(P4): worker 侧 captureOutcomes → 新 WS 帧/扩 Result 回传 → host 落同一字段；peer 同理走 SSE 事件
```

**改动面**：
- 低：schema 加 4 列（`rendered_command` / `result_json` / `artifacts_json` / `diff_summary`）+ `migrate()`；finish 钩子串 captureOutcomes；get_job 多回小字段。
- 低-中：新增 artifacts list/download + diff handler + Web 面板。
- 中：P3 git diff 封装（探仓/超时/截断/非 git 降级）；P4 wsproto 扩帧 + peer SSE 事件 + 两侧 capture。

## 6. 模块详设

### 6.1 捕获钩子（P1 公共地基）
`internal/job/service.go` `finish()` 入口、`persist` 前插 `s.captureOutcomes(entry, runReq)`：
```go
// captureOutcomes 采集 job 产出（best-effort，失败不影响 job 终态）：
// 渲染命令(已有)/result.json/artifacts 清单/diff，写入 entry.result 对应字段。
func (s *Service) captureOutcomes(entry *jobEntry, runReq runner.Request) {
    // E15: local 已在 Build 时拿到 Resolved → 存 runReq.Command/Args（env 仅存 key）
    // E6 : 读 <result_dir>/result.json（存在且 ≤ 上限）→ entry.result.ResultJSON
    // E1 : 列 <result_dir>/artifacts/ → []ArtifactItem JSON → entry.result.ArtifactsJSON
    // E12: P3 接入（git diff）
}
```
- **best-effort**：任何采集失败只记日志、不改 job 状态（产出是附加信息，不能让 diff 失败把 done 变 failed）。
- 各项有大小上限（result.json / diff 摘要入库截断；artifacts 仅存清单元数据，文件留盘）。

### 6.2 E15 渲染命令（P1）
- local：`service.go:250` Build 得到的 `Resolved{Command,Args,Env}`，把 `Command`+`Args` 经 runReq 带到 finish，存 `rendered_command`（JSON `{command,args,env_keys}`）。**env 只存 key 名、不存值**（SR403/SR805，防 secret 入库）。
- 详情/get_job 回 `rendered_command`；Web 展示 + 复制按钮。worker/peer 的渲染命令在执行侧，P4 带回。

### 6.3 E6 结构化结果（P1）
- 约定：agent/wrapper 把结果写 `<result_dir>/result.json`（result_dir 经 `{{result_dir}}` 模板已可得）。
- finish 时若存在且为合法 JSON 且 ≤ 上限（如 256KB）→ 存 `result_json` 列；超限/非法 → 记 warning 跳过。
- get_job 回 `result_json`（已是结构化，前端直接渲染）；Web "结构化结果"面板 pretty-print。

### 6.4 E1 产物回取（P2）
- 约定：产物 = `<result_dir>/artifacts/` 下的文件（agent 写到此目录）。finish 时扫目录 → 清单 `[{name,size,mtime}]` 存 `artifacts_json`（仅元数据；文件留盘）。
- `GET /v1/jobs/{id}/artifacts` → 清单（从库读，或不存库时实时扫目录）。
- `GET /v1/jobs/{id}/artifacts/{name}` → 下载：**name 做 path-safe 校验**（SafeJoin 进 artifacts 目录，拒 `..`/绝对路径/软链逃逸），`http.ServeFile` + `Content-Disposition`。
- Web：产物列表 + 下载链接。

### 6.5 E12 diff 快照（P3）
- finish 时若 `cwd` 是 git 仓（`git -C <cwd> rev-parse --is-inside-work-tree`）：
  - `git -C <cwd> diff --stat`（+ `git diff` 全量）→ 全量写 `<result_dir>/changes.diff`，`--stat` 摘要（截断 ~32KB）存 `diff_summary` 列。
  - 子进程带 ctx 超时（如 5s）、输出上限；非 git / 超时 / 出错 → 优雅跳过（记 warning）。
- **基线**：v1 用 `git diff`（工作树 vs HEAD/index，即"未提交的改动"）；若 agent 自行 commit 会漏——§10-D 决定是否加"job 开始打基线 ref"（v2）。
- 开关：项目级 `capture_diff`（默认：cwd 是 git 仓即开；可关）。
- `GET /v1/jobs/{id}/diff[?full=1]`：默认摘要（库），`full=1` 读 `changes.diff` 文件。Web diff 面板（摘要 + 看全量链接）。

### 6.6 远端捕获回传（P4）
- worker 侧执行后本地 `captureOutcomes`，把"渲染命令 + result.json + artifacts 清单 + diff 摘要"经 **新 WS 帧 `Outcome`（w→s）**（或扩 `Result` 帧的可选字段）回传；host sink 落同一字段。产物**文件**回取两策（§10-E）：(A) 共享盘直接读；(B) 按需经 worker 拉取端点/WS 传输（v1 可仅回清单+小结果，大文件留 worker 侧、标注来源）。
- peer-http 侧：在 SSE 加 `outcome` 事件（`peerhttp/runner.go:141` handleFrame 新分支）；产物经 peer 的 artifacts 端点代理下载。

## 7. 数据模型
`jobs` 表加 4 列（`migrate()` additive，无破坏性迁移）：
```sql
ALTER TABLE jobs ADD COLUMN rendered_command TEXT;  -- {command,args,env_keys} JSON（E15）
ALTER TABLE jobs ADD COLUMN result_json      TEXT;  -- <result_dir>/result.json 内容（E6）
ALTER TABLE jobs ADD COLUMN artifacts_json   TEXT;  -- [{name,size,mtime}] 清单（E1，仅元数据）
ALTER TABLE jobs ADD COLUMN diff_summary     TEXT;  -- git diff --stat 截断摘要（E12）
```
- `JobResult`/`JobRecord`/`toRecord`/`fromRecord`（`job/model.go` + `jobstore/jobs.go`）加对应字段，`json:"...,omitempty"`。
- 全量 diff（`changes.diff`）与产物文件**留结果目录**，不入库（避免 DB 膨胀，呼应日志留文件的现有取舍）。

## 8. API
| 方法 | 路径 | 变更 | 说明 |
|---|---|---|---|
| GET | `/v1/jobs/{id}` | 改 | 响应加 `rendered_command` / `result_json` / `diff_summary`（小字段，omitempty） |
| GET | `/v1/jobs/{id}/artifacts` | 新 | 产物清单 `[{name,size,mtime}]` |
| GET | `/v1/jobs/{id}/artifacts/{name}` | 新 | 下载单个产物（path-safe + Content-Disposition） |
| GET | `/v1/jobs/{id}/diff` | 新 | `?full=1` 全量（读 changes.diff）/ 默认摘要 |
- MCP：可加 `bridge_get_artifacts` / `bridge_get_result`（让 agent 也能读另一个 job 的产出，接 E7 工作流）——§10-F 决定是否本期带。

## 9. 安全
- 产物下载 `name` **强校验**（SafeJoin 限定 `<result_dir>/artifacts/` 内、拒 `..`/绝对/软链逃逸）——这是新增的对外文件下载面，是本设计最大安全点。
- 渲染命令 **env 只存 key、不存值**；result.json/diff 不得回显 token/secret（agent 责任 + 文档提示）。
- diff/产物下载仍在 `/v1` 鉴权 + caller token 之内；diff 子进程带超时与输出上限防滥用。
- 全量 diff/产物文件经结果目录，受 retention 一并清理。

## 10. 待确认事项（决策点，附推荐）
- **D1（远端范围）**：P1–P3 **local-first**，P4 再做 worker/peer 回传——认可分期？（推荐：是）
- **D2（产物声明）**：约定 **`<result_dir>/artifacts/`**（推荐，无需 agent 协议）；或额外支持 job 请求里 `output_globs`？（推荐：v1 仅目录约定）
- **D3（结构化结果）**：约定 **`<result_dir>/result.json`**（推荐）；上限 256KB。
- **D4（diff 基线）**：v1 用 `git diff`（未提交改动，推荐）；是否需要"job 开始打基线 ref/stash"以覆盖 agent 自行 commit 的情况（留 v2）？
- **D5（diff 开关）**：cwd 是 git 仓**默认开**、项目级 `capture_diff:false` 可关（推荐）；diff 子进程超时 5s、摘要入库 32KB、全量留文件。
- **D6（远端大产物，P4）**：worker 产物文件回取——v1 **仅回清单 + 小结果**（大文件留 worker 侧、标注 source=worker，共享盘则直读）？还是必做 WS/HTTP 拉取通道？（推荐：v1 清单+小结果 + 共享盘直读，拉取通道留后续）
- **D7（MCP）**：本期是否加 `bridge_get_artifacts`/`bridge_get_result`（为 E7 工作流铺垫）？（推荐：P2/P1 各带一个读 tool，低成本）

## 11. 结论
四项（E1/E6/E12/E15）共用 finish() 捕获钩子 + jobs 加 4 列 + 详情 API/Web 面板，强内聚、合一份方案分 P1→P4。**local-first** 先端到端见效，远端回传隔离为 P4。最大复用：`finish()` 统一终态点、`result_dir` 写文件先例、`migrate()` 加列模式、`request_json` 大 JSON 入列先例。最大安全点是产物下载的路径校验。

**下一步**：审核（重点过 §10 决策）→ 通过后出分阶段 `plan`（P1–P4，细到列/函数/handler/Web 与验收）。
