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

## 3. 安全模式二选一

添加机器人时必须选一种，两种 gofer 都支持：

| 模式 | 配置 | 说明 |
|---|---|---|
| 自定义关键词 | 不填 `secret_env` | 最省事。关键词填 `gofer` 或 `会话`，消息标题里都带得有 |
| 加签 | 填 `secret_env` | gofer 按 kind 自动算：钉钉签名进 URL 参数，飞书进请求体 |

IP 白名单模式也可以用，gofer 不需要额外配置，但主机出口 IP 变了就会失效。

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
        secret_env: GOFER_DINGTALK_SECRET   # 加签模式才要；关键词模式删掉这行
        events: [session.waiting]
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

| 事件 | 何时触发 |
|---|---|
| `session.waiting` | 中继会话停下来等人回复（手机提醒的主力） |
| `job.terminal` | job 进入终态 |
| `interaction.created` | 产生新的待人工交互 |

**注意**：省略 `events` 时用的是默认集（`job.terminal` + `interaction.created`），**不含** `session.waiting`。想收会话提醒必须显式写进 `events`，这样既有配置不会平白多出流量。

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
| 机器人回 `keywords not in content` | 关键词模式下关键词没出现在消息里。把关键词设成 `gofer` 或 `会话`，或改用加签 |
| 机器人回 `sign not match` | 加签密钥不对，或主机时钟偏差过大（两家都要求时间戳在一小时内） |
| 消息里没有链接 | `server.web_base_url` 没配 |
| 投递一直重试 | `gofer job` 侧的投递队列会退避重试 30s→2m→5m→15m→60m，到 `max_attempts` 后置 failed；失败原因记在投递记录的 last_error |
| 想临时静音某项目 | 该项目配 `notify_enabled: false` |

投递记录可以在 web 的 job 详情页看到；会话通知不挂在 job 上，排障时直接看 serve 日志里的 `NotifyEvent` 警告。

## 8. 为什么不是 QQ

QQ 官方机器人单聊的主动推送额度是 30 天内 4 条（当天 1、1-3 天 1、3-7 天 1、7-30 天 1），做不了每次会话停下都提醒。非官方 OneBot 实现驱动真实账号，有封号风险，不采用。
