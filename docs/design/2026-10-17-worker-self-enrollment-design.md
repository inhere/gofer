<!-- template_id: design; template_version: 1.1.1 -->
# worker 自助注册（申请 → web 限时接受 → 自动配置） 设计

> 状态：Draft 0.3 / 已批准（0.2 经用户 2026-10-17「按推荐，批准设计；出实施计划」批准，决策 1–6 取推荐值、决定窗口 120 秒；0.3 的落点修正随计划经用户「批准计划，开始实施」确认）
> issue：gofer-l1ry

- 思考模式：rigorous。
- 核心目标：worker 机器上一条命令发起注册，管理员在 web 上限时接受或拒绝，接受后双方配置自动写好，不再手工在两台机器之间抄 token。
- scope freeze：范围以本稿「范围与非目标」为准；实施中发现超出范围的需求另开 issue。
- review budget：一轮独立评审已用（针对 0.1）；0.2 按评审必改项与建议项修订，经作者自查，不再做第二轮全面评审；用户确认前如有疑问可做一次针对必改项的窄复核。
- 停止条件：用户对本稿给出具名的批准，或明确要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-17 | Claude（监督会话） | 初稿：申请对象、四条接口、配对码与一次性领取凭据、权限过滤、`gofer worker join`、默认数值与待决策项 |
| 0.2 | 2026-10-17 | Claude（监督会话） | 按独立评审修订：取消「同名 id 唯一 pending」，改为按来源限额并可顶替自己的旧申请、拒绝时可屏蔽来源；来源地址只认连接地址，新增 `trusted_proxies`；匿名接口限请求体与字段；配对码默认手输、加长并限次；申请行不存 token，只存指纹，领取改两阶段（领取 + 确认），撤销前核对指纹并复用 worker 删除语义；接受用条件更新占位，决定窗口与领取窗口分开，启动对账；登记 / 撤销函数移到 core；路由改为 `/v1/worker-enroll`；推送主题三处接线、Today 卡片在 handler 按权限追加；审计落点、`no-store`、不返回 connect url；`runWorkerInit` 对接细节、Windows、配置段与清理；补文档与测试清单 |
| 0.3 | 2026-10-17 | Claude（监督会话） | 按实施计划评审修正落点：登记 / 撤销改为 `internal/config` 的纯函数，在 `core.Update` 回调里调用（core 已持有 hub，httpapi 只经 `ConfigWriter` 碰 core；意图不变——不留在 httpapi）；路由共七条（四条匿名、三条管理）；推送主题为无负载失效通知；列表响应带 `enabled`；hub 首连兜底经 serve 扇出 presence 观察者、异步处理；`config.updated` 审计由 handler / 业务层的审计接口写；配置字段登记 `fieldPolicies`；功能关闭时清扫仍处理遗留申请 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 背景与目标

现在接入一台 worker 要在两台机器之间来回操作：

1. 在 server 端（或 web 的 Runners 页）执行 `gofer worker add <id> [--project …]`，拿到只显示一次的 worker token；
2. 把 token 抄到 worker 机器，执行 `gofer init worker --server … --id … --token … --yes`。

用户希望反过来：worker 机器配好后执行一条子命令向 server 申请注册，server 的 web 弹出提示，管理员在 2 分钟内接受或拒绝；接受后 server 自动登记、worker 自动写好配置。

用户可见的目标：

- worker 机器上 `gofer worker join --server <addr>` 一条命令完成接入，命令行显示配对码与倒计时。
- 有 `worker.manage` 权限的人在 web 顶栏与 Runners 页看到申请，输入 worker 终端上显示的配对码后接受（可同时定 worker id、项目与标签），或拒绝。
- 接受后 worker 不需要再输入任何东西：token 自动交给发起申请的那条命令，`worker.yaml` 与 `.env` 自动写好，可选直接启动。

## 名词

- **注册申请（enrollment）**：worker 机器发起的一次待批请求。状态：`pending`（等人决定）→ `accepting`（接受处理中，短暂）→ `accepted`（已登记，等 worker 领取）→ `collected`（worker 已确认领取）；或 `rejected` / `expired` / `superseded`（同一来源发起了新申请）/ `cancelled`（申请方放弃）。
- **配对码（pairing code）**：server 生成的 8 位短码，命令行与 web 同时显示；接受时管理员要输入 worker 终端上看到的码，用来识别冒名申请。它不是凭据，不能用来领取 token。
- **领取凭据（enroll secret）**：worker 端为本次申请生成的 32 字节随机值，只在领取、确认、放弃时出示；server 只存其 SHA-256。
- **来源（source）**：发起申请的网络地址。默认取连接地址；只有连接来自配置的可信代理时才按转发头取。
- **token 指纹**：worker token 的 SHA-256，存在申请行上，用来判断配置里的 token 是否仍是这次发出的那个。
- **登记 / 撤销**：现有 `worker add` / `worker rm` 做的配置写入——`server.workers[id]`（token、labels）、`runners[id]`、项目 `allowed_runners`；撤销还会断开该 worker 的连接。

## 范围与非目标

范围：

- 注册申请对象：存储、状态机、过期清扫、启动对账、记录清理。
- 匿名接口三条（发起、领取 / 确认、放弃）与管理接口三条（列表、接受、拒绝）。
- 登记 / 撤销逻辑从 httpapi 抽成 config 纯函数（0.3），`worker add` / `worker rm` 与本功能共用。
- web：顶栏提醒、Runners 页「待接受」区、接受对话框；Today 卡片按权限追加。
- CLI：`gofer worker join`。
- 配置段 `server.worker_enrollment`、按来源限流、审计与权限过滤。

非目标：

- 不改变 worker token 的存储方式（仍是 `config.yaml` 里的 `server.workers[id].token`）；token 哈希化另议。
- 不做无人值守的自动接受（按网段或预共享密钥自动批准）。
- 不改变 worker 连接握手（`/v1/workers/connect` 与 register 帧）与协议版本。
- 不替代 `worker add` / `worker init --token`，两条旧路径保留。
- roots、guards、agents 仍只在 worker 本机配置，不由 server 下发。
- 不为整个 HTTP server 加读写超时（`http.Server` 目前没有超时配置）；本功能只对两条匿名路由做请求级限制，全局超时另开 issue。
- 一台 worker 只对接一台 server（`worker.yaml` 只有一份），与 `worker init` 相同。

## 已确认事实与规范

代码现状（2026-10-17，v0.168.0），经独立评审逐条核对：

- **登记**：`internal/httpapi/worker_registration.go:30-106` 的 `handleRegisterWorker`——`adminUserOnly`（只放行人的凭据）、id 正则 `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` + `config.CheckWorkerID`（拒绝 `server` / `local`）、`newWorkerToken()`（`gw_` + 32 字节随机 hex，明文存进配置）、在 `s.core.Update` 里写 `server.workers[id]`、`runners[id]`、项目 `allowed_runners`（空列表先展开为 `[local]`），重复 id 返回 409，随后 `recordConfigUpdate`。删除在同文件 `:108-159`，会调用 `Hub.DisconnectWorker`。路由声明是 `Require(worker.manage)` 加 handler 里的 `adminUserOnly`。
- **热加载**：`core.Update` 保存 `config.yaml` 后 `reloadLocked`（`internal/core/core.go`）重建 worker runner、`workerSelector.updateAllowed`、`Hub.UpdateBindings`；登记后不需要重启，新 token 立即可用于连接。
- **worker 端配置生成**：`internal/commands/worker_init.go:102-238` 的 `runWorkerInit`——依赖全局 `workerInitOpts`（`--id` 必填）；拿到 token 后用 worker token 调 `GET /v1/workers/{id}/assignable`（worker 凭据允许的路由）、按项目 host_path 推断 roots、探测 agents、写 `worker.yaml`（`token_env: GOFER_WORKER_TOKEN`，token 不进 yaml）与 `.env`（`upsertEnvFile`，0600）、跑 doctor。flag：`--server/-s --token --admin-token --id --roots --project --yes/-y --force/-f --workspace --timeout`。`daemon.Spawn` 在 Windows 不支持（G013）。
- **匿名路由**：真正匿名的路由注册在 `/v1` 鉴权组之外（`server.go:1052-1086`），声明 `NoUserSurface()` + `external`（`authz_routes.go:200-205`，例 `"GET /health"`、schedule trigger）；组外路由拿不到组内的 metrics / 限流中间件。
- **限流**：只有按调用方 id 的令牌桶（`httpapi/ratelimit.go`），挂在 `/v1` 组的中间件链上，只对提交 job / workflow 生效；没有按来源地址的限流，也没有可信代理配置。现有 `clientIP`（`job_handler.go:190-205`）无条件信任 `X-Forwarded-For` 第一跳，不能用于本功能。
- **请求体**：`MaxBytesReader` 只在少数 handler 使用；`http.Server` 没有超时设置（`server.go:1686`、`:1719`）。
- **推送**：`pushhub/hub.go` 的 `globalTopics` 白名单；`wsTopicAllowed`（`authz_live_conn.go:497-511`）的 switch 默认放行；`wsTopicPerms`（`:516-530`）在 subscribe / unsubscribe 时声明所需权限。`pending` 快照不按权限过滤。
- **Today**：`today.Service.Today(q)` 与 `Query` 不带调用方，`handleToday`（`today_handler.go:29-48`）不传调用方；卡片按权限过滤只能在 handler 里做。
- **审计**：`audit_events` 只有 `kind, target_id, actor, at`（`jobstore/audit.go`）；`config.updated` 是 scoped 事件。
- **清理**：`internal/job/prune.go` 只清 job，没有通用表清理任务。
- **connect url**：`workerConnectURL()`（`worker_registration.go:186-195`）取 server 自己的监听地址，远程 worker 不能直接用；`worker init` 用的是用户给的 `--server`。
- **长轮询**：会话回合等待服务端上限 25 秒，客户端默认超时 30 秒，调用方循环请求。
- **权限**：`worker.manage`（RiskDangerous，不可授予会话）。

规范：G021 / G022（入口只转发、依赖单向）、G024（双接口依赖倒置）、G032（additive 迁移、不留兼容层）、G045 / G046（skill references 通用写法）、G051–G054（测试生命周期、计时、隔离、失败归因）、G053（commands 全局 flag 登记）、G056（路由声明与权限文档生成）。

## 总体方案

一次注册分三步：

1. **申请**：worker 机器执行 `gofer worker join --server <addr> [--id <提议 id>] [--labels …]`。命令生成领取凭据，发送凭据哈希与主机信息（hostname、OS / 架构、gofer 版本、协议版本、本机探测到的 agents、显式给出的 `--roots`）。server 建一条 pending 申请，返回申请 id、配对码、决定截止时间；命令行显示配对码与倒计时。
2. **决定**：有 `worker.manage` 的人在 web 顶栏提醒或 Runners 页「待接受」区看到申请，核对来源与主机信息，**输入 worker 终端上显示的配对码**，确定 worker id（默认取提议值，可改）、项目与标签后接受；或拒绝（可选同时屏蔽该来源一段时间）。无人处理到截止时间则过期。
3. **领取**：join 命令一直在长轮询。接受后它凭领取凭据拿到 worker id 与 token，**先写 `.env`**，再走 `worker init` 的现有流程写 `worker.yaml`、跑 doctor，最后发确认；带 `--start` 时启动 worker。拒绝、过期、被顶替时打印原因并以非零退出。

一期完成，不拆分。

## 架构

### 分层

- **登记 / 撤销（0.3）**：从 `handleRegisterWorker` / 删除 handler 抽成 `internal/config` 的纯函数——`RegisterWorkerInto(cfg, id, labels, projects, token) error`、`RemoveWorkerFrom(cfg, id, expectFingerprint) (removed bool, err error)`（给了指纹时先核对 token 指纹，不一致不改）；id 正则、`NewWorkerToken()`、重复错误一并移入 config。handler 与业务层都在 `core.Update` 的回调里调用它们；断开连接由调用方经 hub 完成（httpapi 沿用现有方式，业务层的适配器在 serve 装配时拿到 hub）。`worker add` / `worker rm` 行为不变。
- **业务层** `internal/workerenroll`：申请状态机、限流、配对码、清扫与对账。它通过窄接口依赖登记能力（`Registrar{ Register; Remove; Disconnect }`，由 serve 用 `core.Update` + config 纯函数 + hub 实现）与审计能力（`AuditSink`），不 import httpapi（G022 / G024）。
- **入口层**：httpapi 的 handler 只绑定、校验请求体大小、取来源地址、转发；commands 的 `worker join` 只编排 client 调用与 `runWorkerInit`。

### 数据

新表 `worker_enrollments`（additive 迁移）：

| 列 | 说明 |
|---|---|
| `id` | 申请 id（随机 128 位，`we_` 前缀） |
| `state` | 见「名词」 |
| `secret_hash` | 领取凭据的 SHA-256 |
| `pairing_code` | 配对码 |
| `code_failures` | 接受时配对码输错次数 |
| `secret_failures` | 领取凭据校验失败次数 |
| `requested_id` | 申请时提议的 worker id（只作提示，可空，不要求唯一） |
| `worker_id` | 接受时确定的 id |
| `labels_json` / `projects_json` | 提议的标签；接受时确定的标签与项目 |
| `host_json` | hostname、os、arch、gofer 版本、协议版本、agents、显式 roots（只作展示，已按「字段限制」清洗） |
| `source` | 来源地址 |
| `token_fp` | 接受时发出的 token 指纹（SHA-256）；申请行不存 token 本身 |
| `created_at` / `decide_until` / `collect_until` | 创建时间、决定截止、领取截止 |
| `decided_at` / `decided_by` / `reason` / `collected_at` | 决定与领取记录 |

token 只存在 `config.yaml` 的 `server.workers[id].token`（与 `worker add` 相同）。领取时由业务层从当前配置读取，并核对指纹一致；不一致（例如这段时间里有人删掉又重新添加了同名 worker）则返回失败、不交出 token。

### 接口

| 方法与路径 | 凭据 | 声明 | 说明 |
|---|---|---|---|
| `POST /v1/worker-enroll` | 无 | `NoUserSurface()` + `external: "none"` | 发起申请。body ≤ 8 KiB：`{requested_id?, labels?, host, secret_hash}` → `{enroll_id, pairing_code, decide_until}` |
| `GET /v1/worker-enroll/{id}?wait=N` | 领取凭据（请求头 `X-Gofer-Enroll-Secret`） | `NoUserSurface()` + `external: "enroll secret"` | 长轮询，单次 ≤ 25 秒。返回 `pending` / `accepted{worker_id, worker_token, collect_until}` / `rejected{reason}` / `expired` / `superseded` / `cancelled`。`accepted` 在领取截止前可重复领取（应对响应丢失） |
| `POST /v1/worker-enroll/{id}/confirm` | 领取凭据 | 同上 | worker 已写好 `.env` 后确认，置 `collected` |
| `POST /v1/worker-enroll/{id}/cancel` | 领取凭据 | 同上 | 申请方放弃（Ctrl-C）；pending 置 `cancelled`，accepted 按「过期」撤销 |
| `GET /v1/worker-enrollments` | 用户 | `Require(worker.manage)` + `adminUserOnly` | 未决与最近 24 小时的申请（不含配对码以外的任何秘密：不返回 `secret_hash`、`token_fp`） |
| `POST /v1/worker-enrollments/{id}/accept` | 用户 | `Require(worker.manage)` + `adminUserOnly` | body：`{pairing_code, worker_id, labels?, projects?}` |
| `POST /v1/worker-enrollments/{id}/reject` | 用户 | `Require(worker.manage)` + `adminUserOnly` | body：`{reason?, block_source_min?}` |

- 新路径不放在 `/v1/workers/` 下，避免与 `/v1/workers/{id}/…`（id 可以合法地叫 `enroll`）冲突。
- 四条匿名路由在 `/v1` 鉴权组外注册，自己挂按来源限流与计数；不存在的申请 id 与凭据错误返回同一个 404 与同一条消息，不区分。
- 功能关闭时四条匿名路由返回 404，body `{"error":"not found","detail":"worker enrollment is disabled on this server"}`，join 据此提示「server 未开启自助注册」；管理路由照常可用；列表响应为 `{enabled, pending_count, items}`，web 据 `enabled` 提示如何开启。
- 所有七条路由的响应加 `Cache-Control: no-store`。领取响应不返回 connect url：join 一律用自己的 `--server`。
- job 凭据与 worker 凭据调用管理路由一律 403（`adminUserOnly`），即使被授予了 `worker.manage`。

### 字段限制

匿名请求的每个字段都在入口校验并清洗，超出直接 400：

| 字段 | 限制 |
|---|---|
| 请求体 | ≤ 8 KiB（`http.MaxBytesReader`），单次请求读超时 10 秒（`http.ResponseController.SetReadDeadline`） |
| `requested_id` | 与 worker id 相同的正则与保留名检查 |
| `labels` | ≤ 8 个，每个 ≤ 32 字符，`[A-Za-z0-9._-]` |
| hostname / os / arch / 版本 | 可见 ASCII，各 ≤ 64 字符 |
| agents | ≤ 16 个，每个 ≤ 64 字符，同 labels 字符集 |
| roots | ≤ 16 条，每条 ≤ 256 字符，去掉控制字符 |
| `secret_hash` | 64 位 hex |

web 渲染一律当作纯文本。

### 来源地址与限流

- 默认只用连接地址（`Request.RemoteAddr`），不读任何转发头。
- 新配置 `server.worker_enrollment.trusted_proxies`（CIDR 列表，默认空）：只有连接地址落在其中时才读 `X-Forwarded-For`，并取从右往左第一个不在可信列表里的地址。
- 限流按来源：每来源每分钟发起 ≤ 3 次；每来源同时 ≤ 1 条未决申请——同一来源再次发起时，它自己的旧 pending 申请置为 `superseded`（旧的 join 命令收到后提示「已被新申请取代」），不返回 429；全局未决上限 20 条作兜底，满了返回 429。
- 拒绝时可选「屏蔽该来源 N 分钟」（默认 10 分钟），期间该来源发起申请返回 429。
- 限流与屏蔽表在内存里，容量上限 4096 个来源，按最近使用淘汰并带 TTL，防止靠换来源撑爆内存。

### 配对码

- 8 位，字符集 30 个（去掉 0/O/1/I/L/U），约 6.6×10¹¹ 种；显示时分成 4+4 两段。
- 接受时管理员输入 worker 终端上显示的码，服务端常量时间比较；错误返回「配对码不符」，不回显正确值；同一申请累计输错 5 次即作废（置 `expired`，记审计）。
- 说明：配对码防的是冒名——攻击者的申请会显示它自己的码，与真 worker 终端上的码对不上。若某次在 web 上看到多条相似申请，以终端上的码为准。

### 状态机与并发

- **接受**：
  1. 先生成 token 与指纹；
  2. 条件更新占位：`UPDATE … SET state='accepting', token_fp=?, worker_id=?, … WHERE id=? AND state='pending' AND decide_until>now`，影响 0 行则返回 409（已过期 / 已被处理）；
  3. 经 `Registrar.Register`（`core.Update` 回调里调 `config.RegisterWorkerInto`）登记；失败（id 重复、保留名、写配置失败）则条件更新回 `pending`，错误原样返回给 web；
  4. 成功则置 `accepted`，`collect_until = now + 领取窗口`，记审计，发推送。
- **领取**：校验凭据；状态为 `accepted` 且未过领取截止时，从配置读 `server.workers[worker_id].token` 并核对指纹，一致则返回；可重复领取直到确认或截止。凭据错累计 5 次即作废申请（accepted 的按「过期」撤销）。
- **确认**：置 `collected`。另外，worker 首次成功连上 hub 时，若该 worker_id 有 `accepted` 申请、且配置里的 token 指纹与申请一致，也置 `collected`（兜底确认请求丢失）。接线：hub 的 presence 观察者只有一个槽位且已被 livepush 使用，由 serve 扇出给 livepush 与本功能；回调运行在连接 goroutine 上，只入队，由业务层异步处理。
- **过期**（每 5 秒清扫一次，读取时也惰性判定）：
  - `pending` 过决定截止 → `expired`；
  - `accepted` 过领取截止仍未确认 → `expired`，并经 `Registrar.Remove`（`config.RemoveWorkerFrom(cfg, worker_id, token_fp)`）撤销：只有配置里的 token 指纹仍一致时才撤销（含断开连接），否则只改状态、不动配置；
  - `accepting` 超过 30 秒（处理中崩溃）→ 按对账处理。
- **启动对账**：server 启动时扫描未结束的申请：`accepting` 的——配置里存在该 worker 且指纹一致则视为 `accepted`（重新计算领取截止），否则回到 `pending` 或 `expired`；其余按过期规则处理。这样 server 在流程中途重启不会留下无人持有 token 的 worker，也不会丢掉已接受的申请（token 一直在配置里，重启后仍可领取）。
- **清理**：结束的申请保留 30 天后由清扫顺带删除（本功能自己的清理，不依赖 job 清理）。

### 推送、Today 与可见性

- 申请变化发 `ChangeWorkerEnrollment`，推送到新主题 `worker-enrollments`：加入 `globalTopics` 白名单；在 `wsTopicAllowed` 里加分支，要求 `worker.manage`；在 `wsTopicPerms` 里登记同一权限，保证订阅与重新声明时都检查。推送是无负载的失效通知（`Notify`），客户端收到后调受保护的列表接口获取详情与计数。
- 不放进 `pending` 快照（它不按权限过滤）。
- Today：`handleToday` 在 `s.can(c, worker.manage)` 为真时，于 Service 结果之后追加 `worker_enroll` 卡片（不经 Service 的缓存、snooze 与 advice 路径，不产生这些表的记录）；卡片带决定截止的倒计时，紧急度为 `now`，动作「去处理」跳到 Runners 页。
- 顶栏：有未决申请时显示带计数的提醒（复用 EscalationBell 的样式与倒计时组件），只对持有 `worker.manage` 的人显示。

### web

- Runners 页「待接受」区：每条申请显示来源、hostname、OS / 架构、gofer 版本与协议版本、提议的 id 与标签、agents、显式 roots、倒计时；**不显示配对码**——管理员要到 worker 终端上看码再输入。
- 接受对话框：配对码输入框（4+4 两段）、worker id（默认取提议值，可改；保留名与已存在 id 当场提示，并说明已存在时要先删除旧 worker 或改 id）、项目多选、标签。
- 拒绝：原因（可选）与「屏蔽该来源 10 分钟」勾选。
- 只有 `can('worker.manage')` 时显示这些入口；功能关闭时 Runners 页在「添加 worker」旁提示如何开启。

### CLI：`gofer worker join`

```
gofer worker join --server <addr> [--id <提议 id>] [--labels a,b] [--roots from=to]… [--workspace <dir>]
                  [--start] [--yes] [--force] [--timeout <dur>] [-c <config>]
```

- 不提供 `--project`：项目由管理员在接受时选。`-c` 走 `bindConfigFlag`（G011）。新增的全局 flag 结构登记进 `TestFlagGlobalsRegistryComplete`（G053）。
- 先做本地检查（与 `worker init` 相同）：已有 `worker.yaml` 且未带 `--force` 时直接退出，不发申请。
- 生成领取凭据、收集主机信息、发申请；打印配对码（大字号分段）、截止时间与提示「请在 server 的 web → Runners 页输入此配对码并接受」。
- 循环长轮询（每次 ≤ 25 秒）直到结果或截止，期间显示剩余时间；网络错误与 server 重启时退避重试，不直接判失败。
- Ctrl-C（`signal.Notify(os.Interrupt)`，Windows 同样适用）：尽力带凭据调用一次放弃，然后退出。
- accepted：
  1. 立即把 token 写入 `.env`（`upsertEnvFile`，0600）；
  2. 把 `workerInitOpts.id` 设为**接受时确定的 worker id**（管理员可能改过），连同 `server`、`token`、`roots`、`workspace`、`yes`、`force`、`timeout` 调用 `runWorkerInit` 的「已有 token」分支；
  3. 发确认；
  4. 带 `--start` 时启动 worker（Windows 上 `-d` 不支持，改为打印前台启动命令）。
- 步骤 2 失败（assignable、写文件、doctor）时：token 已在 `.env`，打印「worker 已登记为 <id>，token 已保存在 <path>；修复问题后执行 `gofer worker init --server … --id <id> --yes` 继续」，并仍发确认，避免已登记的 worker 被过期撤销。
- rejected / expired / superseded / cancelled：打印原因，非零退出。
- server 地址是非回环的 `http://` 时打印警告：token 会明文经过网络（与 `worker add` 相同），建议使用 https。
- 输出里不打印 token。

## 关键流程

### 正常接入

1. worker：`gofer worker join --server https://srv:8767 --id gpu1 --start` → 显示配对码 `K7QX-M3PA` 与 2 分钟倒计时。
2. 管理员：web 顶栏出现「1 个 worker 申请注册」→ Runners 页「待接受」→ 核对来源与主机名 → 输入 `K7QX-M3PA`，选项目 → 接受。
3. server：占位 → 登记（写配置、热加载）→ accepted → 推送。
4. worker：领取 → 写 `.env` → 写 `worker.yaml`、doctor → 确认 → 启动；首次连上 hub。

### 冒名

攻击者也发了一条提议 id 为 `gpu1` 的申请：web 上出现两条申请；管理员按真 worker 终端上的码输入，只有真申请能通过配对码校验；攻击者的申请可直接拒绝并屏蔽来源。攻击者无法阻止真 worker 发起申请（不再有同名唯一约束），也无法占满所有名额（按来源限额 + 顶替）。

### 默认数值

| 项 | 默认 | 说明 |
|---|---|---|
| 决定窗口 | 120 秒 | 用户要求的 2 分钟；可配置 30–600 秒 |
| 领取窗口 | 60 秒 | 接受后起算；可重复领取，确认即结束 |
| 长轮询单次上限 | 25 秒 | 低于客户端默认的 30 秒超时 |
| 每来源发起频率 | 每分钟 3 次 | 超出 429 |
| 每来源未决 | 1 条 | 新申请顶替旧申请 |
| 全局未决上限 | 20 条 | 兜底，超出 429 |
| 拒绝时屏蔽来源 | 10 分钟（可选） | 期间该来源 429 |
| 配对码输错 | 5 次作废 | 不回显 |
| 领取凭据校验失败 | 5 次作废 | 统一 404 |
| 请求体 | 8 KiB | 读超时 10 秒 |
| 限流表 | 4096 个来源 | LRU + TTL |
| 申请记录保留 | 30 天 | 清扫顺带删除 |

配置（server 端）：

```yaml
server:
  worker_enrollment:
    enabled: false          # 见决策 1
    decide_sec: 120
    collect_sec: 60
    per_source_per_min: 3
    max_pending: 20
    trusted_proxies: []     # 例：["127.0.0.1/32"]，仅在 server 前有反向代理时填
```

- 进 `config` 模型与校验、控制台写接口、`config/*.example.yaml` 与 `gofer init server` 模板（通用写法，G046）。
- 整段可热加载（不需要重启），每次请求读当前配置快照：关闭后匿名路由立即返回 404，清扫仍处理遗留的 pending 与 accepted 申请（到期撤销照常），只是不再接受新申请。字段登记进 `fieldPolicies`：`trusted_proxies` 只随配置文件热加载、不允许经控制台修改；其余字段允许控制台修改。

## 安全、数据、运维与回滚

- **匿名入口**：发起申请不登记任何东西、拿不到任何数据，只有人接受后才登记；按来源限额、顶替、全局上限、屏蔽与开关限制滥用；来源地址不信任转发头，除非配置了可信代理。
- **冒名**：配对码默认手输核对；不再有同名 id 唯一约束，攻击者无法占坑阻止真 worker。
- **token**：只存在配置里（与 `worker add` 一样）；申请行只存指纹；只经领取接口、凭领取凭据、在领取窗口内交给申请方；不进日志、不进 web、不进推送、不进审计；所有响应 `no-store`。
- **撤销误删**：撤销前核对指纹，只撤销这次登记出来的那个 worker；复用 worker 删除语义（含断开连接）。
- **权限**：管理路由 `Require(worker.manage)` + `adminUserOnly`；推送主题与 Today 卡片按同一权限过滤。
- **审计**：业务层经 `AuditSink` 写 `audit_events`，记 `worker.enroll.request`（含失败的匿名请求按来源限频记录）、`accept`、`reject`、`expire`、`collect`、`cancel`、`code_mismatch`、`secret_mismatch`，`target_id` 为申请 id，详情（决定人、worker id、项目、来源）在申请行上；登记本身的 `config.updated` 由接受 handler（actor 为决定人）与清扫撤销（actor 为 `system`）写。
- **数据**：新表 additive；无旧数据迁移。
- **回滚**：`enabled: false` 即关闭；回退二进制时新表无人读，不影响旧功能；通过 join 接入的 worker 与 `worker add` 接入的完全相同。

## 测试计划

遵守 G051–G054：组件在关库前停止；成功路径用 `internal/testutil/wait`，测试里把决定窗口、领取窗口、清扫间隔调小并按 `GOFER_TEST_TIMEOUT_SCALE` 伸缩；commands 测试不连真实 server、子进程用 testcmd。

- 业务层：状态机全路径；接受与过期的竞态（条件更新后可确定性复现）；领取可重复直到确认；确认丢失时 hub 首连兜底；过期撤销在指纹一致时撤销、不一致时不动配置；启动对账（accepting / accepted 各种情况）；配对码与凭据输错限次；同来源顶替；全局上限；屏蔽。
- core：`RegisterWorker` / `RemoveWorker` 迁移后 `worker add` / `worker rm` 原有测试全部通过；指纹不一致不删除。
- httpapi：七条路由的声明与 `TestEveryRouteIsDeclaredOrPending`；功能关闭 404；请求体过大与字段非法 400；`X-Forwarded-For` 伪造不影响来源（未配置可信代理时）与配置了可信代理时的取值；不存在 id 与凭据错同一响应；job 凭据 / worker 凭据调管理路由 403；`no-store`；推送主题对无权限连接的订阅被拒（含 `wsTopicPerms`）；Today 卡片只对有权限的调用方出现。
- commands：`worker join` 端到端（测试 server）：接受、拒绝、过期、顶替、Ctrl-C 放弃、`runWorkerInit` 失败后的提示与确认、管理员改 id 后写入的是新 id、已有 `worker.yaml` 不发申请、非回环 http 警告。
- web：纯函数（倒计时、配对码分段与校验、接受请求构造）vitest；`?raw` 接线断言（权限门、顶栏、Runners 区）。

## 文档

- `skills/gofer-usage/references/setup-recipes.md`（新增「自助注册」配方）、`worker-config.md`、`operations.md`、`commands.md`、`server-config.md`（配置段）、`web-console.md`（Runners 页）；`SKILL.md` 只在入口层需要时加一行。
- `docs/reference/http-api.md`（七条路由）、`docs/reference/permissions.md`（`go generate` 生成）。
- `docs/runbook/2026-09-26-worker-init-runbook.md` 增「自助注册」一节与排障（申请被顶替、配对码不符、领取失败后的补救）。
- README 中接入 worker 的说明（如有）。
- CHANGELOG「## [未发布] / 新增」。

## 决策

1. **默认是否开启**：推荐**默认关闭**（`enabled: false`），在配置里打开一次即可；`gofer init server` 模板里写上并注释说明。理由：gofer 是通用平台，有的 server 暴露在更大的网络里，匿名入口默认关闭更稳妥。备选：默认开启。
2. **接受时配对码**：推荐**默认手输**（web 不显示码，管理员到 worker 终端上看码再输入），这是防冒名的关键一步。备选：web 显示码、点一下接受——只能防点错行，防不了冒名。
3. **接受后到期未领取**：推荐**撤销这次登记**（核对指纹后），避免留下无人持有 token 的 worker。备选：保留登记，由人删除。
4. **项目分配**：推荐**接受时由人选择**，worker 申请时不能指定项目。
5. **拒绝时屏蔽来源**：推荐提供（可选勾选，默认 10 分钟），用来应对重复骚扰申请。备选：不提供，只靠限流。
6. **顶栏提醒**：推荐对持有 `worker.manage` 的人显示带计数的提醒（申请只有 2 分钟，需要及时看到）。备选：只在 Runners 页显示。

## 待确认事项

- 已确认（用户 2026-10-17「按推荐」）：决策 1–6 均取推荐值；决定窗口 120 秒。

## 结论与人工计划 Gate

本稿经用户批准后出实施计划，建议拆为：① 登记 / 撤销抽成 config 纯函数（零行为变化）；② 业务层与存储；③ HTTP 接口、限流与权限；④ `worker join`；⑤ web；⑥ 文档与验收。按惯例建 plan 与 todo 跟进。

## 实施记录

按 plan 任务记录实际提交、路径与偏离（5、6 由 suga 实施、监督者审核后合入）。

| 任务 | 提交 | 主要路径 |
|---|---|---|
| E1 登记与撤销抽成 config 纯函数 | `b25927cd` | `internal/config/worker_register.go`、`internal/httpapi/worker_registration.go` |
| E2a 存储与状态机核心 | `e98f8f06` | `internal/jobstore/worker_enrollments.go`、`internal/jobstore/change.go`、`internal/workerenroll/service.go` |
| E2b 配置、限流、清扫与 serve 接线 | `77244240`、`e16ad9fa` | `internal/config/worker_enrollment.go`、`internal/workerenroll/limiter.go`、`internal/workerenroll/sweep.go`、`internal/serve/worker_enroll.go`、`internal/serve/livepush.go` |
| E3 HTTP 接口 | `872aebc8` | `internal/httpapi/worker_enroll_handler.go`、`internal/httpapi/authz_routes.go`、`internal/client/worker_enroll.go`、`docs/reference/http-api.md` |
| E4 实时 topic 与 Today 卡片 | `212757a9` | `internal/pushhub/hub.go`、`internal/today/enroll.go`、`internal/httpapi/today_handler.go`、`internal/httpapi/authz_live_conn.go` |
| E5 `gofer worker join` | `48a75a3b`、`5623c589`（合入 `fb39d0d1`，suga） | `internal/commands/worker_join.go`、`skills/gofer-usage/references/{commands,setup-recipes,worker-config}.md` |
| E6 web 控制台 | `06e53ea9`、`0a882765`（合入 `c6b073ee`，suga） | `web/src/views/Runners.vue`、`web/src/components/Enroll*.vue`、`web/src/utils/enrollments.ts`、`web/src/api/` |
| E5 后续：CLI 输出改英文 | `5c73cef4`（`fix(worker): English output for gofer worker join`） | `internal/commands/worker_join.go`、`internal/commands/worker_join_test.go` |
| D1 文档收尾 | 本次提交 | 运维手册、README、CHANGELOG、本节 |

### 与设计的偏离及原因

- **E2a**：请求体里带的是 `secret_hash`；领取 / 确认 / 取消时客户端出示原始 secret，由 server 做哈希后比对，server 不保存原始 secret。`collect_until` 之后的确认仍然成功，直到清扫任务撤销该登记为止（窗口只决定何时清扫，不单独拒绝迟到的确认）。
- **E2b**：在线状态观察用 `wireLivePush` 的额外观察者做扇出，不新建通道；控制台写配置时保留 `trusted_proxies`（该字段只能改文件，控制台写回不得丢掉它）。
- **E3**：10 秒读超时只设在会读请求体的 POST 上（一直挂着的读 deadline 会打断长轮询的 ctx）；列表项不返回配对码；缺 secret 头直接返回同样的 404，且不访问数据库（不泄露申请是否存在）。
- **E4**：Today 卡片只对用户类调用方出现，因为 `s.can` 对 job / worker 调用方也可能放行；topic 需要在合并器（coalescer）里补一条间隔配置。
- **E5**：接受后**立即**把 token 写入 `.env` 并发送 Confirm，再执行 `worker init`（doctor 可能超过 60 秒领取窗口，放在 Confirm 之前会让 server 撤销一个凭据其实已安全落地的登记）；随后 CLI 输出统一改为英文（与其余 gofer CLI 一致），帮助文本本来就是英文。
- **取舍（已知）**：来源限流表与屏蔽表在内存里，server 重启即清空；申请本身持久化，重启不丢，所以影响只是限流计数归零。后续若需要再落库。

