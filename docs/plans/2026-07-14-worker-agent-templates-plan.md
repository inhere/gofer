# P2：内置 agent 模板 + detect 上报 实施计划

> 设计：[`docs/design/2026-07-13-worker-config-federation-design.md`](../design/2026-07-13-worker-config-federation-design.md)（v0.5）
> 总纲：[`docs/plans/2026-07-13-worker-config-federation-plan.md`](2026-07-13-worker-config-federation-plan.md)（P1 已完成）
> bd epic：`tools-5pq`

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-07-14 | Claude | 初版（基于代码调研，推翻设计文档 3 条前提）。**未开工即被推翻，见 v0.2** |
| v0.2.1 | 2026-07-14 | Claude | 三处待定决策落定（用户确认）：①`AgentBrief.Available` 用 `*bool`、**不 bump 协议**（v4 仍留给 P3 Policy）；②`localAgentCaps()` **不套 detect 门控**（语义差异写进注释）；③`tty-codex` **放进模板**（依据 `codex --help` 官方文本：裸 codex = 交互 CLI），但 T6 须实测 pty attach，跑不通当场撤 |
| v0.2 | 2026-07-14 | Claude | **实证 + 对抗式审查后重写**。v0.1 有 3 个 P0 缺陷：①只审了"读"路径、没审"写"路径——`config.Save` 会把模板 agent **永久固化**进用户 config.yaml（web 控制台点一次 project 编辑就触发），**P2 的安全模型被自己的写回路径抵消**；②merge 落点没定死，worker 启动会**双重 merge / 双重 detect**；③`gofer agent list` 走另一条装配路径，会漏模板（"这里看得到、那里跑不了"）。另：T3「不升协议」的**论证是错的**（用未知帧的证据去论证未知字段），真正的兼容缺口是 `missing bool → false` 无法与"显式不可用"区分。D13 已实测坐实（并修正其爆炸半径表述） |

---

## 目标

worker 不再手写 agent 定义：**内置模板 + detect 探测**上报「我这台机器上真实装了什么」。

**先把收益说准（v0.1 吹大了）**：
- ✅ **真实收益 = 免配置**：一台装了 claude 的新机器，worker.yaml **不写 agents 段**就能跑 claude / tty-claude。
- ⚠️ **不是安全收益**：D1 的"server 无法凭空定义任意命令"**今天就已成立**（worker 用自己的 config 解析 agent argv，server 根本不下发命令定义）。P2 **不新增**这条保证——它是要**保住**这条保证不被 P3 的 Policy 下发破坏。**但 v0.1 的方案反而会亲手打破它**（见 T0-A）。
- ⚠️ **收益有限定条件**：模板注入**不会**自动改 project 的 `interactive_allowed_agents` 白名单（`job/config.go:109`）。"零配置 worker 上 tty-claude 立刻可用"**只在 project 已允许该 agent 时成立**，必须写进文档，别让人以为全自动。

---

## 验收（先写死；每条都要能指出"怎么证明它真的成立"）

1. **🔴 现网零破坏（最高优先级）**：用**当前 live 形态**的 worker.yaml（`agents` 段有 `claude`/`tty-claude`/`tty-demo`，**无一带 `detect` 块**——已实证）——升级二进制后**零改动**：
   - `/v1/meta` 的 `agent_caps` **逐条 diff 为空**（一个都不许少）
   - 用这些 agent 提交 job **仍跑通**
   - > 实证背书：天真实现下 caps 会塌成 `[exec]`，`claude`/`tty-claude` 提交直接 **HTTP 400 `agent not on worker`**。见 `tmp/d13/`。
2. **🔴 模板不得被写回用户配置**（v0.1 的致命缺陷，见 T0-A）：materialize 之后，调用 `project.Add` / web 控制台「新增 project」/ `gofer project add` → **写回的 config.yaml 里不得出现任何模板 agent**（用户没写过的 key 一个都不许多）。
3. **merge 恰好一次**：worker 启动全链路（`workerConfigToConfig` → `core.Build`）只 detect 一次、只 merge 一次。**幂等断言**：merge 两次 == merge 一次。
4. **装配路径一致**：`gofer agent list` / `/v1/agents` / MCP `ListAgents` / worker caps / `/v1/runners` **看到的 agent 集一致**（不得出现"这里看得到、那里跑不了"）。
5. **零配置 worker**：worker.yaml **无 `agents` 段** → 装了 `claude` → caps 自动出现 `claude` + `tty-claude`；**没装** `codex` → `codex` **不出现**。
6. **逃生舱优先且永不被剔除**：worker.yaml 显式写 `claude: {command: /opt/my/claude}` → 生效的是**本机这条**（整条覆盖，不做字段级合并），且**即使探测失败也绝不从 caps 剔除**。
7. **`Available` 的语义不得被误用为准入**（v0.1 埋的第二颗雷）：旧 worker（无该字段）与"探测失败的逃生舱 agent"**都会**是 `Available=false`，但它们**都能跑**。→ 用 `*bool`（缺失 = unknown）并在 doc 里焊死"仅供展示，任何准入/过滤不得读它"。**测试**：旧 worker 的 agent 不得因此被过滤掉。
8. **detect 预算**：`gofer worker reload <id>` 端到端 **< 10s**（默认 HTTP 回执窗口），**即使所有探针都超时**也不得打穿。
9. **护栏 + 前端配套**：interactive agent 以非交互方式提交 → **明确报错**；且**前端非交互下拉里根本不出现** interactive agent（否则上线第一天就是"下拉能选、提交必挂"）。
10. **server 侧不塌**：server 删掉 `agents` 段后，(a) 装了 claude 的 server、(b) **没装**的 server —— 分别验证**打到 worker 的交互 job** 的行为（(b) 会 400 unknown agent，这是**预期**，但必须写进迁移文档）。
11. 全量 `go test ./... -p 1 -count=1` 绿（且**不得变成机器相关**——见 T0-D 的 Detector seam）；`go vet` 绿；关键包 `-race` 绿。

---

## 现状事实（已逐条核实；**推翻了设计文档 4 条前提 + v0.1 的 3 条**）

### 设计文档的错（M1–M4）

- **M1 `detect` 与"内置模板"已存在一半** —— 不是从零建。`config.AgentConfig.Detect`（`config/model.go:575,604-607`）、`agent.Registry.Detect()`（`agent/detect.go:16-61`，串行、每探针 5s）、`builtinExecAgent`（`agent/registry.go:27-33`）、`builtinSessionDefaults`（`registry.go:90-117`，按 command 基名 fallback —— 这正是 `tty-claude` 今天能白嫖 claude session 默认的机制）。
  **→ P2 的真实增量只有两件**：①内置从"只有 session 字段"扩成**完整定义**；②detect 从**按需查询**接到 **register/reload 的能力上报链**。
- **M2 ❌ 设计 §7.3 说 P1 的 reload 会"重跑 detect" —— 假的。** `workerCaps`（`commands/worker.go:342-350`）纯读配置，**P1 的 reload 路径上零 detect**（核实：`grep -n "Detect" internal/commands/worker.go` → 无命中）。
- **M3 ❌ worker 侧根本没有 `guards` / `roots` 字段。** `config.WorkerConfig`（`model.go:625-634`）只有 `worker_id/server_link/projects/agents/runners/max_concurrent/labels/storage`。P2/P3 是**新建**，不是"复用"。exec 今天的唯一闸是 **project 级** `allow_exec`（`job/config.go:141-143`）。
- **M4 ❌ 设计 §6.3 把 Policy 放在 proto v3 —— v3 已被 P1 的 reload 帧占用**（`wsproto/frames.go:16-40`）。**P3 的 Policy 只能是 v4**（若 P2 因 T3 而 bump 到 v4，**P3 顺延 v5**）。

### 🔴 D13：P2 唯一能打穿现网的点（**已实证，非推理**）

`Registry.Detect`（`detect.go:37-45`）现有语义：**agent 没配 `detect.command` → `Available=false`**（仅 `TypeExec` 豁免）。
**全仓 `detect:` 只出现在 `config/gofer.example.yaml` 一个文件；零个 worker 配置带 detect。**

**实证（`tmp/d13/`）**：给 `resolvedAgentKeys` 加 detect 过滤、编实验二进制、同一份 worker.yaml：

```
baseline: agents = ['claude', 'exec', 'tty-claude', 'tty-demo']
naive-P2: agents = ['exec']                       ← caps 塌陷
提交 agent=claude     → HTTP 400  agent "claude" not on worker
提交 agent=tty-claude → HTTP 400
提交 agent=exec       → HTTP 200 → done exit=0    ← 仅 exec 幸存
```

**决定性证据**：`claude` CLI 在那台机器上**装着**（v2.1.208，能跑），但 `/v1/agents` 仍报 `available:false` —— 这个 false **与装没装无关**，纯粹因为"配置里没有 detect 块"。

**爆炸半径的准确表述**（v0.1 措辞不精确，会误判）：
- ❌ "每一个 agent 消失" → ✅ **每一个 cli-agent 消失，`exec` 幸存**
- ❌ "拒掉所有 job" → ✅ **拒掉所有非 exec 的 job**
- ✅ **推论**：worker.yaml **没有 agents 段**的 worker（`w-host.local.yaml`）**毫发无伤**（本就只有 exec）。**会死的是有 agents 段的** —— 而 **live 的 `w-container-example` 正是**（已读实际配置坐实：`claude`/`tty-claude`/`tty-demo` 三个 cli-agent，无一带 detect）。

**铁律（写进代码注释与测试）**：**逃生舱 agent（operator 在配置里显式声明的）永不因探测失败被剔除；只有模板注入的 agent 受 detect 门控。**

### 🔴 v0.1 自己的错（对抗审查发现，A/B/C/D）

- **A [P0] materialize 进 `cfg.Agents` → 模板会被 `config.Save` 永久写进用户 config.yaml。**
  `project.NewRegistry(cfg, "")`（`core/core.go:74`）持有**同一个** `*config.Config` 指针；`Add()`/`Remove()`（`project/registry.go:73-107`）→ `save()` → `config.Save`；`config.render()`（`config/writer.go:15-25,62`）的 `managedTopKeys` **含 `"agents"`**，直接 `yaml.Marshal(cfg)` 整个落盘。
  **触发点不冷门**：`httpapi/project_handler.go:96,124`（**web 控制台建/改 project**）、`commands/project.go:284,341`。
  **后果**：模板 agent 被固化成显式配置 → 按铁律变成"逃生舱" → **detect 门控失效**；换机器/卸载 CLI 后配置仍宣称拥有该 agent；用户 config.yaml 凭空多出他从没写过的 agents 段。**P2 的存在理由被自己的写回路径抵消。**（已复现，见审查报告）
- **B [P0] worker 启动会双重 merge / 双重 detect。** `commands/worker.go:261`：`wcfg := workerConfigToConfig(wc)` 后紧接 `core.Build(wcfg)` —— v0.1 说两处都调 `MergeBuiltins`。且 `MergeBuiltins(cfg, detected)` 要求调用方先算好 `detected`，而 `core.Build(cfg)` 只收 cfg —— **detect 从哪来、谁跑，v0.1 根本没写**。更阴的是：第一遍 materialize 后，这些 key 在第二遍看来就是"config 已有 key" → **变成逃生舱语义，detect 门控只在第一遍生效**。
- **C [P0] `gofer agent list/detect/show` 走另一条装配路径。** `commands/agent.go:52-58` 的 `loadAgentRegistry` 直接 `config.Load` + `agent.NewRegistry`，**不经 `core.Build`** → 会漏模板 → "serve 能跑 claude，但运维用 `gofer agent list` 排障时看不到它"。
- **D [HIGH] T3「不升协议」的论证用错了证据。** v0.1 的论据（"双方 frame switch 无 default 分支"）说的是**未知帧**，与"已知帧里的未知字段"**无关**。结论碰巧成立，但靠的是另一条证据：`wsproto/envelope.go:82-89` 的 `As[T]` 是裸 `json.Unmarshal`、**没有 `DisallowUnknownFields`**。
  **真正的坑在反方向**：旧 worker 发的 `AgentBrief` 无 `available` → Go 解成 **`false`** → server **无法区分**"旧 worker 没这字段"与"新 worker 说不可用"。**叠加铁律**：探测失败的逃生舱 agent 也是 `Available=false` 但**能跑** → `Available=false` 天然 ≠ 不能跑。任何消费方拿它过滤/置灰，**既误伤旧 worker 的全部 agent，也误伤逃生舱 agent**。
  （现状安全：web 只在 `Agents.vue:93-116` 做展示不过滤，`NewJob.vue` 级联不读 available。但这只是默契，极易被后续 PR 破坏。）

### 其他必须知道的

- **全部 agent 枚举/解析路径**（实施时逐一勾，漏一个就不一致）：`agent/registry.go:68,176`、`job/capabilities.go:62`、`job/selector.go:69`、`httpapi/meta_handler.go:181`、`httpapi/runner_handler.go:272`、`httpapi/agent_handler.go:26`、`httpapi/config_handler.go:198`、`httpapi/project_handler.go:161,169`、`mcpserver/backend_local.go:66`、`commands/agent.go:52`、`project/registry.go:213`。**只有 `commands/agent.go` 完全脱离 core**（= C），其余都吃 cfg 快照。
- **`localAgentCaps()` 是 worker caps 的孪生路径**（`httpapi/runner_handler.go:272`）：读 `s.agents.List()` 原始注册表，**同样不过 detect**，而 live 的 **server 配置也没有 detect 块**。若把 detect 门控顺手套上去，`/v1/runners` 的 local 能力视图会一起塌（前端级联丢 agent）。**不会**导致 job 被拒（local 准入不走 caps 门）→ 是 UI 回归而非打穿，但**必须显式决策**，否则两条路径语义漂移。
- **`/v1/agents` 的 `available` 今天就是"假阴性"**：live server 的 agents 无 detect 块 → 今天就把 claude 报成 `available:false`，而 job 跑得好好的。P2 把它修准后，前端行为会**突变**（原本灰的忽然亮了/反之）。
- **准入门的顺序**：交互 job 依次撞 `not in interactive_allowed_agents` → `interactive agent must be no-raw-cmd and non-exec` → **最后**才是 `not on worker`。模板注入后若属性与 project 配置对不上，用户看到的**不是**真正的根因。
- `allowed_agents` **为空 = 允许全部已配置 agent**（`job/config.go:87`）→ 模板注入后，空 allowlist 的 project 会**自动获得**新 key（tty-*/opencode）→ 这是 T4 护栏必须存在的原因。
- **交互 job 由 host 解析 agent**（`job/config.go:102-111`）：`NewJob.vue:180-183` 的注释写死了"worker 独有的 agent 只能跑非交互 job"。→ **server 删掉 agents 段 + server 没装 claude = 打到 worker 的交互 job 全部 400**（验收 10）。
- **重连不重跑 detect**（利好，v0.1 没提）：worker caps 启动算一次后**缓存**（`worker/reload.go:167-177`、`client.go:450` 只读缓存）；hub 第一帧 register 用 **bare ctx、无读超时**（`wshub/hub.go:167-170`）→ detect 挂在 register 前不会拖挂启动。
- **LookPath 在 Windows 上对 node 系 CLI 安全**（已核实 Go 1.25 `lp_windows.go:122-139`）：PATHEXT 缺省含 `.cmd` → npm 装的 `claude.cmd` 找得到；且 `exec.Command` 与 `LookPath` 共用同一 lookPath → **LookPath 成功 ⇔ Command 能启动**。⚠️ 唯一缺口：**`.ps1` 不在默认 PATHEXT**（纯 PowerShell shim 会假阴性）。
- **exec 的免探只在 detect 为空时生效**（`detect.go:36-45`：**先**判 `Detect.Command == ""`）。而 `gofer.example.yaml:212-215` / `s-docker.local.yaml:56-59` **都给 exec 配了 `detect: sh -c true`** → 换成 LookPath 后 `LookPath("sh")` 在 Windows 上失败 → **内置 exec 报 unavailable**。
- `PtyCapable`（`worker/client.go:455`）= `ptyrunner.Available()`，是 **OS/构建级 pty 后端可用性**，与"装没装某个 agent"**无关**，别混。
- **现网已存在同名分歧**：`claude` 在 `gofer.example.yaml:195`（stream-json 三件套）与 `s-docker.local.yaml:48`（`-p {{prompt}}`）**args 不一致** → 模板**必须**允许本机整条覆盖。
- **`tty-codex` 全仓没有任何定义**（设计点名了它，但无参考实现）。唯一线索：codex 交互 resume 是 `resume {{session_id}}`（`registry.go:99-116`，注释标"实测确认"）。

---

## 任务分解

### 🔴 T0 地基：merge 的唯一落点 + 写回隔离（**必须第一个做**，A/B/C/J 一次解决）

v0.1 把这些当成 T1 的实现细节，结果 3 个 P0 全在这里。**先把地基焊死，模板才敢注入。**

**T0-A 写回隔离（不解这条，P2 的安全模型是假的）**

```go
// config.Config 加一个 yaml:"-" 的字段，记录「哪些 agent key 是运行时注入的、不属于用户配置」
type Config struct {
    ...
    injectedAgents map[string]bool `yaml:"-"`   // 非用户配置，config.render() 落盘前必须剔除
}
```
`config.render()`（`config/writer.go:62`）在 `yaml.Marshal` 前，**从副本**里 delete 掉这些 key。

**验收（T0-A，就是验收 2）**：materialize 之后调 `project.Add` → **写回的 yaml 里不得出现任何模板 agent**。**先证伪**：不加这个隔离时，该测试必须**真的**在 yaml 里看到模板 agent（审查已复现过一次，照着写）。

**T0-B merge 恰好一次**

```go
// detect 由外部注入（可 fake）；Build 独占 merge，workerConfigToConfig 不 merge。
func Build(cfg *config.Config, opts ...BuildOption) (*Core, error)
func WithAgentDetector(d agent.Detector) BuildOption
```
- worker（`commands/worker.go:261`）：`workerConfigToConfig` **只转结构**，merge 交给紧随其后的 `core.Build`。
- **幂等断言测试**（验收 3）：merge 两次 == merge 一次；且全链路 detect **只跑一次**（用计数 fake）。

**T0-C 装配路径收敛**

把「detect + merge」收敛成**一个** `config → resolved config` 的函数，**所有**装配点统一调用：`core.Build` / `commands/agent.go:loadAgentRegistry`（C）/ `mcpserver` / worker。
**验收（验收 4）**：`gofer agent list` 与 `/v1/agents` 与 worker caps 看到的 agent 集**一致**。

**T0-D Detector seam（防验收 11 变成机器相关）**

`core.Build` 必须能注入 fake detector —— 否则走 `core.Build` 的既有测试（`core/worker_wiring_test.go:82`、`serve/cast_test.go:104`、`worker/pty_e2e_test.go:130`、`worker/pty_cast_e2e_test.go:66`）会**依赖宿主 PATH**（开发机装了 claude → `cfg.Agents` 多出模板 key + 起 `--version` 子进程），`go test ./...` 的结果就变成机器相关了。

### T1 内置模板注册表（`internal/agent`）

`builtinTemplates`：**完整定义**（type / command / args / interactive / no_raw_cmd / detect），覆盖 `claude` / `codex` / `opencode` / `tty-claude` / `tty-codex`（`exec` 已内置，**不要重复定义**）。

- **`tty-codex` 放**（决策已定，2026-07-14）。依据：`codex --help` 官方文本 —— **"If no subcommand is specified, options will be forwarded to the interactive CLI"** → 裸 `codex` 即交互 CLI，与 `tty-claude` 形态对称（裸命令 + `interactive: true` + `no_raw_cmd: true`）；且 `codex resume` 子命令存在，印证 `registry.go:99-116` 的交互 resume 模板。
  ⚠️ **但 T6 必须实测 pty attach 跑通一次**（`--help` 说它是交互 CLI ≠ 它在 pty 里真能跑起来）。**跑不通就当场从模板撤掉**，不留半成品。
- **同名冲突 = 逃生舱整条赢**：模板只在 key **不存在**时注入。**不做字段级合并**——`Interactive`/`NoRawCmd` 是 bool，**无法区分「未设」与「显式 false」**，字段级合并会让 `tty-claude: {command: /opt/claude}` 意外继承 `interactive=true`，或反过来永远关不掉。（session/system 字段的字段级兜底**已由 `applySessionDefaults` 覆盖**，别再叠一层。）
- 代价（写进文档）：整条覆盖意味着"只想改 command 路径"也得把 args 抄一遍。**不做 `command_path` 窄覆盖**（新配置面，收益不明）。

### T2 detect 重构：LookPath 定"能不能跑"，`--version` 只做 best-effort

```txt
if ac.Type == TypeExec { return Available:true }      // ★ 必须【先于】任何 detect 配置判断（否则 Windows 上 LookPath("sh") 失败 → 内置 exec 报 unavailable）
Available = exec.LookPath(command) 成功
Version   = best-effort 跑 detect.command/args（缺省 `<command> --version`）；失败只丢 version、【不改 Available】
```

**为什么不能用子进程退出码定 Available**：慢启动 / 首次运行向导 / 网络鉴权（node 系 CLI 常见）会拖成**假阴性**，而假阴性 = agent 从 caps 消失 = job 被拒。

**预算（验收 8）**：今天 detect 是**串行 + 每个 5s**（`detect.go:11`）→ 6 个模板最坏 **30s**，会把 reload 的 **10s** 回执（`httpapi/worker_reload_handler.go:20-23`）打成 504（更糟：handler 注释明说 "the worker may still apply it" —— CLI 报超时但配置其实生效，最差的运维体验）。
→ **并行 fan-out** + **每探针 ≤2s** + **整体 ≤3s 上限**。纯 LookPath 路径接近 0 耗时。
**测试**：所有探针 hang 住 → 整体仍 ≤3s 返回（fake 探针 + 计时断言）；LookPath 成功但 `--version` 超时 → **`Available=true` + version 空**（这条最关键，它就是防假阴性的）。

### T3 接进能力上报链（register + reload）

- `workerCaps`（`commands/worker.go:342-350`）→ 「读配置 + 跑 detect + materialize 模板」，产出最终 caps。
- **不变量（承接 P1 T3）**：`ReloadWith(ncfg)` 与"从 ncfg 算能力"用**同一份 ncfg + 同一次 detect 结果**（所报即所受）。**不许 detect 两次。**
- **`AgentBrief` 追加 `Available *bool` + `Version string`**（**`*bool`，不是 `bool`** —— 缺失 = unknown，可与"显式不可用"区分，见 D）。
- **doc 注释里焊死**：「`Available` **仅供展示**。任何准入 / 过滤 / 置灰**不得**读它 —— 旧 worker（无此字段）与探测失败的逃生舱 agent **都是 false，但都能跑**。」并进 review checklist。
- **协议版本：不 bump**（决策已定，2026-07-14）。`*bool` 方案下无需升版（`As[T]` 是裸 `json.Unmarshal`，无 `DisallowUnknownFields`）→ **v4 仍留给 P3 的 Policy**（M4）。
- **测试（验收 7）**：旧 worker（不发该字段）的 agent **不得**被任何消费方过滤掉。

### T4 护栏：interactive agent 不得非交互提交（**修既有真 bug**）+ 前端配套

**既有 bug（已复现）**：validate 只在 `req.Interactive` 分支里检查 `ac.Interactive`（`job/config.go:93-118`），**没有反向检查** → adapter 用空 Args 渲染（`adapter.go:78-91`）→ **prompt 被静默丢弃**（实测 `resolved argv = ["claude"]`，prompt 消失）→ 跑裸 `claude` 挂到超时。

**落点必须写清**（v0.1 只说"加 3 行"）：**放 `!remote` 块内、基于 `gateAgent`**。
- 放 `!remote` 内 → worker/peer job 不检查（host 本就不解析远端 agent，合理，但**必须写出来**）。
- ❌ 放顶层用 `ResolveAgent(cfg, gateAgent)` → 远端 job 会拿 **host 的**同名 agent 定义去判 worker 上的 agent（联邦口径漂移）。

**前端配套（不做就是"下拉能选、提交必挂"）**：`NewJob.vue:208-233` 的**非交互分支根本没有 `!a.interactive` 过滤**，而 `metaAgents` 会把 tty-* 一并吐出；叠加空 allowlist 自动放行 → 模板注入后**所有空白名单 project 的非交互下拉都会多出 tty-claude**。
→ 加 `if (!interactive.value) list = list.filter(a => !a.interactive)` + 同步 `agentEmptyReason`。

**已核实不会误伤**（写进计划，免得实施者当回归回滚）：
- **resume 不误伤**：`job/resume.go:107` `Interactive: src.Interactive` —— 交互源 resume 恒为 `Interactive=true`。**补一条 resume 回归测试**。
- **workflow step**：`workflow/submit.go:358 rejectInteractiveStepRequest` 已强制 step 非交互 → step 指定 tty-* 会被 T4 拒，**合理**。

**验收（T4）**：**先证伪** —— 加护栏前，非交互提交 tty-claude 必须复现"prompt 被静默丢弃"（断言 argv 里没有 prompt）；加护栏后变成明确报错。

### T5 探测缓存（`/v1/agents` + **MCP `ListAgents`**）

`httpapi/agent_handler.go:26-44` 与 `mcpserver/backend_local.go:66-76` **都是**每请求给每个（配了 detect 的）agent 起子进程。**MCP 的 `ListAgents` 是 agent 反复调的工具，放大效应比 web 刷新更严重**（v0.1 漏了它）。

→ **缓存做在 `agent.Registry` 上**（`Registry.Availability()`，随 `Reload` 一起 atomic swap），来源 = T0-B 那唯一一次 detect。两个消费点同时受益。
**G022 边界已核实安全**：`httpapi` **已经**依赖 `internal/agent`（`go list -deps ./internal/httpapi | grep internal/agent` 有命中），且 `grep -c wshub` **为 0** —— 缓存做在 agent 包**零新增反向依赖**，不碰 P1 T5 刚收窄的边界。

### T6 e2e 冒烟（隔离栈，**不碰 live**）

```txt
1. 【🔴 验收 1】现存形态 worker.yaml（agents 有 claude+tty-claude+tty-demo、无 detect）
   → 旧二进制起一次记下 agent_caps → 换 P2 二进制、配置零改动 → 【diff 必须为空】
   → 用 tty-claude 提交交互 job → 仍跑通
2. 【🔴 验收 2】materialize 后走一次 web「新增 project」→ 读回 config.yaml → 【不得出现模板 agent】
3. 【验收 5】worker.yaml 删掉 agents 段 → reload → claude/tty-claude 自动出现；codex 不出现（本机没装）
4. 【验收 6】worker.yaml 写 claude: {command: /opt/fake/claude}（探测必失败）→ 【仍在 caps 里】
5. 【验收 8】reload 端到端计时 < 10s（贴真实耗时）
6. 【验收 9】非交互提交 tty-claude → 明确报错；前端非交互下拉里【看不到】tty-claude
7. 【验收 10】server 删 agents 段 × (装/没装 claude) → 打到 worker 的【交互】job 行为（(b) 400 是预期，写进迁移文档）
8. 【验收 4】gofer agent list / /v1/agents / worker caps / /v1/runners 的 agent 集一致
9. 【tty-codex 保险】pty attach 实测跑通裸 `codex` 交互会话 —— **跑不通就从 T1 模板里撤掉 tty-codex**
10. proto=2 旧 worker 仍能连、能干活（复用 P1 的 git worktree 编旧二进制手法）
```

**红线（承接 P1 T6）**：隔离端口、**绝不 `pkill gofer`**（会打死 live LIVE-PORT）、**绝不 `pnpm build`**（会热更 live 控制台）、serve 显式 `--web-dir`、只 kill 自己起的 PID。

---

## 风险与对策

| 风险 | 对策 |
|---|---|
| 🔴 **模板被 `config.Save` 固化进用户配置 → 安全模型自我抵消** | **T0-A** injected-keys 落盘剔除 + 验收 2（先证伪） |
| 🔴 **现存 worker.yaml 的 cli-agent 全部从 caps 消失**（D13，已实证 live 中招） | **逃生舱铁律** + 验收 1 的 diff-must-be-empty |
| 🔴 双重 merge / 双重 detect → 门控只在第一遍生效 | **T0-B** merge 恰好一次 + 幂等断言 |
| `gofer agent list` 漏模板 → 排障信错源 | **T0-C** 装配路径收敛 + 验收 4 |
| `Available=false` 被误用为准入 → 误伤旧 worker + 逃生舱 agent | **T3** `*bool` + doc 焊死 + 验收 7 |
| detect 把 reload 打成 504 | **T2** LookPath + 并行 + ≤2s/探针 + ≤3s 上限 |
| detect 假阴性（慢启动/鉴权 CLI） | Available 只看 LookPath，不看子进程退出码 |
| **Windows 上内置 exec 被探成 unavailable** | **T2** `Type==exec → Available=true` **先于**任何 detect 判断 |
| 模板注入 → 非交互下拉多出 tty-* → 提交必挂 | **T4** 前端配套过滤（不做就是上线第一天的事故） |
| server 删 agents 段 + 没装 claude → 交互 job 全 400 | **验收 10** + 迁移文档写明（交互 job 由 host 解析 agent） |
| detect 让既有测试变成机器相关 | **T0-D** Detector seam，测试传 fake |
| `.ps1` shim 不在默认 PATHEXT → LookPath 假阴性 | 已知缺口，文档标注；逃生舱可显式配绝对路径绕过 |
| `tty-codex` 无实机验证 | 已有官方文档级证据（裸 codex = 交互 CLI）；**T6 实测 pty attach，跑不通当场撤模板** |

## 不做（明确排除）

- **`guards.allow_custom_agents` 不在 P2 加**。它把关的是 `Policy.Agents`（server 下发的自定义 agent 定义），而 **P2 阶段 server 根本不下发任何 agent 定义**（Policy 帧属 P3）。P2 加它 = 恒为 false、无人读的 dead config，还会误导运维以为下发通道已存在。**与 `guards.allow_exec` / `allow_interactive` / `roots` 一起留到 P3。**
- **`localAgentCaps()` 不套 detect 门控**（决策已定，2026-07-14）：它是 worker caps 的孪生路径，套上去会让 `/v1/runners` 的 local 能力视图塌（前端级联丢 agent），且**不会**导致 job 被拒（local 准入不走 caps 门）。→ **本期显式决策：local 侧只做 materialize，不做 detect 剔除**；语义差异写进注释，避免两条路径无声漂移。
- `command_path` 之类的窄覆盖配置面。

## 提交节奏（SR1202）

**T0 必须第一个做且单独成 commit**（地基，A/B/C/D 一次解决）。之后 T1 → T2 → T3 → T4 → T5 每步单独 commit（每步 `go test ./... -p 1` 绿），T6 冒烟通过后收尾。

## 进度跟进

- [x] **T0 地基**：写回隔离（A）+ merge 恰好一次（B）+ 装配路径收敛（C）+ Detector seam（D） — commit `440b205`
  - 契约（T1/T2/T3 照此写）：`agent.Detector{ Detect(map[string]config.AgentConfig) map[string]DetectResult }`（一次拿到全部候选，实现方自己并行 + 自己控总预算）；`agent.Resolve(cfg, d) (*config.Config, map[string]DetectResult)` = detect+merge 唯一入口，**原地改 cfg**、幂等；`core.Build(cfg, core.WithAgentDetector(d))`。
  - merge 唯一落点：`core.Build`（启动）+ `core.ReloadWith`（每个新快照一次）；`workerConfigToConfig` 保持纯结构映射、不 merge。
  - **修正计划的一处事实**：脱离 core 的装配点**不止 `commands/agent.go` 一个** —— `commands/project.go:loadRegistry`（→`project.Registry.Validate`→`agentDefined`）与 `commands/config.go` worker doctor 同样直接吃裸 `config.Load`；三处均已收敛到 `agent.Resolve`。
  - T2 注意：`DefaultDetector()` 目前是最小 LookPath 实现（无 version、无并行/预算），按 T2 替换其实现即可，接口不变。
- [ ] T1 内置模板注册表（逃生舱整条优先；tty-codex 本期不放）
- [ ] T2 detect 重构（exec 免探前置 / LookPath 定 Available / 并行 ≤2s、整体 ≤3s）
- [ ] T3 接进 register/reload 上报链 + `AgentBrief.Available *bool`（doc 焊死"不得用于准入"）
- [ ] T4 护栏（`!remote` 块内、基于 `gateAgent`）+ **前端非交互下拉过滤** + resume 回归测试
- [ ] T5 探测缓存（`agent.Registry.Availability()`；覆盖 `/v1/agents` **与 MCP `ListAgents`**）
- [ ] T6 e2e 冒烟（验收 1 的 diff-must-be-empty 与 验收 2 的 config 写回是最高优先级）
