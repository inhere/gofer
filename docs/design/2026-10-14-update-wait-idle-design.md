# 一键更新「空闲时更新」设计

- issue：gofer-dzv3
- 前置：[一键更新设计](2026-10-13-update-apply-design.md)（gofer-29gi）
- 相关：[待批 job（--hold）设计](2026-10-10-hold-for-approval-jobs-design.md)（gofer-9b1b，停住机制同源）
- 状态：已实施（2026-10-14）

## 一、现状与问题

一键更新（`POST /v1/update/apply`）下载并校验新版本后，调用 `serve upgrade` 换二进制并重启。`serve upgrade`（`internal/serve/upgrade_drain.go`）的排空做法是：

1. 立刻**关闭准入**（`job.Service.CloseUpgradeAdmission`）：此后 `BeginUpgradeWork` 返回 `ErrUpgradeDraining`，新提交得 **503**——plan 链、定时任务、重试、唤醒、leader 等内部生产者同样被拒；
2. 等所有 runner 上 `queued / running / awaiting_input / pending_interaction / waiting_dir / recovering` 的 job 归零（只排除空闲的 ACP 持续会话）；
3. 期限写死 5 分钟，超时就放弃（`serve upgrade` 返回失败，一键更新记 `failed`）。

agent job 常跑 20–60 分钟，新 job 又源源不断，于是**永远等不到**那个时刻；用户反馈「一直有 job 在跑，没有好的更新时机」。2026-10-14 主机上实测的一次失败，apply 的 error 只有 `upgrade the server: exit status 2 (output: …serve-upgrade.log)`——连「排空超时」都看不出来。

## 二、目标行为

一键更新新增一个阶段，把「等不到人的活干完」换成「让机器空下来」：

```
downloading → waiting_idle → restarting → upgrading_workers → done
                                            ↘ failed / cancelled
```

- **downloading**（旧 server）：下载、校验照旧。
- **waiting_idle**（旧 server，新）：
  - 打开「为更新停住」开关：**新 job 照常接收，但不启动**——走 hold 的停住路径（阶段 A），停在 `paused_for_update`；
  - 运行中的 job 继续跑；
  - **不关闭准入**，所以不会出现 503；
  - 轮询「会阻塞重启的 job」（§四），归零即进入 restarting；到 `wait_idle_sec`（默认 7200）还没空闲就放弃，或管理员 `DELETE /v1/update/apply` 取消。
- **restarting**：仍然是 `serve upgrade`，但此时排空应当瞬间完成；开关**保持打开**，所以排空期间（关闭准入到切换完成之间）进来的提交也是停住，而不是 503。
- 新 server 起来后（`ResumeUpdateApply`）**先放行停住的 job**，再处理勾选的 worker。apply 结束、失败、超时、取消的**所有出口**都放行（§六）。

## 三、停住方式的选择：新增 `paused_for_update`

阶段 A 的实现方式是新增一个非终态 `paused_for_update`，而不是给 `awaiting_approval` 加一个可区分的 origin/kind。理由：

- `awaiting_approval` 被十余处读路径当成「**等人**做决定」：`internal/today/cards.go` 的待批卡片、`internal/notify` 的默认通知触发集、hold 的过期清理（`jobstore.ListDueHolds`）、批准接口与权限（`ApproveJob`）、web 的 Board 筛选 / ReviewQueue / 审批面板 / JobDetail 首屏面板。复用状态就必须在**每一处**加「kind != update」过滤，漏一处就会出现「更新等空闲时把新 job 显示成待批准、并给它发一条『去批准』通知」，或更糟：过期清理把停住的 job 取消掉。
- 两者的语义也不同：待批 job 在等一个人，停住的 job 在等**同一个更新自己**（新的 server 起来 / 失败 / 超时 / 取消时放行），不需要也不允许批准。

机制上则**完全复用** hold：

- 一行 `jobs` 记录（`request_json` 存**收到的原请求**、`hold_json` 存 digest 与 carry），没有内存 entry、不占并发名额、不加目录锁、不派发；
- 放行走与 `ApproveJob` 同一条路径：同一个 job id，用原请求再 `submitAdmitted` 一次（摘要校验不适用——没有人批准过什么，所以放行时不做 drift 判定，也不在行上留 hold 记录，改由 `job.paused_for_update` / `job.update_resumed` 事件与行本身的历史留痕）；
- `parkJob(parkSpec)` 是两者共用的停住实现（`holdJob` 只是 `parkSpec{Status: awaiting_approval, Kind: ""}` 的一层）。

其它几个必须成立的细节：

- **重启后不被判失败**：`awaiting_approval` / `paused_for_update` 都不在 `jobstore.nonTerminalJobStatuses`（`queued / running / waiting_dir`）里，`ReconcileOrphanJobs` 不会碰它们。
- **不受 hold 过期清理影响**：`ListDueHolds` 只取 `status = 'awaiting_approval'` 且 `hold_expires_at > 0` 的行；停住的 job 没有自己的期限（`ExpiresAt = 0`，期限属于这次更新）。
- **内部生产者不报错**：plan 链派发、定时任务、重试、唤醒、leader 最终都走 `Submit`，因此在停住点被停住而不是失败（测试里至少覆盖 plan 链派发）。放行时这些生产者设的 `json:"-"` 标记（`WorkflowID/StepIndex/FanIndex/LeaderOfPlan/Steward/Messenger/…`）由 hold 的 carry 记录还原，否则放行后会丢掉「它是哪个 workflow 的哪一步」这类身份。
- **开关只在进程内**：它属于正在等待的那次更新。重启后的 server 不去继承它，而是**放行**它找到的停住 job（§六）。

## 四、会阻塞重启的 job 的定义

「准入空闲」和「可以重启」是同一个问题，现在只有**一个**答案（`jobstore.upgradeBlockerWhere`，经 `job.Service.CountUpgradeRestartBlockers` 同时供一键更新的等待与 `serve upgrade` 的排空使用，两处不可能再各说一套）：

| job | 是否阻塞重启 | 原因 |
| --- | --- | --- |
| 本机 runner（`local`）/ peer-http 的 `queued / running / awaiting_input / pending_interaction / waiting_dir` | 阻塞 | 它的编排（dispatch entry / 进程）在这个 server 进程里，重启就没了；`ReconcileOrphanJobs` 会把它判 `failed` |
| worker 已接手（`worker_id` 非空，或 runner 是配置里 `type: worker` 的名字）的 `running / awaiting_input / pending_interaction` | **不阻塞** | job 的进程在 worker 上；新 server 启动时把它放进 `recovering`，worker 重连后由 R4 认领（adopt）继续跑 |
| 同一类 worker job 的 `queued` / `waiting_dir`（**尚未交给 worker**） | 阻塞 | 它还在 server 的 dispatcher 手里，没有任何 worker 进程在跑它；重启会让它进 `recovering` 后以 `worker lost` 失败（无人可认领）。选「阻塞」而不是「也停住」：它已经在派发路径中，正常几秒内就下文；停住要先把它从 dispatcher 手里摘出来，代价大而收益只是几秒，而且失败模式（丢一个 job）比多等几秒严重得多 |
| `recovering`（worker 断线持有 / 本机会话恢复中） | 不阻塞 | worker 侧进程可能还在跑，新 server 会认领；本机恢复的行由启动时的恢复窗口处理 |
| 更新自己停住的 `paused_for_update` | 不阻塞 | 它就是这次重启**不等**的活：更新放行它才开跑 |

`upgradeInFlightStatuses` 里原来的 `recovering` 被去掉正是这次修复的一部分：以前它会让排空去等一批「重启后本来会被新 server 接管」的 worker job。

## 五、与 worker 恢复的关系

- 重启不是「杀掉所有活」，而是「换一个 server 进程」：worker 上的 job 归 worker，server 重启后 `ReconcileOrphanJobs` 把在途的 worker job 放 `recovering`，`ReconcileAdoption`（RECOV-01 R4）在同一 worker 进程重连、且 `worker_instance_id` 与行一致时**认领**它继续跑；认领不到的在窗口结束后以 `worker lost` 失败。
- 因此 §四 的两个非阻塞条件（worker 已接手、recovering）是有依据的，不是「忽略」；而「还没交给 worker」的 job 恰恰**没有**这个保护，所以必须阻塞。
- 本机空闲的 ACP 持续会话照旧不阻塞（`UpgradeIdleSessionIDs` 排除）：本机会话在重启后用 `session/load` 恢复，worker 会话的进程留在 worker。

## 六、等待的边界与出口

- 请求体 `wait_idle_sec`（0 = 7200；钳制到 `[60, 86400]`），web 弹窗给 30 分钟 / 1 小时 / 2 小时 / 4 小时，默认 2 小时。
- `DELETE /v1/update/apply`（管理员，job 凭据 403）取消等待：停住的 job 立即放行，apply 记 `cancelled`（体例与 apply 一致；没有在等待的更新时 409）。
- 每次进入 waiting_idle，`apply.json` 记下 `wait_started_at` / `wait_deadline` / `wait_timeout_sec` 与 applyID：旧 server 意外重启后，新 server 能认出「这次等待的进程已经不在了」。
- **放行的出口**（都调 `job.Service.ResumeStartsForUpdate`，幂等、无停住 job 时是空操作）：
  1. 新 server 启动（`ResumeUpdateApply`，**先**放行，再处理 worker；版本不对也先放行）；
  2. 超时（`failed`，原因写明等了多久、还在挡着重启的是哪些 job）；
  3. 取消（`cancelled`）；
  4. 下载失败 / `serve upgrade` 失败（`failed`）。
  5. 启动时发现 apply 停在 waiting_idle（进程死了，等待不可能续上）→ 记 `failed`（原因：等待期间 server 重启了，请重新发起更新）。
- 放行的自洽性靠 `job.Service` 的 `pauseMu`：**读取开关与插入停住行在同一把锁内**，放行则是「在锁内清开关并列出停住行，再逐条放行」，因此放行之后不可能再冒出一行新的停住 job（不会留下永远没人放行的 job）。

## 七、进度与失败文案

- `GET /v1/update` 的 `apply` 在 waiting_idle 阶段额外带：`waiting`（还在挡着重启的 job，最多 20 条：id / 标题 / 状态 / runner / 开始时间）、`paused`（已停住的 job 数）、`wait_deadline` / `wait_timeout_sec`。列表与「是否空闲」用的是**同一个** jobstore 查询，所以显示与决策不会不一致。
- `serve upgrade` 失败时，apply 的 error 改为：从 `run/update/serve-upgrade.log` 尾部取出最后一条有信息量的行（跳过 `upgrade_id=… phase=draining` 这类进度行，去掉 gcli 加的 `ERROR` 前缀），再加上「此刻还有哪些 job 在挡着重启」与日志路径；例如 `upgrade the server: upgrade <id> was not accepted: context deadline exceeded …; 2 job(s) still running: 20261014-… (running), …; log /…/run/update/serve-upgrade.log`。
- `serve upgrade` 的排空期限改为 `--drain-timeout <dur>`（默认 5m，写死 5 分钟取消）；一键更新传较小的 2 分钟（`httpapi.ApplyDrainTimeout`）：它刚刚等到空闲，长排空只会拖慢一个不会再变安全的重启。

## 实施记录

- `internal/job`：`StatusPausedForUpdate`（model.go）；`update_pause.go`（`PauseStartsForUpdate` / `UpdatePauseOwner` / `parkForUpdate` / `ResumeStartsForUpdate` / `releasePausedJob` / `failReleasedPause` / `cancelPausedJob` / `CountUpdatePaused` / `ListUpdatePaused`，事件 `job.paused_for_update` / `job.update_resumed`）；`hold.go` 抽出 `parkJob(parkSpec)` 与 `heldInternals` 扩容；`submit.go` 在停住点停住、并在排空期间把提交接到停住路径（`ErrUpgradeDraining` + 开关打开 → 停住而不是 503）；`upgrade_drain.go`（Service 侧的重启阻塞计数/列表）。
- `internal/jobstore`：`statusPausedForUpdate` / `ListPausedForUpdate` / `CountPausedForUpdate`；`upgradeInFlightStatuses` → `upgradeBlockerStatuses` + `upgradeBlockerWhere` + `CountUpgradeRestartBlockers` / `ListUpgradeRestartBlockers`；plan 汇总把 `paused_for_update` 记进 queued 桶。
- `internal/serve/upgrade_drain.go`：排空改用 `job.Service` 的同一套计数。
- `internal/httpapi/update_apply.go`：`waiting_idle` / `cancelled` 阶段、`wait_idle_sec`、`waitForIdle`、取消接口、各出口放行、`ResumeUpdateApply` 的启动放行与「等待被打断」处理、`applyView` 的 waiting/paused、`applyFailureMessage` + `lastLogLine`；`server.go` / `jobcredential.go` 路由与权限文案。
- `internal/commands/serve_management{,_upgrade}.go`：`--drain-timeout`；`internal/serve/update_apply.go` 传 `httpapi.ApplyDrainTimeout`。
- web：`UpdateApplyDialog.vue`（新说明 + 最长等待选择）、`About.vue`（waiting_idle 进度：在等的 job、停住数、放弃时间、「取消等待」）、`StatusBadge.vue` / Board 的 `paused_for_update` 文案与筛选、`api/types.ts` / `api/client.ts`（`wait_idle_sec`、`cancelUpdateWait`）。
- 文档：`docs/reference/http-api.md`（新阶段、`wait_idle_sec`、取消接口、`paused_for_update`）、`skills/gofer-usage/references/server-config.md`（一键更新段）、`skills/gofer-usage/references/operations.md`（`serve upgrade --drain-timeout`）、CHANGELOG。
- 未做（有意）：没有让「等空闲」在重启后继续（等待属于那个进程；新 server 放行并让这次 apply 失败，重新发起即可）；没有给 `paused_for_update` 加「预计何时开始」的提示（时间取决于运行中的 job）。
