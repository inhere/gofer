# Gofer 项目规则

> 本项目稳定开发约定（避免重蹈）；详情见对应 design/plan，本处不赘述。

## 项目规则

### 配置与路径

- **G001 单机部署收敛**：一台机一个 `serve`，项目映射收敛全局 `~/.config/gofer/config.yaml`（`GOFER_CONFIG` 锁定、`project add` 默认写全局）；项目目录可放 `.gofer.project.yaml` 瘦配置（仅偏好，**无 server/storage、准入字段留全局**）。详见 `docs/design/2026-06-22-config-simplification-design.md`。
- **G002 执行路径视角**：gofer 进程侧一切路径（执行 cwd / 读 overlay / 扫描）统一走 `Config.ExecPath(proj)`，由 `server.path_view: host|container`（默认 host）决定取 `host_path` 或 `container_path`；**不做容器自检**。主机侧动作（编辑器打开等）恒用 `host_path`。
- **G003 配置目录 ENV**：用户级配置目录环境变量是 `GOFER_CONFIG_DIR`（常量 `config.EnvConfigDir`）；代码引用常量、勿写死字面量。

### CLI

- **G011 `-c` 统一绑定**：`-c/--config` 经公共 `bindConfigFlag(c)` 绑 `config.InputCfgFile`，命令前后均可放；新增子命令一律调它、**勿各自重复绑定 `-c`**。worker 配置独立用 `--worker-config`（worker.yaml，语义不同，不走 app `-c`）。
- **G012 gcli 行为坑**：gcli 无 App 级 PersistentFlags——app 级 flag 只在命令名**之前**消费、不下放子命令；要"命令后也可用"须命令级绑定（即 G011 的 helper）。shell 补全是 gcli 内置 `--gen-completion`（**非** `completion` 子命令）。

### 代码分层（重构后基线，B 组 2026-06-25）

- **G021 入口只做绑定/校验/转发**：`commands`/`httpapi`/`mcpserver` 三入口层不放业务/编排逻辑；编排放 `internal/core`(组装)/`serve`(进程编排)/`streaming`(流式)，业务放 `internal/job` 等。非命令入口（组装、健康探针等）**不放 `commands`**。
- **G022 依赖单向、防环**：入口 → 编排(core/serve/streaming) → job → 数据层(jobstore/project/agent/runner/store/config…)；底层/业务层**绝不**反向 import 入口/编排层。新增包后以 `go build`/`go vet`/`go list -deps` 验环。详见 `docs/design/2026-06-25-code-layering-refactor-design.md`。
- **G023 重构铁律**：搬迁/拆分代码**零行为变化**（函数体逐字，仅改包/导出性/import），每步全量 `go test ./...` 绿背书；专属测试随逻辑迁移、覆盖不降。
- **G024 子域升包判据 + 依赖倒置**：拆文件改善阅读、**升包改善边界**；一个子域满足「域自洽 + 反向 seam 够窄 + 正向可接口化 + 收益>代价」(D-B8) 才升为子包，否则留包内按文件聚合。已落地：`internal/job/workflow`（链编排引擎，design §13）——`job` 经 `WorkflowAdvancer` 接口反向回调（job 不 import workflow），`workflow.Engine` 经 `JobOps` 接口取宿主能力；共享类型（`RetryPolicy` 等 `JobRequest` 字段类型）留 `job`。新子域抽取沿用此「双接口依赖倒置」模式。
