# Gofer CLI 体验优化实施计划（A 组 · 轻 plan）

> 来源：TODO §体验优化 5 项（使用中发现）。轻 plan：3 阶段，每阶段独立 commit。
> **前置**：① 你已起头 app 级全局 `-c`（**未提交**：`internal/config/config.go` 新增 `var InputCfgFile`、`app.go:19` 绑 app flag 默认 `${GOFER_CONFIG}`、`config.go` 把 init 的 `-c→-o` 让位）——P1 在此基础续做；② gofer 已建 codebase 索引（`d-work-...-tools-gofer`）。

## 1. 总纲

| 阶段 | 目标 | 工作量 |
|---|---|---|
| **P1** | `-c` 收敛 app 级：删 ~26 处子命令重复绑定 + `config.Load` 改读 `config.InputCfgFile` + worker 特殊处理 | 中（机械多点）|
| **P2** | config/serve 体验：`serve` 打印配置路径 + `config edit` + `config info` | 小 |
| **P3** | `project add -i` 交互式把当前目录加为项目 | 小 |

## 2. 关键落点

| 改动 | 落点 |
|---|---|
| app 级 -c（已起头）| `app.go:19` → `config.InputCfgFile`（不动）|
| 删子命令重复 -c | serve.go:42 / mcp.go:41 / job.go(×7) / config.go(validate/show) / project.go(×5) / workflow.go(×6) / agent.go(×3) |
| config.Load 改源 | 各 `config.Load(xxxOpts.config)` / `loadRegistry(...)` / `newClient(xxxOpts.config,...)` → `config.InputCfgFile` |
| worker 特殊 | `worker.go:38`（worker.yaml，独立语义）|
| 新子命令 | config edit / config info（`NewConfigCmd` Subs，config.go）|
| 新 flag | project add `-i`（project.go）|
| serve 打印 | `runServe`（serve.go:52，`config.Load` 第二返回值 path）|

## 3. 前置检查

- [ ] `go build ./... && go vet ./...` 绿（注意你的未提交改动已在工作区，先确认能编译）。
- [ ] `go test ./internal/commands/... ./internal/config/... ./internal/project/...` 基线绿。
- [ ] **验 gcli v3.8 行为**（决定 worker -c 处理）：app 级 flag 与子命令同名 flag 的交互——子命令能否覆盖 app flag / 是否报重复注册。一个最小冒烟即可。

## 4. 进度跟进

- [x] **P1** `-c` 收敛 app 级（含 worker 特殊）— worker 用 `--worker-config`；删 26 处子命令 `-c`
- [x] **P2** serve 打印路径 + config edit + config info
- [x] **P3** project add -i

---

## P1：`-c` 收敛到 app 级

### T1.1 删各子命令重复 -c + 改 config.Load 源

- 删除 §2 列出的 ~26 处 `c.StrOpt(&xxxOpts.config, "config", "c", ...)`。
- 各加载点改读全局：`config.Load(config.InputCfgFile)`、`loadRegistry(config.InputCfgFile)`、`newClient(config.InputCfgFile, ...)`（job/workflow 的 server/token flag 保留，仅 config 源改）。
- 删各 opt struct 的 `config` 字段（调用点直接用 `config.InputCfgFile`）；保留 server/token/其它 flag。

### T1.2 worker 特殊处理（决策 D-A1）

`worker.go:38` 的 `-c` 是 **worker.yaml**（与 gofer config 完全不同语义），**不能**并入 app 级 `-c`。
- 先按前置检查验 gcli：若子命令同名 flag 能覆盖 app flag → worker 保留独立 `-c`（绑 `workerOpts.config`，不读 InputCfgFile）。
- 若冲突/不可覆盖 → worker 改用 **`--worker-config`**（无 `-c` 短名），`init worker` 提示 + README 同步。
- **默认倾向 `--worker-config`**（明确无歧义，避免 `-c` 一词两义）；按 gcli 验证结果定稿。

### T1.3 行为一致性（决策 D-A2，必须回归验证）

`config.InputCfgFile` 默认 `${GOFER_CONFIG}`（gcli 展开）：
- `GOFER_CONFIG` 已设 → InputCfgFile=其值 → `config.Load` 用它。
- 未设 → `${GOFER_CONFIG}` 展开为空 → `config.Load("")` → `Resolve` 链（cwd `.gofer.yaml` → 全局 `~/.config/gofer/config.yaml`）。
- 与现状等价（原 `config.Load("")` 也走 Resolve 查 env）。**验证**：未设 GOFER_CONFIG 时 cwd 的 `.gofer.yaml` 仍被发现；设了走指定。

### P1 验收

- [x] `go build ./... && go vet ./...` 绿；`go test ./internal/commands/... ./internal/config/... ./internal/project/...` 绿（已调整受影响测试）。
- [x] `gofer -c X <subcmd>`（app 级 flag 须在子命令**之前**）/ `gofer job run -p p -a a -- ...` 都用 X。**注意 gcli 限制**：`gofer serve -c X`（`-c` 在子命令**之后**）现报 "option not defined: -c"——app 级 flag 由 `app.fs.Parse` 在命令名前消费，不下放给子命令 flagset（见下「关键决策」）。
- [x] 不给 `-c`：GOFER_CONFIG 设→用其值；未设→cwd/全局发现（**D-A2 回归**已冒烟验证：cwd `.gofer.yaml` 仍被发现）。
- [x] worker 用 `--worker-config` 加载 worker.yaml；`init -o` 仍写出（已对齐 README/提示）。

---

## P2：serve 打印配置路径 + config edit + config info

### T2.1 serve 打印加载的配置路径

`runServe`（serve.go:52）`cfg, path, err := config.Load(...)`（现 path 被忽略 `_`），启动日志加：
```go
if path == "" {
    c.Printf("gofer: config: (none — defaults + discovery)\n")
} else {
    c.Printf("gofer: config: %s\n", path)
}
```

### T2.2 `config edit`

`NewConfigCmd` 加 `edit` 子命令：`config.Resolve(config.InputCfgFile)` 取路径（空→提示先 init）→ 依次试 `$VISUAL` / `$EDITOR` / `code` / `vim` / `nano`（`exec.LookPath` 探可用）→ `exec.Command(editor, path)` 接管 tty。无可用→报错列出尝试过的。

### T2.3 `config info`

加 `info` 子命令：`config.Load` 后打印——
- **配置路径**：resolved path（同 T2.1）。
- **关键 ENV**：`GOFER_CONFIG` / `GOFER_CONFIG_DIR` 值；`GOFER_TOKEN` 是否设（**不显值**，SR403）。
- **关键设置**：`server.addr` / `path_view`（默认 host）/ `web_enabled` / `db_path`（`ResolveDBPath`）/ projects·agents·runners 数。

### P2 验收

- [x] `go build/vet` 绿；新子命令 `config_test.go` 注册用例（`config edit`/`info` 存在，含 app.GetCommand 形式）。
- [x] 冒烟：`serve` 启动打印 `gofer: config: <path>`；`config info` 输出路径+ENV+设置（token 仅显 set=yes/no，不显值，已验未泄漏）；`config edit` 设 `EDITOR=true` 秒退、不报错；无配置时报错提示先 `gofer init`。

---

## P3：`project add -i` 交互式添加当前目录

### T3.1 project add `--interactive/-i`

`project.go` `projectAddOpts` 加 `interactive bool`；`NewProjectCmd` add 子命令注册 `-i`；`runProjectAdd`：当 `-i` 时走交互（gcli interact 输入或 `bufio` stdin 提示）：
- **key**：默认 = cwd 目录名（`filepath.Base(cwd)`），可改。
- **host_path**：cwd 绝对路径（自动，确认）。
- 提示 container_path（可空）/ default_agent（可空）/ allowed_agents（逗号分隔，可空走默认）。
- `registry.Add(key, proj, force)` 写入（默认全局，`registry.go:98`）。

### P3 验收

- [x] `go build/vet/test` 绿。
- [x] 冒烟：`cd <临时项目> && printf '\n\n\n\n' | gofer p add -i`（喂回车走默认，临时 GOFER_CONFIG_DIR 隔离）→ 全局 config 出现该项目（key=目录名、host_path=cwd）；`project show <key>` 确认；真实全局 config 未污染。非交互（无 -i）报错语义不变（缺 key / 缺 --host-path）。

---

## 5. 完成判定

- 三阶段验收 PASS；`go build/vet ./...` + 相关包 `go test` 绿。
- CLI：`-c` 统一 app 级（worker 独立）+ `serve` 打印路径 + `config edit`/`info` + `p add -i` 可用；`-c` 未给时的发现链回归不变（D-A2）。
- 各阶段独立 commit（P1 含你已起头的 app -c 改动一并提交）；前端无关。

## 6. 实施结果（完成后回填）

> P1–P3 commit + 关键决策（尤 worker -c / gcli 验证结论）+ 验收 + 遗留。

### P1（done）

- **改动**：删 26 处子命令重复 `-c` 绑定（serve/mcp/job×7/config validate·show/project×5/workflow×6/agent×3），各加载点统一读 `config.InputCfgFile`（newClient/loadRegistry/loadAgentRegistry/config.Load）。app 级 `-c`（默认 `${GOFER_CONFIG}`）保留在 `app.go:19`；`init` 让位 `-o`（已改）。
- **worker（D-A1 定稿 = plan 默认 `--worker-config`）**：worker.yaml 与 gofer config 语义不同，worker 子命令改用 `--worker-config`（无 `-c` 短名，避免一词两义），绑 `workerOpts.config`，**不读** `config.InputCfgFile`。`init worker` 提示、`loadWorkerConfig` 报错、README 已同步。
- **D-A2 回归**：已冒烟。未给 `-c` 且未设 `GOFER_CONFIG` → `config.InputCfgFile=""`（gcli 展开空 `${GOFER_CONFIG}`）→ `Resolve("")` 走发现链（cwd `.gofer[.local].yaml` → 全局），cwd `.gofer.yaml` 仍被发现；设 `GOFER_CONFIG` 走其值；`gofer -c X` 走 X。三态均 PASS。
- **gcli 关键结论（影响 UX，需知会）**：app 级 flag 由 `App.doParseOpts → app.fs.Parse(args)` 在**命令名之前**消费，**不下放**给子命令 flagset（gcli 无 App 级 PersistentFlags，只有 `Command.SharedOpts()` 是命令级 PersistentFlags）。后果：`gofer -c X serve` ✅ 工作，但 `gofer serve -c X`（`-c` 在子命令后）❌ 报 "option provided but not defined: -c"——这是从「每子命令各有 `-c`」收敛到「app 级单一 `-c`」的**位置回归**（位置从"可前可后"变为"仅前"）。已据此修正 README 全部示例（`-c` 置于子命令前；init→`-o`；worker→`--worker-config`；mcp 配置 args 改 `["-c", path, "mcp"]`）。
- **测试**：`config.InputCfgFile` 是包级全局，受影响测试（agent/project/config_test）改设该全局并 `t.Cleanup` 重置隔离。`go build/vet ./...` + 三包 test + 全量 test 全绿；`go test -count=1` 无串扰。
- **遗留**：若要恢复"`-c` 可置于子命令后"的旧体验，需为每个子命令显式加 `SharedOpts()` 级 `-c`（命令级 PersistentFlags 会被下放），或在各子命令 flagset 重新声明并合并 InputCfgFile——本轮按 plan 收敛为 app 级单一 `-c`，未做该回填（如需可作 P1.5 跟进）。

### P2（done · commit 6920091）

- **改动**：`serve.go` `runServe` 改 `cfg, cfgPath, err := config.Load(...)`，启动即打印 `gofer: config: <path>`（path=="" 显 `(none — defaults + discovery)`）。`config.go` 新增 `config edit` / `config info` 两子命令 + `os/exec` 引入。
- **config edit**：`config.Resolve(InputCfgFile)` 取路径（不 decode——配置坏掉时正需编辑）；空→报错提示先 `gofer init`。探测顺序 `$VISUAL → $EDITOR → code → vim → nano`，首个 `exec.LookPath` 命中即 `exec.Command(ed, path)` 接管 tty；都无→报错列出尝试过的。
- **config info**：`config.Load` 后打印 配置路径 / ENV(`GOFER_CONFIG`、`GOFER_CONFIG_DIR` 值 + `GOFER_TOKEN` 仅 set=yes/no) / 设置(`server.addr`、`path_view`(空显 host)、`web_enabled`、`db_path`、projects·agents·runners 计数)。**SR403**：token 仅显是否设、不显值（冒烟 grep 确认未泄漏）。
- **测试**：`config_test.go` 扩 `TestConfigCmdRegistered` 覆盖 edit/info；新增 `TestConfigSubsRegisteredViaApp`（`app.GetCommand("config").GetCommand("edit"|"info")` 非 nil）。
- **ENV 常量**：统一用真实常量 `GOFER_CONFIG_DIR`（`config.EnvConfigDir`，`loader.go:18`）；plan 早期正文误写的 `GOFER_CFG_DIR` 已订正（2026-06-25 文档检查）。

### P3（done · commit 61027bb）

- **改动**：`project.go` `projectAddOpts` 加 `interactive bool`；add 子命令注册 `-i/--interactive`，`<key>` arg 改为 gcli 级 optional（`-i` 时改由提示取，默认 cwd 目录名）；非交互路径仍在 runner 内强制 key+host-path（语义不变）。新增 `runProjectAddInteractive`（`bufio.NewScanner(os.Stdin)` 逐行提示，空行接受默认）+ `splitCSV`。
- **交互项**：key(默认 `filepath.Base(cwd)`) / host_path(=cwd 绝对路径，自动展示) / container_path / default_agent / allowed_agents(逗号分隔)。`registry.Add` 写入；path=="" 时 `save()` 落全局 `UserConfigPath()`。
- **测试**：新增 `TestProjectAddInteractive`——`os.Pipe` 喂空行、`t.Setenv(GOFER_CONFIG_DIR, tmp)` 隔离、chdir 临时目录，断言 key=目录名、host_path=cwd 落临时全局 config。
- **偏差**：项目未用 gcli interact，按 plan 备选用 `bufio` stdin 提示（同时兼容交互 tty 与管道喂入）。`<key>` 由 required 改 optional 是为让 `-i` 不传位置参也能跑；非交互缺 key 仍报原文案。
