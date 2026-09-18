<!-- template_id: design; template_version: 1.1.1 -->
# 监督闭环与 agent 可靠性设计（SUP-01：故障转移 / 验证步骤 / todo 联动 / 用量 / 模板 / 事件镜像）

> 状态：Approved 0.2 / 已实施（P1–P5）（2026-09-18 人工批准；决策 1–5 照初稿）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-18 | Claude | 初稿：A agent 故障转移 + 健康度 + 探针；B job 验证步骤 `--verify`；C plan todo 自动联动 + 提交列表采集；D 监督期间不自动布防（顺带补会话 `caller_id`，关 h-aii-esus）；E 用量/成本记录；F 任务书模板；G worker 侧审批事件镜像到 hub（h-aii-msm2） |
| 0.2 | 2026-09-18 | Claude | 人工批准（决策 1–5 照初稿）。新增横切约束：按 AGENTS.md G032 兼容策略执行——本设计涉及的旧路径（`relay` bool 镜像列、`server.session_auto_relay_idle_sec` 别名、`interactive_allowed_agents` 一次性读取、旧 worker 协议容忍分支等）遇到即打 `DEPRECATED` 标记或直接剔除，不再新增无标记兼容层 |

## 背景与目标

2026-09-14~18 这一周用 gofer 把 gofer 自己的开发派给主机 agent（codex / omp），供人和 Claude 监督。跑下来最费人的不是功能缺失，而是**监督环节全靠手工**：

- codex 供应商一天挂三次（at capacity / stream disconnected / 本地 sandbox 管道超时），每次都是人（或 Claude）手工探针、手工把同一份任务书改派 omp（A）。
- agent 的汇报不能当验收，每批都要在容器里重跑同一套 `gofmt / build / vet / go test ./...`（B）。
- 每个 job 结束都要手工 `plan set-todo` 并抄提交 hash（C）。
- Claude 监督 job 时结束回合等通知，会话中继的 Stop hook 按"距上次人工输入"自动布防、阻塞两小时，job 完成通知送不进来（D，bd h-aii-s2v4）。
- agent 用量只能等"没量了"才知道（E）。
- 每份任务书都复制同一段"通用约束"（F）。
- worker 上跑的 ACP agent 求批时 hub 收不到通知（G，bd h-aii-msm2）。

目标：把这六件事做成 gofer 的内建能力，让"派活 → 跑 → 验证 → 记录 → 失败转移"不再需要人在中间搬运；全部**默认关闭或向后兼容**，老配置零行为变化。

## 已确认事实（代码）

- `job.finish()`（`internal/job/execute.go`）在状态翻转前决定 `needsReview` / `willAutoResume` 并记录对应事件（8e03d6e）；`autoResumeHit(snap)` 是纯判定（配置、session、次数、stderr 尾部 8KB 命中 `transient_error_patterns`），`autoResume(snap, hit)` 提交续投。
- `JobRequest.request_json` 完整持久化，`job rerun` 已能用原请求重提；`ResumedFrom` / `ResumeSourceAgent` / `ReviewFixed` 是 `json:"-"` 的内部标记先例。
- outcomes（`internal/job/outcomes.go`）：终态采集 `changes.diff` + `diff_summary`（worktree job 用 `base..HEAD` + 工作树）；`runner.Outcome` 是 worker/peer 回传的产出载体（RenderedCommand / ResultJSON / DiffSummary / Artifacts 元数据 / SessionID / Worktree*）。
- `agent_sessions` 无 `caller_id` 列（h-aii-esus）；HTTP 层 `callerFromCtx(c)` 可得认证 caller；`sessionrelay.WaitReason()` 按 `relay_mode` → 键盘空闲 → 距上次人工输入 三级判定。
- `plan_todos` 有 `status/note`，`plan set-todo --status --note` 整体覆盖 note；`gofer plan attach <plan> <job>` 把 job 挂 plan（`jobs.plan_id`）。
- ndjson 投影器（`internal/runner/ndjsonfilter`）已读 claude `result.total_cost_usd`（只作展示字段）、omp `message_end.message.usage`；acp runner 把 `usage_update` 记进 `acp.jsonl`；codex `exec` 在 stderr 末尾打 `tokens used\n<n>`。都未入库。
- `wsproto` 当前协议 v7；worker 的交互经 `interaction{open}` 帧镜像到 hub，**job 事件不镜像**。
- `GET /v1/agents`（`internal/httpapi/agent_handler.go`）返回配置/内置 agent 及 detect 可用性缓存。

## 一、A：agent 故障转移、健康度与探针（AGT-03）

### 配置

```yaml
agents:
  codex:
    fallback_agents: [omp]            # 全局默认；有序，逐个尝试
server:
  agent_health:
    window_sec: 3600                  # 统计窗口
    degraded_after: 3                 # 窗口内供应商类失败 ≥N → degraded
    recover_after_ok: 1               # 窗口内成功 ≥N 即恢复（默认 1）
  agent_fallback:
    on_failure: true                  # 失败后转移（默认 true，仅当配置了 fallback_agents）
    pre_dispatch: false               # 主 agent degraded 时提交即改派（默认 false）
projects:
  hyy-ai-inspect:
    agent_fallbacks: { codex: [omp] } # 项目覆盖（可选）
```

`job run --fallback omp[,claude]` 覆盖上面两层；`--no-fallback` 关闭本 job 的转移。备用 agent 必须在项目 `allowed_agents` 内且**与主 agent 同执行形态**（batch）；不满足的候选跳过并记 warn。

### 触发与动作

- **失败后转移**（`finish()` 内，紧接 auto-resume 判定之后）：条件 = job `failed` 且 `autoResumeHit` 命中（同一套 transient 模式，含新增内置模式 `windows sandbox failed|connecting runner pipe`）且**不会自动续投**（无 session / 次数用尽 / agent 不可续 / 续投提交失败），或 job 本身就是一次自动续投载体（`ResumedFrom != ""` 且 `AutoResumeAttempt > 0`）再次命中 —— 即"同一 agent 已经试过续也不行"。
- 动作：以源 job 的 `request_json` 构造新请求：`Agent = 下一个候选`，`FellBackFrom = 源 job id`（`json:"-"` 内部标记，链式回溯用 `jobs.fell_back_from` 列），`FallbackDepth+1`（上限 = 候选数），`Cwd` 走 `resumeCwd(src)`（源 job 的 worktree 内继续，半成品可见），prompt 前缀一段固定说明：`上一次由 <agent> 执行，因供应商错误（<hit>）中断；先 git status / git log 看进度，只做剩余部分，不要重做已提交的工作，然后按原要求汇报。`，`SessionID` 清空（新会话），`Title` 追加 `(→omp)`，`plan_id/tags/timeout/read_only/review/verify/todo_id` 原样继承。源 job 记事件 `job.fell_back {to_job, agent, reason}`（**不**记 `job.terminal`，与自动续投同理：IM 不该收到一条即将被接管的失败），源行 `fell_back_to` 列指向新 job；候选用尽仍失败 → 正常 `job.terminal`。
- **提交即改派**（`pre_dispatch: true` 时）：`Submit` 解析 agent 后查健康度，主 agent `degraded` 且有候选 → 直接用第一个健康候选，记事件 `job.agent_substituted {from, to, reason: degraded}`，job 行 `agent` 即备用（`requested_agent` 列保留原请求）。默认关，避免"我明明指定了 codex"式意外。
- 转移链最多 `len(候选)` 次，`fell_back_from` 非空的 job 不再自动续投源（续投由新 agent 自己的 session 负责）。

### 健康度

- 数据源：`jobs` 表新增 `failure_class TEXT`（`transient|other|''`），`finish()` 在 failed 时按 `autoResumeHit` 的模式匹配填（不依赖 auto-resume 是否开启：判定函数拆成"模式匹配"与"续投资格"两层，匹配层不看 session/次数）。
- 计算：`jobstore.AgentHealth(agent, window)` → `{jobs, ok, transient_fail, other_fail, last_ok_at, last_transient_at}`；状态 `healthy | degraded | unknown`（窗口内无样本）。`degraded` = 窗口内 `transient_fail ≥ degraded_after` 且最近一次 transient 失败之后成功数 `< recover_after_ok`。纯查询，无新表，无后台任务。
- 暴露：`GET /v1/agents` 每项加 `health {state, window_sec, transient_fail, ok, last_transient_at}`；web Agents 页徽标（healthy 绿 / degraded 橙 + 提示"最近 1h N 次供应商错误"）；`gofer agent status [key]`（新子命令组 `gofer agent`，`list` 复用现有 agents 列表）。
- **探针**：`gofer agent probe <key> [-p project] [--timeout 120]` = 提交一个 `--sync` job（prompt 固定 `只回复一行 OK，不要做任何其他事情。`，`tags: [probe]`，`title: probe <key>`），打印 status/耗时/首行输出，退出码 0/1；它就是一个普通 job，健康度天然计入。web Agents 页放"探针"按钮（调 `POST /v1/agents/{key}/probe` → 同一提交路径，返回 job id）。

### 非目标

不做 agent 的模型/配额查询（各家 CLI 无统一接口）；不做跨机（worker）健康度分桶（健康度按 agent key 统计，worker 上跑的 job 一并计入）。

## 二、B：job 验证步骤 `--verify`（JOB-08）

### 语义

- 输入：`job run --verify '<argv…>'`（CLI 用 shell-words 拆成 argv；YAML/HTTP 用 `verify: [cmd, args…]`），项目默认 `projects.<key>.verify: [...]` + `verify_timeout_sec`（默认 600）；`--no-verify` 关闭本 job 的项目默认。**argv 直接执行、不经 shell**，需要 shell 就显式写 `bash -lc '…'`（与 exec job 同口径）。
- 时机：agent 进程**正常结束（exit 0）**之后、`finish()` 之前，在**同一台执行机、同一 cwd（worktree job 就在 worktree 内）**、同一 env 下执行；agent 失败/取消/超时 → `verify.status = skipped`。
- 输出：stdout+stderr 合并追加到本 job 的 `stderr.log`，前后各一行横幅 `===== gofer verify: <argv> =====` / `===== gofer verify: exit=<n> dur=<ms> =====`（worker 路径经既有 stderr 镜像即可回到 hub，不新开文件通道）；结构化结果 `JobResult.Verify {command, status: passed|failed|timeout|skipped, exit_code, duration_ms}` 持久化 `jobs.verify_json`；事件 `job.verify_started` / `job.verify_finished {status, exit_code}`。
- 状态映射：passed → 按原逻辑（done 或 needs_review）；failed/timeout → job **`failed`**，`exit_code` = 验证退出码（timeout 为 -1），`error = "verify failed: exit N"`；若 job 开了 review（`--review`/项目 `require_review`）→ 进 **`needs_review`** 并在 `Verify` 里标 failed（人来定是拒是收）。验证失败**不是** transient（不触发自动续投/故障转移）；受 E24 重试策略约束时按普通失败处理。
- worker/peer：`wsproto.Dispatch` 增 `verify []string` / `verify_timeout_sec`（协议 v8），worker 本地 `job.Service` 同一实现；`runner.Outcome.Verify` 回传结构化结果。协议 <8 的 worker 不接受带 verify/todo 的 job（dispatch 前拒绝，提示升级 worker），不做静默降级（G032）。
- web：JobDetail 顶部状态条旁加 Verify 块（passed/failed + 命令 + 耗时，点击跳 stderr 尾部）；CLI `job show` 打印 `verify: failed (exit 1, 12.3s)`。
- 安全：verify 命令来自提交者（同 exec 的信任面），受项目 `allow_exec` 约束——项目未开 exec 时 `--verify` 被拒（`ErrInvalidRequest: verify requires allow_exec`），与"内部 job 不开后门"的既有取向一致。

## 三、C：plan todo 自动联动 + 提交列表采集

- `job run --todo <todo-id>`（`JobRequest.TodoID`，HTTP/MCP 同名参数；`plan_id` 缺省从 todo 反查；显式 `--plan` 与 todo 所属 plan 不一致 → 400）。持久化 `jobs.todo_id`。
- 联动（都在 hub 侧 `finish()`/`Submit` 后置钩子，失败只记 warn 不影响 job）：
  - 提交成功 → todo `status = doing`（已 done 的 todo 不回退，只追加 note）。
  - job `done` → todo `status = done`，note **追加一行**：`<job-id> ✓ <n> commits: <sha1> <subject>; <sha2> …`（最多 8 条，超出以 `+N` 收尾）；
  - `needs_review` → note 追加 `<job-id> 待验收`（accept 后再按 done 处理，reject 按 failed 处理）；
  - `failed | timeout | cancelled | rejected` → status 保持 `doing`，note 追加 `<job-id> ✗ <status>: <error 前 120 字>`；
  - 故障转移/自动续投的新 job 继承 `todo_id`，note 会自然串起整条链。
- note 追加实现：`plan_todos.note` 仍是单字段，追加 = 读-改-写（同一 `writeMu`）；`plan set-todo --note` 保持整体覆盖语义不变，新增 `--append-note`。
- **提交列表采集**（复用/扩展 outcomes）：每个 job 开跑前在执行机记录 `base_sha`（`git rev-parse HEAD`，cwd 不在 git 仓则空；worktree job 已有 `WorktreeBaseSHA`，直接复用）；终态时 `git log --oneline --no-decorate base..HEAD`（上限 50）→ `Outcome.Commits []{sha, subject}` / `JobResult.Commits`（`jobs.commits_json`），web JobDetail「提交」块，`job show` 列出。worker 路径经 Outcome 回传。
- `gofer plan show` 每个 todo 下列出挂接的 job（`jobs.todo_id`，状态 + agent + 用时）；`GET /v1/plans/{id}` 的 todo 项加 `jobs[]`。

## 四、D：监督期间不自动布防（h-aii-s2v4）+ 会话 caller（h-aii-esus）

- `agent_sessions` 增 `caller_id TEXT`（additive）：`Register` / `Heartbeat` 从 HTTP ctx 盖认证 caller（空 token 环境为空串）；旧行为空。
- 判定：`sessionrelay.WaitReason()` 在进入两条 **auto** 判据之前，若 `session.auto_relay_skip_when_supervising`（默认 true）且 `store.CountActiveJobsByCaller(caller_id, since = now - supervising_window_sec(默认 7200))`（状态 ∈ queued/running/pending_interaction/recovering/needs_review 不算）`> 0` → 返回空（不布防），`wait_reason_detail = "supervising <n> jobs"` 回给 hook（hook 只打 debug 日志）；`relay_mode = on` 不受影响；`caller_id` 为空的会话不套用（无法判定）。
- 顺带闭合 h-aii-esus：`say / deliver / relay set-mode` 要求 `by == session.caller_id`，或 caller 具备 `can_answer`（governance 开启时）/ 为 user 类 caller 且会话 `caller_id` 为空（老会话兼容）；worker caller 仍 403。
- web Sessions 页在"自动"开关旁显示当前不布防的原因（复用 `wait_reason` 展示位）。

## 五、E：用量 / 成本记录（OBS-08）

- 模型：`JobResult.Usage *Usage {input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, total_tokens, cost_usd, source}` 持久化 `jobs.usage_json`；`source ∈ ndjson:omp | ndjson:claude | codex:stderr | acp:usage_update`。
- 采集点（都是 best-effort、失败留空）：
  - omp ndjson：最后一个 `message_end(role=assistant).message.usage`（`input/output/cacheRead/cacheWrite/totalTokens/cost.total`）——投影器已在读该行，`recordNDJSONCapture` 顺手写回；
  - claude stream-json：`result.usage.{input_tokens,output_tokens,cache_read_input_tokens,cache_creation_input_tokens}` + `total_cost_usd`；
  - codex exec：终态扫 stderr 尾部 8KB，正则 `tokens used\s*\n\s*([\d,]+)` → `total_tokens`（无成本）；
  - acp：runner 累计 `usage_update`（各家字段不一，取 `used/total/cost` 的可识别子集），`runner.Result.Usage` 回填。
- worker/peer：`Outcome.Usage` 回传。
- 暴露：`job show` 一行 `usage: in 12.3k / out 3.8k / cache 289k / $0.0032 (ndjson:omp)`；JobDetail 用量块；`GET /v1/stats` 增 `usage {windows: {"24h": {by_agent: {omp: {jobs, total_tokens, cost_usd}}}, "7d": …}}`（SQL 聚合 `json_extract`，沿用 DBStats 的 200ms 预算与 `partial`）；Home 新增「Agent 用量」卡；`gofer agent status` 附带 24h 用量。

## 六、F：任务书模板（JOB-02 精简版）

- 位置与解析顺序：项目 `<ExecPath>/.gofer/templates/<name>.md` → 全局 `<GOFER_CONFIG_DIR>/templates/<name>.md`。仅 server 本机可读路径（worker-only 项目用全局目录）。
- 格式：YAML frontmatter + 正文。frontmatter 可给 job 默认值（`agent, timeout_sec, tags, verify, review, read_only, worktree, fallback_agents`）与变量声明 `vars: {name: {default, required, desc}}`；正文占位 `{{var}}`；内置 `{{project}}`、`{{cwd}}`、`{{head}}`（server 本机可读项目才解析，否则空并 warn）、`{{date}}`；`{{include: common.md}}` 从同目录拼入（一层，防环）。
- CLI：`job run -t <name> --var k=v … [--prompt '<追加正文>']`；显式旗标 > 模板默认 > 项目默认；`gofer template ls|show <name> [-p project]`。HTTP：`POST /v1/jobs {template, vars, …}`，server 渲染后 `request_json` 存**渲染后的 prompt** 与 `template/vars`（可审计、可 rerun）；MCP `gofer_run_job` 加 `template/vars`。缺必填变量 → 400 列出缺项。
- 不做：远程模板仓库、模板版本管理。

## 七、G：worker 侧审批事件镜像到 hub（h-aii-msm2）

- worker → hub 新帧 `job_event {job_id, type, detail, ts}`（协议 v8，与 B 同次升级），**只转发白名单** `job.permission_requested | job.permission_answered | job.permission_timed_out | job.verify_started | job.verify_finished`；hub `applyJobEvent` 记到 host job 行事件表（detail 加 `origin: worker:<id>`），去重键 `(job_id, type, ts, interaction_id)`。
- hub 侧 notify 由此能命中 `job.permission_requested`（显式订阅时）；`interaction.created` 的既有镜像路径不变，不重复发。
- 旧 worker 不发帧即无镜像（不做补偿）；hub 对未知帧照常忽略。

## 横切

- **兼容策略（G032）**：gofer 仍在 1.0 前。本设计新增的协议字段/列全部可选（additive），但**不为已不部署的组合写容忍分支**：P2 起 hub 只对协议 ≥8 的 worker 下发 verify/todo/job_event，对更低版本直接在 dispatch 前拒绝该 job（`ErrInvalidRequest: worker <id> protocol v<n> < 8, upgrade worker`），不做"静默 skipped"；实施中碰到的既有兼容分支按 G032 处理：仍需要的打 `// DEPRECATED(v0.45): remove in v0.48`，无人使用的直接删（候选：`agent_sessions.relay` bool 镜像列、`server.session_auto_relay_idle_sec` 别名、`interactive_allowed_agents` 一次性读取、`runner: local` 旧别名之外的历史别名、Dispatch 对 <v6/<v7 worker 的 warn-only 容忍）。删除项在汇报里逐条列出。

### DEPRECATED 清单（G032，截至 P5）

本批次触碰到的既有兼容路径全部登记在此：**已打标记、仍在被现役二进制/配置读取**的按 G032 要求带
移除版本；无人使用的直接删除（见下方"已删除"）。标记统一写作 `// DEPRECATED(v<标记版本>): remove in v<+3>`。

| 位置 | 标记版本 | 计划移除 | 说明 |
|---|---|---|---|
| `internal/jobstore/store.go`（`agent_sessions.relay` 列 DDL） | v0.45（P1） | v0.48 | pre-R1 二进制仍读这个 bool 镜像列 |
| `internal/jobstore/sessions.go`（`AgentSession.Relay`、`SetSessionRelayMode` 写入） | v0.45（P1） | v0.48 | 同上，写入端 |
| `internal/httpapi/session_handler.go`（`sessionView.Relay`、`sessionRelayReq.Relay`） | v0.45（P1） | v0.48 | 旧布尔面（旧 console/客户端） |
| `internal/config/model.go`（`server.session_auto_relay_idle_sec` 别名） | v0.45（P1） | v0.48 | 旧配置键 |
| `internal/config/loader.go`（`ApplyLegacySessionRelayCompat`） | v0.45（P1） | v0.48 | 旧键的一次性读取 |
| `internal/config/loader.go`（`ApplyLegacyInteractiveCompat` + `legacyInteractiveYAML`） | v0.45（P3 补打） | v0.48 | `interactive_allowed_agents` 一次性读取（AGT-02 0.3 人工决策保留） |
| `internal/wsproto/frames.go`（`PolicyProject.InteractiveAllowedAgents`） | v0.45（P5 补打） | v0.48 | pre-AGT-02 server 仍会发；worker 只当开关读，不再据此收窄 |
| `internal/commands/worker.go`（`policyAllowsInteractive` 的回落） | v0.45（P5 补打） | v0.48 | 同上，读取端 |

**已删除的兼容路径**：`runner/worker` 对 <v6/<v7 worker 的 warn-only 容忍分支（P2 删除，改为 dispatch 前按能力表拒绝）。

**待人工决策**：上表最后两行只服务"新 worker 连老 server"这一组合。若确认该组合不再部署，可整对删除
（Go 的 json 解码忽略未知字段，删除对 wire 是安全的）——G032 的另一半正是"没人用就直接剔除"。


- **协议**：v8 = Dispatch 增 `verify/verify_timeout_sec/todo_id`，新增 `job_event` 帧；`Outcome` 增 `Verify/Commits/Usage`。
- **schema（全 additive 迁移）**：`jobs` 增 `failure_class, fell_back_from, fell_back_to, requested_agent, verify_json, todo_id, base_sha, commits_json, usage_json`；`agent_sessions` 增 `caller_id`。
- **事件**：`job.fell_back`、`job.agent_substituted`、`job.verify_started|finished`；默认通知集不变（`job.needs_review` 已在）。
- **CLI 新面**：`gofer agent status|probe`、`gofer template ls|show`、`job run --fallback/--no-fallback/--verify/--no-verify/--todo/-t/--var`、`plan set-todo --append-note`。
- **回滚**：全部功能默认关闭或仅在显式传参时生效；schema additive；协议字段可选。

## 实施分期与验收（每期一个 omp job，测试先提交）

| 期 | 内容 | 验收 |
|---|---|---|
| P1 | D（`caller_id` + 监督不布防 + owner 校验）+ C（`--todo` 联动、`base_sha/commits` 采集、`plan show` 挂接、`--append-note`） | 有未终态 job 的 caller 的会话 `wait_reason` 为空；`--todo` job done 后 todo 自动 done 且 note 含提交列表；worker job 的 commits 经 Outcome 回传 |
| P2 | B（verify 本地 + worker + web）+ G（`job_event` 帧）；协议 v8 | verify 失败 → failed / review 下 needs_review；worker 上 verify 输出出现在 hub stderr；worker 的 permission_requested 出现在 hub 事件表 |
| P3 | A（fallback_agents / 失败后转移 / pre_dispatch / failure_class / 健康度 / `gofer agent status|probe` / web） | 假 agent 连挂两次自动改派备用并继承 worktree/todo；健康度 degraded 与恢复；探针 job 计入 |
| P4 | E（四路采集 + `stats.usage` + Home 卡 + `job show`） | omp/claude/codex/acp 各一条 usage 入库；stats 聚合正确 |
| P5 | F（模板解析/渲染/CLI/HTTP/MCP）+ 全部 docs/skill/README | `-t` 渲染与缺变量报错；include 一层；request_json 存渲染后 prompt |

## 决策（待批准）

1. A 的失败后转移**默认开启**（只要配置了 `fallback_agents`），`pre_dispatch` 默认关。
2. verify 失败的 job 状态是 **`failed`**（不是新状态），review 场景才停在 `needs_review`。
3. C 的 note 用**追加**而不是覆盖；`plan set-todo --note` 语义不变。
4. D 默认开启（`auto_relay_skip_when_supervising: true`），只影响 `auto`，不影响显式 `on`。
5. F 只做本地文件模板，不做远程/版本化。

## P1 实测记录（2026-09-18）

D（`caller_id` + 监督不布防 + owner 校验）与 C（`--todo` 联动、`base_sha`/`commits` 采集、`plan show` 挂接）已落地并全绿。落地要点与偏差：

- **D 判定入口**：`sessionrelay.WaitDecision(a) (reason, detail)` 是单次查询的入口（`WaitReason` 保留为其 reason-only 包装）——设计里提的 `WaitReasonDetail(a)` 会二次查库（会话列表逐行渲染 = 每行两次 COUNT），故合并为一个方法；`wait_reason_detail` 落在 `sessionView`（heartbeat/register/list/detail 同一投影）与 turn 长轮询响应上。**OpenTurn 的 HTTP 响应是 decision view**，而监督中的会话在 `OpenTurn` 前就已被 `WaitReason == ""` 挡成 409（不会开出 turn），所以 detail 由 hook 必经的 heartbeat 携带、hook 只打日志。
- **监督窗口的列**：`jobs` 表没有 `created_at`，用的是提交时即写入的 `started_at`（`submit.go` 建行时 = now），语义与设计的 `created_at` 一致。
- **C 的 worker 侧**：`Dispatch`/`Forward` 增 `todo_id` 仅为**显示**；worker 的本地 `Submit` 用 `JobRequest.TodoForeign`（`json:"-"`，客户端不可伪造）跳过 todo 解析与联动——plan todo 归 hub 管理，worker 库里没有它。内部续投（`resume`/auto-resume）在未知 todo 时同样按 foreign 处理（`fromRecord` 无法保留该标记，避免"worker 本地续投被 ErrInvalidRequest 拒"）。
- **C 的 note 行**：失败行在 job 无 error 字符串时只写 `<job-id> ✗ <status>`（无 `: <原因>`），不伪造空原因。
- **C 的开关**：提交采集与 `capture_diff` 无关、永远尝试、失败留空（按设计，未新增开关）。

**G032 处理清单**（P1 触碰到的既有兼容路径）：

| 位置 | 处理 |
|---|---|
| `agent_sessions.relay` bool 镜像列（`store.go` DDL + `sessions.go` `AgentSession.Relay` + `SetSessionRelayMode` 写入） | 仍被 pre-R1 客户端/回滚的二进制读取 → 打 `// DEPRECATED(v0.45): remove in v0.48` |
| `sessionView.Relay` / `sessionRelayReq.Relay`（HTTP 旧布尔面） | 同上，打 `DEPRECATED(v0.45)` |
| `server.session_auto_relay_idle_sec` + `ApplyLegacySessionRelayCompat` warn | 旧配置仍可能带该键 → 打 `DEPRECATED(v0.45): remove in v0.48` |
| 未处理的候选（`interactive_allowed_agents` 一次性读取、旧 worker 协议容忍分支） | P1 未触碰，留 P2/P3 处理 |

删除项：无（P1 没有剔除任何兼容路径——三条候选都仍在被现役二进制/配置使用）。

## P2 实测记录（2026-09-18）

B（`--verify` 本地 + worker + peer + CLI/HTTP/MCP/web）与 G（`job_event` 帧镜像审批/验证事件）已落地并全绿；协议 v8。

- **B 的执行位置判定**：`execute()` 用 `req.Forward == nil` 区分"本机执行"与"远端执行"——本地 job 在本机跑验证步骤，远端 job 的步骤由执行机跑（结果经 `Outcome.Verify` 回来），host 绝不重跑（否则校验的是 host 自己那棵树）。`runner.Request` 另加了 `Verify/VerifyTimeoutSec`（本地用的已解析值），`Forward` 带同一对给远端。
- **B 的准入位置**：`--verify` 需要 `allow_exec` 的检查放在 exec 闸**之前**（更具体的那条先说），且与 exec 闸同域——只在**执行侧**判定（`!remote`）：worker job 由 worker 用它自己的 project 配置放行，与 exec 的既有边界一致（worker-only project 在 host 是占位 project，若在 host 判 allow_exec 会误拒）。
- **B 的 `--verify`/`--no-verify` 互斥**：在 `resolveVerify` 里、`--no-verify` 清空 argv **之前**判定，否则显式命令会被静默丢掉。
- **B 的 timeout 错误文案**：设计只写了 `err="verify failed: exit N"`；超时（exit -1）若照抄会是 `verify failed: exit -1`，读起来误导，故超时用 `verify timeout: <原因>`（状态/退出码仍按设计：`timeout` / `-1`）。失败仍逐字用 `verify failed: exit N`。
- **B 的 review 映射**：`finish()` 的 needsReview 判定扩成 `RequireReview && (status == done || verifyBlocked(pre.Verify))`——verify 失败时 `execute` 给的是 `failed`（带退出码与 error），由 finish 翻成 `needs_review`，事件仍是 `job.needs_review`（无 `job.terminal`）。
- **B 的 skipped**：agent 未正常结束时只记 `job.verify_finished{status:skipped, reason}`（**不**记 `verify_started`、不写横幅）——日志不能声称跑过一次没跑的步骤。
- **B 的 remote 能力协商**：`runner/worker` 在 dispatch 前按字段查能力表（`unsupportedDispatchFields`），**删除**了 <v6/<v7 的 warn-only 容忍分支（见下表）；不满足时 job 立即 failed，error 形如 `worker "w1" protocol v7 lacks verify; upgrade the worker`。注意：错误里**不含** `ErrInvalidRequest` 包装（runner 包不能 import job，且这是派发期拒绝而非提交期 400）；HTTP 层看到的是一个失败的 job，不是 400。
- **B 的 MCP/HTTP**：HTTP 无需改 handler（body 直绑 `JobRequest`），MCP `runJobInput`/`jobView` 各加字段。
- **G 的去重位置**：hub 侧 `workerConn.jobEventSeen`（有界 FIFO，键 `(job_id, type, ts, interaction_id)`，cap 256，**内存 LRU 方案**）。放在 hub 而不是 sink，是因为"重放"是连接级现象（重连后 worker 可能重发），且这样 `wshub` 的测试能直接覆盖它。
- **G 的 worker 侧**：`job.Service.SetEventObserver` + 白名单（审批 3 种 + verify 2 种），`worker.Client` 装观察者并把事件推进**有界队列**（cap 64，满则丢 + 计数 warn，绝不阻塞 job），由 `jobEventLoop` 在 socket 上尽力发出（失败只记 debug，不重放）。事件用**本地 job id** 触发，经新增的反向映射（local→hub）改写为 hub job id。
- **G 的 permission 事件字面量**：搬到 `internal/runner`（`EventPermission*`，与 `EventInputInjected` 同址），acp runner 改用它，`job.EventJobPermission*` 别名——镜像白名单因此与发射点共用一份定义，不会漂移。
- **G 的 adopted job**：`core/adoptSink` + `job.AdoptedJob.OnJobEvent` 同样转发（RECOV-01 R4 收养的 job 仍在 worker 上跑，审批/验证事件必须继续落到 host 行）。

**G032 处理清单**（P2 触碰到的既有兼容路径）：

| 位置 | 处理 |
|---|---|
| `runner/worker` 对 <v6 worker 的 warn-only 分支（session_id/resumed_from/read_only） | **删除**，改为 dispatch 前拒绝（`lacks session_id, read_only`） |
| `runner/worker` 对 <v7 worker 的 warn-only 分支（initial_input） | **删除**，改为 dispatch 前拒绝（`lacks initial_input`） |
| `wsproto.SessionLoadMinProtocolVersion` / `InitialInputMinProtocolVersion` 常量 | 保留（能力表仍需要它们做下限），文档改写为"拒绝而非容忍" |
| P1 遗留候选（`interactive_allowed_agents` 一次性读取） | P2 未触碰（该兼容在 config 加载期且已被 `allow_interactive` 取代，留 P3/P5 评估） |

删除项：上表两处 warn-only 分支（P2 唯一删除的兼容路径，代码与注释一并删除）。

## P3 实测记录（2026-09-18）

A（`fallback_agents` / 失败后转移 / `pre_dispatch` / `failure_class` / 健康度 / `gofer agent status|probe` / web 徽标与探针）已落地并全绿。落地要点与偏差：

- **判定拆分**：`transientHit`（纯模式匹配：stderr 尾部 8KB + `snap.Error`，含 P2 的"verify 失败不算瞬时"闸）与 `autoResumeEligible`（session/次数/可续）从 `autoResumeHit` 拆出，`finish()` 用同一个 `failureDecision` 同时决定 `failure_class`、续投与转移——三者在同一决策点，事件与落库不会互相矛盾；`autoResumeHit` 这个只剩组合作用的包装已删除。
- **续投载体的模式归属**：`resume`/自动续投的载体是内置 exec job（argv里才是真正的 CLI），`transientHit` 因此对"`ResumedFrom != ""` 且自身 agent 无瞬时模式"的 job **沿 `ResumedFrom` 取源 agent 的模式**（`transientPatternsFor`）——否则"同一 agent 续投也挂了"永远判不成瞬时，设计的转移规则会失效。
- **转移的基底请求**：设计写"以源 job 的 `request_json` 构造新请求"。对**续投载体**，其 `request_json` 是 exec argv（agent=exec、prompt 为空），照它重提会在新 agent 上"prompt 为空"提交失败；因此 `fallbackBase` 沿 `ResumedFrom` 上溯（上限 8 跳）取**链根那次**的请求——它才是记着 agent 与 prompt 的那一份——再套设计给的改写（Agent=下一候选、清 SessionID、Cwd=resumeCwd、前缀说明）。这也是验收项"假 agent 连挂两次自动改派备用"能成立的前提。
- **`exec`/`Cmd` 类请求**：按设计不加 prompt 前缀、原样重跑（`execShaped` 在替换 Agent **之前**判定）。候选按加载期校验必为**非 exec** 的批处理 agent，所以"exec 源 + cli 候选"这一组合只可能来自显式 `--fallback`，此时源 argv 原样带过去、prompt 仍为空。
- **`jobs.fallback_json` 的形状**：`{"candidates":[…],"depth":N}`（`FallbackState`），`Depth = 已用过的链接数`，下一个候选是 `Candidates[Depth]`；`Depth >= len(Candidates)` 即链条用尽。设计里 `JobRequest.FallbackDepth int json:"-"` 没有单独设字段——深度就装在同一个 `FallbackState` 里随请求传递，避免两处记同一个事实。字段名 `JobRequest.Fallback` 与 `JobResult.Fallback` 同名不同物（前者内部携带、后者持久化投影）。
- **续投继承转移计划**：`resumeJob`（含 ACP 分支）把源 job 的 `Fallback` 原样传下去（深度不变——载体占的还是同一个 agent 的位置），这样"续投也挂了"才有一份**冻结**的候选表可用，而不必重读可能已变的配置。
- **`requested_agent`**：提交期改派（pre_dispatch）与转移 job 都写它（链根调用方原本要的 agent），普通 job 留空；`dispatcher` 与 `finish` 都从快照继承，客户端无法伪造（`JobRequest.RequestedAgent` 是 `json:"-"`）。
- **pre_dispatch 的插入点**：在 `validate` 之后、`normalizeTimeout`/`selectTargetWorker` 之前——deadline 按**实际要跑的 agent** 判定，标签选 worker 也按改派后的 agent 查能力。改派后链深度置为该候选的下标+1，因此后续失败只剩它之后的候选。
- **健康度口径（澄清）**：`ok` 计 `done` **与 `needs_review`**（活已交付、供应商没出问题；把 needs_review 排除会让开了 `require_review` 的项目在三次供应商错误后**永远** degraded）。窗口内无 job = `unknown`（不是 healthy）；恢复计数按"最近一次瞬时失败**之后**的成功数"算（`jobs` 表按 `ended_at` 比较，在一条 SQL 里用 LEFT JOIN 冻结每个 agent 的最近瞬时失败时间）。数据源是 `jobs` 表 + `idx_jobs_agent_started`，没有新表、没有后台任务。
- **健康度的位置**：`jobstore.AgentHealth/AgentHealthAll`（聚合）+ `agent.HealthState`（纯分类）。A1 的 pre_dispatch 就必须读它，所以聚合与分类随 A1 落地；A2 只做暴露面（HTTP/CLI/web）与对应测试。
- **探针的 runner**：设计没说探针跑在哪个 runner，实现取项目 `allowed_runners` 的第一项（空表 → 内置 `local`）——worker-only 项目因此由**真正会跑它活的那台机**来验；exec agent 的探针跑 `cmd /c echo OK`（Windows）/`echo OK`。
- **探针的 channel**：`POST /v1/agents/{key}/probe` 的 body 只有 `project`/`timeout_sec`，没有 provenance 字段，故探针 job 的 `channel` 为空（`caller_id`/`client` 由服务端照常盖章）。
- **web**：Agents 页每行加 health 徽标（healthy 绿 / degraded 琥珀 `--run`——本主题唯一的橙位，与同行 error 的红区分 / unknown 灰，title 写明窗口内几次供应商错误）+「探针」按钮，结果复用现成的 `InteractionToast`（title/text/to），点它跳 `/jobs/<id>`；探针跑完顺带刷新一次列表，新的健康度立即回显。`vue-tsc --noEmit` 与 `vite build` 均通过（无 web 测试框架，按设计的验收口径到此为止）。
- **worker 路径**：转移的重提与自动续投走**同一形态**（都是普通 `Submit`）——host 侧的转移 job 继承 `Runner/WorkerID` 会重新派发到同一台 worker；worker 本地那份 job 也按它**自己的**配置走它的 `finish`。这一点与既有自动续投完全一致（P3 未改变 worker 侧语义）。

**G032 处理清单**（P3 触碰到的既有兼容路径）：

| 位置 | 处理 |
|---|---|
| `config.ApplyLegacyInteractiveCompat` + `legacyInteractiveYAML`（`interactive_allowed_agents` 一次性读取） | 仍在被现役配置/测试读取（AGT-02 0.3 人工决策保留），本轮**补打** `// DEPRECATED(v0.45): remove in v0.48` 标记（原先只有说明性注释、无标记） |
| `agent_sessions.relay` bool 镜像列、`sessionView.Relay`、`server.session_auto_relay_idle_sec` 别名 | P3 未触碰，P1 已打 DEPRECATED 标记，保持 |
| 旧 worker 协议容忍分支 | P2 已删除，P3 未新增 |

删除项：无（P3 触碰到的兼容路径都仍在被现役二进制/配置使用；`autoResumeHit` 属重构后无用的内部函数，非兼容层，随特性一并删除）。

## P4 实测记录（2026-09-18）

E（四路用量采集 + `jobs.usage_json` + `job show`/web 详情 + `stats.usage` + Home 卡）已落地并全绿。落地要点与偏差：

- **模型只有一份**：`runner.Usage` 是唯一类型定义，`job.Usage` 是它的别名（与 `VerifyResult` 同一手法）。理由与设计一致：acp runner 与 ndjson 投影器都要**产出**它，而 runner 不能 import job（G022）。
- **一个解析器喂四路**：`runner.UsageFromObject(obj, source)` 把「agent 自己的 JSON 对象」读成 `Usage`。ACP 的 payload 形状由各家 agent 自定、omp 与 claude 的 ndjson 拼写也不同，所以每个计数器接受多种拼写（`inputTokens|input_tokens|input`、`cacheReadTokens|cache_read_tokens|cacheReadInputTokens|cache_read_input_tokens|cacheRead|cache_read`、缓存写入同理含 claude 的 `cache_creation_*`），数字接受 number / `json.Number` / 数字字符串；**认不出来返回 nil**（宁可不报，也不编造）。`TotalTokens` 缺省 = 四项之和（claude 的 usage 块没有 total 字段）。
- **各来源实测字段映射**：

| `source` | 采集点 | 字段 |
|---|---|---|
| `ndjson:omp` | 投影器读到**最后一个** `message_end`（`message.role=assistant`） | `message.usage.{inputTokens,outputTokens,cacheReadTokens,cacheWriteTokens,totalTokens}` + `cost.total`（snake_case / `total_cost_usd` 也认） |
| `ndjson:claude` | `result` 行 | `usage.{input_tokens,output_tokens,cache_read_input_tokens,cache_creation_input_tokens}` + **行级** `total_cost_usd`（claude 把成本放在 usage 旁边，故先折叠进一份副本再解析——不改动被投影的事件对象本身） |
| `codex:stderr` | 终态 `captureOutcomes` 扫 `<result_dir>/stderr.log` 尾部 8KB | `(?m)^tokens used\s*\n\s*([\d,]+)\s*$` → `TotalTokens`（剥掉千分位逗号）；无成本 |
| `acp:usage_update` | `handler.SessionUpdate` 的 `case acp.UpdateUsage` | 每个 update 取可识别子集并**合并**进累计值 |
| generic 投影器 | — | 不解析（留 nil）：同一条 `result` 行在 generic 下不产生用量 |

- **`codex:stderr` 的门**：只对 agent key 或 command 基名为 codex 的 job 尝试（`codex.exe` 去掉扩展名后比较）——其它命令打出同样两行是**内容**，把它当 token 数就是编数字。测试同时钉住这条门（同一个假 agent 换个 key 就不产生用量）。
- **ACP 的「累计取最后一次」**：实现为 **overlay 合并**（每次 update 只覆盖它真报了的计数器）而不是整体赋值——agent 常分多次上报（先 token、后成本），整体赋值会把先到的 token 抹掉；update 原文照旧进 `acp.jsonl`（行为不变，只多了一个 case）。
- **远端路径**：执行机的 `job.Service` 在终态采集（本地分支）→ 本地 `JobResult.Usage` → `Outcome.Usage`（wsproto 自己声明 `Usage`，与 `Commit`/`VerifyResult` 同址）→ host `applyOutcome` 落库。协议仍 v8：`Outcome` 的字段是**可选**增量（P2 已把协议升到 v8，设计「横切」正是把 `Outcome` 增 `Verify/Commits/Usage` 归在同一次升级里），不发这些字段的 worker 只是没有用量，host 行保持空。
- **`jobs.usage_json`** additive 迁移；读回空串 = 「没有用量」（不伪造成 0 用量）。`job show` 的 usage 行由 `job.FormatUsage`（internal/job）渲染，web 详情页与 Home 卡各自渲染同一格式（跨语言无法共用，两边注释互相指认规则）。
- **`total` 的求和口径**：agent 自己给了 total 就用它，没给才四项相加——不做替换式归一，避免把 agent 的口径改写成我们的口径。
- **`/v1/stats` 的用量块**：`jobstore.UsageStats(now, windows []time.Duration, budget)`（签名照设计）每个窗口一条 `GROUP BY agent` 聚合；`json_valid` 守卫包裹 `json_extract`（SQLite 的 json 函数遇到非 JSON 会**报错**，一行写坏不能让整张卡变空）；预算耗尽 → 未算的窗口**不出现在 map 里** + `partial=true`（web/CLI 都按「没算」显示 `-`，绝不显示 0）。窗口标签由 `windowLabel` 从 duration 渲染（≥48h 且整天 → `Nd`，否则 `Nh`），正好给出 `24h` / `7d`。
- **`job show` 的对齐**：沿用既有 12 列（`usage:` + 6 空格），行内容与设计给的示例逐字一致（`in 12.3k / out 3.8k / cache 289k / total 305k / $0.0032 (ndjson:omp)`）；token 显示 = 3 位有效数字 + k/M（`job.FormatTokens`，CLI 侧唯一定义，`agent status` 的 24h 列共用），成本 4 位小数。
- **`agent status` 的两列取数**：设计只说「附带 24h 用量」，实现取 `/v1/stats` 的 `usage.windows["24h"]`（client 新增 `GetUsageStats`，只解 usage 块：stats 的其他块变化不影响 CLI）。窗口内没有该 agent 的结算 → 两列都是 `-`。
- **web**：Home 新增「Agent 用量」卡（24h/7d 小按钮切换、按 total_tokens 降序、复用 DB 卡的行样式），JobDetail 新增「用量」块；`vue-tsc --noEmit` 通过（无 web 测试框架，按设计的验收口径到此为止）。
- **测试夹具**：`internal/worker` 的 e2e worker 侧配置新增一个 codex 键的假 agent（真的往 stderr 打 `tokens used\n19,802`），host 侧 project 的 `allowed_agents` 相应放开——这是 `TestOutcomeCarriesUsage` 能跑通整条 worker 路径的前提，其余 e2e 不受影响（新增项对未提及 codex 的 job 为惰性）。

**G032 处理清单**（P4 触碰到的既有兼容路径）：

| 位置 | 处理 |
|---|---|
| （无）P4 未触碰到任何既有兼容分支：新增的列/wire 字段/类型都是 additive，未新增无标记兼容层 | — |

删除项：无。

## P5 实测记录（2026-09-18）

F（模板解析/渲染 + `-t/--var` + CLI/HTTP/MCP/web + 仓库示例模板）与全批次 docs/skill/README 收口已落地并全绿。落地要点与偏差：

- **`Render` 的返回值**：设计写的是 `(prompt, missing, err)`，实现是 `Rendered{Prompt, Missing, Warnings}`。"缺必填变量"（提交必须 400）与"未声明的占位符原样保留 + warn"（必须放行）是两回事，一个列表装不下：前者让 `Submit` 拒绝并列出缺项，后者只记一条 `job.template_render` warn 并回显给 `template show` 的读者。
- **调用方给过值的占位符即使模板没声明也替换**：设计只说"未声明的 `{{x}}` 原样保留"。实现把"提交者本轮传进来的 `vars`"算作已解析——只有**没人给值**的占位符才原样保留 + warn（占位符写成 `{{nope}}` 而没人给值，正是"打错了"的样子）。
- **`List` 用 `Info.Err` 报出解析失败的文件**（设计只给了 Name/Source/Path/Desc/Vars）：一份存在但坏掉的模板正是 submit 失败时读者要找的那一行，静默隐藏它比多一个字段更糟（`template ls` 与 web 下拉都把它显示出来并禁用）。
- **模板的 `runner` 默认值**：CLI 的 `--runner` 默认值（`server` ⇒ 内置 local）充当"没有钉 runner"的哨兵——带 `-t` 时不再下发该默认值，把它留给模板；模板也没写时由服务端回落到内置 `local`（**非模板请求仍然必须显式给 runner**，validate 未改）。gcli 分不清"没给 `--runner`"与"写了 `--runner server`"，故"显式 `--runner server` + 模板另写 runner"这一种组合以模板为准（已在 flag 说明里注明，这是唯一的差别）。
- **模板默认只填零值**：bool 只能"打开"、不能"关闭"（false 与未设无从区分）；`--no-verify` 是显式 opt-out，压过模板的 verify 默认值（不让两者同时落到请求上——那样 validate 会直接拒）。role 预设仍在模板**之前**解析（`--role` 是显式旗标），所以 role 填过的字段模板不再覆盖。
- **预览在服务端渲染（对设计的一处 additive 扩展）**：`GET /v1/projects/{key}/templates/{name}` 除模板本身外返回 `render{prompt,missing,warnings}`，变量用重复的 `?var=k=v` 传入。设计把这个端点定为"只读"，但**只有服务端能展开 `{{include: …}}`、也只有它知道 job 将在哪个 checkout 里解析 `{{head}}`**——客户端自渲染会与提交时真正发出去的正文不一致（仓库示例 `impl-batch.md` 就用了 include）。纯读、无副作用、不落库，故按 read-only 预览实现，CLI `template show` 与 web 提交表单共用同一个入口。
- **frontmatter 白名单**：除设计列的 job 默认键外，另允许 `desc`（`List`/`show` 要用）与 `vars`；其余键（`plan_id`/`caller_id`/`cmd`…）一律解析报错（goccy `yaml.Strict()`）。正文没有 frontmatter 时整篇都是正文（include 片段就是这样）。
- **frontmatter 切分器统一**：`SplitFrontmatter` 落在 `internal/template`；`httpapi.parseMarkdownRequest` 与 `job/workflow.parse` 的私有副本一并删除（函数体逐字搬迁，G023）——同一份"任务文档"不该有三种切法。
- **重放不再渲染**：`request_json` 里既有渲染后的 prompt、也留着 `template/vars`（设计要求"可审计、可 rerun"）。因此**重放路径必须丢掉模板字段**，否则会把任务书再拼一遍到 prompt 尾部：`RebuildJob` 与 `fallBack` 各调一次 `dropTemplate`（`resume` 本来就新建请求、不带模板）。设计只说"rerun 不再重渲染"，这条是它的落地前提，也是 `TestRebuildTemplateJobDoesNotReRender` 钉住的行为。
- **`{{head}}`**：仅当项目目录（`ExecPath`，server 本机可读）是 git 仓时解析（`git rev-parse --short HEAD`，5s 上限 + `GIT_OPTIONAL_LOCKS=0`）；解析不出来就渲染空 + warn，不编造 sha。
- **项目目录本机不可读**（worker-only 项目在 host 是占位）时只查全局目录——这正是全局目录存在的理由（设计 §六）。
- **CLI 用法边界**：`-t` 与 `-f`、`-t` 与 post-`--` argv 互斥（三者都在定义"这个 job 是什么"）；`--var` 缺 `-t` 是用法错误（不静默丢弃）；`-t --prompt '…'` 合法 = 追加正文；带 `-t` 时 `--agent` 不再必填（服务端仍会拒"模板与旗标都没给 agent"）。
- **MCP**：`gofer_run_job` 只加 `template`/`vars` 两个入参（schema 里 `agent` 仍必填——SDK 会按 schema 校验入参，"模板填 agent"这条只在 CLI/HTTP 上成立）；新增只读工具 `gofer_list_templates {project}`（项目作用域下省略 project 即取作用域项目）。
- **web**：提交表单新增模板下拉（坏模板列出但禁用并显示其错误）+ 按 `vars` 声明生成的变量输入 + 正文预览（服务端渲染的同一份正文），选中模板后 prompt 文本域变成"追加正文"。`vue-tsc --noEmit` 与 `vite build` 均通过（无 web 测试框架，按既有验收口径到此为止）。
- **仓库示例模板**：`docs/examples/templates/{common.md,impl-batch.md}` 按**全局目录的布局**摆放（`templates/<name>.md`），`gofer init` 不自动安装——文档里给一条 `cp` 即可。实测：`Resolve→Render` 渲染两份示例，include 展开一层、零告警、变量默认值生效。
- **验收对照**：`-t` 渲染与缺变量报错（`TestSubmitWithTemplateRendersPromptAndDefaults` / `TestSubmitTemplateMissingVarRejected` / `TestTemplateShowRenders`）；include 一层（`TestRenderIncludeOneLevelSameDirOnly`）；request_json 存渲染后 prompt（上面两条 job 测试 + `TestSubmitJobWithTemplate` / `TestRunJobTemplateParams`）；项目目录优先于全局（`TestResolveProjectBeforeGlobal`）；白名单（`TestParseFrontmatterWhitelist`）。

**G032 处理清单**（P5 触碰到的既有兼容路径）：

| 位置 | 处理 |
|---|---|
| `internal/wsproto/frames.go` `PolicyProject.InteractiveAllowedAgents` | 原先只有说明性注释 → P5 **补打** `// DEPRECATED(v0.45): remove in v0.48` |
| `internal/commands/worker.go` `policyAllowsInteractive` 的 pre-AGT-02 回落 | 同上**补打**标记（老 server 不发 `allow_interactive` 时的唯一读法） |

删除项：无（这两条仍在被现役组合读取，登记在上方「DEPRECATED 清单」；其余 P1–P4 的标记保持原样）。
