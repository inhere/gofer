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
