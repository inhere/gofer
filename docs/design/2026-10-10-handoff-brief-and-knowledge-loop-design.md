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
