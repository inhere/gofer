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

