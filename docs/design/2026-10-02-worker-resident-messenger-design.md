<!-- template_id: design; template_version: 1.1.1 -->
# worker 侧常驻传话人与小修收尾（N 批）

> 状态：Approved（Draft 0.1；用户 2026-10-02 在 web 中继确认 N1 + N2）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-02 | Claude | N1 三项小修；N2 常驻传话人扩展到 worker |

## 背景

v0.90 的常驻传话人（一个 runner 一个 `claude -p --input-format stream-json` 进程，逐条转发，空闲退出）只在 server 本机 runner 生效。用户的 Claude 终端会话基本都在容器 worker `w-docker-claude` 上，从 web 给它们发消息仍是每条一个一次性 messenger exec job（冷启动约 10s）。

## 已确认事实（2026-10-02）

- 常驻传话人实现在 `internal/httpapi/session_inject.go`（`residentMessengerManager` / `residentMessengerProcess`），属于入口层，违反 G021；worker 也不能依赖入口层（G022）。
- 远程 runner 的传话走一次性 exec job（`claude -p … --allowedTools SendMessage,ListAgents`）派发到该 worker。
- worker 协议当前 v13（持续会话）；新增可选能力按 `SupportsXxx(proto)` 门槛，旧 worker 不静默降级。

## N2 方案

1. **下沉**：把常驻传话人（进程管理、stream-json 逐条写入与 result 解析、空闲退出、失败回退判定、状态）从 `internal/httpapi` 抽到独立包（建议 `internal/messenger`），零行为变化（G023），server 本机路径改为调用它。
2. **协议 v14（可选能力 `MessengerMinProtocolVersion = 14`）**：传话请求派发给 worker 时，在 dispatch 上带"传话"标记与载荷（目标会话名、原文、cwd、超时），不再发裸 exec 命令；worker 收到后交给本机的 `internal/messenger` 常驻进程处理，结果（送达 / 失败原因）作为该 job 的结果返回。worker 侧剔除继承的 `CLAUDE*` 会话标记（沿用默认 env 剔除清单）。
3. **兼容**：worker 协议 < v14 时，server 继续派一次性 messenger exec job（与现在相同）；不新增兼容分支以外的旧路径。

兼容期限：v13 worker 忽略 additive `dispatch.messenger` 并执行同一请求携带的 `Cmd`；代码以 `DEPRECATED(v0.92): remove in v0.95` 标记，v0.95 删除该回退。
4. **可观测**：`gofer worker show` 与 Runners 页显示该 worker 的常驻传话进程状态（无 / 空闲 / 处理中），数据经 worker 上报。
5. **配置**：沿用 `server.session_messaging` 的 `messenger_command` / `messenger_timeout_sec` / `messenger_idle_sec`，随 dispatch 下发；worker 无需本地配置。

## N1 小修

- `tools-18z`：手机上「会话等待回复」提示条（InteractionToast）不遮住输入框：手机宽度改到顶部（顶栏下方），可上滑 / 点 × 关闭；桌面不变。
- `tools-v86`：Sessions 简洁表单新建 ACP 会话后打开会话对话（抽屉 / 工作台线程），不跳 job 详情；会话已开始后输入框占位为"下一条消息"；「最后一条消息」展开后按 markdown 渲染（与轮次气泡一致，保留复制原文）。
- `tools-bpl`：serve 的运行时目录（`run/`：日志、pid）跟随 `-c` 指定配置文件所在目录（未指定 `-c` 时维持现状），避免临时 serve 写进正式运行目录；补测试与文档。

## 测试与验收

固定测试（实施时先红后绿）：`TestResidentMessengerPackageParity`（下沉后行为一致）、`TestWorkerMessengerDispatchUsesResidentProcess`、`TestMessengerFallsBackToExecForOldWorker`、`TestServeRuntimeDirFollowsConfigFlag`，以及 web Vitest。真实验收由监督者在容器内进行：临时 serve + 临时 worker（v14），给同 worker 上的真实 Claude 会话连续发两条，只起一个传话进程且逐条有结果；v13 worker 走一次性 job。
