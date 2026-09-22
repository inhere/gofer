# Web 配置查看/编辑（WEB-04③）使用 Runbook

> 配套 design [`../design/2026-07-03-web-config-write-design.md`](../design/2026-07-03-web-config-write-design.md) / plan [`../plans/2026-07-03-web-config-write-plan.md`](../plans/2026-07-03-web-config-write-plan.md)。写层身份分级与 [交互应答 runbook](2026-07-02-web-interaction-answer-runbook.md) 同源（caller token + 可选能力位）。

## 是什么

功能分三处（IA 调整后）：

- **系统配置总览** —— 独立 **`⚙ 设置`** 入口（顶栏末尾/侧栏，与内容分组分隔）：`GET /v1/config` 展示全量受管配置——server / callers / agents / runners / storage / governance 等。**所有 secret 只显示「已配置 / 未配置」徽标**（`*_set`），token/密钥的值与 env 变量名一律不出网；`agents`/`roles` 的 `env` 只列 **key 名**（`env_keys`）不列值。
- **项目编辑（写）** —— 在 **`Projects` 页**（舰队组，读+写）：新增 / 编辑 / 删除项目映射 + 准入字段（`allowed_agents` / `allowed_runners` / `allow_exec` / `default_agent` / `max_concurrent_jobs` / 路径）。经 `project.Registry` 落**全局** `~/.config/gofer/config.yaml`，**live 生效**（无需重启，后续 job 提交即用新映射/准入）。
- **agents / server 编辑（写，V1.1）** —— 在 **`⚙ 设置` 页**：Agents 卡片每行「编辑 / 删除」+ 顶部「新增 agent」，Server 卡片「编辑」。写路径与项目写同一条事务（`Core.Update`：克隆 → 应用 → 全量校验 → 落盘 → 热重载），**保存即生效**，校验失败不落盘、不半途生效。详见下一节。

## 在 web 里改配置（V1.1：agents / server）

### 能改什么

| 区块 | 可编辑（保存即生效，无需重启） | 不可编辑（改文件 + 重启） |
|---|---|---|
| `agents.<key>` | `type` / `command` / `args` / `interactive_args` / `interactive` / `read_only_args` / `session_inject` / `session_capture` / `session_resume` / `session_resume_interactive` / `system_inject` / `transient_error_patterns` / `fallback_agents` / `max_concurrent` / `stall_timeout_sec` / `retry` / `ndjson_*` / `acp`（补丁式，见下） | `env`（可能含明文 secret）、`detect`、`mcp_server_name`、`allow_raw_cmd`、`no_raw_cmd` |
| `server` | `max_job_timeout_sec` / `auto_resume_max` / `stall_timeout_sec` / `runner_probe` / `retry` / `notification` | `addr` / `token` / `token_env` / `allow_empty_token` / `path_view` / `callers` / `workers` / `metrics` / `governance` / `web_enabled` / `web_dir` / `web_base_url` / `job_recover_window_sec` / `agent_fallback` / `agent_health` / `xfer` / `dir_lock` / `storage.*` / 新增 runner 类型 |

这张表就是服务端 `internal/config/editable.go` 的字段策略表：`GET /v1/config` 把它作为 `server_policy` / `agent_policy` 发给控制台，写端点用**同一张表**拒绝越权字段（`400 field not editable: server.addr`）。控制台的表单是照它渲染的，所以"表单能填的"＝"服务端会收的"，两边不会漂移。

- **secret 只编辑"名字"，永不编辑值**：写请求体里出现 `token` / `secret` / `password` 这类**字面值**字段一律 `400 secret value not accepted: agents.x.token`（错误里点名该字段并提示改用 `*_env`）；`*_env` 是**环境变量名**，不是值，不会被当成 secret。控制台里 `env_keys` 只显示 key 名，`acp.mcp_servers` 的 env **值**不出网（YAML 预览里也是 `***`）。
- **agent PUT 是"整体替换"语义**：body 里没有的**可编辑**字段会被清空（控制台每次都会把整份可编辑字段集发出去，所以正常用它不会丢东西；用 `curl` 手写 body 时要注意）。**不在白名单里的字段由服务端原样保留**（`env` / `detect` / `mcp_server_name` 等），控制台也不会改写它们。
- `acp` 是唯一**补丁式**字段：只覆盖 body 里给出的成员（`permission_policy` / `modes` / `load_session` / `log_thoughts` / `mcp_servers`），其余保留——因为 `acp.mcp_servers[].env` 的值读不出来，整块替换会把它清掉。
- **删除一个 agent**：删掉的是"你在文件里的定义"。若该 key 对应**内置/运行时注入**的定义（控制台显「内置」徽标），删除后**回落到内置定义**（能力仍在），响应体里 `fell_back_to_builtin: true`，确认框也会写明。纯自定义 key 删除后直接消失。

### 外科写回：注释与"首次规范化"

`config.Save` 按**顶层块**外科写回：**没改动的顶层块（含你自己的顶层键、块间注释）逐字保留**。要注意两点：

- **被编辑块内部的注释会丢**：改了 `agents` 就重排 `agents` 块 → 该块内的行内注释没了。**建议**：长注释写在块外（独立 `#` 行）或未受管的顶层键里。
- **首次保存会把"内存里有默认值的块"规范化一次**：`serve` 启动后 `server` / `storage` / `log` 等块在内存里带着默认值，第一次从 web 保存时它们会被重写成规范形式（例如补上 `web_enabled` / 默认子目录 / `log:` 块）。这是既有写回行为（项目写入同理），不是本次改动引入；未被管理的顶层键原文照旧保留。

### 干跑校验、显式 reload、需重启的字段

- **干跑**：编辑弹窗每次改动都会调 `POST /v1/config/validate`（不落盘、不重载），右侧 YAML 预览与底部影响面都来自它的返回；校验失败会把 `error_fields` 里的字段路径**高亮到对应输入框**。
- **显式 reload**：页面顶部「重新读取文件」= `POST /v1/config/reload`，重新读取**本进程正在用的那个 config.yaml**（Windows 没有 SIGHUP，这是唯一的手动入口）。**在主机编辑器里手工改过文件后**用它生效——只对可热重载的项有效。
- **需重启的字段**（上表右列）：在主机改完 `config.yaml` 后执行 `start.ps1 -Action restart`。
- **并发编辑**：`Core.Update` 只在进程内串行。若你同时在主机编辑器里改同一个文件，磁盘上最后写入的一方获胜；控制台保存前请先「重新读取文件」一次，避免覆盖掉手工改动。

准入字段（`allowed_agents`/`allowed_runners`/`allow_exec`）真源恒在**全局** config（不进项目 overlay，配置简化 design D2）；Web 编辑正是落全局，合规。

## 写鉴权：can_admin 能力位

项目 / 配置的 create/update/delete 端点走 `can_admin` 能力闸，**opt-in、默认关闭**（=任何认证 caller 可编辑，向后兼容）。要把配置编辑收敛到 admin token：

```yaml
server:
  governance:
    require_admin_capability: true    # 开闸：仅 can_admin:true 的 caller 可写配置/项目
  callers:
    - id: web-admin
      token_env: GOFER_ADMIN_TOKEN
      can_admin: true                 # 可编辑配置/项目
      can_answer: true                # （按需）也可应答交互
    - id: web-op
      token_env: GOFER_WEBOP_TOKEN
      can_answer: true                # 只应答交互，不能改配置（无 can_admin）
```

- 开闸后，无 `can_admin` 的 caller 调写端点返回 **403**（前端就地提示，不崩页）。
- **防锁死**：开闸但无任何 caller `can_admin: true` 时，serve **加载即 fail-fast** 报错。
- 闸与 `can_answer` 相互独立：一个 caller 可同时具备或分别具备两种能力。

## 端点

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/v1/config` | authed | 全量配置脱敏视图（只读，任何认证 caller 可看），附 `server_policy` / `agent_policy` 字段策略表 |
| POST | `/v1/projects` | authed + can_admin | 新增项目（重名 409） |
| PUT | `/v1/projects/{key}` | authed + can_admin | 编辑项目（upsert） |
| DELETE | `/v1/projects/{key}` | authed + can_admin | 删除项目（未知 404） |
| PUT | `/v1/config/agents/{key}` | authed + can_admin | 新增/整体替换一个 agent 定义（白名单外字段 400） |
| DELETE | `/v1/config/agents/{key}` | authed + can_admin | 删除 agent 定义（未知 404；内置模板 key 回落内置，响应 `fell_back_to_builtin`） |
| PUT | `/v1/config/server` | authed + can_admin | **部分更新** server 白名单字段（越权 400 `field not editable: server.addr`） |
| POST | `/v1/config/validate` | authed + can_admin | 干跑校验：不落盘、不重载，返回 `{ok, errors, error_fields, applied, restart_required, preview}`（非法时 HTTP 400 + `ok:false`） |
| POST | `/v1/config/reload` | authed + can_admin | 重新读取本进程的 config.yaml（手工编辑后生效） |

写请求非法（空 key、越权字段、secret 字面值、校验不过）→ **400 不落盘**（`{error, detail, error_fields}`）；项目写对文件系统路径只作**非致命告警**（`warnings`）随响应返回（路径可后置创建）。写操作有两处审计：`slog`（caller_id + section + key + fields）+ 事件流 `config.updated`（scope `config`，detail `{section, key, by, fields}`，**只记字段名不记值**，不进通知默认集）。

## 安全要点（SR403/805 对齐）

- secret 值**永不出网**：视图 bool 化、env 只列 key 名、写请求无 secret 字段；写体里出现 `token`/`secret`/`password` 字面值一律 400 点名。
- 写端点在 authed `/v1` group（bearer→caller_id），非匿名；agents/server 写端点带 `can_admin` 闸。
- 准入编辑只落全局 Registry（D2），不触项目 overlay。
- 落盘前**全量**校验（`config.Validate` + `agent.ValidateConfig`），避免写坏 config 致 serve 下次重启起不来。

## 冒烟验证

1. 用具 `can_admin` 的 token 登录控制台。
2. `⚙ 设置` 页总览：确认 server/caller/agent 的 token 只显示「已配置/未配置」，无明文；agent env 只见 key 名。
3. `Projects` 页新增一个项目（填 host_path + 选 default_agent/allowed_agents）→ 保存 → 列表出现 → `GET /v1/projects/{key}` 可见。
4. 编辑其准入（如开 allow_exec）→ 保存 live 生效。
5. 删除 → 二次确认 → 列表移除。
6. （开闸时）用无 `can_admin` 的 token → 写操作 403 就地提示。
7. Agents 卡片「新增 agent」填 key/command → 保存 → 出现「已重载」toast、列表出现该 agent、`GET /v1/agents` 也能看到（=真热重载而不只是写盘）。
8. 该行「编辑」→ 把 `max_concurrent` 填成 `-5` → 弹窗右侧/输入框立刻标红（干跑 400），改成合法值后保存成功。
9. Server 卡片「编辑」→ 改 `max_job_timeout_sec` → 保存 → 总览数字变化（无需重启）；表单里「需重启」徽标列出的字段控制台不可编辑。
10. 删除一个带「内置」徽标的 agent → 确认框写明回落 → 删除后该 key 仍在（内置定义）。

