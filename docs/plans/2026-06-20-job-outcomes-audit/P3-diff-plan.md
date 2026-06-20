# P3 — 改了什么 / diff 快照（E12）（实施计划）

> 主纲：[`2026-06-20-job-outcomes-audit-plan.md`](./2026-06-20-job-outcomes-audit-plan.md) · 设计 §6.5。
> job 终态时对其 cwd（git 仓）`git diff` → `--stat` 摘要入库 + 全量写文件 + `/diff` API + Web。**仅 local**（远端 P4）。依赖 P1。

---

## P3-a git diff 封装 + 捕获接入 + 开关

### 落点
- `internal/job/gitdiff.go`（新）：探仓 + diff 封装。
- `internal/job/outcomes.go`：`captureOutcomes` 接 `captureDiff`（受项目开关控制）。
- `internal/config/model.go`：`ProjectConfig` 加 `CaptureDiff *bool`（nil=默认按是否 git 仓决定）。

### 步骤

**1) git 封装** — `internal/job/gitdiff.go`：
```go
const (
	diffTimeout      = 5 * time.Second
	diffSummaryCap   = 32 * 1024  // --stat 摘要入库上限
	diffFullCap      = 4 * 1024 * 1024 // 全量写文件上限（超则截断标注）
)
// captureDiff 在 cwd 是 git 工作树时，采集"未提交改动"(工作树 vs HEAD/index)：
// 全量写 <result_dir>/changes.diff，返回 --stat 摘要(截断)。非 git/超时/出错→""。
func captureDiff(cwd, resultDir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	if !isGitWorkTree(ctx, cwd) { return "" }
	// 全量：git -C cwd diff（含 untracked 视 D4 决策：v1 仅 tracked 改动；untracked 留 v2）
	full := runGit(ctx, cwd, diffFullCap, "diff")
	if len(full) > 0 { _ = os.WriteFile(filepath.Join(resultDir, "changes.diff"), full, 0o644) }
	stat := runGit(ctx, cwd, diffSummaryCap, "diff", "--stat")
	return string(stat)
}
func isGitWorkTree(ctx context.Context, cwd string) bool {
	out := runGit(ctx, cwd, 256, "rev-parse", "--is-inside-work-tree")
	return strings.TrimSpace(string(out)) == "true"
}
// runGit 跑 git 子进程(Dir=cwd)，stdout 上限 cap 截断；err/超时返回已读部分(或 nil)。
func runGit(ctx context.Context, cwd string, cap int, args ...string) []byte { /* exec.CommandContext("git",args...); Dir=cwd; 限读 cap */ }
```
> 子进程同 `runner/local` 的 `exec.CommandContext(...,Dir=cwd)` 口径。`git` 不在 PATH → `isGitWorkTree` 返 false，整体降级为""。

**2) 接入 + 开关** — `internal/job/outcomes.go` `captureOutcomes`：
```go
if s.shouldCaptureDiff(entry) {            // 项目 CaptureDiff 显式 false → 关；nil → 默认(是 git 仓即开)
	if summary := captureDiff(cwd, resultDir); summary != "" {
		entry.mu.Lock(); entry.result.DiffSummary = summary; entry.mu.Unlock()
	}
}
```
- `ProjectConfig.CaptureDiff *bool`（yaml `capture_diff`）；`shouldCaptureDiff` 取项目配置：显式 false→跳过；否则交给 `captureDiff` 自身的 is-git 判定（非 git 自然返""）。

### P3-a 验收
- 单测 `internal/job`：临时 git 仓改一个文件 → `captureDiff` 返非空 `--stat`、`changes.diff` 落盘含改动；非 git 目录→""；`git` 不可用→""(不 panic)。
- 单测：项目 `capture_diff:false` → 不产生 diff。
- best-effort：`captureDiff` 内部失败不影响 job 终态。

---

## P3-b /diff 接口 + Web 面板

### 落点
- `internal/httpapi/job_handler.go`（或 `diff_handler.go`）：`handleGetDiff`。
- `internal/httpapi/server.go`：路由。
- get_job 已回 `diff_summary`（P1 字段）；全量走 `/diff?full=1`。

```go
func (s *Server) handleGetDiff(c *rux.Context) {
	res, ok := s.jobs.Get(c.Param("id"))
	if !ok { writeError(c, 404, "unknown job", …); return }
	if c.Query("full") == "1" {
		p := filepath.Join(res.ResultDir, "changes.diff")
		if _, err := os.Stat(p); err != nil { writeError(c, 404, "no diff", …); return }
		http.ServeFile(c.Resp, c.Req, p)   // text/plain
		return
	}
	c.JSON(200, map[string]string{"summary": res.DiffSummary})
}
```
`server.go` 加 `r.GET("/jobs/{id}/diff", s.handleGetDiff)`。

**前端**（`JobDetail.vue` diff 子块）：展示 `diff_summary`（`<pre>` mono），有摘要时给"查看完整 diff"链接（`/v1/jobs/{id}/diff?full=1`，下载/新窗）。`Job` 类型加 `diff_summary?`。

### P3-b 验收
- 单测 httpapi：有 diff 的 job `GET /diff` 返摘要、`?full=1` 返 changes.diff 内容；无 diff→摘要空 / full 404。
- 真机：local 改动 git 仓的 job 详情页见"改了什么"摘要 + 完整 diff 链接。

### 提交点
P3-a / P3-b 各绿灯分别 `git commit`；更新主纲进度 + 实施结果一行。

> **范围注记**（D4）：v1 仅捕获 tracked 文件的未提交改动（`git diff`）；untracked 新文件、agent 自行 commit 的改动留 v2（"job 开始打基线 ref"）。文档/详情面板注明"未提交改动"语义，避免误读为"全部改动"。
