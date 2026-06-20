# P2 — E2 CLI 日常补全（实施计划）

> 主纲：[`2026-06-20-cli-usability-tags-plan.md`](./2026-06-20-cli-usability-tags-plan.md) · 设计 §5.2/§7/§8。
> `job list`/`watch`/`rerun` + `client` 助手 + 只读 `/request` 端点 + shell 补全。依赖 P1（list 含 tag 维度）。

---

## P2-a client 助手：ListJobs / StreamJob / GetJobRequest

### 落点 `internal/client/client.go`
- `ListJobs(opts ListOpts) ([]job.JobResult, error)`：`GET /v1/jobs?project=&status=&caller=&tag=&agent=&runner=&since=&limit=`，解析 `{jobs:[...]}`。`ListOpts` 复用/对齐 `job.ListOpts`（或 client 本地等价 struct，避免 import 环则用本地）。
- `GetJobRequest(id string) (job.JobRequest, error)`：`GET /v1/jobs/{id}/request`（P2-b 端点），解析回 `JobRequest`。
- `StreamJob(ctx, id string, from int, onEvent func(SSEEvent)) error`：封装现有 `OpenStream`（返回裸 SSE `*http.Response`）+ **Go 端 SSE 帧解析**：
```go
type SSEEvent struct { Event string; Data []byte } // event: 行 + data: 行（多 data 行拼接）
// StreamJob 读 resp.Body，按空行(\n\n)切帧，解析 event:/data:，回调 onEvent；
// ctx 取消或读到 event:end / EOF 即返回。帧解析参考 internal/runner/peerhttp 既有消费。
```
> SSE 事件类型见 `internal/httpapi/stream_handler.go`：`status`/`log`/`log-rotated`/`interaction`/`end`。**复用 `runner/peerhttp` 既有 Go 端 SSE 解析逻辑**（抽到 client 共享，不重造）；peerhttp 本期不强制切换（D4）。

### P2-a 验收
- 单测 `internal/client`（httptest server）：`ListJobs` 解析多 job + 过滤参数进 URL；`GetJobRequest` 解析回 JobRequest；`StreamJob` 对一段 SSE（status→log→end）按序回调、`end`/ctx 取消即停。

---

## P2-b `GET /v1/jobs/{id}/request` 端点

### 落点
- `internal/httpapi/job_handler.go`：`handleGetJobRequest`——`res, ok := s.jobs.Get(id)`（404 if !ok）；取 `res.RequestJSON`（空→404 "no request recorded"）；以 `json.RawMessage` 直出对象（`c.JSON(200, json.RawMessage(res.RequestJSON))`），即回原始 JobRequest。
- `internal/httpapi/server.go`：authed 组加 `r.GET("/jobs/{id}/request", s.handleGetJobRequest)`。
- **不**改 `handleGetJob`（request_json 仍不进主响应，避免 list 膨胀，D1）。

### P2-b 验收
- 单测 `internal/httpapi`：提交 job 后 `GET /v1/jobs/{id}/request` 回原始请求（project/agent/cmd/prompt/tags 等齐全）；未知 id→404；无 request_json 的（理论不存在）→404 不 panic。

---

## P2-c 子命令 job list/watch/rerun + 补全

### 落点 `internal/commands/job.go`（仿现有 run/show/logs/cancel 的 gcli.Command 形）
- **`job list`**：选项 `--project/--status/--caller/--tag/--agent/--runner/--since/--limit`（gcli StrOpt/IntOpt，绑全局 opts struct），调 `cli.ListJobs(opts)`，**表格输出**（列：ID / STATUS / AGENT / RUNNER / PROJECT / TAGS / STARTED）。空结果友好提示。
- **`job watch <id>`**：`c.AddArg("id",required)` + `--from`，调 `cli.StreamJob(ctx,id,from,onEvent)`：
  - `status` 事件 → 打印状态变更行；`log` 事件 → 原样打印增量日志；`end` → 停。
  - 终态 exit code 映射 job 终态（done=0 / failed=非0 / cancelled=130 等，对齐现有约定）。Ctrl-C（ctx 取消）干净退出。
- **`job rerun <id>`**：`c.AddArg("id")` + `--watch`，流程：`req := cli.GetJobRequest(id)` → **`req.RequestID = ""`**（D5，清幂等 key=新 job）→ `cli.SubmitJobSync(req)` → 打印新 job id；`--watch` 则接着 `StreamJob` 跟到终态。
- 三命令加入 `NewJobCmd()` 的 `Subs` 数组。

### 补全 `internal/commands/app.go`
- 接 gcli 内置补全：`app.Add(builtin.GenAutoComplete())` 或等价（查 `github.com/gookit/gcli/v3` 当前 API；提供 `gofer completion`/`gofer genac` 生成 bash/zsh 脚本）。v1 仅静态命令/选项补全（动态 id/project 候选留后续）。

### P2-c 验收
- 单测 `internal/commands`（仿 `job_test.go` 命令注册断言）：`job` 子命令含 list/watch/rerun（注册存在 + 选项绑定）。
- 真机冒烟：`gofer job list --tag x` 表格正确；`gofer job watch <id>` 实时跟到终态、exit code 对；`gofer job rerun <id>` 起新 job（新 id、不被原 request_id 去重）；`gofer completion bash` 输出可 source。

### 提交点
P2-a / P2-b / P2-c 各绿灯分别 `git commit`；更新主纲进度 + 实施结果一行。

> 注：`ListOpts` 跨包（client 需要 job.ListOpts 的字段）。若 client import job 造成环依赖，client 内定义等价 `ListJobsOpts` 并在 URL 拼装处用之（与 web 端 `ListJobsOpts` 对称）。实施时先验证依赖方向再定，回报说明。
