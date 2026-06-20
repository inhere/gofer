# P1 — 模型地基 + 渲染命令(E15) + 结构化结果(E6)（实施计划）

> 主纲：[`2026-06-20-job-outcomes-audit-plan.md`](./2026-06-20-job-outcomes-audit-plan.md) · 设计：[`../../design/2026-06-20-job-outcomes-audit-design.md`](../../design/2026-06-20-job-outcomes-audit-design.md) §6.1/§6.2/§6.3/§7。
> 地基阶段：建好"捕获 → 入库 → 详情暴露"的骨架，先落 E15（最便宜）+ E6，验证整条通路。**仅 local**（远端 P4）。

---

## P1-a schema 加 4 列 + 字段贯通

### 落点
- `internal/jobstore/store.go`：`migrate()` 加 4 个 `ALTER TABLE jobs ADD COLUMN`。
- `internal/jobstore/jobs.go`：`JobRecord` 加 4 字段；`selectCols`、`scanJob`、`UpsertJob` 的 INSERT 列/VALUES/ON CONFLICT SET 同步。
- `internal/job/model.go`：`JobResult` 加 4 字段。
- `internal/job/service.go`：`toRecord` / `fromRecord` 映射 4 字段。

### 步骤

**1) migrate 加列** — `internal/jobstore/store.go` `migrate()`（紧跟现有 `request_id` add 之后）：
```go
if err := add("rendered_command", "rendered_command TEXT"); err != nil { return err } // E15
if err := add("result_json", "result_json TEXT"); err != nil { return err }           // E6
if err := add("artifacts_json", "artifacts_json TEXT"); err != nil { return err }      // E1(P2)
if err := add("diff_summary", "diff_summary TEXT"); err != nil { return err }          // E12(P3)
```
> 同时把这 4 列加进 `schemaStmts` 的 `CREATE TABLE jobs` DDL（新库一次建全；旧库走 migrate）。两处保持一致。

**2) `JobRecord` 4 字段** — `internal/jobstore/jobs.go`（`RequestID` 之后）：
```go
RenderedCommand string // 渲染后实际 argv {command,args,env_keys} JSON（E15）
ResultJSON      string // <result_dir>/result.json 内容（E6）
ArtifactsJSON   string // [{name,size,mtime}] 产物清单（E1）
DiffSummary     string // git diff --stat 截断摘要（E12）
```

**3) selectCols / scanJob / UpsertJob** — `internal/jobstore/jobs.go`：
- `selectCols`（:70）追加 `COALESCE(rendered_command,''), COALESCE(result_json,''), COALESCE(artifacts_json,''), COALESCE(diff_summary,'')`。
- `scanJob` 的 `sc.Scan(...)` 追加 4 个 `&rec.RenderedCommand` 等（顺序与 selectCols 一致）。
- `UpsertJob`（:104）INSERT 列名 + `VALUES (?...)` 占位 + `ON CONFLICT DO UPDATE SET` 各加 4 项（`rendered_command=excluded.rendered_command` 等）；参数列表 append 4 值。

**4) `JobResult` 4 字段** — `internal/job/model.go`（`RequestJSON` 附近，`json` tag omitempty）：
```go
RenderedCommand string `json:"rendered_command,omitempty"`
ResultJSON      string `json:"result_json,omitempty"`   // 透传原文(已是 JSON 字符串)
ArtifactsJSON   string `json:"-"`                        // 不直接回 get_job；走 /artifacts(P2)
DiffSummary     string `json:"diff_summary,omitempty"`
```
> `ResultJSON` 是"JSON 字符串"，get_job 回它时前端 `JSON.parse`；或 handler 端 `json.RawMessage` 包装直出对象（P1-d 定）。`ArtifactsJSON` 不进 get_job（清单走专门端点，避免详情响应膨胀）。

**5) toRecord / fromRecord** — `internal/job/service.go`：两函数各加 4 行映射（`RenderedCommand: r.RenderedCommand` …）。

### P1-a 验收
- 单测 `internal/jobstore`：旧库（仅含 C1+C2+C5 列）`Open` 后 `PRAGMA table_info(jobs)` 含 4 新列；`UpsertJob` 写入含新字段的 record 后 `GetJob` 读回一致；新库一次建全。
- 回归：现有 jobstore 测试全绿（selectCols/scan 顺序对齐）。

---

## P1-b captureOutcomes 钩子

### 落点
- `internal/job/service.go`：`execute()` 内 `res := run.Run(ctx, req)` 之后、`finish(...)` 之前插一步；新增 `captureOutcomes` + 各 capture 子函数（本阶段实现 E15/E6，留 E1 P2 / E12 P3 的接入点）。

### 步骤
`execute()`（service.go:387 附近）：
```go
res := run.Run(ctx, req)
s.captureOutcomes(entry, req)            // 新增：best-effort 采集产出（不影响终态）
status, code, runErr := classify(ctx, res)
s.finish(entry, req.JobID, status, code, runErr)
```
新增（`internal/job/outcomes.go` 新文件，保持 service.go 精简）：
```go
// captureOutcomes 在 job 跑完(终态前)采集产出，写入 entry.result 对应字段；
// best-effort：任何失败仅记 warning，绝不改变 job 终态（产出是附加审计信息）。
// 仅 local 有数据：远端(worker/peer)的 req.Command 为空、result_dir 在执行侧，P4 经回传补。
func (s *Service) captureOutcomes(entry *jobEntry, req runner.Request) {
	entry.mu.Lock()
	resultDir := entry.result.ResultDir
	cwd := entry.result.Cwd
	entry.mu.Unlock()

	rendered := renderedCommandJSON(req)          // E15
	result := readResultJSON(resultDir)           // E6
	// artifacts := scanArtifacts(resultDir)      // E1  → P2
	// diff := captureDiff(ctx, cwd, resultDir)   // E12 → P3
	_ = cwd

	entry.mu.Lock()
	if rendered != "" { entry.result.RenderedCommand = rendered }
	if result != "" { entry.result.ResultJSON = result }
	entry.mu.Unlock()
}
```
> 设字段在 `entry.result` 上、在 `finish` 之前 —— `finish` 的 `snap := entry.result` 会带上它们一并 `persist`，无需改 finish 签名。

### P1-b 验收
- 单测 `internal/job`：构造一个 local exec job（echo），跑完后 `Get(id)` 的 `RenderedCommand` 非空；captureOutcomes 内部 panic/err 被吞、job 仍 done（注入一个会失败的 capture 验证 best-effort）。

---

## P1-c E15 渲染命令

### 落点 `internal/job/outcomes.go`
```go
// renderedCommandJSON 把本次实际执行的命令序列化为审计 JSON：
// {command, args, env_keys}。env 只存 KEY 名、不存值（防 secret 入库，SR403/SR805）。
// 远端 runner 的 req.Command 为空 → 返回 ""（P4 由执行侧回传）。
func renderedCommandJSON(req runner.Request) string {
	if req.Command == "" { return "" }
	keys := make([]string, 0, len(req.Env))
	for k := range req.Env { keys = append(keys, k) }
	sort.Strings(keys)
	b, _ := json.Marshal(struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		EnvKeys []string `json:"env_keys,omitempty"`
	}{req.Command, req.Args, keys})
	return string(b)
}
```
- get_job 已经回 `JobResult.RenderedCommand`（P1-a 的 model 字段）。无需新端点。
- **前端**（`web/src/views/JobDetail.vue`）：在 interactions 与 LogTape 之间加「产出与审计」section 的**渲染命令**子块——展示 `command` + `args`（mono，逐 arg 一行/空格连接）+ 复制按钮；`env_keys` 折叠展示（仅 key）。`web/src/api/types.ts` `Job` 加 `rendered_command?: string`（前端 `JSON.parse`）。

### P1-c 验收
- 单测：`renderedCommandJSON` 对 cli-agent（command=codex,args=[exec,"…"]）与 exec（command=go,args=[version]）产出正确 JSON；env 仅含 key 名、无值。
- 真机：local job 详情页见"渲染命令"，复制可用。

---

## P1-d E6 结构化结果

### 落点 `internal/job/outcomes.go`
```go
const maxResultJSONBytes = 256 * 1024
// readResultJSON 读 <result_dir>/result.json（agent/wrapper 经 {{result_dir}} 写入）：
// 不存在→""；超上限或非法 JSON→记 warning 返 ""（不污染 DB）。
func readResultJSON(resultDir string) string {
	if resultDir == "" { return "" }
	p := filepath.Join(resultDir, "result.json")
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() || fi.Size() > maxResultJSONBytes { return "" }
	b, err := os.ReadFile(p)
	if err != nil || !json.Valid(b) { return "" }
	return string(b)
}
```
- get_job 回 `result_json`。**handler 层**（`handleGetJob`）把 `ResultJSON` 串包成 `json.RawMessage` 直出对象（前端免二次 parse），或保持字符串由前端 parse —— 二选一统一（建议 RawMessage 直出，键名 `result`）。
- **前端**：「产出与审计」section 的**结构化结果**子块——pretty-print（`<pre>`，mono，明暗主题 token）。`getJob` 类型加 `result_json`。

### P1-d 验收
- 单测 `internal/httpapi`：job 的 result_dir 放一个 `result.json` → `GET /v1/jobs/{id}` 含其内容；超 256KB / 非法 JSON → 不含（不报错）。
- 真机：写 `result.json` 的 job 详情页见结构化结果面板。

### 提交点（SR1202）
P1-a（schema）/ P1-b+c+d（钩子+E15+E6，共改 outcomes.go/JobDetail）按收敛度分 1–2 个 commit；更新主纲进度 + 实施结果一行。
