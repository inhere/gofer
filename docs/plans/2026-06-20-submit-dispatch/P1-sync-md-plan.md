# P1 — 同步等待 + md+yaml 提交（实施计划）

> 主纲：[`2026-06-20-submit-dispatch-plan.md`](./2026-06-20-submit-dispatch-plan.md) · 设计：[`../../design/2026-06-20-submit-dispatch-design.md`](../../design/2026-06-20-submit-dispatch-design.md) §6.1/§6.2。
> 两项独立可并行；都只改提交侧，执行/读路径不动。

---

## P1-a 同步等待（G1，决策 D1）

### 落点
- `internal/job/cancel.go`：新增 `Service.WaitFor(id, timeout)`（在现有 `Wait` 外包超时）。
- `internal/job/model.go`：`JobRequest` 加 `Sync`、`WaitTimeoutSec` 字段。
- `internal/httpapi/job_handler.go`：`handleCreateJob` 加 sync 分支。
- `internal/commands/job.go`：`job run` 加 `--sync` / `--wait-timeout`。

### 步骤

**1) `Service.WaitFor`** — `internal/job/cancel.go`，紧邻现有 `Wait`(`cancel.go:71`)：
```go
// WaitFor blocks until the job reaches a terminal state OR timeout elapses.
// ok=false 表示超时（job 仍在后台继续，不取消）或未知 id。已驱逐的终态 job
// 走 Wait 的 DB 回落立即返回。timeout<=0 等同 Wait（无限等，仅测试用）。
func (s *Service) WaitFor(id string, timeout time.Duration) (JobResult, bool) {
	entry := s.entry(id)
	if entry == nil {
		if rec, ok, _ := s.meta.GetJob(id); ok {
			return fromRecord(rec), true
		}
		return JobResult{}, false
	}
	if timeout <= 0 {
		<-entry.done
		return entry.snapshot(), true
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-entry.done:
		return entry.snapshot(), true
	case <-t.C:
		return entry.snapshot(), false // 未到终态：返当前快照 + ok=false
	}
}
```
> `cancel.go` 已 import `time`？否则补。`entry.snapshot()` 在超时分支返回非终态快照（含 id/status=running），handler 只用其 id。

**2) `JobRequest` 字段** — `internal/job/model.go`（紧跟 `RequestID`）：
```go
// Sync requests synchronous submit: the HTTP handler blocks until the job is
// terminal (capped server-side) and returns the final JobResult. Can also be
// set via ?wait=1. WaitTimeoutSec overrides the default wait cap (clamped).
Sync           bool `json:"sync,omitempty"`
WaitTimeoutSec int  `json:"wait_timeout_sec,omitempty"`
```

**3) handler sync 分支** — `internal/httpapi/job_handler.go` `handleCreateJob`：
```go
res, err := s.jobs.Submit(req)
if err != nil {
	writeError(c, submitStatus(err), "job rejected", err.Error())
	return
}
// 同步等待：body.sync 或 ?wait=1。已是终态（如幂等命中既有终态 job）直接返回。
if wantSync(c, req) && !job.IsTerminal(res.Status) {
	wait := clampWait(req.WaitTimeoutSec) // 默认 30s，硬上限 60s
	if final, ok := s.jobs.WaitFor(res.ID, wait); ok {
		c.JSON(http.StatusOK, final)
		return
	}
	c.SetHeader("X-Gofer-Async", "1")
	c.JSON(http.StatusAccepted, res) // 202 + 初始 res(含 id)，客户端转轮询
	return
}
c.JSON(http.StatusOK, res)
```
辅助（同文件）：
```go
const (
	defaultWaitSec = 30
	maxWaitSec     = 60
)
func wantSync(c *rux.Context, req job.JobRequest) bool {
	return req.Sync || c.Query("wait") == "1" || c.Query("wait") == "true"
}
func clampWait(sec int) time.Duration {
	if sec <= 0 { sec = defaultWaitSec }
	if sec > maxWaitSec { sec = maxWaitSec }
	return time.Duration(sec) * time.Second
}
```
> 需导出 `job.IsTerminal`（现 `cancel.go` 有未导出 `isTerminal`；新增导出 `func IsTerminal(status string) bool` 复用之，或直接判 `res.Status==done/failed/...`）。

**4) CLI `--sync`** — `internal/commands/job.go`：`jobRunOpts` 加 `sync bool`、`waitTimeout int`；`Config` 内：
```go
c.BoolOpt(&jobRunOpts.sync, "sync", "", false, "submit synchronously: server waits for terminal state, then returns")
c.IntOpt(&jobRunOpts.waitTimeout, "wait-timeout", "", 0, "sync wait cap in seconds (0 = server default 30s)")
```
`runJobRun` 内构造 `req.Sync = jobRunOpts.sync; req.WaitTimeoutSec = jobRunOpts.waitTimeout`。命中 202（`X-Gofer-Async`）时自动回落到既有 `waitTerminal(cli, res.ID)` 客户端轮询（平滑）。需 client 暴露响应状态码或头：`client.SubmitJob` 返回值带上 status / 是否 async（小改 `internal/client`）。

### P1-a 验收
- 单测 `internal/job`：起一个秒级 done 的假 job，`WaitFor(id, 2s)` 返 ok=true+done；起一个永不结束的 job，`WaitFor(id, 50ms)` 返 ok=false 且 job 仍 running。
- 单测 `internal/httpapi`：`POST /v1/jobs {sync:true}` 对快命令返 200+done+exit_code；对慢命令（mock）超 cap 返 202+`X-Gofer-Async`。
- 真机：`gofer job run -p P -a exec --sync -- echo hi` 一次往返打印终态 + exit_code=0。
- 回归：不带 sync 的提交仍 200+queued 立即返回。

---

## P1-b md+yaml 提交（G2，决策 D2）

### 落点
- `internal/job/model.go`：`JobRequest` 各字段补 `yaml:"..."` tag（含 `caller_id yaml:"-"`）。
- `internal/httpapi/mdreq.go`（新）：frontmatter 解析。
- `internal/httpapi/job_handler.go`：`handleCreateJob` content-type 分支。
- `internal/commands/job.go`：`job run -f <file.md>`。

### 步骤

**1) `JobRequest` yaml tag** — `internal/job/model.go`（与 json 同名 snake_case；`caller_id` 标 `-` 防伪造）：
```go
ProjectKey  string   `json:"project_key" yaml:"project_key"`
Agent       string   `json:"agent" yaml:"agent"`
Runner      string   `json:"runner" yaml:"runner"`
Prompt      string   `json:"prompt,omitempty" yaml:"prompt,omitempty"`
Cmd         []string `json:"cmd,omitempty" yaml:"cmd,omitempty"`
Cwd         string   `json:"cwd,omitempty" yaml:"cwd,omitempty"`
TimeoutSec  int      `json:"timeout_sec,omitempty" yaml:"timeout_sec,omitempty"`
Title       string   `json:"title,omitempty" yaml:"title,omitempty"`
WorkerID    string   `json:"worker_id,omitempty" yaml:"worker_id,omitempty"`
Sync        bool     `json:"sync,omitempty" yaml:"sync,omitempty"`
WaitTimeoutSec int   `json:"wait_timeout_sec,omitempty" yaml:"wait_timeout_sec,omitempty"`
CallerID    string   `json:"caller_id,omitempty" yaml:"-"`
RequestID   string   `json:"request_id,omitempty" yaml:"request_id,omitempty"`
```
> `WorkerLabels`（P2）届时一并带 `yaml:"worker_labels,omitempty"`。

**2) 解析器** — `internal/httpapi/mdreq.go`（用 `github.com/goccy/go-yaml`，go.mod 已有）：
```go
package httpapi

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/inhere/gofer/internal/job"
)

const maxMarkdownBytes = 256 * 1024 // frontmatter+正文总上限

// parseMarkdownRequest 解析「yaml frontmatter + markdown 正文」为 JobRequest：
// frontmatter（首个 '---' 行到次个 '---' 行之间）→ 字段；其余正文 → Prompt。
func parseMarkdownRequest(body []byte) (job.JobRequest, error) {
	var req job.JobRequest
	if len(body) > maxMarkdownBytes {
		return req, fmt.Errorf("markdown body exceeds %d bytes", maxMarkdownBytes)
	}
	fm, rest, ok := splitFrontmatter(body)
	if !ok {
		return req, fmt.Errorf("missing yaml frontmatter (expected leading '---' block)")
	}
	if err := yaml.Unmarshal(fm, &req); err != nil {
		return req, fmt.Errorf("invalid frontmatter yaml: %w", err)
	}
	req.Prompt = strings.TrimSpace(string(rest))
	return req, nil
}

// splitFrontmatter 分离前置 '---' yaml 区块与正文。容忍前导空白与 \r\n。
func splitFrontmatter(body []byte) (fm, rest []byte, ok bool) {
	b := bytes.TrimLeft(body, " \t\r\n")
	if !bytes.HasPrefix(b, []byte("---")) {
		return nil, nil, false
	}
	b = b[3:]
	idx := bytes.Index(b, []byte("\n---"))
	if idx < 0 {
		return nil, nil, false
	}
	fm = b[:idx]
	rest = b[idx+4:] // 跳过 "\n---"
	if i := bytes.IndexByte(rest, '\n'); i >= 0 { // 跳过结束 '---' 那行余下部分
		rest = rest[i+1:]
	} else {
		rest = nil
	}
	return fm, rest, true
}
```

**3) handler 分支** — `internal/httpapi/job_handler.go` `handleCreateJob` 顶部：
```go
var req job.JobRequest
ct := c.Req.Header.Get("Content-Type")
if strings.HasPrefix(ct, "text/markdown") || strings.HasPrefix(ct, "application/x-gofer-md") {
	raw, _ := io.ReadAll(c.Req.Body)
	parsed, err := parseMarkdownRequest(raw)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid markdown request", err.Error())
		return
	}
	if parsed.Agent == "exec" {
		writeError(c, http.StatusBadRequest, "markdown submit is for cli-agents", "use JSON + cmd for exec agents")
		return
	}
	req = parsed
} else if err := c.BindJSON(&req); err != nil {
	writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
	return
}
req.CallerID = callerFromCtx(c)
// ...后续 Submit + sync 分支不变
```
> 补 import `io`、`strings`。

**4) CLI `-f`** — `internal/commands/job.go`：`jobRunOpts` 加 `file string`；`-f/--file` 读文件 → 以 `Content-Type: text/markdown` POST。需 `internal/client` 加 `SubmitMarkdown(body []byte)`（或 `SubmitRaw(ct, body)`）。`-f` 与 `--prompt/--` 互斥校验。

### P1-b 验收
- 单测 `internal/httpapi`：`parseMarkdownRequest` 对标准 md（frontmatter+正文）解析出字段 + Prompt=正文；无 frontmatter→err；超限→err；`agent=exec`→400。
- 单测：`caller_id` 即便写进 frontmatter 也被服务端覆盖（不可伪造）。
- 真机：`gofer job run -f tmp/task.md`（cli-agent）提交成功，详情页 prompt=正文。
- 回归：JSON 提交（无 Content-Type/application/json）路径不变。

### 提交点（SR1202）
P1-a、P1-b 各自绿灯后分别 `git commit`（可两个 commit）；更新主纲进度勾选 + 「阶段实施结果」一行。
