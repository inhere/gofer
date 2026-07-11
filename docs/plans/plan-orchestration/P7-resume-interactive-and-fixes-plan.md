# P7 — interactive 会话续接修复 + env 删除按钮 + `serve --web-dir`（实施计划，代码级）

> 主纲：[plan-orchestration-plan.md](./plan-orchestration-plan.md)
> 上游：P4（「继续会话」resume UI）✅ / P5（rebuild + env 编辑器）✅ / P6（派生终态门禁）✅
> 触点均已实测定位（2026-07-10 只读探查）。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-10 | inhere/claude | 初稿：用户重部署 host server 后实测发现的 3 个问题，方向已拍板（问题 2 选 A 完整打通） |

## 一句话

修用户实测发现的三处：① **env 新增行缺删除按钮**（前端小 bug）；② **pty/interactive 会话「继续会话」失败** —— resume 只有非交互模板（`claude … -p {{prompt}}`），对交互会话用错模式、空 prompt 时 `claude --resume <sid> -p ""` 崩（真实 bug，选 A 完整打通）；③ 新增 **`gofer serve --web-dir`** 指向 `web/dist` 便于本地前端调试（不必每次 `make web` 重嵌）。

## 背景（三个问题怎么发现的）

用户重部署 host server 验收 P5/P6 时实测报告：
1. 「快速重建」的 env 编辑器，**新增的 env 行没有删除按钮**（继承行有「删除/恢复」，新增行漏了）。
2. 「继续会话」对一个 **pty 交互会话** 生成命令 `claude --resume fbfb9332-… -p`（`-p` 后空），**失败**。
3. 希望 `gofer serve` 加 `--web-dir` 指向 `web/dist`，本地改前端后免 `make web` 重嵌即可看到。

## 已确认关键事实（探查结论）

| 事实 | 位置（实测 file:line） | 对 P7 的影响 |
|---|---|---|
| env **新增行** v-for 只渲染 key+value 输入，**无删除按钮**；继承行有「删除/恢复」 | `web/src/views/NewJob.vue:644`（envAdds）vs `:640`（envRows 删除按钮） | T1 给新增行加删除按钮 |
| agent 的 `SessionResume` **只有非交互一种模板**：claude `--resume {{session_id}} -p {{prompt}}`（`-p`=print 非交互）；codex `exec resume {{session_id}} {{prompt}}`（`exec`=非交互一次性） | `internal/agent/registry.go:92,99` | T2 加**交互 resume 模板** |
| `Render` 用 `strings.NewReplacer`，空 `{{prompt}}`→空串**参数保留** → argv 出现 `-p ""` | `internal/agent/template.go:42-48` | 非交互 resume 空 prompt = `-p ""` 崩 → T2 非交互源要求非空 prompt |
| `ResumeJob` 的 Submit **不带 `Interactive`/`Cols`/`Rows`**，也不区分源是否交互 | `internal/job/resume.go:74-95`（Submit 参数无 Interactive） | T2 继承源 interactive |
| `JobResult` **有 `Interactive` 字段**（源是否交互可知） | `internal/job/model.go:182` | T2 用 `src.Interactive` 分支 |
| `JobRequest` 有 `Cols`/`Rows`（pty 初始尺寸） | `internal/job/model.go:30-31` | interactive resume 可从 request_json 还原，或交给 pty 默认 + attach 后 resize（**倾向后者，最简**） |
| `config.AgentConfig` 的 session 字段块（`SessionResume []string`） | `internal/config/model.go:573-583` | T2 加 `SessionResumeInteractive []string` 字段 |
| `applySessionDefaults` 逐字段独立填充、显式配置覆盖 | `internal/agent/registry.go:120-135` | T2 交互模板同法兜底填充 |
| 前端 `doResume` resume 后 `router.push('/jobs/{id}')` | `web/src/views/JobDetail.vue:467-481` | T3 interactive 源改 `?attach=1` |
| 前端 **`?attach=1` 自动打开终端已存在** | `web/src/views/JobDetail.vue:567-568`（`route.query.attach==='1'` → `openTerminal()`） | T3 复用，无需新造 attach 逻辑 |
| web 静态服务 `go:embed all:dist`；`webui.Handler()` 用 `fs.Sub(dist,"dist")` → `handlerFor(fsys fs.FS)`（**纯函数，接受任意 FS**） | `internal/webui/embed.go:20-45` | T4 加 `HandlerForDir(dir)` 用 `os.DirFS` 复用 `handlerFor` |
| server 挂载：`if s.webEnabled { h,_ := webui.Handler() … }` | `internal/httpapi/server.go:488-490` | T4 改为 webDir 非空则 `HandlerForDir(webDir)` |
| web 配置 wiring：`serve.Opts.NoWeb` → `serve.go:84-85` `cfg.Server.WebEnabled` → `httpapi.New` 读 `serverCfg.IsWebEnabled()`（`:263`） | `internal/serve/serve.go:50,84-85`；`internal/commands/serve.go:101`；`internal/httpapi/server.go:253,263` | T4 顺链加 `WebDir`：serve 选项 → Opts → `cfg.Server.WebDir` → server 读 |

## 已拍板决策（2026-07-10 用户确认）

- **问题 2 选 A（完整打通）**：
  - agent 加**交互 resume 模板**；`ResumeJob` 若 `src.Interactive` → 新 job 继承 `Interactive: true` + 用交互模板（无 `-p`/`exec`）。
  - 前端 interactive 源「继续会话」→ 点击起 pty resume job → 跳转新 job 并 **`?attach=1` 自动打开终端**，用户在 TUI 里继续；**prompt 输入框对 interactive 源隐藏**（进 TUI 自己打字）。
  - **非交互源**保持 prompt 续投，但 **prompt 改必填**（前端必填 + 后端校验），杜绝 `claude … -p ""` 崩。
- **问题 1 / 问题 3**：方向明确，无取舍，照实现。

## 核心约束

- **复用既有原语**：前端 attach 用既有 `?attach=1`（`JobDetail.vue:567`）；webui 用既有 `handlerFor`（纯函数）；不重造。
- **G023 交互模板兜底不改非交互行为**：`SessionResume`（非交互）保持逐字不变；新增 `SessionResumeInteractive` 仅在 `src.Interactive` 时启用。非交互源 resume 命令逐字不变。
- **G021 入口薄**：模板选择/继承落 `internal/job.ResumeJob`；serve/httpapi 只做配置传递与挂载。
- 本仓是独立通用工具库（`AGENTS.md` G031）：代码/注释/测试禁止业务信息。

---

## 任务分解（T1..T5）

### T1 —（前端）env 新增行加删除按钮

**`web/src/views/NewJob.vue`** 现状 `:644`（envAdds 的 v-for，仅 key+value 输入）。仿继承行 `:640` 的删除按钮，加一个移除该新增行的按钮：
```vue
        <div v-for="(a, i) in envAdds" :key="'add' + i" class="env-row mono">
          <input v-model="a.key" class="env-key mono" placeholder="KEY" />
          <input v-model="a.value" class="env-val mono" placeholder="value" />
          <button type="button" class="env-act mono" @click="envAdds.splice(i, 1)">删除</button>
        </div>
```
> 样式复用继承行的 `.env-act`（若类名不同，以现有删除按钮的类为准）。

**验收**：`pnpm build` 绿；重建页新增一行 env → 出现「删除」→ 点击移除该行；删除后不影响其他行与继承行。

---

### T2 —（后端）agent 交互 resume 模板 + `ResumeJob` 继承 interactive

**T2.1 `config.AgentConfig` 加字段**（`internal/config/model.go:579` 的 `SessionResume` 旁）：
```go
	// SessionResumeInteractive 是 pty/交互会话续接的 argv 模板（区别于非交互 SessionResume）。
	// 交互会话进 TUI 续接，不用非交互的一次性 flag（claude 的 -p / codex 的 exec）。
	// 仅当源 job 是 interactive 时由 ResumeJob 选用；未配置则回退到内置默认。
	SessionResumeInteractive []string `yaml:"session_resume_interactive,omitempty"`
```

**T2.2 内置默认**（`internal/agent/registry.go:92,99` 的 `builtinSessionDefaults`，各 agent 加交互模板）：
```go
	"claude": {
		…
		SessionResume:            []string{"--resume", "{{session_id}}", "-p", "{{prompt}}"},
		SessionResumeInteractive: []string{"--resume", "{{session_id}}"}, // 交互:进 TUI，无 -p
	},
	"codex": {
		…
		SessionResume:            []string{"exec", "resume", "{{session_id}}", "{{prompt}}"},
		SessionResumeInteractive: []string{"resume", "{{session_id}}"}, // ⚠️ 实测确认 codex 交互 resume 命令
	},
```
> ⚠️ **实施时必须实测 codex 交互 resume 的真实命令**（主机上 `codex --help` / `codex resume --help`）。`codex resume <sid>` 是推测；若 codex 无交互 resume 子命令，则该 agent 的 interactive resume 走 fallback（见 T2.3 的降级）。claude 的 `--resume <sid>`（无 `-p`）进 TUI 是确定的。

**T2.3 `applySessionDefaults` 兜底填充**（`registry.go:120-135`，仿 `SessionResume` 加一段）：
```go
	if len(a.SessionResumeInteractive) == 0 {
		a.SessionResumeInteractive = def.SessionResumeInteractive
	}
```

**T2.4 `ResumeJob` 分支 + 继承 interactive**（`internal/job/resume.go`）：
```go
	// 交互源走交互模板（进 TUI，无 -p/exec）；非交互源走 SessionResume。
	tmpl := ac.SessionResume
	if src.Interactive && len(ac.SessionResumeInteractive) > 0 {
		tmpl = ac.SessionResumeInteractive
	}
	// 非交互 resume 需要非空 prompt：claude `-p ""` / 空续投无意义会崩。交互源不看 prompt。
	if !src.Interactive && strings.TrimSpace(prompt) == "" {
		return JobResult{}, fmt.Errorf("%w: non-interactive resume requires a prompt", ErrInvalidRequest)
	}
	argv := append([]string{ac.Command}, agent.Render(tmpl, agent.Vars{SessionID: src.SessionID, Prompt: prompt})...)
```
Submit 参数追加（继承交互属性；pty 尺寸交给 runner 默认 + attach 后 resize，不强行还原）：
```go
		// 交互源续接为交互 job：走 pty runner，命令用交互模板（上面已选）。前端跳转后
		// ?attach=1 自动接入终端（P7 选 A）。非交互源 Interactive 为 false，行为不变。
		Interactive: src.Interactive,
```
> `ErrInvalidRequest` 若 `internal/job` 已有则复用；否则用最贴近的既有 sentinel（探查确认，勿新造重复语义）。

**验收**：`go build ./... && go vet ./...` 绿；`internal/job` 测试：
- 源 `Interactive=true` → resume 新 job `Interactive==true` 且 argv 不含 `-p`（用交互模板）；
- 源 `Interactive=false` + 非空 prompt → 用 `SessionResume`（含 `-p`），行为逐字不变（回归）；
- 源 `Interactive=false` + **空 prompt** → 返回错误（不再产生 `-p ""`）；
- agent 未显式配 `SessionResumeInteractive` → 内置默认填充。

---

### T3 —（前端）interactive 源「继续会话」自动 attach + prompt 框条件

**`web/src/views/JobDetail.vue`**：

**T3.1 `doResume` 对 interactive 源跳转带 `?attach=1`**（现状 `:475`）：
```ts
    const newJob = await resumeJob(props.id, resumePrompt.value)
    resumePrompt.value = ''
    showResumeForm.value = false
    // 交互源续接为 pty job：跳转即自动打开终端（?attach=1，:567 已有处理），在 TUI 里继续。
    const q = job.value?.interactive ? '?attach=1' : ''
    void router.push(`/jobs/${encodeURIComponent(newJob.id)}${q}`)
```

**T3.2 prompt 输入框：交互源隐藏、非交互源必填**（现状 resume 表单 `:1053` 区）：
- `v-if="!job.interactive"` 才渲染 prompt textarea（交互源进 TUI 自己打字，无需 prompt）。
- 非交互源：prompt 必填 —— 空时禁用「续投」按钮或提示（`:disabled="!job.interactive && !resumePrompt.trim()"`）。
- 交互源：按钮文案可改为「续接终端」，点击直接 `doResume`（prompt 传空，后端交互模板不看 prompt）。

> `job.interactive` 由 `Job.interactive?`（types 已有，`JobResult.Interactive` 出网 `model.go:182`）提供。

**验收**：`pnpm build` 绿；运行期：交互源 job 的「继续会话」不显示 prompt 框、点击后跳转新 job 并自动打开终端；非交互源仍有 prompt 框且空 prompt 时不能提交。

---

### T4 —（后端）`gofer serve --web-dir`

**T4.1 `webui.HandlerForDir`**（`internal/webui/embed.go`，复用纯函数 `handlerFor`）：
```go
// HandlerForDir serves the web console from an on-disk directory (dev convenience,
// P7): `gofer serve --web-dir web/dist` avoids re-embedding via `make web` on every
// front-end change. dir must be the built SPA root (containing index.html). Returns
// the handler and whether a real build is present (index.html exists).
func HandlerForDir(dir string) (http.Handler, bool) {
	return handlerFor(os.DirFS(dir))
}
```
> 需 import `os`。`handlerFor` 已处理 index.html 缺失→placeholder。

**T4.2 `config.ServerConfig` 加 `WebDir`**（仿既有 `WebEnabled *bool`）：
```go
	// WebDir 指向磁盘上的 web SPA 构建目录（dev：serve --web-dir）。非空则服务从该目录
	// 读取，而非嵌入的 dist；空则用嵌入版。不入持久化配置的常规路径，仅运行期设置。
	WebDir string `yaml:"web_dir,omitempty"`
```

**T4.3 `serve.Opts` + serve 命令选项**：
- `internal/serve/serve.go:50` 的 `Opts` 加 `WebDir string`；`:85` 旁设 `cfg.Server.WebDir = opts.WebDir`。
- `internal/commands/serve.go`：加 `c.StrOpt(&serveOpts.webDir, "web-dir", "", "", "serve the web console from this on-disk dir (dev; e.g. web/dist)")`；`serve.Start` 的 `Opts` 传 `WebDir: serveOpts.webDir`。

**T4.4 server 挂载分支**（`internal/httpapi/server.go:488`）：server 需拿到 webDir。仿 `webEnabled` 从 `serverCfg` 取：`:263` 旁存 `webDir: serverCfg.WebDir`，挂载处：
```go
	if s.webEnabled {
		var h http.Handler
		var ok bool
		if s.webDir != "" {
			h, ok = webui.HandlerForDir(s.webDir)
		} else {
			h, ok = webui.Handler()
		}
		_ = ok
		r.NotFound(func(c *rux.Context) { … h.ServeHTTP … })
	}
```
> `--no-web` 仍优先（`webEnabled` 为 false 时整个块跳过，`--web-dir` 无效）。

**验收**：`go build ./... && go vet ./...` 绿；`internal/webui` 测试：`HandlerForDir(临时目录含 index.html)` → `ok=true` 且服务该目录文件；无 index.html → placeholder（`ok=false`）。运行期冒烟（用户）：`gofer serve --web-dir web/dist` → 浏览器加载的是磁盘 dist（改前端 `pnpm build` 后刷新即见，无需重嵌）。

---

### T5 — 测试与验证门禁

**5.1 后端（容器内）**
- [x] `go build ./... && go vet ./...` 绿（容器 Linux）。
- [x] 全量 `go test ./... -p 1 -count=1` 绿（34 包，禁缓存）。
- [x] 新用例逐个 `-run -v -count=1` 确认真实执行（交互 resume 正/反向、非交互 argv 逐字回归、空 prompt 拒、agent 默认、HandlerForDir）。
- [x] `go list -deps ./internal/webui` 无 gofer 内部依赖（叶包性质不变）。

**5.2 前端（主机）**
- [x] `pnpm build` 绿，主控经 `gofer job -a exec` 在主机独立复跑。

**5.3 运行期冒烟（用户眼检，需重部署）**
- 交互 pty 会话「继续会话」→ 起 pty job → 自动打开终端 → TUI 可继续（不再 `-p ""` 崩）。
- 非交互 job「继续会话」空 prompt → 不能提交；填 prompt → 正常续投。
- 「快速重建」env 新增行有「删除」。
- `gofer serve --web-dir web/dist` → 改前端 `pnpm build` 后刷新即见。

> **实施中的关键发现（v0.1 未预见，主控审并确认必要）**：resume 用 `exec` 载体 + `Interactive=true` 会被 `config.go` 准入门当作普通 interactive exec 拒绝（raw exec + interactive 是禁的组合）。故对 resume 载体（`req.ResumeSourceAgent != ""`，`json:"-"` 不可伪造）按**源 agent** 判交互门并豁免"不能带 Cmd"——与 resume 既有的 exec 门豁免同一安全模型。worker 场景：`ResumeSourceAgent` 沿 server→worker 内部 dispatch 帧（wsproto）传递，公开 HTTP job JSON 仍 `json:"-"`。安全边界两个方向均有测试锁死。

## 测试清单汇总

| 层 | 文件 | 用例要点 |
|---|---|---|
| agent | `internal/agent/session_test.go`（扩展） | `SessionResumeInteractive` 内置默认填充（claude 无 `-p`）；显式配置覆盖 |
| job | `internal/job/resume_test.go`（扩展） | 交互源 → Interactive=true + 交互模板（无 `-p`）；非交互源 → 逐字不变；非交互空 prompt → 错误 |
| webui | `internal/webui/embed_test.go`（扩展） | `HandlerForDir` 有 index.html → ok=true 服务磁盘；无 → placeholder |
| 前端 | —（无框架） | `pnpm build` + 5.3 冒烟 |

**核心不变量**
- **非交互 resume 行为逐字不变**：`SessionResume` 模板与 argv 不动；仅新增交互分支。
- **交互源续接为 pty job**：`src.Interactive` → 新 job `Interactive=true` + 交互模板（无 `-p`/`exec`）。
- **非交互 resume 拒绝空 prompt**：不再产生 `claude … -p ""`。
- **`--web-dir` 不改默认**：未给时用嵌入 dist；`--no-web` 优先于 `--web-dir`。
- **env 新增行可删**：与继承行删除对称。

## 风险

- **R1（需实测）**：codex 交互 resume 命令是推测（`resume {{session_id}}`）。实施时须在主机 `codex --help` 实测；若 codex 无交互 resume，则 codex 交互源 resume 降级（保持非交互模板 + 要求 prompt，或前端对 codex 交互源提示暂不支持交互续接）。claude 侧确定。
- **R2**：交互 resume 起 pty job 需 pty runner 可用（P2 已具备）。若源在 worker 上，pty 在 worker 侧选（既有逻辑）。
- **R3**：`P4` 曾定「D4 路② attach 续接不在 P4」；P7 用 resume 重开交互会话（源已终态、无活 pty 可 attach），与当时 attach 一个 running 会话是不同路径，不冲突。
- **R4**：非交互 resume 改「prompt 必填」是对 P4「允许空 prompt」的收紧。P4 的空 prompt 对 claude `-p` 本就崩，属修正而非回退。

## 待确认

无。问题 2 方向（选 A）已拍板；codex 交互 resume 命令为实施时实测项（R1），非设计待确认。
