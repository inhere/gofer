# Gofer 增强路线（Enhancements Roadmap）

> 目标三轴：**① 更方便使用** · **② 更好地利用各种 agent 完成任务** · **③ 始终可观察、可审计 agent 的工作**。
> 本文是思考与优先级清单（非实施计划）；选定项再各自出 design/plan。多 hub HA 不在此（独立大 Epic，见 [`TODO.md`](TODO.md) §大型 Epic）。
>
> **状态图例**：✅ 已落地 · 🚧 部分落地（最小版/留后续） · （空）未做。
> **更新记录**：2026-06-21 标记 E1-E17 完成态（E16/E17 本轮落地）；新增 E18-E27（来自 `tmp/tmp.md` 想法整理）。

## 现状基线（已有，不重复做）

单 job = 单 agent / 单 prompt-or-argv / 单项目 / 单 cwd；状态/元数据入 SQLite。已具备：异步+同步提交、md+yaml、ws 远端 worker + 标签调度、运行中交互、`/v1/runners` 健康名册、`caller_id`/`worker_id` 审计、retention、Web 控制台 + MCP。

**原核心缺口已基本补齐**：产物回取（E1✅）、改了什么 diff（E12✅）、事件时间线（E13✅）、结构化结果（E6✅）、多步工作流线性版（E7🚧）、CLI 日常（E2✅）、指标与配额（E16/E17✅）均已落地。

**新核心缺口（本轮想法聚焦）**：① 任务只能**人工逐个提交**，不能定时/失败自动重试/agent 自动应答——缺**自主化**；② gofer 未融入日常工作环境（IM 提交&通知、IDE 跳转）——缺**连接/入口**；③ 工作流只能**单项目线性**，不能子工作流嵌套/跨项目衔接（开发→测试）——缺**工作流深化**；④ 无插件扩展点（hooks）。

---

## ① 更方便使用

| 编号 | 增强 | 价值 | 工作量 | 说明 |
|---|---|---|---|---|
| E1 ✅ | **产物回取 (artifacts)** | 高 | 中 | job 除日志外的产出文件（构建物/生成代码/报告）：`GET /v1/jobs/{id}/artifacts` 列举 + 下载；agent/wrapper 可声明产物（约定 `<result_dir>/artifacts/` 或清单）。Web 详情可下载。**这是让"会产文件的 agent"真正可用的前提**。 |
| E2 ✅ | **CLI 日常补全** | 高 | 低 | 补 `job list`、`job watch <id>`（终端实时 tail SSE）、`job rerun <id>`（复用 request_json 一键重跑）、shell 补全。日常摩擦最大、最便宜。 |
| E3 ✅ | **引导 / 配置校验** | 中 | 低 | `gofer init`（生成示例配置 + 引导登记首个项目/agent）、`gofer config validate`；example 配置补 `callers/workers/peer-http/worker` 段。降低上手门槛。 |
| E4 | **任务模板库** | 中 | 中 | 命名的 md+yaml 模板 + 变量，`job run -t <template> --var k=v`。把重复 prompt 沉淀复用，天然接 md+yaml 提交。与 E18（工作流导出）同属"复用"主题。 |
| E5 ✅ | **job 标签 + 搜索过滤** | 中 | 中 | 提交带 `tags`；看板/列表按 tag/agent/runner/时间检索。job 一多就需要。 |
| E18 | **工作流导入导出 json** | 中 | 低 | 工作流定义 dump/提交（`spec_json` 已存，近现成）。注意 prompt 内 secret 不导出、`${steps.N}` 引用原样保留、schema 版本兼容。= 工作流模板库雏形。**便宜先行**。 |
| E19 | **文件 / 产物预览** | 中 | 中 | Web 在线预览（md/图/json/代码高亮）。**二义先定**：(a) job 产物 artifacts 渲染——小、安全边界清晰（限 `result_dir`），E1 已有下载加渲染即可，**先做**；(b) 项目工作目录文件浏览——大，需文件树 API + 路径安全（防 `../`、黑名单 `.env`/`.git`）。 |
| E21 | **主机侧动作（编辑器打开等）** | 中 | 低 | 一键用主机编辑器打开项目（`code <host_path>`）/ reveal / 开终端。⚠️ gofer 在容器、编辑器在主机：**必须复用现有 codex-bridge 主机通道**（`host.docker.internal`），抽象成一类"主机侧动作"。仅对有主机 bridge 的部署可用。 |
| E23 | **定时任务（内置 cron）** | 中 | 中 | `schedules` 表 + 复用现有 sweeper loop 范式定时提交 job/工作流。问题：错过补偿、多 hub 重复触发（单 hub 不问题）。接 E24（定时+重试）、E18（定时跑导入的工作流）。属**自主化 epic**。 |

## ② 更好地利用 agent 完成任务

| 编号 | 增强 | 价值 | 工作量 | 说明 |
|---|---|---|---|---|
| E6 ✅ | **结构化结果 (structured result)** | 高 | 中 | agent 除 exit_code/日志外回一份结构化结果（约定 `result.json`），入库 + API/Web 展示。返回可解析摘要而非裸 stdout——也直接增强审计(③)。 |
| E7 🚧 | **多步 / 工作流 (job 链)** | 高 | 大 | 提交一串 job，**上一步产出喂下一步**。✅ 已落地**线性 chain v1** + `${steps.N.xxx}` 引用 + fail-fast。留后续：DAG/并行（E9）、子工作流/跨项目（E27）、per-step on_failure/retry（E24）、md-per-step、workflow 级事件流/retention。 |
| E8 | **审批门 (approval gate)** | 中 | 中 | 运行中交互扩一种"高危动作审批"（agent 跑 `rm -rf`/推送/外发前先求批）。复用 `pending_interaction`；兼顾完成任务与审计/安全(③)。**自主化的安全闸**——E24/E25/E26 自主能力都依赖它兜底。 |
| E9 | **并行 fan-out / 对比** | 中 | 中 | 一个任务派给 N 个 agent/worker 并行，汇总对比（judge-panel）。利用 worker 舰队；接 E7/E27 工作流。 |
| E10 | **mcp-agent 类型** | 中 | 中 | 新 agent type：job 调用"本身是 MCP server"的外部能力（与 runner 正交、可与 worker 组合）。让 gofer 编排 MCP 工具。 |
| E11 | **上下文 / secret / 规则注入** | 中 | 中 | per-job env、范围化 secret 注入、附加上下文文件挂载，**含 agent 规则文件注入**（按 agent 放对 `AGENTS.md`/`CLAUDE.md`，注意污染项目目录 vs prompt 拼接、worker 远端随 job 落地）。secret 不入日志/库（SR403）。 |
| E24 | **自动重试（按策略）** | 中 | 中 | job 失败按 max_attempts + 退避（接 SR606）+ **退出码/条件白名单**自动重试。⚠️ **幂等坑**：纯 exec 可安全重试，改文件的 agent 重试会叠加副作用——默认 opt-in。应与工作流 step 级 `on_failure`**统一一套重试语义**（别各做一套）。属**自主化 epic**。 |
| E25 | **监督 agent 自动应答** | 高 | 大 | `pending_interaction` 由另一"监督 agent"自动作答（用一个 job 答另一个 job 的提问）。⚠️ 失控/套娃/烧 token：定位**半自动**——自动答低危澄清，遇审批门(E8)/高危/超轮次**升级人**（经 E22 IM 或 Web）。gofer 最有特色的"agent 编排 agent"。属**自主化 epic**。 |
| E26 | **hooks 插件（js/py 输出 json）** | 中 | 大 | 生命周期点跑用户脚本影响流程。⚠️ 元能力（E11/E24/E25 都能用 hook 实现）+ RCE 面。分两类：**事件 hook（只读旁路）先做**（订阅事件→跑脚本→不回写，安全）；**决策 hook（回写流程）后做**（pre-submit 否决/改写、interaction 自动答）。信任模型：operator 配的脚本视为可信（如 git hooks）。属**自主化 epic**。 |
| E27 | **子工作流 / 跨项目编排** | 高 | 大 | 工作流作为另一工作流的一个 step（嵌套+终态聚合）+ 跨项目衔接（开发→自动化测试）。**先确认** v1 `StepSpec` 是否已带 per-step `project_key`；难点在跨项目产物流转（A 项目 `result_dir`→B 读，共享盘/拷贝）。属**工作流 v2**（接 E7 尾巴 + E9）。 |

## ③ 观察 / 审计 agent 的工作

| 编号 | 增强 | 价值 | 工作量 | 说明 |
|---|---|---|---|---|
| E12 ✅ | **"改了什么" 审计（diff 快照）** | 高 | 中 | job 前后对项目目录做 git diff / 文件变更快照，详情页展示"这个 agent 改了哪些文件"。对代码类 agent 审计杀手级。 |
| E13 ✅ | **job 事件时间线 (audit trail)** | 高 | 中 | 生命周期事件 append-only 审计流，`GET /v1/jobs/{id}/events` + 详情时间线。 |
| E14 🚧 | **通知 / 事件外发** | 高 | 中 | job `done/failed/pending_interaction` 时外发。✅ **webhook** 已落地（白名单/SSRF/HMAC/投递可见性）；**MQ 外发**设计显式**不做**（gofer 无 MQ 条件，留上层）。IM 外发见 E22。 |
| E15 ✅ | **渲染命令可见** | 低 | 低 | 详情页显式展示"实际执行的命令"（`GET /v1/jobs/{id}/request` 回原始 JobRequest）。 |
| E16 ✅ | **指标 `/metrics`** | 中 | 低 | Prometheus：http/job 计数+时长、in_flight/queued/workers gauge。免认证 + 可选 token；route 模板防高基数。 |
| E17 ✅ | **per-caller 配额 / 限流** | 中 | 中 | per-caller 并发配额（信号量排队）+ 速率限流（令牌桶 429）；配置真源在 job.Service（SIGHUP 热加载）。 |
| E20 | **项目 git / 只读信息查看** | 中 | 中 | 项目**此刻** git status/log/branch 只读视图。**与 E12 划清**：E12=某 job 改了什么（快照）；本项=项目当前状态（不绑 job）。问题：git 在哪跑（本地容器/worker 侧）、只读白名单防写/防 RCE。 |
| E22 | **IM 连接（钉钉/飞书）** | 高 | 大 | 跨①②③轴。**拆三层**：(a) 出站通知接 IM（复用 E14，**最便宜先行**）；(b) 入站提交（IM 消息→job，新入口，回调验签 + IM 用户映射 caller_id）；(c) 交互应答（pending_interaction→IM 卡片→回复→续跑）。平台差异需 adapter。鉴权接 E17 caller。 |

---

## 横切主题：自主化 epic（E23/E24/E25/E26 收敛）

E23 定时 + E24 自动重试 + E25 监督应答 + E26 hooks 共同把 gofer 从"人工逐个提交"推向"**自主运转的 agent 编排平台**"。它们**共享同一套地基**，应合成一个 epic 统一设计，而非各做一套：

- **方向性张力（先拍板）**：自主化与 gofer 现有"人在环路"安全取向**直接冲突**——要先定 gofer 要多"自主"。自主能力**必须**配 E8 审批门兜底，否则失控。
- **统一抽象（别重复造）**：① `pending_interaction` 应答的三种来源——人工 Web / IM 人工(E22c) / 监督 agent(E25)——是同一机制的**可插拔 answerer**；② 失败处理的四个面——手动 rerun(E2) / 自动重试(E24) / hook 决策(E26) / 工作流 on_failure——应**统一一套重试/失败策略**。
- **三个隐含前置（闭环必需）**：强审计（E13 事件标注"AI 自动 vs 人"）· **配额约束**（自主烧 token，受 E17 配额管）· 可接管（人能暂停/接管自主链）。

---

## 建议优先级（下一步做什么）

**已完成（✅）**：E1/E2/E3/E5/E6/E12/E13/E15/E16/E17，E7🚧（线性 v1）/E14🚧（webhook）。原三轴核心缺口基本补齐。

**便宜先行（低成本、边界清晰）：**
1. **E18 工作流导入导出**（`spec_json` 近现成）· **E19(a) 产物预览**（E1 加渲染）· **E22(a) IM 出站通知**（复用 E14）· **E21 编辑器打开**（复用主机 bridge）。

**第二梯队（中等、承接已有）：**
2. **E4 模板库**（接 E18）· **E20 项目 git 信息**（接 E19）· **E11 上下文/规则注入**（含规则文件）。
3. **工作流 v2 epic**：**E27 子工作流/跨项目** + **E9 fan-out** + **E24 重试**（与 on_failure 统一）+ E7 尾巴——需独立 design。

**大件（需独立设计，先对齐取向）：**
4. **自主化 epic**：**E8 审批门**（先行，做安全闸）→ **E23 定时** → **E25 监督应答** + **E26 hooks** + **E22(b/c) IM 双向**——先出设计文档对齐"自主程度"，再排。
5. **E10 mcp-agent**（按需）。

> **一句话主线**：原"看得见 agent 产出/改了什么"已补齐（E1/E6/E12/E13/E15/E16/E17）；下一程向**自主化（定时/重试/监督/hooks）与连接（IM/编辑器）**扩展，且**每一步自主都先有审批门(E8)兜底、受配额(E17)约束、留审计(E13)痕迹**——让 gofer 既能自己跑，又始终可控可审计。
