# gofer web 控制台改版（视觉 + 布局 + zh/en）设计

> issue: gofer-hez1 ｜ 状态：0.2 已批准（2026-10-23）｜ 2026-10-23
>
> 预览：单文件 HTML，放在作者的工作区 tmp 下、不入库（示例数据含具体项目名，按 G031 不进本仓库）；评审时由作者提供。顶部可以切换页面、桌面/手机、浅色/深色、中文/English。
> 参考：suga 的初稿 `docs/design/2026-10-21-ui-restyle-and-i18n-design.md`（下文简称「初稿」）。

## 1. 背景

- 同事反馈现在的控制台「太丑」。用户要一套明显更现代、更好看的界面，PC 和手机都要好用，并支持中英文切换。
- 2026-10-21 suga 出了初稿和一个静态 demo。2026-10-22 另做了一版独立预览（上面的 `preview.html`），用户的评价是「这版比现在的好看多了」，所以视觉以这一版为准。
- 用户看完预览后又提了三条布局意见：
  - 侧栏不要项目选择器；
  - 计划页只剩看板，看不到进度了；
  - 改版过程中丢掉的信息要补回来。
- 本文把这一版预览、初稿里好的机制、对现有前端的审计结果合成一份完整设计。审计覆盖 `web/src` 的路由、页面与组件。

## 2. 目标与非目标

**目标**

1. 统一视觉：一套 token（颜色、字号、间距、圆角、动效），浅色和深色都要过 WCAG AA。
2. 统一状态表达：job、会话、todo、计划、节点的状态都映射到 7 个「状态族」，用形状加颜色区分。
3. 统一外壳：桌面是左侧导航，平板是图标栏，手机是底部 5 个标签加「更多」抽屉。删掉可拖动的半圆 rail-toggle。
4. 页面层面：每个路由都有明确的规格（见 §7）。补齐被埋没的信息（计划进度、job 详情的结构），合并重复的入口。
5. 界面文案支持 zh/en 实时切换。业务内容（标题、消息、日志、命令、路径、id）不翻译。
6. 能分期落地：每一期都可以单独合并、单独回滚，不需要长期分支。

**非目标**

- 不改 HTTP 接口语义、状态机和权限模型。唯一的例外是 §9.4 提到的「错误码映射」，它是向后兼容的增量。
- 不引入 UI 组件库、Tailwind 或 CSS-in-JS（沿用初稿的非目标）。
- 不做第三种语言，不做 RTL，不做语言包远程下发。
- 不重写业务逻辑。`PlanBoard` 的拖拽、`SessionDrawer` 的交互、`ReviewPanel` 的裁决流程都保留，只换外观和布局。

## 3. 设计原则

1. **黄色只代表「需要人」**。万寿菊黄只用于审批、提问、待验收、等回复、受阻，扫一眼黄色的地方就是要处理的。主色松绿代表「可以操作」。其余状态用冷色。
2. **先给决策，再给统计**。首屏放待我决策和并行中，统计数字退到 Today 的贴底状态条和「统计」页（与初稿 D9 一致）。
3. **一个记忆点：信号带（wire）**。顶栏下面有一条 4px 的分段色带，每段是一个活跃会话或 job 的实时状态，运行中的段有缓慢流光，悬停可以看到名字。其他地方都保持朴素。
4. **形状和颜色一起编码状态**，色弱用户和灰度显示也能分辨。
5. **圆角有层级**：标签 5px，控件 8px，面板 12px，弹层 18px。不让所有东西都是同一个圆角的卡片。
6. **项目是筛选条件，不是全局状态**（§6.4）。
7. **改布局不能丢信息**。每个页面都要对照现有页面列出保留的字段（§7），被挪走的要写明挪到了哪里。

## 4. Design tokens

所有 token 放在 `web/src/styles/tokens.css`。旧变量（`--ink`、`--panel`、`--paper`、`--phosphor` 等）在迁移期保留为别名，指向新 token，迁移完成后删除。

> P0 实施时的命名修正（只两处，其余与上表一致，见文末「实施记录 P0」）：
> - 上表的 `--ink` / `--ink-2` / `--ink-3`（正文三档）在 P0 取名 `--fg` / `--fg-2` / `--fg-3`。原因是旧的 `--ink` 是「页面底色」，全库还有 118 处 `var(--ink)` 当底色用，同一个作用域里不能既当底色又当正文色。P6 删别名时把 `--fg` 改回 `--ink`。
> - 上表的 `--term-bg` / `--term-ink` 在 P0 取名 `--log-bg` / `--log-ink` / `--log-dim`。旧的 `--term-bg` 是**分主题**的终端底（浅色主题下是浅底），新设计里日志查看器两种主题下都是深底；别名语义不同，所以新 token 另起名字。

### 4.1 颜色

| token | 浅色 | 深色 | 用途 |
|---|---|---|---|
| `--bg` | `#F2F4F3` | `#0F1513` | 页面底 |
| `--surface` / `--surface-2` | `#FFFFFF` / `#F8FAF9` | `#161E1B` / `#1A2420` | 面板 / 次级面板、侧栏 |
| `--sunken` | `#E9EDEB` | `#0B110F` | 凹陷区（代码、看板列、轨道） |
| `--line` / `--line-strong` | `#DCE2DF` / `#C3CCC7` | `#25302B` / `#35433D` | 分隔线 / 控件边框 |
| `--ink` / `--ink-2` / `--ink-3` | `#16201C` / `#47544E` / `#6F7C76` | `#E3EBE7` / `#A7B4AE` / `#7C8A84` | 正文 / 次要 / 辅助（三档都 ≥4.5:1） |
| `--accent` / `--accent-soft` | `#1E6B57` / `#DFEFE8` | `#5BC39F` / `#17302A` | 松绿：主操作、选中、焦点 |
| `--need` / `--need-ink` / `--need-soft` | `#E3A21A` / `#8A5A00` / `#FCF0D3` | `#F2B84B` / `#F6C870` / `#2F2513` | 万寿菊：需要人 |
| `--run` | `#2B69B0` | `#74AAE8` | 运行 |
| `--done` | `#1E7A55` | `#59C190` | 完成 |
| `--fail` | `#B6382F` | `#EE7B6F` | 失败、拒绝、受阻 |
| `--time` | `#8653B3` | `#BB96E2` | 超时 |
| `--idle` | `#78847F` | `#8D9994` | 空闲、取消、离线、跳过 |
| `--term-bg` / `--term-ink` | `#18201D` / `#D5DED9` | `#0A0F0D` / `#D2DCD7` | 日志查看器（两种主题下都是深底） |
| `--diff-add(-ink)` / `--diff-del(-ink)` | 见预览源码 | 见预览源码 | diff |

每个状态色都配一个 `-soft` 底色，用于 pill 和卡片底。阴影只有两档：`--shadow-1` 给选中项，`--shadow-pop` 给浮层。普通卡片不加阴影。

### 4.2 字体与字号

- 界面字体：`Schibsted Grotesk`。回退顺序依次为 Segoe UI、-apple-system、Arial，然后是 PingFang SC、Microsoft YaHei、Noto Sans SC。
- 等宽字体：`IBM Plex Mono`。只用于 id、命令、路径、日志、diff，不用于普通标签。
- 字体文件用 `@fontsource` 自托管，不走 Google Fonts，因为 server 常在内网或离线环境。初稿已经把 Plex 放在 `docs/design/fonts/`，同样的做法即可。
- 字号阶梯：11.5 / 12.5 / **14（正文）** / 15.5 / 18 / 22 / 28。Today 的标题用 34。手机正文 15。微字下限 11.5，比初稿的 12 略小，只用于计数和时间戳。
- 数字一律 `font-variant-numeric: tabular-nums`，由基元统一给出（同初稿）。

### 4.3 间距、圆角、断点、动效

- 间距：以 4 为基数，取 4 / 8 / 12 / 16 / 20 / 24 / 32 / 40（`--s1`…`--s10`）。
- 圆角：`--r-chip 5`、`--r-ctl 8`、`--r-panel 12`、`--r-sheet 18`。
- 断点（只保留 3 个，写进 `utils/mobileLayout.ts` 和 CSS 注释；现有代码散落 17 种断点）：
  - `≤760`：手机布局；
  - `761–1099`：64px 图标导航栏；
  - `≥1280`：完整三栏。

  1100–1279 之间是中间档：完整侧栏，但工作台不显示右侧上下文栏。
- 动效：
  - `--ease: cubic-bezier(.2,.7,.2,1)`；短反馈 150ms，展开/抽屉 250ms。
  - 常驻动画只有两处：信号带的流光、运行态圆点的脉冲。
  - 遵守 `prefers-reduced-motion`，开启时全部关闭。

## 5. 状态系统

| 状态族 | 字形 | 颜色 | 包含的状态 |
|---|---|---|---|
| run | 实心圆加脉冲环 | `--run` | job: running、recovering；todo: doing；会话: running/working；workflow: running；计划: open |
| need | 菱形 | `--need` | job: awaiting_input、pending_interaction、needs_review、等审批；会话: waiting_reply、blocked；工作项: needs_me；节点: 待接受的申请 |
| done | 圆加勾 | `--done` | done、accepted；todo: done；计划: done |
| fail | 圆角方块加叉 | `--fail` | failed、rejected；计划: blocked；节点: down |
| time | 时钟 | `--time` | timeout |
| idle | 空心圆 | `--idle` | cancelled、idle、offline、skipped、paused、archived |
| queue | 虚线圆 | `--ink-3` | queued、waiting_dir、todo 的 pending/ready |

- 用 `StatusGlyph`（只有字形）和 `StatusPill`（字形或底色加文字）这两个组件，**替换**现有的 5 套状态表达：
  - `StatusBadge`、`PlanStatusBadge`；
  - `InfoCard.vue` 里全局的 `.sbadge`；
  - `workbench/statusPresentation.ts` 的圆点；
  - Schedules 的类型徽标。
- `Signal` 和 `Heartbeat` 有自己的语义（信号强度、心跳），保留，但改用状态族的颜色。
- 映射表放在 `utils/statusFamily.ts`，状态文字走 i18n 的 `status.*` key（§9）。

## 6. 外壳与导航

### 6.1 桌面（≥1100）

```
┌ 侧栏 236 ───────┬ 顶栏：[搜索/命令 Ctrl K] ……… [待我决策 N] [↯ 新版本] [+ 新建任务] [随手记] [信箱•] [主题] [EN] ┐
│ gofer  ● 已连接  ├ 信号带 ─────────────────────────────────────────────────────────────────────────────┤
│ 今天        4    │ 页面                                                                               │
│ 工作台           │                                                                                    │
│ 工作项      2    │                                                                                    │
│ 计划             │                                                                                    │
│ 任务        10   │                                                                                    │
│ 会话             ├ （仅 /today）贴底状态条：在跑 / 排队 / 今天失败 / 节点在线 / 用量 / 管家 / 版本 ────────┤
│ ─ 协作：信箱 · 待验收 · Issues · 统计                                                                  │
│ ─ 自动化：工作流 · 定时                                                                                │
│ ─ 基础设施：运行节点 · Agents · 项目 · 设置                                                            │
│ 节点：host ● / wsl ● / docker ◐ / nas ○                                                               │
└──────────────────┴────────────────────────────────────────────────────────────────────────────────────┘
```

- 侧栏**没有项目选择器**（用户意见，规则见 §6.4）。
- 侧栏顶部显示实时连接状态：已连接、重连中、轮询、已暂停、离线，数据来自 `api/live.ts`。手机上这条信息并入顶栏的副标题。
- 计数徽标：
  - 黄色徽标（「今天」「待验收」）表示需要人处理；
  - 灰色数字只是数量（任务、信箱）。
- 顶栏铃铛合并：
  - `EscalationBell`（待我决策）和 `EnrollmentBell`（worker 申请）合并为一个「待我决策」，申请作为一种决策卡片；
  - `MailButton` 保留为信箱图标，带待指派的数量；
  - 只读 token 时显示「只读」徽标；
  - 有新版本时显示「↯ vX.Y.Z」，点击进入 设置 › 关于。
- 删除重复的齿轮：现在有导航里的「⚙ 设置」和 `TopbarMenu` 的 ⚙ 两个。
  - 设置入口只留在侧栏。
  - 主题和语言做成顶栏上的两个直接开关。
  - 登出放进 设置 › 关于，也可以从命令面板执行。

### 6.2 平板（761–1099）

- 侧栏收成 64px 图标栏。分组之间用细线分隔，需要处理的计数显示成角标。
- 顶栏的「新建任务」只保留图标。

### 6.3 手机（≤760）

- 顶栏：左边是品牌标加连接状态（如「server 在线，3/4 节点」），右边是搜索和「待我决策 N」。
- 底部标签栏，一共 5 项：**今天（黄色角标）· 工作台 · 任务 · 计划 · 更多**。
  - 「更多」是底部抽屉，里面是其余页面的宫格，以及主题和语言开关。语言开关放在更多里，与初稿 Q1 的建议一致。
  - 与初稿的差异：初稿把「工作项」放进标签栏，本设计放的是「计划」。理由见 §12 的 D7。
- 页面形态：
  - 列表和详情页（任务、信箱、计划）在手机上是两级导航；
  - 工作台的会话列表改成左侧抽屉；
  - 看板一次显示一列，用 chip 切换列；
  - 设置的二级导航改成下拉框；
  - 卡片上的主操作按钮等宽铺满，方便拇指点。
- 删除 `rail-toggle` 和它的拖动、位置持久化逻辑（`utils/navTab.ts`），这一点与初稿相同。

### 6.4 项目规则（取代侧栏项目选择器）

gofer 是多项目的，不存在「当前项目」这种全局状态。

1. **列表页**：
   - 在列表头放一个「项目」筛选 chip，默认显示「全部项目」，点开是底部抽屉或下拉。
   - 筛选值写进 URL 的 `?project=`，这是唯一真源，可以分享、可以回退。
   - 适用页面：今天、工作项、工作台（会话列表）、任务、计划、会话、信箱（按会话所属项目过滤）、待验收、工作流、定时、Issues、统计。
2. **详情页**：始终显示所属项目的 tag，点击跳到「该项目的任务列表」。详情页不提供项目筛选。
3. **新建表单**（新建任务、会话、计划、定时、工作流、随手记）：项目字段的默认值取来源列表当前的筛选值；没有筛选时取上次使用的项目，存在 `localStorage gofer.lastProject`。
4. **命令面板**：支持 `p:<项目>` 前缀，跳到当前页的过滤版本。
5. **「项目」页**：每个项目显示活跃任务数、进行中的计划数、待处理数，并给出跳到各个过滤列表的链接。现在的项目页只能跳到 /board。
6. 现状里各页的过滤机制都不一样：
   - Board 用 select；
   - Plans 把 select 藏在「筛选」面板里；
   - Schedules 只认 URL 参数，界面上没有入口；
   - Workbench 用组件内部的 ref，不持久。

   这些都统一成同一个 `ProjectFilter` 组件，配合同一个 URL 参数。

### 6.5 命令面板

把 `WorkbenchCommandPalette.vue` 提升为全局组件。快捷键：`Ctrl/⌘K`，或在非输入状态下按 `/`。

分组内容：
- 操作：新建任务、随手记、切换主题、切换语言、登出；
- 页面；
- 最近的任务；
- 会话；
- 计划。

支持 `p:` 前缀做项目过滤。Today 现有的全局快捷键 `g d` 和 `z` 保留。

## 7. 页面规格（router.ts 全部路由）

格式：**路由**，然后列出保留的信息、调整、明确不丢的内容。下面「现状」指当前代码。

### 7.1 核心

- **`/today` 今天**（`Today.vue`）
  - 主栏结构：
    - 日期；
    - 标题「N 件事在等你」；
    - 一行摘要：agent 在跑数、空闲会话数、今天完成数；
    - 「自上次打开」折叠摘要，包含完成数、失败数、新提交数（现状的 digest）；
    - 类型筛选 chip（审批、提问、验收、等回复、记忆、worker 申请），旁边是项目 chip；
    - 决策卡片队列。
  - 卡片类型：保留现有全部（interaction、decision、relay、终端权限、review、approval、memory、管家建议、稍后），外加 worker 申请。
  - 卡片结构：每张卡有黄色左边线、类型 pill、agent、等待时长、标题、项目和 job id，以及可以直接执行的主操作，支持撤销。
  - 卡片底部：「专注处理」（FocusMode）、「已稍后 N」、「已处理 N」，以及「含 exec 待验收」开关。这些都是现状已有的，全部保留。
  - 右栏：
    - 「并行中」（TodayLanes）：每条显示进度、agent、id，计划显示 todo 小点；
    - 「刚处理过」。
  - 贴底状态条（替代 TodayStatusBar）：在跑、排队、今天失败、节点在线、用量、管家状态、版本，链接目标与现状相同。
  - 首屏**不放** KPI 四宫格（同初稿 D9）。
- **`/workbench` 工作台**（`Workbench.vue`）
  - 左栏会话列表：
    - 按「需要你 / 活跃 / 空闲与离线」分组，项目 chip 可以过滤（现状是按项目分组）；
    - 支持搜索，状态筛选改成 chip；
    - 保留「显示 exec 命令会话」开关；
    - 每行显示：状态字形、agent、轮数、时长、tokens。
  - 中栏：
    - 头部：标题（可重命名）、状态、未提交徽标、置顶、停止、打开 job 详情、中继三态开关；
    - 元信息行：agent、项目、cwd、轮数、用量；
    - 子视图：「过程 / 改动」切换；
    - 时间线：工具调用默认折叠，审批卡片内嵌在对话中；
    - 输入框：可选发送、排队、打断并发送。
  - 右栏（≥1280）：关联任务、改动文件、计划进度、权限。
  - 新建会话：现状把 `WorkbenchComposer` 放在顶部。改为侧栏的「＋」按钮，打开统一的 `NewSessionSheet`（§10）。
  - 分屏（LayoutPane）和服务端保存的布局保留。
- **`/work` 工作项**（`Work.vue`）
  - 页面标题统一为「工作项」（现状 h1 写的是「工作」）。
  - 保留：按状态/按工作区切换、显示已完成、计数 chip（等我、到期、未整理）、管家合并建议、待整理（草稿和收集箱）、8 个状态列、WorkCard 的全部字段和动作。
  - 新增项目 chip。WorkCard 改用状态族的 pill。WorkDrawer 换成统一的 `Drawer` 外壳。
- **`/plans` 计划总览**（`Plans.vue`）——**按用户意见，必须保留并强化进度**。
  - 每行显示：
    - 状态 pill：进行中、受阻、已暂停、已完成、已归档；
    - 标题和 id、项目、tags、「自动推进」（leader on）；
    - **分段进度条**：按 todo 状态（完成、跳过、待验收、进行中、就绪、待定）着色，旁边写「完成数/总数」和百分比，数据来自 `PlanCompletion` 和 `PlanTodoCounts`；
    - **卡点列**：卡在哪个 todo、N 待验收、N 进行中、N 个 job 失败；
    - 主会话（supervisor）；
    - 最近活动。
  - 筛选：状态 chip（进行中、需要你、已完成、已归档）和项目 chip，关键词和 tag 收进「筛选」。服务端分页（`?offset`）保留。
  - 手机：每行变成一张卡片。
- **`/plans/:id` 计划详情**（`PlanDetail.vue`）
  - 头部：
    - 返回「计划」（现状写的是英文「← plans」）；
    - 项目和 id、标题、状态、主会话（改绑、清除）、暂停/恢复、标记完成；
    - 「更多」菜单：归档、重新打开、在任务列表查看；
    - 大号分段进度条和图例，包含 job 运行数、失败数和最近活动；
    - 描述；
    - **链控制行**：链状态、leader、notify、启动链、暂停/恢复/解除阻塞、帮助。
  - 下方分成几个页签（把现状纵向堆叠的约 12 个区块收起来）：
    - **待办**：「看板 / 列表」切换。看板就是现有的 `PlanBoard` 五列加跳过落点，拖到「就绪」即派发；
    - **任务**（job 表）；
    - **决策**；
    - **交接说明**；
    - **评论**；
    - **事件**。

    用量放在头部的元信息里，附加任务并入「任务」页签。
  - 现状 `dt` 里的英文标签（id、status、owner…）一律走 i18n。
- **`/board` → 改名 `/jobs` 任务列表**（`Board.vue`）
  - 现状叫「任务看板」，但其实是一张表，与真正的看板 `PlanBoard` 撞名。
  - 新路由是 `/jobs`，`/board` 保留重定向，同时带上原来的 query 参数。
  - 桌面：左边列表，右边预览详情（可以拖宽）；手机：列表，点进去看详情。
  - 每行显示：状态字形、标题、状态 pill、agent、项目、runner、徽标（未提交、验证失败、只读、角色/渠道）、信号/耗时、时间。
  - 筛选：
    - 状态 chip 带计数；
    - 项目 chip；
    - 「筛选」抽屉：计划、工作流、tag、agent、runner、调用方、时间范围、含上传、显示传话 job。
    - 全部写进 URL，与现状的 query 兼容。
  - 显示刷新时间。
- **`/jobs/:id` 任务详情**（`JobDetail.vue`，4431 行）——**由一整页长页改成页签结构**，页签可以用 `?tab=` 深链。
  - 头部：
    - 返回（回到来源列表，不再固定回 /board）；
    - 项目和 id（可复制）、标题（可编辑）、状态、agent、runner、耗时；
    - 按钮：打开会话、打开终端、取消或重试；
    - 「更多」菜单：快速重建、合并到基线、释放锁并结束、删除记录。
  - 页签：
    - **概览**：
      - 验收条（needs_review 时）、ApprovalPanel（等审批时）、进度步骤、任务说明、最近 6 行日志；
      - **元信息网格**完整保留：项目、agent、MCP、skills、rules、runner/worker、渠道、计划/todo、工作流、源 job、leader 轮次、调用方、会话及来源、续跑表单与会话链、cwd、模型、目录锁、轮次、结束原因、重试 n/m、scope、验收标准、交付方式；
    - **验收**：现在的 ReviewPanel（汇报、提交、Diff、验证、用量、发现、经验），通过或打回；
    - **日志**：LogTape，支持跟随和换行，stdout/stderr 可切换；
    - **变更**：未提交 diff、提交（SHA 可复制）、完整 diff 抽屉，支持行内评论；
    - **消息**：job 消息、注入的 rules，以及 ACP 会话消息输入框；
    - **产物**：结构化结果、产物文件、附件（上传、收集、跳过）；
    - **事件**：事件时间线、交互记录（待处理和已回答）、评论、通知投递、终端会话、wakeups。
  - 页签上带计数。需要人处理的页签显示黄色小角标。
- **`/sessions` 会话**（`Sessions.vue`）
  - 三组（AGENT 中继会话、ACP 持续会话、终端会话）改成页签，每个页签带计数。
  - InfoCard 换成统一的 `Card` 加 `StatusPill`，字段全部保留：名称、话题、授权/归属被拒/可能离线、关联工作项、目录、用量、中继三态、最近进展、字节数、加密、录制。
  - 「新建会话」统一走 `NewSessionSheet`。
- **`/mail` 信箱**（`Mail.vue`）
  - 左栏：
    - 筛选：全部、收件、发件、待指派（带计数）；
    - 写信按钮；
    - 会话对列表：PersonaChip 对、急件和待指派徽标、未读数；
    - 常驻 agent：名称、项目、会话 job、信件数、空闲时长、状态。
  - 右栏：
    - 线程顶部「加载更早的 N 条」；
    - 待指派的信显示「改投 / 撤回」；
    - 广播统计；
    - 回复框带「急件」勾选；
    - 右侧缩略图：点击跳转并高亮，显示当前视口框。
  - 写信（scope 广播、手写地址、markdown、急件）放进 `Sheet`。
  - 入口：桌面侧栏加入「信箱」。现状桌面只能通过信封按钮进入。
- **`/review` 待验收**（`ReviewQueue.vue`）
  - 现状不在导航里，本设计把它放进侧栏的「协作」分组。
  - 列：任务、项目、agent、计划和待办、验证、提交、用量、等待时长、裁决。
  - 新增项目 chip。「待批准 N」跳到 `/jobs?status=awaiting_approval`。
  - 手机：每行变成一张卡片，通过和打回按钮铺满。
- **`/new` 新建任务**（`NewJob.vue`）
  - 保留全部 query 预填和全部字段。
  - 改成两栏：
    - 常用：项目、agent、worker、模板、cwd、标题、任务说明；
    - 高级：可折叠分组，包含模型、参数、exec、锁、等待、上限、PTY、上传、收集、env、超时。
  - 标题跟随模式变化：新建任务、快速重建、从会话新开。

### 7.2 自动化

- **`/workflows`**：
  - 状态 chip 替换现在的 select，并加项目 chip；
  - 列：状态、标题和 id、步骤进度条（当前/总数）、开始时间、耗时。
- **`/workflows/new`**：保留项目、模板（全局或项目）、YAML 编辑器，编辑器用等宽字体加行号。
- **`/workflows/:id`**：
  - 返回按钮文字改为「工作流」（现状写的是英文「← workflows」）；
  - 结果表（步骤状态、开始、退出码、耗时、摘录、日志链接）；
  - 步骤链：并行和重试分组，用 CompareColumns 对比；
  - 事件；
  - 操作：取消、重跑失败步骤、从某步重跑、另存为模板。
- **`/schedules`**：
  - 加项目 chip（现状只认 URL 参数）；
  - 列：启用开关、名称和 id（cron/once 徽标；项目、agent、runner）、cron、下次触发、上次任务、操作；
  - 「立即运行」弹出参数表单，并预览会设置的 env；
  - 编辑；展开行显示详情。
- **`/schedules/new`**：字段不变，改用统一的表单组件。

### 7.3 知识

- **`/issues`**：
  - issues 和记忆两个页签保留；
  - repo 选择改用统一的 `ProjectFilter`（repo 视为项目）；
  - 树形/平铺切换、批量操作栏、IssueDrawer 改用统一的 `Drawer` 外壳。
  - 去掉 eyebrow「TRACKER」（属于模板套路）。
- **`/dashboard` 统计**：
  - 原来在「更多」菜单里，现在进侧栏的「协作」分组；
  - 统计卡片、完成趋势图、热力图、agent 表、按项目、验收、费用、系统面板全部保留；
  - 图表配色用状态族颜色，并遵守 dataviz 的对比度要求；
  - 新增项目 chip（只影响按项目以外的板块）。

### 7.4 基础设施与配置

- **`/runners`**：
  - 「待接受申请」置顶，申请同时进入「待我决策」；
  - 添加 worker、拓扑（可折叠）；
  - Workers 卡片字段全部保留；
  - 传话人入口保留（MessengerDrawer 换成统一的 `Drawer` 外壳）；
  - Peers 和 Server；
  - 英文分组名走 i18n。
- **`/agents`**：
  - 只读健康表保留；
  - 新增「编辑」链接，跳到 设置 › 配置管理 的 agent 段。现状里查看在这里、编辑在 Config，两处是割裂的。
- **`/projects`**：
  - 主从布局保留；
  - 详情增加该项目的活跃任务、计划、工作项计数和跳转（§6.4 第 5 条）；
  - Git 状态、子仓库、关键文件保留。
- **`/settings/*`**：
  - 二级导航（手机上是下拉框）：配置管理、规则、MCP、隧道、通知、权限、工作项、扩展、技能、关于；
  - 每页统一用 `FormSection`（左边说明，右边字段）；
  - 有未保存修改时出现吸附在底部的保存条。
  - 各子页内容不变：
    - 通知：本设备推送和订阅；
    - 权限：授权、会话授权、目录、审计；
    - 关于：版本、更新、登出。
- **`/access`**：
  - 登录页，没有外壳；
  - 只有 token 输入、主题开关、语言开关；
  - 公开路由的 guard 不变。
- **重定向**：`/skills`、`/cluster`、`/config` 保留。新增 `/board → /jobs`。

## 8. 组件清单

基元层放在 `web/src/styles/base.css`，组件放在 `web/src/ui/`。

| 新组件 | 替换或吸收的现有实现 |
|---|---|
| `tokens.css` 扩展、`base.css`（`.btn` `.chip` `.seg` `.field` `.panel` `.tbl` `.empty` `.kbd` `.tag` `.pill`） | 106 个 SFC 里各自写的 `.card`、`.empty`、`.chip`、`.panel`（初稿 E3） |
| `StatusGlyph` / `StatusPill` / `utils/statusFamily.ts` | `StatusBadge`、`PlanStatusBadge`、InfoCard 的 `.sbadge`、`statusPresentation.ts` 的圆点 |
| `AppShell`（`SideNav`、`TopBar`、`TabBar`、`MoreSheet`、`Wire`、`StatBar`） | `App.vue` 的顶栏和抽屉、`NavMore`、`TopbarMenu`、rail-toggle、`TodayStatusBar` |
| `NeedsMeButton` | `EscalationBell` 和 `EnrollmentBell` |
| `CommandPalette`（全局） | `WorkbenchCommandPalette` |
| `ProjectFilter`（chip 加 sheet，与 URL 同步） | Board、Plans、Workbench、Schedules、Issues 各自的项目 select |
| `FilterChips`（带计数）、`FilterDrawer` | 有的页面用 tabs，有的用 select，有的用 chip |
| `Sheet`（底部抽屉）、`Drawer`（侧抽屉外壳） | WorkDrawer、IssueDrawer、MessengerDrawer、Handled/Snoozed 抽屉、JobDetail 内嵌抽屉的外壳。`SessionDrawer` 的内容保留 |
| `Composer`（发送/排队/打断、附件、急件） | SessionDrawer、JobDetail 的 ACP 消息框、ThreadView、Mail 回复框、WorkDrawer 里重复的输入框 |
| `NewSessionSheet` | Sessions 表单、`WorkbenchComposer`、`/new?mode=session` 的入口（后端调用复用） |
| `DecisionCard` 重绘 | `today/DecisionCard`（逻辑不动） |
| `ProgressBar`（分段） | Plans、PlanDetail、Today lanes 各自的进度条 |
| `LogViewer` | `LogTape` 的外观（流式逻辑保留） |
| `DiffView` | `UnifiedDiff` 的外观 |
| `Tabs`（支持 `?tab=` 深链） | JobDetail 和 PlanDetail 的长页、Sessions 分组、ReviewPanel 页签 |
| `BackLink` | `uiText.BACK`、各页自己写的「← plans」等 |
| `FormSection`、`Switch`、`SaveBar` | 设置页各自的表单写法 |
| `Toast`（支持撤销） | `UndoToast`、`InteractionToast` 的外观 |
| `ThemeToggle`、`LangToggle` | ThemeToggle（现在只在 Access 用）、TopbarMenu 里重复的主题切换、被禁用的「中文/EN」 |

## 9. i18n

机制大部分采用初稿：D1–D6、D10，以及 F1–F3。

### 9.1 机制

- `store/locale.ts`：单例 ref，存 `localStorage gofer.locale`。首次访问时读 `navigator.languages`，以 `en` 开头取 `en-US`，否则取 `zh-CN`。`?lang=` 只用于调试和分享（初稿 D2/F2）。
- 挂载前设置 `<html lang>`，位置和 `initTheme()` 一样，避免闪烁。
- `i18n/index.ts` 提供 `t(key, params)`。自己写，不依赖库，平铺字典，插值只支持 `{name}`。缺 key 时回退到中文，并在开发环境 `console.warn`。等 key 超过约 800 个，或者需要复数规则时，再换 vue-i18n（初稿 D3）。
- `utils/format.ts` 用 `Intl` 统一处理相对时间、时长、数字、日期，替换现在的 17 处 `toLocaleString` 和 3 套时长写法（初稿 D6）。

### 9.2 key 命名（P0 冻结）

- 用英文语义命名，不用中文原文做 key（初稿 D4）。
- 域：`nav.* page.* col.* status.* act.* empty.* err.* toast.* confirm.* hint.* field.* help.* shell.* palette.*`。新增域必须先改这一节。
  （`shell.*` 是外壳自己的文案：连接状态、侧栏收起、节点摘要；`palette.*` 是命令面板。P1 实施时追加。
  `composer.*` 是统一输入框 Composer 的通用文案：发送 / 排队 / 打断、附图、急件、说明开关。P4 实施时追加。
  `util.*` 是工具层与跨页公共组件的文案（状态词以外的工具函数输出、交期 / 信箱 / 泳道 / 统计 / 对比视图）：
  这些字符串不专属某一页，所以集中在 `i18n/pages/shared.ts`，P6-b 实施时追加。）
- 页面专属的 key 用 `<page>.<area>.<name>`，例如 `plans.h.progress`。页面专属的前缀（目前是 `plans.`）同样受域白名单管辖，新增一个先在 `i18n.test.ts` 的 `pageDomains` 里登记。
- `status.<状态值>` 对应接口里的状态值（`status.needs_review`），`status.family.<族>` 是 7 个族名（`status.family.need`）。状态值不是业务内容，可以翻译；用户写的标题、消息、评论不翻译。
- 字典是扁平对象，key 里的点号不作嵌套解析；插值只支持 `{name}`（`t('jobs.count', { n })`）。
- `zh-CN` 是回退字典：`en-US` 缺 key 时用中文，两本都没有就原样返回 key（开发环境 `console.warn`）。`te(key)` 用来判断「有翻译才翻译」。
- 两侧字典的 key 集合必须相等，由 `web/src/i18n/i18n.test.ts` 的字典完整性用例保证。

### 9.3 不翻译的内容

以下内容原样输出，外面包 `<span lang="en">`，属于 WCAG 3.1.2 豁免（初稿 D10）：命令、路径、job/plan/todo id、agent 和 runner 名、模型名、配置键、日志、agent 输出。

用户写的业务内容不翻译：任务标题、计划描述、消息、评论、issue。

### 9.4 服务端文案

服务端返回的中文 `error`/`detail` 先原样显示。后续由服务端逐步补 `code` 字段，前端按 `err.<code>` 映射；没有映射的继续显示原文（初稿 D5）。这一步不阻塞改版。

### 9.5 抽取顺序

1. 已经集中的文案：`uiText.ts`（48 个 key）、`statusText.ts`（28 个）、`utils/` 里 18 份 label map。
2. 外壳和基元。
3. 按页面逐个抽取，顺序与 §11 的迁移顺序一致。
4. `window.confirm` 和 `title`/`placeholder`/`aria-label`。

每抽完一屏单独提交（初稿 F3）。

### 9.6 文案长度

英文比中文宽 1.4–2.6 倍（初稿 E5）。约束：

- 导航、chip、按钮不换行时用省略号截断，并配 `title`；
- 表头允许两行；
- 每屏都要在 en 下截图检查。

## 10. 可访问性

- 文本对比度 ≥4.5:1，大字和图标 ≥3:1，浅色和深色都要满足。
- 状态不只靠颜色区分：有字形，pill 里有文字。
- 所有可交互元素都有可见的焦点环（`--focus`）。
- 键盘：
  - 命令面板；
  - Today 快捷键（A 允许、D 拒绝、S 稍后、R 打回、1–3 选择选项），只对获得焦点的卡片生效，输入法组字时不触发；
  - 抽屉和弹层按 Esc 关闭，并把焦点还回打开它的元素。
- 页签用 `role=tablist`，抽屉用 `role=dialog`，开关用 `role=switch`。
- 实时区域（toast、新决策）用 `aria-live=polite`。
- 遵守 `prefers-reduced-motion`。
- 手机上的点按目标 ≥40px。

## 11. 现有前端问题与对应修复

| # | 问题（审计结果） | 修复 |
|---|---|---|
| 1 | 「任务」路由叫 `/board`，标题是「任务看板」，实际是一张表，与 PlanBoard 撞名；Plans.vue 的根 class 也叫 `.board` | 改名为 `/jobs` 任务列表，旧路由重定向（§7.1） |
| 2 | 待验收（`/review`）不在导航里；桌面上的信箱只能从信封按钮进入；统计藏在「更多」里 | 侧栏「协作」分组：信箱、待验收、Issues、统计（§6.1） |
| 3 | 两个齿轮（⚙ 设置链接和 TopbarMenu）；语言项是禁用的空壳 | 设置入口只留侧栏；顶栏放主题和语言开关；登出移到设置 › 关于（§6.1） |
| 4 | 每页的项目过滤机制都不同，Schedules 只认 URL，Workbench 的不持久 | 统一用 `ProjectFilter` 加 `?project=`（§6.4） |
| 5 | 状态表达有 5 套 | `StatusGlyph`/`StatusPill` 加状态族（§5） |
| 6 | 筛选控件不一致（chip、tabs、select 混用） | 统一用 `FilterChips` 加 `FilterDrawer` |
| 7 | JobDetail 是 4431 行的单页，结果、验收、日志都沉在底部 | 改成页签，默认显示概览，可以深链（§7.1） |
| 8 | PlanDetail 纵向堆了约 12 个区块 | 头部放进度和链控制，下方用页签（§7.1） |
| 9 | 新建会话有 4 个入口、4 套表单 | 统一为 `NewSessionSheet` |
| 10 | 消息输入框在 6 个地方各写一份 | 统一为 `Composer` |
| 11 | 抽屉外壳各写一份 | 统一为 `Drawer`/`Sheet` 外壳，内容保留 |
| 12 | 主题切换有两份（ThemeToggle 只在 Access 用） | 只保留一个 `ThemeToggle` |
| 13 | 返回链接混用中英文（「← plans」「← workflows」），JobDetail 固定回 /board | `BackLink` 走 i18n，回到来源页 |
| 14 | 页面标题和导航不一致（Work 页写「工作」，Dashboard/Mail 的 h1 写死） | 统一走 `nav.*` key |
| 15 | 断点有 17 种；手机靠可拖动的半圆打开抽屉 | 3 个断点加底部标签栏（§4.3、§6.3） |
| 16 | `*Mobile.test.ts` 用正则匹配 `.vue` 源码里的 CSS，换样式就会失败 | 改成测结构和行为（§13） |
| 17 | Agents 只能看，编辑在 Config，两处割裂 | Agents 加跳转到编辑的链接（§7.4） |
| 18 | Projects 页看不到该项目的计划、任务、工作项 | 加计数和跳转（§6.4） |
| 19 | 文案只集中了约 5%，大量中英混杂（PlanDetail 的 dt、Runners 的分组名、Schedules 的详情 key） | i18n 抽取（§9） |
| 20 | 过期注释还提到已删除的「左轨」（App.vue:221、:731，Schedules.vue:37） | 迁移外壳时一并清理 |
| 21 | 第一版预览本身丢掉了一些信息：Today 的 digest 和状态条、job 元信息网格和更多操作、会话轮数和用量、信箱的筛选、急件和待指派、计划的链控制和计划总览进度 | 已在本稿 §7 写明，并补进预览 |

## 12. 与初稿的分歧和取舍

| ID | 议题 | 初稿 | 本设计 | 理由 |
|---|---|---|---|---|
| D1 | 配色 | 不改品牌三色（phosphor 青、琥珀运行等） | 界面换成松绿加万寿菊的新色板，logo 和品牌文档不动 | 用户已选这一版视觉；现在的琥珀色表示「运行」，与「需要人」的暖色冲突。本设计把暖色专门留给「需要人」 |
| D2 | 字体 | IBM Plex Sans/Mono | 界面用 Schibsted Grotesk，等宽保留 Plex Mono，都自托管 | 界面字体的个性来自字形；等宽字体沿用，迁移成本低 |
| D3 | 圆角 | 统一 12，浮层 16 | 分 4 档：5/8/12/18 | 圆角层级本身就传达信息层级，所有东西都是同一种圆角卡片正是模板感的来源 |
| D4 | 信息架构 | 「导航项与路由不变」 | 有限调整：`/board→/jobs`，待验收、信箱、统计进入侧栏，铃铛合并，计划总览强化 | 审计发现的问题 1–4 都是导航造成的，不改就修不了 |
| D5 | 窄屏断点 | ≤900 统一切到手机布局 | ≤760 手机，761–1099 图标栏 | 平板宽度下手机布局太浪费，图标栏能保留桌面的信息密度 |
| D6 | 手机标签栏 | 今天、工作台、工作项、任务、更多 | 今天、工作台、任务、计划、更多 | 工作项里「等我」的部分已经进入 Today 的决策；计划进度是用户明确要看的 |
| D7 | 首屏 KPI | 去掉，数字进贴底条 | 同意 | — |
| D8 | i18n 机制 | locale store、自写 `t()`、英文语义 key、code 映射、format.ts | 全部采用 | 方案成熟，与本设计正交 |
| D9 | 签名元素 | 无 | 信号带 | 给产品一个可识别的标志，同时也是信息：一眼看出整体运行状态 |

## 13. 测试

- **单元测试（vitest）**：
  - `statusFamily`：每个状态都必须映射到某个族，有穷举测试；
  - `t()` 的回退和插值；
  - 字典完整性：zh 和 en 的 key 集合相等，可以扩展现有的 `uiTextWiring.test.ts`；
  - `format.ts` 在两种 locale 下的输出；
  - `ProjectFilter` 与 URL 的同步。
- **组件测试**：断言改成 `data-testid` 加可访问的 role 和 name，不再断言中文文案和 class 文本。初稿 E9 统计过：110 个测试文件断言中文。迁移每一屏时同步改它的测试。现有的 `*Mobile.test.ts` 改为在 jsdom 里测结构（比如手机下标签栏是否存在），视觉部分交给截图检查。
- **截图检查**：在 `web/scripts/shots.mjs` 里用 Playwright 或 agent-browser，对 6 个代表页加各自的详情页拍 4 种宽度（1440/1024/800/390）× 2 种主题 × 2 种语言。同时自动检查三件事：
  - 没有页面级横向滚动；
  - 没有尺寸超过 40px 的行内 SVG；
  - 计数、标签没有被断成多行。

  这一版预览附带的截图脚本（不入库）是原型。截图先人工审阅，不在 CI 里做像素比对。
- 每期合并前都要 `npm run build`、`vitest` 全绿、截图人工过一遍。

## 14. 迁移分期

每一步都是一个可以单独合并、可以回滚的提交组。新旧样式通过 token 别名共存。

| 期 | 内容 | 验收 |
|---|---|---|
| P0 基础 | `tokens.css` 扩展（加旧变量别名）、`base.css`、字体自托管、`statusFamily` 和 `StatusGlyph`/`StatusPill`、`locale` 加 `t()` 加 `format.ts` 的骨架 | 旧页面外观基本不变，build 和测试全绿 |
| P1 外壳 | `AppShell`（侧栏、图标栏、标签栏、更多抽屉、信号带）、`NeedsMeButton`、全局命令面板、主题和语言开关、删除 rail-toggle 和重复齿轮；路由 `/jobs`（`/board` 重定向） | 4 种宽度没有横向滚动；所有路由都能到达；zh/en 切换后外壳没有残留中文 |
| P2 列表基元 | `ProjectFilter`、`FilterChips`/`FilterDrawer`、`Tabs`、`Sheet`/`Drawer` 外壳、`BackLink`、`ProgressBar` | 先在任务列表和计划总览落地 |
| P3 高频页 | Today（含状态条、digest）、任务列表和详情（页签化）、计划总览和详情（页签化）、工作台 | 每页 zh/en × 深浅截图，对照 §7 字段清单逐项核对 |
| P4 协作页 | 信箱、待验收、会话（`NewSessionSheet`、`Composer` 统一）、工作项 | 同上 |
| P5 其余页 | 工作流、定时、Issues、统计、运行节点、Agents、项目、设置各页、新建表单、Access | 同上；删除旧的 `.sbadge`、`StatusBadge` 等 |
| P6 收尾 | 删除旧 token 别名、残留文案抽取、服务端 `code` 映射（可选） | 字典完整性测试全绿；全站扫描没有硬编码中文（白名单除外） |

建议 P0 和 P1 先连续做完，因为外壳是所有页面共同依赖的。P3 到 P5 可以按页面并行派发，每页一个 todo。字典 key 的命名规范在 P0 就冻结。

## 15. 风险

- **测试大面积失败**：大量测试断言中文和 class。缓解办法是每屏迁移时同步改测试，不攒到最后一起改。
- **用户习惯**：`/board` 改名、JobDetail 改页签、铃铛合并会改变操作路径。缓解：重定向带上原 query；页签可以深链；在「关于」页写一段变更说明。
- **信息遗漏**：§7 的字段清单作为每页的验收单，每一项都要核对。
- **字体体积**：Schibsted Grotesk 四个字重自托管约 120KB。只打包拉丁子集，中文用系统字体。
- **深色对比**：深色 token 已经按 AA 选色。新增颜色时必须先过对比度检查。
- **范围膨胀**：本设计不改接口语义。需要服务端配合的（错误 code、`/projects` 计数接口）都单独立 issue，不阻塞前端。

## 16. 已定（2026-10-23 用户批准）

第 0.1 版的 5 个待确认问题全部按建议执行：

1. 界面换成新色板与 Schibsted Grotesk；logo 与 `docs/brand` 不动（品牌规范只约束 logo 三色的角色）。
2. 主题默认跟随系统，用户手动选过就记住。
3. 接受 `/board → /jobs`（旧链接带 query 重定向）；手机标签栏用「计划」替换「工作项」，工作项放在「更多」第一位。
4. worker 申请并入「待我决策」；信箱待指派保持独立。
5. JobDetail 页签按状态默认：needs_review →「验收」，failed →「日志」，其余 →「概览」；切换过的页签记在 URL。

用户评审预览后追加的布局要求（覆盖上文相应描述）：

- **侧栏底部的运行节点区默认收起**：只显示一行摘要（如「节点 3/4 在线」），点开展开列表；展开状态记在本地。
- **侧栏整体可以收起**：桌面宽度下也能收成图标栏（与 761–1099 的图标栏同一形态），收起状态记在本地；收起时悬停图标显示名称。
- **任务页列表与详情的布局按宽度决定**：JobDetail 信息量大（元信息、验收、日志、diff、消息、事件），在不够宽时并排会挤。规则：主内容区宽度 ≥ 1280px（约等于视口 ≥ 1560px 且侧栏展开）时可以列表 + 详情并排，并提供「单栏」切换；更窄时列表占满，点开任务进入完整的 `/jobs/:id` 详情页，带返回列表（保留列表的筛选与滚动位置）。手机始终是两级页面。并排时详情面板宽度不小于 760px，不够就自动退回单栏。

## 17. 实施记录

### P0 基础层（2026-10-23，issue gofer-hez1）

落地范围即 §14 表格 P0 行，不碰任何页面组件与 Go 代码。

**token 与基础层**

- `web/src/styles/tokens.css`：只放变量（颜色两套主题、字号阶梯、间距、四档圆角、动效、阴影），深色为默认值，浅色在 `[data-theme="light"]` 覆盖。断点口径（≤760 / 761–1099 / ≥1280）写进注释，CSS 变量不能用于 media query。
- `web/src/styles/base.css`：元素级排版（`font-variant-numeric: tabular-nums`）、焦点环（`--focus`）、主题化滚动条、`::selection`、`.sr-only`、`prefers-reduced-motion`。原来写在 tokens.css 里的全局规则整体搬到这里，规则内容未变。
- `web/src/styles/status.css`：`.k-<族>` 给出 `--c` / `--soft`，以及 `.glyph` / `.pill` 两个基元样式（组件不带 scoped 样式，P2 的基元层可以复用）。

**旧变量别名映射**（P6 删除）：全部按「角色」指向新 token，旧页面不改一行也能拿到新色板：

| 旧变量 | 迁移期指向 | 说明 |
|---|---|---|
| `--ink`（页面底） | `var(--bg)` | 与新版正文色同名冲突，新正文色取名 `--fg` |
| `--panel` | `var(--surface)` | |
| `--paper`（正文） | `var(--fg)` | |
| `--phosphor`（强调/链接） | `var(--accent)` | |
| `--queue`（灰字/待排队） | `var(--fg-3)` | 设计稿 §5：queue 族用中性色 |
| `--line` / `--run` / `--done` / `--fail` | 同名新 token | 角色一致 |
| `--term-bg` | 迁移前的分主题终端底 | 新日志底色叫 `--log-bg` |
| `--radius`（4px） | 保持 4px | 旧页面的圆角层级不动 |
| `--font-sans` / `--font-mono` | `var(--font-ui)` / 新等宽栈 | |

**字体**（§4.2、§15）

- 依赖加在 `web/package.json`（`@fontsource/schibsted-grotesk@^5.1.1`，`@fontsource/ibm-plex-mono` 沿用），`main.ts` 只导入 `latin-400/500/600`（Schibsted Grotesk）与 `latin-400/500`（Plex Mono）。CJK 不打包，回退系统字体。
- 构建产物：5 个字面共 5 个 woff2 + 5 个 woff，合计约 217 KB（woff2 约 102 KB；woff 是 Fontsource 的兼容回退，现代浏览器不取）。无任何外部字体请求。
- 迁移前用的 `@fontsource/ibm-plex-sans` 已无人引用，依赖条目一并删除（新界面字体是 Schibsted Grotesk）。

**状态系统**（§5）

- `web/src/utils/statusFamily.ts`：7 个族（run / need / done / fail / time / idle / queue），状态值 → 族的唯一映射；空值和未登记值落到 idle（后端加新状态不会变成空白或误报「需要人」）。多义值 `pending` 按设计稿归 queue，需要黄色时调用方显式传 family。
- `web/src/ui/statusGlyph.ts` + `StatusGlyph.vue`：7 种字形（脉冲圆 / 菱形 / 圆勾 / 方叉 / 时钟 / 空心圆 / 虚线圆），`role="img"` + `aria-label`，带 `data-family` / `data-kind` 供测试与截图检查。
- `web/src/ui/StatusPill.vue`：字形 + 文字；文字优先取 `label`，其次 i18n `status.<值>`，最后退回旧的 `statusText`（P5 迁移页面时逐步去掉这条退路）。
- P0 **不替换** `StatusBadge` 等旧表达，两个组件只在测试里示范使用。

**i18n 与格式化**（§9）

- `web/src/store/locale.ts`：单例 `locale`，`gofer.locale` 持久化，首访按 `navigator.languages`（en → en-US，其余 zh-CN），`?lang=` 只调试不持久化；挂载前 `initLocale()` 写 `<html lang>`。
- `web/src/i18n/`：自写 `t(key, params)` / `te(key)` / `translate(key, params, locale)`，扁平字典、`{name}` 插值、缺 key 回退中文（再缺就返回 key 并在开发环境 warn）。
- 字典 seed：`nav.*` `act.*` `status.*`（含 `status.family.*`）`empty.*` `err.*` `toast.*` `confirm.*` `hint.*` `field.*` `help.*`。key 命名规范见 §9.2（P0 冻结）。
- `web/src/utils/format.ts`：`formatRelative` / `formatDuration` / `formatNumber` / `formatPercent` / `formatDate(Time)` / `formatTime`，全部走 `Intl`；时长单位统一 h/m/s/ms（与 locale 无关，等宽字体下不跳字）。

**测试**（§13）：`statusFamily` 穷举（每个实体的每个状态 + 黄色只给「需要人」）、`StatusGlyph` / `StatusPill` 的 SSR 渲染与族映射、`t()` 回退与插值、字典 key 集合相等、`format.ts` 两种 locale 输出。全量 `npx vitest run` 195 个文件 1283 个用例通过。

### P1 外壳（2026-10-23，issue gofer-hez1）

落地范围即 §14 表格 P1 行：外壳、顶栏、信号带、命令面板、/jobs 改名。页面内容一行未动，仍由原来的 view 渲染，只是放进新外壳。不涉及 Go 代码（server 内嵌链接与通知里没有 `/board`，`internal/httpapi/server.go` 只是注释里举例；SPA fallback 对 `/jobs` 同样返回 index.html）。

**外壳与导航**

- `web/src/ui/AppShell.vue`：侧栏 / 图标栏、顶栏、信号带、手机标签栏、「更多」抽屉。形态只看视口宽度，与路由无关。
- `web/src/utils/shell.ts`（纯函数，可单测）：`shellForm(width)` 给出 `tabs`(≤760) / `rail`(761–1099) / `desktop`(≥1100)；`sidebarExpanded()` 把「桌面可收起」与断点合起来判断；`readShellPrefs`/`writeShellPrefs` 存 `gofer.shell`（`collapsed` 侧栏收起、`nodesOpen` 节点区展开，坏值回落默认）；`SHELL_NAV_GROUPS` 是四组导航（核心 / 协作 / 自动化 / 基础设施）的唯一出处，文案全是 i18n `nav.*`；`TAB_BAR_ITEMS` + `moreItems()` 给手机的五标签与「更多」，工作项在「更多」第一位（§16-3）。
- 侧栏**没有**项目选择器（§6.4）。底部运行节点区默认收起，只显示「节点 N/M 在线」，点开展开列表，两个状态都记本地。桌面档侧栏底部有收起按钮，收起后与 761–1099 的图标栏同一形态（悬停 title 显示名称）。
- 计数徽标：今天（决策队列件数）与工作项（等我件数）用黄色徽标，只在 `> 0` 时出现（§6.1「黄色只代表需要人」）；任务 / 信箱的灰色数量徽标未接（它们要单独的计数数据源，留给 P2 的 `ProjectFilter` / `FilterChips` 一起做）。图标栏（761–1099 与收起态）里徽标不显示，只有字形 + title。
- 删除 `rail-toggle` 与它的拖动 / 位置持久化（`utils/navTab.ts`）、`components/NavMore.vue`、`components/TopbarMenu.vue`、`utils/nav.ts`（旧的分组常量和它的测试、接线测试一并去掉）。设置入口只留在侧栏底部，主题 / 语言是顶栏上的两个直接开关。

**顶栏**

- `web/src/ui/NeedsMeButton.vue`：合并原来的 `EscalationBell` 与 `EnrollmentBell`（§16-4）。计数是决策队列（`store/today`）+ worker 申请数（`listEnrollments`，仅 `worker.manage` 订阅该主题），两个旧组件删除。原来挂在铃铛上的 `meta`/`stats`/`pending` 订阅与提示条（`utils/pendingAlert`）整体搬过来，行为不变。
- 顶栏顺序：搜索（打开命令面板，Ctrl+K）· 只读徽标 · 新版本提示 · 待我决策 · 新建任务 · 随手记 · 信箱 · 主题 · 语言。信箱待指派仍是独立图标（§16-4）。
- `web/src/ui/LangToggle.vue`：zh/en 直接切换，走 P0 的 `store/locale`。没有新增主题 store：`components/ThemeToggle.vue` 的「跟随系统 / 手动记住」已由 `store/theme` 实现（§16-2），只是从 Access 页搬到了顶栏。

**信号带**

- `web/src/ui/Wire.vue`：顶栏下 4px 的分段带，每段一个排队 / 运行 / 需要人 / 验收中的 job，颜色取 `statusFamily`，运行中的段有流光，`prefers-reduced-motion` 下关闭。数据用现有的 `GET /v1/jobs`（`createPoller` 20s + 后台标签页停表），**不新增接口**；会话段留到 P3 接工作台数据源时再加。

**命令面板**

- `web/src/ui/CommandPalette.vue`：Ctrl/⌘K 或非输入状态下 `/` 打开，Esc 关闭，↑↓ 选择，Enter 跳转。P1 只做页面跳转与 `p:<项目>` 前缀（把 `?project=` 写进目标 URL，只对支持项目筛选的页面生效，§6.4/§6.5）。匹配逻辑是 `utils/shell.ts` 的 `parsePaletteQuery` / `paletteHits`，纯函数单测。任务 / 会话 / 计划三类结果、以及 `/` 与 Today 的 `g d` / `z` 的键位冲突留给 P3（现在 `/` 只在非输入状态、面板未打开时生效）。

**路由**

- `web/src/router.routes.ts`：路由表从 `router.ts` 拆出来（`router.ts` 只建 router 与守卫），这样路由表测试能在 node 环境导入而不需要 DOM。
- 任务列表改名：`/jobs`（name `jobs`，仍是 `Board.vue`），`/board` 保留为重定向、带原 query（`/board?status=needs_review&project=p1` → `/jobs?status=...&project=...`）。页面内所有 `/board` 链接（Board 自身的筛选 push、JobDetail 返回、NewJob 返回、PlanDetail 的「在任务列表查看」、Projects 跳转、ReviewQueue 的待批准入口）改成 `/jobs`。其他旧重定向（`/skills`、`/cluster`、`/config`）与全部现有路由不变。

**i18n**

- 新增两个域：`shell.*`（连接状态、侧栏收起、节点摘要、只读、新版本）与 `palette.*`（面板标题、占位、项目提示、空态），§9.2 已同步。外壳文案全部走 `t()`，zh/en 两本字典同名同数（`i18n.test.ts` 的完整性用例保证）。页面内容仍是原来的中文文案，按 P3 起逐页迁移。

**测试**（§13）

- 新增：`utils/shell.test.ts`（三档断点与边界值、侧栏展开判定、`gofer.shell` 本地记忆的正常 / 坏值 / 存储不可用、四组导航与 `/jobs`、手机五标签与「更多」顺序、`needsMeTotal` 合并、命令面板 `p:` 前缀与项目 URL）、`ui/appShell.test.ts`（合并后的待我决策、顶栏只有一套铃声 / 无重复齿轮 / 无 rail-toggle、本地记忆与手机抽屉接线）、`ui/appShellRender.test.ts`（SSR 冒烟：1440 完整侧栏、1024 图标栏、390 五标签 + 无侧栏；推送客户端用 `setLiveClientForTest` 替身）、`router.test.ts`（`/jobs` 是列表页、`/board` 是带原 query 的函数式重定向、旧重定向仍在）。
- 同步修改：`views/today.test.ts`（导航断言改成 `SHELL_NAV_GROUPS` / `TAB_BAR_ITEMS`）、`views/work.test.ts`、`views/ceilingWiring.test.ts`、`views/mailWiring.test.ts`、`views/mailMinimapWiring.test.ts`、`views/captureBoxWiring.test.ts`（从 App.vue 改读 `ui/AppShell.vue` / `utils/shell.ts`）、`i18n/i18n.test.ts`（两个新域）、`components/drawerAttachPolish.test.ts`（改为断言 rail-toggle 已删除）。
- 删除：`utils/nav.test.ts`、`utils/navTab.test.ts`、`navTabWiring.test.ts`、`components/navTopbar.test.ts`、`components/navTopbarLayout.test.ts`、`components/enrollmentBell.test.ts`、`components/escalationBellAck.test.ts`、`components/escalationBellApproval.test.ts`——被删的组件 / util 就是它们的对象，覆盖搬到 `utils/shell.test.ts` 与 `ui/appShell.test.ts`（`pendingAlert` 的纯逻辑测试原样保留）。
- 全量 `npx vitest run` 191 个文件 / 1270 个用例通过，`npx vue-tsc --noEmit` 与 `npx vite build` 通过。

**四种宽度的形态**（无截图，按 CSS 口径说明）

| 宽度 | 形态 | 不横向滚动的依据 |
|---|---|---|
| 1440 | 完整侧栏 236px + 顶栏 + 信号带 | 侧栏 `flex:none` 固定宽，主列 `flex:1; min-width:0`，内容区 `overflow-x:auto` 兜底，顶栏右侧按钮不换行、`new-job-label` 在 ≤1099 收起 |
| 1024 | 64px 图标栏 + 顶栏（新建任务只留图标） | 同一套 flex，侧栏宽 64px；导航项只留字形、分组标题隐藏 |
| 760 | 手机：顶栏（标 + 搜索 + 待我决策）+ 底部 5 标签 | `.shell--tabs` 改纵向 flex，标签栏 `flex:1 1 0` 均分；`.content` 的 `min-width:0` + 页面自带换行；「更多」抽屉本身 `overflow-y:auto` |
| 390 | 同上，搜索只留图标、连接状态收成一个点 | 顶栏 gap 收到 `--s1`，`search-label` / `search-kbd` 隐藏，标签文字 `--fs-xs` |

未做（如实记录）：没有在真机 / 浏览器里按这四个宽度截图核对（P1 的验收靠 CSS 口径 + 现有各页手机断言的测试）；会话段还没进信号带；命令面板还没有任务 / 会话 / 计划结果。

### P2 列表基元（2026-10-23，issue gofer-hez1）

落地范围即 §14 表格 P2 行：六个列表基元 + 在任务列表与计划总览落地。不涉及 Go 代码，也没有新增接口。

**组件（`web/src/ui/`，文案全部走 `t()`）**

| 文件 | 职责 |
|---|---|
| `ProjectFilter.vue` + `projectFilter.ts` | 列表头的项目 chip（空 = 全部项目），受控组件；`projectFilter.ts` 的 `useProjectFilter(path)` 是读写 `?project=` 的唯一入口（换项目回第一页），`readProjectQuery` / `defaultProject` / `rememberProject` 处理「URL 优先、否则 `gofer.lastProject`」的新建表单默认值 |
| `FilterChips.vue` + `filterChips.ts` | 带计数的 chip 行，单选（再点一次取消）与多选（`multiple`）两种语义；`chipItems(options, counts)` 把值→计数折成 chip 数据。桌面换行、≤760 一行横向滑动（框内 `overflow-x:auto`，不撑宽页面），点按目标 40px |
| `FilterDrawer.vue` | 「更多筛选」触发按钮 + `Sheet` 外壳，内容走默认插槽；触发按钮带已生效维度数，`clearable` 时抽屉里给「清空过滤」 |
| `Sheet.vue` / `Drawer.vue` + `overlay.ts` | 底部抽屉与侧抽屉外壳：`role=dialog`、`aria-modal`、Esc 关闭、点遮罩关闭、Tab 在面板内循环、关闭后焦点回到打开它的元素、打开时 `body` 不滚；Sheet 还带抓手下滑 60px 关闭（手机手势友好）。共用行为在 `overlay.ts`（`focusables` / `trapTab` / `useOverlayBehavior`） |
| `Tabs.vue` + `tabs.ts` | `?tab=` 深链（不是默认页签才写进 URL）、`role=tablist`/`tab`/`tabpanel`、roving tabindex、← → Home End 键盘切换、页签计数与「需要人」黄色角标。值→URL 的换算在 `tabs.ts`（可单测） |
| `BackLink.vue` + `listReturn.ts` + `useListReturn.ts` | 返回列表并保留筛选与滚动位置：列表页 `useListReturn(listId, path)` 把 `fullPath` + `scrollY` 记进 `sessionStorage`（`gofer.return.<listId>`），`BackLink` 读回来跳转，列表页再复原滚动（只有 URL 一致时才复原）。为 P3 任务详情的单栏模式准备 |
| `ProgressBar.vue` + `progress.ts` | 分段进度条：完成 / 进行中 / 待验收 / 受阻 / 未开始五段，`planProgressModel(plan)` 用 `todo_counts` 折算（`done+skipped` 算完成、`doing` 算进行中、其余算未开始），无 todo 时回落 job 计数，百分比优先取服务端 `completion.percent`；带 `done/total` 与百分比，图例给文字与计数，每族另有纹理（实心 / 斜纹 / 点阵 / 交叉纹 / 虚底），不靠颜色区分 |
| `PlanOverviewRow.vue` | 计划总览的一行：状态、标题/id/项目/tags、`ProgressBar`、卡点、主会话、最近活动；≤760 变成卡片 |

**任务列表（/jobs，Board.vue）**

- 列表头换成 `ProjectFilter` + `FilterChips`（状态，计数仍只给 `isCountedStatus` 的进行中类状态）。原来的项目 `<select>` 与状态 tabs 删除：项目只有一个入口（§6.4）。
- 其余维度（计划、工作流、tag、agent、runner、调用方、时间范围、含上传、显示传话 job）原封不动搬进 `FilterDrawer`，请求参数、watch、状态计数、最近 open plan 提示都保持原行为；「刷新 / 清空过滤」仍在页头。
- 滚动位置记录接了 `useListReturn('jobs', '/jobs')`（P3 详情页的 `BackLink` 就能回到当前筛选）。

**计划总览（/plans）**

- 表头换成：状态 · 计划 · 进度 · 卡点 · 主会话 · 最近活动；每行一个 `PlanOverviewRow`。
- 筛选：`ProjectFilter` + 状态 `FilterChips`（多选累积，空 = 全部）+ `FilterDrawer`（关键字、tag）；分页仍走 `?offset`，服务端分页不变；同样接了 `useListReturn('plans', '/plans')`，P3 的计划详情可以直接用 `BackLink`。
- 卡点列：卡在哪个 todo（`blocked_todo`）、N 个 job 失败（`counts.failed`）、N 进行中（`todo_counts.doing`）；没有卡点写「无卡点」。
- **未做（数据不够）**：列表接口没有 `needs_review` 的分状态计数，「N 待验收」要等计划详情（P3，有完整 job 列表）或新接口；因此列表里的「待验收」段恒为 0（`progress.ts` 保留该族，详情页可传）。

**测试**（§13）

- 新增 `ui/progress.test.ts`：`percentOf` 取整/钳制/无总数、`planProgressModel` 的 todo 口径 / job 回落 / `completion` 优先 / 空计划，`ProgressBar` 的分段宽度、`done/total` 与百分比、图例文案、空条。
- 新增 `ui/primitives.test.ts`：chip 单选/多选逻辑、`FilterChips` 的 `aria-pressed` 与计数、`ProjectFilter` 的「全部项目」/当前值/`?project=` 与 `gofer.lastProject` 的默认值优先级、`FilterDrawer` 的维度计数与关闭态、`Sheet`/`Drawer` 的 `role=dialog` 与 `aria-modal`（SSR Teleport）、Esc/Tab/焦点归还的接线、`Tabs` 的 `?tab=` 深链 / 未知值回落 / 默认页签不写 URL / roving tabindex / 计数与角标、`listReturn` 的存取与坏值、`BackLink` 的回落地址、`PlanOverviewRow` 的进度 / 卡点 / 主会话 / 最近活动。
- 新增 `views/listLandings.test.ts`：任务列表的项目 chip 来自 `?project=`、状态 chip 的选中来自 `?status=`、其余九个筛选维度仍在（源码断言，行为不变）、计划总览的列表头顺序与 `PlanOverviewRow` 渲染。
- 同步修改：`views/plansMobile.test.ts`（不再用正则匹配旧 CSS，改为断言用了 `FilterChips`/`FilterDrawer`/`ProjectFilter` 与 ≤760 的卡片布局）、`utils/jobStatusCounts.test.ts`（v-if 断言改成 chip 的 `isCountedStatus` 口径）、`i18n/i18n.test.ts`（页面专属域 `plans.`，§9.2 已给例子）。
- 全量 `npx vitest run` 194 个文件 / 1303 个用例通过，`npx vue-tsc --noEmit` 与 `npx vite build` 通过。

**如实记录的范围边界**

- `Tabs`、`Sheet`/`Drawer`、`BackLink` 只交付外壳与行为，现有抽屉 / 页签内容一行没有迁移（P3/P4 逐页换）。
- 计划总览的「主会话」只显示会话 id 尾部（完整 id 在 `title` 里），点击行为留在详情页。
- 手机上的筛选走 `FilterDrawer`（底部抽屉），桌面同样是抽屉，没有做「桌面内联展开」的第二套形态。
- i18n 只覆盖新组件与新的列表头；页面其余中文文案按 P3 起逐页抽取。

### P3 今天页（2026-10-23，issue gofer-hez1）

落地范围即 §14 表格 P3 行的今天页（`/today`）。任务列表 / 详情、计划总览 / 详情、工作台不在本次范围。不涉及 Go 代码，没有新增接口。

**页面结构（`web/src/views/Today.vue`）**

- 两栏：主栏 `minmax(0,1fr)` + 右栏 340px（1279–1100 为 300px，≤1099 叠成一栏）。
- 主栏自上而下：日期 → 「N 件事在等你」（`today-title`）→ 一行摘要（agent 在跑 / 空闲会话 / 今天完成，带状态字形）→ 「自上次打开」摘要（完成 / 失败 / 新提交，点开是当天工作摘要）→ 类型 chip + 项目 chip → 决策卡队列（前 5 张，其余「还有 N 张」）→ 卡片底部（专注处理 / 含 exec 待验收 / 已稍后 N / 已处理 N）。
- 右栏：「并行中」（原来的 `TodayLanes`，改成右栏区块）+ 「刚处理过」（新增 `TodayRecentHandled`，最近 5 条，数据同「已处理」抽屉）。
- 贴底状态条（`TodayStatusBar` 重绘）：在跑 / 今天失败 / 节点在线 / 用量 / 管家 / 版本，`position: sticky` 在内容底部，异常项变色排最前。首屏**不放** KPI 四宫格（§12 D7）。
- 手机（≤760）：全部单栏，chip 行框内横向滑动，卡片主操作铺满且点按目标 ≥40px。
- 卡片快捷键（§10）：卡片可获得焦点（`tabindex=0`），聚焦后 `a` 允许 / 通过、`d` 拒绝 / 打回、`s` 稍后、`r` 回复、`i` 详情、`1`–`9` 按顺序触发操作、`Esc` 收起卡内菜单；处理掉的键 `stopPropagation`（不连带触发专注模式与全局 `z`）；输入框内打字与输入法组字（`isComposing`）不触发。键位映射在 `utils/todayKeys.ts`（纯函数）。卡片底部有一行快捷键提示，手机隐藏。

**数据（不新增接口）**

- 队列 / digest / 状态条仍来自 `GET /v1/today`；泳道仍来自 `GET /v1/today/lanes`，`TodayLanes` 拉取后 `emit('loaded')` 给页面，首屏摘要的「agent 在跑 / 空闲会话」由泳道的 `agents[].state` 折算（`utils/todayFilters.ts` 的 `laneAgentStats`），「今天完成」取 `digest.since_last.jobs_done`。
- 项目 chip 走 P2 的 `useProjectFilter('/today')`（`?project=` 是唯一真源）；类型 chip 走 `utils/todayFilters.ts` 的 `typeCounts` / `filterByType`（纯函数，单测覆盖）。
- 状态条的「排队」一项没有数据源（`/v1/today` 不返回排队数），本次不摆，不拿别的数字顶替。

**字段核对（§7.1 → 迁移后）**

| 迁移前的信息 / 操作 | 迁移后 |
|---|---|
| 日期 | 主栏头部日期行（新增） |
| 「N 件事在等你」 | 主栏标题（张数按当前筛选算） |
| 一行摘要（agent 在跑 / 空闲会话 / 今天完成） | 头部下方一行，带状态字形（新增） |
| 自上次打开：完成 / 失败 / 新提交 + 当天工作摘要 | 同名 `<details>`（失败数 >0 时变红） |
| 待我决策：全部卡片种类、分组（会超时 / 卡住别人 / 可稍后）、前 5 张 + 还有 N 张 | 不变（`DecisionQueue` / `DecisionCard` 逻辑未动，仅换皮：黄色左边线、类型 pill、agent） |
| 卡片：来源 / 项目 / 等待 / 超时 / 卡住 N / 标题 / 一句话 / 操作 / 详情 / 稍后 / 回复框 / 按建议 | 全部保留 |
| 含 exec 待验收开关 | 卡片底部一行 |
| 专注处理（页面按钮 + 队列底部） | 两处都在 |
| 已稍后 N（抽屉、放回队列） / 已处理（近 7 天抽屉） | 保留 |
| 并行中（进度、agent、id、todo 小点、健康度） | 右栏区块 |
| 刚处理过 | 右栏新块（今天最近 5 条） |
| 状态条：runner 在线 / 离线名单、今日用量、管家、版本、异常排序 | 保留并拆成独立项（在跑、今天失败、节点在线、用量、管家、版本），链接目标不变 |
| 项目筛选 | 项目 chip（§6.4，新增） |
| 类型筛选 | 六类 chip + 全部（新增） |

**i18n**

- 本页文案全部走 `t()`，只写在 `web/src/i18n/pages/today.ts`（key 前缀 `today.`，已登记进 `i18n.test.ts` 的 `pageDomains`）；zh / en 同名同数。
- 服务端下发的业务内容（卡片标题、摘要、digest 正文、agent / 项目名、job id）不翻译；`utils/today.ts` 里由数据拼出的短语（如「等了 6 分钟」「卡住 2」「1 分钟后超时」）本次仍在 util 里，未接管（后续页面迁移时一并处理）。

**测试**

- 新增 `utils/todayFilters.test.ts`：卡片种类 → 六个 chip 的映射（含归不进六类的种类）、计数、类型 / 项目筛选（空值 = 全部）、`laneAgentStats`。
- 新增 `utils/todayKeys.test.ts`：a / d / s / r / i / 1–9 / Esc 的映射，输入框内打字、输入法组字、带修饰键时不触发。
- 改写 `views/today.test.ts`：首屏标题与摘要、digest、前 5 张 + 还有 N 张、状态条顺序与链接、空态与专注处理灰显、类型 / 项目 chip 接线、右栏两块、文案走本页字典、窄屏单栏与 chip 横向滑动。
- 同步修改：`views/todayActionGate.test.ts`（专注处理的禁用条件改成筛选后的张数）、`components/today/decisionCard.test.ts`（「详情 / 收起」走 i18n）、`components/today/todayLanes.test.ts`（「还有 N 条正常运行中」走 i18n）、`i18n/i18n.test.ts`（页面域加 `today.`）。
- 全量 `npx vitest run` 196 个文件 / 1317 个用例通过，`npx vue-tsc --noEmit` 与 `npx vite build` 通过。

**如实记录的范围边界**

- 状态条的「排队」没有数据源，未摆；「今天失败」只算 digest 窗口内（自上次打开）的失败数，不是服务端统计的自然日失败数。
- 类型 chip 只覆盖设计稿列的六类；工作项 / 合并建议 / 收集箱等只在「全部」里出现，没有单独 chip。
- `DecisionCard` 的 `blockShort` / `waitText` / `expiresText` 等 util 文案未走 i18n（见上）。
- 未在浏览器里对 zh/en × 浅/深四种组合截图核对；视觉按预览源码的 CSS 口径落地（tokens、间距、圆角、断点）。

### P3 计划页（2026-10-23，issue gofer-hez1、gofer-rj5e）

落地范围即 §14 表格 P3 行的「计划总览 + 计划详情」两页（Today / 任务详情 / 工作台由同期的其他分支负责），外加为「待验收」段补的服务端聚合（gofer-rj5e，不新增路由）。

**服务端：计划列表的进度数据（gofer-rj5e）**

- `GET /v1/plans` 的每一项与 `GET /v1/plans/{id}` 新增 `needs_review`（等待验收的 job 数）与 `failed_jobs`（失败 job 数，含 timeout / cancelled，等于 `counts.failed`）。
- 列表里 job 汇总改成一次聚合：`jobstore.Store.PlanJobStatusCountsByPlan(ids)` 按 `todoCountsChunk`（500）分块，每块一条 `GROUP BY plan_id, status`；原来的实现是**每个计划一条** `PlanJobStatusCounts`。
- 覆盖：`internal/httpapi/plan_completion_test.go` 新增 `TestPlanListReviewAndFailureCounts`（2 needs_review + failed + timeout + done → `needs_review=2`、`failed_jobs=2`、`counts{total 5, done 1, failed 2}`，详情同样两个字段）；`docs/reference/http-api.md` 补一段字段说明。

**计划总览（`/plans`）**

- 进度条改用 `needs_review` / `failed_jobs`：「待验收」段不再恒为 0，「受阻」段优先取 `failed_jobs`（老服务端回落 `counts.failed`）。分段口径仍在 `ui/progress.ts`，列表与详情共用。
- 卡点列顺序：卡在哪个待办（fail）→ N 个 job 失败（fail）→ N 待验收（黄，`--need`）→ N 进行中（run）。
- tags 行补了两个标识 chip（§7 要求的信息）：已暂停（`paused`，`--time`）与自动推进（`leader === 'on'`，`--run`）。
- 页头标题、状态 chip 标签、关键字 / tag 抽屉、空态、分页条全部走 `t()`，key 在 `i18n/pages/plans.ts`；`plans.*` 从 `zh.ts` / `en.ts` 移到该文件（同一页自己的 key，避免与其他页面并行迁移冲突）。

**计划详情（`/plans/:id`）**

- 头部：`BackLink`（回 `/plans`，带 P2 记下的筛选与滚动位置）；标题、状态、标记完成 / 归档 / 重新打开 / 在任务列表查看、看板·列表切换；**大号分段进度条**（`ProgressBar` size md + 图例）加一行元信息（N 运行中 · N 失败 · 最近活动）；描述；元信息（id / 状态 / 负责人 / 主 Agent 会话 / 项目 / tags / 进度 / 创建 / 更新）；用量块；链控制行（链状态、leader、notify、启动链 / 挂起·继续·解除阻塞、帮助）与 leader 回合、blocked 横幅都留在头部。
- 下方六个页签（`ui/Tabs`，值在 `?tab=`，默认 `todos` 不写进 URL）：待办（`PlanBoard` 五列 / 列表切换，选择仍记 `gofer.plans.view`）、任务（job 表 + 附加任务）、决策（`InteractionCard` 投影，OPEN 时有黄色角标）、交接说明（原头部卡片整体搬进页签，版本选择 / 编辑 / 预览 / 历史只读不变）、评论、事件（按需展开才拉第一页的行为不变）。
- 原来纵向堆叠的约 12 个块收敛：`dt` 里的英文标签（id / status / owner / project / progress / created / updated）走 i18n；旧的 counts-card 进度块删除（头部 `ProgressBar` 取代），`canFinish` 提示移到头部进度条下。

**i18n**

- 新增 `web/src/i18n/pages/plans.ts`（zh/en 同名同数），key 前缀 `plans.`，涵盖总览与详情的：页头 / 页签 / 元信息、链控制与 leader /notify 开关状态、blocked 横幅、用量块、任务表、待办工具栏与派发字段编辑表单、看板落点 toast、交接说明全部文案。
- 待办循环里的变量名是 `t`（Todo），会遮蔽 i18n 的 `t()`，所以循环内用同名别名 `tr`（脚本里 `const tr = t`，注释已写在定义处）。
- 未迁移（如实记录）：`utils/planControls.ts` 的 `PLAN_CONTROL_HELP` 文案（链控件标签与说明，共享 util，另属 P5 的公共文案），以及 `PlanSupervisor` / `PlanBoard` / `InteractionCard` / `CommentThread` 等共享组件内部的文案。

**测试**（§13）

- 新增 `views/planDetailTabs.test.ts`：六个页签声明与 `active ===` 门控、默认 `todos`、链控制在页签之外、`BackLink` 回总览、头部 `planProgressModel` + `failed_jobs`、`?tab=` 的读写（默认页签不写 URL）、总览的待验收段 / 卡点行 / 已暂停·自动推进标记、手机看板一次一列。
- 同步修改：`ui/progress.test.ts`（新增 `needs_review` / `failed_jobs` 两段与缺字段按 0 的口径）、`utils/planControls.test.ts`（`ops-label` 与主会话标签改成断言 i18n key）、`views/uiTextWiring.test.ts`（Plans 标题从 `PAGE.plans` 改为 `t('plans.list.title')`）。
- `npx vue-tsc --noEmit`、`npx vitest run`（195 个文件 / 1317 个用例）、`npx vite build` 三命令全绿。

**对照 §7 字段清单的核对**

| §7 要求 | 落点 |
|---|---|
| 返回「计划」 | 头部 `BackLink`（文案「计划 / Plans」，回 `/plans` 并复原筛选与滚动） |
| 项目 + id、标题、状态 | 头部标题 + 元信息（项目来自 plan.project，详情页 head-card） |
| 主会话（改绑、清除） | 头部元信息一行（名称 + 状态 + 跳会话页），完整面板仍是 `PlanSupervisor` |
| 暂停 / 恢复、标记完成 | 头部状态操作 + 链控制行（挂起 / 继续 / 解除阻塞） |
| 更多菜单：归档、重新打开、在任务列表查看 | 头部「更多」菜单（`/jobs` 链接） |
| 大号分段进度条与图例、job 运行数 / 失败数 / 最近活动 | 头部 `ProgressBar` + `head-progress-meta` |
| 描述 | 头部 `head-desc`（元信息里的 description 行随之去掉，避免重复） |
| 链控制行（链状态、leader、notify、启动、暂停/恢复、解除阻塞、帮助） | 头部 `chain-ctl`，行为与文案来源未变 |
| 用量 | 头部操作条合计 + 「用量」块（jobs 与会话分开、缓存 token），位置从纵向区块改成头部区 |
| 待办：看板 / 列表切换 + PlanBoard 五列与跳过落点 | `todos` 页签，拖拽与确认逻辑未改；手机上改成一次一列 + 列标签切换 |
| 任务（job 表） | `jobs` 页签（状态 / 标题·ID / agent / runner / 开始 / 耗时） |
| 附加任务并入「任务」 | `jobs` 页签第二个区块 |
| 决策 | `decisions` 页签（OPEN 黄角标） |
| 交接说明 | `handoff` 页签（版本 / 新增 / 修改 / 预览 / 历史只读） |
| 评论 | `comments` 页签（`CommentThread` plan 级） |
| 事件 | `events` 页签（折叠 + 加载更多，展开时随 plan 刷新重取第一页） |
| `dt` 英文标签走 i18n | 已迁移（meta 全部走 `t()`） |

手机：页签栏 ≤760 点按目标 40px、横向可滑；看板在手机上**一次一列**（列标签带卡片数，默认 `doing`），其他列在该断点下隐藏，拖拽仍是桌面行为，手机靠卡片上的按钮改状态。

**未做 / 范围边界**

- 没有在浏览器里按 zh/en × 浅/深 × 四种宽度逐页截图（与 P1 同一状态，靠 CSS 口径 + 现有测试）。
- 计划详情的用量块位置在头部区而非元信息网格内部（视觉上紧接链控制行）。

### P3 工作台（2026-10-23，issue gofer-hez1）

落地范围即 §14 表格 P3 行里的 `/workbench`（其余 P3 页面由并行任务负责）。不涉及 Go 代码，没有新增接口，也没有改协议：数据仍来自现有的 `GET /v1/workbench/threads`、`PATCH /v1/workbench/threads/{id}`、`POST /v1/workbench/threads/{id}/turn` 与 `/v1/jobs/{id}`。

**左栏会话列表（`WorkbenchSidebar.vue`）**

- 分组从「按项目」改成「按状态」（§7）：需要你（blocked / review / awaiting_input）、活跃（working）、空闲与离线（idle）、已结束（done，默认折叠）。分组口径在 `components/workbench/threadGroups.ts`（纯函数，6 个会话状态穷举单测），不散在模板里。
- 项目不再当分组：列表头换成 `ProjectFilter`，值是 URL 的 `?project=`（`ui/projectFilter.ts` 的 `useProjectFilter('/workbench')`，换项目重拉）；状态筛选换成 `FilterChips`（单选，再点一次取消）；搜索仍是 `/` 聚焦的输入框；「显示 exec 命令会话」开关与隐藏计数保留（手机上仍隐藏，由抽屉的「＋」承载新会话）。
- 每行：状态字形（`StatusGlyph`）+ 标题 + 置顶标记 + 相对时间 + agent + 轮数 + 时长 + tokens；stalled 与「未读的等待输入」用状态族颜色。已结束组折叠时不渲染（沿用旧的 `v-if` 而非 `v-show`：上百条历史会话不在进场时全建 DOM）。
- **待处理（orphan）计数保留**（评审意见）：服务端把它算在项目组的 `counts.orphan_blocked` / `counts.orphan_review` 里，这些项**不是会话行**（是受阻计划与待决策的计划）；项目不再是分组行，所以改在筛选区下方给一行「另有 N 个待处理（受阻计划 / 待决策）」，点一下去计划列表的「受阻」视图（带当前 `?project=`）。汇总口径在 `orphanWaitingCount`（纯函数单测）。

**中栏对话（`ThreadView.vue` + `ConversationView.vue` + `acpEvents.ts`）**

- 头部改成 §7 的结构：标题可改（点击重命名）、状态 pill、项目 tag、轮数、未提交徽标、stalled、agent·runner，右侧是「过程 / 改动」seg、打开 job 详情、置顶、停止；中继会话的刷新 / 三态 / 移除登记与「现在送」仍在会话面板里（`SessionDrawer` 原样内嵌）。元信息行补回 cwd、轮数、用量、最近活动。
- 时间线（`ConversationView`）：用户消息与 agent 回复仍是气泡；**工具调用默认折叠**——连续的 tool 事件折成一组，摘要显示「N 个工具调用」与失败数，展开才列工具名、状态字形与参数摘要；**审批卡片内嵌**——待处理的 `InteractionCard` 挂在最后一轮下面可直接作答，历史 permission 事件渲染成内嵌决定卡（已允许 / 已拒绝 / 自动允许）。折叠口径与新块模型在 `acpEvents.ts` 的 `timelineBlocks` / `permissionOutcome`（纯函数单测）；`groupACPRounds` 现在保留 prompt / message / tool / permission 四类，思考 / 计划 / 用量 / 结束原因仍只在 job 详情。
- 输入框：发送 / 排队 / 打断后发送三模式 + 附图 + Enter 发送（Shift+Enter 换行）。**排队**是客户端队列：这一轮还在跑时把文本排队，线程一旦能接下一轮就按顺序发出（`watch(canTurn)`），队列条数给「N 条待发」与「清空排队」；**打断后发送**= 取消当前 job 后把文本排到队首，等它停稳再发。服务端没有队列接口，P4 抽 `Composer` 时如果要服务端排队，得单独立接口。
  - **队列持久化**（评审意见）：待发消息按会话写 localStorage（`gofer.workbench.queue.<会话 id>`，`components/workbench/sendQueue.ts`），刷新 / 重开页面后恢复并显示「N 条待发」；发送成功或用户点「清空排队」后删掉该键。坏值（非 JSON、不是数组、混入非字符串）当空队列处理，并有条数 / 单条长度上限，不让一条坏数据把输入框卡死。
- 右侧上下文栏（新组件 `WorkbenchContext.vue`，只在 ≥1280 显示，1100–1279 不显示，§4.3）：会话信息（id、agent、项目、cwd、类型、轮数、用量、开始时间）、未提交数与改动文件（取 `GET /v1/jobs/{id}` 的 `uncommitted_*`）、关联任务（点开 job 详情）与计划 / 待办、待处理权限请求数，以及「过程 / 改动」切换与停止会话按钮（经 `focusedActions.showView` / `stopCurrent` 作用到聚焦窗格）。
- 手机：会话列表改成左侧 `Drawer`（§6.3），中栏的会话头部有抽屉按钮，新会话入口在工作台顶栏与列表头；左右滑动切换的旧手势保留（作用于抽屉的开关）。

**i18n（§9）**

- 本页文案全部进 `web/src/i18n/pages/workbench.ts`（`workbench.*`，zh/en 同名同数），不动 `zh.ts` / `en.ts`：列表与分组、会话头部动作、时间线（工具调用、内嵌审批、空态）、输入框三模式与排队、上下文栏、布局提示与标签页、新会话表单（项目 / agent / runner / 模式 / 计划 / cwd / 锁定目录 / 模型 / 开始）。业务内容（标题、消息、命令、id、路径）不翻译。

**不丢信息核对（对照 §7 字段清单与迁移前的页面）**

| 迁移前 | 现在 |
|---|---|
| 侧栏项目分组、每项目计数、orphan 待处理数 | 状态分组与组内计数（项目改成 chip 筛选，`?project=`）；orphan 计数随项目分组一起去掉，等宽/待验收页仍能看到 |
| 搜索、状态下拉、显示 exec 开关、隐藏计数 | 搜索 + 状态 chip + exec 开关与隐藏计数（手机仍隐藏开关） |
| 每行状态点、标题、置顶、agent、轮数、相对时间、tokens | 状态字形、标题、置顶、agent、轮数、持续时间与相对时间、tokens（另加 stalled） |
| 头部：← 会话、标题重命名、状态、未提交、置顶 / 停止 / 打开详情 / 中继三态 / 移除登记、元信息 | 全部保留（中继三态与「现在送」在会话面板；元信息行补 cwd / 轮数 / 用量 / 相对时间） |
| 子视图「过程 / 改动」 | 头部 seg + 右栏 seg（两处同步） |
| 待处理交互卡（原在对话上方） | 内嵌到对话最后一轮下方，可直接作答 / 转交（ACP 会话原本取到 props 但没渲染） |
| 工具调用（原先只在 job 详情） | 时间线里默认折叠，可展开（§7 要求） |
| 输入框：单按钮发送、Ctrl/Cmd+Enter、附图、一次性批处理不能续接的提示 | 三模式 + Enter 发送、附图、同样的提示；`Ctrl/Cmd+Enter` 仍可用（同时 Enter 也能发） |
| 日志视图尾部 200 行 + 完整日志链接、布局标签页 / 分屏 / 拖拽 / 守护快捷键 / 版本号 | 原样保留（只换外观与文案） |
| 新建会话表单（项目、agent、runner、模式、计划、todo、cwd、锁定目录、模型、prompt、上限提示、只读提示） | 全部字段保留，文案走 t()；入口在列表头「＋」、手机顶栏「＋」与命令面板 |

**测试（§13）**

- 新增 `components/workbench/threadGroups.test.ts`（6 个会话状态的分组穷举、组顺序与标题 key、桶内顺序、orphan 计数汇总）、`components/workbench/sendQueue.test.ts`（按会话键存取 / 清空、跨会话隔离、坏值、条数与长度上限、无 storage）与 `views/workbenchP3.test.ts`（`?project=` 接线、手机左抽屉、≥1280 上下文栏与它的轮数 / 未提交 / 切换 / 停止、三模式与排队 / 打断行为、队列持久化接线、orphan 提示行、工具调用折叠与内嵌审批、文案在页面字典里）。
- 扩展 `components/workbench/acpEvents.test.ts`：`groupACPRounds` 现在保留 tool / permission，新增 `timelineBlocks` 的折叠与失败计数、`permissionOutcome` 的三类判定（原是「时间线只有文本」的用例，按 §7 改成新口径）。
- 同步修改：`views/workbenchMobile.test.ts`、`views/workbenchMobileLighter.test.ts`（已结束组折叠的 `v-if`、exec 开关）、`components/workbench/statusPresentation.test.ts`（文案走 i18n + 状态族）、`components/workbench/threadViewSessionComposer.test.ts`、`components/workbench/workbenchComposerMobile.test.ts`、`components/workbench/workbenchComposerLock.test.ts`、`components/workbench/workbenchRunner.test.ts`、`components/attachmentsWiring.test.ts`（发送按钮的禁用条件按三模式改）。
- 全量 `npx vitest run` 197 个文件 / 1324 个用例通过，`npx vue-tsc --noEmit` 与 `npx vite build` 通过。

**未做 / 如实记录**

- 没有在浏览器里按 zh/en × 浅/深截图核对（本任务环境没有可用的浏览器/截图工具），形态依据 CSS 断点与现有各页手机断言；P6 收尾时统一补截图检查。
- 右侧栏的「计划进度」只显示 job 关联的 plan / todo id（并发一个 `GET /v1/plans/{id}` 才能画进度条，本页暂不为此加请求）；「权限」显示待处理交互数，没有逐条权限明细。
- 队列是客户端队列（按会话持久化）：刷新页面不丢，但**别的设备 / 浏览器看不到**；打断后发送依赖 `cancelJob` 成功。P4 抽 `Composer` 时再定是否要服务端排队。
- orphan 提示行指向的是计划列表的「受阻」视图：`orphan_review` 目前后端从不递增（`internal/workbench/service.go` 只累加 `OrphanBlocked`），所以这一行实际只反映受阻计划与待决策；日后后端开始计 orphan_review 时这行会自动包含它。
- 命令面板仍是工作台内的 `WorkbenchCommandPalette`（§6.5 的全局化在 P1 已交付 `ui/CommandPalette.vue`，两者尚未合并，留给 P4/P5）。

### P3 任务页（2026-10-23，issue gofer-hez1）

落地范围：§7.1 的 `/jobs` 任务列表与 `/jobs/:id` 任务详情，以及 §16 追加的「按宽度并排」要求。不涉 Go 代码、没有新接口。

**列表 / 详情的宽度契约（`web/src/utils/jobsLayout.ts` + `web/src/store/jobsSplit.ts` + `web/src/utils/useElementWidth.ts`）**

| 名称 | 含义 |
|---|---|
| `SPLIT_MIN_CONTENT_WIDTH` = 1280 | 主内容区（`JobsView` 根的实测宽度）达到这个值才可能并排 |
| `DETAIL_MIN_WIDTH` = 760 / `LIST_MIN_WIDTH` = 520 | 并排时详情面板的最小宽度；1280 − 520 = 760，两者是同一个约束的两面 |
| `splitAllowed(width)` / `splitLayout(width, pref)` | 纯函数：宽度不够或用户选了「单栏」就不并排；`NaN` / `Infinity` / 0（SSR、量不到）一律单栏 |
| `readSplitPreference` / `saveSplitPreference` | 「并排 / 单栏」记在 `localStorage gofer.jobsSplit`（默认并排） |
| `jobTabForStatus(status)` | 默认页签：`needs_review` → 验收、`failed` → 日志、其余 → 概览 |
| `JOB_TAB` / `JOB_TAB_ORDER` | 七个页签值（`overview` / `review` / `log` / `changes` / `messages` / `artifacts` / `events`），顺序即页签顺序 |
| `useElementWidth()` | `ResizeObserver` 量根元素宽度，没有 ResizeObserver 时退回 `window.resize` + 首测 |

路由：`/jobs/:id` 的组件从 `JobDetail.vue` 换成宿主 `views/JobsView.vue`（`props: true` 不变）。宿主量自己的宽度：够宽时左列 `Board`、右列 `JobDetail`（都带 `embedded`），不够宽只渲染 `JobDetail`。手机（≤760）主内容区不可能到 1280，自然只有两级页面。

**任务列表（`Board.vue`）**

- 行：`StatusPill`（字形 + 文案）、标题 / id（含 tags）、项目、agent（续接带 ↻）、runner（远端高亮，副行 worker_id）、徽标（未提交、验证失败、只读、角色、渠道、附件、目录锁）、`Signal` + 耗时（**所有状态都显示**，旧版只有 `running` 才显示）、时间。
- `embedded`：并排时隐藏页面标题，只留一行计数；头部多一个「单栏」按钮（写 `store/jobsSplit.ts`）。反方向在单栏且宽度够时由宿主页给「并排」按钮，不然选了单栏就回不去。
- 筛选写回哪个路径由 `listPath()` 决定：并排时留在当前 `/jobs/:id`（右侧详情不丢），否则回 `/jobs`。点行时把当前 `route.query` 带进详情 URL（单栏时返回链接也能回到同一筛选）。
- 列表侧文案（页头、chip、抽屉里的八个筛选维、表头、徽标、空态、分页）全部走 `t('jobs.*')`。
- 并排时左列只有 520–700px，七列表格会横向溢出：`board--embedded` 改成和手机同一种两行行式（标题 + 状态在上，项目 / agent / runner / 信号耗时 / 时间缩成一行，tag 让位给详情页），列头隐藏。

**任务详情（`JobDetail.vue`）**

- 页头：`BackLink`（`list="jobs"`，回来源列表）+ 状态 / 未提交 / 信号 + 快速重建、打开终端、取消、删除记录、释放锁并结束。并排时不显示返回链接（列表就在左边）。
- 页签用 P2 的 `ui/Tabs.vue`：`?tab=` 深链、默认页签不写进 URL、`role=tablist/tab/tabpanel`、← → Home End。页签带计数（变更 = 提交数、消息 = 消息数、产物 = 产物数、事件 = 时间线 + 交互数），验收页签在 `needs_review` 时带黄色角标。
- 各页签内容（全部是旧页面原有区块，只换了位置）：
  - 概览：「去验收」横幅（`needs_review`）、`ApprovalPanel`、元信息网格（完整保留）、渲染命令、最近 6 行日志（取 stdout，没有就 stderr）+ 去日志页签；
  - 验收：`ReviewPanel`；
  - 日志：`LogTape`（跟随 / 换行 / stdout–stderr 切换不变）；
  - 变更：提交列表（sha 可复制、基线短 sha）与未提交 diff 从「产物」里拆出来，空时写一行说明；
  - 消息：传话消息、消息列表（首条 prompt 可展开注入内容）、ACP 会话输入框；
  - 产物：结构化结果、产物文件、文件传输（上传 / 收集 / 跳过）、验证、用量；
  - 事件：待应答 / 已应答交互、事件时间线、评论、通知投递、终端会话、唤醒。
- 产物里的「查看输出」会先切到「日志」页签再聚焦 stderr 末尾（页签没挂载时 `LogTape` 为空）。
- 页头文案、页签名、消息 / 事件 / 产物 / 变更区块的标签走 `t('jobs.*')`（`i18n/pages/jobs.ts`），列表与详情的所有界面文案（含抽屉、弹层、元信息字段、续跑与唤醒表单）都已搬过去。
- 页头与右侧按钮条都允许折行（`flex-wrap`）：并排时详情列只有 760px，按钮不能把卡片撑出横向滚动。

**测试**

- 新增 `utils/jobsLayout.test.ts`：1280 阈值、760 详情最小宽的等价关系、非有限宽度、单栏开关优先、偏好的存取与坏 storage、`jobTabForStatus` 全状态。
- 新增 `views/jobsPage.test.ts`（SSR）：七个页签与 `role=tablist/tabpanel`、`?tab=changes` 深链选中、未知 `?tab=` 回落概览、并排时无返回链接、宿主页在量不到宽度时单栏、列表行的字段与徽标、打开任务带筛选、「单栏」开关。
- 新增 `views/jobsSplit.test.ts`（SSR + mock 宽度 1440）：并排时列表 + 详情同屏、无返回链接、单栏偏好下面板消失并回完整详情页。
- 同步修改：`views/uiTextWiring.test.ts`（Board 标题与 JobDetail 返回链接改断 i18n / `BackLink`）、`components/approvalPanel.test.ts`（待批面板在概览页签）、`views/jobDetailMessages.test.ts`、`views/jobDetailMessenger.test.ts`、`views/jobDetailSessionComposer.test.ts`、`views/jobDetailAgentBudget.test.ts`（中文文案断言改为断 i18n key）。
- 全量 `npx vitest run` 197 个文件 / 1322 个用例通过，`npx vue-tsc --noEmit` 与 `npx vite build` 通过。

**如实记录的范围边界**

- 并排与单栏是固定比例（`minmax` 两列），未做「拖宽分隔条」；宽度够不够由 util 判定，CSS 只负责布局。
- 页头操作还是平铺按钮，没有合并成 §7.1 说的「更多」菜单（按钮集合与旧版一致：快速重建 / 合并到基线 / 释放锁并结束 / 删除记录 / 取消 / 打开终端）。
- 计划待办的进度步骤在任务详情里本来就没有，本页未新增（§7.1 提到的「进度步骤」留给计划详情）。
- 详情页的文案已全部搬进 `i18n/pages/jobs.ts`（含元信息网格字段、续跑表单、产物传输按钮、唤醒表单、diff / 预览 / 终端抽屉）；剩下的三处不在本页范围内：
  - 「等待目录锁」的说明段：它内含行内 `<code>` 标记，要翻译得先拆成多段文案，本轮保留原文；
  - 交付状态的「将 merge / push / pr」：`deliveryModeLabel` 在 `utils/delivery.ts` 返回中文，属工具层，留给 P6 一并改；
  - 用例值、事件名、`正在取消…` 一类的状态值保持接口原文。
- 页签切换会让被隐藏的区块卸载（LogTape、ReviewPanel 的滚动与展开状态不保留），数据在父组件里，切回来重新渲染。
- `LogTape` 在 setup 里读 `window`，node SSR 下不能挂载，所以 SSR 测试只用不渲染日志页签的 `?tab=` 值（该组件本身未改）。

### P4 待验收页与验收面板（2026-10-23，issue gofer-hez1）

落地范围即 §7.1 的 `/review` 待验收页，以及 §7.1 `/jobs/:id` 的「验收」页签里的 `ReviewPanel`（P3 已把面板挂进页签，本轮只改面板自身）。不涉及 Go 代码、没有新增接口：数据仍来自 `GET /v1/jobs?status=needs_review`、`POST /v1/jobs/{id}/accept|reject`、`/logs`、`/diff`、`/review-hints` 与 `memory-candidates`。

**待验收列表（`views/ReviewQueue.vue`）**

- 页头：标题「待验收」+ 一行摘要（`N 个待验收任务 · 等得最久的排最前`，筛选生效时写成 `筛选后/总数`）+ 刷新按钮；「待批准 N →」入口保留，按 §3.1 改成万寿菊黄的 chip（黄色只代表「需要人」）。
- 筛选（§6.4）：项目 chip 用 `ui/ProjectFilter` + `useProjectFilter('/review')`，值进 URL 的 `?project=`（唯一真源，可分享、后退可复原），换值即带 `project` 参数重拉（`listJobs({ status: 'needs_review', project, limit })`）；验证结果 chip 用 `ui/FilterChips`（全部 / 通过 / 失败 / 未验证，带计数），是**纯前端**维度——一次最多取 100 条待验收，不再多打一次接口。
- 每行（桌面九列）：标题 / id、项目、agent、计划 · 待办、验证结果、提交数、用量、等待时长（`ended_at` 起算，1s 一跳）、行内通过 / 拒绝。排序仍是「等得最久的在前」。裁决成功后该行淡出再移除，轮询不会把它塞回来；失败与「已拒绝 / 已续投」用右下角浮层提示（8s 自动消失）。
- 拒绝仍走 `RejectDialog`：理由必填、可勾「拒绝后自动续投」。
- 手机（≤760）：列头隐藏，每行变成一张卡片（标题 + 等待时长一行，项目 / agent / 计划 / 验证 / 提交 / 用量折行），通过 / 拒绝两个按钮等宽铺满。
- 空态区分「现在没有待验收的任务」（附 `--review` / `require_review` / `gofer job review` 三种入手方式）与「当前筛选下没有待验收的任务」。

**验收面板（`components/ReviewPanel.vue`）**

- 页签换成 P2 的 `ui/Tabs`：`role=tablist/tabpanel`、← → Home End、值在 URL。**参数名是 `rtab` 而不是 `tab`**——`?tab=` 已被任务详情自己的七个页签占用，同一 URL 不能放两个页签维度；深链形态 `?tab=review&rtab=diff`，默认（汇报）不写进 URL，认不出的值回落「汇报」。
- 页签保持原有七类：汇报、发现（有 `## 发现但不碰` 才出现）、经验（有候选才出现）、提交（带提交数）、Diff、验证、用量；发现与经验仍夹在汇报与提交之间。各页按需加载（stdout 尾部 64KB / diff / stderr 尾部 64KB / review-hints），但**汇报总是先拉**——它是首页签，也是「发现 / 经验」页签与审阅提示的数据来源，深链到 Diff / 验证时如果只按当前页签加载，那两个页签会一直不出现。
- 面板头：标题、`StatusPill`（替换旧的 `StatusBadge`）、一行说明（可裁决 / 无裁决权 / 已裁决）。
- 内容全部保留：验收标准勾选清单（仅本页有效）、审阅提示折叠卡（删除热点 / 被删注释 / 验证结果行）、发现逐条「建 issue」与复制命令、提交 sha 点击复制、Diff 用 `UnifiedDiff`（下载原文、大文件降级、范围外文件标记）、verify 输出保留尾部 200 行并在没有 verify 时展示汇报「验证」小节（标明 agent 自述）、用量逐项。
- 底部裁决条改成 `position: sticky; bottom: 0`（§7.1「底部固定操作条」）：长汇报不会再把它推走；Accept 文案仍按交付方式（`Accept 通过` / `Accept 通过并<交付方式>`）并提供「本次不交付」，Reject 仍必写理由、可勾自动续投；已验收显示验收人 / 时间 / 备注，无裁决权显示原因（`data-test="rp-blocked"`）。
- 手机（≤760）：两个裁决按钮等宽铺满；汇报与输出块限高改小，给操作条留位置。

**i18n（§9）**

- 两处文案全部进 `web/src/i18n/pages/review.ts`（`review.*`，zh/en 同名同数），不动 `zh.ts` / `en.ts`：列表头与列、筛选 chip、行内动作与提示、空态、面板头 / 页签 / 审阅提示 / 发现 / 提交 / Diff / 验证 / 用量 / 裁决条。业务内容（任务标题、汇报正文、提交标题、文件名、命令）不翻译；`utils/reviewGate.ts` 给的「没有审阅权限」等原因是工具层中文，留给 P6。

**不丢信息核对（对照 §7.1 本页规格与迁移前的页面）**

| 迁移前 | 现在 |
|---|---|
| 页头：标题、`N 个待验收任务 · 等得最久的排最前`、刷新中、刷新按钮 | 同左，另加筛选生效时的 `筛选后/总数` |
| 「待批准 N →」入口（N>0） | 保留，改成黄色 chip |
| 列：任务 · 标题 / ID、项目、agent、计划 · 待办、验收、提交、用量、等待、裁决 | 九列全保留（表头文案走 i18n） |
| 行：标题 / 短 id、项目、agent、计划链接（`@click.stop`）、verify 徽标配色、提交数、用量徽标 + title、等待时长 + 结束时间 title | 全部保留 |
| 行内通过 / 拒绝、无权限时一行灰字原因（`rq-blocked`） | 保留 |
| 裁决淡出、失败 / 已拒绝 / 已续投浮层（带新 job 链接） | 保留 |
| 拒绝弹层：理由必填、自动续投、Esc / 点遮罩关闭 | 保留（`RejectDialog` 未改） |
| 空态四种入手方式 | 保留（文案走 i18n），另加「当前筛选下没有」 |
| 面板：五个固定页签 + 有内容时的发现 / 经验、按需加载 | 同左，改用 `ui/Tabs`，深链 `?rtab=` |
| 面板头：标题、状态、说明三态 | 保留（状态改 `StatusPill`） |
| 验收标准勾选清单（N/M 计数、仅本页有效） | 保留 |
| 审阅提示折叠卡（三段） | 保留 |
| 发现：逐条文本、建 issue、已建 / 创建中 / 失败、复制命令、剪贴板不可用时展开命令 | 保留 |
| 经验：`MemoryCandidatesTab`（接受 / 拒绝候选） | 保留 |
| 提交：基线 → HEAD、sha 点击复制、已复制、无提交说明 | 保留 |
| Diff：加载中 / 失败 + 重试 + 无 diff 说明、声明范围与越界文件、下载原文、大文件降级 | 保留（`UnifiedDiff` 未改） |
| 验证：状态 / exit / 耗时、命令、skipped 原因、输出尾部 200 行、无 verify 时展示汇报「验证」小节 | 保留 |
| 用量：八项逐项（缺项 `—`）+ 一行摘要 | 保留 |
| 底部裁决条：Accept（含交付方式与「本次不交付」）、Reject、已验收信息与备注、无权限原因、无验收记录说明 | 全部保留，改成贴底固定 |

**测试（§13）**

- 新增 `views/reviewPage.test.ts`（14 个用例）：SSR 渲染页头 / 摘要 / 空态（含 `?project=` 时的筛选空态）、面板骨架（标题、说明、五个固定页签、`role=tablist`、冲突按钮）、`?rtab=diff` 深链选中、`?rtab=verify` 时的验证状态 / 退出码 / 耗时；源码断言项目 / 验证 chip 的接线、行内裁决与拒绝弹层、淡出与排序、手机卡片断点、面板用 `ui/Tabs` 且参数是 `rtab`、贴底裁决条、以及原有内容（验收标准、发现建 issue、diff 下载、verify 截尾 200 行、用量逐项、换 job 重置）都还在。
- 同步修改：`views/uiTextWiring.test.ts`（`ReviewQueue` 标题从 `PAGE.review` 改成 `t('review.h.title')`，与 Board 同一条口子）、`components/reviewHintsWiring.test.ts`（「审阅提示」「来源：agent 自述」改为断言 i18n key + 字典译文）。`views/readOnlyAndReviewGate.test.ts` 的断言（`reviewGate(job).allowed`、`blockedReason?: string`、两个 `data-test`）本轮刻意一字未改，作为「面板契约没变」的背书。
- 全量 `npx vitest run` 204 个文件 / 1386 个用例通过，`npx vue-tsc --noEmit` 与 `npx vite build --outDir ../tmp/web-dist-check --emptyOutDir` 通过。

**如实记录的范围边界与差异**

- 设计稿 §7.1 写的是「`?tab=` 深链」，实施里面板用 `?rtab=`：`tab` 已被任务详情的页签占用，一个 URL 只能有一个 `tab` 维度（详见上文）。
- 预览稿（`tmp/gofer-ui-redesign/preview.html`）**没有** `/review` 列表页，也没有完整验收面板（侧栏有入口但点不进）。所以本页形态按 §7.1 的字段清单 + 预览里最近的形态（任务列表的 `.jrow` 行、Today 的 `.dcard` 验收卡、详情页的 `.review-bar` 与 `.save-bar`）实现，而非逐像素对齐。
- 视觉自查用开发服务器 + playwright，把 `/v1` 请求 mock 成固定数据，按 zh/en × 浅/深各截一屏，另加手机两屏（列表、面板）；截图是本机临时产物，未进仓库。
- 新增的「验证结果」chip 是**前端**筛选（一次取 100 条），不是服务端参数；列表本身仍是单页最多 100 条。
- 手机断点用 §4.3 冻结的 760（与 Board 的 900 不同：Board 那条断点是并排布局的口径，本页没有并排形态）。

### P4 会话页（2026-10-23，issue gofer-hez1）

已实施：

- **统一输入框 `web/src/ui/Composer.vue`**（§8）：发送 / 排队 / 打断后发送（只有调用方声明 `modes` 时才显示对应按钮）；排队按会话持久化（`ui/composer.ts`，键 `gofer.<scope>.queue.<id>`，工作台仍是 `gofer.workbench.queue.<id>`，旧队列刷新后还在）；附图（`attachments` + `attachCapability` 才显示，能力位与提示仍由调用方决定）；急件（`urgentOption` 才显示，v-model:urgent）；快捷键 Ctrl/⌘+Enter 总是发送，工作台另传 `submitOnEnter` 保留回车发送；自适应高度（最多视口 40%，手机 28%）；手机点按目标 ≥40px。抽屉与 `ThreadView` 共用同一套队列与提交口径。
- **已替换的输入框**：`SessionDrawer`（附图、接管确认、「起新进程接管并发送」这类专属动作放 Composer 的 `#actions` 插槽，投递模式 auto/hold/force 与「现在送」保留在抽屉头部不变）；`ThreadView`（工作台对话；send / queue / interrupt 三态收进 Composer，打断只发 `interrupt` 让调用方取消当前 job，队首自动发出）。`JobDetail` / `Mail` / `WorkDrawer` 的输入框按任务书留给各自页面任务。
- **`/sessions`**：AGENT / ACP / 终端三组改成 `Tabs`（带计数，等待回复的页签带黄色角标，`?tab=` 可深链）；`ProjectFilter` 走 URL `?project=`，`FilterChips` 按页签给一套状态口径（值即接口状态值，带计数）；卡片仍用 `InfoCard` 外壳（P2 没有交付通用 `Card` 组件），状态徽标保留 `.sbadge`（`StatusPill` 的替换留给 P5 统一做），字段与操作逐项保留。
- **`Composer` 的 API 覆盖 §8 列出的其他输入框**：调用方决定显示哪些（`modes`：send / queue / interrupt）、能不能发（`canSend` / `canInterrupt`）、排队归属（`queueScope` + `queueKey`）、附图（`attachments` + `attachCapability`）、急件（`urgentOption` + `v-model:urgent`）、说明与折叠（`hint` / `hintCollapsible`）、回车是否即发（`submitOnEnter`），专属按钮走 `#actions` 插槽——`JobDetail` 的 ACP 消息框、`Mail` 回复框（急件）、`WorkDrawer` 后续按各自页面任务接入时不需要改 Composer。
- **`NewSessionSheet`**（`web/src/ui/NewSessionSheet.vue`）：底部抽屉表单（`Sheet` 外壳），从 `Sessions.vue` 迁入全部字段与校验：类型、项目（上限收窄 + 说明）、agent（上限收窄）、runner（`runnerChoice` 的灰显原因）、模型、工作目录、锁定目录、标题、第一句话、完整配置链接；创建后的跳转（PTY → 终端、ACP → 工作台对话）留在会话页。
- **会话抽屉外壳**：`SessionDrawer` 的内容不动，外壳换成 `ui/Drawer`（新增向后兼容的 `embedded` / `flush` 两个可选属性：内联渲染、给时间线自己管滚动）；标题与关闭按钮由 Drawer 提供，状态徽标与工具栏留在内容顶部。
- **i18n**：页面文案进 `i18n/pages/sessions.ts`（zh / en 并列），Composer 的通用文案进 `zh.ts` / `en.ts` 的 `composer.*`（§9.2 已登记该域）。
- **测试**：新增 `ui/composer.test.ts`（队列存储、快捷键、按声明渲染模式 / 急件 / 附图）与 `views/sessionsP4.test.ts`（页签、项目 / 状态筛选、NewSessionSheet、Drawer 外壳、Composer 接线）；同步改了断言旧结构 / 旧文案的 `sessionsComposer`、`sessionsLockPaths`、`sessionsCards`、`sessionsAcpList`、`s8SessionMessage`、`attachmentsWiring`、`drawerAttachPolish`、`ceilingWiring`、`uiTextWiring`、`workbenchMobileLighter`、`threadViewSessionComposer`、`workbenchP3`。

与规格的差异 / 未做：

- §7.1 说 InfoCard 换成「统一的 Card 加 StatusPill」：P2 只交付了 `StatusGlyph` / `StatusPill`，没有通用 `Card`，本页继续用 `InfoCard`（字段不变），等 P5 统一替换。
- `WorkbenchComposer`（工作台的「＋」新建会话表单）本轮没有换成 `NewSessionSheet`：任务书只要求替换会话页、SessionDrawer 与 ThreadView 的输入框，工作台的新建入口留给工作台任务。
- 会话抽屉内侧的内容（消息时间线、元信息、接管 / 唤醒说明等）仍是中文：它在 P4 之前就是共享组件（工作台也用它），文案量大，留给 P5 的文案抽取统一做；本次新增 / 改动的抽屉外壳文案与 Composer 文案已进字典。
- 卡片外壳 `InfoCard` 的「详情 / 收起」和几个工具层文案（`sessionResume` 的接管 / 唤醒原因、`sessionOwner` 的转移提示）仍是中文：属共享组件 / 工具层，P5 一并处理（`sessionTopic` 的「可能不在线」已改用页面字典）。
- **视觉检查**：用构建产物 + Playwright（本机 Edge）+ 桩接口把 `/sessions` 按 1440 / 390 宽 × zh / en × 浅 / 深 截图看过，会话抽屉（Drawer 外壳 + Composer）也看了桌面 / 手机两张（截图在 `tmp/`，不入库）；据此修了四个问题：`watch(activeTab)` 放在列表 ref 之前触发 TDZ（整页白屏）、页签标题与大组标题重复、手机页头未堆叠、Composer 与 AttachmentBar 重复显示一行附图说明。
- 浅 / 深两边都用了现有 token；抽屉内侧文案（时间线、元信息、按钮）仍是中文，见下文「未做」。

### P4 信箱页（2026-10-23，issue gofer-hez1）

落地范围即 §7.1 的 `/mail` 信箱页（`views/Mail.vue` + 本页专属组件 `components/mail/*`）。不涉及 Go 代码、没有新增接口：数据仍来自 `GET /v1/mail`、`GET /v1/mail/conversations`、`POST /v1/mail`（含 `dry_run` 预览）、`/reply`、`/ack`、`/cancel`、`/readdress`、`GET /v1/mail/agents`。

**布局（`Mail.vue`）**

- 桌面两栏（§7.1、预览 `.mail`）：320–340px 左栏 + 自适应线程，整块是圆角面板、两栏各自滚；满高布局（`AppShell` 的 `shell--fill` / `content--fill`）不变。
- 左栏自上而下：标题 + 对话数 + 刷新 + 写信 → 筛选 chip（全部 / 收件 / 发件 / 待指派，带计数）+ 项目 chip + 会话过滤 chip → 对话列表（人物卡对、摘要、急件 / 待指派徽标、未读数、信件数与活动时间）→ 常驻 agent（`ResidentAgents`，原来在整页顶部，按 §7.1 挪进左栏底部）。
- 右栏：线程头（手机上是 `BackLink` 返回列表）→ 时间线（顶部「加载更早的 N 条」，`N` = `MAIL_TIMELINE_PAGE`；右边缘小地图与「还有更早的信件未加载」提示、点击跳转与高亮带不变）→ 底部回复框。
- 待指派的信仍在气泡里显示原因、候选、改投 / 撤回与「在收集箱处理」链接；广播副本卡仍带整组收件人状态条。
- 手机（≤760，§4.3 冻结断点）：列表 → 线程两级，返回用 P2 的 `ui/BackLink`（`to` 带上当前 filter / session / project，返回不会丢筛选）。

**写信与回复都走 `Composer`**

- 写信进 `ui/Sheet`（§7.1「写信放进 Sheet」）：收件人标签、会话多选 + 搜索、按项目 / 主题 / agent / 子路径的「全部」选器、手写地址（高级）、服务端 `dry_run` 投递数预览全部保留；正文与急件改用 `ui/Composer`（`urgent-option` + `v-model:urgent`），发送按钮即 Composer 的发送。
- 回复框同理：`Composer` + 急件；只读 / 他人的信灰显改成 Composer 的 `can-send` + `hint`（原因仍来自 `utils/pageGate.ts` 的 `pageWriteBlock`，服务端权威）。
- 文案（发送 / 回复 / 急件 / 排队等）用 Composer 自己的 `composer.*` 字典，页面不再各写一份。

**筛选与计数**

- 筛选 chip 用 P2 的 `ui/FilterChips`，计数来自一次并行查询：收件 / 发件 / 全部的 `conversations.count`，待指派按 `GET /v1/mail?state=held` 的本地分组算对话数。计数失败不影响看信（保持上一次的值）。
- 项目 chip 用 P2 的 `ui/ProjectFilter` + `useProjectFilter('/mail')`，值写进 URL 的 `?project=`（唯一真源）；conversations 接口没有项目参数，所以按参与方的 `persona.project_key` 在本地收窄（`utils/mailConversations.ts` 的 `mailConversationInProject`，两端任一属于该项目即算）。

**i18n（§9）**

- 本页文案全部进 `web/src/i18n/pages/mail.ts`（前缀 `mail.`，zh / en 同名同数，已登记进 `i18n.test.ts` 的 `pageDomains`），不动 `zh.ts` / `en.ts`：页头、筛选、列表、线程、信件状态（`mail.state.<状态值>`）、广播、待指派、写信、收件人标签、投递预览与结果提示、小地图、常驻 agent、人物卡状态词。
- 页面里原先由 util 拼的中文（`mailStateLabel` / `mailAddressLabel` / `mailComposePreviewText` / `MAIL_FILTERS` 的 label / `MAIL_SCOPES` 的 label 与 hint）改在页面按字典拼；`utils/mail.ts` 的对应函数保留（`MailButton` 与既有单测还在用）。

**不丢信息核对（对照 §7.1 本页规格与迁移前的页面）**

| 迁移前 | 现在 |
|---|---|
| 页头：标题「信箱」+ 实验性徽标、刷新、写信 / 收起 | 标题 + 徽标、对话数、刷新、写信（写信改成打开 Sheet，按钮不再兼作收起） |
| 页签「全部 / 收件 / 发件 / 待指派」 | 四个筛选 chip（**新增计数**），值仍写回 URL |
| — | 项目 chip（§6.4，新增）、会话过滤 chip（原样） |
| 对话列表：两个人物卡、摘要、未读数、急件、信件数、活动时间 | 全部保留，另加「待指派」徽标（§7.1） |
| 常驻 agent（整页顶部，名称 / 项目 / 会话 job / 信件数 / 空闲 / 状态 / 结束） | 挪到左栏底部，字段与按钮不变，文案走字典 |
| 线程头：返回「← 列表」、两端人物卡、封信数 | 保留；返回改用 `BackLink`，只在小屏显示 |
| 时间线顶部「加载更早的信件（还有更早的）」/「已到最早」 | 改成「加载更早的 N 条」/「已到最早」（`mail-older-load` / `mail-older-end`） |
| 小地图（标记、悬停、点击跳转、视口框、未加载提示） | 保留（组件只改了颜色 token 与 aria 文案） |
| 气泡：分边、回信「回复 #N」跳转与高亮、急件、状态、时间、长信折叠 | 全部保留（状态文案改走字典） |
| 广播副本卡：组 id、收件人 / 已送达 / 已回复 / 过期 / 待送达、逐条状态 | 保留（文案走字典） |
| 待指派：原因、候选、改投、撤回、收集箱链接 | 全部保留 |
| 回复框：正文、急件、发送中、错误提示、只读灰显原因 | 正文与急件改用 `Composer`，发送中 / 错误 / 只读原因保留 |
| 写信：收件人标签、会话多选 + 搜索、四种「全部」选器、手写地址、投递数预览、正文、急件、错误 | 全部保留（改成 Sheet + Composer 承载），发送按钮不再有「收起」语义 |
| 提示条（已发出 / 群发 / 转待指派 / 已撤回）+ 错误条 | 保留，文案走字典 |
| 手机：列表 / 时间线两级切换 | 保留，断点从 900 改成冻结的 760（§4.3） |

**测试（§13）**

- 新增 `views/mailPage.test.ts`（11 个用例）：SSR 渲染页头 / 四个筛选 chip / 项目 chip / 常驻 agent / 空态与「选一个对话」、`?project=` 的空态、`?session=` 的会话 chip、英文字典无残留中文；源码断言 Sheet + 两处 Composer（急件、`can-send` + `hint`）、FilterChips / ProjectFilter / `useProjectFilter('/mail')`、「加载更早的 N 条」、待指派与广播、手机两级 + `BackLink`；`mailConversationInProject` 的空项目 / 命中 / 不命中 / 无 persona。
- 同步修改：`views/mailWiring.test.ts`、`views/mailConversationsWiring.test.ts`、`views/mailMinimapWiring.test.ts`（旧的中文文案、`mail-tab-*`、`<textarea>` 断言改成 i18n key / chip / Composer，断点 900 → 760）、`views/pageWriteGate.test.ts`（回信灰显改成 Composer 的 `can-send` / `hint`）、`views/experimentalBadges.test.ts`（标题锚点改成 `t('mail.h.title')`）、`i18n/i18n.test.ts`（页面域加 `mail.`）。
- 全量 `npx vitest run` 208 个文件 / 1421 个用例通过（新增用例后 209 / 1432），`npx vue-tsc --noEmit` 与 `npx vite build --outDir ../tmp/web-dist-check --emptyOutDir` 通过。

**如实记录的范围边界**

- **视觉检查**：用构建产物 + 本机 Edge（headless，CDP 驱动）把 `/mail` 按 zh / en × 浅 / 深各截一屏（1440×900），另加手机（390×844）一屏；`/v1` 请求用临时樁数据（`tmp/visual/`，不入库）。据此改了三处：左栏的常驻 agent 从七列表格改成两行式列表（320–340px 宽度下不再折成一堆碎块，字段不变）、小地图轨道从 14px 加到 22px 并把标记与视口框改成能看清的形状、页头与筛选用 P0 token 对齐预览（浅 / 深两边对比度都够）；手机上另修一处：小地图不画时它的「还有更早的信件未加载」提示不再叠在气泡上（顶端已有「加载更早的 N 条」）。
- 筛选计数是提示值：收件 / 发件 / 全部取 conversations 接口的 `count`（受服务端 `limit` / 扫描页数上限约束），待指派按本地分组算对话数，与信件数不是同一个口径。
- 项目筛选是**前端**收窄（服务端 conversations 没有项目参数），一次最多取 100 个对话；跨项目的对话在两个项目下都会出现。
- 写信的「正文」按钮由 Composer 提供：Composer 在正文为空时禁用发送，原来那种点了才提示「先写点内容」的路径不再走到（`buildMailSend` 的校验保留兜底）。
- 仍留在工具层的中文：`utils/mail.ts` 的 `MAIL_STATE_LABEL` / `mailAddressLabel` / `mailComposePreviewText` / `MAIL_FILTERS` / `MAIL_SCOPES`（页面已不用它们出文案，`MailButton` 与单测还在用）、`utils/mailMinimap.ts` 的悬停文案、`utils/mailConversations.ts` 的 `personaStateLabel` 回退与 `mailReplyLabel`——P6 统一处理。

### P4 工作项页（2026-10-23，issue gofer-hez1）

落地范围即 §14 表格 P4 行的 `/work`（`views/Work.vue` + `components/WorkCard.vue` + `components/WorkDrawer.vue` + 新的 `utils/workBoard.ts`）。不涉及 Go 代码，没有新增接口：数据仍来自 `GET /v1/work-items`（看板与收集箱两次查询）、`GET /v1/agent-sessions`、`GET /v1/work-items/merge-suggestions`、`PATCH /v1/work-items/{id}`、`POST /v1/work-items/{id}/suggestions/*`、`/report-request`、`/summarize`、`/attach|detach-session`、`/link|unlink`、`/merge`、`/split`、`/note`、`/to-todo`、`DELETE /v1/work-items/{id}`。

**页面结构（`views/Work.vue`）**

- 页头：标题「工作项」（迁移前写的是「工作」）+ 一行摘要（`N 个未结束的工作项 · 等我 M 个`，没有时写「还没有未结束的工作项」）+ 右侧「按状态 / 按工作区」分段、显示已完成、刷新、＋新建。
- 列表头：`ProjectFilter`（§6.4，值进 URL 的 `?project=`，唯一真源）+ `FilterChips`（全部 / 等我 / 到期提醒 / 未整理，带计数，单选再点取消）+ 搜索框 + 「清除筛选」。计数与分栏都按项目收窄后的结果算。
- 下面仍按原来的顺序：管家合并建议 → 待整理（草稿 / 收集箱两页签，`?tab=capture` 深链不变）→ 筛选结果 / 按状态 8 列 / 按工作区分组 → 空态。
- 手机（≤760，§4.3 冻结断点）：所有区块单栏，空的列不占位，chip 行在框内横向滑动（`FilterChips` 自带），搜索框与分段按钮的点按目标 ≥40px；点开一条进入铺满宽的 `Drawer`（两级页面的详情层）。

**卡片（`components/WorkCard.vue`）**

- 状态徽标改用 P0 的 `StatusPill`（状态族字形 + `status.<值>` 译文，手动设置的状态另起一个小字「手动」）；`InfoCard` 外壳保留（P2 没有交付通用 `Card`，与 P4 会话页同一取舍）。
- 卡片标记：到期（提醒到期 / 搁置到期）、**停滞 N 天**（新增）、未整理、会话已离线，各自带说明 `title`。
- 字段与动作逐项保留（见下表）。

**详情抽屉（`components/WorkDrawer.vue`）**

- 外壳换成 P2 的 `ui/Drawer`（§8「WorkDrawer … 的外壳」）：`role=dialog` + `aria-modal`、Esc / 点遮罩关闭、焦点循环与归还、打开时 body 不滚；宽度 `clamp(640px, 52vw, 1040px)`，手机铺满宽。关闭仍把意图交回页面（页面清 `?id=`）。
- 头部一行：`StatusPill`（`size=md`）、到期标记、健康度（阻塞 / 停滞 / 有风险时才出现，带原因）、刷新按钮；标题来自工作项标题（Drawer 的 `label`），关闭按钮由 Drawer 提供。
- 抽屉内写备注的输入框改成 P4 已合入的 `Composer`（发送按钮即「写入」，Ctrl/⌘+Enter 提交，手机点按目标 ≥40px）；备忘录式的其它表单（标题 / 目标 / 阻塞 / 下一步 / 摘要 / 项目 / 优先级 / 工作区、搁置与提醒、合并 / 拆分）仍是普通字段，因为它们不是消息输入框。
- 其余区块与按钮一个不少：收集箱分派、状态按钮 + 三种状态来源说明 + 交还给自动判定 / 标为已整理、整理建议（采纳 / 忽略、把握百分比、作者与时间）、内容表单与保存（409 冲突提示）、搁置与提醒（预设时间 + datetime-local）、会话（请它汇报 / 请它写交接 / 整理、请求账本一行说明、当前会话的打开 / 唤醒 / 取消关联、历史会话、关联会话）、关联项（job / plan / todo / issue 跳转、待同步标记、移除、转 todo、手动关联）、合并与拆分、日志（里程碑 / 全部切换、作者标注、来自合并前的工作项）。
- 转为 todo 的小对话框保留（选 plan / 新建 plan、409 时提示已转过）。

**i18n（§9）**

- 本页文案全部进 `web/src/i18n/pages/work.ts`（前缀 `work.`，zh / en 同名同数，已登记进 `i18n.test.ts` 的 `pageDomains`），不动 `zh.ts` / `en.ts`：页头、chip、新建、合并建议、待整理、卡片全部字段与标记、详情抽屉各区块与提示消息、整理建议字段名、请求账本的种类与状态。
- 请求账本的一行说明改由 `utils/workBoard.ts` 的 `workRequestLine` 按字典拼（`utils/work.ts` 的 `requestLine` 保留给既有单测，页面不用它）；状态文字统一用 `status.<值>`，不再取 `utils/work.ts` 的 `WORK_STATUSES` 中文 label。
- 业务内容（工作项标题、目标 / 阻塞 / 摘要原文、agent / 项目 / 工作区名、id、日志正文）不翻译。

**不丢信息核对（对照 §7.1 本页规格与迁移前的页面）**

| 迁移前 | 现在 |
|---|---|
| 页头 h1「工作」 | 标题「工作项」（+ 一行摘要） |
| 按状态 / 按工作区分组（记在 `gofer.work.view`） | 保留（分段按钮） |
| 显示已完成 | 保留 |
| 刷新 / ＋新建 / 收起 | 保留（＋新建换成主按钮） |
| 计数徽标「等我 / 到期提醒 / 未整理」（点击筛选） | 四个 chip（新增「全部」与计数），再点一次取消 |
| 搜索（标题 / 目标 / 阻塞 / 项目 / 工作区 / id）+ 清除筛选 | 保留（清除筛选在有筛选或搜索时出现） |
| — | 项目 chip（§6.4，新增，`?project=`） |
| 新建表单（标题 + 目标） | 保留 |
| 管家的合并建议（采纳 / 忽略） | 保留 |
| 待整理：草稿 / 收集箱页签、说明、收集箱条目（类型 / 项目 / 来源 / 时间）、空态 | 保留（文案走字典） |
| 筛选结果 / 状态 8 列 / 工作区分组 / 空态 | 保留 |
| 卡片：状态（+ 手动）、到期、未整理、会话已离线、会话行、工作区、最近活动 | 全部保留（状态改 pill；新增停滞标记） |
| 卡片：目标 / 阻塞 / 下一步 / 字段来源标注 / 整理建议 / 请求进度 / 关联标签 | 全部保留 |
| 卡片详情：目标、阻塞、下一步、摘要、项目、工作区、搁置、提醒、来源、状态来源、ID、会话列表、用量、关联 | 全部保留 |
| 卡片动作：打开会话、唤醒、整理、标状态、编辑 | 全部保留 |
| 抽屉：刷新 / 关闭 / 标题 / 状态 / 到期 / 健康度 | 保留（关闭由 Drawer 提供） |
| 抽屉：收集箱分派、状态按钮与来源说明、交还自动 / 标为已整理、完成 / 放弃 / 重新打开 / 删除 | 全部保留 |
| 抽屉：整理建议、内容表单与保存、搁置与提醒、会话区、关联项、合并 / 拆分、日志与里程碑切换 | 全部保留 |
| 抽屉：写备注 + 写入 | 改成 `Composer`（同一个 `addWorkNote`） |
| 转 todo 对话框 | 保留 |

**测试（§13）**

- 新增 `utils/workBoard.test.ts`（9 个用例）：计数口径（只算未结束、未整理取草稿）、项目收窄（空 = 全部 / 命中 / 不命中）、`stalledDays`（阈值、已结束 / 搁置 / 草稿 / 在途请求不算、结束后重新计入）、`dueLabelKey` / 来源与建议字段 key、`workRequestLine` 的进行中 / 已送达（带超时分钟数）/ 已回复 / 超时改为整理 / 失败带原因，以及跟随 en 字典。
- 新增 `views/workPage.test.ts`（12 个用例）：页面 / 卡片 / 抽屉 / utils 用到的 `t()` 与 `work.*` / `status.*` key 在 zh 与 en 字典里都存在、两份 key 集合相等、英文串里没有汉字、卡片停滞标记（显示 / 已结束·搁置·草稿·在途请求不显示）、`StatusPill` 与到期 / 离线标记、en 下的界面文案（业务内容不翻译）、页头→筛选顺序、空值 chip 映射到「全部」、`Drawer` 宽度 + `flush` + 手机断点、抽屉里 14 个操作锚点、备忘录走 `Composer`。
- 同步修改：`views/work.test.ts`（计数徽标改成 chip + `workCounts`、项目 chip 接线、断点 640 → 760、状态列改 `StatusPill`、抽屉换成 `Drawer` + `Composer`、409 文案断言改 key）、`components/workCard.test.ts`（「手动」标记改成独立元素）、`components/today/todayLanes.test.ts`（日志粒度切换的文案断言改 key）、`i18n/i18n.test.ts`（页面域加 `work.`）。
- 全量 `npx vitest run` 210 个文件 / 1445 个用例通过，`npx vue-tsc --noEmit` 与 `npx vite build --outDir ../tmp/web-dist-check --emptyOutDir` 通过。

**如实记录的范围边界与差异**

- 设计稿 §7.1 的「健康 / 停滞标记」：列表接口（`WorkItem`）**没有** health 字段，只有详情（`WorkDetail`）有。所以列表上的「停滞」是前端口径（未结束、非搁置、非草稿，`last_activity_at` 距今 ≥2 天，且没有在途的汇报 / 整理请求），详情抽屉里显示服务端给的 health。
- 项目收窄是**前端**收窄（一次取 `boardListOpts` 那么多条，与迁移前同一上限），没有把 `?project=` 变成服务端参数。
- 预览稿（`tmp/gofer-ui-redesign/preview.html`）**没有** `/work` 屏，所以本页形态按 §7.1 的字段清单 + 预览里最近的形态（列表行 / 卡片 / 抽屉 / 表单）落地，而非逐像素对齐。
- 仍留在工具层的中文：`utils/work.ts` 的 `statusLabel` / `workspaceLabel` / `dueText` / `actorLabel` / `suggestionLabel` / `requestLine` / `whenPresets`、`utils/todayLanes.ts` 的 `healthText`、`utils/sessionState.ts` 的 `agentStateLabel`（卡片与抽屉的「执行中 / 等待回复」）、`utils/sessionUsage.ts` 的 `workUsageLabel`、`utils/sessionResume.ts` 的唤醒文案、`utils/steward.ts` 的 `mergeSuggestionText`、`utils/capture.ts` 的 `captureKindLabel` / `captureSource`，以及本页专属组件 `WorkDispatchDialog.vue`（收集箱分派对话框）的文案——P6 统一处理。
- 视觉与交互自查：用构建产物 + 本机 Edge（Playwright，headless）+ `/v1` 桩数据（`tmp/visual/`，不入库）把 `/work` 按 zh / en × 浅 / 深各截一屏（1440×900），另加手机（390×844）列表 / 详情两屏。据此只改了一处：卡片上去掉一个与「到期」标记重复的到期小标签（它还是工具层中文，英文界面里会中英混排）；其余按设计稿落地后看起来正常（浅 / 深对比度都够、控制台只有桩服务的 WS 报错、手机无横向滚动）。交互冒烟也跑过：计数 chip 点选后出现「筛选结果」、再点取消；项目 chip 写进 `?project=` 后卡片只剩该项目的；`?id=` 打开抽屉后 Esc 关闭、`?id=` 被清掉、body 滚动恢复；手机「更多」→ 工作项能正常列表（6 张卡）。
- `WorkCard` 的「详情 / 收起」仍由共享的 `InfoCard` 提供（中文），同 P4 会话页的取舍。

### P5 工作流与定时（2026-10-23，issue gofer-hez1）

落地范围即 §14 表格 P5 行的 `/workflows`、`/workflows/new`、`/workflows/:id`、`/schedules`、`/schedules/new`（`views/Workflows.vue`、`views/NewWorkflow.vue`、`views/WorkflowDetail.vue`、`views/Schedules.vue`、`views/NewSchedule.vue`）。不涉及 Go 代码，没有新增或修改接口：数据仍来自 `GET /v1/workflows`（可选 `status`）、`GET /v1/workflows/{id}`、`/events`、`POST /v1/workflows`、`/from-template`、`/rerun`、`/cancel`、`/save-as-template`、模板的 list/source/render/delete、`GET /v1/schedules`（可选 `project`）、`POST /v1/schedules`、`PATCH`、`/enabled`、`/run`、`DELETE`。

**页面结构**

- `/workflows`：页头（标题 + 实验性徽标 + 共 N 个 + 新建按钮 + 轮询指示）+ 带计数的状态 chip（全部 / 各状态，再点一次取消，计数按当前列表）+ 表格。列按 §7.2：状态（`StatusPill`）、工作流标题 / ID、步骤（分段 `ProgressBar` + `当前/总数`）、开始时间、耗时。行整行可点进详情，键盘 Enter 同样进入；列表用 `useListReturn` 记住筛选与滚动位置。
- `/workflows/new`：`ui/Tabs` 两个页签（从模板新建 / YAML，`?tab=` 可深链），`ui/BackLink` 回列表。模板页签：项目 → 模板列表（内置 / 全局 / 项目徽标）→ 变量表单（agent / runner 下拉按项目 allowed_* 与 ceiling 收窄）→ 预览渲染后的步骤 → 提交；模板源文件的查看 / 编辑 / 新建 / 删除与保存层级、提交命令复制全部保留。YAML 页签：等宽编辑器带行号栏（与 textarea 同滚）、重置模板、提交。
- `/workflows/:id`：`ui/BackLink`（文案「工作流」，回原筛选）；头部标题 + `StatusPill size=md` + 取消工作流 + id / 步骤 / 耗时 / 错误；结果表一路 fan 一行（步骤名 · fan 名、重试 / fan 徽标、状态、runner、开始时间、退出码、耗时、JOB 跳转、cwd 与 ref、可展开的失败摘录）；步骤链（并行 / 重试分组，扇出步仍用 `CompareColumns` 对比与择优合并，子工作流入口）；事件时间线；操作：只重跑失败的、从此步重跑、另存为模板（`ui/Drawer`）。
- `/schedules`：页头（标题 + 共 N 个 + 轮询指示 + 新建）+ `ProjectFilter`（值进 URL 的 `?project=`，服务端按项目取数）+ 表格。列：启用开关（`role=switch`）、名称 / id（cron·once 徽标、项目 · agent · runner）、类型 / cron、下次触发（绝对 + 相对，一次性已触发 / 已停用）、上次任务（跳 job）、操作（运行 · 带参数时带「⚙」/ 编辑 / 启停 / 删除）；点行展开被调度的 JobRequest 摘要（id、type、catch_up、cwd、title、timeout、worker_id / labels、tags、参数声明、prompt / cmd 首行）。立即执行参数表单进 `ui/Sheet`（按声明类型生成控件、预填默认、底部预览将设置的环境变量），编辑进 `ui/Drawer`（cron 或触发时间、标题、超时、启停、参数声明增删改；命令 / agent / runner / 项目仍不可改）。删除仍二次确认。
- `/schedules/new`：字段与校验不变（名称、触发类型 cron / once、cron 预设与帮助、延迟或指定时间、项目 / agent / runner 联动、prompt / command、worker 选机、cwd / 标题 / 标签 / 超时 / catch_up / enabled、参数声明编辑器），返回链接换成 `ui/BackLink`，全部文案走字典。

**i18n（§9）**

- 本页文案全部进 `web/src/i18n/pages/workflows.ts`（前缀 `workflows.`）与 `schedules.ts`（前缀 `schedules.`），zh / en 同名同数，已登记进 `i18n.test.ts` 的 `pageDomains`；不动 `zh.ts` / `en.ts`。
- 业务内容（工作流标题、模板名与描述、cron 表达式、参数名、命令、日志、项目 / agent / runner 名、id）不翻译。

**不丢信息核对（对照 §7.2 与迁移前的页面）**

| 迁移前 | 现在 |
|---|---|
| 工作流：标题取自 `PAGE.workflows`、实验性徽标、创建链接、轮询指示 | 保留（标题走 `workflows.title`，新建换主按钮） |
| 工作流：状态下拉筛选 | 带计数的状态 chip（新增「全部」与计数） |
| 工作流列：状态 / 工作流·标题·ID / 步骤 / 开始时间 / 耗时 | 全部保留（状态 byte pill，步骤加分段进度条） |
| 工作流：空态与创建入口 | 保留 |
| 新建工作流：项目选择、模板列表（来源徽标 + 描述）、来源文件查看 / 编辑 / 新建 / 删除、保存层级、提交命令复制、变量表单（agent / runner 下拉、必填 *、说明、校验）、预览步骤、提交 | 全部保留 |
| 新建工作流：YAML 页签、重置模板、解析错误（行 / 列） | 保留（编辑器加行号栏） |
| 详情：返回、状态、取消工作流、id / 步骤 / 耗时 / 错误 | 保留（返回链接文案改为「工作流」并复用 `BackLink`） |
| 详情：结果表（步骤名 · fan 名、重试 / fan 徽标、状态、runner、开始、退出码、耗时、失败摘录、日志跳转、cwd / ref、从此步重跑） | 全部保留（JOB 跳转改按钮，字段名走字典） |
| 详情：只重跑失败的、另存为模板（模板名 / 层级 / 项目 / 提交命令） | 保留（另存为模板进 `Drawer`） |
| 详情：步骤链（并行 / 重试、对比与择优合并、子工作流）、事件时间线 | 保留 |
| 定时：标题、项目过滤（旧：只认 URL）、新建入口、轮询指示、空态 | 保留；项目过滤换成 `ProjectFilter`（读写 `?project=`） |
| 定时：启用状态、名称 / ID、类型 / cron、下次触发、上次任务、操作 | 保留（启用改开关，另加项目 / agent / runner 副行） |
| 定时：运行 / 编辑 / 启停 / 删除、删除确认 | 保留 |
| 定时：展开行（id、type、catch_up、cwd、title、timeout、worker_id / labels、tags、params、prompt / cmd） | 全部保留 |
| 定时：立即执行参数表单（按类型控件、预填默认、desc、环境变量预览） | 保留（外壳换 `Sheet`） |
| 定时：编辑（cron / 触发时间、标题、超时、启停、参数声明增删改） | 保留（外壳换 `Drawer`） |
| 新建定时：全部字段、cron 预设、校验、参数声明 | 保留（返回链接换 `BackLink`，文案走字典） |

**测试（§13）**

- 新增 `views/workflowsSchedulesI18n.test.ts`（3 个用例）：页面里出现的每个 `t('workflows.*' / 'schedules.*')` 在 zh 与 en 都存在、两本页面字典 key 集合相等、英文文案里没有汉字。
- 同步修改：`views/workflowsMobile.test.ts`（改成测接线与结构：760 断点、`FilterChips` / `StatusPill` / `ProgressBar`、列与字典 key、手机卡片布局）、`views/newWorkflowTemplate.test.ts`（页签换成 `ui/Tabs` 与 `?tab=`、YAML 行号栏）、`views/schedulesParams.test.ts`（参数校验文案改 key）、`views/uiTextWiring.test.ts`（标题走 i18n、返回链接换 `BackLink`）、`views/experimentalBadges.test.ts`（锚点改 i18n key）、`views/ceilingWiring.test.ts`（新建按钮类名）、`views/today.test.ts`（定时新建按钮类名）、`i18n/i18n.test.ts`（页面域加 `workflows.` / `schedules.`）。
- 全量 `npx vitest run` 211 个文件 / 1450 个用例通过，`npx vue-tsc --noEmit` 与 `npx vite build --outDir ../tmp/web-dist-check --emptyOutDir` 通过。

**如实记录的范围边界与差异**

- §7.2 的「`/workflows` 加项目 chip」**没有落地**：工作流列表接口（`Workflow`）没有 `project_key` 字段，加一个不改变结果的 chip 会误导人。等列表接口带项目后再补。
- 工作流的状态筛选在前端收窄：一次取全部工作流（同上限），chip 的计数因此是真实总数；没有把 `status` 参数改成前端唯一真源以外的语义。
- 定时列表的项目筛选仍是服务端参数（`GET /v1/schedules?project=`），`ProjectFilter` 只负责把值写进 URL。
- 预览稿（`tmp/gofer-ui-redesign/preview.html`）没有工作流 / 定时屏，形态按 §7.2 的字段清单 + P2/P4 已合入的基元落地，而非逐像素对齐。
- 仍留在工具层的中文（P6 统一处理）：`utils/workflowResult.ts` 的模板保存 / 删除提示与 `templateReadOnlyNote`、`utils/statusText.ts` 的状态名回退、`components/ExperimentalBadge.vue` 的标题、`CompareColumns` 的按钮与合并提示、`api/time.ts` 的 `fmtUntil`（本次已把定时列表里的「待触发」换成 `utils/format.ts` 的 `formatRelative`，工具函数本身留给 P6）。
- 视觉与交互自查：用 vite dev server + 最小路由 + stub 的 `/v1` 响应（自建临时入口，截图完已删）把 `/workflows`、`/workflows/new`、`/workflows/:id`、`/schedules`、`/schedules/new` 按 zh / en × 浅 / 深（1440×900）各截一屏，另加定时列表的手机屏（390×844）。据此改了四处：列表步骤列不再重复显示一次进度（`ProgressBar` 自带分数）、把截断的 id 改为完整 id（避免「ightly-clean」这类只剩尾部的怪样子）、手机上的启用开关不再被最小点按高度拉成胶囊、定时下次触发的「待触发」换成按语言格式化的相对时间（英文界面不再出现中文）。最终一版的截图里没有横向滚动，浅 / 深对比度正常，控制台只有 stub 未提供的 `/v1/ws-ticket` 代理报错。

### P5 Issues、统计、新建任务（2026-10-23，issue gofer-hez1）

落地范围即 §14 表格 P5 行的 `/issues`、`/dashboard`、`/new`（`views/Issues.vue`、`components/IssueDrawer.vue`、`views/Dashboard.vue`、`components/DashboardJobStatus.vue`、`views/NewJob.vue`）。不涉及 Go 代码，接口与字段一个没动。

**`/issues`**

- 页头：标题 + 共 N 条 + 「新建 issue」/「刷新」；去掉眉标「TRACKER」（§7.3）。
- 页签换成 `ui/Tabs`（Issues / 记忆，`?tab=memories` 仍可深链，与今天页的记忆整理卡兼容）。
- 仓库选择换成 `ui/ProjectFilter`（repo 视为项目，值仍是仓库 id；「全部项目」这一项在没有仓库时不动当前选择，页面下方本来就有「暂无已登记仓库」的说明）；「同步」按钮与上次同步摘要并排，同步 job 的链接保留。
- 状态筛选换成带计数的 `FilterChips`（多选，空集合 = 不按状态过滤）；树形 / 平铺、记忆作用域都是同一套分段控件；类型 / 标签 / 关键字仍是输入框，记忆类型 chip 走 `FilterChips`。
- 列表仍是 8 列表格（勾选、ID、标题、状态、优先级、类型、标签、更新）；状态列换 `StatusPill`，行的可点、Shift 连选、连同子项、跨页保留、分页、批量结果全部照旧。手机（≤760）每行压成卡片：勾选 + ID + 状态一行、标题一行、更新时间一行，其余列隐藏。
- `IssueDrawer` 换成 `ui/Drawer` 外壳（Esc 关闭、焦点循环与归还由外壳负责），内容不变：标题 / 类型 / 标签 / 创建人 / 创建时间 / 状态 / 优先级 / 关系（父、子、blocked-by、blocks、其它）/ 描述与 markdown 预览 / 保存 / 备注 / 评论 / 发表评论；只读判断（`writeBlock`）在保存与评论按钮上。

**`/dashboard`**

- 全部配色改状态族 token：完成 = `--done`、失败 = `--fail`、主色 = `--accent`，热力图用 `--done` 与 `--sunken` 的混合，轨道底色 `--sunken`；数字（大数、排名值、用量、时长）一律等宽 + `tabular-nums`（§4.2）。
- 面板 12px 圆角、无普通阴影；分区标题改成小节行（上分隔线），主卡在 ≤1024 两列、≤760 单列，小指标块 ≤760 两列、≤520 单列；活跃度卡里的热力图与「任务状态分布」在 ≤760 纵向堆叠。
- `DashboardJobStatus` 一并迁到新 token 与字典（「任务状态分布 / 全部 job · 不随区间」）。
- 所有标题、口径提示（`data-tip`）、图例、单位、空态、提示条走 `stats.*`；范围与分桶按钮的文案改由 `stats.range.*` / `stats.bucket.*` 取，不再用 `utils/dashStats` 的中文常量（常量仍留给 `copyText` 与别处）。

**`/new`**

- 两栏（§7.1）：左栏项目 / runner / worker 选机 / agent，右栏标题 / 任务书模板（含变量表单与正文预览）/ cwd，任务说明横跨两栏；其余选项全部收进「高级选项」折叠区（模型、agent 参数 / 命令、锁定目录与等锁时长、worktree、交互式、持续会话与其两个上限、只读、预算三项、PTY 行列与录制、tags、超时、上传 / 收集、sync、rebuild 的 env 编辑）。手机上单栏、提交按钮吸底（保留 `env(safe-area-inset-bottom)`）。
- 返回链接换成 `ui/BackLink`（回任务列表并带原筛选），标题随模式变化：新建任务 / 快速重建 / 从会话新开。
- 字段、校验、预填（`?from_item`、`?from_session`、`?lock`、rebuild 继承字段）、上限收窄与灰显原因全部照旧；提交与「重试关联」不变。
- 项目默认值补上 §6.4-3：带 `?project=` 时用它，否则用上次用过的项目（`localStorage gofer.lastProject`，`ui/projectFilter.ts` 的 `defaultProject` / `rememberProject`）；重建与从条目新开随后仍由各自的 prefill 接管。

**i18n（§9）**

- 三页文案全部进 `web/src/i18n/pages/issues.ts`（`issues.`）、`stats.ts`（`stats.`）、`newjob.ts`（`newjob.`），zh / en 同名同数，已在 `i18n.test.ts` 的 `pageDomains` 登记；不动 `zh.ts` / `en.ts`。
- 业务内容（issue / job 标题与正文、仓库 / 项目 / agent / runner / worker 名、路径、命令、模板名、cron、模型名、状态值）不翻译。状态名仍然走 `status.<值>`，没有翻译时回退旧的 `issueStatusText`。

**不丢信息核对（对照 §7 与迁移前的页面）**

| 迁移前 | 现在 |
|---|---|
| Issues：新建 issue 表单（仓库 / 标题 / 描述 / 优先级 / 类型、无仓库提示、错误与成功提示、只读禁用） | 全部保留 |
| Issues：仓库下拉、同步按钮与同步 job 链接、上次同步与冲突摘要 | 保留（仓库换 `ProjectFilter`） |
| Issues / Memories 页签、记忆作用域三选与说明 | 保留（页签换 `Tabs`、作用域换 chip） |
| Issues：全局记忆的「同步状态」折叠区（机器数、最近主机、逐台明细、空态） | 全部保留 |
| Issues：树形 / 平铺、状态 chip、类型 / 标签 / 关键字、记忆类型 chip 与「有标记」 | 保留（状态 chip 新增计数） |
| Issues：选择提示、批量操作栏、批量结果（失败明细、收起）、分页与每页条数 | 全部保留 |
| Issues 表格列：ID、标题（进度 / 连同子项 / 父链接）、状态、优先级、类型、标签、更新时间 | 全部保留（状态换 `StatusPill`） |
| IssueDrawer：全部字段、关系五组、描述与预览、备注、评论、保存 / 评论、只读 | 全部保留（外壳换 `Drawer`） |
| 统计：范围切换、分桶切换、复制统计与两种提示 | 保留 |
| 统计：四张主卡（任务 / 耗时 / Git / 信号，含全部 `data-tip` 口径） | 全部保留 |
| 统计：完成趋势图（SVG，完成 / 失败两色、峰值、轴）、最高产（今日 / 其余两种）、活跃度热力图与图例、任务状态分布 | 全部保留 |
| 统计：Agent 排行、项目 Top 3 + 全部项目折叠、验收与计划四块、耗时最长 / 最快、用量（费用 / 按模型）、口径说明、系统折叠区 | 全部保留 |
| 新建任务：全部 query 预填、项目 / runner / worker / agent 联动与灰显原因、上限收窄与只读提示 | 全部保留 |
| 新建任务：模板与变量、预览、校验提示；prompt / 命令；模型与 agent 参数；锁定目录 / 等锁 / worktree；交互式、持续会话、只读；预算、PTY、tags、上传 / 收集、env、超时与 sync | 全部保留（分区见上） |
| 新建任务：提交 / 提交重建、提交中、结果链接、关联条目成功 / 失败与重试 | 全部保留 |

**测试（§13）**

- 新增 `views/p5bPages.test.ts`（3 个用例）：SSR 渲染 Issues 页的页签（`role=tablist` + 两个 `data-value`）、统一项目 chip、去掉的 TRACKER 眉标与 en 文案；新建任务页（标题 / `ui/BackLink`）。统计页在 SSR 下会去解析 `/gofer-mark-256.png` 公共资源、node 下报错，所以这页不放进 SSR 用例，由 `p5bPagesI18n.test.ts` 与 `dashboardView.test.ts` 覆盖。
- 新增 `views/p5bPagesI18n.test.ts`（3 个用例）：页面里引用的每个 `t('issues.*' / 'stats.*' / 'newjob.*')` 在 zh 与 en 都存在、三本页面字典的 key 集合相等、英文文案里没有汉字（拼出来的 key 跳过，靠 key 集合用例兜住）。
- 新增页面字典后全量 `npx vitest run` 213 个文件通过；`npx vue-tsc --noEmit` 与 `npx vite build --outDir ../tmp/web-dist-check --emptyOutDir` 通过。
- 同步修改：`views/dashboardView.test.ts`（范围 / 分桶 / 复制、最佳时段、手机断点改成断言 i18n key 与 760 断点）、`views/dashboardMobile.test.ts`（断点 760）、`views/newJobMobile.test.ts`（高级选项断言 key）、`views/newJobLockPaths.test.ts`（锁定目录 / 等锁时长断言 key）、`views/uiTextWiring.test.ts`（新建任务标签与返回链接改断言 `t('newjob.*')` / `ui/BackLink`）、`i18n/i18n.test.ts`（页面域加上 `issues.` / `stats.` / `newjob.`）。

**如实记录的范围边界与差异**

- §7.3 的「`/dashboard` 新增项目 chip（只影响按项目以外的板块）」**没有落地**：接口没有按项目切的参数，加一个不改变任何板块的 chip 只会误导人，留待 `/v1/stats/overview` 带项目维度后再补。
- `utils/dashStats.ts` 的 `summaryLine` / `copyText` / `NOTE_TEXT` 文案仍是中文（摘要行与「复制统计」的纯文本）：它们是共享 util，且 `copyText` 的产出要能被粘贴到别处，留给 P6 统一处理。同理 `utils/lockPaths.ts` 的 `DIR_LOCK_HINT`、`utils/ceiling.ts` 的上限文案、`NewJob` 里 `agentEmptyReason` / `validationError` / `itemNote` 这类逻辑层拼出来的提示仍是中文。
- 统计页的「任务状态分布」仍用 `api/client` 的 `statusColor` 取色（沿用接口口径），没有改成 `utils/statusFamily`：两者现在取的是同一套状态色，等 P6 删旧颜色表时一起收敛。
- 本页没有逐像素对齐预览稿（预览稿里没有这三屏）：形态按 §7 的字段清单 + P2/P4/P5-a 已合入的基元落地。
- 视觉自查：用 vite dev server + 临时预览入口（`web/preview-p5b.html` + `src/preview-p5b.ts`，stub 的 `/v1` 数据，截图完已删）把 `/dashboard`、`/issues`、`/new` 按 zh / en × 浅 / 深（1440×900）各截一屏，另加三页的手机屏（390×844）。据此改了两处：热力图月份标尺 12×56px 把手机页面撑出横向滚动（改为容器内裁切）、统计页说明的拼接符改成随语言（中文全角分号 / 英文「; 」）。最终一版三页在 1440 与 390 下都没有页面级横向滚动（热力图与扩散区在各自容器内滚动），浅 / 深对比度正常，控制台只剩 stub 未提供的接口 403。
- `DashboardSystem`（「系统」折叠区）的文案与 token 同步迁移；它的手机断点仍是旧的 640 / 700 / 980（本轮没动，与其它页的 760 不统一，留给 P6）。

### P5 运行节点、Agents、项目（2026-10-23，issue gofer-hez1）

落地范围即 §14 表格 P5 行的 `/runners`、`/agents`、`/projects`（`views/Runners.vue`、`views/Agents.vue`、`views/Projects.vue`）。不涉及 Go 代码，接口一个没动：数据仍来自 `GET /v1/runners`、`GET /v1/projects`、`GET /v1/project/{key}`（含 `/git`、`/repos`、`/file`）、`PUT/POST/DELETE /v1/project…`、`GET /v1/agents`、`GET /v1/config`、`POST /v1/agents/{key}/probe`、`GET /v1/worker-enrollments` 与申请接受 / 拒绝、`POST /v1/workers`、`/reload`、`/upgrade`；项目概览的计数复用 `GET /v1/jobs?project=&status=running`、`GET /v1/plans?project=&status=open`。

**`/runners`**

- 页头：标题（`nav.runners`）+ 一行摘要（`runners.sub`）+ 轮询指示点；去掉眉标「FLEET」。
- 待接受申请置顶（§7.4）：标题、待决定数、来源 / 状态 / 倒计时、host / labels / agents / roots、接受… / 拒绝… 两个按钮、已接受行的领取倒计时、理由行、空态与「自助注册已关闭」提示全部保留；`#enrollments` 锚点与「从 Today / 顶栏跳来补一次滚动」的行为不变。
- 添加 worker（`worker.manage`）与拓扑（可折叠、手机上默认收起、展开状态记本地）保留。
- Workers：名称、id、心跳年龄、in_flight、hostname / 来源地址 / 平台 / gofer 版本 / 运行时长、标签、外部工具（悬停说明）、传话人入口、重新加载配置、一键升级（不可升级原因作为 title）、排空中提示、升级摘要、升级历史（最近 5 次）全部保留；`Heartbeat` 仍是唯一的张扬元素。
- Peers（base_url、探活年龄、延迟、错误）与 Server（in-process、传话人、工具、目录）保留。
- 状态表达全部换成 §5 的字形：worker 在线 = `done`、心跳过期 = `time`（文案带秒数）、离线 = `idle`；申请待决定 = `need`、等领取 = `queue`、已拒绝 / 过期 = `fail`、被取代 / 放弃 = `idle`；peer up = `done`、down = `fail`、未探测 = `idle`。页面里不再有 `.st--*` 静态圆点与自写配色。

**`/agents`**

- 只读健康表保留：检测（可用 / 不可用）、键（可展开）、内置 / extends 徽标、类型与会话数、健康度、版本 / 错误、探针按钮（提交一次探针 job、结果提示条可跳 job）。
- 健康度与检测改用状态字形 + 文字 pill：可用 = `done`、不可用 = `idle`；健康 = 正常（`done`）、供应商错误达阈值 = 降级（`time`）、窗口内无样本 = 无样本（`idle`）。§5 的表没给 agent 健康度定族，这里按「正常 / 有问题 / 无数据」落 `done` / `time` / `idle`，不用黄色（黄色只给「需要人」）。
- 新增「编辑」链接（§7.4 / §11-17）跳到 设置 › 配置管理，查看与编辑不再割裂。
- 展开的配置详情（type、interactive、command、args、session_*、system_inject、env_keys、mcp_server_name、detect.*、allow_raw_cmd）字段与顺序不变，配置键本身不翻译。

**`/projects`**

- 主从布局保留；手机上仍是顶栏项目选择器 + 单栏详情。
- 详情：key、内置徽标 + 说明、host_path / container_path / default_agent / allowed_agents / allowed_runners / rules / allow_exec / allow_interactive / max_concurrent_jobs、跳任务列表的入口全部保留。
- 新增项目概览（§6.4-5）：活跃任务（`status=running`）与进行中计划（`status=open` 的 `total`）两个计数，分别跳到 `/jobs?project=` 与 `/plans?project=`；任一次请求失败就整块不显示，不影响配置与 git。
- 编辑表单（键、默认 agent、主机 / 容器路径、允许的 agents / runners、allow_exec、allow_interactive、最大并发、RULES 逐行编辑器与说明、内置项目保存提示、告警列表、保存 / 删除、删除二次确认、内置项目不可删）与 git 状态卡、子仓库、关键文件查看（含 256KB 截断提示与下载）全部保留，文案走字典。

**i18n（§9）**

- 三页文案全部进 `web/src/i18n/pages/runners.ts`（`runners.`）、`agents.ts`（`agents.`）、`projects.ts`（`projects.`），zh / en 同名同数，已登记进 `i18n.test.ts` 的 `pageDomains`；不动 `zh.ts` / `en.ts`。
- 业务内容不翻译：worker / peer / agent / 项目名、id、路径、命令、标签、工具名、项目配置键、git 分支与提交、错误原文。

**不丢信息核对（对照 §7.4 与迁移前的页面）**

| 迁移前 | 现在 |
|---|---|
| 运行节点：标题、轮询指示、拉取失败错误条、重载 / 升级提示条 | 保留（标题与摘要走字典） |
| 运行节点：待接受申请区（锚点、计数、来源、状态、倒计时、host、labels、agents、roots、接受 / 拒绝、接受后的领取倒计时、理由、空态、关闭态提示） | 全部保留（置顶，状态改字形 + 文字） |
| 运行节点：添加 worker（ID / 标签 / 项目、登记、一次性 token 与命令、错误） | 保留 |
| 运行节点：拓扑（可折叠、手机默认收起、本地记忆） | 保留 |
| 运行节点：Workers 卡片（名称 / id / 心跳年龄 / in_flight / hostname / 来源地址 / 平台 / 版本 / 运行时长 / 标签 / 工具 / 传话人 / 重载配置 / 升级 + 原因 / 排空 / 升级摘要 / 升级历史 / 目录 / 空态邀请） | 全部保留 |
| 运行节点：Peers（地址 / 年龄 / 延迟 / down 错误）、Server（in-process / 传话人 / 工具 / 目录 / 空态） | 全部保留 |
| 运行节点：传话人抽屉、接受 / 拒绝对话框 | 保留（`MessengerDrawer` / `EnrollAcceptDialog` / `EnrollRejectDialog` 未改） |
| Agents：作用域说明（serve 主机 / 内置注入 / worker 见 Runners） | 保留（链接文案走字典） |
| Agents：检测、键（展开配置）、内置 / extends 徽标、类型 + 会话数、健康度 + 悬停口径、探针、版本 / 错误、空态与加载态、探针结果提示条 | 全部保留；新增「编辑」链接 |
| 项目：标题、新增入口、列表（内置徽标）、手机选择器、详情键值、跳任务列表 | 保留（跳转文案改「在任务列表查看」） |
| 项目：编辑表单全部字段与校验、内置项目保存提示、告警、保存 / 删除与二次确认、内置项目不可删 | 全部保留 |
| 项目：git 状态卡（分支 / dirty / 最近提交 / 刷新）、子仓库、关键文件（候选、查看、截断、下载） | 全部保留 |

**测试（§13）**

- 新增 `views/p5cPagesI18n.test.ts`（3 个用例）：页面里引用的每个 `t('runners.*' / 'agents.*' / 'projects.*')` 在 zh 与 en 都存在、三本页面字典的 key 集合相等、英文文案里没有汉字。
- 新增 `views/p5cPages.test.ts`（3 个用例）：Agents 页 SSR 渲染（页头 + 列头文案取自字典、作用域说明里的 `/runners` 链接、首屏空态）。Runners / Projects 用到 `useRoute` / `useRouter`，不在 SSR 用例里，由源码结构与字典用例覆盖。
- 同步修改：`views/runnersEnroll.test.ts`（文案改断言字典 key、状态改字形、`decideCountdownText` / `collectCountdownText`）、`views/runnersMobile.test.ts`（断点改 760，加状态字形断言）、`views/agentsMobile.test.ts`（断点 760、编辑链接、状态字形）、`views/projectsBuiltin.test.ts`（内置提示改断言字典 key）、`views/uiTextWiring.test.ts`（Projects 标题改 `t('nav.projects')`）、`i18n/i18n.test.ts`（页面域加 `runners.` / `agents.` / `projects.`）。
- `npx vue-tsc --noEmit`、`npx vitest run`（215 个文件 / 1464 个用例）、`npx vite build --outDir ../tmp/web-dist-check --emptyOutDir` 全部通过。

**如实记录的范围边界与差异**

- §6.4-5 的「待处理数」（工作项）**没有落地**：web 侧没有按项目取工作项计数的接口，把全部工作项拉下来再数会把页面拖慢；项目概览只给活跃任务与进行中计划，等 `/v1/projects` 或工作项列表带项目维度计数后再补。
- `utils/runners.ts` 的 `upgradeSummary` / `upgradeBlockReason` / `workerStatusText` 仍是工具层文案（升级摘要与不可升级原因在页面上照原样显示，中文），与 P5-a 的处理一致，留给 P6 统一；页面自己的 worker 状态文案（已连接 / 未心跳 / 离线）已走字典。
- `utils/enrollments.ts` 的 `stateLabel` / `decideCountdown` / `collectCountdown` / `disabledHint` 仍被接受 / 拒绝对话框使用；页面侧的同一信息改走字典（`runners.enroll.*`），工具函数本身留给 P6。
- `/runners` 的 `max-width` 从 760 放宽到 900：worker 卡片的信息密度比原来多（状态 pill + 操作按钮一行）。
- 本页没有逐像素对齐预览稿（预览稿没有这三屏），形态按 §7.4 的字段清单 + P2/P4/P5 已合入的基元落地。
- 视觉自查**没有做成**：搭了临时预览入口（`preview-p5c.html` + `src/preview-p5c.ts`，stub 了 `/v1/runners`、`/v1/worker-enrollments`、`/v1/agents`、`/v1/config`、`/v1/projects` 与 `/git`、`/repos`、`/v1/jobs`、`/v1/plans`，vite dev 能起来），但本机 Chrome 在 agent-browser 下启动即退出（`Chrome exited early without writing DevToolsActivePort`，加 `--no-sandbox` 同样失败），拿不到截图。所以 zh / en × 浅 / 深的外观没有经过人眼，只按 CSS 口径（≥1100 完整侧栏、≤760 单栏 + 40px 点按目标）+ 字典完整性用例 + SSR 用例核对。临时入口与 stub 数据已删除。

### P5 项目页「按执行机」视图（2026-10-23，issue gofer-d1kn）

落地范围：`web/src/views/Projects.vue`、新增 `web/src/utils/projectFleet.ts`（纯函数）、`web/src/i18n/pages/projects.ts`、`api/client.ts` 与 `api/types.ts` 各加两个读接口的封装。**后端一行没改**，只用现有接口：`GET /v1/projects`（键 + `injected`）、`GET /v1/projects/{key}`（`allowed_runners`；有配置读权限时先从 `GET /v1/config` 的 `projects` 一次拿全）、`GET /v1/runners`（执行机清单与状态）、`GET /v1/workers/{id}`（`dirs.workspace` 即该执行机的默认工作空间）、`GET /v1/workers/{id}/projects`（worker 自己声明的项目）。

**视图切换**

- `ui/Tabs`，参数名 `view`：默认 `project` 不写进 URL，`?view=runner` 可深链、可前进后退；两个页签带数量（项目数 / 执行机组数）。
- 两种视图各自保留原来的按项目主从布局；执行机视图无需选中态，点项目 chip 即回按项目视图并选中它。

**按执行机**

- server 本机一组 + 每台 worker 一组（同一 worker 上有多个 runner 名时合成一组；`/v1/runners` 里 type=worker 的行才是 worker）。
- 准入口径与后端一致，不在前端自创：`allowed_runners` 为空只放行本机（`internal/project.AllowsLocalRunner`、`internal/core/policy.go` Q8「空 allowlist 不推给任何 worker」），`server` 与 `local` 视为同一个（G043）。
- 组内三行：默认工作空间（server 取本机 `dirs.workspace`，worker 取 `GET /v1/workers/{id}` 的 `dirs.workspace`，目录不存在标「目录不存在」）、可用项目（server 声明且允许它运行）、worker 自有（`GET /v1/workers/{id}/projects` 里 server 没声明的 key，虚线 chip，悬停显示该 worker 上报的解析路径）。
- 离线 worker：整卡灰显（`opacity: .6`）+ `StatusPill`「离线」；心跳上报的项目与目录取不到时按原因写文案（离线 / 在线但未上报），接口失败另起一行显示原始错误。注意 `GET /v1/workers/{id}/projects` 对「已登记但离线」的 worker 也回 200 + 空列表（注册表里没有它的快照），所以**在线与否先看 `connected`**，不能把那个空列表读成「没有自有项目」。

**按项目**

- `default` 行（列表与详情头）加「server 本机」徽标：内置 `default` 指向 server 的默认工作空间，容易与 worker 自己的工作空间混淆。
- 新增「worker 自有项目」一组：跨两列排在主从布局下方，手机上 `order: -1` 排到详情之前；同一 key 被多台 worker 声明时合成一行并列出声明它的 worker。这些项目只读（不在 server 配置里，改要去那台 worker 的 worker.yaml）。
- 表单、项目概览计数、git 状态、子仓库、关键文件、删除与内置项目规则一律未动。

**i18n**

- 新 key 全在 `web/src/i18n/pages/projects.ts`：`projects.view.*`、`projects.fleet.*`、`projects.workerOwn.*`、`projects.defaultLocal*`，zh / en 同名同数；不动 `zh.ts` / `en.ts`。业务内容（项目名、worker id、路径、错误原文）不翻译。

**测试**

- 新增 `web/src/utils/projectFleet.test.ts`（12 个用例）：空 allowlist 只放行本机、`server` 别名、按 runner 名匹配、server 组与它的默认工作空间、worker 自有排除 server 已声明的 key、离线 worker（原因 + 错误原文）、离线 worker 的空项目列表不当作「没有」、在线未上报、`/v1/runners` 为空时仍有 server 组、`workerOwnProjects` 跨 worker 合并、`allowed_runners` 的 config 预填。
- 新增 `web/src/views/projectsFleet.test.ts`（8 个用例）：SSR 渲染两种视图与字典文案、`?view=runner` 深链、默认视图不渲染执行机视图、worker 自有接线、`default` 的本机标记、离线灰显与原因、只用现有接口、手机断点。
- `npx vue-tsc --noEmit`、`npx vitest run`（217 文件 / 1484 用例）、`npx vite build --outDir ../tmp/web-dist-check --emptyOutDir` 全过。

**如实记录的范围边界**

- `GET /v1/projects/{key}` 取不到 `allowed_runners` 时（非管理员且接口失败），该项目的准入名单按「只在本机可用」处理：它仍出现在 server 组，不推给任何 worker。不做猜测。
- 视觉自查没做成截图：本机 Chrome 在 agent-browser 下起不来（P5-c 报 `DevToolsActivePort`，本次连 `open data:text/html,…` 都直接挂住、无输出，3 分钟超时被杀），预览稿也没有这一屏。外观按 token 口径（卡片 `--r-panel`、状态走 `StatusPill`、手机上单列 + chip 36px 点按目标）与 SSR 用例核对，zh / en × 浅 / 深未经过人眼。
- 只做了「两种视图 / 离线 / 默认工作空间」的结构断言，没有真实 `/v1/runners` 联调截图。


### P5 设置与接入（2026-10-23，issue gofer-hez1）

**做了什么**

- **设置外壳**（`views/settings/SettingsLayout.vue`）：二级导航按预览稿重做——桌面（≥761）是 210px 竖排导航（当前项 `--surface` 底 + 加粗 + `--shadow-1` 与内描边，命中由 RouterLink 自己的 `aria-current="page"` 提供），平板收窄到 180px，手机（≤760，与 AppShell 同口径）竖排导航隐藏、换成整行的 `<select data-testid="settings-nav-select">`（选中即 `router.push`，深链进来时下拉框回显当前子页）。页头保留「设置 ▸ 当前子页」的面包屑。导航文案走 `settings.nav.*`（十项）：新增子页 = sections 一行 + 子路由 + zh/en 两个 key。
- **两个基元**（§8 的 `FormSection` / `SaveBar`）：`ui/FormSection.vue` 左边标题 + 一句话说明、右边字段（220px + 内容，≤1099 堆成一行）；`ui/SaveBar.vue` 有未保存修改时 `position: sticky; bottom: 0`，给「放弃修改 / 保存」，没有修改时退回文末（`.save-bar--idle`）且按钮禁用，手机上两个按钮铺满、提示文字隐藏。
- **已迁移的子页**：通知（本设备推送 + 订阅列表，错误与提示也走字典）、MCP（每个 server 一节 FormSection，测试探针结果走插值）、关于（服务端自述 + 更新状态 + 一键更新与等空闲 / 取消等待，更新阶段与 worker 状态按 `settings.about.phase.*` / `settings.about.worker.*` 取，没登记的取值原样显示）、工作项（被动整理 / 请求与交接 / 每日摘要 / 等我通知 / 管家五节，保存改走吸底 `SaveBar`，原有 `data-test` 钩子全部保留）、规则库（编辑器改为 FormSection，表头 / 空态 / 校验与删除确认都走字典；表格结构与 900px 下的横向滚动不动）、权限（页头 + 四个页签的文案、身份条 / 调用方上限条、新增授权表单与预览提示、授权与孤儿的标签与动作、会话授权（含续期说明）、权限目录的搜索与风险档、审计的空态与查询按钮都走字典；权限名、风险 / 范围 / 来源标签与状态文案仍来自服务端与 `utils/permissions.ts`，保持原样）、扩展（每项一张卡的说明 / 类型 / 已改过 / 对比默认 / 恢复默认与二次确认 / 保存与逐键提示，以及导入预览弹层的表头、动作、拒绝说明与写入按钮都走字典；逐键保存模型不变）、技能（标题、范围说明、导入面板、表格列与行内动作、详情块、空态与全部导入 / 更新 / 导出 / 删除的提示与确认都走字典）、隧道（在线转发 / 预设 / 本机未上传三张表、停止流程的确认与状态、预设编辑弹窗的字段与说明都走字典；`utils/forwarderStop` 的权限提示保持原样）。
- **接入页**（`views/Access.vue`）：居中卡片 + 品牌 logo + 标题 / 提示 + token 输入（等宽）+ 连接按钮，右上角主题与语言开关（新增 `LangToggle`）。行为不变：成功后回 `?redirect`（只接受站内路径，默认 `/dashboard`），跨标签页 token 交接仍在 `router.ts` 的公开路由守卫里。
- **文案**：`i18n/pages/settings.ts`（`settings.*`）与 `i18n/pages/access.ts`（`access.*`），zh/en 并列；`i18n.test.ts` 的 `pageDomains` 登记这两个前缀。

**测试**

- `views/settings/settingsLayout.test.ts`（SSR）：十项导航都在、当前项唯一 `aria-current`、手机下拉框列出同样十项并绑定当前值、en 下导航文案翻译。
- `ui/saveBar.test.ts`：吸附/退回、按钮禁用、提示与保存文案可由页面覆盖、翻译。
- `views/settings/settingsPagesI18n.test.ts`：页面用到的 `settings.*` / `access.*` key 两本字典都有、key 集合相等、英文无汉字、迁移页确实用 `FormSection`（工作项页用 `SaveBar`）、通知与接入页模板里没有中文字面量残留。
- 既有测试随迁移改口径：`settingsLayoutMobile.test.ts` 改成测「手机换下拉框 + 选中跳转」（不再断言横向滚动条样式，§13 问题 16）；`settingsTablesMobile.test.ts` 最后一条改为下拉框形态；`workSettings.test.ts` 的中文断言改成查字典并改测吸底保存条；`extensionSettingsWiring.test.ts` 跟随导航项结构（`key:` 代替 `label:`）；`steward.test.ts` / `updateWaitIdle.test.ts` / `uiTextWiring.test.ts` 跟随文案迁移。

**未完成（后续提交）**

- 配置管理（`Config.vue`，1696 行）仍是旧写法：文案未进字典、分节未换 `FormSection`；导航与路由已统一，页面内容未动。`Config.vue` 里的出站 webhook（含 keyword）与事件订阅矩阵保持原样，等该页迁移时再套分节。
- 四态截图（zh/en × 浅/深）与 `web/scripts/shots.mjs` 的自动检查本批未跑。

### P6-a 样式收尾：删别名、断点统一、状态取色收敛（2026-10-23，issue gofer-hez1）

落地范围是 §11 P6 行的三项，不动文案、不动布局结构、不动 Go 代码。

**1）删除旧 token 别名**

`tokens.css` 的「迁移期别名」整块删除，仍在用旧名的 128 个文件改成新 token（`--fg` 改回 §4 命名修正里的 `--ink`，`--ink` 当底色用完换成 `--bg`）：

| 旧名 | 新 token | 迁移前用到的处数 |
|---|---|---|
| `--ink`（页面底） | `--bg` | 74 |
| `--panel` | `--surface` | 104 |
| `--paper`（正文） | `--ink` | 261 |
| `--phosphor`（强调/链接） | `--accent` | 432 |
| `--queue`（灰字/待排队） | `--ink-3` | 430 |
| `--radius`（4px） | `--r-chip`（5px） | 345 |
| `--term-bg` | `--sunken` | 48 |
| `--font-sans` | `--font-ui` | 2（含带 fallback 的一处） |

两处取值需要说明：

- **`--radius` 落到 `--r-chip`（4 → 5px）**：四档圆角里没有 4px 这一档，5px 是视觉差最小的一档；控件/面板按 §4.3 升到 8/12px 属于各页迁移时的层次调整，不在「删别名」里顺手改。
- **`--term-bg` 落到 `--sunken`（而不是 `--log-bg`）**：旧别名是**分主题**的终端/代码底色，而 `--log-bg` 在两种主题下都是深底（§4.1 的日志查看器口径）。旧页面里这 48 处大多是表单控件、代码块、diff、日志行容器的底，行文字用的是 `--ink` / `--ink-3` / 级别色，换成 `--log-bg` 会在浅色主题下变成深底深字。改成 `--sunken`（§4.1 的「凹陷区（代码、看板列、轨道）」）与迁移前的观感一致；唯一已经是「两种主题都深底」的是验收面板的 `.rp-pre`（P4 就用 `--log-bg` + `--log-ink`），保持。
- 日志查看器「两种主题都是深底」需要把 LogTape 的行色一起换成 `--log-ink` / `--log-dim`（现在用的是主题正文色），属于该页的重做，没在本项顺手改。
- `StatusBadge` 的 need 族文字用 `--need-ink`（浅色主题下万寿菊直接做正文色对比不足），状态点/字形仍是 `--need`。

顺带修掉 `Workbench.vue` 里 `--fg-1` / `--bg-2` / `--r-sm` 三个**从未定义过**的旧变量（`context-toggle` 的按下态因此一直是空声明）：按下态改成「选中 = 松绿」的 `--accent` / `--accent-soft`。

**2）手机断点统一 760**

迁移期的手机断点有 620 / 639 / 640 / 700 / 720 / 767 / 768 / 980 八种，同一个「手机」在不同页面生效宽度不同：

- 620 / 639 / 640 / 700 / 720 / 767 / 768 → **760**（含 `LogTape` / `Workbench` 的 `matchMedia` 与 9 个测试文件里的断言）。
- `DashboardSystem` 的 980 → **900**（与顶栏抽屉同一个中间断点），700 的「单列」与 640 的「两列」两层合并成一个 760 块：手机上头部竖排 + 卡片两列（原先 700–640 之间会先单列再跳回两列）。
- 内容宽度（`.col { max-width: 640px }`、`FocusMode` 的 720px 阅读宽度）不是断点，不动。

统一后剩下的手机断点分布：760 ×117，460 / 480 / 520 / 560 各 1–2 处（都是小屏的二次收敛：Dashboard 的格子、Sessions 的工具条、NewSchedule 的字段、MCP 的表格）。

**确实保留的中间断点**（不属于「手机」，写在这里备查）：

| 值 | 处数 | 用途 |
|---|---|---|
| 900 | 10 | 顶栏抽屉、各页「两栏 → 单栏」的密度切换（与 `utils/mobileLayout.ts` 的 `MOBILE_MAX_WIDTH` 同一档） |
| 1099 | 4 | §4.3 图标栏上界（AppShell、FormSection、SettingsLayout、Today） |
| 1279 | 2 | §4.3 完整三栏的下界（ThreadView、Today） |
| 940 | 2 | diff 双栏（UnifiedDiff）与技能表（Skills）的宽度切换 |
| 1024 | 1 | Dashboard 图表区宽高比 |
| 780 | 1 | PlanDetail 详情头部的动作行换行 |

**3）状态取色收敛到 statusFamily**

`api/client` 的 `STATUS_COLOR` / `statusColor`（17 个状态的独立颜色表）整表删除：统计页「任务状态分布」（`DashboardJobStatus`）、`Signal`、`StatusBadge` 改用 `utils/statusFamily` 的族 + `styles/status.css` 的 `.k-<族>`（`--c` / `--soft`）。色值从此只有一处。

口径变化（设计稿 §5 的族表说了算）：`timeout` 从失败色改回超时族（`--time`）、待应答 / 待验收 / 待批准从强调松绿改成「需要人」的万寿菊、`waiting_dir` / `paused_for_update` 归 queue 灰。

**测试**

- `styles/breakpoints.test.ts`（新）：扫全部 `.vue` / `.css`，断言没有任何 `@media (max-width: 760|900 之外)` 的迁移期手机断点（620/639/640/700/720/767/768/980），并断言 760 至少出现在 20 个文件里（防止用例空转）。
- `views/dashboardView.test.ts`：新增「状态数走 statusFamily、模板里没有 statusColor」。
- `components/updateWaitIdle.test.ts`：`paused_for_update` 的颜色断言从 `api/client` 的色表改成 `utils/statusFamily` 里的族。
- 既有手机断点断言（approvalPanel / infoCard / interactionToast / focusMode / jobDetailMobile / planDetailMobile / projectsFleet / sessionsCards / workbenchMobileLighter / steward / dashboardMobile 等）随断点改成 760；`dashboardMobile.test.ts` 另加一条钉住 DashboardSystem 的中间断点是 900。

**没做**

- `utils/mobileLayout.ts` 的 `MOBILE_MAX_WIDTH = 900` 保持 900：它管的是 JS 侧「提示条自动收起 / 会话头部折叠」，与顶栏抽屉同一档，改成 760 会连带改 761–900 的交互（属于该模块而不是本项的取色与断点清理）。
- `docs/design/2026-10-21-ui-restyle-preview.html`、`docs/design/web-v3-preview.html` 与旧 plan 里还写着旧变量名：那是历史快照，不回填。
- 四态截图（zh/en × 浅/深）仍未在本机跑（无可用浏览器/截图工具），`web/scripts/shots.mjs` 的自动检查留给发版前。
- 日志查看器（LogTape）与终端（AttachTerminal）按 §4.1 应该是「两种主题都深底」，但它们内部用的是主题正文色，本轮只把底色换成 `--sunken` 保持迁移前观感；改成深底 + `--log-ink` / `--log-dim` 属于这两块的样式重做。

### P6-c 图标改用 Lucide（2026-10-23，issue gofer-h355）

用户反馈：图标不好看、有些很小看不清。现状是把 Unicode 字形当图标（`utils/shell.ts` 的 `glyph`：◉ ▤ ⧉ ⌁ ⬡ ◈ ⌂ ⚙ …，页面里还有 ✉ ✓ ⚑ ⇉ ◷ ↻ ✕ ▸ 等），打包字体（Schibsted Grotesk / IBM Plex Mono 的拉丁子集）并不含这些符号，回退到系统字体后大小、粗细、基线各机器不一致。

**依赖**

用 `@lucide/vue` **1.53.0**（`--save-exact`，ISC 许可，图标本体来自 lucide）。issue 里写的是 `lucide-vue-next`，但它已被官方整包弃用（`deprecated: Package deprecated. Please use @lucide/vue instead.`），所以改用后继包；API 相同（`import { Search } from '@lucide/vue'`，每个图标一个函数组件）。按需 import + `sideEffects: false`，打包只含用到的图标，无外网依赖。选 1.53.0 是因为该包按日自动发版，取两周前（2026-10-08）的版本，符合「发布超过两周的稳定版」。

**基元**

- `styles/tokens.css`：新增 `--icon-sm: 14px` / `--icon: 16px` / `--icon-lg: 20px`（行内与表格 / 导航与按钮 / 标签栏与宫格）。`ui/icon.ts` 的 `ICON_PX` 是同一组数值（lucide 要像素数），`ui/icon.test.ts` 读 `tokens.css` 校验两者一致，防漂移。
- `ui/Icon.vue`（新）：唯一入口。线宽统一 **1.75**（lucide 默认 2 在 14–16px 上偏重）；颜色跟随 `currentColor`；给了 `label` 才是 `role="img"` + `aria-label`（可加 `title`），不给就是装饰性（lucide 自动 `aria-hidden="true"`）；`spin` 用于运行中。
- `styles/icons.css`（新）：`.icon` 的基线对齐与 `.icon--spin`（唯一的图标动画，`prefers-reduced-motion` 由 `base.css` 统一关闭）。
- `ui/icon.ts`（新）：`NavIcon` 语义名 → lucide 组件的表（换图标只改这里）。

**导航与顶栏**

- `utils/shell.ts`：`ShellNavItem.glyph: string` 改为 `icon: NavIcon`（type-only import，纯逻辑模块不引入组件运行时）；16 个页面 + 手机标签栏 + 「更多」宫格同一个表。
- 图标选择（§6.1 分组）：今天 Sun / 工作台 LayoutGrid / 工作项 ListChecks / 计划 Milestone / 任务 ClipboardList / 会话 MessagesSquare / 信箱 Mail / 待验收 ClipboardCheck / Issues CircleDot / 统计 ChartColumn / 工作流 Workflow / 定时 CalendarClock / 运行节点 Server / Agents Bot / 项目 FolderKanban / 设置 Settings / 更多 Ellipsis。
- `ui/AppShell.vue`：侧栏、图标栏、手机标签栏、更多宫格改 `<Icon>`；侧栏收起/展开 `⟨ ⟩` → PanelLeftClose / PanelLeftOpen；搜索 `⌕` → Search；新建 `+` → Plus；新版本 `↯` → Sparkles。
- 顶栏按钮：MailButton `✉` → Mail、QuickCapture `✎` → SquarePen、ThemeToggle `☀/☾` → Sun/Moon、NeedsMeButton 手机上的 `::before content:'◔'` → 真正的 Bell 图标（桌面藏图标、手机藏文字）。

**状态字形（§5）**

`ui/statusGlyph.ts` 的 shape 从自绘 SVG 换成 lucide：run `loader-circle`（常驻旋转）/ need `circle-alert` / done `circle-check` / fail `circle-x` / time `clock` / idle `circle` / queue `circle-dashed`。`GlyphKind` 改成 lucide 图标名，`data-kind` 与 `StatusGlyph.vue` 的渲染一一对应；族的颜色（`.k-<族>` 的 `--c`）与 `StatusPill` 的文字口径不变，小的脉冲环动画换成 loader 旋转（`icon--spin`），`status.css` 里的 `glyph-pulse` 一并删除。

**页面里的字符按钮**

- 关闭：`Drawer` / `Sheet` 的 `✕`、各弹窗的 `×`（EnrollAccept / EnrollReject / GrantRenew / SessionGrant / ClusterTopology / AttachmentBar / QuickCapture 提示 / Mail 的两个 chip / Workbench 的两处）→ X。
- 展开 / 收起：Skills、Agents、WorkbenchSidebar、ConversationView、UnifiedDiff、SessionDrawer（两处）、ProjectFilter、JobDetail 的会话任务链、AttachTerminal 的输入框开关 → ChevronRight / ChevronDown（ProjectFilter 向上为 ChevronUp）。
- `views/settings/SettingsLayout.vue` 的面包屑分隔符 `content: '▸ '` 改成普通标点 `/`（CSS 伪元素放不了图标组件，而分隔符本身不需要图标语义）。
- 其他：`Schedules` 有参数时「运行」按钮的齿轮 `⚙` → Cog；工作台会话头部的停止 `■` → Square、「更多」`⋯` → Ellipsis。

**事件时间线**

`utils/eventMeta.ts` 的 `EVENT_META.icon` 从字形字符串改成 lucide 组件（`eventIcon()` 返回 `LucideIcon`，未登记的 type 落到空心圆）：已提交 Check / 标题修改与评论 Pencil / 派发 ArrowRight / 运行与验证 Play / 回合结束 Square / 等输入 Keyboard / 各类警告 TriangleAlert / 取消与拒绝 X / 等待批准 Pause / 超时 Timer / 文件收集 PackageCheck / 转交与备用 CornerUpRight / 重试 RotateCw / 等目录锁 Hourglass / 已交付 ArrowUpToLine / 未交付圆点 Circle / ACP 与 MCP Cog / 决策 hook Scale / 捕获会话 KeyRound / 通知丢弃 BellOff / leader 回合（CircleDot / SkipForward / Ban / Flag）/ 交互 MessageSquare / 技能 Boxes / 规则 ScrollText。JobDetail 两处与 PlanDetail 一处的时间线渲染改成 `<Icon class="ev-icon">`，`.ev-icon` 的宽度与取色不变。

**没动（有意留下）**

- 表格里的 `●`（Tabs 活跃点、各页状态点）、文案里的 `✓` / `×`（乘号、删除文案）、`utils/statusText.ts` 的 `⏳` / `⏸` 前缀：这些是文字或状态点，不是图标按钮，本次不动。
- `↻` 在 Board / JobDetail / PlanDetail 的 `agent ↻` 是「经 exec 恢复」的文字后缀（拼在模板字符串里），不是按钮。

**测试**

- `ui/icon.test.ts`（新）：尺寸档位与 `tokens.css` 一致；16+ 个导航项都有可解析的图标且 zh/en 都有可访问名；`Icon` 的 SSR 行为（装饰性 `aria-hidden`、有 `label` 才 `role="img"`、`size="lg"` → width 20、线宽 1.75、`spin`）。
- `ui/iconScan.test.ts`（新）：扫 `web/src` 下全部 `.vue` / `.ts`（排除测试、`i18n/` 字典、注释），不允许再出现 24 个「已彻底清掉」的图标字形（◉ ▤ ▥ ◇ ⧉ ⌁ ✉ ▦ ⇉ ◷ ⬡ ■ ⋯ ⚙ ⌂ ⟨ ⟩ ☀ ☾ ◔ ↯ ▸ ▾ ▴ 表里逐字符写了理由）；白名单 `WHITELIST` 必须写理由。不在清单里的 `●` / `×` / `✓` / `§` / `⏸` / `↻` 等是因为它们仍作为文字或状态点合法出现（详见上面「没动」）。
- `ui/statusGlyph.test.ts`：新增「每个族渲染对应 lucide 图标（`lucide-<kind>` + 线宽 1.75）」与「只有 run 旋转」。
- `views/work.test.ts`、`views/mailWiring.test.ts`：导航项断言从 `glyph: '▥'` / `glyph: '✉'` 改成 `icon: 'work'` / `icon: 'mail'`。

**验证**

`npx vue-tsc --noEmit`、`npx vitest run`（223 个文件 / 1519 个用例）、`npx vite build` 全绿。

截图：本机没有 Playwright 自带的浏览器，但可以用系统 Edge（`playwright` 的 `channel="msedge"`）：`vite preview` 起构建产物（没有后端时页面数据报错，但外壳、导航、顶栏、状态字形都是真实渲染），按 zh/en × 浅/深 × 1440/390 八个组合截了 `AppShell`。核对结果：侧栏 16 项、图标栏、手机 5 个标签、更多宫格、顶栏各按钮、状态字形尺寸与粗细一致，基线对齐；细节上把侧栏收起按钮改成居中（图标比原来的 `⟨` 字符小一圈，原样式把它靠上对齐）。

## 附：0.1 版待确认问题（已于 0.2 决定，见 §16）

1. **品牌色和字体是否跟着换？**
   - 建议：界面换成新色板和 Schibsted Grotesk，logo 和 `docs/brand` 不动。旧色只在品牌物料里使用。
2. **默认主题是什么？**
   - 现状跟随系统，默认深色。
   - 建议：继续跟随系统，用户手动选过就记住。新色板的浅色和深色是同等完成度的。
3. **是否接受 `/board → /jobs` 改名，以及手机标签栏用「计划」换掉「工作项」？**
   - 建议：接受。旧链接重定向，工作项放在「更多」的第一位。
4. **铃铛是否合并？**
   - 建议：worker 申请并入「待我决策」，作为一种决策卡；信箱的待指派保持独立图标，因为它不是「拍板」类事务。
5. **JobDetail 改成页签后默认显示哪个页签？**
   - 建议：按状态决定。needs_review 默认显示「验收」，failed 默认显示「日志」，其余默认显示「概览」。用户切换过的页签会记在 URL 里。

### P6-b 残留文案抽取（2026-10-23，issue gofer-hez1）

目标（§11 问题 19、§14 P6 行）：工具层与组件里残留的硬编码中文全部进 i18n，并加一道全站扫描守着。

**本轮已抽出**（工具层与跨页公共组件，文案集中在新增的 `i18n/pages/shared.ts`、域 `util.*`）：

| 文件 | 抽出的文案 |
|---|---|
| `utils/statusText.ts` | 状态词改走主字典的 `status.<值>`（`status.in_progress` / `status.closed` / `status.draft` 补在 `shared.ts`）；`⚠ / ⏳ / ⏸` 符号前缀留在代码里（符号是状态信号、不是文案）；筛选选项的「全部」走 `act.all` |
| `utils/work.ts` | 状态 hint、工作区标签、没有会话时的提示、提醒时间预设、到期文案、发言者标签（我 / 会话 / 整理器 / 管家 / 系统）、整理字段名、请求账本的种类与状态及整行拼接 |
| `utils/mail.ts` | 地址与收件人校验错误、选器标签与提示、信件状态、收件人标签、（无标题 / 无项目）、发送预览文案、箱筛选标签 |
| `utils/mailMinimap.ts` | 未知发件方 |
| `utils/todayLanes.ts` | 健康度、头部摘要、agent 列的等输入、aria 标签的分隔符 |
| `utils/sessionState.ts` | agent 会话状态与 job 状态词（其余复用 `status.*`） |
| `utils/delivery.ts` | 交付状态 / 方式、note / error 包裹、PR 链接文字 |
| `utils/workflowResult.ts` | 步骤 / 重试 / 扇出的位置文案、模板编辑门槛与内置只读说明、覆盖说明、保存 / 删除结果提示 |
| `utils/dashStats.ts` | 区间名、星期名、周 / 月分秒标签、柱图悬停提示、标题行摘要，以及「复制统计」的纯文本（按当前语言出）、`stats.note.*` 口径说明 |
| `utils/runnerDisplay.ts` | `（本机）` 与下拉项的 `· 本机` |
| `components/ExperimentalBadge.vue` | 默认 label / hint 改用 computed + `t()`（不传 prop 时跟随语言） |
| `components/CompareColumns.vue` | 整个对比视图的模板文案（提示、徽标、按钮、表头、空态、分页 aria） |
| `utils/enrollments.ts` | 配对码校验与倒计时、worker id / 标签校验、接受与拒绝的错误文案、关闭态提示、申请状态词 |
| `utils/dispatch.ts` | 分派校验错误、六个动作的名字与说明、触达结果、409 文案、优先级名、待同步标记 |
| `utils/messenger.ts` | 传话人状态 / 不可用原因、空闲退出倒计时、轮换原因与上限、token 用量、传话受阻原因 |
| `utils/sessionTopic.ts` | 主题来源标签与悬停说明、编辑门槛、范围校验、命中原因、查找结果行 |
| `utils/eventMeta.ts` | 63 个事件类型的标签（移到 `util.event.<type>`）+ `detail_json` 摘要的拼接片段 |
| `utils/permissions.ts` | 授权对象标签、预览问题文案、表单门槛、只读原因、授权状态与提示、审计动作名、403 缺权限文案、有效期选项 |

写法：模块级常量持有的是 getter（`get label() { return statusText('active') }`），模块加载时不定住语言；纯函数内部直接调 `t()`；`t()` 读的是 `store/locale.ts` 的单例 ref，模板 / computed 里调用仍然响应式。zh 的值与抽取前的字面量逐字一致，所以「只换文案来源」可验证：现有按中文断言的用例逐字保留。

**扫描测试** `i18n/noHardcodedCjk.test.ts`：用 `import.meta.glob(?raw)` 读 `web/src` 下的 .ts / .vue（除 `i18n/` 与 `*.test.ts`），.ts 与 SFC 的 `<script>` 交给 TypeScript 解析、只看字符串 / 模板字面量（注释是 trivia，天然不算；SFC 的 `<template>` 按原文扫、只去 HTML 注释与 `<style>`）。`WHITELIST` 收明确需要的字面量（目前只有语言开关的「中文」），逐条写理由；还没抽完的文件列在 `PENDING`，第二个用例断言「表里的文件确实还有硬编码中文」，所以抽干净后不删表项会红——这份名单只减不增。

本批从 141 个文件 / 1884 处降到 **122 个文件 / 1575 处**。

**未完成**：`PENDING` 里剩下的 122 个文件（约 1575 处），集中在 `views/*.vue`（`Config` / `JobDetail` / `NewJob` / `PlanDetail` / `Mail` …）、`components/*.vue`（`SessionDrawer` / `MessengerDrawer` / `WorkDispatchDialog` / `MergeDialog` …）、`api/client.ts` 与 `api/attachments.ts` 的错误文案，以及 `utils/` 里剩下的 50 个小模块（`today` / `approval` / `planControls` / `extensionSettings` …）。`npx node tmp/gen-pending.mjs`（仓库根）可以重算名单；抽完删表项即可。

**测试与验证**：`web` 三命令全绿——`npx vue-tsc --noEmit`（退出码 0）、`npx vitest run`（221 文件 / 1502 例，导出末尾：`Tests 1502 passed`）、`npx vite build --outDir ../tmp/web-dist-check`（`✓ built in`）。现有用例随之改了三处断言（`dashStats.test.ts` 用 `rangeName()`；`uiTextWiring.test.ts` 按 getter 形态；`updateWaitIdle.test.ts` 按符号映射）。

### P6-b4 残留文案抽取（第三批，2026-10-23，issue gofer-hez1）

本批清单里的 **41 个文件全部抽干并移出 `PENDING`**，分两次提交（工具层与小组件 25 个；页面与弹层 16 个）。`PENDING` 里只删了本批抽完的条目，别批的行未动。

| 文件 | 抽出的文案 |
|---|---|
| `api/client.ts` | 请求失败（HTTP 状态）、401 token 无效（8 处同一句收成一个 key） |
| `api/time.ts` | 待触发、刚刚、`{n}s/m/h/d 前` |
| `utils/sessionOwner.ts` | 会话归属被拒的提示与悬停说明、转给 X |
| `utils/runners.ts` | 远程升级的四种挡下原因、平台不一致（含 CLI 提示）、升级记录四态与耗时 |
| `utils/resumeChoice.ts` | 续接形态的标签、切换说明、各形态缺失原因 |
| `utils/fromSession.ts` | 从会话新开的切换说明、缺 `from_session_args` 的三条原因 |
| `utils/jobOutcome.ts` | 预算行（上限 / 已用 / 估算） |
| `utils/sessionMessaging.ts` | 对端状态三词、outbox 五种状态、署名与回信抬头 |
| `utils/pendingAlert.ts` | 待办提示条的标题与正文（人工介入 / 待批准 / 权限 / 等回复 / 决策） |
| `utils/grantRenew.ts` | 剩余时间四档、不可续期原因（复用权限状态词）、续期失败文案 |
| `utils/ceiling.ts` | 上限说明与摘要七行；两个导出常量改函数（见下） |
| `utils/extensionSettings.ts` | 组名、五种类型名、取值（空 / 开 / 关）、取值提示、五条校验错误、提示词空值说明、导入动作与摘要 |
| `utils/today.ts` | 分档词（复用已有 `today.tier.*`）、卡住 N、等待 / 剩余时长四档、已办提示、memory 提议五行；两个模块级 Record 改 getter |
| `utils/mailConversations.ts` | 回信标号（复用 `mail.persona.*`）、最后活动；`PERSONA_STATE_LABEL` 改 getter |
| `utils/agentExtends.ts`、`sessionResume.ts`、`issuePaging.ts`、`forwarderStop.ts`、`trackerSync.ts`、`logPlaceholder.ts` | 继承来源徽标、唤醒 / 接管与确认文案、批量结果摘要、停止不支持说明、同步仍在进行、日志空态 |
| `components/ThemeToggle.vue`、`InteractionToast.vue`、`today/UndoToast.vue`、`workbench/acpEvents.ts`、`workbench/threadAutoFocus.ts` | 主题切换的 aria、提示条关闭的 aria、撤销 toast、ACP 提示与空轮说明、通知定位两句 |

**一处接口变化**：`utils/ceiling.ts` 的 `CEILING_LABEL` / `CEILING_READ_ONLY_NOTICE` 由导出常量改成 `ceilingLabel()` / `ceilingReadOnlyNotice()`——模块级常量在加载时就把语言定死，切成英文后这几处不会跟着变。连带改了 9 个调用方（`NewJob`、`AppShell`、`NewSessionSheet`、`WorkbenchComposer`、`DecisionCard`、`DecisionQueue`、`Today`、`Permissions`、`pageGate` / `todayGate`）与 4 个按源码文本断言的 wiring 测试，全部只改取词方式；同理 `PROMPT_EMPTY_HINT` 也改成函数（`settings/Extensions.vue` 三处调用）。其余模块级 Record / 常量（`TIER_LABEL`、`MEMORY_ACTION_LABEL`、`PERSONA_STATE_LABEL`、`GROUP_LABELS`）按 P6-b 的既有写法改成 getter。

**测试**：`npx vue-tsc --noEmit` 退出码 0；`npx vitest run` **222 文件 / 1505 例全绿**（扫描测试两个用例都过，`PENDING` 里已无本批文件）；`npx vite build --outDir ../tmp/web-dist-check` 通过（`built in 4.40s`）。随迁移改了四处按源码文本断言的用例：`interactionToastMobile.test.ts`（改断言 toast 关闭的 key）、`extensionSettings.test.ts`（`PROMPT_EMPTY_HINT()`）、`steward.test.ts`（改断言 `today.notes.*`）、`updateWaitIdle.test.ts`（改断言 `util.update.runningJobs`）。
### P6-b2 残留文案抽取第 2 批（2026-10-23，issue gofer-hez1）

P6-b 的第二批：本批认领 40 个还挂在 `PENDING` 里的文件（工具层 + 组件 + 配置页），**全部抽干净并从 `PENDING` 删除**（`PENDING` 从 129 项降到 88 项），
只换文案来源，不改样式、布局与行为；`zh` 的值与抽取前的字面量逐字一致，
所以现有按中文断言的用例大多**原样保留**（少数改为按 key 或 zh 断言）。

**抽到的文件**（→ 所属页面字典）：

- `utils/`：`approval` → `pages/shared.ts`；`planControls` → `pages/plans.ts`；`sessionNudge` / `sessionUsage` /
  `planSupervisor` / `webPush` / `sessionGrant` / `sessionPermission` / `attachments` / `workflowTemplate` /
  `runnerChoice` / `reviewGate` / `jobTimeout` / `issueTree` / `configLeader` / `lockPaths` / `attachmentText` →
  `pages/shared.ts`（`util.*`；`workflowTemplate` → `pages/workflows.ts`）
- `api/sse.ts` → `pages/shared.ts`
- `components/`：`SessionTopic` / `SessionGrantDialog` / `SessionPermissionPrompt` → `pages/sessions.ts`；
  `StatusBadge` / `AttachmentThumbs` / `QuickCapture` / `NdjsonTimeline` → `pages/shared.ts`（`util.*` /
  `common.*`）；`ClusterTopology` / `MessengerDrawer` → `pages/runners.ts`；`ApprovalPanel` → `pages/jobs.ts`；
  `ReviewPanel` / `MemoryCandidatesTab` → `pages/review.ts`；`today/FocusMode` / `today/HandledDrawer` →
  `pages/today.ts`；`workbench/LayoutPane` / `workbench/WorkbenchCommandPalette` / `workbench/ThreadChangesView` /
  `workbench/layoutTree.ts` → `pages/workbench.ts`
- `views/`：`Sessions`（列表里的中继说明与卡片标题）→ `pages/sessions.ts`；`Runners`（只有一处命令占位，改成
  `<server-address>`）→ `pages/runners.ts`；`Mail`（预览占位正文）→ `pages/mail.ts`

**保留的字面量**（`WHITELIST`）：`utils/attachmentText.ts` 里识别 worker 写进正文的 `[附图 n] <路径>` 片段——
那是协议文本，不是界面文案，改了就不认老消息。

**文案常量的写法**（本批统一了三类，原因都写在代码注释里）：

- 引用处**全在模板**的导出常量 → `computed`（`DIR_LOCK_HINT`、`HANDOFF_QUICK_PHRASE`）。`<script setup>` 的
  导入绑定在模板里会自动 `unref`，所以名字与模板写法都不用变，语言切换即时生效。
- 被**按下标取字段**、或需要保持数组 / 对象身份的导出常量 → 成员用 getter
  （`SESSION_GRANT_EXPIRY_OPTIONS[].label`、`PLAN_CONTROL_HELP.*.label/help`）：字段仍是字符串，
  `.sec` 取值、`RENEW_EXPIRY_OPTIONS` 的同一性断言、模板 `{{ o.label }}` 都不受影响。
- 不可用原因的导出常量（`UNSUPPORTED_*_REASON`）→ 导出函数（`unsupportedServerReason()` 等），调用点只有
  `SessionDrawer.vue` 两处。

**行为上唯一的变化**：`views/Sessions.vue` 的 `peer_messaging` 徽标原先用「文案 == '待上报'」判 pending，
现在改用同一谓词的函数 `peerMessagingPending()`（`!peer_name && !peer_status && !peer_messaging`）——
文案已随语言变，不能再拿来比较；`ApprovalPanel` 的「已过期」也改成按 `expires_at <= nowSec` 判断。

**验证**：`web` 三命令全绿——`npx vue-tsc --noEmit`（退出码 0）、`npx vitest run`（222 文件 / 1505 例，
`Tests 1505 passed`）、`npx vite build --outDir ../tmp/web-dist-check`（`✓ built in 3.80s`）。扫描测试两例通过，
`PENDING` 从 129 项降到 88 项。

**未完成**：无（本批 40 个文件全部完成，包括 `views/Config.vue`）。
（`npx vite build`：`✓ built in 4.46s`。）
### P6-b3 残留文案抽取第 3 批（2026-10-23，issue gofer-hez1）

接续 §14 P6 行与上面的「P6-b 残留文案抽取」。三批并行（P6-b1 / b2 / b3），本分支只动任务书列出的 41 个文件，`PENDING` 里也只删这 41 条。

| 文件 | 归属字典 | 抽出的文案 |
|---|---|---|
| `components/SessionDrawer.vue` | `sessions.ts`（`sessions.drawer.*`） | 状态表改 getter 复用 `util.session.state.*`；中继三态与投递模式的标题/说明、`relaySummary` 与 demote 提示、输入框占位与 hint（含送不进终端的两种原因）、唤醒 / 接管 / 解除接管的回执与 9 条失败原因、turn 时间线（等待 / 已读 / 展开全文 / 无消息）、元数据区、移出登记与授权 / 续期按钮、历史消息与「最后一条消息」区 |
| `components/SessionNudges.vue` | `sessions.ts`（`sessions.nudge.*`） | 催办列表、三种校验错误、表单与说明 |
| `components/PlanSupervisor.vue` | `plans.ts`（`plans.supervisor.*`） | 主 agent 会话：绑定/修改/清除、失联提示与改绑、选择器、发送区 |
| `views/NewJob.vue` | `newjob.ts` | 空候选的 7 种原因、worker 收窄的 4 种说法、16 条校验错误、暂存失败、收集箱条目提示与提交回执 |
| `components/WorkDispatchDialog.vue` | `work.ts`（`work.dispatch.*`） | 分派动作的提交标签与完成回执、六个 panel 的字段与说明；每路候选的循环变量改名 `target`（原来复用 `t` 会遮住 `t()`） |
| `components/MergeDialog.vue` | `shared.ts`（`util.merge.*`） | 标题、确认按钮、摘要拼装、两种合并方式、清理选项、冲突文件、结果与警告 |
| `components/IssueBatchBar.vue` | `issues.ts`（`issues.batch.*`） | 计数、三个动作与确认框、关闭原因、执行说明 |
| `components/EnrollAcceptDialog.vue` / `EnrollRejectDialog.vue` | `runners.ts`（`runners.enroll.*`） | 配对码说明、字段标签、屏蔽来源、接受/拒绝按钮 |
| `components/RunnerDirs.vue` | `runners.ts`（`runners.dirs.*`） | 四项分组标题与各种「不存在 / 未上报」说明 |
| `components/AttachmentBar.vue` | `shared.ts`（`util.attachBar.*`） | 上传状态、类型/体积/数量错误、按钮 aria 与提示 |
| `components/CommentThread.vue` | `shared.ts`（`util.comment.*`） | 派发标记、空态、占位、只读说明 |
| `components/StewardPanel.vue` | `shared.ts`（`util.steward.panel.*`） | 面板外壳、未启用、重建提示、输入框与发送 |
| `components/MemoryList.vue` | `issues.ts`（`issues.memory.*`） | 无标签、保存 / 删除、空态 |
| `components/UncommittedBadge.vue` / `InfoCard.vue` | `shared.ts`（`util.badge.*` / `util.action.*`） | 未提交计数、截断说明、详情/收起 |
| `views/NewWorkflow.vue` | `workflows.ts`（`workflows.newTemplate.*`） | 新模板正文默认值与两处标签；内置示例 spec 的注释正文进 WHITELIST（用户会复制自己改的内容） |
| `views/Projects.vue` / `views/Skills.vue` | `projects.ts` / `shared.ts` | git 状态卡 aria、删除失败的模板 |
| `components/today/SnoozedDrawer.vue` / `DecisionOverlay.vue` / `TodayLaneRow.vue` / `TodayStatusBar.vue` | `today.ts` | 稍后抽屉、待我决策浮层、并行行「开始于」；状态条里 `startsWith('管家')` 是服务端 wire 告警前缀，进 WHITELIST |
| `utils/compare.ts` / `trackerView.ts` / `steward.ts` / `capture.ts` / `todaySnooze.ts` / `injectMode.ts` / `todoContinue.ts` / `planProgress.ts` / `wrapUpDisplay.ts` / `pageGate.ts` / `permissionGate.ts` / `mcpView.ts` / `mobileLayout.ts` | 各页字典 + `shared.ts` | 合并错误与结果、记忆类型 / 年龄 / doctor 标记、管家状态与快捷问题、收集箱、稍后菜单与标记、投递模式、待办接续、进度摘要、收尾原因、缺权限文案、权限动作名、MCP 绑定位置、手机头部占位 |
| `utils/uiText.ts` | `shared.ts`（`ui.nav.*` / `ui.page.*` / `ui.col.*` / `ui.back.*`） | 导航、页面标题、表头、返回链接改成 getter（`PAGE.board` 在模板里读仍是响应式） |
| `api/attachments.ts` | `shared.ts`（`util.attach.*`） | 上传 / 取图的错误文案 |
| `main.ts` | `shared.ts`（`util.app.*`） | 启动期三条控制台告警 |
| `utils/findings.ts` | WHITELIST | 汇报里固定小节标题的解析关键字（与 Go 侧对齐），不是界面文案 |
| `utils/steward.ts` | WHITELIST | 服务端管家 prompt 原文（prime 头、巡检标题），用来识别首轮与系统消息 |
| `ui/LangToggle.vue` | `shared.ts` + WHITELIST | 悬停说明走 `util.lang.*`；按钮上的「中」按语言写自身名，进白名单 |

写法同前两批：模块级常量 / 选项数组改成 getter（`get accept() { return t('runners.enroll.accept') }`），纯函数内部直接调 `t()`；`utils/mobileLayout.ts` 的占位从常量改成 `mobileHeaderFallback()` 函数，否则语言切换后定住。

**与 P6-c 的关系**：本批只碰文案；不做图标替换（`📎` / `×` / `↗` 等保持原样），避免与并行的图标迁移冲突。

**不丢信息核对**（§7 本页字段清单）：逐文件对照迁移前页面，文案之外的结构、`data-test` 钩子、按钮数量与位置、可选字段的显示条件都未变；`STATE_LABELS` 与 `CAPTURE_KINDS` 这类表只换取值来源。两处循环变量改名（`WorkDispatchDialog` 的 `t` → `target`）是模板局部作用域，渲染结果不变。

**测试与验证**：`npx vue-tsc --noEmit` 退出码 0；`npx vitest run`（222 文件 / 1505 例，导出末尾 `Tests 1505 passed`）；`npx vite build --outDir ../tmp/web-dist-check --emptyOutDir`（`✓ built in 3.81s`）。现有用例里 9 处按中文断言改为按 key 断言（`steward.test.ts`、`messengerSessionWake.test.ts`、`drawerAttachPolish.test.ts`、`sessionDrawerRelay.test.ts`、`s8SessionMessage.test.ts`、`views/sessionsRefusedOwner.test.ts`、`workbench/threadViewSessionComposer.test.ts`、`mobileLayout.test.ts`、`utils/steward.test.ts`）。

本批后 `PENDING` 从 122 个文件降到 **81 个**。

## 实施记录 P6-2（截图检查第二组，gofer-hez1）

输入是全页面截图检查的产物（`tmp/pagecheck/out-0191/`：zh/en × 浅/深 × 1440/390 + `report.json`）。本组修六项：

1. **信箱「还有更早的信件未加载」不再压正文**：原来它是 `.minimap-more`，`position: absolute` 贴在小地图旁，压在信件正文上。现在这一行（`data-test="mail-older"`）同时放提示与「加载更早的 N 条」按钮，居中占整行；`.minimap-more` 与它在手机断点下的隐藏规则一起删掉。测试：`mailPage.test.ts` / `mailConversationsWiring.test.ts` 改为断言 `mail-older-hint` 在同一行、且页面里不再出现 `minimap-more`。
2. **信箱筛选 chip 的计数**：根因是 `GET /v1/mail/conversations` 的 `count` 是**这一页的条数**（`len(conversations)`），而计数只想要个数字，前端带的是 `limit=1`——于是「全部 / 收件 / 发件」永远显示 1，与页头按本地行数算的「N 个对话」对不上。改为按接口返回的对话数数（`limit` 提到服务端上限 200），并新增 `utils/mailConversations.ts` 的 `mailCountConversations(convs, project)`：项目筛选下与页头同一个口径。测试：`mailConversations.test.ts` 新增两条（按对话数而非信件数、项目收窄），`mailPage.test.ts` 钉住不再用 `limit: 1` 取 count。
3. **任务详情「最近日志」的 ANSI**：概览的末 6 行是纯文本 `pre`（不走 `LogTape` 的 ANSI 渲染），把 `[2m`、`[36m` 当可见字符。新增 `utils/logRender.ts` 的 `stripAnsi()`（CSI / OSC / 三字符转义 / 其余 ESC + 残余 C0 控制字符；`\r` 拆行、`\t` 与 `\n` 保留），`JobDetail.vue` 的 `logTail` 先过它。测试：`logRender.test.ts` 四例 + `views/jobDetailLogTail.test.ts`（接线 + 真实彩色日志样例）。
4. **工作项卡片**：`InfoCard` 新增向后兼容的 `stacked` 属性（默认 false，行为不变），`WorkCard` 打开它——header 改列方向，标题占整行、状态 / 到期 / 未整理 / 离线 chip 在下一行；`Work.vue` 的 `.work-page` 加 `padding-bottom: 64px`，右下角浮动的「问管家」（fixed，约 40px + 16px 底距）不再压住最后一张卡的「详情」。测试：`workCard.test.ts`（header 带 `icard-head--stacked`、标题在徽标之前）、`workPage.test.ts`（底部留白）。
5. **今天页建议卡片的动作名**：动作名来自服务端（`internal/today` 的 `Action.Label` 是固定中文），英文界面出现「Suggested：忽略」「采纳 / 忽略」。新增 `utils/today.ts` 的 `actionLabel()`：按 `id:label` 配对映射到本页 i18n key（`today.action.*`，`open` 的四种文案各对一条，`answer` 的选项文案与未知动作原样显示），`DecisionCard` 的「按建议」「其它动作」「回复占位 / 确认」都走它；`handledActionLabel()` 让「已处理」抽屉按 `action_id` 重新取（那里记的是点击时语言的文案）。服务端中文匹配键进 `noHardcodedCjk` 白名单并写明理由。测试：`utils/today.test.ts` 四例（zh 映射、en 本地化、未知原样、已处理重取）。
6. **未定义 CSS 变量**：`--muted`→`--ink-3`、`--warn`→`--need`/`--need-ink`、`--hover`→`--surface-2`、`--dim`→`--ink-3`、`--accent-fg`→`--accent-ink`、`--fs-base`→`--fs-md`（信箱正文）/ 新增 `--fs-input-mobile: 16px`（`Composer` 手机输入框，iOS 聚焦不缩放的下限）。`--risk` 确认是 `SessionGrantDialog` / `settings/Permissions.vue` 的局部变量，保留。测试：`styles/cssVariables.test.ts`——扫 `web/src` 的 `var(--x)`，必须在 `src/styles/*.css` 定义或在同一文件里局部定义；另钉住六个旧拼写不再出现。为读到 CSS 正文，新增 `web/vitest.config.ts`（`test.css: true`，默认会把 CSS 模块变成空串）。

**验证**：`npx vue-tsc --noEmit` 退出码 0；`npx vitest run` → `Test Files 226 passed (226)` / `Tests 1538 passed (1538)`；`npx vite build --outDir ../tmp/web-dist-check --emptyOutDir` → `✓ built in 5.13s`。本组只改前端，没有 Go 侧改动。

**共享组件改动**：`ui/InfoCard.vue` 新增可选 `stacked`（默认 false），`ui/Composer.vue` / `ui/NewSessionSheet.vue` 只换 token 名（`--accent-ink` / `--fs-input-mobile`），外观不变；`styles/tokens.css` 新增一个变量，其余向下兼容。
### P6-fix1 截图检查第一批（2026-10-23，issue gofer-hez1）

来源：全页面截图检查（v0.191 构建，zh / en × 浅 / 深 × 1440 / 390）报的第一组 6 个问题。逐项一个提交，各带测试。

**1. 今天页右栏「并行中」的行排版**（`components/today/TodayLaneRow.vue`）

- 现象：右栏 1440 下只有 340px，原来 6 列（点 / 标题+项目 / agent / 进度 / 健康度 / 用时）挤在一行，标题只剩 4–5 个字，红色「停滞」压在进度点阵上。
- 改法：按 `preview.html` 的两行结构——第一行「状态点 + 标题与项目」，第二行「agent · 点阵 · 当前步骤 · 健康度 · 用时」。标题独占整行（实测 290px，约 22 个汉字），超出省略号 + `title` 悬停全文；点阵与健康度都 `flex: 0 0 auto`，不再叠字；第二行允许换行，手机上不会横向滚动。
- 测试：`components/today/todayLanes.test.ts` 的「three lines」用例改成两行结构断言（areas `'d t'` / `'s s'`、点阵与健康度固定宽、标题 `title`、四处 ellipsis）。

**2. 侧栏「节点 N/M 在线」口径**（`ui/AppShell.vue`、`utils/runners.ts`）

- 根因：`GET /v1/runners` 的 `status` 按类型分口径——local / peer-http 是 `up`，worker 是心跳导出的 `connected`；侧栏摘要只判 `up`，于是 4 个已连接 worker + server 本机显示成 1/5。
- 改法：新增 `utils/runners.runnerOnline(status)`（`up` 或 `connected`），侧栏摘要与一键更新弹层（原来自己写了一遍同样判断）共用，与运行节点页同源同口径。
- 测试：新增 `utils/runnersOnline.test.ts`（connected / up 算在线，down / disconnected / unknown 算离线）；`ui/appShell.test.ts` 加一条接线断言（用 `runnerOnline(r.status)`，不再出现 `r.status === 'up'`）。

**3. 漏译的 key + 全站 key 扫描**（`i18n/pages/runners.ts`、`sessions.ts`、`shared.ts`、新增 `i18n/i18nKeyUsage.test.ts`）

- 缺的三个 key：`runners.dirs.title`（运行节点页工作目录折叠区显示原始 key）、`util.session.state.idle`（会话抽屉「空闲」）、`sessions.drawer.handedOffJob`（「已接管 →」后的 job 链接）。zh / en 一并补齐。
- 新增测试：扫 `web/src` 里 `t('字面量')` / `te(...)` / `translate(...)` 的 key（后面紧跟 `,` 或 `)` 才算，`'status.' + x` 这类拼接不算），每个 key 必须在 zh-CN 与 en-US 字典里都存在；第二个用例守住扫描本身（文件数 > 80、key 数 > 600），走 Vite 的 `?raw` 读源码，不给测试引 `@types/node`。

**4. 统计页「任务状态分布」小格**（`components/DashboardJobStatus.vue`）

- 现象：格子里是英文缩写还被裁成 `runni…` / `recov…` / `appro…`。
- 改法：改用全站统一的状态名（`utils/statusText.jobStatusText`，即 `status.<值>`，与 job 列表、筛选、徽标同一个映射）；格子最小宽 62 → 84px 放得下「⚠ 待验收」，`.l` 不再 ellipsis（窄了换行），原始状态值留在 `title` 里。`SHORT` 缩写表删除。
- 测试：新增 `components/dashboardJobStatus.test.ts`（SSR 渲染断言 11 个状态名都是中文文案、可见文本里不再出现原始值、title 形如 `running · 运行中`；样式断言不截断 + `minmax(84px, 1fr)`）。

**5. 运行节点拓扑两个 server**（`components/ClusterTopology.vue`）

- 现象：中心 hub 是「server」，本机 in-process runner 又画成一个「server (本机)」辐射节点，像两个 server。
- 改法：`local` 不再进辐射节点（`orderedRunners` 只收 worker 与 peer），它的信息并进中心节点面板：hub 面板新增「本机 runner（server 进程内）」一节（执行方式、状态、server 项目列表）；hub 面板同时收掉了原来的 `local` 计数行。信息不丢，只是换了位置。
- 测试：新增 `components/clusterTopology.test.ts`（1 个 local + 2 个 worker 时只有一个 hub 节点、辐射节点只有 2 个、不再出现「server (本机)」；peer 照旧画节点；hub 面板保留本机 runner 一节）。

**6. 计划详情「清单进度」卡片**（`views/PlanDetail.vue`）

- 现象：自画进度条 `.cbar` 与 `.ops-frac` 在同一 grid 里少算了一列（`清单进度` 标签占了第一列），条被挤成 56px、「4/6」掉到条下面，与「合计 tokens」错位。
- 改法：换成全站统一的 `ProgressBar`（`size="sm"`、`:legend="false"`、条 `flex: 1 1 160px`），条与右侧 `4/6 67%` 同一行，用量文本跟在后面；`boardModel` 用 `boardPct`（todo 的 done+skipped / total，清单口径不变）折成 done + todo 两段。旧的 `.cbar` / `.seg*` / `.ops-frac` 样式随替换删除。
- 测试：`views/planDetailTabs.test.ts` 新增用例（不再有 `.cbar` / `ops-frac`，操作条里是 `<ProgressBar>` + `:legend="false"` + `size="sm"`，`.ops-bar` 可伸缩）。

**验证**

- `npx vue-tsc --noEmit` 退出码 0；`npx vitest run`：`Test Files 228 passed (228)` / `Tests 1533 passed (1533)`；`npx vite build --outDir ../tmp/web-dist-check --emptyOutDir`：`✓ built in 5.35s`。
- 视觉自查：用构建产物 + 桩 `/v1` 数据（fixture 是合成的，不进仓库）在 Edge 里跑 `/today`、`/runners`、`/dashboard`、`/plans/<id>` × zh / en × 浅 / 深 × 1440 / 390，并程序化量了关键尺寸：右栏标题 290px 且点阵与健康度不重叠、侧栏「节点 4/5 在线」、拓扑里只有一个 `server` 节点名、状态分布 11 个格子的标签无裁切、清单进度条 829px 且 `4/6` 与条同一行（手机 201px 同样同一行）。

## 实施记录 P6-3（截图检查第三组，gofer-hez1）

输入：`tmp/pagecheck/out-0191/` 的第三组用户反馈（信箱对话没有可复制的 id、工作台附件入口重复、工作台置顶按钮太小看不出含义、设置页内容区太窄）。逐项一个提交，各带测试。

**1. 信箱对话的固定 id（`cv-…`）**

- 后端：`internal/httpapi/mail_conversations.go` 新增 `mailConversationID(key)` = `cv-` + `sha256(key)` 前 10 位 hex（key 本身就是两端地址排序拼接、方向无关，所以同一对人永远同一个 id）；`mailConversationView` 带 `id`。新增 `mailConvIDQuery`（格式校验，`conv=bogus` 是 400）与 `Server.mailConversationIDKey`——在**本调用方可见范围**内按 id 反查 key（与列表同一个 `mailListKeep`、同一段有界扫描，封顶 `mailListScanPages` 页），查不到一律 404（与「看不到那封信」同一个回答，不泄露别人的对话是否存在）。
- 接口：`GET /v1/mail?conv=<id>`（与 `peer`/`with` 互斥，同时给是 400）与 `GET /v1/mail/conversations?conv=<id>` 都按 id 取那一个对话；`GET /v1/mail` 被限定到一个对话时回答里多一个 `conversation: {id, key}`（additive，不改变原有 `mail` / `count` / `has_more`）。路由未变，所以 `authz_routes.go` 的声明不变（`go generate ./internal/httpapi/...` 无 diff）。
- CLI：`gofer session mail conv <cv-id>`（走既有 `session mail` 命令组，G033）——先用 `GET /v1/mail/conversations?conv=` 取对话（双方 + 计数），再用 `?conv=` 取全部信件（最早在前）打印。client 侧新增 `MailConversation` 与 `Client.GetMailConversation`，`MailListOpts` 加 `Conv`。
- 前端：`MailConversation` 类型带 `id`（本地分组拿不到服务端 id 时是空串）；右栏头部显示「对话 cv-xxxx」+ 复制按钮（`copyText`，与既有剪贴板 helper 同一口径），每封信的 `thread_id` 有复制入口（悬停 / 聚焦出现，手机常显）；`/mail?conv=<id>` 打开时按 id 向服务端要那一个对话（不在当前列表里也进得去，404 就报错而不是安静地选第一个）。
- 测试：Go `TestMailConversationID`（方向无关、稳定、格式、不同对话不撞）、`TestMailConversationByID`（列表带 id、两个接口都按 id 取、越权 / 不存在 404、有权限后可见、非法 id 与 `conv`+`peer` 400）；CLI `TestSessionMailConvCLI`；前端 `mailPage.test.ts` 新增两条（id / 复制入口接线、`?conv=` 深链）。

**2. 工作台输入框只剩一个附件入口**

- 根因：`ui/Composer.vue` 既渲染 `AttachmentBar`（自带 📎 与一行提示），又在自己 `composer-row` 里有一个 📎；不可用原因（如「…暂不支持读取图片」）同时出现在旧条的 📎 `title` 与下方小字。
- 改法：`AttachmentBar` 新增向后兼容的 `compact` 属性（默认 false，行为不变）——compact 时不再画 📎 与提示行，只在**有已选附件时**画缩略图条（选文件 / 拖放 / 粘贴 / 上传 / 重试 / 删除全在，只是入口不重复）。`Composer` 用 `compact`，把提示统一放到自己的附件按钮 `title` + 输入框下方一行小字（`data-test="composer-attach-hint"`）；提示来源同一处（`utils/attachments.composerAttachmentState`）：能力不可用时直接取 `capability.reason`（此时缩略图条根本不渲染，拿不到子组件的 hint），其余情况（上传中 / 失败）仍取附件条的 `hint()`。📎 字形顺手换成 lucide `Paperclip`（P6-c 的图标口径）。
- 测试：`ui/composer.test.ts` 新增「只留一个附件入口 + 原因走 title 与小字」（不再有 `attach-button` / `attach-hint`，有 `composer-attach-hint` 与 `title="…"`）。

**3. 工作台置顶按钮**

- 原来是 `.icon-btn` 里一个纯文本 ⌖ 小字形，32×32、无文字、只能靠悬停猜。改为 `btn sm pin-btn`：lucide `Pin` 图标（`size="sm"`，与其它图标同一尺寸口径）+ 桌面显示「置顶 / 取消置顶」文字；≤760px 只收掉 `.pin-text`（点击目标仍 32×32），`title` / `aria-label` / `aria-pressed` 全程都有。已置顶时 `.pin-btn.on` 用 `--accent` 边框 + `--accent-soft` 底，不用悬停也能看出来。手机「更多」菜单里的置顶入口保留。
- 同行的其它纯图标按钮复查：返回 `☰`（仅手机，`aria-label` 有）、停止 `Square`（有 `title` / `aria-label`，`--fail` 颜色）、更多 `Ellipsis`（仅手机，有 `aria-label`），尺寸与含义都够；「打开 job 详情」本来就有文字。故只改置顶这一处。
- 测试：新增 `components/workbench/threadViewPin.test.ts`（图标 + 文字、手机断点、选中样式、手机菜单入口仍在）。

**4. 设置页内容区宽度**

- 根因：`views/settings/SettingsLayout.vue` 的 `.settings-body` 是 `210px minmax(0, 720px)`，内容列被钉死在 720px。
- 改法：内容列 `minmax(0, 1fr)`，整页 `.settings` 加 `max-width: 1480px`（与 `/work` 同口径）；卡片 / 表格 / 说明文字用满内容区，表格在窄屏仍走既有的 `.table-wrap` 横向滚动；表单字段本身没有拉伸（原本就没有 `width: 100%`）。平板（`180px minmax(0, 1fr)`）与手机（单列 + 下拉导航）断点不变。
- 测试：新增 `views/settings/settingsLayoutWidth.test.ts`（内容列 fluid、整页上限、两个断点不变）。

**验证**

- `npx vue-tsc --noEmit` 退出码 0；`npx vitest run` → `Test Files 232 passed (232)` / `Tests 1560 passed (1560)`；`npx vite build --outDir ../tmp/web-dist-check --emptyOutDir` → `✓ built in 5.39s`。
- Go：`go build ./...`、`go vet ./...` 退出码 0；`go test ./internal/httpapi/ ./internal/commands/ ./internal/client/ -count=1` → 三个包 `ok`（httpapi 98.1s / commands 111.2s / client 8.7s）。
- 实机核对（dist 构建 + playwright 拦截 `/v1/**` 的合成桩，桩数据不进仓库）：zh / en × 浅 / 深 × 1440 / 1920 共 8 组，逐项量尺寸与文案——
  - 设置页内容区：1440 下 `.settings-pane` 920px（原来是封顶 720px 的列）、`.settings-body` 1162px；1920 下 1238 / 1480px；两种宽度都没有横向溢出（`scrollWidth` 1430 / 1910）。
  - 信箱：右栏头部是「对话 cv-1e07803035」（en：`Conversation cv-1e07803035`），复制按钮在，两封信各一个 thread id 复制入口；输入框只剩 Composer 自己的一个 📎（旧附件条的 `attach-button` 不再出现）。
  - 置顶：未置顶时 `置顶` / 87×23px（紧邻的「打开 job 详情」是 95×23px，同一行高度一致）、图标 14px、`aria-pressed=false`；置顶后类名带 `on`、文字变 `取消置顶`、颜色与边框都是 `--accent`（`rgb(91,195,159)`）；≤760px 时 `.pin-text` 为 `display: none`、`aria-label` / `title` 仍为「置顶 / 取消置顶」。
