# 交互式 ACP 会话 job，及锁等待 / pty 会话 id / prime 精简

> 状态：Draft 0.1（待用户批准）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-30 | Claude | 用户提出的交互式 ACP job；并入下一批小项 tools-qq3、tools-cch、tools-omo |

## 背景与目标

1. **交互式 ACP job（用户提议）**：希望有一种可以持续对话的 job，走 ACP 协议（结构化消息、工具调用、审批），而不是 pty 的终端字节流。
   - 现状：`internal/runner/acp` 每个 job 只跑一轮（initialize → session/new|load → session/prompt → 结束）；工作台「续聊」（`workbench.Service.Turn` → `ResumeJob`）每一轮都新开一个 job，用 `session/load` 接上下文——上下文连续，但每轮都要冷启动 agent 进程、重新 initialize/load，一轮一个 job，列表里很碎。
2. **tools-qq3**：等目录锁超时报错显示 cwd 而非实际锁路径；不能按 job 设置等锁上限（串联多个长 codex job 时会撞 server 默认 3600s）。
3. **tools-cch**：交互式 pty job 被取消/强杀时，agent 来不及打印退出横幅，gofer 抓不到会话 id，无法再次进入（实例：omp job 20260930-125056-90e1effd，4.8MB pty 转录中无任何 UUID）。
4. **tools-omo**：`gofer repo prime` 有 8KiB 总上限，但"进行中 issue"无条数上限、仓库 memory 全文逐条注入，条目多时会挤满预算。

补充事实（2026-09-30 查询）：Claude Code 不提供 MCP server 主动向会话注入消息/唤醒会话的通道（`notifications/message` 被丢弃、`resources/updated` 不支持）；因此"job 完成后通知会话"继续沿用 SESS-03 的 Stop hook 方案。

## 范围与非目标

- 范围：A1（交互式 ACP 会话 job）、B1–B3（三个小项）。
- 非目标：改造 pty 交互 job；让 cli-agent（非 ACP）具备会话常驻；跨 server 重启保持同一个 agent 进程（重启后以 session/load 恢复，见 A1 恢复）。

## 总体方案

### A1 交互式 ACP 会话 job

**形态**：`gofer job run -a <acp-agent> --session`（web 新建 job 时勾选「持续会话」；工作台对 ACP 线程新增「持续会话」模式）。job 启动 agent 的 ACP server 后：

1. 第一轮与现在相同（有 prompt 就执行；也允许空 prompt，直接进入等待）。
2. 一轮结束（`session/prompt` 返回 stop reason）后，job **不结束**，进入 `awaiting_input`（新的非终态，页面显示"等待输入"），agent 进程保持存活。
3. 用户通过 web（job 详情 / 工作台输入框）、CLI `gofer job say <id> "<消息>"`、HTTP/MCP 发送下一条消息 → 同一 ACP 会话里再发一次 `session/prompt`，job 回到 `running`。
4. 结束条件：用户点「结束会话」/`gofer job end <id>`（done）；空闲超时（`idle_timeout_sec`，默认 30 分钟，可配，超时后 done 并记录原因）；取消（cancelled）；agent 进程异常退出（failed）。

**超时语义**（沿用"排队不计超时"原则）：
- `timeout_sec` 改为**单轮**上限：每轮 `session/prompt` 独立计时；
- `idle_timeout_sec` 为等待输入的上限；
- 可选 `max_session_sec` 为整个会话的总上限（默认不限）。

**目录锁**：会话期间持续持有（它随时可能改文件）；`awaiting_input` 期间页面明确显示"持有 <路径> 的锁"，并提供「释放锁并结束」按钮；建议此类 job 优先配合 `--lock <子目录>` 或 `--worktree`。

**输出与事件**：沿用现有 ACP 映射（stdout 为 agent 文本、stderr 为事件行、acp.jsonl 全量），每轮之间插入轮次分隔（`turn N` 事件 + 文本分隔行），job 事件新增 `job.turn_started` / `job.turn_ended` / `job.awaiting_input`。审批（permission）流程不变，仍走现有审批卡片。

**并发与配额**：会话占用 agent 的 `max_concurrent` 名额直到结束；`awaiting_input` 期间计入名额（避免同时挂起过多进程），在 Agents 页显示"会话中 N 个"。

**恢复**：server 重启 / worker 断线时，会话 job 标记为 `recovering`，恢复后若 agent 支持 `session/load`，重新拉起 agent 并 load 原会话，回到 `awaiting_input`；不支持则 failed 并提示改用新会话。

**与工作台续聊的关系**：保留现有"一轮一个 job"的续聊（适合偶尔追问、不想占锁/名额）；新增"持续会话"作为 ACP 线程的另一种模式，工作台输入框在会话 job 存活时直接发往该 job，不再新开 job。

**权限**：`job say` / `job end` 与 job 取消同一套调用方权限（发起者与 user caller 可操作；job caller 只能操作自己派发的会话 job）。

### B1（tools-qq3）目录锁等待

- 报错文案显示实际锁定路径（`lock_paths` / 解析后的路径）与持有者，而不是 cwd。
- 新增 `--lock-wait <秒>`（CLI / 任务书 `lock_wait_sec` / HTTP / MCP）：按 job 覆盖 server 默认 `dir_lock_max_wait_sec`；`0` 表示不限，是否允许 `0` 由 server 配置 `dir_lock_allow_unbounded_wait`（默认允许）决定。

### B2（tools-cch）pty 会话 id 捕获

- 取消交互式 pty job 时先优雅退出：向 pty 发送 agent 配置的退出序列（新配置项 `exit_keys`，内置模板为 codex/claude/omp 各自的退出命令），等待最多 N 秒读取退出横幅，再强杀。
- 支持的 agent 在开跑时预分配会话 id（agent 配置 `session_id_arg`，如 claude 的 `--session-id {{session_id}}`），会话 id 从开跑起就已知。
- 对不支持预分配的 agent，开跑后按 cwd + 开始时间扫描 agent 自己的会话存储目录（agent 配置 `session_store_glob`），尽早记录候选会话 id；终态时若横幅已给出则以横幅为准。
- web：pty job 的输出标签改名为「终端」，与 stdout/stderr 区分。

### B3（tools-omo）prime 精简

- 进行中/已认领 issue 只列前 10 条，并注明"共 N 条，gofer issue ls 查看全部"。
- 仓库 memory 默认只注入带 `prime` 标签的条目的全文；其余只列 key 与首行摘要（每条 ≤80 字），需要全文用 `gofer memory show`。全局/项目记忆同样规则，但 `agent:<名>` 标签的条目视为需要注入的全文（用户的约定类记忆）。
- `.gofer/tracker/config.yaml` 增加 `prime:` 配置（各段开关与条数上限）；`gofer repo status` 显示 prime 预估字节数。

## 实施分期

| 期 | 内容 | 验收 |
|---|---|---|
| X1 | B1 + B3 | 锁等待文案与 `--lock-wait` 测试；prime 段落条数/摘要测试，输出预估字节 |
| X2 | B2 | 取消时优雅退出拿到横幅的测试（用假 agent）；预分配/扫描会话 id 测试；omp/codex/claude 真机各验证一次 |
| X3 | A1 | 会话 job 状态机（running↔awaiting_input→done/cancelled/failed）、单轮超时/空闲超时、say/end 权限、恢复（session/load）测试；真实进程冒烟：同一 job 内三轮对话，第二轮能引用第一轮内容；web 截图 |

## 待确认事项

1. A1 命名与入口：`job run --session` + `job say` / `job end`，web 勾选「持续会话」。可以吗？
2. A1 空闲超时默认 30 分钟、`timeout_sec` 改为单轮上限，可以吗？
3. A1 会话期间持续占用目录锁与 agent 并发名额（等待输入时也占），可以吗？
4. 分期顺序 X1 → X2 → X3（先小项），还是 A1 先做？

## 结论与人工计划 Gate

用户确认后按分期派发；每期容器验收（含真实进程冒烟与截图）、job 评论留痕，发版时前端 + 主机 server + 容器 CLI 同步升级。
