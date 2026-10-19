<!-- template_id: plan; template_version: 1.2.0 -->
# 工作台会话输入框支持图片 实施计划

> 状态：Draft 0.1 / 待用户批准
> issue：gofer-ha0x

- 思考模式：rigorous。
- 核心目标：落地设计 `docs/design/2026-10-19-workbench-image-attachments-design.md`（Draft 0.1，已批准）：附件上传与存储、ACP 图片内容块（本机与 worker）、文件送达（xfer 回报绝对路径、执行机清理）、workbench 发送与历史、web 交互与手机压缩。
- scope freeze：以设计「范围与非目标」为准；新建 job / 信件 / todo 的附件、非图片文件、CLI 发图另开 issue。
- review budget：本计划最多一轮独立评审；评审后修订由作者自查。
- 停止条件：用户具名批准本计划，或用户要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-19 | Claude（监督会话） | 初稿：P1 附件存储与接口、P2 ACP 图片内容块与 worker 协议、P3 xfer 回报绝对路径与执行机清理、P4 workbench 发送与历史、P5 web、D 文档、V 验收 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 目标与完成定义

1. web 工作台（桌面与手机）可粘贴 / 拖放 / 选择 / 拍照附图，上传后显示缩略图，发送后历史中可见。
2. ACP 会话（agent 声明 `promptCapabilities.image`，本机或协议足够新的 worker 上）收到 `image` 内容块。
3. 其余会话（cli 会话 job、终端会话、不支持图片的 ACP）由 xfer 把图片放到执行机 `tmp/gofer-attachments/<日期>/`，消息末尾附「[附图 n] 绝对路径」；xfer 失败时整条不发并返回原因。
4. pty job 等不支持的收件方，web 在附图时置灰并说明。
5. 限额、权限、调用方上限、保存期（server 14 天、执行机 7 天）按设计落地。

完成定义：上述有测试覆盖并通过；Linux 全量 `-race`、Windows 全量、darwin vet、web 三命令通过；主机实测（Claude Code 终端会话、ACP 会话、手机拍照）通过；文档同步；发版 CI 全绿。

## 范围、排除项与授权

范围：设计「总体方案」与「架构」全部内容。

排除项：设计「非目标」所列。

授权边界：

- 计划批准不授权实施；实施需要用户另发执行请求（可与批准同一句话）。
- 执行请求覆盖：sonnet 子 agent（主 checkout）与 suga（`--model opencode-go/deepseek-v4.1-flash`，主机 worktree）实施并本地提交；监督者审阅、容器 `-race` 复跑、合并、主机实测。
- 发版沿用已有长期授权（`gofer-release-flow`）。
- 不改用户的主机 / 容器配置。
- `host_or_non_offline_action=REQUIRED`（发版、主机实测）。

## 输入与批准证据

- 设计：Draft 0.1，用户 2026-10-19 批准（原话「按推荐，批准设计」），决策 1–8 取推荐值。
- 关联：gofer-ha0x；xfer（`job run --upload`、`tool cp`）；workbench Turn（`internal/workbench/service.go`）。
- 仓库规则：AGENTS.md G021 / G022、G032、G041、G045 / G046、G051–G054、G056。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 把文件放到执行机项目目录 | xfer（`internal/xfer`、`job run --upload`、`tool cp`） | 是 | 完成回报增加 `abs_path` | OWNER_EXTENSION | 不回报绝对路径 | — |
| CAP-02 | ACP 发送图片 | `internal/acp` `ContentBlock` / `Client.Prompt`；worker `say` 协议 | 部分 | `ContentBlock` 增加 image；Prompt 接受内容块；`say` 协议携带内容块 | OWNER_EXTENSION | 只能发文本 | — |
| CAP-03 | 附件存储与读取 | job 结果目录、xfer 暂存区 | 否 | 新表 `attachments` + `<config-dir>/attachments/` | MINIMAL_NEW_MODULE | xfer 暂存区在派发后删除，生命周期与附件（14 天、可读缩略图）不同；结果目录属于 job | 与 xfer 暂存重复 → 附件只存原件，传输仍走 xfer |
| CAP-04 | 工作台输入框 | workbench composer、会话抽屉回复框 | 部分 | 共用一个附件条组件 | OWNER_EXTENSION | 无附件能力 | — |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NM-01 | CAP-03 | `attachments` 表与 `internal/attachment`（存储、校验、缩略图、保存期） | xfer 暂存、结果目录 | 生命周期不同 | 扩展 xfer 会把「传输暂存」与「长期可读附件」混在一起 | 见 CAP-03 | jobstore 持久化；serve prune 清理 | 非图片附件将来复用同一模块 |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 把图片 base64 写进消息文字 | CAP-01 | 删掉它后用文件送达即可；base64 会塞满会话上下文与 outbox，终端会话也无法解码 |

## 前置检查与 fail-closed 条件

workspace baseline：`inhere-tools/gofer`，branch `main`，v0.176.0 之后。

fail-closed：

- 附件读取必须按所属 thread 的可见性判定；未发送的附件只有上传者能读。
- 文件送达：xfer 未全部完成时不发消息。
- 只接受四种图片类型（文件头判定），返回带 `nosniff`。
- 新路由按 G056 声明并 `go generate`；调用方上限的 scope / rw 维度齐全。

## 波次与依赖

| 波次 | 任务 | 依赖与并行 |
|---|---|---|
| 一 | P1 附件存储与接口；P3 xfer 回报绝对路径与执行机清理 | 可并行（jobstore/httpapi vs xfer/worker/wsproto） |
| 二 | P2 ACP 图片内容块与 worker `say` 协议 | 与 P3 同改 wsproto：在 P3 之后，协议版本只升一次 |
| 三 | P4 workbench 发送与历史 | P1、P2、P3 |
| 三 | P5 web | P1（上传接口）；与 P4 并行，联调在 P4 之后 |
| 四 | D 文档；V 验收与发版 | 全部 |

## 任务

通用要求（每个任务）：只提交自己的文件（pathspec）；不还原、不丢弃别人的改动；不碰 `.gofer/tracker/*`；不删注释、不重排无关代码；用户可见变化同步 skill references 与 http-api（G045 / G046）；Go 验证 `GOTOOLCHAIN=go1.26.9 GOWORK=off`，测试前 `env -u CLAUDE_CODE_SESSION_ID -u GOFER_SESSION_ID`；G051 / G052。

### P1 附件存储与接口

- 文件：`internal/jobstore/attachments.go`（新）、`store.go`（建表）、`internal/attachment`（新：类型校验、存储路径、缩略图、保存期清理）、`internal/httpapi/attachment_handler.go`（新）、`authz_routes.go`、`internal/config`（`server.attachments.max_bytes`、`max_per_message`、`hourly_per_caller`，`storage.attachment_retention_days`）、`internal/serve`（prune 接入）。
- 路由：`POST /v1/attachments`（multipart）、`GET /v1/attachments/{id}`（`?thumb=1`）、`DELETE /v1/attachments/{id}`。
- 完成标准：类型伪造被拒、大小与限速、未发送附件只有上传者可读、发送后按 thread 可见性、过期清理、缩略图失败回退原件。

### P3 xfer 回报绝对路径与执行机清理

- 文件：`internal/xfer`、`internal/wsproto`（xfer 完成回报增加 `abs_path`，协议版本 +1）、`internal/worker`（放置后回报）、本机执行路径、执行机放置附件时清理 `tmp/gofer-attachments/` 下 7 天前的日期目录（路径在项目根内校验）。
- 完成标准：本机与 worker 都能拿到绝对路径；旧协议回退为项目相对路径；清理只动附件目录。

### P2 ACP 图片内容块

- 文件：`internal/acp/types.go`（`ContentBlock` 增加 image：`data` / `mimeType`）、`client.go`（`PromptBlocks`，原 `Prompt` 包装它）、`promptCapabilities.image` 解析并保存为会话能力、`internal/job` 的 `SaySession` 接受内容块、wsproto `say` 携带内容块（与 P3 同一次协议升级）、worker 端 ACP 会话转发。
- 完成标准：fake ACP 收到 image 块；不声明能力的 agent 不发 image 块；旧协议 worker 上的 ACP 会话判定为「文件送达」。

### P4 workbench 发送与历史

- 文件：`internal/workbench`（送达方式判定纯函数；Turn 接受附件；内联 / 文件送达；thread 能力位 `attachments: inline|file|none`）、`internal/httpapi/workbench_handler.go`（turn 请求 `attachments`，校验归属）、附件回填 `thread_id` 与发送引用（决策 id / job id / 会话消息 id），thread 详情带各条消息的附件引用。
- 完成标准：送达方式真值表；文件送达消息格式；xfer 失败整条不发；权限与上限；历史引用。

### P5 web

- 文件：共用附件条组件（粘贴 / 拖放 / 📎 / 缩略图 / 进度 / 删除 / 重试）、手机端压缩（canvas，长边 2048、JPEG 0.85，小 PNG 不动）、工作台 composer 与会话抽屉回复框接入、历史缩略图与大图、能力位为 `none` 时置灰与说明。
- 完成标准：纯函数（类型 / 大小判断、压缩参数）vitest；接线 `?raw`；web 三命令；手机宽度截图核对。

### D 文档

- `skills/gofer-usage/references/web-console.md`、`server-config.md`；`docs/reference/http-api.md`；`docs/guide/scenarios.md`、`security.md`；CHANGELOG；设计稿实施记录。

### V 验收与发版

- 全量：Linux `-race`、Windows 全量、darwin vet、web 三命令。
- 主机实测：给 Claude Code 终端会话发截图（agent 能描述图片内容）、给 ACP 会话发图、手机拍照发送；Codex 终端会话读图的表现与 suga 是否支持（设计「待确认事项」）写进实施记录。
- 发版：按 `gofer-release-flow`。

## 回滚与恢复

- 每个任务一到两个提交；P2 / P3 的协议字段为 additive，旧 worker 回退到文件送达或相对路径。
- 发版后回滚：`attachments` 表与目录被忽略；执行机上的附件目录可手工删除。

## 人工 Gate

- **计划批准与执行请求**：用户具名批准本计划并要求开始实施。
- **外部动作**（已有长期授权）：tag、push、CI、mirror、一键更新与 worker 升级。
- **主机实测**：需要真实会话与手机，由监督者与用户完成。

## 可追溯性

| 设计条目 | 任务 | 验证 |
|---|---|---|
| 附件存储、保存期、权限（决策 2、5） | P1 | 接口与权限用例 |
| 执行机存放位置与清理（决策 3） | P3 | 清理用例 |
| 绝对路径回报、旧 worker 回退（决策 7） | P3 | 协议用例 |
| 两种送达方式（决策 1） | P2、P4 | 送达真值表 + fake ACP |
| xfer 失败整条不发（决策 6） | P4 | 失败用例 |
| 手机压缩（决策 4） | P5 | vitest + 实测 |
| 本期范围（决策 8） | 全部 | — |

## 完成 Gate 与剩余工作

- 完成 Gate：完成定义全部满足；V 通过；设计稿实施记录写入。
- 剩余工作：非图片附件、新建 job / 信件 / todo 的附件、CLI 发图。
