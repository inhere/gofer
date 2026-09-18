<!-- template_id: design; template_version: 1.1.1 -->
# 验收台（REV-01）与中继 / peer 收尾（SUP-02）设计

> 状态：Approved 0.1 / 已实施（2026-09-18 人工确认范围"1+2 做了"；R1 = §二 + `job review` CLI，R2 = §一 web 验收台；决策见文末）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-18 | Claude | 初稿：① 验收台——web「待验收」列表 + job 详情验收面板（汇报 / 提交 / Diff 渲染 / 验证 / 用量一屏）+ CLI `job review`；② 收尾——`session release-takeover` CLI、接管 job 终态自动释放会话、tmux 注入 runner 失败也转 B（h-aii-jvia）、peer-http 的 acp resume 续投标记（h-aii-9qiy） |
| 0.2 | 2026-09-18 | omp | R1 实施完成（SUP-02 收尾 + `job review` CLI）：实测记录见 §三；`ResumedFrom` rerun 语义与 `RebuildJob` 的"新会话"规则的差异记在 §3.2 |
| 0.3 | 2026-09-18 | omp | R2 实施完成（web `/review` 列表 + 详情五页签验收面板 + `UnifiedDiff.vue`）：实测记录见 §四；后端零改动（设计允许的两处小改经核实都不需要） |

## 背景

SUP-01 之后一个 job 的验收材料齐了（`needs_review`、`verify`、`commits`、`usage`、`changes.diff`、agent 汇报），但散在四五处：列表要靠状态筛选，diff 只有 `--stat` 摘要 + 一个原文链接，汇报要开日志页。验收仍然是"在容器再跑一遍验证 + 翻日志"。目标：**看一页、点一下**就能 accept/reject，容器侧只在验收台的 verify 结果缺失时才补跑。

## 已确认事实

- `GET /v1/jobs/{id}/diff` 返回 `--stat` 摘要，`?full=1` 流式返回 `<result_dir>/changes.diff`（`internal/httpapi/diff_handler.go`）；worktree job 的 diff 文件是两段（`base..HEAD` + 工作树，段标题行）。web `JobDetail.vue:933` 只渲染摘要 + "查看完整 diff" 原文链接。
- job 行已有 `verify_json / commits_json / usage_json / review 字段 / todo_id / plan_id`（SUP-01），`GET /v1/jobs?status=needs_review` 可筛。
- `sessionrelay.Deliver`（`deliver.go:310`）选路 turn → tmux → `takeoverFallback(reason)`（只对 `no_tmux` / `inject_failed:pane_missing` 转 B）；`ReleaseTakeover`（`service.go:783`）+ HTTP `POST /v1/sessions/{sid}/release-takeover` 已有，CLI 无；接管 job 终态后会话仍 `handed_off`。
- `job.Service.SetEventObserver`（SUP-01 P2）是 job 事件的出站 seam（白名单）；`sessionrelay` 与 `job` 是同层兄弟（都由 httpapi/core 组装）。
- peer-http：`internal/runner/peerhttp` 把 `JobRequest` POST 给对端 `/v1/jobs`；`JobRequest.ResumedFrom` 是 `json:"-"`（非安全原因，只是"不入 request_json"），acp 的 `LoadSessionID` 只在 `ResumedFrom != "" && SessionID != ""` 时设置（S2）。

## 一、REV-01 验收台

### 1. 「待验收」列表（web `/review`，导航「观察」组 Board 之后）

- 数据：`GET /v1/jobs?status=needs_review&limit=…`（现有），每行：job id / 标题 / project / agent / plan+todo（可点）/ **verify**（passed·failed·skipped 徽标）/ commits 数 / usage（tokens·$）/ 等待时长；顶部计数徽标复用 EscalationBell 的 pending 感知（`stats.jobs.by_status.needs_review`）。
- 行内操作：Accept（一键）/ Reject（弹 note + "拒绝后自动续投"）——复用 S3 的接口；行点开进详情验收面板。
- 空态文案指路：`job run --review`、项目 `require_review`。

### 2. 详情验收面板（`JobDetail.vue`，status ∈ needs_review | rejected | done 且 `require_review`）

顶部一个面板，五个页签，默认停在「汇报」：

| 页签 | 内容 | 数据源 |
|---|---|---|
| 汇报 | agent 最终文本（stdout.log 尾部 ≤64KB，markdown 渲染，复用 FilePreview 的 marked+DOMPurify） | `GET /v1/jobs/{id}/logs?stream=stdout&tail=…`（现有日志接口，缺 tail 参数则加） |
| 提交 | `commits[]`（sha 可复制、subject），`base_sha` → `HEAD` | job 行 |
| Diff | **就地渲染** `changes.diff`：文件级折叠、hunk 头、`+/-` 着色、每文件行数统计；上限 1MB / 5000 行，超出显示前 5000 行 + "下载完整 diff"；worktree 两段各成一组 | `GET /v1/jobs/{id}/diff?full=1` |
| 验证 | `verify` 状态/命令/耗时/exit + stderr 尾部中 `===== gofer verify` 横幅之后的输出（前端从 stderr tail 截取） | job 行 + logs |
| 用量 | tokens / cost / source | job 行 |

面板底部固定 Accept / Reject（note 必填、可勾"自动续投"）；已验收的显示 reviewed_by / at / note。Diff 渲染器是**自写的轻量组件**（`UnifiedDiff.vue`：解析 `diff --git` / `@@` / `+-` 行，不引入 diff2html 之类依赖），`vue-tsc` 过。

### 3. CLI `gofer job review <id>`

容器里验收用：一屏打印 status / review 字段 / verify（状态、exit、耗时）/ commits（≤20）/ usage / diff `--stat` 摘要 / 汇报尾部（默认 60 行，`--tail N`）；`--diff` 追加完整 diff 原文。退出码：`needs_review` 为 0，其余也为 0（只是查看）。`job accept|reject` 不变。

### 4. 非目标

不做行内评论、不做逐文件 accept、不做 diff 与提交的双向导航。

## 二、SUP-02 收尾

- **`gofer session release-takeover <sid>`**：调现有 HTTP 端点；输出新状态。
- **接管 job 终态自动释放**：`job.Service` 增 `OnTerminal(func(JobResult))` 钩子列表（`finish()` 后置，异步、不阻塞、失败只 warn；与 workflow/retry 钩子同位置）；组装层（httpapi/core）注册 `sessionrelay.ReleaseTakeoverForJob(jobID)`：找 `handed_off_job_id == jobID` 的会话 → 置 `idle`、清 `handed_off_*`，事件 `session.takeover_released {reason: "job_<status>"}`，通知走既有 `NotifySessionHandedOff` 的对偶（可选订阅 `session.takeover_released`）。人手动 `release-takeover` 仍可用；接管 job 还在跑时不动。
- **B 兜底范围**：`takeoverFallback` 扩到 `inject_failed:runner_error`（runner 拒绝/离线时也能起进程接管；`pane_busy` 不转——人在用终端）。
- **peer-http 的 acp resume**（h-aii-9qiy）：`JobRequest.ResumedFrom` 改为 `json:"resumed_from,omitempty"`（它不是安全字段：唯一作用是让 acp runner 对**自己的** `session_id` 走 `session/load`，agent 不认识的 session 自己会拒）；peerhttp runner 原样 POST；对端 `acpRequest` 规则不变。`request_json` 因此会带 `resumed_from`，`job rerun` 一个续投 job = 再次续接同一会话（与 exec 载体的 argv 内含 session id 一致）。`ResumeSourceAgent` / `ReviewFixed` / `TodoForeign` 仍 `json:"-"`。
- **h-aii-pq8a**：`TestSubmitExecNoSessionInjection` 失败分支加上下文（`Submit` 的错误全文 + 当时 `runtime.NumGoroutine()` 与并发 job 数），便于下次全量失败定位；不改语义。

## 实施分期与验收

| 期 | 内容 | 验收 |
|---|---|---|
| R1 | 二、SUP-02 收尾全部 + `job review` CLI + logs `tail` 参数（后端先行） | `session release-takeover` CLI 可用；接管 job done 后会话自动 idle；`inject_failed:runner_error` 转 B；peer e2e：acp resume 经 peer 走 `session/load`；`job review` 输出含五块 |
| R2 | 一、REV-01 web：`/review` 列表 + 详情验收面板 + `UnifiedDiff.vue` | `vue-tsc`/build 过；桩数据下列表/面板/diff 渲染截图；接口无新增（除 logs tail） |

## 决策

1. Diff 渲染自写轻量组件，不引入第三方 diff 库；上限 1MB / 5000 行。
2. `ResumedFrom` 公开到 HTTP 契约（非安全字段），`rerun` 续投 job 语义 = 再续同一会话。
3. 接管 job 终态自动释放默认开启，无开关（人可随时手动 release，行为可逆）。

## 三、R1 实测记录（2026-09-18）

全部在 Windows 本机、`go test`（`t.TempDir()`，未碰真实配置目录/真实 serve）下验证；`go build ./...` 与 `go vet ./...` 干净，`gofmt -l` 对改过的文件为空。

### 3.1 逐项证据

| 验收项（§实施分期 R1） | 证据（测试 / 原始输出） |
|---|---|
| `session release-takeover` CLI 可用 | `internal/commands` `TestSessionReleaseTakeoverCommand`：`POST /v1/sessions/<sid>/release-takeover`（空 body，全 id 不再查列表），输出含 `state=idle` |
| 接管 job done 后会话自动 idle | `internal/sessionrelay` `TestReleaseTakeoverForJobReleasesHandedOffSession` / `…IgnoresOtherJobs`（不 cancel 已终态 job、只匹配 holder、幂等重放不再发事件）+ `internal/httpapi` `TestTakeoverJobEndReleasesSession`（组装层 e2e：假接管 job 终态 → 会话 idle、事件 `sid:job:job_done` 到达 notifier 缝） |
| `inject_failed:runner_error` 转 B | `internal/sessionrelay` `TestTakeoverFallbackOnRunnerError`（dispatch 报错与脚本非 pane 退出码两种形态都转 B；`pane_busy:vim` 仍不转） |
| peer e2e：acp resume 经 peer 走 `session/load` | `internal/httpapi` `TestPeerACPResumeLoadsSession`：hub 提交 acp job（runner=peer）→ 源 job `session_id=sess-acptest-1` → `ResumeJob` → 对端 job 行 `resumed_from=<hub 源 job id>`，对端 stderr 出现 `acptest: session/load sid=sess-acptest-1`、**无** `acptest: session/new`（acp runner 日志 `loaded=false` → `loaded=true`） |
| `job review` 输出含五块 | `internal/commands` `TestJobReviewPrintsSections`：status/review/verify/commits/usage/`diff --stat`/汇报尾部全出现，`--diff` 才追加 patch（且 `logs/stdout?bytes=65536`） |
| h-aii-pq8a | `internal/job` `TestSubmitExecNoSessionInjection` 两个失败分支带 `Submit` 错误全文 + `goroutines=` + `active_jobs=`（只加上下文，断言未变） |

附带落地：`job.Service.OnTerminal`（异步、每钩子独立 goroutine、panic recover 只 warn）覆盖 `finish`（done/failed/cancelled/timeout）与 accept/reject 两条终态路径——`internal/job` `TestOnTerminalHooksRunAfterFinish`（done/failed 各一次、panic 钩子不影响其它钩子与 job）与 `TestOnTerminalHooksRunOnAcceptReject`（needs_review 期间不触发，accept→done / reject→rejected 各一次）。

### 3.2 与设计文字的偏差 / 待人工决策

- **`job rerun` 一个 acp 续投 job ≠ 再续同一会话**（决策 2 的括注）。`ResumedFrom` 现在确实随 `request_json` 往返（这正是 peer 续投要的），但 `RebuildJob` 按既有规则清空 `SessionID`（"fresh job, NOT a resume — don't rebind the source session"，有测试 `TestRebuildJobEmptyOverridesStampsFreshFields` 钉住）。于是 rerun 出的 acp job 有 `resumed_from` 无 `session_id`，`resumeLoadSessionID` 要求两者同时存在 → **不开 `session/load`，等于新会话**。exec 载体的续投不受影响（argv 自带 session id，rerun 重放 argv 仍是续接），所以设计里"与 exec 载体一致"的说法对 acp 载体不成立。本期按任务要求**保留**该字段、未改 `RebuildJob` 的清理规则；要真做"rerun 再续同一会话"，需要单独决定是否让 rerun 继承 `SessionID`（会与上面那条既有规则/测试冲突）。
- `session.takeover_released` 的 `reason` 用 `job_<status>`（如 `job_done` / `job_failed`；状态行读不到时 `job_unknown`）。design 未规定 job 行缺失时的取值。

## 四、R2 实测记录（2026-09-18）

web 无测试框架，验收方式 = `vue-tsc --noEmit` + `vite build` 干净 + 桩 `/v1/*` 数据在真实 Chromium 里跑一遍。后端本期**零改动**（理由见 4.3），故无 wire/接口变更。

### 4.1 桩数据与跑法

桩服务（临时文件，不进仓库）serve 构建产物 `web/dist` 并顶掉 `/v1/*`：3 条 `needs_review` —— verify `failed`（2 commits）/ `passed`（12 commits + 3 文件 diff 含 rename 与 binary）/ `skipped`（0 commits + 无 stdout + worktree 两段 diff，原文 7462 行），外加一条已 `rejected` 且**没捕获到** `changes.diff` 的 job 用来打 404 分支。Chromium 里依次走：列表渲染 → 行内 Accept → 行内 Reject（弹层校验 + 自动续投）→ 详情五页签逐个 → diff 折叠与 6000+ 行降级 → 800px 窄屏。

### 4.2 观测（截图核对 + 页内取值）

| 验收项 | 观测 |
|---|---|
| 列表列与排序 | 表头 `job·标题/id / project / agent / plan·todo / verify / 提交 / usage / 等待 / 裁决`；三行按等待时长倒序（`1h00m` / `10m02s` / `2m02s`）；verify 徽标 class `verify--bad`/`--ok`/`--skip`，计算色 `rgb(178,58,36)` / `rgb(40,113,64)` / `rgb(79,101,113)`；`plan-review-r2 · t1` 链到 `/plans/plan-review-r2`，无 plan 显示 `—`；usage 单元格 `1.12M tokens · $3.4211`（被夹住时全值在 title）；顶栏 Review 徽标 = `3`，清空后隐藏 |
| 行内 Accept | 点击后该行 class `trow--leaving`（opacity 过渡中 0.78）→ 移除；副标题 `3 → 2 个待验收 job`，导航徽标 `3 → 2` |
| 行内 Reject 弹层 | 标题「拒绝验收」+「拒绝理由（必填）」；空串与纯空白都禁用「确认拒绝」，填入理由后启用；勾「自动续投」→ `POST /reject {note, resume:true}` 返回 `resume_job_id`，弹层关闭、行淡出、右下角 toast「已拒绝，并已续投新 job」+ 新 job 链接（8s 自动消失） |
| 详情验收面板出现时机 | `needs_review` / `rejected` / `done + require_review` 三种都渲染；默认页签「汇报」；底部在 `needs_review` 时是 Accept/Reject，裁决后换成「已验收 · human:you · 09-18 16:19:17 done」（页首徽标同步 `done`），rejected 显示 `reviewed_by/at/note` 与结果徽标 |
| 汇报页签 | stdout 尾部 64KB 交 MarkdownBlock：h1–h3、列表、代码块、3 行表格、引用块均渲染；无 stdout 的 job 显示「agent 无文本输出」 |
| 提交页签 | 头部 `基线 0000000000 → HEAD 0000000000` + 12 条（sha + subject，点 sha 复制）；0 条显示「无提交（可能只改了工作树，见 Diff）」 |
| Diff 页签 | 3 个文件块：`web/src/api/client.ts +3 −2`、`docs/new-notes.md 重命名 +1 −0`（meta 行含 `similarity index` / `rename from|to`）、`web/public/logo.png +0 −0` 只渲染一行「二进制文件 · 不显示内容」；点文件头折叠（`aria-expanded=false`，行数 4980 → 1380 再展开回 4980）；hunk 头 `@@ -1,3000 +1,3002 @@`；`+`/`-` 行背景 `rgba(91,166,110,0.14)` / `rgba(200,85,61,0.14)` |
| 大文件降级 | worktree 两段各成一组：段标题 `=== committed (1a2b3c4..HEAD) ===` / `=== uncommitted ===`（phosphor + 左侧竖线）；原文 7462 行 / 177.1 KB → 只渲染前 5000 行（4980 条内容行），底部「已截断：仅渲染前 5000 行（全文 7462 行 / 177.1 KB）」+「下载完整 diff」；`new file mode` 的块标「新增」 |
| 验证页签 | `failed (exit 1, 1.2s)`（红）+ 命令行 `go test ./...` + 从 stderr 尾部 64KB 截最后一段 `===== gofer verify: … =====` 到 `===== gofer verify: exit=1 dur=1234 =====`（含两端横幅，≤200 行）；`skipped` 显示 `skipped (agent 未正常结束)` + 「未跑验证：agent 未正常结束」 |
| 用量页签 | 逐行 `input_tokens / output_tokens / cache_read_tokens / cache_write_tokens / total_tokens / cost_usd / source`，缺失项 `—`（agent 没报 ≠ 0），底部一行摘要 `total 12k (acp:usage_update)` |
| diff 缺失（404） | 「no diff - this job captured no full diff」+ 提示行「此 job 没有捕获到 diff（无改动、非 git 仓库，或改动全在提交里）」+「重试」按钮 |
| 响应式（800px） | `.rp-tabs { overflow-x: auto }` 生效（页签不换行、超宽横向滚）；diff 行 `white-space: pre`、hunk `overflow-x: auto`（不折行） |

### 4.3 后端零改动（设计允许的两处小改都不需要）

- `GET /v1/jobs?status=needs_review` 与其它筛选并用：`jobstore.ListJobs` 把 `project_key = ?` 与 `status = ?` 一起 AND 进 WHERE，web 的 `listJobs` 本就透传 status + project + tag + agent + runner + session + plan + …，因此 `GET /v1/jobs?status=needs_review&limit=100` 直接可用（未加参数）。
- `GET /v1/stats` 的 `jobs.by_status`：`CountJobsByStatus` 是 `SELECT status, COUNT(*) FROM jobs GROUP BY status`，`needs_review` 天然在列，无需补字段。

### 4.4 与设计文字的偏差（留人工决定）

- **「顶栏计数徽标」落在导航 Review 入口上**（`App.vue`「观察」组；移动抽屉同步）。计数由 `EscalationBell` 那一轮 5s 轮询顺手读 `/v1/stats` 写进共享 `store/reviewCount.ts`——没有第二个定时器，代价是计数只在顶栏挂载时更新（`/access` 页无顶栏，本就不显示）；验收台本地裁决后会按自己的列表长度立刻修正。
- **详情页信息重复**：设计只要求「`:933` 的 diff 摘要保留在原位置」，本期把「产出与审计」里的 **提交 / 验证 / 用量**三块也原样保留（非 review job 同样要看），于是 review job 的详情页面板与产出区各有一份。取舍：面板负责"一屏裁决"，产出区负责"完整审计（含产物、结构化结果、pty）"；是否合并留待人工决定。
- **降级条件**取设计原话「> 1MB **或** > 5000 行」二者任一命中；原文整体已在内存里，「下载完整 diff」由前端 Blob 直接存盘（不再打后端）。
- 列表的 `require_review` 徽标**未渲染**：这个队列里的 job 按定义就是待验收的，该徽标不携带信息（列宽让给标题）。
