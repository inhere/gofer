# WEB-03 P3：cast 录制 + pty_sessions 表 + retention + recording gate 设计细化

> 上游：主设计 [`2026-07-03-web-pty-attach-design.md`](2026-07-03-web-pty-attach-design.md) v0.8（D3 审计 / K4 cast / §8 数据模型 / §9 存储 / §10 API / §11 安全 / §12 待确认 / §14 阶段 P3）；P2 设计 [`2026-07-04-web-pty-attach-P2-design.md`](2026-07-04-web-pty-attach-P2-design.md) v0.4（已实施收官 GREEN）。
> P3 = **给 relay 接上 cast 录制文件（asciinema v2 + 加密 + retention）+ 建 `pty_sessions` 一等表 + `GET /pty/recording` 下载 gate**。P0–P2 的 pty attach 端到端已完成。
> 铁律 G022/G024：`ptyrelay` 保持 **leaf（stdlib only）**，**不 import jobstore/job/config**——故 cast sink 注入 + `pty_sessions` 持久化都放 **httpapi 层**（已有 `s.jobs`/store）。G031 不引第三方 module（加密用 stdlib `crypto/aes`+`crypto/cipher`）。

## 修订记录
| 版本 | 日期 | 说明 |
|---|---|---|
| v0.1 | 2026-07-04 | 初稿：8 决策 D-P3-1..8 + 现状锚点 + 数据模型 + 加密封套 + retention 两 regime + 待评审。据 P3 现状测绘（cast 挂钩/config/jobstore/prune/result_dir/binding/授权/asciinema 全部实测 file:line）。 |

## 0. 范围

**做（P3）**：serve 侧 cast sink（asciinema v2 raw-byte writer）+ 加密（AES-256-GCM 分块封套）+ 落 `<result_dir>/pty.cast`；`pty_sessions` 一等表（建/finalize/查/prune）；cast retention sweep（挂现有 prune 链）；`GET /v1/jobs/{id}/pty/recording` gate（authed + 授权 + 解密流式）；bytes_in/out 采集；config 补默认 TTL + 组合 fail-fast。

**不做**：前端展示录制（P4）；录制回放富 UI（永不，主设计 §3）；key 轮换（§12 留后续，P3 固定单 key）；cast 压缩。

## 1. 现状锚点（P3 测绘，附 file:line）

| 组件 | 位置 | P3 依赖 / 缺口 |
|---|---|---|
| relay cast 挂钩 | `ptyrelay/relay.go:58,74-76,87-88,129-131` | `cast writeCloser`(**仅 `Write`，无 `Close`**) + `WithCast` option + recordLoop 写入点 `if r.cast!=nil` 已就位；**无任何真实 sink 实现**；**`Registry.Open`(`registry.go:107`) `New(source)` 从不传 cast**（`WithCast` 是死代码）→ P3 补注入通道 + `Close()` |
| bytes 计数 | `relay.go:167 RecordedLen`=`ring.WrittenTotal()` | 单向输出累计→bytes_out；**无 bytes_in**（stdin 无计数）→ P3 补 src.Write 计数 |
| RelayBinding | `registry.go:34-42` | 仅 worker/instance/job/session_id/nonce/expiry；**无 cols/rows**（在 JobRequest，serve 不留存）→ P3 补 Cols/Rows |
| config CastConfig | `config/model.go:436-450`（P1 T2） | `RetentionTTLHours`/`Encryption{Enabled,KeyEnv}`；常量 `castDefaultTTLHours=24`(**定义未用**)/`castMaxTTLHours=168`；loader fail-fast(`loader.go:339-344`) 仅「enabled→key名非空」+「TTL≤max」两独立条；**无「无 key+超阈值→fail」组合条**；**key 从不 `os.Getenv` 取** |
| jobstore | `jobstore/store.go:59-239 schemaStmts`,`:290 applySchema`,`:304 migrate`,`:494 tableColumns`；`jobs.go:33-99 JobRecord`(interactive 列 P1 T8) | 迁移幂等模式（新表 = 追 `CREATE TABLE IF NOT EXISTS` + `Upsert/Get`）；**无 `pty_sessions` 表** |
| retention/prune | `jobstore/prune.go`(`PruneJobs`/`PruneWorkflows` 返被删 result_dir)；`job/prune.go:28 Service.Prune`(删行 + `os.RemoveAll(dir)`)；`serve/serve.go:267 startPruneLoop`(ticker, 默认 60m) | 完整 job/wf retention 链；**cast retention 无消费方** → P3 挂进 `Service.Prune`/loop |
| result_dir | `job/submit.go:91,161`；布局 `<base>/<job_id>/`；`JobResult.ResultDir`(`model.go:170`)；handler 已 `s.jobs.Get(jobID)`(`pty_connect_handler.go:79`) | cast 落 `filepath.Join(res.ResultDir,"pty.cast")`（目录 job 提交侧已建） |
| 授权/下载 | `attach_ticket_handler.go:76-85 callerMayAttach`(RequireAttachCapability+CanAttach+CanAdmin+owner)；`callerCanAdmin`(`server.go:82`)；`artifact_handler.go:36 handleDownloadArtifact`(Get→remote-409→SafeJoin→ServeFile) | recording gate 复用 `callerMayAttach`/`CallerCanAdmin` + ServeFile/远端-409 模式 |
| asciinema | 全库无 | 纯新写 v2 header + 事件行 |
| pty-connect handler | `httpapi/pty_connect_handler.go:86-96`（Open→绑 remoteSource→等 `Relay.Done()`/ctx→defer `Close`） | **serve 侧 pty 会话生命周期的天然单点** → cast 建于此 Open、pty_sessions 行建于 Open/finalize 于 Done 后 |

## 2. 关键设计决策

### D-P3-1 cast sink = serve 侧 asciinema v2 writer，建于 relay Open、随 relay Close 关
`ptyrelay` 是 leaf，不能碰文件加密/config。→ cast sink 由 **httpapi 层**（`handlePtyConnect`，已有 `s.jobs.Get`→result_dir + `s.cfg`→cast 配置）在 **Open 时构造**并经新的 `Registry.Open(nonce, source, opts...)` 透传给 `New(source, WithCast(sink))`（首字节即录，K6 eager 一致）。
- **`writeCloser` 补 `Close()`**（现只有 `Write`）：`Relay.Close()`（`relay.go:235-253`）末尾 `if r.cast!=nil { r.cast.Close() }`（flush + 关文件/加密封尾）。cast sink 唯一 writer = recordLoop（主链路同步），无并发写。
- cast 写失败**不阻断主链路**：sink.Write 错误只记一次 + 停止后续 cast 写（`recording_uri` 置失效），pty 会话/relay 不受影响（录制是审计辅助，非关键路径）。
- serve-local drop-in（未来）同构：sink 与传输无关。

### D-P3-2 `RelayBinding` += `Cols/Rows`（供 asciinema header + 表初始尺寸）
host runner `Prepare`（`runner/worker/runner.go:163`）已有 `f.Cols/f.Rows`，填入 binding。pty-connect handler Open 时从 `binding.Cols/Rows` 取初始尺寸写 asciinema header（`width/height`）+ pty_sessions 初始 cols/rows。resize 后的 current size 由 attach resize 帧驱动（P3 可选更新表 current cols/rows，非必需）。

### D-P3-3 `pty_sessions` 一等表（jobstore 新表；持久化在 httpapi，ptyrelay 保 leaf）
`jobstore` 加表 + `PtySessionStore`（`UpsertPtySession`/`GetPtySession`/`ListPtySessionsByJob`/`PrunePtySessions`）。**写入方 = httpapi `handlePtyConnect`**（有 store 句柄；ptyrelay 不 import jobstore）：
- **建行于 Open**：`pty_session_id`(PK,from binding)/`job_id`/`worker_id`/`instance_id`/`owner`(=`JobResult.CallerID`)/`state`=`open`/`cols`/`rows`(from binding)/`recording_uri`(cast 启用则 `<result_dir>/pty.cast` 否则空)/`bytes_in`=0/`bytes_out`=0/`started_at`。
- **finalize 于 relay.Done 后 handler 返回前**：`state`=`closed`/`ended_at`/`bytes_out`=`Relay.RecordedLen()`/`bytes_in`=relay 输入计数（D-P3-8）/若 cast 写失败置 `recording_uri`=空。单点写、幂等 Upsert。
- 时间字段 bigint（Unix 秒，SR301）；表名 `pty_sessions`（服务前缀例外？主设计 §8 用 `pty_sessions` 无 vp/aii 前缀——gofer 是独立工具无业务前缀约束，沿用 `pty_sessions`）。
- 详情/列表暴露 `pty_sessions` = **P4**（前端）；P3 只落库 + 供 recording gate 查 recording_uri/owner。

### D-P3-4 cast 加密 = 分块 AES-256-GCM 封套（stdlib，key=SHA256(env)）
明文 asciinema 含用户键入（可能粘 token/密码，主设计 §11）。→ `Encryption.Enabled` 时 cast sink 外套加密：
- key = `sha256(os.Getenv(KeyEnv))`（32B，容任意长度 env secret；serve start 读取，缺失 fail-fast）。
- **分块封套**（append 友好、不缓存全文）：文件头 = magic(4B)+version(1B)+随机 fileNonce(12B)；随后每块 = `uint32 len` + `GCM.Seal(nonce=fileNonce⊕counter, plaintext_chunk)`（chunk ≤ 16KB，counter 递增）。Close 写结束块（len=0）。GCM 每块带认证 tag，防截断/篡改。
- 解密（recording gate）= 反向逐块 `Open`，流式写 http.ResponseWriter。
- **不引第三方 module**（`crypto/aes`+`crypto/cipher`+`crypto/sha256`+`crypto/rand` 全 stdlib）。算法/格式版本号入文件头，便于 §12 未来 key 轮换。

### D-P3-5 config：默认 TTL 落地 + 组合 fail-fast + 运行期取 key
- `castDefaultTTLHours=24` 落地：`RetentionTTLHours==0` → 用 24（现定义未用）。
- **组合 fail-fast（补缺口）**：`!Encryption.Enabled && effectiveTTL > castPlaintextMaxTTLHours(=24)` → loader 报错（明文 cast 只允许短 TTL；长留存必须加密，主设计 K4「无 key+超阈值→fail-fast」）。
- **运行期取 key**：serve start 时若 `Encryption.Enabled` 则 `os.Getenv(KeyEnv)`，空 → serve 启动 fail-fast（loader 只校验 env 名非空，value 缺失在 start 期暴露，文档写明）。key 不入日志/Redis（主设计 §11、SR403）。

### D-P3-6 cast retention = 两 regime，挂现有 prune 链
1. **cast 独立 TTL sweep**（短，默认 24h）：新 `PrunePtySessions(olderThan)` 按 `ended_at + castTTL < now` 选行 → 删 `pty.cast` 文件 → 删/标记 pty_sessions 行。挂进 `Service.Prune()`（`job/prune.go:28`，与 job/wf prune 同 tick，`serve.go:267 startPruneLoop`）。
2. **job retention 连带**（长）：`PruneJobs` 删 job + `os.RemoveAll(result_dir)` 时连带删 `pty.cast`（在 result_dir 内，天然）+ 需**同时删该 job 的 pty_sessions 行**（job_id 关联；在 `Service.Prune` 删 job 后按返回的 job_id 删 pty_sessions，或建 FK ON DELETE——SQLite FK 默认关闭，用显式删更稳）。
- 两 regime 幂等/任意序：cast 先没（regime1 删）→ job prune 删行 no-op；job 先删→ regime1 查不到行。`recording_uri` 指向已删文件时 gate 返 404 + 详情明示（主设计 §9）。
- **sweeper 不误判 interactive（§12）**：interactive job 无 stdout 轮询产物，但走同一 `PruneJobs`（按 `created_at`/count，与产物无关），不误判；确认 interactive job 的 result_dir 仅含 pty.cast（无 result.json 时 prune 仍正常删目录）。

### D-P3-7 `GET /v1/jobs/{id}/pty/recording` gate
authed `/v1` 组（server.go:337+），复用 `callerMayAttach(caller, job)`（含 owner + CanAdmin，主设计 §10「can_admin 优先或 same owner/caller」）→ 拒则 403。
- 取 job（`s.jobs.Get`）→ 远端 job（`IsRemoteSource`）→ 409（仿 artifact_handler）。
- 查 pty_sessions 取 `recording_uri`；空/文件不存在（已 prune）→ 404（详情明示）。
- 加密 → 流式解密写 resp（`Content-Type: application/x-asciicast` 或 octet-stream）；明文 → `SafeJoinUnder`+`ServeFile`。
- **审计事件**：记录下载（caller/job/session）——复用现有事件/日志机制（notify/slog），下载敏感录制留痕。

### D-P3-8 bytes_in/out 采集
- bytes_out = `Relay.RecordedLen()`（现成，`ring.WrittenTotal()`）。
- bytes_in = **新增 relay 输入计数**：`Relay` 加 `bytesIn atomic`，`Viewer.SendInput`→`src.Write` 成功后累加；`Relay` 暴露 `InputLen()`。handler finalize 时读入表。
- 均为审计字段，写失败不影响会话。

## 3. 数据模型

**`pty_sessions`（jobstore SQLite）**：
```sql
CREATE TABLE IF NOT EXISTS pty_sessions (
  pty_session_id TEXT PRIMARY KEY,
  job_id         TEXT NOT NULL,
  worker_id      TEXT,
  instance_id    TEXT,
  owner          TEXT,              -- job.CallerID（recording gate owner 闸；空=历史 allow_empty_token）
  state          TEXT NOT NULL,     -- open | closed
  cols           INTEGER,
  rows           INTEGER,
  recording_uri  TEXT,              -- <result_dir>/pty.cast；空=未录/已失效
  encrypted      INTEGER NOT NULL DEFAULT 2,  -- 1 是 2 否（SR301 从1起、避0）
  bytes_in       INTEGER NOT NULL DEFAULT 0,
  bytes_out      INTEGER NOT NULL DEFAULT 0,
  started_at     INTEGER NOT NULL,  -- Unix 秒
  ended_at       INTEGER            -- Unix 秒；空=未结束
);
CREATE INDEX IF NOT EXISTS idx_pty_sessions_job ON pty_sessions(job_id);
CREATE INDEX IF NOT EXISTS idx_pty_sessions_ended ON pty_sessions(ended_at);
```
- 迁移：追加到 `schemaStmts`（`store.go:59-239`），`IF NOT EXISTS` 幂等；旧库 `Open()` 自动建（无需 migrate 补列，整表新增）。

**cast 文件** `<result_dir>/pty.cast`：
- 明文：asciinema v2——首行 header JSON `{"version":2,"width":cols,"height":rows,"timestamp":started_at}`，随后每输出块一行 `[elapsed_sec, "o", "<raw bytes>"]`（bytes 按 asciinema 惯例 JSON 字符串转义）。
- 加密：D-P3-4 封套包裹上述明文字节流。文件头 magic 区分明/密。

## 4. 关键流程

```txt
worker 拨入 pty-connect（P2）
  handlePtyConnect: 校验通过 → jobs.Get(job) 取 result_dir/CallerID
    cast 启用? → 建 castSink(<result_dir>/pty.cast, cols/rows from binding, 加密?)
    Registry.Open(nonce, remoteSource, WithCast(castSink)) → relay open, recordLoop 首字节录
    UpsertPtySession(open, cols/rows/owner/recording_uri/started_at)          [httpapi 写 jobstore]
  … 交互中：recordLoop 每块 → ring + castSink.Write + fanout；SendInput → bytesIn++
  relay.Done()（P2 关闭权）→ handler 返回前:
    Relay.Close() 内 castSink.Close()（flush+封尾）
    UpsertPtySession(closed, ended_at/bytes_out=RecordedLen/bytes_in=InputLen)  [finalize]
  defer Registry.Close（幂等）

下载: GET /v1/jobs/{id}/pty/recording (authed)
  callerMayAttach(caller, job)否则403 → remote?409 → GetPtySession.recording_uri
  空/文件没了→404(详情明示) → 加密?流式解密:明文 ServeFile → 审计事件

retention: startPruneLoop tick → Service.Prune()
  ① PrunePtySessions(ended_at+castTTL<now): 删 pty.cast 文件 + 删行
  ② PruneJobs(job MaxAge/Count): 删 job + RemoveAll(result_dir 含 pty.cast) + 删该 job 的 pty_sessions 行
```

## 5. 安全（P3 重点，主设计 §11）
- cast 默认加密**或**短 TTL；`!Encryption && TTL>24h` → loader fail-fast（D-P3-5）。
- 加密 key 从 `KeyEnv` 取、`sha256` 派生、不入日志/Redis/项目文件（SR403/805）；缺失 serve start fail-fast。
- recording gate：`can_admin` 优先或 owner（`callerMayAttach`）；远端 job 409；已 prune 404；下载留审计。
- cast 写失败降级（停录 + recording_uri 失效），不阻断/不泄露到主日志。
- pty.cast 落 result_dir（job 权限域内），随 job retention 连带清（无孤儿密文超期）。

## 6. 待确认（plan 内细化）
- 加密封套 chunk 大小（拟 16KB）+ 文件头 magic/version 具体字节。
- `castPlaintextMaxTTLHours` 是否复用 `castDefaultTTLHours`(24) 还是独立常量。
- pty_sessions 是否记 attacher（当前只 owner=job.CallerID）——多 attacher/只读跟随的审计粒度，P3 拟仅 owner，attacher 审计留 P4/后续。
- resize 后 current cols/rows 是否回写 pty_sessions（P3 拟只记初始，current 非必需）。
- 审计事件的具体载体（notify vs slog vs 新事件表）对齐现有机制。
- bytes_in 计数位置（`Viewer.SendInput` vs `remotePtySource.Write`）确认不重复计。

---
> 评审关注点（v0.1）：**D-P3-1** cast sink 生命周期（Open 建/Close 关）与 P2 relay 五关闭源 CAS 的交互（cast.Close 幂等、不在锁内做文件 IO 卡 registry）；**D-P3-3** pty_sessions 建/finalize 单点是否覆盖所有关闭路径（worker 掉线/dial 失败 relay 从未 Open→无 session 行？还是建 pending 行）；**D-P3-4** 分块 GCM 封套的截断/篡改安全 + 流式解密正确性；**D-P3-6** 两 retention regime 的幂等/顺序 + job prune 删 pty_sessions 行的接线点；**G024** cast sink/pty_sessions 持久化确在 httpapi、ptyrelay 仍 leaf。
