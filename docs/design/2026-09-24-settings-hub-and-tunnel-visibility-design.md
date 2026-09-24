<!-- template_id: design; template_version: 1.1.1 -->
# 设置页二级菜单、隧道在 web 可见与 v0.60.2 小项设计（WEB-12 / TUN-03 / TUN-04 / ACP-02）

> 状态：Approved 0.1 / 实施中（2026-09-24 用户直接指定 v0.60.2 范围：设置页改二级菜单、tun 转发在 web 可见、tun 接受逗号分隔、ACP-02 真机验收；另记 web 工作台构想进 roadmap）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-24 | Claude | 初稿（按用户指定范围直接实施）：WEB-12 `/settings` 左侧二级菜单（配置管理 / Tunnels / 关于）；TUN-03 转发进程向 hub 登记 + 预设存到 server、web 列表与编辑；TUN-04 spec 接受逗号；ACP-02 由我在主机做端到端验收 |

## 背景

- web 顶栏的「⚙ 设置」（`web/src/App.vue:40 settingsNav = { to: '/config' }`）直接进入 `Config.vue`——一整页配置信息，没有容纳其它设置的位置。用户希望进入后是**左侧二级菜单**，「配置管理」是其中一项，后续把 tunnel 配置等放进来。
- 隧道：`gofer tun forward` 在**客户端机器**上起本地监听，每条连接经 hub 转到 worker。hub 现在只知道"连接"（`GET /v1/tunnels`，`internal/httpapi/tunnel_handler.go:189` 的 `id/caller_id/worker_id/target/client_remote/started_at/bytes_up/bytes_down`），**不知道有哪些转发进程在监听**——没有连接时 web 上什么都看不到。预设（`tun save`）存在**执行命令那台机器**的 `<config-dir>/tunnels.yaml`（`internal/config/tunnels.go`），server 与 web 都看不到，换一台机器就得重存一遍。
- `tun save/forward` 的多条规则是空格分隔的多个参数；用户按直觉写成逗号分隔（`udp/21845:…,1502:…,…`）→ `invalid local port "1217,11740"`。不是回归，但逗号是自然写法。
- ACP-02：`acp-agent` 类型自 v0.44 起有，主机上登记了 `claude-acp`、`omp-acp`、`jcode-acp`（未登记 `codex-acp`）；roadmap 里 ACP-02 一直是"等条件"。本期做一次真机端到端。

## 一、WEB-12 设置页二级菜单

```
┌ ⚙ 设置 ───────────────────────────────────────────────┐
│ ┌──────────┐ ┌──────────────────────────────────────┐ │
│ │ 配置管理  │ │  （原 Config.vue 内容：server / agents  │ │
│ │ Tunnels  │ │    / projects / runners / roles … 编辑）│ │
│ │ 关于      │ │                                        │ │
│ └──────────┘ └──────────────────────────────────────┘ │
└──────────────────────────────────────────────────────┘
```

- 新布局组件 `views/settings/SettingsLayout.vue`（左侧二级菜单 + `<router-view>`），子路由：`/settings/config`（原 Config.vue 原样迁入）、`/settings/tunnels`（下节）、`/settings/about`（版本、构建、server 地址、协议版本，从 `/v1/stats` 取）。
- `/config` → 重定向 `/settings/config`（旧链接不失效）；顶栏「⚙ 设置」指向 `/settings`（默认进配置管理）。
- 菜单项是一个数组常量，后续加设置页只加一行。窄屏时二级菜单折叠为顶部横向 tab。

## 二、TUN-03 隧道在 web 可见

### 1. 转发进程登记（forwarders）

- `gofer tun forward` 启动后向 hub 登记：`POST /v1/tunnels/forwarders {id, worker, specs:[{network,bind,local_port,target}], host, pid, started_at}`，之后每 30s 心跳（`PUT …/{id}`），退出时 `DELETE`；hub 内存保存，**90s 无心跳即过期**（进程被杀也会消失）。id 用 `fw-<8hex>`。
- `GET /v1/tunnels/forwarders` 列出在线转发进程，并把 `GET /v1/tunnels` 的活跃连接按 `(caller, worker, target)` 归到对应转发下（附连接数与累计字节）。
- 权限：user caller 可读全部、可写自己的；job caller 只读（SEC-01 默认拒绝写）。
- CLI：`gofer tun ls` 增加"转发进程"一段（原来只列连接）。

### 2. 预设存到 server（presets）

- server 新增预设存储（库表 `tunnel_presets {name PK, worker, specs_json, note, updated_at, updated_by}`），`GET/PUT/DELETE /v1/tunnels/presets[/{name}]`；PUT 走现有 `ValidateTunnelProfile`（`internal/config/tunnels.go:101` 一带）。
- CLI：`tun save`、`tun saved`、`tun forward -n` **优先用 server 预设**；server 上找不到同名预设时回落本地 `tunnels.yaml` 并提示 `gofer tun presets push` 可以上传。本地文件读取路径打 `// DEPRECATED(v0.60.2): remove in v0.63`（G032）。
- `gofer tun presets push [--force]`：把本地 `tunnels.yaml` 里的预设上传到 server（同名冲突默认跳过并列出）。

### 3. web：`/settings/tunnels`

- 上半「在线转发」：表格（worker、规则列表 `udp/21845 → 192.168.0.253:21845`、主机/pid、开始时间、活跃连接数、↑↓字节），5s 轮询（可见性暂停，复用 `utils/poller.ts`）。
- 下半「预设」：表格 + 新增/编辑/删除（名称、worker 下拉、规则多行文本框——**一行一条或逗号分隔都接受**、备注），每行给一个"复制启动命令"按钮（`gofer tun forward -n <name>`）。
- web 不负责启动转发（转发是客户端本地监听，必须在要用端口的那台机器上跑），这点在页面上用一行说明写清。

## 三、TUN-04 spec 接受逗号分隔

- `tun forward` / `tun save` 的每个 spec 参数先按 `,` 拆分、去空白、丢空项，再逐条 `ParseForwardSpec`（`internal/tunnel/spec.go:19`）；空格与逗号可以混用。IPv6 目标用 `[::1]:port`，里面没有逗号，不冲突。
- 错误信息带上出错的那一条（`spec #3 "11217:127.0.0.1:1217": …`）。
- 预设存储里仍存**拆开后**的规则数组。

## 四、ACP-02 真机验收（我在主机做，不派 agent）

范围：主机已登记的 `claude-acp`、`omp-acp`、`jcode-acp`；`codex-acp` 若有内置模板则经 web 配置页（WEB-04③）登记后一起测。每个 agent：

1. 只读小任务（`--read-only` → `set_mode`）：能跑完、stdout 为 assistant 文本、`acp.jsonl` 有记录、`job.acp_summary` 有 usage。
2. 需要审批的写操作（项目 `approval: ask`）：出现 permission 交互卡 → web/CLI 作答 → 继续；自动审批不进时间线（v0.51 行为）。
3. `job resume`：`session/load` 续上下文（问"上一轮我让你做了什么"）。
4. job 凭证：acp 子进程环境无 server token（SEC-01 的 acp 路径真机复核）。

发现问题 → 记 bd、按需派修复 job；结论写进 ACP 设计文档的「ACP-02 真机验收」一节，roadmap ACP-02 状态随之更新。

## 五、roadmap：WEB-11 web 工作台（构想，未设计）

用户 2026-09-24："基于现有功能做 web 工作台页面，方便我远程**开始**工作——现在以终端为主，web 是被动处理任务；初步想法类似现在的 agent 桌面版。" 只记入 roadmap「待做」，**不在本批实施**。可复用的积木（写进 roadmap 条目的"细节"栏，供后续设计）：交互 pty job + web attach（WEB-03/PTY-01）、ACP 会话（ACP-01，结构化消息流比 pty 更适合做对话面板）、会话中继（SESS-01/02）、评论与 @派活（MCP-05）、skills（JOB-10）、plan 链与看板（PLAN-03/WEB-10）、验收台（REV-01）、文件传输（XFER-01）。

## 横切

- 协议：不涉及 worker wire（forwarder 登记走 HTTP，客户端到 hub）。
- G032：本地 `tunnels.yaml` 读取打 DEPRECATED(v0.60.2)→v0.63；`/config` 路由保留为重定向（不算兼容层，旧书签可用）。
- 安全：forwarder 登记只影响展示，不授予任何转发能力（真正的连接仍走现有 `/v1/tunnels/connect` 的鉴权与 worker 策略）。

## 实施分期（omp，测试先写先提交）

| 期 | 内容 | 验收 |
|---|---|---|
| **T1** | TUN-04 逗号；TUN-03 forwarder 登记/心跳/过期 + 列表归并；server 预设表与 CRUD；CLI 预设优先 server + `presets push` + `tun ls` 转发段 | `TestSpecsAcceptCommaAndSpace`、`TestSpecErrorNamesTheEntry`、`TestForwarderRegisterHeartbeatExpire`、`TestForwarderListGroupsConnections`、`TestPresetCRUDValidates`、`TestCLIPresetPrefersServerFallsBackLocal`、`TestPresetsPushSkipsConflicts`、`TestJobCallerCannotWriteForwarders` |
| **T2** | WEB-12 设置页布局 + `/config` 重定向 + 关于页；`/settings/tunnels` 两个表格与预设编辑 | `pnpm typecheck && pnpm build`；临时 server + 浏览器目视：二级菜单、重定向、在线转发（起一个本地 `tun forward` 到临时 worker 或假 worker）、预设增删改 |
| **ACP-02** | 我在主机做（与 T1/T2 并行） | 见 §四 |
