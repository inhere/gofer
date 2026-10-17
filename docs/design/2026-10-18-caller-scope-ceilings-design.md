<!-- template_id: design; template_version: 1.1.1 -->
# 调用方作用范围（上限：项目 / runner / exec / 只读） 设计

> 状态：Draft 0.2 / 待用户确认
> issue：gofer-6pw0

- 思考模式：rigorous。
- 核心目标：让 root 管理员能把某个调用方（一个 token）限制在指定项目、runner、worker 与 agent 范围内，禁止 exec，或设为只读；这些限制对该调用方直接或间接发起的一切（HTTP、它的 job 里的 agent、它创建的定时 / plan / todo、系统代它做的续投与重试）一致生效，且失败方向是关闭的。
- scope freeze：范围以本稿「范围与非目标」为准；实施中发现超出范围的需求另开 issue。
- review budget：一轮独立评审已用（针对 0.1）；0.2 按评审修订，经作者自查；实施计划另行评审。
- 停止条件：用户对本稿给出具名的批准，或明确要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-18 | Claude（监督会话） | 初稿：调用方上限作为只减不加的一层；判定与提交准入两处执行；只读；审阅归属；系统发起的提交按原调用方复核 |
| 0.2 | 2026-10-18 | Claude（监督会话） | 按独立评审重写：威胁模型单列（本机 runner、job 凭据读取、记忆注入、回滚）；上限所有者持久化为 job 的 `origin_caller` 并沿派生链继承，不再按 `CallerID` 字符串解析；准入单点放在 `submitAdmitted`、直接读 cfg 快照；路由声明增加「项目范围」与「读写」两个维度并由覆盖测试强制；内部作业由触发它的 HTTP 路径先做资源检查；job 凭据的读取收窄到上限；长连接按上限复核；`ceiling` 子树严格解码；三态字段；`runners` 含 `local`、新增 `workers`；永久拒绝的哨兵错误；todo 作者与派发者取较严；自审与 hold 批准的准入归属；standalone MCP 不在范围内 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 背景与目标

调用方（`server.callers[]`，每个一个 token）拆分后，普通用户类调用方仍然可以：取消、结束、续接、交付任何人的 job；向任意项目、任意 runner 提交 job（包括 server 本机 runner 上的 exec，等于主机 shell）；没有只读调用方。

现状（2026-10-18，v0.170.0）：

- 对他人 job 的操作已有归属检查（`internal/httpapi/authz_job_rules.go` 的 `ownerAccess`），但 `users` 主体的种子授权包含 `job.cancel.others`、`job.manage.others`、`job.submit`；授权只能叠加，无法把单个调用方收窄到 `users` 之下。
- 授权表的 `scope` 列第一期只接受 `*`，判定不看它（权限设计第二期内容）。
- 大量路由是 `Public()`、没有资源检查（plan、work item、tracker、记忆、项目文件、today / stats）。
- `CallerID` 是混用的命名空间：用户 caller id、job id（job 凭据提交的子 job）、agent 名（评论派发）、`gofer`（leader）、空串（webhook、内部作业、源 job 被清理后的重试）。
- 提交汇合点是 `submitAdmitted`（`internal/job/submit.go`）；定时 sweep 经 `SubmitWithPermit`、hold 批准 / update-pause / 升级准入各有重投路径，都汇到这里。job 层已读 cfg 快照做调用方并发限制（`CallerConcurrencyLimit`）。
- 配置解码是非严格的：未知键被静默忽略（`internal/config/loader.go` 注释、`configlint/known.go`）。

用户可见的目标：

- root 在配置里给某个调用方写上限：可访问的项目、可提交的 runner 与 worker、可用的 agent、是否允许 exec，或者只读。
- 上限是天花板：调用方的权限 = 现有权限 ∩ 上限允许的范围；上限只减不加，未配置上限的调用方行为不变。
- 限制对该调用方「直接或间接」发起的一切生效，解析不到归属时对受限调用方的派生链拒绝（失败关闭）。

## 名词

- **上限（ceiling）**：`server.callers[].ceiling`；未配置 = 无限制。
- **上限所有者（origin caller）**：对一个 job、plan、定时等，最终应受谁的上限约束的那个用户类调用方 id。持久化在记录上（job 的 `origin_caller`），派生时继承。
- **项目范围声明**：每条路由声明它的资源项目从哪里来（路径、资源、请求体、过滤、无项目），判定按此执行。
- **读写声明**：每条路由显式声明是否改变状态（只读调用方据此放行或拒绝）。
- **系统作业**：没有上限所有者的 gofer 自身作业（如升级、清理）；只能由不涉及受限调用方的路径触发。

## 威胁模型

上限是「防误用、防越权」的边界，前提是受限调用方拿不到主机执行能力：

1. **本机 runner 等于主机 shell**：本机 runner 上的 cli-agent / exec 能读 server 的配置与所有 token。因此存在 `ceiling` 时，**默认不允许本机 runner**（`runners` 未包含 `local` 即不允许）；若 root 显式允许本机 runner，加载时给出警告，文档写明此时上限不构成安全边界。
2. **job 里的 agent**：受限调用方提交的 job，其 agent 持有 job 凭据。job 凭据的读取与派生都按该 job 的上限所有者收窄（见「job 凭据」）。
3. **记忆与 tracker 注入**：`memory.write` 写的记忆会注入其它项目 job 的 prompt；受限调用方只能写其项目范围内的记忆（`scope_key` 为范围内项目），全局记忆需要显式允许。tracker 写同理按仓库所属项目约束。
4. **回滚**：旧版本会静默忽略 `ceiling`，回滚即放开限制。回滚前必须先撤销或轮换受限调用方的 token（runbook 写明），启动日志与 whoami 显示「已生效 N 个受限调用方」。

非威胁（不在本设计范围）：主机本地 CLI 与 standalone MCP（`gofer mcp --standalone`，同进程直接用 job.Service、无认证）属于主机信任域，不受上限约束（与权限设计 2026-10-14 一致）。

## 范围与非目标

范围：

- 配置 `ceiling`（严格解码）、校验、热加载、whoami 与启动日志。
- 上限所有者：job / plan / 定时 / todo 记录持久化；所有派生路径继承；解析规则与失败关闭。
- 准入：`submitAdmitted` 单点检查项目、runner、worker、agent、exec（含 Cmd / Verify / hooks argv），用 cfg 快照；永久拒绝的哨兵错误与各重投路径的终止处理。
- 判定：路由「项目范围」与「读写」两个声明维度，覆盖测试强制；`canFor` 三态（允许 / 无权 / 范围外）；列表、聚合、计数按范围过滤。
- 内部作业：触发它的 HTTP 路径先做资源项目检查；内部作业继承触发者的上限所有者；免检清单。
- job 凭据：读取与派生按所属 job 的上限所有者收窄。
- 长连接（attach、SSE、`/v1/ws` 主题、隧道、托管转发）按上限复核。
- 审阅：新权限 `job.review.others`；受限调用方不能审阅自己提交的 job（自审）。
- web：按上限过滤提交表单、隐藏不可用操作。
- 文档、runbook（含回滚）、权限参考。

非目标：

- 不启用授权表 `scope`（项目级授权，权限第二期另议）。
- 不改种子授权。
- 不对 root 管理员生效（配置 root 带上限是错误）。
- 不约束主机本地 CLI 与 standalone MCP。
- 不做角色。

## 架构

### 配置

```yaml
server:
  callers:
    - id: ci-bot
      token_env: GOFER_CI_TOKEN
      ceiling:
        projects: ["app-*", "docs"]   # 可访问的项目（glob）；省略 = 不限
        runners: ["w-linux-*"]        # 可提交到的 runner（glob，含 local / server）；省略 = 不含本机 runner 的全部
        workers: ["w-linux-*"]        # 可被路由到的 worker（glob）；省略 = 不限
        agents: ["codex", "claude"]   # 可用的 agent；省略 = 不限
        allow_exec: false             # *bool；省略 = false（存在 ceiling 时默认不允许 exec）
        global_memory: false          # *bool；省略 = false（不能写全局记忆）
        read_only: false              # *bool；省略 = false
```

- **严格解码**：`ceiling` 子树按已知键严格解码，未知键（拼写错误）加载失败；空的 `ceiling: {}` 报错（避免误以为已限制）；`configlint` 登记新键。
- **默认更严**：存在 `ceiling` 时，本机 runner 与 exec 默认不允许，须显式写 `runners: [local]` / `allow_exec: true`；显式允许本机 runner 时加载给出警告。
- 校验：glob 合法；给 root（复用 `authz.Derive` 的根推导，含单 token 的 `default`）写 `ceiling` 是错误。
- 热加载：每次判定读当前 cfg 快照；字段策略登记为「仅文件、热加载」（`server.callers.*.ceiling`），与 `server.callers` 其余项的分类统一核对。
- whoami 返回本人的 `ceiling`；`GET /v1/config` 对 `ceiling` 的展示与 token 同等脱敏规则核对（只给 `config.read`，不含 token）。

### 上限所有者（origin caller）

- jobs 表新增 `origin_caller`（additive）；plans、schedules、todos 记录创建者（已有字段复用，没有则新增）。
- 提交时由入口层决定上限所有者并写入：
  - 用户类调用方的 HTTP 请求：调用方本人。
  - job 凭据的请求（子 job、评论派发、job 内调 gofer）：所属 job 的 `origin_caller`。
  - 定时到点 / webhook 触发：定时的创建者。
  - plan / todo 派发（含续接、leader 回合）：todo 作者与派发者两者上限的**较严者**（实现为两者都检查）；leader 回合取 plan 的 owner。
  - 自动续投、重试、失败转移、hold 批准重投、update-pause 重投、续接载体：继承源 job 的 `origin_caller`。
  - 内部作业（会话中继 messenger / inject、交付、tracker sync、摘要器、管家）：继承触发它的调用方（HTTP 路径）的上限所有者。
  - 系统作业：空，免检；免检清单在代码里登记并有测试断言它们不能由受限调用方的路径触发。
- 解析不到（例如源 job 已清理）时：若派生链上出现过受限调用方则拒绝（失败关闭），否则视为系统作业。
- 评论派发的 job 的 `CallerID` 不再取 author 名，改取评论发起者的上限所有者（或在 `CallerID` 保留展示值、另用 `origin_caller` 判定——实现择一，避免 agent 名与 caller id 撞名）。

### 准入（job 层）

- 新纯函数 `cfg.Server.CallerCeiling(id)`；`submitAdmitted` 与 `validate(cfg, …)` 用同一 cfg 快照检查上限所有者的上限（job 层读配置有先例，判定不依赖 httpapi 的 decision）。
- 检查项：项目 ∈ `projects`；runner ∈ `runners`（经 `NormalizeRunnerName`，本机 runner 需显式允许）；路由后的 worker ∈ `workers`；agent 用 `gateAgentOf(req)`（resume 载体按 `ResumeSourceAgent`）∈ `agents`；exec：`Agent=exec`、`Cmd`、`Verify` argv、hooks 任一出现都需 `allow_exec`，且**与 runner 是否远程无关**。
- 检查在 `validate` 内、任何副作用（技能挂载、worktree、锁）之前。
- 拒绝返回哨兵错误 `ErrCeilingDenied`（HTTP 映射 403，带原因）；重试扫描遇到它用 `CancelRetry` 终止并在源 job 记事件；自动续投、失败转移、wakeup、todo 派发、定时运行同样终止并记录，不无限重试。

### 判定（httpapi）

- 路由声明增加两个维度（缺省即失败，`TestEveryRouteIsDeclaredOrPending` 同类测试强制）：
  - **项目范围**：`PathProject(param)`、`Resource(kind)`（job / plan / schedule / session / work item / tracker repo / memory，按记录的项目）、`BodyProject`（handler 绑定后必须调用 `s.requireProjectScope`，测试断言调用发生）、`Filter`（列表 / 聚合按范围过滤）、`NoProject`（显式声明与项目无关，如 whoami）。
  - **读写**：`Mutating()` / `ReadSafe()` 显式标注；GET 默认读、非 GET 默认写，覆盖时必须写明。初始 `ReadSafe` 集合至少含 `POST /v1/ws-ticket`、个人偏好类写入（推送订阅、工作台布局、已读标记、Today snooze）、读语义 POST（secret-scan、模板渲染、digest、合并建议）；`GET /v1/tunnels/connect`、attach 等数据面连接标为 `Mutating`。
- `decision` 带上限；`s.canFor(c, perm, res)` 返回三态：允许 / 无权（403，现有语义）/ 范围外（404，与不存在同形）。范围外的写也是 404。只读拒绝在 gate 阶段、先于资源查找，返回 403。
- 没有项目的资源（空项目的会话、work item、plan，隧道）：存在 `ceiling` 且 `projects` 非空时视为范围外（失败关闭）；隧道与 `tunnel.open` 的关系：上限不能放宽 `tunnel.open`（它来自配置，不种给 users），文档写明。
- 列表、计数、today、stats、steward 等聚合按范围计算，不泄露范围外的数量。

### job 凭据

- `can` 对 job 凭据不再一律放行：job 凭据的判定按其 job 的 `origin_caller` 的上限做项目范围检查（读 job / 日志 / 项目文件 / work item / 记忆 / tracker）；SEC-01 的写白名单不变。
- job 凭据派生（子 job、评论派发）经准入时用所属 job 的 `origin_caller`。

### 长连接

- `liveConn` 登记时记录资源项目（或可重求值的闭包）；`liveConnStillAllowed` 的周期复核加入上限与只读判定（上限收紧后，已打开的 attach / SSE / 隧道 / 托管转发在复核周期内断开）。
- `/v1/ws` 的 `job:<id>` 主题对「自己的 job」也要过项目范围；`jobs` / `sessions` 全局失效通知对受限调用方按项目过滤事件或拒绝订阅（实施时二选一，倾向过滤）。

### 审阅

- 新权限 `job.review.others`（中风险，`ScopeYes`，新种子批次补种给 `users`，保持现状）；审阅他人的 job 需要它，路由由 `Require(job.review)` 改为 InHandler 判定。
- 受限调用方不能审阅（accept / reject / approve）自己作为上限所有者的 job（防止机器人自批）；未受限的调用方保持现状。
- hold 批准后的重投按**原提交者**的上限准入（批准者只是放行，不改变 job 的身份）。

### 审计

- 上限拒绝写 `auth_audit`（调用方、动作、资源、原因），限频键为（调用方, 原因, 资源类型），每分钟每键一条。

## 关键流程

- **受限调用方提交到范围外项目**：路由门通过（有 `job.submit`）→ `submitAdmitted` 用 cfg 快照取上限 → 项目不在范围 → `ErrCeilingDenied` → 403「调用方上限不允许：项目 x 不在范围内」→ 审计。
- **它的 job 里的 agent 读别的项目的日志**：job 凭据 → 按所属 job 的 `origin_caller` 上限判定 → 范围外 → 404。
- **它创建的定时任务在上限收紧后到点**：准入按创建者上限拒绝 → 记一次运行失败并通知，定时本身保留（不删除）。
- **回滚**：先撤销 / 轮换受限 token → 再换旧二进制（旧版本会忽略 `ceiling`）。

## 安全、数据、运维与回滚

- 数据：jobs 新增 `origin_caller`；plans / schedules / todos 的创建者字段按需新增（additive）。
- 失败方向：解析不到上限所有者、空项目资源、未声明的路由——对受限调用方一律拒绝。
- 回滚风险：见威胁模型第 4 条；runbook 写明步骤。
- 观测：启动日志与 whoami 显示已生效的受限调用方数量；拒绝有审计。

## 测试计划

- 配置：严格解码（未知键、空 ceiling 报错）、默认更严（本机 runner / exec）、给 root 写上限报错、热加载。
- 上限所有者：每条派生路径一条用例——job 凭据子 job、评论派发（用户与 job 凭据两种）、定时到点、webhook、plan / todo 派发（作者与派发者不同）、leader 回合、自动续投、重试、失败转移、hold 批准、update-pause 重投、续接；源 job 已清理时失败关闭。
- 准入：项目 / runner / worker / agent / exec（Cmd、Verify、hooks、远程 runner 也检查）；resume 载体用真实 agent；`ErrCeilingDenied` 终止重试。
- 判定：路由两个维度的覆盖测试；三态与状态码（范围外 404、无权 403、只读 403）；空项目资源；聚合计数不泄露；表驱动覆盖每种资源类型；受限调用方对所有 `Public` 路由的反向矩阵。
- 内部作业：会话中继、交付、tracker sync、摘要器、管家由受限调用方触发时的资源检查与继承；免检清单不可被受限路径触发。
- job 凭据：读取范围收窄。
- 长连接：上限收紧 / 只读后已打开的连接在复核周期内断开；`/v1/ws` 主题过滤。
- 审阅：`job.review.others` 反向用例；受限调用方自审被拒；hold 批准按原提交者准入。
- 回归：未配置上限的调用方行为不变（现有测试不改断言）。

## 文档

- `docs/reference/permissions.md`（`go generate`）、`docs/reference/http-api.md`（whoami 的 `ceiling`、403 / 404 语义）。
- `skills/gofer-usage/references/server-config.md`（`ceiling`、默认更严、威胁模型摘要）、`web-console.md`。
- runbook：给 agent / CI 发受限 token 的推荐做法；回滚步骤（先撤销 token）。
- CHANGELOG。

## 决策

1. **用上限（只减不加）而不是启用授权 scope**：推荐上限。备选：启用授权 `scope`，需要先改种子、影响面大，留作权限第二期。
2. **上限写在配置文件、严格解码、热加载**：推荐。备选：数据库 + web 管理。
3. **存在上限时默认不允许本机 runner 与 exec**：推荐（本机 runner 等于主机 shell）。备选：省略即不限、只警告。
4. **上限所有者持久化并沿派生链继承，解析不到时对受限链失败关闭**：推荐。备选：只在入口检查，派生路径不复核。
5. **job 凭据的读取收窄到上限**：推荐（否则受限调用方可借 job 里的 agent 读全部项目）。备选：作为已知限制写进文档。
6. **审阅**：推荐新增 `job.review.others`（补种给 users 保持现状）+ 受限调用方禁止自审。备选：只加 `.others`，不限自审。
7. **范围外资源**：推荐 404（与不存在同形），范围内无权仍 403。
8. **plan / todo 作者与派发者不同**：推荐两者上限都检查（取较严）。备选：只按派发者。
9. **实施分期**：推荐分两期——第一期：配置、上限所有者、准入单点、job 凭据收窄、只读、路由声明维度与覆盖测试（全部路由先标注，`Public` 路由逐个归类）；第二期：长连接复核、`/v1/ws` 主题过滤、聚合计数、审阅自审。第一期发布时文档写明第二期前的已知限制。备选：一期完成。

## 待确认事项

- 决策 1–9 采用推荐值还是备选。

## 结论与人工计划 Gate

本稿经用户批准后出实施计划（按决策 9 分期）。

## 实施记录

（实施后补充。）
