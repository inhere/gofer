<!-- template_id: plan; template_version: 1.3.0 -->
# 审批记录页 实施计划

> 状态：Draft 0.2 / 待批准
> issue：gofer-nt9o

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-10 | Claude（gofer 监督会话） | 初稿 |
| 0.2 | 2026-11-10 | Claude（gofer 监督会话） | 按计划评审修订：hold 过滤移到合并之后（N1）；`json_extract` 包 `CASE WHEN json_valid`（N2）；web `listJobs` 真正发出 `hold`（N3）；基线命令拆分、CLI 校验一行、GR110 走桩 server、400 写法（N4–N7） |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：web「审批」页与 `job ls --hold` 能列出人工 hold job 的待批与已决记录，不混入普通 job 与升级停放。Scope freeze：只实施设计 Draft 0.3（已批准，Q1–Q3=A）与下文“对设计的细化”。Review budget：一轮合并轴评审，有阻塞项才继续。停止条件：评审无 `CORE_BLOCKING`，停在计划 Gate。

## 目标与完成定义

- `GET /v1/jobs?hold=pending|decided|any` 与 `gofer job ls --hold …` 返回正确集合（SQL 与内存合并同口径），非法值 400。
- web `/approvals` 两个页签可用，菜单、命令面板、验收台入口接好；字段与 job 详情一致，提交时间取自 hold。
- 文档同步（skill references、http-api、CHANGELOG）。
- 单测、真实验收、反向验证通过；gofer-verify 四路通过；合并、关 issue。

## 范围、排除项与授权

范围：设计 §1–§3。排除：设计“非目标”；`docs/capabilities.md` 在发版时更新（不在本计划）。

外部动作适用性：host_or_non_offline_action=REQUIRED （建 / 删主机 worktree、主机 web 构建、gofer-verify 均为主机 job；发版不在本计划内）。新文件都在现有包 / 目录内，不新增模块。

授权：本计划批准后，按功能点本地提交、建 worktree、派子 agent、跑主机 job 与临时 server。push 与发版在两项（本计划与 gofer-7yrg）都合并后按发版流程进行（用户：「这两个做了发一版」）。

## 输入与批准证据

- 设计：`docs/design/2026-11-10-approvals-page-design.md` Draft 0.3。用户 2026-11-10：「两份设计 Q1–Q3 按推荐，批准进入计划」。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 列出 hold job | `GET /v1/jobs` + `job.ListOpts` + `jobstore.ListQuery`；`/v1/inbox` 审批分类；铃铛 `pendingApprovals` | 是 | 列表加 `hold` 维度 | OWNER_EXTENSION | 现有只有待批，已决无法按 status 区分 | 无新存储；内存合并须同口径 |
| CAP-02 | 审批决定 UI | `ApprovalPanel.vue`（job 详情） | 是 | 列表只链接过去（Q1=A） | DIRECT_REUSE | none | 不复制批准面板 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| NONE | none | none |

## 对设计的细化

| # | 问题 | 定案 |
|---|---|---|
| R1 | 枚举与 SQL | `internal/jobstore/jobs_hold_filter.go`：`const HoldFilterPending = "pending"`、`HoldFilterDecided = "decided"`、`HoldFilterAny = "any"`；`func ValidHoldFilter(v string) bool`；`func holdFilterSQL(v string) (string, error)` 返回常量片段：SQLite 不保证 AND 的求值顺序，`json_extract` 对坏行或空串会报错，所以取值一律包一层：`json_extract(CASE WHEN json_valid(hold_json) THEN hold_json END, '$.kind')`。公共部分 `json_valid(hold_json) AND COALESCE(json_extract(CASE WHEN json_valid(hold_json) THEN hold_json END,'$.kind'),'') = ''`，pending 加 `AND status = 'awaiting_approval'`，decided 加 `AND COALESCE(json_extract(CASE WHEN json_valid(hold_json) THEN hold_json END,'$.decision'),'') <> ''`。`ListQuery.Hold string`；`ListJobs` 只加一行调用。 |
| R2 | 内存合并 | 过滤放在 `internal/job/list.go` 合并之后、与 status 过滤同处（第 4 步，`list.go:175` 附近），对合并后的每一行判断——overlay 循环里的 `continue` 只是不覆盖，DB 已合入的旧行会留下（决定会变，与 status 同理）。`holdMatches` 放 `internal/job/hold.go` 或新文件 `list_hold.go`：`snap.Hold != nil && snap.Hold.Kind == HoldKindHuman`，pending 加 `snap.Status == StatusAwaitingApproval`，decided 加 `snap.Hold.Decision != ""`。`ListOpts.Hold`（`list.go:17`）传给 `ListQuery.Hold`。 |
| R3 | handler | `internal/httpapi/job_handler.go` 列表 handler：读 `hold`，非空且 `!jobstore.ValidHoldFilter` → 400，写法照 `workbench_handler.go:139-148`（`writeError(c, 400, "invalid hold", "hold must be pending, decided or any")`）。 |
| R4 | client / CLI | `internal/client/client.go` `ListJobs`：`opts.Hold` 非空时 `q.Set("hold", …)`。`internal/commands/job.go` `jobListOpts` 加 `hold` 字段与 `c.StrOpt(&jobListOpts.hold, "hold", "", "", "…pending/decided/any…")`，映射到 `ListOpts.Hold`；CLI 侧只用 `jobstore.ValidHoldFilter` 一行校验（`job.go` 已 4210 行，不加其他逻辑）。`jobListOpts` 已按整个结构体登记在 `flagreset_test.go:47`，加字段无需另登记。 |
| R5 | web 页面 | `web/src/views/Approvals.vue`（新）；数据经 `web/src/api/client.ts` 现有 `listJobs`（`client.ts:676-720` 逐字段 `params.set`：加类型的同时加 `if (opts.hold) params.set('hold', opts.hold)`）；纯函数放 `web/src/utils/approvals.ts`（新）：`holdSubmittedAt(job)`（`expires_at - timeout_sec`，缺字段返回 undefined）、`holdOriginLabel(job)`、`decisionTone(decision)`。分页：每页 50，「加载更多」用 `offset`。项目筛选走页面通用的项目筛选（`projectFilter`）。页签状态放 query（`?tab=decided`）便于返回。 |
| R6 | web 测试 | 测试环境无 jsdom（见 gofer-zszn），不写 DOM 级测试：`approvals.test.ts` 测 R5 纯函数，并用 `vi.stubGlobal('fetch', …)` 断言 `listJobs({ hold: 'decided' })` 的请求带 `hold=decided`；菜单顺序改 `shell.test.ts`；页面 i18n 由现有 `noHardcodedCjk` / `i18nKeyUsage` 测试覆盖；页面行为由 T4 浏览器验收覆盖。 |

## 前置检查与 fail-closed 条件

workspace baseline：主机建 worktree `.worktrees/nt9o`（分支 `feat/nt9o-approvals`），`.git` 改相对 gitdir；`web/node_modules` 链接主 checkout；Go 命令带 `GOWORK=off`，`TMPDIR` 在任何 git 仓库之外。

fail-closed（出现即停，回报监督会话）：

- 基线 `go test ./internal/jobstore ./internal/job -run 'List' -count=1` 与 `go test ./internal/httpapi -run 'ListJobs' -count=1` 在未改动时失败。
- 发现需要改 schema、权限声明或 hold 状态机。

## 波次与依赖

```
W0  T0 前置核对（Claude）
W1  T1 后端筛选（sonnet 子 agent）
W2  T2 web 页面（suga，gpt）‖ T3 文档（suga，gpt）   —— 依赖 T1 的参数名
W3  T4 真实验收（Claude）→ T5 反向验证、全量与收尾（Claude）
```

## 任务

### T0 前置核对

- 执行方：Claude。
- 动作：建 worktree；跑基线测试；确认 `jobListOpts` 的 flag 快照登记方式（`internal/commands` 的 TestMain）。
- 完成标准：基线绿。

### T1 后端筛选

- 执行方：sonnet 子 agent（判定逻辑与查询）。
- 文件：`internal/jobstore/jobs_hold_filter.go`（新）及测试、`internal/jobstore/jobs.go`（一行调用 + `ListQuery.Hold` 字段）、`internal/job/list.go`（`ListOpts.Hold` 与第 4 步过滤）、`internal/job/list_hold.go`（新，可选）、`internal/httpapi/job_handler.go`、`internal/client/client.go`、`internal/commands/job.go` 及各自测试。
- 动作：按 R1–R4。
- 验证：
  - `go test ./internal/jobstore -run 'Hold' -count=1`：人工待批、批准、拒绝、过期、撤回、升级停放、坏 JSON 行、普通 job 各一行，三个取值结果正确，坏行不报错。
  - `go test ./internal/job -run 'ListJobs.*Hold|Hold.*List' -count=1`：内存里有运行中的普通 job 与已批准仍在运行的 hold job，pending/decided 结果正确；DB 行仍是待批、内存已批准时，该 job 不在 pending 里。
  - `go test ./internal/httpapi -run 'ListJobs' -count=1`：`hold=bad` 400；无 `job.read.others` 只返回自己的。
  - `go test ./internal/commands -run 'JobList|JobLs' -count=1`：经真实命令分发 `job ls --hold decided` 把参数送到 httptest 桩 server（GR110；不得替换 `listCmd.Func`，`job_tags_test.go:52-60` 的写法不算真实分发）；`--hold bad` 本地报错。
  - 提交前 `go build ./... && go vet ./...`；`go test ./internal/jobstore ./internal/job ./internal/httpapi ./internal/commands -count=1`。

### T2 web 页面

- 执行方：suga（gpt）。
- 文件：`web/src/views/Approvals.vue`（新）、`web/src/utils/approvals.ts`（新）与 `approvals.test.ts`、`web/src/api/client.ts`（`hold` 参数类型）、`web/src/router.routes.ts`、`web/src/utils/shell.ts`（`SHELL_NAV_GROUPS` 协作组 `/review` 之后、`PALETTE_PAGES`）、`web/src/utils/shell.test.ts`、`web/src/i18n/zh.ts` / `en.ts` 或页面分包、`web/src/views/ReviewQueue.vue`（`APPROVAL_LIST` 改 `/approvals`）。
- 动作：按设计 §2 与 R5、R6。不改无关代码、不删注释、不压缩样式（任务书写明）。
- 验证：主机 job `npx vue-tsc --noEmit && npx vitest run && npx vite build` 通过。

### T3 文档

- 执行方：suga（gpt），与 T2 同一任务书分开提交。
- 文件：`skills/gofer-usage/references/hold-approval.md`、`references/commands.md`（若列有 `job ls` flag）、`references/web-console.md`、`docs/reference/http-api.md`、`CHANGELOG.md`「未发布」。
- 动作：按设计 §3。G046：不写 issue id、本机路径。
- 验证：私有名检查（`PRIVATE_NAMES_FILE` 指向主 checkout 的 `.private-names`）。

### T4 真实验收

- 执行方：Claude。
- 环境：worktree 构建（嵌入 T2 的 web 产物）的 Linux 二进制，容器内临时 server（临时端口、临时 `GOFER_CONFIG_DIR`，清除 `GOFER_*` 连接变量，只按自记 pid 停）。
- 动作：提交 4 个 `--hold` exec job（批准、拒绝、短 `--hold-timeout` 等过期、留待批）+ 1 个运行中的普通 job（`sleep`）；核对 `job ls --hold pending|decided|any`；浏览器（agent-browser）打开 `/approvals` 两个页签、项目筛选、加载更多、点行进详情批准后回到列表；验收台入口跳转；截图与记录写 `tmp/nt9o-impl/acceptance.md`。

### T5 反向验证、全量与收尾

- 执行方：Claude。
- 反向（临时副本，不提交）：去掉合并后的 hold 过滤 → T1 内存用例必须失败；去掉 kind 条件 → 升级停放用例必须失败；去掉 `json_valid` → 坏 JSON 用例必须失败。记录 `tmp/nt9o-impl/reverse.md`。
- `git diff --stat main...` 查无关删除；gofer-verify 四路通过；合并（`--no-ff`）；删 worktree 与分支；设计稿补实施记录；计划 COMPLETE 写不超过 8 行总结；关 `gofer-nt9o`。

## 回滚与恢复

单独回退合并提交；无 schema、配置、协议变化。

## 人工 Gate

- 本计划批准后开始 T0。
- 发版：本计划与 gofer-7yrg 计划都合并后，按发版流程定版发布（`docs/capabilities.md` 随发版更新）。

## 可追溯性

| 设计项 | 任务 | 验证 |
|---|---|---|
| §1 `hold` 筛选（含内存合并、json_valid、枚举） | T1 | T1 用例、T4、T5 反向 |
| §1 CLI `--hold` | T1 | T1 命令分发用例、T4 |
| §2 web 页面与菜单 | T2 | T2 构建与纯函数测试、T4 浏览器 |
| §3 文档同步 | T3 | T3 私有名检查 |

## 完成 Gate 与剩余工作

T5 完成即本计划完成；发版另行进行。
