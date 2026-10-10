<!-- template_id: plan; template_version: 1.2.0 -->
# S2 PLAN-04 plan 交接说明实施计划

> 状态：Draft 0.1 / 待人工计划批准；执行方式：DIRECT_CONTINUOUS

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-29 | Codex | 将 Approved 设计的 PLAN-04 拆为测试先行、存储/API、CLI/MCP、prime/Web 与质量门波次 |

## 目标与完成定义

`thinking_mode=RIGOROUS`。核心目标：为每个 plan 增加版本化 Markdown 交接说明，复用现有 plan/todo 的 caller 权限边界，经 HTTP、CLI、MCP、prime 和 Web 访问同一份事实。`allowed_scope=Approved 设计 PLAN-04`；`non_goals=JOB-12、TUN-05、SESS-03、现有 plan/todo/comment 语义或权限调整、远程部署/真实服务操作`；`expansion_policy=DEFER_OR_REQUEST`。

完成定义：三个固定测试先以目标行为缺失有效呈红并单独提交，随后随实现转绿；`plan_handoffs` 迁移、版本冲突、历史读取、权限判定和 16 KiB 限制可观察；HTTP/CLI/MCP/Web 使用同一存储和权限结果；`repo prime` 按 cwd 项目尽力加入最多三个 open plan 的最新交接说明，server 不可达或超时静默省略且总输出遵守 8 KiB；事件写入但不进入默认通知集；全仓格式、双平台构建、vet、指定 Go/Web 测试与控制字符扫描取得 exit code 和原始输出；按功能点本地提交。计划提交本身不代表功能完成。

## 范围、排除项与授权

- 只修改 `tools/gofer` 独立 Git 仓的 PLAN-04 owner：`internal/jobstore` 负责表、迁移、事务和读取；`internal/httpapi` 负责路由/请求校验/入口调用；`internal/commands`、`internal/mcpserver` 只做绑定、校验和转发；`internal/tracker`/`internal/commands/repo.go` 负责 prime 编排；`web` 负责显示与编辑；事件定义沿现有 job/plan 事件 owner 落地。遵守 G021/G022 单向依赖。
- 新增 `plan_handoffs(plan_id, version, body, by, at)`，`(plan_id, version)` 唯一；交接说明独立于 plan description、todo、comment、memory 和 issue。写入只接受用户 caller 或挂载到该 plan 的 job caller，读权限沿现有 plan/todo 读取判定。
- 本期仅授权临时目录离线测试、源代码/文档修改和本地原子提交；禁止 push、重启/reload live gofer server/worker、修改真实配置目录 `D:\work\inhere\config\win-env\gofer`、部署、迁移真实数据库、硬件/流量动作。测试一律 `t.TempDir()`；smoke 如需启动只用临时 config 和随机端口，命令显式带 `--server` 或 `-c`。

## 输入与批准证据

- 唯一设计依据：[Approved 小项批次设计的 PLAN-04 节](../design/2026-09-27-resume-display-plan-handoff-tun-web-hook-watch-design.md)，用户于 2026-09-28 批准；本计划只消费 S2，不借其他三期扩大范围。
- 现有积木已核对：`internal/jobstore/store.go` 的 schema/迁移入口，`internal/httpapi/plan_handler.go` 的 plan/todo handler，`internal/commands` 的 plan 子命令，`internal/mcpserver/server.go` 的 plan 工具注册，`internal/commands/repo.go` 与 `internal/tracker/prime.go` 的 prime，`internal/config/resolve.go` 的 `ProjectForPath`，`web/src/views/PlanDetail.vue` 的顶部卡片位置和 `MarkdownBlock`。
- 本 job 明确授权编写并本地提交 S2 计划候选，且授权监督者代批由已批准设计派生的实施计划。候选尚无批准；提交后停止，等待审批及后续明确实施请求。
- 适用约束：`workspace.md`、本仓 `AGENTS.md` 的 G021/G022/G032、gofer 注入的 `house-rules`/`gofer-repo`、IDEV-STD 0.22.1 plan 合同。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 交接说明持久化、版本和历史 | `internal/jobstore/store.go` 的 schemaStmts/迁移；现有 plan/todo 存储方法 | 复用同一 DB、事务和 plan ID 校验 | 在 jobstore 增加 handoff 表、扫描结构和按版本读写方法 | OWNER_EXTENSION | 当前没有 plan handoff 表或版本 CAS | 不另建文件/issue 真源；唯一约束和事务保证单调版本 |
| CAP-02 | HTTP/CLI/MCP caller 权限与契约 | `plan_handler.go` 的 todo 更新判定；plan CLI `set-todo`/`comments`；`gofer_plan_run` 注册 | 复用 todo 更新的 caller 判定和错误 envelope | 各入口仅绑定 body/flags/action，业务调用 jobstore/共享 helper | DIRECT_REUSE + OWNER_EXTENSION | handoff 路由、CLI 子命令和 MCP action 尚不存在 | 禁止入口各自复制权限逻辑，避免 user/job caller 漂移 |
| CAP-03 | prime 获取项目 open plan 交接说明 | `repo.go`、`tracker/prime.go`、`config.ProjectForPath`、现有 HTTP client | 复用 cwd 项目识别、PrimeMaxBytes 预算和现有输出截断 | 增加 2 秒 best-effort server 查询与专用段落预算 | OWNER_EXTENSION | 当前 prime 不查询 server plan handoff | server 不可达必须静默；不得让 prime 依赖远端成功 |
| CAP-04 | PlanDetail 交接卡片 | `PlanDetail.vue`、`MarkdownBlock`、`web/src/api` 请求/类型 | 复用现有 plan detail 生命周期和 Markdown 渲染 | 增加最新/历史请求、编辑 CAS、409 刷新提示 | OWNER_EXTENSION | 当前页面没有 handoff 类型、请求或卡片 | 不改 plan/todo/comment 组件权限和语义 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 独立 handoff 服务/缓存 | CAP-01/03 | plan handoff 是 plan 的持久事实；增加服务或缓存会复制真源并扩大权限/生命周期 |
| 新的 handoff 权限模型 | CAP-02 | 设计明确要求复用 todo 更新判定；新增模型会改变既有权限语义 |

## 前置检查与 fail-closed 条件

- 计划编写基线：Git root `D:/work/inhere/my-tools-dev/gofer`，branch `main`，HEAD `609a34a`；`git status --short --untracked-files=all` 无输出。工作区 `tmp/idev-std/` 在独立 Git 根之外。
- T00 只核对实际迁移入口、plan/todo caller 判定、路由注册、事件默认通知集、repo prime 请求链、Web API 类型/组件；实施前重新读取相关源和测试。若发现设计与代码事实冲突、需要改变协议/权限/plan 语义、或命中他人 dirty 文件，停止并报告具体位置。
- 实施每个 mutation 阶段前重跑 IDEV-STD BOUND/semantic/fingerprint preflight，确认 HEAD、dirty/untracked 与批准范围；只读 receipt payload 并核验 hash，不把局部加载当作规范身份。
- G032：新增兼容读取/迁移分支必须有 `// DEPRECATED(vX): remove in vY`；没有用户仍用的旧路径直接删除；每个保留/删除项在最终清单列出。预期 handoff 为 additive schema/可选 API 字段，除一次性旧 schema 迁移外不新增兼容分支。

## 波次与依赖

T00 最小核查与 preflight → T1 三项红测试独立提交 → T2 存储/迁移/事件 → T3 HTTP/CLI/MCP → T4 prime 与 Web/文档 → T5 质量门与设计实测记录。单 Agent 顺序执行，按功能点分别 conventional commit；阶段进度独立记录路径、验证命令、exit/output、commit 与 lifecycle 状态。

### T00 基线和实际落点核查

- 文件：只读设计 PLAN-04、`internal/jobstore/store.go` 及 handoff 邻近测试、`internal/httpapi/plan_handler.go`/路由注册/权限测试、`internal/commands` plan 命令、`internal/mcpserver/server.go`、`internal/commands/repo.go`、`internal/tracker/prime.go`、`internal/config/resolve.go`、`web/src/views/PlanDetail.vue`、`web/src/api`。
- 动作：确认 plan 不存在的 404 口径、todo 更新 caller 判定及可复用 helper；确认事件存储/通知集入口；确认 prime 如何获得 server client、按项目列 open plan 和预算；确认 Web 现有 MarkdownBlock、请求错误刷新模式。记录未知点，不提前改代码。
- 验证：`git status --short --untracked-files=all`、`git log -1 --oneline`、定向源码/测试核对、IDEV-STD 阶段指纹。完成标准：T1–T4 owner、字段流、冲突和权限边界明确。
- 依赖：计划获批和后续明确实施请求。

### T1 三项固定测试先行并单独 red 提交

- 文件：按实际入口写入 `internal/jobstore/*_test.go`、`internal/httpapi/*_test.go`、`internal/commands/*_test.go`、`internal/tracker/*_test.go`；仅 stage 测试文件。
- 动作：写固定名 `TestPlanHandoffVersioning`（首次 version=1、递增、最新/指定版本/倒序历史、expected_version 冲突 409 且不写、body>16 KiB 400）；`TestPlanHandoffPermissions`（user 可读写、该 plan 挂载 job 可写、其他/未挂载 job 只读、与 todo 更新一致）；`TestPrimeIncludesPlanHandoff`（cwd 项目最多三个 open plan 按更新时间、2 秒不可达静默、8 KiB 超限只截断该段并注明）。测试只用 `t.TempDir()`，不写 `t.Skip`。
- 验证：运行固定测试集合并同时记录 exit code 和 PASS/FAIL 原文；确认 red 原因是目标行为缺失而非编译/夹具错误。提交前 `git status --short`、`git diff --cached --name-only`。
- 完成标准：三项有效 red 证据和仅含测试的独立 `test(plan): cover handoff versioning permissions and prime` 本地提交。
- 依赖：T00。

### T2 表、存储、迁移与事件

- 文件：`internal/jobstore/store.go` 及 plan 相关存储/测试；job/plan 事件定义与默认通知集；必要的共享 handoff 类型。
- 动作：新增 `plan_handoffs` 表和唯一约束；实现按 plan 最新、指定 version、历史倒序读取；事务内校验 expected_version（首次为 0），成功后 version 单调递增并写 `by/at`；plan 不存在由上层统一判 404；body UTF-8 字节数超过 16 KiB 拒绝。写成功发布 `plan.handoff_updated`，明确不加入默认通知集。迁移遵循现有 schema/迁移写法，幂等且不改变 plan/todo/comment。
- 验证：`TestPlanHandoffVersioning`、存储定向测试；事务并发/冲突不产生新版本；`gofmt -l` 对改动 Go 文件无输出；检查事件默认通知清单没有该事件。提交 `feat(plan): persist versioned handoff notes`。
- 依赖：T1。

### T3 HTTP、CLI 与 MCP

- 文件：`internal/httpapi/plan_handler.go`/路由注册/测试；`internal/commands` plan 子命令及测试；`internal/mcpserver/server.go`/工具测试；共享 caller 判定 helper 所在 owner。
- 动作：实现 `GET /v1/plans/{id}/handoff`、`PUT /v1/plans/{id}/handoff`、`GET /v1/plans/{id}/handoff/history`；PUT 接受 body 与 expected_version，返回新版本，冲突 409，超限 400，未知 plan 404。CLI 实现 `gofer plan handoff <plan>`，支持 `--set`/`-f`、`--history`、`--version N`，写入自动读取当前 version 作为 expected_version，409 提示重新查看。MCP `gofer_plan_handoff` 仅支持 `action=get|set`，参数校验/权限委托现有边界。
- 验证：API/CLI/MCP 对同一临时库的读写、历史、409/404/400 和四类 caller 结果一致；不改变 todo/comment 权限。提交 `feat(plan): expose handoff over http cli and mcp`。
- 依赖：T2。

### T4 prime、Web 与文档

- 文件：`internal/commands/repo.go`、`internal/tracker/prime.go`、相关测试；`web/src/views/PlanDetail.vue`、`web/src/api/` 类型/请求/测试；README plan 段；`skills/` 下对应 gofer-usage skill；设计文档追加 S2 实测记录（只在 T5 有真实结果后填写）。
- 动作：prime 用 `ProjectForPath` 识别 cwd 项目，尽力向 server 查询最近更新的最多三个 open plan 的最新 handoff，设置 2 秒超时；不可达/超时静默省略；在 `PrimeMaxBytes` 预算内只截断 handoff 段并注明，不能截断既有段落语义。PlanDetail 顶部增加“交接说明”卡片，复用 MarkdownBlock，显示 body/version/by/at，可编辑并支持历史展开；409 提示“已被他人更新”并刷新最新内容。补 README 和 skill 用法，保持 source-language README 与英文并行要求（若对应文件已存在）。
- 验证：prime 固定测试；`cd web && pnpm test && pnpm typecheck && pnpm build`；检查 409、Markdown、历史和普通 plan 回退；文档仅记录已验证事实。按功能点提交 `feat(plan): show handoff in prime and web`、`docs(plan): document handoff usage`（若文档可独立提交）。
- 依赖：T3。

### T5 全量质量门与交付

- 文件：必要时只更新设计文档 PLAN-04 S2 实测记录；验证产物放 `tools/gofer/tmp/`，不 stage。
- 动作/验证：全仓 `gofmt -l` 无输出；Windows/Linux `go build` 产物入 tmp；`go vet ./...`；`go test ./internal/jobstore/ ./internal/httpapi/ ./internal/commands/ ./internal/mcpserver/ ./internal/tracker/ -count=1`；Web 三命令；控制字符扫描、`git diff --check`。每项同时记录 exit code 和原始输出，Windows 慢包等最终状态，不以启动成功替代结果。
- 完成标准：三个固定测试和新行为全绿；质量门无未裁决回归；提交 `docs(design): record PLAN-04 handoff verification`（如需）；列出 git log、git status、G032 清单、未完成项和人工决策点。不得 push。
- 依赖：T2–T4。

## 回滚与恢复

每个功能点本地 commit 是恢复点。每次提交前执行 `git status --short`，只 stage 当前 owner 文件并复核 `git diff --cached --name-only`；不得覆盖他人 dirty 路径。失败时记录首个失败、当前 HEAD、已验证范围和下一步；撤销仅针对本任务准确 commit 评估 `git revert`，先核对依赖。进度记录不改变已批准候选版本。

## 人工 Gate

本计划候选提交后停在人工计划批准 Gate。监督者依代批授权批准后，还需本会话续接明确实施请求；执行方式已定为 DIRECT_CONTINUOUS。发现需要改变数据/协议/权限/验收语义时返回 design/plan Gate。push、远程升级、部署、live 服务和真实配置动作另需具名外部授权，本候选不含这些动作。

## 可追溯性

| Approved PLAN-04 / 验收要求 | 任务 | 可观察验证 |
|---|---|---|
| 版本单调、CAS 冲突不写、历史倒序、16 KiB 限制 | T1–T2 | `TestPlanHandoffVersioning`、存储定向测试 |
| user/挂载 job 可写，其他 job 只读，plan 不存在 404 | T1、T3 | `TestPlanHandoffPermissions`、HTTP/CLI/MCP 响应 |
| CLI、HTTP、MCP 共享 handoff 语义 | T2–T3 | 同一临时库的跨入口读写/历史/冲突断言 |
| prime 项目识别、最多三个 open plan、2 秒 best-effort、8 KiB | T1、T4–T5 | `TestPrimeIncludesPlanHandoff`、原始输出与字节数 |
| Web Markdown 编辑、版本元数据、历史、409 刷新 | T4–T5 | pnpm test/typecheck/build、视图和 API 断言 |
| 事件可记录但不进默认通知，G021/G022/G032 与离线边界不变 | T2–T5 | 事件清单、依赖检查、gofmt/build/vet/测试/G032 清单 |

## 完成 Gate 与剩余工作

S2 仅在三个固定测试从有效 red 到 green、版本化存储/权限/API/CLI/MCP/prime/Web 可观察、质量门结果及按功能点本地提交都有证据后，才能标记实施完成。计划候选提交不等于实施完成；本期不做 push、部署、真实 server/worker 或现场验收。
