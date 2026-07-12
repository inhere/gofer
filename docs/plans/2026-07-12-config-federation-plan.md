# gofer 配置模型联邦 — 实施计划（总纲）

> bd: h-aii-xu64.10 ｜ 设计: `docs/design/2026-07-09-config-federation-design.md`(v0.4 定稿)
> 状态: **待执行**。承重代码事实已复验(2026-07-12 dossier，见 §1 现状锚点)。范围=全量，分 5 阶段，逐阶段提交(SR1206)。
> 子文档: [P1](2026-07-12-config-federation/P1-protocol-node-version-plan.md) · [P2](2026-07-12-config-federation/P2-capability-view-plan.md) · [P3](2026-07-12-config-federation/P3-submit-validation-plan.md) · [P4](2026-07-12-config-federation/P4-httpapi-meta-plan.md) · [P5](2026-07-12-config-federation/P5-web-cascade-plan.md)

## 0. 决策基线（设计 v0.4 §12 已拍板 + 实施期细化）

- **范围=全量**：G1 消双份定义 + G2 host 端错配 fail-fast + G3 UI 级联 + G5 节点信息 + AgentBrief 升级，5 阶段一轮做完。
- **AgentBrief 硬不兼容（实施细化）**：设计写"`Agents []string`→`[]AgentBrief` 替换"。实施改为**加法式**：
  - 新增 `Register.AgentCaps []AgentBrief{Key,Type,Interactive}`（typed 明细，UI 级联用）+ **保留** `Register.Agents []string`（key，校验/selector 用，避免下游大改）。
  - 新增 `Register.ProtocolVersion int`；`wshub` 注册处**版本闸**：`ProtocolVersion < 当前值` → 拒绝 + 回"请升级 worker 到 vX"。
  - **为何加法而非替换**：旧 worker 发 `agents:[...]`（无 `agent_caps`/`protocol_version`）→ Go JSON 忽略未知字段、帧**仍干净解码** → 版本闸能回**明确升级提示**（而非替换类型导致的解码崩溃）；且 5 阶段可**加法式独立构建/提交**。硬不兼容由版本闸落地，符合用户"旧客户端拒绝+提示升级"意图。
  - 冗余（新 worker 同时发 keys+caps）可接受；未来 keys 消费点清零后再删 `Agents`（本期不做）。
- **能力 API=扩展现有端点（实施校正）**：设计写"扩展 `/v1/runners`"。dossier 复审发现 **web 级联实际读 `/v1/meta`**（`metaWorker` 已带 projects/agents，`metaAgent` 缺 `interactive`）。故：
  - **`/v1/meta`**（cascade 主源）：`metaAgent` 补 `interactive`；`metaWorker` 补 typed agent 明细（复用 AgentCaps）。
  - **`/v1/runners`**（可观测面板）：`workerView` 补 typed agent 明细 + 节点信息。
  - 两者都**扩展现有、不新增端点**，符合决策。
- **#2 敏感准入**：worker-only project 的 allow_exec 等敏感字段**完全留 worker 本地**；server 只据上报 key 做可达性过滤，不据此授权 exec（exec 双闸仍在 worker 本地）。
- **#4 无候选报错**：自动选 worker 过滤后无候选 → `ErrNoCapableWorker`「无满足 project+agent 的在线 worker」（带缺失 project/agent 名）。
- **残留可选加固**：§13 全局严格/宽松开关、§14 per-project `allow_agent_args` gate —— **都不做**。

## 1. 现状锚点（2026-07-12 复验，dossier）

| 关注点 | 现状 | 位置 |
|---|---|---|
| Register 帧 | 带 `WorkerID/InstanceID/PtyCapable/OS/Labels/Projects/Agents([]string)/MaxConcurrent`；注释"display/optional-prehint only"；**无** Arch/GoferVersion/StartedAt/ProtocolVersion | `wsproto/frames.go:5-24` |
| Dispatch 帧 | **已带** `AgentArgs`（§14 已落） | `wsproto/frames.go:37-57` |
| AgentBrief 类型 | **不存在**（仅 docs） | — |
| 协议版本常量 | **不存在** | — |
| AgentConfig | 有 `Type`(exec 判别) + `Interactive bool` | `config/model.go:565-601` |
| ProjectConfig | `AllowedAgents/AllowExec/AllowedRunners/...` | `config/model.go:537-558` |
| RunnerConfig | `Type/WorkerID`(worker 绑定) | `config/model.go:613-620` |
| buildinfo | `Info{Version,GitCommit,BuildDate}` + `DisplayVersion()`；值从 main 线程注入，**worker 命令未接收** | `buildinfo/buildinfo.go:5-23` |
| workerConn.meta | 整个 `wsproto.Register` 存为 `wc.meta` | `wshub/registry.go:16-26,61-74` |
| WorkerSnapshot | `{WorkerID,InstanceID,LastHeartbeat,InFlight,PtyCapable,Labels,Projects,Agents([]string)}`；**无** CapabilitiesFor | `wshub/registry.go:252-288` |
| 注册版本闸位置 | 绑定校验通过(`hub.go:201`)后、`newWorkerConn`(`:203`)前；reject 模板见 `:194-199` | `wshub/hub.go:150-234` |
| submit project 强制 | `cfg.Projects[req.ProjectKey]` 缺→`ErrUnknownProject`，**无条件**(local/worker 皆然)=G1 seam | `job/config.go:46-49` |
| §13 agent 白名单闸 | `if len(proj.AllowedAgents)>0 { CheckAllowed }`(空=全放) | `job/config.go:64-71` |
| exec/agent-known 放宽 | `if !remote { ResolveAgent + allow_exec 闸 }`；`remote=IsRemoteRunner`(含 worker) | `job/config.go:108-124`；`submit.go:42` |
| submit 顺序 | role→`remote`分类(L42)→`validate`(L44)→env检查→`selectTargetWorker`(L66) | `job/submit.go:25-68` |
| WorkerCandidate | `{WorkerID,Labels,InFlight,PtyCapable,HeartbeatAge}`；**无** Projects/Agents | `job/selector.go:11-19` |
| candidate 数据源 | `hubWorkerSelector.Candidates/Candidate` 从 `hub.WorkerSnapshot` 建，**丢弃** snapshot 的 Projects/Agents | `core/core.go:158-196` |
| worker 端二次校验 | `cl.jobs.Submit(...本地config...)` 失败→`Result{failed}` | `worker/dispatch.go:46-69` |
| /v1/runners | `handleListRunners` 含隐式 local；`workerView{...Projects,Agents([]string)}`；映射在 `renderWorkerStatus` | `httpapi/runner_handler.go:105-198` |
| /v1/meta（web 级联真源） | `metaAgent{Key,Type}`(缺 Interactive)、`metaWorker{...Projects,Agents}` | `httpapi/meta_handler.go:35-57,136-156` |
| web 级联 | `NewJob.vue`/`NewSchedule.vue` 读 `getMeta()`；project→agent/runner 交集用 `allowed_agents/allowed_runners`；**未**用 worker 能力交集 | `web/src/views/NewJob.vue:78-168,442-469` |

## 2. 阶段总览与依赖

加法式设计使各阶段**独立可构建、逐阶段提交**（每阶段 `go build ./... && go test 相关包` 绿再提交）。

```txt
P1 协议+节点+版本闸 ──┬─→ P2 能力视图(snapshot/candidate/local合成) ──→ P3 submit校验(G1+G2 核心)
   (加字段, 不破坏)   └─────────────────────────────────────────────→ P4 httpapi/meta 能力明细
                                                                          └─→ P5 web 级联(依赖 P4)
```

- **P1→P2→P3** 是校验主链（G1/G2）。**P1→P4→P5** 是 UI 明细链（G3/G5）。P2 与 P4 都只依赖 P1，可并行。
- 建议顺序：**P1 → P2 → P3 → P4 → P5**（P3 交付核心价值后再做 UI）。

| 阶段 | bd | 目标 | 主要文件 | 交付 | 状态 |
|---|---|---|---|---|---|
| **P1** | tools-cf2 | 协议扩展(AgentCaps/ProtocolVersion/Arch/GoferVersion/StartedAt) + 版本闸 + worker 填充 + buildinfo 接线 | `wsproto/frames.go`, `wshub/hub.go`, `worker/*`, `commands/worker*.go`, `core`/`serve` 接线 | 新 worker 上报 typed 能力+节点信息；旧 worker 被拒+提示升级 | ☐ |
| **P2** | tools-2fx | 能力视图：`WorkerSnapshot` 带 AgentCaps；`WorkerCandidate` 带 Projects/Agents；local 能力合成；`CapabilitiesFor` 语义 | `wshub/registry.go`, `job/selector.go`, `core/core.go` | server 端可按 runner 维度取能力（local=全局，worker=上报） | ☐ |
| **P3** | tools-6un | **submit 校验改造（核心 G1+G2）**：worker-only project 放行；agent∈目标 runner fail-fast；selector 按 project/agent 过滤；`ErrNoCapableWorker` | `job/config.go`, `job/submit.go`, `job/selector.go`, `job/errors.go` | 消双份定义 + host 端错配 fail-fast | ☐ |
| **P4** | tools-6ic | 能力明细上 API：`/v1/meta` `metaAgent.interactive`+`metaWorker` typed；`/v1/runners` `workerView` typed+节点信息；local 合成 | `httpapi/meta_handler.go`, `httpapi/runner_handler.go`, `serve/probe.go` | 前端可拿到 runner→{projects, agents[{key,type,interactive}]} | ☐ |
| **P5** | tools-f4k | web 级联：选 runner→按该 runner(worker) 能力 + interactive 过滤 project/agent 下拉 | `web/src/views/NewJob.vue`, `NewSchedule.vue`, `api/types.ts` | G3 级联，只列目标 runner 真有的 project/agent | ☐ |

> 依赖: P2←P1, P3←P2, P4←P1, P5←P4；epic xu64.10 ← P3(核心)+P5(收尾)。P1(tools-cf2) ready，余 blocked。

## 3. 全局验收（端到端）

- [ ] P1: 帧 encode/decode 单测（新字段/AgentCaps）；版本闸单测（旧 worker protocol=0 → reject+reason）；worker 实连上报 AgentCaps+节点信息（真机/隔离 serve 冒烟）。
- [ ] P2: `CapabilitiesFor(local)`=全局 config；`CapabilitiesFor(worker)`=上报能力、离线=不可用；`WorkerCandidate` 带 Projects/Agents 单测。
- [ ] P3: **逐分支单测**（local 缺 project 仍拒 / worker-only project 放行 / agent 不在 worker 拒 / 自动选无候选→ErrNoCapableWorker / worker 离线→不可用）；worker-only project 的 host 端 `proj` 处理不破坏结果落盘。
- [ ] P4: `/v1/meta` `metaAgent` 带 interactive、`metaWorker` 带 typed agents；`/v1/runners` workerView 带节点信息；local 合成。契约单测。
- [ ] P5: 选 worker runner → project/agent 下拉按该 worker 能力收窄；interactive agent 过滤；`pnpm build` 绿 + 手工冒烟。
- [ ] 全量 `go build ./... && go vet ./... && go test ./... -p1 -count=1` 绿。
- [ ] 真机/隔离 serve+worker 端到端：worker-only project 提交成功（host 无该 project 定义）；错配 agent host 端即拒。

## 4. 全局风险 / 边界

- **R1 协议兼容**：加法式 + 版本闸，旧 worker 干净拒绝（非崩溃）。**前置**：单机同一份二进制（SR1005/G001），worker 随 server 同版本升级，窗口内旧 worker 短暂掉线属预期。
- **R2 worker-only project 的 host `proj`**：`validate` 现返回 `config.ProjectConfig`；worker-only project host 无定义 → P3 需合成最小 proj（仅 key）并**核实下游**（结果目录/exchange/notify/capture）对 worker 路径不依赖 host 端完整 proj（多数走 worker 本地 config）。**P3 必须带一条验证**：worker-only project 提交后结果能正常落盘/回读。
- **R3 校验时序**：`validate`(L44) 早于 `selectTargetWorker`(L66)。显式 worker_id → validate 内 fail-fast；自动选 → selector 过滤兜底。两路都要覆盖（P3）。
- **R4 安全不放大**：worker 只声明自己能力（worker_id↔token 绑定不变，`hub.go:184-201`）；敏感准入留 worker 本地（#2）。
- **R5 契约面**：改了 wsproto 帧 / WorkerSnapshot / httpapi 视图 / meta —— 全量 `go test -p1` 兜底；每阶段跑相关包 + 契约单测。

## 5. 进度跟进

> 每阶段完成后：勾选 §2 状态 + §3 对应项，填下表实施结果，逐阶段 commit + push(走主机)。

| 阶段 | commit | 实施结果摘要 |
|---|---|---|
| P1 | — | — |
| P2 | — | — |
| P3 | — | — |
| P4 | — | — |
| P5 | — | — |
