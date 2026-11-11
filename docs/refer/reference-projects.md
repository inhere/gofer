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

> 2026-10-19 补记：据第 7 行做的 supervisor（规则作答、AI 监督 job、claude-sup）因 agent 会话之间的通讯没打通（presence 只能靠 agent 轮询收件）而从未被使用，已于 2026-10-16 退役；plan leader 回合保留。信箱与会话送达（v0.173–0.176）落地后重做 Squad，见 gofer-ayas。

### 看过但不学 / 暂缓

- **Issue 全套（labels/properties/sub-issues/自定义状态目录）**：gofer 的 plan/todo 够用，不做项目管理软件。
- **Chat（对工作区随口提问）**：交互式 pty 会话 + 会话中继已覆盖。
- **Desktop / iOS / 多 workspace / 成员角色 owner-admin-member**：单人/小团队自用，不需要。
- **入站 IM（Slack/钉钉触发）**：OBS-07(b)(c)，用户已明确暂不做。
- **每成员可跑哪些 agent**：等有第二个使用者再说（OBS-06 扩展）。
- **Runtime 启动自动探测 CLI 并注册**：gofer 的 `detect` + worker caps 已等价。

## herdr（2026-09-26）

https://herdr.dev/zh-cn/docs/concepts/ — 终端里的 agent 编排器。吸收：工作区 → 标签页 → 窗格三层、agent 五态与**状态逐层上卷**（blocked 的 agent 让窗格/标签页/工作区都显示 blocked）、服务器持有进程 + 客户端随时分离重连、前缀键 + 鼠标原生。不照搬：它靠屏幕快照/前台进程猜 agent 状态，gofer 有结构化事件。落点：WEB-11 web 工作台（`design/2026-09-26-web-workbench-design.md`）。

## dsh-superagent-prototype 二次调研（deepseek-ai，2026-11-11，读到源码）

首次调研只读了 README 与 docs/000–005（结论已用于「等我」收敛，见 `design/2026-11-01-attention-surfaces-review-design.md`）。本次补读 006–007、SETUP 与 `src/sources/github*.ts`、`src/core/stage.ts`、prompts。上游 2026-10-31 重新提交了同一份内容，无新增。

| # | 做法 | gofer 现状 | 推荐 |
|---|---|---|---|
| 1 | **PR 跟到合并**：按分支名或上报编号绑定 PR；只在 CI 失败集合变化时产生输入；合并 / 关闭即结束工单并通知来源 | delivery `pr` 模式开完 PR 即止；workflow 有 `wait-ci` 步骤（`internal/delivery/step.go` 读 check-runs），但不把失败回灌给原 job，也不随合并完成 todo | 高 |
| 2 | **入站事件挂回已有工作**：同一线程上人的后续消息交给原工单，已结束的在原授权下恢复；机器人 / CI 消息不恢复 | trigger 只派一次性 job（trigger 设计 §2 非目标） | 高 |
| 3 | **健康分级与静默巡检**：`status` 退出码 0/1/2/3；每部件唯一写者的健康项；连续 N 次失败才报；巡检无变化不说话、尊重手动停止 | `serve status` 总返回 0；只有 `/health` | 高 |
| 4 | **中断后续跑带固定恢复说明**：先核对目录、git 与远端状态，不重复已做的外部动作 | 续跑 prompt 无此段 | 高 |
| 5 | 入站分 off / observe / auto，observe 只记「本会接受」 | trigger 无只记录模式 | 中 |
| 6 | 「无可行动」结束（dropped），不进「等我」 | 无对应结论 | 中 |
| 7 | 为人的消息预留执行槽、唤醒优先于新启动 | 并发信号量无预留 | 中 |
| 8 | 注入给模型的内容设体积预算并测漂移 | 无 prompt 体积测试 | 中 |

不学：LLM 路由的主会话与 Eval→Mandate 全套实体（模型判断归 xhen，gofer 只取确定性 gate）；改写历史的致谢 force push；worker shim / 独立 gitconfig（并入 SEC-02 再评估）；记录永不删除。

## Proma（proma-ai/Proma，2026-11-11）

本地优先的 Electron 桌面 agent 应用（单机单用户、按会话；AGPL）。主动协作设计稿里只有 Automation（定时任务）落地，Monitor / 推荐 / 审批未实现。

| # | 做法 | gofer 现状 | 推荐 |
|---|---|---|---|
| 1 | **定时任务运行健康**：上轮未结束则本轮记 skipped；连续失败达上限自动暂停；`maxRuns` 跑满自动停用（与手动暂停区分） | `sweepSchedules`（`internal/serve/serve.go`）到点即提交，不看上轮是否在跑；无失败计数与运行上限 | 高 |
| 2 | **定时任务跨轮上下文**：每轮注入任务身份与「不要再建定时任务」，配滚动笔记（开跑先读、结束追加） | schedule 只套参数，不带上轮结果 | 高 |
| 3 | **会话记录分步读取**：outline（每回合一行：角色、预览、工具次数、约 token）+ 按回合区间导出，超限写文件只回路径 | 只有 `job logs` / `session tail` | 中高 |
| 4 | 条件监视：便宜探针按指纹变化才派 agent（仅设计） | 有 trigger / wakeup / `wait-ci`，无通用探针 | 中 |
| 5 | agent 自管定时任务的工具（运行中省略 id 即自身，禁重入） | MCP 无 schedule 工具 | 中 |

归 xhen：Todo / 日程提醒、推荐引擎与降噪、IM 双向命令、桌面状态岛、记忆刷新邀请。归 suga：探索分支、单轮重试预算、会话内消息排队。不学：父会话代答子会话的权限请求（gofer 不让父 job 替子 job 作答）；定时任务强制放开权限；进程内 50 个子会话。

## liaison（liaisonio/liaison，2026-11-11）

自托管私有服务访问网关（Go manager + edge，AGPL）：connector → application → access，浏览器工作区、LLM 范围 key、浏览器驱动 codex / claude、code-server。以「访问资源」为中心，无 job / 计划 / 验收；对 gofer 的价值在机器接入、凭据生命周期与审计。

| # | 做法 | gofer 现状 | 推荐 |
|---|---|---|---|
| 1 | **管理操作审计**：每次变更记用户、动作、资源、IP、成败、耗时，可筛选 | 只有鉴权审计（`auth_audit`）；`routeDecls` 已有读写分类，可在中间件统一记 | 高 |
| 2 | **worker 一行接入 + 注册为用户级服务 + 远程卸载** | 有 `worker join` 配对与远程升级；新机器需自备二进制；`internal/servicemgr` 只托管 serve，worker 只有 `-d` | 高 |
| 3 | 调用方 token 到期、最近使用时间、可识别前缀 | caller 有 hash / 停用 / 轮换（gofer-nhh7 入库进行中），无到期与最近使用 | 中 |
| 4 | 回合耗时分解（首次进展、等人、工具活跃） | 无 | 中 |
| 5 | client 浏览器回环登录 / 配对接入 | `init client` 写明文 token | 中 |
| 6 | 按 job 签发 LLM 虚拟 key 与用量账本 | 无模型网关；边界需先设计（provider 归 suga） | 中低 |
| 7 | worktree 浏览器 IDE / 只读文件浏览、worker 负载上报、来源 CIDR 限制、工作台 slash 菜单 | 均无 | 低 |

安全注意：一行安装命令不得带秘密（liaison 把 AK/SK 放在 `curl \| bash` 命令行里），脚本与二进制校验 sha256、拒绝 root。不学：浏览器 DB / RDP / SSH 工作区与 LLM Playground（访问网关产品面）；内置 shell agent（归 suga / xhen）；Casbin 组织域 IAM（粒度不如现有 authz）；网段扫描式服务发现。

## LobeHub（lobehub/lobehub，2026-11-11）

已转型为 "Super-Agents on Your Team"：多租户 SaaS（Hono/tRPC + PostgreSQL），Task 指派 agent、外接 15 种 CLI、执行设备、GitHub PR 闭环、IM 入站。模型判断内置在平台；按分工 gofer 只取其工程细节。高推荐：PR 闭环细节（三种绑定方式、合并即验收、CI 失败带日志尾唤醒且每 PR 限次合批、只认可信评审人、单条跟踪评论原地更新；`apps/server/src/services/scm/`）；人的打回理由提炼为交付规范（`services/expertise/`）。中高：外部 issue 单向同步（`taskExternalLink`）、准入策略逐条 off / shadow / enforce 且决策落库（`conversationWake/aggregator.ts`）、线程跟进状态机与暂存重放（`conversationWake/scheduler/`）。其余见 [统一整理](2026-11-11-super-agent-workflow-survey.md)。不学：插话分类器与打扰策略（归 xhen）、provider 与上下文引擎（归 suga）、Marketplace / RAG / 多 workspace RBAC。

## crewbot-connector（super-agent.team 的开源本地 daemon，2026-11-11）

约 1.9k 行 TS：出站 WebSocket 把网页 bot 会话映射到本机 ACP 进程。服务器被完全信任（网页可直接给 `full` 档），不学其安全模型。可借鉴：外部线程 id ↔ agent 会话 id 两层映射、resume 失败降级新会话；入站至少一次投递 + msgId 去重 + 接收回执（prompt / injected）；出站按会话递增序号、确认后清 outbox、重连补传（gofer worker 镜像事件现为断线即丢，`internal/worker/client.go`）；运行中插话按 ACP 能力选择插入或排队；被吊销即自清凭据、三态核对防误删。不学：运行时 npm 安装适配器并改写 codex 产物打开联网、logout 递归删除工作目录、内存中的去重与 outbox。
