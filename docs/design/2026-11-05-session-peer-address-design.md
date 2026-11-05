<!-- template_id: design; template_version: 1.2.0 -->
# web 发消息按会话地址投递设计

> 状态：Draft 0.6 / 已批准（0.4 用户 2026-11-05：“批准 g1lk 设计 0.4”；0.5 用户 2026-11-05：“按建议修订设计，继续 g1lk”，批准前已披露实测结果和修订内容；0.5、0.6 未做独立评审；0.6 是重跑发现的修正，属于已批准的回复修复和结果判定范围，已向用户披露）
> issue：gofer-g1lk

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-05 | Claude（gofer 监督会话） | 初稿 |
| 0.2 | 2026-11-05 | Claude（gofer 监督会话） | 按第 1 轮两路评审修订（阻塞 4 条）：offline 会话也用地址；地址改为同 runner 唯一，防 pid 复用；有其他同名会话时不回退名字；常驻路径判定投递结果；并收紧地址白名单、改更新语义、改正 messenger 路径描述 |
| 0.3 | 2026-11-05 | Claude（gofer 监督会话） | 按确认轮修订（阻塞 1 条、纠正 7 条）：回执改为任一行匹配且只看 stdout；常驻路径的工具失败作为结果值返回；同 runner 按归一化比较；清地址时同时关闭 messenger 路径；写明 SessionStart 时序待实测和默认 runner 前提 |
| 0.4 | 2026-11-05 | Claude（gofer 监督会话） | 按最后一轮的 2 条纠正修订（未再复核）：回执与失败原因分开取值，`jobOutput` 不动；传话人历史按结果结构记录 |
| 0.5 | 2026-11-05 | Claude（gofer 监督会话） | 按真实验收修订：模型会改写地址，提示词加固，同名时不写名字；模型行为的停止条件改为“投错或投两次才停”，残余风险写明；修复 hook 回复上报（放行 SendMessage、认中英文前缀），纠正“hook 是主路径”的旧说法 |
| 0.6 | 2026-11-05 | Claude（gofer 监督会话） | 按验收重跑修订：hook 匹配 JSON 转义的 Windows 地址，认传话人名字（发送方或回复目标）；回执第一句算数；提示词要求照抄消息；记录加固在弱模型一次性路径上的实测 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：同一台机器上两个存活的 Claude 会话同名时，web 消息和信箱唤醒仍能送到选中的那个会话，投不到时如实显示失败。
Scope freeze：只改 gofer 的会话上报、投递提示词、投递结果判定和回复归属；不改 Claude Code，不改会话命名，不做通用寻址层。
Review budget：按严谨模式上限；确认轮没有 `CORE_BLOCKING` 即停。停止条件：评审收敛后交用户批准。

## 背景与目标

用户在手机 web 上给会话发消息，收到报错：有两个会话同名，无法确定发给谁，消息没有发出。

名字由 Claude Code 派生，形如 `<目录名>-<2 位十六进制>`，约 256 种取值，同一个项目开几个长期会话就有机会撞名。gofer 的 messenger 目前只按名字调 SendMessage，撞名时只能失败。

用户可见结果：

- web 上选中哪个会话，消息就到哪个会话，对方的回复显示在这个会话下，会话空闲多久都一样。
- 有地址的会话投不到时显示失败，不投给同名的另一个会话，也不显示已送达。没有地址的会话（旧 hook、旧 Claude Code）投递目标与现在相同。
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
- 修复目标会话的 hook 回复上报：worker 和一次性路径只靠它显示回复。

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

真实验收实测（2026-11-05，脚本在 `tmp/g1lk-impl/t8/`，未进仓库）：

| # | 场景 | 结果 |
|---|---|---|
| F6 | Windows，一次性路径，非 Claude 的弱模型，地址有效，5 轮 | 2 轮把 `uds:\\.\pipe…` 写成 `uds:\.\pipe…`，工具失败后回退名字送达；已核对 job 参数相同，是模型改写 |
| F7 | Windows，失效地址，0.4 文案，每组 12 轮（样本小） | 中文改写 1 次；英文改写 3 次，其中 1 次第一次调用就发名字；中文加固文案 0 次 |
| F8 | 容器，`claude-haiku-5-5`，常驻和一次性 | 没有改写 |
| F9 | 同 F6 的弱模型，0.5 及以后的文案，三种情况各 5 轮 | 常驻改写 0/15；一次性改写 6/15，变成多写反斜杠；没有投错或投两次；地址有效的一组有 1 轮显示失败 |
| F10 | 目标会话回复 | 地址在 transcript 里是 JSON 转义的；模型转发时可能丢前缀；目标可能回给名字 `gofer-web` 而不是 `from` 地址 |
| F11 | 容器，一次性路径 | 模型回“已发送。消息已投递到…”，整行匹配会把已送达判成失败 |

代码事实：

- `internal/hookrelay/peer.go:33` 已解析 `MessagingSocketPath`，只转成 `Messaging` 布尔值后丢弃。会话文件是 Claude Code 的内部格式，现有约定是缺失时静默。
- `internal/sessionrelay/message.go:199`、`:221` 与 `deliver_raw.go:128` 三处拼 messenger 提示词，都按 `%q` 引用名字。`%q` 会把反斜杠加倍，不能用来写 Windows 地址。这三处之外没有其他按名字投递的路径；`wakeMessenger` 只收名字，需要增加入参。
- 心跳只来自 hook 事件，沉默 30 分钟即标 offline（`internal/config/model.go:1717`、`internal/jobstore/sessions.go:1342`）。进程可能还活着。信箱也会唤醒 offline 会话（`internal/mail/deliverer.go:353`）。
- `message.go:200-205` 常驻路径在 `rerr==nil` 时直接记已送达，输出被丢弃；模型回复失败原因不算错误。只有一次性 job 路径检查 `messengerSaidSent`。
- `internal/messenger/resident.go:313` 已从 tool_result 读取 ListAgents 的真实结果，可以同样读取 SendMessage。
- `internal/sessionrelay/reply.go:110` `RecordMessengerReply` 按名字匹配，名字为空先报错，匹配数不是 1 时丢弃回复。这条路径只在 `internal/httpapi/server.go:962` 安装，只服务 server 本机的常驻 messenger；worker 上的回复只靠目标会话自己的 hook。
- 目标会话的 hook（`internal/hookrelay/web_reply.go`）按 session id 上报回复，不受撞名影响，但它现在不工作：`SkipPostToolUse`（`permission.go:558`）跳过非 shell 工具，`SendMessage` 进不到 `postToolUse`；`webMessageFrom`（`web_reply.go:69`）也只认中文前缀。本机常驻路径靠 `RecordMessengerReply` 才显示回复，worker 和一次性路径不显示。两条路径同时上报时，`RecordReply` 按相同文本去重。
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
| `internal/hookrelay/permission.go`、`web_reply.go`、`internal/msgprefix` | `SkipPostToolUse` 放行 `SendMessage`；`webMessageFrom` 认所有语言的人的前缀、JSON 转义的地址、传话人名字（系统通知和信箱唤醒除外）；传话人名字下沉到 `msgprefix` |
| `internal/messenger/resident.go`、`internal/worker/dispatch.go` | `read` 同时收集 SendMessage 的 tool_result；`Manager.Send` 返回结果结构；worker 写规范回执 |
| `internal/sessionrelay/message.go:45`、`internal/httpapi/session_inject.go:54` | `ResidentMessenger` 接口与适配跟随结果结构；`session_inject.go` 增加 stdout 字段供回执判定，`jobOutput` 不动 |
| `internal/jobstore/store.go`、`sessions.go` | 新列 `peer_socket`，基础 schema 与 ALTER 迁移（不建索引，G057）；写入时清同 runner 同地址 |
| `internal/i18n/lang/{en,zh-CN}/sessionrelay.ini` | 增加带地址的提示词 |
| `docs/reference/http-api.md`、`skills/gofer-usage/references/sessions-relay.md`、`docs/runbook/container-worker.md` | 写字段、投递规则和前提（G045） |

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
- 写入非空地址时，同一事务把同 runner 其他行的地址清空，并把这些行的 `peer_messaging` 置 0。这样旧记录既不会按地址投到新会话，也不会按名字投到同名会话；它真还活着时，下次心跳会恢复。
- “同 runner” 与 `sameRunner` 用同一等价关系（`local`、`server` 视为相同，不分大小写），不能用 SQL 等值比较：runner 按原值存储，旧行的写法可能不同。先按地址查出其他行，再在事务内逐行比较。心跳不带 runner，取该行已存的值。

### 提示词

新增 `sessionrelay.messenger.prompt_address`、`prompt_address_fallback`、`wake_prompt_address`、`wake_prompt_address_fallback`（en、zh-CN）。保留现有提示词的全部其他要求（回复只输出“已收到”、唤醒提示就是完整消息、只回复“已发送”或失败原因）。两个版本都明确写地址要逐字符照抄，反斜杠的个数不能变，消息从第一个字符起照抄。带回退的版本多一句：

```text
第一次调用的 to 必须逐字符写成 uds:\\.\pipe\LOCAL\cc-msg-…，反斜杠全部保留，开头是两个就写两个。
只有工具返回 success:false 时，才改发给 "proj-4d"，最多一次。
```

不带回退的版本写“工具返回失败就报告失败原因，不要改发其他目标”，并且**整段提示词不出现名字**：有同名会话时，名字是唯一会投错的目标，模型没读到就发不出去。地址不经过 `%q`，原样写入；名字继续用 `%q`。

提示词只能降低模型出错的概率，不能保证。残余风险见“模型改写地址”。

### 投递结果判定

- **常驻路径**（本机和 worker）：看本轮最后一次 SendMessage 的 tool_result。`success:true` 记已送达；`success:false` 或本轮没有调用记失败，原因取工具消息。`Manager.Send` 改为返回结果结构，工具失败是结果值，不经 `error` 返回：`message.go:210` 按错误文字里的 `start`、`exited`、`unavailable` 转一次性 job，错误文字碰巧含这些词就会再投一轮。worker 把结构转成规范回执（`sent` 或失败原因）写入 job 输出。
- **一次性路径**：`messengerSaidSent` 改为只看 stdout，任一行去掉首尾标点和引号后，整行或第一句（到 `。.！!` 为止，逗号不算）等于回执即算成功（中英文）。这样“已发送。消息已投递到…”算成功，“已发送失败”“已发送，但对方拒收”不算；代价是“已发送。但工具报错”这种自相矛盾的回复会判成功，一次性路径读不到工具结果，无法再分辨。现在中文“已发送”出现在任意位置都算，失败叙述带这三个字会误判；只看末行也不行，对方秒回时末行是“已收到”，stderr 也会混入输出（`session_inject.go:163-172`）。`deliver_reconcile.go:171`、`message.go:423` 用同一函数。回执和失败原因分开取值：`MessengerJob`、`InjectState`、`gaveUp` 增加单独的 stdout 字段供回执判定；`jobOutput` 不动，其他读取方（路径 C 的 `offlineMatch`、失败原因）继续拿 stdout 加 stderr。
- **传话人历史**：`Send` 按结果结构记录（`resident.go:113-115`），工具失败在 Runners 抽屉里显示失败，不是成功。

这项修正同样作用于名字路径：现在按名字发失败时，本机常驻路径也会误显示已送达。

## 关键流程

### 撞名时发消息

1. 用户在 web 选中会话 A，发送。A 可能已空闲很久、状态是 offline。
2. A 有地址，提示词写 `uds:<A 的地址>`；因为有同名的 B，不写回退。
3. messenger 发给 A，只有 A 收到；工具返回成功，记已送达。
4. A 回复时，A 自己的 hook 按 session id 上报，显示在 A 下，三条路径都一样。server 本机常驻路径另按 `From` 地址归属到 A，相同文本去重。

### 地址过期

| 情况 | 结果 |
|---|---|
| A 重启，pid 变化 | 新进程的 SessionStart hook 重新注册，地址更新 |
| A 已退出，有同名的 B | 工具返回 ENOENT，不回退，显示失败。用户选的是 A，不能投给 B |
| A 已退出，没有其他同名会话 | 回退名字一次；通常也失败，显示失败，名字指向未接入 gofer 的同名会话时与现在行为相同 |
| 回退后工具仍失败 | 显示失败，消息没有投出 |

### pid 复用

新会话拿到旧会话的 pid 时，新会话第一次上报地址就清掉旧记录的地址和 messenger 路径。

残余风险，gofer 无法识别，记录在此，不增加机制（SR1410.1）：

- 新进程是没有装 gofer hook 的 Claude，它不上报，旧记录仍指向它。
- SessionStart 时会话文件若还没写地址，新会话第一次上报要等到下一个 hook 事件，这段时间旧记录仍指向它。是否存在这个窗口待实测。

### 模型改写地址

| 模型的行为 | 结果 |
|---|---|
| 改写地址，同名会话存活 | 工具失败，显示失败，用户可以重发 |
| 改写地址，没有同名会话 | 工具失败后回退名字，消息送到，只投一次 |
| 第一次就发名字，选中的和同名的都存活 | 提示词里没有名字，模型要自己找到名字才会发生；万一发生，工具因同名报错（F2），显示失败 |
| 第一次就发名字，选中的已退出，同名的存活 | 同样要模型自己找到名字（如调 ListAgents）；万一发生，**会投给同名的另一个会话**。这是残余的投错风险 |
| 第一次就发名字，同名会话已退出 | gofer 不知道有同名时才可能：未接入 gofer 的同名进程存活，与名字路径现状相同 |

除投错那一行外，结果都是如实显示失败或送到。投错那一行的前兆实测出现过：0.4 不带回退的英文文案写着名字，12 轮里有 1 次第一次调用就发名字（F7，探针里没有同名会话，所以没有投错）。提示词不写名字就是针对它，要到 T8 重测才算有效。这里先不增加机制（SR1410.1），T8 投错一次就停。残余风险写进使用文档：传话人用的模型越弱，失败越多，建议用可靠的模型。更严的 `PreToolUse` 校验 `to` 不做，见 D12。

## 安全、数据、运维与回滚

安全（SR1301）：

- 鉴权：字段走已有的注册、心跳接口，属主校验不变（`service.go:620-640`）。
- 注入：地址按白名单校验，以 server 为准；SQL 用参数绑定。消息正文的注入风险是现状，没有扩大。
- 秘密：地址是本机路径，不是凭据，不新增读取字段。它会出现在 messenger 的提示词和 job 日志里，这些位置已经包含消息正文，权限不变。校验失败的日志不写原值。
- 入参校验：见“地址校验”。
- 资源归属：地址只来自该会话自己的上报。

误投风险：有权写自己会话记录的调用方可以把地址设成同机另一个 Claude 会话的地址，从而让发给自己会话的消息投到那里，也会清掉对方记录的地址（对方下次心跳写回）。收紧白名单后，到达范围只有 Claude 会话，与伪造 `peer_name` 相同，没有扩大权限，不新增防护。

前提：地址只在写入它的命名空间内有效，而 `runner` 是自报标签。两种情况会让标签和命名空间对不上：多个容器自报同一个 runner；容器未设 client 模式时默认报 `server`（`commands/hook.go:346`），在 Linux 主机上可能命中主机的同 pid 会话。名字路径在这种情况下只会报错，地址路径可能静默投错。部署上每个容器要用独立的 runner（现有 worker 部署已如此），在 `docs/runbook/container-worker.md` 写明，验收覆盖。

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
| D11 | 模型改写地址不算投递缺陷，投错或投两次才算 | 改写在实测里只造成失败或回退送达（F6、F7），用户看到的结果都是真的；要求零改写在弱模型上做不到 |
| D12 | 不加 `PreToolUse` 校验 `to` | 同名时提示词不写名字后，投错要同时满足选中的已退出、模型自己找到并改发名字；钩子要给 messenger 进程增加配置和失败路径，先看 T8 重测。T8 或真实使用中出现投错再做 |
| D13 | hook 回复修复纳入本次 | 否则 worker 和一次性路径过不了验收 1；两处都是小改动，且已有去重 |

## 待确认事项

无阻塞项。实施时实测，结果不符就回到设计：

- 模型按新提示词的行为：不投错、不投两次，常驻和一次性各多轮，含 Windows 反斜杠。改写次数记录在实施记录里。
- Windows 地址失效时工具的返回，以及 `success:false` 时消息确实没有投出（目前只实测过 Linux ENOENT 和撞名）。
- SessionStart 时会话文件是否已写地址。没写时按残余风险处理，写进实施记录。

## 结论与人工计划 Gate

没有地址时投递目标不变；有地址时投给选中的会话，投不到就显示失败。

因为涉及 schema 与 hook 接口，按 Full 流程走：设计评审后请用户批准；批准后再写实施计划，计划另行批准后才实施。批准本设计不授权实施、push 或发版。

实施验收（写入计划）：

1. 真实环境两个同名存活会话，其中一个已标 offline：web 发消息只到选中的那个，回复显示在同一会话下；Linux 容器和 Windows 主机各一次（SR1402.1）。信箱唤醒同样验证。
2. 选中的会话已退出、另有同名会话时：显示失败，另一个会话没有收到。
3. 没有其他同名会话、地址过期时：回退名字，消息只投一次。
4. 本机常驻路径上工具返回失败时：web 显示失败，不显示已送达（名字路径也一样）。
5. pid 复用：用真实 SessionStart，新会话上报后，旧记录没有地址，`peer_messaging` 为 0；旧行 runner 写法不同（`server`、`local`）时同样清掉。
6. 非法地址（如 docker.sock、带 `..`）被拒绝，走名字路径；日志不含原值。
7. 旧库升级通过 `TestOpenUpgradesPreviousReleaseSchema`。
8. 回执：对方秒回（末行是“已收到”）、stderr 有警告、回复带标点都判成功；失败叙述里带“已发送”判失败；常驻路径工具失败不转一次性 job。
9. 反向验证（SR1433）：把提示词改回只用名字，同名测试必须失败；去掉唯一性清理，pid 复用测试必须失败；`SkipPostToolUse` 改回跳过 `SendMessage`、`webMessageFrom` 改回只认中文，hook 回复测试必须失败。
10. 回复显示：worker、一次性、本机常驻三条路径上，中文和英文 server 下，目标会话回给 messenger 的话都显示在选中的会话下，只显示一次。

## 实施记录

2026-11-05 实施完成，提交范围见 `feat/g1lk-peer-address` 的合并提交。T1–T7 由 suga（`opencode-go/deepseek-v4.1-flash`，计划模型当时过慢）实施，T7a 及之后由监督会话修改。

- 真实验收：容器（Claude Code 2.1.318，`claude-haiku-5-5`）本机常驻、一次性场景 1–7，worker 场景 1–6 和英文场景 1；Windows（2.1.292，非 Claude 的 `deepseek-v4.1-flash`）本机常驻、一次性场景 1–7。全部通过，没有投错、没有投两次。
- `UNVERIFIED_REAL_ENV: Windows pid 复用（管道名不可预置，由 T1 单测承担；复现：验收程序 -path resident -scen 5）`
- `UNVERIFIED_REAL_ENV: 目标回给名字 gofer-web 时的 hook 上报（修复后两次重跑模型都回了地址，由单测承担；复现：验收程序 -path worker -scen 1 多轮）`
- 弱模型的地址改写仍然存在（F9），只影响一次性路径，表现为显示失败或回退送达。
- SessionStart 时序：两个平台 `-p` 模式 4/4，hook 运行时会话文件已写地址，没有窗口；交互模式没有实测。
- 反向验证 12 项全部确认测试会失败。
- 发版时补 `schema-v*` 快照（G057）和能力清单（G058）。
