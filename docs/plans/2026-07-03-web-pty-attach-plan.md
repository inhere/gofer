# WEB-03 浏览器 pty 交互 实施计划（总纲）

> 设计：[`../design/2026-07-03-web-pty-attach-design.md`](../design/2026-07-03-web-pty-attach-design.md)（v0.8）。评审：[`../review/2026-07-03-web-pty-attach-codex-review.md`](../review/2026-07-03-web-pty-attach-codex-review.md)（host codex 两轮）。
> 本文只保留**阶段总纲 + 进度**；各阶段详情见子文档（SR1105）。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-04 | Claude | 初版：P0 已完成回填，出 P1 详细计划。 |

## 阶段总纲

| 阶段 | 范围 | 状态 | 详情 |
|---|---|---|---|
| **P0 spike** | `internal/pty`(接口+creack unix+vendored conpty)/`internal/ptyrelay`(ring+两层背压+lease)/`internal/runner/pty`(PtyRunner+PtySession)+ job 选择 seam；证 3 点 | ✅ **完成** commit `7226251` | 见设计 v0.8 修订记录（3 证明点全成立 + 6 发现回填） |
| **P1** | serve 侧：协议(dispatch nonce+capability)+config 字段+admission 五闸/capability 预解析+relay nonce+专用 pty ws 端点(serve leg)+ptyRelay remote source+attach ticket+attach ws+持久化；**P0 回填**(runner.Request 字段) | ⬜ **本次出计划** | [`web-pty-attach/P1-plan.md`](./web-pty-attach/P1-plan.md) |
| **P2** | worker 端到端：`PtyRunner` 接真 ptmx（P0 已骨架）+ **worker 拨第二条 pty ws**(binary 字节泵)+ worker-client→ptyrunner seam + **三处收敛的取消协议**(有序关闭+ack) + capability 广告；input/output/resize/cancel/断连全链路 | ⬜ 待 P1 后 | 待出 |
| **P3** | cast 加密录制 + `pty_sessions` 一等表 + retention/prune 顺序 + `/pty/recording` gate | ⬜ | 待出 |
| **P4** | 前端 `AttachTerminal.vue`(xterm binary+ticket) + JobDetail 接入 + e2e 矩阵 | ⬜ | 待出 |

## P1/P2 边界（重要）

- **P1 = serve 侧可独立单测的一切**（协议帧、config、admission、nonce、serve 的 pty ws 端点、relay、ticket、attach ws），worker 侧用**测试内 fake 连接**验证端点契约。
- **P2 = worker 侧执行与跨进程时序**（真 ptmx 接线、worker 拨 pty ws、字节泵、取消协议三处收敛）——改动面最大、契约最易偏差（见 P1 map 收尾风险），单列。
- 故 P1 落地后 serve 端点存在但**未端到端联通**（无真 worker 拨入）；端到端在 P2。P1 验收以 serve 侧单测 + 现有全量测试零回归为准。

## 进度跟进

- [x] P0 spike（commit `7226251`）
- [x] P1 完成(T0-T8 + codex 代码审查 + 4阻断高修复, commit 4123243..ac4e807)
- [ ] P2 / P3 / P4
