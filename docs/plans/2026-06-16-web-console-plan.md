# dev-agent-bridge Web 控制台 实施计划

> 依据设计 [`../design/2026-06-16-web-console-design.md`](../design/2026-06-16-web-console-design.md)（v0.2，已评审）。本计划细化 **web-P1**（只读监控+实时日志+取消，零依赖）到代码级；**web-P2**（运行中交互，依赖主 plan P9）仅留大纲，待 P9 落地再细化。
> 衔接主计划 [`2026-06-16-dev-agent-bridge-plan.md`](./2026-06-16-dev-agent-bridge-plan.md)，建议追加为 **P11(web-P1) / P12(web-P2)**。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-16 | Claude | 初版：web-P1 任务分解（jobs.jsonl 索引 / GET /v1/jobs / SSE 流 / webui 嵌入 / 前端脚手架与页面 / 验收）；web-P2 大纲 |
| v0.2 | 2026-06-16 | Claude | **web-P1 (T1–T8) 全部实现完成**（SUPMODE，cvt.2–9 closed）。后端 59a4ea8/ca02c8c/aa79c78/682344e；前端 5e2dd3c/0f4bdc2/268d880/f4dc93a/c7ae046。go 全量 `-race` 绿、curl E2E 9/9 通过。两处实施偏离已纳实现：① 日志走 SSE 单一来源（不用 logsTail+from=tailLen，规避 >256KB 缺口/重复）；② 不改根 `.gitignore`（用 `web/.gitignore`+`internal/webui/dist/.gitignore`）。剩浏览器手验清单与 web-P2（依赖主计划 P9）。 |

## 0. 前置（设计 v0.2 已确认）

- 鉴权：纯 Bearer 无状态（sessionStorage + `fetch`+ReadableStream 流式带头），无 cookie/session、无 `/v1/auth/*`。
- Job 列表/历史：追加写 `jobs.jsonl` 索引 + 内存实时态合并。
- 看板：web-P1 用 2~3s 轮询 `GET /v1/jobs`。
- 前端：Vue3 + Vite + TS + **pnpm**；裸写 + CSS（视觉 token 见设计 §8）；挂载根 `/`。
- 范围：**不**含 web 端提交 job、**不**含交互（web-P2）。

## 1. 当前代码基线（已核对）

| 包 | 关键符号 | web-P1 改动 |
|---|---|---|
| `internal/store` | `FileStore{base}`：`NewFileStore(base)`、`Dir/Ensure/WriteRequest/WriteResult/ReadResult/ReadLogTail` | 新增 `AppendIndex` / `ReadIndex`（`<base>/jobs.jsonl`） |
| `internal/job` | `Service`：`Submit`(L105)、`execute`(L200)、`finish`(L255 终态写 result.json)；持有 `cfg`/`projects` | `Submit`+`finish` 各 append 索引；新增 `ListJobs` |
| `internal/project` | `ResultBaseDir(cfg,projKey,proj)`(L90)、`JobResultDir`(L118) | 复用，索引路径 = `ResultBaseDir` 下 |
| `internal/httpapi` | `New(serverCfg,token,allowEmpty,jobs,projects,agents)`；`/v1` group（auth 中间件）；`Handler()` | 加 `GET /v1/jobs`、`GET /v1/jobs/{id}/stream`；group 外加静态 SPA handler |
| `internal/commands` | `serve.go` 装配 | 加 `--no-web`；config `web_enabled` |
| `internal/config` | `ServerConfig` | 加 `WebEnabled bool`（默认 true） |
| 根 | `Makefile`、`.gitignore` | 加 `make web` 目标；忽略 `web/dist`、`web/node_modules` |

## 2. 任务分解（按子阶段提交，SR1202）

### T1 — `jobs.jsonl` 索引（store + service 钩子）

**store**：`internal/store/filestore.go` 增

```go
const IndexFile = "jobs.jsonl"

// AppendIndex 追加一行 JSON（JobResult 快照）到 <base>/jobs.jsonl。
// 进程内 append；调用方(job.Service)持锁串行化，避免行交错。
func (s *FileStore) AppendIndex(rec any) error {
    f, err := os.OpenFile(filepath.Join(s.base, IndexFile),
        os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
    if err != nil { return err }
    defer f.Close()
    b, err := json.Marshal(rec)
    if err != nil { return err }
    _, err = f.Write(append(b, '\n'))
    return err
}

// ReadIndex 读全部行，解码为 []map 或 []JobResult(由调用方传 newElem)。
// 文件不存在返回空切片、无错。损坏行跳过(容错)。
func (s *FileStore) ReadIndex() ([]json.RawMessage, error) { /* 逐行 json，跳过坏行 */ }
```

**service**：`internal/job/service.go`

- `Submit` 内 `WriteRequest` 之后、起 goroutine 之前：`_ = st.AppendIndex(initialSnapshot)`（status=queued）。
- `finish`（L255）内 `WriteResult` 之后：`_ = entry.store.AppendIndex(terminalSnapshot)`。
- 串行：append 在 `s.mu` 持锁路径或 store 自身锁内，确保同进程不交错。

**测试**（`store` + `job`）：

- `AppendIndex` 两次后 `ReadIndex` 得 2 行；坏行被跳过。
- 一个 exec job 完整跑完后，`<base>/jobs.jsonl` 至少 2 行（create + terminal），同 id；折叠末行 status=done。
- 并发提交 N 个 job，jobs.jsonl 行数 = 2N、无交错坏行（`go test -race`）。

提交：`feat(agent-bridge): add jobs.jsonl index (store + service hooks)`

### T2 — `GET /v1/jobs`（列表：索引 + 内存合并）

**service**：`Service.ListJobs`

```go
type ListOpts struct { Project, Status string; Limit int }

// ListJobs 合并"索引历史"与"内存实时态"：
//  1) 遍历目标项目(opts.Project 或 cfg.Projects 全部)，对每个项目
//     用 project.ResultBaseDir -> store.NewFileStore -> ReadIndex，按 id 折叠(末行胜)。
//  2) 用内存注册表 entries 覆盖同 id(运行中以内存为准)，并补内存独有(尚未落终态)的。
//  3) 过滤 status、按 started_at 倒序、截断 limit(默认如 200)。
func (s *Service) ListJobs(opts ListOpts) ([]JobResult, error)
```

**handler**：`internal/httpapi/job_handler.go` 加 `handleListJobs`，注册到 `/v1` group `r.GET("/jobs", s.handleListJobs)`：解析 `?status=&project=&limit=` → `jobs.ListJobs` → `{"jobs":[...]}`（snake_case，错误 `{error,detail}`）。

**测试**（`httpapi` + `job`）：

- 跑 2 个 exec job（done/failed）后 `GET /v1/jobs` 返回 2 条、started_at 倒序、字段含 id/status/exit_code/project_key/agent。
- `?status=done` 只返回 done；`?project=self` 只返回该项目；`?limit=1` 截断。
- 重启场景：新建 Service（内存空）但索引在盘 → `ListJobs` 仍能从 jobs.jsonl 返回历史（done 态）。
- 认证：不带 Bearer → 401（沿用现有中间件）。

提交：`feat(agent-bridge): add GET /v1/jobs list endpoint`

### T3 — `GET /v1/jobs/{id}/stream`（SSE：log + status）

**handler**：`internal/httpapi/stream_handler.go`

- 响应头：`Content-Type: text/event-stream`、`Cache-Control: no-cache`、`Connection: keep-alive`；取 `http.Flusher`。
- 解析 `?from=<offset>`（stdout 起始字节，断线续传）与可选 `?stream=`（默认两路都推）。
- 解析 job：`jobs.Get(id)` 命中 → 取 `ResultDir` + 实时 status；未命中 → 经索引/`ReadResult` 拿历史 `ResultDir`（仅回放日志 + 末态 status 后关闭）。
- 循环（250ms tick，跨平台不依赖 inotify）：
  - 打开 `stdout.log`/`stderr.log`，`Seek` 到上次 offset，读新增字节 → 按行封 `event: log\ndata: {"stream":"stdout","seq":N,"text":"..."}\n\n`，flush。
  - 比较 `jobs.Get(id).Status` 变化 → `event: status\ndata: {...}`。
  - job 终态：补读剩余日志 + 发终态 `status` + `event: end` 后 return。
  - `ctx.Done()`（客户端断开）→ return。
- 复用 `ReadLogTail` 不合适（要增量）；新增内部 `tailFrom(path, offset)` 读 `[offset, EOF)`。

> **SSE 由 `fetch`+ReadableStream 消费**（前端带 Bearer 头），非 `EventSource`；但服务端就是标准 `text/event-stream`，curl 亦可验证。

**测试**（`httptest`）：

- 起一个 `sleep`/分段输出的 exec job，连 stream，断言收到 `log` 事件且文本含输出、最终收到终态 `status` + `end`。
- `?from=<len>` 从中途 offset 起，只收到其后增量。
- 已完成 job 连 stream：回放日志 + 立即终态 + end。
- 客户端取消 ctx，handler 不泄漏 goroutine（`-race` + 超时断言）。

提交：`feat(agent-bridge): add SSE job stream endpoint`

### T4 — webui 静态嵌入 + serve 挂载 + 开关

- `internal/config`：`ServerConfig` 加 `WebEnabled bool` (`yaml:"web_enabled"`)，loader 默认 `true`（未显式 false 即开）。
- `internal/webui/embed.go`：
  ```go
  //go:embed all:dist
  var dist embed.FS
  // Handler 返回静态文件服务；非资源路径回退 index.html(SPA)。dist 缺失(未构建)
  // 时返回最小占位页，保证裸 go build 不破(构建期 make web 生成 dist)。
  func Handler() (http.Handler, bool) { /* sub FS "dist"; ok=false 则占位 */ }
  ```
  - 占位：嵌入一个 `placeholder.html`（“运行 make web 构建前端”）兜底。
- `internal/httpapi/server.go`：`/v1` group 之外注册 SPA handler（**无 auth 中间件**：仅静态壳；API 仍 Bearer）。优先级：`/health`、`/v1/*` 先匹配，其余交给 webui handler；命中文件返回，否则回退 `index.html`。
- `internal/commands/serve.go`：加 `--no-web`（覆盖 `web_enabled=false`）；`WebEnabled && webui.ok` 时挂载，否则只 API。
- `New(...)` 增参或 server 内读 `serverCfg.WebEnabled` 决定是否挂 webui。

**测试**：

- `web_enabled=true` 且有 dist：`GET /` 返回 index.html（200，含占位或真实壳）；`GET /assets/x` 命中静态；未知前端路由 `GET /board` 回退 index.html。
- `--no-web` / `web_enabled=false`：`GET /` 404 或 405，`/v1/*` 正常。
- dist 缺失：占位页可返回、`go build` 不报错。

提交：`feat(agent-bridge): embed web console static assets in serve`

### T5 — 前端脚手架 + API client + 接入页

`web/`（pnpm + Vite + Vue3 + TS）：

```
web/
|- package.json        # vue ^3, vite, typescript, @vitejs/plugin-vue；scripts: dev/build
|- vite.config.ts      # base:'./', build.outDir:'dist', server.proxy /v1->127.0.0.1:8765(dev)
|- index.html
|- src/
|  |- main.ts, App.vue
|  |- api/client.ts    # fetch 封装：自动带 Authorization: Bearer <token from sessionStorage>
|  |                   #   listJobs/getJob/logsTail/cancel；streamJob(id,from,onEvent) 用 fetch+ReadableStream 解析 SSE
|  |- store/auth.ts    # token 存取 sessionStorage；401 时清并跳接入页
|  |- styles/tokens.css# 设计 §8 色板/字体(IBM Plex Mono/Sans)/圆角
|  |- router.ts        # /access /board /jobs/:id /projects /agents
|  `- views/Access.vue # 粘贴 token -> 试调 /v1/projects 验证 -> 存 sessionStorage -> 跳 /board
```

- `streamJob`：`fetch(url,{headers:{Authorization}})` → `res.body.getReader()` → 累积解析 `event:`/`data:` 帧 → 回调 `{type,data}`；断开/终态结束。
- client 统一处理 `{error,detail}` 与 401（跳接入页）。

**验收**：`pnpm -C web dev` 起开发服（proxy 到本地 serve），接入页粘贴 token 后能进 /board（空列表亦可）；401 时回接入页。

提交：`feat(web): scaffold vue console + api client + access page`

### T6 — Jobs Board + Job 详情（核心页）

- `views/Board.vue`：
  - 2~3s 轮询 `listJobs`（可见时轮询、隐藏暂停）。
  - 行 = 状态徽标(§8 语义色) + 短 id + project + agent + **活信号**（`components/Signal.vue`：running 按 `log` 速率/或简化按 updated 频率跳动的迷你波形；终态静态横线 + 耗时）。
  - 顶部 status 过滤；点击行进详情；左轨 Projects 点击 → `?project=` 过滤。
- `views/JobDetail.vue`：
  - 进入：`getJob`(头部) + `logsTail`(stdout/stderr 历史 ≤256KB)。
  - `streamJob(id, from=tailLen, onEvent)`：`log`→追加+自动滚底(上滚暂停+“N 行新”)；`status`→更新头部/耗时/徽标；终态→停 live 脉冲、隐藏 cancel。
  - **取消**：`cancel` → 乐观 `cancelling`，以 `status` 回填。
  - `components/LogTape.vue`（双栏终端式、等宽、live 脉冲）、`StatusBadge.vue`。

**验收**（浏览器手验，后端起 serve）：

- 提交一个 `exec -- sh -c 'for i in 1 2 3; do echo line$i; sleep 1; done'` job → Board 出现 running 活信号 → 进详情**实时逐行**看到 line1/2/3 → 终态 done、信号转静态。
- 取消一个长 job → 详情与 Board 都转 cancelled。
- 刷新页面后 Board 仍列出历史（来自 jobs.jsonl）。

提交：`feat(web): jobs board + job detail with live log tape`

### T7 — Projects / Agents 页 + 视觉与可达性

- `views/Projects.vue`：列表 + 详情（host_path、allowed agents/runners、allow_exec；可调 `project validate` 对应只读展示——web-P1 用现有 `GET /v1/projects/{key}`，validate 结果若无接口则仅展示配置）。
- `views/Agents.vue`：`GET /v1/agents` 列表 + detect 状态点（available/version/error）。
- 视觉落地 §8：`tokens.css` 全量；活信号/日志带定制；**质量地板**：响应式至移动端、键盘可见焦点、`prefers-reduced-motion`(波形冻结、关自动滚动惯性)、对比度达标。

**验收**：Projects/Agents 页正确渲染；`prefers-reduced-motion` 下无跳动；Tab 焦点可见。

提交：`feat(web): projects/agents views + visual tokens + a11y`

### T8 — 构建链 + 文档 + 整体验收

- `Makefile` 加：
  ```make
  ## web: build the web console (pnpm) into web/dist
  web:
      pnpm -C web install --frozen-lockfile || pnpm -C web install
      pnpm -C web build
  ```
  `build` 依赖说明：构建带 web 时先 `make web` 再 `make build`（dist 缺失走占位页，CI 显式 `make web build`）。
- `.gitignore` 加 `web/dist/`、`web/node_modules/`；`web/`(源) 入仓。
- README 加“Web 控制台”段：`make web && make build` → `serve` → 浏览器开 `http://<addr>/` → 粘贴 token；`--no-web` 关闭。
- 整体验收（§3 矩阵）跑通。

提交：`build(web): make web target + docs + gitignore`

## 3. 验收矩阵（web-P1 完成判据）

| 场景 | 操作 | 期望 |
|---|---|---|
| 列表 | `GET /v1/jobs` | 历史(jsonl)+运行中(内存)合并、倒序、过滤生效 |
| 重启恢复 | 重启 serve 后开 Board | 历史 job 仍在（来自 jobs.jsonl） |
| 实时日志 | 进 running job 详情 | stdout/stderr 逐行实时跟随、自动滚底 |
| 取消 | 详情点 cancel | Board+详情转 cancelled |
| 鉴权 | 不带/错 token | 接入页/401；token 仅在 sessionStorage |
| 无 web | `serve --no-web` | `/` 不可达、`/v1/*` 正常 |
| 构建兜底 | 未 `make web` 直接 `go build` | 通过；`/` 返回占位页 |
| a11y | reduced-motion / 键盘 | 无跳动、焦点可见 |

## 4. 测试策略

- 后端：`store`(AppendIndex/ReadIndex 容错)、`job`(ListJobs 合并/折叠、并发 -race)、`httpapi`(list 过滤/认证、stream 事件/续传/取消不泄漏) 单测。
- 前端：web-P1 以手验为主（脚手架轻量）；可加 `api/client.ts` 的 SSE 帧解析纯函数单测（vitest 可选，不强制）。
- 集成：exec agent 跑分段输出 job，端到端验证实时日志（后端 httptest + 手验浏览器）。

## 5. 风险

| 风险 | 处理 |
|---|---|
| SSE goroutine 泄漏（客户端断开） | 监听 `ctx.Done()`，终态/断开即 return；`-race`+超时测试 |
| jobs.jsonl 无限增长 | web-P1 先不限，README 注 TODO；后续轮转/截断 |
| 跨项目读多份 jsonl 慢 | 项目数有限；必要时缓存 mtime 增量读 |
| dist 缺失致 go build 破 | `//go:embed all:dist` + 占位文件兜底，`make web` 生成真 dist |
| 前端 SSE 解析 `--` 分帧边界 | reader 累积缓冲、按 `\n\n` 切帧，跨 chunk 安全 |
| node/pnpm 环境 | CI/本机需 node 20+；Makefile `make web` 统一入口 |

## 6. web-P2 大纲（依赖主 plan P9，暂不细化）

- 后端（= P9）：`interactions.jsonl` 写入、`pending_interaction` 状态、`GET /v1/jobs/{id}/interactions` + `POST .../answer`、Agent 发起交互机制（方向 A）；`stream` 增 `interaction` 事件。
- 前端：交互卡（question/choice/confirmation）、SSE `interaction` 渲染、Board ⚠ 态、排队作答。
- P9 落地后据其接口细化本节为完整任务分解。

## 7. 交付顺序

T1→T2→T3（后端三件，可独立验证）→ T4（嵌入）→ T5→T6→T7（前端）→ T8（构建+文档+整体验收）。每个 T 子阶段绿灯即提交。
