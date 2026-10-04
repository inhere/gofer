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
5a. Q5（用户追加，与 sessions 主题一起做）：会话无心跳自动标离线——`session.offline_after_sec`（默认 1800，0 关，热重载）、`offline` 新状态、60s 扫描 + sessions 推送、心跳/重登记恢复、有 OPEN 中继 turn 的会话不误判、可唤醒；Web 灰色「离线」徽标与最后心跳、CLI `session ls`；测试 `TestSessionOfflineSweep` 等。
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
| Config | `meta` | 编辑弹窗打开时跳过刷新 |
| Tunnels | — | 暂无推送源，沿用 createPoller（遗留） |
| ThreadChangesView | — | 补页面可见性暂停 |

## 验收数字

- 打开有 events/comments 的 job 详情：`/v1/` 请求从改造前 16 降到 3 个以内（聚合 + 日志或 SSE + ws-ticket；日志另计）。
- 任一页面停留 2 分钟：无定时 REST 请求（只有 ws 帧）。
- 提交 job 后 Board 2s 内刷新；interaction 创建/回答后铃铛 2s 内变化。
- kill serve：15s 内状态点显示断开并进入兜底轮询；重启后自动重连、停止轮询。

## 结果（2026-10-04）

- Q1–Q3、Q5 全部完成；Q4（SSE 去 DB 轮询）未做，列遗留（`/v1/jobs/{id}/stream` 仍保留每 250ms 的 DB 查询，`job:<id>` 主题已能接管 status/event/interaction，后续只需让流只保留日志帧）。
- 真实冒烟（临时 serve，agent-browser）：详情页 `/v1/` 请求 15 → 4（ws-ticket、一次聚合、两个日志窗口）；Board 停留 2 分钟 66 → 0（两次测量中第一次出现过一次 2 请求的刷新，之后两次均为 0）；提交 job 后 Board、interaction 创建/回答后铃铛均在 0.1s 内更新；kill serve 后约 16s 状态点变「disconnected (polling)」并开始 30s 兜底轮询，重启 serve 后约 16s 自动重连并停止轮询。截图在 tmp/q-smoke/shots/。
- 遗留：Q4；Agents 页的 presence 与 Tunnels 页仍是低频轮询（没有推送源）；Vite 开发代理下 Origin 与代理 Host 不一致，需把开发源加进 `governance.attach_origins`。
