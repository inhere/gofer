# Gofer 配置简化设计（全局单 server + 项目瘦配置）

> 一句话：一台机只起**一个** `serve`、项目映射收敛到**全局单文件**；项目目录仅放 `.gofer.project.yaml` **瘦配置**（只声明本项目偏好、无 server）；准入真源在 serve 端、分层合并、SIGHUP 生效。
> 关联：roadmap [`../2026-06-20-enhancements-roadmap.md`](../2026-06-20-enhancements-roadmap.md) E29；配置模型现状见 `internal/config/`。本文新决策从 **D1** 起（与既有架构决策编号独立）。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-06-22 | inhere | 初稿：三文件分工 + D1–D9 + Phase1（零代码）/ Phase2（瘦配置合并 + cwd 推断），待审核 |
| v0.2 | 2026-06-23 | inhere | §12 三点按推荐定稿（max_concurrent_jobs 准入 overlay / 做 config show / worker 暂不做）；进入 plan 阶段 |

## 2. 背景与痛点

现状配置查找链（`config.Resolve` `loader.go:78`，前者**全量覆盖**后者、**不合并**）：

```
1. --config flag  →  2. env GOFER_CONFIG  →  3. ./.gofer.local.yaml  →  4. ./.gofer.yaml  →  5. ~/.config/gofer/config.yaml
```

用户在**每个项目目录放一份带 `server` 段的 `.gofer.yaml`**，导致：

- **痛点①**：在 A 目录跑只命中 A 的那份，别处运行命令找不到 server → "其他地方运行无效"。
- **痛点②**：每份都带 `server` 段 → 概念上"每项目一个 server"，多项目空间要起多个。

期望：**一台机一个 server**、项目映射**全局一个文件**、项目下配置**瘦身为只放项目自己的设置**、不配走默认。

## 3. 核心取向

**痛点主因是用法（每项目放全套 config），不是架构缺陷。** gofer 其实已具备"全局单 config + 单 serve + 项目 CRUD + SIGHUP 热重载 + CLI 连 serve"。因此本设计分两层：

- **Phase 1（零代码，立即可用）**：约定**集中式用法**——全局单 config + `GOFER_CONFIG` 锁定 + `project add` 写全局 + 一个 `serve` + 任意目录 `job run` 连 serve。直接消除痛点①②。
- **Phase 2（改造）**：新增**项目瘦配置** `.gofer.project.yaml` + 分层合并 + cwd 自动推断 project，满足"项目设置随代码走、跟仓库"。

两条关键不变量：① **准入真源在 serve**（安全，见 D2）；② **全套 config 与项目瘦配置用不同文件名**，语义不冲突（D1）。

## 4. 现状能力基线（复用不重造）

| 能力 | 挂点 | Phase2 如何复用 |
|---|---|---|
| 配置查找链 + `GOFER_CONFIG` 优先于 cwd | `config.Resolve` `loader.go:78-95` | **不动**；Phase1 直接利用 env 优先级 |
| 全局配置目录 | `ConfigDir`/`UserConfigPath` `loader.go:100/113`（`GOFER_CFG_DIR` 可覆盖） | 不动 |
| 字段 fallback（subdir 缺省走 storage 默认） | `ResolvedExchangeSubdir`/`ResolvedResultSubdir` `model.go:445/457` | 合并第三层直接复用 |
| 项目注册表（atomic 快照 + CRUD + 写回全局） | `project.Registry` `registry.go:20`；`Add` `:72`；`save` 默认 `UserConfigPath` `:98` | 不动；overlay 在合并后喂给它 |
| SIGHUP 热重载（原子换 cfg） | `Core.Reload` `assemble.go:146`；`startReloadLoop` `serve.go:351` | overlay 合并挂在 `buildCore`/`Reload` 内 |
| 组装真源（serve 与 mcp 共用） | `buildCore` `assemble.go:52` | overlay 合并点（serve+mcp 同时生效） |
| CLI 连 serve（HTTP，地址/token 取自 config） | `newClient` `job.go:194` | cwd 推断在此之上加；**不读 overlay**（D2） |
| 准入校验 | `project.Registry.Validate` `registry.go:118`；`Config.ProjectAllowedAgents` `model.go:435` | 校验在合并后的 cfg 上跑 |

> **关键已具备**：`project add` 不传 `-c` 时 `save()` 默认写 `UserConfigPath()`＝`~/.config/gofer/config.yaml`（`registry.go:98-106`）——Phase1 的"项目映射收敛全局"**已经是默认行为**，无需改码。

## 5. 三种配置文件分工（D1 核心）

| 文件 | 角色 | 含 `server`/`storage`？ | 放哪 | 谁读 |
|---|---|---|---|---|
| `config.yaml` / `.gofer.yaml` | **全套 config**（server 真源 + 项目注册表 + agents/runners/storage） | ✅ | 全局 `~/.config/gofer/`；或某项目想**独立起一套 server** 时放其目录（沿用现有查找链，行为不变） | `serve`/`mcp`/CLI（取 addr+token） |
| **`.gofer.project.yaml`**（新） | **项目瘦配置**：只声明本项目偏好 | ❌ 禁止（出现则忽略 + warn） | 项目目录，随 git | **仅 serve/mcp 端**合并；CLI 不读 |

职责分离：**全局注册表登记 `key → host_path/container_path`（注册锚）**；**项目目录瘦配置补"这个项目怎么跑"的偏好**。

## 6. 范围

**做**：① Phase1 集中式用法约定（含 `init`/README 引导）；② Phase2 `.gofer.project.yaml` 瘦配置结构 + 合并 + 校验；③ serve/mcp 端在 `buildCore`/`Reload` 合并 overlay；④ CLI cwd→project 自动推断。

**不做**：
- **不做** 独立 `projects.yaml` 注册表分离（与全套 config 拆成两文件）——收益低、改动大，留后续。
- **不做** fsnotify 自动监听 overlay 变更——SIGHUP 足够（D6）。
- **不动** worker（`worker.yaml` 有独立 `projects`，`model.go:394`）——worker 端 overlay 支持留后续。
- **不改** 全套 config 查找链行为——向后兼容（D9）。

## 7. 决策点

- **D1 文件名 = `.gofer.project.yaml`**（已定）。直白、与全套 `config.yaml`/`.gofer.yaml` 区分，解决同名 `.gofer.yaml` 的"全套 vs 瘦配置"角色二义。
- **D2 准入真源在 serve（最关键，安全）**：`allowed_agents`/`allowed_runners`/`allow_exec` 这类**准入字段绝不进 overlay、保留全局**。理由：overlay 在项目目录、可被项目方修改，准入放 overlay＝项目方自给自己放权。CLI 也**不读** overlay（`newClient` 不调合并），裁决权只在 serve。
- **D3 注册锚 vs 偏好分离**：`host_path`/`container_path` 只在全局 `projects[key]`（serve 得先靠它定位"去哪读 overlay"）；overlay 只补偏好字段。
- **D4 容器路径**：gofer 在容器内，读 overlay 用 `container_path`（容器内可达）非空优先，否则 `host_path`；文件不存在 → 跳过（项目纯走全局定义）；解析失败 → **warn 跳过该项目 overlay**（不阻塞整个 serve），reload 时 fail-safe 保留旧 cfg（沿用 `Core.Reload` 语义 `assemble.go:146`）。
- **D5 overlay 允许字段（白名单）**：`exchange_subdir` / `result_subdir` / `default_agent`（仍须 ∈ 全局 `allowed_agents`，否则校验失败，借此防绕过准入）/ `capture_diff` / `notify_enabled` / `max_concurrent_jobs`。**禁止**：`server`/`storage`/`host_path`/`container_path` + 全部准入字段（D2）。
- **D6 合并优先级 + 生效时机**：`overlay > 全局 projects[key] > storage 默认`（第三层即 `Resolved*` 已实现）；合并在 `buildCore`（启动）与 `Core.Reload`（SIGHUP）内做，改了 overlay 发 `SIGHUP` 即生效。
- **D7 cwd 自动推断 project**：CLI `job run` 未给 `-p` 时，用 cwd 绝对路径对 `cfg.Projects` 的 `host_path`/`container_path` 做**最长前缀匹配**；唯一命中 → 用该 key 且把 `--cwd` 自动设为相对项目根的子路径；0/多命中 → 报错提示显式 `-p`。仅用注册锚（全局有），不依赖 overlay。
- **D8 指针字段语义**：`capture_diff`/`notify_enabled` 本就是 `*bool`（`model.go:348/353`），overlay 非 nil 覆盖；`max_concurrent_jobs` 为 `int`，overlay `>0` 覆盖（0＝未设）；`default_agent`/subdir 为 string，非空覆盖；slice 不在 overlay（准入，D2）。`allow_exec`（`bool`，有"未设 vs false"二义）**不进 overlay**，规避二义。
- **D9 向后兼容**：无 `.gofer.project.yaml` 的项目行为**完全不变**；现有"每项目放全套 `.gofer.yaml`"用法仍可用（查找链不动），只是**推荐**迁移到"全局 + overlay"。

## 8. Phase 1：集中式（零代码，立即可用）

1. 全局 `~/.config/gofer/config.yaml` 一份：`server`+`storage`+`agents`+`runners`+`projects` 全放这里；每个项目项**只写 `host_path`**（+ 容器内 `container_path`），其余留空自动走默认（`ResolvedExchangeSubdir` 等）。
2. `export GOFER_CONFIG=~/.config/gofer/config.yaml`（写进 shell profile）—— env 优先级高于 cwd（`Resolve` `loader.go:82`），任意目录强制走全局。
3. 删/清空各项目目录里的旧 `.gofer.yaml`。
4. 起**一个** `gofer serve`。
5. 加项目：`gofer project add siv --host-path /d/.../SIV`（默认写全局，`registry.go:98`）。
6. 任意目录：`gofer job run -p siv -a claude "..."`（`newClient` 从全局 config 取 addr+token 连 serve，`job.go:194`）。

> 即满足"一个 server + 项目映射全局一个文件 + 任意目录可用"。**不依赖 Phase 2。**

## 9. Phase 2：项目瘦配置 + cwd 推断（改造）

### 9.1 加载/合并流程

```
serve/mcp 启动:
  cfg = config.Load(path)                 # loader.go:41  (CLI 同此, 但到此为止)
  ── serve/mcp 专属 ──
  config.ApplyProjectOverlays(cfg)        # 新增: 遍历 cfg.Projects
       for key, p := range cfg.Projects:
           dir = p.ContainerPath || p.HostPath          # D4
           ov  = readOverlay(dir + "/.gofer.project.yaml")  # 不存在→跳过; 解析失败→warn 跳过
           cfg.Projects[key] = MergeProjectConfig(p, ov)    # 新增: 白名单字段覆盖 (D5/D8)
  core = buildCore(cfg)                    # assemble.go:52  (拿到已合并的 cfg)

SIGHUP:
  Core.Reload(path):                       # assemble.go:146
     newCfg = config.Load(path); ApplyProjectOverlays(newCfg)   # 同启动路径
     swap Projects/Agents/Jobs (atomic)
```

要点：`ApplyProjectOverlays` **只在 serve/mcp 加载路径调用**，不进通用 `config.Load`，从而 CLI `newClient`（`job.go:194`）天然不读 overlay（D2 安全）。

### 9.2 关键落点（plan 阶段再细化到代码）

| 改动 | 落点 | 说明 |
|---|---|---|
| 瘦配置结构 `ProjectOverlay` | 新增 `internal/config/overlay.go` | 只含 D5 白名单字段；`server`/`storage`/`host_path`/`container_path`/准入字段出现 → warn |
| 合并 `MergeProjectConfig(base, ov)` | `internal/config/overlay.go` | 按 D8 语义；slice/准入字段不参与 |
| `ApplyProjectOverlays(cfg)` | `internal/config/overlay.go` | 遍历 projects 读盘合并；返回 warn 列表供日志 |
| serve 合并接入 | `buildCore` `assemble.go:52` 前 / `runServe` `serve.go:52` 后 | 启动期合并 |
| reload 合并接入 | `Core.Reload` `assemble.go:157` 后 | SIGHUP 期合并（fail-safe 不变） |
| mcp 合并接入 | mcp 命令的 `buildCore` 调用处 | standalone/HTTP-client 两形态都生效 |
| cwd→project 推断 | `runJobRun`/`newClient` 前 `job.go:244` | D7；`-p` 为空时反查；提供 `resolveProjectByCwd(cfg, cwd)` 工具 |
| 校验扩展 | `project.Registry.Validate` `registry.go:118` | overlay 合并后校验 `default_agent ∈ allowed_agents`（防绕过准入） |
| 诊断命令（可选） | 新增 `gofer config show --project <key>` | 打印**合并后**有效配置，便于排查 overlay 是否生效 |

## 10. 配置示例

全局 `~/.config/gofer/config.yaml`（server 真源 + 注册锚）：

```yaml
server:
  addr: 0.0.0.0:8765
  token_env: GOFER_TOKEN
storage:
  db_path: ~/.config/gofer/gofer.db
agents:
  claude: { type: cli-agent, command: claude, args: ["-p", "{{prompt}}"] }
  codex:  { type: cli-agent, command: codex,  args: ["exec", "{{prompt}}"] }
runners:
  local: { type: local }
projects:
  siv: { host_path: /d/work/.../SIV, container_path: /work/SIV,
         allowed_agents: [claude, codex] }     # 准入留全局 (D2)
  bic: { host_path: /d/work/.../BIC, container_path: /work/BIC,
         allowed_agents: [claude] }
```

项目 `/work/SIV/.gofer.project.yaml`（瘦配置，随 git，仅偏好）：

```yaml
# 无 server / storage / host_path / allowed_agents (D2/D5)
default_agent: claude          # 须 ∈ 全局 allowed_agents, 否则校验失败
result_subdir: gofer-out
capture_diff: true
notify_enabled: false
```

合并结果（serve 端 `cfg.Projects["siv"]`）：`host_path`/`container_path`/`allowed_agents` 取全局，`default_agent`/`result_subdir`/`capture_diff`/`notify_enabled` 取 overlay，`exchange_subdir` 走 storage 默认。

## 11. 安全（SR1402 闭环）

- **准入不下放**（D2）：overlay 在项目目录、项目方可改；若把 `allowed_agents`/`allow_exec` 放 overlay，等于项目方自我放权。故准入字段**只在全局**，overlay 只能调偏好。
- **`default_agent` 防绕过**：overlay 可设 `default_agent`，但合并后校验其 ∈ 全局 `allowed_agents`，否则失败——不能借默认 agent 绕开准入。
- **信任模型**：项目目录 overlay 视为**operator 可信**（类比 `.editorconfig`/git hooks，operator 控制部署机器）；偏好字段下放可接受，准入字段坚持全局。
- **secret 不入 overlay**：overlay 仅偏好，无 token/secret 字段（沿用 SR403）。

## 12. 已确认事项（v0.2 定稿，2026-06-23）

1. **`max_concurrent_jobs` 允许 overlay 覆盖**（保留在 D5 白名单内）——operator 可信模型下接受项目自调；operator 若要收回，把它移出白名单即可（注释留痕）。
2. **做 `gofer config show --project <key>`**（打印合并后有效配置），随 Phase2 落地——排查 overlay 是否生效的必备诊断。
3. **worker 端暂不支持 `.gofer.project.yaml`**（worker 有独立 `worker.yaml`，`model.go:394`）；远端 worker 的项目偏好留后续，本轮 out-of-scope。

## 13. 结论

- **Phase 1 现在就能用、零代码**：全局单 config + `GOFER_CONFIG` + 一个 serve + `project add` 写全局，直接解决痛点①②。
- **Phase 2 是体验增强**：`.gofer.project.yaml` 让项目偏好随代码走；核心约束是**准入真源在 serve、CLI 不读 overlay**（D2），合并挂在 `buildCore`/`Reload`（D6），cwd 推断免 `-p`（D7）。
- 改动面（Phase2）：新增 `internal/config/overlay.go` + serve/mcp 合并接入 + CLI cwd 推断 + 校验/诊断，约 5–8 文件，**全 additive、向后兼容**（D9）。审核通过后出 `plans/2026-06-22-config-simplification/` 实施计划。
