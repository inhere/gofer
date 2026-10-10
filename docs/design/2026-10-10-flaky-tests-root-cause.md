<!-- template_id: design; template_version: 1.1.1 -->
# 偶发测试失败根因诊断（epic gofer-r7am）设计

> 状态：Draft 0.1 / 待人工计划批准
> 关联：epic **gofer-r7am**；汇总 issue gofer-uinh、gofer-kvyw、h-aii-pq8a；plan plan-20261010-153837-60401987（T1 诊断）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-10 | inhere + Claude | 初稿：逐用例根因表、横切修复、并行工作包、约定 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 背景与目标

全量负载下（Windows 全量、Linux `-race` 全量、CI 三平台）反复出现「单独重跑就过」的失败，每次靠人判断「已知偶发」，打断 CI 和发版验证。本文是 T1 诊断的产出：对 issue 中登记过的每个用例（以及诊断中新发现的用例）给出根因分类、证据和具体修复方案，并把修复拆成能并行的工作包。

思考模式 RIGOROUS。核心目标：每个用例的根因都有证据、修复方案可直接执行。scope freeze：只诊断、不修复（本次提交只有本文档，代码零改动）。停止条件：每个登记的用例都有结论；复现不了的标「未复现」并给出推断根因。

## 名词

- **A 类**：后台 goroutine / 子进程 / 终态 hook / 打开的文件在测试结束后仍存活。Windows 上表现为 TempDir 清理「being used by another process」，Unix 上表现为「directory not empty」或日志「sql: database is closed」。
- **B 类**：固定超时或真实时间计时，负载下不够用。
- **C 类**：只在特定时序下暴露的生产代码真 bug。
- **D 类**：测试隔离问题，包括全局状态未复位、依赖执行顺序、单独跑就失败、测试不封闭。
- **in-memory 翻转**：`finish()` 先在内存里把 job 置为终态（`internal/job/execute.go:683`），之后才落库（`:754`），再做 revoke、metrics、todo、knowledge、health、hook、Advance 等收尾。

## 范围与非目标

- 范围：gofer 仓库全部 Go 测试包中登记过、或本次诊断中出现的偶发用例。
- 非目标：本文不改代码，不做修复（T2–T4）；web 前端测试不在本次范围。

## 已确认事实与证据来源

- 基线 z-flaky @ 209ebea9。证据日志在工作区（WP）`tmp/flaky/` 下，文件名前缀 g1-…g4-、win-full-1.log、linux-full-race-1.log、ci-macos-raw.log。
- **Windows 全量**（host job 20261010-154117-ad913737，`go test -count=1 ./...`）有 2 个失败：
  - httpapi TestTrackerRepoSyncSourceRunnerAndFallback：stderr.log 被占用。
  - servicemgr TestSupervisorStopsAfterFastFailureBudget：expect 3、actual 10。
- **Linux `-race` 全量**（容器内，与 4 路诊断加压并发）：0 失败。
- **CI macOS go1.25 @ 209ebea9**（Actions job 114163957561）：worker TestCancelArrivingBetweenStartAndMappingIsHonoured 失败，耗时 0.10s，报错 `TempDir RemoveAll cleanup: unlinkat …/002: directory not empty`。断言本身已通过，失败发生在清理阶段。
- **已修复的参照**：
  - 04117561：删除刚完成的 job 后仍能从内存读到。
  - 2fe4b85e：job.Service 跟踪终态 hook，Core.Close 等待这些 hook。
  - 10e51bf1：job 包测试统一加 drainOnClose。
  - h-aii-tcpm（729cc895、456668cb）：worker 取消丢失。
- 规范依据：SR1403 / SR1410（最小充分修复）、SR1405.1（至少两个已验证重复用例才抽象）。本文提出的共享助手都满足 SR1405.1：每个都有 ≥2 个已验证用例。

## 1. 根因表

「复现」一栏中，`L` = Linux 容器，`W` = Windows 主机 job，`CI` = GitHub Actions。

### internal/httpapi、internal/mcpserver

| 用例 (pkg) | 类 | 根因 | 证据 | 修复方案 | 工作量 | 生产改动 |
|---|---|---|---|---|---|---|
| TestTrackerRepoSyncSourceRunnerAndFallback (httpapi) | A + D | 测试拿到 202 就返回，tracker-sync job 往往还没启动。drainJobs 调 `ListJobs(Limit:500)` 时没带 `All:true`（`httpapi/server_test.go:94`，`job/list.go:96-100,147`），带 tracker-sync 标签的隐藏 job 不会被取消，也不会被等待。job 起来后在 TempDir 里打开 stderr.log，并拉起**真实 PATH 上的 `gofer repo sync`**（`tracker_repo_sync_handler.go:73`；`selfProgram` 在 `job/credential.go:306` 不会把它映射到测试二进制）。 | **复现** W 全量 ad913737：`unlinkat …\self\<date>\<job>\stderr.log: being used by another process`。W 53dd7ec4 中 4 个 job 在关库之后才落 running 快照（`persist running snapshot … sql: database is closed`）。单跑 W 2c98cca3 ×10 通过。 | ① 换用共享 drain（`Service.Shutdown`，见 §2）。② 同步命令改为可注入：给 `Server` 加 `trackerSyncCmd` 字段，默认值 `gofer repo sync`，测试改用 testcmd，测试不再执行外部 gofer、也不再连真实 server。 | S | Y（②） |
| TestTrackerRepoSyncDispatch (httpapi) | A + D | 根因同上。 | 未复现。历史记录：gofer-uinh 2026-10-09 第 4 轮出现过 gofer.db 被占用。 | 同上。 | S | Y |
| TestCancelWorkflowAPI (httpapi) | A | `waitWorkflowStatus("cancelled")` 立刻返回：取消是同步把状态置为 cancelled（`workflow/cancel.go:31`），并不等 step-1 job。drain 只看内存是否已翻转为终态，于是关库正好落在 finish 收尾写库期间，以及不受跟踪的 `go s.wf.Advance`（`job/execute.go:856`）期间。 | 未复现失败，但能看到机制在运作：L race ×20 期间出现 315 行 `sql: database is closed`（revoke、metrics、agent health、work.job_outcome）；W 53dd7ec4 有同样的告警。历史：Windows gofer.db 被占用。 | 共享 Shutdown；测试改为 `jobs.Wait(stepJobID)` 等 step job 结束。 | S | N（共享 Shutdown 除外） |
| TestCreateWorkflow (httpapi) | A | 测试结束时 step1 仍在跑。step1 若在 drain 取消之前结束，`go wf.Advance` 会在 drain 拉完列表之后再 submit step2（`workflow/advance.go:85`），step2 在 TempDir 里建日志文件。 | 未复现（L ×10、W ×10、W ×5）；结论来自代码追踪。 | 共享 Shutdown：先关准入（admission），再取消并等待。 | S | N（同上） |
| TestPlanDispatchEndpoint (httpapi) | B（测试竞态） | 响应中的 todo 状态在 Submit 之后才重读（`job/plandispatch.go:185-190`）。testcmd job 很快结束时，`linkTodoOutcome`（`todolink.go:112`）可能已经把 todo 置为 done，断言 "doing"（`plandispatch_test.go:240`）就会失败。 | 未复现（L ×10、W ×15、单核争用 ×40）；原始报错未保存，靠代码推断。 | 断言接受 doing 或 done，或改为断言 `Dispatched` / job 关联。 | S | N |
| TestStatsIncludesUsage (httpapi) | B | usage 统计有 200ms 墙钟预算（`stats_handler.go:162` 的 `statsUsageBudget`）。第一个窗口超过 200ms 就标记 Partial 并跳过 7d 窗口（`jobstore/usagestats.go:57`）。TestStatsIncludesDBAndSessions 同样暴露在这个预算下。 | 未复现（L `-cpu 1` ×100、单核争用 ×40）；结论来自代码追踪。 | 测试里把 `statsUsageBudget` 设为 `time.Minute`，做法同 `:304` 处理 `statsDBBudget`。 | S | N |
| TestPtySessionIDCapturedFromTail (httpapi) | B + 生产低效 | `waitForLocalObserver(t, 3s)`（`pty_transcript_test.go:118`）太紧。`ptySessionCapture.observe`（`pty_session_capture.go:65-85`）每收到一个 chunk，都对已冻结的 64KB head 和整段 64KB tail 重跑正则，CPU 开销随 chunk 数 × 128KB 增长。另外 drain 对仅存在于库里的 running 行空等满 2s。 | **复现** L race ×20：1 次失败，报错 `condition not met within 3s`；与 4 个空转进程争用单核：21/21 失败（9–33s）。 | ① head 没有增长时跳过扫描，tail 只扫新增内容加一段 id 长度的重叠。② 等待改用负载伸缩助手（10s）。③ drain 不再轮询仅存在于库里的行。 | S | Y（①） |
| TestJobDeleteHTTPRemovesRecordKeepsAudit (httpapi) | C（主因已由 04117561 修复）+ 残余 C + A | 残余窗口 1：finish 在内存翻转（`execute.go:683`）与落库（`:754`）之间，GET 读内存会返回 done，而 DELETE 走 `jobstore.DeleteJob`，因库里仍是 running 被拒（`jobstore/delete.go:29`，"is not terminal"），**GET 与 DELETE 结论不一致**。残余窗口 2：`Service.DeleteJob` 把 entry 移出内存之后，finish 仍会为这个已删除的 job 写 metrics、health 和 hook 事件，产生孤儿行，这些写入也会活过测试。 | 单跑 L race ×10 通过。×200 的一轮中途按限压要求被终止：已跑部分 0 失败，但出现 46 行 `database is closed`。 | `Service.DeleteJob` 遇到仍在内存中的 entry 时，先有界等待 `entry.done` 再删，或返回 409「finishing」，与 GET 保持一致。测试侧由共享 Shutdown 覆盖。 | S | Y |
| TestRejectJobTool (mcpserver) | A | reject 加 resume 的后续动作在清理时仍在运行。mcpserver 自带的 drainJobs（`mcpserver/server_test.go:73`）同样只看内存翻转，而且不等终态 hook。 | 未复现失败；L race ×30 期间出现 31 行 `database is closed`（agent health 15、metrics 8、revoke 7）。 | 换用共享 Shutdown，删掉这份 drain 副本。 | S | N |

### internal/worker、internal/core、internal/runner/local

| 用例 (pkg) | 类 | 根因 | 证据 | 修复方案 | 工作量 | 生产改动 |
|---|---|---|---|---|---|---|
| TestTunnelE2EDeviceSideClose (worker) | **C** | `tunnel/splice.go:107-111` 的 `closeBoth()` 先调 `cancel()`、后发 `Close(StatusNormalClosure)`。在 coder/websocket v1.8.15 中，取消挂起 Reader 的 ctx 会直接硬关连接、不发 close 帧，于是对端有时收到裸 EOF，而不是正常关闭。这与负载无关，0.06–1.3s 内就会失败。 | **复现** L race ×150：3 次失败；临时补丁（先 Close 后 cancel）×150：0 次。W dbd2e5f3 无负载 ×20：2 次失败。历史日志：`read err=failed to get reader: … EOF, want a normal websocket close`。 | 读错误分支里先关闭两端，再 `cancel()`；`splice_test.go` 加回归测试。 | S | Y |
| TestWorkerMessengerRespawnsAfterIdleExit (worker) | **C**（负载下表现为 B） | `messenger/resident.go`：idle 计时器从进程启动时开始计（`:199` `touch`），`roundTrip`（`:86-135`）在请求进行中不暂停它。回复耗时超过 idle 窗口时，`stop()` 在请求中途杀掉进程，`Send` 返回 `resident messenger exited: stream closed`，且不重试。测试的 25ms idle 比负载下的进程启动时间还短。生产上（默认 idle 10min），idle 到期前刚到达的请求、或耗时超过 idle 的请求都会失败。 | **复现**：临时写的确定性测试（回复耗时 200ms），idle=50ms 失败、idle=2s 成功。负载下 race ×30：12 次失败；第二轮原代码 1/30，补丁 0/30。历史：W 4d8ef7b2。 | `roundTrip` 写入前暂停计时器，写入后再检查一次 `isStopped`（第一次尝试时重试），所有保留进程的退出路径都重新启动计时器；`resident_test.go` 加回归测试。 | S | Y |
| TestCancelArrivingBetweenStartAndMappingIsHonoured (worker) | A（+ 同链路潜在 C，见下一行） | 测试在 `jobs.Get().Status == cancelled` 时立即返回（`cancel_window_test.go:149`），而这只是内存翻转。finish 和子进程仍在 `Storage.Root` TempDir（`:52` 的第二个 TempDir，`002`）里写结果目录。`go cl.Run` 没有被等待（`:133`），job service 也没有 drain。 | **CI macOS 209ebea9**：失败耗时 0.10s，报错 `unlinkat …/TestCancelArriving…/002: directory not empty`，断言已通过，属于清理失败，**不是 cancel/mapping 逻辑竞态**。L ×30、W ×80（dbd2e5f3、a7eb900b）均未复现。 | 在 TempDir 之后注册 `t.Cleanup`，依次：`jobs.Wait(localID)` → `Service.Shutdown`；取消 Run 并等它返回 → `cl.WaitIdle`。提供 `startClient(t, cl)` 助手，覆盖 13 处没有走 `stopWorkerClient`（`e2e_interaction_test.go:310`）的 `go cl.Run`。 | S | N |
| （无独立用例）worker 取消停放竞态 | **C（潜在）** | recvLoop 的取消分支（`worker/client.go:1192-1198`）中，查映射（`localJobID`）和停放取消（`recordPendingCancel`）是两步，与 dispatch 的 `putJobMapping`（`dispatch.go:199`）加 `takePendingCancel`（`:214`）之间没有原子性。如果 dispatch 恰好在这两步之间建立映射并查完停放表，取消会被停放到一个已经不会再有人读的位置：hub 认为 job 已取消，worker 却继续执行。 | 临时副本中只在这两步之间插入延迟（不改逻辑），即复现 `did not reach "cancelled" in time (status=running)`。 | 停放之后再查一次映射，若 `takePendingCancel` 成功则调 `jobs.Cancel`；或用同一把锁保护两步。通过测试缝在 `cancel_window_test.go` 加回归测试。 | S | Y |
| TestE2ECancelOverWS (worker) | C（已由 h-aii-tcpm 修复） | 根因是取消丢失，已修。这个用例会先等 job 进入 running，碰不到上一行的竞态。 | L race 30/30、W 20/20 与 60/60 全部通过。 | 无需改动；从偶发清单中移除。 | — | N |
| TestHubWorkerSelectorCarriesCapabilities (core) | B（+ A 噪声） | `worker_selector_test.go:31` 的拨号、注册、ack 共用一个 5s ctx。同一份日志中，失败之前出现 `persist running snapshot / revoke … sql: database is closed`：core 包内某个测试在关库后仍留有 running job。 | 未复现（L race `-cpu 1` ×50、W ccd61ff0）。历史：W 5e5e4584，`dial … context deadline exceeded`，6.65s。 | 改用负载伸缩 deadline（约 30s，不超过 `t.Deadline()`）。core 包测试统一走 Shutdown（§2）。 | S | N |
| TestLocalCancelKillsProcessTree (runner/local) | B | `testcmd.Path(t)` 在 Run goroutine 里调用（`proctree_test.go:52`），助手程序的新鲜度检查、锁等待（最长 2min）和可能触发的 `go build` 都算进了 `waitChildPID` 的 30s。这是包里第一个测试，构建开销全落在它身上。另外，`helperFresh` 会扫描 `.worktrees/`，任何 worktree 里有改动都会让主 checkout 的助手程序判为过期，在跑测试中途触发重建。 | 实测负载下（load 15.8）构建助手：缓存热 16.7s，缓存冷 **35.8s，超过 30s**。助手已就绪时 L race ×20 通过。历史失败发生在同一 checkout 里两个全量并发跑的时候。 | 在启动 goroutine 和计时之前先调 `testcmd.Path`；`waitChildPID` 同时监听 `done`，让 Run 的错误直接暴露。`testcmd.helperFresh` 跳过 `.worktrees`。 | S | N |

### internal/servicemgr、internal/commands、internal/serve

| 用例 (pkg) | 类 | 根因 | 证据 | 修复方案 | 工作量 | 生产改动 |
|---|---|---|---|---|---|---|
| TestSupervisorStopsAfterFastFailureBudget (servicemgr, Windows) | B | 测试子进程把 `fastFailure` 设为 500ms（`task_windows_test.go:235`）。只要有一次运行超过 500ms，失败计数就清零（`supervise_windows.go:168-172`）。运行时长包括启动新拷贝的测试二进制、Go 初始化和 100ms sleep，负载下会超过 500ms，于是重启次数超过 3，`:186` 的计数断言失败。 | **复现** W 全量 ad913737：`expect int(3) actual int(10)`，8.38s。W a718a048 在负载下 20/20 通过，单次运行时长 0.48–1.05s，已超过阈值，只是这一轮恰好没清零。 | 子进程里把 `fastFailure` 设为 `time.Minute`：假 server 总是在 100ms 后退出，测试只断言次数。 | S | N |
| TestStandaloneSplitDirectories (servicemgr, Windows) | B | 同一个子进程、同一个阈值、同样的计数断言（`:220`）。TestSupervisorDetachesConsole 也受影响。 | 未复现（W 负载下 10/10 通过）；历史：gofer-uinh 2026-10-09。 | 同上，一行改动同时覆盖三个用例。 | S | N |
| TestWaitForReloadResult (commands) | B（Windows） | 成功路径只等 500ms（`reload_wait_test.go:17`），每 100ms 轮询一次（`reload_wait.go:24-28`），写入方还先 sleep 20ms。Windows 负载下整个进程会停顿。 | **复现** W a718a048：2/300 失败（单次耗时 1.68s、0.62s）。L race ×600（load 41）：0 失败。 | 成功路径的超时改为 10s（第 38 行同理）：调用一拿到回执就返回，不增加耗时。只有预期超时的用例保留 250ms。 | S | N |
| TestJobReviewPrintsSections (commands)，同因：TestJobShowPrintsReview、TestJobShowPrintsRecoveringSince | D | `fmtServerTime` → `serverTZ()` 把 server 时区缓存在包级全局变量里（`timefmt.go:60-72`）。缓存未命中时会向 stub 发 `GET /v1/stats`，而 stub 的 default 分支调 `t.Fatalf`（`job_review_test.go:229`）。整包跑时，前面某个测试的 `setServerTZ` 已经把缓存填好（`job_test.go:672`、`retry_test.go:173`），所以不暴露。 | **确定性复现**：commands 包 348 个测试逐个单进程运行，正好这 3 个每次都失败，报错 `unexpected request: GET /v1/stats`。 | commands 包加 TestMain，固定 `serverTZFetch = func() (int, bool) { return 0, false }`，并提供复位助手。stub 改用 `t.Errorf` + `http.Error`（在非测试 goroutine 里调 `t.Fatalf` 是错误用法，这也是失败信息打印两遍的原因）。 | S | N |
| plan / schedule 相关测试的 flag 全局变量泄漏 (commands，isolateTodoCLI 等) | D | gcli 把 flag 绑定到约 76 个包级 option 结构体上，从不复位，所以每个测试新建 NewApp 也没有用。`resetPlanFlagGlobals`（`plandispatch_test.go:32`）只复位 planCreate、AddTodo、SetTodo；plan_test、plan_chain_test、plan_tags_test、plan_autobind_test 完全不复位；`resetScheduleTestState`（`schedule_test.go:145`）漏了 `agentArgs` 和 `worktreeBase`。 | **复现** `-test.shuffle` 种子 1–8 中 5 个失败（默认顺序全过）。例如种子 8：TestPlanSetTodoAppendNote 报 "append must not carry a status, got ready"；TestScheduleAddBuildsJobRequestFromRunFlags 读到了 TestJobRunWorktreeFlags 泄漏的 `--x 1` / `v1.2.0`。 | 方案甲（推荐）：在 init 时给所有 option 全局变量做快照，在 `NewAppWithBuildInfo`（`app.go:18`）开头恢复（新增 `flagreset.go`），然后删掉零散的复位助手。CLI 一个进程只跑一条命令，行为不变。方案乙（更轻）：只加测试用的 `newTestApp(t)`，做同样的复位。 | M | Y（方案甲）/ N（方案乙） |
| TestCLIIgnoresDotenvTokenInsideJob (commands，新发现) | D | 生产代码 `runWorkerInit` 调用 `os.Setenv(GOFER_WORKER_TOKEN)`（`worker_init.go:221`）。worker-init 测试事先没有 `t.Setenv`，"issued-token" 就留在了进程环境里，之后断言该变量不存在的测试失败（`client_token_test.go:52`）。 | **复现** shuffle 种子 1、3、7。 | worker-init 测试先调 `t.Setenv(workerTokenEnv, "")`，由测试框架负责还原。 | S | N |
| TestPersistPublishesJobs (serve) | B（测试里的帧顺序竞态，不是超时不够） | `NotifyJob`（`pushhub/hub.go:323-331`）的 jobs 失效通知经 coalescer 异步发出，而 `evt job:<id>` 是同步广播，所以 `inval jobs` 可能先到达。`expectFrame`（`livepush_test.go:36-50`）会丢弃不匹配的帧：等 evt 时把 inval 扔掉了，第二次等 inval 就 2s 超时（`:67`）。不同 topic 的帧之间本来就没有顺序保证，不是生产 bug。 | **复现** L 3000 次 × cpu 1,2,8：出现与 CI 完全相同的报错 `no inval frame on jobs: context deadline exceeded`。用 `go test -overlay` 加的诊断测试记录到帧顺序 hello、inval:jobs、evt:job:j-1。 | 新增助手：持续读帧，直到两种帧都收到为止，不要求顺序。 | S | N |
| （无用例，代码推断）reload 回执 rename | C（疑似，未复现） | server 用 `os.Rename(tmp, path)` 发布回执（`config/reload.go:44-51`），CLI 每 100ms `os.ReadFile` 一次。Windows 上替换另一个进程正打开的文件会失败，server 只记一条 warn，回执就再也不会写出：`serve reload` 报 10s 超时，实际 reload 已经成功。 | 仅代码推断；每次轮询的窗口只有微秒级，很少发生。 | Windows 上 rename 失败时短暂重试几次。 | S | Y |

### internal/work、internal/job

| 用例 (pkg) | 类 | 根因 | 证据 | 修复方案 | 工作量 | 生产改动 |
|---|---|---|---|---|---|---|
| TestItemViewHealth (work) | A | `svc.Park`（`health_test.go:148`）通过 `s.spawn` 起后台 auto-handoff（`work/requests.go:205-219`）。`newSvc`（`service_test.go:46-55`）关了 store，却从不调已有的 `svc.WaitIdle()`，goroutine 于是去查已关闭的库。在 Windows 上，若它在关库那一刻正持有连接，w.db 就删不掉。同样的缺口还在 serve/livepush_test.go:136、steward_test.go:122、today/{today,lanes,snooze}_test.go 和 httpapi 测试 server。生产侧：serve 只对 work、steward 循环发停止信号、不等待（`serve/serve.go:319-335`），Core.Close 也不等它们。 | L race ×300 全部通过，但其中 **298 次**都打出 `work.auto_handoff_failed … sql: database is closed`；整包跑时这条告警出现在下一个测试名下，说明 goroutine 活过了本测试。CI Windows 78149b10 曾因 w.db 被占用失败。W f5722191 ×50 未复现文件占用。 | 测试：在 `st.Close` 之后注册 `t.Cleanup(svc.WaitIdle)`（LIFO，先于关库执行）。生产：work.Service 和 steward 增加有界 `Close()`：停循环并等待后台任务；serve 在 `cr.Close` 之前调用。 | S | Y |
| TestSubmitExecNoSessionInjection (job，h-aii-pq8a) | A（已由 10e51bf1 修复） | 2026-09-18 失败时测试还没有 drain，job 在清理之后仍往 TempDir 和库里写。当时没抓到的失败信息，最可能是 TempDir 清理错误，而不是两个 Fatalf 之一。 | 用 overlay 还原旧版测试，负载下 ×300 × `-cpu 1,4`：600/600 次都有关库后写入（共 2397 条告警）。当前版本同样负载 600 次：0 告警，全部通过。 | 无需改动；附上证据关闭 h-aii-pq8a。 | — | N |
| TestSessionAwaitingReplyNotifiesAfterDelay (job) | B | 提醒是进入 awaiting 时启动的 1s 计时器（`session_notify.go:24-41`）。测试先轮询到 awaiting，再固定 sleep 1200ms（`session_notify_y2_test.go:60`），留给计时器触发并写入事件和投递的时间最多约 200ms。兄弟用例 CancelledBySay（sleep 300ms）、OncePerWait（两次 1200ms）问题相同。 | 未复现（L 负载 ×30 × `-cpu 1,4`，0 失败）；结论来自代码追踪。 | 改为轮询直到出现 1 条投递，deadline 5s × 负载系数。 | S | N |
| TestSessionJobMaxSessionTimeout (job，新发现) | B | `MaxSessionSec: 1`（`session_job_deadline_x3_test.go:129`）。这 1s 预算从第一轮 prompt 之前就开始算（`runner/acp/runner.go:271-279`），首轮 agent 往返加 3 次写库必须在 1s 内完成，Windows 负载下会超时，job 在进入 awaiting 之前就结束了。TestSessionJobTimeoutAppliesPerTurn 的 1s 单轮超时有同样的风险。 | W a718a048 失败 1 次（3.41s，报错文本被过滤）；L ×180 未复现。根因靠推断。 | 提交时用空 `Prompt`，让会话直接进入 awaiting，预算内不跑任何一轮；或把预算提到 3s，并配合负载伸缩等待。 | S | N |

### 汇总

- 共 **28 行**：
  - 25 行是具名用例。其中新发现的有 TestSessionJobMaxSessionTimeout、TestCLIIgnoresDotenvTokenInsideJob，以及 TestJobReviewPrintsSections 行里的两个同因用例。
  - 1 行是一组 flag 泄漏。
  - 2 行是没有对应用例的 C 类发现。
- 按主类计：

  | 类 | 行数 | 说明 |
  |---|---|---|
  | A | 8 | 其中 h-aii-pq8a 那条已修复 |
  | B | 11 | 其中 TestPersistPublishesJobs 为帧顺序竞态 |
  | C | 6 | 包括：TestJobDeleteHTTPRemovesRecordKeepsAudit（主因已由 04117561 修复，有残余窗口）、TestE2ECancelOverWS（已由 h-aii-tcpm 修复）、reload rename（疑似） |
  | D | 3 | 另外 tracker sync 两条以 D 为次类 |

- **本次新确认的 C 类真 bug（4 个）**：
  - C1：job 在内存中先于落库翻转为终态，导致 GET 与 DELETE 结论不一致；删除后还会写孤儿行；关闭时收尾写入丢失。
  - C2：tunnel `closeBoth` 先 cancel 后 Close，正常关闭被硬关取代。
  - C3：messenger 的 idle 计时器在请求进行中不暂停，长请求会被中途杀掉。
  - C4：worker 取消停放的两步非原子，取消可能丢失。
- **疑似 C 类（未复现）**：reload 回执的 Windows rename。
- **生产低效**：pty session id 捕获时重复扫描正则。

## 总体方案

按根因而不是按用例修。

- **A 类**：用一个生产侧的关闭协议（`job.Service.Shutdown`，以及 work / steward 的 `Close`）一次覆盖所有后台工作，测试统一走它，不再各写一份 drain。
- **B 类**：成功路径的等待统一改用负载伸缩等待；测试中的生产阈值放宽。
- **C 类**：每个真 bug 单独提交，并附回归测试。
- **D 类**：commands 包隔离，CI 加 `-shuffle`。

细节见 §2，分包见 §3。

## 架构

后台工作的归属与关闭顺序（修复后）：

```text
serve / 测试 teardown
  └─ Core.Close
       ├─ job.Service.Shutdown(ctx)    关准入 → 取消 entries → 等 entry.done → 等 bg 组
       │     bg 组 = 终态 hook + wf.Advance + workflow 重试 + wakeup/adopt/stall + session 计时器
       ├─ work.Service.Close / steward.Close   停循环 → WaitIdle
       └─ Store.Close                  此时已没有在用的连接
  └─ t.TempDir RemoveAll               最后执行（t.Cleanup 按 LIFO）
worker 测试：startClient(t, cl) → cancel Run → 等 Run 返回 → cl.WaitIdle → job Shutdown
```

## 关键流程

job 结束的时间线：这是 A 类和 C1 共同的根源。

```text
execute 结束 → finish():
  :683 内存状态 = 终态          ← GET 已返回 done；旧 drain 在此认为「结束」
  :754 persist(库内终态)        ← 此前 DELETE 报 "not terminal"（C1）
  revoke / metrics / todo / knowledge / health / plan / wakeup …（写库）
  :822 terminalRunning.Add      ← 2fe4b85e 只从这里开始跟踪
  hooks（goroutine，写库）
  :856 go wf.Advance            ← 不受跟踪，可能 submit 新 job
  close(entry.done)             ← 修复后 drain 与 DeleteJob 以此为准
```

## 2. 横切修复

### 2.1 生命周期规则

测试里启动的每个组件（job、workflow Advance、终态 hook、work / steward 后台任务、worker client Run、子进程），必须在 TempDir 清理和关库之前**停止并等待结束**。

- 「job 已结束」的判定以 `entry.done` 加后台组清零为准，**不以** `Get().Status` 是终态为准，因为那只是内存翻转。
- `t.Cleanup` 按 LIFO 执行，注册顺序固定为：`t.TempDir()` → 打开 store 并 `t.Cleanup(st.Close)` → 构建服务并 `t.Cleanup(svc.Shutdown)`。这样 Shutdown 最先执行，关库次之，删目录最后。

### 2.2 生产侧：`job.Service.Shutdown(ctx)`（新增 `internal/job/shutdown.go`）

1. 复用 `upgrade_admission.go` 中 `CloseUpgradeAdmission` 的机制关闭准入，此后新的 Submit 和 Advance 一律拒绝。
2. 取消 `s.jobs` 中的全部 entry，隐藏 job 也包括在内。
3. 等待每个 `entry.done`。
4. 等待一个服务级后台组 `bg`。这个组覆盖：终态 hook、`go wf.Advance`（`execute.go:856`、`review.go:157`）、`workflow/terminate.go:43`、`workflow/advance.go:467` 的 AfterFunc 重试、wakeup（`wakeup.go:419`）、adopt（`adopt.go:411`）、stall（`stall.go:78`）、remote_interaction（`:40`）、session-notify 计时器和 job retry 延迟。所有 AfterFunc 回调都先检查 closed 标志。
5. `bg.Add` 移到状态翻转之前，或者在 finish 开头登记。这同时修掉 `WaitTerminalHooks` 的 WaitGroup 误用（`onterminal.go:56-68`）：计数为 0 时的 Add 与 Wait 并发，且超时后遗留的等待 goroutine 可能触发「WaitGroup is reused」panic。

**Core.Close** 改为：先 `Jobs.Shutdown(10s)`，再关闭 work / steward，最后 `Store.Close`。**C1** 一并修复：`DeleteJob` 等 `entry.done`（有界），或返回 409。

这个 drain 必须是生产包里的导出方法，不能放进 testutil：job 包的内部测试一旦 import 一个依赖 job 的 testutil，就会形成 import 循环。三份 drainJobs 副本都替换掉：`job/service_test.go:790`、`httpapi/server_test.go:91`、`mcpserver/server_test.go:73`。

### 2.3 生产侧：work / steward 的 `Close()`

work.Service 已有 `spawn` + `WaitIdle`，只需加上「停止 Run 循环并等待」；steward 同理。`serve.go:319-335` 中「只 close 停止 channel」改为调用有界 Close，并放在 `cr.Close` 之前。

### 2.4 测试助手：`internal/testutil/wait`（新增，约 60 行，P0 前置）

- `wait.Scale()`：读 `GOFER_TEST_TIMEOUT_SCALE`，默认 1；CI Windows 设为 3。
- `wait.For(t, d, cond, what)`：返回的 deadline 为 `d × Scale()`，且不超过 `t.Deadline()` 减去余量；超时报错时带上 `what`，以及最后一次观测值。
- 现有 21 个各自实现的 `waitFor` / `eventually` 不一次性替换：只在修到的用例里迁移，新代码必须使用 `wait.For`。

### 2.5 封闭性

- 测试不得执行 PATH 上的 `gofer`，也不得连接真实 server：tracker sync 命令改为可注入；子进程统一用 testcmd。
- `testcmd`：在包的 TestMain 里预热（或者规定计时开始前先调 `Path`）；`helperFresh` 跳过 `.worktrees`。

### 2.6 commands 包隔离

- 新增 TestMain：serverTZ 改为离线 stub，并 unset `GOFER_*_TOKEN`。
- flag 全局变量在 init 时快照，在 `NewApp` 里恢复（§1 方案甲）。
- 在 CI 增加 `go test -shuffle=on ./internal/commands/ ./internal/serve/`，作为 D 类守卫。

## 3. 修复计划：并行工作包

**P0 前置**（S，先合入 main）：`internal/testutil/wait/wait.go`（+ `wait_test.go`）。各工作包只新增对它的引用，不修改它。

下面四个包互不改同一个文件，可以放在各自的 worktree 里并行开发；按影响从大到小排序。

| 包 | 内容 | 影响 | 工作量 | 文件 |
|---|---|---|---|---|
| **WP-A** job 生命周期 + httpapi / mcpserver / core 的 A 类，含 C1 与 tracker 封闭 | 主类 A，涵盖 Windows 全量失败的大头 | 最高 | M–L | `internal/job/{service,execute,onterminal,review,wakeup,adopt,stall,remote_interaction,session_notify,delete,cancel}.go`、新增 `internal/job/shutdown.go`、`internal/job/workflow/{advance,terminate}.go`、`internal/core/core.go`、`internal/job/service_test.go`、`internal/job/delete_test.go`、`internal/job/session_notify_y2_test.go`、`internal/job/session_job_deadline_x3_test.go`、`internal/httpapi/{server_test,workflow_test,caller_test,tracker_repo_sync_test,tracker_repo_sync_handler,server}.go`、`internal/mcpserver/server_test.go`、`internal/core/worker_selector_test.go`（含 core 包测试统一 Shutdown） |
| **WP-B** worker / tunnel / messenger 的 C 类 + worker 测试生命周期 + runner/local | C2、C3、C4 三个真 bug，均附回归测试 | 高 | S–M | `internal/tunnel/{splice,splice_test}.go`、`internal/messenger/{resident,resident_test}.go`、`internal/worker/{client,cancel_window_test,e2e_interaction_test}.go`（含 `startClient` 助手）及调用 `go cl.Run` 的 `internal/worker/*_test.go`、`internal/runner/local/proctree_test.go`、`internal/testutil/testcmd/testcmd.go` |
| **WP-C** commands 隔离 + servicemgr + reload | D 类全部；Windows 全量里那条 servicemgr 失败（一行修复） | 中高 | M | `internal/servicemgr/task_windows_test.go`、`internal/commands/{app.go, flagreset.go(新), main_test.go(新), job_review_test.go, job_test.go, worker_init_test.go, reload_wait_test.go, plandispatch_test.go, plan_chain_test.go, schedule_test.go}`、`internal/config/reload.go`（疑似 C）、`.github/workflows/go.yml`（加 shuffle 步骤） |
| **WP-D** work / steward / serve 收尾 + httpapi 计时 | A（work 后台任务）+ B | 中 | S–M | `internal/work/{service,service_test}.go`、`internal/steward/{steward,steward_test}.go`、`internal/serve/{serve,livepush_test}.go`、`internal/today/{today,lanes,snooze}_test.go`、`internal/httpapi/{pty_session_capture,pty_transcript_test,local_pty_observer_test,stats_handler_test,plandispatch_test}.go` |

依赖与冲突说明：

- httpapi 测试 server 里给 work 加 `WaitIdle` 的改动归 WP-A，因为 WP-A 持有 `server_test.go`；WP-D 只改 work 包本身。
- `Core.Close` 调用 work / steward 的 Close，这一步放到 WP-A 和 WP-D 都合入之后，作为一个小提交接线（`core.go`），也可以由 WP-A 先预留调用点。
- 每个包都要按 SR1403 对每个 C 类真 bug **单独提交**，并附回归测试（epic 验收要求）。
- T5 验收：Windows 全量连续 3 轮、Linux `-race` 全量连续 2 轮零失败。

## 4. 修复后写入 AGENTS.md / gofer-dev-verify 记忆的约定

1. **测试生命周期（新增 AGENTS.md 规则 G05x）**：测试里启动的一切组件都要在 TempDir 清理和关库之前停止并等待。job 一律用 `Service.Shutdown`，work 用 `WaitIdle` / `Close`，worker client 用 `startClient`。禁止把 `Get().Status` 是终态当作 job 已结束。`t.Cleanup` 注册顺序：TempDir → store.Close → Shutdown。
2. **计时**：成功路径的等待一律用 `testutil/wait.For`，deadline 宽松（5–10s × scale），成功后立即返回。只有断言「应当超时」的用例才用短 deadline。测试里设置的生产墙钟阈值（fastFailure、budget、idle）要远高于测试的实际耗时。新测试禁止「固定 sleep 后断言」。
3. **stub handler**：不得在 handler goroutine 里调 `t.Fatalf`，改用 `t.Errorf` + `http.Error`。
4. **全局状态与环境**：测试改环境变量一律用 `t.Setenv`；会写全局的生产代码，测试必须复位。commands 测试通过 `newTestApp` / TestMain 隔离；CI 跑 `-shuffle=on`。
5. **封闭性**：测试不执行 PATH 上的 gofer、不连接真实 server，子进程统一用 testcmd，且在开始计时之前取得 `testcmd.Path`。
6. **偶发分诊流程（写入 gofer-dev-verify）**：
   - 保存完整失败文本：不要过滤 `--- FAIL` 之后的行，Windows job 日志也要完整保留。
   - 按 A/B/C/D 归类。
   - D 类用单进程逐个跑测试，或用 `-shuffle` 检查。
   - A 类在 Linux 上看 `database is closed` / `directory not empty` 告警。
   - 加压时同一时刻最多 1 个加压脚本，忙循环不超过 nproc/2，单次不超过 120s，用完确认已退出；优先用 `-cpu 1`、`taskset` 和 `-race` 自带的减速，`-count` 控制在 20–40。

## 安全、数据、运维与回滚

- 生产改动集中在 job 关闭路径、DELETE 语义（可能新增 409）、tunnel 关闭顺序、messenger idle 和 worker 取消停放，都是可回滚的局部改动。
- DELETE 若改为返回 409，属于 HTTP 行为变化，需要同步 gofer-usage skill（G045）。
- `Shutdown` 必须有上限（10s），避免受管升级时的 drain 被拖长。

## 决策

- D1：job 结束的判据从「内存状态为终态」改为「`entry.done` 关闭且 bg 组清零」，测试与生产（Core.Close、DeleteJob）使用同一判据。
- D2：drain 作为生产包的导出方法（`Service.Shutdown`）提供，不放在 testutil：放在 testutil 会形成 import 循环，而且生产关闭本身也需要它。
- D3：计时修复优先改为事件驱动；无法改成事件驱动的，才用 `testutil/wait` 的负载伸缩 deadline，不再单纯调大常量。
- D4：每个 C 类 bug 单独提交并附回归测试；TestE2ECancelOverWS、TestSubmitExecNoSessionInjection 已修复，附证据后从清单移除。

## 待确认事项

1. C1 的处理选「DELETE 等 `entry.done`」还是「返回 409 finishing」？倾向前者（有界等待 2s，超时再返回 409）。
2. commands flag 复位选方案甲（生产代码里快照恢复）还是方案乙（仅测试助手）？
3. reload 回执的 rename 重试（疑似 C）是否纳入 WP-C，还是先只登记 issue？

### 已定（2026-10-10，协调会话）

1. C1：DELETE 有界等待 `entry.done`（2s），超时返回 409 finishing。
2. commands flag 复位：方案乙（仅测试助手 + TestMain，不改生产代码）。
3. reload 回执 rename：纳入 WP-C，作为低成本加固（Windows 下目标被占用时有限次重试），无需先复现。
4. P0 `internal/testutil/wait` 已由协调会话写好并合入 main：`wait.Scale()` / `wait.Timeout(t, d)` / `wait.For(t, d, what, cond func() (bool, any))` / `wait.Until(t, d, what, cond func() bool)`。

## 结论与人工计划 Gate

诊断已完成：每个登记的用例都给出了类别、根因和修复方案，未复现的已标注。进入修复（T2–T4）需要人工确认 §3 的工作包划分和上面三个待确认项。本文不授权实施。
