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
  `composer.*` 是统一输入框 Composer 的通用文案：发送 / 排队 / 打断、附图、急件、说明开关。P4 实施时追加。）
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
