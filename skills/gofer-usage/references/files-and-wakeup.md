# 传文件与登记唤醒

> 两件「别用土办法」的事：把文件搬到执行机（不要 base64 塞进 job 日志），以及让 agent 登记等待后结束、条件到达时自动续投（不要让 job 常驻轮询）。

## 目录

- [传文件（tool cp）](#传文件tool-cp)
- [让 job 自己带文件：--upload / --collect](#让-job-自己带文件--upload----collect)
- [传输约定与失败原因](#传输约定与失败原因)
- [登记唤醒：gofer job wakeup](#登记唤醒gofer-job-wakeup)
- [唤醒的语义与边界](#唤醒的语义与边界)

## 传文件（tool cp）

```bash
gofer tool cp ./firmware.bin <worker-id>:<project>/tmp/in/firmware.bin   # 推到 worker 的项目目录
gofer tool cp <worker-id>:<project>/tmp/out/report.csv ./report.csv      # 从 worker 拉回
gofer tool cp ./x.tar server:<project>/tmp/x.tar                         # 目标是 server 本机（local 同义）
gofer tool cp ./x ./y --force                                            # 目标已存在必须 --force
gofer tool xfer ls [--state staged|dispatched|done|failed|expired] [--runner <id>]   # 暂存区
gofer tool xfer show <id>                                                # 单条详情（含失败原因）
gofer tool xfer rm <id>                                                  # 立即删掉暂存文件与记录
```

恰好一端是远端，写法 `<runner>:<project>/<项目内相对路径>`，runner 填 worker id 或 `server`。

`xfer show` 在执行机回报了绝对路径时多一行 `abs_path`（推送完成时由执行机报它自己盘上的真实路径，例如 `/home/u/proj/.gofer/attachments/20261019/xf-abc12345.png`）。拉取、失败的传输，以及版本较旧的 worker 不报这个字段：那时只有 `path`，按项目根目录拼。目标不是项目时多一行 `target: attachments`：那是往执行机自己的附件目录（`<该机 config-dir>/run/attachments/`）放文件，`path` 相对它、`project` 为空（工作台给无项目的会话送图就走这条路，需要 worker 协议 v27 以上）。

## 让 job 自己带文件：--upload / --collect

更省事的是让 job 带着文件跑：

- `gofer job run … --upload ./a.bin:tmp/in/a.bin`（可重复）：提交前把本地文件暂存到 server，执行机在 agent **开跑前**放到目标（按 job 的 cwd 解析）；放不下即 job `failed`、agent 不启动。
- `gofer job run … --collect 'tmp/out/*.csv'`（可重复）：job **结束后**（失败也收、verify 之后）按 glob 在 cwd 里收集，落进该 job 的产物 `collected/<项目内相对路径>`，web 详情页可直接看和下载。
- 限额：单文件 ≤ `server.xfer.max_bytes`（默认 256MB），单个 job 收集总量 ≤ `server.xfer.collect_max_bytes`（默认 1GB）；超出的跳过并记在该 job 的 xfer 统计里。

## 传输约定与失败原因

- 路径按**执行机**的项目根解析，必须在项目根内（与 `job run --cwd` 同一边界）；目标目录不存在会自动创建。
- 只传单文件、不续传。传目录先打包：`tar czf x.tgz dir/` 或 Windows `Compress-Archive -Path dir -DestinationPath x.zip`，传过去再解。
- 推送前先预检（runner 在线、项目、路径、大小），通过才上传；两端都校验 sha256，拉取落本地前先写临时名再改名。
- 执行机的项目附件默认放在项目根的 `.gofer/attachments/<YYYYMMDD>/`；显式配置 `exchange_subdir` 的项目仍放在对应交换目录的 `gofer-attachments/`。每次往新目录放文件时，gofer 同时清理新旧两处位置中 7 天前的日期子目录（只删这一层，其他名字不会动）。
- 失败原样打印、退出码 1：`exists`（加 `--force`）、`path escapes project`、`worker offline`（不排队，重跑即可）、`too large`；worker 侧单次传输默认 600 秒超时（worker 配置 `xfer_timeout_sec`）。
- **别传 `.env` / 私钥 / token**：gofer 不做内容审查。
- **每条传输属于发起它的 caller**：非 owner、无 `schedule.manage.others` 的 `ls` 只列出自己的传输，`show`（读单条）/ `rm` / 下载他人 `get` 的结果都 403；换 token 后看不到之前那条（用带 `xfer.manage.others` 的 token 或切回原 token）。worker 自用的 `PUT .../content`（上传 get 结果）/ `GET .../content`（拉 put 源）不受影响，仍限分配给它的那条。
- 更懒的替代：文件就在 worker 的项目目录里且内容很小，让 job 把内容 `cat` 进 stdout 即可（≤32KB 的 `result.json` 也会随结果回传）。

## 登记唤醒：gofer job wakeup

「等 verify 出结果 / 等人回复 / 每小时看一眼」不必让 job 常驻轮询：**登记一条唤醒再结束**。条件到达时 gofer 自动起一次**续投**（有会话就续同一会话，没有就用原请求 + 指令重跑），把登记时写的指令当提示词。agent 在 job 内用 `$GOFER_JOB_ID` 指自己。

```bash
gofer job wakeup create "$GOFER_JOB_ID" --kind at --after 10m -m "检查 CI 结果并汇报"        # 或 --at <RFC3339>
gofer job wakeup create "$GOFER_JOB_ID" --kind every --every 1h -m "巡检一次新失败的 job"      # 间隔 ≥ 60s
gofer job wakeup create "$GOFER_JOB_ID" --kind cron --cron '0 9 * * 1-5' --tz Asia/Shanghai -f instr.md
gofer job wakeup create "$GOFER_JOB_ID" --kind event --event job.terminal --job-id "$OTHER" --status done -m "对方结束了，合并结果"
gofer job wakeup create "$GOFER_JOB_ID" --kind event --event interaction.answered -m "人回复了，按答复继续"

gofer job wakeup list "$GOFER_JOB_ID"        # 还有几条在等、触发了几次
gofer job wakeup show|enable|disable|rm <wid>
```

`-m` 与 `-f`（从文件读指令）互斥；cron 时区缺省 = 服务器本地时间。`gofer job show <job>` 有一行 `wakeups:` 摘要。

## 唤醒的语义与边界

- **事件**：`--event` 缺省监听自己（`--job-id` 指定别的 job）。可用事件只有：`job.terminal`（可 `--status done,failed,…` 过滤）、`job.verify_finished`、`job.needs_review`、`job.reviewed`、`job.fell_back`、`job.stalled`、`interaction.answered`、`session.takeover_released`；写错直接 400。用 `--job-id` 监听另一个 job 的 `job.terminal` 时，如果它已经结束（且符合 `--status`），注册后立即触发一次，不会空等到过期。
- **不叠 job**：同一条唤醒同一时刻只允许一个未结束的续投，期间再触发只累计合并次数；前一个结束后下一次触发才再起。
- **次数**：`--mode once`（at / event 默认）触发一次即消费；every / cron 默认 `continuous`。定时器从创建 / 启用时起算，**不补发**错过的周期。
- **过期**：默认 7 天（server `wakeup.ttl_sec`），到期自动停用；目标 job 被清理时唤醒一并删除。
- 没有会话的 job（exec、agent 没有续接模板）走重跑：原请求重放，指令追加到 prompt 末尾（exec job 只记事件）。
- 权限同 `job resume`：只能给自己提交的 job（或有回答权限的 caller）登记。
- 续投 job 带 tag `wakeup:<wid>`；触发、合并、过期、失败都记成目标 job 的事件，失败不会静默。默认通知集不含这些事件，要 IM 提醒就显式订阅 `job.wakeup_*`。
- MCP：`gofer_wakeup_create` / `gofer_wakeup_list` / `gofer_wakeup_disable`。web job 详情页的「唤醒」块可看列表、开关、触发历史、直接新建。

## 常驻 job 的子 job 回执

常驻（`--session`）job 用自己的 job 凭据派子 job 时，gofer 会自动登记一条回执：子 job 有结果时给父 job 开新一轮（`job say`），不需要外部客户端盯着。

```bash
# 父 job：有提交权限（agent 或 role 上 can_submit: true）才会自动登记
#   启动时设够长的空闲时限；默认 1800 秒，子 job 比它长就送不到
gofer job run -p <project> -a <agent> --session --idle-timeout 86400 \
  --shared-dir --prompt "监督这件事：派子 job，然后结束本轮"

# 子 job：正常提交即可；会写文件就加 --worktree
gofer job run -p <project> -a <agent> --worktree --prompt "做第一部分"
```

- **父 job 一句就够**：拿到 `can_submit` 的常驻 job，prompt 末尾会自动带一段「## 派发子 job」提示——派完子 job 就结束本轮、不要轮询；`--idle-timeout` 设够长；会话用 `--shared-dir`（常驻会话默认整段生命周期独占目录，同目录的子 job 会排在它后面）；会写文件的子 job 用 `--worktree`；并发槽位留余量（槽位上限为 1 且被父 job 占着时，子 job 要等会话结束）。用空 prompt 启动的常驻 job 不注入。
- **回执内容**：只含子 job id、状态、退出码和查看命令（`gofer job logs <子 job>`），不含标题和输出——父 job 自己去读日志。子 job 还没结束时只写状态（`running` / `pending_interaction`），不写退出码：运行中的 job 没有结果可读。
- **回执哪些事件**：默认 `job.terminal` + `job.needs_review`。server 配置 `wakeup.child_events` 改全局默认，单次提交用 `job run --parent-wake-events job.terminal,interaction.created` 覆盖；`job.terminal` 始终包含（写别的会补上，未知值直接拒绝）。可选项：`job.awaiting_approval`（hold 待批准）、`interaction.created`（工具审批与提问）。父 job 没有权限代答或批准，「需要输入」类回执要转告人；同一子 job 的这类回执最多 1 分钟一条。窗口中同一条审批被人答了、子 job 又跑起来且当下不需要人时，这条待送回执直接作废（不发、不占回合），子 job 结束时照常回执。
- **关掉回执**：`job run --no-parent-wake`（这个子 job 不登记）。
- **待送**：父 job 这一轮还没结束时触发，回执先记为「待送」，等它回到空闲再送，多个子 job 合并成一条。`job wakeup list` / `job wakeup show`（以及 HTTP / MCP 的 `pending_delivery`）会显示「待送」，不要把它当成 job id。`job.terminal` 回执送达后该自动回执即停用。
- **父 job 先结束**（空闲超时、被人 `job end`、失败、进入待验收）：回执送不到，gofer 记 `job.child_wake_failed` 并通知人（webhook / 桌面 / Web Push / 收件箱「待我处理」），不会复活父 job。所以监督类 job 必须显式设长 `--idle-timeout`。
- **子 job 被接管或重试**：回执跟到真正承载结果的那个 job；重试还在退避期时父 job 不被打扰，重试被取消时补发。
- **权限**：手动 event wakeup 挂在活着的常驻 job 上时，它的 `-m` 指令会被 say 进那个会话，所以除 `job.wake.others` + `job.submit` 外还要求对该 job 的 say 权限（`job.manage.others`）。定时类 wakeup、已结束的常驻 job 不变。
- **降级提醒**：回退到「待送」功能之前的版本前，先确认没有待送回执（`job wakeup list` 里没有 `pending delivery`）；旧版本不会释放它，还会把内部的 `await_input` 显示成 job id。
