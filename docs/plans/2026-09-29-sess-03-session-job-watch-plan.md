<!-- template_id: plan; template_version: 1.2.0 -->
# S4 SESS-03 Stop hook 等待时盯住本会话派出的 job 实施计划

> 状态：Draft 0.1 / 待人工计划批准；执行方式：DIRECT_CONTINUOUS

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-29 | Codex | 将 Approved 设计的 SESS-03 拆为红测试、待盯存储/API、PostToolUse/Stop 轮询、CLI/Web/文档与质量门波次 |

## 目标与完成定义

`thinking_mode=RIGOROUS`。核心目标：让 Claude Code/Codex 会话在 Stop hook 已经挂起等待 web 回复时，能够观察本会话派出的 job；任一待盯 job 进入终态即放行 hook，并把多个同轮完成合并成一条续接注入。`allowed_scope=Approved 设计 SESS-03`；`non_goals=Claude Code 内部会话间消息通道、中继关闭时的行为、job 执行/权限语义、远程 worker 盯 job、真实服务/配置/部署动作`；`expansion_policy=DEFER_OR_REQUEST`。

完成定义：三个固定测试先以目标行为缺失有效呈红并单独提交，随后随实现转绿；会话待盯 job 的增/查/删、caller 权限、SessionEnd/删除清空、PostToolUse 本地快速识别、Stop 长轮询期间终态放行与多 job 合并、显式 `gofer session watch`、Claude/Codex 模板安装去重、Web 会话显示和文档均可观察。全仓格式、双平台构建、vet、指定 Go/Web 测试及控制字符扫描取得 exit code 和原始输出；按功能点本地提交。计划提交本身不代表功能完成。

## 范围、排除项与授权

- 只修改 `tools/gofer` 独立 Git 仓的 SESS-03 owner：`internal/jobstore` 负责会话待盯关系及清理；`internal/sessionrelay` 负责会话服务边界和 Stop 期间读取/终态判断；`internal/hookrelay` 负责 PostToolUse 识别、登记和 Stop 注入；`internal/httpapi`/`internal/client` 负责 HTTP 客户端契约；`internal/commands` 负责显式命令；`hooks` 负责模板；`web` 负责会话页显示；README、`skills/gofer-usage` 和设计实测记录负责用法与证据。遵守 G021/G022 单向依赖。
- 待盯记录属于 session 的短期状态，job id 只作为引用；查询 job 必须沿现有 job caller 权限读取，不能绕过现有鉴权。登记幂等；注入成功后移除；SessionEnd、删除会话和其他终止清空。
- PostToolUse 每次工具调用必须便宜：模板 matcher 只匹配 shell 类工具（Claude `Bash`，Codex 使用其实际 shell 工具名）；hook 进程先在本地正则识别 `job <id> submitted` 或 `gofer job watch <id>`，无匹配立即退出且不连 server。中继未布防时仍不产生额外网络请求。
- `host_or_non_offline_action=NOT_APPLICABLE`。本期仅授权临时目录离线测试、源代码和文档修改、本地原子提交；禁止 push、重启/reload live gofer server/worker、修改真实配置目录 `D:\work\inhere\config\win-env\gofer`、部署、真实任务和现场动作。测试一律 `t.TempDir()`；smoke 如需启动只用临时 config、随机端口，命令显式带 `--server` 或 `-c`。

## 输入与批准证据

- 唯一设计依据：[Approved 小项批次设计的 SESS-03 节](../design/2026-09-27-resume-display-plan-handoff-tun-web-hook-watch-design.md)，用户于 2026-09-28 批准；本计划只消费 S4，不借其他三期扩大范围。
- 起点为当前 gofer 独立仓 `main` 的 `3cd0c70`（v0.72.0），工作树在计划编写时干净；当前设计已明确非目标、中继开关边界、注入格式和固定测试名。
- T00 已核对的现有积木：`internal/hookrelay/payload.go` 目前只解析通用字段且没有工具名/工具输出字段；`run.go` 的 Stop 路径以 `OpenSessionTurn` 后 `WaitSessionTurn` 长轮询并在同一循环做 idle release；`install.go` 以 gofer-owned command 前缀进行事件条目去重；`internal/commands/hook.go` 使用 current-session 文件 `run/sessions/<sha1(cwd)[:16]>`；HTTP 已有 `/v1/sessions` 注册、心跳、turn、删除路由；`release_job_test.go` 展示了读取 job 终态并释放会话 takeover 的方式；`Sessions.vue` 已有 agent session 抽屉和 API 类型，可扩展待盯列表。
- 本 job 明确授权编写并本地提交 S4 计划候选，监督者可代批“由已批准设计派生的实施计划”。此候选尚无批准；提交后停止，等待审批及后续明确实施请求。
- 适用约束：工作区 `workspace.md`、本仓 `AGENTS.md` 的 G021/G022/G032、gofer 注入的 `house-rules`/`gofer-repo`、IDEV-STD 0.22.1 plan 合同。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 会话待盯 job 的持久化、幂等增删和结束清理 | `internal/jobstore/sessions.go` 的 session 表、现有 session 删除/迁移；job 记录读取 | 复用同一 DB、session id 校验和 job 读取权限；用 job id 唯一约束实现幂等 | 在 jobstore 增加 session watches 表/结构和按 session 的增删查清理方法 | OWNER_EXTENSION | 当前 session 行没有待盯 job 关系 | 不把 job 状态复制到 watch 表；删除/SessionEnd 必须是唯一清理入口并可重复执行 |
| CAP-02 | PostToolUse 本地识别与 Stop 终态轮询 | `hookrelay.ParsePayload`、`run.go` Stop 长轮询、`client.WatchJob`/job 读取、`sessionrelay/release_job_test.go` | 复用现有 payload 归一化、turn 等待窗口、job 终态常量和 caller-scoped client | 扩展 payload 工具名/输出字段；增加无网络的正则识别器和一次轮询批量读取 seam；用现有 WaitSessionTurn 窗口避免忙等 | OWNER_EXTENSION | 当前没有 PostToolUse 分支，也没有会话 watch 查询/注入 | 识别器只返回 job id；状态和标题从 server job 读取，避免 hook 自己重建 job 事实或绕过权限 |
| CAP-03 | 会话 HTTP/API 和 caller 边界 | `server.go` 的 `/sessions/*` 路由、session handler、`internal/client` session 方法、现有 job caller middleware | 复用 session 注册/删除/heartbeat 的 caller 判定和 JSON envelope | 增加 `POST/GET /sessions/{sid}/watches`（删除按实际实现需要）及 client 类型/方法 | OWNER_EXTENSION | 当前没有 watch 路由和响应字段 | job caller 只能看到其有权看的 job；未知 session、无权 job、重复登记必须使用现有错误口径 |
| CAP-04 | 显式命令和当前 session 解析 | `internal/commands/session.go` 的 `resolveCurrentSession`、session 命令组、`hook.go` 写 current-session 文件 | 直接复用 cwd 文件和 prefix/ambiguity 错误 | 新增 `gofer session watch <job-id>`，只绑定参数并调用 client watch API | OWNER_EXTENSION | 当前 session 组没有 watch 子命令 | 解析不到时给清晰错误；不复制 cwd/session 解析，也不把命令变成顶级组 |
| CAP-05 | Hook 模板安装去重与 Web 展示 | `hooks/claude.settings.json`、`hooks/codex.hooks.json`、`install.go` 的 `stripOurs`/`isOurCommand`、`Sessions.vue` | 复用 gofer-owned command 前缀、安装/卸载合并和 session 列表轮询 | 增加 PostToolUse matcher 条目、watch API 类型/加载、id/标题/状态链接 | OWNER_EXTENSION | 模板无 PostToolUse，页面无待盯 job 区域 | 不修改真实用户配置；重复 Install 不重复，卸载能完整删除新增条目 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| Claude Code 内部会话间消息通道 | CAP-02 | 设计明确列为非目标；公开 hook/HTTP 足以完成本期注入，接入内部通道会扩大协议和权限边界 |
| 独立 watcher daemon/后台 worker | CAP-02 | Stop hook 已有有界长轮询窗口；新增进程会改变部署与生命周期，并违反 PostToolUse 便宜约束 |
| 第二套 job 状态缓存 | CAP-02 | 现有 job 读取和 terminal 状态是真源；缓存会造成权限、标题、状态漂移 |

## 前置检查与 fail-closed 条件

- 计划编写基线：Git root `D:/work/inhere/hyy-ai-inspect/tools/gofer`，branch `main`，HEAD `3cd0c70`；`git status --short --untracked-files=all` 无输出。工作区 `tmp/idev-std/` 位于独立 Git 根之外。
- T00 仅核对 payload 的 Claude/Codex 字段差异、PostToolUse 实际工具名、session schema/删除事务、job caller 读取权限、终态字段（status/exit/duration/title）、Stop 长轮询窗口、模板安装去重和 Web API 类型。实施前重新读取相关源和测试；若设计与事实冲突、需要改变 job 协议/权限/中继关闭行为、命中他人 dirty 文件或无法复用 caller seam，停止并报告具体位置。
- 每个 mutation 阶段前重跑 IDEV-STD BOUND/semantic/fingerprint preflight，核对 HEAD、dirty/untracked 与批准范围；只读 receipt payload 并核验 hash。新路径按 Operational Discovery、Corrective、Semantic Amendment 或 Ownership Conflict 分类；Semantic Amendment/Ownership Conflict 立即返回 design/plan Gate。
- G032：新增字段/路由按 additive schema 处理；若保留旧 payload/模板读取路径，必须写 `// DEPRECATED(vX): remove in vY` 并在最终清单列出；无人使用的兼容分支直接删除。不得新增无标记兼容分支。
- PostToolUse 无匹配必须在本地返回且不请求 server；只有匹配到 id 才在中继已布防或显式命令路径调用登记。Stop 盯 job 只在已有 turn wait 循环内工作，不增加忙等定时器；server 错误沿现有 transient retry，不能阻塞终端超过 Stop 预算。
- 测试使用 `t.TempDir()` 与随机端口；不得写真实 `.claude/settings.json`/`.codex/hooks.json`，不得重启/reload 正在运行的 server/worker，不碰真实配置目录。

## 波次与依赖

T00 基线和实际落点核查 → T1 三项固定红测试独立提交 → T2 watch 存储/API 与清理 → T3 PostToolUse 识别、Stop 轮询/注入 → T4 CLI、模板、Web、README/skill → T5 全量质量门与设计实测记录。单 Agent 顺序执行，按功能点分别 conventional commit。

## 任务

### T00 基线和实际落点核查

- 文件：只读 Approved 设计 SESS-03；`internal/hookrelay/{payload.go,run.go,install.go,*_test.go}`；`internal/commands/{hook.go,session.go,*_test.go}`；`internal/sessionrelay/{service.go,release_job_test.go,*_test.go}`；`internal/jobstore/{sessions.go,store.go,*_test.go}`；`internal/httpapi/server.go` 的 sessions 路由/handler/测试；`internal/client` session/job API；两个 hooks 模板；`web/src/views/Sessions.vue` 与 API 类型/测试。
- 动作：确认 Claude/Codex PostToolUse stdin 工具名和输出字段，确认现有终态读取可返回标题/status/exit/duration，确认 caller 如何从 session owner 传递到 job 读取，确认 session 删除和 SessionEnd 的事务边界，确认 Stop wait 窗口可插入批量 watch 查询而不改变 web reply 行为；记录任何 G032 旧路径。
- 验证：`git status --short --untracked-files=all`、`git log -1 --oneline`、定向源码/测试核对、IDEV-STD 阶段指纹。完成标准：T1–T4 的字段流、权限、生命周期和注入边界明确，无未裁决核心语义。
- 依赖：计划获批和后续明确实施请求。

### T1 三项固定测试先行并单独 red 提交

- 文件：按实际 seam 写入 `internal/hookrelay/*_test.go`、`internal/httpapi/*_test.go`、`internal/jobstore/*_test.go`、`internal/sessionrelay/*_test.go`、`internal/commands/*_test.go`；只 stage 测试文件。
- 动作：写固定名 `TestPostToolUseRegistersJobWatch`：Claude 与 Codex payload 的工具名/输出含 `job <id> submitted` 或 `gofer job watch <id>` 时登记到指定 session；不含匹配时无任何 HTTP 请求；同 job 重复登记幂等。写 `TestStopHookReleasesOnWatchedJobTerminal`：中继布防且 Stop 挂起时 job 终态放行，续接 JSON 含 `[gofer job 完成] <id> <标题> status=… exit=… 耗时…`，未终态继续等待 web，注入后移除。写 `TestStopHookMergesSimultaneousFinishes`：同一轮两个终态合并一条注入。
- 验证：运行固定测试集合，同时记录 exit code 与 PASS/FAIL 原文；确认 red 原因是目标行为缺失而非编译/夹具错误。提交前 `git status --short`、`git diff --cached --name-only`。
- 完成标准：三项有效 red 证据和仅含测试的独立 `test(session): cover job watch registration and stop release` 本地 commit。
- 依赖：T00。

### T2 会话待盯存储、HTTP/API 与清理

- 文件：`internal/jobstore/sessions.go`、schema/迁移入口及测试；`internal/httpapi` sessions handler/路由/测试；`internal/client` session 类型/方法及测试；必要的 `internal/sessionrelay` service seam。
- 动作：新增 session watch 关系（session id + job id，必要时登记时间/去重约束），实现按 session 增/查/删和清空；POST/GET `/v1/sessions/{sid}/watches`，删除接口按实现需要提供。登记和读取先验证 session 存在；返回 job 摘要沿现有 job caller 权限读取，越权 job 不泄露。SessionEnd heartbeat、删除 session 时清空；清理幂等。复用现有 JSON envelope、caller token 和 job 终态模型，不复制 job 状态。
- 验证：watch 存储/HTTP 定向测试；user/job caller 权限、重复登记、未知 session/job、SessionEnd/删除清理；`go test ./internal/jobstore/ ./internal/httpapi/ ./internal/sessionrelay/ -run 'Watch|Session' -count=1`；改动 Go 文件 `gofmt -l` 无输出。
- 完成标准：API 与存储为单一待盯真源，权限和清理可观察，提交 `feat(session): persist watched jobs`。
- 依赖：T1。

### T3 PostToolUse 识别、Stop 轮询与续接注入

- 文件：`internal/hookrelay/payload.go`、`run.go`、`install.go` 及测试；`internal/sessionrelay`/`internal/client` 的最小读取接口；必要的 jobstore 读取 seam。
- 动作：扩展 Claude/Codex payload 以保留实际工具名和工具输出字段，并核对两种格式；实现本地正则识别两个输出口径，输出去重 job id。PostToolUse 无匹配立即返回且不连 server；匹配时调用当前 session watch API，网络失败静默记录。Stop 在既有 `WaitSessionTurn` 长轮询窗口中按窗口边界批量读取 watches/jobs，过滤非终态，按完成时间或稳定 id 合并同轮终态；构造设计规定的注入文本并通过 `BlockJSON` 返回，成功注入后删除对应 watches。web reply、relay off、超时、服务不可达和中继未布防保持原行为；不得在中继关闭时为后台 job 额外请求。
- 验证：三项固定测试转绿；补充 Codex/Claude payload、重复/无匹配无网络、非终态继续等待、job 权限过滤、退出码缺省和耗时格式、网络重试/Stop 预算测试；`go test ./internal/hookrelay/ ./internal/sessionrelay/ ./internal/httpapi/ -run 'Test(PostToolUseRegistersJobWatch|StopHookReleasesOnWatchedJobTerminal|StopHookMergesSimultaneousFinishes)' -count=1`。
- 完成标准：Stop 轮询不忙等、单/多终态注入和移除可观察，提交 `feat(hook): release stop waits for watched job completion`。
- 依赖：T2。

### T4 显式命令、模板安装、Web 与文档

- 文件：`internal/commands/session.go` 及测试；`hooks/claude.settings.json`、`hooks/codex.hooks.json`；`internal/hookrelay/install.go` 及安装测试；`web/src/api/{client.ts,types.ts}`、`web/src/views/Sessions.vue` 及测试；`README.md`、`README.zh-CN.md` 会话中继段；`skills/gofer-usage/SKILL.md`；设计文档（实测记录待 T5）。
- 动作：新增 `gofer session watch <job-id>`，session id 沿 current-session 文件解析，无法解析时给出明确的 `--session`/`session ls` 指引。模板加入 shell-only PostToolUse matcher（Claude `Bash`，Codex 以 T00 核对的实际工具名），命令仍调用 `gofer hook <agent>`；安装/卸载/重复安装覆盖新增条目且不碰外部 hooks。Sessions.vue 显示待盯 job 的 id、标题、状态，并提供 job 详情链接；轮询后与 session 状态一致，空列表有明确提示。README/skill 说明自动识别、显式 watch、Stop 注入、权限和中继关闭边界，中英文 README 保持并行。
- 验证：命令 current-session/错误/幂等测试；模板 Install 两次后条目数稳定、卸载无 gofer-owned 残留；Web 测试、`cd web && pnpm test && pnpm typecheck && pnpm build`，若 Windows esbuild postinstall 仍被 ELF/权限阻断，使用仓库内 Win32 `.bin` 等效命令并原样记录；确认普通会话、多个 job、终态和无权限 job 的显示。
- 完成标准：CLI、模板安装和 Web 均遵循同一 API/权限语义，按功能点提交 `feat(session): add explicit watch command and web display`，文档可独立时提交 `docs(session): document watched job stop hook`。
- 依赖：T3。

### T5 全量质量门与交付证据

- 文件：必要时只更新设计文档 SESS-03 实测记录；验证产物放 `tools/gofer/tmp/`，不 stage。
- 动作/验证：tracked Go 文件全仓 `gofmt -l` 无输出；Windows/Linux `go build` 产物写入 tmp；`go vet ./...`；`go test ./internal/hookrelay/ ./internal/sessionrelay/ ./internal/httpapi/ ./internal/commands/ ./internal/jobstore/ -count=1`（Windows 慢包必须等待终态，既知基线照实列出）；Web 三命令；控制字符扫描、`git diff --check`。每项同时记录 exit code 与原始输出，不以 scheduler/health 或局部测试代替行为验收。
- 完成标准：三个固定测试及新增行为全绿；质量门无未裁决回归；设计 SESS-03 实测记录只写已验证事实；列出每个功能点 commit、`git log`、`git status`、G032 清单、未完成项和人工决策点。不得 push。
- 依赖：T2–T4。

## 回滚与恢复

每个功能点本地 commit 是恢复点。每次提交前执行 `git status --short`，只 stage 当前 owner 文件并复核 `git diff --cached --name-only`；不得吸收 unrelated dirty work。失败时记录首个失败、当前 HEAD、已验证范围和下一步；撤销仅针对本任务准确 commit 评估 `git revert`，先核对依赖。发现协议、权限或中继生命周期变化时停止并返回 design/plan Gate，不以修改计划版本掩盖语义变化。

## 人工 Gate

本计划候选提交后停在人工计划批准 Gate。监督者依代批授权批准后，还需本会话续接明确实施请求；执行方式已定为 DIRECT_CONTINUOUS。发现需要改变 job 协议、caller 权限、Stop/web reply 语义、Claude/Codex hook 事件契约或真实配置/服务行为时返回 design/plan Gate。push、远程升级、部署、live 服务、真实配置、真实任务和硬件/流量动作另需具名外部授权，本候选不含这些动作。

## 可追溯性

| Approved SESS-03 / 本 job 验收 | 任务 | 可观察验证 |
|---|---|---|
| Claude/Codex PostToolUse 识别两个 job 输出口径，未匹配零网络，重复登记幂等 | T1、T3、T4 | `TestPostToolUseRegistersJobWatch`、payload/无网络测试、模板 matcher/Install 测试 |
| 待盯 job 受 caller 权限约束，SessionEnd/删除清空，注入后移除 | T1–T2、T3 | HTTP/jobstore 权限和清理断言、Stop release 测试 |
| Stop 挂起时终态放行，非终态继续 web 等待，多个同轮完成合并 | T1、T3 | `TestStopHookReleasesOnWatchedJobTerminal`、`TestStopHookMergesSimultaneousFinishes`、原有 web reply 回归 |
| 注入格式、显式 `session watch` 和 current-session 错误口径 | T3–T4 | BlockJSON/CLI 测试、命令输出原文 |
| 模板去重、Web id/标题/状态可跳转、README/skill 用法 | T4–T5 | Install/Web 测试、pnpm 三命令、文档核对 |
| G021/G022/G032、离线边界和质量门 | T00、T2–T5 | preflight 指纹、依赖检查、gofmt/build/vet/Go+Web 测试、控制字符与 Git 原始输出 |

## 完成 Gate 与剩余工作

S4 仅在三个固定测试从有效 red 到 green、待盯存储/API/权限/清理、PostToolUse 本地快速路径、Stop 终态合并注入、显式命令、模板安装、Web/文档和质量门均有证据且按功能点本地提交后，才能标记实施完成。计划候选提交不等于实施完成；本期不做 push、部署、真实 server/worker、真实配置或现场验收。S1–S3 是独立已完成/已批准期，任何跨期依赖需另行批准。
