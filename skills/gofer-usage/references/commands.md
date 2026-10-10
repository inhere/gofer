# gofer 命令参考

> 每组命令「是什么 + 常用写法 + 去哪看细节」。完整 flag 一律以 `gofer <cmd> [<sub>] --help` 为准。连接、project key、agent / runner 的通用规则见 SKILL.md。
> 顶级命令分组：配置（`init` `config` `project` `agent` `mcp` `hook`）· 仓库跟踪（`issue` `memory` `repo`）· 控制面（`serve` `worker` `tunnel` `presence`）· 任务（`job` `plan` `workflow` `schedule` `session` `work` `steward` `template`）· 工具（`tool`）。

## 目录

- [job](#job)
- [plan](#plan)
- [workflow（别名 wf）](#workflow别名-wf)
- [issue / memory / repo](#issue--memory--repo)
- [session（别名 sess）与 hook](#session别名-sess与-hook)
- [work（别名 wk）/ steward（别名 stw）](#work别名-wk-steward别名-stw)
- [schedule（别名 sch）](#schedule别名-sch)
- [template](#template)
- [agent](#agent)
- [project（别名 p / proj）](#project别名-p--proj)
- [config（别名 cfg）](#config别名-cfg)
- [init](#init)
- [worker（别名 w）](#worker别名-w)
- [serve（别名 s）](#serve别名-s)
- [tunnel（别名 tun）](#tunnel别名-tun)
- [tool](#tool)
- [mcp 与 presence](#mcp-与-presence)

## job

| 命令 | 用途 | 细节 |
|---|---|---|
| `job run`（别名 `add`） | 提交 | SKILL.md；进阶 flag 见 [job-advanced.md](job-advanced.md) |
| `job logs <id>` / `show <id>` / `watch <id>` | 读输出 / 查状态 / 跟随到结束 | |
| `job list`（`ls`） | 列 job：`-p` `--status` `--tag` `--agent` `--runner` `--plan` `--session` `--since <unix秒>` `--limit` `--all` | |
| `job cancel <id>` | 取消 | `needs_review` 用 reject |
| `job set <id> --title "…"` | 改 / 清空标题 | |
| `job rerun <id>` | 原请求重提（新会话） | |
| `job resume <id> --prompt "…" [--mode …] [--agent …] [--env K=V] [--model …]` | 续跑同一个 agent 会话 | [job-advanced.md](job-advanced.md)「续跑」 |
| `job say <id> "…"` / `job end <id>` | ACP 持续会话下一轮 / 结束 | 「ACP 持续会话」 |
| `job review <id> [--tail N] [--diff]` | 验收材料一屏 | 「人工验收」 |
| `job accept <id>` / `job reject <id> --note … [--resume]` | 人工验收 | |
| `job findings <id> [--create-issues]` | 汇报里「发现但不碰」各条，可逐条建 issue | |
| `job approve <id>` / `job reject <id> [--reason …]` | 待批 job（`--hold`）的批准 / 拒绝，只给人用 | [hold-approval.md](hold-approval.md) |
| `job interactions <id>` / `job answer <id> <iid> <optionId>` | 工具调用审批 | 「工具调用审批」 |
| `job comment <id> "…"` / `job comments <id>` | 留评论 / 看评论（别写 `@名字`） | 「在 job 上留评论」 |
| `job worktree ls / merge <id> / rm <id>` | `--worktree` job 的 worktree | 「并行」 |
| `job wakeup create / list / show / enable / disable / rm` | 登记唤醒 | [files-and-wakeup.md](files-and-wakeup.md) |
| `job retry ls <id>` / `job retry cancel …` | `--retry` 的重试链 | 「故障转移与重试」 |
| `job redact` / `job secret-scan` / `job delete` | 脱敏、跨 job 秘密扫描、删除终态 job | 「脱敏」 |

`job run` 常用 flag：`-p` `-a` `--runner` `--cwd` `--title` `--prompt` `-f` `--sync` `--wait` `--wait-timeout` `--timeout` `--plan` `--todo` `--issue` `--tags` `--env`；进阶：`--session` `--interactive` `--read-only` `--model` `--from-session` `--max-tokens/--max-cost/--max-turns` `--verify` `--review` `--acceptance` `--scope` `--worktree` `--lock` `--lock-wait` `--shared-dir/--exclusive-dir` `--stall-timeout` `--fallback` `--retry` `-t/--var` `--upload` `--collect` `--hold` `--source-session-id` `--rule` `--skill` `--role`。

## plan

```bash
gofer plan create --title "…" [--desc …] [--project p] [--tags …] [--supervisor-session-id <sid> | --no-supervisor]
gofer plan list / show <id> / archive <id> / set-status <id> <status>
gofer plan set <id> [--supervisor-session-id <sid> | --clear-supervisor-session] [--tags …]
gofer plan add-todo <plan> "…" [派发字段] / set-todo <todo> [--status …] [--note …] [--append-note …] [派发字段]
gofer plan run / pause / resume <plan>
gofer plan dispatch <todo>
gofer plan import <plan> (-f plan.md | --from-job <id>) [--assign <agent>] [--dry-run]
gofer plan attach <plan> <job-id>
gofer plan handoff <plan> [--set … | -f …] [--history] [--version N]
gofer plan brief <plan> [--json]
gofer plan ask / decisions / answer          # 决策点（人这一侧）
gofer plan comment <plan> "…" [--todo <todo>] / comments <plan>
```

细节（派发字段、依赖链、方案规则、决策点、主 Agent 会话）见 [plans-workflows.md](plans-workflows.md)。

## workflow（别名 wf）

```bash
gofer workflow run <file.yaml> [-w] | run --template <name> --var k=v [-w]
gofer workflow template ls | show <name> [-p <project>]
gofer workflow list / show <id> / events <id> / cancel <id> / export <id>
gofer workflow pick <id> <step> <fan> [--merge [--squash] [--cleanup-others]]
```

细节见 [plans-workflows.md](plans-workflows.md)「workflow」「多 agent 对比与择优」。

## issue / memory / repo

```bash
gofer issue ready | ls [-l tag] [--stale] | show <id>... | brief <id>
gofer issue create "标题" [--type …] [-p 0-4] [-l a,b] [-d …] [--design …] [--acceptance …] [--parent <epic>] [--from <id>]
gofer issue update <id>... [--claim] [--status …] [--acceptance …] [--clear field,…]
gofer issue comment <id> "…" | close <id>... --reason "…" | reopen <id> | dep add|rm|ls …
gofer memory set <key> "…" [--kind rule|note|handoff] [--summary …] [--tags …] [--when-*] [--global | --project <p>]
gofer memory ls [关键字] | show <key>... | rm <key> | flag / unflag <key> | doctor | archive / restore / promote
gofer memory candidates | accept <id> --key k | reject <id>
gofer repo init | sync | status [--changed] | prime | merge-driver [--install] | migrate --from-bd [--apply]
```

细节见 [tracker.md](tracker.md)。

## session（别名 sess）与 hook

```bash
gofer init hooks [--agent claude|codex|omp|jcode|all] [--global] [-o <dir>] [--remove] [--prime-only] [--force]
gofer session ls | show <id> | relay on|auto|off [--session <id>] | say <id> "…" [--deliver [--takeover]]
gofer session resume <id> [--input …] [--plan] | release-takeover <id> | watch <job-id> | rm <id>
gofer session nudge <id> (--every <dur> | --when-stalled <dur>) -m "…" [--until …] | nudge ls / rm / pause / resume
gofer hook claude|codex|omp|jcode|generic [--agent <key>] [--wait N]   # hook 执行体，由 hooks 配置调用，人不直接用；日志 <config-dir>/run/hook.log
```

细节见 [sessions-relay.md](sessions-relay.md)。

## work（别名 wk）/ steward（别名 stw）

```bash
gofer work ls | show <id> | new | set | note | park | remind | report | link | to-todo | merge | split | summarize | accept | dismiss | requests | digest | rm
gofer steward status | start | restart | stop | ask "…" | notes | review | merges | merge-accept <n> | merge-dismiss <n>
```

细节见 [work-items.md](work-items.md)。

## schedule（别名 sch）

```bash
gofer schedule add <job 请求 flag…> (--cron '*/5 * * * *' | --delay 30m | --at <RFC3339|unix秒>) [--webhook]
gofer schedule list / show <id> / enable <id> / disable <id> / run <id> / rm <id>
gofer schedule rotate-token <id>        # 换 webhook 触发 token（旧的立即失效）；没开 webhook 的计划也可用它开启
```

- `--webhook`：给计划一个外部触发入口（不需要 gofer 的 bearer token）：
  `curl -X POST -H 'X-Gofer-Trigger-Token: <token>' 'http://<server>/v1/schedules/<id>/trigger'`（也可 `?token=<token>`）。语义同 `schedule run`（立即跑一次、不改下次触发时间）；token 错 → 401，同一计划 10 秒内重复触发 → 429。
- 定时与唤醒的时刻按服务端本地时区显示并带偏移。

## template

```bash
gofer template ls [-p <project>]                          # name / source(project|global) / desc（解析失败标 INVALID）
gofer template show <name> [-p <project>] [--var k=v …]   # 来源路径 + 变量表（必填标 *）+ 服务端渲染后的正文
```

模板位置与写法见 [job-advanced.md](job-advanced.md)「任务书模板」。

## agent

```bash
gofer agent list                        # client 模式默认列 server 的 agents（batch / interactive 能力位；内置模板注入的标「内置」）
gofer agent show <key> | detect         # 看定义 / 跑 detect 报告可用性
gofer agent status [key]                # 可用性 + 健康度（healthy / degraded / unknown）+ 近 1h job 与供应商错误 + 24h 用量
gofer agent probe <key> [-p <project>] [--timeout 120]   # 提交一个只回复 OK 的探针 job，退出码 0/1
gofer agent rule …  /  gofer agent skill …              # server 侧强制规则库 / skill 库
```

健康度与故障转移见 [server-config.md](server-config.md)「故障转移与健康度」。

## project（别名 p / proj）

```bash
gofer project list [--remote]           # client 模式默认即 server 的实时项目表；server 模式读本地 config
gofer project show <key> | validate <key>
gofer project add / remove <key>        # 本机配置（client 模式拒绝）；内置 default 项目不能删
```

POLICY worker 上 `project list` 列映射后的本机路径。

## config（别名 cfg）

```bash
gofer config info                       # 解析出的 config 路径 + 关键 ENV + 关键设置
gofer config show <project>             # 某 project 合并后的有效配置
gofer config validate server|worker     # 校验；worker 按模式给判据 + 校验 roots
gofer config edit                       # $VISUAL / $EDITOR 打开
```

## init

```bash
gofer init [server|worker|client]       # 从内置模板生成配置（默认 server）；client 只生成 <config-dir>/.env
gofer init -g worker                    # 写到用户全局 config 目录
gofer init [-g] skill [-o <dir>] [--force]   # 装本 skill：默认写 ./.claude/skills 与 ./.agents/skills；-g 写 ~/.claude 与 ~/.agents
gofer init hooks …                      # 见 session
gofer init worker --server <addr> --id <id> [--admin-token …] --yes   # worker 接入向导，见 operations.md
```

## worker（别名 w）

```bash
gofer worker ls | show <id> | projects <id>
gofer worker add <id> | remove <id>
gofer worker reload <id> [--reason …] | reload --local [<id>]
gofer worker upgrade <id> [--file …] [--force] [--no-wait]
gofer worker [-d] [--worker-config <path>] | init | doctor | stop [<id>]   # 在 worker 机器本机用
```

细节见 [operations.md](operations.md)、[worker-config.md](worker-config.md)。

## serve（别名 s）

```bash
gofer serve [-d] [-c <config>] [--addr …] [--no-web]   # 起 server（-d 后台，日志 <config-dir>/run/serve.log）
gofer serve stop | reload -c <config>
gofer serve register -c <config> [--start] | start | stop | restart | status [--json] | logs | uninstall | upgrade --binary <file>
```

受管服务与升级见 [operations.md](operations.md)「受管 server 服务」。

## tunnel（别名 tun）

```bash
gofer tunnel forward -w <worker> [udp/][bind:]lport:host:port …   # 本机端口 → worker 所在网络的目标；可多条
gofer tunnel forward --name <preset>                              # 用保存的预设
gofer tunnel save <name> -w <worker> <spec…> [--note …] | saved | forget <name>
gofer tunnel presets push                                         # 上传本机 tunnels.yaml 里尚未上传的预设
gofer tunnel check -w <worker> [udp/]host:port                    # 只验 worker 能否建到目标的连接
gofer tunnel ls                                                   # 在线转发进程 + 活动隧道
gofer tunnel stop <forwarder-id>                                  # 让任意机器上的 forward 进程退出（下次心跳内，≤30 秒）
```

- 目标必须在 worker 的 `tunnel.allow` 白名单内。web Tunnels 页可启动 / 停止 server 预设（server 本机托管转发，`autostart: true` 随 server 恢复监听），也可远程停止外部 forward（只有登记者本人或管理员；旧版 forward 只能去那台机器 Ctrl+C）。
- 日志与「慢在哪」的判读见 [troubleshooting.md](troubleshooting.md)「隧道慢 / 不通」。

## tool

```bash
gofer tool cp <src> <dst> [--force] [--timeout 600]      # 单文件对拷，恰一端是 <runner>:<project>/<路径>
gofer tool xfer ls | show <id> | rm <id>                 # 传输暂存区
gofer tool cert [--out-dir <dir>] --hosts <DNS或IP,…>     # 本地 CA + HTTPS 证书
gofer tool stats-backfill [--since 30d] [--force]        # 给老 job 补算 Dashboard 指标（管理员）
```

传文件见 [files-and-wakeup.md](files-and-wakeup.md)；HTTPS 见 [operations.md](operations.md)「HTTPS 入口」。

## mcp 与 presence

- `gofer mcp [--project <key>|auto] [--standalone]`：stdio MCP server，给 agent 用 gofer 的工具（`gofer_run_job`、`gofer_get_job`、`gofer_add_todo`、`gofer_ask_human`、`gofer_issue_brief`、`gofer_memory_flag`、`gofer_work_report`、`gofer_wakeup_create` 等）。连着 server 时走 server 的鉴权；`--project` 把工具收窄到一个项目；`--standalone` 强制进程内模式（client 模式下拒绝）。
- `gofer presence list | send | inbox`：driver agent 的在线登记与收件箱（一般不直接用）。
