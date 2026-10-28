# 每日记忆综合（daily-memory-synthesis）设计

> issue：gofer-gah7
> 状态：0.1 草稿
> 相关：`docs/design/2026-10-09-prime-memory-quality-design.md`（记忆质量、doctor、管家建议卡）；`docs/design/2026-10-12-local-global-memory-design.md`（全局记忆同步）；`internal/work`（工作项与每日摘要）；`internal/steward`（每日巡检与一次性 agent 会话）；`internal/brief` / `internal/tracker/prime.go`（接手包与会话开场）；`internal/serve/serve.go`（schedule sweeper）。

## 1. 背景与问题

当前系统已经能收集大量“当天发生过什么”的结构化事实，但这些事实分散在不同模块，开场时只能按各自规则拼接：

- `internal/work` 维护工作项、状态、journal、提醒、会话与 job 链接，并在固定时间发出 `work.digest`；
- `internal/today` 在 server 镜像上运行 memory doctor，产生 `memory-findings`，并通过 `memory-suggestions` 等待人确认；
- `internal/steward` 在每日 review 前读取工作项、事件、今天页未处理卡、记忆 hygiene，调用一个 resident ACP 会话进行整理；
- `internal/brief` 读取 issue / plan / todo / handoff / 记忆，生成一次性的接手包；
- `internal/tracker/prime.go` 将本地 tracker、全局记忆、项目记忆和 server handoff 压入固定的 8 KiB 开场预算；
- `schedule` 已有持久化的 cron / once 触发器、CAS 推进、catch-up、权限边界和事件记录。

缺少的是一个**可持久化、可引用、按天更新的综合结果**：把当天的工作事实与已经存在的记忆材料交给一个 agent，生成少量“今天以后值得保留的知识”和下一次开场可引用的摘要。现在如果直接让 steward 自己阅读所有数据库内容，会有三个问题：

1. 输入不稳定：同一事实可能在 work digest、journal、job report、memory finding 中重复出现，模型难以区分事实、建议和用户原文；
2. 结果不可追溯：当前 steward 的 review 只留下 review 状态和可选点评，没有一个带材料快照、来源和版本的综合记录；
3. 成本与隐私不可控：每日无变化也可能启动模型；把完整 prompt、命令、邮件和 secrets 送入模型会扩大暴露面。

因此需要一个 server 侧的编排模块：先以固定 schema 收集和裁剪材料，再由一个可配置的低成本 agent 输出严格结构化结果；结果默认作为 `handoff` / `note` 候选，而不是自动写入长期记忆；下一次开场只引用摘要和来源，不把整份历史再注入。

## 2. 目标与非目标

### 2.1 目标

- 每个项目按本地日生成一份 `daily synthesis`，包含日期、材料指纹、综合摘要、可复用经验候选、未决项和来源引用；
- server 只提交结构化、经过裁剪和脱敏的材料，agent 只承担归纳、去重和候选提议，不直接写 memory、issue、plan、work item 或发送通知；
- 无新材料、材料指纹未变化、当天已成功综合时不启动模型；同一日失败可按退避重试，不能重复生成无界成本；
- 结果可由 HTTP、CLI、MCP 查询，且能被 `repo prime`、`issue brief`、`plan brief` 以短引用形式使用；原始材料仍可追溯到 job / work / plan / issue / finding，但不在开场展开；
- 可选模型、可配置输入/输出长度、运行次数、超时和项目范围；默认关闭或明确 opt-in，不改变既有 `work.digest`、steward review、memory candidates/suggestions 的语义；
- 对 secrets、token、环境变量值、完整命令行、邮件正文和用户未授权的附件实行默认过滤，并记录过滤统计而不是原文。

### 2.2 非目标

- 不自动将 agent 输出写入 `tracker.Memory`，不自动归档、合并、修改 kind 或采纳 `memory-suggestions`；所有长期记忆变化继续经过现有人确认路径；
- 不重新实现 work digest、memory doctor、plan handoff 或 tracker sync；它们是材料源；
- 不让 agent 直接访问 server 数据库、文件系统、shell、MCP 外部工具或任意项目工作区；
- 不翻译用户原始内容，不在本功能中新增多语言协议；结果的显示语言沿用项目 / server 语言解析；
- 不保证“综合”是事实真相判断。输出必须标明推断与来源，不能取代原始记录和人工确认。

## 3. 方案

### 3.1 总体结构

新增 `internal/memorysynthesis` 深模块，接口尽量小，内部组合四类 adapter：材料读取、脱敏裁剪、agent 执行、持久化。入口层只负责绑定配置、HTTP / CLI / MCP 参数和权限；server 编排层负责按日调度。

一次运行按以下顺序执行：

1. 根据 `project_key` 与 server 时区计算业务日窗口 `[day 00:00, next day 00:00)`；读取工作项变化、journal、当天完成 job 的报告摘要 / findings、plan todo 结论、memory-findings / pending suggestions、最近的 plan handoff 和上一份 synthesis 的摘要；
2. 各材料源先在 server 内过滤、去重、排序、截断，并为每条材料附带稳定 `source_ref`、`source_kind`、`occurred_at`、`sensitivity`；
3. 对材料做 secret scrub 和内容预算检查，计算规范化材料的 SHA-256 `input_digest`；
4. 若同项目同日已有相同 `input_digest` 的成功记录，返回已有结果；否则创建 `running` 记录，提交一次只读、无工具的内部 job；
5. agent 只输出版本化 JSON，server 严格校验 schema、长度、枚举和来源引用，失败则记录 `failed`，不产生候选；
6. 通过 CAS 将结果写为 `succeeded`，生成短摘要和候选列表；开场只读短摘要，详情按 ID 查询。

server 不把整份 prompt 存库。为可审计性，保存材料摘要、来源引用、过滤计数、输入 digest、agent 标识 / 模型、输出 digest、状态和错误；是否保存脱敏后的材料快照作为配置项，默认不保存。

### 3.2 配置形状

推荐在顶层新增 `memory.synthesis`，而不是复用 `work` 或 `steward`：三者都可能使用 agent，但生命周期、权限和结果语义不同。

```yaml
memory:
  synthesis:
    enabled: false                 # 默认关闭；显式开启才产生模型成本
    projects: []                   # 空 = 所有启用 memory 的项目
    time: "09:15"                  # 本地日触发时间；空 = work.digest 前 15 分钟
    lookback: 24h                  # 材料时间窗口；按项目时区解释
    agent: ""                     # 空 = work.summarizer_agent；不继承 steward 的 resident 会话
    runner: "local"               # 只允许 server 内置 local
    model_args: []                 # 例如低成本模型参数；不允许工具开关被打开
    max_input_bytes: 24000
    max_output_bytes: 12000
    timeout: 5m
    daily_limit: 1                 # 每项目每天成功或运行中的最大次数；-1 表示不限制
    retry_after: 30m
    retain_days: 90                # 结果与运行元数据保留期
    keep_material_snapshot: false  # 默认只留 digest 与 source_ref
```

配置校验要求：`enabled` 为真时必须能解析 agent；`runner` 只能是规范名 `local`；所有数值有上限；`time` 必须是 `HH:MM`；`projects` 中的项目必须存在；`model_args` 不能包含工具 / shell / MCP 开启参数。热重载在下一次调度生效，运行中的综合不被切换。

触发优先级：显式 `gofer memory synthesize run --project <p> --day <day>` > 已有 schedule 触发 > 内置每日触发。内置触发不新建用户可见 schedule，避免污染 schedule 列表；显式 schedule 仍复用现有 `schedule` 能力和权限。

### 3.3 结构化材料

材料采用版本化 envelope，agent 看到的是 JSON / markdown 混合的只读输入，而不是任意数据库 dump：

```json
{
  "schema": "daily-memory-materials.v1",
  "project": "<project>",
  "day": "2026-10-28",
  "timezone": "+08:00",
  "items": [
    {
      "id": "m-001",
      "kind": "work_report",
      "occurred_at": "...",
      "title": "...",
      "text": "...",
      "source_ref": "job:<id>#report",
      "sensitivity": "normal"
    }
  ],
  "omitted": {"items": 3, "bytes": 1200, "reasons": ["secret", "budget"]}
}
```

材料白名单：

| 来源 | 默认字段 | 规则 |
|---|---|---|
| work item / journal | id、title、goal、status、next_step、blocker、最近 journal 摘要 | 不含完整会话 transcript；用户正文按条数和字符数截断 |
| job | job id、title、terminal status、report 的结构化段落、findings / reusable lessons | 不含 prompt、环境变量、命令 argv、原始日志和附件 |
| plan / todo | plan id、todo 状态、acceptance 摘要、conclusion、handoff 摘要 | 只取项目可见计划；不展开历史 handoff |
| memory doctor | finding slug、memory key、kind、summary、建议 action、现有 suggestion 状态 | 不发送 memory 全文；需要正文时只允许 server 生成的截断片段，并标为 `normal` |
| 现有 synthesis | 上一份摘要、候选的状态、source refs | 只取最近一份成功记录，防止自我回声 |
| work digest | 当日计数与已生成摘要 | 不重复发送同一 work item 的明细 |

去重优先级为 `source_ref`，其次为规范化文本 hash；同一事实来自多个来源时保留一条主记录并列出 `source_refs`。默认每条文本最多 1,000 rune、每类最多 50 条、总输入不超过配置上限。没有新材料时只更新 `last_checked_at`，不创建运行记录。

#### 3.3.1 输出 schema

agent 必须输出单个 JSON 对象：

```json
{
  "schema": "daily-memory-synthesis.v1",
  "summary": "一句话说明今天沉淀了什么",
  "highlights": ["..."],
  "open_questions": ["..."],
  "candidates": [
    {
      "kind": "note|rule|handoff",
      "title": "...",
      "summary": "...",
      "content": "...",
      "when_keywords": ["..."],
      "source_refs": ["job:<id>#lesson"]
    }
  ],
  "discarded": [
    {"reason": "duplicate|temporary|insufficient_evidence", "source_refs": ["..."]}
  ]
}
```

server 强制限制候选数量（默认最多 5）、候选正文长度、`source_refs` 必须来自输入、`kind` 必须为 `note|rule|handoff`，并拒绝包含疑似 secret 的输出。`summary` 是开场引用的唯一默认文本；`content` 只在详情或人工采纳预览中返回。

### 3.4 数据模型与存储

在 jobstore 中新增两张 additive 表，遵循 G057：基础 schema 不引用迁移后才存在的列，并为旧库提供迁移测试。

`memory_synthesis_runs`：

- `id`、`project_key`、`day`、`status`（running / succeeded / failed / skipped）；
- `input_digest`、`material_schema`、`material_counts_json`、`omitted_json`；
- `agent`、`model`、`output_digest`、`result_json`、`error`；
- `started_at`、`finished_at`、`last_checked_at`、`attempt`、`created_by`；
- 可选 `material_snapshot`，仅在 `keep_material_snapshot` 开启时写入已脱敏材料。

唯一约束：`(project_key, day, input_digest, status=succeeded)` 的逻辑去重由事务 / CAS 保证；同一天允许多个失败运行，但只允许一个 `running`。`result_json` 是版本化输出，不把候选复制成另一套 memory 表。

`memory_synthesis_candidates`：

- `id`、`run_id`、`project_key`、`day`、`kind`、`title`、`summary`、`content`、`when_keywords_json`；
- `source_refs_json`、`state`（pending / accepted / rejected / stale）、`decided_by`、`decided_at`、`note`。

候选表的状态机刻意与 `memory_candidates`、`memory_suggestions` 分开：前者是 job 报告中的可复用经验，后者是 doctor 对现有记忆的维护建议；daily synthesis 是跨来源的人工确认队列。采纳时调用现有 tracker / scoped memory 写路径，使用 CAS 和 `source: synthesis:<run-id>`，不直接写文件或绕过 scope 权限。

保留策略：默认保留 90 天的运行元数据和已决候选；过期结果不进入 prime，但可通过 `--history` 查询统计。删除 / 清理由既有 retention sweep 增加一个明确的表级步骤，不与 memory archive 混用。

### 3.5 Agent 综合模块

推荐复用 `work.summarizer_agent` 的 agent 解析和低成本参数默认值，但创建**一次性、只读、无工具**的 synthesis job；不复用 steward 的 resident 会话，不把 synthesis 结果混入 steward 的上下文状态。这样既避免新增必装 agent，也避免 resident 会话携带前一天上下文。

内部接口建议：

```go
type MaterialSource interface {
    Collect(ctx context.Context, project, day string) (Materials, error)
}
type Synthesizer interface {
    Run(ctx context.Context, req Request) (Output, Usage, error)
}
type Repository interface {
    BeginIfChanged(project, day, digest string) (Run, bool, error)
    Finish(runID string, result Result) error
}
```

`Synthesizer` adapter 使用现有 job admission / local runner / agent budget；提交的 `JobRequest` 标记为内部 actor `summarizer`，不允许 worker runner、shell、MCP、写权限和外部网络。agent 返回值先落在内存中校验，校验失败只记录错误摘要，不把原始异常全文展示给用户。

## 4. 接口：HTTP / CLI / MCP

### 4.1 HTTP

新增用户可见路由时必须同时登记 `internal/httpapi/authz_routes.go` 和 `internal/authz/catalog.go`，并运行 `go generate ./internal/httpapi/...` 更新权限参考文档。

推荐接口：

- `GET /v1/memory-syntheses?project=&day=&state=&limit=`：返回摘要、状态、digest、候选计数和时间；默认不返回材料快照 / 候选正文；
- `GET /v1/memory-syntheses/{id}`：按权限返回运行详情、来源引用、脱敏统计和候选；
- `POST /v1/memory-syntheses/run`：body `{project, day, force}`，触发一次；`force` 只能在人工权限下使用，仍受 daily limit 与并发 CAS 限制；
- `GET /v1/memory-synthesis-candidates?run_id=&project=&state=`：列候选；
- `POST /v1/memory-synthesis-candidates/{id}/accept`：body `{scope, scope_key, key, kind, summary}`，复用现有 memory 写入和 scope 校验；
- `POST /v1/memory-synthesis-candidates/{id}/reject`：只改变候选状态。

权限建议：

- 读取摘要 / 候选：现有 memory / today 的 read 权限；job credential 只能读取其 project 范围内的脱敏摘要，不能读其他项目或材料快照；
- 手工触发：新增 `memory.synthesis.run`，默认不可由 job session grant 授予，只授予用户 / steward（steward 仅能触发当前配置项目且不能 force）；
- 采纳 / 拒绝：新增 `memory.synthesis.decide`，仅 user / root，不能由 job credential、worker token 或 agent 自动执行；
- 输入材料中的 source ref 仍按项目过滤；跨项目管理员读取需现有 `others-resource` 权限。

### 4.2 CLI

命令放在现有 `memory` 资源组下，不新增顶级小工具命令：

```text
gofer memory synthesis show [--project <p>] [--day <YYYY-MM-DD>] [--json]
gofer memory synthesis ls [--project <p>] [--state pending|succeeded|failed|all]
gofer memory synthesis run --project <p> [--day <day>] [--force] [--wait]
gofer memory synthesis candidates [--run <id>] [--project <p>]
gofer memory synthesis accept <id> --key <k> [--scope project|global] [--kind rule|note|handoff] [--summary <text>]
gofer memory synthesis reject <id> [--reason <text>]
```

`--json` 返回稳定机器字段；人类输出只显示摘要、候选标题、来源数量和状态，不显示 secret scrub 前的任何内容。`--wait` 有明确超时，超时只返回运行 ID，不让 CLI 阻塞至 agent 自行结束。`-c/--config` 由公共 helper 绑定。

### 4.3 MCP

新增只读工具：

- `gofer_memory_synthesis`：按 project / day 获取摘要和候选标题；
- `gofer_memory_synthesis_candidates`：获取候选详情与 source refs；
- `gofer_memory_synthesis_run`：仅 steward / 人类控制面可用，普通 job credential 拒绝；
- `gofer_memory_synthesis_adopt` / `..._reject`：沿用 MCP 的“agent 可建议、不可替人确认”原则，默认禁止 job credential；如允许 steward 代提议，仍必须创建 pending 候选，不得直接写 memory。

MCP 描述中明确：daily synthesis 是建议，不是事实数据库；agent 需要通过 source ref 请求原始 brief / job 详情，而不是把摘要当作唯一事实来源。

## 5. 权限与安全

1. **材料最小化**：server 先取结构化字段，再做 allowlist；默认不取完整 prompt、shell、环境、token、邮件、附件、transcript。用户写入的文本按原样视作不可信数据，不执行其中的指令。
2. **秘密清理**：复用统一 secret scanner；命中 token / key / password / bearer / private key / env assignment 时替换为 `[redacted]`，并计入 `omitted.reasons`。输出再扫描一次，命中则整条候选丢弃并标记 `unsafe_output`。
3. **项目隔离**：材料查询、摘要读取、候选采纳都在 project scope 内；`global` 只能由明确的人类操作选择。source ref 不得成为绕过 scope 的读取句柄。
4. **执行隔离**：synthesis agent 使用 local runner、只读 project、无工具；不得把用户材料拼接为 shell 参数；内部 job 的 prompt 与结果不通过 webhook 或工作项通知扩散。
5. **审计**：运行记录保存触发者、agent、模型、digest、计费 / token 使用（若 adapter 提供）、状态变化和人工决定者；日志中不打印材料正文、prompt、结果 content 或 token。
6. **幂等与并发**：按 `(project, day, input_digest)` 去重；`force` 不能绕过并发 CAS、daily limit 或权限。server 重启后 `running` 记录按超时转 `failed`，不自动重复提交。
7. **隐私开关**：`keep_material_snapshot` 默认 false；即使开启也只存脱敏快照并遵循 retention。部署可将 synthesis 完全关闭，关闭后现有记忆 / digest / steward 行为不变。

## 6. 与现有功能的关系

- **每日摘要**：`work.digest` 继续是人类通知，内容和发送时机不改变。synthesis 默认安排在 digest 前，读取 digest 的结构化统计但不替代通知。若 digest 未开启，synthesis 仍可独立按自己的配置运行。
- **记忆建议 / findings**：`memory-findings` 继续由 `today.MemoryFindings` 产生；synthesis 可以引用 finding 和 pending action，但不自动调用 `SuggestMemory`。未来可把 synthesis 候选转换为现有 `memory-suggestions`，但应通过显式迁移 adapter，不在第一阶段复用 state。
- **memory kind 与过期**：采纳写入使用现有 `rule|note|handoff`、summary、when、TTL 校验；默认建议 `note`，只有 agent 给出多个 source ref 且材料包含重复验证时才允许提议 `rule`，仍需人工选择。
- **plan brief / 会话开场**：`brief` 增加一个可选的 `daily synthesis` section；`repo prime` 通过现有 `AppendPrimeSectionsIn` 追加一行摘要和 `gofer memory synthesis show` 提示，使用独立预算（建议 ≤600B），不足时整段省略而不是截断核心规则。开场引用必须带日期和 run ID，避免 agent 把昨天的摘要当成今天事实。
- **tracker prime**：synthesis 结果不写入 `.gofer/tracker/memories.jsonl`，不参与 tracker sync。人采纳后才经现有 scoped / tracker 写路径进入记忆；这样保留“自动生成现状不写进记忆”的原则。
- **schedule**：显式调度复用 `ScheduleRecord` 和现有 cron sweeper；内置每日触发作为 server 内部 loop，不暴露 trigger token，不创建外部 webhook。两者都必须以项目权限和 daily limit 为准。
- **steward**：steward review 可在 prompt 中看到“上一份 synthesis 的摘要 / 新候选数”，但不把 synthesis agent 变成 steward 的子代理。没有新材料时，既不唤醒 synthesis，也不因 synthesis 本身唤醒 steward；有新 pending 候选时，是否唤醒仍沿用现有 memory hygiene 的 signature 去重和配额。
- **现有 summarizer**：复用 `work.summarizer_agent` 的默认 agent / cheap-model 参数解析，但新增独立配置覆盖；运行 actor 仍区分 `summarizer` 与 `steward`，便于审计和权限判断。

## 7. 分阶段

### P1：材料与持久化（无模型默认行为变化）

- 新增材料 envelope、来源 adapter、脱敏 / 截断、digest 和 jobstore 表；
- 新增 `GET` 查询接口、CLI `show/ls`，迁移测试和 retention；
- 用固定 fake synthesizer 验证 CAS、重复运行、旧库升级和项目隔离；
- 先不挂每日调度，不在 prime / brief 引用。

### P2：一次性综合与人工候选

- 接入只读 local summarizer adapter，严格 JSON schema 校验；
- 增加 `run`、候选查询、accept / reject HTTP / CLI / MCP；
- 采纳复用 tracker / scoped memory 的现有写入、summary、kind、TTL、scope 权限；
- 默认 `enabled: false`，由显式 run 试用；补充成本与 secret 观测。

### P3：每日调度与开场引用

- 增加内置每日触发，使用 server 时区、无新材料短路、daily limit 和退避；
- `repo prime`、issue / plan brief 增加摘要引用和“查看详情”命令；
- steward review 只显示候选计数 / 新摘要，不直接采纳；
- 文档与 gofer-usage references 同步，说明这是可选建议，不是自动记忆写入。

### P4：可选增强

- 对不同材料源增加可配置权重和更细的 dedupe；
- 在有明确人工确认后，将 synthesis 候选桥接成既有 memory suggestion；
- 增加按项目 / 全局的历史比较、质量反馈和模型成本报表；这些不作为 P1–P3 的前置依赖。

## 8. 测试与守护

- **schema / migration**：新建库和上一个发布版本 schema 都能打开；`result_json`、candidate state、唯一约束、旧的 work digest / memory tables 不受影响；
- **材料 golden**：固定 work / job / plan / findings fixture，验证排序、去重、source ref、长度上限、时区窗口和无新材料判定；用户正文中的命令式文本只作为数据，不被执行；
- **隐私回归**：token、authorization、private key、环境变量、完整 argv、邮件正文、附件路径分别命中 scrub；输入 / 输出 / 日志均不能出现原 secret；
- **agent adapter**：fake agent 返回合法 JSON、未知 source ref、超长字段、错误 JSON、含 secret 输出；只有合法结果可进入 succeeded；
- **成本 / 幂等**：同 digest 不启动第二个 agent；force 受限；失败按 retry_after；每日上限、并发 CAS、重启恢复和超时都有测试；
- **权限**：跨项目 GET 被过滤；job credential / worker token 不能 force、accept、reject 或读取 snapshot；steward 只能当前项目；采纳写入走现有 scope / caller ceiling；
- **开场预算**：prime / brief 在有、无、过期、超预算 synthesis 时均不超过既有上限，旧部署在功能关闭时字节级不变；
- **生命周期**：内部 job、synthesis loop、HTTP server、测试 store 按 G051–G053 顺序停止和等待；不以终态代替 `jobs.Wait`；
- **全量守护**：路由声明与权限目录、生成的权限参考文档、CLI help、MCP 工具清单、gofer-usage references 必须同步。新增 schema 遵守 G057。

## 9. 风险

- **模型把临时状态写成规则**：默认生成 `note`，候选必须带 source refs 和人工采纳；规则候选需要更高证据门槛，且不自动写入。
- **材料偏差 / 遗漏**：schema 中记录 omitted 计数和源类型；UI 明确“这是有界摘要”，详情仍回到原始 work / job / plan / finding。
- **重复和自我回声**：input digest、source ref 去重、只取上一份摘要而不是完整历史，并在 prompt 中禁止复述上一份 synthesis。
- **成本失控**：默认关闭、无变化短路、每项目每天一次、低成本模型默认、输入输出硬上限、超时和退避；后台 job 仍有预算和 runner admission。
- **隐私泄露**：双向 scrub、默认不保存快照、scope 过滤、日志只写 digest / 计数；但无法保证用户正文中所有秘密都能被模式识别，需把“禁止把秘密写进项目内容”作为运维提示而非唯一防线。
- **开场污染**：摘要独立小预算、日期 / run ID、过期结果不引用；功能关闭时不改变既有 prime。
- **和现有队列概念重叠**：P1 明确三种队列的职责；若后续桥接，必须保留原始 state 和审计记录，不能把表直接合并。
- **重复调度**：内置 loop 与显式 schedule 可能同日触发；统一由 `(project, day, digest)` CAS 去重，重复触发只返回已有 run。

## 10. 待确认项

1. **默认开关：推荐默认关闭，先显式启用。**
   - 理由：综合会产生模型成本并处理跨来源材料；关闭时完全不改变现有部署，便于先用 P1/P2 验证材料质量和隐私。
   - 备选：随 `work.digest` 默认开启，体验更自动但升级后成本和数据处理语义不可见。

2. **综合 agent：推荐复用 `work.summarizer_agent` 的配置和 cheap-model 默认，但创建独立一次性 job。**
   - 理由：不增加新的必装 agent，也不把 resident steward 的历史上下文带入；独立 actor / job 便于预算和审计。
   - 备选：直接由 steward resident session 综合，调用少但上下文会累积，失败和权限边界更难隔离。

3. **结果存储：推荐 server jobstore 的运行表 + 候选表，结果不直接进入 tracker。**
   - 理由：需要按项目 / 日期 / digest 幂等、失败重试、审计和 retention；不污染 tracker sync，人工采纳仍走成熟 memory 写路径。
   - 备选：直接写 `handoff` memory，结构简单但会混淆自动现状与人工确认记忆，且难以撤回模型误判。

4. **开场引用：推荐只放摘要 + 日期 + `show` 命令，默认不放候选正文。**
   - 理由：控制 8 KiB prime / brief 预算，避免旧摘要变成事实；需要时由 agent / 人通过 source ref 查询详情。
   - 备选：将候选正文全文注入开场，信息更完整但容易挤掉规则、增加隐私暴露和 token 成本。

5. **材料快照：推荐默认不保存，只保存 digest、计数、source refs 和 scrub 统计。**
   - 理由：降低敏感信息落库风险；失败排查可依赖结构化计数和原始来源。
   - 备选：保存脱敏快照便于复现，但 retention、备份和删除义务更重；可作为显式运维开关。

6. **触发方式：推荐先支持显式 run，再接入内置每日触发；显式 schedule 作为高级用法。**
   - 理由：先验证结果质量，避免 P1/P2 阶段后台悄悄启动模型；接入时可复用已有 schedule 语义而不扩大核心 loop。
   - 备选：第一版直接绑定 `work.digest`，实现更短但无法分别关闭通知与综合，也难以观察成本。

7. **候选队列：推荐保持独立，不立即桥接 `memory_candidates` / `memory_suggestions`。**
   - 理由：三者来源、决策者和审计语义不同；独立状态机可先验证质量，再按实际使用设计转换规则。
   - 备选：统一到 `memory-suggestions`，表更少但会丢失“跨来源综合”的 run 级追踪。

8. **跨项目范围：推荐每次只综合一个 project，global 只作为输入而不是输出默认 scope。**
   - 理由：项目是权限和开场引用的自然隔离单元，避免把一个项目的事实写入全局记忆；跨项目规则应由人明确选择 global。
   - 备选：server 每天做全局综合，成本和隐私范围都更大，且难以解释来源。
