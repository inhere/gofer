# dev-agent-bridge SQLite 存储后端设计（C1 根治）

> 子设计文档。主设计 [`2026-06-16-dev-agent-bridge-design.md`](./2026-06-16-dev-agent-bridge-design.md) §9.3（结果目录）、总览 [`architecture-overview.md`](./architecture-overview.md) §9.1（C1）为上游。本文给出用 SQLite 替换 `jobs.jsonl` 索引与 `result.json`/`interactions.jsonl` 元数据的方案与分期计划。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-18 | Claude | 初版：SQLite（modernc 纯 Go）做 job 元数据/索引/交互 store，日志仍文件；in-memory 仅保留 live job；分期 SP1–SP5。已确认"直接上 SQLite"。 |
| v0.2 | 2026-06-18 | Claude | 确认**不迁移**（fresh-start、不提供 import、直接切 DB、无双写）；request.json SP1/SP2 留文件、SP5 入列。§10/§14 据此收敛。 |
| v0.3 | 2026-06-18 | Claude | **SP1–SP5 全部实施完成并提交**（SUPMODE）。落地与设计的差异/补充见 §13 实施结果与 §16 实施补记。 |

## 2. 背景与目标

C1（总览 §9.1）：`Service.jobs` 内存表永不驱逐、`jobs.jsonl` 仅追加（2 行/job）、`ListJobs` 每次全量读+折叠 → 长跑 server 内存/磁盘/列表耗时随 job 数无界增长。

目标：用 **SQLite（`modernc.org/sqlite` v1.52.0，纯 Go、无 cgo，容器内已验证可构建）** 承载 job 元数据/索引/交互，**根治** C1，并顺带获得原生 **list 过滤/分页/保留/搜索**。**日志（stdout/stderr）仍是文件**（流式 tail-from-offset 与"镜像进 Writer"机制依赖追加文件，且日志可能很大，不入库）。

## 3. 范围

**入库（SQLite）**：job 记录（= JobResult 元数据 + 提交参数）、job 索引（替代 `jobs.jsonl`）、interactions（替代 `interactions.jsonl`）。

**仍为文件（不变）**：`stdout.log` / `stderr.log`（per-job 结果目录）；`request.json` 首期保留为文件（小、写一次，低价值迁移，后续可选入库）。

**不做**：把日志塞进 DB；分布式/多写入者；ORM（用 `database/sql` + 裸 SQL）。

## 4. 已确认

- 用户选 **直接上 SQLite**（非 jsonl stopgap），避免过渡代码作废。
- 纯 Go modernc，容器内可构建（cgo 版 mattn 因无 gcc 不可用）。

## 5. 架构

```txt
┌─ job.Service ───────────────────────────────────────────────┐
│ 内存 liveJobs map[id]*jobEntry  ← 仅 LIVE job（cancel/done/  │
│   interaction 等待通道、运行 goroutine 状态）；终态即驱逐     │
│        │ 写：create/finish/interaction → UPSERT              │
│        ▼ 读：未命中内存 → 查 DB                              │
│   metaStore (SQLite, 全局单库)  ── jobs 表 / interactions 表 │
│   logStore  (文件, per-job 结果目录) ── stdout.log/stderr.log│
└─────────────────────────────────────────────────────────────┘
```

要点：
- **DB 单库全局**（一个 `agent-bridge.db`），`project_key` 作列 → 跨项目 `ListJobs` 一条 SQL（替代"遍历各项目 jsonl"）。
- **内存只放 live job**：终态 job 在最终 DB 写入后从内存驱逐 → 内存随并发量（而非历史量）有界，根治 C1 内存侧。
- **读回退**：`GetJob`/`serveLog`/`stream`/`GetInteractions` 内存未命中 → 查 DB（终态/历史 job）。
- **日志路径不变**：job 行存 `result_dir`，日志读写仍走该目录文件 → SSE/`/logs`/镜像机制零改动。

## 6. 数据模型

```sql
CREATE TABLE IF NOT EXISTS jobs (
  id           TEXT PRIMARY KEY,
  project_key  TEXT NOT NULL,
  agent        TEXT NOT NULL,
  runner       TEXT NOT NULL,
  worker_id    TEXT,                 -- 预留（ws-worker）
  status       TEXT NOT NULL,
  exit_code    INTEGER NOT NULL DEFAULT 0,
  cwd          TEXT,
  result_dir   TEXT NOT NULL,        -- 日志/产物所在目录
  request_json TEXT,                 -- 原始 JobRequest（便于重投/审计）
  error        TEXT,
  started_at   INTEGER NOT NULL,
  ended_at     INTEGER,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_started ON jobs(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_jobs_proj_status ON jobs(project_key, status);

CREATE TABLE IF NOT EXISTS interactions (
  id           TEXT NOT NULL,
  job_id       TEXT NOT NULL,
  type         TEXT NOT NULL,        -- question|choice|confirmation
  prompt       TEXT NOT NULL,
  options_json TEXT,                 -- []{value,label}
  status       TEXT NOT NULL,        -- pending|answered|cancelled
  answer       TEXT,
  created_at   INTEGER NOT NULL,
  answered_at  INTEGER,
  PRIMARY KEY (job_id, id)
);
CREATE INDEX IF NOT EXISTS idx_inter_job ON interactions(job_id);
```

- 写均 **UPSERT**（`INSERT ... ON CONFLICT(...) DO UPDATE`）：create 与 finish 是对同一行的两次 upsert（不再像 jsonl 追加两行），天然去重。
- `updated_at` 用于保留策略与排序兜底。

## 7. store 接口演进

现 `store.Store`（filestore）混了"日志文件"与"元数据/索引/交互"。拆为两个关注点：

- **保留（文件）**：`Dir/Ensure/LogWriter/ReadLogTail`（+ `WriteRequest` 首期保留或改为写 request_json 列）。
- **迁到 metaStore（SQLite）**，新接口 `MetaStore`：
  ```go
  UpsertJob(rec JobRecord) error
  GetJob(id string) (JobRecord, bool, error)
  ListJobs(opts ListQuery) ([]JobRecord, error)   // project/status/limit/offset/since
  UpsertInteraction(jobID string, it Interaction) error
  ListInteractions(jobID string) ([]Interaction, error)
  PruneJobs(policy RetentionPolicy) (int, error)   // SP4
  ```
- `JobRecord` = JobResult + request 投影（job 包既有结构尽量复用；MetaStore 放 `internal/jobstore` 新包，依赖 job 类型或用中性 record）。
- 旧 `AppendIndex/ReadIndex/WriteResult/ReadResult/AppendInteraction/ReadInteractions` 由 MetaStore 等价方法取代。

## 8. job.Service 改动

- `Submit`：建 entry（live）+ `UpsertJob(queued)`；不再 WriteResult/AppendIndex 文件。
- `finish`：`UpsertJob(terminal)` 后从 `liveJobs` **驱逐** entry（释放内存）。
- `Get`：先查 liveJobs；未命中 → `metaStore.GetJob`。
- `ListJobs`：直接 `metaStore.ListJobs`（DB 过滤/排序/分页）+ 合并 live 内存态（live 比 DB 新的字段以内存为准；或 finish 时已 upsert，live 与 DB 收敛）。
- `serveLog`/`stream`：用 JobRecord.result_dir 读日志文件（不变）。
- 交互（含 peer-http 注入/worker 跨线）：`CreateInteraction`/`injectInteraction`/`AnswerInteraction` 改为 `UpsertInteraction` + 内存 live 态；`GetInteractions` 未命中内存 → `ListInteractions`。`WaitAnswer` 仍用内存通道（仅 live 有意义）。

## 9. 并发

- 单 `*sql.DB`（database/sql 连接池）；SQLite 内部串行化写。
- 开 **WAL**（`_pragma=journal_mode(WAL)`）+ `_pragma=busy_timeout(5000)` + `foreign_keys`。
- 写频率低（状态变更/交互），WAL 足够；无需自建写锁（去掉现有 `indexMu`）。

## 10. 迁移

**不迁移（已确认）**：首次启动建库建表即用；历史 `jobs.jsonl`/`result.json`/`interactions.jsonl` **不导入、不提供 `import`**，旧 job 不再出现在列表（结果目录文件仍在盘上可人工查）。旧文件路径停写；logs 路径不变。**直接切 DB，无双写过渡。**

## 11. 配置

```yaml
storage:
  # 现有：default_exchange_subdir / default_result_subdir / root
  db_path: ""   # 新增可选。解析优先级(serve, ResolveDBPath)：
                #   显式 db_path > <root>/gofer.db(若设 root) > <config-dir>/gofer.db
```
日志目录仍由现有 ResultBaseDir 规则决定（per-project 或 root）。

> **ws-worker 变体（2026-06-25 补）**：worker 经 `core.Build` 与 serve 共用 `ResolveDBPath`
> 会在同一 config-dir 下与 serve 的 `gofer.db`（或多 worker 之间）撞库。故 worker 走
> `config.ResolveWorkerDBPath(workerID)`：显式 `db_path` > `<root>/<worker_id>.db`(设 root) >
> `<config-dir>/worker/<worker_id>.db`(默认) —— 按 worker_id 命名隔离，`workerConfigToConfig`
> 启动时 pin。设了 `root=<config-dir>/worker` 与不设 root 都收敛到同一 `<config-dir>/worker/<id>.db`。

## 12. 安全

- DB 不存 token/secret（仅 job 元数据 + prompt/cmd；与现 result.json 同口径）。
- DB 文件权限 0o600；与 logs 同属 private 区。
- prompt/cmd 可能含敏感参数——与现状一致，不新增暴露面。

## 13. 实施分期（SP）

| 阶段 | 内容 | 价值 |
|---|---|---|
| **SP1** | 加 modernc 依赖；`internal/jobstore` 建库/建表/WAL；`JobRecord` + `UpsertJob/GetJob/ListJobs` + 单测（含并发写） | DB 地基 |
| **SP2** | `ListJobs` 切到 DB（替代 jsonl 扫描）；`Submit`/`finish` 双写期或直接切；`Get` 回退 DB | 解决索引无界扫描 + 列表过滤/分页 |
| **SP3** | 内存仅留 live job：`finish` 后驱逐 entry；`serveLog`/`stream` 用 DB record 取 result_dir | **解决内存无界（C1 内存侧）** |
| **SP4** | interactions 入 DB（UpsertInteraction/ListInteractions）；交互各路径切换；保 WaitAnswer 内存通道 | 交互历史入库 |
| **SP5** | 保留策略 `PruneJobs`（按 age/数量，可选删旧日志目录）；停写旧 jsonl/result.json 文件；README/总览/状态矩阵更新 | **解决磁盘无界 + 收尾** |

> 每个 SP 子阶段绿灯即提交（SR1202）。SP1–SP3 为 C1 核心；SP4–SP5 收尾增强。

**实施结果（2026-06-18 SUPMODE，全部完成并提交）**：

| 阶段 | commit | 结果 |
|---|---|---|
| SP1 | `7155917` | jobstore 包（Open/UpsertJob/GetJob/ListJobs，中性 JobRecord），并发写单测全绿 |
| SP2 | `08b3713` | Service.meta + persist 直切 DB，ListJobs/Get 切 DB，config.ResolveDBPath，buildCore 持有/关库 |
| SP3 | `aebe474` | finish 后驱逐内存（仅持久化成功才驱逐）+ 各读路径 DB 回退；**C1 内存侧根治** |
| SP4 | `fcc6572` | interactions 入库（UpsertInteraction/ListInteractions），停写 interactions.jsonl |
| SP5 | `f3ec55b` | request_json 入列停写 request.json + 清理 store 死方法 + PruneJobs 保留策略 + serve 周期 prune |

## 14. 已定 / 风险

- **直接切 DB**（已定）：无双写过渡、不迁移（§10）。
- **request.json**（已定）：SP1/SP2 先保留文件减小改动面；SP5 入 `request_json` 列后停写文件。
- **modernc 体积**：纯 Go SQLite 增二进制体积（~数 MB）。可接受（已用 upx）。
- **测试**：metaStore 用临时库文件；并发写需覆盖（WAL + busy_timeout 下多 goroutine upsert）。`-race` 仍需主机（容器无 gcc）。
- **WAL 文件**：`-wal`/`-shm` 旁文件随库目录；优雅停机无需特殊处理（WAL 自动 checkpoint）。

## 15. 结论

SQLite（modernc 纯 Go）做 job 元数据/索引/交互 store、日志仍文件、内存仅留 live job —— 根治 C1 三处无界增长，并原生支持列表过滤/分页/保留/搜索，对 SSE/日志/镜像/交互机制零破坏。建议按 SP1→SP3 先落 C1 核心，SP4→SP5 收尾。

## 16. 实施补记（与设计的偏离/补充）

实施中基于运行/测试证据做了两处必要调整（已在代码与对应 commit 落地）：

- **§9 写并发——加进程内写锁（覆盖原"无需自建写锁"）**：`WAL + busy_timeout(5000)` 在全速并发 upsert 下仍偶发 `SQLITE_BUSY (database is locked)`。改为 `Store.writeMu` 串行化进程内写（单进程单库，读仍走连接池并发），彻底消除 BUSY。SP3 起 DB 是终态 job 唯一真源，故 `finish` 仅在 terminal 持久化**成功**后才驱逐内存（失败保留 entry，杜绝丢 job）。
- **交互对"已驱逐终态 job"的错误码——确定性保持 409**：SP3 驱逐后终态 job 不在内存，与未知 job 不可区分；若直接返回 404 会与"未驱逐时返回 409"随时序抖动。改为交互入口（create/answer/inject）内存未命中时查 DB：命中=已驱逐终态→`ErrJobTerminal`(409)，否则 `ErrUnknownJob`(404)。对外契约保持 409 不变。

- **新增字段**：`JobResult.updated_at`（对外可见，DB 排序/保留用）；`JobResult.RequestJSON`（`json:"-"`，仅审计/重投，不对外）。
- **config 分层**：retention→jobstore.Policy 的转换放 job 包，`config` 不依赖 `jobstore`（保持 leaf）。
- **优雅停机**：serve 周期 prune goroutine 随 serve 返回停止，与现有 `store.Close` 同口径；进程被信号直杀时不跑 defer（与现状一致，真正的信号优雅停机是 serve 独立增强，超出本设计范围）。
