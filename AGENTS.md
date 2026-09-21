# Gofer 项目规则

> 本项目稳定开发约定（避免重蹈）；详情见对应 design/plan，本处不赘述。

## 项目规则

- **G031**: 这是独立工具库，**不要** 将任何业务相关的信息写入提交、文档、代码或配置里
- **G033 CLI 分组（2026-09-20）**：新增的**小工具类命令**一律放在 `gofer tool <name>` 组下（如 `gofer tool cp`、`gofer tool xfer`），不新增顶级命令；只有核心资源名词（job / plan / session / agent / worker / template / schedule / wf / tun / project / config / init）保持顶级组。
- **G032 兼容策略（2026-09-18，项目仍在 1.0 前）**：不积累永久兼容层。仍需保留的兼容分支（旧配置键、旧 wire 字段、一次性迁移读取）必须打移除标记 `// DEPRECATED(v<引入版本>): remove in v<+3>`，并在 design/docs 记一笔，几个版本后删除；**没人用或不重要的兼容路径直接剔除**（旧别名、预发布行为回退、已不再部署的 worker 版本容忍），不要为用户不运行的组合设计"老 server/老 worker 也能跑"。additive schema 迁移与可选协议字段照常允许。

### 配置与路径

- **G001 单机部署收敛**：一台机一个 `serve`，项目映射收敛全局 `~/.config/gofer/config.yaml`（`GOFER_CONFIG` 锁定、`project add` 默认写全局）；项目目录可放 `.gofer.project.yaml` 瘦配置（仅偏好，**无 server/storage、准入字段留全局**）。详见 `docs/design/2026-06-22-config-simplification-design.md`。
- **G002 执行路径视角**：gofer 进程侧一切路径（执行 cwd / 读 overlay / 扫描）统一走 `Config.ExecPath(proj)`，由 `server.path_view: host|container`（默认 host）决定取 `host_path` 或 `container_path`；**不做容器自检**。主机侧动作（编辑器打开等）恒用 `host_path`。
- **G003 配置目录 ENV**：用户级配置目录环境变量是 `GOFER_CONFIG_DIR`（常量 `config.EnvConfigDir`）；代码引用常量、勿写死字面量。

### CLI

- **G011 `-c` 统一绑定**：`-c/--config` 经公共 `bindConfigFlag(c)` 绑 `config.InputCfgFile`，命令前后均可放；新增子命令一律调它、**勿各自重复绑定 `-c`**。worker 配置独立用 `--worker-config`（worker.yaml，语义不同，不走 app `-c`）。
- **G012 gcli 行为坑**：gcli 无 App 级 PersistentFlags——app 级 flag 只在命令名**之前**消费、不下放子命令；要"命令后也可用"须命令级绑定（即 G011 的 helper）。shell 补全是 gcli 内置 `--gen-completion`（**非** `completion` 子命令）。
- **G013 daemon 模式（`-d/--daemon`）**：serve/worker 后台化用 env-sentinel **re-exec 自身**（Go 不能安全 fork），编排在 `internal/daemon`（`Setsid` 真 detach，unix 实现/windows 报不支持）；`commands` 入口**只**判断 `-d` 并调 `daemon.Spawn`/转发（G021）。运行时文件统一 `<config-dir>/run/`（serve.{pid,log} / worker-`<id>`.{pid,log}），经 `config.RuntimeFilePath` 解析；停机是启动命令的子命令 `gofer serve stop` / `gofer worker stop [<id>]`（worker 省略 `<id>` 时取 worker.yaml 的 `worker_id`），读 pidfile 发 SIGTERM，共用 `stop.go` 的 `stopDaemon`。serve 优雅停机靠 `httpapi.Server.RunCtx`(ctx 取消→`Shutdown`)，worker 靠既有 `worker.Serve`。

### 代码分层（重构后基线，B 组 2026-06-25）

- **G021 入口只做绑定/校验/转发**：`commands`/`httpapi`/`mcpserver` 三入口层不放业务/编排逻辑；编排放 `internal/core`(组装)/`serve`(进程编排)/`streaming`(流式)，业务放 `internal/job` 等。非命令入口（组装、健康探针等）**不放 `commands`**。
- **G022 依赖单向、防环**：入口 → 编排(core/serve/streaming) → job → 数据层(jobstore/project/agent/runner/store/config…)；底层/业务层**绝不**反向 import 入口/编排层。新增包后以 `go build`/`go vet`/`go list -deps` 验环。详见 `docs/design/2026-06-25-code-layering-refactor-design.md`。
- **G023 重构铁律**：搬迁/拆分代码**零行为变化**（函数体逐字，仅改包/导出性/import），每步全量 `go test ./...` 绿背书；专属测试随逻辑迁移、覆盖不降。
- **G024 子域升包判据 + 依赖倒置**：拆文件改善阅读、**升包改善边界**；一个子域满足「域自洽 + 反向 seam 够窄 + 正向可接口化 + 收益>代价」(D-B8) 才升为子包，否则留包内按文件聚合。已落地：`internal/job/workflow`（链编排引擎，design §13）——`job` 经 `WorkflowAdvancer` 接口反向回调（job 不 import workflow），`workflow.Engine` 经 `JobOps` 接口取宿主能力；共享类型（`RetryPolicy` 等 `JobRequest` 字段类型）留 `job`。新子域抽取沿用此「双接口依赖倒置」模式。

### 公共 util（CodeQL 基线，2026-09-20）

- **G041 分配容量提示走 `util.CapSum/CapMul`**：`make([]T, 0, n)` / `make(map[K]V, n)` 的容量提示一律经 `internal/util`（`CapSum(len(a), len(b))`、`CapMul(len(m), 2)`），**不要**在 `make` 里写 `len(a)+len(b)`、`len(m)*2` 这类算式——GitHub CodeQL `go/allocation-size-overflow` 会报「分配大小计算可能溢出」（回绕成负数后 `make` 直接 panic）。CapSum/CapMul 先把每个入参 clamp 到 `MaxHint`(1<<20) 再在 int64 上运算，任何入参（含 `math.MaxInt`）都不可能回绕；容量只是提示，clamp 最多多一次扩容、不改变可观察行为。
- **G042 env 合并走 `util.Environ/MergeEnv/EnvWith`**：进程 env 与 job env 的合并一律用 `internal/util`——`Environ(extra)`（`cmd.Env` / pty `Spec.Env`；extra 覆盖继承值）、`MergeEnv(base, extra)`（extra 覆盖 base，空 extra 返回 base 不拷贝）、`EnvWith(base, overrides)`（总是新 map，gofer 自有 key 压过调用方 key）。**不要**再各自写 `mergedEnv` / `mergeEnv` 副本（曾有三份 mergedEnv + 两个 map 合并 helper，口径已漂移）。
- **G043 内置 runner 两个拼写只有一个归一化入口**：server 本机内置 runner 有 `local`（canonical：wire / DB `jobs.runner` / `/v1/runners` / Web 一律这个）与 `server`（CLI 对外拼写，`--runner` 默认值）两个拼写，常量在 `internal/config`（`BuiltinLocalRunner` / `BuiltinLocalRunnerAlias`）。**所有输入边界**（`Submit`/`Validate`/`resumeJob`/任务书模板的 runner 默认值、`allowed_runners` 校验）必须经 `config.NormalizeRunnerName`（服务端用 `job.normalizeRunner`，带 declare-wins：`runners:` 里真声明了 `server` 就以声明为准）；**不要**再在某个入口各写一份 switch——曾只有 CLI 翻译 `server`，于是 HTTP/SDK/MCP/`-f` 任务文件/模板一律报 `runner "server" is not allowed in project`，而 `allowed_runners: [server]` 连 `config validate` 都过不去。
- **G044 内置 agent 模板要能自证来源**：`agent.Resolve` 从内置模板注入的 agent 必须带 `injected` 标记（`config.MarkInjectedAgents` → `Registry.Injected()`），列表类读路径（`GET /v1/agents` 的 `injected` 字段、Web Agents 页的「内置」徽标）要把它显示出来。否则 Web 上会出现"config.yaml 里根本没写却在列表里"的 agent（如 `claude-acp`/`omp-acp`），用户无法判断来源。注入是 detect-gated 的（本机 PATH 上装了该 CLI 才注入），操作员显式声明的同名 agent 整条覆盖模板。
