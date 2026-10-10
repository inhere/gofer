<!-- template_id: plan; template_version: 1.2.0 -->
# 通知长度配置与 job 脱敏删除（V1–V2c）实施计划

> 状态：Draft 0.1 / 已收到当前 DIRECT_CONTINUOUS 执行请求；执行方式：DIRECT_CONTINUOUS

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-03 | Codex | 将已批准设计拆成 V1 通知长度、V2a 脱敏、V2b 删除、V2c 提交时秘密提示及隔离验收波次 |

## 目标与完成定义

`thinking_mode=RIGOROUS`。核心目标是让 IM 通知长度由全局及 webhook 配置控制并在统一渲染处安全截断，同时为已结束 job 提供不回显原值的全量持久内容脱敏、可审计删除，以及提交前只警告的秘密形态检查。`allowed_scope=2026-10-03-notify-length-job-redact-design.md` 的 V1、V2a、V2b、V2c；`non_goals=远程 worker 工作目录/缓存副本、已发 IM、外部日志、server 侧秘密拦截、真实配置/常驻进程、push/deploy/migration`；`expansion_policy=DEFER_OR_REQUEST`。评审预算为一次现状发现和一次候选核对；若发现设计未定义的协议、权限、schema 或验收语义，立即停止并回到 design/plan Gate。

完成必须同时满足设计列出的固定 Go/Web 测试、受影响包测试、Go/前端质量门、临时 serve 冒烟和数据库/结果目录全量原值扫描；本地编辑、构建或单测通过都不单独构成完成。

## 范围、排除项与授权

- Git 根仅为 `D:/work/inhere/my-tools-dev/gofer`，分支为 `main`。保持 G021/G022：commands/httpapi 只绑定、鉴权、参数校验和错误映射，编排放 `internal/job`/`internal/core`，持久化放 `internal/jobstore`。
- V1 只扩展 `server.notification.max_text_runes`、webhook 同名覆盖、渲染时生效值、DingTalk/Feishu UTF-8 字节安全上限和 session reply preview；generic webhook 的 JSON 契约保持不变。设置页只编辑已登记 editable 字段，并通过现有 reload 配置路径热生效。
- V2a 只接受 `--literal-from-stdin` 和/或 `--pattern`，原文绝不进入命令行参数、响应、审计或日志；只处理已结束 job，覆盖 job 关联的 DB 文本列、结果目录文本文件和持续会话轮次，二进制跳过并列出。旧兼容路径不新增；必要保留的旧路径按 G032 加移除标记。
- V2b 只删除已结束 job 的记录、关联事件/评论/附件/结果目录/plan 关联行，保留不含原标题的审计事件；CLI 多 id 需要 `--yes` 或确认，Web 只有有权限时显示二次确认按钮。
- V2c 只在 CLI 提交前扫描 command/args/prompt 并写 stderr 警告，不阻止提交；`--no-secret-check` 关闭。server 不拦截。
- `host_or_non_offline_action=REQUIRED`：真实冒烟只使用临时 `GOFER_CONFIG_DIR`、随机端口、临时数据库/result_dir 和临时配置；不重启/reload 运行中的 server/worker，不访问 `D:/work/inhere/config/win-env/gofer`，不 push。
- 同批更新 `skills/gofer-usage/` 与 README 的 job/notification 相关段落；不修改 worker 升级分支拥有的 worker 文件和 worker 文档段落。

## 输入与批准证据

- 已批准设计：[`docs/design/2026-10-03-notify-length-job-redact-design.md`](../design/2026-10-03-notify-length-job-redact-design.md)，状态 Approved，用户于 2026-10-03 确认 V1、V2a/b/c。
- 当前任务书明确要求先提交本计划候选，随后按 V1 → V2a → V2b → V2c 连续实施；测试先写、按功能点单独提交、禁止 push，并指定临时 serve 验收矩阵。
- 仓库约束来自 `AGENTS.md`：G032 兼容策略、G021/G022 分层、G045 用户可见功能同步 `skills/gofer-usage/`，测试使用 `t.TempDir()`，tracked Go 文件提交前 `gofmt -l` 无输出。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 按目标 webhook 解析通知上限并安全渲染 | `internal/config.NotificationConfig/WebhookConfig`、`internal/notify/render.go`、`internal/job/session_notify.go`、现有 config reload/settings API | 复用既有 config clone/loader、`MatchWebhooks`、DingTalk/Feishu renderer、session event pipeline | 在 config/notify/job 既有 owner 增字段和运行时参数；不建第二渲染器 | OWNER_EXTENSION | 当前 renderer 只读包级 500，session preview 固定 200，未按目标 webhook 取值 | 若在调用方各自截断会造成 generic/IM 口径漂移；统一 helper 由 notify owner 持有 |
| CAP-02 | job 全持久内容 literal/regex 脱敏 | `internal/jobstore/store.go` schema、jobs/prune/events/comments/interactions/pty/session/workflow owners、result_dir 文件工具 | 复用 jobstore DB、job 权限/owner-admin 判定、事件审计、结果目录路径和文本日志格式 | 在 jobstore 增加 job-scoped redact/delete primitives，job/httpapi/commands 做薄适配 | OWNER_EXTENSION | 现有 prune 是删除而非替换，且没有跨所有文本列和会话轮次的统一扫描 | 双重扫描或只处理 stdout 会漏 prompt/事件/评论；唯一服务必须按 schema 清单驱动并返回命中统计 |
| CAP-03 | 已结束 job 删除及保留审计 | `internal/jobstore/prune.go`、现有 audit/event 写入、HTTP job handler、CLI job 命令、Web JobDetail | 复用终态判定、caller/admin 鉴权、关联删除事务、列表/详情 404 行为 | 扩展 jobstore 原子删除事务和保留审计事件，入口只映射 `--yes`/确认 | OWNER_EXTENSION | 现有 prune 面向保留策略，不提供用户指定单 job 删除及审计墓碑 | 删除顺序和审计保留必须单事务/明确异常边界，不能复用会误删 workflow 的全局 prune |
| CAP-04 | 提交前秘密形态扫描 | `internal/secret` 现有正则/脱敏工具、`internal/commands/job.go` request 构造和 stderr helper | 复用已有 secret.RedactString/命中形态及 job run 参数组装 | 在 job run 统一入口增加只警告扫描与 `--no-secret-check` | THIN_ADAPTER | 当前提交路径无秘密提示，需覆盖 command/args/prompt 且不回显值 | 各 runner 自己扫描会漏 HTTP/任务文件/模板路径；必须在最终 request 边界单点调用 |
| CAP-05 | Web 设置、job 删除和文档同步 | `web/src/views/Config.vue`、`JobDetail.vue`、`web/src/api`、README/skills/gofer-usage | 复用现有 editable policy、config patch、job detail API、权限和确认组件 | 只增加字段、按钮、API 类型和用户说明；不改 worker section | THIN_ADAPTER | 当前页面无 max_text_runes 输入和 job delete 操作 | Web 与 `--help`/HTTP 契约漂移会使 G045 失效；同批定向 Vitest/文档检查 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | NONE | NONE | NONE | NONE | NONE | NONE | NONE | NONE |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 独立 redaction 数据库/索引 | CAP-02 | 全量扫描可在单事务和临时文件替换内完成；新增索引会形成第二真源并扩大生命周期 |
| 独立 secret scanner CLI/library | CAP-04 | 现有 `internal/secret` 已有模式和替换能力；新增实现会产生匹配口径漂移 |

## 前置检查与 fail-closed 条件

- 计划起草基线：Git 根 `D:/work/inhere/my-tools-dev/gofer`，branch `main`，HEAD 以提交前实时输出为准；当前未发现 tracked dirty。`.codebase-memory/`/临时运行目录若出现，分类为本任务外保留，不纳入提交。实施每个 mutation stage 前重新核对 status、HEAD、计划/设计身份和 IDEV-STD fingerprint。
- 图索引项目 `gofer` generation 为 `2026-09-05T06:59:33Z`，status ready；`web/src/api/types.ts` 有 parse_partial 1–989。图查询对新设计符号无结果，已按 AGENTS 先核对索引状态并对 flagged/新路径回退当前源码 `rg`/定向读取；候选路径在每波前再做 coverage 检查或记录文本检索证据。
- T00 必须梳理 `internal/jobstore/store.go` 所有表及 job 外键/文本列，形成 V2a 测试清单：`jobs`（command/args/prompt/title/result/error/verify/usage/rules/xfer/session/worktree 等文本字段）、`interactions`、`job_events.detail_json`、`event_deliveries.last_error`、`workflow_events.detail_json`（只处理关联 job 的链路）、`pty_sessions.recording_uri`/记录文件、`job_wakeups`、`job_retries`、`job_tokens`（不改 hash 原文，但确认是否 job 关联）、`comments.body`、`sessions/session_job_watches` 关联字段、`plans/plan_todos/plan_handoffs/plan_decisions` 中 job 关联说明、`workbench_thread_prefs/layouts`、`tracker_*`/`scoped_memories` 仅在存在 job 外键时纳入；attachments/result files 按实际 schema/目录清单补全。无法证明关联关系或发现外部表时停在 Semantic Amendment，不猜测。
- 测试全部使用 `t.TempDir()`；冒烟前构建到 `tmp/`，临时 serve 显式 `--server http://127.0.0.1:<port>` 或 `-c <temp config>`，剔除 `CLAUDE*` 环境变量，不向 `web/dist` 写构建产物。
- 任一发现改变权限、终态条件、DB schema 语义、generic webhook JSON 契约、外部协议或验收边界，分类为 Semantic Amendment 并停止；命中 u1-upgrade dirty owner（worker/wshub/commands/worker.go 及 worker 文档节）分类 Ownership Conflict 并停止。

## 波次与依赖

| 波次 | 任务 | 依赖 | 出口 |
|---|---|---|---|
| W0 | 现状/DB schema/权限/路径核对 | 设计、当前请求 | owner、文本列、终态和审计落点明确；无未决语义冲突 |
| W1 | V1 通知长度配置与渲染 | W0 | 三个固定通知测试先红后绿，配置 reload/settings 与 skill/README 同步 |
| W2 | V2a job redact | W1 | 四个固定 redact 测试先红后绿；DB 全表和 result_dir 原值扫描通过 |
| W3 | V2b job delete | W2 | 删除/权限/审计测试先红后绿；CLI/Web/API 行为闭合 |
| W4 | V2c 提交时秘密提示 | W3 | secret warning 测试先红后绿；`--help`/文档和 `--no-secret-check` 一致 |
| W5 | 质量门与隔离真实冒烟 | W4 | Go/Node gates、临时 serve 长消息/redact/delete 全链路、无原值/无残留进程 |

每波固定顺序为：`apply_patch` 写有效失败测试 → 定向测试记录 exit code 与原始 FAIL → 只提交测试 `test(...)` → 实现并转绿 → 按功能点提交 `feat/fix/docs(...)`。不写 `t.Skip`，不以骨架或局部编译宣称完成。

## 实施进度（2026-10-03）

| 波次 | 状态 | 本地提交 |
|---|---|---|
| W0 | 完成：schema/owner/配置与现有路由核对 | `1ecbe08e`（计划候选） |
| W1 | 完成：通知长度、UTF-8 字节 cap、session preview、设置页与文档 | `cc89583e`、`b22ddaf8` |
| W2 | 完成：终态 job 全持久内容脱敏、owner/admin、CLI/API、二进制跳过 | `35547619`、`1cf5cd34`、`9c3a2379` |
| W3 | 完成：终态 job 删除、审计、CLI/API/Web | `b53a5782`、`f07e3654`、`25045fda`、`354ba128` |
| W4 | 完成：提交时秘密形态只警告与 `--no-secret-check` | `027c29ed`、`0dd5ab96` |
| W5 | 完成：全量质量门与临时 serve 冒烟 | `gofmt -l` 空输出；Windows/Linux build、vet、全量 Go test、Web 三命令均 exit 0；临时 serve `127.0.0.1:18765` 完成提交→redact→delete，DB 全表/结果目录无假秘密，审计行保留 |

## 任务

### T00 现状与 schema 清单

- **Owner/文件**：只读 `internal/config/{model,loader}.go`、`internal/notify/{render,match}.go`、`internal/job/{events,session_notify}.go`、`internal/jobstore/store.go` 与各表 owner、`internal/commands/job.go`、`internal/httpapi/job_handler.go`、`internal/secret`、`web/src/views/{Config,JobDetail}.vue`；必要时将核对结果追加到本计划进度，不改 Approved 设计。
- **动作**：确认当前配置 clone/reload、webhook kind、session event、终态与 owner/admin 判定；按 `store.go` 的 CREATE TABLE 和所有 `job_id` 外键列出完整 V2a 文本列/文件范围；确认现有 audit/event 能力与 delete 事务边界；执行 `--help` 现状快照。
- **验证**：图索引 `index_status/search_graph/check_index_coverage`（若工具返回不足则保留精确 `rg`/读取证据）；`git status --short`、`git log -1 --oneline`；不启动真实服务。
- **完成标准**：W1–W4 每个 owner、测试入口、权限/终态/审计边界和 G032/G045 影响明确；发现设计外语义则停。

### T01 V1 通知长度

- **Owner/文件**：`internal/config/model.go`、loader/clone/config tests；`internal/notify/render.go` 与 tests；`internal/job/session_notify.go` 与 tests；`internal/job/events.go`/相关 renderer 调用；`internal/httpapi/config_handler.go`、`web/src/views/Config.vue`、web API 类型；`README.md`、`README.zh-CN.md`、`skills/gofer-usage/{SKILL.md,references/*}` 的 notification/job 段落。不得改 worker 升级 owner 文件。
- **动作**：增加全局 `max_text_runes` 默认 3000、`<=0` 回退默认，webhook 同名字段 0 继承；渲染时将生效上限作为参数传入，统一覆盖 job 完成/失败、审批、session 等现有 `clampText` 路径；session preview 读取完整最后一段但最多 64KB，不先截 200；DingTalk/Feishu 按 UTF-8 边界限制 18000 bytes 并保留“…（已截断，完整内容见链接）”提示；generic JSON 结构保持现状；配置视图 editable 登记、reload 后下一条生效。
- **固定测试**：`TestNotifyMaxTextRunesConfigurable`、`TestNotifyByteCapForDingTalk`、`TestSessionReplyPreviewUsesNotifyLimit`；配置 decode/clone/reload 和 Web Vitest 定向断言。
- **完成标准**：同一 Message 按不同 webhook 得到不同 rune 上限；多字节截断不破坏 UTF-8 且 <=18000 bytes；session reminder 不丢最后一段；设置页可编辑且文档只描述已实现行为。提交至少拆为测试、V1 代码、文档/skill（若可独立验证）。

### T02 V2a job redact

- **Owner/文件**：新增能力仍归 `internal/jobstore` 既有文件/最小 helper；`internal/job` 服务方法；`internal/httpapi` job handler/routes；`internal/client` job API；`internal/commands/job.go`/job redact command tests；Web job detail（如设计要求只读提示则不造额外入口）；`skills/gofer-usage` job 段落与 README。
- **动作**：实现 literal stdin（整行或全部输入，去尾换行）和 RE2 pattern 的至少一个校验；在服务端验证终态与所有者/admin；以固定替换串处理 DB job 全部文本列、事件/日志/评论/会话轮次和关联结果文件；文本文件安全原子替换，二进制跳过并列出相对路径；响应仅给各位置命中数/跳过列表；审计记录 caller/time/job/各位置命中数/literal 与 pattern 个数，不存原值和 literal，pattern 原文按设计最小保留；远程 worker/cache/已发通知/外部日志明确提示局限。
- **固定测试**：`TestJobRedactLiteralAllLocations`（完整 job 流程、假秘密、DB 全表与 result_dir 文本扫描）、`TestJobRedactRejectsRunningJob`、`TestJobRedactNeverEchoes`、`TestJobRedactOwnerOrAdminOnly`；HTTP/CLI 不回显断言。
- **完成标准**：一个假秘密在所有 DB 文本列、stdout/stderr、会话轮次、评论和 result_dir 文本文件均不存在；running/queued/awaiting_input 拒绝；无权限拒绝；响应、错误、审计和日志均不含原值；二进制只列出不修改。G032 若保留旧入口必须有移除标记，否则删除无用兼容路径。

### T03 V2b job delete

- **Owner/文件**：`internal/jobstore/prune.go` 或 job 删除 owner、`internal/job`、`internal/httpapi`、`internal/client`、`internal/commands/job.go`、`web/src/views/JobDetail.vue`/API 类型、对应 tests 和 `skills/gofer-usage`。
- **动作**：增加已结束 job 的 owner/admin 删除服务；事务内删除 jobs、events、deliveries、interactions、comments、tokens、pty/session/watch、retries、wakeups、result_dir 和 plan 关联行；保留一条审计事件，标题固定替换为“已删除”，不保留原命令/prompt/title；CLI 支持多个 id、`--yes` 或交互确认；HTTP `DELETE /v1/jobs/{id}`；Web 详情页权限控制、二次确认、删除后回列表。
- **固定测试**：`TestJobDeleteRemovesRecordKeepsAudit`，并覆盖 running/owner-admin/多个 id/404、HTTP 和 Vitest 删除按钮。
- **完成标准**：删除后 `job show`/列表/HTTP 详情不可见，关联记录和 result_dir 不存在，审计仍可读且无原标题；运行中 job 不删除，权限边界与 redact 一致。

### T04 V2c 提交时秘密提示

- **Owner/文件**：`internal/commands/job.go` 及命令测试、必要时 `internal/secret` 既有 owner；`skills/gofer-usage` job run 段落、README 中对应说明。
- **动作**：在最终 request 构造前扫描 command/args/prompt：PEM 私钥头、AKIA、sk-/ghp_/github_pat_/xox 前缀和 key/secret/token/password 长赋值；stderr 只报位置和“会被保存在 job 记录中”，不打印完整值；`--no-secret-check` 关闭；覆盖 exec、prompt、任务文件/模板渲染后的最终字段，HTTP/server 提交不新增拦截。
- **固定测试**：`TestSubmitSecretWarning`，另测关闭开关、多个命中、无命中和 stderr 不含原值；更新 `job run --help` 快照/文档。
- **完成标准**：命中仍成功提交且 warning 可见；关闭后无 warning；模式覆盖设计清单且没有 runner 分叉。

### T05 质量门、隔离 serve 与验收

- **Owner/文件**：只在 `tmp/` 保存临时 config、DB、result_dir、二进制、日志和原始输出；不把构建产物写入 `web/dist`。
- **动作/验证**：tracked Go 文件 `gofmt -l` 无输出；Windows/Linux `go build`、`go vet`、`go test ./... -count=1`；Web 三命令按 `package.json` 实际脚本并将 `--outDir` 指向临时目录；控制字符扫描、`git diff --check`。临时 serve 使用随机端口和临时 config，剔除 `CLAUDE*`：渲染长消息检查 rune/byte/截断提示；提交打印假秘密的 exec job，stdin literal redact 后检查 `job show`、日志、result_dir、DB 全部无原值；delete 后 job 不可见而审计在；全部临时进程退出且无 agent 子进程残留。每条 gofer 命令显式 `--server http://127.0.0.1:<port>` 或 `-c <temp config>`，job 上限 5400s。
- **完成标准**：所有固定测试/质量门/冒烟有原始 exit code 和 PASS/FAIL 行；未完成项、环境阻塞或未授权动作明确列出，不把本地验证说成部署/生产验收。

## 回滚与恢复

每个功能点先测试提交再实现提交，提交前 `git status --short` 和 `git diff --cached --name-only` 只包含本功能点 owner；不使用 `git reset --hard`、clean 或覆盖他人 dirty。失败停在首个红/绿边界，保留原始输出和已验证 commit；恢复时重新核对设计/计划、IDEV-STD fingerprint、HEAD/status、临时进程和 ownership，从首个未绿测试继续。只在语义、schema、权限、验收或 lifecycle 改变时修订计划版本。

## 人工 Gate

1. 本次用户消息同时确认已批准设计、DIRECT_CONTINUOUS 计划候选和按 V1→V2c 连续实施，因此计划提交后继续；若后续要求改变协议/权限/schema/验收，必须返回 design/plan Gate。
2. 真实 secret、push、release、deploy、migration、常驻 server/worker、外部消息和硬件动作不在本计划授权；隔离临时 serve 是唯一出机器验证。
3. 若实现需要新模块、额外 schema、旧 worker 兼容分支或修改 u1-upgrade owner，立即停并记录 Semantic Amendment/Ownership Conflict。

## 可追溯性

| 设计验收/决策 | 任务 | 直接验证 |
|---|---|---|
| 全局/webhook 通知上限、UTF-8 字节 cap、session preview、generic 契约不变 | T01 | 三个固定通知测试、配置 reload、Web 设置、临时长消息 |
| literal/pattern、终态/权限、DB/文件/会话全量脱敏、不回显 | T02 | 四个固定 redact 测试、DB 全表/result_dir 扫描、HTTP/CLI 响应 |
| 删除关联记录、保留审计、`--yes`/二次确认、Web 权限 | T03 | delete 测试、HTTP/CLI/Web、删除后 show/list/DB 证据 |
| 常见秘密形态只警告、位置提示、可关闭、不拦截 | T04 | `TestSubmitSecretWarning`、help/README/skill |
| 隔离真实验收、质量门、无残留 | T05 | build/vet/test/gofmt/web/控制字符/临时 serve 原始输出 |

## 完成 Gate 与剩余工作

V1–V2c 的固定测试先红后绿、受影响包与全量质量门、Web 构建、临时 serve 真实验收、G032/G045 清单、每功能点 commit 和最终 `git status --short` 闭合后，才记录本计划实施完成。远程 worker 副本、已发通知和外部日志的清理由设计明确排除，需另行授权；任何未跑、未通过或环境缺口按原始输出报告。
