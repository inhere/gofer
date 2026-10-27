# 终端会话交给 web：hooks、中继、送话、唤醒

> 人离开电脑时，让**这个终端会话**停下来等的那条消息进 gofer web「会话」页，人在 web（手机也行）回复，回复注入**同一个**会话继续跑。靠 agent CLI 的 hook 实现，不开 pty、不重开会话。完整 flag 以 `gofer session --help` / `gofer init hooks --help` 为准。

## 目录

- [安装 hooks](#安装-hooks)
- [常用命令](#常用命令)
- [三态开关 on / off / auto](#三态开关-on--off--auto)
- [自动布防的两条判据](#自动布防的两条判据)
- [人回来即放行](#人回来即放行)
- [会话进行中预览、用量与授权上 web](#会话进行中预览用量与授权上-web)
- [会话授权：给一个终端会话临时发权限](#会话授权给一个终端会话临时发权限)
- [会话主题与查找](#会话主题与查找)
- [信箱（mail）](#信箱mail)
- [给不在等回复的会话送话](#给不在等回复的会话送话)
- [唤醒 / 接管会话](#唤醒--接管会话)
- [web 发消息（转达）与会话回复](#web-发消息转达与会话回复)
- [session watch：job 跑完时被叫醒](#session-watchjob-跑完时被叫醒)
- [会话催办（nudge）](#会话催办nudge)
- [等另一个会话 / 看它的屏幕（wait / screen）](#等另一个会话--看它在显示什么wait--screen)
- [自研 agent 接入（generic）](#自研-agent-接入generic)
- [其它约定](#其它约定)

## 安装 hooks

```bash
gofer init hooks                         # 写 ./.claude/settings.json（默认 --agent claude）
gofer init hooks --agent codex|omp|jcode|all   # all = 四个里「配置目录已存在」的才写，其余输出「跳过 <agent>：未发现 <dir>」；点名某个 agent 则照常创建目录
gofer init hooks --agent all --global    # 用户级一次装好，所有工作区生效
gofer init hooks --remove [--agent X] [-o <项目目录>]
gofer init hooks --prime-only --global --agent all   # 只装记忆注入（见 tracker.md）
```

| agent | 配置落点 | web 回复注入 |
|---|---|---|
| claude | `.claude/settings.json`（`--global` → `~/.claude`） | 是 |
| codex | `.codex/hooks.json`（`--global` → `~/.codex`；codex 要在交互模式批准一次，`config.toml` 需开 hooks 功能，项目 `.codex/` 需 trust） | 是 |
| omp | 扩展 `.omp/extensions/gofer-relay.ts`（`--global` → `~/.omp/agent/extensions/`） | 是（回复作为新一轮消息送回） |
| jcode | `~/.jcode/config.toml` 的 `[hooks]`（无项目级，须 `--global` 或 `-o <JCODE_HOME>`） | 否，只观察；要传话用 tmux 送话 |
| generic（自研） | 由 agent 自己调 `gofer hook generic --agent <key>` | 是 |

- 幂等，只增删 gofer 自己的条目。jcode 每个事件只能配一条命令，已有用户命令时不覆盖并提示；omp 扩展文件带 gofer 标记，同名的非 gofer 文件拒绝覆盖（`--force` 才换）。
- 全局安装后会扫描当前目录与已登记项目，列出已有的**项目级** gofer hooks（会与用户级重复触发）并给出清理命令。不在项目列表里的目录，会话以「无项目」登记：中继 / 传话照常可用（投递 job 在执行机的默认工作空间 `default` 里跑），但「接管」不行——`--resume` 要在会话自己的目录里重开，无项目时 gofer 会明确告诉你把会话启动在项目目录内。
- 老版本装过的 hooks 重跑一次 `gofer init hooks` 即可获得新条目（子 agent、进行中预览、授权上 web、命令前记忆注入等）。

## 常用命令

```bash
gofer session relay on|auto|off [--session <id>]   # 省略 --session 按当前目录反查
gofer session ls [-p <p>] [--state …] [--all] [-l]  # 默认不列「离线 / 已结束且超过 5 天没活动」的会话（末尾提示隐藏了几个），--all 全列； waiting_reply 置顶；RELAY 列 on/off/auto；NAME 列是 Claude Code 会话名；SESSION 列是列表内唯一的最短前缀（≥8 字符，可直接当 <id> / --session / 信件地址用），-l 显示完整 id
gofer session show <id>                            # 详情 + 最近 turn；relay 行显示 mode 与判定依据
gofer session say <id> "<回复>"                    # = web 输入框：答最新的等待 turn；"/off" = 关中继并放行
gofer session say <id> "<回复>" --deliver          # 没有 turn 在等也送（见「送话」）
gofer session say <id> "<回复>" --deliver --takeover   # 没有 tmux 时起新进程接管（见「唤醒 / 接管」）
gofer session resume <id> [--input "首条消息"] [--plan]
gofer session release-takeover <id>
gofer session watch <job-id> [--session <id>]
gofer session nudge <id> --when-stalled 20m -m "进展如何？"
gofer session rm <id>
gofer session owner <id> <caller>                   # 把会话转给该 caller（管理员，需 `session.transfer`；见「归属被拒」）
gofer session topic "<主题>" [--scope <子路径>]…    # 记下当前会话在做什么（见「会话主题与查找」）
gofer session find <关键词|路径>                    # 找哪个会话在做这件事
```

**约定**：用户说「打开中继 / 我要离开了 / 交给 web」→ 执行 `gofer session relay on`，然后正常结束回合；之后每次回合结束都在 web 等回复，直到 web 回复 `/off`、终端有人输入、或 `relay off`。

## 三态开关 on / off / auto

按会话存在 server，缺省 `auto`：

| mode | 含义 |
|---|---|
| `on` | 每次停下都在 web 等回复；终端有人工输入会自动回到 `auto`（`session show` 多一行 `note:`，web 抽屉显示说明）；web 回复 `/off` 或 `relay off` 关掉 |
| `off` | 从不等；已打开的等待被释放 |
| `auto` | server 按下面两条判据决定这次停下要不要等 |

- hook 每次 Stop 先问 server，不等则零阻塞；server 不可达也直接放行，**永不卡死终端**。
- web 会话列表里也能点切换，**下一次回合结束**生效；已停在空闲提示符、且人从未离开过的会话没有 hook 在跑，需在终端输入一次。
- 等待上限由 server 下发：`on` → `session.relay_on_wait_sec`（默认 3600），`auto` → `session.relay_auto_wait_sec`（默认 600），`0` = 不设上限。

## 自动布防的两条判据

忘了开也不要紧，`auto` 下 server 自动布防：

1. **键盘空闲**（`session.auto_relay_idle_sec`，默认 300，写 0 关）：hook 上报键鼠空闲秒数，人离开超过阈值就等 web 回复（列表显示 `auto (idle 12m)`）。Windows / macOS 探得到键盘；Linux 需要 `xprintidle`（X11），Wayland / 无桌面 / 容器里为「未知」。
2. **距上次人工输入**（`session.auto_relay_turn_sec`，默认 900，写 0 关）：探测不到键盘时（容器里的 hook 测不到主机键盘），看会话里人最后一次输入距今多久，到阈值同样布防（列表显示 `auto (no input 22m)`）。

不布防的情况：你名下还有 job 在跑、或会话里有子 agent 在跑（视同「监督中」，受 `session.auto_relay_skip_when_supervising` 控制；显式 `on` 不受影响）。最后一个子 agent 结束时，已阻塞的等待会被释放（auto 与显式 `on` 都一样：主 agent 先处理子 agent 的结果，下一次 Stop 再继续等 web 回复）。

## 人回来即放行

- 判据一开的等待：hook 每 ≤5 秒重探空闲值，人一碰键鼠就放行。
- 判据二开的等待：按 Esc 结束等待，或直接在终端输入一条，server 就把等待关掉。
- 显式 `on` 的等待不被终端输入关闭（输入只把 mode 降回 `auto`）；但**按 Esc** 会关掉这一轮等待、会话回 idle、开关保持 `on`（下次停下照常中继）。
- web 注入的回复带前缀 `[来自 web 回复，<用户>]`（用户本人的消息都属于“以 `[来自 web` 开头”这一类），与终端输入等价处理，但不会被当成「人回来了」。
- **从 web 发消息后对话转到 web**：从 web（或 `session` 的送话接口）发给会话的消息送达后，`auto` 会话自动切到 `on`，之后每次停下都在 web 等你回复；你回到终端输入一条，就照常降回 `auto`。显式 `off` 的会话不受影响。

## 会话进行中预览、用量与授权上 web

- **进行中预览**：PostToolUse hook 读会话最新的 assistant 文字，以「进行中」更新会话列表、抽屉和工作台；`session.progress_interval_sec` 默认 30 秒，0 关闭。
- **会话用量**：hook 在 Stop / SubagentStop / SessionEnd 增量读会话记录，上报 token（主会话与子 agent 分开、按模型）；Sessions 详情、会话抽屉、工作项卡、首页用量卡可见。只有 token，来源无费用就不显示费用。jcode 不采集。
- **终端授权上 web**（claude）：终端弹工具授权时，会话最后消息显示「需要授权：Bash `…`」；会话正在等 web（同 on / auto 判据）时，「今天」页与会话抽屉出「需要授权」卡，可允许 / 总是允许 / 拒绝 / 附原因拒绝，与终端对话框先答者生效。会话 owner（首次登记的 caller，换 token 后就是 hook 的身份）或持有 `session.permission.answer` 的 caller 能答，worker / job 凭据一律 403；`session say` 不会答它。
- **终端选择题上 web**（claude）：agent 调 AskUserQuestion 时，会话最后消息显示「等你回答：<问题>」；会话正在等 web（同上判据）时，选择题不在终端弹框，而是变成「等你回答」卡（会话抽屉按题选择 / 多选 / 自由填写；「今天」页单个单选题可直接点选项），提交后 agent 直接拿到答案继续；超时、人回到终端或 server 不可达时交回终端弹框。人在终端前时照常在终端问。需要 hooks 里有 `AskUserQuestion` 的 PreToolUse 条目：升级后重跑 `gofer init hooks`。
- **会话名**：claude hook 上报 Claude Code 自己的会话名，web 与 `session ls` 的 NAME 列、`session show` 的 `name:` 行显示，可一键复制。
- **离线**：Claude Code 进程被直接杀掉时没有结束事件；会话超过 `session.offline_after_sec`（默认 1800，0 关闭）无心跳就标 `offline`（灰色徽标），收到新心跳恢复。`offline` 与 `ended` 一样可以唤醒。

## 会话授权：给一个终端会话临时发权限

需要「这个会话能开隧道 / 能发版升级」而**不想把权限发给整个 token** 时，给会话本体发一条授权：

```bash
# 需根管理员（auth.manage）。主体 session:<sid>，有效期 1 / 4 / 24 小时，最长 24 小时。
curl -H "Authorization: Bearer $GOFER_TOKEN" -H 'Content-Type: application/json' \
  -d '{"subject":"session:<sid>","pattern":"tunnel.open","expires_sec":3600}' \
  http://<server>/v1/auth/grants
```

- **在 web 上授权**（根管理员）：Sessions 页的会话卡片与详情抽屉都有「授权」按钮——只列可授予会话的权限，有效期选 1 / 4 / 24 小时（服务端要求必带，上限 24 小时），一个权限一条授权；弹窗里先写明威胁模型：谁能在这个终端会话里运行命令（含会话里的 agent 与它启动的进程），谁在有效期内就拿到这些权限，会话结束即失效。已授权的会话显示「已授权」徽标，抽屉里「撤销授权」。徽标按 `GET /v1/auth/grants` 每行的 `effective` 显示：服务端按与鉴权相同的条件（授权状态、有效期、会话未结束、owner 一致、会话化身一致）判定，会话结束后复活的授权不再显示徽标；列表里该行仍是 active、可撤销。
- 一条 `session:<sid>` 授权生效要**同时**满足：请求带上了那个会话 id（CLI 自动带，见下）；发起人是会话当前的 owner，也是登记授权时的 owner；会话化身没变（每次「离开 ended」都会换）；会话没结束；授权没被撤销、也没过期。不满足就是没这条授权，在哪个因素上失效都不会报错，只是权限没了。
- **续期（到期不用重新授权）**：把同一条授权往后挪即可，授权范围一点不变。

  ```bash
  # 需根管理员（auth.manage）。--for 从「现在」算起，不是从原到期时间算起，最长 24 小时。
  gofer auth grant renew <grant-id> --for 4h

  # 同一件事的接口写法（expires_sec 单位是秒，1 ~ 86400）
  curl -H "Authorization: Bearer $GOFER_TOKEN" -H 'Content-Type: application/json' \
    -d '{"expires_sec":14400}' \
    http://<server>/v1/auth/grants/<grant-id>/renew
  ```

  - 只改**到期时间**：模式、展开出的权限、授权人、绑定的会话与化身全部照旧，所以续期不会把授权放宽，也不会挪到别的会话。
  - 只有**当前仍然生效**的授权能续期，判据与上面那条生效条件完全一致（未撤销、未过期、模式仍匹配得住、会话在册且没结束、owner 与化身与登记时一致）。已撤销 / 已过期 / 会话已结束 / 归属或化身变了的都不能续期——这些要**重新授权**（`409`；非会话主体 `400`，未知 id `404`，服务端的拒绝原因会写明是哪种情况）。
  - `--for` 超过 24 小时或没给，CLI 在本地就报错（不打扰服务端）；接口返回的服务端行里 `state` / `effective` / `expires_at` 已是续期后的值。
  - 续期写审计：`GET /v1/auth/audit` 里的 `grant.renew` 行带旧的与新的到期时间。续期通过本 server 生效后立即对新请求有效（与新增 / 撤销同一条路径）。
  - web「设置 → 权限 → 会话授权」页与 Sessions 页的授权弹窗目前只能新增 / 撤销；续期用上面的 CLI 或接口。
- CLI 怎么把自己认成某个会话：请求带 `X-Gofer-Session-Id`，值来自环境 —— suga 会话里的 `$SUGA_SESSION_ID` 最优先（只有 suga 给自己起的子进程设置，比继承来的 `GOFER_SESSION_ID` 更具体；它指向的会话在 server 上不存在时报错并点名这个变量，不会换成别的变量的会话），其次显式设置的 `$GOFER_SESSION_ID`，否则 `$CLAUDE_CODE_SESSION_ID` / `$CODEX_THREAD_ID` / `$CODEX_SESSION_ID` 去重后**恰好一个**才用；没有、或两个取值不一致就什么都不带（带上别人终端那个会话反而更糟）。只看环境变量，不看当前目录，也不读标记文件。
- 自查：`gofer auth whoami` 会打印这个请求附带的会话与**来源变量**，以及靠会话授权拿到的权限（来源 `db`，并给出是哪条授权）。
- **归属转移会撤销它**：`gofer session owner <sid> <caller>`（web 上的「转给 <caller>」）在同一个事务里撤销该会话的授权，并把它标为已撤销。
- **撤销 / 到期 / 会话结束会断开靠它的长连接**：靠这条授权建立的长连接（隧道、attach 终端、SSE 日志流、`/v1/ws` 推送、托管转发）会在权限没了的时候被服务端断开——撤销与服务端重载当场断，到期与会话结束由周期重判接住（最长 10 秒）。还有别的来源同一项权限（配置推出的授权、另一条授权、自己的资源）的连接重新判定后保留。
- 派出的 job **不继承**上面那几个会话变量（在 job 环境禁止清单里），所以在 job 里跑的 `gofer` 不会认成你终端那个会话、拿不到它的会话授权。显式设置的 `$GOFER_SESSION_ID` 会被后代进程继承，`gofer tunnel forward -d` 起的守护进程同样如此 —— 想只给一次调用，就按命令传环境变量而不是 `export`。

## 会话主题与查找

多个终端会话并行时，用「主题 + 范围」让别的会话和人找到「谁在做什么」。

```bash
gofer session topic "修复登录超时" --scope web/src/auth --scope web/test/auth   # 当前会话
gofer session topic --scope web/src/api          # 只改范围（--scope 整体替换旧范围）
gofer session topic "新主题"                      # 只改主题，范围不变
gofer session topic --clear-scope                # 清空范围
gofer session topic "…" --session <id>           # 显式指定会话
gofer session find 登录                           # 关键词：匹配主题 / 会话名 / 项目
gofer session find web/src/auth/login.go          # 像路径的参数按路径匹配范围（也可写 --path <p>）
gofer session find 登录 --path web/src -p <project> --limit 5
```

- 当前会话**只**按环境变量识别：`SUGA_SESSION_ID`（suga 会话，最优先）、`GOFER_SESSION_ID`（显式设置），否则 `CLAUDE_CODE_SESSION_ID` / `CODEX_THREAD_ID` / `CODEX_SESSION_ID` 恰好一个。没有或有多个不同取值时报错，请加 `--session <id>`；**不**按当前目录、也不读标记文件猜。Claude Code 里 `/clear` 之后环境里的会话 id 会换成新会话，旧会话已结束；已结束的会话改主题返回 409。
- 主题一句话（最多 200 字符），范围是项目根相对的子路径（最多 20 个，不能是绝对路径或含 `..`）。自己设的来源标 `self`，别人替它设的标 `human`；没设时显示由工作目录和会话名推断的值，标 `inferred`（不落库，随时重算）。
- `find` 每个会话一行：短 id、agent、状态（离线的带「可能不在线」）、主题（带来源）、范围、命中原因（`scope` 范围命中 > `scope_weak` 弱命中 > `topic` > `peer_name` > `project`）。只搜还能收到消息的会话；看不到他人会话的调用方只搜到自己的。
- MCP：`gofer_session_set_topic`（`topic` / `scope` / `clear_scope` / `session_id`）与 `gofer_session_find`（`query` / `path` / `project` / `limit`），语义同 CLI，需要 gofer server（单机本地模式会报需要 server）。
- SessionStart hook：登记回应里主题为空或只有推断值时，会往 additionalContext 加一行提示，告诉 agent 用 `gofer session topic` 记下主题；已有自己或人设的主题不再提示。
- **压缩 / 恢复后重新注入**：`source` 为 `compact` / `resume` 时，`gofer repo prime --hook-json` 在仓库级内容之后追加一段 `[gofer · 提示]`「本会话当前任务」——主题、认领中的 issue、监督中的 plan、在等的 job、未回的信件，每项一行带接手命令；`startup` / `clear` 不追加。数据来自 `GET /v1/sessions/{sid}/focus`，详见 `tracker.md` 的「prime」一节。

## 信箱（mail）

会话所属项目的 `language`（未设置时取 `server.language`）决定唤醒提示、标签截断说明、收集箱摘要和退信的语言；`<gofer-mail>` / `<gofer-mail-wake>` 标签及属性保持语言无关。

```bash
gofer session mail send <地址> [--urgent] [--body-file f | 正文]   # 发信
gofer session mail send --to <地址> [--to <地址>…] <正文>          # 群发：一封给多个会话
gofer session mail ls [--in|--out] [--thread <串>] [--state <状态>] # 列信（默认收发都列）
gofer session mail conv <cv-id>     # 按对话 id 看一个对话（双方 + 全部信件，最早在前）
gofer session mail read <id>        # 读信；读过即视为已送达，之后不会再被注入
gofer session mail reply <id> <正文>  # 回信：同一对话串，送回发件方（唯一的回复途径）
gofer session mail cancel <id>      # 撤回还没送达的信（待指派的信被撤回时，它在收集箱里的条目一并丢弃）
```

```bash
# 脱敏：抹掉一封已发出的信里的秘密（原文从 stdin 进，不进 argv / 进程列表）
gofer session mail redact <id> --literal-from-stdin [--pattern <RE2>]
```

- **对话 id**：一组对话（同一对人之间、不分方向的全部信件）有一个稳定短 id `cv-` + 10 位十六进制，由两端地址算出，换个方向、换条对话串都不变。web 信箱右栏头部显示并可以复制它，每封信也能复制自己的 thread id；把它发给人就能定位问题：`gofer session mail conv cv-0123456789` 看这个对话，web 打开 `/mail?conv=cv-0123456789` 直接进这个对话。按 id 取对话只在本调用方可见的范围内找，别人的对话一律 404（与看不到那封信一样）。
- **地址**：`session:<sid>`（指定会话）、`job:<id>`（运行中的 **ACP 会话 job**：`--session` 起的那个）、`topic:<关键词>`（按会话声明的主题找）、`path:<子路径>`（按范围找）、`caller` 或省略（发给自己的收集箱）。只有**唯一命中**才投递；没有命中、有多个候选或收件方不可投递时，信件转为「待指派」进收集箱，发信的回应里会列出候选，再用 `session:<sid>` 发一次即可（或在 web 的收集箱里改投）。
- **`job:` 地址**：job 收信只有一个前提——它是 ACP 会话 job（`session: true`），会在 `awaiting_input` 等下一轮输入。**一次性作业（没有 ACP 会话）与已结束的 job 永远收不到信**，所以这种地址在发信时**直接拒绝**（HTTP 409，`job cannot receive mail`），并给出替代做法：发给它背后的会话（`session:<sid>`）、等它结束再读汇报（`gofer job show <id>`）、或给它留评论（`gofer job comment <id> …`）；`job:<id>` 不存在时报 404（`gofer job ls` 看 id）。
- **出处**：在 agent 会话里发信（环境里有会话标识）显示为「会话 X（调用方 C）」，其它情况是「调用方 C」，job 的 agent 是「job J」。会话标识只是同一调用方内的归属标注，不是认证。
- **权限**：发给自己的会话 / job / 收集箱不需要权限；发给别人的终端会话需要 `session.reply.others`，别人的会话 job 需要 `job.manage.others`。job 里的 agent 只能给自己 job 的来源会话、所属 plan 的主会话和它的调用方发信，也只能读写自己 job 的信。受限调用方（`ceiling`）只能给范围内项目的会话 / job 发信，范围外按「不存在」处理。
- **群发（广播）**：一封信可以发给多个会话，每个收件人各自一封、共用对话串与广播组。两种写法：`--to` 重复（每个都必须是唯一命中），或选择器前缀 `each:` 取全部可投递命中——`each:topic:<关键词>`、`each:path:<子路径>`、`each:project:<项目 key>`（该项目全部会话）、`each:agent:<名字>`（该类 agent 的全部会话）。不带 `each:` 的地址行为不变：匹配到多个仍转「待指派」。
  - 权限逐个收件人判：**显式列出**的收件人里只要有一个没权限，整封拒绝并说明是哪个；`each:` 选择器按权限过滤，结果里写「匹配 N、投递 M、略过 K」（略过的只报地址与原因，不回报会话 id）。
  - 限制：不能群发急件；job / steward 凭据不能发广播；0 个收件人报错；单次最多 `mail.broadcast_max_recipients`（默认 20），每个调用方 `mail.broadcast_rate`（默认 5 次 / 10 分钟，一次广播算一次）。超出分别是 400 / 429。
  - 收件人看到的标签多两个属性：`<gofer-mail … broadcast="b12" recipients="5">`——说明这是群发，不必以为只发给了自己；回信照旧只回发件方（没有「回复全部」）。
  - 广播副本过期只标 `expired`，不进收集箱；发件人 `session mail read <id>` 看到的是这一封副本，web 信箱里能看到整组收件人状态。
  - web：写信框的收件人是**勾选**——搜索框可按会话名 / 标题 / agent / 项目筛会话、勾多个；再加「按项目 / 主题 / agent / 子路径全部」选器（生成 `each:` 地址）；已选显示为可删除标签，手写地址收在「高级」里。下方按服务端预览显示「将投递 M 个会话（略过 K 个）」（`POST /v1/mail` 带 `dry_run`，不落库也不占广播限流）；对话里的广播副本卡带收件人状态条（已送达 / 已回复 / 过期 / 待送达）。

- **发给常驻 agent（`agent:<名字>`）**：地址写 `agent:<名字>`（项目取发件方所在项目）或 `agent:<名字>@<项目>`。名字必须是 server 配置 `mail.agents` 里写过的（没配过返回 400 并列出可用的名字），配置里 `agent:` 指向一个 acp-agent。gofer 为每个（名字, 项目）保留一个**常驻 ACP 持续会话**：有活着的就送进去，没有就新建一个（agent 声明 `loadSession` 时续接上一个会话的上下文），收满 `max_letters` 封或存在超过 `max_hours` 小时则轮换新会话，空闲 `idle_minutes` 后由会话自己的 idle-timeout 结束。全局最多同时 `mail.agents_max_resident`（默认 6）个，超出的信件转「待指派」并写明原因。启动与投递都在投递器里异步做，发信不等待。收件 agent 只用 gofer 的 MCP 信箱工具回信：`gofer_session_mail_reply`（原信 id + 正文），不要用命令行（`gofer session mail reply`）或 shell，那样每封信都要人批；回信挂在原对话串；它只在会话里输出文字不算回信。常驻会话的 gofer MCP 由 gofer 自己注入（带该 job 的凭据），不需要在 `mcp_servers` 里配、也不需要 agent 自己的 MCP 配置。
- **常驻会话的查看与结束**：`gofer session mail agents` 列出配置的名字与当前常驻会话（项目、job、信件数、待投递数、状态），`--end <名字> [--project <项目>]` 手动结束一个；web 信箱页的「常驻 agent」区同样能看与结束。发 `agent:` 信件需要 `job.submit`（可能启动新的会话 job）。
- **收件会话单独的审批策略**：`mail.agents.<名字>.approval` 给这个常驻会话单独设 ACP 审批（结构与项目的 `approval` 一样：`mode` / `auto_allow_kinds` / `ask_kinds` / `auto_allow_tools` / `ask_tools` / `timeout_sec` / `on_timeout` / `remember_allow_always`），例如 `approval: {mode: ask}` 让它在写文件、执行命令前逐次问人，而项目里其他 ACP 会话仍按项目策略走。不写 = 沿用项目策略。它**替代**（不是叠加）项目策略，只有 agent 自己的 `acp.permission_policy` 还能继续收紧；审批照常走 job 的 interaction（web 交互面板、钉钉/桌面通知）。策略随 job 保存，`job resume`、会话续接与 server 重启后的恢复都仍按它审批；续接出来的新常驻会话继续用同一条配置（改了配置则按新配置）。这是 server 内部字段（`session_approval`），**HTTP / MCP / CLI 提交 job 时带它会被 400 拒绝**——想改审批就改配置。等审批的时间不计入这个会话的每轮超时（审批期间该轮计时暂停），所以人回得慢不会把一整轮会话打断；审批自己的 `timeout_sec` 与 `on_timeout` 照旧兜底。
- **回信不需要审批**：`approval: ask` 下，常驻会话调用 `gofer_session_mail_reply` 回复**它自己收到的信**一律自动放行（记在 `acp.jsonl` 的 `reason: mail_resident_reply`），不会每封信找人批；其余 gofer 写工具（如 `gofer_session_mail_send`、`gofer_run_job`）、写文件、执行命令照旧按策略审批。工具名只认本会话实际拿到的 gofer MCP（gofer 为常驻会话注入，名字固定 `gofer`），别的 MCP server 的同名工具不会放行。服务端另有一道校验：job 凭据只能碰本 job 的信件，以及它当前作为那个 `agent:<名字>@<项目>` 常驻会话收到的信——回复别的对话串 / 别的常驻会话的信件仍被 403 拒绝。
  - 启动、轮换、空闲结束、启动失败都记审计事件（kind `mail_resident`）；启动失败的信件留待下次重试（间隔约 2 分钟）。
- **限额**：单封 32 KB；每个发件方每分钟 10 封、每小时 100 封；同一对话串自动往返超过 20 封后，回信转待指派。
- **地址里的会话 id**：`session:<sid>` 可写完整 id 或前缀，规则同 `--session`：唯一匹配就按完整 id 投递；多个匹配报错并列出候选完整 id（最多 8 个，HTTP 409）；没有匹配报 not found（HTTP 404）；已结束会话的前缀仍能匹配，信转待指派。发信人身份取自环境里的会话变量（见上），suga 子进程里发的信记作 `session:<SUGA_SESSION_ID>`，收件会话回信才送得回来。
- **秘密扫描与脱敏**：`gofer job secret-scan` 的报告多一段 `mail`（信件 id、所在串、收发双方的显示名、状态、项目、命中位置与计数，不回显原文），普通 caller 只看到自己发或自己会话 / job 收到的信；`gofer session mail redact <id>` 按信件 id 抹掉正文与摘录里的秘密，同时改写收集箱条目里引用这封信的摘要（条目文本的正文摘录、条目日志的回复摘要），信件的 id 引用保留；`job secret-scan --redact --yes` 连同命中的信件一起脱敏，`job redact <job>` 也会处理带该 `job_id` 的信件。权限与 `job redact` 一致：信件收发双方（发件调用方、收件会话 / job 的属主）或持有 `job.delete.others` 的管理员。
- **保存期**：终态信（delivered / answered / expired / failed / cancelled）超过 `storage.mail_retention_days`（默认 90 天）由 serve 的清理循环删除；待读 / 投递中与待指派（held）的信不删，收集箱条目里的摘要与信件 id 引用保留。
- **送达**：信件由收件会话的 hook 取走并注入，不走 web 消息通道，注入块带固定标签 `<gofer-mail id="…" from="…" topic="…" urgent="…">…</gofer-mail>`（不算有人在键盘前）。
  - **普通件**：在会话**下一轮开始**时注入（SessionStart / UserPromptSubmit），与 job 完成通知、记忆在同一份附加上下文里。单次最多 5 封、合计 8000 字；超出的留到下一轮，注入里会多一行「还有 N 封待读信件…」，也可 `gofer session mail ls` 查看。单封超过 8000 字时截断并提示 `gofer session mail read <id>`。
  - **急件**（`--urgent`）：会话正在一轮里时，在下一个工具调用前（PreToolUse）注入，只取急件（只在 hooks 配了 PreToolUse 的工具前触发：`gofer init hooks` 生成的是 shell 命令前）；该事件每次工具调用都会触发，所以每个会话最多约每 30 秒检查一次（急件在当前一轮中最多约 30 秒后注入），单次取件有 300ms 上限，超时或出错后 2 分钟内不再检查，信件留在服务端，下一轮再送。
  - **用户离开且已有待信**：会话结束一轮（Stop）时，若此刻已有待读信件，且按 relay 规则判定人不在键盘前（与 job 等待的 `away` 同一规则，不受 `stop_job_wait` 影响），就把信件作为继续的理由送出；若 relay 正在等 web 回复，则服务端原子地关闭这一轮并取走信件，和 web 回复同时到达时只有一方成功（输的一方不丢：信件仍待读，或回复仍被注入）。**不会为等一封还没到的信而阻塞**。
  - 至多一次：hook 先向服务端确认再注入；确认失败则不注入，租期（60 秒）过后信件回到待读。收件方自己 `session mail read` 读到的信即视为已送达，之后不再注入。
  - 只对能收到 hook 附加上下文的 agent（Claude Code、codex）注入；其它 agent 仍需 `session mail read`。
  - **空闲会话的唤醒**（server 投递器，每 30 秒扫一次）：会话状态为 `idle` 或 `offline` 且 `last_seen_at` 早于 5 分钟、并有待读信件时，发**一条**唤醒提示 `<gofer-mail-wake count="N"/>` + 一句「有待读信件，将在本轮开始时注入，无需回复」触发一轮，信件在该轮开始时由 hook 注入（唤醒本身不带正文）。急件在会话 `idle` 时不看 5 分钟阈值，立即唤醒；`waiting_reply`（在等人回复）、`running`、`needs_attention`、`ended`、`handed_off` 的会话不唤醒；同一会话 10 分钟内最多唤醒一次（失败也记入，不会每次扫描重试；重启 server 只意味着可能多唤醒一次）。唤醒走独立通道，按顺序：agent 的送话命令（codex 的 `codex queue`）→ Claude SendMessage 传话人（一次性 job）→ tmux 注入；**不是 web 消息**：不走 `Say`、不写会话 outbox、不加 `[来自 web` 前缀，对唤醒提示的任何回复都被丢弃，不会记成 web 回复。会话自己恢复（重新登记 / 心跳）不需要特殊处理：心跳回应带 `mail_pending`，下一个 SessionStart / UserPromptSubmit 照常补送。
  - **suga 会话**：不靠 hook，server 投递器把**正文**经 `suga send`（该 agent 的送话命令）直接送进去，包在同样的 `<gofer-mail id="…">` 标签里；`suga send` 返回 3（会话进程不在线）或失败时信件保持待读（立即还回，约 2 分钟后再试）。server 侧路径是**至少一次**：先占用（租期默认 60 秒）、送出、再确认，送出后确认前出错会重发，所以标签带信件 id，收件方可据此识别重复。
  - **ACP 会话 job**：job 处于 `awaiting_input` 时用 `job say` 送一封（带标签，同样占用 → 送出 → 确认），一次只送一封，等 job 下一次 `awaiting_input` 再送下一封；不在 `awaiting_input` 的会话 job 信件继续等。**pty（交互）session job 与一次性作业不支持收信**：pty 的信不在 `awaiting_input` 也无法说进去，立即转「待指派」进收集箱（原因 `pty session jobs cannot receive mail`）；一次性作业的新信在发信时就被拒绝，升级前已经存下的这种信由投递器下一轮立即转待指派。
  - **收件 job 中途结束**：已接受的信在收件 job 进入终态（done / failed / cancelled …）后**立即**转「待指派」并退信，不再等到送达期限。
  - **收件方不在线 / 送达期限**：不可达或已结束的会话，信件保留到送达期限（默认 24 小时）；同一会话恢复时补送；超过期限仍未送达的信件由投递器转「待指派」进收集箱（并发退信告诉发件方），由用户改投、新建 job 或丢弃。
  - 数值可在 server 配置 `mail:` 块调整（`tick_sec` / `wake_idle_sec` / `wake_interval_sec` / `deliver_by_sec` / `claim_lease_sec` / `broadcast_max_recipients` / `broadcast_rate`，见 server-config.md），热重载生效。
- **退信（收不到就告诉你）**：信发出后如果**后来**才确定送不到——收件 job 结束了、收件方是 pty / 一次性作业、超过送达期限转收集箱——发件方会收到一封 gofer 自己写的信：正文以 `[gofer · 退信]` 开头，写明**哪封**（`信件 #97（发给 job:…）`，急件带「加急」）、**为什么没送到**、**现在在哪**（如「已转入收集箱待指派（条目 wi-…）」），并附原信正文摘录。发件方是会话时按普通信件规则注入 / 唤醒；急件的退信也是急件。退信本身不会再被退信（发件方是 gofer），广播副本过期也不退信（一封群发会退成 N 封）；发件方是 job / 调用方（没有可注入的信箱）时退信只作记录（`session mail ls` / web 里能看到，状态 held）。发信时就能判定的问题（地址没有命中、地址是 pty / 一次性 job）当场答复，不算退信。退信没有发件方可回，`session mail reply` 回退信会报错（409）。
- MCP：`gofer_session_mail_send`（`to` 接受地址数组，或 `each:` 选择器 / `body` / `urgent`）、`gofer_session_mail_list`（`box` / `thread` / `state` / `session` / `limit`）、`gofer_session_mail_read`、`gofer_session_mail_reply`（`id` / `body` / `urgent`）、`gofer_session_mail_cancel`，语义同 CLI，需要 gofer server（单机本地模式会报需要 server）。

## 给不在等回复的会话送话

会话没有等待中的 turn（在干活或已空闲）时，`session say --deliver`（web 抽屉的「送入终端」）按顺序尝试：

1. **agent 的送话命令**（`deliver_command`）：在会话登记的执行机上跑这条命令，退出码 `0` = 已送达，`3` = 进程不在线（继续往下），其它 = 失败（不再退到 tmux，避免重复送）。内置默认：codex 用 `codex queue` 排队消息（已退出的会话下次 resume 时收到），suga 用 `suga send --session <id>`（文本走 stdin，`suga send` 自己对不在线的会话就返回 3）。
2. **tmux 注入**：确认 pane 存在、前台是 agent CLI（默认白名单 `claude|codex|omp|node|gemini|opencode`，`session.inject_commands` 可配），把 `[来自 web 回复，<用户>] …` **整段粘贴**进去（一个 tmux buffer + 括号粘贴 + 一次回车，多行消息还是一条输入；上限 8KB）。
   - **先看输入区再打字**（默认模式 `auto`）：用 `tmux capture-pane` 读 pane 最后几行，只有确认为**空输入框**才投递；输入框里有半截文字、或停在选择界面（AskUserQuestion、权限确认）时不打字，消息改为**留待**（不会丢，见下）。识别不了也算不确定，同样留待。
   - 投递模式按会话设：`auto`（默认）/ `hold`（从不打字，消息一律留待）/ `force`（不检查直接粘贴，只在确定那个终端没人用时选）。CLI `gofer session inject-mode auto|hold|force`（默认取当前目录的会话，`--session <id>` 指定），web 会话页可选择。
   - 留待的消息进会话信箱（`mail`），由会话下一次 SessionStart / UserPromptSubmit / Stop hook 注入；web 会话页显示待读数量并可「现在送」（立刻粘贴一次，跳过检查）。每次留待都在审计事件 `session.inject_hold` 里记原因（输入框有内容 / 选择界面 / 不确定 / hold 模式）。
   - 前提：会话跑在 tmux 里（建议 `tmux new -A -s claude`）且登记了执行机。**容器里的会话**要在容器内起一个 gofer worker，并在容器 `.env` 配 `GOFER_HOOK_RUNNER=<worker-id>`；纯客户端节点不会登记执行机。
   - 注入是一个 exec job，项目需 `allow_exec: true`（worker 侧 `guards.allow_exec` 也不能收紧）。
3. 前两步都不行时，`--takeover` 起新进程接管（见下）。

失败原因：`no_runner`（未登记执行机）、`no_tmux`（不在 tmux）、`ended`、`deliver_failed:<原因>`、`inject_failed:pane_missing`（pane 没了）、`inject_failed:pane_busy:<cmd>`（前台是别的程序，如 vim）、`inject_failed:runner_error`、`not_delivered:<job 状态>`（那只内部 job 还没来得及跑就被撤销，例如更新等待空闲把新 job 停住：什么都没送出去，可以放心重试）。

留待不是失败：投递返回 `path=held`（web/CLI 会说明消息在等下一个 hook），消息在会话信箱里，`gofer session mail ls` 看得到。

送话的内部 job 跑得比服务端等得久（超过 25 秒）时不会被当成失败：job 已开始 → 这次投递是**送达中**，job 结束时回写真实结果；job 尚未开始 → 撤销它并报 `not_delivered:<状态>`。web「发消息」用的一次性传话人 job 超出 `session_messaging.timeout` 时同样处理：已开始记「送达中」并在终态回写（要有 `已发送` 回执才算送达），未开始则撤销并记「未送达」。

## 唤醒 / 接管会话

- **接管**（`--deliver --takeover`，web「起新进程接管并发送」+ 二次确认）：在同一 runner、同一项目相对目录起一个交互 pty job（`claude --resume <sid>` / `codex resume <sid>` / `omp --resume <sid>`），把消息作为首条输入，web 跳到该 job 的终端继续聊。
- **唤醒**（`gofer session resume <id> [--input …]`，web Sessions 每行与会话抽屉的「唤醒 / 接管」按钮）：同一种 `--resume` pty job，**已结束的会话也能唤醒**。`--plan` 只问能不能、在哪个目录起，不起进程。不能唤醒时按钮灰显并写原因。
- 前提：agent 有交互续接模板（内置 claude / codex / omp）、项目 `allow_interactive: true`、会话目录能换算成执行机上的项目相对路径；否则报 `no_resume_template` / `interactive_not_allowed` / `cwd_outside_project`。
- 唤醒目录：优先用会话记录文件验证出的原始启动目录，其次登记的 cwd，再次项目根。
- **原进程还在线就拒绝**（`session_alive`）：最近一次心跳在 `session.takeover_alive_sec`（默认 120 秒，0 关）内算在线，避免两个进程写同一会话分叉。会话显示未结束但你确定终端已关时，先关掉原终端再接管。
- 接管后会话为 `handed_off`，原终端的中继停用（hook 在 stderr 提示）；继续请在接管终端里。想回原终端：web「解除接管」或 `gofer session release-takeover <id>`（先取消接管 job）。接管 job 结束时自动释放；本来已结束的会话回到 `ended`。
- `gofer job ls --tag relay-takeover` 列出接管 job。

## web 发消息（转达）与会话回复

- 会话不在等回复时，web 的「发消息」经**传话人**把话转给目标会话（在会话所在 runner 上用 `claude -p … --allowedTools SendMessage,ListAgents` 转发；server 本机与较新的 worker 用常驻传话进程）。需要目标是有会话间通信能力的 Claude Code 会话。
- **接收方看到的发送方是 `gofer-web`**，正文以 `[来自 web，<用户>]` 开头：这是用户本人在 web 上发来的消息（由 gofer 起的传话人转达），按用户的话对待即可；但它不是终端里的审批操作——需要批准高风险操作时，仍等用户在中继里明确回复或在终端确认。
- **gofer 自己的消息不穿这个前缀**：gofer 送进会话的系统消息一律以 `[gofer · <类型>]` 开头（`job 完成`、`唤醒`、`工作项交接`、`工作项汇报`、`管家带话`、`记忆`、`提示`），不论走传话人、Stop 等待还是补发都是同一张表。`[来自 web…]` 一律是**人**发的；`[gofer · …]` 是机器发的，不要当成用户回话或授权。前缀按会话所属项目的语言写，所以英文项目里可能是 `[gofer · job done]`、`[gofer · memory]`，人的消息是 `[from web, <user>]` / `[web reply, <user>]`；这两种语言是同一张表，识别时两种都认。
- **作为目标会话的 agent**：收到 `[来自 web，…]` 的转达，直接用 SendMessage 回给 `from` 地址，原话会出现在 web 对话里；也可以正常在终端回答。前提是装了 `gofer init hooks` 的 PostToolUse 条目（缺省就装）。
- web 对话流里显示送达状态：**排队中 / 送达中 / 已送达 / 未送达 / 失败**。「送达中」= 内部送达 job 已开始但还没结束（更新等待空闲时最常出现），结果在 job 结束时回写，不要把它当成失败再发一遍。「重试」只对**未送达**开放——那是确实没送出去的（没试过，或 job 未启动就被撤销）；「失败」说明投递试过了、结果不明（送话命令非 0 退出也可能已经送到），重试可能重复送达。失败原因里会写明那只 job 的状态与退出码，不只是 `exit code -1`。Runners 页每台 runner 的卡片显示传话人状态（未启动 / 空闲 / 处理中），抽屉里有最近投递、错误输出尾部与「列出可见会话」。配置 `server.session_messaging` 见 [server-config.md](server-config.md)。
- 「无需回复」：会话停在等回复的 turn 上时，web 可标已读（可撤销）。这不是回答、也不结束等待，只是从「待我决策」里消掉；要放行用 `/off` 或 `relay off`。
- **gofer 发给会话的文本按会话所属项目的语言写**：job 完成通知（中文 `耗时 1m30s（这是 gofer 系统通知，不是用户消息；…）`，英文 `took 1m30s (this is a gofer system notice, …)`）、授权 / 选择题卡的最后消息与卡片问题（`需要授权：Bash …` / `Permission needed: Bash …`，`等你回答：…` / `Waiting for your answer: …`）、唤醒 / 接管失败的解释、传话人提示词。传话人按提示词里的回执词回答（中文 `已发送`、英文 `sent`），两边都认；判断这些文本是不是机器写的，看 `[gofer · …` 开头即可，与语言无关。

## session watch：job 跑完时被叫醒

`gofer session watch <job-id>`（省略 `--session` 按当前目录解析）把当前会话登记为盯这个 job；会话停下时，job 终态通知注入回终端。

- 即使中继关闭，只要还有未结束的 watch，Stop 也会只等 job 事件（不转发 web 输入）；错过的通知在下一次输入 / 会话开始时补投一次（claude / codex；omp 靠 Stop，jcode 只观察）。这种**只等 job** 的等待也会在会话里的子 agent 全部跑完时立即放行（进入等待时就有子 agent 在跑才算）——否则后台子 agent 的结果要等某个 job 结束或 `--wait` 用尽才能被主 agent 读到；同一轮里 job 也结束了的话仍优先送 job 完成通知。
- **只等 job 会关住你的终端**，所以 `session.stop_job_wait`（默认 `away`）管着这件事：`away` 只在人不在时等（判据同 auto 布防：`relay=on`，或 `relay=auto` 且键鼠空闲超过 `auto_relay_idle_sec`；空闲读数未知 —— 容器里读不到主机键盘 —— 算「人在」）；`always` 旧行为（有人盯着的 job 不结束就一直等，最长 `--wait`）；`never` 一律不等。人在键盘前时 Stop 立即放行，终端照常能输入，job 结束通知仍在下一次输入 / 会话开始时补投——三个取值都不会丢通知。
- hook 在自己机器的配置文件里读到这个键就以它为准；读不到（例如只跑了 worker 的容器）就用 server 这份（server 在每次会话响应里带上）。
- 你 `gofer job run` / `watch` 后 PostToolUse hook 会自动登记；Stop 还会自动认领「来源会话就是本会话、且 caller / 项目 / runner / cwd 都匹配」的在途 job。其它 job 用 `session watch` 显式关联。
- **不等自己停下也想被叫醒**：派 job 时加 `--notify`（目标会话 = 提交方，或 `--notify-session <sid>`），job 终态时 gofer 直接用传话人推一条 `[gofer · job 完成] …（这是 gofer 系统通知，不是用户消息）` 给那个会话，不必等它下一次 Stop / 输入。（在 agent 会话里提交的 `--hold` 待批 job 默认带这个通知，`--no-notify` 关掉。）
  - 两条路径不会重复：通知**送达成功**就消费掉对应的 watch，Stop hook 与补发不再发；送达**失败**保留 watch，由原来的补发兜底；同一个 job 只通知一次（server 重启也不重发）。
  - **会话正停在 Stop 的「只等 job」等待里时不动用传话人**：那个等待自己轮询 watch，通知留它来送（传话人的消息送不进被自己的 Stop hook 挡住的会话，只会积压到你下一次发消息）。等待结束或未在等待的会话照旧走传话人。
  - 默认关闭：每条通知都起一次传话人进程。不想被通知就不加 flag；派长 job、准备离开时加上。见 [job-advanced.md](job-advanced.md)。
- **plan 级开关**：`plan create --notify` / `plan set <plan> --notify on|off` 让该 plan 派发的每个 job 都通知它的主 Agent 会话（消息从「等你停下」变成主动推送）；没绑主 Agent 会话时不通知任何人，`plan show` 会提示。见 [plans-workflows.md](plans-workflows.md)。

## 会话催办（nudge）

```bash
gofer session nudge <id> --when-stalled 20m -m "进展如何？" [--until 2h]   # 运行中（或已停下但工作项未结）且 20 分钟无进展就发
gofer session nudge <id> --every 30m -m "…"                                # 定时发（最小间隔 1m）
gofer session nudge ls [<id>] [--all]
gofer session nudge rm|pause|resume <nudge-id>
```

- 送达走与 web「发消息」同一条路径；连续 3 次送达失败自动 `paused`（可订阅事件 `session.nudge_paused`），`resume` 清零后继续。会话结束 / 被接管 / 到 `--until` 时自动结束；`offline` 的会话跳过。
- 只有人（会话属主或有回答权限）能建、改、删；job 凭据与管家不行。web 会话抽屉有「催办」小区块。

## 等另一个会话 / 看它在显示什么（wait / screen）

编排其他会话时不要再 `session ls` + `sleep` 轮询：

```bash
gofer session wait <sid> --until idle|waiting_reply|ended [--timeout 10m] [--json]
gofer session screen <sid> [-n 50] [--json]
```

- `wait`：阻塞到会话进入目标状态（`--until` 可逗号给多个，如 `idle,ended`）；已经处于该状态时立即返回。退出码 **0 = 达成、2 = 超时、1 = 错误**，超时不是失败，脚本可以据此换下一步；`--json` 打印最终状态（会话 + `state` + `reached`）。服务端是长轮询（单次最多 60s，CLI 自己循环到总超时），`GET /v1/sessions/{sid}/wait?until=&timeout=`。
- `screen`：只读取它现在显示的内容，纯文本、已去 ANSI：终端（中继）会话给你最近的 turn（`agent>` / `human>` 行，与 `session show` 同一数据源）；被 pty job（`--resume` 接管）承载的会话给你那个 job 的 `pty.txt` 尾部（与 `job logs` 同一读法）；还没有 turn 时给最后一条消息。`-n` 控制行数（默认 50，最多 500）。`GET /v1/sessions/{sid}/screen?n=`。
- 两者权限都与 `session show` 相同：自己的会话不需要权限，别人的要 `session.read.others`；MCP 里是 `gofer_session_wait` / `gofer_session_screen`。典型用法：`wait` 到 `waiting_reply` → `screen` 看它在问什么 → 人（或有回答权限的调用方）`session say` 回答。

## 自研 agent 接入（generic）

- hook：agent 自己调 `gofer hook generic --agent <gofer agent key>`，stdin 与 claude 同形 JSON；事件 SessionStart、UserPromptSubmit（可带 `"injected": true` 表示注入输入，不算人工输入）、PreToolUse（带 `tool_name` 与 `tool_input.command`，输出命令记忆）、PostToolUse、Stop、Interrupt、SessionEnd、Notification。Stop 阻塞，输出同 claude；最后一条消息取载荷的 `last_assistant_message`。
- agent 配置可加：`ndjson_usage_path`（用量）、`transcript_dialect: generic`（会话记录方言）、`inject_process`（tmux 送话前台进程名）、`inject_guard`（输入区识别规则：`empty` / `has_input` / `selecting` 三组正则，未配时用内置 claude / codex 规则；没有任何规则的 agent 不做 `auto` 检查）、`session_family`（与同族 agent 互相续接）、`deliver_command` / `deliver_stdin` / `deliver_offline_match`（在线送话）。配置样例见 [server-config.md](server-config.md)「agents」与 <https://github.com/inhere/gofer/blob/main/docs/runbook/session-relay.md>。

## 其它约定

- **在 gofer job 里不触发中继**：环境里有 `GOFER_JOB_ID`（你本身是 gofer 派出的 job）时 hook 直接放行。
- **忽略 agent 内部会话**：cwd 在 `~/.codex/memories` 或 `GOFER_HOOK_IGNORE_CWDS` 列出的目录时 hook 静默退出。
- **会话身份**：会话的重复登记与心跳要求同一个认证 caller。worker token 不是会话凭据：它只能调用工作进程自用的路由（自己的升级包、分配给自己的传输内容、自己的 assignable 项目），命中会话 / job / 配置接口一律 403 `worker credentials cannot call this endpoint`。
- **归属被拒与转移**：会话的 owner 是**首次登记**的认证 caller（`caller_id`）。换 token（例如把 agent / 容器 hook 换成自己的 caller）后 hook 以另一个身份上报，注册 / 心跳 / 回复都会被拒——拒绝是对的，但会话的状态、最新消息、进度会停在切换那一刻。最近一次被拒的 caller 与时间记在会话上（`GET /v1/sessions` 与详情的 `refused_caller_id` / `refused_at`），server 日志也有一条限频的 warn，hook 日志（`<config-dir>/run/hook.log`）会记一行并指出转移命令。
- **把会话转给该 caller**：web 的会话列表 / 详情会亮出「归属被拒」并给「转给 <caller>」按钮；CLI 是 `gofer session owner <sid> <caller>`（需要 `session.transfer`；未知 caller 报错列不出就是没配在 `server.callers`）。转移成功后 owner 变成目标 caller、被拒记录清空，它的心跳立即生效；状态、轮次、job 关注都不受影响。该会话自己名下的授权在同一事务里被撤销（见「会话授权」）。
- **让手机响一下**：配钉钉 / 飞书群机器人并显式订阅 `session.waiting`（不在默认集），消息带直达会话的链接。见 <https://github.com/inhere/gofer/blob/main/docs/runbook/im-notification.md>。
- 更多细节：<https://github.com/inhere/gofer/blob/main/docs/runbook/session-relay.md>。
