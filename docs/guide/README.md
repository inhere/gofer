# gofer 用户指南

这组文档写给**用 gofer 的人**：拿到 gofer 之后想知道它是什么、十分钟内跑通第一个 job、遇到问题能自己查。

gofer 自己是什么，一句话：**把 Claude Code、Codex 或任意命令，作为可追踪、可验收的 job，在你自己的机器上、在真实的项目目录里运行的调度台。** 你在主力机器上跑一个 server，按需在其他机器或容器里跑 worker；人和 agent 通过 CLI、HTTP API、MCP 和 Web 控制台访问同一套 job 记录。

## 从这里开始

| 文档 | 什么时候读 |
|---|---|
| [concepts.md](concepts.md) | 第一次接触 gofer：server / worker / runner / project / agent / job / plan / session 各是什么，彼此怎么组合 |
| [scenarios.md](scenarios.md) | 想照着做：在主机跑命令、派 agent 改代码并验收、用 plan 串多步、从 web 接管终端会话、手机上处理待决策、加一台 worker、定时任务 |
| [configuration.md](configuration.md) | 要写配置：配置查找顺序、运行模式（server / worker / client）、项目准入规则 |
| [features.md](features.md) | 想知道 gofer 都提供什么：job、验收、编排、会话、tracker、观测、入口 |
| [security.md](security.md) | 准备让别人或别的机器连进来之前：token 意味着什么、监听地址、权限与上限、秘密处理 |
| [troubleshooting.md](troubleshooting.md) | 连不上、401、job 排队不动、worker 离线、agent 没检测到、配置校验报错、Windows 路径、升级回滚 |
| [stability.md](stability.md) | 想知道哪些功能可以放心依赖、哪些还在演进，以及怎么升级和回滚 |
| [development.md](development.md) | 要改 gofer 本身或从源码构建 |

只想知道命令怎么用：`gofer <命令> -h` 永远是最准的。

## 为什么用它

真正用编程 agent 干活时，很快会遇到同样几类问题：

| 问题 | gofer 的做法 |
|---|---|
| 想用的 agent 在另一台机器上（主机与容器、Windows 与 Linux、GPU 机器） | 一个 server、多个 worker；在任何地方提交，job 在项目和 agent 所在的地方执行 |
| agent 的运行记录淹没在终端滚动里 | 每次运行都是一个 job：状态、日志、diff、提交、用量、事件时间线，存在 SQLite |
| 「agent 说做完了」不等于验收通过 | 验证命令、验收标准、范围检查，以及只有人能接受的 `needs_review` 关口 |
| 多个 agent 并行时互相踩同一份工作区 | 目录锁、`--worktree` 隔离与显式合并回主工作区 |
| 长任务需要多个步骤、多个 agent | 带依赖 todo 的 plan、可扇出 / 择优的 workflow、定时任务与唤醒 |
| 人一离开，会话就停在那里等你 | 会话中继、Web Push 与「今天」决策队列，可以在浏览器或手机上作答 |
| 每个新 agent 会话都从零开始 | 仓库 tracker：issue、记忆、`repo prime`、`issue brief` / `plan brief`、经验候选 |

## 三条阅读路径

- **只想把活派出去**：concepts.md（读到 job 那节就够）→ scenarios.md 的第 1、2 个场景。
- **要给团队或几台机器用**：concepts.md → scenarios.md 第 6 个场景（加 worker）→ security.md 整篇。
- **要接进自己的 agent / 脚本**：features.md 的「入口」一节 → [`docs/reference/http-api.md`](../reference/http-api.md)，或直接让 agent 读 [gofer-usage skill](../../skills/gofer-usage/SKILL.md)。

## 和 gofer-usage skill 的分工

- **本目录（docs/guide）写给人看**：概念、为什么这样、完整命令示例、排障思路。
- **[`skills/gofer-usage/`](../../skills/gofer-usage/SKILL.md) 写给 agent 看**：供 Claude Code / Codex 等 agent 在会话中按需加载的速查与细节（命令索引、配置字段、协议门槛）。它随每个用户可见改动同步更新，是功能细节的权威出处。
- **HTTP 接口与字段**在 [`docs/reference/http-api.md`](../reference/http-api.md)；**权限**在 [`docs/reference/permissions.md`](../reference/permissions.md)；**运维步骤**在 [`docs/runbook/`](../runbook/)。

本目录提到 agent 侧用法时只给一句指路，不复制 skill 的细节。
