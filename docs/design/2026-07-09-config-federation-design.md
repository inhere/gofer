# gofer 配置模型联邦设计（v0.4 定稿）

> bd: h-aii-xu64.10 ｜ 来源：2026-07-09 使用摩擦审计（iss-0709 §E 讨论点1）
> 状态：**v0.4 定稿**（2026-07-11 用户拍板全部决策）。§13/§14 **已实现**（xu64.13 + `48b1634`），本设计剩**联邦核心**（G1/G2/G3/G5）待拆实施计划。承重代码事实已复验（2026-07-11，A–F+I 全成立，仅行号漂移）。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-09 | inhere/claude | 初稿：联邦模型总体设计，待审 |
| v0.2 | 2026-07-09 | inhere/claude | 评审：**联邦方向确认**；补 worker 上报节点信息(os/arch/gofer 版本/启动时间) |
| v0.3 | 2026-07-09 | inhere/claude | 评审续：新增 §13 配置默认化(allowed_agents 空=全放非exec / local 默认)、§14 per-job agent flags(创建 job 自定义 cli-agent 参数，解决非交互授权卡死) |
| v0.4 | 2026-07-11 | inhere/claude | **复审+拍板定稿**：①范围=**全量**(MVP 消双份/fail-fast + G3 UI 级联 + G5 节点信息 + AgentBrief 升级)；②AgentBrief 升级采**硬不兼容**(旧 worker 拒绝+提示升级，非降级)；③§13/§14 **已实现**(校正落点/剔除待办)、残留可选加固(§13 全局开关 / §14 per-project gate)**都不做**；④能力 API=**扩展 `/v1/runners` 快照**(不新增端点)；⑤待确认 #2(敏感准入留 worker 本地)/#4(无候选明确报错)按推荐定。承重事实复验记入 §4。 |

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
- G5 worker 上报**节点关键信息**（os / arch / gofer 版本(含 commit/build date) / 启动时间），供可观测面板展示、兼容性判断、排障。**本轮做**（范围=全量）。
- ~~G6 **配置默认化**~~ **✅ 已实现**（xu64.13 / `48b1634`）：空 `allowed_agents` = 放开所有已配 agents，exec 仍由 `allow_exec` 把关；local runner 空 `allowed_runners` 默认可用。落点在 `job/config.go` 调用处闸（**非** `allow.go`），无全局严格/宽松开关（v0.4 决策：不加）。详见 §13。
- ~~G7 **per-job agent 参数**~~ **✅ 已实现**（`48b1634` + 后续）：CLI `--agent-arg` / MCP `agent_args` / web / worker dispatch 全链路追加 cli-agent 参数，exec 禁用 + secret 脱敏 + 追加式。无 per-project `allow_agent_args` 闸（v0.4 决策：不加）。详见 §14。

**非目标**
- 不做 server→worker 的**配置下发/同步**（那是「中心化」方向，本方案走「联邦/自治」，见 §11 取舍）。
- 不改 workflow / plan 编排（另见 plan 编排设计）。
- 不引入服务注册中心/etcd 等外部依赖（保持单机收敛，符合 G001）。

## 4. 现状（代码级基线）

> **2026-07-11 复审复验**：下表 A–F+I 各事实**语义仍成立**，仅行号漂移几行（xu64.13/xu64.15 改动所致）。实施计划以现行代码为准重取行号。补一条新事实：`config.go` 的"远程 agent 检查放宽"其注释写 "a peer-http runner"，但代码 `remote = IsRemoteRunner` **含 worker runner** —— 即 worker runner 的 agent 检查同样已放宽（与本设计假设一致，仅注释偏窄）。

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
- 去掉 `frames.go:5-8` 的 "display only" 定性，`Register` 帧的能力字段改为**权威能力声明**（参与校验+路由）。
- **AgentBrief 升级（v0.4 决策：硬不兼容）**：`Agents []string` 升级为 `Agents []AgentBrief{Key,Type,Interactive}`（UI 级联需 type/interactive）。**不做降级兼容**——旧格式 worker 连入直接**拒绝注册 + 回明确"请升级 worker 到 vX"错误**（版本闸见下）。project 上报保持 key 列表（准入细节仍由 worker 本地二次校验，见 6.5）。
- **节点信息（G5）**：`Register` 补 `Arch`、`GoferVersion`（取 `buildinfo.Info`，含 commit/build date）、`StartedAt`；`OS` 已存在（`frames.go:11`）server 端 surface 即可。这些进 `WorkerSnapshot`，供 `/v1/runners` 面板/Cluster 视图展示。
- **版本闸（硬不兼容的落地）**：server 在处理 `Register` 时先据 `GoferVersion` / 协议版本判定；worker 版本低于引入 AgentBrief 的最小版本（或能力字段解码为旧 `[]string` 形态）→ 注册被拒 + actionable 错误。**前置约束**：本项目单机同一份二进制部署（SR1005/G001），worker 与 server 天然同版本，故"全部 worker 随 server 一起升级"风险极低——升级窗口内旧 worker 短暂掉线属预期。

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

### 6.4 UI 级联选择（v0.4：本轮做）
- **API（v0.4 决策：扩展 `/v1/runners`，不新增端点）**：扩展现有 `/v1/runners` 快照，每个 runner 带能力明细 `{projects:[], agents:[{key,type,interactive}]}`；`local` runner 的能力由全局 config 合成并入同一视图。前端拉一次 runner 列表即含全部能力，选 runner 后**纯客户端过滤**做级联，无每选一次的额外请求。
- `NewJob.vue` / `NewSchedule.vue` 表单：选 runner → 用已加载的该 runner 能力 → project/agent 下拉**只列可用项**（G3）。
  - 若后续担心 `/v1/runners` 职责耦合（健康态 + 能力明细混在一起），可再拆独立 `GET /v1/capabilities?runner=` —— 成本很低、可后置。

### 6.5 agent-on-runner fail-fast + worker 端二次校验
- host 端 fail-fast 是**第一道**（6.3）；worker 端 `dispatch.go` 的本地 `validate` **保留为第二道**（防御 view 过期/竞态）。即"信任但校验"。
- worker 端失败信息保持回传 failed Result（现状），但因 host 已拦截绝大多数错配，远端失败变为罕见兜底。

## 7. 数据 / 协议改动清单

| 层 | 改动 | 兼容性 |
|---|---|---|
| `wsproto/frames.go` | `Register.Agents` 由 `[]string`→`[]AgentBrief`；去 "display only" 语义 | **协议 breaking**；旧 worker 注册被拒 + 提示升级（v0.4 硬不兼容，非降级） |
| `wsproto/frames.go` | `Register` 补 `Arch/GoferVersion/StartedAt`（`OS` 已有，surface 即可） | 纯新增字段 |
| `wshub/hub/registry.go` | 注册处**版本闸**：旧格式/低版本 worker 拒绝 + actionable 错误 | 新增校验 |
| `wshub/registry.go` | 新增 `CapabilitiesFor(runner)` 只读查询（local=全局 config，worker=在线上报） | 纯新增 |
| `job/selector.go` | `WorkerCandidate` 加 projects/agents；`selectWorker` 过滤不具备 project/agent 的候选 | 内部 |
| `job/config.go` | `validate` 按目标 runner 取能力校验；放宽"全局 project 强制"（仅 local runner 才强制全局有） | **行为变更**（见 §9 迁移） |
| `httpapi` `/v1/runners` | 快照带能力明细 `{projects,agents[{key,type,interactive}]}` + 合成 local 能力 | 扩展（字段新增） |
| web `NewJob/NewSchedule` | 选 runner → 客户端过滤级联 | 前端 |

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

- **旧 worker（上报 `Agents []string`）**：v0.4 **硬不兼容**——注册被拒 + 回"升级 worker 到 vX"错误，**不降级**。前置约束见 §6.1（单机同一份二进制，worker 随 server 同版本，升级窗口短暂掉线属预期）。
- **全局 config 里已有的 project**：继续对 local runner 有效；不删除、不强制迁移。
- **行为变更点**：submit 不再无条件要求 project 在全局 config——**仅当目标是 local runner** 时才要求全局有；worker runner 走该 worker 能力。需在实施计划里为 `job/config.go`(现 ~L46) 附**逐分支单测**（local 缺 project 仍拒 / worker-only project 放行 / agent 不在 worker 拒 / worker 离线时 CapabilitiesFor 返回不可用）。

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

## 12. 已定（v0.4 全部拍板）

原 7 项待确认已全部有结论：

| # | 议题 | 结论（v0.4） |
|---|---|---|
| 范围 | 联邦做到哪一步 | **全量**：MVP(G1 消双份 + G2 fail-fast) + G3 UI 级联 + G5 节点信息 + AgentBrief 升级，一轮做完 |
| 1 | `Register.Agents`→`[]AgentBrief` 版本策略 | **硬不兼容**：旧 worker 拒绝注册 + 提示升级（非降级）；单机同二进制部署风险低（§6.1） |
| 2 | worker-only project 敏感准入归属 | **完全留 worker 本地**，server 只据上报 key 做可达性过滤，不感知/不据此授权 allow_exec（§10） |
| 3 | 能力 API 形态 | **扩展 `/v1/runners` 快照**带能力明细，不新增端点；合成 local 能力（§6.4） |
| 4 | 自动选 worker 无候选报错 | 返回明确错误 `无满足 project+agent 的在线 worker`（带缺失的 project/agent 名） |
| 5 | UI 级联表单改造排期 | **本轮做**（范围=全量，不再后置） |
| 6 | §13 配置默认化 + 全局严格/宽松开关 | §13 **已实现**；全局开关 **不做**（YAGNI，空=全放已够用） |
| 7 | §14 per-job agent flags + per-project gate | §14 **已实现**（全链路）；per-project `allow_agent_args` gate **不做** |

> 下一步：据本定稿拆**实施计划**（`docs/plans/`），按 SR1105 细化到关键代码片段 + 逐分支验收；改动面大（协议 breaking + submit 校验 + selector + registry + httpapi + web），走 design→plan→确认→实施。

## 13. 配置默认化（降低重复配置，v0.3 新增）— ✅ 已实现（xu64.13 / `48b1634`）

> **落地校正（2026-07-11 复审）**：实际实现**不是**改 `allow.go`（`allow.go` 仍文档化"空=全拒"），而是在 `job/config.go` 的**调用处加闸**：`if len(proj.AllowedAgents) > 0 { CheckAllowed(...) }` —— 空列表跳过白名单闸即"全放已配 agents"。exec 仍由 `job/config.go` 的 `allow_exec` 安全闸独立把关。local runner 空 `allowed_runners` 默认放行本就存在。**未加**全局严格/宽松开关（v0.4 决策：不加）。以下为原设计意图，保留备查：

来源 iss-0709 §配置优化：server/worker 已配 `agents`，project 却要再列一遍 `allowed_agents` 才能用。

**现状（已核实）**：
- `agent.CheckAllowed`（`allow.go:17-28`）遍历 `project.allowed_agents`，**空列表 = 全拒** → 必须逐 project 显式白名单，即使 agent 已全局配置。这是重复配置的根因。
- runner：`config.go:203` 已对 `allowed_runners` 为空时特殊放行 local → **local 默认可用现状已（部分）支持**，确认/补齐即可。

**改动**：
- `allowed_agents` **为空 → 默认允许所有已配置 agents**（server/worker 各自视角的 agents 全集）；非空时维持白名单语义（显式收紧）。
- **exec 例外不变**：exec agent 仍由 project 的 `allow_exec` 单独把关（`config.go:109` 安全闸），即"默认全放"**不含** exec——exec 永远需要显式 `allow_exec`。
- runner：确认 `allowed_runners` 空 → local 默认可用（现状 `config.go:203`）；补齐文档与单测。

**安全**：默认放开的是**非 exec** 的受控 cli-agent/内置 agent；真正危险的任意命令执行(exec)仍双闸（`allow_exec` + 仅本地）。

## 14. 创建 job 自定义 cli-agent 参数（per-job agent flags，v0.3 新增）— ✅ 已实现（`48b1634` + 后续）

> **落地状态（2026-07-11 复审）**：`JobRequest.AgentArgs` **全链路已实现**——CLI `--agent-arg`(可重复) / MCP `gofer_run_job.agent_args` / web NewJob / worker dispatch 转发；追加到 cli-agent argv 末尾；**exec 禁用**（`job/config.go` 拒绝）+ secret 脱敏（`rebuild.go`）。**未加** per-project `allow_agent_args` gate（v0.4 决策：不加，exec 已禁用+注入面已受控）。interaction 授权（运行中弹权由人应答）仍为**后续更重的路**（见文末）。以下为原设计，保留备查：

来源 iss-0709：非交互 job 里 cli-agent（claude/codex）遇审批/授权提示会**卡死**——job 无法交互授权（用户实例："job 让 claude 用工具，输出说要权限，但 job 里给不了"）。

**现状**：agent 的 `Command/Args` 是**静态**配置（`AgentConfig`）。用户已能在 server/worker 的 agent 配置里给 codex 设"完全访问"flag，但**同一 agent 不同 job 需要不同 flag**时无法区分；提交 job 无任何"额外 agent 参数"通道（`job run` 只有 `--prompt/--system-prompt/--role`）。

**方案（务实解）**：JobRequest 增可选 `agent_args []string`，**追加**到解析后的 agent argv 末尾。
- 提交渠道：CLI `--agent-arg`（可重复）/ web NewJob 高级区 / MCP `gofer_run_job` param。
- **安全 gate**：
  - 仅 `type=cli-agent` 可用；**exec agent 禁用**（否则等于注入任意命令参数、绕过 exec 安全闸）。
  - 可选按 project 策略限制（如 `allow_agent_args`），或对危险 flag 做白/黑名单。
- 典型用途：`--dangerously-skip-permissions`(claude) / codex 放权 flag，让非交互 job 预先带权，避免运行中卡授权。

**与"交互授权"的关系（另一条更重的路，后续）**：把 cli-agent 的审批提示接入 gofer 现有 **interaction 机制**（`interactions` 表 + `gofer_answer_interaction`），让 job 运行中把授权请求弹到 gofer UI 由人应答。更强但需 agent 侧结构化输出审批请求 + gofer 侧解析拦截，成本高。**本期先做 per-job flags（预授权），interaction 授权列后续。**
