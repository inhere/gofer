# gofer 命令参考

> 每组命令「是什么 + 常用写法 + 去哪看细节」。完整 flag 一律以 `gofer <cmd> [<sub>] --help` 为准。连接、project key、agent / runner 的通用规则见 SKILL.md。
> 顶级命令分组：配置（`init` `config` `settings` `project` `agent` `mcp` `hook` `doctor`）· 仓库跟踪（`issue` `memory` `repo`）· 控制面（`serve` `worker` `tunnel` `auth`）· 任务（`job` `plan` `workflow` `schedule` `session` `work` `steward` `template`）· 工具（`tool`）。

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
- [settings](#settings)
- [init](#init)
- [worker（别名 w）](#worker别名-w)
- [serve（别名 s）](#serve别名-s)
- [auth](#auth)
- [tunnel（别名 tun）](#tunnel别名-tun)
- [doctor](#doctor)
- [tool](#tool)
- [mcp](#mcp)

## job

| 命令 | 用途 | 细节 |
|---|---|---|
| `job run`（别名 `add`） | 提交 | SKILL.md；进阶 flag 见 [job-advanced.md](job-advanced.md) |
| `tool refs <id>` | 列出引用该 issue / job / plan / todo / workflow 的可见对象 | `gofer tool refs gofer-abcd`；也可用 MCP `gofer_refs` |
| `job logs <id>` / `show <id>` / `watch <id>` | 读输出 / 查状态 / 跟随到结束 | |
| `job list`（`ls`） | 列 job：`-p` `--status` `--tag` `--agent` `--runner` `--plan` `--session` `--since <unix秒>` `--limit` `--all` | |
| `job cancel <id>` | 取消 | `needs_review` 用 reject |
| `job set <id> --title "…"` | 改 / 清空标题 | |
| `job rerun <id>` | 原请求重提（新会话） | |
| `job resume <id> --prompt "…" [--mode …] [--agent …] [--env K=V] [--model …]` | 续跑同一个 agent 会话 | [job-advanced.md](job-advanced.md)「续跑」 |
| `job say <id> "…"` / `job end <id>` | ACP 持续会话下一轮 / 结束 | 「ACP 持续会话」 |
| `job review <id> [--tail N] [--diff]` | 验收材料一屏 | 「人工验收」 |
| `job accept <id> [--no-deliver]` / `job reject <id> --note … [--resume]` | 人工验收（`--no-deliver`：通过但不交付 worktree 分支） | |
| `job findings <id> [--create-issues]` | 汇报里「发现但不碰」各条，可逐条建 issue（web 验收面板「发现」页签也可逐条「建 issue」，建到 server 的 tracker 镜像，下次 `repo sync` 进仓库） | |
| `job approve <id>` / `job reject <id> [--reason …]` | 待批 job（`--hold`）的批准 / 拒绝，只给人用 | [hold-approval.md](hold-approval.md) |
| `job interactions <id>` / `job answer <id> <iid> <optionId>` | 工具调用审批 | 「工具调用审批」 |
| `job comment <id> "…"` / `job comments <id>` | 留评论 / 看评论（别写 `@名字`） | 「在 job 上留评论」 |
| `job worktree ls / merge <id> / rm <id>` | `--worktree` job 的 worktree | 「并行」 |
| `job deliver <id> [--mode merge\|push\|pr] [--draft\|--ready] [--branch] [--dry-run]` | 交付 `--worktree` job（合并 / 推分支 / 开 PR）或预览交付计划，也用于重试 | 「交付」 |
| `job wakeup create / list / show / enable / disable / rm` | 登记唤醒 | [files-and-wakeup.md](files-and-wakeup.md) |
| `job retry ls <id>` / `job retry cancel …` | `--retry` 的重试链 | 「故障转移与重试」 |
| `job redact` / `job secret-scan` / `job delete` | 脱敏、跨 job 秘密扫描、删除终态 job | 「脱敏」 |

`job run --timeout` 超过上限（项目 `max_timeout_sec`，否则 `server.max_job_timeout_sec`，默认 3600）会被截断：stdout 的 `submitted` 行末尾带 `timeout=3600s (clamped from 10800s)`，stderr 写明 `timeout 10800s clamped to 3600s by server.max_job_timeout_sec`（来源是项目时写 `projects.<key>.max_timeout_sec`），`job show` 的 `timeout:` 行下面是 `(timeout 14400s clamped to 5400s by server.max_job_timeout_sec)`；web 任务详情概览的 duration 行同样在生效值旁写「请求 4h00m，被 server.max_job_timeout_sec 封顶」。事后再查详情也能看出被截短过。不想被静默截断就加 `--strict-timeout`，超限时直接报错（400，写明上限与配置键）。MCP 提交结果带 `timeout_sec` / `requested_timeout_sec` / `timeout_clamped` / `timeout_limit_source` / `timeout_limit_key`，`gofer_run_job` 可传 `strict_timeout`。
`job run` 常用 flag：`-p` `-a` `--runner` `--cwd` `--title` `--prompt` `-f` `--sync` `--wait` `--wait-timeout` `--timeout` `--strict-timeout` `--plan` `--todo` `--issue` `--tags` `--env`；进阶：`--session` `--interactive` `--read-only` `--read-dir` `--model` `--from-session` `--max-tokens/--max-cost/--max-turns` `--agent-budget-tokens/--agent-budget-cost-usd` `--verify` `--review` `--acceptance` `--scope` `--prime-keywords` `--worktree` `--lock` `--lock-wait` `--shared-dir/--exclusive-dir` `--stall-timeout` `--fallback` `--retry` `-t/--var` `--upload` `--collect` `--hold` `--source-session-id` `--rule` `--skill` `--mcp/--no-mcp` `--deliver/--deliver-branch` `--role`。`--read-dir` 可重复，目录在提交时校验为项目内或 `server.read_dir_allow` 中的只读目录。

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
gofer workflow init verify|release [--dir <仓库目录>] [--force]   # 生成起步文件
gofer workflow template ls | show <name> [-p <project>] [--source]   # ls 另显示执行次数与最近一次状态
gofer workflow template save <name> -f <file> | rm <name> [--global | --project <p>]
gofer workflow save-as-template <wf-id> <name> [--global | --project <p>]
gofer workflow list [--status …] [--template <name>] / show <id> / events <id> / cancel <id> / export <id>
gofer workflow rerun <id> --failed | --from <step> [-w]
gofer workflow pick <id> <step> <fan> [--merge [--squash] [--cleanup-others]]
```

细节见 [plans-workflows.md](plans-workflows.md)「workflow」「verify / release 起步 workflow」「内置步骤」「多 agent 对比与择优」。

## issue / memory / repo

```bash
gofer issue ready | ls [-l tag] [--stale] | show <id>... | brief <id>
gofer issue create "标题" [--type …] [-p 0-4] [-l a,b] [-d …] [--design …] [--acceptance …] [--parent <epic>] [--from <id>]
gofer issue update <id>... [--claim] [--status …] [--acceptance …] [--clear field,…]
gofer issue comment <id> "…" | close <id>... --reason "…" | reopen <id> | dep add|rm|ls …
gofer memory set <key> "…" [--kind rule|note|handoff] [--title …] [--summary …] [--tags …] [--when-*] [--global | --project <p>]
gofer memory sync --global                                 # 本机全局记忆副本与 server 双向同步（写后会自动同步）
gofer memory ls [关键字] | show <key>... | rm <key> | flag / unflag <key> | doctor | archive / restore / promote
gofer memory candidates | accept <id> --key k | reject <id>
gofer repo init | sync | status [--changed] | prime [--keywords a,b] | merge-driver [--install] | changelog-merge [--install [--file f]] | migrate --from-bd [--apply]
```

细节见 [tracker.md](tracker.md)。

## session（别名 sess）与 hook

```bash
gofer init hooks [--agent claude|codex|omp|jcode|all] [--global] [-o <dir>] [--remove] [--prime-only] [--force]
gofer init skill [-a all] [--global] [-o <dir>] [--force]   # -a all 只写已存在的 .claude / .agents，其余跳过并列出；不带 -a 照常创建两处
gofer session ls | show <id> | relay on|auto|off [--session <id>] | say <id> "…" [--deliver [--takeover]]
gofer session resume <id> [--input …] [--plan] | release-takeover <id> | watch <job-id> | rm <id> | owner <id> <caller>
gofer session topic "<一句话主题>" [--scope <子路径>]… [--clear-scope] [--session <id>] | find <关键词|路径> [--path <p>] [-p <project>] [--limit n]
gofer session nudge <id> (--every <dur> | --when-stalled <dur>) -m "…" [--until …] | nudge ls / rm / pause / resume
gofer hook claude|codex|omp|jcode|generic [--agent <key>] [--wait N]   # hook 执行体，由 hooks 配置调用，人不直接用；日志 <config-dir>/run/hook.log
```

细节见 [sessions-relay.md](sessions-relay.md)。

## work（别名 wk）/ steward（别名 stw）

```bash
gofer work ls | show <id> | new | capture | set | note | park | remind | report | link | to-todo | merge | split | summarize | accept | dismiss | requests | digest | rm
gofer steward status | start | restart | stop | ask "…" | notes | review | merges | merge-accept <n> | merge-dismiss <n>
```

细节见 [work-items.md](work-items.md)。

## schedule（别名 sch）

```bash
gofer schedule add <job 请求 flag…> (--cron '*/5 * * * *' | --delay 30m | --at <RFC3339|unix秒>) [--webhook] [--param-def … | --params-file <yaml>]
gofer schedule list / show <id> / enable <id> / disable <id> / run <id> [--param K=V …] / rm <id>
gofer schedule edit <id> [--cron … | --at …] [--title …] [--param-def … | --params-file <yaml>] [--timeout <sec>] [--enable|--disable]
gofer schedule rotate-token <id>        # 换 webhook 触发 token（旧的立即失效）；没开 webhook 的计划也可用它开启
```

- **参数**：`--param-def 'NAME[:type]=default[#说明]'`（可重复，type = `string`（默认）/ `int` / `bool`），`choice` 这类带可选值列表的用 `--params-file <yaml>`（格式是 `[{name, type, default, desc, choices}]`）。两种写法互斥。名字必须是环境变量名（`^[A-Z_][A-Z0-9_]*$`，不能以 `GOFER_` 开头），最多 20 个。
- **参数的传递方式**：值只作为 job 的环境变量传入（不拼进命令行、不替换 prompt），脚本自己读 `$env:NAME` / `$NAME`。叠加顺序：计划里存的 env < 参数默认值 < `schedule run --param` 给的值；cron 触发与 webhook 触发用默认值。一次性临时改参数：`gofer schedule run <id> --param CLEAN_APPLY=1`（给未声明的键会 400 并列出可用参数）。`schedule show` 列出参数声明。
- **`schedule edit` 只能改调度自身的设置**：cron / 一次性时间（改了会重算下次触发）、标题、参数声明、启停、超时。命令 / agent / runner / project 不能在这里改——那算另一个任务，删掉重建（webhook token 会换）。
- `--webhook`：给计划一个外部触发入口（不需要 gofer 的 bearer token）：
  `curl -X POST -H 'X-Gofer-Trigger-Token: <token>' 'http://<server>/v1/schedules/<id>/trigger'`（也可 `?token=<token>`）。语义同 `schedule run`（立即跑一次、不改下次触发时间）；token 错 → 401，同一计划 10 秒内重复触发 → 429。
- **token 归属**：计划属于创建它的 caller。别人（非 owner、无 `schedule.manage.others`）`list` / `show` 时只看到 `trigger_token` 的前 4 位、其余打 `*`，`rotate-token` 的响应同样掩码。
- **`schedule run` 用当前调用方的身份**提交（job 的 caller 就是执行 `run` 的人，并发 / 限流按该 caller 算）；计划里存的请求仍归属创建者，cron 与 webhook 触发的按创建者提交。
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
gofer agent mcp ls | show <name> [--here]               # MCP 注册表：绑定位置、secret 变量名是否已设置（--here 按当前机器）
gofer agent mcp test <name>                             # 在当前机器启动 / 连接该 MCP server 并列出工具
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
gofer config edit [--key agents]        # $VISUAL / $EDITOR 打开；--key 打开该段所在的文件（配置拆分后）
gofer config split [--keys agents,projects] [--dir config.d] [--dry-run]   # 把大段搬到单独文件并 include
gofer config lint [server|worker] [--fix]  # 找出删掉也不影响生效配置的键（--fix 删除）与可改写成 extends: builtin 的 agent；worker 查 worker.yaml
```

## settings

运行时可在数据库里调的扩展设置（降噪阈值与排除名单、提示词覆盖）。存在 **server 的数据库**里，所以这些命令走 HTTP，对 server 所在机器生效、对每台客户端同样可见；改完立刻生效（本进程即时，别的进程 5 秒内）。

```bash
gofer settings ls [--json]                        # 键、类型、是否改过、当前值（--json 给完整条目：默认值、上下限、谁改的）
gofer settings get <key> [--json]                 # 打印当前值：字符串原样、列表一行一个
gofer settings set <key> <value>                  # 列表用逗号分隔："demo,sandbox"
gofer settings set <key> --file <path|->          # 多行值（提示词），- 读 stdin
gofer settings reset <key>                        # 删掉存储值，回到内置默认
gofer settings export [-o <file>]                 # 只导出「改过」的键（YAML），用于备份 / 搬到别的机器
gofer settings import <file|-> [--yes]            # 默认预览：逐键 新增/修改/不变/拒绝及原因；--yes 才写
```

- 读要 `settings.read`（默认给 users），写要 `settings.write`（不默认授予）；受限调用方能读不能写。
- 值按键注册的类型校验，越界 / 类型不符 / 正则不合法都会被拒（非零退出，带原因）。
- 写入记入审计（`gofer auth audit`）：键 + 新旧值摘要。

## init

```bash
gofer init [server|worker|client]       # 从内置模板生成配置（默认 server）；client 只生成 <config-dir>/.env
gofer init -g worker                    # 写到用户全局 config 目录
gofer init [-g] skill [-o <dir>] [--force]   # 装本 skill：默认写 ./.claude/skills 与 ./.agents/skills；-g 写 ~/.claude 与 ~/.agents
gofer init hooks …                      # 见 session
gofer worker init --server <addr> --id <id> [--admin-token …] --yes   # worker 接入向导，见 operations.md
```

## worker（别名 w）

```bash
gofer worker ls | show <id> | projects <id>
gofer worker add <id> | remove <id>
gofer worker join --server <addr> [--id <提议 id>] [--labels a,b] [--roots from=to]… [--start] [--yes]   # 自助注册（需 server 开启 worker_enrollment）
gofer worker reload <id> [--reason …] | reload --local [<id>]
gofer worker upgrade <id> [--source auto|server|mirror] [--version <X.Y.Z>] [--file …] [--force] [--no-wait]
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

## auth

```bash
gofer auth whoami [--json]     # 本 token 是哪个调用方、是否根管理员、持有哪些权限及来源
gofer auth routes [--json]     # 服务端每条路由的声明与需要的权限
gofer auth grant renew <grant-id> --for 4h   # 把一条仍然生效的会话授权往后延（最长 24h）
```

- `whoami` 报的是**服务端**的判定结果：每项权限的来源是 `root`（根管理员）/ `config`（由配置推出，只读）/ `db`（数据库授权，并给出是哪条授权给的）；`--json` 给脚本用。权限不够时的 403 正文让人跑的就是它。它还会打印这个请求附带的**会话 id 与来源环境变量**（`GOFER_SESSION_ID` 或唯一那个 agent 会话变量；多个不一致就不带），靠会话授权拿到的权限就在同一份列表里（见 [sessions-relay.md](sessions-relay.md)「会话授权」）。
- `routes` 与生成文档 `docs/reference/permissions.md` 同一份数据，但来自正在跑的那个二进制。详见 [server-config.md](server-config.md) 末尾的权限段。
- `grant renew` 是这组里唯一的**写**操作（新增与撤销授权在 web 上做）：授权快到期时把同一条授权的到期时间往后挪，不重新发一条，所以范围不会变。用法与生效条件见 [sessions-relay.md](sessions-relay.md)「会话授权」。

## tunnel（别名 tun）

```bash
gofer tunnel forward -w <worker> [udp/][bind:]lport:host:port …   # 本机端口 → worker 所在网络的目标；可多条（逗号或空格分隔）
gofer tunnel forward -w <worker> socks/1080                       # 本机 SOCKS5 / HTTP CONNECT 代理：目标按连接选，浏览器 / curl 直接用原地址
gofer tunnel forward -w <worker> =<ip>:<port>                     # 同地址转发：本机 <ip>:<port> → 远端同一地址（先把 <ip> 挂到本机环回网卡）
gofer tunnel forward --name <preset>                              # 用保存的预设
gofer tunnel save <name> -w <worker> <spec…> [--note …] | saved | forget <name>
gofer tunnel presets push                                         # 上传本机 tunnels.yaml 里尚未上传的预设
gofer tunnel check -w <worker> [udp/]host:port                    # 只验 worker 能否建到目标的连接
gofer tunnel ls                                                   # 在线转发进程 + 活动隧道
gofer tunnel stop <forwarder-id>                                  # 让任意机器上的 forward 进程退出（下次心跳内，≤30 秒）
```

- 目标必须在 worker 的 `tunnel.allow` 白名单内。web Tunnels 页可启动 / 停止 server 预设（server 本机托管转发，`autostart: true` 随 server 恢复监听；停一个正在跑的托管预设只有它的**启动者本人或根管理员**可以，升级前的存量 autostart 预设按它的 `updated_by` 算启动者、那个调用方已不在配置里时只有根管理员能停），也可远程停止外部 forward（只有登记者本人或管理员；旧版 forward 只能去那台机器 Ctrl+C）。
- `socks/` 只能绑环回地址；每条连接仍按真实目标受 worker 白名单约束，被拒时客户端收到 SOCKS5 回复码 / HTTP 状态。只支持 CONNECT（无 UDP、无普通 HTTP 代理）；域名目标由 worker 解析，白名单的 CIDR 条目只匹配 IP。浏览器并发连接多，worker 的 `tunnel.max_conns`（默认 8）建议调到 32 以上。
- `=<ip>:<port>` 让本地服务按真实地址连接；启动前检查监听地址是否属于本机，不在会报错并给出添加环回地址的命令。
- 日志与「慢在哪」的判读见 [troubleshooting.md](troubleshooting.md)「隧道慢 / 不通」。

## doctor

```bash
gofer doctor --bundle [-o <path>] [--lines 500]   # 导出脱敏诊断 zip（默认日志尾部 500 行）
gofer doctor --print                              # 只打印清单与摘要，不创建文件
```

诊断包只包含版本 / 系统 / 运行模式、脱敏配置与 `.env` 路径、日志尾部、worker / agent 状态和受管服务状态；不会收集 token 原值、对话、信件或记忆。上传前仍应检查清单和内容。

## tool

```bash
gofer tool cp <src> <dst> [--force] [--timeout 600]      # 单文件对拷，恰一端是 <runner>:<project>/<路径>
gofer tool xfer ls | show <id> | rm <id>                 # 传输暂存区
gofer tool cert [--out-dir <dir>] --hosts <DNS或IP,…>     # 本地 CA + HTTPS 证书
gofer tool stats-backfill [--since 30d] [--force]        # 给老 job 补算 Dashboard 指标（管理员）
gofer tool hook-test <name> [--point job.submit|job.deliver] [--input sample.json]   # 用样例输入试跑一个决策 hook（本机配置，不提交任何东西）
gofer tool api [-X METHOD] <path> [-f key=value]… [-F key=@file]… [--input file|-] [-q <路径>] [-i]   # 直接调 server 的 HTTP API
```

`tool api` 用和其他命令同一套地址与 token，`path` 写 `jobs` 或 `/v1/jobs` 都行：`-f` 组 body（GET 时变 query，值能当 JSON 解析就按 JSON 发），`-F key=@file` 把文件内容作为字符串值，`--input` 给原始 body；`-q 'jobs.*.id'` 取响应里的值（路径语法 `top.sub` / `jobs.0.id` / `jobs.*.id`，字符串原样、其余 JSON），`-i` 打状态行与响应头；非 2xx 退出码非零、错误体打到 stderr。

传文件见 [files-and-wakeup.md](files-and-wakeup.md)；HTTPS 见 [operations.md](operations.md)「HTTPS 入口」。

## mcp

- `gofer mcp [--project <key>|auto] [--standalone]`：stdio MCP server，给 agent 用 gofer 的工具（`gofer_run_job`、`gofer_get_job`、`gofer_add_todo`、`gofer_ask_human`、`gofer_issue_brief`、`gofer_memory_flag`、`gofer_work_report`、`gofer_wakeup_create` 等）。连着 server 时走 server 的鉴权；`--project` 把工具收窄到一个项目；`--standalone` 强制进程内模式（client 模式下拒绝）。
