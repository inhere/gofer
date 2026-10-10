# 待批 job（`--hold` / `awaiting_approval`）

> 用途：agent 要做**对外 / 不可逆**的一步（典型是 `git push`、发版、对外发消息），却被自己的权限检查拦下；人又不在电脑前（只有手机 web）。这时让 agent 把这一步交成**待批 job**，人在 web 看清完整命令后点一下「批准」才执行。
> **agent 绝不自己批准**（不调 `job approve`、不调 HTTP approve），只负责提交、等结果。

## 提交

```bash
gofer job run -p <p> -a exec --runner server --hold \
  --hold-reason "推送 feat/x 到 origin（我的权限拦下了 git push）" \
  [--hold-timeout <秒>] [--plan <plan> --todo <todo>] [--title "…"] -- git push origin feat/x
```

- `--hold`：job 入库后停在 `awaiting_approval`（非终态），**不派发、不占并发名额与目录锁**；人批准后才转 `queued` 照常执行（同一个 job id）。agent job（`-a claude` 等）同样可以 hold，审批人看到的是 prompt 开头。
- `--hold-reason`：给审批人看的理由，写清「要做什么、为什么要人批」。
- `--hold-timeout`：等多久没人决定就自动取消；0 / 不给 = `server.hold.default_timeout_sec`（默认 86400 = 24h）；超过 `server.hold.max_timeout_sec`（默认 604800 = 7d）或负数直接报错（400，不截断）。过期时刻提交时就定死。
- 输出两行（不含 `finished`，hook 不会误判结束）：

  ```text
  job <id> submitted: status=awaiting_approval expires_at=<RFC3339> result_dir=…
  awaiting approval: <web>/jobs/<id>
  ```

  把第二行的链接发给用户（server 配了 `server.web_base_url` 时就是对外地址，否则按 CLI 连的地址拼）。
- `--sync` 对待批 job 无效：自动转异步并打印 `note: --sync is ignored for a held job …`。要阻塞等到结束用 `--wait`：等待窗口 = hold 超时 + job 超时，待批期间每 5 秒轮询一次。
- MCP：`gofer_run_job` 带 `hold: true`、`hold_reason`、可选 `hold_timeout_sec`；**没有** approve 工具。HTTP 接口见 <https://github.com/inhere/gofer/blob/main/docs/reference/http-api.md>。

## 人怎么批

- **Web（手机可用）**：打开 `/jobs/<id>`，页首「⏸ 等你批准」面板显示理由、完整命令（exec 的 argv 按 shell 引号拼接；agent 的 prompt 开头）、项目 / cwd / runner / agent、提交者、提交时间与过期倒计时、附带属性（`--review` / `--worktree` / 超时）。「批准」一键执行；「拒绝」可写理由也可不写。首页「今天」与顶栏「待我决策」里有「待批准」卡（批准 / 拒绝 / 看详情）；看板有「⏸ 待批准」过滤；`/review` 页头有「待批准 N →」入口。
- **CLI（人在 agent 会话之外的终端）**：

  ```bash
  gofer job approve <id> [--note "…"]          # awaiting_approval → queued，随后执行
  gofer job reject <id> [--reason "…"]          # → cancelled，命令从未执行；待批时理由可选（--reason 是 --note 的别名）
  ```

  `job approve` 在 agent 会话环境里直接拒绝（`GOFER_SESSION_ID` / `CLAUDE_CODE_SESSION_ID` / `CODEX_THREAD_ID` / `CODEX_SESSION_ID` / `GOFER_JOB_TOKEN` 任一有值），**没有绕过开关**。`job reject --resume` 对待批 job 无效（400）。

## 结果怎么回到提交者

| 情况 | 状态 | `error` | 事件 |
|---|---|---|---|
| 批准 | `queued` → 照常执行到终态 | — | `job.hold_approved {by, note?}` |
| 拒绝 | `cancelled`（未执行） | `hold rejected by <who>[: <理由>]` | `job.hold_rejected {by, note?}` |
| 超时 | `cancelled`（未执行） | `hold expired` | `job.hold_expired {expires_at}` |
| 提交者撤回 | `cancelled`（未执行） | `cancelled while awaiting approval` | `job.cancelled` |

- 提交时记 `job.awaiting_approval {reason, timeout_sec, expires_at, origin}`；它在默认通知集里（IM「待批准：<标题>」带理由、命令前几行、过期时间和「去批准」链接；Web Push 高优先级）。决定类事件不在默认集。
- 提交它的会话照常收到完成通知：Stop hook 的 job watch 带 `status=cancelled … reason=hold rejected by …` 或 `reason=hold expired`，agent 据此知道命令没跑、为什么。
- 撤回：`gofer job cancel <id>`（MCP `gofer_cancel_job`）。MCP `gofer_reject_job` 对待批 job 报错并提示用 cancel——拒绝是人的决定。

## 查看

```bash
gofer job ls --status awaiting_approval      # 谁在等批
gofer job show <id>                          # 多出 hold_reason / hold_origin / hold_expires / hold_cmd 或 hold_prompt；
                                             # 决定后 hold_decision: <approved|rejected|expired|cancelled> by=… at=…、hold_note
```

`hold_origin` 是提交来源：`job:<id>`（job 凭据）、`agent-session:<id>`（agent 会话）或提交渠道（cli / web / mcp …）。

## 组合与限制

- 可叠加：`--review`（执行完照常 `needs_review` 等验收）、`--plan` / `--todo`（待批时 todo 为 doing；拒绝 / 超时会让 plan 停在 blocked）、`--worktree` / `--verify` / `--retry` / `--lock` / `-f` 任务文件 / request_id（都在批准后才解析）。
- 拒绝（400）：`--session`、`--interactive`、workflow 步骤、内部 job（steward / leader / messenger）。
- **所批即所跑**：批准时重算请求摘要（项目、agent、runner、cwd、命令、prompt、只读、worktree、env），等待期间变了就 409「请求变了，请重新提交」。
- **hold 跟着请求走**：`job rerun`、web「快速重建」、自动重试、`job resume`、定时任务重放的都是带 hold 的请求，会**重新进入待批**（rerun 会再打印 `awaiting approval:` 链接）——批过一次不等于以后免批。
- 待批 job 不在内存执行表里，server 重启后仍待批；停机期间过期的在启动时转 `cancelled`。

## 权限：护栏不是安全边界

CLI 在 agent 环境拒绝 approve、MCP 不给 approve 工具，只是护栏：容器里的 agent 往往和人共用同一个用户 token，server 分不清是谁。要真正隔离，按 [server-config.md](server-config.md)「待批 job 的审批权限」的推荐：**给 agent 配独立 token（不带 `can_answer`）**，并开 `server.governance.require_answer_capability: true`，只给人自己的 token `can_answer: true`。
