<!-- template_id: plan; template_version: 1.2.0 -->
# 第 3 批：可追溯与机器接入 实施计划

> 状态：Draft 0.1（待用户审）
> 设计：`docs/design/2026-11-14-traceability-and-machine-onboarding-design.md` Draft 0.1（已批准，Q1–Q9 = 推荐项）
> issue：gofer-1uwh、gofer-ivmr、gofer-glzl、gofer-nhh7（㉕ 部分）、gofer-dcmc、gofer-73l5

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-15 | Claude（gofer 监督会话） | 初稿 |

思考模式：RIGOROUS。核心目标：设计 §1–§6 落地并通过真实验收。Scope freeze：只实施设计与下文「对设计的细化」。Review budget：按「治理与改动成比例」，计划不另起评审轮，用户批准即开工；实施审查每条链一轮，有阻塞问题才返工一次。

## 目标与完成定义

设计「验收」各条单测通过；真实验收（断线补传、一行接入、审计、token 到期、健康项）通过；反向验证五项按预期失败；gofer-verify 四路通过；合入 main、关 issue。

## 范围、排除项与授权

范围：设计 §1–§6，加一处第 2 批遗留文案修复（R12）。排除：设计「非目标」；`docs/capabilities.md` 随发版更新。

授权：批准后按功能点本地提交、建 worktree、派子 agent 与 suga job、跑主机 job 与临时 server。真实验收会在本机 Windows 当前用户下注册一个计划任务并在验收结束时卸载。push 被拦时向用户申请；发版另行请示。

## 对设计的细化

| # | 问题 | 定案 |
|---|---|---|
| R1 | 审计挂点 | `wrapRoutes`（`authz_routes.go:1006`）改为 `rt.Use(s.auditGate(decl), s.authzGate(decl))`：审计在外层，`c.Next()` 之后取状态码，被授权拒绝的请求也记下。只记 `effectiveRW == rwWrite` 且未标 `NoAudit()` 的路由 |
| R2 | 审计写入 | `httpapi` 内小组件：有界队列 1024，单 goroutine 每秒或每 100 条批量插入；满了丢弃并计入 `/metrics` 的 `gofer_audit_dropped_total`；`Server` 关闭时排空（G051） |
| R3 | 审计字段 | `target` = 路由路径参数按 `name=value` 拼接；`error` 只记 gofer 自己能确定的拒绝原因（401 / 403 / 上限 / 限流），其他失败只记状态码（不缓冲响应体，避免带出秘密） |
| R4 | 豁免清单 | T1 列出全部写路由，标 `NoAudit()` 的只限：worker 自身凭据的上报类路由、job 凭据的日志 / 心跳类追加路由、`POST /v1/inbox/state` 这类已标 `ReadSafe` 的不必再标。清单写进汇报，审查逐条看 |
| R5 | 保留与清理 | 配置 `server.audit_retention_days`（默认 90，负数永久，0 = 默认）；清理循环改为总是启动；`PruneAuthAudit` 接进同一循环，`auth_audit` 用同一保留期 |
| R6 | `audit.read` | authz catalog 登记：来源根管理员、不种入、可授予用户、不可授予会话；`go generate ./internal/httpapi/...` 更新 `permissions.md`（G056） |
| R7 | token 到期判定 | 过期调用方仍留在内存快照里（带 `ExpiresAt`），在 `lookupCallerIn` 命中后判定，返回 401 并写 `auth_audit` `request.denied reason=token_expired`；`--expires` 接受 `90d` / `2026-12-31` / `never`；`rotate` 不带 `--expires` 时保留原到期 |
| R8 | 最近使用 | 命中时写内存 map（调用方 → 时间），每 60 秒与关机时批量落库；列表接口返回 `last_used_at` |
| R9 | 到期卡片与健康卡片的类别 | 两种新卡都放「等我」的 `failed` 类之前新增的 `attention` 类（标签「需要你处理」），排在 `failed` 之后、`delivery` 之前；卡片 kind `caller.token_expiring` / `server.health`，key 分别按调用方 id / 检查项名 |
| R10 | 健康探针 | 新包 `internal/serve/healthcheck`：周期 60 秒、阈值 `server.health.fail_threshold`（默认 3）；探针：存储（对 `health_probe` 小表 upsert 一行）、磁盘（数据目录所在卷，`golang.org/x/sys` 取剩余空间）、定时扫描（scheduler 暴露 `LastSweepAt()`，超过 3 个周期算失败）、出站通知（notify 暴露各通道连续投递失败数，≥ 阈值算失败）。T3 先确认这两个暴露点是否已有等价物 |
| R11 | 健康结果的出口 | `GET /v1/health/checks`（登录即可读，G056 登记）；`/health` 加 `degraded`；`/v1/stats` 加 `health_failing: [名称]`，`serve status` 读它，有故障项时归 4 并打印项名 |
| R12 | 第 2 批遗留文案 | `schedule.paused` 卡当前归在 `failed` 类，标签显示「你提交的任务失败了」（第 2 批验收所见），改归 R9 的 `attention` 类 |
| R13 | 补传协议 | `wsproto` v30：`JobEvent.Seq int64`（每 job 从 1 递增）、`ResumeJob.EventSeq int64`、`ResumeJob.AcpOff int64`；`JobEventSeqMinProtocolVersion = 30` 与 `SupportsJobEventSeq`；双方都 ≥ 30 才启用 |
| R14 | server 去重 | `jobs` 加 `worker_event_seq`；jobstore `InsertWorkerJobEvent(ev, seq) (applied bool, err)`：同一事务里判 `seq > worker_event_seq` 再插入并推进；只有 `applied` 才走 webhook / 通知 |
| R15 | worker 缓冲 | 按远端 job id 的未确认缓冲（每 job 256、全局 4096，溢出丢最旧并计数）；重连 ack 后丢弃 `<= EventSeq`、按序重放；job 终态时依次「重放未确认事件 → Outcome → Result」，Outcome 随 Result 进同一缓存（`workerResultTTL`）；溢出计数非零时补发 `job.events_lost {count}` |
| R16 | ACP 回卷 | server 侧 `AcpOff` = 该 job `artifacts/acp.jsonl` 当前大小；worker 回卷 `acpOff` 后重发，口径同 stdout |
| R17 | servicemgr 泛化 | `Spec.Kind`（`serve` / `worker`，旧 spec 读作 `serve`）与 `WorkerOptions{ID, ConfigDir}`；按 Kind 构造参数、pid 文件（`worker-<id>.pid`）、描述；T5 先读 `serve supervise`，能参数化就复用，否则加隐藏的 `worker supervise`；worker 只允许用户级（systemd `--user`、Windows 登录任务最低权限），macOS 报不支持 |
| R18 | 命令 | `gofer worker service register|start|stop|status|uninstall`，`register` 在 Linux uid 0 时拒绝；`uninstall --purge` 删除该 worker 的 worker.yaml、`.env` 中的 `GOFER_WORKER_TOKEN` 行与 `run/worker-<id>.*`，不动其他文件 |
| R19 | 安装脚本 | 新包 `internal/install`（embed 模板）；路由 `GET /install.sh`、`GET /install.ps1`、`GET /install/manifest?os=&arch=`（`/v1` 之外、公开、只读）；manifest 给出 server 自己版本的下载地址与 sha256（取自更新源的 `latest.yaml`），取不到校验值返回 404，脚本即失败；server 地址取 `server.web_base_url`，未配置时取请求的 Host |
| R20 | 升级路径校验（Q8） | `verifySHA256` 期望值为空即报错；镜像模板下载改为同时取该版本 `latest.yaml` 的校验值；错误文案说明「更新源缺少校验值」 |
| R21 | 73l5 | 回执确认时子 job 状态已变的分支也计 `fired_count`，补用例 |

## 前置与 fail-closed 条件

- T0（Claude）：导出 v0.214.x 的 schema 到 `internal/jobstore/testdata/schema-v0.214.0.sql`（G057，第 2 批改过 schema）；主机建三个 worktree：`.worktrees/b3`（`feat/b3`，集成）、`.worktrees/b3-a`、`.worktrees/b3-b`，基于当时的 main，`.git` 改相对 gitdir，`web/node_modules` 链接主 checkout。
- Go 命令：`GOWORK=off`，`GOCACHE` 在 `tmp/_cache/<名>`，`TMPDIR=/tmp/gofer-gotmp/<名>`。
- fail-closed（出现即停，回报）：基线相关包测试在未改动时失败；需要设计未列的权限；补传需要改 stdout/stderr 或交互的现有恢复路径；servicemgr 泛化需要改变 serve 已登记服务的行为。

## 波次与依赖

```
W0  T0 前置（Claude）
W1  [b3-a] T1 审计 → T2 token 到期与最近使用 → T3 健康项（含 R9、R12）（sonnet A，串行）
    ‖ [b3-b] T4 断线补传 → T5 servicemgr 泛化与 worker service 命令 → T6 安装脚本与校验收紧（sonnet B，串行）
W2  审查两条链；合并 b3-a、b3-b → b3（Claude，--no-ff）；b3 上相关包测试
W3  [b3] T7 73l5（suga）→ T8 web（suga）→ T9 文档（suga）
W4  T10 真实验收、反向验证、gofer-verify、合并收尾（Claude）
```

共享文件负责方：`internal/httpapi/authz_routes.go` 与 authz catalog——A 改 `/v1` 路由与 `audit.read`，B 只加 `/v1` 之外的安装路由（不进 `routeDecls`，若覆盖测试要求登记则 B 按 `Static` / `Public` 登记）；`internal/jobstore` 迁移——各加各的列与表，迁移函数分文件；`internal/serve/serve.go`——A 加健康检查启动与清理循环调整，B 不改；`internal/i18n/lang` 各加自己的词条；`internal/job/model.go` 事件常量——`server.health_*` 归 A、`job.events_lost` 归 B。

## 任务

| 任务 | 执行方 | 内容 | 验收 |
|---|---|---|---|
| T0 | Claude | schema 快照、worktree | 旧库升级用例在 b3 上通过 |
| T1 | sonnet A | §1 + R1–R6 | 设计验收 §1 各条；豁免清单 |
| T2 | sonnet A | §4 + R7、R8，到期卡（R9） | 设计验收 §4 各条 |
| T3 | sonnet A | §5 + R9–R12 | 设计验收 §5 各条；`schedule.paused` 卡改类 |
| T4 | sonnet B | §2 + R13–R16 | 设计验收 §2 各条；协议版本表与注释 |
| T5 | sonnet B | §3 前半 + R17、R18 | servicemgr worker 规格、root 拒绝、`--purge` 范围；serve 现有用例不变 |
| T6 | sonnet B | §3 后半 + R19、R20 | 模板无秘密、缺校验值失败、manifest 用例；升级路径缺校验值失败 |
| T7 | suga | R21 | 新用例修复前失败 |
| T8 | suga | Web：审计页（设置下）、调用方页的到期 / 最近使用 / 标灰、Runners「添加 worker」一行命令置顶、健康项（设置或概览，用 `GET /v1/health/checks`）、新卡片动作接线 | `vue-tsc`、`vitest`、`vite build` 通过（主机跑） |
| T9 | suga | 文档：skill references（运维、worker 接入、安全与权限、web 控制台）、`docs/reference/http-api.md`、CHANGELOG「未发布」（含升级路径校验的行为变化）、`docs/runbook/2026-09-26-worker-init-runbook.md` 加一行接入 | 删除行审查、业务名检查 |
| T10 | Claude | 真实验收（下节）、反向验证、`git diff --stat main...feat/b3`、私有名检查、gofer-verify、主机合并、合并确认后清理 worktree、实施记录、关 issue、遗留另立 issue | — |

## 真实验收（T10）

主机临时 server（同第 2 批做法，独立配置目录与端口）：

1. 断线补传：容器里起一个连到临时 server 的 worker，中间隔一个本地 TCP 转发进程（临时小工具，放 `tmp/`），worker 跑一个会触发审批的 ACP / 回显 job；审批请求发出前后各断开转发一次再恢复；核对 host 时间线事件完整、无重复行、通知 webhook（临时接收端）计数不重复；job 断线期间结束时 Outcome 与 Result 都到达。
2. 一行接入（Linux）：容器里新建普通用户，执行 `curl -fsSL <server>/install.sh | sh` → 配对码 → 在临时 server 批准 → `worker service status` 为运行 → 杀掉 worker 进程后服务自动拉起 → `uninstall --purge` 后无残留；以 root 执行脚本被拒绝。
3. 一行接入（Windows）：主机当前用户、独立配置目录执行 `irm <server>/install.ps1 | iex`，同上流程；结束 `uninstall --purge`，核对计划任务已删除。
4. 审计：做一次成功的写操作、一次被拒绝的写操作、一次读操作，`gofer auth audit` 只看到前两条且字段正确。
5. token：给入库调用方设 1 分钟到期，到期前可用，到期后 401 且审计有记录；`last_used_at` 在一分钟内更新。
6. 健康项：把临时 server 的数据目录剩余空间阈值临时调高（测试用配置），连续 3 个周期后「等我」出卡、`serve status` 返回 4；恢复后卡片消失。

## 反向验证

去掉审计中间件 → 记录用例失败；去掉按序号去重 → 重放用例失败；去掉 root 检查 → 拒绝用例失败；去掉到期判定 → 401 用例失败；去掉连续计数 → 抖动用例失败。破坏须能编译（筛 `[build failed]`）。

## 风险与回退

- 审计写入拖慢请求：异步队列 + 丢弃计数；验收时看 `/metrics`。
- 补传与现有恢复路径交织：fail-closed 条件已限定只动镜像事件 / Outcome / ACP 三处。
- 计划任务残留：验收脚本按自己的任务名卸载，结束时核对；不按名称模糊删除。
- 回退：新列与新表旧版本忽略；`worker service` 登记的服务回退前先卸载（发版说明写明）。
