# P4 — 远端捕获回传（worker / peer-http）（实施计划）

> 主纲：[`2026-06-20-job-outcomes-audit-plan.md`](./2026-06-20-job-outcomes-audit-plan.md) · 设计 §6.6 / §10-D6。
> P1–P3 在执行机本地已能 capture（worker/peer 进程跑同一 `captureOutcomes` 代码）；P4 把"渲染命令/结构化结果/diff 摘要/产物清单"从执行机**带回 host job**，让远端 job 的详情同样齐全。**v1 仅回清单+小结果**（大产物文件留执行侧/共享盘，D6）。依赖 P1–P3。

---

## 公共改动：res 承载 outcome + captureOutcomes 取数分流

### 落点
- `internal/runner/runner.go`：`Result` 加可选 outcome 字段。
- `internal/job/outcomes.go`：`captureOutcomes` 改签名带 `res`，远端优先用 res、本地回退磁盘扫描。

```go
// runner.Result（runner.go）新增（local runner 不填→nil/空，走本地扫描）：
type Outcome struct {
	RenderedCommand string         `json:"rendered_command,omitempty"`
	ResultJSON      string         `json:"result_json,omitempty"`
	DiffSummary     string         `json:"diff_summary,omitempty"`
	Artifacts       []ArtifactItem `json:"artifacts,omitempty"` // 仅清单元数据
	Source          string         `json:"source,omitempty"`    // "" / worker:<id> / peer:<name>
}
type Result struct { ExitCode int; Err error; Outcome *Outcome }  // 加 Outcome
```
```go
// outcomes.go：execute 改为 captureOutcomes(entry, req, res)
func (s *Service) captureOutcomes(entry *jobEntry, req runner.Request, res runner.Result) {
	if res.Outcome != nil {            // 远端：执行机已 capture 并回传 → 直接落
		applyOutcome(entry, res.Outcome)
		return
	}
	// 本地：磁盘扫描（P1–P3 既有逻辑）
	...
}
```
> `execute()`：`res := run.Run(ctx, req)` 后 `s.captureOutcomes(entry, req, res)`（P1 是 `(entry, req)`，此处加 `res` 实参——小改）。

---

## P4-a worker（WS Outcome 帧）

### 落点
- `internal/wsproto/frames.go`：加 `Outcome` 帧（w→s）+ opcode。
- `internal/worker/*`：worker 本地 job 终态后，读其 outcome 字段 → 发 `Outcome` 帧（在 `Result` 帧之前）。
- `internal/wshub/*`：读循环把 `Outcome` 帧分发到对应 job 的 `JobSink`（新 `OnOutcome`）。
- `internal/runner/worker/runner.go`：`boundedSink` 收 `OnOutcome` → 暂存；`Run` 返回时填入 `runner.Result.Outcome`。

### 步骤
```go
// wsproto.Outcome（w→s）
type Outcome struct {
	JobID           string          `json:"job_id"`
	RenderedCommand string          `json:"rendered_command,omitempty"`
	ResultJSON      string          `json:"result_json,omitempty"`
	DiffSummary     string          `json:"diff_summary,omitempty"`
	Artifacts       json.RawMessage `json:"artifacts,omitempty"`
}
```
- worker：本地执行用的就是共享 `job.Service`（P1–P3 已让它 capture 到 worker 的 result_dir + DB），终态后取这 4 个字段，构 `Outcome` 帧 `hub send`（worker 侧发送封装）。**大产物文件不进帧**（仅 Artifacts 清单元数据，D6）。
- hub：`JobSink` 接口加 `OnOutcome(o wsproto.Outcome)`；读循环 demux（仿 Log/Result 分发，见 `wshub` 读循环）。
- worker runner `boundedSink`：加 `OnOutcome` 存 `s.outcome`；`Run` 的终态返回处 `return runner.Result{ExitCode:…, Err:…, Outcome: s.outcome}`（`worker/runner.go:134` 附近 result 分支）。`Outcome.Source = "worker:"+workerID`。

### P4-a 验收
- 单测 `internal/runner/worker`：fake hub 投递 `Outcome` 帧 → `Run` 返回的 `Result.Outcome` 含 4 字段。
- 单测/e2e `internal/worker`：worker 本地 job 写 result.json/artifacts/改 git → host 侧 `Get(id)` 的 rendered_command/result_json/diff_summary/artifacts 清单齐全、`source=worker:<id>`。
- 回归：worker 不发 Outcome（旧 worker）时 host job 正常、outcome 为空。

---

## P4-b peer-http（SSE outcome / GetJob 复用）

### 落点 `internal/runner/peerhttp/runner.go`
peer 是另一台 gofer，其 get_job 在 P1/P3 后**已返回** `rendered_command`/`result_json`/`diff_summary`。host peerhttp runner 终态时已 `GetJob(peerID)`（`peerhttp/runner.go:103`）——直接把这几个字段拷进 `runner.Result.Outcome`：
```go
final, _ := r.c.GetJob(ctx, peerID)        // 已有调用
res.Outcome = &runner.Outcome{
	RenderedCommand: final.RenderedCommand,
	ResultJSON:      final.ResultJSON,
	DiffSummary:     final.DiffSummary,
	Source:          "peer:" + r.name,
}
```
- 产物清单：host peerhttp runner 额外 `GET <peer>/v1/jobs/{peerID}/artifacts` 填 `Outcome.Artifacts`（client 加方法）。
- 产物**下载**：host 侧 `/v1/jobs/{id}/artifacts/{name}` 检测 `source=peer:*` 时**代理**到 peer 的下载端点（`artifact_handler` 加 peer 分流）；或详情页直给 peer 下载链接（v1 可简化为后者）。

### P4-b 验收
- 单测/e2e `internal/runner/peerhttp` 或 host+peer 双 bridge：peer 跑写 result.json/改 git 的 job → host 详情含 rendered_command/result_json/diff_summary、`source=peer:<name>`、产物清单可见。

---

## P4-c Web：来源标注

`JobDetail.vue` 产出面板按 `outcome.source` 标注"在 worker w-gpu / peer X 执行"，远端产物下载指向对应来源（worker 大文件标注"留在 worker / 共享盘"，peer 走代理或直链）。

### 提交点
P4-a / P4-b（/P4-c 随附）各绿灯分别 `git commit`；更新主纲进度全勾 + 出**完成报告**（SR1430）。**P4-a 触碰 wsproto + hub 读循环 + worker，需全量 `go test ./...` 确认 ws/worker/peer E2E 不回归**。

> **范围注记**（D6）：v1 远端只回**清单 + 小结果**（result_json/diff_summary/rendered_command + artifacts 元数据）。worker 大产物**文件**留 worker 侧（共享盘则 host 可直读 result_dir；否则标注 source、专用 WS/HTTP 拉取通道留后续迭代）。
