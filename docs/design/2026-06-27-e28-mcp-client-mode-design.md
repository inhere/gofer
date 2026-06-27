# E28 · gofer mcp client 模式 设计（多 agent 经中枢协作 MVP）

> 一句话：给 `gofer mcp` 加 **client 模式**——10 个 `bridge_*` 工具的后端从"进程内直操 DB/执行"切换为"转发到中央 serve"，消除 stdio 1:1 + 状态割裂，为多 agent 协作铺地基。
> 关联：roadmap [`../2026-06-20-enhancements-roadmap.md`](../2026-06-20-enhancements-roadmap.md) §E28 + §横切「多 agent 协作 epic」。本文=**E28 MVP（通道层）**；身份/信箱（E36）、角色（E35）、监督应答（E25）各自后续，不在本文。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-06-27 | claude | 初稿：Backend 接口双实现 + 3 client 方法 + 模式触发(env 默认 client + --standalone 逃生)，待审核 |

## 2. 背景 / 动机

现状 `gofer mcp` 是 **standalone in-process**：每个 `bridge_*` 工具闭包直调进程内 `job.Service`/`project.Registry`/`agent.Registry`（`mcpserver.New(jobs, projects, agents)`）。问题（roadmap E28）：

- 多个工作目录的 claude 各自 `command: gofer args: [mcp]` 拉起**独立** mcp 子进程，各自一份进程内状态 → 互不可见、无法协作。
- 与 `serve`/Web 控制台状态割裂：mcp 派的 job 在 mcp 进程里执行/落库，Web 看不到 live job；跨进程 SQLite 写锁风险。

MVP 目标：多 claude 各拉自己的 stdio mcp 子进程、**后端共指同一中央 serve** → 状态一致、Web+MCP 同视图、经中枢间接协作（A 派活、B 看见/续接/互答）。

> **为什么不换 HTTP MCP transport**：那要 claude 端配 URL+鉴权、gofer 实现 HTTP MCP server，更重；stdio 子进程转发对 claude 端**零改动**（仍 `command` 拉起）。详见 roadmap E28 决策段。

## 3. 名词

- **standalone 模式**：现状。mcp 进程内建 Core（DB/registries），`bridge_*` 直接本进程执行 local job。
- **client 模式**（本文新增）：mcp 进程只当瘦客户端，`bridge_*` 经 `internal/client` 转发到中央 serve；**不建 Core/DB**。
- **Backend**：本文引入的接口，抽象 10 个 bridge 工具所需的后端操作；两实现 `localBackend` / `clientBackend`。

## 4. 范围

**做（MVP）**：
- `mcpserver` 抽 `Backend` 接口 + `localBackend`（现逻辑零行为变化搬入）+ `clientBackend`（转发 `*client.Client`）。
- `internal/client` 补 3 项：`ListAgents()`、`GetInteractions()`、`AnswerInteraction` 增强返回 `job.Interaction`（服务端 endpoint **全已存在**，仅补客户端方法）。
- `gofer mcp` 加 `bindServerFlags`（`--server/--token`）+ `--standalone`：按模式判定建 local 或 client 后端。

**不做**：
- **E36 信箱原语**（`bridge_post/poll_message` + agent 注册）——下一片，本 MVP 只复用既有 10 工具。
- SSE 流式 tail（先一次性 `GetLogs` + 客户端截断）。
- 多 hub HA、HTTP MCP transport。

## 5. 已确认事项（2026-06-27 决策）

- **D1 模式触发** = `--server` 默认读 `GOFER_SERVER_ADDR` env（复用 `bindServerFlags`，与 job/wf 一致、零新配置）。判定优先级：`--standalone` 显式 → standalone；否则解析到 server addr（flag/env 非空）→ **client**；否则 → standalone。
- **D2 `--standalone` 逃生开关必做**（非按需）：任何环境（即便 `GOFER_SERVER_ADDR` 已设）都能用 `gofer mcp --standalone` 强制回退进程内单机模式。
- **D3 先 design+plan 审核，再 SUPMODE 实施。**

## 6. 架构

```
standalone:  claude ──stdio──> gofer mcp ──> localBackend ──> job.Service + registries(进程内 DB/执行)
client:      claude#A ─stdio─> gofer mcp#A ─┐
             claude#B ─stdio─> gofer mcp#B ─┼─> clientBackend ─HTTP(/v1)─> 中央 serve(单一 DB/执行/Web 同视图)
                                            ┘   (复用 internal/client + bindServerFlags)
```

- mcp 进程内 10 个 `bridge_*` handler 改为调 `Backend` 接口；视图投影（`jobView`/`interactionView`/…snake_case）仍留 `mcpserver`，两后端共用。
- client 模式下 mcp **不建 Core**（无 DB 文件、无 registries、无本进程执行）——纯转发。

## 7. Backend 接口（10 方法）

接口定义在 `mcpserver`，两实现同包。返回类型尽量用领域类型（`job.JobResult`/`job.Interaction`），由 handler 统一投影；项目/agent/产物因两端来源结构不同，直接返回 mcpserver 视图类型。

```go
type Backend interface {
    ListProjects() ([]projectEntry, error)                    // local: registry; client: ListProjects(丢 host_path,同 --remote)
    ListAgents() ([]agentEntry, error)                        // local: registry+Detect; client: ListAgents(新)
    RunJob(req job.JobRequest) (job.JobResult, error)         // local: Submit; client: SubmitJob(异步,返初始态)
    GetJob(id string) (job.JobResult, error)                  // local: Get(规整 !ok→err); client: GetJob
    TailLog(id, stream string, maxBytes int64) (string, error)// local: TailLog; client: GetLogs+客户端截尾
    CancelJob(id string) (job.JobResult, error)               // local: Cancel+Get; client: CancelJob
    GetInteractions(id string) ([]job.Interaction, error)     // local: GetInteractions; client: GetInteractions(新)
    AnswerInteraction(id, iid, answer string) (job.Interaction, error) // local: 现有; client: AnswerInteraction(增强返 Interaction)
    GetArtifacts(id string) ([]artifactView, error)           // local: GetArtifactManifest; client: ListArtifacts(解析 raw)
    GetResult(id string) (string, error)                      // local: Get→ResultJSON; client: GetJob→ResultJSON
}
```

`New(b Backend) *mcp.Server` / `Serve(ctx, b Backend) error`。

## 8. 关键流程

**(a) 模式判定（runMcp）**：
```
if mcpOpts.standalone:          → localBackend (config.Load + ApplyProjectOverlays + core.Build)
elif resolved server addr != "":→ clientBackend (newClient(addr,token); 不建 Core)
else:                           → localBackend
```
> stdout 是 MCP 协议通道——两路径都**不得**打印 stdout；overlay/错误走 stderr（沿用现状）。

**(b) 一次 bridge 调用（client 模式）**：claude → stdio JSON-RPC → mcp handler → `clientBackend.XXX()` → `internal/client` HTTP `/v1/...` → 中央 serve → 回投影。

**(c) 多 claude 协作语义验证**：serve 起；claude#A 经 mcp#A `bridge_run_job` 派 job；claude#B 经 mcp#B `bridge_get_job`/`bridge_tail_log`/`bridge_get_result` 看到**同一 job**（同库）；A 的 job 提问 `bridge_get_interactions`，B `bridge_answer_interaction` 作答续跑——这就是"经中枢间接协作"。

## 9. API / 契约（服务端复用，仅补客户端）

| client 方法 | endpoint（已存在）| 备注 |
|---|---|---|
| `ListAgents()` | `GET /v1/agents` | 解析 `{agents:[{name,type,available,detail}]}` |
| `GetInteractions(id)` | `GET /v1/jobs/{id}/interactions` | 反序列化为 `[]job.Interaction`（端点返该结构）|
| `AnswerInteraction(id,iid,answer)` 增强 | `POST /v1/jobs/{id}/interactions/{iid}/answer` | 端点已返更新后 `job.Interaction`，改解析返回值（原仅返 error）|

> `AnswerInteraction` 是**签名变更**（多一个返回值）；现有调用方（CLI）需同步改（仅 1-2 处）。

## 10. 安全 / 约束

- **token 来源**：client 模式 token 走 `bindServerFlags`（`--token` 默认 `GOFER_SERVER_TOKEN` env），与 CLI 一致；不新增配置。
- **stdout 洁净**（沿用现状铁律）：mcp 全程不打印 stdout（协议通道）；client 模式连接失败等错误经返回的 coded error 走 stderr。
- **provenance**（E34）：`RunJob` 仍打 `Channel="mcp"` + `Client=os.Hostname()`（mcp 进程主机名）；client 模式下这条 JobRequest 字段随 HTTP 提交，serve 持久化 → Web/审计可见"哪台 mcp 提交"。
- **不建 DB**（client 模式）：无本地 `gofer.db`、无写锁竞争——根治 standalone 多进程 SQLite 隐患。
- **依赖防环**（G022）：`mcpserver` → `internal/client`（client 不 import mcpserver，无环）；`mcpserver` 仍**不 import** `internal/commands`。`commands` 同时装配两后端（已 import client+mcpserver）。

## 11. 部署

- 纯 Go CLI 改动，**无前端**：`go build` 即可（不需 `make web`）。容器换装：`gofer worker stop` → 装新二进制 → `gofer worker -d`；mcp 子进程由 claude 按需拉起，注册 `command: gofer, args: ["mcp"]`（client 模式时该进程读 `GOFER_SERVER_ADDR` env 自动转发，**注册项零改动**）。
- host 侧用户自行重建（见 [[gofer-build-install]]）。

## 12. 待确认 / 留后续

- **host_path 缺失**：client 模式 `bridge_list_projects` 经 `/v1/meta` 拿不到服务端路径（同 E38 `--remote`）——MVP 接受；若 agent 需要路径，后续给 meta 端点补可选字段或另开端点。
- **精确 tail / 流式**：`max_bytes` 先客户端截断（取末尾 N 字节）；SSE 流式 tail 二期。
- **E36 信箱**：多 agent **双向**（serve→agent 定向、agent 间互答的主动推送）需注册 + inbox 轮询原语，本 MVP 仅靠轮询既有工具达成"间接协作"，主动推送留 E36。

## 13. 结论

服务端 endpoint 全就绪 → 改动集中在 mcp 端：`Backend` 接口双实现 + 3 个 client 方法 + 一个模式分支。MVP 切片小、复用度高（7/10 工具直接映射现有 client），且 `localBackend` 抽取须**零行为变化**（现有 `server_test.go` 全绿背书，G023）。落地后即消除状态割裂，为 E36/E35/E25 多 agent 协作铺好通道层。实施计划见 [`../plans/2026-06-27-e28-mcp-client-mode-plan.md`](../plans/2026-06-27-e28-mcp-client-mode-plan.md)。
