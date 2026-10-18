# 调用方上限（ceiling）发放与回滚 Runbook

> 设计：[`../design/2026-10-18-caller-scope-ceilings-design.md`](../design/2026-10-18-caller-scope-ceilings-design.md)。
> 字段与语义：[`../../skills/gofer-usage/references/server-config.md`](../../skills/gofer-usage/references/server-config.md)「调用方上限（ceiling）」。
> 接口（whoami 的 `ceiling`、403 / 404 的区分、job 凭据继承）：[`../reference/http-api.md`](../reference/http-api.md)「Permission grants」。
> 换二进制的流程：[`release-and-binary-swap.md`](release-and-binary-swap.md)。受管 server 的操作见 [`2026-10-08-serve-management-runbook.md`](2026-10-08-serve-management-runbook.md)。

上限是**只减不加**的一层：它不给调用方任何权限，只把调用方已有的权限再收窄到写明的项目 / runner / worker / agent 与开关之内。所以给 agent / CI 发限制的顺序是「先有调用方与权限，再加 `ceiling`」。

## 1. 给 agent / CI 发一枚受限 token

### 1.1 选 id、把 token 值放进环境变量

- 挑一个 id（如 `ci-bot`）。不要和 `server.workers` 的键重名，加载会直接拒绝。
- token 值放在 **server 主机**上 serve 进程能读到的环境里：`<config-dir>/.env`（serve 启动时加载；`GOFER_CONFIG_DIR` 决定目录，默认 `~/.config/gofer`），或服务 / 容器自己的环境。已导出的环境变量优先于 `.env`。

  ```
  GOFER_TOKEN_CI=<足够长的随机串>
  ```

- 生成：`python -c "import secrets;print(secrets.token_urlsafe(32))"`。不要复用人的 token，也不要复用别的调用方的值。

### 1.2 在配置里写上调用方与上限

```yaml
server:
  callers:
    - id: ci-bot
      token_env: GOFER_TOKEN_CI
      ceiling:
        projects: ["app-*", "docs"]   # 只允许这些项目（glob，* 不跨 /）
        runners: ["w-linux-1"]        # 只允许这些 runner；不写 local / server 就没有本机 runner
        agents: [codex, claude]       # 只允许这些 agent
        allow_exec: false             # 不允许 exec 类作业（agent exec / cmd / verify / 额外 agent argv）
```

- 想让它跑**本机**（不推荐）：`runners: [local]`（`server` 同义）——那等于把主机 shell 给了它，加载会打一条警告，此时这个上限不构成安全边界。
- 上限只减少：项目自己的 `allowed_runners` / `allowed_agents` 仍然要包含它要用的 runner / agent，否则会被项目准入拒（与上限无关）。
- 只做报表 / 只读的 CI：加 `read_only: true`（它连个人偏好类之外的写操作一律被拒）。
- 别写 `ceiling: {}`（加载直接失败），也别给 `can_admin: true` 的调用方写（加载拒绝）。

### 1.3 校验并生效

```bash
gofer config validate server      # 在 server 主机上跑：glob 写错、空 ceiling、给根管理员写上限都在这里先报
```

- **新增**调用方（一枚新 token）：重启 serve。`server.callers` 属重启级 —— token 集合、以及取 token 值所依赖的环境，都在进程启动时确定。
- 只给**已在配置里**的调用方改 `ceiling`：`gofer serve reload -c <config>` 即可（判定每次读当前配置快照；reload 回执把它列为已变更，不要求重启）。

### 1.4 核对已经生效

```bash
# 用受限 token 跑 whoami：输出末尾的 ceiling 段就是生效值
GOFER_SERVER_TOKEN=<受限 token> gofer auth whoami
# 或直接问接口
curl -s -H "Authorization: Bearer <受限 token>" http://<server>:8765/v1/auth/whoami | jq .ceiling
```

- 返回的就是生效值：`projects` / `runners` / `workers` / `agents` 是配置里写的名单（没写的维度不出现），`allow_exec` / `global_memory` / `read_only` 是真假值 —— 省略按更严的默认解析成 `false`，`runners` 缺省表示不含本机 runner。
- serve 启动与每次 reload 会打一行：`gofer: caller ceilings: 1 restricted caller(s): ci-bot`（日志里同名事件 `server.caller_ceilings` 带 `count` / `callers`）。**没有这一行就说明这台 server 上没有任何上限在生效**，别以为已经限制了。
- `gofer auth whoami` 同时列出调用方 id、权限与上限，确认三者都符合预期。

### 1.5 试一次被拒、一次放行

```bash
# 范围外的项目（预期 403，detail 写明是哪个维度）
GOFER_SERVER_TOKEN=<受限 token> gofer job run -p other-project -a codex --prompt 'hi'

# 本机 runner（预期 403：上限里没写 local）—— job run 的 --runner 默认就是 server
GOFER_SERVER_TOKEN=<受限 token> gofer job run -p app-a -a codex --prompt 'hi'

# 范围内 + worker runner（预期正常提交）
GOFER_SERVER_TOKEN=<受限 token> gofer job run -p app-a -a codex --runner w-linux-1 --prompt 'hi'

# 范围外的资源（预期 404，与「不存在」同形）
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer <受限 token>" \
  http://<server>:8765/v1/jobs/<另一个项目的 job id>
```

## 2. 排查「被拒」

错误体统一是 `{"error": "...", "detail": "..."}`；先看 `error`：

| `error` | 状态码 | 含义 |
|---|---|---|
| `job rejected` | 403 | 提交准入拒绝；`detail` 形如 `caller ceiling denied: caller "ci-bot": runner "server" is not in the caller's ceiling`，维度写在里面（project / runner / worker / agent / exec） |
| `caller ceiling` | 403 | 路由门拒绝：`read_only` 的写操作、受限调用方改服务器级状态（配置 / 管家 / 隧道 / worker 管理等）、job 凭据的上限所有者解析不到 |
| `not found` | 404 | 资源在上限范围之外（与真的不存在同形）。范围之内但缺权限是 `permission required` 一类 |
| `unauthorized` | 401 | token 本身不对：改了 `token_env` 的值忘了重启，或这个调用方已从配置里删掉 |

常见原因：

- `--runner` 用了默认的 `server`（本机）：上限里没显式写 `local` / `server` 就会被拒。改派 worker runner，或（想清楚后）把本机 runner 写进上限。
- exec：`allow_exec` 省略 / `false` 时，`agent: exec`、`cmd`、`verify`、额外 agent argv 全被拒。要跑构建类作业就给 `allow_exec: true`（建议继续不给本机 runner）。
- `workers` 名单：`--worker-labels` 自动选中的那台 worker 也会被检查，名单要覆盖它。
- `agents` 名单：**续接**按被续接的那个 agent 判定，名单里要有它。
- 项目 glob：`*` 不跨 `/`（`app-*` 匹配 `app-a`，不匹配 `app/a`）。
- 与上限无关的两道门：项目自己的 `allowed_runners` / `allowed_agents`（不在名单里是项目准入拒绝，不是上限），以及调用方自己的权限（上限只减不加，没有 `job.submit` 就根本提不了 job）。
- 失败关闭：源 job 已被清理 / job 行读不出来时，job 凭据发起的提交会被拒，而不是当作无限制；范围非空的上限下，没有项目归属的记录（空项目的会话 / 工作项 / 计划）也算范围外。

派生动作被终止后去哪看：

- 源 job：web 的 job 详情时间线（或 `GET /v1/jobs/{id}/events`）里有一条 `job.ceiling_denied`，detail 里带 `path`（`retry` / `fallback` / `wakeup` / `plan` / …）与 `reason`；每条拒绝在 serve 日志里也有一条 `ceiling denied a derived submission`（带 `job_id` / `path` / `reason`）。`gofer job show <id>` 的失败原因仍是原来那个 —— 上限拒的是它后面那一次提交。
- 定时：serve 日志里 `gofer: schedule <id> submit failed: caller ceiling denied: …`，同时服务端在作用域 `schedule:<id>` 上记一条 `schedule.run_failed`。那**一次运行**被丢掉，周期性的定时**本身保留**，下次到点照常；`type: once` 的一次性定时在提交失败时会被停用（`enabled: 0`）。
- plan 项：`gofer plan show <plan id>` 的 todo 行上写 `dispatch_error: …`（web 的计划清单同样显示）。

「上限收紧后旧的续投 / 失败转移 / 重试 / 定时调用不再自动往下跑」是**预期行为**：上限拒绝是永久的，不会排队等下一次。要么放宽上限，要么把这类任务改派到范围内的项目 / runner / agent。

## 3. 回滚：先撤销或轮换受限 token

**旧版本不认 `ceiling` 这个键**（其它配置本来就宽松解码，旧版本只是把它当成不认识的键忽略）。所以换回旧二进制后，那枚 token 会立刻变回**不受上限约束**的调用方（权限照旧）。顺序不能反：

1. **先**让旧 token 失效：从 `server.callers` 里删掉那一项，或把 `token_env` 指向一个新值 / 直接换掉 token 值，然后重启 serve（token 集合在启动时确定）。
2. 核对旧 token 已经不能用了：

   ```bash
   curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer <旧 token>" http://<server>:8765/v1/auth/whoami
   ```

   期望 `401`。返回 `200` 说明它还能用，不要继续。
3. **再**换回旧二进制 / 旧版本（见 [`release-and-binary-swap.md`](release-and-binary-swap.md)）。
4. 如果这枚 token 已经泄漏：第 1 步的轮换是唯一的补救；回滚本身不改变它的权限。

数据面：

- job 上记「上限所有者」的新列是 additive 的，旧版本直接忽略它（读出来是空）；旧库由新版本启动时自动补列。回退**不需要**任何迁移、改库或删数据。
- 回退后 `whoami` 不再返回 `ceiling`、启动也不再打印 `caller ceilings: …` 那一行 —— 这**不能**当成「上限还在」。

## 4. 日常维护

- **收紧**上限（如 `projects: ["app-*"]` 改成 `["app-a"]`）：改文件 + `serve reload` 即可，被收紧的调用方的派生链从那一刻起开始被拒（见 §2 的事件），旧任务不会自动往下跑。
- **放开**上限：同样改文件 + reload；上限只减不加，放开也放不出它没有的权限。
- **轮换 token**：改值 + 重启 serve；调用方换成新值。
- **想确认哪些调用方还有上限**：看启动 / reload 打的那一行，或逐个受限 token 问 `whoami`，或直接看配置文件里的 `ceiling`（`gofer config edit`）。
- **不需要上限的调用方**：删掉它的 `ceiling` 块 + reload 即可回到不受限（前提是它的 token 没有落入不该有它的地方）。
