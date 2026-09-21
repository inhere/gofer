# Web 控制台 · 配置查看/编辑写层（WEB-04③）设计

> 修订记录
> | 版本 | 日期 | 修改人 | 说明 |
> |---|---|---|---|
> | v0.1 | 2026-07-03 | claude | 初稿：V1 聚焦 projects CRUD（走 Registry，live）+ 全配置脱敏只读；server/secret 编辑显式排除 |

关联：v3 主设计 [`2026-07-02-web-console-v3-design.md`](2026-07-02-web-console-v3-design.md)（§3 明确把本条排除、留独立设计）· 配置简化 [`2026-06-22-config-simplification-design.md`](2026-06-22-config-simplification-design.md)（D2 准入真源在 serve、全局 vs 项目 overlay 边界）· 写层前身 WEB-09 交互应答 [`2026-07-02-web-console-v3-design.md` §11 身份分级] + runbook `2026-07-02-web-interaction-answer-runbook.md`。

## 1. 概览

WEB-04 的拓扑图 + 节点面板（只读）已落地（web v2 P4）。本设计补 **③ 配置查看/编辑写层**：在 Web 上**只读查看全量配置（secret 脱敏）** + **编辑项目注册表（project mappings + 准入）**。

核心取舍：**V1 只让「走 `project.Registry` 的 projects 域」可写**——因为它①是 Core 的 live 内存源（改即生效，无需 reload/重启）②天然无 secret ③已有 Add/Remove/Save 封装。**server / callers / token / governance / agents(含 env secret) / runners 一律只读**（改这些要么触碰热重载盲区、要么涉及 secret 明文，风险与工作量都大，V1 排除、走"文件编辑 + 重启"）。这样 V1 绕开两个最硬的坑，仍交付真实价值（免 SSH 增删改项目映射）。

## 2. 名词

- **受管配置**：`config.Config`（server/storage/projects/agents/runners/roles/supervisor/presence/schedule），落盘全局 `~/.config/gofer/config.yaml`（可被 `GOFER_CONFIG`/`GOFER_CONFIG_DIR` 重定向）。
- **Registry**：`internal/project.Registry`，Core 持有的**项目注册表 live 内存源** + `Add/Remove/save`（save 走 `config.Save` 写回全局文件，保留未知顶层 key）。改 Registry = 立即对 Core 生效（后续 job 提交即用新映射），无需 SIGHUP。
- **准入字段**：`allowed_agents / allowed_runners / allow_exec`——D2 规定其真源恒在**全局** config，**绝不**进项目 overlay。故 Web 编辑准入只能落全局（Registry 正是全局），天然合规。
- **secret 字段**：`*.token`（明文）/`*.token_env`（env 变量名）/`webhooks[].secret_env`/`agents[].env`(可能含明文密钥)。

## 3. 范围

**做（V1）**：
- 全量配置**脱敏只读**视图（`GET /v1/config`）：手写白名单 view DTO，secret 一律以布尔 `set: true/false` 呈现，**永不回传值**（SR403/805）。
- **项目 CRUD 写**：新增 / 编辑 / 删除项目映射 + 准入字段，经 `project.Registry` 落全局 config（live 生效）。
- 写操作**鉴权分级**：新增 `can_admin` 能力位 + `require_admin_capability` 开关闸（仿 `can_answer`）。
- 编辑前后**结构校验**：复用 `project.Registry.Validate` + `config.validate`，非法拒绝不落盘。

**不做（V1 显式排除，view-only + 文件编辑+重启）**：
- server（addr/token/callers/workers/metrics/governance/runner_probe/notification）编辑——触碰 httpapi 启动快照热重载盲区（改了要重启进程），且 callers/token 涉 secret。
- agents / runners / roles 编辑——`agents[].env` 可能含明文 secret，编辑需 secret 写入策略（另设计）。
- secret **值**的 Web 录入/修改——一律不经 HTTP 传明文 secret；如需，仅允许改 `token_env`（env 名，非值）留后续。
- 程序化触发 server 级热重载 / 重启（无既有入口，另设计 CFG reload）。
- WEB-03 pty 交互、CFG-03 主机侧编辑器调起（三个独立条目，不在此）。

## 4. 已确认（依赖既有约定，SR1130 不赘述）

- D2 准入真源在 serve、准入字段禁入 overlay：Web 编辑准入落全局 Registry，合规。
- SR403/805：secret 不回显、不入日志。
- 写端点在 authed `/v1` group（bearer→caller_id），非匿名（同 WEB-09）。

## 5. 架构

```txt
Web(Config.vue)
  │  GET /v1/config                → 脱敏只读全量视图
  │  GET /v1/projects, /{key}      → 项目明细(已有)
  │  POST /v1/projects             → 新增项目   ┐
  │  PUT  /v1/projects/{key}       → 编辑项目   ├─ 经 can_admin 闸
  │  DELETE /v1/projects/{key}     → 删除项目   ┘
  ▼
httpapi handler ── callerMayAdmin(caller) 闸 ──▶ project.Registry.Add/Update/Remove
                                                   │ (live 内存改 + config.Save 写盘)
                                                   ▼
                                             Core.Projects(live) + ~/.config/gofer/config.yaml
```

关键：项目写走 Registry = **改即对 Core 生效**（Registry 就是 Core 的 live 源），无热重载盲区问题——这正是选它做 V1 唯一可写域的根因。

## 6. 模块

- `internal/httpapi/config_handler.go`（新）：`handleGetConfig`（脱敏视图）。
- `internal/httpapi/project_handler.go`（扩）：加 `handleCreateProject` / `handleUpdateProject` / `handleDeleteProject`，各自先过 `callerMayAdmin`。
- `internal/httpapi/server.go`（扩）：`callerMayAdmin(caller) bool`（仿 `callerMayAnswer`）；注册 4 条新路由。
- `internal/config/model.go`（扩）：`CallerConfig.CanAdmin bool` + `GovernanceConfig.RequireAdminCapability bool` + `ServerConfig.CallerCanAdmin(id) bool`（仿 CanAnswer 三件套）；loader `validate` 加 fail-fast（开闸需至少一个 can_admin caller）。
- `internal/project/registry.go`（按需扩）：确认 `Add` 已够；补 `Update(key, ProjectConfig)`（若无）——Load→改 projects[key]→Save，复用现有 save。
- `web/src/api/client.ts` + `types.ts`（扩）：`getConfig` / `createProject` / `updateProject` / `deleteProject` + `ConfigView` 类型。
- `web/src/views/Config.vue`（新）或并入 `Projects.vue`：脱敏配置只读区 + 项目编辑表单（准入多选、路径、agent 白名单）。

## 7. 关键流程

**查看**：`GET /v1/config` → handler 取 `s.core.Cfg`（或等价快照）→ 手写 `configView`（逐段白名单，secret 字段映射成 `token_set: bool`）→ JSON。**不 `c.JSON(cfg)` 裸序列化**（Config 无 json tag 且不脱敏）。

**编辑项目**（`PUT /v1/projects/{key}`）：
1. `callerMayAdmin(callerFromCtx(c))` 否则 403。
2. bind `projectWriteReq`（host_path/container_path/subdir/default_agent/allowed_agents/allowed_runners/allow_exec/max_concurrent_jobs...；**无 secret**）。
3. `Registry.Update(key, cfg)`：内部 Load 快照→校验（路径存在、default_agent∈allowed_agents、runner 引用合法，复用 `Registry.Validate`）→ 非法返错不落盘→合法则改内存 + `config.Save` 写盘。
4. 返回更新后的 `projectView`。live 生效（后续 job 提交即用新准入）。

**新增/删除**同理走 `Registry.Add/Remove`。

## 8. 数据模型

无新表（配置是文件真源，非 DB）。仅新增内存 view DTO + 请求 DTO。

## 9. API

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/v1/config` | authed | 全量配置脱敏视图（secret→bool） |
| POST | `/v1/projects` | authed + can_admin | 新增项目 |
| PUT | `/v1/projects/{key}` | authed + can_admin | 编辑项目（含准入） |
| DELETE | `/v1/projects/{key}` | authed + can_admin | 删除项目 |

响应沿用现有 `writeError` / view DTO 风格。

## 10. 安全

- **secret 永不出网**：view DTO 手写白名单，secret 段只给 `xxx_set: bool`；请求 DTO 不含任何 secret 字段（V1 不经 Web 改 secret）。
- **写鉴权**：`can_admin` 能力位 + `require_admin_capability` 闸（默认见待确认#2）；config 写比 answer 更重，独立能力位不复用 can_answer。
- **落盘安全**：复用 `config.Save`（保留未知顶层 key、`0o644`）；写前 `Registry.Validate` 拒非法，避免写坏 config 致 serve 下次重启起不来。
- **准入边界**：编辑准入字段只落全局（Registry 即全局），不触 overlay（D2）。
- **审计**：写操作记事件（仿 interaction.punted），detail 含 caller_id + 操作（project.created/updated/deleted）——可选，建议加。

## 11. 部署

前端改动，需主机 `make install` + 重启 serve 生效（同 P1/P2）。V1 无 DB 迁移、无新中间件。

## 12. 已确认（2026-07-03 拍板）

1. **V1 可写范围 = 仅 projects CRUD**（✅ 采纳推荐）。governance/retention/agents 等留 V1.1（各需解重启生效 or secret 写策略）。
2. **`can_admin` 闸默认关闭**（✅ 采纳推荐，仿 can_answer 向后兼容）：默认任何 authed caller 可编辑项目；runbook 强烈建议开 `require_admin_capability` + 配独立 admin token。
3. **`GET /v1/config` 回全量受管配置（脱敏）**（✅ 采纳推荐）：secret 段 bool 化，只读总览有价值无泄露。
4. **V1 不做 `POST /v1/config/reload`**（✅ 采纳推荐）：projects 走 Registry 已 live 生效，无需；留 agents 阶段一起做。

## 13. 结论

V1 以「projects CRUD（Registry live）+ 全量脱敏只读」交付 WEB-04③ 的可用子集，**刻意避开** server 字段热重载盲区与 secret 明文编辑两大风险面，复用 `project.Registry` + `config.Save` 现成原语，新增面小、可测、live 生效。server/secret 编辑作为 V1.1+ 独立推进（需先解热重载与 secret 写策略）。待确认 4 项拍板后出实施 plan。

## 14. 外科写回（2026-09-21，bd h-aii-kd57）

`Registry.save → config.Save`（`internal/config/writer.go`）不再整篇重排，改为按**顶级键**做外科手术：

1. 原文用**同一 decoder** 解析成 `Config`（刻意**不跑 `ApplyDefaults`**：问的是"这次保存是否改动了文件写下的东西"，而不是"内存里的配置是否已补全默认值"——否则每次保存都会顺手重写 server/storage 等只为内存补默认的块），再 `yaml.Marshal` 得到每个托管键的规范化渲染；
2. 新配置同样渲染，**逐键比较渲染字节**：
   - 相等 → 该键从**原文文本**原样切出（注释、键序、空列表情写全保留，连内联注释前的空格都不动；改自 AST 重排会在这一步把 `args: [acp]      # note` 规范化掉）；
   - 不等 → 用新渲染整块替换（**块内**注释会丢，块头的注释保留）；
   - 新配置没有的键 → 整块删除；原文没有的托管键 → 追加到文件末尾；
3. 非托管顶级键（`custom_top` 之类）一律原样保留。托管键集合由 `Config` 的 yaml tag 反射推出，不再靠手写表（此前 `log`/`session` 漏登记 → 同一个键既被结构体写出、又被当未知键追加，文件里出现重复块）。

同一批修掉的两处同源缺陷：

- **`interactive_args: []` 每次写回消失**（AGT-02：空列表 = "支持交互、裸启动"）。`AgentConfig.InteractiveArgs` 由 `[]string` 改为 `config.ArgList`（实现 goccy 的 `IsZeroer`，只有 `nil` 算零值），于是 `[]` 显式写回、不写则键消失。读它的地方（`agent.Modes`/`agent.Build`/`config.validate`）语义不变 —— 空列表本来就是"启用交互"。
- **带内联注释的键被当作未知键**：`agents: # 注释` 的 `Key.String()` 含注释，`managedTopKeys` 查不中 → 该块被整块追加。判键改用 key token 的值。

web 侧无需改动：项目设置保存与 `project add/update` 都走 `Registry.save`，因此"改项目"不再触碰 agents 块。测试：`internal/config` 的 `TestSaveKeepsEmptyInteractiveArgs` / `TestSaveOnlyRewritesChangedTopLevelKeys` / `TestSaveNewTopLevelKeyAppended` / `TestInteractiveArgsEmptyRoundTrip`，`internal/project` 的 `TestRegistrySaveDoesNotTouchAgents`。
