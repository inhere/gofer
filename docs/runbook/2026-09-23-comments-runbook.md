# 评论与 @派活（MCP-05 阶段 A）使用 Runbook

> 配套 design [`../design/2026-09-23-skills-binding-and-comment-routing-design.md`](../design/2026-09-23-skills-binding-and-comment-routing-design.md) §二（阶段 A）。**不评论就是老行为**：没有评论行、没有新事件、没有新端点被调用时，job/plan 的一切与以前逐字节一致。

## 是什么

给 **job / plan / todo** 加一层评论：

- **评论就是一条记录**：`comments` 表（`id`=`cm-<8hex>`、`scope`=`job|plan|todo`、`scope_id`、`author`、`author_kind`=`user|agent|system`、`body`、`mentions_json`、`created_at`、`triggered_job_id`）。随对象存亡：job 被 retention 清掉 / workflow 的 step-job 被清掉 / todo 被删，它的评论线程一并删。
- **`@<agent|role>` 即派活**：**user 作者**的评论里每个提及起一个 job，走**同一个 `Submit` 入口**（和 `job run`、plan 派发同一条路），并把 `triggered_job_id` 回写到评论行上（一条评论提及多个时回写第一个，全部结果在响应与事件里）。
- **agent 作者只记录**：MCP 工具 `gofer_comment` 发的评论作者是那个 job 的 agent（`author_kind=agent`），**不派活**。阶段 B 的 leader 白名单会在同一个闸门上放开（`commentAuthorMayTrigger` 这个 seam）。

## 用

```bash
# job 线程（web job 详情页底部也有同一块）
gofer job comment 20260923-135117-b721eca6 "@echoer 请接着把这一步做完"
gofer job comments 20260923-135117-b721eca6

# plan 线程 / 某个待办自己的线程
gofer plan comment plan-20260923-abc "这一步按 X 做"
gofer plan comment plan-20260923-abc --todo todo-20260923-xyz "@omp 你来做"
gofer plan comments plan-20260923-abc

# 提示里身份规则：在 job 里跑（GOFER_JOB_ID 已设）时，CLI 以该 job 的 agent 身份发评论
# （只记录、不派活）。要当"人"发（会派活），unset GOFER_JOB_ID，或直接用 web 评论区。
```

- **HTTP**：`POST|GET /v1/jobs/{id}/comments`、`/v1/plans/{id}/comments`、`/v1/plans/{id}/todos/{todo_id}/comments`（嵌套形式会校验该 todo 属于该 plan）、`/v1/todos/{todo_id}/comments`（只用 todo id 定位，CLI/MCP 走这条）。POST body `{"body":"…","as_job":"<job-id>"?}`；POST **只有 user caller 能用**（worker token → 403），GET 谁都能读（worker 也要能看自己 job 的线程）。
- **MCP**：`gofer_comment {scope, id, body, as_job?}`（`as_job` 省略时读 `GOFER_JOB_ID`；两者都没有就报错——评论必须说清是谁写的）、`gofer_list_comments {scope, id}`。
- **事件**（都不在通知默认集里，要订阅就在 webhook `events:` 里写明）：
  - `comment.created {comment_id,scope,scope_id,author,author_kind,mentions}`
  - `comment.triggered {comment_id,job_id,mention,kind}`（记在被评论对象的 scope 上：job 自己的时间线 / `plan:<id>`）
  - `comment.trigger_throttled {comment_id,reason,count,last_at}`（`reason` = `min_interval` | `max_per_scope`）
  - `comment.mention_rejected {comment_id,mention,reason,error}`（`reason` = `unknown` | `not_allowed` | `submit_failed`）

## 闸门（这是会自动花钱的功能）

派活必须**同时**过四道闸，任何一道不过就"评论保留 + 说清原因"，绝不静默：

| 闸门 | 规则 |
|---|---|
| 作者 | 只有 **user caller** 的评论能触发（worker token 连发评论都 403；agent 作者的评论只记录） |
| 存在 | 提及名必须是已配置的 agent 或 role（同名时 agent 优先） |
| allowlist | 该 agent（role 取 role 的 agent）必须在**评论所在对象 project** 的 `allowed_agents` 里；`allowed_agents` 为空 = 不限制（与 job 自身的准入同一条规则） |
| 限流 | 每个 scope（那条 job / plan / todo）**1 分钟内最多 1 条派活评论**、**累计最多 10 条**；超限记 `comment.trigger_throttled`，不派 |

- 被拒的提及会**追加一条 `author_kind=system` 的评论**（`@nobody 没有派发：…`），人在自己写的地方就能看到原因；限流只挡"派"，不挡这条说明。
- 限流配置**可热改**（web 设置页 / `PUT /v1/config/server`）：

```yaml
server:
  comment_trigger:
    min_interval_sec: 60   # 不写=60；显式 0 = 关掉这条闸
    max_per_scope: 10      # 不写=10；显式 0 = 不设上限
```

## 派出去的 job 拿到什么

`prompt` = 上下文摘要 + 评论正文（原文照抄，不改写）：

```
你在一条评论里被 @ 点名接手工作（gofer 评论派活）。

被评论的 job：20260923-135117-b721eca6 "smoke source"
状态：done
最近一次汇报（stdout 末尾 40 行）：
SOURCE-REPORT-MARKER-9

评论正文：
@echoer 请接着把这一步做完
```

- job 评论：被评论 job 的 **id / 标题 / 状态** + **stdout 尾部 40 行**（读不到日志就不放这一块，不是失败）；`cwd` 取该 job **request 里的相对 cwd**（行上的绝对 cwd 是本机视角，不能继承），`runner` 继承该 job 的 runner（worker job 就还是那台 worker）。
- plan 评论：plan 的标题/状态/暂停/阻塞 + 目标 + 待办清单（最多列 20 项）。
- todo 评论：该 todo 的标题/状态/备注（+ 所属 plan）；`cwd`/`runner` 取该 todo 的派发字段（未设则用内置 local）。
- 另加 tags `from_comment`、`comment_of:<cm-id>`，`channel: comment`，`caller_id` = 评论作者，方便 `job list --tag from_comment` 回看"哪些活是评论派出来的"。

## 已知边界

- 阶段 A **没有** leader 回合：agent 评论不会自动唤醒任何人（阶段 B）。
- web 只挂了 **job 详情**与 **plan 详情**的评论区；每个 todo 自己的线程已可用（`/v1/todos/{id}/comments`），plan 页的待办行还没做展开。
- 评论正文会进 prompt，**别放 secret**（同 skills）。
- 时间线里 comment.* 事件目前只有默认图标与事件名（未加中文标签）。
