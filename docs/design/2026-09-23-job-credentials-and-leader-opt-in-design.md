<!-- template_id: design; template_version: 1.1.1 -->
# job 作用域凭证、leader 按 plan 开启与若干修复设计（SEC-01 / LEAD-02 / 小项）

> 状态：Approved 0.2 / 实施中（2026-09-23 人工批准，采纳推荐：决策 1/3/4 各加一个受控出口）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.2 | 2026-09-23 | Claude | 人工批准，按推荐补三处受控出口：① `projects.<k>.job_env_allow` 逐项目放行继承变量（job 详情显示）；③ `agents.<k>.can_submit` / `roles.<k>.can_submit`：member job 可提交**同项目**、仅限 `submit_agents` allowlist（默认 `[exec]`）的 job，自动打 `submitted_by_job:<id>`；④ 为正在跑成员 job 的 plan 打开 leader 时给出提示。决策 2/5 照原稿（5：CLI 为主、已登记 gofer MCP 的 agent 仍可用 MCP，底层同一 job token） |
| 0.1 | 2026-09-23 | Claude | 初稿：v0.57.0 leader 真机验收暴露"job 继承 server token、自报身份可被绕过"→ SEC-01 job 作用域凭证；leader 目前是全局开关 → LEAD-02 按 plan 开启；小项：worker 模式 skill CLI（bd h-aii-uzvc）、Board plan 过滤改输入框、导航「技能」→「Skills」、plan 页事件区、leader 配置视图 |

## 背景：leader 真机验收发现了什么

2026-09-23 在主机 v0.57.0 上临时打开 `supervisor.leader`（agent=omp，max_rounds=3，wake_delay=20s），建测试 plan `plan-20260923-220518-eb107490`：A 自动跑，B 设为 `--no-auto`（只能由 leader 或人放行）。结果：

- **流程本身通了**：A 完成 → 20s 后起 leader 回合 1（job `20260923-220633-0597f28e`）→ leader 读汇报、核验 A 的证据、把 B 置 ready → B 跑完 → plan `done`，按 S4 规则不再唤醒。
- **但 leader 是"绕着闸门"完成的**。omp 的运行环境里**没有挂 gofer MCP 工具**（MCP 工具只对自己配置了 gofer MCP server 的 agent 可用，见 `internal/agent/mcpenv.go`，本质上只有 codex），于是它退回去用**继承来的 server token** 直接调 HTTP / CLI：
  - 用 `POST /v1/plans/{id}/comments` 发评论，落库成 `author_kind=user`（它在汇报里自己点破了："若服务端把 user 评论当成人已介入，可能抑制后续唤醒"）；
  - 用 `gofer plan set-todo … --status ready` 放行 B——这条路不受 S3 的"leader 只能 ready|skipped"约束（那个约束只在 MCP 工具层）。
- 这正是 S3/S4 已经写进 runbook 的局限：**身份是自报的（`as_job`），job 进程通过 `util.Environ` 继承了 server 进程的整个环境，其中就有 bearer token**（`internal/runner/local/runner.go:55 cmd.Env = util.Environ(req.Env)`，`internal/util/env.go:22` 以 `os.Environ()` 为底）。v0.57 的真机验收把它从"理论局限"变成了"实际发生"。
- 另外两点：`supervisor.leader` 是**全局**开关——开了以后**每一个** plan 的成员终态都会唤醒 leader（测试时主机上还有 4 个 open plan）；`GET /v1/config` 的 supervisor 视图里没有 leader 块（S3 汇报称可读，实际读不到）。

结论：**leader 要能放心打开，得先让 server 从凭证而不是自报字段知道"这是哪个 job"。** 这就是本批的主项。

## 已确认事实（代码 / 环境）

- 调用方身份：`internal/httpapi/auth.go` 按 bearer 匹配 `callers[]`（`config.CallerConfig{ID, Token, TokenEnv, CanAnswer, CanAdmin, CanAttach}`，`model.go:1148`），kind 只有 `user`/`worker`（`server.go:51-52`）。
- 自报身份：CLI 在 `GOFER_JOB_ID` 已设时把它作为 `as_job` 带上（`internal/commands/comment.go:38/83`、`job.go:2257/2280`），**只覆盖了评论与 accept/reject**；`plan set-todo`、`job run`、`job cancel` 等都没有。
- job 环境：`util.Environ(extra)` = `os.Environ()` + extra（`internal/util/env.go:22`）；job 专属变量由 `internal/job/outcomes.go:529` 附近注入（`GOFER_JOB_ID`/`GOFER_RESULT_DIR`/`GOFER_CWD`，leader 另有 `GOFER_LEADER_PLAN`）。verify 步骤同样以 `os.Environ()` 为底（`internal/job/verify.go:177`）。
- gofer MCP 对 job 内 agent 的可用性：只有 codex 这类在自身配置里登记了 gofer MCP server 的 agent 能用；gofer 只负责给它的 MCP 子进程注入 env（`mcpenv.go McpEnvInjectArgs`）。omp / jcode / claude（未配 `--mcp-config`）都拿不到 `gofer_*` 工具。
- leader 开关：`config.LeaderConfig{Enabled, Agent, Scopes, OnMemberDone, WakeDelaySec, MaxRoundsPerScope}`（`model.go:383-400`），`internal/job/leader.go:76 maybeWakeLeader` 只看全局 `Enabled` + scope。
- web：Board 的 plan 过滤是 `listPlans()` 全量拉取后渲染 `<select>`（`web/src/views/Board.vue:197-198, 429`）；导航项 `{ to: '/skills', label: '技能' }`（`web/src/App.vue:69`），其余导航项是英文（Board/Plans/Agents…）。
- bd `h-aii-uzvc`：`GOFER_RUN_MODE=worker` 的节点上 `gofer agent skill …` 判定为非 client 模式 → 走本地库 → 报 "no local gofer config found … drop --local"（没传 `--local`，文案误导）。

## 一、SEC-01 job 作用域凭证

### 1. 凭证

- 每个 job 在**开始执行时**由执行它的 gofer 进程（hub 或 worker）签发一枚 **job token**：`gjt_<job_id>_<32 随机 hex>`。hub 侧存其 sha256（新表 `job_tokens {job_id, token_hash, kind, plan_id, expires_at, revoked_at}`；`kind` = `member` | `leader`），worker 侧执行的 job 由 hub 在 dispatch 时签发、随 dispatch 帧下发（新可选字段，协议 v11）。
- 生命周期：job 终态即吊销（`revoked_at`），另有兜底过期（job 超时 + 10 分钟）。
- 注入：`GOFER_JOB_TOKEN=<token>`、`GOFER_SERVER_ADDR=<hub 地址>`（worker 上填 worker 连接的 hub 地址）。

### 2. 不再继承 server 凭证

- job 进程与 verify 步骤的环境从"`os.Environ()` 全量"改为 **`os.Environ()` 去掉敏感键**：`GOFER_TOKEN`、`GOFER_SERVER_TOKEN`、`GOFER_WORKER_TOKEN`，以及 `server.job_env_denylist`（可配，默认空）里列出的键。实现为 `util.EnvironWithout(deny, extra)`，local / pty / acp / verify 四处统一使用。
- 这一步单独就能关掉"agent 拿 server token 冒充人"的口子；job token 是给它**合法的、受限的**替代。
- **逐项目出口**（0.2）：`projects.<k>.job_env_allow: [VAR,…]` 列出的变量即使在 denylist 里也照常继承；每个用到放行的 job 记事件 `job.env_allowed {keys}` 并在 job 详情显示。无全局"继续继承"开关。

### 3. server 如何对待 job token

新增 caller kind `job`（`auth.go` 在 `callers[]` 之外先试 job token 表）：

| 能力 | member job | leader job |
|---|---|---|
| 读：job/plan/comments/skills/agents | ✓ | ✓ |
| 评论（自动记为 `author_kind=agent`、`author=<agent key>`，不看 `as_job`） | ✓（不触发派活） | ✓（可 `@` 派活，受限流 + allowlist） |
| `gofer_ask_human` / decisions | ✓ | ✓ |
| 自身 job 的 wakeup 创建 | ✓ | ✓ |
| `plan set-todo` | ✗ | 仅本 plan、仅 `ready`/`skipped` |
| 提交 job（`job run`） | ✗（agent/role 配 `can_submit: true` 时：仅同项目、仅 `submit_agents` 内的 agent，默认 `[exec]`，新 job 带 `submitted_by_job:<id>`） | ✗ |
| accept / reject / cancel 他人 job / 改配置 / skill 写 / tool cp | ✗ | ✗ |

- `as_job` 字段**废弃**：身份以凭证为准；job 凭证调用时忽略请求体里的 `as_job`；user caller 带 `as_job` 也忽略（打 `// DEPRECATED(v0.58): remove in v0.61`，G032）。CLI 不再发送它。
- CLI：`GOFER_JOB_TOKEN` 存在时，`--token` / `GOFER_SERVER_TOKEN` 的默认值取它（显式 `--token` 仍可覆盖——但 job 环境里已经没有 server token 了）。
- 403 文案点明原因：`job credential may not <action> (member job)`。

### 4. 过渡

- 旧 worker（协议 < 11）收不到 job token：hub 给它派 job 时照旧（该 job 没有凭证，也就只能用它自己环境里有的东西）；记 `job.credential_skipped {reason:"worker_protocol"}`。worker 升级后自然生效。
- 本机 job 立即生效；`start.ps1` 起的 serve 环境里有 `GOFER_TOKEN`（来自 `.env`），升级后 job 进程将看不到它——**这是目的**。runbook 写清：若有既存脚本依赖"job 里直接调 gofer 用 server token"，改为用 `GOFER_JOB_TOKEN`（权限受限）或在调用处显式传 `--token`。

## 二、LEAD-02 leader 按 plan 开启

- plan 新增字段 `leader`：`off`（默认）| `on`。全局 `supervisor.leader` 只提供 **agent / 延迟 / 轮次上限** 等参数，并保留 `enabled` 作为总闸（总闸关 = 所有 plan 都不唤醒）。
- 开启方式：`gofer plan create --leader`、`gofer plan set <plan> --leader on|off`、web plan 页的开关、HTTP `PATCH /v1/plans/{id} {leader}`。
- `maybeWakeLeader` 判定改为：总闸开 **且** plan.leader == on。
- 打开 leader 时若该 plan 有运行中的成员 job，响应与 web 开关给出提示："N 个运行中的 job 结束后将唤醒 leader"（0.2）。
- leader job 用 `kind=leader` 的 job token（上节），其 prompt 里的"可用动作"改为 **CLI 命令**（`gofer plan comment …`、`gofer plan set-todo <todo> --status ready|skipped`、`gofer job wakeup create …`、`gofer ask-human`/decision 等价命令），因为 CLI 在任何 agent 里都能用，而权限由 server 按凭证强制——不再依赖 agent 挂没挂 MCP。
- `GET /v1/config` 的 supervisor 视图补上 `leader` 块（S3 遗留）；plan 详情显示 `leader: on|off` 与轮次。
- web plan 页新增**事件区**（显示 `plan:<id>` scope 的事件：`plan.leader_*`、`plan.blocked`、`plan.completed`、`comment.*`…；S4 遗留：标签已有、没有页面渲染）。

## 三、小项

| 编号 | 内容 |
|---|---|
| F-a | bd `h-aii-uzvc`：`gofer agent skill …` 在**本地没有 server 配置**时自动走 HTTP（与 client 模式一致），`--local` 才强制本地；错误文案改为说明实际判定（"本机没有 server 配置，且未能连上 server"）。同样审一遍 `agent list`/`template ls` 等"双模式"命令是否有同样问题，一并修。 |
| F-b | Board 过滤：plan 下拉改为**输入框**（输入 plan id，支持前缀匹配；回车/失焦生效；URL query `?plan=` 同步，便于分享）。不再全量 `listPlans()`（plan 越来越多）。输入框旁给一个"最近 5 个 plan"的轻量提示（`GET /v1/plans?limit=5&status=open`），不是完整下拉。 |
| F-c | 导航「技能」→「Skills」（与其他导航项的英文风格一致；页面标题同步）。 |
| F-d | （用户 2026-09-23 补充）Plans 页**分页** + **状态 / 项目 / 标题关键字**过滤：后端 `GET /v1/plans` 现在只认 `status`（`internal/httpapi/plan_handler.go:230 ListPlans(status, 0)`），扩为 `status`、`project`、`q`（标题/id 子串）、`limit`（默认 20，上限 100）、`offset`，响应带 `total`；前端过滤条放在列表上方，URL query 同步，翻页控件在底部；轮询只刷当前页。Board 的"最近 5 个 open plan"提示复用 `limit=5&status=open`。 |

## 横切

- 协议：dispatch 帧新增可选 `job_token`，`CurrentProtocolVersion` 10→11，`JobCredentialMinProtocolVersion = 11`。
- G032：`as_job` 打 DEPRECATED(v0.58) 标记，v0.61 删除；`util.Environ` 的无过滤用法在 job/verify 四处替换，其余（git 子进程等）不动。
- 安全：job token 只以 hash 落库；token 不进 `request_json`、不进渲染命令、不进日志（slog 脱敏）；job 结束即吊销。
- 迁移：`job_tokens` 新表；plan 新列 `leader`（默认 `off`，所以升级后**不会**有 plan 被意外唤醒；原全局 `enabled: true` 的部署需要逐 plan 打开——runbook 写明）。

## 实施分期与验收（全部 omp，测试先写先提交）

| 期 | 内容 | 验收要点 |
|---|---|---|
| **C1** | SEC-01：job token 签发/吊销/校验、caller kind `job` 与权限表、job/verify 环境去敏、CLI 用 `GOFER_JOB_TOKEN`、`as_job` 废弃、协议 v11 | `TestJobEnvHasNoServerToken`（local/pty/acp/verify 四处）、`TestJobTokenRevokedOnTerminal`、`TestMemberTokenCannotSetTodo`、`TestMemberTokenCannotAccept`、`TestLeaderTokenSetTodoOnlyReadyOrSkippedOwnPlan`、`TestJobTokenCommentIsAgentAuthored`（忽略 `as_job`）、`TestUserCallerAsJobIgnored`、`TestOldWorkerGetsNoJobToken`；真机：一个 omp job 里 `env | grep GOFER_` 只见 JOB_TOKEN 不见 TOKEN，用 CLI 试 accept → 403 |
| **C2** | LEAD-02：plan `leader` 字段 + CLI/HTTP/web 开关、唤醒判定改为逐 plan、leader prompt 改 CLI 动作、config 视图补 leader、plan 页事件区 | `TestLeaderOffByDefaultPerPlan`、`TestLeaderOnlyForOptedInPlan`、`TestLeaderPromptListsCliActions`、`TestConfigViewShowsLeader`；真机：只给一个测试 plan 开 leader，别的 plan 成员终态不唤醒；leader 用 CLI 放行 todo 成功、试图 `job run` 被 403 |
| **C3** | 小项 F-a/F-b/F-c/F-d | `TestSkillCmdFallsBackToHTTPWithoutLocalServerConfig`；web：Board 输入框 + `?plan=` 同步、导航文字；`pnpm typecheck && pnpm build` |

## 风险与限制

- **去掉 job 环境里的 server token 可能打断既有用法**：若有人在 job 里跑 `gofer job run` 这类需要高权限的命令，升级后会 401/403。这是有意收紧，runbook 与发布说明写明替代做法。
- **job token 泄露面**：token 在 job 进程环境里，agent 可读可打印；因为它只在 job 存活期间有效、权限表极窄、终态即吊销，泄露后果被限定在"本 job 能做的事"。
- **worker 上的 job**：token 由 hub 签发经 WS 下发，worker 侧注入；worker 本身的 token 同样从 job 环境去掉（`GOFER_WORKER_TOKEN`）。
- **leader 仍可能"想得不对"**：凭证只保证它**做不了越权的事**，不保证决定正确；轮次封顶 + 人评论即接管 + `plan pause` 仍是兜底。

## 决策（已批准 2026-09-23，0.2 采纳推荐）

1. job 环境**去掉** server/worker token（默认 denylist 三个键），不提供全局"继续继承"开关；需要时用 `projects.<k>.job_env_allow` 逐项目放行并可见。
2. 身份以凭证为准，`as_job` 废弃（v0.58 标记、v0.61 删除）。
3. member job 默认**不能** `job run`；`agents/roles.<k>.can_submit` 可放行同项目、`submit_agents`（默认 `[exec]`）内的提交，带溯源标签。
4. leader 改为**逐 plan 开启**，默认关；全局 `enabled` 只做总闸；对有运行中成员 job 的 plan 打开时提示。
5. leader 动作以 CLI 为主（server 按凭证强制权限）；已登记 gofer MCP 的 agent 仍可用 MCP 工具，底层同一枚 job token。

## C1 实测记录（2026-09-23，omp）

实现落地后按 §一 的验收要点实测（**临时 serve**：独立 config 目录 + 随机端口 18765 + `storage.root` 在 `tmp/`，其**进程环境里显式放 `GOFER_TOKEN=must-not-reach-job`**；CLI 一律 `--server http://127.0.0.1:18765 --token smoke-tok`，不使用真实 server）。

| 步骤 | 命令 | 实测结果 |
|---|---|---|
| job 环境 | exec job 跑 `gofer-testcmd env-print GOFER_TOKEN GOFER_SERVER_TOKEN GOFER_WORKER_TOKEN GOFER_JOB_TOKEN GOFER_JOB_ID GOFER_SERVER_ADDR`，读 stdout | `GOFER_TOKEN=` / `GOFER_SERVER_TOKEN=` / `GOFER_WORKER_TOKEN=` **全空**；`GOFER_JOB_TOKEN=gjt_20260923-233050-fdc91b7a_73cb8701b4…`；`GOFER_JOB_ID`、`GOFER_SERVER_ADDR=127.0.0.1:18765` 都在 |
| job 里 accept | job 内 `cmd /c <gofer> job accept <另一个 job>`（CLI 默认吃 `GOFER_JOB_TOKEN`） | 退出码 1，stdout：`ERROR: server 403: job credential may not accept a delivery: a member job may not perform this operation: its credential is scoped to reading, commenting, asking a human, and (a leader) moving its own plan's checklist` |
| job 里评论 | job 内 `cmd /c <gofer> job comment %GOFER_JOB_ID% hi` | 成功：`comment cm-4bc12e13 on job 20260923-233130-6aa361c6 by exec/agent`，`gofer job comments` 显示作者 `exec/agent`（不是人） |

自动化侧：`TestJobEnvHasNoServerToken`（local/pty/acp/verify 四条路径）、`TestJobEnvAllowlistPerProject`、`TestJobTokenIssuedAndRevoked`、`TestMemberTokenPermissions`、`TestMemberTokenCannotMoveTodo`、`TestMemberCanSubmitWhenAllowed`、`TestLeaderTokenPermissions`、`TestUserCallerAsJobIgnored`、`TestWorkerDispatchCarriesJobToken`、`TestOldWorkerGetsNoJobToken`、`TestCLIUsesJobTokenInsideJob` 全绿。

实测中发现并修正的一点：凭证吊销原本写在 `finish` 的终态**行落库之前**，把"内存已终态、库里仍 running"的既有窗口从微秒级放大到毫秒级，`TestPlanClientRoundTrip`（`GetPlan` 计数来自库）因此稳定失败；吊销改到 `persist(snap)` 之后即恢复（`internal/job/execute.go`）。

范围说明（本期未做）：peer-http（非 ws-worker 的远端 runner）不下发凭证——那套 transport 没有携带字段，`Forward.JobToken` 刻意不可序列化，避免本 hub 的凭证被 POST 到无关的 peer；远端 job 因此没有 `GOFER_JOB_TOKEN`。协议 < 11 的 worker 同理（记 `job.credential_skipped`）。

## C2 实测记录（2026-09-24，omp）

实现落地后按 §二 的验收要点实测（**临时 serve**：独立 config + 端口 18771 + `storage.root` 在 `tmp/`，启动命令里 `unset GOFER_SERVER_ADDR/GOFER_SERVER_TOKEN/GOFER_TOKEN`，CLI 一律 `--server http://127.0.0.1:18771 --token smoke-tok`；配置文件里 agent `member` 属于项目 `self`，`leader` agent 的 argv 是一个 bash 脚本，脚本读 `$GOFER_JOB_TOKEN` 调同一支 gofer CLI 并把每条命令的 `exit=` 写进收据文件）。

| 步骤 | 命令 | 实测结果 |
|---|---|---|
| 建 plan | `plan create --plan-id plan-lead-01 --project self --leader` / `plan create --plan-id plan-lead-02 --project self` | `plan plan-lead-01 created: status=open leader=on` / `plan-lead-02 created: status=open leader=off` |
| 是否逐 plan | 各跑一个假成员 job（`--plan plan-lead-01` / `--plan plan-lead-02`），等 sweeper | P1：多出 `20260924-004049-47ca8d8d | leader 回合 1：P1 开 leader | done | channel=leader | tags=leader,leader_of:…`；P2：只有那一个成员 job，`GET /v1/plans/plan-lead-02/events` = `{"events":[]}`（**没开 leader 的 plan 一条 leader 事件都没有**） |
| leader 环境 | leader job 内 `env` 收据 | `job_token_set=yes`、`server_token_set=no`、`plain_token_set=no`，`GOFER_LEADER_PLAN=plan-lead-01`、`GOFER_SERVER_ADDR=127.0.0.1:18771` |
| leader 放行本 plan | `gofer plan set-todo todo-20260924-004046-a724ddc5 --status ready`（P1 的待办） | `todo todo-…-a724ddc5 status=ready`，`exit=0`；`plan show` 里该待办变 `[>]` |
| leader 越权 | 同一脚本里 `plan set-todo <P2 的待办> --status ready` / `job run …` / `job accept <别的 job>` | 三条全部 `ERROR: server 403: job credential may not …`（分别 `move another plan's item` / `submit a job` / `accept a delivery`），`exit=2` |
| 轮次 | `plan show plan-lead-01` | `leader: on (round 1/3)`，`最近一次 leader job` 可点 |
| config 视图 | `GET /v1/config` | supervisor 视图含 `"leader":{"enabled":true,"agent":"leader","scopes":["plan"],"max_rounds_per_scope":3,"wake_delay_sec":1,"on_member_done":true}`（解析后的有效值） |
| plan 事件流 | `GET /v1/plans/plan-lead-01/events?limit=10` | `{"events":[{"seq":9,"job_id":"plan:plan-lead-01","type":"plan.leader_woken","detail":"{…\"round\":1,\"leader_job\":\"20260924-004049-47ca8d8d\"}","at":…}]}`（**最新在前**） |
| `plan set` | `plan set plan-lead-02 --leader on` → `plan show` → `--leader off` | `plan plan-lead-02 leader -> on`、`leader: on (round 0/3)`、`leader -> off`；`--leader maybe` 与漏传 `--leader` 都被 CLI 挡下（`invalid --leader` / `requires --leader on|off`） |
| web plan 页（`serve --web-dir web/dist`，真实 Chromium） | 打开 `/plans/plan-lead-01` | 操作区出现 `leader on` 开关；下方 banner `leader 回合已用 1/3 轮…` + `最近一次 leader job` 按钮；底部折叠面板展开后 `事件（1）` → `◈ leader 回合开始 第 1 轮 · leader · done · 20260924-004049-47ca8d8d`。点开关 → `leader off` 且轮次 banner 消失，再点回 `on` 复原 |
| warning | 建 `plan-lead-03`（leader off）→ 跑一个在 verify 里 `sleep 40` 的成员 job（running）→ 页面上打开 leader 开关 | 开关变 `leader on` 后出现提示框：`1 running job(s) will wake the leader when they finish`（+`知道了` 关闭）；同一文案由 `PATCH /v1/plans/{id} {"leader":"on"}` 返回 |

自动化侧（全部通过）：`TestLeaderOffByDefaultPerPlan`、`TestLeaderOnlyForOptedInPlan`、`TestLeaderGlobalSwitchStillGates`、`TestLeaderPromptListsCliActions`（`internal/job`）、`TestPlanLeaderToggleWarnsOnRunningMembers`、`TestConfigViewShowsLeader`、`TestPlanEventsEndpoint`（`internal/httpapi`）、`TestPlanLeaderFieldMigration`（`internal/jobstore`）；整包 `go test ./internal/job/ ./internal/httpapi/ ./internal/jobstore/ ./internal/commands/ -count=1` 全绿。

实现中定下的几个细节（超出原稿、但不改语义）：

- **`leader` 与轮次在 wire 上分成两个键**：`plan.leader` 是 `"on"|"off"`（列表/详情/创建/PATCH 都发），详情里的轮次块改叫 **`leader_round`**（原来是 `leader` 对象）。同一嵌入结构里两个 `leader` 键会互相遮蔽（encoding/json 只留浅层那个），而 `leader_round` 只在 plan 自己 `on` 时出现——旧键没有任何外部消费方（只有本仓 web），所以直接改名，不留兼容层（G032）。
- **`PATCH /v1/plans/{id}` 变成"字段都可选"**：三种字段至少给一个（只给 `leader` 就是开关）；只给 `progress` 或只给 `leader` 时 status 保持原值（`plan set` 需要）。非法 status/leader 仍是 400。
- **PATCH 的 `warnings` 随响应返回**（不是先问后写）：成员 job 的条数是服务端在写入时算的（`!job.IsFinished(status)` 计数），所以提示只能在写生效之后给；web 页把它渲染成写入后立即出现的可关闭提示框。
- **`plan set` 只做 `--leader`**：plan 的 title/description 至今没有 HTTP 写入口，CLI 不先行（不为了一个 flag 造接口）。
- **`plan events` 没有进 CLI**：本期只要求 HTTP 端点 + web 事件区，CLI 侧用 `curl`/web 即可；需要时再按 G033 加 `gofer plan events`。
- `maybeWakeLeader` 的判定顺序调整为"先看 plan 开关（off 直接静默返回）再看总闸"，所以 plan 关闭时既不起 job 也不记事件；`enabled:false` 但 plan 开着时记 `plan.leader_skipped{reason:"global_off"}`；窗口内被 `--leader off` 关掉记 `{reason:"plan_off"}`。

## C3 实测记录（2026-09-24，omp）

小项 F-a/F-b/F-c/F-d 落地后实测（**临时 serve**：独立 config 目录 + 端口 18790 + `storage.root` 在 `tmp/c3-smoke/`，启动命令里 `unset GOFER_SERVER_ADDR/GOFER_SERVER_TOKEN/GOFER_TOKEN`，CLI 一律 `--server http://127.0.0.1:18790 --token smoke-tok`；web 用 `serve --web-dir web/dist` + 真实 Chromium）。

| 步骤 | 命令 / 操作 | 实测结果 |
|---|---|---|
| F-a worker 回落 | `GOFER_RUN_MODE=worker`（无 `GOFER_CONFIG`、无 config.yaml）+ `GOFER_SERVER_ADDR/TOKEN` → `agent skill ls` | `(no skills)`、exit=0（修前：`ERROR: no local gofer config found … drop --local`） |
| F-a 远端写 | 同环境 `agent skill import tmp/c3-smoke/skillsrc/demo`（本地目录 → zip → POST） | `imported skill demo-skill (version 333bbf150730, 1 files, 65B)`；再 `agent skill ls` → `demo-skill 333bbf150730 65B smoke skill for C3` |
| F-a `--local` | 同环境 `agent skill ls --local` | exit=2：`no local gofer config found: --local reads the skill library beside the server's config, and this box has none; …` |
| F-a 无地址文案 | 同环境但 `GOFER_SERVER_ADDR=` 空 → `agent skill ls` | exit=2：`本机没有 server 配置（运行模式=worker），且连接 server 失败：未找到配置文件，且未通过 -s/--server 指定 server 地址；…` |
| F-a `agent list` | 同环境 `agent list` | 列出 **server 的** agent（`claude/codex/exec…` 带 batch/interactive 位），不是空本地 registry |
| F-d CLI 分页 | 建 25 个 plan（`plan-smoke-01..25`，奇数/偶数分属 self/other，后 5 个置 done）→ `plan list --limit 3` | 3 行 + `共 25 条，显示 1–3`；`--all` → 25 行 + `共 25 条，显示 1–25`（服务端每页 100 时一轮拿完） |
| F-d CLI 过滤 | `plan list --q plan-smoke-0` / `--q alpha` / `--project other --status open --limit 5` | `共 9 条，显示 1–9`（id 前缀）/ `共 10 条，显示 1–10`（标题子串，大小写不敏感）/ `共 10 条，显示 1–5` |
| F-d HTTP 信封 | `GET /v1/plans?limit=5000&offset=-3` / `?status=open&q=plan-smoke-0&project=self` / `?limit=1&offset=1` | `limit 100 offset 0 total 25 rows 25`（上限裁剪、负 offset 归零）/ `total 5`（三条件叠加）/ `limit 1 offset 1 total 25 ['plan-smoke-24']` |
| F-d web 列表 | `/plans` 打开 → 翻页 → 过滤 | 首屏 20 行 + `第 1–20 条 / 共 25 条`；点「下一页 →」→ URL `?offset=20`、`第 21–25 条 / 共 25 条`、下一页禁用；选 status=done → URL `?status=done`（offset 被清）、`第 1–5 条 / 共 5 条`；q 输入 `alpha` + 回车 → URL `?status=done&q=alpha`、2 行、两个翻页按钮都禁用 |
| F-b Board 输入框 | `/board` 点 plan 输入框 | 输入框（placeholder `plan id`），下方提示 `最近：plan-smoke-20 plan-smoke-19 plan-smoke-18 plan-smoke-17 plan-smoke-16`（`GET /v1/plans?status=open&limit=5` 只发一次；在 board 停留 6s 的请求记录里**没有任何 `/v1/plans` 轮询**）；点一个提示 → URL `?plan=plan-smoke-20`、输入框填上、job 列表按该 plan 过滤 |
| F-c 导航 | 任一页面左轨 | 导航项是 `Skills`（英文，与 Board/Plans/Agents 同风格），页面标题 `SKILLS` |

自动化侧（全部通过）：`TestSkillCmdFallsBackToHTTPWithoutLocalServerConfig`、`TestSkillCmdLocalFlagForcesLocal`、`TestSkillCmdFallbackErrorExplainsWorkerMode`、`TestAgentListSameFallback`（`internal/commands`）、`TestListPlansFilterAndPaging`（`internal/jobstore`）、`TestPlansEndpointPaging`（`internal/httpapi`）、`TestPlanListPagingFlags`（`internal/commands`）；整包 `go test ./internal/jobstore/ ./internal/httpapi/ ./internal/commands/ -count=1` 全绿。

实现中定下的几个细节（超出原稿、不改语义）：

- **双模式判定收敛成一个 helper**：`commands.useServerAPI(localFlag)` 返回 `serverAPIChoice{remote, fallback}` —— client 模式 → 远端；`--local` → 本地；否则本机没有 server 配置（`config.Load` 解析不到 config.yaml）→ 远端（fallback）；有 → 本地。`fallback` 只用来给**传输层**失败加前缀 `本机没有 server 配置（运行模式=X），且连接 server 失败：<err>`（`client.StatusOf(err) != 0` 即服务端答过话，不加——否则 404 会被说成"连接失败"）。已切换的判定点：`agent skill`（6 个子命令）、`agent list`、`project show`、`project validate`。**`project list` 保持原样**：它的本地一侧按角色读 worker.yaml/policy 缓存（不是 server 的 config.yaml），套用同一 helper 会把 worker 节点自己的项目列表换成远端视图，属于行为回退。
- **"本机有没有 server 配置"以"解析到配置文件"为准**（`path != ""`），不额外要求 `projects` 非空：新建 server 上还没有项目时它仍然是"本机库的主人"，此时要求 projects 非空会把 `agent skill ls` 甩到远端并连不上自己。非 server 角色下（worker/client）配置里没有 projects 才视为连接桩、走远端。
- **`--all` 是翻页而不是超大 limit**：服务端一页上限 100，`--all` 以 100 为步长按 offset 走完（`plan list --all` 因此是多次请求，测试用"服务端只给 2 行/页"的桩覆盖了这条路）。
- **plan 列表的 `q` 把 LIKE 元字符当字面量**（`escapeLikePattern` + `ESCAPE '\'`）：搜索框里输入 `%` 不该匹配全部。id 是**前缀**匹配、标题是**子串**匹配（两种语义有意不同），测试各钉了一条。
- **plan 列表默认页大小改由 jobstore 常量 `PlanListDefaultLimit=20` / `PlanListMaxLimit=100` 表达**（原 `DefaultListLimit=200` 不再用于 plan），HTTP 响应回显**生效后**的 limit/offset，前端据此判断有无下一页。

