<!-- template_id: plan; template_version: 1.2.0 -->
# web 工作台 F16 review 队列修复实施计划

> 状态：Draft 0.1 / 已获任务书批准与当前 DIRECT_CONTINUOUS 执行请求

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-26 | Codex | 收窄 review 判定，增加 caller 已看基线与 seen-all，并修正 thread.agent 来源 |

> 仅语义变化递增版本；纯 identity/provenance/进度修正沿用原版本，并在 Git/进度记录中留痕。

## 规划声明

- `thinking_mode=RIGOROUS`
- `core_objective=修复 W1 真机中历史终态 job 淹没“等你”队列，并让 resume 载体会话显示原 agent`
- `allowed_scope=Approved 0.2 §二状态定义内的 F16 corrective、caller 已看基线、seen-all HTTP/UI、固定测试和设计/W1 计划留痕`
- `non_goals=W2-W4、其他 job/review 协议、真实 gofer server/worker/config、部署、push、外部消息和硬件操作`
- `expansion_policy=DEFER_OR_REQUEST`
- `delivery_track=Full`：新增一个 HTTP write endpoint，并补足 caller 级状态语义；实现仍限现有 workbench/jobstore/httpapi/Web owner。
- `review_budget=一轮当前任务书确认；只有 CORE_BLOCKING 或 Semantic Amendment 才停止`
- `stop_condition=四个固定测试、实现、文档、验证和原子本地提交完成；或出现范围/协议/owner 冲突时停止`

## 目标与完成定义

按会话链最新 job 判定 review：`needs_review` 始终 review；未看的失败/超时/拒绝类终态 review；未看的、非 exec agent 的成功终态仅在 commits 非空或 diff 快照非空时 review；cancelled、成功无改动和 exec 成功均 done。caller 首次 GET 自动建立当前时间基线，早于基线的终态视为已看；`POST /v1/workbench/threads/seen-all` 推进基线。thread agent 使用首轮 job 的 agent，首轮为 exec 载体时优先 `OriginAgent`。

完成要求：任务书指定的四个固定测试通过；原 W1 冲突断言按新定义调整并列出；前端“等你”下拉可全部标记已看；设计追加 F16 实测记录、W1 计划状态表新增 F16；全部指定质量门有 exit code 与原始输出；变更按测试、后端、前端、文档四个功能点分别本地提交且不 push。

## 范围、排除项与授权

范围：

- `internal/workbench` 的 thread agent、review/done 投影和 caller baseline 行为。
- `internal/jobstore` 在既有 `workbench_thread_prefs` 中使用保留的 per-caller 行保存 baseline；不新增数据库或迁移框架。
- `internal/httpapi` 新增 user-only `POST /v1/workbench/threads/seen-all`，保持 SEC-01 对 job/worker caller 默认拒绝。
- Web workbench API、attention 下拉顶部“全部标记已看”与刷新/error 反馈。
- 固定测试、Approved 0.2 设计实测记录、W1 计划状态表和本计划进度。

排除：

- 不改变 `needs_review` 的人工验收语义，不增加状态枚举或兼容 fallback。
- 不改变 commits/diff 的采集格式；只读取现有 `jobs.commits_json` 与 `jobs.diff_summary`。
- 不重启/reload live server/worker，不读取或修改 `D:/work/inhere/config/win-env/gofer`，不运行真实项目或远端 worker。
- 不 push、deploy、release、迁移真实数据或发送外部消息。

- `host_or_non_offline_action=NOT_APPLICABLE`

## 输入与批准证据

- 设计依据：[2026-09-26-web-workbench-design.md](../design/2026-09-26-web-workbench-design.md)，`Approved 0.2`，§二已定义“needs_review，或终态且有未看的改动”。
- 上游计划：[2026-09-26-web-workbench-w1-plan.md](2026-09-26-web-workbench-w1-plan.md)，批准候选 `1b21a18`；F16 是真机验收发现的范围内 corrective。
- 当前批准：用户 2026-09-26 明确说明任务书已由授权监督者批准，执行方式 `DIRECT_CONTINUOUS`，本消息即执行请求；允许编辑、测试和本地提交，明确禁止 push 与 live 配置/服务动作。
- Beads：`h-aii-pklr`；F16 bug 已 claim。

workspace baseline：Git root=`D:/work/inhere/hyy-ai-inspect/tools/gofer`，branch=`main`，HEAD=`7d388333789aeb647b1729532a0e5f0205ad758b`，`git status --short` 无输出。Codebase Memory 项目=`gofer`、Tier 2、generation=`2026-09-05T06:59:33Z`、HEAD 匹配；新增 workbench symbols 未入图，故精确 owner 源码已直读，后续以 coverage 检查记录缺口。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 按改动/失败/agent/seen 计算 review | `internal/workbench.Service.List/projectJobThread`、现有 prefs | 复用现有 projection、terminal 时间和 job 行的 commits/diff 字段 | 扩展 workbench 唯一投影 owner | OWNER_EXTENSION | 当前把所有未看终态都归 review | 不新增状态机；jobstore/job 仍是事实真源 |
| CAP-02 | caller 首访与 seen-all 基线 | `workbench_thread_prefs`、新表候选 | 复用现有 per-caller prefs 表与写锁 | 使用不可解析为 thread id 的保留行，只保存 baseline | DIRECT_REUSE | per-thread seen 无法覆盖首次访问前的历史 thread | 保留行不出现在 prefs projection；无需新 schema lifecycle |
| CAP-03 | seen-all HTTP 与 UI | workbench handler/client/attention 组件 | 复用现有 auth、request、poll refresh 与下拉组件 | 增加一个 user-only route 和一个顶部按钮 | OWNER_EXTENSION | 当前只能逐 thread PATCH seen | 不新增前端 store、依赖或兼容 route |
| CAP-04 | 行为回归验证 | 现有 `internal/httpapi/workbench_test.go` seam | 复用 `httptest`、`t.TempDir()`、seed helpers | 在公开 HTTP seam 增加四个固定名测试 | DIRECT_REUSE | 现有 W1 tests 固定的是旧“终态未看”口径 | 不创建新测试框架或 production stub |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 新 caller-state 表 | CAP-02 | 删除后，既有 prefs 复合主键可用保留行原子保存 caller baseline；新表会扩大 schema owner 而无额外验收收益 |
| 前端全局状态库 | CAP-03 | 删除后，attention 组件 emit + Workbench 既有 loadThreads 已满足即时刷新 |

## 前置检查与 fail-closed 条件

1. 每个 mutation stage 前以 plan task 的 IDEV-STD session fingerprint 验证；变化即停止对账。
2. 每次提交前确认 gofer worktree 只有当前 owner 文件，精确暂存并核对 cached 文件清单。
3. 测试先写且单独提交；测试必须编译、不得 `t.Skip`，RED 只能来自旧行为或缺失 endpoint。
4. baseline 保留行不得被 thread projection、PATCH thread 或响应泄漏；首次 GET 的创建与读取必须按 caller 隔离。
5. 若事实要求新增状态、改变 job/commits/diff wire 格式、引入第三方依赖或触碰 live lifecycle，则作为 Semantic Amendment 停止。
6. 仅对改过的 Go 文件要求 gofmt clean；不修已知 8 个历史 gofmt 文件和 `internal/worker.TestPolicyCacheRoundTrip` 基线。

## 波次与依赖

| 波次 | 内容 | 依赖 | 预期提交 |
|---|---|---|---|
| F16-W0 | 计划落盘、candidate、SUPMODE preflight | 当前批准 | `docs(web-11): add F16 corrective plan` |
| F16-W1 | 四个固定名测试 + 冲突 W1 断言，先 RED | F16-W0 | `test(web-11): specify F16 attention semantics` |
| F16-W2 | baseline、review/agent 投影、seen-all HTTP | RED 证据 | `fix(web-11): narrow workbench review attention` |
| F16-W3 | Web seen-all 按钮与刷新 | 后端 GREEN | `fix(web-11): add seen-all workbench action` |
| F16-W4 | 设计/W1/F16 进度、全量验证与收口 | 前三波 GREEN | `docs(web-11): record F16 validation` |

## 任务

### F16-01 固定 HTTP 行为测试

- 文件: `internal/httpapi/workbench_test.go`；必要的旧口径调整 `internal/workbench/service_test.go`。
- 动作: 新增 `TestThreadsReviewRequiresChangesOrFailure`、`TestThreadsSeenBaselineOnFirstVisit`、`TestThreadsSeenAll`、`TestThreadAgentUsesOriginAgent`；更新只与新定义冲突的 W1 断言。
- 验证: focused `go test ./internal/httpapi -run 'TestThreads(ReviewRequiresChangesOrFailure|SeenBaselineOnFirstVisit|SeenAll)|TestThreadAgentUsesOriginAgent' -count=1` 必须先 FAIL 且测试编译；扫描新增 `t.Skip` 无命中。
- 完成标准: 六类 review 分支、首次访问前后、seen-all、首轮 origin agent 都经 HTTP 响应观察；测试提交独立。
- 依赖: F16-W0。

### F16-02 实现后端 corrective

- 文件: `internal/jobstore/workbench.go`、`internal/workbench/service.go`、`internal/httpapi/workbench_handler.go`、`internal/httpapi/server.go`、`internal/httpapi/jobcredential.go`。
- 动作: 在 prefs 表中以保留行原子 get-or-create/update caller baseline；List 在 snapshot 前取得 baseline；只按最新 job、有效首轮 agent 和 per-thread/baseline 最大已看时间投影；新增 user-only seen-all endpoint 与 SEC-01 route action。
- 验证: 四个固定测试从 RED 到 GREEN；`go test ./internal/workbench/ ./internal/jobstore/ ./internal/httpapi/ -run 'Workbench|Threads|ThreadAgent|SeenAll' -count=1`。
- 完成标准: 首访历史 thread 不进 attention；新失败/有改动成功进入；exec 成功/cancelled/无改动成功不进入；needs_review 不受 seen 影响；无 N+1。
- 依赖: F16-01。

### F16-03 实现前端 seen-all

- 文件: `web/src/api/workbench.ts`、`web/src/components/workbench/WorkbenchAttention.vue`、`web/src/views/Workbench.vue`。
- 动作: 增加 seen-all API；attention 下拉顶部加入“全部标记已看”，发起时禁用并由父 view 统一处理刷新/错误；不在组件复制 threads 状态。
- 验证: `cd web && pnpm typecheck`。
- 完成标准: 点击一次调用新 endpoint，成功后刷新列表；失败可见且按钮恢复；现有单项选择不变。
- 依赖: F16-02。

### F16-04 文档与质量门

- 文件: `docs/design/2026-09-26-web-workbench-design.md`、`docs/plans/2026-09-26-web-workbench-w1-plan.md`、本计划。
- 动作: 设计 W1 实测记录追加 F16；W1 状态表增加 F16；记录测试、提交和 lifecycle 边界。
- 验证: 改过文件 `gofmt -l`；`go build ./...`；`GOOS=linux go build ./...`；`go vet ./...`；`go test ./internal/httpapi/ ./internal/jobstore/ -count=1`；`cd web && pnpm typecheck`；控制字符扫描；document validator；`git diff --check`。
- 完成标准: 所有命令等待 exit code；报告原始 PASS/FAIL 行、gofmt/控制字符输出、git log/status、G032 清单；issue 与进度一致；不 push。
- 依赖: F16-03。

## 回滚与恢复

- 以测试、后端、前端、文档四个原子提交为边界，需撤销时使用有针对性的 `git revert` 方案；不使用 destructive reset/checkout/clean。
- 后端回滚时先回滚 Web consumer，再回滚 endpoint/projection；prefs 保留行无新 schema，旧代码会把它当未命中的普通 pref，不影响 thread 数据。
- 恢复点记录 plan candidate、当前 HEAD、dirty owner、最后通过的 focused test 和下一条命令；不触碰 live 服务。

## 人工 Gate

1. 设计 Gate：Approved 0.2 已满足；F16 纠正实现回到其 §二原定义，不发明新产品决策。
2. Plan/Execution Gate：当前用户消息已同时确认任务书经授权监督者批准、选择 `DIRECT_CONTINUOUS` 并请求立即实施；按 SR1412 不重复索取。
3. 外部动作 Gate：push、release、deploy、live restart/reload、真实 config/data/worker、外部消息与硬件动作均未授权且不执行。
4. Semantic Amendment/Ownership Conflict：出现协议扩张、未知 dirty owner 或需新增 owner 时停止并请求决断。

## 可追溯性

| 任务书验收 | 任务 | 验证 |
|---|---|---|
| review 只含 needs_review、未看失败、未看有改动的非 exec 成功 | F16-01,F16-02 | `TestThreadsReviewRequiresChangesOrFailure` |
| 首访基线、之后结束仍按规则 | F16-01,F16-02 | `TestThreadsSeenBaselineOnFirstVisit` |
| seen-all 推进 caller baseline | F16-01,F16-02,F16-03 | `TestThreadsSeenAll` + Web typecheck |
| 首轮/OriginAgent 显示 | F16-01,F16-02 | `TestThreadAgentUsesOriginAgent` |
| 文档与 W1 状态 | F16-04 | validator、diff-check、Git evidence |

## 完成 Gate 与剩余工作

只有 F16-01 至 F16-04 均完成、四个固定测试和任务书全验证矩阵有最终结果、所有 owned 变更已按功能点本地提交、Beads 状态同步、`git status --short` 无未归属变更且确认未 push 时，F16 才可标记完成。W1 既有真实浏览器和 `internal/job` 基线 blocker 不属于 F16，不以本修复冒充关闭。

G032 预期清单：新增兼容分支 `NONE`；新增 DEPRECATED 标记 `NONE`；删除旧路径 `NONE`；特殊 prefs baseline 行是当前状态真源，不是兼容 fallback。

## 实施进度（不改变 Approved 0.1 计划语义）

| 任务 | 状态 | 提交/证据 |
|---|---|---|
| F16-W0 | IN_PROGRESS | 计划落盘、validator/candidate/preflight 待完成 |
| F16-01 | PENDING | 待测试先行 |
| F16-02 | PENDING | 依赖 F16-01 RED |
| F16-03 | PENDING | 依赖后端 GREEN |
| F16-04 | PENDING | 依赖实现完成 |
