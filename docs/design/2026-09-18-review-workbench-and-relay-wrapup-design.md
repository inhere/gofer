<!-- template_id: design; template_version: 1.1.1 -->
# 验收台（REV-01）与中继 / peer 收尾（SUP-02）设计

> 状态：Approved 0.1 / 实施中（2026-09-18 人工确认范围"1+2 做了"；决策见文末）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-18 | Claude | 初稿：① 验收台——web「待验收」列表 + job 详情验收面板（汇报 / 提交 / Diff 渲染 / 验证 / 用量一屏）+ CLI `job review`；② 收尾——`session release-takeover` CLI、接管 job 终态自动释放会话、tmux 注入 runner 失败也转 B（h-aii-jvia）、peer-http 的 acp resume 续投标记（h-aii-9qiy） |

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
