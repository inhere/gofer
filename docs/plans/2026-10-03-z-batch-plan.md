<!-- template_id: plan; template_version: 1.2.0 -->
# Z 批多 agent 对比、择优合并与流程模板实施计划

> 状态：Draft 0.1 / DIRECT_CONTINUOUS 执行

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-03 | Codex | 将 Z1–Z3 拆为测试先行的独立功能点，明确本期不做 Z4 Web |

## 规划可靠性声明

- `thinking_mode=RIGOROUS`
- `core_objective=在现有 workflow/job/template 分层上完成 Z1–Z3 的 CLI、API 和内置模板，并以失败测试、实现测试和隔离冒烟证明可用`
- `allowed_scope=tools/gofer 的 internal、cmd、docs、README、skills/gofer-usage、tmp 验证证据`
- `non_goals=不做 Z4 Web，不支持远程 worker 合并，不 push，不重启或 reload 正式 server/worker，不修改正式配置目录，不扩大协议语义`
- `expansion_policy=DEFER_OR_REQUEST`
- `review_budget=每个功能点一次 focused review；发现设计未覆盖或所有权冲突立即停止`
- `stop_conditions=规范指纹变化、临时栈无法隔离、设计冲突、质量门失败且无法在当前 owner 内修复`

## 目标与完成定义

1. Z1：workflow 步骤支持异构 `agents`/`fan`，透传 worktree、模板变量、只读和 verify；按步骤名及 `.all` 聚合引用，保留每个 fan 的 worktree/diff 字段。
2. Z2：`join: pick` 在择优前等待；CLI/API 支持选择 fan、检查主 checkout 干净和预期分支、无半合并地 merge/squash，并可清理未选 fan；远程 runner 返回明确本机限制错误。
3. Z3：项目/全局/内置 workflow 模板库支持 vars、CLI/API 运行与查询；内置 `compare`、`plan-implement`、`review-committee` 均可验证。
4. README 与 `skills/gofer-usage/` 同批更新，内容以实现和 `--help` 为准；不修改 Web。
5. 固定测试全部出现并通过：`TestWorkflowAgentsFanOutUsesEachAgent`、`TestWorkflowStepWorktreePassThrough`、`TestStepRefByNameAndAll`、`TestJoinPickWaitsForSelection`、`TestWorktreeMergeConflictReturns409`、`TestWorkflowTemplateVars`、`TestBuiltinWorkflowTemplatesValidate`。

## 范围、排除项与授权

范围仅限 `tools/gofer` 一期 Z1–Z3 及文档。测试使用 `t.TempDir()`；真实冒烟只使用临时 `GOFER_CONFIG_DIR`、随机端口和临时 git 仓库，每条命令显式带临时 `--server` 或 `-c`，剔除 `CLAUDE*` 环境变量。不构建到 `web/dist`，不连接 8767，不触碰 `D:\work\inhere\config\win-env\gofer`。

当前执行授权来自用户：先写本计划候选并提交，随后直接连续实施；本地提交按功能点进行，不 push。

## 输入与设计边界

- 设计：`docs/design/2026-10-03-multi-agent-compare-and-flow-templates-design.md`（Approved 0.1）。
- 相关 owner：`internal/job/workflow`、`internal/job/worktree.go`、`internal/template`、现有 `httpapi`/`commands` workflow 路由。
- G021/G022：workflow 通过 `JobOps` 取宿主能力，job 不反向 import workflow；入口只绑定、校验和转发。
- G032：不新增无标记兼容分支；仍保留的旧路径必须有 `// DEPRECATED(vX): remove in vY` 和文档记录。
- G045：用户可见命令、接口、模板和会话行为与 `skills/gofer-usage/` 同批更新。

## 波次与依赖

| 波次 | 内容 | 依赖 | 独立提交 |
|---|---|---|---|
| W0 | 本计划、设计合并、基线与计划校验 | 规范 BOUND | `docs(plan): add Z batch implementation plan` |
| W1 | Z1 异构 fan、透传字段、命名及 all 引用 | W0 | 测试先行后实现 |
| W2 | Z2 pick、主 checkout merge/squash、冲突 409、清理 | W1 | 测试先行后实现 |
| W3 | Z3 vars、模板查找/渲染/运行、三个内置模板 | W0 | 测试先行后实现 |
| W4 | README、skill、CLI/API help 与真实隔离冒烟 | W1–W3 | 文档/验证提交 |
| W5 | Windows/Linux build、vet、全量 Go 测试、gofmt、控制字符扫描 | W1–W4 | 无代码变更 |

## 功能点实施

### Z1 异构 fan 与 workflow 引用

- 先写并单独运行固定测试，覆盖每个 agent 只收到对应 fan、worktree/template/vars/read_only/verify 逐字段透传，以及按步骤名和 `.all.stdout` 取值。
- 复用 workflow 的步骤解析、JobOps 和现有 worktree 创建；不得让 workflow import commands/httpapi。
- worktree 写操作的多 agent fan 必须隔离；remote runner 只在 Z2 合并路径拒绝。

### Z2 择优与本机合并

- 先写 `join: pick` 等待测试和 merge 冲突 409 测试。
- server 侧合并项目主 checkout：先验证干净且在预期分支；失败 409 并说明；冲突执行 `git merge --abort`，返回冲突文件；绝不 push。
- CLI：`gofer wf pick` 与 `gofer job worktree merge`；API：`POST /v1/jobs/{id}/worktree/merge`。选中后可显式清理其余 fan。
- 远程 worker job 返回“仅支持本机 runner”的稳定错误；若设计未覆盖的状态迁移出现，停在 Gate 报告差异。

### Z3 workflow 模板

- 先写 vars 和内置模板验证失败测试。
- 模板查找顺序：项目 `.gofer/workflows/*.yaml`、全局 config workflows、embed 内置；vars 支持 default/required/desc，步骤使用 `${vars.x}`。
- 实现 `GET /v1/workflow-templates[/{name}]`、`POST /v1/workflows` 的 template+vars，以及 `gofer wf template ls/show`、`gofer wf run --template --var`。
- 三个内置模板必须通过现有 workflow schema/validation；模板内容不包含业务项目名或真实凭据。

## 前置检查与质量门

- 每个 mutation stage 前以当前 fingerprint/scope 执行 `idev-std resolve --response verify --task implement --task test --mutating`；变化即停。
- 先失败测试后实现；禁止 `t.Skip` 占位。
- 每项提交前 `git status --short`，只暂存 owner 文件；`gofmt -l` 必须无输出。
- 最终运行并保留原始输出：`gofmt -l`、Windows/Linux build、`go vet ./...`、`go test ./... -count=1`、控制字符扫描、固定测试和隔离真实冒烟。

## G032/G045 记录

- 当前预期不保留旧 wire/配置兼容分支；若实现中发现仍有必要的旧路径，使用带版本移除标记并在本节补记。
- 用户可见变化包括 `wf` 命令组、worktree merge API、模板 API/vars、pick 状态与内置模板，因此必须更新 `skills/gofer-usage/SKILL.md` 或其 references，并在最终报告列出具体节；无关 skill 不改。

## 完成 Gate

只有固定测试、隔离 compare→pick→merge→cleanup 冒烟、review-committee 汇总冒烟、全部质量门、git log/status 和 G032/G045 清单都有原始证据，才报告一期完成。未完成项、设计差异和需人工决策点逐项列出；不 push。

