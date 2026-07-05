# WEB-03 P3：cast 录制 + pty_sessions 表 + retention + recording gate 设计细化

> 上游：主设计 [`2026-07-03-web-pty-attach-design.md`](2026-07-03-web-pty-attach-design.md) v0.8（D3/K4/§8/§9/§10/§11/§14 P3）；P2 设计 v0.4（已实施收官 GREEN）；P3 评审 [`../review/2026-07-04-web-pty-attach-P3-codex-review.md`](../review/2026-07-04-web-pty-attach-P3-codex-review.md)。
> P3 = **给 relay 接上 cast 录制（asciinema v2 + framed AEAD 加密 + retention）+ 建 `pty_sessions` 一等表 + `GET /pty/recording` 下载 gate**。P0–P2 的 pty attach 端到端已完成。
> 铁律 G022/G024：`ptyrelay` 保 **leaf（stdlib only）**，不 import jobstore/job/config——cast sink **concrete 实现 + pty_sessions 持久化都在 httpapi**（经 `writeCloser` 接口注入 relay）。G031 不引第三方 module（加密全 stdlib：`crypto/hkdf`(Go1.24+)/`aes`/`cipher`/`sha256`/`rand`，已验证 go1.25.10 可用）。

## 修订记录
| 版本 | 日期 | 说明 |
|---|---|---|
| v0.1 | 2026-07-04 | 初稿 8 决策。 |
| **v0.2** | 2026-07-04 | **codex 评审后大改**（4 阻断+6 高）：① **B1** cast close owner 归 recordLoop、`Relay.Done()`=cast 已封尾（D-P3-1 重写）；② **B2** 封套改 framed AEAD——`crypto/hkdf` 派生 key + 每帧随机 nonce + AAD 绑 {version,session_id,frame_index,is_final} + **认证 final frame**（EOF-before-final=损坏），防截断/重排/跨文件（D-P3-4 重写）；③ **B3** prune loop 启动条件含 cast（D-P3-6）；④ **B4** pty_sessions 删除进 `PruneJobs`/`PruneWorkflows` 同事务（jobstore-owned，D-P3-6）；⑤ **H1** cast 配置/key 在 serve 装配处解析并注入 httpapi（`SetCastRecorder`，D-P3-5）；⑥ **H2** `RelayBinding+=Cols/Rows`+80×24 fallback（D-P3-2）；⑦ **H3** finalize 流程写死（`Close→<-Done()(封尾)→读 bytes→Upsert`，单点 httpapi，runner 不写表）；⑧ **H4** P3 只记「已 Open 的会话」（未拨入无行，显式取舍）；⑨ **H5** httpapi 持 concrete sink、finalize 查 `sink.Err()`（ptyrelay 不知情）；⑩ **H6/M3** 加密流式解密（非 ServeFile）+ allow_empty 保守拒。 |

## 1. 现状锚点（P3 测绘，附 file:line）

| 组件 | 位置 | P3 依赖 / 缺口 |
|---|---|---|
| relay cast 挂钩 | `ptyrelay/relay.go:58,74-76,87-88,129-131` | `cast writeCloser`(仅 `Write`) + `WithCast` + recordLoop 写入点已就位；**无真实 sink**；**`Registry.Open`(`registry.go:107`)`New(source)` 不传 cast**（`WithCast` 死代码）→ 补 opts 透传 + `Close()` + close-owner 归 recordLoop（B1） |
| relay close/done | `relay.go:106-115 Start/recordLoop`,`:235-256 Close/Done` | recordLoop 是 cast 唯一 writer，但 `Close()` 立即 `close(done)` 不等 recordLoop 退出 → cast.Close 与 recordLoop 写竞态（B1） |
| bytes 计数 | `relay.go:167 RecordedLen`=`ring.WrittenTotal()` | →bytes_out；**无 bytes_in**（`Viewer.SendInput`(`relay.go:278-293`) 未计）→ 补 relay 层计数（D-P3-8） |
| RelayBinding | `registry.go:35-42` | **无 cols/rows**（H2）；`Prepare`(`runner/worker/runner.go:163-170`) 未传尺寸 → 补 `Cols/Rows` |
| config CastConfig | `config/model.go:436-450`（P1 T2） | `RetentionTTLHours`/`Encryption{Enabled,KeyEnv}`；`castDefaultTTLHours=24`(**未用**)/`castMaxTTLHours=168`；loader fail-fast(`loader.go:339-344`) 两独立条；**无组合条**；**key 从不 os.Getenv**；`ApplyDefaults`(`loader.go:242-268`) 未设 cast TTL 默认 |
| httpapi Server config | `httpapi/server.go:90-103,213-223` | `Server.cfg` 是 `*config.ServerConfig`，**不含 `Storage.Cast`**（在 `config.Config.Storage.Cast`）→ 需装配处注入 sink factory（H1） |
| jobstore | `jobstore/store.go:59-239 schemaStmts`,`:290 applySchema`,`:304 migrate`；`jobs.go:33-99`（interactive 列 P1 T8） | 新表=追 `CREATE TABLE IF NOT EXISTS`（幂等）；**无 pty_sessions** |
| retention/prune | `jobstore/prune.go:57 PruneJobs`(返 `(deleted,prunedDirs)` 无 job_id!),`:142 PruneWorkflows`(删 step job 无 pty 删);`job/prune.go:28 Service.Prune`(RemoveAll dir);`serve/serve.go:267 startPruneLoop`(**仅 `ret.Enabled()` 起**);`config/model.go:471-477 RetentionConfig.Enabled`(不看 cast TTL) | cast sweep 接不到（B3）；job prune 删不了 pty_sessions 行（B4）；`PruneJobs`/`PruneWorkflows` 需在**同事务**删 pty_sessions |
| result_dir | `job/submit.go:91,161`；`<base>/<job_id>/`；`JobResult.ResultDir`；handler 已 `s.jobs.Get`(`pty_connect_handler.go:79`) | cast 落 `filepath.Join(res.ResultDir,"pty.cast")` |
| 授权/下载 | `attach_ticket_handler.go:76-85 callerMayAttach`(空 owner 仅 admin);`server.go:82 callerCanAdmin`;`artifact_handler.go:36-69`(Get→remote-409→SafeJoin→ServeFile) | recording gate 复用（H6 allow_empty 语义/M3 加密流式） |
| pty-connect handler | `pty_connect_handler.go:86-98`(Open→绑 source→`select{<-Relay.Done();<-ctx.Done()}`→defer Close) | serve 侧会话生命周期单点 → cast 建于 Open、pty_sessions 建/finalize 于此 |

## 2. 关键设计决策

### D-P3-1 cast sink：httpapi 建/持，**close owner 归 recordLoop**，`Done()`=cast 已封尾（修 B1/H5）
`ptyrelay` leaf 不碰文件/加密/config。→ cast sink 的 **concrete 实现在 httpapi**，由 `handlePtyConnect` 在 **Open 时**构造（有 result_dir + 注入的 cast 配置/key），经 `Registry.Open(nonce, source, ptyrelay.WithCast(sink))` 传入（sink 满足 leaf 接口 `castSink{ Write; Close }`）。**httpapi 保留 concrete sink 引用**，finalize 时查 `sink.Err()`（写失败→recording_uri 置空），ptyrelay 不知情（修 H5）。

**close owner = recordLoop**（修 B1）：cast 是 recordLoop 唯一 writer，故封尾也归它——
- `recordLoop` 顶部 `defer r.finish()`；`finish()`（`sync.Once`）= `if cast!=nil { cast.Close() }` 然后 `close(done)`。
- `Relay.Close()`：CAS `closed`，`src.Close()`（→ recordLoop 的 `src.Read` 报错→退出→`finish` 封尾+关 done），drop viewers，**不**自己 `close(done)`；若 relay **从未 Start**（无 recordLoop，如 pending 被 force-close）则 Close 直接调 `finish()`（Once 保证只一次）。
- ∴ `Relay.Done()` 语义**升级为「recordLoop 已退出 + cast 已封尾」**。P2 的 host `relayRegistry.Done(job)` 随之更强（等到 cast 封尾，仍受 `hostCancelGrace` 约束）；handler finalize 读到的是已封尾文件。
- cast.Write 失败：recordLoop 忽略错误继续（不阻断主链路），sink 内部置错误态；finalize 经 `sink.Err()` 得知。

### D-P3-2 `RelayBinding += Cols/Rows`（修 H2）
`RelayBinding` 加 `Cols,Rows int`；host runner `Prepare`（`runner/worker/runner.go:163`）填 `f.Cols/f.Rows`；**0 值 fallback 80×24**（对齐 pty runner 默认 `ptyrunner defaultCols/Rows`）。供 asciinema header `width/height` + pty_sessions 初始 cols/rows。

### D-P3-3 `pty_sessions` 一等表：httpapi 单点建/finalize，覆盖所有关闭路径（修 H3/H4）
表在 jobstore（D-P3-6 owned）；**写入方唯一 = httpapi `handlePtyConnect`**（runner 只 close relay、**绝不**写表，避免竞争）：
- **建行于 Open**（Upsert）：`pty_session_id`(from binding)/`job_id`/`worker_id`/`instance_id`/`owner`(=`JobResult.CallerID`)/`state=open`/`cols`/`rows`(from binding)/`recording_uri`(cast 启用则 `<result_dir>/pty.cast` 否则空)/`encrypted`/`bytes_in=0`/`bytes_out=0`/`started_at`。
- **finalize 流程（写死顺序，覆盖 relay.Done 与 ctx 提前返回）**：
  ```
  select { <-entry.Relay.Done() ; <-ctx.Done() }
  s.ptyRelays.Close(job, reason)   // 触发 recordLoop 退出→finish 封尾（若尚未）
  <-entry.Relay.Done()             // 等 cast 封尾（Done=封尾, D-P3-1）；已闭立即过
  Upsert(state=closed, ended_at, bytes_out=RecordedLen, bytes_in=InputLen,
         recording_uri = (sink.Err()!=nil ? "" : uri))
  ```
  ctx 提前取消也走同一 defer/尾段——不会在 cast 未封尾时写 closed。Upsert 幂等（PK）。
- **H4 取舍（显式）**：P3 **只记「已成功 Open 的 pty 会话」**。worker 从未拨入 / dial 失败 → relay 停 pending_worker、`handlePtyConnect` 从未执行 → **无 pty_sessions 行、无 cast**（该失败由 job 自身 status/error 记录）。`pty_sessions` 定义为「已建立 pty stream 的录制元数据」，未建立即无录制。更细审计（pending/failed 会话）留后续。
- 详情/列表暴露 = **P4**；P3 只落库 + 供 recording gate 查。

### D-P3-4 cast 加密 = framed AEAD（HKDF 派生 key + 认证 final frame，修 B2）
明文 asciinema 含用户键入（token/密码风险）。`Encryption.Enabled` 时 cast sink 外套加密：
- **key 派生**：`key = HKDF-SHA256(secret=os.Getenv(KeyEnv), salt=nil, info="gofer-pty-cast/v1")[:32]`（`crypto/hkdf`，stdlib，容任意 env secret、正确派生 256-bit；避免「短口令经裸 SHA256 变弱 AES-256」）。serve start 读 env，缺失 fail-fast。
- **文件格式（framed AEAD，防截断/重排/跨文件）**：
  - 头：`magic(4) "GFC1" + version(1) + fileID(16 随机)`（fileID 入 AAD 防跨文件拼接）。
  - 帧：`nonce(12 随机) + uint32 ct_len + ciphertext||tag`。`ct = AES-256-GCM.Seal(key, nonce, plaintext_chunk, AAD)`，`AAD = version || fileID || uint64(frame_index) || is_final(1)`。chunk ≤ 16KB，`frame_index` 从 0 递增。
  - **final frame**：`is_final=1`、plaintext=空、AAD 绑 `frame_index`(=总数据帧数)。写在所有数据帧之后（recordLoop finish 时）。
- **解密（gate）**：逐帧 `Open`，校验 `frame_index` 严格递增、AAD 匹配；遇认证失败/`frame_index` 跳变/**EOF 早于认证 final frame → 视为损坏（error，不返回半截）**。final frame 认证到达即正常 EOF。→ 截断（删尾帧）/追加裸帧/重排/跨文件均被拒。
- 全 stdlib，格式带 version 便于 §12 未来 key 轮换/算法升级。

### D-P3-5 config：装配处注入 cast 配置/key + 默认 TTL + 组合 fail-fast（修 H1）
- **注入（H1）**：`Server.cfg` 无 `Storage.Cast`。→ 在 `core/serve` 装配处（有完整 `config.Config`）构造 **cast recorder factory**（含已解析 key + 加密开关 + result_dir→sink 逻辑），经 `Server.SetCastRecorder(factory)` 注入 httpapi。handler 调 factory 而非直读 `s.cfg.Storage.Cast`。
- **默认 TTL 落地**：`ApplyDefaults`（`loader.go:242-268`）补 `RetentionTTLHours==0 → 24`。
- **组合 fail-fast（补缺口，K4）**：loader 加 `!Encryption.Enabled && effectiveCastTTL > castPlaintextMaxTTLHours(=castDefaultTTLHours=24)` → 报错（明文 cast 仅允许 ≤24h；长留存必须加密）。与现有两条不冲突（先算 effectiveTTL）。
- **运行期取 key**：装配/serve start 时 `Encryption.Enabled` → `os.Getenv(KeyEnv)`，空 → fail-fast。key 不入日志/Redis（SR403）。

### D-P3-6 cast retention = 两 regime，pty_sessions jobstore-owned（修 B3/B4）
1. **cast 独立 TTL sweep**（默认 24h）：jobstore 新 `PrunePtySessions(now)` 按 `ended_at + castTTL < now` 选行 → 返回其 `recording_uri`（cast 文件路径）供上层删文件 → 事务内删行。挂进 `Service.Prune()`。
   - **B3 修**：`serve.go:267 startPruneLoop` 启动条件从 `ret.Enabled()` 改为 `ret.Enabled() || castRecordingEnabled`（cast 开则 loop 必起，cast TTL 恒有默认）。启动日志更新。
2. **job/wf retention 连带**（长）：**B4 修**——`pty_sessions` 作 jobstore-owned row，在 `PruneJobs`/`PruneWorkflows` 的**同一事务**里 `DELETE FROM pty_sessions WHERE job_id IN (被删 job)`（不靠返回值/不靠 SQLite FK 级联）；cast 文件随 `os.RemoveAll(result_dir)` 连带删（现成）。
- 两 regime 任意序幂等（PK/存在性检查）；`recording_uri` 指向已删文件时 gate 404（§9 详情明示）。
- **interactive 不误判**：`PruneJobs` 按 terminal status + age/count 选 victim（`prune.go:44-102`），不依赖 result 文件存在性；interactive job（result_dir 仅 pty.cast、无 result.json）照常被选/删。

### D-P3-7 `GET /v1/jobs/{id}/pty/recording` gate（修 H6/M3）
authed `/v1` 组（`server.go:337+`），复用 `callerMayAttach(caller, job)`（owner + CanAdmin）否则 403。
- 取 job → 远端（`IsRemoteSource`）→ 409（仿 artifact）。
- `GetPtySession(job)` 取 `recording_uri`/`encrypted`；空或文件不存在（已 prune）→ 404（详情明示）。
- **明文**：`SafeJoinUnder`+`os.Stat`(regular)+`http.ServeFile`（复用 artifact 模式）。
- **加密（M3）**：**流式解密写 resp**，不用 `ServeFile`；**首帧认证 + header 校验在写 200/body 前完成**（失败 → 4xx/5xx，不发半截）；写 body 中途认证失败（截断）→ 断流 + 日志（已发 200 无法改 status）。`Content-Type: application/x-asciicast`。
- **H6 allow_empty 语义（保守，写死）**：录制敏感 → `callerMayAttach` 的「空 owner 仅 admin」保留；`allow_empty_token` 部署且无 configured admin → **录制下载禁用**（明确、测试固定）。
- 下载留**审计事件**（caller/job/session，复用 slog/notify）。

### D-P3-8 bytes_in/out（修 M2）
- bytes_out = `Relay.RecordedLen()`（现成）。
- bytes_in = relay 层新增 `bytesIn atomic`，`Viewer.SendInput` 中 `src.Write` 返回 `n>0` 时累加**实际接受字节**（只读 follower 被拒不计、多 viewer 不重复）；`Relay.InputLen()` 暴露。审计字段，写失败不影响会话。

## 3. 数据模型

**`pty_sessions`（jobstore SQLite，jobstore-owned）**：
```sql
CREATE TABLE IF NOT EXISTS pty_sessions (
  pty_session_id TEXT PRIMARY KEY,
  job_id         TEXT NOT NULL,
  worker_id      TEXT,
  instance_id    TEXT,
  owner          TEXT,                          -- job.CallerID（gate owner 闸；空=历史 allow_empty）
  state          TEXT NOT NULL,                 -- open | closed
  cols           INTEGER,
  rows           INTEGER,
  recording_uri  TEXT,                          -- <result_dir>/pty.cast；空=未录/写失败/已失效
  encrypted      INTEGER NOT NULL DEFAULT 2,    -- 1 是 2 否（SR301 从1起避0）
  bytes_in       INTEGER NOT NULL DEFAULT 0,
  bytes_out      INTEGER NOT NULL DEFAULT 0,
  started_at     INTEGER NOT NULL,              -- Unix 秒
  ended_at       INTEGER                        -- 空=未结束
);
CREATE INDEX IF NOT EXISTS idx_pty_sessions_job   ON pty_sessions(job_id);
CREATE INDEX IF NOT EXISTS idx_pty_sessions_ended ON pty_sessions(ended_at);
```
迁移：追加 `schemaStmts`（`store.go:59-239`），`IF NOT EXISTS` 幂等整表新增（无需 migrate 补列）。`PruneJobs`/`PruneWorkflows` 事务内加 `DELETE FROM pty_sessions WHERE job_id=?`（B4）。

**cast 文件** `<result_dir>/pty.cast`：
- 明文：asciinema v2——首行 header `{"version":2,"width":cols,"height":rows,"timestamp":started_at}`，随后每输出块一行 `[elapsed, "o", "<raw bytes JSON 转义>"]`。
- 加密：D-P3-4 framed AEAD 包裹上述明文字节流（magic 区分明/密）。

## 4. 关键流程

```txt
worker 拨入 pty-connect（P2）→ handlePtyConnect 校验通过:
  jobs.Get(job) 取 result_dir/CallerID
  cast 启用? → sink = castFactory(<result_dir>/pty.cast, cols/rows from binding, encrypt?)  [httpapi 持 sink]
  Registry.Open(nonce, remoteSource, WithCast(sink)) → relay open, recordLoop 首字节录
  UpsertPtySession(open, cols/rows/owner/recording_uri/encrypted/started_at)                [httpapi 写 jobstore]
  … recordLoop 每块→ ring + sink.Write + fanout；SendInput(n>0)→bytesIn+=n
  select{ <-Relay.Done() | <-ctx.Done() } → ptyRelays.Close → <-Relay.Done()(cast 封尾) →
    UpsertPtySession(closed, ended_at/bytes_out/bytes_in/recording_uri=(sink.Err?"":uri))   [finalize 单点]
  defer ptyRelays.Close（幂等）
   (recordLoop finish: defer cast.Close()封尾 → close(done); close owner=recordLoop, B1)

下载: GET /v1/jobs/{id}/pty/recording (authed)
  callerMayAttach 否则403 → remote?409 → GetPtySession.recording_uri 空/没文件?404(明示)
  encrypted? 流式解密(首帧认证后再发200; 中途截断→断流+日志) : SafeJoin+ServeFile → 审计事件

retention: startPruneLoop(ret.Enabled()||castEnabled) tick → Service.Prune():
  ① PrunePtySessions(ended_at+castTTL<now): 返 recording_uri → 删 pty.cast 文件 + 事务删行
  ② PruneJobs/PruneWorkflows: 同事务 DELETE pty_sessions WHERE job_id + RemoveAll(result_dir 含 pty.cast)
```

## 5. 安全（P3 重点）
- cast 默认加密**或**短 TTL；`!Encryption && TTL>24h` → loader fail-fast（D-P3-5）。
- key：`HKDF-SHA256(env)`；serve start 缺失 fail-fast；不入日志/Redis（SR403）。
- framed AEAD 认证 final frame + AAD 绑 fileID/frame_index → 防截断/重排/跨文件（D-P3-4）。
- recording gate：owner 或 can_admin；远端 409；已 prune 404；allow_empty 无 admin→禁下载（保守，H6）；下载留审计。加密解密失败不发半截（M3）。
- cast 写失败降级（sink 内部记错、recording_uri 置空），不阻断主链路、不双写主日志。
- pty.cast 随 job retention 连带清（无孤儿密文超期，B4）。

## 6. 待确认（plan 内细化）
- framed AEAD chunk 大小（拟 16KB）+ magic/version 具体字节 + 测试向量（含截断/追加/重排负测）。
- `castPlaintextMaxTTLHours` 复用 `castDefaultTTLHours(24)` 还是独立常量（拟复用）。
- resize 后 current cols/rows 是否回写 pty_sessions（P3 拟只记初始）。
- 审计事件载体（slog vs notify vs 新表）对齐现有。
- `SetCastRecorder` factory 的确切签名与 core/serve 装配落点。
- startPruneLoop enable gate 改动对现有「无 retention 配置」部署的行为影响（原不起 loop，现 cast 开则起）——确认无非预期副作用。

---
> 评审关注点（v0.2）：**D-P3-1** `Done()`=cast 封尾后，P2 host `relayRegistry.Done` 语义变强是否影响已实施的 P2 取消时序（grace 内多等封尾）；`finish` 的 `sync.Once` 覆盖「从未 Start 的 pending relay force-close」路径；**D-P3-4** framed AEAD 的 AAD/final-frame 是否真防全部截断+重排+跨文件（要测试向量）+ `crypto/hkdf` API 用法；**D-P3-6** `PruneJobs`/`PruneWorkflows` 事务内删 pty_sessions 的实现（是否需改这两函数签名/事务边界）+ enable gate 改动副作用；**H4** 未拨入无 session 行的取舍是否被 P4 前端依赖。
