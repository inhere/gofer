# Gofer Web 控制台 v2 · 只读层 实施计划

> 对应 design [`../design/2026-06-23-web-console-v2-readonly-design.md`](../design/2026-06-23-web-console-v2-readonly-design.md)（§10 四点定稿 + D1–D7）。实施计划：阶段总纲 + 进度 + 各阶段细化（代码片段/落点 + 验收，SR1105）。
> 全只读、前端为主 + 薄后端（+3 endpoint），无写风险。

## 1. 总纲

| 阶段 | 目标 | 依赖 | 工作量 |
|---|---|---|---|
| **P1** | 后端 3 只读 endpoint（`internal/project/browse.go` + handler + 注册 + 测试）：E20 git / E32 子 git / E32 关键文件 | — | 中 |
| **P2** | E19a 产物预览：`FilePreview.vue`（marked+DOMPurify）+ JobDetail 接入 + 依赖 | — | 中 |
| **P3** | E20/E32 前端：`Projects.vue` git 卡 + 子仓列表 + 关键文件（复用 FilePreview） | P1, P2 | 中 |
| **P4** | E31 集群拓扑：`Cluster.vue`（SVG 星型 + 节点面板，复用 listRunners+listProjects） | — | 中 |

> 顺序：P1（后端）→ P2（预览组件）→ P3（用 P1 endpoint + P2 组件）→ P4（独立，可与 P2/P3 并行）。前端阶段验收=`pnpm -C web build`（vue-tsc）绿 + agent-browser 眼检（渲染需目视）。

## 2. 关键落点（design → 代码）

| 改动 | 落点 | design |
|---|---|---|
| 只读 git/fs helper | 新 `internal/project/browse.go`（**不 import job 包**，避免循环；自带 `exec.CommandContext("git",…)`+超时+cap）| D6 |
| 3 handler | 新 `internal/httpapi/project_browse_handler.go` | §7 |
| 路由注册 | `internal/httpapi/server.go:206-317`（rux `/v1` 组）| §7 |
| 预览组件 | 新 `web/src/components/FilePreview.vue`（E19a+E32 共用）| D5 |
| 产物预览接入 | `web/src/views/JobDetail.vue` | 6.2 |
| 项目 git/子仓/文件 | `web/src/views/Projects.vue` | 6.3/6.4 |
| 拓扑视图 | 新 `web/src/views/Cluster.vue` + `router.ts` + `App.vue` 导航 | 6.1 |
| API client + DTO | `web/src/api/client.ts` + `api/types.ts` | §9 |
| 依赖 | `web/package.json`（marked + dompurify）| D5 |
| 路径根 | `config.ExecPath(proj)` + `project.SafeJoin`（已落地）| D2 |

## 3. 前置检查（plan-checking）

- [x] `go build ./... && go vet ./...` 绿；`go test ./internal/project/... ./internal/httpapi/...` 基线绿。
- [ ] `pnpm -C web install` OK；`pnpm -C web build`（vue-tsc）基线绿。（前端阶段 P2/P3/P4）
- [x] 确认 `runGit` 在 `internal/job/gitdiff.go`（job 包）——P1 **不可** import（job→project 单向依赖，反向循环），browse.go 自带 git 调用。
- [x] 确认 `git` 在运行环境可用（容器内 `git --version` = 2.43.0）。
- [ ] agent-browser CLI 可用（前端眼检）。

## 4. 进度跟进

- [x] **P1** 后端 3 endpoint（browse.go + handler + 注册 + 测试）— 完成 2026-06-27，见 §6 实施结果
- [x] **P2** E19a FilePreview + JobDetail + 依赖 — 完成 2026-06-27（构建绿 + XSS sanitize 已验；agent-browser 眼检随 P3/P4 统一批量做），见 §6 实施结果
- [ ] **P3** E20/E32 前端（Projects 增强）
- [x] **P4** E31 Cluster 拓扑 — 完成 2026-06-27（构建绿；agent-browser 眼检统一最后批量做），见 §6 实施结果

---

## P1：后端 3 只读 endpoint

### T1.1 `internal/project/browse.go`

只读 git 调用（自带，不依赖 job 包）+ 子 git 发现 + 白名单文件读。

```go
package project

import (
	"bufio"; "context"; "os"; "os/exec"; "path/filepath"; "strings"; "time"
	"github.com/inhere/gofer/internal/config"
)

// keyFileAllowlist is the E32 basename whitelist (design D3, §10.3). 只读关键文件,
// 杜绝任意路径读取(防 .env 泄露)。后续可做成可配。
var keyFileAllowlist = map[string]bool{
	"README.md": true, "README": true, "README.txt": true,
	".gitignore": true, "AGENTS.md": true, "CLAUDE.md": true,
	"go.mod": true, "package.json": true, "LICENSE": true, "LICENSE.md": true,
}

const (
	maxKeyFileBytes = 256 * 1024 // D3
	repoScanDepth   = 3          // D4
	maxRepos        = 100        // D4
	gitTimeout      = 5 * time.Second
)
var repoSkipDirs = map[string]bool{"node_modules": true, "vendor": true, "dist": true, ".git": true}

// GitStatus / RepoInfo / FileContent 见下；execRoot 一律 cfg.ExecPath(proj)。

// runGitRO runs a read-only git subcommand under dir (fixed args, no user concat),
// timeout + output cap; errors degrade to ("", err) — never panics. (D6/§8)
func runGitRO(dir string, capBytes int, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	if len(out) > capBytes {
		out = out[:capBytes]
	}
	return string(out), nil
}

// ProjectGit returns the current git state of a project root (E20). is_git_repo
// false when execRoot is not a work tree (or not locally reachable).
func ProjectGit(cfg *config.Config, proj config.ProjectConfig) (GitStatus, error) {
	root := cfg.ExecPath(proj)
	if _, err := runGitRO(root, 1024, "rev-parse", "--is-inside-work-tree"); err != nil {
		return GitStatus{IsGitRepo: false}, nil
	}
	branch, _ := runGitRO(root, 1024, "rev-parse", "--abbrev-ref", "HEAD")
	porcelain, _ := runGitRO(root, 64*1024, "status", "--porcelain")
	logOut, _ := runGitRO(root, 32*1024, "log", "-n", "10", "--pretty=%h\x1f%s\x1f%an\x1f%ct")
	return GitStatus{
		IsGitRepo: true, Branch: strings.TrimSpace(branch),
		Dirty: strings.TrimSpace(porcelain) != "",
		RecentCommits: parseCommits(logOut), // split \n then \x1f
	}, nil
}

// DiscoverRepos walks execRoot up to repoScanDepth, skipping repoSkipDirs, and
// returns each dir containing a .git with its branch+dirty (E32, D4).
func DiscoverRepos(cfg *config.Config, proj config.ProjectConfig) ([]RepoInfo, error) { /* filepath.WalkDir + depth/skip/cap; per hit: branch+dirty via runGitRO */ }

// ReadKeyFile reads a whitelisted key file under the project root (E32, D3):
// basename ∈ allowlist, SafeJoin(execRoot, rel) 防穿越, ≤256KB, text-only.
func ReadKeyFile(cfg *config.Config, proj config.ProjectConfig, rel string) (FileContent, error) {
	if !keyFileAllowlist[filepath.Base(rel)] {
		return FileContent{}, ErrForbiddenFile // → handler 403
	}
	abs, err := SafeJoin(cfg.ExecPath(proj), rel) // 复用既有防逃逸
	if err != nil { return FileContent{}, err }
	// stat ≤256KB; read; isText 检测(非文本→ErrBinary→415); 截断标记 truncated
}
```

类型：`GitStatus{IsGitRepo bool; Branch string; Dirty bool; RecentCommits []Commit}`；`RepoInfo{RelPath, Branch string; Dirty bool}`；`FileContent{Name string; Size int64; Content string; Truncated bool}`。

### T1.2 `internal/httpapi/project_browse_handler.go` + 注册

3 handler 薄封装（先 `s.projects.Get(key)` 404 守卫 → 调 browse → JSON）：

```go
// GET /v1/projects/{key}/git
func (s *Server) handleGetProjectGit(c *rux.Context) { /* Get(key) → project.ProjectGit(cfg, proj) → JSON */ }
// GET /v1/projects/{key}/repos
func (s *Server) handleListRepos(c *rux.Context) { /* → project.DiscoverRepos → {repos:[...]} */ }
// GET /v1/projects/{key}/file?path=<rel>
func (s *Server) handleGetProjectFile(c *rux.Context) {
	// rel := c.Query("path"); ErrForbiddenFile→403; SafeJoin err→404; ErrBinary→415
}
```

`server.go`（`/v1` 组，约 :230 projects 路由附近）注册：
```go
r.GET("/projects/{key}/git", s.handleGetProjectGit)
r.GET("/projects/{key}/repos", s.handleListRepos)
r.GET("/projects/{key}/file", s.handleGetProjectFile)
```

### T1.3 测试

- `internal/project/browse_test.go`：临时 git repo（`git init` + commit）→ `ProjectGit` 返回 branch/dirty/commits；非 git 目录→`IsGitRepo:false`；`DiscoverRepos` 在嵌套含 .git 的临时树命中（深度/skip 验证）；`ReadKeyFile` 白名单内（README）成功、白名单外（`.env`）→ ErrForbiddenFile、`../escape`→SafeJoin 拒绝、>256KB→截断、二进制→ErrBinary。
- handler 测试（`project_browse_handler_test.go` 或并入现有）：3 endpoint 的 200/403/404/415 路径；unknown project→404。

### P1 验收

- [x] `go build ./... && go vet ./...` 绿；`go test ./internal/project/... ./internal/httpapi/...` 绿（新增 20 个用例全绿）。
- [x] 冒烟：起 serve（临时 config，project 指向一个 git 工作树）→ `/git` 返 branch=main/dirty=true/最近提交；`/repos` 列根("."")+子仓(sub1)；`/file?path=README.md`→200、`?path=.env`→403、`?path=../README.md`→404、无 token→401、unknown project→404。
- [x] **安全断言**：白名单外(.env)→403、路径穿越(../)→404、二进制(NUL)→415 均被拒；git 参数全固定字面量（rev-parse/branch/status/log，无用户拼接）；project 包不 import job（`go list -deps` 确认无环）。

---

## P2：E19a 产物预览（前端）

### T2.1 依赖 + 渲染组件

- `web/package.json` 加 `marked` + `dompurify`；`pnpm -C web install`。
- 新 `web/src/components/FilePreview.vue`：props `{ name: string; blob: Blob }`（或 `fetchBlob: () => Promise<Blob>`）。按 `name` 后缀 + blob 大小（>2MB 回退）判类型（D5）：
  - `.md` → `DOMPurify.sanitize(marked.parse(text))` 注入（`v-html` 仅注 sanitized）。
  - 图片(`png/jpg/jpeg/gif/webp/svg`) → `<img :src="URL.createObjectURL(blob)">`（svg 也走 img，不内联）。
  - `.json` → `JSON.stringify(JSON.parse(text), null, 2)` 入 `<pre>`。
  - 其他文本/代码 → `<pre>`（起步不高亮）。
  - 超阈值/二进制 → 显示"过大/二进制，请下载"+ 下载按钮。
  - 组件卸载时 `URL.revokeObjectURL` 释放。

### T2.2 JobDetail 接入

- `web/src/views/JobDetail.vue` 产物清单每项加"预览"——点击取 blob（复用/新增 client 取 blob 的方法，见 T2.3）→ 弹层/内联挂 `FilePreview`；保留原"下载"。

### T2.3 client

- `web/src/api/client.ts`：现有 `downloadArtifact` 若直接触发浏览器下载，新增 `fetchArtifactBlob(id, name): Promise<Blob>`（fetch + Authorization + `res.blob()`）供预览用。

### P2 验收

- [x] `pnpm -C web build`（vue-tsc）绿。
- [x] **XSS 冒烟**（node 侧 jsdom 隔离断言，跑完即删）：`DOMPurify.sanitize(marked.parse(...))` 清除 `onerror`/`javascript:`/`<script>`，PASS。
- [ ] agent-browser 眼检：md 产物渲染为 HTML（表格/代码块正常）、图片 inline、json 格式化、大/二进制回退下载（**统一放 P3/P4 后批量眼检**）。截图存 `tmp/`。

---

## P3：E20/E32 前端（Projects 增强，依赖 P1+P2）

### T3.1 client + DTO

`api/types.ts` 加 `GitStatus`/`RepoInfo`/`FileContent`；`api/client.ts` 加 `getProjectGit(key)` / `listRepos(key)` / `getProjectFile(key, path)`。

### T3.2 Projects.vue

右列详情加三块：
- **git 状态卡**：`getProjectGit` → 分支 + dirty 徽标 + 最近提交列表 + 刷新按钮；`is_git_repo:false` 显"非 git 仓/非本地可达"。
- **子仓库列表**：`listRepos` → rel_path + 分支 + dirty。
- **关键文件**：白名单文件，点击 → `getProjectFile` → `FilePreview` 渲染（md 走渲染器）。

### P3 验收

- [x] `pnpm -C web build` 绿（vue-tsc --noEmit + vite build 均通过）。
- [ ] agent-browser 眼检：选一个 git 项目 → git 卡显分支/dirty/提交；子仓列表正确；点 README.md 经 FilePreview 渲染；非 git 项目优雅降级。截图存 `tmp/`。（浏览器眼检统一最后批量，留空）

> P3 落地：`web/src/api/types.ts`（GitStatus/GitCommit/RepoInfo/ReposResp/FileContent）；
> `web/src/api/client.ts`（getProjectGit/listRepos/getProjectFile）；`web/src/views/Projects.vue`
> 右列加 git 状态卡（分支/dirty/最近提交+刷新）+ 子仓列表 + 关键文件（候选白名单按钮→content 包 Blob→FilePreview）。

---

## P4：E31 集群拓扑（前端，独立）

### T4.1 Cluster.vue + 路由

- 新 `web/src/views/Cluster.vue`（路由 `/cluster`，`router.ts` + `App.vue` 左轨导航）。
- 数据：并发 `listRunners()` + `listProjects()`；4s 轮询（复用 Runners 节奏）。

### T4.2 SVG 星型拓扑 + 节点面板

- SVG 手绘：中心 hub（server，标 in-flight 合计）；辐射 worker（色=connected/disconnected，标 heartbeat_age/in_flight/labels，心跳脉冲复用 `components/Heartbeat.vue`）、peer（色=up/down/unknown，标 latency/base_url）、local。
- 点击节点 → 侧栏面板：worker(id/心跳/in_flight/labels/状态) / peer(base_url/probe latency/error) / local(本机 + server projects 概览，取 listProjects)。**不画项目→节点映射边**（D2/§10.2）。

### P4 验收

- [x] `pnpm -C web build` 绿（vue-tsc --noEmit && vite build，2026-06-27）。
- [ ] agent-browser 眼检：拓扑渲染（hub + worker/peer 辐射）、节点色随状态、点击出面板、心跳脉冲动；深浅主题都看。截图存 `tmp/`。（统一最后批量做）

---

## 5. 完成判定

- 四阶段验收 PASS；`go build/vet ./...` + `go test ./internal/project/... ./internal/httpapi/...` 绿；`pnpm -C web build` 绿。
- 端到端眼检：拓扑/节点面板、产物预览（含 XSS 防护）、项目 git 卡、子仓+关键文件渲染，深浅主题。
- 安全：白名单文件 + SafeJoin + 固定 git 参数 + 仅本地可达项目（D2/D3）；md 渲染 sanitize。
- 前端构建产物不提交（`make web` 流水线生成，沿用既有约定）；roadmap 回填实施结果。

## 6. 实施结果（完成后回填）

> P1–P4 commit 短码 + 关键决策 + 验收/眼检记录 + 遗留。

### P1（2026-06-27）

- 产物：`internal/project/browse.go`（只读 git/fs，自带 `exec.CommandContext("git",…)`+超时+cap，**不 import job**，`go list -deps` 确认无环）、`internal/httpapi/project_browse_handler.go`（3 薄 handler）、`server.go` `/v1` 组注册 3 路由、`internal/project/browse_test.go`（10 用例）、`internal/httpapi/project_browse_handler_test.go`（10 用例）。
- 路由 + 错误码：`GET /v1/projects/{key}/git`（unknown→404；非 git→200 `is_git_repo:false`）、`/repos`（unknown→404；`{repos:[...]}`）、`/file?path=`（缺 path→400；非白名单 basename→403；穿越/缺文件→404；二进制→415）。
- 关键决策：`is-inside-work-tree` 用 `==\"true\"` 判定（对齐 job 包 `isGitWorkTree`，兼顾 bare repo），略偏离计划骨架仅判 err；二进制检测用 NUL 字节启发式（同 git，避免截断切断多字节 UTF-8 误判）；`DiscoverRepos` 含根仓（`rel_path:\".\"`）。
- 验收：`go build/vet ./...` 绿；`go test ./internal/project/... ./internal/httpapi/...` 绿（+20 用例）；真机冒烟 200/403/404/415/401 全过。

### P2（2026-06-27）

- 产物：`web/src/components/FilePreview.vue`（新，E19a+P3 共用渲染器）、`web/src/views/JobDetail.vue`（产物清单每项加「预览」+ 弹层挂 FilePreview，保留「下载」）、`web/src/api/client.ts`（新增 `fetchArtifactBlob(id,name)`：fetch+鉴权头+`res.blob()`）、`web/package.json` + `pnpm-lock.yaml`（加 `marked@18.0.5` + `dompurify@3.4.11`，均自带 TS 类型，无需 `@types/dompurify`）。
- FilePreview 分支（按 name 后缀 + blob 大小，D5）：`.md`→`DOMPurify.sanitize(marked.parse(text,{async:false}))` 后 v-html（仅注 sanitized）；图片 `png/jpg/jpeg/gif/webp/svg`→`<img :src=objectURL>`（svg 也走 img 不内联）；`.json`→`JSON.stringify(JSON.parse(text),null,2)` 入 `<pre>`（非法 JSON 原样兜底）；其他→文本读 + NUL 启发式判二进制，文本入 `<pre>`、二进制回退；`>2MB`/二进制/异常→回退下载（emit download）。`onUnmounted`/blob 变更 `revokeObjectURL` 防泄漏。
- 验收：`pnpm -C web build`（含 vue-tsc）绿；XSS 断言 PASS（node+jsdom 隔离，跑完即删，marked 产出的 `onerror`/`javascript:`/`<script>` 均被 sanitize 清除）。agent-browser 渲染眼检随 P3/P4 统一批量做。
- dist：`web/.gitignore` 已含 `dist/`，提交未含构建产物。

### P4（2026-06-27）

- 产物：`web/src/views/Cluster.vue`（新，路由 `/cluster`）、`web/src/router.ts`（注册 `/cluster`）、`web/src/App.vue`（导航加 cluster 入口）。纯前端、无新后端，复用现成 `listRunners()` + `listProjects()`。
- 拓扑布局：星型——hub 居中(50%,50%)，辐射节点按数量均匀分布在圆周（角度 `-90° + i·360°/N`，三角函数算百分比坐标；为视觉圆形取水平半径 `RX = RY/AR`，AR=容器宽高比 1.3）。边用 SVG `<line>`（viewBox 0..100 + `preserveAspectRatio=none` + `vector-effect=non-scaling-stroke`），节点为绝对定位 HTML（left/top 同百分比对齐边端点）——故能直接复用 `Heartbeat.vue` 而无 `<foreignObject>` 命名空间坑。
- 节点状态色（CSS token，不硬编码）：worker 随心跳态 connected=`--done`/stale=`--run`/flatline=`--fail`；peer up=`--done`/down=`--fail`/unknown=`--queue`；local 恒 `--done`。worker 节点用 `Heartbeat.vue` 脉冲，peer/local 用静态点。
- 节点面板（右侧抽屉 + 遮罩）：hub(in-flight 合计/各类计数/projects 数) / worker(id/状态/心跳/in-flight/labels) / peer(base_url/状态/latency/探活年龄/error) / local(本机 in-process + server projects 概览，取 `listProjects`)。**不画「项目→节点」映射边**（D2/§10.2，server 不知 worker 的 projects）。4s 轮询复用 Runners 节奏（Page Visibility 暂停 + 逐秒推进年龄），卸载清 timer。
- 验收：`pnpm -C web build`（`vue-tsc --noEmit && vite build`）绿。agent-browser 眼检随 P2/P3/P4 统一最后批量做。
- dist：`web/.gitignore` 已含 `dist/`，提交未含构建产物。
