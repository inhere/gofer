<!-- template_id: plan; template_version: 1.2.0 -->
# Web UI 打磨与待办批次（U1–U4）实施计划

> 状态：Draft 0.1 / 待人工计划批准；执行方式：DIRECT_CONTINUOUS

## 规划可靠性声明

`thinking_mode=RIGOROUS`。核心目标是把已批准设计的 U1–U4 变成可测试、可回滚、可逐期验收的实现波次；`scope_freeze=仅 UI-01…UI-04、BL-01…BL-04 及其必要测试/只读接口字段`；`review_budget=每期一次最小源代码核查、一次测试先行提交、一次实现与质量门核查`；停止条件是发现协议/数据模型/权限/目录锁语义变化、命中他人 dirty 文件、或需要真实 server/worker、真实配置、部署、迁移、外部流量时立即停在 Gate。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-29 | Codex | 将已批准的 Web UI 打磨与 BL-01…BL-04 拆成 U1–U4，按测试先行、实现、容器截图验收和质量门执行 |

## 目标与完成定义

目标：在不改变 tracker 镜像、交接说明、目录锁和文件传输语义的前提下，使 Web 控件与现有 Gofer 风格一致，并补齐四个已登记待办的用户可见反馈和必要只读接口。

完成定义：

- U1 菜单/路由、BL-03 CLI/HTTP/MCP 参数校验、BL-04 project_key prime/status 测试全部绿；顶栏仅在 `needsReviewCount > 0` 显示“待验收 N”。
- U2 交接卡片完成版本切换、仅最新可修改、修改/新增、Markdown 预览、expected_version 409 刷新和空状态；组件测试覆盖这些交互。
- U3 Issues/Memories、仓库选择、筛选、详情抽屉、编辑/评论、memory 编辑/墓碑删除和 409 处理完成；需要时只补 tracker 既有路由风格的只读/编辑接口。
- U4 job 上传区块/Board 附件标记/含上传筛选和 waiting_dir 占用者提示完成；README 与 gofer-usage skill 写明只读任务带 `--read-only`。
- 每期有页面结构说明和容器截图验收证据；Go 改动 `gofmt -l` 无输出，相关 build/vet/test 有 exit code 与原始 PASS/FAIL；Web 执行 `pnpm test`、`pnpm typecheck`、`pnpm build`（Windows 若 esbuild 阻断，记录本地 `.bin` 等效命令原文并交容器复跑）；控制字符扫描通过；每个功能点独立 conventional commit；不 push。

## 范围、排除项与授权

- 代码 Git 根固定为 `D:/work/inhere/hyy-ai-inspect/tools/gofer`，branch `main`；工作区根 `D:/work/inhere/hyy-ai-inspect` 仅承载 IDEV-STD receipt 与截图。
- 允许修改 `web/src`、`internal/commands`、`internal/httpapi`、`internal/mcpserver`、`internal/tracker`、`internal/jobstore`、`internal/job`、`skills`、中英文 README 及对应测试；入口层遵守 G021，依赖方向遵守 G022。
- 不改变交接说明、tracker 镜像、同步冲突合并、目录锁重叠判定、xfer 传输协议或 runner 语义；BL-01 只补已有 xfer 数据的只读呈现字段，BL-02 只做提示与文档。
- 不重启/reload live gofer server/worker，不触碰 `D:/work/inhere/config/win-env/gofer`，不部署、不迁移真实数据库、不做硬件/流量/外部消息动作；测试全部 `t.TempDir()`，smoke 只用临时 config 和随机端口，命令显式带 `--server` 或 `-c`。
- `host_or_non_offline_action=NOT_APPLICABLE`：本计划不包含主机外部动作；容器截图属于离线验收，真实进程冒烟仅使用临时配置。
- 用户已授权编写并提交整批计划候选，监督者可代批由批准设计派生的实施计划；本文件提交后停止，等待人工批准和后续当前实施请求。

## 输入与批准证据

- 唯一设计依据：[2026-09-29 Web UI 打磨与待办批次设计](../design/2026-09-29-web-ui-polish-and-backlog-design.md)，状态 Approved，用户批准日期 2026-09-29，决策为保留 Issues 菜单名、Review 移出顶栏、U1→U4 派发。
- 反例截图：`tools/tmp/gofer-plan-handoff.png`、`tools/tmp/gofer-issues.png`。前者显示原生小 textarea/默认按钮，后者显示 tracker_id 和七个裸输入框。
- 已读取的风格来源并将在实现中复用：`web/src/styles/tokens.css` 的 `--ink/--panel/--line/--paper/--run/--done/--fail/--queue/--phosphor/--term-bg/--radius`；`views/Plans.vue` 的 `filter`、`filter-select`、`filter-input`、`page-btn`、`create-input`、`table`、`empty`；`views/PlanDetail.vue` 的卡片/标题行；`views/Board.vue` 的筛选和状态布局；`components/SessionDrawer.vue` 的抽屉；`views/settings/SettingsLayout.vue` 的侧栏；`components/MarkdownBlock.vue`、`StatusBadge.vue`、`PlanStatusBadge.vue`。
- 现状证据：`web/src/store/reviewCount.ts` 与 `EscalationBell.vue` 维护 `needsReviewCount`；`web/src/router.ts` 仍有 `/skills`；`web/src/views/Issues.vue` 仍手填 tracker_id 且使用裸控件；`web/src/api/client.ts` 已有 tracker issue 请求；`internal/httpapi/server.go` 已注册 `/tracker/repos`；`internal/jobstore/tracker.go` 已有 `tracker_repos`；`JobDetail.vue` 已渲染 xfer uploads/collected/skipped；`StatusBadge.vue` 已能显示 waiting_dir holder；`internal/commands/job.go` 已输出 waiting_dir holder。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 顶栏 Review/Skills 菜单、待验收徽标、Skills 重定向 | `App.vue`、`EscalationBell.vue`、`reviewCount.ts`、`router.ts`、`SettingsLayout.vue` | 复用 `needsReviewCount` 和既有 `/review`、设置侧栏导航 | 在 `App.vue` 调整菜单项和条件徽标，在 router 增加 `/settings/skills` 与 redirect | OWNER_EXTENSION | 当前 Review/Skills 仍是顶栏静态项，Skills 无设置路径 | 不新建计数 store；避免重复轮询和两个 Skills 路径真源 |
| CAP-02 | BL-03 非 exec 的 `--`/`cmd` 拒绝 | `internal/commands/job.go`、agent 参数解析、`internal/httpapi`、`internal/mcpserver` 提交入口及现有错误 envelope | 复用现有 agent 类型判定、CLI usage、HTTP/MCP 参数校验 | 在共享提交校验 owner 增加明确错误并让 CLI 提前给 `--prompt`/`-f` 提示 | OWNER_EXTENSION | 当前位置参数可能被静默丢弃；HTTP/MCP `cmd` 需服务端拒绝 | 不在三个入口复制规则；旧路径不加无标记兼容分支 |
| CAP-03 | BL-04 project_key prime/status 识别 | `internal/tracker` prime、`internal/commands/repo.go`、`.gofer/tracker/config.yaml` 读取、现有 tracker CLI 测试 | 复用已有 project_key 优先逻辑和 prime 输出段 | 补无 projects 客户端配置下的 prime 测试及 project_key 为空的 status 提示 | OWNER_EXTENSION | 覆盖不足，需固定回归测试和友好提示 | 不改变 repo sync 语义，不把提示误当成功同步 |
| CAP-04 | 交接卡片版本/编辑/Markdown/409 | `PlanDetail.vue`、现有 plan API/types、`MarkdownBlock.vue`、Plans/PlanDetail card CSS | 复用 plan detail 生命周期、MarkdownBlock、tokens 和按钮/下拉类 | 在现有 PlanDetail handoff 区块扩展版本选择与编辑态；API 类型复用 handoff 响应 | OWNER_EXTENSION | 当前为常驻小 textarea/默认按钮，无版本交互测试 | 不复制 Markdown 渲染器或另建 handoff 页面 |
| CAP-05 | Tracker 仓库选择、Issues/Memories 筛选与抽屉编辑 | `Issues.vue`、`api/client.ts` tracker 请求、`internal/httpapi` tracker handlers、`internal/tracker`/`jobstore` 模型、SessionDrawer | 复用 tracker_repos、issue/memory 本地镜像、SessionDrawer、StatusBadge、MarkdownBlock | 扩展 repo/memory API 仅在字段缺失时；重写 Issues 视图为标签/表格/抽屉 | OWNER_EXTENSION | UI 手填 tracker_id、无 memories/多选筛选/冲突处理；需核对 memory API 是否缺失 | 不改变同步合并和墓碑语义；所有更新沿 expected_rev |
| CAP-06 | xfer 上传在 job/Board 列表可见 | `JobDetail.vue` 的 `JobXferUpload`、`api/types.ts`、Board/列表 job 数据、`internal/jobstore` xfer JSON/API | 复用已返回 xfer uploads、现有 xfer 状态样式和 job list | 只补缺少的只读字段、附件标记和含上传筛选 | OWNER_EXTENSION | 详情已有局部渲染，Board/列表缺标记/筛选；需先核对 wire 字段 | 不复制 xfer store；不改变上传/收集协议 |
| CAP-07 | waiting_dir 占用者提示及只读文档 | `StatusBadge.vue` holder、`JobDetail.vue` 状态字段、Board 卡片、`internal/commands/job.go`、README/skills | 复用 `waiting_on_job`/job.waiting_dir 事实和三种既有办法 | 在详情/Board/CLI 统一文案并补 README/skill | OWNER_EXTENSION | 目前各界面提示不完整，文档未强调只读任务 | 不修改 dirlock 判定；不新增锁模式 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 独立 tracker 前端 store/第二套 drawer | CAP-05 | 现有 API client、SessionDrawer 和视图级状态足以完成验收；新增 store 会复制缓存/冲突生命周期 |
| 新的 xfer 协议或附件服务 | CAP-06 | 设计只允许补缺的只读字段；xfer_json 与既有 API 已是事实源 |
| 第二个 review 计数轮询器 | CAP-01 | `EscalationBell` 已维护 `needsReviewCount`；第二轮询会造成数量漂移 |

## Workspace baseline 与前置检查

- Git root：`D:/work/inhere/hyy-ai-inspect/tools/gofer`；branch：`main`；起点 HEAD：`95a0df3 docs(design): approve web UI polish and backlog batch`。
- 计划编写前 `git status --short --untracked-files=all` 无输出；若后续发现 dirty/untracked，先归属到用户/其他 agent 或本计划 owner，无法排除即停。预期 owner 路径包括 `docs/plans/2026-09-29-web-ui-polish-and-backlog-plan.md`、上述 Web/Go/文档测试路径；不吸收无关修改。
- 实施前每期重跑 IDEV-STD BOUND/probe、semantic load/fingerprint，并读取实际源；本计划 hash/路径不是未来 mutation 的封闭白名单。新路径按 Operational Discovery / Corrective / Semantic Amendment / Ownership Conflict 分类记录。
- T00 只读核查：确认 tracker memories 是否已有 GET/PUT/delete 路由、repo 响应字段、Issue expected_rev、job xfer wire 字段、waiting_on_job 在 Board 数据中是否存在、CLI 非 exec agent 的位置参数实际流；任何协议/权限/数据模型冲突返回 design/plan Gate。
- G032：计划预期无新增兼容分支；若保留旧 `/skills`，明确为路由重定向并记录 `// DEPRECATED(vX): remove in vY` 是否需要。没有用户的旧 wire/CLI 路径直接删除，不做无标记 fallback。

## 波次与依赖

T00 基线/风格/接口核查 → 每期测试先行（独立 commit）→ 该期实现（按功能点 commit）→ 该期 focused 验证与容器截图 → 下一期。U1 必须先完成，因为 U2–U4 共用菜单/路由和 tracker/统计基础；U2、U3、U4 在 U1 绿后顺序执行。实施请求和计划批准均是开始 mutation 的必要条件。

## T00：最小核查与计划后 Gate

- **文件/动作**：只读 `App.vue`、`router.ts`、`EscalationBell.vue`、`reviewCount.ts`、`SettingsLayout.vue`、`Issues.vue`、`api/client.ts/types.ts`、tracker handlers/store/model、`PlanDetail.vue`、`JobDetail.vue`、`Board.vue`、`StatusBadge.vue`、CLI agent/job 校验、prime/status 测试；记录所有实际 API 字段和 expected_rev/expected_version 口径。
- **验证**：`git status --short --untracked-files=all`、`git log -1 --oneline`、`gofmt -l`（仅报告，不改）、计划 validator。完成标准是每个 CAP 有唯一 owner、每期测试入口和不改变语义的边界。
- **依赖/停止**：计划候选提交后等待人工批准；未获批准不得写测试或实现。

## 任务

以下 U1–U4 任务按依赖顺序执行；每个测试先行任务必须先形成独立 red 证据和 commit，再进入同一期实现。

## U1：菜单、重定向、BL-03、BL-04

### U1-T1 测试先行

- **测试文件**：`web/src` 现有 router/App 组件测试位置；`internal/commands` agent/tracker CLI 测试；`internal/httpapi`/`internal/mcpserver` 提交校验测试；`internal/tracker` prime/status 测试。
- **动作**：先写可执行红测试：Review 菜单移除且徽标仅 `count>0`；`/skills` 重定向 `/settings/skills`；非 exec agent 的 CLI `--`、HTTP/MCP `cmd` 均返回包含 `--prompt`/`-f` 的错误；config.yaml 有 project_key 且客户端无 projects 时 prime 交接段出现；project_key 为空时 repo status 提示填写。
- **验证/完成**：运行定向 Go/Web 测试，确认失败原因是目标行为缺失而非夹具/编译错误；提交 `test(u1): cover menu routing agent args and tracker project identity`，只 stage 测试文件。

### U1-T2 实现与验证

- **动作**：在 `App.vue` 保留 EscalationBell 轮询，将 Review/Skills 从顶栏移除，新增条件徽标链接 `/review`；在 SettingsLayout 增加 Skills，router 添加 `/settings/skills` 并把 `/skills` redirect；在共享提交校验 owner 拒绝非 exec 的位置参数/`cmd`，CLI 提前提示；prime/status 补 project_key 分支和文案。旧路径若保留仅做显式 redirect，不新增协议兼容层。
- **验证**：Go 定向测试、`cd web && pnpm test && pnpm typecheck && pnpm build`；页面结构说明：顶栏是品牌/主导航/动态待验收徽标/工具按钮，设置侧栏按现有 SettingsLayout 放 Skills；截图验收核对浅色/深色主题、无裸控件。提交 `feat(u1): align navigation and validate agent tracker inputs`。
- **完成标准**：U1 测试绿，菜单/路由和错误文案可见，prime/status 证据覆盖两种 project_key 状态；不触碰 live server/worker。

## U2：交接卡片

### U2-T1 测试先行

- **测试文件**：`web/src/views/PlanDetail*.test.*` 或现有 Vitest 组件测试目录，必要时补 API mock 类型。
- **动作**：覆盖版本下拉默认最新、历史只读且显示“回到最新”、修改只对最新可见并预填、+新增空白、编辑/预览切换（预览使用 MarkdownBlock）、取消、保存 expected_version、409 提示并刷新、无交接空状态。
- **验证/完成**：先运行单组件集合，确认有效红；提交 `test(u2): cover handoff version editing and conflict flows`。

### U2-T2 实现与验证

- **动作**：在 PlanDetail 交接卡片中复用 PlanDetail card/title row、Plans 的 `filter-select`/`page-btn`/按钮类和 tokens；移除常驻小 textarea、“保存交接说明”“展开历史”默认样式；实现版本下拉列 `vN · 更新人 · 时间`、修改/新增、至少 12 行可拖高 textarea、编辑/预览、保存/取消和 409 刷新。API 只使用设计已存在的 handoff/version endpoint；若字段命名与实际不符先做 Operational Discovery，不改协议语义。
- **验证**：Vitest 组件集合；页面结构说明：PlanDetail 卡片标题行左侧“交接说明”、右侧版本下拉和新增，正文/空状态居中，编辑态整卡大编辑区与底部操作，历史态有提示链接；深浅主题截图；提交 `feat(u2): restyle versioned plan handoff card`。
- **完成标准**：所有 U2 测试绿、无裸控件、样式只用 tokens/复用类、409 不丢编辑语义。

## U3：Tracker Issues + Memories

### U3-T1 测试先行

- **测试文件**：`web/src/views/Issues*.test.*`、`web/src/components/SessionDrawer*.test.*`；若 HTTP 缺 memories 列表/编辑/墓碑删除，先在 `internal/httpapi`/`internal/tracker` 写接口红测试。
- **动作**：覆盖仓库下拉按 `项目 · prefix · 路径` 和未归属标注、默认最近同步仓库；Issues/Memories 标签；closed 默认隐藏、多选状态 chip、类型/标签/关键词筛选；点击行开右抽屉；状态/优先级/标题编辑和评论；expected_rev 409；memory 展开/编辑/删除墓碑；无仓库空状态与同步冲突摘要。
- **验证/完成**：组件和接口定向测试先红且失败原因明确；提交 `test(u3): cover tracker repositories issues drawer and memories`。

### U3-T2 API/视图实现与验证

- **动作**：优先复用 `/v1/tracker/repos`、现有 issues 请求、tracker_repos 的 last_sync/sync_summary 和本地 memory 模型；只有列表/编辑/删除没有 HTTP 接口时，按现有 tracker 路由风格补最小只读/编辑接口，不改同步语义。重做 Issues.vue：顶部仓库选择和同步摘要，Issues/Memories tabs，Plans/Board 风格筛选条和表格，SessionDrawer 风格详情抽屉，MarkdownBlock 描述/全文，StatusBadge 状态，所有 input/button/select/textarea 有类名。
- **验证**：接口/组件集合、409 刷新、`pnpm typecheck/build`；页面结构说明：页面标题/仓库选择/同步摘要在上，tab 与筛选条次之，主表格占左侧，点击行由右侧抽屉覆盖；无仓库时使用 tokens 空状态并指引 `gofer repo init/sync`；深浅主题截图。提交 `feat(u3): rebuild tracker issues and memories workspace`，如新增接口可独立提交 `feat(tracker): expose missing memory read write routes`。
- **完成标准**：仓库不再手填 tracker_id，所有控件非浏览器默认样式，编辑/评论/墓碑删除沿 expected_rev 和现有权限。

## U4：上传可见性与 waiting_dir 提示

### U4-T1 测试先行

- **测试文件**：`web/src/views/JobDetail*.test.*`、`web/src/views/Board*.test.*`、`internal/httpapi` job response tests、`internal/commands` job output tests、必要的 skill/README 静态检查。
- **动作**：用现有 xfer_json fixture 断言上传文件名/目标路径/大小/状态；有上传 job 有附件标记并可按含上传筛选；waiting_dir CLI/详情/Board 显示 holder 与 `--read-only`/`--shared-dir`/`--worktree`；确认没有 xfer 字段时不渲染空区块。
- **验证/完成**：先红再提交 `test(u4): cover upload markers and waiting directory guidance`。

### U4-T2 实现与验证

- **动作**：先核对 job API 和 xfer_json 已有字段；只补缺字段映射，不改变 xfer store/协议。JobDetail 增加“随 job 上传的文件”区块，复用已有 xfer 行/状态 chip；Board/列表增加附件标记和“含上传”筛选；统一 waiting_dir 文案并从 `waiting_on_job` 显示 holder；更新 `skills/gofer-usage` 与 README/README.zh-CN，强调只读任务务必带 `--read-only`，保留三种办法说明。
- **验证**：Go 定向 job/httpapi 测试、Web 组件集合和三命令；页面结构说明：JobDetail 结果区在现有 artifacts/xfer 区域列出文件，Board 筛选条增加附件开关，waiting_dir 状态行紧邻 holder 和解决办法；深浅主题截图；提交 `feat(u4): surface uploads and directory lock guidance`、`docs(u4): document read-only directory lock usage`（若文档独立）。
- **完成标准**：上传/等待提示只读、数据来自已有 API，Board/详情/CLI 文案一致，目录锁判定测试未改。

## 全批质量门（每期末执行，U4 后汇总）

- Go：`gofmt -l` 无输出；`go build ./...`（构建产物如需写入 `tmp/`）；`go vet ./...`；相关 `go test` 包（Windows 慢包可后台运行，但最终报告必须等结果）。
- Web：`cd web && pnpm test && pnpm typecheck && pnpm build`；若 Windows esbuild 阻断，记录 exit code、原始输出和 `.bin` 等效命令，监督者在容器补跑。
- 控制字符：扫描本批修改文件，排除正常 CR/LF 之外的控制字符；`git diff --check`。
- 计划/文档：`python <plugin-root>/scripts/validate_document.py --kind plan docs/plans/2026-09-29-web-ui-polish-and-backlog-plan.md`；候选提交前生成 `git_candidate_revision.py --git-root D:/work/inhere/hyy-ai-inspect/tools/gofer --subject-path docs/plans/2026-09-29-web-ui-polish-and-backlog-plan.md --kind plan`，使用命令返回的 candidate 字段，不手填 hash。
- 每条命令同时记录 exit code 和原始 PASS/FAIL 行；不以 scheduler/build/health 代替 UI/业务验收。

## 回滚与恢复

每次提交前在 `tools/gofer` 执行 `git status --short`，确认只包含本功能点 owner；核对 `git diff --cached --name-only` 后再提交。按 `test(uN):`、`feat(uN):`、`docs(uN):` 分功能点原子提交，不 push。每个提交是恢复点；失败时记录 HEAD、首个失败命令、已过测试和下一步。回滚只评估本任务准确 commit 的 `git revert`，不 reset/clean，不覆盖他人 dirty。任何命中 Semantic Amendment、Ownership Conflict 或 G032 兼容路径争议时停止并回到人工 Gate。

## 人工 Gate

1. 本计划候选提交后等待监督者批准；计划批准不等于实施授权。
2. 获批后需用户/监督者发出当前实施请求，才进入 U1 测试先行。
3. 真实 secret、push、release、deploy、migration、live server/worker、外部消息、设备/流量动作统一需要额外外部动作批准；本批默认不做。
4. 每期容器截图验收是继续下一期的人工检查点；若截图显示风格偏离或深浅主题异常，停在该期修正，不推进后续期。

## 可追溯性

| 设计要求 | 计划任务 | 可观察验证 |
|---|---|---|
| Review 移出顶栏、待验收 N 条件显示、Skills 设置侧栏/重定向 | U1-T1/U1-T2 | router/App 组件测试、菜单截图、`needsReviewCount` 条件 |
| BL-03 非 exec `--`/`cmd` 报错并提示 `--prompt`/`-f` | U1-T1/U1-T2 | CLI/HTTP/MCP 定向测试与原始错误输出 |
| BL-04 project_key prime/status | U1-T1/U1-T2 | prime/status 测试，含无 projects 客户端配置 |
| 交接版本/修改/新增/预览/409/空状态 | U2-T1/U2-T2 | Vitest、PlanDetail 深浅主题截图 |
| Issues/Memories、repo 选择、筛选、抽屉编辑评论、memory 墓碑 | U3-T1/U3-T2 | API/组件测试、Issues 页面截图 |
| xfer 上传区块/附件标记/含上传筛选 | U4-T1/U4-T2 | JobDetail/Board 测试、页面截图、wire 字段核对 |
| waiting_dir holder 和三种处理办法、只读文档 | U4-T1/U4-T2 | CLI/详情/Board 测试、README/skill 静态核对 |
| UI 风格、tokens、无裸控件、双主题 | U2/U3/U4 实现与截图 | `rg` 控件类名检查、pnpm 三命令、深浅主题截图 |

## 完成 Gate 与剩余工作

本计划候选只在计划 validator、`git status --short`、精确 stage 和本地 atomic commit 证据齐全后标记“计划候选已提交”，不标记实施完成。实施完成需 U1–U4 各自测试从有效 red 到 green、Go/Web 质量门原始输出、每期页面结构和容器截图、G032 清单、git log/status 和未完成/人工决策点全部齐全。未完成项按期记录，不用骨架、`t.Skip`、默认样式或未验证的绿色检查冒充完成。
