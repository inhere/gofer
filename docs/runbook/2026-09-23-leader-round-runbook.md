# leader 回合（MCP-05 阶段 B）使用 Runbook

> 配套 design [`../design/2026-09-23-skills-binding-and-comment-routing-design.md`](../design/2026-09-23-skills-binding-and-comment-routing-design.md) §二.B。**默认关闭**：没配 `supervisor.leader`（或 `enabled: false`）时，成员 job 终态不写任何唤醒记录、不起任何 job、不记任何 `plan.leader_*` 事件——与本期之前逐字节一致。

## 是什么

挂在某个 plan 上的**成员 job**（`plan_id` 非空的普通 job）到达 **Finished** 状态时，gofer 唤醒一个 **leader job**，让它看完汇报决定下一步：

```
成员 job 终态(done|failed|needs_review)
      │
      ▼  写一条待唤醒记录（plan_leader_wakes, state=pending, due_at=now+wake_delay_sec）
      │      ← 人在这段时间里评论 = 取消当轮
      ▼  sweeper（每 10s 一跳，重启不丢：行的状态在库里）
leader job（agent=supervisor.leader.agent，plan 同源，tags: leader / leader_of:<成员job> / leader_round:<N>）
      │
      ▼  它只能用这几个 MCP 工具：comment / list_comments / get_plan / update_todo(ready|skipped) / wakeup_create / ask_human
下一步：@成员派活 / 把 todo 置 ready（带 assignee 即派发）/ 建 wakeup / 升级给人
```

**leader 永远不能 accept/reject**（GATE-01 §3 的人工验收边界不动）：它的 MCP 里根本没有 `gofer_accept_job`（从来就没有）与 `gofer_reject_job`（leader 面不注册），HTTP 的 `POST /v1/jobs/{id}/accept|reject` 也会拒绝 leader job 身份（`as_job` = 该 leader job）→ 403。

## 开起来

```yaml
supervisor:
  leader:
    enabled: true
    agent: omp-leader        # 已配置的 agent key，或一个 role 名（同名时 agent 优先）
    scopes: [plan]           # 目前只实现 plan（写别的值 config validate 直接报错）
    max_rounds_per_scope: 6  # 不写=6；到上限即停止唤醒并升级给人
    on_member_done: true     # 不写=true（enabled 打开即按成员终态唤醒）；显式 false 关掉触发
    wake_delay_sec: 30       # 不写=30；这是留给人插话的窗口
```

- 校验：`enabled: true` 但 `agent` 空、或 agent/role 不存在 → `config validate` / serve 启动直接报错（否则每轮唤醒都会在成员 job 已经结束后才 submit 失败）。
- 生效方式：字段策略表里 `supervisor.leader` 是**可热改**（每轮唤醒时重读一次 config），改完文件 `SIGHUP` 即对**下一轮**生效（控制台暂无这个表单，写文件即可）。
- leader 的 agent 需要能调 gofer MCP（和别的 agent 一样）；leader job **只在本机内置 local runner 上跑**（它的身份标记不进 wire，不能派到 worker/peer）。

## leader 拿到什么

prompt（`leader 回合 N：<plan 标题>`）：

```
你是 plan plan-xxx 的 leader（第 3 轮）。一个成员 job 刚刚结束，请你读完它的汇报，决定下一步：…

## Plan              标题 / 状态（含"已暂停"/"阻塞在 todo-x"）/ 目标
## 待办链现状         [status] todo-id 标题（指派：agent，依赖：…）— 备注最后一行
## 刚结束的成员 job    id / 标题 / 状态（error）/ 最近一次汇报（stdout 末尾 40 行）
## 你可以做的         六个工具各自能干什么
## 你不能做的         不能 accept/reject、不能把 todo 标 done、不能改配置/push
## 规则               第 N/M 轮、人插话即接管、不要自己实现活
```

tags：`leader`、`leader_of:<成员 job id>`、`leader_round:<N>`；`channel: leader`；`caller_id: gofer`。它就是个**普通 job**：有 usage、有超时、失败走 AUTO-03 重试链。唯一被强制的是**不进验收队列**（`review: false` 且不可被项目 `require_review` 覆盖）——leader 轮的产出是"决定"，不是待人验收的交付物。

## 闸门与边界

| 规则 | 说明 |
|---|---|
| opt-in | 没配 `supervisor.leader` / `enabled: false` → 什么都不发生 |
| 只认成员终态 | `plan_id` 非空、**非** leader job（行上 `leader_of_plan` 为空）、tags 里**没有** `leader`、状态 ∈ {done, failed, needs_review}；`cancelled`/`timeout` 等人为终止不唤醒 |
| 一轮一条 | 同一个成员 job 只会留一条未取消的唤醒记录（重复走进终态不重复记） |
| plan 暂停 | `plan pause` 之后：唤醒时直接跳过（记 `plan.leader_skipped{reason:"paused"}`）；若在窗口内被暂停，到点那一刻取消该轮并记同一个事件 |
| 轮次封顶 | `max_rounds_per_scope` 数的是**真的起过 leader job 的轮数**；用满即不再唤醒，改记 `plan.leader_exhausted` + 在 plan 上开一条 **decision**（web plan 页 / 待办式"待人处理"项），人接手 |
| 防自激 | leader job 自己的终态不唤醒下一个 leader |
| 人优先 | 人在该 plan 的评论区说话（`scope=plan`，或该 plan 的 todo/job）→ 取消该 plan **所有未开始的**待唤醒轮，记 `plan.leader_cancelled{by}`；已经在跑的 leader job 不受影响（它就是"当轮"，下一轮才归人） |
| 派活闸门 | leader 的 `@成员` 评论走阶段 A 同一条派活路：**限流**（`server.comment_trigger`，1 条/分钟/scope、累计 10 条）+ **项目 allowlist** 照样生效；普通成员 job 的评论仍然只记录 |

## 人在哪接管

- **评论区一句话**就是接管：`gofer plan comment <plan> "这轮我来"` / web plan 页评论区 / job 线程都算（只要作者是 user）。想连 leader 带链一起停：`gofer plan pause <plan>`。
- 想让人来接班一个已经用满轮次的 plan：plan 页上会出现那条 decision（也可以 `gofer_ask_human` 的同一套入口回答）；回答不会自动重启 leader —— 人自己把 todo 置 ready 或评论 @成员。

## 事件

都记在 **plan 的 scope**（`plan:<id>`）上：

| 事件 | 何时 | 详情 |
|---|---|---|
| `plan.leader_woken` | leader job 起来了 | `{plan_id, round, job(成员), member_status, leader_job, agent}` |
| `plan.leader_skipped` | 记了唤醒但没起 job | `{plan_id, job, reason}`，`reason` = `paused` \| `submit_failed`（提交被拒，该轮直接作废：修好配置后下一个成员终态会有新一轮，不每 10s 重试） |
| `plan.leader_cancelled` | 人插话取消了待唤醒轮 | `{plan_id, by, cancelled}` |
| `plan.leader_exhausted` | 轮次用尽 | `{plan_id, job, rounds}` |

`plan.leader_exhausted` **在通知默认集里**（同 `plan.blocked`：没人接手就不会再有人推进这条链），另外三个不在——订阅要在 webhook `events:` 里写明。

## 排查

```bash
gofer job list --tag leader                 # 所有 leader job（谁在替这个 plan 决策）
gofer job show <leader-job-id>              # 它的 prompt = 那一轮的决策简报；usage/超时照常
gofer plan show <plan>                      # plan 详情页显示「leader 第 N/M 轮」+ 最近一次 leader job 链接
gofer job events <leader-job-id>            # 它做了什么（gofer_comment 派活会留 comment.triggered）
```

- leader job 起来但没动作：先看它的本轮 prompt（是不是没人给 plan 描述/待办），再看限流（`comment.trigger_throttled`）。
- 唤醒记了但没起 job：`plan.leader_skipped{reason:"submit_failed"}` 的 `error` 字段（通常是 `supervisor.leader.agent` 不是这个 project 的 `allowed_agents` 成员，或它写了不存在的 agent/role）。该轮作废、不会反复重试，修好后下一个成员终态会有新一轮。
- 唤醒记录在库里（`plan_leader_wakes` 表）：`pending` 等 due、`firing` 正在 submit、`fired` 已起 job（`leader_job_id`）、`cancelled` 被人/暂停取消。

## 已知边界

- 只实现了 **plan** scope；`scopes` 写别的值校验会拒。
- leader job 落在**本机**（内置 local runner）：给 worker 跑会丢身份标记。
- leader 轮的 prompt 会带成员 job 的 stdout 尾部（40 行）——**别在日志里放 secret**。
- leader 是"又能自动花钱"的功能：先在一个项目上开，配合 `max_rounds_per_scope` 与 `plan pause`。
