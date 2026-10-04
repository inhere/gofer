# M 批：传话人与会话唤醒可见性 + runner 工作目录（计划）

分支 `m-batch`。按功能点小提交，每步全量质量门（gofmt / build / windows build / vet / test）。

## ListAgents 调研结论（先于 M3）

在容器里用真实 `claude -p --output-format stream-json --verbose --allowedTools ListAgents` 抓取：

- 最终 `result` 帧是模型“再转述”的文本（被包进 ``` 围栏），不可靠。
- 可靠来源是 `user` 帧里 ListAgents 的 `tool_result`：`content` 为纯文本，同一份文本也在帧级字段 `tool_use_result.listing`。
- 文本形态：首行 `This session is <名称> [<短id>] — …`，空行，`Peer sessions (N):`，之后每个会话一行
  `  <名称> [<短id>]  ·  <kind>  ·  <idle|busy>  ·  started <相对时间>`。**没有 cwd、没有绝对时间戳**。
- 解析策略：常驻进程读取器记录“最近一次 ListAgents 的 tool_result 文本”，Manager 解析 `Peer sessions` 行（按 `·` 分段，容忍未知段），
  同时始终返回 `raw_output`。无 cwd 时 `cwd` 为空（前端靠短 id / 名称与 gofer 登记会话对照）。

## M1 传话人可见

1. `internal/messenger`：状态机 stopped/idle/busy；`Snapshot(runner)`（status、started_at、last_used_at、idle_deadline、最近 20 次投递摘要、stderr 环形缓冲 4KB）。
   统计独立于进程生命周期（进程退出后历史/stderr 仍可看）。
2. `/v1/runners`：local 行带 `messenger_detail`；worker 在心跳 Ping 里 additive 带 `messenger` 快照（旧 worker 不带 → 前端显示“未知”）。
3. Runners 页：local 与 worker 卡片都显示中文状态；点击打开“传话人”抽屉（状态、最近投递、stderr 尾部、会话列表按钮）。
4. “显示传话 job”开关：Dashboard / Workbench 列表；后端 workbench 的硬过滤改为可选参数。

## M2 唤醒会话

1. 后端 `GET /v1/sessions/{sid}/takeover-plan`（can + 中文 reason + runner/agent/命令摘要）、`POST /v1/sessions/{sid}/resume`（可选 `initial_input`）。
   放开 `ended`；成功后状态 `handed_off`；释放接管时 ended 会话回到 `ended`（不是 idle）。
2. 前端：Sessions 每行（含已结束）“唤醒”按钮（不可用灰显 + title 原因）；SessionDrawer 顶部常驻“唤醒/接管”；Workbench 嵌入同样可用；成功跳 `?attach=1`。
3. CLI：`gofer session resume <sid> [--input ...]`。

## M3 传话人可见会话列表

1. `messenger.Manager.ListAgents`（复用串行锁与 cwd 兜底），解析如上；sessionrelay 接口 + httpapi 实现。
2. `GET /v1/runners/{name}/messenger/agents`（30s 缓存，`?refresh=1`）；worker 路径：`MessengerDispatch.Op`（send|list_agents），
   新增协议能力门槛 `MessengerListMinProtocolVersion`（v16），旧 worker 409 中文提示。
3. 抽屉里“列出可见会话”表格 + 与 `/v1/sessions` 对照 + 一键传话。

## M4 runner 工作目录

1. worker Register / 心跳上报 roots（from→to）；local 汇总各项目 ExecPath；`/v1/runners`、`/v1/workers/{id}` 返回。
2. Runners 卡片“工作目录”折叠区（解析路径、不存在标红、原因取 policy_rejected）。
3. 传话人 cwd 兜底：会话 cwd → runner 第一个可用工作目录 → 家目录。

## 验收

质量门 + 前端 vue-tsc / vitest / vite build（scratch 副本）+ 临时 serve/worker 真实冒烟（390x844 / 1280x900 截图存 tmp/m-smoke）；
G045 同步 skills/gofer-usage 与 README。
