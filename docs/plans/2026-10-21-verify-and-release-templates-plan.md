<!-- template_id: plan; template_version: 1.2.0 -->
# verify 与 release 通用模板 实施计划

> 状态：Draft 0.1 / 待用户批准
> issue：gofer-byev、gofer-51b0

- 思考模式：rigorous。
- 核心目标：落地 `docs/design/2026-10-21-verify-and-release-templates-design.md`（Draft 0.3，D1–D7 已批准）。
- scope freeze：只做设计「范围」列出的内容；DAG、矩阵语法、项目自定义步骤、GitHub 以外平台的实际实现不在本计划内。
- review budget：本计划最多一轮独立评审；评审后修订由作者自查。
- 停止条件：用户具名批准本计划，或用户要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-21 | Claude（监督会话） | 初稿：S1 spec 与扇出 → S2 临时检出与 env 下发 → S3 内置步骤 → S4 结果视图 / rerun / run: always → S5 模板读写接口 → S6 web → S7 init、内置模板与文档；V 验收 |

## 目标与完成定义

1. workflow 顶层 `project:`、`gofer wf run -p`；`fan[]` 每路可有 `name / cmd / env / cwd / timeout_sec`；`ref:` 临时检出（提交时解析 sha，结束即删，worker 先 fetch）；`env` 下发 worker（协议 v26）；渲染与校验覆盖新字段。
2. 内置步骤框架（注册表、`with` 严格校验、内部 exec job `gofer tool step`、`InternalExec` 类别 `workflow-step`）与 `gofer/push`（推分支与 tag、`ls-remote` 核对、`allow_push` + 新权限 `repo.push`）、`gofer/wait-ci`、`gofer/wait-assets`（delivery 平台 API，只看新增告警）。
3. 结果视图（每步 / 每路 fan 的 runner、状态、退出码、耗时、失败摘录）、`gofer wf rerun --failed | --from`、`run: always`。
4. 模板读写接口：读源文件；全局模板增删改（校验、审计）；项目模板写入工作区（未提交）；运行记录另存为模板。
5. web：模板源文件查看与编辑、另存为模板、结果表与 rerun 按钮。
6. `gofer workflow init verify|release`；内置 verify / release 模板；文档与实施记录。

## 已确认事实（独立评审 2026-10-21 已对照代码）

- 引擎严格串行（`internal/job/workflow/types.go` Spec）；`FanSpec` 只有 `Agent` / `Runner`（`types.go:87-92`），`Agent` 必填（`submit.go`）。
- `on_failure` 已存在（`types.go:36-40`）；最后一步被跳过时按完成结束（`advance.go`）。
- 内部 exec job 的参照：`job/branch_delivery.go` `deliverRemote`、`commands/tool_deliver.go`；`InternalExec` 封闭列表在 `job/ceiling.go`。
- worktree：`job/worktree.go`（建分支 `gofer/<job-id>`，base 须执行机本地已有）；worker 共用（`worker/dispatch.go`）。
- `wsproto.Dispatch` 不带 `Env`（`worker/dispatch.go`、`wsproto/frames.go`）。
- `delivery.Push` 只推 `refs/heads/<dst>`（`delivery/git.go`）；平台识别、token、代理客户端在 `delivery/provider.go`、`execute.go`。
- 模板查找与渲染：`workflow/templates.go`（`LookupWorkflowTemplates`、`renderSpec` / `checkRenderedSpec`）、`refs.go`（`validateRefs` 只看 Prompt / Cwd / Cmd）；模板读取不展开 `file:`（S3 遗留，不在本计划）。
- web：`NewWorkflow.vue`（从模板 / YAML 新建）、`WorkflowDetail.vue`；模板只读列出与预览。
- workflow 列表 / 详情查询在 `workflow/query.go`；`gofer wf` 命令在 `commands/workflow.go`。

## 波次与依赖

| 波次 | 任务 | 依赖 | 并行 |
|---|---|---|---|
| 一 | S1 spec 与扇出 | — | — |
| 二 | S2 临时检出与 env 下发；S5 模板读写接口 | S1 | 并行（S2：job / worker / wsproto；S5：workflow 模板读写 + httpapi 新路由，不碰引擎推进） |
| 三 | S3 内置步骤 | S1、S2 | — |
| 四 | S4 结果视图 / rerun / run: always | S3 | — |
| 五 | S6 web；S7 init、内置模板与文档 | S4、S5 | 并行（S6 只改 web；S7 改 commands / workflow 内置模板 / 文档） |
| 六 | V 验收与发版 | 全部 | — |

## 任务

通用要求：只提交自己的文件（pathspec），英文 conventional message；不删注释、不重排无关代码；用户可见变化同步 skill references 与 `docs/reference/http-api.md`（G045 / G046，对外产物写通用用法）；新路由 / 权限按 G056 并重跑 `go generate ./internal/httpapi/...`；分层按 G021–G024（job 不 import workflow）；测试按 G051–G053（不连真实 GitHub / server，不执行 PATH 上的 gofer）；主机实施不能 `-race`，监督者容器复跑。

### S1 spec 与扇出

- `Spec.Project`（顶层默认项目）；从项目 `.gofer/workflows` 读出的模板以该项目为默认；`gofer wf run -p`（HTTP 提交体同名字段）。
- `FanSpec` 加 `Name`、`Cmd`、`Env`、`Cwd`、`TimeoutSec`；exec 时 `Agent` 可省（默认 exec）；fan 名在步骤内唯一；`${steps.<step>.<fan 名>.…}` 引用（保留现有 `.fK.`）。
- `StepSpec.Env`、`StepSpec.Ref`、`StepSpec.Uses`、`StepSpec.With`、`StepSpec.Run`（`always`）的字段与解析（S2 / S3 / S4 才接执行，本任务只做解析、渲染与校验，未实现的字段在提交时报「暂不支持」）。
- 渲染与校验：上述字段全部进入 `renderSpec`、残留占位符检查、`validateRefs`、`mergeStepFromMarkdown`。
- 测试：解析、渲染、校验、fan 每路不同 cmd / env 的提交与 `join: all` 判定（用 testcmd）。

### S2 临时检出与 env 下发

- `ref:` 模式：在执行机上建无分支 detached 临时 worktree（复用 `job/worktree.go` 的建立与清理，新增「临时」变体：不建分支、job 结束即删、链接与清理规则同现有）；提交 workflow 时把 ref 解析为 sha 写回 spec；与 `worktree: true` 互斥。
- worker：执行前 `git fetch` 该 sha（远端名取仓库默认），取不到即失败并说明。
- `env`：`JobRequest.Env` 随 `wsproto.Dispatch` 下发（协议 v26，旧 worker 拒绝带 env 的派发并说明需升级）；worker 侧按现有 env 合并规则（G042）。
- 测试：临时检出与清理（含链接）、sha 解析、worker fetch 成功 / 失败、env 在 worker 上生效、协议门槛。

### S3 内置步骤

- 注册表（`internal/job/workflow` 或独立小包，保持 job 不 import workflow）：`Name`、`with` 结构（严格解码）、`Validate`、执行位置、最低 worker 协议。
- 引擎：`uses:` 步骤提交内部 exec job，`Cmd: [gofer, tool, step, <name>]`，`with` JSON 走 stdin，结果取 stdout 最后一行 JSON 进入步骤结果；`InternalExec` 新类别 `workflow-step`（只豁免 exec 维度，其余上限照常）。
- `gofer tool step <name>`（commands 入口只做绑定与转发，逻辑放 job / delivery）：
  - `push`：`delivery.PushRefs(ctx, dir, remote, refs, proxy)`（分支 `refs/heads/`、tag `refs/tags/`，永不强推，`--porcelain` + 推后 `ls-remote` 核对）；检查项目 `delivery.allow_push`；新权限 `repo.push`（RiskMedium、不种，authz catalog 登记，提交 workflow 时对含 push 步骤的检查调用方持有）。
  - `wait-ci`：仓库从 remote URL 推导；平台 API（github 实现，gitlab / gitea 返回「暂不支持」）；`required` 名单；code scanning 只看该 ref 新增告警；`timeout_min`。
  - `wait-assets`：等 Release 上出现 glob 列出的产物；`timeout_min`。
- 测试：注册与严格校验；push 在本地 bare 仓库上推分支与 tag 并核对、`allow_push` / `repo.push` 门槛；wait-ci / wait-assets 用 httptest 假平台（成功、失败、超时、新增告警判定）。

### S4 结果视图 / rerun / run: always

- `workflow/query.go` 的步骤与 fan 视图补 runner、exit_code、duration、failure_excerpt（读日志尾部按规则抽取，最多 20 行），HTTP 与 `gofer wf show` 输出。
- `gofer wf rerun <id> [--failed | --from <step>]` 与对应 HTTP：基于原 spec 裁剪出新 workflow 提交（`--failed` 只保留失败的 fan / 步骤；`--from` 从该步起），不改原 workflow；新 workflow 记录 `rerun_of`。
- `run: always`：前面失败后仍执行该步，不改变 workflow 最终结论。
- 测试：视图字段与摘录、rerun 两种裁剪、run: always 与最终结论。

### S5 模板读写接口

- `GET /v1/workflow-templates/{name}/source`（来源、路径、原文）。
- 全局模板：`PUT / DELETE /v1/workflow-templates/{name}?scope=global`（写 `<config-dir>/workflows/<name>.yaml`；保存前解析、渲染（变量取默认 / 占位）、内置步骤 `with` 校验；需要 `config.write`；记审计）。
- 项目模板：`PUT …?scope=project&project=<p>`：写项目工作区 `.gofer/workflows/<name>.yaml`（按 G002 用 `ExecPath`），返回「未提交」提示与 `git` 提交命令；受调用方上限项目范围约束；同样校验与审计。
- 另存为模板：`POST /v1/workflows/{id}/save-as-template {name, scope, project?}`（基于 export）。
- 内置模板只读（PUT / DELETE 409）。
- 测试：读源、全局增删改与校验失败、项目写入工作区、权限 / 上限 / 审计、另存为。

### S6 web

- 新建 workflow 页：模板可「查看源文件」，有权限时「编辑」（YAML 编辑器 + 保存，校验错误就地显示；项目模板保存后显示未提交提示与提交命令）、新建全局模板、删除。
- workflow 详情：结果表（步骤 / fan、runner、状态、退出码、耗时、失败摘录、日志链接）；「只重跑失败的」「从此步重跑」按钮；「另存为模板」。
- 测试：纯函数与接线；主机 web 三命令。

### S7 init、内置模板与文档

- `gofer wf init verify|release [--force]`：按仓库内容生成起步文件（go.mod / package.json / `.github/workflows`），带注释；提示提交后 server 才读得到。
- 内置 `verify`（单个 `cmd` 变量）与 `release`（push → wait-ci）模板。
- 文档：skill references plans-workflows（新能力、内置步骤、对照表、示例、信任模型）、web-console（模板查看与编辑、结果表、重跑）、server-config（`repo.push`）；http-api；CHANGELOG；设计稿实施记录。

### V 验收与发版（监督者）

- 容器 Linux 全量 `-race -timeout 30m`、darwin / windows vet；Windows 全量；web 三命令。
- 自用：在 gofer 仓库写 `.gofer/workflows/verify.yaml`（按本仓库的检查），用它跑一次本批的候选提交，对比手工验证结果；release 先在测试仓库（本地 bare 远端）走一遍。
- 发版。

## 回滚与恢复

- 每段一到两个提交；新字段与新路由 additive，`git revert` 即可。worker 协议 v26 只在带 env 的派发上要求新 worker。

## 人工 Gate

- **计划批准与执行请求**：用户具名批准本计划并要求开始实施。
- **外部动作**（已有长期授权）：tag、push、CI、mirror、一键更新。

## 可追溯性

| 设计条目 | 任务 |
|---|---|
| 一·1 顶层 project、一·2 扇出每路不同、一·9 渲染校验 | S1 |
| 一·3 ref 临时检出、一·4 env 下发 | S2 |
| 一·5 内置步骤、五 repo.push | S3 |
| 一·6 结果视图、一·7 续跑、一·8 run: always | S4 |
| 四 模板读写（D7） | S5、S6 |
| 二 模板与 init、三 对照表、文档 | S7 |
