# gofer 配置与使用优化 实施计划总纲（config-optimize）

> 关联设计：`docs/design/2026-07-09-config-federation-design.md`（联邦 §1–12 + 配置默认化 §13 + per-job agent flags §14）
> bd：epic 归 h-aii-xu64；子项 xu64.10(联邦) / xu64.13(默认化) / xu64.12(agent flags)
> 本总纲把设计拆成可执行分期；子计划保留主纲引用链接（SR1105）。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-09 | inhere/claude | 初稿：分期总纲 + P0 快赢子计划 |

## 分期总纲

| 期 | 内容 | 设计章节 | bd | 子计划 | 状态 |
|---|---|---|---|---|---|
| **P0 快赢** | §13 配置默认化（allowed_agents 空=默认可用 / local 默认）+ §14 per-job agent flags | §13/§14 | xu64.13 / xu64.12 | [P0-quickwins-plan.md](./P0-quickwins-plan.md) | ◑ 代码完成(48b1634，go test 全绿)，待部署冒烟；T10 web 未做 |
| **P1 联邦核心** | 能力上报变功能性（Register 补字段 + 去 display-only）+ federated view（`CapabilitiesFor`）+ submit 按 runner 取能力校验 + selector 带 projects/agents | §5/§6.1-6.3/§7 | xu64.10 | [P1-federation-plan.md](./P1-federation-plan.md) | ☐ 待实施 |
| **P2 UI + 可观测** | `/v1/capabilities` + NewJob/NewSchedule 级联选择 + 节点信息展示(os/arch/gofer 版本) | §6.4/§6.2/G5 | xu64.10 | P2-ui-observability-plan.md（待拆） | ☐ 未拆 |

**排期理由**：P0 两项改动小、彼此独立、不依赖联邦，能立刻改善日常使用（消除项目重复配 agent + 非交互 job 授权卡死），先落。P1 是联邦核心（medium），P2 依赖 P1 的能力数据。

## 进度跟踪

- [x] P0 快赢（§13 + §14）— 代码完成 48b1634，go test 全绿，待部署冒烟（T10 web 高级区未做）
- [ ] P1 联邦核心
- [ ] P2 UI + 可观测

> 每子阶段完成后更新此处 checkbox + 对应 bd + Git 提交（SR1202/1201）。

## 依赖与环境

- 纯 Go + web 改动，无新增中间件/DB 迁移（P0/P1）。
- 验证：容器内 `go test ./...`（Linux 绿为准）；web 走主机 `pnpm typecheck && pnpm build`（容器 node_modules 勿动）。
- 冒烟：需要一个在线 worker 验联邦（P1）；P0 仅 local 即可验。
