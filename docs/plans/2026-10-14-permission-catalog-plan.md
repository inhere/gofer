<!-- template_id: plan; template_version: 1.2.0 -->
# 权限目录与授权 实施计划

> 状态：Draft 0.2 / 已批准（用户 2026-10-15：「批准权限实施计划 Draft 0.2」；candidate commit f61f5d79，document_revision 0.2），待执行请求
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
| 0.2 | 2026-10-14 | Claude（监督会话） | 按计划评审修订：第一批解析器读取种入的 users 授权；路由声明与迁移按领域拆成同提交的子任务，覆盖测试带待迁移清单；job / worker 凭据白名单不改来源，只标注并加快照测试；删去设计外的 capabilities 写法；存量 autostart 预设的启动者规则；补齐弃用标记、孤儿 / 展开快照、会话有效期校验、使用计数、威胁模型与 standalone 文档、各批文档同步；小项批次先单独发版 |

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
- 版本安排：suga 小项批次（gofer-hxld 等）先单独发版（设计决策 11「先行加固先上线」）；本计划第一批从下一个版本开始。

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
| CAP-02 | 路由声明与按路由包裹 | `jobcredential.go` 的路由词表与 SEC-01 白名单；`workercredential.go`（gofer-hxld）的 worker 白名单 | 部分 | 原位扩展 httpapi（复用 jobcredential 的路由匹配方式） | OWNER_EXTENSION | — | 声明表是用户侧权限的唯一来源；SEC-01 与 worker 白名单**保持现有权威来源**，声明表只引用它们的键作标注，并有快照测试保证一致 |
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

- **升级前检查**：每批发版前，先用新二进制对主机配置跑 `gofer config validate`（新增了调用方 id 与 worker id 重名的拒绝规则）；备份 jobstore 数据库。
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
小项批次（gofer-hxld / rhux / 11tz / gk74）── 单独发版
  └─ 第一批：T1.1 → T1.2 → T1.3 → T1.4 → T1.5a…T1.5i → T1.6 → T1.7 ── 发版
       └─ 第二批：T2.1 → T2.2 → T2.3 → T2.4 ── 发版
            └─ 第三批：T3.1 → T3.2 → T3.3 ── 发版与主机实测
```

- 每个任务（含每个领域子任务）一个提交，提交时测试全绿。
- 每批结束由监督会话做全量验证（Linux -race、Windows 全量、vet linux / windows / darwin、web）后发版。
- 实施者：suga（gofer job，`--runner local --cwd inhere-tools/gofer`）；监督：本会话。
- 403 文案分批：
  - 第一批写「请让根管理员授予 <perm>（`gofer auth whoami` 查看当前权限）」；
  - 第三批 web 上线后改为指向「设置 → 权限」。

## 任务

### T1.1 `internal/authz`：目录与模式

- 文件：`internal/authz/catalog.go`、`internal/authz/pattern.go` 及测试（新包）
- 动作：
  - 登记 40 项权限，每项带说明、风险（低 / 中 / 高 / 危险 / 根）、可否授予会话、可否限定范围、来源（种入 / 配置 / 仅根）、种入版本；
  - 实现模式匹配与展开：`*` 通配；通配符不展开到危险级；`auth.manage` 永不匹配；会话主体只展开到可授予会话的权限；模式至少要匹配一项；
  - 实现展开快照（供第二批「有新权限待确认」使用）。
- 验证：`go test ./internal/authz/...`（仓库根）
- 完成标准：目录 40 项与设计第一节逐行一致（名称集合断言）；模式规则各有单测。
- 依赖：无

### T1.2 配置快照、根管理员与真值表推出

- 文件：`internal/authz/derive.go`、`internal/config/loader.go`、`internal/httpapi/server.go`（`SetServerConfig`）、测试
- 动作：
  - 由配置推出授权（设计第三节真值表）；
  - 判定根管理员：配置 `can_admin` 的用户类调用方；没有 callers 时为 `default`；allow_empty_token 部署没有根；
  - loader：开关打开时「存在根管理员即满足」；拒绝调用方 id 与 worker id 重名；
  - 没有根管理员时，启动日志给出提示；
  - httpapi 用 `atomic.Pointer` 保存 {cfg, callers, 根集合, 推出授权} 不可变快照；
  - 旧能力位（`can_answer` / `can_attach` / `can_tunnel`）与四个 governance 开关加 `DEPRECATED(v<本版>): remove in v<+3>` 标记，启动时检测到使用就提示「请改用 web 授权」（G032、设计决策 3）。
- 验证：
  - `go test ./internal/authz/... ./internal/config/...`；
  - **真值表测试**：4 个开关 × 4 个能力位 × {有 callers, 无 callers, allow_empty_token} × {资源有主, 资源无主}，与旧函数（复制到测试文件里作为对照）逐格比较，除设计列出的有意变化外全部一致；
  - 配置重载并发测试带 `-race`。
- 完成标准：上述测试通过。
- 依赖：T1.1

### T1.3 存储与种入

- 文件：`internal/jobstore/auth.go`、`internal/jobstore/store.go`（迁移）、测试
- 动作：
  - 一次建齐三张表的全部列：`auth_grants`（含会话、孤儿、展开快照、使用计数各列，第二批不再迁移）、`auth_meta`、`auth_audit`；
  - `agent_sessions` 加 `incarnation` 列；回填写成每次启动都幂等执行（`WHERE incarnation IS NULL OR incarnation=''`），覆盖回滚期间旧版本新建的行；
  - 种入 v1（设计第三节的 16 项，发给 `users`），与建表放在同一迁移事务里，写 `seed_version`；
  - 提供授权读取接口与版本号。
- 验证：`go test ./internal/jobstore/...`，覆盖：首次种入；重启不重复种入；撤销后重启不还原；从旧库迁移；回填幂等。
- 完成标准：上述测试通过。
- 依赖：T1.1

### T1.4 路由声明框架与解析器

- 文件：`internal/httpapi/authz_routes.go`（声明表）、`internal/httpapi/authz_resolve.go`、`internal/httpapi/server.go`（注册时按路由包裹）、测试
- 动作：
  - 实现 7 种声明类别，允许组合（用户侧要求 + JobCred / WorkerSelf），按路由包裹；
  - **`JobCred(key)` 与 `WorkerSelf` 只是标注**：引用 `jobcredential.go`（SEC-01）与 `workercredential.go`（gofer-hxld）里现有的键，两份白名单仍是权威来源，本计划不改它们；
  - 解析器只对 user 类计算有效权限：
    - 根管理员拥有全部；
    - 其他调用方 = 配置推出 ∪ **数据库里 `subject=users` 且未撤销的授权**；
    - 启动时加载一次即可，第一批没有写接口；
    - 忽略 `caller:` / `session:` 主体与不认识的字段，这样第二批回滚到第一批时不会误读；
  - 判定记录用测试钩子（`TestMain` 中注入），不用 build tag。
- 验证：
  - **覆盖测试**：遍历 router，每条路由要么已声明，要么在「待迁移」清单里；声明里的权限名必须在目录里；
  - **白名单快照测试**：
    - 声明表引用的 SEC-01 键集合，与 `jobWriteAllowlist`、`stewardReadAllow`、`stewardWriteAllow` 逐项相等；
    - WorkerSelf 集合（含方法）与 `workercredential.go` 的 4 条逐项相等；
    - `GET /v1/xfer/{id}/content` 写成 `InHandler(xfer.manage.others) + WorkerSelf` 的组合；
  - 种入后用非根调用方提交 job 得 200。
- 完成标准：上述测试通过；「待迁移」清单此时包含全部尚未迁移的路由。
- 依赖：T1.2、T1.3

### T1.5a–T1.5i 按领域声明并迁移检查点

每个领域一个子任务、一个提交。每个子任务在同一个提交里完成：
- 给本领域路由写声明；
- 把 handler 里的旧判定换成 `s.can`（按设计的无主资源规则）；
- 加 InHandler 反向用例与调用断言；
- 从「待迁移」清单里删掉本领域的路由。

| 子任务 | 领域 | 主要文件 | 要点 |
|---|---|---|---|
| T1.5a | jobs 与 workflow、retry、wakeup | `job_handler.go`、`job_delete_handler.go`、`job_redact_handler.go`、`attach_ticket_handler.go`、`pty_*`、`session_job_handler.go`、workflow / retry / wakeup handler、`internal/job/wakeup.go` | `job.*`；会产生 job 的入口另查 `job.submit`；`wakeup.go` 不再读配置，由入口层传入判定；`gofer mcp --standalone` 传入「全部允许」 |
| T1.5b | interactions 与决策、推送 | `interaction_handler.go`、`decision_handler.go`、`push_handler.go`、`internal/webpush/` | `interaction.answer`；中继类决策按 `session.reply.others`；推送按钮在签发和兑现时都检查，用户列表在配置重载时刷新 |
| T1.5c | 终端会话 | `session_handler.go`、`session_permission_handler.go`、`session_ask_handler.go`、`session_owner_handler.go`、`work_handler.go`、workbench | 说话入口按目标分流；outbox 归 `session.reply.others`；工具权限提示由 owner 或持有 `session.permission.answer` 的作答；无主会话视为任何用户都是 owner |
| T1.5d | plans 与 todos、work items | `plan_handler.go`、todo / work-item handler | `plan.*`；todo PATCH、plan PATCH 开启 leader 等产生 job 时另查 `job.submit` |
| T1.5e | schedules | schedule handler | `schedule.manage.others`；run-now、enable 另查 `job.submit` |
| T1.5f | 隧道 | `tunnel_handler.go`、`tunnel_hosted_handler.go`、`tunnel_preset_handler.go`、`tunnel_forwarder_handler.go`、`internal/tunnel/hosted.go`、`internal/serve/serve.go` | `tunnel.*`；托管转发改为进程内拨号并记录启动者；存量 autostart 预设以 `UpdatedBy` 为启动者，不是现存用户类调用方时按根管理员处理并输出告警（加 `DEPRECATED` 标记，写进 CHANGELOG） |
| T1.5g | xfer、tracker、记忆、steward | `xfer_handler.go`、`tracker_*`、memory handler、steward handler | `xfer.*`、`tracker.*`、`memory.write`、`steward.manage` |
| T1.5h | 配置、项目、规则、skills、MCP、worker 管理与升级、更新 | `config_write_handler.go`、`project_handler.go`、`rules_handler.go`、`skills_handler.go`、`mcp_handler.go`、`worker_*`、`update_*` | admin 组与 `mcp.test`、`update.*`、`worker.*`；`update.apply` 带 workers 时另需 `worker.upgrade`；`source=staged` 另需 `worker.upgrade.upload` |
| T1.5i | 其余与收尾 | 剩余路由 | 清空「待迁移」清单；删除旧判定函数 |

- 验证（每个子任务）：`go test ./internal/httpapi/... ./internal/job/... ./internal/webpush/... ./internal/tunnel/...`；真值表测试持续通过。
- 旁路关闭用例分别在 T1.5b、T1.5f、T1.5g 里补上：
  - 没有 `interaction.answer` 的调用方用推送按钮作答被拒；
  - 中继类决策作答被拒；
  - 没有 `tunnel.open` 的调用方开托管转发被拒；
  - 他人 xfer 下载被拒；
  - 升级后存量 autostart 预设仍然启动。
- 完成标准（T1.5i）：
  - 「待迁移」清单为空；
  - 下面的 grep 在非测试代码里没有结果（authz 的推出逻辑除外）：
    `grep -rnE "callerMayAdmin|callerHoldsAdmin|callerMayAnswer|sessionMayAnswer|callerMayAttach|upgradeAdmin|humanReviewer|canWakeJob|CallerCan(Admin|Answer|Attach|Tunnel)\(|Governance\.Require|callerOwnsOrAdmins" internal/httpapi internal/job --include=*.go`

    （T1.5g 审阅补充：清单加上 `callerOwnsOrAdmins`，它随本任务的 schedule 掩码改造一并删除。T1.5i 实测残余只有一处
    有据可查：`config_handler.go` 回显已废弃的 `governance.require_*` 开关——那是旧配置面本身（G032 保留到
    `remove in v<+3>`），不是判定。另外 `internal/job` 里那条只判归属、不再读配置的 wakeup 规则改名为
    `callerMayActOnWakeup`（原 `canWakeJob` 是读配置的旧判定，名字随实现一起换掉）。）

    T1.5a 审阅补充（本任务完成）：`POST /v1/jobs/{id}/comments` 的 `job.submit` 检查——声明改为
    `InHandler(job.submit, deny)`，handler 在写评论前先问「这次会不会派 job」。job 层为此抽出纯判定 seam
    `CommentWouldDispatch` / `CommentAsJobWouldDispatch`（复用 `dispatchCommentMentions` 的存在与项目白名单
    闸，不写任何东西、不读节流状态）；权限判定仍在 httpapi（G022）。只有会派发的评论才查权限，普通评论不受影响。

    另：T1.5g 审阅补充的 schedule `trigger_token` 掩码已改为「只有 owner 与根管理员看明文」——原补充里的
    `schedule.manage.others` 分支会让默认部署（种入）的每个用户类调用方都读到该机密，与「默认部署结果须与现状
    一致」相矛盾，已按审阅意见回到 gofer-hxld 的口径。
- 依赖：T1.4；子任务之间按表中顺序。

### T1.6 参考文档生成与规则

- 文件：`internal/httpapi/authz_gen.go`（`go generate` 入口）、`docs/reference/permissions.md`（生成产物）、测试、`AGENTS.md`、`skills/gofer-usage/references/server-config.md`
- 动作：
  - 从目录与声明表生成参考文档，加过期检查测试；
  - AGENTS.md 加规则：新增或修改路由必须声明，新增权限要在目录登记，改完运行 `go generate`；
  - skill 的 references 链到生成文档。
- 验证：`go generate ./internal/httpapi/... && git diff --exit-code docs/reference/permissions.md`（仓库根）；过期检查测试通过。
- 完成标准：生成文档已提交且与代码一致。
- 依赖：T1.5i

### T1.7 第一批文档与 CHANGELOG

- 文件：`CHANGELOG.md`、`docs/reference/http-api.md`、`skills/gofer-usage/references/server-config.md`、设计稿（威胁模型补充）
- 动作：
  - CHANGELOG 写明有意变化：
    - 根管理员拥有全部权限，含开关关闭时代答、回答他人工具权限提示、attach 无主 job；
    - 单 token 部署里 `default` 成为根；
    - 关闭三处旁路；
    - 存量 autostart 预设的启动者规则；
    - 旧能力位与开关弃用；
  - 写进文档：威胁模型中 steward 的残留旁路；`gofer mcp --standalone` 是受信的本地入口。
- 验证：人工核对 CHANGELOG 与设计「有意不保持现状」逐条对应。
- 完成标准：文档已提交。
- 依赖：T1.6

### T2.1 授权管理接口与 whoami

- 文件：`internal/httpapi/auth_handler.go`、`internal/client/auth.go`、`internal/commands/auth.go`、测试、`docs/reference/http-api.md`、skill references、`CHANGELOG.md`
- 动作：
  - 实现设计「接口」一节的 7 个接口：`auth.manage` 只看根管理员；worker 与 job 凭据 403；
  - 解析器扩展为读取 `caller:` 主体的数据库授权；
  - 孤儿授权：调用方在配置里不存在时标为孤儿并停用；同一 id 重新出现时仍保持停用，由 `activate` 接口恢复；
  - 保存授权时记录展开快照，目录升级后标记「有新权限待确认」；
  - 实现 `gofer auth whoami`（含会话 id 的附带情况与来源、没有根管理员时的提示）与 `gofer auth routes`；
  - 数据库授权缓存带版本号：本进程写入后立即刷新，其他进程按 5 秒 TTL 失效；
  - 同步文档：`/v1/auth/*`、`gofer auth whoami | routes`。
- 验证：`go test ./internal/httpapi/... ./internal/commands/...`，覆盖权限矩阵、孤儿、展开快照、缓存失效。
- 完成标准：测试通过；`gofer auth whoami` 在容器里能显示有效权限及来源。
- 依赖：第一批发版

### T2.2 会话授权与化身

- 文件：
  - `internal/jobstore/sessions.go`（所有改写 state 的 SQL）、`internal/sessionrelay/`、`internal/httpapi/`；
  - `internal/client/`：会话头单一注入点，收敛 `client.go` ×4、`watch.go`、`worker_upgrade.go` ×2、`xfer.go` 共 8 处；
  - `internal/job/credential.go`（禁止清单）；
  - 测试、`docs/reference/http-api.md`、skill references（`sessions-relay.md`）、`CHANGELOG.md`。
- 动作：
  - 化身轮换：任何把状态从 `ended` 改成别的状态的写操作都换新，含接管；读时同时判定状态不是 `ended` 且 `ended_at` 为空；
  - 会话授权的四个生效条件；服务端校验 session 主体的 `expires` 必填、不超过 24 小时，「会话结束」也带上限；归属转移时，撤销授权与改 owner 在同一事务里完成；
  - 解析器扩展为读取 `session:` 主体；
  - CLI 附带会话 id：显式 `GOFER_SESSION_ID`，或去重后唯一；三个会话变量加入 job 环境禁止清单；
  - 文档写明 `X-Gofer-Session-Id` 的来源规则，以及 `GOFER_SESSION_ID` 会被 `tunnel forward -d` 等后代进程继承。
- 验证：`go test ./internal/jobstore/... ./internal/sessionrelay/... ./internal/httpapi/... ./internal/client/...`，覆盖：
  - 三种复活场景（结束 → 复活、结束 → 接管 → 释放、删除 → 重新登记）下旧授权都不生效；
  - 会话 id 有多个候选时不附带；
  - `expires` 越界被拒。
- 完成标准：测试通过。
- 依赖：T2.1

### T2.3 长连接按依赖撤销

- 文件：`internal/httpapi/`（登记表；tunnels/connect、托管转发、attach ticket、SSE、ws）、`internal/tunnel/`、测试
- 动作：
  - 长连接按「调用方 id + 依赖授权集合」登记；
  - 以下情况发生时重新判定，不再满足就断开：撤销、到期、化身变化、会话结束；配置重载后调用方被删、token 变化、根身份变化、推出授权变化；
  - ws 推送按 `job.read.others` / `session.read.others` 过滤 topic。
- 验证：`go test ./internal/httpapi/... ./internal/tunnel/...`，覆盖撤销后隧道断开、配置重载去掉能力位后断开、仍有权限的连接保留。
- 完成标准：测试通过。
- 依赖：T2.2

### T2.4 审计与使用计数

- 文件：`internal/jobstore/auth.go`、`internal/httpapi/`、测试
- 动作：
  - 记录授权的增删、只靠会话授权才通过的请求（同时更新该授权的 `last_used_at` / `use_count`）、被拒的请求（含原因）；
  - 保留 90 天，复用现有的清理时机。
- 验证：`go test ./internal/jobstore/... ./internal/httpapi/...`
- 完成标准：审计接口能按主体、权限、授权 id 过滤；使用计数正确。
- 依赖：T2.1

### T3.1 web：设置 → 权限

- 文件：`web/src/views/settings/Permissions.vue`（新）、`SettingsLayout.vue`、`web/src/api/`、测试
- 动作：
  - 四个页签：授权（按主体分组，标来源、孤儿与「有新权限待确认」）、会话授权、权限目录、审计；
  - 新增授权：选预设或填模式，实时显示展开结果；高级别要确认，危险级要字面写出并填写原因；
  - 没有根管理员的部署，页面只读并给出提示；
  - 403 文案改为指向「设置 → 权限」。
- 验证：`cd web && pnpm exec vue-tsc --noEmit && pnpm exec vitest run`
- 完成标准：vitest 覆盖展开预览、危险级确认、只读模式、新权限待确认。
- 依赖：第二批发版

### T3.2 web：Sessions 授权与按权限隐藏

- 文件：`web/src/views/Sessions.vue`、`web/src/components/SessionDrawer.vue`、`web/src/store/`（whoami）、相关测试
- 动作：
  - 会话行与抽屉加「授权」按钮与「已授权」徽标；弹窗只列可授予会话的权限，有效期选 1 / 4 / 24 小时或「会话结束」，并写明威胁模型；
  - 全站按 whoami 隐藏没有权限的操作；403 时显示缺的权限名。
- 验证：同 T3.1。
- 完成标准：vitest 覆盖授权弹窗、有效期选项与按权限隐藏。
- 依赖：T3.1

### T3.3 发版与主机实测

- 文件：`CHANGELOG.md`
- 动作：
  - 发版；
  - 在主机 web 上逐项实测完成定义第 4 条：会话授权隧道与撤销后断开；监督会话授权发版升级；web 回答工具权限提示。
- 验证：实测记录写进 gofer-kd8q 评论。
- 完成标准：三项实测通过。
- 依赖：T3.2

## 回滚与恢复

- 每个任务一个提交，每批一个版本，回滚到上一个版本即可。
- 新表与新列只增不改，旧版本会忽略它们。回滚后的影响（设计「安全、数据、运维与回滚」已写明）：
  - 数据库授权失效，收紧过的授权不会延续到旧版本；
  - 三处旁路重新出现。
- 采用新配置写法（只写根管理员、开关全开）的部署，回滚前要把能力位补回配置。
- 恢复点：每批发版前的 tag；每批发版前备份 jobstore 数据库。
- 第二批回滚到第一批时，第一批的解析器只读 `users` 主体的授权，忽略 `caller:` / `session:` 与不认识的列，不会误读。

## 人工 Gate

1. **本计划批准**：用户明确批准本计划。
2. **执行请求**：用户另行发出「开始实施」，之后才派 suga。
3. **外部动作**：tag、push、升级 server 与容器属于用户的长期授权；远程 worker 由用户在 web 上升级；主机实测（T3.3）需要用户在 web 上操作。

## 可追溯性

| 设计条目 | 任务 | 验证 |
|---|---|---|
| 一、权限目录（40 项） | T1.1 | 目录名称集合测试 |
| 一、无主资源规则 | T1.2、T1.5a / c | 真值表（有主 / 无主）测试、反向用例 |
| 二、模式与展开、孤儿、展开快照、预设 | T1.1、T2.1、T3.1 | 模式单测、孤儿与快照测试、web 预设 |
| 三、配置推出、种入、loader、快照、弃用标记 | T1.2、T1.3、T1.4 | 真值表测试、种入测试、并发测试、非根提交 job 200 |
| 有意不保持现状的三处 | T1.5b、T1.5f、T1.5g | 旁路关闭用例 |
| 四、会话授权与化身 | T2.2 | 三种复活场景测试 |
| 五、工具权限提示 | gofer-hxld（已提交）、T1.5c | hxld 测试、迁移后回归 |
| 六、长连接撤销 | T2.3 | 撤销断开测试 |
| 架构：路由声明与四道兜底 | T1.4、T1.5a–i、T1.2 | 覆盖（含待迁移清单）、白名单快照、反向、调用断言、真值表 |
| 威胁模型补充、standalone mcp | T1.7、T1.5a | 文档 |
| 决策 11：先行加固先上线 | 小项批次单独发版 | 发版记录 |
| 架构：生成参考文档、AGENTS 规则 | T1.6 | 过期检查测试 |
| 接口 | T2.1 | 权限矩阵测试 |
| 审计与使用计数 | T2.4 | 审计测试 |
| web | T3.1、T3.2 | vitest |
| 完成定义第 4 条 | T3.3 | 主机实测记录 |

## 完成 Gate 与剩余工作

- **每批完成条件**：
  - 本批任务的验证全部通过；
  - 监督会话的全量验证通过（Linux -race、Windows 全量、vet linux / windows / darwin、web）；
  - 发版 CI 全绿。
- **整体完成条件**：T3.3 主机实测通过，gofer-kd8q 关闭。
- **剩余**：第二期按项目 / agent / runner 限定范围，以及 steward 记录发起人，归入 gofer-6pw0。
