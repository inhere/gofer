<!-- template_id: design; template_version: 1.1.1 -->
# web 工作台设计（WEB-11）

> 状态：Approved（文档 identity：Draft 0.3）/ 实施中（2026-09-26 用户经 web 中继批准，决策 1–7 照写；W1 试用后用户经 web 中继要求「继续推进 W2」，0.3 只细化 W2 实现口径，不改决策）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.3 | 2026-09-26 | Claude | W1/F16 上线（v0.62.1）并经用户 web 试用后启动 W2：新增「W2 细化」一节（布局树模型与持久化、ctrl+b 前缀表、手机布局、PWA、Web Push 的 VAPID/订阅/触发/通知内审批的一次性动作令牌与降级口径、固定测试名） |
| 0.2 | 2026-09-26 | Claude | 按用户要求调研主流开源 agent 桌面/web（Zed 并行 agents、OpenCode web/desktop、Vibe Kanban、Nimbalyst/Crystal、Claude Squad、Codeman、Conductor、OpenHands）后细化：**以"会话（thread）"为一等公民**而非窗格；加入快速发起、注意力队列、会话内评审→评论回灌同一会话、可操作的推送通知、命令面板与 ctrl-tab、PWA；分期改为 W1 会话列表+单视图（先可用）→ W2 布局与移动端 → W3 对话窗格与评审 → W4 worktree 生命周期与预览（可选） |
| 0.1 | 2026-09-26 | Claude | 初稿：以 herdr 的工作区/标签页/窗格 + 状态上卷为骨架 |

## 背景与目标

用户（2026-09-24）：「想基于现有功能做 web 工作台页面，方便我远程**开始**工作——现在还是以终端为主，web 是被动处理任务；初步想法类似现在的 agent 桌面版。」2026-09-26 又要求参考 herdr 的工作区/布局概念，并调研主流开源 agent 的 desktop/web，避免做出来有可用性差距。

现在的 web 是**按对象组织**的（Board/Plans/Review/Sessions 各一页），适合"来处理一件事"，不适合"坐下来干活"。目标是**按工作组织**：一眼看到所有正在进行的会话和谁在等我，一键发起新任务，在同一处看过程、接着说话、看改动、给反馈。

## 范围与非目标

范围为本设计 W1–W4 的 web 工作台能力；非目标：浏览器里的完整 IDE；多人同屏协作；替代 CLI。

## 已确认事实与规范：调研可用性要点（按"做不到就会被嫌弃"排序）

| # | 要点 | 谁这么做 | 对 gofer 的含义 |
|---|---|---|---|
| 1 | **会话（thread）是一等公民**：侧栏按项目分组列出会话，每条带标题、状态点、跑它的 agent；点一下主区切到该会话 | Zed 并行 agents（Threads Sidebar 按项目分组、`ctrl-tab` 最近切换、侧栏搜索）、OpenCode（server 持有会话、多客户端共享）、Claude Squad | 0.1 以"窗格"为中心是反的：用户先想"哪个会话"，再想"怎么摆"。**会话 = 同一 `session_id` 串起来的一串 job（首轮 + resume 轮次），或一个中继会话** |
| 2 | **注意力优先**：哪些在等我（审批、提问、完成待看）必须一眼可见，并能**在通知里直接批** | herdr（状态上卷）、Codeman（桌面/web 推送带审批）、Nimbalyst（按状态看板） | 顶部/侧栏"等你处理"队列，按等待时长排序；浏览器推送里对 permission 给「允许/拒绝」按钮 |
| 3 | **快速发起**：一个输入框「在项目 X 用 agent Y 做 Z」，回车即开干 | Vibe Kanban（建任务即派 agent）、Codex/Jules 类 web、Nimbalyst（连语音发起都有） | 顶部常驻 composer：项目、agent、模式（终端/对话）、可选 worktree/plan todo/skills，回车 = 提交 job 并在主区打开 |
| 4 | **两种观看方式都要**：实时终端（跟进长会话）与结构化评审（看改了什么、给意见、合并） | Codeman（live terminal）vs Vibe Kanban（review workspace，内联 diff 评论回灌 agent） | 每个会话都有「过程」与「改动」两个视图；**在 diff 上写评论 → 作为下一轮 resume 发给同一会话**（MCP-05 评论 + resume 已具备） |
| 5 | **并行隔离**：并行跑多个 agent 时每个可选独立 worktree，完成后评审、合并、归档清理 | Conductor、Nimbalyst、Vibe Kanban、Zed、Claude Squad、dmux（一键 merge） | WT-01 已有 `--worktree`；工作台把它做成发起时的一个开关 + 会话头上的「合并/归档」 |
| 6 | **服务器持有、随时重连、跨设备** | OpenCode（`/global/event` SSE 同步所有客户端）、Codeman（手机扫码登录、触控）、Nimbalyst（iOS 监控） | gofer 本来就是 server 持有；补齐：PWA 可安装、手机布局、登录即恢复上次工作台 |
| 7 | **键盘效率**：命令面板、最近会话切换、分屏快捷键 | Zed（`ctrl-tab`、侧栏搜索）、herdr（前缀键）、Claude Squad（TUI 快捷键） | `ctrl+k` 命令面板（跳会话/发起/切项目/执行动作）、`ctrl+tab` 最近会话、`ctrl+b` 前缀做布局 |
| 8 | **跟随 agent**：看它正在读/改哪些文件 | Zed（follow agent，编辑器跟着跳） | 对话窗格里工具调用可点开文件；「改动」视图实时刷新（按 ACP 工具事件或定时 git status） |
| 9 | **看板视角**：按状态（进行中/待评审/完成）看全部会话 | Nimbalyst、Vibe Kanban | 侧栏可切"列表 / 看板"；plan 看板（WEB-10）继续作为计划维度 |
| 10 | **预览**：一键起 dev server 看效果 | Vibe Kanban（Start Dev Server + Preview） | 可选（W4）：项目配 `dev_command`，经隧道（TUN）给出预览链接 |

herdr 的工作区 → 标签页 → 窗格与**状态上卷**保留，但降为"布局层"：会话是内容，布局只是摆放。

## 总体方案：概念模型

- **工作区** = 项目（`project_key`）。侧栏的分组单位；状态由其会话上卷，并计入项目内不属于任何会话的待处理项（needs_review、plan.blocked、decision）。
- **会话（thread）**：
  - agent 会话：同一 `session_id` 串起来的 job 链（首轮 + 若干 resume 轮次）；没有 session_id 的一次性 job 自成一个会话。
  - 中继会话：SESS-01 登记的外部终端会话。
  - 标题：首轮 job 的 title，没有就取 prompt 前 30 字；可重命名（存 server）。
  - 状态（gofer 直接给，不猜）：`blocked`（待答审批/提问）> `working`（running/waiting_dir）> `review`（needs_review，或终态且有未看的改动）> `done`（终态已看）> `idle`（会话存活无活动 job，等你下一句）；另有 `stalled`（AUTO-05 输出停滞，黄色，"疑似卡住"）。
- **视图**：主区显示当前会话；会话有三个子视图——「过程」（终端或对话流）、「改动」（本会话以来的 diff + 内联评论）、「信息」（job 链、用量、规则/skills、worktree、日志）。
- **布局**（W2）：主区可分屏同时看多个会话；标签页保存不同的摆法；布局存 server、按 caller。

```
┌ ≡ gofer  [ 在 hyy-ai-inspect 用 omp 做…            ▾对话 ▾worktree  ⏎ ]   ⚠ 等你 2 ┐
├ 会话 ─────────────────┬ omp · 修复 tun 逗号 ── ● working ── 过程 | 改动 | 信息 ───┤
│ ▾ hyy-ai-inspect  ●    │ assistant: 我先看 spec.go…                             │
│   ◐ 修复 tun 逗号  omp │ ▸ 读取 internal/tunnel/spec.go                          │
│   ● 设置页二级菜单 omp │ ▸ 编辑 internal/tunnel/spec.go  (+12 -3)                │
│   ✓ 规则注入验收  omp  │ ┌ 审批：运行 go test ./internal/tunnel ─ [允许][拒绝] ┐ │
│ ▾ zy-bsly-sf-dev  ○    │ └────────────────────────────────────────────────────┘ │
│   ○ nydq 联调     jcode│ > 下一句…                                   [发送]     │
│ 搜索… │ 列表/看板      │                                                         │
└────────────────────────┴─────────────────────────────────────────────────────────┘
```

## 关键流程：关键交互

1. **发起**（composer，或 `ctrl+k` → 新会话）：项目（默认当前工作区）、agent（按项目 allowed_agents，标出能交互/能对话的）、模式（对话：acp-agent；终端：交互 pty；批处理：看日志）、可选 worktree、plan todo、skills/rules、cwd。回车 → 提交 job → 侧栏出现新会话并切过去。
2. **注意力**：顶部「⚠ 等你 N」下拉 = 全部 blocked/review 项按等待时长排序；侧栏状态点上卷到项目；浏览器推送（用户授权后）：blocked 时推送，permission 类带「允许/拒绝」动作按钮（Service Worker 通知动作 → 调现有交互作答接口）；完成时推送"待评审"。
3. **接着说话**：对话会话底部输入框 = 下一轮（resume 同一会话）；终端会话直接键入；中继会话 = 回复框（现有）。
4. **评审**：「改动」视图 = 本会话首轮 `base_sha` 到最新（或 worktree 分支）的 diff（复用 `UnifiedDiff`）；在行上写评论 → 汇总成一条"评审意见"作为下一轮发给同一会话（MCP-05 的评论 + resume）；「接受」= 现有 accept（needs_review）或标记已看。
5. **并行隔离**（W4）：worktree 会话头显示分支；「合并到主干」（exec job：`git merge --no-ff` 或 cherry-pick，冲突则把冲突列表回灌会话）；「归档」清理 worktree（WT-01 retention 已有）。
6. **键盘**：`ctrl+k` 命令面板、`ctrl+tab`/`ctrl+shift+tab` 最近会话、`ctrl+b` 前缀布局（`%` `"` 分屏、方向键切焦点、`x` 分离、`z` 最大化）、`/` 聚焦侧栏搜索、`esc` 从终端里取回焦点。鼠标：拖分隔条、拖会话到主区分屏、右键菜单。
7. **关闭 ≠ 停止**：关闭视图/窗格只是离开，job 照跑；「停止」单独动作、二次确认。
8. **移动端 / PWA**：manifest + Service Worker 可安装；窄屏只显示侧栏或单会话（左右滑切换），composer 与"等你"始终可达；推送在手机上同样可批。

## 架构：后端接口与分层

- `GET /v1/workbench/threads?project=&status=&q=`：返回会话列表（按 `session_id` 聚合 job 链；中继会话并入），每条含状态、标题、agent、项目、最近活动时间、用量合计、未看改动标记、worktree；以及"等你"队列。前端 5s 轮询（`createPoller`），后续可换 SSE。
- `PATCH /v1/workbench/threads/{id}`：重命名、标记已看、置顶（per caller）。
- `GET/PUT /v1/workbench/layout`：布局（per caller，乐观并发）。
- `POST /v1/workbench/threads/{id}/turn`：发下一轮（服务端决定是 ACP resume、cli resume 还是中继回复），对前端屏蔽差异。
- `GET /v1/jobs/{id}/acp/stream`（W3）：ACP 结构化事件 SSE（文本块、工具调用起止与涉及文件、permission 请求/结果、turn 结束）。
- `POST /v1/workbench/threads/{id}/review`（W3）：把一组行内评论合成一轮发回同一会话。
- 推送（W2）：Web Push（VAPID 密钥存 server 配置目录）；订阅按 caller 存；触发复用现有事件（`interaction.created`、`job.needs_review`、`job.terminal`）。

## 五、实施分期（omp，测试先写先提交；每期我远程升级并真机验收）

| 期 | 内容 | 验收 |
|---|---|---|
| **W1 会话中枢（先可用）** | `/workbench`：侧栏会话列表（按项目分组、状态、搜索、上卷）、「等你」队列、composer 发起、主区单会话（终端=现有 attach、批处理=日志、中继=现有回复、ACP=暂时用日志视图）、会话头（状态/用量/job 链/停止）、`threads` 与 `turn` 接口、`ctrl+k` 命令面板与 `ctrl+tab` | 单测：`TestThreadsGroupJobsBySession`、`TestThreadsStatusPrecedence`（blocked>working>review>done>idle，stalled 标记）、`TestThreadsAttentionQueueOrder`、`TestTurnDispatchesByThreadKind`（acp→resume、cli→resume、relay→reply）；真机：从 composer 发起 omp 对话与交互终端各一个，制造审批看"等你"出现并处理 |
| **W2 布局与移动** | 分屏/标签页/持久化、`ctrl+b` 前缀、拖拽、PWA + 手机布局、Web Push（含审批动作按钮）、「已看」语义 | web typecheck+build；布局树纯函数单测（若引入 vitest，仅限此处）；真机：刷新与换设备布局还原；手机收到审批推送并在通知里批准 |
| **W3 对话与评审** | ACP 结构化流 + 对话视图（流式文本、工具调用折叠可点开文件、审批卡内联）、「改动」视图 + 行内评论回灌同一会话、跟随 agent（改动实时刷新） | 单测：`TestACPStreamEmitsStructuredEvents`、`TestReviewCommentsBecomeNextTurn`；真机：omp-acp 连续三轮 + 一次行内评审回灌 |
| **W4 并行隔离与预览（可选）** | composer 的 worktree 开关、会话头合并/归档、看板视角、项目 `dev_command` + 隧道预览链接 | 真机：两个 worktree 会话并行改同一仓库，各自评审后先后合并 |

## 风险与限制

- **范围大**：四期；W1 单独即可用（解决"主动发起 + 看谁在等我 + 接着说话"），后面按需推进。
- **ACP 多轮 = resume 链**：每轮冷启动 ACP 进程 + `session/load`，有秒级延迟；长驻多轮会话另议。
- **gofer 看不到 agent TUI 内部的确认框**：不经审批门的卡住只能按输出停滞标 `stalled`。
- **Web Push** 需要 HTTPS 或 localhost；远程访问若是纯 http 内网地址，推送不可用（退化为页内提醒 + 已有 IM 通知）。
- **多终端同屏性能**：非焦点终端降频渲染。

## W1 实测记录（2026-09-26，不改变 Approved 0.2 设计语义）

实现提交：`af0d136`（固定 HTTP contracts）、`f4b018d`/`0bc1891`（snapshot/prefs 与 decision project bulk mapping）、`ef96efc`（workbench domain）、`c4745f8`（HTTP/SEC-01）、`22f214e`（launcher）、`7c8ab03`（单 thread 主区/键盘/窄屏）、`3cd0b4c`（resume-of-resume 按 `OriginAgent` 解析原 CLI agent）。未 push、未部署、未重启/reload live gofer、未触碰真实配置。

验证分层：

- Source/product PASS：固定 7 项 thread tests、补充 validation/security、`internal/workbench` 与 jobstore tests；Windows/Linux `go build ./...`、`go vet ./...`；Vue `vue-tsc --noEmit`；Vite production build（216 modules）。
- Runtime API smoke PASS（临时 config、随机 `127.0.0.1:61232`、临时 DB/storage、仓内 `gofer-testcmd`）：假 PTY job 到 running、工作台出现 `j:` thread、attach-ticket 成功后取消；假 ACP permission 进入 attention(`answer`)并作答；同一 `s:sess-acptest-1` 经 workbench turn 连续到三轮，三轮均 done、最终 `turns=3`；rename/seen/pin 回读为 title=`ACP 三轮 smoke`、status=done、pinned=true。临时 server 已按精确 binary path 停止，原 live gofer 进程保持未触碰。
- Browser visual NOT_RUN：bsk daemon 无法在 Windows Job Object 下建立可访问 IPC；Orca runtime 无法启动；agent-browser 的 headless/headed Chrome 都在写 `DevToolsActivePort` 前退出。按 Host guardrail 未使用 `--no-sandbox`、未删除 daemon/runtime 文件、未重启共享浏览器。因此 command palette、Ctrl+Tab、终端 Esc、窄屏视觉和截图描述仍缺真实浏览器证据，不能宣称目视验收通过。
- Full Windows package baseline：`internal/workbench`、`internal/httpapi`、`internal/jobstore` PASS；`internal/job` 仍有实施前已复现的 `TestGetArtifactManifest` 与 `TestWorktreeSymlinkedProjectRoot` 两项非 W1 失败。新增 `TestResumeOfResumeUsesOriginAgent`、全部 `TestResume*` 及 HTTP workbench turn 均 PASS。

### F16：review 口径收窄 + 已看基线（不改变 Approved 0.2 设计语义）

W1 真机默认 7 天窗口返回 412 个会话、attention 406 项，主因是实现把“终态且 caller 未逐项看过”都归为 review，偏离了本设计 §二“needs_review，或终态且有未看的改动”。F16 corrective 将投影收敛为：`needs_review` 始终 review；未看的 failed/timeout/rejected 为 review；未看的成功终态仅在最新 job 的 commits 非空或 diff 快照非空时为 review；cancelled、成功无改动和 exec agent 成功均为 done。

caller 第一次 GET threads 时建立全局已看基线，早于它的历史终态视为已看；`POST /v1/workbench/threads/seen-all` 单调推进该基线，前端“等你”下拉提供“全部标记已看”。per-thread rename/seen/pin 继续隔离；真正的 `needs_review` 不受 baseline 或逐项 seen 影响。thread 的 agent 改取首轮 job；首轮为 exec resume 载体时取 `OriginAgent`，空值才回退 exec。

测试/实现提交：`83b46c3`、`845cd07`（固定测试与时间夹具）、`536a853`（后端 projection/baseline/endpoint）、`31f6356`（Web seen-all）。四个固定测试、完整 `internal/httpapi`/`internal/jobstore` 包、Windows/Linux build、vet、Vue typecheck、控制字符扫描及三份文档 validator 均 PASS；未部署、未重启/reload live gofer、未触碰真实配置。

## W2 细化（0.3，W2 实施口径）

### 布局（分屏 / 标签页 / 持久化）

- 布局 = 若干**标签页**，每个标签页是一棵二叉**分屏树**：叶子 `{kind:"pane", thread_id|null}`，内部节点 `{kind:"split", dir:"h"|"v", ratio:0.1–0.9, a, b}`；每个标签页记 `focused` 叶子路径。一个标签页最多 4 个窗格（超出拒绝并提示），总标签页上限 8。
- 纯函数模块 `web/src/components/workbench/layoutTree.ts`：`splitPane`、`closePane`（兄弟节点上提）、`focusDir`（按几何方向找最近窗格）、`setRatio`（夹到 0.1–0.9）、`assignThread`、`normalize`（修复空/非法树）；vitest 单测只覆盖这个模块。
- 持久化：`GET/PUT /v1/workbench/layout`，per caller 存 server（新表 `workbench_layouts {caller_id, version, body_json, updated_at}`）；PUT 带 `version`，不匹配回 409 并返回当前版本（乐观并发，前端拿到 409 就采用 server 版本并提示"布局已在别处更新"）。body 上限 64 KiB，未知字段原样保存；job caller 只读禁止 PUT。前端改动 800ms 去抖保存。
- 窗格内容就是 W1 的单会话主区组件（抽成 `ThreadView`）；非焦点窗格的终端降频（xterm 不卸载，但停 fit/轮询，只保留 WS），日志视图暂停自动滚动。
- 键盘：`ctrl+b` 进入前缀（1.5s 超时，状态栏提示），其后 `%` 左右分、`"` 上下分、方向键切焦点、`x` 关窗格（不停 job）、`z` 最大化/还原当前窗格、`c` 新标签、`n`/`p` 下一个/上一个标签、`1`–`8` 跳标签。焦点在终端里时 `ctrl+b` 同样被前缀截获（设计已约定 esc 取回焦点，前缀只截这一个组合键）。
- 鼠标：拖分隔条改 ratio；从侧栏拖会话到窗格 = 放进该窗格，拖到窗格边缘 1/4 区域 = 朝该方向分屏后放入。

### 手机与 PWA

- 宽度 < 768px：不显示分屏，只显示当前标签页的焦点窗格；侧栏与主区两屏，左右滑动或返回按钮切换；composer 折叠为底部「＋」、「等你 N」固定顶栏。
- PWA：`site.webmanifest` 补 `start_url:"/workbench"`、`scope:"/"`、`id`、maskable 图标；Service Worker `web/public/sw.js` 只负责推送与通知点击，**不做离线缓存**（避免 F8 那类旧 chunk 问题），注册失败不影响页面。

### Web Push

- 密钥：server 首次需要时生成 VAPID P-256 密钥对，存配置目录 `push/vapid.json`（0600）；`GET /v1/push/vapid-public-key` 返回公钥。实现优先用标准库（`crypto/ecdh`、`crypto/hkdf`、AES-GCM；RFC 8291 aes128gcm 加密 + RFC 8292 VAPID ES256 JWT），不引第三方依赖；RFC 8291 附录 A 的测试向量必须过。
- 订阅：`POST /v1/push/subscriptions`（endpoint + keys，per caller，同 endpoint 覆盖）、`DELETE /v1/push/subscriptions`（按 endpoint）、`GET /v1/push/subscriptions`（本 caller 的列表，前端设置页显示"本设备已开启"）。推送服务回 404/410 的订阅自动删除。job caller 禁止订阅。
- 触发：复用现有事件——`interaction.created`（blocked，标题"等你审批/回答"）、`job.needs_review` 与按 F16 口径算作 review 的终态（"待评审"）、`plan.blocked`。一个 caller 只收**自己可见**的项目事件；同一 thread 30 秒内合并为一条（Topic/tag 用 thread id 覆盖旧通知）。推送载荷只放标题、摘要（≤120 字）、thread id、跳转 URL，不放日志正文。
- 通知内审批：permission 类 interaction 的通知带「允许」「拒绝」两个动作。Service Worker 拿不到页面的 bearer token，所以载荷里带一个**一次性动作令牌**：服务端 HMAC 签名，绑定 (caller, job, interaction, 允许的选项)，10 分钟过期、用一次即失效；`POST /v1/push/actions {token, option}` 不要求 bearer，只认令牌，作答走现有 interaction answer 逻辑（记录作答来源 `push`）。令牌过期/已用/interaction 已答 → 返回 409，SW 改为打开页面。
- 降级：`!isSecureContext` 或无 `PushManager` 时设置页显示"推送需要 HTTPS 或 localhost，当前不可用"，工作台用页内提醒（标题栏计数 `(N) gofer` + 顶部提示条）替代；已有 IM 通知不受影响。

### 「已看」语义

沿用 F16：打开会话即 PATCH seen；窗格里可见 ≥2 秒也算看过（非焦点窗格不算）；「全部标记已看」保持。

### W2 固定测试名

- Go：`TestWorkbenchLayoutRoundTrip`、`TestWorkbenchLayoutVersionConflict`、`TestWorkbenchLayoutJobCallerReadOnly`、`TestPushSubscriptionCRUD`、`TestPushEncryptRFC8291Vector`、`TestPushVAPIDJWT`、`TestPushDispatchOnInteraction`（含同 thread 合并、不可见项目不推、410 删除订阅）、`TestPushActionTokenAnswersOnce`（成功一次、重放 409、过期 409、选项不在允许集 400、篡改 401）、`TestPushJobCallerForbidden`。
- web（vitest，只测 `layoutTree.ts`）：分屏、关闭上提、方向焦点、ratio 夹取、上限拒绝、normalize。

### W2a 实测记录（2026-09-26，不改变 Approved 0.3 语义）

codex 计划 `docs/plans/2026-09-26-web-workbench-w2a-plan.md`（`c4228fd`，由用户授权的监督者代为批准，DIRECT_CONTINUOUS）。实现提交：`6456df9`（固定测试 red）、`13582b5`（`workbench_layouts` 表与 `GET/PUT /v1/workbench/layout`）、`8ac10b3`（`layoutTree.ts` 纯函数）、`e55124c`（ThreadView 抽取、分屏树渲染与持久化）、`6c80b01`（拖放、`ctrl+b` 前缀、命令面板、焦点/已看、非焦点降频）、`1d1eefc`（<768px 手机布局）。codex job 在 3600s 超时于 T40 前，文档与验收由监督者在容器补做。

容器验证：Linux/Windows `go build`、`go vet` 通过；全量 `go test ./... -count=1` 45 包 ok；`TestWorkbenchLayoutRoundTrip`、`TestWorkbenchLayoutVersionConflict`、`TestWorkbenchLayoutJobCallerReadOnly` 及 W1/F16 全部工作台用例 PASS；vitest `layoutTree.test.ts` 8/8 PASS；`vue-tsc` 通过；改动文件 gofmt、控制字符、CRLF 扫描均干净。浏览器目视未跑（NOT_RUN），留待上线后用户试用。

## 决策（已批准 2026-09-26）

1. 以**会话**为一等公民（侧栏会话列表 + 主区视图），herdr 式窗格/布局作为 W2 的摆放层。
2. 会话 = 同 `session_id` 的 job 链（或中继会话）；状态优先级 blocked > working > review > done > idle，另标 stalled。
3. 顶部常驻 composer 与「等你」队列；推送里可直接批准审批。
4. 评审评论回灌**同一会话**作为下一轮。
5. 布局、已看、重命名存 server（per caller），跨设备一致；PWA 可安装。
6. ACP 多轮先用 resume 链。
7. 分期 W1 → W2 → W3 →（W4 可选）；W1 做完即上线试用，根据使用反馈再调后续。

## 待确认事项

W1 与 F16 没有未决核心行为。W2 已由用户在 W1 试用后要求推进（口径见「W2 细化」）；W3、W4 是否继续等 W2 试用反馈。

## 结论与人工计划 Gate

Approved 0.2 的决策 1–7 保持有效；0.3 的「W2 细化」是 W2 的实施依据。W1/F16 的实施记录不授权 W3–W4、push、部署、live 服务操作或真实配置变更；这些仍需各自计划与当前请求。

## 参考

- herdr 概念：https://herdr.dev/zh-cn/docs/concepts/ 、https://herdr.dev/zh-cn/docs/agents/
- Zed 并行 agents：https://zed.dev/docs/ai/parallel-agents
- OpenCode web/server：https://opencode.ai/docs/web/ 、https://opencode.ai/docs/server/
- 并行 agent 工具对比（Nimbalyst/Crystal、Vibe Kanban、Conductor、Claude Squad、dmux、Superset…）：https://nimbalyst.com/blog/best-tools-for-running-parallel-ai-coding-agents/
- Vibe Kanban：https://www.vibekanban.com/docs/core-features/creating-projects
- Codeman vs Vibe Kanban（实时终端 vs 评审工作区）：https://getcodeman.com/compare/codeman-vs-vibe-kanban
- OpenHands Agent Canvas：https://openhands.dev/product/gui
