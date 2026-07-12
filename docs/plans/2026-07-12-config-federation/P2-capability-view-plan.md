# P2 — 能力视图（snapshot / candidate / local 合成）

> 主纲: [../2026-07-12-config-federation-plan.md](../2026-07-12-config-federation-plan.md) ｜ bd: h-aii-xu64.10 ｜ 依赖: P1
> 目标: 让"按 runner 维度取能力"可用——`WorkerSnapshot` 带 AgentCaps；`WorkerCandidate` 带 Projects/Agents(keys)；local runner 能力由全局 config 合成。为 P3 校验、P4 API 供数据。**加法式**。

## T2.1 WorkerSnapshot 带 AgentCaps

**文件**: `internal/wshub/registry.go`

1. `WorkerSnapshot`（`:252-261`）加字段：
```go
type WorkerSnapshot struct {
	WorkerID      string
	InstanceID    string
	LastHeartbeat int64
	InFlight      int
	PtyCapable    bool
	Labels        []string
	Projects      []string
	Agents        []string             // keys (back-compat)
	AgentCaps     []wsproto.AgentBrief  // typed detail (P1)
	// 节点信息(G5, 供 P4 面板)
	OS           string
	Arch         string
	GoferVersion string
	StartedAt    int64
}
```
2. `WorkerRegistry.WorkerSnapshot`（`:268-288`）填充新字段（数据全在 `wc.meta`）：
```go
	agentCaps := append([]wsproto.AgentBrief(nil), wc.meta.AgentCaps...)
	return WorkerSnapshot{
		// …现有字段…
		Agents:       agents,
		AgentCaps:    agentCaps,
		OS:           wc.meta.OS,
		Arch:         wc.meta.Arch,
		GoferVersion: wc.meta.GoferVersion,
		StartedAt:    wc.meta.StartedAt,
	}, true
```

**验收 T2.1**: registry 单测——put 一个带 AgentCaps/Arch/... 的 `Register`，`WorkerSnapshot` 回读一致（深拷贝、无共享 slice）。

## T2.2 WorkerCandidate 带 Projects/Agents + core 映射

**文件**: `internal/job/selector.go`, `internal/core/core.go`

1. `WorkerCandidate`（`selector.go:11-19`）加：
```go
type WorkerCandidate struct {
	WorkerID     string
	Labels       []string
	Projects     []string // 该 worker 上报的 project keys (P3 过滤/校验用)
	Agents       []string // 该 worker 上报的 agent keys
	InFlight     int
	PtyCapable   bool
	HeartbeatAge time.Duration
}
```
2. `core.go` `hubWorkerSelector.Candidates`(`:158-176`) 与 `Candidate`(`:180-196`) 补映射（snapshot 已有，今天被丢弃）：
```go
		out = append(out, job.WorkerCandidate{
			WorkerID:     ws.WorkerID,
			Labels:       ws.Labels,
			Projects:     ws.Projects,
			Agents:       ws.Agents,
			InFlight:     ws.InFlight,
			PtyCapable:   ws.PtyCapable,
			HeartbeatAge: time.Duration(now-ws.LastHeartbeat) * time.Second,
		})
```

**验收 T2.2**: selector 侧单测 fixture 的 `WorkerCandidate` 带 Projects/Agents；`go build ./...` 绿。

## T2.3 CapabilitiesFor 语义（按 runner 取能力）

> P3 校验需要"给定 runner → {projects, agentKeys, online}"。**不新建 job→wshub 依赖**（job 不 import wshub）；复用两条现成注入：
> - **local runner** → 全局 `cfg.Projects` keys + `cfg.Agents` keys（job 有 cfg）。
> - **worker runner** → 经 `WorkerSelector.Candidate(workerID)`（P2.2 已带 Projects/Agents）取；worker_id 由 `cfg.Runners[runner].WorkerID`（或显式 `req.WorkerID`）解析。

**实现落点（P3 用）**：在 `job` 包内加一个纯函数 helper（`config.go` 或新 `capabilities.go`），签名约：
```go
// capabilitiesFor 返回目标 runner 的能力：local=全局 config；worker=该 worker 在线上报
// (经 selector.Candidate)。online=false 表示 worker 离线/未注册（P3 据此拒绝）。
func (s *Service) capabilitiesFor(cfg *config.Config, runner, explicitWorkerID string) (projects []string, agentKeys []string, online bool)
```
- local runner（`runner==builtinLocalRunner` 且非 worker/peer）：`projects`=keys(cfg.Projects)、`agentKeys`=keys(cfg.Agents)、online=true。
- worker runner：`wid` = explicitWorkerID 优先，否则 `cfg.Runners[runner].WorkerID`；`cand, ok := s.workers.Candidate(wid)`；ok=false → online=false；否则 projects/agentKeys 取 cand。
- peer-http runner：本期**不纳入联邦校验**（peer 用自己 config，保持现状放宽）——`capabilitiesFor` 对 peer 返回 online=false + 一个"跳过联邦校验"信号，或 P3 分支里对 peer 走原路径（见 P3 T3.1 说明）。

> 注：自动选 worker（无 explicit worker_id）时 `capabilitiesFor` 拿不到确定 worker → P3 对该路径**不在 validate 里查**，交由 selector 过滤（P3 T3.2）。

**验收 T2.3**: `capabilitiesFor` 表驱动单测——local 回全局 keys / worker 在线回其能力 / worker 离线 online=false / 显式 worker_id 覆盖 runner 默认。

## P2 验收总纲 — ✅ 全部完成（commit `e7eefb7`）

- [x] T2.1 WorkerSnapshot 带 AgentCaps/节点信息 + registry 单测绿
- [x] T2.2 WorkerCandidate 带 Projects/Agents + core 映射 + build 绿
- [x] T2.3 capabilitiesFor helper + 表驱动单测绿（local/worker 在线/离线/显式覆盖）
- [x] `go test ./internal/wshub/... ./internal/job/... ./internal/core/... -count=1` 绿（全量套件亦绿）

## 实施补记

- `capabilitiesFor` 的 local 分支判定用「**非 worker runner**」而非严格 `runner == "local"`——任何非 worker/非 peer 的 runner（如内置 pty）都在本进程用本机 config 执行，严格判等会把它们误判 `online=false`。
- local 的 `agentKeys` **复用 P1 的 resolved 集合语义**（内置 `exec` 恒在，即使 `cfg.Agents` 为空）——否则 P3 会错误拒绝 local runner 上的 exec job。
- **P3 需知的 offline 契约**：`capabilitiesFor` 在「worker runner 但 worker_id 不可解析（显式与 runner 配置默认都为空）」或 selector 为 nil 时返回 `(nil,nil,false)`。按 P3 T3.2 的 `if online { ... }` 设计，此路径**不拒绝**、落回既有行为（交 selector / worker 自解析默认）——与「自动选 → 交 selector 过滤」一致。
