<!-- template_id: design; template_version: 1.2.0 -->
# 第 2 批：定时与巡检少打扰 设计

> 状态：Draft 0.1（待用户答复「待确认事项」Q1–Q8）
> issue：gofer-ss3b、gofer-vqoe、gofer-2a86、gofer-4ymk；随批：gofer-qm3l、gofer-fgwc（小改），gofer-oby3、gofer-j01b（第 1 批遗留的掩码问题）
> 来源：`docs/refer/2026-11-11-super-agent-workflow-survey.md`「分批」第 2 批（⑮⑯⑰⑱㉑）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-12 | Claude（gofer 监督会话） | 初稿 |

思考模式：RIGOROUS。核心目标：①定时任务不空跑、不叠跑、坏了自己停；②每轮知道自己是第几轮、上一轮结果如何；③`serve status` 能被脚本判断；④补两处 secret 外泄。Scope freeze：下文「范围」。Review budget：两轴各一轮，有 `CORE_BLOCKING` 才继续，封顶两轮。

## 背景与目标

1. **叠跑、空跑、坏了不停（gofer-ss3b、gofer-vqoe）**：`sweepSchedules`（`internal/serve/serve.go:1043`）到点即提交，不看上一轮 job 是否还在跑；没有运行次数与失败计数，连续失败的定时任务会一直派下去；「CI 红了才修」「有新 issue 才处理」这类巡检多数轮次是空跑，消耗 token。
2. **每轮从零开始（gofer-2a86）**：schedule 参数只以环境变量进 job（`internal/job/schedule_params.go:13`），任务书里拿不到轮次、schedule 身份与上一轮结果；jobs 表也没有记录来源 schedule。
3. **`serve status` 恒退出 0（gofer-4ymk）**：`runServeManagedStatus`（`internal/commands/serve_management.go:404`）只在读配置出错时失败，脚本与监控无法据退出码判断。
4. **同 worker_id 两进程互顶无提示（gofer-qm3l）**：`hub.go:625` 以 `replaced by new registration` 关闭旧连接，server 无专门日志与事件，两进程会无限互顶。
5. **secret 外泄两处**：`stdout.raw.log` 在掩码之前分流、原文落盘（gofer-oby3）；ACP 审批卡片的工具输入摘要完全未掩码（gofer-j01b，比 issue 描述的「先截断后掩码」更重）。

## 范围与非目标

范围：§1–§7。

非目标：schedule 的滚动笔记（Q6 推荐延后）；server 级健康项与「连续 N 次失败才报」（Q7 推荐并入第 3 批的可追溯与运行健康）；MCP schedule 工具（现无，本批不加）；远程 runner 上执行前置检查（Q3）；schedule 事件进 IM / 桌面以外的新通道。

## 已确认事实（代码，main 553c5537）

- schedules 表：`internal/jobstore/store.go:346-368`，迁移 `migrateSchedules`（`:2076+`）；结构 `ScheduleRecord`（`internal/jobstore/schedules.go:15-41`），`RequestJSON` 是序列化的 `job.JobRequest`。无运行计数、无失败计数、无暂停原因；暂停只有 `enabled`。
- 触发：`startScheduleLoop`（`serve.go:1187`）每 `sweep_interval_sec`（默认 30s）扫一次；`sweepSchedules`（`:1043-1095`）先 CAS `AdvanceSchedule` 再 `submitDueSchedule`（`:1112-1141`，`Channel="cron"`、`OriginCaller=CreatorCaller`、`NotifyDegradable=true`），成功后 `SetScheduleLastJob`。
- 事件：scope `schedule:<id>`（`internal/job/model.go:1516-1553`）有 `schedule.run_failed`（只在准入拒绝 / webhook 失败时写，不在默认通知集）、`schedule.run_now`；其他提交失败只进 server 日志。
- 模板：带 `Template` 的请求每次提交重新渲染（`internal/job/submit.go:143`），内置变量 `{{project}} {{cwd}} {{date}} {{head}}`（`internal/template/template.go:402-415`）。
- 权限：创建需 `job.submit`；删除 / 停用 / 轮换需本人或 `schedule.manage.others`；启用 / 立即运行 / PATCH 另需 `job.submit`（`internal/httpapi/authz_routes.go:503-516`）。
- jobs 表没有来源 schedule 的字段（只有 `Channel="cron"`）。
- `serve status`：检查注册、pid、端口、`GET /health`（静态 `{ok:true}`）、`/v1/stats`；结果一行文本或 `--json`；退出码恒 0。
- worker 互顶：`Put` 只在 `instance_id` 相同时把旧连接标为被取代并交接 job；不同时旧连接拆除会让其在途 job 失败（`hub.go:957-987`）。
- fgwc：开跑前复查失败时 `execute.go:204-208` 直接 `finish(failed)`，**不**跑 `captureOutcomes` 等 git 读取；仍会探测残留目录的是 `WorktreeStatus`（`internal/job/worktree.go:820-850`）与保留期清扫 `retireWorktree`（`internal/job/prune.go:22-36`），二者只看 `isDir`。
- `stdout.raw.log`：`ndjson_raw: true` 时由 `internal/job/ndjson.go:93-101` 打开，在 `newSecretMasker`（`execute.go:352/359`）之外分流，原文落盘；gofer 自身不读它，经 `result_dir` 在文件系统可见。
- 审批卡片：`approvalRequestToolCall`（`internal/runner/acp/runner.go:1322`）`truncate(RawInput)` 到 512 字节，无掩码；经 interaction、worker / peer 中继进 CLI 与今天卡片。

## 总体方案

### 1. 定时任务运行健康（gofer-ss3b）

新增 schedule 字段（additive 迁移，G057）：

| 字段 | 含义 | 默认 |
|---|---|---|
| `overlap` | `skip`：上一轮 job 未结束则本轮跳过；`allow`：照常提交 | `skip`（Q1） |
| `max_runs` | 成功提交满 N 次后自动停用，0 = 不限 | 0 |
| `run_count` | 已提交次数（含手动运行） | — |
| `max_failures` | 连续失败满 N 次自动暂停，0 = 不自动暂停 | 3（Q2） |
| `consecutive_failures` | 当前连续失败数，一轮 `done` 清零 | — |
| `paused_reason` | `failures` / `max_runs` / 空（人工停用） | — |

- jobs 表新增 `schedule_id`（additive）。定时触发、立即运行、webhook 触发的 job 都写入；以此判断上一轮状态、统计结果。
- 叠跑判定：读 `last_job_id` 的 job 行，非终态即视为未结束；跳过时照常推进 `next_run_at`，写 `schedule.run_skipped{reason: previous_running, job_id}`。
- 失败计数：schedule job 进终态时（job 层终态钩子，按 `schedule_id`）`failed` / `timeout` 加一，`done` 清零，`cancelled` 不计；提交失败（准入拒绝、请求解码失败）也加一并写 `schedule.run_failed`（现状只进日志的那几类一并写事件）。
- 达到 `max_failures`：`enabled=0`、`paused_reason=failures`，写 `schedule.paused{reason, failures, last_job_id}`；`schedule.paused` 进默认通知集与「等我」的 `failed` 类（创建者本人可见；卡片动作：启用 / 打开）。达到 `max_runs`：`enabled=0`、`paused_reason=max_runs`，写同一事件，不进「等我」。
- 人工启用：清零 `consecutive_failures` 与 `paused_reason`；`run_count` 不清（要重来就改 `max_runs`）。
- 入口：CLI `schedule add/edit` 加 `--overlap --max-runs --max-failures`；HTTP 创建 / PATCH 同名字段；`schedule list/show` 与 Web 列表显示「第 n 次 / 上限」「连续失败 n」「已暂停：原因」；Web 编辑页加三个输入。

### 2. 前置检查（gofer-vqoe，含 ⑮⑱「无变化不出声」）

schedule 新增 `precheck`：

```yaml
precheck:
  argv: ["gh", "run", "list", "--json", "conclusion", "-L", "1"]  # 不经 shell
  timeout_sec: 60          # 上限 300
  skip_unchanged: false    # true：输出指纹与上次相同则跳过（Q4）
```

- 执行位置：server 本机，cwd 为项目 `ExecPath`，环境按 job 的清洗规则（G042 `util.Environ`），stdout / stderr 各截最后 4KB。runner 不是本机的 schedule 不能设置（400）（Q3）。
- 结果：exit 0 → 提交；输出末尾 4KB 经掩码后作为环境变量 `GOFER_PRECHECK_OUTPUT` 与模板变量 `{{precheck_output}}`。exit 2 → 跳过，写 `schedule.run_skipped{reason: precheck}`，不计失败。`skip_unchanged` 下 exit 0 但 stdout 的 sha256 与上次相同 → 跳过（`reason: unchanged`），指纹存 schedule 行 `precheck_fingerprint`。其他退出码 / 超时 → `schedule.run_failed{reason: precheck}`，计入连续失败。
- 叠跑判定先于前置检查（上一轮在跑就不跑检查）。
- 权限：设置或修改 `precheck` 需新权限 `schedule.precheck`（在 server 机器上以 server 身份执行命令，风险高；不可授予会话，job 凭据与 agent 不可用），G056 登记并重生成 `docs/reference/permissions.md`（Q5）。
- 日志不写 argv 以外的输出正文；事件只记退出码、耗时、是否截断。

### 3. 跨轮上下文（gofer-2a86）

- 每轮提交时注入：环境变量 `GOFER_SCHEDULE_ID`、`GOFER_SCHEDULE_RUN`（本轮序号 = `run_count`）、`GOFER_PREV_JOB_ID`、`GOFER_PREV_STATUS`；模板变量同名小写（`{{schedule_run}}` 等）。
- agent job（非 exec）在 prompt 前加一段固定的「定时任务上下文」（i18n，按 job 语言）：第几轮、上一轮 job id / 状态 / 结束时间 / 提交数，以及上一轮最后一条回复的末尾（取自上一轮的 `turns.jsonl` 或 stdout 尾部，限 2KB，掩码后）。事实全部取自 jobstore 与结果目录，不调模型。schedule 字段 `context: on|off`，默认 `on`（Q6）。
- 滚动笔记（Proma 的 notes）本批不做（Q6）。

### 4. `serve status` 退出码分级（gofer-4ymk）

| 退出码 | 含义 | 判定 |
|---|---|---|
| 0 | 正常 | 注册、进程、端口、`/health` 均正常且版本一致 |
| 1 | 检查本身出错 | 现状：读 spec / 配置失败 |
| 3 | 未运行 | 已注册但进程不在 / 端口无人监听 |
| 4 | 部件故障 | 进程在但 `/health` 不通、端口被别的进程占、`/v1/stats` 鉴权不符、运行版本与登记版本不一致 |
| 5 | 待配置 | 未注册受管 server（没有 spec） |

- 取值参照 LSB status（0 运行、3 未运行），4 / 5 为 gofer 自定义（Q7）。`--json` 增加 `state` 与 `exit_code` 字段；文本输出末行加 `state=`。
- server 级健康项与「连续失败才报」延后（Q7）。

### 5. 同 worker_id 两进程互顶（gofer-qm3l）

- server：`Put` 取代旧连接且两者 `instance_id` 不同时，写 server 日志 `worker.replaced`（worker_id、新旧 instance_id、新旧远端地址），并写 worker 事件 `worker.replaced`（同一 worker 10 分钟内只写一条，附计数）；`worker ls` / Web Workers 页显示「24 小时内被顶替 n 次」。
- worker：收到关闭原因 `replaced by new registration` 时打印一条明确的错误（「同一 worker_id 有另一个进程在连接」，附自己的 instance_id）；5 分钟内被顶替 3 次后退避 5 分钟再连，不退出（Q8）。

### 6. 残留目录的探测（gofer-fgwc）

`WorktreeStatus` 与 `retireWorktree` 在读取前先用 `worktreeUsable`（同一判定，`continuation_guard.go:76`）确认目录仍是该分支的 git checkout；不是则按「worktree 已不在」处理（状态 `gone`，清扫只摘登记不跑 git）。开跑前复查本身仍需在路径里跑两条只读 git，保留。

### 7. secret 掩码补漏（gofer-oby3、gofer-j01b）

- `stdout.raw.log`：分流写入器套同一个 `secretMasker`（共用 job 的授予值），与 stdout 一致；文件权限改 0600。
- 审批卡片：`approvalRequestToolCall` 先用 `req.Mask` 掩码 `RawInput`，再截断（与 acp.jsonl 的修法一致）；Title 同样掩码。worker / peer 中继不需改（掩码在源头）。

## 架构

- 分层（G021/G022）：schedule 运行健康与上下文放 `internal/job`（新文件 `schedule_health.go`、`schedule_context.go`），`serve` 只在 sweep 中调用；前置检查的执行放 `internal/job`（复用 exec 的环境清洗），`serve` 不直接起进程。
- 数据：schedules 加 8 列、jobs 加 `schedule_id`，全部 additive；`TestOpenUpgradesPreviousReleaseSchema` 必须通过（G057）。
- 接口：HTTP schedule 创建 / PATCH / 读取加字段；新事件 `schedule.run_skipped`、`schedule.paused`、`worker.replaced` 登记到事件目录、通知目录与前端事件标签。
- 权限：新增 `schedule.precheck`（G056）。

## 关键流程

到点 → CAS 推进 `next_run_at` → 上一轮未结束且 `overlap=skip` → 跳过事件 → 结束；否则前置检查（有则跑）→ exit 2 / 无变化 → 跳过事件；失败 → 计失败（可能暂停）；exit 0 → 提交（注入上下文、`schedule_id`、`run_count+1`）→ 达 `max_runs` 则停用。schedule job 终态 → 更新连续失败，满阈值暂停并发事件。

## 安全、数据、运维与回滚

- 前置检查是 server 身份下的命令执行：只经专门权限、argv 不经 shell、超时上限、输出限长并掩码。
- 默认值变化：`overlap` 默认 `skip`、`max_failures` 默认 3 会改变现有 schedule 的行为（Q1 / Q2），CHANGELOG 列入「行为变化」。
- 回滚：新列不被旧版本读取，回退二进制即恢复旧行为。

## 决策

见「待确认事项」，答复后转入此处。

## 待确认事项

| 编号 | 问题 | 选项 | 推荐 |
|---|---|---|---|
| Q1 | 上一轮未结束时的默认行为 | A：默认 `skip`，可设 `allow`；B：默认 `allow`（现状），需要时设 `skip` | **A**。叠跑几乎都不是想要的 |
| Q2 | 连续失败自动暂停的默认值 | A：默认 3，0 关闭；B：默认 0（不暂停），需要时设 | **A**。坏掉的定时任务没人看会一直派 |
| Q3 | 前置检查在哪执行 | A：只在 server 本机，非本机 runner 的 schedule 不能设；B：同时支持远程 worker（要加 worker 协议） | **A**。B 需改协议，等有需要再做 |
| Q4 | 「无变化不出声」（⑮⑱） | A：`skip_unchanged` 开关，按 stdout 指纹去重；B：本批不做 | **A**。成本小，正好覆盖巡检 |
| Q5 | 谁能设置前置检查 | A：新权限 `schedule.precheck`，不可授予会话；B：沿用 `job.submit` | **A**。它在 server 机器上跑命令 |
| Q6 | 跨轮上下文的范围 | A：注入轮次与上一轮结果（prompt 前固定段 + 变量），默认开，笔记延后；B：同 A 并加滚动笔记文件；C：只给变量，不改 prompt | **A** |
| Q7 | `serve status` 范围 | A：只做退出码分级（上表），server 健康项与连续失败才报并入第 3 批；B：本批一起做健康项 | **A** |
| Q8 | worker 被反复顶替时 | A：明确报错 + 退避 5 分钟后再连；B：报错后退出进程 | **A**。退出会让服务管理器反复拉起，效果相同但更吵 |

## 验收

- §1 单测：上一轮在跑时跳过并记事件（`skip` / `allow` 两种）；连续失败计数（failed / timeout 加一、done 清零、cancelled 不计、提交失败计入）；满阈值暂停与事件；`max_runs` 停用；人工启用清零；jobs `schedule_id` 写入（定时 / 立即运行 / webhook）；旧库升级（G057）。
- §2 单测：exit 0 / 2 / 其他 / 超时四种结局；输出变量注入且掩码；`skip_unchanged` 指纹去重；非本机 runner 设置被拒 400；无 `schedule.precheck` 权限设置被拒 403；叠跑判定先于检查。
- §3 单测：变量与 prompt 段注入（中 / 英）、`context: off` 不注入、上一轮缺失（首轮）时的文案、回复尾部限长与掩码。
- §4 单测：五种退出码各一例；`--json` 字段。
- §5 单测：不同 `instance_id` 取代时写日志与事件（限频）；相同 `instance_id`（重连）不写；worker 侧退避。
- §6 / §7 单测：残留非 git 目录时 `WorktreeStatus` 为 gone 且不跑 git；`stdout.raw.log` 与审批卡片里授予的 secret 只出现掩码（secret 跨 512 字节截断点的用例）。
- 真实验收（主机临时 server）：一个每分钟的 schedule，job 睡 90 秒 → 第二轮被跳过；前置检查 exit 2 → 跳过、exit 0 → 派发且任务书拿到输出；一个必失败的 schedule 第 3 次后自动暂停、今天页出卡片；第二轮 job 的 prompt 里有上一轮结果段；停掉 server 后 `serve status` 退出 3。
- 反向验证：去掉叠跑判定 → 跳过用例失败；去掉掩码 → secret 用例失败；去掉清零 → 计数用例失败。
- 文档（G045 / G046）：`skills/gofer-usage/references` 中 schedule 相关篇、worker 篇、`serve` 运维篇；`docs/reference/http-api.md`（schedule 字段、新事件）；`docs/reference/permissions.md`（生成）；`CHANGELOG.md`（含行为变化）。

## 结论与人工计划 Gate

定时任务加叠跑跳过、失败自动暂停、运行上限、前置检查与无变化跳过，每轮带上一轮结果；`serve status` 按状态返回退出码；补 worker 互顶提示、残留目录探测与两处 secret 掩码。等用户答复 Q1–Q8 并批准后写计划。
