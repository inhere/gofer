<!-- template_id: plan; template_version: 1.2.0 -->
# web 工作台 W2b PWA 与 Web Push 实施计划

> 状态：Draft 0.1 / 待监督者人工计划批准

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-26 | Codex | 初稿：把 Approved 0.3 的 W2b PWA、Web Push、通知内审批、通知设置页、工作台深链与降级收敛为测试先行的连续实施候选 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 规划声明

- `thinking_mode=RIGOROUS`
- `core_objective=在已上线的 W2a 工作台上交付可安装但不离线缓存的 PWA、按 caller 隔离的 Web Push、可一次性安全作答的通知动作，以及 HTTPS 或浏览器能力不足时仍可工作的页内注意力提醒`
- `allowed_scope=Approved 0.3“W2 细化”中的“手机与 PWA”第二条、“Web Push”全节；本任务书 T1/T2、验证矩阵、文档与隔离 smoke；为复用同一 thread/F16 口径所需的最小 workbench/job owner extension`
- `non_goals=W2a 布局重做，W3 ACP 结构化对话与 diff 评审，W4 worktree/看板/预览，离线缓存，per-project caller ACL，新通知供应商，第三方 Go 依赖，live gofer/server/worker/config，push、发布与部署`
- `expansion_policy=DEFER_OR_REQUEST`
- `delivery_track=Full`：新增外部 HTTP Interface、SQLite Schema、持久化浏览器订阅、安全令牌、出站加密协议与 Service Worker，并跨 Go/Vue/浏览器边界。候选不操作生产数据，不触碰真实配置，表为 additive，隔离运行可重建；治理暴露度为低，一轮合并 Standards+Spec 评审封顶。
- `review_budget=一轮 discovery、一轮 confirmation；只有 CORE_BLOCKING、Semantic Amendment 或 Ownership Conflict 才修订候选并重审`
- `stop_condition=计划文档 validator PASS、精确本地提交并生成 candidate identity 后立即停止；未获监督者计划批准与本会话新的当前执行请求前不进入 W2b-T10`

## 目标与完成定义

目标是在现有 `/workbench` 上完成 W2b：浏览器可安装 gofer PWA；用户能在“设置 → 通知”启用、关闭、查看和测试本设备推送；服务端以标准库实现 RFC 8291 `aes128gcm` 与 RFC 8292 VAPID，通过既有 job 事件总线异步推送 blocked/review 事件；permission 通知可用十分钟一次性动作令牌直接“允许/拒绝”；不支持 Push 的环境仍以标题计数与顶部提示条呈现“等你”。

完成不是“manifest 能下载”、某个加密单测通过、推送服务返回 2xx 或页面显示一个开关。只有下列结果全部成立才算 W2b source/runtime 完成：

1. 任务书固定的六个 Go 测试名全部先 RED、单独提交，随后 GREEN；不得用 `t.Skip`、占位实现、只测 mock 返回值或绕开真实 HTTP/crypto/store seam。
2. RFC 8291 附录 A 输入与完整 `aes128gcm` 记录逐字节一致；VAPID ES256 的 64 字节 raw 签名能由公钥验证，`aud` 是 endpoint origin，`exp` 不超过 24 小时，`sub` 支持配置且默认 `mailto:gofer@localhost`。
3. `push_subscriptions` 对 endpoint 全局唯一、按 caller 列表/删除；成功投递更新 `last_ok_at`，HTTP 404/410 删除订阅；job/worker caller 不能读取或修改 push 管理面。
4. `interaction.created` 只向当前可见该项目的 user caller 发送；thread id 使用 `internal/workbench` 的同一映射；同 caller/thread 30 秒内合并；dispatch 异步且任何加密/网络/store 失败都不影响 job 事件主流程。
5. permission 动作令牌以 HMAC-SHA256 绑定 caller/job/interaction/允许选项/expiry/nonce；成功答案落 `answered_by=push`；重放与过期 409、越权选项 400、篡改 401、interaction 已答 409；并发请求至多一个成功。
6. Service Worker 只有 `push`、`notificationclick`、`notificationclose` 三类 listener，没有 `fetch`/install cache；动作失败或 409 打开工作台，通知主体聚焦或打开 `/workbench?thread=<id>`，Workbench 能定位到该 thread。
7. 通知设置页能给出 `!isSecureContext`、无 Service Worker、无 `PushManager`、permission denied 的明确原因；启用/关闭本设备、caller 订阅列表和测试推送可观察；工作台始终以 `(N) gofer` 与顶部条提供降级注意力。
8. Go/Web 全质量门、临时 HTTP server + 假推送端到端解密/action smoke、文档 validator、控制字符/CRLF/BOM 扫描与 Git 边界检查都有 exit code 和原始输出；真实浏览器若不可用则明确 `NOT_RUN` 与未证明范围，不伪造 OS 通知验收。
9. 测试、store、crypto、动作令牌、事件/HTTP、PWA、设置页、工作台降级和文档按功能点独立本地提交；最终不 push，不重启/reload live gofer，不读取或修改 `D:\work\inhere\config\win-env\gofer`。

## 范围、排除项与授权

范围包括：

- 新包 `internal/webpush`：VAPID P-256 密钥生成/原子加载、RFC 8291 加密、RFC 8292 JWT、HTTP sender、动作令牌、事件投影、30 秒合并和异步 fan-out；新代码只使用 Go 标准库与仓内包。
- `<config-dir>/push/vapid.json`：首次真正需要 public key/发送时惰性生成；同目录临时文件 + fsync + rename，文件 `0600`，目录私有；Windows 沿用现有 secret/cache 写法。VAPID subject 从 `server.push.vapid_subject` 读取，空值用默认值。
- additive 表 `push_subscriptions(caller_id, endpoint UNIQUE, p256dh, auth, user_agent, created_at, last_ok_at)` 及 caller 索引；不新增第二数据库、缓存真源或外部队列。
- `GET /v1/push/vapid-public-key`、`GET/POST/DELETE /v1/push/subscriptions`、`POST /v1/push/test`；这些管理接口在现有 authenticated `/v1` group 内且仅 user caller 可用。
- `POST /v1/push/actions`：挂在 auth group 外，和 schedule trigger/attach 一样由 handler 自证凭据；不接受 bearer 身份替代动作令牌。
- `interaction.created`、`job.needs_review`、按 F16 判定为 review 的 `job.terminal`、`plan.blocked`；payload 只含标题、最多 120 rune 摘要、thread id、同源跳转 URL、tag 与必要动作数据，不含日志正文、prompt 全文、token 日志或 secret。
- `web/public/sw.js`、manifest 补齐、主入口注册；设置二级菜单“通知”；Workbench query 定位、标题计数和顶部提示条。
- `README.md`、新建 `docs/runbook/web-push.md`、Approved 0.3 设计的 W2b 实测记录，以及本计划的非语义 progress。

明确排除：

- 任何 fetch handler、离线缓存、precache、background sync、Web Push 第三方库、前端全局状态库、第二事件总线或持久化消息队列。
- 新增 per-project caller ACL 或改变现有 Web/API 可见性。当前代码语义是：所有已配置 user caller 可读取所有当前配置项目；W2b 只过滤非 user/stale caller 与当前 registry 中不存在的项目。若监督者要求真正的 caller→project ACL，属于配置/授权协议的 Semantic Amendment，必须停止回设计/计划 Gate。
- 向 job caller 开放订阅/测试；把操作员 bearer 放进 push payload；用 VAPID 私钥持久化动作 token；让未用动作 token 跨 server restart 继续有效。
- 改写 interaction 普通 human/agent/auto 归因、answer guard 或 ACP option 语义；W2b 只新增精确来源 `push`，仍调用 job owner 的一次性 pending→answered 状态机。
- W2a split/tab/mobile 布局重构，W3/W4，IM webhook 行为变更，真实浏览器推送供应商注册，真实公网流量、真实配置、数据清理、部署、release、tag、pull/rebase 或 push。
- 修复 `internal/worker.TestPolicyCacheRoundTrip` 的 Windows `0600` 已知基线，或清理既有八个 gofmt 不干净文件；只要求本任务改过的 Go 文件 gofmt 干净。

当前请求只授权生成、验证并本地提交本计划候选；不授权实现。候选获批后仍须由本会话收到新的当前执行请求。执行方式已选择 `DIRECT_CONTINUOUS`，但该选择只决定获批后的连续推进方式，不越过本次 Plan Gate。

- `host_or_non_offline_action=REQUIRED`

该值只覆盖获批实施中的本机临时二进制、`t.TempDir()` 配置/DB、随机 loopback HTTP server、`httptest` 假推送端和条件允许时的隔离浏览器目视；不覆盖 live/remote/真实配置、外部 Push provider、push/release/deploy 或任何出机器动作。

## 输入与批准证据

- 唯一设计依据：[2026-09-26-web-workbench-design.md](../design/2026-09-26-web-workbench-design.md)，状态 `Approved`、identity `0.3`；当前 HEAD `f98e089` 已记录 W2a 上线 `v0.63.0`，本计划只消费“W2 细化”的 PWA 与 Web Push。
- 当前任务书明确把本 job 命名为 `WEB-11 W2b`，逐项指定六个 Go 测试名、实现边界、验证矩阵、临时 server 解密/action smoke 与 `DIRECT_CONTINUOUS`，并要求候选提交后停止等待监督者审批。
- 计划批准可由用户已授权的 gofer 监督者 Claude 代为给出；设计已批准、validator PASS、candidate commit 或执行方式选择都不自动构成计划批准或实施授权。
- 跟踪项：tools Beads `tools-eql`（`[gofer][WEB-11] W2b PWA + Web Push 实施计划候选`）；tools DB 从 workspace root 以 `bd -C tools ...` 访问。

workspace baseline（2026-09-26 规划时）：

- Git root：`D:/work/inhere/hyy-ai-inspect/tools/gofer`；branch=`main`；HEAD=`f98e089d1cef79c96d794c2f61c66d9aedb974c7`（tag `v0.63.0`）；`git status --short` 与 `git diff --stat` 无输出。
- 工具：`go version go1.25.10 windows/amd64`、Node `v24.15.0`、pnpm `12.5.1`；Web 已有 Vitest 与 `pnpm test=vitest run`，W2b 不增加 Go module 或 pnpm dependency。
- Codebase Memory 项目=`gofer`、Verify Tier 2、generation=`2026-09-05T06:59:33Z`，索引 HEAD metadata 为当前 `f98e089`。覆盖检查的 37 条候选路径多数为 `metadata_changed`、`not_tracked`、`missing` 或 docs/public excluded；`web/src/api/types.ts:1-989` 为 `parse_partial`。所有 material claim 已从当前 HEAD 精确源文件回读；图的空结果不用于负面或完整性结论。
- 现有事件链：`job.Service.recordEvent → enqueueDeliveries → AddEventObserver subscribers`。observer 在事件持久化后、job terminal snapshot 可见前同步调用，因此 W2b observer 必须只做无阻塞入队；worker 使用 event detail 的最终 status，并从 job/store 补足 project/session/diff。
- 现有 thread/F16 真源：`internal/workbench.projectThreads/projectJobThread` 以 session 优先生成 `s:<session_id>`、否则 `j:<job_id>`；成功终态仅在未看、非 exec 且 commits/diff 非空时为 review，failed/timeout/rejected 未看时为 review，cancelled 为 done。W2b 必须抽取并复用该纯判定，不复制 switch。
- 现有 auth 可见性没有 caller→project ACL：authenticated user caller 全局读项目/工作台；worker/job 是不同 caller kind。W2b production visibility 定义为“当前可认证的 user caller + 当前 registry 中存在的项目”；测试通过注入 visibility seam 固定 false 分支，不借本任务新增 ACL。
- 现有 `rateLimitMiddleware` 只精确覆盖 `POST /v1/jobs` 与 `POST /v1/workflows`，依赖 auth caller，不能应用到无 bearer 的 `/v1/push/actions`。动作面由 256-bit HMAC key、不可预测 nonce、十分钟 expiry、常量时间验签和原子一次性 claim 自证；新增通用匿名限流器不在本计划范围。
- 现有 Web：manifest 缺 `start_url/scope/id/maskable`，没有 `sw.js` 或注册；设置外壳支持增加一条 section+route；Workbench 尚未消费 `?thread=`，页面 title 固定 `Gofer`，attention 只有现有组件。

预期 owner 路径：

- 测试合同：新建/扩展 `internal/webpush/*_test.go`、`internal/httpapi/push_test.go`、`internal/jobstore/push_test.go`、`internal/job/interaction_test.go`、`internal/workbench/service_test.go`。
- 配置/secret：`internal/config/model.go`、`internal/config/loader.go`、对应 config tests、`config/gofer.example.yaml`；新建 `internal/webpush/vapid.go`。
- store/domain：`internal/jobstore/store.go`、新建 `internal/jobstore/push.go`；`internal/workbench/service.go`；`internal/job/interaction.go`。
- Web Push core：新建 `internal/webpush/encrypt.go`、`vapid.go`、`sender.go`、`action.go`、`dispatch.go`、`service.go`；文件可在该目录按职责合并，但不得把业务逻辑移入 HTTP/serve。
- HTTP/assembly：新建 `internal/httpapi/push_handler.go`；修改 `internal/httpapi/server.go`、`jobcredential.go`、`internal/serve/serve.go`。
- PWA/Web：新建 `web/public/sw.js`、`web/src/api/push.ts`、`web/src/utils/webPush.ts`、`web/src/views/settings/Notifications.vue`；修改 `web/public/site.webmanifest`、`web/src/main.ts`、`web/src/api/types.ts`、`web/src/router.ts`、`web/src/views/settings/SettingsLayout.vue`、`web/src/views/Workbench.vue`。
- 文档：`README.md`、新建 `docs/runbook/web-push.md`、`docs/design/2026-09-26-web-workbench-design.md`、本计划的 progress。

这些 expected paths 是 planning evidence，不是封闭白名单。实施发现新 path/symbol 时，在下一次 mutation 前按 Operational Discovery / Corrective / Semantic Amendment / Ownership Conflict 分类；不得通过扩大 staging、hunk staging 或重算 baseline 绕过 owner 冲突。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | caller 订阅持久化、成功时间与失效删除 | `jobstore.schemaStmts`、workbench prefs/layout、event deliveries | 复用同一 SQLite DB、`writeMu`、additive DDL、unix 秒与 fresh/old DB test seam | 新建 neutral push record 与 CRUD/list/mark-ok/delete-invalid methods | OWNER_EXTENSION | 现有 webhook delivery 是 server target 队列，不持有浏览器 p256dh/auth，也不是 per-caller device subscription | 不建第二 DB；endpoint 唯一避免重复 fan-out；表删除生命周期由 Web Push owner 独占 |
| CAP-02 | RFC 8291 加密、RFC 8292 VAPID、发送与 key lifecycle | `crypto/ecdh`、`crypto/ecdsa`、`crypto/hkdf`、`crypto/aes`、`crypto/cipher`、worker policy cache atomic writer | 标准库覆盖全部 crypto/HTTP；复用现有 temp+chmod+sync+rename secret 写法 | 新建单一 `internal/webpush`，以可注入 clock/rand/client 形成 deterministic vector tests | MINIMAL_NEW_MODULE | 仓内无 Web Push content coding、VAPID 或浏览器 subscription owner；塞入 `notify` 会混淆 IM webhook 与 RFC Web Push | 模块唯一拥有协议、token、dispatch；不引依赖；删除时先移除 HTTP/Web consumer，再删模块 |
| CAP-03 | 一次性通知动作令牌与 push answer 归因 | interaction pending→answered 状态机、`AnswerInteractionByHuman/Auto`、现有 error sentinels | 直接复用 job owner 的锁、persist、event 与 waiter wakeup | webpush 只签发/验证/claim nonce；job 增 `AnswerInteractionByPush` 精确 stamp `push` | OWNER_EXTENSION | 现有 bearer/human path不能放进 SW，普通 responder 会被记为 caller/agent 而非 push | 不复制 interaction 状态机；ephemeral HMAC key/nonce 不落 DB，restart 自动失效 |
| CAP-04 | 事件触发、thread 映射、F16 review 与 project visibility | `AddEventObserver`、`workbench.projectJobThread`、project registry、current user caller set | 复用事件出口、job/plan/store reads 与 workbench thread/F16 真源 | 抽取 `JobThreadID` 和 terminal-review 纯函数；serve 注入 current-user/project visibility predicate | OWNER_EXTENSION | 当前 workbench helpers 未导出；直接在 dispatcher 重写会漂移，HTTP caller 列表不是业务包应解析的 config secret | 不新增 ACL/事件总线；visibility seam 只表达现有模型，ACL 变化必须回 Gate |
| CAP-05 | 异步、30 秒合并和多订阅 fan-out | job observer 的 nonblocking 契约、notify best-effort 日志、标准 channel/timer | observer 只入 bounded queue；标准 HTTP client/ticker 足够 | 每 caller/thread immediate-first dedupe 30s，payload `tag` 与 RFC8030 `Topic` 同源 | OWNER_EXTENSION | notify webhook queue 无 p256dh per-subscription encryption，也不能携带动作 token | 队列满/失败仅 warn；不持久化 transient push；不会阻塞 job 主流程 |
| CAP-06 | HTTP 管理面与 public action 路由 | rux group、auth caller kind、SEC-01 default deny、schedule trigger public pattern、error envelope | 复用 bind/status/user gate 与 public self-auth route pattern | `SetWebPush` 窄 interface；管理面在 auth group，action 在外部；friendly job action map | OWNER_EXTENSION | 当前无 push route；现有 submit limiter不适用匿名 action | handler 只绑定/验证/转发，业务在 webpush；不增加无标记兼容 route |
| CAP-07 | PWA manifest、Service Worker 与通知点击 | Vite public copy、Vue main bootstrap、native Service Worker/Notifications API | 浏览器原生 API 与现有 icons 直接复用 | 新 `sw.js` 只注册三类事件；manifest 补 stable id/scope/start/maskable；main best-effort register | OWNER_EXTENSION | 当前 manifest 不可完整安装且没有 worker | 明确无 fetch/cache，避免旧 chunk 生命周期；注册失败不阻断 SPA |
| CAP-08 | 通知设置页与设备 lifecycle | settings layout/route、API `request`、native `PushManager` | 复用二级菜单、auth client、现有 error/loading UI | 新 page/helper 负责 capability reason、base64url key、subscribe/unsubscribe/list/test | OWNER_EXTENSION | 当前无设备订阅 UI | 不引 Pinia/第三方 PWA 插件；server subscription 是跨设备真源，browser subscription 是本设备事实 |
| CAP-09 | 工作台深链与无 Push 降级 | W2a `Workbench.vue` attention poll、layout focus/assignThread、Vue Router | 复用现有 thread map、focus/layout mutation、attention 数据 | 消费 `route.query.thread`；title/banner 仅从 current attention count 派生 | OWNER_EXTENSION | 当前 notification click 无法定位 thread，title 无计数 | 不重做布局、不新建 attention store；卸载时恢复基础 title |
| CAP-10 | 可重复 E2E 加密/action smoke | `httptest.NewServer`、`t.TempDir()`、现有 httpapi test server/fake runner | 真实 router/store/job/event/HTTP 都可进程内组装；假 push endpoint 可持有 receiver private key | 新 fixed smoke test订阅→interaction→decrypt→action→replay | DIRECT_REUSE | 无工具缺口 | 不新增 helper daemon/外部 Push provider，不碰 live port/config |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| MOD-01 | CAP-02 | `internal/webpush` | `internal/notify`、`internal/job` event observer、`internal/httpapi`、`internal/core/serve`、标准库 crypto | `notify` 只渲染/投递普通 webhook，既无浏览器密钥也无 RFC 8291 content coding；job 只拥有事件/interaction | 把 crypto/token/dispatch 加进 notify 会让 IM webhook owner 承担 per-device secret 与 action state；加进 httpapi/serve 违反 G021 | T1 的 RFC vector/JWT/action/dispatch tests 与 T2 明确要求一个新包；同一 owner 才能避免 key/token/sender 规则分散 | 唯一 owner 为 `internal/webpush`；serve 只组装、httpapi 只绑定、jobstore 只存中性行；生命周期随 Web Push 功能 | 回滚先撤 Web/settings/SW，再撤 HTTP/observer，保留 inert additive table，最后删除 module；不得把协议复制回 notify/handler |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 第三方 Web Push/VAPID Go 库 | CAP-02 | 删除后 Go 1.25 标准库已覆盖 P-256 ECDH/ECDSA、HKDF、AES-GCM、HTTP；任务书明确禁止第三方依赖 |
| Workbox / Vite PWA plugin / 前端 Push SDK | CAP-07 | 删除后一个无 fetch 的静态 `sw.js`、manifest 与原生注册即可完成；插件会引入本任务明确排除的缓存/lifecycle |
| 新持久化 push queue / retry state machine | CAP-05 | 删除后已有事件流 + 进程内 best-effort queue 满足“失败只记日志”；持久重试会改变通知可靠性语义并扩大 Schema |
| 新匿名通用 rate limiter | CAP-06 | 现有限流仅面向已认证 submit，不能直接复用；256-bit token+expiry+nonce 已关闭动作面，新增全局 IP limiter 属于安全协议扩围 |
| caller→project ACL Schema | CAP-04 | 当前所有 user caller 可读所有配置项目；删除该候选保持既有授权语义，“不可见”由 non-user/stale caller、missing project 与注入 seam 覆盖 |
| 本地缓存 subscription 真源 | CAP-08 | `PushManager.getSubscription()` 只识别本设备，server 表负责 caller 全设备；localStorage 会制造第二真源和清理漂移 |

## 前置检查与 fail-closed 条件

1. T00 必须核验 approved candidate tuple、监督者批准原话、当前执行请求和 `DIRECT_CONTINUOUS`；缺任一项不进入实现。
2. 每个 mutating wave 前重新执行 IDEV-STD fingerprint Gate；task/mode/规则身份变化、dirty standards、payload/hash/provenance 异常立即停止。
3. Git 操作固定在 `tools/gofer`；每次 mutation 前 `git status --short`。命中本计划 owner 的既有 dirty/untracked 文件视为 Ownership Conflict，不 hunk-stage、不覆盖；外层 workspace/Beads/`tmp/idev-std` 不纳入 gofer commit。
4. 所有 tests/config/db/key/smoke 文件用 `t.TempDir()` 或 repo `tmp/web11-w2b/<run-id>/`；禁止读取/写入 `D:\work\inhere\config\win-env\gofer`，禁止 restart/reload live server/worker。
5. 实施前确认 Go 仍提供 `crypto/hkdf`；若 API/行为不足以逐字节实现 RFC vector，停止报告事实，不引第三方依赖。
6. `server.push.vapid_subject` 只接受合法 `mailto:` 或 `https:` URI；空值默认。若产品需要 UI 编辑 subject、热 reload 或独立 key rotation API，属于 Semantic Amendment。
7. subscription API 生产只接受 HTTPS endpoint；仅 unit/smoke 构造可显式允许 loopback HTTP `httptest`，该 test-only option 不进入配置/HTTP wire。
8. action HMAC key每进程随机 32 bytes且不落盘；nonce 必须在调用 job answer 前原子 claim。若实现不能证明并发最多一个成功，不得进入 Web/SW wave。
9. production visibility 只按当前 auth 事实实现：current user caller + registry project。不得把 job `caller_id` 擅自解释为独占可见性，也不得通过测试 fake 宣称已有 per-project ACL。
10. `job.terminal`/`job.needs_review` 在 final job snapshot 之前发事件；dispatcher 不改事件顺序，只在异步 worker 用 event detail 的决定态和当前 job metadata。若无法取得 project/thread/F16 输入，记录 warning 并跳过，绝不回压 job。
11. 任何 push payload/日志不得打印 auth secret、VAPID private key、subscription auth、p256dh、动作 token 或完整 prompt；测试失败输出也只打印摘要/哈希。
12. source、测试、临时 runtime、浏览器/OS 通知、部署/发布分别报告。fake push endpoint 2xx 和 decrypt PASS 不是公网 provider 或手机真机验收。

### 锁定的 store 与 HTTP contract

- `push_subscriptions`：`endpoint TEXT PRIMARY KEY`（实现“endpoint UNIQUE”）、`caller_id/p256dh/auth/user_agent TEXT NOT NULL`、`created_at INTEGER NOT NULL`、`last_ok_at INTEGER NOT NULL DEFAULT 0`，另建 `(caller_id, created_at)` 索引。
- POST body：`{endpoint,keys:{p256dh,auth}}`；`user_agent` 由 HTTP `User-Agent` header 盖章。重复 endpoint 原子覆盖 caller/key/user-agent，保留或重新记录 created time 必须在 test 中固定为“本次登记时间”，并清零旧 `last_ok_at`，避免把换 key 当作仍健康。
- DELETE body：`{endpoint}`，仅删除当前 caller 的行；别的 caller endpoint 返回 404，不泄漏所有权。GET 返回 `{subscriptions:[{endpoint,user_agent,created_at,last_ok_at}]}`。
- public key 返回 `{public_key:"<base64url-unpadded 65-byte uncompressed P-256>"}`；test 返回 HTTP 202 与 `{queued:<subscription-count>}`，无订阅返回 409。
- 管理接口只允许 user caller；job/worker 对 GET 与 write 均 403。`/v1/push/actions` 不进入 auth group，body 固定 `{token,option}`。
- POST action：签名/格式失败 401；合法 token 但过期、已用、未知/终态/已答 interaction 为 409；option 不在 token allowlist 为 400；成功 200 且 interaction `answered_by=push`；内部故障 500。响应不回显 token。

### 锁定的 crypto 与 delivery contract

- VAPID key JSON 只保存 raw URL-safe base64 private scalar与 uncompressed public key；加载时验证 P-256 长度、public 与 private 一致。生成写临时文件、`Chmod(0600)`、`Sync`、close、same-dir rename；内存 mutex/once 防并发生成。
- VAPID JWT header 固定 `alg=ES256,typ=JWT`；`aud=url.Scheme+"://"+url.Host`，`exp=now+12h`（严格大于 now 且不超过 24h），`sub=server.push.vapid_subject` 或默认；签名编码为 32-byte r + 32-byte s。
- RFC 8291 使用每条 subscription/message 新 salt 与 ephemeral ECDH P-256 key；按 RFC 8291/8188 exact info 与 padding/end delimiter 生成 `aes128gcm` body。测试注入 Appendix A sender key/salt/auth/receiver key并比对完整 record。
- sender 默认 `http.Client{Timeout:10s}`；headers 至少为 `Content-Encoding:aes128gcm`、`Content-Type:application/octet-stream`、`TTL:3600`、VAPID Authorization、合法 `Topic`；blocked/permission/plan.blocked 为 `Urgency: high`，review 为 normal。
- 任何 2xx 视为成功并更新 `last_ok_at`；404/410 删除 endpoint；其他状态、网络、crypto/store 错误只记录脱敏 warning。

### 锁定的事件、合并与动作 contract

- observer callback 只将 `{scope,event_type,detail}` 非阻塞写入 bounded queue；满时 warning+drop。worker/context/ticker 属于 `internal/webpush`，serve 在启动期注册、退出时 cancel；不 close 仍可能被 observer 发送的 channel。
- supported events：`interaction.created`、`job.needs_review`、`job.terminal`、`plan.blocked`；其他事件忽略。
- `workbench.JobThreadID(jobID,sessionID)` 是唯一映射；`plan.blocked` 从 detail 的 `job` 解析失败 job 后走同一 helper。URL 固定 `/workbench?thread=<url-escaped-id>`。
- F16 抽为 workbench pure helper：needs_review 恒 review；done 仅未看、非 exec、有 commits/diff 时 review；failed/timeout/rejected 未看时 review；cancelled 不 review。workbench projection 与 dispatcher 都调用该 helper。
- visibility predicate 接收 `callerID,projectKey`。production 仅当前认证 user caller + 当前 registry project 返回 true；每 caller 查询自己的 seen baseline/pref 后决定 terminal review。
- 同 caller/thread 第一次立即发送；之后 30 秒内事件不重复 fan-out，notification `tag=threadID`，RFC Topic 为 thread hash。key 为 caller+thread；过期 entry 周期清理。此“immediate-first + window suppression”是本期“合并”的唯一语义，不延迟第一条 blocked 通知。
- permission payload只提供两项 OS action：优先 `allow_once` 与 `reject_once`，缺失时分别取第一个 `allow_*`/`reject_*`；只有实际存在的一侧显示。token options 只包含被展示的真实 option value。
- token 格式为 `base64url(canonical-json).base64url(HMAC-SHA256)`，payload 固定 caller/job/interaction/options/exp/nonce；CSPRNG nonce 128 bit、expiry 10 分钟。先常量时间验签，再检查 expiry/options，再在锁内 claim nonce，最后调用 `AnswerInteractionByPush`。

### 锁定的 PWA 与 Web contract

- manifest：`start_url="/workbench"`、`scope="/"`、`id="/"`；现有 192/512 PNG 增 `purpose:"any maskable"`。不新增图片生成或外部资产。
- `main.ts` best-effort `navigator.serviceWorker.register('/sw.js',{scope:'/'})`；失败只 warning，不阻止 Vue mount。
- `sw.js` 恰有 push/click/close listener。push JSON 投影到 Notification；action click POST same-origin action API；非 2xx/exception（含 409）聚焦或打开 payload URL；主体点击优先 navigate+focus 已有同源 window，否则 `clients.openWindow`。
- 通知设置页能力判定顺序：secure context → Service Worker → Notification API → PushManager → permission；明确展示首个原因。启用用 server public key + `pushManager.subscribe({userVisibleOnly:true,applicationServerKey})` 后 POST；关闭先 server DELETE 当前 endpoint，再 browser `unsubscribe()`；列表以 endpoint 与本地 subscription 比对“本设备”。
- Workbench 初次和 route query 变化时消费 `thread`：thread 已存在则用 W2a `assignThread/focus` 定位；尚未出现则保留 query，后续 poll 命中时定位并给出提示。不得创建第二布局状态。
- attention count N>0 时 title=`(N) gofer`，否则 `gofer`；顶部 fallback 条点击打开现有 attention UI/聚焦第一项。该降级不依赖 push permission，离开 Workbench 时恢复基础 title。

## 波次与依赖

| 波次 | 内容 | 依赖 | 预期提交 |
|---|---|---|---|
| W2b-W0 | T00 candidate、批准、执行与 baseline 复核 | 本候选获批 + 新的当前执行请求 | 本计划提交为 `docs(web-11): add W2b push plan`；无代码提交 |
| W2b-W1 | T10 六个固定测试 + E2E smoke contract 先 RED | W0 | `test(web-11): specify W2b push contracts` |
| W2b-W2 | T20 subscription store；T21 crypto/VAPID/sender | W1 RED | `feat(web-11): persist push subscriptions`；`feat(web-11): add web push cryptography` |
| W2b-W3 | T22 action token + push answer；T23 dispatcher/HTTP/serve | W2 | `feat(web-11): add one-time push actions`；`feat(web-11): dispatch workbench push events` |
| W2b-W4 | T30 manifest/SW；T31 通知设置；T32 Workbench deep-link/fallback | W3 | `feat(web-11): add PWA push worker`；`feat(web-11): add notification settings`；`feat(web-11): add workbench push fallback` |
| W2b-W5 | T40 文档；T41 全门；T42 临时 server/fake endpoint/browser；T43 收口 | W4 | `docs(web-11): record W2b validation`；必要时仅 owner 内 corrective commit |

## 任务

### T00 复核 candidate、baseline 与执行边界

- 文件: 无业务文件；只读设计、本计划、AGENTS、Git/Beads/IDEV-STD 状态与当前 owner source。
- 动作: 核验批准 candidate tuple、监督者批准原话、当前执行请求和 `DIRECT_CONTINUOUS`；检查 nested Git root/branch/HEAD/status、tools Beads、Go/Node/pnpm；重读当前 event/workbench/auth/store/SW owner。确认 live gofer 与真实配置只作为禁止触碰目标，不读取 secret 内容。
- 验证: `git status --short`、`git log -5 --oneline`、`go version`、`node --version`、`pnpm --version`；现有 `go test ./internal/httpapi/ ./internal/jobstore/ ./internal/workbench/ -count=1` 与 `cd web && pnpm test && pnpm typecheck && pnpm build`，记录 exit code 和原始 PASS/FAIL 行。
- 完成标准: approved candidate 与当前执行请求均明确，worktree 无 owner 冲突，既有失败独立分类；否则停止。
- 依赖: 监督者计划批准 + 用户续接本会话实施。

### T10 先提交固定名 RED contract tests

- 文件: 新建 `internal/webpush/*_test.go`、`internal/httpapi/push_test.go`、`internal/jobstore/push_test.go`；扩展 `internal/job/interaction_test.go`、`internal/workbench/service_test.go`；可把完整 fake endpoint decrypt smoke 放在 `internal/httpapi/push_test.go`。
- 动作: 增加且保持精确名字：
  - `TestPushSubscriptionCRUD`：HTTP user CRUD、endpoint overwrite、caller list/delete 隔离、header user-agent、last_ok 初值；store fresh/old DB 与 mark-ok/delete-invalid。
  - `TestPushEncryptRFC8291Vector`：RFC 8291 Appendix A sender/receiver key、auth、salt、plaintext 与完整输出逐 byte。
  - `TestPushVAPIDJWT`：raw ES256 公钥验签、header/claims、endpoint origin audience、12h expiry≤24h、default/custom subject。
  - `TestPushDispatchOnInteraction`：真实 job event observer→异步 sender；visible alice 收到并解密 thread/action，invisible bob 不收；同 caller/thread 30s 一次；404/410 endpoint 被删；httptest 充当 push service。
  - `TestPushActionTokenAnswersOnce`：HTTP public action成功且 `answered_by=push`；并发/顺序重放 409、过期 409、非 allowlist 400、payload/signature 篡改 401、另一个已答 interaction 409。
  - `TestPushJobCallerForbidden`：job token 对 public-key/subscriptions/test 管理面均 403，user 正常；public action 只按 token，不因 job bearer 获权。
- 动作: 增加 `TestPushEndToEndSmoke`（可作为固定测试的 subtest/helper）：临时 httpapi server 订阅 receiver key→制造 permission interaction→假 endpoint 解密出 thread id/token→public action成功→重放 409。所有文件/DB/config 用 `t.TempDir()`，不创建 production stub。
- 验证: `go test ./internal/webpush/ ./internal/httpapi/ ./internal/jobstore/ ./internal/workbench/ ./internal/job/ -run 'Push|Workbench.*Review' -count=1` 必须因缺 package/symbol/route/behavior FAIL；扫描 `rg -n 't\.Skip' <changed-test-files>` 无命中。记录原始 FAIL 行与 exit code。
- 完成标准: 六个固定名测试均被 Go test 发现、断言真实行为，RED 只指向 W2b 缺口；精确暂存测试文件并提交唯一 test commit。
- 依赖: T00。

### T20 实现 subscription store

- 文件: `internal/jobstore/store.go`、新建 `internal/jobstore/push.go`、`push_test.go`。
- 动作: 按锁定 DDL 增加表/索引；实现 caller-scoped upsert/list/delete、dispatcher endpoint delete、2xx `last_ok_at` 与按 eligible callers 读取。输入拒绝空 caller/endpoint/key、负时间；写全部走 `writeMu`，store 不 import webpush/job/httpapi。
- 验证: `go test ./internal/jobstore/ -run 'Push|Open|Migrate' -count=1`；fresh DB 与模拟旧 DB Open 均有表，global endpoint overwrite/caller isolation/mark/delete 明确。
- 完成标准: `TestPushSubscriptionCRUD` 的 store 子路径 GREEN；additive/idempotent，无 destructive migration；独立提交。
- 依赖: T10。

### T21 实现 VAPID、RFC 8291 与 sender

- 文件: 新建 `internal/webpush/vapid.go`、`encrypt.go`、`sender.go`、必要的 package model/errors；`internal/config/model.go`、`loader.go`、config tests、`config/gofer.example.yaml`。
- 动作: 实现锁定 crypto/delivery；key惰性生成/严格加载/原子 0600；新增 `server.push.vapid_subject` 与 default resolver/validation；sender 每 subscription独立加密并设置 VAPID/TTL/Urgency/Topic。只允许 HTTPS，test option 才允许 loopback HTTP。
- 验证: `go test ./internal/webpush/ -run 'EncryptRFC8291Vector|VAPIDJWT|VAPID|Send' -count=1`、`go test ./internal/config/ -run 'Push|Example' -count=1`；`git diff -- go.mod go.sum web/package.json web/pnpm-lock.yaml` 无输出。
- 完成标准: RFC vector/JWT tests GREEN；坏/不匹配 key file fail closed，不覆盖原文件；改过的 Go 文件 gofmt clean；独立提交。
- 依赖: T20。

### T22 实现一次性动作令牌与 push answer 来源

- 文件: 新建 `internal/webpush/action.go`；修改 `internal/job/interaction.go`、`interaction_test.go`。
- 动作: 实现 per-process HMAC key、canonical payload、nonce registry/ticker cleanup、constant-time verify、expiry/options/claim 顺序；job 增最窄 `AnswerInteractionByPush` 与 `answeredByPush="push"`，其余 answer guard/human/agent/auto 路径不变。
- 验证: `go test ./internal/webpush/ ./internal/job/ -run 'PushAction|PushAnswer|Interaction' -count=1`；race-sensitive subtest 同 token 并发请求只一个进入 job answer。
- 完成标准: 固定 action test 的 service/job 部分 GREEN，所有状态与归因可从 persisted interaction/event 观察；token/key/options 不入日志/DB；独立提交。
- 依赖: T21。

### T23 实现事件 dispatcher、HTTP 与 serve wiring

- 文件: 新建 `internal/webpush/dispatch.go`、`service.go`、`internal/httpapi/push_handler.go`；修改 `internal/workbench/service.go/service_test.go`、`internal/httpapi/server.go`、`jobcredential.go`、`push_test.go`、`internal/serve/serve.go`。
- 动作: 抽取/复用 `JobThreadID` 与 F16 terminal review helper；实现 nonblocking queue、visibility、caller seen/pref、30s immediate-first merge、payload/action selection、subscription fan-out/cleanup/test push。serve 组装 service、current user IDs与 project registry predicate，注册 event observer，defer cancel；httpapi 仅持窄 service interface和 user gate。
- 动作: 管理 routes 放 authenticated group；action route放 group 外并只验 token；现有 submit limiter保持原样（无适用匿名 limiter）；SEC-01 不给 job caller加 allowlist，只补明确 action 文案。
- 验证: 六个固定测试全部 GREEN；`go test ./internal/webpush/ ./internal/httpapi/ ./internal/jobstore/ ./internal/workbench/ -count=1`；`go test ./internal/job/ -count=1` 可后台但最终必须等待；事件 handler 注入网络失败/queue full/crypto error 后原 job interaction仍成功创建。
- 完成标准: backend HTTP/store/crypto/action/event contract闭合，G021 handler无业务、G022 无环，observer不做网络/阻塞，404/410删除；独立提交。
- 依赖: T20,T21,T22。

### T30 实现 manifest 与最小 Service Worker

- 文件: `web/public/site.webmanifest`、新建 `web/public/sw.js`、`web/src/main.ts`。
- 动作: 按锁定 contract 补 PWA identity/icons；注册 worker；实现 JSON push、两种 action、主体 click focus/navigate/open 与失败 fallback。禁止 `fetch` listener、CacheStorage、install/activate precache。
- 验证: `cd web && pnpm test && pnpm typecheck && pnpm build`；对源码静态扫描 listener 只含三种且 `rg -n 'fetch|caches|CacheStorage|precache' web/public/sw.js` 无实现命中；构建产物包含 manifest/sw/icons。
- 完成标准: SPA 注册失败不白屏；SW 不拦截任何页面/API/chunk；独立提交。
- 依赖: T23。

### T31 实现通知设置页与 Web API

- 文件: 新建 `web/src/api/push.ts`、`web/src/utils/webPush.ts`、`web/src/views/settings/Notifications.vue`；修改 `web/src/api/types.ts`、`web/src/router.ts`、`web/src/views/settings/SettingsLayout.vue`。
- 动作: 实现 capability reason、VAPID base64url→Uint8Array、subscribe/unsubscribe、server list、当前设备匹配和 test push；permission只在用户点击时请求；错误保留现有状态并给可操作提示。
- 验证: `pnpm test && pnpm typecheck && pnpm build`；mock/浏览器检查 unsupported/denied/subscribed/unsubscribed/no-subscription/test-queued 状态；API Network 只访问新固定 routes。
- 完成标准: “通知”菜单可达；本 caller rows与本设备状态不混淆；关闭后 browser/server均无当前 endpoint；独立提交。
- 依赖: T30。

### T32 实现 Workbench push deep-link 与降级提醒

- 文件: `web/src/views/Workbench.vue`，必要时只扩展既有 WorkbenchAttention presentation，不新建状态 owner。
- 动作: 使用 `useRoute/watch` 消费 `?thread=`，复用 W2a tree helper定位；poll后补定位；attention count驱动 title和顶部 fallback banner，点击复用现有 attention selection。unmount恢复 `gofer`。
- 验证: `pnpm test && pnpm typecheck && pnpm build`；browser/mock route验证已有/稍后出现/未知 thread；N=0/1/多项 title/banner；布局保存、mobile focus、Ctrl+B/W2a tests不回归。
- 完成标准: SW主体/失败 action能稳定到目标 thread；无 Push能力时用户仍看见等你计数与入口；独立提交。
- 依赖: T31。

### T40 更新 README、Web Push runbook、设计实测记录与 progress

- 文件: `README.md`、新建 `docs/runbook/web-push.md`、`docs/design/2026-09-26-web-workbench-design.md`、本计划。
- 动作: README 将 W2b 从“尚未做”改为 PWA/通知设置/动作/降级用法。runbook 用官方 runbook template，记录 HTTPS/localhost 前提、`server.push.vapid_subject`、key路径与权限、设置页/API测试、404/410清理、restart token失效、无 fetch cache及诊断；不打印 private key/auth/token。设计追加“W2b 实测记录”，只写真实 commits/tests/smoke/限制/lifecycle，保持 Approved identity 0.3。
- 验证: links、`validate_document.py --kind runbook`、`--kind design`、`--kind plan`、`git diff --check`、控制字符/CRLF/BOM 扫描。
- 完成标准: 文档区分 source、fake endpoint runtime、browser/OS/mobile、部署；不把 build/2xx 冒充真实手机通知；独立 docs commit。
- 依赖: T42 实际证据；可先起草，但最终数据只在验证结束后填写。

### T41 执行完整代码与文档质量门

- 文件: 全部 owned changed files；原始输出存 `tmp/web11-w2b/<run-id>/`，不提交临时产物。
- 动作: 对改过的 Go 文件 `gofmt -w`，再对同一精确清单 `gofmt -l`；执行 Windows/Linux build、vet、指定四包 tests、改动的 internal/job full test 与 Web test/typecheck/build。Windows 慢包可后台并行，但最终报告前必须等待每个 session结束。
- 验证:
  - `go build ./...`；
  - 显式子进程 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...`，不得污染后续环境；
  - `go vet ./...`；
  - `go test ./internal/webpush/ ./internal/httpapi/ ./internal/jobstore/ ./internal/workbench/ -count=1`；
  - `go test ./internal/job/ -count=1`；
  - `cd web && pnpm test && pnpm typecheck && pnpm build`；
  - 三份 document validators、`git diff --check`；
  - changed text 的 `rg --pcre2 '[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]'`、CRLF 与 UTF-8 BOM scan；
  - `git diff --exit-code -- go.mod go.sum` 与无新增 dependency 证据。
- 完成标准: build/vet/tests/Web/validators/diff-check exit code 0；`gofmt -l` exit 0 且无输出；控制字符 rg 原生 exit 1 且无输出表示无匹配。任何 baseline FAIL 都贴原始行、重跑 focused 对照并如实分类，不顺手修无关失败。
- 依赖: T23,T30,T31,T32,T40。

### T42 执行临时 server 加密/action smoke，并条件执行浏览器目视

- 文件: `internal/httpapi/push_test.go` 的 `TestPushEndToEndSmoke`；`tmp/web11-w2b/<run-id>/` 下命令日志、临时 config/DB/storage、可用时的截图；正式配置零改动。
- 动作: 首先 `go test ./internal/httpapi -run '^TestPushEndToEndSmoke$' -v -count=1` 启动真实 `httptest` httpapi server与假 push endpoint：生成 receiver ECDH/auth→通过 bearer API订阅→制造真实 permission interaction→等待收到 encrypted request→用 receiver private key解密 JSON并断言 thread/url/token→无 bearer调用 action成功→重放409→确认 persisted `answered_by=push`。
- 动作: 若浏览器 provider可用，再用 temp config、随机 loopback端口、构建后的 Web 运行：manifest/installability、SW registration无 fetch、通知设置启停/list/test UI、`?thread=`定位、unsupported模拟、title/banner。每条 gofer CLI显式 `-c <temp>` 或 `--server http://127.0.0.1:<port>`；只停止记录的临时 PID。
- 验证: fake endpoint保存脱敏 request headers/status/cipher hash和解密后的非敏感字段；浏览器关键画面截图/描述。检查 live进程与真实 config未变化。
- 完成标准: 临时 HTTP E2E smoke必须 PASS；browser要么按场景 PASS并有证据，要么在穷尽已授权安全 provider后写 `NOT_RUN`、首个稳定错误与未证明范围。公网 push provider/手机 OS notification不在本地 fake smoke 声称范围内。
- 依赖: T41。

### T43 原子提交、Beads 与最终交付检查

- 文件: 每个功能点 exact owner files、tools Beads `tools-eql` progress；不修改其他 dirty 文件。
- 动作: 每次提交前 `git status --short`，精确 add，核对 `git diff --cached --name-only`，使用 conventional commit；不得 hunk-stage绕过 ownership。最终记录各任务状态/一句说明/hash、原始 gofmt/build/vet/test/Web/控制字符输出、smoke/browser、`git log`、`git status`、G032清单、未完成与人工决策。
- 验证: `git log --oneline <implementation-baseline>..HEAD`、`git status --short`、`bd -C tools show tools-eql`。若有 upstream，只做只读状态说明，不 pull/rebase/push。
- 完成标准: 所有 owned 变更按功能点本地提交，nested worktree无未归属变更，Beads与 Git一致，严格未 push、未部署、未 reload/restart live gofer。
- 依赖: T40,T41,T42。

## 回滚与恢复

- 提交序列保持 RED contract、subscription store、crypto、action、dispatch/HTTP、PWA worker、settings、workbench fallback、docs 九类原子边界。回滚采用针对性 `git revert <commit>` 方案，不使用 `reset --hard`、`checkout --` 或 `clean`。
- Web 回滚按 consumer-first：先撤 Workbench/settings，再撤 SW registration/manifest，随后撤 HTTP/dispatcher/action/crypto/store。不得保留旧 route fallback、双 subscription store 或第二 token verifier。
- 已安装 SW 在代码回滚后可能继续驻留，但它没有 fetch/cache，且 backend不再发送时不会影响页面；需要清理时由浏览器站点设置/DevTools显式 unregister，不能在普通 rollback静默操作用户浏览器。
- Backend回滚不 drop `push_subscriptions`，旧 binary会忽略 additive table，订阅行 inert；未来数据删除/密钥轮换另立授权。source回滚也不删除真实 `push/vapid.json`。
- HMAC key/nonce为进程内状态；server restart后未用 token按设计失效。恢复不尝试复活 token，只让用户打开 Workbench重新作答。
- smoke恢复点为 temp root、随机端口、PID与测试名；只停止精确 PID，目标身份不匹配立即停止，不递归删除广域目录。
- 遇到 BLOCKED/PAUSED/FAILED_TERMINAL 时使用统一 6 行中断卡：状态、已完成、当前/已验证、未完成、首个失败边界/影响、唯一下一步；详细输出只引用 `tmp/web11-w2b/`。

## 人工 Gate

1. Design Gate：Approved identity 0.3 已满足，只覆盖 W2b scope freeze；不授权 W3/W4、ACL、离线缓存或外部 provider。
2. Plan Gate：本文件为 Draft 0.1 candidate，提交后必须停止。用户已授权 gofer监督者代为审批，可使用最小批准语句：“批准该 W2b 计划，按 DIRECT_CONTINUOUS 连续实施”。
3. Execution Gate：计划批准后仍需用户续接本会话形成新的当前执行请求；candidate、review PASS或执行方式选择都不自动开始实现。
4. Host action Gate：计划批准并请求执行后，只允许 T42 具名的本机 temp binary/config/DB/random-port/httptest与隔离浏览器；真实 Push provider、push、release、deploy、live restart/reload、真实 config/data、远端 worker、traffic、外部消息和硬件动作仍未授权。
5. Semantic Amendment/Ownership Conflict Gate：新增 caller→project ACL、改变 action/token expiry/status、持久 push retry、离线 cache、第三方依赖、进入排除范围或命中他人 dirty owner时立即停止，返回监督者/用户决策。

## 可追溯性

| 目标/验收 | 设计/任务书来源 | 任务 | 验证 |
|---|---|---|---|
| subscription CRUD、caller隔离、404/410清理 | Web Push“订阅”；T1/T2-2/3 | T10,T20,T23,T42 | `TestPushSubscriptionCRUD`、`TestPushDispatchOnInteraction`、HTTP smoke |
| RFC 8291 exact vector | Web Push“密钥”；T1/T2-1 | T10,T21 | `TestPushEncryptRFC8291Vector` |
| VAPID ES256/aud/exp/sub | T1/T2-1 | T10,T21 | `TestPushVAPIDJWT`、public-key HTTP |
| interaction event、可见 user、thread合并 | Web Push“触发”；T1/T2-4 | T10,T23 | `TestPushDispatchOnInteraction` |
| F16 review 与 plan.blocked | 设计触发/F16；T2-4 | T23 | shared helper tests、event subtests |
| 一次性动作 token/source=push/status | Web Push“通知内审批”；T1/T2-3 | T10,T22,T23,T42 | `TestPushActionTokenAnswersOnce`、E2E smoke |
| job caller禁止 | Web Push“订阅”；T1 | T10,T23 | `TestPushJobCallerForbidden` |
| TTL/Urgency/Topic/async failure-only-log | T2-1/4 | T21,T23 | fake endpoint headers、queue/network failure tests |
| manifest/maskable/SW无cache | 手机与PWA第二条；T2-5 | T30,T42 | Web build、static scan、browser registration |
| 设置通知启停/list/test与不可用原因 | T2-5/6 | T31,T42 | typecheck/build、browser/mock states |
| 通知主体/失败action定位 thread | T2-5；W2 thread映射 | T30,T32,T42 | SW click test/manual、`?thread=` browser |
| `(N) gofer` 与顶部降级条 | T2-6 | T32,T42 | N=0/1/many browser/mock evidence |
| 文档、质量、隔离与无 live副作用 | T2-7与验证矩阵 | T40,T41,T42,T43 | validators、原始命令输出、temp/PID/config/Git evidence |

## 完成 Gate 与剩余工作

W2b 只有在 T10–T43 全部达到各自完成标准、六个固定名测试与用户验证矩阵有最终结果、RFC vector/JWT/动作并发安全成立、临时 server E2E能解密 thread id并成功/重放 action、所有 owned变更按功能点本地提交、Beads状态同步、`git status --short` 无未归属变更且确认未 push时，才能报告 source/runtime实施边界。局部单测、build、VAPID key生成、push endpoint 2xx、Service Worker注册或页面加载都不能替代该 Gate。

浏览器/OS分层：fake endpoint E2E PASS证明 gofer的 wire crypto、事件、HTTP与动作闭环；只有真实受控浏览器证据才能证明设置页/SW/deep-link UI；只有实际 HTTPS/localhost origin上的 provider+OS证据才能证明设备通知；手机真机与公网部署未执行时必须明确 `NOT_RUN`。

G032 预期清单：

- 新增无标记兼容分支：`NONE`。
- 新增 `// DEPRECATED(vX): remove in vY`：`NONE`。
- 删除的旧路径：`NONE`。
- additive schema：仅 `push_subscriptions`，它是当前 subscription真源，不是兼容层。
- additive config：`server.push.vapid_subject`，空值直接走当前默认；不是旧 key alias。
- 允许的兼容行为只有 manifest/browser API的标准 feature detection；不增加旧 route、旧 server fallback、localStorage subscription真源、Push库双实现或 Service Worker cache。实施若发现确需兼容路径，必须先停并按 G032记录引入/移除版本。

明确留待后续：W3结构化 ACP/评审、W4 worktree/预览、caller→project ACL、durable push retry、VAPID rotation API、真实外部 Push provider/手机验收、push/release/deploy。它们不因 W2b完成、设计已批准或 `DIRECT_CONTINUOUS` 获得实施授权。
