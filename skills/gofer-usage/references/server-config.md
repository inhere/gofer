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
    interactive_allowed_agents: [tty-claude]   # pty 交互白名单(空=全禁)
    allowed_runners: [local, builder]          # ★ 见下 —— 决定派给谁
    allow_exec: true
    max_concurrent_jobs: 4                      # 该 project 并发上限(0/不写=无限)
    # capture_diff: false                       # 关 git-diff 抓取(不写=cwd 是 git 树时默认开)
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
    args: [exec, "{{prompt}}"]          # 模板: {{prompt}} {{cwd}} {{job_id}} {{result_dir}}
    detect: { command: codex, args: [--version] }   # 探测本机是否装了
  exec:
    type: exec                          # 内置; 跑请求给的 argv, 不用模板
```

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
storage:
  default_exchange_subdir: tmp
  default_result_subdir: gofer
  # root: /var/lib/gofer               # 设了则结果落 <root>/<project>/<job>
  # retention: {...}                   # 终态 job 保留上限(prune)
```

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
