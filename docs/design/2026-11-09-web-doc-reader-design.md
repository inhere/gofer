<!-- template_id: design; template_version: 1.2.0 -->
# web 内置 Markdown 文档阅读设计

> 状态：Draft 0.1 / 待人工计划批准
> issue：gofer-i1bd

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-09 | Claude | 初稿 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：thinking_mode=RIGOROUS。
核心目标：计划、todo、issue、job 汇报和评论里写的项目内文档相对路径在 web 上可以点开，在 web 内阅读 Markdown。
Scope freeze：只做一个按项目读文档的只读接口、web 端路径识别与链接化、一个阅读页；只覆盖 server 本机能读到的项目目录。
非目标：编辑文档；全文搜索与目录树浏览；经远程 worker 读文件；读 job worktree 里的文件；文档内图片显示；改 marked / DOMPurify 的版本或配置。
Review budget：低暴露度（未改 schema、无数据迁移、可回滚），一轮合并轴评审，不建 review ledger。
停止条件：合并轴评审没有 `CORE_BLOCKING`，且「待确认事项」逐项得到用户答复后，交用户做计划批准。

## 背景与目标

用户 2026-11-09 提出：计划描述、todo、issue（含 design 字段）、job 汇报和评论里常写 `docs/plans/xxx.md` 这类项目内文档路径，现在只是文本，要看内容得另开编辑器或终端。

用户可见结果：

- 上述文本里的文档路径显示为链接，点开后在 web 内看到渲染好的 Markdown。
- 阅读页地址可以复制给别人，对方有同一项目的读权限即可打开。
- 文档内指向其他项目内文档的相对链接可以继续点开；外部链接在新标签页打开。
- 读不到时（项目不在 server 本机、文件不存在、类型不允许）显示明确原因，不显示空白页。

## 名词

- **项目根**：`Config.ExecPath(proj)` 返回的目录，按 `server.path_view` 取 `host_path` 或 `container_path`（G002）。
- **上下文目录（base）**：文本所在对象对应的子目录，相对项目根。issue 取其 tracker 仓库的 `rel_path`，todo 与 job 取 `cwd`，计划取空（项目根）。
- **文档路径**：文本里被识别为项目内文档的相对路径，见「识别规则」。
- **阅读页**：本设计新增的 web 路由，渲染一个文档。

## 范围与非目标

范围：

- server：新增 `GET /v1/projects/{key}/doc?path=<rel>&base=<rel>`，只读、限制在项目根内、扩展名白名单、大小上限。
- server：抽出带符号链接复核的项目内路径解析函数，新接口与已有的关键文件接口共用（见「已确认事实」F6）。
- web：路径识别工具、纯文本字段的链接化组件、`MarkdownBlock` 的文档链接改写、阅读页路由。
- 覆盖的文本：计划描述、todo 标题与备注、issue 描述 / design / 备注 / 评论、job 汇报（验收面板）。
- 文档：`docs/reference/http-api.md`、`docs/reference/permissions.md`（生成）、`skills/gofer-usage/references/web-console.md`（G045）。

非目标：见文首声明。远程 worker 与 worktree 的后续做法写在「待确认事项」。

## 已确认事实与规范

以下均在 2026-11-09 对仓库 main 核对。

- F1 web 已有渲染组件。`web/src/components/MarkdownBlock.vue:20-25`：`marked.parse` → `DOMPurify.sanitize` → `v-html`，可选 `breaks` 与 `externalLinks`（在消毒后给 `<a` 追加 `target/rel`）。`web/src/components/FilePreview.vue:14,79-82` 接收 `name + Blob`，`.md` / `.markdown` 交给 `MarkdownBlock`，另有图片、JSON、文本和回退。
- F2 `FilePreview` 的两个数据源：job 产物走 `GET /v1/jobs/{id}/artifacts/{name}`（`web/src/api/client.ts:1177`，`fetchArtifactBlob`）；项目关键文件走 `GET /v1/projects/{key}/file?path=<rel>`（`web/src/api/client.ts:398`，`web/src/views/Projects.vue:153-161` 把内容包成 Blob）。`SessionDrawer.vue:258` 只是注释提到同一约定，未引用 `FilePreview`。
- F3 已有按项目读文件接口：`internal/httpapi/server.go:1412-1415` 注册 `/projects/{key}/file`，处理函数 `internal/httpapi/project_browse_handler.go:59-89`，逻辑在 `internal/project/browse.go:245-294`（`ReadKeyFile`）。限制：basename 白名单（`browse.go:44-48`，仅 README / AGENTS.md / CLAUDE.md / go.mod / package.json / LICENSE / .gitignore 等）、`maxKeyFileBytes = 256KB` 超出截断并返回 `truncated`（`browse.go:51,278-282`）、只读普通文件、含 NUL 判为二进制返回 415（`browse.go:259,284-286`）。docs 下的设计稿不在白名单内，读不到。
- F4 路由权限：`internal/httpapi/authz_routes.go:807-809`，`GET /v1/projects/{key}`、`/file`、`/git` 均为 `Public()` + `PathProject("key")`。没有独立的「项目读」权限；按项目的限制来自调用方范围上限，`internal/httpapi/authz_ceiling.go:695-700` 对 `PathProject` 路由判项目是否在上限内，不在返回 404。新路由按 G056 必须在 `routeDecls` 声明。
- F5 路径防护工具：`internal/project/path.go:73-101` 的 `SafeJoin` 拒绝 POSIX / Windows 绝对路径与 `..` 逃逸，但只做字面检查，不解析符号链接；`internal/store/safejoin.go:22` 的 `SafeJoinUnder` 额外用 `EvalSymlinks` 复核真实路径仍在 base 内；`internal/util/path.go:20` 的 `RealPath` 解析已存在前缀的符号链接。
- F6 由 F3、F5 推出：`ReadKeyFile` 用的是不复核符号链接的 `SafeJoin`，项目内一个名为 `README.md`、指向项目外的符号链接会被读出。新接口不能沿用这一点。
- F7 server 是否能读到项目目录：`internal/project/registry.go:207-214` 在 `allowed_runners` 非空且不含 `local` 时把项目视为「只在 worker 上运行」，跳过本机文件检查；判定函数 `AllowsLocalRunner`（`registry.go:303-313`，列表为空视为可本机运行）。worker 也可以有不在 server 配置里的自有项目（`web/src/views/Projects.vue:6,212`）。`handleGetProjectFile` 目前不看这个判定，会去读 server 本机同名路径。
- F8 经 worker 取文件的现有通道：`gofer tool xfer` 的 get 方向（`internal/worker/xfer.go` 的 `runFileXfer`，在 worker 侧用 `project.SafeJoin` 解析）。它需要 `xfer.write`（危险级，`authz_routes.go:685`），按传输任务异步执行，不适合作为阅读接口。
- F9 project 与仓库是一对多：`project.DiscoverRepos`（`browse.go:180`）在项目根下最多 3 层找子仓库；tracker 仓库记录 `ProjectKey` 与 `RelPath`（`internal/jobstore/tracker.go:14-25`），即 issue 所在仓库 = 项目根 + `rel_path`。todo 有 `ProjectKey`（缺省用计划的 `Plan.ProjectKey`）和 `Cwd`（`internal/jobstore/todos.go:64,75`；`plans.go:64`），job 有 `project_key` 与相对项目根的 `cwd`（`internal/job/model.go:17,83-85`）。
- F10 worktree：job 结果的 `worktree_path` 是执行机上的绝对路径（`internal/job/model.go:973-978`），默认落在仓库顶层的 `.worktrees/{name}`（`internal/config/project_worktree.go:16`），可由项目 `worktree.dir` 改。
- F11 各处文本的现状渲染：计划描述是纯文本插值（`web/src/views/PlanDetail.vue:1147`）；todo 标题纯文本（`PlanDetail.vue:1501`），todo 备注走 `MarkdownBlock breaks external-links`（`TodoDrawer.vue:359`）；issue 描述走 `MarkdownBlock`（`IssueDrawer.vue:55`），备注与评论是纯文本 `<p>`（`IssueDrawer.vue:57-60`）；job 汇报走 `MarkdownBlock`（`ReviewPanel.vue:434,472`）；通用评论组件 `CommentThread.vue:64-74,121-123` 已有把文本切段（识别 @ 提及）的写法。
- F12 issue 的 design 字段：tracker 模型有 `design`（`internal/tracker/model.go:14`），web 类型 `TrackerIssueView`（`web/src/api/types.ts:3009`）没有这个字段，web 不显示它。计划没有专门的文档字段，只有 `Description`。
- F13 `MarkdownBlock` 里 Markdown 链接 `[x](docs/a.md)` 现在渲染成相对 `href`，web 用 history 路由（`web/src/router.ts:1,6`），点了会跳到不存在的前端路径。marked 版本 18（`web/package.json:21`）默认不给标题生成 id。
- 规范：G002（ExecPath）、G021–G024（分层）、G031（不写业务名）、G045 / G046（对外文案）、G056（路由声明）；本设计不改 schema，G057 不涉及。

## 总体方案

一句话：server 加一个按项目、按扩展名白名单、带符号链接复核的只读文档接口；web 把各处文本里的文档路径变成指向阅读页的链接，阅读页调接口取内容后用现有 `MarkdownBlock` 渲染。

### 识别规则

- 形态：相对路径，最后一段以 `.md` 或 `.markdown` 结尾（大小写不敏感），可选后缀 `:<行号>` 或 `#<锚点>`；可以被反引号包裹（Markdown 里即行内代码）。例：`docs/plans/a.md`、`` `docs/design/b.md:42` ``、`AGENTS.md#配置`。
- 不识别：`/`、`~`、`\`、盘符开头的绝对路径；含 `://` 的 URL；含空格的片段；含 `..` 段的路径（避免误导，服务端也会拒绝）；Markdown 围栏代码块内的内容。
- 路径段以 `.` 开头的不链接（与服务端规则一致，见「读取接口」）。
- 根：链接带上文本所在对象的项目 key 与上下文目录（名词 base）。服务端先按 `base/path` 找，找不到再按项目根下的 `path` 找，返回实际命中的项目内路径。这样同时兼容「相对 issue 所在仓库」和「相对整个项目根」两种写法（F9）。
- 对象到项目与 base 的映射：

| 文本来源 | 项目 | base |
|---|---|---|
| 计划描述 | `plan.project_key` | 空 |
| todo 标题 / 备注 | todo 的 `project_key`，缺省取计划的 | todo `cwd` |
| issue 描述 / design / 备注 / 评论 | tracker 仓库的 `project_key` | tracker 仓库的 `rel_path` |
| job 汇报 | job `project_key` | job `cwd` |

  没有项目 key 的对象（例如计划未设项目）不做链接化，保持纯文本。

### 读取接口

`GET /v1/projects/{key}/doc?path=<rel>&base=<rel>`，响应：

```json
{"path": "docs/plans/a.md", "size": 1234, "content": "...", "truncated": false}
```

处理顺序（`internal/project` 内实现）：

1. 项目不存在 → 404；`AllowsLocalRunner` 为假 → 409，错误码 `project_not_local`（F7）。
2. `path`、`base` 都必须是相对路径：拒绝 POSIX / Windows 绝对路径、UNC、`..` 段、NUL；任一路径段以 `.` 开头（如 `.env.md`、`.git/`、`.worktrees/`）→ 403。
3. 扩展名白名单：`.md`、`.markdown`（第一期，是否加 `.txt` 见待确认 Q5）。不在白名单 → 403。白名单而非黑名单，`.env`、私钥、配置文件天然读不到。
4. 候选依次为 `base/path`、`path`；对每个候选取项目根真实路径与文件真实路径（解析全部符号链接），要求后者在前者之内，否则视为不存在。
5. 只接受普通文件；读到 256KB + 1 字节判断截断（沿用 F3 的上限）；含 NUL → 415。
6. 所有失败的错误详情不含绝对路径。

权限：与 `/file` 一致，`Public()` + `PathProject("key")`，读路由；受调用方范围上限约束（F4）。不新增权限位，`permissions.md` 只多一行路由。

### 阅读界面

- 路由 `/projects/:key/doc?path=<rel>&base=<rel>`，可复制分享（待确认 Q2）。从抽屉内点链接时用路由跳转，浏览器后退回到原页面。
- 页面：顶部显示项目 key、命中的项目内路径、截断提示、「复制路径」；正文用 `MarkdownBlock`；右侧（窄屏折叠到顶部）显示 h1–h3 标题目录，标题 id 在消毒后由前端按 `doc-h-<序号>` 生成，避免与 DOM 属性冲突。
- `:行号` 后缀只在顶部显示「引用第 N 行」，不定位（Markdown 渲染后没有可靠的行映射）；`#锚点` 按标题文本匹配后滚动。
- 文档内链接：相对 `.md` 链接按当前文档所在目录解析成新的阅读页链接（越出项目根则不链接）；`http(s)` 链接新标签页打开；其他相对链接（源码、图片文件）显示为普通文本加路径提示。
- 文档内图片：第一期不加载，渲染成「图片：<路径>」占位（待确认 Q6）。
- 读不到时按错误码给出文案：`project_not_local`（「该项目在 worker 上运行，server 读不到文件」）、404、403（类型不允许）、415。

### worktree 文件

第一期不读 worktree（待确认 Q4）。job 汇报里的路径按 job 的项目与 `cwd` 在主 checkout 里解析；找不到且 job 有 `worktree_path` 时，提示「文件可能只在该 job 的 worktree（分支 `<worktree_branch>`）里，合并后可读」。

## 架构

```mermaid
flowchart LR
  T[计划/todo/issue/job 文本] -->|docPath 识别| L[阅读页链接]
  L --> R[DocReader 路由]
  R -->|GET /v1/projects/key/doc| H[httpapi 处理函数]
  H --> P[project.ReadDoc]
  P --> F[(server 本机项目根)]
  R --> M[MarkdownBlock marked + DOMPurify]
```

代码组织：

- `internal/project/doc.go`：`ReadDoc(cfg, proj, base, rel)` 与错误值；`internal/project/path.go` 增加带符号链接复核的 `ResolveInside(root, rel)`，`ReadKeyFile` 改用它（F6）。项目层是数据层，不 import job / httpapi（G022）。
- `internal/httpapi/project_browse_handler.go`：新增薄处理函数，只做取参、调 `ReadDoc`、错误映射（G021）；`server.go` 注册路由；`authz_routes.go` 声明；`go generate ./internal/httpapi/...` 更新 `permissions.md`（G056）。
- `web/src/utils/docPath.ts`：纯函数，识别、拆后缀、相对路径解析，可单测。
- `web/src/components/DocLinkText.vue`：纯文本字段用，按 `CommentThread` 的切段写法输出文本与链接（F11）。
- `web/src/components/MarkdownBlock.vue`：新增可选 `docContext`（项目 key + base）；消毒之后对 DOM 做两件事：改写相对 `.md` 链接、把整段匹配的行内 `code` 包成链接。不传 `docContext` 时行为不变。
- `web/src/views/DocReader.vue` + `router.routes.ts` 新路由；`api/client.ts` 新增 `getProjectDoc`。
- 参照：同仓库 `Projects.vue` 关键文件（F2、F3）是同类读取的现有写法；同工作区的 Markdown 预览工具 markview 对相对链接的处理可作参考，不引入依赖。

## 关键流程

1. 用户在 issue 抽屉看到评论「设计见 `docs/design/x.md`」，`DocLinkText` 用 tracker 仓库的 `project_key` 与 `rel_path` 生成链接 `/projects/<project>/doc?path=docs/design/x.md&base=<rel_path>`。
2. 点击后进入阅读页，调用 `/v1/projects/<project>/doc`；服务端先找 `<rel_path>/docs/design/x.md`，不存在再找 `docs/design/x.md`，返回命中路径与内容。
3. 阅读页渲染，文档里的 `[计划](../plans/y.md)` 被改写成阅读页链接，继续点开。
4. 项目只在 worker 上运行时，第 2 步返回 409，阅读页显示原因；文本里的链接仍然显示（是否预先灰显见待确认 Q3）。

## 安全、数据、运维与回滚

- XSS：内容一律经 `MarkdownBlock` 的 DOMPurify 消毒（F1）；链接改写、标题 id、占位都在消毒之后做，只设置由前端生成的常量属性和经 `docPath` 校验过的路径，不放松白名单。
- 敏感文件：扩展名白名单 + 拒绝点开头的路径段；`.env`、`.git`、`.claude`、`.worktrees` 等都读不到。残余风险：项目内非隐藏目录下的 `.md` 对有项目可见性的调用方可读，与 agent 在该项目里能读到的范围一致；是否只允许 git 跟踪的文件见待确认 Q7。
- 越界：项目根与目标都取真实路径后比较，拦截 `..`、绝对路径和符号链接逃逸；同时修掉 `ReadKeyFile` 的符号链接问题（F6），这是已有接口的安全修正，行为变化只在「白名单文件是指向项目外的符号链接」时由可读变为 404。
- 资源：单次读取上限 256KB，只读文件不执行命令；没有缓存与新存储。
- 数据：不改 schema、不改 wire 字段（G057 不涉及）；web 给 `TrackerIssueView` 补上已有的 `design` 字段属于读取已存在的数据。
- 运维：无配置项。回滚即回退本批提交；接口是新增的，老 web 不调用它。

## 决策

- D1 新增 `/doc` 接口，不扩大 `/file` 的白名单：两者的放行规则不同（basename 白名单 vs 扩展名白名单加目录规则），合在一起会让关键文件接口的语义变宽。
- D2 根的解析由服务端做「先 base 后项目根」，web 不猜：只有服务端能判断文件是否存在。
- D3 第一期只支持 `AllowsLocalRunner` 为真的项目，其余返回 409 并在 UI 说明（推荐项，最终见 Q3）。
- D4 路径识别只在 web 端做，server 存的文本不变。
- D5 共用一个带符号链接复核的解析函数，并用于 `ReadKeyFile`。

## 待确认事项

- Q1 接口形态：新增 `/v1/projects/{key}/doc`（推荐，见 D1），还是给 `/file` 加 `kind=doc` 参数。
- Q2 阅读界面：独立路由 `/projects/:key/doc`（推荐，可分享、可后退），还是在当前页开抽屉（不离开上下文，但不能分享）。
- Q3 远程 worker 上的项目：第一期返回 409 并说明，链接照常显示、点开后看到原因（推荐）；备选是经 worker 新增只读取文件帧（复用 `ReadDoc`，worker 侧已有项目解析，F8），列为第二期。
- Q4 worktree 文件：第一期不读，找不到时给出 worktree 提示（推荐）；第二期可加 `job=<id>` 参数，按 job 读权限、仅限本机 runner 且 `worktree_path` 真实路径在项目根内时读取。
- Q5 扩展名白名单：只放 `.md` / `.markdown`（推荐）；是否加 `.txt`。
- Q6 文档内图片：第一期占位不加载（推荐）；第二期加图片读取（png / jpg / gif / webp，不含 svg）。
- Q7 是否只允许 git 已跟踪的文件：不限制（推荐，job 刚写出、未提交的文档正是主要用途），靠白名单与隐藏目录规则控制范围。
- Q8 覆盖的文本范围：按「识别规则」表格的五类（推荐）；job prompt 与会话消息是否也纳入。
- Q9 issue 的 design 字段：web 抽屉补显示该字段并链接化（推荐），还是只做描述与评论。

## 结论与人工计划 Gate

本稿给出的方案是：新增一个按项目读 Markdown 的只读接口（扩展名白名单、隐藏路径拒绝、符号链接复核、256KB 上限，权限与项目详情一致），web 把五类文本里的文档路径链接到可分享的阅读页，复用现有消毒渲染；远程 worker 项目与 worktree 文件放第二期。

测试与反向验证思路：

- `project` 层表驱动测试：正常命中、base 优先与回退、`..` / 绝对路径 / 盘符 / UNC、点开头段、非白名单扩展名、指向项目外的符号链接（文件与中间目录两种）、目录与特殊文件、截断、NUL；`ReadKeyFile` 的符号链接逃逸补回归测试（修复前应失败）。
- `httpapi` 测试：错误码映射、`project_not_local`、范围上限外的项目返回 404；`TestEveryRouteIsDeclaredOrPending` 与 `TestAuthzReferenceIsUpToDate` 通过。
- web 单测：`docPath` 的识别正反例（URL、绝对路径、围栏代码内不识别）；`MarkdownBlock` 在有 / 无 `docContext` 时的输出；含 `<script>`、`javascript:` 链接的文档渲染后不含可执行内容。
- 反向验证：在测试项目里放 `.env`、`.git/config`、指向项目外的 `escape.md` 符号链接，逐个确认读不到；去掉真实路径复核后确认对应用例失败。

需要用户：逐项答复 Q1–Q9（或接受全部推荐项），并明确批准进入计划编写。批准设计不等于授权实施。
