# W2a 批：工作项二期第一批（2026-10-06）

分支 w2a-batch，不 push、不合并 main。设计见 `docs/design/2026-10-05-work-items-and-steward-design.md` §14.1–14.3、§14.5 W2a 行、§14.6 建议即确认、§14.7 W2a 验收。W2b（管家）本批不做。

## 确认的默认值

- 整理器：现有 `claude` cli-agent + 便宜模型参数；配置 `work.summarizer_agent`（默认 `claude`）、`work.summarizer_args`（claude 未配置时默认 `--model haiku --tools ""`）；整理功能在没有可用整理器 agent 时降级关闭并在设置页 / `GET /v1/work-items/summarizer` 提示。
- 被动整理默认开启：idle ≥ 15 分钟 / offline / ended，且自上次整理后有新活动；每会话最小间隔 30 分钟；每日上限 50。
- 搁置 / 需现场（人设置）时自动发 handoff 请求（仅会话运行中；不在运行转整理）。
- 请求默认 30 分钟超时。

## 提交序

1. `docs(plan)`：本计划。
2. `feat(work)` 发言者标注：`by` 规范化（`human:<caller>` / `session:<sid>(<agent>)` / `steward(<agent>)` / `summarizer(<agent>)`，job 凭据 `job:<id>`）；字段级来源表 `work_field_sources`（goal / blocker / next / summary 各自的 by + at）；视图 `field_sources`；卡片与详情显示。
3. `feat(work)` 请求账本：`work_requests` 表与状态机、`gofer work report --request`（MCP `gofer_work_report` 的 `request` 参数）回填、超时置 expired 并转整理、会话不在运行转 summarize、搁置 / 需现场自动 handoff、重启恢复（扫描器从表推进）。
4. `feat(work)` transcript 尾部解析：`internal/work/transcript`（claude / codex / omp 解析器 + fixture）。
5. `feat(worker)` 会话尾部只读接口：协议 v17 帧 `transcript_tail` / `transcript_tail_result`、能力位 `SupportsTranscriptTail`、worker 只读受限读取、hub 同步请求。
6. `feat(work)` 被动整理：触发扫描 + 成本控制（`work_summaries` 表）、一次性只读无工具 job（内部标签 `work-summarizer`，列表 / Board 默认隐藏）、固定 JSON 解析（失败重试一次）、写回规则与"整理建议"表 `work_suggestions`、采纳 / 忽略。
7. `feat(work)` 入口：REST（`summarize` / `requests` / `suggestions/{field}/accept|dismiss` / `summarizer` 状态 / `PUT /v1/config/work`）、CLI（`work summarize` / `work requests`）、MCP（`gofer_work_summarize` / `gofer_work_requests` / 只读 `gofer_work_suggestions`，按 W2b 白名单思路只读 + 走账本）。
8. `feat(web)`：来源标注、整理建议采纳 / 忽略、在途请求状态、「整理」按钮、设置页 work 区。
9. `docs`：gofer-usage skill / README / 配置示例 / 设计状态同步（G045）。
10. 冒烟与截图（`tmp/w2a-smoke/`）。

## 新表（additive，`CREATE TABLE IF NOT EXISTS`）

- `work_field_sources(work_item_id, field, by, at)`，PK(work_item_id, field)；field ∈ goal|blocker|next|summary。
- `work_requests(id, work_item_id, session_id, kind[report|handoff|summarize], state[pending|sent|answered|failed|expired], channel, text, by, error, created_at, sent_at, answered_at, deadline, parent_id)`。
- `work_suggestions(work_item_id, field, value, confidence, by, at, state[pending|dismissed], job_id)`，PK(work_item_id, field)；field ∈ goal|blocker|blocker_kind|next|summary|status_hint。
- `work_summaries(id, work_item_id, session_id, at, activity_at, state[ok|failed|running], job_id, error, trigger)`：成本控制（每会话间隔、每日上限、"有新活动"判断）与审计。

## 规则要点

- 字段来源：`UpdateWorkItem` 对 goal / blocker_text / blocker_kind / next_step / summary 的变更按 `by` 写入 `work_field_sources`；写回规则：整理器只写空字段或 `source=summarizer` 的字段，否则存为建议。
- 请求送达：会话运行中（running / waiting_reply / needs_attention / idle）经 `sessionrelay.SendMessage`（等回复走中继注入，否则走传话）；文本带请求 id 与 `gofer work report <id> --request <rid>`；会话不在运行（offline / ended / handed_off / 记录缺失）直接建 `summarize` 请求并整理。
- 超时：Tick 扫描 `deadline` 已过的 pending / sent 请求 → expired + 日志 "会话未回应，已改为整理" + 触发整理；pending 的 summarize 请求超时 → failed（重启中断）。
- 整理一次性 job：整理器 agent + `--read-only` + 参数；prompt 内含 transcript 尾部 + 现有字段；输出固定 JSON（goal/progress/blocker_kind/blocker/next/status_hint/confidence），宽松提取首个 JSON 对象；解析失败重试一次。
- transcript 读取：session.runner 是 server 本机 → 直接读登记的 transcript；worker 上的会话 → 协议帧；旧 worker 不支持或读取失败 → 退回 last_message + progress + 日志。

## 测试

jobstore（来源、请求账本、建议、整理日志）/ work service（状态机、写回规则、成本上限、超时转整理）/ transcript 解析 fixture / wsproto + hub + worker 帧（鉴权与大小上限、旧 worker 降级）/ httpapi / commands / mcpserver / web vitest。质量门：gofmt、build、GOOS=windows build、vet、test ./... -count=1；web vue-tsc、vitest、vite build（输出到 tmp/w2a-web-dist）。
