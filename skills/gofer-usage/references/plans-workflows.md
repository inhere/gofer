# plan / todo 链与 workflow

> 多步骤长任务怎么组织：plan + todo 跟踪与自动派发、依赖链、方案转 todo（plan import）、决策点问人、workflow 编排与多 agent 择优。完整 flag 以 `gofer plan <子命令> --help` / `gofer workflow --help` 为准。

## 目录

- [plan 与 workflow 的分工](#plan-与-workflow-的分工)
- [plan 基本命令](#plan-基本命令)
- [todo 的派发字段](#todo-的派发字段)
- [范式一：todo 指派即派发](#范式一todo-指派即派发)
- [范式二：todo 依赖链 + plan run](#范式二todo-依赖链--plan-run)
- [方案规则与 plan import](#方案规则与-plan-import)
- [决策点问人（gofer_ask_human）](#决策点问人gofer_ask_human)
- [主 Agent 会话与 job 来源会话](#主-agent-会话与-job-来源会话)
- [workflow：有依赖的多步编排](#workflow有依赖的多步编排)
- [verify / release 起步 workflow](#verify--release-起步-workflow)
- [多 agent 对比与择优](#多-agent-对比与择优)

## plan 与 workflow 的分工

- **plan** = 组织与跟踪：把相关 job 和 todo 清单归在一起；todo 指派了 agent 就能自动派发、按依赖一环接一环。适合「一项开发的多个步骤」。
- **workflow** = 执行编排：server 按 step 链跑（依赖 / 扇出 / 汇合），适合「先 build → 再 test → 再 deploy」或多 agent 对比。
- 单发命令用 `job`，别套 workflow。

## plan 基本命令

```bash
gofer plan create --title "<标题>" [--desc "目标 / 范围 / 验收"] [--project <p>] [--tags x,y] \
    [--supervisor-session-id <sid> | --no-supervisor]
gofer plan list / show <id> / archive <id>
gofer plan add-todo <plan> "T1 …" [--note "…"] [派发字段...]     # 建出来是 pending（add-todo 没有 --status）
gofer plan set-todo <todo> --status pending|ready|doing|done|skipped [--note "结论"] [--append-note "追加一行"]
gofer plan set-status <plan> done
gofer plan attach <plan> <job-id>                     # 把已有 job 挂进来（先 plan 后 job）
gofer plan handoff <plan> [--set "下一步…" | -f handoff.md] [--history] [--version N]   # 交接说明（版本化）
gofer plan brief <plan> [--json] [--max-lines N]      # 接手包
gofer plan comment <plan> "…" [--todo <todo>] / comments <plan>
```

- `plan show` 在每个 todo 下列出挂接的 job（状态 / agent / 用时）、assignee 与派发错误，头部有用量汇总。
- `set-todo --status doing` 记开始时间，`done` / `skipped` 记完结时间；裸调用 = done，`--undone` = pending；`--note` 写**结果 / 验收一句话**，过程在 job logs。
- todo 状态：`pending`（不会自动派发）→ `ready`（可派发）→ `doing` → `done` / `skipped`。server 重启**不补派** ready 的项（派发只发生在写入时）。
- plan 状态：`open` / `blocked`（链停在某一项等人处理，非终态）/ `done`（全部 done|skipped 自动置）/ `archived`；另有暂停开关 `plan pause` / `plan resume`。
- web 的 Plans 页有**计划总览**（每行一个计划，带分段进度条与卡点）与**计划详情**；详情页的待办页签就是**计划看板**（五列：把卡片拖到 `ready` 即派发、拖到 `done` / `skipped` 即人工标记；`doing` / `needs_review` 由服务端驱动、不可拖入），任务 / 决策 / 交接说明 / 评论 / 事件是同一个详情页的其他页签（`?tab=`），手机也能看实时进度。

## todo 的派发字段

`add-todo` / `set-todo` 共用：

```text
--assign <agent>        谁跑这一项；ready + 有 assignee = 立刻出 job
--project <p>           覆盖 plan 的 project
--template <名> --var k=v   用任务书模板渲染这一项的 prompt
--verify '<命令>'       agent 正常结束后的验证命令（需项目 allow_exec）
--review                完成后停在 needs_review 等人验收
--runner <key>          执行 runner（缺省 server 本机）
--cwd <相对路径>        工作目录（缺省项目根）
--timeout <秒>          job 超时
--model <id> / --max-tokens / --max-cost / --max-turns   硬预算；--agent-budget-tokens / --agent-budget-cost-usd 软预算（交给 agent 自己收尾，需 agent 配 agent_budget，见 job-advanced.md）
--acceptance "<列表>"   验收标准（"" 清空）；add-todo 还有 --acceptance-from-issue <issue-id>
--scope 'glob,…'        声明改动范围（"" 清空）
--prime-keywords '子系统,…'  这一项的任务关键词，给派出的 job 选材 tracker-prime（"" 清空；缺省读 $GOFER_PRIME_KEYWORDS）
--after <ids|prev>      这一项等哪些 todo（prev = 上一条）
--continue <id|prev>    声明这一项接着前序项的 agent 会话做（prev 仅 add-todo，plan 为空报错；同 plan、不能指自己、不能成环；自动加入 after）
--no-continue           set-todo：取消接续（清空 continue_from）
--auto / --no-auto      允许 / 禁止链自动启动这一项（缺省允许）
--cmd '<argv>'          --assign exec 时这一项跑的命令（exec 项必须有）
```

## 范式一：todo 指派即派发

一边规划、一边把每一步指派给 agent：`status=ready` 且有 `assignee` 的 todo **立刻变成 job**，跑完由 job 终态自动写回状态与交付的提交。

```bash
gofer plan create --title "xxx 改造实施" --project <p>
gofer plan add-todo <plan> "步骤1: 数据模型迁移" --assign codex --note "先跑迁移，再补索引"
gofer plan set-todo <todo> --status ready                     # 立刻出 job → todo 转 doing
gofer plan set-todo <todo> --assign codex --status ready --template <模板> \
    --var tasks="只做索引重建" --verify '<测试命令>' --review --cwd . --timeout 1800
gofer plan dispatch <todo>                                    # 兜底：不看状态，只要有 assignee 且没有活跃 job
gofer plan set-todo <todo> --status skipped --note "原因"      # 某步不做
```

- 触发点只有写入：`--status ready`、设置 / 修改 assignee、`plan dispatch`（MCP `gofer_update_todo` / `gofer_dispatch_todo` 同）。
- 无模板时的默认 prompt = plan 标题与描述 + 「## 本任务」+ todo 标题与备注；有模板时一份模板可服务整个 plan。
- 再跑 / 换人：`doing` / `done` 的项再置 `ready` = 重跑（前一个 job 须已终态）；改 `--assign` 再置 ready = 换人。
- job 失败不自动重派：todo 保持 `doing` 并在备注写原因。派发本身失败（无项目 / agent 不允许 / 项目不在 worker 上）→ todo 保持 `ready`，原因显示为派发错误（`plan show` 与 web 红字）。
- 派出去的就是普通 job：`gofer job list --plan <id>` 能看到。

## 范式二：todo 依赖链 + plan run

把步骤建成有依赖的链，`plan run` 一次开工，之后每一环由前一环的终态自动启动，人只在失败 / 验收处介入：

```bash
gofer plan create --title "xxx 改造实施" --project <p>
gofer plan add-todo <plan> "步骤1: 数据模型迁移" --assign codex
gofer plan add-todo <plan> "步骤2: 服务层改造"   --after prev --assign codex --verify '<单元测试命令>'
gofer plan add-todo <plan> "步骤3: 文档同步"     --after prev --assign codex
gofer plan add-todo <plan> "步骤4: 全量复核"     --after prev --assign exec --cmd '<构建 + 全量测试命令>' --cwd .
gofer plan run <plan>              # 把「依赖已满足 + 有 assignee」的项置 ready（这里只有步骤1）
gofer plan show <plan>             # 盯进度（web Plans 页也可）
# 中途失败停在那一项（plan blocked，并发通知）：
gofer plan set-todo <todo> --status ready      # 重派（可先改 --assign 换人）
gofer plan set-todo <todo> --status skipped    # 或跳过，让后续继续
gofer plan resume <plan>                       # 解除暂停 / 阻塞并推进一次
gofer plan pause <plan>                        # 临时挂住整条链（在跑的 job 不受影响）
```

`--after prev` 指「该 plan 里最后一项」；plan 还没有任何项时视为无依赖，照常添加并在输出里注明（可统一给每条都写 `--after prev`）。

- 根节点（没有 `after`）只有 `plan run` 或人工置 ready 才会起，加 todo 不会自己跑。
- 某项 done（含验收 accept）或 skipped → 依赖全满足、有 assignee、允许自动的后续项置 ready 并立即派发。没人指派的到点项只记事件，补 `--assign` 后再 `plan run`。
- 链上某项 job 失败 / 超时 / 取消 / 被拒（且没被自动续投接手）→ plan `blocked`，事件 `plan.blocked` 在默认通知集（IM 带链接）。`needs_review` 不算失败：后续等人 accept（继续）或 reject（停链）。
- 没有任何 `after` 的平铺清单，失败不会 block。`plan run` 不理会 `--no-auto`（那是给自动推进用的）。
- 链末放一条 `--assign exec --cmd '<构建/测试命令>'` 的复核项，「改完自动验证」也在链上。
- 可订阅事件：`plan.todo_dispatched`、`plan.todo_dispatch_failed`、`job.continued`（todo 的 job 接着前序项的会话继续，挂在新 job 上，带 `pred_todo` / `pred_job` / `chain`——已连续接续的次数）、`job.continue_fallback`（todo 设了 `continue_from` 但本次派发没能接续前序会话，改开新会话，带 `todo_id` / `reason`）、`plan.todo_advanced`、`plan.todo_unassigned`、`plan.blocked`、`plan.completed`、`plan.advance_paused`。MCP 对应 `gofer_add_todo` / `gofer_update_todo`（`after` / `auto` / `cmd`）与 `gofer_plan_run`。

### 接着上一项的会话做

**何时用**：串行的、改同一块代码的相关 todo（例如先做底层、再做上层、再补测试），后一项需要前一项刚读过的代码和上下文。互不相关的项、需要独立视角的复核项、并行的项不要用——新会话更干净也更便宜。

**效果与取舍**（实测：同一组 4 项实现任务，新会话 vs 接续，各 3 次）：接续让整条链更快（耗时约 −20%）、重复探索更少（工具调用约 −35%、新输入约 −35%），产出质量没有可见差别；但每轮都要带上更长的会话上下文，缓存读取约 +30%，按常见的缓存折扣计算**总成本与新会话持平甚至略高**——接续是「省时间、少折腾」，不是「省钱」。轻量的收尾项（只改文档、改个说明）不要接续：它会带着整条链的上下文跑，缓存读取可达新会话的 3 倍。

**怎么用**：

- 新建：`gofer plan add-todo … --continue prev`（接着 plan 里最后一项）或 `--continue <todo-id>`；`prev` 在 plan 为空时报错。
- 修改：`gofer plan set-todo <todo> --continue <todo-id>`；取消：`--no-continue`。
- web：plan 页新建 / 编辑 todo 表单里的「接着上一项的会话做」开关，再选前序项；todo 卡片与列表显示「接续自 …」或「未接续：原因」。
- MCP：`gofer_add_todo` / `gofer_update_todo` 的 `continue_from`（`""` 清空）。
- 前序项必须在同一个 plan 内、不能指自己、不能成环；设置后前序项自动加入 `after`。`plan import` 暂不支持 `continue_from`。

**接续条件**（派发那一刻逐条检查，全部满足才接续）：

- 前序项已完成（需要验收的已被接受），且它的 job 留下了可续的会话。
- 本项与前序用同一会话族的 agent（exec 不行），且配置允许续接（ACP agent 未关闭会话加载；cli agent 有续接模板；本项指定的 model / 预算在该 agent 上可用）。
- 两项用同一个 runner，且是本机 runner（暂不支持 worker）。
- 本项没有另设工作目录，或与前序一致；前序用了 worktree 时，该目录还在。
- 前序不需要自动交付（有自动交付的项不接续）。
- 本项与前序的只读设置一致。
- 前序的会话没有被另一项占用（一个前序只能被一个后继接续，不支持并行后继）。
- 连续接续次数未达上限：项目配置 `continue_max_chain`，默认 3，0 = 不限，最大 10。人工 resume、自动续投、返工不计入链长。

**不满足时**：不报错，照常在新会话里派发本项，并在 todo 上写 `continue_note`（「未接续：原因」，`plan show` 与 web 都能看到），同时记 `job.continue_fallback` 事件。前序留有未合并的 worktree 分支时，本项的新 worktree 基于该分支，`continue_note` 会说明。

**续接的 job 拿到什么**：prompt 是一行承接说明加本项自己的任务书、验收标准、交付约定和时限行；规则、prime、工具段不再重复注入（前序会话里已有）。cli agent 走会话恢复命令，ACP agent 一次性加载会话、跑完这一轮即结束。续接 job 的结果、待验收、失败与自动续投都和普通 job 一样挂在本项上；agent 侧拒绝续接（如会话已被清理）时 job 失败并带原因，可 `--no-continue` 后重新派发。

**改动口径**：提交列表、diff（含 web 会话「改动」视图运行中的实时显示）、改动范围（`scope`）检查从前序项的结束点算起，只统计本项自己的改动；前序留下的未提交文件，本项没碰过的不计入。

**事件**：`job.continued` / `job.continue_fallback`，字段见上面「可订阅事件」。

**局限**：同一个前序项不能有并行的后继；只用于 plan 里的 todo，不适用于 worker 上的任务；一个会话跨多项时上下文会变长，所以有链长上限。

## 方案规则与 plan import

写实施方案（内置 `plan-implement` 的 planner 已自动带上，自己写方案时照做）遵循以下规则（原文）：

```
方案规则：
1. 步骤按依赖自底向上（存储 / 模型 → 协议 / 接口 → 业务 → 调用方 / 前端），任何步骤不得使用后续步骤才创建的东西；
2. 优先垂直切片，每片完成后可编译、可验证；
3. 单步预计改动 > 5 个文件、跨 2 个以上子系统、或标题含「并且 / 同时」，拆开；
4. 每 2~3 步一个检查点（可执行的验证命令）；
5. 对跨模块、不可逆、并发 / 幂等 / 不变量类关键决策做一次「假设作者过度自信」自审，写出最可能错在哪；
6. 不确定、需要人拍板的点收进「待确认问题」，不进入实现。
```

方案末尾附一个 ```` ```gofer-todos ```` 围栏块（YAML 列表，一项一步）：`title`（必填）、`after`（依赖的前序步骤：标题或从 1 起的序号，可写列表；**省略 = 依赖上一步**，`[]` = 无依赖）、`acceptance`（文本或列表）、`scope`（路径 glob 列表或逗号分隔）、`check`（检查点命令）。未知字段报错。

```bash
gofer plan import <plan> -f plan.md --assign codex --dry-run    # 只打印要建的 todo（不连 server）
gofer plan import <plan> --from-job <规划 job-id> --assign codex # 读该 job 汇报里最后一个 gofer-todos 块
gofer plan run <plan>
```

- 每步建一个 todo（带 acceptance / scope；`--assign` 不给 = 先不指派），`after` 解析成刚建出的 todo id。
- 有 `check` 的步骤后面多建一个 exec 复核项「检查点：<步骤标题>」，后续引用该步骤的项等的是这个检查点，检查失败即停链。
- `after` 只能指向前序步骤；`--assign exec` 被拒（检查点自动是 exec）。中途建失败会报「已建 N 项」后停止，不回滚。

## 决策点问人（gofer_ask_human）

大计划跑到需要人拍板的分叉时，agent 用 MCP 工具 `gofer_ask_human` **阻塞提问**；人在 web「待我决策」或 plan 详情页作答，答案从工具返回值流回，会话原地继续：

```text
# agent 会话里（MCP 工具，阻塞等待）：
gofer_ask_human{plan_id, title, question, options: ["方案A","方案B"], timeout_sec: 1800}
# → {state:"answered", answer, answered_by} 或 {state:"expired"}

# 人这一侧（CLI，不阻塞；web 更常用）：
gofer plan ask --plan <id> --title "..." --question "..." [--option A --option B] [--timeout 30m]
gofer plan decisions --state OPEN
gofer plan answer <decision-id> --answer "方案A"
```

- 超时兜底在 agent 侧：收到 `expired` 后按预案继续（执行推荐项），或把该步 todo 置 `skipped` 并说明——不无限阻塞、不原地重问。
- `timeout_sec` 缺省 1800（范围 2 秒到 24 小时）；按宿主客户端的工具调用超时上限来设，宿主先杀调用时决策留 OPEN、到期自动过期。
- 一次只问一个决策点是范式建议，不是连接限制。

## 主 Agent 会话与 job 来源会话

- `plan create` 默认把计划绑定到**当前所在的 agent 会话**（主 Agent）：依次取 `SUGA_SESSION_ID`、`GOFER_SESSION_ID`、`CLAUDE_CODE_SESSION_ID`、`CODEX_THREAD_ID` / `CODEX_SESSION_ID`；须是已登记、未结束、属于当前 caller（指定了 `--project` 时项目一致）的会话，否则建成未绑定并打印绑定命令。其它 agent 需自己 `export GOFER_SESSION_ID=<sid>` 或显式传参。`--no-supervisor` 不绑定；`plan set <plan> --supervisor-session-id <sid>` / `--clear-supervisor-session` 改绑。
- 绑定后：自动派发的 job 与主 Agent 会话同项目、同 runner、同目录、不开 worktree 时以该会话为来源，并登记「job 结束通知主 Agent 会话」；否则照常派发，仍登记通知。
- `plan create --notify` / `plan set <plan> --notify on|off`（缺省 `off`）把这个通知从「等会话自己停下」改成**主动推送**：该 plan 派发的每个 job 结束时，server 直接用传话人给主 Agent 会话发一条 `[gofer · job 完成] …` 系统通知（同 `gofer job run --notify`；默认关是因为每条通知要起一次传话人进程）。
  - 没绑定主 Agent 会话时无人可通知：job 照常派发、不通知任何人，`plan show` / `plan create` 会打一行提示（绑一个即可）。
  - supervisor 会话后来被清理掉也一样：派发照常进行（源会话声明一并归零），该 job 记一条 `job.notify_dropped` 事件说明通知被丢弃，整条链不会因为一条通知卡住。
  - 通知送达会消费该 job 的 watch（Stop hook / 补发不再重发），送达失败保留 watch 由补发兜底；同一个 job 只通知一次。
- 普通 job 可显式 `--source-session-id <sid>`：须由当前认证 caller 拥有，且项目、runner、cwd 与登记一致，否则提交前拒绝。它与 agent 自己的续接目标 `session_id` 不同。
- 主 Agent 会话（含子 agent）的 token 用量同时计入它监督的进行中 plan（同时监督多个时只记到最近有动静的那个），`plan show` 多一行 `session:` 用量；绑定之前的用量不回填。
- web 的 plan 详情页有「主 agent 会话」面板：看状态、改绑、「发给主 agent」（带快捷语「请写交接说明并更新 plan handoff」）。

## workflow：有依赖的多步编排

```bash
gofer workflow run <file.yaml> [-p <project>] [-w]     # 从 yaml / json 提交；-w 轮询到终态并打印每步
gofer workflow run --template <name> --var k=v [-w]   # 从流程模板提交（-p 也可指模板所属项目）
gofer workflow template ls|show <name> [-p <project>] # 看模板（含来源）与 vars
gofer workflow template show <name> --source          # 看模板自己的 YAML 与文件路径
gofer workflow list / show <id> / events <id> / cancel <id>
gofer workflow rerun <id> --failed | --from <step> [-w] # 重跑失败的部分 / 从某步起重跑（新 workflow）
gofer workflow export <id>                            # 导出 spec（去密钥），可再 run
```

- 文件格式：`.json` 按 json，其余按 yaml；顶层 `title` + `project` + `steps: [...]`。先 `export` 一个跑通的当模板最快。
- 顶层 `project:` 是这个 workflow 的默认项目：步骤不写 `project_key` 就在它里面跑。`wf run -p <project>` 覆盖文件里的 `project`（用 `--template` 时同时作为模板查找的项目与模板 `project` 变量的默认值）。
- step 可声明依赖、`fan_out` 扇出、汇合；步骤可带 `worktree`、`worktree_base`、`ref`、`env`、`template`、`vars`、`read_only`、`verify`、`review`。runner 留空 = 项目默认（项目允许本机就用本机；只列一个就用它；列多个且不含本机则必须显式指定）。
- 引用前序结果：`${steps.<name>.<field>}`，`.fK.` 选第 K 个 fan，`.<fan 名>.` 按 fan 的 `name` 选，`.all.` 选所有成功 fan，`.picked.` 选被择优的 fan。字段：`result_dir`、`result`、`stdout`、`exit_code`、`status`、`job_id`、`worktree_branch`、`diff`（`changes.diff` 的路径）、`diff_summary`。`.all.` 聚合超过 32KB 时自动写成文件、引用处换成文件路径。大段或不可信文本（如评审输出）请以**文件路径**传给下一步的 agent，不要插值进 exec 命令。
- 流程模板位置：项目 `.gofer/workflows/<name>.{yaml,yml,json}` → `<config-dir>/workflows/` → 内置。模板的 `vars:`（`default` / `required` / `desc`）以 `${vars.x}` 或 `{{x}}` 在步骤字段里替换；未声明的 `--var`、残留占位、含特殊字符的 agent / runner / project 值都被拒绝。模板默认 agent 不被项目允许时报 400 并列出可用 agent，用 `--var <变量>=<agent>` 覆盖。

### 模板的查看与保存

```bash
gofer wf template show <name> --source [-p <project>]                # 源文件 + 来源（内置 / 全局 / 项目）
gofer wf template save <name> -f <file> [--global | --project <p>]   # 新建或改模板（先校验）
gofer wf template rm <name> [--global | --project <p>]
gofer wf save-as-template <wf-id> <name> [--global | --project <p>]  # 把一个跑过的 workflow 存成模板
```

- `--global`（默认）写 `<config-dir>/workflows/<name>.yaml`；`--project <p>` 写该项目工作区里的 `.gofer/workflows/<name>.yaml`（gofer 进程视角的路径），两者互斥。名字只能用 `[A-Za-z0-9._-]`。
- 保存前先校验：YAML 能解析、有步骤、变量按默认值渲染后不留 `${vars.x}`、扇出 / `on_failure` 等步骤字段过得了提交时的检查；不过就报错且**不会写文件**。
- **覆盖内置模板**：项目或全局里存一个同名文件就盖住内置模板（例：`.gofer/workflows/verify.yaml` 盖住内置 verify），命令会提示「将覆盖内置模板」；内置本身永远不被改写，不带 scope（或 `scope=builtin`）直接写内置层会被拒（409）。`show --source` 会列出被盖住的来源（`shadows`）。
- 删除只删**这一层**的文件，之后回落到下一层 / 内置（命令会告诉你回落到哪层）。
- 项目模板存下去是**未提交改动**，命令会回显建议的 `git add … && git commit …`；运行读的是工作区当前内容，所以未提交也立即生效，但提交后仓库才留得住。
- 需要 `config.write`；只能写自己项目范围内的模板（全局模板要不受限的调用方），每次写入记审计。
- web 的新建 workflow 页可直接看模板源文件并编辑保存（见 [web-console.md](web-console.md)）。

## 结果表、重跑与 `if: always`

- **看结果**：`gofer wf show <id>` 把每个步骤 / 每一路 fan 打成一张表（STEP / ATT / FAN / NAME / RUNNER / STATUS / EXIT / STARTED / TIME / JOB），NAME 列在扇出时显示那一路 fan 自己声明的 `name`（如 `linux-race` / `web`），没命名才回落步骤名；JOB 是那一行的 job id，直接拿去 `gofer job show|logs`（失败摘录标题里也带）。扇出步每一路的 job 标题是「步骤名 · fan 名」，job 列表里能分清是哪一路。失败行下面是失败摘录：优先收 go test 这类**强信号**的失败块——`--- FAIL:` 头连同其后几行细节、`FAIL\t<pkg>` 汇总、`panic:`、`WARNING: DATA RACE` 及其后几行上下文；只有整段日志都找不到强信号时，才回落到 `ERROR:` / `error:` / `✗` 这类弱信号行（所以「校验报错测试」打印的一大片预期 `ERROR:` 不会再淹没真失败）。摘录去重、保持顺序、最多 20 行；内置步骤这类没有日志行的取任务记录的错误。HTTP 详情接口的 `steps[]` 同样带 `fan_name`、`runner`、`exit_code`、`duration_sec`、`failure_excerpt`。失败即停也不影响已结束步骤的信息：失败的 workflow 一样完整显示。
- **工作目录与开始时间**：`gofer wf show <id>` 表里的 STARTED 是那一步 / 那一路真正开始的时刻（未起为 `-`），表头上方另有 `started_at` / `finished_at` / `duration`（工作流级，终态才有后两个）；加 `-v` 会在表下逐行列出实际执行目录（`cwd`），`ref:` 步骤同时标出它检出的提交（临时检出目录）。`gofer wf list` 的列是 ID / STATUS / STEP / TITLE / STARTED / TIME（STARTED = 提交即开始，TIME = 终态的 created_at→updated_at）。
- **从 job 反查工作流**：`gofer job show <id>` 对 step-job 会打一行 `workflow:`（工作流 id + 第几步 + 重试次数 + 第几路 fan 与 fan 名）；`gofer job list --wf <wf-id>` 列出该工作流的全部 step-job（等价于 `GET /v1/jobs?workflow_id=`），排查一个 workflow 卡在哪一步时不用自己去捞 job。
- **只重跑需要重跑的部分**：`gofer wf rerun <id> --failed` 只保留失败的步骤（失败的扇出步骤只保留失败的那几路）；`--from <序号或步骤名>` 从那步起保留后续全部。只剩**一路** fan 的扇出步会去掉 `join`，并把这一路 fan 自己的 `cmd` / `env` / `cwd` / `timeout_sec` / `agent` / `runner` 合进该步（重跑跑的就是失败的那一路，不是步骤自己的基础命令）；剩两路及以上时 `join` 与 fan 列表原样保留。它是**提交一条新的 workflow**，原 workflow 一行不改（状态、指针、任务都留着），新 workflow 记 `rerun_of` = 原 id。变量与原 `ref` 解析出的 sha 保持原值，所以重跑检查的是同一个提交。仍在运行的 workflow 不能重跑（先 `cancel`）；裁剪后某步的 `${steps.N…}` 引用指向被去掉的步骤会提交即报错。`-w` 轮询到终态。
- **`if: always`**：步骤写 `if: always` 就在前面失败后仍然执行（失败即停时跳到这些步骤按顺序跑完，其余步骤跳过），但不改变结论——前面失败了整体还是失败，原因保留（后续又失败时附加在后面）。典型用法：release 失败后的清理 / 通知。
- **多行脚本 `run: |`**：步骤或 fan 可以用 `run:` 写一段脚本代替 `cmd:`（两者互斥）。`shell:` 选解释器：`bash`（默认，`bash -eo pipefail -c <脚本>`）/ `sh`（`sh -ec`）/ `pwsh`（`pwsh -NoProfile -Command`）/ `python`（`python -c`）；没有 `shell: cmd`（Windows 用 `pwsh`，或直接写 `cmd:`）。fan 不写 `run` / `cmd` 就沿用步骤的，写了则覆盖。旧写法 `run: always`（当条件用）仍然当 `if: always` 处理并给一条弃用警告，请改成 `if: always`（v0.185 起不再兼容）。

## 内置步骤：`uses` + `with`

一个步骤可以不是「跑某条命令」，而是让 gofer 自己做一个动作：

```yaml
steps:
  - name: push
    uses: gofer/push
    with: {refs: [main, "v${vars.version}"]}
    on_failure: retry             # 推送失败（网络 / 钩子）重试，每次尝试是独立 job
    retry: {max_attempts: 10, backoff_sec: [300]}
  - name: ci
    uses: gofer/wait-ci
    with: {ref: "v${vars.version}", required: [Unit-Tests], code_scanning: true, timeout_min: 40}
  - name: assets
    uses: gofer/wait-assets
    with: {tag: "v${vars.version}", assets: ["*-linux-amd64.tar.gz"]}
```

内置步骤的规则：

- `uses` 只接受 `gofer/<内置名>`，没有版本号（随 gofer 二进制）；名字与 `with` 的参数**严格校验**，写错一个键直接提交失败，不会静默忽略。
- 参数走 `with:`，可用 `vars` / `${steps…}` 插值。`uses` 步骤不能同时写 `cmd` / `prompt` / `fan`。
- 执行位置看步骤的 `runner:`（默认项目默认 runner）；在 worker 上执行要求该 worker 协议 ≥ v26（旧 worker 提交时即被拒，不会跑到一半才失败）。内置步骤是一次内部 exec job（标签 `workflow-step`），日志 / 取消 / 超时 / `on_failure: retry` 与普通步骤一样。

| 步骤 | `with` | 说明 |
|---|---|---|
| `gofer/push` | `refs`（必填）、`remote`（默认 origin）、`proxy` | 本地分支推 `refs/heads/<名>`、本地 tag 推 `refs/tags/<名>`；**永不强推**；推完用 `ls-remote` 核对每个 ref 的远端 sha 与本地一致，不一致即失败。 |
| `gofer/wait-ci` | `ref`（必填）、`required`、`code_scanning`、`timeout_min`（默认 40）、`interval_sec`（默认 20）、`remote` | 仓库从远端 URL 推导（目前只支持 GitHub，gitlab / gitea 报「暂不支持」）；等到该 ref 的所有 check 结束，`required` 名单里任一失败 / 没跑即失败；`code_scanning: true` 时再查该 ref 上**开放的**告警（GitHub 的 ref 过滤，不区分新旧基线），有则失败。 |
| `gofer/wait-assets` | `tag`（必填）、`assets`（必填，glob 列表）、`timeout_min`（默认 30）、`interval_sec`（默认 20）、`remote` | 等该 tag 的 Release 上出现全部产物（按文件名 glob 匹配）；超时失败会列出还缺哪些、Release 上已有哪些。 |

- 重试、失败策略、步骤顺序仍走 workflow 自己的规则（`on_failure: fail | continue | retry`）；一次推送失败重跑是**新的 job**，不受单 job 时限累积影响。
- **信任模型**：仓库里的 workflow 决定跑什么，所以「能改 `release.yaml` 的人就能改发版命令」。推送分支 / tag 需要两把锁：项目 `delivery.allow_push: true` **且**提交方持有 `repo.push` 权限（否则 403）；其余 exec 步骤仍受项目 `allow_exec` 与调用方的运行上限约束。
- `gofer tool step <name>` 是这个内部 job 的命令（隐藏，参数为 `with` 的 JSON 走 stdin）；手工排查可直接跑：`echo '{"refs":["main"]}' | gofer tool step push`。

## verify / release 起步 workflow

「每次提交都跑一遍检查」和「打 tag 发版」这两件事有现成的起步文件，不用从空白 yaml 写：

```bash
gofer workflow init verify  [--dir <仓库目录>] [--force]    # 生成 .gofer/workflows/verify.yaml
gofer workflow init release [--dir <仓库目录>] [--force]    # 生成 .gofer/workflows/release.yaml
```

- 生成的文件按仓库内容**预填**：有 `go.mod` → `go test ./...` 与 `go vet ./...` 两路；有 `package.json` → 其中存在的 `test` / `build` / `lint` 各一路（按 lock 文件用 `npm` / `pnpm` / `yarn run`）；`package.json` 在子目录（如 `web/`）时那几路带 `cwd`；两者都有就合在**同一个步骤**的 fan 里（并行，`join: all`）；都没有则留一路占位命令并注明怎么改。
- release 预填 `push`（当前默认分支 + `v${vars.version}`）→ `wait-ci`（有 `.github/workflows` 就取其各 workflow 的 `name:` 作 `required`）→ `wait-assets`（注释示例，按需启用）。
- 文件里每一段都有注释说明改法：加 `-race`、`GOOS: darwin` 的 vet、指定某台 worker 跑、子目录 `cwd`、`env` 不要放秘密等。
- 已存在且不带 `--force` 时**拒绝**并说明，不会覆盖你改过的文件。
- 文件写在仓库里，**提交到仓库后 server 才读得到**（server 从项目工作区的 `.gofer/workflows` 读模板）；未提交的改动也立即生效，但没有提交就留不住。
- 想替换内置模板就存一个同名项目文件：`.gofer/workflows/verify.yaml` 盖住内置的 `verify`（见上文「模板的查看与保存」）。

对应的内置模板（项目里没有同名文件时直接可用）：

| 模板 | var | 做什么 |
|---|---|---|
| `verify` | `project`、`commit`、`cmd`（必填）、`runner` | 在 `commit` 的临时检出里跑一条脚本（`run: <cmd>`，`bash -eo pipefail -c`，例如 `go test ./...`） |
| `release` | `project`、`version`（必填）、`branch`（默认 `main`）、`runner` | `gofer/push` 推 `branch` 与 `v<version>` → `gofer/wait-ci` 等该 tag 的检查 |

```bash
gofer workflow run --template verify  -p <project> --var commit=<sha> --var cmd='<检查命令>' -w
gofer workflow run --template release -p <project> --var version=1.2.3 -w
```

### 示例一：检查一个提交（多路并行）

```yaml
title: "verify ${vars.commit}"
project: <project>
vars:
  commit: {required: true, desc: 要检查的提交}
steps:
  - name: checks
    agent: exec                 # 直接跑命令，不经过 agent
    ref: ${vars.commit}         # 提交时解析成 sha，临时检出，结束即删
    join: all                   # 全部跑完，任一路失败则整体失败
    fan:
      - name: unit
        cmd: [go, test, ./...]
      - name: vet-darwin
        cmd: [go, vet, ./...]
        env: {GOOS: darwin}
      - name: test-worker
        runner: <worker-id>     # 换一台执行机跑
        cmd: [go, test, -count=1, ./...]
      - name: web
        cwd: web
        cmd: [sh, -c, "<前端检查命令>"]
        timeout_sec: 900
  - name: report
    agent: exec
    if: always                  # 上面失败也执行（结论不变：仍算失败）
    cmd: [echo, "unit 退出码为 ${steps.checks.unit.exit_code}"]
```

提交：`gofer workflow run verify.yaml -w`。结果看 `gofer workflow show <id>` 的结果表（每路 runner / 状态 / 退出码 / 耗时 / 失败摘录）。只想重跑失败的那几路：`gofer workflow rerun <id> --failed -w`（新 workflow，原记录不动；`--from <步骤>` 从某步起重跑）。

### 示例二：发版（推送 → 等 CI → 等产物）

```yaml
title: "release v${vars.version}"
project: <project>
vars:
  version: {required: true}
steps:
  - name: push
    uses: gofer/push
    with: {refs: [main, "v${vars.version}"]}
    on_failure: retry                      # 网络 / 钩子类失败重试，每次尝试是独立 job
    retry: {max_attempts: 10, backoff_sec: 300}   # 单个数字 = 每次重试都等这么久；也接受列表 [60, 300]
  - name: ci
    uses: gofer/wait-ci
    with: {ref: "v${vars.version}", required: [<CI 检查名>], code_scanning: true, timeout_min: 40}
  - name: assets
    uses: gofer/wait-assets
    with: {tag: "v${vars.version}", assets: ["*-linux-amd64.tar.gz"], timeout_min: 30}
  - name: mirror                            # 自己的追加步骤，照常写 exec
    agent: exec
    if: always
    run: |                                  # 多行脚本；shell 默认 bash，也可 sh / pwsh / python
      echo "sync v${vars.version}"
      <同步 / 通知命令> v${vars.version}
```

提交前先看渲染结果（web 新建页的「预览步骤」，或 `POST /v1/workflow-templates/release/render`），确认无误再跑：这一步会真的推 main 与 tag。

### 与 GitHub Actions 的对照

| 写法 | gofer workflow | GitHub Actions |
|---|---|---|
| 复用别人的动作 | `uses: gofer/<名>`，版本随 gofer 二进制，没有 `@v1` | 任意 action 仓库 + 版本 |
| 参数 | `with:` 按每个内置步骤的结构严格校验，未知键提交即报错 | action 自己决定 |
| 失败处理 | `on_failure: fail \| continue \| retry`；`if: always` | `continue-on-error`、`if: always()` |
| 并行 | 同一步骤内的 `fan` 并行，步骤之间串行 | 多个 job + `needs` |
| 执行位置 | 步骤 / fan 的 `runner:`（server 或某台 worker） | `runs-on` |
| 看结果 | workflow 详情的结果表（每步 / 每路）与 `gofer workflow show` | 各 job 的日志页 |
| 只重跑失败的 | `gofer workflow rerun <id> --failed` | Re-run failed jobs |
| 触发方式 | 由人或 agent 提交（`workflow run`、HTTP、web），没有仓库内触发器 | push / tag / cron 触发器 |

### 信任模型（谁能改 workflow，谁就能改它跑什么）

- server 从**项目主 checkout 的工作区**读模板，不是从被检查的那个提交读；改了模板而没提交的改动也立即生效。
- 所以仓库里 `release.yaml` 的写权限约等于发版权限：能改它的人就能改发版时执行的命令。普通 exec 步骤仍受项目 `allow_exec` 与调用方上限约束，但有 exec 权限的调用方本来就能在 `runner: local` 上执行任意命令——这是 exec 的既有边界。
- 推送非 `gofer/*` 分支与 tag 需要两把锁：项目 `delivery.allow_push: true` **且**提交方持有 `repo.push` 权限（中风险，默认不授予任何人），否则提交即 403。
- `env` 的值随任务记录落库，**不要放秘密**（token / 密码从执行机环境或项目配置取）。
- 人在 web / CLI 上运行 release 前先看渲染后的步骤再确认提交。

## 多 agent 对比与择优

内置流程模板（公共 var：`project`、`task` 必填；`runner` 留空 = 项目默认）：

| 模板 | 作用 | 其它 var |
|---|---|---|
| `compare` | 同一任务由两个 agent 各在隔离 worktree 跑，停在待择优 | `agent_a`（claude）、`agent_b`（codex） |
| `plan-implement` | planner 只读规划 → 人工 review 闸 → implementer 在 worktree 实现；planner 带上面的「方案规则」并在方案末尾附 gofer-todos 块 | `planner`（claude）、`implementer`（codex） |
| `review-committee` | 两个 agent 只读评审 → verifier 读各评审报告、对照源码逐条核实后汇总 | `agent_a`、`agent_b`、`verifier` |

- 异构扇出：步骤 `agents: [claude, codex]`（第 K 个 fan 用第 K 个 agent）或 `fan: [{agent: claude}, {agent: codex, runner: <r>}]`。
- 每路不同命令（一次并行跑多项检查）：`fan` 的每一项可有自己的 `name`、`cmd`、`env`、`cwd`、`timeout_sec`，未写的沿用步骤上的值；带 `cmd` 时 `agent` 可省（默认 exec）；`name` 在步骤内唯一，之后用 `${steps.<step>.<fan 名>.<field>}` 引用这一路。配合 `join: all`，全部跑完、任一路失败则整体失败：

```yaml
title: "verify ${vars.commit}"
project: my-app                      # 各步不写 project_key 时的默认项目
vars:
  commit: {required: true, desc: 要检查的提交}
steps:
  - name: checks
    agent: exec
    join: all
    fan:
      - {name: unit,        cmd: [go, test, ./...]}
      - {name: vet-darwin,  cmd: [go, vet, ./...], env: {GOOS: darwin}}
      - {name: web,         cwd: web, cmd: [npx, vitest, run], timeout_sec: 600}
  - name: report
    agent: exec
    cmd: [echo, "unit 输出：${steps.checks.unit.stdout}"]
```

  一段 fan 的 `env` 叠加在步骤 `env` 之上（fan 的键优先）。`env` 在本机 runner 与 worker 上都生效：worker 拿到的是随任务下发的环境变量，需要支持它的 worker（协议 v26）；给协议更旧的 worker 派发带 `env` 的任务会被拒绝并提示升级该 worker，而不是在缺少变量的情况下跑出一个假的通过。`env` 的值会随任务记录落库，**不要放秘密**（token / 密码改从执行机的环境或项目配置取）。
- 检查固定提交：`ref:` 让这一步在「指定提交的临时检出」里跑 —— 执行机上建一个无分支、detached 的临时 worktree（位置与 `worktree` 相同：项目的 worktree 落点，默认 `<仓库顶层>/tmp/gofer/wt/<job-id>`），任务结束（成功、失败、取消、超时都算，server 停机被中断也算）立即删除。提交 workflow 时 `ref` 会**解析成完整 sha** 写回 spec，所以同一 step 的所有 fan、重试的每次尝试检查的都是同一个提交（分支/tag 之后移动不影响）。`ref` 可写 sha、短 sha、tag、分支，也可用 `${vars.x}`。worker 上执行时先在自己仓库里找该提交，没有就从默认远端 fetch（先 `fetch <remote> <sha>`，不行再全量 fetch）；仍拿不到就失败并提示「该提交不在远端，先推送该提交，或改在 server 本机检查」——worker 只能检出远端已有的提交。`ref` 与 `worktree: true` 写在同一步骤会报错（一个是用完即删的临时检出，一个是留着交付的分支）。删除失败（扫描机上目录仍被进程占用等）会重试，仍不成功则记 `job.temp_checkout_left` 事件；serve 启动与每 10 分钟一次的清扫（worker 在启动时）按项目自己的 `worktree.dir` 模板扫描，把对应任务已终态的残留补删并记 `job.temp_checkout_swept`——属于运行中任务的、以及 HEAD 在分支上的（`--worktree` 交付物、手建的 worktree）一律不动；删除顺序是先解依赖链、再删目录、再 `git worktree prune`。
- `join: pick`：所有 fan 结束后停在待择优，`gofer workflow pick <wf-id> <step> <fan> [--merge [--squash] [--cleanup-others]]` 选一个成功的 fan 后才推进；`--merge` 紧接着把选中分支合入项目主 checkout，`--cleanup-others` 删掉其余 fan 的 worktree 与分支。单独合并：`gofer job worktree merge <job-id>`，它也能一并收尾：`--todo-done [--note <说明>]`（把该 job 所属 todo 标 done，说明默认 `<分支> 合入 <主线短 sha>`）、`--comment <评论>`、`--cleanup`（删链接 → 删 worktree → 删已合并分支）。合并规则与收尾细节见 [job-advanced.md](job-advanced.md)「并行」。
- 方案转 todo：`plan-implement` 跑完后 `gofer plan import <plan> --from-job <规划 job>` 把方案变成 todo 链。
- web：workflow 详情里扇出步显示为对比视图（每路一列：agent、状态、耗时、diff 摘要与提交、verify、汇报尾部，可并排展开 diff，「选这个并合并」）；新建 workflow 页有「从模板新建」向导（选项目 → 模板 → 按 vars 生成表单 → 预览步骤 → 提交）。见 [web-console.md](web-console.md)。
