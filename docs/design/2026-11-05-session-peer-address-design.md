<!-- template_id: design; template_version: 1.2.0 -->
# web 发消息按会话地址投递设计

> 状态：Draft 0.2 / 待人工计划批准
> issue：gofer-g1lk

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-05 | Claude（gofer 监督会话） | 初稿 |
| 0.2 | 2026-11-05 | Claude（gofer 监督会话） | 按第 1 轮两路评审修订（阻塞 4 条）：offline 会话也用地址；地址改为同 runner 唯一，防 pid 复用；有其他同名会话时不回退名字；常驻路径判定投递结果；并收紧地址白名单、改更新语义、改正 messenger 路径描述 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：同一台机器上两个存活的 Claude 会话同名时，web 消息和信箱唤醒仍能送到选中的那个会话，投不到时如实显示失败。
Scope freeze：只改 gofer 的会话上报、投递提示词、投递结果判定和回复归属；不改 Claude Code，不改会话命名，不做通用寻址层。
Review budget：按严谨模式上限；确认轮没有 `CORE_BLOCKING` 即停。停止条件：评审收敛后交用户批准。

## 背景与目标

用户在手机 web 上给会话发消息，收到报错：有两个会话同名，无法确定发给谁，消息没有发出。

名字由 Claude Code 派生，形如 `<目录名>-<2 位十六进制>`，约 256 种取值，同一个项目开几个长期会话就有机会撞名。gofer 的 messenger 目前只按名字调 SendMessage，撞名时只能失败。

用户可见结果：

- web 上选中哪个会话，消息就到哪个会话，对方的回复显示在这个会话下，会话空闲多久都一样。
- 投不到时显示失败，绝不投给同名的另一个会话，也绝不显示已送达。
- 撞名时不需要用户 `/rename`，也不需要用户理解地址。

## 名词

- **messenger**：gofer 起的 `claude -p` 进程，名字是 `gofer-web`，替 web 调 SendMessage。server 本机和 worker 上都优先用常驻进程（`internal/messenger`、`internal/worker/dispatch.go`），启动失败时本机回退一次性 job。
- **派生名（peer name）**：Claude Code 给会话起的名字，gofer 已上报为 `peer_name`。
- **会话地址（peer socket）**：Claude Code 写在会话文件 `messagingSocketPath` 里的本机通信端点。Linux 上形如 `/tmp/cc-socks/<pid>.sock`，Windows 上形如 `\\.\pipe\LOCAL\cc-msg-<32 位十六进制>`。作为 SendMessage 目标时写成 `uds:<地址>`。

## 范围与非目标

范围：

- hook 从会话文件读地址，随注册和心跳上报；server 校验后存库，并保证同 runner 内地址唯一。
- web 消息（`SendMessage`）和信箱唤醒（`wakeMessenger`）优先按地址投递。
- 没有其他同名会话时，地址失败可按名字回退一次；有其他同名会话时不回退。
- 常驻路径判定投递结果，失败如实记录。
- messenger 收到对方回复时，优先按地址归属到会话。

非目标：

- 不改名字，不自动 rename，不改变 web 和 CLI 上显示的会话名。
- 不新增展示地址的读取字段。回复记录的 `SessionMessage.Peer` 现已保存 `from`（可能是地址），现状不变。
- 不处理跨机器投递：messenger 本来就在目标会话的 runner 上运行。
- 不改 tmux、deliver_command 和接管路径。
- 不引入 `[ref]` 解析，理由见“决策”。

## 已确认事实与规范

实测（2026-11-05，一次性探针，脚本在 `tmp/g1lk-probe/`，未进仓库）：

| # | 场景 | 结果 |
|---|---|---|
| F1 | 容器，Claude 2.1.318，两个存活会话同名，第三个进程发给 `uds:<A 的地址>` | `success:true`；A 的输出里有消息，B 没有 |
| F2 | 同上，发给裸名字 | `success:false`，`2 agents are named … Re-send with the ref…`，A、B 都没收到 |
| F3 | 发给不存在的 `uds:/tmp/cc-socks/999999.sock` | `success:false`，`ENOENT … this socket path is stale` |
| F4 | 容器，发给不带 `uds:` 前缀的路径 | `success:true`，A 收到 |
| F5 | Windows 主机，Claude 2.1.292，发给 `uds:\\.\pipe\LOCAL\cc-msg-…` | `success:true`，接收方输出里有消息 |

只有 F1、F5 用的形式进入方案；F4 只作记录。这些探针只证明工具可用，模型按提示词的行为和 Windows 地址失效的结果要在实施时实测（见验收）。

代码事实：

- `internal/hookrelay/peer.go:33` 已解析 `MessagingSocketPath`，只转成 `Messaging` 布尔值后丢弃。会话文件是 Claude Code 的内部格式，现有约定是缺失时静默。
- `internal/sessionrelay/message.go:199`、`:221` 与 `deliver_raw.go:128` 三处拼 messenger 提示词，都按 `%q` 引用名字。`%q` 会把反斜杠加倍，不能用来写 Windows 地址。这三处之外没有其他按名字投递的路径；`wakeMessenger` 只收名字，需要增加入参。
- 心跳只来自 hook 事件，沉默 30 分钟即标 offline（`internal/config/model.go:1717`、`internal/jobstore/sessions.go:1342`）。进程可能还活着。信箱也会唤醒 offline 会话（`internal/mail/deliverer.go:353`）。
- `message.go:200-205` 常驻路径在 `rerr==nil` 时直接记已送达，输出被丢弃；模型回复失败原因不算错误。只有一次性 job 路径检查 `messengerSaidSent`。
- `internal/messenger/resident.go:313` 已从 tool_result 读取 ListAgents 的真实结果，可以同样读取 SendMessage。
- `internal/sessionrelay/reply.go:110` `RecordMessengerReply` 按名字匹配，名字为空先报错，匹配数不是 1 时丢弃回复。这条路径只在 `internal/httpapi/server.go:962` 安装，只服务 server 本机的常驻 messenger；worker 上的回复只靠目标会话自己的 hook。
- 主路径上，目标会话的 hook（`internal/hookrelay/web_reply.go`）按 session id 上报回复，不受撞名影响。
- 心跳里 `peer_name` 只在非空时更新（`sessions.go:852`），注册的 upsert 也保留旧值（`sessions.go:682-685`）。地址不能照搬，否则校验失败时旧地址会留下。
- `runner` 是 hook 自报的标签（`internal/commands/hook.go:332-346`），不能证明地址属于哪个命名空间。
- 所有读取都走 `selectSessionCols` + `scanSession`，新增列不影响其他读路径。
- hook 经 HTTP 直连 server，不走 worker 协议；增加可选字段不需要升协议版本（G032）。心跳校验属主（`internal/sessionrelay/service.go:620-640`），不新增路由（G056 无需改）。
- `session_inject.go` 的 `messengerOriginalMessage` 在第一个 `] ` 处切分，新提示词里不能出现这两个字符。

文档来源：Claude Code 的 SendMessage 工具说明写明“回复时把 `from` 属性抄到 `to`”，`from` 就是 `uds:<地址>`；同时写“there is no separate address syntax”。所以主动发给 `uds:` 不算对外承诺，方案必须能退回现有行为。

规范：G057（schema 升级）、G021–G024（分层）、G045（同步 skill）、G058（能力清单）、G031（不写业务名）、SR1301（安全核对）、GR109（不吞写入错误）、SR1402.1（真实边界验证）、SR1433（反向验证）。

## 总体方案

```text
Claude 会话文件 messagingSocketPath
  → hook 读取 → 注册 / 心跳带 peer_socket（与 peer_messaging 一起覆盖，空值即清除）
  → server 白名单校验 → agent_sessions.peer_socket
    （同一事务清掉同 runner 其他行的相同地址）

web 发消息 / 信箱唤醒
  → 有 peer_socket → 目标 uds:<地址>
      同 runner 没有其他未结束的同名会话 → 提示词允许回退名字一次
      有 → 提示词不写回退，失败就是失败
  → 没有 peer_socket → 目标是名字（与现在相同）
  → 判定结果：常驻路径看 SendMessage 的 tool_result，一次性路径看回执末行

messenger 收到回复（仅 server 本机常驻路径）
  → 先按 From = uds:<地址> + 同 runner 匹配
  → 匹配不到再按名字（与现在相同）
```

没有地址（旧 hook、旧 Claude Code、字段被改名）时，投递目标与现在完全一样。

## 架构

代码组织：

| 位置 | 改动 |
|---|---|
| `internal/hookrelay/peer.go`、`run.go` | `peerIdentity` 增加 `Socket`；注册和心跳带上 |
| `internal/client/client.go` | `SessionRegister` 增加 `peer_socket`，`SessionHeartbeat` 增加指针 `peer_socket`；读取类结构不加 |
| `internal/httpapi/session_handler.go` | 注册、心跳请求接收字段，交给 service |
| `internal/sessionrelay/service.go` | 入参透传；校验失败按空处理，Warn 日志不写原值 |
| `internal/sessionrelay/peeraddr.go`（新） | `validPeerSocket`、`messengerTarget`；调用都在 sessionrelay 内，hook 不做校验 |
| `internal/sessionrelay/message.go`、`deliver_raw.go` | 三处提示词改用 `messengerTarget`；`wakeMessenger` 增加目标入参；常驻分支判定结果；一次性路径改用末行判定 |
| `internal/sessionrelay/reply.go` | `RecordMessengerReply` 先按地址匹配，调整名字为空的校验顺序 |
| `internal/messenger/resident.go` | `read` 同时收集 SendMessage 的 tool_result，放进 `event` 并返回给调用方 |
| `internal/jobstore/store.go`、`sessions.go` | 新列 `peer_socket`，基础 schema 与 ALTER 迁移（不建索引，G057）；写入时清同 runner 同地址 |
| `internal/i18n/lang/{en,zh-CN}/sessionrelay.ini` | 增加带地址的提示词 |
| `docs/reference/http-api.md`、`skills/gofer-usage/references/sessions-relay.md`、`container-worker.md` | 写字段、投递规则和前提（G045） |

分层不变：hook 只上报，判断放 sessionrelay，存储放 jobstore，HTTP 层只透传。参照现有 `peer_messaging` 的上报路径。

### 地址校验

只接受 Claude Code 现有的两种形式，由 server 校验：

```text
Linux / macOS: 路径段由 [A-Za-z0-9._-] 组成，不含 "..", 以 /cc-socks/<数字>.sock 结尾，长度不超过 256
Windows:       ^\\\\\.\\pipe\\LOCAL\\cc-msg-[0-9a-f]{32}$
```

不合格按“没有地址”处理，回到名字路径。这样地址不能指向其他 socket（如 docker.sock），也不可能带引号、空白或换行，写进提示词不会变成指令。Claude Code 改了格式时，退回现有的名字路径。

### 地址更新

- 地址与 `peer_messaging` 一起覆盖，空值即清除；旧 hook 的心跳不带字段（指针为 nil）时保持不变。
- 写入非空地址时，同一事务把同 runner 其他行的相同地址清空。pid 被新会话复用时，新会话注册的那一刻旧记录就失去地址，消息不会投到新会话头上。

### 提示词

新增 `sessionrelay.messenger.prompt_address`、`prompt_address_fallback`、`wake_prompt_address`、`wake_prompt_address_fallback`（en、zh-CN）。保留现有提示词的全部其他要求（回复只输出“已收到”、唤醒提示就是完整消息、只回复“已发送”或失败原因）。带回退的版本多一句：

```text
to 必须原样写成 uds:\\.\pipe\LOCAL\cc-msg-…，不要改动其中的字符。
只有工具返回 success:false 时，才改发给 "proj-4d"，最多一次。
```

不带回退的版本写“工具返回失败就报告失败原因，不要改发其他目标”。地址不经过 `%q`，原样写入；名字继续用 `%q`。

### 投递结果判定

- **常驻路径**：看本轮最后一次 SendMessage 的 tool_result。`success:true` 记已送达；`success:false` 或本轮没有调用记失败，原因取工具消息。worker 上的常驻 messenger 经 job 输出回传，走一次性路径的判定。
- **一次性路径**：`messengerSaidSent` 改为只看末行，要求整行精确匹配（中英文）。现在中文“已发送”出现在任意位置都算成功，失败叙述里带这三个字会误判。

这项修正同样作用于名字路径：现在按名字发失败时，本机常驻路径也会误显示已送达。

## 关键流程

### 撞名时发消息

1. 用户在 web 选中会话 A，发送。A 可能已空闲很久、状态是 offline。
2. A 有地址，提示词写 `uds:<A 的地址>`；因为有同名的 B，不写回退。
3. messenger 发给 A，只有 A 收到；工具返回成功，记已送达。
4. A 回复时，A 自己的 hook 按 session id 上报，显示在 A 下。server 本机常驻路径上，hook 没上报时再按 `From` 地址归属到 A。

### 地址过期

| 情况 | 结果 |
|---|---|
| A 重启，pid 变化 | 新进程的 SessionStart hook 重新注册，地址更新 |
| A 已退出，有同名的 B | 工具返回 ENOENT，不回退，显示失败。用户选的是 A，不能投给 B |
| A 已退出，没有其他同名会话 | 回退名字一次；通常也失败，显示失败，名字指向未接入 gofer 的同名会话时与现在行为相同 |
| 回退后工具仍失败 | 显示失败，消息没有投出 |

### pid 复用

新会话拿到旧会话的 pid 时，注册即清掉旧记录的地址，旧记录退回名字路径。残余风险：新进程是没有装 gofer hook 的 Claude，它不注册，旧记录仍指向它。这种情况下消息会投到那个会话，机器上同时存在装和没装 hook 的 Claude 才会发生；gofer 无法识别，记录在此，不增加机制（SR1410.1）。

## 安全、数据、运维与回滚

安全（SR1301）：

- 鉴权：字段走已有的注册、心跳接口，属主校验不变（`service.go:620-640`）。
- 注入：地址按白名单校验，以 server 为准；SQL 用参数绑定。消息正文的注入风险是现状，没有扩大。
- 秘密：地址是本机路径，不是凭据，不新增读取字段。它会出现在 messenger 的提示词和 job 日志里，这些位置已经包含消息正文，权限不变。校验失败的日志不写原值。
- 入参校验：见“地址校验”。
- 资源归属：地址只来自该会话自己的上报。

误投风险：有权写自己会话记录的调用方可以把地址设成同机另一个 Claude 会话的地址，从而让发给自己会话的消息投到那里，也会清掉对方记录的地址（对方下次心跳写回）。收紧白名单后，到达范围只有 Claude 会话，与伪造 `peer_name` 相同，没有扩大权限，不新增防护。

前提：地址只在写入它的命名空间内有效，而 `runner` 是自报标签。一台机器上多个容器若自报同一个 runner，地址可能指向另一个容器的同 pid 路径；名字路径在这种情况下只会报错，地址路径可能静默投错。部署上每个容器要用独立的 runner（现有 worker 部署已如此），在 `container-worker.md` 写明，验收覆盖。

数据：`agent_sessions` 增加 `peer_socket TEXT`，默认空，ALTER 迁移。不回填，下次 hook 事件写入。发版后按 G057 补该版本的 `schema-v*` 快照。

运维：升级后，存活会话下一次 hook 事件后才有地址，此前走名字。不需要改配置。发版时同步能力清单（G058）。

回滚：退回旧版后多余的列不影响运行（读写都写明列名）。再次升级时旧地址留到下次 hook 事件，由唯一性规则和 ENOENT 兜底。

## 决策

| # | 决策 | 理由 |
|---|---|---|
| D1 | 用 `uds:<地址>` 投递 | F1、F5 实测可用，F2 证明名字走不通 |
| D2 | 不用 `name [ref]` | ListAgents 不返回 session id，gofer 无法把 ref 对应到会话；工具说明写明没读到的 ref 不解析；抽查 ref 也不是 session id 的前缀或常见哈希 |
| D3 | 不让 messenger 自己从 ListAgents 里挑 | 列表没有能区分会话的字段，模型只能猜，投错比报错更糟 |
| D4 | 不自动 rename | 改变用户看到的名字，也改变其他会话的寻址，超出目标 |
| D5 | offline 会话也用地址 | offline 只表示 30 分钟没有 hook 事件，进程可能还活着；手机上发消息的目标经常是这种会话 |
| D6 | 同 runner 内地址唯一 | 解决 pid 复用，比按状态或时间判断更确定 |
| D7 | 有其他同名会话时不回退名字 | 回退可能投给 B 却记成 A 已送达；用户选了 A，失败比投错好 |
| D8 | 回退由提示词描述，不在 gofer 里重试 | 只有 messenger 能看到工具结果；gofer 重试要再判断第一次是否投出，容易重复投递 |
| D9 | 常驻路径按 tool_result 判定 | 模型文字不可靠，tool_result 是工具真实结果；否则失败也显示已送达 |
| D10 | 不做 Warn 之外的地址诊断 | 上报失败退回现有路径，用户无感，不增加机制 |

## 待确认事项

无阻塞项。实施时实测，结果不符就回到设计：

- 模型按新提示词的行为：不改字符、不多发，常驻和一次性各多轮，含 Windows 反斜杠。
- Windows 地址失效时工具的返回，以及 `success:false` 时消息确实没有投出（目前只实测过 Linux ENOENT 和撞名）。

## 结论与人工计划 Gate

没有地址时投递目标不变；有地址时投给选中的会话，投不到就显示失败。

因为涉及 schema 与 hook 接口，按 Full 流程走：设计评审后请用户批准；批准后再写实施计划，计划另行批准后才实施。批准本设计不授权实施、push 或发版。

实施验收（写入计划）：

1. 真实环境两个同名存活会话，其中一个已标 offline：web 发消息只到选中的那个，回复显示在同一会话下；Linux 容器和 Windows 主机各一次（SR1402.1）。信箱唤醒同样验证。
2. 选中的会话已退出、另有同名会话时：显示失败，另一个会话没有收到。
3. 没有其他同名会话、地址过期时：回退名字，消息只投一次。
4. 本机常驻路径上工具返回失败时：web 显示失败，不显示已送达（名字路径也一样）。
5. pid 复用：新会话注册后，旧记录没有地址。
6. 非法地址（如 docker.sock、带 `..`）被拒绝，走名字路径；日志不含原值。
7. 旧库升级通过 `TestOpenUpgradesPreviousReleaseSchema`。
8. 反向验证（SR1433）：把提示词改回只用名字，同名测试必须失败；去掉唯一性清理，pid 复用测试必须失败。
