<!-- template_id: plan; template_version: 1.2.0 -->
# X 批 Windows daemon、升级记录、job 审计与 PTY 稳定性实施计划

> 状态：Draft 0.1 / DIRECT_CONTINUOUS 执行

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-03 | Codex | 将 X1–X4 拆为测试先行的独立功能点 |

## 规划可靠性声明

- `thinking_mode=RIGOROUS`
- `core_objective=修复 X1–X4，并以源码、失败测试、运行时和临时 Windows 冒烟证据证明用户可见结果`
- `allowed_scope=tools/gofer 的代码、测试、README、skills/gofer-usage、此计划及 tmp 验证证据`
- `non_goals=不重启或 reload 正式 server/worker，不修改正式配置目录，不 push，不改变未由 X1–X4 指定的协议语义`
- `expansion_policy=DEFER_OR_REQUEST`
- `review_budget=一轮实现内 focused review；发现语义或所有权冲突立即停止`
- `stop_conditions=设计冲突、规范指纹变化、临时栈无法隔离、质量门失败且无法在当前 owner 内修复`

## 目标与完成定义

1. Windows local runner 的 job object 允许显式 daemon breakaway，普通取消/超时仍杀完整进程树；daemon 回退时有 WARN。
2. worker 升级历史保留最近 10 条，API/CLI/Web 分别按约定展示；交接后的 stdout/stderr 追加到 `<run>/worker-<id>.out.log`，README 与 skill 说明日志含义；离线升级按钮显示原因并与 reload 按钮留间距。
3. `job.redacted` 审计的 `file_matches` 与返回报告一致，并在文件阶段完成后以同一 DB 事务补写。
4. `TestE2EPtyCastRecordingWorkerSourceStillDownloadable` 修复真实竞态；`-count=20` 与并发全量测试稳定。
5. 每项先写失败测试、按功能点单独 conventional commit；tracked Go `gofmt -l` 无输出；Windows/Linux build、vet、全量 Go 测试和 Web 三命令均保留原始输出；不 push。

## 范围、排除项与授权

范围仅限 X1–X4 及同步文档。测试使用 `t.TempDir()` 或 `tools/gofer/tmp` 临时目录；smoke 的每条 gofer 命令显式指定临时 `--server` 或 `-c`，随机端口，剔除 `CLAUDE*` 环境变量。禁止触碰 `D:\work\inhere\config\win-env\gofer`、正式 8767/server/worker；不把 Web 输出写入 `web/dist`。

- `host_or_non_offline_action=REQUIRED`

## 输入与批准证据

- 任务设计：`docs/design/2026-10-03-worker-remote-upgrade-design.md`、`docs/design/2026-10-03-notify-length-job-redact-design.md`。
- 相关基线：`docs/plans/2026-06-25-serve-worker-daemon-plan.md`、当前 X3/X4 源码与测试。
- 当前执行授权：用户要求“先写实施计划候选，提交后直接连续实施”，并明确“不 push”及 Windows 临时真实验证。
- 规则：`tools/gofer/AGENTS.md`、工作区 `workspace.md`、house-rules/gofer-repo 注入规则。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | Windows 进程树 containment 与 daemon 脱离 | `internal/proctree`, `internal/daemon`, local runner tests | 复用现有 job object 和 `CREATE_BREAKAWAY_FROM_JOB` | 仅扩展 job limit flag、回退日志和平台测试 | OWNER_EXTENSION | 当前 job object 未声明 `BREAKAWAY_OK`，回退日志为 Debug | 仍由 proctree/daemon 原 owner 管理，无新 containment 实现 |
| CAP-02 | worker 升级历史与日志展示 | `internal/workerupgrade`, worker API/CLI、Runners Web、README/skill | 复用现有 latest record、API DTO、卡片和日志路径 | 扩展 bounded history、响应字段和展示折叠 | OWNER_EXTENSION | 当前只保存最近一次，交接输出未明确追加日志 | history 固定 10 条，UI 卡片仍只读 latest |
| CAP-03 | job.redacted 文件计数审计 | `internal/jobstore/redact.go`、redact tests | 复用现有事务、文件扫描和 `RedactReport` | 将文件阶段结果纳入同一事务审计 detail | OWNER_EXTENSION | 审计 INSERT 在文件处理前，`file_matches` 恒为 0 | 不新增审计表；保持原子提交 |
| CAP-04 | PTY attach tail flush | `internal/httpapi/attach_handler.go`、`internal/worker/pty_cast_e2e_test.go`、ptyrelay | 复用现有 pump/relay Done channels 和 bounded drain | 修复产品时序或测试握手等待点并补回归测试 | OWNER_EXTENSION | 当前偶发 attach NormalClosure 早于最终 sentinel | 不新增传输协议；只保留一个关闭顺序 owner |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 独立进程管理器 | CAP-01 | 现有 proctree/daemon 已覆盖 containment；新增工具不能缩短验收路径 |
| 升级历史数据库 | CAP-02 | 现有 workerupgrade owner 可 bounded 保存历史；新增数据库会引入第二生命周期 |
| 第二审计表 | CAP-03 | 现有 jobstore 事务已覆盖审计；新增表会破坏原子性边界 |

## 前置检查与 fail-closed 条件

- 每个 mutation stage 前以当前 fingerprint/scope 执行 `idev-std resolve --response verify --task implement --task test --mutating`；变化即停。
- 计划 validator 必须 PASS；实施发现跨模块协议、schema、安全、外部动作或所有权变化时记录并停在 Gate。
- 先写失败测试并单独运行确认失败，再实现；不写 `t.Skip` 占位。
- X1 Windows smoke 只用临时配置和随机端口；完成后停止临时 serve/worker。普通取消/超时至少一条真实临时 job 证据。
- X4 若 focused test 暴露产品协议竞态，按当前 owner 修复；若仅测试 harness 时序，最小化测试等待并记录根因。

## 波次与依赖

| 波次 | 内容 | 依赖 | 结果 |
|---|---|---|---|
| W0 | 本计划、validator、基线 | 规范 BOUND、设计已批准 | 计划 commit |
| W1 | X1 proctree/daemon breakaway 与杀树验证 | W0 | 测试、实现、Windows smoke、独立 commit |
| W2 | X3 job.redacted 审计计数 | W0 | 测试、实现、独立 commit |
| W3 | X2 worker history/API/CLI/Web/log 文档 | W0 | 测试、实现、Web/文档、独立 commit |
| W4 | X4 PTY 竞态定位与修复 | W0 | focused `-count=20`、并发全量、独立 commit |
| W5 | 全量质量门与报告 | W1–W4 | 原始输出、git log/status、G032 清单 |

## 任务

### X1 Windows daemon breakaway

- 文件: `internal/proctree/proctree_windows.go`、`internal/daemon/daemon_windows.go`、对应平台测试和 local runner 测试。
- 动作: 先补 BREAKAWAY_OK 与回退 WARN 的失败测试；设置 `JOB_OBJECT_LIMIT_BREAKAWAY_OK`；仅显式 breakaway 进程脱离；回退日志明确后台进程仍在调用方 job 内；保持普通子孙由 job object 统一终止。
- 验证: Windows focused tests；临时 serve + exec job 内临时 worker `gofer worker -d`，job 结束后检查 worker 存活在线，再停掉；一次临时取消/超时检查整棵树退出。
- 完成标准: daemon 不再因调用方 job object 被清掉，回退可诊断，普通 job 杀树行为未回归。
- 依赖: W0。

### X2 worker upgrade history、UI 与日志

- 文件: `internal/workerupgrade`、worker API/CLI/DTO、`web/src` Runners 组件与样式、`README.md`、`README.zh-CN.md`、`skills/gofer-usage/` 及对应测试。
- 动作: 先补最近 10 条、`upgrade_history` API、CLI 最近 3 条、UI 离线原因/间距/可展开历史、交接日志追加的失败测试；再实现并以代码/help 为准同步文档。G032 旧路径若仍保留须加移除标记。
- 验证: focused Go/Web tests、CLI help、Web 三命令临时 outDir；检查日志追加路径和字段。
- 完成标准: API 返回 10 条以内历史，CLI 3 条，卡片 latest + 可展开历史，离线原因可见，交接 stdout/stderr 追加日志有文档说明。
- 依赖: W0。

### X3 job.redacted 审计计数

- 文件: `internal/jobstore/redact.go`、对应 jobstore 测试/HTTP 测试（若现有 owner 需要）。
- 动作: 先写审计 `file_matches` 与报告不一致的失败测试；将文件阶段结果在事务内完成后写入审计 detail，保持 DB 原子性和不回显原文。
- 验证: focused jobstore/HTTP tests，审计 JSON 与返回 `RedactReport` 的文件计数逐项相等。
- 完成标准: `job.redacted.file_matches` 不再恒为 0，失败回滚不产生半条审计。
- 依赖: W0。

### X4 PTY cast attach 稳定性

- 文件: `internal/httpapi/attach_handler.go`、`internal/worker/pty_cast_e2e_test.go`、必要的 ptyrelay 测试。
- 动作: 先运行目标测试确认失败窗口并添加最小时序断言；根据证据修复 relay/pump 关闭竞态或测试握手，不改变协议语义；禁止 `t.Skip`。
- 验证: `go test ./internal/worker -run TestE2EPtyCastRecordingWorkerSourceStillDownloadable -count=20` 原文；并发全量 Go 测试；必要时 attach focused tests。
- 完成标准: 20 次全部通过，首次输出不再在最终 sentinel 前 NormalClosure，记录根因和修复 owner。
- 依赖: W0。

## 回滚与恢复

每个功能点先确认 `git status --short`，只暂存 exact owner 文件并单独提交；失败测试保留在对应 commit 或同功能点 corrective commit。运行时回滚只删除 `tools/gofer/tmp` 下临时进程、配置、日志和 outDir；不触碰正式路径。若发现 Semantic Amendment、Ownership Conflict 或规范指纹变化，停止 mutation 并报告首个边界。

## 人工 Gate

用户当前请求已同时提供计划创建和连续实施授权；本地提交授权覆盖 X1–X4 exact owner。唯一外部动作 Gate 是本机临时 Windows serve/worker smoke（不含正式服务、push、发布、迁移、硬件）。

## 可追溯性

X1 → proctree/daemon 测试与 Windows smoke；X2 → workerupgrade/API/CLI/Web 测试与文档/日志验收；X3 → jobstore 审计计数测试；X4 → PTY attach/worker E2E `-count=20`。每项验证原始输出、commit 与 lifecycle 状态写入最终报告。

## 完成 Gate 与剩余工作

仅在 W5 所有格式、build、vet、全量 Go、X4 `-count=20`、Web 三命令、Windows 临时 smoke 和 git cleanliness 有原始输出时报告完成；未完成项、G032 清单、skill 章节变更和需人工决策点逐项列出；不 push。

## 执行状态（2026-10-03）

- W1/X1 `COMPLETE`：`2dfefe9b`；Windows 临时 job 内 `worker -d` 在 job 结束后仍在线，普通取消后的子进程退出。
- W2/X3 `COMPLETE`：`375cf9d0`；审计 `file_matches` 与返回报告一致。
- W3/X2 `COMPLETE`：`dd67b0e7` + `d8a8d270`；最近 10 条历史、API/CLI/Web 与日志文档同步完成，并补固化重启后历史记录引用。
- W4/X4 `VERIFIED`：基线已有 `056baeb9` 的 attach pump drain 修复；Linux 目标测试 `-count=20` 与 worker 包并发全量通过，本批无需重复修改协议或测试时序。
- W5 `COMPLETE`：gofmt/build/vet/全量 Go/Web 质量门及 Windows 临时证据已收集；第一次全量 Windows 测试的临时 DB cleanup 锁在单测复跑后，第二次原命令全量通过。
