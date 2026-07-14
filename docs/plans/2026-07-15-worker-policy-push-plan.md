# P3：Policy 下发（server 推策略 → worker 投影执行）实施计划

> 设计：[`docs/design/2026-07-13-worker-config-federation-design.md`](../design/2026-07-13-worker-config-federation-design.md)（**v0.7**，Q6/Q7/Q8 已裁决）
> 总纲：[`docs/plans/2026-07-13-worker-config-federation-plan.md`](2026-07-13-worker-config-federation-plan.md)（P1 ✅）
> 前置：[`docs/plans/2026-07-14-worker-agent-templates-plan.md`](2026-07-14-worker-agent-templates-plan.md)（P2 ✅）
> bd epic：`tools-5pq`　基线：`4bce415`

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-07-14 | Claude | 初版。基于设计 v0.7（Q6 砍 `Policy.Agents` / Q7 ack 带 Policy + policy_pending / Q8 空 `allowed_runners` 不推）。起草时又核出 5 条设计漏掉的结构性事实（§13-C15..C19），已在「现状事实」逐条列出并附核实命令 |

---

## 1. 目标

**新增一个 project 到某台 worker、允许它用某个 agent —— 只改 server 配置 + SIGHUP，不登录 worker、不重启 worker 进程。**

**先把收益说准（别吹大）**：

- ✅ **真实收益 = 高频操作回到 server**：加 project / 改白名单 / 开 exec / 开 pty，**worker 零改动零重启**。前提是该 project 的路径**已经落在这台 worker 已有的 `root` 下**，且 `allowed_runners` 已列出该 worker 的 runner。
- ⚠️ **不是"全自动"**：要暴露一个该机器**从未暴露过的目录树**时，**仍必须上机器加 `root`**。这是 D3 推论——**故意**保留为需要机器访问权的操作，不是没做完。
- ⚠️ **不是安全收益的净增**：D1 的"server 无法凭空定义命令"**P2 就已成立**。P3 要做的是**别把它弄丢**（Q6 砍掉 `Policy.Agents` 正是为此）。护栏（`guards`）买到的是"限制爆炸半径 + 防误配"，**不是**"抵御被攻破的 server"（worker 仍然信任 server，设计 §8）。
- ⚠️ **现网存量 project 不会自动上 worker**：没写 `allowed_runners` 的 project，P3 之后依然只能 local 跑（Q8，与今天一致）。

---

## 2. 验收（先写死；每条都指出"怎么证明它真的成立"）

1. **🔴 现网零破坏（最高优先级，承接 P2 验收 1）**：用**当前 live 形态**的 worker.yaml（有 `projects` 段、无 `roots`、无 `guards`）——只换二进制、**配置零改动**：
   - `/v1/meta` 的该 worker `projects` / `agent_caps` **逐条 diff 为空**
   - 原有 project 提交 exec job / tty-claude 交互 job **仍跑通**
   - **证明方式**：旧二进制起一次记下 `/v1/meta`，换 P3 二进制再记一次，`diff` 必须为空。
   - > 这条成立的前提是：v4 server 对**有本地 projects 的 v4 worker** 的降级行为、以及 `guards` 缺省语义（T1-D）都对。**先证伪**：把 guards 默认写成 `false`，这条验收必须立刻红（exec job 被护栏拒）。

2. **纯 server 侧加 project**：server config 新增一个 project（`host_path` 落在容器 worker 已有 root 下 + `allowed_runners: [w-container-example 的 runner]`）→ `kill -HUP <serve pid>` → **不碰 worker**：
   - `/v1/meta` 中该 worker 的 `projects` 出现新 key
   - **worker 进程 PID 前后不变**（`pgrep` 前后对比，不看日志自述）
   - 立刻用该 project 提交 job → 跑通，且 `cwd` 落在**映射后的本机路径**（断言 job stdout 里的 `pwd`）

3. **roots 映射失败 = 拒绝，不是"落到随机目录"**：server 推一个路径不在任何 root 下的 project →
   - worker `Applied.Rejected` 含 `{key, path_outside_roots}`，该 key **不在** `/v1/meta` 的 worker `projects` 里
   - **证明方式**：断言该 project **完全不进** worker 的 `cfg.Projects`（单测直接读投影结果），且**不存在任何 `HostPath==""` 的 ProjectConfig**。
   - **先证伪**：把"映射失败也生成一个 ProjectConfig"写回去 → 必须复现 `filepath.Abs("")` = 进程 CWD、结果散落（`workerOnlyProject` 注释里记着的同款坑）。

4. **🔴 SIGHUP 不得清空 project（本期最容易翻车的一条）**：worker 已应用 Policy（N 个 project）→ `kill -HUP <worker pid>` →
   - `/v1/meta` 的该 worker `projects` **仍是那 N 个**（不是 0 个）
   - **先证伪**：不保存 `lastPolicy` 的实现下，这条必须**真的**复现为"projects 变成 0、worker 彻底停摆"。**没复现过就说明测试没测到东西。**

5. **加 root 立刻生效**：worker.yaml 加一个 root（覆盖之前被拒的 project）→ `gofer worker reload <id>` → 该 project 从 `Rejected` 变成 `projects` 里的一员，**worker PID 不变**。

6. **护栏只收紧、不放宽**：`guards.allow_exec: false` 的 worker + server 侧 `allow_exec: true` 的 project → exec job **被 worker 拒**（明确错误），且 `Applied.Degraded` 含该 key。反向：`guards.allow_exec: true` + server `allow_exec: false` → **仍然拒**（server 说了不准）。

7. **🔴 `allowed_runners` 为空 ⇒ 不推给任何 worker（Q8，反向测试钉死）**：一个没写 `allowed_runners` 的 project → **任何** worker 的 Policy 里都**没有**它。
   - **证明方式**：单测断言 `computePolicy(cfg, w)` 对每台 w 都不含该 key；e2e 断言 `/v1/meta` 的 worker `projects` 里没有它。
   - **这条是防"好心实施者把空当通配"的唯一保险**。

8. **白名单不做交集（D6，反向测试钉死）**：policy 给 `allowed_agents: [claude, tty-codex]`，而 worker 上**没装 codex** →
   - worker 的 `cfg.Projects[k].AllowedAgents` **逐字等于** `[claude, tty-codex]`（**不是** `[claude]`，更**不是** `[]`）
   - 用 `tty-codex` 提交 → 明确报错（host 侧 `agent not on worker` 或 worker 侧 `unknown agent`），**不是**静默放行
   - **为什么要这条**：一旦有人"优化"成交集，`allowed_agents` 交出空列表就会**静默放开全部 agent**（空 = 放行全部）。这条测试是那道墙。

9. **空窗期给准确错误（Q7-a）**：worker 已 register 但尚未 Applied（人为拖慢投影）→ 此时提交 job →
   - 错误信息是 **"worker w-x 尚未应用策略（policy_pending, rev=N）"**，**不是** `project "X" not on worker w-x`
   - **证明方式**：单测里注入一个 block 住的 PolicyFunc，断言错误字符串。

10. **滚动升级矩阵**（逐格给出预期，不能只写"兼容"）：
    | 组合 | 预期 | 怎么证明 |
    |---|---|---|
    | v4 server + v3 worker | 不下发 Policy；worker 继续用本地 `projects`；能连能跑 | 用 `c3ee6d1` 编的 v3 worker 二进制实测 |
    | v4 server + v2 worker | 同上（连 reload 都没有） | 复用 P1 的 `4def378` 旧二进制 |
    | **v3 server + v4 worker（已删 projects 段）** | worker **醒目告警**"server 不支持 Policy 且本机无 projects → 本 worker 不会接到任何 job"，且**不崩溃**、仍在线 | 用 `c3ee6d1` 编的 v3 server 实测；断言日志里有该告警 |
    | v4 server + v4 worker | 全功能 | 主路径 |
    | 多 URL 混合（新旧 server） | 轮到旧 server → 降级；轮回新 server → 重新拿 Policy 并应用 | 双 server 隔离栈实测；断言 Rev 回退**不会**让 worker 永久丢弃新 Policy |

11. **hub 边界不破**：`go list -deps ./internal/wshub | grep gofer` 输出**只有** `internal/wsproto`（+ 自身）。
    - **证明方式**：命令输出逐字比对。**先证伪**：把 policy 计算直接写进 wshub → 这条必须立刻红。

12. **worker 机器上的 CLI 不塌**：去掉 `projects` 段的 worker 上，`gofer project list` 列出**当前生效的 project**（读 policy 缓存），`gofer config validate` **PASS**（不是因 0 projects 而 FAIL）。

13. **原子性 + 无竞态**：`go test -race ./internal/core/... ./internal/wshub/... ./internal/worker/...` 绿。并发 `Submit × PolicyApply` 断言每次 Submit 要么完全看到旧配置、要么完全看到新配置（承接 P1 验收 8）。

14. 全量 `go test ./... -p 1 -count=1` 绿；`go vet` 绿。

---

## 3. 现状事实（**每条附核实命令**）

> **纪律（P1/P2 三次血的教训）**：不附核实命令的"已核实"，三次里有两次是假的（v0.4 的 pin 前提、v0.5 的 proto v3、P2 的"只有一处 core-less 装配点"——全错）。**行号会漂，命令不会。**
> 设计 §13 的 **C1-C19** 已覆盖大部分。下面只列**直接决定 P3 任务形状**的，其余引用设计。

### 3.1 P3 要新建的东西（不是"复用已有的"）

| 事实 | 核实命令 | 含义 |
|---|---|---|
| **`roots` / `guards` 在代码里不存在** | `grep -n "type WorkerConfig" -A 10 internal/config/model.go` | `WorkerConfig` 只有 `worker_id/server_link/projects/agents/runners/max_concurrent/labels/storage`。**P3 新建**：YAML 结构 + defaults + validate + **最长前缀映射实现**（设计 §10-Q1 只定了语义，**零代码**） |
| **`wshub` 只 import `wsproto`** | `go list -deps ./internal/wshub \| grep gofer` | 输出只有 `internal/wsproto`。⇒ **推送目标计算（读 `cfg.Projects`/`cfg.Runners`）不能放 hub**。必须注入 seam（照 P1 `hubWorkerReloader` 的先例），实现放 `internal/core`（它已同时持有 cfg / hub / job）|
| **`Registered` ack 不带 server 版本** | `grep -n "type Registered" -A 6 internal/wsproto/frames.go` | 只有 `{Accepted, Reason, ServerTime}`。⇒ v4 worker **分不清**「server 是 v3、永远不发 Policy」与「server 是 v4、这台的 Policy 恰好为空」。**必须补 `ProtocolVersion`**（验收 10 的第三格靠它） |
| **`Core.Cfg` 是裸字段，跨包并发读是 race** | `grep -n "c.Cfg = cfg" internal/core/core.go`；`grep -rn "cr.Cfg" internal/serve/serve.go`；`bd show tools-cg4` | `core.go:339` 裸写，`serve.go:390/496/674` 裸读。**已有 bd 记着**。⇒ PolicySource 要读**当前** cfg **+ Rev**，两者必须**一次原子读**（分两次会拿到 `(旧cfg, 新rev)` → worker 记下新 Rev 却应用了旧配置 → 真正的新 Policy 因 Rev 相同被丢弃 → **永久卡在旧配置**）。安全先例：`job.Service.Config()`（`grep -n "func (s \*Service) Config" internal/job/service.go`，atomic）——但它**没有 Rev** |

### 3.2 P3 会打穿的现网路径（不处理就是回归）

| 事实 | 核实命令 | 含义 |
|---|---|---|
| worker 的 project 来源**只有** worker.yaml 一处 | `grep -n "func workerConfigToConfig" -A 8 internal/commands/worker.go`<br>`grep -n "func workerCaps" -A 8 internal/commands/worker.go` | `Projects: wc.Projects` / `Projects: mapKeys(wc.Projects)`。去掉 `projects` 段 → **两处同时变空**。`workerCaps` 的 `Projects` 必须改从**投影后的 `cfg.Projects`** 取 |
| **SIGHUP 会重走 `workerConfigToConfig`** | `grep -n "func newWorkerReloadFn" -A 20 internal/commands/worker.go` | reload 路径 = 重读 worker.yaml → `workerConfigToConfig` → `ReloadWith`。**P3 之后 `wc.Projects` 是空的** ⇒ **一次 SIGHUP 就把所有 project 清空、worker 彻底停摆**。⇒ worker **必须在内存里持有 lastPolicy**，两条路径共用同一个 projection（验收 4） |
| worker 机器上 `gofer project list` **直读 `wc.Projects`** | `grep -n "func localProjects" -A 12 internal/commands/project.go` | `RunMode()==worker` → `loadWorkerConfig("").Projects`。去掉段后**恒空**。CLI 是独立进程，看不到 worker 内存 ⇒ **必须落只读 policy 缓存文件** |
| worker doctor **0 project 直接 FAIL** | `grep -n "no projects (the worker has nothing to run)" -B 4 internal/commands/config.go` | `validateWorkerConfig`：`len(keys)==0` → FAIL + 非零退出码。⇒ P3 之后**每台正常 worker 上** `gofer config validate` 都会失败 |
| `gofer worker show` **不存在** | `grep -n "Name:" internal/commands/worker.go` | 只有 `stop` / `reload`。设计 v0.7 已删掉该引用；**P3 不实现它**（P4） |

### 3.3 投影必须喂饱的字段（D6，逐个核实过）

| 读取点 | 核实命令 | 投影取值 |
|---|---|---|
| cwd 解析 | `grep -n "SafeJoin" internal/job/submit.go`；`grep -n "func (c \*Config) ExecPath" -A 6 internal/config/model.go` | `HostPath` = roots 映射后的本机路径（worker 无 `server.path_view` ⇒ `ExecPath` 恒回落 `HostPath`）。**映射不到 ⇒ 整条不进配置**（空 `HostPath` 经 `filepath.Abs` = 进程 CWD） |
| 结果目录 | `grep -n "func ResultBaseDir" -A 12 internal/project/path.go` | `Storage.Root` 或 `HostPath`+子目录 —— `Storage` 取 **worker 本地**（`workerConfigToConfig` 已这么做，不改） |
| agent 白名单 | `grep -n "AllowedAgents" internal/job/config.go` | **原样透传**（空 = 放行全部已配置 agent） |
| 交互白名单 | `grep -n "InteractiveAllowedAgents" internal/job/config.go` | **原样透传**；`guards.allow_interactive` 显式 false ⇒ **清空**（空 = 全禁，语义与上一行**相反**——见 §5 风险表） |
| exec 闸 | `grep -n "not allowed: project" -B 2 internal/job/config.go` | `policy.AllowExec && guards.allow_exec`（护栏只收紧） |
| runner 准入 | `grep -n "func checkRunnerAllowed" -A 10 internal/job/config.go` | 恒为 `["local"]`（`dispatch.go` 强制 `Runner=local`；非空且不含 local 的列表会被拒） |
| agent 定义 | `grep -rn "agent.Resolve" internal/core/core.go` | **不投影、不拼装**。交给 `core.ReloadWith` 里的 `agent.Resolve`（P2 建的唯一 merge 点）|

### 3.4 P2/P1 已经建好、P3 只消费的（**不要重做**）

- `agent.Resolve`（探测 → 只把探到的模板注入 `cfg.Agents`；逃生舱永不被摘）——唯一调用点 `core.Build` / `core.ReloadWith`。
- `Caps` 帧 + `reg.UpdateCaps` —— **唯一**的能力视图更新通路（`grep -n "UpdateCaps" internal/wshub/hub.go` → `ReloadResult.Caps` 与 `TypeCaps` 两个入口收敛到同一个）。⇒ **`Applied` 必须内嵌 `*Caps`**，走同一条路；`Rejected`/`Degraded` 只做诊断，**不参与路由判定**。
- P1 的**串行 reload executor**（`grep -n "func (cl \*Client) reloadLoop" -A 10 internal/worker/reload.go`）——⇒ **Policy apply 必须进同一个 `reloadCh` 队列**（两者改的是同一个 core，并发 `ReloadWith` = 旧配置覆盖新配置，P1 T3 已解过一次）。
- pin=硬授权（`grep -n "pinned to worker" -B 12 internal/job/config.go` + `internal/job/pin_test.go`）——D4′ 的前提，仍成立。

---

## 4. 任务分解

> 依赖：**T0 先做**（地基）；T1 / T2 可**并行**；T3 需要 T0+T2；T4 需要 T2+T3；T5 需要 T1+T2；T6 需要 T5；T7 最后。

### 🔴 T0 地基：`(cfg, rev)` 原子快照 + PolicySource seam（**第一个做**）

**T0-A 一次原子读拿到 `(cfg, rev)`**（不解这条，Rev 机制是假的 —— §3.1 第 4 行）

```go
// internal/core
type ConfigSnapshot struct {
    Cfg *config.Config
    Rev int64            // server 配置代次；Build=1，每次 ReloadWith +1
}
func (c *Core) Snapshot() ConfigSnapshot          // 单次 atomic.Pointer 读
func (c *Core) Config() *config.Config            // = Snapshot().Cfg（给 serve 的三处裸读用）
```

- `Core.Cfg` 裸字段**改为 accessor**，`serve.go` 的三处裸读（`:390` / `:496` / `:674`）跟着改 → 顺手关掉 bd `tools-cg4`。
- **验收（T0-A）**：`-race` 测试：N goroutine 循环 `Snapshot()` + 1 goroutine 循环 `ReloadWith` → 断言每次读到的 `(Cfg, Rev)` **同属一代**（Rev 与 Cfg 内容对得上，用一个可辨识的 project key 做标记）。

**T0-B PolicySource seam（hub 不能 import config —— §3.1 第 2 行）**

```go
// internal/wshub（只依赖 wsproto，边界不破）
type PolicySource interface {
    PolicyFor(workerID string) (wsproto.Policy, bool) // ok=false ⇒ 不下发（如 source 未接）
}
func (h *Hub) SetPolicySource(ps PolicySource)
```

实现 `corePolicySource` 放 `internal/core`（照 `hubWorkerSelector` 的先例，同文件邻位）。

- **验收（T0-B，就是验收 11）**：`go list -deps ./internal/wshub | grep gofer` **只有** `internal/wsproto`。**先证伪**：把计算写进 wshub → 该命令必须多出 `internal/config`。

**提交**：`feat(core): atomic (cfg, rev) snapshot + hub policy-source seam (P3 T0)`

---

### T1 worker.yaml 新字段：`roots` + `guards`（含最长前缀映射）

**T1-A `config.WorkerConfig` 加字段**

```go
type WorkerRoot struct {
    From string `yaml:"from"` // server 侧逻辑路径前缀
    To   string `yaml:"to"`   // 本机路径前缀
}
type WorkerGuards struct {
    // *bool，不是 bool —— 见 T1-D。nil = 未设 = 不额外收紧（等价于今天的行为）。
    AllowExec        *bool `yaml:"allow_exec,omitempty"`
    AllowInteractive *bool `yaml:"allow_interactive,omitempty"`
}
// WorkerConfig 追加：
Roots  []WorkerRoot `yaml:"roots,omitempty"`
Guards WorkerGuards `yaml:"guards,omitempty"`
```

**T1-B 最长前缀映射（设计 §10-Q1 的语义，第一次落成代码）**

```go
func (wc *WorkerConfig) MapRoot(logical string) (host string, ok bool)
```

- 归一化：`\` → `/`；去尾斜杠；**Windows 侧大小写不敏感**（`D:/work` == `d:/work`），Linux 侧敏感。
- **最长 `From` 优先**；**边界必须对齐**（`/a/b` 不得匹配 `/a/bc` —— 只有 `完全相等` 或 `logical[len(from)] == '/'` 才算命中）。
- 命中后：`host = To + logical[len(From):]`，再 `filepath.Clean`。
- **不中任何 root ⇒ `ok=false`。绝不返回空串当"成功"。**

**T1-C 边界（写死，防实施者发挥）**

- roots **不做**远程改写、**不做**任何 API 暴露（D3 推论：加 root = 扩大该机可执行范围，**故意**要求机器访问权）。
- `..` / 符号链接逃逸：映射产出的 host path 仍会经 `project.SafeJoin` 二次约束；本任务只保证**不产出空串、不越出 `To` 前缀**。

**T1-D `guards` 缺省语义（⚠️ 与设计 §6.1 的"默认 false"有意偏离，理由写死）**

- 设计 §6.1 写 `guards.allow_exec` **默认 false**（安全默认）。**本计划改为 `*bool`，nil（未设）= 不额外收紧**。
- **理由**：现网 worker.yaml **没有 `guards` 段**。`bool` 零值 = false ⇒ **升级二进制那一刻，所有 exec job 与所有 pty job 立刻被护栏拒掉** —— 这直接违反验收 1（现网零破坏，P2 的最高优先级纪律）。
- `*bool` 是本仓既有手法（`ProjectConfig.CaptureDiff` / `NotifyEnabled` / `AgentBrief.Available` 全是这个理由）：**"未设" ≠ "显式 false"**。
- 代价：护栏是 **opt-in**。⇒ `gofer init worker` 模板里**显式写出 guards** 并注释；worker doctor 在未设时**给 WARN 提示**（不 FAIL）。
- **验收（T1-D，就是验收 1 的"先证伪"）**：把 `AllowExec` 改成裸 `bool` → 验收 1 的 exec job 必须**真的**被拒。没红 = 测试没测到东西。

**验收（T1）**：表驱动单测覆盖：多 root 最长优先 / 边界不对齐不命中（`/a/b` vs `/a/bc`）/ Windows 盘符 + 大小写 / 尾斜杠 / 映射失败返回 `ok=false` / **任何情况下不返回空 host**。

**提交**：`feat(config): worker roots + guards with longest-prefix mapping (P3 T1)`

---

### T2 proto v4 帧（Policy / Applied / Registered 扩展）

```go
// internal/wsproto/frames.go
const CurrentProtocolVersion = 4              // 从 3 提到 4（MinProtocolVersion 保持 2，不踢任何存量 worker）
const PolicyMinProtocolVersion = 4            // 照 ReloadMinProtocolVersion 的模式
func SupportsPolicy(proto int) bool { return proto >= PolicyMinProtocolVersion }

type PolicyProject struct {
    Key                      string   `json:"key"`
    HostPath                 string   `json:"host_path"`            // 逻辑路径
    AllowedAgents            []string `json:"allowed_agents"`       // 无 omitempty：空列表必须显式上线，不能与"未设"混淆
    InteractiveAllowedAgents []string `json:"interactive_allowed_agents"`
    AllowExec                bool     `json:"allow_exec"`
}
type Policy struct {                          // s→w
    Rev      int64           `json:"rev"`
    Projects []PolicyProject `json:"projects"`
}
type Applied struct {                         // w→s
    Rev      int64              `json:"rev"`
    Caps     *Caps              `json:"caps,omitempty"`     // ★ 内嵌，走 reg.UpdateCaps 这条唯一通路
    Rejected []AppliedRejection `json:"rejected,omitempty"` // {key, reason}
    Degraded []AppliedDegrade   `json:"degraded,omitempty"` // {key, gate}
}
// Registered 追加（Q7-b + §3.1 第 3 行）：
ProtocolVersion int     `json:"protocol_version,omitempty"` // server 实现的版本；旧 server 解出 0
Policy          *Policy `json:"policy,omitempty"`
```

- 新 FrameType：`TypePolicy = "policy"`（s→w）/ `TypeApplied = "applied"`（w→s）。
- **`Policy.Agents` 不存在**（Q6）。`guards.allow_custom_agents` 不存在。**任何"顺手加上以备将来"都是把 D1 的边界重新捅穿。**
- **`Applied` 不得另起能力上报通路**：`Caps` 内嵌，hub 收到后走**同一个** `reg.UpdateCaps`；`Rejected`/`Degraded` 只挂在 worker 记录上给 Cluster 页看，**不参与路由判定**。

**验收（T2）**：
- `MinProtocolVersion` 仍是 2 → v2/v3 worker 注册**不被踢**（矩阵单测，复用 P1 的 `hub_version_test.go` 模式）。
- 旧 server 的 `Registered` 帧解码后 `ProtocolVersion == 0`（`As[T]` 无 `DisallowUnknownFields`，单测钉死）。
- `PolicyProject` 的空 `allowed_agents` **序列化为 `[]` 而不是省略**（否则"显式清空"与"未设"再次不可分——P2 `*bool` 那一课的同款）。

**提交**：`feat(wsproto): protocol v4 policy/applied frames (P3 T2)`

---

### T3 server 侧推送目标计算（D4′ + Q8）

`internal/core/policy.go`：

```go
// 纯函数，无副作用，好测。
func computePolicy(cfg *config.Config, workerID string, rev int64) wsproto.Policy
```

对每个 project P：

```txt
P 推给 W  ⟺  ∃ r ∈ P.allowed_runners 使 W 经 r 可达：
    cfg.Runners[r].Type == "worker" && WorkerID == W   → 可达（pin 型，精确）
    cfg.Runners[r].Type == "worker" && WorkerID == ""  → 可达（池型；候选提交时才定，server 算不出 → 保守全推）
    其余（local / peer-http / 未定义的 runner key）     → 忽略
P.allowed_runners 为空 → ∃ 恒假 → 不推给任何 worker      ← Q8，【不许当通配】
```

- `HostPath` 取 `proj.HostPath`（server 侧的 host_path **就是**逻辑路径）。`ContainerPath` **不下发**（那是 server 自己 `path_view` 的事）。
- project key 先过 `checkProjectKey` 同款字符集校验（key 会变成 worker 上的目录名）。

**验收（T3）**：
- **反向测试（验收 7）**：空 `allowed_runners` 的 project → `computePolicy` 对**每台** worker 都返回 0 个该 key。**先证伪**：改成"空=全推" → 该测试必须红。
- pin 到 `w-a` 的 project **不出现**在 `w-b` 的 Policy 里。
- 池型 runner 的 project → 两台 worker 都拿到。
- `allowed_runners: [local]` → 不推给任何 worker。

**提交**：`feat(core): compute per-worker policy from runner reachability (P3 T3)`

---

### T4 hub 下发（Registered ack 带 Policy + reload 广播 + policy_pending）

**T4-A ack 同一次写带 Policy（Q7-b）**：`hub.Accept` 的第 4 步（ack）改为：

```go
ack := wsproto.Registered{Accepted: true, ServerTime: ..., ProtocolVersion: wsproto.CurrentProtocolVersion}
if h.policySrc != nil && wsproto.SupportsPolicy(reg.ProtocolVersion) {
    if p, ok := h.policySrc.PolicyFor(reg.WorkerID); ok {
        ack.Policy = &p
        wc.markPolicyPending(p.Rev)   // Q7-a
    }
}
```

**T4-B server reload 后重推**：`Hub.PushPolicyAll()` 遍历 `h.reg.All()`，对 `supportsPolicy()` 的连接发 `TypePolicy`。调用点：`serve.startReloadLoop` 里 `cr.Reload(path)` **成功之后**（失败不推——旧配置没变，推了也是同一份，但会白白触发全量 worker 重投影）。

**T4-C 收 `TypeApplied`**：`readLoop` 加分支 —— **照 `TypeReloadResult` 的先例**：

```go
case wsproto.TypeApplied:
    a, err := wsproto.As[wsproto.Applied](env); if err != nil { continue }
    if a.Caps != nil { h.reg.UpdateCaps(wc, *a.Caps) }   // ★ 唯一的能力通路
    h.reg.MarkPolicyApplied(wc, a.Rev, a.Rejected, a.Degraded)  // 诊断，非路由
```

- `UpdateCaps` 的"旧连接迟到帧不得污染新连接"检查（`r.conns[wc.workerID] != wc → return`）**天然覆盖** Applied —— `MarkPolicyApplied` **必须走同款检查**，别新写一套。

**T4-D policy_pending 暴露给准入闸（Q7-a）**：

- `WorkerSnapshot` 加 `PolicyRev` / `AppliedRev` / `PolicyPending bool` / `Rejected` / `Degraded`。
- `job.WorkerCandidate` 加 `PolicyPending bool`（经 `core.workerCandidate` 填）。
- `job.capabilitiesFor` / `checkWorkerCaps` 在 `cand.PolicyPending` 时返回**明确错误**："worker %q 尚未应用策略（policy_pending, rev=%d）"，**而不是** `ErrProjectNotOnWorker`。
- ⚠️ **只对 proto>=4 的 worker 置 pending**。v3 worker 永远不 pending（它压根不收 Policy），否则**存量 worker 会被永久锁死**。

**验收（T4）**：
- 验收 9（空窗期准确错误）：注入 block 住的 PolicyFunc → 断言错误字符串。
- `-race`：Applied 与 `WorkerSnapshot` 并发无 race；旧连接迟到的 Applied 不污染新连接（复用 P1 T4 的测试模式）。
- v3 worker **不被**标 pending（回归测试）。

**提交**：`feat(wshub): push policy on register + broadcast on reload (P3 T4)`

---

### 🔴 T5 worker 侧投影 + 应用（D6；**本期最容易翻车的任务**）

**T5-A worker 内存持有 `lastPolicy`（不做这条，验收 4 必挂）**

worker 上有效配置 = `project(worker.yaml, lastPolicy)`，**两个输入独立变化**：

| 触发 | 动作 |
|---|---|
| 收到 Policy 帧 / ack 里的 Policy | 存 `lastPolicy` → 用**当前 worker.yaml** 重新投影 |
| SIGHUP / `gofer worker reload` | 重读 worker.yaml → 用**内存里的 `lastPolicy`** 重新投影 |

⇒ **两条路径共用同一个 projection 函数、同一个 P1 串行 executor。**
⇒ 不存 `lastPolicy` ⇒ SIGHUP 后 `workerConfigToConfig` 拿到空 `wc.Projects` ⇒ **worker 一个 project 都没有、彻底停摆**（静默、必然）。

**T5-B 进 P1 的串行队列（不新造应用路径）**

```go
// internal/worker/reload.go：扩展既有 reloadReq，不新建 goroutine、不新建队列。
type reloadReq struct {
    requestID string          // 远程 reload 的回执 id（空 = SIGHUP）
    reason    string
    policy    *wsproto.Policy // 非 nil = 这是一次 policy apply
}
```

- `recvLoop` 收 `TypePolicy` → **只入队**（与 `TypeReload` 完全同款）。
- 执行器串行消费 ⇒ Policy apply 与 SIGHUP reload **天然定序**，不会互相覆盖。
- policy apply 成功 → 发 `TypeApplied`；SIGHUP 成功 → 发 `TypeCaps`（P1 既有）；远程 reload → `TypeReloadResult`（P1 既有）。

**T5-C Rev 语义（⚠️ 设计只写了"单调递增、旧的丢弃"，有个坑）**

- **Rev 状态 per-connection，register 成功时清零。**
- **理由**：Rev 是**某个 server 进程**的配置代次。server 重启后 Rev 从 1 重新数 → 若 worker 跨连接保留 `lastRev=5`，它会把新 server 的 rev 1..4 **全部当"旧的"丢弃 → 永久卡在旧配置**。多 URL 混合新旧 server 时同理（验收 10 最后一格）。
- 会话内：`rev > lastRev` 才 apply（乱序保护）；`rev <= lastRev` 丢弃。
- **代价（接受）**：每次重连会重跑一次投影 + 一次 detect（P2 实测最坏 2.0s，典型 ms 级）。**不做 payload 指纹去重**（见 §6 不做）。

**T5-D 投影函数（放 `internal/commands/worker.go`，与 `workerConfigToConfig` 同处 —— P1 先例）**

```go
func projectPolicy(wc *config.WorkerConfig, p wsproto.Policy) (*config.Config, []wsproto.AppliedRejection)
```

逐个 project：

| 字段 | 取值 |
|---|---|
| `HostPath` | `wc.MapRoot(pp.HostPath)` → 失败即 `Rejected{key, "path_outside_roots"}`，**整条不进配置**（绝不产出空 `HostPath`） |
| `AllowedRunners` | 恒为 `["local"]` |
| `AllowExec` | `pp.AllowExec && guardAllowExec(wc)`（nil = 放行；显式 false = 拒） |
| `AllowedAgents` | **原样透传** `pp.AllowedAgents` |
| `InteractiveAllowedAgents` | **原样透传**；`guards.allow_interactive` **显式 false ⇒ 清空** |
| agent 定义 / Storage / Runners | **不动** —— 走 `workerConfigToConfig` 的既有逻辑（`cfg.Agents` 只放 worker.yaml 的逃生舱，其余交给 `ReloadWith` 里的 `agent.Resolve`）|

**🔴 边界（写死，防"优化"）**：
- **不做**白名单与 `cfg.Agents` 的**交集**。理由（设计 D6 v0.7）：① 交集必须在 `ReloadWith` 之后算，而那时 cfg 已发布给 `job.Service`，回头改 = data race；② `AllowedAgents` 空 = **放行全部**，`InteractiveAllowedAgents` 空 = **全禁**，语义相反——交集算出空列表会**静默放开所有 agent**。透传的准入结果与交集**完全一样**（`agent.ResolveAgent` 找不到定义 → `unknown agent`），错误信息还更准。**验收 8 是这道墙。**
- **不做** `cfg.Agents = ...` 自己拼装（绕过 P2 的 detect gate）。

**T5-E 应用 + 回报**

```txt
cfg, rejected := projectPolicy(wc, policy)
cr.ReloadWith(cfg)                       // P2 的唯一 merge 点在这里跑 agent.Resolve
degraded := diagnose(cfg, policy, wc)    // 【只读】：policy 允许但本机没装的 agent / 被 guards 收紧的 exec/interactive
caps := workerCaps(wc, cfg, det.snapshot())   // ★ Projects 改从 cfg.Projects 取，不再 mapKeys(wc.Projects)
writeAppliedFrame(Applied{Rev, &caps, rejected, degraded})
writePolicyCache(<config-dir>/run/worker-<id>.policy.json)   // 只读缓存，非真源（T6 消费）
```

- `degraded` 在 `ReloadWith` **之后**算 —— 它是**只读诊断**，**不回写配置**，所以没有 T5-D 那条顺序陷阱。

**验收（T5）**：
- **验收 4 先证伪**：删掉 `lastPolicy` 保存 → SIGHUP 后 `cfg.Projects` 必须**真的**变成 0 个。
- **验收 3 先证伪**：让映射失败也产出 ProjectConfig → 必须**真的**复现 `HostPath==""` → `filepath.Abs` = 进程 CWD。
- **验收 8**：断言 `cfg.Projects[k].AllowedAgents` **逐字等于** policy 给的列表。
- **验收 13**：`-race` 并发 Submit × PolicyApply。

**提交**：`feat(worker): project policy onto local config and apply via ReloadWith (P3 T5)`

---

### T6 `projects` 段退役 + 降级路径 + worker 机器上的 CLI 配套

**T6-A `projects` 只读 + 告警一个版本**

- v4 worker 启动 / reload 时 `wc.Projects` 非空 → `slog.Warn`："worker.yaml 的 `projects:` 段已废弃（策略改由 server 下发），请迁到 server config；下一个版本将忽略它"。
- 本版本**仍然使用它**——但**只在降级路径**（见 T6-B）。**server 支持 Policy 时，Policy 是唯一来源**（同名不合并、不"谁赢"——那正是 D4 要消灭的东西）。

**T6-B 🔴 降级路径（v3 server + v4 worker，验收 10 第三格）**

```txt
register ack 回来：
  ack.ProtocolVersion >= 4  → Policy 权威（哪怕 Projects 为空）。忽略 wc.Projects（若非空，打 T6-A 的告警）
  ack.ProtocolVersion <  4  → 旧 server，永远不会发 Policy：
        wc.Projects 非空 → 用它（今天的行为，零破坏）
        wc.Projects 为空 → 🔴 slog.Error 醒目告警：
            "server 不支持策略下发（proto=%d < 4）且本机 worker.yaml 无 projects 段
             → 本 worker 不会接到任何 job。请先升级 server（发布纪律：server 必须先升）"
```

> **发布纪律（写进迁移文档 + 告警文案）**：**server 必须先升。** P3 的真正风险不是"旧 worker 被踢"（P1 已解决，`MinProtocolVersion` 仍是 2），而是 **v4 worker（已删 `projects` 段）撞上 v3 server（不发 Policy）→ 那台 worker 一个 project 都没有、直接停摆。**

**T6-C worker 机器上的 CLI 不塌**（§3.2 后两行）

- `commands/project.go:localProjects()`：worker 模式改读 **policy 缓存文件**（`<config-dir>/run/worker-<id>.policy.json`），读不到再回落 `wc.Projects`（降级期）。
- `commands/config.go:validateWorkerConfig()`：
  - **不再**因 `len(projects)==0` 判 FAIL（改为 INFO："projects 由 server 下发；当前生效 N 个（读自 policy 缓存，worker 未运行时可能为空）"）
  - 新增 `roots` 检查（`to` 目录存在、`from` 非空、无重复 `from`）
  - `guards` 未设 → **WARN**（不 FAIL）："护栏未设置 = 不额外收紧；建议显式声明"

**T6-D `config/worker.example.yaml` 重写**：删 `projects` 段（留一条迁移注释）、加 `roots` / `guards`（含缺省语义注释）。

**验收（T6）**：验收 12（`gofer project list` / `gofer config validate` 在无 `projects` 段的 worker 上正常）+ 验收 10 第三格（v3 server 实测，断言告警文案）。

**提交**：`feat(worker): retire worker.yaml projects section with a downgrade path (P3 T6)`

---

### T7 e2e 冒烟（隔离栈，**不碰 live**）

```txt
🔴 红线（承接 P1/P2 T6）：隔离端口（serve :18899）、【绝不 pkill gofer】（会打死 live LIVE-PORT）、
   【绝不 pnpm build】（会热更 live 控制台）、serve 显式 --web-dir、只 kill 自己起的 PID。

1. 【验收 1】现网形态 worker.yaml（有 projects、无 roots/guards）
     → 旧(4bce415)二进制起一次记 /v1/meta → 换 P3 二进制、配置零改动 → 【diff 必须为空】
     → 原 project 提 exec job + tty-claude 交互 job → 仍跑通
2. 【验收 2】worker.yaml 换成 P3 形态（roots + guards，无 projects）
     → server 加 project（路径落在 root 下 + allowed_runners 列该 worker 的 runner）→ kill -HUP <serve pid>
     → /v1/meta 出现新 key；【worker PID 不变】（pgrep 前后逐字比对）
     → 提交 job → 跑通；stdout 的 pwd = 【映射后的本机路径】
3. 【验收 3】server 加一个路径不在任何 root 下的 project → Applied.Rejected 有它；worker projects 里没有它
4. 【验收 4】kill -HUP <worker pid> → projects 【仍是那 N 个】（不是 0）
5. 【验收 5】worker.yaml 加 root → gofer worker reload → 步骤 3 被拒的 project 变成 accepted；PID 不变
6. 【验收 6】guards.allow_exec:false → exec job 被拒 + Degraded 有它；改回 true → 跑通
7. 【验收 7】没写 allowed_runners 的 project → 任何 worker 的 projects 里【都没有】
8. 【验收 8】policy 给 [claude, tty-codex]（本机没装 codex）→ worker 的 AllowedAgents 逐字相等；提 tty-codex → 明确报错
9. 【验收 10】滚动矩阵：v3 worker(c3ee6d1) / v2 worker(4def378) / 【v3 server(c3ee6d1) + v4 worker】
     ← 第三格断言【醒目告警文案】+ worker 不崩、仍在线
10.【验收 12】gofer project list / gofer config validate 在 P3 形态 worker 上正常
11.【验收 11】go list -deps ./internal/wshub | grep gofer → 只有 internal/wsproto
```

**提交**：冒烟脚本存 `tmp/smoke-p3/`（可复跑），通过后收尾。

---

## 5. 风险与对策

| 风险 | 对策 |
|---|---|
| 🔴 **SIGHUP 把 project 清空 → worker 静默停摆**（P3 最容易翻车的一处） | **T5-A** worker 内存持有 `lastPolicy`，两条 reload 路径共用同一 projection + 同一串行 executor；**验收 4 先证伪** |
| 🔴 **发布纪律：v4 worker（已删 projects）+ v3 server → 一个 project 都没有** | **T6-B** 降级路径 + 醒目告警（靠 T2 给 `Registered` 补的 `ProtocolVersion`）；迁移文档写死**"server 必须先升"**；验收 10 第三格实测 |
| 🔴 **roots 映射失败产出空 `HostPath` → job 散落到进程 CWD** | **T5-D** 映射不到 = **整条不进配置**；`filepath.Abs("")` = CWD 的同款坑 `workerOnlyProject` 注释里记着；**验收 3 先证伪** |
| 🔴 **白名单交集：`AllowedAgents` 空 = 放行全部** → 一个"收紧"动作静默放开所有 agent | **T5-D 不做交集**（原样透传 + guards 收紧）；**验收 8** 逐字断言。这也顺带消掉了"交集必须在 `ReloadWith` 之后算"的顺序陷阱 |
| **"好心"把空 `allowed_runners` 当通配全推** | **T3** 反向测试（验收 7）；设计 §4-D4′ / §11-Q8 已写死 |
| **`guards` 用裸 `bool` → 升级即把现网 exec/pty 全禁** | **T1-D** `*bool`（nil = 不额外收紧）；**验收 1 先证伪** |
| **Rev 跨 server 重启回退 → worker 永久丢弃新 Policy** | **T5-C** Rev per-connection、register 时清零；验收 10 最后一格（多 URL 混合）实测 |
| **`(cfg, rev)` 分两次读 → `(旧cfg, 新rev)` → 永久卡在旧配置** | **T0-A** 一次原子读；`-race` 测试断言同代 |
| **Policy 计算写进 wshub → 破 G022** | **T0-B** seam 注入；**验收 11** 用 `go list -deps` 逐字证明 |
| **Policy apply 与 SIGHUP reload 并发 → 旧配置覆盖新配置** | **T5-B** 进 P1 既有的串行 `reloadCh`（P1 T3 已经为 reload 解过一次，别再犯） |
| **`Applied` 另起一条能力上报通路 → server 出现两个能力真源** | **T2/T4-C** `Applied` 内嵌 `*Caps`，走**同一个** `reg.UpdateCaps`；`Rejected`/`Degraded` 只做诊断 |
| **旧连接迟到的 Applied 污染新连接** | **T4-C** 复用 `UpdateCaps` 的 `r.conns[wc.workerID] != wc` 检查，**不新写一套** |
| **v3 worker 被误标 policy_pending → 永久锁死** | **T4-D** 只对 `SupportsPolicy(proto)` 的 worker 置 pending；回归测试 |
| **worker 机器上 `gofer project list` / `config validate` 塌掉** | **T6-C** policy 缓存文件 + doctor 改判据；验收 12 |
| **`Policy.Agents` 被"顺手"加回来** | **T2 边界写死**；code review checklist：Policy 只带 project 元数据与白名单 |
| 重连重跑 detect（Rev per-connection 的代价） | 接受（P2 实测最坏 2.0s、典型 ms）；不做 payload 指纹去重（§6） |

---

## 6. 不做（明确排除）

- **`Policy.Agents` / `guards.allow_custom_agents`** —— Q6 砍掉（设计 §11）。它是 D1 边界上唯一的破口，且 P2 的 worker.yaml `agents:` 逃生舱已覆盖它的全部能力。
- **白名单与 `cfg.Agents` 的交集** —— 见 T5-D 边界。透传的准入结果一样，还少两个陷阱。
- **"空 `allowed_runners` = 全推"** —— Q8：语义与 `checkRunnerAllowed` 一致，改了会打架。
- **roots 的远程改写 / API 暴露** —— D3 推论：加 root = 扩大该机可执行范围，**故意**要求机器访问权。
- **Policy payload 指纹去重（重连时跳过 re-apply）** —— 先按 T5-C 的"每次重连重投影"做，简单且正确；**测出来是问题再优化**。
- **`gofer worker show` / `worker projects <id>` CLI** —— **今天不存在**，P4 建。P3 的可观测面 = policy 缓存文件 + `/v1/meta`。
- **Cluster 页展示 rejected/degraded/policy_rev** —— P4（P3 只把数据吐到 `/v1/meta`）。
- **`projects.<key>.worker_labels` 为池型 runner 收紧** —— P4，不影响正确性。
- **`workerOnlyProject` placeholder 的代码改动** —— 触发条件（请求的 key 不在 host `cfg.Projects` 里）天然还在，**零改动复用**。

---

## 7. 提交节奏（SR1202）

**T0 必须第一个做且单独成 commit**（地基：原子快照 + seam）。
之后 **T1 ∥ T2**（互不依赖，可并行）→ **T3**（需 T0+T2）→ **T4**（需 T2+T3）→ **T5**（需 T1+T2）→ **T6**（需 T5）→ **T7**。
每步单独 commit，每步 `go test ./... -p 1 -count=1` + `go vet` 绿；T0/T4/T5 额外跑 `-race`。

---

## 8. 进度跟进

- [ ] **T0 地基**：`(cfg, rev)` 原子快照（T0-A，顺带关 bd `tools-cg4`）+ hub PolicySource seam（T0-B，验收 11）
- [ ] **T1** worker.yaml `roots` + `guards`：字段 / defaults / validate / **最长前缀映射** / `*bool` 缺省语义（T1-D，验收 1 先证伪）
- [ ] **T2** proto v4：`Policy` / `Applied{*Caps}` / `Registered{ProtocolVersion, Policy}` / `PolicyMinProtocolVersion=4`
- [ ] **T3** server 推送目标计算（D4′；**验收 7 反向测试先证伪**）
- [ ] **T4** hub：ack 带 Policy（Q7-b）+ reload 广播 + `TypeApplied` → `reg.UpdateCaps` + policy_pending（Q7-a）
- [ ] **T5** worker 投影 + 应用：`lastPolicy`（**验收 4 先证伪**）/ 进 P1 串行队列 / roots 映射（**验收 3 先证伪**）/ **白名单透传不交集**（验收 8）/ `Applied` 回报 / policy 缓存
- [ ] **T6** `projects` 段退役 + 降级路径（**server 先升**）+ `project list` / `config validate` / `worker.example.yaml` 配套
- [ ] **T7** e2e 冒烟（11 条，隔离栈；红线：不碰 live LIVE-PORT、不 `pkill`、不 `pnpm build`）
- [ ] 迁移文档：worker.yaml 从 `projects` 迁到 `roots`；**发布纪律：server 先升**；`guards` 建议显式声明
