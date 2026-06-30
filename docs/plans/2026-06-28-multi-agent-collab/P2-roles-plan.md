# P2 · L3 角色（E35）实施计划

> 主纲 [`../2026-06-28-multi-agent-collab-plan.md`](../2026-06-28-multi-agent-collab-plan.md) · design §7/§8.5/D5 · bd `gofer-fl46`
> 目标：命名 role 预设（reviewer/bugfix…）= base agent + system_prompt + 默认 project/tags；经 per-agent `system_inject` argv 模板注入 system prompt（类比 SessionInject），`job run --role` / `bridge_run_job(role=)` 一键运行。**resume 须重施 system_inject**（复审 #5）。

## 锚点（file:line）

- config：`config/model.go:19-25`(Config 顶层) `371-387`(AgentConfig) · `config/loader.go:65-97`(Load) `240-263`(ApplyDefaults)
- agent：`agent/template.go:9-15`(Vars) `19`(replacements) `38-45`(Render) · `agent/registry.go:89-98`(builtinSessionDefaults)
- submit 注入点：`job/submit.go:143-149`(SessionInject 追加 argv) · 流转 `110/147/161-163/191`
- resume：`job/resume.go:40-83`(ResumeJob，argv 拼接 63)
- JobRequest：`job/model.go:9-107`
- CLI：`commands/job.go:95-216`(NewJobCmd run 子命令) · mcp：`mcpserver/server.go:234-268`(runJobInput/runJobHandler)

---

## P2.1 config：roles 段 + AgentConfig.SystemInject

**`config/model.go`**：

```go
// Config 顶层加字段（line 19-25 内）
Roles map[string]RoleConfig `yaml:"roles"`

// 新增 RoleConfig（design §8.5）
type RoleConfig struct {
    Agent        string   `yaml:"agent"`          // base agent（claude/codex）
    SystemPrompt string   `yaml:"system_prompt"`  // 常驻 system prompt
    Project      string   `yaml:"project"`        // 可选默认项目
    Tags         []string `yaml:"tags"`           // 可选默认标签
    // Rules/Context 文件挂载属 E11，本 P 不做（design §12 留后续）
}

// AgentConfig 加字段（line 371-387 内）—— per-agent system prompt 注入模板
// 非空 + 有 system_prompt 时，submit 渲染追加到 argv（类比 SessionInject）
SystemInject []string `yaml:"system_inject"`
```

**`config/loader.go` ApplyDefaults**（240-263）加：`if cfg.Roles == nil { cfg.Roles = map[string]RoleConfig{} }`。

**`agent/registry.go` builtinSessionDefaults**（89-98）补 system_inject 默认（与 session 默认同结构合并）：
```go
"claude": { SystemInject: []string{"--append-system-prompt", "{{system_prompt}}"}, SessionInject: ..., SessionResume: ... },
"codex":  { /* codex 等价参数, plan 落地时实测确认; 不确定则留空=不注入 */ },
```
> ⚠️ codex 的 system prompt 注入参数需**实测确认**（design §12）；不确定先留空（role 对 codex 仅生效 project/tags 默认，不注 system_prompt），不要瞎填。

- [x] 单测：`config_test` 解析含 `roles:` 的 yaml；ResolveAgent 合并 builtin system_inject。
- [x] `go test ./internal/config/... ./internal/agent/...` 绿 → commit `feat(roles): config roles 段 + AgentConfig.SystemInject + claude 内置默认`

---

## P2.2 agent：Vars.SystemPrompt

**`agent/template.go`**：
```go
// Vars 加字段（line 9-15）
SystemPrompt string
// replacements()（line 19 返回数组）加一对
"{{system_prompt}}", v.SystemPrompt,
```
- [x] 单测 `template_test`：Render 替换 `{{system_prompt}}`；argv 结构保持（不 join shell）。
- [x] commit（可与 P2.1 合并提交）`feat(roles): agent Vars.SystemPrompt + 模板替换`

---

## P2.3 job：Role 解析 + system_inject 注入 + resume 重施

**`job/model.go` JobRequest 加字段**（9-107）：
```go
Role         string `json:"role,omitempty" yaml:"role,omitempty"`           // E35 角色预设引用
SystemPrompt string `json:"system_prompt,omitempty" yaml:"system_prompt,omitempty"`  // 解析后/直传的 system prompt
```

**`job/submit.go` role 解析**（在 validate 后、构建 runReq 前，约 line 90-100 区段插入）：
```go
// E35: role 解析——role 预设填充缺省字段，显式入参优先
if req.Role != "" {
    rc, ok := cfg.Roles[req.Role]
    if !ok { return JobResult{}, fmt.Errorf("%w: role %q", ErrUnknownRole, req.Role) }
    if req.Agent == "" { req.Agent = rc.Agent }
    if req.SystemPrompt == "" { req.SystemPrompt = rc.SystemPrompt }
    if req.ProjectKey == "" { req.ProjectKey = rc.Project }
    if len(req.Tags) == 0 { req.Tags = rc.Tags }
}
```

**`job/submit.go` system_inject 注入**（紧邻 SessionInject 143-149，同 `if ac,ok:=s.agents.Get(req.Agent)` 块内或并列）：
```go
// E35: system_inject —— 有 system prompt + agent 配了 system_inject 模板 → 渲染追加 argv（类比 SessionInject）
if ac, ok := s.agents.Get(req.Agent); ok && len(ac.SystemInject) > 0 && req.SystemPrompt != "" {
    runReq.Args = append(runReq.Args, agent.Render(ac.SystemInject, agent.Vars{SystemPrompt: req.SystemPrompt})...)
}
```
> 与 SessionInject 共存：claude `--append-system-prompt <p>` 与 `--session-id <id>` 独立 flag、顺序无关。

**`job/resume.go` 重施 system_inject（复审 #5，关键）**：
源 job 须能取回 role/system_prompt（已存于 RequestJSON）。`ResumeJob`(40-83) 拼 argv 后追加：
```go
// 还原源 job 的 system_prompt（同 cwdFromRequestJSON 手法）
sysPrompt := systemPromptFromRequestJSON(src.RequestJSON)
argv := append([]string{ac.Command}, agent.Render(ac.SessionResume, agent.Vars{SessionID: src.SessionID, Prompt: prompt})...)
// resume 走 exec 载体绕过 SystemInject → 此处显式重施，否则 role 行为静默丢失
if len(ac.SystemInject) > 0 && sysPrompt != "" {
    argv = append(argv, agent.Render(ac.SystemInject, agent.Vars{SystemPrompt: sysPrompt})...)
}
```
> ⚠️ 落地时**实测**：claude `--resume` 是否确需重传 `--append-system-prompt`（design §12）。若实测 `--resume` 已恢复原 system prompt，则此步改为不重施 + 注释说明；二者择一，以实测为准。新增 `systemPromptFromRequestJSON`（照 `cwdFromRequestJSON` 89-98）。

- [x] 单测：role 解析填充缺省（agent/project/tags/system_prompt）+ 显式优先；submit argv 含 `--append-system-prompt`；未知 role→ErrUnknownRole；resume argv 重施（或按实测注释）。
- [x] `go test ./internal/job/...` 绿 → commit `feat(roles): JobRequest.Role 解析 + system_inject 注入 + resume 重施`

---

## P2.4 CLI + mcp

**`commands/job.go` run 子命令加 flag**（Config 内，照 95-216）：
```go
c.StrOpt(&jobRunOpts.role, "role", "", "", "role preset (fills agent/system_prompt/project/tags)")
c.StrOpt(&jobRunOpts.systemPrompt, "system-prompt", "", "", "override system prompt (advanced)")
```
runJobRun 把 role/systemPrompt 塞进 JobRequest。

**`mcpserver/server.go` runJobInput 加字段**（234-244）：`Role string \`json:"role,omitempty"\``（+ 可选 system_prompt）；runJobHandler 透传到 JobRequest（253-262 区段）。

- [x] 单测：CLI flag 绑定；mcp runJobInput role 透传。`go test ./...` 绿。
- [x] **冒烟**（部署后）：config 配一个 `roles.reviewer{agent:claude, system_prompt:"You are a strict reviewer"}`；`gofer job run --role reviewer -p "..."`；`gofer job show <id>` 看 rendered_command 含 `--append-system-prompt`；（如可）resume 该 job 看 argv 重施。
- [x] 回填主纲 + commit `feat(roles): job run --role + bridge_run_job role 字段 + 冒烟`。bd `fl46` close。

## 验收总清单（P2 Done 标准）

- roles 段解析 + SIGHUP 热载（Registry 已支持）；role 填充缺省、显式优先；未知 role 报错。
- submit argv 正确注入 system_prompt（claude）；codex 按实测（确认或留空）。
- **resume 不丢 role**（重施 system_inject 或实测确认 --resume 自带，二者择一且注释清楚）。
- `go test ./...` 全绿；冒烟 rendered_command 可见 system prompt 注入。
