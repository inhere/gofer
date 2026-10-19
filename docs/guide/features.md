# 功能一览

按主题列出 gofer 提供什么，每条给一句用法。完整的使用参考是 [gofer-usage skill](../../skills/gofer-usage/SKILL.md)（面向 agent，随每个用户可见改动同步）及其 [命令参考](../../skills/gofer-usage/references/commands.md)；flag 一律以 `gofer <命令> -h` 为准，HTTP 端点见 [`docs/reference/http-api.md`](../reference/http-api.md)。

哪些部分已经稳定、哪些还在演进，见 [stability.md](stability.md)。

## Job

- 提交方式：CLI 参数、带 YAML frontmatter 的 markdown 任务文件（`-f task.md`），或 server 渲染的任务书模板（`-t <name> --var k=v`、`gofer template ls|show`）。
- 同步或异步（`--sync`、`--wait`），`--read-only`、`--model`、`--max-tokens / --max-cost / --max-turns`、`--timeout`、`--retry`、`--fallback`、`--env`、`--upload` / `--collect`。
- 隔离：同目录锁（`--lock`、`--shared-dir`、`--exclusive-dir`、`--lock-wait`），托管 worktree（`--worktree`、`gofer job worktree ls|merge|rm`）。
- 续跑：`job resume`（同一 agent 会话，`--mode`，会话族内用 `--agent` 换 agent）、`job rerun`、`--from-session`、供应商类错误自动续投、故障转移到备选 agent。
- 交互：`--interactive` pty job 可在浏览器 attach；`--session` 开 ACP 持续会话。
- 唤醒：`gofer job wakeup create` 按定时或事件续投一个已结束的 job。

## 验收与质量关口

- `--verify '<cmd>'`：agent 结束后在执行机上跑检查，非 0 退出即失败；`--review`：job 停在 `needs_review`，只有人能接受。
- `--hold [--hold-reason "…"]`：job 在**执行前**停在 `awaiting_approval`，用于 agent 被自身权限拦下的对外 / 不可逆操作（如 `git push`）。人在 job 页（手机可用）一键批准，或 `gofer job approve <id>`；拒绝或超时即取消，命令不跑。agent 不能批准自己提交的待批 job。
- `--acceptance` 验收标准与 `--scope` 改动范围进入 prompt 和验收面板；范围外的发现可用 `gofer job findings [--create-issues]` 转成 issue。
- 未提交改动守卫（`on_uncommitted`）、ACP 工具调用审批门（项目 `approval`）、强制规则（`gofer agent rule`）与 skill 绑定（`gofer agent skill`）。

## 编排

- plan 与 todo：`gofer plan create|add-todo|set-todo|run|pause|resume|dispatch|import`，plan 交接说明，决策（`plan ask`、MCP `gofer_ask_human`），Web 看板视图。
- workflow：`gofer workflow run|show|pick|template`，多 agent 对比与择优合并。
- 定时任务：`gofer schedule add|run|enable|disable`，支持 cron 或一次性定时，可选 webhook 触发。
- 评论中 `@agent` 提及即派发 job（人的评论才会触发），可按 plan 开启 leader 回合。

## 会话与人在回路

- 终端会话中继：`gofer init hooks` 之后，停下的 Claude Code / Codex / omp 会话可以在网页上等待回复（`gofer session relay auto|on|off`、`session say`），也可以被重新唤醒（`session resume`）或定时催办（`session nudge`）。
- Claude Code 的权限确认与选择题镜像到网页作答；工作台里可以看 ACP 与 pty 会话；借助可选的 HTTPS 监听（`server.tls`、`gofer tool cert`）安装 PWA 并接收 Web Push。
- 「今天」首页：一个队列列出所有等你决定的事（交互、plan 决策、待验收、待批准、工作项、待指派信件），并行泳道、延后与专注模式。
- 工作项与管家：`gofer work …` 跟踪你跨会话在做的事；可选的管家（`gofer steward …`）以受限凭据帮你整理。随手记与收集箱：`gofer work capture "…"`，之后分派成会话消息 / job / todo / issue。
- 信箱：`gofer session mail send|ls|read|reply|cancel`，人与会话、job 之间发信；Web `/mail`。

## 仓库 tracker 与 agent 记忆

- `gofer repo init|status|prime|sync|migrate`、`gofer issue …`、`gofer memory …`——issue 与记忆存在 `.gofer/tracker/*.jsonl` 随代码提交，另有存在 server 上的全局与项目记忆。
- `repo prime`（安装为 SessionStart hook）给新会话当前重点、进行中的 issue、规则和记忆索引；`gofer issue brief` / `gofer plan brief` 一条命令交接任务。
- `memory flag` 上报过时记忆，`memory doctor` 体检，`memory candidates|accept|reject` 把交付 job 中的可复用经验转成记忆。
- tracker 的 JSONL 有 git 合并驱动（`gofer repo merge-driver`），多个分支各自改 issue 也能自动合并。

## Worker、网络与运维

- Worker：`gofer worker add|init|doctor|show|projects|reload|upgrade|remove`，LEGACY 与 POLICY 两种配置模式，断线恢复；自助注册（`gofer worker join`，默认关闭）。
- 隧道：`gofer tunnel forward|check|ls|save|stop` 在 worker 白名单内经 worker 转发 TCP/UDP（含 SOCKS / HTTP CONNECT 动态转发）；预设存在 server，也可由 server 托管转发。
- 文件：`gofer tool cp <src> <runner>:<project>/<path>`（双向）与 `gofer tool xfer`（暂存区管理）；job 也能 `--upload` / `--collect`。
- 受管 server：`gofer serve register|start|stop|restart|status|logs|upgrade|uninstall`（Windows 登录任务或 Linux systemd 单元），`gofer serve reload` 热重载配置。

## 观测与安全

- 事件时间线、diff 与提交、每个 job 的用量与成本、统计看板（`/dashboard`）、Prometheus `/metrics`、带轮转与脱敏的 JSONL 文件日志。
- 通过 webhook 及钉钉 / 飞书适配器发送通知（`server.notification`）；事件钩子（`server.event_hooks`）与决策 hook（`server.decision_hooks`）。
- job 作用域凭证、带可选能力位的 caller token 与调用方上限、会话临时授权、`gofer job redact`、`job delete`、`job secret-scan`。
- 详见 [security.md](security.md)。

## 接入入口

- **CLI**——命令按 `gofer -h` 分组：Setup & config / Repository tracking / Control plane / Jobs & workflows / Utilities。
- **HTTP**——`/v1/*` 需要 `Authorization: Bearer <token>`；`/health` 与 Prometheus `/metrics` 不在其内（`/metrics` 可单独配 token）。
- **MCP**——`gofer mcp` 是 stdio MCP server，默认转发到 `GOFER_SERVER_ADDR` 指向的 server（`--standalone` 进程内执行，`--project` 限定项目）：

  ```json
  { "mcpServers": { "gofer": { "command": "/abs/path/to/gofer", "args": ["mcp"] } } }
  ```

- **Web 控制台**——内嵌在 `gofer serve` 中（`--no-web` 关闭）：今天、工作台、Works、Plans、Jobs、Sessions，其余在「更多」下拉（Issues、Dashboard、Agents、Runners、Projects、Workflows、Schedules），加设置、`/mail`、`/review`。
- **会话 hook**——`gofer init hooks` 把 Claude Code / Codex / omp / jcode 的会话登记进来（中继 + 记忆注入），用的也是同一个 HTTP 入口。

## 输出与产物

- job 的 stdout / stderr 落在项目自己的 `tmp/gofer/<job-id>/`（可用 `storage.root` / 项目 `exchange_subdir` 改），数据库里存状态、事件与索引；日志可以按 retention 自动清理。
- Claude Code / Codex 这类 NDJSON 输出的 agent，stdout 只留最终答复、过程事件进 stderr，Web 上是结构化视图。
- job 结束时可以把 cwd 里的文件按 glob 收进 artifacts（`--collect`），也可以从别的机器 `tool cp` 拉下来。
