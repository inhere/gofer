<!-- template_id: plan; template_version: 1.3.0 -->
# web 发消息按会话地址投递 实施计划

> 状态：Draft 0.1 / 待人工计划批准
> issue：gofer-g1lk

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-05 | Claude（gofer 监督会话） | 初稿 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：同一台机器上两个存活的 Claude 会话同名时，web 消息和信箱唤醒仍能送到选中的那个会话，投不到时如实显示失败。Scope freeze：只实施设计 Draft 0.4 的范围和下文“对设计的细化”。Review budget：两路独立审核一轮加一轮确认，有阻塞项才继续，上限按严谨规划核心合同。停止条件：确认轮无 `CORE_BLOCKING`，停在计划 Gate。

## 目标与完成定义

目标：web 上选中哪个会话，消息就到哪个；有地址的会话投不到时显示失败，不投给同名的另一个会话；没有地址的会话投递目标不变。

完成定义：T0–T9 通过；设计“结论”的验收 1–9 都有证据；`gofer-verify` 全绿；文档按 G045 同步；设计稿补实施记录。

## 范围、排除项与授权

范围：hook 上报地址；`agent_sessions.peer_socket`；同 runner 地址唯一；三处提示词；常驻路径按 tool_result 判定；回执判定改为只看 stdout 且整行匹配；messenger 回复按地址归属；对应文档。

排除：设计“非目标”全部；`[ref]` 解析；tmux、deliver_command、接管路径；展示地址的读取字段；给 `peer_socket` 建索引；`jobstore/sessions.go`、`sessionrelay/service.go` 已超 1000 行的拆分（GR104 的存量问题，本计划只把新增逻辑放新文件，不搬旧代码）。

授权边界：批准本计划不等于授权执行，执行需要单独的当前请求。本地提交按既有默认授权；push、打 tag、发版、升级不在本计划内。发版后补 `schema-v*` 快照和能力清单（G057、G058）属于发版流程。真实验收只用临时 server 和临时目录，不改用户配置和线上 server。

- `host_or_non_offline_action=REQUIRED`

需要的主机动作只有 T0、T8：经 `gofer job --runner local` 在 Windows 主机起一次性 Claude 进程和临时 server，产物只写工作区 `tmp/`。会消耗少量模型 token（`--model haiku`）。

## 输入与批准证据

- 设计：`docs/design/2026-11-05-session-peer-address-design.md` Draft 0.4。用户 2026-11-05：“批准 g1lk 设计 0.4”（批准前已披露最后 2 条修订未再复核）。
- 探针脚本和结果：工作区 `tmp/g1lk-probe/`（不入库，设计 F1–F5 已写明结论）。
- 规范：G021–G024、G031、G032、G045、G056、G057、SR1211、SR1301、SR1402.1、SR1433、GR104、GR109。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 上报、存储、投递目标、结果判定、回复归属 | `peer_messaging` 上报路径、`resident.go` 的 ListAgents tool_result 读取、`sameRunner`、`messengerSaidSent`、`RecordMessengerReply` | 是 | 原位扩展现有路径；`sameRunner` 下沉到 `config` 供 jobstore 复用 | DIRECT_REUSE | none | 无新模块；`peeraddr.go`、`sessions_peer.go` 是现有包内的文件。一列 additive schema，退回旧版无害 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| NONE | none | none |

## 对设计的细化

代码核对发现设计没定或说法不够具体的地方。下面按最小方案定下，批准本计划即确认。都不改变设计的用户结果。

| # | 问题 | 决定 |
|---|---|---|
| R1 | `sameRunner` 在 sessionrelay，jobstore 不能反向 import（G022），而唯一性清理在 jobstore 事务内 | 新增 `config.SameRunnerLabel(a, b string) bool`，`sessionrelay.sameRunner` 改为调用它，行为不变。jobstore 已 import config |
| R2 | runner 为空的行怎么算“同 runner”。空 runner 表示没说在哪里跑（client 模式容器），这种会话本来就投不了 | 写入方或其他行 runner 为空时都不参与清理。否则多个未设 runner 的容器互相清掉 `peer_messaging` |
| R3 | `ListAgentSessions` 默认只取 200 行，用它判断“有没有其他同名会话”和“按地址匹配”会漏 | 新增两个无上限查询：`ListLiveSessionsByPeerName`、`ListLiveSessionsByPeerSocket`（`state<>'ended'`，含内部会话）。查询失败按“有其他同名会话”处理，不写回退 |
| R4 | 有地址但没有名字的会话走哪条路 | 现有门槛不变：`PeerName` 为空或 `PeerMessaging=false` 仍走 Deliver 阶梯（`message.go:155`、`deliver.go:1352`、`wakeViaMessenger`）。Claude 的会话文件总带名字，不为这种组合加分支 |
| R5 | tool_result 存在但读不出 `success` 字段 | 判失败，状态用 `failed`（`failMessage`，不提供重试），原因写“无法识别工具结果”。这个状态的现有含义就是“尝试过、结果未知”，不会因重试投两次。T0 冻结的格式是唯一接受的形式，Claude Code 改格式时用户会看到失败而不是假的已送达，T0 的真实抽样防止出厂就错 |
| R6 | worker 的规范回执形状 | `SendResult.Receipt()`：成功返回 `sent`；失败返回单行 `send failed: <原因>`（换行压成空格）。带前缀的单行不可能被整行匹配成回执，不依赖原因文字。worker 仍回 `done`、退出码 0，由 server 看 stdout 判定 |
| R7 | 常驻进程的 peer 回合里模型也可能调 SendMessage | 只在 `!peerTurn` 时记录；回合结束（投递给请求或走 `unsolicited`）都清空，同现有 `listing` |
| R8 | `testcmd` 的常驻假进程只输出 result 帧，判定改后现有常驻测试全部变成“没有调用” | 假进程按 T0 的真实帧输出 SendMessage 的 tool_use 和 tool_result，由环境变量控制成败。这是设计“涉及文件”之外的预期路径 |
| R9 | `MessengerJob` 如何带 stdout | 在 `output` 后增加返回值 `stdout string`。一次性路径的判定用 `stdout`，失败原因继续用 `output`。`runner_messenger_handler.go` 用 `_` 忽略。9 个测试文件的假实现机械跟改 |

## 前置检查与 fail-closed 条件

workspace baseline：

- Git root：`inhere-tools/gofer`，分支 `main`，基线为提交本计划的 commit（前一提交 `352802f2` 记录设计批准）。
- 工作区只有 `.gofer/tracker/issues.jsonl` 未提交，属于 tracker 同步，随本计划文档一起提交后再开 worktree。
- 实施在 `.worktrees/g1lk`（分支 `feat/g1lk-peer-address`），合并用 `--no-ff`，合并后删 worktree 和分支。worktree 在主机建。
- expected paths 见各任务；实施发现新路径按四类记录。

依赖：Claude Code（容器 2.1.318、主机 2.1.292，实施时以 `claude --version` 为准）；gofer job 可用。

fail-closed，以下任一情况停止并回到设计：

- T0 发现 Windows 上 `success:false` 时消息其实已投出，或 tool_result 在两个版本上形状不同且无法共同解析。
- T8 中模型改写地址字符、或一轮内多发，多轮抽样里出现一次就算。
- T8 中同名场景投给了未选中的会话。

派活约束（SR1433）：任务书写明 G021–G024、G031、idevstd 加载方式、SR1211 只暂存自己的文件；不做无关重排，不删注释；不在 export 了 `GIT_DIR`/`GIT_WORK_TREE` 的命令里跑测试；不起后台空等；job 用 `--sync`。子 agent 的完成报告只是声明，监督会话逐条核实并做反向验证。

## 波次与依赖

```text
W0  T0 实测冻结（监督会话，不改代码）
W1  T1 存储 → T2 上报
W2  T3 常驻判定、T4 回执判定（互不依赖）→ T5 目标与提示词 → T6 回复归属
W3  T7 文档
W4  T8 真实验收（监督会话）→ T9 收尾
```

T1–T7 由 suga 在同一 worktree 顺序实施（`-a suga --model openai-codex/gpt-6.1-terra`，挂 plan），每个任务一个提交。T0、T8、T9 由监督会话做。

## 任务

命令的运行目录都是 Git root（`inhere-tools/gofer` 或它的 worktree）。

### T0 实测冻结工具结果和时序

- 文件: 只写工作区 `tmp/g1lk-impl/`；`internal/messenger/testdata/sendmessage-*.jsonl`（脱敏的真实帧，随 T3 提交）。
- 动作: 用 `tmp/g1lk-probe/` 的脚本改写，常驻进程 `--output-format stream-json` 发 SendMessage，抓 tool_use、tool_result、`tool_use_result` 的原始帧。容器和 Windows 主机各抓成功、地址失效；容器再抓同名失败。Windows 用已退出进程的管道地址做失效场景，确认接收方没有收到。另用 `--settings` 挂一个只记录会话文件内容的 SessionStart hook，看该时刻 `messagingSocketPath` 是否已写。
- 验证: `tmp/g1lk-impl/facts.md` 写明每个场景的帧形状和版本；帧脱敏后 `sh scripts/check-private-names.sh`。real-env：本任务本身就是真实验证。
- 完成标准: 解析规则能用一句话写出且覆盖两个版本；SessionStart 窗口有结论（有窗口时写进实施记录，按设计的残余风险处理）；没有触发 fail-closed。
- 依赖: 无。

### T1 存储：`peer_socket` 和同 runner 唯一

- 文件: `internal/config/model.go`（`SameRunnerLabel`）、`internal/sessionrelay/reply.go`（`sameRunner` 改调用）、`internal/jobstore/store.go`、`internal/jobstore/sessions.go`、`internal/jobstore/sessions_peer.go`（新）、对应 `_test.go`。
- 动作:
  - 基础 schema 和 ALTER 迁移增加 `peer_socket TEXT NOT NULL DEFAULT ''`，不建索引。`selectSessionCols`、`scanSession` 增加列。
  - `AgentSession.PeerSocket string`；`SessionHeartbeat.PeerSocket *string`。
  - upsert：地址放在现有 peer 块内与 `peer_messaging` 一起覆盖（`sessions.go:682`），insert 同样写入。
  - Touch：`PeerSocket != nil` 时写入，空值即清除，nil 不动。
  - 新增 `clearPeerSocketOthersTx(tx, sid, socket, runner string) error`：`socket` 非空时查出其他行的 `session_id, runner`，用 `config.SameRunnerLabel` 逐行比较，相同的行置 `peer_socket=''`、`peer_messaging=0`。runner 为空的行跳过（R2）。upsert 和 Touch 在写入非空地址时改用事务，写入和清理同一事务；Touch 的 runner 取事务内读到的该行已存值。事务内只用 `tx`，不用 `s.db`，持有现有 `writeMu`。
  - 新增 `ListLiveSessionsByPeerName(name string)`、`ListLiveSessionsByPeerSocket(socket string)`（R3）。
- 验证: `go test ./internal/jobstore -run 'PeerSocket|OpenUpgradesPreviousReleaseSchema' -count=1`；用例覆盖空值清除、nil 不动、清理别的行、`server`/`local` 写法不同也清、不同 runner 不清、runner 为空不清、写失败回滚。
- 完成标准: 上述用例通过；旧库升级通过（验收 7）。
- 依赖: T0。

### T2 上报：hook → client → HTTP → service

- 文件: `internal/hookrelay/peer.go`、`internal/hookrelay/run.go`、`internal/client/client.go`、`internal/httpapi/session_handler.go`、`internal/sessionrelay/service.go`、`internal/sessionrelay/peeraddr.go`（新）、对应 `_test.go`。
- 动作:
  - `peerIdentity.Socket`，取 `MessagingSocketPath` 去空白；注册带 `PeerSocket`，心跳 `hb.PeerSocket = &peer.Socket`。
  - `client.SessionRegister.PeerSocket string`（`peer_socket,omitempty`）；`SessionHeartbeat.PeerSocket *string`。读取类结构不加。
  - HTTP 请求结构接收字段（`session_handler.go:528`、`:870`），透传给 service，响应视图不加。
  - `validPeerSocket(raw string) string`：合法返回原值，否则返回 `""`。规则见设计“地址校验”，Linux 正则 `^(/[A-Za-z0-9._-]+)*/cc-socks/[0-9]+\.sock$` 加“路径段不含 `..`”和长度 ≤256；Windows 正则照抄设计。
  - `RegisterForOwner`、`HeartbeatForOwner` 校验：不合格转为空，`slog.Warn` 只写 session id，不写原值。
- 验证: `go test ./internal/hookrelay ./internal/sessionrelay -run 'PeerSocket' -count=1`；含表驱动的合法和非法地址（`/var/run/docker.sock`、带 `..`、带空白和引号、超长、大写十六进制的 Windows 地址），及一条断言日志不含原值。HTTP 层一条经真实路由的注册加心跳用例，断言入库且 `GET /v1/sessions/{sid}` 响应不含 `peer_socket`。
- 完成标准: 验收 6 的校验和日志部分通过；读取接口没有多出字段。
- 依赖: T1。

### T3 常驻路径判定

- 文件: `internal/messenger/resident.go`、`internal/messenger/snapshot.go`（如需）、`internal/worker/dispatch.go`、`internal/testutil/testcmd/testcmd/main.go`、`internal/sessionrelay/message.go`（接口）、`internal/httpapi/session_inject.go`（适配）、`internal/messenger/testdata/`、对应 `_test.go`。
- 动作:
  - `messenger.SendResult{Output string; Sent bool; Reason string}`；`func (r SendResult) Receipt() string`（R6）。`Manager.Send` 改为返回 `(SendResult, error)`。工具失败是结果值，`error` 只表示进程和请求失败，语义与现在相同。
  - `read` 循环：tool_use 名为 SendMessage 时记下 id；对应 tool_result 按 T0 规则解析，覆盖本回合结果（取最后一次）；`peerTurn` 时不记录（R7）；读不出 `success` 时判失败（R5）；本回合没有调用时 `Sent=false`，`Reason` 留空。`event` 增加这些字段。
  - 历史：`Send` 在 `Sent=false` 时给 `done` 传错误，抽屉显示失败。
  - `dispatch.go:351`：`output = res.Receipt()`。`list_agents` 分支不动。
  - `ResidentMessenger.SendMessengerResident` 返回 `(ResidentResult, error)`，`ResidentResult{Sent bool; Reason string}` 定义在 sessionrelay，由 `session_inject.go` 转换（sessionrelay 不 import messenger）。
  - `testcmd` 常驻假进程按 R8 扩展。
- 验证: `go test ./internal/messenger ./internal/worker -run 'Send|Resident|Messenger' -count=1`；用 T0 的真实帧做解析用例（成功、ENOENT、同名、两次调用取最后、peer 回合不算、没有调用、缺字段）。
- 完成标准: 工具失败时 `Send` 返回 `err==nil`、`Sent=false`，历史记失败；worker 对应写出单行失败回执。
- 依赖: T0。

### T4 回执判定

- 文件: `internal/sessionrelay/message.go`、`internal/sessionrelay/deliver.go`（`InjectState`、`InjectResult`）、`internal/sessionrelay/deliver_raw.go`、`internal/sessionrelay/deliver_reconcile.go`、`internal/httpapi/session_inject.go`、`internal/httpapi/runner_messenger_handler.go`、9 个测试假实现（R9）。
- 动作:
  - `messengerSaidSent(stdout string) bool`：逐行去空白后用 `unicode.IsPunct`、`unicode.IsSymbol` 去掉首尾字符，再整行比较 `已发送`，或不分大小写比较 `sent`。
  - `MessengerJob` 增加 `stdout` 返回值；`InjectState.Stdout`、`InjectResult.Stdout`；`session_inject.go` 用 `TailLog(jobID, StreamStdout, 4096)` 取值；`jobOutput` 不动。
  - 判定调用点改用 stdout：`message.go:235`、`deliver_raw.go:147`、`deliver_reconcile.go:171`、`settleTimedOutMessenger`。失败原因继续用 `output`。
- 验证: `go test ./internal/sessionrelay ./internal/httpapi -run 'SaidSent|Messenger|Reconcile' -count=1`；表驱动：`已发送`、`已发送。`、`"sent"`、`**sent**`、末行是 `已收到` 且前面有回执、stderr 有警告只进 output、`消息未发送`、`没有已发送`、`the message was not sent`、`send failed: sent`。
- 完成标准: 验收 8 的回执部分通过。
- 依赖: T0。

### T5 投递目标、提示词、常驻分支

- 文件: `internal/sessionrelay/peeraddr.go`、`internal/sessionrelay/message.go`、`internal/sessionrelay/deliver_raw.go`、`internal/i18n/lang/en/sessionrelay.ini`、`internal/i18n/lang/zh-CN/sessionrelay.ini`、对应 `_test.go`。
- 动作:
  - `messengerTarget{Name, Socket string; Fallback bool}`；`func (s *Service) messengerTargetFor(a jobstore.AgentSession) messengerTarget`：再过一次 `validPeerSocket`（防旧数据）；有地址时查 `ListLiveSessionsByPeerName`，除自身外没有同 runner 的同名行才 `Fallback=true`；查询失败 `Fallback=false` 并记 Warn。
  - 新增 4 个 i18n 键（设计“提示词”），en、zh-CN 都加；不含 `] `。地址原样写入，名字继续 `%q`。
  - `messengerPrompt`、`wakeMessengerPrompt` 改收 `messengerTarget`；`wakeMessenger` 把 `peerName string` 换成 `target messengerTarget`，job 标题和 `TargetSession` 继续用名字。
  - 常驻分支（`message.go:200-205`）：`Sent=true` 记已送达；`Sent=false` 走 `failMessage`，原因取 `Reason`，空则用新 i18n 键 `sessionrelay.reason.messenger_no_send`，不转一次性 job；`error` 分支不变。
- 验证: `go test ./internal/sessionrelay ./internal/i18n -run 'MessengerTarget|Prompt|Resident|Wake' -count=1`；含 Windows 地址的反斜杠不被加倍；`messengerOriginalMessage` 能切出原文；有同名不写回退、无同名写回退、无地址提示词与现在逐字相同；工具失败时 `residentFake.submitted==0`。
- 完成标准: 没有地址时提示词不变（名字路径零变化，验收“无地址行为不变”）；工具失败显示失败（验收 4、8）。
- 依赖: T1、T2、T3、T4。

### T6 回复按地址归属

- 文件: `internal/sessionrelay/reply.go`、`internal/sessionrelay/*_test.go`。
- 动作: `RecordMessengerReply`：先要求 body 非空；`from` 去掉 `uds:` 前缀后过 `validPeerSocket`，用 `ListLiveSessionsByPeerSocket` 加 `sameRunner` 过滤，恰好一个就归属到它；否则按现有名字逻辑，`peerName` 为空才报错。
- 验证: `go test ./internal/sessionrelay -run 'RecordMessengerReply' -count=1`；同名两个会话时按 `from` 归属正确；地址不匹配回到名字；名字为空但地址匹配能归属。
- 完成标准: 同名时 server 本机常驻路径的回复显示在选中的会话下。
- 依赖: T1。

### T7 文档

- 文件: `docs/reference/http-api.md`、`skills/gofer-usage/references/sessions-relay.md`、`docs/runbook/container-worker.md`。
- 动作: http-api 写注册、心跳的可选字段 `peer_socket`（只写不读）。sessions-relay 写投递规则：优先按地址，同名时投不到显示失败，常驻路径如实显示失败。按 G046 用通用视角，不写本仓库细节。container-worker 写每个容器要用独立 runner 及原因。
- 验证: `sh scripts/check-private-names.sh`；人工核对文档说法与代码一致。
- 完成标准: G045 同步完成，汇报列出改了哪些文件。
- 依赖: T5、T6。

### T8 真实验收（监督会话）

- 文件: 只写工作区 `tmp/g1lk-impl/`（脚本、日志）。
- 环境隔离: 用 worktree 的构建产物起临时 server（临时端口、临时配置目录和库）。真实 Claude 进程的 `PATH` 前置临时二进制，`GOFER_CONFIG_DIR` 指向临时目录，这样用户级 hook 配置里的 `gofer hook` 也报给临时 server。开跑前先起一个进程，确认线上 server 没有新增会话，否则停下改隔离方式。进程只按自己记录的 pid 停，收尾看临时 server 日志。不改用户配置。
- 动作（Linux 容器：本机常驻、一次性、经临时 worker 三条路径；Windows 主机：本机常驻、一次性）:
  1. 两个 `--name g1lk-dup` 存活会话，把其中一个在库里标 offline：web 接口发消息，只有选中的收到；回复显示在同一会话下；信箱唤醒同样验证（验收 1）。
  2. 选中的进程退出、另一个存活：显示失败，另一个没有收到（验收 2）。
  3. 选中的进程退出，一个未接入 gofer 的同名进程存活：回退名字，只投一次（验收 3）。
  4. 经 API 注册不带地址的同名会话：工具失败，web 显示失败（验收 4，名字路径）。
  5. 经 API 预置旧行（地址等于新进程将上报的值，runner 分别写 `server`、`local`），真实 SessionStart 之后旧行没有地址、`peer_messaging=0`（验收 5）。
  6. 经 API 注册非法地址（真实会话文件的地址无法改成非法值）：入库为空、走名字路径、日志不含原值（验收 6）。
  7. 模型行为：常驻和一次性各 5 轮，含 Windows 反斜杠地址；核对地址一字不差、每轮只有一次 SendMessage、回执判定正确。
- 验证: `tmp/g1lk-impl/acceptance.md` 逐条记录命令、接收方日志片段（只记探针文字，不含真实消息）和结论。做不到的项写 `UNVERIFIED_REAL_ENV: <面>（复现：<命令>）`。
- 完成标准: 验收 1–6 在两个平台都有证据，或有明确披露；没有触发 fail-closed。
- 依赖: T7。

### T9 反向验证、全量和收尾（监督会话）

- 文件: 设计稿“实施记录”、本计划状态、`.gofer/tracker/*`。
- 动作:
  - 反向验证（验收 9，在临时副本里改，不提交）：把 `messengerTargetFor` 改回只用名字，同名测试必须失败；去掉 `clearPeerSocketOthersTx`，pid 复用测试必须失败；`Receipt()` 失败也返回 `sent`，常驻判定测试必须失败。
  - 对 `git diff --stat` 查无关删除；逐条对照设计核实 suga 的声明。
  - `gofer-verify` 工作流（四路）通过。Windows 全量随发版前流程。
  - 合并回 main（`--no-ff`），删 worktree 和分支；设计稿补实施记录；计划进入 COMPLETE 时写不超过 8 行的完成总结；关 `gofer-g1lk`。
- 验证: 反向验证输出写入 `tmp/g1lk-impl/reverse.md`；`gofer wf show <id>` 全绿；`sh scripts/check-private-names.sh` 通过；`git log` 核对提交只含本任务文件。
- 完成标准: 见“目标与完成定义”。
- 依赖: T8。

## 回滚与恢复

- 恢复点：每个任务一个提交，worktree 隔离；合并前 main 不受影响。合并后退回用 `git revert -m 1 <merge>`。
- 运行时回滚：退回旧版后多余的列不影响运行（读写都写明列名）。再次升级时旧地址留到下次 hook 事件，由唯一性规则和 ENOENT 兜底。
- T8 失败时不合并，按 fail-closed 回到设计或修订本计划。临时 server 和临时目录用完即删，只留 `tmp/g1lk-impl/` 的记录。

## 人工 Gate

1. 批准本计划。
2. 当前执行请求（可与批准同一句）。
3. push、打 tag、发版、升级：不在本计划内，按发版流程另行处理。

实施过程本身没有其他人工 Gate。除非出现 Semantic Amendment 或 fail-closed。

## 可追溯性

| 设计验收 | 任务 | 验证 |
|---|---|---|
| 1 同名只到选中，回复同会话，offline 也行，唤醒同样 | T1 T2 T5 T6 | T8.1，单元测试 |
| 2 选中已退出、另有同名：失败且另一个没收到 | T5 | T8.2 |
| 3 无其他同名、地址过期：回退一次 | T5 | T8.3 |
| 4 工具失败显示失败（名字路径也一样） | T3 T5 | T8.4，T5 用例 |
| 5 pid 复用，runner 写法不同也清 | T1 | T8.5，T1 用例 |
| 6 非法地址走名字路径，日志不含原值 | T2 | T8.6，T2 用例 |
| 7 旧库升级 | T1 | `TestOpenUpgradesPreviousReleaseSchema` |
| 8 回执判定，工具失败不转一次性 job | T4 T5 | T4、T5 用例 |
| 9 反向验证 | T9 | `reverse.md` |
| 待实测：模型行为 | T5 | T8.7 |
| 待实测：Windows 失效返回 | T3 | T0 |
| 待实测：SessionStart 时序 | T2 | T0，实施记录 |
| 每容器独立 runner 的前提 | T7 | 文档核对 |

## 完成 Gate 与剩余工作

完成 Gate：T0–T9 通过；`gofer-verify` 全绿；真实验收有证据或有披露；设计稿补实施记录；`gofer-g1lk` 关闭。

剩余工作（不在本计划内）：随下一版发版时 push；发版后补 `schema-v*` 快照（G057）、更新能力清单（G058）。设计记录的残余风险（未装 hook 的 Claude 复用 pid）不处理。
