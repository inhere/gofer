<!-- template_id: plan; template_version: 1.2.0 -->
# 整理与管家降噪、扩展设置、信箱对话界面 实施计划

> 状态：Draft 0.1 / 待用户批准
> issue：gofer-k1cg、gofer-yhqs

- 思考模式：rigorous。
- 核心目标：落地两份已批准的设计——`docs/design/2026-10-20-work-noise-and-db-settings-design.md`（k1cg，决策 1–7 取推荐值）与 `docs/design/2026-10-20-mail-conversations-ui-design.md`（yhqs，决策 1–4 取推荐值）。
- scope freeze：只做两份设计「范围」列出的内容；广播（gofer-metc）不在本计划内。
- review budget：本计划最多一轮独立评审；评审后修订由作者自查。
- 停止条件：用户具名批准本计划，或用户要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-20 | Claude（监督会话） | 初稿：K 线（设置表、CLI 与 tool api、降噪、提示词、web 设置页）与 M 线（信箱读接口、web 对话界面）并行 |

## 目标与完成定义

1. 扩展设置：`settings` 表与键登记、`GET/PUT/DELETE /v1/settings[/{key}]`、导入导出、审计、`settings.read`（种给 users）/ `settings.write`（不种）；`gofer settings ls|get|set|reset|export|import`；`gofer tool api`；web「设置 → 扩展设置」页。
2. 降噪：第 2 次人工提问才建草稿、无项目不建、排除名单；整理器与管家（巡检、prime、事件）跳过短命会话，不占整理额度、跳过原因可见。阈值与名单都是扩展设置。
3. 提示词：管家角色、管家巡检、整理器三段提示词各有「整段替换」与「追加」两个设置键；整理器 JSON 格式说明总在末尾；默认提示词改写一版。
4. 信箱：服务端按参与方对聚合（`GET /v1/mail/conversations`）、信件带参与方 `persona`；web 左栏一行一个对话、右栏时间线（回复关系可见）、人物卡、markdown 渲染。
5. 文档与实施记录。

## 范围、排除项与授权

- 范围：上面五条。
- 排除：把 config.yaml 现有配置搬进数据库；广播；会话改名；信件搜索。
- 授权：用户 2026-10-20 批准两份设计；实施以本计划的具名批准为准。tag、push、CI、mirror、一键更新沿用发版长期授权。

## 已确认事实（代码核对）

- 草稿在 `work.Service.OnHumanPrompt`（`internal/work/service.go`）→ `jobstore.EnsureDraftWorkItemForSession`（`internal/jobstore/work.go`）建；`agent_sessions` **没有**人工提问计数字段，需要新增。
- 整理器选择在 `internal/work/summarize.go` `scanSummarize` / `quietEnough`；提示词 `buildPrompt`。管家在 `internal/steward/review.go`（`reviewItems`、`reviewPrompt`）、`prime.go`（`rolePrompt`、`BuildPrime`）、`events.go`（`scanEvents`）。
- 权限目录 `internal/authz/catalog.go` 已有 `SeedV2`；新种子批次为 `SeedV3`。
- 信箱列表 `jobstore.ListMail`（`internal/jobstore/mail.go`）、`internal/httpapi/mail_handler.go`；web `web/src/views/Mail.vue` 现按 `thread_id` 分组（`groupMailThreads`）。

## 波次与依赖

| 波次 | 任务 | 依赖 | 并行 |
|---|---|---|---|
| 一 | K1 设置存储与接口；M1 信箱读接口 | — | 并行（K1 在 jobstore 新文件 + 新包 internal/settings + httpapi 新文件；M1 在 jobstore/mail.go + httpapi/mail_handler.go；两者都改 store.go 的建表清单时按合并处理） |
| 二 | K2 CLI 与 tool api；K3 降噪；M2 web 信箱 | K1；M1 | K2、K3 不同包可并行；M2 只改 web |
| 三 | K4 提示词；K5 web 设置页 | K1、K3 | 并行（K4 后端、K5 web） |
| 四 | G 文档、V 验收与发版 | 全部 | — |

## 任务

通用要求：只提交自己的文件（pathspec），英文 conventional message；不删注释、不重排无关代码；用户可见变化同步 skill references 与 `docs/reference/http-api.md`（G045 / G046）；新路由与权限按 G056 声明并重跑 `go generate ./internal/httpapi/...`；Go 验证 `GOWORK=off`，主机实施不能 `-race`，监督者容器复跑；G051 / G052。

### K1 设置存储与接口

- `jobstore`：`settings(key TEXT PRIMARY KEY, value TEXT, updated_by TEXT, updated_at INTEGER)` additive 迁移；读写 / 删除 / 列表；全局版本号（写入递增）。
- 新包 `internal/settings`：键登记表（名称、类型 string / int / bool / string 列表 / 正则列表、默认值、说明、校验、上下限、是否多行）；`Get[T]`、`Set`、`Reset`、`List`（含默认值与是否改过）；进程内缓存 + 版本号，本进程写入即时、跨进程 5 s 生效（参照授权缓存）。本任务登记第一批键（见设计「键名」），提示词键的默认值先引用现有常量（K4 再改写）。
- `authz`：`settings.read`（RiskLow、种给 users，`SeedV3`）、`settings.write`（RiskMedium、不种）；受限调用方不能写。
- `httpapi`：`GET /v1/settings`、`GET/PUT/DELETE /v1/settings/{key}`、`GET /v1/settings/export`、`POST /v1/settings/import`（`dry_run` 预览差异）；写入记 `auth_audit`（键、旧 / 新值摘要）。
- 测试：类型校验、默认回落、缓存失效、权限矩阵、审计、导入预览与写入。

### K2 CLI 与 tool api

- `gofer settings ls|get|set|reset|export|import`（顶级资源命令，与 `config` 并列）；`set` 支持 `--file` 读多行值；`import` 默认预览，`--yes` 才写。
- `gofer tool api [-X METHOD] <path> [-f key=value]... [-F key=@file] [--input file|-] [--jq expr]`：带 server 地址与 token，非 2xx 以非零退出并打印错误体；`--jq` 用内置的 jq 实现（若仓库已有依赖则复用，没有就先只支持 `-q` 原样输出并在汇报说明）。
- 测试：命令解析、错误码透传。

### K3 降噪

- `agent_sessions` 加 `human_prompts INTEGER`（additive），在 `OnHumanPrompt` 路径递增。
- 草稿门槛：`work.draft.min_prompts`（默认 2）；无项目不建；`work.draft.exclude_projects` / `exclude_cwds`（前缀）/ `exclude_title_patterns`（正则）命中不建。
- 短命判据：会话已结束（或离线）且存活 < `work.short_session.max_minutes`（默认 10）且人工提问 < `work.short_session.max_prompts`（默认 3）。`scanSummarize`、`reviewItems`、`BuildPrime`、`scanEvents` 跳过；整理器记一条跳过原因到整理日志，不占额度。
- 已有草稿不动。
- 测试：门槛、排除、短命跳过、额度不变、原因可见。

### K4 提示词

- 三段提示词各两个键：`prompt.steward.role` / `.extra`、`prompt.steward.review` / `.extra`、`prompt.work.summarizer` / `.extra`；替换键为空表示用默认。
- 整理器：JSON 输出格式说明从提示词里拆出，总在最后由代码追加；替换不掉。
- 改写默认提示词：整理器只记录有实质进展的会话、不对试用 / 测试性质的操作给建议；管家巡检同样加这一条。
- 测试：替换 / 追加 / 默认、JSON 说明位置、job 详情里看到实际提示词。

### K5 web 扩展设置页

- 「设置」下新增「扩展设置」：按子系统分组列出键，显示当前值、默认值、是否改过、说明；按类型编辑（提示词用多行编辑器并可对比默认值）；恢复默认；无写权限时只读。
- 测试：纯函数 + 接线。

### M1 信箱读接口

- `GET /v1/mail` 的 `from` / `to` 补 `persona`（`name`：会话主题 → 标题 → agent + 项目；`agent`、`project_key`、`runner`、`state`、`last_seen_at`；job：标题与 agent；收集箱：调用方显示名）。
- `GET /v1/mail/conversations?box=&limit=&offset=`：按参与方对（方向无关、规范化地址）聚合，返回双方 persona、最后一封、未读、急件、信件数、活动时间；`GET /v1/mail?peer=&with=` 取一个对话。权限沿用 `GET /v1/mail` 的过滤。
- 测试：聚合与方向无关、权限过滤（看不到的信不计入）、persona 回退。

### M2 web 信箱

- 左栏对话列表（人物卡 ×2、摘要、未读、急件、时间、信件数）；右栏时间线（左右气泡按决策 4，回信标「回复 #N」并可跳转，状态角标）；人物卡组件（名字、副行、稳定颜色与首字头像、悬停完整 id / cwd、点击开会话抽屉）；正文用现有 markdown 组件；窄屏左右切换。
- 测试：分组 / 方向 / 颜色纯函数、接线、手机宽度截图（监督者用 uiaudit 工具核对）。

### G 文档

- skill references（工作项与管家降噪、扩展设置键表、`settings` 与 `tool api`、信箱对话）、http-api、CHANGELOG；两份设计稿「实施记录」。

### V 验收与发版（监督者）

- 容器 Linux 全量 `-race`、darwin / windows vet；Windows 全量；web 三命令；web 截图走查（扩展设置页、信箱桌面与手机宽度）；发版。

## 回滚与恢复

- 每个任务一到两个提交，验证不过时 `git revert`；新表与新列 additive，回退旧二进制时被忽略（提示词回到写死版本、草稿回到第一次提问即建）。

## 人工 Gate

- **计划批准与执行请求**：用户具名批准本计划并要求开始实施。
- **外部动作**（已有长期授权）：tag、push、CI、mirror、一键更新与 worker 升级。

## 可追溯性

| 设计条目 | 任务 | 验证 |
|---|---|---|
| k1cg 决策 1、5、6（扩展设置、命令、权限） | K1、K2、K5 | 设置用例、权限矩阵 |
| k1cg 决策 2、3（草稿门槛、短命判据） | K3 | 降噪用例 |
| k1cg 决策 4、7（提示词覆盖、默认改写） | K4 | 提示词用例 |
| yhqs 决策 1、3（对话聚合放服务端） | M1、M2 | 聚合与权限用例 |
| yhqs 决策 2、4（人物名、气泡方向） | M1、M2 | 纯函数与截图 |

## 完成 Gate 与剩余工作

- 全部任务提交并通过 V；两份设计稿补「实施记录」。
