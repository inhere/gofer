# 更新日志

本文件记录 gofer 每个版本中**用户可见**的变化（功能、行为变化、重要修复、兼容性说明），格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)（1.0 之前 minor 版本也可能含不兼容变化，见各条「变更 / 移除」）。

维护方式：

- 每次发版（打 tag）时新增一个版本条目；内容来源是**上一个 tag 到本 tag 之间的提交**（`git log <上一个 tag>..<本 tag>`），只挑用户可见的变化，纯重构、测试、tracker / 记忆同步、设计文档不写。
- 日期取 tag 日期；条目尽量带路线图编号（见 [`docs/gofer-enhancements-roadmap.md`](docs/gofer-enhancements-roadmap.md)）或 issue id。
- 尚未发版的用户可见改动先记在「未发布」，发版时整段移入新版本。
- v0.100.0 起逐版本记录；更早的版本按主题区间压缩记录（见文末「早期版本」）。

## [未发布]

### 改进

- **Web 工作流列表分页（每页 50 条）**：`/workflows` 由「整表返回、只显示最新 200 条」改成服务端分页，页脚可上下翻页、页码写进 URL 的 `?page=`（可分享、后退可复原），切状态过滤回到第 1 页；页头与页脚显示当前过滤下的总条数与第几页。为此 `GET /v1/workflows` 新增 `limit` / `offset` 参数并返回 `total`（不带参数时与以前一样返回整段列表）；没有 `job.read.others` 的调用方「只看自己的」这一限制改为随查询下推，一页不再被过滤后剩半页。状态 chip 不再带计数（分页后拿不到各状态的总数，总数显示在页头 / 页脚）。

### 新增

- **常用 CLI 命令的 `--help` 与输出按语言显示（gofer-992q，阶段 2 之一）**：`gofer job`（含 `review` / `findings` / `worktree merge` / `resume` / `show` / `approve` 等）、`gofer plan`、`gofer session` 三个命令组的说明、flag 说明、提示与错误现在支持 `en` / `zh-CN`。语言来源：`GOFER_LANG` → 系统语言（`LANG` / `LC_ALL`，Windows 取界面语言）→ 缺省；`--json` 等机器可读输出不翻译。中文输出与以前逐字一致（有帮助文本 golden 守住）。

### 修复

- 收集箱条目转为 issue / todo 后正确结束为 `done`，保留 issue / todo 链接；启动迁移会修正旧版已转出但仍为 `parked` 的条目。

## [0.199.1] - 2026-10-27

### 修复

- **macOS 上 `TestRepoInitManagedBlockLanguage` 失败**：测试把临时目录的符号链接路径写进项目配置，而命令取到的是解析后的真实路径，匹配不到项目。测试改为先解析真实路径；`ProjectForPath` 不解析符号链接的问题另行跟踪（gofer-on30）。

## [0.199.0] - 2026-10-27

### 新增

- **`gofer repo init` / `repo migrate` 写进仓库的说明块按项目语言生成（gofer-992q，阶段 1 收尾 P1h）**：AGENTS.md / CLAUDE.md 里的 gofer 托管块（命令速查、提交策略、记忆写法、`repo status --changed` 提示）现在按该仓库所属项目的 `language` 生成（`projects.<key>.language` → `server.language` → 缺省），不再写死中文；语言只看配置，不看机器 locale。托管块的起止标记与语言无关，所以已有仓库重跑 `repo init` 会就地重写成新语言（新旧两种语言的块都认），块外内容不动，中英不会并存。中文默认输出与以前逐字一致。同一版还修了个同类的拾遗：`repo prime` 在没读得到配置时不再按机器 locale（`LANG`）选语言（英文系统 / CI 下会把 zh-CN 部署的 prime 切成英文），只认显式 `GOFER_LANG`，否则用部署缺省。
- **`gofer repo prime` 按项目语言生成会话开场上下文（gofer-992q，阶段 1 P1g）**：prime 的提交策略、命令提示、当前重点、规则与记忆索引、交接和接手提示，以及派发 job 的 tracker-prime / 项目与全局记忆段落现在支持 `en` / `zh-CN`。语言取项目覆盖的 `projects.<key>.language`，再回落 `server.language`；用户自己写的 issue、记忆和 plan 内容保持原文，中文默认输出保持兼容。

### 修复

- **`gofer repo prime` 与 `gofer issue brief` / `plan brief` 不再跟着机器的系统语言变成英文**：两者的内容是给 agent 读的，拿不到配置时与 hook 一致，只认显式的 `GOFER_LANG`，否则用部署默认语言；只有 `gofer init` 写入的初始配置按系统语言。

## [0.198.1] - 2026-10-27

### 修复

- **hook 与 MCP 注入给 agent 的文本不再跟着机器的系统语言变成英文**：hook 在 server 还没下发会话语言时、以及只转发到中央 server 的 MCP 客户端，原先按 `GOFER_LANG` → 系统语言 → 默认的顺序取语言，英文系统（如 `LANG=C.UTF-8`）上会把 zh-CN 部署的注入文本切成英文。现在只认显式的 `GOFER_LANG`，否则用部署默认语言；CLI 自己的输出仍按系统语言。

## [0.198.0] - 2026-10-27

### 改进

- **接手包 brief 按项目语言输出（i18n 阶段 1）**：`gofer issue brief` / `gofer plan brief`、MCP 的 `gofer_issue_brief` / `gofer_plan_brief`，以及 `gofer repo prime` 的进行中 plan 行与压缩 / 恢复后重注入那段，标题、字段、提示行都跟着语言走（CLI 取客户端配置里该项目的 `language`，MCP 取 `server.language`）。中文输出与改动前逐字一致；英文按英文写法重写。提交策略与记忆的时效 / 待复核标记仍由 tracker 生成，会随后续迁移切换。
- **计划详情的待办详情抽屉按「属于这条待办」收敛**：抽屉里删掉「计划交接说明」——那是整个计划的文案，计划详情页的「交接」页签本来就有，混在单条待办里让人分不清哪些属于这一条；「评论」改名为「待办评论」（它就是这条待办自己的评论线程）；「事件」改名为「关联任务的事件」并默认折叠，展开时才请求，没有关联 job 时整节不显示（以前打开抽屉就拉最多 5 个 job 的事件）。备注改用 markdown 渲染：裸 URL（如缺陷系统链接）与 markdown 链接可直接点开、在新标签页打开，`--append-note` 追加的多行备注也保住换行；列表视图里的备注同样处理。列表视图的待办行不再是整行可点的 label，只有勾选框本身才切换完成状态——以前点行内空白会误把待办勾成完成 / 取消完成。

### 新增

- **gofer 生成的文本可按语言输出（gofer-992q，i18n 阶段 1 第一步）**：新增配置 `server.language`（`en` / `zh-CN`，也认 `zh`、`zh_CN.UTF-8`、`english` 这类别名，一律归一化为规范值）与按项目的 `projects.<key>.language` 覆盖。本版迁移的是注入给 agent 的提示词段落：验收标准、交付约定（范围纪律 / 经验收集 / 记忆纠错提示 / 改动范围）、时限提示，以及 worktree job 的交付请求正文；英文提示词整体为英文，不再中英混杂。汇报小节「发现但不碰」「可复用经验」的解析同时接受中英两种小节名，旧的中文报告照旧识别。不写 `server.language` 时暂用 `zh-CN`（现有部署行为不变，启动日志与 `gofer config validate` 会提示显式设置；后续版本默认改为 `en`）；`gofer init` 按本机系统语言写入该值。
- **续接类提示语也按语言输出（gofer-992q，i18n 阶段 1 第三步 P1d）**：自动续投（瞬时错误后续跑）、上一轮有未提交改动时的续投、plan 单项接着上一项会话开头的衔接语，三种提示各有自己的模板，语言取该 job 提交时固定的语言（`projects.<key>.language` → `server.language`），与同一 job 的其余文本一致。自动续投的提示以前只有英文，现在中文部署收到对应的中文版、英文部署仍逐字不变；未提交续投与衔接语的中文与以前逐字一致。
- **MCP 工具说明按语言输出（gofer-992q，P1d）**：agent 在 `tools/list` 里读到的工具说明与返回给它的错误文本里不再有中英混杂——`server.language: en` 下全是英文（如「今天」→ `Today`、「可复用经验」→ `Reusable lessons`、`@<agent 或 role>` → `@<agent or role>`），中文部署看到的文本与以前逐字一致。语言来源：内建 MCP（`gofer mcp --standalone`）读本机 `server.language`；瘦客户端（转发到中央 serve）暂用本机语言链（`GOFER_LANG` → 系统语言），因为中央的语言目前没有传到该进程。换语言需新开 MCP 会话。
- **发给会话的通知与工作项请求也按语言输出（gofer-992q，i18n 阶段 1）**：`internal/sessionrelay` 发给会话的文本按收件会话所属项目的语言生成——job 完成通知（`耗时 1m30s` / `took 1m30s`、`（这是 gofer 系统通知，不是用户消息；…）` / `(this is a gofer system notice, …)`）、决策卡的标题与问题（`需要授权：` / `Permission needed: `、`等你回答：` / `Waiting for your answer: `、`在线送话` / `live delivery`、`接管` / `takeover`、`tmux 注入` / `tmux injection`）、唤醒与接管失败的解释（`无法唤醒这个会话（…）` / `Cannot wake this session (…)` 等）以及传话人提示词；`internal/work` 的工作项交接 / 汇报请求正文与工作项状态名同样按语言。传话人按提示词里的回执词回答（中文 `已发送`、英文 `sent`），识别两种都认。中文输出逐字不变（有 golden 钉住）。
- **管家提示词与信箱渲染按语言输出（gofer-992q，P1c）**：管家首轮提示词现在首行带语言无关标记 `<!-- gofer:steward-prime -->`，按管家项目语言选择中英文角色 / 巡检模板；Web 同时识别新标记与旧中文开头。信件唤醒、标签截断提示、收集箱摘要与退信提供 `en` / `zh-CN` 渲染 API，中文默认输出保持不变。
- **管家首轮简报与巡检提示的动态部分按语言输出（gofer-992q，P1e）**：管家会话开场那份简报（`## 现在`、管家笔记、未结工作项、在途请求账本、最近 24 小时日志这几节的标题与行，工作项行里的状态 / 阻塞 / 下一步 / 最后活动 / 在途请求标签，`N分钟前` 这类时间说法，星期名，草稿标记）与巡检提示里由代码拼出的部分（触发原因标题、工作项与事件清单、`「今天」` 待决策步骤、仓库记忆整理步骤、结尾约束）、管家事件摘要、会话标题（`管家 · <agent>`）与首轮指令、提问包装语全部进了新的 `internal/i18n/lang/{en,zh-CN}/steward.ini` 与三份模板，语言取管家项目（`projects.<管家项目>.language` → `server.language`）。英文环境下管家读到的提示词不再中英混杂；中文输出逐字不变（有 golden 钉住）。Web 的管家面板同时识别英文的巡检标题与英文提问包装语。

## [0.197.0] - 2026-10-27

### 改进

- **送进会话的消息前缀有了英文写法**：`internal/msgprefix` 的前缀表现在按语言给出两种写法——系统消息 `[gofer · job done]`、`[gofer · wakeup]`、`[gofer · work handoff]`、`[gofer · work report]`、`[gofer · steward relay]`、`[gofer · memory]`、`[gofer · hint]`、`[gofer · bounce]`，人的消息 `[from web, <用户>]` / `[web reply, <用户>]`（设计稿第 9 节）。生成按收件会话所属项目的语言（`Open` / `PrefixIn` / `FormatIn`），**识别两种语言都认**：老消息、改过语言的项目里的旧文本、旧 worker 发来的消息不会被当成普通输入。`[gofer · ` 这个开头本身与语言无关，「是不是机器发的」判断不变。
- **hook 注入给会话的文本按会话语言输出**：`internal/hookrelay` 注入的记忆块（标头、「因…命中」、「项目记忆 / 全局记忆」标注、摘要与超预算提示）、`还有 N 封待读信件…`、job 完成行（`[gofer · job done] … took 60s`、待验收提示）、`需要授权：` / `等你回答：`、拒绝回给 agent 的说明、建议标签（`rule …` / `directories …` / `switch to … mode` / 范围）全部进了 `internal/i18n/lang/{en,zh-CN}/hook.ini`，按收件会话所属项目的语言生成。语言由 server 随 session 响应下发（`language` 字段，`projects.<p>.language` → `server.language`），hook 自己没有配置也能对上；旧 server 不带这个字段时回退 `GOFER_LANG` 再到缺省。中文输出逐字不变（有 golden 钉住）。
- **计划详情：视图选择记到浏览器、空列收窄、待办详情抽屉（gofer-5inz）**：待办页的「看板 / 列表」切换现在除了写进 URL，还记到本浏览器（按计划一份、另有一份全局「最近一次」），刷新或下次打开沿用；URL 上显式的 `?view=board` / `?view=list` 仍然优先，默认看板不写进 URL 的链接习惯不变。看板里 0 张卡的列收窄到约普通列三分之一，只留列头与计数（仍是拖拽落点），空出来的宽度归有卡片的列，手机一次一列的布局不变。看板卡片与列表行标题（或行内「详情」）可点开右侧详情抽屉：标题、状态与时间、依赖、验收标准、派发字段、关联 job（可跳转）、备注、计划交接说明、评论与事件都在一处，抽屉里可直接改状态、派发 / 重派（没指派时先选 agent，与看板拖到 ready 同一条规则）。
- **任务详情概览的长键不再挤到值上（gofer-shue）**：概览的元信息网格里标签列从定长 78px 改成「至少 78px、按最长键自适应、超长可断词换行」，值列占余下宽度并省略号截断——`require_review`、`reviewed_by`、`wt_branch` 这类长键以前会和值连成「REQUIRE_REV需要人验收」。
- **对外门面：贡献指南、安全策略与 issue 模板**：新增英文 `CONTRIBUTING.md`（构建、测试、提交前检查、conventional commit 约定、issue / PR 的期待，并指向 `AGENTS.md`）、`SECURITY.md`（只维护最新 0.x 版本、GitHub 私密漏洞报告、自托管且面向单人 / 互信小团队的信任模型）、`.github/ISSUE_TEMPLATE/`（bug / feature 表单，提问与文档的链接指向 Discussions 与 `docs/`）与 `.github/pull_request_template.md`；README 中英文顶部各加一句 0.x 状态说明（作者每天在用、发版频繁、接口可能调整，以 CHANGELOG 为准）；需要网页上操作的仓库设置（简介、topics、社交预览图、Discussions）记在 `docs/runbook/github-repo-settings.md`；`make test` 跑 Go 全量测试。

### 新增

- **英文用户文档（T04）**：新增 `docs/guide/en/`——`getting-started.md`（安装→第一个 job→Web 控制台→加 worker→终端会话交给 web，每步带预期输出与常见卡点）、`concepts.md`（概念总览 + mermaid 图）、`features.md`（按功能分节，链接到对应 skill reference / runbook）、`security.md`（信任模型、部署建议、脱敏范围）。`README.md` 的 Features / Documentation / 指南链接改指英文版，`docs/guide/README.md` 增加英文入口。其余指南与 runbook 仍为中文。

### 修复

- **中文界面的验收按钮不再中英并列**：「Accept 通过 / Reject 拒绝…」改为「通过 / 拒绝…」。
- **收集项转 issue 不再丢原文（gofer-wcga）**：从收集箱把条目转成仓库 issue 时，若在转换界面把标题改短，描述里原来只剩「captured as work item …」，认真写下的收集内容哪里都不剩。现在描述总是带收集原文：issue 标题与条目标题不同时把条目标题全文写进描述（多行原样保留），条目 goal 与标题不同时一并写入，条目文本里的附图 / 附件引用（`/v1/attachments/<id>`）单列一行，另加一行来源（谁收集、何时、来自哪个会话 / job / 收集箱 / 信箱、项目；「谁」取收集时的作者，不是最后一次编辑者），末行仍是 `captured as work item <id>`。转会话 / 转 todo / 转 job 也已核对：它们本来就带整份文本，无同类丢失。

## [0.196.0] - 2026-10-26

### 修复

- **管家日常整理不再每次写工具都卡在人工审批（gofer-j0aq）**：管家会话现在带自己的审批策略：没配置 `steward.approval` 时沿用项目自己的审批模式（项目没开审批仍然不问），只在放行清单里加上管家本职的写工具；配置了 `steward.approval`（新增）则整条替换。管家本职的写工具——`gofer_work_update` / `gofer_work_note` / `gofer_work_remind` / `gofer_work_request_report` / `gofer_work_summarize` / `gofer_steward_notes` / `gofer_session_ask`。以前：项目开了 `approval` 时，管家每次记备注、设提醒都发一张审批卡，而管家的卡没人作答，超时被拒后整轮巡检以 agent 拒绝结束。只改管家这一条会话，项目里其他 ACP 会话照旧问人；写文件 / 执行命令 / 其他 MCP server 的工具仍然问人，`mode: strict` 一律问人，审计行记 `gofer_steward_tool`。

### 改进

- **对外发布前的私有信息清扫（G031）**：仓库里残留的业务项目名、个人主机 / worker 名、内网地址统一换成中性示例（`acme-app` / `w-office` / `office-pc` / `192.0.2.x`），设计稿与 runbook 只改名不改结论，tracker 里的历史条目与项目记忆同步改名。新增 `scripts/check-private-names.sh`（`sh scripts/check-private-names.sh`）：读**不入库**的 `.private-names`（格式见 `.private-names.example`，一行一个 ERE），扫 `git ls-files`，命中私有名就非零退出；发版前跑一次（见 `AGENTS.md`）。

## [0.195.1] - 2026-10-24

### 修复

- **测试稳定性**：worktree 竞态重试的测试在 Windows 短路径（8.3）下误判、传话人快照测试在 stderr 还没读到时就断言，导致 v0.195.0 的 CI 失败；功能代码不变，v0.195.0 的全部改动随本版本一起发布。

## [0.195.0] - 2026-10-24

### 修复

- **被服务端截短的任务时限不再静默（gofer-ea2i）**：`gofer job show` 的 `timeout:` 行下面写明请求值与封顶的配置键（`(timeout 14400s clamped to 5400s by server.max_job_timeout_sec)`），web 任务详情概览的 duration 行同样在生效时限旁写「请求 4h00m，被 server.max_job_timeout_sec 封顶」（zh / en）。以前 web 只在悬停提示里说、`job show` 只在提交时打警告，事后再查详情只看到生效值，看不出它被截短过。
- **扇出步提交中途失败不再留下孤儿 fan（gofer-ourk）**：一个 fan 提交失败时整个 workflow 以失败结束，但此前已经提交的 fan 还在跑，只能手工取消。现在失败的步骤把已经起的那几路 fan 一并取消；取消放在 workflow 写入失败状态之后，被取消的 fan 触发的那次推进不会再把同一步骤重新起起来。
- **`git worktree add` 撞上并发登记时短暂重试（gofer-ourk）**：同一仓库被主机与容器同时创建 worktree 时，git 可能读到别人刚建了一半的 `.git/worktrees/<id>/commondir`（或 `gitdir`）而直接报 `fatal: failed to read …: No error`。这类错误现在最多试 4 次（退避 200/400/600ms）；真正的失败（目录或分支已存在、base 不是提交、没有 git）仍然一次就返回原错误。
- **信箱筛选 chip 的对话数由服务端算准（gofer-av8f）**：`GET /v1/mail/conversations` 新增 `project` 参数（按参与方所属项目收窄，与页面的本地过滤同一口径）与 `total`（分页前的对话总数）；Web 的「全部 / 收件 / 发件」计数改为只要一条（`limit=1`）读 `total`，不再拉满 200 个对话在前端数。页头的「N 个对话」与 chip 同一口径，对话超过 200 个时也准确。`count` 仍是这一页的条数（不变，additive 字段）。
- **工作台「改动」页签点开才请求 diff（gofer-yb5x）**：以前打开工作台就把每个会话的 diff 都拉一遍，首轮作业还没有 git 基线的会话每次都吃一个 409（浏览器控制台报错、页面顶部一行红字）。现在只在点开「改动」页签时才请求（页签开着换会话，拉的是新会话），没有基线时页签里写一句原因和「刷新」，不再当成加载错误。页签的选择按 pane 记着，同一个 pane 里换会话不会退回「过程」。
- **计划进度统一口径（gofer-j4j1）**：完成数 / 总数在所有地方同一套算法——跳过算完成，受阻 / 失败 / 待验收都不算——百分比永远由这两个数折算。以前详情头部用的是服务端的百分比、分子分母却是前端自己数出来的各段之和（受阻 / 待验收两段还会把总数撑大），出现过「9/10 100%」「9/11」这类对不上的显示。现在详情头部、清单进度、计划总览、看板都走 `web/src/utils/planProgress.ts` 一处。

### 改进

- **各处的项目选择都能搜了（gofer-b8nh）**：列表头的项目 chip，以及新建任务 / 新建会话（工作台输入框与会话抽屉）/ 新建工作流 / 工作流模板 / 定时 / 记忆作用域 / 项目页的手机选择器，统一换成同一个可搜索下拉（新 `ui/SearchSelect`）：点开即聚焦搜索框，按项目 key 过滤（大小写不敏感、包含匹配、前缀优先），↑ ↓ 移动、Enter 选中、Esc 关闭，无匹配显示空态；首项「全部项目」/「未选项目」与当前选中高亮保留，手机上贴底展开。`ui/ProjectFilter` 内部改用同一组件（对页面 API 不变）。

## [0.194.1] - 2026-10-24

### 修复

- **重启 server / 一键更新的等待能立刻取消**：等待阶段一公开（`waiting_idle`）就可以取消；以前取消句柄要等后台等待真正开始才就绪，接受请求后马上点取消会得到 409「没有可取消的重启」。

## [0.194.0] - 2026-10-24

### 修复

- **升级下载地址只接受规范的版本号与平台**：单点升级指定的版本只接受 `X.Y.Z[-后缀]`，平台只接受小写字母数字，不能借版本号改变下载地址（CodeQL request-forgery）。
- **配置视图里的地址不再泄露凭据**：配置管理页回显的 MCP server 地址与更新源地址，把 `user:pass@` 与查询参数的值打码，协议、主机、路径与 `{os}` / `{arch}` 占位符照常显示。
- **聊天里的长行不再把气泡撑出可视区（gofer-ndub）**：工作台、会话抽屉、信箱、管家面板、审批卡片、评论、任务的「消息」区与汇报里，代码块、长命令、长路径、长 URL 与宽表格以前会把气泡（甚至整页）撑宽。现在这类内容走同一份 `.msg-body` 样式（`web/src/styles/msg.css`）：气泡本身不再被内容顶开（`min-width:0`），普通文本里的超长单词 / URL 就地断行（`overflow-wrap:anywhere`），代码块与表格在自己的框里横向滚动。markdown 渲染统一经 `MarkdownBlock`（根元素自带该类），纯文本气泡在模板里加同名 class；扫掠测试守住「每个注入点在 `.msg-body` 里」。

### 新增

- **配置管理页展示完整配置 + 可重启 server（gofer-4uk5）**：
  - `GET /v1/config` 现在会带回全部顶层段：`log`、`wakeup`、`session`、`work`、`steward`、`pty`、`mail`、`tools`、`mcp_servers`、`secrets`、`pricing`、`http_proxy`、`include`，以及 `server.*` 里原先没有的块（`tls`、`update`、`worker_enrollment`、`push`、`session_messaging`、`policy_repush`、`agent_fallback`、`xfer`、`attachments`、`worker_queue_max_wait_sec`、`hold`、`notification.desktop`），还有 `governance.require_tunnel_capability` / `attach_origins`、`callers[].can_attach` / `can_tunnel`、`storage.mail_retention_days` / `attachment_retention_days`。全都是**只读**字段；密钥与代理 URL 只回「已配置 / 未配置」，env / header 只给名字（沿用 webhook `secret_env` 的先例）。
  - 设置 › 配置管理把其余各段按分组卡片（默认收起，标「只读」）展示，敏感字段显示「已配置 / 未配置」；原有的 server / agents 编辑入口不变。
  - 「重新读取文件」旁新增「重启 server」：写清影响的二次确认（在跑的 job 先结束、新 job 排队、控制台断几秒）、可选「最多等空闲」（30 分钟 / 1 / 2 / 4 小时）与「取消等待」；不是受管服务（未 `gofer serve register`）时按钮灰显并说明原因。
  - 新接口 `POST /v1/server/restart`（`GET` 读进度与 `managed`、`DELETE` 取消等待），复用一键更新的受管交接（`gofer serve upgrade`）：套接字照样先停放新 job、等机器空闲，再把**当前可执行文件的副本**交给交接——不下载任何东西。权限与一键更新同档（admin + `config.write`）；非受管 server 返回 409。
- **异构 worker 可以单点升级：从下载源取该平台的发布包（gofer-n1vj）**：
  - `POST /v1/workers/{id}/upgrade` 新增来源 `mirror`（始终从 `server.update` 的下载源取）与 `auto`（新的默认值：与 server 同 os/arch 用 server 自身可执行文件，其他平台从下载源取该平台的发布包）。显式 `source: server` 在平台不同时仍报 `platform mismatch`，`staged` 未变。
  - 新增 `version` 字段（`X.Y.Z`，可带 `v`）：默认跟 server 同版本，可指定其他发布版本；mirror 用 `url_template` 的 `{version}`，GitHub 走该 tag 的 release。下载与校验复用的是一键更新的逻辑（有 sha256 就校验；mirror 只发布最新版的校验和）。
  - CLI：`gofer worker upgrade <id>` 默认 `--source auto`，新增 `--source auto|server|mirror` 与 `--version`；`--file` 仍优先。下载源未配 / 检查失败 / 平台没有对应包时命令直接报原因。
  - Web 运行节点页：平台不同的 worker 不再灰掉升级按钮，改为从下载源升级（卡片上注明），确认框说明二进制来自下载源。

### 变更

- **Web 计划详情：看板状态带字形与状态色，视图切换挪到待办标题旁，页签计数按含义着色（gofer-qw2g）**：
  - 看板列头不再显示 `pending` / `ready` 这类原始状态键，改为状态名（未开始 / 就绪 / 进行中 / 待验收 / 完成）加状态族字形；卡片也带待办状态与 job 状态的 pill（失败、待验收靠形状也能分辨），颜色仍是全站同一套状态族，不新增颜色表。手机的切列标签同步。
  - 「看板 / 列表」从页头右上角挪到「待办（完成 x/y · 进行中 n）」这一行右侧；选择写进 URL 的 `?view=list`（默认看板不写），可分享、可前进后退，不再只记在本浏览器。
  - 「待办 / 任务 / 决策」页签的计数改成带状态色边框的徽标：受阻或等待人用黄色、有失败用红色、进行中用运行色、全部完成用绿色，其余中性。

## [0.193.0] - 2026-10-24

### 新增

- **Web 控制台登录可以「在此浏览器记住登录」（gofer-x5k4）**：`/access` 页新增勾选框（默认勾选）。勾选时 token 存 localStorage（按站点持久），从桌面通知 / 钉钉链接打开的新标签页、重开浏览器都不用再登录；不勾选只留在当前标签页（sessionStorage，关掉即失效）。读取顺序 sessionStorage → localStorage；退出登录与 401 都把两处清干净。设置 › 关于新增「这个浏览器」一节，显示当前是否记住登录，并提供「退出并忘记此浏览器」。另外 `GET /health` 多返回一个 `web_base_url`（`server.web_base_url` 未配置时没有该字段）：接入页拿它与当前地址比对，两者 origin 不同时给一行提示——两个地址各记一份登录，从通知链接打开会要求重新登录。

### 修复

- **按工具名放行不越过改动类调用**：按名放行只用于非改动类调用；agent 报为 edit / delete / move / execute / switch_mode 的调用，即使标题是 gofer 自己的工具，也一律问人。
- **审批可以按工具名放行：gofer 自己的 MCP 工具不再每次找人批（gofer-78on）**：`approval: ask` 以前只按 ACP `kind` 放行，而 agent 调 MCP 工具一律报 `kind=other`，于是管家（steward）每做一次巡检都要为 `gofer_today_advise` 这类只写建议的工具等人批。现在审批支持**工具名名单**：
  - 新增 `approval.auto_allow_tools` / `ask_tools`（项目 `approval` 与 `mail.agents.<名字>.approval` 同结构，`ask_tools` 优先，可写裸名或 MCP 限定名、大小写不敏感、也可写别的 MCP server 的工具名）。不写 = 默认名单：gofer 的**只读** MCP 工具（`gofer_work_list`、`gofer_today_list` …）加**只写建议、不改状态**的三个（`gofer_today_advise`、`gofer_work_merge_suggest`、`gofer_memory_suggest`，名单唯一来源 `internal/mcptools`）。写了就替换默认名单，`auto_allow_tools: []` = 只按 kind 判。
  - 放行原因记在 `artifacts/acp.jsonl`：只读工具 `reason=gofer_readonly_tool`、建议类 `gofer_advice_tool`、名单里的其他工具 `auto_allow_tool`。
  - **按名字伪造绕不过去**：工具名只认 gofer 自己的 MCP server——`mcp__<server>__<tool>` 的 server 必须是本会话实际拿到的 gofer server（gofer 注入的名字固定 `gofer`，同一 gofer 二进制绑在别的名字下也算），裸名只在本会话确实拿到 gofer MCP 时才算。别的 MCP server 的同名工具、无 gofer MCP 时的冒名裸名照旧按策略审批；写文件、执行命令、提交 job、改状态仍旧问人，`mode: strict` 仍然每个都问。
  - 审批策略随 worker dispatch 下发的线路字段（`wsproto.ApprovalPolicy`）同步带上这两个名单，worker 上的 job 与服务端同样判定。

## [0.192.2] - 2026-10-24

### 修复

- **从通知 / 复制的链接新开标签页不再跳登录页**：外壳一挂载就请求接口，新标签页还在向已登录的标签页要 token 时，这些请求没带 token 拿到 401，401 处理又把刚交接来的 token 清掉并跳到 /access，跨标签页交接因此从未生效。现在先完成交接再挂载应用；401 跳登录时也带上原页面，登录后回去。
- **记忆页标题按语言显示**：中文界面不再显示「记忆 / Memory」。

## [0.192.1] - 2026-10-23

### 新增

- **Web 记忆页支持跨项目搜索、分页与详情抽屉（gofer-icqt，UI 重设计 P6）**：
  - **「全部」范围**（`/memory?memory_scope=all`）：一次搜全部项目 / 仓库 / 全局的记忆，每行标出来源（项目 · `<key>` / 仓库 · `<项目 / 目录>` / 全局）；取数仍是既有接口（各自按调用方权限过滤），前端并发取回后按「来源 + key」合并去重、最近的在前。单个来源取数失败不影响其余，错误提示里给出失败个数与第一条原因。
  - **分页**：与 Issues 页同一套控件（每页 50 / 100 / 200，选择记在本机），页码写进 `?page=`（第 1 页不写）；切换范围 / 仓库 / 项目 / 搜索 / 类型 chip / 每页条数都回到第 1 页。
  - **详情抽屉**：点列表项在右侧打开抽屉（手机全屏），展示 key、摘要、类型、来源、更新时间、过期时间、作者与失效报告；可就地改内容、摘要与标签（标签 chip 增删），保存 / 删除（二次确认）/ 标记失效（理由必填，只对 server 存的全局 / 项目记忆开放）。旧的页内展开详情删掉，信息无缺失。

### 修复

- **展开的 markdown 不再出现大片空行**：会话「最后一条消息」与「进度」展开后、任务详情的 markdown 形态渲染命令，渲染后的 HTML 放在了保留换行的容器里，标签之间的换行都变成了空行；改为正常换行（最后一条消息改用正文字体，代码块仍为等宽），并加扫描测试防止再出现。

## [0.192.0] - 2026-10-23

### 修复

- **发给收不到信的 job 的信不再静默挂起（gofer-0n6r）**：`job:<id>` 只对 **ACP 会话 job**（`session: true`）有意义——一次性作业永远没有 `awaiting_input`，已结束的 job 也不会再读信，但过去这两类地址收下的信一律留在「待送达」，直到 24 小时送达期限才转收集箱，发件方全程无感。现在：
  - **发信即拒绝**：发给一次性作业或已结束 job 的信直接报 `409`（`job cannot receive mail`），`job:<id>` 不存在报 `404`，并说明替代做法（发给它背后的会话、等结束看汇报、`gofer job comment`）。
  - **收件 job 结束即退信**：已接受的信在收件 job 进终态、收件方是 pty / 一次性作业、或超过送达期限转收集箱时，立刻给发件方回一封 `[gofer · 退信]` 系统信（哪封信、为什么没送到、现在在收集箱哪个条目、原文摘录），急件的退信也是急件；发件方是会话时按普通信件规则注入 / 唤醒。退信不会再被退信，广播副本过期不退信。`session mail reply` 回退信报 409。

### 新增

- **信箱对话有可复制的固定 id（gofer-hez1，UI 截图检查 P6 第三组）**：每组对话（同一对人之间、不分方向的全部信件）多一个稳定短 id `cv-` + 10 位十六进制（由两端地址算出，谁先发、几条 thread 都不变）。`GET /v1/mail/conversations` 的每一项带 `id`，`GET /v1/mail` 被限定到一个对话时回答里也带 `conversation: {id, key}`；两个接口都接受 `conv=<id>` 按 id 取对话（只在本调用方可见范围内反查，别人的对话一律 404）。CLI 新增 `gofer session mail conv <cv-id>` 看一个对话（双方与全部信件，最早在前）；web 信箱右栏头部显示「对话 cv-xxxx」并可一键复制，每封信也有 thread id 的复制入口，`/mail?conv=<id>` 直接打开这个对话。
- **Web 控制台把「记忆」独立成一页（gofer-hez1，UI 重设计 P6）**：侧栏「协作」组 Issues 下面新增「记忆」（`/memory`，书签图标），手机的「更多」抽屉与命令面板（搜「记忆」）也能进。页面沿用记忆页签的全部能力，并把位置写进 URL（可分享、后退可复原）：作用域 `?memory_scope=repo|project|global`、项目 `?memory_project=`、仓库 `?tracker=`、关键字 `?q=`、深链 `?memory=<key>`；仓库作用域保留「同步」按钮与上次同步摘要，全局作用域保留各机器的同步状态折叠区。Issues 页的「记忆」页签保留为入口（点它跳到 `/memory`），旧链接 `/issues?tab=memories…` 带全部参数自动重定向到 `/memory`——两处共用同一份列表组件，不会出现两套记忆界面。

### 变更

- **左上角显示 gofer logo**：侧栏与手机顶栏左上角的字母「g」换成 gofer 的 logo（与浏览器标签页图标同一张）。
- **信件收件人是 job 时可点击**：信件头部的收件地址是 `job:` 时显示为链接，直接打开该 job 的详情页；地址显示宽度放宽，完整地址仍在悬停提示里。
- **工作台输入框只留一个附件入口（gofer-hez1，P6 第三组）**：去掉输入框上方那条旧附件栏（第二个 📎 + 一行提示），只保留 Composer 自己的附件按钮；它承载的提示（该会话不支持附图等原因、上传中、失败）改由按钮 `title` + 输入框下方一行小字，信息不丢。
- **工作台置顶按钮看得懂了（gofer-hez1，P6 第三组）**：右上角原先是一个纯文本 ⌖ 小字形，现在用统一尺寸的图标，桌面宽度带「置顶 / 取消置顶」文字（手机只留图标，`aria-label` / `title` 不变），已置顶时用强调色边框 + 淡底，不用悬停也能看出状态。
- **设置页内容区跟随宽度（gofer-hez1，P6 第三组）**：二级导航右侧的内容区原先固定在 720px，1440 / 1920 宽度下右边大片空白；现在跟随可用宽度（页面宽度见下一条的全局统一口径），卡片 / 表格 / 说明用满内容区，手机布局不变。
- **各页面内容区宽度统一（gofer-hez1，P6 宽度统一）**：右侧内容区原先每个页面各写一个上限与居中（Issues / 待验收 1240、任务详情 1200、Agents 1160、统计 1120、任务 / 计划 / 项目 / 工作流 1100、新建任务 1080、计划详情 980、新建工作流 / 运行节点 900、新建定时 760、会话 1280、设置 1480），视觉上左右边界不一致，设置与运行节点页尤其显窄。现在全站只有一种页面容器：上限 1440px（按 Issues 较满的页面定）、居中、统一页边距（手机收窄），宽度不足时铺满；工作台与信箱按设计铺满整个内容区。登录页仍是窄卡片，表单输入控件自己的限宽不变。
- **仓库选择器能分清多个仓库，完整路径看得见（gofer-hez1，UI 重设计 P6）**：`/issues` 与 `/memory` 的仓库下拉以前只列 tracker id（`tracker-187c7d1141`），同一项目下多个仓库无法区分；现在每项主文字是「项目 key / 仓库目录名」，次要小字是「tracker id · 路径末两级」，选项按项目排序（没有项目 key 的排最后）。选中后的信息条不再用省略号截断一半路径：项目 · 前缀一行、完整路径一行（自动换行），旁边有「复制路径」按钮；手机上信息条与按钮同样换行可读。只在前端组合现有字段（目录名取 `rel_path` 最后一段），接口未改。
- **工作项页改版收尾（gofer-hez1）**：卡片与详情抽屉的操作按钮换成统一按钮组件（打开会话为主操作，其余次要 / 文字，尺寸与 token 统一，手机点按 40px），标状态从原生下拉改成下拉菜单；去掉「待整理（草稿 / 收集箱）」分段与 8 列状态栏，改成一条平铺列表（等我 / 受阻 → 进行中 → 等资源 → 待整理草稿 → 收集箱 → 已完成，状态内按最近活动倒序），卡片宽度一致、不再有大片空白；卡片左边框按状态族着色，旁边仍有状态文字；列表头加状态筛选 chip（全部 + 各状态，带计数，值进 URL 的 `?status=`，可与到期提醒、项目叠加），原来的「等我 / 未整理」不再单独设 chip（它们就是泳道），`/work?tab=capture` 旧深链仍落到收集箱泳道；手机单列、筛选 chip 在框内横向滑动。

## [0.191.2] - 2026-10-23

### 修复

- **临时检出清扫不再删除别人的 detached 检出**：项目的 `worktree.dir` 用 `{name}` 模板（如 `.worktrees/{name}`）时，模板能匹配任何目录名，v0.190.0 起的启动 / 周期清扫会把该目录下**任何** detached 检出当成「已结束 job 的残留」删掉——实际删掉过一次发版构建正在用的 `.worktrees/rel-v0.191.1`。现在只有能证明属于 gofer 已结束 job 的检出才会被清扫：job 记录存在且已终态，或记录已被清理但目录名是 gofer 生成的 job id（`job-<日期>-<时间>-<hex>`）；其他名字一律保留。带分支的检出照旧不碰。

## [0.191.1] - 2026-10-23

### 修复

- **UI 重设计截图检查的第一批问题（P6 第二组，gofer-hez1）**：信箱「还有更早的信件未加载」不再浮在小地图旁压住信件正文，而是与「加载更早的 N 条」按钮同一行放在时间线顶端；信箱筛选 chip 的计数改成「这个筛选下有几个对话」（原先取的是接口返回的那一页的条数——为了省流量带 `limit=1`，所以四个 chip 全显示 1，与页头的「N 个对话」对不上），项目筛选下两处口径一致；任务详情概览的「最近日志」去掉 ANSI 转义码（原先把 `[2m`、`[36m` 这类控制字符当可见文字显示）；工作项卡片的标题不再被右侧的状态 chip 挤成一窄列（标题占整行，状态与标记在下方一行），页面底部留出浮动的「问管家」按钮的高度、不再压住最后一张卡的「详情」；今天页建议卡片的动作按钮按动作类型本地化（英文界面不再出现「Suggested：忽略」「采纳 / 忽略」这类混排，未知类型才显示服务端原文）。另修：`--muted`、`--warn`、`--hover`、`--dim`、`--accent-fg`、`--fs-base` 六个从未定义、全靠写死兜底色的 CSS 变量改回设计 token（`--ink-3` / `--need` / `--surface-2` / `--accent-ink` / `--fs-md`，手机输入框字号新增 `--fs-input-mobile`），并加测试守住（`var(--x)` 必须在 tokens 或同组件内定义）。
- **计划详情「清单进度」卡片排版（gofer-hez1）**：自画的进度条被挤成 ~56px、「4/6」掉到条下面、与「合计 tokens」错位；改用全站统一的 `ProgressBar`（size sm、条与右侧分数同一行、不显示图例），条可伸缩，用量文本跟在后面。
- **运行节点拓扑出现两个 server（gofer-hez1）**：中心 hub 与本机 in-process runner 各画了一个节点，读起来像两个 server。本机 runner 不再单独成节点，它的说明与 server 项目列表并进中心节点的面板（hub 面板新增「本机 runner」一节）。
- **统计页「任务状态分布」小格的状态名（gofer-hez1）**：原来写英文缩写并被裁成 runni… / recov… / appro…；改成与全站一致的状态名（`status.<值>`），格子最小宽放到 84px 且不再 ellipsis（窄了就换行），原始状态值留在悬停提示里。
- **今天页右栏「并行中」的行排版（gofer-hez1）**：右栏只有 ~340px，原来 6 列挤在一行，标题只剩 4–5 个字、红色「停滞」压在进度点阵上。改成两行：第一行状态点 + 标题与项目（独占整行，超出省略号，悬停给全文），第二行 agent / 点阵 / 当前步骤 / 健康度 / 用时；点阵与健康度固定宽度，不再重叠。桌面与手机宽度相同结构。
- **侧栏「节点 N/M 在线」把已连接的 worker 算成离线（gofer-hez1）**：worker 的在线值是 `connected`（local / peer-http 才是 `up`），侧栏摘要只判 `up`，于是 4 个已连接 worker + server 本机显示成 1/5。在线口径统一到 `utils/runners.runnerOnline`，侧栏摘要与一键更新弹层都用它，与运行节点页一致。
- **Web 漏译的文案 key（gofer-hez1）**：运行节点页的工作目录折叠区显示原始 key `runners.dirs.title`，会话抽屉的「空闲」状态与「已接管 →」后的 job 链接同样漏 key；三处 zh / en 一并补齐。新增 `web/src/i18n/i18nKeyUsage.test.ts`：扫描 `web/src` 里 `t('字面量')` 的 key，必须同时存在于 zh 与 en 字典（动态拼接的 key 不在范围内），以后再漏 key 直接测试失败。

## [0.191.0] - 2026-10-23

### 变更

- **界面样式收尾：旧色板变量彻底下线、手机断点统一、状态取色收敛（P6-a，gofer-hez1）**：迁移期为了让旧页面自动拿到新色板而保留的旧变量（`--ink` 当底色、`--panel`、`--paper`、`--phosphor`、`--queue`、`--term-bg`、`--font-sans`、`--radius`）全部删除，所有页面改用新 token；正文色按设计稿重新叫回 `--ink`。代码块 / diff / 输入框这类凹陷区的底色统一到 `--sunken`（与迁移前的观感一致）；卡片与控件的圆角从统一的 4px 改成 5px（`--r-chip`）。手机断点从散落的 620 / 639 / 640 / 700 / 720 / 767 / 768 / 980 统一成 **≤760**（与各页已迁移的手机布局同一个值），「系统」折叠区的卡片在手机上保持两列、头部竖排。状态颜色只剩一份来源（状态族）：统计页「任务状态分布」、任务状态徽标与信号行改为按族取色——超时是紫色（不再是失败红），待应答 / 待验收 / 待批准是「需要人」的黄色（不再是主色绿），等目录锁与等更新是中性灰。
- **设置区与接入页改版（P5-d，gofer-hez1）**：`/settings` 的二级导航按新稿重做——桌面是竖排导航（当前项高亮），手机（≤760）收成下拉框；各子页统一用 `FormSection`（左说明 / 右字段）分节，有未保存修改时出现吸附在底部的保存条（放弃 / 保存）。本批已迁到新样式的子页：通知（本设备推送 + 订阅列表）、MCP（每个 server 一节，测试探针结果原样保留）、关于（服务端信息、更新状态、一键更新与等空闲 / 取消等待）、工作项（被动整理、请求与交接、每日摘要、等我通知、管家；保存走吸底保存条）、规则库（编辑器改用分节，表头 / 空态 / 校验与删除确认走字典）、权限（页头、四个页签、身份与调用方上限条、新增授权表单与预览提示、授权标签与动作、会话授权、权限目录的搜索与风险档走字典）、扩展（逐键卡片的标签、默认值对比、恢复默认二次确认、逐键保存提示与导入预览弹层走字典）、技能（表格列、行内动作、导入面板、详情块与各类提示 / 确认走字典）、隧道（在线转发 / 预设 / 本机未上传三张表、停止流程、预设编辑弹窗走字典）；这几页的界面文案全部走 zh/en 字典（`web/src/i18n/pages/settings.ts`）。`/access` 换成居中登录卡（品牌、token 输入、主题与语言开关），成功后回原页面的 redirect 与跨标签页 token 交接行为不变（`web/src/i18n/pages/access.ts`）。配置管理页的字段与操作不变，样式与文案迁移留待后续提交。
- **项目页新增「按项目 / 按执行机」两个视图（gofer-d1kn）**：`/projects` 顶部加视图切换（`?view=runner` 可深链）。按执行机视图每台执行机一组：server 本机（显示为 `server`）与每个 worker 各一组，组内列出 server 声明且 `allowed_runners` 允许它运行的项目、它自己声明的项目（标「worker 自有」，来自 `GET /v1/workers/{id}/projects`）与它的默认工作空间（server 取本机工作空间，worker 取 `GET /v1/workers/{id}` 的 `dirs.workspace`），点项目 chip 回到按项目视图并选中；离线 worker 整卡灰显、标「离线」，心跳上报的项目与目录取不到时写明原因，取数失败也显示原始错误。按项目视图里 worker 自有项目另列一组（带声明它的 worker），内置 / `default` 项目标明「server 本机」。只用现有接口，未改后端。
- **界面文案全部进 i18n（P6-b，gofer-hez1）**：工具层、公共组件与各页面里仍硬编码的中文全部抽进字典（页面字典 `i18n/pages/<page>.ts`，跨页共用的进新增的 `i18n/pages/shared.ts`，域 `util.`），英文界面不再夹中文；选项数组、状态表与模块级常量改成 getter 或函数，渲染时才取词，切换语言即时生效；「复制统计」等纯文本也按当前语言输出。新增扫描测试 `noHardcodedCjk`：`web/src` 下的 .ts / .vue（排除字典、测试与注释）出现中文即失败，确需保留的字面量（语言名、协议 / wire 文本的解析关键字、服务端 prompt 原文、内置示例 workflow 正文）集中在白名单里并写明理由。只换文案来源，样式、布局与行为不变。
- **Web 图标改用 Lucide 图标库（P6-c，gofer-h355）**：界面里当图标用的 Unicode 字形（导航的 ◉ ▤ ⧉ ⌁ ⬡ ◈ ⌂ ⚙，页面里的 ✉ ✓ ⚑ ⇉ ◷ ↻ ✕ ▸ 等）全部换成 [lucide](https://lucide.dev) 图标（`@lucide/vue`，ISC，按需打包、离线可用）。侧栏 / 图标栏 / 手机标签栏 / 「更多」宫格、顶栏按钮（搜索、新建、随手记、信箱、待我决策、主题、语言、新版本）、窗口与弹窗的关闭按钮、各处的展开 / 收起箭头、任务与计划事件时间线的类型标记都换成线稿图标；尺寸统一为 14 / 16 / 20 三档（`--icon-sm` / `--icon` / `--icon-lg`）、线宽统一 1.75，颜色跟随文字。状态字形（`StatusGlyph` / `StatusPill`）的族与颜色不变，形状改为 lucide 图标（运行中旋转圆环、需要人感叹号、完成对勾、失败叉、超时时钟、空闲空心圆、排队虚线圆），小尺寸下更清楚。纯图标按钮都带 `aria-label` 与 `title`（走 zh/en 字典）。新增扫描测试：`web/src` 下不再允许出现当图标用的那批字形（白名单需写理由）。

## [0.190.0] - 2026-10-23

### 变更

- **运行节点、Agents、项目三页改版（P5-c，gofer-hez1）**：`/runners` 的页头换成标题 + 一行摘要，待接受申请置顶，worker 申请同时进入「待我决策」；worker / peer / server 卡片保留状态、协议与 gofer 版本、平台、运行时长、in_flight、项目目录、agents 与外部工具、传话人入口、重新加载配置、一键升级与升级记录，离线 / 心跳过期 / 待决定 / 已拒绝都改用状态字形 + 文字 pill（不再各自画圆点），拓扑可折叠（手机上默认收起）。`/agents` 的只读健康表保留检测、键、类型、健康度、版本 / 错误与内置标记，健康度改用状态字形 + 文字 pill、附带宽口径的悬停说明，并新增「编辑」链接直达 设置 › 配置管理 的 agent 段。`/projects` 的主从布局保留（手机上是顶栏选择器 + 单栏），详情新增项目概览（活跃任务 / 进行中计划的计数，各自跳到该项目的过滤列表），内置项目标记、allowed_agents / allowed_runners、交互与 exec 开关、规则绑定、worktree 路径、git 状态、子仓库与关键文件全部保留。三页文案进 `web/src/i18n/pages/runners.ts`、`agents.ts`、`projects.ts`。
- **Issues、统计、新建任务三页改版（P5-b，gofer-hez1）**：`/issues` 的仓库下拉换成列表头统一的项目 chip（repo 视作项目），Issues / 记忆两个页签换成统一页签（`?tab=` 可深链），状态筛选换成带计数的 chip，树形 / 平铺切换与记忆作用域都改用分段控件，issue 详情改成统一侧抽屉（关系、备注、评论、保存/评论的只读判断都保留），页头去掉 TRACKER 眉标。`/dashboard` 迁到新 token（状态族配色、数字等宽、面板圆角分级），所有标题、口径提示、图例与单位走 zh/en 字典，手机断点统一到 760px。`/new` 改成两栏（左栏项目 / runner / worker / agent，右栏模板 / 标题 / cwd / 任务说明），其余选项收进可折叠的「高级选项」，返回链接换成统一的「← 任务」，标题随模式变化（新建任务 / 快速重建 / 从会话新开），全部字段与校验不变。三页文案进 `web/src/i18n/pages/issues.ts`、`stats.ts`、`newjob.ts`。
- **工作流与定时页改版（P5-a，gofer-hez1）**：`/workflows` 的状态下拉换成带计数的状态 chip（全部 / 各状态，再点一次取消），列表列固定为状态、工作流标题 / id、步骤进度（分段进度条 + `当前/总数`）、开始时间、耗时；`/workflows/new` 的模板 / YAML 两个页签换成统一页签组件（`?tab=` 可深链），YAML 编辑器加行号栏；`/workflows/:id` 的返回链接改成「← 工作流」并回到原筛选，结果表一列一路 fan（名字、runner、状态字形、退出码、开始时间、耗时、JOB 跳转、工作目录与 ref、可展开的失败摘录），「只重跑失败的」「从此步重跑」「取消工作流」保留，另存为模板改到侧抽屉。`/schedules` 加项目 chip（值是 URL 的 `?project=`，服务端按项目取数）、启用列改成开关、列固定为名称 / id（cron·once 徽标 + 项目 · agent · runner）、cron、下次触发、上次任务、操作，行内仍是运行（带参数的显示「⚙」）/ 编辑 / 启停 / 删除与展开的请求摘要；「立即运行」的参数表单改到底部抽屉（按声明生成控件、预填默认、底部预览将设置的环境变量），编辑改成侧抽屉（cron 或触发时间、标题、超时、启停、参数声明增删改），新建调度的字段与参数声明编辑器不变、文案走字典。本页全部界面文案走 zh/en 字典（`web/src/i18n/pages/workflows.ts`、`schedules.ts`）。

### 修复

- **不属于任何项目的会话不再传不进去话（gofer-vin1）**：会话的 cwd 不在任何已配置项目下时（`session.project` 为空），给它的传话 / 送话一律失败 `deliver_failed: invalid request: invalid project key ""`，web 侧则是「该会话所在目录不属于任何已配置项目，无法派发传话人」。现在这类由 server 发起的投递 job（agent 的 `deliver_command`、tmux 注入、一次性传话人）落在**执行机的默认工作空间项目**（`default`，即该机 `~/.gofer/workspace`，可用 `GOFER_WORKSPACE` 改）里跑：cwd 是该工作空间根，不会跑进任何别的项目目录；为此 gofer 自己的投递 job 在 `default` 项目里放行 runner 准入与 exec 门槛（操作员从未为这个内置项目写过策略，命令也是 gofer 自己的），普通 job 仍照旧受限。远程 worker 也一样——worker 现在也会注入并上报自己的 `default` 项目（`gofer init` 写的那套定义），所以会话在哪台机就在那台机的默认工作空间跑；worker 太旧或没有该项目时会明确报 `project "default" not on worker`，而不是奇怪的 project key 报错。**接管（`--resume`）仍要求会话在项目内**（要在会话自己的目录里重开），无项目时返回 `no_project` 并说明原因。
- **临时检出的启动清扫也覆盖嵌套仓库（gofer-l0zi）**：`ref:` 临时检出建在 job cwd 所在的**最近 git 顶层**下，所以项目根下另有仓库时（工作区里的子项目），残留落在那个嵌套仓的 `worktree.dir` 目录里；而清扫以前只列项目根的对应目录，v0.189.0 的清扫上线后这些残留依然扫不到（gofer 自己某个嵌套仓的 `.worktrees/` 下仍留着 9 个）。现在清扫按项目 job 记录过的工作目录回放要扫的仓库顶层（不遍历项目树），对每个顶层仍按项目的 `worktree.dir` 模板找 job id 匹配、任务已终态、且 detached 的检出补删；安全规则不变——带分支的不动、运行中任务的检出不动、先解链再删、不跟随链接。升级后重启一次 serve，这些残留会在启动清扫里被清掉（`removed N stale temporary checkout(s)`，每个检出记一条 `job.temp_checkout_swept`）。

## [0.189.0] - 2026-10-23

### 变更

- **工作项页改版（P4，gofer-hez1）**：`/work` 的列表头换成标题 + 一行摘要（未结束数、等我数）+ 计数 chip（全部 / 等我 / 到期提醒 / 未整理，带数字，再点一次取消），旁边**新增项目 chip**（值是 URL 的 `?project=`，可分享、后退可复原，按项目收窄列表、收集箱与所有计数）。「显示已完成」、按状态 / 按工作区分组、搜索、管家合并建议、待整理（草稿 / 收集箱）、8 个状态分栏与 `WorkCard` 的全部字段和动作都保留；状态改成统一的字形 + 文字 pill，卡片上新标「停滞 N 天」（未结束、非搁置、非草稿，两天以上没有活动且没有在途的汇报 / 整理请求）。详情抽屉换成统一 `Drawer` 外壳（手机铺满宽），写备注的输入框改用统一 `Composer`，其余操作（标状态、完成 / 放弃 / 删除、搁置与提醒、请它汇报 / 写交接、整理与建议、关联会话与关联项、转 todo、合并 / 拆分、日志与里程碑切换）一项不少。本页界面文案走 zh/en 字典。
- **待验收页与验收面板改版（P4，gofer-hez1）**：`/review` 列表头的项目筛选统一成项目 chip（值是 URL 的 `?project=`，可分享、后退可复原），新增验证结果 chip（通过 / 失败 / 未验证，带计数）；每行仍是标题 / id、项目、agent、计划与待办、验证结果、提交数、用量、等待时长（等最久的在前）与行内通过 / 拒绝，≤760 每行变成一张卡片、两个按钮铺满。验收面板（任务详情的「验收」页签）改用统一的页签组件，`?tab=review&rtab=diff` 可深链到汇报 / 提交 / Diff / 验证 / 用量 / 发现 / 经验，Accept / Reject 操作条贴底固定；审阅提示、验收标准勾选、发现建 issue、Diff 下载原文与大文件降级、verify 输出保留尾部、用量逐项等原有内容与行为不变。两处文案走 zh/en 字典。
- **会话页与统一输入框（P4）**：`/sessions` 的三组会话改成带计数的页签，新增项目 chip（`?project=`）与按页签切换的状态 chip；新建会话统一收进底部抽屉表单 `NewSessionSheet`（类型 / 项目 / agent / runner 灰显原因 / 模型 / 目录 / 锁定目录 / 标题 / 第一句话 / 完整配置全部保留），会话抽屉的外壳换成统一的 `Drawer`。新增统一输入框 `Composer`（发送 / 排队 / 打断后发送、附图、急件、Ctrl/⌘+Enter、自适应高度、手机友好），会话抽屉与工作台对话的输入框都用它；排队仍按会话持久化到 localStorage。
- **信箱页改版（P4，gofer-hez1）**：`/mail` 换成两栏面板——左栏是标题、对话数与刷新 / 写信按钮、带计数的筛选 chip（全部 / 收件 / 发件 / 待指派）、项目 chip（值是 URL 的 `?project=`，按参与方所属项目收窄）、会话过滤 chip、对话列表（人物卡对、摘要、急件 / 待指派徽标、未读数、信件数与活动时间）与常驻 agent（名称 / 项目 / 会话 job / 信件数 / 空闲 / 状态）；右栏是线程——顶部「加载更早的 N 条」、右侧小地图与视口框、待指派的改投 / 撤回、广播统计、底部回复框。写信改成打开底部抽屉（收件人标签、会话多选 + 搜索、按项目 / 主题 / agent / 子路径「全部」、手写地址、投递数预览全保留），回复框与写信正文都用统一输入框，两处都有「急件」。手机（≤760）是列表 → 线程两级，返回链接保留当前筛选。本页全部界面文案走 zh/en 字典。

### 修复

- **`wf rerun --failed` 只重跑一路 fan 时不再被拒（gofer-rx7s）**：`join: all` + 多路 fan 的步骤只有一路失败时，`--failed` 只保留那一路，但 join 还写着，提交被拒（`step 1 sets join="all" but fan_out=0`），整个工作流无法重跑。现在裁剪后某步只剩一路时去掉 join，并把这一路 fan 自己的 `cmd` / `env` / `cwd` / `timeout_sec` / `agent` / `runner` 合并进该步（否则重跑会去跑该步骤自己的命令、而不是失败的那一路）；剩两路及以上时 join 与 fan 列表保持不变。
- **远端 worker 满载时 job 排队等空位，不再直接判失败（gofer-y05q）**：派发到远程 worker 的 job 数超过它的 `max_concurrent` 时，过去整个 job 以 `worker at capacity` 直接失败（exit -1、一秒也没跑），扇出验证里常有一两路遇上。现在 job 保持 `queued`，时间线上记一条 `job.waiting_worker`（带 worker id 与当时的 `in_flight` / `max`）；worker 空出名额（某个 job 结束 / 注销、调高 `max_concurrent`、重连回来）后按**提交顺序**重新派发，多个排队的 job 不会插队。**排队时间不计入 job 自己的超时**（与等目录锁一致：超时从 worker 报 `started` 才算起）；worker 断线 / 下线时排队的 job 按现有规则立即按 `worker offline` 失败。排队上限是新配置 `server.worker_queue_max_wait_sec`（默认 1800 秒，0 = 不限），超时才失败并在 `error` 里写明「一直满载、从未开始」。
- **`ref:` 临时检出不再残留（gofer-bgkk）**：workflow 的 `ref:` 步骤在 fan job 跑前建一个 detached 临时检出、结束即删，但清理只覆盖了「job 正常走完 finish」这一条路，且删除失败只记日志：job 在 shutdown 中被中断、提交在 worktree 建好之后提前失败、以及 Windows 上目录仍被进程占用时，检出会连同 git 登记一起留在磁盘上（gofer 自己的 `.worktrees/job-*` 曾积到 9 个）。现在：① 除成功 / 失败 / 取消 / 超时之外，shutdown 中断的 job 与在提交阶段提前返回的 job 也会删掉自己的临时检出；② 删除失败会重试（Windows 句柄释放有延迟），仍失败则记 `job.temp_checkout_left` 事件留给清扫，不再只打一行日志；③ 启动 / 周期性清扫按**项目自己的 `worktree.dir` 模板**（以前只认默认的 `tmp/gofer/wt`，自定义落点的残留永远扫不到）找回对应 job 已终态的临时检出补删，成功时记 `job.temp_checkout_swept`，serve 每 10 分钟一次、worker 启动时各扫一次；④ 清扫只删 detached 的临时检出——HEAD 在分支上的 `--worktree` 交付物、手建的 worktree 一律不动；⑤ 删除沿受管 worktree 的安全顺序：先解链（`worktree.links`、顶层 junction / symlink，只删链接不跟目标）→ 再删目录 → 再 `git worktree prune`，不会顺链接删到主 checkout。升级到本版后重启一次 serve / worker，启动清扫会自动清掉现有残留（日志 / 事件会写 `removed N stale temporary checkout(s)`）。

## [0.188.1] - 2026-10-23

## [0.188.0] - 2026-10-23

### 变更

- **今天页改成决策收件箱（P3 今天页，gofer-hez1）**：首屏是日期 + 「N 件事在等你」+ 一行摘要（agent 在跑 / 空闲会话 / 今天完成），下面是「自上次打开」摘要（完成 / 失败 / 新提交，点开是当天工作摘要）。新增决策类型 chip（全部 / 审批 / 提问 / 验收 / 等回复 / 记忆 / worker 申请，带计数）与项目 chip（`?project=`，可分享、后退可复原）；决策卡改成黄色左边线的面板卡，带类型 pill、agent 与等待时长。右栏放「并行中」与新增的「刚处理过」（今天处理过的最近 5 张）；贴底状态条改为在跑 / 今天失败 / 节点在线 / 用量 / 管家 / 版本。现有信息与操作（专注处理、已稍后 N、已处理 N、含 exec 待验收、稍后与撤销窗口、全局浮层）全部保留；≤1099 叠成一栏。
- **问题列表头用项目 / 状态 chip，计划列表变「计划总览」（P2 列表基元，gofer-hez1）**：任务列表（`/jobs`）与计划列表（`/plans`）的项目筛选统一成一个项目 chip，值是 URL 的 `?project=`（可分享、后退可复原）；状态改成带计数的 chip，计划列表的状态 chip 可多选。任务列表其余筛选（计划、工作流、tag、agent、runner、调用方、时间范围、含上传、显示传话 job）收进「筛选」抽屉（手机上为底部抽屉），行为与参数不变。计划列表每行显示状态、分段进度条（完成 / 进行中 / 待验收 / 受阻 / 未开始，带完成数 / 总数与百分比）、卡点（卡住的待办、失败 job 数、进行中的待办）、主会话与最近活动。新增的列表基元（项目筛选、筛选 chip、页签、底部 / 侧抽屉外壳、返回链接、分段进度条）供后续页面逐步接入。
- **web 控制台换新外壳，任务列表 /board 改名 /jobs（P1 外壳，gofer-hez1）**：左侧导航按「核心 / 协作 / 自动化 / 基础设施」分组，桌面可整体收起成图标栏、底部运行节点区默认收起（都记本机偏好）；761–1099 自动图标栏，≤760 手机改为底部 5 个标签（今天 / 工作台 / 任务 / 计划 / 更多）加「更多」抽屉（工作项排第一）。顶栏合并了「待我决策」与 worker 申请两个铃铛，新增 `Ctrl/⌘K` 命令面板（页面跳转与 `p:<项目>` 前缀过滤）、主题与语言直接开关、顶栏下的信号带；删掉了可拖动的半圆导航标签和重复的齿轮菜单。**任务列表路径改为 `/jobs`**，旧链接 `/board` 带原 query 自动重定向；其他路由与旧重定向不变。
- **web 控制台启用新的一份设计 token（P0 基础层，gofer-hez1）**：界面颜色换成松绿 + 万寿菊的新色板（黄色只用于「需要人」），字体换成自托管的 Schibsted Grotesk（正文）/ IBM Plex Mono（id、命令、日志、diff），只打包拉丁子集，CJK 用系统字体。旧变量名保留为别名，页面布局、间距、圆角与文案不变，所以 P0 只看到配色与字体变化。同时新增状态族与中英文 i18n 骨架，供后续分期接入各页面（设计见 [`docs/design/2026-10-23-ui-redesign-design.md`](docs/design/2026-10-23-ui-redesign-design.md)）。
- **计划总览的进度条不再缺段，计划详情改成页签（P3 计划页，gofer-hez1、gofer-rj5e）**：计划总览的「待验收」段不再恒为 0（`GET /v1/plans` 新增 `needs_review` / `failed_jobs`，卡点列同时显示 N 待验收、N 个 job 失败、N 进行中）。计划详情头部重做：返回链接回到总览并保留筛选与滚动位置、大号分段进度条 + 图例 + job 运行 / 失败数与最近活动、描述；链控制（链状态、leader、notify、启动 / 挂起 / 继续 / 解除阻塞）仍在头部；原来纵向堆叠的区块收成六个页签（待办 / 任务 / 决策 / 交接说明 / 评论 / 事件），值在 URL 的 `?tab=` 可深链，默认待办；待办页签保留看板与列表切换（选择记本机）。计划两页的文案走 zh/en 字典。
- **工作台改版：会话按状态分组、对话时间线内嵌审批、输入框可排队 / 打断后发送（P3 高频页，gofer-hez1）**：`/workbench` 会话列表改成按状态分组（需要你 / 活跃 / 空闲与离线，已结束默认折叠，手机上为左侧抽屉），项目筛选改用列表头的项目 chip 并写进 URL 的 `?project=`，状态改 chip 筛选，每行显示状态字形、agent、轮数、时长与 tokens。对话时间线把工具调用收成默认折叠的一组（显示条数与失败数），审批卡片内嵌在对话里可直接处理；输入框提供发送 / 排队 / 打断后发送三种模式，附图照旧；**待发队列按会话存在本机**（刷新 / 重开页面后恢复，发送或清空后清除）。项目不再是分组，原来项目行上的「待处理」计数（受阻计划 / 待决策）改成列表头一行提示，点一下去计划列表的「受阻」视图。≥1280 宽度右侧新增上下文栏：会话信息（id、agent、项目、cwd、类型、轮数、用量、开始时间）、未提交数与改动文件、关联任务与计划、待处理权限请求，以及「过程 / 改动」切换与停止会话。中继三态与「现在送」保留在会话面板里。本页全部界面文案走 zh/en 字典。
- **任务详情改成页签，任务列表可以列表 + 详情并排（P3 任务页，gofer-hez1）**：`/jobs/:id` 从一页长页改成页签——概览（待批面板、完整元信息网格、渲染命令）、验收、日志、变更（提交与未提交 diff）、消息（消息与 ACP 输入框）、产物、事件（交互、时间线、评论、通知投递、终端会话、唤醒），`?tab=` 可深链，默认页签按状态（`needs_review` 进验收、`failed` 进日志、其余概览），概览里另有「去验收」指路。主内容区宽度 ≥1280px 时列表与详情并排（详情面板不窄于 760px），列表头可切「单栏」（记在本地），更窄时点开进完整详情页并用返回链接回到原筛选与滚动位置；手机始终两级。任务列表的行统一成状态 pill + 标题 / id + 项目 + agent + runner + 徽标 + 信号 / 耗时 + 时间，耗时不再只给运行中的行，打开任务时带上当前筛选。

### 新增

- **计划接口带「待验收 / 失败 job」数（gofer-rj5e）**：`GET /v1/plans` 的每一项与 `GET /v1/plans/{id}` 新增 `needs_review`（等待验收的 job 数）与 `failed_jobs`（失败 job 数，含 timeout / cancelled），列表里的 job 汇总与 `todo_counts` 一样按计划批量聚合，不是逐计划查询。仅新增字段，旧客户端不受影响。

## [0.187.0] - 2026-10-23

### 修复

- **等人工审批的时间不再计入 ACP 每轮超时（gofer-ji7m）**：`--session` 持续会话的一轮原先用普通超时计时，人回审批慢（一轮里几次 permission 交互）就会把这一轮撑过 `--timeout` 而判 "acp: turn timed out" 失败。现在 job 停在 `pending_interaction`（工具调用审批）期间暂停这一轮的计时，作答后继续；暂停 / 恢复各记一条 job 事件 `job.turn_timeout_paused` / `job.turn_timeout_resumed`（详情带本轮预算 `timeout_sec`、已用 `used_sec`、剩余 `remaining_sec`、`interaction_id`），worker 上的 job 也镜像到 hub。审批自己的 `timeout_sec` + `on_timeout` 仍照旧兜底，等待有上限；`--max-session` 与 job 总时限按墙上时间算，**不**暂停。不影响单轮（非持续会话）job。
- **Windows 关掉通知时桌面通知不再“假成功”（gofer-3a9e）**：`ToastNotificationManager` 的 `Show()` 在 `ToastNotifier.Setting` 不是 `Enabled`（`DisabledForUser` / `DisabledForApplication` / `DisabledByGroupPolicy` / `DisabledByManifest`）时静默返回，看起来弹了实际什么都不显示，日志里也什么都没有。现在固定 PowerShell 脚本在 `Show()` 之前先读 `Setting`，非 `Enabled` 就以退出码 3 结束并打印 `setting=<值>`，Go 侧映射成“跳过”：日志记一条 `notify.desktop_skipped`，`reason` 带设置值与修复路径（Windows 设置 → 系统 → 通知：打开通知总开关与 Windows PowerShell）。同一原因仍只记一条；原因变了（例如被组策略关掉）或恢复正常弹过之后又坏，会重新记一条。脚本仍固定，事件数据仍经环境变量传入。
- **常驻收件会话用 gofer 信箱工具回信，回信不再每次找人批（gofer-sh7r）**：`mail.agents.<名字>` 的常驻会话现在启动时由 gofer 自己把 gofer MCP 注入到 ACP 会话（带该 job 的凭据，与 steward 同一套注入），所以 `gofer_session_mail_reply` 等信箱工具一定在——以前要等 agent 自己的 MCP 配置碰巧绑了 `gofer`，否则会话只能用命令行回信。开场说明也改为只给 MCP 工具一种回信方式，明确不要用命令行 / shell 回信。`approval: ask` 下，常驻会话用 `gofer_session_mail_reply` 回复**它自己收到的信**自动放行（`acp.jsonl` 记 `reason: mail_resident_reply`）；其余 gofer 写工具（`gofer_session_mail_send`、`gofer_run_job` …）、写文件、执行命令照旧按策略审批，`mode: strict` 也不放行。服务端另加一道校验：job 凭据只能碰本 job 的信件，以及它当前作为那个 `agent:<名字>@<项目>` 常驻会话收到的信，回复别的对话串 / 别的常驻会话的信件返回 403。
- **worker 断线重连后重新上报会话的轮次 / 会话状态（gofer-pcbc）**：远端会话 job 的轮次 / 会话状态（`turn_started` / `awaiting_input` 等）随 Status 帧上报，而 worker 只在状态或轮次**变化**时才发。它记的“已经发过”是进程级的：断线那一刻写进将死连接的帧（本地写成功返回、hub 根本收不到）也就被当成已送达，于是 host 侧 job 的轮次 / 会话状态会一直停在旧值，直到下一次状态变化才刷新。现在这份状态按**连接**记账，worker 每次重连都会在新连接上重发一次当前的轮次 / 会话状态（hub 本来就按连接去重，重发不会产生重复的状态行），断线前后看到的会话状态不断档。
- **三处用固定 sleep 代替同步信号的测试改为等真实可观测信号（gofer-0t9i，G052）**：caller 并发上限的测试过去先睡 50ms 再断言 job2 仍是 `queued`（睡少了断言落空、睡多了白等），现在生产侧在信号量等待处记一个只读的排队计数（`waitGate` 包裹一次 acquire，行为不变），测试等「job2 确实停在 caller 信号量上」；agent 健康度恢复的测试改为注入 `nowFn` 时钟并显式跨过 unixtime 秒边界，不再睡 1100ms；worker 交互式取消的 drain-wait 测试改为等「取消帧已发出」（证明 Run 已进三路等待），不再睡 50ms。只改测试与一个测试可见的观测点，无行为变化。

## [0.186.0] - 2026-10-22

### 修复

- **钉钉 / 飞书返回 HTTP 200 + 业务错误码时不再当成送达（gofer-map1）**：两家机器人在拒绝消息时仍回 200，真正结果在响应体的 `errcode` / `code` / `StatusCode` 里。gofer 现在解析它，非 0 即投递失败，并把机器人返回的错误原文写进投递记录的 `last_error`、serve 日志和 web 投递记录（以前一律记 `delivered`，用户没收到也看不出来）。重试口径：关键词不匹配、签名错误、token 失效这类永久错误（钉钉 310000 / 300001，飞书 19001 / 19021 / 19024）**首次即 failed 不重试**，限流（钉钉 130101、飞书 11232）与其他未知错误仍按原退避重试。generic webhook 仍只看 HTTP 状态。
- **测试里写死的短等待统一改成随负载伸缩的时间窗（G052）**：发版前五路并行验证（主机负载高）暴露的「偶发失败」都是测试自带的固定阈值太紧——`select` 里 1–2s 的 `time.After` 等 goroutine 返回、`waitFor` / `waitForStatus` / `eventuallyTrue` 等 helper 里硬编码 2s、`context.WithTimeout` 的 400–500ms 读窗口、会话 per-turn 预算 3s 只比等待长 200ms。现在这些成功路径的等待都经 `internal/testutil/wait`（随 `GOFER_TEST_TIMEOUT_SCALE` 伸缩），断言「应当超时 / 不该发生」的负向窗口保持短。只改测试，不改生产行为。

### 新增

- **webhook 新增 `keyword`：自动满足钉钉 / 飞书的「自定义关键词」安全设置**：消息标题与正文都不含该关键词时，gofer 把它加到标题前（钉钉 markdown 的 `title` 与正文标题行都带），已经包含时不重复加。这样机器人侧只开关键词模式也能收到 gofer 的各类通知，不必为每个模板手工塞词。`keyword` 只对 `kind: dingtalk | feishu` 有效，配在 generic 上、写成空串或含换行时 `gofer config validate` 直接报错。
- **桌面通知通道（gofer-3wce）**：`server.notification.desktop` 让 server 在**自己所在主机**弹系统通知，解决审批 / 等回复的 job 在浏览器里被漏掉的问题（不需要托盘进程，也不要求 HTTPS）。默认订阅 `job.awaiting_approval`、`interaction.created`、`session.awaiting_reply`、`job.needs_review`，可用 `events` / `projects` 过滤；标题与正文复用 IM 通知的渲染，点击打开 `web_base_url` 对应页面。`merge_sec`（默认 10s）把同类事件合成一条（「3 个 job 等待批准」）。平台：Windows 用 `Windows.UI.Notifications` toast（点击可开链接），macOS 用 `osascript`，Linux 用 `notify-send`（没装则跳过）；以服务（Windows session 0）或无桌面会话运行时自动跳过，每种原因只记一条 `notify.desktop_skipped`，不刷屏。整段不写 = 不启用。见 [`docs/runbook/im-notification.md`](docs/runbook/im-notification.md) §10。
- **信箱对话改为分页加载**：活跃对话动辄几十封信，打开时不再整串取回、渲染。web 信箱打开一个对话只加载**最新 6 封**（按时间顺序显示，旧在上、新在下，打开即滚到最新一封），顶部「加载更早的信件（还有更早的）」每点一次取早 6 封、插到上方并保持当前阅读位置不跳，到底显示「已到最早」；刷新 / 新信到达只补最新的信，已加载的页不重取；右侧小地图只画已加载的信，还有更早未加载时轨道顶端提示「还有更早的信件未加载」（点它等同加载更多）。接口侧 `GET /v1/mail` 新增 `before=<信件 id>`（只要更早的）与 `order=asc|desc`（`order=desc` 取最新 N 封），回答新增 `has_more`；权限 / 收件发件 / 上限过滤仍先于分页，凑不够一页时继续往后扫。不新增路由。
- **会话压缩 / 恢复后重新注入「本会话当前任务」（gofer-o53s）**：`gofer repo prime --hook-json` 在 SessionStart 的 `source` 为 `compact` 或 `resume` 时，在原有仓库级内容之后追加一段 `[gofer · 提示]`「本会话当前任务」——本会话的主题、认领中的 issue、监督中的 plan（进度 / 下一项 / 阻塞）、在等的 job、未回的信件，每项只给标题与接手命令（`gofer issue brief` / `gofer plan brief` / `gofer job show` / `gofer mail show`）。`session_id` 与 `source` 由 prime 直接读 hook 的 stdin JSON（两家的 SessionStart 都带），已装好的 hook 不用重装；`startup` / `clear` 不追加，没有当前任务或 server 连不上也不追加、不影响其余内容。tracker 不记录 claim 的会话 id，因此 assignee 与调用方对得上的是确定认领，会话启动后动过的 in_progress issue 标「可能」。新增接口 `GET /v1/sessions/{sid}/focus` 一次返回这些数据（只读，不标记信件已读，权限同读本会话）；`repo prime` 另增 `--session` / `--source` 供手动指定。
- **收件常驻会话单独的 ACP 审批策略：`mail.agents.<名字>.approval`（gofer-81b4）**：给一个收件 agent 的常驻会话单独设审批（结构与项目的 `approval` 完全一致：`mode` / `auto_allow_kinds` / `ask_kinds` / `timeout_sec` / `on_timeout` / `remember_allow_always`，校验同项目 approval），例如 `approval: {mode: ask}` 让它在写文件、执行命令前逐次问人，而项目里其他 ACP 会话仍按项目策略自动放行；不写 = 沿用项目策略（行为不变）。它**替代**（不是叠加）项目策略，只有 agent 自己的 `acp.permission_policy` 还能继续收紧。审批交互不变（web 交互面板、钉钉 / 桌面通知）。策略随 job 保存，`job resume` / 会话续接 / server 重启恢复后仍按它审批，续接出的新常驻会话继续用同一条配置。这是 server 内部字段（`session_approval`）：HTTP / MCP / CLI 提交 job 时带它一律 400 拒绝，想改审批就改配置。已知限制：策略还不能随 worker dispatch 过去，所以设了 `approval` 的收件 agent 只能跑在 server 本机 runner 上，`runner` 指到 worker 时拒绝启动常驻会话（记 start_failed 事件），不会落到那台 worker 的项目策略上。
- **终端投递不覆盖人正在输入的内容：投递方式、投递模式与「现在送」（gofer-k23r）**：往会话终端打字前先看输入区，只有确认为空才投递；不确定或非空时消息改为留待，不丢。
  - 投递改为**整段粘贴**：一条多行消息不再被拆成多次提交（以前每行一次回车，等于逐行喂给 agent）。
  - 新增**会话投递模式** `inject_mode`：`auto`（默认，只在确认为空的输入框投递）/ `hold`（从不打字，全部留待）/ `force`（不检查直接粘贴，给确定没人在用的终端）。已停在选择界面（AskUserQuestion、权限确认）时一律不投递，避免替人按下回车。CLI `gofer session inject-mode auto|hold|force [--session <id>]`，接口 `POST /v1/sessions/{sid}/inject-mode`。
  - 留待的消息进会话信箱，由会话下一次 hook 注入；`gofer session inject-mode --send-held` / `POST /v1/sessions/{sid}/held/send`（web「现在送」）可立刻送出。每次留待记审计事件 `session.inject_hold`（带原因）。
  - 自研 agent 可配 `agents.<k>.inject_guard`（`empty` / `has_input` / `selecting` 三组正则）教 gofer 认识自己的输入框；内置 claude / codex 规则，没有任何规则的 agent 不做 `auto` 检查。

## [0.185.0] - 2026-10-22

### 新增

- **信箱第三期：`agent:` 地址与常驻 agent 会话（gofer-utyo）**：信件可以写给一个配置好的收件 agent——`agent:<名字>`（取发件方所在项目）或 `agent:<名字>@<项目>`。名字在 `mail:` 配置的 `agents:` 里声明（`agent:` 必须是 acp-agent，另有 `args` / `runner` / `idle_minutes` / `max_letters` / `max_hours` / `prompt`），未配置的名字返回 400 并列出可用的名字。gofer 为每个（名字, 项目）保留一个常驻 ACP 持续会话 job：有活着的就直接送进去（复用 awaiting_input → `job say`），没有就启动一个（agent 声明 `loadSession` 时续接上一个会话），收满 `max_letters` 或超过 `max_hours` 轮换新会话，空闲 `idle_minutes` 后由会话自己的 idle-timeout 结束；全局最多 `agents_max_resident`（默认 6）个，超出的信件转待指派并写明原因。启动与投递在投递器里异步完成，发信接口不等待。发 `agent:` 信件需要 `job.submit`；受限调用方（`ceiling`）发 `agent:<名字>@<项目>` 时在发信阶段就检查目标项目，超出上限直接 403、不落信件；启动、轮换、空闲结束、启动失败记审计事件（`mail_resident`）。接口：`GET /v1/mail/agents`、`POST /v1/mail/agents/{name}/end?project=`、CLI `gofer session mail agents [--end <名字>]`；MCP 发信工具的地址说明加上 `agent:`。
- **信箱页占满视口，并加了一条对话小地图**：信箱（`/mail`）整页锁定在顶栏以下的高度——左栏对话列表与右栏时间线各自在自己的区域内滚动，页面本身不再出现外层滚动条；信件占对话区的大部分宽度（收发仍左右分边，超宽屏上用行长上限，手机宽度下满宽）。时间线右边缘新增竖向小地图：每封信一个标记，按在对话里的位置排列（信多时按比例压缩），颜色区分收件 / 发件 / 急件 / 未读，一条高亮带标出当前可视范围；点标记滚到那封信，悬停显示发件方与时间、首行摘要；三封以上才出现，手机宽度下隐藏。信件的数据与接口不变。
- **写信框收件人改为勾选，带服务端投递预览（gofer-ndy5）**：web 信箱写信框不再是一行文本——会话列表可搜索（会话名 / 标题 / agent / 项目）多选；另有「按项目 / 主题 / agent / 子路径全部」选器生成 `each:` 地址；已选显示为可删除标签，手写地址收在「高级」里。发送前向服务端要一次投递预览，显示「将投递 M 个会话（略过 K 个）」。预览就是同一个 `POST /v1/mail` 带 `dry_run: true`（G056：不新增路由），按与真发一致的口径解析（权限过滤、`each:` 选择器、收件人上限），不写任何信件、也不占广播限流。

### 变更

- **会话消息前缀统一成一张表（gofer-as5h）**：送进 agent 会话的文字不再各自造前缀，只分两类——**用户本人**发的以 `[来自 web` 开头（新消息 `[来自 web，<用户>]`；回复会话提问 `[来自 web 回复，<用户>]`，取代原来像系统消息的 `[gofer web 回复]`）；**gofer 系统消息**一律 `[gofer · <类型>]`，类型为 `job 完成` / `唤醒` / `工作项交接` / `工作项汇报` / `管家带话` / `记忆` / `提示`，不再出现 `[gofer 通知] [gofer job 完成]` 这种叠加，编号与命中原因放进正文。识别侧仍同时认旧前缀三个版本（`// DEPRECATED(v0.185): remove in v0.188`），生成侧已全部切新。

### 修复

- **`each:project:` / `each:agent:` 选择器不再只扫一页会话（gofer-ndy5）**：旧实现只读 `ListAgentSessions` 的第一页（200 条），会话多的项目会漏掉尾部；现在分页读到短页为止，命中总数仍由 `mail.broadcast_max_recipients` 决定能不能发。

## [0.184.0] - 2026-10-22

### 修复

- **只读身份在计划详情 / 工作项 / issue / 信箱页不再看到会 403 的写按钮（gofer-bp9c）**：接续今天页（gofer-darr）的做法，写操作按它真正调用的那条路由声明的权限灰显，悬停说明「只读身份」或缺少哪个权限（判定在 `web/src/utils/pageGate.ts`，与今天页同一套 `permissionGate` / `ceiling` 口径）。覆盖计划详情的标记完成 / 归档 / 重新打开、leader 与结束通知开关、启动链 / 挂起 / 继续 / 解除阻塞、交接说明的新增与保存（他人的 plan 要 `plan.manage.others`，自己的不要）；工作项的整理建议与合并建议采纳 / 忽略、issue 的新建 / 保存 / 评论（这两处路由是 Public，只受只读上限约束）；信箱回信（自己的信不要权限，别人的会话要 `session.reply.others`、别人的 job 要 `job.manage.others`）。纯查看不受影响。

### 变更

- **GitHub CI 再提速：少排队、commands 独占一片（gofer-fofi）**：`internal/commands` 现在整包单独一个 CI 任务，不再与其他包同片、也不再按测试名对半拆（它有包级 flag 全局状态和不少计时测试，与别的包同跑会慢数倍）；Windows 分片由 6 降为 5，且 Windows / macOS 只跑 `stable`（Ubuntu 仍跑 `1.26` + `stable`，版本差异由它覆盖），一次 CI 的任务数从 18 降到 10。分片表按 v0.183.0 / v0.183.1 两次实测的包耗时重算。测试覆盖不变：每个分片仍跑全部包、不加 `-short`。

### 新增

- **常驻传话人按条数 / 时长轮换，用量可见（gofer-li3t）**：web 给终端会话「发消息」共用的常驻 stream-json 进程不再无限增长上下文——`server.session_messaging.messenger_max_messages`（默认 50 条）与 `messenger_max_age_sec`（默认 2h）任一达到，就在**两条消息之间**重建（正在送达的那条不受影响），`0` = 该限制关闭。`GET /v1/runners` 的 `messenger_detail` 现在带当前进程的 `messages` / `age_sec` / `usage`（result 帧里的 token 与 `total_cost_usd`）、生效的上限 `max_messages` / `max_age_sec`，以及重建记录 `rotations`（原因 `idle` / `failed` / `max_messages` / `max_age`，含被换下进程的条数、存活时长与用量；最多 10 条）；Web「传话人」抽屉同步显示「已处理 / 存活 / 用量」与「最近重建」。
- **整理器每个项目只用一个会话，并会轮换（gofer-ayu0）**：整理器不再每整理一件工作就新开一个 agent 会话——第一次整理照旧由 job 层给 agent 分配会话（`session_inject`），gofer 记下这次 job 的会话 id，之后每次续在该会话里（agent 的 `session_resume`，即 `job resume` 那套）；续用时提示开头说明「这是新的一件工作，忽略之前的工作项」，仍只输出 JSON。累计整理 `work.summarizer_session_max_runs`（默认 20）次或会话存在超过 `work.summarizer_session_max_age`（默认 24h）后换新会话；续接失败回退成新会话并记 `work.summarizer_session_reset` 事件，整理不失败。整理器会话是内部会话：`gofer session ls`、web 工作台会话列表与会话搜索默认不显示，`--all`（接口 `all=1` / `internal=1`）可见，旧版本产生的整理器会话只要认得出也一并隐藏。
- **信箱广播（群发，gofer-metc）**：一封信可以发给多个会话，每个收件人各自一封、共用对话串与广播组，送达 / 已读 / 已回复按收件人分开可见。
  - 地址：`--to` 可重复（每个都必须唯一命中），或用选择器前缀 `each:` 取全部可投递命中——`each:topic:<关键词>`、`each:path:<子路径>`，新增 `each:project:<key>`（该项目下全部会话）与 `each:agent:<名字>`（该类 agent 的全部会话）。不带 `each:` 的地址行为不变（多命中仍转待指派进收集箱）。
  - 权限逐个收件人按单封规则：显式列出的收件人只要有一个没权限，整封拒绝并说明是哪个；`each:` 按权限过滤，并在结果里写「匹配 N 个、投递 M 个、略过 K 个」（略过的只报地址与原因，不回报会话 id）。job / steward 凭据不能发广播（403），急件不能广播（400），0 个收件人 400。
  - 限流：每个调用方 `mail.broadcast_rate`（默认 `5/10m`，超出 429）；单次最多 `mail.broadcast_max_recipients`（默认 20）个收件人。两个配置项都可热加载。
  - 收件人看到的注入标签多两个属性：`<gofer-mail ... broadcast="b12" recipients="5">`；广播副本过期只标 `expired`，不进收集箱。
  - 接口：`POST /v1/mail` 的 `to` 接受字符串或数组，响应新增 `broadcast_id` / `thread_id` / `matched` / `delivered_count` / `skipped_count` / `recipients[]`；`GET /v1/mail/{id}` 对广播返回 `broadcast` 汇总段（列表里的广播副本也带）；`gofer session mail send --to <地址> [--to …]`；MCP 发信工具的 `to` 接受数组；web 写信框可直接写多个地址（空格 / 逗号分隔）或 `each:` 选择器，并预览「将投递 N 个会话」，对话里广播副本卡带收件人状态条。

## [0.183.1] - 2026-10-22

### 修复

- **prime 超时测试在负载高的 CI 机器上失败**：测试里 server 改为挂起直到客户端放弃，上限放宽到按比例伸缩的 8 秒。功能代码无变化；v0.183.0 因此 CI 未通过、未上传 mirror，请直接使用 v0.183.1。

## [0.183.0] - 2026-10-22

### 修复

- **gofer 自己的只读 MCP 工具不再转给人审批（gofer-o49v）**：acp-agent 调用 MCP 工具时把 `kind` 报成 `other`（默认 `ask_kinds` 里就有 `other`），于是管家 job 调 `gofer_work_list` / `gofer_today_list` 时被要求人批。现在 `mode: ask` 下，工具名解析为 gofer 自己的**只读**工具（`internal/mcptools` 登记表，精确名单，不靠前缀猜）时不论 `kind` 都自动放行，并在 `artifacts/acp.jsonl` 记 `reason=gofer_readonly_tool`；写类工具、别的 MCP server 的同名伪装仍按策略，`mode: strict` 仍然每个都问人。同时每个 gofer MCP 工具都补了标准 `annotations`（`readOnlyHint`；删除 / 覆盖类标 `destructiveHint`；可选 `idempotentHint`），MCP 客户端能先分清读与写。
- **含空格 / 非 ASCII 字符路径的未提交改动不再误判是否在 `.gofer/tracker/` 下（gofer-7lp5）**：交付（合并）前的脏检查以前解析 `git status --porcelain` 的普通文本输出，只去掉路径两侧的双引号，没有反解 git 对特殊路径的 C 风格转义（`core.quotePath`，默认开启），于是路径里的中文变成 `\350\257\264` 这类转义，错误信息里列出的路径也不可读；名称里带 ` -> ` 的 tracker 文件还会被当成分支重命名、凭空多出一个 tracker 外的路径而拒绝合并。现在改用 `git status --porcelain -z`（NUL 分隔、路径不转义），重命名按 git 的「新路径、旧路径」两个字段各自判断，错误信息里列出的始终是原样路径。影响 `job worktree merge` / `job merge` 等走交付合并的路径。
- **只读身份（调用方上限 `read_only: true`）在今天页不再看到会 403 的按钮（gofer-darr）**：顶栏「只读」标记与 `+job` 之外，决策卡的 回复 / 请求汇报 / 搁置 / 按建议 / 验收与批准 / 采纳记忆建议 / 采纳建议 等会改状态的按钮现在按对应权限灰显，悬停说明「只读身份」或缺少哪个权限（判定在 `web/src/utils/todayGate.ts`，复用 `permissionGate` / `reviewGate` / `ceiling` 的现有口径）；「专注处理」同样灰显。纯查看（详情、看 diff、打开 plan、稍后 / 从稍后放回）不受影响——「稍后」是 ReadSafe 的个人偏好写，服务端对只读身份照收。
- **web 在没有 `sessionStorage` 的渲染环境里不再抛错**：`store/auth.getToken` 对缺失的 `sessionStorage` 返回未登录，按身份取的 store（whoami 等）在服务端渲染 / 组件渲染测试里保持 null，而不是抛 `ReferenceError`。

### 变更

- **审阅提示补全：删除热点带净变化与「删除显著多于新增」标记，验证页签补 agent 自述输出（gofer-hjj1）**：`deletion_hotspots` 每项新增 `net`（新增-删除）与 `deletion_heavy`（删除 ≥ 2× 新增），CLI 打印 `+4/-30 net -26` 并给后者加 ⚠；改名文件按新路径列，二进制段（`Binary files … differ` / `GIT binary patch`）不计行数。`review_hints` 新增 `report_verify`：从 agent 汇报「验证」/Verification 标题下的 ``` 代码块提取（最多 3 块，长块留尾部），带 `source: "agent"`，`job review` 标「来源: agent 自述」，web 在没有 verify 步骤的「验证」页签里作参考展示；有 verify 时该页签的输出超出 200 行改为留**尾部**（以前留开头，把失败的报错截掉）。
- **GitHub CI 的测试分片改为按包耗时自动均衡（gofer-fofi）**：Windows 由固定的 4 个任务改为 6 个、Ubuntu 2 个，分片由 `.github/scripts/ci_shard.py` 依据实测包耗时装箱生成（耗时最长的 `internal/job` / `internal/commands` / `internal/httpapi` 再按测试名切成几份，用 `-run` 分别跑）。测试覆盖不变——每个 Go 版本（1.26 / stable）× 每个平台仍然跑全部包、不加 `-short`；目的只是把发版时等 CI 的时间从 12–15 分钟压到 6 分钟左右。

### 新功能

- **定时任务参数与编辑（gofer-15jj）**：
  - 定时任务可以**声明参数**，值作为环境变量传给 job（不拼进命令行、不替换 prompt）：`string` / `int` / `bool` / `choice` 四种类型，名字必须是环境变量名且不能以 `GOFER_` 开头，最多 20 个，值最长 1024 字符。叠加顺序：计划里存的 env < 参数默认值 < 本次给的值；cron 触发与 webhook 触发用默认值。
  - `POST /v1/schedules/{id}/run-now` 可选 body `{"params":{...}}`，CLI `gofer schedule run <id> --param K=V`；给未声明的键返回 400 并列出可用参数（防止借立即执行注入任意环境变量）。每次立即执行的覆盖值记在计划事件 `schedule.run_now` 里。
  - `PATCH /v1/schedules/{id}` 原地编辑：`cron` / `at` / `title` / `params` / `enabled` / `timeout_sec`，改时间后重算下次触发；命令 / agent / runner / project 不可改（400，要换就删掉重建），权限同删除。CLI `gofer schedule edit <id> …`。
  - 声明参数的 CLI 写法：`--param-def 'NAME[:type]=default[#说明]'`（可重复）或 `--params-file <yaml>`（choice 这类带可选值列表的用这个）；`gofer schedule show <id>` 列出参数声明。
  - Web 定时页：有参数的任务「运行」带 ⚙，点开是按声明生成的参数表单（预填默认值、显示说明、底部预览将设置的环境变量），无参数的任务保持一键执行；新增「编辑」弹窗（cron + 下次运行、标题、超时、启停、参数声明增删改）；新建页也能直接声明参数。

## [0.182.1] - 2026-10-21

### 修复

- **delivery 包的合并回归测试在开启 `core.autocrlf` 的 Windows 机器上失败**：测试用的临时仓库固定 `core.autocrlf=false`（逐字节比较文件内容）。功能代码无变化；v0.182.0 因此 CI 未通过、未上传 mirror，请直接使用 v0.182.1。

## [0.182.0] - 2026-10-21

### 修复

- **workflow / 配置的 `retry.backoff_sec` 既接受列表也接受单个数字（gofer-pn0x）**：`backoff_sec: 300` 与 `backoff_sec: [300]` 等价（每次重试都等 300 秒），YAML 与 JSON 两种写法都支持；导出 / 回写仍统一成列表形式。以前单个数字报 `int was used where sequence is expected`。
- **`job worktree rm` 不再删掉正在运行的 job 的 worktree，也不再在 Windows 目录删不掉时报 500（gofer-6zmd）**：被还没结束的 job 记录的同一个 checkout（含自动续接出来的新 job——它承接源 job 的目录）一律拒绝删除并返回 409（`--force` 不覆盖），信息里列出是哪些 job；目录被进程占着删不掉、但分支与 git 登记能清时照常删完并返回 200 + `warnings` 列出残留目录，不再是「目录在、分支没删」的半状态；两者都做不到时返回 409 说明原因（什么都不删）。`gofer job worktree rm` 会打印这些 warning。
- **worktree 合并 / 交付失败不再丢掉主 checkout 里未提交的 tracker 改动（gofer-nwyw）**：合并允许在 `.gofer/tracker/` 有未提交的 issue / memory 改动时开始，失败时（冲突、`--squash` 冲突、squash 提交失败、分支与本地 tracker 改动撞车被 git 拒绝）以前一律 `git reset --hard HEAD`，把这些未提交改动一起回滚到 HEAD，实测丢过一条新建的 issue。现在只撤销合并本身（`merge --abort`；`--squash` 没有 MERGE_HEAD 时用 `reset --merge`），被合并改动涉及的路径照样复原，合并之外的未提交改动保持原样。
- **workflow 结果视图（gofer-j5xc）**：`gofer wf show` 结果表里扇出步每一路的 NAME 以前都显示步骤名（如 `checks`），现在显示那一路 fan 自己声明的 `name`（如 `linux-race` / `web`），没命名才回落步骤名；`GET /v1/workflows/{id}` 的每个 fan 行新增 `fan_name` 字段（原 `name` 保留），web 详情页结果表同样显示。
- **失败摘录不再被预期的 `ERROR:` 行淹没（gofer-j5xc）**：摘录改为优先收 go test 这类强信号失败块——`--- FAIL:` 头连同其后几行细节、`FAIL\t<pkg>` 汇总、`panic:`、`WARNING: DATA RACE` 及其后几行上下文；只有整段日志都找不到强信号时才回落到 `ERROR:` / `error:` / `✗` 弱信号行。
- **会话停在 Stop hook 的「只等 job」等待里时，job 完成通知不再被积压到下一轮（gofer-fv6a）**：等待中的 Stop hook 是轮询自己 watch 的那个通道，但通知以前会先删掉 watch、再起传话人推送——传话人的消息送不进被自己的 Stop hook 挡住的 agent，watch 又已经被消费，通知就一直压到人下一次发消息（实测滞后约 25 分钟）。现在 Stop hook 进入「只等 job」等待时向 server 报一个 park 截止时间（`stop_park_sec`），在它到期前（以及会话有打开的 relay turn 时）通知不认领 watch、不走传话人，交给那个等待在下一次轮询里带出；送达仍是一次性的（watch 就是送达凭据，由 hook 交付时消费），等待结束 / 未在等待的会话照旧走传话人。同时把 `SendMessage` 的选路与 `deliver` 统一为「有打开回合就答复回合」，不再看会漂移的会话状态。

### 变更

- **新 job id 带 `job-` 前缀（gofer-qo3t）**：新提交的 job id 形如 `job-20261021-212412-6a715d34`，与 `wf-` / `plan-` / `todo-` 一致，从 id 就能看出是哪种对象。只改新 id 的生成：已经存在的、没有前缀的 id（`20261021-212412-6a715d34`）不迁移，按 id 查询 / 续接 / 取消 / 合并 / worktree 分支（`gofer/<job-id>`）/ 结果目录（`<root>/<yyyymmdd>/<job-id>`）照常可用；识别 id 形状的地方（如 Stop hook 从终端输出里识别「job … submitted」）现在新旧两种都认。脚本里若用正则抓 job id，请写成 `(job-)?[0-9]{8}-[0-9]{6}-[0-9a-f]{8}`。

### 新增

- **`wf show` 加 JOB 列、扇出 job 标题带 fan 名**：结果表最后一列是每行的 job id（失败摘录标题也带），可直接 `gofer job show|logs`；扇出步每一路的 job 标题改为「步骤名 · fan 名」（未命名的 fan 仍是步骤名），job 列表不再全是同一个步骤名；结果视图的步骤名改从 workflow 定义读取。
- **workflow 步骤支持 GitHub Actions 风格的多行脚本与 `if: always`（gofer-od84）**：步骤与 fan 可以用 `run: |` 写多行脚本代替 `cmd`（两者互斥），用 `shell:` 选解释器——`bash`（默认，`bash -eo pipefail -c <脚本>`）/ `sh`（`sh -ec`）/ `pwsh`（`pwsh -NoProfile -Command`）/ `python`（`python -c`）；没有 `shell: cmd`，Windows 用 `pwsh` 或直接写 `cmd:`。fan 不写 `run` / `cmd` 就沿用步骤的，写了则覆盖。脚本在提交时转成 argv 的 `cmd`，下发与执行链路不变（worker 无协议变化）。
- **条件字段从 `run: always` 改为 `if: always`**：语义不变（前面失败后仍执行、结论仍是失败）。旧写法 `run: always`（没写 `shell` 时，带不带 `cmd` 都一样；升级前已存的 workflow 定义同样）仍然当条件处理，并在提交与 CLI 解析时给一条弃用警告；代码标 `DEPRECATED(v0.182): remove in v0.185`，v0.185 起不再兼容。
- **工作流 / 任务显示补全（gofer-plkx）**：`gofer wf list` 与 web 工作流列表新增「开始时间」列（终态另显示耗时）；`gofer wf show` 结果表新增 STARTED 列，`-v` 在表下逐行打印实际执行目录（`ref:` 步骤同时标出检出的提交），头部新增 `started_at` / `finished_at` / `duration`；web 工作流详情每路结果行显示开始时间与 `cwd`（含 `ref`）。
- **job 与 workflow 的关联可见（gofer-plkx）**：`gofer job show` 对 step-job 打印 `workflow:` 一行（工作流 id、步骤 / 重试 / fan 序号与 fan 名），web 任务详情页多一行可点击的 `workflow` 链接；`gofer job list --wf <wf-id>` 与 web 任务看板的「工作流」过滤框只列该工作流的 step-job，对应 `GET /v1/jobs?workflow_id=`。
- **HTTP 视图补齐（gofer-plkx）**：`GET /v1/workflows/{id}` 的每行新增 `cwd` / `ref` / `started_at` / `finished_at`（job 的 `workflow_id` / `step_index` / `attempt` / `fan_index` 原本已有）；新增 `GET /v1/jobs?workflow_id=` 过滤。

## [0.181.2] - 2026-10-21

### 修复

- **`gofer wf run --template <t> -p <project>`**：`-p` 以前被当成模板变量 `project`，没声明该变量的模板会被拒（`workflow template has no variable "project"`）；现在作为请求体的项目传给服务端，模板自己声明了 `project` 变量时由它填上。
- **`ref:` 在嵌套仓库里解析**：项目根下有多个仓库、步骤的 `cwd` 指向其中一个时，`ref` 以前按项目根解析而找不到提交；现在按「项目根 + 步骤 cwd」所在的仓库解析。

## [0.181.1] - 2026-10-21

与 0.181.0 内容相同，另修复 CodeQL `go/allocation-size-overflow` 告警（`make` 的容量提示统一走 `util.CapSum`）并整理 gofmt。0.181.0 因 code scanning 告警未推送到 mirror 与一键更新；请直接用 0.181.1。

## [0.181.0] - 2026-10-21

### 修复

- **`gofer repo changelog-merge` 不再把只有一边改过的条目判成两边都改**：「未发布」段里一条条目只有对方改过（哪怕我们这侧刚在它前面 / 后面新增了别的条目），以前会报 `changed on both sides` 并留下冲突标记；现在取改过的那一边。两边改成不同内容仍是冲突，一边删除、另一边改写也仍是冲突。
- **job 完成通知改用独立前缀 `[gofer 通知]`（gofer-11gk）**：之前系统通知经传话人送达时被包成 `[来自 web，gofer] [gofer job 完成] …`，与用户本人在 web 上发的消息（`[来自 web，<用户>]`）同形，监督 agent 会误当成用户回话。现在 gofer 自己发的通知一律以 `[gofer 通知]` 开头（后跟 `[gofer job 完成]` 等功能标签）：传话人、Stop 等待的放行理由、SessionStart / 下一轮输入的补发都是同一个前缀；`[来自 web，…]` 只代表人发的消息。同时修正识别：这类自动重入的输入仍不进「人回来了」的判定，会话对系统通知的 SendMessage 也不会被当成 web 回复。

### 新功能

- **workflow 顶层 `project` 与每路不同的扇出**：一个 workflow 可以只声明一次项目，`fan` 的每一路可以跑自己的命令。
  - 顶层 `project:` 是默认项目，步骤不写 `project_key` 就在它里面跑；从某个项目 `.gofer/workflows` 读出的模板以该项目为默认。`gofer workflow run -p <project>`（文件与模板两种提交都可用），HTTP 提交体同名字段，`vars.project` 仍作兼容回退。
  - `fan[]` 新增 `name`、`cmd`、`env`、`cwd`、`timeout_sec`，未写的沿用步骤的值；带 `cmd` 时 `agent` 可省（默认 exec）。配合 `join: all` 一个步骤就能并行跑完整套检查。`name` 在步骤内唯一，并可用 `${steps.<step>.<fan 名>.<field>}` 引用这一路的输出（`${steps.<step>.fK.<field>}` 照旧）。
  - 步骤与 fan 新增 `env`、`ref`（执行侧见下一条）、`uses` / `with`、`run: always`：字段已能解析、渲染与校验；其中 `run: always` 的执行侧见下面「结果视图」条。
  - 模板渲染与 `${steps…}` 静态校验覆盖以上全部新字段（含 `with` 递归、fan 的 `cmd` / `env` / `cwd`），markdown 分步文件的 frontmatter 同样可填这些字段。
- **workflow 模板可以查看源文件、保存与删除**：新建 workflow 时不再只能预览渲染后的步骤，也能看到模板自己的 YAML（并标明来源是内置 / 全局 / 项目），并把改过的模板存回去。
  - `GET /v1/workflow-templates/{name}/source[?project=<p>]` → `{name, source, path, content, shadows}`：按「项目 `.gofer/workflows` → `<config-dir>/workflows` → 内置」的顺序取第一份，内置模板没有路径，内容是它的 spec 序列化成的 YAML；`shadows` 是这一份盖住了哪些来源（项目文件盖住全局与内置，全局文件盖住内置）。
  - `PUT /v1/workflow-templates/{name}?scope=global`（体 `{content}`）写 `<config-dir>/workflows/<name>.yaml`，`?scope=project&project=<p>` 写该项目工作区（gofer 进程视角的路径，见 `server.path_view`）里的 `.gofer/workflows/<name>.yaml`；`DELETE` 同两个 scope。保存前先校验：YAML 能否解析、有步骤、变量按默认值渲染后不留 `${vars.x}`、扇出 / `on_failure` 等步骤字段过得了提交时的检查；不过返回 400 且一个字节都不写。名字只允许 `[A-Za-z0-9._-]`。
  - 项目模板保存后是**未提交改动**，响应带 `uncommitted: true` 与建议的 `git add … && git commit …`；gofer 不替你提交。
  - 权限 `config.write`；保存在 `auth_audit` 里留一行（谁、模板名、scope、内容长度与 sha256），不存正文。受调用方上限约束：只能写自己项目范围内的模板，全局模板要不受限的调用方。
  - **覆盖内置模板**：项目 / 全局里存一个同名文件就盖住内置模板（响应 `overrides_builtin: true`，CLI 会提示），这正是「把内置 verify 换成自己那套」的路径；内置本身永远不会被改写，不带 scope 或 `scope=builtin` 直接写内置层返回 409。删除只删该层文件，之后回落到下一层 / 内置（响应 `falls_back_to`），`GET …/source` 用 `shadows` 列出被盖住的来源。
  - `POST /v1/workflows/{id}/save-as-template {name, scope, project?}`：把已完成 workflow 的 `export` spec（已剥敏感值）序列化成模板，走同一套校验与 scope。
  - CLI：`gofer wf template show <name> --source`、`gofer wf template save <name> -f <file>`、`gofer wf template rm <name>`、`gofer wf save-as-template <wf-id> <name>`，两者都带 `--global` / `--project <p>`（默认全局，互斥）。
- **审阅提示：删除热点、被删注释与验证结果行（gofer-hjj1）**：验收材料不再只有「汇报 + Diff」，服务端按同一个 job 的 `changes.diff` 与 stdout 尾部现算三节提示，`gofer job review` 打印为「审阅提示」块、web 验收面板「汇报」页签顶部显示为默认折叠的卡片，空节省略：
  - **删除热点**：diff 里删除 ≥10 行的文件，按删除行数取前 5（附新增行数）；
  - **被删注释**：被删除、且同文件新增行里没有原样出现的注释行（`//`、`#`、`/* */`、`<!-- -->`、Go doc 注释），最多 20 行，带旧文件行号；顺手删注释、把注释挪走都看得出来（挪走的不算删除）；
  - **验证结果行**：从汇报正文里抽出的 `ok  <pkg>` / `FAIL` / `--- FAIL:` / `PASS` / `*_EXIT=n` / `exit=n` / `tsc=n` / `Test Files … passed` / `Tests … passed`，最多 30 行、保持原文顺序。
  - 新增 `GET /v1/jobs/{id}/review-hints`（权限同 diff）；`gofer job review --json` 打 `{id, status, review_hints}`。只是提示，不挡 accept / reject。
- **workflow 的 `ref:` 临时检出与 `env` 下发 worker**：检查固定提交、给每一步 / 每一路设置自己的环境变量。
  - `ref:`（sha / 短 sha / tag / 分支 / `${vars.x}`）让这一步在指定提交的**临时检出**里跑：执行机上建无分支的 detached worktree（`<仓库顶层>/tmp/gofer/wt/<job-id>`，与 `--worktree` 同落点、同样带项目依赖链接），任务一结束（成功 / 失败 / 取消）就删除。提交 workflow 时 `ref` 解析成完整 sha 写回 spec，所以同一步骤的所有 fan 与每次重试检查的都是同一个提交；`ref` 与 `worktree: true` 同写报错。
  - worker 上执行时先在自己的仓库里找该提交，没有就 `git fetch <默认远端> <sha>`（不行再全量 fetch），仍拿不到即失败并提示「该提交不在远端，先推送该提交，或改在 server 本机检查」。
  - server 启动时扫描各项目仓库的临时检出目录，把不属于运行中任务的残留（崩溃 / 重启遗留）按任务结束时的同一套步骤清掉（先删依赖链接、扫描兜底、`git worktree remove --force`、`git worktree prune`），并记日志；属于运行中任务的目录保留。
  - `env` 随派发下发 worker（worker 协议升到 v26；`wsproto.Dispatch.env` / `ref`），worker 侧按既有 env 合并规则叠加，gofer 自己的变量（服务地址、worker id）仍然优先。向协议 < v26 的 worker 派发带 `env` / `ref` 的任务会被拒绝并提示升级该 worker（旧 worker 会丢掉字段，前者跑出缺变量的假通过，后者跑错树）。`env` 的值随任务记录落库，文档已注明不要放秘密。
- **workflow 内置步骤 `uses: gofer/<名>`**：release / verify 这类流程不用再写一堆 shell，而是把「推分支与 tag、等 CI、等发布产物」交给 gofer 自己执行。
  - 三个内置步骤：`gofer/push`（`with: {refs: [...], remote}`；分支推 `refs/heads/`、tag 推 `refs/tags/`，**永不强推**，推完用 `ls-remote` 核对每个 ref 的远端 sha）、`gofer/wait-ci`（`ref` / `required` / `code_scanning` / `timeout_min`；GitHub，`required` 任一失败即失败，`code_scanning` 只看该 ref 上开放的告警）、`gofer/wait-assets`（`tag` / `assets` glob，等 Release 上的产物齐全）。
  - `with` 按步骤自己的结构**严格校验**，未知键、缺必填参数都在提交时报错；`uses` 只接受 `gofer/<内置名>`，不能与 `cmd` / `prompt` / `fan` 同写。
  - 执行是一个内部 exec job（`gofer tool step <名>`，标签 `workflow-step`，`with` 以 JSON 走 stdin），所以在步骤的 `runner:` 上跑，日志 / 取消 / `on_failure: retry` 与普通步骤一致；在 worker 上跑要求该 worker 协议 ≥ v26，旧 worker 提交时即被拒。
  - 信任模型：推送需要项目 `delivery.allow_push` **且**提交方持有新权限 `repo.push`（中风险，默认不授予任何人），否则提交即 403 并说明。
- **workflow 结果视图、重跑与 `run: always`**：跑完（尤其是失败时）能一眼看出哪一步、哪一路失败，只重跑要重跑的部分。
  - **结果表**：workflow 详情（`GET /v1/workflows/{id}` 的 `steps[]`、`gofer wf show`）每一行步骤 / fan 都带 `runner`、`exit_code`、`duration_sec`，失败行还带 `failure_excerpt`：从该任务日志尾部按规则抽 `--- FAIL:` / `FAIL` / `✗` / `error:` / `panic:` 之类行，去重、保持顺序、最多 20 行；没有这类行时（内置步骤、只报一句错的 agent）取任务记录的错误信息。`gofer wf show` 打印成一张表（STEP / ATT / FAN / NAME / RUNNER / STATUS / EXIT / TIME），失败摘录跟在表后。失败即停（fail-fast）也不影响已结束步骤的信息：workflow 失败时同样完整显示。
  - **重跑**：`gofer wf rerun <id> [--failed | --from <step>]`，HTTP `POST /v1/workflows/{id}/rerun {mode: failed|from, step?}`。它基于原 spec 裁剪出**新的** workflow 提交，原 workflow 一行不改：`--failed` 只保留失败的步骤，失败步骤里只保留失败的那几路 fan；`--from` 从该步（序号或名字）起保留后续全部。变量与原 `ref` 解析出的 sha 保持原值，所以重跑检查的是同一个提交；新 workflow 带 `rerun_of`（原 id）。仍在运行的 workflow 不能重跑（先取消）。裁剪后如果某步的 `${steps.N…}` 引用指向了被去掉的步骤，提交时会明确报错而不会跑错。
  - **`run: always`**：步骤在前面失败后仍然执行（失败即停时跳到这些步骤按顺序执行，其余步骤跳过），但**不改变** workflow 的最终结论——前面失败了整体仍然失败，失败原因保留（第二次失败会附加在后面）。此前提交会报「暂不支持」，现在可以直接用，典型写法是 release 失败后的清理 / 通知步骤。
- **web 控制台上的 workflow 模板编辑与运行结果**：模板可以看源文件、改、存、删；跑出来的每一步 / 每一路都有结果行、失败摘录与日志入口，失败时可以只重跑失败的部分，跑好的运行也能一键存成模板。
  - **新建 workflow → 模板**：每张模板卡可以「查看源文件」，标明来源（内置 / 全局 / 项目）、来自哪个文件、盖住了哪些层。有 `config.write` 时可以「编辑」：YAML 编辑区 + 保存；服务端校验不过时把原因就地显示（YAML 位置与原因），一个字节都不写。保存前选「全局」或「当前项目」；存的是内置同名模板时提示「将覆盖内置模板 <name>」。
  - 项目模板保存后是**未提交改动**：页面醒目提示「已写入项目工作区，尚未提交——提交到仓库才算数」并给出可复制的 `git add … && git commit …`；还能「新建模板」与「删除」（删除要二次确认，删完说明名字回落到全局还是内置）。内置模板只能查看；没有 `config.write` 时整块只读并写明原因。
  - **workflow 详情 → 结果**：每个步骤 / 每路 fan 一行，列出 runner、状态、退出码、耗时；失败行可展开 `failure_excerpt`，并有到该任务日志的链接。「只重跑失败的」与每行的「从此步重跑」确认后提交，完成后直接跳到新的 workflow；「另存为模板」把这次运行的 spec（已剥 secret）存成全局或项目模板，同样的未提交提示。手机宽度下结果表改成纵向卡片（字段带标签）。
- **`gofer workflow init verify|release` 与内置 verify / release 模板**：检查与发版这两类流程不用再从空白 yaml 写起。
  - `gofer workflow init verify [--dir <仓库目录>]` 生成 `.gofer/workflows/verify.yaml`，按仓库内容预填：有 `go.mod` → `go test ./...` 与 `go vet ./...` 两路；有 `package.json` → 其中存在的 `test` / `build` / `lint` 各一路（按 lock 文件用 `npm` / `pnpm` / `yarn run`，在子目录时带 `cwd`）；两者都有则合在同一步骤的 `fan` 里（`join: all` 并行）；都没有则留一路占位命令并注明改法。
  - `gofer workflow init release [--dir <仓库目录>]` 生成 `push`（当前默认分支 + `v${vars.version}`）→ `wait-ci`（有 `.github/workflows` 则取其各 workflow 的 `name:` 作 `required`）→ `wait-assets`（注释示例）。
  - 生成的文件带中文注释逐段说明改法；生成结果先过一遍模板校验（同保存模板的规则），不可能写出一个跑不起来的文件。已存在且不带 `--force` 时拒绝并说明；生成后提示「提交到仓库后 server 才读得到」。
  - 内置模板 `verify`（var `project` / `commit` / `cmd` / `runner`）与 `release`（var `project` / `version` / `branch` 默认 `main` / `runner`）：项目里存同名文件即可盖住。
  - skill 的 [plans-workflows](skills/gofer-usage/references/plans-workflows.md) 新增「verify / release 起步 workflow」一节：两个完整示例、与 GitHub Actions 的对照表、信任模型与 `repo.push`。

## [0.180.1] - 2026-10-21

与 0.180.0 内容相同，只修了一个测试：Windows CI 上 `TestDispatchOutlivesConnection` 因固定 3 秒等待超时，0.180.0 的 CI 未通过，因此未推送到 mirror 与一键更新；请直接用 0.180.1。

## [0.180.0] - 2026-10-21

### 新功能

- **CHANGELOG「未发布」段的合并驱动（gofer-yf4p）**：`gofer repo changelog-merge <base> <ours> <theirs> [<path>]` 作为 `CHANGELOG.md` 的 git 合并驱动（签名同 tracker 驱动，`%O %A %B %P`，结果写回 `%A`，退出码 0 = 合并成功、非 0 = 留了冲突）。并行分支各自往「未发布」里加条目时按 `### 小节` 自动并集：我方保留条目顺序、对方新增条目按对方顺序追加，多行条目与缩进子项整块搬，一边删一边没动的删，两边改成不同内容的同一条目留冲突标记；只有一边加的小节整节保留。默认识别 `## [未发布]` / `## [Unreleased]`（`--unreleased` 可加自己的写法）。已发布版本段与文件头仍走 `git merge-file`：两边改同一处就是 git 的标准冲突标记 + 非零退出，不做猜测；发版改名（一边把「未发布」改成版本号并新开空段、另一边往旧段加条目）时新条目落到新的「未发布」段。`--install [--file CHANGELOG.md]` 把 `CHANGELOG.md merge=gofer-changelog` 写进 `.gitattributes`（要提交）并在本克隆的 git config 登记驱动，幂等。
- **工作台附图新增「命令行参数」送达（gofer-r0ke）**：`agents.<k>.image_input` 新增取值 `arg`，配合新字段 `agents.<k>.image_args`（如 `["--image", "{{path}}"]`，每张图展开一次，须含 `{{path}}`、不得含 `{{prompt}}`）：图片仍先放到执行机，但路径不再写进正文的 `[附图 n]` 行，而是追加到该次续接 job 的 argv（落在 job 的 `cmd` 里，落库与转发 worker 都不丢）。内置 `suga` 模板改为 `image_input: arg` + `image_args: [--image, "{{path}}"]`（suga v0.40+ 的 `suga run --image`），工作台里命令行 suga 会话的 📎 不再置灰；`suga-acp` 仍由 ACP `promptCapabilities.image` 决定是否内联。发送给**正在运行的终端会话**（`suga send`）还没接 `--image`，那条通路仍按不支持处理。thread 能力位新增取值 `arg`（Web 与 `inline` / `file` 一样可用 📎）。
- **worktree 位置 / 分支 / 依赖链接与 `--var @file`（gofer-7b8e）**：`--worktree` job 的落点不再写死。
  - 项目配置新增 `worktree:` 段：`dir`（相对仓库顶层，默认 `tmp/gofer/wt/{job_id}`）、`branch`（默认 `gofer/{job_id}`）支持 `{name}` / `{job_id}` 模板，`links` 列出建好 worktree 后从主 checkout 同路径链接进来的目录（Windows 目录联接，其它平台符号链接，不需要管理员权限）。
  - `job run --worktree --worktree-name <名> [--branch <分支名>]`：`{name}` 来自 `--worktree-name`（未给时 = job id，默认布局不变，只允许字母数字 `.` `_` `-`），`--branch` 单次覆盖模板。同名 worktree 目录或同名分支已存在时提交失败（不复用）。
  - 链接是 best-effort：源目录不存在、worktree 里已有同路径、链接创建失败只记一条 `job.worktree_links` 事件（带 `linked` / `skipped` 原因）并继续执行。`job worktree rm` 与交付后的清理**先逐个删链接本身再删 worktree**，不会顺着链接删掉主 checkout 里的目录与内容。
  - worker 上的 job 由那台 worker 建 worktree 与链接；`--worktree-name` / `--branch` 随派活转发。
  - `config validate` 校验模板只含 `{name}` / `{job_id}`、展开后不逃出项目根，`links` 是项目内相对路径。
  - 任务书模板变量支持 `--var key=@file`（读文件）、`--var key=@-`（读 stdin）、`--var key=@@text`（字面量 `@text`）；值在 CLI 侧读取，文件不存在 / 是目录 / 超过 1 MB 时报错并指明变量名。
- **worktree 合并收尾与 `job wait`（gofer-7b8e）**：合并后要做的机械活由 gofer 一次做完，派活一方只审 diff、决定合不合。
  - `job worktree merge` / `POST /v1/jobs/{id}/worktree/merge` 新增：`--todo-done [--note <说明>]`（合并成功后把 job 所属 plan todo 标 done，说明默认 `<分支> 合入 <主线短 sha>`；job 不属 todo 时忽略并在结果里给 warning）、`--comment <评论>`（在 job 上留评论，按当前凭据身份记录，`@` 仍按现有派活规则处理）、`--cleanup`（删依赖链接 → 删 worktree → `git branch -d` 删已合并分支）。三者只在合并成功后执行，失败只记 warning，不会让已完成的合并变失败；有冲突时照旧中止复原，不做任何收尾。
  - 合并前检查放宽：主 checkout 的未提交已跟踪改动只在 `.gofer/tracker/` 下时允许合并（其他文件仍 409 并列出文件名），因为监督过程中 tracker 几乎总是有改动。
  - `.git/index.lock` 被占用时每秒检查一次、最多等 60 秒再合并；超时返回 409「锁仍被占用」，**绝不删锁文件**。
  - `gofer job wait <id>... [--any] [--timeout <时长>]`：等这些 job 全部（或 `--any` 时任一）到终态后退出，输出每个 job 的状态、退出码与标题；超时非零退出并列出还在跑的 job。走现有读取接口轮询（`GET /v1/jobs/{id}`），没有新增 HTTP 路由。
  - 修复：worktree 的依赖链接改成建立时就把**实际建成的链接**记在 job 上（`worktree_links`，随 job 落库），清理按这份记录逐个删；另外删 worktree 前还会扫一遍顶层与已知路径，凡链接 / 联接先删链接本身再继续——配置事后删掉某条 `links` 也不会把残留链接交给 `git worktree remove --force` 顺着删进主 checkout。

## [0.179.0] - 2026-10-20

### 新功能

- **扩展设置与 API 直调（gofer-k1cg）**：
  - `gofer settings ls|get|set|reset|export|import`：读写 server 数据库里的扩展设置（降噪阈值与排除名单、提示词覆盖）。`ls` 给键 / 类型 / 是否改过 / 当前值的表（`--json` 完整条目）；`get` 只打值（列表一行一个）；`set` 按键类型转换，列表用逗号分隔，`--file <path|->` 取多行提示词；`reset` 回到内置默认；`export` 只导出改过的键（YAML）；`import` 默认逐键预览（新增 / 修改 / 不变 / 拒绝及原因），`--yes` 才写。
  - `gofer tool api [-X METHOD] <path> [-f key=value]… [-F key=@file]… [--input file|-] [-q <路径>] [-i]`：用 CLI 自己的地址与 token 直接调 server 的 HTTP API，`path` 可省 `/v1`；非 2xx 非零退出并把错误体打到 stderr，`-q` 按路径取值。
- **整理与管家降噪（gofer-k1cg）**：测试 / 试一试的会话不再变成工作项，也不再被整理器和管家拿去点评。
  - 工作项草稿改成同一会话**第 `work.draft.min_prompts` 次人工提问**（默认 2）才建，标题取触发它的那次提问；不属于任何项目的会话不建；`work.draft.exclude_projects` / `work.draft.exclude_cwds`（前缀，Windows 不分大小写与分隔符）/ `work.draft.exclude_title_patterns`（正则）命中的不建。已经建出来的草稿不动。会话表新增人工提问计数（additive）。
  - 短命会话（已结束或离线、存活短于 `work.short_session.max_minutes`（默认 10）且人工提问少于 `work.short_session.max_prompts`（默认 3））不进整理器：整理记录里留一条 `skipped` 与原因，**不占每日整理额度**；管家巡检、首条摘要的未结列表、会话离线 / 结束事件同样跳过。
  - 上面六个都是扩展设置（`settings` 表），web 设置 →「扩展设置」或 `gofer settings set` 改了立即生效；读设置失败时退回默认值并记日志。
- **整理器与管家的提示词可改（gofer-k1cg）**：三段提示词各有「整段替换」与「追加」两个扩展设置键——管家角色 `prompt.steward.role` / `.extra`、管家巡检 `prompt.steward.review` / `.extra`（每日 / 手动 / 事件三种巡检都适用）、整理器 `prompt.work.summarizer` / `.extra`；替换键为空就用代码默认，`extra` 追加在生效的提示词之后并以 `## 使用者补充` 分隔，改了下一次整理 / 巡检即生效。整理器要求的 JSON 输出格式说明从提示词正文拆出、**总由代码追加在最末尾**，替换键改不掉它（解析不会坏）；管家巡检的动态材料（工作项清单、待处理事件、`「今天」待决策卡`、记忆整理步骤）也仍由代码拼接，替换的只是指令正文。默认提示词改写一版：整理器只记录有实质进展的会话，试用 / 测试 / 演示性质的操作不算进展、不给建议，材料不足如实写「信息不足」；管家巡检不再给测试性事项提建议，优先真正卡住、等人、到期的事，建议具体到下一步动作。
- **Web 设置 →「扩展设置」页**：
  - 按 `work.*`（降噪阈值 / 排除名单）与 `prompt.*`（提示词覆盖）分组列出全部扩展设置，每项显示中文说明、类型、当前值、默认值、「已改过」徽标与最后修改人 / 时间。
  - 按类型编辑：整数（带范围提示）、开关、名单（一行一个，正则保存前先检查能否编译）、提示词（大编辑区，**留空 = 使用内置默认**，可展开「对比默认」）。保存立即生效（其他进程 5 秒内可见），「恢复默认」要二次确认；服务端拒绝的原因（越界、正则编译失败、超长）显示在那一项下面。
  - 「导出 YAML」只下载改过的键；「导入」选文件后先预览逐键差异（改为 / 恢复默认 / 不变 / 拒绝及原因），确认后才写入，被拒的键保持原值。
  - 写需要 `settings.write`，调用方上限只读时整页只读：写按钮隐藏并说明原因。
### 改进
- **记忆加独立标题（gofer-pkyb）**：记忆新增可选字段 `title`（显示名，≤ 80 个字符、单行），与「一句话摘要」`summary` 分开。`gofer memory set --title`（传 `-` 清空），`memory show` / `ls`（含 `--json`）带标题，`ls <关键字>` 也搜标题；有标题时 `ls` 行与 prime 记忆索引行显示「标题 — 摘要」（没写摘要只显示标题），没有标题的记忆显示不变。服务端全局 / 项目记忆同样支持：HTTP `POST|PUT /v1/memories` 接受并返回 `title`，MCP `gofer_memory_set` 新增可选 `title`。旧数据无需迁移；tracker 同步按字段合并 `title`，`gofer repo status --changed` 会列出 `title` 字段变化。
- **Web 信箱按对话显示、会话以人物呈现（gofer-yhqs）**：左栏不再按串分（同一对会话来回几轮会变成好几条），而是一行一个**对话**——两个参与方之间的全部往来，方向无关；每行有两个人物的名字、最后一封的纯文本摘要、未读数、急件、信件数与活动时间。右栏是该对话从早到晚的时间线：自己的一方在右、对方在左，回信标「回复 #N」并可点击跳到被回复的那封，正文按 **markdown 渲染**（代码块 / 标题 / 列表都正常显示，仍经消毒），长信默认折叠。人物卡显示名字（会话主题 → 标题 → agent · 项目）、agent · 项目 · 执行机、状态点（在线 / 空闲 / 等回复 / 离线 / 已结束），头像按 id 稳定取色，悬停看完整 id 与 cwd，点会话名字打开会话抽屉。原有的过滤、写信、回信、改投、撤回、打开即已读都保留；手机宽度下列表与时间线切换显示。对话来自新增的 `GET /v1/mail/conversations`，时间线用 `GET /v1/mail?peer=&with=`。

## [0.178.0] - 2026-10-20

### 新功能

- **调用方上限第二期（gofer-7798）**：受限调用方（配置了 `ceiling`）的边界补全——
  - 已打开的长连接（SSE、attach、隧道、托管转发、`/v1/ws`）按当前上限与只读复核，上限收紧后 10 秒内断开；job 凭据开的连接按其上限所有者复核。
  - `/v1/ws` 推送按范围：范围外的 `job:<id>` 主题拒绝订阅，失效通知按项目过滤，`stats` / `pending` 快照按范围单独计算。
  - 聚合按范围：今天页（卡片、lanes、已处理、稍后、digest）、`/v1/stats`、`/v1/stats/overview`、工作项摘要、`/v1/meta` 只含范围内数据；用量、数据库、会话统计等服务器级数字不返回；管家路由与会发 webhook 的摘要 `POST` 对受限调用方 403。
  - 审阅：审阅他人提交的 job 需要新权限 `job.review.others`（升级时自动补发给全体用户类调用方，现有部署行为不变）；受限调用方不能审阅上限所有者是自己的 job。
  - 多项目 / 通配符上限下 jobs 列表直接按范围分页（含只在 worker 上的项目）。
  - 中继 job 的上限所有者记为真正发起它的调用方（催办创建者、信件发信人、会话 owner），不再用显示标签。
  - `GET /v1/config` 显示每个调用方的上限；新增 `GET /v1/schedules/{id}/events` 与 `gofer schedule events <id>`，webhook 触发与 run-now 被上限拒绝时同样记录；上限拒绝写授权审计（每分钟每键一条）。

### 修复

- **suga 会话里的 gofer 命令认得出自己是哪个会话（gofer-fitp）**：`SUGA_SESSION_ID`（suga v0.39 起在子进程里导出完整会话 id）加入会话识别变量（优先级最高：SUGA → GOFER → CLAUDE → CODEX；suga 在 gofer job 里跑时环境里同时有 job 的 `GOFER_SESSION_ID`，不能让它盖过 suga 自己的会话），`plan create` 自动绑定、`X-Gofer-Session-Id` 请求头、`--session` 默认值、`session topic` 等都用它；此前 suga 会话里发的信被记成「调用方 default」，收件会话回信时落进收集箱。该变量同时加入 job 环境禁止清单。`SUGA_SESSION_ID` 指向的会话在 server 上不存在时，请求报错并点名该变量（`plan create` 的自动绑定则不绑并在提示里点名），不会回落到其他变量；Claude / Codex 行为不变。`memory set` 的默认 source 与 `work report` 的默认会话也改走同一识别规则。
- **`session mail send "session:<前缀>"`**：地址里的会话 id 与 `--session` 同一套前缀规则——唯一匹配就投递给完整 id；多个匹配报错（409，列出最多 8 个完整 id）；没有匹配报 404。此前 `ls` 显示的前缀会被当成「没有可投递的会话」静默落进收集箱。HTTP / MCP 发信走同一处服务端解析。
- **撤回 / 改投待指派的信，收集箱条目随之丢弃**：此前信被撤回后条目仍是 captured，成了没人处理的噪音；现在条目转为 dropped 并在日志里写明原因（已被人转走的条目不动）。

### 改进

- **`gofer session ls` 默认隐藏久未活动的离线 / 已结束会话**：状态为 offline / ended 且最后活动（`last_seen_at`）超过 5 天的不再列出，末尾提示「另有 N 个超过 5 天未活动的会话已隐藏，用 --all 查看」；运行中 / 空闲 / 等回复等状态无论多久都显示；`--all` 全列，明确给了 `--state` 时不隐藏。仅 CLI 过滤，HTTP 默认行为与 web 不变（`-a` 已被 `--agent` 占用，所以只有 `--all`）。
- **`gofer session ls` 的 SESSION 列能区分 suga 会话**：不再固定截 8 个字符（suga 会话 id 都以 `sess_261…` 开头），改为显示在当前列表里唯一的最短前缀（至少 8 字符），新增 `-l/--long` 显示完整 id；`session find` 同样；`session show` 本来就输出完整 id；多个前缀匹配的报错现在列出候选完整 id。
- **Jobs 页状态计数（web 端）**：只给进行中的状态（排队、运行、等输入、待应答、待验收、待批准等）显示数字，完成 / 失败 / 取消等终态不再计数；计数只看最近 3 天（选了更短的时间范围就按所选），并一次拉全 3 天内的 job，不再因列表默认 200 条截断而数不准。
- **Web 控制台跟随只读 / 受限身份（gofer-7798 第二期）**：只读 token 在 job 详情页只剩查看——重建、取消、删除记录、合并 / 交付、继续会话 / 续接、会话发消息、交互作答、评论输入、验收与批准都不摆，日志、事件、验收材料、评论列表照常；验收与批准另按权限显示（要 `job.review`，审别人的 job 还要 `job.review.others`，受限调用方不能审自己提交的 job），不满足时按钮位置给一行灰字说明原因，验收台 `/review` 的行内裁决同一条规则。受限调用方被省略的 `/v1/stats` 与 `/v1/today` 字段在界面上整块不显示（统计页系统卡、今天页状态条），不再当成 0 显示。

## [0.177.5] - 2026-10-20

### 修复

- **排队久的 job 一开始就只剩很少时间（gofer-drlm）**：job 排队等并发槽位 / 目录锁的时间本来就不算进执行时限，但 agent 看到的截止时刻是**提交时**算好的——`--timeout 2400` 的 job 排队 25 分钟后开跑，prompt 与 `GOFER_JOB_DEADLINE` 都只剩 15 分钟，agent 自行收尾、产出为零。现在 `GOFER_JOB_TIMEOUT_SEC` / `GOFER_JOB_DEADLINE` 与执行时限一样从真正开始执行（离开 queued / waiting_dir）那一刻起算，远程 worker 上用的是执行机自己的开始时间；prompt 里的时限行只写时长并说明「从开始执行算起」，绝对时刻以环境变量为准（prompt 提交后不再改写）。`gofer job show` 与 job 详情新增 `queued_at` 与「排队 X」，与 `started_at` 一起把排队时长和运行时长分开显示。

## [0.177.4] - 2026-10-20

### 修复

- **手机导航标签点了没反应（web 端，0.177.3 引入）**：点按时在 pointerup 就打开了抽屉，标签随即隐藏、遮罩出现，手机合成的 click 落到遮罩上又把抽屉关掉；改为在 click 里开关抽屉。
- **「会话等待回复」提示末尾乱码（web 端）**：提示标题里的会话名按 40 字节截断，中文标题会被切在半个字上，末尾显示成乱码；改为按完整字符截断。`gofer` CLI 列表里的长指令摘要同样修正。
- **会话抽屉的署名（web 端）**：你答 agent 正在等的那一轮（例如 Stop hook 停下等回复）时，气泡署名改为「你（回复）」；经传话人 / 在线会话 / 终端送达的仍是「你（经转达）」。
- **消息提示气泡中文末尾乱码（web 端）**：Agents 页「探针」结果气泡的首行证据按 200 字节截断，恰好切在中文字符中间就显示成乱码；改为按字符边界截断。同类的 ACP 事件原始输入摘要（512 字节）与会话转达正文（16KB）截断也一并改为不切断多字节字符。

## [0.177.3] - 2026-10-20

### 修复

- **手机导航标签拖不动（web 端）**：0.177.2 把拖动下边界提到视口高度 − 360px，可拖范围只剩上面六成，标签停在边界处往下拖、松手又弹回原处，看起来像拖不动；下边界改为视口高度 − 200px（仍不压 📎 与发送行），拖动中就夹在区间里、到边界停住；导航抽屉展开时标签隐藏，不再压住菜单项（点遮罩或菜单项关闭）。

## [0.177.2] - 2026-10-20

### 修复

- **会话抽屉的附图与重复消息（web 端）**：同一句转达答复不再显示两个气泡——outbox 消息的 `decision_id` 命中某轮次时只留带送达状态和缩略图的那条；去掉输入框上方「已回复 agent ✓」这类冗余结果提示（失败提示照旧）；气泡正文末尾由服务端追加的 `[附图 n] 路径` 不再显示，改为气泡下方该消息的缩略图（没有附件记录的老消息保持原文）；会话抽屉的 📎 只看 thread 的附件能力位（agent 没在等回复也能发图），不可用原因只写一处；手机端贴边导航标签默认改到视口高度 45%，拖动下边界改为视口高度 − 360px（不进输入区），旧位置越界会被夹回边界内。

## [0.177.1] - 2026-10-20

### 变更

- **手机端主导航入口改成贴边小标签（web 端）**：宽 ≤900px 时左下角那个 48×48 浮动按钮会压住工作台输入框里的 📎，现在改成贴在屏幕边缘的半圆小标签（约 24×48，默认在左边缘、视口高度 60% 处）。按住可以上下拖动、也能拖到另一侧，松手吸附到最近一侧，垂直位置限制在顶栏下方到视口底部上方 120px 之间（避开底部输入区），位置记在浏览器里；移动没超过 6px 仍算点击。输入框获得焦点或页面滚动时标签淡出且不可点，抽屉打开时始终可点（用来关闭），「减少动态效果」下不淡入淡出。桌面宽屏不变。

## [0.177.0] - 2026-10-19

### 新增

- **工作台的输入框能带图片了（web 端）**：工作台会话的输入框与会话抽屉的回复框都多了一条附图条——粘贴、拖进输入区、点 📎 选文件（手机上系统给出「拍照 / 相册」）。选中即上传，缩略图上显示进度、失败可重试、未发送的可删除；上传没完成或还有失败项时「发送」不可用，发送成功才清空。一条消息最多 6 张、单张上限由服务端定（默认 10MB），超了就地提示不上传，只收 PNG / JPEG / GIF / WebP。手机（宽 ≤900px）下大图在浏览器里压到长边 2048 的 JPEG 0.85（长边不超 2048 且小于 1MB 的 PNG 截图保持原样），桌面默认发原图、超过 10MB 才压，GIF 不压；压缩时 EXIF 随之丢弃。历史里发出的消息下方显示缩略图网格，点开看原图，过期后显示「图片已过期」。thread 带 `attachments` 能力位（`inline` / `file` / `none`）：前两种 📎 可用（用户看不出差别，送达方式由服务端决定），`none` 与字段缺失时 📎 置灰并以 title / 小字说明原因。发送走 `POST /v1/workbench/threads/{id}/turn` 的 `attachments` 字段（最多 6 个 id），服务端侧的发送与送达见后续条目。
- **工作台图片附件的 server 端（web 端见上一条）**：新增 `POST /v1/attachments`（multipart 上传 PNG / JPEG / GIF / WebP，按文件头判定类型，SVG 与其它类型一律 415；超过 `server.attachments.max_bytes`（默认 10MiB）413，每个 caller 每小时超过 `server.attachments.hourly_per_caller`（默认 200）429）、`GET /v1/attachments/{id}`（原件，`?thumb=1` 为长边 320px 的缩略图，失败回退原件；未发送的只有上传者能读，发送后跟随所属会话的可见性）与 `DELETE /v1/attachments/{id}`（上传者在发送前删除）。文件存于 `<配置目录>/attachments/`，到期由 serve 的清理循环删除（`storage.attachment_retention_days`，默认 14 天，负数 = 不清理）；`server.attachments.max_per_message`（默认 6）供发送侧使用。不涉及 worker 协议；回到旧版本时新表与目录被忽略。

- **ACP 会话支持发送图片内容块（server 端，web 随后跟进）**：agent 在 initialize 里声明 `promptCapabilities.image` 时，会话可以收到 ACP `image` 内容块（文本在前、图片按序在后）；job 层新增 `SaySessionBlocks` / `SessionAcceptsImages`，不支持图片的会话返回 `ErrImagesNotSupported` 且不发送任何内容，调用方改走文件送达。worker 协议 v25 的 `session_cmd` 携带 `images`、`status` / `inflight` 回报 `session_images`；协议低于 v25 的 worker 上的会话一律视为不支持图片。该能力随会话持久化（`session_state_json`，重启后由 agent 的 initialize 重新确认）。

- **工作台发送图片（server 端发送与历史）**：`POST /v1/workbench/threads/{id}/turn` 新增 `attachments: [id…]`。持续 ACP 会话且 agent 声明支持图片时图片作为内容块内联发出；其余会话（命令行会话、终端会话、不支持图片的 ACP）把图片经 xfer 放到执行机项目的 `tmp/gofer-attachments/<日期>/`，在消息末尾追加 `[附图 n] 绝对路径`（协议低于 v25 的 worker 给项目相对路径并注明项目）；放图失败整条不发并返回原因，pty job 与不支持读图的 agent 返回 400。附件必须是调用方自己的、未发送的（403 / 409），数量受 `server.attachments.max_per_message` 限制。每个 thread 带 `attachments`（`inline` / `file` / `none`）与 `attachments_reason`；历史里 ACP 事件流的 `prompt` 事件、job 的 `messages` 与会话详情的 `turns[]` 带 `attachments` 引用。新增 agent 配置 `image_input: path|none`（内置：claude / codex 及 `-acp` 变体 = path，suga = none，未知 = none）。

### 修复

- **工作台给终端会话发消息，会话在运行 / 空闲时失败**：之前走 `relay.Say`，只有会话有未答复的回合才行，否则报 `session has no open turn`；现在和会话抽屉的网页消息走同一个入口（`SendMessage`：等待回复的会话答复回合，有 Claude SendMessage 地址的走传话人，否则 deliver_command → tmux），turn 响应多了 `path`（渠道）/ `message_id` / `delivering`，`decision_id` 在有回合或审计行时给出；`GET /v1/sessions/{sid}/outbox` 的消息带 `attachments`。带图片时先按同样的路由确认会话可投递，再放图片（不可投递就不放、不发）；图片路径与正文写在同一行（终端按行提交）；发不出去时返回「会话当前无法接收消息：…」的 409 / 502。

- **`--timeout` 超出上限被截断时提示不清、来源写错**：之前只在 stderr 写「project ceiling」，而上限实际可能来自 `server.max_job_timeout_sec`。现在 job 结果带 `timeout_limit_source`（`project` / `server`）与 `timeout_limit_key`，CLI 警告和 `job show` 写成 `timeout 10800s clamped to 3600s by server.max_job_timeout_sec`，stdout 的 `submitted` 行在被截断时追加 `timeout=3600s (clamped from 10800s)`，MCP 提交结果带同样字段。新增 `job run --strict-timeout`（请求字段 `strict_timeout`）：超出上限直接 400 并写明上限与配置键，不再截断。

### 变更

- **顶栏「更多」下拉改为横排、信箱按钮宽屏带文字**：「更多 ▾」面板不再是一列竖排，改成一个分组一行（行首分组标题，后面链接横排），宽度随内容、最宽到视口两侧边距，放不下时链接自动换行；键盘操作（Enter / Space 打开、Esc 关闭、点外部关闭）、当前页高亮与「更多」在子页高亮均不变。信箱按钮宽屏显示「✉ 信箱」（与「随手记」文字的隐藏断点一致），窄屏只留信封图标，并且图标字号与相邻的「随手记」、铃铛按钮对齐（窄屏 14px，不再偏小）。
- **xfer 推送完成时回报执行机上的绝对路径**：执行机把文件放好之后会在结果里带上 `abs_path`（它自己盘上的真实路径），`gofer tool xfer show` 多一行 `abs_path`，`GET /v1/xfer/{id}` 也多这个字段（可选）。拉取、失败的传输，以及协议较旧的 worker（低于 `XferAbsPathMinProtocolVersion`，worker 协议 v25）不报它，那时只有项目内相对路径 `path`。
- **执行机自动清理附件目录**：往项目 `tmp/gofer-attachments/`（项目配了 `exchange_subdir` 时是它下面的 `gofer-attachments/`）放文件时，本机与 worker 两端都会删掉 7 天前的日期子目录（`YYYYMMDD`）；只删这一层、只在这个目录内（路径在项目根内校验），删除失败只记日志、不影响传输，手工放进去的其他名字不动。

## [0.176.0] - 2026-10-19

### 变更

- **控制台给仍在调整的功能加了「实验性」标注**：工作流页与「从模板新建」、信箱页、计划详情里的 leader 回合开关、设置页的决策 hook 旁都挂了一个小徽标，悬停提示「此功能仍在调整，行为与配置可能变化」；只做提示，不改行为。
- **`gofer init server` 生成的配置默认关闭 `/metrics`**：此前写的是 `metrics.enabled: true` 且无 token，server 监听 0.0.0.0 时局域网内任何人都能读到按 caller / project / agent / runner 打标签的 job 计数。现在生成 `enabled: false` 并在注释里写了怎么带 token 开启；整段不写 `metrics` 的老配置行为不变。`gofer config validate` 新增警告：`/metrics` 开启、没设 token、`server.addr` 又不是回环地址。
- **项目子仓库列表带 30 秒缓存**：`GET /v1/projects/{key}/repos` 要遍历整个项目目录，Projects 页和新建任务页每次打开都在等；现在按（项目 key + 执行路径）缓存 30 秒，配置重载或项目修改后失效，`?refresh=1` 强制重扫。分支 / 脏标记最多滞后 30 秒。
- **工作台线程列表支持 `?detail=summary`**：只返回列表用到的字段（去掉 `jobs` / `pending_interactions` / `cwd` 等，保留 `job_ids`），`attention` 不变；`full=<线程 id>`（可重复）让指定线程保持完整；不带参数的响应不变。工作台入口那一拉仍是完整响应（一次请求），之后的刷新用 summary + 当前打开的会话，打开新会话时补拉一次。真实数据下 93 KB 降到约 29 KB。
- **release 压缩包里的二进制统一叫 `gofer`**（Windows 为 `gofer.exe`），与 README 一致；原先是 `gofer-linux-amd64` 等带平台后缀的名字。一键更新同时认新旧两种包内布局，`latest.yaml` 与 mirror 用的裸二进制不变。
- **`gofer init server` 生成的配置不再带示例项目**：`demo` / `api`（还带 Windows 路径 `D:/projects/...`）改成注释里的示例，写完 `init` 后 `gofer project add demo ...` 与 `gofer config validate` 可直接通过。
- **`work.digest` 无订阅提示只在显式开启时出现**：`work.digest_enabled` 不写（默认开）时 `config validate` / `steward status` 不再提示，显式 `true` 或开了管家才提示。

### 文档

- **新增面向新用户的 `docs/guide/`，README 精简为上手入口**：`docs/guide/` 按主题分了概念、常见用法（7 个可照做的场景）、配置、安全须知、排障、稳定性与发版升级、贡献与开发；`README.md` / `README.zh-CN.md` 只留一句话定位、一张简图、安装（release 包与 `go install` 的区别）、10 分钟上手和文档索引。原 README 的功能总览、配置、开发命令搬进 guide，不再两面维护。

### 修复

- **官方 release 的版本号不再带 `-dirty`**：发布流程把 tag 作为 `VERSION` 传给 `make release`。
- **非绝对的项目路径被拒绝**：`host_path` / `container_path` 在运行 gofer 的系统上不是绝对路径（如 Linux 上的 `D:/projects/demo`）时，`config validate` 明确报错，job 也不再把它拼到当前目录下并在那里建目录。
- **`gofer serve -d` / `gofer worker -d` 不再谎报成功**：父进程会等后台进程就绪（serve 轮询 `/health`；worker 观察数秒），后台进程提前退出时报错并附日志末尾，且不留 pidfile。
- **被托管服务拉起的 server 不再把 `GOFER_MANAGED_SPEC` 泄给它跑的 job**：此前在 job 里再起一个用别的 `GOFER_CONFIG_DIR` 的 `gofer serve`，会误认为自己是托管服务并报 `managed server startup identity does not match its spec`（重启托管服务后生效）。
- **web 界面文字统一成中文**：导航、页面标题、表头、表单标签、筛选标签与状态显示改成中文（工作项 / 计划 / 任务 / 会话 / 统计 / 项目 / 工作流 / 定时；Issues、Agents、Runners 等专有名词保留英文），文案与状态映射（如 `open` → 进行中、`done` → 已完成、`archived` → 已归档、`blocked` → 受阻）集中在一处定义，命令文本、配置键与接口里的值不变；手机宽度（≤900px）下工作流列表与任务看板一样改成卡片式，设置页里的表格改为横向滚动。
- **手机版工作台更轻**：进入工作台只拉一次会话列表（此前进一次拉两次 `/v1/workbench/threads`，每次约 95 KB）；侧栏「已结束」分组默认折叠时不再渲染其中的行（此前只是 `v-show` 隐藏，上百条历史会话照旧建 DOM），展开才渲染；会话打开后，≤900 宽度的屏上主题 / 范围 / 中继状态说明收成一行「主题 · 范围 ▸」摘要（点开展开；唤醒 / 接管 / 从此会话新开这组操作不受影响，一直可见），输入框下方的说明收成一个「?」开关，把纵向空间留给消息；手机上新待办的提示条压成单行并在 5 秒后自动收起（宽屏不变：底部常驻、手动关闭），桌面布局一律不变。
- **web 传话开着（`on`）时，子 agent 跑完仍卡在 Stop**：从 web 发过消息后会话处于 `on`，子 agent 结束不放行，结果要等人在终端输入才被处理；现在最后一个子 agent 结束时无论 auto 还是 on 都放行，主 agent 处理完再继续等 web 回复。

## [0.175.0] - 2026-10-19

### 新增

- **agent 软预算（gofer-hnqf）**：job 的 `budget` 多两个字段 `agent_tokens` / `agent_cost_usd`（CLI `--agent-budget-tokens` / `--agent-budget-cost-usd`，用于 `job run|resume`、`plan add-todo|set-todo`，HTTP / MCP / 任务书 frontmatter 同名；`max_*` 仍是 gofer 杀 job 的硬上限）。它们交给 agent 自己执行——agent 用到这个量就自行收尾并汇报，job 正常结束——按 agent 配置新增的 `agent_budget` 映射（每维 argv 片段和/或环境变量名；内置 `suga` 已映射）渲染。没有映射的 agent 带软预算提交即 400（不静默忽略），resume 沿用源 job 的软预算，协议低于 v24 的 worker 拒收。agent 上报的预算快照（acp `usage_update._meta` 预算字段、stream-json 用量 / result 行的 `budget`）记在 `usage.agent_budget`；用尽时 job 的 `wrap_up` 标为 `budget`（预算耗尽收尾，区别于步数上限 / 时限 / 硬上限熔断）、记录事件 `job.agent_budget_exhausted`，`job show` 打 `agent_budget:` 行，web 任务详情的用量区显示「预算 已用 / 上限」与「预算耗尽收尾」徽标。

### 变更

- **web 顶栏导航瘦身**：顶栏一行只留最常用的 6 项（今天 · 工作台 · Works · Plans · Jobs · Sessions），其余收进「更多 ▾」下拉，下拉里保留分组标题（「工作」：Issues · Dashboard；「配置」：Agents · Runners · Projects · Workflows · Schedules）；当前页面在下拉里时「更多」显示为激活态，键盘可用（Enter / Space 打开，Esc 关闭，点击外部关闭），「设置」仍在主导航末尾。信箱不再占主导航，改为顶栏右侧「随手记」旁边的信封按钮（`/mail`），有待指派信件时带数字徽标（0 不显示，读信件对所有调用方都允许）；900px 以下改用抽屉，抽屉是完整的分组列表（含信箱）。1280px 宽度下一行放得下，去掉了导航的横向滚动兜底。

### 修复

- **Windows 交互（pty）job 结尾输出偶发丢失**：子进程打印后立即退出时，ConPTY 还在转发的输出会被关闭伪终端时丢掉；现由 gopty v0.1.2 在关闭前读完在途输出（gofer 去掉了自己的临时等待窗口）。
- **信箱唤醒偶尔被传话人拒绝**：一次性传话人把唤醒提醒当成「没有正文的信」拒绝转发，要等下一个唤醒间隔才送达；现在指令里写明提醒本身就是要转发的完整消息。

## [0.174.0] - 2026-10-18

### 新增

- **信箱 server 投递器**：server 内每 30 秒扫一次，补上 hook 够不着的收件方。空闲会话（`idle` / `offline` 且 `last_seen_at` 早于 5 分钟；急件在 `idle` 时立即）有待读信件时收到**一条**唤醒提示 `<gofer-mail-wake count="N"/>`（每会话 10 分钟最多一次，`waiting_reply` 不唤醒），经 codex 送话命令 / 传话人 / tmux 发出，但不是 web 消息：不走 `Say`、不写 outbox、不加 web 前缀，对它的回复被丢弃；信件仍在该轮开始时由 hook 注入。suga 会话的信件正文经 `suga send` 直送（返回 3 视为不在线、信件保持待读），ACP 会话 job 在 `awaiting_input` 时经 `job say` 一次送一封，pty session job 的信件立即转「待指派」；server 侧送达为至少一次（占用 → 送出 → 确认，标签带信件 id）。超过送达期限（默认 24 小时）的信件转「待指派」进收集箱。新增 server 配置块 `mail:`（`tick_sec` / `wake_idle_sec` / `wake_interval_sec` / `deliver_by_sec` / `claim_lease_sec`），热重载生效。

- **web 信箱页与会话抽屉的信件入口**：新增 `/mail`（导航「工作 → 信箱」）——左栏对话串（按最近活动倒序，显示对方、状态、急件标记与未读条数，页签 全部 / 收件 / 发件 / 待指派），右栏串内按时间显示每封信（出处 → 收件地址、状态、纯文本正文）与底部回复框，顶部「写信」支持 `session:` / `job:` / `topic:` / `path:` / `caller` 地址与急件开关；待指派信件写明原因，可「改投」（改地址重发）或「撤回」。会话抽屉多一行「信件」，显示该会话的往来封数并跳到 `/mail?session=<id>`；Today 新增「信件 N 封待指派」汇总卡（`kind: mail_held`，点「去指派」跳 `/mail?state=held`），从 Today 的卡或 `?state=held` 也能直接打开待指派页签。

- **信件的秘密扫描、脱敏与保存期（信箱第二期 M8）**：`gofer job secret-scan` 的报告多一段 `mail`（信件 id、对话串、收发双方的显示名、状态、项目、命中位置与计数，不回显正文），普通 caller 只看自己发或自己会话 / job 收到的信；新增 `gofer session mail redact <id> --literal-from-stdin`（HTTP `POST /v1/mail/{id}/redact`，权限与 `job redact` 一致：信件收发双方或管理员），按信件 id 抹掉正文与持久化摘要里的秘密，并同步改写收集箱条目里引用该信件的摘要（条目文本的正文摘录、条目日志的回复摘要），保留信件 id 引用；`job secret-scan --redact --yes` 连同命中的信件一起脱敏，`job redact <job>` 也会处理带该 `job_id` 的信件。新增配置 `storage.mail_retention_days`（默认 90 天，负数 = 不清理）：serve 的清理循环删除超过保存期的终态信件（delivered / answered / expired / failed / cancelled），待读 / 投递中与待指派（held）的信保留。

## [0.173.1] - 2026-10-18

### 修复

- **急件在一轮中的注入实际不生效**：PreToolUse 走的快速路径没有接上 server，急件只能等到 Stop 才送达；现已在下一个 shell 命令前注入（每会话约 30 秒检查一次）。

## [0.173.0] - 2026-10-18

- **gofer 信箱：发信 / 读信 / 回信（`gofer session mail`）**：会话、session job 与调用方之间可以互相发信——`gofer session mail send|ls|read|reply|cancel`（MCP 同名工具 `gofer_session_mail_*`，HTTP `/v1/mail*`）。地址写法 `session:<sid>` / `job:<id>` / `topic:<关键词>` / `path:<子路径>` / `caller`，只有唯一命中才投递，歧义与不可投递的信转「待指派」进收集箱并列出候选；出处如实标注（会话 / 调用方 / job），信件不是用户输入；限额单封 32 KB、每发件方每分钟 10 封（429）；权限沿用会话权限表（他人会话 `session.reply.others`，他人会话 job `job.manage.others`，job 凭据只能给来源会话、plan 主会话和 caller 发信），受限调用方（`ceiling`）只能给范围内项目收发信、范围外按不存在（404）。信件由收件会话的 hook 取走并注入（先向服务端确认再注入，至多一次）：普通件在下一轮开始（SessionStart / UserPromptSubmit）与 job 通知、记忆合并进同一份附加上下文，单次最多 5 封 / 8000 字，超出留待下一轮并提示「还有 N 封」；急件在轮内的下一个 PreToolUse 注入；人离开且已有待信时在 Stop 阻塞送出（独立于 `stop_job_wait`，与 web 回复竞争时服务端原子关 turn 并取信，只有一方成功）。新增 `POST /v1/sessions/{sid}/mail/claim|confirm`、`/turns/{id}/complete-mail`，会话对象带 `mail_pending`。空闲会话唤醒与 web 信箱页在后续版本。收集箱条目的「交给会话」也改成了发信：分派 = 发一封出处为分派者（带会话头就是那个会话）的信，条目上记信件 id，信件送达 / 被回复时把「已送达」与回复前 200 字摘要写进条目日志（旧的「经 web 发消息」分派路径已移除）。

## [0.172.0] - 2026-10-18

### 新增

- **调用方上限（`server.callers[].ceiling`）**：给一个 token（一个调用方）划定它可访问的项目、可提交的 runner 与 worker、可用的 agent，以及是否允许 exec、是否允许写全局记忆、是否只读。上限是「只减不加」的一层：调用方的实际范围 = 它已有的权限 ∩ 上限，不写 `ceiling` 的调用方行为完全不变。写上限时**本机 runner（`local` / `server`）与 exec 默认禁止**，要用必须显式写 `runners: [local]` / `allow_exec: true`（本机 runner 等于主机 shell，显式放开时启动日志会警告：那种情况下上限不构成安全边界）；`projects` / `workers` / `agents` 为空 = 该维度不限制，`runners` 为空 = 只允许 worker runner。`ceiling` 子树按已知键**严格解码**（键名拼错、`ceiling: {}` 都直接加载失败），给根管理员（`can_admin: true`）写上限也会被拒。只能改配置文件；给已在配置里的调用方改上限，`serve reload` 后判定即用新值（新增调用方要重启 serve）；`GET /v1/auth/whoami` 的 `ceiling` 返回本 token 的上限（`gofer auth whoami` 不打印这一字段），serve 启动与 reload 打印一行 `caller ceilings: N restricted caller(s): …`。**提交时按上限检查**：任何提交（HTTP、job 凭据的子 job、评论派发、定时与 webhook、plan / todo 派发、续投、重试、失败转移、hold 审批后重投、内部作业）都在 job 层唯一入口按「上限所有者」（发起它的那个调用方，随派生链继承并落库）检查项目 / runner / worker / agent / exec，被拒返回 403 且重投路径终止（不再重试）；解析不到上限所有者的派生链对受限调用方失败关闭。**路由判定**：受限调用方（用户类 token）访问项目范围外的资源（路径 / 查询里的项目、任一资源类型的记录、请求体里的项目）一律返回与「不存在」同形的 404（写操作也是 404），范围内但缺权限仍是 403；`read_only` 的调用方对所有写操作返回 403（个人偏好类 `ReadSafe` 例外）；列表按上限过滤；不属于任何项目的资源与项目无关的服务器级写操作（配置、管家、隧道等）对受限调用方不可用；**已知限制**：聚合与计数（today、stats、digest）、`/v1/ws` 的推送主题过滤、上限收紧后已打开的长连接（attach / 日志流 / 隧道 / 托管转发）的断开、以及审阅权限的细分（受限调用方仍能验收 / 拒绝 / 批准自己作为上限所有者的 job）尚未实现 —— 这些路径仍可能向受限调用方暴露范围外的计数或事件。说明见 skill `gofer-usage` 的 server-config。

## [0.171.0] - 2026-10-18

### 新增

- **会话授权可以续期，不用重新授权**：终端会话的授权快到期时，`gofer auth grant renew <grant-id> --for 4h`（或 `POST /v1/auth/grants/{id}/renew`，体 `{"expires_sec":14400}`，1 ~ 86400 秒）把**同一条**授权的到期时间从「现在」往后挪，最长 24 小时。只改到期时间：模式、展开结果、授权人与绑定的会话都不变，续期不会把授权放宽或挪到别的会话。只有仍然生效的授权能续期（未撤销、未过期、会话未结束、owner 与化身与登记时一致），已撤销 / 已过期 / 会话已结束的返回 409 并提示重新授权。续期写审计（`grant.renew`，含旧 / 新到期时间），接口需根管理员的 `auth.manage`。web 上也能续：设置 → 权限的会话授权行（每行带剩余时间）与会话抽屉的「已授权」条（剩余不足 1 小时时）都有「续期」，选 1 / 4 / 24 小时即可，不用重新授权。

### 修复

- **续接作业运行中的「改动」视图只显示本项的改动**：接着上一项会话继续做的 job 在运行期间，web 会话的「改动」从分支基线算起，把前序各项的改动也一并算给了它；现在从该 job 自己的基线（前序结束点）起算，与它的提交列表、验收 diff 和改动范围一致。普通 job 与会话首轮的视图不变。
- **`--notify` 不再按 job 的项目找当前会话；hold 默认通知被拒时降级**：之前显式 `--notify`（不带 `--notify-session`）用 job 的项目过滤当前 agent 会话，会话属于别的项目就识别不到而报错；现在会话只当通知地址，不按项目过滤，与 hold 默认通知一致。另外，`--hold` 的隐式默认通知若因通知目标不可用被服务端拒绝（如会话属于另一个调用方），CLI 不再让整条提交失败，而是去掉通知、打印一行警告后再提交一次；显式 `--notify` / `--notify-session` 的错误照旧，其它错误不重试。服务端对这类拒绝在错误体里带稳定的 `code: notify_target_rejected`。
- **带续接的退回现在也要求提交 job 的权限**（gofer-dc9l）：之前 `job reject --resume`（`POST /v1/jobs/{id}/reject` 且 `resume=true`）只检查验收权限 `job.review`，没有 `job.submit` 的调用方也能借它新建 job；现在续接需要两项权限，缺 `job.submit` 返回 403 且不记录退回。另外，会话 job 的 say / end 在无权限时只写一次 403 响应，不再在响应体后追加第二段。
- **后台子 agent 跑完时，只等 job 的 Stop 立即放行**（gofer-7lac）：中继关闭、会话还有在途 job 关注时，Stop 只等 job 事件；若进入这个等待时会话里有后台子 agent 在跑，主 agent 的「子 agent 已完成」那一轮也被一起关着，子 agent 的结果要等某个 job 结束或 `--wait` 用尽才送得进来。现在这种等待在最后一次 `SubagentStop`（会话子 agent 计数归零）时立即放行（日志记 `sub-agent(s) finished, released …`）；同一轮里若有 job 结束，仍优先投递它的完成通知（两种情况都不会被关住）。服务端 `GET /v1/sessions/{sid}/watches` 响应新增 `subagent_count`，hook 由此读到子 agent 数；进入等待时没有子 agent 在跑的行为不变。

## [0.170.0] - 2026-10-18

### 新增

- **计划任务项可接着上一项的会话做**（gofer-rnp3）：串行、改同一块代码的相关 todo 可声明 `continue_from`，派发时直接在前序项的 agent 会话里继续，省掉重新读代码和重复注入规则的开销。CLI `plan add-todo --continue prev|<todo-id>`、`plan set-todo --continue <todo-id>` / `--no-continue`，HTTP `POST /v1/plans/{id}/todos` / `PATCH /v1/todos/{id}` 的 `continue_from`，MCP `gofer_add_todo` / `gofer_update_todo`，web 任务项表单的「接着上一项的会话做」开关；todo 视图带 `continue_from` / `continued_by` / `continue_note`。条件：前序已完成并捕获到会话、同会话族 agent、同一本机 runner、同工作目录、worktree 仍在、无自动交付、只读设置一致、前序未被其他项接续、连续接续次数未达项目配置 `continue_max_chain`（默认 3，0 = 不限，最大 10）；不满足时照常开新会话，并在 todo 上写「未接续：原因」、记 `job.continue_fallback`。续接 job 只带本项的任务书、验收、交付约定和时限，提交列表 / diff / 改动范围从前序项的结束点算起；`job.continued` 事件记录接续链。说明见 skill `gofer-usage` 的 plans-workflows。
- **plan 用量含缓存 token**：`GET /v1/plans/{id}` 的 `usage` 与每个 agent 的分项新增 `cache_read_tokens` / `cache_write_tokens`，按真实 agent 归类（cli 续接载体算在被续接的 agent 名下）；`plan show` 与 web 同步显示。

### 修复

- **会话里提交的待批 job 批准后不再要等用户再发消息才被处理**（gofer-fxbu）：之前 `job run --hold` 提交的 job（常见于后台 shell，且项目 / runner 可能和会话不同）与提交它的会话没有任何关联，用户在 web 批准、job 跑完后，停在 Stop hook 的会话仍不动，直到用户再回复一条。现在：`job run --hold` 在 agent 会话环境里运行时默认通知该会话（新增 `--no-notify` 可关；普通 job 仍需显式 `--notify`）；待批 job 一入库就被该会话关注；Stop 等待每次轮询重读关注列表，不再用 Stop 开始时的快照；auto 模式下批准的 job 开始运行使自动等待关闭时，会话仍等待该 job 的结束通知，不再静默退出；Stop 时认领来源会话 job 也包含待批与排队中的；hook 只在输出里出现真正的 job id 时才登记关注，「the job was submitted」之类的话不再被当成名为 `was` 的 job。
- **待批准（`--hold`）的 job 不再沉到「待决策」队列最底部，并会在顶栏弹出提示**：待批准的卡带「等待批准才能开工」的阻塞分，排在只有建议性动作的 0 分卡之前（其他卡的相对顺序不变）；job 一进待批准，顶栏「待我决策」就弹一条提示（带标题与要批的命令），点它进该 job 的批准面板，批准 / 拒绝 / 过期后提示与该卡一起消失。

## [0.169.0] - 2026-10-17

### 新增

- **worker 自助注册**（默认关闭，`server.worker_enrollment.enabled`）：worker 机器执行 `gofer worker join --server <addr>` 发起注册申请，屏幕显示配对码；管理员在 web Runners 页「待接受」区输入配对码并选择 worker id / 项目 / 标签后接受，join 自动领取 token、写好 `worker.yaml` 与 `.env` 并自检（`--start` 可后台启动）。申请有决定 / 领取窗口、来源限流与屏蔽，拒绝与取代都有明确提示；Today 与 Runners 页实时提示待处理申请。配置段 `server.worker_enrollment`（`decide_sec` / `collect_sec` / `per_source_per_min` / `max_pending` / `trusted_proxies`）可热加载，说明见 `docs/runbook/2026-09-26-worker-init-runbook.md`。
- **job 里的 agent 知道自己的时限**：有时限的 job 注入环境变量 `GOFER_JOB_TIMEOUT_SEC`（生效秒数，已按上限截断）与 `GOFER_JOB_DEADLINE`（RFC3339，带时区）；一次性的 cli / acp agent job（exec 不加）的 prompt 末尾还会多一行「本 job 时限 N 分钟，截止 HH:MM（时区）」并提示「接近时限时先提交已完成的部分」——之前 agent 无从知道真实时限，会把自己的收尾误报成超时。续接 job 按它自己生效的时限算（继承源 job 的超时），持续会话（`--session`）带 `max_session` 时按它算、否则按每一轮的 timeout；完全没有时限的 job（如没显式给 `--timeout` 的交互 pty job）不注入（gofer-s2er）。

## [0.168.0] - 2026-10-17

### 新增

- **授权视图带 `effective`**：`GET /v1/auth/grants` 每行加 `effective`（此刻是否生效）；会话授权按设计的全部生效条件（状态、有效期、会话未结束、owner 一致、会话化身一致）由服务端用与鉴权同一份判定得出，会话结束后复活的授权不再显示为生效。web 的「已授权」徽标与设置 → 权限的会话授权列表改用它（gofer-x632）。

### 修复

- **额度 / 用量上限类错误不再被当成临时错误自动续接**：没有 `error_kind` 的事件走文本兜底时，含 usage limit / quota / insufficient_quota / limit resets at / weekly usage limit / HTTP 402 的错误一律不算瞬时（即使同时写着 rate limit / 429），不再白白派发一次必然失败的续接（gofer-3jap）。
- **web「发消息」的一次性传话人 job 超时不再写死失败**：传话人 job 超出 `session_messaging.timeout` 时，已开始 → 消息记「送达中」，job 结束时回写真实结果（需有 `已发送` 回执才算送达）；尚未开始 → 撤销该 job 并记「未送达」（可安全重试）。与会话送达 / 注入的口径一致（gofer-kx53）。

### 变更

- **Web 主色调亮**：强调色（链接、主按钮、焦点环、选中与拖放高亮）深色主题由 `#4FB0C6` 改为 `#2BD9F5`，浅色主题由 `#0F7184` 改为 `#0369A1`，对比度均不低于 4.5:1；原来写死旧色值的高亮改为由同一变量派生。
- **按 agent 自带的 `error_kind` 判定临时错误**：suga 的 stream-json `type=error` 行（cli）与 ACP prompt 错误响应的 `data`（suga-acp）带 `error_kind` 时以它为准——只有 `provider_transient` 自动续接；`provider_quota`、`provider_rejected`、`provider_bad_response` 与未知取值都不续接；额度类错误带 `reset_at` 时写进 job 错误。无 `error_kind` 的事件仍走原文本模式兜底。续接因目录与会话原目录不一致被 suga 拒绝时，suga 的提示原样写进 job 错误（gofer 不会自动加 `--allow-cwd-change`）（gofer-3jap）。

## [0.167.0] - 2026-10-17

### 新增

- **调用方显示名 `callers[].name`**：`server.callers` 每项可选加 `name`（如给主 token 的调用方 `default` 写 `name: admin`），只改显示：id 仍是 job / 授权 / 审计里记的身份，无数据迁移，不影响任何权限判定。加载时去空白，非空时全局唯一（忽略大小写）、至多 64 字符、不含控制字符。`gofer auth whoami` 打印 `name`；`GET /v1/auth/whoami` 加 `caller_name` 与 `caller_names`（id → 名称），`GET /v1/auth/grants` 加 `caller_names`，`GET /v1/config` 的 callers 行带 `name`；web 设置 → 权限显示名称、悬停见 id，配置页 callers 表加 name 列（gofer-6n3h）。

### 修复

- **`gofer init skill|hooks -a all` 不再为没装的 agent 建目录**：`-a all` 只对配置目录已存在的 agent 写入（hooks：`.claude` / `.codex` / omp / jcode 的配置目录；skill：`.claude` / `.agents`），其余输出「跳过：未发现 <dir>」；点名某个 agent（如 `-a claude`）或不带 `-a` 的行为不变，照常创建目录（gofer-vlvb）。
- **未知 `/v1` 路径返回 404 JSON**：`/v1/` 下没有匹配路由的请求（如已删除的 `/v1/agents/presence`、拼错的路径，GET / POST / HEAD 皆然）不再回退到 web 首页（200 HTML），改为 404 + `{error, detail}`；非 `/v1` 的前端路由仍回退首页（gofer-enho）。
- **Web：exec / 一次性 cli-agent job 详情不再显示「消息」区**：该区只对 ACP 持续会话 job（或确有多轮会话消息的 job）显示并请求 `include=messages`，避免与上方命令渲染重复（gofer-h7hy）。
- **`plan add-todo --after prev` 空 plan**：plan 还没有任何项时，`--after prev` 视为无依赖并成功添加（输出注明），不再报 `has no items yet`；非空 plan 行为不变（gofer-hm1p）。

## [0.166.0] - 2026-10-17

### 新增

- **会话主题与查找**：给会话记下「在做什么」，需要时按主题或路径找到该问谁。
  - CLI / MCP / hook：`gofer session topic "<主题>" [--scope <子路径>]… [--clear-scope] [--session <id>]` 记下当前会话在做什么，`gofer session find <关键词|路径>` 找谁在做；MCP 新增 `gofer_session_set_topic` 与 `gofer_session_find`。当前会话只按环境变量识别（不按目录猜），识别不到或不唯一时要求 `--session`。SessionStart hook 在会话还没有自己的主题时，往 additionalContext 加一行提示 agent 记主题。
  - HTTP：`PATCH /v1/sessions/{sid}`（主题与范围，空串 `topic` 清除）、`GET /v1/sessions?q=&path=`（查找，返回命中原因与「可能不在线」）；自己的会话不需额外权限，改他人的会话需 `session.manage.others`。
  - Web：Sessions 卡片与会话抽屉显示主题与范围，推断出来的主题灰显并标「推断」，自述 / 人工显示来源小标签；可就地编辑或「清除主题」（已结束的会话不能改并说明原因，范围按项目根相对路径逐行填，提交前先校验）。Sessions 页新增查找框：含 `/` 的无空格输入按路径，其余按关键词，结果按相关度排序并显示命中原因与「可能不在线」。
- **收集箱：随手记下想法，之后再整理**。
  - 记录：`gofer work capture "<内容>" [--kind idea|issue|request|note] [--project <p>]`（kind 缺省 `note`）；MCP 新增 `gofer_work_capture`（job 凭据下记录来源 job，来源会话按环境识别）；Web 顶栏「随手记」（想法 / 问题 / 需求 / 记录 + 可选项目，手机贴底面板），记下后提示条带「去收集箱」链接。HTTP：`POST /v1/work-items/capture`。
  - 整理：Works 的「待整理」区分「草稿」「收集箱」两个页签（`/work?tab=capture` 直接打开），每条显示类型、来源会话 / job 与记录时间；Today 出一张「收集箱 N 条待处理」汇总卡，「去整理」跳到收集箱。收集箱条目不出现在看板、分组、日报、整理扫描、健康检查、到期提醒、合并建议与 Today 泳道里。
  - 分派：条目抽屉的「分派…」对话框（Web）与 `gofer work dispatch <id> --to session|job|todo|issue|archive|drop`（CLI）；`session` 发给会话（失败则条目留在收集箱并给出原因），`job` 关联已提交的 job（结束时终态写进条目日志；Web 新建 job 页 `?from_item=<id>` 预填并在提交后自动关联，可重试），`todo` 沿用 `to-todo`，`issue` 在仓库镜像里新建 issue，`archive` / `drop` 直接结案。HTTP：`POST /v1/work-items/{id}/dispatch`；他人的会话 / job / plan 分别需 `session.reply.others` / `job.read.others` / `plan.manage.others`，缺 `job.submit` 时 Web 不显示新建 job。
  - 目标推荐：`GET /v1/work-items/{id}/dispatch-targets` 按条目文本与会话主题、范围、会话名、项目推荐最多 3 个候选会话并给出命中原因（推断的主题单独标记；他人的会话按 `session.read.others` 先过滤）；Web 分派对话框直接展示。
  - 保护：`captured` 条目不会被汇报、整理器的状态建议、管家或 job 凭据改动状态；人用 `PATCH /v1/work-items/{id}` 把状态改出 `captured` 视同归档并写日志。
- **server 新建 tracker issue**：`POST /v1/tracker/repos/{tracker_id}/issues`（`{title, description?, priority?, labels?, type?}`）在 server 镜像里新建 issue，id 按仓库前缀分配，仓库还没有前缀时 409；issue 在仓库拉取前带「待同步」标记（`server_created`，work 关联里是 `pending_sync`），下次 `gofer repo sync` 进本地。Web 的 Issues 页新增「新建 issue」表单（只列有 id 前缀的仓库），收集箱「转 issue」用它建（kind 为 `issue` 记 `bug`，其余记 `task`，关联条目并改为 `parked`）。
- **`gofer work park --captured`**：回滚辅助，把收集箱里全部条目转为 `parked`（各写一条「从收集箱归档」日志），与 `<id>` 二选一；不删数据。

### 变更

- **`POST /v1/jobs/{id}/findings/issue` 改走 server 新建路径**：建出的 issue 同样带「待同步」标记，并参与下面的 id 冲突改名。
- 权限参考文档（`docs/reference/permissions.md`）登记了新增的路由：会话改主题、收集箱的记录 / 推荐 / 分派、server 新建 issue。

### 修复

- **仓库推送同 id 的新 issue 时不再逐字段静默合并**：仓库在拉到 server 新建的 issue 之前，推送了同 id 的新 issue 时，server 给自己的那条换新 id（同时改写镜像内的父子 / 依赖引用和工作项关联，issue 备注与工作项日志留一笔），两条都保留。已知例外：仓库首次同步（无 `sync-revs.json`）。

## [0.165.0] - 2026-10-17

### 新增

- **web「设置 → 权限」**：根管理员可在网页上管理授权，不用再手写接口。四个页签：授权（按 `users` / 调用方分组，标出来自配置、孤儿，以及「有新权限待确认」，孤儿可「确认恢复」）、会话授权（所有者、有效期、状态，可撤销）、权限目录（风险、可否授予会话、范围、来源）、审计（按对象 / 权限 / 授权 id 过滤，最新在前）。新增授权可选常用预设或直接写模式，实时显示展开结果；高风险权限要确认，危险级必须写出完整权限名并填写原因。普通账号和没有根管理员的部署只能查看目录与自己的权限，页面会说明原因。
- **Sessions 页给会话授权**：会话卡片与详情抽屉新增「授权」（仅根管理员）：只列可授予会话的权限，有效期 1 / 4 / 24 小时，弹窗里写明「谁能在这个终端会话里运行命令，谁就拿到这些权限」以及授权随会话结束失效；已授权的会话显示「已授权」，抽屉里可撤销。
- **控制台按权限隐藏操作**：升级 worker、添加 / 重载 worker、一键更新、写配置、项目增改删、删除他人的 job 记录、托管转发与预设、停他人的转发，没有对应权限就不再显示。
- **`GET /v1/auth/expand` 预览授权模式的展开结果**（需 `auth.manage`）：返回某个对象（`users` / `caller:<id>` / `session:<sid>`）实际会得到的权限，以及被规则挡下的权限和原因；网页预览改用它，展开规则只在服务端一份。
- **权限不足的提示指向「设置 → 权限」**：网页里所有因缺权限而被拒的操作，现在显示缺少的权限名和去哪里处理。
- **plan 详情页可开关结束通知（gofer-3db4）**：链控制区新增「结束通知」开关，对应 `PATCH /v1/plans/{id}` 的 `notify`；没绑监督会话时会提示不会通知任何人。

### 移除

- **移除 presence / driver 与 supervisor 自动作答**：不再有 agent 在线登记与收件箱（MCP 工具 `gofer_register`、`gofer_poll_inbox`、`gofer_post_message`、`gofer_list_presence`，命令 `gofer presence`，`/v1/agents/presence` 与收件箱相关接口，web 的 Drivers 收件箱页与 Agents 页的在线 driver 列表）；不再有 supervisor 规则作答、升级路由、AI supervisor job 与其对账进程，以及作答分级守卫（answerguard）。同时去掉 job 的 `origin_agent` / `escalate_to` 字段、交互作答请求的 `responder` 字段和统计里的 `drivers`。
- 待答的交互仍由人在「今日」页与推送里作答，顶栏的决策入口与 plan 的监督会话不受影响。
- **旧配置段被忽略并给出警告**：配置里残留的 `supervisor:`、`presence:` 段不再生效，会在 `serve` 日志、`gofer config validate` 和配置接口（`GET /v1/config` 与写接口响应的 `warnings`）里提示；配置页顶部也会显示。该兼容处理计划在 v0.168.0 移除。
- **plan leader 配置移到顶层 `leader:`**：字段不变（`enabled`、`agent`、`scopes`、`on_member_done`、`wake_delay_sec`、`max_rounds_per_scope`），原来写在 `supervisor:` 下的 leader 块不再被读取。已启用 leader 回合的部署升级前需把该块移到顶层，否则 leader 回合会关闭；`GET /v1/config` 与配置页也改为显示顶层 `leader`。

## [0.164.1] - 2026-10-16

### 修复

- **升级等待空闲时，阶段公开前就暂停新 job（gofer-tzwo）**：此前「等待空闲」阶段先对外可见、过一小会儿才开始暂停新 job，其间提交的 job 会照常启动而不是被停住；现在先暂停、再公开阶段。v0.164.0 的 CI 因此失败，该版本不要使用，请直接升级到 v0.164.1。

## [0.164.0] - 2026-10-16

### 新增

- **权限审计与使用计数（gofer-kd8q）**：授权新增 / 撤销 / 恢复分别记录为 `grant.create`、`grant.revoke`、`grant.activate`；只靠会话授权通过的请求记录为 `request.session_grant`，被拒请求记录为 `request.denied`。会话授权行同步维护 `last_used_at` 与 `use_count`；审计记录按主体、权限、授权 id 过滤，并保留 90 天。

## [0.163.0] - 2026-10-16

### 修复

- **送达 job 超出等待时间不再被误记为失败（gofer-qzv5）**：web 转达 / 注入的内部 job 若在服务端等待窗口（25s）内没跑完，现在按两种情况处理：job **已开始** → 消息记「送达中」，job 终态时回写真实结果（job 结束钩子写，读 outbox 时也会补一次以覆盖重启丢失的写回）；job **尚未开始**（典型：更新等待空闲把新 job 停住） → 撤销该 job，消息记「未送达」并写明原因和 job 状态，可以安全重试。此前两种都报 `deliver_failed:exit code -1: job …`，而 job 后来真的送达了也不回写，web 一直显示失败、点「重试」会重复送达。
- **outbox 多两个状态，重试范围收紧**：新增 `delivering`（送达中）与 `undelivered`（未送达，确定没送出去）；web 只对 `undelivered` 开「重试」——`failed` 是「投递试过了、结果不明」（送话命令非 0 退出也可能已经送达），再发可能重复送达。失败原因里现在带上那只 job 的状态与退出码（如 `job … 状态 failed exit=5：…`），不再只是一个 `exit code -1`。
- **送达 / 注入 / 传话人 job 不受「更新等待空闲」停住**：这些是 gofer 自己的短内部 job（tag `relay-deliver` / `relay-inject` / `session-messenger`），停住它们只会丢掉要送的消息，而重启不差这几秒；人 / agent 提交的 job 照旧停住。

### 新增

- **会话授权真的生效了（gofer-kd8q）**：给一个终端会话（主体 `session:<sid>`）发的授权现在会作用到那个会话的请求上——容器里的会话要开隧道、监督会话要发版升级，都可以只给它自己发，不用把权限给整个 token。一条会话授权生效要同时满足：请求带了该会话 id、发起人是会话当前的 owner、会话化身没变、会话没结束、授权没撤销也没过期。有效期 1 / 4 / 24 小时（「到会话结束」也带上限），接口见 [`docs/reference/http-api.md`](docs/reference/http-api.md)。查自己：`gofer auth whoami` 现在打印请求附带的会话 id 与**来源变量**，以及靠会话授权拿到的权限（来源 `db`，并给出是哪条授权）。
- **会话化身：旧授权不会因为会话「复活」而回来（gofer-kd8q）**：会话每次「离开 ended」（重新登记、心跳复活、接管一个已结束的终端）都换一个新化身，删除后重新登记也一定是新的；会话授权绑的是授权那一刻的化身。读时还判 `ended_at` 为空，所以「结束 → 接管 → 释放」也带不回旧授权。

### 变更

- **撤销授权会断开靠它的长连接（gofer-kd8q）**：长连接（隧道、attach 终端、SSE 日志流、`/v1/ws` 推送、托管转发）在建连时按「调用方 id + 依赖的授权」登记。授权的撤销或到期、会话化身变化或会话结束、以及配置重载后调用方被删 / token 变了 / 根身份变了 / 推出的授权变了，都会让这些连接**重新判定**：不再持有所需权限的断开（撤销和重载当场断，到期与会话结束由周期重判接住，最长 10 秒），仍然有权限的——比如同一项权限另有来源（配置推出的授权、另一条授权、自己的资源）——照旧保留。以前这些连接只在建连时校验一次，撤了授权也一直开着。
- **`/v1/ws` 推送按读权限过滤主题**：`job:<id>` 需要自己的 job 或 `job.read.others`（`jobs` 主题同）、`sessions` 需要 `session.read.others`；没权限的 topic 在 `error` 帧里报回来、不订阅。这两个权限默认种入给全体用户类调用方，所以默认部署行为不变。
- **CLI 每个请求都带会话头（gofer-kd8q）**：`X-Gofer-Session-Id` 来自环境——显式设置的 `$GOFER_SESSION_ID` 优先，否则 `$CLAUDE_CODE_SESSION_ID` / `$CODEX_THREAD_ID` / `$CODEX_SESSION_ID` 去重后**恰好一个**才带上；没有、或两个取值不一致就不带（宁可不带，也不认错会话）。注入点只有一个，普通请求、job 提交、SSE 流、文件传输与隧道 websocket 握手全走它。不看当前目录，不读标记文件。注意：显式 `export GOFER_SESSION_ID=…` 会被后代进程继承，`gofer tunnel forward -d` 起的守护进程也算。
- **`GOFER_SESSION_ID` / `CODEX_THREAD_ID` / `CODEX_SESSION_ID` 加入 job 环境禁止清单**：job 不继承启动它的终端那个会话 id，job 里的 `gofer` 不认成那个会话（否则 job 里跑的每条命令都能拿到那个终端的会话授权）。
- **会话归属转移会撤销该会话的授权（gofer-kd8q）**：`gofer session owner <sid> <caller>` / `POST /v1/sessions/{sid}/owner` 在改 owner 的同一个事务里把该会话名下的授权标为已撤销——在此之前它们虽然已经因为 owner 变了而失效，行上却还看不出原因。

## [0.162.0] - 2026-10-16

### 新增

- **job 详情页的「消息」区（gofer-chd9）**：详情页现在直接列出**发给 agent 的消息**——首条是发起 job 的 prompt（自动剥掉 gofer 注入的 rules 段，点「查看完整注入内容」可展开全文），后面是持续会话里各轮 `session say` 的消息，每条用户消息后面跟着 agent 的回复摘录，按时间升序；非会话 job 也至少能看到首条 prompt。数据来自 job 已存下的 request prompt 与 job 的 ACP 结构化记录（`artifacts/acp.jsonl`），不用再跑一趟工作台。接口是详情聚合的新选项 `GET /v1/jobs/{id}?include=messages`（权限同 job 读，即 `job.read.others`），字段见 [`docs/reference/http-api.md`](docs/reference/http-api.md)。
- **授权落库，可以按主体发（gofer-kd8q）**：除权限目录外，server 现在有一张**授权**表：一条授权 = 主体（全体用户 / 某个调用方 `caller:<id>` / 某个终端会话 `session:<sid>`）+ 名称模式（`tunnel.*`、`job.*.others`、`*`），存在数据库里、立即生效。首次启用时那一组「原本不检查」的权限照旧发给全体用户类调用方，所以**升级后行为不变**；新增的是「单独发给某个调用方」的能力（此前只能整 token 给能力位）。接口：`POST /v1/auth/grants` 新增、`DELETE /v1/auth/grants/{id}` 撤销（行留着，撤销后重启不还原）、`POST /v1/auth/grants/{id}/activate` 恢复孤儿、`GET /v1/auth/grants` 列出（含配置推出的只读项与孤儿）、`GET /v1/auth/audit` 按主体 / 权限 / 授权 id 过滤（审计的写入在后续）。只有**根管理员**能管理授权：`auth.manage` 是任何模式都匹配不到的保留权限，worker 与 job 凭据一律 403。
- **`gofer auth whoami` / `gofer auth routes`（gofer-kd8q）**：`whoami` 显示当前调用方、是否根管理员、持有的每项权限与来源（`root` / `config` / `db`，数据库授权还给出是哪一条给的）、请求附带的会话 id，以及没有根管理员时的提示——权限不够时的 403 正文里让人跑的就是它；`routes` 列出运行时每条路由的声明与需要的权限（与 `docs/reference/permissions.md` 同一份数据）。
- 只读查询接口：`GET /v1/auth/whoami`、`GET /v1/auth/catalog`（权限目录 + 预设及其展开）、`GET /v1/auth/routes`，任何用户类调用方都可以调（看不到自己有什么，就只能盲猜该找谁要）。
- **新建 job / 新会话 / 续接都能设目录锁与 worktree（gofer-jkd2）**：Web 新建 job 表单新增「锁定目录」（可多个，项目根相对，留空 = 锁整个 CWD）、「等锁时长」、「worktree」三项，与 CLI 的 `job run --lock` / `--lock-wait` / `--worktree` 一一对应；「快速重建」预填这三项并只在改过时回传。「工作台」与 Sessions 页的新会话表单也新增「工作目录」与「锁定目录」——会话落在项目根会把整个项目根锁住，别的子目录 job 只能排队等锁。`job resume` 新增 `--lock`（HTTP `POST /v1/jobs/{id}/resume` 的 `lock_paths`）：续接默认照抄源 job 的锁，现在可以缩小或改锁（声明的锁会同时要求独占）；会话的 cwd 不变——ACP 的 `session/load` 必须在同一 cwd 加载会话。字段见 [`docs/reference/http-api.md`](docs/reference/http-api.md)。

### 变更

- **孤儿授权**：某个调用方从 `server.callers` 里删掉后，发给它的授权标为孤儿并停用；同一 id 以后重新出现也**仍保持停用**（防止换了人的同名 token 继承旧授权），要根管理员调 `activate` 才恢复。会话主体的授权可以登记（记下会话的 owner 与化身，有效期不超过 24 小时），生效条件随会话授权一并落地。
- **授权缓存按版本号 + 5 秒 TTL 失效**：本进程写入（新增 / 撤销 / 恢复）立即生效，其他进程的写入 5 秒内生效。
- **保存授权时记展开快照**：授权列表给出该模式**现在**展开出的权限与保存时的快照；目录以后新增了该模式能匹配到的权限时，用 `new_permissions` 报出来（「有新权限待确认」，不会自动放开）。

### 修复

- **suga 的供应商错误现在会触发自动续投（gofer-aiuj）**：内置「瞬时错误」模式此前只给 `codex` / `claude` / `omp`，`suga`（含 `suga-acp`、`command` 是 suga 的 wrapper）的失败一律按普通失败处理；同时内置模式补齐**流式返回中断**类错误（供应商的流中途断了，agent 拿到的是解析错误，如 `provider error: parse openai stream chunk: unexpected end of JSON input`）。该类只在错误附近同一行出现 `stream` / `chunk` 这类词时才算，普通的 JSON 解析错误（配置文件 / 会话文件损坏）仍按真失败处理。
- **web：排队中 / 等目录锁的 job 也有取消按钮（gofer-83fe）**：job 详情页的「取消」原先只认 `running`（以及带 session 的 `awaiting_input`），停在 `queued` / `waiting_dir`（等目录锁）/ `recovering` 的 job 在页面上没有取消入口，只能回 CLI `gofer job cancel`。现在按钮的显示口径与服务端 `job cancel` 的允许范围一致：除终态（`done` / `failed` / `cancelled` / `timeout` / `rejected`）与 `needs_review`（要 accept / reject，取消会 409）外，所有非终态——排队中、执行中、`pending_interaction`、`recovering`、`awaiting_approval`（取消 = 撤回）、`paused_for_update`——都显示「取消」；工作台会话头部的「停止」用同一口径。

## [0.161.0] - 2026-10-16

### 变更

- **权限：一份固定名称的目录，每条路由声明自己要什么（gofer-kd8q）**：server 现在持有一份权限目录（40 项，如 `job.submit`、`session.reply.others`、`config.write`），每条 `/v1` 路由声明它需要哪些权限；判定不再看配置里的能力位与 `governance.require_*` 开关，而是看**调用方持有哪些权限**。**现有部署升级后行为不变**：旧配置推出的授权与今天的能力位一一对应，另有「原本不检查」的那组在首次启用时发给全体用户类调用方（读他人 job、提交 job、取消 / 管理他人 job、会话读取与删除、plan / schedule 管理、steward、记忆写入、tracker 同步、托管转发、传输创建、配置读取、检查更新、MCP 测试）。每条权限的含义、风险、可否授予会话、来源，以及每条路由需要哪些权限见 [`docs/reference/permissions.md`](docs/reference/permissions.md)（由服务端代码生成，与二进制一致）。权限不够时响应是 `403`，正文写出缺的是哪一项。
- **根管理员拥有全部权限**（`can_admin: true` 的调用方），与四个 `governance.require_*` 开关无关——包括开关打开时也能答复 / 验收 / 唤醒他人的 job、开隧道、接入交互终端，以及开关关闭时也能代答他人终端会话、回答他人终端的工具权限提示、attach 无主 job。无主 job 的 attach 与 pty 录像仍然只有根管理员。
- **单 token 部署里的 `default` 成为根管理员**：没有 `server.callers` 时唯一能鉴权的 `default` 现在是根管理员（此前它不算管理员，所以单 token 部署里从来没有根管理员）。只有「`allow_empty_token` 且不声明调用方」的部署没有根管理员，那里的权限只读、没人能管理授权。
- **关闭三处旁路**：
  - 他人的传输（读取 / 下载 / 删除）只有根管理员能碰：`xfer.manage.others` 是仅根权限，**刻意不种入**给全体调用方，所以别人的传输不会随默认授权放开。
  - 托管转发除 `tunnel.hosted` 外另需 `tunnel.open`，停别人的托管转发只有根管理员——见下面「托管转发」一条。
  - 推送通知的**动作按钮**与**中继类决策**（终端会话在等的那个）的回答都按权限判定：按钮在签发与兑现时都查 `interaction.answer`（开关打开时只有带 `can_answer` 的调用方能拿到按钮），没有它仍会收到通知、只是不带按钮；中继类决策改按 `session.reply.others` 判定（默认部署里只有会话 owner 或无主会话的任何用户类调用方；开关打开时发给带 `can_answer` 的调用方），此前这条接口不做任何检查。
- **旧的能力位与开关弃用**：`server.callers[].can_answer` / `can_attach` / `can_tunnel` 与 `governance.require_answer_capability` / `require_admin_capability` / `require_attach_capability` / `require_tunnel_capability` 标为弃用（三个版本后移除），启动时每个仍在用的键给一条「请改用 web 授权」的提示；它们的判定结果不变（配置推出的只读授权）。开关打开时不再要求「至少一个调用方带对应能力位」——只要有根管理员就通过校验。`can_admin` 不在弃用之列：它继续用来指定根管理员。
- **调用方 id 与 worker id 不得重名**：`server.callers[].id` 与 `server.workers` 的键相同时，配置校验（加载、`config validate`、控制台写接口）直接拒绝。两者是两个鉴权身份，同名会让 job 归属、审计与权限判定含糊，worker 凭据也会盖掉该调用方自己的 token。
- **托管转发（`tunnel.hosted`）也要 `tunnel.open`**：托管转发的启停、预设的写入 / 删除 / 导入现在除 `tunnel.hosted` 外还需要 `tunnel.open` —— 开关（`server.governance.require_tunnel_capability`）打开时，托管隧道不再能绕过隧道权限。托管转发也不再借 server 自己的 token 回连自己的 HTTP 接口：它在本进程内拨号，隧道按**启动者**判定与登记（`GET /v1/tunnels` 与转发列表里的归属、流量统计都记在启动者名下），所以开关打开时只要启动者有 `tunnel.open` 就能用。升级后**存量 autostart 预设照常启动**：它们的启动者取预设的 `updated_by`；那个调用方已不在配置里时按根管理员处理（只有根能停它）并在启动日志里提一条告警。另外，**停别人的托管转发现在只有根管理员能做**（`tunnel.manage.others`）：此前任何用户类调用方都能停别人的托管转发，现在只有它的**启动者**或根管理员可以停。

## [0.160.0] - 2026-10-15

### 新增

- **job 结束时主动通知提交它的会话（`--notify` / `--notify-session`，gofer-3db4）**：过去只能靠那个会话自己的 Stop hook 等、或下一次输入时补投；现在 `gofer job run --notify` 让 gofer 在 job 结束时（终态，以及停在 `needs_review` 时）经**传话人**把一条系统通知送到指定会话，不必等它自己停下。
  - `--notify` 的目标是提交方（`--source-session-id`，否则按 `$GOFER_SESSION_ID` / `$CLAUDE_CODE_SESSION_ID` / `$CODEX_THREAD_ID` 认出当前会话），`--notify-session <sid>` 显式指定并隐含开启；都认不出会话时报错（不静默跳过）。HTTP / MCP 同名可选字段 `notify` / `notify_session`。
  - 通知一行，以 `[gofer job 完成]` 开头，带 job id、标题、状态、退出码、耗时、`wrap_up` 与 `job show` / `job logs` 提示，并注明是 gofer 系统通知（消息来源记为 `gofer`）；送达成功即消费掉该会话的 job watch，Stop hook 与补发不会再发一次，送达失败则保留 watch 由补发兜底，同一个 job 只通知一次（server 重启不重发）。
  - **默认关闭**：每条通知都要起一次传话人进程，因此只在派长 job（或准备离开）时才加。
- **plan 级开关（gofer-3db4）**：`gofer plan create --notify` / `gofer plan set <plan> --notify on|off`（缺省 `off`）让该 plan **派发的每个 job** 都通知它的主 Agent 会话（supervisor）——一个决定覆盖整条链，不必给每个 todo 加 flag。没绑主 Agent 会话时无人可通知：job 照常派发、不通知任何人，`plan show` / `plan create` 会打一行提示。HTTP / MCP 与 plan 同名字段 `notify`（`on`/`off`，PATCH 省略则保持原值）。
- **续跑继承（gofer-3db4）**：`gofer job resume` 默认继承源 job 的结束通知（通知同一会话），`--no-notify` 关掉；HTTP `/v1/jobs/{id}/resume` 对应 `no_notify`。
- **内部派发不再因目标会话失效而拒绝（gofer-3db4）**：只有**用户直接指定**的 `--notify` / `notify_session`（`job run`、`POST /v1/jobs`、MCP `gofer_run_job`）在目标会话不存在 / 属于别人 / 根本没给时才 400。**gofer 自己发起的 job**——定时任务（cron、run-now、webhook 触发）、plan 派发、workflow 步、wakeup、自动续跑、rerun（包括 hold 批准与升级停靠后的重入、故障转移、耐用重试）——一律**丢弃通知并照常跑**，在 job 上记一条 `job.notify_dropped {target, reason}` 事件（web 时间线标「结束通知已丢弃」），这样一段没人过问的活不会因为一条通知找不到收件人而丢。supervisor 会话已被清掉时 plan 派发也走这条路（源会话声明归零，不再卡住整条链）。
- **MCP 补上 plan 级通知开关**（gofer-3db4）：`gofer_create_plan` 新增 `notify`（`on`/`off`），新增工具 `gofer_set_plan_notify(plan_id, notify)`，与 `gofer_run_job` 的 `notify` / `notify_session` 一致。

## [0.159.0] - 2026-10-15

### 新增

- **写记忆时按子项目自动标注适用路径**（gofer-opv5）：工作区根目录有 tracker、下面多个子项目各自是独立 git 仓（各自有 `.git`）时，在某个子仓里新写的记忆自动带上 `when.paths = <子仓>/**`，只在那个子仓的 job / 会话里注入；命令多打一行提示（「适用路径已设为 deploy-hub/**（整仓通用用 --when-paths '**'，工作区通用请写成项目记忆 --project）」）。
  - 要**整仓通用**就显式写 `--when-paths '**'`；要**工作区通用（跨项目）**应该写成**项目记忆**（`memory set --project <p>`，存在 server 上整项目共享、web 记忆页有仓库 / 项目 / 全局三类）。`--when-paths` 可重复、逗号分隔，传空值或 `-` 清空已有路径；更新已有记忆一律不动它的 `when`。cwd 在 tracker 根所在的仓里、cwd 未知、或写 `--global` / `--project` 时都不自动标。
  - 交付后「可复用经验」候选同样按 job 的 cwd 标注（`memory candidates` 显示「适用路径 …」，接受时提示一行），接受后跟记忆一起写入；写到全局作用域时不带（全局记忆不属于任何仓）。
  - MCP / web 的记忆写入没有 cwd，不自动标注。
- **job 的 tracker-prime 加入项目记忆与全局记忆**（gofer-opv5）：派发的 job 过去只拿到仓库 tracker 的记忆，项目 / 全局记忆只进会话开场。现在 server 直接读 jobstore 的两类作用域记忆（不走 HTTP），按与会话开场相同的规则注入：规则类全文、其余进索引，按 cwd / `when.paths` 与 prime 关键词选材，顺序是项目记忆在前；总预算内截断，每节被截断时标出 `[项目记忆已截断]` / `[全局记忆已截断]`（`prime.scoped_memory: false` 整块关闭）。
- **`gofer memory scope`：给存量记忆补适用路径**（gofer-opv5）：默认只读地列出建议（表格：key、建议的 `when.paths`、依据、置信度），`--apply` 才写入。
  - 依据按优先级：来源 `job:` / `session:` 的 cwd 落在子仓（高）→ key 前缀 / 标签与子仓目录名匹配（高，去掉 `-cli` 之类后缀、`-` 与 `_` 归一后相同）→ 摘要 / 正文只提到一个子仓（中）→ 提到多个或无线索则不建议。
  - `--apply` 默认只写高置信度，`--min-confidence medium` 连中等一起写，`--only key1,key2` 只处理指定 key，`--json` 机器可读；已有 `when.paths` 的记忆一律不动。

## [0.158.1] - 2026-10-14

### 修复

- 两处切片容量提示改用 `util.CapSum`，消除 CodeQL `go/allocation-size-overflow` 告警（关键词选材、升级排空计数）。

## [0.158.0] - 2026-10-14

### 新增

- **嵌套仓 job 把项目根交给 agent（`add_dir_args`）**：一个项目根下常放着多个仓（工作区的 `gofer/`、`web/`、`.worktrees/` 等）。job 的 cwd 是其中一个**嵌套仓**（它所在 git 仓顶层 ≠ 项目根）时，agent 看不到项目根那一层的东西——工作区级规则、tracker、文档；现在 agent 配置新增通用字段 `add_dir_args`（须含 `{{dir}}`）会追加到 argv，`{{dir}}` 填项目根，续接 argv 同样追加。
  - **内置 `suga` 模板默认 `[--add-dir, "{{dir}}:ro"]`**（只读：外层工作区是要读的上下文，不是第二个写入点）。cwd 就在项目根自己的仓里、agent 没配这个字段、或 cwd 所在仓根本定不下来（没装 git / 不是 checkout）时都不追加，行为与以前一致；acp-agent 不支持该字段（配置校验直接拒）。

- **job 上显示 agent 自己的收尾原因（`wrap_up`）**（gofer-gk74）：`suga` 的 stream-json 在步数 / 用量 / 时限到顶时由 agent 自己收尾（result 行带 `wrap_up=max_steps|max_tokens|timeout`，正常回答不带），旧版 suga 撞步数上限时最后一行是 `{"type":"error","error":"max steps exceeded: 100"}`；两种都识别为同一种。
  - 这种 job 的 status **仍是 done**、汇报也在（汇报是真实产出），以前只能自己从 agent 输出里找线索；现在 `job show` / `job review` 打一行 `wrap_up: 步数上限（agent 自己收尾，任务可能未完成）`，web 详情有「收尾原因」，`GET /v1/jobs/{id}` 带 `wrap_up` 字段，持久化到 `jobs.wrap_up`。
  - 与 `stop_reason`（ACP 协议的停止原因）及 gofer 的预算熔断（status=failed、failure_class=budget）都不是一回事：只认 agent 自己说的，不认识就不标。要续跑用 `gofer job resume <id> --prompt "…"`。

- **job 注入的 tracker-prime 按任务关键词选材、排序**（gofer-11tz）：派活时给出任务涉及的子系统 / 语言 / 模块，注入的规则 / 记忆里命中的一条不再被 cwd 与预算淹没。
  - `gofer job run --prime-keywords "job, rules"`（plan todo 用 `plan add-todo|set-todo --prime-keywords`，`--prime-keywords ""` 清空）；不给 flag 时读环境变量 `GOFER_PRIME_KEYWORDS`，**显式 flag 优先**——环境变量留在派活的终端里，单独用它会把之后无关的 job 也缩小选材。
  - 匹配文本 = 关键词 + job 标题 + prompt 开头约 2KB；命中条件 = 记忆的 `when.keywords` 出现在这段文本里，或关键词出现在记忆的 key / 标签 / 摘要 / 它自己的 `when.keywords` 里（不区分大小写、子串）。命中项排在索引最前、优先进全文与预算，并标「（相关）」；命中且**没有 `when.paths`** 的 rule 给全文，带 `when.paths` 指向别处的仍只列一行（路径是作者对适用范围的声明）。未命中的一条都不删，行为与以前一致。
  - 会话开场同样支持：`gofer repo prime --keywords "…"`（同样读 `GOFER_PRIME_KEYWORDS`）。

- 新增会话配置 `session.stop_job_wait`（gofer-rhux）：`away`（默认）/ `always` / `never`，决定 Stop hook 要不要为「关注中的 job」（plan 绑定了本会话，或 job 带 `source_session_id`）阻塞终端。以前只要有未结束的关注 job 就一直阻塞到某个 job 结束（最长 `--wait`，约 2 小时），期间终端输入与跨会话消息都进不来。
  - `away`：只在人不在时为 job 阻塞，判据与 relay 自己的布防一致——`relay=on`，或 `relay=auto` 且键鼠空闲超过 `session.auto_relay_idle_sec`；空闲读数未知（容器里读不到主机键盘）算「人在」，显式 `relay=off` 也算「人在」。
  - `always`：旧行为；`never`：一律不等，立即放行。
  - 不阻塞不等于丢通知：job 结束的通知照旧由下一次 SessionStart / UserPromptSubmit 的 catch-up 补发。
  - hook 以本机配置文件里的值为准；本机没写时用 server 的值（server 在会话响应里新增 `stop_job_wait` 字段下发），因此在容器里跑的 hook 也听 server 这份。

### 修复（安全）

- **worker token 收紧到工作进程自用的路由**（gofer-hxld）：worker token 以前在 `/v1` 组里等同于一个操作员 token，可提交 job、改 agent / 项目配置、登记新 worker、读写会话与 job；现在只放行工作进程自己调用的几条——`GET /v1/workers/{id}/assignable`、`GET /v1/workers/{id}/upgrade/file`、`GET|PUT /v1/xfer/{id}/content`——其余一律 403 `worker credentials cannot call this endpoint`（三条 WS 握手路由在鉴权组之外，不受影响；job 的状态 / 日志 / 结果 / 心跳 / 取消本来就走 worker 连接，不经 HTTP）。同时 `callerMayAdmin` 先要求 user 类凭据：worker / job 凭据无论 `governance.require_admin_capability` 开关如何都不是管理员。
- **schedule / 传输 / 会话授权三项收口**（gofer-hxld）：
  - `GET /v1/schedules`、`GET /v1/schedules/{id}`、`rotate-token` 对非 owner 且无 `can_admin` 的调用方把 `trigger_token` 掩码成前 4 位 + `*`；`POST /v1/schedules/{id}/run-now` 改用**当前调用方**的身份提交（job 的 `caller_id` 就是执行 run 的人），计划存储的请求仍归属创建者（cron / webhook 按创建者提交）。
  - 传输记录属于发起它的 caller：`GET /v1/xfer` 只列自己的（除非 `can_admin`），`GET /v1/xfer/{id}` / `DELETE` / 用户下载他人 `get` 内容一律 403；worker 自用的 `PUT|GET /v1/xfer/{id}/content` 不变。
  - 终端授权卡（`POST /v1/sessions/{sid}/permissions/{id}/answer`）：除会话 owner 外，带 `can_admin` 的 caller 也能作答——换 token 后会话 owner 是 hook 的身份（如 `agent-dev`），web 上的人（另一个 caller）以前看得见却答不了；worker / job 凭据仍一律 403。

## [0.157.0] - 2026-10-14

### 变更

- 新增项目配置 `projects.<k>.tracker_prime`（gofer-krzy），决定 job 注入的 `tracker-prime` 取自哪个 tracker：`nearest`（默认，与以前一致：逐级向上找最近的 tracker，工作区里的项目共用工作区 tracker）| `repo`（只找 job cwd 所在 git 仓内的 tracker，到该仓顶层就停，给需要隔离的嵌套独立仓用）| `off`（不注入；`gofer repo prime` 不受影响）。绑定规则与 `.gofer/RULES.md` 照常注入。
- 同一批：job 注入的 `tracker-prime` 按 **job 的 cwd** 选材（与会话开场 prime 同一套）：`when.paths` 不覆盖该 cwd 的规则不全文注入，索引只列适用于该 cwd 的记忆（没设 `when.paths` 的，或命中的），命中的排前面；cwd 是仓库根时索引列出全部记忆（job 可能改任何子目录），只是不命中的规则不全文注入；cwd 相对 tracker 根目录计算。因此**语言 / 子系统专属的记忆请加 `when.paths`**，否则仍会注入到整个仓库的每个 job。

### 修复

- `gofer job resume` 的续接 job 明确跟着源 job 的 plan：默认继承（提交时的 `--plan`、plan 派发、`gofer plan attach` 都算），`--plan <plan-id>` 换一个，`--no-plan` 不挂（并丢弃继承的 checklist 挂接）；`gofer job show` 现在显示 `plan:` / `todo:`，以前 plan 根本不显示——续接 job 看上去像“没挂 plan”（gofer-z414）。HTTP：`POST /v1/jobs/{id}/resume` 新增可选 `plan_id`（空串 = 不挂）。
- 测试：xfer 的后台派发改为可等待（`Manager.Wait`），server 停机与测试收尾时先等派发结束再关库，修复 Windows 上偶发的「数据库仍被占用」清理失败。
- `gofer plan attach` 把 job 挂到还在跑（或刚终态、进程里还留着的）job 上不再丢：以前只改元数据库、不改内存里的那份 job，该 job 自己下一次写入（含终态落库）会把 plan 覆盖回去，于是 `plan show` / 续接都看不到它（gofer-z414）。

## [0.156.0] - 2026-10-14

### 新增

- 一键更新改为「空闲时更新」（gofer-dzv3）：下载并校验新版本后不再死等运行中的 job（以前最多 5 分钟、超时即放弃），而是先**等机器空闲**——运行中的 job 照常跑完，**等待期间新提交的 job 照常接收但先停住**（新非终态 `paused_for_update`，不占执行位），不再对提交方回 503；空闲后重启，新 server 起来先把停住的 job 全部放行（同一 job id 照常开跑，不用重新提交）。已在 worker 上跑的 job 不阻塞重启（worker 进程留着它，server 回来后重新接管），但还没有交给 worker 的 worker job 仍会等。确认框可选最长等待 30 分钟 / 1 小时 / 2 小时 / 4 小时（默认 2 小时，请求体 `wait_idle_sec`）；等待期间关于页列出还在挡着重启的 job（可点进详情）、已停住的 job 数与放弃时间，并可点「取消等待」（`DELETE /v1/update/apply`，管理员）。apply 失败 / 超时 / 取消都会把停住的 job 放行。

### 变更

- `gofer serve upgrade` 的排空上限改为 `--drain-timeout <dur>`（默认 `5m`）：排空现在按「本机还没做完的活 + 尚未交给 worker 的 worker job」计，已在 worker 上跑的 job 不再算（以前会把它们和断线 `recovering` 的 job 都等进去）。
- 一键更新失败时的原因不再只有 `exit status 2`：从 `run/update/serve-upgrade.log` 尾部取命令行自己报的错（如排空超时），并补上当时还在挡着重启的 job 与日志路径。
- `paused_for_update` 在 web 与 CLI 上可读：`gofer job ls --status paused_for_update`、调度板与新 job 提交输出都有说明。

## [0.155.1] - 2026-10-14

### 修复

- 切 token（`server.callers`：人和 agent 用不同 token）后旧会话会被拒且**无声停更**：hook 以新身份上报被拒时，会话上记下最近一次被拒的调用方与时间（`refused_caller_id` / `refused_at`，`GET /v1/sessions` 与详情返回，server 日志一条限频 warn，hook 日志记一行）；web 会话列表 / 详情显示「归属被拒」，并新增「转给 <caller>」按钮与 `POST /v1/sessions/{sid}/owner`（`gofer session owner <sid> <caller>`，需 `can_admin`）把会话转给 hook 现在用的调用方，转移后心跳立即生效（gofer-eh53）。
- 管理员可以修改他人 plan 的 `supervisor_session_id`，并能绑定任何调用方的会话（须存在、项目一致）：切 token 后 web 上的人与建 plan 的 agent 不是同一个调用方，之前只认 plan owner，管理员也改不了；非 owner 且无 `can_admin` 的调用方仍然被拒，报错也说清了会话属于哪个调用方、本次是谁在请求（gofer-ka6r）。

## [0.155.0] - 2026-10-14

### 变更

- 最低 Go 版本从 1.25 升到 1.26（`go.mod`）：CI 改为测 1.26 与最新稳定版，发布构建用最新稳定版（gofer-c8nf）。

### 新增

- 内置 agent 模板新增 `suga`（cli-agent）与 `suga-acp`（acp-agent）：本机 PATH 上有 `suga` 就自动注入（`agent list` / web 上标「内置」）。两者共用一份会话存储，`job resume --agent` 可在 `suga` ↔ `suga-acp` 之间换。suga 的 job 默认跑 `suga run --allow-write --output-format stream-json`（stdout = 最终答复、stderr = 过程事件、用量计入预算），并内置了只读（`--read-only`）、`--model`、`--from-session`、取消时的 TUI 退出输入与在线送话（`suga send`）（gofer-nitz）。

### 修复

- acp-agent 的 job 不填超时时默认超时从 300 秒改为 1200 秒，与 cli-agent 一致：两者都要跑完整的一轮模型调用，之前在 web 上新建 job 选 acp-agent、超时留空，只有 5 分钟就判定超时（gofer-6uhf）。

## [0.154.6] - 2026-10-13

### 修复

- `serve upgrade`（含一键更新）在排空之前先加载一次配置文件：配置读不通（例如改到一半）时直接拒绝，旧 server 继续运行。之前要等新 server 启动失败才发现，而回滚又可能失败，服务被留在停止状态。
- 升级失败回滚时，supervisor 文件可能仍被刚停止的进程占用（Windows 报 `Access is denied`），现在会重试；即使仍恢复不了，也会把已恢复的旧程序重新启动，不再让服务停着。

## [0.154.5] - 2026-10-13

### 新增

- 出站代理 `http_proxy: {proxy_url, allow_sites}`：server 自己发出的请求（检查更新与下载、skill 导入、通知 webhook、Web Push、交付 API）访问 `allow_sites` 里的站点时走 `proxy_url`（http / https / socks5），其余照旧跟随环境变量；`example.com` 含子域名，`*.example.com` 只含子域名，省略 = 全部。只能改配置文件，reload 立即生效；`server.update.proxy` 仍优先（gofer-29gi）。

### 修复

- Windows 上以服务方式运行的 server 一键更新时，替换步骤报 `detached breakaway: … Access is denied`：server 在计划任务的 job object 里，不允许升级 helper 脱离。现在 `serve upgrade` 放在允许脱离的 job object 里运行（与作为 job 执行时相同）。

## [0.154.4] - 2026-10-13

### 修复

- web：「设置 → 关于」的「刷新」「检查更新」按钮没有样式（显示成浏览器默认按钮）；Issues 页分段按钮（仓库 / 项目 / 全局、树形 / 平铺）、状态筛选、分页、批量操作与记忆的保存 / 删除按钮字号偏大，统一为与其他页面一致的小按钮。

## [0.154.3] - 2026-10-13

### 修复

- 一键更新从 GitHub 下载时改走 `api.github.com` 的资产地址：有的网络能访问 API 但连 `github.com` 的下载链接超时，导致读不到 `latest.yaml` 的 sha256、下载新版本失败。取不到 `latest.yaml` 时日志记 `update.checksums_unavailable`；一键更新在下载阶段失败时，未处理的 worker 显示为「跳过」。

## [0.154.2] - 2026-10-13

### 修复

- 0.154.1 的「注册时补触发」也作用到了监听自己的唤醒（不带 `--job-id`），对已结束的 job 注册就会立即续跑它；现在只用于监听另一个 job。

## [0.154.1] - 2026-10-13

### 修复

- 监听另一个 job 结束（`job wakeup create --kind event --event job.terminal --job-id <X>`）时，如果 X 在注册前已经结束，唤醒永远不会触发（结束事件已经过去），只能等到过期；现在注册时发现 X 已结束（且符合 `--status`）会立即触发一次。

## [0.154.0] - 2026-10-13

### 新增

- 一键更新：有新版本时 web 顶栏显示 `↑ <版本>`，并发 `update.available` 通知（默认触发事件）。以服务方式运行的 server（`gofer serve register`）可在「设置 → 关于」点「立即更新」：server 下载新版本（有 sha256 就校验）并通过 `serve upgrade` 替换自己（等运行中的 job 结束，失败回滚），重启后再升级勾选的 worker（默认勾选全部在线的；其他平台的 worker 下载自己平台的二进制）。普通 `gofer serve` 显示手动步骤。接口 `POST /v1/update/apply`，`GET /v1/update` 新增 `managed` / `apply` / `sha256`（gofer-29gi）。
- `make latest-yaml` 在 `latest.yaml` 里记录各平台二进制的 sha256（`files`）；`make release` 把 `latest.yaml` 一并放进发布物，GitHub release 也会上传它。

## [0.153.1] - 2026-10-13

### 修复

- worker 执行派发的 job 时启动的两个辅助 goroutine（关停时取消本地 job、提前上报渲染命令）不在派发的等待范围内，派发结束后仍可能在访问本地 job 库；现在派发返回前会停止并等它们结束（gofer-g3rn）。
- job 结束并从内存移出后，从库里读回时丢失了 issue / tracker 关联（只存在请求记录里），`job show` 看不到关联，job 凭据按 tracker 的作用域检查也可能误拒；现在读回时一并恢复。
- 测试稳定性（只影响 CI）：假 ACP agent 的孙进程 pid 文件改为单一写入者、原子写入；ACP 超时取消测试的时限覆盖 agent 启动；worker 恢复测试等待偏移量提交（gofer-9n1n）。

## [0.153.0] - 2026-10-13

### 新增

- 检查更新：server 定时检查是否有新版本（默认每 2 小时，`server.update.interval`），web「设置 → 关于」显示当前 / 最新版本与本平台下载链接，可点「检查更新」立即检查。默认查 GitHub 最新 release，配 `server.update.latest_url` / `url_template` 改查内部 mirror（`make build-all` 生成的 `latest.yaml` 与二进制，占位符 `{name}` `{os}` `{arch}` `{ext}` `{version}`），`proxy` 可指定代理。只检查不升级；配置只能改文件。接口 `GET /v1/update`、`POST /v1/update/check`（gofer-6x2n）。

## [0.152.3] - 2026-10-13

### 修复

- 没有声明 `allowed_agents` 的项目（即允许所有 agent）在 `gofer agent probe` 上被当成「全部不允许」，返回 400；故障转移的候选 agent 也因同一判断在这类项目里全部被跳过。现在与 job 提交一致：空列表即不限制。

## [0.152.2] - 2026-10-13

### 修复

- 全局记忆同步状态里，机器名附带操作系统（如 `DESKTOP-1 (linux)`）：容器通常沿用宿主机的主机名，原来会与宿主机合并成一行。

## [0.152.1] - 2026-10-13

### 修复

- 交互等待（`WaitAnswer`）在 job 刚结束、已从内存移出后才开始等待时，返回交互已落库的最终状态（已答 / 已取消），不再报「unknown job」；CI 上偶发的 `TestFinishReconcilesPendingInteraction` 失败即此时间窗口。

## [0.152.0] - 2026-10-13

### 新增

- job 密钥引用（`job run --secret <名字>`，JOB-06②）：顶层 `secrets` 声明从 server 进程的哪个环境变量取值（值不进配置），项目用 `projects.<p>.secrets` 允许；提交时只记名字，执行时注入 job 环境（变量名可用 `as` 指定），job 的 stdout / stderr 写盘时把值替换成 `***`，并记 `job.secrets_granted` 事件。只支持 server 本机 runner、不支持交互 job；两项配置只能改文件；`config validate` 提示本机未设置的变量（gofer-rjmi）。
- 全局记忆各机器的同步状态：同步时上报主机名，server 按机器记最近一次同步，web 的 Issues → 记忆 → 全局 显示「同步状态」面板；接口 `GET /v1/tracker/repos/{tracker_id}/clients`（仓库 tracker 同样适用）。
- plan 详情与看板卡片显示 todo 关联 job 的交付状态：开了 PR 时有「PR ↗」链接，交付失败时标红（gofer-5n9i）。

### 变更

- 其余 `make` 容量提示统一经 `util.CapSum`，防止 CodeQL 的分配大小溢出告警。

## [0.151.0] - 2026-10-13

### 新增

- 本地全局记忆：`memory --global` 改为读写本机配置目录下的副本 `<config-dir>/tracker/`（与仓库 tracker 同格式），不连 server 也能完整使用；连着 server 时写后自动同步，`gofer memory sync --global` 手动同步，新机器第一次使用先从 server 拉取。`archive` / `restore` / `promote` 现在也支持 `--global`。在 gofer job 里全局记忆只读，`flag --global` 经 server 记录后拉回本地（gofer-j6qn）。
- 会话开场的 prime、按关键字 / 命令注入记忆的 hook、`issue / plan brief` 改为从本机副本读全局记忆：server 慢或连不上时全局约定照常注入，hook 也少一次网络往返；prime 在原有预算内先同步一次。从未同步过、本地也为空的副本仍向 server 读（gofer-j6qn）。

### 变更

- `memory ls|show --global --json` 输出改为仓库记忆的格式（`by` 代替 `updated_by`，没有 `scope`）。

## [0.150.0] - 2026-10-13

### 变更

- 全局记忆在 server 上改存为保留的 tracker `global`（与仓库 tracker 同一套带版本号、删除记录与增量游标的存储），升级后首次启动自动迁移已有的全局记忆。`memory --global`、web、MCP 与知识沉淀的用法不变；这是配置目录本地全局记忆（离线可用、与 server 双向同步）的第一步（gofer-j6qn）。

## [0.149.0] - 2026-10-12

### 新增

- 决策 hook（`server.decision_hooks`）：提交 job 前（`job.submit`）、交付 worktree 分支前（`job.deliver`），server 同步运行配置的命令，按它输出的 JSON 放行、否决，或只往保守方向修改（提交：加验收、改成待批、只读、调低超时与预算、加标签；交付：改 PR 标题与正文、改成 draft、加 label）。脚本出错、超时或要求放宽时默认按否决处理（`on_error`）。提交被否决返回 403，交付被否决记为交付失败并通知，每次裁决记成 job 事件 `job.decision_hook`。只能在配置文件里改。`gofer tool hook-test` 用样例输入试跑一个 hook。协议见 `docs/runbook/decision-hooks.md`（gofer-sr0b）。

## [0.148.1] - 2026-10-12

### 新增

- `job deliver --dry-run` 也可预览 `needs_review` 的 job：accept 之前就能看到将要开的 PR 标题与正文（gofer-iy68）。

### 修复

- `--worktree` job 被 `reject --resume` 退回返工后，续接 job 现在在同一个 worktree 与分支上继续，并沿用源 job 的交付设置；返工后 accept 即自动交付整条分支（之前续接 job 没有 worktree 记录，无法交付）。交付的 PR 标题与任务说明取最初的任务，而不是退回理由（gofer-glyo）。

## [0.148.0] - 2026-10-12

### 新增

- `gofer job deliver <id> --dry-run` 预览交付模式、分支、目标分支和 PR 标题与正文；HTTP 交付接口支持 `dry_run`，预览不会执行交付或写入交付记录。

### 修复

- 合并交付（及 `job worktree merge`）因冲突以外的原因失败时（如机器没配 git 提交身份、hook 拒绝），报告 git 的原始错误，而不是一句没有文件列表的「has conflicts」。

## [0.147.0] - 2026-10-12

### 新增

- 从 web 给终端会话发消息并送达后，对话转到 web 继续：`auto` 会话自动切到 `on`（之后每次停下都在 web 等回复），人在终端输入一条即照常降回 `auto`；显式 `off` 不受影响（gofer-n64n）。

### 修复

- 交付开的 PR 正文不再带 gofer 注入的规则段与工作约定段（规则 / 记忆里可能有机器名、路径、内网地址，而 PR 可能公开），只放提交时写的任务说明；回到 gofer web 的 job 链接改为 `delivery.job_link: true` 才附。
- 会话等着的 job 停在 `needs_review`（开了验收）时，Stop hook 不再一直等下去：`needs_review` 与终态一样唤醒监督会话，通知里附「待验收」提示。

## [0.146.2] - 2026-10-12

### 新增

- 交付支持代理：项目 `delivery.proxy`（http / https / socks5）让交付的推送与平台 API 都经代理；不配时平台 API 跟随 git 对该 remote 的 `http.proxy`，再退到代理环境变量——只能经代理访问 GitHub 等平台的机器也能自动开 PR。

### 修复

- web 给 Claude 会话发消息报 `no_tmux`：hook 识别会话的 SendMessage 地址时，按文件名只扫 Claude 会话目录里的前 128 个文件，而目录里会积下大量已退出进程的文件，排在后面的活会话被漏掉，于是退到 tmux 投递。现在按修改时间从新到旧扫描。

## [0.146.1] - 2026-10-12

### 修复

- 工作项整理 job 不再注入 agent / 项目的 rules（例如 agent 挂了通用纪律时，每次整理都会把它拼进 prompt，浪费 token 并干扰固定 JSON 输出）。

## [0.146.0] - 2026-10-12

### 新增

- worktree job 的交付（GIT-02）：项目配置 `delivery`（或 `job run --deliver`）让 `--worktree` job 在完成（开了验收则 Accept 后）且有新提交时自动交付——`merge` 合进主 checkout、`push` 推分支、`pr` 推分支并开 PR / MR（GitHub 含企业版、GitLab 含自托管、Gitea / Forgejo，按 remote 识别，走 REST API，token 从执行机的环境变量或已登录的 gh / glab 取）。推送须项目 `delivery.allow_push: true`，从不 force push；同分支已有打开的 PR 时复用。PR 正文带任务说明、验收标准、提交、改动统计、验证、用量、验收结论与 job 链接。worker 上的 job 在那台 worker 上交付。`gofer job deliver <id>`（及 web job 详情的「交付」）手动交付 / 重试；`gofer job accept --no-deliver` / web「本次不交付」通过但不交付；交付失败进默认通知（gofer-5sms）。
- 费用估算：顶层 `pricing`（每百万 token 美元价，模型名或通配，不内置价格）给没有自报费用的用量估算费用——终端会话用量、plan 合计、`/v1/stats` 会话窗口、带 `--model` 的 job；界面标「估算」并列出未定价的模型（gofer-2ldy）。

### 修复

- plan 只有主会话用量（没挂 job）时，plan 页也显示用量合计。

## [0.145.2] - 2026-10-12

### 变更

- `gofer worker show` 与 web Runners 页的升级历史改为最近 5 次，每条带升级时间。
- `gofer init hooks` 的提示里加上选择题转 web 的说明。

## [0.145.1] - 2026-10-12

### 修复

- 终端选择题上 web：AskUserQuestion 的 PreToolUse 走了不连 server 的快速路径，hook 空指针崩溃，Claude Code 报 `PreToolUse:AskUserQuestion hook error`。改为走完整路径（gofer-02fu）。

## [0.145.0] - 2026-10-12

### 新增

- 终端会话的选择题上 web（gofer-02fu）：Claude Code 调 AskUserQuestion 时，若人不在终端前（与 Stop 中继、授权上 web 同一判据），问题变成 web「等你回答」卡（会话抽屉按题单选 / 多选 / 自由填写，「今天」页单个单选题可直接点选项），作答后由 hook 作为工具输入交回，终端不弹框；超时、人回到终端或 server 不可达时交回终端。需重跑 `gofer init hooks` 装上新的 PreToolUse 条目。
- web 配置页的 agent 表单可编辑 MCP 绑定（`mcp` / `mcp_format` / `mcp_args`）。

### 修复

- web 发给会话的消息由传话人以 Claude Code 会话名 `gofer-web` 转达，接收方不再看到按目录自动起名的别的会话名而误以为不是用户本人（gofer-02fu）。
- codex 等 agent 已正常退出、答复完整，但其子进程仍占着输出管道时，job 不再被标成失败（`exec: WaitDelay expired before I/O complete`），按进程自身退出码判定并在 stderr 注明（gofer-uyou）。

## [0.144.0] - 2026-10-12

### 新增

- MCP 注册表（MCP-06）：server 配置顶层 `mcp_servers` 声明 MCP server（stdio / http），用 `server.mcp`、`agents.<k>.mcp`、`projects.<k>.mcp`、`job run --mcp` 四级绑定（`--no-mcp` 关闭）。派发时按 agent 自动注入：acp-agent 走 `session/new` 的 mcpServers（http 仅对声明支持的 agent），claude 生成 `--mcp-config` 文件，codex 用 `-c mcp_servers.*`；续接会重新注入。凭据只写环境变量名，值从执行 job 的机器读取，不进命令行、任务记录、生成的文件与日志。worker 协议 v23 起随派发带上 MCP 声明（旧 worker 跳过并记事件）。`gofer agent mcp ls|show|test`、web「设置 → MCP」查看与测试；job 详情显示绑定的 MCP（gofer-0sxd）。
- 终端会话用量也在节流的进度心跳上报：长时间不结束的一轮，plan 上的「主会话用量」会随进度更新，不必等这一轮结束。

### 修复

- `gofer worker reload` 不带 id 时，与 `--local` 一样先识别本机唯一运行的 worker，不再直接报 `worker id is required`（gofer-wvyo）。

### 兼容

- worker 协议升到 v23；MCP 绑定要派到 worker 执行，worker 需升级到本版本。

## [0.143.0] - 2026-10-11

### 新增

- agent `extends` 支持另外两种写法：`builtin:<模板名>` 以指定内置模板为底（agent 名可与模板不同）；`<agent 名>` 以同一配置里已声明的另一个 agent 为底，可链式继承，循环和自引用报错。web 徽标显示继承来源。
- 验收面板「发现」页签每条可直接「建 issue」：建到该 job 的仓库 tracker（job 关联的 tracker，否则项目唯一的 tracker），带 `discovered` 标签和来源 job，下次 `gofer repo sync` 进仓库；同一条重复点击不会重复建。没有可用 tracker 时显示原因并给出 CLI 命令。新接口 `POST /v1/jobs/{id}/findings/issue`。
- `gofer config lint worker [-c worker.yaml] [--fix]`：worker.yaml 也能找出可删的键（agent 的默认 detect / 续接参数、显式写出的 exec 等）、可改写成 `extends: builtin` 的 agent 与未知键。

## [0.142.1] - 2026-10-11

### 修复

- 热加载回执（`serve reload` / web「重新读取文件」）的 `changed` / `restart_required` 改为按生效值比较：删掉与默认值相同的键、agent 改写成 `extends: builtin`、把段落挪进 `include` 文件不再误报变更（例如误报 `restart_required=server.web_enabled`）（gofer-n6ax）。
- server 停机时先关闭 worker 连接并等其中的 job 转入 `recovering` 并落库，再关闭数据库；之前这一步在数据库关闭之后执行，日志里出现 `persist recovering snapshot … database is closed`（gofer-5fiw）。

## [0.142.0] - 2026-10-11

### 新增

- 配置拆文件：server 配置可写 `include: [config.d/*.yaml]`，把整段（如 agents、projects）放到单独文件；一个段只能在一个文件里。web 保存时写回该段所在的文件（保留注释、未变的文件不动），热加载 / `config validate` 覆盖所有文件，reload 回执与 `config info` 列出文件，`config edit --key <段>` 打开所在文件，web 配置页标出每段的文件。`gofer config split` 按原文把 agents、projects 搬出去（先备份、前后比对生效配置）（gofer-arsj）。
- agent `extends: builtin`：与内置模板同名的 agent 只写要改的键，其余沿用模板并随升级更新；写了的键整项替换（`false` / `[]` 也算），web 修改时只写回差异；config.yaml 与 worker.yaml 都支持；Agents 页显示「继承内置」（gofer-arsj）。
- `gofer config lint [--fix]`：逐个键试删并重新加载，列出删掉也不改变生效配置的键（与默认值相同的 detect、日志 / 存储默认值、默认续接参数等），`--fix` 原地删除（先备份、改完复核）；给出可改写成 `extends: builtin` 的 agent 的最小写法；未知键单独列出、不自动删除（gofer-arsj）。
- job 用量记录 agent 报告的工具调用次数 `tool_calls`，`job show` / web 显示为 `tools N`（gofer-z2c6）。

### 修复

- omp 的用量与预算计量改为把每次模型请求的用量相加；之前只取了最后一次请求，多步任务的用量被严重低估。

### 兼容

- 使用 `include` 或 `extends` 的配置需要 server、worker、CLI 都是本版本或更新：旧版本会把这两个键当作未知键忽略。

## [0.141.1] - 2026-10-11

### 修复

- `job run --model`：cli-agent 的 prompt 是某个 flag 的值时（`--prompt {{prompt}}`、`-p {{prompt}}`），`model_args` 改为插在该 flag 之前，不再插进 flag 与值之间、把 `--model` 当成 prompt 传给 CLI。claude 内置模板的参数顺序随之变为 `--model <m> --verbose <prompt>`，效果不变。

## [0.141.0] - 2026-10-11

### 新增

- 隧道动态转发 `socks/[bind:]port`：`gofer tun forward -w <worker> socks/1080` 在本机起一个 SOCKS5 代理（同一端口也接受 HTTP CONNECT），浏览器 / curl 用目标的原始地址访问 worker 网络，不必再给每个目标分配本地端口。每条连接仍按真实目标走隧道鉴权与 worker 白名单，被拒时返回 SOCKS5 回复码 / HTTP 状态；只能绑环回地址；可写进预设、由 server 托管启停（TUN-06，gofer-2m7a）。
- 同地址转发 `[udp/]=ip:port`：在本机 `ip:port` 监听、转发到 worker 网络里的同一地址，本地服务按真实地址联调（需先把该 IP 挂到本机环回网卡）（TUN-07，gofer-2m7a）。

### 变更

- `tun forward` 与 server 托管转发启动前检查所有规则的监听地址是否属于本机：不在就整体报错、不启动任何监听，并给出添加环回地址的命令（Linux / macOS / Windows），不再是 `cannot assign requested address`（TUN-07）。
- 含 `socks/` 或 `=` 规则的预设需要新版 CLI 才能读取。

## [0.140.2] - 2026-10-11

### 修复

- worker 上的 ACP 持续会话如果还停在第 0 轮（例如续接后还没发第一条消息），server 重启或升级接管后不再一直显示「运行中」：会恢复成等待输入，工作台可以继续输入（之前要等空闲超时结束）（gofer-rvdd）。

## [0.140.1] - 2026-10-11

### 修复

- 工作台的 ACP 持续会话：还没有任何对话记录的一轮（例如刚续接、还没发第一条消息）不再一直显示「加载结构化记录…」。流连上即结束加载，等输入时显示「会话已就绪，等待输入」，运行中显示「agent 运行中，等待第一条输出」（gofer-m600）。

## [0.140.0] - 2026-10-11

### 新增

- 事件钩子 `server.event_hooks`：事件发生时在 server 主机上运行一条命令（argv，不经 shell），事件 JSON 与 generic webhook 的 POST body 相同、从 stdin 传入，另带 `GOFER_EVENT_TYPE` / `GOFER_DELIVERY_ID` 等环境变量；退出码非 0 按 webhook 同一退避表重试，stderr 尾部脱敏后进投递记录。钩子不受通知总开关与项目 `notify_enabled` 影响，最多 4 个并行、不拖住 webhook。因为等于在主机上执行命令，只能在配置文件里改：web 配置页只读展示（env 只显示键名），配置写接口拒绝该字段；`gofer config validate` 检查名字 / 命令 / 超时，命令在本机找不到时给 WARN。用法见 `docs/runbook/im-notification.md` 第 8 节（gofer-u4e1）。
- runner 可以声明本机装了哪些外部工具：worker 在 worker.yaml、server 本机在 config.yaml 顶层写 `tools:`（名字 + 可选说明 / 可执行文件）。派到这台机器的 agent job，prompt 会在规则和技能清单之后带一段「本机可用工具」（exec job、续接、`--no-rules` 不加）；`gofer worker show`（`-v` 带说明）、`GET /v1/runners` 和 web Runners 页能看到每台机器声明的工具；worker 跑的 job 环境里多了 `GOFER_WORKER_ID`。可执行文件找不到时 `gofer config validate` 与 `gofer worker doctor` 只告警（gofer-vni6）。

## [0.139.1] - 2026-10-11

### 修复

- Windows 受管 server（`gofer serve register/start/upgrade`）启动和升级时不再闪一下 cmd 窗口：计划任务拉起的 supervisor 拷贝改为 GUI 子系统，系统不再先给它建控制台窗口。升级时 supervisor 由发起 `serve upgrade` 的 gofer 程序重新拷贝，用新版本发起升级即可生效（gofer-gwn8）。
- 安全加固（code scanning）：pty 录像下载的 Content-Type / nosniff 直接设在写出正文的响应上，CI 的 CodeQL 不再报 XSS（gofer-1sqv）。

## [0.139.0] - 2026-10-11

### 新增

- `gofer repo mirror ls` 列出 server 上的 tracker 镜像，`gofer repo mirror rm <tracker_id> --yes` 删除一个镜像（测试或废弃仓库的镜像原先无法清理）；对应 `DELETE /v1/tracker/repos/{tracker_id}`，只允许人凭据且需管理员能力。仓库本地的 tracker 不受影响（gofer-gtom）。

### 修复

- 安全加固（code scanning）：pty 录像下载加 `X-Content-Type-Options: nosniff`，终端输出不会被浏览器当成网页解析；skill 导入的 git clone 用 `--` 隔开选项与地址；skill 解包路径改用可被静态检查识别的限定写法（行为不变）（gofer-1sqv）。
- 工作台支持 `?job=<id>`：Sessions 页新建的 ACP 会话还没拿到 session_id 时，跳到工作台后会等该 job 出现并聚焦，不再停在原来的会话（gofer-k5d4）。

## [0.138.1] - 2026-10-11

### 修复

- 受管升级（`gofer serve upgrade`）的排空不再被 worker 上空闲的 ACP 持续会话挡住：等待输入、没有排队消息的 worker 会话与本机会话一样不计入在飞 job，server 重启后由 worker 重连接管，会话可继续 `say`（gofer-47ey）。
- `gofer serve upgrade` 开始时立即打印 `upgrade_id=… phase=draining`，排空期间每 30 秒打印进度，不再在排空的几分钟里没有任何输出；server 日志新增 `upgrade.drain_waiting` 事件，列出排空在等的 job（gofer-47ey）。
- 会话开场注入（`gofer repo prime`）在较慢的机器上不再丢掉服务端段落（全局 / 项目记忆、进行中 plan）：服务端请求的 1.5 秒预算改为在本地部分（含「当前重点」）算完后才开始计时。

## [0.138.0] - 2026-10-11

### 变更

- **gofer-usage skill 重写为面向任意项目的使用指引**（gofer-8g1t）：SKILL.md 从约 197KB 压到约 15KB，以「开发流程速查」为骨架，只放最常用的入口；进阶与运维细节按主题拆到 `references/`（job 进阶、plan 与 workflow、会话交给 web、tracker、工作项、web 控制台、传文件与唤醒、排障、运维、待批 job），去掉内部实现、路线图编号与版本考古；HTTP 接口移到 `docs/reference/http-api.md`。已装 skill 的机器重跑 `gofer init skill` 更新。
- `gofer issue brief` 的验证命令改为按项目配置：`.gofer/tracker/config.yaml` 的 `brief.verify: [{paths, cmd}]`（`cmd` 支持 `{dirs}` / `{files}`）；不配置时只在有 `go.mod` 的仓库给 Go 的构建与测试命令，不再对所有项目注入 `-race`、跨平台 vet 与前端三命令。「提到的文件」识别更多语言的源码，关键符号支持 JS。
- `gofer repo init --commit-policy local-commit|ask|none`：AGENTS.md / CLAUDE.md 托管块的提交说明按 `commit_policy` 生成，与会话开场注入一致（默认仍是 local-commit）。
- 会话开场注入：`gofer work report` 提示只在装了会话中继 hook 或本项目有未结工作项时出现；「当前重点」的服务行只统计服务本项目的 worker，不再列出 server 上其他 worker 的名字；新开关 `prime.focus_env`（默认开）与 `prime.focus_tag`（「最近 tag 之后 N 个提交」，默认关）。
- `gofer init server` 生成的示例配置：注释统一中文，去掉内部编号，示例项目名改为中性名；workflow 内置模板 plan-implement / review-committee 的提示词统一中文，plan 示例不再偏向 Go。
- 示例任务书模板（`docs/examples/templates`）改为不绑定语言与 agent 的通用版。

- 内置 claude agent 默认带 `from_session_args: [--resume, "{{from_session}}", --fork-session]`：`job run --from-session` 与 Web「从此会话新开」对未自定义的 claude 直接可用——分叉源会话，新会话 id 照常由 `--session-id` 注入，源会话不动；显式配置的 `from_session_args` 优先。

## [0.137.0] - 2026-10-11

### 新增

- 待批 job（gofer-9b1b）：`gofer job run --hold [--hold-reason "…"] [--hold-timeout <秒>]` 提交后停在新状态 `awaiting_approval`（非终态，不派发、不占并发名额与目录锁，server 重启后仍待批），人批准才转 `queued` 照常执行（同一 job id）；拒绝、超时（`server.hold.default_timeout_sec` 默认 24h，上限 `max_timeout_sec` 默认 7d）或提交者 `job cancel` 撤回都落 `cancelled`，命令从未执行（error `hold rejected by <who>[: <理由>]` / `hold expired`）。用于 agent 被自身权限拦下的对外 / 不可逆操作（如 `git push`）：
  - 人批准：`gofer job approve <id> [--note]`（在 agent 会话环境里直接拒绝，不留绕过开关）、`gofer job reject <id>`（待批时理由可选，`--reason` 同 `--note`）；HTTP `POST /v1/jobs/{id}/approve`、`/reject`（worker token 与 job 凭据 403，开了 `governance.require_answer_capability` 要 `can_answer`）。MCP `gofer_run_job` 支持 `hold` / `hold_reason` / `hold_timeout_sec`，不提供 approve 工具。
  - Web：job 详情页首「⏸ 等你批准」面板——理由、完整命令（exec 的 argv / agent 的 prompt 开头，手机宽度折行不横滚）、项目 / cwd / runner / agent、提交者、过期倒计时与附带属性，「批准」一键、「拒绝」可附理由，手机上按钮贴底；「今天」与「待我决策」出「待批准」卡（批准 / 拒绝 / 看详情）；看板加「⏸ 待批准」过滤，`/review` 页头有「待批准 N →」入口；徽标、统计与事件时间线认得新状态和 `job.awaiting_approval` / `job.hold_approved` / `job.hold_rejected` / `job.hold_expired`。
  - 通知与回流：`job.awaiting_approval` 进默认通知集（IM「待批准：<标题>」带理由、命令前几行、过期时间与「去批准」链接，Web Push 高优先级）；提交会话的完成通知对 `cancelled` 附 `reason=hold rejected…` / `reason=hold expired`。
  - 提交输出 `job <id> submitted: status=awaiting_approval expires_at=…` 与 `awaiting approval: <web>/jobs/<id>`；`--sync` 自动转异步，`--wait` 等待窗口为 hold 超时 + job 超时。批准时校验请求摘要（等待期间请求变了返回 409）；hold 跟着请求走，`job rerun`、重建、自动重试、`job resume` 会重新待批。`--session` / `--interactive` / workflow 步骤不能 hold。

### 修复

- 测试：3 个「子进程退出前没人读 pty」的回归测试在 macOS 上跳过——darwin 上会话首进程退出要等终端输出被读走，该场景不存在，测试会死锁（gofer-auat）。

## [0.136.0] - 2026-10-10

### 修复

- 交互式（pty）job 在 Linux / macOS 上，子进程打印后立即退出时，最后一段还没被读走的输出不再丢失（macOS 上短命 job 的输出可能整段丢）：子进程自然退出后先把 pty 缓冲读到 EOF 再关 master（最多等 2s），且 server 自己持有 slave 端直到子进程回收，避免 macOS 在 slave 最后一次关闭时清空未读输出（gofer-auat）。
- Web 工作台：worker 上的 ACP 会话（含 `awaiting_input` 多轮）不再一直显示「加载结构化记录…」。worker 现在把 `artifacts/acp.jsonl` 经日志通道镜像到 server（协议 v22，新 `log` 流 `acp`），`/v1/jobs/{id}/acp/stream` 对 worker job 实时出事件；worker < v22 或 peer-http job 的流里改发一条 `notice` 说明原因，工作台直接显示。**需要升级 worker 才有结构化记录；先升 server 再升 worker**（worker 只向 v22+ server 发该流，旧 server 收到会写进 stdout）（gofer-e2x7）。

## [0.135.1] - 2026-10-10

### 修复

- tracker 锁：多个进程 / 协程持续争用时，等锁一方不再因为一直抢不到、而锁其实在不断易手就报 `tracker lock busy`；现在只有同一持有者占锁超过等待期限才算忙（gofer-rgnw）。
- 常驻传话人投递历史：会话回复的原文已被记下时，传话人自己那一回合的转述不再重复记成一条没有发送方的「收到回复」（gofer-rgnw）。

## [0.135.0] - 2026-10-10

### 新增

- tracker 合并驱动 `gofer repo merge-driver`：并行 worktree / 分支各自改 `.gofer/tracker/*.jsonl`（issues / memories / memories-archive）后 `git merge` 按记录 id 合并，不再出现 jsonl 行冲突——一边的增删改直接采用，删除对修改保留修改，同一 issue 两边都改时逐字段合并（同字段取 `updated_at` 较新者，评论 / notes / 标签 / 依赖取并集），读不了的输入退回 git 文本合并。`gofer repo init`（及 `repo migrate --from-bd --apply`）自动写 `.gitattributes` 与本克隆的 git config；已有克隆运行 `gofer repo merge-driver --install`（TRK-09，gofer-7rqx）。

### 变更

- web 经传话人发给终端会话的消息，会话用 SendMessage 回给传话人的回复，现在由会话自己的 gofer hook（`PostToolUse`）原文上报，直接显示在 web 该会话的对话里（「<会话名> · 回复」），不再由传话人转述；新接口 `POST /v1/sessions/{sid}/replies`，outbox 记录新增 `direction` / `source` / `peer` / `reply_to`。hook 没报上来时，server 本机的常驻传话人用它收到的原文兜底（gofer-6er0）。
- 传话 prompt 要求传话人收到会话回复时只回「已收到」、不再转述；Runners 页传话人抽屉的投递历史新增「收到回复」行（gofer-6er0）。

### 修复

- Web 工作台：回复完一个会话再切到另一个会话后，推送刷新不再把焦点拽回之前的会话（移动端也不再被拉回对话栏）。通知 / 今天 / 会话页带 `?thread=` 的链接只定位一次，随后从地址里去掉；新建会话的自动定位在用户自己选了别的会话后作废；在会话里回复不再触发重新定位（gofer-2seo）。
- 常驻传话进程收到会话发来的消息时会自己开一个回合，它的结果曾被下一条传话当成自己的结果读走（下一条传话 job 输出变成上一条回复的转述，此后每条错位一拍，连续两条还会卡住读取）；现在只有正在等待的请求能拿到结果（gofer-6er0）。

## [0.134.0] - 2026-10-10

> 测试稳定性根治（gofer-r7am）：Windows 全量 ×3 + Linux race 全量 ×2 零失败，期间修复 10 个生产时序 bug。

### 新增

- 以 MIT 许可证发布（新增 `LICENSE`）。
- gofer-usage skill 开头新增「★ 开发流程速查」：新会话从接手（brief）到立项、plan / todo、派活、并行、验收、记忆、收尾的常用路径（`gofer init skill` 安装的副本同步更新）。

### 变更

- 优雅停机时本机执行的 job 会被取消并等其收尾（Unix 也会杀掉进程树），后台写入完成后才关库；worker 上的 job 不受影响（gofer-r7am.1）。
- 删除刚结束、尚在收尾的 job：最多等待 2 秒，仍未完成返回 409（此前可能误报「not terminal」或留下孤儿记录）（gofer-r7am.1）。
- `gofer memory doctor` 末行计数由 `flagged N` 改为 `with findings N`（统计有检查发现的条数，避免与 `memory flag` 的「待复核」标记混淆；JSON 字段仍叫 `flagged`）。

### 修复

- 隧道一端正常结束时，另一端也按正常关闭握手收尾，不再被硬关（gofer-r7am.2，C2）。
- 常驻传话进程处理请求期间暂停空闲计时，长请求不再被中途杀掉（gofer-r7am.2，C3）。
- worker 上「刚派发就取消」的取消请求不再可能丢失（gofer-r7am.2，C4）。
- 被领养的 job 等待结束时不再永久阻塞（gofer-r7am.1）。
- 配置 reload 回执在 Windows 下目标文件被占用时有限次重试（gofer-r7am.3）。
- work / steward 服务停止时等待后台任务结束；pty 会话 id 捕获改为增量扫描，长输出下不再越来越慢（gofer-r7am.4）。
- Web 终端查看端在输出推送时断开，不再可能触发向已关闭 channel 发送导致的 panic（ptyrelay）。
- 本机 pty 中继纳入 job 后台任务，停机时 pty 会话记录能写完（不再停在 open）（gofer-r7am.5）。
- 接手包不再漏掉兄弟 issue 关闭说明里全是数字的短 hash（gofer-r7am.5）。
- tracker 锁在 Windows 上遇到「删除尚未完成」时等待重试，并发的 tracker 命令不再偶发失败。

## [0.133.0] - 2026-10-10

### 新增

- 普通（非 worktree）job 的 `changes.diff` 与范围检查也包含该 job 自己产生的提交（committed 段）（gofer-3nxa.6）。
- `gofer memory doctor --global / --project` 覆盖作用域记忆，并检查 30 天未处理的经验候选（gofer-3nxa.8）。
- 接手包（`issue brief` / `plan brief`）增强：先列 issue 正文点名的文件、从相关提交列出关键 Go/TS 符号（`file:line`）、按代码入口给出针对性的验证命令、issue 中的方案评论置顶全文、缺验收标准时提示。

### 修复

- MCP client 模式下作用域记忆保留 kind / summary / when / flags 等元数据。

## [0.132.0] - 2026-10-09

### 新增

- 接手包：`gofer issue brief <id>` / `gofer plan brief <id>`（MCP `gofer_issue_brief` / `gofer_plan_brief`），一次拿齐上下文树、设计稿、相关提交、job / plan 与适用记忆（TRK-07）。
- 记忆过时反馈：`gofer memory flag|unflag`（仓库与 `--global/--project`，job 凭证也可 flag），MCP `gofer_memory_flag`，注入时标「⚠ 待复核」（TRK-06）。
- 交付后知识提炼：项目 `knowledge_capture` 打开后，job 报告的「## 可复用经验」记为经验候选，`gofer memory candidates|accept|reject` 由人决定是否写入作用域记忆（TRK-08）。
- 方案规则注入 `plan-implement` 的 planner，新增 `gofer plan import` 把 ```` ```gofer-todos ```` 块导入为 todo 链（PLAN-08）。

### 修复

- 删除刚结束的 job 时同时清掉内存中的条目。

## [0.131.0] - 2026-10-09

### 新增

- 验收标准贯穿：todo / job 的 `acceptance`（`job run --acceptance`）注入 prompt「## 验收标准」并在验收面板与 `job review` 中展示（GATE-03）。
- 范围纪律：项目 `scope_discipline` 追加「## 交付约定」，`job run --scope` 声明改动范围，越界文件标「范围外」（`--no-scope-discipline` 关闭）（GATE-03）。
- 报告中的「发现但不碰」解析为 findings：验收面板「发现」页签与 `gofer job findings [--create-issues]`。

## [0.130.2] - 2026-10-09

### 修复

- 空的 `expires_at` 不再阻止写入作用域 rule / note 记忆。

## [0.130.1] - 2026-10-09

### 变更

- Dashboard 增加「今天」区间（按小时序列），默认区间改为 7 天；活动卡片显示 job 状态。

## [0.130.0] - 2026-10-09

### 新增

- Dashboard 重做为统计墙：`GET /v1/stats/overview`、`job_metrics` 侧表，历史 job 用 `gofer tool stats-backfill` 补算（gofer-yelm）。
- Web「从此会话新开」入口（对应 `job run --from-session`）。

### 修复

- tracker 事务出错时总是回滚。
- macOS 可编译（servicemgr 升级钩子为不支持桩）；全新默认工作空间可通过校验；会话 cwd 经符号链接也能匹配。
- 监督会话的用量只记到一个 plan 上。

### 变更

- 依赖升级：gookit cliui v0.5.3、goutil v0.8.1、rux v2.1.2。

## [0.129.0] - 2026-10-09

### 新增

- 新会话开场（`repo prime`）：自动生成「当前重点」段，规则全文 + 分组记忆索引，按段预算裁剪。
- 记忆增加 `kind` / `summary` / `when` / `ttl` / `source` 字段；`memory doctor`、`archive|restore|promote`；`issue ls --stale`、`issue create --from`。
- hook 按关键词在人工提问时注入相关记忆，在 shell 工具调用前注入命令相关记忆。
- Claude Code 终端的权限确认弹窗镜像到 web（「今天」页与会话抽屉），可在 web 上作答。
- `plan create` 自动把当前 agent 会话绑定为计划主会话；主会话用量计入 plan。
- 「今天」页记忆整理卡片：管家在 server doctor 有新发现时提议清理，人工采纳。

### 修复

- 注入的记忆块标注为参考数据而非用户指令。
- 权限弹窗内容在服务端也做脱敏。

## [0.128.2] - 2026-10-09

### 修复

- tracker 同步携带已知的 server rev，过期写入作为冲突返回；一次性修复已分叉的记录。

## [0.128.1] - 2026-10-09

### 新增

- 远程停止外部隧道转发进程：`gofer tunnel stop`、Tunnels 页停止按钮（转发端收到 410 后退出）。

## [0.128.0] - 2026-10-09

### 新增

- 「今天」页卡片延后（按时间、job 结束或有动静时唤醒）与专注模式（N3）。
- 决策卡上显示管家建议，「按建议」一键处理（N3 T4）。
- `job run --from-session <job>`：从旧会话继承上下文开一个新的 agent 会话。

## [0.127.1] - 2026-10-09

### 变更

- 只有「等人」的工作项才会因阻塞描述被判为阻塞；relay `on` 回落为 `auto` 时给出说明；空闲会话发送按钮改名「送到会话」。

## [0.127.0] - 2026-10-09

### 新增

- Web 首页「今天」：待我决策队列（交互 / decision / 待验收 / 等我）、摘要与状态条，`GET /v1/today`；并行泳道 `GET /v1/today/lanes`；全局「待我决策」浮层（WEB-17）。
- 工作项日志分级（里程碑 / 细节，`?level=`），卡片只显示里程碑，泳道健康度与 `work.stall_after`（WORK-06）。

### 变更

- tracker 托管说明引导 agent 用 `gofer repo status --changed` 查看变化，而不是 diff jsonl。

## [0.126.1] - 2026-10-08

### 修复

- web 同步在 tracker `project_key` 不是已登记项目时回落到按路径匹配（此前返回 409）。

## [0.126.0] - 2026-10-08

### 变更

- tracker 改用短 id，旧 UUID id 确定性迁移。
- 内置 `claude` agent 以 ndjson 方式读取 stream-json 输出。

### 修复

- job 中的裸 `gofer` 程序只在运行中的二进制确为 gofer 时才解析为它自身（`GOFER_BIN`）。
- 采纳任一整理建议即把草稿移出「未分类」。

## [0.125.0] - 2026-10-08

### 新增

- 会话催办：`gofer session nudge`（按间隔或「N 分钟无进展」给终端会话送话），会话抽屉中可配置（SESS-12）。

### 修复

- 重复的 claude 消息用量按增长量计数。

## [0.124.0] - 2026-10-08

### 新增

- 终端会话用量：从 transcript 增量采集 token，按主会话 / 子 agent 拆分并在 web 显示（OBS-14）。
- 预算熔断：`job run --max-tokens / --max-cost / --max-turns`（及 agent / 项目 `budget:` 默认），超限杀进程树、`failure_class=budget`、发 `job.budget_exceeded`；远程 worker 需协议 v20（GATE-02）。
- Web Issues 页「同步」：server 在仓库所在 runner 派 `gofer repo sync`（TRK-05）。

### 修复

- 同步提交的 HTTP 超时大于 server 等待上限；git 操作移出提交响应路径。
- peer 终态帧之后继续镜像日志。

## [0.123.0] - 2026-10-08

### 新增

- `job run --model` 与 agent `model_args`（内置 claude / codex；ACP 走协议选择），web 表单可选模型；worker 需协议 v19（AGT-06）。
- Stop 等待感知会话内子 agent，server 按 relay 模式下发等待预算（SESS-10）。
- `deliver_offline_match` 与内置 codex queue 送话（SESS-11）。
- Plan 详情显示并可直接联系绑定的主会话（PLAN-06）。
- `gofer repo status --changed` 列出 tracker 相对 HEAD 的增删改。
- 摘要 / 提醒 / 等我通知没有任何 webhook 订阅时告警（OBS-13）。

### 修复

- `work ls --all` 与 `work rm --status` 计数口径一致；reload 回执按 `reloaded_at` 判断。

### 移除

- 旧的 Windows `start.ps1` / supervisor / selfupdate 脚本（由 v0.120 的原生 serve 管理取代）。

## [0.122.0] - 2026-10-08

### 新增

- 删除已结束的工作项：`gofer work rm`、`DELETE /v1/work-items/{id}`、web 抽屉删除按钮（WORK-05）。

## [0.121.1] - 2026-10-08

### 变更

- serve 升级接受 UPX 压缩的候选二进制（通过自报构建信息校验）。

## [0.121.0] - 2026-10-08

### 变更

- plan job 只绑定经认证的显式来源会话；会话登记与心跳归属原子化（SESS-09）。
- hook 中继把 `injected` 标记与 job 完成前缀视为非人工输入。

### 修复

- ACP job 保留工具 diff 与元数据中的用量。

## [0.120.1] - 2026-10-08

### 修复

- Windows 受管 supervisor 的控制台窗口在 detach 前隐藏。

## [0.120.0] - 2026-10-08

### 新增

- 原生受管 server：`gofer serve register|start|stop|restart|status|logs|uninstall|upgrade`（Windows 登录任务 + supervisor、Linux systemd），升级交独立执行者并持久化结果（SVC-05）。
- tracker 离线仓库迁移（带来源守卫）。
- 配置目录模板 `{config_dir}` 在使用时解析。

## [0.119.0] - 2026-10-07

### 新增

- 通用 agent 接入：`gofer hook generic --agent <key>`、`transcript_dialect`、`ndjson_usage_path`、`inject_process`、`session_family`、`deliver_command`（协议 v18）；web 送话可达非 Claude 会话（AGT-05）。

## [0.118.0] - 2026-10-06

### 变更

- tracker 提交策略写明：push 需要用户授权。
- cli-agent 续接保留源 agent 的输出投影与环境。

## [0.117.0] - 2026-10-06

### 新增

- Issues 页分页、多选与批量关闭 / 改状态 / 打标签（`POST /v1/tracker/issues/batch`）；`issue close|update` 接受多个 id（TRK-04）。

### 修复

- 客户端节点上 `repo init / migrate` 经 server 匹配 `project_key`；bd 迁移的活动守卫忽略 gofer 自身子进程。

## [0.116.1] - 2026-10-06

### 修复

- 手动 `repo sync` 不再沿用 2 秒的自动同步超时。

## [0.116.0] - 2026-10-06

### 新增

- Issues 页父子树、进度与依赖关系（TRK-04）；会话卡片 / 抽屉 / `session ls` 显示 Claude Code 会话名。

### 修复

- relay 关闭时 Stop 仍等待被盯的 job 并补发未送达通知；bd 迁移 gate-lock 不再误判为近期写入；`repo init / migrate` 报告匹配到的项目。

## [0.115.0] - 2026-10-06

### 新增

- issue / memory 命令对齐 bd 日常用法（字段更新、comment、reopen、`dep rm|ls`、列表筛选、关系展示），`repo migrate --from-bd` 加固（TRK-01 X1）。
- plan decision 驱动「等我」、完成回写建议、`work.needs_me` 通知；管家带话 `gofer_session_ask` 与只读 issue MCP 工具（WORK-04）。

## [0.114.0] - 2026-10-06

### 新增

- 工作项转 plan todo（`gofer work to-todo`）；ACP / pty 会话卡片与工作项互链；导航调整与 Works「等我」徽标（WORK-04）。

## [0.113.0] - 2026-10-06

### 新增

- 管家（steward，默认关闭）：常驻 ACP 会话，只整理工作项；专用凭据白名单、自动注入 gofer MCP、版本化笔记、每日巡检、合并建议；`gofer steward …`、`/v1/steward*`、Works 页「问管家」（WORK-03）。

## [0.112.1] - 2026-10-06

### 修复

- worker 重连不再改写已完成的升级记录（升级耗时显示异常）。

## [0.112.0] - 2026-10-06

### 修复

- Windows 上后台 server / worker 启动的非交互子进程不再弹出控制台窗口（SVC-04）。

## [0.111.0] - 2026-10-06

### 新增

- 未声明 `default` 项目时注入内置 `default` 项目（默认工作空间，仅本机 runner，标「内置」、不落盘、不可删除）（CFG-15）。
- 整理器项目解析顺序：`work.summarizer_project` → 工作项项目 → `default`。

## [0.110.0] - 2026-10-06

### 新增

- 工作项二期：字段级发言者标注、请求账本（请会话汇报 / 写交接）、被动整理（读 claude / codex / omp transcript 尾部，worker 协议 v17）、整理建议采纳 / 忽略与成本上限、设置页 `/settings/work`（WORK-02）。

## [0.109.1] - 2026-10-05

### 修复

- 自动中继等待条件失效时关闭等待轮次（web 显示已收到但未送达会话）。

## [0.109.0] - 2026-10-05

### 新增

- 工作项一期：会话首次提问自动建草稿、状态自动映射且人工优先、搁置 / 提醒 `work.remind`、每日摘要 `work.digest`、合并 / 拆分；`gofer work`、`/v1/work-items`、MCP、「工作」页（WORK-01）。
- Sessions 页卡片化；serve / worker 启动时自动创建默认工作空间目录。

## [0.108.0] - 2026-10-05

### 变更

- Runners 页 worker 心跳实时刷新；runner 工作目录只列允许该 runner 的项目。

## [0.107.0] - 2026-10-04

### 移除

- 内置 `tty-claude` / `tty-codex` agent：交互改用 `claude` / `codex --interactive`，旧 tty job 续接自动映射。

## [0.106.0] - 2026-10-04

### 变更

- job 日志 SSE 改为事件驱动（空闲连接不再轮询数据库，wire 不变）。
- `codex-acp` 改用 `@agentclientprotocol/codex-acp` 并加入 codex 会话族。

### 修复

- omp 扩展取消等待时先上报中断；全局安装重复提示的路径去重。

## [0.105.0] - 2026-10-04

### 新增

- omp / jcode 接入 gofer hook（omp 完整支持中继；jcode 只上报登记 / 状态 / 结束）；`gofer init hooks --agent claude|codex|omp|jcode|all`，全局安装后提示项目级重复（SESS-08）。

## [0.104.0] - 2026-10-04

### 新增

- job 详情 `?include=` 聚合；浏览器单连接 `/v1/ws` 主题推送，轮询降为断线兜底（WEB-16）。
- 会话超过 `session.offline_after_sec` 无心跳标为离线（仍可唤醒）。

### 变更

- 内置 `claude-acp` 改用 `@agentclientprotocol/claude-agent-acp`。

### 修复

- 中继等待时按 Esc 正确结束等待轮次。

## [0.103.1] - 2026-10-04

### 修复

- 以空行开头的日志不再让 job 详情页卡死。

## [0.103.0] - 2026-10-04

### 变更

- `server` / `local` 成为内置本机 runner 的保留名：自定义 runner 与 worker id 不得使用，仅允许 `{type: local}` 声明（CFG-14，G043）。
- workflow 模板默认 agent 不可用时 web 自动替换、CLI 报出可用 agent。

### 修复

- workflow 完成后步数显示夹取；`.all` 聚合输出超 32KB 时写文件并传路径。

## [0.102.0] - 2026-10-04

### 变更

- `server` / `local` 两种拼写在所有入口统一识别并落规范名（CFG-14）。
- 唤醒会话优先回到经 transcript 验证的原始启动目录；心跳记录 `last_cwd`。

### 修复

- 项目写接口接受内置 `exec` agent。

## [0.101.0] - 2026-10-04

### 新增

- Web 多 agent 对比视图（每路一列、并排 diff、择优合并 merge / squash、清理其余分支，冲突 409 就地展示并复原仓库）（WF-05）。
- 从模板新建 workflow（变量表单与步骤预览）；job 详情「合并到基线」按钮。

### 变更

- Web 中本机 runner 统一显示为 `server`；合并只被已跟踪改动阻塞；squash 合并后识别为已合并。

## [0.100.1] - 2026-10-03

### 修复

- 会话接管 / 唤醒判断目录时使用 worker 上报的项目实际路径。

## [0.100.0] - 2026-10-03

### 新增

- 传话人可见：Runners 页状态、最近投递、stderr、可见会话对照（worker 协议 v16）（SESS-07）。
- 会话唤醒（含已结束会话）：`gofer session resume`、Sessions 页与会话抽屉的唤醒按钮。
- runner 工作目录展示（默认工作空间 `GOFER_WORKSPACE` / `~/.gofer/workspace`、roots 映射、项目解析路径）。

### 变更

- server 本机会话改走常驻传话人；传话人启动目录兜底为 会话目录 → 默认工作空间 → 家目录。

## 早期版本

v0.1.0 – v0.99.x 按主题区间压缩记录。每段列出该区间内落地的主要能力，细节见路线图与 [`docs/roadmap-history.md`](docs/roadmap-history.md)。

### [0.90.0 – 0.99.1] - 2026-10-01 → 2026-10-03

- 可选 HTTPS 额外监听 `server.tls` 与 `gofer tool cert`（PWA 用）（CFG-11）；手机端会话视图与「无需回复」。
- 常驻传话人扩展到 worker（协议 v14），并下沉为独立模块（SESS-05/06）。
- worker 策略下发超时主动重推 `server.policy_repush`；配置热重载补全：`gofer serve reload`、`worker reload --local`、需重启键清单（CFG-12）。
- worker 一键接入：`gofer worker add|remove`、`POST/DELETE /v1/workers`、`gofer init worker --admin-token`、Runners 页添加 worker（CFG-13）。
- 远程升级 worker：`gofer worker upgrade`（协议 v15，排空交接、失败回滚）（SVC-02）；传话 job 默认隐藏（JOB-15）。
- IM 正文长度可配 `max_text_runes`（OBS-07e）；`gofer job redact` / `job delete`；`gofer job secret-scan` 跨 job 查找与批量脱敏；提交时秘密形态提示（SEC-03/04）。
- 多 agent 对比择优与流程模板：workflow `agents[]` 异构扇出、每路 worktree、`join: pick` + `gofer wf pick --merge`、内置模板 `compare` / `plan-implement` / `review-committee`（WF-05）；`job resume --mode --agent`（JOB-16）。

### [0.80.0 – 0.89.0] - 2026-09-29 → 2026-10-01

- 远程 job 排队时间不计入超时（JOB-13）；job 详情日志渲染性能。
- 全局 / 项目作用域记忆（`memory --global/--project`，`agent:<名>` 标签注入）与 MCP memory 工具（TRK-02）。
- `--lock-wait` 按 job 设置等锁上限（JOB-14）；`repo prime` 精简（TRK-03）。
- pty 取消先发 `exit_keys` 优雅退出以捕获会话 id（PTY-02）；job 标题可编辑（`job set --title`）。
- ACP 持续会话 job：`job run --session`、`job say`、`job end`、`awaiting_input`（ACP-03）；远程 worker 持续会话（协议 v13）（ACP-04）。
- web 给终端会话发消息（SESS-04）；持续会话「等你回复」提醒（OBS-12）。
- `gofer worker show|projects` 与策略诊断；以会话为中心的工作台（WEB-11b）。

### [0.70.0 – 0.79.0] - 2026-09-28 → 2026-09-29

- 续接 job 记录原 agent `resume_agent`（JOB-12）；plan 版本化交接说明（PLAN-04）。
- Web 按预设启停 server 托管隧道（TUN-05）；Stop 等待盯住本会话派出的 job、`gofer session watch`（SESS-03）。
- tracker server 镜像与三方合并同步、`job run --issue`、Web Issues 页（TRK-01 P4）。
- 目录锁细化 `--lock <path>`、`dir_lock_mode: repo`（JOB-11b）；workflow 终态原子收尾；续接复用 agent `global_args`。
- exec job 默认不采集 diff（`capture_diff: auto|on|off`）；plan 标签与过滤（PLAN-05）；命令帮助去掉计划编号（CFG-10）。

### [0.60.0 – 0.69.0] - 2026-09-23 → 2026-09-27

- 凭证补漏：job 环境去掉 `GOFER_CONFIG_DIR`、job 内 CLI 不读 `.env` token、PATH 前置运行它的 gofer（SEC-01 F10）。
- 设置页二级菜单、隧道在 web 可见与 server 侧预设（WEB-12、TUN-03/04）；ACP 真机验收修复（ACP-02）。
- 强制规则库与注入（JOB-06①）；`gofer worker init` 向导（CFG-05）；默认工作空间 `~/.gofer/workspace`；`job run --env`。
- Web 工作台 W1–W3：会话中枢、分屏 / 标签页布局、PWA + Web Push、ACP 对话视图、会话改动视图与行内评审（WEB-11）。
- 未提交改动守卫 `on_uncommitted`（GIT-01）。
- 仓库本地 issue / memory：`gofer repo init|prime|sync|migrate|status`、`gofer issue`、`gofer memory`（`.gofer/tracker/*.jsonl`），SessionStart 注入 prime，bd 迁移（TRK-01）。

### [0.50.0 – 0.59.0] - 2026-09-21 → 2026-09-23

- Windows 桌面会话常驻：`serve -d` / `worker -d` / `stop` 的 Windows 实现（SVC-01，后被 SVC-05 取代）。
- 会话捕获兜底：未配 `session_capture` 的 cli-agent 也能续接（AGT-04）。
- job 级可靠重试：落库 + 租约 sweeper、四级策略、`job run --retry`、`job retry ls|cancel`（AUTO-03）。
- web 编辑 agents / server 配置（WEB-04③）；前端升级后自愈（F8）。
- skills 绑定：server 侧 skill 库、`gofer agent skill`、四级并集绑定（JOB-10）。
- 评论层与 `@agent` 提及派活、leader 回合（MCP-05）；job 作用域凭证（SEC-01，协议 v11）；leader 逐 plan 开启（LEAD-02）。

### [0.41.0 – 0.49.0] - 2026-09-16 → 2026-09-21

- worker 断线恢复 `recovering` 与 serve 重启认领（RECOV-01）；`job run --worktree` 托管 worktree（WT-01）。
- 纯客户端模式 `GOFER_RUN_MODE=client` 与 `gofer init client`（CFG-07）。
- 会话中继：空闲自动布防、三态 `relay_mode`、tmux 送话、`--resume` pty 接管（SESS-01/02）。
- 供应商类错误自动续投（AUTO-RES）；agent 故障转移 `fallback_agents`、健康度与 `agent status|probe`（AGT-03）。
- ndjson 输出投影（OBS-10）；`acp-agent` 类型（ACP-01）；审批门与 `needs_review` 人工验收（GATE-01）；验收台 `/review` 与 `gofer job review`（REV-01）。
- `job run --verify`（JOB-08）、`--todo` 联动、用量 / 成本采集（OBS-09）、任务书模板 `-t/--var`（JOB-02）。
- 文件传输 `gofer tool cp` / `tool xfer`、`--upload` / `--collect`（XFER-01/02）。
- 同目录串行 `waiting_dir`（JOB-11）、输出停滞检测（AUTO-05）、todo 指派即派发（PLAN-02）、job wakeups（JOB-09）。
- pty 转录与会话续接（PTY-01）；todo 依赖链与 `plan run|pause|resume`（PLAN-03）；计划看板（WEB-10）；容器 worker 与 `gofer worker doctor`（CFG-09）；schedule webhook 触发（AUTO-02b）。

### [0.31.0 – 0.40.0] - 2026-07-12 → 2026-09-15

- MCP `--project` 作用域；worker 协议版本与能力上报，提交前按执行 runner 能力校验。
- 内置 agent 模板；worker 配置远程 reload；worker POLICY 模式（server 下发策略，worker 只做 `roots` 映射与 `guards` 收紧，协议 v4）。
- gofer-usage skill 与 `gofer init skill`。
- plan todo 生命周期与决策通道（`gofer_ask_human`、`plan ask|answer`）。
- 终端会话 ↔ web 消息中继：`gofer hook`、`gofer init hooks`、`gofer session`（SESS-01）；钉钉 / 飞书通知（OBS-07a）。
- TCP / UDP 隧道经 worker 转发、命名预设（TUN-01/02）；结构化 JSONL 文件日志（OBS-11）。
- agent 双模式 `args` + `interactive_args`、项目 `allow_interactive`（AGT-02）；job 超时上限可配（JOB-TO）。

### [0.20.1 – 0.30.0] - 2026-06-29 → 2026-07-11

- 监督 agent 自动应答完善：owner-first 升级路由、派生作答白名单、按需派发（MCP-01）；presence 与 role 寻址完善。
- 内置 cron 调度 `gofer schedule`（AUTO-02），含一次性 / 延迟任务。
- Web 控制台 v3：导航重构、Dashboard、`/v1/stats`、Drivers / Inbox、web 应答交互（WEB-06–09）；web 配置管理与项目编辑（WEB-04）。
- 浏览器 pty attach：交互 job、worker 端 pty 隧道、加密录制（WEB-03）。
- `gofer plan` 编排与 todo（PLAN-01），job 血缘、快速重建与请求脱敏。

### [0.11.0 – 0.20.0] - 2026-06-21 → 2026-06-28

- job 事件时间线（OBS-02）；webhook 通知（白名单 / SSRF 防护 / HMAC，OBS-03）。
- 多步工作流 v1 + v2：`${steps.N.*}` 引用、重试、fan-out / join、子工作流、导入导出（WF-01–04）。
- Prometheus `/metrics`（OBS-05）；按 caller 配额与限流（OBS-06）。
- 配置简化：全局单 server + 项目瘦配置 `.gofer.project.yaml`、cwd 推断项目、`path_view`（CFG-04）。
- `serve -d` / `worker -d` 后台运行与 `serve stop` / `worker stop`。
- agent 会话捕获与 `job resume`（JOB-07）；提交来源追踪（OBS-08）；`GOFER_RUN_MODE` 与 `project list --remote`（CFG-06）。
- Web 只读层：项目 git 状态、子仓与关键文件、产物预览、集群拓扑（WEB-01/02/05）。
- `gofer mcp` client 模式（MCP-02）；presence / 信箱（MCP-04）；角色预设 `--role`（MCP-03）；监督 agent 分层自动应答（MCP-01）。

### [0.1.0 – 0.10.0] - 2026-06-16 → 2026-06-21

- 首个版本（当时名为 `agent-bridge`）：项目与 agent 注册表、异步本地 job、`/v1` HTTP API、job CLI。v0.5.0（2026-06-18）起模块、二进制与环境变量改名为 `gofer`。
- SSE 实时流、内嵌 Web 控制台（看板、job 详情、实时日志、提交表单、明暗主题）。
- peer-http runner；stdio MCP server；运行中交互（`pending_interaction`）。
- SQLite 元数据存储与 retention；多 caller token、SIGHUP 热加载、日志轮转、`request_id` 幂等提交。
- WebSocket 远端 worker（hub、心跳、重连、`/v1/runners` 名册）与按标签调度。
- 同步提交与 md+yaml 任务文件；产物、结构化结果、diff 捕获（JOB-01/04、OBS-01）；job 标签与过滤（JOB-03）。
- `job list|watch|rerun`、shell 补全、`gofer init`、`gofer config validate`（CFG-01/02）。
