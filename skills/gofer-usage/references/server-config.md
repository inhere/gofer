# config.yaml（server）配置参考

> 配主机 server：project 目录映射、准入白名单、runner 名册、worker 绑定、agent 定义。全部 `<占位符>`，按实际替换。
> 脚手架：`gofer init server`（`-g` 写全局 `~/.config/gofer/config.yaml`）。校验：`gofer config validate server`。改完用 `gofer serve reload -c <config>`（或 Unix SIGHUP、web 设置页「重新读取文件」）生效。

## 目录

- [结构总览](#结构总览)
- [projects（配得最多）](#projects配得最多)
- [worker = 两段配置](#worker--两段配置)
- [runners（名册）](#runners名册)
- [worker_id 三处对齐](#worker_id-三处对齐)
- [agents（agent 定义）](#agentsagent-定义)
- [server / session / log / storage（常用项）](#server--session--log--storage常用项)
- [待批 job 的审批权限](#待批-job-的审批权限)
- [故障转移与健康度](#故障转移与健康度)
- [任务书模板目录](#任务书模板目录)
- [同目录串行锁与输出停滞](#同目录串行锁与输出停滞)
- [校验与生效](#校验与生效)

## 结构总览

```yaml
server:   {...}   # 监听地址 / 鉴权 / callers / workers 绑定 / 限流 / webhook
session:  {...}   # 终端会话中继的判据与时限
storage:  {...}   # 结果落盘 / 保留
projects: {...}   # ★ 每个 project 的目录 + 准入(agent/runner) —— 配得最多
agents:   {...}   # agent 定义(cli-agent 命令 + detect 探测)
runners:  {...}   # runner 名册: local / peer-http / worker
work: {...}  steward: {...}   # 工作项与管家（见 work-items.md）
```

## projects（配得最多）

> 不写 `default:` 时 server 自动注入内置 `default` 项目（指向默认工作空间 `~/.gofer/workspace` / `GOFER_WORKSPACE`，`allowed_runners: [local]`，`allowed_agents` = 本机可用 agent），列表标「内置」、不落盘、不能删；要自定义就显式声明 `default:`（整条覆盖）；在 web 里编辑并保存会把它写进 config 成为声明项目。

```yaml
projects:
  app-a:
    host_path: D:/projects/app-a              # 逻辑路径(server 视角; 也是下发给 worker 的 host_path)
    container_path: /work/projects/app-a      # 容器执行视角(server path_view=container 时用)
    default_agent: codex
    allowed_agents: [codex, claude, exec]     # 准入白名单(空=放行全部)
    allowed_runners: [local, builder]         # ★ 决定派给谁(见下)
    allow_exec: true
    allow_interactive: true                   # pty/交互 job 的项目开关(默认 false)
    max_concurrent_jobs: 4                    # 该 project 并发上限(0/不写=无限)
    # dir_lock_mode: repo                     # cwd 下有嵌套仓库时，可写 job 必须 --lock 或显式 shared/exclusive
    # agent_fallbacks: {codex: [omp, claude]} # 该项目的故障转移候选：按「挂掉的 agent」给有序候选，整份覆盖 agent 级 fallback_agents；空列表 = 不转移
    # max_timeout_sec: 7200                   # 该项目 job 超时上限(秒), 覆盖 server.max_job_timeout_sec
    # budget: { max_tokens: 1000000, max_cost_usd: 10 }  # 该项目 job 默认花费上限：盖过 agent.budget、被请求盖过；只对能上报用量的 agent 生效
    # worktree_default: true                  # 该项目 job 默认在受管 git worktree 里跑
    # capture_diff: auto                      # auto/on/off；auto 跳过普通 exec，cli-agent 或要验收的 job 采集
    # scope_discipline: auto                  # auto/on/off；agent job prompt 末尾追加「## 交付约定」
    # knowledge_capture: auto                 # auto/on/off；要求汇报写「## 可复用经验」，交付时记为经验候选
    # require_review: true                    # job 正常完成后停在 needs_review 等人验收
    # verify: [make, test]                    # 默认验证步骤：agent 正常结束后在同一 cwd/env 跑, 非 0 → failed；需要 allow_exec
    # verify_timeout_sec: 900                 # 验证步骤超时(不写=600s)
    # job_env_allow: [MY_TOKEN_FOR_JOB]       # 放行被默认剔除的环境变量
  # 瘦写法: 只写路径 + allowed_agents, 其余走默认
  docs-site:
    host_path: D:/projects/docs-site
    container_path: /work/projects/docs-site
    allowed_agents: [exec]
```

🔴 **`allowed_runners` 决定这个 project 派给谁**（列的是 runner 的**名字**，见「runners」）：
- 含 `local`（或同义 `server`）→ 可在 **server 本机**跑。
- 含某 **worker-runner 名**（如 `builder`）→ 可派给那台 worker，**且 server 会把这个 project 下发进那台 worker 的 POLICY**。
- **空 `allowed_runners` = 不推给任何 worker、也不在本机跑**（不是通配）。

项目目录里还可放 `.gofer.project.yaml` 瘦配置（只放偏好，准入字段留在全局 config）。

## worker = 两段配置

要把 project 派到某 worker 执行，两边都要配、且 **project key 两边一致**：

| | server 侧（本文件） | worker 侧（worker.yaml） |
|---|---|---|
| 准入 | project 的 `allowed_runners` 含该 **worker-runner 名** | （POLICY 下无需） |
| 执行 | — | LEGACY：同名 project + `allowed_runners: [local]`；POLICY：`roots` 覆盖其 `host_path` |
| project key | 一致 | 一致（对不上 worker 以「未知项目」拒） |

- **POLICY worker**：server 侧 project 的 `allowed_runners` 含它的 runner、且 worker 的 `roots` 覆盖 `host_path`，就自动下发，**worker.yaml 无需再定义该 project**。
- **LEGACY worker**：还得在 worker.yaml 里同名再定义一遍。

## runners（名册）

```yaml
runners:
  local: { type: local }                       # 内置, 声明可选
  builder:                                      # 派发给 worker
    type: worker
    worker_id: builder-1                        # = server.workers 的 KEY = worker 端 worker_id
  docker-peer:                                  # 反向调用容器内的 peer bridge
    type: peer-http
    base_url: http://127.0.0.1:8766
    token_env: CONTAINER_BRIDGE_TOKEN
```

- 声明 `type: worker` 的 runner 才会：① 让该 worker 出现在 web Runners 名册；② 可被 `--runner <名>` 派发。重载后 type=worker runner 的增删即时生效；peer-http 等启动级 runner 的修改要重启。
- project 要用它，还得在该 project 的 `allowed_runners` 里加上这个 runner 名。
- `server` / `local` 是保留名：唯一合法的声明是 `local: {type: local}`（或 `server: {type: local}`），自定义 runner 与 worker id 不能叫这两个名字。

## worker_id 三处对齐

一台 worker 连上并可派发，**同一个 `worker_id`** 三处一致（缺一不可）：

1. `server.workers` 的 **KEY**（+ 绑定 token）：
   ```yaml
   server:
     workers:
       builder-1:
         token_env: GOFER_WORKER_BUILDER1_TOKEN
         labels: [linux, gpu]
   ```
2. `runners.<name>.worker_id`（见上）。
3. worker 端 worker.yaml 的 `worker_id`（见 [worker-config.md](worker-config.md)「worker_id 三处对齐」）。

且 worker 端 token 必须解析为 `server.workers.<id>` 的同一 token。
- 缺 `server.workers` → 注册被拒（`worker_id not bound to this token`）。
- 缺 `runners` → 连上了但名册看不到、不能被派发。
- 也可以不手写：管理员 `gofer worker add <id>` 登记并拿一次性 token（见 [operations.md](operations.md)）。

## agents（agent 定义）

```yaml
agents:
  codex:
    type: cli-agent
    command: codex
    global_args: [-s, danger-full-access, -a, never] # 命令级选项；续接时仍放在 exec resume 前
    args: [exec, "{{prompt}}"]          # 批处理 argv(job run); 模板: {{prompt}} {{cwd}} {{job_id}} {{result_dir}}
    interactive_args: []                # pty argv(job run --interactive); [] = 裸 TUI; 有此字段 = 支持交互
    detect: { command: codex, args: [--version] }   # 探测本机是否装了
    # model_args: ["-m", "{{model}}"]   # --model 时插在含 {{prompt}} 的参数之前；须含 {{model}}、不得含 {{prompt}}；不写 = 内置(claude --model / codex -m)
    # from_session_args: [--from, "{{from_session}}"]  # --from-session 时追加在 args 之后；须含 {{from_session}}；无内置默认
    # read_only_args: [-s, read-only]   # --read-only 时追加（内置 codex / claude 已有）
    # exit_keys: [/exit, enter]         # 取消交互 job 时顺序写入 TUI；enter/ctrl-c/ctrl-d/escape 是按键名
    # exit_grace_sec: 8                 # 等退出横幅的上限；超时强杀，状态仍 cancelled
    # session_inject: [--session-id, "{{session_id}}"]   # 能预分配会话 id 时用（内置 claude 已有）
    # session_store_glob: "{{home}}/.codex/sessions/*/*/*/rollout-*.jsonl"   # 从本机会话文件找会话 id
    # session_store_id_regex: '([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})'
    # fallback_agents: [omp]            # 供应商错误时改派的候选(有序)
    # max_concurrent: 2                 # 该 agent 同时在跑的 job 上限: 超出排队(queued); 0/不写=不限
    # stall_timeout_sec: 300            # 输出停滞窗口覆盖: 0 = 永不判停滞; 不写=用 server 的值
    # budget: { max_tokens: 500000, max_cost_usd: 5, max_turns: 80 }  # 默认花费上限; 各维 0/不写=不限
    # env: { HOME: /home/x }            # 该 agent 的进程环境
    # transient_error_patterns: [...]   # 覆盖内置「瞬时错误」正则(不区分大小写)；内置含 at capacity|rate limit|429|503|stream disconnected|stalled: no output 等
    # ── 自研 agent 接入（均可选） ──
    # output_format: ndjson             # 按事件流读：stdout=最终答复、stderr=过程事件
    # ndjson_usage_path: usage          # ndjson 结果行里的用量对象(点路径)
    # transcript_dialect: generic       # claude|codex|omp|generic; 会话记录方言
    # inject_process: [myagent]         # tmux 送话的前台进程名
    # session_family: myfam             # 同族 cli/acp agent 可互相续接
    # deliver_command: [send, --session, "{{session_id}}"]   # 在线送话命令；退出码 0=送达 3=进程不在 其它=失败
    # deliver_offline_match: "no rollout found"   # 命令非 0 且输出匹配 → 按 exit 3 处理
    # deliver_stdin: true               # 文本走 stdin（argv 不含 {{text}}）
  exec:
    type: exec                          # 内置; 跑请求给的 argv, 不用模板
```

🔴 **一个 key 两种模式**：`args` = 批处理，`interactive_args` = pty；两者都写就是双模（内置 claude / codex 模板已默认双模，但**自定义同名 agent 是整体覆盖**，要自己写 `interactive_args`）。写了 `interactive: true` 的 agent = 仅交互（`args` 即 pty argv），普通 `job run` 会被拒 `has no batch mode`；`interactive: true` 配 `{{prompt}}` 是配置错误，serve 启动即拒。

- 从内置模板注入的 agent（本机 PATH 上装了对应 CLI 才注入，如 `claude-acp`）在 `agent list` / web 上标「内置」；同名声明整条覆盖模板。
- 会话文件扫描（`session_store_glob`）：在开跑后与终态前各扫一次，只认开跑后创建、且元数据里 `cwd` 等于执行目录的文件。内置：claude 用 `session_inject`，codex 扫 `~/.codex/sessions`，omp 扫 `~/.omp/agent/sessions`。CLI 自定了存储根时覆盖 glob。内置 `exit_keys` 需在目标环境确认能打出退出横幅。
- 在 web 改项目或执行 `project add` 只会重写改动的顶级块，`agents:` 等未改动的块（含注释与 `interactive_args: []`）原样保留。

**自研 cli-agent 示例**（一个支持会话续接与从旧会话分叉的 agent，如 suag）：

```yaml
  myagent:
    type: cli-agent
    command: myagent
    args: [run, --output-format, stream-json, --append-system-prompt, "{{system_prompt}}", --prompt, "{{prompt}}"]
    interactive_args: [chat]
    session_inject: [--session-id, "{{session_id}}"]
    session_resume: [run, --output-format, stream-json, --resume, "{{session_id}}", --prompt, "{{prompt}}"]
    session_resume_interactive: [chat, --resume, "{{session_id}}"]
    from_session_args: [--from, "{{from_session}}"]   # 追加在 args / interactive_args 之后
    read_only_args: [--read-only]
    output_format: ndjson
    ndjson_stdout: final_text
    ndjson_stdout_path: result
    session_family: myagent
```

源会话不存在时这类 agent 应以非 0 退出并在 stderr 说明，job 失败，可原样重试。

**acp-agent**（`type: acp-agent`，如内置 `claude-acp` / `codex-acp` / `omp-acp`）：`args: [acp]` 即协议入口，`{{prompt}}` 走协议不进 argv。

```yaml
  omp-acp:
    type: acp-agent
    command: omp
    args: [acp]
    acp:
      # modes: { read_only: plan }        # --read-only 映射到 agent 的 mode id
      # permission_policy: ask|strict     # 只能比项目 approval 更严; 默认沿用项目策略
      # load_session: false               # false = resume 直接报不支持
      # log_thoughts: false               # 默认 true: 思考合并成一行落 stderr + acp.jsonl
      # mcp_servers: [...]                # 给 ACP 会话注入的 MCP server
```

acp-agent 的日志分工：`stdout.log` 是 agent 文本，`stderr.log` 是紧凑事件行（工具调用 / 思考 / 审批 / 计划 / 结束），`artifacts/acp.jsonl` 是完整结构化记录。

## server / session / log / storage（常用项）

```yaml
server:
  addr: 0.0.0.0:8765                    # 默认 0.0.0.0:8765(容器经 host.docker.internal 可达)
  token_env: GOFER_TOKEN               # 鉴权 token 从 env 读
  allow_empty_token: false             # 要显式 true 才能无 token 起
  # web_enabled: true                  # 不写=开
  # web_base_url: https://gofer.example.com   # 对外地址(通知 / 待批链接里用)
  # path_view: host|container          # 执行视角(默认 host=用 host_path)
  # callers: [...]                     # 多调用方鉴权 + 每个 caller 的权限(can_admin / can_answer)与限流
  # governance: {...}                  # 限流全局兜底、require_answer_capability、attach_origins
  # max_job_timeout_sec: 3600          # job --timeout 上限; 超出被截断并明示; 项目 max_timeout_sec 可覆盖
  # job_recover_window_sec: 120        # worker 断线后在途 job 停在 recovering 等重连的窗口; 0 = 断线即 failed
  # dir_lock: true                     # 同目录串行锁(见下); false = 所有 job 共享目录
  # stall_timeout_sec: 900             # 输出停滞窗口(见下); 0 = 全局关
  # job_env_denylist: [...]            # job 环境里额外剔除的变量
  # tls:                               # 另开一个 HTTPS 监听(同路由同鉴权); 改它需重启
  #   addr: 0.0.0.0:9443
  #   cert_file: "{config_dir}/certs/server.crt"
  #   key_file: "{config_dir}/certs/server.key"
  # policy_repush: { timeout_sec: 60, max_attempts: 3 }  # 在线 POLICY worker 迟迟不确认时按退避重推
  # session_messaging:                 # web 给终端会话「发消息」的传话人
  #   enabled: true
  #   messenger_command: claude        #   传话进程命令(可写路径)
  #   messenger_timeout_sec: 90        #   单条超时
  #   messenger_idle_sec: 600          #   常驻传话进程空闲退出; 改它需重启
  # agent_fallback:                    # 故障转移开关
  #   on_failure: true                 #   失败后转移(配了候选才生效)
  #   pre_dispatch: false              #   提交时主 agent degraded 就改派(默认 false)
  # agent_health: { window_sec: 3600, degraded_after: 3, recover_after_ok: 1 }
  # hold:                              # 待批 job 的等待时限; 热重载
  #   default_timeout_sec: 86400       #   --hold-timeout 0/不给时(默认 24h); 到期 → cancelled
  #   max_timeout_sec: 604800          #   允许请求的上限(默认 7d); 超过直接 400
  # xfer: { max_bytes: 268435456, collect_max_bytes: 1073741824 }   # 文件传输上限
  # notification:
  #   max_text_runes: 3000             # 通知正文上限; 每个 webhook 可用同名字段覆盖; 钉钉/飞书另有字节上限
  #   webhooks: [...]                  # IM 通知; 事件订阅见 IM 通知文档
session:                               # 终端会话中继; 0 = 关该判据
  # auto_relay_idle_sec: 300           # 键盘空闲 >= 阈值 → 停下时在 web 等回复
  # auto_relay_turn_sec: 900           # 探测不到键盘(容器)时改看距上次人工输入的秒数
  # relay_on_wait_sec: 3600            # 显式 on 时等回复的上限; 0 = 不设上限
  # relay_auto_wait_sec: 600           # auto 布防的等待上限
  # progress_interval_sec: 30          # 进行中预览最短上报间隔; 0 = 关
  # takeover_alive_sec: 120            # 最近心跳在此秒数内 → 拒绝接管(session_alive); 0 = 关
  # offline_after_sec: 1800            # 无心跳超过它 → 标 offline; 0 = 关
  # takeover_input_delay_ms: 1500      # 接管 pty 首次输出后安静多久再写首条输入
  # inject_commands: [claude, codex]   # tmux 送话允许的前台进程
wakeup:
  # ttl_sec: 604800                    # 唤醒默认过期时间(7 天)
log:                                   # JSONL 文件日志(server 默认 <config-dir>/run/serve.log)
  # file: "{config_dir}/run/serve.log" # 显式路径(打不开则启动失败); 不写=默认路径(打不开只 warn)
  max_size_mb: 50
  max_age_days: 14
  max_backups: 10
storage:
  default_exchange_subdir: tmp
  default_result_subdir: gofer
  # root: "{config_dir}/results"       # 设了则结果落 <root>/<project>/<job>
  # db_path: "{config_dir}/gofer.db"   # 显式 SQLite 路径
  # retention: {...}                   # 终态 job 保留上限
```

- 本机路径字段 `server.tls.cert_file`、`server.tls.key_file`、`server.web_dir`、`log.file`、`log.dir`、`storage.root`、`storage.db_path` 可在开头写 `{config_dir}`（由 `GOFER_CONFIG_DIR` 决定，缺省 `~/.config/gofer`；`-c` 只选配置文件，不改它）。`serve --web-dir` 与 `gofer tool cert --out-dir` 也支持。agent 参数、prompt、project 路径与 worker roots 不支持这个变量。
- IM 通知（钉钉 / 飞书 / 通用 webhook）与可订阅事件见 <https://github.com/inhere/gofer/blob/main/docs/runbook/im-notification.md>。

## 待批 job 的审批权限

批准 / 拒绝走与验收相同的「人」判定（worker token、job 凭据一律拒绝）。CLI 在 agent 会话里拒绝 `job approve`、MCP 不提供 approve 工具只是护栏——容器里的 agent 若和人共用同一个用户 token，server 分不清谁在批。要真正隔离：给 agent 配一个**独立 token**（`server.callers` 里单独一项，不带 `can_answer`），人的 token 带 `can_answer: true`，并开 `server.governance.require_answer_capability: true`（开了却没有任何 caller 带 `can_answer` 会被配置校验拒绝）。用法见 [hold-approval.md](hold-approval.md)。

## 故障转移与健康度

agent 因供应商错误（at capacity / stream disconnected / 本地 sandbox 管道超时等）频繁挂掉时，不用手工改派：

```yaml
agents:
  codex:
    fallback_agents: [omp]              # 全局默认候选(有序); 也可在 projects.<key>.agent_fallbacks 里按项目覆盖
server:
  agent_fallback: { on_failure: true, pre_dispatch: false }
  agent_health: { window_sec: 3600, degraded_after: 3, recover_after_ok: 1 }
```

- 候选必须是 `agents` 里声明过的、非 exec、能跑批处理的 agent，加载时校验；提交时再按项目 `allowed_agents` 过滤（不在白名单的跳过并告警），解析结果随 job 冻结。单个 job 用 `job run --fallback omp,claude` 覆盖、`--no-fallback` 关掉。
- 触发：job 失败且命中瞬时错误模式、自己也续不了；验证步骤失败不算。动作：以普通 job 重提原请求（新会话、同一 cwd，prompt 加前缀说明只做剩余部分，标题加 `(→omp)`，继承 plan / tags / review / verify / todo 等）。
- 健康度（`gofer agent status`、web Agents 徽标）：窗口内没有 job = `unknown`；供应商错误 ≥ `degraded_after` 且之后成功数 < `recover_after_ok` → `degraded`。预算熔断不计入供应商错误。
- `pre_dispatch: true`：提交时主 agent 处于 degraded 就直接改用健康的候选（`job show` 里保留原本要的 agent）。默认关，避免「指定了 codex 却跑了 omp」。
- 事件 `agent.degraded` / `agent.recovered`（某个 agent 开始 / 停止抽风）可订阅，不在默认通知集，要写进 webhook 的 `events`。
- 探针：`gofer agent probe <key> [-p <project>]`（web Agents 页也有按钮）提交一个只回复 OK 的普通 job，结果计入健康度。

## 任务书模板目录

模板不是配置项，是 server 上的一批 md 文件，`job run -t <name>` 时由 server 渲染成 prompt：

| 位置 | 路径 | 用途 |
|---|---|---|
| 项目模板 | `<项目 host_path>/.gofer/templates/<name>.md` | 项目专属，跟随仓库走，可提交进 git |
| 全局模板 | `<config-dir>/templates/<name>.md` | 所有项目共用（`GOFER_CONFIG_DIR`，默认 `~/.config/gofer/`） |

- 同名时项目副本赢；项目目录在 server 上不可读（worker-only 项目）时只查全局目录。worker 上不需要放模板。
- 改文件即时生效（每次提交重新读取）；`gofer template ls` 看当前可用清单。
- frontmatter 只放 job 默认值与变量声明，写法见 [job-advanced.md](job-advanced.md)「任务书模板」。
- 全局目录对所有项目生效，只放真正通用的约束；项目特有的验证命令、禁区放项目模板。写法示例见 <https://github.com/inhere/gofer/tree/main/docs/examples/templates>。

## 同目录串行锁与输出停滞

两个「别让一个 job 白占资源」的开关，默认开、都可以按 job 反转：

```yaml
server:
  dir_lock: true                       # 同目录串行锁: 可写 agent job 独占其工作目录
  stall_timeout_sec: 900               # 输出停滞窗口(秒); 0 = 关
agents:
  omp:
    max_concurrent: 3                  # 这个 agent 的并发上限; 0/不写 = 不限
    stall_timeout_sec: 300             # 覆盖 server 的窗口; 0 = 永不判停滞
```

- **同目录串行锁**：两个可写 agent job（非只读、非交互）不能同时跑在重叠的目录上（同目录、祖先、子目录都算），后来的停在 `waiting_dir`，FIFO 排队；软链与 Windows 短名会归一化。exec 与只读 job 默认共享；`--exclusive-dir` / `--shared-dir` 单个反转；`--worktree` job 不取锁。
- **输出停滞**：非交互 job N 秒没有任何输出即杀掉并按瞬时错误处理（自动续投 / 故障转移接管）。顺序 `--stall-timeout` > agent `stall_timeout_sec` > server `stall_timeout_sec`；exec job 默认关，交互 job 恒关；等人作答与 verify 阶段不计时。

## 校验与生效

```bash
gofer config validate server           # 校验路径 / agent / runner；工作项摘要或管家开启却没有 webhook 订阅时给 [WARN]
gofer config info                      # 解析出的 config 路径 + 关键 ENV
gofer config show <project>            # 某 project 合并后的有效配置
gofer config edit                      # 用 $EDITOR 打开
gofer serve reload -c <config>         # 让 server 重读（打印 rev / changed / restart_required / error）
```

- web 改 project 会自动重推 POLICY worker；要某台 worker 重读自己的 worker.yaml：`gofer worker reload <id>`。
- 需重启才生效的（`restart_required` 里列出）：`server.addr`、token、tls、callers、session_messaging、policy_repush、agent_fallback、xfer、governance 等；`server.workers` 的增删改热生效。

---

- worker 侧怎么配（roots / guards / agents）见 [worker-config.md](worker-config.md)。
- 加 project / 建 worker / 迁移的分步配方见 [setup-recipes.md](setup-recipes.md)。
