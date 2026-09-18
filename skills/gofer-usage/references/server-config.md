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

```yaml
projects:
  my-project1:
    host_path: D:/projects/my-project1        # 逻辑路径(server 视角; 也是下发给 worker 的 host_path)
    container_path: /work/projects/my-project1 # 容器执行视角(server path_view=container 时用)
    default_agent: codex
    allowed_agents: [codex, claude, exec]      # 准入白名单(空=放行全部)
    # agent_fallbacks: {codex: [omp, claude]}  # ★ 该项目的故障转移候选(SUP-01 P3): 按"挂掉的 agent"给有序候选,
                                               #   覆盖 agent 级 fallback_agents(整份替换, 不合并); 空列表 = 该 agent 不转移

    allowed_runners: [local, builder]          # ★ 见下 —— 决定派给谁
    allow_exec: true
    allow_interactive: true                    # pty/交互 job 的项目级开关(默认 false); 项目侧唯一的交互闸
                                               # (旧字段 interactive_allowed_agents 已移除: 旧 yaml 里非空列表
                                               #  且未写本开关 → 加载期当作 true 并 warn, 请改写)
    max_concurrent_jobs: 4                      # 该 project 并发上限(0/不写=无限)
    # max_timeout_sec: 7200                     # 该项目 job 超时上限(秒), 覆盖 server.max_job_timeout_sec(可高可低)
    # worktree_default: true                    # 该项目 job 默认在受管 git worktree 里跑(= 每个 job 都 --worktree)
    # capture_diff: false                       # 关 git-diff 抓取(不写=cwd 是 git 树时默认开)
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

- 声明一个 `type: worker` 的 runner 才会：① 让该 worker 出现在 `/v1/runners` 名册（Web 可见）；② 可被 `--runner <名>` 派发。
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
    args: [exec, "{{prompt}}"]          # 批处理 argv(job run); 模板: {{prompt}} {{cwd}} {{job_id}} {{result_dir}}
    interactive_args: []                # pty argv(job run --interactive); [] = 裸 TUI; 有此字段 = 支持交互
    detect: { command: codex, args: [--version] }   # 探测本机是否装了
    # fallback_agents: [omp]            # ★ 供应商错误时改派的候选(SUP-01 P3): 有序, 逐个尝试
    # transient_error_patterns: [...]   # 覆盖内置的"瞬时错误"正则(不区分大小写); 内置含 at capacity|rate limit|
                                        #   429|503|stream disconnected|windows sandbox failed|connecting runner pipe 等
  exec:
    type: exec                          # 内置; 跑请求给的 argv, 不用模板
```

🔴 **一个 key 两种模式**：`args` = 批处理，`interactive_args` = pty；两者都写就是双模（内置模板的 claude/codex 已默认双模，但**自定义同名 agent 是整体覆盖**，要自己写 `interactive_args`）。旧写法 `interactive: true` = "仅交互、args 即 pty argv"（tty-claude 之类），这种 agent 普通 `job run` 会被拒 `has no batch mode`；`interactive: true` 配 `{{prompt}}` 是配置错误，serve **启动即拒**。四种组合与校验规则见 `config/gofer.example.yaml` 的 agents 注释。

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
  # agent_fallback:                    # ★ 故障转移开关(SUP-01 P3)
  #   on_failure: true                 #   失败后转移(默认 true; 配了 fallback_agents/agent_fallbacks 才生效)
  #   pre_dispatch: false              #   提交时主 agent degraded 就改派(默认 false: 不悄悄换掉你指定的 agent)
  # agent_health:                      # 健康度统计窗口/阈值(SUP-01 P3)
  #   window_sec: 3600                 #   统计窗口(秒)
  #   degraded_after: 3                #   窗口内供应商错误 >= N → degraded
  #   recover_after_ok: 1              #   最近一次供应商错误之后成功 >= N → 恢复
  # session_auto_relay_idle_sec: 300   # 【已迁移】顶层 session.auto_relay_idle_sec 的别名
session:                               # 终端会话中继(SESS-01 R1/R2)的自动布防判据; 0 = 关该判据
  # auto_relay_idle_sec: 300           # 键盘空闲 >= 阈值 → 会话停下时在 web 等回复
  # auto_relay_turn_sec: 900           # 探测不到键盘(容器)时改看距上次人工输入的秒数
log:                                   # 结构化 JSONL 文件日志(server 默认 <config-dir>/run/serve.log; worker 为 run/worker-<id>.log)
  # file: /var/log/gofer/serve.log     # 显式路径(打不开则启动失败); 不写=默认路径(打不开只 warn 并降级为 stderr)
  max_size_mb: 50                      # 单文件上限; 超过轮转
  max_age_days: 14                     # 轮转文件保留天数
  max_backups: 10                      # 轮转文件保留份数
storage:
  default_exchange_subdir: tmp
  default_result_subdir: gofer
  # root: /var/lib/gofer               # 设了则结果落 <root>/<project>/<job>
  # retention: {...}                   # 终态 job 保留上限(prune)
```

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
- **健康度**（`GET /v1/agents` 的 `health` 块、`gofer agent status`、web 徽标）：窗口内没有 job = `unknown`（不是"健康"）；`transient_fail ≥ degraded_after` 且最近一次供应商错误后成功数 `< recover_after_ok` → `degraded`。`failure_class`（transient|other）在每个 failed job 上无条件记录。
- **探针**：`gofer agent probe <key>`（web Agents 页也有按钮）提交一个 `--sync` 探针 job（固定 prompt、`tags: [probe]`），它走的就是普通提交路径，所以结果天然计入健康度。

## 8. 任务书模板目录（`<config-dir>/templates/`，SUP-01 P5）

模板**不是**配置项：它们是 server 上的一批 md 文件，提交时 `job run -t <name>` 让 server 渲染成 prompt。
查找顺序是「项目目录优先、全局目录兜底」：

| 位置 | 路径 | 用途 |
|---|---|---|
| 项目模板 | `<projects.<key>.host_path>/.gofer/templates/<name>.md` | 这个项目专属的任务书（跟随仓库走，可提交进 git） |
| 全局模板 | `<config-dir>/templates/<name>.md` | 所有项目共用（`GOFER_CONFIG_DIR`，默认 `~/.config/gofer/`） |

- 同名时**项目副本赢**；项目目录在 server 上不可读（worker-only 项目）时只查全局目录——这正是全局目录存在的理由。
- 文件格式：`---` frontmatter（YAML，白名单键：`desc`/`agent`/`runner`/`timeout_sec`/`tags`/`verify`/`verify_timeout_sec`/`review`/`read_only`/`worktree`/`fallback_agents`/`vars`）+ 正文（prompt 模板，`{{变量}}`、`{{include: 同目录.md}}`）。
- 改文件**即时生效**（每次提交都重新读取），不需要 SIGHUP，也没有"安装"步骤；`gofer template ls` 看当前能被项目用到的清单。
- 仓库自带两份示例（`docs/examples/templates/`），按全局目录的布局摆放：
  `mkdir -p ~/.config/gofer/templates && cp docs/examples/templates/*.md ~/.config/gofer/templates/`。
- 写模板的纪律：frontmatter **只**放 job 默认值与变量声明——`plan_id`/`caller_id`/`cmd` 这类在提交面才该出现的字段会被解析报错拒掉。
- worker：模板由 **server** 渲染，所以 worker 上不需要放模板文件；worker-only 项目用全局目录。

## 校验 / 生效

```bash
gofer config validate server           # 校验路径/agent/runner
gofer config info                      # 看解析出的 config 路径 + 关键 ENV
gofer config show <project>            # 看某 project overlay 合并后的有效配置
# 改完 SIGHUP server 进程即时生效(web 改 project 也会自动重推 POLICY worker)
```

---

- worker 侧怎么配（roots/guards/agents）见 `worker-config.md`。
- 加 project / 建 worker / 迁移的分步配方见 `setup-recipes.md`。
