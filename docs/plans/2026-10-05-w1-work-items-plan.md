# W1 批：工作项一期 + Sessions 卡片化 + 工作空间自动创建（2026-10-05）

分支 w1-batch，不 push、不合并 main。设计见 `docs/design/2026-10-05-work-items-and-steward-design.md`（只做一期）。

## 提交序

1. W3 `fix(workspace)`：serve / worker 启动时 `config.EnsureWorkspaceDir()`（已完成）。
2. W1-a 数据层与服务：`jobstore/work*.go` + `internal/work` + 会话钩子 + 配置 `work:`。
3. W1-b 通知与推送：`work.remind` / `work.digest` 事件、`work` 推送主题、定时扫描。
4. W1-c REST + CLI + MCP + SessionStart prime。
5. W1-d Web「工作」页 + 公共卡片组件。
6. W2 Sessions 页卡片化（复用公共卡片）。
7. 收尾：skill / README / AGENTS.md(G033)，质量门，冒烟与截图。

## 表结构（additive，schemaStmts 内 CREATE IF NOT EXISTS）

- `work_items`：id、title、goal、status、status_source(auto|human|report)、blocker_kind、blocker_text、next_step、summary、project_key、workspace、priority、park_until、park_note、remind_at、reminded_at（已提醒到的时间点，防重复）、source(auto|human|steward)、unsorted（未整理）、merged_into、rev（乐观锁）、created_at、updated_at、updated_by、closed_at、last_activity_at。
- `work_item_sessions`：work_item_id、session_id、role(current|past)、attached_at、detached_at；PK(work_item_id, session_id)。会话可同时属于多个工作项（拆分「保留」模式）。
- `work_journal`：id（自增）、work_item_id、kind(report|note|status|steward|link)、text、by(human:<caller>|session:<sid>|job:<id>|system|steward)、at、origin_item（合并来源）。只追加。
- `work_links`：work_item_id、kind(issue|plan|todo|job)、ref、created_at；PK 三列。
- `work_kv`：key/value，只存 `digest_last_date`（每日摘要去重）。

## 规则

- 自动草稿：会话首次出现「人工提问」（hook 带 title 的 UserPromptSubmit，非注入）且该会话尚无任何工作项关联 → 建 `source=auto, unsorted=1` 的草稿，标题取提问首行。
- 状态映射（仅当 status_source=auto 且非 done/dropped）：当前会话 waiting_reply / needs_attention → needs_me；否则关联 job（会话 watch + work_links 的 job）有 needs_review → review；否则任一会话 running → active；idle/offline/ended 不改状态（卡片标注）。人手动设置 → status_source=human，自动映射不再覆盖；会话 `work report --status active` 解除（回到 auto）。report 带其他状态在 human 锁定下仅写日志。
- 日志：所有字段变更自动写 status/link 类日志；journal 只追加。
- 合并：多个源工作项 → 目标；会话/链接并入，日志搬到目标并标 origin_item；源标 dropped + merged_into，默认列表隐藏。拆分：新建工作项，指定会话移动（或 keep 模式同时保留）。
- 提醒：`remind_at` 或（status=parked 且）`park_until` 到点 → 一次 `work.remind`（`reminded_at` 去重），卡片进「到期提醒」；清除 remind_at / 改状态后离开。
- 每日摘要：`work.digest_time`（默认 09:00，`work.digest_enabled: false` 关闭）；数据库确定性生成正文（等我/等资源/需现场计数、搁置超 7 天、昨日有进展列表，带链接）；当天只发一次，窗口 6h。

## 接口

- REST：`GET/POST /v1/work-items`（筛选 status/project/workspace/unsorted/session/q/closed）、`GET/PATCH /v1/work-items/{id}`（rev 乐观锁，409）、`GET/POST /{id}/journal`、`GET/POST/DELETE /{id}/sessions`、`GET/POST/DELETE /{id}/links`、`POST /{id}/merge`、`POST /{id}/split`、`POST /{id}/report`（job 凭据可用）、`POST /{id}/report-request`（对运行中会话发固定汇报请求）、`GET/POST /v1/work-items/digest`（预览/立即发送）。
- 推送：`/v1/ws` 新主题 `work`（inval），经 store ChangeWork 触发。
- CLI：`gofer work ls|show|new|set|note|park|remind|report|link|merge|split|digest`（G033 核心名词清单加入 `work`）。
- MCP：`gofer_work_list/get/update/note/report`、只读 `gofer_session_list/get`；local 与 client 两个 Backend 都实现；project-scoped MCP 仅可见/可改本项目工作项；leader 白名单不含（leader 只做 plan 内评论/todo）。
- SessionStart prime：一行 `gofer work report` 用法。

## Web

- 新页「工作」：顶部「等我」「到期提醒」计数；按状态分栏 / 按工作区分组切换；「未整理」区；`WorkCard`（与 Sessions 卡片共用的 `SessionCard`/`WorkCard` 公共外壳 `CardShell`）；详情抽屉（字段编辑、日志时间线、会话、关联项）；操作齐全；「请它汇报」仅对运行中的会话。
- Sessions 页：AGENT / ACP / 终端三区由表格改卡片，默认关键信息 + 展开细节；手机单列桌面多列；显示所属工作项并可跳转；保留全部既有功能。

## 测试清单

- jobstore：建/改/乐观锁冲突/日志/合并/拆分/草稿幂等/到期查询/迁移幂等。
- work service：自动草稿、状态映射与人工优先、report 解除、提醒扫描只发一次、摘要内容确定性。
- httpapi：CRUD、筛选、409、merge/split、report（job 凭据）、report-request、digest、ws `work` 主题。
- notify：默认事件集含 work.remind/work.digest、渲染（钉钉 markdown、max_text_runes）。
- CLI：`work` 子命令对 httptest server。MCP：local 与 client 后端读写。
- hookrelay prime 行。
- web vitest：工作页状态分栏/分组切换、卡片展开/收起、操作；Sessions 卡片展开/收起、中继开关/传话/唤醒保留、所属工作项跳转。
- 冒烟：临时 serve + 假 webhook，走完设计 §13 全流程，1280/390 截图 `tmp/w1-smoke/`。
