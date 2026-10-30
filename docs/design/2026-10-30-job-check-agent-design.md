# job 结束后的检查 agent 与后台 agent 统一管理设计

> issue：gofer-r29d、gofer-f3jr
> 状态：0.1 草稿
> 相关：`internal/job/execute.go`、`internal/job/review.go`、`internal/job/resume.go`、`internal/job/model.go`、`internal/work/summarize.go`、`internal/work/summarize_session.go`、`internal/config/model.go`、`internal/config/steward.go`、`internal/httpapi/config_write_handler.go`、`internal/httpapi/steward_handler.go`、`web/src/views/settings/WorkSettings.vue`、`docs/design/2026-09-18-supervision-loop-and-agent-reliability-design.md`

## 1. 背景与问题

gofer 已经有两种互补的监督能力：`job run --verify` 在执行机上直接运行验证命令，人工验收通过 `needs_review` 阻止实施 agent 自己签收；同时，工作项整理器和管家已经会由 server 发起后台 agent。当前仍有三处缺口：

1. 命令验证只能证明命令的结果，不能检查任务书中「至少提交几次」「汇报必须列出未完成项」「必须补某类测试」等交付要求。监督者仍需逐个阅读汇报、提交列表和工作树。
2. 整理器、管家和拟新增的 job 检查 agent 各自管理自己的配置、会话和状态；整理器虽已有按次数 / 年龄轮换的会话记录，另外两类没有统一的轮换和用量视图。
3. Web 设置页目前把整理器和管家放在工作设置中，无法回答「后台 agent 是否启用、最近花费多少、正在复用哪个会话、何时会轮换、如何手动清空上下文」这些运维问题。

本设计合并两个 issue，但实现方案保持两节独立：第 4 节定义 job 检查 agent 的一次性监督闭环，第 5 节定义三类后台 agent 的统一会话与管理页。两者共享会话轮换、NDJSON 用量和配置字段策略，但检查 agent 不依赖 Web 页面才能工作。

## 2. 目标与非目标

### 目标

- `job run --verify-prompt "…"` 为 job 保存简短、明确、可审计的交付检查要求；任务书模板、plan todo、HTTP 和 MCP 入口都能传递它。
- agent 正常完成后，server 在执行现有 `--verify` 命令之后准备有限、脱敏的验收材料，调用一个只读、无工具、单轮的检查 agent，得到严格结构化的合格 / 不合格结果。
- 不合格时自动续接原 job，默认最多 2 轮；每轮把具体不合格条目交给实施 agent 修复；超过上限后停在 `needs_review`，并保留检查结果和事件。
- 检查 agent 的失败与「实施交付不合格」分开处理，不能因为检查基础设施故障而自动修改代码或无限重试。
- 检查 agent、整理器、管家在 Web 的同一「后台 agent」页展示配置、运行次数、用量、当前会话和轮换状态，并能手动新开会话。
- 尽量复用同一会话；支持清空上下文的 agent 使用 `suga run --sid <id> --clear-ctx` 语义，其他 agent 没有该能力时退化为新会话。
- 配置写入继续走现有配置写接口、字段策略和管理员权限；新增 HTTP 路由和权限全部按 G056 登记、生成参考文档。

### 非目标

- 检查 agent 不运行验证命令、不读取工作树、不调用 shell、MCP、网络或其他工具；它只判断 server 提供的材料。
- 不让检查 agent 代替人工验收；`needs_review` 仍是最终需要人决定的状态。
- 不把三类 agent 的配置强行搬到一个新 YAML 块，不删除现有 `work:` / `steward:` 字段，也不改变现有整理器和管家的业务提示词。
- 不做跨 provider 的统一模型参数语义；只保存 agent 能理解的模型字段 / 参数，并展示它报告的用量。
- 不把完整 diff、源码、环境变量、密钥或任意 stdout 全量发送给后台 agent。

## 3. 共同设计原则

### 3.1 深模块与适配器

新增一个 server 内部的 `BackgroundAgentRunner` 深模块作为共同接口：调用方只提供 agent 类型、提示词、项目和一次性约束，模块负责选择会话、构造无工具请求、采集 NDJSON 用量、解析固定 JSON、持久化运行记录和按阈值轮换。整理器、管家和 job checker 分别是该接口的适配器；现有 `work.OneShot`、`steward` 的业务接口不直接暴露会话数据库细节。

建议的最小接口（名称可在实施时按包边界调整）：

```go
type BackgroundAgentKind string
const (
    BackgroundJobCheck BackgroundAgentKind = "job_check"
    BackgroundSummarizer BackgroundAgentKind = "summarizer"
    BackgroundSteward BackgroundAgentKind = "steward"
)

type BackgroundRunRequest struct {
    Kind, Agent, Project, Prompt string
    Args, ModelArgs []string
    Timeout time.Duration
    Session SessionRef
    Mode RunMode // fresh | resume | clear_context
}

type BackgroundRunResult struct {
    JobID, SessionID string
    Output []byte
    Usage UsageSnapshot
    Session SessionSnapshot
}
```

该接口不允许调用方传入 `cmd.Env`、工具列表、MCP 注册表或任意读目录；权限和安全约束在适配器内固定。测试通过这个接口注入 fake adapter，不启动真实 CLI，也不依赖 PATH 上的 gofer。

### 3.2 一套会话记录、三种业务记录

job 检查 agent 的每一次调用要有可关联的 `job_id`，整理器和管家也要保留自己的业务关联（整理 run、管家 review / ask）。建议增加通用 `background_agent_sessions` 与 `background_agent_runs` 表，字段只保存审计所需的元数据：

- session：`kind`、`session_id`、`agent`、`project`、`created_at`、`last_used_at`、`runs`、`context_used`、`context_limit`、`last_usage_json`、`state`。
- run：`kind`、关联业务 id、session id、开始 / 结束时间、结果状态、输入 / 输出字节数、usage JSON、错误摘要；提示词和完整输出不入通用表，job 检查结果另存 job 的受限字段。

所有 `context_*` 数值都允许为空：provider 没有报告上下文窗口时只按次数 / 年龄轮换，Web 显示「未知」，不能把未知当作 0。用量采用已有 `Usage` 的 token / cost 字段，并保留 `source`；一次 NDJSON 流中以最后一个合法 usage 为本轮快照，坏行只记 debug，不使业务运行失败。

### 3.3 统一轮换规则

会话在一次运行前按下列顺序决定：

1. 手动「新开会话」：结束并解绑当前记录，下一次必 fresh；不删除历史 run。
2. `runs >= max_runs`、超过 `max_age`，或 `context_used / context_limit >= max_context_ratio`：尝试清空上下文；若 agent 不支持清空，则 fresh。
3. agent / project / model / 运行模式变化，或上一轮会话不可继续：fresh。
4. 其余情况 resume / continue。

默认建议 `max_runs=20`、`max_age=24h`、`max_context_ratio=0.75`。轮换判断在运行前执行并记录 `rotation_reason`；清空上下文成功后保留相同 session id 并把 `runs` 与上下文计数归零，无法清空时创建新 id。这样既满足「尽量复用同一会话」，又不会让长期上下文无限增长。

## 4. 方案一：job 结束后的检查 agent（gofer-r29d）

### 4.1 触发时机与状态机

job 的顺序固定为：

1. 实施 agent 进程结束。
2. 若请求有 `--verify`，先在原有执行机 / cwd / env 执行该 argv；这一步的既有 `passed|failed|timeout|skipped` 语义不变。
3. 只有 agent 正常结束且 `--verify` 未失败时，server 才运行检查 agent。检查 agent 看不到验证命令的执行权限，但材料中会带验证结果。
4. server 持久化检查结果，再决定正常 `done` / `needs_review`，或创建检查续接。

检查 agent 的不合格是交付检查结果，不把它伪装成 provider 的 transient failure，也不进入现有 `auto_resume` / fallback 链。检查续接使用独立的 `check_resume_count`，避免和瞬时错误的续投次数共用预算。

每次检查：

- 合格：继续原有逻辑。若 `--review` 或项目 `require_review` 生效，状态为 `needs_review`；否则 `done`。
- 不合格且 `check_resume_count < max_auto_resumes`：源 job 记录 `job.check_failed` 和 `job.check_auto_resumed`，源 job 以失败型中间结果结束，提交一个继承原工作树、plan、todo、verify prompt 和通知策略的新 resume job。todo 保持 `doing`，不写 done note。续接提示只包含不合格条目和修复要求，要求先检查已有提交，只做剩余修复。
- 不合格且达到上限：源 job 直接以 `needs_review` 停留，即使原 job 没有 `--review`；这是为了把「机器判定不合格但无法再自动修复」交给人。记录最终检查结论和每轮摘要，todo 按 `needs_review` 规则联动。
- 检查 agent 本身超时、不可用、输出不是合法 JSON、usage 解析失败或内部持久化失败：记 `job.check_error`，不自动 resume，按原 job 的 `done` / `needs_review` 结果继续。检查基础设施故障不能变成对代码的错误否定；页面和事件明确显示「检查未完成」。

检查续接完成后再次从第 2 步开始，因此每一轮仍然先跑 `--verify`，再检查。`--verify` 失败不会再调用检查 agent，也不会消耗 `check_resume_count`；现有 `--verify` 的 review 映射保持不变。`needs_review` 的判定在检查成功 / 检查上限决策之后执行，保证不合格不会先被人工验收状态遮住，也保证检查续接不会试图 resume 一个已经进入 `needs_review` 的源 job。

### 4.2 配置形状

不新增顶层业务配置，增加 `server.job_check`，因为它是 server 对 job 终态的编排策略，不属于某个项目的工作项整理：

```yaml
server:
  job_check:
    enabled: false
    agent: "claude"
    model: "haiku"
    args: ["--tools", ""]
    timeout_sec: 120
    max_auto_resumes: 2
    max_runs: 20
    max_age: "24h"
    max_context_ratio: 0.75
    daily_limit: 50
    input_max_bytes: 65536
    report_tail_bytes: 16384
```

- `enabled` 默认 false；没有 `verify_prompt` 的 job 不触发，即使全局开启。
- `agent` 必须是已解析、可执行一次性无工具调用的 agent；`model` 和 `args` 是后台调用参数，server 强制追加 / 覆盖无工具和只读约束。
- `timeout_sec`、`max_auto_resumes`、`daily_limit` 是成本硬上限；0 表示关闭该限制只在字段文档明确允许时使用，默认值不靠 0 猜测。
- `max_runs`、`max_age`、`max_context_ratio` 参与共同会话轮换。若 `max_context_ratio` 无法计算，只用其它阈值。
- `input_max_bytes` 和 `report_tail_bytes` 是防止任务输出放大费用的 server 限制，不由 job 请求覆盖。

任务请求新增：

```go
VerifyPrompt string `json:"verify_prompt,omitempty" yaml:"verify_prompt,omitempty"`
```

提交时 trim 并限制长度；原始要求存入 `request_json`，检查结果不覆盖它。模板 frontmatter、plan todo 和 MCP / HTTP 都使用同一字段名 `verify_prompt`。项目默认暂不新增：先用模板 / todo 复用，避免一个项目的模糊默认要求误套到所有 job；若后续需要项目默认，应单独设计继承与关闭语义。

### 4.3 server 准备的输入材料与 JSON 输出

server 建立版本化的 `CheckInput`，检查 agent 只看到一个明确的、带不可信内容分隔符的 JSON / Markdown 请求，不访问 cwd：

```json
{
  "schema": "job-check/v1",
  "requirements": "至少 3 个 git commit；汇报列出未完成项；必须新增测试",
  "report_tail": "<job stdout 末尾，可能被截断>",
  "verify": {"status":"passed", "command":["go","test","./..."], "exit_code":0, "duration_ms":1234},
  "commits": [{"sha":"…", "subject":"…"}],
  "diff_summary": {"files": 4, "insertions": 80, "deletions": 12},
  "uncommitted": {"count": 1, "paths":["…"], "truncated":false},
  "worktree": {"base":"…", "head":"…"}
}
```

材料由 server 从已持久化的 job outcome / git 采集结果组装。提交列表最多 50 条，路径、subject、汇报和要求均按字节上限截断；不提供完整 diff、文件内容、环境变量、token、secret 或 worker 凭据。未提交改动只提供路径 / 数量，不让检查 agent读取文件内容。报告末尾按既有 stdout 采集口径取得，缺失时显式标记 `unavailable`，不能当空汇报。

检查 agent 必须只输出一个 JSON 对象：

```json
{
  "schema": "job-check-result/v1",
  "verdict": "pass|fail",
  "items": [
    {"id":"commit-count", "requirement":"至少 3 个 git commit", "passed":false,
     "evidence":"found 1", "action":"继续完成剩余提交"}
  ],
  "summary": "…"
}
```

`items` 至少一项时才允许 `fail`；`summary` 和 `evidence` 都按长度限制保存。解析采用严格 schema、未知字段忽略并记录 debug；额外文字、重复 JSON、超长输出、缺少 verdict 都属于检查 agent 错误而非不合格。检查 agent 不应自行推测缺失材料为失败。

### 4.4 数据模型、事件和审计

在 job 持久化模型中增加可选字段（旧库迁移必须先建旧 schema 再执行迁移）：

- `verify_prompt`：请求的已裁剪值也存入 `request_json`，JobResult 镜像用于详情展示。
- `check_json`：最后一次检查的 schema、verdict、items、summary、session id、usage、时间和 `input_truncated`；不存检查 prompt 全文。
- `check_resume_count`：已经创建的检查续接轮数。
- `check_error`：检查基础设施错误摘要，与实施 agent 的 `Error` 分开。

新增事件：`job.check_started`（agent、轮数、输入截断标记）、`job.check_finished`（verdict / status / usage 摘要）、`job.check_failed`（基础设施错误）、`job.check_auto_resumed`（续接 job id、失败条目 id、轮数）。事件 detail 不带完整汇报和秘密。job comment 追加一条短结论，包含 verdict、轮数、续接 id 或「检查未完成」；长 JSON 只在 job 详情中按权限返回。

`JobResult`、HTTP job view、CLI `job show` 和 Web JobDetail 只展示结构化结果和截断的 evidence；`job events` 展示事件顺序。`check_json` 的持久化应在源 job 的状态对外可见前完成，遵循 `finish` 当前「事件先于终态可见」的时序；检查调用本身在后台编排中但不能让终态读者看到一个尚未有检查结论的假定通过。
