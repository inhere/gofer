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
