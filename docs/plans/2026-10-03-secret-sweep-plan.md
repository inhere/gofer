<!-- template_id: plan; template_version: 1.2.0 -->
# 跨 job 秘密查找与批量脱敏（Y 批）实施计划

> 状态：Draft 0.1 / 用户已在 2026-10-03 当前请求中授权提交后连续实施

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-03 | Codex | 将已批准的 Y1–Y3 设计拆成可执行波次、文件所有权和验收证据 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 规划可靠性声明

- `thinking_mode=RIGOROUS`
- `core_objective`：在不回显秘密的前提下，跨终态 job 查找同一字面量/RE2 模式，并可按扫描结果批量复用既有脱敏实现；同时覆盖提交标题和标签的秘密提示。
- `allowed_scope`：`tools/gofer` 独立仓库内的 jobstore、HTTP client/server、CLI、提交提示、README 与 `skills/gofer-usage/`；临时 serve 验收使用临时配置、随机端口和临时目录。
- `non_goals`：远程 worker 副本、已发通知、外部日志、定期扫描、正式 server/config、web/dist、协议语义以外的兼容层。
- `expansion_policy=DEFER_OR_REQUEST`
- `review_budget`：低暴露度本地工具变更一轮合并轴复核；每个功能点先失败测试、后实现、focused 验证，再单独 commit。
- `stop_conditions`：发现 schema/权限/外部协议或生命周期语义变化、命中他人 dirty work、秘密值可能进入 argv/日志/响应、或临时 serve 无法证明数据库字节级清除时停止并报告。

## 目标与完成定义

完成 Y1–Y3：

1. `gofer job secret-scan --literal-from-stdin [--pattern <RE2>] [-p <project>] [--since <dur>]` 只从 stdin 接收原文 literal，扫描与 `RedactJob` 完全相同的 job DB 文本列、job comments 和 `result_dir` 文本文件；仅输出 job id、经掩码的命中标题、位置及计数，不输出原文；运行中 job 只列出并提示不能脱敏。
2. `--redact --yes` 逐个调用既有 `RedactJob`，每个 job 产生一条 `job.redacted`，最后只做一次 WAL 截断；`--vacuum` 额外执行一次明确提示有锁影响的 VACUUM。管理员可全局扫描，普通调用者只看/改自己拥有的 job。
3. 提交提示覆盖 `title` 与 `tags`，保持不回显、不阻止提交的既有行为；同步 CLI help、README 和 gofer-usage skill。

完成证据包括固定测试五项、`gofmt -l` 无输出、Windows/Linux build、`go vet`、`go test ./... -count=1` 原文，以及临时 serve 三 job + 一无关 job 的真实验收和数据库文件字节级无残留检查。

## 范围、排除项与授权

- **范围**：复用 `internal/jobstore/redact.go` 的列发现、文本文件处理和 `purgeWAL`；把扫描和脱敏共用的“该 job 的文本位置”收敛到同一 owner；新增 `/v1/jobs/secret-scan`、CLI `job secret-scan`、client 请求封装；扩展提交提示。
- **排除**：不修改正式配置目录 `D:\work\inhere\config\win-env\gofer`，不重启/reload 现有 server/worker，不扫描 `CLAUDE*` 文件，不产生 `web/dist`，不做 push、release、deploy、migration 或硬件动作。
- **权限**：扫描/脱敏请求沿用现有管理员与 owner 鉴权；真实秘密只通过 stdin 进入进程，不进入 argv、审计 detail、CLI 输出或 HTTP 响应。
- `host_or_non_offline_action=NOT_APPLICABLE`

## 输入与批准证据

- 设计：[docs/design/2026-10-03-secret-sweep-design.md](../design/2026-10-03-secret-sweep-design.md)，状态 Approved（用户 2026-10-03 web 中继确认）。
- 现状基线：Git root `D:\work\inhere\hyy-ai-inspect\tools\gofer`，branch `main`，HEAD 为合并 `docs-y` 后的 `2c4f53f344be4d47ac139ef767fa0757b009b8b9`；合并前工作树无 dirty/untracked，合并只加入设计与参考文档。
- 当前请求明确要求“写实施计划候选，提交后直接连续实施”，作为本计划执行授权；仍不包含 push 或正式环境动作。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 按 job 枚举可扫描文本位置 | `internal/jobstore/redact.go`、`jobs`/`comments` schema、result_dir walker | 复用 `RedactJob` 的动态 TEXT 列发现、`jobRowBelongs`、`redactResultDir` 和 `purgeWAL` | 在 jobstore 内提取只读枚举/匹配回调，RedactJob 与 ScanJobs 共用 | OWNER_EXTENSION | 当前实现只改写一个 job 且不返回位置/标题，无法全局只读扫描 | 第二套列清单会导致扫描与脱敏漂移；生命周期跟随 jobstore |
| CAP-02 | HTTP 管理员/owner 批量扫描与权限过滤 | `internal/httpapi/job_redact_handler.go`、job credential middleware、`internal/client/job_redact.go` | 复用现有 bearer、owner/admin gate、JSON 错误与 client `doJSON` | 新增 jobstore scan DTO、HTTP handler、client 方法 | THIN_ADAPTER | 现有 `/redact` 只接受单 job id | 新 route 与 CLI help/skill 必须同批维护 |
| CAP-03 | CLI stdin、过滤、批量脱敏和确认门槛 | `internal/commands/job.go` 的 `redact` 命令和 client API | 复用 `literal-from-stdin`、pattern 校验、`job` 命令连接 flags | 增加 `secret-scan` 子命令与扫描结果渲染/`--yes`/`--vacuum` | THIN_ADAPTER | 现有命令没有跨 job 结果集或一次性 WAL/VACUUM 控制 | 不新增顶级命令；兼容分支必须有 G032 标记（本计划不保留旧别名） |
| CAP-04 | 提交提示覆盖 title/tags 且不回显 | `internal/secret.ScanSubmission`、`warnJobRunSecrets`、JobRequest | 复用现有位置提示和不阻止提交策略 | 扩展 scanner 输入并更新失败测试 | OWNER_EXTENSION | 当前 scanner 只接受 command/args/prompt | 不能把 title/tags 原文带进 warning |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 独立 secret-scan 包/第二列清单 | CAP-01 | 与 `RedactJob` 共用枚举即可通过跨 job、comments、文件验收；独立包会产生列清单漂移，删除该候选 |

## 前置检查与 fail-closed 条件

1. 在 `tools/gofer` 执行 `git status --short`，确认只包含本阶段 owner；所有测试使用 `t.TempDir()`。
2. 每个 mutation stage 前重新执行 IDEV-STD `resolve --response verify`，指纹或 scope 变化则停。
3. 运行 `go test` 前确认测试数据库、结果目录和 server config 均位于临时目录；每条 gofer 命令显式 `--server http://127.0.0.1:<随机端口>` 或 `-c <临时配置>`。
4. 输入 literal 必须来自 stdin 且非空；pattern 由 RE2 编译，任何无效 pattern、非终态脱敏、权限不足、未知 job 或扫描数据读取错误都返回非零且不输出秘密。
5. 扫描不得读取/输出 `CLAUDE*` 文件；二进制文件只计 skipped，不读取其内容。

## 波次与依赖

| 波次 | 内容 | 依赖 | 交付 |
|---|---|---|---|
| W0 | 失败测试与扫描 DTO/共享枚举契约 | 设计、当前 schema | `test:` commit |
| W1 | jobstore 全局扫描、权限/时间/项目过滤、批量脱敏与 WAL/VACUUM 控制 | W0 | `feat(jobstore):` commit |
| W2 | HTTP route 与 client 封装、owner/admin scope | W1 | `feat(http):` commit |
| W3 | CLI secret-scan、提交确认和输出；标题/标签 warning | W2 | `feat(cli):` commit |
| W4 | README/skill 同步、全量质量门和临时 serve 真实验收 | W3 | `docs:` 或随功能点 commit；最终证据 |

## 任务

### Y1.1 扫描失败测试与共享文本位置枚举

- 文件: `internal/jobstore/secret_scan_test.go`、`internal/jobstore/redact.go`、必要时 `internal/jobstore/jobs.go`/`comments.go`
- 动作: 先写 `TestSecretScanFindsAcrossJobs`、`TestSecretScanNeverEchoes`、`TestSecretScanOwnerScope` 的失败断言；提取扫描与脱敏共用的 DB TEXT 列、job 归属、comments、result_dir 文件枚举；返回只含位置、计数、掩码标题和运行中提示的中立 DTO。
- 验证: `go test ./internal/jobstore -run 'TestSecretScan(FindsAcrossJobs|NeverEchoes|OwnerScope)$' -count=1`
- 完成标准: 测试先红后绿；同一列/文件枚举被 Scan 与 Redact 调用；任何结果字符串不含 literal。
- 依赖: W0，无。

### Y1.2 HTTP 只读扫描契约

- 文件: `internal/httpapi/secret_scan_handler.go`、`internal/httpapi/server.go`、`internal/httpapi/job_credential.go`、`internal/client/job_secret_scan.go`、`internal/httpapi/secret_scan_test.go`
- 动作: 先写 HTTP owner/admin、project/since、literal/pattern 请求测试；实现 `POST /v1/jobs/secret-scan`，响应只含聚合命中，不回显原文；运行中 job 只列出不可脱敏状态。
- 验证: `go test ./internal/httpapi ./internal/client -run 'SecretScan|secret.scan' -count=1`
- 完成标准: 非 owner/admin 被拒；管理员可跨项目；owner 只能自己的 job；HTTP body/日志无 literal。
- 依赖: Y1.1。

### Y2.1 批量脱敏与 WAL/VACUUM

- 文件: `internal/jobstore/secret_scan.go`、`internal/jobstore/redact.go`、`internal/jobstore/secret_scan_test.go`、`internal/httpapi/secret_scan_handler.go`
- 动作: 先写 `TestSecretScanRedactAll`；扫描结果按 job 顺序逐个调用既有 `RedactJob`，保持每 job 一条 `job.redacted`；默认最后一次 `purgeWAL`，`--vacuum` 只在显式请求时执行一次并返回耗时/锁提示；失败 job 明确报告且不泄漏输入。
- 验证: `go test ./internal/jobstore ./internal/httpapi -run 'TestSecretScanRedactAll|SecretScanRedact' -count=1`
- 完成标准: 3 个 job 的命令/输出/评论秘密全部清除，重复 scan 为零；WAL 截断只发生一次；VACUUM 不默认执行。
- 依赖: Y1.2；共享枚举和既有 RedactJob。

### Y3.1 CLI 命令与提交提示

- 文件: `internal/commands/job.go`、`internal/commands/job_secret_scan_test.go`、`internal/secret/redact.go`、`internal/secret/redact_test.go`
- 动作: 先写 CLI 失败测试；新增 `job secret-scan`（stdin literal、重复 pattern、project/since、`--redact --yes`、`--vacuum`），沿用 `bindConfigFlag`/`bindServerFlags`；扩展 `ScanSubmission` 输入 title/tags，只显示位置不显示值。
- 验证: `go test ./internal/commands ./internal/secret -run 'SecretScan|Submission.*Title|Title.*Tags' -count=1`; `gofer job secret-scan --help`（使用临时 config/server 参数）。
- 完成标准: 未加 `--yes` 不执行脱敏；`--redact` 复用 HTTP/服务端实现；标题/tag 秘密触发位置提示且 warning 无原文。
- 依赖: Y2.1。

### Y3.2 用户文档同步

- 文件: `README.md`、`skills/gofer-usage/SKILL.md`、必要时 `skills/gofer-usage/references/commands.md`
- 动作: 以代码和 `--help` 为准补充 secret-scan、stdin、owner/admin、运行中 job、`--redact --yes`、`--vacuum` 和不扫描 CLAUDE* 的边界；删除被推翻的旧说法。
- 验证: `rg -n "secret-scan|literal-from-stdin|vacuum|CLAUDE" README.md skills/gofer-usage`; 控制字符扫描。
- 完成标准: G045 同批同步，文档不声称尚未实现的行为。
- 依赖: Y3.1。

### V4 质量门与真实验收

- 文件: `tmp/` 下临时 config、serve 日志和验收记录（不入提交）
- 动作: 构建 Windows/Linux、vet、全量测试；启动临时 serve（随机端口、临时 GOFER_CONFIG_DIR），创建 3 个终态 job（命令/输出/评论各含同一假秘密）和 1 个无关 job，执行 scan→`--redact --yes`→scan；剔除 `CLAUDE*`；检查主 DB、`-wal`、`-shm` 和结果文件字节级无秘密残留。
- 验证: `gofmt -l`; `go build ./...`; `GOOS=linux GOARCH=amd64 go build ./...`; `go vet ./...`; `go test ./... -count=1`; 临时 serve 命令逐条显式 `--server` 或 `-c`。
- 完成标准: 记录每条命令 exit code 与原始 PASS/FAIL 输出；数据库字节扫描无残留；git status 仅允许计划/功能点提交后的 clean 状态。
- 依赖: Y3.2。

## 回滚与恢复

每个功能点一个 conventional commit；发现失败时只回退最后一个本地功能点 commit，不使用 reset --hard，不触碰合并的 `docs-y` 设计文档。临时 serve 直接停止并删除临时目录；正式 server、worker 和真实配置不在回滚范围。恢复点记录在 `tmp/`，包含 HEAD、首个失败命令、已过测试和下一步命令。

## 人工 Gate

- 设计批准已由用户在 2026-10-03 确认。
- 本请求同时授权：提交本计划候选后按波次连续实施，并创建本地 atomic commit。
- 不包含 push；若未来需要 push、release、deploy、migration、外部消息或真实 worker/server 动作，必须另行明确批准。

## 可追溯性

| 设计验收 | 计划任务 | 验证 |
|---|---|---|
| Y1 跨 job 只读扫描、无回显、运行中提示 | Y1.1、Y1.2、Y3.1 | jobstore/httpapi/CLI focused tests，临时 serve scan |
| Y2 批量复用 redact、审计、WAL/VACUUM | Y2.1、V4 | `TestSecretScanRedactAll`、DB/WAL 字节扫描、临时 serve |
| Y3 title/tags 提交提示 | Y3.1、Y3.2 | secret scanner focused tests、CLI help、skill/README grep |
| 安全与边界（owner/admin、stdin、CLAUDE* 排除） | Y1.2、Y3.1、Y3.2、V4 | owner scope tests、控制字符扫描、真实验收 |

## 完成 Gate 与剩余工作

完成必须同时满足：所有固定测试和全量质量门有原始输出；临时 serve 验收通过且数据库字节无残留；G032 兼容清单为空或列出带 `DEPRECATED(vX): remove in vY` 的保留路径；G045 skill/README 已同步；每个功能点 commit 已核对 `git diff --cached --name-only`；不 push。若任一项失败，保留已完成 commit，并报告具体 blocker、未完成任务和需要人工决策点。
