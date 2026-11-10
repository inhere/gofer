<!-- template_id: plan; template_version: 1.3.0 -->
# ACP 持续会话历史与继续 实施计划

> 状态：Draft 0.2 / 待批准
> issue：gofer-7yrg

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-10 | Claude（gofer 监督会话） | 初稿 |
| 0.2 | 2026-11-10 | Claude（gofer 监督会话） | 按计划评审修订：短路用例数据 ≥101（S1）；T3 用独立 worktree，`api/workbench.ts` 并入 T1（S2）；补两个源码断言测试（S3）；路由目标排首、cursor 页去重说明、helper 放 `filter.go`、`include` 支持逗号、改库前停 server（S4–S7） |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：会话页 ACP 页签按会话列出全部 ACP 持续会话（含已结束），点开在工作台回看并继续，时间窗外的旧会话也能打开且刷新后不丢。Scope freeze：只实施设计 Draft 0.3（已批准，Q1–Q3=A）与下文“对设计的细化”。Review budget：一轮合并轴评审，有阻塞项才继续。停止条件：评审无 `CORE_BLOCKING`，停在计划 Gate。

## 目标与完成定义

- `GET /v1/workbench/threads` 支持 `source=acp|pty|relay`、`live=1`、`include`（可重复 ≤8），非法值 400；可见性不变。
- 工作台每次首页加载附带 `include`；从会话页打开的旧会话可定位、刷新后不丢、可继续发言、不变置顶。
- 会话页 ACP 页签：一个会话一张卡，服务端分页 / 项目筛选 / `live`，「打开会话」进工作台。
- 文档同步；单测、真实验收、反向验证通过；gofer-verify 四路通过；合并、关 issue。

## 范围、排除项与授权

范围：设计 §1–§4。排除：设计“非目标”；续接回放是否重复只在 T5 实测记录，若重复另立 issue；`docs/capabilities.md` 在发版时更新。

外部动作适用性：host_or_non_offline_action=REQUIRED （建 / 删主机 worktree、主机 web 构建、gofer-verify 均为主机 job；发版不在本计划内）。新文件都在现有包 / 目录内，不新增模块。

授权：本计划批准后，按功能点本地提交、建 worktree、派子 agent、跑主机 job 与临时 server。push 与发版在本计划与 gofer-nt9o 计划都合并后按发版流程进行（用户：「这两个做了发一版」）。

## 输入与批准证据

- 设计：`docs/design/2026-11-10-acp-session-history-design.md` Draft 0.3。用户 2026-11-10：「两份设计 Q1–Q3 按推荐，批准进入计划」。
- 现状实测：`tmp/7yrg-probe/`（工作区，不入库）——会话页 ACP 页签显示 0；工作台打开窗口内已结束会话可回看、可发送。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 按会话列出 ACP 会话 | `GET /v1/workbench/threads`（按 session 归组、分页、Keep）；`GET /v1/jobs` | 是 | 线程接口加 `source` / `live` | OWNER_EXTENSION | 现有 `kind=agent` 含 PTY；无「未结束」多值筛选 | 过滤只写在 `match` / `buildResponse` 共用的一个判断函数 |
| CAP-02 | 时间窗外线程随首页返回 | `LoadWorkbenchPage` 的 `PinnedIDs`（置顶附带） | 是 | `include` 并入本次 `PinnedIDs` | OWNER_EXTENSION | 无按 id 取线程的入口 | 不写偏好，不新增 store 查询 |
| CAP-03 | 回看与继续 | 工作台 `ThreadView` + `Turn` | 是 | 会话页只链接过去（Q3=A） | DIRECT_REUSE | none | 不复制对话 UI |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 按 id 取单条线程的新接口 | CAP-02 | 置顶附带机制已能让指定线程不受时间窗返回，并已过可见性过滤；新接口要另写投影与权限 |

## 对设计的细化

| # | 问题 | 定案 |
|---|---|---|
| R1 | Query 字段 | `workbench.Query` 加 `Source string`（`""`/`acp`/`pty`/`relay`）、`Live bool`、`Include []string`。常量 `SourceACP`/`SourcePTY`/`SourceRelay` 与 `ValidSource(v)` 放 `internal/workbench/filter.go`（新）。 |
| R2 | 判断函数 | `filter.go`：`func threadMatchesSource(t Thread, src string) bool`——acp：`t.Kind != KindRelay && !t.Interactive`；pty：`t.Kind != KindRelay && t.Interactive`；relay：`t.Kind == KindRelay`。`func threadLive(t Thread) bool`——relay：`t.RawStatus` 不是 `ended` / `offline`；job 来源：`t.Jobs` 任意一项 `!job.IsTerminal(status)`（与 store「线程内任意一行非终态」一致；`t.Jobs` 为空时退回 `RawStatus`）。`listPage` 的 `match` 与 `buildResponse` 的过滤各加一行调用这两个函数。注意过滤在 `Summary()` 之前（`Summary` 会去掉 `interactive` 与 `jobs`）。 |
| R3 | `live=1` 短路 | `listPage` 循环：`query.Live` 时只收集 `key.Exception` 的线程，第一次 `LoadWorkbenchPage` 后即 break，`HasMore=false`、`NextCursor=""`。带 cursor 的 `live=1` 请求直接返回空页。判断与 R4 的并入写成 `filter.go` 里的 helper，`service.go`（已 1137 行）只加调用。 |
| R4 | `include` | handler 读 `include`（可重复，也支持逗号分隔，与现有 `full` 一致，`workbench_handler.go:176-183`），>8 或任一 `workbench.ParseThreadID`（现有解析函数，按实际名）失败 → 400；`listPage` 仅在 `query.Cursor == ""` 时 `pinned = append(pinned, query.Include...)`（去重），不写偏好。非分页的 `List` 路径忽略 `include`。带 cursor 的后续页里该线程按普通线程可能再出现，工作台 `appendOlderPage` 已按首页 id 去重（`threadPaging.ts:72-84`）。 |
| R5 | handler | `internal/httpapi/workbench_handler.go` `handleListWorkbenchThreads`：按现有 `status` / `detail` 的校验写法解析 `source`、`live`（`1`/`true`；其他非空值 400）、`include`。 |
| R6 | 工作台前端 | `web/src/api/workbench.ts` query 类型加 `source`、`live`、`include?: string[]`（重复参数）。`threadPaging.ts` 首页参数函数加 `include` 入参。新 `web/src/components/workbench/deepLink.ts`：`includeIDs(routeThread, wanted): string[]`（路由目标排第一，其后并集、去空、去重、截 8）与 `needsReload(routeThread, loaded): boolean`。`Workbench.vue` 只加接线：`loadThreads` 首页请求传 `includeIDs(route.query.thread, wantedFullIDs())`；`route.query.thread` 的 watch 中 `needsReload` 为真时调用 `loadThreads()` 一次。行数增量不超过 ~10 行。 |
| R7 | 会话页 | 新 `web/src/components/SessionsAcpTab.vue`：props `project`、`showEnded`、`readOnly`、工作项关联所需数据；取数 `listWorkbenchThreads({ source: 'acp', since: 1, limit: 50, live: !showEnded || undefined, project })`，「加载更多」用 `next_cursor` 追加；卡片字段与按钮按设计 §3；`emit` 计数给父页签（`N` / `N+`）。`Sessions.vue` 删掉旧 `acpSessions` / `loadAcpSessions` / ACP 卡片模板，换成组件；工作项关联逻辑按需下沉或经 props/emit 复用，行为不变。纯函数（卡片状态文案、链接）放 `web/src/utils/acpThreads.ts`（新）。 |
| R8 | web 测试 | 无 jsdom：`deepLink.test.ts`、`acpThreads.test.ts` 测纯函数；`sessionsAcpList.test.ts`（源码断言式）改为断言新组件存在与关键调用（`source: 'acp'`、`/workbench?thread=`）；页面行为由 T5 浏览器验收覆盖。 |

## 前置检查与 fail-closed 条件

workspace baseline：主机建 worktree `.worktrees/7yrg`（分支 `feat/7yrg-acp-history`），`.git` 改相对 gitdir；`web/node_modules` 链接主 checkout；Go 命令带 `GOWORK=off`，`TMPDIR` 在任何 git 仓库之外。

fail-closed（出现即停，回报监督会话）：

- 基线 `go test ./internal/workbench ./internal/jobstore -run 'Workbench' ./internal/httpapi -run 'Workbench'` 未改动时失败。
- `PinnedIDs` 附带实际受时间窗限制，或附带线程不经过 `match`。
- 需要改 schema、权限声明或续接逻辑。

## 波次与依赖

```
W0  T0 前置核对（Claude）
W1  T1 后端筛选与 include（sonnet 子 agent）
W2  T2 工作台深链（sonnet 子 agent，`.worktrees/7yrg`）‖ T3 会话页 ACP 页签 + T4 文档（suga，gpt，`.worktrees/7yrg-web`）
W3  T5 真实验收（Claude）→ T6 反向验证、全量与收尾（Claude）
```

`web/src/api/workbench.ts` 的类型改动并入 T1 一起提交。T3 用独立 worktree `.worktrees/7yrg-web`（分支 `feat/7yrg-acp-tab`，从 T1 完成后的 `feat/7yrg-acp-history` 提交开出），与 T2 并行、不共用 index；T2 完成后先合 T3 分支进 `feat/7yrg-acp-history`（两边改不同文件），再整体合入 main。

## 任务

### T0 前置核对

- 执行方：Claude。
- 动作：建 worktree；跑基线；确认线程 id 解析函数名与 `job.IsTerminal` 可用（`internal/workbench` 已 import `job`）。

### T1 后端筛选与 include

- 执行方：sonnet 子 agent（判定逻辑、可见性）。
- 文件：`web/src/api/workbench.ts`（query 类型：`source`、`live`、`include?: string[]` 按重复参数发出）、`internal/workbench/filter.go`（新）与测试、`internal/workbench/model.go`（Query 字段）、`internal/workbench/service.go`（`match` / `buildResponse` 各一行调用、R3 短路、R4 并入）、`internal/httpapi/workbench_handler.go` 及测试。
- 动作：按 R1–R5。
- 验证：
  - `go test ./internal/workbench -count=1`：acp / pty / relay 各自只返回对应线程（含置顶、活跃附带的线程）；`live=1` 只返回未结束线程且 `has_more=false`，并断言 store 只被调用一次（`workbench.Store` 是接口，测试里嵌入真实 store、覆盖 `LoadWorkbenchPage` 计数；数据须含 ≥101 个已结束且能通过筛选的线程，否则没有短路也只调一次）；旧行未结束、最新行已结束的线程算 live；`include` 让 30 天前未置顶线程出现在首页、带 cursor 时不生效、偏好表未写入；`include` 指向 `Keep` 拒绝的线程时不返回。
  - `go test ./internal/httpapi -run 'Workbench' -count=1`：`source=bad`、`live=x`、`include` 非法格式、9 个 `include` → 400；受限调用方经 `include` 拿不到别人的线程。
  - 提交前 `go build ./... && go vet ./...`。

### T2 工作台深链

- 执行方：sonnet 子 agent（前端状态与刷新时序；T1 完成后同一 agent 续做或新派）。
- 文件：`web/src/components/workbench/threadPaging.ts`、`web/src/components/workbench/deepLink.ts`（新）与测试、`web/src/views/Workbench.vue`（接线）。
- 动作：按 R6（`api/workbench.ts` 已在 T1 改好）。
- 验证：主机 job `npx vue-tsc --noEmit && npx vitest run && npx vite build`；`Workbench.vue` 行数增量 ≤ 10（`git diff --stat`）。

### T3 会话页 ACP 页签

- 执行方：suga（gpt）。
- worktree：`.worktrees/7yrg-web`（见「波次与依赖」）。
- 文件：`web/src/components/SessionsAcpTab.vue`（新）、`web/src/utils/acpThreads.ts`（新）与测试、`web/src/views/Sessions.vue`、`web/src/views/sessionsAcpList.test.ts`、`web/src/views/sessionsCards.test.ts`、`web/src/views/sessionsP4.test.ts`（这两个源码断言 ACP 标记，改为指向 `SessionsAcpTab.vue`，不许删断言）、i18n（`web/src/i18n/` 现有 sessions 文案处）。
- 动作：按 R7、R8 与设计 §3。`Sessions.vue` 行数只减不增。任务书写明：不改无关代码、不删注释、不压缩样式、不改 `Workbench.vue`。
- 验证：同 T2 的主机 web 构建与测试；`wc -l web/src/views/Sessions.vue` 小于改前。

### T4 文档

- 执行方：suga（gpt），与 T3 同一任务书分开提交。
- 文件：`docs/reference/http-api.md`、`skills/gofer-usage/references/web-console.md`、`CHANGELOG.md`「未发布」。
- 动作：按设计 §4。G046。
- 验证：私有名检查。

### T5 真实验收

- 执行方：Claude。
- 环境：worktree 构建（嵌入新 web）的 Linux 二进制，容器内临时 server（临时端口与配置目录，只按自记 pid 停）。造数据：起一个 ACP 持续会话（suga-acp 或 codex-acp，`--shared-dir`），说两轮后 `job end`；起一个交互式 PTY job；为「旧会话」用例，先停临时 server，再在临时库里把一条已结束 ACP 会话的 `started_at` / `updated_at` 改到 30 天前（只改临时 server 的库；容器无 sqlite3 时用一次性 Go 小程序改），再启动。
- 动作：
  - 会话页 ACP 页签：勾 / 不勾「显示已结束」、项目筛选、加载更多；卡片数 = 接口各页之和；PTY 会话不出现。
  - 「打开会话」打开旧会话 → 定位显示两轮对话 → 等一次推送或手动刷新仍在 → 发一条消息 → 续接 job 生成、回复出现在同一线程 → 该线程未置顶。
  - 记录续接 job 的输出是否重复了前两轮（只记录）。
  - 不碰线上 server 与线上库；线上「页签显示 0」的现状已在 `tmp/7yrg-probe/` 留证，发版后再在线上复看一次。
  - 记录写 `tmp/7yrg-impl/acceptance.md`，截图同目录。

### T6 反向验证、全量与收尾

- 执行方：Claude。
- 反向（临时副本，不提交）：`threadMatchesSource` 的 acp 分支去掉 `!Interactive` → PTY 用例必须失败；去掉 R4 的并入 → 旧线程 include 用例必须失败；`includeIDs` 只返回路由目标 → `deepLink` 用例「刷新后仍带打开的线程」必须失败；去掉 R3 短路 → store 调用次数用例必须失败。记录 `tmp/7yrg-impl/reverse.md`。
- `git diff --stat main...` 查无关删除；gofer-verify 四路通过；合并（`--no-ff`）；删 worktree 与分支（先摘 `web/node_modules` 联接）；设计稿补实施记录；计划 COMPLETE 写不超过 8 行总结；关 `gofer-7yrg`；若续接回放重复，另建 issue。

## 回滚与恢复

单独回退合并提交；无 schema、配置、协议变化。

## 人工 Gate

- 本计划批准后开始 T0。
- 发版：本计划与 gofer-nt9o 计划都合并后，按发版流程定版发布（`docs/capabilities.md` 随发版更新）。

## 可追溯性

| 设计项 | 任务 | 验证 |
|---|---|---|
| §1 `source` / `live` | T1 | T1 用例、T5、T6 反向 |
| §2 `include` 与深链 | T1 T2 | T1/T2 用例、T5、T6 反向 |
| §3 会话页 ACP 页签 | T3 | T3 构建与纯函数测试、T5 浏览器 |
| §4 文档同步 | T4 | T4 私有名检查 |

## 完成 Gate 与剩余工作

T6 完成即本计划完成；发版另行进行。
