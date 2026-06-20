# P2 — 产物回取（E1）（实施计划）

> 主纲：[`2026-06-20-job-outcomes-audit-plan.md`](./2026-06-20-job-outcomes-audit-plan.md) · 设计 §6.4。
> 约定 **`<result_dir>/artifacts/`** 为产物目录；清单入库、列举 + 下载 API + Web。**仅 local**（远端 P4）。依赖 P1。

---

## P2-a 产物清单捕获 + 列举接口

### 落点
- `internal/job/outcomes.go`：`scanArtifacts(resultDir)` + 接入 `captureOutcomes`（写 `entry.result.ArtifactsJSON`）。
- `internal/httpapi/artifact_handler.go`（新）+ `server.go` 路由。

### 步骤

**1) 扫描** — `internal/job/outcomes.go`：
```go
type ArtifactItem struct {
	Name  string `json:"name"`   // artifacts/ 下相对路径（可含子目录）
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`  // unix 秒
}
const maxArtifacts = 500
// scanArtifacts 列举 <result_dir>/artifacts/ 下的常规文件（递归，相对路径），
// 仅元数据入库（文件留盘）。目录不存在→nil。超 maxArtifacts 截断并标注。
func scanArtifacts(resultDir string) []ArtifactItem { /* filepath.WalkDir，跳目录/软链，cap 数量 */ }
```
`captureOutcomes` 内：
```go
if items := scanArtifacts(resultDir); len(items) > 0 {
	if b, err := json.Marshal(items); err == nil { entry.result.ArtifactsJSON = string(b) }
}
```

**2) 列举接口** — `GET /v1/jobs/{id}/artifacts`（`artifact_handler.go`，仿 `serveLog` 取 job）：
```go
func (s *Server) handleListArtifacts(c *rux.Context) {
	res, ok := s.jobs.Get(c.Param("id"))
	if !ok { writeError(c, 404, "unknown job", …); return }
	// 优先用库里 ArtifactsJSON；为空则实时扫 <res.ResultDir>/artifacts/（兜底）
	c.JSON(200, map[string]any{"artifacts": itemsFor(res)})
}
```
`server.go` authed 组加 `r.GET("/jobs/{id}/artifacts", s.handleListArtifacts)`。

### P2-a 验收
- 单测：job result_dir 下建 `artifacts/a.txt`+`artifacts/sub/b.bin` → `scanArtifacts` 返 2 项（相对名 `a.txt`/`sub/b.bin`、size 正确）；无目录→空。
- 单测 httpapi：`GET /v1/jobs/{id}/artifacts` 返回清单；未知 id→404。

---

## P2-b 产物下载（路径安全）

### 落点 `internal/httpapi/artifact_handler.go`
```go
func (s *Server) handleDownloadArtifact(c *rux.Context) {
	res, ok := s.jobs.Get(c.Param("id"))
	if !ok { writeError(c, 404, "unknown job", …); return }
	base := filepath.Join(res.ResultDir, "artifacts")
	full, err := safeJoinUnder(base, c.Param("name"))   // ← 关键安全点
	if err != nil { writeError(c, 400, "invalid artifact path", err.Error()); return }
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() { writeError(c, 404, "no such artifact", …); return }
	c.SetHeader("Content-Disposition", "attachment; filename="+strconv.Quote(filepath.Base(full)))
	http.ServeFile(c.Resp, c.Req, full)
}

// safeJoinUnder 把 name 安全拼到 base 下，拒绝逃逸：Clean + 必须以 base 为前缀，
// 且 EvalSymlinks 后仍在 base 内（防软链逃逸）。借鉴 project.SafeJoin(path.go:21) 的口径。
func safeJoinUnder(base, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) { return "", errors.New("bad name") }
	full := filepath.Join(base, filepath.Clean("/"+name)) // 先把 name 锚到根再 Clean，去 ..
	rel, err := filepath.Rel(base, full)
	if err != nil || strings.HasPrefix(rel, "..") { return "", errors.New("escapes artifacts dir") }
	if real, err := filepath.EvalSymlinks(full); err == nil {
		if r2, _ := filepath.Rel(base, real); strings.HasPrefix(r2, "..") { return "", errors.New("symlink escape") }
	}
	return full, nil
}
```
`server.go` 加 `r.GET("/jobs/{id}/artifacts/{name}", s.handleDownloadArtifact)`（`{name}` 含 `/` 子路径需确认 rux 通配匹配；若 rux 不支持单段含 `/`，改用 `?path=` query 承载相对路径）。

**前端**（`JobDetail.vue` 产物子块）：清单列表，每项 `name`(title)+`size`(mono)+下载链接 `(/v1/...)/artifacts/<name>`（带 token：用 `client` 下载或在 URL 走同源代理；P3 注意鉴权头——下载用 fetch+blob 或 `?token=` 视 client 现状定）。`api/client.ts` 加 `listArtifacts(id)`。

### P2-b 验收
- 单测 `safeJoinUnder`：`a.txt`/`sub/b`合法；`../x`/`/etc/passwd`/`..%2f`→拒；构造软链逃逸→拒。
- 单测 httpapi：下载存在产物返 200+内容+Content-Disposition；`..` 名→400；不存在→404。
- 真机：详情页产物列表点下载得到文件。

---

## P2-c MCP 读 tool（D7）

### 落点 `internal/mcpserver/server.go`
新增 2 个 tool（仿现有 `bridge_tail_log` 形）：
- `bridge_get_artifacts`：入参 `{job_id}` → 返回清单（name/size/mtime）。
- `bridge_get_result`：入参 `{job_id}` → 返回结构化结果（result_json）。
复用 `job.Service.Get` + P2-a/P1-d 的取数逻辑（不重复实现）。更新 `server_test.go` 工具清单断言（现 8 → 10）。

### P2-c 验收
- 单测 `internal/mcpserver`：工具注册含两新 tool；`bridge_get_artifacts`/`bridge_get_result` 对有产物/结果的 job 返回正确，对无的返回空。

### 提交点
P2-a / P2-b / P2-c 各绿灯分别 `git commit`；更新主纲进度 + 实施结果一行。
