<!-- template_id: plan; template_version: 1.2.0 -->
# web 工作台 W3b 会话改动视图与行内评审实施计划

> 状态：Draft 0.1 / 待人工计划批准

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-27 | Codex | 初稿：把 Approved identity 0.4 的 W3 剩余范围落为会话级 live/captured diff、改动子视图、行内评论与复用 turn 的同会话评审回灌 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 规划声明

- `thinking_mode=RIGOROUS`
- `core_objective=在不改变既有 thread 聚合、turn/resume、needs_review 验收状态机与 W3a ACP 流合同的前提下，让一个会话从首轮 Git 基线看到当前完整改动，并把带 diff 上下文的行内意见作为下一轮回灌同一 session`
- `allowed_scope=Approved identity 0.4 的“W3 细化”中“改动视图”“行内评审 → 下一轮”两节、W3a locations 跳转、本任务固定测试与验证矩阵、README 工作台说明和最后的 W3b 实测记录`
- `non_goals=W3a prompt/message/stream 语义重做、thread/layout/status/seen 状态模型重构、needs_review 自动 reject 或隐式 accept、W4 worktree 合并/归档/看板/预览、新数据库表、新依赖、push、部署、live gofer restart/reload、真实配置或远端 worker 操作`
- `expansion_policy=DEFER_OR_REQUEST`
- `delivery_track=Full`：新增两个 authenticated HTTP Interface，跨 `workbench`、`httpapi`、Git 只读采集与 Vue diff 交互多个 owner，且业务代码文件数和行数必然超过 SR1204 阈值；设计已经批准，本计划提供实施前 scope confirmation。候选不改数据库、Secret、部署、真实流量或不可逆状态，source 变更均可按原子提交回滚，治理暴露度为低，一轮合并 Standards+Spec 评审封顶。
- `review_budget=一轮 discovery、一轮 confirmation；只有 CORE_BLOCKING、Semantic Amendment 或 Ownership Conflict 才修订候选并重新进入评审`
- `stop_condition=计划 validator PASS、精确本地提交并生成 candidate identity 后立即停止；监督者明确批准本 revision 且用户续接本会话形成新的当前执行请求之前，不进入 W3B-T00`

## 目标与完成定义

目标是给工作台会话增加「过程｜改动」双子视图：改动视图在 server 本机能从首轮基线实时计算整条会话的 Git 改动，在远端或现场目录不可用时诚实回退到最新一轮采集结果；用户能在具体 old/new 行写评论，确认后把摘要、位置、上下文和正文通过既有 turn 路径续到同一 session。

完成不是“route 返回 200”“diff 能显示”或“前端 build 通过”。只有下列结果全部成立，才算 W3b source 实施完成：

1. `TestThreadDiffSpansChain` 先 RED 后 GREEN：临时 Git 仓三轮记录以首轮 `base_sha` 为基线；live 结果覆盖中间提交、当前 tracked 改动和仅列名的 untracked 文件；patch 超过 2 MiB 时 `truncated=true`；远端 runner 或 cwd 不存在时返回最新一轮 `changes.diff` 与 commits，`source=captured` 且明确“只含最新一轮”；首轮没有 `base_sha`/`worktree_base_sha` 时 409 并说明无法建立基线。
2. `GET /v1/workbench/threads/{id}/diff` 返回稳定 JSON：`source`、`base`、`head`、`files[{path,status,additions,deletions,binary}]`、`patch`、`truncated`，captured 时另带最新一轮 `commits` 与说明；live Git 子进程共享 10 秒 deadline，单项输出不超过 2 MiB，使用参数数组和 `GIT_OPTIONAL_LOCKS=0`，不在 HTTP handler 拼命令。
3. `TestReviewCommentsBecomeNextTurn` 先 RED 后 GREEN：summary 位于 prompt 开头；每条意见包含 `path:line`、`side`、当前 patch 中命中行上下各 2 行和正文，找不到行时只省略代码块；派发严格调用 `Service.Turn`，返回新 job id、session 不变并标记该 caller 已看；空请求、一次性 job、超过 50 条、非法位置/side、空评论或单条超过 4000 Unicode 字符均得到对应 400/409。
4. 评审回灌继承 turn 的全部既有 Gate：ACP 与 CLI 都沿原 `ResumeJob` 路径，relay/一次性 job 不发明旁路；`needs_review` 未 accept/reject 前仍由 `ResumeJob` 拒绝为 409，不在 W3b 暗改验收状态机。改动视图中的「接受」对 `needs_review` 调现有 accept，其他状态只 PATCH seen。
5. `TestReviewJobCallerForbidden` 先 RED 后 GREEN：job credential 能 GET thread diff，但 POST review 被 SEC-01 default-deny 为 403；普通 user caller 能按既有 workbench 规则使用两个接口。
6. Web 纯函数测试先 RED 后 GREEN：草稿状态以 thread id 隔离，新增、编辑、删除不串会话；生成请求体去除浏览器内部 id，只保留 summary 与 `{path,line,side,text}`；函数不依赖 Vue、DOM、network 或 localStorage。
7. Thread 主区有「过程｜改动」切换；改动视图左侧显示文件状态与 `+/-`，右侧复用并增强 `UnifiedDiff`。working 时 10 秒刷新；最新 ACP 轮收到 `edit|delete|move` tool 事件立即合并刷新；W3a tool location 点击切换改动并定位文件，当前 patch 无对应文件块时显示精确提示“该文件无改动”。
8. old/new 行号来自 hunk header 与行推进规则；点行号旁「＋」出现内联草稿，可编辑、删除；草稿以版本化 key 按会话写 localStorage，所有 get/set/remove 均 `try/catch`，存储不可用不影响页面或 API。
9. 「发送评审（N）」在 summary 或评论非空且 thread 可续接时可用；确认后 POST review，成功即清除此 thread 草稿、切回「过程」并打开返回的新 job。失败保留草稿。窄屏仍能切两个子视图、选择文件、编辑评论、发送评审和接受。
10. 改过的 Go 文件 `gofmt -l` 无输出；Windows/Linux build、vet、指定三包测试、Web test/typecheck/build、文档 validator、diff/编码/控制字符扫描均有 exit code 与原始 PASS/FAIL 行。测试、后端 diff、后端 review、安全/API、Web 草稿、Web UI、文档各按功能点独立本地提交；最终不 push、不部署、不重启/reload live gofer、不触碰真实配置。

## 范围、排除项与授权

范围包括：

- `internal/workbench` 解析 thread job 链、选择首轮基线和最新执行现场，拥有 live/captured source 决策、diff 文件模型、patch 上下文解析、review prompt 合成、seen 记录与对既有 `Turn` 的调用。
- live source 仅在最新轮 runner 为 G043 内置本机 runner（持久化 canonical `local`，正式 alias `server` 同样识别）且最新 `cwd` 仍是目录时使用。records 按 `started_at,id` 升序；基线优先首轮 `worktree_base_sha`，否则 `base_sha`；现场 cwd 取最新轮。`git diff <base> --` 同时覆盖已提交与 tracked 未提交改动，`git ls-files --others --exclude-standard -z` 只为 untracked 生成文件清单，不读取其内容。
- captured source 用最新轮 `<result_dir>/changes.diff`（按 2 MiB UTF-8 安全限读）与 `commits_json`；`head` 优先最新轮 `worktree_head_sha`，其次最新 commit SHA，没有则留空。响应的 `base` 仍是首轮会话基线，并显示“只含最新一轮采集结果”，不伪装成链级 live diff。
- live 文件元数据由 `--name-status -z`、`--numstat -z` 与 untracked 名单合并；patch 与文件清单独立限读，所以 patch 截断不会丢掉已成功收集的文件名。二进制 numstat 的 `-` 映射 `binary=true`，untracked 使用明确状态且 additions/deletions 为 0。
- review comment 只从同一次 `Diff` 返回的 patch 取上下文，不按评论 path 读取工作区文件，避免路径穿越、现场漂移或把 untracked 内容带入 prompt。每条代码块最多目标行前后各 2 个 diff 行，保留 `+|-| ` 前缀；path/line/side 未命中时仍保留位置与正文，只不生成代码块。
- review 请求先完成 kind/数量/长度/位置校验和 prompt 合成，再通过既有 thread preference 路径记录 seen，最后直接调用 `Service.Turn(caller, threadID, prompt)`；若 turn 因 job 状态 Gate 失败，seen 仍成立，因为本次用户动作已经证明其看过改动，但不会产生新 job 或改变 job 验收状态。
- HTTP 层只绑定/校验 body、调用 workbench service、映射 sentinels 与写 JSON；新增正式 routes 为 `GET /v1/workbench/threads/{id}/diff` 和 `POST /v1/workbench/threads/{id}/review`。review 不加入 job write allowlist，并给 SEC-01 增加稳定 route word/action 文案。
- Web 扩展现有 `UnifiedDiff` 的可选行号、评论 props/events 与文件定位能力；job 详情未传这些可选 props 时保持现状。新增 thread changes 组件与纯草稿模块，不复制 diff parser、turn/resume、accept、seen 或 ACP stream。
- README 工作台小节改为 W1–W3b 并说明操作与 source 降级；全部质量结果出来后，Approved identity 0.4 设计追加不改变设计 revision 的“W3b 实测记录”，如实分开 source、测试、runtime/browser 和 lifecycle 状态。

明确排除：

- 不修改 ACP artifact、normalizer、SSE wire kinds、thread `job_ids` 排序、W1/F16 status precedence、W2 layout、Push、turn/resume/accept/reject 状态机或 job schema。
- 不让 review 自动 accept/reject `needs_review`，不新增“force resume”、第二套 resume dispatcher、review job 类型、评论数据库表、server 草稿同步或跨设备草稿语义。
- 不把 untracked 内容放进 patch/prompt，不读取 arbitrary comment path，不把 captured 最新轮冒充完整会话 diff，不在远端 runner 上执行 Git。
- 不增加第三方 Go/pnpm diff、Git、状态或持久化依赖；不新建 package、数据库 migration、WebSocket、SSE route、兼容 alias 或旧 payload fallback。
- 不修 `internal/worker` 的 `TestPolicyCacheRoundTrip` 0600 断言，不格式化 8 个既有 gofmt 脏文件，不顺手处理 W3a/W2/W1 观察项。
- 不 pull/rebase、push、tag、release、deploy、迁移/清理真实数据、启动/停止/restart/reload live server/worker、修改 `D:/work/inhere/config/win-env/gofer`、访问远端 worker、发 traffic/外部消息或做硬件动作。

当前请求只授权生成、验证并本地提交本计划候选；不授权实施。候选获批后仍须用户续接本会话形成新的当前执行请求。执行方式已选择 `DIRECT_CONTINUOUS`，它只决定获批后的连续推进方式，不越过本次 Plan Gate。

- `host_or_non_offline_action=REQUIRED`

该值只覆盖获批实施中的本机源码编辑、Git 临时仓测试、Windows/Linux 编译、Go/Web 测试与构建，以及必要时使用 `t.TempDir()`/随机 loopback/临时 config 的离线 smoke。它不覆盖真实 gofer 进程、真实配置、远端、push、发布或部署；若增加独立 CLI smoke，每条 gofer 命令必须显式携带 `--server http://127.0.0.1:<随机端口>` 或 `-c <临时配置>`。

## 输入与批准证据

- 唯一设计依据：[2026-09-26-web-workbench-design.md](../design/2026-09-26-web-workbench-design.md)，状态 `Approved`、identity `0.4`，当前设计与 W3a 实测记录位于基线提交 `6b2886c`；本计划只消费“W3 细化”的“改动视图”“行内评审 → 下一轮”和 W3a locations 跳转，不消费 W4。
- 当前任务书明确命名 `WEB-11 W3b`，固定 `TestThreadDiffSpansChain`、`TestReviewCommentsBecomeNextTurn`、`TestReviewJobCallerForbidden` 与 Web 草稿纯函数测试，要求测试先行单独 RED 提交，按“测试 → 后端 → 前端 → 文档”推进，3600 秒内最终等待慢包结果，并明确禁止 push/live/真实配置动作。
- 用户已授权 gofer 监督者 Claude 代为批准“由已批准设计派生的实施计划”；设计 Approved、validator PASS、candidate commit、低暴露 review PASS 或 `DIRECT_CONTINUOUS` 选择都不自动构成计划批准或当前实施授权。
- IDEV-STD：workspace `BOUND`，runtime `0.22.0`，standards revision `d2a5c03b46ab65fb50156ffadb83774cb7a82bb5`；任务 `plan+git` semantic delivery=`FULL`、resource count=`25`、payload SHA-256 已核对；两次规划 mutation 前 verify 均为 `inputs_unchanged=true`。
- 跟踪项：tools Beads `tools-yal`（`[gofer][WEB-11] W3b 会话改动视图与行内评审计划`，本轮已 claim）；从 workspace 使用 `bd -C tools ...`，gofer Git 子仓本身没有独立 Beads DB。

workspace baseline（2026-09-27 规划时）：

- Git root=`D:/work/inhere/hyy-ai-inspect/tools/gofer`；branch=`main`；HEAD=`6b2886c3a821a792f92599abfb992266be609132`（tag `v0.65.0`）；`git status --short` 无输出；`git status -sb`=`main...origin/main [ahead 95]`。origin 仅作只读基线，本 job 禁止 pull/push。
- 工具=`go version go1.25.10 windows/amd64`、Node `v24.15.0`、pnpm `12.5.1`；Web 已有 `vitest run`、`vue-tsc --noEmit`、Vite build，无需新增依赖。
- Codebase Memory project=`gofer`、Verify Tier 2、generation=`2026-09-05T06:59:33Z`、status=`ready`。图确认 `captureDiff <- captureOutcomes <- execute <- Submit`，但 generation 早于 W1–W3a；19 条证据路径均为 `not_tracked` 或 `metadata_changed`，`web/src/api/types.ts:1-989` 另有 `parse_partial`，不覆盖本计划使用的 Workbench types 行。所有物质性结论已从 current HEAD 精确回读；不以旧图作否定性或完整性结论。
- 当前 `workbench.Service.Turn` 是 agent/relay/one-shot 的唯一派发 seam：agent 取同 session 最新 job 后调用 `ResumeJob`；一次性 job 返回 `ErrNotResumable`。`ResumeJob` 明确拒绝未终态与 `needs_review`，ACP/CLI continuation、同 runner、cwd、session lineage 和 policy 继承都在 job owner 内。
- 当前 `captureDiff`/`captureWorktreeDiff` 使用 `exec.CommandContext`、参数数组、`GIT_OPTIONAL_LOCKS=0`、文件 cap 和 `changes.diff`；这些 helpers 未导出、timeout/cap 与 W3b 不同。W3b 在现有 `internal/workbench` owner 内复用同一安全模式，不为单一 caller 扩大 job package API。
- 当前 `UnifiedDiff.vue` 已解析 `diff --git`、meta、hunk、binary、两段 worktree 标题并负责 1 MiB/5000 行渲染降级，但没有 old/new 行号、评论 seam 或文件定位；`ReviewPanel.vue` 与 job detail 直接复用它。W3a `ConversationView` 已保留结构化 `locations[{path,line?}]`，ACP ToolKind 正式修改类集合为 `edit|delete|move`。
- 当前 `ThreadView` 为每个 pane 的过程 owner，ACP 用 `ConversationView`，非 ACP 用现有 stream/LogTape，底部 turn 走 workbench API；其 seen timer、accept API、context refresh/continued 与窄屏布局均可直接复用。

预期 owner 路径：

- RED tests：新建 `internal/workbench/diff_test.go`、`internal/workbench/review_test.go`、`internal/httpapi/workbench_review_test.go`、`web/src/components/workbench/reviewDrafts.test.ts`；只使用 `t.TempDir()`/临时 Git repo/httptest，不建共享 fixture 目录。
- 后端 domain：修改 `internal/workbench/model.go`，新建 `internal/workbench/diff.go` 与 `internal/workbench/review.go`；`service.go` 原则上只读，若共享 thread records/pref helper 的抽取能避免重复且行为不变，可按 Corrective 精确修改。
- HTTP/SEC-01：新建 `internal/httpapi/workbench_review_handler.go`，修改 `internal/httpapi/server.go` 与 `internal/httpapi/jobcredential.go`；旧 `diff_handler.go` 是 job 级接口，只读保留，不改其语义。
- Web domain/API：新建 `web/src/components/workbench/reviewDrafts.ts`、新建 `ThreadChangesView.vue`，修改 `web/src/api/types.ts`、`web/src/api/workbench.ts`。
- Web integration：修改 `web/src/components/UnifiedDiff.vue`、`web/src/components/workbench/ConversationView.vue` 与 `ThreadView.vue`；`ReviewPanel.vue` 只读回归，`Workbench.vue`、`acpEvents.ts`、layout tree 原则上不改。
- 文档：`README.md`、`docs/design/2026-09-26-web-workbench-design.md`；本计划只保留 candidate，不作为 implementation progress checklist 改写。

这些 expected paths 是 planning evidence，不是封闭白名单。实施发现新 path/symbol 时，必须在下一次 mutation 前按 Operational Discovery / Corrective / Semantic Amendment / Ownership Conflict 分类；不得通过扩大 staging、hunk staging、格式化无关文件或重算 baseline 绕过 owner 冲突。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 从首轮基线得到 live/captured 会话 diff、文件统计与截断标记 | `workbench.Store.ListJobs/GetJob`、`JobRecord.BaseSHA/WorktreeBaseSHA/Cwd/Runner/ResultDir/CommitsJSON`、`job.captureDiff/runGit`、`captureWorktreeDiff/joinDiffSections`、job `GET /diff` | 直接复用持久化字段、thread id 解析、G043 runner helper、Git 安全参数模式与 captured artifact | 在既有 `internal/workbench` 增 chain selection、10s/2MiB read-only Git helper和响应 parser；不导出 job 私有 helper、不建新 package | OWNER_EXTENSION | job `GET /diff` 只有单轮摘要/文件，现有私有 helper 是终态采集且 cap/timeout/输出形态不同，无法表达首轮基线、untracked names 与 live/captured | 权威仍是 Git 现场或最新 artifact；helper 无 daemon/cache，request 结束释放；删除 W3b 时与 routes/tests 一并移除 |
| CAP-02 | 从当前 patch 为 old/new 行生成安全的 ±2 行 review 上下文 | `UnifiedDiff.parseDiff` 行推进规则、`changes.diff`、任意 path 文件读取候选 | 直接复用统一 diff 语法与同次 Diff 结果 | 在 `internal/workbench/review.go` 做最小 hunk/line parser，只读 patch，不碰 filesystem path | OWNER_EXTENSION | 现有 Go 端没有统一 diff 行号 parser；读工作区文件会漂移且扩大路径安全面 | parser 只服务 response/prompt，同一 patch 是唯一输入；不建第二 artifact 或索引 |
| CAP-03 | review 必须走与 turn 相同的同 session 派发并标记 seen | `Service.Turn`、`Patch(Seen)`、`Jobs.ResumeJob`、relay/one-shot sentinels | `Turn` 与 `Patch` 直接复用，ACP/CLI/runner/session/status Gate 不复制 | 新 `Service.Review` 只做校验、prompt composition、seen 与调用 `Turn` | OWNER_EXTENSION | 普通 turn 不接受结构化 comments，也不会生成 diff 上下文 | 不新增 dispatcher/job type；Turn 继续是唯一 continuation owner，回滚 review 不影响普通 turn |
| CAP-04 | authenticated routes 与 job caller diff-read/review-deny | `workbench_handler.go`、`workbenchHTTPStatus`、`server.go` routes、SEC-01 `jobCallerMayRead`/default-deny/route words | GET 自动复用 job caller read；POST 复用 user caller guard 与 default-deny | 新 handler 只绑定/映射；SEC-01 只加正式 route word/action，不加 allowlist | THIN_ADAPTER | 现有 HTTP surface 没有 thread diff/review route | 删除前端 consumer 后可直接删两 route/handler；无 alias、DB 或 wire generation |
| CAP-05 | 复用 UnifiedDiff 的文件/hunk 渲染并增加行号、评论与定位 | `UnifiedDiff.vue`、`ReviewPanel.vue`、job detail diff、后端 files list | 继续使用同一 parser/render/cap/download；无评论 props 时保持 job detail 行为 | 给同组件加 optional comments/events/focus seam与 old/new counters；ThreadChangesView 负责 thread API/列表 | OWNER_EXTENSION | 当前组件没有行号和可定位 DOM seam，复制一个 review diff 会产生两套 parser | optional API 有唯一通用 owner；ReviewPanel 回归守旧 consumer，W3b 删除时可删 optional seam |
| CAP-06 | 按 thread 隔离、纯函数可测的 review drafts 与请求体 | `ThreadView` local refs、`layoutTree.ts`/`acpEvents.ts` 纯函数范式、Plan/Theme localStorage guard | 复用 TypeScript immutable object/array 与版本化 localStorage 模式 | 新建无 IO `reviewDrafts.ts`；组件单独用 try/catch load/save | MINIMAL_NEW_MODULE | 把 keyed mutation 和 payload shaping 写进 Vue 无法满足固定纯函数测试，也容易让分屏会话串草稿 | 唯一 consumer 为 ThreadChangesView；无服务/订阅；删除功能时与 test/storage key 一并删除 |
| CAP-07 | 过程/改动切换、10s poll、tool edit refresh、location jump、send/accept | `ThreadView`、`ConversationView`、W3a tool events、`createPoller`、`acceptJob`、`patchWorkbenchThread` | ACP stream、accept/seen、context refresh/continued、mobile pane 全部复用 | 新建 `ThreadChangesView.vue` 薄视图；ThreadView 只保持两个 subtree 与路由事件，ConversationView 只 emit | MINIMAL_NEW_MODULE | ReviewPanel 是 job 级且无 thread/live/comment；继续塞进 459 行 ThreadView 会混合 transport、draft、diff 与过程 owner | 新组件不拥有 session/accept/ACP 真源；timer/unmount 可释放；删 W3b 时回到原过程 subtree |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| MOD-01 | CAP-06 | `web/src/components/workbench/reviewDrafts.ts` | ThreadView inline refs、api/workbench payload builder、layout/acp pure helpers | 现有 refs 不按 thread 隔离，API client 不应拥有浏览器 draft id/state | 放 Vue component 内不能独立 RED/GREEN 测增删改与 payload shaping | 任务书明确要求按会话增删改和生成请求体的纯函数 Vitest | workbench review draft 唯一 owner；无 IO/定时器；storage 生命周期由 view 管理 | W3b 回滚时与 test/key/view 一并删；只有出现第二个真实 consumer 才上浮 |
| MOD-02 | CAP-07 | `web/src/components/workbench/ThreadChangesView.vue` | ReviewPanel、ThreadView、UnifiedDiff、JobDetail | ReviewPanel 绑定单 job `/jobs/{id}/diff` 与 accept/reject，不能承载 thread source、poll 或 draft | 把全部状态放 ThreadView 会混合现有过程 stream/terminal/relay owner；薄组件可只组合既有 API/UnifiedDiff | 文件列表、captured notice、poll、inline review、accept 与窄屏构成独立内聚视图 | ThreadView 唯一挂载 owner；unmount 清 timer/inflight request，localStorage 按 thread | W3b 回滚时先删入口与 events，再删 view；若未来 job detail复用，先以第二用例证明再合并 |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 第三方 Git/diff/parser 依赖或新 Go package | CAP-01 | stdlib `os/exec`、现有 Git pattern 与受限 unified parser 足够；新增依赖/package只扩大供应链和分层，Go/pnpm dependency diff 应保持 0 |
| 新 review dispatcher、job type、queue 或 reject+resume 隐式流程 | CAP-03 | `Service.Turn` 已是批准的唯一同 session 派发；任何旁路会漂移 runner/session/policy，删除测试是固定 test 断言 recording Jobs 只收到 Turn 路径调用 |
| server draft 表、跨设备同步或全局 Vue store | CAP-06 | 任务明确 localStorage 按会话；持久表/Schema/全局 store 不增加当前验收价值，且改变数据/lifecycle Gate |
| 第二套 diff 组件或读取评论 path 的文件 API | CAP-05 | 现有 UnifiedDiff 可薄扩展；任意文件读取会扩大安全面并让上下文脱离当前 patch，故必须删除该候选 |
| WebSocket、新 SSE 或额外 Git watch daemon | CAP-07 | 已有 W3a live ACP tool stream + 10s poll 足够；新长驻机制没有 proven gap，删除测试是没有新 route/process/dependency |

## 前置检查与 fail-closed 条件

1. 监督者先对本候选做低暴露的一轮合并 Standards+Spec review，并明确批准 revision 0.1；用户再续接本会话形成当前执行请求。二者缺一即停。
2. 执行开始使用 `inhere-run-supmode` 重新加载 `implement+test+git` 语义资源，核对 approved candidate tuple、`DIRECT_CONTINUOUS`、session fingerprint、branch/HEAD/status 与 Beads；每个 mutation stage 前过 fingerprint Gate。
3. `git status --short` 必须把 owner 内 dirty/untracked 判为本 job 可拥有；任何用户/其他 Agent 修改命中 expected path，分类为 Ownership Conflict 并停止。无关 dirty 只保留，不暂存、不格式化。
4. T00 只跑与 W3b 相关的最小 baseline，不跑全仓 Go/Web 完整基线：focused 复核既有 workbench turn、Git diff helper、W3a reducer与当前 type surface；全量指定三包、build/vet/Web 门只在实现收口执行一次。
5. 禁止读取/修改 `D:/work/inhere/config/win-env/gofer`；禁止启动、停止、restart/reload live gofer server/worker。测试一律 `t.TempDir()`；任何独立 smoke 只用临时 config、临时 storage 与随机 loopback，并逐条显式指定 `--server` 或 `-c`。
6. T10 必须形成真实 RED，失败只指向 W3b 缺失 symbol/route/behavior；fixture、Git 可执行文件或既有环境失败先修测试，不把假 RED、`t.Skip`、空断言或复制实现的 test helper 提交为 contract。
7. `Service.Turn`、`ResumeJob`、needs_review accept/reject、thread id/status/seen、job `/diff`、W3a ACP stream 与 UnifiedDiff 默认 consumer 是冻结面。发现必须改变其外部语义、Schema、安全 Gate 或 runner lineage，分类为 Semantic Amendment 并停止。
8. live/captured 是 Approved W3b 产品分支，不是 G032 legacy fallback；`server/local` 是 G043 当前正式 alias。除此之外不增加缺字段、旧 route、旧 server/worker、猜 agent 名、双 parser 或无标记兼容分支。
9. `internal/workbench` 不 import `httpapi`；HTTP 不执行 Git、不解析 patch、不拼 prompt；Web API client 不拥有 localStorage/draft；以 build/vet/必要时 `go list -deps` 验 G021/G022。
10. 文本修改只用 `apply_patch`，保持 LF、UTF-8 无 BOM；不使用 `gofmt -w` 或脚本覆写 source，`gofmt -l/-d` 发现差异后仍用 `apply_patch` 修正。
11. 任何 required test/build/vet/typecheck 失败都保留 exit code 和原始 PASS/FAIL 行；只修 owned regression。慢 `internal/job`/`internal/httpapi` 可尽早放独立后台 exec session，最终汇报前必须等待完成，不以“仍在跑”收尾。

## 波次与依赖

| 波次 | 任务 | 依赖 | 产出/恢复点 |
|---|---|---|---|
| W0 | W3B-T00 | approved candidate + 当前执行请求 | 最小只读 preflight 与执行基线 |
| W1 | W3B-T10 | T00 | Go/Web 固定 contract 的唯一 RED commit |
| W2 | W3B-T20 → W3B-T21 | T10 | thread diff GREEN commit；review/HTTP/SEC-01 GREEN commit |
| W3 | W3B-T30 → W3B-T31 | T21 | draft pure functions GREEN commit；改动/评审 UI commit |
| W4 | W3B-T40 → W3B-T41 → W3B-T42 | T31 | 全质量门；最后 README/实测记录；Git/Beads/lifecycle 交付 |

单 Agent 按依赖顺序执行，不需要 transfer registry。若监督者改为多 Agent 并行，mutation 前必须冻结重复 owner（尤其 `model.go`、`server.go`、`types.ts`、`UnifiedDiff.vue`、`ThreadView.vue`）的 transfer chain。

## 任务

### W3B-T00 复核候选、执行授权与最小 baseline

- 文件: 只读本计划、Approved 0.4 design、`AGENTS.md`、Git/Beads/IDEV-STD 状态和 expected owner；不改 source。
- 动作: 核对 candidate 四元组、监督者批准原话、用户续接执行请求、`DIRECT_CONTINUOUS`、branch/HEAD/status、真实配置/live 禁区；确认 current source 仍由 `Turn`/`ResumeJob`、`UnifiedDiff`、W3a locations 等 owner 掌权。
- 验证: `git branch --show-current`、`git rev-parse HEAD`、`git status --short`、`git log -5 --oneline`、`bd -C tools show tools-yal`；focused baseline 仅跑 `go test ./internal/workbench/ -run 'TestTurnDispatchesByThreadKind|TestThreadPreferenceIsolation' -count=1`、`go test ./internal/job/ -run 'TestCaptureDiffGitRepo|TestWorktreeDiffIncludesCommits|TestCommitsCapturedFromBaseSHA' -count=1`、`cd web && pnpm test -- acpEvents.test.ts`。
- 完成标准: plan approved + execution current，fingerprint/preflight PASS，owner 无冲突，focused baseline 结果已记录；否则停止，不为 baseline 跑全仓。
- 依赖: Plan Gate + Execution Gate。

### W3B-T10 先提交固定名 RED contract tests

- 文件: 新建 `internal/workbench/diff_test.go`、`review_test.go`、`internal/httpapi/workbench_review_test.go`、`web/src/components/workbench/reviewDrafts.test.ts`；只复用同 package 现有 test helpers。
- 动作: `TestThreadDiffSpansChain` 在 `t.TempDir()` 初始化 Git，首轮记录 base，第二轮真实 commit，第三轮留 tracked 与 untracked；断言 live source/base/head/files/patch、untracked 仅列名、2 MiB 截断；子测试把最新 runner 改远端或删 cwd 并写最新 artifact/commits，断言 captured；首轮 base 为空断言 409 sentinel。
- 动作: `TestReviewCommentsBecomeNextTurn` 使用 recording Jobs/store 创建可续 agent thread；两条 old/new 评论 + summary 断言 summary 为首段、位置、±2 diff 上下文、正文、同 session resume source、返回 job id 与 seen；再覆盖上下文未命中不出代码块、summary-only、空请求 400、one-shot 409、51 条 400、4001-rune 400 与非法 path/line/side/text 400。
- 动作: `TestReviewJobCallerForbidden` 从公开 HTTP seam 用 user token与 job credential：job GET diff=200、POST review=403，普通 user POST 可到 workbench service；不得通过测试专用 route 绕 middleware。
- 动作: `reviewDrafts.test.ts` 固定 thread A/B 隔离、add/edit/delete immutability、summary 与 request body shaping、内部 id 不出请求；只 import 尚不存在的 pure module，不复制实现自绿。
- 验证: `go test ./internal/workbench/ -run '^Test(ThreadDiffSpansChain|ReviewCommentsBecomeNextTurn)$' -count=1`、`go test ./internal/httpapi/ -run '^TestReviewJobCallerForbidden$' -count=1`、`cd web && pnpm test -- reviewDrafts.test.ts` 必须因缺失 W3b behavior FAIL；`rg -n 't\.Skip' <owned-test-files>` 原生 exit 1 且无输出。
- 完成标准: 四组 contracts 被测试运行器发现，RED 只指向缺失行为；提交前 `git status --short`、精确 add 与 `git diff --cached --name-only` 仅含测试 owner，创建单独 `test(web-11): pin W3b diff review contracts` commit。
- 依赖: T00。

### W3B-T20 实现会话 live/captured diff domain 与 GET route

- 文件: 修改 `internal/workbench/model.go`，新建 `internal/workbench/diff.go`、`internal/httpapi/workbench_review_handler.go`，修改 `internal/httpapi/server.go`；消费 T10 diff tests。
- 动作: 定义 response/file/commit shape 与 missing-base、Git timeout/unavailable sentinels。agent thread 用 session 查询，one-shot 用 job id，relay 明确 conflict；统一按 `started_at,id` 排序并取首轮基线/最新现场。
- 动作: live helper 使用一个 10 秒 context，参数数组执行 `rev-parse HEAD`、`diff` patch、`--name-status -z`、`--numstat -z`、`ls-files --others --exclude-standard -z`，每次设置 `GIT_OPTIONAL_LOCKS=0`。patch 读 cap+1 判 2 MiB truncation并在 UTF-8 边界截断；metadata 超限或 Git 非零/timeout 不静默伪造完整结果。
- 动作: captured 只限读最新 `changes.diff` 并解析该 patch 的 file stats，附最新 commits 与 notice；文件不存在可返回空 patch + commits，而不是 404。首轮无基线先于 source fallback 返回 conflict。
- 动作: HTTP `GET /workbench/threads/{id}/diff` 只调用 service、映射 404/409/504/500 并输出 JSON；所有 authenticated GET（含 job caller）沿 SEC-01 read 规则通过。
- 验证: diff fixed test GREEN；补覆盖 NUL path/status/numstat、binary、rename/新增/删除、UTF-8 cap、captured missing file；`go test ./internal/workbench/ -run 'TestThreadDiffSpansChain|Diff' -count=1`、`go test ./internal/httpapi/ -run 'ThreadDiff|ReviewJobCaller' -count=1`、owned Go `gofmt -l` 无输出。
- 完成标准: live/captured/base/files/patch/cap/source/error 语义从 domain 与公开 route 可观察，handler 无 Git/patch 逻辑；创建独立 `feat(web-11): expose thread diff snapshots` commit。
- 依赖: T10。

### W3B-T21 实现 review prompt、turn 复用与 SEC-01

- 文件: 修改 `internal/workbench/model.go`，新建 `internal/workbench/review.go`，完成 `internal/httpapi/workbench_review_handler.go`，修改 `server.go`、`jobcredential.go`；消费 T10 review/security tests。
- 动作: validate 采用 Unicode rune 长度；最多 50 comments，每条 path 非空、line>0、side=`old|new`、text trim 后非空且 ≤4000 runes；summary trim 后与 comments 同为空返回 `ErrEmptyReview`。summary-only 不强求 diff；有 comments 时调用同一次 `Diff` 并从 patch 建索引。
- 动作: prompt 格式固定为“非空 summary 原文”开头，之后每条独立段含 `path:line (side)`、可选 fenced `diff` ±2 行、评论正文，保持请求顺序。上下文 parser 不读取文件；找不到 path/line 时没有空 fence。
- 动作: 只允许 `KindAgent`；校验/合成后沿现有 Patch preference 路径写 `seen=true`，再调用 `s.Turn`。不直接调 `ResumeJob`、不复制 agent/relay switch；返回 TurnResult job id。Turn 的 needs_review/live/runner/session errors 原样映射现有 409/400。
- 动作: 注册 `POST /workbench/threads/{id}/review`，handler 先要求 user caller，再 bind body/call service；在 SEC-01 route words/action 加 `review` 的稳定拒绝文案，不加入 write allowlist。
- 验证: `TestReviewCommentsBecomeNextTurn`、`TestReviewJobCallerForbidden` GREEN；`go test ./internal/workbench/ -count=1`；`go test ./internal/httpapi/ -run 'Workbench|ReviewJobCallerForbidden' -count=1`；`go vet ./internal/workbench/ ./internal/httpapi/`。
- 完成标准: prompt、same-session/turn、seen、limits、one-shot/needs_review conflict 与 caller 权限均被固定测试锁定；创建独立 `feat(web-11): resume threads from inline reviews` commit。
- 依赖: T20。

### W3B-T30 实现 Web review draft 纯函数

- 文件: 新建 `web/src/components/workbench/reviewDrafts.ts`，消费已提交的 `reviewDrafts.test.ts`。
- 动作: 定义 versioned state、thread draft、comment 与 request types；纯函数 `add` 生成/接收稳定内部 id、`update` 仅改目标 thread/id、`remove` 清目标、`setSummary` 按 thread 隔离、`buildRequest` trim/剥 internal id 且保持评论顺序。所有操作返回新对象/数组。
- 动作: 提供保守 normalize/empty helper供 view 处理损坏 JSON；模块不得访问 localStorage、window、Vue 或 API。
- 验证: `cd web && pnpm test -- reviewDrafts.test.ts` GREEN，随后 `pnpm test`；`pnpm typecheck` 在 UI 接入前不因新 module 产生错误。
- 完成标准: 固定纯函数 contract 绿色且无 IO/global state；创建独立 `feat(web-11): manage per-thread review drafts` commit。
- 依赖: T21。

### W3B-T31 接入改动子视图、行内编辑、刷新、跳转与接受

- 文件: 新建 `web/src/components/workbench/ThreadChangesView.vue`；修改 `web/src/api/types.ts`、`api/workbench.ts`、`components/UnifiedDiff.vue`、`workbench/ConversationView.vue`、`ThreadView.vue`；消费 T30 module。
- 动作: 增 thread diff/review typed clients。ChangesView 负责 fetch/inflight coalescing/error/captured notice、左文件列表、10 秒 working timer、版本化 localStorage try/catch、summary/comments、确认发送、成功清草稿、accept-or-seen 与窄屏布局；unmount 清 timer/abort。
- 动作: UnifiedDiff 从 hunk header维护 old/new counters，在可评论内容行显示两侧行号与「＋」；optional comments 原位渲染 textarea/保存/删除并 emit immutable changes；为文件块建立稳定 path anchor与 `focusFile(path): boolean`。未传评论 props时 ReviewPanel/job detail DOM与行为保持。
- 动作: ThreadView 增「过程｜改动」，用 `v-show`/稳定 subtree 保持 W3a SSE/PTY/log 连接；review success 调 `context.continued(job_id)`并切过程，accept/seen 后 refresh。过程 footer只在过程视图显示，草稿发送只在可续终态启用。
- 动作: ConversationView 把 location 文本变为 button并 emit path；仅最新 running round 的 `edit|delete|move` tool update emit refresh signal。ThreadView 切改动、请求刷新后定位；response files/UnifiedDiff 无可定位 block时显示“该文件无改动”，不猜文件内容。
- 验证: `cd web && pnpm test`、`pnpm typecheck`、`pnpm build`；手工 source review确认 localStorage 全部 try/catch、timer/AbortController cleanup、`UnifiedDiff` old/new line推进（context/+/-/no-newline）与 ReviewPanel无新必填 props。若做浏览器 smoke，只能连临时 server/config/random port，不能使用 live。
- 完成标准: 桌面与 `<768px` 均可切换、定位、评论、编辑/删除、发送、接受；working poll与 tool refresh 不泄漏/风暴；创建独立 `feat(web-11): add thread changes review view` commit。
- 依赖: T30。

### W3B-T40 运行最终质量门并等待慢包

- 文件: 不新增 source；只读验证所有 owned commits，日志放仓库 `tmp/w3b-validation/`（不提交）。
- 动作: 尽早分别启动慢 `internal/httpapi`、`internal/job` package test exec sessions；同时顺序跑 workbench、build/vet 和 Web gates。任何 session 未结束不得进入 T41 或写最终报告。
- 验证: changed Go files `gofmt -l`（exit 0、无输出）；`go build ./...`；Linux/amd64 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...`；`go vet ./...`；`go test ./internal/workbench/ ./internal/httpapi/ ./internal/job/ -count=1`；`cd web && pnpm test && pnpm typecheck && pnpm build`。每条同时记录 exit code 和原始 PASS/FAIL/module summary。
- 验证: `git diff --check`；对 owned text 运行控制字符 `rg --pcre2 '[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]'`（期望 exit 1/无输出）、UTF-8 BOM 与 CRLF 扫描（期望计数 0）；`git diff -- go.mod go.sum web/package.json web/pnpm-lock.yaml` 无输出。
- 完成标准: required gates 全部完成且结果已归档；失败则只做 owned corrective + focused rerun，随后重跑受影响的 required gate，不以 timeout/后台中结束。
- 依赖: T31。

### W3B-T41 最后更新 README 与 Approved design 实测记录

- 文件: `README.md`、`docs/design/2026-09-26-web-workbench-design.md`。
- 动作: README 标题升 W1–W3b，替换“尚未做”为操作说明：source live/captured、文件列表/UnifiedDiff、评论草稿、发送/接受、location 跳转与 10 秒/tool 刷新；HTTP 表补 diff/review endpoints。
- 动作: 只有 T40 全部有结果后才在设计追加“W3b 实测记录”；列实施 commit、固定 tests、build/vet/Web/hygiene 原始事实，明确 browser/live/push/deploy 的 RUN/NOT_RUN，不因 identity/provenance 或实测记录提升 Approved 0.4 revision。
- 验证: 从 selected runtime 运行 `validate_document.py --kind design docs/design/2026-09-26-web-workbench-design.md` 与 `--kind plan docs/plans/2026-09-27-web-workbench-w3b-plan.md`；README links/route strings精确搜索；docs/text控制字符、BOM、CRLF与 `git diff --check`。
- 完成标准: 文档只陈述真实最终结果，不把 build/httptest 推导为浏览器或用户验收；精确提交 `docs(web-11): document W3b changes review`。
- 依赖: T40 全部结束。

### W3B-T42 收口 Git、Beads、G032 与最终报告

- 文件: 不改 source；仅更新 tools Beads `tools-yal` 状态/notes。若代码/文档尚有未提交 owner，先按其功能点补齐原子 commit，禁止混合提交。
- 动作: 每个 commit 前均用 `git status --short`、精确 `git add <owned paths>`、`git diff --cached --name-only`/`--check` 确认 owner；最后 `bd -C tools close tools-yal --reason ...`，不执行任何 Beads remote sync。
- 验证: `git log --oneline --decorate -12`、`git status --short`、`git status -sb`、`bd -C tools show tools-yal`；确认没有 source/test/docs 未归属改动、没有 push/deploy/restart/config动作。
- 完成标准: 最终报告按注入格式逐任务列状态/一句话/commit hash，贴 gofmt、Windows/Linux build、vet、三包测试、Web 三门、控制字符扫描的原始关键行与 exit code，再列 git log/status、G032 清单、未完成与人工决策点；明确 source、runtime/browser、deployment、push、acceptance 五个状态。
- 依赖: T41。

## 回滚与恢复

- 每个功能点独立 commit；回滚按 docs → Web UI → draft module → review/API/security → diff domain → RED tests 的逆序 `git revert <sha>`，不得 `reset --hard`、checkout 覆盖或删除用户工作。
- 无 DB migration、新依赖或 server config。Web localStorage 使用版本化 key；回滚 source 后旧 key 变成无 consumer 的浏览器数据，不需批量删除。若确需用户清理，只给浏览器侧单 key 操作，不操作其他本地数据。
- live/captured helper 无后台 goroutine；HTTP request context、Git child、frontend timer/AbortController都必须在 request/unmount 收口。恢复时以最近 atomic commit、`git status --short`、fixed test和 validation log 为检查点，不从半写 source 继续。
- 若 3600 秒接近上限，先完成并验证当前功能点、精确提交，等待已启动 test session 到明确 PASS/FAIL；不能完成的后续任务保留 Beads in_progress并按 6 行中断卡报告，不用骨架或 `t.Skip` 冒充完成。

## 人工 Gate

1. **Plan Approval Gate（当前）**：监督者需明确批准本文件 Draft 0.1 candidate；批准对象由 document revision、subject path 与提交后的 candidate commit 固定。未批准不实施。
2. **Current Execution Gate（后续）**：计划批准后，用户续接本会话并明确要求继续实施，才进入 `DIRECT_CONTINUOUS`。既有设计批准、监督者代理权限或本轮“等待审批”都不能替代当前执行请求。
3. **Semantic Amendment / Ownership Conflict Gate**：若需要改变 diff/review response、turn/needs_review、Schema、安全/权限、外部 action、验收或命中他人 dirty owner，立即停止并回到 design/plan review；普通同 owner path discovery 按合同记录后继续。
4. **External Action Gate**：本计划不申请 push、release、deploy、migration、device、traffic、外部消息、live restart/reload 或真实配置。即使 source 完成，这些仍为 `NOT_AUTHORIZED/NOT_RUN`。

## 可追溯性

| 需求/验收 | 设计/任务依据 | 实施任务 | 可观察验证 |
|---|---|---|---|
| 首轮基线、live跨轮提交/未提交/untracked、2MiB、captured、无base 409 | W3“改动视图”；T1/T2.1 | T10,T20 | `TestThreadDiffSpansChain`、GET JSON、Git temp repo |
| Git 10s/2MiB、命令不在 handler、G021/G022 | T2.1；G021/G022 | T20,T40 | domain source review、timeout/cap tests、build/vet |
| summary+两评论+±2上下文、同session turn、seen、job id | W3“行内评审”；T1/T2.1 | T10,T21 | `TestReviewCommentsBecomeNextTurn`、recording resume/seen assertions |
| 400/409/数量/长度/one-shot；needs_review不旁路 | T1；既有 Turn/ResumeJob | T10,T21 | fixed subtests、existing workbench/job review tests |
| job caller GET diff可读、POST review 403 | T1；SEC-01 | T10,T21 | `TestReviewJobCallerForbidden`、middleware route key |
| 草稿按会话增删改、请求体、localStorage try/catch | T1/T2.2 | T10,T30,T31 | `reviewDrafts.test.ts`、typecheck/source review |
| 过程/改动、文件列表+UnifiedDiff、10s/tool刷新 | T2.2；W3改动视图 | T31 | Web tests/typecheck/build、timer/event source review |
| 行号＋、内联编辑删除、确认发送、成功切过程 | T2.2；W3行内评审 | T30,T31 | pure tests、typecheck/build、临时 browser smoke如运行 |
| Accept needs_review，否则seen；locations跳转/无改动提示；窄屏 | T2.2；W3a locations | T31 | source/typecheck/build、focused manual review |
| README 与 W3b 实测记录最后且不虚报 | T2.3；文档规则 | T41 | README search、design/plan validators、记录只消费T40结果 |
| 按功能点 commit、原始输出、G032、无push/live/config | house-rules/gofer-repo | T10–T42 | cached owner、git log/status、G032清单、lifecycle状态 |

## 完成 Gate 与剩余工作

W3b 只有在 T10–T42 达到各自完成标准、三项固定 Go contract与 Web draft contract从真实 RED 转 GREEN、diff source/limits/fallback与 review turn/security合同闭合、过程/改动和评论/接受/跳转在 source 中接通、所有 required Go/Web/encoding/document gates得到最终结果、慢包已结束、owned 改动按功能点提交且 Git/Beads状态可解释时，才能报告 source implementation 完成。局部 test、route 200、页面截图、typecheck 或 build 单独成功都不能替代该 Gate。

G032 预期清单：

- 保留的既有 legacy 路径：W3a 的 pre-v0.65 stdout fallback 与逐 token thought merge，继续使用其现有 `DEPRECATED(v0.65): remove in v0.68` 标记；W3b 不改它们。
- 当前正式双拼写：`server`/`local` 由 G043 `config.IsBuiltinLocalRunnerName` 统一识别，不是 W3b 新增兼容分支，不加 DEPRECATED。
- 当前产品 fallback：`source=captured` 是 Approved 设计对远端/cwd消失的正式行为，不是 legacy compatibility，不加 DEPRECATED。
- 新增无标记兼容分支：`NONE`；新增 alias/旧 payload/旧 route/旧 server-worker 容忍：`NONE`。
- 删除的旧路径：`NONE`。若实施发现没人使用的旧路径，先按 Operational Discovery 记录，不能顺手扩围删除。

明确留待后续：真实浏览器/手机目视与用户试用、真实三轮 ACP + 行内回灌、live server 升级、push/release/deploy，以及 W4 worktree 合并/归档/看板/预览。它们不因 W3b source/tests完成、设计已批准或 `DIRECT_CONTINUOUS` 获得授权；最终逐项报告 `NOT_RUN/NOT_AUTHORIZED`，由后续明确请求决定。
