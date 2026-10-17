# 更新日志

本文件记录 gofer 每个版本中**用户可见**的变化（功能、行为变化、重要修复、兼容性说明），格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)（1.0 之前 minor 版本也可能含不兼容变化，见各条「变更 / 移除」）。

维护方式：

- 每次发版（打 tag）时新增一个版本条目；内容来源是**上一个 tag 到本 tag 之间的提交**（`git log <上一个 tag>..<本 tag>`），只挑用户可见的变化，纯重构、测试、tracker / 记忆同步、设计文档不写。
- 日期取 tag 日期；条目尽量带路线图编号（见 [`docs/gofer-enhancements-roadmap.md`](docs/gofer-enhancements-roadmap.md)）或 issue id。
- 尚未发版的用户可见改动先记在「未发布」，发版时整段移入新版本。
- v0.100.0 起逐版本记录；更早的版本按主题区间压缩记录（见文末「早期版本」）。

## [未发布]

### 新增

- **授权视图带 `effective`**：`GET /v1/auth/grants` 每行加 `effective`（此刻是否生效）；会话授权按设计的全部生效条件（状态、有效期、会话未结束、owner 一致、会话化身一致）由服务端用与鉴权同一份判定得出，会话结束后复活的授权不再显示为生效。web 的「已授权」徽标与设置 → 权限的会话授权列表改用它（gofer-x632）。

### 修复

- **web「发消息」的一次性传话人 job 超时不再写死失败**：传话人 job 超出 `session_messaging.timeout` 时，已开始 → 消息记「送达中」，job 结束时回写真实结果（需有 `已发送` 回执才算送达）；尚未开始 → 撤销该 job 并记「未送达」（可安全重试）。与会话送达 / 注入的口径一致（gofer-kx53）。

## [0.167.0] - 2026-10-17

### 新增

- **调用方显示名 `callers[].name`**：`server.callers` 每项可选加 `name`（如给主 token 的调用方 `default` 写 `name: admin`），只改显示：id 仍是 job / 授权 / 审计里记的身份，无数据迁移，不影响任何权限判定。加载时去空白，非空时全局唯一（忽略大小写）、至多 64 字符、不含控制字符。`gofer auth whoami` 打印 `name`；`GET /v1/auth/whoami` 加 `caller_name` 与 `caller_names`（id → 名称），`GET /v1/auth/grants` 加 `caller_names`，`GET /v1/config` 的 callers 行带 `name`；web 设置 → 权限显示名称、悬停见 id，配置页 callers 表加 name 列（gofer-6n3h）。

### 修复

- **`gofer init skill|hooks -a all` 不再为没装的 agent 建目录**：`-a all` 只对配置目录已存在的 agent 写入（hooks：`.claude` / `.codex` / omp / jcode 的配置目录；skill：`.claude` / `.agents`），其余输出「跳过：未发现 <dir>」；点名某个 agent（如 `-a claude`）或不带 `-a` 的行为不变，照常创建目录（gofer-vlvb）。
- **未知 `/v1` 路径返回 404 JSON**：`/v1/` 下没有匹配路由的请求（如已删除的 `/v1/agents/presence`、拼错的路径，GET / POST / HEAD 皆然）不再回退到 web 首页（200 HTML），改为 404 + `{error, detail}`；非 `/v1` 的前端路由仍回退首页（gofer-enho）。
- **Web：exec / 一次性 cli-agent job 详情不再显示「消息」区**：该区只对 ACP 持续会话 job（或确有多轮会话消息的 job）显示并请求 `include=messages`，避免与上方命令渲染重复（gofer-h7hy）。
- **`plan add-todo --after prev` 空 plan**：plan 还没有任何项时，`--after prev` 视为无依赖并成功添加（输出注明），不再报 `has no items yet`；非空 plan 行为不变（gofer-hm1p）。

## [0.166.0] - 2026-10-17

### 新增

- **会话主题与查找**：给会话记下「在做什么」，需要时按主题或路径找到该问谁。
  - CLI / MCP / hook：`gofer session topic "<主题>" [--scope <子路径>]… [--clear-scope] [--session <id>]` 记下当前会话在做什么，`gofer session find <关键词|路径>` 找谁在做；MCP 新增 `gofer_session_set_topic` 与 `gofer_session_find`。当前会话只按环境变量识别（不按目录猜），识别不到或不唯一时要求 `--session`。SessionStart hook 在会话还没有自己的主题时，往 additionalContext 加一行提示 agent 记主题。
  - HTTP：`PATCH /v1/sessions/{sid}`（主题与范围，空串 `topic` 清除）、`GET /v1/sessions?q=&path=`（查找，返回命中原因与「可能不在线」）；自己的会话不需额外权限，改他人的会话需 `session.manage.others`。
  - Web：Sessions 卡片与会话抽屉显示主题与范围，推断出来的主题灰显并标「推断」，自述 / 人工显示来源小标签；可就地编辑或「清除主题」（已结束的会话不能改并说明原因，范围按项目根相对路径逐行填，提交前先校验）。Sessions 页新增查找框：含 `/` 的无空格输入按路径，其余按关键词，结果按相关度排序并显示命中原因与「可能不在线」。
- **收集箱：随手记下想法，之后再整理**。
  - 记录：`gofer work capture "<内容>" [--kind idea|issue|request|note] [--project <p>]`（kind 缺省 `note`）；MCP 新增 `gofer_work_capture`（job 凭据下记录来源 job，来源会话按环境识别）；Web 顶栏「随手记」（想法 / 问题 / 需求 / 记录 + 可选项目，手机贴底面板），记下后提示条带「去收集箱」链接。HTTP：`POST /v1/work-items/capture`。
  - 整理：Works 的「待整理」区分「草稿」「收集箱」两个页签（`/work?tab=capture` 直接打开），每条显示类型、来源会话 / job 与记录时间；Today 出一张「收集箱 N 条待处理」汇总卡，「去整理」跳到收集箱。收集箱条目不出现在看板、分组、日报、整理扫描、健康检查、到期提醒、合并建议与 Today 泳道里。
  - 分派：条目抽屉的「分派…」对话框（Web）与 `gofer work dispatch <id> --to session|job|todo|issue|archive|drop`（CLI）；`session` 发给会话（失败则条目留在收集箱并给出原因），`job` 关联已提交的 job（结束时终态写进条目日志；Web 新建 job 页 `?from_item=<id>` 预填并在提交后自动关联，可重试），`todo` 沿用 `to-todo`，`issue` 在仓库镜像里新建 issue，`archive` / `drop` 直接结案。HTTP：`POST /v1/work-items/{id}/dispatch`；他人的会话 / job / plan 分别需 `session.reply.others` / `job.read.others` / `plan.manage.others`，缺 `job.submit` 时 Web 不显示新建 job。
  - 目标推荐：`GET /v1/work-items/{id}/dispatch-targets` 按条目文本与会话主题、范围、会话名、项目推荐最多 3 个候选会话并给出命中原因（推断的主题单独标记；他人的会话按 `session.read.others` 先过滤）；Web 分派对话框直接展示。
  - 保护：`captured` 条目不会被汇报、整理器的状态建议、管家或 job 凭据改动状态；人用 `PATCH /v1/work-items/{id}` 把状态改出 `captured` 视同归档并写日志。
- **server 新建 tracker issue**：`POST /v1/tracker/repos/{tracker_id}/issues`（`{title, description?, priority?, labels?, type?}`）在 server 镜像里新建 issue，id 按仓库前缀分配，仓库还没有前缀时 409；issue 在仓库拉取前带「待同步」标记（`server_created`，work 关联里是 `pending_sync`），下次 `gofer repo sync` 进本地。Web 的 Issues 页新增「新建 issue」表单（只列有 id 前缀的仓库），收集箱「转 issue」用它建（kind 为 `issue` 记 `bug`，其余记 `task`，关联条目并改为 `parked`）。
- **`gofer work park --captured`**：回滚辅助，把收集箱里全部条目转为 `parked`（各写一条「从收集箱归档」日志），与 `<id>` 二选一；不删数据。

### 变更

- **`POST /v1/jobs/{id}/findings/issue` 改走 server 新建路径**：建出的 issue 同样带「待同步」标记，并参与下面的 id 冲突改名。
- 权限参考文档（`docs/reference/permissions.md`）登记了新增的路由：会话改主题、收集箱的记录 / 推荐 / 分派、server 新建 issue。

### 修复

- **仓库推送同 id 的新 issue 时不再逐字段静默合并**：仓库在拉到 server 新建的 issue 之前，推送了同 id 的新 issue 时，server 给自己的那条换新 id（同时改写镜像内的父子 / 依赖引用和工作项关联，issue 备注与工作项日志留一笔），两条都保留。已知例外：仓库首次同步（无 `sync-revs.json`）。

## [0.165.0] - 2026-10-17

### 新增

- **web「设置 → 权限」**：根管理员可在网页上管理授权，不用再手写接口。四个页签：授权（按 `users` / 调用方分组，标出来自配置、孤儿，以及「有新权限待确认」，孤儿可「确认恢复」）、会话授权（所有者、有效期、状态，可撤销）、权限目录（风险、可否授予会话、范围、来源）、审计（按对象 / 权限 / 授权 id 过滤，最新在前）。新增授权可选常用预设或直接写模式，实时显示展开结果；高风险权限要确认，危险级必须写出完整权限名并填写原因。普通账号和没有根管理员的部署只能查看目录与自己的权限，页面会说明原因。
- **Sessions 页给会话授权**：会话卡片与详情抽屉新增「授权」（仅根管理员）：只列可授予会话的权限，有效期 1 / 4 / 24 小时，弹窗里写明「谁能在这个终端会话里运行命令，谁就拿到这些权限」以及授权随会话结束失效；已授权的会话显示「已授权」，抽屉里可撤销。
- **控制台按权限隐藏操作**：升级 worker、添加 / 重载 worker、一键更新、写配置、项目增改删、删除他人的 job 记录、托管转发与预设、停他人的转发，没有对应权限就不再显示。
- **`GET /v1/auth/expand` 预览授权模式的展开结果**（需 `auth.manage`）：返回某个对象（`users` / `caller:<id>` / `session:<sid>`）实际会得到的权限，以及被规则挡下的权限和原因；网页预览改用它，展开规则只在服务端一份。
- **权限不足的提示指向「设置 → 权限」**：网页里所有因缺权限而被拒的操作，现在显示缺少的权限名和去哪里处理。
- **plan 详情页可开关结束通知（gofer-3db4）**：链控制区新增「结束通知」开关，对应 `PATCH /v1/plans/{id}` 的 `notify`；没绑监督会话时会提示不会通知任何人。

### 移除

- **移除 presence / driver 与 supervisor 自动作答**：不再有 agent 在线登记与收件箱（MCP 工具 `gofer_register`、`gofer_poll_inbox`、`gofer_post_message`、`gofer_list_presence`，命令 `gofer presence`，`/v1/agents/presence` 与收件箱相关接口，web 的 Drivers 收件箱页与 Agents 页的在线 driver 列表）；不再有 supervisor 规则作答、升级路由、AI supervisor job 与其对账进程，以及作答分级守卫（answerguard）。同时去掉 job 的 `origin_agent` / `escalate_to` 字段、交互作答请求的 `responder` 字段和统计里的 `drivers`。
- 待答的交互仍由人在「今日」页与推送里作答，顶栏的决策入口与 plan 的监督会话不受影响。
- **旧配置段被忽略并给出警告**：配置里残留的 `supervisor:`、`presence:` 段不再生效，会在 `serve` 日志、`gofer config validate` 和配置接口（`GET /v1/config` 与写接口响应的 `warnings`）里提示；配置页顶部也会显示。该兼容处理计划在 v0.168.0 移除。
- **plan leader 配置移到顶层 `leader:`**：字段不变（`enabled`、`agent`、`scopes`、`on_member_done`、`wake_delay_sec`、`max_rounds_per_scope`），原来写在 `supervisor:` 下的 leader 块不再被读取。已启用 leader 回合的部署升级前需把该块移到顶层，否则 leader 回合会关闭；`GET /v1/config` 与配置页也改为显示顶层 `leader`。

## [0.164.1] - 2026-10-16

### 修复

- **升级等待空闲时，阶段公开前就暂停新 job（gofer-tzwo）**：此前「等待空闲」阶段先对外可见、过一小会儿才开始暂停新 job，其间提交的 job 会照常启动而不是被停住；现在先暂停、再公开阶段。v0.164.0 的 CI 因此失败，该版本不要使用，请直接升级到 v0.164.1。

## [0.164.0] - 2026-10-16

### 新增

- **权限审计与使用计数（gofer-kd8q）**：授权新增 / 撤销 / 恢复分别记录为 `grant.create`、`grant.revoke`、`grant.activate`；只靠会话授权通过的请求记录为 `request.session_grant`，被拒请求记录为 `request.denied`。会话授权行同步维护 `last_used_at` 与 `use_count`；审计记录按主体、权限、授权 id 过滤，并保留 90 天。

## [0.163.0] - 2026-10-16

### 修复

- **送达 job 超出等待时间不再被误记为失败（gofer-qzv5）**：web 转达 / 注入的内部 job 若在服务端等待窗口（25s）内没跑完，现在按两种情况处理：job **已开始** → 消息记「送达中」，job 终态时回写真实结果（job 结束钩子写，读 outbox 时也会补一次以覆盖重启丢失的写回）；job **尚未开始**（典型：更新等待空闲把新 job 停住） → 撤销该 job，消息记「未送达」并写明原因和 job 状态，可以安全重试。此前两种都报 `deliver_failed:exit code -1: job …`，而 job 后来真的送达了也不回写，web 一直显示失败、点「重试」会重复送达。
- **outbox 多两个状态，重试范围收紧**：新增 `delivering`（送达中）与 `undelivered`（未送达，确定没送出去）；web 只对 `undelivered` 开「重试」——`failed` 是「投递试过了、结果不明」（送话命令非 0 退出也可能已经送达），再发可能重复送达。失败原因里现在带上那只 job 的状态与退出码（如 `job … 状态 failed exit=5：…`），不再只是一个 `exit code -1`。
- **送达 / 注入 / 传话人 job 不受「更新等待空闲」停住**：这些是 gofer 自己的短内部 job（tag `relay-deliver` / `relay-inject` / `session-messenger`），停住它们只会丢掉要送的消息，而重启不差这几秒；人 / agent 提交的 job 照旧停住。

### 新增

- **会话授权真的生效了（gofer-kd8q）**：给一个终端会话（主体 `session:<sid>`）发的授权现在会作用到那个会话的请求上——容器里的会话要开隧道、监督会话要发版升级，都可以只给它自己发，不用把权限给整个 token。一条会话授权生效要同时满足：请求带了该会话 id、发起人是会话当前的 owner、会话化身没变、会话没结束、授权没撤销也没过期。有效期 1 / 4 / 24 小时（「到会话结束」也带上限），接口见 [`docs/reference/http-api.md`](docs/reference/http-api.md)。查自己：`gofer auth whoami` 现在打印请求附带的会话 id 与**来源变量**，以及靠会话授权拿到的权限（来源 `db`，并给出是哪条授权）。
- **会话化身：旧授权不会因为会话「复活」而回来（gofer-kd8q）**：会话每次「离开 ended」（重新登记、心跳复活、接管一个已结束的终端）都换一个新化身，删除后重新登记也一定是新的；会话授权绑的是授权那一刻的化身。读时还判 `ended_at` 为空，所以「结束 → 接管 → 释放」也带不回旧授权。

### 变更

- **撤销授权会断开靠它的长连接（gofer-kd8q）**：长连接（隧道、attach 终端、SSE 日志流、`/v1/ws` 推送、托管转发）在建连时按「调用方 id + 依赖的授权」登记。授权的撤销或到期、会话化身变化或会话结束、以及配置重载后调用方被删 / token 变了 / 根身份变了 / 推出的授权变了，都会让这些连接**重新判定**：不再持有所需权限的断开（撤销和重载当场断，到期与会话结束由周期重判接住，最长 10 秒），仍然有权限的——比如同一项权限另有来源（配置推出的授权、另一条授权、自己的资源）——照旧保留。以前这些连接只在建连时校验一次，撤了授权也一直开着。
- **`/v1/ws` 推送按读权限过滤主题**：`job:<id>` 需要自己的 job 或 `job.read.others`（`jobs` 主题同）、`sessions` 需要 `session.read.others`；没权限的 topic 在 `error` 帧里报回来、不订阅。这两个权限默认种入给全体用户类调用方，所以默认部署行为不变。
- **CLI 每个请求都带会话头（gofer-kd8q）**：`X-Gofer-Session-Id` 来自环境——显式设置的 `$GOFER_SESSION_ID` 优先，否则 `$CLAUDE_CODE_SESSION_ID` / `$CODEX_THREAD_ID` / `$CODEX_SESSION_ID` 去重后**恰好一个**才带上；没有、或两个取值不一致就不带（宁可不带，也不认错会话）。注入点只有一个，普通请求、job 提交、SSE 流、文件传输与隧道 websocket 握手全走它。不看当前目录，不读标记文件。注意：显式 `export GOFER_SESSION_ID=…` 会被后代进程继承，`gofer tunnel forward -d` 起的守护进程也算。
- **`GOFER_SESSION_ID` / `CODEX_THREAD_ID` / `CODEX_SESSION_ID` 加入 job 环境禁止清单**：job 不继承启动它的终端那个会话 id，job 里的 `gofer` 不认成那个会话（否则 job 里跑的每条命令都能拿到那个终端的会话授权）。
- **会话归属转移会撤销该会话的授权（gofer-kd8q）**：`gofer session owner <sid> <caller>` / `POST /v1/sessions/{sid}/owner` 在改 owner 的同一个事务里把该会话名下的授权标为已撤销——在此之前它们虽然已经因为 owner 变了而失效，行上却还看不出原因。

## [0.162.0] - 2026-10-16

### 新增

- **job 详情页的「消息」区（gofer-chd9）**：详情页现在直接列出**发给 agent 的消息**——首条是发起 job 的 prompt（自动剥掉 gofer 注入的 rules 段，点「查看完整注入内容」可展开全文），后面是持续会话里各轮 `session say` 的消息，每条用户消息后面跟着 agent 的回复摘录，按时间升序；非会话 job 也至少能看到首条 prompt。数据来自 job 已存下的 request prompt 与 job 的 ACP 结构化记录（`artifacts/acp.jsonl`），不用再跑一趟工作台。接口是详情聚合的新选项 `GET /v1/jobs/{id}?include=messages`（权限同 job 读，即 `job.read.others`），字段见 [`docs/reference/http-api.md`](docs/reference/http-api.md)。
- **授权落库，可以按主体发（gofer-kd8q）**：除权限目录外，server 现在有一张**授权**表：一条授权 = 主体（全体用户 / 某个调用方 `caller:<id>` / 某个终端会话 `session:<sid>`）+ 名称模式（`tunnel.*`、`job.*.others`、`*`），存在数据库里、立即生效。首次启用时那一组「原本不检查」的权限照旧发给全体用户类调用方，所以**升级后行为不变**；新增的是「单独发给某个调用方」的能力（此前只能整 token 给能力位）。接口：`POST /v1/auth/grants` 新增、`DELETE /v1/auth/grants/{id}` 撤销（行留着，撤销后重启不还原）、`POST /v1/auth/grants/{id}/activate` 恢复孤儿、`GET /v1/auth/grants` 列出（含配置推出的只读项与孤儿）、`GET /v1/auth/audit` 按主体 / 权限 / 授权 id 过滤（审计的写入在后续）。只有**根管理员**能管理授权：`auth.manage` 是任何模式都匹配不到的保留权限，worker 与 job 凭据一律 403。
- **`gofer auth whoami` / `gofer auth routes`（gofer-kd8q）**：`whoami` 显示当前调用方、是否根管理员、持有的每项权限与来源（`root` / `config` / `db`，数据库授权还给出是哪一条给的）、请求附带的会话 id，以及没有根管理员时的提示——权限不够时的 403 正文里让人跑的就是它；`routes` 列出运行时每条路由的声明与需要的权限（与 `docs/reference/permissions.md` 同一份数据）。
- 只读查询接口：`GET /v1/auth/whoami`、`GET /v1/auth/catalog`（权限目录 + 预设及其展开）、`GET /v1/auth/routes`，任何用户类调用方都可以调（看不到自己有什么，就只能盲猜该找谁要）。
- **新建 job / 新会话 / 续接都能设目录锁与 worktree（gofer-jkd2）**：Web 新建 job 表单新增「锁定目录」（可多个，项目根相对，留空 = 锁整个 CWD）、「等锁时长」、「worktree」三项，与 CLI 的 `job run --lock` / `--lock-wait` / `--worktree` 一一对应；「快速重建」预填这三项并只在改过时回传。「工作台」与 Sessions 页的新会话表单也新增「工作目录」与「锁定目录」——会话落在项目根会把整个项目根锁住，别的子目录 job 只能排队等锁。`job resume` 新增 `--lock`（HTTP `POST /v1/jobs/{id}/resume` 的 `lock_paths`）：续接默认照抄源 job 的锁，现在可以缩小或改锁（声明的锁会同时要求独占）；会话的 cwd 不变——ACP 的 `session/load` 必须在同一 cwd 加载会话。字段见 [`docs/reference/http-api.md`](docs/reference/http-api.md)。

### 变更

- **孤儿授权**：某个调用方从 `server.callers` 里删掉后，发给它的授权标为孤儿并停用；同一 id 以后重新出现也**仍保持停用**（防止换了人的同名 token 继承旧授权），要根管理员调 `activate` 才恢复。会话主体的授权可以登记（记下会话的 owner 与化身，有效期不超过 24 小时），生效条件随会话授权一并落地。
- **授权缓存按版本号 + 5 秒 TTL 失效**：本进程写入（新增 / 撤销 / 恢复）立即生效，其他进程的写入 5 秒内生效。
- **保存授权时记展开快照**：授权列表给出该模式**现在**展开出的权限与保存时的快照；目录以后新增了该模式能匹配到的权限时，用 `new_permissions` 报出来（「有新权限待确认」，不会自动放开）。

### 修复

- **suga 的供应商错误现在会触发自动续投（gofer-aiuj）**：内置「瞬时错误」模式此前只给 `codex` / `claude` / `omp`，`suga`（含 `suga-acp`、`command` 是 suga 的 wrapper）的失败一律按普通失败处理；同时内置模式补齐**流式返回中断**类错误（供应商的流中途断了，agent 拿到的是解析错误，如 `provider error: parse openai stream chunk: unexpected end of JSON input`）。该类只在错误附近同一行出现 `stream` / `chunk` 这类词时才算，普通的 JSON 解析错误（配置文件 / 会话文件损坏）仍按真失败处理。
- **web：排队中 / 等目录锁的 job 也有取消按钮（gofer-83fe）**：job 详情页的「取消」原先只认 `running`（以及带 session 的 `awaiting_input`），停在 `queued` / `waiting_dir`（等目录锁）/ `recovering` 的 job 在页面上没有取消入口，只能回 CLI `gofer job cancel`。现在按钮的显示口径与服务端 `job cancel` 的允许范围一致：除终态（`done` / `failed` / `cancelled` / `timeout` / `rejected`）与 `needs_review`（要 accept / reject，取消会 409）外，所有非终态——排队中、执行中、`pending_interaction`、`recovering`、`awaiting_approval`（取消 = 撤回）、`paused_for_update`——都显示「取消」；工作台会话头部的「停止」用同一口径。

## [0.161.0] - 2026-10-16

### 变更

- **权限：一份固定名称的目录，每条路由声明自己要什么（gofer-kd8q）**：server 现在持有一份权限目录（40 项，如 `job.submit`、`session.reply.others`、`config.write`），每条 `/v1` 路由声明它需要哪些权限；判定不再看配置里的能力位与 `governance.require_*` 开关，而是看**调用方持有哪些权限**。**现有部署升级后行为不变**：旧配置推出的授权与今天的能力位一一对应，另有「原本不检查」的那组在首次启用时发给全体用户类调用方（读他人 job、提交 job、取消 / 管理他人 job、会话读取与删除、plan / schedule 管理、steward、记忆写入、tracker 同步、托管转发、传输创建、配置读取、检查更新、MCP 测试）。每条权限的含义、风险、可否授予会话、来源，以及每条路由需要哪些权限见 [`docs/reference/permissions.md`](docs/reference/permissions.md)（由服务端代码生成，与二进制一致）。权限不够时响应是 `403`，正文写出缺的是哪一项。
- **根管理员拥有全部权限**（`can_admin: true` 的调用方），与四个 `governance.require_*` 开关无关——包括开关打开时也能答复 / 验收 / 唤醒他人的 job、开隧道、接入交互终端，以及开关关闭时也能代答他人终端会话、回答他人终端的工具权限提示、attach 无主 job。无主 job 的 attach 与 pty 录像仍然只有根管理员。
- **单 token 部署里的 `default` 成为根管理员**：没有 `server.callers` 时唯一能鉴权的 `default` 现在是根管理员（此前它不算管理员，所以单 token 部署里从来没有根管理员）。只有「`allow_empty_token` 且不声明调用方」的部署没有根管理员，那里的权限只读、没人能管理授权。
- **关闭三处旁路**：
  - 他人的传输（读取 / 下载 / 删除）只有根管理员能碰：`xfer.manage.others` 是仅根权限，**刻意不种入**给全体调用方，所以别人的传输不会随默认授权放开。
  - 托管转发除 `tunnel.hosted` 外另需 `tunnel.open`，停别人的托管转发只有根管理员——见下面「托管转发」一条。
  - 推送通知的**动作按钮**与**中继类决策**（终端会话在等的那个）的回答都按权限判定：按钮在签发与兑现时都查 `interaction.answer`（开关打开时只有带 `can_answer` 的调用方能拿到按钮），没有它仍会收到通知、只是不带按钮；中继类决策改按 `session.reply.others` 判定（默认部署里只有会话 owner 或无主会话的任何用户类调用方；开关打开时发给带 `can_answer` 的调用方），此前这条接口不做任何检查。
- **旧的能力位与开关弃用**：`server.callers[].can_answer` / `can_attach` / `can_tunnel` 与 `governance.require_answer_capability` / `require_admin_capability` / `require_attach_capability` / `require_tunnel_capability` 标为弃用（三个版本后移除），启动时每个仍在用的键给一条「请改用 web 授权」的提示；它们的判定结果不变（配置推出的只读授权）。开关打开时不再要求「至少一个调用方带对应能力位」——只要有根管理员就通过校验。`can_admin` 不在弃用之列：它继续用来指定根管理员。
- **调用方 id 与 worker id 不得重名**：`server.callers[].id` 与 `server.workers` 的键相同时，配置校验（加载、`config validate`、控制台写接口）直接拒绝。两者是两个鉴权身份，同名会让 job 归属、审计与权限判定含糊，worker 凭据也会盖掉该调用方自己的 token。
- **托管转发（`tunnel.hosted`）也要 `tunnel.open`**：托管转发的启停、预设的写入 / 删除 / 导入现在除 `tunnel.hosted` 外还需要 `tunnel.open` —— 开关（`server.governance.require_tunnel_capability`）打开时，托管隧道不再能绕过隧道权限。托管转发也不再借 server 自己的 token 回连自己的 HTTP 接口：它在本进程内拨号，隧道按**启动者**判定与登记（`GET /v1/tunnels` 与转发列表里的归属、流量统计都记在启动者名下），所以开关打开时只要启动者有 `tunnel.open` 就能用。升级后**存量 autostart 预设照常启动**：它们的启动者取预设的 `updated_by`；那个调用方已不在配置里时按根管理员处理（只有根能停它）并在启动日志里提一条告警。另外，**停别人的托管转发现在只有根管理员能做**（`tunnel.manage.others`）：此前任何用户类调用方都能停别人的托管转发，现在只有它的**启动者**或根管理员可以停。

## [0.160.0] - 2026-10-15

### 新增

- **job 结束时主动通知提交它的会话（`--notify` / `--notify-session`，gofer-3db4）**：过去只能靠那个会话自己的 Stop hook 等、或下一次输入时补投；现在 `gofer job run --notify` 让 gofer 在 job 结束时（终态，以及停在 `needs_review` 时）经**传话人**把一条系统通知送到指定会话，不必等它自己停下。
  - `--notify` 的目标是提交方（`--source-session-id`，否则按 `$GOFER_SESSION_ID` / `$CLAUDE_CODE_SESSION_ID` / `$CODEX_THREAD_ID` 认出当前会话），`--notify-session <sid>` 显式指定并隐含开启；都认不出会话时报错（不静默跳过）。HTTP / MCP 同名可选字段 `notify` / `notify_session`。
  - 通知一行，以 `[gofer job 完成]` 开头，带 job id、标题、状态、退出码、耗时、`wrap_up` 与 `job show` / `job logs` 提示，并注明是 gofer 系统通知（消息来源记为 `gofer`）；送达成功即消费掉该会话的 job watch，Stop hook 与补发不会再发一次，送达失败则保留 watch 由补发兜底，同一个 job 只通知一次（server 重启不重发）。
  - **默认关闭**：每条通知都要起一次传话人进程，因此只在派长 job（或准备离开）时才加。
- **plan 级开关（gofer-3db4）**：`gofer plan create --notify` / `gofer plan set <plan> --notify on|off`（缺省 `off`）让该 plan **派发的每个 job** 都通知它的主 Agent 会话（supervisor）——一个决定覆盖整条链，不必给每个 todo 加 flag。没绑主 Agent 会话时无人可通知：job 照常派发、不通知任何人，`plan show` / `plan create` 会打一行提示。HTTP / MCP 与 plan 同名字段 `notify`（`on`/`off`，PATCH 省略则保持原值）。
- **续跑继承（gofer-3db4）**：`gofer job resume` 默认继承源 job 的结束通知（通知同一会话），`--no-notify` 关掉；HTTP `/v1/jobs/{id}/resume` 对应 `no_notify`。
- **内部派发不再因目标会话失效而拒绝（gofer-3db4）**：只有**用户直接指定**的 `--notify` / `notify_session`（`job run`、`POST /v1/jobs`、MCP `gofer_run_job`）在目标会话不存在 / 属于别人 / 根本没给时才 400。**gofer 自己发起的 job**——定时任务（cron、run-now、webhook 触发）、plan 派发、workflow 步、wakeup、自动续跑、rerun（包括 hold 批准与升级停靠后的重入、故障转移、耐用重试）——一律**丢弃通知并照常跑**，在 job 上记一条 `job.notify_dropped {target, reason}` 事件（web 时间线标「结束通知已丢弃」），这样一段没人过问的活不会因为一条通知找不到收件人而丢。supervisor 会话已被清掉时 plan 派发也走这条路（源会话声明归零，不再卡住整条链）。
- **MCP 补上 plan 级通知开关**（gofer-3db4）：`gofer_create_plan` 新增 `notify`（`on`/`off`），新增工具 `gofer_set_plan_notify(plan_id, notify)`，与 `gofer_run_job` 的 `notify` / `notify_session` 一致。

## [0.159.0] - 2026-10-15

### 新增

- **写记忆时按子项目自动标注适用路径**（gofer-opv5）：工作区根目录有 tracker、下面多个子项目各自是独立 git 仓（各自有 `.git`）时，在某个子仓里新写的记忆自动带上 `when.paths = <子仓>/**`，只在那个子仓的 job / 会话里注入；命令多打一行提示（「适用路径已设为 deploy-hub/**（整仓通用用 --when-paths '**'，工作区通用请写成项目记忆 --project）」）。
  - 要**整仓通用**就显式写 `--when-paths '**'`；要**工作区通用（跨项目）**应该写成**项目记忆**（`memory set --project <p>`，存在 server 上整项目共享、web 记忆页有仓库 / 项目 / 全局三类）。`--when-paths` 可重复、逗号分隔，传空值或 `-` 清空已有路径；更新已有记忆一律不动它的 `when`。cwd 在 tracker 根所在的仓里、cwd 未知、或写 `--global` / `--project` 时都不自动标。
  - 交付后「可复用经验」候选同样按 job 的 cwd 标注（`memory candidates` 显示「适用路径 …」，接受时提示一行），接受后跟记忆一起写入；写到全局作用域时不带（全局记忆不属于任何仓）。
  - MCP / web 的记忆写入没有 cwd，不自动标注。
- **job 的 tracker-prime 加入项目记忆与全局记忆**（gofer-opv5）：派发的 job 过去只拿到仓库 tracker 的记忆，项目 / 全局记忆只进会话开场。现在 server 直接读 jobstore 的两类作用域记忆（不走 HTTP），按与会话开场相同的规则注入：规则类全文、其余进索引，按 cwd / `when.paths` 与 prime 关键词选材，顺序是项目记忆在前；总预算内截断，每节被截断时标出 `[项目记忆已截断]` / `[全局记忆已截断]`（`prime.scoped_memory: false` 整块关闭）。
- **`gofer memory scope`：给存量记忆补适用路径**（gofer-opv5）：默认只读地列出建议（表格：key、建议的 `when.paths`、依据、置信度），`--apply` 才写入。
  - 依据按优先级：来源 `job:` / `session:` 的 cwd 落在子仓（高）→ key 前缀 / 标签与子仓目录名匹配（高，去掉 `-cli` 之类后缀、`-` 与 `_` 归一后相同）→ 摘要 / 正文只提到一个子仓（中）→ 提到多个或无线索则不建议。
  - `--apply` 默认只写高置信度，`--min-confidence medium` 连中等一起写，`--only key1,key2` 只处理指定 key，`--json` 机器可读；已有 `when.paths` 的记忆一律不动。

## [0.158.1] - 2026-10-14

### 修复

- 两处切片容量提示改用 `util.CapSum`，消除 CodeQL `go/allocation-size-overflow` 告警（关键词选材、升级排空计数）。

## [0.158.0] - 2026-10-14

### 新增

- **嵌套仓 job 把项目根交给 agent（`add_dir_args`）**：一个项目根下常放着多个仓（工作区的 `gofer/`、`web/`、`.worktrees/` 等）。job 的 cwd 是其中一个**嵌套仓**（它所在 git 仓顶层 ≠ 项目根）时，agent 看不到项目根那一层的东西——工作区级规则、tracker、文档；现在 agent 配置新增通用字段 `add_dir_args`（须含 `{{dir}}`）会追加到 argv，`{{dir}}` 填项目根，续接 argv 同样追加。
  - **内置 `suga` 模板默认 `[--add-dir, "{{dir}}:ro"]`**（只读：外层工作区是要读的上下文，不是第二个写入点）。cwd 就在项目根自己的仓里、agent 没配这个字段、或 cwd 所在仓根本定不下来（没装 git / 不是 checkout）时都不追加，行为与以前一致；acp-agent 不支持该字段（配置校验直接拒）。

- **job 上显示 agent 自己的收尾原因（`wrap_up`）**（gofer-gk74）：`suga` 的 stream-json 在步数 / 用量 / 时限到顶时由 agent 自己收尾（result 行带 `wrap_up=max_steps|max_tokens|timeout`，正常回答不带），旧版 suga 撞步数上限时最后一行是 `{"type":"error","error":"max steps exceeded: 100"}`；两种都识别为同一种。
  - 这种 job 的 status **仍是 done**、汇报也在（汇报是真实产出），以前只能自己从 agent 输出里找线索；现在 `job show` / `job review` 打一行 `wrap_up: 步数上限（agent 自己收尾，任务可能未完成）`，web 详情有「收尾原因」，`GET /v1/jobs/{id}` 带 `wrap_up` 字段，持久化到 `jobs.wrap_up`。
  - 与 `stop_reason`（ACP 协议的停止原因）及 gofer 的预算熔断（status=failed、failure_class=budget）都不是一回事：只认 agent 自己说的，不认识就不标。要续跑用 `gofer job resume <id> --prompt "…"`。

- **job 注入的 tracker-prime 按任务关键词选材、排序**（gofer-11tz）：派活时给出任务涉及的子系统 / 语言 / 模块，注入的规则 / 记忆里命中的一条不再被 cwd 与预算淹没。
  - `gofer job run --prime-keywords "job, rules"`（plan todo 用 `plan add-todo|set-todo --prime-keywords`，`--prime-keywords ""` 清空）；不给 flag 时读环境变量 `GOFER_PRIME_KEYWORDS`，**显式 flag 优先**——环境变量留在派活的终端里，单独用它会把之后无关的 job 也缩小选材。
  - 匹配文本 = 关键词 + job 标题 + prompt 开头约 2KB；命中条件 = 记忆的 `when.keywords` 出现在这段文本里，或关键词出现在记忆的 key / 标签 / 摘要 / 它自己的 `when.keywords` 里（不区分大小写、子串）。命中项排在索引最前、优先进全文与预算，并标「（相关）」；命中且**没有 `when.paths`** 的 rule 给全文，带 `when.paths` 指向别处的仍只列一行（路径是作者对适用范围的声明）。未命中的一条都不删，行为与以前一致。
  - 会话开场同样支持：`gofer repo prime --keywords "…"`（同样读 `GOFER_PRIME_KEYWORDS`）。

- 新增会话配置 `session.stop_job_wait`（gofer-rhux）：`away`（默认）/ `always` / `never`，决定 Stop hook 要不要为「关注中的 job」（plan 绑定了本会话，或 job 带 `source_session_id`）阻塞终端。以前只要有未结束的关注 job 就一直阻塞到某个 job 结束（最长 `--wait`，约 2 小时），期间终端输入与跨会话消息都进不来。
  - `away`：只在人不在时为 job 阻塞，判据与 relay 自己的布防一致——`relay=on`，或 `relay=auto` 且键鼠空闲超过 `session.auto_relay_idle_sec`；空闲读数未知（容器里读不到主机键盘）算「人在」，显式 `relay=off` 也算「人在」。
  - `always`：旧行为；`never`：一律不等，立即放行。
  - 不阻塞不等于丢通知：job 结束的通知照旧由下一次 SessionStart / UserPromptSubmit 的 catch-up 补发。
  - hook 以本机配置文件里的值为准；本机没写时用 server 的值（server 在会话响应里新增 `stop_job_wait` 字段下发），因此在容器里跑的 hook 也听 server 这份。

### 修复（安全）

- **worker token 收紧到工作进程自用的路由**（gofer-hxld）：worker token 以前在 `/v1` 组里等同于一个操作员 token，可提交 job、改 agent / 项目配置、登记新 worker、读写会话与 job；现在只放行工作进程自己调用的几条——`GET /v1/workers/{id}/assignable`、`GET /v1/workers/{id}/upgrade/file`、`GET|PUT /v1/xfer/{id}/content`——其余一律 403 `worker credentials cannot call this endpoint`（三条 WS 握手路由在鉴权组之外，不受影响；job 的状态 / 日志 / 结果 / 心跳 / 取消本来就走 worker 连接，不经 HTTP）。同时 `callerMayAdmin` 先要求 user 类凭据：worker / job 凭据无论 `governance.require_admin_capability` 开关如何都不是管理员。
- **schedule / 传输 / 会话授权三项收口**（gofer-hxld）：
  - `GET /v1/schedules`、`GET /v1/schedules/{id}`、`rotate-token` 对非 owner 且无 `can_admin` 的调用方把 `trigger_token` 掩码成前 4 位 + `*`；`POST /v1/schedules/{id}/run-now` 改用**当前调用方**的身份提交（job 的 `caller_id` 就是执行 run 的人），计划存储的请求仍归属创建者（cron / webhook 按创建者提交）。
  - 传输记录属于发起它的 caller：`GET /v1/xfer` 只列自己的（除非 `can_admin`），`GET /v1/xfer/{id}` / `DELETE` / 用户下载他人 `get` 内容一律 403；worker 自用的 `PUT|GET /v1/xfer/{id}/content` 不变。
  - 终端授权卡（`POST /v1/sessions/{sid}/permissions/{id}/answer`）：除会话 owner 外，带 `can_admin` 的 caller 也能作答——换 token 后会话 owner 是 hook 的身份（如 `agent-dev`），web 上的人（另一个 caller）以前看得见却答不了；worker / job 凭据仍一律 403。

## [0.157.0] - 2026-10-14

### 变更

- 新增项目配置 `projects.<k>.tracker_prime`（gofer-krzy），决定 job 注入的 `tracker-prime` 取自哪个 tracker：`nearest`（默认，与以前一致：逐级向上找最近的 tracker，工作区里的项目共用工作区 tracker）| `repo`（只找 job cwd 所在 git 仓内的 tracker，到该仓顶层就停，给需要隔离的嵌套独立仓用）| `off`（不注入；`gofer repo prime` 不受影响）。绑定规则与 `.gofer/RULES.md` 照常注入。
- 同一批：job 注入的 `tracker-prime` 按 **job 的 cwd** 选材（与会话开场 prime 同一套）：`when.paths` 不覆盖该 cwd 的规则不全文注入，索引只列适用于该 cwd 的记忆（没设 `when.paths` 的，或命中的），命中的排前面；cwd 是仓库根时索引列出全部记忆（job 可能改任何子目录），只是不命中的规则不全文注入；cwd 相对 tracker 根目录计算。因此**语言 / 子系统专属的记忆请加 `when.paths`**，否则仍会注入到整个仓库的每个 job。

### 修复

- `gofer job resume` 的续接 job 明确跟着源 job 的 plan：默认继承（提交时的 `--plan`、plan 派发、`gofer plan attach` 都算），`--plan <plan-id>` 换一个，`--no-plan` 不挂（并丢弃继承的 checklist 挂接）；`gofer job show` 现在显示 `plan:` / `todo:`，以前 plan 根本不显示——续接 job 看上去像“没挂 plan”（gofer-z414）。HTTP：`POST /v1/jobs/{id}/resume` 新增可选 `plan_id`（空串 = 不挂）。
- 测试：xfer 的后台派发改为可等待（`Manager.Wait`），server 停机与测试收尾时先等派发结束再关库，修复 Windows 上偶发的「数据库仍被占用」清理失败。
- `gofer plan attach` 把 job 挂到还在跑（或刚终态、进程里还留着的）job 上不再丢：以前只改元数据库、不改内存里的那份 job，该 job 自己下一次写入（含终态落库）会把 plan 覆盖回去，于是 `plan show` / 续接都看不到它（gofer-z414）。

## [0.156.0] - 2026-10-14

### 新增

- 一键更新改为「空闲时更新」（gofer-dzv3）：下载并校验新版本后不再死等运行中的 job（以前最多 5 分钟、超时即放弃），而是先**等机器空闲**——运行中的 job 照常跑完，**等待期间新提交的 job 照常接收但先停住**（新非终态 `paused_for_update`，不占执行位），不再对提交方回 503；空闲后重启，新 server 起来先把停住的 job 全部放行（同一 job id 照常开跑，不用重新提交）。已在 worker 上跑的 job 不阻塞重启（worker 进程留着它，server 回来后重新接管），但还没有交给 worker 的 worker job 仍会等。确认框可选最长等待 30 分钟 / 1 小时 / 2 小时 / 4 小时（默认 2 小时，请求体 `wait_idle_sec`）；等待期间关于页列出还在挡着重启的 job（可点进详情）、已停住的 job 数与放弃时间，并可点「取消等待」（`DELETE /v1/update/apply`，管理员）。apply 失败 / 超时 / 取消都会把停住的 job 放行。

### 变更

- `gofer serve upgrade` 的排空上限改为 `--drain-timeout <dur>`（默认 `5m`）：排空现在按「本机还没做完的活 + 尚未交给 worker 的 worker job」计，已在 worker 上跑的 job 不再算（以前会把它们和断线 `recovering` 的 job 都等进去）。
- 一键更新失败时的原因不再只有 `exit status 2`：从 `run/update/serve-upgrade.log` 尾部取命令行自己报的错（如排空超时），并补上当时还在挡着重启的 job 与日志路径。
- `paused_for_update` 在 web 与 CLI 上可读：`gofer job ls --status paused_for_update`、调度板与新 job 提交输出都有说明。

## [0.155.1] - 2026-10-14

### 修复

- 切 token（`server.callers`：人和 agent 用不同 token）后旧会话会被拒且**无声停更**：hook 以新身份上报被拒时，会话上记下最近一次被拒的调用方与时间（`refused_caller_id` / `refused_at`，`GET /v1/sessions` 与详情返回，server 日志一条限频 warn，hook 日志记一行）；web 会话列表 / 详情显示「归属被拒」，并新增「转给 <caller>」按钮与 `POST /v1/sessions/{sid}/owner`（`gofer session owner <sid> <caller>`，需 `can_admin`）把会话转给 hook 现在用的调用方，转移后心跳立即生效（gofer-eh53）。
- 管理员可以修改他人 plan 的 `supervisor_session_id`，并能绑定任何调用方的会话（须存在、项目一致）：切 token 后 web 上的人与建 plan 的 agent 不是同一个调用方，之前只认 plan owner，管理员也改不了；非 owner 且无 `can_admin` 的调用方仍然被拒，报错也说清了会话属于哪个调用方、本次是谁在请求（gofer-ka6r）。

## [0.155.0] - 2026-10-14

### 变更

- 最低 Go 版本从 1.25 升到 1.26（`go.mod`）：CI 改为测 1.26 与最新稳定版，发布构建用最新稳定版（gofer-c8nf）。

### 新增

- 内置 agent 模板新增 `suga`（cli-agent）与 `suga-acp`（acp-agent）：本机 PATH 上有 `suga` 就自动注入（`agent list` / web 上标「内置」）。两者共用一份会话存储，`job resume --agent` 可在 `suga` ↔ `suga-acp` 之间换。suga 的 job 默认跑 `suga run --allow-write --output-format stream-json`（stdout = 最终答复、stderr = 过程事件、用量计入预算），并内置了只读（`--read-only`）、`--model`、`--from-session`、取消时的 TUI 退出输入与在线送话（`suga send`）（gofer-nitz）。

### 修复

- acp-agent 的 job 不填超时时默认超时从 300 秒改为 1200 秒，与 cli-agent 一致：两者都要跑完整的一轮模型调用，之前在 web 上新建 job 选 acp-agent、超时留空，只有 5 分钟就判定超时（gofer-6uhf）。

## [0.154.6] - 2026-10-13

### 修复

- `serve upgrade`（含一键更新）在排空之前先加载一次配置文件：配置读不通（例如改到一半）时直接拒绝，旧 server 继续运行。之前要等新 server 启动失败才发现，而回滚又可能失败，服务被留在停止状态。
- 升级失败回滚时，supervisor 文件可能仍被刚停止的进程占用（Windows 报 `Access is denied`），现在会重试；即使仍恢复不了，也会把已恢复的旧程序重新启动，不再让服务停着。

## [0.154.5] - 2026-10-13

### 新增

- 出站代理 `http_proxy: {proxy_url, allow_sites}`：server 自己发出的请求（检查更新与下载、skill 导入、通知 webhook、Web Push、交付 API）访问 `allow_sites` 里的站点时走 `proxy_url`（http / https / socks5），其余照旧跟随环境变量；`example.com` 含子域名，`*.example.com` 只含子域名，省略 = 全部。只能改配置文件，reload 立即生效；`server.update.proxy` 仍优先（gofer-29gi）。

### 修复

- Windows 上以服务方式运行的 server 一键更新时，替换步骤报 `detached breakaway: … Access is denied`：server 在计划任务的 job object 里，不允许升级 helper 脱离。现在 `serve upgrade` 放在允许脱离的 job object 里运行（与作为 job 执行时相同）。

## [0.154.4] - 2026-10-13

### 修复

- web：「设置 → 关于」的「刷新」「检查更新」按钮没有样式（显示成浏览器默认按钮）；Issues 页分段按钮（仓库 / 项目 / 全局、树形 / 平铺）、状态筛选、分页、批量操作与记忆的保存 / 删除按钮字号偏大，统一为与其他页面一致的小按钮。

## [0.154.3] - 2026-10-13

### 修复

- 一键更新从 GitHub 下载时改走 `api.github.com` 的资产地址：有的网络能访问 API 但连 `github.com` 的下载链接超时，导致读不到 `latest.yaml` 的 sha256、下载新版本失败。取不到 `latest.yaml` 时日志记 `update.checksums_unavailable`；一键更新在下载阶段失败时，未处理的 worker 显示为「跳过」。

## [0.154.2] - 2026-10-13

### 修复

- 0.154.1 的「注册时补触发」也作用到了监听自己的唤醒（不带 `--job-id`），对已结束的 job 注册就会立即续跑它；现在只用于监听另一个 job。

## [0.154.1] - 2026-10-13

### 修复

- 监听另一个 job 结束（`job wakeup create --kind event --event job.terminal --job-id <X>`）时，如果 X 在注册前已经结束，唤醒永远不会触发（结束事件已经过去），只能等到过期；现在注册时发现 X 已结束（且符合 `--status`）会立即触发一次。

## [0.154.0] - 2026-10-13

### 新增

- 一键更新：有新版本时 web 顶栏显示 `↑ <版本>`，并发 `update.available` 通知（默认触发事件）。以服务方式运行的 server（`gofer serve register`）可在「设置 → 关于」点「立即更新」：server 下载新版本（有 sha256 就校验）并通过 `serve upgrade` 替换自己（等运行中的 job 结束，失败回滚），重启后再升级勾选的 worker（默认勾选全部在线的；其他平台的 worker 下载自己平台的二进制）。普通 `gofer serve` 显示手动步骤。接口 `POST /v1/update/apply`，`GET /v1/update` 新增 `managed` / `apply` / `sha256`（gofer-29gi）。
- `make latest-yaml` 在 `latest.yaml` 里记录各平台二进制的 sha256（`files`）；`make release` 把 `latest.yaml` 一并放进发布物，GitHub release 也会上传它。

## [0.153.1] - 2026-10-13

### 修复

- worker 执行派发的 job 时启动的两个辅助 goroutine（关停时取消本地 job、提前上报渲染命令）不在派发的等待范围内，派发结束后仍可能在访问本地 job 库；现在派发返回前会停止并等它们结束（gofer-g3rn）。
- job 结束并从内存移出后，从库里读回时丢失了 issue / tracker 关联（只存在请求记录里），`job show` 看不到关联，job 凭据按 tracker 的作用域检查也可能误拒；现在读回时一并恢复。
- 测试稳定性（只影响 CI）：假 ACP agent 的孙进程 pid 文件改为单一写入者、原子写入；ACP 超时取消测试的时限覆盖 agent 启动；worker 恢复测试等待偏移量提交（gofer-9n1n）。

## [0.153.0] - 2026-10-13

### 新增

- 检查更新：server 定时检查是否有新版本（默认每 2 小时，`server.update.interval`），web「设置 → 关于」显示当前 / 最新版本与本平台下载链接，可点「检查更新」立即检查。默认查 GitHub 最新 release，配 `server.update.latest_url` / `url_template` 改查内部 mirror（`make build-all` 生成的 `latest.yaml` 与二进制，占位符 `{name}` `{os}` `{arch}` `{ext}` `{version}`），`proxy` 可指定代理。只检查不升级；配置只能改文件。接口 `GET /v1/update`、`POST /v1/update/check`（gofer-6x2n）。

## [0.152.3] - 2026-10-13

### 修复

- 没有声明 `allowed_agents` 的项目（即允许所有 agent）在 `gofer agent probe` 上被当成「全部不允许」，返回 400；故障转移的候选 agent 也因同一判断在这类项目里全部被跳过。现在与 job 提交一致：空列表即不限制。

## [0.152.2] - 2026-10-13

### 修复

- 全局记忆同步状态里，机器名附带操作系统（如 `DESKTOP-1 (linux)`）：容器通常沿用宿主机的主机名，原来会与宿主机合并成一行。

## [0.152.1] - 2026-10-13

### 修复

- 交互等待（`WaitAnswer`）在 job 刚结束、已从内存移出后才开始等待时，返回交互已落库的最终状态（已答 / 已取消），不再报「unknown job」；CI 上偶发的 `TestFinishReconcilesPendingInteraction` 失败即此时间窗口。

## [0.152.0] - 2026-10-13

### 新增

- job 密钥引用（`job run --secret <名字>`，JOB-06②）：顶层 `secrets` 声明从 server 进程的哪个环境变量取值（值不进配置），项目用 `projects.<p>.secrets` 允许；提交时只记名字，执行时注入 job 环境（变量名可用 `as` 指定），job 的 stdout / stderr 写盘时把值替换成 `***`，并记 `job.secrets_granted` 事件。只支持 server 本机 runner、不支持交互 job；两项配置只能改文件；`config validate` 提示本机未设置的变量（gofer-rjmi）。
- 全局记忆各机器的同步状态：同步时上报主机名，server 按机器记最近一次同步，web 的 Issues → 记忆 → 全局 显示「同步状态」面板；接口 `GET /v1/tracker/repos/{tracker_id}/clients`（仓库 tracker 同样适用）。
- plan 详情与看板卡片显示 todo 关联 job 的交付状态：开了 PR 时有「PR ↗」链接，交付失败时标红（gofer-5n9i）。

### 变更

- 其余 `make` 容量提示统一经 `util.CapSum`，防止 CodeQL 的分配大小溢出告警。

## [0.151.0] - 2026-10-13

### 新增

- 本地全局记忆：`memory --global` 改为读写本机配置目录下的副本 `<config-dir>/tracker/`（与仓库 tracker 同格式），不连 server 也能完整使用；连着 server 时写后自动同步，`gofer memory sync --global` 手动同步，新机器第一次使用先从 server 拉取。`archive` / `restore` / `promote` 现在也支持 `--global`。在 gofer job 里全局记忆只读，`flag --global` 经 server 记录后拉回本地（gofer-j6qn）。
- 会话开场的 prime、按关键字 / 命令注入记忆的 hook、`issue / plan brief` 改为从本机副本读全局记忆：server 慢或连不上时全局约定照常注入，hook 也少一次网络往返；prime 在原有预算内先同步一次。从未同步过、本地也为空的副本仍向 server 读（gofer-j6qn）。

### 变更

- `memory ls|show --global --json` 输出改为仓库记忆的格式（`by` 代替 `updated_by`，没有 `scope`）。

## [0.150.0] - 2026-10-13

### 变更

- 全局记忆在 server 上改存为保留的 tracker `global`（与仓库 tracker 同一套带版本号、删除记录与增量游标的存储），升级后首次启动自动迁移已有的全局记忆。`memory --global`、web、MCP 与知识沉淀的用法不变；这是配置目录本地全局记忆（离线可用、与 server 双向同步）的第一步（gofer-j6qn）。

## [0.149.0] - 2026-10-12

### 新增

- 决策 hook（`server.decision_hooks`）：提交 job 前（`job.submit`）、交付 worktree 分支前（`job.deliver`），server 同步运行配置的命令，按它输出的 JSON 放行、否决，或只往保守方向修改（提交：加验收、改成待批、只读、调低超时与预算、加标签；交付：改 PR 标题与正文、改成 draft、加 label）。脚本出错、超时或要求放宽时默认按否决处理（`on_error`）。提交被否决返回 403，交付被否决记为交付失败并通知，每次裁决记成 job 事件 `job.decision_hook`。只能在配置文件里改。`gofer tool hook-test` 用样例输入试跑一个 hook。协议见 `docs/runbook/decision-hooks.md`（gofer-sr0b）。

## [0.148.1] - 2026-10-12

### 新增

- `job deliver --dry-run` 也可预览 `needs_review` 的 job：accept 之前就能看到将要开的 PR 标题与正文（gofer-iy68）。

### 修复

- `--worktree` job 被 `reject --resume` 退回返工后，续接 job 现在在同一个 worktree 与分支上继续，并沿用源 job 的交付设置；返工后 accept 即自动交付整条分支（之前续接 job 没有 worktree 记录，无法交付）。交付的 PR 标题与任务说明取最初的任务，而不是退回理由（gofer-glyo）。

## [0.148.0] - 2026-10-12

### 新增

- `gofer job deliver <id> --dry-run` 预览交付模式、分支、目标分支和 PR 标题与正文；HTTP 交付接口支持 `dry_run`，预览不会执行交付或写入交付记录。

### 修复

- 合并交付（及 `job worktree merge`）因冲突以外的原因失败时（如机器没配 git 提交身份、hook 拒绝），报告 git 的原始错误，而不是一句没有文件列表的「has conflicts」。

## [0.147.0] - 2026-10-12

### 新增

- 从 web 给终端会话发消息并送达后，对话转到 web 继续：`auto` 会话自动切到 `on`（之后每次停下都在 web 等回复），人在终端输入一条即照常降回 `auto`；显式 `off` 不受影响（gofer-n64n）。

### 修复

- 交付开的 PR 正文不再带 gofer 注入的规则段与工作约定段（规则 / 记忆里可能有机器名、路径、内网地址，而 PR 可能公开），只放提交时写的任务说明；回到 gofer web 的 job 链接改为 `delivery.job_link: true` 才附。
- 会话等着的 job 停在 `needs_review`（开了验收）时，Stop hook 不再一直等下去：`needs_review` 与终态一样唤醒监督会话，通知里附「待验收」提示。

## [0.146.2] - 2026-10-12

### 新增

- 交付支持代理：项目 `delivery.proxy`（http / https / socks5）让交付的推送与平台 API 都经代理；不配时平台 API 跟随 git 对该 remote 的 `http.proxy`，再退到代理环境变量——只能经代理访问 GitHub 等平台的机器也能自动开 PR。

### 修复

- web 给 Claude 会话发消息报 `no_tmux`：hook 识别会话的 SendMessage 地址时，按文件名只扫 Claude 会话目录里的前 128 个文件，而目录里会积下大量已退出进程的文件，排在后面的活会话被漏掉，于是退到 tmux 投递。现在按修改时间从新到旧扫描。

## [0.146.1] - 2026-10-12

### 修复

- 工作项整理 job 不再注入 agent / 项目的 rules（例如 agent 挂了通用纪律时，每次整理都会把它拼进 prompt，浪费 token 并干扰固定 JSON 输出）。

## [0.146.0] - 2026-10-12

### 新增

- worktree job 的交付（GIT-02）：项目配置 `delivery`（或 `job run --deliver`）让 `--worktree` job 在完成（开了验收则 Accept 后）且有新提交时自动交付——`merge` 合进主 checkout、`push` 推分支、`pr` 推分支并开 PR / MR（GitHub 含企业版、GitLab 含自托管、Gitea / Forgejo，按 remote 识别，走 REST API，token 从执行机的环境变量或已登录的 gh / glab 取）。推送须项目 `delivery.allow_push: true`，从不 force push；同分支已有打开的 PR 时复用。PR 正文带任务说明、验收标准、提交、改动统计、验证、用量、验收结论与 job 链接。worker 上的 job 在那台 worker 上交付。`gofer job deliver <id>`（及 web job 详情的「交付」）手动交付 / 重试；`gofer job accept --no-deliver` / web「本次不交付」通过但不交付；交付失败进默认通知（gofer-5sms）。
- 费用估算：顶层 `pricing`（每百万 token 美元价，模型名或通配，不内置价格）给没有自报费用的用量估算费用——终端会话用量、plan 合计、`/v1/stats` 会话窗口、带 `--model` 的 job；界面标「估算」并列出未定价的模型（gofer-2ldy）。

### 修复

- plan 只有主会话用量（没挂 job）时，plan 页也显示用量合计。

## [0.145.2] - 2026-10-12

### 变更

- `gofer worker show` 与 web Runners 页的升级历史改为最近 5 次，每条带升级时间。
- `gofer init hooks` 的提示里加上选择题转 web 的说明。

## [0.145.1] - 2026-10-12

### 修复

- 终端选择题上 web：AskUserQuestion 的 PreToolUse 走了不连 server 的快速路径，hook 空指针崩溃，Claude Code 报 `PreToolUse:AskUserQuestion hook error`。改为走完整路径（gofer-02fu）。

## [0.145.0] - 2026-10-12

### 新增

- 终端会话的选择题上 web（gofer-02fu）：Claude Code 调 AskUserQuestion 时，若人不在终端前（与 Stop 中继、授权上 web 同一判据），问题变成 web「等你回答」卡（会话抽屉按题单选 / 多选 / 自由填写，「今天」页单个单选题可直接点选项），作答后由 hook 作为工具输入交回，终端不弹框；超时、人回到终端或 server 不可达时交回终端。需重跑 `gofer init hooks` 装上新的 PreToolUse 条目。
- web 配置页的 agent 表单可编辑 MCP 绑定（`mcp` / `mcp_format` / `mcp_args`）。

### 修复

- web 发给会话的消息由传话人以 Claude Code 会话名 `gofer-web` 转达，接收方不再看到按目录自动起名的别的会话名而误以为不是用户本人（gofer-02fu）。
- codex 等 agent 已正常退出、答复完整，但其子进程仍占着输出管道时，job 不再被标成失败（`exec: WaitDelay expired before I/O complete`），按进程自身退出码判定并在 stderr 注明（gofer-uyou）。

## [0.144.0] - 2026-10-12

### 新增

- MCP 注册表（MCP-06）：server 配置顶层 `mcp_servers` 声明 MCP server（stdio / http），用 `server.mcp`、`agents.<k>.mcp`、`projects.<k>.mcp`、`job run --mcp` 四级绑定（`--no-mcp` 关闭）。派发时按 agent 自动注入：acp-agent 走 `session/new` 的 mcpServers（http 仅对声明支持的 agent），claude 生成 `--mcp-config` 文件，codex 用 `-c mcp_servers.*`；续接会重新注入。凭据只写环境变量名，值从执行 job 的机器读取，不进命令行、任务记录、生成的文件与日志。worker 协议 v23 起随派发带上 MCP 声明（旧 worker 跳过并记事件）。`gofer agent mcp ls|show|test`、web「设置 → MCP」查看与测试；job 详情显示绑定的 MCP（gofer-0sxd）。
- 终端会话用量也在节流的进度心跳上报：长时间不结束的一轮，plan 上的「主会话用量」会随进度更新，不必等这一轮结束。

### 修复

- `gofer worker reload` 不带 id 时，与 `--local` 一样先识别本机唯一运行的 worker，不再直接报 `worker id is required`（gofer-wvyo）。

### 兼容

- worker 协议升到 v23；MCP 绑定要派到 worker 执行，worker 需升级到本版本。

## [0.143.0] - 2026-10-11

### 新增

- agent `extends` 支持另外两种写法：`builtin:<模板名>` 以指定内置模板为底（agent 名可与模板不同）；`<agent 名>` 以同一配置里已声明的另一个 agent 为底，可链式继承，循环和自引用报错。web 徽标显示继承来源。
- 验收面板「发现」页签每条可直接「建 issue」：建到该 job 的仓库 tracker（job 关联的 tracker，否则项目唯一的 tracker），带 `discovered` 标签和来源 job，下次 `gofer repo sync` 进仓库；同一条重复点击不会重复建。没有可用 tracker 时显示原因并给出 CLI 命令。新接口 `POST /v1/jobs/{id}/findings/issue`。
- `gofer config lint worker [-c worker.yaml] [--fix]`：worker.yaml 也能找出可删的键（agent 的默认 detect / 续接参数、显式写出的 exec 等）、可改写成 `extends: builtin` 的 agent 与未知键。

## [0.142.1] - 2026-10-11

### 修复

- 热加载回执（`serve reload` / web「重新读取文件」）的 `changed` / `restart_required` 改为按生效值比较：删掉与默认值相同的键、agent 改写成 `extends: builtin`、把段落挪进 `include` 文件不再误报变更（例如误报 `restart_required=server.web_enabled`）（gofer-n6ax）。
- server 停机时先关闭 worker 连接并等其中的 job 转入 `recovering` 并落库，再关闭数据库；之前这一步在数据库关闭之后执行，日志里出现 `persist recovering snapshot … database is closed`（gofer-5fiw）。

## [0.142.0] - 2026-10-11

### 新增

- 配置拆文件：server 配置可写 `include: [config.d/*.yaml]`，把整段（如 agents、projects）放到单独文件；一个段只能在一个文件里。web 保存时写回该段所在的文件（保留注释、未变的文件不动），热加载 / `config validate` 覆盖所有文件，reload 回执与 `config info` 列出文件，`config edit --key <段>` 打开所在文件，web 配置页标出每段的文件。`gofer config split` 按原文把 agents、projects 搬出去（先备份、前后比对生效配置）（gofer-arsj）。
- agent `extends: builtin`：与内置模板同名的 agent 只写要改的键，其余沿用模板并随升级更新；写了的键整项替换（`false` / `[]` 也算），web 修改时只写回差异；config.yaml 与 worker.yaml 都支持；Agents 页显示「继承内置」（gofer-arsj）。
- `gofer config lint [--fix]`：逐个键试删并重新加载，列出删掉也不改变生效配置的键（与默认值相同的 detect、日志 / 存储默认值、默认续接参数等），`--fix` 原地删除（先备份、改完复核）；给出可改写成 `extends: builtin` 的 agent 的最小写法；未知键单独列出、不自动删除（gofer-arsj）。
- job 用量记录 agent 报告的工具调用次数 `tool_calls`，`job show` / web 显示为 `tools N`（gofer-z2c6）。

### 修复

- omp 的用量与预算计量改为把每次模型请求的用量相加；之前只取了最后一次请求，多步任务的用量被严重低估。

### 兼容

- 使用 `include` 或 `extends` 的配置需要 server、worker、CLI 都是本版本或更新：旧版本会把这两个键当作未知键忽略。

## [0.141.1] - 2026-10-11

### 修复

- `job run --model`：cli-agent 的 prompt 是某个 flag 的值时（`--prompt {{prompt}}`、`-p {{prompt}}`），`model_args` 改为插在该 flag 之前，不再插进 flag 与值之间、把 `--model` 当成 prompt 传给 CLI。claude 内置模板的参数顺序随之变为 `--model <m> --verbose <prompt>`，效果不变。

## [0.141.0] - 2026-10-11

### 新增

- 隧道动态转发 `socks/[bind:]port`：`gofer tun forward -w <worker> socks/1080` 在本机起一个 SOCKS5 代理（同一端口也接受 HTTP CONNECT），浏览器 / curl 用目标的原始地址访问 worker 网络，不必再给每个目标分配本地端口。每条连接仍按真实目标走隧道鉴权与 worker 白名单，被拒时返回 SOCKS5 回复码 / HTTP 状态；只能绑环回地址；可写进预设、由 server 托管启停（TUN-06，gofer-2m7a）。
- 同地址转发 `[udp/]=ip:port`：在本机 `ip:port` 监听、转发到 worker 网络里的同一地址，本地服务按真实地址联调（需先把该 IP 挂到本机环回网卡）（TUN-07，gofer-2m7a）。

### 变更

- `tun forward` 与 server 托管转发启动前检查所有规则的监听地址是否属于本机：不在就整体报错、不启动任何监听，并给出添加环回地址的命令（Linux / macOS / Windows），不再是 `cannot assign requested address`（TUN-07）。
- 含 `socks/` 或 `=` 规则的预设需要新版 CLI 才能读取。

## [0.140.2] - 2026-10-11

### 修复

- worker 上的 ACP 持续会话如果还停在第 0 轮（例如续接后还没发第一条消息），server 重启或升级接管后不再一直显示「运行中」：会恢复成等待输入，工作台可以继续输入（之前要等空闲超时结束）（gofer-rvdd）。

## [0.140.1] - 2026-10-11

### 修复

- 工作台的 ACP 持续会话：还没有任何对话记录的一轮（例如刚续接、还没发第一条消息）不再一直显示「加载结构化记录…」。流连上即结束加载，等输入时显示「会话已就绪，等待输入」，运行中显示「agent 运行中，等待第一条输出」（gofer-m600）。

## [0.140.0] - 2026-10-11

### 新增

- 事件钩子 `server.event_hooks`：事件发生时在 server 主机上运行一条命令（argv，不经 shell），事件 JSON 与 generic webhook 的 POST body 相同、从 stdin 传入，另带 `GOFER_EVENT_TYPE` / `GOFER_DELIVERY_ID` 等环境变量；退出码非 0 按 webhook 同一退避表重试，stderr 尾部脱敏后进投递记录。钩子不受通知总开关与项目 `notify_enabled` 影响，最多 4 个并行、不拖住 webhook。因为等于在主机上执行命令，只能在配置文件里改：web 配置页只读展示（env 只显示键名），配置写接口拒绝该字段；`gofer config validate` 检查名字 / 命令 / 超时，命令在本机找不到时给 WARN。用法见 `docs/runbook/im-notification.md` 第 8 节（gofer-u4e1）。
- runner 可以声明本机装了哪些外部工具：worker 在 worker.yaml、server 本机在 config.yaml 顶层写 `tools:`（名字 + 可选说明 / 可执行文件）。派到这台机器的 agent job，prompt 会在规则和技能清单之后带一段「本机可用工具」（exec job、续接、`--no-rules` 不加）；`gofer worker show`（`-v` 带说明）、`GET /v1/runners` 和 web Runners 页能看到每台机器声明的工具；worker 跑的 job 环境里多了 `GOFER_WORKER_ID`。可执行文件找不到时 `gofer config validate` 与 `gofer worker doctor` 只告警（gofer-vni6）。

## [0.139.1] - 2026-10-11

### 修复

- Windows 受管 server（`gofer serve register/start/upgrade`）启动和升级时不再闪一下 cmd 窗口：计划任务拉起的 supervisor 拷贝改为 GUI 子系统，系统不再先给它建控制台窗口。升级时 supervisor 由发起 `serve upgrade` 的 gofer 程序重新拷贝，用新版本发起升级即可生效（gofer-gwn8）。
- 安全加固（code scanning）：pty 录像下载的 Content-Type / nosniff 直接设在写出正文的响应上，CI 的 CodeQL 不再报 XSS（gofer-1sqv）。

## [0.139.0] - 2026-10-11

### 新增

- `gofer repo mirror ls` 列出 server 上的 tracker 镜像，`gofer repo mirror rm <tracker_id> --yes` 删除一个镜像（测试或废弃仓库的镜像原先无法清理）；对应 `DELETE /v1/tracker/repos/{tracker_id}`，只允许人凭据且需管理员能力。仓库本地的 tracker 不受影响（gofer-gtom）。

### 修复

- 安全加固（code scanning）：pty 录像下载加 `X-Content-Type-Options: nosniff`，终端输出不会被浏览器当成网页解析；skill 导入的 git clone 用 `--` 隔开选项与地址；skill 解包路径改用可被静态检查识别的限定写法（行为不变）（gofer-1sqv）。
- 工作台支持 `?job=<id>`：Sessions 页新建的 ACP 会话还没拿到 session_id 时，跳到工作台后会等该 job 出现并聚焦，不再停在原来的会话（gofer-k5d4）。

## [0.138.1] - 2026-10-11

### 修复

- 受管升级（`gofer serve upgrade`）的排空不再被 worker 上空闲的 ACP 持续会话挡住：等待输入、没有排队消息的 worker 会话与本机会话一样不计入在飞 job，server 重启后由 worker 重连接管，会话可继续 `say`（gofer-47ey）。
- `gofer serve upgrade` 开始时立即打印 `upgrade_id=… phase=draining`，排空期间每 30 秒打印进度，不再在排空的几分钟里没有任何输出；server 日志新增 `upgrade.drain_waiting` 事件，列出排空在等的 job（gofer-47ey）。
- 会话开场注入（`gofer repo prime`）在较慢的机器上不再丢掉服务端段落（全局 / 项目记忆、进行中 plan）：服务端请求的 1.5 秒预算改为在本地部分（含「当前重点」）算完后才开始计时。

## [0.138.0] - 2026-10-11

### 变更

- **gofer-usage skill 重写为面向任意项目的使用指引**（gofer-8g1t）：SKILL.md 从约 197KB 压到约 15KB，以「开发流程速查」为骨架，只放最常用的入口；进阶与运维细节按主题拆到 `references/`（job 进阶、plan 与 workflow、会话交给 web、tracker、工作项、web 控制台、传文件与唤醒、排障、运维、待批 job），去掉内部实现、路线图编号与版本考古；HTTP 接口移到 `docs/reference/http-api.md`。已装 skill 的机器重跑 `gofer init skill` 更新。
- `gofer issue brief` 的验证命令改为按项目配置：`.gofer/tracker/config.yaml` 的 `brief.verify: [{paths, cmd}]`（`cmd` 支持 `{dirs}` / `{files}`）；不配置时只在有 `go.mod` 的仓库给 Go 的构建与测试命令，不再对所有项目注入 `-race`、跨平台 vet 与前端三命令。「提到的文件」识别更多语言的源码，关键符号支持 JS。
- `gofer repo init --commit-policy local-commit|ask|none`：AGENTS.md / CLAUDE.md 托管块的提交说明按 `commit_policy` 生成，与会话开场注入一致（默认仍是 local-commit）。
- 会话开场注入：`gofer work report` 提示只在装了会话中继 hook 或本项目有未结工作项时出现；「当前重点」的服务行只统计服务本项目的 worker，不再列出 server 上其他 worker 的名字；新开关 `prime.focus_env`（默认开）与 `prime.focus_tag`（「最近 tag 之后 N 个提交」，默认关）。
- `gofer init server` 生成的示例配置：注释统一中文，去掉内部编号，示例项目名改为中性名；workflow 内置模板 plan-implement / review-committee 的提示词统一中文，plan 示例不再偏向 Go。
- 示例任务书模板（`docs/examples/templates`）改为不绑定语言与 agent 的通用版。

- 内置 claude agent 默认带 `from_session_args: [--resume, "{{from_session}}", --fork-session]`：`job run --from-session` 与 Web「从此会话新开」对未自定义的 claude 直接可用——分叉源会话，新会话 id 照常由 `--session-id` 注入，源会话不动；显式配置的 `from_session_args` 优先。

## [0.137.0] - 2026-10-11

### 新增

- 待批 job（gofer-9b1b）：`gofer job run --hold [--hold-reason "…"] [--hold-timeout <秒>]` 提交后停在新状态 `awaiting_approval`（非终态，不派发、不占并发名额与目录锁，server 重启后仍待批），人批准才转 `queued` 照常执行（同一 job id）；拒绝、超时（`server.hold.default_timeout_sec` 默认 24h，上限 `max_timeout_sec` 默认 7d）或提交者 `job cancel` 撤回都落 `cancelled`，命令从未执行（error `hold rejected by <who>[: <理由>]` / `hold expired`）。用于 agent 被自身权限拦下的对外 / 不可逆操作（如 `git push`）：
  - 人批准：`gofer job approve <id> [--note]`（在 agent 会话环境里直接拒绝，不留绕过开关）、`gofer job reject <id>`（待批时理由可选，`--reason` 同 `--note`）；HTTP `POST /v1/jobs/{id}/approve`、`/reject`（worker token 与 job 凭据 403，开了 `governance.require_answer_capability` 要 `can_answer`）。MCP `gofer_run_job` 支持 `hold` / `hold_reason` / `hold_timeout_sec`，不提供 approve 工具。
  - Web：job 详情页首「⏸ 等你批准」面板——理由、完整命令（exec 的 argv / agent 的 prompt 开头，手机宽度折行不横滚）、项目 / cwd / runner / agent、提交者、过期倒计时与附带属性，「批准」一键、「拒绝」可附理由，手机上按钮贴底；「今天」与「待我决策」出「待批准」卡（批准 / 拒绝 / 看详情）；看板加「⏸ 待批准」过滤，`/review` 页头有「待批准 N →」入口；徽标、统计与事件时间线认得新状态和 `job.awaiting_approval` / `job.hold_approved` / `job.hold_rejected` / `job.hold_expired`。
  - 通知与回流：`job.awaiting_approval` 进默认通知集（IM「待批准：<标题>」带理由、命令前几行、过期时间与「去批准」链接，Web Push 高优先级）；提交会话的完成通知对 `cancelled` 附 `reason=hold rejected…` / `reason=hold expired`。
  - 提交输出 `job <id> submitted: status=awaiting_approval expires_at=…` 与 `awaiting approval: <web>/jobs/<id>`；`--sync` 自动转异步，`--wait` 等待窗口为 hold 超时 + job 超时。批准时校验请求摘要（等待期间请求变了返回 409）；hold 跟着请求走，`job rerun`、重建、自动重试、`job resume` 会重新待批。`--session` / `--interactive` / workflow 步骤不能 hold。

### 修复

- 测试：3 个「子进程退出前没人读 pty」的回归测试在 macOS 上跳过——darwin 上会话首进程退出要等终端输出被读走，该场景不存在，测试会死锁（gofer-auat）。

## [0.136.0] - 2026-10-10

### 修复

- 交互式（pty）job 在 Linux / macOS 上，子进程打印后立即退出时，最后一段还没被读走的输出不再丢失（macOS 上短命 job 的输出可能整段丢）：子进程自然退出后先把 pty 缓冲读到 EOF 再关 master（最多等 2s），且 server 自己持有 slave 端直到子进程回收，避免 macOS 在 slave 最后一次关闭时清空未读输出（gofer-auat）。
- Web 工作台：worker 上的 ACP 会话（含 `awaiting_input` 多轮）不再一直显示「加载结构化记录…」。worker 现在把 `artifacts/acp.jsonl` 经日志通道镜像到 server（协议 v22，新 `log` 流 `acp`），`/v1/jobs/{id}/acp/stream` 对 worker job 实时出事件；worker < v22 或 peer-http job 的流里改发一条 `notice` 说明原因，工作台直接显示。**需要升级 worker 才有结构化记录；先升 server 再升 worker**（worker 只向 v22+ server 发该流，旧 server 收到会写进 stdout）（gofer-e2x7）。

## [0.135.1] - 2026-10-10

### 修复

- tracker 锁：多个进程 / 协程持续争用时，等锁一方不再因为一直抢不到、而锁其实在不断易手就报 `tracker lock busy`；现在只有同一持有者占锁超过等待期限才算忙（gofer-rgnw）。
- 常驻传话人投递历史：会话回复的原文已被记下时，传话人自己那一回合的转述不再重复记成一条没有发送方的「收到回复」（gofer-rgnw）。

## [0.135.0] - 2026-10-10

### 新增

- tracker 合并驱动 `gofer repo merge-driver`：并行 worktree / 分支各自改 `.gofer/tracker/*.jsonl`（issues / memories / memories-archive）后 `git merge` 按记录 id 合并，不再出现 jsonl 行冲突——一边的增删改直接采用，删除对修改保留修改，同一 issue 两边都改时逐字段合并（同字段取 `updated_at` 较新者，评论 / notes / 标签 / 依赖取并集），读不了的输入退回 git 文本合并。`gofer repo init`（及 `repo migrate --from-bd --apply`）自动写 `.gitattributes` 与本克隆的 git config；已有克隆运行 `gofer repo merge-driver --install`（TRK-09，gofer-7rqx）。

### 变更

- web 经传话人发给终端会话的消息，会话用 SendMessage 回给传话人的回复，现在由会话自己的 gofer hook（`PostToolUse`）原文上报，直接显示在 web 该会话的对话里（「<会话名> · 回复」），不再由传话人转述；新接口 `POST /v1/sessions/{sid}/replies`，outbox 记录新增 `direction` / `source` / `peer` / `reply_to`。hook 没报上来时，server 本机的常驻传话人用它收到的原文兜底（gofer-6er0）。
- 传话 prompt 要求传话人收到会话回复时只回「已收到」、不再转述；Runners 页传话人抽屉的投递历史新增「收到回复」行（gofer-6er0）。

### 修复

- Web 工作台：回复完一个会话再切到另一个会话后，推送刷新不再把焦点拽回之前的会话（移动端也不再被拉回对话栏）。通知 / 今天 / 会话页带 `?thread=` 的链接只定位一次，随后从地址里去掉；新建会话的自动定位在用户自己选了别的会话后作废；在会话里回复不再触发重新定位（gofer-2seo）。
- 常驻传话进程收到会话发来的消息时会自己开一个回合，它的结果曾被下一条传话当成自己的结果读走（下一条传话 job 输出变成上一条回复的转述，此后每条错位一拍，连续两条还会卡住读取）；现在只有正在等待的请求能拿到结果（gofer-6er0）。

## [0.134.0] - 2026-10-10

> 测试稳定性根治（gofer-r7am）：Windows 全量 ×3 + Linux race 全量 ×2 零失败，期间修复 10 个生产时序 bug。

### 新增

- 以 MIT 许可证发布（新增 `LICENSE`）。
- gofer-usage skill 开头新增「★ 开发流程速查」：新会话从接手（brief）到立项、plan / todo、派活、并行、验收、记忆、收尾的常用路径（`gofer init skill` 安装的副本同步更新）。

### 变更

- 优雅停机时本机执行的 job 会被取消并等其收尾（Unix 也会杀掉进程树），后台写入完成后才关库；worker 上的 job 不受影响（gofer-r7am.1）。
- 删除刚结束、尚在收尾的 job：最多等待 2 秒，仍未完成返回 409（此前可能误报「not terminal」或留下孤儿记录）（gofer-r7am.1）。
- `gofer memory doctor` 末行计数由 `flagged N` 改为 `with findings N`（统计有检查发现的条数，避免与 `memory flag` 的「待复核」标记混淆；JSON 字段仍叫 `flagged`）。

### 修复

- 隧道一端正常结束时，另一端也按正常关闭握手收尾，不再被硬关（gofer-r7am.2，C2）。
- 常驻传话进程处理请求期间暂停空闲计时，长请求不再被中途杀掉（gofer-r7am.2，C3）。
- worker 上「刚派发就取消」的取消请求不再可能丢失（gofer-r7am.2，C4）。
- 被领养的 job 等待结束时不再永久阻塞（gofer-r7am.1）。
- 配置 reload 回执在 Windows 下目标文件被占用时有限次重试（gofer-r7am.3）。
- work / steward 服务停止时等待后台任务结束；pty 会话 id 捕获改为增量扫描，长输出下不再越来越慢（gofer-r7am.4）。
- Web 终端查看端在输出推送时断开，不再可能触发向已关闭 channel 发送导致的 panic（ptyrelay）。
- 本机 pty 中继纳入 job 后台任务，停机时 pty 会话记录能写完（不再停在 open）（gofer-r7am.5）。
- 接手包不再漏掉兄弟 issue 关闭说明里全是数字的短 hash（gofer-r7am.5）。
- tracker 锁在 Windows 上遇到「删除尚未完成」时等待重试，并发的 tracker 命令不再偶发失败。

## [0.133.0] - 2026-10-10

### 新增

- 普通（非 worktree）job 的 `changes.diff` 与范围检查也包含该 job 自己产生的提交（committed 段）（gofer-3nxa.6）。
- `gofer memory doctor --global / --project` 覆盖作用域记忆，并检查 30 天未处理的经验候选（gofer-3nxa.8）。
- 接手包（`issue brief` / `plan brief`）增强：先列 issue 正文点名的文件、从相关提交列出关键 Go/TS 符号（`file:line`）、按代码入口给出针对性的验证命令、issue 中的方案评论置顶全文、缺验收标准时提示。

### 修复

- MCP client 模式下作用域记忆保留 kind / summary / when / flags 等元数据。

## [0.132.0] - 2026-10-09

### 新增

- 接手包：`gofer issue brief <id>` / `gofer plan brief <id>`（MCP `gofer_issue_brief` / `gofer_plan_brief`），一次拿齐上下文树、设计稿、相关提交、job / plan 与适用记忆（TRK-07）。
- 记忆过时反馈：`gofer memory flag|unflag`（仓库与 `--global/--project`，job 凭证也可 flag），MCP `gofer_memory_flag`，注入时标「⚠ 待复核」（TRK-06）。
- 交付后知识提炼：项目 `knowledge_capture` 打开后，job 报告的「## 可复用经验」记为经验候选，`gofer memory candidates|accept|reject` 由人决定是否写入作用域记忆（TRK-08）。
- 方案规则注入 `plan-implement` 的 planner，新增 `gofer plan import` 把 ```` ```gofer-todos ```` 块导入为 todo 链（PLAN-08）。

### 修复

- 删除刚结束的 job 时同时清掉内存中的条目。

## [0.131.0] - 2026-10-09

### 新增

- 验收标准贯穿：todo / job 的 `acceptance`（`job run --acceptance`）注入 prompt「## 验收标准」并在验收面板与 `job review` 中展示（GATE-03）。
- 范围纪律：项目 `scope_discipline` 追加「## 交付约定」，`job run --scope` 声明改动范围，越界文件标「范围外」（`--no-scope-discipline` 关闭）（GATE-03）。
- 报告中的「发现但不碰」解析为 findings：验收面板「发现」页签与 `gofer job findings [--create-issues]`。

## [0.130.2] - 2026-10-09

### 修复

- 空的 `expires_at` 不再阻止写入作用域 rule / note 记忆。

## [0.130.1] - 2026-10-09

### 变更

- Dashboard 增加「今天」区间（按小时序列），默认区间改为 7 天；活动卡片显示 job 状态。

## [0.130.0] - 2026-10-09

### 新增

- Dashboard 重做为统计墙：`GET /v1/stats/overview`、`job_metrics` 侧表，历史 job 用 `gofer tool stats-backfill` 补算（gofer-yelm）。
- Web「从此会话新开」入口（对应 `job run --from-session`）。

### 修复

- tracker 事务出错时总是回滚。
- macOS 可编译（servicemgr 升级钩子为不支持桩）；全新默认工作空间可通过校验；会话 cwd 经符号链接也能匹配。
- 监督会话的用量只记到一个 plan 上。

### 变更

- 依赖升级：gookit cliui v0.5.3、goutil v0.8.1、rux v2.1.2。

## [0.129.0] - 2026-10-09

### 新增

- 新会话开场（`repo prime`）：自动生成「当前重点」段，规则全文 + 分组记忆索引，按段预算裁剪。
- 记忆增加 `kind` / `summary` / `when` / `ttl` / `source` 字段；`memory doctor`、`archive|restore|promote`；`issue ls --stale`、`issue create --from`。
- hook 按关键词在人工提问时注入相关记忆，在 shell 工具调用前注入命令相关记忆。
- Claude Code 终端的权限确认弹窗镜像到 web（「今天」页与会话抽屉），可在 web 上作答。
- `plan create` 自动把当前 agent 会话绑定为计划主会话；主会话用量计入 plan。
- 「今天」页记忆整理卡片：管家在 server doctor 有新发现时提议清理，人工采纳。

### 修复

- 注入的记忆块标注为参考数据而非用户指令。
- 权限弹窗内容在服务端也做脱敏。

## [0.128.2] - 2026-10-09

### 修复

- tracker 同步携带已知的 server rev，过期写入作为冲突返回；一次性修复已分叉的记录。

## [0.128.1] - 2026-10-09

### 新增

- 远程停止外部隧道转发进程：`gofer tunnel stop`、Tunnels 页停止按钮（转发端收到 410 后退出）。

## [0.128.0] - 2026-10-09

### 新增

- 「今天」页卡片延后（按时间、job 结束或有动静时唤醒）与专注模式（N3）。
- 决策卡上显示管家建议，「按建议」一键处理（N3 T4）。
- `job run --from-session <job>`：从旧会话继承上下文开一个新的 agent 会话。

## [0.127.1] - 2026-10-09

### 变更

- 只有「等人」的工作项才会因阻塞描述被判为阻塞；relay `on` 回落为 `auto` 时给出说明；空闲会话发送按钮改名「送到会话」。

## [0.127.0] - 2026-10-09

### 新增

- Web 首页「今天」：待我决策队列（交互 / decision / 待验收 / 等我）、摘要与状态条，`GET /v1/today`；并行泳道 `GET /v1/today/lanes`；全局「待我决策」浮层（WEB-17）。
- 工作项日志分级（里程碑 / 细节，`?level=`），卡片只显示里程碑，泳道健康度与 `work.stall_after`（WORK-06）。

### 变更

- tracker 托管说明引导 agent 用 `gofer repo status --changed` 查看变化，而不是 diff jsonl。

## [0.126.1] - 2026-10-08

### 修复

- web 同步在 tracker `project_key` 不是已登记项目时回落到按路径匹配（此前返回 409）。

## [0.126.0] - 2026-10-08

### 变更

- tracker 改用短 id，旧 UUID id 确定性迁移。
- 内置 `claude` agent 以 ndjson 方式读取 stream-json 输出。

### 修复

- job 中的裸 `gofer` 程序只在运行中的二进制确为 gofer 时才解析为它自身（`GOFER_BIN`）。
- 采纳任一整理建议即把草稿移出「未分类」。

## [0.125.0] - 2026-10-08

### 新增

- 会话催办：`gofer session nudge`（按间隔或「N 分钟无进展」给终端会话送话），会话抽屉中可配置（SESS-12）。

### 修复

- 重复的 claude 消息用量按增长量计数。

## [0.124.0] - 2026-10-08

### 新增

- 终端会话用量：从 transcript 增量采集 token，按主会话 / 子 agent 拆分并在 web 显示（OBS-14）。
- 预算熔断：`job run --max-tokens / --max-cost / --max-turns`（及 agent / 项目 `budget:` 默认），超限杀进程树、`failure_class=budget`、发 `job.budget_exceeded`；远程 worker 需协议 v20（GATE-02）。
- Web Issues 页「同步」：server 在仓库所在 runner 派 `gofer repo sync`（TRK-05）。

### 修复

- 同步提交的 HTTP 超时大于 server 等待上限；git 操作移出提交响应路径。
- peer 终态帧之后继续镜像日志。

## [0.123.0] - 2026-10-08

### 新增

- `job run --model` 与 agent `model_args`（内置 claude / codex；ACP 走协议选择），web 表单可选模型；worker 需协议 v19（AGT-06）。
- Stop 等待感知会话内子 agent，server 按 relay 模式下发等待预算（SESS-10）。
- `deliver_offline_match` 与内置 codex queue 送话（SESS-11）。
- Plan 详情显示并可直接联系绑定的主会话（PLAN-06）。
- `gofer repo status --changed` 列出 tracker 相对 HEAD 的增删改。
- 摘要 / 提醒 / 等我通知没有任何 webhook 订阅时告警（OBS-13）。

### 修复

- `work ls --all` 与 `work rm --status` 计数口径一致；reload 回执按 `reloaded_at` 判断。

### 移除

- 旧的 Windows `start.ps1` / supervisor / selfupdate 脚本（由 v0.120 的原生 serve 管理取代）。

## [0.122.0] - 2026-10-08

### 新增

- 删除已结束的工作项：`gofer work rm`、`DELETE /v1/work-items/{id}`、web 抽屉删除按钮（WORK-05）。

## [0.121.1] - 2026-10-08

### 变更

- serve 升级接受 UPX 压缩的候选二进制（通过自报构建信息校验）。

## [0.121.0] - 2026-10-08

### 变更

- plan job 只绑定经认证的显式来源会话；会话登记与心跳归属原子化（SESS-09）。
- hook 中继把 `injected` 标记与 job 完成前缀视为非人工输入。

### 修复

- ACP job 保留工具 diff 与元数据中的用量。

## [0.120.1] - 2026-10-08

### 修复

- Windows 受管 supervisor 的控制台窗口在 detach 前隐藏。

## [0.120.0] - 2026-10-08

### 新增

- 原生受管 server：`gofer serve register|start|stop|restart|status|logs|uninstall|upgrade`（Windows 登录任务 + supervisor、Linux systemd），升级交独立执行者并持久化结果（SVC-05）。
- tracker 离线仓库迁移（带来源守卫）。
- 配置目录模板 `{config_dir}` 在使用时解析。

## [0.119.0] - 2026-10-07

### 新增

- 通用 agent 接入：`gofer hook generic --agent <key>`、`transcript_dialect`、`ndjson_usage_path`、`inject_process`、`session_family`、`deliver_command`（协议 v18）；web 送话可达非 Claude 会话（AGT-05）。

## [0.118.0] - 2026-10-06

### 变更

- tracker 提交策略写明：push 需要用户授权。
- cli-agent 续接保留源 agent 的输出投影与环境。

## [0.117.0] - 2026-10-06

### 新增

- Issues 页分页、多选与批量关闭 / 改状态 / 打标签（`POST /v1/tracker/issues/batch`）；`issue close|update` 接受多个 id（TRK-04）。

### 修复

- 客户端节点上 `repo init / migrate` 经 server 匹配 `project_key`；bd 迁移的活动守卫忽略 gofer 自身子进程。

## [0.116.1] - 2026-10-06

### 修复

- 手动 `repo sync` 不再沿用 2 秒的自动同步超时。

## [0.116.0] - 2026-10-06

### 新增

- Issues 页父子树、进度与依赖关系（TRK-04）；会话卡片 / 抽屉 / `session ls` 显示 Claude Code 会话名。

### 修复

- relay 关闭时 Stop 仍等待被盯的 job 并补发未送达通知；bd 迁移 gate-lock 不再误判为近期写入；`repo init / migrate` 报告匹配到的项目。

## [0.115.0] - 2026-10-06

### 新增

- issue / memory 命令对齐 bd 日常用法（字段更新、comment、reopen、`dep rm|ls`、列表筛选、关系展示），`repo migrate --from-bd` 加固（TRK-01 X1）。
- plan decision 驱动「等我」、完成回写建议、`work.needs_me` 通知；管家带话 `gofer_session_ask` 与只读 issue MCP 工具（WORK-04）。

## [0.114.0] - 2026-10-06

### 新增

- 工作项转 plan todo（`gofer work to-todo`）；ACP / pty 会话卡片与工作项互链；导航调整与 Works「等我」徽标（WORK-04）。

## [0.113.0] - 2026-10-06

### 新增

- 管家（steward，默认关闭）：常驻 ACP 会话，只整理工作项；专用凭据白名单、自动注入 gofer MCP、版本化笔记、每日巡检、合并建议；`gofer steward …`、`/v1/steward*`、Works 页「问管家」（WORK-03）。

## [0.112.1] - 2026-10-06

### 修复

- worker 重连不再改写已完成的升级记录（升级耗时显示异常）。

## [0.112.0] - 2026-10-06

### 修复

- Windows 上后台 server / worker 启动的非交互子进程不再弹出控制台窗口（SVC-04）。

## [0.111.0] - 2026-10-06

### 新增

- 未声明 `default` 项目时注入内置 `default` 项目（默认工作空间，仅本机 runner，标「内置」、不落盘、不可删除）（CFG-15）。
- 整理器项目解析顺序：`work.summarizer_project` → 工作项项目 → `default`。

## [0.110.0] - 2026-10-06

### 新增

- 工作项二期：字段级发言者标注、请求账本（请会话汇报 / 写交接）、被动整理（读 claude / codex / omp transcript 尾部，worker 协议 v17）、整理建议采纳 / 忽略与成本上限、设置页 `/settings/work`（WORK-02）。

## [0.109.1] - 2026-10-05

### 修复

- 自动中继等待条件失效时关闭等待轮次（web 显示已收到但未送达会话）。

## [0.109.0] - 2026-10-05

### 新增

- 工作项一期：会话首次提问自动建草稿、状态自动映射且人工优先、搁置 / 提醒 `work.remind`、每日摘要 `work.digest`、合并 / 拆分；`gofer work`、`/v1/work-items`、MCP、「工作」页（WORK-01）。
- Sessions 页卡片化；serve / worker 启动时自动创建默认工作空间目录。

## [0.108.0] - 2026-10-05

### 变更

- Runners 页 worker 心跳实时刷新；runner 工作目录只列允许该 runner 的项目。

## [0.107.0] - 2026-10-04

### 移除

- 内置 `tty-claude` / `tty-codex` agent：交互改用 `claude` / `codex --interactive`，旧 tty job 续接自动映射。

## [0.106.0] - 2026-10-04

### 变更

- job 日志 SSE 改为事件驱动（空闲连接不再轮询数据库，wire 不变）。
- `codex-acp` 改用 `@agentclientprotocol/codex-acp` 并加入 codex 会话族。

### 修复

- omp 扩展取消等待时先上报中断；全局安装重复提示的路径去重。

## [0.105.0] - 2026-10-04

### 新增

- omp / jcode 接入 gofer hook（omp 完整支持中继；jcode 只上报登记 / 状态 / 结束）；`gofer init hooks --agent claude|codex|omp|jcode|all`，全局安装后提示项目级重复（SESS-08）。

## [0.104.0] - 2026-10-04

### 新增

- job 详情 `?include=` 聚合；浏览器单连接 `/v1/ws` 主题推送，轮询降为断线兜底（WEB-16）。
- 会话超过 `session.offline_after_sec` 无心跳标为离线（仍可唤醒）。

### 变更

- 内置 `claude-acp` 改用 `@agentclientprotocol/claude-agent-acp`。

### 修复

- 中继等待时按 Esc 正确结束等待轮次。

## [0.103.1] - 2026-10-04

### 修复

- 以空行开头的日志不再让 job 详情页卡死。

## [0.103.0] - 2026-10-04

### 变更

- `server` / `local` 成为内置本机 runner 的保留名：自定义 runner 与 worker id 不得使用，仅允许 `{type: local}` 声明（CFG-14，G043）。
- workflow 模板默认 agent 不可用时 web 自动替换、CLI 报出可用 agent。

### 修复

- workflow 完成后步数显示夹取；`.all` 聚合输出超 32KB 时写文件并传路径。

## [0.102.0] - 2026-10-04

### 变更

- `server` / `local` 两种拼写在所有入口统一识别并落规范名（CFG-14）。
- 唤醒会话优先回到经 transcript 验证的原始启动目录；心跳记录 `last_cwd`。

### 修复

- 项目写接口接受内置 `exec` agent。

## [0.101.0] - 2026-10-04

### 新增

- Web 多 agent 对比视图（每路一列、并排 diff、择优合并 merge / squash、清理其余分支，冲突 409 就地展示并复原仓库）（WF-05）。
- 从模板新建 workflow（变量表单与步骤预览）；job 详情「合并到基线」按钮。

### 变更

- Web 中本机 runner 统一显示为 `server`；合并只被已跟踪改动阻塞；squash 合并后识别为已合并。

## [0.100.1] - 2026-10-03

### 修复

- 会话接管 / 唤醒判断目录时使用 worker 上报的项目实际路径。

## [0.100.0] - 2026-10-03

### 新增

- 传话人可见：Runners 页状态、最近投递、stderr、可见会话对照（worker 协议 v16）（SESS-07）。
- 会话唤醒（含已结束会话）：`gofer session resume`、Sessions 页与会话抽屉的唤醒按钮。
- runner 工作目录展示（默认工作空间 `GOFER_WORKSPACE` / `~/.gofer/workspace`、roots 映射、项目解析路径）。

### 变更

- server 本机会话改走常驻传话人；传话人启动目录兜底为 会话目录 → 默认工作空间 → 家目录。

## 早期版本

v0.1.0 – v0.99.x 按主题区间压缩记录。每段列出该区间内落地的主要能力，细节见路线图与 [`docs/roadmap-history.md`](docs/roadmap-history.md)。

### [0.90.0 – 0.99.1] - 2026-10-01 → 2026-10-03

- 可选 HTTPS 额外监听 `server.tls` 与 `gofer tool cert`（PWA 用）（CFG-11）；手机端会话视图与「无需回复」。
- 常驻传话人扩展到 worker（协议 v14），并下沉为独立模块（SESS-05/06）。
- worker 策略下发超时主动重推 `server.policy_repush`；配置热重载补全：`gofer serve reload`、`worker reload --local`、需重启键清单（CFG-12）。
- worker 一键接入：`gofer worker add|remove`、`POST/DELETE /v1/workers`、`gofer init worker --admin-token`、Runners 页添加 worker（CFG-13）。
- 远程升级 worker：`gofer worker upgrade`（协议 v15，排空交接、失败回滚）（SVC-02）；传话 job 默认隐藏（JOB-15）。
- IM 正文长度可配 `max_text_runes`（OBS-07e）；`gofer job redact` / `job delete`；`gofer job secret-scan` 跨 job 查找与批量脱敏；提交时秘密形态提示（SEC-03/04）。
- 多 agent 对比择优与流程模板：workflow `agents[]` 异构扇出、每路 worktree、`join: pick` + `gofer wf pick --merge`、内置模板 `compare` / `plan-implement` / `review-committee`（WF-05）；`job resume --mode --agent`（JOB-16）。

### [0.80.0 – 0.89.0] - 2026-09-29 → 2026-10-01

- 远程 job 排队时间不计入超时（JOB-13）；job 详情日志渲染性能。
- 全局 / 项目作用域记忆（`memory --global/--project`，`agent:<名>` 标签注入）与 MCP memory 工具（TRK-02）。
- `--lock-wait` 按 job 设置等锁上限（JOB-14）；`repo prime` 精简（TRK-03）。
- pty 取消先发 `exit_keys` 优雅退出以捕获会话 id（PTY-02）；job 标题可编辑（`job set --title`）。
- ACP 持续会话 job：`job run --session`、`job say`、`job end`、`awaiting_input`（ACP-03）；远程 worker 持续会话（协议 v13）（ACP-04）。
- web 给终端会话发消息（SESS-04）；持续会话「等你回复」提醒（OBS-12）。
- `gofer worker show|projects` 与策略诊断；以会话为中心的工作台（WEB-11b）。

### [0.70.0 – 0.79.0] - 2026-09-28 → 2026-09-29

- 续接 job 记录原 agent `resume_agent`（JOB-12）；plan 版本化交接说明（PLAN-04）。
- Web 按预设启停 server 托管隧道（TUN-05）；Stop 等待盯住本会话派出的 job、`gofer session watch`（SESS-03）。
- tracker server 镜像与三方合并同步、`job run --issue`、Web Issues 页（TRK-01 P4）。
- 目录锁细化 `--lock <path>`、`dir_lock_mode: repo`（JOB-11b）；workflow 终态原子收尾；续接复用 agent `global_args`。
- exec job 默认不采集 diff（`capture_diff: auto|on|off`）；plan 标签与过滤（PLAN-05）；命令帮助去掉计划编号（CFG-10）。

### [0.60.0 – 0.69.0] - 2026-09-23 → 2026-09-27

- 凭证补漏：job 环境去掉 `GOFER_CONFIG_DIR`、job 内 CLI 不读 `.env` token、PATH 前置运行它的 gofer（SEC-01 F10）。
- 设置页二级菜单、隧道在 web 可见与 server 侧预设（WEB-12、TUN-03/04）；ACP 真机验收修复（ACP-02）。
- 强制规则库与注入（JOB-06①）；`gofer worker init` 向导（CFG-05）；默认工作空间 `~/.gofer/workspace`；`job run --env`。
- Web 工作台 W1–W3：会话中枢、分屏 / 标签页布局、PWA + Web Push、ACP 对话视图、会话改动视图与行内评审（WEB-11）。
- 未提交改动守卫 `on_uncommitted`（GIT-01）。
- 仓库本地 issue / memory：`gofer repo init|prime|sync|migrate|status`、`gofer issue`、`gofer memory`（`.gofer/tracker/*.jsonl`），SessionStart 注入 prime，bd 迁移（TRK-01）。

### [0.50.0 – 0.59.0] - 2026-09-21 → 2026-09-23

- Windows 桌面会话常驻：`serve -d` / `worker -d` / `stop` 的 Windows 实现（SVC-01，后被 SVC-05 取代）。
- 会话捕获兜底：未配 `session_capture` 的 cli-agent 也能续接（AGT-04）。
- job 级可靠重试：落库 + 租约 sweeper、四级策略、`job run --retry`、`job retry ls|cancel`（AUTO-03）。
- web 编辑 agents / server 配置（WEB-04③）；前端升级后自愈（F8）。
- skills 绑定：server 侧 skill 库、`gofer agent skill`、四级并集绑定（JOB-10）。
- 评论层与 `@agent` 提及派活、leader 回合（MCP-05）；job 作用域凭证（SEC-01，协议 v11）；leader 逐 plan 开启（LEAD-02）。

### [0.41.0 – 0.49.0] - 2026-09-16 → 2026-09-21

- worker 断线恢复 `recovering` 与 serve 重启认领（RECOV-01）；`job run --worktree` 托管 worktree（WT-01）。
- 纯客户端模式 `GOFER_RUN_MODE=client` 与 `gofer init client`（CFG-07）。
- 会话中继：空闲自动布防、三态 `relay_mode`、tmux 送话、`--resume` pty 接管（SESS-01/02）。
- 供应商类错误自动续投（AUTO-RES）；agent 故障转移 `fallback_agents`、健康度与 `agent status|probe`（AGT-03）。
- ndjson 输出投影（OBS-10）；`acp-agent` 类型（ACP-01）；审批门与 `needs_review` 人工验收（GATE-01）；验收台 `/review` 与 `gofer job review`（REV-01）。
- `job run --verify`（JOB-08）、`--todo` 联动、用量 / 成本采集（OBS-09）、任务书模板 `-t/--var`（JOB-02）。
- 文件传输 `gofer tool cp` / `tool xfer`、`--upload` / `--collect`（XFER-01/02）。
- 同目录串行 `waiting_dir`（JOB-11）、输出停滞检测（AUTO-05）、todo 指派即派发（PLAN-02）、job wakeups（JOB-09）。
- pty 转录与会话续接（PTY-01）；todo 依赖链与 `plan run|pause|resume`（PLAN-03）；计划看板（WEB-10）；容器 worker 与 `gofer worker doctor`（CFG-09）；schedule webhook 触发（AUTO-02b）。

### [0.31.0 – 0.40.0] - 2026-07-12 → 2026-09-15

- MCP `--project` 作用域；worker 协议版本与能力上报，提交前按执行 runner 能力校验。
- 内置 agent 模板；worker 配置远程 reload；worker POLICY 模式（server 下发策略，worker 只做 `roots` 映射与 `guards` 收紧，协议 v4）。
- gofer-usage skill 与 `gofer init skill`。
- plan todo 生命周期与决策通道（`gofer_ask_human`、`plan ask|answer`）。
- 终端会话 ↔ web 消息中继：`gofer hook`、`gofer init hooks`、`gofer session`（SESS-01）；钉钉 / 飞书通知（OBS-07a）。
- TCP / UDP 隧道经 worker 转发、命名预设（TUN-01/02）；结构化 JSONL 文件日志（OBS-11）。
- agent 双模式 `args` + `interactive_args`、项目 `allow_interactive`（AGT-02）；job 超时上限可配（JOB-TO）。

### [0.20.1 – 0.30.0] - 2026-06-29 → 2026-07-11

- 监督 agent 自动应答完善：owner-first 升级路由、派生作答白名单、按需派发（MCP-01）；presence 与 role 寻址完善。
- 内置 cron 调度 `gofer schedule`（AUTO-02），含一次性 / 延迟任务。
- Web 控制台 v3：导航重构、Dashboard、`/v1/stats`、Drivers / Inbox、web 应答交互（WEB-06–09）；web 配置管理与项目编辑（WEB-04）。
- 浏览器 pty attach：交互 job、worker 端 pty 隧道、加密录制（WEB-03）。
- `gofer plan` 编排与 todo（PLAN-01），job 血缘、快速重建与请求脱敏。

### [0.11.0 – 0.20.0] - 2026-06-21 → 2026-06-28

- job 事件时间线（OBS-02）；webhook 通知（白名单 / SSRF 防护 / HMAC，OBS-03）。
- 多步工作流 v1 + v2：`${steps.N.*}` 引用、重试、fan-out / join、子工作流、导入导出（WF-01–04）。
- Prometheus `/metrics`（OBS-05）；按 caller 配额与限流（OBS-06）。
- 配置简化：全局单 server + 项目瘦配置 `.gofer.project.yaml`、cwd 推断项目、`path_view`（CFG-04）。
- `serve -d` / `worker -d` 后台运行与 `serve stop` / `worker stop`。
- agent 会话捕获与 `job resume`（JOB-07）；提交来源追踪（OBS-08）；`GOFER_RUN_MODE` 与 `project list --remote`（CFG-06）。
- Web 只读层：项目 git 状态、子仓与关键文件、产物预览、集群拓扑（WEB-01/02/05）。
- `gofer mcp` client 模式（MCP-02）；presence / 信箱（MCP-04）；角色预设 `--role`（MCP-03）；监督 agent 分层自动应答（MCP-01）。

### [0.1.0 – 0.10.0] - 2026-06-16 → 2026-06-21

- 首个版本（当时名为 `agent-bridge`）：项目与 agent 注册表、异步本地 job、`/v1` HTTP API、job CLI。v0.5.0（2026-06-18）起模块、二进制与环境变量改名为 `gofer`。
- SSE 实时流、内嵌 Web 控制台（看板、job 详情、实时日志、提交表单、明暗主题）。
- peer-http runner；stdio MCP server；运行中交互（`pending_interaction`）。
- SQLite 元数据存储与 retention；多 caller token、SIGHUP 热加载、日志轮转、`request_id` 幂等提交。
- WebSocket 远端 worker（hub、心跳、重连、`/v1/runners` 名册）与按标签调度。
- 同步提交与 md+yaml 任务文件；产物、结构化结果、diff 捕获（JOB-01/04、OBS-01）；job 标签与过滤（JOB-03）。
- `job list|watch|rerun`、shell 补全、`gofer init`、`gofer config validate`（CFG-01/02）。
