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
- web 的 Plans 页有**计划看板**（五列：把卡片拖到 `ready` 即派发、拖到 `done` / `skipped` 即人工标记；`doing` / `needs_review` 由服务端驱动、不可拖入），手机也能看实时进度。

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
--model <id> / --max-tokens / --max-cost / --max-turns
--acceptance "<列表>"   验收标准（"" 清空）；add-todo 还有 --acceptance-from-issue <issue-id>
--scope 'glob,…'        声明改动范围（"" 清空）
--after <ids|prev>      这一项等哪些 todo（prev = 上一条）
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

- 根节点（没有 `after`）只有 `plan run` 或人工置 ready 才会起，加 todo 不会自己跑。
- 某项 done（含验收 accept）或 skipped → 依赖全满足、有 assignee、允许自动的后续项置 ready 并立即派发。没人指派的到点项只记事件，补 `--assign` 后再 `plan run`。
- 链上某项 job 失败 / 超时 / 取消 / 被拒（且没被自动续投接手）→ plan `blocked`，事件 `plan.blocked` 在默认通知集（IM 带链接）。`needs_review` 不算失败：后续等人 accept（继续）或 reject（停链）。
- 没有任何 `after` 的平铺清单，失败不会 block。`plan run` 不理会 `--no-auto`（那是给自动推进用的）。
- 链末放一条 `--assign exec --cmd '<构建/测试命令>'` 的复核项，「改完自动验证」也在链上。
- 可订阅事件：`plan.todo_dispatched`、`plan.todo_dispatch_failed`、`plan.todo_advanced`、`plan.todo_unassigned`、`plan.blocked`、`plan.completed`、`plan.advance_paused`。MCP 对应 `gofer_add_todo` / `gofer_update_todo`（`after` / `auto` / `cmd`）与 `gofer_plan_run`。

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

- `plan create` 默认把计划绑定到**当前所在的 agent 会话**（主 Agent）：依次取 `GOFER_SESSION_ID`、`CLAUDE_CODE_SESSION_ID`、`CODEX_THREAD_ID` / `CODEX_SESSION_ID`；须是已登记、未结束、属于当前 caller（指定了 `--project` 时项目一致）的会话，否则建成未绑定并打印绑定命令。其它 agent 需自己 `export GOFER_SESSION_ID=<sid>` 或显式传参。`--no-supervisor` 不绑定；`plan set <plan> --supervisor-session-id <sid>` / `--clear-supervisor-session` 改绑。
- 绑定后：自动派发的 job 与主 Agent 会话同项目、同 runner、同目录、不开 worktree 时以该会话为来源，并登记「job 结束通知主 Agent 会话」；否则照常派发，仍登记通知。
- 普通 job 可显式 `--source-session-id <sid>`：须由当前认证 caller 拥有，且项目、runner、cwd 与登记一致，否则提交前拒绝。它与 agent 自己的续接目标 `session_id` 不同。
- 主 Agent 会话（含子 agent）的 token 用量同时计入它监督的进行中 plan（同时监督多个时只记到最近有动静的那个），`plan show` 多一行 `session:` 用量；绑定之前的用量不回填。
- web 的 plan 详情页有「主 agent 会话」面板：看状态、改绑、「发给主 agent」（带快捷语「请写交接说明并更新 plan handoff」）。

## workflow：有依赖的多步编排

```bash
gofer workflow run <file.yaml> [-w]                   # 从 yaml / json 提交；-w 轮询到终态并打印每步
gofer workflow run --template <name> --var k=v [-w]   # 从流程模板提交
gofer workflow template ls|show <name> [-p <project>] # 看模板（含来源）与 vars
gofer workflow list / show <id> / events <id> / cancel <id>
gofer workflow export <id>                            # 导出 spec（去密钥），可再 run
```

- 文件格式：`.json` 按 json，其余按 yaml；顶层 `title` + `steps: [...]`。先 `export` 一个跑通的当模板最快。
- step 可声明依赖、`fan_out` 扇出、汇合；步骤可带 `worktree`、`worktree_base`、`template`、`vars`、`read_only`、`verify`、`review`。runner 留空 = 项目默认（项目允许本机就用本机；只列一个就用它；列多个且不含本机则必须显式指定）。
- 引用前序结果：`${steps.<name>.<field>}`，`.fK.` 选第 K 个 fan，`.all.` 选所有成功 fan，`.picked.` 选被择优的 fan。字段：`result_dir`、`result`、`stdout`、`exit_code`、`status`、`job_id`、`worktree_branch`、`diff`（`changes.diff` 的路径）、`diff_summary`。`.all.` 聚合超过 32KB 时自动写成文件、引用处换成文件路径。大段或不可信文本（如评审输出）请以**文件路径**传给下一步的 agent，不要插值进 exec 命令。
- 流程模板位置：项目 `.gofer/workflows/<name>.{yaml,yml,json}` → `<config-dir>/workflows/` → 内置。模板的 `vars:`（`default` / `required` / `desc`）以 `${vars.x}` 或 `{{x}}` 在步骤字段里替换；未声明的 `--var`、残留占位、含特殊字符的 agent / runner / project 值都被拒绝。模板默认 agent 不被项目允许时报 400 并列出可用 agent，用 `--var <变量>=<agent>` 覆盖。

## 多 agent 对比与择优

内置流程模板（公共 var：`project`、`task` 必填；`runner` 留空 = 项目默认）：

| 模板 | 作用 | 其它 var |
|---|---|---|
| `compare` | 同一任务由两个 agent 各在隔离 worktree 跑，停在待择优 | `agent_a`（claude）、`agent_b`（codex） |
| `plan-implement` | planner 只读规划 → 人工 review 闸 → implementer 在 worktree 实现；planner 带上面的「方案规则」并在方案末尾附 gofer-todos 块 | `planner`（claude）、`implementer`（codex） |
| `review-committee` | 两个 agent 只读评审 → verifier 读各评审报告、对照源码逐条核实后汇总 | `agent_a`、`agent_b`、`verifier` |

- 异构扇出：步骤 `agents: [claude, codex]`（第 K 个 fan 用第 K 个 agent）或 `fan: [{agent: claude}, {agent: codex, runner: <r>}]`。
- `join: pick`：所有 fan 结束后停在待择优，`gofer workflow pick <wf-id> <step> <fan> [--merge [--squash] [--cleanup-others]]` 选一个成功的 fan 后才推进；`--merge` 紧接着把选中分支合入项目主 checkout，`--cleanup-others` 删掉其余 fan 的 worktree 与分支。单独合并：`gofer job worktree merge <job-id>`。合并规则见 [job-advanced.md](job-advanced.md)「并行」。
- 方案转 todo：`plan-implement` 跑完后 `gofer plan import <plan> --from-job <规划 job>` 把方案变成 todo 链。
- web：workflow 详情里扇出步显示为对比视图（每路一列：agent、状态、耗时、diff 摘要与提交、verify、汇报尾部，可并排展开 diff，「选这个并合并」）；新建 workflow 页有「从模板新建」向导（选项目 → 模板 → 按 vars 生成表单 → 预览步骤 → 提交）。见 [web-console.md](web-console.md)。
