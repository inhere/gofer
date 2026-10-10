# 更新日志

本文件记录 gofer 每个版本中**用户可见**的变化（功能、行为变化、重要修复、兼容性说明），格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)（1.0 之前 minor 版本也可能含不兼容变化，见各条「变更 / 移除」）。

维护方式：

- 每次发版（打 tag）时新增一个版本条目；内容来源是**上一个 tag 到本 tag 之间的提交**（`git log <上一个 tag>..<本 tag>`），只挑用户可见的变化，纯重构、测试、tracker / 记忆同步、设计文档不写。
- 日期取 tag 日期；条目尽量带路线图编号（见 [`docs/gofer-enhancements-roadmap.md`](docs/gofer-enhancements-roadmap.md)）或 issue id。
- 尚未发版的用户可见改动先记在「未发布」，发版时整段移入新版本。
- v0.100.0 起逐版本记录；更早的版本按主题区间压缩记录（见文末「早期版本」）。

## [未发布]

### 变更

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
