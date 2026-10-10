# config.yaml（server）配置参考

> 配主机 server：project 目录映射、准入白名单、runner 名册、worker 绑定。全部 `<占位符>`。
> 脚手架：`gofer init server`（`-g` 写全局 `~/.config/gofer/config.yaml`）。校验：`gofer config validate server`。改配置后 `SIGHUP` 或 `gofer` reload 即时生效。

## 结构总览

```yaml
server:   {...}   # 监听地址 / 鉴权 / callers / workers 绑定 / 限流 / webhook
storage:  {...}   # 结果落盘 / 保留 / 录制
projects: {...}   # ★ 每个 project 的目录 + 准入(agent/runner) —— 配得最多
agents:   {...}   # agent 定义(cli-agent 命令 + detect 探测)
runners:  {...}   # runner 名册: local / peer-http / worker
```

## 1. projects（配得最多）

> 不写 `default:` 时 server 自动注入内置 `default` 项目（`~/.gofer/workspace` / `GOFER_WORKSPACE`，`allowed_runners: [local]`，`allowed_agents` = 本机可用 agent），列表标「内置」、不落盘、不能删；要自定义就显式声明 `default:`（整条覆盖）；Web 里编辑它并保存会把它写进 config 成为声明项目。

```yaml
projects:
  my-project1:
    host_path: D:/projects/my-project1        # 逻辑路径(server 视角; 也是下发给 worker 的 host_path)
    # dir_lock_mode: repo                    # cwd 下有嵌套仓库时，可写 job 必须 --lock 或显式 shared/exclusive
    container_path: /work/projects/my-project1 # 容器执行视角(server path_view=container 时用)
    default_agent: codex
    allowed_agents: [codex, claude, exec]      # 准入白名单(空=放行全部)
    # agent_fallbacks: {codex: [omp, claude]}  # ★ 该项目的故障转移候选(SUP-01 P3): 按"挂掉的 agent"给有序候选,
                                               #   覆盖 agent 级 fallback_agents(整份替换, 不合并); 空列表 = 该 agent 不转移

    allowed_runners: [local, builder]          # ★ 见下 —— 决定派给谁
    allow_exec: true
    allow_interactive: true                    # pty/交互 job 的项目级开关(默认 false); 项目侧唯一的交互闸
                                               # (旧字段 interactive_allowed_agents 已于 v0.48 移除: 配置里再出现
                                               #  该键 → 加载直接报错, 请改用本开关)
    max_concurrent_jobs: 4                      # 该 project 并发上限(0/不写=无限)
    # max_timeout_sec: 7200                     # 该项目 job 超时上限(秒), 覆盖 server.max_job_timeout_sec(可高可低)
    # budget: { max_tokens: 1000000, max_cost_usd: 10 }  # 该项目 job 默认花费上限(N2 §B): 盖过 agent.budget、被请求盖过; 只对能上报用量的 agent 生效(exec/pty/文本 agent 不受影响)
    # worktree_default: true                    # 该项目 job 默认在受管 git worktree 里跑(= 每个 job 都 --worktree)
    # capture_diff: auto                        # auto/on/off；auto 默认跳过普通 exec，cli-agent 或 review job 采集；旧 true/false 仍兼容
    # scope_discipline: auto                    # auto/on/off；agent job prompt 末尾追加「## 交付约定」(只改相关内容、范围外写「发现但不碰」)。auto=todo 派发/要验收/带 acceptance 或 scope 的 job
    # knowledge_capture: auto                   # auto/on/off(同上口径)；「## 交付约定」再要求汇报末尾写「## 可复用经验」，交付时记为经验候选(`gofer memory candidates`)，人接受才入记忆
    # verify: [go, test, ./...]                 # 该项目 job 的默认验证步骤(SUP-01 P2): agent 正常结束后在同一个 cwd/env 跑,
                                             #   非 0 退出 → job failed(开 review 则停 needs_review); 需要 allow_exec;
                                             #   单个 job 用 `job run --no-verify` 关掉, `--verify '…'` 覆盖
    # verify_timeout_sec: 900                   # 验证步骤的独立超时(不写=600s)
  # 瘦写法: 只写 host_path/container_path + allowed_agents, 其余走默认
  my-tools:
    host_path: D:/projects/my-tools
    container_path: /work/projects/my-tools
    allowed_agents: [exec]
```

🔴 **`allowed_runners` 决定这个 project 派给谁**（列的是 runner 的**名字**，见 §3）：
- 含 `local` → 可在 **server 本机**跑。
- 含某 **worker-runner 名**（如 `builder`）→ 可派给那台 worker，**且 server 会把这个 project 下发进那台 worker 的 POLICY**（`computePolicy` 按可达性算）。
- **空 `allowed_runners` = 不推给任何 worker、也不在本机跑**（不是通配）。

## 2. 🔴 worker = 两段配置（server 侧 + worker 侧）

要把 project 派到某 worker 执行，两边都要配、且 **project key 两边一致**：

| | server 侧(本文件) | worker 侧(worker.yaml) |
|---|---|---|
| 准入 | project.`allowed_runners` 含该 **worker-runner 名** | (POLICY 下无需, 见下) |
| 执行 | — | LEGACY: 同名 project.`allowed_runners: [local]` / POLICY: `roots` 覆盖其 `host_path` |
| project key | 一致 | 一致(对不上 worker 以"未知项目"拒) |

- **POLICY worker**：只要 server 侧 project 的 `allowed_runners` 含它的 runner + worker 的 `roots` 覆盖 `host_path`，就自动下发，**worker.yaml 无需再定义该 project**（这是 P3 的价值）。
- **LEGACY worker**：还得在 worker.yaml 里同名再定义一遍（`allowed_runners: [local]`）。

## 3. runners（名册）

```yaml
runners:
  local: { type: local }                       # 内置, 声明可选
  builder:                                      # ws-worker 派发目标
    type: worker
    worker_id: builder-1                        # = server.workers 的 KEY = worker 端 worker_id
  docker-peer:                                  # 反向调用容器内的 peer bridge
    type: peer-http
    base_url: http://127.0.0.1:8766
    token_env: CONTAINER_BRIDGE_TOKEN
```

- 声明一个 `type: worker` 的 runner 才会：① 让该 worker 出现在 `/v1/runners` 名册（Web 可见）；② 可被 `--runner <名>` 派发。reload 后新增/删除 type=worker runner 会立即更新名册和派发注册；peer-http 等启动级 runner 修改会保留到重启，并出现在 `restart_required`。
- project 要用它，还得在该 project 的 `allowed_runners` 里加上这个 runner 名。

## 4. 🔴 worker_id 三处对齐

一台 worker 连上并可派发，**同一 `worker_id`** 三处一致（缺一不可）：

1. `server.workers` 的 **KEY**（+ 绑定 token）：
   ```yaml
   server:
     workers:
       builder-1:
         token_env: GOFER_WORKER_BUILDER1_TOKEN
         labels: [linux, gpu]
   ```
2. `runners.<name>.worker_id`（§3）。
3. worker 端 worker.yaml 的 `worker_id`（见 `worker-config.md` §5）。

且 worker 端 token 必须解析为 `server.workers.<id>` 的同一 token。
- 缺 `server.workers` → 注册被拒（`worker_id not bound to this token`）。
- 缺 `runners` → 连上了但名册看不到。

## 5. agents（agent 定义 + detect 探测）

```yaml
agents:
  codex:
    type: cli-agent
    command: codex
    global_args: [-s, danger-full-access, -a, never] # 命令级选项；续接时仍放在 exec resume 前
    args: [exec, "{{prompt}}"]          # 批处理 argv(job run); 模板: {{prompt}} {{cwd}} {{job_id}} {{result_dir}}
    interactive_args: []                # pty argv(job run --interactive); [] = 裸 TUI; 有此字段 = 支持交互
    # model_args: ["-m", "{{model}}"]   # `job run --model` 时插在含 {{prompt}} 的参数之前（无则追加末尾）；须含 {{model}}、不得含 {{prompt}}；不写 = 内置表（claude --model / codex -m，按 key 或 command 基名）；只对 cli-agent 有效
    # from_session_args: [--from, "{{from_session}}"] # `job run --from-session <id>` 时追加在 args 模板之后（新会话继承旧会话上下文）；须含 {{from_session}}、不得含 {{prompt}}；无内置默认；只对 cli-agent 有效
    # exit_keys: [/exit, enter]         # 取消时顺序写入 TUI；enter/ctrl-c/ctrl-d/escape 是按键名
    # exit_grace_sec: 8                 # 等待退出横幅的上限；超时后强杀，状态仍 cancelled
    # session_inject: [--session-id, "{{session_id}}"] # 能预分配时立即记录；Claude 内置已有
    # session_store_glob: "{{home}}/.codex/sessions/*/*/*/rollout-*.jsonl"
    # session_store_id_regex: '([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})'
    detect: { command: codex, args: [--version] }   # 探测本机是否装了
    # fallback_agents: [omp]            # ★ 供应商错误时改派的候选(SUP-01 P3): 有序, 逐个尝试
    # max_concurrent: 2                 # ★ 该 agent 同时在跑的 job 上限(JOB-11): 超出的排队(queued), 不拒绝; 0/不写=不限
    # stall_timeout_sec: 300            # ★ 输出停滞窗口覆盖(AUTO-05): 0 = 这个 agent 的 job 永不被判停滞; 不写=用 server 的值
    # budget: { max_tokens: 500000, max_cost_usd: 5, max_turns: 80 }  # ★ 该 agent 的 job 默认花费上限(N2 §B GATE-02); 各维 0/不写=不限; 项目 budget 与请求按维度逐项覆盖它; 越线 job failed + failure_class=budget
    # ── 自研 agent 接入（均可选；详见 docs/runbook/session-relay.md §9）──
    # ndjson_usage_path: usage          # ndjson 结果行里的用量对象(点路径, 如 result.usage); 入 job 用量, source=ndjson:<agent>; 需 output_format: ndjson
    # transcript_dialect: generic       # claude|codex|omp|generic; 不写按 agent key 前缀再嗅探
    # inject_process: [myagent]         # tmux 送话的前台进程名(无路径/扩展名); 与 session.inject_commands 并集, 只对该 agent 的会话生效
    # session_family: myfam             # 同族 cli/acp agent 可互相续接; 覆盖内置族(claude/codex)
    # deliver_command: [send, --session, "{{session_id}}"]   # 在线送话命令(接在 command 后; 首元素绝对路径=完整 argv); 退出码 0=送达 3=进程不在 其它=失败
    # deliver_offline_match: "no rollout found"   # Go 正则: 命令非 0 且输出匹配 → 按 exit 3 处理; codex 内置默认已带 deliver_command+此项
    # deliver_stdin: true               # 文本走 stdin(argv 不含 {{text}}); worker 需协议 >= v18
    # transient_error_patterns: [...]   # 覆盖内置的"瞬时错误"正则(不区分大小写); 内置含 at capacity|rate limit|
                                        #   429|503|stream disconnected|windows sandbox failed|connecting runner pipe|stalled: no output 等
  exec:
    type: exec                          # 内置; 跑请求给的 argv, 不用模板
```

🔴 **一个 key 两种模式**：`args` = 批处理，`interactive_args` = pty；两者都写就是双模（内置模板的 claude/codex 已默认双模，但**自定义同名 agent 是整体覆盖**，要自己写 `interactive_args`）。旧写法 `interactive: true` = "仅交互、args 即 pty argv"（内置模板已不再提供这种 agent；`tty-claude` / `tty-codex` 已移除，交互直接用 `claude` / `codex` + `--interactive`），这种 agent 普通 `job run` 会被拒 `has no batch mode`；`interactive: true` 配 `{{prompt}}` 是配置错误，serve **启动即拒**。四种组合与校验规则见 `config/gofer.example.yaml` 的 agents 注释。

会话存储扫描在运行后约 0.5 秒和终态前各做一次：文件必须晚于开始时间，前 16 行 JSON 元数据里的 `cwd` 必须等于执行目录，取修改时间最新者；ID 正则先匹配元数据 `id`，再匹配文件名。内置 Claude 用 `session_inject`；Codex 默认扫描 `~/.codex/sessions/*/*/*/rollout-*.jsonl`，OMP 默认扫描 `~/.omp/agent/sessions/*/*.jsonl`。使用 CLI 自定存储根时覆盖 glob。OMP 18.3.5 的 `/exit` 已实测会打印 resume 横幅；Claude/Codex 的隔离 TUI 本轮未完成认证/网络验证，内置 `/exit` 需在目标环境复核。

✅ **写回安全**（bd h-aii-kd57）：`config.Save` 现在按顶级键做**外科写回** —— 未改动的块（`agents:` 等）连注释、键序、`interactive_args: []` 的空列表拼写一起原样保留，只重写真正改动的块（块内注释会丢）。所以「web 改项目设置 / `project add|update` 把 `interactive_args: []` 抹掉、agent 变成不可交互」这个问题不会再出现；`log`/`session` 也不会再被重复写出。

**自研 cli-agent 示例（suag，含 `--from-session`）**：`from_session_args` 让 `job run --from-session <id>` 开一个继承旧会话（摘要/计划/已加载工具）的**新**会话；新会话 id 仍由 `session_inject` 注入。suag 找不到源会话时退出码 4、stderr `session not found: <id>`，job 失败，同一 `--session-id` 可重试。

```yaml
  suag:
    type: cli-agent
    command: suag
    args: [run, --output-format, stream-json, --append-system-prompt, "{{system_prompt}}", --prompt, "{{prompt}}"]
    interactive_args: [chat]
    session_inject: [--session-id, "{{session_id}}"]
    session_resume: [run, --output-format, stream-json, --resume, "{{session_id}}", --prompt, "{{prompt}}"]
    session_resume_interactive: [chat, --resume, "{{session_id}}"]
    from_session_args: [--from, "{{from_session}}"]   # 追加在 args / interactive_args 之后: suag run|chat … --from <id>
    read_only_args: [--read-only]
    output_format: ndjson
    ndjson_stdout: final_text
    ndjson_stdout_path: result
    session_family: suag
```

**acp-agent**（`type: acp-agent`，如内置 `claude-acp`/`omp-acp`）：`args: [acp]` 即协议入口，`{{prompt}}` 走协议不进程 argv。可选项：

```yaml
  omp-acp:
    type: acp-agent
    command: omp
    args: [acp]
    acp:
      # modes: { read_only: plan }        # `job run --read-only` 映射到 agent 的 mode id
      # permission_policy: ask|strict     # 只能比项目 approval 更严; 默认沿用项目策略
      # load_session: false               # false = resume 直接报不支持, 不尝试 session/load
      # log_thoughts: false               # 默认 true: 思考合并成一行落 stderr + acp.jsonl; false 完全不落
```

acp-agent 的日志分工：`stdout.log` 是 agent 文本（一条消息一块、块间空行），`stderr.log` 是紧凑事件行（`tool_call`/`thought`/`permission`/`plan`/`stop`，web 时间线直接渲染），`artifacts/acp.jsonl` 是完整结构化流；job 事件时间线只留生命周期（`job.permission_*` + 每回合一条 `job.acp_summary`）。

## 6. server / storage（常用项）

```yaml
server:
  addr: 0.0.0.0:8765                    # 默认 0.0.0.0:8765(容器经 host.docker.internal 可达)
  token_env: GOFER_TOKEN               # 鉴权 token 从 env 读
  allow_empty_token: false             # 要显式 true 才能无 token 起
  # web_enabled: true                  # 不写=开; false 关 web 控制台
  # path_view: host|container          # 执行视角(默认 host=用 host_path); 不自检容器
  # callers: [...]                     # 多调用方鉴权 + per-caller 配额/限流
  # governance: {...}                  # 限流全局兜底
  # max_job_timeout_sec: 3600          # job --timeout 上限(默认 1h); 超出被 clamp 且 CLI/API 明示; 项目 max_timeout_sec 可覆盖
  # job_recover_window_sec: 120        # worker 断线后 in-flight job 停在 recovering 等重连的窗口; 0 = 关(断线即 failed)
  # dir_lock: true                     # ★ 同 cwd 串行锁(JOB-11): 可写 agent job 独占其工作目录(祖先/子目录同锁), 后者排队等
  #                                    #   false = 所有 job 共享目录(退回 JOB-11 之前的语义); 单个 job 用 --exclusive-dir/--shared-dir 反转
  # stall_timeout_sec: 900             # ★ 输出停滞窗口(AUTO-05): 非交互 job 静默超过 N 秒即杀(failed: stalled: ...)并按 transient 续投/转移
  #                                    #   0 = 全局关; exec job 默认不吃这个值(要显式给); 交互 job 恒关
  # tls:                               # ★ 另开一个 HTTPS 监听(同路由同鉴权); HTTP 监听不变(CLI/worker 继续走 HTTP); 改它需重启
  #   addr: 0.0.0.0:9443               #   证书/私钥用 `gofer tool cert` 生成, 放仓库外或 tmp/, 不入库不进日志; Android PWA 步骤见 docs/runbook/https-pwa.md
  #   cert_file: "{config_dir}/certs/server.crt"
  #   key_file: "{config_dir}/certs/server.key"
  # policy_repush: { timeout_sec: 60, max_attempts: 3 }  # 在线 POLICY worker 迟迟不回 Applied 时按退避重推策略
  # session_messaging:                 # ★ web 给终端会话「发消息」的传话人(经 SendMessage 转达; 对方视为"另一会话转达", 非用户审批)
  #   enabled: true                    #   默认开
  #   messenger_command: claude        #   传话进程命令(可写路径)
  #   messenger_timeout_sec: 90        #   单条超时, 超时判失败并重启传话进程
  #   messenger_idle_sec: 600          #   常驻传话进程空闲退出; 本机 runner 与协议 >= v14 的 worker 走常驻, 更旧 worker 退回一次性 job; 改它需重启
  # agent_fallback:                    # ★ 故障转移开关(SUP-01 P3)
  #   on_failure: true                 #   失败后转移(默认 true; 配了 fallback_agents/agent_fallbacks 才生效)
  #   pre_dispatch: false              #   提交时主 agent degraded 就改派(默认 false: 不悄悄换掉你指定的 agent)
  # hold:                              # ★ 待批 job(--hold)的等待时限; 热重载, 只影响之后提交的待批 job(已在等的过期时刻提交时已定)
  #   default_timeout_sec: 86400       #   --hold-timeout 0/不给时的等待秒数(默认 24h); 到期没人决定 → cancelled(hold expired)
  #   max_timeout_sec: 604800          #   允许请求的上限(默认 7d); 超过直接 400, 不截断
  # agent_health:                      # 健康度统计窗口/阈值(SUP-01 P3)
  #   window_sec: 3600                 #   统计窗口(秒)
  #   degraded_after: 3                #   窗口内供应商错误 >= N → degraded
  #   recover_after_ok: 1              #   最近一次供应商错误之后成功 >= N → 恢复
session:                               # 终端会话中继(SESS-01 R1/R2)的自动布防判据; 0 = 关该判据
  # auto_relay_idle_sec: 300           # 键盘空闲 >= 阈值 → 会话停下时在 web 等回复
  # auto_relay_turn_sec: 900           # 探测不到键盘(容器)时改看距上次人工输入的秒数
  # relay_on_wait_sec: 3600            # 显式 on 时 Stop 阻塞等回复的服务端上限(秒); hook 取 min(--wait, 它); 0 = 不设上限; 热重载
  # relay_auto_wait_sec: 600           # 同上, auto 布防的等待(idle_probe / turn_age)
  # progress_interval_sec: 30          # PostToolUse 进行中预览最短上报间隔；0 = 关闭
  # takeover_alive_sec: 120            # web 接管前: 最近一次 hook 心跳在此秒数内 → 拒绝接管(session_alive); 0 = 关; 有 deliver_command 的 agent 以其 exit 3 为准
  # offline_after_sec: 1800            # 会话无心跳超过它 → 标 offline(进程可能已被杀掉); 0 = 关闭; 热重载; 有 OPEN 中继 turn 的会话从 turn 截止时刻起算
log:                                   # 结构化 JSONL 文件日志(server 默认 <config-dir>/run/serve.log; worker 为 run/worker-<id>.log)
  # file: "{config_dir}/run/serve.log" # 显式路径(打不开则启动失败); 不写=默认路径(打不开只 warn 并降级为 stderr)
  max_size_mb: 50                      # 单文件上限; 超过轮转
  max_age_days: 14                     # 轮转文件保留天数
  max_backups: 10                      # 轮转文件保留份数
storage:
  default_exchange_subdir: tmp
  default_result_subdir: gofer
  # root: "{config_dir}/results"       # 设了则结果落 <root>/<project>/<job>
  # db_path: "{config_dir}/gofer.db"    # 显式 SQLite 路径；不写时从 root 或 config-dir 推导
  # retention: {...}                   # 终态 job 保留上限(prune)
```

**待批 job 的审批权限（推荐配置）**：批准 / 拒绝走与验收相同的「人」判定（worker token、job 凭据一律 403）。CLI 在 agent 会话里拒绝 `job approve`、MCP 不提供 approve 工具只是护栏——容器里的 agent 若和人共用同一个用户 token，server 分不清谁在批。要真正隔离：给 agent 配一个**独立 token**（`server.callers` 里单独一项，不带 `can_answer`），人的 token 带 `can_answer: true`，并开 `server.governance.require_answer_capability: true`（开了却没有任何 caller 带 `can_answer` 会被配置校验拒绝）。用法与语义见 [hold-approval.md](hold-approval.md)。

本机路径字段 `server.tls.cert_file`、`server.tls.key_file`、`server.web_dir`、`log.file`、`log.dir`、`storage.root`、`storage.db_path` 可在路径开头写精确的 `{config_dir}`。目录由 `GOFER_CONFIG_DIR` 决定，未设置时为 `~/.config/gofer`；`-c/--config` 仅选择配置文件，不改变该目录。`serve --web-dir` 和 `gofer tool cert --out-dir` 也支持同一写法；例如 `gofer tool cert --out-dir '{config_dir}/certs'`。没有变量的相对路径仍按原来的工作目录解释。支持字段中的其他花括号变量会按字段报错；保存配置和重载后仍保留模板原文。agent 参数、prompt、project 的 host/container 路径和 worker roots 不使用此路径变量。

## 7. agent 故障转移与健康度（SUP-01 P3）

codex 一天挂三次（at capacity / stream disconnected / 本地 sandbox 管道超时）时，人不用再手工把同一份任务书改派 omp：

```yaml
agents:
  codex:
    fallback_agents: [omp]              # 全局默认候选(有序); 也可以写在 projects.<key>.agent_fallbacks 里按项目覆盖
server:
  agent_fallback:
    on_failure: true                    # 默认 true(只有配了候选才生效)
    pre_dispatch: false                 # 默认 false
  agent_health: {window_sec: 3600, degraded_after: 3, recover_after_ok: 1}
```

规则与边界（详见 `docs/design/2026-09-18-supervision-loop-and-agent-reliability-design.md` §一）：

- **候选**必须是在 `agents` 里声明过的、非 exec、且能跑批处理的 agent（交互-only 的 `interactive: true` agent 不行）；**加载期**就校验，写错启动即报。**提交期**还会按项目 `allowed_agents` 过滤（不在白名单的候选跳过并 warn，一个都不剩就等于没有候选），并把**解析结果冻结**进 job（`fallback_json`）——运行中改配置不会让链条漂移。单个 job 用 `job run --fallback omp,claude` 覆盖、`--no-fallback` 关掉。
- **触发**：job `failed` 且命中该 agent 的瞬时错误模式、且自己也没法续（或已经续过一次又挂）。**验证步骤失败不算**（那是活的问题，不是供应商的问题）。
- **动作**：以一个**普通 job** 重提链根那次的请求——新会话、同一 cwd（源在 worktree 里就继续用那个 worktree）、prompt 加前缀说明"上一次由 X 执行、只做剩余部分"、标题加 `(→omp)`，并继承 plan/tags/timeout/read_only/review/verify/todo/caller。链长 = 候选数。
- **健康度**（`GET /v1/agents` 的 `health` 块、`gofer agent status`、web 徽标）：窗口内没有 job = `unknown`（不是"健康"）；`transient_fail ≥ degraded_after` 且最近一次供应商错误后成功数 `< recover_after_ok` → `degraded`。`failure_class`（transient|other|budget）在每个 failed job 上无条件记录（`budget` = 被预算熔断杀掉，不是 transient，不会让 agent 被判 degraded）。
- **探针**：`gofer agent probe <key>`（web Agents 页也有按钮）提交一个 `--sync` 探针 job（固定 prompt、`tags: [probe]`），它走的就是普通提交路径，所以结果天然计入健康度。

## 8. 任务书模板目录（`<config-dir>/templates/`，SUP-01 P5）

模板**不是**配置项：它们是 server 上的一批 md 文件，提交时 `job run -t <name>` 让 server 渲染成 prompt。
查找顺序是「项目目录优先、全局目录兜底」：

| 位置 | 路径 | 用途 |
|---|---|---|
| 项目模板 | `<projects.<key>.host_path>/.gofer/templates/<name>.md` | 这个项目专属的任务书（跟随仓库走，可提交进 git） |
| 全局模板 | `<config-dir>/templates/<name>.md` | 所有项目共用（`GOFER_CONFIG_DIR`，默认 `~/.config/gofer/`） |

- 同名时**项目副本赢**；项目目录在 server 上不可读（worker-only 项目）时只查全局目录——这正是全局目录存在的理由。
- 文件格式：`---` frontmatter（YAML，白名单键：`desc`/`agent`/`runner`/`model`/`timeout_sec`/`tags`/`verify`/`verify_timeout_sec`/`review`/`read_only`/`worktree`/`fallback_agents`/`vars`）+ 正文（prompt 模板，`{{变量}}`、`{{include: 同目录.md}}`）。
- 改文件**即时生效**（每次提交都重新读取），不需要 SIGHUP，也没有"安装"步骤；`gofer template ls` 看当前能被项目用到的清单。
- 仓库自带两份示例（`docs/examples/templates/`），按全局目录的布局摆放：
  `mkdir -p ~/.config/gofer/templates && cp docs/examples/templates/*.md ~/.config/gofer/templates/`。
- 写模板的纪律：frontmatter **只**放 job 默认值与变量声明——`plan_id`/`caller_id`/`cmd` 这类在提交面才该出现的字段会被解析报错拒掉。
- worker：模板由 **server** 渲染，所以 worker 上不需要放模板文件；worker-only 项目用全局目录。

## 9. 同目录串行锁与输出停滞（JOB-11 / AUTO-05）

两个"别让一个 job 白占资源"的开关，都是**默认开**、都可以按 job 反转：

```yaml
server:
  dir_lock: true                       # 同目录串行锁: 可写 agent job 独占其工作目录
  stall_timeout_sec: 900               # 输出停滞窗口(秒); 0 = 关
agents:
  omp:
    max_concurrent: 3                  # 这个 agent 的并发上限; 0/不写 = 不限
    stall_timeout_sec: 300             # 覆盖 server 的窗口; 0 = 这个 agent 永不被判停滞
```

**同目录串行锁**（`server.dir_lock`，JOB-11）：两个**可写 agent job**（cli-agent/acp-agent、非 `--read-only`、非交互）不允许同时跑在同一个工作目录上——后来的那个停在非终态 `waiting_dir`（统计上等同 `queued`、可取消），事件 `job.waiting_dir {holder_job, dir}` 说明在等谁。**目录重叠就算同一把锁**（`proj/` 与 `proj/sub/` 互斥），路径经 `util.RealPath` 归一化（软链、Windows 8.3 短名同一化），FIFO 排队。exec 与只读 job 默认**共享**（巡检类 job 不该排队）；`job run --exclusive-dir` / `--shared-dir` 单个反转；`--worktree` job 天然独立、不取锁。`agents.<key>.max_concurrent` 是同一信号量机制的 per-agent 版本（超出停在 `queued`）。

**输出停滞**（`server.stall_timeout_sec`，AUTO-05）：跑着的非交互 job **N 秒没有任何输出**（stdout/stderr 有没有新字节；acp 的 `session/update` 走 stderr 所以同样计入）即判停滞：`job.stalled` 事件 + 杀进程 + `failed: stalled: no output for Ns`，且按 **transient** 归类，于是自动续投 / 故障转移照常接管（否则一个挂死的供应商流只能等 job 自己的 deadline）。解析顺序 `job run --stall-timeout` > `agents.<key>.stall_timeout_sec` > `server.stall_timeout_sec`（默认 900s）；**exec job 默认关**（构建/测试本来就会长时间没输出，要就显式给 `--stall-timeout`）；交互 job 恒关；等人作答（`pending_interaction`）与 verify 阶段不计时。`job run --no-stall` 单个关掉。

## 校验 / 生效

```bash
gofer config validate server           # 校验路径/agent/runner
gofer config info                      # 看解析出的 config 路径 + 关键 ENV
gofer config show <project>            # 看某 project overlay 合并后的有效配置
# 改完让 server 重读: `gofer serve reload -c <config>`（本机 PID/SIGHUP 或 Windows 命名事件），或 unix SIGHUP、任何平台 POST /v1/config/reload、web 设置页「重新读取文件」(需 can_admin)
# 本机 CLI 等待 run/serve.reload.json 并打印 rev/path/changed/restart_required/error（回执含 reloaded_at，CLI 据此判新结果）；HTTP 响应也带 rev、changed 和 restart_required
# web 改 project 会自动重推 POLICY worker; 要某台 worker 重读自己的 worker.yaml: gofer worker reload <id>
# 需重启才生效（editable 表标 restart_required）: server.addr / token / tls / callers / session_messaging / policy_repush / agent_fallback / xfer / metrics / governance 等；`server.workers` reload 会更新绑定、selector 和 worker runners，新增/删除/改 token 不需要重启
```

---

- worker 侧怎么配（roots/guards/agents）见 `worker-config.md`。
- 加 project / 建 worker / 迁移的分步配方见 `setup-recipes.md`。
