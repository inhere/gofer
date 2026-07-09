# P0 快赢：配置默认化 + per-job agent flags（实施计划）

> 主纲：[config-optimize-plan.md](./config-optimize-plan.md) ｜ 设计：`docs/design/2026-07-09-config-federation-design.md` §13 / §14
> bd：xu64.13（§13 默认化）、xu64.12（§14 agent flags）
> 触点均已实测定位（2026-07-09 调研）。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-09 | inhere/claude | 初稿：代码级任务分解 |

## 范围

- **§13 配置默认化**：`allowed_agents` 为空 → 不按白名单限制（默认可用所有已配置 agent）；exec 仍由 `allow_exec` 独立把关。runner-local 默认已支持，仅补测试/文档。
- **§14 per-job agent flags**：`JobRequest.AgentArgs[]` 追加到 cli-agent argv 末尾；CLI / MCP / web 三入口透传；exec 禁用。

**不含**：联邦（P1）、UI 级联（P2）、per-project `allow_agent_args` 门控（后续可选）、interaction 授权（后续重方案）。

---

## 部分一：§13 配置默认化

### 精确语义（实施须严格遵循）
`allowed_agents` 空 = **白名单不生效（放开对所有已配置 agent）**；exec 的安全**不靠**白名单，而由 `allow_exec` 闸（`config.go:109`）单独把关（默认 `false`，故 exec 仍锁，需显式 `allow_exec=true`）。非空 `allowed_agents` 时维持原白名单语义（显式收紧）。

> 注：设计 §13 文字"全放非 exec"的精确落地即此——exec 在白名单层放开，但被 allow_exec 兜住，实际效果=非 exec 默认可用、exec 默认仍锁。

### T1. 改 `internal/job/config.go` validate 白名单闸（核心，约 3 行）

**现状（config.go:64-67）**：
```go
	// Agent must be in the project's allowed_agents (exec is not exempt).
	if err := agent.CheckAllowed(cfg, req.ProjectKey, gateAgent); err != nil {
		return config.ProjectConfig{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
	}
```

**改为**：
```go
	// Agent whitelist. Empty allowed_agents = allow all configured agents (§13,
	// config-optimize); exec safety is enforced independently by the allow_exec
	// gate below (~line 109, default false), so an empty whitelist never opens exec.
	if len(proj.AllowedAgents) > 0 {
		if err := agent.CheckAllowed(cfg, req.ProjectKey, gateAgent); err != nil {
			return config.ProjectConfig{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
		}
	}
```

- **不改** `internal/agent/allow.go` 的 `CheckAllowed`（保持"逐项匹配"语义，其直接单测 `agent_test.go` 不受影响）——默认化只在 validate 层通过"空则跳过白名单"实现。
- `proj.AllowedAgents` 与 `CheckAllowed` 内 `cfg.ProjectAllowedAgents` 同源（`model.go:674-680` 返回 `p.AllowedAgents`），故 `len(proj.AllowedAgents)>0` 判据准确。
- exec 双保险不破：空白名单时 exec 过了白名单层，但仍被 `config.go:109`（`ac.Type==TypeExec && !proj.AllowExec`）拦；`allow_exec=true` 时才放行——符合预期。

### T2. runner-local 默认：仅确认 + 补测试
- 现状 `checkRunnerAllowed`（`config.go:197-207`）已实现"`allowed_runners` 空 → 仅 local 放行"，**无需改代码**。
- 补一条测试固化该现状（见 T3）。

### T3. §13 测试（`internal/job/` validate/submit 测试文件）
定位现有 validate/submit 测试（`internal/job/*_test.go`，找断言 `ErrInvalidRequest`/`allowed` 的用例），新增：
1. `allowed_agents=[]` + 非 exec agent（如 codex）→ **通过**（原行为是拒）。
2. `allowed_agents=[]` + exec agent + `allow_exec=false` → **仍拒**（被 line 109 拦，错误含 `allow_exec=false`）。
3. `allowed_agents=[]` + exec agent + `allow_exec=true` → **通过**。
4. `allowed_agents=["codex"]` + 请求 `claude` → **拒**（非空白名单语义不变）。
5. `allowed_runners=[]` + runner=`local` → **通过**；+ runner=`worker-x` → **拒**（固化 T2）。
- 检查是否有**旧测试假设"空 allowed_agents = 全拒"**，若有则按新语义更新期望（预期极少，空=全拒本是 footgun）。

**验收**：`go test ./internal/job/...` 绿。

---

## 部分二：§14 per-job agent flags

### T4. `internal/job/model.go` 加字段
`JobRequest`（`model.go:9-157`）在 `Prompt`（line 13）后加：
```go
	// AgentArgs are extra CLI flags appended to a cli-agent's argv at build time
	// (e.g. permission-bypass flags for non-interactive jobs). Ignored for exec
	// agents (§14). Persisted in request_json → replayed on rerun.
	AgentArgs []string `json:"agent_args,omitempty" yaml:"agent_args,omitempty"`
```

### T5. `internal/agent/adapter.go` 在 cli-agent 分支 append（唯一 argv 组装点）
- `BuildOptions`（含现有 `AllowEmptyPrompt`）加字段：
```go
	AgentArgs []string // extra args appended to cli-agent argv (§14); exec unaffected
```
- cli-agent 分支（`adapter.go:56-68`）改 `Args`：
```go
		return Resolved{
			Command: ac.Command,
			Args:    append(Render(ac.Args, vars), opts.AgentArgs...),
			Env:     copyEnv(ac.Env),
		}, nil
```
- **exec 分支（`adapter.go:45-54`）不接收 opts.AgentArgs** → 结构性禁用，无需额外判断；resume（走 exec 载体，`resume.go`）同样天然不受影响。
- 注意 `Render` 返回新 slice（不复用 `ac.Args` 底层）才能安全 append；若 `Render` 可能返回引用，需 `append(append([]string{}, rendered...), opts.AgentArgs...)` 防串改（实施时确认 `Render` 实现）。

### T6. `internal/job/submit.go` 透传
`submit.go:164-171` 的 `BuildOptions{AllowEmptyPrompt: req.Interactive}` 改为：
```go
	agent.BuildOptions{AllowEmptyPrompt: req.Interactive, AgentArgs: req.AgentArgs}
```

### T7. §14 显式拒绝（可选增强，推荐）：exec + agent_args 报错
在 `config.go:99-112` 的 `!remote` 块内、拿到 `ac` 后加：
```go
		if len(req.AgentArgs) > 0 && ac.Type == agent.TypeExec {
			return config.ProjectConfig{}, fmt.Errorf("%w: agent_args not allowed for exec agent %q", ErrInvalidRequest, gateAgent)
		}
```
- 比"静默忽略"更友好（用户知道 exec 传 agent_args 无效）。remote 路径 agent 未必可知，交 worker 端二次校验/忽略即可。

### T8. CLI：`internal/commands/job.go` 加 `--agent-arg`（可重复）
- `jobRunOpts`（`job.go:25-46`）加：`agentArgs gcli.Strings`
- 绑定（`job.go:112-140` 内，仿 `project.go:76` 的 `VarOpt` 范式）：
```go
	c.VarOpt(&jobRunOpts.agentArgs, "agent-arg", "", "extra arg appended to cli-agent argv (repeatable)")
```
- `buildJobRunRequest`（`job.go:390-428`）赋值（约 line 402 区）：`AgentArgs: jobRunOpts.agentArgs,`（`gcli.Strings` 底层 `[]string`，必要时 `[]string(jobRunOpts.agentArgs)`）
- `schedule add` 复用 `buildJobRunRequest` → **自动继承**，无需额外改。

### T9. MCP：`internal/mcpserver/server.go` 加入参
- `runJobInput`（`server.go:346-365`）紧邻 `Prompt`/`Cmd` 加：`AgentArgs []string `json:"agent_args,omitempty"``
- `runJobHandler`（`server.go:378-397`）的 `job.JobRequest{...}` 加：`AgentArgs: in.AgentArgs,`
- schema 由 SDK 反射 struct tag 生成，无需手写。

### T10. web NewJob 高级区（前端，可与后端分开落）
- `web/src/views/NewJob.vue` 高级/可选区加一个"额外 agent 参数"输入（多值，如逗号/换行分隔或 tag 输入），提交时映射到 `agent_args`。
- `web/src/api/types.ts` 提交类型加 `agent_args?: string[]`。
- 仅对 cli-agent 展示/生效（可按所选 agent type 条件显示；type 信息来自已有 agents 数据）。
- **可作为 P0.5 独立小任务派 codex**（后端先行不阻塞）。

### T11. §14 测试
- `internal/agent/adapter_test.go`：cli-agent + `BuildOptions.AgentArgs=["--x"]` → `Resolved.Args` 末尾含 `--x`；exec + AgentArgs → argv **不含**（结构性忽略）。
- `internal/job/`：agent_args 从 JobRequest 流到 `runReq.Args`（submit 层）；exec + agent_args → validate 报错（T7）。
- CLI：`--agent-arg a --agent-arg b` → req.AgentArgs=["a","b"]（若有 commands 层测试）。

**验收**：`go test ./internal/agent/... ./internal/job/... ./internal/commands/... ./internal/mcpserver/...` 绿。

---

## 总验收

- [ ] `go test ./...` 全绿（容器 Linux）。
- [ ] web（若含 T10）：主机 `pnpm typecheck && pnpm build` 通过。
- [ ] 冒烟（local，容器/主机）：
  - project 不配 `allowed_agents` → 直接用 codex 提 job 成功（§13）。
  - exec + 无 allow_exec → 仍被拒（§13 未破坏 exec 安全）。
  - `gofer job run ... --agent-arg '--dangerously-skip-permissions'` → 该 flag 出现在渲染 argv（`rendered_command` 可见）（§14）。
  - exec agent + `--agent-arg` → 报错（T7）。

## 风险

- **§13 语义变更**：空=全拒→空=放开。风险面：若有 project 依赖"空=禁用所有 agent"来停用项目——但空=全拒会让项目完全不可用，属 footgun，实际不存在此用法。仍需 grep 现网/示例 config 确认无依赖，并在发布说明提示。
- **§14 append 串改**：见 T5 关于 `Render` 返回值是否共享底层数组的确认。
- **exec 泄漏**：agent_args 若误对 exec 生效=任意命令注入。结构性放在 cli-agent 分支（T5）+ 显式拒绝（T7）双保险。
