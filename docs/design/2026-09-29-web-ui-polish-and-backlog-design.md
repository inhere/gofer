# Web UI 打磨（交接卡片 / Issues 页 / 菜单整理）与待办批次

> 状态：Approved（文档 identity：Draft 0.1；用户 2026-09-29 批准）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-29 | Claude | 用户看过 v0.74.1 截图后提出的 UI 调整，加上 4 个已登记待办 |

## 背景与目标

用户截图（`tools/tmp/gofer-plan-handoff.png`、`gofer-issues.png`）反映：

1. **plan 交接卡片**：编辑框是很小的原生 textarea，「保存交接说明」「展开历史」是默认样式按钮，常驻在正文下方；与 PlanDetail 其余部分的风格不一致。
2. **Issues 页**：输入框、按钮是浏览器默认样式，一排八个裸输入框，没有列表、没有空状态设计，完全不像其他页面（Plans、Board）。页面也不含 memory。
3. **菜单**：Review 几乎没用过；Skills 放在顶栏不合适。
4. 已登记待办：tools-dup、tools-3u5、tools-iyv、tools-kjq。

## 范围与非目标

- 范围：下列 UI-01…UI-04 与 BL-01…BL-04。
- 非目标：改变交接说明、tracker 镜像的数据模型与 API 语义（只允许为 UI 补必要的只读字段）；目录锁的重叠判定语义（BL-02 只做提示与文档）。

## 总体方案

### UI-01 plan 交接卡片

- 标题行：「交接说明」+ 右侧 **版本下拉**（默认最新版，列出全部历史版本：`vN · 更新人 · 时间`）+ **「+ 新增」** 按钮。
- 选中最新版本时显示 **「修改」** 按钮；选中历史版本时只读查看，并显示提示「正在查看历史版本 vN」与「回到最新」链接，不显示修改。
- 「修改」：在卡片内切换到编辑态，预填最新正文；「+ 新增」：编辑态为空白，写一份全新的交接。两者保存都是写入新版本（沿用 `expected_version` 冲突检测，409 时提示并刷新）。
- 编辑态：足够大的编辑区（至少 12 行、宽度与卡片一致、可拖高），「编辑 / 预览」切换（预览用 `MarkdownBlock`），「保存」「取消」按钮。
- 没有任何交接时：卡片显示空状态文案与「+ 新增」。
- 样式全部用现有 `tokens.css` 变量与现有页面的按钮/下拉/卡片类（参照 PlanDetail 其他卡片、Plans 的 `filter-select`、`page-btn`），不引入新的视觉语言。

### UI-02 Issues 页重做（含 Memories）

- 页面改名 **Tracker**（菜单项仍叫 Issues 也可，以用户偏好为准），两个标签：**Issues** / **Memories**。
- 顶部：仓库选择器（下拉，列出已登记仓库：`项目 · prefix · 路径`，未归属的标"未归属"），**不再让用户手填 tracker_id**；默认选最近同步的仓库。
- 筛选条：状态（多选 chip：open / in_progress / blocked / closed，默认隐藏 closed）、类型、标签、关键字搜索；样式对齐 Plans/Board 的筛选条。
- Issues 列表：表格行（id、标题、状态徽标、优先级、类型、标签、更新时间），点击行打开右侧详情抽屉（复用 SessionDrawer 类的抽屉样式）：字段、描述（Markdown）、notes 时间线、comments、deps；抽屉内可编辑状态/优先级/标题等与发表评论（沿用 `expected_rev` 409 处理）。
- Memories 列表：key、内容摘要、标签、更新时间/更新人；点击展开全文（Markdown），可编辑、删除（删除即墓碑，经同步传回仓库）。
- 空状态：未登记任何仓库时说明"在仓库里执行 `gofer repo init` / `gofer repo sync` 后出现在这里"。
- 顶部显示所选仓库的上次同步时间与冲突摘要（来自 `tracker_repos`）。

### UI-03 菜单：Review

- 现状：菜单 Review = 待验收队列（`status=needs_review`，即 `--require-review` 的 job，行内 Accept/Reject）。工作台的「等你」是**超集**：待回答的交互（answer）、等回复的中继会话（reply）、以及需要看一眼的结束 job（review，包含 needs_review 与未查看过的有改动的结束 job）。所以 Review 队列里的 job 本来就会出现在「等你」里。
- 方案：**从顶栏菜单移除 Review**，保留 `/review` 路由与页面。入口改为：
  - 顶栏在待验收数 > 0 时显示一个小徽标「待验收 N」（点击进 `/review`），为 0 时不占位（计数已由 EscalationBell 轮询维护）；
  - Board / PlanBoard 已有的"待验收"列链接保持；
  - 工作台「等你」里 needs_review 项的操作可直接跳 `/review` 或 job 详情的验收面板（现状保持）。

### UI-04 菜单：Skills 移到设置

- Skills 页移入「设置」侧栏（与 Tunnels、Rules、Notifications 同级），路由改为 `/settings/skills`，旧 `/skills` 重定向过去；顶栏移除 Skills。

### BL-01（tools-dup）job --upload 在 web 可见

- job 详情增加「随 job 上传的文件」区块（来自 `xfer` 字段：本地文件名、目标路径、大小、状态）；有上传的 job 在 Board/列表上显示一个小附件标记，并可按"含上传"筛选。先核对 `xfer_json` 是否已含所需字段、API 是否已返回，缺的只补只读字段。

### BL-02（tools-3u5）目录锁与只读会话

- 语义不变（同目录/祖先/后代互斥；只读、interactive、exec、worktree 不加锁）。
- job 进入 `waiting_dir` 时：CLI 输出、job 详情、Board 卡片提示「被 <holder> 占用；只读任务可加 `--read-only`，或 `--shared-dir` 放弃独占、`--worktree` 隔离」。
- gofer-usage skill 与 README 补一段"只读任务务必带 `--read-only`"。

### BL-03（tools-iyv）cli-agent 的 `--` 参数

- 非 exec 类 agent 提交时带了 `--` 位置参数（CLI）或请求里有 `cmd`（HTTP/MCP）：直接报错并提示改用 `--prompt` / `-f`，不再静默丢弃。

### BL-04（tools-kjq）容器会话 prime 识别项目

- P4 已让 `.gofer/tracker/config.yaml` 的 `project_key` 优先。验证容器内有 `project_key` 时 prime 交接段出现；无 `project_key` 时 `gofer repo status` 提示可填写。验证通过即关闭。

## 实施分期

| 期 | 内容 | 验收 |
|---|---|---|
| U1 | UI-03、UI-04、BL-03、BL-04（小改） | 菜单与重定向、`--` 报错测试、prime 用 project_key 的测试 |
| U2 | UI-01 交接卡片 | 版本下拉/修改仅最新/新增空白/409 的组件测试；截图对比 |
| U3 | UI-02 Tracker 页（Issues + Memories） | 仓库选择、筛选、抽屉编辑/评论、memory 编辑删除的组件测试；截图对比 |
| U4 | BL-01、BL-02 | job 详情上传区块、waiting_dir 提示的测试 |

每期测试先行、容器验收（web 三命令 + 真实进程冒烟 + agent-browser 截图给用户看）、发版时前端 + 主机 server + 容器 CLI 同步升级。

## 决策（已批准 2026-09-29）

1. UI-02 菜单名保留 **Issues**，页内分 Issues / Memories 两个标签。
2. UI-03 Review 移出顶栏菜单，顶栏只在待验收数 > 0 时显示「待验收 N」徽标（点进 `/review`）；Board 待验收列链接与工作台「等你」照旧。
3. 按 U1 → U4 派发。

## 结论与人工计划 Gate

用户批准后按 U1 → U4 派发；每期我在容器验收并附截图。
