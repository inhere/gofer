# Gofer Web 控制台 v2 · 只读观察层 设计

> 一句话：把控制台从"看板 + 详情 + Workers 名册"推向"**集群拓扑 + 项目透视 + 产物/文件预览**"——全部**只读、无写风险**，数据源多现成。
> 关联：roadmap [`../2026-06-20-enhancements-roadmap.md`](../2026-06-20-enhancements-roadmap.md) §横切「Web 控制台 v2」（本设计=**只读层**）；写/交互层（E30 pty · E31 配置编辑 · E21 主机动作）各自独立 design，不在本文。
> 覆盖：**E31 拓扑+节点面板（只读）· E32 项目空间浏览（子 git+关键文件）· E19a 产物预览 · E20 项目 git 状态**。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-06-23 | inhere | 初稿：4 功能 + D1–D7 + 新增 3 只读 endpoint + 前端落点，待审核 |
| v0.2 | 2026-06-23 | inhere | §10 四点定稿：md 渲染=**marked + DOMPurify**（代码 `<pre>` 起步、按需 highlight.js）/ 拓扑只画 hub-worker-peer 物理拓扑 / 白名单先硬编码 / E20·E32 不限流；进入 plan 阶段 |

## 2. 现状基线（调研结论，复用不重造）

前端：**Vue 3 + Vite + vue-router**，9 视图，API client 统一 Bearer（`web/src/api/client.ts`），SSE 已有（`api/sse.ts`）。后端 `internal/httpapi`（rux router，`server.go:206-317`），`/v1` Bearer 鉴权。

| 能力 | 现状 | 本设计需要 |
|---|---|---|
| 节点数据源 | `GET /v1/runners`（worker/peer/local + 状态/心跳/in-flight/labels，`runner_handler.go`）；`Runners.vue` 已 4s 轮询分组展示 | **无需新后端**；前端加拓扑图 + 节点面板 |
| 项目数据 | `GET /v1/projects[/{key}]`（key/host_path/container_path/agents/runners…，`project_handler.go`）；`Projects.vue` 只读配置 | 节点面板/项目透视复用 |
| 产物 | `GET /v1/jobs/{id}/artifacts`(清单) + `/artifacts/{name}`(下载，`artifact_handler.go`)；`JobDetail.vue` 已列举+下载 | **缺 inline 预览**（md/图/json/代码）|
| git 调用 | `runGit(ctx,cwd,cap,args...)` + `captureDiff`（`internal/job/gitdiff.go`，os/exec、超时、cap、出错降级）| E20/E32 复用其范式（需提取到可共享位置）|
| 路径解析 | `Config.ExecPath(proj)`（`config/model.go`，D10 已落地）+ `project.SafeJoin(execRoot,cwd)`（`project/path.go`，防逃逸）| E20/E32 跑 git/读文件的根 + 安全 |
| 前端嵌入 | `internal/webui/embed.go`（`//go:embed all:dist`）+ `make web`（pnpm build）| 新视图随 bundle 打包，无 CDN |

## 3. 范围

**做**（4 项只读）：
- **E31** 集群拓扑图 + 节点面板（前端，复用 `/v1/runners`+`/v1/projects`，无新后端）。
- **E19a** 产物 inline 预览（前端渲染器，复用现有下载 endpoint）。
- **E20** 项目当前 git 状态（新 `GET /v1/projects/{key}/git` + 前端）。
- **E32** 子 git 发现 + 关键文件查看（新 `GET /v1/projects/{key}/repos` + `/file` + 前端，复用 E19a 渲染器）。

**不做**：
- 写/交互层（E30 pty、E31 配置**编辑**、E21 主机动作）——各自独立 design。
- **通用文件树浏览**（E19b）——只白名单关键文件（防 `.env` 泄露 + 路径穿越，D3）。
- **远端 worker 项目的 git/文件/产物**——gofer 进程经 `ExecPath` 只达本地项目；worker 远端项目不可读（同 E29 跨机限制），留后续（D2）。
- 引入重前端图库（拓扑用 SVG 手绘，D1）。

## 4. 架构总览

```
前端（新增/增强视图）                后端（新增 3 只读 endpoint，其余复用）
  Cluster.vue (新, /cluster)  ──┐
    SVG 星型拓扑 + 节点面板      ├─ GET /v1/runners        (现成)
                                └─ GET /v1/projects[/{key}](现成)
  Projects.vue (增强)         ──┬─ GET /v1/projects/{key}/git    (新, E20)
    git 状态卡 + 子 git 列表    ├─ GET /v1/projects/{key}/repos  (新, E32 子git发现)
    + 关键文件查看             └─ GET /v1/projects/{key}/file?path= (新, E32 白名单文件)
  JobDetail.vue (增强)        ──── GET /v1/jobs/{id}/artifacts/{name} (现成下载, E19a 前端渲染)
  components/FilePreview.vue (新, E19a+E32 共用渲染器)

  共享后端: internal/project/browse.go (新) — 复用 runGit 范式 + ExecPath + SafeJoin
```

## 5. 决策点

- **D1 拓扑渲染 = SVG 手绘星型**（hub 居中，worker/peer 辐射，节点色=状态，点击=面板）。不引 vis-network/d3 等重库——契合"离线字体/无 CDN"取向，节点数（单 hub 下 worker/peer）规模小，SVG 足够。
- **D2 范围 = 仅 gofer 进程可达项目**：E20/E32 跑 git/读文件的根一律 `ExecPath(proj)`（本地）。远端 worker 项目 gofer 进程访问不到 → endpoint 对其返回"不可达/非本地"，**不做远端拉取**（留后续）。拓扑里 worker 节点面板只显 server 已知信息（心跳/labels/in-flight/地址），**不显 worker 的项目列表**（worker.yaml 独立、server 不可见）。
- **D3 关键文件白名单（E32 安全核心）**：`/file` 只允许 **basename 白名单**（`README*`、`.gitignore`、`AGENTS.md`、`CLAUDE.md`、`go.mod`、`package.json`、`LICENSE*` 等可配），path 必经 `SafeJoin(ExecPath, rel)` 防穿越，大小上限 **256KB**，仅文本（二进制拒绝）。**不开放任意路径**。
- **D4 子 git 发现**：从 `ExecPath(proj)` 起递归找 `.git`，**深度 ≤3** + 排除噪声目录（`node_modules`/`vendor`/`dist`/`.git` 内部等），每个子 repo 只跑轻量 `branch + dirty`（runGit，cap+超时）；命中数上限（如 ≤100）防爆扫。
- **D5 E19a 预览**（v0.2 定稿）：前端按**后缀 + 大小阈值（默认 2MB）**判类型：`md`→`marked` 渲染 + `DOMPurify` sanitize；图片(`png/jpg/gif/webp/svg`)→`<img src=blob>`（svg 走 img 不内联，防脚本）；`json`→格式化展示；文本/代码→`<pre>` **起步**（按需再引 `highlight.js` 只注册用到的语言，不全量打包）；其他/超阈值/二进制→回退现有下载。库 `marked`+`DOMPurify` 打包进 bundle（无 CDN）——marked 比 markdown-it 轻约半、GFM 表格够 README 用；**DOMPurify 必留**（产物含 agent 生成的不可信内容，md→HTML 必 sanitize）。
- **D6 只读 git/fs helper 位置**：把只读 git 子集（`status --porcelain`/`rev-parse --abbrev-ref`/`log`）+ 子 git 扫描 + 关键文件读取放 **新 `internal/project/browse.go`**（复用 `runGit` 范式；若 `runGit` 现私有于 job 包，提取一个共享小工具或在 project 包内重写同款 `exec.CommandContext("git",...)`+超时+cap）。httpapi handler 薄封装调用。**git 参数全固定**（不接用户拼接），杜绝命令注入。
- **D7 刷新策略**：git 状态/子 git/文件**进入视图时取 + 手动刷新**（不轮询——git 状态变化不频繁，避免无谓 exec）；拓扑沿用 `Runners.vue` 现有 4s 轮询。

## 6. 功能模块设计

### 6.1 E31 集群拓扑 + 节点面板（前端，无新后端）

- **新视图 `Cluster.vue`**（路由 `/cluster`，加进 `router.ts` + 左轨导航）。也可在 `Runners.vue` 加"拓扑"tab——倾向独立视图（拓扑是新呈现，名册保留）。
- **数据**：并发 `listRunners()` + `listProjects()`（client 现成）；4s 轮询（复用 Runners 节奏）。
- **拓扑**：SVG 星型——中心 hub（server，标 in-flight 总数/版本）；辐射节点：worker（色=connected/disconnected，标 heartbeat_age/in_flight/labels）、peer（色=up/down/unknown，标 latency/base_url）、local（本机执行）。心跳脉冲复用 `components/Heartbeat.vue`。
- **节点面板**：点击节点 → 侧栏展示该节点详情（worker: id/心跳/in-flight/labels/状态；peer: base_url/probe latency/error；local: 本机 + server 的 projects 概览）。projects 信息取 `/v1/projects`（仅 server 本地项目，D2）。

### 6.2 E19a 产物 inline 预览（前端 + 可选后端 Content-Type）

- **新组件 `components/FilePreview.vue`**：入参 `{name, fetchBlob()}`，按 D5 渲染；E32 关键文件查看**复用同组件**。
- **JobDetail.vue 增强**：产物清单项加"预览"——点击 → `downloadArtifact(id,name)` 取 blob → `FilePreview` 渲染；超阈值/二进制 → 保留下载按钮。
- **后端（可选小改）**：`handleDownloadArtifact`（`artifact_handler.go:58`）现回 `octet-stream`；可按后缀补正确 `Content-Type` 便于前端判断（前端也可纯按后缀，后端改非必须）。
- **依赖**：`web/package.json` 加 `marked` + `dompurify`（打包进 bundle）。

### 6.3 E20 项目当前 git 状态（新 endpoint + 前端）

- **`GET /v1/projects/{key}/git`** → `{is_git_repo, branch, dirty, ahead?, behind?, recent_commits:[{hash,subject,author,ts}]}`。后端 `browse.go`：`cwd=ExecPath(proj)`；`rev-parse --is-inside-work-tree`（非 git → `is_git_repo:false`）；`rev-parse --abbrev-ref HEAD`；`status --porcelain`（dirty=非空）；`log -n 10 --pretty=...`。固定参数、超时、cap、出错降级。
- **前端**：`Projects.vue` 右列详情加 git 状态卡（分支 + dirty 徽标 + 最近提交列表 + 刷新按钮）。

### 6.4 E32 子 git 发现 + 关键文件（新 endpoint + 前端）

- **`GET /v1/projects/{key}/repos`** → `{repos:[{rel_path, branch, dirty}]}`。后端 `browse.go`：从 `ExecPath(proj)` 按 D4 扫 `.git`（深度/排除/上限），每命中跑轻量 branch+dirty。
- **`GET /v1/projects/{key}/file?path=<rel>`** → `{name, size, content, truncated?}` 或 404/415。后端：basename ∈ 白名单（D3）→ `SafeJoin(ExecPath, path)` → 文本读（≤256KB，二进制 415）。
- **前端**：`Projects.vue` 加"子仓库"列表（rel_path + 分支 + dirty）+ "关键文件"区（白名单文件存在性，点击 → `FilePreview` 渲染，md 走渲染器）。

## 7. 新增 API 规格（统一 `/v1` Bearer 鉴权，只读 GET）

| 方法 | 路径 | 返回 | 说明 |
|---|---|---|---|
| GET | `/v1/projects/{key}/git` | `{is_git_repo,branch,dirty,recent_commits[]}` | E20；cwd=ExecPath；非 git → `is_git_repo:false`；非本地可达 → 同样 false + 提示 |
| GET | `/v1/projects/{key}/repos` | `{repos:[{rel_path,branch,dirty}]}` | E32 子 git；深度≤3、排除噪声、上限 |
| GET | `/v1/projects/{key}/file?path=<rel>` | `{name,size,content,truncated}` | E32 关键文件；白名单 basename + SafeJoin + ≤256KB + 文本 only；越界→403/404，二进制→415 |

注册点：`internal/httpapi/server.go:206-317`（rux `/v1` 组）；handler 新增 `project_browse_handler.go`，调 `internal/project/browse.go`。

## 8. 安全（SR1402 闭环）

- **只读、无写**：本层不改任何状态；git 仅只读子集（status/branch/log/rev-parse），**参数全固定**、不接用户拼接 → 无命令注入；无 git 写命令。
- **路径**：所有项目内路径经 `SafeJoin(ExecPath, rel)` 防 `../`/符号链接逃逸（复用 `path.go` 既有防护）；文件查看 basename 白名单（D3），杜绝 `.env`/任意文件读取。
- **资源**：git 调用超时 + 输出 cap（复用 runGit 范式）；子 git 扫描深度/数量上限（D4）；文件 256KB 上限。
- **前端 XSS**：md 渲染 `DOMPurify` sanitize；svg 走 `<img>` 不内联（防脚本）；json/代码纯文本展示。
- **鉴权**：复用 `/v1` Bearer（`auth.go`），与现有只读 API 同级；可纳入 caller 限流（E17，按需）。
- **远端边界**：worker 远端项目不可达即返回"非本地"，不尝试拉取（D2），不暴露 server 进程无关路径。

## 9. 前端落点

| 改动 | 文件 |
|---|---|
| 新视图 Cluster（拓扑+面板）| `web/src/views/Cluster.vue` + `router.ts` + `App.vue` 导航 |
| 预览组件（E19a+E32 共用）| `web/src/components/FilePreview.vue` |
| 产物预览接入 | `web/src/views/JobDetail.vue` |
| 项目 git/子仓/文件 | `web/src/views/Projects.vue` |
| API client + DTO | `web/src/api/client.ts`（getProjectGit/listRepos/getProjectFile）+ `api/types.ts` |
| 依赖 | `web/package.json`（marked + dompurify）|

## 10. 已确认事项（v0.2 定稿，2026-06-23）

1. **md 渲染 = `marked` + `DOMPurify`**；代码/json 预览 `<pre>` 起步，按需再引 `highlight.js`（只注册用到的语言）。DOMPurify 必留（sanitize 不可信内容）。
2. **拓扑只画 hub-worker-peer 物理拓扑**——server 不知 worker 的 projects（worker.yaml 独立），不画"项目→节点"映射。
3. **关键文件白名单先硬编码**：`README*` / `.gitignore` / `AGENTS.md` / `CLAUDE.md` / `go.mod` / `package.json` / `LICENSE*`；后续按需做成可配。
4. **E20/E32 不纳入 caller 限流**（只读轻量）；鉴权仍走 `/v1` Bearer。

## 11. 结论

- 4 项全只读、**E31 数据源现成（纯前端）**、E19a 复用下载 endpoint（前端渲染器）、E20/E32 仅 +3 只读 endpoint（复用 runGit+ExecPath+SafeJoin）。改动以**前端为主 + 薄后端**，无写风险、边界清晰。
- 安全核心：白名单文件（非通用文件树）+ SafeJoin + 固定 git 参数 + 仅本地可达项目（D2/D3）。
- 拆阶段建议（plan 时）：**P1** 后端 3 endpoint（browse.go + handler + 测试）· **P2** E19a 预览组件 + JobDetail · **P3** E20/E32 前端（Projects 增强）· **P4** E31 Cluster 拓扑视图。P1 是 P3 的后端前置；P2/P4 偏前端可并行。审核通过后出 `plans/2026-06-23-web-console-v2-readonly/`。
