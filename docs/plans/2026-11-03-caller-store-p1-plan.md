<!-- template_id: plan; template_version: 1.3.0 -->
# 配置入库 P1：用户入库 实施计划

> 状态：Draft 0.4 / 已批准（用户 2026-11-03：“批准 P1 计划 0.4 并开始实施”）
> issue：gofer-nhh7

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-03 | Claude（gofer 监督会话） | 初稿 |
| 0.2 | 2026-11-03 | Claude（gofer 监督会话） | 按两路审核修订（5 阻塞、15 纠正）：同 id 接管时停用 DB 行并标孤儿；隐藏行的 owner 按停用拒绝；合并时跑完整字段校验，名字冲突清空名字；拒绝空上限；回退风险写入发版说明并实测；修正测试名、清理路径和任务细节 |
| 0.3 | 2026-11-03 | Claude（gofer 监督会话） | 按确认轮修订（1 阻塞、6 纠正）：启用被接管的行要先轮换，并清除接管标记；用户写入后按当前行重新合并；接管事务放在校验之后；事务内标孤儿；standalone mcp 只读；重判和继承也判封锁；丢弃的行不进封锁集合 |
| 0.4 | 2026-11-03 | Claude（gofer 监督会话） | 按确认轮的 2 条纠正修订（确认轮无阻塞项，这两条未再复核）：启动和 standalone mcp 在开库前查标记；接管和轮换的时间戳保证先后 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：新增用户、发 token、轮换、停用、改上限都不用改文件，不用重启。Scope freeze：只做设计 Draft 0.10 划给 P1 的部分和下文“对设计的细化”。Review budget：一轮审核加一轮确认，有阻塞项才继续，上限按严谨规划核心合同。停止条件：确认轮无 `CORE_BLOCKING`，停在计划 Gate。

## 目标与完成定义

目标：根管理员在 web 或 CLI 上新增用户，拿到一次性 token，用户立即可用；可轮换、停用、启用、改上限和配额。配置文件不变，不用重启。

完成定义：本计划 T1–T10 通过；设计“结论”里属于 P1 的验收都有真实环境证据；`gofer-verify` 全绿；文档按 G045、G056、G058 同步。

## 范围、排除项与授权

范围：`auth_callers`、`config_audit` 两张表；用户合并进配置快照；鉴权支持哈希；停用语义；`/v1/callers*`；`gofer auth caller`；离线修复 `auth caller repair --offline`；web 用户管理页；见到 `config_store` 标记拒绝启动。

排除（P2、P3）：`config_entries`、`config_store_id`、`ApplyEntries`、`SplitForStore`、`StagePending`、`config.exec.write`、`init` 改造、`config migrate`、`config export`、`config doctor`、`config entry rm`、`config store rebuild`、只读开库与升级预检、`config validate` 调 server、saveConfig 写文件的临时文件加 rename 改造、非 serve 的 `config.Load` 改造、`project add` 改 API、roles、`can_submit` 表单、高危配置页、worker token 哈希。

授权边界：批准本计划不等于授权执行，执行需要单独的当前请求。本地提交按既有默认授权；push 和发版不在本计划内。真实验收只用临时 server，不改用户主机配置和线上 server。

- `host_or_non_offline_action=REQUIRED`

需要的主机动作只有 T10：经 `gofer job --runner local` 起临时 server（当前构建和 v0.209.3 各一次），产物只写 `tmp/`。不消耗模型 token。

## 输入与批准证据

- 设计：`docs/design/2026-11-03-config-into-db-design.md` Draft 0.10。用户 2026-11-03：“配置接受 0.10，先做 P1”。
- 代码核对清单：工作区 `tmp/gofer-tasks/config-p1/facts.md`（基线 `6287fd73`，不入库，下文写明结论）。

## 对设计的细化

代码核对发现设计没定或说法不准的地方。下面按最小方案定下，批准本计划即确认。**标“方向”的 4 条会影响用户看到的结果，提请单独留意。**

| # | 问题 | 决定 |
|---|---|---|
| D1（方向） | trigger 的 owner 指向 DB 用户时，开库前的文件校验就失败（`config/triggers.go:252`、`config/loader.go:175`） | P1 限定 trigger owner 只能是文件用户，错误文案写明原因。P2 用 `refs` 开关解除。今天 trigger 只能写文件，改错 reload 就会报错，不会静默生效 |
| D2（方向） | 重判停用会打到重启后恢复的常驻会话（`job/session_recovery.go:130` 不经准入） | 恢复的会话算“在跑的 job”，不重判，继续运行直到结束或被人取消。它派的子 job 走准入，会被拒 |
| D3（方向） | 设计说“停用用户的 job 跳过重试”，但工作流 job 不走 `maybeRetryJob`（`job/execute.go:968-971`） | 不为工作流加逻辑：下一步 `Submit` 被准入拒绝，工作流失败。会多跑一轮退避，能终止。这是对设计说法的收窄 |
| D4 | `Core.Update` 无条件 Save；无配置文件的部署第一次 `caller add` 就会新建 config.yaml（`core/core.go:589,600-606`） | DB 用户的写入走新的 `Core.UpdateCallers`，不调 saveConfig。其他配置写入走 `fileProjection`，DB 用户不写回文件 |
| D5 | 设计既说 pid 文件不可靠，又用它确认 server 未运行 | `auth caller repair` 把 pid 检查作为尽力提示。安全性靠三点：不建库（先 stat，不跑 `Open`、不迁移）、打印实际库路径、修复只会把用户改得更严。server 其实在跑时，修复在下次 reload 或重启生效 |
| D6 | 重判点拿不到 `InternalExec`、`SystemJob`、`ExtraCeilingOwners`（`execute` 只收 `runner.Request`） | `execGates` 加 `recheckOwners []string`，在 `submit.go:1408` 由与准入相同的 helper 算好；恢复路径留空。系统 job 和放行的内部 exec 也留空 |
| D7 | 内部 exec 放行清单（设计留给计划） | 只放行 `delivery`、`delivery-check`（在跑 job 的收尾）。`relay`、`tracker-sync`、`summarizer`、`workflow-step` 都是新动作，拒绝 |
| D8 | `config_audit` 没有读入口，保留期没定 | 加 `GET /v1/callers/audit` 和 `gofer auth caller audit`，web 用户页显示。保留 90 天，同 `auth_audit`。清理放在 `PruneJobs` 的 `policy.IsZero()` 判断之前（否则没配 retention 的部署永远不清） |
| D9 | 设计 CLI 清单没有改配额的命令，而验收要求“改并发上限不用重启” | 加 `auth caller set-limits` 和 `PUT /v1/callers/{id}/limits` |
| D10（方向） | DB 用户与文件用户或 worker 同 id：设计只说隐藏，但隐藏后旧授权被新身份继承，文件条目删除后旧 token 复活，名下 schedule 变成无上限 | `RegisterWorkerInto` 拒绝与用户同 id。文件改出同 id 时不拒绝启动，按“身份更换”处理：首次发现就在合并的同一步把 DB 行标 `superseded_at` 并停用、把 `caller:<id>` 授权标孤儿、写审计（actor `system:file-takeover`）。任一步失败就算合并失败。之后删掉文件条目，旧 token 不复活：启用被接管的行要求 `rotated_at` 晚于 `superseded_at`，否则 409 要求先轮换；时间戳是秒级，写入时保证先后，不加列：接管取 `max(now, rotated_at)`，轮换取 `max(now, superseded_at+1)`。启用成功才清除 `superseded_at`，所以再次接管会再走一遍停用、标孤儿。孤儿授权用现有 activate 恢复。与 worker 同 id 时该 id 记入快照的“封锁 owner”集合，准入按停用拒绝。被文件用户接管时，名下 schedule 按文件用户的上限运行（根管理员亲手声明的身份，写进文档） |
| D11 | 标记拒绝放进 loader 会让 34 处 `config.Load` 全失败 | 只拦三处：`serve.Start`、standalone mcp、`reloadFromPathLocked`（reload 失败保留旧快照）。其他 CLI 不拦，服务管理命令还能用 |
| D12 | `set-ceiling` 改文件用户时，文件和审计没有原子性 | 先 `Core.Update` 写文件，再写审计。审计失败记 ERROR，响应带 warning，改动不回退。`server:` 块注释会丢，与今天所有 server 配置写入一样，CLI 和界面提示 |
| D13 | `callerCeilingWarnings` 不能简单挪到合并后（`config validate` 还要用） | 文件用户的告警保持原位。DB 用户的告警单独存一个切片，读时拼接，label 为 `db`，写入时重算 |
| D14 | `allow_empty_token` 且文件无根管理员时没人能管理用户 | 这类部署 P1 用不了用户管理，接口返回 403 并提示配置根管理员。判空规则仍按设计做 |
| D16 | `{"ceiling":{}}` 能过校验，写进文件后 `config.Load` 拒绝，server 起不来，repair 修不了 | `ceiling` 全部字段未设时返回 400，文件和 DB 用户都一样。`DecodeCallerCeiling` 同规则。不设上限只能用 `no_ceiling` |
| D17 | 合并后的用户集可能不被 `config.Validate` 接受（名字重复、非法 glob、负配额），之后所有写入都失败，连停用都做不了 | `ApplyStoredCallers` 对每行跑与 `validate` 相同的字段检查，违反就按坏行拒绝启动，repair 能修。名字和其他用户冲突时不拒绝：清空该 DB 行的名字并告警（`Name` 允许为空）。合并后再跑一次 `config.Validate`，失败同样退出 |
| D18（方向） | 回退 v0.209.3 后，DB 用户名下的 schedule 继续触发且没有上限（旧版本查不到用户就跳过上限，已发布改不了） | 发版说明写明：回退前先取消 DB 用户名下的 schedule、计划和排队 job，回退后这些自动化没有上限。T10 实测记录 |
| D15 | 细节 | token 为 `gct_` + 32 字节随机数 base64url；`token_hint` 为 `gct_…` + 末 4 位；id 用 worker id 同款规则 `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`，保留 `default`、`system`、`root` 和前缀 `job-`（job 凭据的 CallerID 是 job id）；`auth_callers` 加 `updated_at`；升级回滚靠现有 `waitManagedHealthy` 和回滚，预检留给 P2 |

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 用户持久化 | `internal/settings`、`auth_grants`、`job_tokens` | 否 | jobstore 加表，写法照 `auth.go`、`previews.go` | OWNER_EXTENSION | settings 只放有注册表的标量 | 无新包 |
| CAP-02 | 串行写入和发布 | `Core.Update`、`updateMu`、`reloadLocked` | 是 | 加 `UpdateCallers`（不 Save） | OWNER_EXTENSION | `Update` 必写文件 | 复用锁和广播 |
| CAP-03 | 鉴权 | `buildCallers`、`lookupCallerIn` | 是 | `callerEntry` 加哈希 | OWNER_EXTENSION | none | 不新建鉴权路径 |
| CAP-04 | 权限 | `authz.PermAuthManage`、`routeDecls`、`grantAdminOnly` | 是 | none | DIRECT_REUSE | none | 不加新权限 |
| CAP-05 | web 页 | `settings/Permissions.vue`、`SettingsLayout` | 是 | 新页挂在 settings | OWNER_EXTENSION | Permissions 管授权且已 695 行 | 无新框架 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 独立 `internal/callers` 包 | CAP-02 | 逻辑都依附 `updateMu` 和快照，放 `internal/core/callers.go` 足够；删掉这个包不损失能力 |

## 前置检查与 fail-closed 条件

基线（2026-11-03）：Git root `inhere-tools/gofer`，分支 `main`，HEAD 为写本计划的提交；工作树只有本会话的 `.gofer/tracker/issues.jsonl`。开 worktree 前先提交 tracker。

依赖：R1（`gofer-rt3z`）正在实施，和本计划都改 `internal/job/execute.go`、`internal/job/session_recovery.go`、`internal/job/model.go`、`internal/config/model.go`、`internal/serve/serve.go`。**本计划在 R1 合并后开工**，开工时按新 HEAD 重新核对下文行号。

Fail-closed：
- `TestOpenUpgradesPreviousReleaseSchema` 红时不继续。
- 发现需要改 worker 协议，或要把排除项拉进来，属于 `Semantic Amendment`，停下回到计划 Gate。
- 真实环境不可用时，T10 报 `UNVERIFIED_REAL_ENV`，不算完成。

派活：`-a suga --runner local --cwd inhere-tools/gofer --worktree --model openai-codex/gpt-6.1-terra --strict-timeout --timeout 5400 --notify`。每批一个 job，合并后再派下一批。我审查 diff、接线和反向验证，不以汇报为准。

## 波次与依赖

| 波次 | 任务 | 依赖 | 说明 |
|---|---|---|---|
| W1 | T1、T2、T3 | R1 合并 | 地基：DB 无用户时行为和今天一致（T3 的标记拒绝和 `server.callers` 改分类除外） |
| W2 | T4、T5、T6、T7 | W1 | 鉴权、停用、API、CLI |
| W3 | T8、T9 | W2 | web、文档 |
| W4 | T10 | W3 | 真实环境验收 |

## 任务

### T1 存储

- 文件: `internal/jobstore/callers.go`（新）、`internal/jobstore/auth.go`、`internal/jobstore/prune.go`、`internal/jobstore/*_test.go`。
- 动作: 用 `init()` 追加 schema，不碰 `store.go`（G057）。`auth_callers(id PK, name, token_hash, token_hint, limits_json, ceiling_yaml, disabled_at, superseded_at, created_by, created_at, rotated_at, updated_at)`，`token_hash` 不设唯一约束。`config_audit(id PK AUTOINCREMENT, actor, path, before_masked, after_masked, at)` 加 `at` 索引。这批列是后续只读预检的契约，之后只增不改。`auth_callers` 另加 `superseded_at`（D10）。类型 `StoredCaller`（行的原样字段，`ceiling_yaml`、`limits_json` 不解析）。方法：`ListStoredCallers() ([]StoredCaller, error)`；`InsertCallerTx`、`UpdateCallerTx`（统一 `func (s *Store) WithCallerTx(fn func(tx CallerTx) error) error`，写审计同事务，用 `writeMu` + `Begin`，照 `auth.go`）；`CallerTx` 提供 `ListStoredCallers`、`OrphanGrants(ids)`：把 `OrphanAuthGrants` 的事务体抽成 `orphanAuthGrantsTx`，原方法调用它，不在事务里再取 `writeMu`；`InsertConfigAudit`（给 D12 用）；`ListConfigAudit(prefix string, limit int)`；`PruneJobs` 在 `policy.IsZero()` 判断之前清 90 天前的 `config_audit`（D8）。`OpenExistingForRepair(path string) (*Store, error)`：先 stat，不存在报错，不建库、不跑 `applySchema`、不跑 `migrate`；`auth_callers` 表不存在也报错。`RepairCaller(id, actor string, opts RepairOptions) (RepairResult, error)`，`RepairOptions{ValidCeiling func(string) error; ValidLimits func(string) error; ValidName func(string) error; LockedCeilingYAML string}` 由调用方传入，jobstore 不依赖 config 的新符号。只做这些修复：停用；坏的 `ceiling_yaml` 换成 `LockedCeilingYAML`；坏的 `limits_json` 清空；坏的 name 清空；不删行。
- 验证: 在 `inhere-tools/gofer` 运行 `go test ./internal/jobstore -run 'Caller|ConfigAudit|OpenUpgrades|OpenExistingForRepair'`。
- 完成标准: 旧库能升级；`OpenExistingForRepair` 对不存在的路径不产生文件；审计和用户写入同事务（中途失败两边都没有）；超期审计被清（含零 retention 策略的用例）。
- 依赖: 无。

### T2 配置模型与合并

- 文件: `internal/config/model.go`、`internal/config/callers_store.go`（新）、`internal/config/ceiling.go`、`internal/config/writer.go`、`internal/config/include.go`、`internal/config/triggers.go`、`internal/config/worker_register.go`、`internal/config/editable.go`、`internal/core/reload_report_test.go`、`internal/core/reload_diff_test.go`、`internal/core/core.go:931-968`（只为 restart 列表存在的分支）、测试。
- 动作:
  - `CallerConfig` 加 `yaml:"-"` 字段：`Source`（零值为文件，常量 `CallerSourceDB`）、`TokenHash`、`TokenHint`、`Disabled bool`，零值保持现有夹具不用改。
  - `ApplyStoredCallers(cfg *Config, rows []StoredCallerInput) (MergeResult, error)`：只追加，不改文件用户。`StoredCallerInput` 是 config 自己的类型。非法行（启用行 hash 不是 64 位十六进制；id 非法或保留；ceiling 严格解码失败、为空或 glob 非法；limits 解码失败或为负；name 超长或含控制字符）返回错误，错误带用户 id（D17）。已停用且 id 非法或保留的行丢弃并告警，不进封锁集合（这种行只能来自手改库，不可能有名下任务；保留 id 如 `default` 进集合会封住真实身份）；启用这种行时重新合并会报错，事务回滚。停用行不校验 hash。名字与其他用户重复：清空名字并告警。与文件用户或 worker 同 id 的行不进 `Callers`，返回在 `MergeResult.Superseded`，由 T3 处理；与 worker 同 id 的进封锁集合（D10）。封锁集合放 `Config` 未导出字段，`Clone` 复制。访问器 `OwnerBlocked(id)`：id 对应停用用户或在封锁集合里都返回 true，准入、重判、继承三处共用。
  - `DecodeCallerCeiling`（严格解码）、`LockedCallerCeiling()`（repair 用：`projects`、`runners`、`workers`、`agents` 各放保留 glob `__gofer_locked_no_match__`，`read_only: true`，`allow_exec`、`global_memory` 显式 false）、`ValidCallerID`。
  - `HasRootAdmin`（`model.go:2178`）只数文件来源。`HasRootAdmin` 和 `Derive` 只拿 `*ServerConfig`，所以来源放 `CallerConfig` 内。
  - `fileProjection(cfg)`：把 `Server.Callers` 滤成文件来源，结果为空时和 baseline 的 nil 或空保持一致。`Save`、`render`、`saveSplit` 先过 `fileProjection` 再进 `withoutRuntimeValues`（放在它之前，这样 `authored == nil` 分支也覆盖）；`CanonicalConfig` 不过滤，reload 差异仍能看到 DB 用户。
  - 告警按 D13。
  - `Config.HasStoreMarker()`：用 `configSources.byKey` 新访问器判断 `config_store` 顶层键（含 include 文件）；`SectionFile` 不能用，它对不存在的键返回主文件。
  - `validateTriggers` 对未声明的 owner 文案补一句“owner 只能是配置文件里的用户”（D1）。
  - `RegisterWorkerInto` 拒绝与 `Callers` 同 id（含停用）。
  - `server.callers` 从 `RestartRequired` 改为热载，改 `core.go` 对应分支和两份测试。
- 验证: `go test ./internal/config ./internal/core -run 'Caller|Store|Marker|HasRootAdmin|Projection|ReloadReport|TestReloadCeilingOnlyChangeIsHot|RegisterWorker'`。
- 完成标准: 有 DB 用户时 `Save` 的文件字节不变（断言字节相等，包括 Callers 原本 nil 和原本非空两种）；DB 用户的 `can_admin` 不可能为真；只有 DB 用户时 `HasRootAdmin` 按文件判定；重复 id 和重复名字不拒绝启动；有属性测试或表驱动用例断言任意合并结果都能过 `config.Validate`；坏 glob、负配额、坏名字的行都报错。
- 依赖: 无。

### T3 启动、reload 与写入事务

- 文件: `internal/core/core.go`、`internal/core/callers.go`（新）、`internal/serve/serve.go`、`internal/commands/mcp.go`、测试。
- 动作:
  - `core.WithStore(st *jobstore.Store)` 选项：`Build` 不再自己开库，store 仍由 Core 的 `Close` 关闭；`serve.Start` 在 `logLoadWarnings` 之前按 `core.go:329-333` 同样的路径开库、读用户、合并，失败就退出；`Build` 返回错误时由 `Start` 关 store。没传 `WithStore` 的调用方走原路径，不合并（worker 属于这类）。
  - `serve.Start` 和 standalone mcp 在开库之前查标记（否则会留下空库，或把 P2 的线上库迁移掉）；reload 在 `config.Load` 之后查，库已开着。合并 helper `mergeStoredCallers(cfg, store, mode) error`（可单测）。顺序固定：`ListStoredCallers`，失败就返回错误，绝不当作空集；`ApplyStoredCallers`（不写库）；`config.Validate`（D17）；最后才是接管事务：对 `Superseded` 里 `superseded_at` 为空的行，在一个事务内设 `superseded_at`、`disabled_at`，用 `CallerTx.OrphanGrants` 标 `caller:<id>` 授权孤儿，写审计（actor `system:file-takeover`），失败就返回错误（D10）。这样前面任一步失败都不会留下半截接管。`mode` 为 server（`serve.Start`、reload）才写接管事务；为只读（standalone mcp）时跳过，只在内存隐藏，不写库（设计：CLI 不直接写库），由下次 server 启动处理。
  - `reloadFromPathLocked`：`config.Load` 成功后（`core.go:705` 之后、overlay 之前）调用 helper，失败按 `:702-705` 同样返回，保留旧快照。不放进 `reloadLocked`。
  - 标记检查（D11）的文案：“这份配置已迁移到 DB，当前版本不支持，请恢复迁移前备份或升级。”
  - standalone mcp（`commands/mcp.go:135`）开库合并；不跑孤儿扫描。
  - `Core.UpdateCallers(fn func(next *config.Config, tx jobstore.CallerTx) error) (*ConfigSnapshot, error)`：持 `updateMu`，Clone，开事务，`fn` 写行，在同一事务读全部行，把 next 的 DB 用户和封锁集合按当前行重新合并（这样启用被隐藏的行立刻生效，非法行报错回滚），校验 next（`config.Validate` 加 `agent.ValidateConfig`，Core 不依赖 httpapi 的 `validateCandidate`），提交，`reloadLocked` 发布。全程不调 saveConfig。校验失败回滚。
- 验证: `go test ./internal/core ./internal/serve ./internal/commands -run 'WithStore|MergeStoredCallers|StoreMarker|UpdateCallers|Takeover'`。
- 完成标准: 读用户失败时启动退出、reload 保留旧快照且孤儿扫描不运行；标记在三处都拒绝，`gofer job ls` 这类客户端命令不受影响；无配置文件部署 `UpdateCallers` 后不产生文件；接管用例：文件新增同 id 后 DB 行停用、授权孤儿、审计一条，再 reload 不重复；删掉文件条目后旧 token 仍 401；校验失败时 DB 不变；轮换加启用后再次接管，再次停用并标孤儿（反向验证：启用不清 `superseded_at`，这条失败）；standalone mcp 路径不写库。现在没有测试调用 `serve.Start`，所以开库合并那段抽成函数单测（读失败退出、标记拒绝且不建库），真实启动由 T10 覆盖。反向验证：把 helper 改成读失败返回空集，用例要失败。
- 依赖: T1、T2。

### T4 鉴权

- 文件: `internal/httpapi/server.go`、`internal/httpapi/auth.go`、`internal/httpapi/tunnel_handler.go`、`internal/httpapi/authz_live_conn.go`、`internal/httpapi/authz_resolve.go`、`internal/httpapi/config_handler.go`、测试。
- 动作: `callerEntry` 加 `tokenHash`、`fingerprint`。`buildCallers` 把启用的 DB 用户放在文件用户、worker、旧 token 之后，停用的不进。`lookupCallerIn` 只算一次请求 token 的 sha256，文件用户比明文、DB 用户比哈希，都用恒定时间比较。`tokenFingerprint` 对 DB 用户取 `token_hash[:16]`（与现算法结果相同，加用例固定）。`serverSnapshot` 加 `disabledCallers int`；判空统一为一个 helper（`callerEntries` 为空且 `disabledCallers == 0`），四处共用（`auth.go:32`、`tunnel_handler.go:25`、`authz_live_conn.go:527-531`、`server.go:1104`）。`effective` 在根判断之后、派生权限之前，对停用用户返回空。`callerConfigView` 加 `source`、`disabled`、`token_hint`；`TokenSet` 含哈希。
- 验证: `go test ./internal/httpapi -run 'CallerEntry|LookupCaller|EmptyAuth|TokenFingerprint|Effective.*Disabled|LiveConn'`。
- 完成标准: DB 用户能认证且 ceiling、配额、限流生效；停用后 401；所有 DB 用户都停用且 `allow_empty_token` 时不回到匿名（反向验证：判空改回 `len(callerEntries)==0`，用例失败）；长连接巡检不误断 DB 用户（反向验证：指纹改错，用例失败）；停用和轮换后长连接断开；孤儿不产生。
- 依赖: T3。

### T5 停用语义与配额

- 文件: `internal/job/ceiling.go`、`internal/job/execute.go`、`internal/job/fallback.go`、`internal/job/submit.go`、`internal/job/session_recovery.go`、`internal/job/model.go`、`internal/job/concurrency.go`、测试。
- 动作: `admitCeiling` 在 `CallerCeiling` 取值之前用 `OwnerBlocked` 判断，返回包住 `ErrCeilingDenied` 的错误（内部 exec 放行 D7）。helper `disabledRecheckOwners(req) []string` 复用准入的 owner 计算，系统 job 和放行的内部 exec 返回空。`execGates.recheckOwners` 在 `submit.go:1408` 赋值，恢复路径留空（D2）。`execute.go` 在目录锁块结束后（`:202` 和 `:204` 之间）用当前快照的 `OwnerBlocked` 重判，命中就 `releaseDir` 后 `finish(StatusFailed)`，错误用固定文本的哨兵，`failureDecision`（`fallback.go`）照 budget 的先例识别成新的 `FailureClassCallerDisabled`（`finish` 里 `execute.go:776` 会覆盖事先设置的类别，不能提前设）；`maybeRetryJob` 对它不重试。`inheritCeilingOwner` 用 `OwnerBlocked` 判断。`callerSemaphore` 取用时比较容量，不同就换新 channel，在途 job 释放旧 channel。
- 验证: `go test ./internal/job -run 'Disabled|CallerSemaphore|InheritCeilingOwner'`。
- 完成标准: 停用后 trigger、schedule、计划派发、leader、@ 触发、resume、接管、hold 放行都起不来（至少 schedule、计划派发、hold 放行各一条经真实入口的用例，其他入口列出经 `admitCeiling` 的证据）；排队中的 job 拿到槽位后失败且不重试；恢复的常驻会话继续；`delivery` 放行、`workflow-step` 被拒；worker 同 id 隐藏后 schedule 触发被拒；改并发上限后新 job 按新值排队。反向验证两条：去掉 `CallerCeiling` 取值前的停用判断，无 ceiling 用户用例失败；去掉容量比较，改上限用例失败。
- 依赖: T3。

### T6 用户管理 API

- 文件: `internal/core/callers.go`、`internal/httpapi/callers_handler.go`（新）、`internal/httpapi/server.go`（路由、接口注入）、`internal/httpapi/authz_routes.go`、`internal/serve/serve.go`（注入）、`docs/reference/permissions.md`（生成）、测试。
- 动作: Core 方法 `AddCaller`、`RotateCaller`、`DisableCaller`、`EnableCaller`、`SetCallerCeiling`、`SetCallerLimits`、`ListCallers`、`ListCallerAudit`；handler 只绑定和转发（G021–G024）。路由都用 `Require(authz.PermAuthManage)`、`NoProject()`，登记 `routeDecls`（G056）；每个 handler 先调 `grantAdminOnly`（门禁放过非 user 凭据，不加的话 job 凭据能读 GET）：
  - `GET /v1/callers`、`POST /v1/callers`（body：`id,name,max_concurrent_jobs,rate_limit,rate_burst,ceiling,no_ceiling`；`ceiling` 和 `no_ceiling` 必须二选一，`ceiling` 全部字段未设时 400（D16）；返回 `caller, token, warnings`）
  - `POST /v1/callers/{id}/rotate`（返回 `token`）、`/disable`、`/enable`
  - `PUT /v1/callers/{id}/ceiling`、`/limits`
  - `GET /v1/callers/audit?limit=`
  文件来源用户：`rotate`、`disable`、`enable` 返回 409 并提示；`ceiling`、`limits` 走 `Core.Update` 写文件并按 D12 写审计，mut 内跑 `config.Validate` 加 `agent.ValidateConfig` 和 D16 的规则。列表显示被接管的行（状态 `superseded`）；启用时 id 仍和文件用户或 worker 冲突就 409；被接管过的行要求已轮换（D10）；启用清除 `superseded_at`。`no_ceiling` 且未开 `require_admin_capability` 时，`warnings` 提示无上限用户能写 `agents.*.command`；`warnings` 同时带该用户的 `callerCeilingWarnings`（如本机 runner 不是安全边界）。token 用 `crypto/rand`，不进日志、审计、错误。审计前后值只有 name、limits、ceiling、disabled、token_hint。启用时 hash 非法要求先轮换。轮换允许对停用用户做，不改变停用状态。id 不复用：`auth_callers`、文件用户、worker 任一已有就 409（含停用，否则能给根管理员 id 再发一个 token）。
- 验证: `go test ./internal/httpapi ./internal/core -run 'Callers|CallerAudit|TestAuthzReferenceIsUpToDate|TestEveryRouteIsDeclaredOrPending'`。
- 完成标准: 非根调用方、job 凭据（GET 和审计路由也断言 403）、worker 凭据、带 ceiling 的调用方都被拒；空上限 400；未轮换就启用被接管的行 409；经 API 删除 worker 后轮换加启用，token 立刻可用（不等 reload）；新用户不重启可用；响应之外查不到 token（DB、日志、审计，用例扫描）；并发 add 同 id 只有一个成功；`TestEveryRouteIsDeclaredOrPending` 通过。SR1301 五项在汇报里逐项写。
- 依赖: T4、T5。

### T7 CLI 与离线修复

- 文件: `internal/commands/auth.go`、`internal/commands/auth_caller.go`（新）、`internal/client/auth.go`（或新 `callers.go`）、`internal/commands/flagreset_test.go`、`internal/commands/testdata/cli-help-zh-CN.rest.golden`、i18n、`internal/httpapi/auth_handler.go`、`internal/httpapi/session_owner_handler.go`、`internal/commands/session.go` 里指向 `server.callers` 的文案、测试。
- 动作: `gofer auth caller add|ls|rotate|disable|enable|set-ceiling|set-limits|audit`，瘦客户端（G033）。`add`、`set-ceiling` 要求 `--ceiling-project`、`--ceiling-runner`、`--ceiling-worker`、`--ceiling-agent`（可重复）、`--ceiling-allow-exec`、`--ceiling-global-memory`、`--ceiling-read-only` 或 `--no-ceiling`，两者都没有就报错；帮助写明“不给 `--ceiling-runner` 时不含本机”。`auth caller repair <id> --offline`：解析配置找库路径，打印路径，pid 检查（活着就拒绝，加 `--force` 也只是提示放行，D5），`OpenExistingForRepair`，打印修复前后（不含哈希），actor `offline:<用户>`。没有 `--offline` 就报错。错误文案不提 P2 才有的命令。
- 验证: `go test ./internal/commands -run 'AuthCaller|TestFlagGlobalsRegistryComplete|TestZhRestCliHelpIsUnchanged|TestRestCommandGroupsAreCovered'`，至少一条经 `app.Run` 分发（GR110）。
- 完成标准: 没带上限参数时 `add` 失败；token 只打印一次；repair 能把坏行修到 server 可以启动；repair 指向不存在的库不产生文件。
- 依赖: T6。

### T8 web 用户管理

- 文件: `web/src/views/settings/Callers.vue`（新）、`web/src/api/callers.ts`（新）、`web/src/router.routes.ts`、`web/src/views/settings/SettingsLayout.vue`、`web/src/i18n/pages/settings.ts`、`web/src/views/settings/Permissions.vue`（datalist 接用户列表）、`web/src/views/Config.vue`（只读表显示来源、停用）、`web/src/api/types.ts`、`internal/httpapi` 的页面门控（如需要）、结构性测试（`settingsLayout.test.ts`、`settingsPagesI18n.test.ts`、`settingsTablesMobile.test.ts`、`pageWidth.test.ts`、`noHardcodedCjk.test.ts`、`i18nKeyUsage.test.ts`）。
- 动作: 列表（来源、状态、token 提示、上限摘要）、新增（必须选上限且至少填一项，或明确不设）、轮换、停用、启用、改上限、改配额、审计列表。token 只在创建和轮换后显示一次，有复制按钮，关闭后不再可见。文件来源用户的按钮禁用并说明。无上限和文件注释丢失都有提示。
- 验证: 在 `web` 下跑类型检查和 `vitest`（上述结构性测试和新页测试）。
- 完成标准: 非根用户看不到入口；刷新后 token 不再出现；新用户立刻出现在权限页建议列表；手机宽度可用。
- 依赖: T6。

### T9 文档

- 文件: `skills/gofer-usage/references/server-config.md`、`web-console.md`、`commands.md`、`docs/reference/http-api.md`、`docs/capabilities.md`、`docs/guide/en/security.md`、`config/gofer.example.yaml`、`CHANGELOG.md`、设计稿末尾。
- 动作: 写用户管理用法、上限语义、停用语义（含恢复的会话继续运行、工作流多跑一轮退避）、离线修复、备份用 SQLite 一致性备份。更正“callers 改了要重启”。写文件用户接管 DB 用户 id 的后果（D10）。发版说明写回退影响（D18）：回退前先取消 DB 用户名下的 schedule、计划和排队 job，回退后这些自动化没有上限；DB 用户 token 401，其授权被标孤儿，重新升级后用 activate 恢复；回退目标不低于 P1 的规则从 P2 起才适用。设计稿补实施记录和 D1、D3、D10、D18 的说明。
- 验证: `sh scripts/check-private-names.sh`；`idevstd script validate_document --kind design docs/design/2026-11-03-config-into-db-design.md`。
- 完成标准: 文档和 `--help` 一致；对外文档没有 issue id 和本机信息。
- 依赖: T7、T8。

### T10 真实环境验收

- 文件: `tmp/gofer-wt-check/`（脚本和证据，不入库）。
- 动作: 用当前构建起临时 server（独立配置、库和端口），文件里只有根管理员。逐条跑并留证据：`caller add` 带上限；新 token 立刻能调接口，不重启、配置文件字节和 mtime 都不变；ceiling 和限流生效；web 打开用户页（能跑浏览器就截图，不能跑就写 `UNVERIFIED_REAL_ENV`）；长连接在轮换和停用后断开；停用后提交被拒；排队 job 失败不重试；`set-ceiling` 改文件用户；重启后用户还在；`sqlite3` 和日志里搜不到 token；手改坏一行后 server 拒绝启动，`repair` 后能起（ceiling 坏 glob、负配额各一次）；文件新增同 id 用户后旧授权已孤儿，删掉条目后旧 token 仍 401；手写 `config_store` 标记后 server 拒绝启动，`gofer job ls` 不受影响；无配置文件部署 `caller add` 不产生文件。真实回退：用 v0.209.3 打开同一个库和配置，能启动，文件用户正常，DB 用户 401，DB 用户带上限建的 schedule 回退后的实际行为（D18），再升级回来 DB 用户能用，并记录授权孤儿和 activate 恢复的实际结果。
- 验证: 每条一份证据写入 `tmp/gofer-wt-check/`，我逐条核对，不用子 agent 的汇报。
- 完成标准: 全部通过，或明确列出 `UNVERIFIED_REAL_ENV` 并停在完成 Gate。
- 依赖: T9。

## 回滚与恢复

每批一个 worktree、一次合并，出问题回退合并提交。合并后未发版不影响线上。Schema 只加两张表，旧版本读旧表不受影响。发版后回退见 T9。恢复点是每批合并后的 `main`。

## 人工 Gate

1. 计划批准（本次）。
2. 当前执行请求：批准后另说一句开始。
3. 发版：tag、升级已长期授权，push 仍走既有审批。

## 可追溯性

| 设计项或验收 | 任务 | 验证 |
|---|---|---|
| 用户入库：新增、发 token、轮换、停用、改上限 | T1、T3、T6、T7、T8 | T6、T7 用例；T10 |
| 不改文件、不重启 | T2、T3 | T2 字节断言；T10 |
| 文件优先、DB 同 id 隐藏 | T2、T3、T5 | T3 接管用例；T5 封锁用例；T10 |
| 根判定只看文件 | T2 | T2 用例 |
| 读失败不当空集、不标孤儿 | T3 | T3 用例 |
| 鉴权、指纹、判空 | T4 | T4 用例；T10 |
| 停用语义 | T4、T5 | T5 用例；T10 |
| 配额热更新 | T5 | T5 用例；T10 |
| 审计 | T1、T6 | T6 用例；T10 |
| 坏用户行拒绝启动、离线修复 | T2、T3、T7 | T7 用例；T10 |
| 见到标记拒绝启动 | T2、T3 | T3 用例；T10 |
| 安全：token 不落地、非根被拒 | T4、T6 | T6 用例；T10 |
| web 用户管理 | T8 | T8 测试；T10 |
| 回退 | T9 | T10 |

## 完成 Gate 与剩余工作

完成条件：T1–T10 通过；对合并后的 `main` 跑 `gofer-verify` 全绿；发版后补 `schema-v*.sql`。进入 `COMPLETE` 时在 progress 写不超过 8 行的完成总结。gofer-nhh7 留到 P3 完成再关。

剩余工作：P2、P3（另写计划），D1 的解除。
