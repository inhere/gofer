# TODO

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
- [ ] 支持远端机器运行作为客户端与server通信，暂定使用 ws 协议通信并保持连接
  - **设计已细化** → [`design/2026-06-17-ws-remote-worker-design.md`](design/2026-06-17-ws-remote-worker-design.md)（Worker 执行机 + 流式推送 server 镜像 + 显式 worker_id + 与 peer-http 并存；WP1-WP4 分期）。待评审后写实施计划。
  - 客户端也有任务记录信息 → 已解：server 镜像为 server 侧真源，worker 本地另留一份，互不耦合。
  - 任务输出详情在客户端，server 如何读取？→ 已解：worker 推日志帧、server 写进自己 result_dir，复用既有读路径。

### Web 控制台

- [ ] **webui 浅色模式**：当前仅 CRT/终端深色主题。出一套浅色模式（主题切换 + 持久化偏好，跟随系统 `prefers-color-scheme`）。
  - **实施时必须使用 `frontend-design` skill**（设计方向/配色/对比度，避免套默认模板）。
  - 落点：`web/src/styles/tokens.css` 抽明/暗两套 CSS 变量；切换器组件 + sessionStorage/localStorage 记忆；保证对比度达标、`prefers-reduced-motion` 不受影响。
- [ ] **控制台适配新架构（让"在哪执行"可见）**：看板/详情展示 `runner`（及未来 `worker_id`）；ws-worker 落地后加 **Workers 仪表盘**（连入列表/心跳/在飞 job/标签）；（更大）控制台内提交表单。详见 [`design/architecture-overview.md`](design/architecture-overview.md) §9.2。
  - 注：peer-http 远端 job 的交互/日志因"镜像"机制已透明呈现在现有控制台，无需改前端即可用；本项是让远端执行位置**可见**。

### 架构加固（见 [`design/architecture-overview.md`](design/architecture-overview.md) §9.1）

- [x] **C1 ✅ 已完成（2026-06-18 SUPMODE）**内存 job 表 + `jobs.jsonl` 无界增长 → **SQLite 存储后端**。
  - 设计：[`design/2026-06-18-sqlite-store-design.md`](design/2026-06-18-sqlite-store-design.md)（SP1–SP5，实施结果与 §16 补记见文末）。
  - 落地：modernc 纯 Go SQLite；DB 存 job 元数据/索引/交互，日志仍文件，内存仅留 live job（终态驱逐），retention 保留策略周期 prune 清磁盘。commit `7155917`/`08b3713`/`aebe474`/`fcc6572`/`f3ec55b`。
  - 补记：jobstore 加进程内写锁消除 SQLITE_BUSY；交互对已驱逐终态 job 确定性返回 409。
- [ ] C2 单一 token 无身份/吊销 → per-worker / per-caller token。
- [ ] C3 配置无热加载 → SIGHUP/接口热重载 registry。
- [ ] C4/C5/C6/C7：日志流控、提交幂等键、远端节点健康探针、多 hub HA（按需）。
  - 已知限制（见 [`plans/2026-06-18-hardening-c2-c5-plan.md`](plans/2026-06-18-hardening-c2-c5-plan.md) §7.1）：
    - **C-N1（低）** job `exit 0` 但 background 子进程持有管道写端超 `WaitDelay`(2s) → `exec: WaitDelay expired before I/O complete`，runner 判为 `failed`（非回归）；仅"起 daemon 后退出"成受支持模式时才 follow-up。
    - **C-N2（极低）** `log-rotated` 后前端清 buffer 但不重置 `?from=` offset，重连靠 `tailFrom` 自愈（size<offset 重放全新文件）；MVP 不修。
