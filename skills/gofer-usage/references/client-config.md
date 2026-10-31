# 纯客户端节点配置（`GOFER_RUN_MODE=client`）

> 场景：这台机器/容器上的 `gofer` **只当 CLI 用**——提交 job、看 plan、开会话中继、做 `tunnel forward`——不起 `serve`，也不起 `worker`。容器里给 AI agent 用的 gofer 基本都是这一种。

## 只需要一个 `.env`

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

### CLI 输出语言：`GOFER_LANG`

`gofer init client` 生成的 `.env` 末尾带一行注释掉的 `GOFER_LANG`：

```dotenv
# 这个节点的 CLI 输出语言: en 或 zh-CN（去掉下一行前面的 # 生效；不设则跟随系统语言）
# GOFER_LANG=en
```

它是**这台机器 CLI 自己**的语言（`--help`、提示、错误），优先级：命令行已导出的 `GOFER_LANG` → 这个 `.env` → 系统语言（`LANG` / `LC_ALL`；Windows 取界面语言）→ `en`。`.env` 在命令组装前被导出，所以等效于直接导出环境变量。`--json` 等机器可读输出不翻译。

注意：与 server 的 `server.language` / 项目的 `language` 是两回事——那两个决定**服务端生成、注入给 agent** 的文本（任务书、brief、通知），这里只决定本机 CLI 印给人看的字。两者的 `language` 不写等同 `auto`：按所在机器的系统语言选（中文 → zh-CN，其余 → en）。已迁移的命令组：`job` / `plan` / `session` / `init` / `config` / `worker` / `project` / `serve`；其余命令组仍是中文。

自检：

```bash
gofer config info          # run_mode: client / server 地址 / token set=yes / config dir / "(no local config in client mode)"
gofer job list             # 能列出 job = 通了
gofer project list         # client 模式默认就是 server 的实时项目表(等价 --remote); 找本工作空间的 project key 用它
gofer agent list           # server 的 agents, 带 batch/interactive 能力位
```

## 三种运行模式对照

| `GOFER_RUN_MODE` | 本地配置文件 | 用途 | `project list` 读谁 |
|---|---|---|---|
| `server`（默认） | `./.gofer[.local].yaml` → `<config-dir>/config.yaml` | 起 `serve`、本机跑 job | 本地 config.yaml |
| `worker` | `<config-dir>/worker.yaml` | 连入 hub 执行派发的 job | worker.yaml / POLICY 缓存 |
| `client` | **无**（只有 `.env`） | 纯 CLI | server（远程） |

常见错误：容器里把 `GOFER_RUN_MODE` 设成 `worker` 却没有 `worker.yaml`，于是 `gofer project list` 报 `read worker config …/worker.yaml: no such file`、`agent list` 列的是本地内置模板——纯 CLI 用途请设成 `client`。

## client 模式下能用 / 不能用

- **照常**：`job`（run/list/show/logs/watch/resume/cancel/worktree）、`plan`、`workflow`、`schedule`、`session`（含 `init hooks`、relay）、`tunnel`（forward/check/save/saved/ls，预设存在 server 上）、`project list/show/validate`（远程）、`agent list`（远程；`--local` 看内置模板）、`worker list`、`mcp`（连 `--server` 的模式）、`config info`。
- **明确拒绝**（错误文案 `run mode is client: … unset GOFER_RUN_MODE or use server/worker on that node`）：`serve`、`worker`、`project add/remove`、`config validate server|worker`、`config edit`、`mcp --standalone`。
- 显式 `--config <path>` / `GOFER_CONFIG` 仍按指定文件加载（显式优先于角色默认）。

## 容器 ↔ 主机的两个注意点

- **`host.docker.internal` 解析不到**（容器内有自定义 DNS 时常见）：`nslookup host.docker.internal 127.0.0.11` 问 Docker 内置 DNS 拿到 IP，把 `GOFER_SERVER_ADDR` 写成 IP。
- **路径**：job 在**执行机**的项目根解析 `--cwd`，容器路径与主机路径不同，命令里不要写死容器绝对路径，用 `--cwd <相对项目根>`（见 SKILL.md「派活」的两条约定）。
