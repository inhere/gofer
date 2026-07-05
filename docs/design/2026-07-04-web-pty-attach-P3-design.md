# WEB-03 P3：cast 录制 + pty_sessions 表 + retention + recording gate 设计细化

> 上游：主设计 [`2026-07-03-web-pty-attach-design.md`](2026-07-03-web-pty-attach-design.md) v0.8（D3/K4/§8/§9/§10/§11/§14 P3）；P2 设计 v0.4（已实施收官 GREEN）；P3 评审 [`../review/2026-07-04-web-pty-attach-P3-codex-review.md`](../review/2026-07-04-web-pty-attach-P3-codex-review.md)。
> P3 = **给 relay 接上 cast 录制（asciinema v2 + framed AEAD 加密 + retention）+ 建 `pty_sessions` 一等表 + `GET /pty/recording` 下载 gate**。P0–P2 的 pty attach 端到端已完成。
> 铁律 G022/G024：`ptyrelay` 保 **leaf（stdlib only）**，不 import jobstore/job/config——cast sink **concrete 实现 + pty_sessions 持久化都在 httpapi**（经 `writeCloser` 接口注入 relay）。G031 不引第三方 module（加密全 stdlib：`crypto/hkdf`(Go1.24+)/`aes`/`cipher`/`sha256`/`rand`，已验证 go1.25.10 可用）。

## 修订记录
| 版本 | 日期 | 说明 |
|---|---|---|
| v0.1 | 2026-07-04 | 初稿 8 决策。 |
| **v0.3** | 2026-07-04 | **codex round-2 收敛（1 阻断+2 高+2 中）**：① **B3 阻断** 加 `storage.cast.enabled`（默认 false 显式启用录制）——`RetentionTTLHours=0` 语义澄清、prune loop gate 用之（D-P3-5/6）；② **B1 高** cast `Close` **bounded**（不阻塞 P2 完成路径）+ P2 回归测试（D-P3-1）；③ **B2 高** framed AEAD 固化：`hkdf.Key(sha256.New,...)` 精确调用 + **counter nonce**（per-file prefix+frame_index，防复用）+ 高熵 secret 约束 + final 后 trailing bytes=损坏 + 测试向量清单（D-P3-4）；④ **中** cast TTL sweep **只清 `recording_uri`/标失效、保留 session 行**（存审计），删行只在 job/wf retention（D-P3-6）；⑤ **中** 下载 200/半截口径统一（首帧+header 认证在 200 前；中途损坏只能断流+日志，D-P3-7/§5）。**round-3 终审=GO（唯一阻断=`hkdf.Key` 签名）已修**：`hkdf.Key` Go1.25 返回 `(key,err)`（codex `go run` 实证）→ 改双返回值+err fail-fast；顺带改**每文件派生 key**（fileID salt）彻底消 nonce 跨文件复用。 |
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
- `recordLoop` 顶部 `defer r.finish()`；`finish()`（`sync.Once`）= `if cast!=nil { boundedClose(cast) }` 然后 `close(done)`。
- `Relay.Close()`：CAS `closed`，`src.Close()`（→ recordLoop 的 `src.Read` 报错→退出→`finish` 封尾+关 done），drop viewers，**不**自己 `close(done)`；若 relay **从未 Start**（无 recordLoop，如 pending 被 force-close）则 Close 直接调 `finish()`（Once 保证只一次）。
- **bounded close（修 B1 高 / P2 兼容）**：cast `Close` **不得无界阻塞**（写 final frame + 文件 close，均本地快操作）；`finish` 的 `boundedClose` 在 goroutine 里跑 cast.Close、`select{ done | time.After(castCloseGrace ~2s) }`，超时 → 置 `sink.Err()`（recording 视为失败）**并仍 `close(done)`**——保证 `Done()` **恒在 bound 内触发**，不把磁盘 hang 带进 P2 完成路径。
- ∴ `Relay.Done()` 语义**升级为「recordLoop 已退出 + cast 已封尾（或封尾超时置错）」**。P2 的 host `relayRegistry.Done(job)` 随之略强（多等 ≤castCloseGrace 的封尾，仍在 `hostCancelGrace=10s` 内）；handler finalize `<-Done()` 因 finish 恒关 done 而不会永久等。**P2 回归测试**（plan）：cast.Close 人为阻塞 → host `Run` 仍在 grace 内返回、recording 置失败（非半截 URI）、ctx cancel 路径同样收敛。
- cast.Write 失败：recordLoop 忽略错误继续（不阻断主链路），sink 内部置错误态；finalize 经 `sink.Err()` 得知（含 boundedClose 超时）。

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
明文 asciinema 含用户键入（token/密码风险）。`Encryption.Enabled` 时 cast sink 外套加密（全 stdlib）：
- **key 派生（Go1.25 精确调用，`hkdf.Key` 返回 `(key, error)`——务必收 err）**：
  - master key（serve start 一次）：`mk, err := hkdf.Key(sha256.New, []byte(os.Getenv(KeyEnv)), nil, "gofer-pty-cast/v1", 32)`；`err!=nil` → fail-fast。
  - **每文件 key**（消除跨文件 nonce 复用，评审加固）：`fk, err := hkdf.Key(sha256.New, mk, fileID, "gofer-pty-cast-file/v1", 32)`（fileID 作 salt → **每文件独立 key**，counter nonce 即使跨文件同值也不复用同一 key，从根上消 4B prefix 碰撞隐患）。
  - **secret 必须高熵**（HKDF **非** password KDF）：约定 `KeyEnv` 值为 **base64/hex 编码的 ≥32B 随机密钥**，loader/start 校验解码后长度 ≥32B，否则 fail-fast（拒绝把短口令当 256-bit 用）。缺失 → fail-fast。
- **文件格式（framed AEAD，防截断/重排/跨文件/追加）**：
  - 头：`magic(4)="GFC1" + version(1) + fileID(16 随机)`（fileID 既作 per-file key salt 又入 AAD 防跨文件拼接）。
  - 帧：`uint32 ct_len + ciphertext||tag`。`ct = AES-256-GCM.Seal(fk, nonce, plaintext_chunk, AAD)`。
    - **counter nonce**：`nonce(12) = uint32BE(0) || uint64BE(frame_index)`（前 4B 0 padding + 8B 计数）；`frame_index` 从 0 严格递增、uint64 不回绕。因 **fk 每文件唯一**，counter-only nonce 无跨文件复用。
    - `AAD = magic || version || fileID || uint64BE(frame_index) || is_final(1) || uint32BE(plaintext_len)`。chunk ≤ 16KB。
  - **final frame**：`is_final=1`、plaintext=空（`Seal(nonce, nil, aad)` 合法）、AAD 的 `frame_index`=总数据帧数。写在所有数据帧后（recordLoop finish）。
- **解密（gate）**：逐帧 `Open` 校验 AAD（含 frame_index 严格递增）；遇认证失败/frame_index 跳变/**EOF 早于认证 final frame → 损坏（error）**；**认证 final frame 后必须物理 EOF**，其后**任何 trailing 字节 = 损坏**（拒「追加旧认证帧/垃圾」）。
- **测试向量清单（进 plan）**：正常往返、截断（删尾帧/删 final）、final 后追加、追加裸帧、帧重排、跨文件 fileID 替换、单字节篡改（tag 失败）、noncePrefix 不同文件、空会话（仅 header+final）。
- 格式带 version 便于 §12 未来 key 轮换/算法升级。

- **cast 录制总开关（修 B3 阻断）**：`CastConfig` 加 **`Enabled bool yaml:"enabled"`**（默认 `false`——**录制 opt-in**，未配置=不录）。`castRecordingEnabled := cfg.Storage.Cast.Enabled` 是全设计唯一「是否录制」判据（handler 建 sink、prune loop gate、pty_sessions.recording_uri 都据此）。
  - `RetentionTTLHours` 语义澄清：**仅当 `Enabled`** 才有意义；`Enabled && RetentionTTLHours==0` → 默认 24h（`ApplyDefaults` 落地）；`Enabled==false` → 不录、TTL 无关。（不再用 `RetentionTTLHours=0` 表达禁用——由 `Enabled` 表达。）
- **注入（H1）**：`Server.cfg` 无 `Storage.Cast`。→ `core/serve` 装配处（有完整 `config.Config`）构造 **cast recorder factory**（`Enabled` 时含已解析 key + 加密开关 + result_dir→sink 逻辑；`Enabled==false` → factory nil）经 `Server.SetCastRecorder(factory)` 注入。handler：factory==nil 则不录（`recording_uri` 空）。**mcp/测试直建 `httpapi.New` 时 factory nil = 禁录默认**。
- **默认 TTL 落地**：`ApplyDefaults`（`loader.go:242-268`）补 `Cast.Enabled && RetentionTTLHours==0 → 24`。
- **组合 fail-fast（补缺口，K4）**：loader 加 `Cast.Enabled && !Encryption.Enabled && effectiveCastTTL > castPlaintextMaxTTLHours(=castDefaultTTLHours=24)` → 报错（明文 cast 仅允许 ≤24h；长留存必须加密）。与现有两条不冲突（先算 effectiveTTL）。
- **运行期取 key + 高熵校验**：装配/serve start 时 `Cast.Enabled && Encryption.Enabled` → `os.Getenv(KeyEnv)` 解码校验 ≥32B（D-P3-4），空/过短 → fail-fast。key 不入日志/Redis（SR403）。

### D-P3-6 cast retention = 两 regime，pty_sessions jobstore-owned（修 B3/B4）
1. **cast 独立 TTL sweep**（默认 24h）：jobstore 新 `ExpireCastRecordings(now)` 按 `ended_at + castTTL < now AND recording_uri!=''` 选行 → 返回其 `recording_uri`（cast 文件路径）供上层删文件 → 事务内 **`UPDATE pty_sessions SET recording_uri='', encrypted=2 WHERE ...`（只清录制字段、`state` 加/记 `expired` 标或保留 closed，**保留整行**——存 owner/state/bytes/started/ended 审计元数据，P4 详情可显示「已录制但已过期清理」）**。挂进 `Service.Prune()`。
   - **B3 修**：`serve.go:267 startPruneLoop` 启动条件从 `ret.Enabled()` 改为 `ret.Enabled() || cfg.Storage.Cast.Enabled`（cast 录制开则 loop 必起）。启动日志更新。**副作用确认**（§6）：原无 retention 配置的部署，开 cast 录制后 prune loop 会启动——但只跑 cast 过期（job/wf prune 各自 `Enabled()` gate 内仍不动），无非预期删 job。
2. **job/wf retention 连带**（长，删整行）：**B4 修**——`pty_sessions` 作 jobstore-owned row，在 `PruneJobs`/`PruneWorkflows` 的**同一事务**里 `DELETE FROM pty_sessions WHERE job_id IN (被删 job)`（这两函数已有事务且删 interactions/job_events 等，加一句 DELETE，不改返回签名）；cast 文件随 `os.RemoveAll(result_dir)` 连带删（现成）。
- 两 regime 任意序幂等：cast 先过期（regime1 清 uri+删文件）→ job prune 删行 no-op（行已无 uri）；job 先删→ regime1 查不到行。`recording_uri` 空/文件已删时 gate 404（§9 详情明示）。
- **interactive 不误判**：`PruneJobs` 按 terminal status + age/count 选 victim（`prune.go:44-102`），不依赖 result 文件存在性；interactive job（result_dir 仅 pty.cast、无 result.json）照常被选/删。

### D-P3-7 `GET /v1/jobs/{id}/pty/recording` gate（修 H6/M3）
authed `/v1` 组（`server.go:337+`），复用 `callerMayAttach(caller, job)`（owner + CanAdmin）否则 403。
- 取 job → 远端（`IsRemoteSource`）→ 409（仿 artifact）。
- `GetPtySession(job)` 取 `recording_uri`/`encrypted`；空或文件不存在（已 prune）→ 404（详情明示）。
- **明文**：`SafeJoinUnder`+`os.Stat`(regular)+`http.ServeFile`（复用 artifact 模式）。
- **加密（M3，口径统一）**：**流式解密写 resp**，不用 `ServeFile`。**在写 200/任何 body 前**先解密+认证**文件 header + 首帧**：失败 → 返回 4xx/5xx（此时尚未发 200，绝不半截）。首帧通过后发 200 开始 stream；**中途帧认证失败（截断/篡改）→ 只能中断 stream + 记日志**（200 已发无法改 status），由客户端校验/提示。`Content-Type: application/x-asciicast`。
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
- recording gate：owner 或 can_admin；远端 409；已 prune/uri 空 404；allow_empty 无 admin→禁下载（保守，H6）；下载留审计。加密下载 **header+首帧认证在发 200 前**（失败即 4xx，不半截）；发 200 后中途损坏只能断流+日志（M3，口径见 D-P3-7）。
- cast 写失败降级（sink 内部记错、recording_uri 置空），不阻断主链路、不双写主日志。
- pty.cast 随 job retention 连带清（无孤儿密文超期，B4）。

## 6. 待确认（plan 内细化）
- `castCloseGrace`（拟 2s）+ framed AEAD chunk 大小（拟 16KB）具体常量。
- `castPlaintextMaxTTLHours` 复用 `castDefaultTTLHours(24)` 还是独立常量（拟复用）。
- resize 后 current cols/rows 是否回写 pty_sessions（P3 拟只记初始）。
- 审计事件载体（slog vs notify vs 新表）对齐现有。
- `SetCastRecorder` factory 的确切签名与 core/serve 装配落点（`Enabled==false`→nil）。
- cast TTL 过期后 `state` 是否加 `expired` 值还是仅清 `recording_uri`（拟仅清 uri，`state` 保持 closed；P4 据 uri 空判「已清理」）。
- **配置迁移（plan 落）**：`Cast.Enabled` 默认 false = 录制 opt-in——已配 `storage.cast.encryption/ttl` 但未写 `enabled` 的部署将**静默不录**；plan 需更新示例配置/测试预期 + 文档说明「开录制须 `storage.cast.enabled: true`」。

---
> 评审关注点（v0.3）：**D-P3-1** `boundedClose`（castCloseGrace 超时仍 close done）是否真保证 `Done()` 恒在 bound 内、不回归 P2 取消时序；**D-P3-4** counter nonce（noncePrefix||frame_index）唯一性 + final-frame/trailing-bytes 规则 + `hkdf.Key(sha256.New,...)` 精确 API + 高熵 secret 校验是否够；**D-P3-5** `Cast.Enabled` opt-in 语义 + factory nil 禁录默认（mcp/测试）是否自洽；**D-P3-6** cast TTL 过期「保留行清 uri」vs job retention「删行」的两 regime 幂等 + `PruneJobs`/`PruneWorkflows` 事务内加 DELETE 的改动面。
