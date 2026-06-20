# P2 — 按 worker labels 自动调度（实施计划）

> 主纲：[`2026-06-20-submit-dispatch-plan.md`](./2026-06-20-submit-dispatch-plan.md) · 设计：[`../../design/2026-06-20-submit-dispatch-design.md`](../../design/2026-06-20-submit-dispatch-design.md) §6.3。
> 唯一较重的一阶段：含 worker runner「动态路由」解绑重构。决策 D3/D4。依赖 P1（`JobRequest` 已加字段，本阶段再加 `WorkerLabels`）。

---

## 概览

runner=worker 现状：① `JobRequest` 无 labels；② 校验强制 `worker_id` 必填（`service.go:564`）；③ worker runner 构造期绑死单 worker（`runner/worker/runner.go` `r.workerID`，Dispatch 用它）。

目标：未给 `worker_id` 但给了 `worker_labels` 时，Submit 从 hub 注册表快照按 labels+负载选一台已连接 worker，注入 `req.WorkerID`，worker runner 按该 id 动态派发；`worker_id`/`labels` 都没给则回落 `rc.WorkerID` 兜底（D4）。

---

## P2-a 字段 + 动态路由解绑

### 1) `JobRequest.WorkerLabels` — `internal/job/model.go`
```go
// WorkerLabels auto-selects a worker by labels when runner=worker and WorkerID
// is empty (D3): a candidate worker must advertise ALL these labels. Ignored
// when WorkerID is set (explicit routing wins).
WorkerLabels []string `json:"worker_labels,omitempty" yaml:"worker_labels,omitempty"`
```

### 2) `Forward.WorkerID` — `internal/runner/runner.go`（`Forward` 结构加字段）
```go
type Forward struct {
	ProjectKey string
	Agent      string
	PeerRunner string
	Prompt     string
	Cmd        []string
	Cwd        string
	TimeoutSec int
	WorkerID   string // resolved target worker for runner=worker (dynamic routing, P2)
}
```

### 3) worker runner 读 `f.WorkerID`（回落 `r.workerID`）— `internal/runner/worker/runner.go`
`Run` 内取目标 worker，替换原先直接用 `r.workerID` 的三处（RegisterSink / Dispatch / DeregisterSink / Answer / Cancel）：
```go
func (r *Runner) Run(ctx context.Context, req runner.Request) runner.Result {
	f := req.Forward
	if f == nil { return runner.Result{ExitCode: -1, Err: errors.New("worker runner requires forward request")} }
	workerID := f.WorkerID
	if workerID == "" { workerID = r.workerID } // D4 兜底：配置期绑定的默认 worker
	if workerID == "" { return runner.Result{ExitCode: -1, Err: errors.New("worker runner: no target worker_id")} }
	// ...后续把所有 r.workerID 改用局部 workerID（sink.bridge.answer 闭包、RegisterSink、
	//    Dispatch、DeregisterSink、Cancel）
}
```
> 注意 `sink.bridge.answer = func(iid, ans string){ _ = r.hub.Answer(workerID, req.JobID, iid, ans) }` 与 `defer r.hub.DeregisterSink(workerID, req.JobID)` 都改用局部 `workerID`。

### 4) Submit 注入 `Forward.WorkerID` — `internal/job/service.go`
构造远程 Forward 处（explorer 报 `service.go:213-224`），把解析后的 `req.WorkerID` 写进 `Forward.WorkerID`：
```go
Forward: &runner.Forward{
	ProjectKey: req.ProjectKey, Agent: req.Agent,
	Prompt: req.Prompt, Cmd: req.Cmd, Cwd: req.Cwd, TimeoutSec: timeout,
	WorkerID: req.WorkerID, // P2：动态路由目标（选机已注入或显式传入）
},
```

### P2-a 验收
- 单测 `internal/runner/worker`：`Forward.WorkerID` 非空时 Dispatch 用它；为空时回落 `r.workerID`（fake dispatcher 断言收到的 workerID）。
- 回归：现网"一 runner 绑一 worker"配置（不传 worker_id、不传 labels）行为不变（走兜底）。

---

## P2-b Submit 内 labels 选机

### 1) 候选数据 + 选择器接口 — `internal/job`（新 `selector.go`，保持 job 不 import wshub）
```go
// WorkerCandidate 是选机所需的中性快照（由 commands 层从 hub 注册表适配填充）。
type WorkerCandidate struct {
	WorkerID     string
	Labels       []string
	InFlight     int
	HeartbeatAge time.Duration // 距最近一帧的时长；越小越新鲜
}
// WorkerSelector 暴露当前已连接 worker 的候选快照。
type WorkerSelector interface {
	Candidates() []WorkerCandidate
}

const workerStaleAfter = 30 * time.Second // 心跳过期阈值（对齐 C6 口径）

// selectWorker 从候选里选一台满足 labels 全包含 + 新鲜的 worker：
// 排序 in_flight↑ → heartbeat_age↑（D3 负载优先）。返回空串表示无合格候选。
func selectWorker(cands []WorkerCandidate, required []string) string {
	type c = WorkerCandidate
	var ok []c
	for _, w := range cands {
		if w.HeartbeatAge > workerStaleAfter { continue }
		if !hasAllLabels(w.Labels, required) { continue }
		ok = append(ok, w)
	}
	if len(ok) == 0 { return "" }
	sort.Slice(ok, func(i, j int) bool {
		if ok[i].InFlight != ok[j].InFlight { return ok[i].InFlight < ok[j].InFlight }
		return ok[i].HeartbeatAge < ok[j].HeartbeatAge
	})
	return ok[0].WorkerID
}
func hasAllLabels(have, required []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, l := range have { set[l] = struct{}{} }
	for _, r := range required {
		if _, k := set[r]; !k { return false }
	}
	return true
}
```

### 2) 适配器（hub → WorkerSelector）— `internal/commands/assemble.go`
`buildCore` 内构造 selector 并注入 `job.NewService(...)`（加一个参数）：
```go
sel := &hubWorkerSelector{hub: hub, allowed: cfg.Server.Workers} // allowed=配置在册的 worker 集
jobs := job.NewService(cfg, projects, agents, runners, store, sel)
```
适配器（`assemble.go` 或新 `worker_selector.go`）：遍历 `cfg.Server.Workers` 在册 id，取 `hub.WorkerSnapshot(id)`（仅 connected 命中），算 `HeartbeatAge = now - LastHeartbeat`：
```go
type hubWorkerSelector struct {
	hub     *wshub.Hub
	allowed map[string]config.WorkerConfig
}
func (h *hubWorkerSelector) Candidates() []job.WorkerCandidate {
	out := make([]job.WorkerCandidate, 0, len(h.allowed))
	now := time.Now().Unix()
	for id := range h.allowed {
		ws, ok := h.hub.WorkerSnapshot(id) // 已连接才 ok
		if !ok { continue }
		out = append(out, job.WorkerCandidate{
			WorkerID:     ws.WorkerID,
			Labels:       ws.Labels,
			InFlight:     ws.InFlight,
			HeartbeatAge: time.Duration(now-ws.LastHeartbeat) * time.Second,
		})
	}
	return out
}
```
> `*wshub.Hub` 需有透传 `WorkerSnapshot(id)` 的方法（registry 已有；hub 若未暴露则补一行转发）。`config.WorkerConfig` 按现有 `cfg.Server.Workers` 类型。

### 3) 校验/选机接入 — `internal/job/service.go`（改 `validate` 的 worker 分支 `service.go:564`）
```go
if isWorkerRunner(cfg, req.Runner) {
	if req.WorkerID != "" {
		if _, ok := cfg.Server.Workers[req.WorkerID]; !ok {
			return ..., fmt.Errorf("%w: unknown worker_id %q", ErrInvalidRequest, req.WorkerID)
		}
	} else if len(req.WorkerLabels) > 0 {
		picked := selectWorker(s.workers.Candidates(), req.WorkerLabels)
		if picked == "" {
			return ..., fmt.Errorf("%w: no eligible worker for labels %v", ErrNoEligibleWorker, req.WorkerLabels)
		}
		req.WorkerID = picked // 注入：后续 Forward + JobResult.worker_id 都用它
	} else {
		// 既不显式也无 labels：保留 rc.WorkerID 兜底（D4）——此处放行，
		// 由 worker runner 回落默认；若该 runner 也无默认绑定则 runner 返错。
	}
}
```
> `validate` 当前是值接收/返回 ProjectConfig；`req` 的修改需回传到 Submit（让 `validate` 返回选定的 worker_id，或把选机移到 Submit 内紧接 validate 之后，避免改 validate 签名——**推荐后者**：validate 只做校验，选机单独一步写 `req.WorkerID`）。

### 4) 新错误码 + HTTP 映射
- `internal/job`：`var ErrNoEligibleWorker = errors.New("no eligible worker")`。
- `internal/httpapi/job_handler.go` `submitStatus`：
```go
if errors.Is(err, job.ErrNoEligibleWorker) { return http.StatusServiceUnavailable } // 503
```

### P2-b 验收
- 单测 `internal/job`：`selectWorker` —
  - labels 全包含才入选；缺一个 label 被排除；
  - 过期心跳（>30s）被排除；
  - 多候选按 `in_flight↑→age↑` 选首个；
  - 无候选返空串。
- 单测 Submit：runner=worker + 仅 labels → 注入 worker_id 并落 `JobResult.worker_id`；无候选 → `ErrNoEligibleWorker`（HTTP 503）；worker_id 与 labels 同给 → 用 worker_id（labels 忽略）。
- 真机：起 2 个带不同 labels 的 worker，`--worker-labels gpu` 自动落到 gpu 那台；详情页 worker_id 正确；停掉该 worker 再提交 → 503。
- 回归：显式 worker_id 路由、无 labels 的兜底路径均不变。

### CLI（可选，本阶段附带）
`job run` 加 `--worker-labels`（逗号分隔 → `[]string`）；`runJobRun` 填 `req.WorkerLabels`。

### 提交点
P2-a、P2-b 分别绿灯各 `git commit`；更新主纲进度 + 实施结果一行。**P2-a 重构需全量 `go test ./...` 确认 worker/peer 既有 E2E 不回归**。
