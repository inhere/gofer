<!-- template_id: plan; template_version: 1.2.0 -->
# TRK-01 server 镜像、三方同步、issue 联动与 Issues Web（P4）实施计划

> 状态：Draft 0.1 / 待人工计划批准；执行方式：DIRECT_CONTINUOUS

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-29 | Codex | 将 Approved TRK-01 P4 拆为测试先行、server 镜像与同步、job issue 联动、project_key/prime、Web Issues 与质量门波次 |

## 目标与完成定义

`thinking_mode=RIGOROUS`。核心目标：保持 `.gofer/tracker/*.jsonl` 为仓库真源，把 issue/memory 镜像到 server，提供可离线恢复的 `repo sync` 三方合并、`job run --issue` 联动和 Web Issues 页面。`allowed_scope=Approved 0.2 设计文档的 TRK-01 P4 同步 + 联动 + web，以及任务书明确的 project_key/PLAN-04 prime 交接小改`；`non_goals=P1–P3 已发布行为、GIT-01、Dolt/远端 Git、真实仓库迁移、部署/现场硬件、IDEV-STD 适配`；`expansion_policy=DEFER_OR_REQUEST`。评审预算为一轮 discovery、一轮 confirmation；发现会改变协议、schema、权限、删除语义或验收的 CORE_BLOCKING 问题即停止并回到 design/plan Gate。

完成定义：三个固定测试先以目标行为缺失有效呈红并单独提交，随后转绿；server 三表/同步游标与权限可观察；三方合并保留单方修改、按 `updated_at` 解决双方标量冲突并追加冲突 note，集合字段并集；memory 按 key 同理；删除/关闭使用墓碑或不可复活的删除标记并在计划执行前固定；离线写入不失败且恢复同步幂等；`job run --issue` 在开跑/终态更新镜像并写 notes；`project_key` 可由配置提供且 prime 交接优先使用；Web Issues 可按项目/仓库筛选、查看、编辑和评论，会话可链接 issue；质量门、临时 smoke、原始输出、G032 清单和按功能点本地提交均有证据。计划提交本身不等于 P4 实施完成。

## 范围、排除项与授权

- Git 根固定为 `D:/work/inhere/hyy-ai-inspect/tools/gofer`，独立 branch `main`。遵守 G021/G022：commands/httpapi/mcpserver 只绑定和转发，业务编排放 core/job/tracker，数据层不反向依赖入口。
- server 新增 `tracker_repos`、`tracker_issues`、`tracker_memories` 及同步 API、镜像列表/详情/编辑/评论接口；仓库 jsonl 仍是真源，server 是镜像。
- 本地 `repo sync` 读取 `.local/sync-base.jsonl` 与游标，推本地差异、拉 server 增量、三方合并后原子写盘，只有写盘成功才推进基线。`auto_sync` 在 `repo prime` 与每次写命令后 2 秒尽力执行，失败只提示。
- issue 无物理删除，`close` 仍为状态传播；memory `rm` 需以墓碑记录（含 key、删除时间、更新时间、by）参与同步和基线，墓碑优先于旧快照，除显式恢复命令/策略外不允许旧一方复活。若现有设计或代码无法承载该语义，实施前返回 Semantic Amendment，不自行改协议。
- server/user caller 可读写镜像；job caller 只读，除 `job run --issue` 对其关联 issue 的开跑/终态联动外不得任意写；同步鉴权复用现有 CLI client。
- `.gofer/tracker/config.yaml` 增加可选 `project_key`；为空时 server 以 `rel_path`/上报路径尽力匹配，仍失败登记未归属且 Web 可见。`repo init`/`migrate` 在能识别时写入，不能识别不猜测。PLAN-04 prime 交接段直接优先配置值。
- 只授权临时目录测试、源代码/文档修改和按功能点本地 commit；`host_or_non_offline_action=NOT_APPLICABLE`。不授权 push、部署、真实 server/worker 重启或 reload、真实配置 `D:/work/inhere/config/win-env/gofer`、真实仓库 `.gofer/tracker`、数据库迁移和硬件/流量动作。

## 输入与批准证据

- 唯一设计依据：[`2026-09-27-local-first-tracker-and-uncommitted-guard-design.md`](../design/2026-09-27-local-first-tracker-and-uncommitted-guard-design.md)，Approved 0.2；P2/P3 实测记录、server 镜像与同步、实施分期 P4、风险与决策章节是本计划输入。
- 当前起点为 `969f9632225111e9c41bf1e8209509fb429b9c48`（v0.73.0 基线）；以实施前实际 HEAD/status 为准，不重写历史。
- 本 job 授权编写并提交计划候选，监督者可代批由已批准设计派生的实施计划；候选提交后停止，等待批准及后续明确实施请求。设计语义、权限、schema、删除传播或验收冲突返回 Gate。
- 适用约束：workspace.md、gofer `AGENTS.md` 的 G021/G022/G032、house-rules/gofer-repo、IDEV-STD 0.22.1 plan 合同。测试先行、`t.TempDir()`、显式临时 `-c`/`--server`、不修已知 `TestPolicyCacheRoundTrip` 0600 基线。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | server tracker 三表、迁移、事务与镜像 CRUD | `internal/jobstore/store.go` schema/迁移、现有 job/plan 表与事务方法 | 复用同一 store、迁移写法、JSON body 编解码和 caller 上下文 | 在 jobstore 增加 tracker 表与按 tracker_id 的读写/游标方法 | OWNER_EXTENSION | 当前无 tracker 表、rev/cursor 或镜像权限方法 | 不建第二 DB；表和迁移由 jobstore 单一 owner 管理 |
| CAP-02 | repo sync 三方合并、游标、原子基线 | `internal/tracker` Store/lock/atomic JSONL、现有 repo 命令与 config | 复用锁、排序、临时文件替换、tracker_id/config/client | 在 tracker 内扩展 sync engine、merge/tombstone 与 server client adapter | OWNER_EXTENSION | 当前 `repo sync` 未实现，无基线/冲突审计 | 合并规则只有一个 owner；基线仅成功写盘后推进 |
| CAP-03 | issue/memory caller 权限与 job 联动 | `internal/httpapi/jobcredential.go`、`--todo` 终态回写、job caller action 白名单 | 复用 todo 状态机、job 结果 notes/提交列表/未提交提示和 caller 判定 | 增加 issue action 与 tracker mirror client，限制到关联 issue | OWNER_EXTENSION | 无 `--issue` 参数和 tracker action | 不复制权限表；job caller 不能获得通用写权限 |
| CAP-04 | project_key 与 prime 交接 | tracker config/repo init/migrate、`repo prime`、PLAN-04 handoff 段 | 复用 YAML config、ProjectForPath、prime 预算与 best-effort HTTP | 增加可选字段解析/写入和优先级 | OWNER_EXTENSION | 当前 config 无 project_key 优先路径 | 空值回退路径匹配，不能伪造项目归属 |
| CAP-05 | Web Issues 列表/详情/编辑/评论与会话链接 | `web/src/views/Plans.vue`、`PlanDetail.vue`、`MarkdownBlock`、`Sessions.vue`、现有 API client | 复用列表筛选、详情编辑、评论、错误/加载状态和导航注册 | 增加 Issues API 类型、页面、路由和会话链接 | OWNER_EXTENSION | 当前无 tracker Issues API/UI | 不新建前端状态仓库；API 类型与 server 契约单一来源 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 独立同步 daemon/缓存数据库 | CAP-01 | server 镜像与本地基线已足够；新增进程会改变离线语义、生命周期和权限边界 |
| 独立同步 daemon/缓存数据库 | CAP-02 | server 镜像与本地基线已足够；新增进程会改变离线语义、生命周期和权限边界 |
| 独立 Web tracker 数据源 | CAP-05 | 设计规定 Web 只编辑 server 镜像，经 sync 回仓库；新真源会破坏 local-first 决策 |

## 前置检查与 fail-closed 条件

- T0 重新记录 Git root、branch、HEAD、`git status --short --untracked-files=all`、已有 dirty/untracked 归属和本任务 owner；命中他人 dirty 路径、live 配置或设计未承载墓碑语义时停止。
- T0 读取实际 tracker/jobstore/httpapi/job/mcp/web 路径，确认当前 `repo status` 的 P4 占位、job `--todo` 终态链、job caller 白名单、client 鉴权和 Web API/路由惯例。知识图谱只用于结构定位，候选路径确认后对所有路径调用 `check_index_coverage`；覆盖缺口按源码精读补证。
- 每个 mutation 阶段前重跑 `idev-std probe`、`idev-std load --delivery semantic --task plan+git --mutating --thinking-mode rigorous` 与带 task/agent/mutating 的 fingerprint verify；只读唯一 payload 并核对 hash。
- 所有单测使用 `t.TempDir()`；smoke 仅临时 config、随机 localhost 端口、临时仓库，每条 gofer 命令显式 `--server http://127.0.0.1:<port>` 或 `-c <temp-config>`，不回落 live server。禁止重启/reload 正在运行的 gofer server/worker。
- 三方合并、墓碑优先级、project_key 归属、job caller 可写范围、HTTP 字段/状态码如与现有实现冲突，标为 Semantic Amendment 并停止，不在实施中猜测。

## 波次与依赖

T0 基线与落点核查 → T1 三项固定测试红提交 → T2 server 表/同步 API 与镜像权限 → T3 本地 sync 三方合并/auto_sync/status/init → T4 job `--issue` 联动 → T5 project_key/prime 交接 → T6 Web Issues 与会话链接 → T7 文档、质量门与临时 smoke。单 Agent 顺序执行；每个功能点独立 conventional commit，测试先行提交不得混入实现。

## 任务

### T0 基线、协议和删除语义核查

- 文件：只读设计 P4/风险/决策章节；`internal/tracker/`、`internal/jobstore/store.go`、`internal/httpapi/server.go`/`jobcredential.go`、job `--todo` 提交与终态文件、MCP 注册、`web/src/views/PlanDetail.vue`/`Plans.vue`/`Sessions.vue`、API/路由/导航。
- 动作：核对现有字段、rev/cursor 约定、caller 身份、镜像 API 命名和 Web 组件；明确 memory tombstone 的最小 JSON 形态及恢复禁止规则；记录 G032 兼容路径（新增 wire 字段若保留必须标记 remove 版本，没人用的旧路径删除）。
- 验证：`git status --short --untracked-files=all`、`git log -1 --oneline`、定向源码/图覆盖证据、IDEV-STD receipt/fingerprint。完成标准：T1–T6 owner、接口、验证和 stop 条件唯一明确。
- 依赖：计划批准及后续实施请求。

### T1 三项固定测试先行，单独 red 提交

- 文件：按真实入口新增/修改 tracker、jobstore/httpapi、job/commands/mcp 测试；只 stage 测试与必要脱敏 fixture。
- 动作：写固定名 `TestSyncThreeWayMerge`（issue/memory 单方与双方标量、`updated_at` 冲突 note、notes/comments/deps/tags 并集、墓碑/close 传播）；`TestSyncOfflineThenCatchUp`（server 不可达本地写成功并提示，恢复推送，模拟 Web 改动拉回，基线推进，重复同步无写盘/git diff）；`TestJobIssueLinkAppendsNotes`（开跑 in_progress，终态 notes 含状态/提交/未提交，下一次 sync 可见，未同步仓库只打标签）。不写 `t.Skip`。
- 验证：运行固定测试集合并记录 exit code、FAIL/PASS 原文；红必须来自目标实现缺失而非编译/夹具错误。提交前 `git status --short` 和 staged exact paths；提交 `test(tracker): cover P4 sync and issue linkage`。
- 依赖：T0。

### T2 server 镜像表、同步 API 与权限

- 文件：`internal/jobstore/store.go` 及 tracker 存储测试；`internal/httpapi/server.go`、tracker handler/client、权限测试；必要的 schema/model 文件。
- 动作：新增三表与幂等迁移，按 `(tracker_id,id/key)` 唯一；保存 `body_json/rev/updated_at`、project/path/last_sync；实现推差异、拉增量、rev/cursor、列表/详情/编辑/评论接口；user 可读写、job 只读，issue 联动使用单独受限 action；同步鉴权沿现有 CLI client。
- 验证：T1 相关 server 断言、迁移重复执行、rev/cursor 增量、未知 tracker/issue、user/job caller 权限和 403/404/409 口径；`go test ./internal/jobstore/ ./internal/httpapi/ -count=1`。提交 `feat(tracker): add server mirror and sync api`。
- 依赖：T1；若 schema/权限语义改变，回 design Gate。

### T3 本地 repo sync、三方合并、auto_sync 与 status/init

- 文件：`internal/tracker/` sync/merge/tombstone/base 文件、`internal/commands/repo.go`、config model、CLI/client 测试。
- 动作：实现本地差异推送/远端增量拉取；单方标量取改方，双方改取较新 `updated_at` 并追加“同步冲突：<字段> 取了 <某方>，另一方为 <值>”；notes/comments/deps/tags 并集并稳定去重；memory 按 key 同理；close 与墓碑不可被旧快照复活；成功原子写盘后推进基线和 cursor；2 秒 auto_sync 失败只提示；`repo status` 显示待同步与上次时间；`repo init` 连接时首次 sync，离线照旧。
- 验证：三项固定测试转绿；临时 server/client smoke；重复同步无 diff；锁/原子替换、超时、未同步仓库和 server 不可达；提交 `feat(tracker): implement repo sync and three-way merge`。
- 依赖：T2。

### T4 job run --issue 联动

- 文件：`internal/job` 提交参数/终态回写、CLI/MCP/HTTP 绑定和测试、job caller action 清单。
- 动作：新增 `--issue <id>` 及等价提交入口；开跑将关联镜像 issue 置 `in_progress`；终态追加状态、提交列表、未提交提示 notes；仅允许该 job 关联 issue；仓库从未同步时只给 job 打标签不报错；本地在下一次 sync 获取更新。
- 验证：`TestJobIssueLinkAppendsNotes` 转绿；job caller 越权写入拒绝；保持 `--todo` 行为和 G021/G022；提交 `feat(job): link runs to tracker issues`。
- 依赖：T3。

### T5 project_key 与 prime/PLAN-04 交接

- 文件：tracker config/init/migrate、`internal/commands/repo.go`、`internal/tracker/prime.go`、PLAN-04 prime 交接 owner 与测试、README/skill 若行为需说明。
- 动作：可选 `project_key` 读写；init/migrate 能识别时保存；sync 上报 tracker_id+project_key+路径；空值回退路径匹配并登记未归属；prime 交接直接优先配置值，保留 2 秒 best-effort 与 8 KiB 预算，不改变 P1–P3 输出。
- 验证：配置有值/空值/容器路径 fixture、未归属 Web 可见、prime 交接测试与现有 P3 测试；提交 `feat(tracker): support project key for sync ownership`，文档独立时再提交 `docs(tracker): document P4 sync and project ownership`。
- 依赖：T3；PLAN-04 现有接口若不兼容则停止。

### T6 Web Issues 与会话链接

- 文件：`web/src/views/Issues.vue`、`web/src/api/` 类型/请求、路由/导航、`Sessions.vue` 链接、现有 Web 测试；对应 HTTP handler 仅在契约缺口时修改。
- 动作：按项目/仓库列出，支持状态/类型/标签/关键字筛选；详情显示字段、notes/comments/deps/tags；编辑与评论写 server 镜像，展示冲突/错误；会话页可链接 issue；未归属仓库可见；复用 Plans/PlanDetail/MarkdownBlock 的加载、编辑、错误和样式模式。
- 验证：`cd web && pnpm test && pnpm typecheck && pnpm build`；必要时使用仓库 `.bin` 等效命令并记录阻断；API/UI 权限、空/离线回退、评论和导航测试；提交 `feat(web): add tracker issues page`。
- 依赖：T2、T5；不扩展为独立 tracker 真源。

### T7 文档、质量门、临时 smoke 与交付证据

- 文件：README/README.zh-CN tracker 段、gofer-usage skill、设计文档 P4 实测记录；验证产物仅放 `tools/gofer/tmp/`，不 stage。
- 动作/验证：全仓 `gofmt -l` 无输出；Windows/Linux `go build ./cmd/gofer`；`go vet ./...`；`go test ./internal/tracker/ ./internal/jobstore/ ./internal/httpapi/ ./internal/commands/ ./internal/job/ ./internal/mcpserver/ -count=1`（等慢包终态）；Web 三命令；控制字符扫描、`git diff --check`；临时 config+随机端口 smoke：init→离线写→sync→模拟 Web 改→sync 拉回，所有命令显式 `--server`/`-c`。同时记录 exit code、原始 gofmt/build/vet/PASS/FAIL、git log/status、G032 清单。
- 完成标准：三项固定测试和目标行为闭合；无 live 配置/进程/真实仓库改动；每个功能点 commit 可回滚；提交 `docs(design): record TRK-01 P4 verification`（仅在有真实证据时）。
- 依赖：T2–T6。

## 回滚与恢复

每个波次的本地 atomic commit 是恢复点。每次提交前执行 `git status --short`，只暂存当前 owner 文件并核对 `git diff --cached --name-only`；不得吸收他人 dirty/untracked。同步失败保留本地真源和旧基线，不推进 cursor；恢复时先复核 HEAD/status、首个失败输出和已过测试，再从对应 commit 继续。撤销只针对本任务准确 commit 评估 `git revert`。任何 schema/协议/权限/删除语义变化都停止并回 Gate。

## 人工 Gate

本计划候选提交后停在人工计划批准 Gate。监督者依代批授权批准后，还需本会话续接明确实施请求；执行方式为 DIRECT_CONTINUOUS。实施中若发现设计未定义的墓碑恢复、project ownership、caller 权限或 API/schema 语义，返回 design/plan Gate。push、部署、live server/worker、真实配置、真实数据迁移和硬件动作均不在本计划授权内。

## 可追溯性

| Approved P4 / 验收要求 | 任务 | 可观察验证 |
|---|---|---|
| server 镜像三表、推拉增量、rev/cursor、权限 | T1–T2 | jobstore/httpapi 测试、迁移与权限原始输出 |
| 三方标量冲突、集合并集、memory 同理、墓碑/close 不复活 | T1、T3 | `TestSyncThreeWayMerge`、重复 sync 无 diff |
| 离线写、恢复 catch-up、基线正确、幂等 | T1、T3、T7 | `TestSyncOfflineThenCatchUp`、临时 server smoke |
| auto_sync、repo status/init | T3 | CLI 测试、超时提示与 status 输出 |
| job issue 开跑/终态 notes 与权限 | T1、T4 | `TestJobIssueLinkAppendsNotes`、caller 越权断言 |
| project_key 与 PLAN-04 prime 交接 | T5 | 配置 fixture、prime 输出和未归属可见 |
| Issues Web 列表/筛选/详情/编辑/评论/会话链接 | T2、T6、T7 | Web test/typecheck/build、API/UI 证据 |
| G021/G022/G032、离线边界和可回滚提交 | T0–T7 | 图覆盖/源码核对、gofmt/build/vet/测试/G032 清单/git 记录 |

## 完成 Gate 与剩余工作

P4 仅在固定测试从有效 red 到 green、server/CLI/job/Web 链路和 project ownership 有证据、质量门和临时 smoke 已等终态、所有 commit 与 G032 清单已核对后标记实施完成。计划候选提交不代表实施完成；真实仓库/试点迁移、live server/worker、push/deploy、硬件和现场验收留在另行授权的外部动作 Gate。
