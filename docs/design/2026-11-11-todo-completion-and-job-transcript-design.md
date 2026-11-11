<!-- template_id: design; template_version: 1.2.0 -->
# 第 1 批：todo 按交付完成、长 job 记录按回合查看 设计

> 状态：Draft 0.3 / 待评审
> issue：gofer-94yq、gofer-mmh0（含 gofer-tykh）；随批：gofer-gqap、gofer-8hlc、gofer-juo0（Quick），gofer-ehvd、gofer-r20b、gofer-5x85（小改，接口变化见 §4）
> 来源：`docs/refer/2026-11-11-super-agent-workflow-survey.md`「分批」第 1 批

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-11 | Claude（gofer 监督会话） | 初稿 |
| 0.2 | 2026-11-11 | Claude（gofer 监督会话） | 按两轴评审修订：完成时机改为 plan 级 `complete_on`，默认维持现状，避免破坏 `continue_from` 与未合并分支基线（F1）；merge 交付只认 `merged` 状态、完成判定放进 `recordDelivery`（F4）；无提交按 `CommitsAhead` 判定、worktree 判定用 `WorktreeBranch`（F5）；远程 job 与 `worktree rm` 的处理（F6/F7）；续跑守卫决定 resume 时不完成（F10）；「待你收尾」卡片位置与只读结构化事实（F9/S4）；merge 不改 wire 默认值（F8/S2）。转录改为执行时写按回合的附属文件 `turns.jsonl`（claude 投影器无正文，F2；suga 的 `message_delta` 可累积，tykh 不再依赖 suga，F3）；接口改分页、不在 server 写文件、CLI `--out` 本地导出（S1）；业务入口 `job.Service.Transcript`、路由 `permDeny`、MCP 位置与命名（S6/F12/F16）；参数校验（SR1301）。ehvd / r20b / 5x85 移出 Quick 单列（S3）；文档同步清单（S5）；GR104（S7） |
| 0.3 | 2026-11-11 | Claude（gofer 监督会话） | 第 2 轮修订：续跑提交失败时补做完成判定（N1）；转录游标带回合内偏移，大回合可取完（N2）；ACP 在回合结束写、handler 累积本回合文本（N3）；suga `retry{restart}` 只截最近一段（N4）；回合方言按 agent 注册、未配置不写（N5）；卡片按 job 行的交付状态判定，不读 note（N6）；`turns.jsonl` 由 job 层打开并脱敏、接线在 `captureNDJSON`（Gov N1 / Spec N7）；行类型单一定义（Gov N3）；claude 文本由新累积器自行解析（N8）；远程回写幂等（N9）；复用 `store/safejoin.go`（N10）；`complete_on` 校验与显示（N11）；文档清单补齐（Gov N2）；suga 事件契约登记跟进 issue（Gov N4） |

思考模式：RIGOROUS。核心目标：①需要人合并的 plan 里，worktree 交付的 todo 在真正合入（或确认无需合入）后才算完成，后续 todo 不被提前派发；②监督方能按回合低成本读长 job 的记录，多回合 job 不再只剩最后一条回复。Scope freeze：下文「范围」；Review budget：两轴各一轮，有 `CORE_BLOCKING` 才继续。

## 背景与目标

1. **todo 完成太早（gofer-94yq）**：`linkTodoOutcome`（`internal/job/todolink.go:101`）在 job `done` 时立刻置 todo done 并 `advancePlan`。监督式开发里 worktree job「结束」不等于「已合入」：2026-11-03 审查退回返工时 todo 已显示完成、进度虚高，后续 todo 被提前派发。
2. **长 job 记录难读（gofer-mmh0、gofer-tykh）**：只能 `job logs` 看 stdout / stderr 头尾。多回合 job 的 stdout 只剩最后一轮回复（job-20261111-161820-acb5d2d8：两回合，stdout 只有第二轮）。stderr 是投影后的事件摘要，不含正文。

## 范围与非目标

范围：plan 级完成时机与「待你收尾」卡片；执行时写按回合附属文件与 `job transcript` 的 CLI / HTTP / MCP；§3 Quick 与 §4 小改。

非目标：PR / CI 自动跟进（第 4 批）；远程 worker job 的附属文件回传（v1 远程 job 只能分页读 stdout / stderr）；codex exec 纯文本日志的回合切分；gofer-ehvd 的第二个子问题（resume 先于 fallback 导致链停住，另行处理）；向 suga 要求新事件（可选：请 suga 写明 stream-json 事件契约，见 §2.2）。

## 已确认事实（代码，main 1ff19283，经两轴评审核实）

- `linkTodoOutcome` 调用点：`execute.go:871`（终态）、`review.go:170`（accept / reject）、`hold.go:763`；它先于 `OnTerminal(autoDeliver)`（`service.go:534`）执行。`needs_review` 时 todo 保持 doing。
- 未提交改动守卫判 resume 时 job 仍是 `done`，`linkTodoOutcome` 照常执行（`execute.go:708-715`、`:762`）——现状下续跑还在进行时 todo 就已 done。
- plan 接续能力依赖前项 done：`continue_from` 条件 1、条件 6（`todo_continue.go:177-190`、`:236-244`）；后项基于前项未合并分支（`basePredecessorBranch`，`todo_continue.go:86-127`）。
- `worktree merge --todo-done` 置 done 并推进（`worktree.go:733-760`）；HTTP 体 `todo_done bool`（`worktree_handler.go:56`）；`MergeWorktree` 拒绝远程 runner（`worktree.go:678-680`）。
- `recordDelivery` 在 failed / pending 时也返回 nil；远程交付先记 pending、后台再记最终结果（`branch_delivery.go:182-211`、`:460-466`）。`delivery_skipped` 只在模式非 none 时记（`:86-92`）。job 行有 `CommitsAhead`（`model.go:996`）；清理后保留 `WorktreeBranch`、清空 `WorktreePath`。
- 投影器：claude 的 assistant 事件写成 `{type, tools, text_len}`，不带正文（`ndjsonfilter/projector.go:466-477`），已有 `AllAssistantText` 选项可收集全部 assistant 文本；suga 走 generic 投影器，白名单丢弃 `message_delta`（`agent/registry.go:412-433`），但过滤器在白名单前能看到每一行；suga 用 `retry{restart}` 表示从头重发。ACP runner 的 stdout 有 `--- turn N ---` 分隔（`runner/acp/runner.go:321`）。
- 远程 job 只回传 stdout / stderr（`worker/dispatch.go:591`）。日志路由 `InHandler(permDeny, PermJobReadOthers)`，无 JobCred（`authz_routes.go:290-291`）。MCP 实现在 `internal/mcpserver`，工具名形如 `gofer_get_job` / `gofer_tail_log`。

## 总体方案

### 1. plan 级完成时机（gofer-94yq）

新增 plan 字段 `complete_on: job | merge`（默认 `job`，现状不变）。`plan create / set --complete-on merge`；HTTP / MCP 的 plan 创建与修改同名字段。只对 `merge` 的 plan 改变行为（Q1）：

| job 形态（`complete_on=merge`） | todo | 推进 plan |
|---|---|---|
| 非 worktree job（`WorktreeBranch == ""`，含 `ref:` 临时检出） | done | 是 |
| 本机 worktree job，`CommitsAhead <= 0`（无可交付提交） | done，note「无改动」 | 是 |
| 本机 worktree job，交付 `none` | doing，note「已完成，待合并：分支 B（job J）」 | 否；`worktree merge` 时完成（Q2） |
| 本机 worktree job，交付 `merge` | 交付最终状态为 `merged` 时 done；其余（failed / conflict / pending）doing 并记原因 | 交付 merged 时 |
| 本机 worktree job，交付 `push` / `pr` | doing，note「已推送 / PR 已开，待合并」 | 否（第 4 批跟进 PR） |
| 远程 worker 上的 worktree job | done，note「远程 job，合并不经 gofer」（现状语义） | 是 |

- **两种模式共有的修正**（F10，属于 94yq 的同一症状）：未提交改动守卫判定 resume 时，不完成、不推进，由续跑 job 结束时再进同一判定。续跑**提交失败**时（`execute.go:911-913` → 补记终态分支 `:939-944`），在该分支对 `guardDecision==resume && !autoResumed` 补调一次 `linkTodoOutcome`（不带守卫标记），todo 照常完成或记原因，不会永久停在 doing（N1）。
- 判定函数 `todoCompletion(snap, plan, guard) (complete bool, note string)` 放新文件 `internal/job/todo_completion.go`；`linkTodoOutcome` 的 `StatusDone` 分支只改为调用它。
- **merge 交付的完成点**放进 `recordDelivery` 内部（只认 `res.State == merged`），覆盖自动交付与人工 `job deliver` 重试；pending 不动 todo（F4）。远程 worktree job 在 job 结束时已完成（见表），远程交付最终回写时 todo 已 done，判定幂等、不再改动（N9）。
- **合并即完成**（Q2）：`complete_on=merge` 的 plan，`worktree merge` 一个绑定 todo 的 job 时总是完成该 todo（不看 `todo_done`）；`job` 模式维持 `--todo-done` 显式开关。wire 字段不变（F8）。`markMergeTodoDone` 在 `worktree.go`（已超 1000 行）只改调用行，判定在 `todo_completion.go`（GR104）。
- **丢弃**（F7）：`worktree rm` 一个 merge 模式 plan 的待合并 todo 的 worktree 时，todo note 记「worktree 已丢弃，未合并」，卡片保留（见下）；不自动 skip。
- **接续能力**（F1）：`job` 模式完全不变；`merge` 模式下后项在前项合并后才派发，此时前项分支已进主线，`basePredecessorBranch` 自然不再适用；`continue_from` 若要求接着前项会话，需合并时不带 `--cleanup`（条件 6 要求 worktree 仍在）——在 plan 文档写明，不改接续逻辑。
- **「待你收尾」卡片**（Q4，F9/S4）：在 `internal/today` 生成新卡片类型（与「待你验收」同处），条件全部是库内事实：plan `complete_on=merge`、todo `doing`、todo 绑定的最近 job 为终态 `done`、该 todo 没有正在运行的后续 job。文案按 job 行的结构化字段区分（不读 note，N6）：交付模式与 `delivery_json.state`（`pushed` / `pr_opened` → 已推送待合并；`failed` → 交付失败；`pending` → 不出卡片）、`WorktreePath==""` 且未合并 → 已丢弃（动作给 `plan set-todo done|skipped`），其余 → 待合并。`inbox/waiting.go` 新增类别 `awaiting_merge`，排在「待你验收」之后、「计划卡住」之前；读路径不跑 git。
- schema：plans 表加 `complete_on`（additive 迁移，G057：补升级测试），照现有 `leader` / `notify` 字段的路径接入 `Plan` 结构、`plan set`、HTTP 与 MCP；取值只允许 `job` / `merge`，其他 400；`plan show` 与 Web 计划页显示（N11）。

### 2. 长 job 记录按回合查看（gofer-mmh0、gofer-tykh）

#### 2.1 执行时写附属文件 `turns.jsonl`（Q6）

结果目录新增 `turns.jsonl`，每回合一行：`{turn, role, text, tools:{name:count}, tokens, error, truncated}`。由执行侧写，读时不再猜格式：

| 来源 | 写法 |
|---|---|
| claude stream-json | `ndjsonfilter` 新增可选 `Turns io.Writer` 与回合累积器（新文件）：一次 `-p` 运行即一回合；累积器自行解析 assistant 事件的 content 取文本与 `tool_use` 计数（不改投影器，N8），`result` 时写一行 |
| suga stream-json | 同一累积器：在白名单之前读 `message_delta` 累积正文，`turn_start` 开新回合，`turn_end` 写一行；`retry{restart:true}` 只截掉最近一次 `tool_result` 或 `turn_start` 之后累积的文本（N4）——tykh 由此解决，不依赖 suga 发版 |
| ACP | `runner.Request` 加 `Turns io.Writer`；handler 增加本回合文本缓冲（256KB 上限），工具计数复用 `toolStatus`；在回合结束（prompt 返回 / stop）时写一行，不在开头的 `--- turn N ---` 处写（N3） |
| 其他 cli（codex exec 等） | 不写；读取时回退为整份 stdout 一回合 |

回合方言（`claude` / `suga`）登记在 `builtinNDJSONDef` 的新字段里，未登记方言的 agent 不写 `turns.jsonl`（N5）。行类型 `TurnRecord` 与写入器只在 `ndjsonfilter` 定义一处，ACP runner 与 job 读取都引用它（Gov N3）。

上限：单回合正文 256KB（超出截断、`truncated:true`），文件 8MB。**文件由 job 层打开**（接线在 `internal/job/ndjson.go` `captureNDJSON`，与 `stdout.raw.log` 先例同处；ACP 由 job 层把同一写入器放进 `runner.Request.Turns`），文件名常量加在 `internal/store`；文本字段在 JSON 编码**之前**用该 job 的 secret 值脱敏（与 stdout 外层的 `newSecretMasker` 同一组值，编码前处理避免转义绕过）（Gov N1）。远程 job v1 不回传该文件（非目标）。跨项目：登记跟进 issue，批准后请 suga 会话在其仓库写明 stream-json 事件契约（`message_delta`、`retry` 等字段与稳定性），gofer 按契约维护解析（Gov N4）。

#### 2.2 读取接口

- 业务入口 `job.Service.Transcript(ctx, id, opts) (TranscriptPage, error)`（新文件 `internal/job/transcript.go`）：由 job 行定位结果目录（不接受调用方给的路径；路径拼接与越界符号链接拒绝复用 `internal/store/safejoin.go`，N10），读 `turns.jsonl` 或回退。
- 参数：`outline`（默认）| `turns=A-B` | `head=N` | `tail=N` | `search=<词>`，互斥，违者 400；区间与 N 校验上下界；`search` 为字面子串、长度 ≤ 200，不调外部命令。
- 响应：单次上限 64KB，超出返回 `truncated:true` 与游标 `next={turn, offset}`（`offset` 为回合内字节偏移，可指向同一回合；回退的整份 stdout 同样按偏移分页），调用方带 `cursor` 续取直到取完（N2）。**server 不写任何文件**（S1）。
- CLI：`gofer job transcript <id> [--outline|--turns A-B|--head N|--tail N|--search 词] [--out 文件]`，`--out` 由 CLI 自动翻页并写到客户端本地文件。命令放新文件 `internal/commands/job_transcript.go`（`job.go` 已 4216 行），新 `*Opts` 在 flag 快照登记（G053），经真实分发测试（GR110）。
- HTTP：`GET /v1/jobs/{id}/transcript`，`routeDecls` 写 `InHandler(permDeny, PermJobReadOthers)`、scope `Resource("job")`，与 `/logs/stdout` 一致、不加 JobCred（G056，重新生成权限文档）。
- MCP：`internal/mcpserver` 新工具 `gofer_get_job_transcript`（ReadOnly 类，同参数），供持用户凭据的监督会话使用。
- 提示：多回合 job 的 `job show` / `job logs` 末尾加一行「共 N 回合，`job transcript <id>` 查看各回合」。

### 3. Quick 修复（SR1404：≤3 文件、不改接口）

| issue | 改动 |
|---|---|
| gofer-gqap | 续跑类提示词模板（`auto-resume`、`uncommitted-resume`、`delivery-check-resume`，zh-CN 与 en 各自成句、不混语言，G046）补一句：先核对远端与外部状态（已推送的分支、已开的 PR、已发出的消息），不要重复已完成的外部动作；故障转移前缀 `fallbackPrompt` 同步 |
| gofer-8hlc | `internal/brief/refocus.go:174` 命令改为 `gofer session mail read <id>`，同步 `refocus_test.go`、`prime_refocus_test.go`、golden `internal/brief/testdata/refocus.txt` 与 `skills/gofer-usage/references/tracker.md` |
| gofer-juo0 | 待办接续条件 6（`todo_continue.go:237-240`）改用 `worktreeUsable`，与续跑守卫同口径 |

### 4. 随批小改（有外部可见变化，按本设计验收与文档同步）

| issue | 改动 | 外部变化 |
|---|---|---|
| gofer-ehvd | 提交时的候选筛选函数（`fallback.go:32-49`）返回被跳过的候选，job 行写入后记事件 `job.fallback_skipped{agent, reason}`；`fallback.go:328` 的跳过同样记录；`job show` 显示 | 新事件：登记 `docs/reference/http-api.md` 事件清单、`web/src/utils/eventMeta.ts` 与 i18n |
| gofer-r20b | `POST /v1/jobs` 缺 runner 时先报 400 `runner is required`，再判调用方上限 | 带上限调用方由 403 变 400：写入 http-api 与 CHANGELOG |
| gofer-5x85 | `job rerun`（`RebuildJob`）重提续跑 / 转移 job 时带 `WorktreeFrom`，worktree 失效即按续跑守卫拒绝（409） | rerun 由放行变拒绝：写入 job-advanced.md 与 CHANGELOG |

## 架构

- 业务：`internal/job/todo_completion.go`（§1）、`internal/job/transcript.go`（§2 读取）；入口层（commands / httpapi / mcpserver）只做参数绑定与转发（G021）。
- 执行侧：`internal/runner/ndjsonfilter` 增 `TurnRecord`、`Turns` 写入器与回合累积器（新文件，`projector.go` 不扩）；`internal/runner/acp`（`runner.go` 已 1506 行）handler 回合缓冲放新文件、`runner.go` 只加调用行；`internal/agent/registry.go` 的 `builtinNDJSONDef` 加回合方言字段；文件由 job 层在 `captureNDJSON` 打开并传入。
- 注意力：`internal/today` 新卡片生成、`internal/inbox/waiting.go` 新类别。
- 数据：jobstore plans 表 `complete_on` 迁移。
- 已超 1000 行的文件（`worktree.go`、`execute.go`、`commands/job.go`、`model.go`、`runner/acp/runner.go` 等）只加调用行或字段（GR104）。

## 关键流程

1. `complete_on=merge` 的 plan，T1（本机 worktree，交付 none）→ job 完成 → T1 doing「待合并」→「等我」出现「待你收尾」→ `worktree merge` → T1 done → 派发 T2。
2. 同上但交付 merge：自动交付 merged → T1 done；冲突 → T1 doing，note 写原因。
3. 默认 `complete_on=job` 的 plan：行为与现状相同，只多了「续跑进行中不完成」的修正。
4. `gofer job transcript J` → 回合概要 → `--turns 2-3` 取全文；`--out notes.md` 本地导出。

## 安全、数据、运维与回滚

- 行为变化：仅 `complete_on=merge` 的 plan 改变完成时机；所有 plan 在续跑进行中不再提前完成 todo；r20b 状态码、5x85 rerun 拒绝、新事件 `job.fallback_skipped`。
- 转录：只读接口不写 server 文件；结果目录由 job 行推出、拒绝越界符号链接；`search` 字面匹配且限长；单次响应限长；附属文件与 stdout 同权限、同脱敏；日志不记搜索词与正文。
- 数据：plans 表 additive 列；结果目录多一个有上限的文件。
- 回滚：回退合并提交；`complete_on` 列保留无害（旧代码忽略）。

## 决策

| 编号 | 问题 | 选项 | 推荐 |
|---|---|---|---|
| Q1 | 完成时机如何改 | A：plan 级 `complete_on: job | merge`，默认 `job`（现状），监督式 plan 设 `merge`；B：全局改为合并后完成（会破坏 `continue_from` 与分支基线链） | **A** |
| Q2 | merge 模式下 `worktree merge` 是否总完成所属 todo | A：总完成（不看 `todo_done`）；B：仍需显式 `--todo-done` | **A**。merge 模式下合并就是完成条件 |
| Q3 | merge 模式下交付 `push` / `pr` | A：待合并，人工或第 4 批收尾；B：推送成功即完成 | **A** |
| Q4 | 待合并是否进「等我」 | A：进，新类「待你收尾」；B：只写 todo note | **A** |
| Q5 | 远程 worker job | A：merge 模式下仍在 job 结束时完成并注明；B：远程 job 一律待人工收尾 | **A**。gofer 不能合并远程 worktree |
| Q6 | 转录数据来源 | A：执行时写 `turns.jsonl`（claude / suga / ACP），其他回退；B：读时解析各家原始会话文件（只本机可用、格式随上游变） | **A** |

## 待确认事项

- Q1–Q6。

## 验收

- §1 单测：表中每行一例（merge 交付 merged / failed / pending、远程交付最终回写、`CommitsAhead=0`、远程 job、accept 后同一判定）；`job` 模式行为不变（含 `continue_from` 与分支基线回归）；续跑进行中不完成（两种模式）、续跑提交失败后 todo 完成或记原因；卡片按交付状态分类（pending 不出卡片）；`complete_on` 非法值 400；merge 模式合并即完成；`worktree rm` 后 note 与卡片；卡片出现、后续 job 运行中不出现、合并后消失；plans 迁移升级测试（G057）。
- §2 单测：claude / suga（含 `retry{restart}` 只截最近一段）/ ACP（回合结束写、正文非空）三种写入、未登记方言不写、上限截断；注入 secret 后 `turns.jsonl` 只出现掩码；读取参数互斥与校验 400、大于 64KB 的回合经游标分页取完、回退分页、符号链接越界拒绝；HTTP 权限与他人 job 拒绝；CLI 真实分发与 `--out`；MCP 工具。
- §3 / §4：各自 focused test；ehvd 事件出现在 `job show`。
- 真实验收：`complete_on=merge` 的 plan 两个 worktree todo 串联，第一个完成后第二个不派发，`worktree merge` 后才派发；一个两回合 suga job 与一个 ACP job 的 `job transcript --outline` / `--turns` 能看到每回合正文。
- 反向验证：去掉 §1 判定 →「交付 none 不推进」用例失败；去掉 suga 回合累积 → 两回合用例只剩一回合。
- 文档同步（G045 / G046）：`skills/gofer-usage/references/plans-workflows.md`（`complete_on`、待你收尾）、`job-advanced.md`（`job transcript`、rerun 拒绝）、`tracker.md`（8hlc）、`web-console.md`（新卡片）、`docs/reference/http-api.md`（transcript 接口与游标、plan `complete_on`、waiting / Inbox 小节的新类别 `awaiting_merge` 与 today 新卡片 kind、`GET /v1/inbox/rules` 的默认栏、worktree merge 段写明 merge 模式下 `todo_done` 不起作用（`--help` 同步）、rebuild 路由 409（5x85）、事件 `job.fallback_skipped`、r20b）、`docs/reference/permissions.md`（生成）、`CHANGELOG.md`。

## 结论与人工计划 Gate

只在 `complete_on=merge` 的 plan 把 todo 完成点移到交付落地，并补「待你收尾」可见性；所有 plan 修正续跑进行中提前完成的问题。执行时按回合写记录，统一经 `job transcript` 分页读取，多回合 job 不再丢前几轮回复。等用户答复 Q1–Q6 并批准后写计划。
