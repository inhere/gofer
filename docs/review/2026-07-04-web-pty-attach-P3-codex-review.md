# WEB-03 P3 设计对抗式评审（Codex）

## 总评

结论：**v0.1 不能直接收敛成可拆 plan**。P3 的方向可实现，但当前设计有 4 个阻断和 6 个高风险项需要先修正，尤其是 cast sink 关闭权、加密封套完整性、retention 接线和配置持有位置。按现状拆实现会在录制文件截断认证、registry/relay 关闭竞态、cast TTL 不运行、job prune 后 pty_sessions 孤儿行这几处踩实。

自证：
- `go build ./...`：通过（`GOCACHE=.cache/go-build`，`GOMODCACHE=.cache/gomod`）
- `go vet ./...`：通过（同上）
- `go list -deps ./internal/ptyrelay` 未出现 `internal/jobstore`、`internal/job`、`internal/config`、`internal/httpapi`、`internal/serve`，当前 leaf 边界成立。

## 阻断

### B1. D-P3-1 让 `Relay.Close()` 关闭 cast 会引入 recordLoop 写入与 cast.Close 的竞态

现状 `recordLoop` 是 cast 唯一 writer：`internal/ptyrelay/relay.go:120-131`。但 `Relay.Close()` 可以被 recordLoop 自己调用，也可以被 registry 外部关闭调用：`internal/ptyrelay/relay.go:235-252`、`internal/ptyrelay/registry.go:194-203`。如果 P3 按设计在 `Relay.Close()` 末尾 `cast.Close()`，外部 close 路径会先 `src.Close()` 并关闭 `done`，但不等待 `recordLoop` 真正退出；`recordLoop` 仍可能在 `Read` 返回尾部 `n>0` 后继续执行 `r.cast.Write(chunk)`。

这会造成 `cast.Write` 与 `cast.Close` 并发，轻则写关闭文件报错、尾块丢失，重则加密封尾与数据块交错导致录制不可解密。P2 把 registry IO 移出锁是成立的：`Registry.Close` 锁内只 detach，锁外 `rel.Close()`（`internal/ptyrelay/registry.go:198-203`），但 P3 还必须把 cast 关闭权放到 recorder 所有的生命周期里，例如 `recordLoop` `defer closeCastOnce()`，或者让 `Relay.Close` 只触发 source close，再等待 recorder goroutine 完成后由 recorder 关 cast。否则“唯一 writer”不等于“Close 无并发”。

必改：明确 cast sink 的 close owner 是 recordLoop，或者为 cast sink 提供内部互斥/once 且保证 final block 写在所有 data block 之后；同时 `Done()` 语义要对应“cast 已 flush/close 完成”，否则 handler finalize 读到的 recording_uri 可能指向未封尾文件。

### B2. D-P3-4 加密封套不能按 v0.1 宣称防截断，零长度结束块若不认证可被伪造

设计封套为 `magic+version+fileNonce` 后接 `uint32 len + GCM.Seal(nonce=fileNonce XOR counter, plaintext_chunk)`，Close 写 `len=0` 结束块（`docs/design/2026-07-04-web-pty-attach-P3-design.md:51-55`）。GCM 能认证单块密文，但 v0.1 没有认证“整条流到这里结束”。如果 `len=0` 是明文未认证哨兵，攻击者可以在任意完整块边界截断文件并追加 4 字节零长度结束块，下载端会接受一个合法前缀，尾部被静默删除。

`fileNonce XOR counter` 对 nonce 唯一性本身可接受，前提是 fileNonce 随机 96-bit、counter 不回绕且同 key 下 fileNonce 不复用；但完整性还缺：
- AAD 至少应绑定 magic/version/session_id/counter/plaintext_len/is_final，防止跨文件/跨块语义混淆。
- 结束块必须是认证块，例如 final frame = GCM-Seal(empty, AAD{counter,is_final=true,total_plaintext,block_count})，不能只是裸 `len=0`。
- 解密端必须把 EOF before authenticated final 视为 corruption，不能当普通 EOF。
- key=`sha256(env)` 只适合 env 是高熵随机密钥；如果允许口令短语，必须用 KDF。至少要定义 env 编码（建议 base64 32B 或 hex 32B）、长度校验和错误策略，避免任意短字符串经 SHA256 变成“看似 AES-256”的弱口令。

必改：把封套改为有认证终止条件的 framed AEAD，写清 AAD、final frame、EOF/截断/追加垃圾的处理规则和测试向量。否则安全项不能进实现。

### B3. D-P3-6 cast TTL 独立 sweep 接不到现有 prune loop：未配置 job retention 时永远不跑

P3 要求 cast retention 默认 24h，且作为独立 regime（`docs/design/2026-07-04-web-pty-attach-P3-design.md:63-66`）。但当前 serve 只在 `storage.retention.Enabled()` 为真时启动 prune loop：`internal/serve/serve.go:267-270`；`RetentionConfig.Enabled()` 只看 `MaxAgeDays/MaxCount/WorkflowMaxAgeDays`，不看 cast TTL：`internal/config/model.go:471-477`。

这意味着默认只开 cast 录制、未配置 job retention 的部署中，P3 的“默认 24h cast TTL”不会运行，明文或密文 cast 都会一直留在 `result_dir`，与 K4“默认短 TTL”冲突。`Service.Prune()` 读取 config 阈值没用，因为 goroutine 根本没启动。

必改：prune loop enable gate 要包含 cast retention，或为 cast sweep 单独 loop；启动日志/重载语义也要更新。否则 retention regime 1 不可实现。

### B4. job prune 后删除 pty_sessions 行的接线点缺失，`PruneJobs` 当前只返回 result_dir

P3 设计说 job retention 连带删除该 job 的 pty_sessions 行（`docs/design/2026-07-04-web-pty-attach-P3-design.md:65`），但当前 `PruneJobs` 只返回 `(deleted, prunedDirs)`：`internal/jobstore/prune.go:57-102`。`Service.Prune()` 只能拿到目录并 `os.RemoveAll(dir)`：`internal/job/prune.go:49-62`，没有 job_id 可再删 pty_sessions。

更严重的是 workflow prune 也会删除 step job：`internal/jobstore/prune.go:187-218`，同样没有 pty_sessions 删除语句。若 P3 只在 `Service.Prune` 外层按 job_id 补删，workflow path 仍会漏。SQLite FK 默认也未见开启，因此不能靠外键级联。

必改：把 `pty_sessions` 作为 jobstore owned row，在 `PruneJobs` 和 `PruneWorkflows` 的同一事务里 `DELETE FROM pty_sessions WHERE job_id=?`；或改变 prune API 同时返回 job_id 并覆盖 workflow path。推荐前者，事务边界更清晰，也避免 job 层理解新表。

## 高

### H1. `httpapi.Server` 当前没有 `storage.cast` 配置，P3 文档的 `s.cfg` 锚点不成立

P3 文档说 `handlePtyConnect` 已有 `s.cfg` 可取 cast 配置（`docs/design/2026-07-04-web-pty-attach-P3-design.md:36`），但 `Server` 字段 `cfg` 是 `*config.ServerConfig`：`internal/httpapi/server.go:90-103`、`internal/httpapi/server.go:213-223`。`storage.cast` 在 `config.Config.Storage.Cast`：`internal/config/model.go:426-436`，当前未注入 httpapi。

这不是单纯行号漂移，而是实现落点冲突：cast sink 放 httpapi 可以成立，但需要新增窄配置注入（例如 `SetCastConfig`/`SetRecordingConfig`）或在 `core/serve` 组装处构造 sink factory。直接在 handler 里读 `s.cfg.Storage.Cast` 不可编译。

### H2. `RelayBinding` 当前没有 `Cols/Rows`，P3 的 asciinema header/pty_sessions 初始尺寸来源不存在

P3 D-P3-2 写明 host runner `Prepare` 已有 `f.Cols/f.Rows` 并填入 binding（`docs/design/2026-07-04-web-pty-attach-P3-design.md:41-42`）。真实代码里 worker dispatch 帧确实带 `Cols/Rows`：`internal/runner/worker/runner.go:201-214`，但 `RelayBinding` 只有 worker/instance/job/session/nonce/expiry：`internal/ptyrelay/registry.go:35-42`，`Prepare` 调用也没有传尺寸：`internal/runner/worker/runner.go:163-170`。

必改设计：P3 需要显式扩展 `RelayBinding{Cols,Rows}`，并定义 0 值 fallback（应与 pty runner 默认 80x24 对齐），否则 cast header 和 pty_sessions rows/cols 都没可信来源。

### H3. D-P3-3 finalize 不能只写“relay.Done 后 handler 返回前”，必须覆盖 ctx 提前返回和 close 后 drain/flush

当前 `handlePtyConnect` Open 后只是 `defer s.ptyRelays.Close(...)`，然后 `select { case <-entry.Relay.Done(): case <-ctx.Done(): }` 返回：`internal/httpapi/pty_connect_handler.go:86-98`。如果 ctx 提前取消，handler 会先返回并触发 registry Close；按 B1 的现状，Close 不保证 recordLoop/cast flush 已完成。

因此 pty_sessions finalize 若简单写在 select 后，可能在 ctx 分支过早写 `closed/bytes_out/recording_uri`，而 cast 仍未封尾或 bytes_out 仍会变化。若放在 defer，也必须明确顺序：close source -> wait recorder done/cast closed -> read `RecordedLen/InputLen` -> Upsert closed。当前 `Relay.Done()` 的定义是 relay close，不是“cast sink 已关闭”的设计契约。

### H4. worker 从未拨入 / dial 失败没有 pty_sessions 行，与“一等表审计”目标存在语义缺口

P2 的 pending_worker 语义是：无 Relay 时 `Done(job)` 立即返回（`internal/ptyrelay/registry.go:224-244`），worker 未拨入或 dial 失败时 `handlePtyConnect` 从未执行，自然不会建 cast，也不会建 pty_sessions 行。P3 设计“建行于 Open”只覆盖真正打开的会话：`docs/design/2026-07-04-web-pty-attach-P3-design.md:44-48`。

如果 P3 的审计目标只定义为“成功建立 pty stream 的录制元数据”，这可接受，但必须在设计里说清楚；如果要审计 interactive job 的 pending/dial_failed，会话行必须提前在 host runner `Prepare` 处建 `pending`，并在 `Registry.Close`/job finish path finalize 为 `failed/no_worker`。当前 v0.1 没做这个取舍，plan 会在测试口径上分裂。

### H5. cast sink 写失败“停后续写 + recording_uri 失效”需要回传状态，否则 finalize 单点无法可靠置空

`recordLoop` 当前忽略 cast 写错误：`internal/ptyrelay/relay.go:129-130`。P3 说 sink 写失败只记一次、停后续 cast 写、finalize 时若 cast 写失败置 `recording_uri` 空（`docs/design/2026-07-04-web-pty-attach-P3-design.md:37-38`、`:46-47`）。但 `ptyrelay` leaf 不应知道 jobstore/httpapi，且 `writeCloser` 接口当前不暴露 error state：`internal/ptyrelay/relay.go:72-76`。

需要在设计里补一个 leaf 友好的状态读取方式：例如 ptyrelay 只负责调用 sink，httpapi 持有 concrete sink 并在 finalize 查询 `sink.Valid()/Err()`；或者 `Relay` 暴露 `CastErr()` 但不 import 上层。否则“recording_uri 置失效”没有可靠数据源。

### H6. route 可挂 authed `/v1`，但 allow_empty_token 与 `callerMayAttach` 的下载授权语义要单独定义

`callerMayAttach` 对空 owner 只允许 admin：`internal/httpapi/attach_ticket_handler.go:76-85`。在 `allow_empty_token` 模式下，请求 caller 可能为空；如果没有 configured admin caller，录制下载会被拒绝，即使系统显式允许空 token。attach ticket 当前也复用此规则。

这可能是合理收紧，但 P3 的 recording gate 不能只写“复用 callerMayAttach”。需要说明 allow-empty 部署下录制下载是否禁用、是否需要 admin、还是允许 same empty caller。录制内容敏感，建议保持更保守，但必须在设计和测试中固定。

## 中

### M1. `Registry.Open(nonce, source, opts...)` 用 variadic 不会破坏现有调用点，但测试需要覆盖 opts 透传

当前 Open 签名是 `Open(nonce string, source PtySource)`：`internal/ptyrelay/registry.go:107`，真实调用点主要是 `handlePtyConnect`：`internal/httpapi/pty_connect_handler.go:87` 和若干测试。改成 `Open(nonce, source, opts ...Option)` 对现有调用点源码兼容；Open 内 `New(source)` 需要改为 `New(source, opts...)`：`internal/ptyrelay/registry.go:130`。这不是阻断，但需要加测试证明 `WithCast` 不是死代码。

### M2. bytes_in 放 `Viewer.SendInput` 方向正确，但要按实际写入 n 计数并避免吞错

输入唯一入口当前是 attach handler 的 input frame：`internal/httpapi/attach_handler.go:160-166`，最终走 `Viewer.SendInput`；只读 viewer 会被拒绝：`internal/ptyrelay/relay.go:278-293`，因此在 `SendInput` 成功写入后计数不会重复，多 viewer read-only 场景也不会计入。相比在 `remotePtySource.Write` 计数，relay 层更通用，也不污染 httpapi source。

但实现时不要简单 `Add(len(b))`：`PtySource.Write` 接口返回 `(int,error)`，应在 `n>0` 时累计实际写入字节，且 input frame 写失败目前被 attach handler 忽略（`internal/httpapi/attach_handler.go:166`）。如果需要审计准确性，至少 relay 层要只统计 source 接受的字节。

### M3. 明文 `ServeFile` 分支必须继续做 SafeJoin/regular-file 检查，加密分支不能复用 `ServeFile`

artifact 下载现有模式是 `SafeJoinUnder` + `os.Stat` + `http.ServeFile`：`internal/httpapi/artifact_handler.go:56-69`。recording 明文可复用该安全模式，但加密录制必须边解密边写 `ResponseWriter`，不能走 `ServeFile`，也不能在解密失败后继续返回 200 的半截内容。设计需要定义解密 header 校验失败、认证失败、写响应中途失败的状态处理；认证失败发生在已写 body 后只能断流/日志，无法改 status，应尽量在发送 headers 前完成 header 和首块认证。

## 逐条核对

1. **D-P3-1 cast sink 生命周期 vs P2 relay 五关闭源 CAS**：registry 锁外关闭已成立，`Prepare` 替换旧 relay 锁外 close：`internal/ptyrelay/registry.go:83-101`，`Close` 锁外 close：`internal/ptyrelay/registry.go:198-203`。但把 `cast.Close()` 放 `Relay.Close()` 会与 recordLoop 写 cast 并发，见 B1。唯一 writer 是 recordLoop，不代表 close 无并发。`Open` variadic opts 源码兼容，需改 `New(source, opts...)`。

2. **D-P3-3 pty_sessions 建/finalize 覆盖所有关闭路径**：Open 建行覆盖成功拨入；worker 从未拨入/dial 失败没有 handler、没有 session 行，需明确接受或改为 Prepare 建 pending，见 H4。handler ctx 提前返回时 finalize 必须在 defer 中保证 close/drain/flush 顺序，现有 handler 只有 defer Close：`internal/httpapi/pty_connect_handler.go:86-98`。Upsert 单点应放 httpapi，但不得与 job runner defer Close 竞争写；runner 只应 close relay，不写 pty_sessions。

3. **D-P3-4 分块 AES-256-GCM 封套**：nonce 唯一性方案基本可行但要写明 counter 编码和回绕限制；当前结束块/总长未认证，不能防攻击者截断后追加裸 `len=0`，见 B2。key derivation 要限制 env 为高熵原始 key，或改 KDF。流式解密需把 EOF before authenticated final 作为 corruption。

4. **D-P3-5 config**：现有常量和 fail-fast 在 `internal/config/model.go:439-449`、`internal/config/loader.go:339-344`；默认 TTL 未落地，`ApplyDefaults` 没设置 cast TTL：`internal/config/loader.go:242-268`。组合 fail-fast 不与现有两条冲突，但要先算 effective TTL。运行期取 key 当前没有落点；httpapi 也没有 Storage config，见 H1。

5. **D-P3-6 retention 两 regime**：`Service.Prune` 当前先 workflow 再 job：`internal/job/prune.go:28-62`。cast 独立 sweep 如果挂在这里，还必须修 serve loop enable gate，见 B3。job prune 连带删 pty_sessions 不能靠 `PruneJobs` 返回值，见 B4。interactive job 是否有产物不影响 prune 选择，`PruneJobs` 按 terminal status 和 age/count 选 victim：`internal/jobstore/prune.go:44-57`、`:65-102`，不是按 result 文件存在性判断。

6. **D-P3-7 recording gate**：路由挂 authed `/v1` group 可行，`/v1/jobs/{id}` 相关路由集中在 `internal/httpapi/server.go:337-362`；worker connect/pty-connect/attach ws 是 group 外特殊路由：`internal/httpapi/server.go:298-309`。授权可复用 `callerMayAttach`，但 allow-empty 语义要写清，见 H6。明文可复用 artifact 的 SafeJoin/ServeFile 模式，加密必须流式解密，见 M3。

7. **G024 leaf 边界**：当前 `ptyrelay` 是 leaf，`go list -deps ./internal/ptyrelay` 未出现 `jobstore/job/config/httpapi/serve`。cast sink concrete 实现和 pty_sessions 持久化放 httpapi 方向正确；接口留在 ptyrelay，具体 sink 由 httpapi 注入也正确。注意不要让 ptyrelay 为了查询 cast error import httpapi/config/jobstore。

8. **D-P3-8 bytes_in 计数位置**：放 `Viewer.SendInput` 更合适，覆盖所有 viewer input 且不会统计 read-only follower；放 `remotePtySource.Write` 会把 httpapi source 变成统计源，不如 relay 层通用。实现时按实际写入 n 计数，见 M2。

## 事实性冲突

- P3 文档说 `handlePtyConnect` 可从 `s.cfg` 取 cast 配置，但 `Server.cfg` 是 `*config.ServerConfig`，不含 `Storage.Cast`：`internal/httpapi/server.go:90-103`、`internal/httpapi/server.go:213-223`。
- P3 文档说 `RelayBinding` 可取 `Cols/Rows`，但当前 struct 没有这两个字段：`internal/ptyrelay/registry.go:35-42`；worker `Prepare` 也未传尺寸：`internal/runner/worker/runner.go:163-170`。
- P3 文档依赖 `PruneJobs` 后按 job_id 删 pty_sessions，但 `PruneJobs` 只返回 result_dir：`internal/jobstore/prune.go:57-102`，`Service.Prune` 只有目录：`internal/job/prune.go:49-62`。
- P3 文档说 cast TTL 挂现有 prune 链即可，但现有 prune loop 的启动条件不包含 cast TTL：`internal/serve/serve.go:267-270`、`internal/config/model.go:471-477`。
- P2 设计文档仍有“`Registry.Close` 持锁调 `Relay.Close`”的旧描述（`docs/design/2026-07-04-web-pty-attach-P2-design.md:28`），当前实现已修成锁外 close：`internal/ptyrelay/registry.go:190-203`。

## 必改项

- 重写 D-P3-1：cast close owner/ordering 必须与 recordLoop 绑定，`Done()` 必须代表 cast 已 flush/close。
- 重写 D-P3-4：封套必须认证 final frame/总长或块数，定义 AAD、key 格式、EOF/截断/追加处理和测试向量。
- 重写 D-P3-6：cast retention 启动条件必须独立于 job retention；pty_sessions 删除必须进入 `PruneJobs`/`PruneWorkflows` 事务或等价 job_id 接线。
- 补 D-P3-5/H1：给 httpapi/cast sink 一个真实的 `Storage.Cast` 配置注入点和 key 解析落点。
- 补 D-P3-2：`RelayBinding` 显式携带 `Cols/Rows`，定义 fallback。
- 明确 D-P3-3：未拨入是否建 pending 行；finalize 的 defer 顺序和幂等写入点。

# 第二轮：v0.2 复审

结论：**需 v0.3，暂不可拆 plan**。v0.2 已把 v0.1 的主要方向性问题收敛到可实现路径，但还剩 **1 个阻断、2 个高风险**：cast 录制总开关/enable 语义缺失是阻断；`Relay.Done()` 语义变强对 P2 host 取消时序的影响、加密封套细节/测试向量不足是高风险。

## 事实性冲突优先

- v0.2 多处写 `castRecordingEnabled` / `castEnabled`，但当前配置只有 `Storage.Cast.RetentionTTLHours` 与 `Storage.Cast.Encryption`，没有 `Storage.Cast.Enabled` 或等价总开关：`internal/config/model.go:426`、`internal/config/model.go:442`、`internal/config/model.go:447`。`RetentionTTLHours=0` 在设计中又表示默认 24h，不可同时表达“禁用录制”。这会让实现无法判定“cast 开则 prune loop 起”的 `castEnabled` 来自哪里。
- D-P3-1 说 `Relay.Done()` 升级为“cast 已封尾”，这会改变 P2 已实施 host runner 的等待含义：worker result 后仍会等 `relayRegistry.Done(job)`，但只被 `hostCancelGrace=10s` 兜底：`internal/runner/worker/runner.go:37`、`internal/runner/worker/runner.go:223`、`internal/runner/worker/runner.go:233`。v0.2 没有规定 cast `Close`/final frame 写入必须快速、不可被文件系统 hang 住、以及超时后 handler finalize 如何处理。
- D-P3-1 文字说 sink 满足 `castSink{ Write; Close }`，但当前 leaf 接口实际只有 `Write`，`WithCast` 也按 `writeCloser` 保存：`internal/ptyrelay/relay.go:72`、`internal/ptyrelay/relay.go:87`。实现必须同步改接口并保证 `Registry.Open` 可透传 opts；当前 `Open` 仍是 `Open(nonce, source)` 且 `New(source)`：`internal/ptyrelay/registry.go:105`、`internal/ptyrelay/registry.go:130`。

## 上轮问题核销

| 项 | 判定 | 依据 |
| --- | --- | --- |
| B1 cast.Close 与 recordLoop 写竞态 | **部分解决** | v0.2 把 close owner 放到 `recordLoop defer finish()`，方向正确，能避免外部 `Relay.Close()` 与 `cast.Write` 并发；当前竞态根因仍在 `Close()` 立即 `close(done)` 而不等 `recordLoop`：`internal/ptyrelay/relay.go:120`、`internal/ptyrelay/relay.go:129`、`internal/ptyrelay/relay.go:235`、`internal/ptyrelay/relay.go:251`。但 v0.2 未充分处理 P2 兼容性：host runner 会在 result/cancel 路径等 `relayRegistry.Done`，上限是 `hostCancelGrace`：`internal/runner/worker/runner.go:233`、`internal/runner/worker/runner.go:272`。若 `Done()` 改成等 cast 封尾，cast sink `Close` 必须有明确快速/不可阻塞契约；否则 P2 的“drain complete”会被录制封尾拖到 grace 超时，handler finalize 也可能无限等。never-Start pending force-close 路径在当前 registry 中 `pending_worker` 没有 Relay，`Done` 返回 closedChan：`internal/ptyrelay/registry.go:224`；设计里的 `finish Once` 可覆盖“有 Relay 但未 Start”的一般路径，需在实现测试固定。 |
| B2 GCM 封套不认证截断 | **部分解决** | framed AEAD + 认证 final frame + EOF-before-final=损坏，已覆盖 v0.1 的裸 `len=0` 截断问题。AAD 绑定 version/fileID/frame_index/is_final 可拒绝跨文件与重排；解密端若严格递增 frame_index 并在 final 后要求物理 EOF，也可拒绝追加裸帧/追加已认证旧帧。仍需补 plan 细节：随机 12B nonce 对单文件可行但不是零风险，建议改为 per-file 随机 nonce prefix + frame counter 或至少规定 GCM nonce 碰撞测试/上限；Go1.25 stdlib 正确写法应是 `hkdf.Key(sha256.New, []byte(secret), nil, "gofer-pty-cast/v1", 32)`，不是切片式伪码；`hkdf.Extract`/`Expand` 仅在复用 PRK 时需要；GCM `Seal(nonce, nil, nil, aad)` 生成空明文 final frame合法。测试向量必须进入 plan：截断、追加垃圾、final 后追加、重排、跨文件 fileID 替换、nonce 重复防护/检测。 |
| B3 cast sweep 接不到 prune loop | **未解决** | v0.2 把 gate 写成 `ret.Enabled() || castRecordingEnabled`，但 `castRecordingEnabled` 未定义。当前 prune loop 只看 job/workflow retention：`internal/serve/serve.go:267`、`internal/serve/serve.go:268`；`RetentionConfig.Enabled()` 只看 `MaxAgeDays/MaxCount/WorkflowMaxAgeDays`：`internal/config/model.go:471`、`internal/config/model.go:475`。当前 cast config 没有总开关，且 `RetentionTTLHours=0` 被 v0.2 定义为默认 24h；因此 v0.3 必须定义“录制启用”的来源，例如 `storage.cast.enabled` 默认 false/true 的取舍、或由 `retention_ttl_hours/encryption` 派生但要能关闭。 |
| B4 job prune 删不了 pty_sessions 行 | **已解决（设计层）** | 现有 `PruneJobs` 与 `PruneWorkflows` 都已有同一事务边界，且已经在事务内删除 interactions/job_events/event_deliveries/jobs：`internal/jobstore/prune.go:73`、`internal/jobstore/prune.go:77`、`internal/jobstore/prune.go:94`、`internal/jobstore/prune.go:180`、`internal/jobstore/prune.go:213`。v0.2 要求把 `DELETE FROM pty_sessions WHERE job_id=?` 加进这两个事务，改动面成立，不需要改变返回签名。 |
| H1 httpapi 无 Storage.Cast | **已解决（设计层）** | v0.2 改为在装配处构造 recorder factory 并通过 `SetCastRecorder(factory)` 注入 httpapi，符合现状：`Server.cfg` 只有 `*config.ServerConfig`：`internal/httpapi/server.go:88`、`internal/httpapi/server.go:213`；完整 `config.Config` 在 core/serve 装配链上可得：`internal/core/core.go:75`、`internal/serve/serve.go:100`。需要注意 mcp/测试直接构造 `httpapi.New` 时 factory nil 的禁录默认。 |
| H2 RelayBinding 无 Cols/Rows | **已解决（设计层）** | v0.2 明确 `RelayBinding += Cols/Rows`，worker dispatch 已有 `f.Cols/f.Rows`：`internal/runner/worker/runner.go:201`、`internal/runner/worker/runner.go:211`；当前 `RelayBinding` 还没有字段：`internal/ptyrelay/registry.go:34`。80x24 fallback 与 pty runner 默认一致：`internal/runner/pty/runner.go:18`、`internal/runner/pty/runner.go:89`。 |
| H3 finalize 顺序/ctx 提前返回 | **已解决（设计层）** | v0.2 写死 `select -> Close -> <-Done -> read bytes -> Upsert`，能覆盖当前 handler ctx 分支过早返回问题：`internal/httpapi/pty_connect_handler.go:86`、`internal/httpapi/pty_connect_handler.go:92`、`internal/httpapi/pty_connect_handler.go:94`。实现时必须把 finalize 放在单一尾段/defer 中，并避免 `entry.Relay.Done()` 在 cast Close hang 住时无限等待。 |
| H4 未拨入无 session 行 | **已解决（取舍自洽）** | v0.2 明确 `pty_sessions` 是“已建立 pty stream 的录制元数据”，未拨入/dial failed 由 job status/error 表达，不建 pending 行。这与当前 P2 pending 语义一致：pending/missing/finalized `Done` 直接返回 closedChan：`internal/ptyrelay/registry.go:224`、`internal/ptyrelay/registry.go:240`。P4 前端若只展示 recording metadata 可接受；若要展示 pending/dial_failed，会是 P4/P5 新需求而非 P3 表契约。 |
| H5 cast 写失败状态回读 | **已解决（设计层）** | v0.2 规定 httpapi 持 concrete sink 并查 `sink.Err()`，ptyrelay leaf 只 `Write`/`Close`，不 import 上层，符合现有 leaf 边界：`internal/ptyrelay/relay.go:18`、`internal/ptyrelay/relay.go:72`。实现时接口需从只有 `Write` 扩成 `Write+Close`，错误态保留在 httpapi concrete sink。 |
| H6/M3 下载授权/加密流式 | **已解决（设计层）** | allow_empty 保守拒与现有 `callerMayAttach` 的空 owner 仅 admin一致：`internal/httpapi/attach_ticket_handler.go:76`、`internal/httpapi/attach_ticket_handler.go:81`；allow_empty 下 caller 为空：`internal/httpapi/auth.go:27`、`internal/httpapi/auth.go:29`，而 `CallerCanAdmin("")` 恒 false：`internal/config/model.go:354`、`internal/config/model.go:356`。加密下载不复用 `ServeFile`、首帧认证后再发 200 的方向正确；明文下载可复用 artifact 的 `SafeJoinUnder`/`ServeFile` 模式：`internal/httpapi/artifact_handler.go:56`、`internal/httpapi/artifact_handler.go:68`。 |

## v0.2 新问题

### 阻断：cast 录制总开关/enable 语义缺失

v0.2 的 retention、factory 注入、handler 建 sink 都依赖“cast 是否启用”，但当前配置没有 enabled 字段：`internal/config/model.go:442`。如果把“有 `storage.cast` 节”当启用，YAML 解码后的零值无法区分未配置和显式默认；如果把 `RetentionTTLHours=0` 当关闭，又与 v0.2 默认 24h 冲突；如果把 `Encryption.Enabled` 当启用，则明文短 TTL 录制无法启用。该项必须 v0.3 定义清楚，否则会出现 cast 恒开、无法关闭录制或 prune loop 不启动三选一的问题。

### 高：`Relay.Done()` 变强可能回归 P2 取消/完成时序

P2 当前语义是 host runner 等“relay drain complete”，并以 10s grace 防卡死：`internal/runner/worker/runner.go:37`、`internal/runner/worker/runner.go:233`、`internal/runner/worker/runner.go:272`。v0.2 把 `Done()` 改成“recordLoop 已退出 + cast 已封尾”，这对 handler finalize 有利，但把磁盘 flush、GCM final frame、文件 close 的耗时纳入 P2 job 完成路径。v0.3 需补：

- cast sink `Close` 不得做无界阻塞操作；必要时 recordLoop finish 只做 bounded close，超时置 `sink.Err()`。
- host runner grace 超时后 job 可完成，但 httpapi finalize 不能永久等 `<-Done()`；应有同一 bounded wait 或由 `Relay.Close`/`finish` 保证可终止。
- 增加 P2 回归测试：worker result 后 cast Close 人为阻塞，`Run` 在 `hostCancelGrace` 后返回；ctx cancel 路径同样返回；recording 状态被置失败而不是半成品 URI。

### 高：framed AEAD 还缺实现约束与测试向量

D-P3-4 的安全模型大体成立，但 plan 级别仍缺足够约束。随机 96-bit nonce 对低帧数可接受，但 GCM 安全依赖 key 下 nonce 唯一；长时间/大量 frame 用纯随机 nonce 没有实现级防复用，计数 nonce 更稳。HKDF `salt=nil` 在 HKDF 规范上可接受，但如果 env secret 是低熵口令，HKDF 不是密码哈希，不能宣称“容任意 env secret”即可达到强 256-bit；应要求高熵 secret，或明确不是 password KDF。AAD 建议包含 magic/version/fileID/frame_index/is_final/plaintext_len 或 total frames，以便测试和审计更直观。解密端必须规定 final frame 后若还有任何字节就是损坏，否则“追加已认证旧帧/垃圾”可能被忽略。

### 中：D-P3-6 的独立 cast TTL sweep 会删除 pty_sessions 行，可能损失录制审计元数据

v0.2 写 `PrunePtySessions(ended_at+castTTL<now)` 返回 `recording_uri` 并“事务内删行”。但表定义同时承载 `started_at/ended_at/bytes_in/bytes_out/owner/state` 等会话元数据。若独立 TTL 只为删除 cast 文件，直接删整行会让 job 仍在而录制元数据消失，P4 详情页无法显示“已录制但已过期/已清理”。建议 v0.3 明确两种 regime：cast TTL 只清 `recording_uri/encrypted` 并保留 session 行，job/workflow retention 再删行；或明确产品接受 TTL 后 session metadata 一并消失。

### 中：首帧认证后发 200 与“截断不返回半截”表述仍有冲突

v0.2 写“首帧认证 + header 校验在写 200/body 前完成”，但后续帧如果在响应中途认证失败，HTTP status 已发，无法做到“不返回半截”，只能断流并记录日志。设计正文已有“写 body 中途认证失败→断流”，但安全章节仍写“不发半截”。v0.3 应统一口径：下载端可避免在首帧/header 损坏时发 200；中途损坏只能中断 stream，并依赖客户端校验/错误提示。

## 收敛判定

**需 v0.3，不可直接拆 plan。** 上轮 4 阻断 + 6 高中，B4/H1/H2/H3/H4/H5/H6 已达到可计划实现；B1/B2 方向正确但仍需补边界和测试；B3 因 cast enable 语义缺失仍未解决。v0.3 最小修订范围：

- 定义 `storage.cast.enabled` 或等价启用规则，并说明默认值、禁用录制、`retention_ttl_hours=0` 的语义，以及 `startPruneLoop` 的真实 gate。
- 给 `Relay.Done()` 语义变更补 P2 兼容约束和 bounded close/finalize 测试。
- 把 framed AEAD 格式固化到可实现级别：HKDF Go1.25 调用、nonce 策略、final 后 trailing bytes、测试向量清单。

# 第三轮：v0.3 终审

结论：**NO-GO，暂不可拆 plan**。v0.3 已基本核销 round-2 的启用开关、P2 等待边界、TTL 两 regime 和下载口径问题；但 D-P3-4 写成“Go1.25 精确调用”的 `hkdf.Key` 示例与真实 Go1.25.10 API 不符，按文档原样实现会编译失败。这是事实性冲突，先修这一处后可再收敛为 GO。

## 五项核销表

| round-2 项 | 判定 | 依据 |
| --- | --- | --- |
| B3 阻断：cast 录制总开关缺失 | **已解决** | v0.3 明确 `CastConfig` 新增 `Enabled bool yaml:"enabled"`、默认 false、`castRecordingEnabled := cfg.Storage.Cast.Enabled` 是 handler 建 sink / prune gate / recording_uri 的唯一判据：`docs/design/2026-07-04-web-pty-attach-P3-design.md:73`。当前真实代码 `CastConfig` 只有 `RetentionTTLHours` 与 `Encryption`：`internal/config/model.go:442`，新增 bool 与现有 YAML 零值/loader 结构自洽；`ApplyDefaults` 可在现有默认段落补 `Enabled && TTL==0 -> 24`：`internal/config/loader.go:242`，现有 encryption key_env/TTL 上限校验可扩展组合校验：`internal/config/loader.go:339`。`Enabled==false` 时不录且 TTL 无关，避免把 `RetentionTTLHours=0` 同时当禁用和默认：`docs/design/2026-07-04-web-pty-attach-P3-design.md:74`。prune loop gate 改为 `ret.Enabled() || cfg.Storage.Cast.Enabled` 对应当前只看 retention 的入口：`internal/serve/serve.go:267`、`docs/design/2026-07-04-web-pty-attach-P3-design.md:82`。opt-in 默认关合理：当前尚无真实录制功能，已有 `storage.cast.encryption/ttl` 配置不能代表曾经可录制；不过 plan 需同步更新示例/测试里旧的 cast 配置预期。 |
| B1 高：`Relay.Done()`=封尾影响 P2 | **已解决（带实现约束）** | v0.3 把 close owner 放在 `recordLoop defer finish()`，`finish` 内 bounded close 后再 `close(done)`：`docs/design/2026-07-04-web-pty-attach-P3-design.md:36`、`:38`。这修正当前 `Close()` 立即 `close(done)`、不等 recordLoop/cast 的基线问题：`internal/ptyrelay/relay.go:120`、`:129`、`:235`、`:251`。P2 host 等待点只有 interactive result/cancel 两处，均受 `hostCancelGrace` 约束：`internal/runner/worker/runner.go:37`、`:233`、`:272`；v0.3 拟 `castCloseGrace ~2s` 小于 10s，叠加后不会把 P2 完成路径拖出 host grace：`docs/design/2026-07-04-web-pty-attach-P3-design.md:39`。handler finalize 先 `Close` 再 `<-Done()`，且 finish 恒关 done，因此 ctx 提前返回不会永久等待：`internal/httpapi/pty_connect_handler.go:92`、`:94`、`docs/design/2026-07-04-web-pty-attach-P3-design.md:48`。新风险评估：boundedClose 超时后不会有后续 `cast.Write`，因为 finish 只在 recordLoop 退出时执行；若 concrete `Close` 永久阻塞，后台 goroutine 仍可能残留，设计已把 `cast.Close` 限定为本地 final frame + file close 的快操作并把超时视为录制失败，P2 不回归。实现测试里的阻塞 fake Close 应可释放，避免测试泄漏。 |
| B2 高：framed AEAD 固化 | **未解决** | 算法方向已收敛：counter nonce `noncePrefix(4)||uint64BE(frame_index)(8)` 消除同文件随机 nonce 复用：`docs/design/2026-07-04-web-pty-attach-P3-design.md:64`、`:66`；AAD 绑定 magic/version/fileID/frame_index/is_final/plaintext_len，配合严格递增、认证 final、final 后必须物理 EOF，可拒截断、追加、重排、跨文件与篡改：`docs/design/2026-07-04-web-pty-attach-P3-design.md:67`、`:68`、`:69`；空明文 final frame 的 GCM Seal/Open 合法，测试清单覆盖空会话：`docs/design/2026-07-04-web-pty-attach-P3-design.md:68`、`:70`。但 D-P3-4 明写 `key := hkdf.Key(...)` 是“Go1.25 精确调用”：`docs/design/2026-07-04-web-pty-attach-P3-design.md:62`，实测 Go 1.25.10 编译失败：`assignment mismatch: 1 variable but hkdf.Key returns 2 values`。正确形态应为 `key, err := hkdf.Key(...)` 并处理 err；否则按设计拆 plan 会产出不可编译实现。noncePrefix 4B 跨文件碰撞概率不是认证漏洞的直接触发条件，因为 fileID 进入 AAD 且同一文件内 counter 唯一；不过大规模同 key 文件下，4B prefix 仍偏短，plan 可把 prefix 扩到 8/12B 或规定文件数上限作为加固项，不作为本轮阻断。 |
| 中：cast TTL sweep 删行丢审计 | **已解决** | v0.3 把 cast 独立 TTL 定义为只清 `recording_uri`/标失效、保留 pty_sessions 行：`docs/design/2026-07-04-web-pty-attach-P3-design.md:81`；job/wf retention 才删整行并由 result_dir 删除 cast 文件：`docs/design/2026-07-04-web-pty-attach-P3-design.md:83`。两 regime 幂等：独立 TTL 用 `recording_uri!=''` 过滤，重复执行无事；job/wf retention 删除 job-owned row 后目录清理 best-effort，重复目录删除无事：`internal/job/prune.go:37`、`:50`、`:57`。`PruneJobs` 已有单事务删除 owned rows 的结构：`internal/jobstore/prune.go:73`、`:77`；`PruneWorkflows` 也在事务内收集 step job 后逐 job 删 owned rows：`internal/jobstore/prune.go:180`、`:213`。在这两处加 `DELETE FROM pty_sessions WHERE job_id=?` 改动面成立，不需要改返回签名。 |
| 中：下载 200/半截口径 | **已解决** | v0.3 D-P3-7 明确加密下载不走 `ServeFile`，在写 200/body 前认证 header + 首帧，失败返回 4xx/5xx；发 200 后中途认证失败只能断流 + 日志：`docs/design/2026-07-04-web-pty-attach-P3-design.md:92`。安全章节也同步成同一口径：`docs/design/2026-07-04-web-pty-attach-P3-design.md:156`。明文路径继续复用 artifact 的 `SafeJoinUnder` + `ServeFile` 模式可行：`internal/httpapi/artifact_handler.go:56`、`:68`；录制路由挂 `/v1/jobs/{id}` 组并复用 owner/admin gate 与当前路由/授权形状一致：`internal/httpapi/server.go:337`、`:357`、`internal/httpapi/attach_ticket_handler.go:76`。 |

## 剩余硬问题

### 阻断：`hkdf.Key` API 签名写错，按设计原样实现不可编译

事实性冲突优先：v0.3 把 `key := hkdf.Key(sha256.New, []byte(os.Getenv(KeyEnv)), nil, "gofer-pty-cast/v1", 32)` 标为 Go1.25 精确调用：`docs/design/2026-07-04-web-pty-attach-P3-design.md:62`。本机 `go version go1.25.10 windows/amd64` 下 3 行 demo 按该写法 `go run` 失败：

```text
.cache\hkdf-demo\main.go:3:21: assignment mismatch: 1 variable but hkdf.Key returns 2 values
```

双返回值写法 `key, err := hkdf.Key(...)` 可编译并输出 `32 <nil>`。必改：D-P3-4 与后续 plan 必须改为双返回值并规定 err fail-fast；不要继续声称单返回值是 Go1.25 精确调用。

## 判定

**NO-GO：阻断 1，高 0。**

阻断只剩 `hkdf.Key` 调用签名这一处事实性错误；B3/B1/TTL/download 已可实施，B2 的 nonce/AAD/final/trailing 规则方向成立。修正 D-P3-4 的 HKDF 调用与错误处理后，v0.3 可据以拆 P3 实施计划。

# 第四轮：P3 实施计划评审

结论：**需改后可逐 T 实施**。计划已修正第三轮的 `hkdf.Key` 双返回值问题，T0/T1/T2/T3/T4/T6/T7 的主要触碰点对得上真实代码；但 T5 仍有一个会导致按片段实现直接漂移/编译失败的硬问题：当前 `httpapi.Server` **没有** `store` 句柄，`s.store.UpsertPtySession` / `s.store.GetPtySessionByJob` 不是现有可用面。计划必须先明确装配方案（推荐在 `job.Service` 增加 pty session 窄方法，或显式 `Server.SetPtySessionStore` 注入 `*jobstore.Store`），否则 T5/T6 会返工。

## 逐 T 核对表

| T | 判定 | 依据 |
|---|---|---|
| T0 config | **可实施，需补 secret 校验落点措辞** | `CastConfig` 目前只有 `RetentionTTLHours`/`Encryption`，新增 `Enabled bool` 不破坏旧 YAML 零值：`internal/config/model.go:442-445`。`ApplyDefaults` 当前到 roles map 即止，可加 `Enabled && TTL==0 -> 24`：`internal/config/loader.go:242-268`。校验现有两条是 encryption key_env 与 TTL 上限，可扩组合 fail-fast：`internal/config/loader.go:339-344`。secret base64/hex 解码需在 serve/core 有 `os.Getenv` 的装配处做，loader 不应读环境；计划第 32 行已写“运行期 serve start”，可实施。 |
| T1 ptyrelay | **可实施，重点测试必须保留** | 当前 `writeCloser` 只有 `Write`：`internal/ptyrelay/relay.go:72-76`，`Close()` 直接 `close(done)`：`internal/ptyrelay/relay.go:235-252`，`recordLoop` 写 cast：`internal/ptyrelay/relay.go:120-131`，确有 `Write`/`Close` 竞态。把 `recordLoop defer finish()` 作为 close owner、`Close()` 只关 source/viewers、不关 done，可消竞态；从未 Start 分支可用 `started` 字段判断：`internal/ptyrelay/relay.go:65-66`。`Registry.Open` 当前无 opts 且 `New(source)`：`internal/ptyrelay/registry.go:107-131`，改 variadic 对现调用兼容。`RelayBinding` 当前无 cols/rows：`internal/ptyrelay/registry.go:34-42`，`Prepare` 可从 dispatch fields 填：`internal/runner/worker/runner.go:163-170`、`:210-212`。P2 host 等 `Done()` 的上限是 `hostCancelGrace=10s`：`internal/runner/worker/runner.go:37-43`、`:233-237`、`:272-277`，castCloseGrace 2s 可被覆盖。 |
| T2 castrec | **可实施** | 计划已使用 `hkdf.Key(...)([]byte,error)` 双返回值：`docs/plans/web-pty-attach/P3-plan.md:100-102`，本机 `go run .\.cache\hkdf-demo` 输出 `32 <nil>`。framed AEAD 自洽：fileID 进 HKDF salt + AAD，counter nonce 在 per-file key 下无跨文件复用，final 空明文认证、EOF-before-final/trailing-after-final 均在 DecReader 校验：`docs/design/2026-07-04-web-pty-attach-P3-design.md:62-73`。新包 `internal/castrec` 被 httpapi/serve 装配导入，`ptyrelay` 只认接口，方向不破坏 leaf；当前 `go list -deps ./internal/ptyrelay | rg internal/(jobstore|job|config|httpapi|serve)` 无命中。 |
| T3 jobstore | **可实施，需修正函数签名描述** | `schemaStmts` 是 `IF NOT EXISTS` 列表，追加 `pty_sessions` 建表与索引幂等：`internal/jobstore/store.go:59-239`，`Open` 每次 apply schema：`internal/jobstore/store.go:288-296`。`PruneJobs` 已在单事务内按 `ids` 循环删 owned rows，`id` 在作用域内，可加 `DELETE FROM pty_sessions WHERE job_id=?` 且不改返回签名：`internal/jobstore/prune.go:73-102`。`PruneWorkflows` 已在事务内收集 step jobIDs 并逐 job 删除，可加同一句：`internal/jobstore/prune.go:180-239`。问题：计划第 126 行写 `ExpireCastRecordings(now int64)`，同时说 ttl 由调用方传入；第 139 行又说 `ExpireCastRecordings(now, castTTL)`。需统一为带 TTL 参数，避免 T4 接线返工。 |
| T4 wiring | **可实施，需明确 prune gate 函数签名一起改** | `core.Build` 有完整 `*config.Config`、`Core.Store`、`Core.Jobs`：`internal/core/core.go:31-54`、`:75-115`。`serve` 调 `core.Build(cfg)` 后构造 HTTP server，可在此解析 key/注入 recorder：`internal/serve/serve.go:100-113`。`Server` 已有 post-construction setter 模式 `SetMetrics`/`SetPresence`/`SetPtyRelay`：`internal/httpapi/server.go:171-195`，加 `SetCastRecorder` 合适。`startPruneLoop` 当前只接 `RetentionConfig` 并只看 `ret.Enabled()`：`internal/serve/serve.go:267-270`，计划里的 `ret.Enabled() || cfg.Storage.Cast.Enabled` 需要同步改函数签名为接 `castEnabled` 或完整 cfg，否则调用点不成立。`Service.Prune` 当前读 `s.config().Storage.Retention` 和 `s.meta`：`internal/job/prune.go:28-62`，接 cast sweep 可行。 |
| T5 handler | **需补，高风险** | 现 handler 正好是 `Open -> defer Close -> select Done/ctx` 的改造点：`internal/httpapi/pty_connect_handler.go:86-98`，且 `s.jobs.Get` 已拿到 `ResultDir`/`CallerID`：`internal/httpapi/pty_connect_handler.go:79-84`、`internal/job/cancel.go:12-20`。但是 `httpapi.Server` 字段只有 `jobs *job.Service`，没有 `store`：`internal/httpapi/server.go:88-123`、`:130-160`；`job.Service.meta` 是私有字段：`internal/job/service.go:107-112`。因此计划第 171 行的 `s.store.UpsertPtySession` 与真实签名不符。必须在 T3/T4 明确暴露/注入 pty session store 面，否则 T5/T6 按片段会 build fail。另：计划第 166 行要求 `sink.Err()`，但 T1 的 `CastSink` 接口不含 Err，这是对的，实际变量类型不能只声明成 ptyrelay 接口；需保留 concrete sink 引用或定义本地 `interface{ ptyrelay.CastSink; Err() error }`。 |
| T6 endpoint | **需随 T5 store 面一起补** | authed `/v1` 组挂点存在，`/jobs/{id}/...` 附近可加路由：`internal/httpapi/server.go:311-357`。`callerMayAttach` 可复用 owner/admin 语义，且 allow_empty 下非 admin 会 403：`internal/httpapi/attach_ticket_handler.go:76-84`。artifact 的远端拒绝与 SafeJoin/ServeFile 模式可复用：`internal/httpapi/artifact_handler.go:36-69`。但计划第 188 行同样写 `s.store.GetPtySessionByJob`，受 T5 同一硬问题影响。加密流式下载“首帧认证在 200 前”可实现，但要把 DecReader 构造/首次 Read 的错误边界写进测试，避免先 `WriteHeader(200)`。 |
| T7 verification | **可实施，覆盖充分** | e2e 覆盖录制、两种 retention、授权矩阵、加密下载，能覆盖设计 D-P3-1/3/4/6/7。P2 blocked cast.Close 回归直击 `Done()` 语义变强的风险：`docs/plans/web-pty-attach/P3-plan.md:218-220`。环检项对齐 G022 leaf：当前 ptyrelay deps 无上层包命中，`go build ./...`、`go vet ./...` 已在 `.cache` 缓存下通过。 |

## 事实性冲突优先

1. **T5/T6 计划片段引用不存在的 `s.store` 字段**。当前 `Server` 没有 store 句柄：`internal/httpapi/server.go:88-123`、`:130-160`；`job.Service.meta` 私有：`internal/job/service.go:107-112`。按 `s.store.UpsertPtySession` / `s.store.GetPtySessionByJob` 实施会编译失败。需在计划中新增明确接线：要么 `job.Service` 暴露 `UpsertPtySession/GetPtySessionByJob/ExpireCastRecordings` 这类窄方法供 httpapi/job prune 使用，要么 `Server` 通过 setter 注入 `*jobstore.Store` 或窄接口。
2. **T3/T4 `ExpireCastRecordings` 签名表述不一致**。计划 T3 写 `ExpireCastRecordings(now int64)` 但同句要求 ttl 由调用方传入，T4 又写 `ExpireCastRecordings(now, castTTL)`：`docs/plans/web-pty-attach/P3-plan.md:126`、`:139`。实现前必须统一为 `(now, ttlSeconds)` 或 store 持配置二选一；按当前分层，store 不应持 config，推荐调用方传 TTL。
3. **T4 prune gate 片段少了 `startPruneLoop` 参数改动**。真实签名只有 `(c, jobs, ret, stop)`，函数内拿不到 `cfg.Storage.Cast.Enabled`：`internal/serve/serve.go:267-270`。计划需写清同步改为 `startPruneLoop(c, jobs, ret, cfg.Storage.Cast.Enabled, stop)` 或传完整 cast 配置。

## 硬问题

### 阻断：无

`hkdf.Key` 已修为双返回值，relay close owner/prune 事务/crypto envelope 三个重点对抗项没有发现必须推翻设计的阻断问题。

### 高：T5/T6 store 句柄缺失会导致按计划片段实现失败

这是唯一高风险返工点。`httpapi.Server` 目前只持 `*job.Service`，而不是 `*jobstore.Store`；直接按计划写 `s.store` 会失败。建议把计划改成以下二选一并锁定：

- **推荐**：在 `job.Service` 上新增 pty session 窄方法，内部转调 `s.meta`。优点是 httpapi 继续只依赖 job service，符合当前 handler 读取 job/result/artifact 的风格；`Service.Prune` 也已在 job 包内访问 `s.meta`，cast sweep 同层自然。
- **可选**：给 `httpapi.Server` 增加 `ptySessions ptySessionStore` 窄接口 + `SetPtySessionStore`，由 serve 用 `cr.Store` 注入。优点是少动 job.Service API；代价是 httpapi 直接依赖 jobstore-owned 元数据面，需要约束为接口避免扩大层级耦合。

## 验证

- `go version`: `go1.25.10 windows/amd64`
- `hkdf.Key` demo：`go run .\.cache\hkdf-demo` 输出 `32 <nil>`，证明双返回值写法编译通过。
- `go build ./...`：通过（`GOCACHE=.cache/go-build`, `GOMODCACHE=.cache/go-mod`）。
- `go vet ./...`：通过（同上）。
- `go list -deps ./internal/ptyrelay | rg "internal/(jobstore|job|config|httpapi|serve)"`：无输出，当前 leaf 边界干净。

## 判定

**判定：需改。** 不是设计回退，也不是 8T 全面不可实施；修正 T5/T6 的 store 注入/暴露方案，并统一 T3/T4 的 cast TTL sweep 签名与 prune gate 参数后，可据此逐 T 实施。阻断 0，高 1。
