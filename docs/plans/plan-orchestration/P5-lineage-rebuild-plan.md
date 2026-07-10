# P5 — job 血缘 `source_job_id` + 无会话 job「快速重建」（实施计划，代码级）

> 主纲：[plan-orchestration-plan.md](./plan-orchestration-plan.md)
> 设计：[../../design/2026-07-09-plan-orchestration-design.md](../../design/2026-07-09-plan-orchestration-design.md) §7（session 续跑）
> 上游：[P1-data-plan.md](./P1-data-plan.md)（✅ push）、[P2-mcp-aggregate-plan.md](./P2-mcp-aggregate-plan.md)（✅ push）、[P3-todo-plan.md](./P3-todo-plan.md)（✅ push）、[P4-frontend-plan.md](./P4-frontend-plan.md)（✅ **已合并 push**，`origin/main`）。
> 关联安全 issue：`h-aii-xqe1`（`GET /v1/jobs/{id}/request` 裸吐 request_json 含 env 明文）——本阶段 T7/T9 关闭的是**直读**（该端点不再返回 env/secret 明文）。注意：这**不等于**「env 明文永不出服务端」——rebuild 继承源 env + 覆盖执行体后仍可经 job 日志/结果读回（见 §核心约束「安全声明」，用户已拍板接受为信任模型内残留）。
> 触点均已实测定位（2026-07-10 只读探查，见各 T 的 file:line）。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-10 | inhere/claude | 初稿：`source_job_id`（客户端可设）+ `?redact=1` 脱敏读端点 + 预填「普通 POST /v1/jobs」提交 + env 脱敏后用户重填。 |
| v0.2 | 2026-07-10 | inhere/claude | **用户拍板「env 值不出服务端」，推翻 v0.1 的 D1/D3/D4/D6**：① 前端拿不到 env 值 → 提交改走**服务端** `POST /v1/jobs/{id}/rebuild`（服务端从源 job 的 request_json 取基底，应用覆盖字段 + `env_set`/`env_unset`，未触碰的 env key **保留原值**，env 明文全程不出服务端）；② `source_job_id` 改为**服务端盖章**（`json:"-" yaml:"-"`，仿 `ResumeSourceAgent`），消除伪造入口（推翻 v0.1「客户端可设、伪造无害」）；③ `rerun` = rebuild 空 body 特例，一端点覆盖两事 → `GET /request` **默认脱敏**（裸吐分支无消费者，关 `h-aii-xqe1`）；④ 占位符双防线（未编辑字段不回传 + 服务端 400 兜底拒绝）。**探查已证实 v0.1 三处纠偏**（端点已存在、NewJob 路由 `/new`、workflow→job 单向 import）仍成立，本版沿用其正确结论（`internal/secret` 叶包、env wholesale 脱敏、`?source_job=` 反查、system_prompt 需脱敏）。 |
| v0.3 | 2026-07-10 | inhere/claude | **对抗式复审后修订**：① 不变量诚实降级（用户拍板 **A**，接受残留 exfiltration）——「env 明文不出」的真值是「gofer 不把 `Env` 序列化进 `GET /request`/`JobResult`」，**非**全部响应路径（job 日志 `logs/stdout`、`result_json`、`error`、pty 录制可经 rebuild 注入读回，见 §核心约束「安全声明」）；② 补 `AgentArgs` 脱敏（真漏洞——`--token=x` 类 secret 的首要向量，T7/T8 遗漏）；③ 清除全文「P4 未落」陈旧假设（P4 已合并 push，`resume.go:98` 已有 `PlanID: src.PlanID`，`web/types.ts` 已有 `plan_id/plan/session`）；④ UpsertJob 同步口径由「三处」改**四处**（外加 `ON CONFLICT SET`，漏则静默错列）；⑤ `env_set`/`env_unset` 同 key 语义定为 **unset 优先**；⑥ rebuild/resume/rerun 均**不做 caller 归属校验**（已定，接受）。 |

## 一句话

给 jobs 加一列 **`source_job_id`**（血缘键，**服务端盖章**、不可客户端伪造）：resume/rebuild 出的 job 都用它指回源 job，靠 `session_id` 是否与源相同即可区分「续会话」与「重建」（不另存派生类型列）。新增 **`POST /v1/jobs/{id}/rebuild`**：服务端以源 job 的 `request_json` 为基底，套用请求体里**用户改动过的字段** + `env_set`/`env_unset`（未提及的 env key 保留原值），Submit 一个新 job——**源 env 真值不进任何请求/响应体的序列化**（`GET /request` 脱敏、`JobResult` 无 `Env` 字段）。`GET /v1/jobs/{id}/request` **默认脱敏**（供前端预填**显示**用，env 值渲染为占位），`gofer job rerun` = rebuild 空 body（原样重投）。

> ⚠️ **不变量边界**：脱敏防的是 env 的**意外暴露**（预填表单/截图/浏览器缓存/API 响应体里出现明文），**不防**持 token 的 caller **主动提取**（rebuild 继承源 env + 覆盖 prompt/cmd 让新 job 把 env 打进日志再读回）。详见 §核心约束「安全声明」。

## ⚠️ 实施前必读（最易实施错的 3 点）

> 复审实测：以下三处若照 v0.2 字面或凭直觉实现，极可能出错。**动手前先核对当前源码**。

1. **`resume.go` 合并——`PlanID` 已在，勿重复加（T4）**：P4 已合并，`resume.go` 的 `s.Submit(JobRequest{...})` 里 `EscalateTo: src.EscalateTo,`（约 `:94`）之后**已有** `PlanID: src.PlanID,`（约 `:98`），`})` 在约 `:99`。T4 **只**在 `PlanID` 旁**追加** `SourceJobID: jobID,`——**不要**再加一行 `PlanID`（重复字段编译不过 / 逻辑错位）。最终代码块见 T4。
2. **`UpsertJob` 是四处同步（T1）**：`internal/jobstore/jobs.go` 的 `INSERT INTO jobs` 需同增 **四处**——列清单、`VALUES` 的 `?`、`ON CONFLICT(id) DO UPDATE SET` 的逐列赋值、`Exec` 实参。其中 `ON CONFLICT SET`（第四处）最易漏，**漏了会静默错列**（编译通过、运行时 `source_job_id` 写不进/写错列）。当前 34 列 → 35。
3. **前端 `buildRebuildBody` 的 diff/env（T13）**：baseline 快照**必须**在 `prefillFrom` 预填完成后**立即**打（`snapshotBaseline()`），否则 diff 基准错、全字段误发；env 的 `keep` 行**绝不回传**（否则占位符 `••••`/空值污染源 env）；同一 key **不能**同时进 `env_set` 与 `env_unset`。

## 范围

**做（P5）**

1. **数据层**（T1）：`jobs` 表加 `source_job_id` 列（+`?source_job=` 反查索引）；`JobRecord`/selectCols/scanJob/UpsertJob/`ListQuery.SourceJob` 逐层打通。仿 `plan_id`（P1 先例）。
2. **job 层血缘字段**（T2）：`JobRequest.SourceJobID` **`json:"-" yaml:"-"`**（仅服务端内部可设，仿 `ResumeSourceAgent`）+ `JobResult.SourceJobID` `json:"source_job_id,omitempty"`（出网供前端展示）。
3. **贯通落库/互转/重试保血缘**（T3）：toRecord/fromRecord 互转；submit 落库；**`maybeRetryJob` 从快照恢复 `source_job_id`**（`json:"-"` 不进 request_json，重试再提交须显式带回，仿现有 CallerID 恢复）。
4. **resume 盖血缘**（T4）：`resume.go` Submit **追加** `SourceJobID: jobID`（P4 已合并落地 `PlanID: src.PlanID`，二者并存；**勿重复加 PlanID**）。
5. **list 反查**（T5）：`?source_job=<id>` 列出某 job 直接派生出的所有 job（血缘双向导航）。
6. **脱敏原语**（T6）：抽 `internal/secret` 叶包（`RedactString`），`internal/job/workflow` 委托复用（零行为重构 G023；因 workflow→job 单向 import，脱敏原语不能留 workflow 供 job 复用）。
7. **脱敏读视图**（T7）：`internal/job` 的 `RedactedRequest(id)`——env 值 wholesale→占位、prompt/cmd/cwd/system_prompt/**agent_args**（v0.3 补，真漏洞）走 `RedactString`，清 request_id/caller_id/session_id。供 `GET /request` **默认**返回。
8. **rebuild 编排**（T8）：`internal/job` 的 `RebuildJob(id, overrides, callerID, clientIP)`——源 request_json 为基底 → 套覆盖字段（指针语义）+ env merge（`env_set`/`env_unset`）→ 清 request_id/session_id、盖 caller/`source_job_id=id`、保留/可覆盖 plan_id → **占位符兜底拒绝（400）** → Submit。
9. **httpapi**（T9）：`handleGetJobRequest` **改默认脱敏**（删裸吐，关 `h-aii-xqe1`）；新增 `POST /jobs/{id}/rebuild` = `handleRebuildJob`（薄入口，仿 `handleResumeJob`）；`handleListJobs` 加 `?source_job=`。
10. **Go client + CLI**（T10）：`client.GetJobRequest` 语义改（返回脱敏，更新 doc）+ 新增 `client.RebuildJob`；`gofer job rerun` 改调 rebuild 空 body（`--watch` 逻辑不变）；`job list --source-job`。
11. **web**（T11~T14）：`types.ts` 加 `Job.source_job_id`/`ListJobsOpts.source_job`/`RebuildRequest`（脱敏显示视图）/`RebuildBody`（覆盖+env_set/unset）；`client.ts` 加 `getJobRequest`（脱敏，预填显示）/`rebuildJob`/`listJobs ?source_job`；`NewJob.vue` **双模式**（`?from=` → 预填显示 + 快照 diff + env 编辑器仅显示 key/占位 → rebuild 提交）；`JobDetail.vue` 加「快速重建」按钮（**所有 job**）+ `source_job_id`/`plan_id` meta。
12. **验证**（T15）：后端 `go build/vet` + `go test ./...`；前端 `pnpm typecheck && pnpm build`。

**不做（划归后续 / 留边界）**

- **派生类型列 / 血缘成树 UI**：只存单键 `source_job_id`（父指针），前端只做「源→派生」双向链接，不画树。
- **rebuild 支持 sync 等待终态**：rebuild 与 resume 一样**默认 async**（提交即返回新 job，详情页轮询）。`rerun` 由此从 sync 变 async——不带 `--watch` 时打印的状态可能是 queued/running 而非终态（见 R3，行为等价）。
- **rebuild 覆盖 `role`/`retry`/`origin_agent`/`escalate_to`/`record_pty`**：这些从源 job **静默继承**，本期不开放覆盖（覆盖面收敛到 NewJob 表单可编辑项 + plan_id + env，见待确认 D1）。
- **provenance 重放精度**：rebuild 的 `channel` 可由 body 覆盖（web 传 `web`），`client` 重盖为新来源 IP；`channel` 未覆盖时继承源（如 `cli`）——可接受。

## 核心约束（承接总纲 C1..C5 / G021..G024）

- **血缘服务端盖章、不可伪造**：`source_job_id` 由 `RebuildJob`/`ResumeJob` 从 URL 里的源 id 内部设置；`JobRequest.SourceJobID` 用 `json:"-" yaml:"-"`（不入 request_json、`c.BindJSON` 收不到、md frontmatter 设不了），仿 `ResumeSourceAgent`（`model.go:163`）。区别：`ResumeSourceAgent` 的 `json:"-"` 是**安全刚需**（防绕 allow_exec 门）；`source_job_id` 的 `json:"-"` 是**主动消除伪造面**（URL 已是真源，无需接受客户端声明）——两者机制同、动机异。
- **env 真值不进 gofer 的请求/响应序列化**（诚实降级，非「永不出服务端」）：源 env 真值只在服务端（request_json 落库 + `RebuildJob` 内存合并）；**gofer 主动序列化的**响应体不含 env 明文——`GET /request` 脱敏（env 值占位）、`GET /jobs` 的 `JobResult` 无 `Env` 字段、rebuild 返回新 `JobResult` 亦无。**边界见下条安全声明**：这不覆盖 job 自己写进日志/结果的内容。
- **⚠️ 安全声明：rebuild 存在残留 env exfiltration（用户已拍板接受，2026-07-10）**：
  - **机制**：rebuild 允许覆盖执行体（`prompt`/`cmd`/`agent`/`agent_args`/`system_prompt`）**且继承源 job 的 env 真值**。持 token 的 caller 可提交一个 rebuild，让新 job 把继承来的 env 打进 stdout，再经 `GET /v1/jobs/{id}/logs/stdout`（及 `result_json` / `error` / pty 录制）读回明文。门禁不变（exec 载体仍需 `allow_exec`，cli-agent 仍受 `allowed_agents`），但被允许的 agent 足以打印自身环境。
  - **这是 P5 新引入的注入能力**：旧 `job rerun` 只能**原样重投**、改不了 prompt/cmd；rebuild 的「继承 env + 覆盖执行体」组合是新增的。
  - **定位**：脱敏防的是 env 的**意外暴露**（明文出现在 UI 预填表单 / 截图 / 浏览器缓存 / API 响应体）；**不防恶意 caller 主动提取**。gofer 的信任模型是「持 token = 可跑任意命令 = 可读任意 job 的 env」——单信任层、无 per-job ACL。
  - **缓解（审计而非阻断）**：血缘列本身即审计痕迹——rebuild 出的新 job 同时记录 `source_job_id`（重建自谁）与发起者 `caller_id`（谁发起），"谁重建了谁的 job"可事后追溯。
  - **被否决的方案与理由**：给 rebuild/resume/rerun 加 caller 归属校验（只能重建自己 caller 的 job）——否决，因为会破坏"重建/续接他人 job 来协作调试"这一真实用例，且 resume/rerun 现也无此校验，单给 rebuild 加会造成语义不一致。
- **默认端点脱敏、无裸吐**：`GET /request` 默认脱敏（关闭 `h-aii-xqe1` 的**直读裸吐分支**——注意 `h-aii-xqe1` 关的是端点直读，**非**全部 env 泄露路径，见上条安全声明）；裸吐分支删除——`rerun` 服务端化后无消费者（T9 已核验，仅内部 `execute.go`/`serve.go`/pty 直接读 `RequestJSON` 字段，**不经此端点**，不受影响）。
- **G021 入口薄**：merge/env 合并/清理/盖章/占位符校验落 `internal/job.RebuildJob`；httpapi handler 只绑定 + 盖 caller + 映射 sentinel → 状态码。
- **G024 无环**：`internal/secret` 叶包（仅 import `regexp`）；`internal/job` 与 `internal/job/workflow` 均可 import 它。`internal/job` **不能** import `internal/job/workflow`（后者 18 文件 import 前者，已核验 `workflow/query.go:8` 等）——故脱敏原语必须抽叶包。
- **G023 零行为重构**：T6 抽 `internal/secret` 后 `workflow/export.go` 委托调用，行为逐字不变，现有 workflow export 测试背书。

## 已确认关键事实（探查结论）

| 事实 | 位置（实测 file:line） | 对 P5 的影响 |
|---|---|---|
| `GET /v1/jobs/{id}/request` **端点已存在，裸吐未脱敏**；HTTP 侧**唯一非测试消费者是 CLI rerun** | `httpapi/job_handler.go:181-193`、路由 `server.go:382`；client `client.go:349-357` → 唯一调用方 `commands/job.go:705` | rerun 服务端化后裸吐无消费者 → T9 改**默认脱敏**、关 `h-aii-xqe1` |
| `request_json` 的其余读者**直接读字段、不经端点**：重试 `maybeRetryJob`、schedule sweep、pty 观察 | `job/execute.go:244`、`serve/serve.go:710`、`httpapi/local_pty_observer.go:51,59`、`pty_connect_handler.go:107` | 端点脱敏**不影响**它们（读的是内存 `RequestJSON` 字段，非 HTTP 响应） |
| `ResumeSourceAgent` = `json:"-" yaml:"-"`，仅 `ResumeJob` 内部设，注释明示"不入 request_json、客户端不可伪造" | `job/model.go:148-163`（tag `:163`）；设值 `resume.go:87` | **N2 类比成立**：`JobRequest.SourceJobID` 照此 `json:"-" yaml:"-"` |
| `maybeRetryJob` 从快照恢复 `json:"-"` 字段（CallerID 等）再 re-submit | `job/execute.go:244-264`（`req.CallerID = snap.CallerID` `:249`；`next.RequestID=""` `:263`） | **T3 须加** `req.SourceJobID = snap.SourceJobID`，否则重试丢血缘（`json:"-"` 不在 request_json） |
| schedule sweep 从 `ScheduleRecord.RequestJSON` 起新 job（非派生） | `serve/serve.go:708-718` | 该路径 `source_job_id` **应保持空**（无源 job）——无需改，天然为空 |
| submit 把 `req` 各字段拷进 `entry.result` 落库（Go 赋值，与 json tag 无关） | `job/submit.go:231-266`（PlanID `:265`）；reqJSON marshal 在 cwd 解析前 `:110` | `json:"-"` 的 SourceJobID 仍能 `SourceJobID: req.SourceJobID` 拷进 result → DB 列；血缘走 DB 列非 request_json（**N2 第二问：够贯通**） |
| record↔result 互转两处已是 plan_id 先例 | `persistence.go:63`（toRecord PlanID）/`:135`（fromRecord PlanID） | `SourceJobID` 照抄两处即贯通 DB 列 ↔ JobResult |
| ~~`resume.go` Submit 尾部当前无 PlanID 也无 SourceJobID~~ **【v0.3 更正：P4 已执行并 push，`PlanID: src.PlanID` 已在位】** | `job/resume.go:74`（`return s.Submit(JobRequest{`）；`PlanID: src.PlanID` 已落于 `EscalateTo` 之后 | T4 只需在 `PlanID` 旁**追加** `SourceJobID: jobID`，两行并存。R1 的"谁后落谁合并"已确定为：**P4 先落，P5 后落，勿覆盖 PlanID** |
| 脱敏原语 `redactSecretsInString` 未导出，困在 `internal/job/workflow`；`workflow` 18 文件 import `job` | `workflow/export.go:40-62`（正则 `:26,:33`，占位 `:13`）；`workflow/query.go:8`、`cancel.go:6` 等 | 抽 `internal/secret` 叶包（T6），双方复用不成环 |
| workflow export 脱敏：值换 `***REDACTED***`，置 `X-Gofer-Redacted:1` 头 | `workflow_handler.go:82-97`（头 `:94`）；client 读头 `client.go:632`；CLI 警告 `commands/workflow.go:352-353` | `RedactedRequest` 复用占位符 + 头 |
| `JobRequest.Env`/`SystemPrompt` 随 request_json 落库；`EnvFiles` 声明非敏感路径、加载值不回写 | `job/model.go:45-57`（Env 含"勿放 secret"）、`:44`（SystemPrompt） | env wholesale 脱敏 + system_prompt 走字符串脱敏；EnvFiles 路径可显示、继承 |
| **【v0.3 真漏洞】`AgentArgs` 是 cli-agent 追加到 argv 的 CLI flags、随 request_json 落库**——是 `--token=x`/`--api-key=x` 类 secret 的**首要向量**（workflow `secretFlagPattern` 正为此设计）；v0.2 的 `RedactedRequest`/`rejectPlaceholders` **漏了它** | `job/model.go:14-16`（AgentArgs doc）；`config.go:113`（agent_args 对 cli-agent 合法） | **T7 补** `for i := range req.AgentArgs { secret.RedactString }`；**T8 `rejectPlaceholders` 补** AgentArgs 循环（仿 Cmd）——否则 agent_args 里的 secret 经 `GET /request` 明文泄漏，h-aii-xqe1 对此向量未关闭 |
| `job rerun` flags 仅 `{watch bool}`；流程 = GetJobRequest→清 RequestID→SubmitJobSync→打印 id→(watch)watchToTerminal | `commands/job.go:92-94`（opts）、`:696-723`（run）；`client.go:82,89`（SubmitJob/Sync） | **N3 成立**：rerun 改调 `RebuildJob(id, 空)`→拿新 id→`--watch` 逻辑不变 |
| POST /v1/jobs 走 `c.BindJSON(&req)` 无字段白名单，仅 stamp `CallerID`/`Client` | `httpapi/job_handler.go:33-65`（`:56` caller、`:63-65` client） | rebuild 端点仿此盖 caller + client IP（`clientIP` `:95-107`） |
| plan_id 落库/互转/过滤三路已就位（P1） | 落库 `submit.go:265`；互转 `persistence.go:63/135`；过滤 `jobs.go:407-410`、`list.go:82/121-123`、`job_handler.go:141` | `source_job_id` 逐处照抄 |
| jobs UpsertJob 列/占位符/`ON CONFLICT SET`/实参**四处**须同步；当前 34 列（plan_id 末列） | `jobs.go:172-227`（列 `:173-177`、VALUES 34 `?` `:178`、`ON CONFLICT SET` `:180-212`、Exec 实参 `:217-227`） | 加 `source_job_id`→**35 列**，**四处同步 +1**（`ON CONFLICT SET` 第四处最易漏，漏则静默错列） |
| **NewJob 路由是 `/new`（name `new-job`），非 `/jobs/new`**；已读 `route.query`，refs 齐全但**无 env / plan 输入**，onSubmit 从 refs 拼 body | `web/src/router.ts:16`；`NewJob.vue:42,206-253,266-271` | 重建跳 `/new?from=<id>`；双模式 + 新增 env 编辑器 + 快照 diff |
| web `request<T>` 丢响应头；`Job`/`ListJobsOpts`/`SubmitJobReq` 无 plan_id/env/source | `client.ts:89-102`；`types.ts:12-56,412-423,583-604` | `getJobRequest` 自带 fetch（读 `X-Gofer-Redacted`）；类型补字段 |
| JobDetail 动作区 `.head-right`（取消/终端按钮）+ meta 区（session_id `:927`）为按钮/血缘落点 | `JobDetail.vue:870-892,927-930` | 「快速重建」按钮 + `派生自`/`plan` meta 落此 |

---

## 任务分解（T1..T15）

> 顺序：数据层(T1) → 血缘字段(T2~T4) → list 反查(T5) → 脱敏原语/视图/编排(T6~T8) → httpapi(T9) → client/CLI(T10) → web(T11~T14) → 验证(T15)。SR1202：每个 T（或相邻小 T 合并）完成后更新总纲 checkbox + Git 提交。

---

### T1 — jobstore：`jobs` 加 `source_job_id` 列 + 反查索引 + 记录/查询贯通

**`internal/jobstore/store.go`**

(a) CREATE TABLE 加列（现状 `store.go:90` `plan_id TEXT` 末列）：
```go
  role             TEXT,
  plan_id          TEXT,
  source_job_id    TEXT
)`,
```

(b) migrate ALTER（现状 `store.go:444` plan_id 的 add 之后、`migrateWorkflows()` `:447` 之前）：
```go
	// plan 编排 P5：血缘键——resume/rebuild 出的 job 指回源 job（服务端盖章 source_job_id=源 id）。
	// 旧库 ALTER ADD，旧行 COALESCE→""。区别引擎私有 workflow_id；区别 source 列（执行位置）。
	if err := add("source_job_id", "source_job_id TEXT"); err != nil {
		return err
	}
```

(c) 反查索引（现状 `store.go:464-468` plan_id 索引之后、`return nil` 之前）：
```go
	// source_job_id 反查索引（list ?source_job=）：列出某 job 直接派生出的所有 job。
	if _, err := s.db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_jobs_source_job_id ON jobs(source_job_id)`,
	); err != nil {
		return fmt.Errorf("jobstore: migrate source_job_id index: %w", err)
	}
```

**`internal/jobstore/jobs.go`**

(d) `JobRecord` 加字段（现状 `jobs.go:99-101` `PlanID` 后）：
```go
	// SourceJobID 是血缘键（P5）：resume/rebuild 出的 job 指回源 job id（服务端盖章）。空=非
	// 派生（旧库 COALESCE→""）。与 job.JobResult.SourceJobID 互转；反查 ?source_job=。
	// 注意区别既有 Source 列（执行位置 worker:/peer:）。
	SourceJobID string
```

(e) `ListQuery` 加字段（现状 `jobs.go:114` `Plan` 后）：
```go
	SourceJob string // exact source_job_id match when non-empty (P5, list ?source_job=)
```

(f) `selectCols` 末尾（现状 `jobs.go:134`）：
```go
  COALESCE(role,''), COALESCE(plan_id,''), COALESCE(source_job_id,'') FROM jobs`
```

(g) `scanJob` Scan 末尾（现状 `jobs.go:154`）：
```go
		&r.OriginAgent, &r.EscalateTo, &r.Role, &r.PlanID, &r.SourceJobID,
```

(h) `UpsertJob` **四处**同步 +1（34→35）——**四处缺一不可，`ON CONFLICT SET`（③）最易漏，漏则 finish 二次 upsert 时 `source_job_id` 不回写/写错列（编译通过、运行时静默错列）**：
- ① 列清单（`:173-177`）末尾加 `, source_job_id`；
- ② VALUES（`:178`）**加一个 `?`**（34→35）；
- ③ `ON CONFLICT(id) DO UPDATE SET`（`:180-212`）末尾加 `,\n    source_job_id=excluded.source_job_id`；
- ④ Exec 实参（`:217-227`）`rec.PlanID,` 后加 `rec.SourceJobID,`。

(i) `ListJobs` where（现状 `jobs.go:407-410` `q.Plan` 块后）：
```go
	if q.SourceJob != "" {
		where = append(where, "source_job_id = ?")
		args = append(args, q.SourceJob)
	}
```

> ⚠️ 占位符铁律（P1 踩过，v0.3 强化为**四处**）：列数=VALUES `?`数=Exec 实参数=**35** 且 `ON CONFLICT SET` 同步加 `source_job_id=excluded.source_job_id`，改完**四处各数一遍**。命名 `source_job_id`/`SourceJob`/`?source_job=`，勿与既有 `source` 列（执行位置）撞（R9）。

**验收**：`go test ./internal/jobstore/...` 绿；扩展单测：Upsert 两 job（`SourceJobID="j-src"`/`""`）→ `ListJobs{SourceJob:"j-src"}` 只回前者；`GetJob` 回读正确；**`ON CONFLICT` 回写用例：对同一 job 先 create-upsert（`SourceJobID="j-src"`）再 finish-upsert，回读 `source_job_id` 仍为 `"j-src"`（证明第四处 ON CONFLICT SET 未漏）**；旧库 Open→migrate 自动 ALTER + 建索引，旧行读 `""`。

---

### T2 — job 层 model：`JobRequest.SourceJobID`（`json:"-"`，服务端盖章）+ `JobResult.SourceJobID`（出网）

**`internal/job/model.go`**

(a) `JobRequest` 加字段（现状 `model.go:82-85` `PlanID` 后；**tag 用 `json:"-" yaml:"-"`，仿 `ResumeSourceAgent` `:163`**）：
```go
	// SourceJobID is the lineage key set ONLY by ResumeJob / RebuildJob (P5): it points
	// the new job back to its SOURCE job id. Like ResumeSourceAgent (model.go:163) it is
	// json/yaml "-": it is NEVER written from a client body (c.BindJSON can't set it) nor
	// from md frontmatter, and it does NOT round-trip through request_json. UNLIKE
	// ResumeSourceAgent (whose "-" is a SECURITY requirement — it exempts allow_exec),
	// here "-" is chosen to ELIMINATE the forge surface: the URL /jobs/{id}/rebuild
	// already carries the authoritative source id, so the server stamps it internally
	// (submit copies it into the JobResult → jobs.source_job_id via a plain Go assignment,
	// which the json tag does not affect). Empty == not derived.
	SourceJobID string `json:"-" yaml:"-"`
```

(b) `JobResult` 加字段（现状 `model.go:214-216` `PlanID` 后；**出网供前端展示血缘**）：
```go
	// SourceJobID is the lineage key (P5): the source job this one was resumed/rebuilt
	// from. Persisted to jobs.source_job_id (single source of truth — it is NOT in
	// request_json, see JobRequest.SourceJobID). Surfaced in show/list and via
	// ?source_job= for bidirectional lineage nav. Empty == not derived (omitempty).
	SourceJobID string `json:"source_job_id,omitempty"`
```

**验收**：`go build ./internal/job/...` 绿（贯通在 T3 后测）。

---

### T3 — job 层 persistence + submit + retry：互转 + 落库 + 重试保血缘

**`internal/job/persistence.go`**
- `toRecord`（现状 `:63` `PlanID: r.PlanID,`）后加 `SourceJobID: r.SourceJobID,`
- `fromRecord`（现状 `:135` `PlanID: rec.PlanID,`）后加 `SourceJobID: rec.SourceJobID,`

**`internal/job/submit.go`**，`entry.result` 初始化（现状 `submit.go:265` `PlanID: req.PlanID,`）后加：
```go
			PlanID: req.PlanID,
			// 血缘（P5）：ResumeJob/RebuildJob 内部盖在 req 上（源 job id）；普通 job 为空。
			// json:"-" 不影响此 Go 赋值——落 jobs.source_job_id（血缘的真源，不进 request_json）。
			SourceJobID: req.SourceJobID,
```

**`internal/job/execute.go`**，`maybeRetryJob` re-submit（现状 `:249` `req.CallerID = snap.CallerID` 旁，因 `SourceJobID` 亦 `json:"-"`、不在 request_json，须从快照恢复否则重试丢血缘）：
```go
	req.CallerID = snap.CallerID
	// P5: SourceJobID 亦 json:"-"（不入 request_json），从快照恢复，使派生 job 的重试保留血缘。
	req.SourceJobID = snap.SourceJobID
```

> schedule sweep（`serve.go:710`）从 `ScheduleRecord.RequestJSON` 起的是**非派生**新 job，`source_job_id` 天然空——无需改。

**验收**：`go test ./internal/job/...` 绿；扩展 submit 测试：`req.SourceJobID="j-src"` → `GetJob` 回读 `SourceJobID=="j-src"`、`ListJobs{SourceJob:"j-src"}` 命中；带 `Retry` 的派生 job 失败重试后新 attempt 仍带 `SourceJobID`。

---

### T4 — job 层 resume：续投 job 盖 `SourceJobID`（P4 已落 `PlanID`，只追加一行）

**`internal/job/resume.go`**，`ResumeJob` 的 `s.Submit(JobRequest{...})`（实测 2026-07-10：`EscalateTo: src.EscalateTo,` 约 `:94`，其后**已有** `PlanID: src.PlanID,` 约 `:98`，`})` 约 `:99`）。**只在 `PlanID` 旁追加 `SourceJobID: jobID,`**，最终形态如下（PlanID 行 P4 已在，勿重复加）：
```go
		OriginAgent: src.OriginAgent,
		EscalateTo:  src.EscalateTo,
		// 续跑归组（P4，已落）：续投 job 继承源 plan_id。
		PlanID: src.PlanID,
		// 血缘（P5，本次追加）：续投 job 指回源 job。resume 语义 = source_job_id=源 id 且
		// SessionID 与源相同（上面 :84 已带 SessionID=src.SessionID）——据此区分"续会话"
		// （rebuild 则 session 空/新）。
		SourceJobID: jobID,
	})
```

> **✅ R1 已定论（v0.3）**：P4 **已合并 push**，`PlanID: src.PlanID,` 已在位（约 `:98`）。T4 **只追加** `SourceJobID: jobID,` 一行；**不要**再写 `PlanID`（重复字段编译不过）。二者正交、仅文本相邻。实施前扫一眼 resume.go 确认 `PlanID` 行仍在（应在）。

**验收**：`go build ./... && go vet ./...` 绿；扩展 `internal/job/resume_test.go`：源 job 带 `SessionID` → resume 新 job `SourceJobID==<源 id>` 且 `SessionID==<源 session>`（据此判 resume 而非 rebuild）。

---

### T5 — list `?source_job=` 反查（job.Service / HTTP / client / CLI）

**`internal/job/list.go`**
- `ListOpts`（现状 `list.go:32-33` `Plan` 后）：
```go
	// SourceJob, when non-empty, keeps only jobs whose source_job_id matches exactly
	// (P5, ?source_job=：列出某 job 直接派生出的所有 job)。
	SourceJob string
```
- DB 映射（现状 `list.go:82` `Plan: opts.Plan,`）后加 `SourceJob: opts.SourceJob,`
- 内存 overlay（现状 `list.go:121-123` `Plan` 块后，逐维一致）：
```go
		if opts.SourceJob != "" && snap.SourceJobID != opts.SourceJob {
			continue
		}
```

**`internal/httpapi/job_handler.go`**，`handleListJobs`（现状 `:141` `Plan: c.Query("plan"),`）后加 `SourceJob: c.Query("source_job"),`（并补 `:124-125` 注释）。

**`internal/client/client.go`**，`ListJobs`（plan query 拼装后，仿 `?plan=`）加：
```go
	if opts.SourceJob != "" {
		q.Set("source_job", opts.SourceJob)
	}
```

**`internal/commands/job.go`**
- `jobListOpts`（`:77`）加 `sourceJob string`
- flag（现状 `:193` `plan` flag 后）：`c.StrOpt(&jobListOpts.sourceJob, "source-job", "", "", "filter by source job id (list jobs derived from it)")`
- `runJobList` 映射（现状 `:592` `Plan: jobListOpts.plan,`）后加 `SourceJob: jobListOpts.sourceJob,`

**验收**：`go test ./internal/job/... ./internal/httpapi/... ./internal/commands/...` 绿；冒烟 `gofer job list --source-job <id>` 命中（DB+overlay 两路）。

---

### T6 — 抽 `internal/secret` 叶包 + `workflow` 委托（零行为重构，G023）

> **动机**：脱敏原语困在 `internal/job/workflow`（`export.go:40-62`），而 `internal/job` 不能 import workflow（`workflow` 18 文件 import `job`，反向成环，G024）。抽中性叶包 `internal/secret`（仅 import `regexp`），workflow + job 双方复用。

**新建 `internal/secret/redact.go`**（逐字迁 `export.go:13,26-62` 并导出）：
```go
// Package secret holds credential-redaction primitives shared by the workflow export
// path and the job request-redact path. Leaf package (imports only regexp) so both
// internal/job and internal/job/workflow reuse it without a cycle (internal/job cannot
// import internal/job/workflow — G024).
package secret

import "regexp"

// Placeholder replaces a value that matched a secret pattern (SR403). Structure is
// preserved so a redacted request/spec stays a runnable template; the real value must
// be filled back in before re-running.
const Placeholder = "***REDACTED***"

var secretKVPattern = regexp.MustCompile(
	`(?i)\b([\w.\-]*(?:secret|token|password|passwd|api[_\-]?key|access[_\-]?key|private[_\-]?key|auth|bearer|credential)[\w.\-]*\s*[:=]\s*)("?[^"\s]+"?)`,
)
var secretFlagPattern = regexp.MustCompile(
	`(?i)(--?[\w\-]*(?:secret|token|password|passwd|api[_\-]?key|access[_\-]?key|private[_\-]?key|auth|bearer|credential)[\w\-]*[=\s]+)(\S+)`,
)

// RedactString replaces credential-looking assignments/flags in s with Placeholder,
// keeping the key/flag so the output stays a usable template. Returns the scrubbed
// string and whether anything was redacted. 逐字迁自 export.go:40-62。
func RedactString(s string) (string, bool) {
	if s == "" {
		return s, false
	}
	redacted := false
	out := secretFlagPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := secretFlagPattern.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		redacted = true
		return sub[1] + Placeholder
	})
	out = secretKVPattern.ReplaceAllStringFunc(out, func(m string) string {
		sub := secretKVPattern.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		redacted = true
		return sub[1] + Placeholder
	})
	return out, redacted
}
```

**改 `internal/job/workflow/export.go`**（委托，删本体 `:26-62`，`redactStepSecrets`/`redactWorkflowSecrets`/`ExportWorkflow` `:64-144` 不动）：
```go
import (
	"encoding/json"
	"fmt"

	"github.com/inhere/gofer/internal/secret"
)

const secretPlaceholder = secret.Placeholder // 保留常量名供本包既有引用

func redactSecretsInString(s string) (string, bool) { return secret.RedactString(s) }
```

**验收**：`go test ./internal/job/workflow/... ./internal/client/...` 绿（`TestExportWorkflowRoundTrip` 背书零行为）；`go list -deps ./internal/secret` 仅 stdlib。

---

### T7 — job 层：`RedactedRequest(id)` 脱敏视图（供 `GET /request` 默认返回）

**新增 `internal/job` 方法**（放新 `rebuild.go` 或 `resume.go` 尾；`internal/job` 可 import `internal/secret`）：
```go
// RedactedRequest returns the JobRequest a job was created from, SECRET-STRIPPED for
// safe read (P5, h-aii-xqe1): this is the DEFAULT and ONLY shape GET /request returns
// (there is no verbatim path — the rerun re-submit went server-side to RebuildJob). env
// values → Placeholder (wholesale — env is the main leak surface; a redacted read only
// SHOWS keys, values never leave the server), and prompt/cmd/cwd/system_prompt/agent_args
// run through secret.RedactString (workflow parity; agent_args is the --flag secret
// vector, v0.3). It also CLEARS read-noise: RequestID,
// CallerID, SessionID (a request read must not resurrect an idempotency key or a session
// binding). The second bool reports whether anything was redacted (surfaced via
// X-Gofer-Redacted so a UI marks fields as "originals kept server-side").
//
// Cwd is already the ORIGINAL RELATIVE path (parsed out of request_json, marshalled
// pre-resolution submit.go:110 — NOT reconstructed from JobResult.Cwd's absolute path).
// ok=false when the job is unknown or has no stored request.
func (s *Service) RedactedRequest(id string) (JobRequest, bool, bool, error) {
	src, found := s.Get(id)
	if !found || src.RequestJSON == "" {
		return JobRequest{}, false, false, nil
	}
	var req JobRequest
	if err := json.Unmarshal([]byte(src.RequestJSON), &req); err != nil {
		return JobRequest{}, false, false, fmt.Errorf("decode request_json of %q: %w", id, err)
	}
	redacted := false
	for k := range req.Env { // wholesale value redaction, keep keys (show what exists)
		if req.Env[k] != "" {
			req.Env[k] = secret.Placeholder
			redacted = true
		}
	}
	if r, hit := secret.RedactString(req.Prompt); hit {
		req.Prompt, redacted = r, true
	}
	if r, hit := secret.RedactString(req.SystemPrompt); hit {
		req.SystemPrompt, redacted = r, true
	}
	if r, hit := secret.RedactString(req.Cwd); hit {
		req.Cwd, redacted = r, true
	}
	for i := range req.Cmd {
		if r, hit := secret.RedactString(req.Cmd[i]); hit {
			req.Cmd[i], redacted = r, true
		}
	}
	// v0.3 真漏洞修复：AgentArgs 是 cli-agent 追加到 argv 的 CLI flags（model.go:14-16），
	// 是 `--token=x`/`--api-key=x` 类 secret 的首要向量——必须同 Cmd 一样脱敏，否则明文外泄。
	for i := range req.AgentArgs {
		if r, hit := secret.RedactString(req.AgentArgs[i]); hit {
			req.AgentArgs[i], redacted = r, true
		}
	}
	req.RequestID, req.CallerID, req.SessionID = "", "", ""
	return req, true, redacted, nil
}
```

> 返回 `job.JobRequest`（omitempty 隐藏清零字段），**不建 view struct**（前端要完整形状，`json:"-"` 字段本就不出）。`SourceJobID` 因 `json:"-"` 天然不出网（血缘只在 `JobResult.SourceJobID`）——正确。

**验收**：`go test ./internal/job/...` 绿；单测：源 job 带 `Env{"API_TOKEN":"xyz"}`+`RequestID`+`SessionID` → env 值为 `***REDACTED***`、request_id/session_id 空、`redacted==true`；**源 job 带 `AgentArgs:["--api-key=sk-xxx"]` → agent_args 值被脱敏为 `--api-key=***REDACTED***` 且 `redacted==true`（v0.3 新增，防真漏洞）**；无 env/secret → `redacted==false`；未知 id → ok=false。

---

### T8 — job 层：`RebuildJob(...)` 编排（覆盖 + env merge + 盖章 + 占位符拒绝）

**新增 `internal/job/rebuild.go`**：
```go
package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/secret"
)

// ErrRedactedPlaceholder marks a rebuild whose override still carries the redaction
// placeholder — the user viewed a redacted field and re-submitted it without replacing
// the secret. HTTP: 400 (defence-in-depth; unedited fields are never sent, so a
// placeholder here means an edited-but-unreplaced value). ErrUnknownJob (404) is reused
// for an absent source job; "no request" is a 404 too.
var ErrRedactedPlaceholder = errors.New("override still contains the redaction placeholder; replace it")

// RebuildOverrides is the POST /v1/jobs/{id}/rebuild body: ONLY user-edited fields.
// Pointer scalars distinguish "not provided (nil → inherit source)" from "explicit set
// (incl. empty)". env_set adds/overrides env keys (value "" = set to empty string, NOT
// delete); env_unset deletes keys; untouched keys keep the SOURCE value (env plaintext
// never leaves the server). Fields NOT here (request_id/session_id/caller_id/
// source_job_id/workflow_id/role/retry/…) are server-controlled or silently inherited.
type RebuildOverrides struct {
	ProjectKey   *string           `json:"project_key,omitempty"`
	Agent        *string           `json:"agent,omitempty"`
	Runner       *string           `json:"runner,omitempty"`
	Prompt       *string           `json:"prompt,omitempty"`
	SystemPrompt *string           `json:"system_prompt,omitempty"`
	Cmd          *[]string         `json:"cmd,omitempty"`
	AgentArgs    *[]string         `json:"agent_args,omitempty"`
	Cwd          *string           `json:"cwd,omitempty"`
	Title        *string           `json:"title,omitempty"`
	Tags         *[]string         `json:"tags,omitempty"`
	TimeoutSec   *int              `json:"timeout_sec,omitempty"`
	Interactive  *bool             `json:"interactive,omitempty"`
	Cols         *int              `json:"cols,omitempty"`
	Rows         *int              `json:"rows,omitempty"`
	WorkerID     *string           `json:"worker_id,omitempty"`
	WorkerLabels *[]string         `json:"worker_labels,omitempty"`
	PlanID       *string           `json:"plan_id,omitempty"`
	Channel      *string           `json:"channel,omitempty"`
	EnvSet       map[string]string `json:"env_set,omitempty"`
	EnvUnset     []string          `json:"env_unset,omitempty"`
}

// RebuildJob starts a NEW job from the SOURCE job's persisted request_json (P5, D1). It
// takes that request as the base, applies the caller's edited overrides + env merge, then
// STAMPS the lineage/identity fields server-side and Submits. env plaintext never leaves
// the server: the base env (real values, from request_json) is merged in memory; only
// env_set/env_unset touch it. Default async (like ResumeJob); the caller watches the new
// job. callerID/clientIP are stamped by the HTTP entry (anti-spoof), mirroring Submit.
func (s *Service) RebuildJob(jobID string, ov RebuildOverrides, callerID, clientIP string) (JobResult, error) {
	src, ok := s.Get(jobID)
	if !ok {
		return JobResult{}, fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	if src.RequestJSON == "" {
		return JobResult{}, fmt.Errorf("%w: %q has no stored request", ErrUnknownJob, jobID)
	}
	var base JobRequest
	if err := json.Unmarshal([]byte(src.RequestJSON), &base); err != nil {
		return JobResult{}, fmt.Errorf("decode request_json of %q: %w", jobID, err)
	}
	// Defence-in-depth: reject any edited string field still carrying the placeholder.
	if err := rejectPlaceholders(ov); err != nil {
		return JobResult{}, err
	}
	applyOverrides(&base, ov) // pointer scalars + slices; env_set/env_unset merge

	// Server-controlled fields — a rebuild is a FRESH, faithful re-submit:
	base.RequestID = ""       // else the new submit dedupes onto the source (C5)
	base.SessionID = ""       // fresh job, NOT a resume — don't rebind the source session
	base.CallerID = callerID  // re-stamped from auth (anti-spoof, mirrors handleCreateJob)
	base.Client = clientIP    // new submission origin
	base.SourceJobID = jobID  // lineage: server-stamped from the URL (unforgeable, json:"-")
	// PlanID inherited from source (base already carries it); ov.PlanID may override.
	return s.Submit(base)
}

// applyOverrides mutates base with only the provided (non-nil) fields, then merges env.
func applyOverrides(base *JobRequest, ov RebuildOverrides) {
	if ov.ProjectKey != nil { base.ProjectKey = *ov.ProjectKey }
	if ov.Agent != nil { base.Agent = *ov.Agent }
	if ov.Runner != nil { base.Runner = *ov.Runner }
	if ov.Prompt != nil { base.Prompt = *ov.Prompt }
	if ov.SystemPrompt != nil { base.SystemPrompt = *ov.SystemPrompt }
	if ov.Cmd != nil { base.Cmd = *ov.Cmd }
	if ov.AgentArgs != nil { base.AgentArgs = *ov.AgentArgs }
	if ov.Cwd != nil { base.Cwd = *ov.Cwd }
	if ov.Title != nil { base.Title = *ov.Title }
	if ov.Tags != nil { base.Tags = *ov.Tags }
	if ov.TimeoutSec != nil { base.TimeoutSec = *ov.TimeoutSec }
	if ov.Interactive != nil { base.Interactive = *ov.Interactive }
	if ov.Cols != nil { base.Cols = *ov.Cols }
	if ov.Rows != nil { base.Rows = *ov.Rows }
	if ov.WorkerID != nil { base.WorkerID = *ov.WorkerID }
	if ov.WorkerLabels != nil { base.WorkerLabels = *ov.WorkerLabels }
	if ov.PlanID != nil { base.PlanID = *ov.PlanID }
	if ov.Channel != nil { base.Channel = *ov.Channel }
	// env merge: base env = source real values; set adds/overrides, unset deletes.
	if len(ov.EnvSet) > 0 && base.Env == nil {
		base.Env = map[string]string{}
	}
	for k, v := range ov.EnvSet {
		base.Env[k] = v // "" = set to empty string (NOT delete; delete via EnvUnset)
	}
	// v0.3 同 key 语义（D2）：set-loop 在前、unset-loop 在后 → 一个 key 同时出现在 env_set 与
	// env_unset 时 **env_unset 优先**（先赋值、再删除，最终不存在）。前端须避免同 key 同时进两者。
	for _, k := range ov.EnvUnset {
		delete(base.Env, k)
	}
}

// rejectPlaceholders 400s when any edited string override still carries the placeholder
// (the user must replace a redacted value, not re-submit it). Unedited fields are nil and
// inherit the SOURCE real value, so they never hit this.
func rejectPlaceholders(ov RebuildOverrides) error {
	has := func(s string) bool { return strings.Contains(s, secret.Placeholder) }
	if ov.Prompt != nil && has(*ov.Prompt) { return ErrRedactedPlaceholder }
	if ov.SystemPrompt != nil && has(*ov.SystemPrompt) { return ErrRedactedPlaceholder }
	if ov.Cwd != nil && has(*ov.Cwd) { return ErrRedactedPlaceholder }
	if ov.Cmd != nil {
		for _, a := range *ov.Cmd { if has(a) { return ErrRedactedPlaceholder } }
	}
	if ov.AgentArgs != nil { // v0.3: agent_args 同 Cmd 是 secret 向量，须一并兜底
		for _, a := range *ov.AgentArgs { if has(a) { return ErrRedactedPlaceholder } }
	}
	for _, v := range ov.EnvSet { if has(v) { return ErrRedactedPlaceholder } }
	return nil
}
```

> `RebuildJob` 走 `s.Submit`（async，与 `ResumeJob` 一致）。**rebuild 之于门禁**：它 Submit 的是**原 agent 的原请求**（非 exec 载体），故按原 agent 正常门控（exec 源 job 的 rebuild 仍需 allow_exec）——与 `job rerun` 现有语义一致，**不设** `ResumeSourceAgent`（那是 resume 专属豁免）。

**验收**：`go test ./internal/job/...` 绿；单测：源 job 带 `Env{A:1,B:2}` → `RebuildJob(id, {EnvSet:{A:9},EnvUnset:["B"]})` 的新 job env=`{A:9}`、`SourceJobID==id`、`RequestID`/`SessionID` 空、`CallerID`=传入；**同 key 语义（v0.3）：`RebuildJob(id, {EnvSet:{A:9},EnvUnset:["A"]})` → 新 job env 无 `A`（unset 优先）**；`AgentArgs:[*][]string{"--api-key=***REDACTED***"}` override → `ErrRedactedPlaceholder`（v0.3）；空 overrides → 新 job 与源同请求（仅清 id/session、盖 source/caller）；override 含 `***REDACTED***` → `ErrRedactedPlaceholder`；未知 id → `ErrUnknownJob`。

---

### T9 — httpapi：`GET /request` 默认脱敏（关 h-aii-xqe1）+ `POST /jobs/{id}/rebuild` + `?source_job=`

**`internal/httpapi/job_handler.go`**

(a) `handleGetJobRequest` **改默认脱敏、删裸吐**（现状 `:181-193`）：
```go
// handleGetJobRequest returns the SECRET-STRIPPED JobRequest a job was created from
// (P5, closes h-aii-xqe1). It no longer echoes request_json verbatim — the only verbatim
// consumer (CLI rerun) moved server-side to POST /jobs/{id}/rebuild. env values / secret-
// looking prompt/cmd/cwd/system_prompt are redacted (job.RedactedRequest); when anything
// was stripped an X-Gofer-Redacted: 1 header is set (workflow-export parity). It backs the
// web rebuild PREFILL (display only). Unknown id / no request → 404.
func (s *Server) handleGetJobRequest(c *rux.Context) {
	id := c.Param("id")
	req, ok, redacted, err := s.jobs.RedactedRequest(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read request failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "no request recorded", "job "+id+" has no stored request")
		return
	}
	if redacted {
		c.SetHeader("X-Gofer-Redacted", "1")
	}
	c.JSON(http.StatusOK, req)
}
```
> 删掉旧的 `res.RequestJSON` 裸吐 + `json.RawMessage`；`encoding/json` import 若因此无其他用途则移除（`go vet` 会提示）。

(b) 新增 `handleRebuildJob` + `rebuildStatus`（薄入口，仿 `handleResumeJob` `:294-324`）：
```go
// handleRebuildJob starts a NEW job from a source job's request + the caller's edits
// (P5, D1). 编排 in job.Service.RebuildJob (G021); the handler binds the overrides, stamps
// the authenticated caller + client IP (anti-spoof, like handleCreateJob) and maps
// sentinels. Default async — the client watches the new job. env plaintext is never in the
// request or response (only env_set/env_unset carry NEW values in).
func (s *Server) handleRebuildJob(c *rux.Context) {
	id := c.Param("id")
	var ov job.RebuildOverrides
	if err := c.BindJSON(&ov); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	res, err := s.jobs.RebuildJob(id, ov, callerFromCtx(c), clientIP(c))
	if err != nil {
		writeError(c, rebuildStatus(err), "rebuild rejected", err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// rebuildStatus: unknown source job → 404; a lingering placeholder → 400; inner Submit
// errors (unknown project etc.) via submitStatus.
func rebuildStatus(err error) int {
	if errors.Is(err, job.ErrUnknownJob) {
		return http.StatusNotFound
	}
	if errors.Is(err, job.ErrRedactedPlaceholder) {
		return http.StatusBadRequest
	}
	return submitStatus(err)
}
```

(c) `handleListJobs` 加 `?source_job=`：已在 T5。

**`internal/httpapi/server.go`**，resume 路由（现状 `:415`）后加：
```go
		// P5: rebuild a NEW job from a source job's request + edits (env stays server-side).
		// rerun = empty-body rebuild. 校验失败 4xx/404(rebuildStatus)。
		r.POST("/jobs/{id}/rebuild", s.handleRebuildJob)
```
> 限流：POST /jobs/{id}/rebuild 是提交类写。确认 `isSubmitPath`（`server.go:467`）是否纳入——现状 resume 未纳入（`:475` 注释"仅 POST /v1/jobs|/workflows"）；rebuild 与 resume 同级，暂**不纳入**，如需再议（待确认 D7）。

**验收**：`go test ./internal/httpapi/...` 绿；(1) `GET .../request` 返回脱敏体 + env 占位 + `X-Gofer-Redacted`；(2) `POST .../rebuild` 空 body → 200 新 job `source_job_id==源`；带 `env_set` → 新 job 用新值；含占位符 → 400；未知源 → 404。**改写** `internal/httpapi/job_request_test.go`：原 verbatim 断言改为"脱敏体不含 env 明文 + 含 X-Gofer-Redacted"。

---

### T10 — Go client + CLI：`GetJobRequest` 语义改 + `RebuildJob` + `rerun`→rebuild + `list --source-job`

**`internal/client/client.go`**

(a) `GetJobRequest`（现状 `:349-357`）——更新 doc（现返回**脱敏** JobRequest；不再供 rerun 重投，仅审计/展示）：
```go
// GetJobRequest fetches a job's ORIGINAL request, SECRET-STRIPPED (GET /v1/jobs/{id}/
// request, P5: the endpoint now redacts by default — env values and secret-looking
// prompt/cmd become ***REDACTED***). It is for audit/display; it is NO LONGER used to
// re-submit (that moved server-side to RebuildJob). Unknown id / no request → error.
func (c *Client) GetJobRequest(id string) (job.JobRequest, error) { /* body 不变 */ }
```

(b) 新增 `RebuildJob`（仿既有 `ResumeJob`，POST /v1/jobs/{id}/rebuild，返回新 job `JobResult`）：
```go
// RebuildJob re-runs a job from its stored request + edits (P5). Empty overrides == a
// faithful re-run (the old `job rerun`); env stays server-side (only EnvSet/EnvUnset carry
// new values). Returns the NEW job's JobResult (its source_job_id links back to the source).
func (c *Client) RebuildJob(id string, ov job.RebuildOverrides) (job.JobResult, error) {
	var res job.JobResult
	err := c.doJSON(http.MethodPost, "/v1/jobs/"+url.PathEscape(id)+"/rebuild", ov, &res)
	return res, err
}
```

**`internal/commands/job.go`**，`runJobRerun`（现状 `:696-723`）改调 rebuild 空 body（`--watch` 逻辑不变）：
```go
func runJobRerun(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job rerun requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	// P5: rerun = 服务端 rebuild 空 body（原样重投；env 全程不出服务端；血缘服务端盖 source_job_id）。
	res, err := cli.RebuildJob(id, job.RebuildOverrides{})
	if err != nil {
		return err
	}
	c.Printf("rerun of %s submitted: new job %s status=%s\n", id, res.ID, res.Status)
	if !jobRerunOpts.watch {
		return nil
	}
	return watchToTerminal(c, cli, res.ID, 0)
}
```
> 删除原 `GetJobRequest`→`req.RequestID=""`→`SubmitJobSync` 三步（清 request_id 现由服务端 `RebuildJob` 做）。`job list --source-job` 透传已在 T5。

**验收**：`go build ./... && go test ./internal/commands/... ./internal/client/...` 绿；**改写** `internal/client/client_e2e_test.go` 的 `TestGetJobRequestRoundTrip`（原断言"往返等值"——现返回脱敏；若测试 job 无 env/secret 则等值仍成立，否则放宽为"结构一致、env 值占位"）；冒烟 `gofer job rerun <id>` → 新 job `source_job_id==<id>`；`--watch` 正常。

---

### T11 — web `types.ts`：`Job.source_job_id` + `ListJobsOpts.source_job` + `RebuildRequest`(显示) + `RebuildBody`(提交)

**`web/src/api/types.ts`**

(a) `Job`（实测 2026-07-10：`Job.plan_id?` **已存在**于 `types.ts:58`，P4 已落——**只在其后追加** `source_job_id?`，勿重复加 `plan_id`）：
```ts
  escalate_to?: string
  plan_id?: string          // 归组键（P1/P4，已存在，勿重复）
  source_job_id?: string    // 血缘键（P5，本次追加）：本 job resume/rebuild 自哪个源 job
}
```

(b) `ListJobsOpts`（实测 2026-07-10：`plan?`（`:425`）/`session?`（`:428`）**已存在**，P4 已落——**只追加** `source_job?`）：
```ts
  caller?: string
  plan?: string             // 已存在（P1/P4），勿重复
  session?: string          // 已存在（P4），勿重复
  source_job?: string       // 血缘反查（P5，本次追加）：列出某 job 派生出的所有 job
  limit?: number
  offset?: number
}
```

(c) 文件末尾新增两类型：
```ts
// GET /v1/jobs/{id}/request 的脱敏结果（rebuild 预填仅用于「显示」——env 值恒为占位、
// request_id/caller_id/session_id 已清）。字段对齐脱敏后的 job.JobRequest。
export interface RebuildRequest {
  project_key: string
  agent: string
  runner: string
  prompt?: string
  system_prompt?: string
  cmd?: string[]
  agent_args?: string[]
  cwd?: string
  timeout_sec?: number
  title?: string
  tags?: string[]
  env?: Record<string, string> // 值恒为 ***REDACTED*** 占位（明文不出服务端）
  env_files?: string[]
  plan_id?: string
  interactive?: boolean
  cols?: number
  rows?: number
  worker_id?: string
  worker_labels?: string[]
}

// POST /v1/jobs/{id}/rebuild 提交体：只发用户改动过的字段（未改字段服务端继承源真值）。
// env_set 新增/改值（值不出现在 GET，只在此处提交新值）；env_unset 删除 key。
export interface RebuildBody {
  project_key?: string
  agent?: string
  runner?: string
  prompt?: string
  system_prompt?: string
  cmd?: string[]
  agent_args?: string[]
  cwd?: string
  title?: string
  tags?: string[]
  timeout_sec?: number
  interactive?: boolean
  cols?: number
  rows?: number
  worker_id?: string
  worker_labels?: string[]
  plan_id?: string
  channel?: string
  env_set?: Record<string, string>
  env_unset?: string[]
}
```

**验收**：`pnpm typecheck` 绿。

---

### T12 — web `client.ts`：`getJobRequest`（脱敏，预填显示）+ `rebuildJob` + `listJobs ?source_job`

**`web/src/api/client.ts`**

(a) `listJobs`（现状 `:239`，plan/session 透传附近）加：
```ts
  if (opts?.source_job) {
    params.set('source_job', opts.source_job)
  }
```

(b) `getJobRequest`（自带 fetch 读 `X-Gofer-Redacted`，因通用 `request<T>` 丢头 `:89-102`）+ `rebuildJob`：
```ts
// rebuild 预填「显示」源（P5）：拉脱敏 JobRequest（env 值恒占位）。返回 { request, redacted }。
export async function getJobRequest(
  id: string,
): Promise<{ request: RebuildRequest; redacted: boolean }> {
  const res = await fetch(`/v1/jobs/${encodeURIComponent(id)}/request`, {
    headers: authHeaders(),
  })
  if (res.status === 401) {
    triggerUnauthorized()
    throw new Error('未授权（401）：token 无效或已失效')
  }
  if (!res.ok) {
    return raiseForStatus(res)
  }
  const request = (await res.json()) as RebuildRequest
  return { request, redacted: res.headers.get('X-Gofer-Redacted') === '1' }
}

// rebuild 提交（P5）：只发改动过的字段 + env_set/env_unset。返回新 job（source_job_id 指回源）。
export function rebuildJob(id: string, body: RebuildBody): Promise<Job> {
  return request<Job>(`/v1/jobs/${encodeURIComponent(id)}/rebuild`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}
```
> 对齐 `authHeaders`/`triggerUnauthorized`/`raiseForStatus` 真实签名；`RebuildRequest`/`RebuildBody`/`ListJobsOpts` 补进 import。

**验收**：`pnpm typecheck` 绿；无未用 import。

---

### T13 — web `NewJob.vue`：双模式（`?from=` → 预填显示 + 快照 diff + env 编辑器 → rebuild 提交）

**`web/src/views/NewJob.vue`**

(a) 模式判定 + 新 refs（现状 refs 区 `:28-57`）：
```ts
const rebuildFrom = computed(() => (typeof route.query.from === 'string' ? route.query.from : ''))
const isRebuild = computed(() => rebuildFrom.value !== '')
const rebuildRedacted = ref(false)
const planId = ref('')   // 隐藏：rebuild 继承/可覆盖源 plan_id（若已有则复用）

// 快照：预填后记下各标量初值，提交时 diff（只发改动过的字段，N6.1——未编辑字段不回传，
// 占位符没机会回传；服务端继承源真值）。
const baseline = ref<Record<string, unknown>>({})

// env 编辑器（N5）：源 env 每 key 一行，值脱敏、初始 action='keep'（不发）。
type EnvAction = 'keep' | 'set' | 'unset'
const envRows = ref<Array<{ key: string; action: EnvAction; value: string }>>([])
const envAdds = ref<Array<{ key: string; value: string }>>([])
```

(b) 预填（`onMounted` `:266-271` 内，**loadMeta 之后**——R8 时序陷阱仍适用：须在 `selectProject()` 重置 agent/runner 之后再显式 set）：
```ts
onMounted(async () => {
  if (sessionMode.value) {
    interactive.value = true
  }
  await loadMeta()
  if (isRebuild.value) {
    await prefillFrom(rebuildFrom.value)
  }
})

// R8：selectProject 会按 allowlist 把 agent/runner 收敛到默认——必须先 selectProject 触发联动，
// 再显式覆盖 agentKey/runnerName，否则被联动重置。env 只填 key（值脱敏、不进表单值）。
async function prefillFrom(from: string): Promise<void> {
  try {
    const { request: r, redacted } = await getJobRequest(from)
    if (r.project_key) selectProject(r.project_key)   // 先联动
    if (r.agent) agentKey.value = r.agent             // 再覆盖
    if (r.runner) runnerName.value = r.runner
    if (r.cwd) cwd.value = r.cwd
    if (r.prompt) prompt.value = r.prompt             // 可能含占位；用户改则校验、不改则不发
    if (r.cmd && r.cmd.length) command.value = r.cmd.join(' ')
    if (r.title) title.value = r.title
    if (r.tags && r.tags.length) tags.value = r.tags.join(', ')
    if (r.timeout_sec) timeoutSec.value = r.timeout_sec
    if (r.interactive) { interactive.value = true; if (r.cols) cols.value = r.cols; if (r.rows) rows.value = r.rows }
    if (r.worker_id) { workerMode.value = 'id'; workerId.value = r.worker_id }
    else if (r.worker_labels?.length) { workerMode.value = 'labels'; workerLabels.value = r.worker_labels.join(', ') }
    envRows.value = Object.keys(r.env ?? {}).map((k) => ({ key: k, action: 'keep' as EnvAction, value: '' }))
    if (r.plan_id) planId.value = r.plan_id
    rebuildRedacted.value = redacted
    snapshotBaseline()   // 记初值，供提交 diff
  } catch (e) {
    submitError.value = e instanceof Error ? e.message : String(e)
  }
}

function snapshotBaseline(): void {
  baseline.value = {
    project_key: projectKey.value, agent: agentKey.value, runner: runnerName.value,
    prompt: prompt.value, command: command.value, cwd: cwd.value, title: title.value,
    tags: tags.value, timeout: timeoutSec.value, interactive: interactive.value,
    cols: cols.value, rows: rows.value, worker_id: workerId.value,
    worker_labels: workerLabels.value, plan_id: planId.value,
  }
}
```

(c) 提交分流（现状 `onSubmit` `:197-264`）——rebuild 模式走 diff → `rebuildJob`：
```ts
async function onSubmit() {
  submitError.value = ''; notice.value = ''
  if (validationError.value !== '') { submitError.value = validationError.value; return }
  submitting.value = true
  try {
    if (isRebuild.value) {
      const job = await rebuildJob(rebuildFrom.value, buildRebuildBody())
      void router.push(`/jobs/${job.id}`)
    } else {
      /* 原普通提交路径不变（:206-258）：submitJob(req) → router.push */
    }
  } catch (e) {
    submitError.value = e instanceof Error ? e.message : String(e)  // 400 占位符未替换 / 404 无源 → 显示不跳转
  } finally {
    submitting.value = false
  }
}

// 只发改动过的标量（对比 baseline）+ env_set/env_unset（来自 envRows/envAdds）。
function buildRebuildBody(): RebuildBody {
  const b: RebuildBody = {}
  const chg = <T,>(key: string, cur: T, apply: (v: T) => void) => {
    if (baseline.value[key] !== cur) apply(cur)
  }
  chg('project_key', projectKey.value, (v) => (b.project_key = v))
  chg('agent', agentKey.value, (v) => (b.agent = v))
  chg('runner', runnerName.value, (v) => (b.runner = v))
  if (isCliAgent.value) chg('prompt', prompt.value, (v) => (b.prompt = v))
  if (isExec.value) chg('command', command.value, () => (b.cmd = parseCmd(command.value)))
  chg('cwd', cwd.value, (v) => (b.cwd = v))
  chg('title', title.value, (v) => (b.title = v || ''))
  chg('tags', tags.value, () => (b.tags = parseLabels(tags.value)))
  // v0.3 零值陷阱规避：原 `v ?? 0` 会把「清空 timeout」发成 0（服务端显式清零而非继承）。
  // 改为仅当填了正整数才发；清空/0 → 不发该字段 → 服务端继承源 timeout。（真要「无超时」须另设
  // 显式选项，本期不做——见风险 R15。）
  chg('timeout', timeoutSec.value, (v) => { if (v != null && (v as number) > 0) b.timeout_sec = v as number })
  chg('interactive', interactive.value, (v) => (b.interactive = v))
  chg('plan_id', planId.value, (v) => (b.plan_id = v || ''))
  b.channel = 'web'
  // env：keep 不发；set → env_set；unset → env_unset；新增行 → env_set。
  const envSet: Record<string, string> = {}
  const envUnset: string[] = []
  for (const row of envRows.value) {
    if (row.action === 'set') envSet[row.key] = row.value
    else if (row.action === 'unset') envUnset.push(row.key)
  }
  for (const a of envAdds.value) { if (a.key.trim()) envSet[a.key.trim()] = a.value }
  // v0.3：同一 key 不同时进 env_set 与 env_unset（避免歧义；服务端亦 unset 优先）。unset 赢，
  // 故从 envSet 剔除任何也在 envUnset 里的 key。
  for (const k of envUnset) delete envSet[k]
  if (Object.keys(envSet).length) b.env_set = envSet
  if (envUnset.length) b.env_unset = envUnset
  return b
}
```

(d) 模板：标题按模式切换（「新建 job」/「快速重建」）；rebuild 时脱敏提示条 + env 编辑器（N5：源 key 列表，值 `••••`，每行「改值」→ 展开输入设 action='set'、「删除」→ action='unset' 划线；底部「新增」→ push envAdds）：
```vue
      <h1 class="title mono">{{ isRebuild ? '快速重建' : '新建 job' }}</h1>
      ...
      <div v-if="isRebuild" class="field">
        <label class="label mono">ENV（源 job 继承；值保留在服务端）</label>
        <div v-for="row in envRows" :key="row.key" class="env-row mono">
          <span class="env-key">{{ row.key }}</span>
          <template v-if="row.action === 'set'">
            <input v-model="row.value" class="control mono" placeholder="新值" />
            <button type="button" @click="row.action = 'keep'; row.value = ''">撤销</button>
          </template>
          <template v-else>
            <span class="env-val" :class="{ struck: row.action === 'unset' }">••••（保留原值）</span>
            <button type="button" @click="row.action = 'set'">改值</button>
            <button type="button" @click="row.action = row.action === 'unset' ? 'keep' : 'unset'">
              {{ row.action === 'unset' ? '恢复' : '删除' }}
            </button>
          </template>
        </div>
        <div v-for="(a, i) in envAdds" :key="'add' + i" class="env-row mono">
          <input v-model="a.key" class="control mono" placeholder="KEY" />
          <input v-model="a.value" class="control mono" placeholder="value" />
        </div>
        <button type="button" class="env-add" @click="envAdds.push({ key: '', value: '' })">+ 新增 env</button>
        <p v-if="rebuildRedacted" class="field-hint field-hint--warn mono">
          源 job 的 env / 命令含敏感值，已在服务端保留；仅改动过的字段会提交，未改字段沿用原值
        </p>
      </div>
```

> **限制（诚实标注，待确认 D6）**：NewJob 无 `agent_args` 输入，rebuild 不 round-trip 它（源值经服务端继承——只要用户不覆盖，`agent_args` 走 base 继承**不丢**；仅"想改 agent_args"本期做不到）。`sync` 复选框在 rebuild 模式不生效（rebuild 恒 async）。

**验收**：`pnpm typecheck && pnpm build` 绿；`/new?from=<jobid>` 标题「快速重建」、预填源 job 各字段、env 显示 key + `••••`；改一个 env 值 + 删一个 key + 改 prompt → 提交只发这三项（env_set/env_unset + prompt），跳新 job；不改任何字段直接提交（空 body）≡ 原样重投；提交 400（占位符未替换）显示错误不跳转；无 `?from=` 时行为与原 NewJob 一致。

---

### T14 — web `JobDetail.vue`：「快速重建」按钮（所有 job）+ `source_job_id`/`plan_id` meta

**`web/src/views/JobDetail.vue`**

(a) 「快速重建」按钮（现状 `.head-right` `:870-892`；**所有 job 显示**，D9/N9；与「继续会话」并存不互斥）：
```vue
        <RouterLink
          class="rebuild-btn mono"
          :to="`/new?from=${encodeURIComponent(job.id)}`"
          title="用本 job 的参数预填新建表单，提交为一个新 job（env 保留在服务端）"
        >
          快速重建
        </RouterLink>
```

(b) 血缘 meta（现状 meta 区 `:927-930` session_id 附近）：
```vue
      <div v-if="job.source_job_id" class="meta-item">
        <span class="meta-k mono">派生自</span>
        <RouterLink class="meta-v mono" :to="`/jobs/${encodeURIComponent(job.source_job_id)}`">
          {{ job.source_job_id }}
        </RouterLink>
      </div>
```
> `plan` meta 行 P4 已加（P4 已合并）；`/plans/:id` 路由**已可用**（P4 落地 Plans 列表/详情页）。本期只加「派生自」meta 行落在其旁即可。`RouterLink` 本模板已用（`:869`）。

(c) 样式 `.rebuild-btn` 复用 `.terminal-open`/`.cancel` 视觉（`:1307` 附近）；`.env-row`/`.env-key`/`.env-val.struck`（划线）/`.env-add`（T13 用）。

**验收**：`pnpm typecheck && pnpm build` 绿；任一 job 详情出现「快速重建」→ 跳 `/new?from=<id>`；派生 job 显示「派生自 <源 id>」可点回源；`gofer job list --source-job <源>` / `?source_job=` 反查其派生 job（双向可达）。

---

### T15 — 验证门禁（收尾）

**后端（容器内）**
- [ ] `go build ./... && go vet ./...` 绿（注意 job_handler.go 删裸吐后 `encoding/json` 若无其他用途需移 import）。
- [ ] `go list -deps ./internal/secret` 仅 stdlib（叶包无环）。
- [ ] `go test ./internal/jobstore/... ./internal/job/... ./internal/job/workflow/... ./internal/httpapi/... ./internal/client/... ./internal/commands/...` 绿。
- [ ] 全量 `go test ./...` 绿（workflow export 既有测试证明 T6 零行为）。

**前端（主机跑；容器内无 node → `gofer job run -p <project> -a exec --runner local --sync -- bash -lc 'cd <gofer>/web && pnpm typecheck && pnpm build'`）**
- [ ] `pnpm typecheck` 绿（T11~T14）。
- [ ] `pnpm build` 绿（`/new` 懒加载解析）。

**运行期冒烟（可选）**
- exec 一次性 job → 详情「快速重建」→ 预填（env 显 key/占位）→ 改 env 值 + 改 prompt → 提交 → 新 job `source_job_id` 指回源、`gofer job list --source-job <源>` 命中；DB 里源 env 明文未泄漏到任何响应。
- 带 env-secret 的 job → `GET .../request` 返回 env 占位 + `X-Gofer-Redacted:1`（不含明文）。
- `gofer job rerun <id>`（= 空 body rebuild）→ 原样重投、新 job 带血缘、`--watch` 正常。
- resume 一个 session job（P4 已落 UI）→ 新 job `source_job_id`=源、`session_id`=源（区分 resume vs rebuild）。

**收尾**
- [ ] 关闭安全 issue `h-aii-xqe1`（默认脱敏已落）。
- [ ] 更新总纲 P5 checkbox + 分子阶段提交（SR1202）。

---

## 测试清单汇总

| 层 | 文件 | 用例要点 |
|---|---|---|
| jobstore | `internal/jobstore/jobs_test.go`（扩展） | source_job_id 加列回读；`ListJobs{SourceJob}` 过滤；**`ON CONFLICT` 回写：同一 job create-upsert 后 finish-upsert，`source_job_id` 仍正确（证第四处未漏）**；旧库 migrate ALTER+索引，旧行读 `""` |
| job | `internal/job/*_test.go`（扩展） | submit 带 SourceJobID 落库回读；`ListOpts{SourceJob}` DB+overlay；resume 新 job `SourceJobID==源`；**retry 保血缘**（带 Retry 的派生 job 重试后仍带） |
| job（脱敏） | `internal/job/*_test.go`（新） | `RedactedRequest`：env 值→占位、request_id/session_id 清、redacted 位；**`AgentArgs:["--api-key=sk-xxx"]`→脱敏为 `--api-key=***REDACTED***` 且 redacted=true（v0.3 真漏洞）**；无 secret→false；未知 id→ok=false |
| job（rebuild） | `internal/job/rebuild_test.go`（新） | 空 overrides≡原样重投（仅清 id/session、盖 source/caller）；`env_set`/`env_unset` merge；未提 env key 保留原值；**同 key 同进 env_set+env_unset → unset 优先（key 最终不存在）（v0.3）**；占位符 override（含 `AgentArgs`）→`ErrRedactedPlaceholder`；未知源→`ErrUnknownJob` |
| secret | `internal/secret/redact_test.go`（新，可选） | `RedactString` KV/flag 命中；空串→(空,false) |
| workflow | 既有 `TestExportWorkflowRoundTrip` | 零行为回归（抽包后仍 redacted=true + 可再导入） |
| httpapi | `internal/httpapi/job_request_test.go`（**改写**）+ 新 rebuild 测试 | GET /request 脱敏体（无 env 明文 + X-Gofer-Redacted）；rebuild 空 body/env_set/占位 400/未知 404 |
| client/commands | `client_e2e_test.go`（**改写**）+ 扩展 | GetJobRequest 现返回脱敏；`RebuildJob` 往返；`job rerun`→rebuild 新 job 带 source_job_id；`list --source-job` |
| 前端 | —（无测试框架） | `pnpm typecheck && pnpm build`；冒烟见 T15 |

**核心不变量**
- **env 真值不进 gofer 的请求/响应序列化**（v0.3 诚实降级——**非**「永不出服务端」）：`GET /request` 脱敏（env 值占位）；`JobResult`（get/list/rebuild 返回）无 `Env` 字段；源 env 真值只在服务端（request_json + `RebuildJob` 内存合并）。**注意**：这不覆盖 job 自己写进日志/结果的内容——rebuild 继承 env + 覆盖执行体仍可让 job 把 env 打进 `logs/stdout`/`result_json` 再读回（见下条）。
- **⚠️ 残留 exfiltration（已知、用户拍板接受）**：持 token 的 caller 可 rebuild 他人 job（继承其 env）+ 覆盖 `prompt`/`cmd`/`agent_args` → 新 job 打印 env → 经 job 日志端点读回。属 gofer 单信任层模型内、不阻断；缓解=血缘审计（`source_job_id` + 发起者 `caller_id`）。详见 §核心约束「安全声明」。
- **`source_job_id` == URL 里的源 job id 且不可客户端伪造**：`RebuildJob`/`ResumeJob` 服务端盖章；`JobRequest.SourceJobID` `json:"-" yaml:"-"`（`c.BindJSON`/frontmatter 设不了、不入 request_json）；血缘真源是 `jobs.source_job_id` 列（经 toRecord/fromRecord 贯通）。
- **空 body rebuild ≡ 旧 `job rerun` 行为**：继承源全部字段，仅服务端清 request_id/session_id、盖 caller/source_job_id → 忠实重投。
- **血缘单键区分**：`source_job_id!="" && session_id==源.session_id` ⇒ resume；`source_job_id!="" && session_id 空/新` ⇒ rebuild。不存派生类型列。
- **默认端点无裸吐**：`GET /request` 只有脱敏一种形态；内部 `request_json` 读者（retry/schedule/pty）直接读字段、不经端点、不受影响。
- **脱敏占位符双防线**（指 `***REDACTED***`）：未编辑字段不回传（前端 diff）+ 服务端 `rejectPlaceholders` 400 兜底。
- **SQL 同步四处**（v0.3 由三处更正为**四处**，与上一条无关）：`UpsertJob` 的 ① 列名 = ② `VALUES` 里 `?` 数 = ④ `Exec` 实参数 = **35**（现状实测 34/34/34），**外加 ③ `ON CONFLICT(id) DO UPDATE SET` 需加 `source_job_id=excluded.source_job_id`**；`jobstore/jobs.go` 的 `INSERT INTO jobs` **四处必须同增，漏一处静默错列**（③ 最易漏——finish 二次 upsert 时不回写）。
- **retry 保血缘**：`maybeRetryJob` 从快照恢复 `source_job_id`（因 `json:"-"` 不在 request_json）。

## 风险

- **R1 resume.go 合并（v0.3 已解）**：P4 **已合并 push**，`PlanID: src.PlanID` 已在位（约 `:98`）。T4 **只追加** `SourceJobID: jobID`，**勿重复加 PlanID**（重复字段编译不过）。
- **R2 默认端点删裸吐的连带**：`handleGetJobRequest` 删 `res.RequestJSON` 后 `encoding/json` import 可能无用途（`go vet` 报）；`job_request_test.go`/`client_e2e_test.go` 的 verbatim 断言须改写（T9/T10 验收已列）。**务必确认全仓无其他消费者依赖裸吐**——已核验仅 CLI rerun（服务端化后消失）+ 测试；内部 `execute.go/serve.go/pty` 读字段非端点。
- **R3 rerun 由 sync 变 async**：改调 rebuild 后不带 `--watch` 时打印状态可能是 queued/running（原 SubmitJobSync 可能已 done）。行为等价（重投同请求），仅初始状态显示差异；`--watch` 完全一致。若需严格保 sync，rebuild 端点/方法加 sync 支持（本期不做，D7）。
- **R4 retry 丢血缘**（若漏 T3 execute.go 那行）：`source_job_id` 是 `json:"-"`、不在 request_json，`maybeRetryJob` 不显式恢复则派生 job 的重试丢血缘。必须加 `req.SourceJobID = snap.SourceJobID`。
- **R5 web 读不到脱敏头**：通用 `request<T>` 丢头。`getJobRequest` 必须自带 fetch 读 `X-Gofer-Redacted`。
- **R6 抽叶包破坏 workflow**（T6）：正则/占位符逐字迁；以 `TestExportWorkflowRoundTrip` 背书，改完先跑 `go test ./internal/job/workflow/...`。
- **R7 预填时序**（T13）：`prefillFrom` 须在 `loadMeta()` 后、且 `selectProject()`（按 allowlist 重置 agent/runner）之后再显式 set agent/runner，否则被联动覆盖。
- **R8 diff 漏发/误发**（T13）：`buildRebuildBody` 靠 baseline 快照对比——快照须在预填完成后立即打；只发变化项。env 的 keep 行绝不发（否则占位符 `••••` 或空值污染源 env）。envAdds 空 key 跳过。
- **R9 命名混淆 source**：新列 `source_job_id`（血缘）≠ 既有 `source` 列（执行位置）。查询用 `source_job`/`SourceJob`/`?source_job=`。
- **R10 rebuild 门禁与 exec**：rebuild 忠实重投原 agent 请求（非 exec 载体），exec 源 job 的 rebuild 仍需 allow_exec（与 rerun 现语义一致，正确）。**不**设 `ResumeSourceAgent`（那是 resume 专属豁免）。
- **R11 残留 env exfiltration（安全声明，用户拍板接受）**：rebuild 继承源 env + 覆盖执行体 → caller 可让新 job 打印 env、经日志读回。非阻断（信任模型内），缓解=血缘审计。**实施要点**：务必做好 T7/T8 的 `AgentArgs` 脱敏（否则连**直读**都泄）。详见 §核心约束「安全声明」。
- **R12 `rejectPlaceholders` 误杀合法占位串**：用户合法想提交字面含 `***REDACTED***` 的 prompt（如写脱敏文档）会被 400（`strings.Contains`、大小写敏感、部分匹配）。**接受**（罕见，且属防御纵深；绕过它只是提交了字面占位符、无害）。如成为痛点，再改为「仅当该字段值 == 源脱敏占位且未变」才拒。
- **R13 MCP 层**：`internal/mcpserver` 的 `GetJob`/`ListJobs` 返回 `job.JobResult` → 加了 `JobResult.SourceJobID` 后血缘**自动透出**，无需改 MCP。**不经 MCP 做 rebuild**（MCP 仅 `RunJob/GetJob/CancelJob`，无 rebuild/rerun 语义）——本期不新增 MCP 工具。
- **R14 rebuild 不进限流**：`isSubmitPath`（`ratelimit.go:53`）精确匹配 `/v1/jobs`、`/v1/workflows`，`/jobs/{id}/rebuild` **不受 E17 限流**——与 resume 一致（轻微 DoS 面）。**接受**（D7）；若 rebuild/rerun 提交量大再纳入。
- **R15 前端 timeout 零值陷阱（v0.3 已规避）**：原 `b.timeout_sec = v ?? 0` 会把「清空 timeout」发成显式 0（服务端清零而非继承）。已改为**仅正整数才发**（清空→不发→继承源）。代价：本期前端**无法**表达「把源的 timeout 改为无超时」——需要时另设显式选项。

## 待确认（开放点结论）

> 任务已拍板 N1~N10 为设计约束，本节落实现取舍。标「已定」按此实施；「待用户拍板」给倾向、可用倾向开工。

- **D1 rebuild body 可覆盖字段（已定）**：覆盖面 = NewJob 表单可编辑项 + `plan_id` + env → `project_key/agent/runner/prompt/system_prompt/cmd/agent_args/cwd/title/tags/timeout_sec/interactive/cols/rows/worker_id/worker_labels/plan_id/channel` + `env_set/env_unset`（`RebuildOverrides`，T8）。**禁止覆盖**（服务端控制/静默继承）：`request_id`（清）、`session_id`（清）、`caller_id`（盖）、`source_job_id`（盖）、`workflow_id/step_index/attempt/fan_index`（引擎私有，rebuild 起独立 job）、`role/retry/origin_agent/escalate_to/record_pty`（继承源，不开放覆盖——收敛面）。

- **D2 `env_set:{K:""}` 语义 + 同 key 冲突（已定，v0.3 补全）**：`env_set:{K:""}` = 设为空串（**非**删除）；删除只走 `env_unset`。**同 key 同时出现在 `env_set` 与 `env_unset` 时：`env_unset` 优先**（`applyOverrides` set 循环在前、unset 循环在后 → 先赋值再 delete，最终不存在）。前端 `buildRebuildBody` 亦保证同 key 不同时进两者（v0.3：`for (const k of envUnset) delete envSet[k]`）。

- **D3 未提供 vs 显式清空（已定）**：指针语义。`*string`/`*int`/`*bool`/`*[]string`：nil=未提供（继承源）、非 nil（含 `""`/空 slice）=显式设。理由：`encoding/json` unmarshal 缺失字段→nil 指针、存在字段（含空值）→非 nil 指针，天然区分（Go idiom，避免"零值 vs 未设"歧义）。前端 `buildRebuildBody` 只在 diff≠baseline 时置字段 → 天然对应"非 nil=改过"。

- **D4 rebuild handler 落点 + G021（已定）**：编排全落 `internal/job.RebuildJob(jobID string, ov RebuildOverrides, callerID, clientIP string) (JobResult, error)`（merge/env 合并/清理/盖章/占位符校验）；`handleRebuildJob` 只 `BindJSON` + 盖 caller/clientIP + `rebuildStatus` 映射。占位符校验 `rejectPlaceholders` 在 job 层（返回 `ErrRedactedPlaceholder`→400）。

- **D5 R8 时序在新方向仍适用（已定）**：适用。最终顺序 = `await loadMeta()` → `prefillFrom()` → 内部 `selectProject(r.project_key)`（触发 allowlist 联动、重置 agent/runner 默认）→ **再**显式 `agentKey.value=r.agent`/`runnerName.value=r.runner` 覆盖 → `snapshotBaseline()`。env 只填 key（值脱敏不进表单值）。

- **D6 rebuild 提交错误呈现（已定）**：`rebuildJob` 抛 `ApiError` → `onSubmit` catch 置 `submitError`（不跳转）。400（占位符未替换/校验失败）、404（源 job 无 request）均复用现有 `submitError` 错误条（`:509`）。成功 → `router.push(/jobs/<新id>)`。

- **D7 rebuild 是否纳入限流/支持 sync（待用户拍板，倾向：暂不）**：rebuild 与 resume 同级——resume 现未纳入 `isSubmitPath` 限流、也不支持 sync（async）。**倾向**沿用：rebuild 暂不纳入限流、恒 async（`rerun` 由此 async，R3 行为等价）。若后续 plan/rebuild 提交量大或 rerun 需 sync 反馈再评估（加 `isSubmitPath` 分支 / body `sync` → SubmitSync）。

- **D8「快速重建」显示条件（已定，N9）**：**所有 job 显示**。rebuild 只需 request_json（每 job 都有），且与「继续会话」（仅 session job）意图不同（fresh 克隆 vs 续接上下文），并存不互斥。

- **D9 caller 归属校验（已定，v0.3，用户拍板）**：rebuild / resume / rerun **均不做** caller 归属校验——任意 token 可重建/续接任意 job。属 gofer 既有单信任层模型（resume/rerun 现也无校验），用户拍板**接受**（关联 R11 安全声明）。**被否决**：只给 rebuild 加归属校验——会破坏"重建他人 job 协作调试"用例，且与 resume/rerun 语义不一致。缓解靠血缘审计（`source_job_id` + 发起者 `caller_id` 可追溯"谁重建了谁"）。

- **D10 MCP 不新增（已定，v0.3）**：血缘随 `JobResult.SourceJobID` 从 MCP `GetJob`/`ListJobs` **自动透出**（无需改）；**不经 MCP 暴露 rebuild/rerun**（MCP 仅 `RunJob/GetJob/CancelJob`）。见 R13。
