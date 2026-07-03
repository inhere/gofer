# Web 交互应答 / operator token（WEB-09）使用 Runbook

> 配套 design [`../design/2026-07-02-web-console-v3-design.md`](../design/2026-07-02-web-console-v3-design.md)（§6.5 / §13）/ plan [`../plans/2026-07-02-web-console-v3/P2-interaction-write-plan.md`](../plans/2026-07-02-web-console-v3/P2-interaction-write-plan.md)。P2 写层：EscalationBell 内联应答 + JobDetail punt + answered_by 审计归属 + 可选 can_answer 闸。

## 是什么

Web 控制台从 P1 的**只读**升级 escalation 通知为**可就地应答**：

- **就地应答**：右下角 EscalationBell 下拉对 `choice` / `confirmation` 直接出按钮应答，`question` 引导进 JobDetail；JobDetail 卡片同样可应答 + **punt**（留给人工）。
- **审计归属**：Web/CLI 人工应答的 `answered_by` 由 **serve 端盖认证 caller_id**（防伪，不采信客户端字段），且**不触发** agent 派生作答闸。
- **可选能力闸**：opt-in 的 `can_answer` 能力位，可把「替 agent 应答 / 放行」收敛到指定 operator token。

## answered_by 审计语义

应答落库的 `answered_by` 按来源区分，Web/CLI/JobDetail 均展示：

| 来源 | answered_by 值 | 说明 |
|---|---|---|
| Web/CLI 人工（认证 caller） | 裸 `caller_id`（如 `web-op`） | serve 端从 bearer→caller 盖，客户端无法伪造 |
| Web/CLI 人工（空 token / 匿名部署） | `human` | 无 caller 身份时退回 |
| MCP/agent 驱动应答 | `agent:<agent_id>` | 经派生作答闸分级（owner/supervisor 白名单 choice） |
| L0 内置规则应答 | `auto:<policy>` | 规则自动答 |

punt（留人工）不改 `answered_by`，仅置 `needs_human=1`，并写一条 `interaction.punted` 事件（`{interaction_id, caller_id}`）到 job 事件流，可经 `GET /v1/jobs/{id}/events` 审计发起者。

## 配置 operator token（推荐）

给「可应答」的人配一个独立 caller token，secret 走环境变量（不入配置文件）：

```yaml
server:
  callers:
    - id: web-op
      token_env: GOFER_WEBOP_TOKEN   # 值从环境变量读，不写明文
      can_answer: true               # 具备应答/放行能力
    - id: ci-bot
      token_env: GOFER_CI_TOKEN      # 只提交任务、不应答（无 can_answer）
```

- `can_answer` 仅在下方能力闸开启时才**强制**；默认闸关时它只是一个声明位（任何认证 caller 都能答）。
- Web 前端应答**不传 responder**，serve 以该请求认证的 caller_id 盖 `answered_by`——所以让操作者用 `web-op` 的 token 登录控制台，审计即可追溯到人。
- **区分多个操作者**：`answered_by` 落的是该请求 token 映射的 caller_id。若共用一个 token（如只配一个 `web-op`），所有人应答都记成同一个 id（如 `default`）；要在审计里区分「谁答的」，给**每个操作者各配一条独立 caller**（各自 `id` + 各自 `token_env`），各人用自己的 token 登录，`answered_by` 即落各自 id。控制台**不引入独立用户 / 登录体系**（design §11），身份完全由 caller token 承载。

## 开启 can_answer 能力闸（可选，纵深防御）

默认**不开**（保持向后兼容：任意认证 caller 可答）。要把应答/放行收敛到 operator token：

```yaml
server:
  governance:
    require_answer_capability: true   # 开闸：仅 can_answer:true 的 caller 可答/punt
  callers:
    - id: web-op
      token_env: GOFER_WEBOP_TOKEN
      can_answer: true
```

- 开闸后，无 `can_answer` 的 caller 调应答/punt 端点返回 **403**（前端就地提示，不崩下拉）。
- **防锁死**：开闸但没有任何 caller `can_answer: true` 时，serve **加载即 fail-fast** 报错（避免全员无法应答）。
- 闸基于请求认证身份统一判断，MCP/agent 路径同受约束——若让 mcp 后端也能应答，其 caller 也需 `can_answer: true`。

## 安全要点（SR201-204 对齐）

- 所有应答/punt 写端点在**已鉴权** `/v1` group（bearer→caller_id），非匿名。
- `answered_by` 由 serve 端盖 caller_id，**不采信客户端传入**做人工归属（防伪）。
- `can_answer` 闸为纵深防御，把「替 agent 决策 / 放行」限定到 operator token。
- 未引入 web 会话 / 用户体系（design §13 反面）；写操作经认证 caller 即可审计追溯到人。

## 冒烟验证

1. 起带交互的 job（或 sup punt）造一个 `pending_interaction`。
2. 用 `web-op` token 登录控制台 → EscalationBell 或 JobDetail 就地应答 → job 续跑、计数回落、`answered_by` 显示 `web-op`。
3. punt 一条 → 卡片转 `needs_human`，`GET /v1/jobs/{id}/events` 见 `interaction.punted{caller_id:web-op}`。
4. （开闸时）用无 `can_answer` 的 token 应答 → 403，前端就地提示。
