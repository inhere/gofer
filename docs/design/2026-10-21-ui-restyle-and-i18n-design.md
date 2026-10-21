<!-- template_id: design; template_version: 1.1.1 -->
# gofer web 界面改版与 zh/en 双语 设计

> 状态：Draft 0.1 / 待人工计划批准

> 思考模式：`RIGOROUS`（design）· Delivery Track：`Full` · SR1409 暴露度：低（未投产、可回滚）
> 核心目标：把「界面改版」与「zh/en 双语」冻结成一份可评审、可分期落地的设计，范围止于 gofer web 的壳层、基元层与文案层。
> scope freeze：`inhere-tools/gofer/web/**`、`inhere-tools/gofer/docs/design/**`；不含 Go 服务端接口语义、CLI/日志输出语言、tracker 数据模型。
> review budget：SR 严谨规划上限 8 轮（1 discovery + 1 confirmation + 最多 6 轮 blocking closure）；用尽即停并披露开放 blocker 与未复核修订。
> 停止条件：出现未决核心选择（§决策 D1–D3）、文档校验失败、或需要把范围扩到服务端/CLI 时停止回问。
> expansion_policy：`DEFER_OR_REQUEST`（新增模块/协议/部署/数据动作一律 deferred 或先请求扩围）。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-21 | suga | 初稿：壳层与基元改版方向、zh/en 文案架构、分期与决策 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

## 背景与目标

用户提出两件事：① 现在的 web 控制台「有点丑」，要更现代的设计，且 PC / 手机都要撑住；② 界面要不要做 zh/en 双语、成本多大。已先出静态 demo 给用户看效果（§已确认事实 E1），本设计把 demo 里隐含的取舍写成可评审的决策，并把两件事排成一条可实施的路径。

用户可见目标：

- **看得清**：一屏内的重点（待我拍板 / 并行中）先于统计数字；文字不再靠 10–11px 挤，正文 14、微字下限 12。
- **找得到**：导航从「顶栏一行塞 14 个目标」变成分组的左侧轨道；手机是底部 5 标签，拇指区可及。
- **换得动语言**：顶栏一处切换 `中文 / English`，界面文案、`title`/`placeholder`/`aria-label`、`<html lang>` 一起切；命令、路径、job id、agent 输出不译。
- **改得动代码**：先补一层共享基元，之后新增页面不再复制 `.card`/`.empty`/`.chip`。

## 名词

| 名词 | 含义 |
|---|---|
| 壳层 chrome | 页头/主导航/顶栏/贴底状态条/抽屉与浮层的总称，不含业务页面内容 |
| 基元 element layer | 按钮、徽标、输入、卡面、表格、空状态、浮层等共用类，落在 `web/src/styles/base.css` |
| token | `web/src/styles/tokens.css` 里的 CSS 变量；颜色已在，本设计新增尺度、派生色与浮层阴影 |
| 文案层 | 用户在界面上读到的文字；不含命令、配置键、字段名、日志与 agent 输出（既有政策 `web/src/utils/uiText.ts:1-6`） |
| 已集中处 | `uiText.ts`（48 个 key）与 `statusText.ts`（28 个状态）以及 `utils/` 下 18 份 label map |
| 膨胀比 | 同一标签 en 宽度 ÷ zh 宽度；本设计用 1.4–2.6× 区间做版式约束（§已确认事实 E5） |
| demo | `docs/design/2026-10-21-ui-restyle-preview.html`，单文件静态原型（4 屏桌面 + 2 屏手机 + 基元页 + zh/en + 深/浅色） |

## 范围与非目标

范围：

1. `web/src/styles/`：扩展 `tokens.css`，新增 `base.css`（基元层）。
2. 壳层：`web/src/App.vue` 的顶栏/抽屉/贴边标签 → 左侧轨道 + 顶栏工具行 + 贴底状态条（`/today`）+ 窄屏底部标签栏。
3. 页面：`views/` 逐屏替换到基元类；首轮只做 `Today`、`Board`、`Workbench`、`Dashboard`。
4. 文案层：新增 `store/locale.ts`、`i18n/`（`t()` + `zh-CN` / `en-US` 字典），把 `uiText` / `statusText` / 18 份 label map 迁入字典。
5. 格式化：新增 `utils/format.ts`，收敛三套时长方言与 `toLocaleString`。
6. 文档与原型：本设计与 demo 放 `docs/design/`。

非目标：

- 不改变任何 HTTP 接口语义、状态机、schema、鉴权与审批模型（`web/src/api/client.ts` 的 envelope 形状不动）。
- 不为 CLI / 服务端日志 / agent 输出做双语；它们属「值」不属界面文字。
- 不引入 UI 组件库、Tailwind 或 CSS-in-JS（既有决定，`docs/design/2026-06-16-web-console-design.md:271`）。
- 不做第三种语言、不做 RTL、不做语言包热插拔/远程下发。
- 不改品牌三色与超家族字体（`docs/brand/README.md:3,21-25`）。
- 不重排信息架构：`/today` 仍是落地页，导航项与路由不变。

## 已确认事实与规范

已确认事实（本会话实测，可复现）：

| # | 事实 | 证据 |
|---|---|---|
| E1 | 静态 demo 已完成并本地提交；1440/390 × zh/en × 深/浅 全屏核对无横向溢出 | `docs/design/2026-10-21-ui-restyle-preview.html`；commit `c72cb755` |
| E2 | 顶栏一行承载 6 个导航链 + 更多 + 设置 + 6 个状态/动作按钮；≤900px 退化为抽屉 + 可拖动半圆 | `web/src/App.vue:262-326`、`:367-384`、`:423-466` |
| E3 | 无共享基元层：106/107 个 SFC 各写 `<style scoped>`，`.empty` ≥20 份、`.card` 9 份、`.chip` 6 份、`.panel` 6 份 | 例 `views/Work.vue:552`、`views/Board.vue:989` |
| E4 | 无尺度 token：`tokens.css` 只有颜色 + `--radius:4px` + 两族字体；`10–11px` 声明散落在约 90 个 SFC | `web/src/styles/tokens.css:5-20` |
| E5 | 中英同义标签实测膨胀 1.46–2.57×（表头「进度」2.06×、状态「需关注」2.57×） | demo「双语与文案长度」面板，canvas 实测 |
| E6 | 文案集中度约 5%：`uiText` 48 key 仅被 20/110 个 `.vue` 引用；`.vue` 中文 ≥2000 行、`api/store/utils` 962 行/89 文件、345 个 `placeholder/title/aria-label`、18 处 `window.confirm` | `utils/uiText.ts:7-70`、`utils/statusText.ts:14-46` |
| E7 | 服务端把中文写进 `error`/`detail`，前端原样拼接展示 | `internal/httpapi` 28 个文件（如 `auth_handler.go:251-254` 权限组标签）、`web/src/api/client.ts:166-174` |
| E8 | 格式化分散：3 套时长方言、17 处 `toLocaleString`（15 处随浏览器语言漂）、`dashStats.ts:216` 写死 `en-US`、13 处 `localeCompare`、0 处 `Intl` | `api/time.ts:45-90`、`utils/today.ts:46-58` |
| E9 | 测试被中文文案与 class 文本强耦合：177 个测试文件、110 个断言中文（624 行）；另有 46 个文件断言 `data-testid`、29 处断言 `class="…"` | 例 `components/approvalPanel.test.ts:72`、`views/uiTextWiring.test.ts:51` |
| E10 | 无 i18n 依赖；`index.html` 写死 `lang="zh-CN"`；`store/theme.ts` 已是「单例 ref + `gofer.*` localStorage + init + `data-*`」范式 | `web/package.json:13-23`、`web/index.html:2`、`store/theme.ts:6-68` |

引用的中央规范（不复制正文）：SR1101 事实不重复、SR1102 设计简洁、SR1103 修订记录、SR1108 思考模式与 scope freeze、SR1137 阈值与人工计划 Gate、SR1204 范围确认、SR1401/1412 追问与授权消费、SR1403/1405/1410 最小充分路径与证据边界、SR1409 暴露度。

已知文档漂移（本设计以代码为准，不回改历史文档）：`--phosphor` 在旧原型与 `docs/design/2026-06-16-web-console-design.md:172` 记 `#4FB0C6`，现行 `tokens.css:14` 为 `#2BD9F5`（浅色 `#0369A1`）。

## 总体方案

一次改版、两条正交主线，样式先行、i18n 紧跟：

- **主线 A（样式）**：token 扩展 → `base.css` 基元层 → 壳层 → 逐屏替换。
- **主线 B（文案）**：locale store + `t()` → 已集中处（48+28+18 份 map）先入字典 → 壳层与基元文案 → 6 个重页面 → 服务端 code 化。

两条合并的理由：基元层会把「按钮/空态/toast/表头」的文案一次性收敛到少数文件，B 的抽取成本在 A 之后显著下降；反过来，B 需要 A 提供的最小/最大宽度、换行规则和 `tabular-nums`，否则各页仍会各自撑破。

分期（每期独立可验收、可回滚）：

| 期 | 内容 | 产出 | 验收 |
|---|---|---|---|
| P0 | `tokens.css` 扩展（尺度/派生色/浮层阴影/`--stats-bg`/`--rail-bg`）+ `base.css` 基元层 | 2 个文件 + 基元页对照 | 基元页覆盖 demo 全部类；旧页面 `npm run build` 与现有测试不变 |
| P1 | 壳层改造（左轨 + 顶栏工具行 + 贴底状态条 + 底部标签栏），下线 `rail-toggle` | `App.vue` + `utils/nav.ts` + 相关测试 | 1440/1024/900/390 四档无横向滚动；导航可达 14 项 |
| P2 | 4 个首批页面（Today / Board / Workbench / Dashboard）换基元类 | 4 个 view + 局部组件 | 逐屏 zh/en × 深/浅 截图核对；相关 mobile 测试通过 |
| P3 | locale 层落地：`store/locale.ts`、`i18n/{zh-CN,en-US}.ts`、`t()`、顶栏切换、`<html lang>`、`format.ts` | 新增 5 文件 + `main.ts` 接线 | 切换后无残留中文（除白名单）；`?lang=`/localStorage/`Accept-Language` 三条入口一致 |
| P4 | 页面文案迁移（重页面逐屏）+ 服务端 code 映射 | 逐屏提交 | 每屏 `?raw` 断言同步更新后全绿 |

> P0–P2 与 P3–P4 可并行由不同 job 承担，但 P3 的字典 key 命名必须先冻结（§决策 D4）。

## 架构

### token 与基元（样式主线）

```
tokens.css       语义色（已有）+ 尺度/派生/浮层（新增）    ← 唯一颜色与尺度真源
   ↓ var()
base.css         .btn .chip .field .seg .card .tbl .empty .toast .nav-shell
   ↓ class
SFC scoped      只写「本页布局」，不写颜色/圆角/字号常量
```

新增 token（命名与 demo 一致，便于对照）：

- 尺度：`--fs-11..--fs-32`、`--sp-1..--sp-7`、`--r-xs..--r-xl`、`--t-fast`/`--t`。
- 派生色：每个状态色加 `-bg` / `-line` 两档（深色 12%/34%，浅色 9%/30%）。
- 表面与浮层：`--rail-bg`（暗色比页面更深、浅色更白）、`--stats-bg`、`--sh-1`（仅内高光）、`--sh-2`（浮层）。
- 数字：`font-variant-numeric: tabular-nums` 由基元统一给，页面不再各写。

### 壳层（桌面 / 窄屏）

```
┌ rail 248 ┬ appbar 58（搜索 ⌘K / 语言 / 信箱 / 通知 / 新建）─────────────┐
│ 今天 4   │ page-head(上下文行 · h1 · 副标题 · 主操作)                  │
│ 工作台   │ scroll ....                                                │
│ ...      │                                                            │
│ 设置     ├ stats bar（/today 专用，滚动区外的固定条）───────────────────┤
│ 用户     └ tabbar（≤900px：今天/工作台/工作项/任务/更多 + safe-area）────┘
```

- 桌面：左轨固定，`.main` 内 `appbar / scroll / stats / tabbar` 四段 flex；`stats` 与 `tabbar` 在滚动区外，内容不会被浮条遮住。
- 窄屏（≤900px）：隐藏左轨，顶栏只留搜索 + 2 个图标 + 主操作，导航改底部 5 标签（Material 3–5 项上限），删除 `rail-toggle` 及其拖动/持久化逻辑。
- 浮层统一：侧板、toast、菜单用 `--sh-2` + 同一套边界；深色下靠表面提亮分层，不给每张卡加同一种软阴影。

### 文案层（locale）

```
store/locale.ts     ref<string> + localStorage('gofer.locale') + initLocale() + setLocale()
      ↓
i18n/index.ts       t(key, params) / tn(key, n) / dt(ms, opts) —— 唯一取词入口
      ↓
i18n/zh-CN.ts       { 'nav.today': '今天', ... }
i18n/en-US.ts       { 'nav.today': 'Today', ... }
      ↓
<组件>               {{ t('nav.today') }} / :title="t('act.refresh')"
```

key 分域：`nav.*`、`page.*`、`col.*`、`status.*`、`act.*`、`empty.*`、`err.*`、`toast.*`、`confirm.*`、`hint.*`。约束：

- 字典是 flat、单层；`t()` 未命中时回落 `zh-CN` 值并在 dev 下 `console.warn` 出 key 名。
- 插值只允许 `{name}` 形式参数，不做复数机件；数量与单位一律走 `format.ts`（中文「4 项」/ 英文「4 items」用同一 key + 单位 key）。
- `title` / `placeholder` / `aria-label` 属可见文案，必须走 `t()`；命令、路径、job id、agent 名用 `<span lang="en">` 包裹后原样输出（WCAG 3.1.2 的 proper-name 豁免）。
- `index.html` 的 `lang` 与 `<html data-lang>` 由 `initLocale()` 在挂载前设定（与 `initTheme()` 同位置，防闪烁）。

### 格式化层

`utils/format.ts` 提供 `fmtAgo` / `fmtDuration` / `fmtCountdown` / `fmtNum` / `fmtDateTime`，内部用 `Intl.*` 并按当前 locale 取 minutes/seconds 等单位；页面里的 `toLocaleString` 与手拼时长一律改为调用它。三套方言收敛为两套语义：相对时间（`3m ago` / `3 分钟前`）与时长（`1h05m`），`minutesText` 的「N 分钟」并入相对时间。

## 关键流程

**F1 语言切换**

```
用户点顶栏 中文/English
  → setLocale(l)：写 store + localStorage('gofer.locale') + <html lang/data-lang>
  → 订阅 locale 的组件重渲染（t() 读 store）
  → format.ts 的 Intl 实例换 locale
  → 400ms 内完成，不刷新页面、不跳路由
```

**F2 首访判定**：`localStorage('gofer.locale')` → 若无，`navigator.languages` 首个以 `en` 开头的取 `en-US`，否则 `zh-CN`；服务端 profile 持久化留到后续设计（§待确认 Q2）。切换结果不回写 URL，`?lang=` 只作调试与分享。

**F3 文案抽取（每屏一次）**

```
识别中文节点/属性 → 判断属「界面文案」还是「值」 → 归域命名 key
  → 写 zh/en 两侧 → 替换为 t('key') → 更新该屏 ?raw 断言
  → 单屏提交（SR1211 原子提交）
```

**F4 样式落地（每屏一次）**

```
读 demo 对应屏 → 用基元类重写模板 → 只保留布局 CSS
  → 四档断点截图（1440/1024/900/390）× 深/浅
  → 跑该屏 mobile/wiring 测试 → 单屏提交
```

## 安全、数据、运维与回滚

- **安全**：本设计不新增网络面与权限路径；语言只影响文案选择，不参与鉴权判定。既有约束继续有效：ANSI/富文本渲染必须过 DOMPurify（`docs/design/2026-07-02-web-console-v3-design.md:253,267`），字典值不得进 `v-html`。服务端在 P4 前仍返回中文 `detail`，前端**不**据其做逻辑判断（现状也不做）。
- **数据**：新增本地键 `gofer.locale`（可清）；无 schema、无迁移、无后端字段。服务端 profile 语言属后续独立设计。
- **运维**：无部署形态变化；`vite build` 产物体积预计 ±10KB（字典 + base.css）。灰度 = 保留 `?lang=` 与 localStorage 开关，默认 zh。
- **回滚**：样式回滚只需保留旧类名映射或整体 revert 该屏提交（token 是新增，不替换旧变量）；i18n 回滚＝把 `t()` 退回读 `zh-CN` 字典（组件不必改），或 `setLocale('zh-CN')` 强制。两主线互不阻塞。

## 决策

| ID | 决策 | 理由 | 备选与不选原因 |
|---|---|---|---|
| D1 | 语言切换常驻顶栏右侧，文字选项 `中文 / English`，即时生效 | W3C 要求每页可见、用目标语言书写；不用国旗（flags are not languages） | 放设置页＝入口太深；国旗＝错误映射 |
| D2 | 默认 zh-CN，首访读 `Accept-Language`，此后以 localStorage 为准 | 中文用户零配置；英文用户首访即正确 | 固定 zh＝英文用户要去找开关 |
| D3 | 先自写 `t()`（零依赖，flat 字典），不引入 vue-i18n | 起步只有约 250 key；`t()` 形态与 vue-i18n 兼容，超阈值再换不返工 | 直接引 vue-i18n＝多一份插件/加载与测试成本，当前收益不足 |
| D4 | key 用英文语义（`nav.today`），不用中文原文当 id | 中文改动不该改 id；便于检查未覆盖项 | 中文当 key＝改文案即改代码引用 |
| D5 | 服务端文案改「前端映射 code」，接口保持兼容 | 不改 API 语义（非目标），中文 `detail` 作 fallback | 让服务端按 `Accept-Language` 渲染＝要动 28 个 handler 与 envelope 契约 |
| D6 | 统一 `utils/format.ts` + `Intl`，数字一律 `tabular-nums` | 现在 17 处 `toLocaleString` 随浏览器语言漂、3 套时长方言 | 各页自拼＝切语言后数字/日期格式不一致 |
| D7 | 桌面左轨 + 顶栏工具行；窄屏底部 5 标签 | Carbon UI shell（>5 项用左面板）；Material 底栏 3–5 项 | 保留顶栏 14 目标＝继续挤；抽屉 + 半圆＝拇指不可及 |
| D8 | 圆角 4 → 12（浮层 16），深色靠表面提亮、阴影只给浮层 | 低圆角仍是产品性格；逐卡同一种软阴影+同圆角是最典型的模板感 | 保留发丝线直角＝像 2015 后台；全圆角气泡＝丢身份 |
| D9 | 首屏不再放四张大数字卡，数字进 `/today` 贴底状态条 | 让「待我拍板」成为第一眼；大数字+小标签+渐变是默认套路 | 保留四宫格＝首屏被统计挤占 |
| D10 | 标识符/命令/路径/job id 不译，`aria`/`title` 走 `t()` | GitLab 翻译指南 + WCAG 3.1.2 豁免 | 全量翻译＝命令被译坏，排障断链 |

## 待确认事项

| ID | 问题 | 影响 | 我的建议 |
|---|---|---|---|
| Q1 | 手机端语言切换放「更多」面板还是顶栏 | 顶栏窄屏空间、可发现性 | 放「更多」首行，顶栏只留搜索与主操作 |
| Q2 | 语言是否要跨设备持久化（服务端 profile） | 需新增字段与接口，属独立设计 | 先本地，等出现第二台设备诉求再做 |
| Q3 | `Jobs / Issues / Agents / Runners` 与「统计 / 计划」的中英口径是否统一成「中文优先、专有名词英文」 | 导航与 `NAV` 文案表 | 维持现状（`uiText.ts:7-22`），本设计不扩充词表 |
| Q4 | P2 与 P3 谁先 | 并行 job 的排期与目录锁 | 样式先行（P0–P2），i18n 紧跟（P3） |
| Q5 | 是否接受「首屏去掉 KPI 四宫格」（D9） | 用户对首屏信息量的偏好 | 接受；统计数字进 `/dashboard` 与贴底条 |

## 结论与人工计划 Gate

本设计确立：两条正交主线（样式 / 文案）、壳层结构、token 与基元分层、locale 架构与 key 规范、10 条决策与 5 个待确认项。设计只确立决策，**不授权规划与实施**。

下一步需要具名批准：

1. 用户回复「批准 gofer web 界面改版与双语设计 Draft 0.1」→ 才进入 `docs/plans/2026-10-21-ui-restyle-and-i18n-plan.md` 的实施计划编写；
2. 计划再经一轮审核 + 人工计划批准，才解除实施 Gate（SR1137）。

附：可核对产物

- 原型：`docs/design/2026-10-21-ui-restyle-preview.html`（`?view=today|workbench|board|kit|phone`、`theme=light`、`lang=en`、`sheet=1`）
- 字体：`docs/design/fonts/`（offline，来源 `web/node_modules/@fontsource`）
- 提交：`c72cb755 docs(web): 界面改版样式提案预览（三屏 + 手机 + zh/en）`
