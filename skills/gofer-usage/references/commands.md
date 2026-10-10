# gofer 命令参考（`job` 之外）

> 主 `SKILL.md` 详讲最常用的 `gofer job`。本文补齐其余命令，**按需查阅**——AI 要用 workflow / plan / schedule / tunnel / session 等时读这里。
> 每个命令的**完整 flag** 用 `gofer <cmd> --help`（本文只给"是什么 + 常用法 + 何时用"）。
> 命令通过主机 server 执行；连接、project key、agent/runner 的通用规则见 `SKILL.md`。

## serve — 本机受管 server

`gofer serve register -c <config.yaml> [--name gofer-serve] [--exe <binary>] [--work-dir <dir>]` 登记原生入口，默认不启动；`--start` 登记后启动并验证进程、端口及健康。Linux 默认 `--scope system` 且必须显式 `--run-as <user>`；`--scope user` 使用当前账号。Windows 使用当前交互账号的计划任务，`--elevated` 与 `--adopt` 仅在 Windows 可用。具名实例用于隔离测试或迁移暂态，须分开配置目录和端口。`-c` 在子命令前后均可写；受管 server 始终以最终配置文件路径启动，pid 与默认应用日志位于该文件所在目录的 `run/`。

登记后使用 `gofer serve start|stop|restart|status|logs|uninstall [--name <name>]`。`status --json` 给出原生入口、受管进程身份、端口归属、健康和已登记版本；进程与端口核对后从受管服务 `/v1/stats` 读取运行版本，并再次核对身份。stats 凭据不可用或运行构建未写版本时，`running_version` 留空并报告 `version_error`，不会以登记版本代替。`logs --lines N [--follow]` 默认读取配置解析后的应用日志；Linux 用 `--journal` 读取 systemd 日志。`uninstall` 停止并移除原生入口，保留程序、配置和数据。没有受管登记的默认实例仍可用原 `serve stop` pidfile 路径。

`gofer serve upgrade --binary <prebuilt> [--name <name>] [--no-wait]` 校验预构建程序并等待独立执行者取得持久接管确认；在本机 direct exec job 内调用时打印 `upgrade_id` 后返回，避免当前 job 等待自身停机。终端默认等待最终结果，`--no-wait` 在确认接管后返回。随后用 `gofer serve upgrade status <upgrade_id> [--json]` 读取跨重启回执；非终态且执行者已不在时显示 `interrupted`，不代表升级成功。候选可以是 UPX 压缩的发布包：文件里读不到 Go build info 时，CLI 以 `GOFER_PRINT_BUILDINFO=1` 运行候选，让它自报 module 路径与 GOOS/GOARCH（旧版 CLI 没有这个回退，可用新候选自身执行 `serve upgrade`）。从 job 发起时要点：命令本身必须是 exec job 的进程（`-- <gofer 绝对路径> serve upgrade …`，不要包在 PowerShell/cmd 脚本里，否则 drain 会等发起 job 自己结束直至超时）；job 不继承 `GOFER_CONFIG_DIR`，用 `--env GOFER_CONFIG_DIR=<config-dir>` 传入。Linux 与 Windows 的平台边界分别见 [serve-management-linux.md](serve-management-linux.md) 和 [serve-management-windows.md](serve-management-windows.md)。

## workflow（别名 `wf`）— job 链（有依赖的多步编排）

把多个 step 串成一条链，step 间可有依赖 / fan-out / join。**从文件提交**（title + `steps[]`）：

```bash
gofer workflow run <file.yaml> [-w]     # 从 yaml/json 提交; -w=轮询到终态并打印每步
gofer workflow run --template <name> --var k=v [-w]  # 从项目/全局/内置流程模板提交(不带 file)
gofer workflow template ls|show <name> [-p <project>] # 查看流程模板(含来源)和 vars 声明
gofer workflow list                     # 列 workflow(可带状态过滤)
gofer workflow show <id>                # 状态 + step 链；完成后进度显示 `1/1 (done)`（不再出现 2/1）
gofer workflow pick <id> <step> <fan> [--merge [--squash] [--cleanup-others]]  # join=pick 选一个 fan(也可 --step/--fan)
gofer workflow events <id>              # 生命周期事件时间线
gofer workflow cancel <id>              # 取消运行中的
gofer workflow export <id>              # 导出 spec(去密钥)可再 import, 默认 yaml(= run 格式)

多 agent 对比和评审流程（Z1–Z3）：

- **异构扇出**：步骤 `agents: [claude, codex]`（第 K 个 fan 用第 K 个 agent，与 `fan_out` 互斥）或 `fan: [{agent: claude}, {agent: codex, runner: <r>}]`（逐个覆盖 agent/runner）；步骤透传 `worktree`、`worktree_base`、`template`、`vars`、`read_only`、`verify`。runner 留空 = 项目默认（项目 `allowed_runners` 含 local/server 或为空 → local；只列一个 → 该 runner；列多个且不含 local → 报错要求显式指定）。
- **引用**：`${steps.<name>.<field>}`（按步骤名）、`.fK.`（第 K 个 fan）、`.all.`（所有成功 fan；`stdout` 直接拼接；**聚合后超过 32KB 不再报错**：自动把聚合内容写成第一个成功 fan 的结果目录下的 `workflow-<步骤名>-all-<字段>.txt`，引用处替换成该文件的绝对路径，让下一步自己去读；每个 fan 最多取 stdout 末尾 8MB）、`.picked.`（被选中的 fan）。字段：`result_dir`、`result`、`stdout`、`exit_code`、`status`、`job_id`、`worktree_branch`、`diff`（= `<result_dir>/changes.diff` 的**路径**）、`diff_summary`（`git diff --stat` 摘要）。无选择器的 `result_dir` 在扇出步上是各成功 fan 的目录（换行分隔）。引用是逐字段逐 argv 替换；评审输出这类大/不可信文本请通过**文件路径**传给下一步的 agent，**不要**插值进 exec `cmd`。
- **`join: pick`**：所有 fan 结束后工作流停在待择优，`workflow pick`（`POST /v1/workflows/{id}/pick` `{step, fan}`）选定一个成功的 fan 后才推进；选择不会自动合并。
- **合并**：`POST /v1/jobs/{id}/worktree/merge` `{squash, cleanup_others}`（CLI `gofer job worktree merge <id> [--squash] [--cleanup-others]`）。仅本机 runner；主 checkout 必须干净且在命名分支（否则 409）；冲突 abort 复原并返回 409 与冲突文件，绝不 push；`cleanup_others` 强制清理同 workflow 步骤/轮次其余 fan 的 worktree 和分支，响应 `cleaned` 列出 job id。远程 worker 的 worktree 返回 409 "supported only for a local runner"。
- **渲染预览**：`POST /v1/workflow-templates/{name}/render` `{"vars":{...}}` 返回套入 vars 后的可执行 spec，不提交（Web 向导的「预览步骤」用它；缺必填 / 未知 var / 未知模板 → 400）。
- **step 行字段**：`GET /v1/workflows/{id}` 的 steps 对 `join: pick` 的扇出步带 `join:"pick"`，被选中的那一路带 `picked:true`。
- **模板**：`GET /v1/workflow-templates[/{name}][?project=<key>]`；`POST /v1/workflows` 体可为 `{"template": "<name>", "vars": {...}}`。查找顺序：项目 `.gofer/workflows/<name>.{yaml,yml,json}`（按 `vars.project` 对应项目）→ `<config-dir>/workflows/` → 内置。spec 的 `vars:`（`default`/`required`/`desc`）在步骤的字符串字段（project/agent/agents/fan/runner/prompt/cmd/cwd/worktree_base/template/vars/verify/tags/子工作流）里以 `${vars.x}` 或 `{{x}}` 替换；未声明的 `--var`、残留的 `${vars.x}`、含空白/特殊字符的 agent/runner/project 值都被拒绝；可选 var 没默认值时为空串。
- **模板默认 agent 项目未开放时**：web 新建向导会自动换成该项目第一个可用的同类 agent（同 type、非 exec）并在表单上方提示；`gofer wf run --template` / `POST /v1/workflows {template}` 渲染后若某步 agent 不被其项目允许，直接 400 并列出该项目允许的 agent，用 `--var <变量>=<agent>` 覆盖即可。
- **内置模板 vars**：`compare`：`project`*、`task`*、`agent_a`(claude)、`agent_b`(codex)、`runner`；`plan-implement`：`project`*、`task`*、`planner`(claude)、`implementer`(codex)、`runner`（规划步只读 + review 闸，计划文本经 `${steps.plan.stdout}` 进实现步 prompt）；`review-committee`：`project`*、`task`*、`agent_a`(claude)、`agent_b`(codex)、`verifier`(claude)、`runner`（`reviews` 步只读扇出，`summary` 步是只读 verifier agent，prompt 里列出各评审的 result_dir，要求读取其中 `stdout.log`、对照源码逐条核实后汇总）。
```

- 文件格式：`.json` 按 json，其余按 yaml；顶层 `title` + `steps: [...]`。先 `export` 一个跑通的当模板最快。
- **何时用**：多步、有依赖/并发的编排（先 build → 再 test → 再 deploy）。**单发命令**用 `job`，别套 workflow。

## plan — job 分组计划（组织 + 跟踪，不是执行编排）

把相关 job 归到一个 plan 下、附带 todo 清单跟踪进度（**不**决定执行顺序，只做归类/看板）：

```bash
gofer plan create --title "<标题>" [--desc "<说明>"] [--plan-id <id>] [--project <项目>] [--supervisor-session-id <session-id> | --no-supervisor]
    # --project = 这个 plan 的 todo 默认在哪个项目里跑(PLAN-02)
    # --supervisor-session-id = 绑定已认证且属于当前 caller 的终端会话(主 Agent)，供 plan 自动派发使用
    # 不传时自动绑定"当前所在的 agent 会话"（见下方「自动绑定主 Agent」）；--no-supervisor = 不绑定
gofer plan set <plan-id> --supervisor-session-id <session-id>  # 绑定；当前 caller 必须是 plan owner 且拥有该 session
gofer plan set <plan-id> --clear-supervisor-session          # 清除绑定
    # plan set 只打印实际改了的字段：leader -> on|off / tags -> a,b / 主 Agent 会话 -> <短 id>|(none)
gofer plan attach <plan-id> <job-id>    # 把已有 job 挂到 plan（注意顺序：先 plan 后 job）
gofer plan list / show <id> / archive <id>
    # show 在每个 todo 下列出挂接的 job(id/状态/agent/用时, 新→旧最多 10 条),
    # 并显示 assignee 与 dispatch_error；头部一行 usage 汇总(tokens/$，来自挂接 job)
gofer plan add-todo <id> "<待办>" [--note "<备注>"] [派发字段...]   # 加 todo(别名 todo-add)，建出来是 pending
gofer plan set-todo <todo-id> [--status pending|ready|doing|done|skipped] [--note "<结果>"] [--append-note "<追加一行>"] [派发字段...]
    # 生命周期推进：--status doing 自动记开始时间, done/skipped 记完结时间;
    # 裸调用=done, --undone=pending(旧用法兼容); --note 单独用只改备注;
    # --append-note 追加一行到现有备注(与 --note 互斥, 服务端原子追加; 只追加不改状态;
    # job 的终态就是这么自动记账的: 见下「todo 联动」)
gofer plan dispatch <todo-id>           # 显式派发(PLAN-02): 不看状态, 只要 assignee + 无活跃 job
gofer plan run <plan-id>                # PLAN-03: 开工/续跑——把所有"依赖已满足 + 有 assignee"的 pending 项置 ready
gofer plan pause <plan-id>              # PLAN-03: 暂停自动推进(正在跑的 job 不取消)
gofer plan resume <plan-id>             # PLAN-03: 解除暂停/阻塞, 并推进一次
gofer plan set-status <id> <status>
gofer plan handoff <plan-id>                 # 查看最新交接说明
gofer plan handoff <plan-id> --set "下一步…"  # 写入新版本（自动 CAS）
gofer plan handoff <plan-id> -f handoff.md --history
gofer plan handoff <plan-id> --version 2
gofer plan brief <plan-id> [--json] [--max-lines N]   # 接手包：字段 / todo（状态·依赖·验收·job 结论·issue）/ 交接说明 / 关联 issue
```

### 接手包（brief）

```bash
gofer issue brief <id> [--max-lines 400] [--json] [--tracker <dir>] [-s <server>]
gofer plan brief <plan-id> [--max-lines 400] [--json]
```

- issue brief 八节：issue（方案评论置顶 / 缺验收标准给补写命令）→ 上下文树 → 设计稿 → 相关提交（+ 代码入口 + 关键符号）→ 相关 job / plan → 本 issue 的验证命令 → 适用记忆 → 接手提示；plan brief：plan → todo → 交接说明 → 关联 issue → 接手提示。节的内容与匹配规则见 SKILL.md「接手：先跑 brief」。
- `--json`：`{kind, id, sections: [{title, lines, note?, more?, truncated?}]}`；文本输出就是逐节渲染。
- server：沿用 `-s/--server`、`--token` 与配置；先探测 `/v1/meta`，连不上时 issue brief 的「相关 job / plan」与全局 / 项目记忆注明原因后跳过，plan brief 报错。每个请求 3 秒上限。
- MCP：`gofer_issue_brief` / `gofer_plan_brief`，入参 `{id, max_lines?, project?}`，返回 `{kind, id, text}`（与 CLI 文本相同）；仓库 tracker 取 MCP 进程的 cwd；独立模式（无 server 客户端）下 server 节注明跳过。

### 计划监督会话与 job 来源会话（Z1）

- **自动绑定主 Agent**：`plan create` 不带 `--supervisor-session-id` / `--no-supervisor` 时，按 `GOFER_SESSION_ID` → `CLAUDE_CODE_SESSION_ID`（Claude Code 自动导出）→ `CODEX_THREAD_ID` / `CODEX_SESSION_ID`（Codex 自动导出）取当前会话 id，做一次短超时（3s）查询：是已注册、未结束、且（指定了 `--project` 时）项目一致的会话才绑定（worker / 容器里的会话也绑定），输出 `已绑定主 Agent 会话 <短 id> (<名称>)`；否则（没有 id / 未注册 / 已结束 / 项目不符 / server 以"不属于当前 caller"拒绝）照常创建未绑定的 plan，并输出一行 `未绑定主 Agent 会话（原因；绑定：gofer plan set <id> --supervisor-session-id <sid>…）`。其它 agent（omp/jcode 等）不导出会话 id，需自己 `export GOFER_SESSION_ID=<sid>` 或显式传参。派发时：job 与主 Agent 会话**同项目、同 runner、同目录、不开 worktree** 时，job 以该会话为可信来源；否则（会话在容器 / worker 上、todo 指定了别的 runner 或目录）job 不带来源声明照常派发，仍登记「job 结束通知主 Agent 会话」。
- MCP `gofer_create_plan`（HTTP-backed `gofer mcp`）同样自动绑定：会话 id 取自启动 `gofer mcp` 的 agent CLI 传下来的环境（不保证传入，如 Codex 只给 MCP server 白名单环境变量），结果里 `supervisor_note` 说明绑定或未绑原因；`no_supervisor=true` 跳过。local backend 无认证 caller，永不绑定。
- plan 可选持久化 `supervisor_session_id`；通过 `plan create/set`、HTTP `POST/PATCH /v1/plans`、client 或 MCP `gofer_create_plan` / `gofer_set_plan_supervisor_session` 设置。PATCH 字段缺省表示保留，显式空字符串表示清除。绑定要求认证 caller 与 plan owner、session owner 一致；plan 项目若已指定，还须与 session 项目一致。
- Web：plan 详情页（PlanDetail）有「主 agent 会话」面板，显示绑定会话的名称 / 状态 / 最后心跳，可打开会话抽屉、从会话列表修改或清除绑定（PATCH `supervisor_session_id`，仅 plan owner），并有「发给主 agent」输入框（走 `POST /v1/sessions/{sid}/messages`，与会话抽屉同一入口：中继 → 传话 → 送话），带快捷语「请写交接说明并更新 plan handoff」。
- job 可选提交 `source_session_id`：CLI 用 `gofer job run … --source-session-id <session-id>`；HTTP/client 的 job 请求和 MCP `gofer_run_job` 使用同名字段。必须由认证 caller 拥有该 session，且项目、规范化 runner、有效 cwd 与登记上下文匹配；未知、他人或上下文不匹配的 session 会在创建 job 前拒绝。省略时普通 job 仍可提交，但不会自动登记 session watch。
- **监督会话用量计入 plan**：绑定后，监督会话（主会话 + 子 agent）心跳上报的 token 用量同时记到该 plan（仅 plan 为 `open` / `blocked` 时；`done` / `archived` 不再累计）。一个会话监督多个进行中 plan 时，每笔用量只记到**最近有动静**的那个（`MAX(plan.updated_at, 最新 todo.updated_at, 最新 job.updated_at)`，同分取较新的 plan），所以各 plan 的会话用量相加等于会话总量、不重复；活跃 plan 切换后只有之后的用量跟着走。`GET /v1/plans/{id}` 的 `usage` 多 `session {main, sub, total, by_model, sessions}` 与 `overall {total_tokens, cost_usd}`（jobs + 会话）；`gofer plan show` 多一行 `session: main … / sub … tokens / $… (overall …)`；web plan 详情「用量」区分 jobs 与主 Agent 会话（主 / 子 agent、按模型）。**绑定前的用量不回填**（会话用量按日桶存，无法切到绑定时刻）。
- plan 派发会继承 `supervisor_session_id` 写入 job 的 `source_session_id`，并登记到现有 session watch。`source_session_id` 是提交者/监督会话；`session_id` 是 agent 自己的续接目标，两者不能互换。MCP local backend 没有认证 caller context，会拒绝设置或提交 source session；HTTP-backed MCP 使用服务端认证。

**派发字段（PLAN-02，`add-todo` / `set-todo` 共用）**：

```bash
--assign <agent>       # 谁跑这一项；**ready + 有 assignee = 立刻出 job**（见下「todo 指派即派发」）
--project <项目>       # 覆盖 plan 的 project
--template <名> --var k=v   # 用任务书模板渲染这一项的 prompt（内置变量含 {{plan_title}} {{plan_description}} {{todo_title}} {{todo_note}} {{todo_id}}）
--verify '<命令>'      # agent 正常结束后在同一 cwd 跑的验收命令（非 0 退出 → job failed；需项目 allow_exec）
--review               # 这一项正常完成后停在 needs_review，等人验收
--runner <key>         # 执行 runner（缺省 = server 即本机；`server` 与 `local` 在所有入口等价，服务端落库为 `local`）
--cwd <相对路径>       # 工作目录（缺省项目根）
--timeout <秒>         # job 超时（缺省 server 默认）
--after <ids|prev>     # PLAN-03: 这一项等哪些 todo（逗号分隔的 todo id；`prev` = 上一条，建链最常用）
--auto / --no-auto     # PLAN-03: 允许/禁止链自动启动这一项（缺省 auto=1）
--cmd '<argv>'         # PLAN-03: `--assign exec` 时这一项要跑的 argv（如 --cmd 'go test ./...'）；exec 项没 --cmd 会被拒绝
```

- **workflow vs plan**：workflow = **执行**依赖链（server 按链跑）；plan = **组织** view（把散 job + todo 归一起看）。
- **todo 状态机**：`pending`（backlog，永不自动派发）→ `ready`（可派发）→ `doing`（job 在跑）→ `done`/`skipped`；**server 重启不补派** ready 的项（派发只发生在写入路径上）。
- **plan 状态**：`open`（在跑）/ `blocked`（链停在某一项，等人处理；**非终态**）/ `done`（全部 done|skipped 时自动置）/ `archived`；另有 `paused` 开关（暂停自动推进，不影响 status）。

### 范式：todo 指派 agent 即派发（PLAN-02，长任务不再靠人敲命令）

多步骤长任务（大改造 / 迁移 / 分阶段实施）的推荐姿势：**一边规划、一边把每一步指派给一个 agent**——`status=ready` 且有 `assignee` 的 todo 会**立刻变成 job**，跑完由 job 终态自动写回（状态 + 交付的提交），web 控制台 Plan 详情页（手机可开）就是实时进度页，人不必一条条敲"开始/结束"。

```bash
# 开工：建计划（--project 让后续每一项不必重复写项目） + 每一步一个 todo
gofer plan create --title "xxx 改造实施" --project <项目>
# 规划 + 派发一步到位（两个条件哪个后到都触发）：
gofer plan add-todo <plan-id> "步骤1: 数据模型迁移" --assign omp --note "先跑 migration，再补索引"
gofer plan set-todo <todo-id> --status ready          # 该步立刻出 job → todo 转 doing
# 想连"怎么跑"一起定下来（模板/验收/验收人/执行机）：
gofer plan set-todo <todo-id> --assign omp --status ready --template impl-batch \
    --var tasks="只做 index 重建" --verify 'go test ./...' --review --runner local --cwd . --timeout 1800
# 验收标准 + 声明改动范围（派出的 job prompt 末尾带「## 验收标准」「## 交付约定」，验收面板对照）：
gofer plan add-todo <plan-id> "步骤2: 索引重建" --assign omp --acceptance-from-issue <issue-id> --scope 'internal/index/**'
gofer plan set-todo <todo-id> --acceptance $'- 全量测试通过\n- 迁移可重复执行'   # --acceptance "" / --scope "" 清空
# 兜底：显式派发（不看状态；只要求 assignee 且当前没有活跃 job）
gofer plan dispatch <todo-id>
# 某步决定不做 / 手工推进：
gofer plan set-todo <todo-id> --status skipped --note "原因..."
gofer plan set-todo <todo-id> --status done --note "手工收尾完成"
```

要点：

- **触发点只有写入路径**：`--status ready`、设置/修改 `assignee`、显式 `plan dispatch`（MCP 侧 `gofer_update_todo` / `gofer_dispatch_todo` 同）。**server 重启不补派**——parking 一项是有意为之，开机自动跑工作不是。
- **无模板时的默认 prompt** = `# <plan.title>` + `<plan.description>` + `## 本任务` + `<todo.title>` + `<todo.note>`；有模板时用模板，内置变量多 `{{plan_title}} {{plan_description}} {{todo_title}} {{todo_note}} {{todo_id}}`（**一份模板服务整个 plan 的每一项**）。
- **再跑 / 换人**：处于 `doing`/`done` 的项再置 `ready` = 重跑一次（前一个 job 必须已终态或 needs_review 已裁决）；`--assign` 另一个 agent + `--status ready` = 换人。改派/重跑都不会插队到正在跑的那一次。
- **失败不自动重派**：job 失败 → todo 保持 `doing` + note 写原因；要不要再跑由人或 wakeup 决定。派发本身失败（无项目 / agent 不允许 / 项目不在 worker 上）→ todo **保持 ready** 并把原因写进 `dispatch_error`（`plan show` 与 web 都显示红字），事件 `plan.todo_dispatch_failed` 记在 plan 作用域；成功派发记 `plan.todo_dispatched`（事件默认通知集不变）。
- **派发出去的还是普通 job**：verify / review / runner / cwd / timeout / worktree 等照常生效，`job list --plan <id>` 能看到，进度页可点进日志。
- note 写**结果/验收一句话**（不是过程流水，过程在 job logs）。
- **验收标准 / 范围**：todo 的 `acceptance`（`--acceptance`；`--acceptance-from-issue <id>` 只在 add-todo，从当前仓库 tracker 读 issue 的 `acceptance_criteria`，读不到报错）与 `scope`（`--scope` 路径 glob，相对仓库根，逗号分隔可重复）派发时原样拷进 job；HTTP `POST /v1/plans/{id}/todos`、`PATCH /v1/todos/{id}` 与 MCP `gofer_add_todo` / `gofer_update_todo` 同名字段（`acceptance: ""` / `scope: []` 清空）。plan todo 派出的 job 在 `scope_discipline: auto`（默认）下都带「## 交付约定」节。

### 范式：todo 依赖链 + plan run（PLAN-03，建好链只管验收）

**推荐姿势**（取代「一条条 set-todo --status ready」）：把步骤建成**有依赖的链**，`--after prev` 让每一项等上一条，然后 `plan run` 一次开工——之后每一环由前一环的终态自动启动，人只在失败/验收处介入。

```bash
# 1) 建计划（--project 让后续每一项不必重复写项目）
gofer plan create --title "xxx 改造实施" --project <项目>

# 2) 建链：4 项，最后一项是容器/本机的构建+测试复核（exec，不调 agent）
gofer plan add-todo <plan-id> "步骤1: 数据模型迁移"   --assign omp --note "先跑 migration，再补索引"
gofer plan add-todo <plan-id> "步骤2: 服务层改造"     --after prev --assign omp --verify 'go test ./internal/...'
gofer plan add-todo <plan-id> "步骤3: CLI/文档同步"   --after prev --assign omp
gofer plan add-todo <plan-id> "步骤4: 全量复核"       --after prev --assign exec \
    --cmd 'bash -lc "go build ./... && go vet ./... && go test ./..."' --cwd .

# 3) 开工：把"无未完成依赖 + 有 assignee"的项置 ready（这里只有步骤1），随后自动一环接一环
gofer plan run <plan-id>

# 4) 盯着看（web Plan 页也可）：todo 状态、当前 job、plan 是否 blocked
gofer plan show <plan-id>

# 5) 中途失败会停在那一项（plan status=blocked，plan.blocked 是默认通知事件）：
gofer plan set-todo <todo-id> --status ready     # 重派这一项（改 --assign 换人后再置 ready 也可）
gofer plan set-todo <todo-id> --status skipped   # 或者跳过它，让后续继续
gofer plan resume <plan-id>                      # 或：解除暂停/阻塞后继续

# 6) 想临时挂住整条链（正在跑的 job 不受影响）
gofer plan pause <plan-id>
```

要点（PLAN-03）：

- **根节点不自动启动**：`after` 为空的项**只有** `plan run` 或人工 `--status ready` 才会起（加一条 todo 不会自己跑）。
- **一环接一环**：某项 done（含验收 accept）或 skipped → 扫同 plan 的 pending 项，依赖全满足且 `assignee` 非空且 `auto=1` → 置 ready 并立即派发（PLAN-02 的"ready + assignee = 出 job"照旧生效）。
- **没人指派的到点项**：只记事件 `plan.todo_unassigned`（人来补 `--assign` 再 `plan run`/置 ready），不会静默卡死。
- **失败 = 停链**：链上某项 job `failed|timeout|cancelled|rejected`（且不是被自动续投/转移接手的）→ plan `status=blocked` + `blocked_todo` + 事件 `plan.blocked {todo_id, job, reason}`（**进通知默认集**，IM 会带 `/plans/{id}` 链接）。注意 `needs_review` **不算失败**：它让后序继续等，直到人 accept（accept 后继续推进）或 reject（按失败停链）。
- **平铺清单不受影响**：plan 里没有任何 `after` 时，失败不会 block——还是 PLAN-02 的行为。
- **`plan run` 不理会 `--no-auto`**：`--no-auto` 是给**链**用的（自动推进时跳过这一项），人工 `plan run` 是显式开工。
- **事件**：`plan.todo_advanced {todo_id, after}` / `plan.todo_unassigned {todo_id}` / `plan.blocked {todo_id, job, reason}` / `plan.completed` / `plan.advance_paused`，都记在 plan 作用域（`plan:<id>`），可订阅；只有 `plan.blocked` 在默认通知集里。
- MCP 侧对应：`gofer_add_todo` / `gofer_update_todo` 的 `after` / `auto` / `cmd` 字段，以及 `gofer_plan_run`。

### 方案规则与 plan import（gofer-3nxa.5，方案直接变 todo 链）

写实施方案（`plan-implement` 的 planner 已自动带上，自己写方案时照做）遵循以下规则（单一来源：`internal/job/plan_rules.go` 的 `PlanRules`，此处为原文）：

```
方案规则：
1. 步骤按依赖自底向上（存储 / 模型 → 协议 / 接口 → 业务 → 调用方 / 前端），任何步骤不得使用后续步骤才创建的东西；
2. 优先垂直切片，每片完成后可编译、可验证；
3. 单步预计改动 > 5 个文件、跨 2 个以上子系统、或标题含「并且 / 同时」，拆开；
4. 每 2~3 步一个检查点（可执行的验证命令）；
5. 对跨模块、不可逆、并发 / 幂等 / 不变量类关键决策做一次「假设作者过度自信」自审，写出最可能错在哪；
6. 不确定、需要人拍板的点收进「待确认问题」，不进入实现。
```

方案末尾附一个 ```` ```gofer-todos ```` 围栏块（YAML 列表，一项一步）：`title`（必填）、`after`（依赖的前序步骤：标题或从 1 起的序号，可写列表；**省略 = 依赖上一步**，`[]` = 无依赖）、`acceptance`（文本或列表）、`scope`（路径 glob 列表或逗号分隔）、`check`（检查点命令）。未知字段报错（防拼写错误静默丢字段）。

```bash
gofer plan import <plan-id> -f plan.md --assign codex --dry-run   # 只打印要建的 todo（不连 server）
gofer plan import <plan-id> --from-job <规划 job-id> --assign codex # 读该 job 汇报（stdout 尾部 256KB）里最后一个 gofer-todos 块
gofer plan run <plan-id>                                           # 建好后开工
```

- 每步建一个 todo（`--assign` 给的 agent；不给 = 先不指派），带 `acceptance` / `scope`；`after` 引用解析成刚建出的 todo id。
- 有 `check` 的步骤后面多建一个 exec 复核项「检查点：<步骤标题>」（`--assign exec --cmd '<check>'`），**后续引用该步骤的项等的是这个检查点**，检查失败即停链。
- `after` 只能指向前序步骤（指向自己或后面的步骤、不存在的序号 / 标题、重名标题都报错）；`--assign exec` 被拒（步骤是 agent 的活，检查点自动是 exec）。
- 中途建失败会报「已建 N 项」后停止，不回滚。


### 范式：决策点问人（gofer_ask_human）

大计划跑到**需要人拍板的分叉**时，agent 经 MCP 工具 `gofer_ask_human` **阻塞提问**；人在 web「待我决策」（首页 / 顶栏浮层）/ Plan 详情页作答，答案从工具返回值流回，会话原地继续（设计 §C3）：

```
# agent 会话里(MCP 工具, 阻塞等待):
gofer_ask_human{plan_id, title, question, options: ["方案A","方案B"], timeout_sec: 1800}
# → 返回 {state:"answered", answer, answered_by} 或 {state:"expired"}

# 人这一侧(CLI 等价面, 不阻塞; web 更常用):
gofer plan ask --plan <id> --title "..." --question "..." [--option A --option B] [--timeout 30m]
gofer plan decisions --state OPEN
gofer plan answer <decision-id> --answer "方案A"
```

要点：

- **超时兜底在 agent 侧**：收到 `{state:"expired"}` 后按预案继续（执行推荐项），或把该步 todo 置 `skipped` + note 说明后跳过——**不无限阻塞、不原地重问**。
- `timeout_sec` 缺省 1800s（clamp `[2s, 24h]`）。按**宿主客户端的 tool 调用超时上限**设定：宿主若先杀调用，decision 留 OPEN、到期自动 EXPIRED，通道本身无错。
- **决策点串行提问是范式建议**（宿主客户端可能串行执行 tool call），**不是** MCP 连接限制——go-sdk 服务端并发执行 tool call，ask 阻塞不排队其他调用。

## memory — 经验候选（gofer-3nxa.2，交付后知识回流）

开了 `knowledge_capture`（项目配置，`auto|on|off`，默认 `auto` 与 `scope_discipline` 同口径）的 agent job，「## 交付约定」会要求汇报末尾写「## 可复用经验」（每条一行，只写以后其他任务也用得上的）。job 交付（done / needs_review）时 server 解析该小节（规则同「发现但不碰」：最后一个同名小节、任意级别标题、续行并入、代码块里的标题不算），逐条记成**待处理候选**（同一 job 同一文本只记一次）。没有人接受就**不会进记忆**。

```bash
gofer memory candidates [-p <项目>] [--job <job-id>] [--all] [--json]   # 默认只列待处理；--all 含已接受 / 已拒绝
gofer memory accept <候选 id> --key <k> [--kind rule|note] [--summary "一句话"] [--global | --project <p>]
gofer memory reject <候选 id>
```

- 接受 = 写一条**作用域记忆**：默认写到候选所在 job 的项目（`--global` / `--project` 改），正文就是候选原文，`kind` 默认 `note`，来源 `job:<id>`；正文 >200 字须 `--summary`（同 `memory set`）；目标作用域里已有同名 key → 409，换 key 再接受（不会覆盖已有记忆）。
- 拒绝只改候选状态，不写任何记忆。接受 / 拒绝**只能由人做**（job 凭据只能列出，worker token 不能访问）。
- HTTP：`GET /v1/memory-candidates?job_id=&project=&status=pending|accepted|rejected|all`、`POST /v1/memory-candidates/{id}/accept {key,kind,summary,global,project}`、`POST /v1/memory-candidates/{id}/reject`。MCP：`gofer_memory_candidates`、`gofer_memory_candidate_adopt`（即 accept；MCP 面不出现 accept 字样的工具）、`gofer_memory_candidate_reject`。
- Web：job 详情验收面板的「经验」页签（有候选才出现，计数 = 待处理数）。

## job 的"续"与"验收"：`resume` / `worktree` / `accept|reject`

```bash
gofer job resume <源id> --prompt "…" [--runner <同源>]   # 续跑源 job 的 agent 会话(新 job id); 源 job 须终态且有 session_id
gofer job resume <源id> --env K=V   # 续接显式 env(可重复, 覆盖继承值, 存进新 job 的 request_json 勿放密钥); 继承的源 agent env / 源 job env / env_files 只在执行时注入, 不写入新 request_json; cli-agent 续接沿用源 agent 的 ndjson 输出投影
gofer job resume <源id> --mode session|interactive|batch [--agent <同族agent>]   # 显式选续接形态(持续 ACP / pty / 一次性 --resume -p); --agent 只允许同会话族(claude-acp/claude；codex-acp/codex)
gofer job say <id> "下一轮消息"                          # ACP 持续会话(job run --session): 同一 job 再发一轮; 状态 awaiting_input → running
gofer job end <id>                                       # 结束持续会话并释放目录锁/并发名额(done)
gofer job set <id> --title "新标题"                      # 改 job 标题; --title "" 清空
gofer job accept <id> [--note "…"]                       # 人工验收通过: needs_review → done
gofer job reject <id> --note "…" [--resume]              # 人工验收拒绝: needs_review → rejected(终态); --resume 以 note 为 prompt 续投
gofer job run … --review                                 # 让这个 job 正常完成后停在 needs_review 等人验收
gofer job run … --acceptance $'- 测试通过\n- 文档已更新'  # prompt 末尾追加「## 验收标准」(非 exec)；job review / 验收面板显示
gofer job run … --scope 'internal/job/**,web/src/x.vue'  # 声明改动范围(可重复)；验收时越界文件标「范围外」（含 job 自己提交的文件；共享 checkout 里他人同期提交可能误报），不阻塞 accept
gofer job run … --no-scope-discipline                    # 本次不追加「## 交付约定」节(项目 scope_discipline: auto|on|off)
gofer job review <id> [--tail N] [--diff]                # 验收材料一屏：验收标准 / scope 越界 / 汇报 / 发现但不碰
gofer job findings <id> [--create-issues] [-p 2] [--tag discovered]   # 列汇报「## 发现但不碰」各条；--create-issues 在当前仓库 tracker 逐条建 issue
gofer job list --status needs_review                     # 谁在等人验收
gofer job list --all                                     # 包含内部传话送达 job
gofer job worktree ls [-p <project>]                     # 列 --worktree job 留下的 worktree: 分支/领先提交/是否脏/是否已合并
gofer job worktree rm <job-id> [--force] [--delete-branch]   # 移除 worktree(脏且无 --force 拒绝); 分支默认保留

### ACP 持续会话 job（`--session`，A1）

```bash
gofer job run -p <project> -a <acp-agent> --session [--prompt "…"] [--timeout <每轮秒>] [--idle-timeout <等待秒,0=1800>] [--max-session <总秒,0=不限>] [--lock <子目录>|--worktree]
gofer job say <id> "…"   /   gofer job end <id>
gofer job resume <acp源id> [--prompt "首条消息，可省"]    # 新开持续会话: session/load 载入源会话 → awaiting_input
```

- 状态机：`running → awaiting_input → running …` → `done`（`end` / 空闲到期，`session_end_reason`）/ `cancelled` / `failed`。事件 `job.turn_started` / `job.turn_ended` / `job.awaiting_input`，stdout 里每轮有 `turn N` 分隔。
- `--timeout` 单轮；`--idle-timeout` 等下一条消息；`--max-session` 全会话。`awaiting_input` 期间仍占目录锁与 agent `max_concurrent`。
- runner：server 本机 或 协议 ≥ v13 的 worker（`gofer worker show <id>`）；更老的 worker、peer 等其他远程 runner 提交即被拒。非 ACP agent、或与 `--interactive` 同用，也被拒。
- 恢复：server 重启 / worker 重连后以 `session/load` 重拉 agent；agent 不支持（或 `acp.load_session: false`）则 `failed`，改开新会话。

### 交互 pty job 的文本转录与会话续接（PTY-01）

交互 job（`--interactive`）的 pty 输出**不进 stdout.log**——它只走 cast 录制与 attach 流。gofer 现在同时在结果目录写一份**去 ANSI 的文本转录** `<result_dir>/pty.txt`（默认保留尾部 4MB，`pty.transcript_max_bytes` 可调），所以：

- `gofer job logs <id>` 对交互 job 自动回落到 `pty.txt`（stdout 页签同样；`GET /v1/jobs/{id}/logs/stdout` 也是），不再是一个空面板。
- **session_id 在退出时捕获**：观察器改为「头 64KB + 尾 64KB 环」并对**去 ANSI 后**的文本跑 `session_capture`，relay 关闭时再扫一次尾部；终态还会兜底扫 `pty.txt`（claude 的 TUI 在退出横幅打印 id，codex 打印 `codex resume <uuid>`）。**TUI 每帧都夹着 ANSI**、id 又在最后几毫秒才打印，这是以前交互 job 拿不到 `session_id` 的原因。
- **注入对交互同样生效**：带 `session_inject` 的 agent（claude `--session-id <uuid>`）在交互 argv 上也追加，提交时就知道 id。于是 `gofer job resume <id>` 能续上交互会话（codex 无注入模板，靠上面的退出捕获）。

## job wakeup — 登记等待，条件到达时自动续投（JOB-09）

agent 在一个 job 上登记**事件订阅**或**定时器**，job 正常结束；条件到达时 gofer 自动起一次**续投**（有 session → 续同一会话；无 session → 原请求 + 指令重跑），把登记时的 `instruction` 当提示词。没有常驻进程。agent 在 job 内用 `$GOFER_JOB_ID` 指自己。

| 命令 | 说明 |
|---|---|
| `gofer job wakeup create <job> --kind at --after 10m \| --at <RFC3339> -m "…"` | 到点触发一次（`--after`/`--at` 互斥，客户端换算成绝对时刻） |
| `gofer job wakeup create <job> --kind every --every 1h -m "…"` | 每隔一段（≥ 60s；下一次 = 现在 + 间隔，不补发漏掉的 tick） |
| `gofer job wakeup create <job> --kind cron --cron '0 9 * * 1-5' [--tz Asia/Shanghai] -f instr.md` | cron 表达式（时区缺省 = 服务器本地时间）；`-f` 从文件读指令（与 `-m` 互斥） |
| `gofer job wakeup create <job> --kind event --event job.terminal[,…] [--job-id <源>] [--status done,failed]` | 订阅事件；`--job-id` 缺省 = 自己；`--status` 只对 `job.terminal` 有效 |
| `--mode once \| continuous` | 触发一次即消费（at/event 默认）/ 保持生效（every/cron 默认） |
| `gofer job wakeup list <job>` | 列出一个 job 的唤醒（形态 / 在等什么 / 触发与合并次数） |

时间渲染（h-aii-tnua）：job / 唤醒 / schedule 的绝对时刻统一按**服务端本地时区**渲染并带偏移后缀（如 `2026-09-22 20:13:20 +08:00`），偏移来自 `GET /v1/stats` 的 `server_tz_offset_sec`（web 同）；拿不到时回落进程本地时区并在末尾标 ` (local)`。线上时间字段仍是 Unix 秒。
| `gofer job wakeup show <wid>` | 单条详情（含 next_run_at / last_fired / 最近续投 job / 到期时刻 / 指令全文） |
| `gofer job wakeup enable <wid>` | 启用（定时器**从现在重新起算**，不补发关闭期间的触发） |
| `gofer job wakeup disable <wid>` | 停用（保留记录） |
| `gofer job wakeup rm <wid>` | 删除 |
| `gofer job show <job>` | 一行摘要：`wakeups: 2 enabled (1 every, 1 event)` |

事件目录（v1，写别的类型 400）：`job.terminal`、`job.verify_finished`、`job.needs_review`、`job.reviewed`、`job.fell_back`、`job.stalled`、`interaction.answered`、`session.takeover_released`。

语义与边界：

- **同一 wakeup 同一时刻只允许一个未终态续投**：期间再触发只 `coalesced_count++`（事件 `job.wakeup_coalesced`），不叠 job。前一个续投到终态后，下一次触发才再起一个。
- 续投 job 带 tag `wakeup:<wid>`；目标 job 上记 `job.wakeup_fired {wakeup_id, kind, reason, continuation_job}`。失败（无法续投也无法重跑）记 `job.wakeup_failed {…, error}` —— 不会静默。
- **默认 7 天过期**（`wakeup.ttl_sec`）：到期自动停用 + `job.wakeup_expired`；目标 job 被 retention 清理时唤醒级联删除。
- 权限同 `job resume`：只能给自己提交的 job（或持有 `can_answer` 的 caller）登记 / 开关 / 删除；worker token 写 HTTP 一律 403。
- HTTP：`POST|GET /v1/jobs/{id}/wakeups`、`GET|PATCH|DELETE /v1/wakeups/{wid}`（PATCH body `{enabled}`）。MCP：`gofer_wakeup_create` / `gofer_wakeup_list` / `gofer_wakeup_disable`。
- 默认通知集**不变**：要 IM 提醒就显式订阅 `job.wakeup_*`。web job 详情页有「唤醒」块（列表 / 开关 / 新建 / 触发历史）。
gofer job run … --worktree [--worktree-base <ref>]       # 在 <顶层>/tmp/gofer/wt/<job-id> 的 worktree 里跑, 分支 gofer/<job-id>
gofer job run … --todo <todo-id>                         # 为某个 plan todo 跑这个 job(见下「todo 联动」)
gofer job run … --source-session-id <session-id>         # 以当前认证 caller 拥有的终端 session 作为提交来源并登记 watch
gofer job run … --verify 'go test ./...' [--verify-timeout 900]   # agent 正常结束后在同一个 cwd/env 跑这条验收命令; 非 0 退出 → job failed
gofer job run … --no-verify                              # 关掉项目默认的 verify(见下「验证步骤」)
gofer job run … --upload ./a.bin:tmp/in/a.bin            # 提交前把本地文件暂存到 server, 执行机在 agent 开跑前放好(目标按 job 的 cwd 解析, 须在项目根内); 放不下就 job failed、agent 不启动; 可重复
gofer job run … --collect 'tmp/out/*.csv'                # job 结束后(失败也收、verify 之后)按 glob 在 job 的 cwd 收集, 传回落进本 job 的 artifacts/collected/<项目根内相对路径>(web 详情页与 artifacts 下载直接可用); 可重复
gofer job run … --fallback omp,claude                    # 本 job 的故障转移候选(覆盖项目/agent 级; 见下「故障转移」)
gofer job run … --no-fallback                            # 本 job 不做故障转移(覆盖一切配置)
gofer job run … --exclusive-dir                          # 强制独占工作目录(默认规则外, 见下「同目录串行锁」); exec job 也独占
gofer job run … --shared-dir                             # 放弃独占(可写 agent job 默认独占), 自担与他人共用一棵树的后果
gofer job run … --stall-timeout 600                      # 静默超过 600s 就判停滞并杀掉(默认 = agent 配置 > server 900s; 见下「输出停滞」)
gofer job run … --no-stall                               # 本 job 永不因静默被杀(覆盖 --stall-timeout 与各类配置)
gofer job run … -t <模板> [--var k=v …] [--prompt "追加正文"]   # 用任务书模板派活(服务端渲染 prompt; 见下「任务书模板」)
gofer job show <id>                                      # 打印 dir(独占/共享, 等目录锁时的持有者) / todo / base_sha / commits(这次交付了哪些提交) / verify(验收结果) / usage(用量与成本) / xfer(upload/collect/skipped 计数)

通知长度：`server.notification.max_text_runes` 默认 3000，单个 webhook 可用同名字段覆盖（0/缺省继承全局；<=0 的全局值回到默认）。钉钉/飞书渲染还受 18000 UTF-8 字节上限约束；generic webhook 的 JSON 契约不变。会话“等你回复”会读取最后一段（最多 64K rune）交给统一渲染，不再先截 200 字。

`gofer job redact <id> --literal-from-stdin` 从 stdin 读取原文（去掉末尾换行）；也可重复 `--pattern <RE2>`，两者至少一个。只允许终态 job 的 owner/admin，返回 DB/文件命中数和跳过的二进制相对路径，不回显原文；远程 worker 缓存、已发通知和外部日志不处理。

`gofer job secret-scan --literal-from-stdin [--pattern <RE2>] [-p <project>] [--since <dur>]` 跨终态 job 查找同一秘密；literal 只从 stdin 进入，输出仅含 job id、掩码标题、位置和计数。非管理员按 caller 过滤到自己的 job，管理员可跨项目；运行中 job 只列出并提示不能脱敏，`CLAUDE*` 文件跳过。

`gofer job secret-scan ... --redact --yes` 在扫描结果上逐个复用 `job redact`，每个 job 保留 `job.redacted` 审计，批次结束统一截断 WAL；`--vacuum` 显式执行一次 VACUUM 并提示可能的锁影响。未带 `--yes` 不会修改任何 job。redact / secret-scan 的 literal 同时匹配它的长片段（开头或结尾连续 ≥12 字符、且不短于一半），用于覆盖自动标题、预览被截断后残留的部分原值。

`gofer job delete <id> [<id> ...] --yes` 删除终态 job 的记录、评论、附件和 result_dir，并保留 `job.deleted` 审计；未带 `--yes` 拒绝执行。HTTP 为 `DELETE /v1/jobs/{id}`，Web 详情页有权限时显示二次确认按钮。

`job run` 提交前会对最终 command/args/prompt/title/tags 做常见秘密形态扫描；命中只向 stderr 写出位置和“命令行疑似包含秘密，会被保存在 job 记录中”，不回显值、不阻止提交。`--no-secret-check` 关闭；HTTP/server 提交没有这层客户端提示。
gofer job review <id> [--tail 60] [--diff]                # 验收一屏: status/review/verify/commits(≤20)/usage/diff --stat + 汇报尾部(默认 60 行, 取 stdout 末 64KB); --diff 追加完整 diff; 只看不改, 退出码 0
```

- **resume 的 `--mode` / `--agent`（HTTP `POST /v1/jobs/{id}/resume` 的 `mode` / `agent` 字段）**：空 mode = 按源/目标 agent 类型决定形态（行为不变）。`session` → 目标须是 acp-agent（且 `load_session` 未关），源 runner 须是 local 或 worker(v13+)，prompt 可省；`interactive` → 目标须是带 `session_resume_interactive` 的 cli-agent、项目须 `allow_interactive`，prompt 被忽略；`batch` → 目标须带 `session_resume` 模板，prompt 必填。`--agent` 缺省 = 源 agent；换 agent 只允许同「会话族」（`agent.SessionFamily`：`claude`/`claude-acp` 同族——依据：claude-code-acp 经 Claude Agent SDK 把会话写到 `~/.claude/projects/<编码cwd>/<id>.jsonl`，与 `claude --resume` 读的是同一份存储；`codex`/`codex-acp` 同族——codex-acp 的会话 id 即 `~/.codex/sessions/**/rollout-*-<id>.jsonl` 的 id，2026-10-05 主机实测双向互转通过）。不支持的组合返回 400 并说明原因。`GET /v1/agents` 每项带 `session_resume` / `session_resume_interactive` / `acp_load_session` / `from_session`（能否带 `from_session` 新开，即 cli-agent 配了 `from_session_args`）/ `session_family` 供前端预判。
- `resume` vs `rerun`：`rerun` 是同一请求重提（新会话）；`resume` 是让 codex/claude 用 `exec resume <sid>` / `--resume <sid>` 接着上次会话跑，prompt 只说"从哪继续"。**acp-agent 的 resume 走协议 `session/load`，不需要 `session_resume` 模板**（也不需要注入/捕获模板）；agent 没声明 `loadSession`（或配了 `acp.load_session: false`）时 resume 直接报不支持，不会偷偷开新会话。
- **指定模型**：`job run --model <id>`（MCP/HTTP `model`、任务书 frontmatter `model`、`plan add-todo|set-todo --model`、`job resume --model` 覆盖）——cli-agent 的 `model_args`（内置 claude `--model {{model}}`、codex `-m {{model}}`）插在 `{{prompt}}` 参数之前（无 prompt 参数的交互/交互 resume 模板追加在末尾），acp-agent 经 `session/set_config_option`（category `model`）或 `session/set_model` 选择，agent 不支持则 job 失败并列出可选值；不指定 = argv 不变；resume 沿用源 job 的 model；`job show` 打 `model:`；worker 需协议 v19。
- **继承旧会话开新会话**：`job run --from-session <会话id>`（HTTP/MCP `from_session`）——cli-agent 把 `from_session_args`（如 suag `[--from, "{{from_session}}"]`，无内置默认）追加在 args 模板之后，新会话 id 仍由 `session_inject` 注入；没配该片段的 agent、exec/acp-agent、与 resume（`session_id`/`resumed_from`）混用即 400；源 id 是 gofer 已知会话时须同 agent 或同会话族；`job show` 打 `from_session:`；worker 需协议 v21。web：job 详情页 / 会话抽屉的「从此会话新开」按钮带 `from_session` 打开新建表单（`/new?from_session=<id>`）。
- **只读 job**：`job run --read-only`（审查/分析类任务，agent 不能写文件）——cli-agent 追加 `read_only_args`（内置 codex `-s read-only`、claude `--permission-mode plan`），acp-agent 用 `acp.modes.read_only` 映射到 agent 的 mode id（prompt 前 `session/set_mode`）；exec agent 与没配只读模式的 agent 提交即被拒。resume 继承只读（同一 job 链内不能升级为可写）。
- **人工验收（`needs_review`）**：`job run --review`（或项目 `require_review: true`，或 workflow 步骤 `review:`）的 job，agent **正常完成**后停在非终态 `needs_review`，等人 `job accept`（→done）或 `job reject --note …`（→终态 `rejected`，workflow 按失败聚合、不会被自动重试/续投）。**只有人能 accept**：worker token 打 HTTP `POST /v1/jobs/{id}/accept|reject` 一律 403；MCP 只有 `gofer_reject_job`，没有 accept 工具。`job cancel` 对 `needs_review` 返回 409（改用 reject），`job resume` 也要求先把验收做完。
- 断线恢复：worker 断线时 job 进 `recovering`（`job list --status recovering`），窗口内同进程重连即恢复；serve 重启也一样。**recovering 不要重派。**
- **todo 联动（`--todo`，SUP-01 C / PLAN-02）**：`job run --todo <todo-id>` 把"跑一次活"和 checklist 上的那一项绑起来——`plan_id` 缺省从 todo 反查（显式 `--plan` 与 todo 所属 plan 不一致直接 400），提交成功后该项转 `doing` 并指向这个 job；终态时自动写回：`done` → 该项 `done` 且备注追加一行 `<job-id> ✓ N commits: <sha> <subject>; …`（最多 8 条，超出 `+N`；无提交写 `no commits`）、`needs_review` → 追加 `<job-id> 待验收`（accept 后再补 done 行）、失败/超时/取消/拒绝 → 状态不动、追加 `<job-id> ✗ <status>: <原因前 120 字>`。**失败只影响备注，不影响 job 本身**；`reject --resume` / 自动续投的新 job 继承 `todo_id`，整条链的每一轮都追加到同一项上。**PLAN-02 起这条联动不用人敲 `job run`**：todo 自己带派发字段（assignee + 模板/verify/review/runner/cwd/timeout/project），`ready` + 有 assignee 即出 job（见上「todo 指派 agent 即派发」）——job 行 `channel=plan`、`todo_id`/`plan_id` 都指向那一项。
- **提交采集**：job 开跑时在执行机记 `base_sha`（cwd 不是 git 仓则空；worktree job 用其基线），终态时 `git log base..HEAD`（上限 50，新→旧）写进 `commits`——`job show` 列出、web 详情页「提交」块可一键复制 sha、worker 上跑的 job 经 Outcome 回传后 host 行同样有。它独立于 `capture_diff` 开关，采集失败留空、不影响 job。
- **预算熔断**：`job run --max-tokens <50000|50k|1.5m> --max-cost <USD> --max-turns <N>`（HTTP/MCP `budget {max_tokens,max_cost_usd,max_turns}`、任务书 frontmatter `budget`、`plan add-todo|set-todo` 同名 flag、`job resume` 覆盖）——执行侧流式计量（claude 按 message.id 去重累加 / omp / ndjson_usage_path / acp usage_update / codex 事后），越线杀进程树，job `failed` + `failure_class=budget` + 事件 `job.budget_exceeded`（默认通知集），不自动续投/转移/重试；默认值 agent `budget:` < 项目 `budget:` < 请求；`job show` 打 `budget:` 行（含已用）；exec / pty / 文本 agent 显式带 budget 即 400；worker 需协议 v20（提交时 400）。详见 SKILL.md §5e3。
- **验证步骤（`--verify`，SUP-01 P2）**：agent 汇报不当验收——`job run --verify '<argv>'`（shell-words 拆成 argv，**不经 shell**；要 shell 就写 `bash -lc '…'`）在 agent **正常结束（exit 0）**后于**同一台执行机、同一 cwd/env**跑这条命令，独立超时 `--verify-timeout`（缺省项目 `verify_timeout_sec`，再缺省 600s）。结果：`passed` → job 按原逻辑；`failed`/`timeout` → job `failed`（exit_code 取验证退出码，timeout 为 -1）且**不是** transient（不触发自动续投/故障转移）；开了 `--review` 则停 `needs_review` 留人裁决；agent 自己失败/取消/超时 → `skipped`（不跑）。stdout+stderr 合并写进本 job 的 stderr 日志并夹两条横幅，web 详情页「验证」块可点击跳到输出，`job show` 打印 `verify: failed (exit 1, 12.3s)`。**需要项目 `allow_exec`**（argv 来自提交者，与 exec 同一信任面）；不想跑项目默认值就 `--no-verify`。**worker/peer 上跑的 job 由执行机跑验证**，结果经 Outcome 回传（不会在 server 上重跑）；协议 < v8 的 worker 会被**直接拒绝**（提示升级，不再静默忽略）。
- **故障转移（`--fallback` / `--no-fallback`，SUP-01 P3）**：agent 因**供应商错误**（`transient_error_patterns`，含内置 `at capacity|rate limit|429|stream disconnected|windows sandbox failed|connecting runner pipe` 等）挂掉、且它**自己也没法续**（无会话 / 续投额度用尽 / 已续过一次又挂）时，server 用一个**普通 job** 把这份活交给下一个候选 agent：以**链根那次的请求**重提（新会话、同一 cwd——源 job 在 worktree 里就继续在那个 worktree、不新建），prompt 前面加一段"上一次由 X 执行，因供应商错误（…）中断；先 git status / git log 看进度，只做剩余部分，不要重做已提交的工作"（exec 类请求不加前缀、原样重跑），标题追加 `(→omp)`，`plan_id`/`tags`/`timeout`/`read_only`/`review`/`verify`/`todo_id`/caller 全部继承。源 job 记 `job.fell_back {to_job, agent, reason}`（**不**记 `job.terminal`，IM 不该收到一条马上被接管的失败），源行 `fell_back_to` 指向新 job、新 job `fell_back_from` 指回源、`requested_agent` 记调用方原本要的 agent；链长 = 候选数，用尽即正常 `job.terminal`。候选来源：`--fallback` > 项目 `agent_fallbacks` > agent `fallback_agents`；**提交时解析并冻结**（`fallback_json`），运行中改配置不会让链条漂移；不在项目 `allowed_agents` 内的候选被跳过并 warn。失败归类 `failure_class`（transient|other|budget）无条件写入（与是否开启转移无关，健康度按它统计）。
- **同目录串行锁（JOB-11）**：同一个工作目录上不许有两个**可写 agent job** 同时跑——后者进新的非终态 `waiting_dir`（等同 `queued`：不占执行位、统计里并入 queued、可 `job cancel`），事件 `job.waiting_dir {holder_job, dir}` 点名持有者，`job show` 打 `waiting_dir: holder=<id>`，web 芯片灰蓝、悬停显示持有者。默认规则：cli-agent/acp-agent 且非 `--read-only`、非交互 = **独占**；exec 与只读 job **共享**（`git log`、`go test` 这类巡检不该排队）。`--exclusive-dir` / `--shared-dir` 反转，`server.dir_lock: false` 全局关闭。冲突判定是**目录重叠**（同目录、祖先、子目录都算同一把锁），按 `util.RealPath` 归一化（软链、Windows 8.3 短名视作同一目录），FIFO 排队。`--worktree` job **不取锁**：它的目录（`<顶层>/tmp/gofer/wt/<job-id>`）只属于它，取锁反而会让每个 worktree job 排在主 checkout 后面，正好废掉 WT-01 的并行。`agents.<key>.max_concurrent`（默认 0=不限）另给单个 agent 限流：超出的 job 停在 `queued`（不是 `waiting_dir`）。
- **输出停滞（AUTO-05）**：跑着的非交互 job 若 **N 秒内 stdout/stderr 都没有新增字节**（acp 的任何 `session/update` 也写 stderr，所以同样算），即判停滞：记事件 `job.stalled {silent_sec, stall_timeout_sec}`、杀进程、job `failed` 且 `error = "stalled: no output for Ns"`。这条 error 命中内置 transient 模式，所以**自动续投 → 故障转移链照常接管**（这正是它存在的理由：供应商流挂起不该白等 job 的 deadline）。N = `--stall-timeout` > `agents.<key>.stall_timeout_sec` > `server.stall_timeout_sec`（默认 900s）；**exec job 默认关**（构建/测试本来就会长时间无输出），交互 job 恒关。等人作答（`pending_interaction`）期间与 verify 阶段**不计时**，作答后从头计。`--no-stall` 关掉本 job 的看门狗。worker 上跑的 job 由 worker 自己的 job.Service 看门狗（它拥有进程），hub 只收 `failed` + error 后按既有规则续投/转移。
- **任务书模板（`-t/--var`，SUP-01 P5）**：`job run -t <name> --var k=v …` 让**服务端**把一份任务书渲染成 prompt。模板放在项目的 `<host_path>/.gofer/templates/<name>.md`（优先）或 server 的 `<config-dir>/templates/<name>.md`；frontmatter 可给 `agent/runner/model/timeout_sec/tags/verify/verify_timeout_sec/review/read_only/worktree/fallback_agents` 这些默认值（**显式旗标 > 模板默认 > 项目默认**，只填你没给的），变量声明写在 `vars:`。正文支持 `{{变量}}`、内置 `{{project}}/{{cwd}}/{{date}}/{{head}}` 与一层 `{{include: 同目录文件.md}}`。缺必填变量 → 400 并列出缺项；`request_json` 存**渲染后的 prompt** + 模板名/变量（重跑不再渲染）。`-t` 与 `-f`、与 post-`--` argv 互斥；`--prompt` 是**追加正文**。`gofer template ls|show` 看清单与预览（预览由服务端渲染，include/head 都已展开）。示例见仓库 `docs/examples/templates/`。
- **worker 侧事件镜像（SUP-01 G）**：worker 上跑的 job 的审批（`job.permission_requested|answered|timed_out`）与验证（`job.verify_started|finished`）事件现在会**镜像到 hub 的 job 事件表**（detail 带 `origin: worker:<id>`），所以 `job watch`/webhook 订阅这些事件对远端 job 同样生效。重复帧在 hub 侧按 `(job_id, type, ts, interaction_id)` 去重。
- **用量/成本（SUP-01 E）**：agent 自己报的 token/成本会落在 job 上（`usage`，入库 `jobs.usage_json`），`job show` 打一行 `usage: in 12.3k / out 3.8k / cache 289k / total 305k / $0.0032 (ndjson:omp)`，web 详情页有「用量」块，`GET /v1/stats` 的 `usage.windows` 与 Home「Agent 用量」卡按 agent 汇总 24h/7d。四路来源：`output_format: ndjson` 的 omp（最后一条 assistant 消息的 `message.usage`）/ claude（`result` 行的 `usage` + `total_cost_usd`）、codex `exec`（stderr 尾部的 `tokens used`，无成本）、acp-agent（`usage_update` 事件）。**采集全是 best-effort**：agent 不报或解析不出来就没有一行（不是 0），`usage.source` 说明这串数字从哪来；远端 job 由执行机采集后随 Outcome 回传。usage 行只列 agent 真报了的项（缺项省略，`total` 缺省时后端按四项求和）；`agent status` 表的 `24H_TOKENS` / `24H_COST` 两列取同一份 `/v1/stats` 24h 窗口，窗口内没采集到就是 `-`。
- ACP 的 `tool_call.content`（包括 diff 的 `oldText`/`newText`）和 `locations` 会进入 `acp.jsonl`、ACP SSE 与 job detail 的 stderr 工具事件，并受各自现有事件大小上限约束。

## tunnel（别名 `tun`）— 经 worker 的 TCP/UDP 端口转发

```bash
gofer tunnel forward -w <worker> [udp/][bind:]lport:host:port …   # 本机端口 → worker 所在网络的目标; 可多条
gofer tunnel forward --name <preset>                              # 用 tunnel save 存的预设
gofer tunnel save <name> -w <worker> <spec…> [--note …] / saved / forget <name>
gofer tunnel check -w <worker> [udp/]host:port                    # 只验 worker 能否建到目标的 socket(UDP 不代表设备会应答)
gofer tunnel ls                                                   # 在线转发进程(FORWARDERS, 含 STOP 列) + 活动隧道(CONNECTIONS)
gofer tunnel stop <forwarder-id>                                  # 让任意机器上的 tunnel forward 进程退出(下次心跳 ≤30s)
gofer tunnel presets push                                          # 上传本机 tunnels.yaml 预设（同名按既有规则跳过）
```

Web 的 Tunnels 页面可对 server 预设执行“启动/停止”，启动的是 server 本机 hosted forwarder；也可导入本机 `tunnels.yaml` 中尚未上传的单条预设。托管转发不走普通 forwarder TTL，server 关闭会停止；`autostart: true` 只保证监听恢复，worker 尚未重连时不会阻塞 server 启动，首次连接仍按普通转发报告拨号错误。

- forward 的日志：默认 `<config-dir>/run/tunnels/forward-<时间>-<pid>.log`（`--log-file`/`--log-dir` 改；`--quiet` 只静默终端）；事件带 `tunnel_id`（与 server/worker 日志同一个）、`session_id`（UDP=本地来源地址）、`dial_ms`、`first_byte_ms`、`bytes_up/down`、`packets_up/down`（UDP）、`close_reason`。`GOFER_TUNNEL_TRACE=1` 逐报文记 `tunnel.datagram`（`dir/len/gap_ms`）。
- 远程停止外部 forward 进程：`gofer tunnel stop <fw-id>` 或 web Tunnels 页在线转发行的「停止」（行内确认，显示「停止中（≤30 秒内退出）」）。hub 只做标记，进程下一次心跳收到 410 后退出码 0 退出、不重新登记；仅新版 forward（登记带 `caps: ["stop"]`，`tun ls` STOP 列 `remote`）支持，旧版（`ctrl+c-only`）409，只能去那台机器 Ctrl+C。权限：登记者本人或 `can_admin` caller；job 凭证拒绝。HTTP：`POST /v1/tunnels/forwarders/{id}/stop` → 202。删除登记（DELETE）不能停进程——它会在下次心跳 404 后重新登记。
- 目标必须在 worker 的 `tunnels.allow` 白名单内；判读"慢在哪"见仓库 `docs/runbook/tcp-tunnel.md`。

## tool — 小工具（XFER-01 文件传输）

小工具类命令统一挂在 `gofer tool` 组下（G033）：文件传输（`cp` / `xfer`）、本地 HTTPS 证书（`cert`）与 Dashboard 指标补算（`stats-backfill`），后两者见本节末：

| 命令 | 作用 |
|---|---|
| `gofer tool cp <src> <dst> [--force] [--timeout 600]` | 单文件对拷，**恰一端是远端**：远端写法 `<runner>:<project>/<相对路径>`（runner = worker id 或 `server`／`local`）；恰一端是本地路径 |
| `gofer tool xfer ls [--state staged\|dispatched\|done\|failed\|expired] [--runner <id>]` | 列暂存区（新→旧） |
| `gofer tool xfer show <id>` | 单条详情：op / runner / project:path / size / sha256 / state / error / 时间 |
| `gofer tool xfer rm <id>` | 立即删掉该条（暂存文件 + 记录） |

传输 id 自 XFER-02 起是 **`xf-<8hex>`**（11 位，与唤醒 `wk-` 同风格）；旧行仍是 32 位 hex，照常可读——没有任何地方解析 id 形态。

```bash
gofer tool cp ./firmware.bin w-plc:shop-floor/tmp/in/firmware.bin   # 推到 worker 项目目录
gofer tool cp w-plc:shop-floor/tmp/out/report.csv ./report.csv      # 从 worker 拉回
gofer tool cp ./x.tar server:build/tmp/x.tar                        # 目标是 server 本机
```

要点：

- 路径按**执行机**的项目根解析（`SafeJoin`，POLICY worker 经 roots 映射），**只允许项目根内**，与 `job run --cwd` 同一边界；目标目录不存在会自动创建；目标已存在必须 `--force`。
- **v1 单文件、无断点续传**：目录先 `tar czf` / `Compress-Archive` 打包；大小上限 `server.xfer.max_bytes`（默认 256MB），worker 侧单次传输超时 `xfer_timeout_sec`（默认 600s）。
- **推送前先预检**：`POST /v1/xfer/precheck`（同 meta JSON）先校验 runner 在线/协议、项目、路径与大小上限，**通过才读文件**——逃逸路径、离线 worker、超限大小在上传前就报错，不会传到一半才 400。服务端 multipart 也要求 `meta` part 先于 `file`、且在读 file 前完成同一套校验。
- **推送**：本地算 sha256 → multipart 上传到 server 暂存（打印进度）→ 轮询到 `done`/`failed`；**拉取**：先建 get 记录 → worker 读文件回传到暂存 → `GET /v1/xfer/{id}/content` 落本地（临时名 + rename，校验 sha256）。
- 退出码：成功 0；失败 1 并**原样**打印 `error`（`exists`、`path escapes project`、`worker offline`、`too large` …）。
- worker 离线不会排队：直接 failed（`worker offline`），重跑命令即可。
- 不做内容审查：别传 `.env`/私钥/token。

### `gofer tool cert` — 本地 CA + HTTPS 证书

```bash
gofer tool cert --out-dir ./tmp/certs --hosts gofer.local,192.168.1.20   # --out-dir 默认 <config-dir>/certs
```

生成 `ca.crt/ca.key/server.crt/server.key`（已有 CA 则复用、只重签服务器证书；SAN 取 `--hosts`，访问用的 IP/主机名必须在里面）。再在 server 配置里加 `server.tls: {addr, cert_file, key_file}`（另开 HTTPS 监听，HTTP 不变，需重启）。Android：把 `ca.crt` 装成「CA 证书」后用 `https://<IP>:<port>` 打开并「安装应用」即独立窗口 PWA（Web Push 也要求 HTTPS）。证书/私钥不入库、不进日志。步骤见仓库 `docs/runbook/https-pwa.md`。

### `gofer tool stats-backfill` — 补算 Dashboard 的 job 指标

```bash
gofer tool stats-backfill                  # 所有结束的 job 里还没有指标的
gofer tool stats-backfill --since 30d      # 只补近 30 天结束的（也可 12h / 2026-09-01）
gofer tool stats-backfill --force          # 已有指标的也重算
```

Dashboard 统计页（`GET /v1/stats/overview`）的 per-job 指标存在 `job_metrics` 表：job 结束时自动写入，这个命令给**升级前就结束的老 job** 补算。server 侧分批执行（`POST /v1/stats/backfill {since, after_ended, after_id, limit, force}` → `{scanned, written, failed, with_signal, with_git, after_ended, after_id, done}`，每批 ≤15s，CLI 按游标循环；`--limit` 每批 job 数，默认 100、上限 1000）；只接受人（user）凭据。**幂等**：不加 `--force` 时只处理没有当前版本指标的 job，重跑是空操作。补算来源：`job_events`（acp 每轮摘要的轮次 / 工具调用、会话 job 的轮次与追加消息）、`interactions`（人回答次数、等人时长）、`commits_json`、`usage_json`、仍在的结果目录 `stderr.log`（claude / omp 的紧凑事件里的轮次与工具调用）、git 行数（worktree job 取已存的 diff 摘要；其他 job = `git diff --shortstat base 最新提交`（仓库里还有这些提交时）+ 已存的未提交 diff 摘要）。找不到来源的字段留空（页面显示「—」），不按 0 记。

## session（别名 `sess`）— 终端会话中继（web ↔ 终端）

终端里的 Claude Code / Codex 会话经 hooks 登记到 server；会话的 **relay 开关**打开时，Stop hook 把 agent 最后一条消息发成一个 turn 并阻塞等待，人在 web「会话」页 / 「待我决策」/ CLI 作答，答案经 `decision: block` 注入同一会话继续（设计 SESS-01）。

```bash
gofer init hooks [--agent claude|codex|omp|jcode|all] [--global] [-o <dir>] [--remove] [--force]
#   合并写 ./.claude/settings.json、./.codex/hooks.json、omp 扩展 ./.omp/extensions/gofer-relay.ts、jcode ~/.jcode/config.toml [hooks](--global 写用户级; jcode 无项目级, -o 视为 JCODE_HOME 目录); 幂等、只增删 gofer 自己的条目
#   --agent all = 四个都写; 全局安装后会列出已有项目级 gofer hooks 与清理命令(`--remove --agent X -o <项目>`)
#   Codex 另需 config.toml [features] hooks = true(旧名 codex_hooks), 且项目 .codex/ 需 trust
gofer session ls [-p <project>] [--state waiting_reply|…|offline] [--all]   # 列会话(waiting_reply/needs_attention 置顶; offline = 无心跳太久,进程可能已退出, 默认显示, --all 才多出 ended)
gofer session show <id>                 # 详情 + 最近 turn(id 可用前 8 位)
gofer session relay auto|on|off [--session <id>]  # 省略 --session: 按当前目录反查(歧义时列出候选); auto = 缺省
gofer session say <id> "<回复>"         # 答最新 OPEN turn; "/off" = 关中继放行
gofer session say <id> "<回复>" --deliver   # 选路: 有 OPEN turn 就当作答, 否则敲进该会话的 tmux pane(§9.1 A)
gofer session say <id> "<回复>" --deliver --takeover   # 没有 tmux 时起新进程 `--resume` 接管该会话, 这条消息作首条输入(§9.1 B)
gofer session release-takeover <id>     # 解除接管: cancel 接管 job → 会话回 idle(本来已 ended 的回 ended), 原终端恢复中继(未接管 → 409)
gofer session resume <id> [--input "首条消息"] [--plan]   # 唤醒会话: 起新进程 `--resume` 接管(已结束的会话也行); --plan 只看能不能/怎么起(含目录依据 cwd_abs/cwd_source/cwd_reason: 优先用 transcript 验证出的原始启动目录，其次登记 cwd，再次项目根)
gofer session watch <job-id> [--session <id>] # 登记当前会话盯住 job；省略 --session 按当前目录解析
gofer session rm <id>                   # 移除登记(turn 保留)
gofer hook generic --agent <key> [--wait N]   # 自研 agent 的 hook 执行体(stdin 与 claude 同形; 会话以 <key> 登记, 续接/接管按该 agent 的模板); 其余 agent: gofer hook claude|codex|omp|jcode
gofer hook claude|codex|omp|jcode [--wait N]   # hook 执行体(由 hooks 配置调用, 人不直接用; jcode 从 JCODE_HOOK_* 环境变量读事件); 日志 <config-dir>/run/hook.log
```

要点：

- 开关在 server、按会话、**三态**（`agent_sessions.relay_mode`）：`on` 每次停下都等；`off` 从不等（已打开的 turn 释放）；`auto`（缺省）由 server 判定。hook 每次 Stop 先问 server（heartbeat 返回 `wait_reason`），不等则零阻塞，server 不可达也直接放行，**永不卡死终端**。
- **自动布防（`auto`，两条判据；`0` 分别关闭）**：
  1. **键盘空闲**（`session.auto_relay_idle_sec`，默认 300s）：hook 每次 Stop / Notification(idle_prompt) 上报"键鼠空闲秒数"（Windows `GetLastInputInfo`、macOS `ioreg HIDIdleTime`、Linux `xprintidle`；取不到或超 ~200ms = 未知）。空闲 ≥ 阈值 ⇒ 停下就发 turn 并在 web 等回复（`wait_reason=idle_probe`，列表显示 `auto (idle 12m)`）。
  2. **距上次人工输入**（`session.auto_relay_turn_sec`，默认 900s，R2）：**探测不到键盘时**（容器/无 X11，`idle_sec` 恒为 -1）改用本会话的 `last_human_at`（SessionStart、以及非注入的 UserPromptSubmit 会刷新），距今 ≥ 阈值 ⇒ 同样布防（`wait_reason=turn_age`，列表显示 `auto (no input 22m)`）。这条是为"键盘在主机、hook 在容器里"的场景准备的。
  - 探测成功时以判据一为准，不会再用判据二猜；`last_human_at = 0`（没见过人工输入）不构成布防理由。
- **人回来即放行**：判据一的等待，hook 每轮（≤5s）重探空闲值并报告给 server，空闲 < 阈值 ⇒ turn 关成 `EXPIRED` + `released_by=user_returned`。判据二的等待没有可探的读数，靠人的动作本身：按 Esc（hook 被杀，等价放行）或在终端输入一条 —— `UserPromptSubmit` / `Interrupt` 事件到达时 server 把该会话的 OPEN turn 关成 `EXPIRED` + `released_by=user_returned`，`hook.log` 里该轮随即 `turn expired, released`。**显式 `on` 的等待不受"终端输入"影响**——输入只把 mode 降回 `auto`，turn 由 web `/off` 或 `session relay off` 结束；**Esc 例外**：Esc 会让 Claude Code 用信号终止阻塞中的 Stop hook，hook 临死前上报 `Interrupt`（`hook.log`：`interrupted by the terminal while waiting`），server 把 turn 关成 `EXPIRED` + `released_by=interrupted`、会话回 `idle`，开关保持 `on`，下一次 Stop 仍中继。
- `UserPromptSubmit`（人在终端输入）在 `on` 下把 mode 降回 `auto`（"人回到键盘就交还给自动判据"）；web 注入的回复虽也触发该事件，但带 `[gofer web 回复]` 前缀，hook 上报 injected，不会动开关，也不会被当成"人回来了"。
- Stop hook 等待期间终端显示 hook 运行中；人回到电脑想直接输入可按 Esc 取消（hook 会先通知 server 结束这一轮等待，开关不变）。
- 硬边界：会话已停在空闲提示符、且人从未离开过的场景没有 hook 进程活着，web 拨开开关要等下一次 Stop；需终端输入一次（人离开过则由自动布防覆盖）。
- **无 OPEN turn 时送话（阶段 2-A，tmux 注入）**：`POST /v1/sessions/{sid}/deliver {text}`（CLI 是 `session say --deliver`，web 是抽屉里变成「送到会话」的输入框，依次尝试 agent 的 deliver_command（socket 等）与 tmux）先看有没有 OPEN turn —— 有就等价 `say` 作答（`path=turn`）；否则派一个内部 exec job 到该会话的 runner：`tmux display -p -t <pane> '#{pane_current_command}'` 确认 pane 存活且前台是 agent CLI（白名单默认 `claude|codex|omp|node|gemini|opencode`，`session.inject_commands` 可配），再逐行 `tmux send-keys -t <pane> -l -- '<行>'` + `Enter`；文本前缀 `[gofer web 回复] `、上限 8KB、pane 与文本都按 shell 单引号转义。成功：`{path:"tmux", job_id, decision_id}`，会话置 running，审计行 `plan_decisions(kind=relay, detail={"path":"tmux","job_id":…})`。
  - 失败码（HTTP 状态）：`no_runner` / `no_tmux` / `ended` → 409（原因码写在错误信封的 `error` 字段，如 `deliver failed: no_tmux`）；`inject_failed:pane_missing|pane_busy:<cmd>|runner_error` → 502；空文本 / 超 8KB → 400。**worker token 不能送话**（403）。
  - 前置条件：会话跑在 tmux 里 + 登记了执行机（容器里要在容器内起 worker 并配 `GOFER_HOOK_RUNNER=<worker-id>`，纯客户端节点不再假装登记成 `server`）；注入 job 是 exec 类型，project 需 `allow_exec: true`。
- **无 OPEN turn 且没有 tmux 时送话（阶段 2-B，`--resume` pty 接管）**：`POST /v1/sessions/{sid}/deliver {text, allow_takeover: true}`（CLI `session say --deliver --takeover`，web 是 A 报 `no_tmux` / `pane_missing` 后出现的「起新进程接管并发送」+ 二次确认）。server 在**同一 runner、同一项目相对目录**起一个交互 pty job：argv = agent 的交互 resume 模板（`claude --resume <sid>` / `codex resume <sid>` / `omp --resume <sid>`，经 `PlanTakeover` 由宿主解析），`InitialInput = "[gofer web 回复] <文本>\r"` 由 pty 在**首次输出后安静 `session.takeover_input_delay_ms`（默认 1500ms，最多等 10s）**再写入子进程 stdin 并记 `job.input_injected` 事件（worker 路径经 `wsproto.Dispatch.initial_input`，协议 v7）。成功：`{path:"takeover", job_id, decision_id}`，会话置 `handed_off`（`handed_off_job_id`/`handed_off_at`），审计 `detail={"path":"takeover","job_id":…}`，通知事件 `session.handed_off`（不在默认集）。
  - `allow_takeover` 缺省 false：不带它时 server 停在 A 的 409 `no_tmux`（接管会把会话从原终端移走，必须显式要）。
  - A 的三类"救不回来"失败会自动改走 B（仍需显式 `allow_takeover`）：`no_tmux`（没有 pane）、`inject_failed:pane_missing`（pane 没了）、`inject_failed:runner_error`（注入 job 根本没跑起来 —— runner 不可达/脚本自己挂了）。**`pane_busy:<cmd>` 不改走 B**：那个终端正在被人用。
  - 前提与失败码（409）：`no_resume_template`（agent 无交互 resume 模板）、`interactive_not_allowed`（项目未开 `allow_interactive`）、`cwd_outside_project`（cwd 换算不到执行机上的项目相对路径，POLICY roots 映射的已知限制）、`handed_off:<job>`（已被接管）；派发失败 → 502 `inject_failed:runner_error`。接管 job 是 exec 载体但按**源 agent** 过访问门（与 job resume 同一豁免），不需要 `allow_exec`。
  - 接管后原终端：`wait_reason` 恒空、`OpenTurn` 拒绝（头一次 Stop 起就以 `ErrRelayOff` 放行），心跳响应带 `notice`，hook 在 `UserPromptSubmit` / `Stop` 把它打到 **stderr**（"该会话已于 <时间> 在 web 接管（job <id>）…本终端的中继已停用"）。原终端的 Stop / UserPromptSubmit / Notification / SessionEnd 都不会把会话从 `handed_off` 改回去。
  - **解除接管**：`POST /v1/sessions/{sid}/release-takeover`（CLI `gofer session release-takeover <sid>`；web 抽屉「解除接管」）→ 先 cancel 接管 job，再置 `idle` 并清空接管标记；cancel 失败返回 502（不会假装成功）。会话未接管时 409。**接管 job 自己结束时 server 会自动释放**（终态钩子：置 idle、清 `handed_off_*`、事件 `session.takeover_released {job_id, reason: job_<status>}`，可订阅），所以"跑完就卡在已接管"不会发生；人在 job 还在跑时点解除仍走上面的 cancel 路径。
  - 监控：`gofer job ls --tag relay-takeover` / `gofer job show <id>`（`job.input_injected` 记录首条输入的字节数与安静窗口）。
- **会话催办（N2 §E，SESS-12）**：`gofer session nudge <sid> (--every <dur> | --when-stalled <dur>) -m "<text>" [--until <time>]`（间隔最小 1m；`--until` 接受 `2h` / `3d` / `2026-10-08 09:30` / RFC3339）；`nudge ls [<sid>] [--all]`（不给 sid = 所有会话；`--all` 含已结束）、`nudge rm|pause|resume <id>`。REST：`POST/GET /v1/sessions/{sid}/nudges`（POST body `{kind: every|stalled, interval_sec, text, until_at?}`；GET `?all=1` 含已结束）、`GET /v1/nudges`、`PATCH /v1/nudges/{id} {state: paused|active}`、`DELETE /v1/nudges/{id}`；web 会话抽屉输入框上方有「催办」小区块（列出 / 新建 / 暂停 / 恢复 / 删除）。server 每 30s 扫一次（表 `session_nudges`，additive）：`every` 到点就发；`stalled` 要会话 `running`（或 `idle` 且关联工作项未结——非 done/dropped/parked）且「最后进展」距今 ≥ 阈值，最后进展取 `last_seen_at`（任何 hook 心跳含 Stop/子 agent）/ `progress_at` / 用量增长时间 `usage_at` 中最近者，另以该 nudge 上次发送时刻起算，所以同一次停滞每个阈值周期只催一次。发送走与 web「发消息给会话」同一条送达阶梯（等回复的 turn 直接作答 → 传话人 → deliver_command / tmux），operator 为 `gofer-nudge:<id>`，会出现在会话 outbox。**连续送达失败 3 次自动 `paused`**（成功即清零；`resume` 清零重来）并发通知事件 `session.nudge_paused`（**不在默认通知集**，要订阅写进 webhook `events`）。会话 `ended` / `handed_off`、或到 `--until`，nudge 自动 `ended`；`offline` 的会话跳过（不计失败）；`rm` 会话时一并删除。**权限**：只有人（user / admin，且是会话属主或 `can_answer`）能建、改、删；worker token、job 凭证（member / leader / **steward**）一律 403——管家不拍板，也不替人设定时器；steward 连读都不行（读白名单不含 nudges）。
- turn 复用决策通道：「待我决策」里的「会话等回复」卡可直接回复或标已读（同一会话只出一张卡）；`gofer plan decisions --state OPEN` 也能看到（kind=relay；被"人回来"关掉的 turn 是 EXPIRED + `released_by=user_returned`）。

- **hook 在 gofer job 内自动放行**：环境里有 `GOFER_JOB_ID` 时 `gofer hook` 直接 bypass（日志 `bypass relay: GOFER_JOB_ID is set`），不登记会话、不开 turn、不阻塞。中继只针对人手开的终端会话。
- **`session watch <job-id>`**：把当前会话登记为盯住该 job；会话停下并在等待时，job 终态通知注入回终端（`--session` 省略则按 cwd 解析）。中继关闭时 Stop 仍会只等这些 job 的事件（不转发 web 输入），错过的通知由 UserPromptSubmit / SessionStart 补投一次（claude / codex）。你名下有 job 在跑、或会话里有子 agent 在跑（claude 的 SubagentStart/Stop hook，`gofer init hooks` 装）时 `auto` 不布防（`session.auto_relay_skip_when_supervising`），以免自己的 Stop 压住完成通知；Stop 阻塞等回复的时长另受 server 下发的 `wait_budget_sec` 封顶（`session.relay_on_wait_sec` 默认 3600 / `relay_auto_wait_sec` 默认 600，`0` = 不封顶），hook 取 `min(--wait, 它)`。cwd 在 `~/.codex/memories` 或 `GOFER_HOOK_IGNORE_CWDS` 的 agent 内部会话，hook 直接忽略。Stop 自动补认领只选 `source_session_id` 与当前会话 SID 相同、且 caller、project、runner、cwd 均匹配的在途 job；同 caller 的其他会话不会分走它。旧 job、缺少 source SID 或上下文不匹配的 job 不会仅因 caller 相同而自动加入 watch；仍可显式用 `session watch` 关联 job。
- 已登记 session 的重复注册与 heartbeat 均要求 authenticated caller 精确匹配登记 owner；body caller ID / `can_answer` 不代表 hook session 身份。worker token 只能更新 caller ID 本身登记为该 worker 的会话，runner 相同不授予身份；ownerless legacy session 可由首个认证注册或心跳补上 owner，空认证 caller 不能心跳；unknown heartbeat SID 返回 404。
- **web → 终端会话「发消息」（转达，Y6/P5）**：会话不在等回复时，server 在会话所在 runner 上经**传话人**（`claude -p … --allowedTools SendMessage,ListAgents`，一次性 exec job 或常驻 stream-json 进程；常驻在本机 runner 与协议 ≥ v14 的 worker 上，更旧 worker 退回一次性）把原文转给目标会话，正文前缀 `[来自 web，<用户>]`。消息先写 `<storage.root>/sessions/<sid>.outbox.jsonl`，在会话对话流里显示为「你（经转达）」并带送达状态（排队/已送达·通道/失败·原因·可重试）。**接收方看到的是"另一个会话转达的消息"，不是用户本人的指令或审批**——要拍板/授权请在中继里答（`session say`）或终端输入。需要目标会话有 Claude Code 会话间通信（hook 从 `~/.claude/sessions/<pid>.json` 读到名称与通信地址上报）。配置 `server.session_messaging`（见 [`server-config.md`](server-config.md)）。`gofer worker show` / Runners 页可看传话进程状态。
- **唤醒会话（含 ended）**：`GET /v1/sessions/{sid}/takeover-plan` 是干跑：`{can, reason, message(中文), warning, state, ended, runner, agent, project_key, cwd(项目相对), command}`，能不能都是 200（`can=false` + `reason` 是数据，未知会话 404）。`POST /v1/sessions/{sid}/resume {initial_input?}` 真起进程（和接管同一种交互 pty job，tag `relay-takeover`；只允许 user caller），返回 `{path:"takeover", job_id, decision_id}`；失败是 409 `resume failed: <reason>`（`no_runner` / `handed_off:<job>` / `no_resume_template` / `interactive_not_allowed` / `cwd_outside_project`）或 502 `resume failed: inject_failed:runner_error`，`detail` 是中文原因。**ended 会话可以唤醒**（`deliver` 仍拒绝 ended）；成功后会话 `handed_off`，接管 job 终态/解除接管后：原本 ended 的回 `ended`，其余回 `idle`。`GET /v1/sessions` / `GET /v1/sessions/{sid}` 的会话对象每个都带 `can_resume` / `resume_reason` / `resume_message`。
- **传话人快照与会话列表**：`GET /v1/runners` 的行带 `messenger`（状态串 `stopped|idle|busy`）、`messenger_detail`（`status`、`started_at`、`last_used_at`、`idle_deadline`（Unix 秒）、`deliveries`（最近 20 条，新的在前：`at/op(send|list_agents)/target/message(<=200字)/ok/error/duration_ms`）、`stderr_tail`）和 `dirs`（`workspace{path,exists}`、`roots[{from,to,exists}]`、`projects[{key,path,exists}]`）；worker 行的这三项随心跳（协议 v16）上报，缺省 = 未上报（旧 worker）。`GET /v1/runners/{name}/messenger/agents[?refresh=1]` 列出传话人能看到的会话（30 秒缓存；worker 经内部传话 job + `MessengerDispatch.op=list_agents`，门槛 `wsproto.MessengerListMinProtocolVersion=16`，旧 worker 409 中文提示；peer-http 409；未知 runner 404）。`GET /v1/workbench/threads?all=1` 同 job 列表一样取消内部传话 job 的隐藏。
- **「无需回复」（ack）**：对 OPEN 的等待 turn，web 可标「无需回复」（`POST /v1/sessions/{sid}/turns/{id}/ack`，`DELETE` 撤销）。仅标已读：turn 仍 OPEN、hook 继续阻塞、仍可之后 `session say` 作答，只是从「待我决策」队列里隐去。要放行用 `/off` 或 `relay off`。

## work（别名 `wk`）— 工作项（W1）

一张卡 = 一件事，跨会话；会话首次有人提问时自动建草稿。完整说明（状态映射、人工优先、搁置 / 提醒、每日摘要、合并 / 拆分、MCP / REST）见 SKILL.md §13。

```bash
gofer work ls [--status s1,s2] [--project p] [--workspace dir] [--query q] [--unsorted] [--due] [--all] [--limit N] [--json]   # --all 含 done/dropped 与被合并源项（标「已并入 <id>」），与 `work rm --status` 预览同一口径
#   列：ID STATUS SESS SEEN FLAGS TITLE；FLAGS: U=未整理草稿 D=提醒/搁置到期 O=会话已离线；末行 needs_me/due/open 计数
gofer work show <id> [--json]               # id 可用唯一前缀
gofer work new <title> [--goal --status --blocker --blocker-kind --next --summary --project --workspace --priority N] [--session <sid>]...
gofer work set <id> [字段同 new] [--title t] [--auto] [--sorted] [--rev N]   # `-` 清空字段；--auto 把状态交还给会话自动判定；--rev 乐观锁(过期 409)
gofer work note <id> <text>
gofer work park <id> [--until <时间>] [--note <条件>]   # 至少给一个；status=parked
gofer work remind <id> <时间> | --clear
gofer work report <id> [--goal --status --blocker --next --summary] [--session <sid>]   # 会话自汇报；--status active = 阻塞已解除
gofer work link <id> (--issue|--plan|--todo|--job <ref> | --session <sid> | --acp <job-id>) [--rm]   # --acp：ACP 持续会话 / 终端 job 的 job id
gofer work to-todo <id> [--plan <plan-id> | --new-plan <标题>]   # 转 plan todo 并回链；只能转一次（再转 409）
gofer work merge <id> <src...>
gofer work split <id> <title> [--goal g] [--session <sid>]... [--keep]
gofer work rm <id>... [--yes]               # 永久删除 done/dropped 的项及附属数据；无 --yes 只列出并非 0 退出；人专用（无 MCP、job/管家凭据 403）
gofer work rm --status dropped|done [--dry-run] [--yes]   # 删除该终态下全部；--dry-run 只列出
gofer work digest [--send]                  # 预览今日摘要；--send 立即作为 work.digest 通知发出
```

时间写法：`2h` / `90m` / `3d` / `1w` / `tomorrow`（次日 09:00）/ `2026-10-08` / `"2026-10-08 09:30"`（本地时间）/ RFC3339 / unix 秒。

## steward（别名 `stw`）— 工作管家（W2b）

常驻的持续 ACP 会话，只调度和整理工作项（不能完成 / 放弃、不能提交 job / 改配置）；机制、白名单与配置见 SKILL.md §13「管家」。

| 命令 | 说明 |
|---|---|
| `gofer steward status [--json]` | 开关、agent、会话状态（未启动 / 运行中 / 空闲）、笔记版本与大小、待处理事件、最近一次巡检与点评；digest / 管家开启但无 webhook 订阅 `work.digest` 时多一行 WARN（`GET /v1/steward` 的 `warnings`） |
| `gofer steward start` / `restart` / `stop` | 启动 / 结束会话后用最新数据重建 / 结束会话；需要时本来也会自动启动 |
| `gofer steward ask "<问题>" [--no-wait] [--timeout N]` | 提问（未启动自动启动，首条带 prime）；默认等回复并打印，`--no-wait` 只返回会话 job id |
| `gofer steward notes [--version N] [--history] [--edit] [--set-file <f\|->]` | 看 / 编辑管家笔记（版本化，`--edit` 开 `$VISUAL`/`$EDITOR`，冲突提示合并） |
| `gofer steward review [--force]` | 立即巡检：只处理有变化的工作项，没变化不起会话 |
| `gofer steward merges` / `merge-accept <n>` / `merge-dismiss <n>` | 管家记下的合并建议；采纳才会真正合并 |

## schedule（别名 `sch`）— 定时 job

```bash
gofer schedule add <...job请求...>      # 从一个 job 请求建定时计划
    # --cron '*/5 * * * *' | --delay 30m | --at <RFC3339|unix秒> 三选一
gofer schedule list / show <id>
gofer schedule enable <id> / disable <id>
gofer schedule run <id>                 # 立即跑一次(不等下次触发)
gofer schedule add ... --webhook        # AUTO-02b: 额外开一个"外部 webhook 触发"入口, 服务端发一个 token
gofer schedule rotate-token <id>        # 换一个 token(旧 token 立即失效); 没开 webhook 的计划也可用它开启
gofer schedule rm <id>
```

- **webhook 触发**（AUTO-02b）：`--webhook` 建的 schedule 带自己的 `trigger_token`（24 字节 base64url，`schedule show` 可再看到），外部系统无需 gofer bearer 即可触发：

  ```bash
  curl -X POST 'http://<server>/v1/schedules/<id>/trigger?token=<token>'
  curl -X POST -H 'X-Gofer-Trigger-Token: <token>' 'http://<server>/v1/schedules/<id>/trigger'
  ```

  语义 = `schedule run`（立即跑一次，不改 `next_run_at`），并在新 job 上记事件 `schedule.triggered {source: "webhook"}`。token 错/缺 → 401，计划不存在 → 404，同一计划 10s 内重复触发 → 429（防重试风暴）。
- **事件**（plan/agent 侧的新事件同理可订阅）：`agent.degraded {agent, transient_fail, window_sec, last_error}` 与 `agent.recovered {agent, window_sec}` 记在 agent 作用域（`agent:<key>`），用于"某个 agent 开始/停止抽风"的告警；不在通知默认集，需要显式写进 webhook 的 `events`。

## agent — 看 agent、验活 agent（SUP-01 P3）

```bash
gofer agent list                                  # 列配置/内置 agent(client 模式默认读 server)
gofer agent detect                                # 跑 detect 命令报告可用性(缺 CLI 不算失败)
gofer agent show <key>                            # 看某个 agent 的配置
gofer agent status [key]                          # 可用性 + 健康度 + 24h 用量: health/1h job 数/成功/供应商错误/上次供应商错误时间/24h tokens/24h $
gofer agent probe <key> [-p <project>] [--timeout 120]   # 提交一个探针 job 验活: 只回复一行 OK, 打印结果, 退出码 0/1
```

- **健康度**：按 `jobs` 表聚合（窗口 `server.agent_health.window_sec` 默认 3600s）：窗口内**没有样本 = `unknown`**（不是"健康"）；`transient_fail >= degraded_after`（默认 3）且最近一次供应商错误之后的成功数 `< recover_after_ok`（默认 1）→ `degraded`；否则 `healthy`。`needs_review` 计入成功（活是交付了的）。web「Agents」页每行有徽标（绿/橙/灰，橙的 title 写明窗口内几次供应商错误），旁边「探针」按钮调 `POST /v1/agents/{key}/probe`。
- **探针**是个**普通 job**：固定 prompt、`tags: [probe]`、`title: probe <key>`、超时默认 120s，`--sync` 等它跑完并返回 `{job_id, status, exit_code, duration_ms, first_line}`；因此它天然计入健康度，也能从 web 跳到那个 job。`-p/--project` 缺省取**第一个允许该 agent 的项目**（按 key 排序，稳定）；没有项目允许它 → 400，未知 agent → 404。exec agent 的探针跑一句 `echo OK`（exec 请求自带 argv，没有 CLI 可问）。
- **提交即改派**（`server.agent_fallback.pre_dispatch: true`，默认关）：提交时主 agent 处于 `degraded` 且候选里有非 degraded 的 → 直接改用第一个这样的候选，记事件 `job.agent_substituted {from, to, reason: degraded}`，行里 `requested_agent` 保留你原本指定的 agent。默认关是为了不出现"我明明指定了 codex 却跑了 omp"这种意外。

## template — 任务书模板（SUP-01 P5）

```bash
gofer template ls [-p <project>]                       # 列模板: name / source(project|global) / desc(解析失败会标 INVALID)
gofer template show <name> [-p <project>] [--var k=v …] # 看来源路径 + 变量表(必填标 *) + 服务端渲染后的正文预览
```

- 模板是 **server 上**的文件：`<project host_path>/.gofer/templates/<name>.md` 优先，其次
  `<config-dir>/templates/<name>.md`（`GOFER_CONFIG_DIR`）。同名项目副本赢；没有 `-p`（且当前目录
  探测不到项目）时只列全局目录。
- `show` 打印的正文就是**提交时会发出去的那份**：渲染在服务端做（`include` 展开、`{{head}}` 按将要
  执行的项目解析），`--var` 以 `?var=k=v` 传给 `GET /v1/projects/{key}/templates/{name}`。
- 变量表里 `*` = `required: true`（没给值就提交不了）；`default=` 是缺省值。预览里的 `warning:` 行说明
  哪些占位符没解析（未声明的变量、非 git 仓里的 `{{head}}`、被 include 的文件里的二层 include）。

## project（别名 `p` / `proj`）

```bash
gofer project list [--remote]           # 不带=本地(按 GOFER_RUN_MODE 读 config.yaml/worker.yaml); --remote=server 实时 project(client 模式默认即 --remote)
gofer project show <key>                # project 详情(client 模式来自 server 的 /v1/meta)
gofer project validate <key>            # 校验路径/agent/runner(别名 check)
gofer project add / remove <key>        # 注册 / 移除(client 模式拒绝: 需本地配置)
```

- server 配置没声明 `default` 项目时，本地 `project list` / `--remote` 都会列出内置 `default`（行尾标 `[内置]`）；`project remove default` 对内置项被拒。
- POLICY 模式 worker 上 `gofer project list` 读 server 下发的策略缓存，列**映射后的本机路径**（见 `SKILL.md` §6）。

## config（别名 `cfg`）

```bash
gofer config info                       # 解析出的 config 路径 + 关键 ENV + 关键设置
gofer config show <project>             # 某 project overlay 合并后的有效 config
gofer config validate server|worker     # 校验 config(别名 check); worker 按模式给判据 + 校验 roots; server 另在 digest/管家开启却无 work.digest 订阅时给 [WARN]
gofer config edit                       # 用 $VISUAL/$EDITOR 打开解析出的 config
```

## init — 脚手架

```bash
gofer init [server|worker|client]       # 从内置 example 模板生成 config(默认 server); client 生成 <config-dir>/.env(GOFER_RUN_MODE=client 的纯客户端节点, 无 config.yaml/worker.yaml)
gofer init -g worker                    # 写到用户全局 config 目录(client 默认就写全局 config dir 的 .env)
gofer init [-g] skill                   # 装 gofer-usage skill: 默认写 ./.claude/skills 和 ./.agents/skills 两处; -g 写全局 ~/.claude+~/.agents; -o <dir> 单目标
```

`gofer init hooks [--agent claude|codex|omp|jcode|all] [--global] [--remove]`：安装/卸载会话中继 hooks（见上文 session 节）。

## worker — 看 / 查 / 远程重载 worker

```bash
gofer worker ls                          # 列 server 上登记的 worker(别名 list)
gofer worker show <id>                   # 连接状态、gofer 版本、protocol: vN、in_flight、projects/agents、messenger(常驻传话进程状态)、policy_rev/applied_rev/policy_pending、rejected/degraded 项
gofer worker projects <id>               # 该 worker 当前**生效**的 project 清单(POLICY 下即 server 下发 + roots 映射后的结果)
gofer worker reload <id> [--reason "…"] [--timeout 秒]   # 别名 rl: 经 server 让已连接的 worker 重读配置(不重启), 等回执; Windows worker 同样可用
gofer worker reload --local [<id>] [-c <server-config>] [--timeout 秒] # 本机 PID/SIGHUP 或 Windows 命名事件；等待 run/worker-<id>.reload.json
gofer worker doctor | init | stop        # 在 worker 机器本机用(自检 / 一键接入 / 停后台进程)
```

- `reload` 热生效 agents / roots / guards / labels / max_concurrent / tunnel 白名单；`worker_id`、`server_link`、storage、`xfer_timeout_sec` 要重启。需 `can_admin`。结果：成功（打印新能力摘要）/ worker 自己的拒绝原因 / `offline`、`too_old`（协议太旧：升级重启）/ 超时 504（worker 仍可能已应用，稍后 `worker show`）/ 未知 worker 404。
- 新装了某 agent CLI 想让探测可见：对应 server（SIGHUP 或 `POST /v1/config/reload`）或 worker（`worker reload`）重载一次。
- 本机 server：`gofer serve reload -c <config> [--timeout 秒]`。结果文件为 `<配置目录>/run/serve.reload.json`，字段是 `rev`、`path`、`changed`、`restart_required`、可选 `error` 与 `reloaded_at`（RFC3339Nano 写入时间）；CLI 以 `reloaded_at` 不早于发信号时间判定新结果（不依赖 `rev`，server 重启后 `rev` 会重新计数）；CLI 默认等待 10 秒，超时后查看该文件和 serve 日志。Windows 使用 `Global\\gofer-reload-<pid>`，Unix 使用 SIGHUP。
- 新装了 CLI 想让探测可见：对应 server（`serve reload`、SIGHUP 或 `POST /v1/config/reload`）或 worker（`worker reload` / `--local`）重载一次。`server.workers` 与 type=worker 的 `runners` 增删/改 token 无需重启；`server.callers` 仍需重启，peer-http 等 runner 类型仍通过 `restart_required` 提示重启。

## 运维向（AI 一般不直接用，了解即可）

- `serve` / `worker` / `presence`：起 server / worker、看在线状态。worker 配置见 [`worker-config.md`](worker-config.md)、server 配置见 [`server-config.md`](server-config.md)、加 project / 建 worker / 迁移分步见 [`setup-recipes.md`](setup-recipes.md)。
- `agent` / `mcp`：agent 定义探测、MCP 接入。
#### Worker 远程升级（协议 v15）

`gofer worker upgrade <id>` 的选项（以 `--help` 为准）：`--file <二进制>`（上传到 server 暂存；缺省用 server 自己的可执行文件，仅同 os/arch）、`--force`（不等在途 job，直接切换，在途 job 按 worker 重启处理）、`--drain-timeout <秒>`（等在途 job 的上限，默认 600，超时放弃升级并恢复接单）、`--no-wait`（worker 接受新二进制后即返回）、`--timeout <秒>`（等最终结果的上限，默认 `--drain-timeout` + 90）。默认等到最终结果：成功打印「已升级到 vX（耗时）」，回滚/失败打印原因并以非 0 退出。

HTTP 接口（均需 `can_admin`，除 worker 下载）：
- `PUT /v1/workers/{id}/upgrade/file`：原始 body 即二进制，server 暂存到 `<配置目录>/run/upgrade/<id>.bin` 并自己计算 sha256/size；返回 `{sha256,size}`。
- `POST /v1/workers/{id}/upgrade`：body `{source:"staged"|"server", force, drain_timeout_sec, ready_timeout_sec}`（`source` 缺省 `server`）。worker 校验通过并开始交接时返回 202 `{upgrade:{...}}`；worker 离线、协议 < v15、平台不符、已有升级进行中返回 409，worker 超过 5 分钟没应答返回 504。
- `GET /v1/workers/{id}/upgrade/file`：**只允许该 worker 自己的 worker token** 下载自己的那份，其他 worker token / 普通 caller token 一律 403。
- `GET /v1/workers/{id}` 与 `GET /v1/runners`（worker 行）带 `upgrade` 记录：`state`（`pending|succeeded|rolled_back|failed`）、`from_version`、`target_version`、`error`、`started_at`/`finished_at`（毫秒）、`duration_ms`；worker 排空期间 `worker.draining=true`。`GET /v1/runners` 顶层另有 `server:{os,arch,version}`。

交接机制：新进程用旧进程的参数与环境启动（detached），附加内部参数 `--upgrade-from <旧pid> --upgrade-id <id> --upgrade-ready <就绪标记文件>`，注册帧带 `upgrade_id`；注册成功后新进程接管 daemon pidfile 并写就绪标记，旧进程见标记即退出；server 以带 `upgrade_id` 的注册作为升级成功信号。新进程输出追加写到 `<配置目录>/run/worker-<id>.out.log`。旧二进制保留为 `<exe>.old`（成功后留一个版本供手动回退）。协议 < v15 的 worker 保持在线，只是被拒并提示手动升级一次。
