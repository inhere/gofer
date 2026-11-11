<!-- template_id: design; template_version: 1.2.0 -->
# 第 1 批：todo 按交付完成、长 job 记录按回合查看 设计

> 状态：Draft 0.1 / 待评审
> issue：gofer-94yq、gofer-mmh0（含 gofer-tykh）；随批 Quick：gofer-gqap、gofer-8hlc、gofer-ehvd、gofer-r20b、gofer-5x85、gofer-juo0
> 来源：`docs/refer/2026-11-11-super-agent-workflow-survey.md`「分批」第 1 批

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-11 | Claude（gofer 监督会话） | 初稿 |

思考模式：RIGOROUS。核心目标：①plan 里 worktree 交付的 todo 在真正合入（或确认无需合入）后才算完成，后续 todo 不被提前派发；②监督方能按回合低成本读长 job 的记录，多回合 job 不再只剩最后一条回复。Scope freeze：下文「范围」；Review budget：两轴各一轮，有 `CORE_BLOCKING` 才继续。

## 背景与目标

1. **todo 完成太早（gofer-94yq）**：`linkTodoOutcome`（`internal/job/todolink.go:101`）在 job `done` 时立刻把 todo 置 done 并 `advancePlan`。worktree 交付的 job「结束」不等于「已合入」：2026-11-03 审查退回返工时 todo 已显示完成、进度虚高，后续 todo 可能被提前派发。自动交付（`autoDeliver`，`branch_delivery.go:82`）是 job 终态之后的 hook，交付失败时 todo 早已 done。
2. **长 job 记录难读（gofer-mmh0、gofer-tykh）**：监督与验收时只能 `job logs`（整份 stdout / stderr 的头尾窗口）。多回合 job 的 stdout 只剩最后一轮回复：suga 的 stream-json 每回合 `turn_end` 不带正文，只有最终 `result` 带最后一轮文字（实测 job-20261111-161820-acb5d2d8：stderr 84 行事件，`turn_start` ×2、`turn_end` ×2、`result` ×1，第一轮的完整汇报不在任何事件里）。

## 范围与非目标

范围：todo 完成时机与「待合并」可见性；`job transcript`（回合概要、按回合导出、搜索）的 CLI / HTTP / MCP；随批 Quick 修复。

非目标：PR / CI 状态自动跟进（第 4 批 gofer-eeod）；远程 worker 上 job 的会话文件读取（只读结果目录里已回传的产物）；codex exec 纯文本日志的回合切分（v1 只支持整份分页与搜索）；改动 suga（只提需求，见 §2.3）。

## 已确认事实（代码，main 1ff19283）

- `linkTodoOutcome` 调用点：`execute.go:871`（终态）、`review.go:170`（accept / reject）、`hold.go:763`。`needs_review` 时 todo 保持 doing、不推进（现状正确）。
- `job worktree merge --todo-done`（`worktree.go:733` `markMergeTodoDone`）置 done 并 `PlanTodoChanged` → 推进 plan；不带 flag 时 todo 不动（flag 默认关，`commands/job.go:1202`）。
- 自动交付模式 `none / merge / push / pr`（`delivery/execute.go:20-23`）；交付结果经 `recordDelivery` 落库；没有可交付的提交时记 `job.delivery_skipped`（`branch_delivery.go:91`）。
- 现有转录解析：`internal/work/transcript` 已能解析 claude、codex rollout、omp 三种 jsonl 方言为回合列表（为工作项摘要服务，只读文件尾部）。
- job 结果目录产物：cli 类（claude / codex / suga）为 `stdout.log`（最终文字）+ `stderr.log`（claude / suga 为 stream-json 事件，codex 为纯文本过程日志）；ACP 类 `stdout.log` 带 `--- turn N ---` 分隔（`runner/acp/runner.go:321`）。

## 总体方案

### 1. todo 按交付完成（gofer-94yq）

`linkTodoOutcome` 的 `StatusDone` 分支按 job 的交付形态决定（新函数 `todoCompletion(snap) (complete bool, note string)`，放新文件 `internal/job/todo_completion.go`）：

| job 形态 | todo | 推进 plan |
|---|---|---|
| 非 worktree job（含 `ref:` 临时检出） | done（现状） | 是 |
| worktree job，交付模式 `none` | 保持 doing，note「已完成，待合并：分支 B（job J）」 | 否，待 `worktree merge`（Q2）或人工 `plan set-todo done` |
| worktree job，`merge` | 交付成功（`recordDelivery` 成功）时 done；失败保持 doing 并记原因 | 交付成功时 |
| worktree job，`push` / `pr` | 保持 doing，note「已推送 / PR 已开，待合并」（Q3） | 否（第 4 批自动跟进 PR） |
| worktree job，无可交付提交（`delivery_skipped`） | done，note「无改动」 | 是 |

- `needs_review` 与 accept 路径不变：accept 后以 `StatusDone` 再进同一判定。
- 交付成功的 todo 完成放在 `recordDelivery` 之后的一行调用（`branch_delivery.go`），复用 `PlanTodoChanged`。
- **「待合并」可见**（Q4）：「等我」新增一类「待你合并」：todo 为 doing、其最近 job 为 done 且 worktree 分支仍领先基线、未被合并清理。判定只用结构化事实（todo 状态 + job 行的 `worktree_branch` / `worktree_path` + 分支 ahead 计数），与现有「待你验收」同处。

### 2. 长 job 记录按回合查看（gofer-mmh0）

#### 2.1 接口

- CLI：`gofer job transcript <id>`（job 是核心名词，放 job 组）
  - 默认 / `--outline`：每回合一行——序号、角色、首行预览（≤80 字）、工具调用次数（如 `Read×3 Bash×2`）、约 token、是否出错。
  - `--turns A-B`、`--head N`、`--tail N`：按回合导出全文；`--search <词>`：列出命中的回合与片段。
  - 输出超过上限（默认 64KB）时写入结果目录下 `transcript-export-<ts>.md`，只打印路径（G045 文档化）。
- HTTP：`GET /v1/jobs/{id}/transcript?outline=1|turns=A-B|search=…`，权限与 `GET /v1/jobs/{id}/logs/stdout` 相同（`PermJobReadOthers` 在 handler 内过滤，G056 登记）。
- MCP：`gofer_job_transcript`（同参数），给监督会话与子 agent 用。

#### 2.2 解析（读时解析，不新增存储，Q6）

新包 `internal/jobtranscript`：输入 job 结果目录，按 agent 产物选解析器，输出统一的 `[]Turn{Index, Role, Text, Tools map[string]int, Tokens, Error}`：

| 来源 | 解析 |
|---|---|
| claude / suga stream-json（`stderr.log`） | 复用 `internal/work/transcript` 的 claude 方言；suga 方言新增（`turn_start` / `tool_call` / `tool_result` / `turn_end` / `result`） |
| ACP（`stdout.log` 的 `--- turn N ---` 分隔） | 按分隔切回合，正文即该回合输出 |
| 其他（codex exec 纯文本等） | 不切回合：整份作为一个回合，`--head/--tail/--search` 按行工作 |

`internal/work/transcript` 只读文件尾部、为摘要服务；新包需要全文与回合边界，把两者共用的方言解析抽成内部函数供两处调用（不复制逻辑；G024 不升包，留 `work/transcript` 内导出最小函数）。

#### 2.3 多回合只剩最后一条回复（gofer-tykh）

根因在 suga 的事件流（见背景 2），gofer 无法补回。向 suga 提需求（Q5）：`turn_end` 事件带该回合的最终回复文本（或每回合发一条 `message` 事件）。gofer 侧：解析到回合文本即用；`job logs` 默认的 stdout 不变（仍是最终回复），在多回合 job 的 `job show` / `job logs` 末尾提示「共 N 回合，`job transcript <id>` 查看各回合」。suga 未发版前，suga job 的 outline 只有工具统计，没有回合正文。

### 3. 随批 Quick 修复（不另写设计，按 SR1404 直接改 + focused test）

| issue | 改动 |
|---|---|
| gofer-gqap | 续跑类提示词模板（`auto-resume`、`uncommitted-resume`、`delivery-check-resume`、故障转移前缀 `fallbackPrompt`，zh-CN / en）补一句：先核对远端与外部状态（已 push 的分支、已开的 PR、已发出的消息），不要重复已经完成的外部动作。现有「先 git status / git log，不重做已提交部分」保留 |
| gofer-8hlc | `internal/brief/refocus.go:174` 命令改为 `gofer session mail read <id>`，同步两处测试 |
| gofer-ehvd | 换 agent 被跳过时在源 job 记事件 `job.fallback_skipped{agent, reason}`，`job show` 显示 |
| gofer-r20b | 带上限调用方缺 runner 时先报 `runner is required`（400），再判上限 |
| gofer-5x85 | `job rerun` 重提续跑 / 转移 job 时带上 `WorktreeFrom`，worktree 失效即按续跑守卫拒绝 |
| gofer-juo0 | 待办接续条件 6 改用 `worktreeUsable`，与续跑守卫同口径 |

## 架构

- 新文件：`internal/job/todo_completion.go`（§1 判定）、`internal/jobtranscript/`（§2 解析，数据层，被 job / httpapi / mcpserver 调用；不 import 入口层，G022）。
- 入口层只做参数绑定与转发：`internal/commands/job_transcript.go`（新文件，`job.go` 已 4216 行，GR104）、`internal/httpapi` handler、`internal/mcptools` 工具（G021）。
- CLI 经真实分发测试（GR110）；新路由登记 `routeDecls` 并重新生成权限文档（G056）。
- 「等我」新类别加在现有判定 `internal/inbox/waiting.go`，不新建模块。

## 关键流程

1. plan 中 todo T1（worktree，交付 `none`）→ job 完成 → T1 仍 doing、note「待合并」→「等我」出现「待你合并」→ 监督方 `worktree merge`（默认带 `--todo-done`）→ T1 done → plan 推进到 T2。
2. 同上但交付 `merge` → job 完成 → 自动交付成功 → T1 done → 推进；交付冲突失败 → T1 doing、note 写原因、plan 不推进。
3. 监督方 `gofer job transcript J` → 看到 3 回合概要 → `--turns 2-3` 取全文。

## 安全、数据、运维与回滚

- 行为变化：worktree 交付的 todo 不再在 job 结束时完成；`worktree merge` 默认完成所属 todo（Q2）。已有 plan 中处于 doing、job 已 done 的 todo 不做迁移（下次合并或人工收尾时完成）。
- 转录读取只读结果目录，权限与日志读取一致；导出文件写在该 job 的结果目录内；搜索词不进日志（日志不含 prompt 与正文）。
- 不改 schema（「待合并」判定读现有字段；ahead 计数在判定时用 git 只读命令，结果可缓存在内存）。
- 回滚：回退合并提交。

## 决策

| 编号 | 问题 | 选项 | 推荐 |
|---|---|---|---|
| Q1 | 交付 `none` 的 worktree todo 何时完成 | A：合并后（或人工）；B：维持 job 结束即完成 | **A**。这是 gofer-94yq 的本意 |
| Q2 | `worktree merge` 是否默认完成所属 todo | A：默认完成，`--no-todo-done` 关闭（`--todo-done` 保留为无副作用的显式写法）；B：维持需显式 `--todo-done`，不带时输出提醒 | **A**。Q1 之后不带 flag 的合并会让 todo 永远停在 doing |
| Q3 | 交付 `push` / `pr` 的 todo | A：保持 doing「待合并」，人工或第 4 批收尾；B：推送成功即完成 | **A**。推送 / 开 PR 不等于合入 |
| Q4 | 「待合并」是否进「等我」 | A：进，新类「待你合并」；B：只在 plan show / todo note 里写 | **A**。否则 todo 静默卡住 |
| Q5 | 回合正文缺失 | A：向 suga 提需求（`turn_end` 带回合回复），gofer 先上解析与提示；B：gofer 侧改为每回合截获 stdout（suga 的 stdout 只在进程结束写一次，做不到） | **A** |
| Q6 | 转录存储 | A：读时解析结果目录产物，不新增存储；B：执行时另写一份规范化 transcript | **A**。产物已在，避免第二份真相 |

## 待确认事项

- Q1–Q6。

## 验收

- 单测：§1 表每行一例（含交付失败保持 doing、无可交付提交即完成、accept 后进入同一判定）；merge 默认完成 todo 与 `--no-todo-done`；「待你合并」出现与合并后消失。
- 单测：三类来源解析（claude、suga、ACP 分隔、纯文本回退）；`--turns` / `--search` / 超限写文件；HTTP 权限与他人 job 过滤；CLI 真实分发；MCP 工具。
- 真实验收：plan 两个 worktree todo 串联，第一个完成后第二个不派发，`worktree merge` 后才派发；对一个多回合 suga job 与一个 ACP job 跑 `job transcript --outline` / `--turns`。
- 反向验证：去掉 §1 判定 → 「交付 none 不推进」用例失败；去掉回合切分 → ACP outline 用例失败。
- Quick 项：各自 focused test。

## 结论与人工计划 Gate

把 todo 完成点从「job 结束」移到「交付落地」，并补上「待你合并」的可见性；新增按回合读取 job 记录的统一入口。等用户答复 Q1–Q6 并批准后写计划。suga 侧需求（Q5）批准后发给 suga 会话。
