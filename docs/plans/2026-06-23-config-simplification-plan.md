# Gofer 配置简化实施计划（全局单 server + 项目瘦配置）

> 对应 design [`../design/2026-06-22-config-simplification-design.md`](../design/2026-06-22-config-simplification-design.md)（D1–D9 + §12 定稿）。本文是**实施计划**：阶段总纲 + 进度跟进 + 每阶段细化任务（含关键代码片段与验收，SR1105）。
> 规模约 5–8 文件、全 additive 向后兼容（D9），单文件 plan（不拆子目录）。

## 1. 总纲

| 阶段 | 目标 | 依赖 | 工作量 |
|---|---|---|---|
| **P0** | Phase1 集中式落地：README 推荐部署 + `init --global` 引导 + example 注释 | — | 轻 |
| **P1** | overlay 核心（config 包）：`ProjectOverlay` + `MergeProjectConfig` + `ApplyProjectOverlays` + 单测 | — | 中 |
| **P2** | serve/mcp 合并接入 + 校验扩展（`default_agent ∈ allowed_agents`）+ 冒烟 | P1 | 中 |
| **P3** | CLI cwd→project 自动推断 + `config show --project`（合并后有效配置）+ 测试 | P1 | 中 |

> P1 是地基（纯 config 包、无副作用）；P2/P3 都挂在 P1 之上，可并行。P0 独立、随时可做。

## 2. 关键设计落点（design → 代码）

| 改动 | 落点 | design 决策 |
|---|---|---|
| 瘦配置结构 + 合并 + 应用 | 新增 `internal/config/overlay.go` | D1/D5/D8 |
| serve 启动合并 | `runServe` `serve.go:52` 后、`buildCore` `assemble.go:52` 前 | D6 |
| reload 合并 | `Core.Reload` `assemble.go:157` 后 | D4/D6 fail-safe |
| mcp 合并 | `runMcp` `mcp.go:46` 后、`buildCore` `mcp.go:50` 前 | D6（standalone/HTTP-client 都生效）|
| 校验扩展 | `Registry.Validate` `registry.go:167` | D5 防绕过准入 |
| cwd 推断 | `runJobRun` `job.go:244`（`-p` 为空时）| D7 |
| config show | `NewConfigCmd` `config.go:115` 加 `show` 子命令 | §12.2 |
| init 全局引导 | `runInit` `config.go:76` 加 `--global` | Phase1 顺手 |

## 3. 前置检查（plan-checking, SR1430.2）

- [ ] 工具链：`go build ./... && go vet ./...` 绿（基线）。
- [ ] 基线测试绿：`go test ./internal/config/... ./internal/commands/... ./internal/project/...`。
- [ ] 无外部依赖（纯配置/代码改动，不涉 DB/MQ/Redis）。
- [ ] 确认 YAML 库：`github.com/goccy/go-yaml`（与 `loader.go:9` 一致），overlay 解码沿用同库。

---

## P0：Phase1 集中式落地（文档 + init 全局引导）

**目标**：让"全局单 server"成为**推荐默认**，并消除 `init` 默认写 `./.gofer.yaml` 助长的反模式。

### T0.1 README 推荐部署章节

`README.md` 增 "推荐部署：单机全局单 server" 段（放在 MCP 接入附近）：

```md
## 推荐部署（单机）

一台机器只起一个 server，项目映射收敛到全局单文件：

    export GOFER_CONFIG=~/.config/gofer/config.yaml   # 写进 shell profile
    gofer init server --global                         # 生成全局骨架
    # 编辑：填 server.token_env / agents / runners
    gofer project add siv --host-path /abs/SIV --container-path /work/SIV
    gofer serve                                        # 一个进程
    # 任意目录:
    gofer job run -p siv -a claude "..."               # CLI 连 serve

GOFER_CONFIG 优先于当前目录的 .gofer.yaml，任意目录命令都走全局。
项目专属偏好放项目目录 .gofer.project.yaml（见"项目瘦配置"）。
```

### T0.2 `init --global` flag（`config.go`）

`initOpts` 加 `global bool`；`NewInitCmd` 注册；`runInit` 解析：当 `--global` 且 server target 且未显式 `--config` 时，路径取 `config.UserConfigPath()` 并提示设 `GOFER_CONFIG`。

```go
// initOpts (config.go:22) 增 field
var initOpts = struct {
	config string
	force  bool
	global bool
}{}

// NewInitCmd Config (config.go:64) 增
c.BoolOpt(&initOpts.global, "global", "g", false, "write to the user-global config dir (~/.config/gofer/config.yaml)")

// runInit (config.go:86) 路径解析改为
path := initOpts.config
if path == "" {
	if initOpts.global && (target == "" || target == "server") {
		gp, err := config.UserConfigPath()        // loader.go:113
		if err != nil {
			return errorx.Failf(configExitErr, "resolve global config path: %v", err)
		}
		path = gp
		_ = os.MkdirAll(filepath.Dir(path), 0o755) // 确保 ~/.config/gofer 存在
	} else {
		path = defaultPath
	}
}
```

成功提示（global 分支）补一行：`提示: export GOFER_CONFIG=<path> 后任意目录可用`。

> `--global` 仅对 server target 生效（worker 无全局发现，D 保留 `worker.yaml`）。`--config` 显式路径仍最高优先，向后兼容。

### T0.3 example 注释更新（`config/gofer.example.yaml`）

- `projects:` 段示例改为"只写 `host_path`/`container_path` + 准入 `allowed_agents`，其余留空走默认"。
- 顶部注释加：推荐放 `~/.config/gofer/config.yaml`；项目偏好走 `.gofer.project.yaml`（P1/P2 落地后）。

### P0 验收

- [ ] `go build ./... && go vet ./...` 绿。
- [ ] `gofer init server --global -f` 写入 `~/.config/gofer/config.yaml` 并打印 GOFER_CONFIG 提示；`config_test.go` 加用例（global 路径 = UserConfigPath）。
- [ ] `gofer init worker --global` 仍写 `worker.yaml`（global 对 worker 无效，不破坏）。
- [ ] README diff review；example 经 `config validate` 仍解析通过（既有 example-parse 测试不回归）。

---

## P1：overlay 核心（config 包）

**目标**：纯 config 包内实现瘦配置的结构 / 合并 / 应用，零副作用、可单测。

### T1.1 新增 `internal/config/overlay.go`

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"

	yaml "github.com/goccy/go-yaml"
)

// ProjectOverlayName is the per-project thin config file (E29/D1). It lives in
// the project dir and carries ONLY preference fields — server/storage/host_path/
// container_path 与准入字段 (allowed_agents/allowed_runners/allow_exec) 一律不在此
// (D2: 准入真源在全局 config, overlay 不得放权).
const ProjectOverlayName = ".gofer.project.yaml"

// ProjectOverlay is the decoded .gofer.project.yaml (D5 白名单). Every field is a
// pointer so "absent" (nil) is distinguishable from a zero value — only non-nil
// fields override the global ProjectConfig (D8).
type ProjectOverlay struct {
	ExchangeSubdir    *string `yaml:"exchange_subdir"`
	ResultSubdir      *string `yaml:"result_subdir"`
	DefaultAgent      *string `yaml:"default_agent"`
	MaxConcurrentJobs *int    `yaml:"max_concurrent_jobs"`
	CaptureDiff       *bool   `yaml:"capture_diff"`
	NotifyEnabled     *bool   `yaml:"notify_enabled"`
}

// forbiddenOverlayKeys are top-level keys that must NOT appear in an overlay
// (D2/D5). Presence is a config mistake → warn (not fatal): a project author
// cannot self-grant 准入 nor redefine the注册锚/server.
var forbiddenOverlayKeys = []string{
	"server", "storage", "projects", "agents", "runners",
	"host_path", "container_path",
	"allowed_agents", "allowed_runners", "allow_exec",
}

// MergeProjectConfig returns base with every non-nil overlay field applied (D8).
// Slice/准入/注册锚 fields are never touched (they are absent from ProjectOverlay).
func MergeProjectConfig(base ProjectConfig, ov ProjectOverlay) ProjectConfig {
	if ov.ExchangeSubdir != nil {
		base.ExchangeSubdir = *ov.ExchangeSubdir
	}
	if ov.ResultSubdir != nil {
		base.ResultSubdir = *ov.ResultSubdir
	}
	if ov.DefaultAgent != nil {
		base.DefaultAgent = *ov.DefaultAgent
	}
	if ov.MaxConcurrentJobs != nil {
		base.MaxConcurrentJobs = *ov.MaxConcurrentJobs
	}
	if ov.CaptureDiff != nil {
		base.CaptureDiff = ov.CaptureDiff
	}
	if ov.NotifyEnabled != nil {
		base.NotifyEnabled = ov.NotifyEnabled
	}
	return base
}

// ApplyProjectOverlays merges each project's .gofer.project.yaml into
// cfg.Projects IN PLACE (D6). Read dir = ContainerPath || HostPath (D4):
// gofer runs in-container, so the container path is read first. A missing file
// is skipped silently; a decode error or forbidden key appends a warning but
// never aborts (one bad project overlay must not take down serve). Returns the
// warnings for the caller to log.
func ApplyProjectOverlays(cfg *Config) []string {
	var warns []string
	for key, p := range cfg.Projects {
		dir := p.ContainerPath
		if dir == "" {
			dir = p.HostPath
		}
		if dir == "" {
			continue
		}
		path := filepath.Join(dir, ProjectOverlayName)
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				warns = append(warns, fmt.Sprintf("project %q: read overlay %s: %v (using global)", key, path, err))
			}
			continue // 不存在 → 纯走全局定义 (D9 向后兼容)
		}
		warns = append(warns, detectForbiddenOverlayKeys(key, data)...)
		var ov ProjectOverlay
		if err := yaml.Unmarshal(data, &ov); err != nil {
			warns = append(warns, fmt.Sprintf("project %q: decode overlay %s: %v (skipped)", key, path, err))
			continue
		}
		cfg.Projects[key] = MergeProjectConfig(p, ov)
	}
	return warns
}

// detectForbiddenOverlayKeys decodes the overlay loosely and warns on any
// forbidden top-level key (D2/D5). The forbidden key is otherwise ignored
// (ProjectOverlay has no field for it), so this only surfaces the mistake.
func detectForbiddenOverlayKeys(key string, data []byte) []string {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil // 真正的解码错误由调用方的 strict 解码报告
	}
	var warns []string
	for _, k := range forbiddenOverlayKeys {
		if _, ok := raw[k]; ok {
			warns = append(warns, fmt.Sprintf("project %q: overlay key %q is not allowed (准入/server 留全局, D2) — ignored", key, k))
		}
	}
	return warns
}
```

### T1.2 单测 `internal/config/overlay_test.go`

覆盖：
- 合并语义：每个白名单字段 nil→不变 / 非 nil→覆盖（含 `*bool` 的 false 显式覆盖）。
- `ApplyProjectOverlays`：临时目录写 `.gofer.project.yaml` → 合并入 `cfg.Projects[key]`；缺文件→不变；坏 YAML→warn + 跳过（该项目仍是全局值）；forbidden key（如 `allowed_agents`）→warn 且**未**改 `AllowedAgents`。
- `ContainerPath` 优先于 `HostPath`（D4）：两者都设、只在 container 目录放 overlay → 命中。

### P1 验收

- [ ] `go test ./internal/config/...` 绿（含新 overlay_test）。
- [ ] `go vet ./internal/config/...` 绿。
- [ ] 合并/forbidden/坏文件三类用例均 PASS；`AllowExec`/`AllowedAgents` 在任何 overlay 下都不被改（D2 守卫）。

---

## P2：serve/mcp 合并接入 + 校验扩展

**目标**：让 serve、mcp 在加载/重载路径合并 overlay；校验堵住"`default_agent` 绕过准入"。

### T2.1 serve 接入（`serve.go`）

`runServe`（`serve.go:52` `config.Load` 后、`buildCore` `serve.go:80` 前）插入：

```go
for _, w := range config.ApplyProjectOverlays(cfg) {
	c.Printf("gofer: overlay warn: %s\n", w)
}
```

### T2.2 reload 接入（`assemble.go`）

`Core.Reload`（`assemble.go:157` `config.Load` 后、swap 前）：

```go
newCfg, _, err := config.Load(path)
if err != nil {
	return fmt.Errorf("reload config: %w", err)
}
config.ApplyProjectOverlays(newCfg) // D6: reload 同样合并; warns 在 reload 日志已足够, 此处可丢弃或经 c 打印
c.Cfg = newCfg
c.Projects.Reload(newCfg)
...
```

> fail-safe 不变：`config.Load` 失败仍保留旧 cfg（`assemble.go:158`）；overlay 解析失败只 warn 不阻断（T1.1 语义），不会让 reload 失败。

### T2.3 mcp 接入（`mcp.go`）

`runMcp`（`mcp.go:46` `config.Load` 后、`buildCore` `mcp.go:50` 前）插入同 T2.1 合并（standalone 与未来 HTTP-client 模式都生效）。MCP stdout 是协议通道，warn **必须走 stderr**：

```go
for _, w := range config.ApplyProjectOverlays(cfg) {
	fmt.Fprintf(os.Stderr, "gofer mcp: overlay warn: %s\n", w)
}
```

### T2.4 校验扩展（`registry.go` `Validate`）

`Registry.Validate`（`registry.go:167` `default_agent` 检查处）补：当 `proj.AllowedAgents` 非空且 `proj.DefaultAgent` 非空时，`default_agent` 必须 ∈ `allowed_agents`，否则 FAIL（D5 防 overlay 借 default_agent 绕过准入）：

```go
if proj.DefaultAgent != "" {
	if !agentDefined(cfg, proj.DefaultAgent) {
		add("default_agent", false, fmt.Sprintf("agent %q not defined", proj.DefaultAgent))
	} else if len(proj.AllowedAgents) > 0 && !contains(proj.AllowedAgents, proj.DefaultAgent) {
		add("default_agent", false, fmt.Sprintf("agent %q not in allowed_agents %v (D2)", proj.DefaultAgent, proj.AllowedAgents))
	} else {
		add("default_agent", true, proj.DefaultAgent)
	}
}
```

（`contains` 为小工具；若包内已有等价 helper 则复用。）

### P2 验收

- [ ] `go test ./internal/commands/... ./internal/project/...` 绿（含 reload_test 不回归 + 新 default_agent 越权用例 FAIL）。
- [ ] **冒烟**（design §9.1 路径）：临时全局 config（含 project siv，`allowed_agents:[claude]`）+ 项目目录 `.gofer.project.yaml`（`default_agent: claude, result_subdir: out`）→ `gofer serve` 启动日志无 overlay warn；`config validate` 显示 `result_dir` 落在 `out`。
- [ ] 越权用例：overlay 写 `default_agent: codex`（不在 allowed）→ `config validate` 报 FAIL（D5）。
- [ ] SIGHUP 重载：改 overlay 的 `result_subdir` → `kill -HUP` → 新 job 的 result_dir 变化（手动冒烟）。

---

## P3：CLI cwd 自动推断 + config show

**目标**：项目目录里 `job run` 免写 `-p`；提供合并后有效配置的诊断命令。

### T3.1 cwd→project 反查（`job.go`）

新增 helper（`job.go`，紧邻 `newClient`）：

```go
// resolveProjectByCwd returns the project key whose host_path or container_path
// is the longest prefix of absCwd, plus cwd relative to that root (D7). It only
// uses the注册锚 (host/container path), never the overlay. ok=false on zero match
// or a tie (两个项目同长前缀 → 让用户显式 -p, 避免误派).
func resolveProjectByCwd(cfg *config.Config, absCwd string) (key, relCwd string, ok bool) {
	bestLen := -1
	tie := false
	for k, p := range cfg.Projects {
		for _, root := range []string{p.ContainerPath, p.HostPath} {
			if root == "" {
				continue
			}
			abs, err := filepath.Abs(root)
			if err != nil {
				continue
			}
			if absCwd == abs || strings.HasPrefix(absCwd, abs+string(filepath.Separator)) {
				if len(abs) > bestLen {
					bestLen, key, tie = len(abs), k, false
					if rel, e := filepath.Rel(abs, absCwd); e == nil {
						relCwd = rel
					}
				} else if len(abs) == bestLen && k != key {
					tie = true
				}
			}
		}
	}
	if bestLen < 0 || tie {
		return "", "", false
	}
	if relCwd == "" {
		relCwd = "."
	}
	return key, relCwd, true
}
```

### T3.2 `runJobRun` 接入（`job.go:244`）

在 `newClient` 之前，`jobRunOpts.project == ""` 时尝试推断：

```go
if jobRunOpts.project == "" {
	if cfg, _, err := config.Load(jobRunOpts.config); err == nil {
		if abs, e := filepath.Abs("."); e == nil {
			if key, rel, ok := resolveProjectByCwd(cfg, abs); ok {
				jobRunOpts.project = key
				if jobRunOpts.cwd == "" || jobRunOpts.cwd == "." {
					jobRunOpts.cwd = rel
				}
				c.Printf("auto-detected project %q (cwd=%s)\n", key, rel)
			}
		}
	}
}
```

> 仍为空则下游 `--project/-p is required`（`job.go:302`）报错——行为对未注册目录不变（D9）。`-p` 显式给出时跳过推断（跨项目提交不误判）。

### T3.3 `config show --project`（`config.go`）

`NewConfigCmd` `Subs`（`config.go:119`）加 `show` 子命令：load 配置 → `ApplyProjectOverlays` → 打印合并后 `ProjectConfig`（含 `ResolvedExchangeSubdir`/`ResolvedResultSubdir` 的有效值）。

```go
{
	Name: "show",
	Desc: "Show the effective (overlay-merged) config of a project",
	Config: func(c *gcli.Command) {
		c.AddArg("key", "project key", true)
		c.StrOpt(&configShowOpts.config, "config", "c", "", "path to the config file")
	},
	Func: runConfigShow, // config.Load → ApplyProjectOverlays(cfg) → 取 cfg.Projects[key] → 打印
}
```

> **诊断用途 ≠ 裁决**：`config show` 本地读 overlay 仅为给 operator 看合并结果，不参与任何 job 准入，与 D2 不冲突；它需在 serve 同机/容器内运行才能读到 overlay（否则只显示全局值，并提示）。正式真源诊断（连 serve 取有效配置）可后续加 `GET /v1/projects/{key}`，本轮不做。

### P3 验收

- [ ] `go test ./internal/commands/...` 绿（含 `resolveProjectByCwd` 单测：唯一命中 / 最长前缀 / tie→false / 无命中→false / rel 计算）。
- [ ] **冒烟**：在已注册项目目录 `cd /work/SIV && gofer job run -a claude "x"` → 自动识别 `-p siv`、cwd 相对化；`cd /tmp && gofer job run -a claude "x"` → 报 `--project required`。
- [ ] **冒烟**：`gofer config show --project siv` 打印合并后字段（overlay 的 `result_subdir` 生效、`allowed_agents` 取全局）。

---

## 4. 进度跟进

- [ ] **P0** 文档 + init 全局引导（README / `config.go` / example）
- [ ] **P1** overlay 核心 + 单测（`internal/config/overlay.go` + `_test.go`）
- [ ] **P2** serve/mcp 合并接入 + 校验扩展 + 冒烟（`serve.go`/`assemble.go`/`mcp.go`/`registry.go`）
- [ ] **P3** cwd 推断 + config show + 测试（`job.go`/`config.go`）

> SR1202：每个子阶段完成即提交 Git；最终按会话完成协议 push。

## 5. 完成判定

- 四阶段验收全 PASS；`go build/vet ./...` + 全量相关包 `go test` 绿。
- 端到端冒烟：全局单 config + `GOFER_CONFIG` + 一个 serve + `project add` 写全局 + 任意目录 `job run`（自动识别项目）+ `.gofer.project.yaml` 偏好生效 + `config show` 可见 + 越权 `default_agent` 被校验拦截。
- D9 向后兼容：无 overlay 的项目、现有"每项目全套 `.gofer.yaml`"用法均不回归。
- README/example/design 同步；roadmap E29 回填实施结果。

## 6. 实施结果（完成后回填）

> P0/P1/P2/P3 commit 短码 + 关键决策 + 验收记录 + 遗留。
