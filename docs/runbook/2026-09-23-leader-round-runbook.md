# leader 回合（MCP-05 阶段 B）使用 Runbook

> 配套 design [`../design/2026-09-23-skills-binding-and-comment-routing-design.md`](../design/2026-09-23-skills-binding-and-comment-routing-design.md) §二.B，以及 LEAD-02 的按 plan 开启 [`../design/2026-09-23-job-credentials-and-leader-opt-in-design.md`](../design/2026-09-23-job-credentials-and-leader-opt-in-design.md) §二。
>
> **默认双重关闭**：全局 `supervisor.leader` 是**总闸**（没配 / `enabled: false` → 什么都不发生），而**每个 plan 自己还有一枚 `leader` 开关（默认 `off`）**。两把都开（`enabled: true` 且 `plans.leader = on`）才会在成员终态唤醒 leader；plan 没开时**连事件都不记**（每个成员终态记一条 skip 就是噪音）。

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
      ▼  它用 gofer CLI 做事：plan comment / plan comments / plan show / plan set-todo(ready|skipped) / job wakeup create / plan ask
      │     （挂了 gofer MCP 的 agent 也能用对应工具，底层是同一枚 job 凭证、权限完全相同）
下一步：@成员派活 / 把 todo 置 ready（带 assignee 即派发）/ 建 wakeup / 升级给人
```

**leader 永远不能 accept/reject**（GATE-01 §3 的人工验收边界不动）：它的 MCP 里根本没有 `gofer_accept_job`（从来就没有）与 `gofer_reject_job`（leader 面不注册）；HTTP 的 `POST /v1/jobs/{id}/accept|reject` 由 **SEC-01 凭证**拒绝——leader job 带着自己那枚 `kind=leader` 的 job token，server 按凭证（而不是请求体里自报的 `as_job`）判定身份 → 403 `job credential may not accept a delivery`。细节见 [job 凭证 runbook](./2026-09-23-job-credentials-runbook.md)。

## 开起来（两把开关）

### 1. 全局：参数 + 总闸（一次配好）

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
- leader 的 agent **不再需要** gofer MCP：动作面是 CLI，任何 agent 都会用；挂了 gofer MCP 的 agent（如 codex）仍可用对应工具，权限相同（同一枚 job token）。
- leader job **只在本机内置 local runner 上跑**（它的身份标记不进 wire，不能派到 worker/peer）。

### 2. 逐 plan：打开这个 plan 的 leader

```bash
gofer plan create --leader --title "迁移收尾" --project self   # 建的时候就开
gofer plan set <plan> --leader on                               # 或事后开
gofer plan set <plan> --leader off                              # 关掉（本轮窗口内未起的轮次会被丢弃）
gofer plan show <plan>                                          # leader: on (round 2/6)
```

HTTP：`PATCH /v1/plans/{id} {"leader":"on"|"off"}`；web：plan 详情页的 leader 开关（切到 on 时若该 plan 还有在跑的成员 job，页面会显示"N 个运行中的 job 结束后将唤醒 leader"的提示——这些 job 是开关之前起的，操作员看不到它们的存在会有意外）。

**升级影响（v0.58 → 本版）**：v0.57/v0.58 里 `enabled: true` 会让**所有** open plan 的成员终态唤醒 leader。现在 `plans.leader` 默认 `off`，所以升级后**一个 plan 都不会被唤醒**：原来靠全局开关跑着的部署，必须逐个 plan `--leader on`（或建 plan 时加 `--leader`）。这是有意的——真机验收时主机上另外 4 个 open plan 也会被醒。

## leader 拿到什么

prompt（`leader 回合 N：<plan 标题>`）：

```
你是 plan plan-xxx 的 leader（第 3 轮）。一个成员 job 刚刚结束，请你读完它的汇报，决定下一步：…

## Plan              标题 / 状态（含"已暂停"/"阻塞在 todo-x"）/ 目标
## 待办链现状         [status] todo-id 标题（指派：agent，依赖：…）— 备注最后一行
## 刚结束的成员 job    id / 标题 / 状态（error）/ 最近一次汇报（stdout 末尾 40 行）
## 你可以做的         六条 gofer CLI（plan comment / comments / show / set-todo / job wakeup create / plan ask），并写明"权限由凭证强制，越权 403"
## 你不能做的         不能 accept/reject、不能把 todo 标 done、不能改配置/push
## 规则               第 N/M 轮、人插话即接管、不要自己实现活
```

tags：`leader`、`leader_of:<成员 job id>`、`leader_round:<N>`；`channel: leader`；`caller_id: gofer`。它就是个**普通 job**：有 usage、有超时、失败走 AUTO-03 重试链。唯一被强制的是**不进验收队列**（`review: false` 且不可被项目 `require_review` 覆盖）——leader 轮的产出是"决定"，不是待人验收的交付物。

## 闸门与边界

| 规则 | 说明 |
|---|---|
| 总闸（全局） | 没配 `supervisor.leader` / `enabled: false` → 什么都不发生。plan 开了也没用：记 `plan.leader_skipped{reason:"global_off"}`（这个是要记的——有人把 plan 打开了，得知道为什么没动静） |
| plan 开关 | `plans.leader != on` → 什么都不发生，且**不记事件**（默认路径；每个成员终态记一条 skip 是噪音）。窗口内被 `--leader off` 关掉 → 取消该轮并记 `plan.leader_skipped{reason:"plan_off"}` |
| 只认成员终态 | `plan_id` 非空、**非** leader job（行上 `leader_of_plan` 为空）、tags 里**没有** `leader`、状态 ∈ {done, failed, needs_review}；`cancelled`/`timeout` 等人为终止不唤醒 |
| 一轮一条 | 同一个成员 job 只会留一条未取消的唤醒记录（重复走进终态不重复记） |
| plan 暂停 | `plan pause` 之后：唤醒时直接跳过（记 `plan.leader_skipped{reason:"paused"}`）；若在窗口内被暂停，到点那一刻取消该轮并记同一个事件 |
| plan 已完成 | 成员终态时 plan 已是 `done`（所有待办 done/skipped —— `advancePlan` 在这条 hook 之前就已经把 plan 收尾）→ 不记唤醒行，记 `plan.leader_skipped{reason:"plan_done"}`。链都完了，一轮 leader 没有可决定的事 |
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
| `plan.leader_skipped` | 记了唤醒但没起 job，或 plan 开了却被闸门挡住 | `{plan_id, job, reason}`，`reason` = `global_off` \| `plan_off` \| `paused` \| `plan_done` \| `submit_failed`（`submit_failed` = 提交被拒，该轮直接作废：修好配置后下一个成员终态会有新一轮，不每 10s 重试）。plan 自己没开时不记 |
| `plan.leader_cancelled` | 人插话取消了待唤醒轮 | `{plan_id, by, cancelled}` |
| `plan.leader_exhausted` | 轮次用尽 | `{plan_id, job, rounds}` |

`plan.leader_exhausted` **在通知默认集里**（同 `plan.blocked`：没人接手就不会再有人推进这条链），另外三个不在——订阅要在 webhook `events:` 里写明。

在哪看：`GET /v1/plans/{id}/events`（按 plan scope 查，**时间倒序**，`?limit=` 默认 50、上限 200，`?before=<seq>` 往前翻页）；web plan 详情页有折叠的**事件区**（复用 job 时间线同一套图标/标签）；`GET /v1/config` 的 `supervisor.leader` 块能读到总闸与参数（含解析后的默认值）。

## 局限：验收闸门曾经不是安全边界 —— **已由 SEC-01 解决**（2026-09-23）

v0.57 真机验收时发现：leader 的"不能 accept/reject"是**自报身份**（`as_job`）实现的，而 job 进程继承 serve 进程的环境（含 server bearer token），所以一个能跑 `curl` 的 agent 不带 `as_job` 直接以"人"的身份 accept 照样成功。**实测就是这样绕过去的**（leader 用继承来的 token 发评论、并用 CLI 把 todo 置 ready）。

SEC-01 之后：

- job 环境里**没有** server/worker token 了（`GOFER_TOKEN` / `GOFER_SERVER_TOKEN` / `GOFER_WORKER_TOKEN` 一律不再继承），取而代之的是该 job 自己的 `GOFER_JOB_TOKEN`；
- 身份由**凭证**决定，`as_job` 已废弃（server 忽略、CLI 不再发送）；
- accept/reject/cancel/改配置等写操作对**任何** job 凭证一律 403（默认拒绝），leader 的放宽只到"自己的 plan、`ready|skipped` 的 set-todo"。

也就是说："不能 accept"现在是**服务端强制的**，不再依赖 agent 手里有没有 `curl`。剩下的限制只有一条：leader 仍可能**判断错**——凭证保证它做不了越权的事，不保证决定正确；轮次封顶 + 人评论即接管 + `plan pause` 仍是兜底。配置与权限表见 [job 凭证 runbook](./2026-09-23-job-credentials-runbook.md)。

F10（2026-09-24，v0.60 真机复验的两个漏口）之后还有两条与 leader 直接相关：job 里**没有** `GOFER_CONFIG_DIR`（否则 CLI 会按它加载 server 的 `.env`，把操作员的 token 捡回来），且 job 内加载 dotenv 时跳过所有 `*_TOKEN` 键；job 里的 `gofer` 一律是**运行这个 job 的那个 gofer**（`PATH` 前置 + `$GOFER_BIN`），所以 prompt 里写 `gofer plan comment …` 就对了，不必担心主机 PATH 上是哪个版本。

## 排查

```bash
gofer job list --tag leader                 # 所有 leader job（谁在替这个 plan 决策）
gofer job show <leader-job-id>              # 它的 prompt = 那一轮的决策简报；usage/超时照常
gofer plan show <plan>                      # leader: on (round N/M) + 最近一次 leader job
gofer job events <leader-job-id>            # 它做了什么（CLI 的 plan comment 派活会留 comment.triggered）
curl  "$GOFER_SERVER/v1/plans/<plan>/events?limit=20"   # plan scope 的事件流（时间倒序）
```

- 什么都不发生：先确认**两把开关**（`gofer plan show <plan>` 的 `leader:` 行 + `GET /v1/config` 的 `supervisor.leader.enabled`），再看 plan 事件流里有没有 `plan.leader_skipped{reason:"global_off"|"plan_off"}`。
- leader job 起来但没动作：先看它的本轮 prompt（是不是没人给 plan 描述/待办），再看限流（`comment.trigger_throttled`）。
- 唤醒记了但没起 job：`plan.leader_skipped{reason:"submit_failed"}` 的 `error` 字段（通常是 `supervisor.leader.agent` 不是这个 project 的 `allowed_agents` 成员，或它写了不存在的 agent/role）。该轮作废、不会反复重试，修好后下一个成员终态会有新一轮。
- 唤醒记录在库里（`plan_leader_wakes` 表）：`pending` 等 due、`firing` 正在 submit、`fired` 已起 job（`leader_job_id`）、`cancelled` 被人/暂停取消。

## 已知边界

- 只实现了 **plan** scope；`scopes` 写别的值校验会拒。
- leader job 落在**本机**（内置 local runner）：给 worker 跑会丢身份标记。
- leader 轮的 prompt 会带成员 job 的 stdout 尾部（40 行）——**别在日志里放 secret**。
- leader 是"又能自动花钱"的功能：先在一个项目上开，配合 `max_rounds_per_scope` 与 `plan pause`。
