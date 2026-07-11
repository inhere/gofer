# gofer MCP 项目级作用域设计（v0.2）

> bd: h-aii-xu64.15 ｜ 来源：2026-07-10 讨论（承接 config-federation / mcp 配置讨论）
> 状态：**已实现**（2026-07-11，T1-T4 全落 + 全量 test 绿 + 真实二进制 4 场景冒烟通过）。v0.2 定稿（用户批准 §8 全部决策，含 3 分歧点 #1/#4/B）。实施计划见 `docs/plans/2026-07-11-mcp-project-scope-plan.md`。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-10 | inhere/claude | 初稿：两种 MCP 角色 + 方向 B + project-key 解析 |
| v0.2 | 2026-07-11 | inhere/claude | 评审（承重代码事实已核实）：①解析改**三态显式**(none=operator / `--project X` / `--project auto`,不静默退全量)；④CWD 探测以 `.gofer.project.yaml` 的 `key:` **为主**、**砍掉** resolve-project 端点(降为后续可选)；B **create_plan 在 scope 下归 operator-only(隐藏)** 而非 scope-force；A 明确作用域=防误触**非鉴权**(显式非目标)；②③⑤按推荐定 |
| 实现 | 2026-07-11 | inhere/claude | T1-T4 落地。实现期 2 处修正：(1) overlay `key:` **无需加白名单**——`detectForbiddenOverlayKeys` 是黑名单机制，`key` 不在其中，加字段即可；(2) `run_job` 的 `project_key` 由 required 改 **omitempty**(schema optional)，否则 SDK schema 校验先于 handler 拒绝"缺 project"，scoped 缺省即填无法生效；operator 缺 project 仍在 backend 报错（无回归）。 |

## 1. 背景与问题

`gofer mcp` 现状（standalone 默认）：`config.Load` + `ApplyProjectOverlays` 加载**全量** config 建本地 Core，MCP 工具暴露**所有** project——`gofer_list_projects` 列全部、`gofer_run_job`/`gofer_create_plan` 能打任意 project。

对**项目内 agent**（Claude Code / codex 开在单个仓）这是**过度暴露 + 不必要耦合**：它只该看/操作自己那个 project，却能看到整个舰队、误打其它 project。

> 已确认（探查）：agent/runner 是**全局定义**、project 按 key 引用，故"只加载一个 project"在解析层做不到（引用的 agent/runner 定义在全局）。所以问题不在"加载"，在**暴露/隔离**。用户否决"项目瘦 config"（会让配置碎片化、与全局真源漂移，违背 G001 收敛）。

## 2. 名词

- **operator MCP**：无作用域限定，持/见所有 project 的 MCP（现状默认）。
- **项目级 MCP**：`--project X` 作用域限定到单 project 的 MCP。
- **project-key 解析**：MCP 启动时确定"当前 project"的过程（v0.2 三态，见 §5）。
- **`.gofer.project.yaml` `key:`**：项目目录瘦配置里的**可选自我标识字段**（v0.2 新增，`--project auto` 的主探测源）。
- **resolve 端点**：v0.1 曾设想的中央 serve "路径 → project-key" 反查；**v0.2 本期不做**（§5.2），保留术语仅备后续可选增强参考。

## 3. 两种 MCP 角色（都是一等场景，均保留）

| 角色 | 作用域 | 用途 | 承载 |
|---|---|---|---|
| **operator / supervisor MCP** | 无（全量） | 主控、舰队编排、跨项目 plan、人工 operator | standalone 全量 OR client 无 scope |
| **项目级 MCP** | `--project X` | 单项目内的 agent（Claude/codex 开在某仓） | **方向 B：client 瘦 MCP + scope** |

→ `--project` 是同一 MCP 上**可选的暴露收窄**，不是另建一种服务。operator 场景**行为不变**。

## 4. 方向 B：项目级走 client 瘦 MCP

项目级 MCP = `gofer mcp --server <central> --project X`：
- **不建本地 Core、不加载全量 config**（config 全在中央 `gofer serve`）；client 模式现状已不建 Core（`mcp.go:72` 走 `newClient`），只需读 server addr/token。
- **作用域在工具边界过滤**（暴露层，不改配置模型）——按工具语义分三类（v0.2 定）：
  - **收窄类**：`gofer_list_projects` → 只回 scoped project；`gofer_run_job` → project 缺省即填 scoped、显式传别的则拒。
  - **隐藏类（operator-only）**：`gofer_create_plan`（及 plan 相关 `attach_job`）在 scope 下**不注册/不暴露**——plan 天然跨项目分组，是 operator 概念，单项目 agent 不需要（B）。
  - **放行类（按资源 id）**：`gofer_get_job` / `gofer_get_plan` / `gofer_list_pending_interactions` 等**不强制 scope**，按已知 id 读单资源（低风险、利跨项目排障；仅 get_job 回 project 字段有轻微"按 id 探测"泄漏，可接受）。
- fleet 里 operator 也可是 client（无 `--project`）→ 中央 serve 有全量、operator 见全部。client 模式两种角色都能承载。

## 5. project-key 解析（核心机制）—— 三态显式模型（v0.2）

`--project` flag 三态，**绝不静默退回全量**（避免"以为限定了其实全暴露"）：

| flag | 语义 |
|---|---|
| **无 `--project`** | **operator**（全量、无 scope）。向后兼容，现状默认不变。 |
| **`--project X`** | 显式 scope 到 X。最直白、无魔法。**per-project 配置的默认推荐**（每仓 agent mcp 配置各自带 `--project X`）。 |
| **`--project auto`** | 从环境自动探测 project-key，**解析不出即报错拒启**（不退全量）。 |

### 5.1 `--project auto` 的探测顺序
1. **`./.gofer.project.yaml` 的 `key:` 字段**（v0.2 **主路**，standalone/client **通用**、纯本地读、**无服务端往返**）——项目目录自我标识。`.gofer.project.yaml` 已是 G001 的 per-dir 瘦配置，加一个可选 `key:` 最自然。
2. **standalone 专属**：`ProjectForPath(cwd)` —— `cwd` 匹配 `cfg.Projects` 中 `ExecPath(p)`（host/container 按 `server.path_view`）为**前缀或相等**者，多命中取**最长前缀**，完全相等/歧义则**确定性报错**。数据（`ProjectConfig.HostPath/ContainerPath` + `Config.ExecPath`）已具备，缺 helper 需新增。
3. **`GOFER_PROJECT` env**（CI / 容器注入兜底）。
4. 以上都无 → 报错。

### 5.2 关于 resolve-project 端点（v0.2 **砍掉/降级**）
v0.1 曾设 `GET /v1/resolve-project?path=` 给 client 瘦 MCP 做路径反查。v0.2 **不做**：client 的自动探测由「`.gofer.project.yaml` 的 `key:`」覆盖（本地、无往返、无新协议面）。仅当出现"client 在无项目文件的目录、且必须靠 server 端 path→key 反查"的真实需求，才作为**后续可选增强**补该端点——本期不引入。

## 6. 改动清单（v0.2，已随决策收窄）

| 层 | 改动 |
|---|---|
| `internal/commands/mcp.go` | 加 `--project` flag（三态：空=operator / `X` / `auto`）；把 scoped project 传入 mcpserver 构造 |
| `internal/config/*.go` | ① `.gofer.project.yaml` overlay 加**可选 `key:`** 字段（自我标识，auto 主路）；② 新增 `ProjectForPath(cwd) (key, ok)`（standalone auto 兜底；ExecPath 最长前缀、歧义报错） |
| `internal/mcpserver` | 工具边界按 §4 三类处理：收窄类(list_projects/run_job)注入/校验 scoped、隐藏类(create_plan/attach_job)在 scope 下**不注册**、放行类(get_*)不变；scoped project 从 server 构造参数传入 |
| ~~`internal/httpapi`~~ | ~~resolve-project 端点~~ **本期不做**（§5.2） |
| ~~`internal/client/client.go`~~ | ~~ResolveProject~~ **本期不做** |

→ 净新增面：mcp.go 一个 flag + config 两处(key 字段 / ProjectForPath) + mcpserver 装配。**无新 HTTP 端点、无新 client 方法、无新协议面**。

## 7. 安全 / 边界

- **★显式非目标（A）**：作用域是**"防误触"边界，不是鉴权边界**。它是**客户端侧暴露过滤**——scoped client 能做的是 operator 的子集，但一个绕过 client 的调用仍能打其它 project，**中央 serve 必须对每次提交做全量准入校验**（不因 MCP 自称 scoped 就免校验）。真正的 per-project 授权（如 per-project token）是**另立的后续项**，不在本设计范围。
- 作用域**只收窄暴露、不放权**：项目级 MCP 能做的是 operator MCP 能做的子集。
- operator MCP（无 scope）**行为完全不变**（向后兼容）。
- **边界·保留字**：`--project auto` 的 `auto` 是保留触发词——若某 project 的 key **恰为** `auto`，无法用 `--project auto` 显式 scope 到它（总走 CWD/env 探测）。real key 用 `auto` 极少见；如遇可用 `GOFER_PROJECT=auto` 或改 key 规避。flag 值**不 trim/不改大小写**，`AUTO`/` auto ` 按字面 key 处理（→空 scope，fail-safe，绝不静默退 operator）。

## 8. 已定（v0.2 决策，待你批准）

评审推荐默认已折入正文，逐条记录：

1. **解析三态**（→§5）：无 flag=operator 全量（向后兼容）；`--project X`=显式 scope；`--project auto`=环境探测、**解析不出即报错**。不静默退全量、不引入 `--all-projects`。
2. **多命中**（→§5.1.2）：最长前缀；完全相等/歧义**确定性报错**。
3. **作用域粒度**（→§4）：只**收窄** list_projects/run_job（创建/列举）；`get_*`/interactions **放行**（按资源 id，利排障）。
4. **CWD 探测源**（→§5.1、§6）：以 `.gofer.project.yaml` 的 **`key:` 为主**（本地、无往返）；**砍掉 resolve-project 端点**（降为后续可选）。
5. **standalone 也支持 `--project`**（→§4 通用）：作用域过滤对 standalone/client 通用，standalone 亦可 scope。

二级问题：**A**（作用域=防误触非鉴权）已入 §7 作显式非目标；**B**（`create_plan` 在 scope 下 operator-only 隐藏）已入 §4。

> 仍需你拍板的分歧点（我已按推荐拟定，若不同意请指出）：**#1 是否接受"无 flag=operator 全量"为默认**（另一选择是"scope-by-default、全量需显式"，更安全但破坏向后兼容）；**#4 是否接受本期不做 resolve 端点**；**B 是否同意 scope 下隐藏 create_plan**。其余（#2/#3/#5 + A）为低分歧。
