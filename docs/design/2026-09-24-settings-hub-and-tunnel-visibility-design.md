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

## T1 实测记录（2026-09-24）

实施提交：`test(tun-03,tun-04)` red 提交（三个测试文件，先写先提交，此时不编译）→ `feat(tun-03,tun-04)` 实现提交 → `docs(tun-03)` 文档提交，详见 `git log`。

### 1. 测试（要求名）

`go test ./internal/tunnel/ ./internal/httpapi/ ./internal/commands/ ./internal/config/ ./internal/jobstore/ -count=1` 五个包全 `ok`。要求名的逐条结果（`-v`）：

| 测试 | 结果 | 备注 |
|---|---|---|
| `internal/tunnel` `TestSpecsAcceptCommaAndSpace` | PASS | 含 `[::1]` 不被误拆、空项丢弃、全空报错 |
| `internal/tunnel` `TestSpecErrorNamesTheEntry` | PASS | 断言含 `spec #3` 与 `"not-a-spec"` |
| `internal/httpapi` `TestForwarderRegisterHeartbeatExpire` | PASS | 注册→可见→心跳跨过原 TTL 仍存活→静默过期→DELETE 立即消失→未知 id 404→非法登记 400 |
| `internal/httpapi` `TestForwarderListGroupsConnections` | PASS | 两条同 (caller,worker,target) 连接 → `connections=2`、字节 400/700；异 worker 不计入 |
| `internal/httpapi` `TestPresetCRUDValidates` | PASS | 非法规格 400 且不落库；逗号列表入库即拆分；无 force 409；DELETE 后 404 |
| `internal/httpapi` `TestJobCallerCannotWriteForwarders` | PASS | job 凭证 GET forwarders/presets 200，四种写 403 |
| `internal/commands` `TestCLIPresetPrefersServerFallsBackLocal` | PASS | server 同名优先；仅本地有时回落并给出 push 提示；server 不可达同样回落；两边都没有则报错 |
| `internal/commands` `TestPresetsPushSkipsConflicts` | PASS | 冲突跳过并列出、不覆盖 server 值；`--force` 覆盖且不跳过 |
| `internal/commands` `TestTunLsShowsForwarders` | PASS | FORWARDERS 段（规则渲染、host/pid、连接数）+ CONNECTIONS 段 |

`gofmt -l`（本次改过的 go 文件）为空；`go build ./...`、`go vet ./...` 干净。

### 2. 临时 server smoke（临时 config + 私有 `GOFER_CONFIG_DIR` + 127.0.0.1:18765）

启动：`go build -o tmp/tun03-smoke/gofer.exe ./cmd/gofer`，`gofer serve -c tmp/tun03-smoke/smoke-config.yaml`（`server.addr: 127.0.0.1:18765`、`allow_empty_token: true`、storage/project 都在 tmp 目录）。**每条 CLI 命令都显式带 `-c <临时配置>` 与 `--server http://127.0.0.1:18765`，并在同一条命令里 unset `GOFER_SERVER_ADDR/GOFER_SERVER_TOKEN/GOFER_TOKEN`**；真实配置目录 `D:\work\inhere\config\win-env\gofer` 与正在跑本 job 的 server 全程未被访问。

```text
$ gofer tun save demo -w w-smoke "1502:127.0.0.1:502,11217:127.0.0.1:1217"
saved preset demo on the server (w-smoke: 1502:127.0.0.1:502 11217:127.0.0.1:1217)   # 逗号形式被拆成 2 条
$ gofer tun saved
NAME WORKER SPECS NOTE
demo w-smoke 1502:127.0.0.1:502,11217:127.0.0.1:1217
$ curl -s http://127.0.0.1:18765/v1/tunnels/presets
{"presets":[{"name":"demo","worker":"w-smoke","specs":["1502:127.0.0.1:502","11217:127.0.0.1:1217"],"note":"","updated_at":"2026-09-24T15:44:21+08:00","updated_by":""}]}
$ gofer tun forward -n demo            # 监听 1502/11217，worker 离线只影响拨号
registered as fw-a673e3c2
$ curl -s http://127.0.0.1:18765/v1/tunnels/forwarders
{"forwarders":[{"id":"fw-a673e3c2","caller_id":"","worker":"w-smoke","specs":[{"network":"tcp","bind":"127.0.0.1","local_port":1502,"target":"127.0.0.1:502"},{"network":"tcp","bind":"127.0.0.1","local_port":11217,"target":"127.0.0.1:1217"}],"host":"PC-20260513EDRL","pid":46756,"started_at":"...","last_seen_at":"...","connections":0,"bytes_up":0,"bytes_down":0}]}
$ gofer tun ls
FORWARDERS
ID WORKER RULES HOST PID AGE CONNS UP DOWN
fw-a673e3c2 w-smoke 1502 -> 127.0.0.1:502;11217 -> 127.0.0.1:1217 PC-20260513EDRL 46756 4s 0 0 0
CONNECTIONS
no active tunnels
# Ctrl+C 后（进程 exit code 0）
$ curl -s http://127.0.0.1:18765/v1/tunnels/forwarders
{"forwarders":[]}
```

临时配置目录里没有生成 `tunnels.yaml`——`tun save` 只写 server，与"server 为真源"一致。

### 3. 实施中的偏差与既有缺口

- **G032**：本地 `tunnels.yaml` 的读取路径打 `// DEPRECATED(v0.60.2): remove in v0.63`（`config.LoadTunnels`、`config.UserTunnelsPath`）；未新增任何无标记兼容分支。`tun save` 的行为变化记录在此：server 可达时只写 server（连不上才本地兜底并提示），这是设计 §二.2 的直接结果。
- **既有缺口（本次顺手补齐，非新兼容层）**：`tun save` 之前**没有绑定 `-s/--server`**（只有 forward/check/ls 有），客户端节点只能用 `GOFER_SERVER_ADDR`；TUN-03 起 `save/saved/forget/presets push` 都绑定了，否则 `tun save --server ...` 是 usage error。
- **归并语义（设计未细说，实现取"诚实重复计数"）**：同 `(caller, worker, target)` 的两个转发进程会各自显示同一组连接数——hub 无法知道某条连接来自哪个本地监听端口，编造归属比重复计数更糟；字节**总额**仍然正确（每个进程对同一连接各算一次，不存在重复累加到同一行的情况）。
- **`tun ls` 段落标题**用 `FORWARDERS` / `CONNECTIONS`（与本文件既有英文表头一致），语义即设计里的「转发进程」/「活跃连接」两段。
- **心跳容错**：PUT 心跳的空 body 视为"仅续期"（不 400）；收到 404 时 `tun forward` 会自动重新登记并打印，避免 hub 重启后条目永久消失。
- **TTL 热改**：`server.tunnel.forwarder_ttl_sec` 进 R3 字段策略表（`server.tunnel`，Editable、无需重启），registry 每次读/写都经 `Config.EffectiveForwarderTTL` 取当前值；`PUT /config/server` 的 `applyServerField` 增加 `tunnel` 分支（负值 400）。
- **jobstore**：新表 `tunnel_presets` 走 `IF NOT EXISTS`（新表无需 migrate ALTER），并加入 `/v1/stats` 的 db 表清单；删除不存在的名字返回哨兵 `jobstore.ErrTunnelPresetNotFound`，HTTP 层据此 404（真实存储错误仍是 500）。
