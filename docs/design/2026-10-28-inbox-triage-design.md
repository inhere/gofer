# Inbox Triage：今天页双栏分诊与个人状态设计

> issue：gofer-addo
> 状态：0.1 草稿
> 相关：`web/src/views/Today.vue`、`internal/today`、`docs/design/2026-10-09-n3-today-decision-home-design.md`、`docs/design/2026-10-16-session-topics-mailbox-and-inbox-design.md`、`internal/notify`、G045/G056

## 1. 背景与问题

当前「今天」页把所有需要人介入的来源聚合为一个 `decisions[]` 队列：交互提问与权限请求、OPEN decision、持续会话等回复、待批准 job、待验收 job、工作项、阻塞 plan、收集箱和待指派信件都由 `internal/today` 统一排序，前端 `DecisionQueue` 再按「会超时 / 卡住别人 / 可稍后」分组。这个模型适合快速拍板，但不适合长期分诊：

- **动作不同却混在一起**：回复、批准、恢复和验收的认知成本不同；「等你」是即时决策，「待验收」需要阅读汇报、diff 和验证结果。
- **来源状态与个人阅读状态不是一回事**：mail 的 `read_at` 是信件送达语义，decision 的 ack 是回答协议，`today.action` 是操作审计；它们都不能代表某个用户是否在 Today 上看过或清理过一条项目。
- **通知与 Today 可能漂移**：`internal/notify` 已有事件目录、webhook 默认事件集合和更窄的桌面默认集合；如果双栏另写一套规则，会出现页面显示但没有推送，或推送了不再需要处理的事件。
- **手机上需要连续处理**：桌面两列能同时看全局，手机必须保持「先处理，再验收」的优先级，并让筛选、批量已读和清理不被横向布局挤掉。

现有实现的重要事实：`GET /v1/today` 是聚合读接口，卡片动作仍调用各自已有写接口；`GET /v1/today/handled` 读取 `today.action` 审计；mail 的待指派信件是 `mail_held:box` 汇总卡；`GET /v1/today/lanes` 是并行泳道的独立读接口。本文只设计交互、分类、个人状态和数据契约，不改变这些来源的业务状态机。

## 2. 目标与非目标

### 2.1 目标

- 将 Today 的决策区稳定地呈现为两栏：**等你**（需要即时处理或指派）与 **待验收**（需要阅读产出并裁决）。
- 给出可解释、可测试的默认分类表；允许按事件、卡片类型、项目和执行机覆盖，但不允许脚本化规则在服务端执行。
- 将「已读」和「清理」设计为**按调用方 / 用户隔离**的 UI 状态，不改写共享的 job、decision、mail 状态；源事项变化时能可靠重新出现。
- 让 Today、IM webhook、桌面通知使用同一套事件目录和匹配语义，只在渠道层保留不同的默认打扰阈值。
- 桌面、窄屏和手机均能完成看详情、标记已读、清理和原有卡片动作；不牺牲现有的专注模式、稍后和撤销窗口。

### 2.2 非目标

- 不把 Today 变成新的任务、信箱或验收业务系统；不复制已有动作 API。
- 不改变 job `needs_review`、interaction、decision、mail 的状态机及其权限含义；Today 的「已读」不是批准、回答、ack 或 mail 的 `read_at`。
- 不把普通 job 完成、失败、每日摘要等纯信息事件强行塞进两栏；它们继续由 digest、泳道和通知中心承载。
- 不增加即时通讯式的已读回执、跨 server 同步或多用户协作编辑。
- 不在本稿实现 Web、CLI、MCP 或配置代码；这些是后续实施阶段的接口约束。

