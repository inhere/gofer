# 配置

## 文件在哪

按顺序查找，先命中先用：

1. `-c / --config <path>`（命令行；`gofer serve`、`job`、`project` 等命令都认）
2. 环境变量 `GOFER_CONFIG`
3. 当前目录的 `./.gofer.local.yaml`，再 `./.gofer.yaml`
4. `<config-dir>/config.yaml`

`<config-dir>` 默认是 `~/.config/gofer`（Windows 上是 `%USERPROFILE%\.config\gofer`），可用环境变量 `GOFER_CONFIG_DIR` 改。`gofer init server --global` 就是写到这里的 `config.yaml`，也是**推荐的单机形态**：一台机器一个 server、一份全局配置，在任意目录执行 gofer 都读它。

项目目录里可以再放一个**瘦配置** `.gofer.project.yaml`，只写这个项目的偏好（默认 agent、结果子目录等）。准入相关的字段（允许的 agent / runner、`allow_exec` 等）只写在全局配置里——否则“这个项目能不能跑”就会有两份互相矛盾的说法。

`<config-dir>/.env` 与当前目录的 `./.env` 会被自动加载，token 之类放这里。

```bash
gofer config info               # 解析到了哪个文件、哪些 ENV 在生效
gofer config show <project>     # 某个项目合并后的有效配置
gofer config validate           # 校验（加载 + 逐项目检查路径 / agent / runner）
gofer config lint               # 哪些键可以删、哪些 agent 可以 extends: builtin
```

## 三种运行模式

同一个二进制，靠 `GOFER_RUN_MODE` 决定它扮演谁：

| `GOFER_RUN_MODE` | 本地配置 | 用途 |
|---|---|---|
| `server`（默认） | 上面的查找链 | `gofer serve`、本机管理、提交 job |
| `worker` | `<config-dir>/worker.yaml` | 连入 server 并执行派发来的 job |
| `client` | 只有 `<config-dir>/.env` | 提交与观察；project 与 agent 命令读 server 的实时数据 |

容器里最常见的是 `client`：`gofer init client` 写出 `.env`（`GOFER_SERVER_ADDR` / `GOFER_SERVER_TOKEN` / `GOFER_RUN_MODE=client`），之后 `gofer project list` 看到的就是 server 上的项目。`gofer init client` 之后**不要**再在里面跑 `serve` / `worker` / `project add`——那些是本机命令，要在 server 或 worker 那台机器上执行。

## 一份最小的 server 配置

```yaml
server:
  addr: 0.0.0.0:8765            # 默认值；纯本机用 127.0.0.1:8765
  token_env: GOFER_TOKEN        # token 从环境变量读，不要写死在文件里
  allow_empty_token: false      # 只有本机试验才设 true

projects:
  <project>:
    host_path: /abs/path/to/<project>
    default_agent: codex
    allowed_agents: [codex, claude, exec]
    allowed_runners: [local]    # local / server 是本机内置 runner（保留名）
    allow_exec: true            # 允许 exec（裸命令）job
```

其余常用段（都可有可无，文件里都有注释）：`server.tls`（HTTPS）、`server.metrics`、`server.callers`（多调用方与上限）、`server.workers`、`server.notification`（webhook / 钉钉 / 飞书）、`server.event_hooks` / `server.decision_hooks`、`session`（会话中继的判据与等待上限）、`work` / `steward`（工作项与管家）、`storage`（结果目录、保留期）、`log`。

完整示例见 [`config/gofer.example.yaml`](../../config/gofer.example.yaml) 与 [`config/worker.example.yaml`](../../config/worker.example.yaml)；字段级参考在 skill 里：[server](../../skills/gofer-usage/references/server-config.md) · [worker](../../skills/gofer-usage/references/worker-config.md) · [client](../../skills/gofer-usage/references/client-config.md) · [分步配方](../../skills/gofer-usage/references/setup-recipes.md)。

## 热重载 vs 重启

- `gofer serve reload`：重新读配置文件，大多数阈值、token、callers、项目增改、会话与通知设置即时生效；失败时保留旧配置继续跑。
- `gofer worker reload <worker-id>`（或 worker 机器上 `--local`）：让 worker 重新读 `worker.yaml`。
- 需要**重启**的少数键会在提示里说明（例如 runner 实例重建、retention 的启停与间隔）。受管服务用 `gofer serve restart`。

## Windows 与容器

- **路径**：`host_path` / `container_path` 必须是**运行 gofer 那台机器上**的绝对路径。Linux 上写 `D:/projects/demo` 会被 `config validate` 直接拒绝。
- **容器**：主机侧项目写 `host_path`，容器里看到的挂载点写 `container_path`；`server.path_view: host|container`（默认 host）决定 gofer 进程侧一律用哪个视角。gofer 不做容器自检，由你显式切换。
- **容器访问主机 server**：`GOFER_SERVER_ADDR=http://host.docker.internal:8765`（server 监听 `0.0.0.0` 才能这样连）。

## 安全基线（写配置时就要定下来）

- 没有 token 时 server 拒绝启动（除非 `--allow-empty-token`）。
- 不需要远程访问时监听 `127.0.0.1`。
- exec 类 job 需要项目显式 `allow_exec: true`；交互 job 需要 `allow_interactive: true`。
- argv 从不经过 shell；job 的 cwd 被限制在项目目录内。
- 密钥走 `secrets` + `--secret`，不要写进 `--env`、prompt 或配置文件。

更多见 [security.md](security.md)。
