<!-- template_id: design; template_version: 1.2.0 -->
# 第 3 批：可追溯与机器接入 设计

> 状态：Draft 0.1（待用户审）
> issue：gofer-1uwh（管理操作审计）、gofer-ivmr（断线补传）、gofer-glzl（worker 一行接入，范围按用户 2026-11-14「本机安装 / 注册 / 卸载，远程卸载延后」）、gofer-nhh7 的 token 到期与最近使用（㉕）、gofer-dcmc（server 健康项与连续失败才报，第 2 批 Q7 延后）；随批：gofer-73l5
> 来源：`docs/refer/2026-11-11-super-agent-workflow-survey.md`「分批」第 3 批（㉑㉒㉓㉔㉕）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-14 | Claude（gofer 监督会话） | 初稿 |

## 背景与目标

gofer 已经能派活、跟进、定时，但「谁在什么时候改了什么」「远程机器断线时丢了什么」「新机器怎么接进来」「server 自己是否健康」这几件事还靠人肉排查。本批目标：

1. 改动类操作统一留痕，可按人、动作、时间筛。
2. worker 断线期间产生的审批事件、产出与 ACP 记录不再丢。
3. 新机器一条命令接入并作为用户级服务常驻，命令里不带秘密。
4. 调用方 token 有到期时间与最近使用时间，能认出、能淘汰。
5. server 自检几个关键部件，连续失败才报，偶发抖动不出声。

## 范围与非目标

- 范围：上面 5 项与 gofer-73l5。
- 非目标：远程卸载 worker（延后）；macOS 服务注册（servicemgr 没有 launchd，延后）；配置文件里的调用方（`server.callers`）与 `server.workers` 的 token 到期（随 nhh7 P3 改哈希时再做）；审计请求体内容（只记元数据）；把现有 `auth_audit` / `config_audit` 合并进新表。

## 已确认事实（代码，main 741acbbb）

审计：
- 路由声明已有读写分类：`routeDecl.rw` 与 `effectiveRW(key)`（`internal/httpapi/authz_route_dims.go:83-125`），显式 `ReadSafe()` / `Mutating()` 覆盖，其余按方法（GET/HEAD 读，其余写）；上限闸与生成文档已在用。
- `wrapRoutes`（`authz_routes.go:995`）给每条 `/v1` 路由挂 `authzGate(decl)`（:951），这是唯一同时看得到声明与上下文的地方。`metricsMiddleware`（`metrics_handler.go:34`）已有「计时 `c.Next()` 后取状态码」的写法。
- 调用方：`callerFromCtx` / `callerKindFromCtx`（user / worker / job，`server.go:73-117`），job 凭据另带 job / plan id；会话来自 `X-Gofer-Session-Id`；IP 用 `clientIP(c)`（`job_handler.go:279`）。
- 已有三张审计表：`auth_audit`（授权与拒绝，`GET /v1/auth/audit`，无 CLI）、`config_audit`（调用方变更，`gofer auth caller audit`）、`audit_events`（少数破坏性动作，无列表接口、不清理）。保留期 90 天写死；`PruneAuthAudit` 从未被调用，清理循环只在配置了 `storage.retention` 等时才启动（`serve.go:782`）。

断线补传：
- 镜像事件只有审批类与回合超时类（`internal/job/events.go:51-60`）。worker 侧 `jobEventLoop`（`internal/worker/client.go:543-555`）写失败即丢，只有 Debug 日志；断线时 `writeFrame` 立即失败（:1511）。
- server 侧去重键 `job_id+type+ts+interaction_id`，存在每条连接自己的 FIFO（`internal/wshub/registry.go:162-185`），换连接即失效；`ts` 是秒级，同秒同类事件会撞。重放会重复插入 `job_events` 并重复触发 webhook 与通知。
- stdout/stderr（按 server 持久偏移回卷）、交互、最终 Result（缓存重放）已能跨重连恢复。永久丢失的只有：镜像事件、ACP 记录镜像（`acpOff` 不回卷，`client.go:617-621`）、Outcome 帧（`dispatch.go:316` 发完不管，Result 重放不带它）。
- 协议 `CurrentProtocolVersion = 29`（`internal/wsproto/frames.go:21`），新能力按 `XxxMinProtocolVersion` + `SupportsXxx` 门控；重连握手带 `Inflight`，ack 回 `Resume []ResumeJob{JobID, StdoutOff, StderrOff}`。

worker 接入：
- 没有安装脚本、没有 worker 服务注册、没有拒绝 root。`servicemgr` 只服务 serve：`Spec` 只有 serve 选项、`ServerArgs()` 写死 `serve -c`、读 `serve.pid`，Windows 守护程序是 `serve supervise`；只支持 Windows（计划任务，登录触发、最低权限）与 Linux（systemd system / user）。
- `gofer worker join` 已是无秘密的自助接入：本地生成密钥、打印 8 位配对码，管理员在 Runners 页输码批准后 worker 长轮询拿到 token 写入 `<config-dir>/.env`。Web「添加 worker」生成的是带 token 的 `worker init` 命令。
- 发布产物有 `latest.yaml`（各平台 sha256）；`updatecheck.verifySHA256` 在期望值为空时直接放行（`internal/updatecheck/download.go:322-338`），镜像模板下载不校验。

token：
- 入库调用方表 `auth_callers` 有 `token_hash`、`token_hint`（`gct_…末 4 位`），token 前缀 `gct_` 已有（`internal/core/callers.go:280-296`）；无到期、无最近使用列。授权表 `auth_grants` 已有 `expires_at` / `last_used_at`，按请求尽力更新（`MarkAuthGrantUsed`）。

健康：
- `/health` 只回 `ok:true` 与时间；`serve status` 按进程 / 端口 / 健康 / 版本分级（第 2 批）。没有 server 内部部件的自检。

## 总体方案

### 1. 管理操作审计（gofer-1uwh，㉒）

- 新表 `admin_audit`：`id, at, caller_id, caller_kind, session_id, job_id, method, route（路由模板，如 POST /v1/schedules/{id}/run）, target（路径参数拼成的资源 id）, status, duration_ms, ip, error（失败时的错误摘要，截 200 字，已掩码）`。
- 记录点：`authzGate` 外包一层，只记 `effectiveRW == rwWrite` 的路由，**包括被拒绝的请求**（401/403 也记，这是审计最有用的部分）。路由声明加 `NoAudit()`，给高频且无管理意义的写路由（如 worker 自己的心跳 / 上传、job 凭据的日志追加）豁免，豁免清单在计划里逐条列出。
- 不记请求体与查询值（可能带秘密），只记查询键名。
- 写入异步批量（有界队列，满了丢弃并计数到 `/metrics`），不拖慢请求。
- 查看：`GET /v1/audit?caller=&route=&status=ok|fail&since=&until=&cursor=`；CLI `gofer auth audit`（放在已有 `auth` 组，G033）；Web 设置里「审计」页，表格 + 筛选。权限：新权限 `audit.read`（来源根管理员、不种入，可授予）。
- 保留：`server.audit_retention_days`，默认 90，负数永久；清理循环改为**总是**启动（审计保留不再依赖 `storage.retention`），顺带把从未调用的 `PruneAuthAudit` 接上，`auth_audit` 用同一保留期。

### 2. 断线补传（gofer-ivmr，㉓）

协议 v30（`JobEventSeqMinProtocolVersion = 30`），只在双方都 ≥ 30 时启用，旧组合行为不变。

- **镜像事件**：worker 给每个 job 的镜像事件编号（`JobEvent.Seq`，从 1 递增），发送后保留在该 job 的未确认缓冲里（每 job 上限 256 条、全局上限 4096 条，超出丢最旧并累计丢失数）。server 收到后按 `seq` 去重：`jobs` 表加 `worker_event_seq`（已应用的最大序号），`seq <= 已应用` 直接跳过，否则插入事件并在同一事务里推进序号——这样重放不会重复插行，也不会重复发 webhook / 通知。
- **确认与重放**：重连 ack 的 `ResumeJob` 加 `EventSeq`（server 已应用的最大序号）；worker 丢弃 `<= EventSeq` 的缓冲，按序重放其余的，再恢复正常发送。正常连接期间不逐条 ack（避免流量翻倍），缓冲靠重连 ack 与 job 结束时清理。
- **终态顺序**：job 结束时 worker 先重放该 job 未确认事件，再发 Outcome，再发 Result；Result 缓存重放时同样先带 Outcome（Outcome 随 Result 一起缓存）。
- **ACP 记录镜像**：`ResumeJob` 加 `AcpOff`（server 侧 `acp.jsonl` 已写字节数），worker 回卷到该偏移重发，与 stdout/stderr 同一口径。
- **丢失可见**：缓冲溢出时，重连后 worker 补一条 `job.events_lost {count}` 事件，job 时间线上能看到缺口。
- 旧的连接内 FIFO 去重保留给 < 30 的 worker。

### 3. worker 一行接入（gofer-glzl，㉔，本机部分）

- **servicemgr 泛化**：`Spec` 加 `Kind`（`serve` / `worker`）与 worker 选项（worker id、worker.yaml 路径）；参数构造、pid 文件、描述文字按 Kind 分开；Windows 守护程序加 `worker supervise`。worker 只提供**用户级**：Linux systemd `--user`（并提示 `loginctl enable-linger` 以便未登录时常驻），Windows 计划任务（登录触发、最低权限）。macOS 报「暂不支持，用 `gofer worker -d`」。
- **命令**：`gofer worker service register [--id <id>] [--start]`、`start`、`stop`、`status`（退出码沿用第 2 批 serve status 的分级）、`uninstall [--purge]`。`--purge` 额外删除该 worker 的 worker.yaml、`.env` 里的 worker token 与运行时文件，用于「这台机器不再接入」；不加时只摘掉服务登记。
- **拒绝 root**：`register` 在 Linux 以 uid 0 运行时拒绝；Windows 不拒绝管理员身份，但任务始终以最低权限运行。
- **一行安装**：server 提供公开路由 `GET /install.sh` 与 `GET /install.ps1`（静态模板，内容只含 server 地址，不含任何秘密）。脚本：判断平台 → 从 server 配置的更新源下载对应二进制 → **必须**用 `latest.yaml` 的 sha256 校验（取不到校验值即失败）→ 装到用户目录（`~/.local/bin` / `%LOCALAPPDATA%\gofer\bin`）→ 以 root 运行即退出 → 执行 `gofer worker join --server <addr>`（屏幕显示配对码，管理员在 Runners 页批准）→ `gofer worker service register --start`。
- Web「添加 worker」把一行命令放在首位（`curl -fsSL <server>/install.sh | sh` 与 PowerShell `irm <server>/install.ps1 | iex`），带 token 的 `worker init` 命令收进「其他方式」。
- 说明：脚本与二进制都来自 server 或其更新源，能篡改 server 响应的人本来就能控制整个系统；sha256 防的是下载损坏与镜像被替换。

### 4. 调用方 token 到期与最近使用（gofer-nhh7 ㉕）

- `auth_callers` 加 `expires_at`、`last_used_at`。`gofer auth caller add|rotate --expires <时长|日期|never>`，HTTP 与 Web 调用方页同步。
- 判定：已过期的入库调用方视同停用（401，`auth_audit` 记 `request.denied reason=token_expired`）；根管理员（配置文件）不受影响。
- 最近使用：命中时在内存里记时间，每分钟批量落库一次（不在每个请求写库）。
- 提醒：到期前 7 天在「等我」出一张「调用方 token 即将到期」卡（动作：轮换 / 看详情），过期当天变为「已过期」。
- 列表显示：`gct_…末 4 位`（已有）、到期、最近使用；超过 30 天未用的标灰，方便淘汰。

### 5. server 健康项与连续失败才报（gofer-dcmc，㉑）

- server 每 60 秒跑一组自检，每项维护连续失败计数，**连续 3 次失败**才判为故障，1 次成功即恢复：
  - 存储可写（对库做一次小写入）；
  - 数据目录剩余空间（低于 1GB 或 5%）；
  - 定时扫描是否卡住（距上次扫描超过 3 个周期）；
  - 出站通知（最近 N 次投递全部失败的通道）。
- 入口：`GET /v1/health/checks`（需登录）；`/health` 加 `degraded: true|false`（不含细节，保持公开）；`serve status` 在有故障项时归 4（部件故障）并列出项名；`gofer doctor` 显示同一组结果。
- 通知：某项转为故障时发 `server.health_failing` 并在「等我」出卡，恢复时发 `server.health_recovered` 并收卡；同一项故障期间不重复发。

### 6. 随批小修（gofer-73l5）

回执确认时子 job 状态已变，已送出的回执也计入 `fired_count`。

## 架构

- 审计：`httpapi`（中间件、路由声明）→ `jobstore`（`admin_audit`、保留清理）；写入队列放 `httpapi` 内的小组件，不进 `job`（G021/G022）。
- 补传：`wsproto`（字段与版本）、`worker`（缓冲与重放）、`wshub`（ack 带 `EventSeq` / `AcpOff`）、`job`（按序号去重插入，事务内推进 `worker_event_seq`）。
- 接入：`servicemgr`（泛化）、`commands`（`worker service` 只做绑定转发）、`httpapi`（安装脚本路由，模板放 `internal/webui` 或新 `internal/install` 包）、`updatecheck`（安装路径强制校验）。
- token：`jobstore`（列）、`core/callers`（到期判定、最近使用批量落库）、`inbox`（到期卡）。
- 健康：新 `internal/serve/healthcheck`（编排层，周期与计数），各项检查由所属包提供探针函数；`inbox` 出卡。

## 关键流程

断线重放：worker 断线 → 事件进缓冲 → 重连 Register（Inflight 照旧）→ server 回 ack，`ResumeJob` 带 `EventSeq`、`AcpOff` → worker 丢弃已确认、按序重放、回卷 ACP → 恢复实时发送。job 在断线期间结束：重放事件 → Outcome → Result（缓存）。

## 安全、数据、运维与回滚

- 审计不记请求体与查询值；错误摘要按授予的 secret 掩码。审计表只有 `audit.read` 能读。
- 新列与新表都是加法迁移（G057：基础 schema 不引用新列，补旧库升级用例）。
- 协议 v30 是可选字段，旧 worker / 旧 server 组合退回现状。
- 安装脚本不含秘密；校验值缺失即失败；以 root 运行即退出。
- token 到期对现有调用方不生效（默认 `never`），不会因升级被锁在门外。
- 回滚：旧版本忽略新列与新表；`worker service` 登记的服务在旧版本上无法管理，回滚前先 `gofer worker service uninstall`（写进发版说明）。

## 决策

见下表推荐项；用户批准后转为决策。

## 待确认事项

| 编号 | 问题 | 选项 | 推荐 |
|---|---|---|---|
| Q1 | 审计记录哪些请求 | A：所有写路由（含被拒绝的），少数高频路由显式豁免；B：只记用户调用方的写操作，job 凭据与 worker 不记 | **A**。agent 用 job 凭据做的改动正是需要追溯的 |
| Q2 | 审计存哪 | A：新表 `admin_audit`，现有两张审计表不动；B：扩 `auth_audit` 加列，所有审计进一张表 | **A**。`auth_audit` 的语义是授权判定，混进去筛选更乱 |
| Q3 | 谁能看审计 | A：新权限 `audit.read`（默认只有根管理员，可授予）；B：沿用 `auth.manage` | **A** |
| Q4 | 断线补传范围 | A：镜像事件 + Outcome + ACP 记录三项都补；B：只补镜像事件 | **A**。Outcome 丢了结果摘要与提交就缺，代价小 |
| Q5 | worker 服务命令放哪 | A：`gofer worker service register/start/stop/status/uninstall`；B：与 serve 对齐成 `gofer worker register/start/uninstall/status`（与已有 `worker stop` 并存） | **A**。避免与已有 `worker stop`（停后台进程）混淆 |
| Q6 | 安装脚本从哪来 | A：server 自己提供 `/install.sh`、`/install.ps1`；B：放到发布产物 / 镜像里，命令带 server 地址参数 | **A**。一行命令只需 server 地址，Web 直接给出 |
| Q7 | 新建 token 的默认到期 | A：默认不过期，可选设置；B：默认 90 天 | **A**。个人自托管，默认过期容易把自动化锁在门外；到期提醒照做 |
| Q8 | 更新 / 升级路径缺校验值时 | A：本批只让安装路径强制校验，升级路径不变；B：升级路径也改为缺校验值即失败 | **B**。发布一直带 `latest.yaml`，静默不校验是隐患；镜像缺文件时会明确报错 |
| Q9 | 健康项的「连续失败」阈值 | A：固定 3 次（每 60 秒一次，约 3 分钟）；B：可配置，默认 3 | **B**，配置键 `server.health.fail_threshold` |

## 验收

- §1 单测：写路由记录、读路由不记、被拒绝请求记录、豁免路由不记、请求体与查询值不落库、队列满丢弃计数、保留清理（含 `auth_audit`）、筛选与分页、`audit.read` 权限；`TestEveryRouteIsDeclaredOrPending` 与生成文档更新（G056）。
- §2 单测：断线期间事件缓冲、重连后按 `EventSeq` 重放且不重复插行 / 不重复通知、缓冲溢出写 `job.events_lost`、断线期间结束的 job 按「事件 → Outcome → Result」到达、ACP 回卷、< 30 的 worker 走旧路径。真实验收：容器 worker 跑一个会触发审批的 job，中途断开 worker 网络，恢复后 host 时间线完整、通知不重复。
- §3 单测：servicemgr worker 规格（参数、pid、单元文件 / 任务 XML）、root 拒绝、`--purge` 删除范围、安装脚本模板不含秘密、校验值缺失失败。真实验收：在一台干净的 Linux 用户账号（容器）与本机 Windows 用户下执行一行命令 → 配对 → 服务常驻 → 重启后自动连上 → `uninstall --purge` 后无残留。
- §4 单测：过期即 401 并记审计、`never` 不过期、最近使用批量落库、到期前 7 天出卡、过期当天卡片变化。
- §5 单测：每项探针成功 / 失败、连续 3 次才故障、1 次恢复、故障期间不重复通知、`serve status` 归 4、`/health` 的 `degraded`。
- 反向验证：去掉审计中间件 → 记录用例失败；去掉按序号去重 → 重放用例失败；去掉 root 检查 → 拒绝用例失败；去掉到期判定 → 401 用例失败；去掉连续计数 → 抖动用例失败。
- 文档（G045 / G046）：skill references（运维、worker 接入、安全与权限、HTTP 在 `docs/reference/http-api.md`）、`docs/reference/permissions.md`（生成）、CLI 帮助 golden、CHANGELOG（含升级路径校验的行为变化）。

## 结论与人工计划 Gate

改动类操作统一留痕；worker 断线期间的审批事件、产出和 ACP 记录重连后补齐且不重复；新机器一条命令接入为用户级服务；调用方 token 可设到期并显示最近使用；server 自检关键部件、连续失败才报。等用户答复 Q1–Q9 并批准后写计划。
