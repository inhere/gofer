# gofer 配置配方（分步，照做即可）

> 常见配置任务的可照抄步骤。字段细节见 [server-config.md](server-config.md) / [worker-config.md](worker-config.md)，命令见 [operations.md](operations.md)。
> 全部 `<占位符>`。改 server config 后 `gofer serve reload -c <config>`（或 Unix SIGHUP）生效；改 worker.yaml 后 `gofer worker reload` 或重启 worker。

## 配方 1：加一个 project 并让它能跑

**只在 server 本机跑**（最简单）：

```yaml
# server config.yaml → projects:
app-new:
  host_path: D:/projects/app-new
  container_path: /work/projects/app-new
  allowed_agents: [exec, claude]
  allowed_runners: [local]
  allow_exec: true
```

`gofer config validate server` → `gofer serve reload -c <config>` → `gofer project list --remote` 确认。也可以用 `gofer project add` 或 web Projects 页。

**要派给某台 worker `builder-1`**：

- ① 上面 `allowed_runners` 加该 worker 的 runner 名：`allowed_runners: [local, builder]`。
- ② 确认该 worker：**POLICY** → 它的 `roots` 覆盖 `D:/projects/app-new`（不覆盖就加一条 root，见配方 3）；**LEGACY** → 在它的 worker.yaml 同名再定义一遍（`allowed_runners: [local]`）。
- ③ 重载 server（POLICY worker 会自动收到新 project，无需上机器）。

## 配方 2：接一台新 worker 节点

**推荐：一步接入向导**

```bash
# 管理员（任意能连 server 的机器）：
gofer worker add builder-1 --labels linux --project app-new      # 输出一次性 worker token 与下一步命令
# worker 机器上：
gofer init worker --server <server-addr> --id builder-1 --token <worker token> --project app-new --yes
#   或直接用管理员 token 一步登记：--admin-token <管理员 token>（只用于本次，不写盘）
gofer worker -d                 # 后台起；停用 gofer worker stop
```

向导会向 server 要项目、推断 roots、探测 agent、写 worker.yaml、跑 doctor。

**手工配置**：同一 `worker_id`（下例 `builder-1`）三处对齐：

① server config.yaml：

```yaml
server:
  workers:
    builder-1: { token_env: GOFER_WORKER_BUILDER1_TOKEN, labels: [linux] }
runners:
  builder: { type: worker, worker_id: builder-1 }
# 要跑的 project 的 allowed_runners 加上 "builder"
```

② worker 机器：

```bash
gofer init worker -g            # 生成 worker.yaml 骨架(全局)
# 编辑 worker.yaml: worker_id: builder-1; server_link.urls 指向 server; 加 roots(见配方 3)
export GOFER_WORKER_TOKEN=<与 server.workers.builder-1 同一 token>
gofer config validate worker    # 自检
gofer worker doctor             # 连通 / agent / 注册握手
gofer worker -d
```

③ 验证：`gofer worker ls` / `gofer worker show builder-1`，或 web Runners 页看到 builder-1 在线。

> 连不上 / 看不到 → 查三处对齐（[worker-config.md](worker-config.md)「worker_id 三处对齐」）：注册被拒 = `server.workers` 缺或 token 不符；名册看不到 = `runners` 缺。

## 配方 3：把一台 worker 从 LEGACY 迁到 POLICY（加 roots，可回滚）

**目标**：worker 不再本机定义 projects，改由 server 下发。**两阶段、逐台、随时可回滚**。

**① 加 roots + guards，暂留 projects**（roots 非空即进 POLICY）：

```yaml
# worker.yaml
roots:
  - { from: D:/work/x, to: <本机对应前缀> }        # 覆盖 server 下发 project 的 host_path
  # 末段不一致的加更具体 root: { from: D:/work/x/proj-a, to: <本机 proj-b> }
guards: { allow_exec: true, allow_interactive: true }   # 显式声明, 保持现有能力
# projects: 段【暂时保留】(此刻被忽略, 会告警) —— 为了能回滚
```

**🔴 路径核对表（必做，别想当然）**：对 server 下发的每个 project，逐条核对「`host_path` 经 roots 映射后」== 你要的本机目录。不一致就加更具体的 root。（`gofer config validate worker` 会校验 `to` 目录存在并提示重叠。）

**② 重启 worker 应用**（LEGACY → POLICY 需重连拿 server 的策略，不是 reload）：

```bash
gofer worker stop && gofer worker -d
gofer project list        # 确认: 列出 server 下发的 project 及【映射后本机路径】, 无被拒项
```

**③ 观察期**：projects 段仍保留 = 可安全回滚。稳定后才删 projects 段、再重启。

**回滚**（随时，配置级）：删 `roots`（或恢复备份）→ 重启 → 立刻回 LEGACY。

## 配方 4：加一个 agent

server 侧定义 + 探测（config.yaml → agents:）：

```yaml
agents:
  myagent:
    type: cli-agent
    command: myagent
    args: [run, "{{prompt}}"]        # {{prompt}}/{{cwd}}/{{job_id}}/{{result_dir}}
    interactive_args: []             # 要支持 --interactive 时写
    detect: { command: myagent, args: [--version] }
```

- 允许某 project 用它：该 project `allowed_agents` 加 `myagent`，然后重载 server。
- **在 worker 上跑**：那台机器得**本机装了** `myagent` 并在 worker.yaml `agents:` 里定义（同样的写法）——否则提交报 `unknown agent`。
- 验活：`gofer agent probe myagent -p <project>`。

## 配方 5：诊断「project / agent 不能跑」

```bash
gofer worker projects <id>        # 该 worker 实际生效的 project
gofer config validate worker      # 模式? roots 对不对? to 目录在不在?（在 worker 机器上）
gofer project list                # POLICY: 当前生效 project + 映射路径
```

- **project 不在 worker**：POLICY → ① server 侧该 project `allowed_runners` 含这台的 runner？② worker `roots` 覆盖其 `host_path`？（不覆盖 = `path_outside_roots`）③ 重连窗口内会短暂 `policy_pending`。LEGACY → worker.yaml 里定义了吗？
- **`unknown agent`**：该 agent 本机没装 / 没定义 → 换已装的或装上后重载。
- **`path_outside_roots`**：加一条覆盖该 `host_path` 的 root（配方 3）。

更多现象见 [troubleshooting.md](troubleshooting.md)。
