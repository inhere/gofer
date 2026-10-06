# W2b 批：管家（Steward）（2026-10-06）

分支 w2b-batch，不 push、不合并 main。设计见 `docs/design/2026-10-05-work-items-and-steward-design.md` §9、§14.1、§14.4、§14.5 W2b 行、§14.6、§14.7 W2b 验收、§14.8（W2a 已落地的账本 / 读写接口）。

## 原则（§14.1）

管家只调度与整理，不干活不拍板：不能把工作项设为 done / dropped、不能提交执行类 job、不能改配置、不能删除；派活异步 + 回调（复用 W2a 的 `work_requests` 账本）；改写而非转发；发言者标注 `steward(<agent>)`；关键信息全部落库，管家会话可抛弃，换 agent 后靠 prime 继续。

## 关键决定

1. **凭据层强制**：新增 job 凭据种类 `steward`（与 `member` / `leader` 并列）。`jobCredentialMiddleware` 对 steward 种类走**默认拒绝的双向白名单**——读（GET）和写都只放行下表里的路由，其余一律 403（不只靠 prompt，也不只靠 MCP 不注册工具）。处理函数里再做目标级规则：steward 不能把状态写成 done / dropped，不能 merge / split / 删除。
2. **标记服务端盖章**：`JobRequest.Steward`（`json:"-"`，不上线、不进 request_json）→ `jobs.steward` 列（additive）；常驻会话重启恢复时从该列恢复，保证凭据种类与 MCP 注入不丢。
3. **MCP 自动注入**：管家 job 启动（含恢复）时，server 往 ACP `session/new` 的 `mcpServers` 追加 `gofer mcp`（用 server 本机 gofer 二进制 `GOFER_BIN`）+ 显式 env（`GOFER_JOB_TOKEN` 管家专用凭据、`GOFER_SERVER_ADDR`、`GOFER_JOB_ID`、`GOFER_STEWARD=1`）。MCP 进程见 `GOFER_STEWARD` 只注册管家白名单工具（和 leader 一样由服务端环境决定，agent 无法自己放宽）。
4. **管家笔记**：复用 `plan_handoffs` 的存储与乐观锁，plan_id 取保留命名空间 `steward:notes`（16KB 上限、版本只增）。`/v1/steward/notes` GET（`?version=`、`?history=1`）/ PUT（body 带 `version` 乐观锁）。压缩：超过 8KB 时状态里标 `notes_need_slim`，巡检 prompt 要求管家重写精简版（新版本写入，旧版本保留可回看）。
5. **运行形态**：管家 job = 持续 ACP 会话 job（tag `steward`、`ExclusiveDir=false` 不占目录锁、`IdleTimeoutSec = steward.idle_end_min*60` 由 job 服务原生空闲结束）。server 把当前管家 job id / agent 记在 `work_kv`（`steward.job_id` …）。状态：未启动 / 运行中（回合进行）/ 空闲（awaiting_input）。切 agent：Tick 与配置写入后 `Reconcile` 发现 `steward.agent` 与运行中管家的 agent 不同 → 结束旧会话，下次需要时按 prime 用新 agent 重建。
6. **Ask**：`POST /v1/steward/ask {text}`：未启动 → 启动，首条 prompt = prime + 问题；已启动 → 等到 awaiting_input（最多 60s）再 `SaySession`；忙超时 409。返回 `job_id`，前端沿用 ACP 事件流（`/v1/jobs/{id}/acp`）展示回复与历史。
7. **巡检**：每日 `review_time`（默认 = 摘要时间前 10 分钟）。只处理「自上次巡检后有变化」的未结工作项（含到期项），每次最多 `steward.review_max_items`（默认 20，等我优先）；没有变化就不起会话（巡检记录标 skipped）。巡检 prompt 列出这批项 + 要做的事（触发整理、查到期、提合并建议、更新 / 压缩笔记），并要求结束时调 `gofer_steward_notes action=review_summary` 写点评。点评存入 `steward_reviews`，每日摘要 `work.digest` 附「管家点评」（当天有点评才有，未启用管家则无）。
8. **事件触发**：`steward_events`（kind+ref 去重）记 会话 offline / ended、到期、草稿 ≥ 5。默认只攒着留给下次巡检；`steward.event_wake`（默认 false）开了才会在节流窗口（30 分钟）后批量唤醒管家整理。
9. **Prime**（≤ 24KB）：角色说明（原则 / 白名单 / 输出风格）→ 笔记 → 未结工作项简表（一项一行：id / 状态 / 标题 / 阻塞 / 下一步 / 最后活动 / 在途请求）→ 最近 24h 日志摘要 → 在途请求账本；超长按「等我 > 到期 > 需现场 / 等资源 > 其他」截断简表，其余区块吃剩余预算，截断处写明「另有 N 项」。

## 白名单（steward 凭据，REST 层）

| 读 | 写 |
|---|---|
| `GET /v1/work-items`、`/*`、`/*/journal`、`/*/sessions`、`/*/links`、`/*/requests`、`/requests`、`/merge-suggestions` | `PATCH /v1/work-items/*`（状态不得为 done / dropped）|
| `GET /v1/sessions`、`/*`、`/*/tail`（新增，只读 transcript 尾部） | `POST /v1/work-items/*/journal`（kind=steward）|
| `GET /v1/jobs`、`/*`（只读状态，不含日志 / request / 产物） | `POST /v1/work-items/*/report-request`（走账本）、`/*/summarize` |
| `GET /v1/steward`、`/steward/notes` | `POST /v1/work-items/*/merge-suggestions`（只记建议）|
| | `PUT /v1/steward/notes`、`POST /v1/steward/review-summary` |

不在表内（提交 job、`/v1/config*`、`/v1/steward/ask|start|stop`、merge / split、删除、accept / reject、work 状态终态写入……）全部 403。MCP 工具与之一一对应：读 `gofer_work_list/get/requests`、`gofer_session_list/get/tail`、`gofer_list_jobs`（新增只读）、`gofer_get_job`；写 `gofer_work_update`（管家版，状态不含终态）、`gofer_work_note`、`gofer_work_remind`（新增）、`gofer_work_merge_suggest`（新增）、`gofer_work_request_report`、`gofer_work_summarize`、`gofer_steward_notes`（新增，get / set / history / review_summary）。issue 只读：仓库没有对应 MCP 工具，本批跳过。

## 新表 / 列（additive）

- `jobs.steward INTEGER`。
- `steward_events(id, kind, ref, detail, at, handled_at, UNIQUE(kind, ref))`。
- `steward_reviews(id, day, state[running|done|skipped|failed], trigger, item_ids, summary, job_id, started_at, ended_at, error)`。
- `work_merge_suggestions(id, target_id, source_id, reason, by, at, state[pending|accepted|dismissed])`。
- 笔记复用 `plan_handoffs`（plan_id = `steward:notes`）；状态复用 `work_kv`。

## REST / CLI

- `GET /v1/steward`（状态）、`POST /v1/steward/start|stop|restart`、`POST /v1/steward/ask`、`POST /v1/steward/review`（手动巡检）、`GET|PUT /v1/steward/notes`、`PUT /v1/config/steward`（设置页写配置）。
- CLI：`gofer steward status|start|stop|ask "<问题>"|notes [--edit]|review`。

## 提交序

1. `docs(plan)`：本计划。
2. `feat(config)`：`steward:` 配置块（含默认值 / 校验）。
3. `feat(jobstore)`：新表 / 列、steward 凭据种类、近期日志查询、合并建议 / 事件 / 巡检记录。
4. `feat(job)`：steward 标记 + steward 凭据种类 + gofer MCP 自动注入（含恢复）。
5. `feat(work)`：session tail、合并建议、管家版更新规则、摘要点评。
6. `feat(steward)`：服务包（prime、笔记、生命周期、ask、巡检、事件）+ 假 host 测试。
7. `feat(httpapi)`：steward 凭据白名单中间件、REST、配置写入、装配。
8. `feat(mcp)`：管家工具集 + client 方法。
9. `feat(commands)`：`gofer steward`。
10. `feat(web)`：设置页管家区、「问管家」面板、笔记查看 / 编辑 / 历史、合并建议。
11. `docs`：skill / README / 配置示例 / 设计状态（G045）。
12. 冒烟与截图（`tmp/w2b-smoke/`）。

## 测试

白名单与凭据强制（不能提交 job / 改配置 / 终态）；prime 截断优先级；笔记版本冲突与压缩保留旧版本；巡检只处理有变化项；事件节流；切换 agent 后状态与在途请求可复述（假 ACP agent fixture，`internal/acp/acptest`）；web vitest。质量门：gofmt、build、`GOOS=windows go build`、vet、`go test ./... -count=1`；web vue-tsc、vitest、vite build（输出到 `tmp/w2b-web-dist`，不动 `web/dist`）。
