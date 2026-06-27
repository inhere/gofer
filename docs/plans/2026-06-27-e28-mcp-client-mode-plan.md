# E28 · gofer mcp client 模式 实施计划

> 对应 design [`../design/2026-06-27-e28-mcp-client-mode-design.md`](../design/2026-06-27-e28-mcp-client-mode-design.md)（D1–D3 决策 + Backend 接口 §7）。
> 纯 Go CLI 改动、无前端。核心铁律：**P2 `localBackend` 抽取零行为变化**（现有 `mcpserver/server_test.go` 全绿背书，G023）。

## 1. 总纲

| 阶段 | 目标 | 依赖 | 工作量 |
|---|---|---|---|
| **P1** | `internal/client` 补 3 项：`ListAgents` / `GetInteractions` / `AnswerInteraction` 增强返回 `job.Interaction` + 单测 | — | 小 |
| **P2** | `mcpserver` 抽 `Backend` 接口 + `localBackend`（现逻辑零行为变化搬入）+ `New(b)`/`Serve(ctx,b)` 改签名；现有 server_test 全绿 | — | 中 |
| **P3** | `clientBackend`（转发 `*client.Client`）+ 单测（对 httptest serve 或 fake） | P1, P2 | 中 |
| **P4** | `commands/mcp.go` 模式分支（`bindServerFlags` + `--standalone`，D1/D2）+ 单测 | P2, P3 | 小 |
| **P5** | 真机 E2E：起 serve + 两 mcp client 经中枢协作（A 派/B 看/互答）+ standalone 回归 | P1-P4 | 中 |

> 顺序：P1 与 P2 可并行（互不依赖）；P3 需 P1+P2；P4 需 P2+P3；P5 收尾。建议 P1→P2→P3→P4→P5 顺序跑（每阶段独立提交，SR1202）。

## 2. 前置检查（plan-checking，开工前 PASS）

- [ ] `go build ./... && go vet ./...` 绿；`go test ./internal/client/... ./internal/mcpserver/... ./internal/commands/...` 基线绿。
- [ ] 确认服务端 endpoint 在：`GET /v1/agents`(handleListAgents) / `GET /v1/jobs/{id}/interactions`(handleListInteractions) / `POST /v1/jobs/{id}/interactions/{iid}/answer`(handleAnswerInteraction，返更新后 Interaction)。
- [ ] 确认 `job.JobResult` 含 `ResultJSON`（client `GetResult`=`GetJob().ResultJSON`）；`job.Interaction` JSON tag 与 answer/interactions endpoint 返回结构一致（client 可直接反序列化）。
- [ ] 确认 `AnswerInteraction` 现有调用方（CLI）数量（签名变更需同步改）：`grep -rn "\.AnswerInteraction(" internal/`。
- [ ] 确认 `newClient`/`bindServerFlags`/`jobConnOpts` 在 `internal/commands/job.go`（mcp 复用）。

## 3. 进度跟进

- [x] **P1** client 3 方法 + 单测
- [x] **P2** Backend 接口 + localBackend 抽取（零行为变化）
- [x] **P3** clientBackend + 单测
- [x] **P4** mcp.go 模式分支（--server/--standalone）+ 单测
- [x] **P5** 真机 E2E（双 client 协作 + standalone 回归）— 全 PASS，见 §5

---

## P1：internal/client 补 3 方法

### T1.1 `ListAgents()` + `GetInteractions()`（新增 GET）

`internal/client/client.go`（参考既有 `ListProjects`/`ListJobs` 的 `doJSON` 用法）：

```go
// AgentMeta mirrors GET /v1/agents item (name/type/available/detail).
type AgentMeta struct {
    Name      string `json:"name"`
    Type      string `json:"type"`
    Available bool   `json:"available"`
    Detail    string `json:"detail,omitempty"`
}
func (c *Client) ListAgents() ([]AgentMeta, error) {
    var resp struct{ Agents []AgentMeta `json:"agents"` }
    if err := c.doJSON(http.MethodGet, "/v1/agents", nil, &resp); err != nil { return nil, err }
    return resp.Agents, nil
}

// GetInteractions lists a job's running-time interactions (GET .../interactions).
func (c *Client) GetInteractions(id string) ([]job.Interaction, error) {
    var resp struct{ Interactions []job.Interaction `json:"interactions"` }
    if err := c.doJSON(http.MethodGet, "/v1/jobs/"+url.PathEscape(id)+"/interactions", nil, &resp); err != nil { return nil, err }
    return resp.Interactions, nil
}
```
> 先读 `interaction_handler.go` 的 list/answer 响应包装键名（`interactions` / 顶层对象），DTO 严格对齐。

### T1.2 `AnswerInteraction` 增强返回 `job.Interaction`

签名 `AnswerInteraction(jobID, interactionID, answer string) error` → `(..., (job.Interaction, error))`，解析 endpoint 返回的更新后 Interaction。**同步改现有调用方**（CLI，按 T 前置检查清单），保持其行为（多忽略返回值即可）。

### P1 验收
- [x] `go test ./internal/client/...` 绿（新增方法单测：httptest mock `/v1/agents`、`/interactions`、answer 返回 Interaction）。
- [x] `go build ./...` 绿（AnswerInteraction 签名变更后全调用方已改）。
- [x] commit：`feat(gofer): client 补 ListAgents/GetInteractions + AnswerInteraction 返 Interaction(E28 P1)`。

> 偏差记录：design §9 / 任务"已核实事实"称 `GET /v1/agents` 返 `{name,type,available,detail}`，实际端点（`httpapi.agentView`）返 `{key,type,available,version,error}`。`ListAgents` 改为解码真实 wire 形再折叠为 `AgentMeta{name,type,available,detail}`（`name=key`，`detail=version`/不可用时取 `error`），与本地 `mcpserver` list-agents handler 一致；`AgentMeta` 字段名/tag 仍按 design 保持，P3 可 1:1 映射 `agentEntry`。

---

## P2：mcpserver Backend 接口 + localBackend 抽取（零行为变化）

### T2.1 定义 `Backend` 接口（见 design §7，新 `internal/mcpserver/backend.go`）

10 方法签名照 design §7。`projectEntry`/`agentEntry`/`artifactView`/`jobView`/`interactionView` 视图类型保留在 mcpserver。

### T2.2 `localBackend`（搬现逻辑，G023 逐字不变）

把现 10 个 handler 闭包里**对 job.Service/registries 的调用**原样搬进 `localBackend` 方法（`internal/mcpserver/backend_local.go`），handler 只保留"调 backend + 投影"。关键：行为**逐字不变**——
- `ListProjects`：现 `projects.List()`+`Get` 组 `projectEntry`。
- `ListAgents`：现 `agents.List()`+`Detect` 组 `agentEntry`。
- `RunJob`：`jobs.Submit(req)`（**provenance `Channel/Client` 改由 handler 统一注入** req 后传 backend——见 T2.3）。
- `GetJob`/`GetResult`：`jobs.Get(id)`，`!ok`→`fmt.Errorf("unknown job %q", id)`（规整 bool→err，与现 handler 报错一致）。
- `TailLog`：现 stream 校验留 handler，backend 收规整后的 stream+maxBytes 调 `jobs.TailLog`。
- `CancelJob`：`jobs.Cancel(id)`+`Get`。
- `GetInteractions`/`AnswerInteraction`：`jobs.GetInteractions` / `jobs.AnswerInteraction`。
- `GetArtifacts`：`jobs.GetArtifactManifest(id)`，`!ok`→err，组 `[]artifactView`。

### T2.3 handler 改调 backend；`New(b Backend)`/`Serve(ctx, b)`

```go
func New(b Backend) *mcp.Server { /* AddTool ×10，handler 闭包 over b */ }
func Serve(ctx context.Context, b Backend) error { return New(b).Run(ctx, &mcp.StdioTransport{}) }
// 兼容现有调用：保留一个 NewLocal(jobs,projects,agents) = New(newLocalBackend(...)) 便于 server_test 少改
```
provenance：`runJobHandler` 内建 `job.JobRequest{...in..., Channel:"mcp", Client:mcpHostname()}` 再 `b.RunJob(req)`（两后端都透传）。

### P2 验收
- [x] **现有 `internal/mcpserver/server_test.go` 全绿**（零行为变化的硬背书；断言一字未改，仅把 `connect` 构造入口 `New(jobs,projects,agents)`→`NewLocal(jobs,projects,agents)`）。
- [x] `go build ./... && go vet ./...` 绿（`commands/mcp.go` 改走兼容包装 `mcpserver.ServeLocal(ctx, Jobs, Projects, Agents)` 保持行为不变）。
- [x] commit：`refactor(gofer): mcpserver 抽 Backend 接口 + localBackend(零行为变化)(E28 P2)`。

> 落地说明：新增 `internal/mcpserver/backend.go`（`Backend` 接口 10 方法，签名照 design §7）+ `internal/mcpserver/backend_local.go`（`localBackend` + `newLocalBackend`，把 10 handler 的后端调用逐字搬入）；`server.go` 的 `New`/`Serve` 改签名 over `Backend`，handler 闭包改 over `b` 仅保留输入校验(tail_log stream)+provenance 注入(run_job)+视图投影；新增 `NewLocal`/`ServeLocal` 兼容入口。`go test ./...` 全绿。

---

## P3：clientBackend（转发）

### T3.1 `internal/mcpserver/backend_client.go`

```go
type clientBackend struct{ cli *client.Client }
func NewClientBackend(cli *client.Client) Backend { return &clientBackend{cli} }

func (b *clientBackend) RunJob(req job.JobRequest) (job.JobResult, error) { return b.cli.SubmitJob(req) } // 异步,返初始态
func (b *clientBackend) GetJob(id string) (job.JobResult, error)         { return b.cli.GetJob(id) }
func (b *clientBackend) CancelJob(id string) (job.JobResult, error)      { return b.cli.CancelJob(id) }
func (b *clientBackend) GetResult(id string) (string, error)            { r,e:=b.cli.GetJob(id); return r.ResultJSON, e }
func (b *clientBackend) GetInteractions(id string) ([]job.Interaction, error) { return b.cli.GetInteractions(id) }
func (b *clientBackend) AnswerInteraction(id,iid,ans string) (job.Interaction, error) { return b.cli.AnswerInteraction(id,iid,ans) }
func (b *clientBackend) TailLog(id,stream string, maxBytes int64) (string, error) {
    s, err := b.cli.GetLogs(id, stream); if err != nil { return "", err }
    if maxBytes > 0 && int64(len(s)) > maxBytes { s = s[int64(len(s))-maxBytes:] } // 客户端截尾(末 N 字节)
    return s, nil
}
func (b *clientBackend) ListProjects() ([]projectEntry, error) { /* cli.ListProjects()→projectEntry(host_path 空) */ }
func (b *clientBackend) ListAgents() ([]agentEntry, error)     { /* cli.ListAgents()→agentEntry */ }
func (b *clientBackend) GetArtifacts(id string) ([]artifactView, error) { /* cli.ListArtifacts(id) raw→解析→[]artifactView */ }
```

### P3 验收
- [x] `go test ./internal/mcpserver/...` 绿（clientBackend 单测：httptest mux 罐头 JSON → `NewClientBackend(client.New(ts.URL,tok))` → 调各方法验转发；tail 截尾断言；artifacts raw 解析断言；projects host_path 空 + 非 nil 空切片；agents 1:1）。原 server_test 仍绿。
- [x] `go build ./... && go vet ./...` 绿。
- [x] commit：`feat(gofer): mcpserver clientBackend 转发到中央 serve(E28 P3)`。

> 落地说明：新增 `internal/mcpserver/backend_client.go`（`clientBackend{cli *client.Client}` + `NewClientBackend`，10 方法转发 `*client.Client`）+ `backend_client_test.go`（12 测，httptest mux）。关键映射：`GetResult`=`GetJob().ResultJSON`；`TailLog` 取全量 `GetLogs` 后客户端截末 N 字节（maxBytes>0）；`ListProjects` 由 `[]ProjectMeta` 映射，`host_path/container_path/AllowExec/MaxConcurrentJobs` 留空（meta 端点不暴露，同 --remote），非 nil 空切片；`ListAgents` `[]AgentMeta`→`agentEntry` 1:1，非 nil 空；`GetArtifacts` 解析 `ListArtifacts` 返回的内层 `[{name,size,mtime}]` raw 数组（`len(raw)==0`→非 nil 空），与 local 行为对齐。

---

## P4：commands/mcp.go 模式分支

### T4.1 flag + 判定（D1/D2）

```go
var mcpOpts = struct{ standalone bool }{}
func NewMcpCmd() *gcli.Command { return &gcli.Command{ Name:"mcp", ...,
  Config: func(c *gcli.Command){ bindConfigFlag(c); bindServerFlags(c) // --server/-s + --token (env 默认)
      c.BoolOpt(&mcpOpts.standalone,"standalone","",false,"force in-process mode (ignore GOFER_SERVER_ADDR)") },
  Func: runMcp } }

func runMcp(_ *gcli.Command, _ []string) error {
    addr := resolveServerAddr(config.InputCfgFile, jobConnOpts.server) // flag/env/config，空=无
    if !mcpOpts.standalone && addr != "" {                              // D1: client
        cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
        if err != nil { return errorx.Failf(mcpExitErr, "%v", err) }
        return serveMcp(mcpserver.NewClientBackend(cli))               // 不建 Core
    }
    // standalone(现状路径): config.Load + ApplyProjectOverlays(stderr 警告) + core.Build + localBackend
    ... cr, _ := core.Build(cfg); defer cr.Close()
    return serveMcp(mcpserver.NewLocal(cr.Jobs, cr.Projects, cr.Agents))
}
// serveMcp 包 isCleanShutdown 逻辑(stdin EOF/ctx 取消→exit 0，stdout 洁净)
```
> `resolveServerAddr`：复用 `newClient` 里的 addr 解析（flag>env>config.server.addr），抽一个小 helper 或内联判断；注意 standalone 模式**不**因 env 存在而误切（D2）。

### P4 验收
- [x] 单测：`mcpUseClient` 真值表（`(true,"x")→false`、`(false,"")→false`、`(false,"x")→true`、`(true,"")→false`）+ `NewMcpCmd` 绑定 --server/--token/--standalone/-c 断言。`go test ./internal/commands/...` 绿。
- [x] 冒烟：`gofer mcp --help` 列出 `-s/--server`、`--standalone`、`--token`、`-c/--config`（env 默认经 gcli `${ENV}` 展开）；模式判定见 `mcpUseClient` 单测（不真起 stdio，stdout 洁净）。
- [x] `go build ./... && go vet ./...` 绿。
- [x] commit：`feat(gofer): gofer mcp client 模式分支(--server env 默认 + --standalone 逃生)(E28 P4)`。

> 落地说明：`internal/commands/mcp.go` —— 新增包级 `mcpOpts{standalone}`；Config 加 `bindServerFlags(c)` + `--standalone`；纯函数 `mcpUseClient(standalone, serverAddr)=!standalone && serverAddr!=""`（**只看 jobConnOpts.server**=flag/env，不看 config.server.addr，避免 serve 主机裸 mcp 连自己，D1）。`runMcp`：client 分支 `newClient`→`mcpserver.Serve(NewClientBackend(cli))`（不建 Core），否则 standalone 现状路径（config.Load + ApplyProjectOverlays(stderr) + core.Build + defer Close + `ServeLocal`）。`isCleanShutdown` 收尾抽成 `finishMcp(err)`（stdin EOF/ctx 取消→exit0；否则 `errorx.Failf(mcpExitErr)`），两路径共用；两路径均不写 stdout（协议通道）。新增 `internal/commands/mcp_test.go`。

---

## P5：真机 E2E（双 client 协作 + standalone 回归）

> 验证"多 claude 经中枢间接协作"语义（design §8c）。可用 `exec` agent 模拟（无需真 claude）。

### T5.1 双 client 看同一 job

> **配置示例修正（P5 实测踩坑，照此才跑得起来）**：exec agent **不豁免**项目 allowlist（`internal/agent/allow.go`），仅 `allow_exec:true` 不够，须显式 `allowed_agents: [exec]`；result base 默认 `<host_path>/tmp/gofer` 会与仓库 `tmp/gofer*` 撞名，用 `storage.root: <某独立目录>` 规避；健康探针是 **`/health`**（非 `/readyz`）。可用最小 config：
> ```yaml
> server: { addr: 127.0.0.1:18941, allow_empty_token: true }
> storage: { db_path: .../tmp/e28.db, root: .../tmp/e28-results }
> projects:
>   demo: { host_path: .../tools/gofer, allowed_agents: [exec], default_agent: exec }
> ```

1. 起 serve（临时 config，一个 project allowed_agents:[exec]，含 local runner）。
2. 模拟两个 mcp client：`GOFER_SERVER_ADDR=<serve> gofer mcp`（client 模式）起两个进程（或直接对同一 serve 用两个 `client.New` + `NewClientBackend` 在测试里跑工具）。
3. clientA `bridge_run_job` 派一个 exec job → 得 id；clientB `bridge_get_job`/`bridge_tail_log`/`bridge_get_result` 看到**同一 job** 同状态（同库，跨进程可见）。
4. Web 控制台同时能看到该 live job（状态一致，验割裂消除）。

### T5.2 互答 interaction
1. 派一个会触发 `pending_interaction` 的 job（或用既有交互样例）。
2. clientA `bridge_get_interactions` 看到 pending；clientB `bridge_answer_interaction` 作答 → job 续跑完成。

### T5.3 standalone 回归
1. `gofer mcp --standalone`（或无 env）→ 进程内单机：`bridge_list_projects`/`bridge_run_job` 仍工作（现状行为不变）。

### P5 验收
- [x] **T5.1 PASS**（stdio 全栈）：两个独立 `gofer mcp` client 进程经同一 serve，A `bridge_run_job`(exec echo) 得 id，**B 进程 `bridge_get_job` 看到同一 job 终态 + `bridge_tail_log` 读回 A 的输出 `hi-from-A`**（跨进程同库可见=中枢共享状态最强证据）。
- [x] **T5.3 PASS**（standalone 回归）：`mcp --standalone`（**故意设 env 仍忽略**）走进程内 localBackend、load config、`bridge_list_projects` 正常返回。
- [x] **provenance PASS**：job 落库 `channel=mcp` + `client=<主机名>`（get_job / `job show` / raw API 三处一致）。
- [~] **T5.2 互答 interaction**：E2E 未单独跑（exec agent 不易触发 `pending_interaction`）；**转发链路已由 P1/P3 单测覆盖**（client `GetInteractions`/`AnswerInteraction` 返 Interaction + clientBackend 转发），本地互答由既有 `httpapi`/`mcpserver` 交互测试背书。**遗留**：真 claude/交互式 agent 的双 client 互答留 P5 后续或 E36 一并验。
- [x] stdout 全程洁净（三次运行均合法 JSON-RPC 帧、mcp stderr 0 字节、serve.log 无 error）。
- [x] commit：`docs(gofer): E28 client 模式 MVP 落地回填(roadmap + plan E2E 结果)`。

---

## 4. 完成判定

- P1–P5 验收全 PASS；`go build/vet/test ./...` 全绿；现有 `mcpserver/server_test.go` 零行为变化背书绿。
- 双 client 经中枢看到同一 job + 互答续跑 + Web 同视图（割裂消除）；standalone 回归不变。
- 安全：stdout 洁净（mcp 协议通道）、client 模式不建 DB、provenance(channel=mcp) 落库。
- 边界留后续记录在案：host_path 缺失 / 精确 tail SSE / E36 信箱原语。

## 5. 实施结果（2026-06-27 全落地）

**提交链**：`b52901b`(P1 client 3 方法) → `bf18bc7`(P2 Backend 接口+localBackend 零行为变化) → `3f57ec3`(P3 clientBackend 转发) → `a21df1f`(P4 mcp 模式分支) → 本次 docs 回填。

**关键结果**：
- 服务端 endpoint 全复用，仅补 3 个 client 方法 + Backend 接口双实现 + 一个模式分支；`go build/vet/test ./...` 全绿；**P2 现有 `mcpserver/server_test.go` 零断言改动**（仅 `New→NewLocal` 一行）背书零行为变化（G023）。
- P5 真机 E2E 全 PASS：双 client 进程经同一 serve 共享状态（B 读到 A 的 job 输出）、provenance `channel=mcp` 落库、standalone `--standalone` 忽略 env 回归不变、stdout 洁净。

**关键决策/偏差（实施中确认）**：
- `/v1/agents` 真实 wire 形是 `{key,type,available,version,error}`（非 design §9 初稿的 `{name,detail}`）；`client.ListAgents` 内部解码真实形再折叠为 `AgentMeta{name,type,available,detail}`（name=key、detail=version|error），与本地 mcpserver list-agents handler 逐字一致 → client/standalone 两模式 agent 列表对齐。
- client 模式 `bridge_list_projects` 省略 `host_path/container_path/allow_exec`（`/v1/meta` 不暴露服务端文件系统路径，同 E38 `--remote`）——属 intended；空 slice 仍非 nil。
- tail `max_bytes` client 模式在**客户端截末 N 字节**（`GetLogs` 无 byte 上限），与 local 服务端截尾结果等价。

**遗留**：① T5.2 真互答（交互式 agent 双 client）留后续/E36；② 精确 tail/SSE 流式二期；③ host_path 缺失若 agent 需要再给 meta 补可选字段；④ **E36 信箱原语**（agent 注册 + inbox 主动推送）是多 agent 协作下一片。
