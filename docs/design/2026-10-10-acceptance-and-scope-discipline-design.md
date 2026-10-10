# 验收标准贯穿 + 范围纪律（gofer-3nxa.4 / .3）

> 2026-10-10 · epic gofer-3nxa · plan plan-20261010-114318-eade7db6
>
> 状态：已实施（分支 z-accept，2026-10-10），与本文的差异见文末「实施记录」。

## 背景

- 验收标准只存在于 tracker issue（`acceptance_criteria`），plan todo 和 job 都用不上；派活时靠人把标准抄进任务书，验收时靠人记。
- 子 agent 常"顺手"改范围外代码；范围外发现的问题要么被悄悄改掉，要么丢在汇报正文里没人跟进。

目标：验收标准从 todo 一路带到 job 任务书和验收面板；agent job 默认遵守范围约定，范围外发现集中写进汇报的固定小节，验收时单列并可转 issue；可选声明改动范围，越界文件在验收时标出。

## 一、验收标准贯穿（.4）

### 数据

| 位置 | 新字段 | 说明 |
|---|---|---|
| `jobstore.PlanTodo` / `plan_todos` | `acceptance TEXT` | additive 迁移（沿用 `ALTER TABLE plan_todos ADD COLUMN` 循环） |
| `jobstore.TodoPatch` | `Acceptance *string` | `""` 清空 |
| `job.JobRequest` | `Acceptance string \`json:"acceptance,omitempty"\`` | 落 request_json |
| job 详情视图 | `acceptance` | 显式字段（job 详情不再回显 request_json） |

### 入口

- CLI：`plan add-todo/set-todo --acceptance "<文本>"`（`todoDispatchFlags` 共用）；`plan add-todo --acceptance-from-issue <id>` 在客户端从当前仓库 tracker 读 issue 的 `acceptance_criteria` 填入（读不到报错，不静默）。`job run --acceptance "<文本>"`。
- HTTP：`addTodoReq` / `PATCH /v1/todos/{id}` / `POST /v1/jobs` 接受 `acceptance`；todo 视图回显。
- MCP：`gofer_add_todo` / `gofer_update_todo` 增加 `acceptance` 参数；`gofer_submit_job`（若有同类提交工具）同步。
- 派发：`dispatchTodo` 把 `todo.Acceptance` 拷进 `JobRequest.Acceptance`。

### 注入任务书

`Submit` 在模板渲染（`applyTemplate`）之后、仅对非 exec agent 且 `Acceptance != ""` 时，在 prompt 末尾追加：

```
## 验收标准

<acceptance 原文>

汇报末尾逐条说明是否满足：满足 / 未满足 / 无法验证，并给出依据（测试名、命令输出、文件位置）。
```

- 只追加一次：`job resume` / 自动续投 / rerun 不重复追加（续接的 prompt 是续接指令本身，不带 Acceptance；rerun 复用原 request 时注入发生在 Submit，需确认原 prompt 未已含该节——以注入前检测 `## 验收标准` 标记兜底）。
- exec agent 忽略（只记录、不注入），验收面板照样显示。

### 展示

- Web `ReviewPanel.vue`：有 `acceptance` 时，在页签上方显示「验收标准」块（markdown 渲染），每条列表项前带本地勾选框（仅前端状态，不持久化，用于人工逐条对照）。
- Job 详情页基本信息区显示 acceptance（折叠）。
- `gofer job review <id>`：在汇报前打印「验收标准」节。

## 二、范围纪律 +「发现但不碰」（.3）

### 约定注入

对**非 exec 的批处理 agent job**（不含交互 pty / ACP 持续会话），`Submit` 在 prompt 末尾追加「交付约定」节：

```
## 交付约定

- 只改与本任务直接相关的代码和文档；不要顺手重构、改名或清理范围外的内容。
- 范围外发现的问题（bug、坏味道、过时文档等）不要修改，写进汇报末尾的「## 发现但不碰」小节，每条一行：`- <位置>：<问题>`。没有就省略该小节。
```

开关：项目配置 `scope_discipline: auto|on|off`（默认 `auto`）。`auto` = 仅对 plan todo 派出的 job、review 门控 job（`--review` / `require_review`）以及带 `acceptance` 或 `scope` 的 job 注入；`on` = 所有非 exec 批处理 agent job；`off` = 不注入。job 级 `--no-scope-discipline` 可单次关闭。注入顺序：验收标准节在前，交付约定节在后。

### 声明范围（可选）

- `JobRequest.Scope []string`（路径 glob，相对 job cwd 所在仓库根）；todo 同名字段；CLI `--scope 'internal/job/**,web/src/components/ReviewPanel.vue'`（逗号分隔，可重复）。
- 有 scope 时，交付约定节追加一行「本任务的改动范围：<globs>」。
- 验收时：用 job 的 diff 文件列表（已有 changes / commits diff）比对 scope，越界文件在 Web Diff 页签与 `job review` 中标「范围外」。只提示，不阻塞 accept。

### 解析与展示

- 解析规则（前后端各一份，口径一致，Go 侧 `internal/job` 一个纯函数 + 单测；前端 `web/src/utils/` 一个纯函数 + vitest）：在汇报 markdown 中找标题文本为「发现但不碰」（兼容 `Out of scope` / `Out-of-scope findings`，任意级别 `#`）的小节，取到下一个同级或更高级标题为止，抽出其中的列表项文本。
- Web `ReviewPanel.vue`：有发现时新增「发现」页签（带计数），列出每条；每条提供「复制为 issue 命令」按钮，生成 `gofer issue create "<文本>" -d "discovered in job <id>" --tag discovered`（服务端暂无「新建 tracker issue」写接口，不在本期新增）。
- CLI：`gofer job review <id>` 打印「发现但不碰」节；新增 `gofer job findings <id> [--create-issues] [-p <优先级>] [--tag <t>]`：无参数列出；`--create-issues` 在**当前仓库**的 tracker 里逐条建 issue（描述带 job id），打印新 id。

## 不做（本期）

- 服务端新建 tracker issue 的写接口与 Web 一键建 issue（后续视需要补，届时「复制命令」升级为直接创建）。
- 验收勾选状态持久化、逐条验收结果结构化回写。
- 从 issue 自动继承验收标准的服务端路径（todo 无 issue 关联字段；本期只有 CLI `--acceptance-from-issue`）。

## 验证

- Go：迁移 / todo patch / dispatch 拷贝 / Submit 注入（含 exec 不注入、resume 不重复、开关三态）/ 发现小节解析 / scope 越界判断 的单测；`go test -race` 相关包。
- Web：解析函数 vitest；`vue-tsc` / `vitest` / `vite build`。
- 实测：主机建一个带 acceptance + scope 的 todo，派给 claude 做小改动，验收面板看到验收标准、发现页签、越界标记。
- Skill（G045）：commands.md 的 plan / job 节、SKILL.md 验收段落。

## 实施记录（2026-10-10）

- 注入点：`Submit` 在 `applyTemplate` 之后、交互首条输入与 rules 注入之前调用 `injectPromptSections`（`internal/job/prompt_sections.go`）。续接 / worker 重入沿用 `RulesResolved` 跳过；rerun / peer 重提交靠节标题（`## 验收标准`、`## 交付约定`）去重。验收标准只排除 exec，交互 pty 的首条输入里也带。
- 交付约定额外排除 messenger 传话 job 与管家 job。`scope_discipline` 只在全局项目配置里，不进 `.gofer.project.yaml` 白名单。
- `acceptance` / `scope` 作为 `JobResult` 字段（从 request_json 读出），所以 job 列表响应里也有，不只详情。
- 越界判断（`job.ScopeMatch`）：`**` 跨目录，`*` / `?` 只在一层；不含通配符的 glob 也按目录前缀匹配。文件列表取自 `changes.diff`：普通 job 只含未提交改动，自提交的改动不在其中（worktree job 含已提交段）。
- 发现小节取汇报（stdout 尾部 64KB）里**最后一个**同名小节，代码块内标题忽略；`job review` 只在 `--tail > 0`（默认 60）时解析。`job findings --create-issues`：标题 = 条目（超 120 字截断），描述 = `discovered in job <id>` + 原文，类型 task，默认 P2、标签 `discovered`。
- Web 验收标准块：列表项是纯文本 + 勾选框，非列表段落走 markdown 渲染。
