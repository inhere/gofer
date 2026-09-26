<!-- template_id: plan; template_version: 1.2.0 -->
# web 工作台 W2a 布局与手机布局实施计划

> 状态：Draft 0.1 / 待监督者人工计划批准

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-26 | Codex | 初稿：把 Approved 0.3 的 W2 布局、窄屏手机布局和已看语义收敛为测试先行的 W2a；PWA、Service Worker 与 Web Push 留待下一 job |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 规划声明

- `thinking_mode=RIGOROUS`
- `core_objective=在 W1/F16 会话中枢上增加可跨设备持久化的标签页与四窗格布局、原生键鼠操作、窄屏单焦点体验和连续可见两秒的已看语义，同时保持终端与日志实例连续`
- `allowed_scope=Approved 0.3 的 W2 细化中“布局”全部条目、“手机与 PWA”的第一条窄屏布局、“已看”条目，以及任务书指定的测试、文档、隔离 API/browser 验证`
- `non_goals=PWA manifest、Service Worker、Web Push/VAPID/订阅/动作令牌，W3 ACP 结构化流与评审，W4 worktree/看板/预览，live gofer server/worker/config，push、发布与部署`
- `expansion_policy=DEFER_OR_REQUEST`
- `delivery_track=Full`：增加 REST write Interface、SQLite Schema、持久化状态模型，并跨 Go/Vue/xterm 多个 owner。候选不操作生产数据，使用 additive table 且可按本地提交回滚，无 live/remote 动作，治理暴露度为低；一轮合并 Standards+Spec 评审封顶。
- `review_budget=一轮 discovery、一轮 confirmation；只有 CORE_BLOCKING、Semantic Amendment 或 Ownership Conflict 才修订候选并重审`
- `stop_condition=计划文档 validator PASS、精确本地提交并生成 candidate identity 后立即停止；未获监督者计划批准与本会话新的当前执行请求前不进入 W2a-T10`

## 目标与完成定义

目标是在现有 `/workbench` 上交付 W2a：主区按标签页承载最多四个窗格的二叉分屏树，布局按 caller 在 server 持久化；用户可通过标签条、分隔条、侧栏拖放、`ctrl+b` 前缀表和命令面板操作布局；桌面端多窗格保持终端连接，窄屏仅显示焦点窗格并能在侧栏/主区间滑动；会话只有在焦点窗格连续可见至少两秒后才标记已看。

完成不是“页面能渲染”或“某个单测通过”。只有下列结果全部成立才算 W2a source/runtime 完成：

1. 任务书指定的三个 Go 固定名测试先 RED 后 GREEN；布局 GET/PUT、版本冲突、64 KiB 上限、非法 JSON、job caller 只读和 caller 隔离均从公开 HTTP seam 可观察。
2. `vitest` 只作为 devDependency 引入，`pnpm test` 固定为 `vitest run`；`layoutTree.ts` 的分屏、关闭上提、末窗格空叶、四方向焦点、ratio 夹取、4 窗格/8 标签上限和非法树修复全部有纯函数测试，无 `t.Skip` 或骨架占位。
3. 页面刷新可恢复 server 布局；800ms 去抖写使用乐观版本，409 丢弃本地冲突候选、采用最新 server 布局并显示“布局已在别处更新”。
4. 标签重命名/关闭/新建、分隔条拖动、侧栏中心/边缘拖放、`ctrl+b` 前缀表和命令面板布局动作均能操作同一纯函数状态；组件不直接原地修改布局树。
5. 非焦点终端保持挂载与 WebSocket、停止 fit；非焦点日志继续接收数据但暂停自动滚动。焦点且真实可见连续两秒才 PATCH seen，失焦、隐藏、切标签或切到手机侧栏会取消计时。
6. `<768px` 只展示焦点窗格，侧栏/主区可水平滑动或按钮返回；composer 折叠为底部“＋”，“等你 N”固定顶栏。
7. Go/Web 全质量门、临时 server API smoke、文档更新、控制字符扫描和 Git 边界检查都有 exit code 与原始输出；真实浏览器若环境可用则完成指定目视，否则明确 `NOT_RUN` 与首个环境阻断，不伪造视觉验收。
8. 测试、后端、纯布局模型、桌面布局交互、手机布局和文档按功能点独立本地提交；最终不 push，不重启/reload live gofer，不触碰真实配置目录。

## 范围、排除项与授权

范围包括：

- `GET/PUT /v1/workbench/layout`；caller 维度的 `version + body`，事务内乐观并发和 additive `workbench_layouts` 表。
- 前端布局文档、标签页与二叉分屏树的 immutable helpers、递归 `LayoutPane.vue`、`ThreadView.vue`、分隔条、拖放和最大化的页面内状态。
- 标签页上限 8、每标签窗格上限 4、split ratio `0.1–0.9`、非法/旧布局输入的安全 normalize。
- `ctrl+b` 1.5 秒前缀表和状态提示；命令面板的左右分屏、上下分屏、关闭窗格、最大化、新标签。
- 非焦点终端/log 行为、焦点可见两秒 seen timer，以及 W1 polling/SSE/interaction/turn/stop 等既有能力在多窗格中的复用。
- `<768px` 的单焦点窗格、两屏切换/滑动、底部“＋”composer 与固定 attention 顶栏。
- README 工作台布局/快捷键说明、Approved 0.3 设计的“W2a 实测记录”和本计划 progress。

明确排除：

- `site.webmanifest`、maskable 图标、`sw.js`、离线缓存、PushManager、VAPID、订阅、推送触发与通知内审批；它们属于下一 job。
- W3 的 ACP 结构化事件、对话流、工具调用文件跟随、diff/行内评论；W4 的 worktree 生命周期、看板和 dev server 预览。
- 新前端全局状态库、第三方 split-pane/drag-drop/手势库、第二个持久化后端、layout 兼容 route、旧 server fallback 或隐藏的 localStorage 真源。
- 改变 W1 thread/status/attention/turn 合同、F16 review 判定、job/session/relay 状态机，或以“布局”名义停止/重启任何 job。
- push、pull/rebase、tag、release、deploy、迁移/清理真实数据、真实配置读写、live server/worker restart/reload、远端 worker、外部消息和硬件动作。

当前请求只授权生成、验证并本地提交本计划候选；不授权实施。候选获批后仍须由本会话收到新的当前执行请求。执行方式已选择 `DIRECT_CONTINUOUS`，但该选择只决定获批后的连续推进方式，不越过本次 Plan Gate。

- `host_or_non_offline_action=REQUIRED`

该值只覆盖获批实施中的本机临时二进制、临时配置/DB、随机 loopback 端口 API smoke 和条件允许时的隔离浏览器目视；不覆盖 live/remote/真实配置或任何出机器动作。

## 输入与批准证据

- 唯一设计依据：[2026-09-26-web-workbench-design.md](../design/2026-09-26-web-workbench-design.md)，状态 `Approved`、identity `0.3`，当前设计基线提交 `22c54d2`；本计划只消费其中“W2 细化”的布局、窄屏第一条与已看语义。
- 当前任务书明确把本 job 命名为 `WEB-11 W2a`，逐项指定 Go/Web 固定测试、实现边界、验证矩阵、`DIRECT_CONTINUOUS`，并要求候选提交后停止等待监督者审批。
- 计划批准可由用户已授权的 gofer 监督者 Claude 代为给出；设计已批准、validator PASS、candidate commit 或执行方式选择都不自动构成计划批准或实施授权。
- 跟踪项：tools Beads `tools-9gh`（`[gofer][WEB-11] W2a 工作台布局实施计划候选`）；tools DB 从 workspace 以 `bd -C tools ...` 访问。

workspace baseline（2026-09-26 规划时）：

- Git root：`D:/work/inhere/hyy-ai-inspect/tools/gofer`；branch=`main`；HEAD=`22c54d26a69cf6ec239f2be68340cffa61895b65`；`git status --short` 无输出。
- 工具：`go version go1.25.10 windows/amd64`、Node `v24.15.0`、pnpm `12.5.1`；`web/pnpm-lock.yaml` 为 lockfile v9，当前 `package.json` 没有 test script 或 Vitest。
- Codebase Memory 项目=`gofer`、Verify Tier 2、generation=`2026-09-05T06:59:33Z`。coverage 检查的 27 条候选路径均为 `not_tracked`、`metadata_changed` 或 excluded；`web/src/api/types.ts:1-989` 另有 `parse_partial`。因此当前 W1 owner、路由、Schema、xterm fit 和日志滚动事实均已从 HEAD 精确源文件回读，图的空结果不用于负面或完整性结论。
- 现有唯一复用链：`httpapi.Server.workbench -> workbench.Service -> jobstore.Store`；`schemaStmts` 在每次 `Open` 幂等应用；job credential 对 GET 默认只读开放、未列入 allowlist 的 write 默认拒绝；前端 W1 已有 threads poller、sidebar、composer、attention、command palette、单 `WorkbenchThreadPane`、`AttachTerminal` 与 `LogTape`。
- 当前 `AttachTerminal` 的 `ResizeObserver -> FitAddon` 和 `LogTape` 的 pinned auto-scroll 都没有 focused gate；`Workbench.vue` 仍以单 `selectedID` 渲染一个 pane，窄屏阈值为 760px。这些是 W2a owner extension，不另建平行实现。

预期 owner 路径：

- 后端 store/domain：`internal/jobstore/store.go`、`internal/jobstore/workbench.go`、`internal/jobstore/workbench_test.go`、`internal/workbench/model.go`、`internal/workbench/service.go`、`internal/workbench/service_test.go`。
- HTTP/security/tests：`internal/httpapi/workbench_handler.go`、`internal/httpapi/workbench_test.go`、`internal/httpapi/server.go`、`internal/httpapi/jobcredential.go`、必要时 `internal/httpapi/jobcredential_test.go`。
- Web test infrastructure/data：`web/package.json`、`web/pnpm-lock.yaml`、`web/src/api/types.ts`、`web/src/api/workbench.ts`。
- Web layout：`web/src/views/Workbench.vue`、新建 `web/src/components/workbench/layoutTree.ts`、`layoutTree.test.ts`、`LayoutPane.vue`、`ThreadView.vue`；删除被替代且无人继续引用的 `WorkbenchThreadPane.vue`，不留兼容 wrapper。
- Web interaction/performance/mobile：`WorkbenchSidebar.vue`、`WorkbenchComposer.vue`、`WorkbenchAttention.vue`、`WorkbenchCommandPalette.vue`、`web/src/components/AttachTerminal.vue`、`web/src/components/LogTape.vue`。
- 文档：`README.md`、`docs/design/2026-09-26-web-workbench-design.md`、本计划的非语义 progress 记录。

这些 expected paths 是 planning evidence，不是封闭白名单。实施发现新 path/symbol 时，在下一次 mutation 前按 Operational Discovery / Corrective / Semantic Amendment / Ownership Conflict 分类；不得通过扩大 staging 或重算 baseline 绕过 owner 冲突。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | per-caller 原样布局持久化与乐观并发 | `jobstore.schemaStmts`、`writeMu`、workbench prefs、`workbench.Service`、现有 handler/error seam | 复用同一 SQLite metadata DB、caller context、JSON HTTP 与 SEC-01 默认拒绝 | 扩展既有 workbench store/service/handler；事务内比较 version 并返回当前记录 | OWNER_EXTENSION | prefs 表是 thread 展示偏好，不能承载一个 caller 的版本化布局 body | 不建第二 DB/cache；additive table 由旧 binary 忽略，业务语义仍只有 workbench owner |
| CAP-02 | 可测试、不可变的布局树与标签文档变换 | 当前 `Workbench.vue` refs/computed、Vue reactive object、既有 utils | TypeScript、数组/对象结构复制与标准数学函数可直接复用 | 新建单一 `layoutTree.ts`，组件只派发 helper，不直接改树 | MINIMAL_NEW_MODULE | 当前没有 split tree、方向几何、上限和 normalize owner；塞进 Vue 组件会让状态规则无法独立测试 | 新模块只拥有布局值对象/变换，无 IO/全局状态；删除 W2a 时与唯一 consumer/test 一并删除 |
| CAP-03 | 递归窗格、标签页、splitter 与 W1 thread 内容复用 | `WorkbenchThreadPane.vue`、`AttachTerminal`、`LogTape`、`SessionDrawer`、InteractionCard | W1 thread header/content/turn/stop/interaction/SSE 全部直接迁移复用 | 抽成 `ThreadView(focused)`；`LayoutPane` 只递归排版、drop zone、splitter 和 focus | THIN_ADAPTER | 单 pane view 无法表达标签/分屏，W1 内容本身无需重写 | 删除旧 ThreadPane 而非双实现；ThreadView 继续由 workbench 页面唯一拥有 |
| CAP-04 | 启动恢复、800ms debounce、409 server-wins | `request` API、现有 page refs/error、`createPoller` 生命周期 | GET、成功 PUT、页顶错误/提示 seam 直接复用 | Workbench 页面持有 layout version、dirty generation、in-flight 和 timer；409 后 GET 最新并 normalize | OWNER_EXTENSION | 当前页面状态只在内存，没有 layout API/version | 不引 Pinia/localStorage 作为真源；server body 是唯一跨设备状态 |
| CAP-05 | 键盘、标签、拖放与指针分隔交互 | W1 window capture keydown、command palette、HTML Drag and Drop、Pointer Events | 原生浏览器 API 和当前命令面板直接复用 | 扩展前缀状态机、sidebar draggable、pane drop zones 与 pointer-capture splitter | OWNER_EXTENSION | 当前只有 ctrl+k、ctrl+tab、`/`、Esc，没有布局命令或 drag target | 不引 gesture/split-pane/DnD 依赖；所有入口汇合到 CAP-02 helpers |
| CAP-06 | 多 pane 生命周期、非焦点降频与 seen timer | `AttachTerminal` ResizeObserver/FitAddon、`LogTape` pinned scroll、existing PATCH seen | WebSocket/SSE 继续挂载，现有 seen endpoint 直接复用 | 增加 focused/visible gate；恢复焦点时单次 fit/scroll；ThreadView 管 2s 连续可见计时 | OWNER_EXTENSION | 当前切 thread 会卸载唯一 pane且立即 seen；终端/log 无 focus 输入 | 不复制连接或日志流；非焦点只抑制视图副作用，不停止数据源 |
| CAP-07 | `<768px` 单焦点窗格与两屏导航 | W1 mobile sidebar/main class、back button、CSS media query | 复用 `mobilePane` 与现有返回事件 | CSS/v-show 保持 pane mounted；原生 touch delta 判定水平滑动；composer/attention 改为 mobile presentation | OWNER_EXTENSION | 当前阈值 760、composer 仍占顶栏且无滑动，不能满足 W2a 手机口径 | 不引手势库；桌面 DOM/状态与手机共用，避免第二页面 |
| CAP-08 | contract tests、质量门与隔离 runtime smoke | 当前 `newWorkbenchTestServer`、`t.TempDir()`、Go test helpers、temp config/random port、W1 smoke 边界 | HTTP test seam、testcmd、browser provider 和 repo `tmp/` 直接复用 | 增加 Vitest node-only runner与 W2a 临时 smoke 产物 | OWNER_EXTENSION | Web 当前无单测入口，布局纯函数缺可执行契约 | Vitest 仅 devDependency且仅测 layoutTree；禁止 fallback 到 live server/config |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| MOD-01 | CAP-02 | `web/src/components/workbench/layoutTree.ts` | `Workbench.vue`、现有 `web/src/utils/*`、Vue component local state | 无既有 split/tree/tab/normalize 纯函数 | 把变换继续写在 Workbench/LayoutPane 会让两个组件各自拥有上限、focus path 与 close promotion 语义 | 设计和固定 Vitest 场景要求一个无 Vue/DOM 依赖的唯一 owner | owner 为 workbench layout value model；生命周期随 W2a 页面，公开面只供同目录组件/tests | 若 W2a 撤销，先移除 LayoutPane/Workbench consumer，再删除 module/test；不得复制 helper 回组件 |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| Pinia/Vuex 或第二个 layout store | CAP-04 | 删除后 Workbench 单页面 refs + server layout 已覆盖生命周期与跨设备状态；全局 store 会形成第二真源 |
| 第三方 split-pane、DnD 或手势库 | CAP-05 | 删除后 Pointer Events、HTML Drag and Drop 和 bounded touch delta 足以完成固定交互；新增运行时依赖没有额外验收收益 |
| layout localStorage fallback 或旧 server `/threads` 拼装 fallback | CAP-04 | 删除后新 API 是唯一持久合同；fallback 会违反 G032 并掩盖 server/client 版本不匹配 |
| jsdom/browser test environment | CAP-08 | 删除后 `layoutTree.ts` 是 node-only 纯函数，Vitest 默认环境即可；UI 行为由 typecheck/build 与条件浏览器 smoke 覆盖 |
| PWA/Web Push 预埋接口 | CAP-07 | 删除后 W2a 的窄屏布局完整成立；manifest、SW、push 是下一 job 的独立协议/安全 owner |

## 前置检查与 fail-closed 条件

1. 实施前核验批准对象的 document revision、candidate commit、subject path；候选有语义变化时先生成新 revision 并重新审批。计划批准后还必须收到本会话新的当前执行请求，不能把 `DIRECT_CONTINUOUS` 当成自动启动。
2. 每个 mutating stage 前复核 IDEV-STD fingerprint；核验 Git root=`tools/gofer`、branch/HEAD/status 与 exact owner。发现用户/其他 Agent dirty 或 staged 文件立即停止，禁止 hunk staging 绕过。
3. 从 workspace 使用 `bd -C tools show/update tools-9gh`；不得在 nested gofer 目录 `bd init`。实施开始时把任务/子阶段记入该 issue，结束时同步真实状态。
4. W2a-T10 先写测试并单独提交。Go tests 必须编译并以 404/旧行为 RED；Web RED 必须来自尚不存在的 `layoutTree.ts`/未实现合同，不添加 production stub；所有新增测试禁止 `t.Skip`。
5. 所有 tracked 文本修改只通过 `apply_patch`；改过的 Go 文件可用 `gofmt` 做格式化。`package.json`/lockfile 先形成明确 patch，之后的 `pnpm install` 不得留下未归属 manifest/lock 漂移。
6. Go 测试全部使用 `t.TempDir()`；smoke 仅使用 repo `tmp/web11-w2a/<run-id>/`、临时 config/DB/storage 和随机 `127.0.0.1` 端口。不得读取或写入 `D:/work/inhere/config/win-env/gofer`。
7. 每条 gofer smoke 命令必须显式 `--server http://127.0.0.1:<port>` 或 `-c <temp-config>`；双模式命令不允许依赖自动回落。临时 server 只按记录的精确 PID 停止，禁止 reload/restart live server/worker。
8. 若实现需要改变 layout wire shape、64 KiB/冲突语义、thread/status/seen 合同、增加第三方运行时依赖、增加 Schema/table、触碰 PWA/Push/W3/W4 或 live lifecycle，均为 Semantic Amendment，停止回到 design/plan Gate。
9. 仅要求本次改过的 Go 文件 `gofmt -l` 无输出；仓库已知 8 个历史 gofmt 脏文件和 `internal/worker.TestPolicyCacheRoundTrip` 的 Windows 0600 基线不在范围内，不得顺手修复。
10. Windows 上 `internal/httpapi`/`internal/job` 慢不是跳过理由；允许后台运行，但最终报告必须等 exit code 与 PASS/FAIL 行。任一门失败先区分新增回归与已确认 baseline，不能用 build/health 代替业务验收。

## W2a 合同锁定

### Layout HTTP 与存储

- `workbench_layouts` 固定为 `caller_id TEXT PRIMARY KEY, version INTEGER NOT NULL, body_json TEXT NOT NULL, updated_at INTEGER NOT NULL`；由 `schemaStmts` 以 `CREATE TABLE IF NOT EXISTS` 加法创建，不新增 destructive migration。
- `GET /v1/workbench/layout` 不创建记录。无记录返回 `200 {"version":0,"body":{}}`；有记录返回保存的当前 version 与 raw JSON body。任一已认证 user/job caller可读，但 caller 只能看到自己的行。
- `PUT /v1/workbench/layout` 请求为 `{version, body}`；只允许 user caller。`version` 必须是非负整数，`body` 必须是合法 JSON，trim 后 raw body 不得超过 65536 bytes；非法 JSON/缺字段为 400，超限为 413，且都不得改变当前记录。
- store 在 `writeMu` 保护的 transaction 内读取当前记录（缺失等价 version 0）、比较请求 version、写入 `version+1` 与当前 Unix 秒。成功返回 `200 {version:<next>,body:<submitted>}`；旧 version 返回 409，响应顶层同时含 `{error,detail,version,body}` 的当前 server 值，不执行写入。
- `body_json` 对 backend 是 opaque `json.RawMessage`：不解释树、不剔除未知字段。roundtrip 以 JSON 结构/未知字段保持为验收，不依赖对象 key 顺序或外层空白逐字节一致。
- `internal/workbench.Service` 是 HTTP 与 store 之间的唯一布局 owner；handler 只做 caller Gate、bounded decode、调用与状态映射。`jobcredential.go` 为 `layout` 增加准确的 literal/action 文案，但绝不加入 write allowlist。

### 前端布局文档与纯函数

- 持久 body 的已知形态为 `{active_tab_id,tabs:[...]}`；tab 至少含 `{id,title,focused,root}`。`root` 是 `{kind:"pane",thread_id:string|null}` 或 `{kind:"split",dir:"h"|"v",ratio,a,b}`；`focused` 是从 root 到叶子的稳定 `a`/`b` path。`h` 表示左右排列，`v` 表示上下排列。
- `layoutTree.ts` 只做值变换，不 import Vue/DOM/API。`splitPane`、`closePane`、`focusDir`、`setRatio`、`assignThread`、`normalize`，以及标签新增/关闭/重命名/切换 helpers 都返回新对象；拒绝上限时返回未变对象和 typed reason，组件统一转成提示。
- 每 tab 最多 4 个 pane，总 tabs 最多 8。第 5 个 pane、第 9 个 tab 均保持原树；关闭最后 pane 得到 `{kind:"pane",thread_id:null}`；关闭 split 中任一 pane 把 sibling 原样上提。关闭最后 tab 时保留一个空 tab，避免没有 active/focused owner。
- `setRatio` 把任意有限 number 夹到 `0.1–0.9`；非有限值由 normalize 回到安全默认 `0.5`。`normalize` 修复非对象、缺字段、非法 dir/path、ratio 越界、空/单边 split、重复/空 tab id、active/focused 丢失，并强制 4/8 上限；异常输入不得递归失控。
- `focusDir` 在根节点的归一化矩形中计算叶中心；只考虑目标方向半平面的候选，按主轴距离、次轴距离、path 字典序确定唯一最近 pane；没有候选则保持当前 focus。
- 合法对象上的未知字段在 immutable copy/normalize 中尽量 spread 保留；backend 的 opaque roundtrip 是强合同，frontend 未发生本地 mutation 时不得仅因 GET/normalize 自动回写。

### 页面协调、标签/窗格与持久化

- 启动并行读取 threads 与 layout，二者就绪后再决定默认状态。server body 为空/不可用布局时创建一个 tab、一个空 pane，并把 W1 当前自动选中的 thread 放入该 pane；没有 thread 时保持空叶。该本地初始化按 800ms debounce 保存。
- `Workbench.vue` 持有 server version、normalized document、focus、dirty generation、in-flight generation、debounce timer 与 conflict banner。每次成功 PUT 采用响应 version；若请求飞行期间又有本地变更，成功后继续安排下一次保存，不丢最后一笔。
- 409 不做 merge：立即取消旧 dirty candidate，GET 最新 server layout、normalize 后替换本地状态，并在顶部显示“布局已在别处更新”。其他保存错误保留本地布局和可重试 dirty 状态，显示可见错误，不静默覆盖。
- 标签条提供新建、inline 重命名、关闭与激活；tab title 属于 layout body。最大化 `z` 只是不改树的页面临时显示状态，不持久化，切 tab/关闭目标 pane 时自动清除。
- `LayoutPane.vue` 递归渲染 split/pane；pointer-capture splitter 只根据指针位移计算 ratio 并调用 `setRatio`。所有 focus、assign、split、close、ratio、tab mutation 均回到 layoutTree helpers。
- 现有 `WorkbenchThreadPane.vue` 内容迁到 `ThreadView.vue`，props 固定为 `threadId`、`focused`（thread 数据由 workbench page/lookup 提供可作为内部 computed 输入）；旧文件删除，不保留 G032 兼容 wrapper。pane 关闭只移除视图，绝不 cancel job。

### 侧栏拖放、前缀键与命令面板

- 侧栏 thread row 使用标准 `DataTransfer` 携带 thread id。点击把 thread 放入当前 focused pane；中心 drop 直接 assign；距 pane 任一边 25% 内按最近边分屏后放入，left/right 使用 `h`，top/bottom 使用 `v`，新 pane 位于对应边。
- 边缘 drop 在 4-pane 上限时显示拒绝提示并保持树；中心替换仍允许。同一 thread 可出现在多个 pane，W2a 不引入隐式“从旧 pane 移走”语义。
- `ctrl+b` 在 window capture 层进入 1.5 秒 prefix 状态并显示状态栏；随后 `%` 左右分、`"` 上下分、方向键移焦、`x` 关 pane、`z` 最大化/还原、`c` 新 tab、`n/p` 前后 tab、`1–8` 跳 tab。超时、Esc 或未知键清除 prefix 并提示；布局键不传给 xterm。
- xterm 聚焦时必须截获 `ctrl+b` 及 prefix 生效后的单个布局键；非 prefix 状态的其他终端键保持现有链路，不能借此吞掉普通 Ctrl/方向/输入。`ctrl+k`、`ctrl+tab`、`/` 与 Esc 的 W1 行为继续成立。
- 命令面板新增“左右分屏、上下分屏、关闭窗格、最大化/还原、新标签”，与前缀键调用同一 actions/helpers；禁用或拒绝原因可见。

### Focus、已看、终端/log 与手机布局

- 每个 `ThreadView` 始终以 `focused` 明确状态工作。非焦点 `AttachTerminal` 不卸载、不关闭 WebSocket，只抑制 ResizeObserver/`FitAddon.fit()`；重新获得焦点后执行一次尺寸同步。非焦点 `LogTape` 保持内容更新和新行计数，但不自动切 tab/滚底；重新聚焦时仅在用户原本 pinned 的流上恢复滚底。
- seen timer 只在 pane focused、pane 实际可见、`document.visibilityState=visible` 且有 thread id 时启动。连续 2 秒后一次 PATCH `{seen:true}` 并刷新；任一条件中断、thread/focus/tab/mobile screen/maximize 变化或 unmount 都清除 timer。打开 thread 本身通过 assign+focus 进入同一路径，非焦点 pane 永不计 seen。
- 桌面保留完整 split DOM。`<768px` 时用 CSS/`v-show` 只展示 focus path，隐藏 sibling/splitter但保持 component mounted；sidebar/main 使用既有 `mobilePane`，主区按钮返回，横向触摸位移达到阈值且明显大于纵向位移时左右切换，不劫持普通纵向滚动。
- 手机顶栏固定显示 `WorkbenchAttention`；`WorkbenchComposer` 折叠为底部固定“＋”，点击展开 sheet/面板并复用同一表单实例，提交后收起并切主区。桌面 composer/attention 布局保持。

## 波次与依赖

| 波次 | 内容 | 依赖 | 预期提交 |
|---|---|---|---|
| W2a-W0 | T00 candidate、批准、执行与 baseline 复核 | 本候选获批 + 新的当前执行请求 | 本计划提交为 `docs(web-11): add W2a layout plan`；无代码提交 |
| W2a-W1 | T10 Go/Web 固定 contract tests 和 Vitest 入口先 RED | W0 | `test(web-11): specify W2a layout contracts` |
| W2a-W2 | T20 layout store/domain/HTTP/security | W1 RED 证据 | `feat(web-11): persist workbench layouts` |
| W2a-W3 | T30 immutable layout tree，Web tests GREEN | W1 | `feat(web-11): add immutable layout tree` |
| W2a-W4 | T31 ThreadView、LayoutPane、tabs/splitter 与 server persistence | T20,T30 | `feat(web-11): render persisted split layouts` |
| W2a-W5 | T32 sidebar drag、prefix/palette、focus/seen 与非焦点降频 | T31 | `feat(web-11): add workbench layout controls` |
| W2a-W6 | T33 `<768px` 手机两屏、底部 composer 与固定 attention | T32 | `feat(web-11): add focused mobile workbench` |
| W2a-W7 | T40 文档；T41 全门；T42 隔离 API/browser smoke；T43 收口 | W2a-W6 | `docs(web-11): record W2a validation`；必要时仅 owner 内 corrective commit |

## 任务

### T00 复核 candidate、baseline 与执行边界

- 文件: 无业务文件；只读设计、本计划、AGENTS、Git/Beads/IDEV-STD 状态。
- 动作: 核验批准 candidate tuple、监督者批准原话、当前执行请求和 `DIRECT_CONTINUOUS`；检查 nested Git root/branch/HEAD/status、tools Beads、Go/Node/pnpm；记录 focused Go/Web baseline。确认 live gofer 与真实配置只作为禁止触碰目标，不读取 secret 内容。
- 验证: `git status --short`、`git log -5 --oneline`、`go version`、`node --version`、`pnpm --version`、现有三个 Go package tests与 `pnpm typecheck/build`；命令同时记录 exit code 和原始 PASS/FAIL 行。
- 完成标准: approved candidate 与当前执行请求均明确，worktree 无 owner 冲突，既有失败已独立分类；否则停止。
- 依赖: 监督者计划批准 + 用户续接本会话实施。

### T10 先提交 Go/Web RED contract tests

- 文件: `internal/httpapi/workbench_test.go`；新建 `web/src/components/workbench/layoutTree.test.ts`；`web/package.json`、`web/pnpm-lock.yaml`。
- 动作: 增加固定名 `TestWorkbenchLayoutRoundTrip`、`TestWorkbenchLayoutVersionConflict`、`TestWorkbenchLayoutJobCallerReadOnly`。分别固定：首次 GET version 0/空 body、PUT 0→1 和未知字段回读；旧 version 409 带当前值、body 65537 bytes 为 413 且不改当前、raw malformed JSON 为 400；job GET 200/PUT 403、两个 user caller 互不可见。全部复用 `newWorkbenchTestServer`/`t.TempDir()` 与真实 auth middleware。
- 动作: 只增加 Vitest devDependency、`"test":"vitest run"` 和布局纯函数测试；覆盖 splitPane、closePane sibling promotion、最后 pane→空叶、focusDir 四方向、setRatio 两端夹取、第 5 pane/第 9 tab 拒绝，以及 normalize 的缺字段、越界 ratio、空/单边 split。测试直接 import 尚未实现的 `layoutTree.ts`，不创建 production stub。
- 验证: focused Go 命令必须编译并因 layout route/行为缺失 FAIL；`cd web && pnpm install` exit 0 后 `pnpm test` 必须因 module/contract 缺失 FAIL。扫描新增 `t.Skip` 无命中；记录原始 FAIL 行及 exit code。
- 完成标准: 三个 Go 固定名测试和全部布局场景均为真实断言，RED 只指向 W2a 缺口；精确暂存测试/manifest/lock 并提交一个 test commit。
- 依赖: T00。

### T20 实现 layout store、domain、HTTP 与 caller Gate

- 文件: `internal/jobstore/store.go`、`internal/jobstore/workbench.go`、`internal/jobstore/workbench_test.go`、`internal/workbench/model.go`、`internal/workbench/service.go`、`internal/workbench/service_test.go`、`internal/httpapi/workbench_handler.go`、`internal/httpapi/server.go`、`internal/httpapi/jobcredential.go`、必要时 `internal/httpapi/jobcredential_test.go`、`internal/httpapi/workbench_test.go`。
- 动作: 按“Layout HTTP 与存储”锁定实现 additive table、opaque RawMessage、transactional compare/increment、typed conflict current；扩展 workbench Store/Service 而不把语义塞进 handler；注册 GET/PUT，固定 400/403/409/413/503 与 job route action，PUT 永不进入 job allowlist。
- 验证: 三个固定测试从 RED 到 GREEN；补 jobstore fresh/旧 DB table、concurrent stale writer、body unchanged after rejection，以及 service error mapping；`go test ./internal/httpapi/ ./internal/jobstore/ ./internal/workbench/ -run 'WorkbenchLayout|Layout' -count=1`。
- 完成标准: 无记录 GET 不写 DB；两个 caller/version 独立；同 version 最多一个 writer 成功；未知 JSON 字段回读；handler 保持 G021、依赖保持 G022；一个 backend 功能提交。
- 依赖: T10。

### T30 实现 immutable layout tree 并使 Vitest GREEN

- 文件: 新建 `web/src/components/workbench/layoutTree.ts`，必要的 shared types 留在该文件；`layoutTree.test.ts` 仅在 RED 暴露真实遗漏时补边界断言，不降低既有断言。
- 动作: 实现“前端布局文档与纯函数”全部 invariants、typed rejection、几何 focus 与 bounded normalize；不 import Vue/DOM，不引第三方依赖，不在测试中 mock 实现。
- 验证: `cd web && pnpm test`；`pnpm typecheck`；测试逐项显示 pass，且第 5 pane/第 9 tab 返回未变对象与可识别 reason。
- 完成标准: 固定 Web tests 全 GREEN，所有布局 mutation 只有一个纯函数 owner；独立提交 layout model。
- 依赖: T10。

### T31 抽取 ThreadView 并渲染/持久化 tabs 与 split tree

- 文件: 删除 `WorkbenchThreadPane.vue`；新建 `ThreadView.vue`、`LayoutPane.vue`；修改 `Workbench.vue`、`web/src/api/types.ts`、`web/src/api/workbench.ts`。
- 动作: 把 W1 thread 内容无语义改写地迁到 `ThreadView(threadId, focused)`；递归 LayoutPane、标签条和 pointer splitter 全部调用 T30 helpers。接入 GET/PUT、empty layout 初始化、800ms save、in-flight dirty generation、409 GET-and-adopt 与 banner；tab rename/close/new、focus/maximize 均按锁定合同。
- 验证: `pnpm test && pnpm typecheck && pnpm build`；临时 API fixture 下刷新前后 body/version 相同；409 单元由 Go test 固定，页面通过 mock/manual network 观察 banner 与 server-wins。
- 完成标准: W1 单会话能力在每个 pane 可用；刷新恢复布局；旧 ThreadPane 无引用且已删除；无 direct reactive tree mutation；独立提交。
- 依赖: T20、T30。

### T32 完成拖放、prefix/palette、focus/seen 与非焦点降频

- 文件: `Workbench.vue`、`LayoutPane.vue`、`ThreadView.vue`、`WorkbenchSidebar.vue`、`WorkbenchCommandPalette.vue`、`AttachTerminal.vue`、`LogTape.vue`。
- 动作: 实现中心/边缘 1/4 drop、splitter/limit feedback、1.5s `ctrl+b` prefix 和状态提示、命令面板五项布局命令；window capture 只在 prefix 合同内截获 xterm 键。增加 focused gates 与 2 秒连续可见 timer，移除 W1 的立即 seen 路径。
- 验证: `pnpm test && pnpm typecheck && pnpm build`；用可控 timer/手工 browser 观察非焦点 2s 不 PATCH、焦点不足 2s 不 PATCH、连续 2s 恰好 PATCH；DevTools/临时日志确认非焦点终端 WS 未关闭、重新 focus 才 fit，非焦点 log 不滚底。
- 完成标准: mouse/keyboard/palette 三入口产生同一树结果；普通 xterm 输入不回归；seen 与非焦点副作用满足锁定语义；独立提交。
- 依赖: T31。

### T33 完成 `<768px` 手机布局

- 文件: `Workbench.vue`、`LayoutPane.vue`、`ThreadView.vue`、`WorkbenchComposer.vue`、`WorkbenchAttention.vue`；若实现发现必须拆出同目录纯 presentation component，先记录 Operational Discovery。
- 动作: 阈值统一为 `<768px`；仅显示 focused pane但不卸载 sibling；实现 sidebar/main 水平 swipe 与返回按钮；composer 同实例折叠为底部“＋”/sheet；attention 固定顶栏。触摸判定只接受明显横向 gesture，不阻止终端/日志的纵向滚动。
- 验证: `pnpm test && pnpm typecheck && pnpm build`；浏览器在 767px/768px 两侧验证布局边界、focus pane、返回/滑动、composer/attention 可达，以及终端实例切换后仍连接。
- 完成标准: 任务书手机四项全部可观察，桌面 tabs/split 不回归；独立提交。
- 依赖: T32。

### T40 更新 README、设计实测记录与 progress

- 文件: `README.md`、`docs/design/2026-09-26-web-workbench-design.md`、本计划。
- 动作: README 把 W1 说明扩为 W2a 布局、拖放、完整快捷键、跨设备冲突提示与手机操作，同时明确 PWA/Push 未做。设计追加“W2a 实测记录”，只写真实 commits、tests、API/browser smoke、限制和 lifecycle 状态；这是 Approved 0.3 的 progress/provenance，不递增设计 identity。计划 progress 同样只记录 task/commit/validation/lifecycle。
- 验证: 链接检查、IDEV-STD plan/design validator、`git diff --check`、控制字符扫描。
- 完成标准: 文档不把 source/build/API smoke 冒充部署或用户视觉验收，不声称 PWA/Web Push 完成。
- 依赖: T42 的实际证据；可先起草，但最终数据只能在验证结束后填写。

### T41 执行完整代码与文档质量门

- 文件: 全部 owned changed files；原始输出存 `tmp/web11-w2a/<run-id>/`，不提交临时产物。
- 动作: 对改过的 Go 文件 `gofmt -w`，再对同一精确清单跑 `gofmt -l`；依次执行 Windows/Linux build、vet、三个指定 package tests 与 Web install/test/typecheck/build。慢测试可后台并行，但最终必须等待每个 session 结束。
- 验证: `go build ./...`；PowerShell 子进程设置 `GOOS=linux` 后 `go build ./...` 并恢复环境；`go vet ./...`；`go test ./internal/httpapi/ ./internal/jobstore/ ./internal/workbench/ -count=1`；`cd web && pnpm install && pnpm test && pnpm typecheck && pnpm build`；document validators；`git diff --check`；对 changed text 执行 `rg -n --pcre2 '[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]'`。
- 完成标准: build/vet/tests/install/test/typecheck/build/validator/diff-check exit code=0；`gofmt -l` exit 0 且无输出；控制字符 rg 的原生 exit 1 且无输出表示无匹配，exit 0 表示发现控制字符并失败。报告保留原始 PASS/FAIL 行，不只写“通过”。
- 依赖: T20、T30、T31、T32、T33、T40。

### T42 用临时 server 完成 API smoke，并条件执行真实浏览器目视

- 文件: `tmp/web11-w2a/<run-id>/` 下临时 config、DB/storage、logs、请求/响应、可用时的截图；正式配置零改动。
- 动作: 构建临时 gofer binary，配置两个 user caller 和独立 storage/DB，动态选择空闲 loopback 端口，以隐藏窗口启动并保存精确 PID。所有 gofer 命令显式 `-c` 或 `--server`；直接 HTTP 也只命中该随机地址。结束时只停止记录 PID，并核对 live 进程未变化。
- 验证: API smoke 原样保存首次 GET 0、PUT 0→1、GET 未知字段回读、stale PUT 409 当前值、第二 caller 空布局、超限 413/非法 JSON 400 且当前值不变。若浏览器 provider 可用，再目视/截图：分屏、splitter、中心/边缘拖放、tabs rename/close/new、prefix（含 xterm 内 ctrl+b）、palette、刷新恢复、模拟 409、seen 2s、非焦点 terminal/log、767px swipe/back/“＋”/attention。
- 完成标准: API smoke 必须 PASS；browser 要么按场景 PASS 并有截图/描述，要么在穷尽已授权安全 provider 后写 `NOT_RUN`、首个稳定错误与未证明范围。禁止 `--no-sandbox` 等降级、禁止操作共享浏览器/live server。
- 依赖: T41。

### T43 原子提交、Beads 与最终交付检查

- 文件: 每个功能点 exact owner files、tools Beads `tools-9gh` progress；不修改其他 dirty 文件。
- 动作: 每次提交前 `git status --short`，精确 add，核对 `git diff --cached --name-only`，使用 conventional commit；不得 hunk-stage 绕过 ownership。最终记录各任务状态/一句说明/hash、原始 gofmt/build/vet/test/Web/控制字符输出、API/browser smoke、`git log`、`git status`、G032 清单、未完成与人工决策。
- 验证: `git log --oneline <implementation-baseline>..HEAD`；`git status --short`；Beads show。若有 upstream，只做只读状态说明，不 pull/rebase/push。
- 完成标准: 所有 owned 变更按功能点本地提交，nested worktree 无未归属变更，Beads 与 Git 一致，严格未 push、未部署、未 reload/restart live gofer。
- 依赖: T40、T41、T42。

## 回滚与恢复

- 提交序列保持 test contract、backend API、layout tree、persisted layout shell、desktop controls、mobile、docs 七类原子边界。回滚采用针对性 `git revert <commit>` 方案，不使用 `reset --hard`、`checkout --` 或 `clean`。
- Web 回滚按 consumer-first：先撤 mobile/controls/layout shell，再撤 tree/test infrastructure；backend layout API 可暂时无人调用。不得保留旧 ThreadPane wrapper 或 localStorage fallback。
- Backend 回滚不 drop `workbench_layouts`，旧 binary 会忽略 additive table，caller body 不丢。若未来确认废弃，数据删除属于另一个明确授权任务。
- 409 是 server-wins，无自动 merge；恢复 checkpoint 记录 server version、最后本地 dirty generation、当前 tab/focus 和最后通过的验证，不能通过强写旧 version 恢复。
- smoke 恢复点为 temp root、随机端口、PID 和请求/响应目录；只停止精确 PID，目标身份不匹配即停止，不做递归广域删除。
- 遇到 BLOCKED/PAUSED/FAILED_TERMINAL 时使用统一 6 行中断卡：状态、已完成、当前/已验证、未完成、首个失败边界/影响、唯一下一步；详细输出只引用 `tmp/web11-w2a/` 路径。

## 人工 Gate

1. Design Gate：Approved identity 0.3 已满足，只覆盖 W2a scope freeze；不授权 PWA/Push/W3/W4。
2. Plan Gate：本文件为 Draft 0.1 candidate，提交后必须停止。用户已授权 gofer 监督者代为审批，可使用最小批准语句：“批准该 W2a 计划，按 DIRECT_CONTINUOUS 连续实施”。
3. Execution Gate：计划批准后仍需用户续接本会话形成新的当前执行请求；candidate、review PASS 或执行方式选择都不自动开始实现。
4. Host action Gate：计划批准并请求执行后，只允许 T42 具名的本机临时 binary/config/DB/random-port server 与隔离浏览器；push、release、deploy、live restart/reload、真实 config/data、远端 worker、traffic、外部消息和硬件动作仍未授权。
5. Semantic Amendment/Ownership Conflict Gate：新增/改变 protocol/schema/state/security/依赖/验收、进入排除范围或命中他人 dirty owner 时立即停止，返回监督者/用户决策。

## 可追溯性

| 目标/验收 | 设计/任务书来源 | 任务 | 验证 |
|---|---|---|---|
| layout 首 GET/PUT roundtrip 与未知字段 | 设计 W2 细化“持久化”；T1 Go | T10,T20,T42 | `TestWorkbenchLayoutRoundTrip`；API smoke |
| version conflict、64 KiB、非法 JSON | 设计乐观并发；T1 Go | T10,T20,T42 | `TestWorkbenchLayoutVersionConflict`；409/413/400 smoke |
| job caller read-only 与 caller 隔离 | 设计 per caller；T1 Go | T10,T20 | `TestWorkbenchLayoutJobCallerReadOnly` |
| split/close/focus/ratio/4-pane/8-tab/normalize | 设计布局树；T1 Web | T10,T30 | `pnpm test` 的 layoutTree 固定场景 |
| tabs、recursive pane、splitter、刷新恢复、800ms save | T2 frontend 1–3；设计布局 | T31 | Web test/typecheck/build；API/browser smoke |
| 侧栏 click/center-edge drop | T2 frontend 4；设计鼠标 | T32 | layout tree tests；browser smoke |
| ctrl+b prefix 与 palette 五命令 | T2 frontend 5；设计键盘 | T32 | xterm/页面 keyboard browser smoke |
| 非焦点 terminal/log 与 2s seen | T2 frontend 6–7；设计已看 | T32 | timer/focus checks；browser/network evidence |
| `<768px` focused pane、两屏滑动、“＋”、attention | T2 frontend 8；设计手机第一条 | T33 | 767/768 browser viewport evidence |
| 文档、全门、隔离、无 live 副作用 | T3 与验证矩阵 | T40,T41,T42,T43 | validators、原始命令输出、PID/config/port/Git evidence |

## 完成 Gate 与剩余工作

W2a 只有在 T10–T43 全部达到各自完成标准、固定 Go/Web tests 与任务书全验证矩阵有最终结果、API smoke PASS、browser 为有证据 PASS 或如实 `NOT_RUN`、所有 owned 变更已按功能点本地提交、Beads 状态同步、`git status --short` 无未归属变更且确认未 push 时，才能报告 source/runtime 实施边界。局部单测、build、health、页面加载或调度成功都不能替代该 Gate；browser `NOT_RUN` 时必须明确“视觉验收未证明”。

G032 预期清单：

- 新增无标记兼容分支：`NONE`。
- 新增 `// DEPRECATED(vX): remove in vY`：`NONE`。
- 删除的旧路径：`web/src/components/workbench/WorkbenchThreadPane.vue`（由 `ThreadView.vue` 直接替代，无 consumer 后删除，不留 wrapper）。
- additive schema：仅 `workbench_layouts`，它是 W2a 当前状态真源，不是兼容层。
- 允许的 forward-compatible 行为只有 backend opaque JSON 未知字段 roundtrip；不增加旧 route、旧 server fallback、localStorage layout 真源或永久 alias。实施若发现确需兼容路径，必须先停并按 G032 记录引入/移除版本。

明确留待后续 job：PWA manifest/maskable icon、Service Worker、Web Push 的 VAPID/订阅/触发/动作令牌与降级；以及 W3、W4。它们不因 W2a 完成、设计已批准或 `DIRECT_CONTINUOUS` 而获得实施授权。
