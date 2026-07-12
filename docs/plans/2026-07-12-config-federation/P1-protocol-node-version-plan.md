# P1 — 协议扩展 + 节点信息 + 版本闸

> 主纲: [../2026-07-12-config-federation-plan.md](../2026-07-12-config-federation-plan.md) ｜ bd: h-aii-xu64.10
> 目标: worker 上报 typed 能力(AgentCaps) + 节点信息(Arch/GoferVersion/StartedAt) + ProtocolVersion；server 拒绝旧协议 worker 并回升级提示。**加法式、不破坏下游**（P1 单独可构建+测试）。

## T1.1 wsproto: AgentBrief 类型 + Register 新字段 + 版本常量

**文件**: `internal/wsproto/frames.go`

1. 新增类型（放 Register 之上）：
```go
// AgentBrief is a worker-reported agent capability with the detail the UI cascade
// needs (type/interactive) beyond a bare key. Federation (xu64.10): the worker is
// the authority for ITS agents' type/interactive (the server may not have them in
// its own config).
type AgentBrief struct {
	Key         string `json:"key"`
	Type        string `json:"type,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
}
```

2. 协议版本常量（同文件或 `envelope.go`）：
```go
// ProtocolVersion is the current worker↔hub wire capability version. Bumped to 2
// when federation made the capability report authoritative (AgentCaps). A worker
// that reports a lower/absent version is rejected at registration (hub gate) with
// an upgrade prompt — old workers report 0 (field absent). NOT tied to gofer
// release version; it only gates capability-frame compatibility.
const ProtocolVersion = 2
```

3. `Register` 改注释 + 加字段（**保留** `Agents []string`）：
```go
// Register (w→s, P1): the worker announces its identity + capability snapshot on
// connect. The hub validates worker_id against the token binding (review #1) AND
// rejects a worker whose ProtocolVersion < wsproto.ProtocolVersion (xu64.10 联邦:
// 硬不兼容+升级提示). AgentCaps/Projects are now AUTHORITATIVE for validation+routing
// (was display-only); the worker still re-validates locally on dispatch (review #8).
type Register struct {
	WorkerID   string `json:"worker_id"`
	InstanceID string `json:"instance_id,omitempty"`
	// ProtocolVersion is the worker's capability-frame version (0 = pre-federation
	// worker → rejected). New workers set wsproto.ProtocolVersion.
	ProtocolVersion int      `json:"protocol_version,omitempty"`
	PtyCapable      bool     `json:"pty_capable,omitempty"`
	OS              string   `json:"os,omitempty"`
	Arch            string   `json:"arch,omitempty"`          // runtime.GOARCH (G5)
	GoferVersion    string   `json:"gofer_version,omitempty"` // buildinfo.DisplayVersion (G5)
	StartedAt       int64    `json:"started_at,omitempty"`    // worker process start, unix sec (G5)
	Labels          []string `json:"labels,omitempty"`
	Projects        []string `json:"projects,omitempty"`
	// Agents 保留为 key 列表(校验/selector 用，back-compat)；AgentCaps 是带 type/interactive
	// 的富明细(UI 级联用)。新 worker 两者都发（keys 冗余可接受，未来清理）。
	Agents        []string     `json:"agents,omitempty"`
	AgentCaps     []AgentBrief `json:"agent_caps,omitempty"`
	MaxConcurrent int          `json:"max_concurrent,omitempty"`
}
```

**验收 T1.1**: `wsproto` 帧 encode/decode round-trip 单测——新增字段 + AgentCaps 往返一致；**旧帧兼容**：一段 `{"worker_id":"w","agents":["a"]}`（无 protocol_version/agent_caps）decode 成 `Register{ProtocolVersion:0, Agents:["a"], AgentCaps:nil}`（证明加法式不破坏解码）。

## T1.2 wshub: 注册版本闸

**文件**: `internal/wshub/hub.go`（`Accept`，绑定校验 `:201` 之后、`newWorkerConn` `:203` 之前）

插入（模板对齐 `:194-199` 的 binding-reject）：
```go
	// 版本闸(xu64.10 硬不兼容): 旧协议 worker(ProtocolVersion 缺省=0)拒绝 + 升级提示。
	// 加法式帧仍能干净解码，故这里能回明确 Reason（而非解码崩溃）。
	if reg.ProtocolVersion < wsproto.ProtocolVersion {
		slog.Warn("hub rejected worker: protocol too old",
			"worker_id", reg.WorkerID, "worker_proto", reg.ProtocolVersion, "min", wsproto.ProtocolVersion)
		_ = writeEnvelope(ctx, conn, wsproto.TypeRegistered, "", wsproto.Registered{
			Accepted:   false,
			Reason:     fmt.Sprintf("worker 协议版本过旧(v%d)，请升级 worker 到 v%d 后重连", reg.ProtocolVersion, wsproto.ProtocolVersion),
			ServerTime: h.nowMillis(),
		})
		_ = conn.Close(websocket.StatusPolicyViolation, "protocol version too old")
		return
	}
```
> `fmt` 若未导入需补。测试 fixture（`hub_test.go:110` 等）构造的 `Register` 未设 `ProtocolVersion` → 会被新闸拒。**需同步更新这些测试 fixture**加 `ProtocolVersion: wsproto.ProtocolVersion`（否则既有 hub 测试红）。

**验收 T1.2**: 单测——`ProtocolVersion=0` 的 register → `Registered{Accepted:false}` + Reason 含"升级"；`ProtocolVersion=wsproto.ProtocolVersion` → 正常 accept。既有 hub 测试 fixture 补版本号后全绿。

## T1.3 worker 端填充 Register 新字段

**文件**: `internal/worker/client.go`（`:408` Register 构造）+ Client 结构体/构造处

1. Register 构造补字段：
```go
	if err := cl.writeFrame(ctx, wsproto.TypeRegister, "", wsproto.Register{
		WorkerID:        cl.workerID,
		InstanceID:      cl.instanceID,
		ProtocolVersion: wsproto.ProtocolVersion,
		PtyCapable:      ptyrunner.Available(),
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		GoferVersion:    cl.goferVersion, // info.DisplayVersion()
		StartedAt:       cl.startedAt,    // 进程启动 unix 秒
		Labels:          cl.labels,
		Projects:        cl.projects,
		Agents:          cl.agents,
		AgentCaps:       cl.agentCaps,
		MaxConcurrent:   cl.maxConc,
	}); err != nil {
```

2. `Client` 结构体加 `goferVersion string`、`startedAt int64`、`agentCaps []wsproto.AgentBrief`；在 client 构造处（`cl.projects`/`cl.agents` 赋值同处）填充：
   - `agentCaps` 由 worker config 的 `Agents map[string]AgentConfig` 构建：每项 `{Key:k, Type:ac.Type, Interactive:ac.Interactive}`（与 `cl.agents` keys 同源）。
   - `startedAt` = client 创建时的 unix 秒（沿用现有 nowFn/注入时钟，勿用裸 `time.Now` 若包内有时钟抽象）。
   - `goferVersion` 由 T1.4 传入的 `buildinfo.Info.DisplayVersion()`。

**验收 T1.3**: 构造 `agentCaps` 的 helper 单测（config.Agents → []AgentBrief，key/type/interactive 正确）；client 构造后字段非空。

## T1.4 buildinfo 接线到 worker 命令

**文件**: `internal/commands/app.go`, `internal/commands/worker.go`, worker client 构造链

- `app.go:39`: `NewWorkerCmd()` → `NewWorkerCmd(info)`（对齐 `NewServeCmd(info)`）。
- `worker.go:51` `NewWorkerCmd()` → `NewWorkerCmd(info buildinfo.Info)`；`runWorker` 内把 `info`（或 `info.DisplayVersion()`）透传到 worker client 构造。
- 参考 serve 侧现成路径：`buildinfo.Info` → `serve.Options.Build`（`serve/serve.go:54`）/ `httpapi SetBuildInfo`。worker 侧最短接线即可，不必进 core。

**验收 T1.4**: `go build ./...` 绿；`gofer worker` 起来后（隔离）server 日志 accept 行含 `gofer_version`/`arch`（或 `/v1/runners` P4 后可见）。

## P1 验收总纲

- [ ] T1.1 帧字段 + AgentBrief + ProtocolVersion 常量 + 往返/旧帧兼容单测绿
- [ ] T1.2 版本闸 + 既有 hub fixture 补版本号 + 拒绝/接受单测绿
- [ ] T1.3 worker 填充 AgentCaps/节点信息 + helper 单测绿
- [ ] T1.4 buildinfo 接线 worker，`go build ./...` 绿
- [ ] `go test ./internal/wsproto/... ./internal/wshub/... ./internal/worker/... -count=1` 绿
- [ ] 隔离 serve+worker 冒烟：新 worker 连上被 accept；模拟旧 worker(ProtocolVersion 0)被拒 + Reason 含升级提示
