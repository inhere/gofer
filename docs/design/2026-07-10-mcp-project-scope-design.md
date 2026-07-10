# gofer MCP 项目级作用域设计（草案 v0.1）

> bd: h-aii-xu64.15 ｜ 来源：2026-07-10 讨论（承接 config-federation / mcp 配置讨论）
> 状态：**草案待审**。用户已定**方向 B**（client 瘦 MCP）。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-10 | inhere/claude | 初稿：两种 MCP 角色 + 方向 B + project-key 解析 |

## 1. 背景与问题

`gofer mcp` 现状（standalone 默认）：`config.Load` + `ApplyProjectOverlays` 加载**全量** config 建本地 Core，MCP 工具暴露**所有** project——`gofer_list_projects` 列全部、`gofer_run_job`/`gofer_create_plan` 能打任意 project。

对**项目内 agent**（Claude Code / codex 开在单个仓）这是**过度暴露 + 不必要耦合**：它只该看/操作自己那个 project，却能看到整个舰队、误打其它 project。

> 已确认（探查）：agent/runner 是**全局定义**、project 按 key 引用，故"只加载一个 project"在解析层做不到（引用的 agent/runner 定义在全局）。所以问题不在"加载"，在**暴露/隔离**。用户否决"项目瘦 config"（会让配置碎片化、与全局真源漂移，违背 G001 收敛）。

## 2. 名词

- **operator MCP**：无作用域限定，持/见所有 project 的 MCP（现状默认）。
- **项目级 MCP**：`--project X` 作用域限定到单 project 的 MCP。
- **project-key 解析**：MCP 启动时确定"当前 project"的过程。
- **resolve 端点**：中央 serve 提供的"路径 → project-key"反查（本设计新增）。

## 3. 两种 MCP 角色（都是一等场景，均保留）

| 角色 | 作用域 | 用途 | 承载 |
|---|---|---|---|
| **operator / supervisor MCP** | 无（全量） | 主控、舰队编排、跨项目 plan、人工 operator | standalone 全量 OR client 无 scope |
| **项目级 MCP** | `--project X` | 单项目内的 agent（Claude/codex 开在某仓） | **方向 B：client 瘦 MCP + scope** |

→ `--project` 是同一 MCP 上**可选的暴露收窄**，不是另建一种服务。operator 场景**行为不变**。

## 4. 方向 B：项目级走 client 瘦 MCP

项目级 MCP = `gofer mcp --server <central> --project X`：
- **不建本地 Core、不加载全量 config**（config 全在中央 `gofer serve`）；client 模式现状已不建 Core（`mcp.go:72` 走 `newClient`），只需读 server addr/token。
- **作用域在工具边界过滤**（暴露层，不改配置模型）：
  - `gofer_list_projects` → 只回 scoped project。
  - `gofer_run_job` / `gofer_create_plan` → project 默认/强制 = scoped（缺省即填、显式传别的则拒）。
  - `gofer_get_job` / `gofer_get_plan` / `gofer_list_pending_interactions` 等 → 可选限定到 scoped project 的资源（见待确认）。
- fleet 里 operator 也可是 client（无 `--project`）→ 中央 serve 有全量、operator 见全部。client 模式两种角色都能承载。

## 5. project-key 解析（核心机制）

三条路，优先级从高到低：

### 5.1 显式 `--project X`（primary）
per-project 的 mcp 配置里写死。最直白、无魔法。**方向 B 的默认推荐**（每个仓的 agent mcp 配置各自带 `--project`）。

### 5.2 CWD 自动探测（"全局共享一份 mcp 配置、在不同仓启动自动识别"）
- **standalone**（持全量 config）：新增 `ProjectForPath(cwd)` —— 拿 `cwd` 匹配 `cfg.Projects` 中 `ExecPath(p)`（host_path/container_path 按 `server.path_view`）为**前缀或相等**的 project；多命中取**最长前缀**。现**无此 helper**，但数据（`ProjectConfig.HostPath/ContainerPath` + `Config.ExecPath`）已具备。
- **client 瘦 MCP**（方向 B，本地**无** project→path 映射）：本地无从匹配 → **新增中央 serve 端点** `GET /v1/resolve-project?path=<cwd>`，serve 用它的 projects 路径反查、回 project-key。瘦 client 启动时发 CWD → 得 key → 作用域。**这是方向 B 下自动探测的正解**（配置全在中央、客户端无状态）。

### 5.3 `GOFER_PROJECT` env 兜底
标准化环境变量（如 CI / 容器注入）。

**综合推荐**：方向 B + 全局共享 mcp 配置时，用 **5.2 client 分支**（CWD → 中央 serve resolve）；per-project 配置时用 **5.1**。

## 6. 改动清单（初估）

| 层 | 改动 |
|---|---|
| `internal/commands/mcp.go` | 加 `--project` flag；client 分支支持作用域装配 |
| `internal/config/*.go` | 新增 `ProjectForPath(cwd) (key, ok)`（standalone 自动探测；匹配 ExecPath 最长前缀） |
| `internal/httpapi` | 新增 `GET /v1/resolve-project?path=` 端点（client 瘦 MCP 自动探测用） |
| `internal/client/client.go` | 加 `ResolveProject(path) (key, error)` |
| `internal/mcpserver` | 工具边界作用域过滤（list_projects / run_job / create_plan 等注入/校验 scoped project）；作用域从 server 构造参数传入 |

## 7. 安全 / 边界

- 作用域**只收窄暴露、不放权**：项目级 MCP 能做的是 operator MCP 能做的子集。
- operator MCP（无 scope）**行为完全不变**（向后兼容）。
- `resolve-project` 端点只读、需既有 `/inner` 网关/ token 准入，只回 project-key（不泄露路径/敏感字段）。
- 作用域是**客户端侧暴露过滤**——中央 serve 仍会对提交做全量准入校验（不因 MCP 声称 scoped 就免校验）。

## 8. 待确认

1. `--project` 未给、CWD 也解析不出 project 时的行为：**报错拒启**、还是**退回 operator 全量**（无 scope）？（倾向：报错，避免"以为限定了其实全量"的误解；或加显式 `--all-projects` 才全量）。
2. `resolve-project` 多命中（嵌套 project 路径）取最长前缀——确认策略。
3. 作用域粒度：`get_job`/`get_plan`/interactions 这些**按资源**的工具是否也强制限定 scoped project？还是只限"创建/列举"类（list_projects/run_job/create_plan）？（倾向：先限创建/列举，查询类按资源 id 放行以免破坏跨项目排障）。
4. `.gofer.project.yaml` overlay 是否顺带承载一个可选 `key:` 字段作为 project-dir 自我标识（另一种自动探测源）——还是只靠 path 匹配 / resolve 端点？
5. standalone 也支持 `--project` 收窄暴露吗（单机多 agent 各限一个 project 的场景，方向 A 的残留需求）？（倾向：支持，作用域过滤对 standalone/client 通用）。
