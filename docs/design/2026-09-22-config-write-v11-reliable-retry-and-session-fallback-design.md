<!-- template_id: design; template_version: 1.1.1 -->
# 配置写层 V1.1、可靠重试与会话捕获兜底设计（WEB-04③ / AUTO-03 / AGT-04）

> 状态：Approved 0.2 / 实施中（2026-09-22 人工批准，决策 1–5 照初稿；分期 R1 → R2 → R3，全部 omp）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.2 | 2026-09-22 | Claude | 人工批准，决策 1–5 不变；R1（AGT-04）开工 |
| 0.1 | 2026-09-22 | Claude | 初稿：WEB-04③ agents/server 在 web 编辑（外科写回 + 热重载盲区显式化 + secret 只引用不落盘）；AUTO-03 重试可靠版（落库 + sweeper + 租约，进程重启不丢）；AGT-04 会话捕获兜底（新增 agent 免配即可续接，jcode 实测驱动） |

## 背景与目标

三件事都指向同一个痛点：**新增/调整一个 agent，仍要上主机改 yaml、再手工 reload；失败的 job 仍要人来重派；不是 claude/codex/omp 的 agent 连会话 id 都拿不到。**

- **WEB-04③**：web 控制台的「系统配置」页至今是只读（`web/src/views/Config.vue:92` 标题写着「系统配置（只读）」）。写层 V1 只做了 projects 的 CRUD（`POST/PUT/DELETE /v1/projects`，`internal/project/registry.go:170` 落 `config.Save`）。agents / server 的任何改动都要 RDP 上主机编辑 `D:\work\inhere\config\win-env\gofer\config.yaml`。外科写回（只重写真正改动的顶层块、保留注释与 `interactive_args: []`）与重复块自愈在 v0.47/v0.48 已经做完，**写回这一层已经可靠，缺的只是端点与界面**。
- **AUTO-03**：job 级重试现在是「进程内最小版」——`internal/job/execute.go:679 maybeRetryJob` 用 `time.AfterFunc` 排一个定时器，注释里就写明「a process restart loses a pending retry」。退避表 `[30,120,300,900,3600]` 写死在 `internal/job/retry.go:22`，`RetryPolicy` 只能从 `JobRequest.Retry` 传，**没有 server/agent/项目级默认**，也没有 CLI 开关，所以实际上没人用：失败的 job 都是我看到之后手工 `job rerun`。
- **AGT-04**：`session_capture` 的内置默认只覆盖 claude/codex/omp（`internal/agent/registry.go:167 builtinSessionDefaults`），且三条正则都按 **uuid** 写死。2026-09-22 用户新增 `jcode`（OpenCode Go 系，client/server 架构）后实测：交互退出横幅是

  ```
  Session hamster - to resume:
    jcode --resume session_hamster_1790079148520_bc5cb0d44153fe56
  ```

  id 不是 uuid，`job show` 的 `session_id` 为空、`job resume` 不可用。用户由此提出「每新增一种 agent 都需要代码适配吗」——答案应该是"不需要"，但今天的兜底不够。

目标：**agents/server 在 web 改；失败重试由 server 自己扛住重启；新 agent 不写一行配置也能拿到会话 id。**

非目标：配置的多人协同编辑 / 版本回滚（V1.2 再说）；把 secret 值搬进 web（永远只编辑 `*_env` 引用）；workflow step 级重试语义变化（共用同一套 `RetryPolicy`，不动）。

## 已确认事实（代码 / 环境）

- 写回：`config.Save(path, cfg)`（`internal/config/writer.go:57`）按**顶层块**外科写回——未管理的顶层键原文保留；管理的块只在规范化序列化真的变了才重写（块内注释会丢）；`mergeTopKeys` 会丢弃重复顶层块并 `slog.Warn`（v0.47.2 自愈）。
- 写路径：`Registry.Add(key, proj, force)` → 校验 → `config.Save` → `Core` 侧 `Reload/ReloadWith` 原子换 config（`internal/core/core.go:41 updateMu` 串行化所有写路径）。
- 热重载覆盖面（`core.go` Reload 注释）：**覆盖** 增删 projects / agents、改已有 runner 的字段；**不覆盖** 新增一个 runner *类型*（新 peer-http 条目要重启才实例化）。另有若干「进程级」项只在启动时读一次（如 worker 的 `xfer_timeout_sec`、server 监听地址/token）。
- 权限：projects 的写端点用 `s.cfg.CallerCanAdmin(caller)` 闸（`internal/httpapi/project_handler.go:114/153/186`），403 文案已有；`GET /v1/config` 无写层，返回 `configView`（server/storage/projects/agents/runners/roles/supervisor/presence 各自的 view，已做裁剪）。
- 重试：`RetryPolicy{MaxAttempts,BackoffSec,OnExitCodes}`（`internal/job/retry.go:13`）；`MaxAttemptsPolicy/BackoffForPolicy/RetryableExitPolicy` 三个纯函数已被 step 级与 job 级共用；`maybeRetryJob` 只对 `StatusFailed` 生效（cancel/timeout 明确不重试），重投时 `RequestID=""`（每次尝试是新 job，不走 C5 幂等）、`Attempt+1`。
- 可靠定时的现成范式：`job_wakeups` 表 + `DueWakeups(now)` + serve 的 `sweepDueWakeups`（`internal/serve/serve.go:768`）；投递侧还有**带租约**的 `ClaimDueDeliveries(now, limit, lease)`（`internal/jobstore/deliveries.go:138`）——AUTO-03 直接复用后者的形状。
- 会话捕获：`CaptureSessionIDBytes(b, reSrc)`（`internal/job/outcomes.go:221`，F6 起取**第一个非空捕获组**，因此正则可写多分支交替）；终态扫描 `captureSessionID` 扫 `stdout.log`/`stderr.log`，交互 job 追加扫去 ANSI 的 `pty.txt`；`builtinSessionDefaultFor(key, a)` 只按 **agent key** 匹配内置表。
- jcode 现状：主机已注册 `jcode`（cli-agent）与 `jcode-acp`（acp-agent）；`jcode` 的 `session_capture` 为空。`/exit` 不被它接受（`Unknown skill: /exit`），退出用 `/quit`。

## 一、WEB-04③ 配置写层 V1.1（agents / server）

### 1. 端点（都带 `CanAdmin` 闸，与 projects 一致）

| 方法 | 路径 | 说明 |
|---|---|---|
| `PUT` | `/v1/config/agents/{key}` | 新增或整体替换一个 agent 定义（body = agent 的可编辑字段集） |
| `DELETE` | `/v1/config/agents/{key}` | 删除一个 agent（内置模板 key 允许删除"覆盖"，回落内置） |
| `PUT` | `/v1/config/server` | 部分更新 server 块（**仅白名单字段**，见下） |
| `POST` | `/v1/config/validate` | 干跑：拿 body 构造候选 config 跑完整校验 + 返回**影响面**（哪些字段需要重启），不落盘 |
| `POST` | `/v1/config/reload` | 显式触发一次 reload（等价 SIGHUP；Windows 无 SIGHUP，这是唯一的手动入口） |

写流程统一走一个 helper `mutateConfig(fn func(*config.Config) error) error`：`core.updateMu` 下 **快照 → 应用 fn → 全量 `config.Validate` → `config.Save` → `core.ReloadWith(newCfg)`**；任一步失败则原样返回错误，磁盘与内存都不动。保存失败不得留下半个文件（`Save` 已是 temp+rename）。

### 2. agent 的可编辑字段（白名单，不是整个结构体）

`command / args / interactive_args / read_only_args / env_keys / type / acp.* / session_inject / session_capture / session_resume / session_resume_interactive / system_inject / transient_error_patterns / max_concurrent / stall_timeout_sec / ndjson_* / fallback_agents / health.*`。

- **secret 永不入 body**：凡是 `*_env` 的字段编辑的是"环境变量名"，值不读不写不回显；任何形如 `token/secret/password` 的**字面值字段**在 V1.1 里是只读的（web 显示为"由 env 提供"）。
- `env_keys` 之外的 `env` map 保持只读（它会落 `request_json`，model.go:286 已有告诫）。
- 删除一个"覆盖了内置模板"的 agent（如 `claude`）不是删能力，而是**回落到内置定义**；响应里明确告知这一点。

### 3. server 块的可编辑白名单与"重启才生效"

可编辑：`max_job_timeout_sec / auto_resume_max / stall_timeout_sec / approval / notification.* / runner_probe.* / retry`（AUTO-03 新增，见下）/ `session.*`。

**只读（改了要重启，web 上禁用输入并给出徽标）**：`addr / token / token_env / allow_empty_token / storage.*`（DB 路径）/ 新增 runner 类型 / `path_view`。

实现方式不是硬编码两张表，而是在 `config` 包里给字段打标：新增 `internal/config/editable.go`，用一张 `map[string]FieldPolicy{Editable, RestartRequired, SecretRef}`（按 `server.xxx` 这样的点路径）。`GET /v1/config` 的 view 里为每个块附带 `editable`/`restart_required` 两个布尔数组（web 据此渲染），`PUT` 侧用同一张表拒绝越权字段（`400 field not editable: server.addr`）。**一张表、两处消费**，避免前后端各写一份漂移。

### 4. web 界面

- 「系统配置」页标题去掉"（只读）"；Agents 卡片每行加「编辑 / 删除」，顶部加「新增 agent」；Server 卡片加「编辑」。
- 编辑弹窗：左表单右 YAML 预览（预览是 `POST /v1/config/validate` 的回显，不是本地拼的），底部显示影响面（"保存后立即生效" / "需要重启 server 才生效"）。
- 保存成功后 toast 里带一行"已重载"，并刷新 `GET /v1/config`。
- 校验失败把 server 返回的字段路径高亮到对应输入框。

### 5. 审计

每次配置写记一条事件 `config.updated {section, key, by, fields}`（进现有事件流与通知白名单的可选集，不进默认集）。**不记具体值**（可能含路径/主机名），只记字段名。

## 二、AUTO-03 job 重试可靠版

### 1. 持久化

新表 `job_retries`（与 `job_wakeups` 同构，带租约，照 `ClaimDueDeliveries` 的形状）：

```sql
CREATE TABLE IF NOT EXISTS job_retries (
  id            TEXT PRIMARY KEY,      -- rt-<8hex>（XFER-02 风格短 id）
  source_job_id TEXT NOT NULL,         -- 失败的那个 job
  attempt       INTEGER NOT NULL,      -- 即将发起的尝试序号（>=2）
  request_json  TEXT NOT NULL,         -- 重投用的 JobRequest（含 Attempt/CallerID/SourceJobID）
  reason        TEXT NOT NULL,         -- exit_code=N / transient:<pattern> / stall
  next_run_at   INTEGER NOT NULL,
  lease_until   INTEGER NOT NULL DEFAULT 0,
  state         TEXT NOT NULL DEFAULT 'pending', -- pending|claimed|done|cancelled
  new_job_id    TEXT,
  created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_job_retries_due ON job_retries(state, next_run_at);
```

- `maybeRetryJob` 不再 `time.AfterFunc`，改为**写一行 pending**（同一事务里随终态落库，所以"终态已写、重试没排上"不可能发生）。
- serve 新增 `startRetryLoop`（15s 一轮，与其他 sweeper 并列）：`ClaimDueRetries(now, limit, lease)` → 逐条 `Submit` → 成功写 `state=done,new_job_id` → 失败则释放租约并把 `next_run_at` 推到下一档退避（不丢，最多多等一轮）。租约让多实例/重启期间不会重复投。
- 进程重启：pending 行还在，重启后的第一轮 sweeper 照常投——**这就是"可靠版"的全部含义**。

### 2. 触发面扩大（这才是它现在没人用的原因）

- **配置层级**：`server.retry`（全局默认）→ `agents.<k>.retry` → `projects.<k>.retry` → `JobRequest.Retry`（就近覆盖，与 `stall_timeout` 的三级一致）。结构就是现有的 `RetryPolicy`，additive。
- **默认值**：全部为空 = 关闭（与今天行为一致，不给任何人惊喜）。runbook 里给推荐值：`{max_attempts: 3, backoff_sec: [60, 300], on_exit_codes: []}`。
- **CLI**：`job run --retry <n>[:<backoff1,backoff2,...>]`（如 `--retry 3:60,300`），`job show` 展示 `retry: attempt 2/3, next at …`，`job list --tag retry` 可筛（重投的 job 自动带 `retry` tag 与 `retry_of:<源 job>`）。
- **与已有两条自动路径的边界**（必须写清，否则会叠加重投）：
  - `auto_resume`（供应商 transient 错误 → 同会话续投）**优先**；它接手了就不排 retry。
  - `AUTO-05 stall`（输出停滞）已经按 transient 处理，同上。
  - 只有"agent 真的跑完并失败"（`StatusFailed` + 退出码可重试）才进 `job_retries`。`cancelled/timeout` 仍然永不重试。
  - `needs_review`/`rejected` 不进重试（人已经在闭环里）。

### 3. 可观测

- 事件：`job.retry_scheduled {retry_id, attempt, next_run_at, reason}`、`job.retry_started {retry_id, new_job_id}`、`job.retry_exhausted {attempts}`（后者**进通知默认集**——重试都用光了就是要人看的信号，与 `plan.blocked` 同级）。
- web：job 详情顶部出现「重试 2/3 · 下次 14:05」的条；`job.retry_exhausted` 在验收台里像 `needs_review` 一样可见。
- `GET /v1/jobs/{id}/retries` 列出该 job 的重试链；`gofer job retry cancel <retry-id>` 取消一次待发的重试。

## 三、AGT-04 会话捕获兜底（新 agent 免配）

### 1. 兜底正则

`builtinSessionDefaultFor` 在**按 key 命中内置表失败**时，给任何 `cli-agent` 填一条通用 `SessionCapture`：

```
(?i)(?:^|\s)(?:--resume|resume|--session[-_]?id)[=\s]+["']?([A-Za-z0-9][A-Za-z0-9._-]{7,127})["']?
```

要点：

- token 放宽到「字母数字开头、`._-` 可选、8–128 位」，覆盖 uuid、`session_hamster_1790079148520_bc5cb0d44153fe56`、`ses_01H…` 这类 ULID/前缀式 id。
- 只在**去 ANSI 后的文本**上匹配（PTY-01 起 `pty.txt` 与日志都已去 ANSI）。
- 显式配置永远覆盖兜底；内置表命中（claude/codex/omp）也不用兜底。
- 扫描顺序：内置/显式正则 → 兜底正则。兜底命中时记事件 `job.session_captured {agent, by: "fallback"}`，方便发现"该给这个 agent 写条正则了"。

### 2. 误抓防护

- **只扫尾部**：兜底只在输出的**最后 4KB**（去 ANSI 后）上跑。退出横幅永远在尾部；正文里偶然出现的 `--resume xxx`（比如 agent 在解释命令用法）多数在中部，能显著降低误抓。
- **黑名单**：捕获到的 token 若是 `<session_id>`、`SESSION_ID`、`session-id`、`your-session-id`、`…`、纯 `-` 之类占位符，丢弃。
- 捕获值长度 > 128 或含空白 → 丢弃。

### 3. resume 模板兜底

会话 id 抓到了还得能续接。给没有 `session_resume` 的 cli-agent 填兜底模板：

- `session_resume: ["--resume", "{{session_id}}", "-p", "{{prompt}}"]`
- `session_resume_interactive: ["--resume", "{{session_id}}"]`

这是 claude/omp/jcode 三家共同的形态（codex 是 `exec resume`，但 codex 有内置表命中，不受影响）。**若某 agent 的 resume 语法不同**，配置里写一行覆盖即可——这正是"不用改代码"的那一层。

### 4. jcode 验收

- 交互 job：退出横幅 → `session_id = session_hamster_…`；`pty.txt` 可读（F6 的空格修复已在）。
- `job resume`：起交互续接 job，TUI 里能看到上一轮历史。
- 不写任何 `agents.jcode.session_*` 配置就要通过——这就是本项的验收标准。
- 另外在 runbook 补一节「新增一个 cli-agent 需要配什么」：最小只需 `command`（+ `interactive_args` 若要交互），会话/续接默认兜底；需要覆盖时再写 `session_*`；ndjson 投影与新 agent 类型才需要改代码。

## 横切

- G032：不新增兼容分支。`maybeRetryJob` 的进程内 `time.AfterFunc` 路径**直接删除**（被落库路径取代，没有外部依赖）；`RetryPolicy` 结构与三个纯函数不动（step 级共用）。
- 协议：不涉及 worker wire（重试在 hub 侧排、按原 runner 重投），协议版本不变。
- 安全：配置写端点一律 `CanAdmin`；secret 只编辑 env 名；`config.updated` 事件不记值。
- 迁移：`job_retries` 是新表（`CREATE TABLE IF NOT EXISTS`，随 Open 建）；老库无迁移动作。配置全部 additive，旧 config 行为不变（retry 默认关闭）。

## 实施分期与验收（全部 omp，测试先写先提交）

| 期 | 内容 | 验收 |
|---|---|---|
| **R1** | AGT-04 全部（兜底正则 + 尾部窗口 + 黑名单 + resume 模板兜底 + `session_captured` 事件） | 单测：`TestFallbackCaptureAcceptsNonUUIDToken`（jcode 真机原文）、`TestFallbackCaptureOnlyScansTail`、`TestFallbackCaptureRejectsPlaceholders`、`TestBuiltinKeyBeatsFallback`、`TestFallbackResumeTemplateApplied`；容器全量 |
| **R2** | AUTO-03：`job_retries` 表 + `ClaimDueRetries` + `startRetryLoop` + 三级配置 + CLI + 事件 + web 条 | 单测：`TestRetryRowWrittenWithTerminal`、`TestRetrySurvivesRestart`（新 Store 打开后 sweeper 仍投）、`TestRetryLeasePreventsDoubleSubmit`、`TestRetryExhaustedEvent`、`TestAutoResumeWinsOverRetry`、`TestCancelledNeverRetried`；`gofer job run --retry 2:5` 的真机 smoke（临时 server） |
| **R3** | WEB-04③：`editable.go` 字段表 + 四个端点 + `mutateConfig` + web 编辑弹窗 | 单测：`TestAgentPutCreatesAndReloads`、`TestAgentPutRejectsSecretLiteral`、`TestServerPutRejectsRestartOnlyField`、`TestConfigValidateDryRunDoesNotWrite`、`TestConfigWriteRequiresAdmin`、`TestSurgicalSaveKeepsComments`（改 agents 不动 projects 块的注释）；`pnpm typecheck && pnpm build`；真机：在 web 里给 `jcode` 加一条 `session_capture` 并保存 → `gofer agent show jcode` 能看到、config.yaml 的其他块注释无损 |

真机验收（R3 之后，我在容器发起 + 用户在 web 点）：**在 web 上完成一次"新增 agent → 试跑 → 调参数"的完整闭环，全程不 RDP 上主机。**

## 风险与限制

- **外科写回会丢被编辑块内部的注释**（`writer.go` 已声明的既有行为）。web 编辑 agents 块 = 该块注释丢失。缓解：弹窗里提示一次；runbook 建议把长注释写在**未管理的顶层键**或块外。
- **兜底正则误抓**：尾部窗口 + 黑名单降低概率，但无法为零。影响面是"`job resume` 用了个错 id 然后失败"，不会损坏数据；`session_captured{by:"fallback"}` 事件让这种情况可发现。
- **重试放大**：三级配置若在 server 级开得太松，一个必然失败的 job 会重投 N 次（每次都真的跑 agent，消耗额度）。缓解：默认关闭；`on_exit_codes` 建议配；`job.retry_exhausted` 进默认通知；runbook 明确"先在单个 job 上用 `--retry` 验证，再考虑 agent/server 级默认"。
- **配置写与手工编辑并发**：`updateMu` 只保护进程内；用户同时在主机编辑器里改 yaml 仍可能覆盖。缓解：`mutateConfig` 保存前比对文件 mtime，变了就 409 让前端刷新后重试（与 artifact 的冲突处理同思路）。
- **restart_required 字段表要人维护**：新增字段时漏标就会出现"web 改了没生效"。缓解：R3 加一个测试 `TestEveryServerFieldHasPolicy`，反射遍历 `ServerConfig` 字段，缺策略即失败。

## 决策（已批准 2026-09-22）

1. 配置写层这一期只做 **agents + server 白名单**；callers/roles/runners/notification 目标留到 V1.2（notification 的 secret 面更复杂）。
2. secret **只编辑 env 名**，字面值字段在 web 上只读。
3. 重试落库 + 租约 sweeper，**删掉**进程内 `time.AfterFunc` 路径；默认仍然关闭。
4. `auto_resume` / stall 优先于 retry；`cancelled/timeout/needs_review` 永不重试。
5. AGT-04 兜底只在**尾部 4KB**、只给 `cli-agent`、命中记事件；显式与内置配置永远优先。

## R1 实测记录（2026-09-22，omp job）

**落地范围**：§三 AGT-04 全部 —— 兜底正则（`agent.FallbackSessionCapture` / `IsFallbackCapture`）、
resume 模板兜底（`FallbackSessionResume[Interactive]`）、尾部 4KB 窗口、占位符过滤
（`acceptableSessionID`）、`job.session_captured {agent,by,source}` 事件（终态捕获 + 实时 pty
捕获两条路径）、web 时间线图标/标签/详情、runbook 新增《新增一个 cli-agent 需要配什么》。
提交：`452cc10`（测试）→ `1d5a4d6`（agent）→ `42cef87`（job/web）。

**与设计的偏差（3 处，均为实现细节，语义不变）**

1. 兜底正则的起始分支按任务书写成 `(?:^|[\s"'])`（设计 §1 只写了 `(?:^|\s)`）：让 `"…--resume x"` 这种
   被引号包住的横幅也能命中。
2. `TestFallbackCaptureRejectsPlaceholders` 落在 `internal/job`（不是 `internal/agent`）：占位符过滤按
   §三.2 明确"不写进正则"，只存在于 `internal/job.acceptableSessionID`，在 agent 包内无法测到该行为。
3. 实时 pty 捕获（`internal/httpapi`）也记同一条事件：交互 job 的 id 通常**在流里就被抓到**，终态扫描
   随即跳过；不在这里记，"兜底命中可发现"这条设计意图在最典型的场景（交互 TUI）里就失效了。

**单测（`go test ./internal/agent/... ./internal/job/... ./internal/httpapi/... -run 'Fallback|SessionCapture|CaptureSessionID|PtySessionID|Transcript' -v`，exit 0，30 PASS / 0 FAIL）**

```
--- PASS: TestFallbackCaptureAcceptsNonUUIDToken (0.00s)      # jcode 真机原文（非 uuid）
--- PASS: TestFallbackCaptureAcceptsUUIDAndPrefixedIDs (0.00s)
--- PASS: TestBuiltinKeyBeatsFallback (0.00s)
--- PASS: TestExplicitCaptureBeatsFallback (0.00s)
--- PASS: TestFallbackResumeTemplateApplied (0.00s)
--- PASS: TestFallbackSkipsExecAndACP (0.00s)
--- PASS: TestFallbackCaptureOnlyScansTail (0.01s)
--- PASS: TestFallbackCaptureRejectsPlaceholders (0.01s)
--- PASS: TestFallbackCaptureRecordsEvent (0.74s)
--- PASS: TestFallbackPtyCaptureReadsOnlyTheTailWindow (2.22s)
ok  	github.com/inhere/gofer/internal/agent	(cached)
ok  	github.com/inhere/gofer/internal/job	4.067s
ok  	github.com/inhere/gofer/internal/httpapi	(cached)
```

**真机 jcode 验收：未做（主机侧无法驱动 TUI）**

主机上 `jcode` 确实在（`D:\env\bin\jcode.exe`，`jcode v0.86.0`），但本机没有"提交交互 job 并驱动它的
TUI"的通道：`gofer job run --interactive` 只是**请求** pty job，交互本身要靠 attach 客户端
（web 的 xterm 或 pty ws + relay nonce），CLI 侧没有 `job attach`；唯一能往 pty stdin 写字面的
`JobRequest.InitialInput` 是 `json:"-"` 的内部字段（只由 session-takeover 流程填写），HTTP/CLI 都传不进去。
按本 job 的硬约束（不得指向真实配置、临时 server 必须随机端口、不得 reload 正在跑本 job 的 server），
没有在主机上拼 attach 客户端。**请在容器侧用 attach 驱动补验**：

```bash
gofer job run -a jcode -p <proj> --interactive --prompt "..."
#   TUI 里 /quit（不是 /exit）退出
gofer job show <job-id>     # 期望 session_id = session_hamster_…（不写任何 agents.jcode.session_* 配置）
gofer job resume <job-id> --prompt "继续"   # 期望进入上一轮 TUI
```

**顺带修掉的既有测试**（AGT-04 改变了它们钉的契约，不是重钉文案）

- `internal/agent`：`TestNonSessionAgentUnchanged` → `TestFallbackSessionDefaultsForUnknownAgent`；
  `TestNonInteractiveAliasDoesNotGainSessionDefaults` → `…GainBuiltinSessionDefaults`。两者原来断言
  "未知/非交互 cli-agent 的 session 字段全空"，现在断言"拿到通用兜底、且**不被注入** `--session-id`"。
- `internal/job`：`TestResumeJobResumeUnsupported` 原来用"配了 inject 但没 resume 模板的 cli-agent"，
  该形状已被兜底填满；改用唯一还剩的载体 —— 显式带 `session_id` 的 **exec** job（argv 是调用方的，
  gofer 无从渲染模板）。
