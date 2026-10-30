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

## 3. 方案

### 3.1 交互结构与双栏判定

双栏只改变 Today 的投影，不改变 `internal/today.Service.Decisions` 的来源读取和排序。服务端返回每个卡片的 `column` 与 `inbox_state`，前端先按 column 分栏，再在栏内沿用现有 `urgency`、`blocks.score`、`waiting_since` 排序。默认排序仍然是「会超时」优先，其次阻塞分数和等待时长；双栏之间不互相抢排序。

| 来源 / 条件 | 默认栏 | 是否进入默认 Today | 默认清理语义 | 说明 |
|---|---|---:|---|---|
| `interaction`：提问、工具权限 | 等你 | 是 | 源事项解决后消失 | 有可执行回答，属于即时决策 |
| OPEN `decision`、未读 relay turn | 等你 | 是 | 回答 / ack 后消失 | Today 已有 `refs.decision_ids` 聚合语义 |
| `session.awaiting_reply` / 可回复会话 | 等你 | 是 | 回复后消失 | 「等你」和会话通知使用同一事件 |
| `approval`：`awaiting_approval` | 等你 | 是 | 批准 / 拒绝后消失 | 不因 Today 已读而改变 job |
| `work`：`needs_me`、`needs_onsite`、到期提醒 | 等你 | 是 | 状态或提醒源变化后重算 | 保留现有回复、请求汇报、搁置动作 |
| `plan_blocked` | 等你 | 是 | plan 恢复或 blocker 变化后重算 | 仍跳转 plan 详情 |
| `mail_held`（待指派信件汇总） | 等你 | 是 | 信件被重新指派、取消或过期后重算 | 「已读」不等于 readdress / cancel |
| `review`：job `needs_review` | 待验收 | 是 | accept / reject 后消失 | 详情复用现有 ReviewPanel 数据 |
| `review` 且 agent 为 exec | 待验收 | 否 | — | 与现有「含 exec 待验收」开关保持一致；用户可显式打开 |
| `capture`、`suggestion`、`merge`、`memory` | 等你 | 依来源 | 源事项解决后消失 | 继续沿用 Today 现有卡片；不是新增第三栏 |
| 普通 `job.terminal`、`work.digest`、泳道健康变化 | 不进入双栏 | 否 | — | 进入 digest、泳道或通知，不制造待办噪声 |

**规则优先级**：先应用用户 / 项目过滤，再应用 `enabled` 与 exec 开关，最后按有序规则决定栏位。默认规则是服务端内置的版本化规则表；没有命中或规则配置非法时回退默认表，不让一条坏配置把 Today 变成空白页。一个 item 只能命中一个栏位，禁止通过配置制造重复卡。

**栏内交互**：

- 每栏标题显示「未读 / 总数」，标题旁提供「全部标已读」和「清理已读」；操作是个人状态写入，不调用源事项动作。
- 卡片保留现有四行骨架、详情折叠、建议按钮、稍后和撤销。待验收卡的主按钮仍是通过 / 附意见重跑 / 看 diff；不在 Today 复刻验收面板。
- 「清理」只隐藏当前用户已经看过的 item。对仍有动作的源事项，清理不是确认完成：源事项的 `source_revision` 发生变化，或用户重新打开「显示已清理」时，item 可见；对 `review` 也不能用清理代替裁决。
- 进入页面不把所有卡自动标已读。推荐规则是：卡片进入可视区累计 800ms 即写 `seen_at`，详情展开或点击任一动作也写入；提供「全部标已读」处理低打扰批量场景。这样手机快速滑过不会把整栏误标已读。
- Today 顶栏徽标取两栏未读总和；可以分别显示「等你 N · 待验收 M」，但不重复统计通知中心或 worker 申请徽标。

### 3.2 默认规则与可配置形状

配置以全局 `server.inbox` 为默认规则，项目只作为规则匹配条件，不把 server/storage/准入信息放进项目瘦配置。规则只使用结构化谓词，禁止正则、表达式和任意代码：

```yaml
server:
  inbox:
    enabled: true
    rules:
      - id: human-action
        column: needs_me
        kinds: [interaction, decision, relay, permission, approval, work, plan_blocked, mail_held]
        events: [interaction.created, session.awaiting_reply, job.awaiting_approval,
                 plan.blocked, work.needs_me]
        priority: 100
      - id: review
        column: review
        kinds: [review]
        events: [job.needs_review]
        priority: 90
        include_exec: false
      - id: low-noise-review
        column: review
        kinds: [review]
        events: [job.needs_review]
        priority: 80
        include_exec: true
        projects: [<project>]
```

字段语义：`id` 稳定且只用于诊断；`column` 只允许 `needs_me` / `review`；`kinds` 与 Today wire 的 `kind` 对齐；`events` 使用 `internal/notify` 事件目录的 key；`projects` 为空表示全部；`include_exec` 只对 review 生效；`priority` 越大越先匹配。规则缺省时使用上表默认值；显式配置某个 `kind` / `event` 后，该项按 first-match-wins，不再把未列出的规则隐式补进来。配置校验必须拒绝未知 kind、event、column、重复 id 和同优先级的不可判定覆盖。

个人偏好（栏是否折叠、是否默认展开 exec、是否显示已清理）不写 YAML，而写用户状态表，避免一个调用方的手机偏好改变其他人的首页。规则本身是 server 管理员配置；后续若需要个人规则，只允许在同一结构化谓词集合内存为用户 override，并记录规则版本。

### 3.3 数据模型与生命周期

新增 `inbox_item_state`（additive）用于个人 UI 状态，不改来源表：

```sql
CREATE TABLE inbox_item_state (
  caller_id       TEXT NOT NULL,
  item_key        TEXT NOT NULL,
  column_name     TEXT NOT NULL,
  source_revision TEXT NOT NULL,
  seen_at         INTEGER,
  cleared_at      INTEGER,
  restored_at     INTEGER,
  updated_at      INTEGER NOT NULL,
  PRIMARY KEY (caller_id, item_key)
);
CREATE INDEX idx_inbox_state_caller_updated
  ON inbox_item_state(caller_id, updated_at DESC);
```

- `caller_id` 来自已认证 user-class caller；它是现有 server 能稳定识别的用户边界，不把浏览器 localStorage 当作身份。worker / job credential 不读写个人 inbox 状态。
- `item_key` 复用 Today 的稳定 key（如 `review:<job-id>`、`mail_held:box`）；若汇总卡代表多条来源，`source_revision` 使用 count + 最新 activity / 状态摘要的哈希，避免一封新信被旧的清理状态吞掉。
- `source_revision` 由服务端根据不含秘密的源状态生成，至少包括 source kind、状态、activity_at、动作集合版本和汇总计数；正文变化也应改变 revision。revision 变化时保留历史 `seen_at` 仅作审计，但把 item 视为未读且清理失效。
- `seen_at`、`cleared_at`、`restored_at` 都是 UI 语义。源对象的 `read_at`、`AckAt`、`reviewed_at`、`today.action` 不被反写；卡片动作成功后由下一次聚合自然移除状态，后台 GC 清理 30 天以上且源对象已消失的行。
- 并发写使用 `updated_at` 或 `state_version` 条件更新；批量操作部分失败时逐项返回结果，不覆盖其他设备刚刚的阅读状态。

状态机：新 item = unread；看见或展开 = seen；用户清理已读 item = cleared；源 revision 变化 = unread；用户点「恢复」= seen（不执行任何源动作）。`cleared` 不计入默认栏数量，但仍可由「显示已清理」筛选恢复。对 `mail_held` 汇总卡，清理只隐藏该用户的入口，不能改变 held 信件。

### 3.4 与 IM / 桌面通知一致

`internal/notify` 的 `EventCatalog` 是唯一事件词汇来源；Inbox 规则只引用这些 key，不复制一份字符串表。每个事件先经过同一份 `MatchIntent`（项目过滤、事件过滤、全局 / channel enabled、事件默认集合），得到「是否需要人处理」及 source revision；随后分三路：

1. **Inbox 投影**：只要事件对应的源事项仍处于可处理状态，就创建 / 更新 `item_key`，按双栏规则显示。Inbox 不受 webhook 或 desktop 开关影响，否则关闭桌面弹窗会错误地关闭网页待办。
2. **IM webhook**：继续使用 `MatchWebhooks` 的 webhook enabled、事件列表、项目过滤和重试投递；正文可带 Today 深链。它是外部通道，不读取某个用户的 `seen_at`，同一事件在多个用户之间不共享已读。
3. **桌面通知**：继续使用更窄的 `DefaultDesktopEvents` 和 `MatchDesktop`；它只负责打断运行 server 的机器上的人，不代表 Inbox 已读。桌面合并窗口内的多个事件应保留最新深链，并在文本中注明数量。

因此「一致」的含义是**同一事件 key、同一项目过滤和同一源事项生命周期**；「不同」的含义是渠道默认打扰阈值和暂停开关不同。配置中的 `server.notification.webhooks[].events`、`server.notification.desktop.events` 仍保持现有语义；新增 `server.inbox.rules` 不修改既有通知的默认集合。通知送达失败不改变 Inbox；Inbox 清理也不抑制后续通知。

### 3.5 接口：HTTP、CLI、MCP

**HTTP**（新增路由均需在 `internal/httpapi/authz_routes.go` 声明，并按 G056 生成权限参考）：

| 方法 | 路径 | 用途 | 推荐权限 / 作用域 |
|---|---|---|---|
| GET | `/v1/inbox` | 返回两栏、计数、规则版本和分页游标 | `Public` user-class；按 caller_id 过滤个人状态 |
| POST | `/v1/inbox/state` | 批量 `seen` / `clear` / `restore`，支持幂等 | `Public` user-class；只写自己的 caller_id |
| GET | `/v1/inbox/rules` | 返回生效规则、默认值、版本和可用事件目录 | `Public` user-class；不返回 secret / 内部配置路径 |
| PUT | `/v1/config/server` | 复用现有 server 配置补丁写 `inbox` 规则 | 现有 `config.write`；管理员行为，不是个人状态 |

`GET /v1/inbox` 的 item 投影应包含 `key`、`column`、`kind`、`title`、`summary`、`source_revision`、`state`、`actions`、`refs` 和 `deep_link`；不把完整 mail 正文、job secret、webhook URL 带进列表。详情仍走原有 job / mail / session API。批量请求带 `expected_revision` 时，规则变化返回 409 并要求重拉；状态端点不接受任意 item key，必须重新验证 item 当前存在且属于调用方可见范围。

**CLI**：按 G033，小工具归入 `gofer tool inbox`：`ls`（`--column`、`--unread`、`--json`）、`read <key>`、`clear <key>`、`restore <key>`、`rules`。CLI 与 Web 使用同一 HTTP API；`--json` 保持机器可读字段不本地化，文本输出走现有 CLI 语言解析。不要另建顶级 `gofer inbox`。

**MCP**：增加只读 `gofer_inbox_list` 与个人状态写入 `gofer_inbox_mark`；工具只允许 user/session 所属调用方的 item，不暴露全局配置写入。MCP 返回 `column`、`state`、`source_revision` 与原有 action 描述，agent 可以知道有待验收或待处理事项，但不能替用户批准、回答或清理来源；真正动作仍调用已有 Today / job / session / mail 工具。

### 3.6 权限与安全

- 个人状态的隔离键必须来自认证上下文，绝不接受请求体中的 `caller_id`；缺少 user caller 时拒绝。job credential、worker token 和匿名请求不能读写个人状态。
- Inbox 聚合沿用 Today 的 project ceiling；规则匹配在过滤之后进行，不能通过 `cleared` 或规则把调用方上限之外的 project 泄露出来。全局 `mail_held:box` 在受限调用方下仍按现有规则隐藏或只返回其可见范围。
- `source_revision` 只允许不可逆摘要，不包含 mail 正文、prompt、token、webhook secret 或完整 diff；日志只记 item key、caller、动作和结果。
- 清理是可逆且无副作用的 UI 操作；任何会改变 job / decision / mail 的按钮必须继续走原有权限检查与审计。`seen` / `clear` 的审计可低成本记录 `inbox.state`，但不混入 `today.action`，避免「看过」伪装成「已处理」。
- 配置规则在加载和写入时严格校验，未知事件不静默接受；事件目录输出只给描述和 key，不给运行时内部路径。规则版本变化时客户端丢弃本地缓存并重新拉取。

### 3.7 ASCII 布局草图

桌面（`>= 1100px`）：

```text
┌──────────────────────────────────────────────────────────────────────┐
│ 今天                 等你 4 · 待验收 2        [全部标已读] [筛选]      │
│ 自上次打开：完成 3 · 失败 1 · 新提交 5                              │
├───────────────────────────────┬──────────────────────────────────────┤
│ 等你                           │ 待验收                              │
│ 未读 3 · 共 4   [清理已读]     │ 未读 2 · 共 2   [清理已读]            │
│ ┌ interaction · 需要回答 ┐    │ ┌ review · job-123 ┐                 │
│ │ agent 的问题摘要       │    │ │ 3 个提交 · verify 通过 │             │
│ │ [允许] [回复] [稍后]   │    │ │ [通过] [附意见重跑]   │             │
│ └────────────────────────┘    │ │ [看 diff] [详情]      │             │
│ ┌ mail_held · 待指派 ┐        │ └─────────────────────┘             │
│ │ 信件 2 封待指派    │        │ ┌ review · job-124 ┐                 │
│ │ [去指派] [已读]    │        │ │ …                   │                 │
│ └───────────────────┘        │ └─────────────────────┘             │
├───────────────────────────────┴──────────────────────────────────────┤
│ 并行中 …                                                            │
└──────────────────────────────────────────────────────────────────────┘
```

手机（`< 760px`）：两栏不并排，改为顶部可横向滚动但不换行的分段切换；默认打开「等你」，切换到「待验收」后保留滚动位置和筛选条件。

```text
┌─────────────────────────┐
│ 今天     [等你 4] [验收 2]│  ← sticky segmented switch
│ 自上次打开…              │
│ [全部标已读] [筛选]      │
│ ┌ 等你 · 未读 3 / 4 ┐    │
│ │ 卡片（触控高 ≥44） │    │
│ │ [主操作] [详情]    │    │
│ └───────────────────┘    │
│ ┌ 下一张卡 ──────────┐    │
│ └───────────────────┘    │
│                         │
│ （切换）待验收 2         │
│ ┌ review 卡 ─────────┐   │
│ │ [通过] [重跑] [diff]│   │
│ └────────────────────┘   │
│ 并行中（可折叠）          │
└─────────────────────────┘
```

手机不显示桌面快捷键提示；卡片主操作条在详情底部保持可见但不覆盖软键盘；详情、回复框和清理确认均支持 `Esc` / 返回手势，横向滚动只用于 chip，不用于卡片正文。

### 3.8 与现有功能的关系

- **Today 聚合**：`internal/today` 继续负责读取来源、计算 `urgency` / `blocks`、过滤项目和生成 actions；新增的是一个分栏与个人状态投影层。`GET /v1/today` 可在兼容期继续返回 `decisions[]`，新增 `columns` / `inbox_state` 为 additive 字段；前端从新字段切换，旧客户端仍能看到原单列队列。
- **「等你」与顶栏入口**：Today、全局 NeedsMeButton / 决策浮层和手机分段栏共用同一份聚合数据与 unread 计数，不再各自计算交互、relay、待批和验收数量。原有 `/review` 保留为集中验收页，双栏的待验收卡提供跳转而不是替代它。
- **待验收**：复用 `GET /v1/jobs?status=needs_review` 与现有 ReviewPanel 的数据和裁决接口；Today 只负责摘要、个人状态和跳转。`include_exec` 与现有 Today 开关保持同一偏好，不在新接口定义第二套 exec 语义。
- **信件 / 收集箱**：`mail_held:box` 仍由 held 信件聚合生成，`/mail?state=held` 仍是指派入口；Inbox 的 `clear` 不调用 `MarkMailRead`、readdress 或 cancel。真正收件信的 `read_at` 仍表示收件方已读，不拿来作为 Today 的用户已读。
- **稍后、撤销和审计**：稍后继续使用 `today_snooze` 与唤醒规则；卡片动作继续先经过撤销窗口并写 `today.action`。Inbox `seen` / `clear` 是独立的 `inbox.state`，两者都不计入「今天处理了 N 张」。
- **泳道与通知**：泳道继续显示并行工作和健康度；普通完成 / 失败由 digest、泳道和通知承载，只有仍需要人动作的事件才成为 Inbox item。通知主题仍由 `pending`、`jobs`、`work`、`sessions`、`plans` 等现有推送触发刷新，新增 mail 变化时复用 mail topic。

## 4. 分阶段

| 阶段 | 内容 | 依赖 / 交付物 |
|---|---|---|
| P0 规则冻结 | 从现有 `internal/today`、`internal/notify`、mail 与 review 页面列出所有来源；固定默认规则、事件 key、exec 默认值和 source revision 组成 | 规则表、事件覆盖清单、旧客户端兼容说明 |
| P1 服务端投影 | 新增 `inbox_item_state` 迁移与 DAO；实现双栏分类、source revision、caller ceiling 过滤和 `GET /v1/inbox`；对 `/v1/today` 只加 additive 字段 | 新库与旧库升级测试、HTTP golden |
| P2 状态与 Web | 实现批量 seen / clear / restore、个人偏好、Today 双栏、顶栏计数、手机分段切换；保留单列回退与深链 | Web 单测、键盘 / 触控 / 无障碍验收 |
| P3 配置与渠道对齐 | 增加 `server.inbox.rules` 校验、规则目录接口；把 Inbox 与 notify 事件目录的覆盖测试接上，不改变 webhook / desktop 既有默认集合 | 配置示例、权限登记、生成权限文档 |
| P4 CLI / MCP 与收尾 | 实现 `gofer tool inbox` 和 MCP 工具；补 skill references、HTTP API 文档、变更记录；完成旧客户端和升级回滚演练 | G033/G045 相关文档、发布前全量验证 |

每阶段都保持旧 `/today` 客户端可用；任何 schema 先在迁移前保证基础 schema 不引用新列，符合 G057。P1 完成后再扩大 Web 交互，避免在没有个人状态存储时先做只能写 localStorage 的临时方案。

## 5. 测试与守护

- **默认分类表**：对每个 Today `kind`、每个默认 notify event、exec 开关、项目过滤和无项目全局卡各有表驱动用例；断言每个 item 至多一个栏位，普通纯信息不会进入双栏。
- **规则校验**：未知 key、重复 id、非法 column、冲突 priority、超长列表和空规则的默认回退都有失败用例；规则版本变化会使客户端重新拉取。
- **状态隔离**：两个 caller 对同一 `item_key` 分别读写 seen / clear，互不影响；同一 caller 多设备并发写不回退；job credential、worker 和越权 project 均返回拒绝或过滤后的结果。
- **source revision**：状态、计数、最新活动、动作集合和正文摘要变化分别触发重现；同 revision 的轮询不重复制造未读；源对象消失后的 GC 不影响仍存在的其他 item。
- **渠道一致性**：事件目录每个 key 至少覆盖 Inbox / webhook / desktop 的期望矩阵；验证「desktop 默认较窄但显式 events 可扩展」「关闭 desktop 不隐藏 Inbox」「webhook 投递失败不改变 Inbox」。
- **HTTP / MCP / CLI**：HTTP handler 测试覆盖游标、批量部分失败、幂等、409 revision 冲突、JSON 字段稳定；MCP 不得暴露配置 secret；CLI `--json` 通过快照，文本走语言入口。
- **Web 交互**：桌面双栏计数、空态、过滤、清理与恢复、动作后重拉；手机 320px / 375px / 414px 宽度无横向溢出，触控目标至少 44px；键盘焦点、屏幕阅读器 label、`prefers-reduced-motion` 和软键盘遮挡均有用例。
- **升级与生命周期**：用上一个发布版本 schema 建库后打开当前代码；验证服务停止后测试组件、数据库和临时目录清理顺序；跑相关 Go / Web 测试后再跑全量验证，按 G051–G057 处理失败。
- **守护扫描**：禁止在前端另写事件默认集合；Go 侧所有规则事件必须来自 `internal/notify` catalog；新增 `/v1` 路由必须有 authz route declaration；新增用户可见 CLI / MCP / HTTP 行为必须同步 references。

## 6. 风险

- **分类规则过宽导致第二个待办列表**：默认只纳入已有可执行动作和 `needs_review`，纯通知不进双栏；设置「显示已清理」与来源深链，避免用清理掩盖真正阻塞。
- **个人状态与共享源状态混淆**：明确表名、API 动词和审计 kind，禁止把 `read_at`、ack 或 reviewed 当作 Inbox read；代码评审把所有源写接口列为禁止调用清单。
- **事件目录演进造成漏分栏**：catalog 增加事件时要求同时补默认分类矩阵和渠道一致性测试；未知事件在配置校验时失败而不是静默落到等你。
- **多人 / 多设备竞态**：采用 caller + item 主键、source revision 和条件更新；客户端收到 409 即重拉，不做最后写入者覆盖全部状态。
- **汇总卡的 revision 不稳定**：汇总卡只使用 count、最新活动和有限摘要的稳定哈希，不把随机生成时间或心跳放入 revision；held mail 变化通过 mail state hook 触发刷新。
- **手机双栏降低可发现性**：默认首屏固定「等你」，标题同时展示两栏未读数，待验收不藏在菜单；保留 `/review` 深链和 URL filter，返回时恢复栏位。
- **通知过度打扰**：不把 Inbox 开关复用成通知总开关；IM / 桌面仍各有暂停、项目和事件过滤，只有公共事件词汇与源生命周期一致。

## 7. 待确认项

1. **两栏名称与范围**：推荐保留「等你」与「待验收」，前者只放可执行动作，后者只放 `needs_review`。理由是与现有 `Today`、`/review`、顶栏 NeedsMe 术语连续，用户一眼能区分「回答 / 指派」和「阅读 / 裁决」。
2. **默认是否纳入 exec 待验收**：推荐继续默认不纳入，只通过现有「含 exec 待验收」开关显式打开。理由是 exec 常用于验证 / 辅助任务，默认进入会扩大噪声；保持现有设计确认项，不引入第二套语义。
3. **个人状态的身份键**：推荐使用认证上下文的 `caller_id`，不增加浏览器用户表。理由是 server 当前已经用 caller 进行 ceiling 与审计，且无法区分同一 token 的 Web / CLI；新增账号体系超出本题范围。
4. **清理是否允许作用于仍可操作卡**：推荐允许个人隐藏，但源 revision 变化即复现，并在 UI 明示「不会执行动作」。理由是用户需要清掉暂不处理的噪声，同时不能把清理误认为批准；不提供永久删除。
5. **个人规则是否首期实现**：推荐首期只做全局 `server.inbox.rules`，个人首期只存折叠 / exec / 显示已清理偏好。理由是全局分类更易审计和测试，个人规则会让 IM、桌面与 Inbox 的解释难以统一；需要时再增加受限 override。
6. **已读触发时机**：推荐可视区累计 800ms、展开详情或明确批量操作时标记，快速滑过不算已读。理由是手机滚动很快，自动把整页标读会破坏未读计数；显式动作仍能即时更新。
7. **通知与 Inbox 的开关关系**：推荐独立开关，共用事件目录、项目过滤和源 revision，不共用 enabled 状态。理由是网页 Inbox 是可靠工作面，IM / 桌面是可选打扰渠道；关闭弹窗不应让待验收消失。
8. **是否新建 `inbox_item_state` 而不是复用 `today.action`**：推荐新表。理由是 `today.action` 是动作成功审计、按 caller 以外的现有记录语义读取；把浏览状态混进去会让「已读 / 清理」误计为已处理并破坏已有撤销 / 重拉逻辑。
9. **HTTP 路由权限**：推荐沿用 Today 的 user-class `Public` + caller 隔离，拒绝 worker / job credential；配置写仍走现有 `config.write`。理由是个人状态不是跨用户资源，新增独立 permission 会增加授予面而没有额外安全收益；实施时仍按 G056 登记每条路由。
10. **是否首期改造 `/v1/today` 而新建 `/v1/inbox`**：推荐先新建 `/v1/inbox` 并给 `/v1/today` additive 字段，稳定后再考虑前端合并请求。理由是旧客户端兼容、回滚和分阶段验证成本更低，也避免把 Today 的既有聚合契约变成强制个人状态依赖。

