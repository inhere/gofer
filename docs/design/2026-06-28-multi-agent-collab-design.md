# 多 agent 协作 合并设计（E28 通道 ✅ + E36 身份/信箱 + E35 角色 + E25 监督应答）

> 一句话：把 gofer 从"单 agent 提交执行 job"推向"**多个 agent 经中枢互相派活、协作、互答**"。E28 通道（client 模式）已通，本文在其上叠加 **L2 身份/信箱(E36) → L3 角色(E35) → L4 监督应答(E25)**，依赖 **E33** session_id，与「自主化 epic」在 E25 交叠、共享 E8/E13/E17。
> 关联：roadmap [`../2026-06-20-enhancements-roadmap.md`](../2026-06-20-enhancements-roadmap.md) §横切「多 agent 协作 epic」；通道层 design [`2026-06-27-e28-mcp-client-mode-design.md`](2026-06-27-e28-mcp-client-mode-design.md)；身份关联 [`2026-06-26-session-capture-design.md`](2026-06-26-session-capture-design.md)。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-06-28 | claude | 初稿：四层模型（通道✅/身份信箱/角色/监督应答）+ 4 项决策（并存共抽象 / 覆盖 E36+E35+E25 轻量安全 / 分层 answerer / config roles 段）。待审核排 plan |
| v0.2 | 2026-06-28 | claude | 对抗复审修订（7 处）：①agent_id **不蹭** jobs.session_id（两不同概念），driver 自报 name/caller、可选外部 session ref 入 meta_json；②共享 token 下加 **agent_token** 能力句柄软隔离 + 如实标注配额到 caller 粒度；③L4 自动答**收窄到 choice+可枚举 options**，question 默认升级，诚实标"覆盖率低，价值在路由/升级"；④跨 job pending 查询 **JOIN jobs 过滤终态** + 修既有缺口（job 终态对账残留 pending→cancelled，`InteractionCancelled` 现从未赋值）+ 启动 sweeper + answerer 跳过 ErrJobTerminal；⑤**resume 须重施 role SystemInject**（否则 role 行为静默丢失）；⑥messages **按收件人 fan-out 多行** + TTL/不可达回执 + presence 周期 prune；⑦监督套娃熔断参数留 plan 钉死 |

## 2. 背景 / 动机

E28 已让多个工作目录的 claude 经 `gofer mcp --server` 共指同一 serve（状态一致、Web+MCP 同视图），但**只够"经中枢间接看见彼此的 job"**——没有：

- **身份**：serve 不知道"谁在线"，无法**定向**找某个 agent 会话；同工作目录开多个会话无从区分。
- **主动**：MCP 是 client→server 单向工具调用，agent 收不到推送（A 没法"叫" B）。
- **角色**：每开一个协作 agent 都要重发一大段"你是 reviewer / 你只做 bugfix"提示。
- **自动**：job 提问（`pending_interaction`）只能人工逐个 Web/CLI 作答，A 派的活卡在 B 的提问上要人盯。

本文把这四块**分层叠加**成一条主线，而非各做一套。

## 3. 名词

- **driver agent**：交互式 claude 会话经 mcp client 接入 serve，是**协作主体**（派活 / 被派 / 作答 / 互通）。本文新引入的一类 actor。
- **job agent**：gofer 派生执行 job 的 claude/codex/exec 进程，是**工作单元**；其 `pending_interaction` 是 L4 的作答对象。
- **presence 名册**：在线 driver agent 注册表（`agent_presence` 表）。`agent_id`=serve 分配 uuid（**公开寻址地址**，名册可见）；`agent_token`=register 返回的 **per-agent 能力句柄**（私有，poll/deregister 校验，软隔离）。
- **agent_id ≠ session_id**：`agent_id` 是 driver 的协作身份；[E33](2026-06-26-session-capture-design.md) `jobs.session_id` 是 gofer 派生 job 的底层 CLI 会话——**两不同概念，不关联**。driver 若需可自报一个外部 session ref 存 `meta_json`（仅记录，不蹭 jobs 表）。
- **mailbox / inbox**：寄给某 `agent_id`（或 `role:xxx` / 广播）的消息队列（`messages` 表）。
- **message**：信箱里一条带寻址的消息 `{kind, to, from, body, ref}`（kind=task/note/answer/escalation…）。
- **interaction**：现有的 job 运行中提问机制（**本设计不改其存储/语义**，见 [§5 决策 D1](#5-已确认事项)）。
- **answerer**：`pending_interaction` 的**可插拔作答方**——人工 Web/CLI · IM 人(E22c) · 监督(E25) · 对等 agent(E36)，同一机制可换实现。
- **role 角色**：命名预设 = base agent + system_prompt + 规则 + 默认 project/tags（E35）。

> driver agent vs job agent 是本设计的**核心二分**：协作发生在 driver 之间（信箱），执行落在 job 上（interaction）；二者经 serve 衔接。

## 4. 范围

**做**：

- **L2 身份/信箱(E36)**：`agent_presence` + `messages` 两表 + 注册/心跳/收发原语（mcp `bridge_register`/`bridge_poll_inbox`/`bridge_post_message` + 对应 `/v1` 端点 + client 方法）。
- **L3 角色(E35)**：config `roles:` 段 + per-agent `system_inject` argv 模板 + `JobRequest.Role` 解析（`job run --role` / `bridge_run_job(role=)`）。
- **L4 监督应答(E25)**：补**跨 job 列 pending interaction** 端点 + **分层 answerer**（白名单规则自动答低危 → 够不着升级人，经 L2 inbox）+ 审计(E13)/配额(E17) 钩子。

**不做（本文显式排除）**：

- **统一消息总线**：按决策 D1 **并存·共抽象**——不动成熟的 interaction 存储/channel/worker 转发，只在"answerer / 寻址"概念层与信箱统一。
- **完整独立 E8 审批门**：按 D3，E8 仅以"answerer 拒答 `confirmation` 类、必升级人"表达；完整 E8 留自主化 epic 独立 design。
- **派生监督 claude job 作默认 answerer**：按 D4 留**可选高级**形态（§8.4），MVP 用规则+升级。
- roles 动态 CRUD（DB 表 + API）、HTTP MCP transport、多 hub HA、Web 显示 presence/inbox（留 Web v2 写层，§12）。

## 5. 已确认事项

| 编号 | 决策 | 依据 |
|---|---|---|
| **D1** | **信箱与 interaction 并存·共抽象**：保留 interaction 机制零改动；新增独立轻量 `messages`/inbox 原语给 driver agent 互发；二者只在"可插拔 answerer"概念层统一，**存储分离** | 不 churn 成熟且跨 worker 的 interaction 代码 |
| **D2** | **本 design 覆盖 E36+E35+E25（轻量安全）**：一次串全四层 | roadmap "攒够后出合并 design" |
| **D3** | **E8 审批门 ≈ answerer 拒答 `confirmation` 类**：不单独重做完整 E8 | interaction 已有 `confirmation` 类型 + `WaitAnswer` 阻塞，天然是审批门原语 |
| **D4** | **E25 用分层 answerer**：规则/白名单自动答低危 → 升级人；派生监督 agent 作可选高级 | 最可控、防套娃/烧 token；半自动定位 |
| **D5** | **E35 角色 = config `roles:` 段（静态 SIGHUP）+ per-agent `system_inject` 模板** | 与现 agents/projects 配置模型一致、最省 |
| **D6** | **driver agent ≠ job agent**（§3 二分）；`agent_id` 由 serve 分配 uuid，**与 jobs.session_id 是不同概念、不关联**；身份靠注册自报 name/caller（+ 可选外部 session ref 入 meta_json） | 协作主体与工作单元解耦；避免蹭 job 会话语义 |
| **D7** | **共享 token 内软隔离**：register 返回 `agent_token` 句柄，inbox 操作校验之（防误读他人 inbox）；同 caller token 的多 driver 互为**同一信任域**（非跨域硬隔离），E17 配额到 **caller** 粒度 | 共享 token + 可发现 agent_id 的现实约束 |

## 6. 架构

### 6.1 四层叠加（自下而上，各层独立可用）

```
L4 监督应答(E25)   分层 answerer: 白名单自动答低危 → confirmation/高危/超轮次 升级人(经 L2 inbox)
   ▲                跨 job 列 pending interaction;审计标 AI/人;配额管 token
L3 角色(E35)       roles 预设(reviewer/bugfix)=base agent+system_prompt+规则+默认 project/tags
   ▲                注入靠 per-agent system_inject argv 模板;job run --role / 注册带 role
L2 身份/信箱(E36)  presence 名册(register+心跳) + mailbox(post/poll inbox)
   ▲                MCP 单向 → "注册+轮询自己 inbox" 经中枢达成双向
L1 通道(E28 ✅)    中央 serve + gofer mcp --server client 模式;多 agent 共指同库、状态一致
```

### 6.2 driver / job 二分与衔接

```
工作目录A: claude(driver A) ─stdio─> gofer mcp#A ─┐
工作目录B: claude(driver B) ─stdio─> gofer mcp#B ─┼─HTTP /v1─> 中央 serve ── SQLite
                                                  ┘            ├─ agent_presence(在线名册)
                                                               ├─ messages(信箱/inbox)
                                                               ├─ interactions(job 提问, 不动)
                                                               └─ jobs / events / artifacts ...

协作:  A bridge_post_message(to=B, kind=task) ─▶ messages ─▶ B bridge_poll_inbox 取到 ─▶ B bridge_run_job 执行
作答:  job 提问 ─▶ interactions(pending) ─▶ [L4 answerer] 白名单? 自动答 : 升级(post escalation 到 owner inbox) ─▶ driver 取到 ─▶ bridge_answer_interaction
```

要点：

- **协作走信箱（messages），执行走 job，作答走 interaction**——三条线各自原语，serve 衔接。
- **双向**：serve→agent 定向 = 往该 agent inbox 投消息；agent↔agent = 互投 inbox。MCP 仍单向，靠 driver **轮询自己 inbox** 实现"收推送"（SSE 推送二期，§12）。
- L4 answerer 既可是 serve 内置规则器（MVP），也可是一个 driver agent 轮询 pending 后作答（§8.4，同机制）。

## 7. 模块（按包）

| 包 | 改动 | 说明 |
|---|---|---|
| **internal/presence**（新）or `jobstore` 内 | `agent_presence` + `messages` 表 DAO + 注册/心跳/收发/过期逻辑 | 与 `jobstore` 同风格（SQLite、upsert、COALESCE）。是否独立包按 G024 判据：域自洽且与 job 无强耦 → 倾向独立 `internal/presence` |
| **internal/mcpserver** | Backend 接口扩 5 方法 + 5 个新 `bridge_*` 工具 + view 投影 | 沿 E28 双实现：local 直调 presence/job，client 转发 `internal/client` |
| **internal/httpapi** | 新增 6 个 `/v1` 端点（§10） | 复用 auth/quota 中间件 |
| **internal/client** | 补对应方法（register/poll/post/listPresence/listPending） | client 模式转发 |
| **internal/config** | `roles:` 段（`RolesConfig`）+ `AgentConfig.SystemInject []string` | SIGHUP 热载（Registry 已支持） |
| **internal/agent** | `template.Vars` 加 `SystemPrompt`；role 解析 helper | 渲染 system_inject 模板 |
| **internal/job** | `JobRequest.Role`/`SystemPrompt`；submit 解析 role + 注入 system_prompt（类比 SessionInject）；`jobstore.ListPendingInteractions()` | interaction 存储不动，只加跨 job 查询 |
| **internal/supervisor**（新）or `serve` 内 | 分层 answerer：poller + 策略 + 升级路由 | 经 `job.Service` 公开 API 作答（G022 不反向 import） |
| **internal/commands** | `job run --role`；`agent`（presence 查看）、`inbox`（可选）子命令 | 入口只绑定/转发（G021） |

依赖方向（G022）：入口 → 编排(serve/supervisor) → job/presence → 数据层。`supervisor` 经 `job.Service` 接口取/答 interaction，**不反向**。

## 8. 关键流程

### 8.1 driver agent 上线 + 心跳轮询

```
driver A 启动协作 → bridge_register(name="reviewer-A", role="reviewer", project="cv")
                  → serve 分配 agent_id(uuid) + agent_token(per-agent 句柄) → upsert agent_presence(online, last_seen=now)
                  → 返回 {agent_id, agent_token}（driver 后续调用持有二者）
循环:  bridge_poll_inbox(agent_id, agent_token) → 校验句柄 → 取 unread + 刷新 last_seen → 处理 → (隐式心跳)
过期:  last_seen 超 TTL(如 90s) → 名册判 offline（懒判定，查询时算 status）
GC:    presence/messages 行需周期 prune（懒判定只算 status、不删行）：offline 超 N 刷掉 presence；read/过期 message 清理
```

> agent_id 公开（presence 可见、`post_message(to=)` 寻址用）；agent_token 私有（poll/deregister 校验）。同 caller token 多 driver 同信任域——agent_token 是软隔离防误操作，非跨域硬隔离（D7）。

### 8.2 A 派活给 B（信箱式协作）

```
A: bridge_post_message(to="reviewer-B"|"role:reviewer", kind="task", body="审下 PR#12", ref="job:..."?)
   → messages 入库(unread, from=A.agent_id)
B: bridge_poll_inbox(B.agent_id) → 取到 task → B 自行决定: bridge_run_job(...) 执行 / 直接在自身上下文做
   → (可选) B bridge_post_message(to=A, kind="note", body="已完成 → job xyz", ref="job:xyz") 回执
```

> `to="role:reviewer"` = 投给该 role 的任一/全部在线 agent（投递策略见 §9）；`broadcast` 慎用、限频。

### 8.3 监督应答 job 提问（L4 分层，核心）

```
job agent 运行中提问 → job.CreateInteraction → interactions(pending) + job=StatusPendingInteraction (现状不变)
L4 answerer 发现:  poller 周期调 ListPendingInteractions()（JOIN jobs 仅取活跃 job 的 pending；或订阅 E13 事件）
策略判定(每条 pending):
  ├─ type==confirmation | 无 options | 自由文本 question | 命中高危规则 | 超应答轮次  → 升级人:
  │     post_message(to=owner driver/role, kind="escalation", ref="job:<id>#<iid>")
  │     → driver 轮询取到 → bridge_answer_interaction(...) 人工/agent 作答
  └─ type==choice 且带可枚举 options 且命中白名单(role 许可 / 默认或指定选项)  → 自动答:
        AnswerInteraction(id,iid, 选定 option.value)
        → 审计(E13)标记 answered_by=auto:<policy>;配额(E17)计
  └─ AnswerInteraction 返回 ErrJobTerminal(僵尸 pending) → 静默跳过 + 对账该行为 cancelled
job 被唤醒续跑(现有 WaitAnswer→close channel 链路不变)
```

要点（含复审收紧）：

- **自动答能力边界（诚实预期）**：规则 answerer **只可靠自动答"choice + 带可枚举 options"**（选默认/指定项）；**自由文本 question 与无 options 的 choice 默认升级**——不替 job 编造自由文本答案。故 L4 MVP 的价值在**发现 + 路由 + 升级**，自动答覆盖率本就低；真能答自由问题的是可选派生监督 agent（§8.4）。
- **E8 审批门 = `confirmation` 一律升级**：高危动作前 agent 创建 `confirmation` interaction → answerer 拒答 → 必到人。
- **跨 job pending 必须对账**（复审 #4，含修既有缺口）：① `ListPendingInteractions` **JOIN jobs 过滤掉终态 job**（终态 job 的 pending 行是僵尸，不入活跃队列）；② **修既有 bug**：job 终态（`finish`/`cancel`）时把残留 pending interaction 对账为 `cancelled`（现 `InteractionCancelled` 常量从未被赋值，pending 行在 job 结束后永久滞留）；③ serve 启动一次 sweeper 兜底崩溃残留；④ answerer 遇 `ErrJobTerminal` 静默跳过。
- **answerer 经 `job.Service` 既有 API** 作答——对 job 包零侵入；worker 远端 job 的 interaction 已在 host 侧权威（E28/worker 现状），自动覆盖。
- **可接管**：人可暂停 answerer poller（config 开关 / 运行时）；所有自动答留审计痕（D3/横切）。

### 8.4 监督 agent 形态（D4 分层）

- **MVP = serve 内置规则 answerer**（§8.3 的 poller+策略），最可控。
- **可选高级 = 派生监督 driver agent**：把"难答的 pending"经 escalation 投给一个**监督 driver agent**（它就是 §8.1 注册的 driver，role=`supervisor`），由它 reason 后 `bridge_answer_interaction`——即"用一个 agent 答另一个 job 的提问"。**复用 L1/L2，无新执行模型**；硬约束：只见白名单内、最大轮次、E17 配额、E13 审计。本文给出位置，细化留 plan/后续。

### 8.5 角色化运行（E35）

```
job run --role reviewer  (或 bridge_run_job(role="reviewer"))
 → submit 解析 role: roles[reviewer] = {agent=claude, system_prompt, 默认 project/tags}
 → 缺省字段用 role 填充(agent/project/tags);显式入参优先
 → 若 resolved agent 有 SystemInject 且 system_prompt 非空:
     argv = append(argv, agent.Render(SystemInject, Vars{SystemPrompt: ...}))   // 类比 SessionInject
 → 正常提交执行
```

claude: `system_inject: ["--append-system-prompt", "{{system_prompt}}"]`；codex 用其等价参数（plan 时确认）。`agent.Vars` 需新增 `SystemPrompt` 字段供模板渲染。

> **resume × role（复审 #5，必须处理）**：`--append-system-prompt` 是**调用时**参数、不随 claude session 持久化，每轮须重传。但现 `ResumeJob`（`resume.go`）走 exec 载体 + 仅渲染 `SessionResume`，**绕过 SystemInject** → resume 一个 role job 会**静默丢失 role 的 system_prompt**。故源 job 须记录 role/system_prompt，`ResumeJob` 把 `SystemInject` 重渲染进 resume argv（plan 时确认 claude/codex resume 是否确需重传 system prompt）。

## 9. 数据模型（新增两表，interactions 不动）

> SQLite，风格对齐现有 `jobstore`（additive 迁移、upsert、读 COALESCE）。

### agent_presence

```sql
CREATE TABLE IF NOT EXISTS agent_presence (
  agent_id      TEXT PRIMARY KEY,   -- serve 分配 uuid(公开寻址地址);≠ jobs.session_id(D6)
  agent_token   TEXT NOT NULL,      -- per-agent 能力句柄(私有);poll/deregister 校验(D7 软隔离)
  name          TEXT NOT NULL,      -- 自报显示名 (reviewer-A)
  role          TEXT,               -- 关联 roles 段(可空)
  project_key   TEXT,               -- 所在项目(可空)
  caller_id     TEXT,               -- 注册时 caller(provenance, E34;同 token 多 driver 同一 caller)
  client        TEXT,               -- 主机/地址
  status        TEXT NOT NULL,      -- online | offline(懒判定:last_seen 超 TTL)
  registered_at INTEGER NOT NULL,
  last_seen_at  INTEGER NOT NULL,   -- 每次 poll 刷新;TTL 过期判 offline
  meta_json     TEXT                -- 扩展(driver 可选自报外部 session ref 等)
);
```

> presence 行需周期 prune（懒判定只算 status、不删行；offline 超阈值的行由 sweeper GC）。

### messages（信箱）

```sql
CREATE TABLE IF NOT EXISTS messages (
  id          TEXT PRIMARY KEY,     -- uuid (一收件人一行,见下 fan-out)
  to_agent    TEXT NOT NULL,        -- 落地后恒为具体 agent_id(投递时已解析)
  from_agent  TEXT NOT NULL,        -- 发件 agent_id | "system" | "job:<id>"
  to_spec     TEXT,                 -- 原始寻址: agent_id | "role:<name>" | "role-one:<name>" | "broadcast"(留痕/回执)
  kind        TEXT NOT NULL,        -- task | note | answer | escalation
  body        TEXT,                 -- 消息内容
  ref         TEXT,                 -- 关联引用: job:<id> / job:<id>#<iid> / msg:<id>(可空)
  status      TEXT NOT NULL,        -- unread | read
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER,              -- TTL:过期未读不再投递(见下不可达处理)
  read_at     INTEGER
);
CREATE INDEX IF NOT EXISTS idx_messages_inbox ON messages(to_agent, status, created_at);
```

**投递 = post 时 fan-out 成多行**（复审 #7：单 status 列无法表达"A 已读 B 未读"，故按收件人**展开为每人一行**，各自独立 status）：

- **投递策略定稿（2026-06-29）**：`to_agent` 为具体 `agent_id` → 1 行；`role:<name>` → 命中 presence 中 **online 同 role** 的**每个** agent 各 1 行（fan-out / 通知全员）；**`role-one:<name>` → online 同 role 中**随机取一个** agent 1 行（工作分派，近似均衡，`crypto/rand` 取一）；`broadcast` → 每个 online 各 1 行（限频）。
- `to_spec` 保留原始寻址用于留痕/回执。
- **不可达处理定稿（2026-06-29，best-effort）**：① **直投具体 agent_id**（在线或离线）→ 落该 agent inbox，**store-and-forward 到 message TTL**（离线 agent 上线 poll 即领取）；未知 agent_id → 0。② `role:`/`role-one:`/`broadcast` 当时**无 online 收件人** → **不留库，`delivered=0` 作回执**返回，发送方/supervisor 据此重试（**不做 role 上线补投**——避免额外 pending 队列/重解析复杂度，YAGNI；直投已覆盖"离线领取"语义）。message 设 TTL，过期未读由 prune sweeper 清理。
- **可配置（2026-06-29 收尾）**：online TTL / message TTL / prune 周期默认 90s / 24h / 60s，可经 config `presence: {ttl_sec, message_ttl_sec, prune_interval_sec}` 覆盖（<=0 回落默认；serve 启动读取，改需重启非 SIGHUP）。锚点 `config.PresenceConfig` + `presence.Service.Configure`。

> 跨 job pending 不新建表：`SELECT * FROM interactions WHERE status='pending'`（interactions 已持久化 status，pending 行 create 时即落库）。

## 10. API / 契约

### 新增 `/v1` 端点（复用 auth/quota 中间件）

| Method | Path | 说明 |
|---|---|---|
| POST | `/v1/agents/register` | driver agent 注册/续约 → 返回 `{agent_id, agent_token}`；幂等（同 name+caller 续约刷 last_seen，复用同 agent_id） |
| GET | `/v1/agents/presence` | 在线名册（懒算 status；可按 `?role=`/`?project=` 过滤）；**不返回 agent_token** |
| POST | `/v1/agents/{id}/inbox/poll` | **校验 agent_token** → 取该 agent unread + 刷 last_seen + 标 read（或 `?ack=false` 只读不标） |
| POST | `/v1/messages` | 投递 `{to, kind, body, ref}`，post 时 fan-out 多行（§9）；from 由 caller/agent_id 盖章 |
| GET | `/v1/interactions?status=pending` | **跨 job 列 pending interaction**（L4 监督发现；**JOIN jobs 过滤终态 job**，仅活跃 job 的 pending；带 `job_id`） |
| POST | `/v1/agents/{id}/deregister` | 主动下线（**校验 agent_token**；否则靠 TTL/prune） |

> 作答仍用现有 `POST /v1/jobs/{id}/interactions/{iid}/answer`（不新建，answerer/人共用）。

### 新增 mcp `bridge_*` 工具（Backend 接口扩 5 方法，沿 E28 双实现）

| 工具 | → 端点 | local / client |
|---|---|---|
| `bridge_register` | `POST /v1/agents/register` | local 直写 presence；client 转发 |
| `bridge_poll_inbox` | `POST /v1/agents/{id}/inbox/poll` | 同上 |
| `bridge_post_message` | `POST /v1/messages` | 同上 |
| `bridge_list_presence` | `GET /v1/agents/presence` | 同上 |
| `bridge_list_pending_interactions` | `GET /v1/interactions?status=pending` | 监督 driver 用；client 转发 |

> 现有 10 个 `bridge_*` 不变；新增 5 个挂同一 Backend 接口。view 投影（snake_case）留 mcpserver，两后端共用（E28 模式）。

### internal/client 补方法

`Register(name,role,project)`/`PollInbox(id,ack)`/`PostMessage(to,kind,body,ref)`/`ListPresence(filter)`/`ListPendingInteractions()`——端点新增、客户端方法配套（client 模式转发）。

## 11. 安全 / 约束

- **身份与隔离（复审 #2 修正，如实表述）**：注册/收发经 bearer token（沿用，E28/CLI 一致）；但同机多 driver **共用同一 token → serve 端单一 caller_id**，鉴权层**无法区分 driver**。故：`agent_id` 由 serve 分配且**公开可发现**（寻址必需）；register 另返 **`agent_token`**（私有句柄），`poll/deregister` 校验之 → 提供 driver 间**软隔离**（防误读/误删他人 inbox）。**这不是跨信任域硬隔离**：同 token 的 driver 互信，持有 token 即可冒充注册。provenance（E34）记 `caller_id`/`client`。
- **限频（粒度如实标注）**：`post_message`/`broadcast` 接 E17 配额防刷；但 E17 按 **caller** 计 → 同 token 多 driver 共享配额，**粒度到 caller 非单 driver**（一个 driver 可耗尽全体配额）——MVP 接受，更细粒度留后续。register 续约幂等不放大。
- **L4 自主安全闸（D3/D4）**：① `confirmation`/高危规则/超轮次 **一律升级人**（E8 表达）；② 自动答仅白名单（type+prompt regex+role 许可）；③ 每条自动答入 **E13 审计**（`answered_by=auto:<policy>` vs `human`/`agent:<id>`）；④ 监督烧 token 受 **E17 配额**；⑤ 人可暂停 answerer poller（接管）。
- **角色注入安全（SR403）**：`system_prompt` 经 **argv 模板**注入（类比 SessionInject，保 argv 结构、**不 shell 拼接**、无注入面）；roles 段/system_prompt **不含 secret**；规则/上下文文件挂载属 E11 territory（本文不展开）。
- **依赖防环（G022）**：`mcpserver`→`internal/client`；`supervisor`→`job.Service`（接口）；presence 数据层不反向 import 编排/入口。
- **stdout 洁净**：mcp 全程不打印 stdout（协议通道，E28 铁律）。

## 12. 待确认 / 留后续

- **SSE 主动推送 inbox**：MVP 靠 driver 轮询 `poll_inbox`；agent 实时收推送（serve→agent SSE/ws）留二期。
- **Web 控制台显示 presence/inbox**：属 Web v2 写/交互层，独立设计（与 E30 pty/E31 配置编辑同批）。
- **派生监督 driver agent**（§8.4 高级形态）细化：轮次/配额/套娃熔断参数 → 排 plan 时定。
- **IM answerer(E22c)** 接入同一 escalation 机制（升级到 IM 卡片）→ 与自主化 epic 协同。
- **完整 E8 审批门**独立 design（本文仅 `confirmation` 升级表达）。
- **roles 规则/上下文文件**（E11 注入包）与 role 的合并边界。
- ~~**`role:` 投递策略**（投全部 vs 取一 vs 负载均衡）；**不可达消息**是上线补投 vs 回执 vs 仅 TTL 过期~~ → **已定稿 2026-06-29**：`role:`=投全部（通知）、新增 `role-one:`=取一（工作分派，随机近似均衡）；不可达 best-effort——直投 store-and-forward 到 TTL、role/broadcast 无在线返回 `delivered=0` 回执由发送方重试（不做上线补投）。见 §9。锚点 `internal/presence/service.go` resolveRecipients。
- **presence TTL**（默认 90s）与心跳间隔、prune 阈值确认；message TTL 默认值确认。
- **既有缺口修复范围**（复审 #4）：job 终态对账残留 pending interaction（`InteractionCancelled` 现从未赋值）是本 epic 顺带修，还是单独小 issue——倾向随 P3 一并修（L4 依赖跨 job pending 可靠）。
- ~~**resume × role**（复审 #5）：claude/codex `--resume` 是否确需重传 system_prompt~~ → **已实测定稿 2026-06-29**：**两端均原生恢复**——claude-cli 2.1.191 `--resume <sid>` 恢复 `--append-system-prompt`、codex-cli 0.142 `codex exec resume <sid>` 恢复 `-c developer_instructions=`（均 marker 验证 + 负对照）。故 `ResumeJob` **去掉重施**（重施反而会双重注入）。锚点 `internal/job/resume.go`。
- ~~**codex system_inject 等价参数**（§3/§4 注）~~ → **已实测定稿 2026-06-29**：codex-cli 0.142 无 `--append-system-prompt` argv flag，但 `-c developer_instructions=<p>`（developer message 覆盖；`instructions` 亦可）**行为确认生效**（marker 在 + 负对照无），且 `-c` 值按 TOML 失败兜底为字面量 → 引号/`=`/`[...]`/换行/`[`开头的 role 提示**全部鲁棒**。故 codex `SystemInject = ["-c", "developer_instructions={{system_prompt}}"]`（`Render` 保持单 argv 元素、仅 role/system_prompt 非空时触发，不影响普通 codex job）。锚点 `internal/agent/registry.go`。
- **agent_token 粒度**：是否值得给每 driver 独立 token 入鉴权层（彻底硬隔离 + per-driver 配额），还是停在软隔离——MVP 软隔离，按需升级。

## 13. 结论

四层叠加把多 agent 协作收敛为**一套"带寻址的消息/交互，由可插拔 answerer 作答"**：信箱(messages)承协作、interaction(不动)承 job 提问、二者经 answerer 概念统一。新增面集中且低耦——两张 additive 表 + 5 个 mcp 工具 + 6 个 `/v1` 端点 + config `roles:` 段 + 一个 `system_inject` 模板 + 一个分层 answerer，**复用** E28 双后端 / E33 身份 / E13 审计 / E17 配额 / 现有 interaction 与 worker 转发链路。E8 以"拒答 confirmation"轻量表达、监督以"规则+升级"半自动定位，守住 gofer 人在环路的安全取向。

经对抗复审收紧（v0.2）：agent_id 与 jobs.session_id **解耦**（不蹭 job 会话）、共享 token 下加 **agent_token 软隔离**并如实标注配额粒度、L4 自动答**收窄到 choice+options**（诚实预期覆盖率低、价值在路由升级）、**顺带修既有缺口**（job 终态对账残留 pending interaction，`InteractionCancelled` 现从未赋值）、resume 重施 role system_prompt、messages 按收件人 fan-out + TTL/不可达回执 + presence prune。

**落地节奏（建议排 plan）**：**P1 L2 身份/信箱(E36)** → **P2 L3 角色(E35)** → **P3 L4 监督应答(E25，含跨 job pending 端点 + 分层 answerer)**；每 P 自带 mcp 工具 + 端点 + client + CLI 一条龙，独立可验。
