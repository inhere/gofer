<!-- template_id: design; template_version: 1.1.1 -->
# 会话体验打磨、HTTPS 入口与常驻传话人（P 批）

> 状态：Approved（Draft 0.1；用户 2026-10-02 在 web 中继确认 P1–P4 方案，并决定先用自签证书试 PWA；手机为 Android）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-02 | Claude | 用户反馈 5 项 + 转达消息进对话流；HTTPS 入口；常驻传话人（含实测） |

## 已确认事实（2026-10-02）

- `SessionDrawer.vue`：「最后一条消息」固定在信息区与时间线之间（v0.89 S8），手机上占用空间；会话停在回合末尾等回复时，它与中继轮次里那条消息内容相同，重复显示。
- 经传话人 / 中继发出的 web 消息存在 `<storage.root>/sessions/<sid>.outbox.jsonl`，抽屉里单独显示为「Web 消息」区块（`SessionDrawer.vue:722`），不在对话流里。
- 工作台 `ThreadView.vue:394`：agent 会话（relay 线程）的输入框在非等待状态下禁用；Sessions 抽屉在同样情况下会走传话人。
- 传话人实测：ACP 持续会话（claude-code-acp）**没有** SendMessage 工具，不能当传话人；常驻 `claude -p --input-format stream-json --output-format stream-json --allowedTools SendMessage,ListAgents` 进程可连续转发多条（一次冷启动，两条共 6.6s）；同时写入的多条会被合并成一轮，需逐条写入并等待 `result` 事件。
- gofer server 不支持 TLS；PWA 已有（`site.webmanifest` 的 `display: standalone`），但安装为独立窗口要求安全上下文（HTTPS）。Android 上 Chrome 信任用户安装的根证书。

## 方案

### P1 「最后一条消息」挪到底部并默认折叠
放在对话区底部、输入框上方；默认一行「最后一条消息 · 展开 · 历史消息」（桌面可多显示几个字的预览），展开后显示全文，「复制」仅展开后出现。从列表「查看全文」进入时默认展开。

### P2 去重
最后一条消息与最新一轮中继（等待回复）消息内容相同（规范化空白后比较）时，不显示该区块；只有被放行、未开中继轮的情况才显示。

### P3 工作台 agent 会话
- 非等待状态也能输入：走传话人转达（与 Sessions 抽屉同一接口），输入框下提示"经转达，对方不会当作你的审批"；等待状态仍走中继回复。
- 打开线程时滚动到最新一轮；新消息到达时若用户已在底部则跟随。

### P4 转达消息进入对话流
outbox 中的消息按时间穿插进对话流，显示为"你（经转达）"气泡，带状态（排队中 / 已送达·通道 / 失败·原因·重试）；移除单独的「Web 消息」区块。工作台与 Sessions 抽屉一致。

### P5 常驻传话人（同 runner 复用）
- 每个 runner 按需拉起一个常驻传话进程（`messenger_command -p --input-format stream-json --output-format stream-json --allowedTools SendMessage,ListAgents`，剔除继承的 `CLAUDE*` 会话标记），由该 runner 上的 gofer（server 本机或 worker）管理。
- 消息串行：写入一条 user 消息 → 等到对应 `result` 事件 → 判定送达 / 失败 → 再写下一条；单条超时（默认 90s）判失败并重启进程。
- 空闲 10 分钟（`server.session_messaging.messenger_idle_sec`）自动退出；进程异常退出或启动失败时退回现有一次性传话 job。
- 可观测：传话进程状态在 Runners / worker show 中可见；每条消息的结果仍写 outbox。
- 远程 runner：经 worker 协议下发（需要新增帧时按可选能力门槛处理，旧 worker 退回一次性 job）；若改动过大，本期先做 server 本机 runner，远程沿用一次性 job，并在汇报中说明。

### P6 HTTPS 入口（自签证书试 PWA）
- 配置 `server.tls: { addr, cert_file, key_file }`：在现有 http 监听之外**另开**一个 HTTPS 监听（同一套路由与鉴权），http 监听保持不变（worker / CLI 不受影响）。未配置则不启用。
- `gofer tool cert`（G033 tool 组）：生成本地根 CA 与服务器证书（SAN 含指定的 IP / 主机名，默认本机主机名与非回环 IPv4），输出 CA 证书（`.crt`，供手机安装）与服务器证书 / 私钥，私钥文件权限收紧；已有 CA 时复用只签新服务器证书。
- 文档：Android 安装根证书（设置 → 安全 → 加密与凭据 → 安装证书 → CA 证书）、用 `https://<IP>:<port>` 打开并「安装应用」的步骤；PWA 推送在 HTTPS 下可用。
- 证书与私钥不入仓库、不写进日志。

## 不做
- 原生外壳 App（Capacitor）与内置 OpenVPN：先看 PWA 效果再定。
- worker 侧 wss 连接（http 监听保留，worker 继续走 ws）。

## 测试与验收
每项先写失败测试（Go / Vitest），按功能点提交。监督者在容器内真实复验：抽屉与工作台在 390px 下的布局与折叠；等待回复时不重复；工作台非等待状态经转达送达且出现在对话流；同一 runner 连续两条转达只起一个传话进程、逐条有结果；HTTPS 监听用 `gofer tool cert` 生成的证书启动，浏览器信任 CA 后可访问且 manifest / service worker 生效。
