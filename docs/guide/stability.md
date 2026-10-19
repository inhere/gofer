# 稳定性、发版与升级

gofer 现在处于 **1.0 之前**：核心的那条线（提交 job、远程执行、验收、日志与用量）已经稳定，用在你真正在意的工作上没问题；但外围功能还在长，**minor 版本也可能带不兼容变化**。这一页说明哪些可以放心依赖、哪些还在演进，以及怎么升级和退回。

判断依据始终是**代码与 [CHANGELOG](../../CHANGELOG.md)**——本页只是给一个当前的概览，有出入时以那两处为准。

## 可以放心依赖的部分

这些是主干，接口与行为变化会走 CHANGELOG 的「变更 / 移除」，并尽量保持兼容：

| 部分 | 说明 |
|---|---|
| job 生命周期 | 提交（CLI / HTTP / MCP / Web）、状态机、日志与事件、diff 与提交、用量与成本、取消 / 续跑 / 重跑 |
| project / agent / runner 三元组 | 项目准入（`allowed_agents` / `allowed_runners` / `allow_exec` / `allow_interactive`）、三种 agent 类型（cli / acp / exec）、本机与 worker runner |
| worker | 登记、连入、心跳与断线恢复（`recovering`）、POLICY 模式的项目下发与路径映射、`worker doctor` |
| 验收与质量关口 | `--verify`、`--review` + accept / reject、`--acceptance` / `--scope`、`--hold` 待批 |
| plan 与 todo | todo 链（`--after`）、按指派人自动派发、`plan run / pause / resume` |
| 定时任务 | cron、一次性、webhook 触发 |
| 终端会话中继 | `init hooks`、`session relay`、`say` / `resume` / `nudge`、Web 工作台 |
| 仓库 tracker | `.gofer/tracker/*.jsonl`、`repo prime`、`issue` / `memory`、`issue brief` / `plan brief` |
| Web 控制台与 HTTP `/v1` | 今天 / 工作台 / 验收台 / Jobs / Plans / Sessions / Issues / Runners / Projects / 设置；`/metrics` |
| 运维 | 受管 server（Windows 登录任务 / Linux systemd）、`serve reload` 热重载、远程升级 worker、隧道、`tool cp` |

## 标为实验性 / 默认关闭的部分

用之前知道它在演进：接口可能变、默认值可能调、边界条件可能还有没覆盖到的角落。

| 部分 | 现状 | 提示 |
|---|---|---|
| **workflow 模板** | 步骤链、扇出 / 汇合、子工作流、多 agent 对比与 `join: pick` 已可用；模板库与 Web 模板向导较新 | 按 hunk 合并、`join: judge`（裁判择优）还没做；对比流程建议先在小任务上跑通 |
| **leader 回合** | 按 plan opt-in（默认关），成员终态唤醒 leader 写评论 / 接着派活，有轮次上限，leader **不能** accept / reject | 只在信任的 agent 上开；人的评论里出现 `@agent` 会真的派 job |
| **信箱** | `session mail` 发信 / 读信 / 回信、Web `/mail`、投递器与保存期都已落地 | 送达依赖收件方的能力（hook 注入、ACP `job say`、终端送话），不是每条路径都即时；投不出去的信会进「待指派」 |
| **决策 hook** | `server.decision_hooks` 的 D1（提交前 `job.submit` / 交付前 `job.deliver`）已落地，只能否决或收紧 | D2（ACP 交互自动应答）、D3（会话授权自动应答）未做；出错默认按“否决”处理，别指望它兜底 |
| **工作区与交付** | `--worktree` 隔离、`job deliver`（merge / push / PR）、并行 worktree 都已可用 | 交付里 `push` 仍是不可逆操作，配合 `--hold` 与人的授权；容器里建的 worktree 在主机上跑 git 会出问题 |
| **工作项 / 收集箱 / 管家** | `gofer work` 与 Web Works 页、随手记与分派、steward 已落地 | steward 默认关闭（会消耗模型额度）；整理器需要一台装了只读 CLI agent 的机器 |
| **调用方上限 `ceiling`** | 提交与路由判定已覆盖 | 已知未覆盖：聚合与计数（today / stats / digest）、`/v1/ws` 推送主题过滤、上限收紧后已打开的长连接 |
| **沙箱 / 多用户** | 没有 | 容器沙箱 runner（一次性容器、默认断网）与 agent 独立 OS 账号都还没做。现在执行隔离靠目录锁 + worktree + 容器 worker |

不确定某个功能算哪一档：看 `gofer <命令> -h`、CHANGELOG 里它第一次出现的条目，以及 [`docs/gofer-enhancements-roadmap.md`](../gofer-enhancements-roadmap.md) 的状态标记（✅ 已落地 · 🚧 部分 · ⏳ 待做 · ❄ 暂缓）。

## 版本与兼容策略

- 版本号遵循语义化版本。**1.0 之前 minor 版本也可能含不兼容变化**，每个版本的不兼容点写在 CHANGELOG 的「变更 / 移除」里；发版前值得扫一眼。
- 不积累永久兼容层：仍需保留的兼容分支会打 `// DEPRECATED(v<引入版本>): remove in v<+3>` 标记并在文档记一笔，几个版本后删除。老别名、预发布行为回退这类“没人用”的路径会被直接剔除。
- 数据库是 SQLite，schema 变更走增量迁移；升级不需要手工操作，**回退**时新版本写过的数据旧版本会忽略（新功能的数据会留在库里）。
- server 与 worker 之间有协议版本：worker 版本过旧时，server 会拒绝它不支持的请求（错误里写明协议门槛），升级 server 后记得把 worker 一起升级。

## 三种装法

| 方式 | 含 Web 控制台 | 适用 |
|---|---|---|
| Release 包（`eget install inhere/gofer`、GitHub Releases 归档） | **有**（内嵌） | 正常使用，推荐 |
| `go install github.com/inhere/gofer/cmd/gofer@latest` | **没有**（控制台只显示占位页） | 只要 CLI / API / MCP，或自己带前端 |
| 源码构建 `make web build` | 有 | 改 gofer 本身 |

三种方式装出来的二进制是同一个 CLI，区别只在有没有内嵌前端。

## 升级

**受管 server（推荐）**

- Web：设置 → 关于 → 检查更新 → 更新。server 先排空（在跑的 job 跑完；期间新提交的 job 停在 `paused_for_update`），再换二进制重启，失败自动回滚。
- CLI：`gofer serve upgrade --binary <新二进制>`（`--drain-timeout` 控制排空等待），结果看 `gofer serve upgrade status`。
- 只更新配置不换二进制：`gofer serve reload`（热重载；runner 实例、retention 启停这类需要重启的键会被提示）。

**worker**

- `gofer worker upgrade <worker-id>`：默认上传 server 自己的二进制（要求同 os / arch），也可以 `--file <路径>`；排空、交接、失败回滚。`--force` 不等在跑的 job。

**自己起的进程**

停掉（`gofer serve stop` / Ctrl+C）、换二进制、重新启动；配置不用动。

## 回滚

- 留着上一版的二进制文件：受管 server 用 `gofer serve upgrade --binary <旧二进制>` 换回去；worker 用 `gofer worker upgrade <id> --file <旧二进制>`。
- 升级执行器本身有回滚：新二进制起不来或注册失败时会自动退回到旧二进制，并把结果持久化（`gofer serve upgrade status` 可查）。
- 配置是纯文本、升级不改它；改坏了直接改回，再 `gofer serve reload`。
- 关于发版与换二进制的完整步骤，见 [`docs/runbook/release-and-binary-swap.md`](../runbook/release-and-binary-swap.md)。
