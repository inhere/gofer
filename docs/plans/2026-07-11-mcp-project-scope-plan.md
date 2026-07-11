# gofer MCP 项目级作用域 — 实施计划

> bd: h-aii-xu64.15 ｜ 设计: `docs/design/2026-07-10-mcp-project-scope-design.md`(v0.2 定稿)
> 状态: **已完成**(2026-07-11)。T1-T4 全落, 全量 `go test ./... -p1 -count=1` 绿, 真实二进制 4 场景冒烟通过。提交: T1=a086bb5 / T2+T3=ac70831(编译耦合于 Serve/ServeLocal 边界, 合并为一次可构建提交)。

## 0. 决策基线(设计 §8,已批)

- 解析三态: **无 flag=operator 全量**(向后兼容) / `--project X` / `--project auto`(探测不出即报错,不静默退全量)。
- CWD 探测: `.gofer.project.yaml` 的 `key:` **为主**(本地无往返); standalone 兜底 `ProjectForPath`; `GOFER_PROJECT` env; 都无→报错。**不做 resolve 端点**。
- 工具边界三类: 收窄(list_projects/run_job) · 隐藏 operator-only(create_plan/attach_job) · 放行按 id(get_*)。
- 作用域=防误触**非鉴权**(serve 仍全量准入)。

## 1. 现状锚点(已核实)

- `internal/commands/mcp.go`: `runMcp` — client 分支 `mcpserver.Serve(ctx, NewClientBackend(cli))`(不建 Core); standalone `mcpserver.ServeLocal(ctx, cr.Jobs, cr.Projects, cr.Agents, cr.Presence)`。`mcpUseClient(standalone, serverAddr)` 判模式。
- `internal/mcpserver/server.go`: `Serve(ctx, b)` → `newServer(b, originAgent, originToken)` 注册 ~20 工具; `ServeLocal→Serve`; `New→newServer(b,"","")`。handler 是闭包工厂 `xxxHandler(b Backend[, originAgent])`。
- `internal/config`: `ProjectOverlay`(overlay.go:20, D5 白名单 + `detectForbiddenOverlayKeys`); `Config.ExecPath(p)`(model.go:671, 按 `server.path_view` 取 host/container); `ProjectConfig.HostPath/ContainerPath`。**无** `ProjectForPath`。

---

## T1 — config: `key:` 字段 + `ProjectForPath` + overlay `key:` 读取

**文件**: `internal/config/overlay.go`, `internal/config/model.go`(或新 `resolve.go`)

1. `ProjectOverlay` 加自我标识字段(overlay.go:20):
   ```go
   Key *string `yaml:"key"` // D5 自我标识: --project auto 的主探测源(仅解析用,不 merge 进 ProjectConfig)
   ```
   并把 `key` 加入 D5 白名单(`detectForbiddenOverlayKeys` 的允许集),否则会被判违禁键。
   > 注意: `key:` **不进** `MergeProjectConfig`(它是目录自我标识、非 project 设置)。

2. 新增读取 helper(读 CWD 的 `.gofer.project.yaml` 的 `key:`,client/standalone 通用、无需 cfg):
   ```go
   // ProjectKeyFromDir 读 dir/.gofer.project.yaml 的 key: 字段;无文件/无 key 返回 ""。
   func ProjectKeyFromDir(dir string) (string, error) {
       b, err := os.ReadFile(filepath.Join(dir, ProjectOverlayName))
       if errors.Is(err, os.ErrNotExist) { return "", nil }
       if err != nil { return "", err }
       var ov ProjectOverlay
       if err := yaml.Unmarshal(b, &ov); err != nil { return "", err }
       if ov.Key != nil { return strings.TrimSpace(*ov.Key), nil }
       return "", nil
   }
   ```

3. 新增 standalone path 匹配(仅 standalone 有全量 cfg):
   ```go
   // ProjectForPath 返回 cwd 落在哪个 project 的 ExecPath 下(前缀或相等);多命中取最长前缀,
   // 完全相等/歧义(两个等长命中)返回 (\"\", false)+调用方报错。无命中 (\"\", false)。
   // 注: cfg.Projects 是 map[string]ProjectConfig —— key 是 map 键(ProjectConfig 无 Key 字段)。
   func (c *Config) ProjectForPath(cwd string) (key string, ok bool) {
       cwd = filepath.Clean(cwd)
       best, bestLen, tie := "", -1, false
       for k, p := range c.Projects {
           ep := filepath.Clean(c.ExecPath(p))
           if ep == "" || ep == "." { continue }
           if cwd == ep || strings.HasPrefix(cwd, ep+string(filepath.Separator)) {
               if len(ep) > bestLen { best, bestLen, tie = k, len(ep), false } else if len(ep) == bestLen { tie = true }
           }
       }
       if best == "" || tie { return "", false }  // map 迭代序不定,但 tie 检测与序无关
       return best, true
   }
   ```

**验收 T1**: 表驱动单测 —
- `ProjectKeyFromDir`: 有 key / 无 key / 无文件 / 坏 yaml。
- `ProjectForPath`: 命中 / 最长前缀(嵌套) / 等长歧义→!ok / 无命中→!ok / path_view=container 分支。
- `go test ./internal/config/...` 绿。

---

## T2 — mcp.go: `--project` flag + 三态解析 + 贯穿 mcpserver

**文件**: `internal/commands/mcp.go`

1. flag(NewMcpCmd.Config 内):
   ```go
   c.StrOpt(&mcpOpts.project, "project", "", "", "scope MCP to one project: <key> | auto (env) | empty=operator(all)")
   ```
   `mcpOpts` 加 `project string`。

2. 解析 helper(纯函数,可单测; cfg 可为 nil=client):
   ```go
   // resolveScopedProject 把 --project flag 三态解析为 scoped key。空 flag→(\"\",nil)=operator。
   // \"auto\"→按序: .gofer.project.yaml key: → (cfg!=nil)ProjectForPath → GOFER_PROJECT → 报错。
   func resolveScopedProject(flag string, cfg *config.Config, cwd string) (string, error) {
       if flag == "" { return "", nil }               // operator(向后兼容)
       if flag != "auto" { return flag, nil }          // 显式 --project X
       if k, err := config.ProjectKeyFromDir(cwd); err != nil { return "", err } else if k != "" { return k, nil }
       if cfg != nil { if k, ok := cfg.ProjectForPath(cwd); ok { return k, nil } }
       if k := strings.TrimSpace(os.Getenv("GOFER_PROJECT")); k != "" { return k, nil }
       return "", fmt.Errorf("--project auto: 无法从 CWD(%s) / .gofer.project.yaml / GOFER_PROJECT 解析 project", cwd)
   }
   ```

3. 接入 `runMcp`(两分支都传 scoped;解析放各自分支,client 无 cfg 传 nil):
   ```go
   cwd, _ := os.Getwd()
   // client 分支:
   scoped, err := resolveScopedProject(mcpOpts.project, nil, cwd)
   if err != nil { return errorx.Failf(mcpExitErr, "%v", err) }
   ... mcpserver.Serve(ctx, mcpserver.NewClientBackend(cli), scoped)
   // standalone 分支(cfg 已 Load):
   scoped, err := resolveScopedProject(mcpOpts.project, cfg, cwd)
   if err != nil { return errorx.Failf(mcpExitErr, "%v", err) }
   ... mcpserver.ServeLocal(ctx, cr.Jobs, cr.Projects, cr.Agents, cr.Presence, scoped)
   ```

**验收 T2**: `resolveScopedProject` 表驱动单测(空→operator / X→X / auto+key文件 / auto+ProjectForPath / auto+env / auto 全无→err)。`go test ./internal/commands/...` 绿。

---

## T3 — mcpserver: scoped 贯穿 + 工具边界过滤

**文件**: `internal/mcpserver/server.go`

1. 签名加 `scoped string`(空=operator,全链路透传):
   ```go
   func Serve(ctx context.Context, b Backend, scoped string) error { ... newServer(b, originAgent, originToken, scoped)... }
   func ServeLocal(ctx, jobs, projects, agents, pres, scoped string) error { return Serve(ctx, newLocalBackend(...), scoped) }
   func New(b Backend) *mcp.Server { return newServer(b, "", "", "") }   // 测试/无 scope
   func NewLocal(...) *mcp.Server { return New(newLocalBackend(...)) }
   func newServer(b Backend, originAgent, originToken, scoped string) *mcp.Server { ... }
   ```

2. `newServer` 内按 §4 三类:
   - **收窄类**:
     ```go
     mcp.AddTool(s, &mcp.Tool{Name:"gofer_list_projects", ...}, listProjectsHandler(b, scoped))
     mcp.AddTool(s, &mcp.Tool{Name:"gofer_run_job", ...}, runJobHandler(b, originAgent, scoped))
     ```
   - **隐藏类(operator-only)**——scoped != "" 时**跳过注册**:
     ```go
     if scoped == "" {
         mcp.AddTool(s, &mcp.Tool{Name:"gofer_create_plan", ...}, createPlanHandler(b))
         mcp.AddTool(s, &mcp.Tool{Name:"gofer_attach_job", ...}, attachJobHandler(b))
     }
     ```
   - **放行类**: get_job/get_plan/interactions 等注册不变。

3. handler 收窄逻辑:
   ```go
   // list_projects: 只回 scoped(operator 时全回)
   func listProjectsHandler(b Backend, scoped string) mcp.ToolHandlerFor[...] {
       return func(...) {
           entries, err := b.ListProjects(); if err != nil { return ..., err }
           if scoped != "" {
               entries = filterByKey(entries, scoped) // 只留 Key==scoped;空则回空列表(不报错)
           }
           return nil, listProjectsOutput{Projects: entries}, nil
       }
   }
   // run_job: 缺省即填,显式传别的则拒
   func runJobHandler(b Backend, originAgent, scoped string) ... {
       return func(_, _, in runJobInput) {
           if scoped != "" {
               if in.ProjectKey == "" { in.ProjectKey = scoped }
               if in.ProjectKey != scoped {
                   return nil, jobView{}, fmt.Errorf("project-scoped MCP(--project %s): 不能向 project %q 提交", scoped, in.ProjectKey)
               }
           }
           ... // 原逻辑不变
       }
   }
   ```

**验收 T3**: mcpserver 单测 —
- `newServer(scoped="")` 注册含 create_plan/attach_job;`newServer(scoped="X")` **不含**这两个(断言工具列表)。
- list_projects: scoped 只回该 project;operator 回全部。
- run_job: 缺 project→填 scoped;传别的→err;operator 不干预。
- `go test ./internal/mcpserver/...` 绿。

---

## T4 — 集成验证 + 收尾

1. 全量 `go build ./... && go vet ./... && go test ./... -p 1 -count=1`(禁缓存,改了 HTTP/mcp 契约面全量跑)。
2. 冒烟(容器 stdio,或起 test serve):
   - **operator**(无 `--project`): list_projects 回全部、create_plan 在工具列表——**行为不变**(向后兼容基线)。
   - **`--project X`**: list_projects 只回 X;run_job 缺 project→入 X、传 Y→拒;工具列表**无** create_plan/attach_job。
   - **`--project auto`**: 在带 `key: X` 的 `.gofer.project.yaml` 目录启动→scoped=X;无处可解析→**报错拒启**(非退全量)。
3. 更新设计 §状态、本计划勾选;bd close xu64.15;push。

## 验收总纲(端到端)

- [x] T1 config: `key:` 字段(黑名单机制**无需**白名单) / `ProjectKeyFromDir` / `ProjectForPath` + 单测绿
- [x] T2 mcp.go: `--project` 三态 + `resolveScopedProject` + 单测绿
- [x] T3 mcpserver: scoped 贯穿 + 三类过滤 + 单测(工具注册差异/收窄/拒绝)绿 · `project_key` 改 omitempty 支持缺省即填
- [x] T4: 全量 test 绿 + 4 场景冒烟(operator 21 工具不变 / demo 19 / auto→demo / auto 无解→exit2 快失败) + push

## 风险 / 边界

- **向后兼容**: operator(无 flag)零行为变化——T4 首个冒烟即基线。
- **非鉴权**(设计§7): 仅 client 侧暴露过滤,serve 仍全量准入——不因此免校验。
- 契约面: 改了 mcpserver 工具注册/handler + Serve/ServeLocal 签名,调用点仅 mcp.go(+测试),全量 test 兜底。
