# dev-agent-bridge Web 控制台设计

> 本文是 [`2026-06-16-dev-agent-bridge-design.md`](./2026-06-16-dev-agent-bridge-design.md) 的**子设计文档**，只描述 `serve` 内嵌的 Web 控制台模块。主设计已定的事实（CLI、配置、Job 模型、`/v1` API、安全口径、P9 运行中交互后端协议）不再重复，按编号引用。
>
> 关联：主设计 §10（HTTP API 草案）、§12.4（运行中 Agent 双向交互）、§13（安全设计）；实施计划 [`../plans/2026-06-16-dev-agent-bridge-plan.md`](../plans/2026-06-16-dev-agent-bridge-plan.md) §9 P4/P5/P9。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-16 | Claude | 初版：Web 控制台范围、信息架构、关键交互流程、视觉方向、新增 API（job list / SSE 流 / cookie 鉴权）、数据模型、安全、两步实施（web-P1 只读+日志+取消 / web-P2 联合 P9 交互） |
| v0.2 | 2026-06-16 | Claude | 设计评审收敛：①鉴权改**纯 Bearer 无状态**（token 存 sessionStorage，流式用 `fetch`+ReadableStream 带头），删除 cookie/session 与 `/v1/auth/*`；②Job 列表/历史改用**追加写 `jobs.jsonl` 索引**（替代扫盘）；③看板 web-P1 定为 2~3s 轮询；④UI 定为裸写+CSS；⑤挂载根 `/`、包管理器 pnpm；§14 由待确认转为已确认 |

## 1. 背景

MVP（主设计 §15）已交付 `agent-bridge serve` 的 HTTP 控制面与 `job` CLI，但观察一个异步 job 只能靠 `job show` 轮询 + `job logs` 拉取 256KB 快照，**看不到运行中实时输出**，更无法响应 Agent 运行中提出的问题（P9）。Web 控制台把这些能力变成一个**随 `serve` 自带、零额外部署**的浏览器界面：开发者打开页面即可纵览所有项目/Agent/Job、实时跟随某个 job 的 stdout/stderr、在 Agent 卡住提问时当场作答。

## 2. 名词

| 名词 | 含义 |
|---|---|
| 控制台 / Console | 本文设计的 Web 界面，由 `serve` 内嵌静态资源提供 |
| 信号板 / Board | Job 列表主视图，每个运行中 job 显示按日志速率跳动的"活信号"行 |
| 日志带 / Tape | Job 详情里 stdout/stderr 的实时终端式滚动视图 |
| 交互卡 / Interaction | Agent 运行中发起的待答事件（question/choice/confirmation，见主设计 §12.4） |
| SSE | Server-Sent Events，服务端单向推流（`text/event-stream`），用于实时日志与事件 |

## 3. 范围（已确认）

**做**（用户确认）：

- 只读监控：项目、Agent（含 detect 状态）、Job 列表与详情。
- 实时日志：running job 的 stdout/stderr 实时跟随。
- 运行中双向交互：展示 `pending_interaction`、提交 answer 续跑（依赖 P9 后端）。
- 控制动作：**仅取消**运行中 job。

**不做**（已确认）：

- **不在 Web 端提交新 job**（创建仍走 CLI / MCP / HTTP `POST /v1/jobs`）。Web 是观测+应答面，不是发起面。
- 不做多用户/账号体系、不做权限分级（本地开发工具，单 token 准入，见 §9）。
- 不做日志全文检索/持久化分析（日志真源是 `result_dir` 文件，见主设计 §9.3）。

**技术选型**（已确认）：

- 前端 **Vue 3 + Vite + TypeScript**（与团队 Vue 管理端项目 一致），构建产物 `go:embed` 进二进制。
- 实时机制 **SSE**（非 WebSocket）。
- 浏览器鉴权 **纯 Bearer 无状态**：token 存 `sessionStorage`，含流式用 `fetch`+`Authorization` 头（不用 cookie/session，沿用现有中间件）。

**实施分两步**（已确认）：设计覆盖全量；先交付 **web-P1**（只读+实时日志+取消，立即可用），再联合 **P9 后端**交付 **web-P2**（双向交互）。见 §10。

## 4. 总体思路

```txt
                    单一 agent-bridge 二进制
  浏览器 ──HTTP/SSE──▶ [ rux 路由 ]
                         ├── /            静态资源 (go:embed web/dist, SPA)
                         ├── /v1/...       既有 JSON API + 新增 list/stream/auth
                         └── /v1/jobs/{id}/stream   SSE: 日志行 + 状态变更 + 交互事件
                              │
                         [ job.Service ]──读/写──▶ <result_dir>/<job_id>/
                                                    stdout.log stderr.log
                                                    result.json interactions.jsonl
```

一句话：控制台不引入新的状态真源，**所有数据来自 `job.Service` 内存快照 + `result_dir` 文件**（主设计 §9.3）。Web 只是把"轮询 + 拉快照"换成"一条 SSE 长连接实时推送"，把 P9 的"轮询 pending + answer"换成"SSE 推 pending + POST answer"。后端协议与 CLI/MCP 完全同源（主设计 §12.4 方向 A）。

## 5. 架构

### 5.1 静态资源内嵌与挂载

- 前端独立子项目 `tools/dev-agent-bridge/web/`（Vite），`pnpm build` 产出 `web/dist/`。
- Go 侧 `internal/webui/embed.go` 用 `//go:embed dist/*` 嵌入（构建前需先 `vite build`；提供 `make web` 与 `make build` 串联，`dist` 缺失时回退到一个最小占位页，避免裸 `go build` 失败）。
- rux 路由优先级：`/health`、`/v1/*` 先匹配；其余路径交给 SPA handler（命中静态文件则返回，否则回退 `index.html` 支持前端路由）。同源提供，**无 CORS**。
- 开关：`server.web_enabled`（默认 `true`）/ `serve --no-web` 可关闭，仅留纯 API。

### 5.2 实时通道：单连 SSE

详情页只开**一条** `GET /v1/jobs/{id}/stream` 流（用 `fetch` + `ReadableStream` 消费，以便带 `Authorization: Bearer` 头；**不用**原生 `EventSource`——它无法自带请求头），服务端多路复用三类事件，避免多连接/多轮询：

| event | data | 用途 |
|---|---|---|
| `log` | `{stream:"stdout"\|"stderr", seq, text}` | 增量日志行（按字节 offset 续传，断线用 `?from=<offset>` 重连） |
| `status` | `{status, exit_code, ended_at}` | 状态机变更（queued→running→done/failed/timeout/cancelled） |
| `interaction` | `{action:"open"\|"answered"\|"cancelled", interaction}` | P9 交互事件（web-P2 启用） |

- 服务端实现：打开 `stdout.log`/`stderr.log`，`Seek` 到末尾或客户端 offset，定时/事件驱动读取新增字节，按行封 `log` 事件；job 终态后补发 `status` 并关闭流。**不依赖文件系统 inotify**（跨 Windows/Linux），用短周期轮读文件大小变化（如 250ms）+ job 内存状态联动，简单可移植。
- 看板页 web-P1 **用 2~3s 轮询 `GET /v1/jobs`**（看板非高频，最简）；该接口由 `jobs.jsonl` 索引 + 内存实时态合并（见 §10）。量大/体验不够再上看板级 `GET /v1/jobs/stream` SSE。

### 5.3 与 P9 后端的关系（SR1401 二级问题）

Web 的"交互卡"只是 P9 后端的前端面。**P9 后端不落地，则 web 的交互功能无数据可显**。因此：

- web-P1 完全不碰交互，仅消费已有 `JobResult` + 日志文件。
- web-P2 与 P9 后端**同期**：P9 落地 `interactions.jsonl` 写入、`pending_interaction` 状态、`GET /v1/jobs/{id}/interactions`、`POST .../answer`（主设计 §12.4 已定义），并明确 Agent 运行中**如何发起**交互（主设计 §12.4 方向 A：wrapper / MCP client 写入；stdout JSON marker 需显式启用）。Web 端在 `stream` 上收 `interaction` 事件后渲染交互卡。

## 6. 信息架构与页面

```txt
┌───────────────────────────────────────────────────────────────┐
│  AGENT-BRIDGE  ▸ console            host:port   ● connected   ⏻ │  顶栏:连接态/登出
├──────────────┬────────────────────────────────────────────────┤
│ PROJECTS     │  JOBS BOARD                          [status ▾] │
│  ▸ self      │  ┌──────────────────────────────────────────┐  │
│  ▸ other     │  │ ●run  j-…0db0  self  codex  ▁▃▆▇▅▂ 12s   │  │  ← 活信号行(签名)
│ AGENTS       │  │ ✓done j-…f2eb  self  exec   ────── 0.4s  │  │
│  ▸ codex  ◌  │  │ ✗fail j-…7a13  other exec   ────── 3s    │  │
│  ▸ claude ●  │  │ ◔que  j-…9c02  self  claude  ·            │  │
│  ▸ exec   ●  │  └──────────────────────────────────────────┘  │
└──────────────┴────────────────────────────────────────────────┘
```

- **左轨**：Projects（点击过滤看板 + 进项目详情）、Agents（detect 状态点：● available / ◌ unavailable）。
- **主区 = Jobs Board**：状态过滤、自动刷新；每行 = 状态徽标 + 短 id + 项目 + agent + **活信号**（running 显示日志速率跳动的迷你波形；终态显示静态横线 + 耗时）。
- **Job 详情**（见 §7.1 线框）：头部元信息 + 双栏日志带 + 取消 + 交互卡。
- **Project 详情**：配置（host_path、allowed agents/runners、allow_exec）、`validate` 结果逐项。
- **Agent 详情**：type / command / args 模板 / detect（available/version/error）。

## 7. 关键交互流程

### 7.1 Job 详情：实时日志带

```txt
┌ JOB j-20260616-015748-08940db0 ───────────────── ✗ cancel ─┐
│ ● running   exec · local · self     cwd: .                  │
│ started 14:57:48   ⏱ 00:12   timeout 30s                    │
├──────────────────────── stdout ───────┬──────── stderr ─────┤
│ go version go1.25.10 linux/amd64      │                     │
│ building ./...                        │ war: deprecated …   │
│ ▏                              ● live │                     │  ← 光标+live脉冲
└───────────────────────────────────────┴─────────────────────┘
```

1. 进入详情 → 一次性 `GET /v1/jobs/{id}`（头部）+ `GET .../logs/stdout|stderr`（拉已有 tail，≤256KB）渲染历史。
2. 用 `fetch('/v1/jobs/{id}/stream', {headers:{Authorization}})` + `ReadableStream` 消费：`log` 事件追加到对应栏并自动滚到底；用户上滚则暂停自动滚动并显示"↓ 回到底部 / N 行新"。
3. `status` 事件更新头部徽标/耗时；终态后流关闭、停止"live"脉冲、cancel 按钮置灰。
4. **取消**：`POST /v1/jobs/{id}/cancel` → 乐观置 `cancelling`，以 `status` 事件回填 `cancelled`（主设计 §6.2 状态机；已完成 job 取消为稳定 no-op，按钮在终态隐藏）。

### 7.2 运行中双向交互（web-P2）

```txt
┌ ⚠ Agent 需要你的确认 ─────────────────────────────────────┐
│ 检测到 2 处将被覆盖的文件，是否继续写入？                  │
│   ( 继续覆盖 )   ( 跳过这些文件 )   [ 自定义回答…________ ] │
└────────────────────────────────────────────────────────────┘
```

1. job 进入 `pending_interaction`，`stream` 推 `interaction{action:"open"}`。
2. 控制台在详情顶部弹出交互卡 + 看板该行徽标转 ⚠：`question`→文本框；`choice`→选项按钮（来自 `interaction.options`）；`confirmation`→确认/取消。
3. 提交 `POST /v1/jobs/{id}/interactions/{iid}/answer`（主设计 §12.4）→ 卡片置"已提交"，`interaction{action:"answered"}` 回填、job 续跑、日志带继续推进。
4. 多个 pending 按 `created_at` 顺序排队，逐个作答。

### 7.3 鉴权（纯 Bearer 无状态）

1. 无有效 token → 前端跳"接入"页粘贴 token，存 `sessionStorage`（非 `localStorage`，关页即失效）。
2. 所有请求统一带 `Authorization: Bearer <token>`——**含流式**：日志/事件流用 `fetch(stream, {headers:{Authorization}})` + `ReadableStream` 消费（不用原生 `EventSource`，因其无法自带请求头）。
3. 服务端**复用现有 token 中间件**，**不引入 cookie/session**、**无 `/v1/auth/*` 端点**；CLI/curl 与 Web 同一鉴权路径。
4. 空 token 模式（`allow_empty_token`）免接入页。token 仅在浏览器 `sessionStorage`/内存，不落 cookie、不进 URL/日志。

## 8. 视觉方向

> 主体世界：AI 编码 Agent 的异步作业**调度与信号回传**。控制台读起来应像一块**运维调度信号板**叠加**示波器**，而非通用后台表格。刻意避开 AI 默认的"纯黑底 + 单一酸绿"终端套路：底色用蓝灰调度板而非纯黑，状态色是**与 job 状态机语义绑定的一组色**（不是装饰性单点缀色）。

**色板（4–6 named）**：

| token | hex | 用途 |
|---|---|---|
| `--ink` | `#0E1A24` | 调度板底（深蓝灰，非纯黑） |
| `--panel` | `#15252F` | 抬升面板/卡片 |
| `--line` | `#2A3D49` | 发丝分隔线/网格 |
| `--paper` | `#E8E2D4` | 主前景文本（暖纸白，非纯白） |
| `--run` `#E0A24A` / `--done` `#5BA66E` / `--fail` `#C8553D` / `--queue` `#6B8A99` | — | **状态机语义色**（in-flight 琥珀 / 完成 哑绿 / 失败 砖红 / 排队 石板） |
| `--phosphor` | `#4FB0C6` | 交互/链接强调（CRT 磷光青，克制使用） |

**字体（2 角色，同族克制）**：

- 数据/日志/标签/job-id/状态：**IBM Plex Mono**——主体的母语（终端、ID、数字），日志带与信号板的统一声音。
- UI 正文/说明：**IBM Plex Sans**——同超家族，UI chrome 可读、不喧宾夺主。
- 不引入第三款展示体；个性放在**布局与信号**而非字体花式（Chanel 减一件原则）。

**结构即信息**：状态徽标与色彩**编码 job 状态机真值**，不是装饰；左轨的"项目/Agent"是被调度的"线路"。**不**使用 `01/02/03` 顺序编号（job 是并发集合不是有序流程，编号会误导）。

**签名元素**：**活信号行**——running job 的迷你波形由其**日志输出速率（行/秒）**驱动实时跳动，让"哪个 job 真在动"一眼可辨；这是页面唯一的大胆点，其余保持安静、发丝线、低圆角（4px）。

**动效（克制）**：活信号波形、日志带底部 live 脉冲、交互卡一次性滑入。`prefers-reduced-motion` 下波形冻结为静态条、关闭自动滚动惯性。质量地板：响应式至移动端、键盘可见焦点、对比度达标。

## 9. 新增 / 复用 API

复用（主设计 §10，已实现）：`GET /v1/projects[/{key}]`、`GET /v1/agents`、`GET /v1/jobs/{id}`、`GET /v1/jobs/{id}/logs/stdout|stderr`、`POST /v1/jobs/{id}/cancel`。

**新增**（本设计引入，落到后端）：

| 方法 | 路径 | 阶段 | 说明 |
|---|---|---|---|
| GET | `/v1/jobs` | web-P1 | **Job 列表**（当前缺）。来源 `jobs.jsonl` 索引（折叠末行）+ 内存实时态合并（见 §10）；支持 `?status=&project=&limit=`，按 `started_at` 倒序 |
| GET | `/v1/jobs/{id}/stream` | web-P1 | 单连 SSE：`log`/`status` 事件（web-P2 增 `interaction`）；`?from=<offset>` 续传；**由 `fetch`+ReadableStream 消费**（带 Bearer 头） |
| GET | `/v1/jobs/stream` | web-P1（可选） | 看板级轻量 SSE（仅 job 状态/活动计数）；web-P1 先用 2~3s 轮询 `GET /v1/jobs` 替代 |
| GET | `/v1/jobs/{id}/interactions` | web-P2 | 主设计 §12.4（P9 落地） |
| POST | `/v1/jobs/{id}/interactions/{iid}/answer` | web-P2 | 主设计 §12.4（P9 落地） |

鉴权：全部沿用现有 `Authorization: Bearer` 中间件，**不新增 `/v1/auth/*`**（纯 Bearer，见 §7.3/§11）。错误结构沿用主设计 `{"error","detail"}`。SSE 事件 data 字段 **snake_case**，与 JSON API 一致。

> `GET /v1/jobs` 列表能力对 CLI（`job list`）同样有用——属顺带补齐的后端能力，不止服务 Web。

## 10. 数据模型与 `jobs.jsonl` 索引

唯一新增的持久件是一个**追加写的 job 索引** `jobs.jsonl`（其余仍是既有 `result.json` / 日志 / `interactions.jsonl`）：

- **写**：`job.Service` 在 job **创建**、及到达**终态**时各 append 一行 `JobResult` 快照（JSON Lines）。进程内串行化（mutex），单行原子追加。
- **读**：`GET /v1/jobs` 读 `jobs.jsonl`、**按 `id` 折叠取末行**（last-wins）得历史列表，再与内存注册表的运行中实时态合并（运行中以内存为准）。重启后历史即来自此文件，**免扫 N 个 result_dir**。
- **位置**（随主设计 §9.3 存储模式）：默认各项目 `<host_path>/<exchange>/<result_subdir>/jobs.jsonl`（看板跨已登记项目各读一份）；设 `storage.root` 时单文件 `<storage.root>/jobs.jsonl`（行内含 `project_key`）。
- **定位**：与既有 `message` / `interactions.jsonl` 同属"jsonl 被动索引"，**不作状态真源**（真源仍是 `result.json` + 进程态）。增长上限/轮转 web-P1 先不限（TODO，见 §14）。

前端类型派生自既有模型：

```ts
// 派生自 JobResult(主设计 §6.2) + 列表/实时附加字段
interface JobRow {
  id; project_key; agent; runner;
  status: 'queued'|'running'|'done'|'failed'|'cancelled'|'timeout'|'pending_interaction';
  exit_code; started_at; ended_at?; title?;
  // 列表/看板附加(后端计算或前端累计)
  activity_rate?: number;   // 近窗口 行/秒, 驱动活信号
  result_dir;
}
// 派生自 Interaction(主设计 §12.4)
interface Interaction { id; job_id; type:'question'|'choice'|'confirmation'; prompt; options?; status; answer?; created_at; answered_at? }
```

`pending_interaction` 作为前端展示态并入 `status`（主设计 §12.4 已预留该状态值）。

## 11. 安全

继承主设计 §13，针对浏览器面补充：

- **同源 + 纯 Bearer**：静态与 API 同源、同一 token 准入；**无 cookie/session**，token 仅存浏览器 `sessionStorage`、随所有请求（含流式 `fetch`）走 `Authorization` 头，不落 cookie、不进 URL/日志。同源同进程，故 XSS 面与服务本身一致（本地开发工具可接受；呼应主设计 §13）。
- **只读 + 受限控制**：Web 暴露的写操作仅 `cancel` 与 `interaction answer`；**不提供 job 提交**，攻击面小于 `POST /v1/jobs`（仍仅 CLI/受信内网可达）。
- **空 token**：仅 `allow_empty_token` 时免接入页；默认拒绝（主设计 §13、计划 §11）。
- **SSE 资源**：每连接限频读文件、限最大缓冲；日志历史拉取沿用 256KB tail 上限（计划 §11），实时仅推增量。
- **监听地址**：默认 `0.0.0.0:8765`（容器经 `host.docker.internal` 可达），靠 token + 内网准入；纯本地可收紧 `127.0.0.1`（主设计 §13）。
- 日志/事件中不回传 token、AK/SK、appSecret。

## 12. 部署

- 仍是单一二进制单进程（主设计 §13 / 计划 §11），Web 随 `serve` 提供，无独立部署单元。
- 构建链：`make web`（`pnpm -C web i && pnpm -C web build`）→ `make build`（`go build` 嵌入 `web/dist`）。CI/本机需 node 20+；`dist` 缺失回退占位页保证 `go build` 不破。
- `web/` 加入仓库；`web/dist/`、`web/node_modules/` 入 `.gitignore`（产物在构建期生成/嵌入）。

## 13. 实施分期（衔接主计划 §9）

> 本设计审核通过后另出实施计划（SR1107）；以下为分期总纲，建议在主 plan 追加为 **P11 / P12**。

**web-P1 — 只读监控 + 实时日志 + 取消**（立即可用，不依赖 P9）

- 后端：`GET /v1/jobs`（list，源自 `jobs.jsonl` 索引 + 内存实时态）、`jobs.jsonl` 写入（创建/终态 append）、`GET /v1/jobs/{id}/stream`（SSE：log+status，`fetch`+ReadableStream 消费）、`internal/webui` 静态嵌入 + serve 挂载 + `--no-web`/`web_enabled`。**无 `/v1/auth/*`**（纯 Bearer）。
- 前端：`web/` Vite 脚手架；接入页、Jobs Board（活信号）、Job 详情（双栏日志带 + 取消）、Projects/Agents 视图；视觉 token 落地（§8）。
- 验收：serve 起后浏览器可登入、看板自动刷新、进详情实时跟随 exec job 输出、取消生效；`prefers-reduced-motion`/键盘焦点达标。

**web-P2 — 运行中双向交互**（联合 **P9 后端**）

- 后端（= 主计划 P9）：`interactions.jsonl` 写入、`pending_interaction` 状态、`GET interactions` + `POST answer`、Agent 发起交互机制（方向 A）；`stream` 增 `interaction` 事件。
- 前端：交互卡（question/choice/confirmation）、SSE `interaction` 渲染、看板 ⚠ 态、排队作答。
- 验收：wrapper/MCP 触发的 `question` 在控制台弹卡，作答后 job 续跑完成；与 CLI/MCP 作答路径行为一致。

## 14. 已确认事项（设计评审 v0.2）

| # | 决策点 | 选择 |
|---|---|---|
| 1 | 静态挂载根 | **`/`**（控制台占根，API 在 `/v1`） |
| 2 | 看板实时 | **web-P1 用 2~3s 轮询 `GET /v1/jobs`**；详情页日志走单连 SSE。量大再上看板级 SSE |
| 3 | Job 历史/列表来源 | **追加写 `jobs.jsonl` 索引**（创建+终态各 append、读时按 id 折叠末行胜）+ 内存实时态合并；重启可恢复、免扫盘（§10） |
| 4 | 前端 UI | **裸写 + 少量 CSS**，贴合 §8 视觉方向，无 UI 库主题改写成本 |
| 5 | 鉴权 | **纯 Bearer 无状态**：token 存 `sessionStorage`，含流式用 `fetch`+`Authorization` 头；无 cookie/session、无 `/v1/auth/*`（§7.3/§11） |
| 6 | 包管理器 | **pnpm**（Vue 管理端项目 三种 lockfile 并存无统一，新 `web/` 子项目用 pnpm） |

> 留待实施计划细化的小点：`jobs.jsonl` 增长上限/轮转（web-P1 先不限，注 TODO）；活信号 `activity_rate` 由前端按 `log` 事件累计（倾向，零后端改动）还是后端计算。

## 15. 结论

Web 控制台以"零额外部署、单连 SSE（`fetch` 流式）、纯 Bearer 无状态"把现有 `/v1` 控制面升级为可实时观测与应答的浏览器界面，**仅新增一个 `jobs.jsonl` 轻索引**（job 列表/历史），不引入新状态真源、协议与 CLI/MCP 同源。先交付 web-P1（只读+实时日志+取消）立即提升可用性，再随 P9 后端交付 web-P2 双向交互。视觉上以"调度信号板 + 活信号行"建立区别于通用后台的辨识度。据本设计另出 web-P1/web-P2 实施计划（追加主 plan P11/P12）。
