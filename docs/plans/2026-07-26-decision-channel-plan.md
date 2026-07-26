# 决策通道 gofer_ask_human 实施计划(C-T4~T6)

> 设计:[`docs/design/2026-07-18-session-handoff-and-pty-ux-design.md`](../design/2026-07-18-session-handoff-and-pty-ux-design.md) **§C3(:347)/§C4/§C5**
> 前置:C-T1/C-T2 已落地(`3772687` feat(plan): todo lifecycle)
> bd issue:`tools-frx`　基线:`3772687`

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-07-26 | Kimi | 初版。按 §C3/§C5 把 C-T4~T6 拆成 T0~T5,所有落点经代码核实(§4)。设计未写死的 6 个语义点给出显式决策(§6),防止实施者各自发挥 |
| v0.2 | 2026-07-26 | Kimi | **一审(对抗式子代理)后修:1 BLOCKER + 4 HIGH + 6 MED,全部有代码证据**:<br>① **🔴 B1 懒过期 SQL 时间单位错误** —— v0.1 写 `asked_at + timeout_sec*1000 <= ?`,但全仓时间戳是**秒**(`unixNow()` workflows.go:14),`*1000` 把 1800s 变成 ~20.8 天,懒过期永不触发,验收 2/4 必红。→ 改 `asked_at + timeout_sec <= ?`(T0)。<br>② **🟠 H1 client 模式无处可调 ExpireDecision(计划自相矛盾)** —— v0.1 的 T2 step4 让 handler 调 `ExpireDecision`,但 Backend 接口只有 Ask/Get,client 模式要 expire 必须走 HTTP 而路由清单没有 expire 端点。→ **定稿:删掉主动 expire**。handler 到 deadline 后重查一次 `GetDecision`,读路径懒过期(store 侧)已会置 EXPIRED;两路径同一 `timeout_sec` 语义,时钟偏移 δ 只造成提前/延后 δ,不会双判或悬挂。store 不再暴露独立 `ExpireDecision`,只保留 `expireDueDecisions()`。<br>③ **🟠 H2 EscalationBell 合并被严重低估** —— bell 条目操作全部硬编码 job interaction:`submitAnswer`→`answerInteraction(item.job_id, ...)`(EscalationBell.vue:149)、`gotoJob`→`/jobs/{job_id}`(:96-99)、`submitPunt`(:166)、模板按 `item.type` 分支(:250/:263);toast 硬编码文案与跳转(InteractionToast.vue:24-40)。decision 直接投影进去会 404/错跳。→ T4 改为**可联合类型 `{source:'interaction'|'decision', ...}` + 模板分源渲染 + toast 参数化**。<br>④ **🟠 H3 InteractionCard 对 EXPIRED 形态不成立** —— 卡片只把 `status==='answered'` 当只读(:17),status 联合无 expired(types.ts:442-443);cancelled/expired 会落入可点的 confirmation 按钮分支(:138-155),EXPIRED→answered 显示"已提交:<空白>"假回显;pending 投影还会带出无语义的 punt 按钮(:19-21)。→ T4 明确**改 InteractionCard**:加 expired 只读展示态 + 投影侧隐藏 punt。<br>⑤ **🟠 H4 PlanDetail 轮询门不含 OPEN decisions** —— 轮询只在 `isActive = running+queued>0` 时存活(PlanDetail.vue:34-37/:213);规划期提问(create_plan 后、无 running job)页面不刷新。→ T4:isActive 并入"存在 OPEN decision"。<br>⑥ 🟠 M1+M6:`GetDecision` 签名 `(PlanDecision, bool, error)`(todos.go:115);answer handler 先 Get(!ok→404)再条件 UPDATE(!ok→409)。<br>⑦ 🟠 M2:answer 前也跑 `expireDueDecisions()`,堵"超时后无人读、answer 又成功"的审计缝隙。<br>⑧ 🟠 M3:timeout clamp 下限 10s→**2s**(验收 2 与 T5 演练的超时分支各 ≥10s 太贵)。<br>⑨ 🟠 M4:`gofer_ask_human` 按 add_todo/update_todo 先例**全量注册**(scoped 模式不裁剪;scope_test.go:63 的 `len(sc)==len(op)-2` 断言不受影响)。<br>⑩ 🟠 M5:409 映射是**有意偏离** interaction 先例(interactionStatus 把非 pending 映 400,:162-174),理由:decision 无 terminal job 概念,404/409 二义必须分开。<br>⑪ L2:ask 带 plan_id 时先校验 plan 存在(404,仿 handleAddPlanTodo :270-276),防悬空 plan_id。<br>⑫ L4:T5 范式文档落点定为 `skills/gofer-usage/SKILL.md`(:15 已有"长任务进度跟进"指针)+ `references/commands.md`。<br>⑬ L1:风险节措辞修正——go-sdk v1.6.1 对 tool call 一律 `jsonrpc2.Async`(server.go:1443-1446),ask 阻塞期间**服务端不排队**。<br>⑭ L3:MCP 面只给 ask(不给 list/answer)补一句理由:作答面在人,发现面在 web。 |
| v0.3 | 2026-07-26 | Kimi | **二审(独立子代理,复核一审修法+新视角)后修:无 BLOCKER,2 HIGH + 5 MED**。一审修法逐条推演全部站住(B1 秒级 ✅、H1 时序闭环含"超时瞬间作答→409"与"Get 已 expired→条件 UPDATE 兜底 409"✅、H4 轮询"先 fetch 后停"终态不丢 ✅)。新修:<br>① **🟠 HIGH-1 writeMu 锁组合写死** —— v0.2 让 `AnswerDecision`"先 expireDue 再条件 UPDATE(仿 UpdateTodoStatus)",而 UpdateTodoStatus 整体包 writeMu(todos.go:180-181)、`sync.Mutex` 不可重入;若实施者整体包锁再调同样自持锁的 expireDue → **自死锁**。→ T0 写明锁纪律(仓内 SetTodoDone 先例):**`expireDueDecisions` 自持 writeMu;GetDecision/ListDecisions/AnswerDecision 调它时不持锁,随后各自取锁**。<br>② **🟠 HIGH-2 timeout clamp/缺省下沉 store 单点收口** —— v0.2 的 clamp `[2,86400]`+缺省 1800 只在 MCP 路径(T2);HTTP/CLI 面可传 0/负数(落成即过期)或超大值绕过。→ **clamp+缺省在 `InsertDecision` 强制**,MCP 侧 clamp 保留作早失败。<br>③ 🟡 decision id 生成:仓内先例 `plan_handler.go:278-279`;但 MCP standalone 走 localBackend 直连 store 不过 httpapi——**id 在 `InsertDecision` 内生成**(`dec-` 前缀,与 HIGH-2 同思路单点收口)。<br>④ 🟡 options_json 空数组归一:空数组 `[]` 会投影成"零选项的 choice 卡"(无按钮死卡)→ `InsertDecision` 把空数组归一为 NULL(=自由文本)。<br>⑤ 🟡 EscalationBell 键冲突:interaction PK 是 `(job_id, id)`(store.go:108),裸 id 跨 job 就可能撞;`:key`/`submittingIds`/`removeItem` 全按裸 id 键控(:237/:110-136)→ T4 写明改 `{source}:{id}` 复合键。<br>⑥ 🟡 web `PlanDetail` TS 接口补 `decisions?: Decision[]`(H4 与决策 section 都依赖它,v0.2 只写了 Decision 类型)。<br>⑦ 🟡 行号修正:schemaStmts 实际起于 store.go:59(plan_todos :282-292 不变);mcpserver todo 工具注册 :184-192。 |

---

## 1. 目标

**终端里跑大计划的 agent 遇决策点时,经 MCP `gofer_ask_human` 阻塞提问;人在 web(PlanDetail 决策卡片 / 全局铃铛)作答;答案从 tool 返回值流回 agent,会话原地继续。超时(缺省 30min)置 EXPIRED,agent 按预案继续,不无限阻塞。**

边界说准:

- ✅ **突破的是 §11 的远程作答边界**——那只限制 Claude Code 内建交互;MCP 上抛的决策不受限(提问方是 MCP 调用,谁答都行)。
- ⚠️ **不是通知系统的替代品**:§10/§11 零侵入观察继续有效;本通道要求 agent 按范式主动调 MCP(C-T6 才管这个约定)。
- ⚠️ **超时兜底在 agent 侧**:EXPIRED 后"按预案继续/skip+note"是范式约定,通道本身只保证返回 `{state:"expired"}`。
- ⚠️ **answer 原文返回**,serve 不做二次解释、不做权限分级(走既有 token 鉴权)。

---

## 2. 验收(每条写怎么证明)

1. **后端闭环**:单测 + httpapi e2e 证明:ask → OPEN 落库 → answer → ANSWERED 且带 answer/answered_by/answered_at;answer 不存在的 id 返回 404、重复 answer 返回 409;list `?state=OPEN` 只回未答未超时的;带悬空 plan_id ask 返回 404;`timeout_sec` 传 0/负数/超上限时被 clamp(HTTP 与 CLI 面同样生效)。
2. **MCP 阻塞回流**:mcpserver e2e(仿 `answer_gate_e2e_test.go`)证明:tool 调用阻塞中,经另一通路 answer 后 tool 返回 `{state:"answered", answer}`;小超时(`timeout_sec=2`)不答等到期返回 `{state:"expired"}` 且库里状态=EXPIRED。
3. **answer/expire 竞态**:store 单测——同一 decision 并发 `AnswerDecision` 与 `expireDueDecisions`,最终恰好一个生效(两条都是 `WHERE state='OPEN'` 的条件 UPDATE,影响行数=1 者胜),状态不留中间态,且不死锁(HIGH-1 锁纪律)。
4. **懒过期**:agent 进程被杀(无人轮询)时,`asked_at + timeout_sec`(秒)过后 list/get/answer 都不再把它当 OPEN(先证伪:注释掉懒过期调用该用例红;回归锁死 B1 的单位错误——测试断言用秒级时间戳)。
5. **web 闭环**:PlanDetail 出现决策卡片(选项/自由文本/EXPIRED 三种形态,无 punt 按钮),作答后卡片变只读回显;**规划期(无 running job)提问页面也随轮询刷新**(H4);EscalationBell 聚合 OPEN decisions,作答/跳转按 source 分流不 404,条目键不撞(`{source}:{id}`),与 supervisor 升级卡片同入口、来源标签可区分。
6. **范式文档**:`skills/gofer-usage/SKILL.md` + `references/commands.md` 含"大计划必建 plan + 决策点用 gofer_ask_human"段落,且 e2e 演练 verdict 落 `tmp/`。

验证命令:`go test ./... -p1 -count=1`(全绿);web 侧 `cd web && npm run build` 通过。

---

## 3. 任务分解

依赖:T0 → T1 → T2 → T3;T4 只依赖 T1(可与 T2/T3 并行);T5 最后。

### T0 jobstore:`plan_decisions` 表 + CRUD(C-T4 之一)

- `internal/jobstore/store.go`:`schemaStamps` 切片(store.go:59 起,`plan_todos` 在 :282-292)后追加(**所有时间戳为秒,与 `unixNow()` workflows.go:14 一致——B1**):

  ```sql
  CREATE TABLE IF NOT EXISTS plan_decisions (
    id          TEXT PRIMARY KEY,
    plan_id     TEXT,                    -- 可空(全局提问)
    title       TEXT NOT NULL,
    question    TEXT NOT NULL,
    options_json TEXT,                   -- JSON array, 可空=自由文本作答
    answer      TEXT,
    state       TEXT NOT NULL DEFAULT 'OPEN',   -- OPEN|ANSWERED|EXPIRED
    timeout_sec INTEGER NOT NULL DEFAULT 1800,
    asked_at    INTEGER NOT NULL,        -- 秒
    answered_at INTEGER,                 -- 秒
    answered_by TEXT
  );
  CREATE INDEX IF NOT EXISTS idx_plan_decisions_plan ON plan_decisions(plan_id);
  CREATE INDEX IF NOT EXISTS idx_plan_decisions_state ON plan_decisions(state);
  ```

  新表走 `schemaStmts` 即可(`applySchema` store.go:344 幂等),**不需要** `migrate()` 挂点——那是给存量表加列用的。
- 新建 `internal/jobstore/decisions.go`(仿 `todos.go` 惯例:状态常量+`ValidDecisionState`、`selectDecisionCols`+`scanDecision`、错误 wrap `jobstore: xxx: %w`):
  - `InsertDecision(d *PlanDecision) error` — **单点收口(二审 HIGH-2 + id/选项归一)**:
    - id 为空时生成:`"dec-" + now.Format(job.JobIDLayout) + "-" + job.RandomSuffix()`(仿 plan_handler.go:278-279;MCP standalone 走 localBackend 直连 store,不过 httpapi,故 id 必须在 store 层);
    - `timeout_sec` clamp 到 `[2, 86400]`、`<=0` 时取缺省 1800(**clamp 的权威位置**,三个入口面都经此);
    - `options_json` 为空数组 `[]` 时归一为 NULL(=自由文本,防"零选项死卡");
  - `GetDecision(id string) (PlanDecision, bool, error)` — 签名仿 `GetTodo`(todos.go:115);
  - `ListDecisions(state string, planID string) ([]*PlanDecision, error)`;
  - `AnswerDecision(id, answer, answeredBy string) (bool, error)` — 先 `expireDueDecisions()`(M2),再单条条件 UPDATE:`UPDATE ... SET state='ANSWERED', answer=?, answered_at=?, answered_by=? WHERE id=? AND state='OPEN'`,bool=影响行数;
  - `expireDueDecisions() error`(私有)——`UPDATE ... SET state='EXPIRED' WHERE state='OPEN' AND asked_at + timeout_sec <= ?`(秒,B1);
  - **不提供独立 `ExpireDecision`**(H1 定稿:到期收口全走懒过期,无第二作者)。
- **🔒 锁纪律(二审 HIGH-1,仓内 `SetTodoDone` todos.go:152-158 先例)**:`expireDueDecisions` **自持** `writeMu`;`GetDecision`/`ListDecisions`/`AnswerDecision` 调用它时**不持锁**,返回后各自再取锁。`sync.Mutex` 不可重入(store.go:52),违反即自死锁(T0 第一个 answer 测试就会挂死)。
- 测试 `decisions_test.go`:CRUD 往返、clamp/id/options 归一、条件 UPDATE 竞态且不死锁(验收 3)、懒过期(验收 4,秒级断言)、answer 输后状态不变、非法 state 拒绝。

### T1 client + httpapi:ask/answer/list/get(C-T4 之二)

- 路由(`internal/httpapi/server.go` `buildRouter` :341,plan 路由 :460-466 之后):
  - `POST /v1/decisions` — ask(body 含可选 `plan_id`;单入口,D1)
  - `GET /v1/decisions?state=OPEN&plan_id=` — list(铃铛/PlanDetail 用)
  - `GET /v1/decisions/{id}` — 单查(MCP 轮询用)
  - `POST /v1/decisions/{id}/answer` — body `{answer}`
- handler 新建 `decision_handler.go`,仿 `plan_handler.go`(`handleAddPlanTodo` :259);**错误映射(有意偏离 interaction 先例,M5)**:
  - ask:plan_id 非空时先 `GetPlan`,不存在 → 404(L2,仿 :270-276);
  - answer:先 `GetDecision`,!ok → **404**;再 `AnswerDecision`,!ok → **409**(已答/已过期;含"超时瞬间作答"时序:expire 先置 EXPIRED → 条件 UPDATE 0 行 → 409,二审已推演闭环)。interaction 的 `interactionStatus`(:162-174)把非 pending 映 400,但 decision 无 terminal job 概念,404/409 二义必须分开——这是新决策,不是仿。
  - `answered_by` 取 `callerFromCtx(c)`(server.go:59)。
- `handleGetPlan`(plan_handler.go)的 detail view 内联 `decisions` 数组(additive 扩展,PlanDetail 单请求拿全,复用现有轮询)。
- `internal/client/client.go` 加 `AskDecision/GetDecision/ListDecisions/AnswerDecision`(仿 `AddTodo` :700 / `UpdateTodoStatus` :721;非 2xx 走 `errorFor` :943)。
- 测试 `decision_handler_test.go`(helper 现成:`newTestServer`/`do`/`testToken`):状态码矩阵(含 404/409/悬空 plan_id/越界 timeout 被 clamp)+ `e2e_decision_test.go` 覆盖验收 1 闭环。

### T2 MCP:`gofer_ask_human`(C-T4 之三,本期最易翻车)

- `internal/mcpserver/backend.go` `Backend` 接口(:17-57)加 `AskDecision(...)` + `GetDecision(...)`(**两个,没有第三个**——H1);双实现都加:`localBackend`(backend_local.go,直连 `b.jobs.Meta()`)+ `clientBackend`(backend_client.go,走 T1 的 client 方法)。standalone 与 client 两种部署形态(commands/mcp.go:122/:148)轮询路径不同,都要能跑。
- `server.go` 注册工具(todo 工具 :184-192 之后):`gofer_ask_human{plan_id?, title, question, options[]?, timeout_sec?}`;**全量注册**,scoped 模式不裁剪(M4,同 add_todo/update_todo 先例;scope_test.go:63 断言不受影响);`server_test.go` `expectedTools` map(:107-132)加 `"gofer_ask_human": false`。
- handler 逻辑(仓内无 tool 级长轮询先例——`job.WaitAnswer` 是同进程内存 channel,**不可复用**;返回走 `ToolHandlerFor[In,Out]` 结构化输出惯例,output struct `{state, answer?, answered_by?}`):
  1. 校验 title/question 非空;`timeout_sec` 缺省 1800、clamp `[2, 86400]`(**早失败;权威 clamp 在 store InsertDecision**,HIGH-2);
  2. `AskDecision` 落库拿 id;
  3. `ticker 2s` + `deadline = now + timeout_sec` 循环 `GetDecision`:ANSWERED → 返回 `{state:"answered", answer, answered_by}`;EXPIRED(懒过期置的)→ 返回 `{state:"expired"}`;
  4. deadline 到 → **最后再 `GetDecision` 一次**(读路径懒过期会把它置 EXPIRED;若恰被答则按 ANSWERED 返回)→ 按实际状态返回(H1:不主动 expire,无 client 模式死角);
  5. `ctx.Done()`(agent 宿主断连)→ 返回 ctx err,**decision 留 OPEN**(D6,懒过期收尾)。
- MCP 面只给 ask、不给 list/answer(L3):作答面在人(MCP 代答违背通道意图),发现面在 web。
- 测试:`server_test.go` 加 round-trip;新建 `ask_human_e2e_test.go`(仿 `answer_gate_e2e_test.go` :51)用 `timeout_sec=2` 覆盖验收 2 两分支。

### T3 CLI:decision 子命令(C-T4 收尾,三面一致)

- `internal/commands/plan.go`(`NewPlanCmd` :35,仿 add-todo :106 / set-todo :120)加:
  - `gofer plan ask --plan <id?> --title --question [--option x --option y] [--timeout 30m]`(CLI 侧**不阻塞等待**,落库打印 id——阻塞等待是 MCP 的语义;D5)
  - `gofer plan decisions [--state OPEN] [--plan <id>]`
  - `gofer plan answer <decision_id> --answer "..."`(或位置参数)
- 经 `internal/client` 发 HTTP,不直连 store;clamp 由 store 兜底(HIGH-2),CLI 不重复实现。

### T4 web:决策卡片 + 铃铛聚合(C-T5,H2/H3/H4 + 二审补丁)

- `web/src/api/types.ts`:`Decision` 类型 + **`PlanDetail` 接口加 `decisions?: Decision[]`**(二审补丁⑥);`api/client.ts`:`listOpenDecisions/answerDecision`(plan/todo 系列 :558-635 旁)。
- **`InteractionCard.vue` 改造(H3)**:
  - status 处理加 `expired` 只读展示态(显示"已过期"+ question,无按钮、无假回显);现状只认 `answered`(:17),cancelled/expired 落入可点 confirmation 分支(:138-155),一并修;status 联合(types.ts:442-443)加 `'expired'`;
  - punt 按钮加隐藏条件(`canPunt` :19-21 对 decision 投影为 false)。
  - decision → Interaction 投影:OPEN→pending(options 非空→choice 型、空→question 型)、ANSWERED→answered、EXPIRED→expired。
- **`views/PlanDetail.vue`**:
  - 决策 section 插在 TODOS section(:397-428)之前,`v-for` 投影后的卡片,emits `answer` → `answerDecision` → `fetchPlan` 刷新;
  - **轮询门修正(H4)**:`isActive`(:34-37)并入 `plan.decisions?.some(d => d.state==='OPEN')`;轮询"先 fetch 后停"(:211-215)保证 EXPIRED 那轮终态不丢(二审已核实);不新增定时器。
- **`EscalationBell.vue` 分源合并(H2 + 复合键补丁⑤)**:
  - items 改可联合类型 `{source:'interaction'|'decision', ...}`;`fetchPending`(:40)并行拉 `listPendingInteractions()` + `listOpenDecisions()`,合并排序(needs_human 优先 :25-33 保留);
  - **条目键改 `{source}:{id}` 复合键**:interaction PK 是 `(job_id, id)`(store.go:108),裸 id 跨 job 可撞;`:key`(:237)、`submittingIds`/`itemErrors`/`removeItem`(:110-136)全部改复合键;
  - 条目操作按 source 分流:decision 的作答调 `answerDecision(id)`(**不是** `answerInteraction(job_id,...)` :149);点击跳 `/plans/{plan_id}`(无 plan_id 则仅展开);decision 条目**无 punt**;模板按 source 分源渲染(不动 interaction 既有分支 :250/:263);
  - 来源标签:decision 条目加"决策"标,与 supervisor 升级卡片同入口不同标;
  - `InteractionToast.vue`(:24-40)参数化标题与跳转目标,新 decision 弹"新的决策请求 · {title}"。
- 验证:`npm run build` + 手工过一遍验收 5(e2e 演练在 T5 统一做)。

### T5 范式落地 + e2e 演练(C-T6)

- 文档(L4 定稿落点):`skills/gofer-usage/SKILL.md`(:15 已有"长任务进度跟进"范式指针)+ 其 `references/commands.md`,补 §C4 五行范式(开工 create_plan → 每步 update_todo → 决策点 ask_human → 超时按预案继续/skip+note),并注明"决策点串行提问"是范式建议(宿主客户端可能串行),**非** MCP 连接限制(go-sdk 服务端并发执行 tool call)。
- e2e 演练:起隔离栈(仿 `scripts/smoke/p3/`,隔离端口、不碰 live),跑一个真实 MCP client 调 `gofer_ask_human`(`timeout_sec` 用小值)→ web API answer → 断言 tool 返回;再跑一轮不答等超时断言 EXPIRED。verdicts 落 `tmp/decision-channel/`。
- 演练记录追加到本文件 §9。

---

## 4. 现状事实(已核实,实施前可直接信)

- 建表:`schemaStmts` 切片起于 store.go:59,`plan_todos` 在 :282-292;`applySchema` :344 幂等;存量表加列才走 `migrate()` :358(如 `migratePlanTodos` :575-613)。
- **时间戳全仓为秒**:`unixNow()` workflows.go:14,todos.go:175/plans.go:116/plan_handler.go:283-284 同;web 展示 `v * 1000`(InteractionCard.vue:71)。
- store 惯例:todos.go 全文即模板(状态常量 :11、scan :48、`GetTodo` 签名 :115、`SetTodoDone` 组合方法不持锁 :152-158、`UpdateTodoStatus` 整体包 writeMu :171-205);写路径 `writeMu`(store.go:52,不可重入)+ busy_timeout 5000ms(:37);id 生成先例 plan_handler.go:278-279。
- httpapi:plan 路由 server.go:460-466;鉴权链 :508;`callerFromCtx` :59;interaction 错误映射 `interactionStatus` interaction_handler.go:162-174(非 pending→400,**不仿**,见 M5);interaction PK `(job_id, id)` store.go:108。
- MCP:Backend 接口 backend.go:17-57,local/client 双实现(测试 mockBackend 套真 clientBackend,接口加方法破坏面=两真实现);工具注册 server.go:184-192;scoped 裁剪先例 :165-177(只挡 create_plan/attach_job);`expectedTools` server_test.go:107-132;工具返回走 `ToolHandlerFor[In,Out]` 结构化输出(:601)。
- 部署:`gofer mcp` 有 server addr = client 模式(纯 HTTP 转发),否则 standalone 内嵌 jobstore(commands/mcp.go:122/:148);MCP standalone 直连 store 不过 httpapi ⇒ id/clamp 必须在 store 层收口。
- web:PlanDetail.vue(todo 渲染 :397-428,轮询门 isActive :34-37、"先 fetch 后停" :211-215)、EscalationBell.vue(5s 轮询 :12,作答 :149,跳转 :96-99,punt :166,裸 id 键控 :237/:110-136)、InteractionCard.vue(props :11/emits :13/只读 :17/punt :19-21/confirmation 分支 :138-155/已答回显 :84-91)、InteractionToast.vue(:24-40 硬编码 job interaction)、api 封装 client.ts:558-635、status 联合 types.ts:442-443。
- e2e 先例:mcpserver/answer_gate_e2e_test.go、httpapi/e2e_interaction_test.go;隔离冒烟 scripts/smoke/p3/。
- 范式文档落点:skills/gofer-usage/SKILL.md:15 + references/commands.md。
- plan 只归档不删(全仓无 DeletePlan),decisions 无级联删除问题。

---

## 5. 决策遗留(设计没写死,本计划定稿)

- **D1 单入口 ask**:只开 `POST /v1/decisions`(plan_id 可选放 body),不开 `/v1/plans/{id}/decisions`。双入口=两套校验两处漂移。
- **D2 懒过期在读路径**:MCP 轮询经 `GetDecision` 读路径触发懒过期;agent 死掉无人轮询时,list/get/answer 读路径同样先跑 `expireDueDecisions()`,保证铃铛不出现"永远 OPEN"的幽灵卡片、超时后补答不会成功(M2)。读路径带写可接受(sqlite 单写者 + writeMu + busy_timeout)。
- **D3 已答不可改**:`AnswerDecision` 条件 UPDATE 天然拒绝二次作答(409)。改答=重新 ask。
- **D4 timeout clamp [2s, 24h],权威在 store**:`InsertDecision` 单点强制(HIGH-2),MCP 侧 clamp 只是早失败;下限 2s 是为了验收 2/演练的超时分支可跑(M3);缺省 1800s 不改。
- **D5 CLI 不阻塞**:`plan ask` 落库即返回 id;阻塞等待只属 MCP tool(agent 场景)。人要等就 `plan decisions --state OPEN` 自己刷。
- **D6 ctx 取消不留 EXPIRED**:agent 宿主断连时 decision 保持 OPEN(记录可查),交懒过期收尾;不主动置 EXPIRED(那不是超时,是提问方消失,语义不同)。

## 6. 风险与对策

- **agent 宿主的 MCP tool 超时 < timeout_sec**:宿主(如 Claude Code)对 tool 调用有自身超时,可能先杀调用。对策:范式文档(T5)写明 timeout_sec 应按宿主上限设定;EXPIRED 语义兜底,通道本身无错。
- **长阻塞期间的可观察性**:go-sdk v1.6.1 服务端对 tool call 一律异步并发执行(ask 阻塞不排队其他调用);但宿主客户端侧是否串行未证实——范式建议决策点串行提问,是约定不是机制限制。
- **web 轮询放大**:铃铛 5s + PlanDetail 2.5s 各多一个/一个内联请求;OPEN decision 最长挂 24h 期间 PlanDetail 轮询不停,与跑 24h 的 job 行为相同,可接受;不引入推送(设计标注可选,本期不做)。

## 7. 不做

- 手机/外部推送(设计 §C3 标"可选")。
- decision 的 web 端主动创建(通道方向是 agent→人)。
- 多选、打分、富文本选项;answer 仅纯文本。
- decision 与 job 的直接关联(只有 plan_id;要追 job 经 plan 查)。
- decision 的级联删除(plan 只归档不删,无此问题)。

## 8. 提交节奏

按 T0→T5 逐任务提交,每个任务"代码+测试+`go test ./... -p1 -count=1` 绿"一次 commit;T4 web 单独一个 commit;T5 文档+演练记录收尾 commit 并在 `tools-frx` 留言。

## 9. 进度跟进

| 任务 | 状态 | 备注 |
|---|---|---|
| T0 jobstore | ☑ | B1 秒级;HIGH-1 锁纪律;HIGH-2 clamp/id/options 单点收口 |
| T1 client+httpapi | ☑ | 404/409 分离(M5);悬空 plan_id 404(L2) |
| T2 MCP tool | ☑ | 双 Backend;H1 无主动 expire;轮询间隔可注入(生产 2s) |
| T3 CLI | ☑ | ask/decisions/answer;不阻塞(D5);clamp 靠 store 兜底 |
| T4 web | ☑ | H2 分源+复合键/H3 卡片改造/H4 轮询门/PlanDetail 类型 |
| T5 范式+e2e 演练 | ☑ | 落点 SKILL.md+commands.md;verdicts 落 tmp/decision-channel/ |

## 10. T5 演练记录

**2026-07-26 · decision-channel smoke(scripts/smoke/decision-channel/,Windows 本机 Git Bash)**

- 栈:隔离 `127.0.0.1:19091` + 独立数据目录 `tmp/decision-channel/`;真实 `gofer.exe serve`(no-web)+ 真实 `gofer mcp` 子进程(client 模式转发该 serve),MCP 驱动为一次性 stdio client `mcpcall`(go-sdk CommandTransport)。
- **演练 1(answered)**:MCP `gofer_ask_human`(timeout_sec=30)阻塞中,经 HTTP `POST /v1/decisions/{id}/answer` 作答 → tool 返回 `{state:"answered", answer:"tonight"}`(原文流回)✅
- **演练 2(expired)**:`timeout_sec=5` 不答 → ~8s 后 tool 返回 `{state:"expired"}`;CLI `plan decisions --state EXPIRED` 可见该行,`--state OPEN` 无幽灵卡片 ✅
- **verdict:pass=17 fail=0**。verdicts:`tmp/decision-channel/out/verdicts.txt`;tool 原始返回 `out/call1.json`/`out/call2.json`;复跑:`bash scripts/smoke/decision-channel/{build-binaries,run-smoke}.sh`。
