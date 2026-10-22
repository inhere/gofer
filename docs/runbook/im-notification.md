# Runbook · IM 出站通知（钉钉 / 飞书）

> OBS-07a。把 gofer 的事件推到钉钉或飞书的群机器人，手机上被动收到提醒。
> 相关：[`session-relay.md`](session-relay.md)（会话中继，`session.waiting` 事件的来源）。

## 1. 它是什么 / 不是什么

- **是**：单向出站。事件发生 → gofer 渲染成机器人消息 → POST 给 webhook → 手机响。
- **不是**：双向。钉钉和飞书的**自定义机器人只能收不能发回**，在 IM 里回复不会回到 gofer。要回复就点消息里的链接进 web 会话页。真正的 IM 双向是 OBS-07c（飞书自建应用 + 长连接 + 卡片），尚未实施。

实现上没有新建 channel/connection 实体：在既有 webhook（OBS-03）上加了一个 `kind` 适配，投递队列、退避重试、投递审计、出站 URL 校验全部复用。

## 2. 建一个只有自己的群

钉钉自定义机器人只能加到群里，不能私聊。要让消息只有你自己看到：

1. 新建群，随便拉一个人（钉钉不允许直接建单人群）；
2. 建好后把对方移出，群里只剩你；
3. 群设置 → 智能群助手 → 添加机器人 → 自定义 → 拿到 webhook URL。

飞书允许直接建只有自己的群，省掉第 1、2 步。

## 3. 安全模式三选一

添加机器人时必须选一种，三种 gofer 都支持：

| 模式 | 配置 | 说明 |
|---|---|---|
| 自定义关键词 | `keyword: <关键词>` | 最省事。写上和机器人里一样的词（如 `gofer`）；消息标题 / 正文都不含它时 gofer 自动把 `gofer ` 加到标题前（钉钉的 markdown 标题和正文标题行都带） |
| 加签 | `secret_env: <环境变量名>` | gofer 按 kind 自动算：钉钉签名进 URL 参数，飞书进请求体 |
| IP 白名单 | 不用配置 | 在机器人侧填 server 机器出口 IP；主机出口 IP 变了就会失效 |

关键词和加签可以同时开（机器人侧两个都勾时，配置里两行都写）。
**被机器人拒绝时 gofer 记 failed，不再重试**：`keywords not in content`（钉钉 310000 / 飞书 19024）、`sign not match`（钉钉 310000 / 飞书 19021）、`token is invalid`（钉钉 300001）这类错误重试也过不去，投递记录里直接就是 `failed`，`last_error` 是机器人返回的原文。限流（钉钉 130101 / 飞书 11232）仍按退避重试。

## 4. 配置

```yaml
server:
  # 通知里那条可点链接的前缀。server 在反代/内网 IP 后面推断不出自己的公网地址，
  # 必须显式配；留空则消息里不带链接。
  web_base_url: https://gofer.example.com

  notification:
    # 出站 host 白名单。空 = 全部拒绝（fail closed），所以这行必须加。
    allow_hosts: [oapi.dingtalk.com, open.feishu.cn]
    webhooks:
      - url: https://oapi.dingtalk.com/robot/send?access_token=xxx
        kind: dingtalk
        keyword: gofer                     # 机器人开了「自定义关键词」才写；三个字的词不区分大小写
        secret_env: GOFER_DINGTALK_SECRET   # 加签模式才要；关键词模式删掉这行
        events: [session.waiting, session.attention]
      - url: https://open.feishu.cn/open-apis/bot/v2/hook/xxx
        kind: feishu
        events: [session.waiting, job.terminal]
        projects: [my-project]              # 省略 = 全部项目
```

`kind` 取值：`generic`（默认，原来的 `{event, job}` 机器契约 + HMAC 头）、`dingtalk`、`feishu`。写错会在 `gofer config validate` 直接报错，不会到发送时才失败。

加签模式下密钥从环境变量取，不入库不进日志：

```bash
export GOFER_DINGTALK_SECRET='SECxxxxxx'
```

改完执行 `gofer config validate`，再重启 serve。

## 5. 可订阅的事件

| 事件 | 何时触发 | 能在手机上处理吗 |
|---|---|---|
| `session.waiting` | 中继会话停下来等人回复 | ✅ 点链接进 web 回复 |
| `session.attention` | 中继会话弹权限确认等终端内对话框 | ❌ 只能回终端处理，通知是叫你回去 |
| `session.nudge_paused` | 会话催办（`gofer session nudge`）连续 3 次送达失败后自动暂停；**不在默认订阅集**，需显式写进 `events` | ✅ 点链接进会话抽屉，会话可达后点「恢复」 |
| `work.remind` | 工作项的提醒时间到了，或搁置（`park_until`）到期；每个到期点只发一次 | ✅ 点链接进 web「工作」页该工作项 |
| `work.digest` | 每日摘要（`work.digest_time`，默认 09:00；`work.digest_enabled: false` 关闭）：等我 / 等资源 / 需现场 / 待验收计数、搁置超 7 天、昨日有进展，带链接；正文由数据库确定性生成 | ✅ |
| `work.needs_me` | 工作项**进入**「等我」（自动映射、手动、会话汇报或管家标记都算）；**默认关闭**：`work.needs_me_notify: true`（web 设置 →「工作项」页）才发，同一工作项在 `work.needs_me_throttle_min`（默认 30）分钟内只发一次 | ✅ |
| `job.terminal` | job 进入终态 | — |
| `interaction.created` | 产生新的待人工交互 | — |
| `job.needs_review` | 带 `--review`/`require_review` 的 job 正常完成、停在 `needs_review` 等人验收（GATE-01 S3） | ✅ 点链接进 web job 页的验收卡 accept/reject |
| `job.reviewed` | 验收裁决已做出（accept/reject）；**非**默认事件，需显式订阅 | — |
| `plan.blocked` | 链式 plan（todo 带 `--after` 依赖）里某项 job `failed`/`timeout`/`cancelled`/`rejected`，整条链停在那一项（PLAN-03） | ✅ 点链接进 web `/plans/{id}`，或 `plan set-todo <todo> --status ready\|skipped` / `plan resume` |
| `plan.completed` | 链式 plan 的每一项都 done/skipped；**非**默认事件 | — |
| `agent.degraded` | 某 agent 的健康度从 healthy/unknown 变为 degraded（窗口内供应商错误达到阈值）；**非**默认事件（PLAN-03 §六） | — |
| `agent.recovered` | 该 agent 恢复到 healthy；**非**默认事件 | — |

两个会话事件都**只在该会话的中继开关打开时**才推：关着说明人就在键盘前，终端里能直接看到。
`session.attention` 会按内容去重——同一个提示反复弹不会重复推，换了个不同的提示才会再推一条。

**注意**：省略 `events` 时用的是默认集（`job.terminal` + `interaction.created` + `job.needs_review` + `plan.blocked` + `job.retry_exhausted` + `plan.leader_exhausted` + `session.awaiting_reply` + `work.remind` + `work.digest` + `work.needs_me`），**不含** `session.waiting`；`work.needs_me` 虽在默认集，但总开关 `work.needs_me_notify` 默认关，所以不会平白多出流量。想收会话提醒必须显式写进 `events`，这样既有配置不会平白多出流量。`job.needs_review` 只在**开了验收**的 job 上发生（`--review` / 项目 `require_review`），没开验收的项目不会因此多出通知。`plan.blocked` 只在**用了 `--after` 依赖的链式 plan** 上发生（平铺清单不会 block），且它是唯一进默认集的 plan 事件——链停住意味着"没人会再往前推"，和 `job.needs_review` 同属"必须有人动手"。

**想让"某个 agent 开始抽风"也上 IM**（PLAN-03 §六）：`agent.degraded` / `agent.recovered` 记在 agent 作用域（`agent:<key>`），可订阅但不进默认集，显式加一行即可：

```yaml
server:
  notification:
    webhooks:
      - url: https://oapi.dingtalk.com/robot/send?access_token=xxx
        kind: dingtalk
        events: [agent.degraded, agent.recovered, job.terminal, job.needs_review, plan.blocked]
```


## 6. 消息长什么样

钉钉走 markdown，链接可点；飞书走纯文本，客户端自动识别 URL。两者都是：

```
会话等待回复 · <会话标题>

<agent 最后一条消息，超过 500 字截断>

打开会话回复：https://<web_base_url>/sessions?sid=<会话id>
```

点链接直接落到会话抽屉，输入框就在那里。

## 7. 排障

| 现象 | 处理 |
|---|---|
| 完全没收到 | 先确认 `events` 里显式写了 `session.waiting`；再看 `allow_hosts` 是否包含目标 host（空列表=全拒） |
| 机器人回 `keywords not in content` | 关键词模式下关键词没出现在消息里。给该 webhook 写 `keyword: <机器人里那个词>`，gofer 会自动加进标题；不改配置就只能改用加签 |
| 机器人回 `sign not match` | 加签密钥不对，或主机时钟偏差过大（两家都要求时间戳在一小时内）。这类投递直接记 failed，不会重试 |
| 消息里没有链接 | `server.web_base_url` 没配 |
| 投递一直重试 | `gofer job` 侧的投递队列会退避重试 30s→2m→5m→15m→60m，到 `max_attempts` 后置 failed；失败原因记在投递记录的 last_error |
| 想临时静音某项目 | 该项目配 `notify_enabled: false` |

投递记录（含失败原因 `last_error`）可以在 web 的 job 详情页看到。会话 / 工作项通知不挂在 job 上，页面里看不到；serve 日志在**入队**失败时记 `NotifyEvent: render` / `NotifyEvent: insert delivery`，在**发送**失败时记 `DeliverDue: delivery failed`（终态，带机器人返回的错误原文）/ `DeliverDue: delivery retry scheduled`（还会重试，带下次重试时间），同时写进该投递记录的 `last_error`。排查这类通知时先按上表逐项核对配置；要确认事件确实产生了，可以临时加一个订阅同一事件的事件钩子（见第 8 节）把 stdin 写进文件看。

## 8. 事件钩子（server 主机上跑命令）

webhook 之外的另一条出口：事件发生时，在 **server 主机**上运行一条命令，事件 JSON 从 stdin 传入。适合写审计日志、触发本地同步脚本、转发给内部系统，而不用另起一个 HTTP 服务接 webhook。

它是**只读旁路**：命令的输出不回写 gofer，不影响 job / plan 的流程；成败只决定这一次投递是否重试。

### 8.1 配置

钩子是独立的 `server.event_hooks` 块，不在 `notification` 里：

```yaml
server:
  event_hooks:
    - name: audit-log                        # 必填，唯一；投递记录的 target = hook:audit-log
      command: ["python3", "/opt/ops/gofer_audit.py"]   # 必填，argv 形式，不经 shell
      events: [job.terminal, plan.completed] # 可选；空 = 与 webhook 相同的默认事件集；["*"] = 全部事件
      projects: [my-project]                 # 可选；空 = 全部项目（不属于任何项目的事件只匹配空过滤）
      dir: /opt/ops                          # 可选；工作目录，缺省 = gofer 配置目录，相对路径也相对它
      env: {AUDIT_ENDPOINT: "http://127.0.0.1:9000"}  # 可选；额外环境变量
      timeout_sec: 30                        # 可选；默认 30，上限 300
      max_attempts: 3                        # 可选；默认 3（含首次）
      enabled: true                          # 可选；默认 true，false = 暂停但保留配置
```

改完执行 `gofer config validate`（重名 / 非法名字 / 空命令 / 超时越界直接报错；命令在本机找不到给 WARN），再 `gofer serve reload` 生效。

与 webhook 的区别：

- `notification.enabled: false`（通知总暂停）和项目的 `notify_enabled: false` **不作用于钩子**——它们是「通知人」的开关；钩子只看自己的 `enabled` / `projects`。
- 不需要 `allow_hosts`、`web_base_url`、签名。
- 手动结束的会话 job 的 `job.terminal` 不发 IM，但钩子照样收到。

### 8.2 执行方式

- 命令不经 shell 直接执行；超时会杀掉**整个进程树**。
- **stdin**：与 generic webhook 的 POST body 完全相同的 JSON（会话 / 工作项等事件是它们的 generic 渲染），可以和 webhook 接收端共用解析代码。
- **环境变量**：serve 进程的环境去掉凭据变量（`GOFER_TOKEN` 等，与 job 相同的拒绝列表，含 `server.job_env_denylist`），再加：

  | 变量 | 含义 |
  |---|---|
  | `GOFER_EVENT_TYPE` | 事件类型，如 `job.terminal` |
  | `GOFER_EVENT_SEQ` | 事件序号；会话 / 工作项等不落事件表的事件为空 |
  | `GOFER_DELIVERY_ID` | 投递记录 id（重试时不变，可用于去重） |
  | `GOFER_JOB_ID` | job id；不属于某个 job 的事件（plan / 传输 / 会话等）为空 |
  | `GOFER_HOOK_NAME` | 钩子名 |

  钩子自己的 `env` 最后叠加，可以覆盖上面任何一个。
- **结果**：退出码 0 = 已投递；非 0 / 超时 / 启动失败 = 失败，按 webhook 同一退避表（30s → 2m → 5m → 15m → 60m）重试，到 `max_attempts` 记 failed。投递是**至少一次**：serve 在执行中途被停掉时，这条会在租约到期后再跑一次，脚本要能容忍重复（用 `GOFER_DELIVERY_ID` 去重）。
- **输出**：stdout 直接丢弃；stderr 末尾 1 KiB 经脱敏（钩子 env 的值、`token=` / `password=` 之类）后写进投递记录的 `last_error`。
- **并发**：一轮投递里钩子最多 4 个并行，和 webhook 发送互不等待，慢钩子不会拖住 IM 通知。
- **日志**：serve 日志每次执行记 `event_hook.ran`（name、event_type、delivery_id、exit_code、duration_ms），失败记 `event_hook.failed`（附脱敏后的 stderr 尾部）。
- 钩子在事件入队后被删除或停用：这条投递记 failed（`hook removed` / `hook disabled`），不再执行。

job 相关事件的钩子投递和 webhook 一样出现在 job 详情页的投递记录里（target 为 `hook:<name>`）。

### 8.3 stdin 样例与自测

一个 `job.terminal` 的 stdin：

```json
{
  "event": {
    "seq": 1287,
    "job_id": "job-20261011-101500-a1b2c3d4",
    "type": "job.terminal",
    "detail": {"status": "done", "exit_code": 0, "error": ""},
    "at": 1791684900
  },
  "job": {
    "id": "job-20261011-101500-a1b2c3d4",
    "status": "done",
    "project": "my-project",
    "agent": "codex",
    "runner": "local",
    "exit_code": 0,
    "title": "修复登录页跳转"
  }
}
```

会话 / 工作项事件的 stdin 是 `{"event": {"type": ..., "at": ...}, "title": ..., "text": ..., "link": ...}`。

写脚本时先把样例存成 `sample.json`，用管道喂给它自测，不必等真实事件：

```bash
GOFER_EVENT_TYPE=job.terminal GOFER_HOOK_NAME=audit-log \
  python3 /opt/ops/gofer_audit.py < sample.json; echo "exit=$?"
```

```powershell
$env:GOFER_EVENT_TYPE = 'job.terminal'
Get-Content sample.json -Raw | python3 D:\ops\gofer_audit.py; "exit=$LASTEXITCODE"
```

退出码为 0 即算投递成功。

### 8.4 安全：只能改配置文件

钩子等于在 server 主机上以 serve 进程的用户身份执行命令，所以：

- **只能在配置文件里改**。web 配置页只读展示（env 只显示键名），`PUT /v1/config/server` 与 `POST /v1/config/validate` 对 `event_hooks` 一律返回 `field not editable`。否则在没开 `governance.require_admin_capability` 时，任何已认证调用方都能远程执行命令。
- 信任模型与 git hooks 相同：能改 server 配置文件的人写的命令视为可信。
- `env` 里可以放密钥，它的值不会出现在任何 API 读视图里；但不要让脚本把密钥打到 stderr——虽然会脱敏，也只是尽力而为。

决策 hook（在提交 / 交付前同步裁决、输出会回到 gofer）见 [decision-hooks.md](decision-hooks.md)。

## 9. 为什么不是 QQ

QQ 官方机器人单聊的主动推送额度是 30 天内 4 条（当天 1、1-3 天 1、3-7 天 1、7-30 天 1），做不了每次会话停下都提醒。非官方 OneBot 实现驱动真实账号，有封号风险，不采用。

## 10. 桌面通知（server 所在主机弹系统通知）

`server.notification.desktop` 是第三条通知通道：事件发生时，**server 在它自己所在的那台机器的桌面上弹一条系统通知**。它专门解决「审批 / 等回复的 job 在浏览器里没被注意到」这个问题——不需要额外的托盘进程，不需要 HTTPS（Web Push 的要求），也不用手机上装 IM。

```yaml
server:
  web_base_url: https://gofer.example.com   # 点击通知打开的页面前缀；留空则不带链接
  notification:
    desktop:
      enabled: true
      # 省略 events = 默认这四个「等你现在处理」的事件
      events: [job.awaiting_approval, interaction.created, session.awaiting_reply, job.needs_review]
      projects: [demo]          # 省略 = 全部项目
      merge_sec: 10             # 同类事件合并窗口（默认 10s；0 = 不合并）
```

整段不写 = 不启用（升级不会自己开始弹窗）。标题和正文复用 IM 通道的消息渲染：待批 job 是「待批准：<标题>」加理由、要执行的命令、过期时间；`job.needs_review` 是「job 等待验收：<标题>」加项目 / agent；会话等回复带轮次与回复预览。点击打开 `web_base_url` 对应页面。

**合并**：`merge_sec` 窗口内同一类事件合成一条（「3 个 job 等待批准」，正文列各自标题），不同类型各弹一条。一次 plan 交出的多个 item 同时等批时不会刷屏。

各平台怎么弹：

| 平台 | 实现 | 点击 | 跳过条件 |
|---|---|---|---|
| Windows | PowerShell 调 `Windows.UI.Notifications` 发 ToastGeneric toast | 有（`activationType="protocol"`，交给默认浏览器） | 进程在 **session 0**（以服务 / 「不论用户是否登录」的计划任务运行）；找不到 `powershell.exe` |
| macOS | `osascript -e 'display notification …'` | 无（链接写在正文里） | 找不到 `osascript`（macOS 自带）；首次需在系统设置里允许通知 |
| Linux | `notify-send --app-name=gofer` | 无（链接写在正文里） | 没装 `notify-send`（libnotify）；`DISPLAY` / `WAYLAND_DISPLAY` / `DBUS_SESSION_BUS_ADDRESS` 全空（容器、无头机） |

Windows 用 PowerShell 而不是 Go 直接调 WinRT 的原因：WinRT toast 要求 AUMID **已在开始菜单注册**，否则 `CreateToastNotifier` 报 element not found；gofer 借用 Windows PowerShell 自带的 AUMID（Windows 10/11 必有），于是既不用装启动器快捷方式、也不留常驻进程。事件数据（标题 / 正文 / 链接）不拼进脚本——脚本是固定程序，XML 经环境变量传入，避免任务标题里的引号变成命令。

检测到不能弹时不是每来一个事件就报一次：每种原因只记一条 `notify.desktop_skipped`（JSONL 日志里带 `reason`），其余照常推 webhook / IM。

排障：

```powershell
# 日志里看是否有跳过（JSONL 日志默认 <config-dir>\run\serve.log）
Select-String -Path <config-dir>\run\serve.log -Pattern 'notify\.desktop'
```

- **一条都没有、也不弹**：确认 `desktop:` 段写在 `server.notification` 下（不是 `server:` 下），并且 `server.notification.enabled` 与 `server.notification.desktop.enabled` 都不是 `false`；项目的 `notify_enabled: false` 也会压掉桌面通知。
- **Windows 上记 `windows session 0`**：现在的 server 跑成了服务而不是计划任务。`gofer serve install` 建的计划任务（登录时触发、以用户身份、带交互令牌）跑在用户桌面会话里，可以弹；服务方式运行则不行，改用计划任务或换一台有桌面会话的机器。
- **Windows 上 toast 不出现也不报错**：检查「设置 → 系统 → 通知」里 PowerShell 的通知是否被关掉（gofer 借用它的 AUMID）。
