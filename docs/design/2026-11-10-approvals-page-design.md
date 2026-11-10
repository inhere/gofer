<!-- template_id: design; template_version: 1.2.0 -->
# 审批记录页设计

> 状态：Draft 0.1 / 待批准
> issue：gofer-nt9o

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-10 | Claude | 初稿：现状核查与方案 |

需要用户：答复 Q1–Q3（或接受全部推荐项），并明确批准进入计划编写。批准设计不等于授权实施。

## 背景与目标

`--hold` 提交的 job（推送、发布等需要人批准的动作）在 web 上只有两处入口：铃铛 / 收件箱里的「待你批准」，以及 job 详情页的批准面板。批过、拒过、过期的审批没有地方回看。

目标：web 上加一个「审批」菜单，列出待批和已决的审批记录，每条能看到 job、申请来源、提交时间、理由、结果、决定人、决定时间、备注。

## 范围与非目标

范围：

- 人工审批的 hold job（`hold.kind` 为空）。
- 新增 job 列表筛选参数，web 新页面与菜单项，CLI `job ls` 对应 flag。

非目标：

- 升级停放的 job（`paused_for_update`，`hold.kind = update`）：不是人的决定，不列入。
- 其他审批类：job 内 ACP 工具权限（`interactions` 表）、会话权限与传话（`plan_decisions` 表）、needs_review 验收、worker 注册。存储与语义各不相同，本次不合并（见 Q3）。
- 不新增存储、不改 hold 的状态机与审批逻辑。

## 已确认事实

- 审批记录已经完整落库，无需新存储：`jobs.hold_json`（`internal/jobstore/store.go:159`）存 `HoldState`（`internal/job/hold.go:93-115`）：提交时的 `reason`、`kind`、`timeout_sec`、`expires_at`、`origin`、`command` / `prompt_preview`；决定后的 `decision`（approved / rejected / expired / cancelled）、`decided_by`、`decided_at`、`note`。
- 申请人：`jobs.caller_id`（已认证的提交者）；`hold.origin` 记来源（`job:<id>`、`agent-session:<id>` 或提交渠道）。
- 列表结果已带出 hold：DB 行转结果时 `result.Hold = unmarshalHold(rec.HoldJSON)`（`internal/job/persistence.go:539`），`JobResult.Hold` 是公开字段，`holdSecret` 不出 API。
- 现有列表只能筛出待批：`GET /v1/jobs?status=awaiting_approval`。决定后的状态是 `queued` → 照常跑完（批准）或 `cancelled`（拒绝 / 过期 / 撤回），按 status 筛不出审批记录。
- jobstore 已在用 SQLite `json_extract`（`internal/jobstore/audit.go:60`、`plans.go:770`）。
- web 菜单在 `web/src/utils/shell.ts` 的 `SHELL_NAV_GROUPS`，协作组里已有 `/review`（待验收）；`shell.test.ts` 断言菜单顺序。批准面板是 `web/src/components/ApprovalPanel.vue`，挂在 job 详情页。

## 总体方案

### 1. 列表筛选 `hold`

`GET /v1/jobs` 新增查询参数 `hold`，下推到 SQL（分页仍正确）：

| 值 | 条件 |
|---|---|
| `pending` | `status = 'awaiting_approval'` 且 hold 是人工审批 |
| `decided` | hold 是人工审批，且 `json_extract(hold_json,'$.decision')` 非空 |
| `any` | hold 是人工审批（待批 + 已决） |

「hold 是人工审批」= `COALESCE(hold_json,'') <> ''` 且 `COALESCE(json_extract(hold_json,'$.kind'),'') = ''`。非法值返回 400。与其余筛选（project、caller、since、limit/offset）AND 组合；权限沿用 `GET /v1/jobs` 现有规则（无 `job.read.others` 只看自己的），不新增权限。

排序沿用列表的 `started_at DESC`（提交时间）。

CLI：`gofer job ls --hold pending|decided|any`，同一参数直传。

### 2. web「审批」页

- 路由 `/approvals`，菜单项放协作组、`/review` 之后，键 `nav.approvals`（中「审批」/ 英 “Approvals”），图标复用现有图标。
- 页面两个页签：**待批**（`hold=pending`）、**已决**（`hold=decided`），各自分页（加载更多）；支持项目筛选（与其他列表页一致的 `projectFilter`）。
- 每行：标题（链接到 job 详情）、项目、申请来源（`caller_id` + `hold.origin`）、提交时间、理由、命令 / prompt 摘要（截断，悬浮看全文）；待批行加剩余时间（`expires_at`）；已决行加结果徽标、决定人、决定时间、备注。批准后 job 的运行结果（done / failed）作为次要信息显示在结果旁。
- 待批行的操作：见 Q1。
- 验收台 `ReviewQueue.vue` 里「N 个待批」的入口改跳 `/approvals`（原来跳 `/jobs?status=awaiting_approval`）。

## 架构

入口（`httpapi` 解析 `hold` 参数、`commands` 加 flag）→ `job.ListOpts.Hold` → `jobstore.ListQuery.Hold` 生成 SQL 条件。G021 / G022 方向不变。路由不变，`authz_routes.go` 不需要新声明。

## 关键流程

1. 用户点「审批」菜单 → 页面请求 `GET /v1/jobs?hold=pending&limit=50`（项目筛选时带 `project`）。
2. 切到「已决」→ `hold=decided`；滚到底「加载更多」→ 同参数加 `offset`。
3. 点某行 → job 详情页；待批的在原批准面板上批准 / 拒绝，返回后列表刷新，该条移到「已决」。

## 安全、数据、运维与回滚

- 只读；不改 schema（G057 无影响）。
- 输出字段都是已经公开的 `JobResult.Hold`，`holdSecret`（digest、carry）不出 API。
- 回滚：去掉菜单与参数即可，无数据迁移。

## 决策

| 编号 | 问题 | 选项 | 推荐 |
|---|---|---|---|
| Q1 | 待批行能否直接批准 / 拒绝 | A：只链接到 job 详情，在原批准面板上决定；B：行内放批准 / 拒绝按钮 | **A**。批准面板展示完整命令、理由与漂移校验，审批应在看全内容后做；列表只负责找到它 |
| Q2 | CLI 是否同步加 `job ls --hold` | A：加；B：只做 web | **A**。参数直传，成本很小，agent 也能查审批记录 |
| Q3 | 范围是否包含工具权限、会话权限等其他审批 | A：只做 hold job；B：一并列入 | **A**。其他审批存储与生命周期不同，合并需要新的聚合接口；先把 hold 做好，需要再单独立项 |

## 待确认事项

- 上表 Q1–Q3，等用户答复。

## 验收

- 真实 server：提交三个 `--hold` exec job，分别批准、拒绝、等过期（`--hold-timeout` 取短值）；再留一个待批。`job ls --hold pending|decided|any` 与 web 两个页签结果正确，字段与 job 详情一致。
- 升级停放的 job 不出现在任何页签（用测试数据构造）。
- 无 `job.read.others` 的调用方只看到自己提交的审批。
- `hold=bad` 返回 400。

## 结论与人工计划 Gate

方案只加一个下推到 SQL 的筛选与一个只读页面，不新增存储与权限。等用户答复 Q1–Q3 并批准后写计划。
