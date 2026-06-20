# P3 — 控制台提交表单（实施计划）

> 主纲：[`2026-06-20-submit-dispatch-plan.md`](./2026-06-20-submit-dispatch-plan.md) · 设计：[`../../design/2026-06-20-submit-dispatch-design.md`](../../design/2026-06-20-submit-dispatch-design.md) §6.4。
> 纯前端 + 一个只读聚合接口。依赖 P1（sync）+ P2（labels）。决策 D5：首版覆盖 cli-agent + exec 主路径，labels 作可选高级项。

---

## P3-a 后端 `GET /v1/meta`（表单选项聚合）

### 落点
- `internal/httpapi/meta_handler.go`（新）。
- `internal/httpapi/server.go`：authed 组内加 `r.GET("/meta", s.handleMeta)`（紧邻 `/runners`，`server.go:194`）。

### 内容
只读、authed，聚合前端建表单需要的全部选项（一次取齐，避免前端多打几枪）：
```go
type metaResp struct {
	Projects []metaProject `json:"projects"`
	Agents   []metaAgent   `json:"agents"`
	Runners  []metaRunner  `json:"runners"` // name+type（local/peer-http/worker）
	Workers  []metaWorker  `json:"workers"` // id+labels+connected（复用 /v1/runners 数据源）
}
type metaProject struct {
	Key            string   `json:"key"`
	AllowedAgents  []string `json:"allowed_agents"`
	AllowedRunners []string `json:"allowed_runners"`
	DefaultAgent   string   `json:"default_agent,omitempty"`
}
type metaAgent  struct{ Key, Type string `json:"-"` ; /* Key string json:"key"; Type string json:"type" */ }
type metaRunner struct{ Name, Type string }
type metaWorker struct{ ID string `json:"id"`; Labels []string `json:"labels,omitempty"`; Connected bool `json:"connected"` }
```
> 数据源：projects/agents/runners 从 config 快照（同 `/v1/projects`、`/v1/agents`、`/v1/runners` 的既有取法）；workers 复用 `renderWorkerStatus`/`WorkerSnapshot` 判 connected。**不要新查 DB**。

### P3-a 验收
- 单测 `internal/httpapi`：`GET /v1/meta` 返回各分组非空数组；project 带 allowed_agents/allowed_runners；worker connected 态与 `/v1/runners` 一致。
- 401：无 token 拒绝（authed 组）。

---

## P3-b 前端提交表单

### 落点
- `web/src/api/types.ts`：加 `MetaResp` / `SubmitJobReq`。
- `web/src/api/client.ts`：加 `getMeta()`、`submitJob(req)`（`POST /v1/jobs`）。
- `web/src/views/NewJob.vue`（新）。
- `web/src/router.ts`：加 `/new` 路由。
- `web/src/App.vue`：顶栏/看板加「+ 新建 job」入口。

### 表单交互
- **项目** 下拉（来自 meta.projects）→ 选定后**联动**限定可选 agent / runner（用该 project 的 allowed_*）。
- **agent** 下拉；类型为 cli-agent → 显示 **prompt 文本域**（支持直接贴 markdown）；类型 exec → 显示 **command 输入**（空格分词或逐项，提交为 `cmd []string`）。
- **runner** 下拉；选到 worker 类型 → 显示二选一：
  - 「指定 worker」下拉（meta.workers 中 connected 的 id）；或
  - 「按标签自动」labels 输入（逗号分隔 → `worker_labels`）。（D5：默认折叠为高级项）
- **cwd**（默认 `.`）、**title**、**timeout**、**sync** 勾选（勾选则等终态，命中 202 提示“仍在后台，跳详情”）。
- 提交 → `submitJob(req)`：
  - 200 终态 / queued → `router.push('/jobs/'+res.id)`；
  - 202（`X-Gofer-Async`）→ 提示 + 跳详情页（详情页自有 SSE 续看）。
- 校验：cli-agent 必填 prompt；exec 必填 command；runner=worker 必须二选一（worker_id 或 labels），否则禁用提交并提示。
- **复用**：token（store/auth）、状态色板、**浅色主题**（本轮已落地）、错误提示风格（`writeError` 的 message）。

### client/types 片段 — `web/src/api/client.ts`
```ts
export function getMeta(): Promise<MetaResp> { return request<MetaResp>('/v1/meta') }
export function submitJob(req: SubmitJobReq): Promise<Job> {
  return request<Job>('/v1/jobs', { method: 'POST', body: JSON.stringify(req) })
}
```
> 复用既有 `request<T>()`（已带 token/401 处理）。`Job` 类型沿用 `api/types.ts` 现有 JobResult 映射。

### P3-b 验收
- `pnpm -C web build` 绿（vue-tsc 类型 + 构建）。
- 真机（agent-browser，深/浅两态）：打开「+ 新建 job」→ 选 project/agent/runner → 提交 exec `echo hi`（勾 sync）→ 跳详情页见 done+exit_code；提交 cli-agent prompt 同样成功。
- runner=worker：选「按标签自动」labels=gpu 提交，详情页 worker_id 落到 gpu worker；无连接 worker 时表单内 503 错误清晰展示。
- 回归：未登录跳 `/access`；现有看板/详情不受影响。

### 提交点
P3-a、P3-b 各绿灯分别 `git commit`；最后 `make web`（拷 dist 进 embed）+ `make build` 烘进二进制（按既有发布流程，构建产物不入仓）。更新主纲进度全勾 + 出**完成报告**（SR1430 完成报告口径）。
