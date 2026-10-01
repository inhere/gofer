<!-- template_id: design; template_version: 1.1.1 -->
# 持续会话的提醒推送（Y2）与远程 worker 持续会话（Y3）

> 状态：Approved（Draft 0.1；用户 2026-10-01 在 web 中继直接确认待确认事项 1–4 全部同意）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-01 | Claude | Y2 提醒推送缺口与方案；Y3 远程持续会话协议方案 |

## 背景与目标

v0.85.0 上线了本机 runner 的交互式 ACP 持续会话 job（`job run --session` / `job say` / `job end`，状态 `awaiting_input`）。两个后续：

1. **Y2（tools-yig）**：人不在电脑前时，会话在等你回复、或 agent 请求审批，手机上要能及时收到提醒。
2. **Y3（tools-pne）**：持续会话目前只支持 server 本机 runner，远程 worker 提交带 `session` 的 job 在提交阶段被拒绝；要把它扩到远程 worker。

## 已确认事实（2026-10-01 核对代码）

- **tools-yig 原计划的主体已经实现**（`a5daedd9` feat(notify): DingTalk/Feishu webhook adapters and session-waiting push (OBS-07a)）：webhook `kind` 厂商适配（钉钉 / 飞书消息体渲染、各自签名）、预渲染投递（`event_deliveries` 的可空 body / event_type）、`server.web_base_url` 生成可点链接、pty 会话中继的 `session.waiting` 推送。issue 未关闭属于遗漏。
- 默认触发事件 `notify.DefaultTriggerEvents`：`job.terminal`、`interaction.created`、`job.needs_review`、`plan.blocked`、`job.retry_exhausted`、`plan.leader_exhausted`。
- Web Push（`internal/webpush/dispatch.go`）推送：审批请求（带可直接作答的按钮）、需验收、终态、plan 阻塞。
- ACP 持续会话每轮结束记录 `job.awaiting_input`（`{turn_no, idle_deadline_at}`），终态带 `session_end_reason`（`manual_end` / `idle_timeout` / …）。

## Y2 缺口

| # | 缺口 | 现状 |
|---|---|---|
| G1 | 会话等你回复没有提醒 | `job.awaiting_input` 不在默认触发集，Web Push 不推；IM 若显式订阅，只显示 `job job.awaiting_input` + 一行 id/状态 |
| G2 | 连续对话时的噪音 | 每一轮结束都会进入 awaiting_input；你正在 web 上来回聊时，每轮都推送是骚扰 |
| G3 | 审批请求在 IM 里看不出要批什么 | `interaction.created` 在钉钉 / 飞书里只是通用 job 行（Web Push 已有专用内容和按钮） |
| G4 | 会话结束的提醒不区分原因 | 持续会话的 `job.terminal` 与普通 job 相同；自己点"结束"的也会推送 |

## Y2 方案

1. **延迟的"等你回复"提醒（新事件 `session.awaiting_reply`）**
   - job 进入 `awaiting_input` 后开一个延时器，时长 `server.notification.session_reply_delay_sec`（默认 **120 秒**）。到点仍在等待才发出；期间收到 `say` / `end` / 取消即撤销。一次等待最多发一次。
   - 这样正在 web 上连续对话时不会每轮都推；走开后两分钟收到一条。
   - 加入默认触发集；Web Push 同步推送，点开直达工作台里这个会话。
   - 消息内容：标题「会话等你回复：<job 标题>」；正文：第 N 轮 · agent · 项目；**本轮 agent 回复的前 200 字**；"空闲将在 HH:MM 自动结束"。链接到工作台该会话（无工作台时退到 job 详情）。
   - server 重启恢复回 `awaiting_input` 的会话按重新进入等待处理（重新计时），不补发重启前已发过的提醒。
2. **审批请求的 IM 专用消息**：`interaction.created` 渲染为「需要审批：<agent 请求的操作摘要>」+ 选项列表 + 链接（IM 里不能直接点按钮作答，双向交互属于 OBS-07c，本期不做；Web Push 仍可直接作答）。
3. **会话结束提醒按原因区分**：持续会话的终态消息带结束原因；**`manual_end`（自己结束的）不推送**，`idle_timeout` / `max_session` / 失败照常推送并写明原因。
4. 不新增实体或配置段：沿用现有 webhook 的 `events` / `projects` 过滤和项目级 `notify_enabled`；只新增一个延时配置项（可在设置页编辑）。

测试（固定名，实施时先红后绿）：`TestSessionAwaitingReplyNotifiesAfterDelay`、`TestSessionAwaitingReplyCancelledBySay`、`TestSessionAwaitingReplyOncePerWait`、`TestInteractionCreatedIMMessage`、`TestSessionManualEndNotNotified`、Web Push 侧 `TestWebPushSessionAwaitingReply`。真实冒烟：临时 serve + 本机可达的 webhook 接收端（钉钉 / 飞书格式各一）验证消息体与延时撤销。

## Y3 远程 worker 持续会话

### 现状（2026-10-01 代码调研）

- worker 协议当前 v12（`wsproto/frames.go`）。`dispatch` 是一次性的，没有会话字段；server→worker 只有 `cancel`、`answer`（审批答复，worker 离线时直接丢弃、不重投）可以推到运行中的 job。
- 远程 job 的目录锁和 agent 名额由 **worker 本地** 的 job 服务持有；server 侧只有项目 / 调用方 / agent 信号量和 worker 并发槽。
- server 只消费 worker `status` 帧里的 `started`；worker 本地进入 `awaiting_input` 时 server 看不到，行一直是 `running`。
- server 为远程 job 在 `started` 后按 `timeout_sec` 计时；会话下这会变成"整个会话的总时长"。
- 断线重连（同一 worker 进程）：worker 上的 job 继续跑，重连后按日志偏移续传（RECOV-01），会话进程不受影响。worker 进程重启：新实例注册时 server 立即判这些 job `worker lost`。
- server 重启：只把 `queued / running / recovering` 的 worker job 置为待接管；`awaiting_input` 的行会被漏掉、永久悬挂。
- job token 只按提交时的超时签发；多轮会话里 worker 上的 agent 回调 server 会因 token 过期失败。

### 方案

1. **协议（v13，可选功能门槛 `SessionJobMinProtocolVersion = 13`）**
   - `dispatch` 增加可选字段 `session`、`idle_timeout_sec`、`max_session_sec`；worker 用本地已有的持续会话实现运行（单轮 / 空闲 / 总时限都在 worker 上执行）。
   - 新帧 `session_cmd`（server→worker）：`{job_id, cmd_id, action: say|end, prompt}`；worker 映射到本地 `SaySession / EndSession`，按 `cmd_id` 去重。
   - 复用 `status` 帧上报会话状态（与 v0.80 的 `started` 同一做法）：`turn_started{turn_no}`、`awaiting_input{turn_no, idle_deadline_at}`、`session_ending`。server 据此更新状态并写同样的 `job.turn_started / turn_ended / awaiting_input` 事件，页面与本机会话完全一致。
   - 旧 worker（< v13）：提交阶段直接拒绝并说明"该 worker 版本不支持持续会话，请升级到 vX"（G032：不静默降级成单轮）。
2. **server 侧**
   - 会话 job 跳过 server 端的整 job 超时计时（与本机一致）；只保留安全上限：设置了 `max_session_sec` 时按它加余量兜底。
   - 每轮 `turn_started` 时延长 job token（与本机 `ExtendJobTokenExpiry` 一致）。
   - `say` 只在 server 行为 `awaiting_input` 且 worker 在线时接受；worker 离线时返回 409「worker 离线，稍后重试」。`end` 离线时与 `cancel` 一样暂存，重连后补投递。
   - worker 并发槽与 server 侧信号量在整个会话期间占用（与本机"整会话占用"语义一致）。
3. **恢复**
   - **断线重连（同一 worker 进程）**：会话进程还活着，按现有 RECOV-01 续传即可；重连后补投递暂存的 `end` / `cancel`。
   - **server 重启**：把 `awaiting_input`（以及 `pending_interaction`）加入 worker job 的待接管状态，恢复窗口内 worker 重连并报告该 job 即接管，会话照常继续。
   - **worker 进程重启**：本期判失败，错误写明"worker 重启，会话已结束"，并提示用 `gofer job resume`（会以 `session/load` 开新 job 接上上下文）。worker 启动时自行以 `session/load` 拉起旧会话涉及注册时序与本地恢复，留作后续。
4. **兼容与清理**：v0.85 的"远程拒绝"改为按 worker 协议版本判断；G032 无需兼容标记（新增可选字段与帧）。

### 测试与冒烟

固定测试：`TestRemoteSessionDispatchRejectedForOldWorker`、`TestRemoteSessionSayEndRoundTrip`、`TestRemoteSessionStatusMirrorsTurns`、`TestRemoteSessionSkipsHostWholeJobTimeout`、`TestRemoteSessionTokenExtendedPerTurn`、`TestRemoteSessionSayRejectedWhileWorkerOffline`、`TestRemoteSessionEndParkedAndRedelivered`、`TestRemoteSessionAdoptedAfterServerRestart`、`TestRemoteSessionFailsOnWorkerRestart`。
真实冒烟：临时 serve + 临时 worker（各自独立配置目录），真实 ACP agent 三轮对话；断开并恢复 worker 网络连接后继续对话；重启临时 serve 后会话被接管并继续；重启 worker 后 job 失败且提示可 resume。

## 待确认事项

1. Y2 延时默认 120 秒是否合适（可配）。
2. 自己点"结束"的会话不推送终态，是否同意。
3. Y3 worker 离线时 `say` 直接拒绝（而不是暂存后补发），是否同意。
4. Y3 worker 进程重启时本期判失败（提示 `job resume` 接续），worker 自行恢复留作后续，是否同意。

## 结论与人工计划 Gate

本稿审批后：Y2 由 codex 直接实施（规模小、无协议变更）；Y3 先写实施计划候选（协议升级 v13，按波次实施），计划批准后实施。
