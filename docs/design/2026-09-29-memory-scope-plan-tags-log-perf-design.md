# 全局/项目记忆、plan 标签与状态切换、命令文案清理、job 详情日志性能

> 状态：Approved（文档 identity：Draft 0.1；用户 2026-09-29 批准）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-29 | Claude | 用户提出的五项：memory 作用域、plans 状态切换、plan tags、去掉命令上的计划编号、job 详情卡顿 |

## 背景与目标

1. `gofer memory` 目前只能写进某个仓库的 `.gofer/tracker/memories.jsonl`，跨电脑使用需要同一个仓库再 `repo sync`；与仓库无关的长期偏好（例如给 agent 的通用约定）没有合适的位置。用户希望 `memory set` 支持 `--project` 与 `--global`，存到 server，任何电脑可用，并在会话开场自动注入，避免每次提醒。
2. Plans 页的状态筛选是下拉框，切换不便。
3. `plan create` 不能打标签，后期难以搜索 plan。
4. 多个命令的帮助文字里带有实施计划编号（如 `(E5)`、`(E35)`、`PLAN-02:`、`MCP-05`），对使用者没有意义。
5. 进入运行中的 agent job 详情页非常卡，stderr 很长时尤甚。

## 范围与非目标

- 范围：M1–M5。
- 非目标：改变仓库 tracker 的同步语义（P4）；记忆的权限模型之外的多用户隔离；日志存储格式。

## 已确认事实

- 仓库记忆：`internal/tracker`（本地 jsonl 真源）+ server 镜像 `tracker_memories`（按 `tracker_id` + key，含墓碑）。
- prime：`gofer repo prime [--hook-json]` 只在有 tracker 的仓库里工作；SessionStart hook 调用它。
- job 详情日志：`web/src/views/JobDetail.vue` 经 SSE 逐帧 `appendCapped`（每路保留最近 2 MB）；`components/LogTape.vue` 对整段文本 `renderAnsi` 后 `v-html` 整块替换。每帧开销与缓冲总长成正比，是卡顿主因。
- plan 列表已有状态/项目筛选；plan 模型无 tags 字段。

## 总体方案

### M1 全局 / 项目记忆

- 存储：server 新表（或复用镜像表加作用域列）`scoped_memories {scope, scope_key, key, content, tags, updated_at, updated_by, deleted}`，`scope ∈ {global, project}`，`scope_key` 为空（global）或项目 key。与仓库 tracker 记忆互不影响。
- CLI：`gofer memory set|ls|show|rm` 增加 `--global` 与 `--project <key>`（二者互斥；都不带时保持现状写仓库 tracker）。这两种作用域**只存在 server**，需要能连上 server；连不上时报错（不做离线缓存）。
- HTTP / MCP：对应的增删查接口；MCP 工具让 agent 也能读写（写入权限同 issue 镜像：user caller 可写，job caller 只读）。
- web：Issues 页的 Memories 标签增加作用域切换：仓库 / 项目 / 全局。
- 会话开场注入：`gofer repo prime` 增加「全局记忆」与「项目记忆」两段；**没有 tracker 的目录也能输出这两段**（只是没有仓库段）。按 `--agent <name>` 过滤：带 `agent:<name>` 形式标签（或决策 2 选定的标签规则）的记忆只注入给对应 agent。计入 prime 的 8 KiB 上限（超出截断并注明），server 不可达静默省略。
- SessionStart hook 模板改为对所有目录调用 prime（带 `--agent`），不再要求仓库有 tracker。

### M2 Plans 页状态切换按钮

- 状态筛选改为一排切换按钮（chip，复用 Board / Issues 的状态 chip 样式与配色），可多选，默认选中 open；保留"全部"快捷。项目筛选不变。

### M3 plan 标签

- plan 模型增加 `tags`；`gofer plan create --tags a,b`、`plan set --tags` / `--untag`；`plan list --tag T`（可重复，取交集）与关键字搜索；HTTP / MCP 同步；web Plans 列表显示标签、可按标签筛选，PlanDetail 可编辑标签。

### M4 去掉命令文案里的计划编号

- 清理所有 CLI 命令/选项帮助（`Desc`、选项说明）、HTTP 错误文案、web 可见文案中的实施计划编号（如 `E5`、`E35`、`PLAN-02`、`MCP-05`、`SESS-01`、`TUN-03`、`JOB-11` 等）。代码注释与设计文档里的编号保留（便于追溯）。
- 加一个测试扫描所有命令的帮助文本，禁止再出现 `\b[A-Z]{1,5}-?\d{1,3}\b` 形式的计划编号（给出白名单以免误伤如 `UTF-8`）。

### M5 job 详情日志性能

- 合并渲染：SSE 日志帧先入缓冲，按 `requestAnimationFrame`（或约 200ms）批量合并后再更新。
- 增量渲染：LogTape 按块/按行维护已渲染的 HTML 片段，新数据只渲染新增部分并追加到 DOM，不再对全文 `renderAnsi` + 整块替换；ANSI 状态跨块延续。
- DOM 行数上限：默认保留最近约 5000 行（可配），更早的走现有「加载更早」分页。
- 只渲染可见标签：未激活的标签只累积缓冲、不渲染；agent job 默认显示 stdout（结果/汇报），stderr 在切换时再渲染。
- 验收：构造 50k 行 stderr 的运行中 job，页面首次打开与持续追加时主线程无长任务（>200ms），滚动流畅；给出前后对比数据。

### M6 超时只从 job 真正开跑后计时（用户 2026-09-29 追加）

- 原则：排队时间（并发配额、目录锁、worker 本地排队）一律不计入 `timeout_sec`，只从进程真正启动后计时。
- 现状：本机执行已满足（`internal/job/execute.go` 在所有排队之后才建 `runCtx`）。**派给 worker 的 job 不满足**：server 端在派发时就开始 `runCtx` 计时，而 worker 收到后还会在本地按 `max_concurrent` 排队，这段时间被算进 server 的超时；job 在页面上也显示为 running。
- 修复：远程 runner 由 worker 在本地真正开跑时回报"started"；server 端对远程 job 从收到 started 才开始计时（派发到 started 之间不计时，但仍可取消），此前 job 状态保持 queued（可标注"worker 排队中"）；worker 端继续按本地开跑计时并执行超时。started_at 以真正开跑时间为准。
- 验收：`TestRemoteJobQueueTimeNotCounted`（worker max_concurrent=1，第二个 job 排队时间超过其 timeout 仍能正常跑完）；本机路径已有 `TestDirLockWaitDoesNotConsumeTimeout` 保持通过。

## 实施分期

| 期 | 内容 | 验收 |
|---|---|---|
| V1 | M4、M2、M3 | 帮助文本扫描测试；plan tags 读写/筛选测试；web 截图 |
| V2 | M5 | 组件测试 + 大日志真实页面对比（容器 agent-browser 性能数据） |
| V2 | M6（与 M5 同期） | 见 M6 验收 |
| V3 | M1 | 作用域记忆 CRUD/权限/prime 注入测试；真实进程冒烟；随后把监督者的记忆迁入 `--global --tag claude` |

## 决策（已批准 2026-09-29）

1. M1 全局/项目记忆**只存 server**，不做离线缓存；CLI 连不上 server 报错，prime 连不上静默省略。
2. M1 注入规则：带 `agent:<名>` 标签的记忆只注入给该 agent（如 `agent:claude`）；没有 `agent:` 标签的注入给所有 agent。
3. M4 同时清理 web 页面可见文案中的计划编号；代码注释与设计文档保留。
4. 按 V1 → V3 派发（V1：M4 + M2 + M3；V2：M5 + M6；V3：M1 并迁入监督者记忆）。
5. M6（2026-09-29 用户追加）：排队时间不计入超时，远程 job 从 worker 真正开跑才计时。

## 结论与人工计划 Gate

批准后按 V1 → V3 派发；每期容器验收、截图、job 评论留痕，发版时前端 + 主机 server + 容器 CLI 同步升级。
