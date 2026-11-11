<!-- template_id: plan; template_version: 1.3.0 -->
# 第 1 批：todo 按交付完成、长 job 记录按回合查看 实施计划

> 状态：Draft 0.1 / 待评审
> issue：gofer-94yq、gofer-mmh0（含 gofer-tykh）、gofer-gqap、gofer-8hlc、gofer-juo0、gofer-ehvd、gofer-r20b、gofer-5x85

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-11 | Claude（gofer 监督会话） | 初稿 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：①`complete_on=merge` 的 plan 中，worktree 交付的 todo 在交付落地后才完成、后续 todo 才派发，所有 plan 在续跑进行中不提前完成；②`job transcript` 按回合分页读取 claude / suga / ACP job 的完整记录。Scope freeze：只实施设计 Draft 0.3（已批准，Q1–Q6=A）与下文「对设计的细化」。Review budget：两轴独立各一轮，有 `CORE_BLOCKING` 才继续。停止条件：两轴无开放 `CORE_BLOCKING`，停在计划 Gate。

## 目标与完成定义

- 设计 §1 表每行行为成立；`job` 模式行为不变（含 `continue_from` 与分支基线回归）；续跑进行中与续跑提交失败两种情况 todo 结局正确；「待你收尾」卡片按交付状态出现与消失。
- claude / suga / ACP 本机 job 写出 `turns.jsonl`（脱敏、上限）；`job transcript` 的 CLI / HTTP / MCP 可分页取完任意大小的回合；多回合 suga job 能看到每回合正文。
- §3 Quick 与 §4 小改完成；文档同步；单测、真实验收、反向验证、gofer-verify 四路通过；合并、关 issue。

## 范围、排除项与授权

范围：设计 §1–§4。排除：设计「非目标」；`docs/capabilities.md` 随发版更新。

外部动作适用性：host_or_non_offline_action=REQUIRED （建 / 删主机 worktree、主机 web 构建、主机临时 server 真实验收、gofer-verify 为主机 job；push 与发版不在本计划内）。

授权：本计划批准后，按功能点本地提交、建 worktree、派子 agent、跑主机 job 与临时 server。push 与发版另行请示。设计批准后把 gofer-ohf7 的需求发给 suga 会话（会话消息，属设计已列的跨项目动作）。

## 输入与批准证据

- 设计：`docs/design/2026-11-11-todo-completion-and-job-transcript-design.md` Draft 0.3。用户 2026-11-11：「设计 Q1–Q6 按推荐，批准进入计划」。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | plan 字段与接口 | `Plan` 的 `leader` / `notify` 字段路径（jobstore、`plan set`、plan HTTP、MCP） | 是 | 照同一路径加 `complete_on` | OWNER_EXTENSION | 无该字段 | additive 迁移，G057 升级测试 |
| CAP-02 | todo 完成判定 | `linkTodoOutcome`、`markMergeTodoDone`、`recordDelivery` | 是 | 判定抽到新文件，原处只调 | OWNER_EXTENSION | 现判定只看 job 状态 | 单一判定函数，三处调用 |
| CAP-03 | 注意力卡片 | `internal/today` 卡片生成、`internal/inbox/waiting.go` 分类 | 是 | 新卡片 kind 与类别 | OWNER_EXTENSION | 无「待合并」 | 与「待你验收」同处 |
| CAP-04 | 回合记录写入 | `internal/runner/ndjsonfilter`（过滤器见全部行）、ACP handler `toolStatus` | 是 | 新写入器与累积器 | OWNER_EXTENSION | 投影器丢正文 / suga delta | 行类型只在 ndjsonfilter 定义 |
| CAP-05 | 安全拼路径 | `internal/store/safejoin.go` `SafeJoinUnder` | 是 | 直接调用 | DIRECT_REUSE | none | 不另写 |
| CAP-06 | 脱敏 | `internal/job/secrets.go` `secretMasker` | 是 | 抽按值替换的纯函数供两处用 | OWNER_EXTENSION | `mask` 是流式方法 | 同一组 secret 值 |
| CAP-07 | 读接口与权限 | `/logs/stdout` 的 `InHandler(permDeny, PermJobReadOthers)`、mcpserver 工具注册 | 是 | 新路由与工具 | OWNER_EXTENSION | 无转录接口 | 权限文档生成 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| NONE | none | none |

## 对设计的细化

| # | 问题 | 定案 |
|---|---|---|
| R1 | ACP 写入器的打开点（P1） | 不在 `captureNDJSON`（ACP 走不到）。在 job 层组装 `runner.Request` 处，仅本机 runner、且 agent 为 ACP 时打开 `turns.jsonl`（文件名常量 `store.TurnsFile`），赋给 `Request.Turns`；ndjson 类在 `captureNDJSON` 打开同一常量文件。两处共用一个 `openTurnsWriter(entry, jobID)` helper（新文件 `internal/job/turns_writer.go`），负责脱敏值注入与关闭 |
| R2 | 脱敏顺序（P2） | 回合累积完成后：先对整段文本脱敏（`maskSecrets(text, values) string`，从 `secretMasker` 抽出的纯函数，`secrets.go` 原方法改为调用它，行为不变），再截断到 256KB，再 JSON 编码写行。`stdout.raw.log` 不脱敏是现有问题，T9 另立 issue，不在本计划修 |
| R3 | 游标偏移（P3①） | `offset` 为回合正文内的字节偏移，切片时向前对齐到 UTF-8 字符边界（`utf8.RuneStart`），`next.offset` 为对齐后的位置 |
| R4 | 合并成功但 todo 写失败（P3②） | `markMergeTodoDone` 写库失败时除 warning 外记事件 `todo.complete_failed{todo_id, job_id, err}`；卡片判定在「已丢弃」之前先查该 job 是否有 merged 记录（`WorktreeStatus.Merged` 或该事件），有则显示「已合并，待标记完成」（动作 `plan set-todo done`）。T1 加此组合用例 |
| R5 | 续跑提交失败补调（N1） | `execute.go:939-944` 分支内一行：`if guard == guardResume && !autoResumed { s.linkTodoOutcome(snap) }`（`linkTodoOutcome` 增加可选的 guard 参数时用零值） |
| R6 | 回合方言登记 | `builtinNDJSONDef` 加 `turns: "claude" | "suga" | ""`；`NDJSONProjectorFor` 旁新增 `NDJSONTurnsFor(agent)`；空值不写 |
| R7 | 卡片 kind 与类别 | today 卡片 kind `todo_awaiting_merge`；inbox 类别 `awaiting_merge`，Rank 在 `awaiting_review` 之后、`plan_stuck` 之前；默认栏「要处理」 |

## 前置检查与 fail-closed 条件

workspace baseline：主机建 worktree `.worktrees/b1`（分支 `feat/b1-todo-transcript`），`.git` 改相对 gitdir；`web/node_modules` 链接主 checkout；Go 命令带 `GOWORK=off`，`GOCACHE` / `TMPDIR` 在仓库之外。

fail-closed（出现即停，回报监督会话）：

- 基线 `go test ./internal/job ./internal/jobstore ./internal/runner/... ./internal/today ./internal/inbox -count=1` 在未改动时失败。
- 需要改 worker 协议（回传 `turns.jsonl`）、改 `todo_done` wire 语义、或新增设计未列的权限。

## 波次与依赖

```
W0  T0 前置（Claude）
W1  T1 完成时机（sonnet）‖ T3 回合写入（sonnet，另一 agent）
W2  T2 收尾卡片（sonnet，接 T1）‖ T4 转录读取（sonnet，接 T3）‖ T6 小改 §4 与 juo0（sonnet，接 T1 的 agent）
W3  T5 web（suga）‖ T7 Quick 文案 gqap / 8hlc（suga）
W4  T8 文档（suga，依赖 T1–T7 已提交）
W5  T9 真实验收、反向验证、全量与收尾（Claude）
```

两个 sonnet agent 分别负责 §1 链（T1→T2→T6）与 §2 链（T3→T4），文件不重叠；同一 worktree 内按任务各自提交、只暂存自己的文件。

## 任务

### T0 前置

- 执行方：Claude。动作：建 worktree；基线测试；任务书定稿（R1–R7、约束）。完成标准：基线绿。

### T1 完成时机

- 执行方：sonnet（状态机与数据）。
- 文件：`internal/jobstore/plans.go` 与迁移、`internal/job/todo_completion.go`（新）、`todolink.go`、`branch_delivery.go`（`recordDelivery` 一处）、`worktree.go`（`markMergeTodoDone` 与 rm 各一处调用）、`execute.go`（R5 一行、守卫传参）、`internal/commands/plan.go`、`internal/httpapi/plan_handler.go`、`internal/mcpserver`（plan 工具）、`internal/jobstore/testdata` 升级测试，以及测试。
- 动作：设计 §1 全部与 R4、R5。
- 验证：设计验收 §1 全部用例（含 `job` 模式 `continue_from` / 分支基线回归、R4 组合、`complete_on` 非法值 400、迁移升级）；`go test ./internal/jobstore ./internal/job ./internal/httpapi ./internal/commands ./internal/mcpserver -count=1`。

### T2 收尾卡片

- 执行方：sonnet。文件：`internal/today`（新卡片生成，新文件）、`internal/inbox/waiting.go`、服务端 i18n 文案。动作：R7 与设计 §1 卡片条件。验证：卡片出现 / pending 不出 / 合并后消失 / 已丢弃 / R4 显示；`go test ./internal/today ./internal/inbox -count=1`。

### T3 回合写入

- 执行方：sonnet（执行链路、脱敏）。
- 文件：`internal/runner/ndjsonfilter`（`TurnRecord`、写入器、累积器，新文件）、`internal/runner/runner.go`（`Request.Turns`）、`internal/runner/acp`（回合缓冲新文件 + `runner.go` 调用行）、`internal/agent/registry.go`（R6）、`internal/job/turns_writer.go`（新，R1）、`internal/job/ndjson.go` 与组装 `runner.Request` 处各一行、`internal/job/secrets.go`（R2 抽函数）、`internal/store`（常量）。
- 验证：claude、suga（含 `retry{restart}`）、ACP（回合结束写、正文非空）写入；未登记方言不写；上限截断；注入 secret 后只出现掩码（含跨两条 delta 的 secret）；`go test ./internal/runner/... ./internal/job ./internal/agent -count=1`。

### T4 转录读取

- 执行方：sonnet。文件：`internal/job/transcript.go`（新）、`internal/commands/job_transcript.go`（新）、`internal/httpapi`（handler 与 `routeDecls`）、`internal/mcpserver`（`gofer_get_job_transcript`）、`internal/client`、权限文档生成。
- 验证：参数互斥 400、上下界、search 限长；大于 64KB 回合经游标取完（含多字节字符边界）；回退分页；`SafeJoinUnder` 越界拒绝；他人 job 403/404 与日志一致；CLI 真实分发与 `--out`；新 `*Opts` 登记 flag 快照；MCP 工具；`go generate ./internal/httpapi/...` 后 `TestAuthzReferenceIsUpToDate` 通过。

### T5 web

- 执行方：suga（gpt）。文件：计划页 `complete_on` 显示与设置、今天 / 收件箱新卡片 kind 的文案与动作、`web/src/utils/eventMeta.ts`（`job.fallback_skipped`）、i18n。验证：主机 `npx vue-tsc --noEmit && npx vitest run && npx vite build`。

### T6 小改 §4 与 juo0

- 执行方：sonnet（接 T1 的 agent）。动作：设计 §4 三项（ehvd 事件、r20b 校验顺序、5x85 rerun 带 `WorktreeFrom`）与 §3 juo0。验证：各自 focused test；`go test ./internal/job ./internal/httpapi -count=1`。

### T7 Quick 文案

- 执行方：suga（gpt）。动作：设计 §3 gqap（三个续跑模板 zh-CN / en 与 `fallbackPrompt`）、8hlc（命令、两处测试、golden、tracker.md）。验证：`go test ./internal/i18n/... ./internal/brief ./internal/job -run 'Prompt|Refocus|Fallback' -count=1`。

### T8 文档

- 执行方：suga（gpt）。文件：设计验收「文档同步」所列全部文件与 `CHANGELOG.md`。G046：不写 issue id、本机路径。验证：私有名检查；我审删除行。

### T9 真实验收、反向验证、全量与收尾

- 执行方：Claude。
- 真实验收（主机临时 server，临时 git 仓库）：`complete_on=merge` plan 两个 worktree todo 串联，第一个完成后第二个不派发，「等我」出现「待你收尾」，`worktree merge` 后派发第二个；一个两回合 suga job 与一个 ACP job 的 `job transcript --outline` / `--turns` / `--out`。记录 `tmp/b1-impl/acceptance.md`。
- 反向（副本，不提交）：去掉 §1 判定 →「交付 none 不推进」失败；去掉 suga delta 累积 → 两回合用例只剩一回合；去掉 R5 → 续跑提交失败用例失败；去掉脱敏 → 掩码用例失败。记录 `tmp/b1-impl/reverse.md`。
- 收尾：`git diff --stat main...` 查无关删除；全分支私有名检查；gofer-verify 四路；合并（`--no-ff`）；删 worktree 与分支；设计稿实施记录；计划 COMPLETE 总结；关 issue；另立 issue：`stdout.raw.log` 不脱敏（R2）；把 gofer-ohf7 发给 suga 会话。

## 回滚与恢复

单独回退合并提交；plans 表 `complete_on` 列保留无害；结果目录多出的 `turns.jsonl` 无害。

## 人工 Gate

- 本计划批准后开始 T0。
- 发版：攒批另行请示。

## 可追溯性

| 设计项 | 任务 | 验证 |
|---|---|---|
| §1 完成时机、续跑修正、merge 即完成、rm、远程 | T1 | T1 用例、T9 验收与反向 |
| §1 待你收尾卡片 | T2、T5 | T2 用例、T9 验收 |
| §2.1 回合写入与脱敏 | T3 | T3 用例、T9 反向 |
| §2.2 读取接口 | T4 | T4 用例、T9 验收 |
| §3 Quick | T6（juo0）、T7 | focused test |
| §4 小改 | T6、T5（事件显示）、T8 | focused test、文档 |
| 文档同步 | T8 | 私有名检查 |

## 完成 Gate 与剩余工作

T9 完成即本计划完成；发版另行进行。
