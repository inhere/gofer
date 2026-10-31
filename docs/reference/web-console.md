# Web console

> Moved from the README (2026-10-10): page list, Workbench layout, keyboard shortcuts, live push and the review/compare views. The page list below predates the 「今天」 home page (`/today`, v0.127) and the Dashboard redesign (`/dashboard`, v0.130); see [`../../skills/gofer-usage/SKILL.md`](../../skills/gofer-usage/SKILL.md) §12 for those. Design records: [`../design/2026-09-26-web-workbench-design.md`](../design/2026-09-26-web-workbench-design.md), [`../design/2026-10-04-web-request-aggregation-and-live-push-design.md`](../design/2026-10-04-web-request-aggregation-and-live-push-design.md).


## Workbench（W1–W3）

`/workbench` 是主动工作的会话中枢：顶部 composer 选择 project、agent、runner、模式（ACP 对话 / ACP 持续会话 / 交互 PTY / 批处理）、可选 plan todo 和 cwd 后提交（runner 下拉按项目 `allowed_runners` 过滤，不可用项灰显并写原因；持续会话可用 local 或协议 ≥ v13 的 worker；选了 ACP agent 而模式是批处理时默认切到持续会话）；左侧按项目分组显示会话、状态上卷、相对时间与 token 用量；「⚠ 等你 N」按等待时长列出待答 interaction、relay 回复和待看结果；主区复用现有终端、日志、审批卡与 session relay，并可重命名、置顶、标记已看、停止当前 job、打开 job 详情或继续同一 agent 会话。

Workbench 的 thread 定义：`s:<session_id>` 是同一 agent session 的首轮与所有 resume jobs；`j:<job_id>` 是没有 session 的一次性 job（不可续接）；`r:<sid>` 是 session relay 会话。状态优先级为 `blocked > working > review > done > idle`，`stalled` 是独立的疑似停滞标记。caller 自己的标题、已看时间和置顶状态存 server，不影响其他 caller。

快捷键：`/` 聚焦会话搜索；`Ctrl/Cmd+K` 打开命令面板；`Ctrl+Tab` / `Ctrl+Shift+Tab` 在本页最近会话间切换；`Ctrl/Cmd+Enter` 提交 composer 或发送下一轮；`Esc` 从终端/输入区把焦点交还工作台。窄屏一次显示侧栏或主区，主区的「← 会话」返回列表；composer 与「等你」始终位于顶部。

布局（W2a）：主区是标签页，每个标签页最多 4 个分屏窗格，总共最多 8 个标签页；每个窗格显示一个会话。布局按 caller 存 server（`GET/PUT /v1/workbench/layout`，带版本号），刷新或换设备后还原；两处同时改时后写的一方收到 409，页面采用 server 版本并提示「布局已在别处更新」。拖分隔条调比例；从侧栏把会话拖到窗格中心 = 放入该窗格，拖到窗格边缘 = 朝该方向分屏后放入。`Ctrl+B` 进入前缀（1.5 秒内按下一个键，终端里同样生效）：`%` 左右分、`"` 上下分、方向键切焦点、`x` 关窗格（不停 job）、`z` 最大化/还原、`c` 新标签、`n`/`p` 下一个/上一个标签、`1`–`8` 跳标签；命令面板里也有这些动作。只有焦点窗格连续可见 2 秒才把会话标为已看；非焦点窗格的终端保持连接但不重排，日志暂停自动滚动。宽度小于 768px 时只显示焦点窗格，侧栏与主区左右滑动或按「← 会话」切换，composer 收成底部「＋」，「等你」固定在顶栏。

侧栏里已完成（done）的会话只保留最近 3 天（等你、进行中、空闲和当前打开的不受限）。主区日志只从每条流最近 200 行开始加载、默认显示 stdout，完整日志点「完整日志见 job 详情」（流接口 `GET /v1/jobs/{id}/stream?tail=N` 从最后 N 行开始，上限 5000）。会话列表默认隐藏 agent=exec 的会话（多为一次性构建/验证命令），运行中、等待中或当前打开的除外；侧栏勾选「显示 exec 命令会话」可显示（按浏览器记住）。

实时推送（Q 批）：页面只开一条 `/v1/ws`，订阅 `stats` / `pending`（快照）与 `jobs` / `job:<id>` / `sessions` / `runners` / `meta` / `plans` / `workflows` / `schedules`（失效通知后 REST 重拉）。原来的 2.5–5s 定时轮询全部降为断线兜底：WS 断开超过 15s 才改为 30s 一次的轮询，恢复后自动停；页面隐藏超过 5 分钟主动断开。顶栏状态点显示真实连接状态（已连接 / 重连中 / 已断开·兜底轮询）；job 详情页一次 `GET /v1/jobs/{id}?include=…` 取回事件、评论、投递、重试、唤醒、pty、产物和会话链，打开页面的请求从 16 个降到 4 个（含两个日志窗口与首次 ws-ticket）。反向代理需放行 `Upgrade` 头并让空闲超时大于 60s。会话超过 `session.offline_after_sec`（默认 30 分钟）没有心跳会被标为「离线」（进程可能已被杀掉），仍可唤醒；设 `0` 关闭。

PWA 与推送（W2b）：web 可"安装"为应用（manifest `start_url=/workbench`）；设置 → 通知 可开启本设备 Web Push，等你审批/回答、待评审和 plan 阻塞时推送，审批类通知可直接点「允许/拒绝」。推送要求 HTTPS 或 localhost，不满足时退化为标题计数 `(N) gofer` + 顶部提示条。点通知打开 `/workbench?thread=<id>` 并定位会话。细节见 `docs/runbook/web-push.md`。

ACP 对话（W3a）：agent 类型为 `acp-agent` 的 thread 在「过程」区按 `job_ids` 逐轮显示用户 prompt、默认折叠的 thought、可展开的 tool call（含状态、输入和 `path[:line]` 文本）、Markdown 助手消息、permission/plan/usage/stop。默认加载最近 3 轮，顶部可每次再加载 3 轮；只有最新轮仍运行时保持 `GET /v1/jobs/{id}/acp/stream?tail=300`，旧轮读到 `end` 即关闭。待答 interaction 内联在最新轮末尾，底部输入框仍通过 thread turn 续接同一会话。非 ACP 的 cli/批处理继续使用最近 200 行 stdout/stderr 日志视图。

改动与评审（W3b）：会话主区有「过程｜改动」两个子视图。「改动」显示本会话以来的全部改动（`GET /v1/workbench/threads/{id}/diff`）：基线是首轮作业的 `base_sha`（worktree 会话用 `worktree_base_sha`）；续接作业（请求带 `baseline_sha`，如 plan todo 接着上一项做）从它自己的基线起算，运行中也只显示本项的改动；作业在 server 本机执行且目录还在时现场计算（`source=live`，含中间提交与未提交的已跟踪改动，未跟踪文件只列名，patch 上限 2 MiB），在 worker 上执行或目录不在时退回最新一轮已采集的 diff（`source=captured`，只含最新一轮）。会话运行中每 10 秒刷新，收到编辑类工具事件立即刷新；对话里工具卡的文件路径点一下跳到该文件。在 diff 行号旁点「＋」写评论，草稿按会话存在浏览器里；「发送评审（N）」把摘要与每条 `path:line`、上下各 2 行代码和评论合成下一轮发给同一会话（`POST /v1/workbench/threads/{id}/review`，最多 50 条、每条 ≤4000 字）。「接受」对待验收的作业走现有 accept，否则标记已看。

Workflow fan-out worktrees can be merged after a human pick (`gofer workflow pick <id> <step> <fan> --merge [--squash] [--cleanup-others]` or `gofer job worktree merge <job-id>`). The server only merges local-runner worktrees into the project main checkout, requires it to be clean and on a named branch, aborts conflicts (HTTP 409 with the conflicting files), and never pushes. The web workflow page shows a fan-out step as a compare view (one column per agent with status, duration, diff summary, verify result and report tail, side-by-side diffs, and a confirm-first "pick and merge" button that merges first and records the pick only after the merge succeeded; a 409 conflict lists the files and states the repository was restored), the new-workflow page can start from a template (vars form with agent / runner dropdowns filtered by the project, step preview via `POST /v1/workflow-templates/{name}/render`), and the job page has a "merge to baseline" button for worktree jobs (local runner only). The web shows the built-in local runner as `server` (the canonical wire/DB name stays `local`). Built-in templates (`compare`, `plan-implement`, `review-committee`; `gofer workflow template ls`) take `--var` values for project, task, agents and runner; an empty `runner` means the project default. When a template's default agent (e.g. `claude`) is not allowed by the chosen project, the web wizard swaps in the project's first allowed agent of the same kind and says so, and `wf run --template` fails with the list of agents the project allows (override with `--var <name>=<agent>`).

`serve` embeds a static SPA (the page itself needs no auth; its `/v1/*` calls do). Build and embed it with `make web build`; a bare `go build` serves a placeholder page without affecting the API. Pages: Home (service health, runners, escalations, jobs by status, schedules, projects, plus two metadata-db cards — **Server DB**: file + WAL size, page geometry, the busiest tables by row count; **Sessions**: totals by state, relay mode split, turns still waiting for a reply, the card links through to Sessions) / board (the plan filter is a free-text plan-id input — prefix match — with a one-click "recent open plans" hint, and `?plan=` in the URL) / **review queue (`/review`, REV-01)** — jobs parked in `needs_review` with their acceptance material in one row (verify badge, commit count, usage, wait time; longest wait first), inline accept/reject and a top-bar count badge / job detail (live logs, diff, artifacts, pty attach; a job in `needs_review`, `rejected`, or `done` with `require_review` opens on a five-tab **review panel** — report / commits / diff / verify / usage — with accept/reject at the bottom) / Plans (todos, decisions; **plan board** — five-column kanban, drag a card to `ready` to dispatch it; the list filters by status/project/keyword and pages 20 at a time, all carried in the URL) / Sessions (relay switch, `auto (idle Xm)`) / Workflows / Schedules / Agents (configured agents and their detect status) / Runners / Projects (including "allow interactive jobs") / Skills (the skill library: list, SKILL.md body, import/update/remove/export) / New job. The left rail groups the observing pages as Board, Review, Plans, Sessions, Workflows, Schedules and the fleet pages as Agents, Runners, Projects, Skills. Disable with `serve --no-web` or `server.web_enabled: false`.



## 外壳与导航

外壳由 `web/src/ui/AppShell.vue` 提供（`web/src/App.vue` 只决定壳内 / 壳外：`/access` 登录页没有外壳，只有 token 输入、主题与语言开关）。形态完全由视口宽度决定，逻辑在 `web/src/utils/shell.ts`：

| 宽度 | 形态 |
|---|---|
| ≥ 1100px | 236px 完整侧栏；底部按钮可收起成图标栏（`localStorage gofer.shell`，同时记节点区是否展开） |
| 761–1099px | 64px 图标栏（强制收起，不读收起偏好）；顶栏「新建任务」只留图标 |
| ≤ 760px | 无侧栏；底部标签栏「今天 · 工作台 · 任务 · 计划 · 更多」；顶栏左侧是品牌标与连接状态点 |

导航分组（`SHELL_NAV_GROUPS`）：

- 核心：今天 `/today` · 工作台 `/workbench` · 工作项 `/work` · 计划 `/plans` · 任务 `/jobs` · 会话 `/sessions`
- 协作：信箱 `/mail` · 待验收 `/review` · Issues `/issues` · 记忆 `/memory` · 统计 `/dashboard`
- 自动化：工作流 `/workflows` · 定时 `/schedules`
- 基础设施：运行节点 `/runners` · Agents `/agents` · 项目 `/projects` · 设置 `/settings`

旧路径 `/board` 重定向到 `/jobs`（带原 query），`/cluster` 到 `/runners`，`/config` 与 `/skills` 到 `/settings/*`。「设置 → 扩展」里的 `web.nav_hidden` 可按项隐藏导航，今天与设置始终保留；隐藏项仍可直接 URL 或命令面板访问。

侧栏顶部是品牌标（点击回 `/today`）与连接状态（已连接 / 重连中 / 已暂停 / 已断开 / 离线，来自 `api/live.ts`）；侧栏底部是运行节点摘要（`n/m` 在线，点开列出各节点，30 秒刷新）。徽标：「今天」显示待决策数，「工作项」显示「等我」数。

顶栏从左到右：搜索 / 命令面板（`Ctrl/⌘K`，也可在非输入状态按 `/`）、「待我决策」按钮、「只读」徽标（只读 token）、新版本提示（点击进 设置 › 关于；构建过期时改为「刷新」按钮）、「新建任务」（只读时隐藏）、「随手记」、信箱按钮（待指派数量）、主题开关、语言开关。

手机宽度（≤ 760px）的行为：

- 「更多」打开底部抽屉：其余页面的宫格（「工作项」排第一）、主题与语言开关、退出登录；换路由时自动关闭。顶栏不再放语言开关。
- 标签栏有「隐藏项」过滤：被 `nav_hidden` 隐藏的项由后面的项补位，仍凑满 4 个标签。
- 连接状态并入顶栏，缩成一个点。

## 界面语言

- 支持 `zh-CN` 与 `en-US` 两种，不做第三种、不做 RTL。顶栏（手机在「更多」里）的开关直接切换，按钮显示“要切到的语言”（中文界面显示 `EN`）。
- 选择存在浏览器 `localStorage gofer.locale`，同时写 `<html lang>`。优先级：URL `?lang=`（`en` / `zh` 等宽松写法均可，只用于调试与分享，**不写入存储**）> localStorage > 浏览器 `navigator.languages`（以 `en` 开头取英文，其余一律中文）。
- 文案是前端内置字典（`web/src/i18n`，平铺 key；缺 key 回退到中文），切换后立即重绘，不重载页面。
- 服务端显示文本：S3a 起 web 请求会带 `Accept-Language`，服务端生成的显示文本随界面语言返回；阶段 3 逐步覆盖，尚未覆盖的接口仍返回中文。TODO（请核）：当前源码的 `web/src/api/client.ts` 里还没有设置 `Accept-Language` 的代码，这条按 S3a 的计划写，合并前确认 S3a 是否已进本分支。
- 服务端 CLI / 日志语言（`GOFER_LANG`、`LANG`）是另一套机制（`internal/i18n`），与 web 的界面语言互不影响。

## 项目筛选

gofer 是多项目的，没有全局“当前项目”：

- **按页，不是全局**：适用的列表页（今天、工作项、工作台、任务、计划、会话、信箱、待验收、工作流、定时、Issues、记忆、统计，以实际页面为准）在列表头放“项目”筛选，默认“全部项目”。侧栏没有项目选择器。
- **URL 是唯一真源**：选择写进 `?project=<key>`（`web/src/ui/projectFilter.ts`），可分享、前进 / 后退可复原；换项目时去掉 `offset`，回到第一页。命令面板的 `p:<项目>` 前缀会跳到当前页的过滤版本。
- **记忆**：只有一处——选过的项目写入 `localStorage gofer.lastProject`，仅作新建表单（新建任务 / 会话 / 计划 / 定时 / 工作流 / 随手记）的默认值：来源列表带筛选时用筛选值，否则用上次的项目。列表页本身不会因为这个记忆自动加筛选。
- **详情页**：显示所属项目的 tag，点击跳到该项目的任务列表，详情页本身不提供筛选。TODO（请核）：各详情页是否都已带项目 tag，我只核对了筛选与 URL 同步的实现。

## 截图检查脚本

`web/scripts/shots.mjs` 对 6 个代表页（今天、工作台、任务、计划、会话、工作流）加各自的详情页（任务、计划、工作流；id 取列表接口第一条，没有数据或没有 token 则跳过详情），按 4 种宽度（1440 / 1024 / 800 / 390）× 2 种主题 × 2 种语言拍整页截图，默认共 `9 个页面（6 列表 + 3 详情）× 16 = 144` 张。截图给人审阅，不做像素比对，也不进 CI。

依赖：Playwright 不在 `web/package.json` 里，脚本运行时才 `import`，缺失会打印安装提示并以退出码 2 结束。临时安装（不改 package.json / lock）：

```bash
cd web
npm install --no-save playwright && npx playwright install chromium
# 已有系统浏览器时只装库：npm install --no-save playwright-core，运行时加 --channel msedge|chrome
```

运行（需要一个已启动的 `gofer serve`，或 `pnpm dev` 的 vite 开发服务器，后者代理到 `127.0.0.1:8765`）：

```bash
export GOFER_TOKEN=<token>          # token 只从环境变量读，不接受命令行明文
node web/scripts/shots.mjs                                   # 全量
node web/scripts/shots.mjs --pages jobs,plans --widths 390,800 --langs en --themes dark
node web/scripts/shots.mjs --base http://127.0.0.1:5173 --out /tmp/shots --no-detail
```

参数：`--base`（默认 `http://127.0.0.1:8765`）、`--token-env <NAME>`（默认 `GOFER_TOKEN`）、`--out`（默认 `web/shots/`，已被 `web/.gitignore` 忽略）、`--pages` / `--widths` / `--themes` / `--langs` 过滤、`--no-detail`、`--channel`、`--settle <ms>`、`--fail-on-warn`。主题与语言通过预置 `localStorage`（`gofer.theme`、`gofer.locale`）切换，登录 token 预置为 `gofer-token`。

输出：`<out>/<页>[-detail]__<宽>__<主题>__<语言>.png` 与 `<out>/report.json`（含参数、每张的问题列表、汇总；不含 token）。终端逐张打印并汇总；有 error 级问题退出码 1，参数错误或缺依赖退出码 2。

自动检查（设计稿 §13）：

| 检查 | 级别 | 规则 |
|---|---|---|
| `horizontal-overflow` | error | 页面 `scrollWidth` 超过 `clientWidth` 1px 以上 |
| `oversize-svg` | error | 可见行内 SVG 任一边超过 40px（祖先带 `data-shots-allow-svg` 的豁免） |
| `wrapped-label` | error | 徽标 / chip / pill / tag / 标签栏文字 / 导航文字 / tab 等被断成 ≥2 行（`white-space: nowrap` 的不算） |
| `navigation` | error | 被重定向走（多为 token 缺失或无效而落到 `/access`） |
| `residual-cjk` | warn | 仅英文界面：可见文本或 title / aria-label / placeholder 含中文（用户数据本身含中文会误报，可加 `data-shots-allow-cjk` 豁免；`--fail-on-warn` 让它也算失败） |

纯函数部分（参数解析、组合展开、判定规则）在 `web/scripts/shots.lib.mjs`，测试 `shots.lib.test.mjs` 随 `vitest run` 执行。
