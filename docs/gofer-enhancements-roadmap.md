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
| v1.9 | 2026-06-26 | claude | 新增 **E33 agent session 捕获与恢复**（捕获底层 CLI `session_id` 入库 → 检查/检索/恢复续接；exec 透传 `claude --resume` 跨 job 已实测可行）。design [`design/2026-06-26-session-capture-design.md`](design/2026-06-26-session-capture-design.md)（bd `example-project-nnk`）；分期 P1/P2/P3 |
| v1.10 | 2026-06-26 | claude | **E33 已落地**（SUPMODE P1-P3 + 真机 E2E PASS）：注入优先(claude `--session-id`)/捕获(codex)、`job resume`、worker `Outcome.SessionID` 回传、`list --session`。plan 进度全勾 |
| v1.22 | 2026-06-30 | claude | **新增 E39-E44（Web 控制台 v3 + AI 助手 + skills），来自用户使用反馈**：E39 导航/IA 重构（补 agent **roles** 等缺失信息）· E40 Dashboard 首页（聚合 stats endpoint）· E41 列表分页+时间列+详情(running 显 rendered cmd / STDOUT-ERR tab / ANSI) · E42 全局交互通知+Web 应答（写层，**前置阻断=web 鉴权**，实现 E25 升级人面）· E43 内置 AI 助手（对接 LLM，答疑/教用/帮配置）· E44 gofer 使用 skills。**并入项**：#6 mcp client web/log 可见性→E28/E36；#7 sup/role 标记→E36/E35；#5 running 显 rendered cmd→E15 gap。**建议**：E39-E42 收敛一份「Web 控制台 v3」design 统一 IA（E30 pty/E31③ config 写归此写层）；E41+#6+#7 可快赢先落；E44 先于 E43；E42 写层需先定鉴权分级。**仅 roadmap 记录，未设计未实施。** |
| v1.21 | 2026-06-28 | claude | **多 agent 协作合并 design + plan 已出**（design [`design/2026-06-28-multi-agent-collab-design.md`](design/2026-06-28-multi-agent-collab-design.md) v0.2 含对抗复审 7 处收紧；plan [`plans/2026-06-28-multi-agent-collab-plan.md`](plans/2026-06-28-multi-agent-collab-plan.md) 总纲 + 子 plan `multi-agent-collab/{P1-presence-mailbox,P2-roles,P3-supervisor-answerer}`）：四层模型（L1 通道 E28✅ / L2 身份信箱 E36 / L3 角色 E35 / L4 监督应答 E25），依赖 E33。4 决策：**并存·共抽象** · 覆盖 **E36+E35+E25 轻量安全** · **分层 answerer**（白名单自动答低危→升级人，E8≈拒答 confirmation）· **config `roles:` 段 + system_inject 模板**。复审收紧：agent_id 与 jobs.session_id 解耦 / agent_token 软隔离 / 自动答收窄 choice+options / **顺带修既有缺口**（job 终态对账残留 pending interaction，`InteractionCancelled` 现从未赋值）/ resume 重施 role / messages fan-out+TTL。新增面：2 additive 表 + 5 mcp 工具 + 6 `/v1` 端点 + roles 段 + 分层 answerer。bd epic `example-project-hyxz`(P1=y2jg/P2=fl46/P3=axma)。**只设计未实施**，可进 SUPMODE。 |
| v1.20 | 2026-06-27 | claude | **E28 client 模式 MVP 全落地+真机验收**（SUPMODE P1-P5，commit `b52901b`/`bf18bc7`/`3f57ec3`/`a21df1f`）：`gofer mcp` `Backend` 接口双实现（local/client）+ 3 client 方法 + 模式分支（env 默认 client + `--standalone` 逃生）。P2 抽取零行为变化（server_test 仅 New→NewLocal 一行）。E2E：双 mcp client 进程经同一 serve 共享状态（B 读到 A 的 job 输出）+ channel=mcp + standalone 回归全 PASS。E28🚧（信箱原语 E36 + 真互答 E2E 待）。design+plan `2026-06-27-e28-mcp-client-mode-*` |
| v1.19 | 2026-06-27 | claude | **Web 控制台 v2 只读层 全落地**（SUPMODE P1-P4，commit `b393fcf`/`05ef7b2`/`24224d2`/`de4288a`）：**E20✅**(项目 git 状态)·**E32✅**(子 git 发现+白名单关键文件)·**E19a✅**(产物 inline 预览 FilePreview marked+DOMPurify)·**E31✅**(集群拓扑 SVG 星型+节点面板，只读部分；配置编辑写层仍待)。+3 只读 endpoint(`/projects/{key}/git\|repos\|file`，browse.go 不 import job 包/SafeJoin+白名单+256KB+二进制拒/固定 git 参数)。go test+pnpm build 全绿 + agent-browser 眼检全 PASS(console 0 报错)。**前端改动，需 `make web` 重 embed + 重建二进制才在 web 控制台生效**。plan [`plans/2026-06-23-web-console-v2-readonly-plan.md`](plans/2026-06-23-web-console-v2-readonly-plan.md) |
| v1.18 | 2026-06-27 | claude | **E38①② 已落地+部署**（commit `deae1ae`，容器验证）：`GOFER_RUN_MODE=server\|worker` + `RunMode()`；`project list` 本地按角色读 yaml（worker→worker.yaml.projects）；`--remote` 走 `/v1/meta` 列服务端项目（新增 `client.ListProjects`）；单测 `TestRunMode`。**E38 三项(①②③)全完成。** 实测：默认无 config.yaml 提示 / RUN_MODE=worker 列 worker 2 项目 / --remote 列 server 3 项目 |
| v1.17 | 2026-06-27 | inhere+claude | **E38①② 厘清并合并**：`GOFER_RUN_MODE=server\|worker` 决定 project/CLI 读哪个本地 yaml（worker→worker.yaml.projects，与 config.yaml 同 ProjectConfig 类型）；`p ls --remote`(非 --server) 走 API 列服务端实时项目（补 client.ListProjects）；**mcp 不进 RUN_MODE**（standalone 仍需 config.yaml，client 由 E28 决定）。待实现 |
| v1.16 | 2026-06-27 | claude | **E38③ 已修**（commit `5be26be`，部署容器 CLI 验证）：`gofer wf` 子命令复用 `bindServerFlags` 读 `${GOFER_SERVER_ADDR/TOKEN}` env（与 `job` 一致），修复无 config 节点 wf 连不上 server。E38 ①② 仍待做 |
| v1.15 | 2026-06-27 | inhere | 新增 **E37 worker 配置向导**（交互式 init：填 server→拉 projects→cliui 多选→自动生成 .env 随机 worker token + server addr/token + worker.yaml）+ **E38 节点/CLI 易用性补强**（`p ls` 在 worker 查 server / `GOFER_RUN_MODE` 自动加载默认 yaml / `wf` 补读 server env 与 job 一致——③可立即修）。来源：用户想法讨论 |
| v1.14 | 2026-06-27 | inhere | 新增横切主题 **多 agent 协作 epic**（E28 通道/E36 身份寻址/E35 角色/E25 自动应答 收敛，依赖 E33；与自主化 epic 在 E25 交叠；统一信箱 answerer；落地先 E28 `--server` MVP→E36→E35/E25，攒够出合并 design） |
| v1.13 | 2026-06-27 | inhere | 新增 **E35 Agent 角色/人设预设库**（可复用运行方向 reviewer/bugfix，免重发提示；**命名取「角色 Role」**，与 E4 任务模板/E11 注入区分）+ **E36 Agent 身份注册/多会话寻址**（经 mcp 注册 name+id 到 serve、同工作目录多会话 id 区分、双向经中枢信箱；E28/E25 的身份层）。来源：用户想法讨论 |
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
| E19a ✅ | **文件 / 产物预览** | 中 | 中 | **(a) 产物 inline 预览已落地**（web v2 只读层 P2，commit `05ef7b2`）：`FilePreview.vue`（marked+DOMPurify sanitize / 图走 img 不内联 / json 格式化 / 大文件二进制回退下载）+ JobDetail 接入 + `fetchArtifactBlob`。**(b) 项目工作目录通用文件树仍不做**（路径穿越/`.env` 泄露风险）——E32 以白名单关键文件替代。 |
| E21 | **主机侧动作（编辑器打开等）** | 中 | 低 | 一键用主机编辑器打开项目（`code <host_path>`）/ reveal / 开终端。⚠️ gofer 在容器、编辑器在主机：**必须复用现有 codex-bridge 主机通道**（`host.docker.internal`），抽象成一类"主机侧动作"。仅对有主机 bridge 的部署可用。 |
| E23 | **定时任务（内置 cron）** | 中 | 中 | `schedules` 表 + 复用现有 sweeper loop 范式定时提交 job/工作流。问题：错过补偿、多 hub 重复触发（单 hub 不问题）。**触发位置（设计维度）**：① server 集中触发（默认、简单，复用 sweeper，worker 只执行被派的 job）vs ② 下发 worker 自触发（worker 内置调度器 + 本地 schedule，断网离线自治，但管理分散）——倾向默认①、②作可选高级。接 E24（定时+重试）、E18（定时跑导入的工作流）。属**自主化 epic**。 |
| E29 ✅ | **配置/部署模型简化（全局单 server + 项目瘦配置）** | 高 | 中 | **已落地**（SUPMODE 2026-06-23，P0-P3 commit `65cdb70`/`bfc6211`/`ae45372`/`9b2e6dd`，独立验收 D1–D9 全 PASS）。一台机一个 `serve` + 项目映射**全局单文件**；项目目录 `.gofer.project.yaml` **瘦配置**（仅偏好、无 server、准入留全局）。design [`design/2026-06-22-config-simplification-design.md`](design/2026-06-22-config-simplification-design.md) + plan [`plans/2026-06-23-config-simplification-plan.md`](plans/2026-06-23-config-simplification-plan.md)。Phase1（`init --global` + `GOFER_CONFIG` + `project add` 写全局）+ Phase2 overlay 合并（`buildCore`/`Reload`）+ cwd 推断免 `-p`；准入真源在 serve、CLI 不读 overlay（D2 安全）。**✅ 路径视角 `path_view`**（design D10，commit `f3a1db8`）：`server.path_view: host\|container`(默认 host) + `Config.ExecPath` 统一所有 gofer 侧路径（含 overlay/E32 扫描），修正 overlay 路径不一致；默认 host 零变化、job/httpapi 回归绿。 |
| E37 | **worker 配置向导（interactive init）** | 中 | 中 | `gofer worker init` 交互式生成 worker 配置：① 先填 **server URL + token** → ② 拉取 server 的 **projects 列表**（`GET /v1/projects`）→ ③ 经 **gookit/cliui `interact`**（prompt/多选）勾选本 worker 要接哪些 projects → ④ 全局配置目录**自动生成 `.env`**：随机 `GOFER_WORKER_TOKEN` + 写入 `GOFER_SERVER_TOKEN`/`GOFER_SERVER_ADDR`（供 CLI `job add`/`wf add` 零配置直用）+ 生成 `worker.yaml`（选中 projects、`allowed_runners` 含 `local`、对齐三处 `worker_id`）。**降低 worker 上手门槛**。扩展 **E3**(init)/**E29**(配置简化)。跨①轴。 |
| E38 ✅ | **节点 / CLI 易用性补强** | 中 | 低 | **①②③ 全部已落地+部署**（2026-06-27）。①② 合并为「**`GOFER_RUN_MODE` 决定 project/CLI 读哪个本地 yaml**」（✅ commit `deae1ae`，部署容器验证）：`GOFER_RUN_MODE=server`(默认)\|`worker` —— mode=server→`project` 命令读 `config.yaml.projects`；mode=worker→读 **`worker.yaml.projects`**（与 config.yaml 同 `map[string]ProjectConfig` 类型，列出渲染可复用，解决"worker 节点 `gofer p ls` 取不到 config.yaml"）。`p ls` 读本地（按 mode）；**`p ls --remote`**（**用 `--remote` 非 `--server`**，避免与 job/wf 的"服务器地址 `--server <addr>`"撞）→ `GET /v1/meta` 列**服务端实时**项目（`client.ListProjects` 已补，E28/E37 共用）。**mcp 不进 RUN_MODE**：standalone mcp 仍需加载 `config.yaml`（进程内执行需 projects/agents/runners），仅 E28 client 模式(`--server`)不加载——mcp 的 config 由 E28 standalone/client 决定。③ **✅ `gofer wf` 读 server env 已修**（commit `5be26be`，已部署容器 CLI 验证）：6 个 wf 子命令统一复用 `bindServerFlags`（绑入共享 `jobConnOpts`、带 `${GOFER_SERVER_ADDR/TOKEN}` 默认）。跨①轴。 |
| E39 | **Web 控制台 v3 导航 / 信息架构重构（壳层）** | 中 | 中 | 现 UI 布局滞后于功能、信息缺失（如 agent **roles** 未展示）；重构导航/菜单/壳层布局，补齐功能入口与缺失信息。**Web 控制台 v3** 主题（与 E40/E41/E42 收敛一份 design 统一 IA；E30 pty / E31③ config 写归此写层）。来源：用户使用反馈 2026-06-30。跨①③轴。 |
| E41 | **job 列表 / 详情体验补强** | 中 | 中 | 列表：**分页** + 末列时间 `hh:mm:ss`。详情：**running 即显 rendered exec 命令**（不等完成；补 E15 的 running 态 gap）· STDOUT/STDERR 两栏过窄 → **tab 切换**（默认 stdout）· **ANSI 终端色彩渲染**。Web 控制台 v3 主题。来源：用户反馈 2026-06-30。跨①轴。 |
| E43 | **内置 AI 助手（对接 LLM API）** | 中 | 大 | gofer 内嵌对接 LLM 的助手：自然语言**答疑 / 教用 / 帮配置**（读 gofer 文档 + 当前配置作答，降低上手门槛）；知识底座取 E44。⚠️ LLM key 管理（SR403/805 secret 不入库不回显）+ 上下文工程 + 成本；若能改配置/起 job 同样过鉴权红线。建议 **E44 先行、E43 随后**。来源：用户反馈 2026-06-30。跨①轴。 |
| E44 | **gofer 使用 skills（usage/manual + 完善 gofer-job）** | 中 | 低 | 新增 `gofer-usage`/manual skill（总览/答疑/手册），并审视完善现有 `gofer-job` skill。零后端、纯知识，便宜见效，可作 **E43 助手的知识底座**。来源：用户反馈 2026-06-30。跨①轴。 |

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
| E28 🚧 | **多 agent 经 gofer 协作通信（mcp HTTP-client 接入）** | 高 | 中 | **client 模式 MVP 已落地+真机验收**（2026-06-27，SUPMODE P1-P5，commit `b52901b`/`bf18bc7`/`3f57ec3`/`a21df1f`）：`gofer mcp` 加 `Backend` 接口双实现（localBackend/clientBackend）+ 3 client 方法（ListAgents/GetInteractions/AnswerInteraction 返 Interaction）+ 模式分支（`--server` 默认读 `GOFER_SERVER_ADDR` env → client；`--standalone` 逃生）。E2E：双 mcp client 进程经同一 serve 共享状态（B 读到 A 的 job 输出）+ provenance channel=mcp + standalone 回归。design [`design/2026-06-27-e28-mcp-client-mode-design.md`](design/2026-06-27-e28-mcp-client-mode-design.md) + plan [`plans/2026-06-27-e28-mcp-client-mode-plan.md`](plans/2026-06-27-e28-mcp-client-mode-plan.md)。**待做**：E36 信箱原语（`bridge_post/poll_message` + agent 注册，主动推送/双向寻址）、真互答 E2E、精确 tail SSE、**#6 mcp client 注册 presence 后 web/server-log 不可见（待补展示+注册日志，用户反馈 2026-06-30）**。<br>—— 原始构想 —— 让多个工作目录的 claude/agent 进程把 gofer 当**中枢**互通：A 派活给 B、信箱式 `pending_interaction` 互答、共享 `result`/`artifacts`。**分层结合**：**地基=中央 `serve`**（job 执行/状态/日志集中，前提非选项，已有）；**接入=给 `gofer mcp` 加 HTTP-client 模式**（8 个 `bridge_*` 工具从进程内直操 DB 改为转发到中央 serve，**复用 `internal/client` peer-http 客户端，小改造**）——一举消除 stdio 1:1 + "job 在哪进程执行/日志在哪/跨进程 SQLite 写锁"三坑。**信箱语义与 E25 可插拔 answerer 统一**（人工 Web / IM(E22c) / 监督 agent(E25) / 对等 agent 同一机制）；可选再加 message 原语（`bridge_post/poll_message`）。⚠️ MCP 是 client→server 单向工具调用、非对等总线，故只能"经中枢间接互通"，非两 claude 直连。分阶段：先零改造跑 serve+HTTP 验证协作语义 → 再补 mcp HTTP-client 体验。跨①②轴。 |
| E30 | **浏览器 pty 交互（attach 交互式 agent）** | 高 | 大 | Web 里经 pty(伪终端)直连 agent 的**交互式终端**（xterm.js + ws 双向流 input/output/resize），区别于结构化 `pending_interaction`（单问单答）——适合本身是 REPL/交互式 CLI 的 agent（claude 交互模式 / shell / 调试器）。**改执行模型**：agent 跑在 ptmx、stdin 接入，需 `interactive:true` job 选项或新 runner 模式；后端 `creack/pty`。远端 worker pty 经 hub ws 隧道转发（难点，local 先行 / 远端二期）。⚠️ pty ≈ 全 shell 能力，严格鉴权 + 会话审计（考虑录制）；定位"attach 交互式 agent"，**不做通用 web shell**（防后门）。属 Web 控制台 v2 写/交互层。跨①②轴。 |
| E33 ✅ | **agent session 捕获与恢复** | 高 | 中 | **已落地**（SUPMODE 2026-06-26，P1-P3 + 真机 E2E PASS）。获取**注入优先**：claude `--session-id <gofer-uuid>`（零解析、不需 json）/ codex `session id:` 正则捕获，best-effort 挂 `captureOutcomes`；additive 加 `jobs.session_id` 列；`gofer job resume <job-id>`（走 exec 路径、同 runner 约束）；worker 经 `Outcome.SessionID` 自报回传；`job list --session`。design [`design/2026-06-26-session-capture-design.md`](design/2026-06-26-session-capture-design.md) + plan [`plans/2026-06-26-session-capture-plan.md`](plans/2026-06-26-session-capture-plan.md)。跨②③轴。 |
| E35 | **Agent 角色/人设预设库 (role presets)** | 中 | 中 | 命名的可复用"agent 运行方向/行为预设"（reviewer / bugfix / 审核员…），让 agent 带固定 **system prompt + 规则 + 约束** 常驻一个方向运行，**免每次重发提示**。取向：`job run --role <name>` 基于预设创建运行；预设 = 基础 agent(claude/codex) + system prompt/规则 + 可选默认工具/项目/标签。**与 E4 严格区分**：E4 任务模板=一次性"做什么"(prompt+变量)；E35 角色=agent"是谁/怎么干"的**常驻身份/行为**。**与 E11 关系**：角色≈命名的可复用注入包(规则/上下文)，可构建于 E11 注入之上。**命名决策（2026-06-27）**：取「**角色 Role**」（备选 人设/Persona；**不用「模板」**避免撞 E4、**不用「人物」**叙事味）。跨①②轴。 |
| E36 | **Agent 身份注册 / 多会话寻址 (presence & addressing)** | 中 | 中 | agent 经 mcp（**E28 client 模式**）**注册自身到 serve**（name + id）→ 形成在线 agent 名册(presence registry)，使 serve 能**定向**与某个 agent 会话**双向**交互（非仅 agent→serve）。**同一工作目录开多个 agent 会话**：各分配 id 以区分/寻址。**MCP 约束**：client→server 单向工具调用、非对等总线 → "双向"经**中枢信箱**实现（agent 注册 + 按 id 轮询自己 inbox，复用 E28 信箱语义 / E25 可插拔 answerer）。**与 E33 关系**：可关联注入的 `session_id` 作为稳定身份。**定位**：E28 多 agent 协作 + E25 监督应答 的**身份/寻址层**（地基）。**+ #6/#7（用户反馈 2026-06-30）**：在线 driver 名册（含 `gofer mcp --server` client）需在 **web 展示** + server-log 打注册；sup / role **明显标记**（现 sup job 混在 job list 无标记）。跨②③轴。 |
| E42 | **全局交互通知 + Web 应答（写/交互层）** | 高 | 中-大 | 跨页面**右下角全局弹窗**提示 `pending_interaction`（含 sup 升级），简单交互（choice/confirm）**就地回复**；**实现 E25"升级人→Web 应答"面** + 通用交互应答。⚠️**前置硬阻断：现 web 为免鉴权 NotFound mount，加写操作前必须先定鉴权分级（SR201-204）**，否则任何人可替 agent 应答/放行审批门。Web 控制台 v3 写层（与 E30/E31③ 同层）。来源：用户反馈 2026-06-30。跨①②轴。 |

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
| E15 ✅ | **渲染命令可见** | 低 | 低 | 详情页显式展示"实际执行的命令"（`GET /v1/jobs/{id}/request` 回原始 JobRequest）。**#5 gap（用户反馈 2026-06-30）**：**running 态也应显示** rendered cmd（现仅完成后才显），前端经 E41 补。 |
| E16 ✅ | **指标 `/metrics`** | 中 | 低 | Prometheus：http/job 计数+时长、in_flight/queued/workers gauge。免认证 + 可选 token；route 模板防高基数。 |
| E17 ✅ | **per-caller 配额 / 限流** | 中 | 中 | per-caller 并发配额（信号量排队）+ 速率限流（令牌桶 429）；配置真源在 job.Service（SIGHUP 热加载）。 |
| E20 ✅ | **项目 git / 只读信息查看** | 中 | 中 | **已落地**（web v2 只读层 P1+P3，commit `b393fcf`/`24224d2`）：`GET /v1/projects/{key}/git`（cwd=ExecPath、固定只读 git 参数、出错降级）→ Projects.vue git 状态卡（分支/dirty/最近提交）。**与 E12 划清**：E12=某 job 改了什么；本项=项目当前状态。git 跑在 server 进程经 `ExecPath`（仅本地可达项目，D2）。 |
| E22 | **IM 连接（钉钉/飞书）** | 高 | 大 | 跨①②③轴。**拆三层**：(a) 出站通知接 IM（复用 E14，**最便宜先行**）；(b) 入站提交（IM 消息→job，新入口，回调验签 + IM 用户映射 caller_id）；(c) 交互应答（pending_interaction→IM 卡片→回复→续跑）。平台差异需 adapter。鉴权接 E17 caller。 |
| E31 🚧 | **节点拓扑 + 配置管理（Web）** | 中 | 中-大 | **① 拓扑图 + ② 节点面板（只读）已落地**（web v2 只读层 P4，commit `de4288a`）：`Cluster.vue` SVG 星型拓扑（hub+worker+peer+local，色=状态/心跳脉冲）+ 点击节点面板（worker 心跳/in-flight/labels、peer latency、local projects 概览），复用 `/v1/runners`+`/v1/projects` 无新后端；不画项目→节点边（D2，server 不知 worker projects）。**③ 配置查看/编辑（写层）仍待**：高危需鉴权分级 + secret 不回显（SR403/805）+ 编辑后 `config validate`，属 Web v2 写/交互层独立设计。跨①③轴。 |
| E32 ✅ | **项目空间浏览（子 git 发现 + 关键文件）** | 中 | 中 | **已落地**（web v2 只读层 P1+P3，commit `b393fcf`/`24224d2`）：① 子 git 发现 `GET /v1/projects/{key}/repos`（从 ExecPath WalkDir 深度≤3、跳 node_modules/vendor/dist/.git、上限 100，每个 branch+dirty）；② 关键文件 `GET /v1/projects/{key}/file?path=`（basename 白名单 README*/.gitignore/AGENTS.md/CLAUDE.md/go.mod/package.json/LICENSE* + SafeJoin + ≤256KB + 二进制拒）。Projects.vue 子仓列表 + 关键文件点击经 FilePreview 渲染。**白名单而非通用文件树**（防 `.env` 泄露+穿越）。属 Web v2 只读层。跨①③轴。 |
| E34 ✅ | **job 提交来源追踪 (provenance)** | 中 | 低 | **已落地+三端部署**（2026-06-27，commit `ff95515`，真机验收 PASS）。DB 记录原先看不出"谁/哪台/经哪渠道"提交（`caller_id` 来自共享 token 多为 `default`，且 `job show` 都没显示）。新增 `channel`(cli/web/mcp/im，**客户端声明**：CLI=cli 可 `--channel` 覆盖 / Web=web / MCP=mcp) + `client`(**来源主机/地址**：CLI 填 `os.Hostname()` / HTTP 提交 client 空时 **server 盖章 remote IP**，`X-Forwarded-For` 优先)，与既有 `caller_id` 共同回答提交来源。全链路：model + jobstore(additive 迁移 `channel/client` 列) + persistence + CLI(`job run` 盖章 / `show` 补 channel/client/caller_id / `list` 加列) + HTTP(盖章 IP) + MCP(channel=mcp) + Web(NewJob channel=web / JobDetail 显示)；`resume` 沿用源 job 来源。配套 **`job list` 改用 `gookit/cliui show/table`**（CJK-aware 列对齐，gcli v3.8.0 已 require cliui v0.3.1）+ gofer-job skill/runbook 补 `--cwd`(相对项目根，勿命令里 cd 绝对路径)/`--title` 约定。跨①③轴。 |
| E40 | **Dashboard 首页（聚合统计 + 状态总览）** | 中 | 中 | 首页改 dashboard：服务/节点状态、任务统计（总/成功/失败）、项目数等一眼观察整体运行态。**需新增轻量后端聚合**（建议 `/v1/stats` 或扩 `/v1/meta`，勿前端拉全量 job 自算）。Web 控制台 v3 主题。来源：用户反馈 2026-06-30。跨①③轴。 |

---

## 横切主题：自主化 epic（E23/E24/E25/E26 收敛）

E23 定时 + E24 自动重试 + E25 监督应答 + E26 hooks 共同把 gofer 从"人工逐个提交"推向"**自主运转的 agent 编排平台**"。它们**共享同一套地基**，应合成一个 epic 统一设计，而非各做一套：

- **方向性张力（先拍板）**：自主化与 gofer 现有"人在环路"安全取向**直接冲突**——要先定 gofer 要多"自主"。自主能力**必须**配 E8 审批门兜底，否则失控。
- **统一抽象（别重复造）**：① `pending_interaction` 应答的四种来源——人工 Web / IM 人工(E22c) / 监督 agent(E25) / 对等 agent(E28)——是同一机制的**可插拔 answerer**；② 失败处理的四个面——手动 rerun(E2) / 自动重试(E24) / hook 决策(E26) / 工作流 on_failure——应**统一一套重试/失败策略**。
- **三个隐含前置（闭环必需）**：强审计（E13 事件标注"AI 自动 vs 人"）· **配额约束**（自主烧 token，受 E17 配额管）· 可接管（人能暂停/接管自主链）。

---

## 横切主题：多 agent 协作 epic（E28/E36/E35/E25 收敛，依赖 E33）

把 gofer 从"单 agent 提交执行 job"推向"**多个 agent 经 gofer 中枢互相派活、协作、互答**"。这条主线由几块拼成，应**分层叠加**而非各做一套（且与「自主化 epic」在 E25 处交叠）：

- **分层（自下而上）**：① **通道**＝中央 `serve` + **E28 `gofer mcp --server` client 模式**（多 agent 共指同一 serve、状态一致，已定为便宜先行 MVP）→ ② **身份/寻址**＝**E36** agent 注册 name+id + 同工作目录多会话 id 区分（serve 能定向到某会话）→ ③ **角色化**＝**E35** 角色预设（agent 带 reviewer/bugfix 等常驻方向运行）→ ④ **自动应答**＝**E25** 监督 agent 自动作答 `pending_interaction`（A 派活、B 自动答）。
- **统一抽象（别重复造）**：`pending_interaction` 应答的四种来源——人工 Web / IM 人工(E22c) / 监督 agent(E25) / 对等 agent(E28/E36)——是**同一信箱机制的可插拔 answerer**；agent 间"双向"因 **MCP 是 client→server 单向工具调用、非对等总线**，统一**经中枢信箱**实现（注册 + 按 id 轮询 inbox），不做两 agent 直连。
- **依赖与边界**：依赖 **E33** session_id 作 agent 稳定身份；与「自主化 epic」共享 **E8 审批门**（高危兜底）· **E17 配额**（多 agent 烧 token 受控）· **E13 审计**（标注"哪个 agent/AI 自动 vs 人"）。
- **落地节奏**：先 **E28 `--server` MVP**（消除状态割裂、跑通"经中枢间接协作"语义）→ 再补 **E36** 身份/信箱原语（`bridge_post/poll_message` + register）→ 然后 **E35** 角色 / **E25** 监督应答。✅ **合并 design + plan 已出**（2026-06-28，design [`design/2026-06-28-multi-agent-collab-design.md`](design/2026-06-28-multi-agent-collab-design.md) v0.2 + plan [`plans/2026-06-28-multi-agent-collab-plan.md`](plans/2026-06-28-multi-agent-collab-plan.md) 总纲 + P1/P2/P3 子 plan）把 E28/E36/E35/E25 串成四层模型 + 4 决策 + 对抗复审 7 处收紧，bd epic `example-project-hyxz`。**待进 SUPMODE 实施**（P1 E36→P2 E35→P3 E25）。

---

## 横切主题：Web 控制台 v2（E19/E20/E21/E30/E31/E32 收敛）

把 Web 控制台从"看板 + 详情 + Workers 名册"推向"**集群可观察 + 项目透视 + 可交互操作**"。这批增强按**只读 vs 写/交互**切两层（安全闭环，SR1402）：

- **只读观察层（便宜先行，数据源多现成、无写风险）**：E31 拓扑图 + 节点面板(只读) · E32 子 git 发现 + 关键文件 · E19(a) 产物预览 · E20 项目 git 状态。**✅ 全落地**（2026-06-27，SUPMODE P1-P4，commit `b393fcf`/`05ef7b2`/`24224d2`/`de4288a`，go test+pnpm build+agent-browser 眼检全 PASS）：design [`design/2026-06-23-web-console-v2-readonly-design.md`](design/2026-06-23-web-console-v2-readonly-design.md) + plan [`plans/2026-06-23-web-console-v2-readonly-plan.md`](plans/2026-06-23-web-console-v2-readonly-plan.md)。前端改动**需 `make web` 重 embed + 重建二进制**才在控制台生效。
- **写 / 交互层（重、高危，各需独立安全设计）**：E30 pty 交互（改执行模型 + 会话审计）· E31 配置编辑（写回 + reload + 鉴权分级 + secret 不回显）· E21 主机侧动作（复用主机 bridge）。
- **统一前置**：鉴权分级（当前 token 平权，写/交互操作需更细粒度）· 审计（写操作 / pty 会话入 E13 事件流）· secret 不回显（SR403/805）。

---

## 建议优先级（下一步做什么）

**已完成（✅）**：E1/E2/E3/E5/E6/E12/E13/E15/E16/E17，**E7 ✅（v1+v2 全落地）** + 随 v2 的 **E9✅ / E18✅ / E27✅**，**E29✅**（配置简化）· **E33✅**（session 捕获/恢复，含 resume 豁免 allow_exec + 详情全展示收尾）· **E34✅**（提交来源追踪 channel/client + job list cliui table）· **E38✅**（节点/CLI 易用性：GOFER_RUN_MODE 本地按角色读 yaml + `p ls --remote` + wf 读 server env）· **Web 控制台 v2 只读层✅**（E19a 产物预览 / E20 项目 git / E32 子 git+关键文件 / E31 拓扑+节点面板，2026-06-27），E14🚧（webhook，MQ 不做）/E24🚧（工作流 step 级重试已落地，独立 job 级重试最小版）/E31🚧（只读层已落地，配置编辑写层待） · **E28 client 模式 MVP✅**（gofer mcp Backend 接口双实现 + 模式分支，多 agent 经中枢协作地基，2026-06-27；E36 信箱原语待）。原三轴核心缺口基本补齐。

**便宜先行（低成本、边界清晰）：**
1. ~~**Web 控制台 v2 只读层**（E31 拓扑+节点面板 · E32 子 git+关键文件 · E19a 产物预览 · E20 git 状态）~~ —— **✅ 已落地**（2026-06-27，commit `b393fcf`..`de4288a`）。剩 **E22(a) IM 出站通知**（复用 E14，未做，需小 design）。
2. ~~**E28 `gofer mcp --server` client 模式 MVP**~~ —— **✅ 已落地**（2026-06-27，SUPMODE P1-P5，commit `b52901b`..`a21df1f`）：Backend 接口双实现 + 3 client 方法 + 模式分支（env 默认 client + `--standalone` 逃生），双 client 真机协作验收 PASS。**多 agent 协作 epic 下一片 = E36 信箱原语**（agent 注册 + inbox 主动推送 + 双向寻址）。

**第二梯队（中等、承接已有）：**
3. **E4 模板库**（接 E18）· **E20 项目 git 信息**（接 E19）· **E11 上下文/规则注入**（含规则文件）。（E28 mcp client 模式已提到「便宜先行」item 2。）
4. ~~**工作流 v2 epic**：E27 子工作流/跨项目 + E9 fan-out + E24 重试 + E7 尾巴~~ —— **✅ 已落地**（design [`design/2026-06-22-workflow-v2-design.md`](design/2026-06-22-workflow-v2-design.md) + plan [`plans/2026-06-22-workflow-v2/`](plans/2026-06-22-workflow-v2/)，commit `7c470b8`..`92cc669`）。**剩余尾巴**：工作流模板库(E4)、export secret 启发式剥离非保证、子 wf retry 重跑整条、独立 job 级重试可靠版(E24)。

**大件（需独立设计，先对齐取向）：**
5. **自主化 epic**：**E8 审批门**（先行，做安全闸）→ **E23 定时** → **E25 监督应答** + **E26 hooks** + **E22(b/c) IM 双向**——先出设计文档对齐"自主程度"，再排。
6. **多 agent 协作 epic**（E28 通道 + E36 身份/寻址 + E35 角色 + E25 自动应答）：首片 **E28 `--server` MVP 已在「便宜先行」item 2**；全 epic 需先出**合并 design**（见上「多 agent 协作 epic」横切），与自主化 epic 在 E25 交叠、共享 E8/E17/E13。
7. **E10 mcp-agent**（按需）。

> **一句话主线**：原"看得见 agent 产出/改了什么"已补齐（E1/E6/E12/E13/E15/E16/E17）；下一程向**自主化（定时/重试/监督/hooks）与连接（IM/编辑器）**扩展，且**每一步自主都先有审批门(E8)兜底、受配额(E17)约束、留审计(E13)痕迹**——让 gofer 既能自己跑，又始终可控可审计。
