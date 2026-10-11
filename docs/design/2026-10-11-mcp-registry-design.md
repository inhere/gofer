# MCP 注册表：server 统一声明，按项目 / agent / job 注入（MCP-06）

- issue：gofer-0sxd
- 路线图：MCP-06（N4）
- 状态：已确认（2026-10-11，四项均按建议），实施中

## 一、背景与目标

现在想让 job 里的 agent 用上某个 MCP server（数据库、浏览器、内部 API……），只能在**每台执行机**的 agent 定义里手写：

- ACP agent：`agents.<k>.acp.mcp_servers`，值是明文 `env`，而且 web 配置页可以把明文写进去；
- cli-agent：没有任何注入，要自己在 `args` 里拼 `--mcp-config`，或者在执行机的 `~/.claude.json` / `~/.codex/config.toml` 里全局配置——全局配置会影响这台机器上所有会话，不分项目；
- worker 上的 job 用的是 worker 自己的 agent 定义，server 上配的不会带过去。

目标：

1. MCP server **在 server 上声明一次**（注册表），按 server / agent / project / job 四级**绑定**，和 skills、rules 的规则一致；
2. 派发时 gofer 按 agent 类型**自动注入**：ACP 会话走 `session/new` 的 `mcpServers`；claude / codex 等 cli-agent 生成各自的配置；
3. **凭据不进 argv、不进 request_json、不落盘**，配置里只写环境变量名；
4. 能看见：job 详情、`job show` 列出这次注入了哪些 MCP；`gofer agent mcp test <name>` 能验证一条声明能否启动、列出工具。

## 二、现状（事实，带出处）

- 管家（steward）是唯一的自动注入：`internal/job/steward.go` 的 `stewardMCPServer` 用本机 gofer 二进制拼一条 stdio 条目（`gofer mcp`，env 只放 job 凭据等四项），只对 ACP 会话生效，替换同名 `gofer` 条目。leader job 同一模式。
- ACP wire 只有 stdio 形态：`acp.MCPServer{name,command,args,env[]}`（`internal/acp/types.go`）；initialize 回包的 `mcpCapabilities` 只存 RawMessage、没解析。来源只有 `agents.<k>.acp.mcp_servers`（`submit.go` 的 `acpRequest` 原样复制）。
- cli-agent 侧唯一相关的是 codex 的 `McpEnvInjectArgs`：给 gofer 自己的 MCP 透传 job env（`-c mcp_servers.<name>.env.K=V`），注释写明 token 必须放 codex 的 config.toml、不能进 argv。
- skills（JOB-10）是现成的范式：四级并集 `EffectiveSkills`，提交时解析一次写回请求，`SkillsResolved` 阻止 worker 重复并集；本机复制到 `<result_dir>/skills/`，worker 走 staged upload（`Base=result_dir`），协议低于门槛的 worker 剥掉并记 `job.skills_skipped` 事件。
- rules（JOB-06①）同样四级并集，正文在提交方渲染进 prompt。
- job 级 env 进 request_json（注释「勿放 secret」），读出时 env 值整体打码；job 级 env **不随派发下发给 worker**（Dispatch / Forward 都没有 Env）。
- web 配置写层有 `SecretRef`：字段只存环境变量名（如 `server.token_env`），请求体里像 secret 的字段名一律拒绝。
- 协议当前 v22。

## 三、方案

### 3.1 注册表（config.yaml 顶层 `mcp_servers`）

```yaml
mcp_servers:
  pg-readonly:
    desc: 业务库只读查询
    type: stdio                      # stdio（默认）| http
    command: npx
    args: [-y, "@modelcontextprotocol/server-postgres", "{{env:PG_URL}}"]
    env:                             # 非敏感值，原样下发
      PGAPPNAME: gofer
    secret_env:                      # 敏感值：只写「执行进程里的变量名 ← 取值的环境变量名」
      PG_URL: GOFER_MCP_PG_URL
  issues-api:
    type: http
    url: https://mcp.example.internal/mcp
    secret_headers:                  # 同上，值来自环境变量
      Authorization: GOFER_MCP_ISSUES_AUTH
    tools: [search_issues, get_issue] # 可选：只允许这些工具（能表达的 agent 才生效，见 3.4）
```

- 字段：`desc`、`type`、`command`/`args`/`env`（stdio）、`url`/`headers`（http）、`secret_env` / `secret_headers`（值是环境变量名）、`tools`（可选白名单）、`enabled`（默认开）。
- 名字规则同 agent key；`gofer` 是保留名（管家 / leader 的注入继续由 gofer 自己管，见 3.7）。
- `args` / `url` 里可以写 `{{env:NAME}}` 引用某个 `secret_env` 变量，注入时由执行进程环境展开，argv 里只出现占位或变量引用，不出现明文（3.4 按 agent 说明怎么做到）。
- `config validate`：stdio 要有 `command`；http 要有 `url`；`secret_*` 引用的变量在**本机**不存在时给 `[WARN]`（worker 上的 job 在 worker 侧校验，见 3.5）。
- 可以被 `include` 拆到 `config.d/mcp.yaml`。web 配置页可编辑，`secret_*` 按 `SecretRef` 处理：只接受变量名。

### 3.2 绑定与并集

与 skills 相同的四级并集，按首次出现顺序去重：

| 级别 | 写法 |
|---|---|
| server | `server.mcp: [name…]`（所有 job 默认带上，一般留空） |
| agent | `agents.<k>.mcp: [name…]` |
| project | `projects.<k>.mcp: [name…]` |
| job | `job run --mcp name[,name]`（可重复）/ `--no-mcp`；plan todo、MCP 工具、web 表单同名字段 |

- `--no-mcp` 只关掉注册表注入，不影响管家 / leader 自带的 `gofer`。
- 提交时解析一次（名字不存在 → 400），结果写回 `JobRequest.MCP []string`（**只有名字**）并置 `MCPResolved`，续接 / 重跑沿用；exec agent 不注入。
- 同名优先级：agent 自己 `acp.mcp_servers` 里的同名条目 > 注册表（运维显式写在 agent 里的最具体）。以后视情况把 `acp.mcp_servers` 标 DEPRECATED，统一到注册表（G032）。

### 3.3 凭据：放哪里、怎么到 agent 手里

原则：**明文只存在于两个地方——取值的环境变量、agent 子进程的环境。** 不进 config 文件、argv、request_json、rendered command、acp.jsonl、日志。

- 取值：`secret_env` / `secret_headers` 的值是环境变量名，在**执行这条 job 的机器**上从 gofer 进程环境读取（server 本机 runner = server 进程；worker = worker 进程）。读不到 → 提交期在执行机拒绝（本机 job 返回 400，worker job 落 `failed` 并写明缺哪个变量），不静默跳过。
- 进入 agent：统一把明文放进 agent 子进程环境，变量名固定为 `GOFER_MCP_<NAME>_<KEY>`（大写、非字母数字转 `_`），配置文件 / argv / ACP 参数里只引用这个变量名：
  - ACP：`session/new` 的 `mcpServers[].env` 本身就是内存里的协议参数，直接放明文（这条消息不写 acp.jsonl；实施时加测试守住）；http 形态的 `headers` 同理。
  - claude / codex：见 3.4，用各自的「环境变量展开」机制。
- 「凭据只在 server」与 worker：worker 上跑的 job，明文要么在 worker 自己的环境里，要么由 server 发过去。**本期只做前者**：worker 用同样的变量名在自己的环境里配置；server 发送明文（随 Dispatch 帧下发、只在内存）列为后续选项，见「八、确认项」1。

### 3.4 按 agent 注入

新增 agent 字段 `mcp_format`（内置模板预填，自定义 agent 自己写）和 `mcp_args`（argv 模板，可用 `{{mcp_config_file}}`），与 `model_args` 同一套插入规则（插在 prompt flag 之前）。

| agent | `mcp_format` | 注入方式 | 凭据怎么传 | 工具白名单 |
|---|---|---|---|---|
| ACP（各 `*-acp`） | `acp` | 合并进 `session/new` / `session/load` 的 `mcpServers`；http 条目仅当 initialize 的 `mcpCapabilities.http` 为真时下发，否则跳过并记事件 | 协议参数里的 env / headers | 不支持（ACP 无此字段），记一条提示 |
| claude | `claude-json` | 生成 `<result_dir>/mcp.json`（`{"mcpServers":{…}}`），`mcp_args: [--mcp-config, "{{mcp_config_file}}"]` | 文件里写 `${GOFER_MCP_…}`，claude 展开环境变量（实施时实测 `--mcp-config` 文件也展开；不展开则退回「文件 0600 + 终态删除」） | `--allowedTools mcp__<name>__<tool>`（只在声明了 `tools` 时追加） |
| codex | `codex-config` | argv 追加 `-c mcp_servers.<name>.command=…` 等非敏感项 | stdio：`-c mcp_servers.<name>.env_vars=["GOFER_MCP_…"]` 让 codex 从父进程透传；http：`bearer_token_env_var` / `env_http_headers` | `-c mcp_servers.<name>.enabled_tools=[…]`（codex 支持时） |
| 其他 cli-agent（suag、omp…） | 自己声明，通常 `claude-json` | 同 claude | 同 claude | 视 agent 是否支持 |
| 没写 `mcp_format` 的 cli-agent | — | 绑定了 MCP 时提交期报错「agent x 不支持 MCP 注入」，不静默丢 | — | — |

- `args` / `url` 里的 `{{env:NAME}}`：ACP 在内存里替换成明文；claude-json 写成 `${GOFER_MCP_…}` 交给 claude 展开；codex 的 `args` 不展开环境变量，所以这类声明绑定到 codex 时提交期报错，提示改成经 `secret_env` 传给 MCP 进程（多数 MCP server 都支持从环境变量读连接串 / token）。
- 生成的 `mcp.json` 只含变量引用，属于 job 结果目录，随 job 保留，可在详情里查看（便于排错）。
- 续接（`job resume`）重新生成注入，不复用上一次的文件。
- 交互 pty job 同样注入（claude/codex 的交互模式都接受这些参数）。

### 3.5 worker 上的 job

- Dispatch 帧新增 `mcp []MCPSpec`（协议 v23）：server 把**解析后的声明**发给 worker——名字、类型、command/args/url、非敏感 env/headers、`secret_env` / `secret_headers` 的**变量名**，不含明文。worker 不再用自己的配置做并集（同 skills 的 `*Resolved`）。
- worker 按 3.3 从自己的环境取值、按 3.4 用**自己 agent 定义的** `mcp_format` 注入。
- 协议 < v23 的 worker：剥掉 MCP、job 照跑、记 `job.mcp_skipped` 事件并在 job 详情提示（与 skills 降级一致）。若 job 显式 `--mcp`，则提交期直接拒绝，避免以为带上了。
- `gofer worker doctor` 增加一项：列出 server 下发给本 worker 的项目会用到的 MCP 及其变量是否齐全。

### 3.6 可观察与入口

- job 详情 / `job show`：`mcp: pg-readonly, issues-api`（名字 + 来源级别），被跳过的列原因（不支持的格式、http 能力缺失、协议过低、变量缺失）。
- 事件：`job.mcp_injected`（名字列表）、`job.mcp_skipped`。
- CLI（放在 `agent` 组下，`gofer mcp` 已是 MCP server 命令）：`gofer agent mcp ls`、`gofer agent mcp show <name>`（显示绑定到哪些 agent / 项目、变量名与本机是否存在）、`gofer agent mcp test <name> [--runner <r>]`（在指定执行机启动它，做 initialize + tools/list，打印工具名；凭据照常从环境取）。
- HTTP：`GET /v1/mcp-servers`（变量名 + 是否在 server 本机可解析，不回传值）、`GET /v1/mcp-servers/{name}`、`POST /v1/mcp-servers/{name}/test`；写走现有配置写层（`PUT /v1/config/...`）。
- web：设置页新增「MCP」二级菜单：注册表列表、绑定关系、变量就绪状态、「测试」按钮；Agents / Projects 页显示绑定的 MCP；job 详情显示注入结果；新建 job 表单加 MCP 多选。

### 3.7 与管家 / leader 的 `gofer` 条目

保持现状：管家 / leader 的 `gofer` MCP 由 gofer 按 job 凭据自动生成，不进注册表、不受 `--no-mcp` 影响；`gofer` 是注册表保留名。两者在注入时合并，`gofer` 优先。

## 四、不做（本期）

- MCP 请求级代理 / 网关（server 托管 MCP 进程、agent 经 server 调用）：2026-10-08 评估过，不做。
- server 把凭据明文下发给 worker（见确认项 1）。
- 按工具粒度的审批 / 计费：工具调用的审批仍走现有 ACP 审批门。
- 把各 agent 自己全局配置里的 MCP（`~/.claude.json` 等）读出来展示。

## 五、风险与对策

| 风险 | 对策 |
|---|---|
| 明文泄漏到持久化位置 | 测试覆盖：request_json、rendered command、acp.jsonl、stdout/stderr 事件、`mcp.json` 均不含明文（用一个哨兵值断言） |
| claude 的 `--mcp-config` 不展开 `${VAR}` | T2 先实测；不展开就写明文到 0600 文件、终态删除，并在详情标注 |
| agent 不支持 http MCP | ACP 看 `mcpCapabilities`；cli-agent 由 `mcp_format` 决定，跳过并记事件 |
| 绑定太宽导致每个 job 都起一堆 MCP 进程 | `server.mcp` 默认空；job 详情显示来源级别，便于收窄 |
| 名字和 agent 自带的同名条目冲突 | agent 自带的优先，详情里标「agent 定义覆盖」 |

## 六、实施分期

1. **T1 模型与绑定**：`mcp_servers` 注册表、`server/agents/projects.mcp`、`JobRequest.MCP` + `MCPResolved`、`--mcp/--no-mcp`、校验与 `config validate`、`GET /v1/mcp-servers`、`gofer agent mcp ls|show`。
2. **T2 本机注入**：ACP（stdio + http 能力判定）、claude-json、codex-config；凭据进子进程环境；内置模板补 `mcp_format` / `mcp_args`；泄漏哨兵测试；claude 展开实测。
3. **T3 worker**：Dispatch `mcp`（v23）、worker 侧取值与注入、降级事件、`worker doctor`。
4. **T4 web 与测试命令**：设置页 MCP、Agents / Projects / job 详情 / 新建表单；`agent mcp test` 与 `POST /v1/mcp-servers/{name}/test`。
5. **T5 文档与发版**：skill references（server-config、worker-config、commands、job 相关）、http-api、示例配置、CHANGELOG；Linux race、Windows 全量、web 三命令；主机实测（claude / codex / 一个 ACP agent 各跑一次带 MCP 的 job，worker 上再跑一次）。

## 七、验证

- 单测：注册表解析与校验、四级并集与优先级、各 `mcp_format` 生成结果（argv / 文件 / ACP 参数）、凭据哨兵不出现在任何持久化位置、协议降级。
- 集成：本机 runner + fake ACP agent 收到的 `mcpServers`；fake cli-agent 读到的 `mcp.json` 与环境变量；worker 派发帧内容。
- 实测：主机上用一个最小的 stdio MCP（如 `@modelcontextprotocol/server-everything`）让 claude / codex / omp-acp 各调一次它的工具。

## 八、确认项（2026-10-11 用户确认：全部按建议）

1. **worker 上的凭据**：本期只从 worker 自己的环境取（每台 worker 配同名环境变量）。是否需要「server 下发明文、只在内存」？——建议先不做，等有 worker 实际要用再加（需要 TLS 链路、按 worker 白名单下发）。
2. **CLI 位置**：`gofer agent mcp …`（与 `agent skill` 并列）。可以吗？
3. **`acp.mcp_servers` 的去留**：本期保留并优先于注册表；是否在注册表落地后标 DEPRECATED、三个版本后删除？——建议标。
4. **默认绑定**：`server.mcp` 默认空，即注册表里的 MCP 不会自动出现在任何 job 里，必须显式绑定。可以吗？
