# P1 — E13 job 事件时间线（实施计划）

> 主纲：[`2026-06-20-job-events-notify-plan.md`](./2026-06-20-job-events-notify-plan.md) · 设计 §5.1–5.4/§6/§7。
> 给每个 job 落 append-only 生命周期事件流 + API + SSE + Web 时间线。**仅 local 视角**（host 记录 host 所见）。

---

## P1-a `job_events` 表 + DAO + prune 连带删

### 落点
- `internal/jobstore/store.go`：`schemaStmts` 加 `job_events` 建表 + 索引（仿 `interactions` 第二表 `:84-95`，**IF NOT EXISTS**，无需 migrate）。
- `internal/jobstore/events.go`（新）：`JobEvent` struct + `InsertJobEvent` + `ListJobEvents`。
- `internal/job/prune.go`：prune job 时连带 `DELETE FROM job_events WHERE job_id=?`（best-effort，仿现有日志目录删）。

### 步骤
**1) 建表**（store.go schemaStmts）：
```sql
CREATE TABLE IF NOT EXISTS job_events (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id      TEXT    NOT NULL,
  type        TEXT    NOT NULL,
  detail_json TEXT,
  at          INTEGER NOT NULL
)
-- 另一条 schemaStmts 元素：
CREATE INDEX IF NOT EXISTS idx_job_events_job ON job_events(job_id, seq)
```
**2) DAO**（`jobstore/events.go`）：
```go
type JobEvent struct {
    Seq    int64  `json:"seq"`
    JobID  string `json:"job_id"`
    Type   string `json:"type"`
    Detail string `json:"detail,omitempty"` // detail_json 原文(已是 JSON 字符串)，前端 parse
    At     int64  `json:"at"`
}
// InsertJobEvent: INSERT only（append-only），写 writeMu lock 内（仿 UpsertInteraction 的并发口径）。
func (s *Store) InsertJobEvent(e JobEvent) error
// ListJobEvents: WHERE job_id=? [AND seq>?] ORDER BY seq ASC。
func (s *Store) ListJobEvents(jobID string, sinceSeq int64) ([]JobEvent, error)
```
**3) prune 连带**（prune.go）：`PruneJobs` 删行后，对被删 job_id 批量 `DELETE FROM job_events WHERE job_id IN (...)`（或在 jobstore.PruneJobs 内一并删，保持一致）。

### P1-a 验收
- 单测 `internal/jobstore`：新库建表含 `job_events`（PRAGMA）；`InsertJobEvent` 三条→`ListJobEvents(jobID,0)` 按 seq ASC 返三条、`ListJobEvents(jobID, seq1)` 只返其后；不同 job 隔离。
- 单测：prune 删 job → 其 job_events 一并清。

---

## P1-b 中央 recordEvent + 7 处插桩

### 落点
- `internal/job/events.go`（新）：`func (s *Service) recordEvent(jobID, eventType string, detail any)`。
- `internal/job/service.go` / `cancel.go` / `interaction.go`：7 处插桩。

### 步骤
**1) recordEvent**（best-effort，detail marshal + 上限截断）：
```go
const maxEventDetailBytes = 8 * 1024
// recordEvent 落一条生命周期事件(append-only)。best-effort：marshal/写库失败仅 warning，
// 绝不影响 job 终态(呼应 captureOutcomes 铁律)。detail 不含 secret(SR403)。
func (s *Service) recordEvent(jobID, eventType string, detail any) {
    var dj string
    if detail != nil {
        if b, err := json.Marshal(detail); err == nil && len(b) <= maxEventDetailBytes { dj = string(b) }
    }
    if err := s.meta.InsertJobEvent(jobstore.JobEvent{JobID: jobID, Type: eventType, Detail: dj, At: s.nowFn().Unix()}); err != nil {
        log.Printf("recordEvent: job %s type %s: %v", jobID, eventType, err)
    }
}
```
**2) 7 处插桩**（每处事件 type + detail，按设计 §5.2）：
| type | 插桩点 | detail |
|---|---|---|
| `job.submitted` | `service.go` Submit 持久化 queued 后(:300 附近) | `{project,agent,runner,caller_id,tags}` |
| `job.dispatched` | `service.go` 远端 Forward 准备好(:232-248，仅 remote 分支) | `{runner,worker_id}` |
| `job.running` | `service.go` queued→running 置 Running 后(:368-371) | `nil` |
| `job.terminal` | `service.go` finish 设终态后(:415-424) | `{status,exit_code,error}` |
| `job.cancelled` | `cancel.go` 发 cancel 信号处(:56-58) | `{was_terminal}` |
| `interaction.created` | `interaction.go` CreateInteraction persist 后(:144) | `{interaction_id,type,prompt}` |
| `interaction.answered` | `interaction.go` AnswerInteraction persist 后(:279) | `{interaction_id,answer}` |
> 插桩放各转换点**持久化成功之后**（事件反映已发生的事实）。`job.terminal` 的 detail.status 区分 done/failed/timeout/cancelled。

### P1-b 验收
- 单测 `internal/job`：跑一个 echo exec job（无交互）→ `ListJobEvents` 含 `job.submitted`→`job.running`→`job.terminal(done)` 有序；含交互的 job → 另见 `interaction.created`/`interaction.answered`；cancel 的 job → 见 `job.cancelled`+`job.terminal(cancelled)`。
- best-effort：注入 InsertJobEvent 失败 → job 仍正常终态（recordEvent 不 panic、不改状态）。

---

## P1-c GET /events + SSE pumpEvents

### 落点
- `internal/httpapi/`（`event_handler.go` 新 或 job_handler.go）：`handleListEvents`。
- `internal/httpapi/server.go`：路由 `r.GET("/jobs/{id}/events", s.handleListEvents)`。
- `internal/httpapi/stream_handler.go`：加 `pumpEvents()`，与 `pumpLogs`/`pumpInteractions` 并行轮询。

### 步骤
**1) handler**：取 job(404 if !ok)；`since := parseInt64(c.Query("since"))`；`c.JSON(200, {"events": s.jobs.ListJobEvents(id, since)})`（Service 暴露 `ListJobEvents` 转发 meta）。
**2) SSE**（stream_handler.go）：循环内加 `pumpEvents(lastSeq)`——`ListJobEvents(id, lastSeq)` 取增量 → 每条发 `event:event` 帧 `{seq,type,detail,at}`、更新 `lastSeq`；初始（live 前）回放现有事件（仿 `pumpInteractions` 现态回放）。事件帧并入现有 writer。

### P1-c 验收
- 单测 `internal/httpapi`：`GET /v1/jobs/{id}/events` 返事件列表（有序）；`?since=N` 增量；未知 id→404。
- 单测/集成：SSE 流含 `event` 帧（一个 job 的 submitted→running→terminal 经 SSE 收到）。回归：现有 log/status/interaction/end 帧不变。

---

## P1-d Web 事件时间线面板

### 落点
- `web/src/api/types.ts`：`JobEvent { seq, type, detail?, at }`。
- `web/src/api/client.ts`：`listEvents(id)`。
- `web/src/api/sse.ts` + `web/src/views/JobDetail.vue`：SSE `event` 帧解析 + 时间线面板（interactions 与 outcomes 之间）。

### 步骤
- sse.ts：`onEvent` 加 `case 'event'` → push 到 `timelineEvents`。
- JobDetail.vue：`timelineEvents = ref<JobEvent[]>([])`，初始 `listEvents(id)` 拉 + SSE 增量 append（按 seq 去重/有序）；面板每事件一行（图标按 type + 关键 detail + 相对时间 `fmtTime`）。仿现有 interactions 渲染风格。

### P1-d 验收
- `pnpm -C web build` 绿。
- 真机：含交互的 job 详情页时间线依次展示 submitted→running→interaction.created→interaction.answered→terminal。

### 提交点（SR1202）
P1-a（表+DAO）/ P1-b（recordEvent+插桩）/ P1-c（API+SSE）/ P1-d（Web）各绿灯分别 `git commit`；更新主纲进度 + 实施结果一行。
