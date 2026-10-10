# Web 控制台（使用者视角）

> gofer server 自带 web 控制台（手机可用，可装成 PWA）。这里讲人在 web 上能做什么、agent 该把用户引到哪一页。HTTP 接口不在本 skill 范围，见 <https://github.com/inhere/gofer/blob/main/docs/reference/http-api.md>。

## 目录

- [导航](#导航)
- [今天与「待我决策」](#今天与待我决策)
- [验收台与待批准](#验收台与待批准)
- [Jobs / Plans / Workflows](#jobs--plans--workflows)
- [Sessions 与工作台](#sessions-与工作台)
- [Works、Issues 与 Memories](#worksissues-与-memories)
- [Runners、Agents、Projects 与设置](#runnersagentsprojects-与设置)
- [Dashboard 统计页](#dashboard-统计页)
- [实时刷新与手机](#实时刷新与手机)

## 导航

- 「工作」组：今天 · 工作台 · Works · Plans · Jobs · Sessions · Issues · Dashboard；「配置」组：Agents · Runners · Projects · Workflows · Schedules。
- 左上角 Logo 回首页「今天」（`/today`）；连接状态圆点在 Logo 右侧；Works 菜单有「等我」数量徽标。
- 顶栏「新建 job」；Schedules 页标题栏「+ 新建」。

## 今天与「待我决策」

- **早报一行**：自上次打开以来完成 / 失败 / 新提交数，点开是当天工作摘要（开了管家时附点评）。
- **待我决策**：所有等人的事汇成卡片——工具调用审批、`gofer_ask_human` 提问、会话等回复、待验收 job、待批准 job、工作项等我 / 需现场 / 到期、整理与合并建议、阻塞的 plan、记忆整理建议、终端授权请求。按「会超时 / 卡住别人 / 可稍后」分组，前 5 张在首页，其余点「还有 N 张」。
- **卡片**：来源 · 项目 · 等了多久 · 多久后超时 · 「卡住 N」/ 标题（点了跳到负责的页面）/ 一句话 / 操作（批准、回复、通过、附意见重跑、采纳等）；「详情」里有阻塞明细、改动数据和管家建议。管家写过建议的卡，主按钮变成「按建议：X」。
- **撤销窗口**：点操作后卡片收起，底部提示「撤销 5s」，倒计时结束才真正执行；按 `z` 撤销。快超时的卡立即发送。
- **稍后**：卡片「稍后」→ 1 小时 / 明早 9:00 / 等相关 job 结束；稍后**不暂停超时**，到点仍按原规则兜底。到点、相关 job 结束或有新动静时卡片提前回来并带标记。队列底部「已稍后 N」可放回。
- **专注处理**：全屏一次一张；键位 `j` / `k` 上下张、`1`–`9` 按顺序触发操作、`a` 通过 / 采纳、`r` 回复、`i` 详情、`h` 稍后、`s` 跳过、`z` 撤销、`Esc` 退出；手机左右滑。
- **全局浮层**：任何页面点顶栏「待我决策 N」（会超时变红）或按 `g` 再 `d` 打开，处理完不离开当前页。
- **并行中泳道**：未结的工作项与在跑的 plan 一行一件事，显示在跑的 agent、进度点、健康度、耗时与用量；点行打开工作项抽屉或 plan 详情。
- 底部状态条：runner 在线数与离线名单、今日 job 用量、管家今日动作、版本；异常项变红。

## 验收台与待批准

- **验收面板**（job 详情页首，`needs_review` 时）：先列「验收标准」（勾选框只是本页对照），页签有汇报、发现（「## 发现但不碰」各条，可复制为 issue 命令）、经验（填 key 和 kind 后接受，或拒绝）、提交、Diff（按文件折叠，声明了 scope 时标出「范围外」文件）、验证、用量；底部 Accept / Reject（拒绝必写理由，可勾「自动续投」）。
- `/review` 列全部待验收 job（等最久的在前），行内 Accept / Reject。待验收 job 同时进「待我决策」（exec job 默认不进，勾「含 exec 待验收」才显示）。CLI 等价：`gofer job review <id>`。
- **待批准**（`--hold` 的 job）：job 详情页首「⏸ 等你批准」面板显示理由、完整命令、项目 / cwd / runner、过期倒计时，「批准」一键执行、「拒绝」可写理由。见 [hold-approval.md](hold-approval.md)。

## Jobs / Plans / Workflows

- **Jobs**（看板）：按状态过滤（含「⏸ 待批准」）；详情页有日志（运行中只拉末尾、断线续传、「加载更早」）、stderr 事件流的结构化视图、提交、verify、用量、唤醒块（列表 / 开关 / 新建 / 触发历史）、评论、worktree 区（「合并到基线」，远程 runner 灰显）、「重跑」（可改模型 / 预算等）、「继续会话」（续接方式单选）、「从此会话新开」、有权限时「删除」。
- **新建 job / 会话**：选项目、agent、runner（按项目允许的 runner 过滤，不可用项灰显并写原因）、模型、高级选项里的预算；选了 ACP agent 会默认切到「ACP 持续会话」。
- **Plans**：计划看板（拖到 `ready` 即派发，拖到 `done` / `skipped` 即人工标记）；plan 详情有 todo 进度、用量（jobs 与主 Agent 会话分开）、「主 agent 会话」面板与「发给主 agent」、决策作答。
- **Workflows**：扇出步显示为对比视图（每路一列，可并排展开 diff，`join: pick` 时「选这个并合并」，冲突时列出冲突文件、仓库已复原）；新建页有「从模板新建」向导（选项目 → 模板 → 按 vars 生成表单 → 预览步骤 → 提交）。

## Sessions 与工作台

- **Sessions**：终端会话列表（等回复置顶），RELAY 开关 auto / on / off 可直接切；会话抽屉里回复、送入终端、起新进程接管、唤醒、解除接管、催办、从此会话新开，显示会话名、当前目录、用量、所属工作项。离线会话灰色徽标。见 [sessions-relay.md](sessions-relay.md)。
- **工作台**：按线程看会话与 ACP 会话的对话（只呈现用户消息与 agent 回复，工具 / 思考从「查看过程」进 job 详情），新会话表单可选 runner。

## Works、Issues 与 Memories

- **Works**（`/work`）：顶部「等我 / 到期提醒 / 未整理」计数（点击筛选），按状态分栏或按工作区分组；卡片标出每个字段谁写的、何时；抽屉里改字段、标状态、搁置、提醒、完成 / 放弃、合并 / 拆分、关联、转为 todo、写备注、看日志、整理、请它汇报 / 写交接、采纳建议、删除已结束的项。右下角「问管家」。见 [work-items.md](work-items.md)。
- **Issues**：树形 / 平铺、批量操作、「同步」按钮；**Memories** 列表带类型与 doctor 标记。见 [tracker.md](tracker.md)。

## Runners、Agents、Projects 与设置

- **Runners**：server 本机与每台 worker 一张卡：在线状态、心跳、在跑 job、版本；「工作目录」折叠区显示默认工作空间、roots 映射与各项目在该 runner 上的路径（不存在或被拒的标红）；「传话人」抽屉（状态、最近投递、错误输出尾部、列出可见会话）；「升级」按钮与升级历史；「添加 worker」。
- **Agents**：每个 agent 的健康度徽标（绿 / 橙 / 灰）、「探针」按钮；从内置模板注入的 agent 标「内置」。
- **Projects**：项目表单（允许的 agent / runner、交互、exec 等）；内置 `default` 项目标「内置」。
- **Tunnels**：在线转发（可远程「停止」）、server 预设的启动 / 停止、导入本机预设。
- **设置**：编辑配置、「重新读取文件」、「工作项」页（整理器、摘要、管家）等，需管理员。

## Dashboard 统计页

`/dashboard` 回答「一段时间里干得怎么样」：范围切换 今日 / 近 7 天 / 近 30 天 / 全部，「复制统计」；Jobs、耗时、Git 活动、信号（轮次 / 工具调用 / 人介入）、产出趋势、活跃热力图、项目 Top 3、验收与计划、耗时最长 / 最快、用量（费用与 token，job 与终端会话合并）；底部「系统」折叠区是实时状态卡。没有数据来源的指标显示「—」。只统计 gofer 自己的消耗，没有供应商额度。老 job 缺指标时管理员可跑 `gofer tool stats-backfill`。

## 实时刷新与手机

- 页面实时刷新；连接断开超过 15 秒自动改为低频轮询，恢复后自动切回。顶栏状态点显示「已连接 / 重连中 / 已断开·兜底轮询」，server 升级后提示「有新版本」。
- 手机用 HTTPS 打开可「安装应用」成独立窗口 PWA 并收 Web Push，步骤见 [operations.md](operations.md)「HTTPS 入口」。
- 经反向代理访问时，代理需放行 WebSocket（Upgrade 头）、关缓冲、空闲超时大于 60 秒。
