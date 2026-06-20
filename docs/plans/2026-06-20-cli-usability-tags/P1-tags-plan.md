# P1 — E5 tags 地基（实施计划）

> 主纲：[`2026-06-20-cli-usability-tags-plan.md`](./2026-06-20-cli-usability-tags-plan.md) · 设计 §5.1/§6/§7。
> job 提交带 `tags` 入库 + 列表/看板按 **tag/agent/runner/since** 检索。**仅 local**，纯加列 + 查询面扩展。

---

## P1-a `tags_json` 加列 + 5 处贯通 + JobRequest/JobResult.Tags

### 落点
- `internal/jobstore/store.go`：`schemaStmts` 的 `CREATE TABLE jobs` 加 `tags_json TEXT`；`migrate()` 紧跟 `source` add 之后加 `add("tags_json","tags_json TEXT")`（两处一致）。
- `internal/jobstore/jobs.go`：`JobRecord` 加 `TagsJSON string`；`selectCols` 追加 `COALESCE(tags_json,'')`；`scanJob` 末尾追加 `&r.TagsJSON`（顺序与 selectCols 一致）；`UpsertJob` 的 INSERT 列名 + `VALUES(?)` 占位 + `ON CONFLICT DO UPDATE SET tags_json=excluded.tags_json` + 参数 append `rec.TagsJSON`。
- `internal/job/model.go`：`JobResult` 加 `Tags []string json:"tags,omitempty"`；`JobRequest` 加 `Tags []string json:"tags,omitempty" yaml:"tags,omitempty"`。
- `internal/job/service.go`：`Submit` 把 `req.Tags` 写入 `entry.result.Tags`；`toRecord` 把 `r.Tags` marshal → `TagsJSON`；`fromRecord` 把 `rec.TagsJSON` unmarshal → `Tags`。

### 步骤
**1) schema**（store.go，模板同 `source` 列）：DDL 加列 + `migrate()`：
```go
if err := add("tags_json", "tags_json TEXT"); err != nil { return err } // E5
```
**2) JobRecord + 3 处**（jobs.go）：`TagsJSON string`；selectCols 末加 `, COALESCE(tags_json,'')`；scanJob 末加 `&r.TagsJSON`；UpsertJob 三处（列/占位/ON CONFLICT SET）+ 参数。
**3) model**：`JobResult.Tags []string`、`JobRequest.Tags []string`。
**4) service 映射**（新增小 helper，放 service.go 或 model.go）：
```go
// tags 与 tags_json 互转（best-effort：marshal 失败存 ""；空/非法 unmarshal 返 nil）
func marshalTags(tags []string) string {
    if len(tags) == 0 { return "" }
    b, err := json.Marshal(tags); if err != nil { return "" }
    return string(b)
}
func unmarshalTags(s string) []string {
    if s == "" { return nil }
    var t []string; if json.Unmarshal([]byte(s), &t) != nil { return nil }
    return t
}
```
- `toRecord`：`TagsJSON: marshalTags(r.Tags)`；`fromRecord`：`Tags: unmarshalTags(rec.TagsJSON)`；`Submit` 内 `entry.result.Tags = req.Tags`（在构造 `JobResult{...}` 处加 `Tags: req.Tags`）。

### P1-a 验收
- 单测 `internal/jobstore`：旧库 Open 后 `PRAGMA table_info(jobs)` 含 `tags_json`；`UpsertJob` 写含 `TagsJSON` 的 record → `GetJob` 读回一致；新库一次建全。
- 单测 `internal/job`：提交带 `Tags:["a","b"]` 的 job → `Get(id).Tags` == `["a","b"]`；无 tags → `Tags` 为 nil（API omitempty 不出现）。
- 回归：现有 jobstore/job 测试全绿（selectCols/scan 顺序对齐）。

---

## P1-b 检索维度后端（tag/agent/runner/since）

### 落点
- `internal/jobstore/jobs.go`：`ListQuery` 加 `Tag/Agent/Runner string`（`Since int64` 已存在，复用）；`ListJobs` 的 WHERE 动态拼接加 4 条。
- `internal/job/list.go`：`ListOpts` 加 `Tag/Agent/Runner string`、`Since int64`；映射到 `ListQuery`；**in-memory overlay 过滤同步加这 4 维**（关键：live job 未落终态时也要被新过滤命中/排除）。
- `internal/httpapi/job_handler.go`：`handleListJobs` 加 query 映射 `tag/agent/runner/since`。

### 步骤
**1) jobstore**（`ListJobs` WHERE，jobs.go:211 附近，走预编译占位符）：
```go
if q.Tag != ""    { where = append(where, "tags_json LIKE ?"); args = append(args, "%\""+q.Tag+"\"%") }
if q.Agent != ""  { where = append(where, "agent = ?");        args = append(args, q.Agent) }
if q.Runner != "" { where = append(where, "runner = ?");       args = append(args, q.Runner) }
// Since 已有：started_at >= ?
```
> tag 用 `LIKE '%"<tag>"%'`（匹配 JSON 数组里的 `"tag"` 元素，含引号避免子串误命中，D2 子串近似可接受）。
**2) service ListOpts → ListQuery**（list.go）：加字段 + 透传；**in-memory overlay**（合并 live 条目的过滤函数）同步按 tag（`slices.Contains(entry.Tags, opts.Tag)`）/agent/runner/since 过滤——保证 DB 与内存两路一致。
**3) handler**（job_handler.go:121）：
```go
job.ListOpts{
    Project: c.Query("project"), Status: c.Query("status"), Caller: c.Query("caller"),
    Tag: c.Query("tag"), Agent: c.Query("agent"), Runner: c.Query("runner"),
    Since: parseInt64(c.Query("since")), Limit: limit,
}
```
（`since` 非数值 → 0 → 不过滤，仿现有 limit 容错。）

### P1-b 验收
- 单测 `internal/jobstore`：建多 job（不同 tag/agent/runner/started_at）→ `ListJobs` 按各维过滤命中正确；`tags_json LIKE` 不误命中（`"ab"` 不命中查 `a`）。
- 单测 `internal/job`：含一个**未终态的内存 live job** + 已持久化 job，`ListJobs(ListOpts{Tag/Agent/Runner/Since})` 对两路都正确过滤。
- 单测 `internal/httpapi`：`GET /v1/jobs?tag=x&agent=y&runner=z&since=N` 映射正确；省略参数 → 行为同旧（回归）。

---

## P1-c Web Board 过滤器 + tag 徽标

### 落点
- `web/src/api/types.ts`：`ListJobsOpts` 加 `tag?/agent?/runner?/since?/caller?`；`Job` 加 `tags?: string[]`。
- `web/src/api/client.ts`：`listJobs` 把新参数加进 `URLSearchParams`。
- `web/src/views/Board.vue`：过滤器加 tag（输入框）/agent（下拉，取自已知 agents 或自由输入）/runner（下拉）/since（时间/快捷）+ 补 caller 输入；列表行渲染 `tags` 徽标。

### 步骤
- types/client：加字段与 query 拼装（仿现有 status/project）。
- Board.vue：`ref` 新增 `tagFilter/agentFilter/runnerFilter/sinceFilter/callerFilter`，并入 `listJobs({...})` 调用；过滤器区按现有 UI 风格加控件；行模板加 tag chips（无 tags 不渲染）。

### P1-c 验收
- `pnpm -C web build` 绿（含 `vue-tsc`）。
- 手测/快照：Board 选 tag/agent/runner/since/caller → 请求带对应 query；带 tags 的 job 行显示徽标。

### 提交点（SR1202）
P1-a（schema 贯通）/ P1-b（检索后端）/ P1-c（Web）各绿灯分别 `git commit`；更新主纲进度 + 实施结果一行。
