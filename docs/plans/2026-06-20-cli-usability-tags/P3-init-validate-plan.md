# P3 — E3 引导 / 校验 / example 补全（实施计划）

> 主纲：[`2026-06-20-cli-usability-tags-plan.md`](./2026-06-20-cli-usability-tags-plan.md) · 设计 §5.3。
> `gofer init` 脚手架 + `gofer config validate` 全局校验 + `config/gofer.example.yaml` 补全缺失段。无后端逻辑风险，置末。

---

## P3-a `gofer init` + `gofer config validate`

### 落点
- `config/gofer.example.yaml`：先在 P3-b 补全为"可即用模板"，本步用 `//go:embed` 嵌入它作为 init 模板源（init 与 example 同一份，避免漂移）。
- `internal/commands/`：新增命令。推荐形态——顶层 `gofer init` + `config` 命令组带 `validate` 子命令（`gofer config validate`）。实现可放 `internal/commands/config.go`（新）。
- `internal/commands/app.go`：`app.Add(NewConfigCmd())`（含 init 或单独顶层 init，按 gcli 风格择一，回报说明）。

### 步骤
**1) init**（`gofer init [--config <path>] [--force]`）：
```go
// 默认 path = ./.gofer.yaml（config 加载链优先项，loader.go）。
// 已存在且无 --force → 报错退出（不覆盖用户配置，D6）。
// 写入 embed 的 gofer.example.yaml 内容（注释化 starter）。打印"已生成 <path>，编辑后 gofer config validate"。
```
- 用 `//go:embed ../../config/gofer.example.yaml`（或合适相对路径）嵌入；`os.WriteFile(path, content, 0o644)`，写前 `os.Stat` 判存在。
**2) config validate**（`gofer config validate [--config]`）：
- 复用 `internal/config` 的 `Load`（含 `validate(cfg)` 查 host_path）；再用 `agent`/`project` 的 registry：遍历所有 project 调 `reg.Validate(key)`（路径存在/agent 可用/runner 存在，逻辑同 `project validate <key>`，`commands/project.go:227`），逐条 `[OK/FAIL] <Name> <Info>` 输出。
- 任一 FAIL → 进程非零退出（CI 可用）；全 OK → 打印 "config OK"。
- 复用 `commands/project.go` 的 `loadRegistry` + `Validate` 渲染逻辑，不重造。

### P3-a 验收
- 单测 `internal/commands`：`config validate` 命令注册存在；对一个好配置返回全 OK / 0 退出，对坏配置（host_path 不存在 / 未知 agent）返回 FAIL / 非零。
- 单测：`init` 写出文件后内容 == embed 模板；已存在且无 `--force` → 报错不覆盖；`--force` → 覆盖。
- 真机：`gofer init` 生成 `./.gofer.yaml` → `gofer config validate` 通过。

---

## P3-b `config/gofer.example.yaml` 补全缺失段

### 落点 `config/gofer.example.yaml`
按 recon 缺口补齐（每段带注释说明用途 + 何时需要）：
- `server.callers`：多 caller（C2）示例 `[{id, token_env}]`。
- `server.workers`：ws-worker 注册示例 `[{id?, token_env, labels:[...]}]`（对齐 `WorkerAuthConfig` 字段）。
- `server.runner_probe`：peer-http 健康探针参数示例。
- `storage.retention`：`{max_age_days, max_count}` 示例。
- `storage.db_path`：显式 DB 路径示例（注释默认值）。
- `runners`：加一个 `type: worker` 的 runner 样例（worker_id 等）。
- （可选）`projects[].capture_diff`、`tags` 用法注释（呼应 P3/P1 新增能力）。

> 字段名/结构以 `internal/config/model.go` 的实际 struct（`ServerConfig`/`StorageConfig`/`WorkerAuthConfig`/`RunnerProbeConfig`/`RetentionConfig`/`RunnerConfig`）为准，逐字段对齐，**不臆造字段**。

### P3-b 验收
- example copy 成 `.gofer.yaml` → `gofer config validate` 能 Load 不报解析错（结构合法）；关键段齐全可 `serve`（至少 Load 成功）。
- 单测/校验：把 example 用 `config.Load` 解析不 panic、字段映射正确（可加一个 "example 可解析" 测试）。

### 提交点
P3-a / P3-b 各绿灯分别 `git commit`；更新主纲进度全勾 + 出**完成报告**（SR1430）。

> 范围注记：init 为非交互注释模板（D6），交互式向导 / `project add` 引导留后续；config validate 复用既有 per-project 校验，不新增校验规则。
