<!-- template_id: plan; template_version: 1.1.0 -->
# 第 6 批：托管常驻会话 实施计划

> 状态：Draft 0.1（待批）
> 设计：`docs/design/2026-11-17-hosted-resident-session-design.md`（已批准，Q1–Q5 按推荐）
> issue：gofer-anrv、gofer-xmit、gofer-geyu、gofer-fj5x

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-17 | Claude（gofer 监督会话） | 初稿 |

思考模式：RIGOROUS。Scope freeze：只实施设计 §1–§5。Review budget：计划不另起评审轮，用户批准即开工；实施审查每条链一轮，有阻塞问题才返工一次。

## 目标与完成定义

- 托管会话不按空闲结束；serve 重启、worker 进程重启（含升级）后会话自动接回到等待输入，job id 不变；停在提问上的会话重启后不再卡死；worker 升级不再被空闲会话拖住；`--notify-session <job-id>` 把结果送进 ACP 会话；worker 上等目录锁显示 `waiting_dir`。
- 单测与反向验证通过；web `vue-tsc` / `vitest` / `vite build` 通过；gofer-verify 四路通过；真实验收（容器 worker）通过；合入 main、关 issue。

## 已完成的底座

- `61ffe8d5`：`JobRequest.Hosted`（`job/model.go`）；Submit 对 Hosted 不补 1800 的空闲默认，不带 Session 或与显式空闲 / 最长时长同用时拒绝；用例 `TestSubmitHostedSession`。会话无 `MaxSessionSec` 时本来就没有整段时限（`job/outcomes.go:653-670`，`execute.go:251-259`）。两条链都从这个提交开分支。

## 分工与分支

- **A 链（sonnet 子 agent，分支 `feat/b6-core`，worktree `.worktrees/b6a`）**：并发与协议——T1–T4。
- **B 链（suga，分支 `feat/b6-ux`，worktree `.worktrees/b6b`）**：入口、通知、web、文档——T5–T8。
- 两链改动面分开；共享文件只有 `CHANGELOG.md`（各自追加）与 `docs/reference/http-api.md`（各改各的节）。合并顺序 A 先 B 后，冲突由我解决。

## 任务

### A 链

- **T1 本机 `pending_interaction` 接回（geyu）**：`jobstore.ClaimLocalSessionsForRecovery`（`jobstore/jobs.go:723-759`）把带会话状态的 `pending_interaction` 行一并领为 `recovering`；接回时把该 job 仍 open 的交互记为过期（原因「会话已重启」，沿用现有过期 / 取消路径与事件，不另造状态），记一条会话事件，web 时间线显示「重启前的提问已失效，请重新发起」（文案走 i18n；时间线渲染在 B 链之外，T1 只加事件并在 `ConversationTimeline` 的事件映射里加一行）。不带会话状态的本机 `pending_interaction` 行加入孤儿判失败（`orphaned:` 前缀，即 `interrupted`）。用例：模拟 serve 重启（参照现有 `session_recovery` 用例），停在提问上的会话接回到 `awaiting_input` 且交互过期；非会话行判失败；之后 `end` / `cancel` 正常。
- **T2 worker 进程重启后接回（anrv）**：旧连接收尾判失败的路径（`wshub/hub.go:614-630, 947-977`，`runner/worker/runner.go:1172`，`job/adopt.go:381`）对「持续会话 + 有 `session_id` + agent 允许 `loadSession`（`ac.ACP.AllowsLoadSession()`，与 `resume.go:577` 同口径）」的 job 改为转 `recovering`；同一 `worker_id` 的新进程注册后，hub 用**原 job id** 重新派发，派发帧带 `session_id`（已有，v6），worker 端走 `session/load`，结束后回到等待输入。新进程在恢复窗口内（沿用 `serve/recovery.go` 的窗口值）没连上则判失败「worker lost」。断开时正在跑一轮的，接回后记事件「上一轮因 worker 重启中断」。
  - **先核实**：worker 端收到「本地没有、带 `session_id`、job id 已存在于 hub」的派发能否直接建本地 job（凭据、`CredentialJobID`、日志偏移从 0 还是续写）；hub 侧同一 job id 的 entry 在 `recovering` 下能否再次 `execute`（参照本机 `resumeLocalSession` 的做法）。核实结论写进最终回复；若必须加 wire 字段，升协议 v32，旧 worker 照现状失败。
  - `hosted` 随派发帧下发（`wsproto` dispatch 帧加 `hosted`，`worker/dispatch.go` 回填到 `JobRequest.Hosted`），否则 worker 端 Submit 会补 1800 的空闲默认。托管会话派给协议低于本批版本的 worker 时拒绝提交（照 v31 `permission_tier` 的做法），所以这一项**需要升 v32**。
  - worker 会话结束原因回传（设计 F8）：`runner.Outcome` / 结果帧带 `session_end_reason`，hub 行写入。
- **T3 升级排空不算空闲会话（xmit）**：`worker/upgrade_run.go:252-268` 的 `drain` 计数排除「`awaiting_input` 且可接回」的会话（worker 端判可接回：ACP 初始化时 agent 声明了 `loadSession`）。**先核实**旧进程退出时不会为这些会话上报终态结果（否则 hub 会先判失败）；需要时在退出路径上对这些会话只停进程、不发结果，交给 T2 接回。放弃升级时结果里列出拖住的 job id。用例：一个空闲可接回会话不阻塞 drain；一个正在跑的会话仍阻塞。
- **T4 worker 上等目录锁的状态（fj5x）**：hub 收到 worker 的 `job.waiting_dir` 事件（`execute.go:449-454` → `recordWorkerEvent`）时，`queued` 行改为 `waiting_dir`；`markStarted` 照常转 `running`；`applyRemoteSessionState`（`session_recovery.go:199-206`）认 `waiting_dir`。用例：hub 侧行状态随事件变化。

### B 链

- **T5 `--hosted` 入口**：CLI `gofer job run --hosted`（只在 `--session` 下有效，与 `--idle-timeout` / `--max-session` 同用报错，错误来自 Submit，CLI 不另判）；HTTP / MCP 字段 `hosted`；`job show` 与会话状态显示「托管」；`resume`（`resume.go:597`）续接时带上源 job 的 `Hosted`。web：`NewSessionSheet.vue` 加「托管（不按空闲结束）」开关，默认勾选；job 详情与工作台线程显示「托管」标记。用例：CLI 参数透传、HTTP 字段、resume 继承、vitest 默认勾选。
- **T6 `--notify-session <job-id>`**：`job/notify.go:75-86` 的目标解析接受 `job-` 开头的 id：校验是本 server 的持续会话 job 且未结束（否则 400，说明原因）；登记一条与子 job 回执同类的唤醒（`wakeup_child_register.go`、`wakeup_session.go` 的停放与送达），被通知 job 结束时把结果摘要作为一轮送进会话（摘要格式与现有回执一致）。HTTP / MCP 同名字段照现有 `notify_session` 处理。web job 详情「完成后通知」下拉列出进行中的 ACP 会话（标题 + job id）。用例：会话等待输入时立即送达；会话忙时停放、回到等待输入后送达；目标不是会话 job 时 400。
- **T7 文档**：gofer-usage references（持续会话一节：托管、自动接回、升级不再被拖住、`--notify-session` 接受会话 job id）、`docs/reference/http-api.md`（`hosted`、`notify_session`、协议 v32、`waiting_dir`）、CHANGELOG「未发布」、设计稿实施记录。G046：不写本仓库细节、issue id、本机路径。
- **T8 真实验收**（我做）：见下。

## 前置与 fail-closed 条件

- T2 核实发现 hub 不能对同一 job id 再次执行、需要重做 job 生命周期时：停下，写明原因，回到 Q3 的备选（自动 resume 出新 job）由用户决定。
- T3 核实发现旧进程退出必然上报终态、且改动超出 worker 退出路径时：停下说明。
- 任一链需要改 `jobstore` schema：照 G057 跑 `TestOpenUpgradesPreviousReleaseSchema`。

## 真实验收（T8）

1. 本机：起托管会话（容器外 server 本机 runner），记录 idle 状态 30 分钟以上不结束（可用短窗口的 `--idle-timeout` 对照组证明非托管会结束）；停在 `gofer_ask` 提问上时重启临时 server，会话接回到等待输入，提问显示失效，再发一轮成功。
2. worker：容器临时 worker 上起托管会话，`gofer worker upgrade`（同版本强制重装即可）不等 600s，升级后会话自动接回，web 线程不变，再发一轮成功；手动重启 worker 进程同样接回。
3. 通知：另起 exec job `--notify-session <会话 job id>`，会话里出现结果一轮；会话正在跑时，结束后送达。
4. 显示：worker 上 `--exclusive-dir` 的 job 等锁时，列表显示「等目录锁」。
5. 无头浏览器截图：新建会话默认勾「托管」、线程上的「托管」标记、失效提问那一行。

## 反向验证

- 去掉 T1 的 `pending_interaction` 领回 → T1 用例失败。
- 去掉 T2 的转 `recovering`（恢复判失败）→ T2 用例失败。
- 去掉 T3 的排空排除 → T3 用例失败。
- 去掉 T6 的停放 → 会话忙时送达用例失败。
- 每条确认替换生效且能编译（筛 `[build failed]`）。

## 风险与回退

- 接回逻辑出错会让会话停在 `recovering`：恢复窗口到期必须判失败，用例覆盖。
- 协议 v32：旧 worker 上托管会话被拒、接回不做，其余行为不变。
- 回退：两链各自 `--no-ff` 合并，可整链 revert。
