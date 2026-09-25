# ACP agent 认证 Runbook（F14）

> 配套 design [`../design/2026-09-17-acp-agent-and-approval-gate-design.md`](../design/2026-09-17-acp-agent-and-approval-gate-design.md) §F14。
> 适用 `agents.<key>.type: acp-agent`（claude-acp / codex-acp / omp-acp / gemini-acp / jcode-acp …）。

## claude-acp 的认证

**推荐做法：key 只维护在 claude 自己的设置文件里，gofer 不用管。**

主机上 claude 的 key 与中转地址通常写在 `~/.claude/settings.json` 的 `env` 块（`ANTHROPIC_API_KEY`、`ANTHROPIC_BASE_URL`、各档 `ANTHROPIC_DEFAULT_*_MODEL[_NAME]`）。`claude` CLI 自己会读它，所以「在终端里能用」；但 `claude-acp`（Zed 的 `@zed-industries/claude-code-acp`，底层是 Claude Agent SDK）在会话前自己解析凭据，**既不看 gofer 进程环境、也不读这个文件** → 报 `-32000 Authentication required`。

F14 的开关把这件事补上：`agents.claude-acp.acp.claude_settings_env: true` 时，gofer 在**执行机**上启动 ACP 子进程之前读该文件，把其中**当前进程环境与 job env 都没有**的键追加进子进程环境。

```yaml
agents:
  claude-acp:                    # 内置模板已经默认打开，通常什么都不用写
    type: acp-agent
    acp:
      claude_settings_env: true  # 默认值；显式 false 可关
```

要点：

| 项 | 行为 |
|---|---|
| 文件路径 | `$CLAUDE_CONFIG_DIR/settings.json`（设了该变量时；job env 优先于进程 env），否则 `<用户主目录>/.claude/settings.json` |
| 读什么 | 只读 `env` 对象里的**字符串**值；文件里其它内容（permissions、hooks…）无关 |
| 优先级 | **显式设置优先**：进程环境或 job env 里已有的同名键一律**不覆盖**（不看值是否为空，存在即算显式） |
| 谁打开 | 内置 `claude-acp` 模板默认 `true`；其它模板与手写 agent 都是 `nil`（关） |
| 在哪读 | **执行机**上读——job 派给 worker 时，读的是该 worker 的 claude 设置文件（worker 用自己的 agent 配置复校，与 `read_only` / `acp.modes` 同一规则） |
| 读不到怎么办 | 文件不存在 / 解析失败 / 没有 `env` 块 → 一条 `slog.Warn`，job 照常启动（不是错误） |
| 安全 | 值只进子进程环境：**不**进 `request_json`、渲染命令、事件、job 日志或 gofer 日志（日志只记「注入了 N 个键」与**键名**列表）；SEC-01 的 deny list 仍然生效，设置文件里出现 `GOFER_TOKEN` 等被拒键照样剔除 |

**其它两条等价路径**（选一条即可，别混着维护多份）：

1. 放 gofer 部署的 `<config-dir>/.env`（`ANTHROPIC_API_KEY` / `ANTHROPIC_BASE_URL`），serve 进程环境就有了 → 子进程直接继承，与 F14 无关。
2. 给运行 serve/worker 的账号设**用户环境变量**（Windows 的 `setx`、systemd 的 `Environment=`）。

**不要**把 key 写进 `config.yaml` 的 `agents.<key>.env`：那是明文进配置文件、并会落进 job 的 `request_json`；配置 API 也把 `agents.*.env` 列为不可编辑（写入会被拒）。真要用 agent env，就用 `${ANTHROPIC_API_KEY}` 这类**引用**指向外部环境变量。

## 排查

| 症状 | 先看什么 |
|---|---|
| `-32000 Authentication required`（prompt 立刻失败） | 执行机上 claude 的凭证是否可用：`claude auth status`；`~/.claude/settings.json` 的 `env` 块是否有 key；`CLAUDE_CONFIG_DIR` 有没有指到别处 |
| 子进程确实没拿到 key | 在 job 的 `stderr.log` / gofer 日志里搜 `acp runner: inherited claude settings env`：有这行说明注入了若干键（行里带键名与数量，如 `count=2 keys=ANTHROPIC_API_KEY,ANTHROPIC_BASE_URL`） |
| 该行是 `cannot read claude settings file` | 路径不对（看日志里的 `path=`）或该机上没这个文件；也可能是执行机不是你以为的那台（worker job 读 worker 的文件） |
| 该行是 `declares no env block` | 文件没有 `env` 块，或块是空的 |
| 注入了 key 但仍认证失败 | 看 `stderr.log` 里适配器自己的报错；claude-acp 还有一个已知前提：需要能定位到已登录的 claude 可执行文件（`CLAUDE_CODE_EXECUTABLE`）或长期 token（`CLAUDE_CODE_OAUTH_TOKEN`），见 design §S0「claude-acp 复查」 |

## 其它适配器的认证（各自不同，均为实测结论）

| agent | 凭据来源 | 备注 |
|---|---|---|
| `codex-acp` | `~/.codex/auth.json` | 适配器自己读，**不需要** F14 的开关；真机失败点是供应商流断线，不是认证 |
| `omp-acp` | omp 自己的配置 | 模板不设 `claude_settings_env` |
| `gemini-acp` | gemini CLI 的登录态 | 本机未安装时为 MISSING（availability 是 PATH 探测，与登录态无关） |
| `jcode-acp` | jcode 守护进程 | 同上 |
