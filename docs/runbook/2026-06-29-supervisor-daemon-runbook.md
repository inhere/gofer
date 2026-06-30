# 通用 sup daemon 落地 Runbook（监督分层升级路由 P4a）

> 配套：design `docs/design/2026-06-29-supervisor-routing-design.md` §8.3-8.5；plan `docs/plans/2026-06-29-supervisor-routing-plan.md` P4a。
> 适用前提：host serve **已重建到含 P0-P3 的版本**（owner-first 路由 / owner 超时兜底 / 套娃防护 / 派生作答白名单 + `answered_by` 审计 / mcp 工具 `gofer_*`）。未重建前，下方端到端验证步骤跑不通。

## 1. 通用 sup 是什么 · 落地链路

通用 sup = 一个常驻的 **driver agent**（role=`supervisor`），循环 `gofer_poll_inbox` 取 `kind=escalation` → reason → `gofer_answer_interaction`。它本质是「一个长生命周期的 agent job」（design §8.3）。

它怎么获得 `role=supervisor` 身份，是整条链路的核心。**role 不来自 `gofer job run --role`**（那是 E35 角色预设，只填 agent/system_prompt/project/tags），而来自 **sup 的 gofer MCP 进程读环境变量 `GOFER_AGENT_ROLE=supervisor` 自注册**（P3.1，`internal/mcpserver/server.go:40` `envAgentRole` → `selfRegister` 读它 → `RegisterAgent(name, role)`）。只有 role=supervisor 的应答会被 `answerguard` 闸按白名单收口；普通 driver 无此 env → role 空 → 不被 gate（放行）。

完整链路（env 传播是关键）：

```
host serve(进程 env)
  └─ gofer job run --role supervisor       # 起一个 codex job(roles.supervisor)
       └─ local runner: cmd.Env = os.Environ(serve) + agent.env + roles.supervisor.env(GOFER_AGENT_ROLE)   # ← 见 §3
            └─ codex 进程(env 含 GOFER_AGENT_ROLE=supervisor)
                 └─ codex spawn `gofer mcp --server <serve>`(client 模式)
                      └─ 该 mcp 进程读 GOFER_AGENT_ROLE → selfRegister(role=supervisor)
                           └─ 注册到中央 serve presence(role=supervisor)
                                ├─ gofer_poll_inbox 取 escalation
                                └─ gofer_answer_interaction → answerguard 按 role 分级 gate
```

## 2. 已核实结论（决定本 runbook 可行性 / gap）

| 项 | 结论 | 依据（代码） |
|---|---|---|
| `gofer job run --role` 存在 | ✅ 存在，但它是 **E35 角色预设**（填 agent/system_prompt/project/tags），**不设 presence role** | `internal/commands/job.go:124`；`internal/job/submit.go:317` `resolveRole` |
| role 预设字段 | `agent / system_prompt / project / tags`，**无 env、无 non-interactive 字段** | `internal/config/model.go:77` `RoleConfig` |
| 能否给被起 agent 进程**设 env** | ✅ 但**只能 per-agent**（agent 配置 `env:` map）或 serve 进程级 env；**无 per-job `--env` flag、`JobRequest` 无 `Env` 字段** | `internal/agent/adapter.go:42` `copyEnv(ac.Env)` → `internal/runner/local/runner.go:55` `cmd.Env=mergedEnv(req.Env)`；`job.go` flag 集无 `--env` |
| GOFER_AGENT_ROLE 能否贯通到 sup mcp | ✅ **gofer 侧到 codex 进程 env 这一跳已验证**（mergedEnv 注入）；**codex → 其 spawn 的 mcp 子进程 env 透传是唯一未在真机核实的一跳**（见 §6 gap①） | 同上 + 待真机验证 codex MCP env |
| `--timeout 0` / 长生命周期 | ❌ **不支持无限 timeout**。`--timeout 0`=默认 `DefaultTimeoutSec=300s`；任何值被 `normalizeTimeout` 钳到 `MaxTimeoutSec=3600s`（1h）**硬上限** → sup job 最多活 1h（gap②，靠外部 relaunch 兜底） | `internal/job/service.go:21-22`；`internal/job/submit.go:372-379` |
| non-interactive 怎么传 | 经 **agent 定义的 `command`/`args`**（codex 用 `exec` 子命令本身即非交互 + 沙箱 flag），role 预设不承载；写进 sup 专用 agent 的 args | `internal/agent/registry.go:97` codex 内置 `args:[exec,...]` |

> **gap 小结**：①codex→mcp 子进程 env 透传需真机核实（有不依赖透传的稳妥兜底，§6）；②job timeout 1h 硬上限（用外部 relaunch loop 兜底，根治留 P4b reconciler）。**两者都无需改 gofer 代码即可文档绕过**。

## 3. 关键：把 `GOFER_AGENT_ROLE=supervisor` 注入到 sup（不污染普通 job）

**推荐(增强 `c0f355a` 后,最简)**：用 **`roles.supervisor.env`** 注入——`--role supervisor` 提交时 `resolveRole` 把 role 的 env 叠加到**该 job** 的进程 env(per-job,优先级 `agent.env < job.env`),**只对带 `--role supervisor` 的 job 生效、普通 codex job 不受污染**。无需单独定义 `codex-sup` agent,直接复用内置 `codex`(其 `system_inject`/`session_resume` 默认按 agent 名生效)。

> 链路:`roles.supervisor.env` → `resolveRole` 填 `req.Env` → local runner `cmd.Env` → codex 进程 env 含 `GOFER_AGENT_ROLE` → codex spawn 的 gofer mcp 子进程继承(gap① 见 §6)→ 自注册 role=supervisor。**仅 local runner 生效**(远端 Forward 不带 Env);sup 跑主机 local,符合。

> 旧做法(仍可用):定义 sup 专用 agent `codex-sup` 配 `env`——**仅当你额外要 read-only 沙箱等 agent 级差异(改 args)时才需要**;只为注入 env 已无需它。注意新 agent 名不继承内置 codex 默认(`builtinSessionDefaults["codex"]`,`registry.go:89`),需显式补 `system_inject`。

## 4. 配置样例（`~/.config/gofer/config.yaml`）

```yaml
# --- L0 answerer + 答闸策略（answerguard 白名单与 L0 同源，core.Build 恒注入，
#     与 supervisor.enabled 无关；enabled 只控 L0 内置 poller 是否跑）---
supervisor:
  enabled: true                        # 启动 serve 内置 L0 规则 answerer poller
  auto_answer: true                    # L0 自动答白名单内 choice；false=只路由不自动答
  interval_sec: 5                      # poller 周期（默认 5s）
  max_rounds_per_job: 3                # 单 job 自动处理轮次熔断（默认 3）
  escalate_to: "role-one:supervisor"   # owner 离线/超时后投给在线 sup（取一，防多 sup 抢答）
  owner_answer_timeout_sec: 300        # L1 owner 超时 → fallback L2（默认 300s）
  allow_prompt_regex:                  # 真实白名单：L0 自动答 + L2 sup 派生作答 同源校验
    - '^是否继续[?？]?\s*$'              # 仅放行这类“通用低危确认式 choice”
    - '(?i)^(proceed|continue)\b'
    - '^(yes/no|y/n)\b'

# --- 普通 codex agent（无需 sup 专用 agent;env 走 roles.supervisor.env，增强 c0f355a）---
agents:
  codex:                               # 普通 codex（内置 system_inject/session 默认按名生效）
    type: cli-agent
    command: codex
    args: [exec, "{{prompt}}"]
    detect: { command: codex, args: [--version] }

# --- role 预设：env 在此注入 supervisor 身份（增强 c0f355a，仅 --role supervisor 生效、不污染普通 codex job）---
roles:
  supervisor:
    agent: codex                       # 直接复用内置 codex（system_inject 自动生效）
    project: my-project1               # 默认项目（可被 -p 覆盖）
    env:
      GOFER_AGENT_ROLE: supervisor     # ← 核心：--role supervisor 时注入 → codex mcp 子进程注册 role=supervisor
      # 若 codex 不向 mcp 子进程透传父 env（§6 gap①），改 CODEX_HOME 兜底（§6 方案B）：
      # CODEX_HOME: /home/you/.codex-sup
    system_prompt: |                   # 经 codex 内置 system_inject 注入；亦可改 --prompt 直传
      你是 gofer 的通用 supervisor（兜底层，不持具体任务上下文）。
      循环执行 gofer_poll_inbox 取自己 inbox 的消息；只处理 kind=escalation。
      对每条 escalation：仅当它是“上下文无关的通用低危决策”（如通用确认/默认值）时，
      用 gofer_answer_interaction 作答；任何高危（删除/外发/付费/不可逆）、需要任务前因
      后果才能答、或你拿不准的，一律不要猜——留在 pending 让人处理（L3）。
      你的应答会被 serve 端白名单闸二次校验，越权应答会被拒（这是预期，不要绕过）。
```

> 真实白名单要按你环境里**真正出现且确属低危**的 prompt 文案来配；`allow_prompt_regex` 为空 = sup 派生作答**什么都答不了**（最保守），owner/human 仍放行。

## 5. sup prompt 模板（不用 role 预设时，直接 `--prompt` 传）

```
你是 gofer 的通用 supervisor（兜底层，不持具体任务上下文）。请进入循环：
1) 调 gofer_poll_inbox(agent_id, agent_token, ack=true) 取自己 inbox 的消息；
2) 只处理 kind=escalation 的消息（ref 形如 job:<id>#<iid>）；其余忽略；
3) 对每条 escalation 做“是否上下文无关的通用低危决策”判断：
   - 是（如通用确认、安全默认值、可枚举 choice）→ gofer_answer_interaction(id,interaction_id,answer)；
   - 高危（删除/外发/付费/不可逆/需要任务前因后果）或拿不准 → 不要猜，留 pending 给人；
4) 处理完短暂等待后回到 1）继续轮询，直至进程退出。
约束：你必须 non-interactive（不发起需人确认的动作）；你的应答会被 serve 白名单闸校验，越权会被拒。
```

## 6. 已知 gap 与应对

### gap① codex↔gofer MCP 集成 + env 透传（E2E 实测定位的真正前置）

> **⚠️ 前置硬条件（E2E 2026-06-29 实测定位）**：host codex 的 `~/.codex/config.toml` **必须先配 `[mcp_servers.gofer]`**（client 模式指向中央 serve）。否则 codex 根本**不会 spawn `gofer mcp` 子进程**——它会把 prompt 里的 "gofer" 当成 CLI 直接 shell 调用（实测 stderr：`gofer presence list` usage + `Exit code:1`），sup 永远不会成为在线 driver。补该配置前，§7「形式一/三」起的 codex 只是空跑（控制面侧逻辑不受影响，用 HTTP 模拟 sup 已全绿）。

**实测结论（gap① 验证 2026-06-29）**：codex 启动 MCP stdio 子进程用**净化 env、不透传父进程 `GOFER_*`** —— §3 注入到 codex 进程 env 的 `GOFER_AGENT_ROLE` 到不了 mcp 子进程，且 mcp 连 serve 所需 token 也到不了 → `selfRegister` 静默失败、presence 无 driver。**方案A（依赖透传）已证伪。**

> **⚠️ 更广影响（不只 sup）**：同理，**该主机所有 codex 会话**的 gofer mcp 都拿不到 token → P1.0 owner 自注册 / E36 driver presence 一直静默降级为空。**要让真实 codex 会话能成为 gofer driver（owner-first 路由真实生效），必须给 `[mcp_servers.gofer]` 配 token env** —— 这是比 sup 更基础的前提。

**修复 = ① token（用户全局配）+ ② role（方案C 已自动）**：

- **① token（用户必做，全局）**：host codex `[mcp_servers.gofer]` 补 **单个 `GOFER_SERVER_TOKEN`**（client 连接 token;`GOFER_TOKEN` 是 serve 自己的变量、client 不用它）：
  ```toml
  [mcp_servers.gofer]
  command = "gofer"
  args = ["mcp", "--server", "http://127.0.0.1:8767"]
  env = { GOFER_SERVER_TOKEN = "<serve token>" }   # 本机 config，不入 gofer 库;单个即可
  ```
- **② role（方案C，gofer 已自动，`74353d1`）**：`--role supervisor` 提交 codex job 时，gofer **自动**追加 `-c mcp_servers.gofer.env.GOFER_AGENT_ROLE=supervisor`（把 role.env 注入 mcp 子进程，key 排序、独立 argv 不经 shell）。**无需手配** CODEX_HOME 或 codex args;普通 codex job（无 role.env）不追加 → 不污染。mcp 块名默认 `gofer`，可经 agent 配置 `mcp_server_name` 改。判据 = agent 的 SystemInject 为 `-c` 形态（codex）。
- **方案B（可选，纯用户配，不依赖方案C）**：给 sup 用独立 `CODEX_HOME`，其 `config.toml` 的 `[mcp_servers.gofer].env` 直接写 `{ GOFER_AGENT_ROLE = "supervisor", GOFER_SERVER_TOKEN = "..." }`;普通 codex 用默认 CODEX_HOME 不受影响。仅当不想用方案C 自动注入时用。

> 验证：配好 token + 起 `--role supervisor` sup → `presence?role=supervisor` 见到在线 driver = 贯通。

### gap② job timeout 1h 硬上限 → sup 活不久

`normalizeTimeout` 把所有 timeout 钳到 `MaxTimeoutSec=3600s`，**没有无限 timeout**。sup job 最多活 1h 会被杀。

- **MVP 兜底（本 runbook 采用）**：外部 **relaunch loop** 拉起（shell `while true` / systemd `Restart=always` / cron）。`escalated_at`/`answered_by` 已落表、`fellBack` 偏安全方向、presence 90s TTL 自然过期，**relaunch 安全**（不会重复投递/双重作答）。
- **根治**：留 P4b serve `sup reconciler`（周期核对在线 sup 数 < desired → 自动重派），或后续给 job 增加“长生命周期/不被 timeout 杀”形态（design §11 待确认）。

> codex `exec` 本身是**一次性**执行（agentic loop 跑到它认为完成即退）。即便不撞 1h 上限，单次 exec 也可能提前结束——这同样靠 relaunch loop 兜底，每次 relaunch 做“注册→轮询若干次→答能答的→退出”。

## 7. 启动命令

前置：host serve 已重建（含 P0-P3）并在跑；codex 已配好 gofer MCP server 指向中央 serve（client 模式，即既有 E36 driver 接入方式），sup 这一跳只多了 `GOFER_AGENT_ROLE`（§3/§6）。

**形式一：用 role 预设（推荐，复用 system_prompt）**

```bash
# 单次（会被 1h 上限/exec 结束所限）
gofer job run -p my-project1 --role supervisor --runner local
# --role supervisor 解析出 agent=codex + 注入 roles.supervisor.env(GOFER_AGENT_ROLE) + system_prompt
```

**形式二：直接指定 agent + prompt（不依赖 role 预设）**

```bash
gofer job run -p my-project1 -a codex-sup --runner local \
  --prompt "$(cat sup-prompt.txt)"     # sup-prompt.txt = §5 模板
```

**形式三：常驻（MVP daemon，外部 relaunch loop，兜 gap②）**

```bash
# 简易 shell 守护：sup job 退出后立即重起；relaunch 安全（§6 gap②）
while true; do
  gofer job run -p my-project1 --role supervisor --runner local --sync
  sleep 2
done
# 生产建议改 systemd unit：Restart=always / RestartSec=2，命令同上单行。
```

> 不要用 `--timeout 0` 期望“无限”——它等于默认 300s。需要更久就传到上限（如 `--timeout 3600`），并始终配 relaunch loop。

## 8. 端到端真机验证步骤

> **整段需 host serve 重建到含 P0-P3 的版本后执行**（影响现有环境，由主控/用户决策触发）。建议经 `gofer job --runner local` 提交到主机，或直接在主机 shell 跑。

前置自检：
0. **host codex `~/.codex/config.toml` 已配 `[mcp_servers.gofer]`**（client 模式指向 serve）—— 否则 codex 不 spawn gofer mcp、sup 起不来（§6 gap①，E2E 实测前置硬条件）。
1. `gofer_list_agents` 见 `codex`(`--role supervisor` 解析出的 agent)、`detect` 通过（用 codex-sup 旧做法时则见 codex-sup）。
2. 起 sup（§7 形式三），稍候 `presence?role=supervisor` → **看到一个 role=supervisor 在线 driver**（验证 §6 gap①:token 已配 + 方案C 自动注入 role 贯通;看不到 → 查 `[mcp_servers.gofer].env` 的 `GOFER_SERVER_TOKEN` 是否配，或走方案B）。

主流程（owner 离线 → sup 接管 → 答 → 原 job 续 + 审计 + 高危拒答）：

3. **造 owner 离线的 escalation**：用一个**独立 mcp 会话 A**（owner，role 空）`gofer_run_job` 起一个会触发 **非白名单/或 choice** interaction 的 job（记下 `origin_agent` = A 的自注册 id）；随后**关掉会话 A**（owner 离线）。
   - 也可直接造一个 owner 为空的 job（CLI/web 提交，无 driver 身份）→ 其 interaction 直接 L2 投 sup，省去等 owner 超时。
4. **sup 取到 escalation**：等 supervisor poller（≤5s）+ owner 超时窗口（`owner_answer_timeout_sec`，验证时可临时调小到 ~30s）后，escalation 落 sup inbox。sup 的 `gofer_poll_inbox` 取到 `kind=escalation`、`ref=job:<id>#<iid>`。
5. **低危分支（应答放行）**：该 interaction 是**白名单内 choice** → sup `gofer_answer_interaction` 成功 → 原 job 续跑到终态。
   - 校验审计：`gofer_get_interactions(job_id)` 看该 interaction `status=answered`、**`answered_by=agent:<sup-id>`**（sup 的自注册 agent_id）。
6. **高危分支（gate 拒答）**：另造一个 **confirmation 或非白名单 choice** 的 escalation → sup 尝试 `gofer_answer_interaction` → **被拒（`ErrAnswerNotAllowed` / HTTP 403）**，interaction **仍 pending**（未被 sup 越权答）。
   - 对照：用 owner（responder==origin_agent）或 human（web/CLI，responder 空）答**同一条** → 成功（`answered_by=agent:<owner-id>` 或 `human`）。
7. **套娃防护**：构造一个 role=supervisor 的 job 自身产生的 interaction → 路由器**不自动答、不回投 sup**，直接留 pending 等人（`internal/supervisor/service.go:206` `jr.Role==roleSupervisor` 跳过）。
8. **relaunch 安全**：kill sup job（或等其 1h/exec 结束）→ relaunch loop 重起新 sup → 不重复投递已 `escalated_at` 的 interaction、不重复作答（落表 dedup 生效）。

通过判据：第 5 步 `answered_by=agent:<sup-id>` 且原 job 续；第 6 步高危被 403 拒、owner/human 能答；第 7 步 sup 源 interaction 不进自动答；第 8 步 relaunch 无重复副作用。

## 9. 备注 / 后续

- 本 runbook 覆盖 P4a 的「文档化 daemon」。**端到端真机过**（§8）需重建 host serve、影响现有环境，**留主控/用户决策触发**（plan P4a 第二项）。
- gap② 的根治（server 托管常驻、节点无关、自愈）是 **P4b reconciler**（plan §P4b / design §8.3 P2）。
- ✅ **已落地(`c0f355a`)**：`RoleConfig` 加 `env map`、`JobRequest` 加 per-job `Env`，`--role supervisor` 直接注入 `GOFER_AGENT_ROLE`、无需单独 `codex-sup` agent（本 runbook §3/§4 已按此简化）。**仅 local runner 生效**（远端 Forward 不带 Env，sup 跑主机 local 符合）。
</content>
</invoke>

## 10. 走标准 MCP 工具路径(claude-sup,2026-06-30 落地)

**背景**：codex `exec` 非交互模式**结构性不暴露 MCP server 工具**(OpenAI codex #24135：MCP 工具调用因无法弹批准被自动取消，唯一绕过 `--dangerously-bypass-approvals-and-sandbox` 会关沙箱)。故 codex sup 只能自写 PowerShell HTTP 轮询兜底(功能正常但非标准路径)。改用 **claude** 做 sup：`claude -p` 配 `--mcp-config` + `--allowedTools` 可在非交互下精确放行并真正调用 gofer MCP 工具(不关沙箱)。

**配置(全局 config)**：
- `roles.supervisor.agent: claude-sup`(`project` 仍必填)。
- 新 agent `claude-sup`：`command: claude`，`args: [-p, "{{prompt}}", --mcp-config, <json>, --allowedTools, mcp__gofer__gofer_poll_inbox, mcp__gofer__gofer_get_interactions, mcp__gofer__gofer_answer_interaction, mcp__gofer__gofer_list_pending_interactions]`。
  - **`{{prompt}}` 必须紧跟 `-p`(位置参数)**；变参 `--allowedTools` 放最后，否则会吞掉 prompt。
  - 项目 `allowed_agents` 需加 `claude-sup`。
- 专属 MCP 配置 `<config-dir>/gofer-sup-mcp.json`(不污染普通 claude job)：
  ```json
  {"mcpServers":{"gofer":{"command":"gofer","args":["mcp","--server","http://127.0.0.1:8767"],
    "env":{"GOFER_SERVER_TOKEN":"<token>","GOFER_AGENT_ROLE":"supervisor","GOFER_AGENT_NAME":"gofer-supervisor"}}}}
  ```
- `supervisor.reconcile_prompt`：指示「只用 gofer_* 工具：gofer_poll_inbox(不传参=轮询自身收件箱)→对 escalation 用 gofer_get_interactions 找 interaction_id→低危纯确认才 gofer_answer_interaction，其余留 pending→连续2轮空箱即结束(调度器再拉起)」。

**配套 3 个 gofer 代码改动(否则 claude sup 无法干净工作)**：
- `2d5be81` `gofer_poll_inbox` 的 `agent_id/agent_token` 改可选(omitempty)，省略时**回退到本 mcp 进程自注册身份**(`selfRegister` 同时返回 token)。否则 poll 要显式 register、而 answer 硬归因自注册身份→三处身份(poll/answer/role-one 路由)不对齐。
- `f226fd7` `GOFER_AGENT_NAME` 固定 mcp 自注册 name。否则每个短命 claude-sup burst 是新 `mcp-<hash>-<pid>`→escalation 会孤立在已退出进程的 inbox；固定名(`gofer-supervisor`)让所有 burst **复用一个持久 inbox**累积消息，任意 burst 都能 poll 到全部。
- `b8675f6` `reconcile_job_timeout_sec`(默认 3600)：sup job 超时上限。

**生产实测全绿(2026-06-30)**：presence 只剩固定名 `gofer-supervisor`；造 5 条纯「是否继续?」→ 3 条 L0 `auto:choice` + 2 条超轮次升级 → claude-sup burst 经 `gofer_poll_inbox`→`gofer_get_interactions`→`gofer_answer_interaction` 应答，`answered_by=agent:<gofer-supervisor-id>`，**零 PowerShell**。answerguard 白名单闸正常生效。

**回退**：`roles.supervisor.agent` 改回 `codex`(原 PowerShell sup)或 `desired_supervisors: 0` 重启。
