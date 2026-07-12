# P3 — submit 校验改造（核心 G1 + G2）

> 主纲: [../2026-07-12-config-federation-plan.md](../2026-07-12-config-federation-plan.md) ｜ bd: h-aii-xu64.10 ｜ 依赖: P2
> 目标: **G1** worker-only project 不必在 server 全局 config 定义即可提交；**G2** agent/project 不在目标 runner 上时 **host 端 fail-fast**（显式 worker_id 在 validate 拒、自动选在 selector 过滤）。worker 端二次校验保留为兜底。

## 校验矩阵（改造后目标行为）

| runner 类型 | project 校验 | agent 校验 | 时机 |
|---|---|---|---|
| local | 必须 ∈ 全局 `cfg.Projects`（**不变**） | §13 白名单闸 + exec 闸（**不变**） | validate |
| worker + 显式 worker_id / 配置默认 worker | ∈ 该 worker 上报 projects（**放行 worker-only**） | ∈ 该 worker 上报 agents（fail-fast） | validate |
| worker + 自动选(labels) | 不在 validate 查（worker 未定） | 交 selector 按 project+agent 过滤，无候选→`ErrNoCapableWorker` | selectTargetWorker |
| peer-http | 必须 ∈ 全局 `cfg.Projects`（**本期不变**，保持现状放宽 agent） | 现状放宽（不变） | validate |

## T3.1 errors + project 放行（G1）

**文件**: `internal/job/errors.go`, `internal/job/config.go`

1. `errors.go` 加：
```go
var (
	ErrUnknownProjectOnRunner = errors.New("project not available on target runner")
	ErrAgentNotOnRunner       = errors.New("agent not available on target runner")
	ErrNoCapableWorker        = errors.New("no online worker satisfies the required project+agent")
)
```

2. `config.go` `validate` 的 project 块（现 `:46-49`）改造：
```go
	proj, projKnown := cfg.Projects[req.ProjectKey]
	isWorker := isWorkerRunner(cfg, req.Runner)
	if !projKnown {
		if !isWorker {
			// local / peer-http: project 仍必须全局定义(行为不变)。
			return config.ProjectConfig{}, fmt.Errorf("%w: unknown project %q", ErrUnknownProject, req.ProjectKey)
		}
		// worker runner: worker-only project 放行(G1)。host 不执行该 job(worker 执行并用
		// 自己的 project config)，故 host 侧只需一个占位 proj(仅承载 key 相关记账)。
		proj = config.ProjectConfig{}
	}
```

> ⚠️ **R2 必办**：`validate` 返回的 `proj` 下游用于结果目录/exchange/notify/capture。worker-only project 时 `proj` 为空——**必须核实** host 侧对 worker 路径不 deref 需要真实 proj 的字段（worker job 结果按 `storage.root/<project_key>/<job_id>` 落盘，key 驱动）。**本阶段带一条集成验证**（见验收），若发现空 proj 破坏落盘，则合成最小 proj（填 key + 默认 exchange/result subdir 自全局 storage 默认）。

## T3.2 worker runner 能力 fail-fast（G2，显式/默认 worker）

**文件**: `internal/job/config.go`（`validate` 内，project 块之后、`if len(proj.AllowedAgents)>0` 白名单闸附近）

```go
	// 联邦(G2): worker runner 且能定到具体 worker(显式 worker_id 或 runner 配置默认)时，
	// host 端提前校验 project/agent 在该 worker 上可用；自动选(labels)路径 worker 未定，
	// 交 selectWorker 过滤(T3.3)。peer-http 不走此路(保持现状)。
	if isWorker {
		wprojs, wagents, online := s.capabilitiesFor(cfg, req.Runner, req.WorkerID)
		if online {
			if !containsStr(wprojs, req.ProjectKey) {
				return config.ProjectConfig{}, fmt.Errorf("%w: project %q not on worker for runner %q", ErrUnknownProjectOnRunner, req.ProjectKey, req.Runner)
			}
			if !containsStr(wagents, gateAgent) {
				return config.ProjectConfig{}, fmt.Errorf("%w: agent %q not on worker for runner %q", ErrAgentNotOnRunner, gateAgent, req.Runner)
			}
		}
	}
```
> `containsStr` 小 helper（或用现成）。`gateAgent` 已在上文解析（resume 源 agent 优先，否则 `req.Agent`）。
> 空能力语义：worker 上报 projects/agents 为空列表 = 该 worker 声明无此维度 → `containsStr` 判否 → 拒（保守，与 selector 过滤一致）。

## T3.3 selectWorker 按 project+agent 过滤（G2，自动选）

**文件**: `internal/job/selector.go`, `internal/job/config.go`(`selectTargetWorker`)

1. `selectWorker`（`:40-64`）签名加 `project, agent string`，过滤加两条：
```go
func selectWorker(cands []WorkerCandidate, required []string, interactive bool, project, agent string) string {
	ok := make([]WorkerCandidate, 0, len(cands))
	for _, w := range cands {
		if w.HeartbeatAge > workerStaleAfter { continue }
		if !hasAllLabels(w.Labels, required) { continue }
		if interactive && !w.PtyCapable { continue }
		if project != "" && !hasAllLabels(w.Projects, []string{project}) { continue } // 复用成员判定 or containsStr
		if agent != "" && !containsStr(w.Agents, agent) { continue }
		ok = append(ok, w)
	}
	// …排序/返回不变…
}
```
2. `selectTargetWorker`（`config.go:161-191`）调用处传 `req.ProjectKey` + `gateAgent`；`selectWorker` 返回 `""` 时**改报** `ErrNoCapableWorker`（带 project/agent 名），而非泛化"no worker"：
```go
	wid := selectWorker(s.workers.Candidates(), req.Labels, req.Interactive, req.ProjectKey, gateAgent)
	if wid == "" {
		return fmt.Errorf("%w: project=%q agent=%q", ErrNoCapableWorker, req.ProjectKey, req.Agent)
	}
```
> 注意 `selectTargetWorker` 仅在**无显式 worker_id**（自动选）时进入；显式 worker_id 已在 T3.2 校验。确认 `gateAgent` 在 `selectTargetWorker` 作用域可得（否则用 `req.Agent`）。

**验收 T3（逐分支单测，`job` 包）**：
- **local 缺 project** → 仍 `ErrUnknownProject`（回归，行为不变）。
- **worker-only project（不在全局，worker 上报有）+ 显式 worker_id** → 放行（G1）。
- **worker runner + agent 不在该 worker 上报** → `ErrAgentNotOnRunner`（G2 fail-fast）。
- **worker runner + project 不在该 worker** → `ErrUnknownProjectOnRunner`。
- **自动选(labels) + 无 worker 具备 project/agent** → `ErrNoCapableWorker`（带名）。
- **自动选 + 有具备的 worker** → 选中该 worker。
- **worker 离线（capabilitiesFor online=false）+ 显式 worker_id** → 走 selector/或报离线（明确不 panic）。
- **local runner 正常 project/agent** → 通过（回归）。
- 用 mock `WorkerSelector`（Candidates/Candidate 返回构造的能力）驱动，不需真 hub。

## T3.4 R2 集成验证（worker-only project 落盘）

**验收（隔离 serve+worker 或 job 包集成测试）**：worker 配 project `demo-w`（server 全局无 `demo-w`）→ 经 worker runner 提交 job → host 端**不报 ErrUnknownProject**、job 正常派发、worker 执行、结果 `storage.root/demo-w/<job_id>` 正常落盘 + 可经 `/v1/jobs/<id>` 回读。若空 proj 破坏此路径，按 T3.1 R2 合成最小 proj 修复并复测。

## P3 验收总纲 — ✅ 全部完成（commit `d7db513`）

- [x] T3.1 errors + worker-only project 放行（G1）
- [x] T3.2 worker 能力 fail-fast（显式/默认 worker，G2）
- [x] T3.3 selectWorker project+agent 过滤 + ErrNoCapableWorker（自动选，G2）
- [x] T3.4 worker-only project 端到端落盘验证通过（R2）—— in-process e2e + 真机双进程冒烟
- [x] 逐分支单测全绿（8 分支 + selectWorker 过滤 + R2 落盘）
- [x] `go test ./internal/job/... -count=1` 绿 + 全量 `go test ./... -p1 -count=1` 绿

## 实施补记（R2 深挖 + 复审）

**R2 用选项 (b)：空 `ProjectConfig{}` 不安全，必须合成最小 proj。** 实现子 agent 逐条追踪 `validate` 返回的 `proj` 在 worker 派发路径的下游用途，发现两处真问题（计划未预见）：
- **空 proj 会让 `checkRunnerAllowed` 只放行 `local`** → 每个 worker-only job 被直接拒 = **干掉 G1**。合成 proj 设 `AllowedRunners:[req.Runner]`（host 对自己未定义的 project 无准入策略；真边界是 G2 闸 + worker 自校验，#2）。
- **`storage.root` 未设时**（所有随附示例的默认）结果目录回落 `SafeJoin(ExecPath(""))` = **serve 进程 CWD**，静默错放。合成 proj 镜像 `ResolveDBPath` 回落：`HostPath=<config-dir>`、`ExchangeSubdir=remote`、`ResultSubdir=<key>` → `<config-dir>/remote/<key>/<date>/<job_id>`。
- **安全（计划外）**：G1 让 project key 变**请求方可控**且落进路径 → `../` 可逃逸。加 `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` 仅在 worker-only 分支（已配 key 不动）。

**G2 gate 条件细化**：`isWorker && (req.WorkerID != "" || len(req.WorkerLabels)==0)` —— labels 自动选路径不在 validate 查（default worker 非该 job 目标），交 selector 过滤，与矩阵一致。

**sentinel 包装既有错误**（ErrNoCapableWorker→ErrNoEligibleWorker=503；On-Runner 类→ErrInvalidRequest=400），httpapi `submitStatus` 零改动、`errors.Is` 既有调用不破。

**真机冒烟 5/5 PASS**：G1 `wonly`(host 无) 提交 done/exit0，落盘 `<config-dir>/remote/wonly/...`、serve CWD 零散落；G2 `nonesuch` agent → 400 ErrAgentNotOnRunner、`ghost` project → 400 ErrUnknownProjectOnRunner，均**无派发**（worker 日志停 2 行）。

**对抗式复审 0 confirmed bug**（runner allowlist 绕过 / 路径逃逸 / fixture 掩盖 / 错误码 / local 回归 / fall-through 全攻过）。两个低危 UX 缺口（交互式 worker-only 报错误导 / `ListJobs(project=worker-only)` 空）→ 归档 `tools-2gk`，不扩 P3。
