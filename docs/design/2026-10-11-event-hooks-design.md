# AUTO-04 事件钩子（只读旁路）设计

- issue：gofer-u4e1
- 路线图：AUTO-04（N4，idea #4「插件机制」；roadmap-history E26「事件 hook（只读旁路）先做，决策 hook 后做」）
- 状态：已实施（设计 2026-10-11 用户确认两点：独立 hooks 块、只能改配置文件；实施记录见第八节）

## 一、问题

gofer 的事件（job 终态、plan 完成 / 阻塞、会话等你、预算熔断、每日摘要……）现在只能外发到 webhook（generic / 钉钉 / 飞书）。想在事件发生时做点本地的事——写一行到自己的日志系统、触发一个本地同步脚本、给某个内部系统发消息——就得另起一个 HTTP 服务接 webhook。

目标：server 主机上配置「事件 → 命令」，事件发生时运行该命令，事件 JSON 从 stdin 传入。**只读旁路**：命令的输出不回写 gofer，不影响 job / plan 的流程；成败只影响这次投递本身的重试。

不做（留给后续「决策 hook」）：提交前否决 / 改写、交互自动应答、根据命令输出改变流程。

## 二、配置

独立于 `server.notification.webhooks`（webhook 处处假设有 URL：URL 校验、allow_hosts、签名、投递 target），放在 `server.event_hooks`：

```yaml
server:
  event_hooks:
    - name: audit-log                       # 必填，唯一；投递记录 target = hook:<name>
      command: ["python3", "D:/ops/gofer_audit.py"]   # 必填，argv 形式，不经 shell
      events: [job.terminal, plan.completed] # 可选；空 = 与 webhook 相同的默认事件集；["*"] = 全部
      projects: [my-tools-dev]               # 可选；空 = 全部项目（无项目的事件只匹配空过滤）
      dir: D:/ops                            # 可选；工作目录，缺省 = gofer 配置目录
      env: {AUDIT_ENDPOINT: "http://127.0.0.1:9000"}  # 可选；额外环境变量（值不进任何 API 视图）
      timeout_sec: 30                        # 可选；默认 30，上限 300
      max_attempts: 3                        # 可选；默认 3（含首次）
      enabled: true                          # 可选；默认 true
```

**编辑权限（用户确认）**：`server.event_hooks` 只能改配置文件，改完 `gofer serve reload` 生效。配置写接口（web 配置页、`PATCH /v1/config`）对它一律拒绝；读视图只展示 name / command / events / projects / dir / timeout / max_attempts / enabled 与 env 的**键名**。理由：钩子等于在 server 主机上执行命令，没开 `governance.require_admin_capability` 时任何已认证调用方都能改配置，放开编辑就是远程执行。

信任模型：与 git hooks 相同——由能改 server 配置文件的运维写的命令视为可信，以 serve 进程的用户身份运行。

## 三、匹配与入队

两条事件路径都接入，与 webhook 共用匹配规则（events / projects 过滤），各自产生一条投递记录（`event_deliveries`，target = `hook:<name>`）：

1. `recordEvent` → `enqueueDeliveries`（job / plan / xfer / config 等落 `job_events` 的事件）：在匹配 webhook 的同一处匹配钩子；
2. `NotifyEvent`（session.* / work.* 预渲染事件）：为钩子预渲染 generic JSON（不是 IM 文本）存进 `body`。

- `notification.enabled`（通知总暂停）与项目 `notify_enabled` **不作用于钩子**：它们是「通知」开关，钩子是自动化旁路，用钩子自己的 `enabled` / `projects`。
- 没有任何钩子与 webhook 匹配时不产生记录（与现状一致）。

## 四、执行

投递 sweeper（`DeliverDue`）认领到 `hook:` 行时交给钩子执行器：

- **并发**：钩子行在独立 goroutine 中执行，最多 4 个并行；webhook 行仍串行 POST。一个慢钩子不会拖住 IM 通知。sweep 结束前等本轮钩子全部结束（受 timeout 约束），租约语义不变（至少一次）。
- **进程**：`exec.CommandContext(command[0], command[1:]...)`，不经 shell；`procattr.Background`（Windows 不弹窗）；超时杀整个进程树（与本机 runner 同一套）。
- **stdin**：与 generic webhook POST body 完全相同的 JSON（`{event, job, link}`；预渲染事件即其 body），读者可以用同一份解析代码。
- **环境**：serve 进程环境去掉 SEC-01 拒绝列表里的凭据变量（`GOFER_*_TOKEN` 等），再加：
  - `GOFER_EVENT_TYPE`、`GOFER_EVENT_SEQ`（无则空）、`GOFER_DELIVERY_ID`、`GOFER_JOB_ID`（无则空）、`GOFER_HOOK_NAME`；
  - 钩子自己的 `env`（最后叠加）。
- **结果**：退出码 0 = 已投递；非 0 / 超时 / 启动失败 = 失败，按 webhook 同一退避表（30s / 2m / 5m / 15m / 60m）重试，到 `max_attempts` 记 failed。
- **输出**：stdout 读走丢弃（只读旁路，最多读 64 KiB 防止管道阻塞）；stderr 末尾 1 KiB 经脱敏后写入投递的 `last_error`。
- **日志**：每次执行在 serve.log 记 `event_hook.ran`（name、event_type、delivery_id、exit_code、duration_ms），失败为 `event_hook.failed`（附脱敏后的 stderr 尾部）。
- 钩子在入队后被删除 / 停用：该行记 failed（`hook removed` / `hook disabled`），不再认领。

## 五、可见性

- `gofer config validate`：name 重名 / 非法、command 为空、timeout 越界报错；`command[0]` 在本机找不到时警告。
- `GET /v1/config` 读视图按第二节展示；web 配置页通知区下方只读列出钩子（「只能在配置文件中修改」）。
- 投递记录：job 相关事件的钩子投递照常出现在 `GET /v1/jobs/{id}/deliveries`（target 为 `hook:<name>`）。

## 六、测试

- config：解析、校验、默认值、事件 `*`。
- notify：钩子匹配（events / projects / enabled / `*`），与 webhook 匹配互不影响。
- job：两条路径入队 `hook:` 行；`notification.enabled=false` 不挡钩子；执行器——stdin 内容、环境（含去掉 token 变量）、退出码成功 / 失败重试 / 超时杀进程、stderr 尾部入 last_error、钩子删除后记 failed、慢钩子不阻塞 webhook。测试用 testcmd 辅助程序，不依赖 shell。
- httpapi：配置写接口拒绝 `server.event_hooks`；读视图不含 env 值。

## 七、文档

- `docs/runbook/im-notification.md` 加「事件钩子」一节（配置、stdin 样例、调试方法：把样例 JSON 管道给脚本自测）；顺手改正「看 serve 日志里的 NotifyEvent 警告」的旧说法。
- skill `references/server-config.md` 加 `event_hooks`（面向任意项目，G046）。
- `config/gofer.example.yaml` 加注释示例；CHANGELOG「新增」；路线图 AUTO-04 移到已落地（决策 hook 另列待做）。

## 八、实施记录（2026-10-11，分支 feat/auto04-event-hooks）

**落地内容**

- config：`internal/config/event_hooks.go` 定义 `EventHookConfig`（`IsEnabled` / `Target` / `EffectiveTimeout` / `EffectiveMaxAttempts` / `EffectiveDir` / `EnvKeys`）与加载校验（名字 `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`、重名、空命令、`timeout_sec` 0..300、`max_attempts` ≥ 0、空事件项、非法 env 键）；`ServerConfig.EventHooks`（`event_hooks`），`Clone` 深拷贝。字段策略 `server.event_hooks: {HotReload: true}`——不可编辑，写接口（`PUT /v1/config/server`、`POST /v1/config/validate`）统一回 `field not editable: server.event_hooks`；其它 server 字段写入时整块重渲染会原样带回文件里的钩子（测试覆盖）。
- 匹配：`notify.MatchEventHooks`，与 webhook 共用 events / projects 规则，加 `*` 通配；跳过停用 / 无命令的钩子。OBS-13 的「摘要无订阅」告警把订阅了 `work.digest` 的钩子也算作订阅者。
- 入队：`enqueueDeliveries` / `enqueueScopedDeliveries` 拆成 webhook 半（保留通知开关与项目 `notify_enabled`）与钩子半（不受这些开关影响）；`NotifyEvent` 同样拆分，钩子行预渲染 generic JSON。job 事件的钩子行不预渲染，执行时按 generic webhook 的方式从事件表重建，所以 stdin 与 generic webhook body 逐字节一致。
- 执行：`internal/job/event_hooks.go`。`DeliverDue` 在只有钩子时也运行；认领后把 `hook:` 行交给独立 goroutine（信号量 4），webhook 行仍串行 POST，sweep 返回前等钩子结束。进程：`exec.CommandContext` + `procattr.Background` + `proctree`（超时杀整树）+ `WaitDelay 2s`；env = `util.EnvironWithout(effectiveJobEnvDeny(cfg), …)` + 五个 `GOFER_*` 变量 + 钩子 env（`util.MergeEnv`，钩子最后叠加）；stderr 保留末尾 1 KiB，先替换钩子 env 的值再走 `secret.RedactString`，写入 `last_error` 与 `event_hook.failed` 日志。钩子删除 / 停用 → `hook removed` / `hook disabled` 记 failed。
- 可见性：`gofer config validate` 每个钩子一行 OK / WARN（`command[0]` 用 PATH 或路径查找，找不到只 WARN）；`GET /v1/config` 的 `server.event_hooks`（timeout / max_attempts 为生效值，env 只给 `env_keys`）；web 配置页 server 编辑框通知区下方只读列表 + 「只能在配置文件中修改」提示。
- 测试辅助：testcmd 新增 `hook-capture`（记录 stdin / cwd / 指定 env）与 `wait-file`（证明并发）。

**与设计的偏差及原因**

- stdout 直接接空设备（`cmd.Stdout = nil`），而不是「读走最多 64 KiB 丢弃」：效果相同（不会因管道写满阻塞，也不占内存），还少一条管道和一个拷贝 goroutine。
- 认领租约保持 60s 不变：serve 的投递循环一次只跑一轮 sweep，本轮还在执行的行不会被再次认领；租约只在进程崩溃后起作用，不需要按钩子超时加长。
- serve 被停止（sweep ctx 取消）打断的钩子：不计一次尝试、不改状态，行留在租约里，租约到期后重跑（至少一次）。设计没写这一情形。
- `GOFER_JOB_ID` 只给真正的 job；plan / 传输等以 `<scope>:<id>` 记的事件为空（事件 JSON 里的 `event.job_id` 仍是原值）。
- 手动结束的会话 job 的 `job.terminal`：webhook 照旧不发（避免打扰人），钩子照样收到（审计类钩子需要完整记录）。
- `dir` 为相对路径时相对 gofer 配置目录解析；缺省目录不存在时用 serve 进程的工作目录。
- 已知边界：所有钩子与 webhook 都从配置删光后，`DeliverDue` 直接返回，残留的 `hook:` 行留在 pending，直到再配上任何一个钩子或 webhook 才被记 `hook removed`（与 webhook 全删时残留行的现状一致）。
- 本轮未做「决策 hook」（路线图另列 AUTO-04b）。
