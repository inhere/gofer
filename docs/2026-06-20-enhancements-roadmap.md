# Gofer 增强路线（Enhancements Roadmap）

> 目标三轴：**① 更方便使用** · **② 更好地利用各种 agent 完成任务** · **③ 始终可观察、可审计 agent 的工作**。
> 本文是思考与优先级清单（非实施计划）；选定项再各自出 design/plan。多 hub HA 不在此（独立大 Epic，见 [`TODO.md`](TODO.md) §大型 Epic）。

## 现状基线（已有，不重复做）

单 job = 单 agent / 单 prompt-or-argv / 单项目 / 单 cwd；产出以**日志**为主，状态/元数据入 SQLite。已具备：异步+同步提交、md+yaml、ws 远端 worker + 标签调度、运行中交互、`/v1/runners` 健康名册、`caller_id`/`worker_id` 审计字段、retention、Web 控制台 + MCP。

**核心缺口**：agent 跑完产生的**文件产物**取不回（只回日志）；看不到"这个 agent **到底改了什么/做了什么**"的完整审计视图；任务只能**单步**（不能 codex 生成→exec 测试→claude 评审串起来）；结果**非结构化**（只有 exit_code+stdout）；CLI **日常操作**仍单薄。

---

## ① 更方便使用

| 编号 | 增强 | 价值 | 工作量 | 说明 |
|---|---|---|---|---|
| E1 | **产物回取 (artifacts)** | 高 | 中 | job 除日志外的产出文件（构建物/生成代码/报告）：`GET /v1/jobs/{id}/artifacts` 列举 + 下载；agent/wrapper 可声明产物（约定 `<result_dir>/artifacts/` 或清单）。Web 详情可下载。**这是让"会产文件的 agent"真正可用的前提**。 |
| E2 | **CLI 日常补全** | 高 | 低 | 补 `job list`（现仅 web/http）、`job watch <id>`（终端实时 tail SSE）、`job rerun <id>`（复用 request_json 一键重跑）、shell 补全。日常摩擦最大、最便宜。 |
| E3 | **引导 / 配置校验** | 中 | 低 | `gofer init`（生成示例配置 + 引导登记首个项目/agent）、`gofer config validate`；example 配置补 `callers/workers/peer-http/worker` 段（现缺，无法 copy 即用）。降低上手门槛。 |
| E4 | **任务模板库** | 中 | 中 | 命名的 md+yaml 模板 + 变量，`job run -t <template> --var k=v`。把重复 prompt 沉淀复用，天然接 md+yaml 提交。 |
| E5 | **job 标签 + 搜索过滤** | 中 | 中 | 提交带 `tags`；看板/列表按 tag/agent/runner/时间检索（现仅 status/project/caller）。job 一多就需要。 |

## ② 更好地利用 agent 完成任务

| 编号 | 增强 | 价值 | 工作量 | 说明 |
|---|---|---|---|---|
| E6 | **结构化结果 (structured result)** | 高 | 中 | agent 除 exit_code/日志外回一份结构化结果（约定 `result.json` 或新 `bridge_report_result` 通道），入库 + API/Web 展示。"总结失败用例"返回可解析摘要而非裸 stdout——也直接增强审计(③)。 |
| E7 | **多步 / 工作流 (job 链)** | 高 | 大 | 提交一串/一个 DAG 的 job，**上一步产出喂下一步**（codex 生成 → exec 跑测试 → claude 评审）。最大的"完成任务"杠杆，也是最大范围——需独立 design。先做最小版：线性 chain + `${steps.N.output}` 引用。 |
| E8 | **审批门 (approval gate)** | 中 | 中 | 运行中交互扩一种"高危动作审批"（agent 要跑 `rm -rf`/推送/外发前先求批）。复用 `pending_interaction` 机制；兼顾"完成任务"与审计/安全(③)。 |
| E9 | **并行 fan-out / 对比** | 中 | 中 | 一个任务派给 N 个 agent/worker 并行，汇总对比（judge-panel 模式）。利用 worker 舰队；接 E7 工作流。 |
| E10 | **mcp-agent 类型** | 中 | 中 | 新 agent type：job 调用"本身是 MCP server"的外部能力（与 runner 正交、可与 worker 组合）。让 gofer 编排 MCP 工具。见架构 §9.2 扩展点。 |
| E11 | **上下文 / secret 注入** | 中 | 中 | per-job env、范围化 secret 注入、附加上下文文件挂载——让 agent 有完成任务所需的环境（现 env 仅 per-agent 配置）。注意 secret 不入日志/库（SR403）。 |

## ③ 观察 / 审计 agent 的工作

| 编号 | 增强 | 价值 | 工作量 | 说明 |
|---|---|---|---|---|
| E12 | **"改了什么" 审计（diff 快照）** | 高 | 中 | job 前后对项目目录做 git diff / 文件变更快照，详情页展示"这个 agent 改了哪些文件"。对**代码类 agent 的审计是杀手级**——目前完全看不到。 |
| E13 | **job 事件时间线 (audit trail)** | 高 | 中 | 每个生命周期事件（caller X 提交 → 派发 worker Y → 状态变更 → 交互问答 → 取消）落 append-only 审计流，`GET /v1/jobs/{id}/events` + 详情时间线。现只有终态字段、无过程留痕。 |
| E14 | **通知 / 事件外发** | 高 | 中 | job `done/failed/pending_interaction` 时 webhook / **MQ 外发**（接公司 MQ 网关 SR501/SR507），把人/系统拉进来——别让卡住或失败的 agent 无人知。审计 + 协同双赢。 |
| E15 | **渲染命令可见** | 低 | 低 | 详情页显式展示"实际执行的命令"（cli-agent 渲染后的 argv / exec 的 argv，request_json 已存）——"到底跑了什么"一眼可见。E12/E13 的轻量前置。 |
| E16 | **指标 `/metrics`** | 中 | 低 | Prometheus：按 status/agent/runner 的 job 数、时长、队列深度、worker 利用率。把舰队从"点态 /v1/runners"变成"时序可观测"。 |
| E17 | **per-caller 配额 / 限流** | 中 | 中 | 按 caller/agent 的并发与速率上限（治理）；接 caller_id 体系。 |

---

## 建议优先级（下一步做什么）

**第一梯队（高价值、范围可控，直击三轴核心缺口）：**
1. **E1 产物回取** + **E6 结构化结果** —— 让"会产文件/产结构化输出的 agent"真正可用（②），且产物/结果即审计材料（③）。
2. **E12 改了什么 (diff 快照)** + **E15 渲染命令可见** —— 代码类 agent 审计的杀手级补齐（③），E15 便宜先行。
3. **E2 CLI 日常补全（list/watch/rerun）** + **E3 引导/example 补全** —— 最低成本的日常易用提升（①）。

**第二梯队：**
4. **E13 事件时间线** + **E14 通知/MQ 外发** —— 过程留痕 + 卡住/失败有人知（③，接公司 MQ）。
5. **E7 工作流（线性 chain 最小版）** —— 单步→多步的关键一跃（②），需独立 design。
6. **E8 审批门** + **E16 metrics** —— 安全治理 + 时序可观测。

**探索 / 按需：** E4 模板库 · E5 标签搜索 · E9 fan-out · E10 mcp-agent · E11 上下文注入 · E17 配额限流。

> **一句话主线**：先把"agent 产出的**东西**（文件/结构化结果）取得回、看得见它**改了什么**"补齐（E1/E6/E12/E15），再补"**日常顺手**（E2/E3）"，然后才向"**多步编排**（E7）与**主动通知**（E14）"扩展——始终保证每一步都可观察、可审计。
