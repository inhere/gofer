<!-- template_id: plan; template_version: 1.3.0 -->
# web 内置 Markdown 文档阅读 实施计划

> 状态：Draft 0.1 / 已批准（2026-11-09，用户：「F3 选 A，三份计划批准」）
> issue：gofer-i1bd

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-09 | Claude（gofer 监督会话） | 初稿 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：计划、todo、issue、job 汇报和评论里的项目内 Markdown 文档路径在 web 上可点开，在 web 内阅读。Scope freeze：只实施设计 Draft 0.1（已批准，待确认事项全按推荐项）的范围和下文“对设计的细化”。Review budget：一轮合并轴评审，有阻塞项才继续，不建 review ledger。停止条件：评审无 `CORE_BLOCKING`，停在计划 Gate。

## 目标与完成定义

目标：五类文本（计划描述、todo 标题与备注、issue 描述 / design / 备注 / 评论、job 汇报）里的项目内 `.md` / `.markdown` 相对路径显示为链接，点开进入可分享的阅读页；server 只读、只在项目根内、只读白名单扩展名；读不到时显示明确原因。

完成定义：T0–T9 通过；设计“结论”列出的测试与反向验证都有证据；真实浏览器走通一次（或按 T8 写 `UNVERIFIED_REAL_ENV`）；`gofer-verify` 全绿；文档按 G045 同步；设计稿补实施记录。

## 范围、排除项与授权

范围：`GET /v1/projects/{key}/doc` 只读接口（路径规则、根解析“先 base 后项目根”、扩展名白名单、符号链接复核、大小上限、远程项目 409）；路由声明与权限参考（G056）；web 路径识别、纯文本链接化、`MarkdownBlock` 链接改写、阅读页与标题目录；web 显示 issue 的 `design` 字段；i18n；`docs/reference/http-api.md`、`skills/gofer-usage/references/web-console.md`、CHANGELOG「未发布」。

排除：设计“非目标”全部（编辑、搜索与目录树、经 worker 读文件、worktree 文件、图片显示、改 marked / DOMPurify 版本或配置）；`.txt` 等其他扩展名；只限 git 跟踪文件；job prompt 与会话消息的链接化；带符号链接复核的解析函数本身及 `ReadKeyFile` 的修复（由 gofer-8kgi 完成，本计划只复用）。

授权边界：批准本计划不等于授权执行，执行需要单独的当前请求。本地提交按既有默认授权；push、打 tag、发版不在本计划内（用户要求攒一批再发）。真实验收只用临时 server、临时配置目录和临时项目目录，不改用户配置和线上 server。

- `host_or_non_offline_action=REQUIRED`

需要的主机动作：建、删 worktree；suga 实施 job；T8 经 `gofer job --runner local` 在主机构建并起临时 server、浏览器验收；`gofer-verify`。脚本和记录写工作区 `tmp/i1bd-impl/`。

## 输入与批准证据

- 设计：`docs/design/2026-11-09-web-doc-reader-design.md` Draft 0.1。用户 2026-11-09：“设计都按推荐项批准”，Q1–Q9 全按推荐项定案（新增 `/doc` 接口；独立路由；远程 worker 项目 409 并在 UI 说明；worktree 文件第一期不读；只 `.md` / `.markdown`；图片占位；不限 git 跟踪；五类文本链接化；web 补显示 issue design 字段并链接化）。
- 前置 issue：gofer-8kgi（新增带符号链接复核的项目内路径解析函数，`ReadKeyFile` 改用它），对应设计 D5。本计划以它已合入 main 为前提。
- 规范：G002、G021–G024、G031、G045、G046、G056；本计划不改 schema，G057 不涉及。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 项目内路径解析与符号链接复核 | `project.SafeJoin`、`store.SafeJoinUnder`、`util.RealPath`、gofer-8kgi 新增函数 | 是 | 直接调用 gofer-8kgi 的函数 | DIRECT_REUSE | none | 不另写解析逻辑，避免两套规则分叉 |
| CAP-02 | 读文件、截断、二进制判定 | `project.ReadKeyFile`（`browse.go`）、`maxKeyFileBytes` | 是 | 同包新增 `ReadDoc`，复用上限常量与 NUL 判定写法 | OWNER_EXTENSION | 白名单规则不同（D1），不能扩 `ReadKeyFile` | 同一包内，上限常量共用一处 |
| CAP-03 | Markdown 渲染与消毒 | `MarkdownBlock.vue`、`FilePreview.vue` | 是 | `MarkdownBlock` 加可选 `docContext`，不传时行为不变 | OWNER_EXTENSION | none | 不新增渲染组件 |
| CAP-04 | 纯文本切段输出链接 | `CommentThread.vue` 的 @ 提及切段 | 部分 | 新组件 `DocLinkText.vue` 按同一写法实现 | OWNER_EXTENSION | 提及切段与路径识别规则不同，抽公共层收益小 | 识别逻辑只在 `docPath.ts` 一处 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| NONE | none | none |

## 对设计的细化

代码核对发现设计没定或说法不够具体的地方。下面按最小方案定下，批准本计划即确认。都不改变设计的用户结果。

| # | 问题 | 决定 |
|---|---|---|
| R1 | issue 所在仓库的 `rel_path` 根仓为 `.`（`web/src/api/types.ts:1450`） | `base` 为空或 `.` 都视为项目根；服务端先 `path.Clean` 再判断，`base` 也走第 2 步全部规则 |
| R2 | NTFS 备用数据流：`secret:x.md` 扩展名是 `.md`，实际读的是 `secret` 的数据流 | `path`、`base` 任一字符为 `:` 或 `\` 一律 403（也挡住盘符写法）；web 识别时把 `:<行号>` 后缀拆掉，只把纯路径发给服务端 |
| R3 | 扩展名大小写 | 白名单比较不分大小写（`.MD` 可读）；点开头段判断与大小写无关 |
| R4 | 解析函数名 | 以 gofer-8kgi 合入后的代码为准，T0 写进 `tmp/i1bd-impl/facts.md`，任务书引用实际名字；下文用“8kgi 解析函数”指代 |
| R5 | 远程项目判定放哪层 | `ReadDoc` 第一步检查 `project.AllowsLocalRunner(proj.AllowedRunners)`，为假返回 `ErrProjectNotLocal`；处理函数映射为 409，`error` 字段 `project_not_local` |
| R6 | 错误体 | 沿用 `writeError(c, status, msg, detail)`：400 `missing path`；403 `forbidden doc`；404 `no such file`（不存在、越界、符号链接逃逸、目录、特殊文件都是它，detail 固定为 `file not found`）；409 `project_not_local`；415 `binary file`。detail 不含任何绝对路径 |
| R7 | 响应 `path` | 命中候选相对项目根的斜杠路径（`base/path` 命中时含 base），不返回绝对路径 |
| R8 | 阅读页内链接如何走路由 | `v-html` 里不能放 `RouterLink`；改写出的 `<a>` 带 `data-doc-link` 和真实 `href`（阅读页 URL），`MarkdownBlock` 在根元素上监听点击：无修饰键时 `preventDefault` 后 `router.push`，有 Ctrl / Meta / Shift / 中键时走浏览器默认（新标签） |
| R9 | worktree 提示的数据来源 | job 汇报生成的链接在 job 有 `worktree_path` 时额外带 `wtb=<worktree_branch>` 查询参数，只供阅读页 404 时显示提示，不发给服务端 |
| R10 | 计划没有项目 key、todo 与计划都没有项目 key | 不链接化，保持纯文本（`docContext` 为空） |
| R11 | issue 抽屉的数据是否带 `design` | 详情接口返回 `tracker.Issue`，含 `design`（`internal/tracker/model.go:14`）；T6 核实抽屉实际使用的数据源，若列表数据不带则改读详情，不改 server |

## 前置检查与 fail-closed 条件

workspace baseline：

- Git root：`inhere-tools/gofer`，分支 `main`，基线为提交本计划之后、且 gofer-8kgi 已合入的 commit。
- 同一 checkout 里有其他 agent 在改 `internal/project` 与写其他文档；开 worktree 前只核对自己相关的文件已提交，不处理别人的未提交改动。
- 实施在 `.worktrees/i1bd`（分支 `feat/i1bd-doc-reader`），合并用 `--no-ff`，合并后删 worktree 和分支。worktree 在主机建。
- expected paths 见各任务；实施发现新路径按四类记录。

fail-closed，以下任一情况停止并回到设计：

- gofer-8kgi 未合入，或其函数不能对“根真实路径 + 相对路径”给出“真实路径在根内”的判定（例如只做字面检查）。
- T1 发现符号链接逃逸、点开头段、`:` / `\`、非白名单扩展名中任一项在测试里能读出内容。
- T4 发现链接改写需要放宽 DOMPurify 配置或在消毒前改 HTML 才能实现。
- T8 中任一反向样本（`.env.md`、`.git/config`、`escape.md` 符号链接、`secret.txt`、超大文件）在真实 server 上能读出内容。

派活约束：任务书写明 G021–G024、G031、idevstd 加载方式、只暂存自己的文件；不做无关重排，不删注释；不在 export 了 `GIT_DIR` / `GIT_WORK_TREE` 的命令里跑测试；不起后台空等；suga job 用 `--sync`。子 agent 与 suga 的完成报告只是声明，监督会话逐条核实（先看 `git diff --stat` 删除行）并做反向验证。

## 波次与依赖

```text
W0  T0 前置核对（监督会话，不改代码）
W1  T1 project.ReadDoc → T2 /doc 接口与路由声明（sonnet 子 agent）
W2  T3 docPath 与 client → T4 链接化组件与 MarkdownBlock → T5 阅读页 → T6 接入五类文本与 design 字段 → T7 文档与 CHANGELOG（suga，gpt）
W3  T8 真实验收（监督会话）→ T9 反向验证、全量与收尾（监督会话）
```

所有任务在同一 worktree 顺序提交，每个任务一个提交。T3 逻辑上只依赖 T0，但为避免同一 worktree 并发提交，排在 T2 之后。suga 模型按当时可用的 `openai-codex/gpt-6.1-terra` 或 `gpt-6-sol`，实际所用写进实施记录。

## 任务

命令的运行目录都是 Git root（worktree）。server 任务提交前跑 `go build ./... && go vet ./...`；web 任务提交前在 `web/` 跑 `pnpm typecheck && pnpm test`。

### T0 前置核对

- 执行方：Claude（监督会话）。
- 文件: 只写工作区 `tmp/i1bd-impl/facts.md`。
- 动作: 确认 gofer-8kgi 已关闭且提交在 main；读出 8kgi 解析函数的名字、签名、错误值，写进 `facts.md`；确认 `ReadKeyFile` 已改用它；建 worktree。
- 验证: `git log --oneline main | grep -i 8kgi` 有记录；`go test ./internal/project -count=1` 在 main 通过；`facts.md` 写明函数签名。
- 完成标准: 不触发 fail-closed 第 1 条；任务书可以引用实际函数名。
- 依赖: gofer-8kgi 已合入。

### T1 `project.ReadDoc` 与安全测试

- 执行方：sonnet 子 agent。
- 文件: `internal/project/doc.go`（新）、`internal/project/doc_test.go`（新）。不改 `path.go`、`browse.go`（8kgi 已改，本任务只调用）。
- 动作:
  - `type DocContent struct { Path string; Size int64; Content string; Truncated bool }`，JSON 字段 `path`、`size`、`content`、`truncated`。
  - 错误值：`ErrProjectNotLocal`、`ErrForbiddenDoc`；不存在类错误包 `os.ErrNotExist`；二进制复用 `ErrBinary`。
  - `ReadDoc(cfg *config.Config, proj config.ProjectConfig, base, rel string) (DocContent, error)`，按设计“读取接口”的顺序：
    1. `AllowsLocalRunner` 为假 → `ErrProjectNotLocal`（R5）。
    2. `rel` 为空 → `ErrForbiddenDoc`（处理函数先挡 400）；`rel`、`base` 含 NUL、`:`、`\`（R2），是 POSIX 绝对路径，含 `..` 段，或任一段以 `.` 开头 → `ErrForbiddenDoc`。`base` 为空或 `.` 视为根（R1）。
    3. `rel` 最后一段扩展名不分大小写不在 `.md`、`.markdown` → `ErrForbiddenDoc`（R3）。
    4. 候选依次为 `base/rel`（base 非根时）、`rel`；每个候选用 8kgi 解析函数对 `cfg.ExecPath(proj)` 解析，解析失败或越界视为不存在，继续下一个；命中后要求 `Mode().IsRegular()`，否则视为不存在。
    5. 读 `maxKeyFileBytes+1` 字节，超出截断到 `maxKeyFileBytes` 并置 `Truncated`；含 NUL → `ErrBinary`。
    6. 返回的 `Path` 为命中候选的相对斜杠路径（R7）；所有错误信息不含绝对路径。
  - 只 import `config` 与标准库，不 import job / httpapi（G022）。
- 验证: `go test ./internal/project -run 'ReadDoc' -count=1`。表驱动用例（临时目录建项目根）：
  - 正向：根下命中；`base/rel` 优先于根下同名文件；`base` 下不存在时回退根；`base` 为 `.`；`.MD` 大写扩展名；正好 256KB 不截断、256KB+1 截断。
  - 反向：`../x.md`、`a/../../x.md`、`/etc/x.md`、`C:/x.md`、`C:x.md`、`\\srv\x.md`、`a\b.md`、`secret:x.md`、含 NUL；`.env.md`、`.git/x.md`、`docs/.hidden/x.md`、`base=.worktrees/a`；`x.txt`、`x.md.bak`、`x`；`escape.md` 为指向项目外文件的符号链接；`linkdir/x.md` 中 `linkdir` 为指向项目外目录的符号链接；项目根本身经符号链接访问时正常文件仍可读；目录名为 `d.md`；含 NUL 的 `.md` → `ErrBinary`；`allowed_runners: [w1]` → `ErrProjectNotLocal`，`allowed_runners` 为空或含 `local` 可读。
  - 每条反向用例断言错误不含临时目录绝对路径。Windows 上建不了符号链接时用例 `t.Skip` 并写明原因，容器里必须实际执行。
- 完成标准: 上述用例在容器全部通过（符号链接用例不跳过）；`go vet ./internal/project` 无告警。
- 依赖: T0。

### T2 `/doc` 接口、路由声明与权限参考

- 执行方：sonnet 子 agent。
- 文件: `internal/httpapi/project_browse_handler.go`、`internal/httpapi/server.go`、`internal/httpapi/authz_routes.go`、`docs/reference/permissions.md`（生成）、`internal/httpapi/project_doc_handler_test.go`（新，名字可按现有测试文件惯例调整）。
- 动作:
  - `handleGetProjectDoc`：取 `key`、`path`、`base`；项目不存在 404；`path` 为空 400；调 `project.ReadDoc`；按 R6 映射错误；成功 200 返回 `DocContent`。处理函数只做取参、调用与映射（G021）。
  - `server.go` 在 `/projects/{key}/file` 旁注册 `r.GET("/projects/{key}/doc", s.handleGetProjectDoc)`。
  - `authz_routes.go` 增加 `"GET /v1/projects/{key}/doc": {user: Public(), scope: PathProject("key")}`（G056），与 `/file` 同级。
  - `go generate ./internal/httpapi/...` 更新 `docs/reference/permissions.md`，提交生成结果。
- 验证: `go test ./internal/httpapi -run 'ProjectDoc|TestEveryRouteIsDeclaredOrPending|TestAuthzReferenceIsUpToDate' -count=1`。经真实路由的用例：200 正常与 `base` 回退；400 缺 `path`；403 `.env.md` 与 `x.txt`；404 不存在与 `../`；409 `project_not_local`；415 NUL；调用方范围上限不含该项目时 404（参照 `authz_ceiling_test.go` 现有写法）；所有错误响应体不含临时目录路径。
- 完成标准: 上述用例通过；`permissions.md` 只多出这一行路由；`git diff --stat` 无无关文件。
- 依赖: T1。

### T3 web 路径识别纯函数与 API 客户端

- 执行方：suga（gpt）。
- 文件: `web/src/utils/docPath.ts`（新）、`web/src/utils/docPath.test.ts`（新）、`web/src/api/client.ts`、`web/src/api/types.ts`。
- 动作:
  - `docPath.ts` 导出纯函数：`findDocPaths(text)` 返回 `{start, end, path, line?, anchor?}[]`；`parseDocRef(raw)` 拆出路径与 `:行号` / `#锚点`；`resolveDocHref(fromDir, href)` 按当前文档目录解析相对链接，越出根返回 `null`；`isDocPath(path)` 供 `MarkdownBlock` 判断行内代码；`docReaderLocation(ctx, ref)` 生成阅读页路由对象（`ctx` 为 `{projectKey, base?, worktreeBranch?}`）。
  - 识别规则按设计“识别规则”：相对路径、末段 `.md` / `.markdown`（不分大小写）、可选后缀；不识别绝对路径、`~`、`\`、盘符、含 `://`、含空格、含 `..` 段、任一段以 `.` 开头。围栏代码由调用方排除（纯文本里按行首 ```` ``` ```` 成对跳过）。
  - `client.ts` 新增 `getProjectDoc(key, path, base?)`；`types.ts` 新增 `ProjectDoc`。错误按现有 `client.ts` 的错误类型抛出，带 HTTP 状态。
- 验证: `cd web && pnpm test -- docPath`。正例：`docs/plans/a.md`、`` `docs/design/b.md:42` ``、`AGENTS.md#配置`、`README.MD`、中文标点紧跟路径（`见 docs/a.md。`）；反例：`/abs/a.md`、`~/a.md`、`C:\a.md`、`a\b.md`、`https://x/a.md`、`my doc.md`、`../a.md`、`.env.md`、`docs/.x/a.md`、`a.txt`、围栏代码内的路径。`resolveDocHref`：`../plans/y.md` 正常、`../../../y.md` 越界为 `null`、`#h` 与 `http(s)` 不按文档处理。
- 完成标准: 用例通过；`pnpm typecheck` 通过。
- 依赖: T2（同一 worktree 顺序提交）。

### T4 纯文本链接化组件与 `MarkdownBlock` 改写

- 执行方：suga（gpt）。
- 文件: `web/src/components/DocLinkText.vue`（新）、`web/src/components/MarkdownBlock.vue`、对应 `*.test.ts`（新，按 `web/src/components/*.test.ts` 惯例）。
- 动作:
  - `DocLinkText`：props `text`、`docContext?`；用 `findDocPaths` 切段，路径段输出 `RouterLink`，其余原样文本（插值，不用 `v-html`）；`docContext` 为空时整段纯文本（R10）。
  - `MarkdownBlock` 新增可选 `docContext` 与 `docDir`（阅读页内当前文档目录）；`marked.parse` → `DOMPurify.sanitize` 之后，在 DOM 上（`template` 元素解析消毒后的字符串）做：
    - 相对 `.md` 链接：有 `docDir` 时按 `resolveDocHref` 解析，否则按 `docContext.base`；结果改写为阅读页 `href` 并加 `data-doc-link`；越界或不合规时摘掉 `<a>` 留文本。
    - 其他相对链接（源码、图片文件路径）：摘掉 `<a>`，留文本并把原 href 放 `title`。
    - `http(s)` 链接：`target=_blank`、`rel=noopener noreferrer`。
    - 不在 `pre` 内、整段内容满足 `isDocPath` 的行内 `code`：包成阅读页链接。
    - `<img>`：替换为文本节点「图片：<路径>」（i18n），不发请求。
    - 只用 `setAttribute` / `textContent` / `createElement` 写入前端生成的值，不再拼 HTML 字符串。
  - 点击拦截按 R8。不传 `docContext` 与 `docDir` 时输出与现在逐字相同。
- 验证: `cd web && pnpm test -- MarkdownBlock DocLinkText`。用例：无 `docContext` 时输出与改动前快照一致；`[x](docs/a.md)` 变阅读页链接；`[x](../../../a.md)` 越界不链接；`![i](a.png)` 变占位且无 `img`；`` `docs/a.md` `` 变链接、围栏代码内不变；含 `<script>`、`[x](javascript:alert(1))`、`<a href="javascript:...">`、`<img onerror=...>` 的 Markdown 渲染后不含 `script`、`javascript:`、`on*` 属性；`DocLinkText` 对 `<b>docs/a.md</b>` 文本只输出转义文本。
- 完成标准: 用例通过；`MarkdownBlock` 现有调用方行为不变（现有测试全过）。
- 依赖: T3。

### T5 阅读页、路由、目录与 i18n

- 执行方：suga（gpt）。
- 文件: `web/src/views/DocReader.vue`（新）、`web/src/router.routes.ts`、`web/src/router.test.ts`、`web/src/i18n/pages/` 下新增或扩展的页面词条文件（中英两份，按 `pages/index.ts` 现有登记方式）、`DocReader` 的 `*.test.ts`（新）。
- 动作:
  - 路由 `/projects/:key/doc`，查询参数 `path`、`base`、`line`、`anchor`、`wtb`（R9）。
  - 页面：顶部项目 key、命中路径、截断提示、`line` 存在时「引用第 N 行」、「复制路径」；正文 `MarkdownBlock`（传 `docContext` 与 `docDir`）；h1–h3 目录在右侧，窄屏折叠到顶部；标题 id 在消毒后按 `doc-h-<序号>` 生成；`anchor` 按标题文本匹配滚动。
  - 错误文案按状态：409「该项目在 worker 上运行，server 读不到文件」；404「文件不存在」，有 `wtb` 时追加「文件可能只在该 job 的 worktree（分支 `<wtb>`）里，合并后可读」；403「不允许读取这类文件」；415「不是文本文件」；400 缺参数。不显示空白页。
  - 所有文案走 i18n，中英两份。
- 验证: `cd web && pnpm test -- DocReader router i18n noHardcodedCjk`。用例：路由解析出参数；成功渲染与目录生成；409 / 404（含与不含 `wtb`）/ 403 / 415 各自文案；`anchor` 匹配；`i18n.test.ts`、`i18nKeyUsage.test.ts`、`noHardcodedCjk.test.ts` 通过。
- 完成标准: 用例通过；`pnpm typecheck` 通过。
- 依赖: T4。

### T6 接入五类文本与 issue `design` 字段

- 执行方：suga（gpt）。
- 文件: `web/src/views/PlanDetail.vue`、`web/src/components/TodoDrawer.vue`、`web/src/components/IssueDrawer.vue`、`web/src/views/Issues.vue`、`web/src/components/ReviewPanel.vue`、`web/src/views/JobDetail.vue`（传上下文，如需）、`web/src/api/types.ts`、`web/src/i18n/pages/issues.ts`、对应 `*.test.ts`。
- 动作（映射见设计“识别规则”表）:
  - 计划描述（`PlanDetail.vue:1147`）改用 `DocLinkText`，上下文 `{plan.project_key}`，无 key 不链接。
  - todo 标题（`PlanDetail.vue:1501`）用 `DocLinkText`；todo 备注（`TodoDrawer.vue:359`）给 `MarkdownBlock` 传 `docContext`；项目取 todo `project_key`，缺省取计划的，`base` 取 todo `cwd`。
  - issue：`Issues.vue` 把当前 `repo` 的 `project_key` 与 `rel_path` 传给 `IssueDrawer`；描述给 `MarkdownBlock` 传 `docContext`；备注与评论（`IssueDrawer.vue:58,61`）改用 `DocLinkText`。
  - issue `design`：`TrackerIssueView` 增加 `design?: string`；抽屉在描述之后新增「设计」区块，用 `MarkdownBlock` 加 `docContext`，字段为空不显示；按 R11 核实数据源。
  - job 汇报（`ReviewPanel.vue:434,472`）给 `MarkdownBlock` 传 `{job.project_key, base: job.cwd, worktreeBranch}`，`worktreeBranch` 仅在 job 有 `worktree_path` 时取 `worktree_branch`（R9）。
- 验证: `cd web && pnpm test -- PlanDetail TodoDrawer IssueDrawer ReviewPanel`（按实际存在的测试文件），用例至少覆盖：计划无项目 key 时纯文本；todo 缺省取计划 key；issue 链接带 `base=<rel_path>`，根仓 `.` 不出错；`design` 有值时显示并链接化、为空不显示；job 有 worktree 时链接带 `wtb`。
- 完成标准: 用例通过；`pnpm typecheck`、`pnpm test` 全量通过。
- 依赖: T5。

### T7 文档与 CHANGELOG

- 执行方：suga（gpt）。
- 文件: `docs/reference/http-api.md`、`skills/gofer-usage/references/web-console.md`、`CHANGELOG.md`。
- 动作:
  - http-api：在 projects 行加 `GET /v1/projects/{key}/doc?path=&base=`，并新增一节写响应字段、根解析顺序、扩展名白名单、点开头段与 `:` / `\` 拒绝、符号链接复核、256KB 截断、错误码表（含 409 `project_not_local`）、权限与 `/file` 相同。
  - web-console skill reference：写哪些文本里的文档路径可点开、阅读页地址可分享、只读 `.md` / `.markdown`、远程 worker 项目与 worktree 文件读不到及提示（G045）。按 G046 用通用视角，不写本仓库细节与业务名。
  - CHANGELOG「未发布」写一条新增功能（带 gofer-i1bd）。
- 验证: `PRIVATE_NAMES_FILE=<主 checkout 绝对路径>/.private-names sh scripts/check-private-names.sh`（worktree 里没有该文件）；人工核对文档与 T1、T2 代码一致。
- 完成标准: 三个文件更新；汇报列出改动文件。
- 依赖: T6。

### T8 真实验收（含浏览器）

- 执行方：Claude（监督会话）。
- 文件: 只写工作区 `tmp/i1bd-impl/`（脚本、截图、`acceptance.md`）。
- 环境:
  - 经 `gofer job --runner local --sync` 在主机用 worktree 按仓库现有方式构建（`make web` 后 `make build`，以 Makefile 为准），产物放 `tmp/i1bd-impl/bin/`。
  - 临时 server：临时端口、`GOFER_CONFIG_DIR` 指向 `tmp/i1bd-impl/conf/`、临时库；启动前清除 `GOFER_JOB_ID`、`GOFER_JOB_TOKEN`、`GOFER_SERVER_ADDR`、`GOFER_SERVER_TOKEN`、`GOFER_CONFIG`、`GOFER_RUN_MODE`。只按自己记录的 pid 停。
  - 临时项目 `doc-demo`（`tmp/i1bd-impl/proj/`）：`docs/plans/a.md`（含 `[b](../design/b.md)`、外链、图片、`../../../../x.md` 越界链接、h1–h3）、`docs/design/b.md`、`sub/docs/c.md`、`.env.md`、`.git/config`、`secret.txt`、超过 256KB 的 `big.md`；另一个临时项目 `remote-demo` 配 `allowed_runners: [w-x]`。符号链接 `escape.md` 指向项目外文件：主机建不了时在容器起同版本临时 server 补测这一项。
  - 用 API 在 `doc-demo` 建一个计划，描述含 `docs/plans/a.md`、`` `docs/design/b.md:3` ``、`.env.md`、`/abs/a.md`；建一个 todo 设 `cwd=sub`，标题含 `docs/c.md`。
- 动作:
  1. 浏览器（主机 Playwright / headless Chromium，或 Claude 可用的浏览器工具）打开临时 server 的计划详情页，截图；断言 `docs/plans/a.md`、`docs/design/b.md:3` 是链接，`.env.md`、`/abs/a.md` 不是。
  2. 点 `docs/plans/a.md` 进入阅读页：标题目录可见、外链带 `target=_blank`、图片显示占位、越界链接为纯文本；点文档内 `b` 进入 `docs/design/b.md`；浏览器后退回到 a、再后退回到计划页。
  3. 点 `docs/design/b.md:3` 链接，顶部显示「引用第 3 行」；复制阅读页地址在新标签打开同样可读。
  4. todo 标题里的 `docs/c.md` 解析到 `sub/docs/c.md`（顶部显示命中路径）。
  5. 反向：直接请求 `/v1/projects/doc-demo/doc` 读 `.env.md`、`.git/config`、`secret.txt`、`escape.md`、`../x.md`、`secret:x.md` 都读不到（403 / 404），`big.md` 返回 `truncated:true`，`remote-demo` 返回 409；阅读页对 409、404、403 显示各自文案（截图）。所有错误体不含临时目录绝对路径。
  6. 英文界面抽查一次阅读页与错误文案。
- 验证: `tmp/i1bd-impl/acceptance.md` 逐条记录命令、HTTP 状态、截图文件名与结论。浏览器在主机与容器都起不来时，第 1–4、6 项写 `UNVERIFIED_REAL_ENV: web 浏览器交互（复现：<启动命令> 后打开 <URL>）`，第 5 项的 HTTP 部分仍必须真实执行。
- 完成标准: 第 5 项全部有证据且未触发 fail-closed；第 1–4、6 项有截图证据或按上条披露。
- 依赖: T7。

### T9 反向验证、全量与收尾

- 执行方：Claude（监督会话）。
- 文件: 设计稿“实施记录”、本计划状态、`.gofer/tracker/*`。
- 动作:
  - 反向验证（临时副本里改，不提交）：`ReadDoc` 去掉 8kgi 解析函数改用 `SafeJoin`，符号链接逃逸用例必须失败；去掉点开头段检查，`.env.md` 用例必须失败；去掉 `:` 检查，`secret:x.md` 用例必须失败；扩展名白名单改为放行全部，`x.txt` 用例必须失败；去掉 `AllowsLocalRunner` 检查，409 用例必须失败；去掉 `authz_routes.go` 新行，`TestEveryRouteIsDeclaredOrPending` 必须失败；`docPath` 去掉 `..` 判断，对应 vitest 反例必须失败；`MarkdownBlock` 改成消毒前改写，XSS 用例必须失败。
  - `git diff --stat main...` 查无关删除；逐条对照设计核实 sonnet 子 agent 与 suga 的声明。
  - `gofer-verify` 工作流（四路）通过。
  - 合并回 main（`--no-ff`），删 worktree 和分支；设计稿补实施记录；计划进入 COMPLETE 时写不超过 8 行的完成总结；关 `gofer-i1bd`。
- 验证: 反向验证输出写入 `tmp/i1bd-impl/reverse.md`；`gofer wf show <id>` 全绿；主 checkout 里 `sh scripts/check-private-names.sh` 通过；`git log` 核对提交只含本任务文件。
- 完成标准: 见“目标与完成定义”。
- 依赖: T8。

## 回滚与恢复

- 恢复点：每个任务一个提交，worktree 隔离；合并前 main 不受影响。合并后退回用 `git revert -m 1 <merge>`。
- 运行时回滚：接口与路由都是新增，不改 schema 与 wire 字段，退回旧版后老 web 不调用 `/doc`，没有残留数据。
- T8 触发 fail-closed 时不合并，回到设计或修订本计划。临时 server 与临时目录用完即删，只留 `tmp/i1bd-impl/` 的记录。

## 人工 Gate

1. 批准本计划。
2. 当前执行请求（可与批准同一句）。
3. push、打 tag、发版：不在本计划内，攒批后按发版流程另行处理。

实施过程本身没有其他人工 Gate。除非出现 Semantic Amendment 或 fail-closed。

## 可追溯性

| 设计要求 | 任务 | 验证 |
|---|---|---|
| 只读接口：路径规则、白名单、符号链接复核、上限 | T1 T2 | T1、T2 用例，T8.5，T9 反向 |
| 根解析“先 base 后项目根” | T1 T6 | T1 用例，T8.4 |
| 远程 worker 项目 409 并在 UI 说明（Q3） | T1 T2 T5 | T2 用例，T5 用例，T8.5 |
| 权限与路由声明（G056） | T2 | `TestEveryRouteIsDeclaredOrPending`、`TestAuthzReferenceIsUpToDate` |
| 识别规则 | T3 | `docPath.test.ts` |
| 五类文本链接化（Q8）与 design 字段（Q9） | T4 T6 | T6 用例，T8.1、T8.4 |
| 阅读页、可分享、目录、行号与锚点（Q2） | T5 | T5 用例，T8.2、T8.3 |
| 文档内链接改写、外链新标签、图片占位（Q6） | T4 | T4 用例，T8.2 |
| XSS：消毒后改写、不放宽白名单 | T4 | T4 用例，T9 反向 |
| worktree 文件不读，给提示（Q4） | T5 T6 | T5、T6 用例 |
| 文档同步（G045、G046） | T7 | 人工核对，私名扫描 |

## 完成 Gate 与剩余工作

完成 Gate：T0–T9 通过；`gofer-verify` 全绿；T8 按完成标准有证据或披露；设计稿补实施记录；`gofer-i1bd` 关闭。

剩余工作（不在本计划内）：随下一批发版时 push 与发版；设计第二期（经 worker 读文件、worktree 文件、图片、`.txt`）另立 issue。
