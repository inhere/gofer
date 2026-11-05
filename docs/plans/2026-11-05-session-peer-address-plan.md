<!-- template_id: plan; template_version: 1.3.0 -->
# web 发消息按会话地址投递 实施计划

> 状态：Draft 0.6 / 已批准并执行中（0.5 用户 2026-11-05：“批准 g1lk 计划 0.5 并开始实施”；0.6 用户 2026-11-05：“按建议修订设计，继续 g1lk”，批准前已披露修订内容，0.6 未做独立评审）
> issue：gofer-g1lk

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-05 | Claude（gofer 监督会话） | 初稿 |
| 0.2 | 2026-11-05 | Claude（gofer 监督会话） | 按两路审核修订（阻塞 3 条，都在 T8）：真实验收的环境隔离写明清除变量和双向预检；进程退出改硬杀；排除开放回合路径并逐条断言走了地址。纠正：写明事务细节和 runner 来源；缺省失败原因和英文文案；真实帧交接；验证命令补全；反向验证加两项；验收 1 不允许用披露跳过 |
| 0.3 | 2026-11-05 | Claude（gofer 监督会话） | 按确认轮修订（阻塞 3 条，都在 T8）：测试会话设 `relay_mode=off`，防止发送后转入开放回合；用临时包装程序强制一次性路径，并按 `job_id` 区分路径；失败消息不断言 channel。纠正：offline 构造、信箱唤醒的配置和断言、`to` 从 transcript 读取、临时 server 和 worker 也清环境、Windows 按真实 pid 硬杀 |
| 0.4 | 2026-11-05 | Claude（gofer 监督会话） | 按第 3 轮修订（阻塞 1 条，在 T8）：worker 常驻路径的消息带 `job_id`，断言改为 `MessengerMeta.Channel=resident`。纠正：临时项目 `allow_exec: true`；包装程序读完 stdin 第一行再退出、Windows 起子进程；一次性路径单独起 server；`session relay` 带 `--session` |
| 0.5 | 2026-11-05 | Claude（gofer 监督会话） | 按第 4 轮的 3 条纠正修订（该轮无阻塞项，这三条未再复核）：临时项目配 `allowed_runners` 和 worker runner；一次性 server 有独立的 hook 目录，预检按所属 server 断言；channel 断言写明读取接口；T0 带包装程序实测 Windows 换行 |
| 0.6 | 2026-11-05 | Claude（gofer 监督会话） | T8 命中 fail-closed 后按设计 0.5 修订：新增 T7a（提示词加固、hook 回复修复、文档）；模型行为的 fail-closed 改为投错或投两次；T8 全路径重跑；反向验证加两项 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：同一台机器上两个存活的 Claude 会话同名时，web 消息和信箱唤醒仍能送到选中的那个会话，投不到时如实显示失败。Scope freeze：只实施设计 Draft 0.5 的范围和下文“对设计的细化”。Review budget：两路独立审核一轮加一轮确认，有阻塞项才继续，上限按严谨规划核心合同。停止条件：确认轮无 `CORE_BLOCKING`，停在计划 Gate。

## 目标与完成定义

目标：web 上选中哪个会话，消息就到哪个；有地址的会话投不到时显示失败，不投给同名的另一个会话；没有地址的会话投递目标不变。

完成定义：T0–T9 通过；设计“结论”的验收 1–9 都有证据；`gofer-verify` 全绿；文档按 G045 同步；设计稿补实施记录。

## 范围、排除项与授权

范围：hook 上报地址；`agent_sessions.peer_socket`；同 runner 地址唯一；三处提示词；常驻路径按 tool_result 判定；回执判定改为只看 stdout 且整行匹配；messenger 回复按地址归属；对应文档。

排除：设计“非目标”全部；`[ref]` 解析；tmux、deliver_command、接管路径；展示地址的读取字段；给 `peer_socket` 建索引；`jobstore/sessions.go`、`sessionrelay/service.go` 已超 1000 行的拆分（GR104 的存量问题，本计划只把新增逻辑放新文件，不搬旧代码）。

授权边界：批准本计划不等于授权执行，执行需要单独的当前请求。本地提交按既有默认授权；push、打 tag、发版、升级不在本计划内。发版后补 `schema-v*` 快照和能力清单（G057、G058）属于发版流程。真实验收只用临时 server 和临时目录，不改用户配置和线上 server。

- `host_or_non_offline_action=REQUIRED`

需要的主机动作：T0、T8 经 `gofer job --runner local` 在 Windows 主机起一次性 Claude 进程和临时 server；建、删 worktree；suga 实施 job；`gofer-verify`。脚本和记录写工作区 `tmp/`。T0、T8 消耗少量模型 token（`--model haiku`）。

真实 Claude 进程会在用户目录留下 `sessions/<pid>.json` 和 transcript 目录，硬杀后也不会自己清掉。收尾只删自己记录的 pid 对应的文件和自己 cwd 对应的 transcript 目录。

## 输入与批准证据

- 设计：`docs/design/2026-11-05-session-peer-address-design.md` Draft 0.5。用户 2026-11-05：“批准 g1lk 设计 0.4”（批准前已披露最后 2 条修订未再复核）；“按建议修订设计，继续 g1lk”（0.5）。
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
| R2 | runner 为空的行怎么算“同 runner”。空 runner 表示没说在哪里跑（client 模式容器），这种会话本来就投不了 | 写入方或其他行 runner 为空时都不参与清理，否则多个未设 runner 的容器互相清掉 `peer_messaging`。残余风险：未设 runner 的进程与带标签的 runner 在同一命名空间时，它复用旧行地址不会清掉旧行；只剩它同名时，回退也不把它算作同名。两者都可能投错，属于设计“前提”的延伸，不加机制。T7 在运维文档写明 client 模式必须设 `GOFER_HOOK_RUNNER` |
| R3 | `ListAgentSessions` 默认只取 200 行，用它判断“有没有其他同名会话”和“按地址匹配”会漏 | 新增两个无上限查询：`ListLiveSessionsByPeerName`、`ListLiveSessionsByPeerSocket`（`state<>'ended'`，含内部会话）。查询失败按“有其他同名会话”处理，不写回退 |
| R4 | 有地址但没有名字的会话走哪条路 | 现有门槛不变：`PeerName` 为空或 `PeerMessaging=false` 仍走 Deliver 阶梯（`message.go:155`、`deliver.go:1352`、`wakeViaMessenger`）。Claude 的会话文件总带名字，不为这种组合加分支 |
| R5 | tool_result 存在但读不出 `success` 字段 | 判失败，状态用 `failed`（`failMessage`，不提供重试），原因用英文固定文案 `unrecognized SendMessage result`（messenger 包不在 CJK 豁免内，也不翻译）。这个状态的现有含义就是“尝试过、结果未知”，不会因重试投两次。T0 冻结的格式是唯一接受的形式，Claude Code 改格式时用户会看到失败而不是假的已送达，T0 的真实抽样防止出厂就错 |
| R6 | worker 的规范回执形状 | `SendResult.Receipt()`：成功返回 `sent`；失败返回单行 `send failed: <原因>`（换行压成空格）。`Reason` 为空时用固定原因 `no SendMessage call`，`Receipt()` 和历史共用；本机常驻分支显示 i18n 文案（T5）。带前缀的单行不可能被整行匹配成回执，不依赖原因文字。worker 仍回 `done`、退出码 0，由 server 看 stdout 判定 |
| R7 | 常驻进程的 peer 回合里模型也可能调 SendMessage | 只在 `!peerTurn` 时记录；回合结束（投递给请求或走 `unsolicited`）都清空，同现有 `listing` |
| R8 | `testcmd` 的常驻假进程只输出 result 帧，判定改后现有常驻测试全部变成“没有调用” | 4 个模式（`GOFER_TEST_STREAM_JSON`、`_PEER`、`stream-json-env`、`stream-json-fake`）都按 T0 的真实帧输出 SendMessage 的 tool_use 和 tool_result，默认成功，由环境变量控制失败。这是设计“涉及文件”之外的预期路径 |
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
- T8 中接收方收到同一消息两次（模型改写地址后回退送达只算一次，不停，改写次数记入 `acceptance.md`）。
- T8 中同名场景投给了未选中的会话。
- T0 或 T8 中工具返回 `success:true`，接收方却没有收到（判定依据 D9 失效）。
- T8 的隔离预检失败。

派活约束（SR1433）：任务书写明 G021–G024、G031、idevstd 加载方式、SR1211 只暂存自己的文件；不做无关重排，不删注释；不在 export 了 `GIT_DIR`/`GIT_WORK_TREE` 的命令里跑测试；不起后台空等；job 用 `--sync`。子 agent 的完成报告只是声明，监督会话逐条核实并做反向验证。

## 波次与依赖

```text
W0  T0 实测冻结（监督会话，不改代码）
W1  T1 存储 → T2 上报
W2  T3 常驻判定、T4 回执判定（互不依赖）→ T5 目标与提示词 → T6 回复归属
W3  T7 文档 → T7a 提示词加固与回复修复（监督会话，设计 0.5）
W4  T8 真实验收（监督会话）→ T9 收尾
```

T1–T7 由 suga 在同一 worktree 顺序实施（计划写 `openai-codex/gpt-6.1-terra`，因其和 `gpt-6-sol` 当时过慢，实际用 `opencode-go/deepseek-v4.1-flash`），每个任务一个提交。T0、T7a、T8、T9 由监督会话做。

## 任务

命令的运行目录都是 Git root（`inhere-tools/gofer` 或它的 worktree）。T1–T6 每个任务提交前都跑 `go build ./... && go vet ./...`（vet 编译测试文件），保证每个提交能编译。

### T0 实测冻结工具结果和时序

- 文件: 只写工作区 `tmp/g1lk-impl/`，脱敏的真实帧放 `tmp/g1lk-impl/frames/`，由 T3 复制到 worktree 的 `internal/messenger/testdata/`。
- 动作: 用 `tmp/g1lk-probe/` 的脚本改写，常驻进程 `--output-format stream-json` 发 SendMessage，抓 tool_use、tool_result、`tool_use_result` 的原始帧。容器和 Windows 主机各抓成功、地址失效；容器再抓同名失败。两个平台都抓硬杀后的失效地址（socket 文件可能残留，错误不一定是 ENOENT），确认仍是 `success:false`；Windows 确认接收方没有收到。再抓 peer 回合的 `command_lifecycle` 和“请求排在 peer 回合之后”的帧序，确认 `peerTurn` 的依据在这两个版本上仍成立。Windows 上确认含换行的提示词经 exec job 和包装程序传给 `claude.cmd` 不被截断。同时确认 messenger 进程 transcript 的位置，以及能从中读到 SendMessage 的 `input.to`（T8 依赖）。另用 `--settings` 挂一个只记录会话文件内容的 SessionStart hook，看该时刻 `messagingSocketPath` 是否已写。
- 验证: `tmp/g1lk-impl/facts.md` 写明每个场景的帧形状和版本；帧文件未跟踪，`check-private-names.sh` 扫不到，改用 `grep -E -f .private-names tmp/g1lk-impl/frames/*` 须无命中。real-env：本任务本身就是真实验证。
- 完成标准: 解析规则能用一句话写出且覆盖两个版本；SessionStart 窗口有结论（有窗口时写进实施记录，按设计的残余风险处理）；没有触发 fail-closed。
- 依赖: 无。

### T1 存储：`peer_socket` 和同 runner 唯一

- 文件: `internal/config/model.go`（`SameRunnerLabel`）、`internal/sessionrelay/reply.go`（`sameRunner` 改调用）、`internal/jobstore/store.go`、`internal/jobstore/sessions.go`、`internal/jobstore/sessions_peer.go`（新）、对应 `_test.go`。
- 动作:
  - 基础 schema 和 ALTER 迁移增加 `peer_socket TEXT NOT NULL DEFAULT ''`，不建索引。`selectSessionCols`、`scanSession` 增加列。
  - `AgentSession.PeerSocket string`；`SessionHeartbeat.PeerSocket *string`。
  - upsert：地址放在现有 peer 块内与 `peer_messaging` 一起覆盖（`sessions.go:682`），insert 同样写入。
  - Touch：`PeerSocket != nil` 时写入，空值即清除，nil 不动。无行时回滚并返回 `(AgentSession{}, false, nil)`，hook 的先注册后重试不变。先 Commit，再做后面的消息日志追加，失败的返回方式与现状相同。
  - 新增 `clearPeerSocketOthersTx(tx, sid, socket, runner string) error`：`socket` 非空时查出其他行的 `session_id, runner`，用 `config.SameRunnerLabel` 逐行比较，相同的行置 `peer_socket=''`、`peer_messaging=0`。runner 为空的行跳过（R2）。upsert 和 Touch 在写入非空地址时改用事务，写入和清理同一事务。只有本次确实写入了非空地址才清理：upsert 的 peer 门槛（`sessions.go:683`）没通过时不写也不清。runner 来源：upsert 更新分支取 `pick(in.Runner, existing.Runner)` 的结果，插入分支取 `in.Runner`，Touch 取事务内读到的已存值。拒绝分支和 `getSessionLocked` 都在 Begin 之前。事务内只用 `tx`，不用 `s.db`，持有现有 `writeMu`。
  - 新增 `ListLiveSessionsByPeerName(name string)`、`ListLiveSessionsByPeerSocket(socket string)`（R3）。
- 验证: `go test ./internal/jobstore -run 'PeerSocket|OpenUpgradesPreviousReleaseSchema' -count=1` 和 `go test ./internal/config ./internal/sessionrelay -count=1`；用例覆盖空值清除、nil 不动、清理别的行、`server`/`local` 写法不同也清（直接写库构造，API 会归一化）、不同 runner 不清、runner 为空不清、空 runner 重新注册按已存 runner 清、门槛没过不写不清、未知会话返回 `ok=false`、写失败回滚。
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
- 验证: `go test ./internal/hookrelay ./internal/sessionrelay ./internal/httpapi -run 'PeerSocket' -count=1`；含表驱动的合法和非法地址（`/var/run/docker.sock`、带 `..`、带空白和引号、超长、大写十六进制的 Windows 地址），及一条断言日志不含原值。HTTP 层一条经真实路由的注册加心跳用例，断言入库且 `GET /v1/sessions/{sid}` 响应不含 `peer_socket`。
- 完成标准: 验收 6 的校验和日志部分通过；读取接口没有多出字段。
- 依赖: T1。

### T3 常驻路径判定

- 文件: `internal/messenger/resident.go`、`internal/messenger/snapshot.go`（如需）、`internal/worker/dispatch.go`、`internal/worker/dispatch_test.go`、`internal/sessionrelay/message_resident_test.go`、`internal/testutil/testcmd/testcmd/main.go`、`internal/sessionrelay/message.go`（接口）、`internal/httpapi/session_inject.go`（适配）、`internal/messenger/testdata/`、对应 `_test.go`。
- 动作:
  - `messenger.SendResult{Output string; Sent bool; Reason string}`；`func (r SendResult) Receipt() string`（R6）。`Manager.Send` 改为返回 `(SendResult, error)`。工具失败是结果值，`error` 只表示进程和请求失败，语义与现在相同。
  - `read` 循环：tool_use 名为 SendMessage 时记下 id；对应 tool_result 按 T0 规则解析，覆盖本回合结果（取最后一次）；`peerTurn` 时不记录（R7）；读不出 `success` 时判失败（R5）；本回合没有调用时 `Sent=false`，`Reason` 留空。`event` 增加这些字段。
  - 历史：`Send` 在 `Sent=false` 时给 `done` 传错误，抽屉显示失败。
  - `dispatch.go:351`：`output = res.Receipt()`。`list_agents` 分支不动。
  - `ResidentMessenger.SendMessengerResident` 返回 `(ResidentResult, error)`，`ResidentResult{Sent bool; Reason string}` 定义在 sessionrelay，由 `session_inject.go` 转换（sessionrelay 不 import messenger）。
  - `testcmd` 常驻假进程按 R8 扩展。
  - `TestWorkerMessengerDispatchUsesResidentProcess` 现在用日志帧证明环境变量到达子进程，改为断言规范回执；环境标记的证明留给 `internal/messenger/resident_test.go`，那里读 `SendResult.Output`。
- 验证: `go test ./internal/messenger ./internal/worker -run 'Send|Resident|Messenger' -count=1`、`go test ./internal/sessionrelay ./internal/httpapi -run 'Messenger|Resident|WebMessage' -count=1`、`go test ./internal/testutil/cjkscan -count=1`；用 T0 的真实帧做解析用例（成功、ENOENT、同名、两次调用取最后、peer 回合不算、没有调用、缺字段）。
- 完成标准: 工具失败时 `Send` 返回 `err==nil`、`Sent=false`，历史记失败；worker 对应写出单行失败回执。
- 依赖: T0。

### T4 回执判定

- 文件: `internal/sessionrelay/message.go`、`internal/sessionrelay/deliver.go`（`InjectState`、`InjectResult`）、`internal/sessionrelay/deliver_raw.go`、`internal/sessionrelay/deliver_reconcile.go`、`internal/httpapi/session_inject.go`、`internal/httpapi/runner_messenger_handler.go`、9 个测试假实现（R9）。
- 动作:
  - `messengerSaidSent(stdout string) bool`：逐行去空白后用 `unicode.IsPunct`、`unicode.IsSymbol` 去掉首尾字符，再整行比较 `已发送`，或不分大小写比较 `sent`。
  - `MessengerJob` 增加 `stdout` 返回值；`InjectState.Stdout`、`InjectResult.Stdout`；`session_inject.go` 用 `TailLog(jobID, StreamStdout, 4096)` 取值；`jobOutput` 不动。
  - 判定调用点改用 stdout：`message.go:235`、`deliver_raw.go:147`、`deliver_reconcile.go:171`、`settleTimedOutMessenger`。失败原因继续用 `output`。
  - 测试跟改：假实现的 `stdout` 给回执而不是空串；`message_timeout_test.go:87`、`:125` 的字面量改用 `Stdout`。
- 验证: `go test ./internal/sessionrelay ./internal/httpapi -run 'SaidSent|Messenger|Reconcile' -count=1`；httpapi 加一条经真实 job 日志的适配层用例，断言 `Stdout` 不含 stderr；表驱动：`已发送`、`已发送。`、`"sent"`、`**sent**`、末行是 `已收到` 且前面有回执、stderr 有警告只进 output、`消息未发送`、`没有已发送`、`the message was not sent`、`send failed: sent`。
- 完成标准: 验收 8 的回执部分通过。
- 依赖: T0。

### T5 投递目标、提示词、常驻分支

- 文件: `internal/sessionrelay/peeraddr.go`、`internal/sessionrelay/message.go`、`internal/sessionrelay/deliver_raw.go`、`internal/i18n/lang/en/sessionrelay.ini`、`internal/i18n/lang/zh-CN/sessionrelay.ini`、对应 `_test.go`。
- 动作:
  - `messengerTarget{Name, Socket string; Fallback bool}`；`func (s *Service) messengerTargetFor(a jobstore.AgentSession) messengerTarget`：再过一次 `validPeerSocket`（防旧数据）；有地址时查 `ListLiveSessionsByPeerName`，除自身外没有同 runner 的同名行才 `Fallback=true`；查询失败 `Fallback=false` 并记 Warn。
  - 新增 4 个 i18n 键（设计“提示词”），en、zh-CN 都加；不含 `] `。地址原样写入，名字继续 `%q`。
  - `messengerPrompt`、`wakeMessengerPrompt` 改收 `messengerTarget`；`wakeMessenger` 把 `peerName string` 换成 `target messengerTarget`，job 标题和 `TargetSession` 继续用名字。
  - 常驻分支（`message.go:200-205`）：`Sent=true` 记已送达；`Sent=false` 走 `failMessage`，原因取 `Reason`，空则用新 i18n 键 `sessionrelay.reason.messenger_no_send`，不转一次性 job；`error` 分支不变。
- 验证: `go test ./internal/sessionrelay -run 'MessengerTarget|Prompt|Resident|Wake' -count=1`、`go test ./internal/i18n -run CatalogKeysMatch -count=1`、`go test ./internal/httpapi -run 'Messenger|Resident|WebMessage' -count=1`；含 Windows 地址的反斜杠不被加倍；`messengerOriginalMessage` 能切出原文；有同名不写回退、无同名写回退、无地址提示词与现在逐字相同；工具失败时 `residentFake.submitted==0`。
- 完成标准: 没有地址时提示词不变（名字路径零变化，验收“无地址行为不变”）；工具失败显示失败（验收 4、8）。
- 依赖: T1、T2、T3、T4。

### T6 回复按地址归属

- 文件: `internal/sessionrelay/reply.go`、`internal/sessionrelay/*_test.go`。
- 动作: `RecordMessengerReply`：先要求 body 非空；`from` 去掉 `uds:` 前缀后过 `validPeerSocket`，用 `ListLiveSessionsByPeerSocket` 加 `sameRunner` 过滤，恰好一个就归属到它；否则按现有名字逻辑，`peerName` 为空才报错。
- 验证: `go test ./internal/sessionrelay -run 'RecordMessengerReply' -count=1`；同名两个会话时按 `from` 归属正确；地址不匹配回到名字；名字为空但地址匹配能归属。
- 完成标准: 同名时 server 本机常驻路径的回复显示在选中的会话下。
- 依赖: T1、T2。

### T7 文档

- 文件: `docs/reference/http-api.md`、`skills/gofer-usage/references/sessions-relay.md`、`docs/runbook/container-worker.md`。
- 动作: http-api 写注册、心跳的可选字段 `peer_socket`（只写不读）。sessions-relay 写投递规则：优先按地址，同名时投不到显示失败，常驻路径如实显示失败。按 G046 用通用视角，不写本仓库细节。container-worker 写每个容器要用独立 runner 及原因，client 模式必须设 `GOFER_HOOK_RUNNER`。
- 验证: `PRIVATE_NAMES_FILE=<主 checkout 绝对路径>/.private-names sh scripts/check-private-names.sh`（worktree 里没有这个文件）；人工核对文档说法与代码一致。
- 完成标准: G045 同步完成，汇报列出改了哪些文件。
- 依赖: T5、T6。

### T7a 提示词加固与回复修复（监督会话）

- 文件: `internal/i18n/lang/{en,zh-CN}/sessionrelay.ini`、`internal/sessionrelay/message_target_test.go`、`internal/hookrelay/permission.go`、`internal/hookrelay/web_reply.go`、`internal/hookrelay/permission_test.go`、`internal/hookrelay/web_reply_test.go`、`skills/gofer-usage/references/sessions-relay.md`。
- 动作:
  - 4 个 `*_address*` 提示词按设计 0.5 改写：地址逐字符照抄、反斜杠个数不变；带回退的版本写明第一次调用必须用地址；不带回退的版本不出现名字。
  - `SkipPostToolUse` 放行 `SendMessage`（`sendMessageTool`），其他工具仍只花一次 stat。
  - `webMessageFrom` 的快速过滤改为认 `msgprefix.PersonOpens` 给出的所有拼写（web 消息和 web 回复两类），不再只认中文。
  - sessions-relay 写明弱模型可能改写地址：结果是显示失败或回退送达，建议传话人用可靠的模型。
- 验证: `go test ./internal/sessionrelay ./internal/hookrelay ./internal/i18n ./internal/commands -run 'Prompt|Messenger|WebReply|SkipPostToolUse|Hook|CatalogKeysMatch' -count=1`；含不带回退的提示词不含名字、Windows 反斜杠不被加倍、`SendMessage` 不被跳过、英文前缀的 transcript 能上报。
- 完成标准: 上述用例通过，一个提交。
- 依赖: T7。

### T8 真实验收（监督会话）

- 文件: 只写工作区 `tmp/g1lk-impl/`（脚本、日志）。
- 环境隔离:
  - 用 worktree 的构建产物起临时 server（临时端口、配置目录和库），server 和 hook 用两个配置目录：hook 目录的 `.env` 写地址、token、`GOFER_RUN_MODE=client`、`GOFER_HOOK_RUNNER`（本机路径写 `local`，worker 路径写 worker id）；server 目录不设 client 模式（client 模式下 serve 拒绝启动）。cwd 属于临时配置里的项目（否则报 `no_project`），该项目设 `allow_exec: true`（一次性路径和信箱唤醒都是 exec job）和 `allowed_runners: [local, <worker id>]`，并声明 `runners.<worker id>`（type worker），它等于 `GOFER_HOOK_RUNNER`。
  - 启动真实 Claude、临时 server、临时 worker 前都清除 `GOFER_JOB_ID`、`GOFER_JOB_TOKEN`、`GOFER_SERVER_ADDR`、`GOFER_SERVER_TOKEN`、`GOFER_CONFIG`、`GOFER_RUN_MODE`、`GOFER_HOOK_RUNNER`、`GOFER_PROJECT`、`GOFER_BIN`、`CLAUDECODE`、`CLAUDE_CODE_*`，再设 `GOFER_CONFIG_DIR` 指向临时目录、`PATH` 前置临时二进制。主机 job 里 `GOFER_JOB_ID` 会让 hook 直接退出，job 凭据优先于 `.env`，不清就可能报给线上 server。
  - 一次性路径的 server 有自己的端口、配置目录、库和 hook 目录，每条路径的 `GOFER_CONFIG_DIR` 指向所属 server 的 hook 目录。
  - 预检：每条路径先起一个进程，所属临时 server 必须有这条会话，另一个临时 server 和线上 server 都必须没有；任一不符就停。
  - 排除开放回合路径：临时配置 `session.auto_relay_idle_sec: 0`、`auto_relay_turn_sec: 0`；每个真实会话注册后、第一次发送前用 `gofer session relay off --session <sid>` 设为 `off` 并断言（`auto` 会话第一次发送成功就会被 `FollowWeb` 切到 `on`，下一条消息走回合路径，也不再被唤醒）；每次发送前断言没有 open turn。
  - 强制一次性路径：本机路径的 `messenger_command` 指向 `tmp/g1lk-impl/` 里的一次性 Go 包装程序（不入库），参数含 `--input-format` 时读完 stdin 第一行再退出（产生 `resident messenger exited`，避开写入失败的竞争），否则起真实 claude 的子进程，透传输出和退出码（Windows 不支持 exec）。两者都写绝对路径。`messenger_command` 是 server 级配置且不热加载，一次性路径单独起一个 server；本机常驻和 worker 用配真实 claude 的 server。worker 只有常驻路径，矩阵里不列一次性。
  - 路径断言：成功消息要求 `channel=messenger`，本机一次性要求 `job_id` 非空，本机常驻要求为空；worker 常驻的消息也带 `job_id`，要求 `GET /v1/jobs/<job_id>` 的 `.messenger.channel` 等于 `resident`（本机一次性等于 `one-shot`；`gofer job show` 读不到这个字段）。消息字段从 `GET /v1/sessions/{sid}/outbox` 读。失败消息没有 channel，用工具调用目标和 `error` 不是 Deliver 阶梯的原因判定。
  - `to` 的来源：从 messenger 进程的 transcript 读 SendMessage 的 `input.to`，常驻和一次性相同。T0 确认 transcript 位置，T8 预检确认读得到。
  - 信箱唤醒：临时配置调小 `mail.wake_idle_sec`、`mail.wake_interval_sec`。
  - 每个场景用独立名字（如 `g1lk-s3-<路径>`）。硬杀留下的行不是 `ended`，同名会干扰后续场景。
  - 临时 worker 路径：worker 注册到临时 server，`GOFER_HOOK_RUNNER` 等于 worker id。
  - 进程只按自己记录的 pid 停；收尾看临时 server 日志并按“授权”一节清理。不改用户配置。
- 动作（Linux 容器：本机常驻、一次性、经临时 worker 三条路径；Windows 主机：本机常驻、一次性）。场景 1–4 按上面的路径断言，并记录 `to`（`uds:` 或名字）；“退出”一律硬杀（Linux `kill -9`，Windows `Stop-Process -Force`），pid 取 `sessions/<pid>.json` 的文件名（`.cmd` 入口下启动进程的 pid 是 shim），发送前断言该进程已退出、状态不是 `ended`:
  1. 两个同名存活会话，用 `session.offline_after_sec` 等两个都变 offline（扫描周期 60s，阈值要大于第一次发送的耗时）。选中 A 发一次（发送前断言 A 是 offline），只有 A 收到。再给 B 喂一轮 stdin 让它恢复，选中 B 发一次（发送前断言 B 不是 offline），只有 B 收到。回复显示在同一会话下。信箱唤醒：发信前断言状态是 `idle` 或 `offline`，唤醒没有 outbox 行，改断言 messenger job 的提示词带地址、只有选中的会话收到（验收 1）。
  2. 选中的进程硬杀、同名的另一个存活：显示失败，另一个没有收到，提示词不带回退（验收 2）。
  3. 选中的进程硬杀，一个未接入 gofer 的同名进程存活（只给它设 `GOFER_MESSENGER=1`，走 hook 的 bypass，并核实临时 server 没有这行）：回退名字，只投一次（验收 3）。
  4. 经 API 注册不带地址的同名会话：工具失败，web 显示失败（验收 4，名字路径）。
  5. Linux：`sh -c 'echo $$ >pid; 等预置完成; exec claude ...'` 先拿 pid（pid 不变的前提先实测），算出地址，预置带 `peer_name` 的旧行，用 SQL 把它的 runner 分别改成 `server`、`local`；断言第一次带地址的上报之后旧行没有地址、`peer_messaging=0`（验收 5）。Windows 的管道名不可预置，写 `UNVERIFIED_REAL_ENV`，由 T1 用例承担。
  6. 经 API 注册非法地址（真实会话文件的地址无法改成非法值）：入库为空、走名字路径、日志不含原值（验收 6）。
  7. 模型行为：逐轮记录每次 SendMessage 的 `to` 和接收方收到的次数，每种情况常驻和一次性各 5 轮，含 Windows 反斜杠地址。改写只记录次数；投错或投两次才判失败。情况：地址有效；地址失效且有同名存活（不带回退，提示词不含名字，改发名字且投到同名会话就停）；地址失效且无同名（带回退，最多一次）；`wake_prompt_address*` 抽样。
- 重跑（设计 0.5）：提示词和 hook 都变了，容器三条路径和 Windows 两条路径都重跑全部场景。回复断言按验收 10：三条路径都要显示在选中的会话下且只有一条（`source` 可能是 `session` 或 `messenger`）；英文 server 另抽一次。
- 验证: `tmp/g1lk-impl/acceptance.md` 逐条记录命令、接收方日志片段（只记探针文字，不含真实消息）和结论。做不到的项写 `UNVERIFIED_REAL_ENV: <面>（复现：<命令>）`。
- 完成标准: 验收 1 在两个平台都有证据，不能用披露代替；验收 2–6 也都要有证据，只有确实不可构造的项（目前只有 Windows 的 T8.5）可以披露；没有触发 fail-closed。
- 依赖: T7。

### T9 反向验证、全量和收尾（监督会话）

- 文件: 设计稿“实施记录”、本计划状态、`.gofer/tracker/*`。
- 动作:
  - 反向验证（验收 9，在临时副本里改，不提交）：把 `messengerTargetFor` 改回只用名字，同名测试必须失败；去掉 `clearPeerSocketOthersTx`，pid 复用测试必须失败；`Receipt()` 失败也返回 `sent`，worker 判定测试必须失败；`read` 把 `success:false` 当成功，本机常驻判定测试必须失败；`Fallback` 恒为 true，同名不回退的测试必须失败；`SkipPostToolUse` 改回跳过 `SendMessage`，对应测试必须失败；`webMessageFrom` 改回只认中文，英文前缀的测试必须失败。
  - 对 `git diff --stat` 查无关删除；逐条对照设计核实 suga 的声明。
  - `gofer-verify` 工作流（四路）通过。Windows 全量随发版前流程。
  - 合并回 main（`--no-ff`），删 worktree 和分支；设计稿补实施记录；计划进入 COMPLETE 时写不超过 8 行的完成总结；关 `gofer-g1lk`。
- 验证: 反向验证输出写入 `tmp/g1lk-impl/reverse.md`；`gofer wf show <id>` 全绿；主 checkout 里 `sh scripts/check-private-names.sh` 通过；`git log` 核对提交只含本任务文件。
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
| 10 回复显示，三条路径、中英文 | T7a | T8 重跑，T7a 用例 |
| 待实测：模型行为 | T5 | T8.7 |
| 待实测：Windows 失效返回 | T3 | T0 |
| 待实测：SessionStart 时序 | T2 | T0，实施记录 |
| 每容器独立 runner 的前提 | T7 | 文档核对 |

## 完成 Gate 与剩余工作

完成 Gate：T0–T9 通过；`gofer-verify` 全绿；真实验收按 T8 完成标准有证据；设计稿补实施记录；`gofer-g1lk` 关闭。

剩余工作（不在本计划内）：随下一版发版时 push；发版后补 `schema-v*` 快照（G057）、更新能力清单（G058）。设计记录的残余风险（未装 hook 的 Claude 复用 pid）不处理。
