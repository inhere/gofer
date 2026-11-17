<!-- template_id: design; template_version: 1.2.0 -->
# 第 6 批：托管常驻会话 总设计

> 状态：Draft 0.1（待批）
> issue：gofer-anrv（托管常驻会话）、gofer-xmit（空闲常驻会话拖住 worker 升级）、gofer-geyu（会话已死时 job 无法结束）、gofer-fj5x（worker 上等目录锁只显示 queued）
> 关联设计：第 5 批 `2026-11-16-web-session-experience-design.md`（本批承接其「遗留」）、`2026-11-10-acp-session-history-design.md`（ACP 历史）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-17 | Claude（gofer 监督会话） | 初稿：现状逐条核对代码，五块方案 |

## 背景与目标

第 5 批让 web 里起的容器 `claude-acp` 会话在交互和显示上能用了，但会话本身还不能长期挂着用：空闲 30 分钟自动结束；worker 一重启（包括升级）会话就失败；serve 被杀时停在提问上的会话既结束不掉也答不了；worker 升级会被空闲会话拖 10 分钟后放弃。目标是让一个 web 会话可以像开着的终端一样一直在，机器或服务重启后自己接回来，别处的 job 做完能把结果送进这个会话。

## 现状（已核对代码）

| # | 现状 | 位置 |
|---|---|---|
| F1 | `--idle-timeout` 为 0 时提交改写成 1800，负值拒绝；没有「不按空闲结束」。到期后 job 记为 done，`session_end_reason=idle_timeout` | `job/submit.go:290-301`，`runner/acp/runner.go:422-452` |
| F2 | 本机会话在 serve 重启后已能自动接回：`running` / `awaiting_input` 且有会话状态的行改为 `recovering`，用 `session/load` 重新执行，job id 不变 | `jobstore/jobs.go:723-759`，`job/session_recovery.go:40-159` |
| F3 | 本机 job 停在 `pending_interaction` 时，既不在接回列表，也不在孤儿判失败列表，于是一直停着：`end` 409、`cancel` 空操作、`answer` 409「job is terminal」 | `jobstore/jobs.go:717,765`，`job/cancel.go:68-101` |
| F4 | worker 上的会话：serve 重启、同一 worker 进程重连时已能领回；**worker 进程重启（含升级）后**，新进程不认识旧 job，旧连接收尾时 job 判失败，错误提示用户手动 `job resume` | `wshub/hub.go:614-630`，`runner/worker/runner.go:1172` |
| F5 | worker 升级排空把 `awaiting_input` 的会话算在途，等满 600s 后放弃升级；server 侧升级已把空闲会话排除在外 | `worker/upgrade_run.go:252-268`，`job/upgrade_admission.go:123-176` |
| F6 | `--notify` 只能指向终端 / hook 注册的 agent 会话，不能指向 ACP 会话 job。往 ACP 会话送话的通路已有：子 job 回执（`SaySession`，会话忙时停放到回到等待输入） | `job/notify.go:75-86`，`job/wakeup_session.go` |
| F7 | worker 上的 job 等目录锁时，hub 只收到事件，行状态停在 `queued` 直到开始执行；会话的状态帧里 `waiting_dir` 被忽略 | `job/execute.go:238-263`，`job/session_recovery.go:199-206` |
| F8 | worker 会话结束原因（如 `idle_timeout`）不回传 hub：`runner.Outcome` 没有该字段 | `runner/runner.go:734` |

## 方案

### 1. 托管会话：不按空闲结束（anrv）

- 新增 `--hosted`（HTTP / MCP 字段 `hosted`）：只对持续会话（`--session`）有效。等价于空闲超时与最长时长都不设，会话只在用户结束、agent 自己退出或出错时结束。会话状态里记 `hosted=true`，web 与 `job show` 显示「托管」。
- 不改 `--idle-timeout 0` 的现有含义（仍是默认 1800），避免改变已有调用方的行为。
- web「新建会话」默认勾选「托管」（见 Q2）。
- 结束原因：托管会话结束时照常记 `session_end_reason`；worker 会话把该字段随结果回传（补 F8）。

### 2. 重启后自动接回（anrv、geyu）

- **本机，pending_interaction**（F3）：serve 启动时，带会话状态的 `pending_interaction` 行与 `running` / `awaiting_input` 一起领为 `recovering` 并 `session/load` 接回。原来那条提问随旧进程失效：交互记为过期（原因「会话已重启」），会话接回后停在等待输入，时间线上显示一行「重启前的提问已失效，请重新发起」。不是会话的 `pending_interaction` 行按孤儿判失败（`interrupted`），这样 end / cancel / answer 不再卡住。
- **worker 进程重启 / 升级**（F4）：会话 job 不再因旧连接收尾直接判失败，而是转 `recovering`，等同一 `worker_id` 的新进程连上后，hub 用原 job id 重新派发，派发帧带 `session_id`（已有字段，协议 v6 起），worker 端以 `session/load` 起会话后回到等待输入。
  - 条件：agent 支持 `loadSession`；不支持的仍按现状判失败并提示手动续接。
  - 时限：新进程在恢复窗口（沿用 serve 重启的领回窗口）内没连上，判失败「worker lost」。
  - 正在跑一轮的会话（`running`）：这一轮的输出到断开为止，接回后停在等待输入，时间线显示「上一轮因 worker 重启中断」。
  - 是否要协议版本门槛：worker 收到一个它不认识、但带 `session_id` 的派发，本来就走 resume 路径；若实现发现需要新字段则升 v32，旧 worker 不做接回（照现状失败）。
- 接回对所有持续会话生效，不只托管会话：接回只恢复用户已经开着的东西，没有额外代价。

### 3. worker 升级不再被空闲会话拖住（xmit）

- worker 排空时，`awaiting_input` 且可接回（agent 支持 `loadSession`）的会话不计入在途，与 server 侧一致。升级完成后由第 2 节把它们接回。
- 正在跑一轮的会话照旧等；不可接回的会话照旧等（到时限放弃升级），并在升级结果里写明「被哪些会话拖住」。

### 4. job 完成通知送进 ACP 会话（anrv）

- `--notify-session` 接受 ACP 会话的 job id（`job-…`）：提交时校验该 job 是持续会话且未结束，登记一条与子 job 回执同类的唤醒。被通知的 job 结束时，结果摘要经 `SaySession` 作为一轮送进会话；会话忙时停放，回到等待输入后送达（复用 `wakeup_session` 的停放与送达）。
- 会话被接回（第 2 节）后 job id 不变，停放的通知照常送达。
- web：job 详情页「完成后通知」可选一个进行中的 ACP 会话（见 Q4）。
- plan 的监督会话绑定到 ACP 会话 job 不在本批（只做单个 job 的通知）。

### 5. worker 上等目录锁的显示（fj5x）

- hub 收到 worker 的 `job.waiting_dir` 事件时，`queued` 行改为 `waiting_dir`，开始执行时照常转 `running`；会话轮次之间的 `job.dir_wait` 在会话状态帧里体现为「等目录锁」，web 显示占用者（第 5 批已有占用者文案）。

## 不做

- 托管会话数量上限、长期空闲提醒（先观察实际用量）。
- plan 监督会话绑定 ACP 会话 job；IM 通道。
- 不支持 `loadSession` 的 agent 的接回（只能重放转录，另议）。

## 待决问题

- **Q1 托管的表达**：推荐 `--hosted` 一个开关（不按空闲结束 + 不限最长时长）。备选：`--idle-timeout never`，最长时长另设。
- **Q2 web 新建会话默认**：推荐默认「托管」（web 会话本来就是要一直开着用的）；CLI 默认不变。
- **Q3 worker 重启后的接回方式**：推荐原 job id 重新派发（线程、通知、计数都不用改）。备选：自动 `resume` 出一个新 job（实现更简单，但 web 线程会多一段、停放的通知要改投）。
- **Q4 通知入口**：推荐本批做 CLI / HTTP / MCP 的 `--notify-session <job-id>` 加 web job 详情页的选择框。备选：只做 CLI / HTTP / MCP，web 下批。
- **Q5 实施分工**：第 2、3 节（重启接回、排空，涉及并发与协议）交 sonnet 子 agent；第 1、4、5 节与文档派 suga。

## 验收

- 本机：起托管会话，等过 30 分钟不结束；kill serve 再启动，停在提问上的会话接回到等待输入，提问显示已失效，可以继续对话。
- worker：在容器 worker 上起托管会话，`gofer worker upgrade` 不再等 600s，升级后会话自动接回，web 线程不变、可继续对话；worker 进程手动重启同样接回。
- 通知：另起一个 job 带 `--notify-session <会话 job id>`，结束后会话里出现结果一轮；会话正在跑时，结束后再送达。
- 显示：worker 上等目录锁的 job 在列表显示「等目录锁」。
- 反向验证：去掉 pending_interaction 领回、去掉排空排除、去掉重新派发，对应用例失败。
