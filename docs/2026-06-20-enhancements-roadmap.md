# Gofer 增强路线（Enhancements Roadmap）

> 目标三轴：**① 更方便使用** · **② 更好地利用各种 agent 完成任务** · **③ 始终可观察、可审计 agent 的工作**。
> 本文是思考与优先级清单（非实施计划）；选定项再各自出 design/plan。多 hub HA 不在此（独立大 Epic，见 [`TODO.md`](TODO.md) §大型 Epic）。
>
> **状态图例**：✅ 已落地 · 🚧 部分落地（最小版/留后续） · （空）未做。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v1.0 | 2026-06-20 | inhere | 初版：E1-E17 三轴增强想法 + 建议优先级（思考清单，非实施计划） |
| v1.1 | 2026-06-21 | inhere | 标记 E1-E17 完成态（E16/E17 本轮落地，E7🚧/E14🚧）；新增 E18-E27（来自 `tmp/tmp.md` 想法整理）；新增「自主化 epic」横切章节；更新现状基线与优先级 |
| v1.2 | 2026-06-22 | inhere | 回填滞后标记：**工作流 v2 已全落地**（design `workflow-v2-design.md` + plan `2026-06-22-workflow-v2/`，commit `7c470b8`/`4492871`/`dc71b06`/`92cc669`），随之 **E7✅(v1+v2) · E9✅ · E18✅ · E27✅ · E24🚧**；更新现状基线（核心缺口去掉"工作流深化"）与建议优先级 |
| v1.3 | 2026-06-22 | inhere | 新增 **E28 多 agent 经 gofer 协作通信**（中央 serve 中枢 + mcp HTTP-client 接入；信箱语义并入 E25 可插拔 answerer），来自"两个工作目录 claude 经 gofer 互通"讨论；补 E28 实现取向（stdio mcp **standalone/client 双模式** + 适用场景表，明确"该废的是 in-process 后端、非 stdio 本身"） |
| v1.4 | 2026-06-23 | inhere | 新增 **E29 配置/部署模型简化**（全局单 server + 项目瘦配置 `.gofer.project.yaml`），已出 design + plan（Phase1 零代码 / Phase2 overlay 合并 + cwd 推断） |
| v1.5 | 2026-06-23 | inhere | 新增 **E30 浏览器 pty 交互** / **E31 节点拓扑+配置管理** / **E32 项目空间浏览（子 git+关键文件）**；E23 补"触发位置"维度（server 集中 vs 下发 worker）；新增横切「**Web 控制台 v2**」（只读层/写交互层切分）。来源：用户新想法整理 |
| v1.6 | 2026-06-23 | inhere | E29 标注**路径视角待补丁**（design D10：`path_view` 开关 + `ExecPath`，修正 container_path 未进执行链路 + overlay 路径不一致）；E32 子 git 扫描改走 `ExecPath` |
| v1.7 | 2026-06-23 | inhere | **D10 路径视角已落地**（commit `f3a1db8`）：`path_view`+`ExecPath` 统一执行路径、修正 overlay 不一致、默认 host 零变化回归绿 |
| v1.8 | 2026-06-23 | inhere | **Web 控制台 v2 只读层 design 已出**（E31拓扑/E19a预览/E20 git/E32 子git+关键文件，[`design/2026-06-23-web-console-v2-readonly-design.md`](design/2026-06-23-web-console-v2-readonly-design.md)）；待出 plan |
| v1.9 | 2026-06-26 | claude | 新增 **E33 agent session 捕获与恢复**（捕获底层 CLI `session_id` 入库 → 检查/检索/恢复续接；exec 透传 `claude --resume` 跨 job 已实测可行）。design [`design/2026-06-26-session-capture-design.md`](design/2026-06-26-session-capture-design.md)（bd `hyy-ai-inspect-nnk`）；分期 P1/P2/P3 |
| v1.10 | 2026-06-26 | claude | **E33 已落地**（SUPMODE P1-P3 + 真机 E2E PASS）：注入优先(claude `--session-id`)/捕获(codex)、`job resume`、worker `Outcome.SessionID` 回传、`list --session`。plan 进度全勾 |
| v1.12 | 2026-06-27 | inhere | **E28 决策**：client 模式确认要做，`gofer mcp --server <addr>` 作 MVP 先落地（现有 standalone 单进程鸡肋、与 serve/Web 状态割裂）；复用 `bindServerFlags`(零新配置) + `internal/client`(10 工具 7 个直接映射，补 ~3 GET)；命名统一 `--server`。见 E28 决策段，提优先级到"便宜先行" |
| v1.11 | 2026-06-27 | claude | **E33 收尾**（commit `2383735`）：resume **豁免 allow_exec**（按 SOURCE agent 判门，载体 exec 不再要求 allow_exec；防伪不入 request_json）+ `session_id` 详情**全面展示**（除 CLI 外补 MCP `jobView` / Web `JobDetail`）。新增 **E34 job 提交来源追踪**（channel/client provenance）+ `job list` 改用 `gookit/cliui show/table`(CJK 对齐)，已落地+三端部署+真机验收（commit `ff95515`）。配套 gofer-job skill/runbook 补 `--cwd` 相对路径 / `--title` 约定（主仓 `b74c7e5`） |

## 现状基线（已有，不重复做）

单 job = 单 agent / 单 prompt-or-argv / 单项目 / 单 cwd；状态/元数据入 SQLite。已具备：异步+同步提交、md+yaml、ws 远端 worker + 标签调度、运行中交互、`/v1/runners` 健康名册、`caller_id`/`worker_id` 审计、retention、Web 控制台 + MCP。

**原核心缺口已基本补齐**：产物回取（E1✅）、改了什么 diff（E12✅）、事件时间线（E13✅）、结构化结果（E6✅）、多步工作流（E7 ✅v1+v2，含 per-step 重试/并行 fan-out/子工作流跨项目）、CLI 日常（E2✅）、指标与配额（E16/E17✅）均已落地。

**新核心缺口（本轮想法聚焦）**：① 任务只能**人工逐个提交**，不能定时/失败自动重试/agent 自动应答——缺**自主化**；② gofer 未融入日常工作环境（IM 提交&通知、IDE 跳转）——缺**连接/入口**；③ 无插件扩展点（hooks）。

> 原"③ 工作流只能单项目线性，缺工作流深化"已由 **工作流 v2 落地解决**（子工作流嵌套 / 跨项目衔接 / 并行 fan-out），不再列为缺口。

---

## ① 更方便使用

| 编号 | 增强 | 价值 | 工作量 | 说明 |
|---|---|---|---|---|
| E1 ✅ | **产物回取 (artifacts)** | 高 | 中 | job 除日志外的产出文件（构建物/生成代码/报告）：`GET /v1/jobs/{id}/artifacts` 列举 + 下载；agent/wrapper 可声明产物（约定 `<result_dir>/artifacts/` 或清单）。Web 详情可下载。**这是让"会产文件的 agent"真正可用的前提**。 |
| E2 ✅ | **CLI 日常补全** | 高 | 低 | 补 `job list`、`job watch <id>`（终端实时 tail SSE）、`job rerun <id>`（复用 request_json 一键重跑）、shell 补全。日常摩擦最大、最便宜。 |
| E3 ✅ | **引导 / 配置校验** | 中 | 低 | `gofer init`（生成示例配置 + 引导登记首个项目/agent）、`gofer config validate`；example 配置补 `callers/workers/peer-http/worker` 段。降低上手门槛。 |
| E4 | **任务模板库** | 中 | 中 | 命名的 md+yaml 模板 + 变量，`job run -t <template> --var k=v`。把重复 prompt 沉淀复用，天然接 md+yaml 提交。与 E18（工作流导出）同属"复用"主题。 |
| E5 ✅ | **job 标签 + 搜索过滤** | 中 | 中 | 提交带 `tags`；看板/列表按 tag/agent/runner/时间检索。job 一多就需要。 |
| E18 ✅ | **工作流导入导出 json** | 中 | 低 | 已随 v2 P4 落地（commit `92cc669`）：`GET /v1/workflows/{id}/export` + CLI `wf export`（默认 YAML，= `wf run` 输入格式，`-f yaml/json`）+ `wf run` 按内容自动识别导入；secret 启发式剥离 + 递归子 wf；`${steps.N}` 引用原样保留。 |
| E19 | **文件 / 产物预览** | 中 | 中 | Web 在线预览（md/图/json/代码高亮）。**二义先定**：(a) job 产物 artifacts 渲染——小、安全边界清晰（限 `result_dir`），E1 已有下载加渲染即可，**先做**；(b) 项目工作目录文件浏览——大，需文件树 API + 路径安全（防 `../`、黑名单 `.env`/`.git`）。 |
| E21 | **主机侧动作（编辑器打开等）** | 中 | 低 | 一键用主机编辑器打开项目（`code <host_path>`）/ reveal / 开终端。⚠️ gofer 在容器、编辑器在主机：**必须复用现有 codex-bridge 主机通道**（`host.docker.internal`），抽象成一类"主机侧动作"。仅对有主机 bridge 的部署可用。 |
| E23 | **定时任务（内置 cron）** | 中 | 中 | `schedules` 表 + 复用现有 sweeper loop 范式定时提交 job/工作流。问题：错过补偿、多 hub 重复触发（单 hub 不问题）。**触发位置（设计维度）**：① server 集中触发（默认、简单，复用 sweeper，worker 只执行被派的 job）vs ② 下发 worker 自触发（worker 内置调度器 + 本地 schedule，断网离线自治，但管理分散）——倾向默认①、②作可选高级。接 E24（定时+重试）、E18（定时跑导入的工作流）。属**自主化 epic**。 |
| E29 ✅ | **配置/部署模型简化（全局单 server + 项目瘦配置）** | 高 | 中 | **已落地**（SUPMODE 2026-06-23，P0-P3 commit `65cdb70`/`bfc6211`/`ae45372`/`9b2e6dd`，独立验收 D1–D9 全 PASS）。一台机一个 `serve` + 项目映射**全局单文件**；项目目录 `.gofer.project.yaml` **瘦配置**（仅偏好、无 server、准入留全局）。design [`design/2026-06-22-config-simplification-design.md`](design/2026-06-22-config-simplification-design.md) + plan [`plans/2026-06-23-config-simplification-plan.md`](plans/2026-06-23-config-simplification-plan.md)。Phase1（`init --global` + `GOFER_CONFIG` + `project add` 写全局）+ Phase2 overlay 合并（`buildCore`/`Reload`）+ cwd 推断免 `-p`；准入真源在 serve、CLI 不读 overlay（D2 安全）。**✅ 路径视角 `path_view`**（design D10，commit `f3a1db8`）：`server.path_view: host\|container`(默认 host) + `Config.ExecPath` 统一所有 gofer 侧路径（含 overlay/E32 扫描），修正 overlay 路径不一致；默认 host 零变化、job/httpapi 回归绿。 |

## ② 更好地利用 agent 完成任务

| 编号 | 增强 | 价值 | 工作量 | 说明 |
|---|---|---|---|---|
| E6 ✅ | **结构化结果 (structured result)** | 高 | 中 | agent 除 exit_code/日志外回一份结构化结果（约定 `result.json`），入库 + API/Web 展示。返回可解析摘要而非裸 stdout——也直接增强审计(③)。 |
| E7 ✅ | **多步 / 工作流 (job 链)** | 高 | 大 | **v1+v2 全落地** ✅。v1：线性 chain + `${steps.N.xxx}` 引用 + fail-fast + 幂等推进 + sweeper 兜底 + CLI/HTTP/Web（[`design/2026-06-21-workflow-chains-design.md`](design/2026-06-21-workflow-chains-design.md)）。v2（[`design/2026-06-22-workflow-v2-design.md`](design/2026-06-22-workflow-v2-design.md) + plan [`plans/2026-06-22-workflow-v2/`](plans/2026-06-22-workflow-v2/)，commit `7c470b8`/`4492871`/`dc71b06`/`92cc669`）：per-step on_failure/retry（E24）、并行 fan-out+join（E9）、子工作流嵌套+跨项目（E27）、workflow 事件流/retention、导入导出+md-per-step（E18）。**不做**通用菱形 DAG（留 v3）。遗留：工作流模板库(E4)、export secret 启发式剥离、子 wf retry 重跑整条。 |
| E8 | **审批门 (approval gate)** | 中 | 中 | 运行中交互扩一种"高危动作审批"（agent 跑 `rm -rf`/推送/外发前先求批）。复用 `pending_interaction`；兼顾完成任务与审计/安全(③)。**自主化的安全闸**——E24/E25/E26 自主能力都依赖它兜底。 |
| E9 ✅ | **并行 fan-out / 对比** | 中 | 中 | 已随 v2 P2 落地（commit `4492871`）：单 step `fan_out` 起 N 并行 job + `join`(all/any/quorum，永不悬挂) + 引用聚合（`${steps.N.result_dir}` 多目录 / `${steps.N.fK}`）。judge-panel 类对比可直接编排。 |
| E10 | **mcp-agent 类型** | 中 | 中 | 新 agent type：job 调用"本身是 MCP server"的外部能力（与 runner 正交、可与 worker 组合）。让 gofer 编排 MCP 工具。 |
| E11 | **上下文 / secret / 规则注入** | 中 | 中 | per-job env、范围化 secret 注入、附加上下文文件挂载，**含 agent 规则文件注入**（按 agent 放对 `AGENTS.md`/`CLAUDE.md`，注意污染项目目录 vs prompt 拼接、worker 远端随 job 落地）。secret 不入日志/库（SR403）。 |
| E24 🚧 | **自动重试（按策略）** | 中 | 中 | **工作流 step 级 on_failure/retry 已随 v2 P1 落地**（commit `7c470b8`：fail/continue/retry + (step,attempt) 二元组抢权 + next_step_at 退避 + sweeper backstop）；独立 job 级自动重试为**最小版**（finish 失败重投，进程内 timer）。**待做**：持久化退避的可靠版 + 退出码/条件白名单 + opt-in 幂等保护。⚠️ **幂等坑**：纯 exec 可安全重试，改文件的 agent 重试会叠加副作用——默认 opt-in。属**自主化 epic**。 |
| E25 | **监督 agent 自动应答** | 高 | 大 | `pending_interaction` 由另一"监督 agent"自动作答（用一个 job 答另一个 job 的提问）。⚠️ 失控/套娃/烧 token：定位**半自动**——自动答低危澄清，遇审批门(E8)/高危/超轮次**升级人**（经 E22 IM 或 Web）。gofer 最有特色的"agent 编排 agent"。属**自主化 epic**。 |
| E26 | **hooks 插件（js/py 输出 json）** | 中 | 大 | 生命周期点跑用户脚本影响流程。⚠️ 元能力（E11/E24/E25 都能用 hook 实现）+ RCE 面。分两类：**事件 hook（只读旁路）先做**（订阅事件→跑脚本→不回写，安全）；**决策 hook（回写流程）后做**（pre-submit 否决/改写、interaction 自动答）。信任模型：operator 配的脚本视为可信（如 git hooks）。属**自主化 epic**。 |
| E27 ✅ | **子工作流 / 跨项目编排** | 高 | 大 | 已随 v2 P3 落地（commit `dc71b06`）：`type=workflow`/`sub_workflow` 嵌套（深度≤3、fan×wf 互斥）+ `parent_*` 列 + 子 wf 终态 triggerParentAdvance。跨项目产物：本地 `result_dir` 直读已支持；**远端跨机依赖共享文件系统**（自动拉取通道留后续，README 已警示）。 |
| E28 | **多 agent 经 gofer 协作通信（mcp HTTP-client 接入）** | 高 | 中 | 让多个工作目录的 claude/agent 进程把 gofer 当**中枢**互通：A 派活给 B、信箱式 `pending_interaction` 互答、共享 `result`/`artifacts`。**分层结合**：**地基=中央 `serve`**（job 执行/状态/日志集中，前提非选项，已有）；**接入=给 `gofer mcp` 加 HTTP-client 模式**（8 个 `bridge_*` 工具从进程内直操 DB 改为转发到中央 serve，**复用 `internal/client` peer-http 客户端，小改造**）——一举消除 stdio 1:1 + "job 在哪进程执行/日志在哪/跨进程 SQLite 写锁"三坑。**信箱语义与 E25 可插拔 answerer 统一**（人工 Web / IM(E22c) / 监督 agent(E25) / 对等 agent 同一机制）；可选再加 message 原语（`bridge_post/poll_message`）。⚠️ MCP 是 client→server 单向工具调用、非对等总线，故只能"经中枢间接互通"，非两 claude 直连。分阶段：先零改造跑 serve+HTTP 验证协作语义 → 再补 mcp HTTP-client 体验。跨①②轴。 |
| E30 | **浏览器 pty 交互（attach 交互式 agent）** | 高 | 大 | Web 里经 pty(伪终端)直连 agent 的**交互式终端**（xterm.js + ws 双向流 input/output/resize），区别于结构化 `pending_interaction`（单问单答）——适合本身是 REPL/交互式 CLI 的 agent（claude 交互模式 / shell / 调试器）。**改执行模型**：agent 跑在 ptmx、stdin 接入，需 `interactive:true` job 选项或新 runner 模式；后端 `creack/pty`。远端 worker pty 经 hub ws 隧道转发（难点，local 先行 / 远端二期）。⚠️ pty ≈ 全 shell 能力，严格鉴权 + 会话审计（考虑录制）；定位"attach 交互式 agent"，**不做通用 web shell**（防后门）。属 Web 控制台 v2 写/交互层。跨①②轴。 |
| E33 ✅ | **agent session 捕获与恢复** | 高 | 中 | **已落地**（SUPMODE 2026-06-26，P1-P3 + 真机 E2E PASS）。获取**注入优先**：claude `--session-id <gofer-uuid>`（零解析、不需 json）/ codex `session id:` 正则捕获，best-effort 挂 `captureOutcomes`；additive 加 `jobs.session_id` 列；`gofer job resume <job-id>`（走 exec 路径、同 runner 约束）；worker 经 `Outcome.SessionID` 自报回传；`job list --session`。design [`design/2026-06-26-session-capture-design.md`](design/2026-06-26-session-capture-design.md) + plan [`plans/2026-06-26-session-capture-plan.md`](plans/2026-06-26-session-capture-plan.md)。跨②③轴。 |

> **E28 实现取向：stdio mcp 双模式（别误砍 stdio）**
> `gofer mcp` 的 **stdio transport 要保留**——它是 claude/MCP 生态最自然的接入方式（`command` 拉子进程、零网络/端口/token 配置）；该废的是它现在的 **in-process 后端**，不是 stdio 本身。"单进程独享" 是 stdio 1:1 的 transport 本质（非缺陷）；"单项目" 的说法不成立——一个 mcp 进程加载整份 config，可向**任意已登记项目**派 job，只是 local job 都在**本进程执行**。改造后两模式并存，一个 flag 切（如 `gofer mcp --serve http://...` 走 client，否则 standalone）：
>
> | 模式 | 形态 | 适用 | 不适用 |
> |---|---|---|---|
> | **standalone（现状）** | in-process 直操 DB + 本进程执行 local job | 单人单机、不跑 serve、一个 claude 即用即走编排 job（派后台长任务 / 轮询 / 读产物 / 应答交互）；**零常驻服务** | 多 client；Web 控制台与 mcp 并存（两进程状态割裂、互不见 live job）；多 claude 协作 |
> | **client（新增，E28 核心）** | stdio mcp 仅当瘦客户端，`bridge_*` 转发到中央 serve（复用 `internal/client`） | 多 claude 各自 1:1 拉起自己的 stdio mcp 子进程、后端共指同一 serve → 中枢化 / 协作 / Web+MCP 状态一致 | 无中央 serve 的纯单机轻量场景（杀鸡用牛刀，回退 standalone 即可） |
>
> **不选 HTTP MCP transport 替代 stdio**：那要 claude 端配 URL+鉴权、gofer 实现 HTTP MCP server，更重；stdio 子进程转发对 claude 端**零改动**（仍是 `command` 拉起），更贴合 claude 用法。
>
> **【决策 2026-06-27】client 模式确认要做，`gofer mcp --server <addr>` 作为最快见效的 MVP 先落地。** 动机：现有 standalone 单进程**鸡肋**——in-process 状态与 `serve`/Web 控制台**割裂**（多个 claude 子进程、Web 各看各的 live job，互不可见），无法支撑多 agent 协作。MVP 切片很小、复用度高：
> - **旗标复用 `bindServerFlags`**（`--server/-s` + `--token`，默认读 `GOFER_SERVER_ADDR/TOKEN` env，**与 CLI 完全一致 → 零新配置概念**）；客户端注册仍是 `command: gofer, args: ["mcp","--server","..."]`，对 claude 端零改动。**命名统一用 `--server`**（与 job 子命令一致；修正本文上文示例里的 `--serve`）。
> - **后端换转发，复用 `internal/client`**：10 个 `bridge_*` 工具里约 7 个直接映射现有 client 方法（run→`SubmitJobSync`、get→`GetJob`、tail→`GetLogs`、cancel→`CancelJob`、answer→`AnswerInteraction`、artifacts→`ListArtifacts`、result→`GetJob`）；需补 ~3 个 client GET（`ListProjects`/`ListAgents`/`GetInteractions`）+ tail 流式（可先用一次性 `GetLogs`，SSE 二期）。
> - **standalone 保留**（无 serve 的纯单机轻量场景，一个 flag 切：有 `--server` 走 client、否则 standalone）。
> 效果：多 claude 各拉自己的 stdio mcp 子进程、后端共指同一 serve → 状态一致、Web+MCP 同视图、为 E25 监督应答 / 多 agent 协作铺好地基。

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
| E31 | **节点拓扑 + 配置管理（Web）** | 中 | 中-大 | ① **拓扑图**：server(hub)+workers+peers 关系图，数据源现有 `/v1/runners`(C6)+peers，纯可视化**便宜**；② **点击节点→节点面板**：项目空间 / 配置 / 在飞 job / 心跳；③ **配置查看/编辑**：Web 看/改 config → 写回（复用 `writer.go` 保留未知字段）+ SIGHUP reload。⚠️ **拆只读/写**：只读查看便宜先做；**编辑高危**（写 config + reload）需鉴权分级（当前 token 平权）+ **secret 不回显**（只显 `token_env` 名，SR403/805）+ 编辑后先 `config validate`。属 Web 控制台 v2（拓扑/面板=只读层，配置编辑=写层）。跨①③轴。 |
| E32 | **项目空间浏览（子 git 发现 + 关键文件）** | 中 | 中 | E19(b)/E20 的**安全聚焦版**：① **子 git 发现**：项目根下递归找 `.git` 仓库列出 + branch/status（扫描走 `ExecPath`，受 `path_view` 控制，见 E29 design D10）；② **关键文件查看**：README / .gitignore / AGENTS.md / CLAUDE.md。**取舍**：做**白名单关键文件**而非通用文件树（后者 E19b 有 `.env` 泄露 + 路径穿越风险）。属 Web 控制台 v2 只读层。跨①③轴。 |
| E34 ✅ | **job 提交来源追踪 (provenance)** | 中 | 低 | **已落地+三端部署**（2026-06-27，commit `ff95515`，真机验收 PASS）。DB 记录原先看不出"谁/哪台/经哪渠道"提交（`caller_id` 来自共享 token 多为 `default`，且 `job show` 都没显示）。新增 `channel`(cli/web/mcp/im，**客户端声明**：CLI=cli 可 `--channel` 覆盖 / Web=web / MCP=mcp) + `client`(**来源主机/地址**：CLI 填 `os.Hostname()` / HTTP 提交 client 空时 **server 盖章 remote IP**，`X-Forwarded-For` 优先)，与既有 `caller_id` 共同回答提交来源。全链路：model + jobstore(additive 迁移 `channel/client` 列) + persistence + CLI(`job run` 盖章 / `show` 补 channel/client/caller_id / `list` 加列) + HTTP(盖章 IP) + MCP(channel=mcp) + Web(NewJob channel=web / JobDetail 显示)；`resume` 沿用源 job 来源。配套 **`job list` 改用 `gookit/cliui show/table`**（CJK-aware 列对齐，gcli v3.8.0 已 require cliui v0.3.1）+ gofer-job skill/runbook 补 `--cwd`(相对项目根，勿命令里 cd 绝对路径)/`--title` 约定。跨①③轴。 |

---

## 横切主题：自主化 epic（E23/E24/E25/E26 收敛）

E23 定时 + E24 自动重试 + E25 监督应答 + E26 hooks 共同把 gofer 从"人工逐个提交"推向"**自主运转的 agent 编排平台**"。它们**共享同一套地基**，应合成一个 epic 统一设计，而非各做一套：

- **方向性张力（先拍板）**：自主化与 gofer 现有"人在环路"安全取向**直接冲突**——要先定 gofer 要多"自主"。自主能力**必须**配 E8 审批门兜底，否则失控。
- **统一抽象（别重复造）**：① `pending_interaction` 应答的四种来源——人工 Web / IM 人工(E22c) / 监督 agent(E25) / 对等 agent(E28)——是同一机制的**可插拔 answerer**；② 失败处理的四个面——手动 rerun(E2) / 自动重试(E24) / hook 决策(E26) / 工作流 on_failure——应**统一一套重试/失败策略**。
- **三个隐含前置（闭环必需）**：强审计（E13 事件标注"AI 自动 vs 人"）· **配额约束**（自主烧 token，受 E17 配额管）· 可接管（人能暂停/接管自主链）。

---

## 横切主题：Web 控制台 v2（E19/E20/E21/E30/E31/E32 收敛）

把 Web 控制台从"看板 + 详情 + Workers 名册"推向"**集群可观察 + 项目透视 + 可交互操作**"。这批增强按**只读 vs 写/交互**切两层（安全闭环，SR1402）：

- **只读观察层（便宜先行，数据源多现成、无写风险）**：E31 拓扑图 + 节点面板(只读) · E32 子 git 发现 + 关键文件 · E19(a) 产物预览 · E20 项目 git 状态。**✅ 只读层 design 已出**：[`design/2026-06-23-web-console-v2-readonly-design.md`](design/2026-06-23-web-console-v2-readonly-design.md)（marked+DOMPurify 预览 / SVG 物理拓扑 / 白名单关键文件 / +3 只读 endpoint，复用 runGit+ExecPath+SafeJoin）。
- **写 / 交互层（重、高危，各需独立安全设计）**：E30 pty 交互（改执行模型 + 会话审计）· E31 配置编辑（写回 + reload + 鉴权分级 + secret 不回显）· E21 主机侧动作（复用主机 bridge）。
- **统一前置**：鉴权分级（当前 token 平权，写/交互操作需更细粒度）· 审计（写操作 / pty 会话入 E13 事件流）· secret 不回显（SR403/805）。

---

## 建议优先级（下一步做什么）

**已完成（✅）**：E1/E2/E3/E5/E6/E12/E13/E15/E16/E17，**E7 ✅（v1+v2 全落地）** + 随 v2 的 **E9✅ / E18✅ / E27✅**，**E29✅**（配置简化）· **E33✅**（session 捕获/恢复，含 resume 豁免 allow_exec + 详情全展示收尾）· **E34✅**（提交来源追踪 channel/client + job list cliui table），E14🚧（webhook，MQ 不做）/E24🚧（工作流 step 级重试已落地，独立 job 级重试最小版）。原三轴核心缺口基本补齐。

**便宜先行（低成本、边界清晰）：**
1. **Web 控制台 v2 只读层**（E31 拓扑+节点面板 · E32 子 git+关键文件 · E19a 产物预览 · E20 git 状态——**打包一份 design**，见上「Web 控制台 v2」横切）· **E22(a) IM 出站通知**（复用 E14）。
2. **E28 `gofer mcp --server` client 模式 MVP**（**2026-06-27 决策提前**）：standalone 单进程鸡肋、与 serve/Web 状态割裂；MVP 复用 `bindServerFlags`(零新配置) + `internal/client`，把 10 个 `bridge_*` 后端换转发（7 个直接映射、补 ~3 GET），standalone 保留。最快消除割裂、为多 agent 协作铺地基。详见 E28 决策段。

**第二梯队（中等、承接已有）：**
3. **E4 模板库**（接 E18）· **E20 项目 git 信息**（接 E19）· **E11 上下文/规则注入**（含规则文件）。（E28 mcp client 模式已提到「便宜先行」item 2。）
4. ~~**工作流 v2 epic**：E27 子工作流/跨项目 + E9 fan-out + E24 重试 + E7 尾巴~~ —— **✅ 已落地**（design [`design/2026-06-22-workflow-v2-design.md`](design/2026-06-22-workflow-v2-design.md) + plan [`plans/2026-06-22-workflow-v2/`](plans/2026-06-22-workflow-v2/)，commit `7c470b8`..`92cc669`）。**剩余尾巴**：工作流模板库(E4)、export secret 启发式剥离非保证、子 wf retry 重跑整条、独立 job 级重试可靠版(E24)。

**大件（需独立设计，先对齐取向）：**
5. **自主化 epic**：**E8 审批门**（先行，做安全闸）→ **E23 定时** → **E25 监督应答** + **E26 hooks** + **E22(b/c) IM 双向**——先出设计文档对齐"自主程度"，再排。
6. **E10 mcp-agent**（按需）。

> **一句话主线**：原"看得见 agent 产出/改了什么"已补齐（E1/E6/E12/E13/E15/E16/E17）；下一程向**自主化（定时/重试/监督/hooks）与连接（IM/编辑器）**扩展，且**每一步自主都先有审批门(E8)兜底、受配额(E17)约束、留审计(E13)痕迹**——让 gofer 既能自己跑，又始终可控可审计。
