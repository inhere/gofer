# 安全须知

gofer 的安全模型一句话：**持有一个 token，就等于能在 server 上执行命令**。它不是沙箱，不是多租户平台，也没有“租户隔离”这回事。这一页讲清楚这意味着什么，以及哪些旋钮能把它收紧。

## token 即权限

- server 必须配 token 才启动（`server.token_env` 指向环境变量，推荐；或 `server.token` 直接写，不推荐）——除非显式 `--allow-empty-token`，那是给本机试验用的。
- 任何拿到 token 的人，都可以通过 CLI / HTTP / MCP 提交 job；exec job 就是在执行机上的一个进程，**以 gofer 进程的权限运行**。所以 token 只能给信任的人或受控的 CI。
- 反过来：不给 token 就没有入口。删掉/换掉 token 并 `gofer serve reload` 即吊销（用 `callers[]` 登记过的调用方同样按条目删除 + reload 吊销）。
- 查看自己是谁、持有什么：`gofer auth whoami`；看某条路由要什么权限：`gofer auth routes`。

## 监听地址与内网

- 默认监听 `0.0.0.0:8765`。这是有意为之：容器要经 `host.docker.internal:8765` 连进来。它依赖 token + 你的网络边界，**不依赖**监听地址本身的安全。
- 纯本机使用就收紧：配置里写 `server.addr: 127.0.0.1:8765`。要让其他机器（worker、手机、同事）连，才放开到内网地址。
- 不要把 server 直接暴露在公网。需要远程访问时：走内网 / VPN / 隧道，或者用反向代理加 TLS 并只放行可信来源。
- 反向代理要注意：放行 WebSocket（`Upgrade` 头）、关掉响应缓冲、空闲超时大于 60 秒，否则控制台会掉线、日志流会断。
- 想用 HTTPS（手机装 PWA、Web Push、跨网访问）：`server.tls` 配证书，`gofer tool cert --hosts <server-ip>` 生成自签 CA 与证书，步骤见 [`docs/runbook/https-pwa.md`](../runbook/https-pwa.md)。

## 多个调用方与权限

只给一个 token 时，所有人都是同一个身份（记作 `default`）。要区分人、区分权限，用 `server.callers[]`：

```yaml
server:
  callers:
    - id: ops
      name: Ops                        # 显示名，只影响显示
      token_env: GOFER_CALLER_OPS_TOKEN
      max_concurrent_jobs: 4           # 同时在跑的 job 上限（超出排队）
      rate_limit: 5                    # 每秒提交数
    - id: ci-bot
      token_env: GOFER_TOKEN_CI
      ceiling:                         # 作用范围上限：只减不加
        projects: ["app-*"]
        runners: ["w-linux-*"]
        agents: [codex, exec]
        allow_exec: true
```

- **上限（`ceiling`）** 是这个调用方“最多能到哪”：项目、runner、worker、agent、能否 exec、能否写全局记忆、是否只读。实际范围 = 已有权限 ∩ 上限，两者同时成立才放行。没写 `ceiling` 的调用方行为不变。
- 写 `ceiling` 时，**本机 runner（`local` / `server`）与 exec 默认禁止**，要放开必须显式写 `runners: [local]` / `allow_exec: true`；这两种放开后，上限就不再构成安全边界（本机 runner 等于主机 shell），启动日志会打印警告。
- 权限分两层：**上限**决定范围内有什么，**权限**决定能做什么。细分权限（读别人的 job、验收、改配置、管理 worker……）登记在 [`docs/reference/permissions.md`](../reference/permissions.md)；根管理员可以在 Web「设置 → 权限」给会话发临时授权（1 / 4 / 24 小时，可续期），**谁能在那个终端里跑命令，谁就拿到这些权限**。
- 超范围访问一律按“不存在”处理（404，连“存在但没权限”都不告诉你）。

## 会话授权与调用方上限为什么存在

两个容易混的东西：

- **会话授权（grant）**：临时把某几个权限借给一个终端会话（例如“这半小时允许这个 Claude 会话推分支”），到期或会话结束即失效，根管理员随时可撤销。用途：不必给一个长跑的 agent 永久权限。
- **调用方上限（ceiling）**：把某个 token 永久限制在某个范围里（例如给同事的 token 只能碰两个项目、只能用 codex、不能跑 exec）。用途：把“临时来的一个人”挡在核心项目之外。

补一个常被问到的问题：**先写上限，再放开本机 runner，就等于没设上限**。要真正的边界，是“只给 worker runner + 不给 exec + 限定项目”，因为那些项目目录本身对这个人也不可见。

## metrics 与健康检查端点

- `/health` 与 Prometheus `/metrics` **不在** `/v1` 鉴权组里。`/metrics` 默认开启且免认证（抓取器很少带 Bearer token），靠内网访问控制保护。
- 不想要：配置 `server.metrics.enabled: false`。想加一层认证：`server.metrics.token` 设一个 token，抓取时带 `Authorization: Bearer <token>`。
- 这两个端点会暴露 job 数量、耗时、队列、调用方等运行指标，不涉及 job 正文。

## 秘密：不要进日志、不要进 job 请求

- **`--env K=V` 的值会存进 job 请求（`request_json`），能看到 job 详情的人都能看到**。不要用它传密钥。
- 需要密钥的场景用 `secrets`：配置里只写“从本机 gofer 进程的哪个环境变量取值”，job 里按名字使用。

  ```yaml
  secrets:
    gh-token:
      env: GITHUB_TOKEN        # 值放在 server 的环境 / <config-dir>/.env 里
      as: GH_TOKEN             # job 里的变量名
  projects:
    <project>:
      secrets: [gh-token]      # 这个项目允许用哪些
  ```

  ```bash
  gofer job run -p <project> -a exec --secret gh-token --sync -- ./scripts/push.sh
  ```

  值不进配置、不进 job 请求；job 输出写盘时会被替换成 `***`。目前只支持 server 本机 runner（worker 上的密钥还没做）。
- job 的执行环境会**去掉** server / worker 的 token，只留一个作用域受限、终态即吊销的 job 凭证——所以 agent 在自己的 job 里跑 `gofer` 命令不会拿到你的管理员权限。
- 提交时如果 prompt 或命令看起来含密钥形态，会给出警告（`--no-secret-check` 关掉，慎用）。
- 已经漏了：`gofer job secret-scan --literal-from-stdin` 跨 job 找出来，`gofer job redact <id> …` 抹掉正文与日志里的秘密（需要相应权限），`job secret-scan --redact --yes` 连命中的信件一起处理。信箱里的秘密用 `gofer session mail redact <id>`。
- 日志（`<config-dir>/run/*.log`）是 JSONL 且对敏感键脱敏，轮转保留；仍然不要把密钥写进命令参数或 prompt。
- **截图也是数据**：工作台发给会话的图片在 server 上保留（默认 14 天，`storage.attachment_retention_days`），执行机项目目录的 `tmp/gofer-attachments/` 里保留 7 天，能看会话历史的人都能看到缩略图。终端截图常带 token、内网地址，发之前自己看一眼。

## 执行侧的其他边界

- job 的工作目录被限制在项目目录内（越界路径直接拒绝），argv 从不经过 shell。
- exec 类 job 需要项目显式 `allow_exec: true`；交互 pty job 需要 `allow_interactive: true`。
- 远程 job 的授权留在执行方：worker 收到派发后会用**自己的**配置再校验一次（项目 / agent 白名单、exec 门禁、目录不越界）。server 逼不了 worker 跑它没放行的事。
- `--hold` 让不可逆操作（`git push`、发布、对外发消息）在执行前停下等人批准；**agent 不能批准自己提交的待批 job**。
- 事件钩子（`server.event_hooks`）与决策 hook（`server.decision_hooks`）等同于在服务器上执行命令，只能在配置文件里改，写接口一律拒绝。

## 一页检查清单

- [ ] token 来自环境变量（`token_env`），不在仓库、不在配置文件里
- [ ] 只在需要时监听非环回地址；公网访问走 VPN / 反代 + TLS
- [ ] 给不同的人 / CI 建 `callers[]`，该限的写 `ceiling`
- [ ] 关键项目不开 `allow_exec`，或只对特定调用方开
- [ ] `/metrics` 关掉或加 token
- [ ] 密钥走 `secrets` + `--secret`，不进 `--env` / prompt / 命令参数
- [ ] 不可逆操作走 `--hold`，`git push` 仍需要人的明确授权
- [ ] 定期 `gofer job secret-scan` 自查有没有漏出去的东西
