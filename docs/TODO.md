# TODO

- [x] 新增 AGENT_BRIDGE_CFG_DIR 自定义全局的配置目录(不设置默认还是 ~/.config/dev-agent-bridge/)
  - `config.ConfigDir()` 统一解析；`UserConfigPath()` 复用；config.yaml 与全局 .env 都落在该目录下。
- [x] 使用 goutil/envutil 支持先于配置加载 .env 文件
  - `config.LoadDotenv()` 在 main 启动最早期调用；先加载 `<cfg-dir>/.env` 再加载当前目录 `./.env`(后者覆盖前者)；已导出的 OS env 始终优先。
- [ ] 项目名(暂缓，沿用 dev-agent-bridge / AGENT_BRIDGE_ 前缀)：
  - Ferry （渡船 / 摆渡人） 含义：把 prompt/命令“摆渡”到目标项目目录里执行，再把日志/结果“摆渡”回来。完美替代“bridge”的动态感（bridge是静态的，ferry是主动往返的）
  - Convey（输送 / 传达） 含义：强调“输送任务 + 回传结果”的管道感，同时带有“传送指令”的语义，非常贴合 {项目, agent, prompt} 的提交模式。
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

- [ ] **C1（进行中，下一个实施任务）**内存 job 表 + `jobs.jsonl` 无界增长 → **SQLite 存储后端**（已定方向，无 stopgap）。
  - 设计+计划：[`design/2026-06-18-sqlite-store-design.md`](design/2026-06-18-sqlite-store-design.md)（SP1–SP5，**SP1–SP3=C1 核心**）。
  - 已定：modernc 纯 Go SQLite（容器内可构建）；DB 存 job 元数据/索引/交互，日志仍文件，内存仅留 live job；**不迁移/fresh-start/直接切**；request.json SP1/SP2 留文件、SP5 入列。
  - 下一步：SUPMODE 推进 SP1→SP3 再 SP4/SP5（clear 后从 SP1 开始）。
- [ ] C2 单一 token 无身份/吊销 → per-worker / per-caller token。
- [ ] C3 配置无热加载 → SIGHUP/接口热重载 registry。
- [ ] C4/C5/C6/C7：日志流控、提交幂等键、远端节点健康探针、多 hub HA（按需）。
