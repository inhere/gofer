# 纯客户端节点配置（`GOFER_RUN_MODE=client`）

> 场景：这台机器/容器上的 `gofer` **只当 CLI 用**——提交 job、看 plan、开会话中继、做 `tunnel forward`——不起 `serve`，也不起 `worker`。容器里给 AI agent 用的 gofer 基本都是这一种。

## 1. 只需要一个 `.env`

```bash
gofer init client          # 在 <config-dir>/.env 生成模板(已存在不覆盖); config-dir 默认 ~/.config/gofer, 可用 GOFER_CONFIG_DIR 改
```

```dotenv
# <config-dir>/.env
GOFER_SERVER_ADDR=http://host.docker.internal:8765   # 主机 server 地址(容器内常用 host.docker.internal; 解析不到时用 IP)
GOFER_SERVER_TOKEN=<server 的 bearer token>            # 不要提交进 git
GOFER_RUN_MODE=client                                  # 节点角色: 不加载、不要求任何本地 config.yaml / worker.yaml
```

`gofer` 启动最早期自动加载 `<config-dir>/.env`（再叠加 `./.env`，已导出的 OS env 最高优先），`gofer job` 等命令的 `--server/--token` 默认值就来自这两个变量，**无需手动 source、无需每次传 token**。

自检：

```bash
gofer config info          # run_mode: client / server 地址 / token set=yes / config dir / "(no local config in client mode)"
gofer job list             # 能列出 job = 通了
gofer project list         # client 模式默认就是 server 的实时项目表(等价 --remote); 找本工作空间的 project key 用它
gofer agent list           # server 的 agents, 带 batch/interactive 能力位
```

## 2. 三种运行模式对照

| `GOFER_RUN_MODE` | 本地配置文件 | 用途 | `project list` 读谁 |
|---|---|---|---|
| `server`（默认） | `./.gofer[.local].yaml` → `<config-dir>/config.yaml` | 起 `serve`、本机跑 job | 本地 config.yaml |
| `worker` | `<config-dir>/worker.yaml` | 连入 hub 执行派发的 job | worker.yaml / POLICY 缓存 |
| `client` | **无**（只有 `.env`） | 纯 CLI | server（远程） |

以前容器里常把 `GOFER_RUN_MODE` 设成 `worker` 却没有 `worker.yaml`，于是 `gofer project list` 报 `read worker config …/worker.yaml: no such file`、`agent list` 列的是本地内置模板——这就是 client 模式要解决的事。

## 3. client 模式下能用 / 不能用

- **照常**：`job`（run/list/show/logs/watch/resume/cancel/worktree）、`plan`、`workflow`、`schedule`、`session`（含 `init hooks`、relay）、`tunnel`（forward/check/save/saved/ls，预设存在 `<config-dir>/tunnels.yaml`）、`project list/show/validate`（远程）、`agent list`（远程；`--local` 看内置模板）、`worker list`、`mcp`（连 `--server` 的模式）、`config info`。
- **明确拒绝**（错误文案 `run mode is client: … unset GOFER_RUN_MODE or use server/worker on that node`）：`serve`、`worker`、`project add/remove`、`config validate server|worker`、`config edit`、`mcp --standalone`。
- 显式 `--config <path>` / `GOFER_CONFIG` 仍按指定文件加载（显式优先于角色默认）。

## 4. 容器 ↔ 主机的两个注意点

- **`host.docker.internal` 解析不到**（容器内有自定义 DNS 时常见）：`nslookup host.docker.internal 127.0.0.11` 问 Docker 内置 DNS 拿到 IP，把 `GOFER_SERVER_ADDR` 写成 IP。
- **路径**：job 在**执行机**的项目根解析 `--cwd`，容器路径与主机路径不同，命令里不要写死容器绝对路径，用 `--cwd <相对项目根>`（见 `SKILL.md` §2）。
