<!-- template_id: design; template_version: 1.2.0 -->
# web 发消息按会话地址投递设计

> 状态：Draft 0.1 / 待人工计划批准
> issue：gofer-g1lk

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-05 | Claude（gofer 监督会话） | 初稿 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：同一台机器上两个存活的 Claude 会话同名时，web 消息和信箱唤醒仍能送到选中的那个会话。
Scope freeze：只改 gofer 的会话上报、投递提示词和回复归属；不改 Claude Code，不改会话命名，不做通用寻址层。
Review budget：按严谨模式上限；第 2 轮没有 `CORE_BLOCKING` 即停。停止条件：评审收敛后交用户批准。

## 背景与目标

用户在手机 web 上给会话发消息，收到报错：两个会话都叫 `proj-4d`，无法确定发给谁，消息没有发出。

名字由 Claude Code 派生，形如 `<目录名>-<2 位十六进制>`，约 256 种取值，同一个项目开几个长期会话就有机会撞名。gofer 的 messenger 目前只按名字调 SendMessage，撞名时只能失败。

用户可见结果：web 上选中哪个会话，消息就到哪个会话；对方的回复显示在这个会话下。撞名时不需要用户 `/rename`，也不需要用户理解地址。

## 名词

- **messenger**：gofer 起的 `claude -p` 进程，名字是 `gofer-web`，替 web 调 SendMessage。主机上是常驻进程，其他 runner 上是一次性 job。
- **派生名（peer name）**：Claude Code 给会话起的名字，gofer 已上报为 `peer_name`。
- **会话地址（peer socket）**：Claude Code 写在会话文件 `messagingSocketPath` 里的本机通信端点。Linux 上形如 `/tmp/cc-socks/<pid>.sock`，Windows 上形如 `\\.\pipe\LOCAL\cc-msg-<32 位十六进制>`。作为 SendMessage 目标时写成 `uds:<地址>`。

## 范围与非目标

范围：

- hook 从会话文件读地址，随注册和心跳上报；server 校验后存库。
- web 消息（`SendMessage`）和信箱唤醒（`wakeMessenger`）优先按地址投递，失败时按名字回退一次。
- messenger 收到对方回复时，优先按地址归属到会话。

非目标：

- 不改名字，不自动 rename，不改变 web 和 CLI 上显示的会话名。
- 不在 web、CLI 或读取类 API 里展示地址。
- 不处理跨机器投递：messenger 本来就在目标会话的 runner 上运行。
- 不改撞名回退失败时的报错文案（由 Claude Code 给出，已经能说明原因）。
- 不改 tmux、deliver_command 和接管路径。
- 不引入 `[ref]` 解析，理由见“决策”。

## 已确认事实与规范

实测（2026-11-05，一次性探针，脚本在 `tmp/g1lk-probe/`，未进仓库）：

| # | 场景 | 结果 |
|---|---|---|
| F1 | 容器，Claude 2.1.318，两个存活会话同名 `g1lk-dup`，第三个进程发给 `uds:<A 的地址>` | `success:true`；A 的输出里有消息，B 没有 |
| F2 | 同上，发给裸名字 | `success:false`，`2 agents are named 'g1lk-dup'. Re-send with the ref…`，A、B 都没收到 |
| F3 | 发给不存在的 `uds:/tmp/cc-socks/999999.sock` | `success:false`，`ENOENT … this socket path is stale` |
| F4 | 容器，发给不带 `uds:` 前缀的路径 | `success:true`，A 收到 |
| F5 | Windows 主机，Claude 2.1.292，发给 `uds:\\.\pipe\LOCAL\cc-msg-…` | `success:true`，接收方输出里有消息 |

只有 F1、F5 用的形式进入方案；F4 只作记录。

代码事实：

- `internal/hookrelay/peer.go:33` 已解析 `MessagingSocketPath`，只转成 `Messaging` 布尔值后丢弃。会话文件是 Claude Code 的内部格式，字段可能变化，现有约定是缺失时静默。
- `internal/sessionrelay/message.go:199`、`:221` 与 `deliver_raw.go:128` 三处拼 messenger 提示词，都按 `%q` 引用名字。`%q` 会把反斜杠加倍，不能用来写 Windows 地址。
- `internal/jobstore/sessions.go:852` 心跳的 `peer_name` 只在非空时更新；`peer_messaging` 用指针表示“本次是否上报”。
- `internal/sessionrelay/reply.go:111` `RecordMessengerReply` 按名字匹配，匹配数不是 1 时丢弃回复（只记日志）。撞名时这条兜底路径会丢回复。`PeerReply.From` 已经带 `uds:` 地址（`internal/messenger/reply.go`）。
- 主路径上，目标会话自己的 hook（`internal/hookrelay/web_reply.go`）按 session id 上报回复，不受撞名影响；messenger 路径只是兜底。
- `SessionOffline` 由心跳超时扫描设置（`internal/jobstore/sessions.go:40`）。
- hook 经 HTTP 直连 server，不走 worker 协议，增加可选字段不需要升协议版本。JSON 解码未对这些接口启用 `DisallowUnknownFields`（只有 `worker_enroll_handler.go` 启用）。

文档来源：Claude Code 的 SendMessage 工具说明写明“回复时把 `from` 属性抄到 `to`”，`from` 就是 `uds:<地址>`；同时写“there is no separate address syntax”，并且“没有从列表或报错里读到的 ref 不会解析”。所以 `uds:` 目标是有文档支撑的回复用法，但主动发给它不算对外承诺，方案必须能回退。

规范：G057（schema 改动要能升级旧库）、G021–G024（分层）、SR1301（安全核对）、GR109（不吞写入错误）、SR1402.1（真实边界验证）。

## 总体方案

```text
Claude 会话文件 messagingSocketPath
  → hook 读取并校验 → 注册 / 心跳带 peer_socket
  → server 再次校验 → agent_sessions.peer_socket

web 发消息 / 信箱唤醒
  → 选目标：会话非 offline 且有 peer_socket → uds:<地址>，名字作回退
             否则 → 名字（与现在相同）
  → messenger 提示词写明“发给哪个目标；只有工具返回 success:false 才改发名字，最多一次”

messenger 收到回复
  → 先按 From = uds:<地址> + 同 runner 匹配唯一会话
  → 匹配不到再按名字（与现在相同）
```

没有地址（旧 hook、旧 Claude Code、字段被改名）时，行为与现在完全一样。

## 架构

代码组织：

| 位置 | 改动 |
|---|---|
| `internal/hookrelay/peer.go`、`run.go` | `peerIdentity` 增加 `Socket`；注册和心跳带上 |
| `internal/client/client.go` | `SessionRegister`、`SessionHeartbeat` 增加可选 `peer_socket`；读取类结构不加 |
| `internal/httpapi/session_handler.go` | 注册、心跳请求接收字段，交给 service |
| `internal/sessionrelay/service.go` | 入参透传，校验失败时按空处理并记 Warn |
| `internal/sessionrelay/peeraddr.go`（新） | `ValidPeerSocket`、`messengerTarget`，归 sessionrelay 这一领域包；hook 不能依赖 sessionrelay 时校验函数放 `internal/util` |
| `internal/sessionrelay/message.go`、`deliver_raw.go` | 三处提示词改用 `messengerTarget`；`wakeViaMessenger` 的入参不变 |
| `internal/sessionrelay/reply.go` | `RecordMessengerReply` 先按地址匹配 |
| `internal/jobstore/store.go`、`sessions.go` | 新列 `peer_socket`，基础 schema 与 ALTER 迁移，读写 |
| `internal/i18n/lang/{en,zh-CN}/sessionrelay.ini` | 增加带地址的提示词 |

分层不变：hook 只上报，判断放 sessionrelay，存储放 jobstore，HTTP 层只透传。参照现有 `peer_messaging` 的上报路径。

### 地址校验

只接受白名单形式，长度不超过 256：

```text
Linux / macOS: ^/[A-Za-z0-9._/-]+$
Windows:       ^\\\\\.\\pipe\\[A-Za-z0-9._\\-]+$
```

不合格按“没有地址”处理，回到名字路径。这样地址里不可能有引号、空白或换行，写进提示词不会变成指令。

### 提示词

新增 `sessionrelay.messenger.prompt_address`、`wake_prompt_address`（en、zh-CN）。中文示意：

```text
请使用 SendMessage 将下面消息原样转发给 Claude Code 会话，to 必须原样写成 uds:\\.\pipe\LOCAL\cc-msg-…，不要改动其中的字符。
只有工具返回 success:false 时，才改发给 "proj-4d"，最多一次。不要调用 ListAgents。不要改写。只回复‘已发送’或失败原因。
```

地址不经过 `%q`，原样写入；名字继续用 `%q`。回复判定（`messengerSaidSent`）不变。

## 关键流程

### 撞名时发消息

1. 用户在 web 选中会话 A，发送。
2. `SendMessage` 走到 messenger 分支，A 的状态不是 offline，且有地址，提示词写 `uds:<A 的地址>`。
3. messenger 发给 A，只有 A 收到；状态记为已送达。
4. A 回复时，A 自己的 hook 按 session id 上报，显示在 A 下。hook 没上报时，messenger 按 `From` 地址归属到 A。

### 地址过期

- A 重启后 pid 变化，新进程的 SessionStart hook 重新注册，地址更新。
- 两次心跳之间如果发送，工具返回 ENOENT，messenger 按提示词改发名字一次。工具已返回失败，消息没有投出，不会重复。
- 回退时名字仍然撞名，则按 F2 失败，记录失败原因；web 显示发送失败，重试不会重复投递。

### 会话 offline

Linux 地址里含 pid，pid 可能被复用，长时间没有心跳的记录可能指向别的会话。所以 offline 会话不用地址，只按名字发（与现在相同）。心跳恢复后状态回到存活，再用地址。

## 安全、数据、运维与回滚

安全（SR1301）：

- 鉴权：字段走已有的注册、心跳接口，调用方校验不变。只能写自己有权写的会话记录，与 `peer_name` 相同。
- 注入：地址按白名单校验，hook 和 server 各校验一次，以 server 为准；SQL 用参数绑定。
- 秘密：地址是本机路径，不是凭据，但不对外展示，读取类 API 和 CLI 都不返回。它会出现在 messenger job 的提示词里，这些 job 的日志已经包含消息正文，权限不变。
- 入参校验：见上。
- 资源归属：地址只来自该会话自己的上报；投递时 messenger 运行在该会话的 runner 上，不跨机器。

误投风险：调用方若伪造地址，消息会投到同一台机器上的其他会话。这与伪造 `peer_name` 的能力相同，没有扩大权限，不新增防护。

数据：`agent_sessions` 增加 `peer_socket TEXT`，默认空，ALTER 迁移，并补 `schema-v<下一版>` 快照。不回填，下次心跳写入。

运维：升级后，存活会话下一次 hook 事件后才有地址，此前走名字。不需要改配置。

回滚：退回旧版后多余的列不影响运行（读写都写明列名）。

## 决策

| # | 决策 | 理由 |
|---|---|---|
| D1 | 用 `uds:<地址>` 投递，名字作回退 | F1、F5 实测可用，F2 证明名字走不通 |
| D2 | 不用 `name [ref]` | ListAgents 不返回 session id，gofer 无法把 ref 对应到会话；工具说明写明没读到的 ref 不解析；抽查 ref 也不是 session id 的前缀或常见哈希 |
| D3 | 不让 messenger 自己从 ListAgents 里挑 | 列表没有能区分会话的字段，模型只能猜，投错比报错更糟 |
| D4 | 不自动 rename | 改变用户看到的名字，也改变其他会话的寻址，超出目标 |
| D5 | offline 不用地址 | 避免 pid 复用造成误投；回到现有行为 |
| D6 | 回退由提示词描述，不在 gofer 里重试 | 只有 messenger 能看到工具结果；gofer 重试要再判断第一次是否投出，容易重复投递 |
| D7 | 不展示地址 | 用户不需要，少一个对外字段 |

## 待确认事项

无阻塞项。实施时核实一项：hook 所在包是否能引用校验函数，决定它放 `sessionrelay` 还是 `util`（只影响代码位置）。

## 结论与人工计划 Gate

方案改动小于通用寻址层：一个可选字段、一列、两条提示词、一处回复匹配。没有地址时行为不变。

因为涉及 schema 与 hook 接口，按 Full 流程走：设计评审后请用户批准；批准后再写实施计划，计划另行批准后才实施。批准本设计不授权实施、push 或发版。

实施验收（写入计划）：

1. 真实环境两个同名存活会话，web 发消息只到选中的那个，回复显示在同一会话下；Linux 容器和 Windows 主机各一次（SR1402.1）。
2. 地址过期时回退到名字，消息只投一次。
3. 旧库升级通过 `TestOpenUpgradesPreviousReleaseSchema`。
4. 反向验证（SR1433）：把提示词改回只用名字，同名测试必须失败。
