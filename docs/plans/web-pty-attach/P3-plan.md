# P3：cast 录制 + pty_sessions 表 + retention + recording gate 实施计划

> 上游：设计 [`../../design/2026-07-04-web-pty-attach-P3-design.md`](../../design/2026-07-04-web-pty-attach-P3-design.md) **v0.3（codex round-3 = GO）**；评审 [`../../review/2026-07-04-web-pty-attach-P3-codex-review.md`](../../review/2026-07-04-web-pty-attach-P3-codex-review.md)（3 轮）。P2 已收官 GREEN。
> 铁律：G022/G024（`ptyrelay` 保 leaf；cast concrete sink + pty_sessions 持久化在 httpapi/新包，不入 ptyrelay）；G031 加密全 stdlib（`crypto/hkdf` Go1.24+/`aes`/`cipher`/`sha256`/`rand`）；G023（非交互 + 未开录制路径零变化）。每 T 容器 `go test ./...` 绿即提交（前缀 `feat(pty)`/`fix(pty)`，Co-Authored-By，**不 push**）。

## 任务依赖
```
T0(config Enabled+校验) ─┐
T1(ptyrelay: Close接口+close owner+Binding Cols/Rows+bytesIn) ─┐
T2(internal/castrec: asciinema+framed AEAD) ─┬─────────────────┼─▶ T4(factory+prune gate wiring) ─▶ T5(handler cast+pty_sessions) ─┐
T3(jobstore pty_sessions表+prune) ───────────┴──────────────────┘                                    T6(recording endpoint) ─┴─▶ T7(e2e+P2回归+环检)
```
进度（**全完成 2026-07-05 SUPMODE，全绿未 push**）：
- [x] T0 config：`Cast.Enabled` + 默认 TTL + 组合 fail-fast + secret 校验 — `2f67799`
- [x] T1 ptyrelay：`CastSink` 补 `Close` + close owner 归 recordLoop（bounded, Done=封尾）+ `RelayBinding.Cols/Rows` + bytesIn + `Open(opts...)` — `8ab626b`
- [x] T2 `internal/castrec`：asciinema v2 + framed AEAD（per-file HKDF key + counter nonce + final frame）+ 测试向量（5 类负测全过） — `cfc4e3c`
- [x] T3 jobstore：`pty_sessions` 表 + Store + `ExpireCastRecordings` + `PruneJobs/PruneWorkflows` 事务删行 — `c38bd98`
- [x] T4 serve/core：factory + `SetCastRecorder`/`SetPtySessionStore` + prune gate + `Service.Prune` cast sweep — `937537e`
- [x] T5 httpapi handler：Open 建 sink + Upsert(open) + finalize 单点 — `d76ecda`
- [x] T6 httpapi：`GET /pty/recording` gate（授权/解密流式首帧认证/审计） — `bb61fdd`
- [x] T7 e2e（录制往返明文+加密/两 regime/授权）+ P2 零回归 + 配置迁移 + 环检 — `8b9c9b7`
- [x] **fix**：recording gate 去 remote-409（cast 恒 hub-local，T7 实证 bug） — `12806b1`（设计 D-P3-7 修 `0696be7`）
- [x] **收官审查（codex 第五轮真实 diff）**：crypto(castrec framed AEAD)=GREEN（10 测试向量全过，无绕过/截断/nonce 复用）；唯一高 H1（boundedCastClose 超时未回传失败态）**已修** `d4c2432`（`Relay.CastClosedCleanly()` + finalize OR 判据清 recording_uri，D-P3-1 机制修正 `ceca08b`）

> **实施结果**：`go build`/`vet`/`test ./...`（Linux 真 pty）全绿；`-race`（castrec/jobstore/ptyrelay/httpapi/worker/runner）绿；`GOOS=windows build` 绿；`go list -deps`：ptyrelay leaf、job 无 pty/ptyrelay/castrec（环检 PASS）。P2 零回归逐包确认。castrec 5 类加密负测全过。**T7 实证抓出并修复 recording gate remote-409 bug**（主控拍板设计修正）。

---

## T0 — config：`Cast.Enabled` + 默认 TTL + 组合 fail-fast + secret 校验（B3/D-P3-5）

**触碰**：
1. `internal/config/model.go:442` `CastConfig` 加 `Enabled bool yaml:"enabled"`（默认 false，录制 opt-in）。
2. `internal/config/loader.go:242-268` `ApplyDefaults`：`if c.Storage.Cast.Enabled && c.Storage.Cast.RetentionTTLHours==0 { =castDefaultTTLHours(24) }`。
3. `internal/config/loader.go:339-344` 校验扩展（仅 `Cast.Enabled` 时）：
   - 组合：`!Encryption.Enabled && effectiveTTL > castPlaintextMaxTTLHours(=24)` → err（明文仅短 TTL）。
   - secret：`Encryption.Enabled` → `KeyEnv` 非空（现有）+ **新增运行期**（serve start）解码 `os.Getenv(KeyEnv)`（base64 或 hex）长度 ≥32B，否则 err（拒短口令当 256-bit）。
4. 常量 `castPlaintextMaxTTLHours=24`（复用 `castDefaultTTLHours`）。
5. `config_handler.go` governanceView/脱敏：`cast` 视图不回显 key（仅 enabled/ttl/encryption.enabled）。

**验收**：config 单测——`enabled=false`→不校验 cast、TTL 无关；`enabled=true`+明文+TTL>24→err；`enabled=true`+加密+key 名空→err；`ApplyDefaults` 补 24；现有 config 测试零回归（未写 enabled 的旧 fixture 解出 `Enabled=false`）。

---

## T1 — ptyrelay：close owner 归 recordLoop + Binding Cols/Rows + bytesIn（B1/H2/H5/D-P3-8）

> **最敏感**：改 relay close 语义（`Done()`=cast 封尾），影响已实施 P2。全程守 P2 零回归。

**触碰** `internal/ptyrelay/relay.go`：
1. `writeCloser`（`:74-76`）改名/扩为 leaf 友好接口（保持 ptyrelay 不 import 上层）：
```go
// CastSink 是 cast 录制端；ptyrelay 只 Write/Close，不知加密/文件（concrete 在 httpapi/castrec）。
type CastSink interface {
	io.Writer
	Close() error
}
```
（`WithCast(CastSink)` 保持；relay 只调 `Write`/`Close`；错误态由 httpapi 持 concrete sink 查，ptyrelay 不暴露 Err——H5 在 T5 落）。
2. **close owner 归 recordLoop**（B1）：
```go
// recordLoop 顶部
defer r.finish()
// finish: cast 封尾 + 关 done，只一次（sync.Once）。close owner 归此，消 cast.Close 与 Write 竞态。
func (r *Relay) finish() {
	r.finishOnce.Do(func() {
		if r.cast != nil { r.boundedCastClose() } // bounded, 见下
		close(r.done)
	})
}
// boundedCastClose: cast.Close 在 goroutine 跑, select grace; 超时保证 Done 有界。
// 超时→r.castClean=false(收官审查 H1: 回传失败态供 finalize 清 recording_uri)。
func (r *Relay) boundedCastClose() {
	done := make(chan struct{})
	go func() { _ = r.cast.Close(); close(done) }()
	select {
	case <-done: // grace 内干净封尾, castClean 保持 true
	case <-time.After(castCloseGrace): r.castClean.Store(false) // 超时未封尾→录制失败
	}
}
// CastClosedCleanly: true=grace 内干净封尾; false=超时未封尾。finalize(httpapi)据此清 recording_uri。
// 初始 true(无 cast 或干净封尾); ptyrelay 只报自己结果, 不写 concrete sink.Err(leaf 不破)。
func (r *Relay) CastClosedCleanly() bool { return r.castClean.Load() }
```
3. `Close()`（`:235-253`）改：CAS closed + `src.Close()` + drop viewers，**不** `close(done)`；若 **从未 Start**（无 recordLoop）→ 直接 `r.finish()`（Once 保证）。→ `Done()` 语义=recordLoop 退出 + cast 封尾(或超时)。
4. bytesIn（D-P3-8）：`Relay` 加 `bytesIn atomic.Int64`；`Viewer.SendInput`（`:278-293`）`src.Write` 返回 `n>0` 时 `r.bytesIn.Add(int64(n))`；`Relay.InputLen() int64`。
5. 常量 `castCloseGrace = 2 * time.Second`（var 便于测试 shrink）。

**触碰** `internal/ptyrelay/registry.go`：
6. `RelayBinding`（`:34-42`）加 `Cols, Rows int`。
7. `Open`（`:107`）签名 `Open(nonce string, source PtySource, opts ...Option)`；内 `New(source, opts...)`（`:130`）。

**触碰** `internal/runner/worker/runner.go:163-170`：`Prepare` 的 `RelayBinding{}` 填 `Cols: f.Cols, Rows: f.Rows`（0 值不变，fallback 在 sink 侧按 80×24）。

**验收**：
- ptyrelay 单测：`finish` 恒关 done（有 cast/无 cast/从未 Start/boundedClose 超时 各路径）；cast.Write 与 Close 不并发（fake sink 记录调用序，断言所有 Write 早于 Close）；`Done()` 在 boundedClose 超时后仍关（fake Close 阻塞→Done 在 grace 内触发）；`InputLen` 计数（SendInput n>0 累计、只读 viewer 不计）；`WithCast` 经 `Open(opts)` 生效（消死代码，M1）。
- `-race` 绿。
- **P2 零回归**（关键）：现有 `ptyrelay`/`runner/worker` 测试全绿；专补 `runner/worker` 回归——interactive result 后 host `Run` 等 `Done()`，cast.Close 人为阻塞 → `Run` 仍在 `hostCancelGrace` 内返回。

---

## T2 — `internal/castrec`：asciinema v2 + framed AEAD 封套（B2/D-P3-4）

**新包** `internal/castrec`（httpapi 导入；满足 ptyrelay `CastSink`）。纯 stdlib。

**触碰**：
1. `asciicast.go`：明文 writer——首行 header `{"version":2,"width":cols,"height":rows,"timestamp":startedAt}`，`Write(p)` 追加事件行 `[elapsed,"o",string(p)]`（JSON 转义 raw bytes）；`Close` flush。`startedAt`/`elapsed` 由注入的时钟（测试可控，避免 `time.Now` 不可测——传 `now func() time.Time`）。
2. `envelope.go`：framed AEAD（加密时包裹 asciicast writer）：
```go
// key 派生（master 一次 / 每文件）——hkdf.Key 返回 (key, err)！
func deriveMaster(secret []byte) ([]byte, error) { return hkdf.Key(sha256.New, secret, nil, "gofer-pty-cast/v1", 32) }
func deriveFileKey(mk, fileID []byte) ([]byte, error) { return hkdf.Key(sha256.New, mk, fileID, "gofer-pty-cast-file/v1", 32) }
// 文件: magic"GFC1"(4)+version(1)+fileID(16 rand)
// 帧: uint32 ct_len + GCM.Seal(fk, nonce, chunk, aad)
//   nonce(12)=uint32BE(0)||uint64BE(frameIdx)  (fk 每文件唯一→counter-only 安全)
//   aad = magic||version||fileID||uint64BE(frameIdx)||isFinal(1)||uint32BE(ptLen)
//   final frame: isFinal=1, 空明文 Seal(fk,nonce,nil,aad), 写在所有数据帧后(Close)
```
   - `EncWriter`：`Write` 累积到 ≤16KB 成帧加密写盘 + frameIdx++；`Close` 写 final frame + 关文件；`Err()` 暴露写错误态。
   - `DecReader`（gate 用）：逐帧 `Open` 校验 aad（frameIdx 严格递增）；EOF-before-authenticated-final=err；authenticated final 后有 trailing bytes=err。
3. `factory.go`：`Recorder` factory——`New(cfg CastConfig, resolvedKey []byte) *Recorder`；`Recorder.Open(path string, cols,rows int, startedAt int64) (CastSink, error)`（enabled+encrypt→EncWriter(asciicast)；enabled 明文→asciicast；文件创建失败→err）。

**验收**（**测试向量必做**）：
- 明文往返：写 N 块→读回 asciicast header+events 正确。
- 加密往返：EncWriter→DecReader 得回原字节。
- **负测（安全）**：截断（删尾帧/删 final）→DecReader err；final 后追加字节→err；追加裸帧→err；帧重排→err；跨文件 fileID 替换（拿 A 的 fk 解 B 帧）→err；单字节篡改→GCM tag err；空会话（仅 header+final）→往返 OK。
- `hkdf.Key` 双返回值编译通过（Go1.25）；`go test -race` 绿。

---

## T3 — jobstore：`pty_sessions` 表 + Store + prune（B4/D-P3-3/6）

**触碰**：
1. `internal/jobstore/store.go:59-239 schemaStmts` 追加（设计 §3 建表 SQL + 两 index），`IF NOT EXISTS` 幂等。
2. 新 `internal/jobstore/pty_sessions.go`：`PtySessionRecord` + `UpsertPtySession(rec)`（PK 冲突 upsert）+ `GetPtySessionByJob(jobID)`（gate 用；取最近一条 open/closed）+（P4 用）`ListPtySessionsByJob`。
3. `ExpireCastRecordings(now, ttlSeconds int64) (uris []string, err error)`（regime1，**签名统一：调用方传 TTL，store 不持 config**）：事务内 `SELECT recording_uri ... WHERE recording_uri!='' AND ended_at IS NOT NULL AND ended_at+ttlSeconds<now` → 返回 uris → `UPDATE pty_sessions SET recording_uri='', encrypted=2 WHERE ...`（**保留行**）。
4. `PruneJobs`（`prune.go:73-94` 事务）+ `PruneWorkflows`（`:180-213` 事务）：各在删 job owned rows 处加 `DELETE FROM pty_sessions WHERE job_id=?`（对每个被删 job_id；不改返回签名）。

**验收**：jobstore 单测——Upsert/Get 往返；旧库 `Open` 自动建表（迁移幂等）；`ExpireCastRecordings` 只清过期行的 uri 且保留行、返回 uris；`PruneJobs`/`PruneWorkflows` 删 job 连带删 pty_sessions 行（同事务）；两 regime 任意序幂等；现有 prune 测试零回归。

---

## T4 — serve/core：factory 注入 + prune gate + cast sweep 接线（D-P3-5/6/H1）

**触碰**：
1. `internal/httpapi/server.go` `Server` 加 `castRecorder *castrec.Recorder` + `SetCastRecorder(...)`（nil=禁录）**和** `ptySessions PtySessionStore` + `SetPtySessionStore(...)`（T5 用；nil-safe）。
2. `internal/core/core.go:75+` 或 `internal/serve/serve.go:100`（有完整 `config.Config` + `Core.Store`）：
   - `Cast.Enabled` → 解析 key（T0 校验 + `os.Getenv`→decode）→ `castrec.New(cfg.Storage.Cast, key)` → `srv.SetCastRecorder(rec)`；`Enabled==false`→不注入（nil）。key 解析失败→serve 启动 fail-fast。
   - `srv.SetPtySessionStore(core.Store)`（`*jobstore.Store` 满足 `PtySessionStore`）。
3. `internal/serve/serve.go:267-270 startPruneLoop`：**改签名带 cast**——`startPruneLoop(c, jobs, ret, castEnabled bool, castTTLSec int64, stop)`（现签名 `(c,jobs,ret,stop)` 拿不到 cast 配置）；gate `if ret.Enabled() || castEnabled { … }`；loop 内 `jobs.Prune()` 已跑 cast sweep（见 4）。启动日志加 cast。
4. `internal/job/prune.go:28 Service.Prune`：Service 加 `castTTLSec int64` 字段（装配注入）或 `Prune(castTTLSec)` 传参；`store.ExpireCastRecordings(now, castTTLSec)` → 对返回 uris `os.Remove(uri)`（best-effort 删 cast 文件）。**签名与 T3 一致**。

**验收**：装配单测——`Enabled=true` 有效 key → recorder 注入；无 key → serve 启动 err；`Enabled=false` → recorder nil、prune gate 若无 retention 则仍按原不启动；`Enabled=true` 无 retention → prune loop 启动（只跑 cast sweep，不动 job/wf）。`Service.Prune` 删过期 cast 文件 + 清 uri。

---

## T5 — httpapi handler：Open 建 sink + pty_sessions 建/finalize（D-P3-1/3）

**触碰** `internal/httpapi/pty_connect_handler.go:86-98`（在校验通过、`s.jobs.Get` 后）：
```go
// Open 前: 建 cast sink（factory nil / 非 interactive-cast → nil sink）
var sink castrec.CastSink; var recURI string; encrypted := false
if s.castRecorder != nil {
	res, _ := s.jobs.Get(binding.JobID) // result_dir
	uri := filepath.Join(res.ResultDir, "pty.cast")
	if cs, err := s.castRecorder.Open(uri, binding.Cols, binding.Rows, startedAt); err == nil {
		sink = cs; recURI = uri; encrypted = s.castRecorder.Encrypted()
	} // 建失败→不录, recURI 空(降级)
}
var opts []ptyrelay.Option
if sink != nil { opts = append(opts, ptyrelay.WithCast(sink)) }
entry, err := s.ptyRelays.Open(hello.RelayNonce, source, opts...)
// ... 现有校验 ...
s.upsertPtySession(open: id/job/worker/instance/owner=res.CallerID/cols/rows/recURI/encrypted/startedAt)
defer func() {
	s.ptyRelays.Close(binding.JobID, "pty_ws_closed")
	<-entry.Relay.Done() // Done=封尾(T1); finish 恒关→不永久等
	uriFinal := recURI
	if (sink != nil && sink.Err() != nil) || !entry.Relay.CastClosedCleanly() { uriFinal = "" } // 写失败/封尾超时→uri 空(H5+收官H1)
	s.upsertPtySession(closed: ended_at/bytes_out=entry.Relay.RecordedLen()/bytes_in=entry.Relay.InputLen()/recording_uri=uriFinal)
}()
select { case <-entry.Relay.Done(): case <-ctx.Done(): }
```
- **runner 不写 pty_sessions**（单点=httpapi）。
- **store 句柄（修评审高1）**：`httpapi.Server` **当前无 store 引用**（只有 `jobs *job.Service`，`job.Service.meta` 私有）。→ 给 Server 注入**窄接口**（装配处传 `*jobstore.Store` 满足）：
```go
// httpapi 定义, *jobstore.Store 实现
type PtySessionStore interface {
	UpsertPtySession(rec jobstore.PtySessionRecord) error
	GetPtySessionByJob(jobID string) (jobstore.PtySessionRecord, bool, error)
}
// Server 加字段 ptySessions PtySessionStore + SetPtySessionStore(...) 或 New 参; nil-safe(不建行)
```
  handler 用 `s.ptySessions.UpsertPtySession(...)`（**非** `s.store`）。装配注入见 T4。
- `sink.Err()`：concrete sink（castrec）暴露 `Err()`（含 boundedClose 超时），ptyrelay 不知情。
- `startedAt`：`time.Now().Unix()`（handler 侧）。

**验收**：httpapi 单测（`httptest`+fake worker 拨入，复用 P2 pty_connect 测试骨架）——录制启用→cast 文件生成+pty_sessions 行(open→closed, bytes 记录)；cast.Close 阻塞→finalize 不永久等、recording_uri 置空；ctx 提前取消→仍 finalize closed；factory nil→无 cast 无 recording_uri（但仍建 session 行? 设计：recording_uri 空、行仍建——确认建行不依赖 sink）。**未拨入无行**（H4）：relay 停 pending→handler 未执行→无行（断言）。

---

## T6 — httpapi：`GET /v1/jobs/{id}/pty/recording` gate（D-P3-7/H6/M3）

**触碰**：
1. `internal/httpapi/server.go:337+` authed `/v1` 组加 `r.GET("/jobs/{id}/pty/recording", s.handlePtyRecording)`。
2. 新 `pty_recording_handler.go`：
```go
caller := callerFromCtx; job,ok := s.jobs.Get(id)
if !ok { 404 }   // 【无 remote-409】cast 恒 hub-local(serve 写 host result_dir, T5), 与 artifact 不同(T7 修正)
if !s.callerMayAttach(caller, job) { 403 }          // owner 或 admin(H6: 空 owner 仅 admin)
sess,ok,_ := s.ptySessions.GetPtySessionByJob(id)   // 注入的窄接口(见 T5), 非 s.store
if !ok || sess.RecordingURI=="" { 404 }             // 未录/已过期清理(明示)
path := SafeJoinUnder(resultDir, sess.RecordingURI)
if !fileExists(path) { 404 }
audit(caller, id, sess.PtySessionID)                // 下载留痕
if sess.Encrypted {
	// 流式解密: 先解密+认证 header+首帧(失败→4xx/5xx, 未发 200); 通过→200 + stream; 中途损坏→断流+日志
	dec, err := s.castRecorder.NewDecReader(path)  // 首帧认证在此/首次 Read
	if err != nil { 4xx; return }
	w.Header().Set("Content-Type","application/x-asciicast"); w.WriteHeader(200)
	io.Copy(w, dec) // 中途 err→截断连接(记日志)
} else {
	// 明文: 复用 artifact SafeJoin+ServeFile
	http.ServeFile(w, r, path)
}
```

**验收**：单测——owner→200+内容；**worker-source 自然完成 job→200**（cast hub-local，非 409，T7 修正）；非 owner 非 admin→403；allow_empty 无 admin→403；未录/uri 空/文件没了→404；加密→解密内容正确、header 认证失败→4xx（未发半截）、中途篡改→断流；明文→ServeFile；审计事件发出。

---

## T7 — e2e + P2 回归 + 配置迁移 + 环检

**e2e**（复用 P2 pty attach harness + Linux 真 pty + cast 启用 config）：
1. 录制往返：interactive job（cast enabled）→ 会话产 pty.cast → GetPtySessionByJob 有行(bytes/started/ended) → GET /pty/recording 得回内容（明文 + 加密两路）。
2. **retention regime1**：cast TTL 过期 sweep → pty.cast 文件删、pty_sessions 行保留、recording_uri 空 → gate 404。
3. **retention regime2**：job prune → pty_sessions 行删 + result_dir(含 pty.cast) 删。
4. 授权矩阵：owner/admin/非 owner/allow_empty（403/200 各路径）。
5. 加密安全（castrec 单测已覆盖测试向量，e2e 补一条端到端加密下载解密正确）。

**P2 回归**（关键，Done()=封尾变更）：
6. interactive cancel/正常 result：cast.Close 人为阻塞 → host `Run` 在 `hostCancelGrace` 内返回、job 终态正确、recording 置失败（非半截）。
7. 全量 P2 e2e/单测零回归（T1 改了 relay close 语义）。

**配置迁移**：更新 `config.example`/测试 fixture——`storage.cast.enabled` 示例 + 文档「开录制须 enabled:true」；断言旧 fixture（无 enabled）解出 `Enabled=false` 不录（不回归）。

**环检**：`go list -deps ./internal/ptyrelay` 仍无 `jobstore/job/config/httpapi/castrec`（leaf）；`ptyrelay` 不 import castrec（castrec 满足 ptyrelay 接口、方向 httpapi→castrec→ptyrelay 接口）；`go build`/`vet`/`test ./...`（真 pty）绿 + `-race`（ptyrelay/castrec/httpapi/runner）+ `GOOS=windows build` 绿。

---

## P3 验收总门
- `go build`/`vet`/`test ./...`（Linux 真 pty）全绿；`-race` 绿；`GOOS=windows build` 绿。
- `go list -deps` 环检：`ptyrelay` leaf（不 import castrec/jobstore/job/config）；`job` 不 import pty/ptyrelay/castrec。
- **安全**：castrec 测试向量（截断/追加/重排/跨文件/篡改/空会话）全过；secret <32B / 无 key fail-fast；下载授权矩阵。
- **P2 零回归**（T1 relay close 语义变更）：P2 全量绿 + blocked-cast-Close 的 host grace 回归。
- **两 retention regime**：cast TTL 保留行清 uri / job prune 删行，e2e 各验。

## 待办（plan 内低风险）
- `castCloseGrace`(2s)/chunk(16KB)/`castPlaintextMaxTTLHours`(24) 常量落定。
- 审计事件载体（slog vs notify）实施时对齐。
- ~~Server store 句柄~~：已定=注入窄 `PtySessionStore` 接口（T5/T4，评审高1 修）。
- resize 后 current cols/rows 是否回写（P3 只记初始，P4 可补）。
