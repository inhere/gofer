<!-- template_id: plan; template_version: 1.2.0 -->
# 权限目录与授权 实施计划

> 状态：Draft 0.1 / 待人工计划批准
>
> - issue：gofer-kd8q（并入 gofer-6pw0 第二步的模型部分）
> - 思考模式：RIGOROUS
> - 核心目标：按已批准设计（Draft 0.7）分三批落地权限目录与授权；每批独立可发版；现有部署行为不变，只有设计里列出的有意变化
> - scope freeze：设计第一至六节、架构、接口、决策 1–11；不含第二期的按项目 / agent / runner 限定范围
> - review budget：一轮计划评审；无 CORE_BLOCKING 即停
> - 停止条件：用户批准本计划，或评审发现需要返回设计 Gate 的 Semantic Amendment

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-14 | Claude（监督会话） | 初稿 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 目标与完成定义

**目标**：实现已批准设计 `docs/design/2026-10-14-db-capabilities-and-session-grants-design.md`（Draft 0.7）。

**完成定义**：

1. 三批全部合入并发版：
   - 第一批：权限核心、路由声明、配置推出、种入、生成参考文档；
   - 第二批：授权管理、会话授权、长连接撤销、审计、whoami；
   - 第三批：web。
2. 四道测试兜底全部存在并通过：覆盖测试、InHandler 反向用例、调用断言、真值表对照。
3. 生成的 `docs/reference/permissions.md` 与代码一致，过期检查测试通过。
4. 主机实测通过：
   - 在 web 上给容器里的一个会话授予 `tunnel.open`，隧道可用；撤销后已建立的隧道断开；
   - 给监督会话授予「发版升级」，可以执行一键更新与 worker 升级；
   - 终端工具权限提示可在 web 上作答。

## 范围、排除项与授权

**范围**：设计稿第一至六节、架构、接口所列全部内容。

**排除**：
- 按项目、agent、runner 限定范围（第二期，gofer-6pw0）；
- 在 web 上新建调用方、生成 token；
- job 凭据白名单的改造；
- 冒用防护的加密手段。

**授权**：
- 本计划批准后，需要用户另行发出执行请求，才派 suga 实施。
- 发版（tag、push、升级 server 与容器）属于用户的长期授权；远程 worker 由用户在 web 上升级。
- 除此之外不做任何出机器动作。

- `host_or_non_offline_action=REQUIRED`

## 输入与批准证据

- 设计：`docs/design/2026-10-14-db-capabilities-and-session-grants-design.md` Draft 0.7（提交 `b2a2fd6d`）。批准原话（用户 2026-10-14）：「补上生成参考文档后，批准 权限目录与授权 设计，进入计划」。
- 评审：三轮评审与一次 closure 复核，结论记录在 gofer-kd8q 的评论里。
- 先行加固 gofer-hxld 已提交 `3c0e455c`（worker token 白名单、schedule token 掩码、xfer 归属、web 回答工具权限提示）。本计划以它为前提。
- 全量路由清单：设计稿附录 A（冻结快照，基线 `61627db2`）。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 权限判定（目录、模式匹配、真值表推出、Decision） | 现有 `callerMayAdmin` / `callerHoldsAdmin` / `callerMayAnswer` / `sessionMayAnswer` / `callerMayAttach` 等散落函数；`config` 的 `CallerCan*` | 否 | 否 | MINIMAL_NEW_MODULE | 现有判断是约 30 处各自为政的布尔函数，没有权限名、来源、模式、会话主体；放进 httpapi 会违反 G022（job 层也要用判定结果） | 旧函数在第一批迁移完后删除，只在测试里保留为真值表对照；避免两套判定并存 |
| CAP-02 | 路由声明与按路由包裹 | `jobcredential.go` 的路由词表与 SEC-01 白名单；gofer-hxld 的 worker 白名单 | 部分 | 原位扩展 httpapi（复用 jobcredential 的路由匹配方式） | OWNER_EXTENSION | — | 声明表成为唯一来源，jobcredential 与 worker 白名单改为从声明表派生 |
| CAP-03 | 授权、种入、审计存储 | `internal/jobstore` 的 additive 迁移、holds / sessions 表 | 部分 | 原位扩展 jobstore | OWNER_EXTENSION | — | 无 |
| CAP-04 | 参考文档生成与过期检查 | `go generate`、现有测试 | 是 | 无 | DIRECT_REUSE | — | 无 |
| CAP-05 | 长连接按依赖撤销 | `tunnel.Splice` 的 ctx、托管转发的 cancel、SSE 的请求 ctx、ws hub | 部分 | 原位扩展：在 httpapi 加一个登记表，现有连接包一层可取消的 ctx | OWNER_EXTENSION | — | 无 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NM-01 | CAP-01 | `internal/authz`（纯逻辑，只依赖 `config`） | httpapi 内的判定函数、`config` 的 `CallerCan*` | 现有函数没有权限名与来源，不能表达模式与会话主体 | 放在 httpapi：job 层的唤醒判定与 standalone mcp 也要用，违反 G022；放在 config：判定依赖 DB 授权与会话，超出 config 职责 | 需要同时被入口层、job 层（经入口传入）与生成器使用的纯判定库 | httpapi 维护者；随 gofer 版本演进；权限名一经发布不改名 | 不会被删除；旧判定函数在第一批 T1.4 完成后删除 |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 引入通用策略引擎（OPA、casbin 等） | CAP-01 | 删除测试：去掉它，目录 + 模式 + 真值表依然完整可用；它只会带来额外依赖与策略语言，用户要的是固定名称加简单通配 |
| 单独的权限服务进程 | CAP-01 | 删除测试：单机部署（G001）不需要；进程内判定即可 |

## 前置检查与 fail-closed 条件

- **工作区基线**（写计划时）：
  - Git root `inhere-tools/gofer`，分支 `main`，HEAD `b2a2fd6d`；
  - `git status --short`：`.gofer/tracker/*.jsonl` 为监督会话的 tracker 改动；`.commitmsg.txt` 是 suga 小项批次的临时文件，不属于本计划。
- **开工前提**：
  - suga 的小项批次（job `20261014-155959-4e6e06c8`：gofer-hxld / rhux / 11tz / gk74）已结束并完成验收；
  - 工作区里没有其他 agent 的未提交改动，有就停止（Ownership Conflict）；
  - 每批开工前先 `git pull --ff-only`，并记录 HEAD。
- **依赖**：Go 1.26（主机由 xenv 按 go.mod 切换）；web 在主机上 `pnpm` 已就绪。
- **fail-closed**：
  - 第一批的真值表测试没有逐格还原旧行为（除设计「有意不保持现状」所列项）→ 停止，不发版；
  - 覆盖测试发现有路由无法归入任何声明类别 → 停止，返回设计（Semantic Amendment）。

## 波次与依赖

```
第一批（T1.x）── 发版 v0.158 ─┐
                               ├─ 第二批（T2.x）── 发版 ─┐
                               │                         ├─ 第三批（T3.x）── 发版 ── 主机实测
```

- 每批内部按任务编号顺序执行，每个任务一个提交。
- 每批结束由监督会话做全量验证（Linux -race、Windows 全量、vet linux / windows / darwin、web）后发版。
- 实施者：suga（gofer job，`--runner local --cwd inhere-tools/gofer`），监督：本会话。

## 任务

### T1.1 `internal/authz`：目录与模式

- 文件：`internal/authz/catalog.go`、`internal/authz/pattern.go` 及测试（新包）
- 动作：
  - 登记 40 项权限，每项带说明、风险（低 / 中 / 高 / 危险 / 根）、可否授予会话、可否限定范围、来源（种入 / 配置 / 仅根）、种入版本；
  - 实现模式匹配与展开：`*` 通配；通配符不展开到危险级；`auth.manage` 永不匹配；会话主体只展开到可授予会话的权限；模式至少要匹配一项。
- 验证：`go test ./internal/authz/...`（在仓库根运行）
- 完成标准：
  - 目录 40 项与设计第一节逐行一致（测试里断言名称集合）；
  - 模式规则各有单测。
- 依赖：无

### T1.2 配置快照、根管理员与真值表推出

- 文件：`internal/authz/derive.go`、`internal/config/loader.go`、`internal/httpapi/server.go`（`SetServerConfig`）、测试
- 动作：
  - 由配置推出授权（设计第三节真值表），并判定根管理员：配置 `can_admin` 的用户类调用方；没有 callers 时为 `default`；allow_empty_token 部署没有根；
  - 新增 `capabilities: [...]` 配置写法；
  - loader 改为「开关打开时，存在根管理员即满足」；拒绝调用方 id 与 worker id 重名；
  - httpapi 用 `atomic.Pointer` 保存 {cfg, callers, 根集合, 推出授权} 不可变快照。
- 验证：
  - `go test ./internal/authz/... ./internal/config/...`；
  - **真值表测试**：4 个开关 × 4 个能力位 × {有 callers, 无 callers, allow_empty_token}，与旧函数（复制到测试文件里作为对照）逐格比较，除设计列出的有意变化外全部一致。
- 完成标准：真值表测试通过；配置重载并发测试（`-race`）通过。
- 依赖：T1.1

### T1.3 存储与种入

- 文件：`internal/jobstore/auth.go`、`internal/jobstore/store.go`（迁移）、测试
- 动作：
  - 新建 `auth_grants`、`auth_meta`、`auth_audit` 三张表，`agent_sessions` 加 `incarnation` 列并给存量行回填随机值；
  - 种入 v1（设计第三节列出的 16 项，发给 `users`），与建表放在同一迁移事务里，写 `seed_version`；
  - 提供授权的读取接口与版本号。
- 验证：`go test ./internal/jobstore/...`：首次种入；重启不重复种入；撤销后重启不还原；从旧库迁移。
- 完成标准：上述测试通过。
- 依赖：T1.1

### T1.4 路由声明框架与全部声明

- 文件：`internal/httpapi/authz_routes.go`（声明表）、`internal/httpapi/server.go`（注册时按路由包裹）、`internal/httpapi/jobcredential.go`（改为从声明派生）、gofer-hxld 的 worker 白名单（改为从声明派生）、测试
- 动作：
  - 实现 7 种声明类别，允许组合（用户侧要求 + JobCred），按路由包裹；
  - 把附录 A 的 310 条路由按设计第一节目录全部声明；
  - 解析器只对 user 类计算有效权限，根管理员拥有全部权限；
  - 测试构建下 `s.can` 记录已判定的权限。
- 验证：
  - **覆盖测试**：遍历 router，未声明即失败，权限名必须在目录里；
  - **InHandler 反向用例**：每条 InHandler 路由登记一个「缺权限、操作他人资源 → 403 或被过滤」用例；
  - **调用断言**：handler 返回后，每个声明的权限都被判定过。
- 完成标准：三类测试通过；`go test ./internal/httpapi/...` 全部通过。
- 依赖：T1.2、T1.3

### T1.5 迁移现有检查点、关闭旁路

- 文件：`internal/httpapi/` 下涉及的 handler（attach、interaction、job、session、session_permission、session_ask、work、tunnel、tunnel_hosted、tunnel_preset、push、decision、xfer、tracker、config_write、project、rules、skills、worker_*、update_apply、pty_*、secret_scan、plan、schedule、mcp）、`internal/job/wakeup.go`、`internal/webpush/`、`internal/tunnel/hosted.go`、`internal/serve/serve.go`、测试
- 动作：
  - 把全部旧判定函数换成 `s.can`（InHandler）或路由声明，按设计写明的无主资源规则处理；
  - 说话入口按目标分流（终端会话 → `session.reply.others`，会话 job → `job.manage.others`）；
  - 会产生 job 的入口另查 `job.submit`；
  - `wakeup.go` 不再读配置，由入口层传入判定；
  - 关闭旁路：推送按钮在签发和兑现时都检查权限，用户列表在配置重载时刷新；中继类决策按 `session.reply.others` 判定；托管转发改为进程内拨号并记录启动者，`tunnel.hosted` 另需 `tunnel.open`，autostart 不认会话授权；`xfer.manage.others` 仅根；
  - 删除旧判定函数。
- 验证：
  - `go test ./...`；
  - 真值表测试继续通过；
  - 新增旁路关闭用例：没有 `interaction.answer` 的调用方用推送按钮作答被拒；中继类决策作答被拒；没有 `tunnel.open` 的调用方开托管转发被拒。
- 完成标准：`grep -rn "callerMayAdmin\|callerHoldsAdmin\|callerMayAnswer\|sessionMayAnswer\|callerMayAttach\|CallerCanAdmin(" internal/httpapi internal/job --include=*.go` 在非测试代码中无结果；以上测试通过。
- 依赖：T1.4

### T1.6 参考文档生成与规则

- 文件：`internal/httpapi/authz_gen.go`（`go generate` 入口）、`docs/reference/permissions.md`（生成产物）、测试、`AGENTS.md`、`skills/gofer-usage/references/server-config.md`、`CHANGELOG.md`
- 动作：
  - 从目录与声明表生成参考文档，加过期检查测试；
  - AGENTS.md 加规则：新增或修改路由必须声明，新增权限要在目录登记，改完运行 `go generate`；
  - skill 的 references 链到生成文档；
  - CHANGELOG 写明有意变化：根管理员拥有全部权限；关闭三处旁路；单 token 部署里 `default` 成为根。
- 验证：`go generate ./... && git diff --exit-code docs/reference/permissions.md`；过期检查测试通过。
- 完成标准：生成文档已提交且与代码一致。
- 依赖：T1.5

### T2.1 授权管理接口与 whoami

- 文件：`internal/httpapi/auth_handler.go`、`internal/client/auth.go`、`internal/commands/auth.go`、测试
- 动作：
  - 实现设计「接口」一节的 7 个接口：`auth.manage` 只看根管理员；worker 与 job 凭据 403；403 响应写明缺哪个权限；
  - 孤儿授权：调用方在配置里不存在时标为孤儿并停用，`activate` 接口恢复；
  - 实现 `gofer auth whoami` 与 `gofer auth routes`；
  - 数据库授权缓存带版本号：本进程写入后立即刷新，其他进程按 5 秒 TTL 失效。
- 验证：`go test ./internal/httpapi/... ./internal/commands/...`，覆盖权限矩阵、孤儿、缓存失效。
- 完成标准：测试通过；`gofer auth whoami` 在容器里能显示有效权限及来源。
- 依赖：第一批发版

### T2.2 会话授权与化身

- 文件：`internal/jobstore/sessions.go`（所有改写 state 的 SQL）、`internal/sessionrelay/`、`internal/httpapi/`、`internal/client/`（会话头单一注入点：收敛 `client.go`、`watch.go`、`worker_upgrade.go` ×2、`xfer.go` 共 8 处）、`internal/job/credential.go`（禁止清单）、测试
- 动作：
  - 化身轮换：任何把状态从 `ended` 改成别的状态的写操作都换新，含接管；读时同时判定状态不是 `ended` 且 `ended_at` 为空；
  - 会话授权的四个生效条件；归属转移时，撤销授权与改 owner 在同一事务里完成；
  - CLI 附带会话 id 的规则（显式 `GOFER_SESSION_ID`，或去重后唯一）；三个会话变量加入 job 环境禁止清单。
- 验证：`go test ./internal/jobstore/... ./internal/sessionrelay/... ./internal/httpapi/... ./internal/client/...`，覆盖三种复活场景（结束 → 复活、结束 → 接管 → 释放、删除 → 重新登记）旧授权都不生效，以及多候选会话 id 时不附带。
- 完成标准：测试通过。
- 依赖：T2.1

### T2.3 长连接按依赖撤销

- 文件：`internal/httpapi/`（登记表；tunnels/connect、托管转发、attach ticket、SSE、ws）、`internal/tunnel/`、测试
- 动作：
  - 长连接按「调用方 id + 依赖授权集合」登记；
  - 撤销、到期、化身变化、会话结束，以及配置重载后调用方被删 / token 变化 / 根身份变化 / 推出授权变化时，重新判定，不再满足就断开；
  - ws 推送按 `job.read.others` / `session.read.others` 过滤 topic。
- 验证：`go test ./internal/httpapi/... ./internal/tunnel/...`，覆盖撤销后隧道断开、配置重载去掉能力位后断开、仍有权限的连接保留。
- 完成标准：测试通过。
- 依赖：T2.2

### T2.4 审计

- 文件：`internal/jobstore/auth.go`、`internal/httpapi/`、测试
- 动作：
  - 记录授权的增删、只靠会话授权才通过的请求、被拒请求（含原因）；
  - 保留 90 天，复用现有的清理时机。
- 验证：`go test ./internal/jobstore/... ./internal/httpapi/...`
- 完成标准：审计接口能按主体、权限、授权 id 过滤。
- 依赖：T2.1

### T3.1 web：设置 → 权限

- 文件：`web/src/views/settings/Permissions.vue`（新）、`SettingsLayout.vue`、`web/src/api/`、测试
- 动作：
  - 四个页签：授权（按主体分组，标来源与孤儿）、会话授权、权限目录、审计；
  - 新增授权：预设或模式；实时显示展开结果；高级别要确认，危险级要字面写出并填写原因；
  - 没有根管理员的部署，页面只读并给出提示。
- 验证：`cd web && pnpm exec vue-tsc --noEmit && pnpm exec vitest run`
- 完成标准：vitest 覆盖展开预览、危险级确认、只读模式。
- 依赖：第二批发版

### T3.2 web：Sessions 授权与按权限隐藏

- 文件：`web/src/views/Sessions.vue`、`web/src/components/SessionDrawer.vue`、`web/src/store/`（whoami）、相关测试
- 动作：
  - 会话行与抽屉加「授权」按钮与「已授权」徽标，弹窗只列可授予会话的权限，并写明威胁模型；
  - 全站按 whoami 隐藏没有权限的操作；403 时显示缺的权限名。
- 验证：同 T3.1。
- 完成标准：vitest 覆盖授权弹窗与按权限隐藏。
- 依赖：T3.1

### T3.3 发版与主机实测

- 文件：`CHANGELOG.md`
- 动作：
  - 发版；
  - 在主机 web 上逐项实测完成定义第 4 条：会话授权隧道、撤销后断开；监督会话授权发版升级；web 回答工具权限提示。
- 验证：实测记录（截图或日志）写进 gofer-kd8q 评论。
- 完成标准：三项实测通过。
- 依赖：T3.2

## 回滚与恢复

- 每个任务一个提交，每批一个版本，回滚到上一个版本即可。
- 新表与新列只增不改，旧版本会忽略它们。回滚后的影响（设计「安全、数据、运维与回滚」已写明）：
  - 数据库授权失效，收紧过的授权不会延续到旧版本；
  - 三处旁路重新出现。
- 采用新配置写法（只写根管理员、开关全开）的部署，回滚前要把能力位补回配置。
- 恢复点：每批发版前的 tag。

## 人工 Gate

1. **本计划批准**：用户明确批准本计划。
2. **执行请求**：用户另行发出「开始实施」，之后才派 suga。
3. **外部动作**：tag、push、升级 server 与容器属于用户的长期授权；远程 worker 由用户在 web 上升级；主机实测（T3.3）需要用户在 web 上操作。

## 可追溯性

| 设计条目 | 任务 | 验证 |
|---|---|---|
| 一、权限目录（40 项） | T1.1 | 目录名称集合测试 |
| 二、模式与展开、孤儿、预设 | T1.1、T2.1、T3.1 | 模式单测、孤儿测试、web 预设 |
| 三、配置推出、种入、loader、快照 | T1.2、T1.3 | 真值表测试、种入测试、并发测试 |
| 有意不保持现状的三处 | T1.5 | 旁路关闭用例 |
| 四、会话授权与化身 | T2.2 | 三种复活场景测试 |
| 五、工具权限提示 | gofer-hxld（已提交）、T1.5 | hxld 测试、迁移后回归 |
| 六、长连接撤销 | T2.3 | 撤销断开测试 |
| 架构：路由声明与四道兜底 | T1.4、T1.2 | 覆盖、反向、调用断言、真值表 |
| 架构：生成参考文档、AGENTS 规则 | T1.6 | 过期检查测试 |
| 接口 | T2.1 | 权限矩阵测试 |
| 审计 | T2.4 | 审计测试 |
| web | T3.1、T3.2 | vitest |
| 完成定义第 4 条 | T3.3 | 主机实测记录 |

## 完成 Gate 与剩余工作

- **每批完成条件**：
  - 本批任务的验证全部通过；
  - 监督会话的全量验证通过（Linux -race、Windows 全量、vet linux / windows / darwin、web）；
  - 发版 CI 全绿。
- **整体完成条件**：T3.3 主机实测通过，gofer-kd8q 关闭。
- **剩余**：第二期按项目 / agent / runner 限定范围，以及 steward 记录发起人，归入 gofer-6pw0。
