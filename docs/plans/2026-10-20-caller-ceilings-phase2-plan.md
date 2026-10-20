<!-- template_id: plan; template_version: 1.2.0 -->
# 调用方上限第二期 实施计划

> 状态：Draft 0.1 / 已批准并请求执行（用户 2026-10-20：「批准计划，按推荐」；决策 1–3 取推荐值）
> issue：gofer-7798

- 思考模式：rigorous。
- 核心目标：补齐 `docs/design/2026-10-18-caller-scope-ceilings-design.md`（已批准，决策 1–9 取推荐值）按决策 9 留给第二期的部分，以及第一期「实施记录」列出的已知限制：长连接复核、`/v1/ws` 主题与快照按范围、聚合与计数按范围、`job.review.others` 与禁止自审、上限拒绝审计，外加中继所有者、配置视图、定时事件读入口、web 只读按钮、多项目 jobs 列表。
- scope freeze：只做本计划「范围」列出的内容；授权 `scope` 的启用（决策 1 的备选）不在本计划内。
- review budget：本计划最多一轮独立评审；评审后修订由作者自查。
- 停止条件：用户具名批准本计划，或用户要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-20 | Claude（监督会话） | 初稿：A 底层数据、B 权限种入、C 中继所有者、D 推送、E1–E7 httpapi、F web、G 文档、V 验收 |

## 目标与完成定义

1. 受限调用方（有 `ceiling`）已打开的长连接（SSE、attach、隧道、托管拨号与转发、ws）在上限收紧或变为只读后，于复核周期（10 s）内断开；未受限调用方行为不变。
2. 受限调用方在 `/v1/ws` 上只能收到范围内的 `job:<id>` 增量与失效通知；`stats` / `pending` 快照只含范围内数据；越界的主题订阅在 `error` 帧报回。
3. `/v1/today`（含 handled、lanes）、`/v1/stats`、`/v1/stats/overview`、work-items digest、`/v1/meta` 对受限调用方只含范围内项目的卡片、计数与列表；未受限调用方的响应逐字节不变（回归用例）。
4. 新权限 `job.review.others`（中风险，`ScopeYes`，SeedV2 补种给 `users`，现有部署行为不变）；审阅（accept / reject / approve）他人的 job 需要它；受限调用方不能审阅自己作为上限所有者的 job；hold 批准后的重投按原提交者准入（补测试）。
5. 上限拒绝写 `auth_audit`，限频键（调用方, 原因, 资源类型）每分钟一条。
6. 其余已知限制关闭：中继区分展示用的 `by` 与上限所有者；`GET /v1/config` 的 callers 视图显示上限；新增 `GET /v1/schedules/{id}/events`；web job 详情只读时隐藏写按钮、审阅按钮按 `.others` 与自审规则显示；受限调用方的 jobs 列表在多项目上限下按查询下推分页（不再先分页后过滤）。
7. 文档（http-api、permissions 生成文档、skill references、CHANGELOG）与设计稿「实施记录」补第二期。

## 范围、排除项与授权

- 范围：上面七条。
- 排除：授权 `scope` 字段的启用；worker 侧对上限的二次校验（第一期已说明由 server 把关）；web 权限页的新交互。
- 授权：用户 2026-10-20「好的，可以」同意本批（drlm + 7798），实施以本计划的具名批准为准；tag、push、CI、mirror、一键更新沿用发版长期授权。

## 输入与批准证据

- 设计：`docs/design/2026-10-18-caller-scope-ceilings-design.md`（「长连接」「审阅」「审计」三节、决策 6 / 9、实施记录的已知限制），用户 2026-10-18「按推荐」批准。
- 代码定位：2026-10-20 只读梳理（见各任务的文件清单）。

## 决策（本计划新增，待确认）

1. **ws 对受限调用方：过滤，不拒绝订阅**（设计倾向过滤）。`jobstore.Change` 加 `Project`；pushhub 连接带范围谓词，失效通知按项目过滤，`stats` / `pending` 快照按范围分别计算（缓存键加范围指纹；未受限调用方共用现有缓存帧）。备选：受限调用方只允许订阅 `job:<id>`，其余主题拒绝——实现小，但受限用户的 web 首页、待决浮层不会实时刷新。推荐：过滤。
2. **没有项目归属的全局信息对受限调用方的处理**：`/v1/meta` 的项目与 worker 列表按上限过滤；`/v1/steward` 与 work-items digest 的 `POST`（会发 webhook）对受限调用方 403；digest 的 `GET` 按范围过滤；today 状态条里的 runner 列表按上限的 `workers` 过滤；用量、DB 大小这类全局数字对受限调用方不返回（字段省略，web 隐藏该块）。推荐如上。
3. **job 凭据开的长连接也复核**：凭据的上限所有者经 `CredentialCeilingOwner` 求得，按其上限复核。推荐：做（否则受限调用方的 job 里的 agent 开的 SSE 不受收紧影响）。

## Capability Discovery

| 能力 | 现状（文件） |
|---|---|
| 长连接登记与复核 | `internal/httpapi/authz_live_conn.go`：`liveConn` 无项目字段；`liveConnStillAllowed` 不看上限 / 只读，非 user 类直接放行；10 s 扫描 |
| ws 主题 | `authz_live_conn.go` `wsTopicAllowed` 不看上限；`/v1/ws` 是 NoUserSurface，不过 `ceilingGate`；`internal/pushhub/hub.go` 快照全体共用 |
| 事件源 | `internal/jobstore/change.go` `Change{Kind,ID,Status}` 无项目；`internal/serve/livepush.go` 接线 |
| 聚合 | `today_handler.go`、`today_lanes_handler.go`、`stats_handler.go`、`stats_overview_handler.go`、`work_handler.go`（digest）、`meta_handler.go`、`steward_handler.go` 未按范围 |
| 审阅 | `job_handler.go` accept / reject / approve 经 `jobReviewer` → `jobMayReview` 只判 `job.review` |
| 种入 | `internal/authz/catalog.go`（`SeedV1`）、`jobstore/auth.go` `seedAuthGrants` 按版本补种 |
| 中继所有者 | `internal/sessionrelay/deliver.go` 以 `by` 作 CallerID / OriginCaller；`gofer` / `gofer-nudge:<id>` / `gofer-mail` 等标签 |
| 其他 | `config_handler.go` callers 视图无 ceiling；`serve/serve.go` 定时拒绝事件只写不读；`web/src/views/JobDetail.vue` 未取 `readOnly`；`job_handler.go` 列表多项目先分页后过滤 |

## 前置检查与 fail-closed 条件

- 开工前 main 全量测试绿；第一期 ceiling 测试夹具（`authz_ceiling_fixture_test.go`）可用。
- 任何任务让未受限调用方的现有响应发生变化（回归用例失败）即停，回到计划修订。
- `TestEveryRouteIsDeclaredOrPending`、`TestAuthzReferenceIsUpToDate` 必须保持绿；新路由先声明再实现（G056）。

## 波次与依赖

| 波次 | 任务 | 依赖 | 并行 |
|---|---|---|---|
| 一 | A 底层数据、B 权限种入、C 中继所有者 | — | 三者不同包，可并行 |
| 二 | D 推送 | A | — |
| 三 | E1 → E7（httpapi，同包串行） | A、B、C、D | — |
| 四 | F web | E4、E6 接口形状 | 可与 E 后段并行 |
| 五 | G 文档、V 验收与发版 | 全部 | — |

## 任务

通用要求（每个任务）：只提交自己的文件（pathspec），英文 conventional message；不删注释、不重排无关代码；用户可见变化同步 skill references 与 `docs/reference/http-api.md`（G045 / G046）；Go 验证 `GOTOOLCHAIN=go1.26.9 GOWORK=off go build/vet/test`，主机实施（suga）不能 `-race`，由监督者在容器复跑；G051 / G052 测试生命周期与等待。每个任务都要有「未受限调用方不变」的回归断言。

### A 底层数据

- `jobstore.Change` 加 `Project`，单条发出点带上；批量发出点（无 ID）保持全局失效，D 里对受限连接按「全量失效、客户端重拉（重拉已按范围）」处理。
- `jobstore` 列表查询支持多项目（`IN`）；`job.ListOpts.Projects`。
- `internal/today`、`internal/overview` 的 Query 加项目谓词；`Handled` 补项目字段（或经卡片键反查，择一并说明）。
- 测试：各包单测。

### B 权限种入

- `internal/authz/catalog.go` 新增 `job.review.others`（RiskMedium、ScopeYes、`SeedV2`）；`latestSeedVersion` 自动补种；同步 `catalog_test.go` 的冻结与种子集断言；`jobstore/auth_test.go` 覆盖 V1→V2 升级补种与新库。

### C 中继所有者

- `sessionrelay` 把展示用的 `by` 与上限所有者分开：HTTP 入口传调用方；nudge 取 `SessionNudge.CreatedBy`；mail 取信件发起者；`gofer` 内部通知取会话 owner。上限所有者取不到时按设计「失败关闭」。
- 测试：受限调用方建的 nudge / 信件触发的中继 job 受其上限约束。

### D 推送

- `pushhub` 连接带范围谓词（nil = 不受限，走现有路径）；`jobs` / `sessions` / `plans` 等失效按 `Change.Project` 过滤；`stats` / `pending` 快照按范围计算，缓存键加范围指纹；`serve/livepush.go` 接线。
- 测试：两个连接（受限 / 不受限）收到的帧不同；不受限连接帧与改动前一致。

### E httpapi（同包串行，按序分提交）

- **E1 长连接复核**：`liveConn` / `liveConnSpec` 加项目（或可重求值闭包）与「写」标记（attach write 模式、托管转发）；`liveConnStillAllowed` 加上限与只读判定；job 凭据按上限所有者复核（决策 3）；隧道与托管转发在调用方有上限时断开。
- **E2 ws**：ticket 带上限所有者；`wsTopicAllowed` 对 `job:<id>` 过范围；订阅时给连接装 D 的范围谓词。
- **E3 聚合**：today（卡片、lanes、handled、状态条按决策 2）、stats、overview、digest、meta、steward 按范围；未受限调用方逐字节回归。
- **E4 审阅**：accept / reject / approve 改为 InHandler 判定：他人的 job 需要 `job.review.others`；受限调用方不能审阅上限所有者是自己的 job；补 hold 批准按原提交者准入的测试；`go generate ./internal/httpapi/...` 更新 `docs/reference/permissions.md`。
- **E5 jobs 列表**：多项目上限下推到查询（通配符先展开为字面项目），分页正确。
- **E6 配置视图与定时事件**：`GET /v1/config` callers 视图显示上限（复用 whoami 的视图）；新增 `GET /v1/schedules/{id}/events`（`Resource("schedule")`，登记 routeDecls），webhook 触发路径的拒绝同样记录。
- **E7 审计**：上限拒绝（`ceilingGate`、范围外写、提交时 `ErrCeilingDenied`）写 `auth_audit`，按（调用方, 原因, 资源类型）每分钟一条。

### F web

- `JobDetail.vue` 取 `readOnly`，只读时隐藏写按钮（重建、取消、批准、合并、验收、交互作答、评论、续接、删除）；审阅按钮按 `.others` 与自审规则显示（服务端为准，前端只做显示）。
- 测试：纯函数 + 接线 `?raw` 断言。

### G 文档

- `docs/reference/http-api.md`、`skills/gofer-usage/references/server-config.md`（上限一节：第二期后的行为、已知限制删改）、`web-console.md`、CHANGELOG；设计稿「实施记录」补第二期。

### V 验收与发版（监督者）

- 容器 Linux 全量 `-race`、darwin vet；Windows 全量；web 三命令；用体验调用方（`read_only`）与一个 `projects` 受限的临时调用方各走查一遍 web 与 ws（只读 token 由用户配置，监督者不改主机配置）。
- 发版走既有流程。

## 回滚与恢复

- 每个任务一到两个提交，验证不过时 `git revert`；E 段按 E1–E7 顺序回退。
- 发版后：上限只对配置了 `ceiling` 的调用方生效，回滚二进制前按设计稿「回滚」先撤销受限调用方的 token；`SeedV2` 补种的授权行在旧版本里无人读。

## 人工 Gate

- **计划批准与执行请求**：用户具名批准本计划（含「决策」三条）并要求开始实施。
- **外部动作**（已有长期授权）：tag、push、CI、mirror、一键更新与 worker 升级。

## 可追溯性

| 设计条目 / 已知限制 | 任务 | 验证 |
|---|---|---|
| 长连接复核（设计「长连接」） | A、E1 | 收紧上限后连接在复核周期内断开 |
| ws 主题与快照（设计「长连接」第二条） | A、D、E2 | 受限 / 不受限连接帧对比 |
| 聚合与计数 | A、E3 | 受限范围断言 + 未受限逐字节回归 |
| `job.review.others` 与禁止自审（决策 6） | B、E4、F | 审阅矩阵用例 |
| 审计（设计「审计」） | E7 | 限频用例 |
| 中继所有者 | C | 中继 job 的上限所有者断言 |
| 配置视图、定时事件、web 只读按钮、jobs 分页 | E5、E6、F | 各自用例 |

## 完成 Gate 与剩余工作

- 全部任务提交并通过 V；设计稿「实施记录」补第二期并列出仍未覆盖的项（若有）。
