# TODO

> 增强/优化/扩展的整体思考与优先级见 [`2026-06-20-enhancements-roadmap.md`](2026-06-20-enhancements-roadmap.md)（按"易用 / 用好 agent / 可观察审计"三轴 + 建议梯队）。

- [x] 新增 AGENT_BRIDGE_CFG_DIR 自定义全局的配置目录(不设置默认还是 ~/.config/dev-agent-bridge/)
  - `config.ConfigDir()` 统一解析；`UserConfigPath()` 复用；config.yaml 与全局 .env 都落在该目录下。
- [x] 使用 goutil/envutil 支持先于配置加载 .env 文件
  - `config.LoadDotenv()` 在 main 启动最早期调用；先加载 `<cfg-dir>/.env` 再加载当前目录 `./.env`(后者覆盖前者)；已导出的 OS env 始终优先。
- [x] 项目改名为 **Gofer**（代码/构建/CLI/env/运行时**及根目录**均已完成）
  - 含义：gofer = "跑腿取送的人"，一词概括"派任务给 agent → 在目标项目目录执行 → 回传日志/结果"全链路；自带 `gofer`↔`gopher` 的 Go 双关。
  - 已落地（commit `dcba644` 代码 + 文档）：module `github.com/inhere/gofer`、binary/CLI `gofer`、env 前缀 `GOFER_`、运行时名 `~/.config/gofer`、`gofer.db`、默认结果子目录 `gofer`；README 同步。
  - 重名核查：同名 [`clintjedwards/gofer`](https://github.com/clintjedwards/gofer)(任务编排引擎,30★)**已于 2025-06 归档并转 Rust**，Go 圈该名实质让出；碰撞可忽略。
  - 备选(未采纳)：Ferry(撞 Shopify + 偏"运输")、Convey(强撞 `goconvey/convey`)、Errand(撞在世纯 Go 任务运行器 `nuvrel/errand`)。
  - 根目录改名 `tools/dev-agent-bridge` → `tools/gofer` 已完成：外层主仓不跟踪该独立子仓(无 submodule/gitignore 条目)、无 linked worktree、代码无硬编码路径、外层 CLAUDE.md/workspace 无引用，mv 后仅更新 git safe.directory + live 文档路径。
  - **保留(史实)**：历史 design/plan/runbook 文档记录 codex-bridge→dev-agent-bridge 迁移过程，及 dated 文件名 `*-dev-agent-bridge-*.md`，均不回改（如需全量对齐再单列）。
- [x] 给 peer-http runner 补 P9 交互透传（commit `1d8ed80`）
  - mirrorStream 处理 peer SSE 的 `interaction` 帧 → 经新增 `runner.InteractionSink`（`job.injectInteraction`）注入 host job（host 转 pending_interaction）；host 侧作答经 `client.AnswerInteraction` POST 回 peer `/answer` 续跑。中性类型在 runner 基包避免 import 环。测试 host+peer 双 bridge E2E。
- [x] 支持远端机器运行作为客户端与server通信，ws 协议保持连接（**WP1–WP3 + WP4 Workers 仪表盘已落地**，2026-06-19 SUPMODE，逐阶段独立验收全 PASS）
  - 设计 [`design/2026-06-17-ws-remote-worker-design.md`](design/2026-06-17-ws-remote-worker-design.md) + 实施计划 [`plans/2026-06-19-ws-worker-c6c7/`](plans/2026-06-19-ws-worker-c6c7/)（主 + P0..P4 子文档）。
  - 落地：P0 spike(`f63e4e4`) / WP1 端到端远程执行(`7442ff6`..`f414742`) / WP2 交互透传+cancel/timeout(`5adefc2`..`e3ca797`) / WP3+C7 心跳/重连/worker-lost/多 worker+多地址退避(`3537826`..`2d98d13`) / WP4 Workers 仪表盘前端(`84655cf`)。
  - 客户端也有任务记录信息 → 已解：server 镜像为 server 侧真源，worker 本地另留一份，互不耦合。
  - 任务输出详情在客户端，server 如何读取？→ 已解：worker 推日志帧、server 写进自己 result_dir，复用既有读路径。
  - **WP4 标签自动调度已落地**（P2，commit `df86f5e`）：`worker_labels` 全包含 AND 选机 + worker runner 动态路由解绑（保 `rc.WorkerID` 兜底）；无候选 503。见 [`plans/2026-06-20-submit-dispatch/`](plans/2026-06-20-submit-dispatch/)。
  - **仍缓做**：多 hub HA（C7 大版，独立 Epic，见本文 §大型 Epic）。WP4 仪表盘动画/响应式待真机浏览器眼检。
- [x] http 请求可以发送 md 格式文本 头部可以用 yaml 定义参数 后面就是任务描述 —— **已落地（P1-b，commit `730b6bb`）**：`Content-Type: text/markdown` 分支，yaml frontmatter→参数、正文→prompt（仅 cli-agent）。见 [`plans/2026-06-20-submit-dispatch/`](plans/2026-06-20-submit-dispatch/)。
- [x] 提交 exec 任务允许同步等待返回，方便快速的执行简单命令 —— **已落地（P1-a，commit `730b6bb`）**：`POST /v1/jobs` `sync`/`?wait=1`，服务端等终态（默认 30s/顶 60s，超时 202+`X-Gofer-Async`）；CLI `--sync`。

### Web 控制台

- [x] **webui 浅色模式已落地**（2026-06-19，用 `frontend-design` skill）。深色保持默认；浅色为「日班·冷调蓝灰制图纸/搪瓷调度板」——非通用暖奶油，沿用同一套状态色但压暗达 WCAG AA。
  - 落点：`tokens.css` 加 `:root[data-theme="light"]` 覆盖块（明/暗双套语义变量 + 抽出 `--term-bg` 替换 LogTape/InteractionCard 两处硬编码 `#08121a`；每 `:root` 带 `color-scheme` 让原生控件/滚动条跟随）。
  - 切换：`store/theme.ts`（localStorage 持久化 + 未选择时跟随系统 `prefers-color-scheme` 并监听其变化）；`components/ThemeToggle.vue` 切换器放顶栏 + 登录页右上角；`main.ts` 挂载前 `initTheme()` 减首屏闪烁。
  - 验收：`pnpm -C web build` 绿（vue-tsc）；agent-browser 截图深/浅两态 + 状态色板眼检 PASS（`tmp/theme-shots/`）；切换/持久化/reload/跟随系统均验证；对比度脚本核验正文+次要文本≥4.5:1；未引入新动画（`prefers-reduced-motion` 不受影响）。
  - 注：构建产物（`web/dist` / 嵌入二进制的 `internal/webui/dist`）未提交，按既有约定由 `make web build` 流水线生成。
- [x] **Workers 仪表盘已落地**（`/runners` 视图：Workers/Peers/Local 名册 + 心跳脉冲签名 + 在飞/标签 + 实时年龄轮询，commit `84655cf`，用 `frontend-design` skill；消费 C6 `/v1/runners`）。
- [x] **控制台进一步适配已落地**：~~① 看板/详情行内展示 `runner`/`worker_id`~~ —— **① 已落地（commit `169e48e`）**：Board runner 列堆叠 worker_id、JobDetail meta 加 worker_id，远端 phosphor 凸显；~~②（更大）控制台内**提交表单**~~ —— **② 已落地（P3，commit `7c7082f`+`15aef7c`）**：`NewJob.vue` 选 项目/agent/runner/worker_id 或 labels，端到端冒烟 PASS。详见 [`design/architecture-overview.md`](design/architecture-overview.md) §9.2 + [`plans/2026-06-20-submit-dispatch/`](plans/2026-06-20-submit-dispatch/)。
  - 注：peer-http/worker 远端 job 的交互/日志因"镜像"机制已透明呈现在现有看板/详情，无需改前端即可用；本项是让远端执行**位置**进一步可见（①）+ 控制台内发起 job（②已做）。

### 架构加固（见 [`design/architecture-overview.md`](design/architecture-overview.md) §9.1）

- [x] **C1 ✅ 已完成（2026-06-18 SUPMODE）**内存 job 表 + `jobs.jsonl` 无界增长 → **SQLite 存储后端**。
  - 设计：[`design/2026-06-18-sqlite-store-design.md`](design/2026-06-18-sqlite-store-design.md)（SP1–SP5，实施结果与 §16 补记见文末）。
  - 落地：modernc 纯 Go SQLite；DB 存 job 元数据/索引/交互，日志仍文件，内存仅留 live job（终态驱逐），retention 保留策略周期 prune 清磁盘。commit `7155917`/`08b3713`/`aebe474`/`fcc6572`/`f3ec55b`。
  - 补记：jobstore 加进程内写锁消除 SQLITE_BUSY；交互对已驱逐终态 job 确定性返回 409。
- [x] **C2 ✅** 单一 token 无身份/吊销 → per-worker/per-caller token（多 caller token + `subtle` 常时间比对 + caller_id 入库防伪；commit `8200654`/`61fd149`/`07065ce`）。
- [x] **C3 ✅** 配置无热加载 → SIGHUP 热重载（`atomic.Pointer` 原子替换 registry/service cfg + 失败安全含被删配置守卫；commit `17dbfb8`/`289d138`）。
- [x] **C4 ✅** 日志流控（LogWriter 轮转 + SSE 帧 cap/分片/动态节流/rotated 标记 + 前端 buffer 窗口；commit `7fa6080`/`614db2b`/`f7e5f47`）。
- [x] **C5 ✅** 提交幂等键（`request_id` 部分唯一索引 + Submit 前置查重 + 并发唯一冲突回退；commit `8200654`/`07065ce`）。
- [x] **C6 ✅** 远端节点健康探针（`GET /v1/runners`：worker 心跳态 + peer-http 周期主动探针；commit `ae6a630`..`0db128b`）。
- [x] **C7 🟡 最小版** 多 hub HA → 仅 worker 多地址 + 全抖动退避重连（`a43a694`）；**多 hub 共享注册表 / 跨 hub job 接管 / 选主仍显式 out-of-scope**。
  - 已知限制（见 [`plans/2026-06-18-hardening-c2-c5-plan.md`](plans/2026-06-18-hardening-c2-c5-plan.md) §7.1）：
    - **C-N1（低）** job `exit 0` 但 background 子进程持有管道写端超 `WaitDelay`(2s) → `exec: WaitDelay expired before I/O complete`，runner 判为 `failed`（非回归）；仅"起 daemon 后退出"成受支持模式时才 follow-up。
    - **C-N2（极低）** `log-rotated` 后前端清 buffer 但不重置 `?from=` offset，重连靠 `tailFrom` 自愈（size<offset 重放全新文件）；MVP 不修。

### 工作流引擎（E7：v1 线性 + v2 重试/并行/子工作流，见 [roadmap](2026-06-20-enhancements-roadmap.md) §工作流）

- [x] **v1 线性 chain 已落地**：单活跃 step + `${steps.N.xxx}` 引用 + fail-fast + 幂等推进 + sweeper 兜底 + CLI/HTTP/Web。设计 [`design/2026-06-21-workflow-chains-design.md`](design/2026-06-21-workflow-chains-design.md)。
- [x] **v2 已全落地**（2026-06-22，SUPMODE，P1–P4 独立验收 PASS + 前端眼检修 2 bug）：per-step on_failure/retry(E24) + 并行 fan-out+join all/any/quorum(E9) + 子工作流嵌套+跨项目(E27) + workflow 事件流/retention + 导入导出+md-per-step(E18)。设计 [`design/2026-06-22-workflow-v2-design.md`](design/2026-06-22-workflow-v2-design.md) + 计划 [`plans/2026-06-22-workflow-v2/`](plans/2026-06-22-workflow-v2/)；commit `7c470b8`(P1)/`4492871`(P2)/`dc71b06`(P3)/`92cc669`(P4)，前端修复 `ac6fa2f`。
  - **不做**：通用菱形 DAG（任意前驱依赖图，留 v3）、循环/条件跳转、跨工作流引用中间 step、worker 远端跨机产物自动拉取（依赖共享盘）。
  - **剩余尾巴**：工作流模板库(E4，save/`--template`)未做；export secret 剥离是启发式非保证；子 wf retry 重跑整条（非断点续跑）；独立 job 级自动重试为最小版（持久化退避可靠版待做，见 roadmap E24🚧）。

### 大型 Epic（需独立设计，暂不并入近期实施计划）

- [ ] **C7 大版：多 hub HA（共享注册表 / 跨 hub job 接管 / 选主）** —— 工作量**大（多周级）**，且与当前「单二进制 + 本地 SQLite + 单 hub 内存 worker 注册表」设计取向有冲突，**必须先出独立设计文档再排计划**，不与 WP4/提交体验类小项混排。
  - **为何大**：要同时解三件强耦合的事——① **共享状态**：job/worker 注册表跨 hub 可见（现为各 hub 本地 SQLite + 进程内 registry）→ 需引入共享存储（MySQL/PG/Redis）或 hub 间复制/gossip；② **跨 hub job 接管**：hub A 宕机后 hub B 接管 A 的在飞 job（worker 凭多地址重连到 B，但 B 需经共享状态认领该 job + 租约/owner 语义防双发）；③ **选主 / 调度协调**：哪个 hub 跑 sweeper/派发，避免重复调度 → 需 raft/etcd/redis-lock 或外部协调者。
  - **牵动面**：存储层（从本地 SQLite 改为可共享后端或加复制）、worker 注册表（集群感知）、job ownership/lease、sweeper 协调、WS 重连认领语义、部署形态（多 hub 实例 + 共享后端）。
  - **现状已覆盖常见场景**：C7 最小版（worker 多地址 + 全抖动退避重连，`a43a694`）已把「hub 重启」从永久失联降为短暂中断；**真零停机 / hub 横向扩容**才需要大版——内网开发工具多数尚不需要。
  - **决策前置**（设计文档先答）：是否真有「hub 高可用 / 横扩」诉求？若有 → 选型 **共享存储派**（最简：共享 MySQL + DB 租约选主，复用公司中间件）vs **共识派**（raft/etcd，重）；先定这一轴再谈实现。
  - 来源：`design/architecture-overview.md` §9.1 C7 行 + `plans/2026-06-18-hardening-c2-c5-plan.md` §7.1（显式 out-of-scope）。
