# 更新日志

本文件记录 gofer 每个版本中**用户可见**的变化（功能、行为变化、重要修复、兼容性说明），格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)（1.0 之前 minor 版本也可能含不兼容变化，见各条「变更 / 移除」）。

维护方式：

- 每次发版（打 tag）时新增一个版本条目；内容来源是**上一个 tag 到本 tag 之间的提交**（`git log <上一个 tag>..<本 tag>`），只挑用户可见的变化，纯重构、测试、tracker / 记忆同步、设计文档不写。
- 日期取 tag 日期；条目尽量带路线图编号（见 [`docs/gofer-enhancements-roadmap.md`](docs/gofer-enhancements-roadmap.md)）或 issue id。
- 尚未发版的用户可见改动先记在「未发布」，发版时整段移入新版本。
- v0.100.0 起逐版本记录；更早的版本按主题区间压缩记录（见文末「早期版本」）。

## [未发布]

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
