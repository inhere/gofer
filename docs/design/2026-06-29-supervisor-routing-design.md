# Gofer 监督应答 · 分层升级路由 设计

> 本文从 `2026-06-28-multi-agent-collab-design.md` §8.4「监督 agent 形态」**独立细化**而来。
> collab design 把"派生监督 driver agent"标为"给出位置、细化留 plan";实测发现其基础设施已约 70% 就绪，
> 真正缺口是**路由模型**与**安全闸**——这部分一旦做偏，后续增强（自主化、IM 接入、web 写层）会全部跟着歪，
> 故拎出独立 design 作为长期对齐基准。collab design §8.4/§8.6 相关段落以本文为准。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-29 | inhere | 初稿：确立「按上下文分层的升级路由」模型，定位两类 supervisor，定三道安全闸 |

## 2. 背景 / 动机

job 执行中 agent 会产生 **pending interaction**（向人提问：choice/confirmation/自由文本 question），阻塞等待应答。
若全靠人答，多 agent 并发编排时人会成为瓶颈；但若交给一个"通用自动应答器"全权代答，又会在**需要业务上下文的问题**上答错、带偏整条实施链。

核心矛盾：**同一个 pending interaction，正确答案落在哪里，取决于问题是否依赖前因后果**——

- 「Overwrite existing file?」「Continue?」「选哪种输出格式」——**上下文无关**，谁答都一样。
- 「这个字段该用 snake_case 还是缩写」「A/B 两个方案选哪个」——答案**只存在于发起调度的主 agent 的上下文里**（它持有 plan/design 全貌）。一个冷启动、不知前因后果的通用应答器在这种问题上会"自信地答错"。

既有内置规则 answerer（`internal/supervisor`，正则白名单）只能处理第一类的子集（低危 choice）。本设计要把"谁来答"按上下文能力**分层路由**，让每类问题落到**有能力正确回答**的那一层，并保证任何一层答不了都能安全地向上兜底，最终落到人。

## 3. 名词

| 名词 | 含义 |
|---|---|
| **job agent** | 被 gofer 派去跑**一次性 job** 的 agent（`gofer job run` 拉起的 claude/codex）。执行完即结束。 |
| **driver agent** | **长在线**、用 MCP 主动驱动 gofer 的协作主体（你这个 Claude Code 会话、常驻 supervisor）。登记在 `agent_presence`。 |
| **presence** | 在线 driver agent 名册（`agent_presence` 表）。靠 90s 心跳 TTL 判 online/offline，使消息可寻址。 |
| **interaction** | job 执行中产生的待答提问（`interactions` 表，`pending/answered/cancelled`）。 |
| **escalation** | 某 interaction 被判定"本层答不了" → 经 `messages` 信箱投递给上一层应答者的消息（`kind=escalation`，`ref=job:<id>#<iid>`）。 |
| **origin agent / owner** | **发起该 job 的主 agent**（派活的编排者）。本设计**新增**：job 记录它，escalation 优先回投它。 |
| **通用 sup agent** | server 托管、常驻的 `role=supervisor` driver agent。答**上下文无关**的通用决策，是 owner 之外的兜底层。 |
| **内置规则 answerer** | `internal/supervisor` 现有的正则白名单应答器（serve 内 goroutine，非 agent）。最底层、最确定的一道。 |

## 4. 范围

**做（本设计覆盖）**：
- 确立分层升级路由模型（§6）与 owner-first 路由（§8.1）。
- job 记录 `origin_agent` + 可选 `escalate_to`，escalation 由"一刀切 role:supervisor"改为 **owner-first → 通用 sup → 人** 的分层 fallback。
- 通用 sup agent 由 **server 调度托管**（而非独立 `gofer supervisor run` CLI 守护进程）。
- 三道安全闸（§10）：白名单约束派生作答 / 套娃熔断（non-interactive）/ owner 超时兜底。

**不做（留后续，§11）**：
- web 显示/操作 presence·inbox（属"Web v2 写/交互层"，独立 design）。
- 实施 agent 双向接收主 agent **主动 push 指令**（MVP 只做单向：实施 agent 问 → owner 答）。
- IM answerer 接入 escalation（与自主化 epic 协同）。
- 动态 role CRUD、多通用 sup 负载均衡的精细策略。

## 5. 已确认事项（本次讨论定稿，作为长期约束）

1. **两类 supervisor 不是二选一，是同一条升级链的不同层**：owner（有上下文，主路径）与通用 sup（无上下文，兜底）各司其职。
2. **通用 sup 用 server 托管、可派任意节点**，复用 job 调度，不另起 CLI 守护进程。
3. **owner-first 是核心新增**：能力最强的应答者是发起 job 的主 agent，escalation 必须优先回投它。
4. **owner 是会话式、非常驻** → owner 在线但超时未答必须自动 fallback，否则编排者一下线子 agent 全卡死。
5. **通用 sup 必须 non-interactive**：它自身产生的任何 interaction 直接升级人，绝不回投 supervisor inbox（防死循环套娃）。
6. **派生作答（经 `bridge_answer_interaction`）必须受白名单约束**：当前此路径完全不过白名单，是必须堵的安全缺口。
7. **MVP 单向**：实施 agent 通过 interaction 提问、owner 应答即可；主 agent 主动 push 指令留后续。

## 6. 核心思路：按上下文能力分层的升级路由

把"谁来答 pending interaction"建模为一条**自下而上、能力递增、答不了就兜底**的链。每一层只接它**有能力正确回答**的问题，其余向上交。

```
job 产生 pending interaction
  │
  L0  内置规则 answerer（正则白名单）         ── 上下文无关·低危 choice          [已实现]
  │      不命中 / confirmation / 自由文本 / 超轮次 ↓
  L1  owner-first：job.origin_agent 在线？       ── 有完整 plan 上下文 → 准确答    [本设计核心新增]
  │      owner 离线 / 超时 N 分未答 ↓
  L2  通用 sup agent（server 托管, role=supervisor）── 上下文无关·中低危 LLM 判断  [server 托管化]
  │      答不了 / 高危 / 超时 ↓
  L3  人（IM / web / CLI）                        ── 终极兜底                       [已有 escalate 投递]
```

要点：

- **L0 与 L1 顺序**：L0 是确定性正则、零成本，先跑；它只敢答明确低危的，其余统统下放。L1 才是"主路径"——大多数需要判断的提问应由 owner 答。
- **L1 是能力天花板**：owner 持有最完整上下文，是唯一能正确回答"业务相关"提问的层。L2/L3 是它不可用时的降级。
- **每层都"答不了就向上"**，绝不横向死等；最终一定收敛到人（L3），不存在"悬死"。
- **L0 已实现，L3 投递已通**；本设计的工程主体是 **L1（owner-first 路由）** 与 **L2 的 server 托管化**，外加贯穿各层的三道安全闸。

## 7. 架构总览

```
┌──────────────────────────────────────────────────────────────┐
│  主 agent（owner，会话式，你）                                  │
│   bridge_run_job(origin_agent=self)  ──派活──┐                  │
│   bridge_poll_inbox  ◀── escalation(L1) ──┐  │                  │
│   bridge_answer_interaction ──应答──┐      │  │                  │
└─────────────────────────────────────┼──────┼──┼────────────────┘
                                       │      │  │  (MCP / /v1, 经 serve)
┌──────────────────────────────────────┼──────┼──┼────────────────┐
│  serve                                ▼      │  ▼                │
│   job.Service ── 创建 interaction ──▶ interactions 表            │
│   supervisor.Service(L0 规则器 + 路由器)                         │
│     decide(): L0 白名单 → 否则 escalate                          │
│     escalate(): owner-first 路由 ──▶ presence.Post              │
│   presence.Service ── resolveRecipients ──▶ messages(inbox)     │
│   (P2) sup reconciler ── 确保通用 sup job 常驻 ──▶ dispatch      │
└───────────┬───────────────────────────────────┬────────────────┘
            │ dispatch                           │ dispatch
┌───────────▼──────────┐            ┌────────────▼───────────────┐
│ 实施 job agent        │            │ 通用 sup agent（L2）         │
│ （任意 runner 节点）   │            │ role=supervisor, 任意节点     │
│  卡住 → 创建 interaction│           │ non-interactive             │
│                       │            │ loop: poll_inbox → reason   │
│                       │            │   → bridge_answer_interaction│
└───────────────────────┘            └─────────────────────────────┘
```

与 collab design 四层叠加的关系：本设计**复用** L1 通道(E28)/L2 身份信箱(E36)/L3 角色(E35)，只在 **L4 监督应答(E25)** 内部把"单一 escalate_to"升级为"分层路由"，并把派生 sup 的部署方式定为 server 托管。

## 8. 关键流程

### 8.1 owner-first 路由（核心）

改造点：`supervisor.escalate`（现 `internal/supervisor/service.go:228`，一刀切投 `s.policy.EscalateTo`）。新逻辑按序解析收件人：

```
escalate(it):
  job := jobs.Get(it.JobID)
  targets := []                                   # 有序候选
  if job.OriginAgent != "" && presence.Online(job.OriginAgent):
      targets += "agent:" + job.OriginAgent       # L1：直投 owner（store-and-forward，离线也先落）
  if job.EscalateTo != "":  targets += job.EscalateTo   # job 级覆盖（可选）
  targets += policy.EscalateTo                     # L2：全局通用 sup，默认 role-one:supervisor
  # 逐个尝试，首个 delivered>0 即停；都不可达 → 标记 NEEDS_HUMAN（L3 由 web/IM/CLI 捞起）
```

- **owner 直投用 `agent:<id>`**：直投具体 agent_id 是 store-and-forward（presence 探查证实：离线也落 inbox、上线 poll 领取），所以 owner 短暂离线不丢；配合 §8.2 超时决定是否 fallback。
- **通用 sup 默认 `role-one:supervisor`（取一）而非 `role:`（投全部）**：多个通用 sup 在线时 `role:` 会让它们**重复抢答同一 interaction**；`role-one:` 取一即可（presence 已支持，crypto/rand 近似均衡）。
- 同一 interaction 仍保留 dedup（现 `escalated` map），避免多 tick 重复投。

### 8.2 owner 超时兜底（必做，防悬死）

owner 在线 ≠ owner 会答（会话式 agent 可能已结束/compact）。引入**每 interaction 的 escalation 时效**：

```
escalate 时记 escalated_at；后续 tick 检查：
  若 interaction 仍 pending 且 (now - escalated_at) > owner_answer_timeout_sec:
      → 重新 escalate，跳过 L1，直接投 L2 通用 sup（再超时 → L3 人）
```

`owner_answer_timeout_sec` 进 supervisor config（建议默认 300s）。这条是 §5.4 的落地：保证 owner 下线后链路继续向上收敛。

### 8.3 通用 sup agent 的 server 托管（替代 CLI run）

通用 sup **本质是一个长生命周期的 agent job**：prompt 即"你是 supervisor，循环 `bridge_poll_inbox`，对 escalation 做通用判断后 `bridge_answer_interaction`；拿不准的不要猜，留给人"。它通过 agent 内置 gofer MCP 操作。

- **MVP**：用文档化的"常驻 daemon job"起（`gofer job run -a codex --role supervisor ...` + 长 timeout / 无 timeout），手动或脚本拉起；serve 不介入生命周期。先验证 L2 闭环。
- **P2（你要的"server 启动、任意节点"）**：serve 内置一个 **sup reconciler**——config 声明 `desired supervisor`，serve 经 `job.Service.Submit` 自构造 sup job 投 dispatch（可指定/任选 runner 节点），挂了重派（desired=1，类比 K8s Deployment）。复用现有 dispatch，节点无关、自愈。
  - 落地依赖：job 需支持"长生命周期/不被 timeout 杀"的形态（待确认 §11）。

### 8.4 套娃防护（通用 sup 必须 non-interactive）

通用 sup job 自身 reason 时若触发 interaction，**绝不能**再被路由回 supervisor inbox（自己问自己 → 死循环）。约束：

- 通用 sup job 以 **non-interactive** 配置跑（codex `approval_policy=never`/只读；agent 侧不发起需人确认的动作）。
- 兜底：路由器识别"来源 job 自身是 supervisor 角色"的 interaction → 一律直接 L3 升级人，永不进 L0/L1/L2 自动答路径。

### 8.5 派生作答的白名单约束（堵安全缺口）

当前 `bridge_answer_interaction` 让任何 driver 答任何 interaction、**不过白名单**。改造：

- L2 通用 sup 经 MCP 作答时，serve 侧对其应答做**与 L0 同源的白名单/范围校验**（type 限定 + `allow_prompt_regex` + 角色许可）；高危（confirmation/删除/外发/自由文本）即便 sup 想答也**强制升级人**。
- L1 owner 作答**不受此限**（owner 是人类授权的编排者代表，持完整上下文，等同人答）；二者经 `answered_by` 区分留痕。

## 9. 数据模型

**jobs 表新增两列**（与现有 `channel`/`client` provenance 列同构；`internal/jobstore/store.go:60`）：

```sql
ALTER TABLE jobs ADD COLUMN origin_agent TEXT;   -- 发起该 job 的主 agent agent_id（owner，L1 路由用）
ALTER TABLE jobs ADD COLUMN escalate_to  TEXT;   -- 可选 job 级 escalate 覆盖（缺省走全局 policy）
```

> 取舍：origin_agent 作为**独立列**（而非塞进 `request_json`），因为它是路由热路径查询字段、需随 interaction 升级快速取到。

**JobRequest / runJobInput 新增字段**（`internal/job` + `internal/mcpserver/server.go:267`，沿 provenance 注入方式透传）：
- `origin_agent`（MCP handler 可由 caller 的 agent_id 自动填，或显式传）、`escalate_to`（可选）。

**interaction 升级态（折中档已定，疑问2）**：现 `escalated`/`rounds` 是 supervisor 内存 map，serve 重启后会重复 escalate 活跃 job 的 pending。定档：
- **`interactions` 加 `escalated_at INTEGER`**——escalate 写、tick 读，承载 dedup + owner 超时计时 → 跨重启不重复投、超时窗口连续；副带升级历史可查。
- `rounds`(jobID 计数) **仍内存**（重启重置=多升几轮、偏安全方向，不值得加聚合查询），全落表留完整版。见 plan P1.1/P1.2/P2.1。

**messages（escalation 消息）**：复用现有表，`kind=escalation`、`ref=job:<id>#<iid>`、`to_spec` 记录命中的层（owner/role-one:supervisor）便于审计。

**supervisor config 新增**（`internal/config/model.go:54` `SupervisorConfig`）：
```yaml
supervisor:
  escalate_to: "role-one:supervisor"   # 默认改 role-one（取一，防重复抢答）
  owner_answer_timeout_sec: 300        # L1 owner 超时 → fallback L2（新增）
  desired_supervisors: 0               # P2：server 托管常驻数（0=不托管，靠外部起）
  # allow_prompt_regex / max_rounds_per_job / auto_answer 沿用
```

## 10. 安全 / 约束

| 闸 | 约束 | 对应 |
|---|---|---|
| 白名单约束派生作答 | L2 经 MCP 作答过 type+regex+角色校验；高危强制升级人 | §8.5 / 已确认 §5.6 |
| 套娃熔断 | 通用 sup non-interactive；其自身 interaction 永不进自动答 | §8.4 / 已确认 §5.5 |
| owner 超时兜底 | L1 超时自动降级 L2→L3，杜绝悬死 | §8.2 / 已确认 §5.4 |
| 审计区分 | `answered_by = auto:<policy>` / `agent:<owner|sup-id>` / `human`，每条入 E13 审计 | collab §11 |
| 配额 | L2 监督烧 token 受 E17 配额；可暂停 sup poller（人接管） | collab §8.4 |
| dedup | 同 interaction 单次投递；落表后跨重启不重复（§9 强化项） | 现 `escalated` map |

## 11. 待确认 / 留后续

- **server 托管 sup 的 job 长生命周期形态**（§8.3 P2）：现有 job 有 timeout 语义，常驻 sup 需"无 timeout / 自续"的 job 类型或 reconciler 周期重派——落地前需定 job 侧支持方式。
- ~~owner 自动填充来源~~ **已实测定论 2026-06-29**：gofer mcp = **stdio per-进程**（每会话一个独立 mcp 子进程，共享 serve/DB）；MCP handler 拿不到连接身份（go-sdk session 钩子在 handler 层不可用，实测确认），故不走"SDK session 自动绑定"，改用 **mcp 进程级自注册**——进程启动自动 `RegisterAgent` 缓存 agent_id、`runJobHandler` 自动注入 `origin_agent`（仿现有 Channel/Client 注入），显式入参覆盖。主 agent 零感知。**name 须 per-进程唯一**（hostname+pid/随机会话 id）防同机多会话串身份。详见 plan P1.0。
- ~~interaction 升级态是否 MVP 就落表~~ **已定（疑问2，折中档）**：`escalated_at` 落表（dedup+超时计时），`rounds` 留内存。见 plan P1.1/P2.1。
- **mcp 工具前缀 `bridge_*` → `gofer_*`**（与项目名对齐）：横切改名，列为 plan **P0**，独立先行/提交；全仓引用（本 design / collab design / architecture-overview / runbook / 测试）随 P0 统一更新。
- **web 显示 presence/inbox**：独立"Web v2 写层"design；本设计仅附带最小只读端点（`GET /v1/agents/{id}/inbox`，调试可见 escalation 堆积）作为可选项。
- **双向 push**（主 agent 主动给实施 agent 发指令）：需实施 agent driver 化（register+周期 poll），留后续。
- **多通用 sup 的取一策略精化**：`role-one` 目前 crypto/rand 近似均衡，未来可按负载/能力路由。

## 12. 结论

把"谁来答 pending interaction"从"单一 escalate_to"重构为**按上下文能力分层的升级路由**：L0 规则器（确定性低危）→ **L1 owner（有上下文·主路径，本设计核心）** → L2 通用 sup（server 托管·无上下文兜底）→ L3 人。两类 supervisor 各归其层、互不取代；每层答不了即安全向上，最终必收敛到人。

工程主体集中且低耦——**jobs 加 2 列 + escalate 路由改写 + owner 超时兜底 + 通用 sup server 托管 + 三道安全闸**，全部复用 collab 已落的 presence/messages/interaction/dispatch，不新增执行模型。守住 gofer"人在环路、可控自治"的取向：自动化是为把人从机械确认中解放，而非让冷启动的应答器替代持上下文者做业务判断。

落地节奏见同名 plan：`docs/plans/2026-06-29-supervisor-routing-plan.md`。
