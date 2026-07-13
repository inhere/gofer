# worker 配置远程化设计（worker=能力提供方，server=策略权威）

> bd: `tools-5pq`（epic）。前置：`docs/design/2026-07-09-config-federation-design.md`（xu64.10，解决了反方向的重复定义）。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-07-13 | Claude | 初版：问题定义 + 权威模型 + 分阶段。待审 |
| v0.2 | 2026-07-13 | Claude | 定稿 Q1(前缀最长匹配) / Q2(自动接受) / Q4(保留逃生舱, Policy 优先)；新增 D3：策略走**下发**而非远程改写 worker 配置文件（附理由）。 |
| v0.3 | 2026-07-13 | Claude | **推翻 Q4**：worker 端零 project 配置（逃生舱与 D3「单一真源」自相矛盾，且 worker-only project 本就是「配置写两遍」的绕行方案，本设计从根上解决后它变成分裂的来源）。新增 D4：**放置由 roots 推导**——server 全量推目录、worker 用 roots 最长前缀映射自筛，取代 v0.2 里「按 allowed_runners 算该推给谁」（labels 型 runner 下 server 根本算不出）。 |

## 1. 概览

配好一个 worker 节点后，**新增 project、开启 pty agent 都必须登录那台机器改 `worker.yaml` 并重启进程**——控制面完全够不着。实际后果：配了多台 worker，却只有 server 本机的一两个 project 能用起来，其余 worker「看得见、用不了」。

本设计把 worker 从「既管能力又管策略」收敛为**只管能力**，策略上收到 server（server 已有 SIGHUP 热重载与 `project add` CLI）。目标状态：**新增 project 到某 worker、允许它用 tty agent，全程只改 server，不登录 worker、不重启 worker 进程。**

## 2. 名词

- **能力（capability）**：这台机器上客观存在的东西——哪些目录可执行、装了哪些 agent 二进制、并发上限。只有 worker 知道。
- **策略（policy）**：允许谁用什么——哪个 project 跑在哪台 worker、可用哪些 agent、能否 exec / 开 pty。集中、高频变更。
- **护栏（guard）**：worker 本地设定、**server 无法远程放宽**的硬约束（路径根、allow_exec、allow_interactive）。
- **内置 agent 模板**：代码里预置的已知 agent 定义（command + argv + detect + session 默认），worker 只探测「装没装」，不再手写定义。

## 3. 问题分析（代码事实）

1. **worker 无热重载**：`internal/worker/serve.go` 只注册 SIGINT/SIGTERM 停机；server 侧有 `startReloadLoop`（SIGHUP → `core.Reload`，`internal/serve/serve.go:846`）。worker 任何配置改动 = 上机器改文件 + 重启进程。
2. **project 必须在 worker 上重复定义**：派发后 worker 用**自己的 config** 重新 `Submit`（`internal/worker/dispatch.go:46`，runner 强制 `local`）——project 的执行目录、agent argv、`allow_exec`、`interactive_allowed_agents` 全在 worker 侧解析与校验。server 上配了不算数。
3. **联邦只做了一半**：xu64.10 解决的是「worker-only project 不必在 server 重复定义」；反过来「server 定义的 project 要在每台 worker 重复定义、且只能手工」没有解。

> 实证：2026-07-13 为容器 worker 加一个 `tty-claude`，需要改 worker.yaml 两处（`agents` 定义 + 该 project 的 `interactive_allowed_agents`）并重启 worker 两次；第一次还因为漏了 `interactive_allowed_agents` 被 worker 第二道 validate 拒掉。

## 4. 已确认决策

- **D1 权威模型**：**server 推策略 + worker 内置 agent 模板**。worker 不再手写 agent 定义；内置模板 + `detect` 探测上报「我这装了什么」。server 只决定「哪个 project 能用哪些**已探测到**的 agent、能否 exec、能否 pty」。
  - 安全含义：server 无法凭空定义任意命令让 worker 执行 → 即便 server 被攻破，活动范围受限于 worker 上真实安装的 agent + worker 护栏。（`exec` agent 本身仍是任意命令，由 worker 的 `allow_exec` 护栏把关。）
  - 被否决：server 全权推送（含 agent command 定义）——那等价于对 worker 的完全 RCE，`allow_exec` 护栏形同虚设。
- **D2 首期范围**：**先做 worker 热重载 + 远程 reload**（P1）。它是所有后续阶段的公共前提，且立刻见效（改配置不必再重启进程）。

- **D3 策略传递方式**：**server 下发（push desired state）**，**不是**「远程调用让 worker 改写自己的 `projects` 配置文件」。
  - 根据：**worker 没有自主功能**——它只执行 server 派发的 job，离开 server 什么也不干。所以 worker 持久化一份 project 配置买不到任何东西，只会制造第二个真源。
  - 漂移：下发只有一个真源（server config），构造上不会漂。远程改写有两份要同步的配置，server 眼里的"这台允许什么"退化成缓存，而缓存会因手工改文件 / 半截写入 / 从旧文件重启而失效。
  - 离线 worker：远程写入对离线机器直接失败 → 集群进入"部分应用"状态要人工补。下发模式下离线不是事件——worker 上线 register 时 server 无条件重推当前 Rev，自动收敛。
  - 回滚：下发＝改回 server config + SIGHUP 全体收敛；远程改写要逐台写回，漏一台就是雷。
  - 实现代价：远程改写要在远端机器上 round-trip 一个 YAML 文件（丢注释/格式）并处理并发写、权限、磁盘满、半截写入；下发只是一个帧 + 内存应用，没有远端磁盘写入这个失败面。
  - 校验不需要持久化：worker 的第二道 validate 用**当前生效的 Policy**（内存）即可；没连上 server 时它收不到任何派发。
  - **推论（重要）**：worker.yaml 剩下的 `roots` / `guards` / identity 才是 worker 真正拥有的，**故意不做远程改写**——远程新增一个 `root` ＝ 远程扩大该机器的可执行目录范围，正是唯一应当要求机器访问权限的操作。把它做成远程一键就等于自己拆掉 D1 守住的边界。**"要上机器改"在这里是特性，不是缺陷。**
  - 妥协：worker 可把「最后应用的 Policy」**只读**落一个缓存文件，仅供 `gofer worker show` / Cluster 页展示"这台机器现在认为自己能跑什么"。**非真源**，重连时以 server 重推的为准。

- **D4 project 放置由 roots 推导，worker 端零 project 配置**（v0.3，推翻 v0.2 的 Q4）
  - **worker.yaml 不再有 `projects` 段**，一条都没有。留「本地逃生舱」与 D3 的单一真源自相矛盾：只要 worker 还能自己声明 project，配置就仍是两半，还要额外背一条「同名谁赢」的合并规则。
  - **`worker-only project`（xu64.10 G1）随之退役**：它当初就是为了绕开「project 要在 server 和 worker 各写一遍」的痛点而生的**创可贴**；本设计从根上治好那个痛点后，它反而成了「配置分两半」的唯一来源。**配置概念砍掉，代码路径保留复用**——「server 声明了但本机路径不可解析的 project」仍走 `workerOnlyProject` 的 placeholder + `remote/` 结果目录逻辑（server 不执行它，只做归档）。
  - **放置怎么决定**：**server 全量推**「所有可在 worker 上跑的 project」目录（key + 逻辑 host_path + 策略）给每台在线 worker；**worker 用自己的 `roots` 最长前缀映射自筛**——映射得到本机路径 → 接受；映射不到 → `Rejected(path_outside_roots)` 并回报原因（Cluster 页可见）。
  - 于是「每台 worker 持有的 project 集」**不由任何一边手工维护，而是从能力自动推导**：A 机挂了 `/data/proj-a`、B 机没有 → proj-a 自然只在 A 上可用，两边都不用配。这正是「worker=能力 / server=策略」的一致贯彻。
  - **取代 v0.2 §6.2 的「按 allowed_runners 算该推给谁」**：那条有洞——labels 型 runner 的候选 worker 是**提交时**按 job 携带的标签选的，runner 配置里没有 worker 列表，server 算不出目标集合。全量推 + worker 自筛没有这个问题。
  - **收益线**：新增 project 到某 worker，只要路径落在该 worker **已有的 root** 下 → **纯 server 侧一行配置，worker 零改动零重启**。只有要暴露一个该机器从未暴露过的目录树时，才必须上机器加 root——而这正是 D3 推论里**故意**要求机器访问权的操作。
  - 代价（可接受）：worker 会看到全部 worker-runnable project 的 key 与逻辑路径。单操作者场景无碍；若需收窄，P3 可选加 `projects.<key>.workers: [w-a]` / `worker_labels: [...]` 做**显式限制**（只收紧、非必需的放置声明）。
  - 迁移：现存 worker.yaml 的 `projects` 段在 P3 转为**只读+告警**（server 在 register 时打印「worker w-x 仍在本地声明 project [...]，请迁到 server config」），下一个版本忽略。

## 5. 架构

```txt
         ┌──────────────── server（策略权威）────────────────┐
         │ projects.<key>                                    │
         │   host_path / allowed_agents /                    │
         │   interactive_allowed_agents / allow_exec /       │
         │   allowed_runners  ← 决定这个 project 去哪台 worker│
         │ SIGHUP 热重载 + `gofer project add`               │
         └───────────────┬───────────────────────────────────┘
                         │ ① Policy 下发（register 后 + 每次 server reload）
                         │ ② Reload 指令（远程触发 worker 重读本地能力）
                         ▼
         ┌──────────── worker（能力提供方）─────────────────┐
         │ roots: 逻辑路径 → 本机路径映射（唯一可执行范围） │
         │ guards: allow_exec / allow_interactive（不可远程放宽）│
         │ 内置 agent 模板 + detect → 「我这装了 claude、python3，没有 codex」│
         │ max_concurrent / labels                          │
         └───────────────┬──────────────────────────────────┘
                         │ ③ Register/Applied：接受了哪些 project、拒绝了哪些（含原因）、
                         │    探测到哪些 agent → server 的能力视图（前端级联已消费）
                         ▼
                  Cluster / NewJob 页面自动收窄
```

**职责边界**：worker 回答「**能**跑什么」，server 回答「**准**跑什么」。两者 AND 后才是有效能力——这正是现有 `capabilitiesFor`（`internal/job/capabilities.go`）+ 前端级联已经在消费的模型，只是数据来源从「worker 手写」换成「worker 探测 + server 下发」。

## 6. 数据模型

### 6.1 worker.yaml（目标形态）

```yaml
worker_id: w-container-example
server_link: { urls: [...], token_env: GOFER_WORKER_TOKEN }
labels: [linux, docker]
max_concurrent: 4

# 能力：server 的逻辑路径 → 本机路径。worker 只在这些根下执行，映射不到即拒绝该 project。
roots:
  - from: D:/work/inhere
    to: /path/to/ws-root

# 护栏：server 只能收紧、不能放宽
guards:
  allow_exec: true          # 默认 false
  allow_interactive: true   # 默认 false
  allow_custom_agents: false # 是否接受 server 下发的自定义 agent 定义（默认 false）

# 逃生舱：内置模板不够时才用（默认空）
agents: {}
```

**worker.yaml 里没有 `projects` 段**（D4）——一条都没有。worker 只声明「我这台机器是什么样的」，不声明「我允许跑什么」。`agents` 从必填降为逃生舱。

### 6.2 server 侧策略表达（零新增配置面）

复用现有结构，不新增 key：

- `projects.<key>.host_path` → **逻辑路径**，由每台 worker 的 `roots` 映射成本机路径（映射不到 = 那台机器跑不了它）。现有 `container_path` 是这个思路的硬编码单例，保留兼容。
- `projects.<key>.allowed_agents` / `interactive_allowed_agents` / `allow_exec` → 该 project 在 worker 上的策略（与 worker 护栏 AND）。
- `projects.<key>.allowed_runners` → 仍是**准入**（这个 project 允许经哪些 runner 提交），**不再兼任「推给谁」**（见 D4：那条在 labels 型 runner 下算不出来）。

**放置（哪台 worker 持有哪些 project）不写在任何配置里，由 roots 推导**：

```txt
server: 把所有 worker-runnable project 的目录整份推给每台在线 worker
worker: 逐条用 roots 最长前缀映射
          D:/work/inhere/foo  ──[from D:/work/inhere → to /path/to/ws-root]──▶ /path/to/ws-root/foo  ✅ 接受
          /data/only-on-boxB  ──[无 root 命中]────────────────────────────▶ ❌ Rejected(path_outside_roots)
```

于是「把 project X 放到 worker W 上」= 在 server 声明 X（路径落在 W 已有的 root 下即可）+ SIGHUP；「允许 X 用 tty-claude」= `interactive_allowed_agents` 加一行 + SIGHUP。**worker 零改动、零重启。**

### 6.3 协议（proto v2 → v3）

新增 server→worker 两类帧；worker→server 一类：

```go
// server → worker：策略下发。register 后立即发一次；server SIGHUP reload 后重推所有在线 worker。
type Policy struct {
    Rev      int64            // server 配置代次，单调递增；worker 幂等应用，旧 Rev 丢弃
    Projects []PolicyProject  // key / host_path(逻辑) / allowed_agents / interactive_allowed_agents / allow_exec
    Agents   []PolicyAgent    // 可选：自定义 agent 定义，仅当 worker guards.allow_custom_agents=true 才接受
}

// server → worker：远程 reload（重读 worker.yaml + 重跑 detect + re-register），P1 就要
type ReloadCmd struct { Reason string }

// worker → server：应用结果（接受/拒绝可见化，别让人猜）
type Applied struct {
    Rev      int64
    Accepted []string                  // project keys
    Rejected []struct{ Key, Reason string } // path_outside_roots / guard_denied / ...
    Degraded []struct{ Key, Gate string }   // 护栏收紧了：如 server 给了 allow_exec，worker guards 拒绝
}
```

**向后兼容**：现有版本闸（`hub.go` 按 `ProtocolVersion` 拒绝过旧 worker）扩展为「v2 worker 仍按旧语义跑（worker 本地 config 为准，不下发 Policy）」，v3 才吃 Policy。**不强制一次性升级所有 worker。**

## 7. 关键流程

### 7.1 worker 接入（P3 之后的目标态）

```txt
worker 启动
  → 读 worker.yaml（identity / roots / guards / max_concurrent）——没有 projects
  → 对每个内置 agent 模板跑 detect → 得到「已安装 agent 集」
  → Register{proto:3, labels, agents=已探测集}                  ← 不再上报 projects
server
  → 把所有 worker-runnable project 的目录整份下发 → Policy{Rev, Projects}（不做 per-worker 裁剪）
worker（自筛 = 放置决策）
  → 逐个 project：host_path 经 roots 最长前缀映射
        映射得到 → Accepted（记下本机路径，后续 job 的 cwd 由它解析）
        映射不到 → Rejected(path_outside_roots)
  → 护栏收紧：guards.allow_exec=false → 该 project 的 exec 能力 Degraded（不拒 project，只降能力）
              guards.allow_interactive=false → 同理降交互能力
  → Applied{Rev, Accepted, Rejected, Degraded} + 上报「我现在能跑的 project 集 + agent 集」
server
  → 更新能力视图 → /v1/meta、/v1/runners、Cluster 页、NewJob 级联全部自动跟上
```

### 7.2 改一条策略（用户视角，目标态）

```txt
gofer project add X --path <逻辑路径> --agent tty-claude   (或直接编辑 server config)
  → kill -HUP <serve pid>（或 `gofer serve reload`）
  → server 重推 Policy（当前 Rev）给所有在线 worker
  → 路径落在 root 下的 worker：Accepted，立刻可用
    其余 worker：Rejected(path_outside_roots)，Cluster 页看得到原因
  → 浏览器刷新即可选到 X + tty-claude
全程不登录 worker 机器、不重启 worker 进程。
唯一需要上机器的情况：该路径不在这台 worker 的任何 root 下 —— 加 root = 扩大该机可执行范围，
故意保留为需要机器访问权的操作（D3 推论）。
```

### 7.3 P1（首期）：热重载 + 远程 reload

```txt
本地： kill -HUP <worker pid>            → 重读 worker.yaml + 重跑 detect + re-register
远程： gofer worker reload <worker_id>   → server 经 hub 下发 ReloadCmd → 同上
       POST /v1/workers/{id}/reload      → 同上（web Cluster 页按钮）
不中断在跑的 job：reload 只替换配置快照与能力上报，不动 in-flight 的执行槽。
```

## 8. 安全

- **护栏是 AND，不是 OR**：有效能力 = server 策略 ∩ worker 护栏 ∩ 实际探测到的 agent。server 只能收紧不能放宽。
- **路径根**：`roots` 是 worker 上唯一可执行范围；server 下发的 `host_path` 映射不进任何 root 就直接拒（不是回落到当前目录）。杜绝「server 指哪打哪」。
- **agent 定义不下发**（D1）：默认只认内置模板 + worker 本地 `agents`。`guards.allow_custom_agents` 是显式的、要在 worker 机器上开的逃生舱。
- **exec**：`exec` agent 天然是任意命令。worker `guards.allow_exec=false` → 该 worker 上所有 project 的 exec 一律降级不可用，server 无法推翻。
- **不放大信任面**：worker token ↔ worker_id 绑定不变（`hub.go` 现有校验），Policy 只在已鉴权的 hub 连接上下发。

## 9. 分阶段

| 阶段 | 内容 | 依赖 |
|---|---|---|
| **P1**（首期，已选） | worker 热重载：SIGHUP + `gofer worker reload <id>`（经 hub）+ `POST /v1/workers/{id}/reload`；reload = 重读 config + 重跑 detect + re-register，不中断在跑 job | — |
| **P2** | 内置 agent 模板注册表 + detect 上报；worker.yaml 的 `agents` 降级为逃生舱 | P1 |
| **P3** | Policy 下发（proto v3）：server 全量推 project 目录；worker 按 roots 最长前缀映射自筛（接受/拒绝/降级）并回报；worker.yaml 去掉 `projects`（旧字段只读+告警一个版本）；`worker-only project` 配置概念退役（placeholder 代码路径复用） | P1,P2 |
| **P4** | 管理面：Cluster 页展示每台 worker 的 accepted / rejected(原因) / degraded / detected agents；CLI `gofer worker projects <id>`；可选 `projects.<key>.workers` 显式收窄 | P3 |

每阶段单独可发布、可回退；v2 worker 全程不受影响。

## 10. 已定稿的细节（原 Q1-Q5）

- **Q1 roots 匹配语义** ✅ **前缀 + 最长匹配优先**。归一化后比较：统一分隔符为 `/`、Windows 侧大小写不敏感（`D:/work` == `d:/work`），Linux 侧敏感。映射不中任何 root → 拒绝该 project（`path_outside_roots`），**绝不回落到进程 CWD**。
- **Q2 worker 侧是否要显式白名单确认** ✅ **自动接受**，靠 `roots` 护栏兜底。加一层"worker 侧确认"就又回到「要上机器改配置」的老路，等于白做；真正的准入边界是 roots + guards，而不是一张要人工维护的清单。
- **Q3 Policy 乱序 / 断连重连** ✅ Rev 单调递增，worker 丢弃旧 Rev；重连时 server 无条件重推当前 Rev（幂等应用）。
- **Q4 worker 本地 `projects` 逃生舱** ❌ **v0.3 推翻：不保留**（见 D4）。理由：与 D3 的「单一真源」直接矛盾——只要 worker 还能自己声明 project，配置就仍是两半，还多一条「同名谁赢」的合并规则要人记。而 `worker-only project` 本就是「配置要写两遍」的绕行方案，本设计根治该痛点后，它反成分裂的唯一来源。**配置概念退役，代码路径（placeholder + `remote/` 结果目录）保留复用**于「server 声明了但本机不可解析路径」的 project。
  - 真正的 worker 主权保留在 `roots` / `guards` / identity —— 那些才是 worker 拥有的东西，且**故意**不可远程改。
- **Q5 P1 的 reload 是否重连 hub** ✅ **不重连**，只换配置快照 + 重跑 detect + re-register，避免打断在跑的 job。

## 11. 待确认

（暂无——待实施中发现。）

## 12. 结论

问题的根不在「少了个远程改配置的接口」，而在**职责划错了边界**：策略被钉死在远端机器的静态文件里。把 worker 收敛为能力提供方后，「加 project」「开 pty agent」这类高频操作天然回到 server 侧——那里本来就有热重载和 CLI。

v0.3 的关键修正：**不能只把策略上收、却给 worker 留一个 project 逃生舱**——那等于宣称单一真源却又保留两半配置。worker 端 project 配置归零，而「哪台 worker 持有哪些 project」这个**看起来必须手工维护的东西，其实是从 `roots` 自动推导出来的**：server 全量推目录、worker 按能力自筛。手工维护的清单从两边同时消失，这才是把边界划对了的标志。

P1 的热重载是所有后续阶段的地基，也是唯一一个现在就能立刻缓解疼痛的改动。
