# Runbook · 新增一个 cli-agent 需要配什么（AGT-04）

> 适用：往 `config.yaml` 的 `agents:` 里加一个第三方 CLI agent（jcode / opencode / 自研 wrapper …）。
> 目标：**多数情况只写 `command` 就能跑、能 `job resume`**；本文说清"哪些是自动的、哪些必须自己写、哪些必须改代码"。

## 1. 最小配置

```yaml
agents:
  jcode:
    type: cli-agent            # 可省略：不写就是 cli-agent
    command: jcode             # 唯一必填项（PATH 上的可执行名或绝对路径）
    args: ["run", "{{prompt}}"]        # 批处理 argv（含 {{prompt}} 才把提示词传给 agent）
    interactive_args: []               # 只有它存在（哪怕是空列表）才支持交互/pty job
```

- `args` 省略 = 该 agent 只支持交互；`interactive_args` 省略 = 只支持批处理（`Modes()` 的判据）。
- `type` 只有 `cli-agent` / `exec` / `acp-agent` 三种；**新类型要改代码**（见 §4）。
- 在项目里可用：项目的 `allowed_agents` 要包含这个 key（或用 `*`）。

## 2. 自动兜底（不用写）

| 能力 | 自动来源 |
|---|---|
| 会话 id 捕获 | 内置正则（仅 claude/codex/omp）→ **通用兜底正则**（AGT-04，所有 cli-agent） |
| `job resume` argv | 内置模板（claude/codex/omp）→ **兜底 `["--resume","{{session_id}}","-p","{{prompt}}"]`**（批处理）与 `["--resume","{{session_id}}"]`（交互） |
| 只读 argv | `read_only_args` 内置表（按 key，再按 `command` 基名） |
| transient 重试模式 | 内置表（按 key/`command` 基名，仅 claude/codex/omp 与已知 wrapper） |
| ndjson 投影 | 仅 omp/claude 有内置投影器；其余走通用（只做 `ndjson_keep` 白名单过滤） |

通用兜底正则（`agent.FallbackSessionCapture`，源码里的唯一定义）：

```
(?i)(?:^|[\s"'])(?:--resume|resume|--session[-_]?id)[=\s]+["']?([A-Za-z0-9][A-Za-z0-9._-]{7,127})["']?
```

也就是：**输出里出现 `--resume <id>` / `--session-id=<id>`（id 8–128 位、字母数字开头）就能被捕获**，
uuid、`ses_01H9…`、`session_hamster_1790079148520_bc5cb0d44153fe56` 这类都覆盖。

两条误抓防护（都在代码里，配置改不了）：

- **只读尾部 4KB**：兜底正则只看日志/流的最后 4KB（退出横幅永远在尾部）；内置/显式正则仍整读
  （codex 的 `session id:` 在输出头部）。交互 job 的实时 pty 捕获同理只读 tail 窗口。
- **占位符过滤**：`<session_id>`、`SESSION_ID`、`session-id`、`your-session-id`、`uuid`、`id`、
  `xxx`、`...`、`-`、`none`、`null`，以及空值 / 超 128 字节 / 含空白的值一律丢弃。

jcode 的实测退出横幅（2026-09-22 真机采样）正好落在兜底里：

```
Session hamster - to resume:
  jcode --resume session_hamster_1790079148520_bc5cb0d44153fe56
```

因此下面这几行 **现在可以不写**（写上只会覆盖兜底，等价）：

```yaml
agents:
  jcode:
    command: jcode
    interactive_args: []
    # ↓ 以下 3 行 AGT-04 起由兜底自动填充，无需配置
    # session_capture: '(?i)(?:^|[\s"''"])(?:--resume|resume|--session[-_]?id)[=\s]+["'']?([A-Za-z0-9][A-Za-z0-9._-]{7,127})["'']?'
    # session_resume: ["--resume", "{{session_id}}", "-p", "{{prompt}}"]
    # session_resume_interactive: ["--resume", "{{session_id}}"]
```

## 3. 什么时候**必须**自己写 `session_*`

| 情况 | 写什么 |
|---|---|
| resume 语法不是 `--resume <id>`（例：codex 的 `exec resume <id>`） | `session_resume`（+ 交互源用 `session_resume_interactive`） |
| id 出现在没有 `--resume`/`--session-id` 字样的行里（例：`session: <id>`、json 行 `"sessionId":"…"`） | `session_capture`（多分支写交替，**取第一个非空捕获组**） |
| 想让 gofer 生成 id 并在 argv 注入（claude 那样） | `session_inject: ["--session-id", "{{session_id}}"]` |
| 退出横幅里还有别的 `--resume xxx` 干扰、需要更严的正则 | `session_capture` |
| 想给这个 agent 开只读模式 | `read_only_args` |
| 供应商错误想触发自动续投/转移 | `transient_error_patterns` |

只有 `session_capture` 是**通用兜底**；`session_inject` 永远不兜底——gofer 不会对没见过的 CLI 瞎猜
`--session-id` 语义（注错了会直接把 agent 的 argv 打坏）。

## 4. 什么时候必须改代码

- **ndjson 事件流投影**：这个 agent 的 `--output-format json` 事件形状与 omp/claude 都不同，且想让
  stderr 时间线/最终答复好看 → `internal/runner/ndjsonfilter` 加一个投影器 + `internal/agent`
  的 `builtinNDJSON` 加一条（纯配置能做的只有 `ndjson_keep` 白名单）。
- **新 agent 类型**（既不是 argv 渲染的 CLI、也不是 ACP 协议 server）→ `internal/agent/adapter.go`
  的 `Modes`/`BuildFrom` 与 `internal/runner` 的对应 runner。
- **内置模板注入**（`internal/agent/templates.go`）：本机装了该 CLI 就自动出现在 `/v1/agents`
  的那种"开箱即用"条目——可选，不写也只是要手工在 config.yaml 声明。

## 5. 配完怎么验

```bash
gofer agent show jcode          # 看解析后的 session_capture / session_resume（兜底也会显示出来）
gofer job run -a jcode -p <proj> --interactive --prompt "..."   # 交互 job
#   TUI 里退出（jcode 用 /quit；/exit 不认），等 job 终态
gofer job show <job-id>         # session_id 非空 = 捕获成功
gofer job resume <job-id> --prompt "继续"   # 能起续接 job = resume 模板可用
```

- 时间线里会有一条 **`job.session_captured`** 事件：`agent=… · stdout · 兜底`。
  `by=fallback` 表示**这个 agent 还没有自己的 `session_capture`**——想收紧就按 §3 写一条。
  **远端 worker job 不会有这一条**：捕获只在**执行机**（worker）的库里记，hub 侧只从 outcome 拿
  `session_id`（所以 `job show` 的 `session_id` 有值、时间线却没有该行）；worker→hub 的事件镜像
  白名单刻意不含它——控制台里 attach 过的交互 job 由 hub 自己记一条，镜像会让那条翻倍。见
  `internal/job/events.go` 的 `mirroredEventTypes`。
- `job show` 的 `session_id` 为空时的排查顺序：① agent 的 `command` 是否真的打印了
  `--resume`/`--session-id` 字样；② 该行是否落在**最后 4KB**内（批处理 job 看 `stdout.log`
  末尾，交互 job 看 `pty.txt` 末尾）；③ 捕获值是否被占位符规则挡掉；
  ④ `gofer job logs <id>` 直接看原文。
