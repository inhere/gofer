# gofer 配置模型联邦设计（草案 v0.1）

> bd: h-aii-xu64.10 ｜ 来源：2026-07-09 使用摩擦审计（iss-0709 §E 讨论点1）
> 状态：**草案待审**。定稿后再拆实施计划。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-09 | inhere/claude | 初稿：联邦模型总体设计，待审 |
| v0.2 | 2026-07-09 | inhere/claude | 评审：**联邦方向确认**；补 worker 上报节点信息(os/arch/gofer 版本/启动时间) |
| v0.3 | 2026-07-09 | inhere/claude | 评审续：新增 §13 配置默认化(allowed_agents 空=全放非exec / local 默认)、§14 per-job agent flags(创建 job 自定义 cli-agent 参数，解决非交互授权卡死) |

## 1. 背景与问题

单机审计发现两个由「配置模型」引出的使用摩擦：

1. **项目双份定义**：提交 job 时 `project_key` 永远打在 **server 全局 config** 的 `Projects` map 上（`internal/job/config.go:46`，缺失即 `ErrUnknownProject`）。即使这个 job 是要**转发给 worker** 执行的，server 也必须先认得这个 project；而 worker 端又用**自己的** `worker.yaml.projects` 重新校验一遍（`internal/worker/dispatch.go:46`）。→ 同一 project 要在 server 和 worker **各配一份**。用户直觉："这样不是要 server 有所有 worker 的项目了。"

2. **agent/runner 错配无 host 端拦截**：创建 job 时按 project 选 agent，但选中的 agent 可能**不在**目标 runner/worker 上。当前 host 端 `validate` **不校验**「agent 在不在目标 runner」（对远程 runner 的 agent-must-be-known 检查被显式放宽，`config.go:39-44`）；自动选 worker 的 `WorkerCandidate` 结构体（`internal/job/selector.go:11-19`）**根本不带 agents/projects 字段**。→ 错配要**派到远端才失败**（可见的 failed job，但反馈慢、体验差）。

**关键事实（决定方案成本）**：worker 握手时**其实已经上报了自己的 `projects/agents/labels`**（`internal/wsproto/frames.go:9-24` 的 `Register` 帧），server 也存进了 `workerConn.meta`（`internal/wshub/registry.go:61-68`，经 `WorkerSnapshot` 暴露）——但协议注释（`frames.go:5-8`）把它定性为 **"display/optional-prehint only"**，**不参与校验与路由**。本方案的核心就是：**让这份已经在流动的能力数据变成功能性的**。

## 2. 名词

- **server**：单机一个 `gofer serve`，持全局 `~/.config/gofer/config.yaml`。
- **worker**：从节点 `gofer worker`，持独立 `worker.yaml`（自带一整套 projects/agents/runners）。
- **能力上报（capability report）**：worker 握手 `Register` 帧携带的 `projects/agents/labels/pty/max_concurrent`。
- **federated view（联邦视图）**：server 端 = 全局 config ∪ 各在线 worker 上报能力，的一个**只读合并视图**，用于校验与 UI。
- **worker-only project**：只在某 worker 上定义、server 全局 config 里没有的 project。

## 3. 目标 / 非目标

**目标**
- G1 worker-only project 不必在 server 全局 config 里重复定义即可提交（消除双份定义）。
- G2 提交/创建 job 时，能在 **host 端提前**校验「project/agent 在目标 runner 上可用」，错配 fail-fast，不再拖到远端。
- G3 UI 创建表单支持 project→runner→agent **级联过滤**（只列目标 runner 真有的 agent）。
- G4 不牺牲现有安全边界（worker 只能声明**自己**的能力；token↔worker_id 绑定不变）。
- G5 worker 上报**节点关键信息**（os / arch / gofer 版本(含 commit/build date) / 启动时间），供可观测面板展示、兼容性判断（如协议/AgentBrief 降级）、排障。
- G6 **配置默认化**：project 未显式列 `allowed_agents` 时默认可用所有已配置 agents（exec 除外，仍由 `allow_exec` 把关）；local runner 默认可用。降低重复配置（§13）。
- G7 **per-job agent 参数**：创建 job 时可自定义追加 cli-agent 参数（如放权 flag），解决非交互 job 无法交互授权（§14）。

**非目标**
- 不做 server→worker 的**配置下发/同步**（那是「中心化」方向，本方案走「联邦/自治」，见 §11 取舍）。
- 不改 workflow / plan 编排（另见 plan 编排设计）。
- 不引入服务注册中心/etcd 等外部依赖（保持单机收敛，符合 G001）。

## 4. 现状（代码级基线）

| 维度 | 现状 | 关键位置 |
|---|---|---|
| project 定义 | 全局 `Config.Projects` map；瘦 overlay 禁止新增 project | `config/model.go:22`、`config/overlay.go` forbidden 列表 |
| agent 定义 | 全局 `Config.Agents`；project 以白名单引用 | `config/model.go:23`、`ProjectConfig.AllowedAgents` |
| runner 定义 | 全局 `Config.Runners`；`type:worker` 绑单个 worker_id | `config/model.go:24,606` |
| submit 校验 | project 打 server 全局 map；agent∈白名单；runner∈白名单 | `job/config.go:46/65/118` |
| 远程 agent 检查 | **放宽**（host 可不认得远程 agent） | `job/config.go:39-44` |
| 自动选 worker | 只按 labels+pty+心跳+负载；**不含 agents/projects** | `job/selector.go:11-64` |
| worker 能力上报 | `Register` 带 projects/agents/labels，**仅展示** | `wsproto/frames.go:5-24` |
| worker 认证 | `server.workers` 静态 token↔worker_id | `config/model.go:151-156`、`core.go:91` |
| 错配后果 | 派到远端 worker 本地 `validate` 失败，回传 failed Result | `worker/dispatch.go:46-66` |

## 5. 方案总览（联邦模型）

一句话：**server 全局 config 仍是"本机/local runner"的真源；worker 上报的能力成为"该 worker runner"的真源；两者在 server 端合并成 federated view，submit 校验与 UI 选择都基于它。**

```txt
                 ┌─────────────── server (federated view，只读合并) ───────────────┐
 global config → │  local runner 能力 = 全局 Projects/Agents                         │
 worker A 上报 → │  runner(worker A) 能力 = A 的 Projects/Agents (握手上报)            │
 worker B 上报 → │  runner(worker B) 能力 = B 的 Projects/Agents                      │
                 └────────────────────────────────────────────────────────────────┘
                         ↑ submit 校验 / UI 级联选择 都查这里（按 runner 维度）
```

## 6. 详细设计

### 6.1 能力上报「变功能性」（协议）
- `Register` 帧结构**无需加字段**（已有 `Projects/Agents/Labels`）；仅去掉 `frames.go:5-8` 的 "display only" 定性，改为**权威能力声明**。
- 需补充：agent 上报**不只 key，还要带类型/interactive 能力**（UI 级联需要），即把 `Agents []string` 升级为 `Agents []AgentBrief{Key,Type,Interactive}`（协议 minor 版本兼容处理见 §9）。project 上报可保持 key 列表（准入细节仍由 worker 本地二次校验，见 6.5）。
- **节点信息（G5）**：`Register` 补 `Arch`、`GoferVersion`（取 `buildinfo.Info`，含 commit/build date）、`StartedAt`；`OS` 字段已存在（`frames.go:11`）仅需 server 端 surface。这些进 `WorkerSnapshot`，供 `/v1/runners` 面板与 Cluster 视图展示，并作**兼容性判断**依据（如旧版 worker 上报 `Agents []string` 时按版本提示升级）。

### 6.2 server 端 federated view
- 在 `wshub/registry.go` 现有 `workerConn.meta` 基础上，提供一个**按 runner 维度查询能力**的只读接口：`CapabilitiesFor(runnerKey) → {projects, agents}`。
  - runner=`local` → 返回全局 config 能力。
  - runner=`worker`(绑定 worker_id) → 返回该 worker 在线上报的能力；worker 离线则返回"不可用"。
- view 随 worker 上线/下线/重连**动态更新**（复用现有 registry 生命周期事件）。

### 6.3 submit 校验改造（核心）
`job/config.go:validate` 调整为**按目标 runner 取能力**再校验：
1. 解析目标 runner（含自动选 worker 后的结果）。
2. project 校验：`project ∈ CapabilitiesFor(runner).projects`（local runner 即查全局；worker runner 查该 worker）。→ **消除"worker-only project 必须在 server 配"**（G1）。
3. agent 校验：`agent ∈ CapabilitiesFor(runner).agents` 且 `∈ project.allowed_agents`。→ host 端 fail-fast（G2）。
4. 保留现有 exec 安全闸、runner 白名单校验。
- **自动选 worker** 增强：`WorkerCandidate` 增加 `projects/agents` 字段（数据源已在 registry），`selectWorker` 过滤掉「不具备所需 project/agent」的候选。

### 6.4 UI 级联选择
- 新增只读 API：`GET /v1/capabilities?runner=<key>`（或扩展现有 `/v1/runners` 快照带能力明细）返回 `{projects:[], agents:[{key,type,interactive}]}`。
- `NewJob.vue` / `NewSchedule.vue` 表单：选 runner → 拉该 runner 能力 → project/agent 下拉**只列可用项**（G3）。
  - 注：本表单改造属**独立后续任务**，不在本次 UI 重构 A+B 范围（A+B 只动 App/Board）。

### 6.5 agent-on-runner fail-fast + worker 端二次校验
- host 端 fail-fast 是**第一道**（6.3）；worker 端 `dispatch.go` 的本地 `validate` **保留为第二道**（防御 view 过期/竞态）。即"信任但校验"。
- worker 端失败信息保持回传 failed Result（现状），但因 host 已拦截绝大多数错配，远端失败变为罕见兜底。

## 7. 数据 / 协议改动清单

| 层 | 改动 | 兼容性 |
|---|---|---|
| `wsproto/frames.go` | `Register.Agents` 由 `[]string`→`[]AgentBrief`；去 "display only" 语义 | 协议 minor bump；旧 worker 上报 `[]string` 时降级为无 type（UI 不级联但可用） |
| `wsproto/frames.go` | `Register` 补 `Arch/GoferVersion/StartedAt`（`OS` 已有，surface 即可） | 纯新增字段，旧 worker 留空降级 |
| `wshub/registry.go` | 新增 `CapabilitiesFor(runner)` 只读查询 | 纯新增 |
| `job/selector.go` | `WorkerCandidate` 加 projects/agents；过滤逻辑 | 内部 |
| `job/config.go` | `validate` 按 runner 取能力校验；放宽全局 project 强制 | **行为变更**（见 §9 迁移） |
| `httpapi` | 新增/扩展 `/v1/capabilities` | 纯新增 |
| web `NewJob/NewSchedule` | 级联选择（后续独立任务） | 前端 |

## 8. 关键流程（submit 校验时序）

```txt
client submit(project, agent, runner?)
  └─ server.validate
       ├─ 解析 runner（显式 or 自动选 worker，候选已按 project/agent 过滤）
       ├─ caps = CapabilitiesFor(runner)
       ├─ project ∈ caps.projects ?           ── 否 → 400 ErrUnknownProjectOnRunner（fail-fast）
       ├─ agent ∈ caps.agents ∩ project.allowed ? ── 否 → 400 ErrAgentNotOnRunner（fail-fast）
       ├─ exec 安全闸 / runner 白名单（现状保留）
       └─ dispatch → worker 本地 validate（第二道，罕见兜底）
```

## 9. 兼容与迁移

- **旧 worker（上报 `Agents []string`）**：server 降级处理——能校验 key 归属，但 UI 无法按 type 级联（可接受，提示升级 worker）。
- **全局 config 里已有的 project**：继续对 local runner 有效；不删除、不强制迁移。
- **行为变更点**：submit 不再无条件要求 project 在全局 config——**仅当目标是 local runner** 时才要求全局有；worker runner 走该 worker 能力。需在实施计划里为 `job/config.go:46` 附**逐分支单测**（local 缺 project 仍拒 / worker-only project 放行 / agent 不在 worker 拒）。

## 10. 安全考量

- worker 只能声明**自己**的能力：`Register` 帧的 worker_id 由 `server.workers` 的 token 绑定校验（`core.go:91`），伪冒他人 worker_id 会被拒。故联邦不放大信任面。
- worker-only project 的**准入细节**（allowed_agents/allow_exec 等敏感字段）仍以 **worker 本地 config 为真源**，server 只据上报的 key 列表做"可达性"过滤，不据此授权 exec 等敏感能力（exec 安全闸仍在 worker 本地）。
- `/v1/capabilities` 只读、需既有 token 准入，不泄露敏感字段（只回 key/type）。

## 11. 取舍：为什么走「联邦」而非「中心化」

| | 联邦（本方案） | 中心化（备选） |
|---|---|---|
| 真源 | worker 自治，能力上报 | server 单一真源，下发 worker |
| 双份定义 | 消除 | 仍需下发同步 |
| 契合直觉 | ✅ 用户"不想 server 背所有 worker 项目" | ❌ |
| 改动 | 让已上报数据功能化（成本低） | 需建下发通道/同步一致性 |
| 一致性风险 | view 可能短暂过期 → 二次校验兜底 | 下发失败/漂移风险 |

倾向**联邦**：改动小、契合用户直觉、数据链路已存在。

## 12. 已定 / 待确认

**已定（v0.2 评审）**：走**联邦**方向；worker 补报节点信息（os/arch/gofer 版本/启动时间）。

**待确认**：
1. `Register.Agents` 升 `[]AgentBrief` 的协议版本策略：minor bump + 旧 worker 降级，是否可接受？
2. worker-only project 的准入敏感字段是否**完全**留 worker 本地（server 不感知 allow_exec）——确认安全边界。
3. `/v1/capabilities` 独立新端点，还是并入 `/v1/runners` 快照？
4. 自动选 worker 过滤后**无候选**时的报错语义（"无满足 project+agent 的在线 worker"）。
5. 级联选择表单改造是否确认为**独立后续任务**（不阻塞本次 A+B UI）。
6. 配置默认化（§13）：`allowed_agents` 空=全放（非 exec）语义变更确认？是否需要全局严格/宽松开关以兼容既有"空=全拒"期望？
7. per-job agent flags（§14）：`agent_args` 追加式 + exec 禁用 + 可选 project gate 的安全边界确认？本期做还是与 interaction 授权一起排期？

## 13. 配置默认化（降低重复配置，v0.3 新增）

来源 iss-0709 §配置优化：server/worker 已配 `agents`，project 却要再列一遍 `allowed_agents` 才能用。

**现状（已核实）**：
- `agent.CheckAllowed`（`allow.go:17-28`）遍历 `project.allowed_agents`，**空列表 = 全拒** → 必须逐 project 显式白名单，即使 agent 已全局配置。这是重复配置的根因。
- runner：`config.go:203` 已对 `allowed_runners` 为空时特殊放行 local → **local 默认可用现状已（部分）支持**，确认/补齐即可。

**改动**：
- `allowed_agents` **为空 → 默认允许所有已配置 agents**（server/worker 各自视角的 agents 全集）；非空时维持白名单语义（显式收紧）。
- **exec 例外不变**：exec agent 仍由 project 的 `allow_exec` 单独把关（`config.go:109` 安全闸），即"默认全放"**不含** exec——exec 永远需要显式 `allow_exec`。
- runner：确认 `allowed_runners` 空 → local 默认可用（现状 `config.go:203`）；补齐文档与单测。

**安全**：默认放开的是**非 exec** 的受控 cli-agent/内置 agent；真正危险的任意命令执行(exec)仍双闸（`allow_exec` + 仅本地）。

## 14. 创建 job 自定义 cli-agent 参数（per-job agent flags，v0.3 新增）

来源 iss-0709：非交互 job 里 cli-agent（claude/codex）遇审批/授权提示会**卡死**——job 无法交互授权（用户实例："job 让 claude 用工具，输出说要权限，但 job 里给不了"）。

**现状**：agent 的 `Command/Args` 是**静态**配置（`AgentConfig`）。用户已能在 server/worker 的 agent 配置里给 codex 设"完全访问"flag，但**同一 agent 不同 job 需要不同 flag**时无法区分；提交 job 无任何"额外 agent 参数"通道（`job run` 只有 `--prompt/--system-prompt/--role`）。

**方案（务实解）**：JobRequest 增可选 `agent_args []string`，**追加**到解析后的 agent argv 末尾。
- 提交渠道：CLI `--agent-arg`（可重复）/ web NewJob 高级区 / MCP `gofer_run_job` param。
- **安全 gate**：
  - 仅 `type=cli-agent` 可用；**exec agent 禁用**（否则等于注入任意命令参数、绕过 exec 安全闸）。
  - 可选按 project 策略限制（如 `allow_agent_args`），或对危险 flag 做白/黑名单。
- 典型用途：`--dangerously-skip-permissions`(claude) / codex 放权 flag，让非交互 job 预先带权，避免运行中卡授权。

**与"交互授权"的关系（另一条更重的路，后续）**：把 cli-agent 的审批提示接入 gofer 现有 **interaction 机制**（`interactions` 表 + `gofer_answer_interaction`），让 job 运行中把授权请求弹到 gofer UI 由人应答。更强但需 agent 侧结构化输出审批请求 + gofer 侧解析拦截，成本高。**本期先做 per-job flags（预授权），interaction 授权列后续。**
