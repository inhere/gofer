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

## 让 job 自己带文件：--upload / --collect

更省事的是让 job 带着文件跑：

- `gofer job run … --upload ./a.bin:tmp/in/a.bin`（可重复）：提交前把本地文件暂存到 server，执行机在 agent **开跑前**放到目标（按 job 的 cwd 解析）；放不下即 job `failed`、agent 不启动。
- `gofer job run … --collect 'tmp/out/*.csv'`（可重复）：job **结束后**（失败也收、verify 之后）按 glob 在 cwd 里收集，落进该 job 的产物 `collected/<项目内相对路径>`，web 详情页可直接看和下载。
- 限额：单文件 ≤ `server.xfer.max_bytes`（默认 256MB），单个 job 收集总量 ≤ `server.xfer.collect_max_bytes`（默认 1GB）；超出的跳过并记在该 job 的 xfer 统计里。

## 传输约定与失败原因

- 路径按**执行机**的项目根解析，必须在项目根内（与 `job run --cwd` 同一边界）；目标目录不存在会自动创建。
- 只传单文件、不续传。传目录先打包：`tar czf x.tgz dir/` 或 Windows `Compress-Archive -Path dir -DestinationPath x.zip`，传过去再解。
- 推送前先预检（runner 在线、项目、路径、大小），通过才上传；两端都校验 sha256，拉取落本地前先写临时名再改名。
- 失败原样打印、退出码 1：`exists`（加 `--force`）、`path escapes project`、`worker offline`（不排队，重跑即可）、`too large`；worker 侧单次传输默认 600 秒超时（worker 配置 `xfer_timeout_sec`）。
- **别传 `.env` / 私钥 / token**：gofer 不做内容审查。
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

- **事件**：`--event` 缺省监听自己（`--job-id` 指定别的 job）。可用事件只有：`job.terminal`（可 `--status done,failed,…` 过滤）、`job.verify_finished`、`job.needs_review`、`job.reviewed`、`job.fell_back`、`job.stalled`、`interaction.answered`、`session.takeover_released`；写错直接 400。
- **不叠 job**：同一条唤醒同一时刻只允许一个未结束的续投，期间再触发只累计合并次数；前一个结束后下一次触发才再起。
- **次数**：`--mode once`（at / event 默认）触发一次即消费；every / cron 默认 `continuous`。定时器从创建 / 启用时起算，**不补发**错过的周期。
- **过期**：默认 7 天（server `wakeup.ttl_sec`），到期自动停用；目标 job 被清理时唤醒一并删除。
- 没有会话的 job（exec、agent 没有续接模板）走重跑：原请求重放，指令追加到 prompt 末尾（exec job 只记事件）。
- 权限同 `job resume`：只能给自己提交的 job（或有回答权限的 caller）登记。
- 续投 job 带 tag `wakeup:<wid>`；触发、合并、过期、失败都记成目标 job 的事件，失败不会静默。默认通知集不含这些事件，要 IM 提醒就显式订阅 `job.wakeup_*`。
- MCP：`gofer_wakeup_create` / `gofer_wakeup_list` / `gofer_wakeup_disable`。web job 详情页的「唤醒」块可看列表、开关、触发历史、直接新建。
