# 参考项目对比与吸收记录

> 只记"对 gofer 有什么可学 / 学了哪些 / 决定不学的原因"。条目编号与 [`../gofer-enhancements-roadmap.md`](../gofer-enhancements-roadmap.md) 对齐。

## WebCodex（yyjeqhc/webcodex，2026-09-16 对比）

单机 web 版 codex 任务台。已吸收 4 项，均落地：

| 吸收项 | gofer 落点 |
|---|---|
| 断线不失败（recovering） | RECOV-01（v0.42），[design](../design/2026-09-16-job-recovery-and-worktree-design.md) |
| 每任务独立 worktree | WT-01 `job run --worktree`（v0.42） |
| 只读任务 | `job run --read-only`（v0.45，ACP S2） |
| 模型不能接受自己的工作 | `needs_review` / accept / reject（v0.45，GATE-01 S3） |

不学：单用户单机模型、内嵌 codex 专用 UI（gofer 是多 agent / 多机）。

## Multica（multica-ai/multica，2026-09-20 对比）

"把 agent 当同事：指派 issue → agent 自己领取、边做边评论、交回 review"。Go 后端 + Next.js，本地 daemon 驱动 26 家 agent CLI，PostgreSQL。与 gofer 的重合面很大（server + 多机 runtime、agent CLI 驱动、review gate、用量、自动重试、IM、cron），差异主要在**工作模型以 issue 为中心**与**触发方式更丰富**。

### 值得学的（已进路线图）

| # | Multica 做法 | gofer 现状 | 路线图条目 |
|---|---|---|---|
| 1 | **同目录串行**：两个 run 目标同一本地目录时，后者进 `waiting_local_directory` 排队，不并发改同一 checkout | 无；同一 checkout 上并发派两个 agent job 会互相踩（本周只能人工串行） | **JOB-11 同 cwd 串行锁**（小、高价值） |
| 2 | **Wakeups**：agent 在 issue 上登记"事件订阅或定时器"后**结束运行**，输入到达时再获得一次普通 run；没有常驻进程，业务完成仍由 agent 读当前状态决定。25 种 issue 级事件 + at/every/cron | 有 cron schedules、会话中继 hook（常驻阻塞等人）；没有"job 级订阅→续投" | **JOB-09 job wakeups**（事件/定时 → `job resume` 续投；覆盖"等 verify / 等人回复 / 每小时巡检"而不占会话） |
| 3 | **指派即派发**：issue 设 assignee 为 agent 且状态到 `todo` 就自动起 run；`backlog` 不触发；同一 issue 可多次 run、换 agent | plan todo 已能挂 job（`--todo` 联动），但派发仍靠人敲 `job run` | **PLAN-02 todo 指派 agent 即派发**（todo 增 assignee/template/verify，状态→todo 自动 `job run -t … --todo`） |
| 4 | **失败分类**：runtime 离线 / daemon 重启 / 平台超时 / **codex 输出停滞**自动重试（普通 2 次、网络中断 3 次）；鉴权过期 / 配额耗尽 / 配置错 / 模型不可用**不重试** | 有 transient 模式 + 自动续投 + 故障转移；无"输出停滞"判定，卡死只能等超时 | **AUTO-05 输出停滞检测**（N 分钟无输出 → 当 transient 处理） |
| 5 | **Skills 是工作区资产，按 agent 绑定**：`SKILL.md`+脚本，可从目录/zip/URL/已连 runtime 导入，源引用可更新；"解决过的问题沉淀为 playbook" | 有 roles（system_inject）与任务书模板（F）；skill 只靠各 agent 自己的 `.claude/skills` | **JOB-10 skills 绑定**（项目/agent 级 skill 目录，派发时挂载/注入，`gofer skill import`） |
| 6 | **Autopilots** = cron **或 webhook 或手动**触发的固定任务 | schedules 只有 cron + run-now | **AUTO-02b schedule webhook 触发**（小） |
| 7 | **Squad**：一个 leader agent + 成员；先唤醒 leader，它用 `@成员` 评论派活，成员汇报后 leader 再被唤醒决定下一步/升级/转 in_review；leader 不实现、不做最终 done | 有 supervisor 路由（owner-first、答低危）、roles、presence/inbox | **MCP-05 leader 路由**（评论 @agent 触发 job + leader 回合）——依赖 PLAN-02 与"评论触发" |
| 8 | 并发上限双层：daemon 20 / 每 agent 6，取小 | 有项目/caller 上限，无 per-agent 上限 | 并入 JOB-11（`agents.<k>.max_concurrent`） |
| 9 | Token 用量按 agent **和按 issue** 汇总 | 按 job 与按 agent（E） | **plan 级用量汇总**（小，并入 PLAN-02） |

### 看过但不学 / 暂缓

- **Issue 全套（labels/properties/sub-issues/自定义状态目录）**：gofer 的 plan/todo 够用，不做项目管理软件。
- **Chat（对工作区随口提问）**：交互式 pty 会话 + 会话中继已覆盖。
- **Desktop / iOS / 多 workspace / 成员角色 owner-admin-member**：单人/小团队自用，不需要。
- **入站 IM（Slack/钉钉触发）**：OBS-07(b)(c)，用户已明确暂不做。
- **每成员可跑哪些 agent**：等有第二个使用者再说（OBS-06 扩展）。
- **Runtime 启动自动探测 CLI 并注册**：gofer 的 `detect` + worker caps 已等价。
