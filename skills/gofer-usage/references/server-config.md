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
- [事件钩子](#事件钩子)
- [本机可用工具](#本机可用工具)
- [MCP 注册表](#mcp-注册表)
- [交付（worktree job）](#交付worktree-job)
- [价格表（费用估算）](#价格表费用估算)
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
tools:    [...]   # server 本机装了哪些外部工具(只给本机 runner 的 agent job)
include:  [...]   # 可选：把整段放到别的文件，见下
```

### 配置太长时：拆文件、去重

- **拆文件**：主文件写 `include: [config.d/*.yaml]`（相对主文件目录，可用 glob）。片段文件和主文件写法一样，只放整个顶层段（如 `agents:`、`projects:`）；一个段只能出现在一个文件里，片段里不能再写 `include`。web 改配置时写回该段所在的文件（保留注释，未变的文件不动）；新出现的段写进主文件。`gofer config split` 一键把 agents、projects 搬出去（按原文搬，注释随段走；先备份，前后比对生效配置一致才算成功）。`gofer config info` 列出所有文件，`gofer config edit --key agents` 打开 agents 所在文件。
- **去重**：`gofer config lint` 逐个键试删并重新加载，列出删掉也不改变生效配置的键（与默认值相同的 `detect`、日志 / 存储默认值、和默认续接参数相同的字段等），`--fix` 原地删除这些行（其余文本不动，先备份、改完复核）。不认识的键单独列出（多半是写错了），不会被删除。判定基于运行它的这台机器和当前 gofer 版本的默认值，server 配置要在 server 主机上跑。
- **agent 只写差异**：与内置模板同名的 agent 写 `extends: builtin`，只列要改的键（见下方 agents 一节）；lint 会给出改写后的最小写法。

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

与内置模板（claude / codex / opencode / 各 `*-acp`）同名、只想改几项时，用 `extends: builtin`：以模板为底，写了的键整项替换（包括写成 `false` / `[]`），没写的沿用模板并随 gofer 升级而更新；它仍是你声明的 agent，不受探测结果影响；web 改它时只写回差异。worker.yaml 的 agents 同样支持。

```yaml
agents:
  claude:
    extends: builtin
    interactive_args: [--enable-auto-mode]
    rules: [house-rules]
```

`extends` 还有两种写法：`builtin:<模板名>` 以指定的内置模板为底（agent 名可以和模板不同，例如 `claude-fast: {extends: "builtin:claude", model_args: [...]}`）；写另一个 agent 的名字，则以配置里已声明的那个 agent 为底（它自己也可以有 `extends`，可以链式继承，循环会报错）。同一份配置文件集内的 agent 才能互相继承。

```yaml
agents:
  claude-review:
    extends: claude          # 以上面声明的 claude 为底
    read_only_args: [--permission-mode, plan]
```

完整声明的写法：

```yaml
agents:
  codex:
    type: cli-agent
    command: codex
    global_args: [-s, danger-full-access, -a, never] # 命令级选项；续接时仍放在 exec resume 前
    args: [exec, "{{prompt}}"]          # 批处理 argv(job run); 模板: {{prompt}} {{cwd}} {{job_id}} {{result_dir}}
    interactive_args: []                # pty argv(job run --interactive); [] = 裸 TUI; 有此字段 = 支持交互
    detect: { command: codex, args: [--version] }   # 探测本机是否装了
    # model_args: ["-m", "{{model}}"]   # --model 时插在含 {{prompt}} 的参数之前（prompt 是 `--prompt`/`-p` 的值时插在该 flag 之前）；须含 {{model}}、不得含 {{prompt}}；不写 = 内置(claude --model / codex -m)
    # from_session_args: [--from, "{{from_session}}"]  # --from-session 时追加在 args 之后；须含 {{from_session}}；内置 claude 默认 [--resume, "{{from_session}}", --fork-session]
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
  # event_hooks: [...]                 # 事件发生时在 server 主机上跑命令(见下「事件钩子」); 只能改文件
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

## 事件钩子

事件发生时在 **server 主机**上运行一条命令，事件 JSON（与 generic webhook 的 POST body 相同）从 stdin 传入。只读旁路：输出不回写 gofer，成败只决定这次投递是否重试。

```yaml
server:
  event_hooks:
    - name: <hook-name>                  # 必填，唯一；投递记录 target = hook:<hook-name>
      command: ["<interpreter>", "<script>"]   # 必填，argv 形式，不经 shell
      events: [job.terminal]             # 空 = webhook 默认事件集；["*"] = 全部
      projects: [<project>]              # 空 = 全部项目
      # dir: <dir>                       # 工作目录，缺省 = gofer 配置目录
      # env: {KEY: value}                # 额外环境变量（值不出现在任何 API 读视图）
      # timeout_sec: 30                  # 默认 30，上限 300；超时杀整个进程树
      # max_attempts: 3                  # 含首次；退避 30s → 2m → 5m → 15m → 60m
      # enabled: true
```

- **只能改配置文件**：web 配置页只读展示，配置写接口拒绝 `event_hooks`；改完 `gofer serve reload` 生效。
- 不受 `notification.enabled` 与项目 `notify_enabled` 影响（那是通知人的开关）。
- 环境：serve 环境去掉凭据变量（同 job 的拒绝列表），加 `GOFER_EVENT_TYPE` / `GOFER_EVENT_SEQ` / `GOFER_DELIVERY_ID` / `GOFER_JOB_ID` / `GOFER_HOOK_NAME`，再叠加钩子自己的 `env`。
- 退出码 0 = 成功；stdout 丢弃，stderr 末尾 1 KiB 脱敏后进投递记录的 `last_error`。至少一次投递，脚本用 `GOFER_DELIVERY_ID` 去重。
- 自测：把样例 JSON 管道给脚本（`<script> < sample.json`），样例与细节见 <https://github.com/inhere/gofer/blob/main/docs/runbook/im-notification.md>「事件钩子」。

## 本机可用工具

顶层 `tools` 声明 **server 这台机器**装了哪些外部工具，只作用于本机 runner（`--runner server`）跑的 agent job；worker 在自己的 worker.yaml 里声明（形状相同，见 [worker-config.md](worker-config.md#tools本机可用工具)）。

```yaml
tools:
  - name: docker                     # 必填，agent 看到的名字
    desc: 容器构建与运行               # 可选，一句话用途 / 注意事项
  - name: <tool>
    bin: /opt/<tool>/bin/<tool>      # 可选，缺省 = name
```

- 本机执行的 agent job，prompt 在规则段、技能清单之后多一段「本机可用工具」；exec job、续接、`--no-rules` 不加。不改 PATH、不参与调度。
- 规则与上限同 worker：名字唯一（不区分大小写）、≤50 条、清单总长 ≤ 4 KiB；`bin` 找不到时 `config validate` 只 WARN。
- 随 `serve reload` 生效；不进项目瘦配置，web 设置页也不编辑（这是本机事实，由本机维护）。`GET /v1/runners` 的本机行 `capabilities.tools` 与 web Runners 页可见。

## MCP 注册表

MCP server 在顶层 `mcp_servers` 里**声明一次**，再按需绑定给 job；gofer 派发时按 agent 类型自动注入，不用在每台机器、每个 agent 里手写。

```yaml
mcp_servers:
  pg-readonly:
    desc: 业务库只读查询
    command: npx                       # stdio（默认）：command / args / env
    args: [-y, "@modelcontextprotocol/server-postgres"]
    secret_env:                        # 敏感值只写「MCP 进程里的变量名: 取值的环境变量名」
      DATABASE_URL: PG_READONLY_URL
  issues-api:
    type: http                         # http：url / headers
    url: https://mcp.example.internal/mcp
    secret_headers:
      Authorization: ISSUES_MCP_AUTH   # 环境变量里放完整的头值，如 "Bearer xxx"
    tools: [search_issues]             # 可选：只开放这些工具（codex 生效；其他 agent 会提示不支持）

projects:
  <project>:
    mcp: [pg-readonly]                 # 绑定：server.mcp / agents.<k>.mcp / projects.<k>.mcp 取并集
```

- **绑定**：`server.mcp`（所有 job，一般留空）、`agents.<k>.mcp`、`projects.<k>.mcp`、`job run --mcp <名字>`（可重复 / 逗号分隔）四级取并集；`--no-mcp` 全部关闭。名字不存在、agent 不支持注入时提交即被拒。`job show` / web job 详情显示 `mcp:`。
- **注入方式**由 agent 的 `mcp_format` 决定：acp-agent 自动走协议（`session/new` 的 mcpServers；http 只发给声明支持 http 的 agent，否则跳过并在 stderr 说明）；内置 claude 为 `claude-json`（生成 `<result_dir>/mcp.json`，经 `mcp_args: [--mcp-config, "{{mcp_config_file}}"]` 传入）；内置 codex 为 `codex-config`（`-c mcp_servers.*`）。自定义 agent 自己写 `mcp_format`（支持 `--mcp-config` 的 CLI 一般用 `claude-json` + `mcp_args`）；没写的 agent 绑定了 MCP 会被拒。agent 自己 `acp.mcp_servers` 里的同名条目优先于注册表。
- **凭据**：配置里只写环境变量名，值从**执行 job 的那台机器**的环境读取（本机 runner = server 进程，worker = worker 进程），缺了 job 直接被拒并写明缺哪个变量。值只进 agent 进程环境（或 ACP 协议参数），不进命令行、request_json、`mcp.json`、日志。`args` / `url` 可以写 `{{env:变量名}}` 引用 `secret_env` 里的项（codex 不支持，改用 secret_env 传给 MCP 进程）。
- **查看与测试**：`gofer agent mcp ls|show <名字>`（变量名 + 是否已设置，`--here` 按当前机器检查）；`gofer agent mcp test <名字>` 在当前机器启动 / 连接并列出工具；web「设置 → MCP」可看注册表并在 server 本机测试。
- 改注册表或绑定后 `serve reload` 即生效。agent 的 `mcp` / `mcp_format` / `mcp_args` 与 `server.mcp` 也可在 web 配置页编辑；注册表本身（`mcp_servers`）在配置文件里改。

## 交付（worktree job）

```yaml
projects:
  <project>:
    delivery:
      mode: pr                  # none（默认）| merge | push | pr；job 可用 --deliver 覆盖
      allow_push: true          # 推送许可：不开时 push / pr 一律拒绝，merge 只在本地合
      remote: origin
      branch: "gofer/{{job_id}}"   # 远端分支名模板：{{job_id}} {{short_id}} {{slug}}（由标题生成）
      base: ""                  # PR 目标 / 合并基线，空 = 主 checkout 当前分支
      draft: true               # PR 是否草稿（默认 true）
      squash: false             # merge 用 --squash
      push_base: false          # merge 后推基线分支（还要 allow_push）
      cleanup: false            # 交付成功后删 worktree（merge 时连分支一起删）
      labels: [agent]           # PR 标签（GitHub / GitLab）
      provider: auto            # auto | github | gitlab | gitea（自托管域名认不出时写明）
      token_env: ""             # 平台 token 的环境变量名，空 = 平台默认
```

行为、时机与平台细节见 [job-advanced.md](job-advanced.md#交付合并--推分支--开-pr)。

## 价格表（费用估算）

agent 自己报了费用的照用；没报的（终端会话用量、只给 token 的 job）按顶层 `pricing` 估算，界面标「估算」并和自报费用分开汇总。不内置任何价格，按你用的平台填写（每百万 token 美元）：

```yaml
pricing:
  "<model-name>":   { input: 0, output: 0, cache_read: 0, cache_write: 0 }
  "<model-prefix>*": { input: 0, output: 0 }      # 支持 * 通配，精确名优先，其次最长的模式
```

- 用在：会话页 / 抽屉的「估算费用」、plan 用量合计（「其中估算 $x」）、`/v1/stats` 会话用量窗口、job 详情（没自报费用且带 `--model`、有输入 / 输出拆分的 job）。
- 价格表里没有的模型会被列出（「N 个模型未定价」）。改价格 `serve reload` 即生效，历史用量按当前价格表显示。

## 校验与生效

```bash
gofer config validate server           # 校验路径 / agent / runner / 事件钩子；钩子命令在本机找不到、工作项摘要或管家开启却没人订阅时给 [WARN]
gofer config info                      # 解析出的 config 路径 + 关键 ENV
gofer config show <project>            # 某 project 合并后的有效配置
gofer config edit                      # 用 $EDITOR 打开
gofer serve reload -c <config>         # 让 server 重读（打印 rev / changed / restart_required / error）
```

- web 改 project 会自动重推 POLICY worker；要某台 worker 重读自己的 worker.yaml：`gofer worker reload <id>`。
- 需重启才生效的（`restart_required` 里列出）：`server.addr`、token、tls、callers、session_messaging、policy_repush、agent_fallback、xfer、governance 等；`server.workers` 的增删改热生效。
- `changed` / `restart_required` 按**生效值**比较：删掉与默认值相同的键、agent 改写成 `extends: builtin`、把段落挪进 `include` 文件，只要实际生效的配置没变，都不会列出。

---

- worker 侧怎么配（roots / guards / agents）见 [worker-config.md](worker-config.md)。
- 加 project / 建 worker / 迁移的分步配方见 [setup-recipes.md](setup-recipes.md)。
