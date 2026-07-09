# P1 联邦核心：能力上报变功能性 + federated 校验 + selector 过滤（实施计划）

> 主纲：[config-optimize-plan.md](./config-optimize-plan.md) ｜ 设计：`docs/design/2026-07-09-config-federation-design.md` §5 / §6.1-6.3 / §7 / §9
> bd：xu64.10（联邦模型）
> 触点均已实测定位（2026-07-09 调研），下方 `文件:行号` 为调研时快照，实施前以实际为准。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-09 | inhere/claude | 初稿：代码级任务分解（T1..T8） |

## 范围

把 worker 握手已经上报、但被定性为"display/optional-prehint only"的能力数据（`projects/agents/labels`）**变成功能性的**：

- **T1-T3（§6.1 协议）**：`Register.Agents` 由 `[]string`→`[]AgentBrief{Key,Type,Interactive}`（带宽容解码兼容旧 worker）；补节点信息 `Arch/GoferVersion/StartedAt`（`OS` 已有）；worker 侧填充上报；worker 命令接入 `buildinfo`。
- **T4（§6.2 federated view）**：`wshub.Hub.CapabilitiesFor(workerID)` 只读能力查询 + `WorkerSnapshot` surface 节点信息。
- **T5（§6.3 submit 校验核心）**：`job/config.go:validate` 按目标 runner 取能力校验 project + agent（消除 worker-only project 必须在 server 配；agent-on-runner host 端 fail-fast）；与 P0 §13 默认化叠加。
- **T6（§6.3 自动选 worker）**：`selector.WorkerCandidate` 加 `Projects/Agents`，`selectWorker` 过滤不具备所需 project/agent 的候选；无候选报错语义。
- **T7（§6.1/G5 可观测，轻量）**：`/v1/runners` workerView surface 节点信息。

**不含**：`/v1/capabilities` 独立端点与 UI 级联表单（P2，见设计 §6.4）；配置默认化 / per-job agent flags（P0，见 `P0-quickwins-plan.md`）；server→worker 配置下发（非目标）。

---

## 关键前置事实（实施必读，决定任务耦合）

1. **`validate` 先于 `selectTargetWorker`**（`submit.go:44` vs `submit.go:66`）。即 validate 运行时，**auto-select（空 worker_id + labels）的目标 worker 尚未确定**。因此：
   - **显式 worker_id** 的 worker runner：validate 时 worker 已知 → 可 host 端 fail-fast 校验 project/agent 在该 worker 上（T5）。
   - **auto-select** 的 worker runner：validate 时 worker 未知 → validate **放宽**（不 hard-fail project/agent），把 project/agent 具备性下沉给 `selectWorker` 候选过滤（T6）+ worker 端二次 validate（`dispatch.go`，现状保留）。
   - **local runner**：validate 查全局 config（现状，不变）。
2. **worker-only project 的下游副作用**：validate 返回的 `proj config.ProjectConfig` 被 submit 下游用于建**本地 result 目录**（`submit.go:95-101` → `project.ResultBaseDir`）。worker-only project 在 server 全局 config 缺失 → `proj` 为零值 → 若 `storage.root` 未设，`ResultBaseDir` 走 `ExchangeDir(cfg, proj)` 依赖 `proj.HostPath`（空）会得到坏路径。**结论**：worker-only project（server 完全不配）要落地，server 侧需 `storage.root`（全局 store 模式，`ResultBaseDir=<root>/<projKey>`，与 proj 无关）。见 T5 处理 + 风险 + 待确认。
3. **wire 类型变更非 JSON 兼容**：`Agents []string`→`[]AgentBrief` 后，旧 worker 仍发 `"agents":["claude","codex"]`（字符串数组），新 server 用 `[]AgentBrief`（对象数组）解码会**整帧 Register 解码失败 → 旧 worker 注册直接被拒**。必须给宽容解码器（T3），否则 §9 "旧 worker 降级"落空。
4. **P0 §13 同处改动**：P0 把 `config.go:64-67` 的 agent 白名单闸改为 `if len(proj.AllowedAgents) > 0 { CheckAllowed(...) }`。P1 在同一 validate 内**新增** runner-能力校验，二者正交叠加（P0 管"server 侧 allowed_agents 白名单"，P1 管"agent 在目标 runner 上存不存在"）。T5 给出叠加后的完整块形态。

---

## T1. `wsproto` 协议：`AgentBrief` 类型 + `Register` 字段升级 + 去"display only"语义

**文件**：`internal/wsproto/frames.go`

**现状（frames.go:5-24）**：
```go
// Register (w→s, P1): the worker announces its identity + capability snapshot on
// connect. The hub validates worker_id against the token binding (review #1);
// labels/projects/agents are display/optional-prehint only — the worker
// re-validates locally on dispatch (review #8).
type Register struct {
	WorkerID string `json:"worker_id"`
	...
	InstanceID    string   `json:"instance_id,omitempty"`
	PtyCapable    bool     `json:"pty_capable,omitempty"`
	OS            string   `json:"os,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	Projects      []string `json:"projects,omitempty"`
	Agents        []string `json:"agents,omitempty"`
	MaxConcurrent int      `json:"max_concurrent,omitempty"`
}
```

**目标**：
```go
// AgentBrief is one advertised agent's authoritative capability descriptor (§6.1).
// Key is the agent id; Type/Interactive feed host-side federated validation (T5)
// and the future UI cascade (§6.4). An old worker reporting a bare string carries
// only Key (Type/Interactive zero) — see AgentBriefs.UnmarshalJSON (T3).
type AgentBrief struct {
	Key         string `json:"key"`
	Type        string `json:"type,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
}

// Register (w→s, P1): the worker announces its identity + AUTHORITATIVE capability
// snapshot on connect. The hub validates worker_id against the token binding
// (review #1). projects/agents are the worker runner's capability source of truth
// consumed by host-side federated validation (§6.2/§6.3); the worker still
// re-validates locally on dispatch as a second line of defence (review #8, §6.5).
type Register struct {
	WorkerID string `json:"worker_id"`
	...
	InstanceID string `json:"instance_id,omitempty"`
	PtyCapable bool   `json:"pty_capable,omitempty"`
	// Node info (G5): surfaced on /v1/runners + used for compat/降级 hints.
	OS           string `json:"os,omitempty"`
	Arch         string `json:"arch,omitempty"`          // runtime.GOARCH
	GoferVersion string `json:"gofer_version,omitempty"` // buildinfo DisplayVersion (含 commit)
	StartedAt    int64  `json:"started_at,omitempty"`    // worker 进程启动 unix 秒
	Labels        []string   `json:"labels,omitempty"`
	Projects      []string   `json:"projects,omitempty"`
	Agents        AgentBriefs `json:"agents,omitempty"` // 见 T3 宽容解码
	MaxConcurrent int        `json:"max_concurrent,omitempty"`
}
```

- `Projects` 保持 `[]string`（准入细节仍以 worker 本地为真源，§6.1）。
- `OS` 字段位置不动，仅注释归组到"节点信息"。

**验收**：`go build ./internal/wsproto/`；`Register` 与 `AgentBrief` 均导出。

---

## T2. `wsproto`：`AgentBriefs` 宽容解码器（兼容旧 worker `[]string`）

**文件**：`internal/wsproto/frames.go`（紧邻 `AgentBrief`）

**目标（新增）**：
```go
// AgentBriefs is the wire type for Register.Agents. Its UnmarshalJSON accepts BOTH
// the new object form (`[{"key":"claude","type":"cli-agent","interactive":true}]`)
// and the legacy string form an OLD worker still sends (`["claude","codex"]`),
// decoding the latter to briefs carrying only Key (Type/Interactive zero). This is
// the §9 "old worker downgrades" mechanism: without it the type change would break
// legacy Register decoding entirely (整帧失败). Marshal always emits the object form.
type AgentBriefs []AgentBrief

func (a *AgentBriefs) UnmarshalJSON(b []byte) error {
	// Try the object form first ([]AgentBrief). A legacy string element fails here
	// (a JSON string can't decode into the struct) and falls through.
	var briefs []AgentBrief
	if err := json.Unmarshal(b, &briefs); err == nil {
		*a = briefs
		return nil
	}
	// Fall back to the legacy string form (["claude","codex"]).
	var keys []string
	if err := json.Unmarshal(b, &keys); err != nil {
		return err
	}
	out := make([]AgentBrief, len(keys))
	for i, k := range keys {
		out[i] = AgentBrief{Key: k}
	}
	*a = out
	return nil
}
```
> 注：`json` 已在 `frames.go` 顶部 import（`Outcome.Artifacts` 用了 `json.RawMessage`），无需新增 import。

**实施注意**：
- 对象数组里每个元素 `{"key":...}`：旧标准 JSON 里字符串 `"claude"` 无法解进 `AgentBrief` 结构体 → 第一次 `json.Unmarshal` 报错 → 落到字符串分支。空数组 / null → 第一分支即成功（空 slice）。二者都安全。
- 该宽容仅服务 **server 解码 worker 帧**方向（旧 worker→新 server）；新 worker→旧 server 不在兼容目标内（server 先升级，§9）。

**验收**：单测（T-test）覆盖三种输入：对象数组 / 字符串数组 / 空。

---

## T3. worker 侧上报填充（client + command 装配 + buildinfo 接入）

### T3.1 `internal/worker/client.go`：Client 字段与 register 帧

**现状（client.go:83-87 字段 / 147-160 Config / 408-419 register）**：
```go
// Client 字段
labels     []string
projects   []string
agents     []string
maxConc    int
// Config
Labels   []string
Projects []string
Agents   []string
// register 帧
if err := cl.writeFrame(ctx, wsproto.TypeRegister, "", wsproto.Register{
	WorkerID:      cl.workerID,
	InstanceID:    cl.instanceID,
	PtyCapable:    ptyrunner.Available(),
	OS:            runtime.GOOS,
	Labels:        cl.labels,
	Projects:      cl.projects,
	Agents:        cl.agents,
	MaxConcurrent: cl.maxConc,
}); err != nil {
```

**目标**：
- Client 字段：`agents []string` → `agents []wsproto.AgentBrief`；新增 `arch string`、`goferVersion string`、`startedAt int64`。
- `Config`：`Agents []string` → `Agents []wsproto.AgentBrief`；新增 `GoferVersion string`（Arch/StartedAt 在 `New` 内就地取 `runtime.GOARCH` / `time.Now().Unix()`，无需入参）。
- `New`（client.go:176-194）：
```go
cl := &Client{
	...
	agents:       cfg.Agents,
	arch:         runtime.GOARCH,
	goferVersion: cfg.GoferVersion,
	startedAt:    time.Now().Unix(), // 进程启动时刻，跨重连稳定（在 New 定一次）
	...
}
```
- register 帧（client.go:408-419）追加：
```go
wsproto.Register{
	WorkerID:      cl.workerID,
	InstanceID:    cl.instanceID,
	PtyCapable:    ptyrunner.Available(),
	OS:            runtime.GOOS,
	Arch:          cl.arch,
	GoferVersion:  cl.goferVersion,
	StartedAt:     cl.startedAt,
	Labels:        cl.labels,
	Projects:      cl.projects,
	Agents:        cl.agents,
	MaxConcurrent: cl.maxConc,
}
```
> `runtime` 已 import（`runtime.GOOS`）；`time` 已 import。

### T3.2 `internal/commands/worker.go`：装配 briefs + 传 version

**现状（worker.go:193-205 worker.New / 320-330 agentKeys）**：
```go
cl := worker.New(worker.Config{
	...
	Projects: mapKeys(wc.Projects),
	Agents:   agentKeys(wc.Agents),
	MaxConc:  wc.MaxConcurrent,
	...
}, cr.Jobs)

func agentKeys(m map[string]config.AgentConfig) []string { ... 仅 key ... }
```

**目标**：用 `agentBriefs` 替换 `agentKeys`，携带 type/interactive；传 `GoferVersion`：
```go
cl := worker.New(worker.Config{
	...
	Projects:     mapKeys(wc.Projects),
	Agents:       agentBriefs(wc.Agents),
	GoferVersion: workerBuildInfo.DisplayVersion(),
	MaxConc:      wc.MaxConcurrent,
	...
}, cr.Jobs)

// agentBriefs 从 worker 本地 agent 配置构造上报能力（key + type + interactive）。
// 注意：agent 的实际 Type 需按 registry 语义归一（如 exec 内置、空 type），此处直接读
// 配置 Type/Interactive 即可满足级联/校验的 key 归属与 type 提示；exec 归属仍以 worker
// 本地二次校验为准（§6.5 / §10）。
func agentBriefs(m map[string]config.AgentConfig) []wsproto.AgentBrief {
	if len(m) == 0 {
		return nil
	}
	out := make([]wsproto.AgentBrief, 0, len(m))
	for k, ac := range m {
		out = append(out, wsproto.AgentBrief{Key: k, Type: ac.Type, Interactive: ac.Interactive})
	}
	return out
}
```
- `agentKeys` 若无其他调用点则删除（grep 确认；`mapKeys` 保留给 Projects）。
- `commands` 需 import `internal/wsproto`（纯类型 leaf 包，符合 G022 分层：commands 是入口层，import proto 类型无环）。

### T3.3 `buildinfo` 贯通到 worker 命令

**现状**：`NewWorkerCmd()` 无 buildinfo 入参（`app.go:39` 直接 `NewWorkerCmd()`）；`NewServeCmd(info)` 已有范式（`serve.go:30`）。

**目标**（镜像 serve 范式）：
- `commands/worker.go`：`func NewWorkerCmd(infos ...buildinfo.Info) *gcli.Command`，闭包捕获 `info`；`runWorker` 内可见 `workerBuildInfo`（把 info 存到闭包变量或经 `runWorker` 传参，仿 `runServe(c, args, info)`）。
- `commands/app.go:39`：`app.Add(NewWorkerCmd(info))`。
- import `internal/buildinfo`（worker.go 已 import config/core/worker，新增 buildinfo）。

**验收**：`go build ./...`；`gofer worker` 启动后 register 帧含 `arch/gofer_version/started_at`（冒烟：hub 侧 `/v1/runners` 或日志可见，见 T7）。

---

## T4. `wshub`：`CapabilitiesFor` 只读查询 + `WorkerSnapshot` surface 节点信息

**文件**：`internal/wshub/registry.go`（+ `hub.go` 薄封装）

### T4.1 `WorkerSnapshot` 结构与取值

**现状（registry.go:252-288）**：
```go
type WorkerSnapshot struct {
	WorkerID      string
	InstanceID    string
	LastHeartbeat int64
	InFlight      int
	PtyCapable    bool
	Labels        []string
	Projects      []string
	Agents        []string
}
...
	labels := append([]string(nil), wc.meta.Labels...)
	projects := append([]string(nil), wc.meta.Projects...)
	agents := append([]string(nil), wc.meta.Agents...)   // ← wc.meta.Agents 现为 AgentBriefs，类型不匹配
	return WorkerSnapshot{ ... Agents: agents, ... }, true
```

**目标**：
- `WorkerSnapshot` 新增节点信息字段；`Agents` 保持 `[]string`（keys）以**不破坏** `runner_handler`/`meta_handler`/`serve.probe` 现有消费（它们读 `snap.Agents []string`），从 briefs 映射 keys：
```go
type WorkerSnapshot struct {
	WorkerID      string
	InstanceID    string
	LastHeartbeat int64
	InFlight      int
	PtyCapable    bool
	OS            string // G5 节点信息
	Arch          string
	GoferVersion  string
	StartedAt     int64
	Labels        []string
	Projects      []string
	Agents        []string // 上报 briefs 的 key 列表（keep []string，保现有 surface 不变）
}
...
	agents := agentKeysFromBriefs(wc.meta.Agents) // briefs → keys
	return WorkerSnapshot{
		...
		OS:           wc.meta.OS,
		Arch:         wc.meta.Arch,
		GoferVersion: wc.meta.GoferVersion,
		StartedAt:    wc.meta.StartedAt,
		Labels:       labels,
		Projects:     projects,
		Agents:       agents,
	}, true

// agentKeysFromBriefs 抽取 briefs 的 key（防御性拷贝，nil→nil）。
func agentKeysFromBriefs(bs wsproto.AgentBriefs) []string {
	if len(bs) == 0 {
		return nil
	}
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Key
	}
	return out
}
```

### T4.2 `CapabilitiesFor` 只读能力查询

**目标（registry.go 新增 + hub.go 封装）**：
```go
// WorkerCapabilities is a worker's advertised capability snapshot for host-side
// federated validation (§6.2). Agents keeps full briefs (key/type/interactive) so
// the future /v1/capabilities cascade (§6.4) reuses it; P1 validation only reads
// keys. Copies out under the registry lock; ok=false when the worker is offline.
type WorkerCapabilities struct {
	Projects []string
	Agents   []wsproto.AgentBrief
}

// CapabilitiesFor returns workerID's advertised projects/agents (§6.2). It is the
// registry accessor for the federated view: runner→worker resolution and the
// local-runner (global config) case live at the job/core boundary (job never
// imports wshub). ok=false when the worker has no live connection (offline →
// "unavailable", per §6.2).
func (r *WorkerRegistry) CapabilitiesFor(workerID string) (WorkerCapabilities, bool) {
	r.mu.RLock()
	wc, ok := r.conns[workerID]
	r.mu.RUnlock()
	if !ok {
		return WorkerCapabilities{}, false
	}
	projects := append([]string(nil), wc.meta.Projects...)
	agents := append([]wsproto.AgentBrief(nil), wc.meta.Agents...)
	return WorkerCapabilities{Projects: projects, Agents: agents}, true
}
```
- `hub.go` 加薄封装 `func (h *Hub) CapabilitiesFor(workerID string) (WorkerCapabilities, bool) { return h.reg.CapabilitiesFor(workerID) }`（镜像 `WorkerSnapshot` 现有封装 hub.go:401）。
- **runnerKey→能力的联邦解析**（local=全局 config / worker=该 worker / 离线=不可用）在 **job 层**用扩展后的 `WorkerCandidate.Projects/Agents`（T6）+ job 自身 `cfg` 完成，避免 job import wshub（G022）。CapabilitiesFor 是被 `core.hubWorkerSelector` 消费来填 `WorkerCandidate`（见 T6），非死代码。

**验收**：`go test ./internal/wshub/...`（含 `hub_p3_test.go:TestWorkerSnapshotExposed` 需按新字段/映射更新，见测试清单）。

---

## T5. `job/config.go`：`validate` federated 校验（核心）

**文件**：`internal/job/config.go`

**现状关键分支**：
- `config.go:46-49` project 不存在即 `ErrUnknownProject`（无论目标 runner）。
- `config.go:64-67` agent 白名单闸（P0 会改成 `len(proj.AllowedAgents)>0` 才查）。
- `config.go:99-112` exec 安全闸（`!remote` 才做）。
- `config.go:114-120` runner 白名单 `checkRunnerAllowed(proj, req.Runner)`。

**目标语义**（与前置事实 1/2/4 对齐）：

1. **project 解析放宽 worker runner**：
```go
proj, ok := cfg.Projects[req.ProjectKey]
if !ok {
	// worker-only project (§6.1/G1): server 全局 config 没有该 project 是合法的——
	// 只要目标是 worker runner（能力以该 worker 上报为真源）。合成一个空 proj，跳过所有
	// 依赖全局 config 的 project 级闸（allowed_agents/allow_exec/allowed_runners），
	// 交给 worker 端二次 validate（§6.5）。local / peer runner 仍要求全局有该 project。
	if !isWorkerRunner(cfg, req.Runner) {
		return config.ProjectConfig{}, fmt.Errorf("%w: unknown project %q", ErrUnknownProject, req.ProjectKey)
	}
	// workerOnly=true：后续 project 级校验按"worker 自治"跳过，能力校验改查 worker 上报。
}
```
   用一个局部 `workerOnly := !ok`（`ok` 来自上面的 map 查找）标记。

2. **agent 校验叠加 P0 §13 + P1 runner 能力**（`config.go:64-67` 处的最终形态）：
```go
// (P0 §13) server 侧 allowed_agents 白名单：空=放开；仅 workerOnly=false 时该 project
// 在全局有配置才有意义。workerOnly 时 proj 为空，天然跳过。
if !workerOnly && len(proj.AllowedAgents) > 0 {
	if err := agent.CheckAllowed(cfg, req.ProjectKey, gateAgent); err != nil {
		return config.ProjectConfig{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
	}
}

// (P1 §6.3) runner 能力 fail-fast：agent 必须在目标 runner 上存在。
//  - local runner：agent ∈ 全局 config.Agents（ResolveAgent 命中）。
//  - 显式 worker_id 的 worker runner：agent ∈ 该 worker 上报 agents（selector.Candidate）。
//  - auto-select（空 worker_id + labels）：worker 未定（validate 早于 selectTargetWorker），
//    放行——由 selectWorker 候选过滤（T6）+ worker 二次 validate 兜底。
if err := s.checkAgentOnRunner(cfg, req, gateAgent); err != nil {
	return config.ProjectConfig{}, err // ErrAgentNotOnRunner / fail-fast
}
```

3. **`checkAgentOnRunner` 新增**（job/config.go）：
```go
// checkAgentOnRunner enforces the federated "agent exists on the target runner"
// check (§6.3 step 3). It is the P1 fail-fast that previously only surfaced after
// dispatch to the remote worker (job/config.go:39-44 放宽注释所述). Auto-select
// (empty worker_id) defers to selectWorker's capability filter (T6).
func (s *Service) checkAgentOnRunner(cfg *config.Config, req JobRequest, gateAgent string) error {
	// local / peer 交由既有 exec-gate + agent 解析路径（peer 放宽，见 remote 分支）。
	if !isWorkerRunner(cfg, req.Runner) {
		return nil
	}
	if req.WorkerID == "" {
		return nil // auto-select：目标未定，下沉 selectWorker 过滤 + worker 二次校验
	}
	if s.workers == nil {
		return nil // 无 selector（如无 hub 装配）→ 交 worker 二次校验
	}
	cand, ok := s.workers.Candidate(req.WorkerID)
	if !ok {
		return nil // worker 离线/未连：worker_id 合法性已由 config.go:127-131 校验；此处不重复报错
	}
	if !agentKeyIn(cand.Agents, gateAgent) {
		return fmt.Errorf("%w: agent %q not available on worker %q", ErrInvalidRequest, gateAgent, req.WorkerID)
	}
	return nil
}
```
   > `ErrAgentNotOnRunner` 可复用 `ErrInvalidRequest`（400 语义一致）或新增专用哨兵（见待确认 4）。`agentKeyIn` 是简单 `slices.Contains`（`slices` 已 import）。

4. **exec 安全闸（`config.go:99-112`）**：`!remote` 分支保留。worker runner 是 remote → 不在此分支（exec 由 worker 本地闸，§10）。**无需改**。

5. **runner 白名单（`config.go:114-120`）**：`checkRunnerAllowed(proj, req.Runner)`。workerOnly 时 `proj` 为空 → 现状 `checkRunnerAllowed` 对空 `AllowedRunners` 只放行 `local`，会**误拒** worker runner。目标：
```go
if !workerOnly {
	if err := checkRunnerAllowed(proj, req.Runner); err != nil {
		return config.ProjectConfig{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
	}
}
// workerOnly：project 的 allowed_runners 也是 worker 本地真源，跳过（worker 二次校验）。
```
   （`req.Runner == ""` 的必填校验保留在 workerOnly 之外，仍然先行。）

6. **worker-only project 的 result-dir 副作用**（前置事实 2）：validate 返回空 `proj` → `submit.go:96 ResultBaseDir` 需 `storage.root` 才能给出稳定路径。**本任务不改 submit**，但：
   - 在计划/文档明确：**worker-only project 要求 server 配 `storage.root`**（全局 store）。
   - 可选防御（推荐）：`ResultBaseDir` 在 `storage.root==""` 且 `proj.HostPath==""` 时返回明确错误（现状会静默拼坏路径）。列为 T5 的可选子项 / 或 P1.5 独立小改。→ 见风险 R3 + 待确认 2。

**验收**：`go test ./internal/job/...` 绿；新增逐分支单测（见测试清单）。

---

## T6. `selector`：`WorkerCandidate` 加能力字段 + `selectWorker` 过滤 + core 装配

### T6.1 `internal/job/selector.go`

**现状（selector.go:11-19 / 40-64）**：
```go
type WorkerCandidate struct {
	WorkerID   string
	Labels     []string
	InFlight   int
	PtyCapable bool
	HeartbeatAge time.Duration
}
...
func selectWorker(cands []WorkerCandidate, required []string, interactive bool) string {
	...
	for _, w := range cands {
		if w.HeartbeatAge > workerStaleAfter { continue }
		if !hasAllLabels(w.Labels, required) { continue }
		if interactive && !w.PtyCapable { continue }
		ok = append(ok, w)
	}
	...
}
```

**目标**：候选带 project/agent 能力；`selectWorker` 额外过滤需要的 project + agent：
```go
type WorkerCandidate struct {
	WorkerID   string
	Labels     []string
	InFlight   int
	PtyCapable bool
	// Projects/Agents 是该 worker 上报的能力（§6.3 auto-select 过滤依据）。空视为"未上报"，
	// 保守起见按"不具备"处理（见 selectWorker：required 非空时空能力候选被排除）。
	Projects []string
	Agents   []string
	HeartbeatAge time.Duration
}

// selectWorker 增加 project/agent 入参（Submit 传 req.ProjectKey / gateAgent）。
func selectWorker(cands []WorkerCandidate, required []string, interactive bool, project, agentKey string) string {
	ok := make([]WorkerCandidate, 0, len(cands))
	for _, w := range cands {
		if w.HeartbeatAge > workerStaleAfter { continue }
		if !hasAllLabels(w.Labels, required) { continue }
		if interactive && !w.PtyCapable { continue }
		if project != "" && !contains(w.Projects, project) { continue } // §6.3 过滤
		if agentKey != "" && !contains(w.Agents, agentKey) { continue } // §6.3 过滤
		ok = append(ok, w)
	}
	if len(ok) == 0 { return "" } // 调用方 → ErrNoEligibleWorker（含 project/agent 不满足语义）
	// 排序不变（InFlight↑ → HeartbeatAge↑）
	...
}
```
- `contains` 复用/新增小工具（或直接 `slices.Contains`）。
- `selectTargetWorker`（config.go:169-178）调用点更新：`selectWorker(cands, req.WorkerLabels, req.Interactive, req.ProjectKey, gateAgent)`。注意 `gateAgent` 在 `selectTargetWorker` 里需按 resume 语义解析（与 validate 的 gateAgent 同规则）——或直接传 `req.Agent`（auto-select 通常非 resume；resume 走 exec 载体，见待确认 4）。
- **无候选报错语义**：`ErrNoEligibleWorker` 复用现状（config.go:175），错误信息扩展为包含 project/agent，例如 `no eligible worker for labels %v (project %q agent %q)`。

### T6.2 `internal/core/core.go`：`hubWorkerSelector` 填能力

**现状（core.go:159-196）**：`Candidates()` / `Candidate()` 从 `hub.WorkerSnapshot(id)` 构造 `WorkerCandidate`，**未填 Projects/Agents**。

**目标**：两处构造都补 `Projects/Agents`（来源 `ws.Projects`/`ws.Agents`，`WorkerSnapshot` 已带 keys，T4）：
```go
out = append(out, job.WorkerCandidate{
	WorkerID:     ws.WorkerID,
	Labels:       ws.Labels,
	InFlight:     ws.InFlight,
	PtyCapable:   ws.PtyCapable,
	Projects:     ws.Projects, // §6.3 能力过滤数据源
	Agents:       ws.Agents,
	HeartbeatAge: time.Duration(now-ws.LastHeartbeat) * time.Second,
})
```
   `Candidate(workerID)`（core.go:189-195）同样补两行。
   > 用 `WorkerSnapshot.Agents`（keys）即可满足 selector 与 `checkAgentOnRunner` 的 key 归属；`CapabilitiesFor` 的 briefs 留给 P2 `/v1/capabilities`。二者数据同源（wc.meta），一致。

**验收**：`go test ./internal/job/... ./internal/core/...`；`selector_test.go` 更新签名 + 新增 project/agent 过滤用例。

---

## T7. `/v1/runners` surface 节点信息（G5 可观测，轻量）

**文件**：`internal/httpapi/runner_handler.go` + `internal/serve/probe.go`

**目标**（把 T4 的 OS/Arch/GoferVersion/StartedAt 透到 `/v1/runners` 的 `workerView`）：
- `httpapi.WorkerStatus`（runner_handler.go:40-47）加 `OS/Arch/GoferVersion/StartedAt`。
- `workerView`（runner_handler.go:91-98）加对应 JSON 字段（`os,omitempty` 等）；`renderWorkerStatus`（189-197）填充。
- `serve/probe.go:26-33` 的 `hubWorkerRegistry.WorkerStatus` 从 `snap` 映射新字段（`StartedAt` 若统一 millis 需 `*1000`，或保 unix 秒并在字段名注明；与 `LastHeartbeat` 口径对齐，见待确认 3）。

**验收**：`go test ./internal/httpapi/... ./internal/serve/...`；`meta_test.go:TestMetaWorkerConnectedMatchesRunners` 若断言字段需同步。

> 说明：`/v1/capabilities` 独立端点与 UI 级联属 P2（设计 §6.4），本任务只做**节点信息展示**，不引入新端点。

---

## 测试清单

### 协议 / 兼容（T1-T3）
- `internal/wsproto`（新增或就近测试文件）：`AgentBriefs.UnmarshalJSON` 三形态——对象数组（保 type/interactive）/ 旧字符串数组（降级为仅 Key）/ 空数组、null。
- `internal/worker`（`e2e_test.go` / `reconnect_test.go` 附近）：register 帧含 `Arch/GoferVersion/StartedAt`；`StartedAt` 跨重连**不变**（New 定一次）。
- `internal/wshub`（`hub_test.go` / `hub_p3_test.go`）：旧 worker（发 `agents:["a","b"]`）注册**成功**且 `WorkerSnapshot.Agents==["a","b"]`（降级不阻断，§9 红线）；新 worker（对象数组）`CapabilitiesFor` 返回带 type 的 briefs。
- `hub_p3_test.go:TestWorkerSnapshotExposed`：按新字段更新断言（含 OS/Arch/GoferVersion；Agents 仍为 keys）。

### federated 校验（T5）— 逐分支（设计 §9 明确要求）
- local runner + 全局缺 project → **仍拒** `ErrUnknownProject`（回归红线）。
- worker runner + 显式 worker_id + worker-only project（全局无） → **放行**（G1）。
- worker runner + 显式 worker_id + agent 不在该 worker 上报 agents → **拒** fail-fast（G2，`ErrInvalidRequest`）。
- worker runner + auto-select（labels，空 worker_id） + worker-only project → validate **放行**（下沉 selector）。
- 全局有 project（双定义）+ worker runner + P0 空 `allowed_agents` → 放行（P0 叠加不冲突）；非空且请求 agent 不在白名单 → 拒（P0 语义保持）。
- worker runner + workerOnly + 任意 runner（非 local）→ 不被 `checkRunnerAllowed` 误拒。
- exec 闸不回归：local + exec agent + `allow_exec=false` → 仍拒（T5 未触碰 exec 分支）。

### selector 过滤（T6）
- `selector_test.go`：候选 A 有 project P + agent X、候选 B 缺 X → 请求(P,X) 只选 A；全都缺 → `""`（→ `ErrNoEligibleWorker`，错误含 project/agent）。
- project/agent 参数为空（向后兼容旧调用）→ 不因能力过滤而少选（等价原行为）。
- 能力字段为空（worker 未上报）+ required project/agent 非空 → 该候选被排除（保守语义，与 R2 对齐）。

### 可观测（T7）
- `runner_handler_test.go`：worker 连接后 `/v1/runners` 的 `worker` 块含 `os/arch/gofer_version`。

**总验收**：
- [ ] `go test ./...` 全绿（容器 Linux）。
- [ ] `go vet ./...` / `go build ./...` 无环（G022：job 不 import wshub；commands import wsproto 合法）。
- [ ] 冒烟（主机 gofer server + 容器 worker，或本地双进程）：
  - worker-only project（server 完全不配、仅 worker.yaml 有）+ `storage.root` 已设 → 经 worker runner 提 job **成功**（消除双份定义，G1）。
  - agent 不在目标 worker → host 端**立即**拒（不再拖到远端 failed，G2）。
  - 旧 worker（模拟发 `agents:[]string`）注册**成功**、`/v1/runners` 可见（§9 降级）。

---

## 风险

- **R1 wire 类型变更破坏旧 worker**：`[]string`→`[]AgentBrief` 若无 T3 宽容解码 → 旧 worker 整帧 Register 解码失败、注册被拒。**缓解**：T3 `AgentBriefs.UnmarshalJSON` 双形态 + 单测强背书；发布说明标注协议 minor bump。
- **R2 auto-select 能力过滤误伤**：worker 能力上报为空（未配 projects/agents 或旧 worker 只报 keys）时，若 required project/agent 非空则该候选被排除，可能"本来能跑却选不到"。**缓解**：保守语义是**故意**的（宁可 fail-fast 也不错派）；worker 正常上报能力即命中；文档提示"worker 需上报 projects/agents 才能被 auto-select 命中所需能力"。回退口子：可加全局宽松开关（默认严格），列待确认。
- **R3 worker-only project 的 result-dir**：server 无该 project 的 `host_path` 时，`ResultBaseDir` 在 `storage.root` 未设下会拼坏路径（前置事实 2）。**缓解**：文档硬性要求 worker-only project 场景启用 `storage.root`；可选给 `ResultBaseDir` 明确报错替代静默坏路径（T5 可选子项 / P1.5）。
- **R4 校验下沉削弱 host 拦截**：auto-select 路径 host 端不 fail-fast，靠 selector + worker 二次校验。**缓解**：设计即"信任但校验"（§6.5）；错配在 selector 阶段即 `ErrNoEligibleWorker`（仍在 host、提交即失败），比派到远端 failed 体验好；worker 二次校验兜底竞态/view 过期。
- **R5 gateAgent 在 selector 的传参**：`selectWorker` 用 `req.Agent` 还是 resume 语义的 gateAgent，需与 validate 一致，否则 resume 场景过滤错。**缓解**：`selectTargetWorker` 复用与 validate 相同的 gateAgent 解析（`ResumeSourceAgent` 优先），或明确 resume 不走 auto-select 能力过滤（待确认 4）。

---

## 待确认

1. **Agents wire 兼容策略**：`[]AgentBrief` + `AgentBriefs.UnmarshalJSON` 宽容旧 `[]string`（协议 minor bump、旧 worker 降级为仅 Key）——确认可接受（对应设计 §12 待确认 1）。
2. **worker-only project + result-dir**：确认 worker-only project 落地**要求 server 配 `storage.root`**（全局 store）；是否顺手给 `ResultBaseDir` 在无 root+无 host_path 时明确报错（本期 or P1.5）。
3. **节点信息时间口径**：`StartedAt` 用 unix **秒**（与 register/心跳一致）还是在 `/v1/runners` 统一转 **millis**（与 `LastHeartbeat` SR102 口径）——确认 T7 字段口径。
4. **能力校验错误哨兵**：新增 `ErrAgentNotOnRunner`/`ErrUnknownProjectOnRunner`（设计 §8 用到）还是复用 `ErrInvalidRequest`/`ErrUnknownProject`（400 语义足够）；`selectTargetWorker` 里 gateAgent 是否需 resume 语义解析（R5）。
5. **auto-select 严格/宽松开关**：R2 的保守语义是否需要全局开关兜底"能力未上报也放行"的过渡期需求（默认严格）。
6. **CapabilitiesFor 归属**：确认 `wshub.Hub.CapabilitiesFor` 作为 registry 只读 accessor（worker 维度）、runner→能力的联邦解析放 job/core 边界（不引入 job→wshub 依赖）——与设计 §6.2 "CapabilitiesFor(runnerKey)" 的落地差异（runnerKey 解析下沉 job 层）是否 OK。
