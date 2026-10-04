# Q 批：Web 请求聚合 + 全局推送

分支 q-batch；设计见 docs/design/2026-10-04-web-request-aggregation-and-live-push-design.md（Q1–Q3 必做，Q4 视时间）。

## 步骤

1. Q1 后端：`GET /v1/jobs/{id}?include=` 聚合（events/comments/deliveries/retries/wakeups/pty_sessions/artifacts/session_jobs），各项上限、单项容错、pty 权限；`TestJobDetailInclude`。
2. Q2 后端：
   - `internal/pushhub`：订阅表、每连接有界队列（64）、队满 resync、合并节流器；`TestPushHubNonBlockingAndResync`、`TestStatsCoalesced`。
   - jobstore 变更钩子（job/decision/interaction/session/plan/workflow/schedule 写后只入队）；decisions 到期扫描；wshub 上下线观察者；事件 tap（带 seq）。
   - `POST /v1/ws-ticket`（拒绝 job 凭据与 worker token）与 `GET /v1/ws`（auth 组外，ticket + Origin，20s ping/60s 读超时，每 caller ≤16 连接）。
   - 测试：`TestWSTicketRejectsJobAndWorkerCallers`、`TestDecisionChangesPublishPending`、`TestDecisionExpirySweepPublishes`、`TestPersistPublishesJobs`、`TestRunnersInvalOnWorkerConnect`。
3. Q3 前端：`api/live.ts`（单连接、退避重连、兜底切换、隐藏 >5min 断开）、`useLiveTopic`、`api/metaCache.ts`（agents/meta 模块级缓存，meta inval 失效）；Vite 代理加 ws；顶栏状态点；`hello.version` 驱动新版本提示。
4. 页面迁移（原轮询降为 30s 断线兜底）：见下表。
5. Q4（时间允许）：SSE 去 DB 轮询，否则列遗留。
6. 同步 skills/gofer-usage 与 README（G045）；质量门；真实冒烟（截图存 tmp/q-smoke/）。

## 页面迁移清单

| 页面/组件 | 主题 | 备注 |
|---|---|---|
| EscalationBell | `pending` + `stats` | 去掉 15s 轮询与 4 轮一次的 stats |
| App 顶栏（待验收数、新版本提示、状态点） | `stats`、`hello.version` | 状态点 = 真实 WS 状态 |
| JobDetail | 单次聚合 + `job:<id>` | 16 请求 → 聚合 + 日志/SSE + ws-ticket |
| Board | `jobs` inval | 原 2.5s 轮询降级 |
| ReviewQueue | `jobs` inval | 原轮询降级 |
| Dashboard | `stats`（+ `jobs`） | |
| Runners | `runners` | |
| Plans / PlanDetail | `plans` | |
| Workflows / WorkflowDetail | `workflows` | |
| Schedules | `schedules` | |
| Sessions / SessionDrawer | `sessions` | |
| Workbench | `sessions` + `jobs` | |
| Agents | `meta` + 缓存 | presence 仍保留低频兜底 |
| Config / Tunnels | `meta` | 兜底轮询 |
| ThreadChangesView | — | 补页面可见性暂停 |

## 验收数字

- 打开有 events/comments 的 job 详情：`/v1/` 请求从改造前 16 降到 3 个以内（聚合 + 日志或 SSE + ws-ticket；日志另计）。
- 任一页面停留 2 分钟：无定时 REST 请求（只有 ws 帧）。
- 提交 job 后 Board 2s 内刷新；interaction 创建/回答后铃铛 2s 内变化。
- kill serve：15s 内状态点显示断开并进入兜底轮询；重启后自动重连、停止轮询。
