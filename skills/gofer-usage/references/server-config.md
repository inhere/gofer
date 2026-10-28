# config.yaml（server）配置参考

> 配主机 server：project 目录映射、准入白名单、runner 名册、worker 绑定、agent 定义。全部 `<占位符>`，按实际替换。
> 脚手架：`gofer init server`（`-g` 写全局 `~/.config/gofer/config.yaml`）。校验：`gofer config validate server`。改完用 `gofer serve reload -c <config>`（或 Unix SIGHUP、web 设置页「重新读取文件」）生效。 生成的配置不含示例项目（只有指向默认工作空间的 `default`；示例写在 `projects:` 下的注释里），接着 `gofer project add` 即可。

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
- [决策 hook](#决策-hook)
- [本机可用工具](#本机可用工具)
- [MCP 注册表](#mcp-注册表)
- [job 密钥（--secret）](#job-密钥--secret)
- [交付（worktree job）](#交付worktree-job)
- [价格表（费用估算）](#价格表费用估算)
- [检查更新](#检查更新)
- [worker 自助注册（worker_enrollment）](#worker-自助注册worker_enrollment)
- [调用方上限（ceiling）](#调用方上限ceiling)
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
    # continue_max_chain: 3                   # 计划里「接着上一项会话做」(continue_from)最多连续接续几次，达到后下一项开新会话；默认 3，0=不限，最大 10
    # worktree_default: true                  # 该项目 job 默认在受管 git worktree 里跑
    # worktree:                               # --worktree job 落在哪、分支叫什么、从主 checkout 链接哪些目录
    #   dir: ".worktrees/{name}"              # 相对仓库顶层；默认 tmp/gofer/wt/{job_id}；{name} 来自 --worktree-name（未给时 = job id）
    #   branch: "feat/{name}"                 # 默认 gofer/{job_id}；--branch 可单次覆盖
    #   links: [web/node_modules]             # 建好 worktree 后从主 checkout 同路径链接过来（Windows 目录联接 / 其它平台符号链接）；源不存在只记事件
    # capture_diff: auto                      # auto/on/off；auto 跳过普通 exec，cli-agent 或要验收的 job 采集
    # scope_discipline: auto                  # auto/on/off；agent job prompt 末尾追加「## 交付约定」
    # tracker_prime: nearest                  # 注入的 tracker-prime 来自哪个 tracker：nearest(默认，逐级向上找最近的)/repo(只找 cwd 所在 git 仓，不越仓顶层)/off(不注入)
    # knowledge_capture: auto                 # auto/on/off；要求汇报写「## 可复用经验」，交付时记为经验候选
    # language: en                            # 本项目的文本语言(覆盖 server.language)；注入的提示词段落与交付正文用它
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

项目目录匹配会解析 `host_path` / `container_path` 与当前目录的符号链接后再判断归属；项目目录尚不存在或无法解析时回落到清理后的字面路径。

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

与内置模板（claude / codex / opencode / suga / 各 `*-acp`）同名、只想改几项时，用 `extends: builtin`：以模板为底，写了的键整项替换（包括写成 `false` / `[]`），没写的沿用模板并随 gofer 升级而更新；它仍是你声明的 agent，不受探测结果影响；web 改它时只写回差异。worker.yaml 的 agents 同样支持。

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
    # image_input: path                # 工作台附图的送达方式(path|arg|none)。path = agent 自己按绝对路径读图（消息末尾得到 `[附图 n] 绝对路径`）；arg = 图片路径作为命令行参数随续接 job 的 argv 送给 agent（不在正文写路径，需要 image_args）；none = 收不到图。不写 = 内置表(claude/codex 及 -acp 变体=path, suga=arg, suga-acp=none, 未知=none); none 的会话在工作台里 📎 置灰。ACP 内联图片块只看 agent 自己声明的 promptCapabilities.image，与此项无关
    # image_args: [--image, "{{path}}"]  # image_input: arg 必填：每张图片展开一次（三张图就是 --image p1 --image p2 --image p3），须含 {{path}}、不得含 {{prompt}}；内置 suga 默认 [--image, "{{path}}"]
    # model_args: ["-m", "{{model}}"]   # --model 时插在含 {{prompt}} 的参数之前（prompt 是 `--prompt`/`-p` 的值时插在该 flag 之前）；须含 {{model}}、不得含 {{prompt}}；不写 = 内置(claude --model / codex -m / suga --model)
    # from_session_args: [--from, "{{from_session}}"]  # --from-session 时追加在 args 之后；须含 {{from_session}}；内置 claude 默认 [--resume, "{{from_session}}", --fork-session]
    # add_dir_args: [--add-dir, "{{dir}}:ro"]   # job 的 cwd 是【嵌套仓】（所在 git 仓顶层 ≠ 项目根）时追加，把项目根作为 {{dir}} 交给 agent；须含 {{dir}}、不得含 {{prompt}}；内置 suga 默认 [--add-dir, "{{dir}}:ro"]
    # read_only_args: [-s, read-only]   # --read-only 时追加（内置 codex / claude / suga 已有）
    # exit_keys: [/exit, enter]         # 取消交互 job 时顺序写入 TUI；enter/ctrl-c/ctrl-d/escape 是按键名
    # exit_grace_sec: 8                 # 等退出横幅的上限；超时强杀，状态仍 cancelled
    # session_inject: [--session-id, "{{session_id}}"]   # 能预分配会话 id 时用（内置 claude 已有）
    # session_store_glob: "{{home}}/.codex/sessions/*/*/*/rollout-*.jsonl"   # 从本机会话文件找会话 id
    # session_store_id_regex: '([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})'
    # fallback_agents: [omp]            # 供应商错误时改派的候选(有序)
    # max_concurrent: 2                 # 该 agent 同时在跑的 job 上限: 超出排队(queued); 0/不写=不限
    # stall_timeout_sec: 300            # 输出停滞窗口覆盖: 0 = 永不判停滞; 不写=用 server 的值
    # budget: { max_tokens: 500000, max_cost_usd: 5, max_turns: 80 }  # 默认花费上限; 各维 0/不写=不限
    # budget 里的 agent_tokens / agent_cost_usd 是交给 agent 自己执行的软预算（它自己收尾，不被杀），需下面的 agent_budget 映射
    # agent_budget: { tokens_args: [--budget-tokens, "{{budget_tokens}}"], cost_args: [--budget-cost-usd, "{{budget_cost_usd}}"], tokens_env: AGENT_BUDGET_TOKENS, cost_env: AGENT_BUDGET_COST_USD }  # 软预算怎么交给 agent：每维 argv 片段（追加在 args 之后，须含对应占位符）和/或环境变量名；cli-agent 优先 argv、acp-agent 优先环境变量；没映射的维度带值提交即 400；内置 suga 已有
    # env: { HOME: /home/x }            # 该 agent 的进程环境
    # transient_error_patterns: [...]   # 覆盖内置「瞬时错误」正则(不区分大小写)；内置含 at capacity|rate limit|429|503|stream disconnected|stalled: no output|流式返回中断 等(见「故障转移与健康度」)
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
    add_dir_args: [--add-dir, "{{dir}}:ro"]           # job 在嵌套仓里跑时把项目根追加为只读附加目录
    read_only_args: [--read-only]
    output_format: ndjson
    ndjson_stdout: final_text
    ndjson_stdout_path: result
    session_family: myagent
```

源会话不存在时这类 agent 应以非 0 退出并在 stderr 说明，job 失败，可原样重试。

**`add_dir_args`（嵌套仓）**：一个项目根下往往放着多个仓（工作区的 `gofer/`、`web/`、`.worktrees/` 等）。job 的 cwd 是其中一个**嵌套仓**（它所在 git 仓顶层 ≠ 项目根）时，agent 看不到项目根那一层的东西（工作区级规则、tracker、文档）；配了 `add_dir_args`（须含 `{{dir}}`，如 `[--add-dir, "{{dir}}:ro"]`）的 cli-agent 会追加该片段，`{{dir}}` 填**项目根**。cwd 就在项目根自己的仓里、没配这个字段、或 cwd 所在仓根本定不下来（没装 git / 不是 checkout）时都不追加；只读还是可写由模板自己写（内置 suga 用 `:ro`，外层工作区是要读的上下文不是第二个写入点）。同一段也追加在**续接 argv**（resume / 续接会话）之后，所以续投的 job 看到的和首轮一样。

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
  # language: zh-CN                    # gofer 生成文本的语言: en 或 zh-CN(也认 zh / zh_CN.UTF-8 / english 等别名，归一化为规范值)；影响注入给 agent 的提示词段落(验收标准 / 交付约定 / 时限提示)、交付请求正文、续接 / 自动续投 / 未提交续投的提示语、管家会话的首条简报与巡检提示里的动态部分，以及 agent 在 MCP tools/list 里读到的工具说明与报错(内建 MCP 读本机配置；瘦客户端暂用本机语言链)、`gofer repo init` / `repo migrate` 写进 AGENTS.md / CLAUDE.md 的托管说明块；项目可用 projects.<key>.language 覆盖；不写 = 暂用 zh-CN(以后版本默认改为 en，启动日志与 `gofer config validate` 会提示)；`gofer init` 按本机系统语言写入

  # path_view: host|container          # 执行视角(默认 host=用 host_path)
  # read_dir_allow: [D:/reference]      # 允许 job --read-dir 使用的额外绝对目录
  # callers: [...]                     # 多调用方鉴权 + 每个 caller 的权限(can_admin / can_answer)与限流
  # governance: {...}                  # 限流全局兜底、require_answer_capability、attach_origins
  # max_job_timeout_sec: 3600          # job --timeout 上限; 超出被截断并明示(结果带 timeout_limit_source/timeout_limit_key 指出是哪个上限; job run --strict-timeout 改为直接报错); 项目 max_timeout_sec 可覆盖
  # job_recover_window_sec: 120        # worker 断线后在途 job 停在 recovering 等重连的窗口; 0 = 断线即 failed
  # dir_lock: true                     # 同目录串行锁(见下); false = 所有 job 共享目录
  # stall_timeout_sec: 900             # 输出停滞窗口(见下); 0 = 全局关
  # worker_queue_max_wait_sec: 1800    # 远端 worker 满载时 job 的排队上限(见 worker-config.md); 0 = 不限
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
  #   messenger_max_messages: 50       #   处理满这么多条后在两条消息之间重建(清上下文); 0 = 不限
  #   messenger_max_age_sec: 7200      #   常驻进程存活上限, 满了下一条消息起新进程; 0 = 不限
  # agent_fallback:                    # 故障转移开关
  #   on_failure: true                 #   失败后转移(配了候选才生效)
  #   pre_dispatch: false              #   提交时主 agent degraded 就改派(默认 false)
  # agent_health: { window_sec: 3600, degraded_after: 3, recover_after_ok: 1 }
  # hold:                              # 待批 job 的等待时限; 热重载
  #   default_timeout_sec: 86400       #   --hold-timeout 0/不给时(默认 24h); 到期 → cancelled
  #   max_timeout_sec: 604800          #   允许请求的上限(默认 7d); 超过直接 400
  # xfer: { max_bytes: 268435456, collect_max_bytes: 1073741824 }   # 文件传输上限
  # attachments:                       # 工作台图片附件(PNG/JPEG/GIF/WebP, 按文件头判定); 0/未写 = 默认, 负数拒绝加载, 改文件 + 重载后对下一次上传生效
  #   max_bytes: 10485760              #   单张上限(默认 10MiB, 上限 100MiB), 超出 413
  #   max_per_message: 6               #   一条消息最多带几张(默认 6; 由发送侧检查)
  #   hourly_per_caller: 200           #   每个 caller 每小时最多上传次数(默认 200), 超出 429
  # notification:
  #   enabled: true                 # 出站通知总开关
  #   allow_hosts: [hooks.example.com] # 空列表一律拒绝 webhook；也可在 web 设置 → 通知编辑
  #   allow_http: false             # 仅本地测试需要开启
  #   max_attempts: 6
  #   max_text_runes: 3000
  #   session_reply_delay_sec: 120
  #   webhooks: [...]                  # IM 通知; 事件订阅见 IM 通知文档
  #                                    #   也可在 web「设置 → 通知」里增删、勾选事件、限定项目与发送测试
  #   desktop:                         # 桌面通知(server 所在主机弹系统通知); 整段不写 = 不启用
  #     enabled: true                  #   false = 暂停但保留配置
  #     events: [job.awaiting_approval, interaction.created, session.awaiting_reply, job.needs_review]  # 省略 = 这四个
  #     projects: [demo]               #   只对这些项目弹(省略 = 全部)
  #     merge_sec: 10                  #   同类事件合并成一条("3 个 job 等待批准")，0 = 不合并
  #                                    #   这一段同样可在 web「设置 → 通知」里改（开关 / 事件 / 项目 / 合并窗口），并可发一条测试弹窗
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
mail:                                  # 信箱投递器(见 sessions-relay.md 信箱); 0/未写 = 默认, 负数拒绝加载, 热重载生效
  # tick_sec: 30                       # 多久扫一次
  # wake_idle_sec: 300                 # idle/offline 会话 last_seen_at 早于此才发唤醒提示(急件 idle 时不看)
  # wake_interval_sec: 600             # 同一会话两次唤醒的最短间隔
  # deliver_by_sec: 86400              # 新信件的送达期限, 过期转「待指派」进收集箱
  # claim_lease_sec: 60                # 占用信件的租期(hook 取件与投递器的 suga send / job say), 未确认则回到 pending
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
  # mail_retention_days: 90            # 终态信件（delivered/answered/expired/failed/cancelled）保存期，默认 90 天；负数 = 不清理
  # attachment_retention_days: 14      # 工作台图片附件保存期(文件在 <config-dir>/attachments/)，默认 14 天；负数 = 不清理；新上传的按它算到期，清理循环只在启动时决定是否运行
```

- **token 从哪来（服务方式运行时重点）**：只有两处来源——`server.token` 字面量，或 `server.token_env` 指名的环境变量（`gofer init` 写 `GOFER_TOKEN`；**不写 `token_env` 就不读 `GOFER_TOKEN`**，没有内置缺省）。变量值按 dotenv 规则取：`<config-dir>/.env` 与工作目录 `./.env` 依次加载（后者覆盖前者），启动终端显式 export 的同名变量又覆盖两者。**注册成系统服务后不继承注册终端的环境**（unit / 计划任务只注入 `GOFER_CONFIG_DIR`），只读 `<config-dir>/.env` 与 config.yaml；**Windows 上任务计划程序还会把启动账户的持久环境变量（用户级 / 机器级）传给服务，它压过 `.env`**——用户级环境里一个旧的 `GOFER_TOKEN` 会让改 `.env` 完全无效。于是“注册前用终端 token 登录 web 正常、`serve start` 后 401”只需一个原因：两边不是同一个 token。`gofer serve register` 会比对并警告（不阻止注册；持久环境变量压过 `.env` 时单独一段并给出删除 / 改值命令 `[Environment]::SetEnvironmentVariable('<name>',$null,'User')`）；`gofer serve status` 的 `auth_token`（`service` / `current_process` / `match`，以及 `persistent_env` / `unread_dotenv`）与 `run/serve.log` 的 `server.token_source` 行给出来源与**掩码**（`len=<长度> <前两位>**<末位>`，不含 token 值）；`version_error` 是 401 时会直接说 CLI 用的 `GOFER_SERVER_TOKEN` 与服务端那份不一致（两边掩码）。要两边一致就把 token 写进 `<config-dir>/.env`（并清掉持久环境里的旧值）。
- 本机路径字段 `server.tls.cert_file`、`server.tls.key_file`、`server.web_dir`、`log.file`、`log.dir`、`storage.root`、`storage.db_path` 可在开头写 `{config_dir}`（由 `GOFER_CONFIG_DIR` 决定，缺省 `~/.config/gofer`；`-c` 只选配置文件，不改它）。`serve --web-dir` 与 `gofer tool cert --out-dir` 也支持。agent 参数、prompt、project 路径与 worker roots 不支持这个变量。
- **`/metrics`（Prometheus）**：不在 `/v1` 鉴权组里，带 caller / project / agent / runner 标签的 job 计数。`server.metrics` 整段不写 = 开启且免认证（老配置的行为不变），但 `gofer init server` 生成的配置写了 `enabled: false`。要抓取就设 `server.metrics: {enabled: true, token: "<长随机串>"}`，抓取器带 `Authorization: Bearer <token>`。`gofer config validate` 在「开启 + 没设 token + `server.addr` 不是回环地址（127.0.0.0/8、::1、localhost）」时给 WARN。
- IM 通知（钉钉 / 飞书 / 通用 webhook）与可订阅事件见 <https://github.com/inhere/gofer/blob/main/docs/runbook/im-notification.md>。
- **桌面通知**（`server.notification.desktop`）：server 在自己所在主机弹系统通知，默认订阅 `job.awaiting_approval` / `interaction.created` / `session.awaiting_reply` / `job.needs_review`，`projects` 过滤项目，`merge_sec`（默认 10s）把同类事件合成一条。整段不写 = 不启用；以服务（Windows session 0）、无桌面会话或被系统关掉通知时自动跳过，每种原因只记一条 `notify.desktop_skipped`，原因变化或恢复后再坏会重新记。**Windows 需要在「设置 → 系统 → 通知」里打开通知总开关与 Windows PowerShell**（gofer 借用它的 AUMID；被关掉时跳过日志的 `reason` 会给出设置值与修复路径）。平台行为见同上文档。
- 钉钉 / 飞书机器人的三种安全设置对应：**自定义关键词** → webhook 写 `keyword: <机器人里那个词>`（消息标题 / 正文都不含它时 gofer 自动把「<关键词> 」加到标题前，钉钉的 markdown 标题与正文标题行都带）；**加签** → `secret_env: <环境变量名>`（密钥值从 serve 进程环境读）；**IP 白名单** → 不用配置，在机器人侧填 server 出口 IP。被机器人拒绝（关键词没命中、签名错、token 失效）的投递直接记 `failed` 且不重试，`last_error` 是机器人返回的原文；限流仍按退避重试。

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

> `gofer` 命令自己的 `--help` 与提示按**调用方**的语言显示，来源链：`GOFER_LANG` 环境变量 → 系统语言(`LANG` / `LC_ALL`，Windows 取界面语言) → 缺省。`--json` 等机器可读输出不翻译。已支持的命令组:`job` / `plan` / `session`。

- 候选必须是 `agents` 里声明过的、非 exec、能跑批处理的 agent，加载时校验；提交时再按项目 `allowed_agents` 过滤（不在白名单的跳过并告警），解析结果随 job 冻结。单个 job 用 `job run --fallback omp,claude` 覆盖、`--no-fallback` 关掉。
- 触发：job 失败且命中瞬时错误模式、自己也续不了；验证步骤失败不算。动作：以普通 job 重提原请求（新会话、同一 cwd，prompt 加前缀说明只做剩余部分，标题加 `(→omp)`，继承 plan / tags / review / verify / todo 等）。
- 内置瞬时错误模式：agent 不写 `transient_error_patterns` 时，名字命中内置模板的 agent（key，或 `command` 的基名：`claude` / `codex` / `omp` / `suga`，它们的 wrapper 与 `suga-acp` / `omp-acp` 这类同 CLI 的 ACP 前端也按基名命中）自动带上内置正则——供应商限流与状态码（at capacity / rate limit / 429 / 503）、连接中断（connection reset / stream disconnected）、进程侧失败（windows sandbox failed / connecting runner pipe）、输出停滞（stalled: no output），以及**流式返回中断**（`parse ... stream chunk: unexpected end of JSON input`、`unexpected EOF` 这一类：供应商的流中途断了，agent 拿到的是解析错误）。流式那类只在错误附近同一行出现 stream / chunk 这类词时才算，普通的 JSON 解析错误（配置文件 / 会话文件损坏）仍按真失败处理，不会续投。其余 agent（含 `npx` 启动的 `claude-acp` / `codex-acp`）默认没有内置正则，要自动续投就自己写 `transient_error_patterns`（写成 `[]` 即显式关掉）。
- **agent 自带的错误分类优先于文本模式**：suga 等会给供应商错误打 `error_kind`（stream-json 的 `type=error` 行；ACP 则在 prompt 错误响应的 `data` 里）。只有 `provider_transient`（流中断 / 过载 / 408 / 429 / 5xx / 连接错误且 agent 自己的重试已用尽）才会自动续投；`provider_quota`（额度 / 用量上限）、`provider_rejected`（请求被拒）、`provider_bad_response`（非法响应 / 永久性 API 错误）与任何未知取值都不续投。额度类错误若带 `reset_at`，job 错误里会写明重置时间。没有 `error_kind` 的事件仍走上面的文本模式兜底，但含 usage limit / quota / insufficient_quota / limit resets at / HTTP 402 的文本一律不算瞬时错误（即使同时写着 rate limit / 429），续投只会再失败一次。
- **续接目录不一致**：续接始终在源 job 的工作目录里进行；若该目录已改动或被删除，suga 会拒绝在不同目录续接会话。gofer 不会替你加 `--allow-cwd-change`，而是把 suga 的提示原样写进 job 错误，按提示核对目录后手动处理。
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

## 决策 hook

在**决策点**上同步运行一条命令，按它在 stdout 输出的 JSON 决定放行、否决或**收紧**。与事件钩子不同，它在请求路径上，所以要快（默认 5 秒超时，上限 30 秒），出错时默认**拒绝**。

```yaml
server:
  decision_hooks:
    - name: <hook-name>                  # 必填，唯一
      points: [job.submit]               # 必填：job.submit（提交前）/ job.deliver（交付前）
      command: ["<interpreter>", "<script>"]   # 必填，argv 形式，不经 shell
      projects: [<project>]              # 空 = 全部项目
      # dir / env                        # 同事件钩子；env 的值不出现在任何 API 读视图
      # timeout_sec: 5                   # 默认 5，上限 30
      # on_error: deny                   # 脚本出错（启动失败 / 超时 / 非 0 退出 / 输出不合法 / 越权补丁）时：deny（默认）| allow
      # include_internal: false          # gofer 内部 job（交付、tracker 同步等）默认不问
      # enabled: true
```

- **输入**：stdin 一个 JSON 对象：`{version, point, hook, project, caller, job}`（`job.submit`）或 `{version, point, hook, project, delivery}`（`job.deliver`，含模式、分支、PR 标题与正文、提交列表、改动统计）。
- **输出**：stdout 一个 JSON 对象，退出码 0：`{"decision":"allow"}`、`{"decision":"deny","reason":"…"}`，或 `{"decision":"allow","patch":{…}}`。
- **只收紧，不放宽**。`job.submit` 的 patch 只能：`review: true`、`hold: true`（+ `hold_reason`，改成待批 job）、`read_only: true`、调低 `timeout_sec` / `budget`、`tags_add`、`title`、`acceptance_append`。`job.deliver` 只能：改 `title` / `body`、`draft: true`、`labels_add`。改 agent、runner、命令、目标分支等一律视为出错，按 `on_error` 处理。
- **结果**：提交被否决返回 403（`denied by decision hook <name>: <reason>`），不建 job；交付被否决记为交付失败并通知。每个 hook 的裁决记成 job 事件 `job.decision_hook`。
- 多个 hook 按配置顺序串行，任一否决即停；后一个看到的是前一个收紧后的请求。
- 待批 job 批准后执行、worker 收到派发的 job 时不再问 hook；hook 也不能代替人批准待批 job 或验收 job。`job deliver --dry-run` 不调用 hook。
- **只能改配置文件**，同事件钩子；环境同样去掉凭据变量，加 `GOFER_DECISION_POINT` / `GOFER_HOOK_NAME`。
- 自测：`gofer tool hook-test <name> [--point <p>] --input sample.json` 用样例输入跑一次，打印裁决与 gofer 会怎么处理，不提交任何东西。协议与样例见 <https://github.com/inhere/gofer/blob/main/docs/runbook/decision-hooks.md>。

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

## job 密钥（--secret）

job 需要令牌（推送、开 PR、调外部 API）时，按名字引用 server 上保管的密钥，值不进请求、不进日志：

```yaml
secrets:                         # 顶层；只写「去哪里取」，值不在配置里
  <name>:
    env: <VAR>                   # 执行机器上 gofer 进程的环境变量（可放 <config-dir>/.env）
    as: <JOB_VAR>                # job 里的变量名；省略 = 名字转大写，- 和 . 换成 _
    description: <一句话>
projects:
  <project>:
    secrets: [<name>]            # 这个项目的 job 可以引用哪些
```

- 提交：`gofer job run … --secret <name>`（可重复 / 逗号分隔；任务书 frontmatter 写 `secrets: [...]`；MCP `gofer_run_job` 的 `secrets`）。名字未声明、项目不允许、变量在 server 上未设置或值短于 4 个字符，提交即被拒并写明原因（不带值）。
- 执行：值作为 `as` 变量注入 job 环境（不受 job 环境拒绝列表影响）；job 的 stdout / stderr 写盘时把值替换成 `***`，结果、通知、PR 正文都读脱敏后的日志。job 事件 `job.secrets_granted` 记录用了哪些名字。
- 只支持 server 本机 runner；不能和 `--interactive` 一起用。续接、重跑、重试、故障转移沿用原来的名字，执行时重新取值。
- `secrets` 与 `projects.<p>.secrets` **只能改配置文件**（web 与配置写接口不能改），`env` 不能是 `GOFER_` 开头的变量。`gofer config validate` 对本机未设置的变量给 [WARN]。
- 脱敏只管 gofer 自己的记录：job 写进工作目录的文件、发到网络上的内容不在范围内。

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
      job_link: false           # PR 正文附 gofer web 的 job 链接（需 server.web_base_url）；web 常在内网、PR 可能公开，默认不附
      proxy: ""                 # 推送与平台 API 走的代理（http:// / https:// / socks5://）；空 = git 对该 remote 的 http.proxy，再退到代理环境变量
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

## 检查更新

server 定时检查是否有新版本（默认每 2 小时，启动约 1 分钟后先查一次）；web「设置 → 关于」显示当前 / 最新版本、本平台下载链接与 GitHub 仓库地址，并可点「检查更新」立即查（有新版本时「立即更新」按钮就在它右边，窄屏整组按钮换行）。有新版本时 web 顶栏显示 `↑ <版本>`，并发 `update.available` 通知（默认触发事件，每个版本一次）。

```yaml
server:
  update:
    # enabled: true            # 定时检查；false 只关定时，手动检查仍可用
    # interval: 2h             # 最短 10m
    # proxy: http://<host>:<port>   # 只给更新用的代理；空 = 跟随 http_proxy 块 / HTTPS_PROXY 等环境变量
    # repo: inhere/gofer       # 默认来源：GitHub 最新 release
    # 内部 mirror：配了 latest_url 就不再查 GitHub
    # latest_url: "http://mirror.example.com/tools/{name}/latest.yaml"
    # url_template: "http://mirror.example.com/tools/{name}/{name}-{os}-{arch}{ext}"
```

- 占位符：`{name}`（`gofer`）、`{os}` / `{arch}`（server 的系统与架构）、`{ext}`（Windows 为 `.exe`）、`{version}`（最新版本号）。
- 下载链接：mirror 按 `url_template` 指向**裸二进制**（`make build-all` 的产物，如 `gofer-linux-amd64`、`gofer-windows-amd64.exe`）；GitHub 取 release 里 `gofer-{os}-{arch}` 开头的**压缩包**（`.tar.gz` / `.zip`；包内只有一个文件，名为 `gofer`（Windows 为 `gofer.exe`），更新时也兼容旧版压缩包里带平台后缀的名字）。`latest.yaml` 读 `version` / `released_at` / `description` 与 `files`（各平台二进制文件名 → sha256，`make latest-yaml` 生成）；GitHub release 也带一份 `latest.yaml`。server 自己下载 GitHub 资产（`latest.yaml`、一键更新的二进制）走 `api.github.com` 的资产地址，不依赖 `github.com` 下载链接可达。
- 版本按 `X.Y.Z` 比较；开发构建（`0.152.3-2-gabc`）不会把同一个基础版本报成新版本。
- 只能改配置文件（server 会去请求这些地址）；改完 `gofer serve reload` 生效。发现新版本时 serve 日志记 `update.available`。
- 接口：`GET /v1/update`（最近一次结果）、`POST /v1/update/check`（立即检查；job 凭据不可调用）。

### 一键更新

以服务方式运行的 server（`gofer serve register`）可在「设置 → 关于」点「立即更新」：

- 确认框列出运行中的 job 数和 worker（默认勾选全部在线的），并可选最长等多久（30 分钟 / 1 小时 / 2 小时 / 4 小时，默认 2 小时）；勾选的 worker 在 server 更新后一同升级。
- server 下载本平台二进制（有 sha256 就校验，并核对 `--version`），然后**等机器空闲**：
  - 运行中的 job（本机的）跑完才重启；worker 上已经在跑的 job 不算，也不受影响（worker 进程留着它，server 回来后再接管）。
  - 等待期间**新提交的 job 照常接收，但先停住**（非终态 `paused_for_update`，不占执行位）——不会出现 503；更新完成 / 失败 / 被取消时自动开跑，job id 不变，不需要重新提交。gofer 自己的短内部 job 不停：会话送达 / 注入（`relay-deliver` / `relay-inject`）与传话人（`session-messenger`）照常跑——停住它们只会丢掉要送的消息，而重启并不差这几秒。
  - 等待期间 web 转达给会话的消息即使内部 job 没跑完也不会再被记成「失败」：状态是「送达中」，job 结束时回写真实结果（gofer-qzv5）。
  - 到最长等待时间还没空闲就放弃本次更新（停住的 job 立即放行）；等待期间可在关于页点「取消等待」（接口 `DELETE /v1/update/apply`，管理员）。
- 空闲后把二进制交给 `gofer serve upgrade`（内部 `--drain-timeout` 用较短的 2 分钟：刚刚已经等到空闲）替换并重启，失败自动回滚。
- 新 server 起来后**先把停住的 job 全部放行**，再逐个升级勾选的 worker：同平台用 server 自己的新二进制，其他平台用同一来源下载的该平台二进制。
- 进度在关于页显示（也在 `GET /v1/update` 的 `apply`）：等空闲时列出还在挡着重启的 job（可点进详情）、已停住的 job 数与放弃时间；`<config-dir>/run/update/` 下有下载的文件、`apply.json` 与 `serve-upgrade.log`（失败时错误信息就从它尾部取）。
- 普通 `gofer serve`（前台 / `-d`）不能自我替换：关于页的「更新」节给出一个警示框——先说明这个 server 不是以服务方式运行、所以没有一键更新按钮，再分两段写清做法：
  - 「现在手动更新」：下载二进制替换后重启 `gofer serve`；worker 用 `gofer worker upgrade <id>`（其他平台由 server 从下载源 `server.update` 自动取该平台的发布包，也可以加 `--file <该平台二进制>`）。
  - 「改成服务（之后可在这里一键更新）」：① 停掉当前前台运行的 `gofer serve`（否则端口冲突）；② `gofer serve register`（默认只登记、不启动，加 `--start` 一步注册并启动）；③ `gofer serve start`；④ `gofer serve status` 确认 `health=healthy`。命令等宽显示、可点旁边按钮复制；全部选项见 `gofer serve register --help`。

## worker 自助注册（worker_enrollment）

默认关闭。开启后，worker 机器可以向 server 提交注册申请，由有 `worker.manage` 权限的人核对配对码后接受或拒绝；接受时才登记 worker（等同 `worker add`），token 只交给发起申请的那一端。申请的接口和 `gofer worker join` 命令随同一个版本提供；本节只说明服务端配置。

```yaml
server:
  worker_enrollment:
    enabled: false         # 总开关；关闭后不再接受新申请
    decide_sec: 120        # 管理员决定窗口，30-600
    collect_sec: 60        # 接受后申请方领取窗口，30-600
    per_source_per_min: 3  # 每个来源地址每分钟最多发起次数，1-60
    max_pending: 20        # 同时未决的申请上限，1-200
    trusted_proxies: []    # 反向代理的 CIDR，如 ["127.0.0.1/32"]
```

- 整段可热加载（`gofer serve reload`），每次请求读当前配置：切换 `enabled` 立即生效。关闭时新申请被拒绝，但已有的申请仍会按期过期；已接受而未领取的登记在领取窗口结束后自动撤销。
- `trusted_proxies` 只能在配置文件里改，控制台不可编辑。来源地址默认取连接地址；只有连接来自列表里的代理时才采信转发头。server 前面没有反向代理就留空。
- 其余字段可在控制台修改。取值越界或 CIDR 写错，`gofer config validate` 与加载都会报错。
- 申请记录的保留期为 30 天，过期后自动清理；申请与决定都会写入审计事件。

## 人与 agent 分开 token（callers + governance）

只配了 `server.token` 时，所有用这个 token 的客户端（web、人的 CLI、直接连 server 的 agent 会话）是同一个调用方 `default`，权限相同。给 agent 单独一个 token，并打开能力闸门：

```yaml
server:
  token_env: GOFER_TOKEN            # 人用的 token = 调用方 default
  callers:
    - id: default                   # 不写 token：只给上面的 server token 声明能力
      name: admin                   # 可选显示名：界面与 whoami 显示 admin，id 仍是 default
      can_admin: true               # 根管理员：持有全部权限，与开关无关
      can_answer: true              # 已弃用：仍是配置推出的只读授权
      can_attach: true
      can_tunnel: true
    - id: agent-dev                 # agent 会话用的 token
      token_env: GOFER_AGENT_TOKEN
      max_concurrent_jobs: 6        # 同时在跑的上限（超出排队）
      rate_limit: 2                 # 每秒提交次数
  governance:
    require_admin_capability: true  # 改配置 / 项目 / 规则 / skill、升级与 reload worker、一键更新
    require_attach_capability: true # 接入交互终端
    require_tunnel_capability: true # TCP 隧道
```

- 权限改成一份固定名称的目录（`job.submit`、`tunnel.open`、`config.write`……），每条 `/v1` 路由声明它需要哪些权限。**`can_answer` / `can_attach` / `can_tunnel` 与四个 `require_*` 开关已弃用**（三个版本后移除）：留着照旧生效（它们就是「配置推出的只读授权」），启动日志对每个仍在用的键给一条「请改用 web 授权」的提示。
- **`can_admin` 是根管理员**，它不在弃用之列：持有目录里的全部权限，与四个开关无关 —— 开关打开时也能答复 / 验收 / 开隧道 / 接入终端，开关关闭时也能代答他人终端会话、回答他人终端的工具权限提示、attach 无主 job。只声明 `server.token`、不写 `callers` 的部署里那个 `default` 就是根管理员；`allow_empty_token` 且没有 callers 的部署没有根管理员（那里的权限只读，没人能管理授权）。
- 开了某个 `require_*` 时，只要有根管理员就能通过校验（不再要求「至少一个调用方带对应能力位」）；既没有带对应能力位的调用方、也没有根管理员才会被拒绝（防止把自己锁在外面）。
- **`name` 是可选显示名**（只改显示）：job / 授权 / 审计里记的仍是 `id`，权限判定也只认 `id`，没有数据迁移。加载时去首尾空白，非空时在所有 caller 间唯一（忽略大小写）、至多 64 个字符、不含控制字符，否则配置校验拒绝。主 token 的调用方 `default` 的显示名写在 `id: default` 那一项上。`gofer auth whoami` 与 web（设置 → 权限、配置页的 callers 表）显示名称，悬停可见 id；没配 `name` 就显示 id。
- 调用方 id 与 worker id 不得重名：`server.callers[].id` 撞上 `server.workers` 的键时配置校验直接拒绝。
- agent 一侧把 `GOFER_SERVER_TOKEN` 换成 agent 的 token；人的 web / CLI 不用改。
- callers / governance 需重启 server 生效。
- 各项权限的含义、风险、来源，以及每条 HTTP 路由需要哪些权限：见 <https://github.com/inhere/gofer/blob/main/docs/reference/permissions.md>（由服务端代码生成，与二进制一致）。
- **审计与自查**：授权增删、只靠会话授权通过的请求和被拒请求会进入 `GET /v1/auth/audit`；使用计数保留在授权行，审计记录保留 90 天。`gofer auth whoami` 显示本 token 是哪个调用方、是否根管理员、持有的每项权限及**来源**（`root` 根管理员 / `config` 由配置推出 / `db` 数据库授权，并给出是哪条授权给的）；权限不够的 403 正文里让人跑的就是它。`gofer auth routes` 列出服务端每条路由的声明与需要的权限（与上面那份生成文档同一份数据，但来自正在跑的那个二进制）。
- **未知的 `/v1` 路径**：`/v1/` 下没有匹配的路由（含已删除的旧接口、拼错的路径）一律返回 404 + JSON 错误体（`error` / `detail`），不再回退到 web 首页 HTML；web 前端路由（非 `/v1`）仍回退 `index.html`。客户端遇到 404 JSON 先核对 server 版本与路径。
- **授权可以落库、按主体发**：一条授权 = 主体（`users` 全体用户类调用方 / `caller:<id>` 某个调用方 / `session:<sid>` 某个终端会话）+ 名称模式（`tunnel.*`、`job.*.others`、`*`），存数据库、立即生效。升级后行为不变（原来不检查的那组仍发给全体），能新加的是「单独发给某个调用方」（如只给容器里的会话约 `tunnel.open`）。接口：`GET/POST /v1/auth/grants`、`DELETE /v1/auth/grants/{id}`（撤销，行留着）、`POST /v1/auth/grants/{id}/activate`（恢复孤儿）、`POST /v1/auth/grants/{id}/renew`（把仍然生效的会话授权往后延，上限 24 小时）；只有**根管理员**能管理授权（`auth.manage` 是任何模式都匹配不到的保留权限），worker / job 凭据一律 403。
- **网页管理**：根管理员在 web「设置 → 权限」里查看目录与审计、新增 / 撤销授权、恢复孤儿授权；不是根管理员或没有根管理员的部署，该页只读。
- **撤销或到期会断开靠它的长连接**：隧道、attach 终端、SSE 日志流、`/v1/ws` 推送、托管转发在建连时按「调用方 id + 依赖的授权」登记，权限没了就被服务端断开（撤销与配置重载当场，到期与会话结束靠周期重判，最长 10 秒）；同一项权限另有来源的连接重新判定后保留。`/v1/ws` 的订阅也按读权限过滤：`job:<id>` / `jobs` 需要 `job.read.others`（自己的 job 除外），`sessions` 需要 `session.read.others`。
- **会话主体（`session:<sid>`）**：只对那一个会话生效，有效期 1 / 4 / 24 小时（最长 24 小时；「到会话结束」也带上限）。生效条件是请求带了该会话 id、发起人是会话当前 owner、会话化身没变、会话没结束、授权未撤销未过期；会话结束 / 结束后被接管 / 删掉后重新登记都会换新化身，旧授权不再生效。到期前可以续期（`POST /v1/auth/grants/{id}/renew`，CLI `gofer auth grant renew <grant-id> --for 4h`），只把到期时间从「现在」往后挪、上限仍是 24 小时；已撤销 / 已过期 / 会话已结束 / 归属或化身变了的不能续期，要重新授权。CLI 请求的会话头来自环境：`$SUGA_SESSION_ID`（suga 会话）最优先，其次显式的 `$GOFER_SESSION_ID`，否则 `$CLAUDE_CODE_SESSION_ID` / `$CODEX_THREAD_ID` / `$CODEX_SESSION_ID` 去重后恰好一个才带，其余情况不带；这几个变量在 job 环境的禁止清单里（job 里的 `gofer` 不认会话）。怎么用、怎么排错见 [sessions-relay.md](sessions-relay.md)「会话授权」。
- **孤儿授权**：某个调用方从 `server.callers` 里删掉后，发给它的授权会被标为孤儿并停用；同一 id 以后重新出现也仍保持停用（换了人的同名 token 不该继承旧授权），要根管理员 `activate` 才恢复。授权缓存是版本号 + 5 秒 TTL：本进程写入立即生效，其他进程 5 秒内生效。
- gofer 派出的 job 本来就不继承 server token，用的是只对该 job 有效的凭据；worker 连接用各自的 `server.workers.<id>` token。这里拆的是 job 之外直接连 server 的客户端。
- 拆分防的是误操作：能在 server 本机 runner 上提交 `exec` 的调用方，仍等于能在主机上执行命令。
- **切 token 后旧会话会被拒**：会话的 owner 在首次登记时定死，之后换身份的注册 / 心跳 / 回复一律 403，web 上会话的状态、最新消息、进度停在切换那一刻。会话对象会带 `refused_caller_id` / `refused_at`，会话列表与详情显示「归属被拒」并给「转给 <caller>」按钮，CLI 用 `gofer session owner <sid> <caller>`（需 `session.transfer`）；转移后该 caller 的上报立即生效。详见 [sessions-relay.md](sessions-relay.md)。

## 出站代理（http_proxy）

server 自己发出的请求——检查更新与下载、skill 导入、通知 webhook、Web Push、交付（开 PR 等）的 API——访问 `allow_sites` 里的站点时走 `proxy_url`，其余站点照旧跟随 `HTTPS_PROXY` / `NO_PROXY` 环境变量。适合「只有访问 GitHub 需要代理」这类网络。

```yaml
http_proxy:
  proxy_url: http://<host>:<port>     # http / https / socks5
  allow_sites:
    - github.com                      # 含子域名（api.github.com）
    - "*.githubusercontent.com"       # 只含子域名；release 资产下载会重定向到这里
```

- `allow_sites`：`example.com` 匹配它及其子域名，`*.example.com` 只匹配子域名，`*` 匹配全部；省略 = 全部站点都走代理。
- 更具体的设置优先：`server.update.proxy`（只管更新）、交付配置的 proxy。
- 只能改配置文件；改完 `gofer serve reload` 立即生效（每个请求读当前配置）。

## 调用方上限（ceiling）

给一个 token（一个调用方）划定它能做什么、能碰到哪些项目。上限写在配置文件里、只能改文件，是「只减不加」的一层：调用方的实际范围 = 它已有的权限 ∩ 上限，不写 `ceiling` 的调用方行为完全不变。

```yaml
server:
  callers:
    - id: ci-bot
      token_env: GOFER_TOKEN_CI
      ceiling:
        projects: ["app-*", "docs"]   # 可访问的项目（glob）；省略 = 不限
        runners: ["w-linux-*"]         # 可提交到的 runner（glob，含 local / server）；省略 = 只允许 worker
        workers: ["w-linux-*"]         # 可被路由到的 worker（glob）；省略 = 不限
        agents: [codex, "claude*"]     # 可用的 agent（glob）；省略 = 不限
        allow_exec: false              # exec 类作业（agent exec / cmd / verify / hooks）；省略 = 不允许
        global_memory: false           # 写全局（非项目范围）记忆；省略 = 不允许
        read_only: false               # 只读：拒绝一切改状态的请求；省略 = 否
```

每个字段是一条 glob 名单（`path.Match` 语义，`*` 不跨 `/`）或一个开关：

| 字段 | 省略 | 写明 |
|---|---|---|
| `projects` | 不限 | 只允许匹配其中之一的项目；名单非空时，没有项目归属的资源（空项目的会话 / 工作项 / 计划、隧道）也算范围外 |
| `runners` | **不含本机 runner**，即只允许 worker runner | 只允许匹配的 runner（比较前先归一化拼写，`local` 与 `server` 等价） |
| `workers` | 不限 | 只允许路由到匹配的 worker（含自动选中的那台） |
| `agents` | 不限 | 只允许匹配的 agent（续接按被续接的那个 agent 判定） |
| `allow_exec` | 不允许 | `true` 才允许 exec 类作业 |
| `global_memory` | 不允许 | `true` 才允许写全局（非项目范围）记忆 |
| `read_only` | 否 | `true` 拒绝一切改状态的请求 |

- **默认更严**：写了 `ceiling` 就等于把本机 runner 与 exec 关上了 —— 要用必须显式写 `runners: [local]`（`server` 同义）/ `allow_exec: true`。**本机 runner 只认名字**：`runners` 里的 glob（`*`、`w-*` 这类）永远不会匹配它，只有写出 `local` / `server` 才算允许。**本机 runner 等于主机 shell**：在那上面跑的 job 就在 server 主机上执行、能读到 server 的配置与全部 token，所以显式放开本机 runner 时加载会给一条警告（`gofer config validate` / `config info`、启动日志、`GET /v1/config` 的 warnings 都能看到），那种情况下上限不构成安全边界。
- **空列表的含义**：`projects` / `workers` / `agents` 为空 = 该维度不限制；`runners` 为空 = 只允许 worker runner（不含本机）。列了名单的维度要求请求里的值命中其中之一；该维度没给值的请求会被拒（失败关闭）。
- **exec 的口径**：`allow_exec: false`（或省略）拒绝 `agent: exec`、任何 `cmd`、`verify` 步骤与 agent 的额外 argv；与 runner 是否远程无关。
- **管到哪里（直接与间接）**：上限跟着**调用方**走，不只管它亲手提交的那一次。提交时记下这次提交的**上限所有者**（发起它的那个调用方），派生出去的作业沿链继承，所以下面这些都和它自己提交一样受限：
  - 它的 job 里的 agent 用 **job 凭据**提交的子 job，以及用 job 凭据做的读取（读范围外得 404，写仍按 job 凭据自己的白名单）；
  - 它建的**定时任务**到点与 webhook 触发；
  - 它建的 **plan / todo** 派发（条目的作者与派发者都按各自的上限检查）；
  - gofer 替它做的**自动续投、失败转移、唤醒续跑、重试、hold 批准后的重投**；
  - 它触发的**内部作业**（会话中继、交付、tracker 同步等）。
  - 取不到上限所有者（源 job 已被清理、job 行读不出来）时**拒绝**，不当作无限制。取消、验收他人 job 这类不提交新 job 的动作由权限与审阅决定，不在上限里。
- **严格解码**：`ceiling` 子树里的键名写错就加载失败（报出键名与位置），别的配置键仍照旧宽松；`ceiling: {}`（一个字段都没写）直接报错 —— 免得看着像限制了其实没有。
- **不能给根管理员写上限**（`can_admin: true` 的调用方）：根持有全部权限、与配置无关，写了也不生效，加载直接拒绝。
- **改文件与生效**：只能改配置文件（控制台写接口不提供这个字段）。给**已在配置里**的调用方改 `ceiling`，`gofer serve reload -c <config>` 后判定即用新值（每次判定读当前配置快照）；**新增**调用方（一枚新 token）要重启 serve —— `server.callers` 属重启级，token 集合与取 token 值所需的环境在进程启动时确定。
- **自查与观测**：`GET /v1/auth/whoami` 返回本 token 的 `ceiling`：`projects` / `runners` / `workers` / `agents` 是配置里写的名单（没写的维度不出现），`allow_exec` / `global_memory` / `read_only` 是**生效值** —— 省略即已按更严的默认解析成 `false`（`runners` 缺省表示不含本机）。`gofer auth whoami` 在输出末尾的 ceiling 段列出同样的内容。serve 启动与每次 reload 打一行 `caller ceilings: N restricted caller(s): <id>, ...`（日志里同名事件带 `count` 与 `callers`）。
- **被拒时的表现**：
  - **提交**：403，错误里写明调用方与是哪个维度（项目 / runner / worker / agent / exec），如 `caller ceiling denied: caller "ci-bot": runner "server" is not in the caller's ceiling`。
  - **范围外资源**：404，与「不存在」同形（读和写一样，也不透露上限的存在）—— 项目、job、plan、todo、定时、会话、工作项、tracker 仓、记忆等按记录归属判定。
  - **只读**：所有改状态的请求在路由门 403（`caller ceiling`），个人偏好类写入（推送订阅、工作台布局、已读标记、`ws-ticket` 等）例外；没有项目归属的服务器级写操作（配置、管家、隧道、worker 管理等）对受限调用方一律 403。web 控制台按同一口径把会 403 的按钮灰显（顶栏「只读」标记；今天页的决策操作与「专注处理」、计划详情的状态 / 链控件 / leader 开关 / 交接说明、工作项的整理与合并建议、issue 的新建 / 保存 / 评论、信箱回信等，悬停说明缺的是只读身份还是某个权限），只留查看与个人偏好操作（如「稍后」）。
  - **列表**会丢掉范围外的行；聚合与计数见下面的已知限制。
  - 上限拒绝是**永久**的，不会排队等下一次：gofer 自己替它发起的后续动作——自动续投、失败转移、唤醒续跑、重试、plan/todo 派发、定时到点——一律**到此为止**：重试行被取消、todo 上写「派发失败：原因」、唤醒被消费掉不再重复触发、定时那一次运行丢掉（**定时本身保留**，下次到点照常）。源 job 的失败原因不变，它的时间线（web job 详情）多一条 `job.ceiling_denied`，写明是哪条路径被拒和原因；定时丢掉的那次运行记在定时自己的事件流（作用域 `schedule:<id>`）上的 `schedule.run_failed`（到点、webhook 触发与 run-now 都记；`gofer schedule events <id>` 或 `GET /v1/schedules/{id}/events` 查看），serve 日志里同时有一条 `schedule <id> submit failed: caller ceiling denied: …`。所以「上限收紧后旧任务不再自动往下跑」是预期行为，不是卡住。
- **受限调用方看到什么**（聚合与推送按范围）：
  - `GET /v1/today`、`/v1/today/handled`、`/v1/today/lanes` 只含范围内项目的卡片、已处理行与 lanes，lanes 表头计数按留下来的行重算；顶部 digest（标题 / 正文与「自上次打开」的完成 / 失败 / 提交计数）也只基于范围内的 job 与工作项；状态条的 runner / worker 行按 `runners` / `workers`，正在运行的 job 数按范围重算，**当天用量与管家那一块不返回**（字段省略）。`GET /v1/today/snoozed` 同样只列范围内的「已稍后」卡片。
  - `GET /v1/stats` 只统计范围内的 job / 定时 / 待处理升级，`projects` 是范围内的项目数；`db` / `sessions` / `usage` / `session_usage` 这四个服务器级块**整体省略**（不是给 0）。`GET /v1/stats/overview` 同样只统计范围内的 job。
  - `GET /v1/meta` 的项目、worker 与 runner 列表只含范围内 / 允许的条目；`GET /v1/work-items/digest` 只统计范围内的工作项（管家点评不返回），**POST**（会向服务器级 webhook 发送）对受限调用方 **403**。
  - 管家相关路由（`GET /v1/steward`、`GET|PUT /v1/steward/notes`、`POST /v1/steward/review-summary`、`POST /v1/steward/ask`、`POST /v1/steward/review`、`start` / `stop` / `restart`）都作用于服务器级的那一个管家，没有项目归属，对受限调用方一律 **403**。
  - `GET /v1/jobs` 的多项目上限（含通配符）直接下推到查询：第一页是满页的范围内 job，`limit` / `offset` 在范围内分页，不会出现「先分页再丢掉范围外的行」导致的短页。
  - `/v1/ws`：范围外的 `job:<id>` 主题按「不存在的主题」拒（`error` 帧列出）；`jobs` / `sessions` 等失效通知按项目过滤；`stats` / `pending` 快照按本调用方的范围单独计算（每个范围一份缓存）。
- **审计**：上限拒绝（只读 / 服务器级写操作的 403、范围外的 404、提交时的 `caller ceiling denied`）写进授权审计（`GET /v1/auth/audit`，web「设置 → 权限」的审计列表），动作为 `request.ceiling_denied`，记调用方、请求、资源类型与 id、原因；同一（调用方, 原因类别, 资源类型）每分钟只记一条（按进程计），循环重试的 agent 不会刷满审计表。
- **配置页显示上限**：`GET /v1/config` 的 callers 行带该调用方的 `ceiling`（与 whoami 同一视图；没写上限的调用方没有这个键），web 配置页的 callers 表随之显示。
- **已打开的长连接会被复核**（每 10 s 一轮，改授权、改配置、交给别人时立即复核）：SSE / attach 按资源的项目、隧道与托管转发这类没有项目的连接在调用方有上限时断开、只读上限断开写型连接；`/v1/ws` 记下建连时的上限，上限收窄 / 放宽 / 变只读后断开（客户端会重连并按新范围重新订阅）。想让某个调用方的连接立刻全断，轮换它的 token 即可。
- **审阅（accept / reject / approve）**：自己的 job 只需 `job.review`；**他人提交的** job 还需要 `job.review.others`（403 文案会写缺哪个）。受限调用方不能审阅「上限所有者是自己」的 job（即使 job 是别人提交的）—— 机器人不能批自己的活，403 说明原因；未受限调用方不受这条限制。hold 批准后重投的 job 仍按**原提交者**的上限准入，上限所有者不会变成点批准的人。
- **当前版本的已知限制**：
  - 实时推送：不带项目的失效通知（批量变更）仍会触发客户端重拉，重拉得到的是范围内数据；一个谁都归属不到项目的 `job:<id>` 在订阅后仍会收到增量（订阅本身已经按范围把关）。
- **回滚**：旧版本会忽略 `ceiling`，所以换回旧二进制前先撤销或轮换受限调用方的 token；job 上记上限所有者的新列被旧版本忽略，可以直接回退。

## 校验与生效

```bash
gofer config validate server           # 校验路径 / agent / runner / 事件钩子与决策 hook；钩子命令在本机找不到、工作项摘要或管家开启却没人订阅时给 [WARN]
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
