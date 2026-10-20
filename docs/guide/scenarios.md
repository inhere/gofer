# 常见用法

七个能直接照着做的场景。命令里的 `<project>` / `<worker-id>` / `<server>` / `<id>` 换成你自己的值；flag 细节以 `gofer <命令> -h` 为准。

前提：已经有一个 server 在跑（`gofer serve`），并且你的终端能连上它——在 server 本机直接跑 CLI 即可；从别的机器或容器提交，先 `gofer init client` 写好 `.env`（`GOFER_SERVER_ADDR` / `GOFER_SERVER_TOKEN` / `GOFER_RUN_MODE=client`），见 [configuration.md](configuration.md)。

## 1. 在主机上跑一条命令

最常见的用法：把构建、测试、`git pull`、巡检这类命令交给 server 本机执行，日志和状态留在 gofer 里。

```bash
# 一条短命令：--sync 等到终态（默认最多等约 30 秒）
gofer job run -p <project> -a exec --title "看下 Go 版本" --sync -- go version

# 长一点的活：不加 --sync，用 watch 跟到结束
gofer job run -p <project> -a exec --title "全量测试" --timeout 1800 -- go test ./...
gofer job watch <id>

# 只取日志
gofer job logs <id> --stderr --tail -n 50
```

要点：

- 命令放在 `--` 之后，gofer **不经过 shell** 执行它，所以管道、重定向要自己包一层（Linux/macOS 用 `bash -lc '…'`，Windows 用 `cmd /c "…"`）。
- 工作目录用 `--cwd <相对项目根>`，不要在命令里 `cd` 绝对路径——job 可能被派到别的机器执行，路径按那台机器的项目根解析。
- 每个 job 带 `--title "…"`，否则 `job list` 里认不出谁是谁。
- `--sync` 只回状态不回输出；输出用 `job logs`。

**Web 入口**：Jobs 页（`/jobs`）看列表，点进去看日志、事件、用量、提交。

## 2. 派 agent 改代码并验收

把“读代码、改代码、跑测试”交给 agent，并且要求它做完停在验收台等你确认——agent 说做完了不算数。

```bash
gofer job run -p <project> -a claude --title "修登录超时" \
  --prompt "修复 issue <issue-id>：登录偶发 401 超时，先找复现路径再改" \
  --acceptance "- 复现步骤与结论写在汇报里
- 新增/修改的测试通过" \
  --scope 'src/auth/**,src/login/**' \
  --verify 'go test ./src/auth/...' \
  --worktree --review --timeout 2400

gofer job watch <id>          # 跟在跑的过程
gofer job review <id>         # 一屏看完：汇报、验收标准、提交、diff、验证结果、用量
```

几个关键开关：

- `--acceptance` / `--scope` 会作为「验收标准」「交付约定」注入 prompt，同时在验收面板里逐条对照；超出 `--scope` 的改动会被标成「范围外」。
- `--verify '<命令>'` 在 agent 结束后于**执行那台机器**上跑，非 0 退出即失败。
- `--worktree` 在托管 worktree（分支 `gofer/<job-id>`）里干活，不碰你的主工作区；确认后用 `gofer job deliver <id>` 合并回主线（或推分支 / 开 PR），或 `gofer job worktree rm <id>` 丢掉。
- `--read-only` 用于只审查不改代码；`--model` 换模型；`--max-tokens` / `--max-cost` 设上限。

验收：

```bash
gofer job accept <id>                          # 通过
gofer job reject <id> --note "改动越界" --resume   # 驳回，并让它按意见继续
gofer job findings <id> --create-issues        # 把「发现但不碰」逐条建成 issue
gofer job comment <id> "验收：…"                # 留一笔
```

**Web 入口**：验收台 `/review`（所有待验收 job，等最久的在最前，行内通过 / 驳回），或 job 详情页页首的验收面板。

## 3. 用 plan 串多步任务

一件事需要几个步骤、可能几个不同 agent 时，把它写成 plan 的 todo 链：前一项完成才派下一项，中间某步失败就停在那里等人。

```bash
gofer plan create --project <project> --title "修掉登录超时" --desc "目标 / 范围 / 验收"

gofer plan add-todo <plan> "先读代码与日志，给出方案" --assign claude --read-only

gofer plan add-todo <plan> "按方案实施" --assign claude --after prev \
  --acceptance "- 超时时间可配" --scope 'src/auth/**' --review

gofer plan add-todo <plan> "跑测试" --assign exec --cmd 'go test ./...' --after prev

gofer plan run <plan>          # 开始推链：可以跑的项立刻出 job
gofer plan show <plan>         # 进度、每项的 job 与用量
gofer plan pause <plan>        # 有事要先停一下
```

- 不带 `--assign` 的 todo 只是清单项，人自己推进（`gofer plan set-todo <todo> --status done --note "结论"`）。
- 同一块代码的连续步骤可以 `--continue prev`，直接在前一项的 agent 会话里继续，省掉重新读代码。
- 需要人拍板的分叉：`gofer plan ask --plan <plan> --title "方案选择" --question "选 A 还是 B" --option A --option B`，在 Web 或手机上作答；作答前链停在这一步。
- 想让 agent 自己写方案再转成 todo：`gofer plan import <plan> --from-job <id>`。

**Web 入口**：Plans 页（`/plans`）是看板，拖到 `ready` 即派发、拖到 `done` 即人工标记；plan 详情里有 todo 进度、用量、决策作答、主会话面板。

## 4. 从 Web 接管 / 回复终端会话

你在终端里跑 Claude Code / Codex，人可以走开：会话停下来时在 Web 上等你回复，回到终端按 Esc 或直接输入即放行。

```bash
gofer init hooks --agent all --global      # 一次性安装：会话登记 + 开场注入
gofer session relay on                     # 本会话：每次停下都在 Web 等回复（auto = 只在人不在时才等）
gofer session ls                           # 谁在等回复
gofer session say <sid> "改 B 方案"          # 也可以在 CLI 代答
```

- 人走了想让它继续：`gofer session say <sid> "…" --deliver` 可以送进空闲会话（经 tmux）；`gofer session resume <sid>` 唤醒已经结束的会话（在 Web 工作台里 attach），`gofer session nudge <sid> --every 30m -m "进度如何"` 定时催办。
- 会话里出现的确认框 / 选择题（Claude Code 的权限提示、`AskUserQuestion`）也会镜像到 Web 上等你答。
- 你已经离开键盘，任务里又出现不可逆操作（`git push`、发版）：让 agent 用 `--hold` 提交，job 停在“等批准”，你在手机上批准它才真正执行。

**Web 入口**：Sessions 页（`/sessions`，等回复的置顶，可直接切 relay 开关、回复、送入终端、唤醒）与工作台（按会话看对话）。首页「今天」里所有等你回复的会话也会作为卡片出现。

## 5. 在手机上处理待决策

gofer 的首页是**以决策为中心**的：所有在等你的东西汇成一个队列——待批准的 job、等人回答的交互、等回复的会话、待验收的 job、阻塞的 plan、待指派的工作项。

用手机访问 `https://<server>:<tls 端口>/`：首页「今天」就是那个队列，卡片上直接批准 / 回复 / 通过 / 附意见重跑，一次一张的「专注处理」更长按操作；窄屏是贴底面板。

要装成手机 App（PWA）并接收 Web Push，需要 HTTPS：

```bash
gofer tool cert --hosts <server-ip>      # 生成自签 CA 与证书到 <config-dir>/certs
# 在配置里开 server.tls（addr / cert_file / key_file），然后 gofer serve reload
```

步骤见 [`docs/runbook/https-pwa.md`](../runbook/https-pwa.md) 与 [`docs/runbook/web-push.md`](../runbook/web-push.md)。不用 HTTPS 也能用浏览器看和点，只是不能安装、收不到推送。

**Web 入口**：`/today`；顶栏「待我决策 N」浮层在任何页面都能打开。

**截图发给会话**：在工作台或会话抽屉的输入框里点 📎（手机上可以直接拍照或选相册），也可以粘贴、拖放图片，然后照常发送。持续 ACP 会话且 agent 支持图片时，图片随消息直接发出；终端里的 Claude Code / Codex 会话收到的是执行机上的图片路径（`[附图 n] <绝对路径>`），由 agent 自己打开。收件方读不了图片时 📎 置灰并写明原因。细节见 [`skills/gofer-usage/references/web-console.md`](../../skills/gofer-usage/references/web-console.md)。

## 6. 给另一台机器加一台 worker

想把活派到有 GPU 的机器、某个容器、或者和代码同处一台的开发机上。

```bash
# ① server 上（管理员 token）：登记 worker，打印一次性 token
gofer worker add <worker-id> --project <project>

# ② worker 机器上：一条命令完成初始化（拉项目、推断 roots、探测 agent、写 worker.yaml、自检）
gofer worker init --server http://<server>:8765 --id <worker-id> --token <一次性 token>
gofer worker -d
```

或者反过来——让 worker 自己申请加入（需在配置里开 `server.worker_enrollment.enabled`）：

```bash
# worker 机器上：终端显示 8 位配对码
gofer worker join --server http://<server>:8765 --id <worker-id> --start
# server 的 Web → Runners → 「待接受」：输入那个配对码并选项目，接受后 token 自动落到 worker 机器
```

检查：

```bash
gofer worker ls                                  # server 上看到它
gofer worker show <worker-id>                     # 协议、能力、策略状态
gofer worker projects <worker-id>                 # 它在哪些项目里能干活
gofer worker doctor                               # 在 worker 机器上自检
gofer job run -p <project> -a exec --runner <worker-id> --sync -- uname -a
```

**Web 入口**：Runners 页（`/runners`）——在线状态、心跳、在跑 job、版本、工作目录映射，也能在这里升级 worker、接受申请。

## 7. 定时任务

按 cron 或一次性时间点重复提交同一个 job。

```bash
# 每天 03:00 跑测试
gofer schedule add --name nightly-tests -p <project> -a exec \
  --cron "0 3 * * *" --timeout 3600 -- bash -lc 'go test ./...'

# 半小时后跑一次
gofer schedule add --name once -p <project> -a codex --delay 30m --prompt "巡检依赖更新"

# 到点跑；也可以立刻试跑一次 / 暂停
gofer schedule ls
gofer schedule run <id>
gofer schedule disable <id>
```

- `--cron` 是标准 5 段 cron；`--at` 指定 RFC3339 或 unix 秒；`--delay` 是相对时长，三者选一个。
- 加了 `--webhook` 会拿到一个 `trigger_token`，外部可以 `POST /v1/schedules/{id}/trigger` 触发；`gofer schedule rotate-token <id>` 换 token。
- `--catch-up`（默认开）让 server 在重启错过一次触发后补跑一次。

**Web 入口**：Schedules 页（`/schedules`，标题栏「+ 新建」），可看上次 / 下次运行、启停、立即运行。

## 还有这些入口

- **MCP**：`gofer mcp` 是 stdio MCP server，把上面这些能力以 `gofer_*` 工具暴露给 agent（配置片段见 [features.md](features.md)「接入入口」；agent 侧用法见 [skill](../../skills/gofer-usage/SKILL.md)）。
- **HTTP**：`/v1/*` + `Authorization: Bearer <token>`，端点见 [`docs/reference/http-api.md`](../reference/http-api.md)。
- **文件搬运**：`gofer tool cp <本地路径> <runner>:<project>/<相对路径>`（runner 可以是 `server` 或某台 worker）；job 也能 `--upload` / `--collect`。
