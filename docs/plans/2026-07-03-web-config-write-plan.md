# Web 配置写层（WEB-04③）实施计划

> 设计：[`../design/2026-07-03-web-config-write-design.md`](../design/2026-07-03-web-config-write-design.md)（v0.1，§12 决策已拍板）。
> V1 范围：**projects CRUD（走 `project.Registry`，live 生效）+ 全量配置脱敏只读 + `can_admin` 能力闸（默认关）**。server/secret/governance/agents 编辑 V1 排除。
> SUPMODE + host codex 实施、容器验收。每子任务完成即 commit（SR1202）。

## 现状锚点（已核实，勿重查）
- `Server` 持有 `s.projects *project.Registry`（`server.go:67`）+ `s.cfg *config.ServerConfig`（`server.go:64`）。
- `Registry`：`Config() *config.Config`（全量快照，供脱敏视图）/`List()`/`Get(key)`/`Add(key,proj,force bool)`（force=false 拒重、true 覆盖）/`Remove(key)`/`Validate(key)([]CheckResult,bool,error)`。**无 Update**——create=`Add(k,p,false)`，update=`Add(k,p,true)`。
- `project_handler.go`：`projectView`（key/host_path/container_path/default_agent/allowed_agents/allowed_runners/allow_exec/max_concurrent_jobs，无 secret）+ `handleListProjects`(s.projects.List)/`handleGetProject`(s.projects.Get)。路由 `server.go:258-265` GET /projects[/{key}][/git|repos|file]。
- P2 鉴权样例：`callerMayAnswer`（`interaction_handler.go:152`）+ `CallerConfig.CanAnswer`/`Governance.RequireAnswerCapability`/`ServerConfig.CallerCanAnswer`（`model.go`）+ loader `validate` fail-fast（`loader.go:290`）。**can_admin 三件套完全仿此**。
- `Config` 结构**无 json tag 且不脱敏**——`GET /v1/config` **必须手写 view DTO**，禁止 `c.JSON(cfg)` 裸序列化。

## 进度跟踪
- [x] **C1** 后端 · `can_admin` 能力位 + 闸（model/loader/callerMayAdmin）— 7900d1d
- [x] **C2** 后端 · `GET /v1/config` 脱敏只读视图 — 96920d1
- [x] **C3** 后端 · 项目 CRUD 写端点（create/update/delete）+ 校验 + 路由 + 审计事件 — ca4c3ed
- [x] **C4** 前端 · client + types + Config.vue（脱敏总览 + 项目编辑表单）— efa3844
- [x] **C5** 文档 · runbook admin token 节 + roadmap WEB-04③ ✅ — runbook 2026-07-03-web-config-edit-runbook.md + roadmap v1.29

---

## C1 后端 · can_admin 能力位 + 闸

**仿 P2.2 can_answer 三件套**：
- `internal/config/model.go`：`CallerConfig` 加 `CanAdmin bool \`yaml:"can_admin"\``（注释：可编辑配置/项目的能力位，仅 require_admin_capability 开启时强制）；`GovernanceConfig` 加 `RequireAdminCapability bool \`yaml:"require_admin_capability"\``（默认 false=任何 authed caller 可写配置，向后兼容）；加 `func (sc *ServerConfig) CallerCanAdmin(callerID string) bool`（仿 `CallerCanAnswer`，逐条查 `cc.ID==callerID` 返回 `cc.CanAdmin`，空/未知 false）。
- `internal/config/loader.go` `validate`：在 governance 段加 fail-fast——`RequireAdminCapability==true` 时至少一个 caller `CanAdmin==true`，否则返错 `require_admin_capability is on but no caller has can_admin: true (would lock out all config edits)`。
- `internal/httpapi/server.go`：加 `func (s *Server) callerMayAdmin(caller string) bool { if s.cfg == nil || !s.cfg.Governance.RequireAdminCapability { return true }; return s.cfg.CallerCanAdmin(caller) }`。

**测试**：`internal/config` `CallerCanAdmin` 三态 + 加载 fail-fast 双向（仿 governance_test.go 现有 can_answer 用例）。

**验收**：`go build ./... && go test ./internal/config/...` 绿。

**commit**：`feat(config): can_admin capability + require_admin_capability gate (WEB-04③ C1)`

---

## C2 后端 · GET /v1/config 脱敏只读视图

**新** `internal/httpapi/config_handler.go`：`handleGetConfig`——取 `s.projects.Config()`（全量 `*config.Config`），手写 `configView`，**secret 一律 bool 化**。逐段白名单（示例，按 model 实际字段补全）：

```go
type configView struct {
    Server   serverView              `json:"server"`
    Storage  storageView             `json:"storage"`
    Projects []projectView           `json:"projects"`
    Agents   map[string]agentView    `json:"agents"`
    Runners  map[string]runnerCfgView `json:"runners"`
    Roles    []string                `json:"roles"`      // 仅 key 列表
    // supervisor/presence/schedule 纯行为参数，可原样带（无 secret）或按需
}
type serverView struct {
    Addr            string       `json:"addr"`
    PathView        string       `json:"path_view"`
    AllowEmptyToken bool         `json:"allow_empty_token"`
    WebEnabled      bool         `json:"web_enabled"`
    TokenSet        bool         `json:"token_set"`        // server.token/token_env 任一非空
    Governance      govView      `json:"governance"`
    Callers         []callerView `json:"callers"`          // 每条 secret→bool
    MetricsEnabled  bool         `json:"metrics_enabled"`
    MetricsTokenSet bool         `json:"metrics_token_set"`
}
type callerView struct { // 绝不含 token/token_env 值
    ID        string `json:"id"`
    TokenSet  bool   `json:"token_set"`   // Token!="" || TokenEnv!=""
    CanAnswer bool   `json:"can_answer"`
    CanAdmin  bool   `json:"can_admin"`
    // 配额 max_concurrent_jobs/rate_limit/rate_burst 可带（非 secret）
}
type runnerCfgView struct { // token_env 只给 bool
    Type       string `json:"type"`
    BaseURL    string `json:"base_url,omitempty"`
    TokenSet   bool   `json:"token_set"`
    WorkerID   string `json:"worker_id,omitempty"`
}
type agentView struct { // agents[].env 可能含明文 secret → 只给 env key 名，不给值
    Type        string   `json:"type"`
    Command     string   `json:"command,omitempty"`
    EnvKeys     []string `json:"env_keys,omitempty"` // 只列 env 变量名，绝不列值
    AllowRawCmd bool     `json:"allow_raw_cmd"`
}
```

- **铁律**：任何 `token`/`token_env`/`secret_env` 的**值**都不进 view；`agents[].env`/`roles[].env` **只列 key 名不列 value**（value 可能是明文密钥）。
- handler：`callerFromCtx` 已认证即可读（读不设 can_admin 闸——只读总览，authed 即可）；`s.cfg==nil` 兜底空视图。
- 路由 `server.go`：authed group 加 `r.GET("/config", s.handleGetConfig)`。

**测试**：`internal/httpapi` 造带 token/token_env/env 的 config → `GET /v1/config` 断言响应体**不含**任何 secret 值（grep token 明文不出现）、`token_set=true`、`env_keys` 只有名无值。

**验收**：`go test ./internal/httpapi/...` 绿 + 手测响应无 secret 泄露。

**commit**：`feat(httpapi): redacted GET /v1/config view (WEB-04③ C2)`

---

## C3 后端 · 项目 CRUD 写端点 + 校验 + 审计

`internal/httpapi/project_handler.go` 加：

```go
type projectWriteReq struct { // 无任何 secret 字段
    HostPath          string   `json:"host_path"`
    ContainerPath     string   `json:"container_path,omitempty"`
    DefaultAgent      string   `json:"default_agent,omitempty"`
    AllowedAgents     []string `json:"allowed_agents,omitempty"`
    AllowedRunners    []string `json:"allowed_runners,omitempty"`
    AllowExec         bool     `json:"allow_exec"`
    MaxConcurrentJobs int      `json:"max_concurrent_jobs,omitempty"`
}
```

- `handleCreateProject`（POST /projects，body 带 key 或单独字段）：`callerMayAdmin` 否则 403 → bind → **结构+引用预校验**（key 非空、host_path 非空、default_agent∈allowed_agents、allowed_agents 里每个 ∈ 全局 `s.projects.Config().Agents`、allowed_runners 引用合法）非法 400 → `s.projects.Add(key, proj, false)`（已存在返 409）→ `recordConfigEvent("project.created", caller, key)` → 返回 `projectView` + `Validate(key)` 的 CheckResults（路径存在性等作**非致命告警**返回，不回滚）。
- `handleUpdateProject`（PUT /projects/{key}）：同上但 `Add(key, proj, true)`（覆盖）；key 取 path 参数。
- `handleDeleteProject`（DELETE /projects/{key}）：`callerMayAdmin` → `s.projects.Remove(key)`（未知 404）→ `recordConfigEvent("project.deleted", caller, key)` → 200 `{status:ok}`。
- 路由 `server.go`（authed group）：`r.POST("/projects", ...)` / `r.PUT("/projects/{key}", ...)` / `r.DELETE("/projects/{key}", ...)`。
- **审计**：`recordConfigEvent` 走既有 job 事件流不合适（无 job 关联）——改用结构化日志（含 caller_id/action/project_key）或既有 audit 通道；先 grep 有无全局 audit/event sink，无则结构化日志留痕（回报说明选型）。

**校验取舍（明确）**：结构+引用错误（空 host_path、default_agent 不在 allowed、引用未定义 agent/runner）**硬拒 400/不落盘**；文件系统路径不存在**软告警**（路径可能后置创建），随响应返回不阻断。

**测试**：`internal/httpapi`——create 正常→200+Get 可见+config.yaml 写盘；create 重复→409；update 改准入→Get 反映新值；delete→Get 404；无 can_admin+开闸→403；引用非法 agent→400 不落盘。用 t.TempDir 的临时 config 路径（Registry 可注入 path）验证写盘。

**验收**：`go test ./internal/httpapi/... ./internal/project/...` 绿。

**commit**：`feat(httpapi): project create/update/delete write endpoints (WEB-04③ C3)`

---

## C4 前端 · client + Config.vue

- `web/src/api/types.ts`：加 `ConfigView`（对齐 C2 configView）+ `ProjectWriteReq`。
- `web/src/api/client.ts`：`getConfig()` / `createProject(req)` / `updateProject(key,req)` / `deleteProject(key)`（沿用 `request` 惯例；403/409 错误透传由调用方处理）。
- `web/src/views/Config.vue`（新，加路由 + 导航「舰队」组）：
  - **脱敏配置总览**（只读）：server（addr/path_view/web_enabled/token_set/metrics/governance）+ callers 表（id/token_set/can_answer/can_admin）+ agents（type/env_keys）+ runners（type/base_url/token_set）+ storage/roles。secret 全显示为 `已配置/未配置` 徽标。
  - **项目编辑区**：项目列表 → 编辑表单（host_path/container_path/default_agent/allowed_agents 多选/allowed_runners 多选/allow_exec/max_concurrent_jobs）+ 新增 + 删除（二次确认）。提交调 create/update/delete，成功刷新，失败（403 无 can_admin / 409 重名 / 400 校验）就地提示 + 显示 Validate 告警。
  - 复用现有 CRT 蓝灰 token/表单样式（参考 NewJob.vue/NewSchedule.vue 表单风格）。
- 可选：Cluster.vue/Projects.vue 加入口跳 Config.vue。

**验收**：主机 `pnpm typecheck` + `pnpm build` 绿（我容器不构建，走 host gofer job）；眼检留部署后。

**commit**：`feat(web): config overview + project editor (WEB-04③ C4)`

---

## C5 文档

- `docs/runbook/`（并入 `2026-07-02-web-interaction-answer-runbook.md` 或新 config 运维节）：配 admin token（`can_admin: true` caller）+ 开 `governance.require_admin_capability: true` 的说明 + 编辑项目准入落全局（D2）+ secret 不经 Web 改（走文件/env）的约定。
- roadmap `gofer-enhancements-roadmap.md`：WEB-04 ③ 标进展（V1 projects CRUD 落地；server/secret 编辑 V1.1 待）+ v1.29 修订行。

**commit**：`docs: WEB-04③ config write V1 runbook + roadmap`

---

## 统一验收 & 部署
1. 容器 `go build ./... && go test ./...` 全绿。
2. 前端主机 `pnpm build` 绿。
3. agent-browser 眼检（待部署后）：脱敏视图无 secret；新增/编辑/删除项目 live 生效（编辑准入后提交 job 反映）；开闸时无 can_admin caller 编辑 403。
4. 主机 `make install` + 重启 serve 部署。
5. 回填 plan checkbox + roadmap + `bd remember`。

## 安全要点
- secret 值**永不出网**（view bool 化、env 只列 key 名、写请求无 secret 字段）。
- 写端点 authed + 可选 can_admin 闸；写前引用校验拒非法不落盘。
- 准入编辑只落全局 Registry（D2），不触 overlay。
