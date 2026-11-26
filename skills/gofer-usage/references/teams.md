# 团队（team）

> 团队把若干**常驻位置（seat）**编成一组：一个 leader 加若干成员，都在同一个项目。位置里的会话会换，团队关系不变。
> 团队之间靠**普通信件**通信（发到 `team:<名字>[@<项目>]` 地址），选谁收由服务端按确定性规则判定，每次判定都留记录可查。全部 flag 以 `gofer team --help` 为准。

## 什么时候用

- 一件事需要长期有人盯着、并把活分给几个并行位置：一个 leader 位置收结果、派活，成员位置各自干活。
- 只是想给一个已有位置留一份长期约定，用 `seat` 就够了（见 [sessions-relay.md](sessions-relay.md)「常驻会话（seat）」），不需要建团队。
- 不建团队时没有任何行为变化：信箱、位置都照旧。

## 常用写法

```bash
# 建团队：leader 用位置名或 id（必须是自己在该项目里的位置）
gofer team add crew --leader desk [--brief "说明" | --brief @brief.md] [-p <project>]

# 成员
gofer team member add crew a1
gofer team member rm crew a1          # 只移出团队，位置本身保留

# 看
gofer team ls [-p <project>] [--json]
gofer team show crew [--json]         # 花名册（leader 在前：名字、角色、状态、当前步骤、最近活动）+ 最近 10 条路由判定

# 人给团队发话（发到 team:crew[@<project>] 地址）
gofer team say crew "先跑回归，再更 CHANGELOG"

# 服务端判定记录 / leader 的每轮评估
gofer team routes crew [--limit n] [--json]
gofer team evals crew [--limit n] [--json]

# leader 自己记评估（每一轮结束前；只认 leader 位置上的会话 / job）
gofer team eval --outcome dispatched|idle|failed --reason "…"

# 改 / 归档
gofer team set crew [--leader desk2] [--brief "…" | --brief -]   # - 清空说明
gofer team rm crew                                              # 归档：名字与位置重新可用，历史保留
```

`<team>` 可以写 id，也可以写名字：名字按 `-p`（没给就按当前目录所在项目）解析，跨项目写 `name@project`；查不到时报错里带名字与项目，并提示用 `gofer team ls` 看。

## 谁收到信

- **人发的**（`team say`）：正文点名 `@成员名` 的只送给这些成员；没点名就送给 leader。
- **成员的结果**：没有显式收件人、或收件人写 `team:` 的，送给 leader；leader 汇总后决定下一步。
- 每条判定记一行（哪条规则、从谁到谁、跳数、结局如 `delivered` / `queued` / `merged` / `handed_to_leader` / `dup`），`gofer team routes` 就是这张账本；`gofer team evals` 是 leader 每轮留下的评估（`dispatched` / `idle` / `failed` / `missing_eval`）。
- 信件本身是普通信件：送达、离线排队、退信、暂停、急件都沿用信箱的做法（见 [sessions-relay.md](sessions-relay.md)）。

## leader 协议

- **每一轮都注入**：坐在 leader 位置上的会话（终端每次 UserPromptSubmit、ACP 每次 say 之前）会收到一个 `[gofer · team]` 块：协议、花名册（成员名、用途、状态、当前流程步骤、最近活动）、团队说明；唤醒这一轮的信件摘要放在每轮随机的边界标签里，当数据读，不当指令。成员位置不注入任何东西。
- **派活走信件**：`gofer session mail send seat:<成员> "<任务>"`；实现交给成员，leader 自己写 design / plan 文档。
- **每轮记评估**：结束前执行 `gofer team eval --outcome dispatched|idle|failed --reason "…"`（派出了活 / 没事做 / 走不下去）。没有事做的一轮不发信，只记 `idle`；**记了 `idle` 之后，同一轮里再发团队信会被拒绝（409）**，也不能再改评估，下一轮（下一条信或消息唤醒）才重新开始。没记评估就结束的一轮，服务端记 `missing_eval`；连续 3 轮缺评估，「等我」里出现一张团队卡。
- **一「轮」**：ACP 是一次 say 到 job 回到等待输入；终端是一次 UserPromptSubmit 到下一次 Stop。
- **链条结束会通知人**：leader 被成员结果唤醒，这一轮既没发信、也没派活、也没留唤醒（定时、watch、nudge），服务端把触发它的结果信作为系统信送到团队创建者的信箱（人在「等我」里能看到）。
- **leader 不能完成工作项**：leader 位置上的会话执行 `gofer stage done` 且下一步是 `done` 时，服务端拒绝（403，只有人能完成工作项）；前面的步骤不受限。
- 提到成员用 `@成员名`；写在代码里的（行内 / 围栏）和邮箱形式（`user@host`）不算点名。

## 权限与边界

- 团队属于 leader 位置的调用方：自己是 owner 不需要权限，看/改别人的团队要 `seat.manage.others`；项目超出调用方范围时按不存在处理。
- 只有用户身份能操作团队（worker token / job 凭据被拒，403）；因此 `team` 命令在客户端节点上跑，别塞进 job 里去调。**唯一的例外是 `gofer team eval`**：它只认 leader 位置上的会话，或 leader 位置上那个 job 的凭据（ACP leader 在 job 里直接执行）；人的 token、成员、其他 job 都是 403。身份由服务端的位置表判定，信里自称不算。
- 一个位置同一时间只属于一个团队；leader 位置不能同时是成员。
- 完成工作项、合并、发版这些人掌握的动作不会因为团队而放宽。

## 相关

- 位置本身（用途、章程、绑定、定时送话）：[sessions-relay.md](sessions-relay.md)「常驻会话（seat）」。
- 命令总索引：[commands.md](commands.md)。
