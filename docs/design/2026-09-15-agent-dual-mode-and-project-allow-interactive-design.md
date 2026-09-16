<!-- template_id: design; template_version: 1.1.1 -->
# agent 双模式启动与项目级交互开关设计 — AGT-02

> 状态：Approved 0.1 / 实施中（2026-09-15 人工拍板）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-15 | Claude | 初稿：agent 用 `interactive_args` 同时支持批处理与 pty 两种启动；项目用 `allow_interactive` 总开关取代必填的 `interactive_allowed_agents` |

## 背景与目标

现状把"agent 能不能交互"和"argv 长什么样"绑在同一个布尔 `interactive` 上：`interactive: true` 意味着 `args` 就是 pty 里裸启动的参数（不含 `{{prompt}}`），因此同一个 CLI 必须写两份定义（`codex` / `tty-codex`），而且在批处理 agent 上误加 `interactive: true` 会让所有普通 job 被 admission 以 "interactive-only" 拒绝——2026-09-15 主机 server 上实际发生过，直到重启前 `-a codex --runner local` 全部失败。项目侧还要单独维护 `interactive_allowed_agents`，web 控制台既没有该字段，编辑项目时又会把它清空（bd h-aii-3scy）。

目标：

1. 一个 agent 定义同时描述批处理与交互两种启动方式，两种 job 都能用同一个 key 提交。
2. 项目是否允许交互 job 变成一个布尔开关，默认关闭；agent 白名单只维护 `allowed_agents` 一份。
3. 误配在 serve 加载配置时报错，而不是在提交时才拒绝。
4. 现有 yaml（`tty-*` 定义、非空的 `interactive_allowed_agents`）不改也能继续工作。

## 已确认事实

- `config.AgentConfig.Interactive bool`（`internal/config/model.go`）由 admission 消费：`req.Interactive` 要求 `ac.Interactive && ac.NoRawCmd && Type != exec`（`internal/job/config.go:95-120`）；反向闸 `ac.Interactive && !req.Interactive` → "interactive-only"（同文件 :157）。
- 内置模板 `internal/agent/templates.go`：`claude`/`codex`/`opencode` 是含 `{{prompt}}` 的批处理定义，`tty-claude`/`tty-codex` 是 `Interactive+NoRawCmd`、无 args 的裸 TUI 定义。
- resume 已经是双模板：`SessionResume`（批处理）与 `SessionResumeInteractive`（TUI），按 agent key 或 command 基名从 `builtinSessionDefaults` 填充（`internal/agent/registry.go`）。
- 能力上报 `wsproto.AgentBrief{Key, Type, Interactive}`（worker → server），`/v1/meta` 的 agents 同样只有一个 `interactive` 布尔；web `NewJob.vue` 据此收窄下拉。
- 项目闸：`allowed_agents`、`interactive_allowed_agents`（空 = 不支持交互）、`allow_exec`；worker 闸：`guards.allow_interactive`（POLICY 模式下 server 下发的 interactive 列表被 worker 清空，`internal/commands/worker.go:581-605`）。
- `PUT /v1/projects/{key}` 用表单字段重建整个 `ProjectConfig` 后写回 yaml（`internal/httpapi/project_handler.go:181-189`），未在表单里的字段丢失。

## 总体方案

### 1. agent：两套 argv，能力由字段推导

```yaml
agents:
  codex:
    type: cli-agent
    command: codex
    args: [-s, danger-full-access, -a, never, exec, "{{prompt}}"]   # 批处理：job run
    interactive_args: [-s, danger-full-access, -a, never]           # pty：job run --interactive
```

| 字段状态 | 批处理模式 | 交互模式 |
|---|---|---|
| `args` 含 `{{prompt}}`，无 `interactive_args` | 有 | 无 |
| `args` 含 `{{prompt}}`，有 `interactive_args`（可为空列表 `[]` = 裸启动） | 有 | 有 |
| `interactive: true`（旧写法），`args` 不含 `{{prompt}}` | 无 | 有；`args` 即交互 argv |
| `interactive: true` 且 `args` 含 `{{prompt}}` | **配置错误**，加载即拒 | — |

- 新增 `AgentConfig.InteractiveArgs []string`（yaml `interactive_args`）；`Interactive bool` 保留为兼容别名，语义固定为"仅交互、args 即交互 argv"。
- 能力位统一由一个函数给出：`agent.Modes(ac) (batch, interactive bool)`；admission、worker 上报、`/v1/meta`、web 收窄全部只看能力位，不再直接读 `Interactive` 布尔。
- admission 两条闸对称：`--interactive` 而无交互模式 → `agent %q has no interactive mode`；非交互提交而无批处理模式 → `agent %q has no batch mode; submit it as an interactive job`（替换现在的 interactive-only 文案）。去掉 "交互 agent 必须 `no_raw_cmd`" 的 agent 级要求——`interactive job cannot override Cmd` 这条请求级闸已经覆盖同一风险；`no_raw_cmd` 字段本身保留、语义不变。
- 交互 argv 的构建：有 `interactive_args` 用它，否则（旧写法）用 `args`；`system_inject` / `session_inject` 追加规则与现在一致。
- 内置模板：`claude` 与 `codex` 加 `InteractiveArgs: []string{}`（裸 TUI，已在 ConPTY 上验证过），`tty-claude`/`tty-codex` 保留不动。用户 overlay 时按现有"整体覆盖"规则，不做字段级合并。
- 配置加载校验（`internal/config` 或 agent registry 解析处）：`interactive: true && args 含 {{prompt}}` → 报错并提示改用 `interactive_args`；`interactive_args` 含 `{{prompt}}` → 报错；`type: exec` 带 `interactive_args` → 报错。

### 2. 项目：`allow_interactive` 总开关，`interactive_allowed_agents` 退为可选收窄

```yaml
projects:
  work-tools-dev:
    allowed_agents: [codex, claude, exec]
    allow_interactive: true          # 默认 false；与 allow_exec、worker guards.allow_interactive 同一套词汇
    # interactive_allowed_agents: [claude]   # 可选：只想放行其中一部分时才写
```

交互 job 放行条件（全部满足）：项目 `allow_interactive` 为真；agent ∈ `allowed_agents`（`allowed_agents` 为空沿用现状语义）；agent 有交互模式；若 `interactive_allowed_agents` 非空则 agent 也在其中；runner 为 local 或 worker 且 worker `allow_interactive` 未显式关闭。

兼容：加载时若 `interactive_allowed_agents` 非空而 `allow_interactive` 未写，视为 `allow_interactive: true`（并在日志 warn 一次建议显式写出）；`interactive_allowed_agents` 为空且 `allow_interactive` 未写 → 关闭，与现状一致。

### 3. 能力上报与 web

- `wsproto.AgentBrief` 增加 `Batch bool`、保留 `Interactive bool`（含义改为"有交互模式"）；旧 worker 不上报 `Batch` 时 server 视为 `Batch = !Interactive`（与旧语义等价）。
- `/v1/meta` agents 同样带两个能力位；`NewJob.vue` 收窄改为：普通 job 看 `batch`，交互 job 看 `interactive && project.allow_interactive && (收窄列表为空或包含)`。
- `Projects.vue` 表单：复选框「允许交互 job」（`allow_interactive`），高级折叠里可选多选「仅限这些 agent」（`interactive_allowed_agents`，候选 = allowed_agents 中有交互模式者）；`PUT /v1/projects/{key}` 以现有配置为基底只覆盖显式给出的字段（修 h-aii-3scy 的丢字段）。

## 安全与回滚

- 默认不放宽：项目开关默认 false；旧 worker 的能力位按旧语义推导；`tty-*` 与所有现有 yaml 不改即等价。
- 回滚：不写 `interactive_args`、不写 `allow_interactive` 即回到现状；新字段全部 `omitempty`，旧二进制读取新 yaml 会忽略未知字段（需确认 yaml 解析非 strict；若 strict 则在文档注明升级顺序）。

## 决策

- 用显式 `interactive_args` 表达交互 argv，不从批处理 args 里"去掉 exec 和 {{prompt}}"推导。
- 项目开关命名 `allow_interactive`，与 `allow_exec` 对齐；`interactive_allowed_agents` 不删除、降级为可选收窄。
- 误配在加载期失败，不再依赖提交期拒绝。

## 非目标

- 不改 pty 会话协议、attach、录制；不改 worker guard 语义；不做 agent 字段级 overlay 合并。

## 验收

- 同一 `codex` key 能分别以 `job run` 与 `job run --interactive` 提交并各自按正确 argv 启动（单测断言 argv）。
- `interactive: true` + `{{prompt}}` 的配置在 serve 启动时报错，错误信息含字段名与修法。
- 未开 `allow_interactive` 的项目提交交互 job 被拒；开了且 agent 有交互模式则放行；`interactive_allowed_agents` 非空时按收窄生效；旧 yaml（非空列表、未写开关）自动视为开启。
- web 编辑项目后 yaml 中未在表单出现的字段保持不变；`/v1/meta` 与 worker 上报含两个能力位。
- `go test ./...` 无新增失败；`pnpm -C web build` 通过。
