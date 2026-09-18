<!-- template_id: design; template_version: 1.1.1 -->
# 监督闭环与 agent 可靠性设计（SUP-01：故障转移 / 验证步骤 / todo 联动 / 用量 / 模板 / 事件镜像）

> 状态：Approved 0.2 / 实施中（2026-09-18 人工批准；决策 1–5 照初稿）

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
