# 同类 agent 工具调研：Orca / Paseo / Magpie（2026-10-03）

> 参考资料，非可信源；基于项目主页与公开评测摘要，未读源码。口述名对应：orca → stablyai/orca；pseao → Paseo（getpaseo/paseo）；magpia → magpie（yetone/magpie 模型路由，或 liliu-z/magpie 对抗评审）。

## Orca（stablyai/orca，MIT）

桌面 ADE（macOS/Windows/Linux）+ iOS/Android 配套 App + CLI。

- 每个 agent 一个 git worktree，支持 Claude Code / Codex / OpenCode 等数十种 CLI agent。
- 同一 prompt 扇出给 N 个 agent 各跑一份，再对比；按 hunk 勾选合并。
- 在 agent 的 diff 上逐行批注并回传给 agent 返工。
- "未读"状态突出需要人介入的任务；手机看状态、发追问、完成推送。
- SSH 远程 worktree；从 GitHub / Linear issue 开 worktree。

## Paseo（getpaseo/paseo，Apache-2.0）

自托管 Node 守护进程 + 桌面 / 移动 / web / CLI 客户端（WebSocket + relay，设备配对）。

- 统一驱动数十种 agent CLI，手机远程控制，语音输入。
- TypeScript client SDK。
- 内置 skill：handoff（如 Claude 规划 → Codex 实现）、committee（多 agent 联合分析）。

## Magpie

- yetone/magpie（MIT）：菜单栏 App + 本地网关，按 agent 选择模型与厂商、协议转换、profile 一键切换、路由组故障转移、带用量上限的网关 key、OTLP / tracing 导出。
- liliu-z/magpie：多 AI 独立评审同一 PR 并多轮辩论，再由读真实代码的验证者逐条确认、过滤误报。

## 对 gofer 的借鉴（按价值排序）

| # | 借鉴点 | 来源 | gofer 现状 / 落地思路 |
|---|---|---|---|
| 1 | 同 prompt 扇出到多 agent / worktree，结果对比择优 | Orca | 已有 worktree 与派发；缺扇出编排与对比视图，可做成 plan / workflow 模式 |
| 2 | diff 行级批注回传 agent 返工 | Orca | web job 详情已有 diff；加批注 → 作为持续会话下一轮 say 或新 job 的 prompt |
| 3 | 预置多 agent 模板：handoff（规划→实现）、committee / 对抗评审 + 验证者 | Paseo、magpie B | 基于现有 workflow 链实现，成本低 |
| 4 | "需要我处理"未读标记 | Orca | Board / Sessions 统一的待处理计数与已读 |
| 5 | 集中配置 agent 的模型 / 厂商 profile 与故障转移 | magpie A | server 下发 agent 参数，额度耗尽自动换 |
| 6 | OTLP / tracing 导出 | magpie A | job 耗时、token、失败率 |
| 7 | client SDK、手机语音输入 | Paseo | 已有 CLI / MCP / PWA；语音可用浏览器语音转文字 |

已有、不必借鉴：worktree 隔离、远程执行（worker 机制）、CLI、手机推送（钉钉 / 飞书 + 终端会话中继）、配置下发。

## 来源

- https://github.com/stablyai/orca ，https://github.com/stablyai/orca/discussions/681
- https://github.com/getpaseo/paseo ，https://paseo.sh/
- https://github.com/yetone/magpie ，https://github.com/liliu-z/magpie
- https://www.augmentcode.com/tools/open-source-agent-orchestrators
