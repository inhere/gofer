<!-- template_id: plan; template_version: 1.2.0 -->
# JOB-12 续接 job 记录并显示原 agent 实施计划

> 状态：Draft 0.1 / 待人工计划批准；执行方式：DIRECT_CONTINUOUS

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-28 | Codex | 将已批准 JOB-12 拆为红测试、持久化与回填、查询与显示、离线验证 |

## 目标与完成定义

`thinking_mode=RIGOROUS`。核心目标：续接的 exec 载体 job 保留可查询的原 agent，让列表、详情和筛选正确呈现续接关系。`allowed_scope=Approved 设计 S1 JOB-12`；`non_goals=PLAN-04、TUN-05、SESS-03、执行/权限/审批语义调整、真实服务操作`；`expansion_policy=DEFER_OR_REQUEST`。评审预算为一轮发现、一轮核对；只有 CORE_BLOCKING 问题才追加阻断修正，语义冲突即停止。

完成定义：三个固定名测试先有效呈红并单独提交，后随实现转绿；新提交的 CLI 续接载体和旧库回填记录 `resume_agent`，普通 job 与 ACP 续接保持空值；HTTP/CLI agent 筛选兼容原始 `exec` 查询并能按原 agent 找到载体；API、CLI、Web 显示符合设计。全仓格式、双平台构建、vet、指定 Go/Web 测试及控制字符扫描取得 exit code 和原始输出；按功能点本地提交。计划提交本身不代表功能完成。

## 范围、排除项与授权

- 只修改 `tools/gofer` 独立 Git 仓的 JOB-12 owner：`internal/job` 决定续接来源和结果，`internal/jobstore` 存储/回填/过滤，`internal/commands` 与 `internal/httpapi` 仅绑定和呈现，`web` 显示。遵守 G021/G022 的单向依赖。
- jobs 表增加可选 `resume_agent`；现有 `agent` 仍是执行载体，`ResumeSourceAgent` 仍是请求内部授权标记且不落库。`resume_agent` 只承载展示/查询事实，不参与执行、权限或审批判定。
- `host_or_non_offline_action=NOT_APPLICABLE`。本期仅授权临时目录里的离线测试、源代码和文档修改、本地原子提交。禁止 push、部署、远程升级、重启/reload live gofer、改真实配置或执行真实任务。

## 输入与批准证据

- 唯一设计依据：[Approved 小项批次设计的 JOB-12 节](../design/2026-09-27-resume-display-plan-handoff-tun-web-hook-watch-design.md)，commit `cd8c228`；设计记录用户于 2026-09-28 批准，S1 固定三项测试。本计划只消费 JOB-12，不借其他三期扩大范围。
- 本 job 明确授权编写并本地提交 S1 计划候选，且已授权监督者代批由已批准设计派生的实施计划。此候选尚无批准；提交后停止，等待审批及续接实施请求。
- 适用约束：工作区 `workspace.md`、本仓 `AGENTS.md` 的 G021/G022/G032、gofer 注入规则、IDEV-STD 0.22.1 的 plan 合同。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 续接原 agent 的确定与记录 | `internal/job/resume.go` 的 `resumeAgent`/`ResumedFrom` 回溯、`fallback.go` 的 `fallbackBase`/`isExecCarrier`、`JobResult` | 直接复用已解析的原 agent，不另建解析器 | 在 job 结果和现有提交流程传递展示字段，ACP 与普通 job 留空 | OWNER_EXTENSION | 当前 `ResumeSourceAgent` 仅在请求内，结果/持久层无展示字段 | 一个来源事实由 job owner 产生；不复用授权标记作持久权限依据 |
| CAP-02 | jobs 存储、旧库一次性回填与 agent 查询 | `internal/jobstore/jobs.go` 的 schema/insert/scan/list 与既有迁移机制 | 复用 jobs 表及现有迁移入口 | 加列并沿已有来源链回填可确认的 exec 载体；列表条件扩展为 `agent = X OR resume_agent = X` | OWNER_EXTENSION | 旧记录只有 `agent=exec`、`resumed_from` | 不建旁表或第二索引真源；一次性回填必须幂等且不反复扫描 |
| CAP-03 | API、CLI、Web 的显示 | `internal/commands/job.go`、HTTP 现有 job JSON/列表、`web/src/{api/types.ts,views/Board.vue,views/JobDetail.vue,views/PlanDetail.vue}` | 复用现有 job 结果 JSON 和列表参数 | 在既有类型、格式化与显示位置扩展；不加新路由或权限通道 | OWNER_EXTENSION | 当前页面和 CLI 直接显示载体 `agent` | 单一 `resume_agent` 语义贯穿各入口，避免新 API/双字段推断器 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| NONE | none | none |

## 前置检查与 fail-closed 条件

- Workspace baseline（计划编写时）：Git root `D:/work/inhere/my-tools-dev/gofer`，branch `main`，HEAD `cd8c2282d93a7c4a0fdb08f8f006c4cce7b1e423`；`git status --short` 无输出；in-scope dirty/untracked 和 preserved unrelated dirty 均无。工作区 `tmp/idev-std/` 位于该独立 Git 根之外。
- 当前预计路径/符号：`internal/job/{resume.go,fallback.go,model.go,list.go,resume_test.go,list_test.go}` 的 `ResumeJob`、`ResumeSourceAgent`、`JobResult`、`ListJobs`；`internal/jobstore/{jobs.go,*_test.go}` 的 jobs schema/scan/list；`internal/httpapi/{list_tags_test.go,resume_test.go}` 与现有列表 handler；`internal/commands/{job.go,*_test.go}` 的 job ls/show；`web/src/api/types.ts`、`web/src/views/{Board.vue,JobDetail.vue,PlanDetail.vue}` 和对应测试；`README.md`、本设计的 S1 实测记录。T00 只核对这些落点，不提前做跨域重构。
- graph 项目 `gofer` generation `2026-09-05T06:59:33Z` 已过时；coverage 对上述路径报 metadata changed，`web/src/api/types.ts:1-989` 是 parse partial。已用当前定向源码核对主要落点；实施前重新读相关源与测试，不能凭图作否定或完整性断言。
- 实施前重跑 IDEV-STD BOUND/semantic/fingerprint preflight，核对 HEAD、dirty/untracked 和设计批准范围；命中他人改动、旧库迁移接口冲突、授权/协议/数据语义变化即停止。新路径在 mutation 前分类为 Operational Discovery、Corrective、Semantic Amendment 或 Ownership Conflict 并记进度。G032：新增保留的旧路径须标 `// DEPRECATED(vX): remove in vY`，无用旧路径删除，均在报告列出。
- 单测只用 `t.TempDir()`；若做二进制 smoke，临时 config、随机端口且每条 gofer 命令显式 `-c` 或 `--server`。Windows 慢包须等待终态；`internal/job` 的两项已知基线失败需列原始输出，不能伪报通过。

## 波次与依赖

T00 最小核查与 preflight → T1 三项红测试独立提交 → T2 续接字段与旧库迁移 → T3 agent 过滤与 JSON/CLI → T4 Web 显示与文档 → T5 质量门和交付。单 Agent 顺序执行，按功能点分别 conventional commit；阶段进度独立记录实际路径、验证命令、exit/output、commit 与 lifecycle 状态。

## 任务

### T00 基线和实际落点核查

- 文件：只读 Approved 设计 JOB-12、`internal/job/{resume.go,fallback.go,model.go,list.go}`、`internal/jobstore/jobs.go`、`internal/commands/job.go`、HTTP 列表路径、三个 Web 视图/Job 类型及相关现有测试。
- 动作：核对 CLI 载体、ACP、链式续接、JobResult/JobRecord 映射、DB 打开迁移、HTTP/CLI agent 过滤与显示位置；辨认 G032 旧分支。若现有机制与设计冲突，停止并报告具体位置。
- 验证：`git status --short --untracked-files=all`、`git log -1 --oneline`、定向源码/覆盖缺口记录、IDEV-STD 阶段指纹。
- 完成标准：T1–T4 owner 与字段流明确，无未裁决语义或归属冲突。
- 依赖：计划获批和续接实施请求。

### T1 三项固定测试先行并单独 red 提交

- 文件：`internal/job/resume_test.go`、`internal/jobstore/*_test.go`；按实际列表入口在 `internal/job/list_test.go`、`internal/httpapi/*_test.go`、`internal/commands/*_test.go` 补必要断言，只 stage 测试文件。
- 动作：写 `TestResumeCarrierRecordsResumeAgent`：CLI 载体、续接的续接、ACP、普通 job 和落库读回；`TestJobListAgentFilterIncludesResumeCarriers`：codex 原 job+载体匹配，同时 exec 仍查到载体，覆盖 HTTP/CLI 共享过滤；`TestResumeAgentBackfill`：临时旧库沿链回填、断链留空、打开后只迁移一次。断言行为，不写 `t.Skip`。
- 验证：`go test ./internal/job/ ./internal/jobstore/ ./internal/httpapi/ ./internal/commands/ -run 'Test(ResumeCarrierRecordsResumeAgent|JobListAgentFilterIncludesResumeCarriers|ResumeAgentBackfill)$' -count=1`；记录 exit code 和原始失败，确认 red 由目标行为缺失而非编译/夹具笔误。提交前 `git status --short` 与 staged exact paths。
- 完成标准：三项有效 red 证据和仅含测试的独立 `test(job): cover resume agent persistence filtering and backfill` 本地提交。
- 依赖：T00。

### T2 记录、持久化和旧库回填

- 文件：`internal/job/{resume.go,model.go}`、`internal/jobstore/jobs.go` 与 T1 对应测试；实际 JobResult 映射点如在同一 job owner 内，按 T00 记录后纳入。
- 动作：在 `JobResult` 增 `resume_agent` JSON 字段；仅 CLI 续接载体将已解析 `resumeAgent` 写入结果/记录，ACP 和普通 job 留空，`ResumeSourceAgent` 内部标记仍 `json:"-"`。jobs 加列并接入 insert/scan/update；打开旧库时一次性沿 `ResumedFrom` 链回填 `agent=exec` 且值为空的记录，断链保持空，保护非载体和已有值。迁移采用现有 jobstore 机制并确保幂等。
- 验证：`TestResumeCarrierRecordsResumeAgent`、`TestResumeAgentBackfill` 转绿；临时旧库二次打开后值稳定、执行载体 `agent` 不变。
- 完成标准：新旧记录读取 `resume_agent` 正确，独立 `feat(job): persist resume source agent` 提交。
- 依赖：T1。

### T3 查询兼容与 CLI/API 呈现

- 文件：`internal/job/{list.go,model.go}`、`internal/jobstore/jobs.go`、`internal/commands/job.go`、既有 HTTP 列表 handler/测试及 T1 对应测试。
- 动作：DB 与 live overlay 的 agent 过滤均匹配 `agent = X OR resume_agent = X`，保持 `agent=exec` 可查；API 结果输出可选 `resume_agent`；`gofer job ls/show` 将载体显示为 `codex (resume)`，普通 job 保持原样。HTTP/commands 只做参数/输出，不改变鉴权或执行判定。
- 验证：`TestJobListAgentFilterIncludesResumeCarriers` 转绿；HTTP 与 CLI 对同一临时数据的 codex/exec 查询、结果 JSON 与普通 job 显示断言。
- 完成标准：两种筛选兼容且输出一致，独立 `feat(job): show and filter resumed agents` 提交。
- 依赖：T2。

### T4 Web 显示与使用说明

- 文件：`web/src/api/types.ts`、`web/src/views/{Board.vue,JobDetail.vue,PlanDetail.vue}` 及其既有测试（若有）、`README.md`、`docs/design/2026-09-27-resume-display-plan-handoff-tun-web-hook-watch-design.md`。
- 动作：Job 类型接受可选 `resume_agent`；Board、job 详情、plan 详情 jobs 列表显示 `codex ↻`，title 固定为“续接，经 exec 载体执行”；普通 job 显示原 agent。README 相关段落补一句话；设计只追加 S1 实测记录，记录真实验证与边界，不提前写验收结论。
- 验证：`cd web && pnpm test && pnpm typecheck && pnpm build`；检查三个视图的字段、title 和普通 job 回退；文档与实际输出一致。
- 完成标准：三个页面与类型正确，Web/文档分别按功能点本地提交，文档实测记录不得先于验证伪填。
- 依赖：T3；实测记录待 T5 验证结果后写入。

### T5 全量质量门与交付

- 文件：只更新 T4 设计的 S1 实测记录和必要的同范围测试/文档；验证产物放仓库 `tmp/`，不 stage。
- 动作：全仓 `gofmt -l` 要求 exit 0 且无输出；Windows/Linux `go build` 产物入 `tmp/`；`go vet ./...`；`go test ./internal/job/ ./internal/jobstore/ ./internal/httpapi/ ./internal/commands/ -count=1`；Web 三命令；控制字符扫描及 `git diff --check`。逐项同时核对 exit code 与原始输出，慢测试等最终退出。列明 G032 新增标记/删除，若没有则明确“无”。
- 验证：保存 gofmt/build/vet、测试 PASS/FAIL 行、Web、控制字符扫描、`git log -n 8 --oneline`、`git status --short` 原始输出；已知 `TestGetArtifactManifest`、`TestWorktreeSymlinkedProjectRoot` 若复现只列为既知基线，不将整包称为通过。
- 完成标准：三个固定测试及新行为全绿，质量门无未裁决回归；设计 S1 实测记录据实提交，功能点 commit hash 和未完成项可逐项追踪。若阻断，保留有效本地提交并报告首个失败边界。
- 依赖：T2–T4。

## 回滚与恢复

每个本地 commit 是恢复点。每次提交前 `git status --short`，只 stage 当前功能点并复核 `git diff --cached --name-only`；不得覆盖他人 dirty 路径。失败时记录首个失败、当前 HEAD、已验证范围及下一步；撤销仅针对本任务准确 commit 评估 `git revert`，先核对依赖。实施进度另记，不用进度改动已批准候选的版本。

## 人工 Gate

本计划候选提交后停在人工计划批准 Gate。监督者依本 job 的代批授权批准后，还需在本会话续接明确实施请求；执行方式已定为 DIRECT_CONTINUOUS。push、远程升级、部署、live 服务、真实配置和硬件/流量动作另需具名外部动作授权，本候选不含这些动作。实施发现改变数据/协议/权限/验收语义时返回 design/plan Gate。

## 可追溯性

| Approved JOB-12 / 本 job 验收 | 任务 | 可观察验证 |
|---|---|---|
| 载体记录原 agent，链式续接与 ACP/普通 job 边界 | T1、T2 | `TestResumeCarrierRecordsResumeAgent`、落库读回 |
| 旧库只回填可确认的 exec 续接链，一次性、断链空值 | T1、T2 | `TestResumeAgentBackfill`、二次打开 |
| codex 与 exec 过滤兼容，API 结果有 `resume_agent` | T1、T3 | `TestJobListAgentFilterIncludesResumeCarriers`、HTTP/CLI 临时数据 |
| CLI `codex (resume)` 与三个 Web 位置 `codex ↻`/title | T3、T4 | CLI 断言、Web test/typecheck/build 与视图检查 |
| 执行、权限、审批不变；G021/G022/G032；离线质量 | T00、T2–T5 | 源码 diff、gofmt/build/vet/Go+Web 测试、控制字符与 Git 记录 |

## 完成 Gate 与剩余工作

S1 仅在三个测试从有效 red 到 green、旧库回填与筛选/显示可观察、质量门结果及按功能点本地提交都有证据后，才能标记实施完成。已知基线失败和任何新失败分别列出，不写 `t.Skip` 或以骨架代替。S2–S4 是独立后续期；本期本地验证不等于 live 服务或用户现场验收。
