# P1 · L2 身份/信箱（E36）实施计划 ✅ 完成（2026-06-28）

> 主纲 [`../2026-06-28-multi-agent-collab-plan.md`](../2026-06-28-multi-agent-collab-plan.md) · design §6.2/§8.1-8.2/§9/§10 · bd `example-project-y2jg`
> 目标：driver agent 经 mcp 注册 name+id 到 serve → presence 名册 + mailbox（post/poll），MCP 单向靠"注册+轮询 inbox"经中枢达成双向。

> **完成记录（落地与计划的偏差，2 处自动决策）**：
> 1. **CLI 名 `gofer presence`（别名 `driver`），非 `gofer agent ls`**：`gofer agent` 已存在=job-agent 定义检视(已带 `list` 子命令)，复用会混淆 driver/job 二分+子命令冲突 → 新建顶层命令分离。inbox token 标志 `--agent-token`(避 bearer `--token` 冲突)。
> 2. **httpapi 用 `SetPresence` 注入器（非宽位置构造器加参）**：照既有 `SetMetrics` 逃生口(重建 router + 路由仅在 service 非 nil 时挂载)，避免改 5 处 `httpapi.New` 测试调用点。
> 另：`presence.Agent/Message/RegisterResult` 加 snake_case json tag 作**单一线缆契约**(httpapi 直返 + client 直解码，去重 view 结构)。commits: `05492dc`(P1.1) `ba458f4`(P1.2) `aab40fc`(P1.3) `92ab5d7`(P1.4) `ceaa475`(P1.5 CLI+sweeper) `b73f281`(E2E)。

## 锚点（照抄模式，file:line）

- 建表：`jobstore/store.go:59-171`(schemaStmts) · DAO 样板：`jobstore/interactions.go`(UpsertInteraction 52-80 / ListInteractions 85-104 / scanInteraction 37-44 / selectInterCols 32-34)
- 路由：`httpapi/server.go:216-334`(Group `/v1` + 中间件链) · handler 样板：`httpapi/interaction_handler.go:31-94` · caller：`auth.go:27-51`+`server.go:50-57`(callerFromCtx) · 错误：`respond.go:18-20`(writeError)
- client：`client/client.go:24-66`(New/NormalizeBaseURL) `497-538`(do/doJSON) `133-138`/`466-495`(GET/POST 样板)
- Backend：`mcpserver/backend.go:14-25`(接口) `backend_local.go:18-89` `backend_client.go:18-35` · 工具注册：`server.go:41-44`+`194-202` · 装配：`commands/mcp.go:68-98`（**新增工具零改 mcp.go**）
- core 线缆：`commands/mcp.go` ServeLocal(cr.Jobs,cr.Projects,cr.Agents) ← 需加 presence

---

## P1.1 jobstore：两表 + DAO

**改 `jobstore/store.go` schemaStmts**（末尾追加，照 §9 DDL；含索引）：

```go
`CREATE TABLE IF NOT EXISTS agent_presence (
  agent_id TEXT PRIMARY KEY, agent_token TEXT NOT NULL, name TEXT NOT NULL,
  role TEXT, project_key TEXT, caller_id TEXT, client TEXT, status TEXT NOT NULL,
  registered_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL, meta_json TEXT)`,
`CREATE TABLE IF NOT EXISTS messages (
  id TEXT PRIMARY KEY, to_agent TEXT NOT NULL, from_agent TEXT NOT NULL, to_spec TEXT,
  kind TEXT NOT NULL, body TEXT, ref TEXT, status TEXT NOT NULL,
  created_at INTEGER NOT NULL, expires_at INTEGER, read_at INTEGER)`,
`CREATE INDEX IF NOT EXISTS idx_messages_inbox ON messages(to_agent, status, created_at)`,
```

**新文件 `jobstore/presence.go`**（DAO，照 interactions.go 风格：Record struct + scan + selectCols(COALESCE nullable) + writeMu 写）：

```go
type PresenceRecord struct {
    AgentID, AgentToken, Name, Role, ProjectKey, CallerID, Client, Status string
    RegisteredAt, LastSeenAt int64
    MetaJSON string
}
const selectPresenceCols = `SELECT agent_id, agent_token, name, COALESCE(role,''),
  COALESCE(project_key,''), COALESCE(caller_id,''), COALESCE(client,''), status,
  registered_at, last_seen_at, COALESCE(meta_json,'') FROM agent_presence`

func (s *Store) UpsertPresence(rec PresenceRecord) error      // INSERT ... ON CONFLICT(agent_id) DO UPDATE（照 UpsertInteraction）
func (s *Store) GetPresence(agentID string) (PresenceRecord, bool, error)  // 照 GetJob:226-235
func (s *Store) ListPresence() ([]PresenceRecord, error)      // 照 ListInteractions（无 where，ORDER BY last_seen_at DESC）
func (s *Store) TouchPresence(agentID string, ts int64) error // UPDATE last_seen_at=? WHERE agent_id=?
func (s *Store) DeletePresence(agentID string) error          // 主动下线
func (s *Store) PrunePresence(cutoff int64) (int, error)      // DELETE WHERE last_seen_at < cutoff（GC offline）
```

**新文件 `jobstore/messages.go`**：

```go
type MessageRecord struct {
    ID, ToAgent, FromAgent, ToSpec, Kind, Body, Ref, Status string
    CreatedAt, ExpiresAt, ReadAt int64
}
func (s *Store) InsertMessages(recs []MessageRecord) error    // 批量 INSERT（fan-out 多行，一事务）
func (s *Store) ListInbox(agentID string, includeRead bool) ([]MessageRecord, error)  // WHERE to_agent=? AND status='unread'(可含 read) ORDER BY created_at ASC
func (s *Store) MarkRead(ids []string, ts int64) error        // UPDATE status='read', read_at=? WHERE id IN (...)
func (s *Store) PruneMessages(now int64) (int, error)         // DELETE WHERE (status='read') OR (expires_at>0 AND expires_at<now)
```

**单测 `jobstore/presence_test.go` / `messages_test.go`**（照现有 jobstore 测试用 `Open(t.TempDir())`）：
- [x] 验收：upsert→get 往返；ListPresence 顺序；TouchPresence 刷 last_seen；Prune 删 offline。
- [x] 验收：InsertMessages 批量；ListInbox 只回 unread；MarkRead 后不再回；PruneMessages 清 read/过期。
- [x] `go test ./internal/jobstore/...` 绿 → commit `feat(presence): jobstore agent_presence+messages 两表+DAO`

---

## P1.2 `internal/presence` Service（业务层）

**新包 `internal/presence`**，业务逻辑（uuid/token 生成、role: fan-out 解析、TTL 懒判定、prune）。依赖 `*jobstore.Store`（G022 单向）：

```go
type Service struct {
    store *jobstore.Store
    nowFn func() time.Time
    ttl   time.Duration   // 默认 90s（design §12 待确认）
}
func NewService(store *jobstore.Store) *Service

type Agent struct{ AgentID, Name, Role, ProjectKey, Status string; LastSeenAt int64 }  // 对外（不含 token）
type RegisterInput struct{ Name, Role, ProjectKey, CallerID, Client, MetaJSON string }
type RegisterResult struct{ AgentID, AgentToken string }
type Message struct{ ID, FromAgent, ToSpec, Kind, Body, Ref string; CreatedAt int64 }

// Register: 同 name+caller 已存在则复用 agent_id 续约(刷 last_seen)，否则新分配 uuid+token
func (s *Service) Register(in RegisterInput) (RegisterResult, error)
// Poll: 校验 agent_token → TouchPresence → ListInbox(unread) → (ack 时)MarkRead → 返回
func (s *Service) Poll(agentID, token string, ack bool) ([]Message, error)
// Post: 解析 to(agent_id|role:x|broadcast) → fan-out 成 []MessageRecord(每在线收件人一行) → InsertMessages；无在线收件人按策略(留库 TTL / 回执)
func (s *Service) Post(from, to, kind, body, ref string) (delivered int, err error)
func (s *Service) List(roleFilter, projectFilter string) ([]Agent, error)  // 懒算 status: now-last_seen>ttl → offline
func (s *Service) Deregister(agentID, token string) error
func (s *Service) Prune() (presenceN, msgN int, err error)  // 供 serve sweeper 周期调
```

要点：
- **agent_token 校验**：`Poll`/`Deregister` 比对 `GetPresence().AgentToken`，不符 → 返回 `ErrUnauthorizedAgent`（httpapi 映 403）。
- **role: fan-out**（design §9）：`Post` 中 `to="role:reviewer"` → `List` 出 online 同 role agent，每个生成一行（`to_agent=该 agent_id`、`to_spec="role:reviewer"`）；`broadcast` 同理全 online。
- **不可达**（design §12 待确认，MVP 取**留库 TTL**）：fan-out 出 0 收件人 → 仍按 `to_spec` 留一行 `to_agent=""` + `expires_at`？或直接回 `delivered=0` 让发件方知。**MVP：delivered=0 返回 + 不留行**（发件方据返回值决定重试），role: 留库补投留后续。
- **TTL/prune**：status 查询时懒算（不写库）；`Prune` 由 serve sweeper（见 P1.5 线缆）周期删 offline 超阈值的 presence + read/过期 messages。

**单测 `presence/service_test.go`**：
- [x] Register 新建 vs 续约（同 name+caller 复用 id）；token 不为空。
- [x] Poll：token 错→ErrUnauthorizedAgent；ack=true 后再 poll 空；ack=false 不消费。
- [x] Post：直投 agent_id 1 行；role: fan-out 多行（各 online 一行）；无在线→delivered=0。
- [x] List：last_seen 超 ttl→offline；role/project 过滤。
- [x] `go test ./internal/presence/...` 绿 → commit `feat(presence): Service register/poll/post/list + role fan-out + token 校验`

---

## P1.3 httpapi：5 端点

**新文件 `httpapi/presence_handler.go`**（照 interaction_handler.go 模式：req struct + BindJSON + writeError + c.JSON）。Server 需持有 `presence *presence.Service`（构造注入，照 `s.jobs`）。

| 端点 | handler | 关键 |
|---|---|---|
| `POST /v1/agents/register` | handleRegisterAgent | BindJSON{name,role,project}; CallerID=callerFromCtx; Client=盖章 remote IP(照 E34 job_handler 盖章)；返回 {agent_id, agent_token} |
| `GET /v1/agents/presence` | handleListPresence | `?role=`/`?project=` via c.Query；`map[string]any{"agents":list}`；**不含 token** |
| `POST /v1/agents/{id}/inbox/poll` | handlePollInbox | id=c.Param; BindJSON{agent_token}; `?ack=`(默认 true)；token 错→403 |
| `POST /v1/messages` | handlePostMessage | BindJSON{from_agent,to,kind,body,ref}；返回 {delivered} |
| `POST /v1/agents/{id}/deregister` | handleDeregister | id+token；no-op 幂等 |

路由注册（`server.go` buildRouter Group `/v1` 内追加，照 213-305 写法）：
```go
r.POST("/agents/register", s.handleRegisterAgent)
r.GET("/agents/presence", s.handleListPresence)
r.POST("/agents/{id}/inbox/poll", s.handlePollInbox)
r.POST("/messages", s.handlePostMessage)
r.POST("/agents/{id}/deregister", s.handleDeregister)
```
错误映射 helper `presenceStatus(err)`（照 interactionStatus:99-109）：`ErrUnauthorizedAgent→403`、`ErrUnknownAgent→404`、其他→400。

**单测 `httpapi/presence_handler_test.go`**（照现有 httpapi 测试起 Server）：
- [x] register→presence 可见；poll token 错 403；post 直投 delivered=1；poll 取到。
- [x] `go test ./internal/httpapi/...` 绿 → commit `feat(presence): httpapi 5 端点(register/presence/poll/messages/deregister)`

---

## P1.4 client 5 方法 + Backend 扩 + mcp 4 工具

**`client/client.go` 补 5 方法**（照 GET/POST 样板 133-138/481-495）：
```go
func (c *Client) RegisterAgent(name, role, project string) (agentID, token string, err error)  // POST /v1/agents/register
func (c *Client) PollInbox(agentID, token string, ack bool) ([]presence.Message, error)         // POST .../inbox/poll
func (c *Client) PostMessage(from, to, kind, body, ref string) (int, error)                     // POST /v1/messages
func (c *Client) ListPresence(role, project string) ([]presence.Agent, error)                   // GET /v1/agents/presence
func (c *Client) DeregisterAgent(agentID, token string) error                                   // POST .../deregister
```
> 注意 client 不应 import 大包导致环——`presence.Message`/`presence.Agent` 是纯数据 struct（presence 包不 import client，无环）。若担心 client→presence 反向，可在 client 内定义对等 DTO；**倾向直接复用 presence 的纯 DTO**（确认 presence 不 import client）。

**`mcpserver/backend.go` 接口扩 4 方法**（list_pending 留 P3）：
```go
RegisterAgent(name, role, project string) (RegisterView, error)
PollInbox(agentID, token string, ack bool) ([]messageView, error)
PostMessage(from, to, kind, body, ref string) (int, error)
ListPresence(role, project string) ([]presenceView, error)
```
**两实现**：`backend_local.go` 调 `b.presence.*`（localBackend 加 `presence *presence.Service` 字段，newLocalBackend 加参）；`backend_client.go` 调 `b.cli.*`（照 RunJob 转发 29-31）。

**`mcpserver/server.go` 注册 4 工具** + view（snake_case）：
```go
mcp.AddTool(s, &mcp.Tool{Name:"bridge_register", Description:"Register this agent (name+role) to the central serve; returns agent_id+agent_token for inbox ops."}, registerHandler(b))
mcp.AddTool(s, &mcp.Tool{Name:"bridge_poll_inbox", Description:"Poll this agent's inbox for unread messages (refreshes presence heartbeat)."}, pollInboxHandler(b))
mcp.AddTool(s, &mcp.Tool{Name:"bridge_post_message", Description:"Send a message/task to another agent (by agent_id, role:<name>, or broadcast)."}, postMessageHandler(b))
mcp.AddTool(s, &mcp.Tool{Name:"bridge_list_presence", Description:"List online agents (presence registry) with role/project/status."}, listPresenceHandler(b))
```
input/output struct snake_case（照 runJobInput 234-244）；handler 照 listProjectsHandler/runJobHandler。

**线缆**：`core.Build` 创建 `presence.Service`（用同一 `*jobstore.Store`）→ 暴露 `cr.Presence` → `commands/mcp.go` ServeLocal 传入 → newLocalBackend；httpapi.Server 构造也接收 presence.Service。

- [x] 验收：`go test ./...` 全绿；`go vet`/`go list -deps ./internal/presence`(无环)。
- [x] commit `feat(presence): client 5 方法 + Backend 扩 4 + mcp 4 工具 + core/httpapi 线缆`

---

## P1.5 CLI + 部署 + E2E

**新文件 `commands/agent.go`** `gofer agent`（照 NewJobCmd:95-216 / bindServerFlags）：
- `gofer agent ls`（= presence ls）→ `client.ListPresence` → cliui table（照 E34 job list 的 cliui，CJK 对齐）。
- （可选）`gofer agent inbox <id> --token <t>`、`gofer agent send --to <x> --kind task <body>` 便于人工冒烟。

**serve sweeper 线缆**：serve 启动循环（照现有 sweeper 范式）周期 `presence.Service.Prune()`（如每 60s），日志 prune 数。

- [x] 单测：`mcpUseClient`/CLI flag 绑定（照现有）。`go test ./...` 绿。
- [x] **部署**：`go build -o tmp/gofer-linux .`；容器 `gofer worker stop → cp → gofer worker -d`；host 用户自建。
- [x] **E2E（多 agent 协作语义，照 E28 plan 手法，双 mcp client）**：
  1. serve 起（host）。
  2. claude#A 经 mcp#A `bridge_register(name=alice)` 拿 agent_id_A；claude#B `bridge_register(name=bob)` 拿 id_B。
  3. `bridge_list_presence` 两端都见 alice+bob online。
  4. A `bridge_post_message(to=id_B, kind=task, body="审 PR")`；B `bridge_poll_inbox(id_B,token_B)` 取到该消息；再 poll 为空（已 read）。
  5. token 错的 poll 被拒（403）。
  6. role: 投递：A 注册 role=reviewer、`bridge_post_message(to="role:reviewer")` → A 自己 inbox 收到。
- [x] 回填主纲进度 + commit `feat(presence): gofer agent CLI + serve prune sweeper + E2E`。bd `y2jg` close。

## 验收总清单（P1 Done 标准）

- 两表 additive 迁移、旧库幂等；5 端点 + 5 client + 4 mcp 工具全绿单测。
- 双 mcp client 经同一 serve **互发互收**（A→B inbox）；token 软隔离生效；role: fan-out 正确。
- `go test ./...` 全绿、`go vet` 净、无 import 环；三端（CLI/serve/mcp）部署冒烟 PASS。
