# job 凭证（SEC-01）使用 Runbook

> 配套 design [`../design/2026-09-23-job-credentials-and-leader-opt-in-design.md`](../design/2026-09-23-job-credentials-and-leader-opt-in-design.md) §一 / §4。

## 是什么

以前每个 job 进程都**继承 serve 进程的整个环境**，其中就有 server 的 bearer token（`server.token_env: GOFER_TOKEN`）——所以 job 里的 agent 能直接用人的身份调 API。v0.57 leader 真机验收就是这么被绕过去的。

现在反过来：**job 进程里没有 server/worker token，只有一枚它自己的凭证**。

| | 变化 |
|---|---|
| 身份来源 | 从"请求体自报的 `as_job`"改为"请求带的 bearer token"——token 决定你是谁，说不了谎 |
| job 的环境 | `GOFER_TOKEN` / `GOFER_SERVER_TOKEN` / `GOFER_WORKER_TOKEN` / **`GOFER_CONFIG_DIR`** 一律**不再继承**（外加 `server.job_env_denylist` 里你点名的键） |
| job 拿到什么 | `GOFER_JOB_TOKEN=<gjt_<job_id>_<32hex>>`、`GOFER_SERVER_ADDR=<hub 地址>`，以及 `GOFER_BIN=<运行这个 job 的 gofer 绝对路径>` 和"该二进制所在目录"前置的 `PATH` |
| 有效期 | job 开始执行时签发；job 结束（含 `needs_review`）即吊销；另有兜底过期（job 超时 + 10 分钟），覆盖 hub 崩了没人吊销的情况 |
| 落库 | 只存 sha256（`job_tokens` 表），token 明文只存在于 job 进程环境和（派给 worker 时的）dispatch 帧里 |

CLI 与 gofer MCP 的默认 token 是 `GOFER_JOB_TOKEN`（显式 `--token` 仍可覆盖），所以 agent 在 job 里跑 `gofer job comment …` 就是**以那个 job 的身份**说话。

`GOFER_CONFIG_DIR` 也在名单里，因为它是**凭证指针**而不是设置：job 继承它，等于把 server 的 `<config-dir>/.env`（里面有操作员的 `GOFER_TOKEN`）交给 job 里下一次 `gofer` 调用。这正是 v0.60 真机验收的漏口 1（详见 design「v0.60 真机验收与 F10」）。与之配套，**CLI 在 job 内加载 dotenv 时跳过所有 `*_TOKEN` 键**：进程里已有 `GOFER_JOB_TOKEN` 就说明这是 job，而它读到的 `.env` 是 server 的——文件里的其它键照常加载（它仍然是部署的设置文件）。

`GOFER_BIN` / `PATH` 是漏口 2 的修法：job 里的 `gofer` **永远是运行它的那个 gofer**（hub 上的 job 是 serve 的那个二进制，worker 上是 worker 的那个），不再取决于主机 PATH 上碰巧装了哪个版本。`PATH` 前置只加目录、不删原有项，`GOFER_BIN` 给不想依赖 PATH 的脚本用。

## job 里能做什么、不能做什么

| 能力 | member job | leader job |
|---|---|---|
| 读：job/plan/comments/skills/agents（所有 GET） | ✓ | ✓ |
| 评论（记为 `author_kind=agent`、`author=<该 job 的 agent>`） | ✓（只记录，不派活） | ✓（可在**自己的 plan** 里 `@成员` 派活，照旧受限流 + 项目 allowlist） |
| `gofer_ask_human` / decisions | ✓ | ✓ |
| 给**自己**建 wakeup（`gofer job wakeup create <自己>`） | ✓ | ✓ |
| `gofer plan set-todo` | ✗ | 仅**本 plan**、仅 `ready` / `skipped` |
| 提交 job（`gofer job run`） | 默认 ✗；见下 `can_submit` | ✗ |
| accept / reject / cancel 别的 job / 改配置 / skill 写 / xfer / tool cp | ✗ | ✗ |

被拒时的文案固定：`job credential may not <动作>`（例如 `job credential may not accept a delivery`）。读路由一律放行；**写路由默认拒绝**——没在表里显式放行的写操作，job 凭证一律 403。

## 两个受控出口

### 1. `projects.<k>.job_env_allow`：这个项目的 job 继续继承某些变量

```yaml
projects:
  legacy-scripts:
    host_path: /srv/legacy
    job_env_allow: [GOFER_TOKEN]      # 即使 denylist 里有，也照常继承
```

- 逐项目、要显式写；没有全局"继续继承"开关。
- 真的用上时，该 job 会记事件 `job.env_allowed {keys:[GOFER_TOKEN]}`（`gofer job events <id>` 能看到），所以"哪个 job 还看得见旧 token"是可查的，而不是只写在配置文件里。
- 也可以通过 `PUT /v1/projects/{key}` 的 `job_env_allow` 字段改（web/HTTP 都能读写），`GET /v1/projects/{key}` 会回显。
- 想给某个 job 更多继承变量：`server.job_env_denylist` 是**加**在默认三键之上的名单，用来把其它名字的凭证也挡掉。

### 2. `agents.<k>.can_submit` / `roles.<k>.can_submit`：member job 可以提交 job

```yaml
agents:
  omp-supervisor:
    type: cli-agent
    command: omp
    can_submit: true
    submit_agents: [exec]        # 不写 = [exec]
```

- 只对 **member job**（非 leader）生效，只在**同项目**内，只允许 `submit_agents` 里的 agent（默认 `[exec]` = 只能起一条命令）。
- 谁开的闸看**发起提交的那个 job** 的 agent 与 role：两者任一 `can_submit: true` 即放行（role 适合"这个 preset 是调度者"，agent 适合 `--agent` 直跑）。
- 新 job 的 tags 会带 `submitted_by_job:<发起 job id>`，`gofer job list --tag submitted_by_job:<id>` 就能找出"哪个 job 起的活"。
- 被拒文案：`job credential may not submit a job` / `job credential may not submit agent omp`。

## 升级影响（重要）

**job 里不再有 server token**。依赖它的用法会收到 401/403，需要按下面改：

| 原来的用法 | 现在怎么做 |
|---|---|
| job 里跑 `gofer job list/show/comment`（CLI） | 不用改：CLI 默认用 `GOFER_JOB_TOKEN`，读与评论本来就允许 |
| job 里 `gofer job run` / 建 plan / 改 todo（需要高权限） | 默认不行（这是有意的收紧）。要么给该 job 的 agent/role 配 `can_submit`，要么由人在外面跑 |
| job 内脚本 `curl` 调 gofer API，硬用继承来的 token | 改用 `$GOFER_JOB_TOKEN` + `$GOFER_SERVER_ADDR`（`curl -H "Authorization: Bearer $GOFER_JOB_TOKEN" $GOFER_SERVER_ADDR/v1/jobs/<id>`），并按上表确认那个动作对 job 凭证是放行的 |
| job 里跑 `gofer …` 时"顺便"读到了 server 的 `.env` | 已经不可能：`GOFER_CONFIG_DIR` 不再继承，且 job 内的 dotenv 加载跳过所有 `*_TOKEN` 键。要用 server 的 `.env` 请显式 `-c <config>`；要调 API 就用 `$GOFER_JOB_TOKEN` |
| job 里写死 `gofer`（PATH 查找） | 现在解析到运行该 job 的那个 gofer（`GOFER_BIN` 是它的绝对路径）。若确实要另一个版本，写绝对路径 |
| 老脚本确实必须拿 server token | 给**那个项目**配 `job_env_allow: [GOFER_TOKEN]`（会被记 `job.env_allowed`，可审计）。只是图省事就不要这么做——这正是本次要关的口子 |
| 老 worker（协议 < 11）上跑的 job | 收不到凭证：job 照常跑，但它在 job 里调 API 会 401。hub 会记 `job.credential_skipped {reason:"worker_protocol"}`，升级 worker 后自然生效 |
| peer-http（非 worker 的远端 runner） | 本次不下发凭证（那套 transport 没有携带字段）：远端 job 里没有 `GOFER_JOB_TOKEN` |

`as_job` 字段**已废弃**（server 侧对所有 caller 忽略，只为不把老客户端打成 400 而保留解析；CLI 不再发送）。它会在 v0.61 删除。

## 事件

记在 job 自己的 scope 上：

| 事件 | 何时 | 详情 |
|---|---|---|
| `job.env_allowed` | 项目的 `job_env_allow` 真的放行了某个继承变量 | `{keys:[...]}` |
| `job.credential_skipped` | 目标 worker 协议 < 11，拿不到凭证，job 照跑 | `{reason:"worker_protocol"}` |

**吊销不记事件**：凭证随 job 终态失效是终态的必然推论，记一行只会把 `job.terminal`/`job.needs_review` 从"最后一条事件"的位置挤掉（SSE、验收台、`job watch` 都依赖这点）。要查吊销时间就查凭证行自己的 `revoked_at`（`created_at` → `revoked_at` 即它的存活区间）。

## 排查

```bash
gofer job events <job-id>            # env_allowed / credential_skipped
gofer job show <job-id>              # request_json 里没有 token（凭证不进审计体）
```

job 内自检（agent 侧，三条都不用猜）：

```bash
env | grep ^GOFER_                   # 只有 JOB_TOKEN/SERVER_ADDR/BIN/ID/CWD/RESULT_DIR；
                                     # 没有 GOFER_CONFIG_DIR，也没有别的 *_TOKEN
command -v gofer                     # 应等于 $GOFER_BIN
echo "$GOFER_BIN"                    # 运行这个 job 的 gofer 绝对路径
```

- job 里 CLI 报 401：确认 `GOFER_JOB_TOKEN` 是否在环境里（worker 上协议 < 11 时没有），以及 job 是否已经结束（终态即吊销）。
- job 里 CLI 报 403 且文案是 `job credential may not …`：这个动作不在上表里。要么改由人做，要么用 `can_submit` 那个出口。
- job 里 `env | grep ^GOFER_CONFIG_DIR` 有值：说明该 job 的项目 `job_env_allow` 显式放行了它（看 `job.env_allowed` 事件），或者跑的是旧版 serve——两种情况都要当"凭证指针又漏出来了"处理。
- job 里 `gofer --version` 与 server 版本不一致：脚本大概覆盖了 `PATH`；改用 `"$GOFER_BIN"`。
- 想知道某台 worker 上的 job 有没有凭证：看该 job 的 `job.credential_skipped` 事件。

## 已知边界

- **同用户进程的边界（务必如实理解）**：job 与 server **以同一个系统用户运行**。SEC-01 关掉的是**顺手/默认路径**上的越权——环境变量里的 token、CLI 自动加载的 `.env`、默认 token 链；它**挡不住**一个下定决心的 agent：同用户下它仍可以直接去读 server 的配置文件（`<config-dir>/config.yaml`、`<config-dir>/.env`、`.gofer.project.yaml`）或直接读 `server.token_env` 指向的值，拿到 server token。**真正的隔离要让 agent 跑在另一个系统账号下**——例如放到以独立账号启动的 worker 上（worker 上的 job 只能看到 worker 自己那台机的环境与文件，且凭证由 hub 签发、终态即吊销）。
- **job 内的 `gofer` 与运行它的 gofer 同版本**：`PATH` 第一项是 `os.Executable()` 所在目录，并导出 `GOFER_BIN`。所以 job 里的 `gofer` 就是 serve（或 worker）的那个二进制，不再是主机 PATH 上碰巧装的版本——v0.60 真机验收里那条"0.53.1 的 CLI 对 0.60.0 的 server"不会再出现。要跨版本调 CLI 就显式写绝对路径。
- **凭证泄露面**：token 在 job 进程环境里，agent 可读可打印。因为它只在该 job 存活期内有效、权限表很窄、终态即吊销，泄露的后果被限定在"这个 job 本来能做的事"。
- **leader 仍可能"想得不对"**：凭证只保证它**做不了越权的事**，不保证决定正确。轮次封顶、人评论即接管、`plan pause` 仍是兜底。
- peer-http 与协议 < 11 的 worker 上的 job 没有凭证（见上）。
