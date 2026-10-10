# 接手包 + 知识回流 + 方案规则（gofer-3nxa.7 / .1 / .2 / .5）

> 2026-10-10 · epic gofer-3nxa · plan plan-20261010-134446-fc0d529d

## 目标

agent 新会话能顺畅接手任何功能、实施更顺：一条命令拿齐接手所需上下文；注入的知识会被纠错、会随交付增长；方案写成可直接执行的 todo 链。

## 基线：冷启动接手演练（2026-10-10）

冷启动 agent 接手 gofer-3nxa.6 并出实施方案：约 10 次（合并后的）工具调用。卡点：

1. issue 不链接设计稿，靠 grep 反查到 `docs/design/...` 的「实施记录」。
2. `repo prime` 里没有该 issue 的任何上下文（P3 挤出 ready 前 10）；「进行中 plan 的交接说明」为空。
3. 验证约定、G045 等只在 `memory show` 全文里，prime 只给摘要行。
4. 数据流（采集 → 落库 → worker 回传 → 展示）要读多个文件拼出来；相关提交要 `git log --grep`。
5. 「Go 与 TS 双份实现须同步」这类陷阱只写在代码注释里。

复测（T8）用同一场景，对比调用次数与卡点。

## 一、接手包（.7）

> 状态：已实施（分支 z-brief），差异见文末「实施记录 · 一 / 二」。

### 命令

- `gofer issue brief <id> [--max-lines N] [--json]`
- `gofer plan brief <plan-id> [--json]`：plan 字段、todo 列表（状态 / 依赖 / 验收标准 / 关联 job 结论）、交接说明、关联 issue 的精简 brief。
- MCP：`gofer_issue_brief`、`gofer_plan_brief`（同一份输出）。
- 汇编逻辑放独立包 `internal/brief`（G021：commands / mcpserver 只做绑定转发）；输入是仓库 tracker + git + docs + 可选的 server client（取 job / plan / 评论，连不上时跳过该节并注明）。

### issue brief 内容（按序，每节有内容才输出，空节写「无」的只有验收标准）

1. **issue**：标题、状态、优先级、标签、描述、design、验收标准（缺失写「无验收标准」）、评论（最近 5 条）。
2. **上下文树**：父 issue 及兄弟（状态 + 已关闭的 close_reason 一行），子 issue，依赖 / 被依赖、discovered-from。
3. **设计稿**：`docs/**/*.md` 中提到本 issue id 或其父 id 的文件；列路径 + 命中行所在的小节标题与该小节前若干行（默认每文件 ≤ 15 行）。issue 的 description / design / 评论里出现的 `docs/…md` 路径也列入。
4. **相关提交**：`git log --grep <id>`（含父 id，及兄弟 close_reason 里提到的 sha），列 `sha 日期 subject`；并汇总这些提交触及最多的文件（前 10）作为「代码入口」。
5. **相关 job / plan**（需 server）：issue_id 关联的 job（状态、标题、评审评论首行）、包含该 issue id 的 plan / todo。
6. **适用记忆**：kind=rule 的记忆（全文，含全局 / 项目作用域且对当前 agent 生效的）；when-paths 命中第 4 节「代码入口」的记忆；关键词（issue 标签 + 标题词）命中的 note。被 flag 的记忆带「待复核」标记（见 .1）。
7. **接手提示**：固定尾注——本地提交 / push 规则来自 rule 记忆；验证命令以 rule 记忆为准；建议先 `gofer issue update <id> --claim`。

输出总长默认上限 400 行（`--max-lines`），超出时按节截断并标注被截断的节与查看命令。

### prime 配合

- 「进行中 plan」节：每个 plan 列出 doing / ready 的 todo 与其关联 issue id（todo 标题或验收标准里出现的 issue id），并提示 `gofer plan brief <id>`。
- prime 末尾加一行入口提示：接手 issue 用 `gofer issue brief <id>`。
- 设计稿约定（写进 skill）：设计稿头部写明 issue id；实施记录写进设计稿；issue 的 design 字段写设计稿路径。

## 二、记忆过时反馈（.1）

> 状态：已实施（分支 z-brief），差异见文末「实施记录 · 一 / 二」。

- `gofer memory flag <key> --reason "<为何不符>" [--global | --project <p>]`；`gofer memory unflag <key>`。MCP `gofer_memory_flag`。
- 存储：`MemoryMeta.Flags []MemoryFlag{At, By, Job, Reason}`（最多保留 5 条，新的在前）。仓库记忆走 tracker jsonl；作用域记忆走 server（与现有 scoped 写路径一致）。
- 修改记忆正文（`memory set` 改 content）视为已复核，自动清空 flags。
- 注入：prime / 命中注入里被 flag 的记忆前缀「⚠ 待复核（<最近原因>）」；rule 仍给全文（不能因一次 flag 就丢规则），note 只给索引行。
- `memory doctor` 新增 `flagged` 检查项，列出 key、次数、最近原因、来源 job。
- 提示词：在 prime 规则区和「交付约定」节各加一句——发现注入的记忆 / 规则与实际不符时 `gofer memory flag <key> --reason …`，不要静默绕过。

## 三、交付后知识提炼（.2）

- 项目配置 `knowledge_capture: auto|on|off`（默认 auto，与 `scope_discipline` 同口径：auto 覆盖 plan todo / review / 带验收标准或 scope 的 job）。开启时「交付约定」节追加：跨任务可复用的经验（踩坑、约定、验证技巧）写进汇报末尾「## 可复用经验」小节，每条一行；只写以后其他任务也用得上的，不写本次业务细节；没有就省略。
- 解析：job 终态时服务端用与「发现但不碰」同一套小节解析（`job.ParseReportSection(report, titles…)` 泛化现有 `ParseFindings`）抽出条目，写入新表 `memory_candidates(id, job_id, project_key, text, status pending|accepted|rejected, memory_key, created_at, decided_at, decided_by)`。
- 处理：
  - CLI `gofer memory candidates [-p <project>] [--all]`、`gofer memory accept <cand-id> --key <k> [--kind rule|note] [--summary …] [--global | --project <p>]`、`gofer memory reject <cand-id>`。
  - 接受即写入**项目作用域**记忆（默认 job 的项目；`--global` 可改），source=`job:<id>`；拒绝只改状态。
  - Web：`ReviewPanel` 新增「经验」页签（有候选时，带计数），每条接受（填 key / kind，默认 note）/ 拒绝。
- 不自动入库；候选 30 天未处理在 `memory doctor` 中提示。

## 四、方案规则（.5）

- 规则文本（单一来源 `internal/job/workflow` 或 template 内常量，skill 引用同一份）：
  1. 步骤按依赖自底向上（存储 / 模型 → 协议 / 接口 → 业务 → 调用方 / 前端），任何步骤不得使用后续步骤才创建的东西；
  2. 优先垂直切片，每片完成后可编译、可验证；
  3. 单步预计改动 > 5 个文件、跨 2 个以上子系统、或标题含「并且 / 同时」，拆开；
  4. 每 2~3 步一个检查点（可执行的验证命令）；
  5. 对跨模块、不可逆、并发 / 幂等 / 不变量类关键决策做一次「假设作者过度自信」自审，写出最可能错在哪；
  6. 不确定、需要人拍板的点收进「待确认问题」，不进入实现。
- 输出格式：方案末尾附一个 ```` ```gofer-todos ```` 围栏块（YAML 列表：`title`、`after`（引用前序 title 或序号）、`acceptance`、`scope`、`check`（检查点命令，生成 exec 复核项））。
- `plan-implement` 内置模板的 planner 提示词注入上述规则与格式。
- `gofer plan import <plan-id> (--from-job <job-id> | -f <file>) [--assign <agent>] [--dry-run]`：解析 `gofer-todos` 块，按序建 todo（`after` 串联，`check` 生成 `--assign exec --cmd` 复核项），`--dry-run` 只打印。

## 不做（本期）

- server 端新建仓库 tracker issue；接手包的语义检索（只用 id / 关键词 / 路径匹配）。
- 记忆置信度数值模型（flag 计数足够）。

## 验证

- 单测：brief 各节组装（临时仓库 + 假 docs + 假 server）、memory flag / unflag / 正文修改清 flag / 注入前缀 / doctor、候选解析与表读写 / 接受写入作用域记忆、gofer-todos 解析与 import dry-run。
- 实测：`gofer issue brief gofer-3nxa.6` 人工审阅；T8 复测演练；带 `knowledge_capture` 的 todo job 产生候选并接受。
- Skill（G045）：SKILL.md 新增「接手」小节（brief 为入口）、记忆 flag、经验候选、方案规则与 plan import；commands.md 对应节。

## 实施记录 · 一 / 二（gofer-3nxa.7 / .1，分支 z-brief）

**二、memory flag**

- 存储按设计：`MemoryMeta.Flags`（`at / by / job / reason`，新的在前，最多 5 条）。flag / unflag **不改 `updated_at`**（flag 是对正文的报告，不是改写；否则「N 天前」与 note 90 天陈旧判断会被刷新）。
- 「改正文即复核」覆盖三条写路径：`tracker.ApplyMemoryPatch`（`memory set`、作用域记忆 POST / PUT 都经它）、server 镜像编辑 `PatchTrackerMemory`（web 改正文时删掉 `flags`）。只改摘要 / 标签 / kind 不清。仓库记忆的 flags 在 `repo sync` 三方合并里按整体值合并（同 `when`）。
- 作用域记忆新增专用接口 `POST|DELETE /v1/memories/{scope}/{scope_key}/{key}/flag`（只改 `meta_json`）。原因：作用域记忆的其它写接口对 job 凭证一律 403，而 flag 的主要调用方正是 job 内的 agent。job 凭证可以 flag（记为该 job 自己的 id，忽略 body 里的 job），但只能 flag 全局或**自己项目**的记忆；unflag 只允许人。该路由已加入 job 凭证写白名单。
- MCP `gofer_memory_flag {key, reason, scope?, scope_key?}`：不给 scope 时写 MCP 进程 cwd 所在仓库的 tracker。**没有 MCP unflag**（清除是人的复核）。
- 注入：prime 规则 / 索引、作用域记忆段、派发 job 的规则（`tracker-prime`）、提问 / 执行命令前的命中注入，被 flag 的记忆前缀「⚠ 待复核（最近原因）」，rule 仍全文。prime 规则段开头与派发 job 规则里各加一行 flag 提示（`tracker.MemoryFlagHint`）；「交付约定」节的同一句由 .2 / .5 所在分支补（`internal/job/prompt_sections.go` 归该分支）。
- doctor 新增 slug `flagged`（次数 · 最近原因 · job · by · 日期），排在报告首位；server 端 doctor 同样产出，但不对应管家整理动作。

**一、接手包**

- 新包 `internal/brief`（与 `internal/focusremote` 同层：commands / mcpserver 只绑定输入与打印）。server 通过窄接口 `brief.Client` 读取；`brief.Connect` 先探测一次 `/v1/meta`，每个请求 3 秒上限，连不上时 server 节写「未连接 server，本节跳过（原因）」。
- 「相关 job」：server 没有按 issue 过滤 job 的接口，取本项目最近 200 个 job 按 `issue_id`（及 `tracker:issue:<id>` 标签）过滤；「相关 plan」：open plan 中最近更新的 8 个取详情，看标题 / 描述 / todo（标题、验收、备注）是否提到本 id。
- id 匹配按整词：`gofer-x` 不匹配 `gofer-x.3`，`gofer-x.1` 不匹配 `gofer-x.10`。plan 里 todo 标题常写 `.1 / .7` 简写：plan 标题 / 描述提到的 issue 若同属一个父，`.N` 按该父展开。
- 「代码入口」排除 `docs/`、测试文件与 `.gofer/`；merge 提交按第一父比较取文件。
- 「适用记忆」：带 `when` 的 rule 只在 `when.paths` 命中代码入口或 `when.keywords` / 标签 / 标题词命中时给全文（与 prime 的「cwd 命中才全文」同口径）；note 只列路径 / 关键字命中的索引行（≤10）。
- `--max-lines` 截断：按节顺序分配，后面每节至少保留标题，末节「接手提示」不截；被截的节写「[本节截断 N 行：`查看命令`]」。`--json` 输出 `{kind, id, sections[{title, lines, note, more, truncated}]}`；MCP 返回 `{kind, id, text}`。
- prime：「进行中 plan」节（标题由「进行中 plan 的交接说明」改为「进行中 plan」）对每个 open plan（最多 3 个）列 plan 行 + `gofer plan brief` 提示、doing / ready todo 及其 issue id，再附交接说明（原先没有交接说明的 plan 不出现）。接手入口提示放在**本地 prime 的末行**（全局 / 项目记忆与进行中 plan 这两段 server 内容仍在其后追加；放到整个输出最末需要给 server 段另留预算，本期不做）。
## 实施记录（2026-10-10，§三 / §四，分支 z-know）

### §三 交付后知识提炼（.2）

- 「## 交付约定」节在 `scope_discipline` 或 `knowledge_capture` 任一生效时追加（`internal/job/prompt_sections.go`）：前者给范围纪律两行，后者给「## 可复用经验」一行，两者都带「发现注入的记忆或规则与实际不符时，用 `gofer memory flag <key> --reason …` 上报」一行（命令由 .1 提供）；有 scope 时范围行仍在最后。`--no-scope-discipline` 关掉整节（含知识提炼）。
- 是否提炼记在 `JobRequest.KnowledgeCapture`（Submit 决定、覆盖调用方的值，进 request_json，`JobResult.knowledge_capture` 回显）；续接 job 继承源 job 的值，worker 侧副本没有这个字段（由 hub 提炼）；rerun 的 prompt 已带该节时不再追加，但照样重新决定是否提炼。
- 提炼点：`finish` 里与 `linkTodoOutcome` 同处、needs_review 分支之前（`captureKnowledge`），只处理 done / needs_review，失败 / 取消的 job 不提炼；读 stdout 尾部 64KB，`job.ParseReportSection`（`ParseFindings` 改为它的一个调用，行为不变，Go / TS 共用规则未变，前端不需要解析经验小节）。
- 表 `memory_candidates`（additive，`UNIQUE(job_id, text)` 保证同一 job 重复经过终态路径只记一次）；字段同设计，状态 pending / accepted / rejected。
- 接受：先对候选做 pending→accepted 的比较后写（并发接受只有一个成功），再经 `PutScopedMemoryPatch` 写作用域记忆（kind 默认 note、source `job:<id>`、按 `ValidateMemoryForWrite` 校验 summary）；写失败把候选退回 pending。目标作用域已有同名 key 时拒绝（409），不覆盖。
- 入口：HTTP `GET /v1/memory-candidates`、`POST /v1/memory-candidates/{id}/accept|reject`（裁决只接受人凭据，job 凭据只读，worker token 403）；CLI `gofer memory candidates|accept|reject`；Web 验收面板「经验」页签（`MemoryCandidatesTab.vue`）。
- 与设计的差异：MCP 工具叫 `gofer_memory_candidates` / `gofer_memory_candidate_adopt` / `gofer_memory_candidate_reject`——MCP 面有「不出现 accept 工具」的不变量（`TestNoAcceptJobTool`），所以接受在 MCP 上叫 adopt。
- 未做（延后）：「候选 30 天未处理在 `memory doctor` 中提示」——`memory doctor` 属 .1 的改动范围（`internal/tracker`），等 .1 合入后再接。

### §四 方案规则（.5）

- 单一来源 `internal/job/plan_rules.go`：`PlanRules`（六条原文）、`PlanTodosFormat`（gofer-todos 格式说明 + 示例）、`PlannerGuidance`；`plan-implement` 内置模板的 planner prompt = `Plan: <task>` + `PlannerGuidance`。skill（`references/commands.md`「方案规则与 plan import」）逐字引用 `PlanRules`，`TestSkillQuotesPlanRules` 防漂移。
- 解析在 `internal/job`（`ExtractPlanTodosBlock` / `ParsePlanTodos` / `BuildTodoImport`），命令层只做读文件 / 取 job 汇报、拆 argv、调 `AddTodo`。取**最后一个** gofer-todos 块；未知字段报错。
- `after` 语义（设计未定的细节）：省略 = 依赖上一步，`[]` = 无依赖；引用可写标题或从 1 起的序号；只能指向前序步骤；重名标题被引用时报错。`check` 生成 exec 项「检查点：<标题>」，后续引用该步骤的项等这个检查点。`--assign exec` 被拒。
- `--dry-run` 不连 server（`-f` 时完全离线）；`--from-job` 读 stdout 尾部 256KB。建到一半失败时报告已建数量后停止，不回滚。

## 复测：接手演练（2026-10-10，v0.132.0）

同场景（冷启动 agent 接手 gofer-3nxa.6，sonnet · medium，同一任务书）：6 次工具调用（基线约 10 次），`gofer issue brief` 一次给齐上下文树、设计稿、相关提交、适用记忆与验证约定。注意：issue 上已有基线演练留下的方案评论，本次比真冷启动有利。

仍缺（后续候选）：
- brief 中评论只保留前几行，「已有实施方案」类长评论应完整或置顶展示；
- issue 缺验收标准时 brief 只写「无」，可提示补写；
- 关键符号与行号清单（目前只有「提交触及最多的文件」）；
- 该 issue 专属的验证命令（目前只有通用 rule）。
