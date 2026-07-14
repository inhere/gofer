# worker 配置远程化设计（worker=能力提供方，server=策略权威）

> bd: `tools-5pq`（epic）。前置：`docs/design/2026-07-09-config-federation-design.md`（xu64.10，解决了反方向的重复定义）。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-07-13 | Claude | 初版：问题定义 + 权威模型 + 分阶段。待审 |
| v0.2 | 2026-07-13 | Claude | 定稿 Q1(前缀最长匹配) / Q2(自动接受) / Q4(保留逃生舱, Policy 优先)；新增 D3：策略走**下发**而非远程改写 worker 配置文件（附理由）。 |
| v0.3 | 2026-07-13 | Claude | **推翻 Q4**：worker 端零 project 配置（逃生舱与 D3「单一真源」自相矛盾，且 worker-only project 本就是「配置写两遍」的绕行方案，本设计从根上解决后它变成分裂的来源）。新增 D4：**放置由 roots 推导**——server 全量推目录、worker 用 roots 最长前缀映射自筛，取代 v0.2 里「按 allowed_runners 算该推给谁」（labels 型 runner 下 server 根本算不出）。 |
| v0.4 | 2026-07-13 | Claude | **修正 D4 的过度简化**：roots 只回答「能不能跑」（能力），「准不准跑」（策略）仍由 `allowed_runners` 表达——共享盘下多台 worker 都能映射同一路径，但可能只准一台跑。推送目标改为**按 runner 可达性算**（pin 型精确到那台；池型才退化成全推），再由 roots 自筛。新增 D5：web 表单按 project 收窄 runner（今天的 agent 收窄的镜像，现有能力视图即可实现）。 |
| v0.5 | 2026-07-14 | Claude | **对抗式审查（host codex）后的修正**。v0.4 的 D4′ 建立在一个**错误前提**上：pin 并不是硬授权，显式 `worker_id` / `worker_labels` 可以覆盖它（已实测复现）。已先修代码（`1e69ff5`，pin=授权）使 D4′ 成立。另补：D6 Policy→worker 本地执行配置的**投影规范**（原文只说「记下路径」，按此实施会在 `dispatch.go → Submit → validate` 处失败）；§8 安全承诺**降级**为实事求是的表述（token 绑定只防冒充，不防被攻破的 server）。 |
| v0.6 | 2026-07-14 | Claude | **P1/P2 落地后的对账（基线 `c3ee6d1`）。改掉两条硬错误 + 补全核实**：<br>① **Policy 帧改 proto v4**（原写 v3）——v3 已被 P1 的 reload 帧占用（`CurrentProtocolVersion=3` / `ReloadMinProtocolVersion=3`），P3 须新增独立的 `PolicyMinProtocolVersion=4` 门。<br>② **`roots` / `guards` 是 P3 新建字段，不是"复用 worker 已有的东西"**——`config.WorkerConfig` 今天根本没有这两个字段（原文多处把它们当既有事实）。<br>③ §6.3「必须拆版本闸」**已由 P1 完成**，不再是 P3 任务；§7.1 的 detect、§4-D1 的内置模板**已由 P2 完成**。<br>④ **D6 的「agent 定义」一行按 P2 事实重写**：`agent.Resolve` 已在 `core.Build`/`ReloadWith` 里完成「内置模板 ∩ detect ∪ 逃生舱」的物化——Policy 投影**不得**自己拼 `cfg.Agents`，只能交给 `ReloadWith` 跑 Resolve。<br>⑤ **`Applied` 帧不另起炉灶**：P1 已有 `Caps` 帧 + `reg.UpdateCaps` 这条唯一的能力视图更新通路，Applied 须内嵌 `*Caps`（照 `ReloadResult` 的先例），否则 server 端出现两个能力真源——正是本设计自己批判的东西。<br>⑥ 新增 §13**代码事实核实表**（每条断言附核实命令）；行号全部重测并锚定到 `c3ee6d1`。<br>⑦ 新增 §11 三个**必须先答的待确认**（Policy.Agents 与 D1 自相矛盾 / register→Policy 空窗期 / `allowed_runners` 为空的推送语义）。 |

> **基线**：本文所有代码事实核实于 `c3ee6d1`（P2 完成态），核实命令见 §13。行号会漂——**以 §13 的命令为准，不以行号为准**。

## 1. 概览

配好一个 worker 节点后，**新增 project、开启 pty agent 都必须登录那台机器改 `worker.yaml` 并重启进程**——控制面完全够不着。实际后果：配了多台 worker，却只有 server 本机的一两个 project 能用起来，其余 worker「看得见、用不了」。

本设计把 worker 从「既管能力又管策略」收敛为**只管能力**，策略上收到 server（server 已有 SIGHUP 热重载与 `project add` CLI）。目标状态：**新增 project 到某 worker、允许它用 tty agent，全程只改 server，不登录 worker、不重启 worker 进程。**

## 2. 名词

- **能力（capability）**：这台机器上客观存在的东西——哪些目录可执行、装了哪些 agent 二进制、并发上限。只有 worker 知道。
- **策略（policy）**：允许谁用什么——哪个 project 跑在哪台 worker、可用哪些 agent、能否 exec / 开 pty。集中、高频变更。
- **护栏（guard）**：worker 本地设定、**server 无法远程放宽**的硬约束（路径根、allow_exec、allow_interactive）。
- **内置 agent 模板**：代码里预置的已知 agent 定义（command + argv + detect + session 默认），worker 只探测「装没装」，不再手写定义。

## 3. 问题分析（代码事实，@`c3ee6d1` 复核）

1. ~~**worker 无热重载**~~ ✅ **P1 已解决**。今天 `internal/worker/serve.go` 已注册 SIGHUP（`notifyReloadSignal` → `cl.enqueueReload`），并有 hub 下发的 `reload` 帧（`internal/wshub/reload.go`）。server 侧 `startReloadLoop` 在 `internal/serve/serve.go:829`（调用点 `:193`）——**不是原文写的 `:846`**。（核实 §13-C1）
2. **project 必须在 worker 上重复定义** —— **仍然成立，是 P3 要解的核心**。派发后 worker 用**自己的 config** 重新 `Submit`（`internal/worker/dispatch.go:46`，`Runner: builtinLocalRunner`）——project 的执行目录、agent argv、`allow_exec`、`interactive_allowed_agents` 全在 worker 侧解析与校验。server 上配了不算数。（核实 §13-C2）
3. **联邦只做了一半** —— 仍成立。xu64.10 解决的是「worker-only project 不必在 server 重复定义」；反过来「server 定义的 project 要在每台 worker 重复定义、且只能手工」没有解。
4. **（v0.6 新增）worker 的 project 来源只有 worker.yaml 一处**：`workerConfigToConfig`（`internal/commands/worker.go:439`）把 `wc.Projects` 原样搬进 `config.Config.Projects`；能力上报 `workerCaps`（`:393`）的 `Projects: mapKeys(wc.Projects)`。**去掉 `projects` 段后这两处会同时变成空** —— P3 必须同时给出替代来源（这正是 D6 投影要填的洞），否则 worker 一个 project 都跑不了、也不上报任何 project。（核实 §13-C3）

> 实证：2026-07-13 为容器 worker 加一个 `tty-claude`，需要改 worker.yaml 两处（`agents` 定义 + 该 project 的 `interactive_allowed_agents`）并重启 worker 两次；第一次还因为漏了 `interactive_allowed_agents` 被 worker 第二道 validate 拒掉。
> （其中「`agents` 定义」这一半**已被 P2 消除**——内置模板 + detect 自动物化，见 D1。剩下的一半正是 P3。）

## 4. 已确认决策

- **D1 权威模型**：**server 推策略 + worker 内置 agent 模板**。worker 不再手写 agent 定义；内置模板 + `detect` 探测上报「我这装了什么」。server 只决定「哪个 project 能用哪些**已探测到**的 agent、能否 exec、能否 pty」。
  - 安全含义：server 无法凭空定义任意命令让 worker 执行 → 即便 server 被攻破，活动范围受限于 worker 上真实安装的 agent + worker 护栏。（`exec` agent 本身仍是任意命令，由 worker 的 `allow_exec` 护栏把关。）
  - 被否决：server 全权推送（含 agent command 定义）——那等价于对 worker 的完全 RCE，`allow_exec` 护栏形同虚设。
  - ✅ **「worker 内置 agent 模板」这一半已由 P2 落地**：`internal/agent/templates.go`（`builtinTemplates`）+ `internal/agent/resolve.go`（`Resolve` = 探测 → 只把**探到的**模板注入 `cfg.Agents`；operator 在 config 里手写的 agent 是**逃生舱**，永不因探测失败被摘）。`core.Build`（`internal/core/core.go:118`）与 `core.ReloadWith`（`:338`）是仅有的两个调用点。**P3 不要重做这一层**，只消费它。（核实 §13-C4）
  - ⚠️ 与 §6.3 的 `Policy.Agents` + `guards.allow_custom_agents` **直接冲突** —— 见 §11-Q6，P3 开工前必须裁决。
- **D2 首期范围**：**先做 worker 热重载 + 远程 reload**（P1）。它是所有后续阶段的公共前提，且立刻见效（改配置不必再重启进程）。 ✅ **已完成**（SIGHUP + `reload` 帧 + `POST /v1/workers/{id}/reload`）。

- **D3 策略传递方式**：**server 下发（push desired state）**，**不是**「远程调用让 worker 改写自己的 `projects` 配置文件」。
  - 根据：**worker 没有自主功能**——它只执行 server 派发的 job，离开 server 什么也不干。所以 worker 持久化一份 project 配置买不到任何东西，只会制造第二个真源。
  - 漂移：下发只有一个真源（server config），构造上不会漂。远程改写有两份要同步的配置，server 眼里的"这台允许什么"退化成缓存，而缓存会因手工改文件 / 半截写入 / 从旧文件重启而失效。
  - 离线 worker：远程写入对离线机器直接失败 → 集群进入"部分应用"状态要人工补。下发模式下离线不是事件——worker 上线 register 时 server 无条件重推当前 Rev，自动收敛。
  - 回滚：下发＝改回 server config + SIGHUP 全体收敛；远程改写要逐台写回，漏一台就是雷。
  - 实现代价：远程改写要在远端机器上 round-trip 一个 YAML 文件（丢注释/格式）并处理并发写、权限、磁盘满、半截写入；下发只是一个帧 + 内存应用，没有远端磁盘写入这个失败面。
  - 校验不需要持久化：worker 的第二道 validate 用**当前生效的 Policy**（内存）即可；没连上 server 时它收不到任何派发。
  - **推论（重要）**：worker.yaml 剩下的 `roots` / `guards` / identity 才是 worker 真正拥有的，**故意不做远程改写**——远程新增一个 `root` ＝ 远程扩大该机器的可执行目录范围，正是唯一应当要求机器访问权限的操作。把它做成远程一键就等于自己拆掉 D1 守住的边界。**"要上机器改"在这里是特性，不是缺陷。**
  - ⚠️ **v0.6 更正（硬错误）**：`roots` 和 `guards` **在代码里今天不存在**。`config.WorkerConfig`（`internal/config/model.go:686-695`）只有 `worker_id / server_link / projects / agents / runners / max_concurrent / labels / storage`。本文多处（§4-D3/D4、§5、§6.1、§7.1）把它们写成「worker 已有的东西 / 剩下的」——**全部是 P3 要新建的字段**，含 YAML 结构、defaults、validate、以及路径归一化/最长前缀匹配的实现（§10-Q1 只定了语义，没有任何代码）。P3 的工作量要把这块算进去。（核实 §13-C5）
  - 妥协：worker 可把「最后应用的 Policy」**只读**落一个缓存文件，仅供 Cluster 页展示"这台机器现在认为自己能跑什么"。**非真源**，重连时以 server 重推的为准。
    - ⚠️ 原文写的 `gofer worker show` **是不存在的命令**（`gofer worker` 今天只有 `stop` / `reload` 两个子命令）。要么在 P4 建它，要么删掉这个说法——**不要在 P3 的计划里当既有能力引用**。（核实 §13-C6）

- **D4 project 放置由 roots 推导，worker 端零 project 配置**（v0.3，推翻 v0.2 的 Q4）
  - **worker.yaml 不再有 `projects` 段**，一条都没有。留「本地逃生舱」与 D3 的单一真源自相矛盾：只要 worker 还能自己声明 project，配置就仍是两半，还要额外背一条「同名谁赢」的合并规则。
  - **`worker-only project`（xu64.10 G1）随之退役**：它当初就是为了绕开「project 要在 server 和 worker 各写一遍」的痛点而生的**创可贴**；本设计从根上治好那个痛点后，它反而成了「配置分两半」的唯一来源。**配置概念砍掉，代码路径保留复用**——「server 声明了但本机路径不可解析的 project」仍走 `workerOnlyProject` 的 placeholder + `remote/` 结果目录逻辑（server 不执行它，只做归档）。
  - **worker 用自己的 `roots` 最长前缀映射自筛**——映射得到本机路径 → 接受；映射不到 → `Rejected(path_outside_roots)` 并回报原因（Cluster 页可见）。
  - 于是「这台机器**能**跑哪些 project」**不由任何一边手工维护，而是从能力自动推导**：A 机挂了 `/data/proj-a`、B 机没有 → proj-a 自然只在 A 上可用，两边都不用配。
  - 迁移：现存 worker.yaml 的 `projects` 段在 P3 转为**只读+告警**（server 在 register 时打印「worker w-x 仍在本地声明 project [...]，请迁到 server config」），下一个版本忽略。

- **D4′ 推送目标 = runner 可达性；roots 只筛能力，不表达策略**（v0.4 修正 v0.3；v0.5 补上它缺失的前提）
  - **v0.3 的错**：把「放置」整个交给 roots 推导 = 用**能力**冒充**策略**。共享盘场景下（host 与容器 worker 共享同一份 `/d/work`）两台 worker 都能映射同一路径，能力上都能跑——但你可能**只准**其中一台跑。「能不能」和「准不准」必须分开。
  - **⚠️ v0.4 的错（codex 审查发现，已实测复现并修复）**：v0.4 断言「`allowed_runners` 里只列 pin 型 runner ＝ 只准那台 worker 跑」——**当时并不成立**。显式 `worker_id` 会覆盖 runner 的 pin（`internal/runner/worker/runner.go`：`f.WorkerID` 优先于 `r.workerID`），`worker_labels` 同样会改路由（`selectTargetWorker` 偏好标签分支）；validate 只校验 worker_id 是已登记 worker，**不校验它是否等于 pin**。实测：`runner=w-container-example(pin 容器) + worker_id=w-host-example` 会按主机 worker 的能力做校验——拦住它的只是「那台恰好没这个 project」，不是 pin。
  - **前提已补齐**：commit `1e69ff5` 把 pin 改成**硬授权**——runner 配了 `worker_id=X` 时，请求的 `worker_id` 必须为空或等于 X，`worker_labels` 一律拒。**代价（须知晓）**：标签自动选机从此只能走**池型 runner**（`type: worker` 且不 pin），这本来就是它该待的地方。
    - ✅ **v0.6 复核：P1/P2 之后该前提仍然成立**，且有回归测试守着。校验点 `internal/job/config.go:185-207`（`pin != "" && req.WorkerID != pin → ErrInvalidRequest`；`worker_labels` 一律拒），测试 `internal/job/pin_test.go`。注意：`internal/runner/worker/runner.go:131-137` 的 `f.WorkerID` 优先逻辑**并没有改**——修的是它上游的 `validate`，在请求进 runner 之前就拒掉 re-route。（核实 §13-C7）
  - **表达机制无需新配置项**：`projects.<key>.allowed_runners`（`config.ProjectConfig.AllowedRunners`，`internal/config/model.go:606`，类型 `[]string`，元素是 **runner key**、不是 worker id）里列了哪些 worker 型 runner，就等于声明了这个 project 在 worker 侧准给谁跑——**在 pin 成为硬授权之后，这句话才为真**。
  - **推送目标算法**（对每台在线 worker W）：
    ```txt
    P 推给 W  ⟺  ∃ r ∈ P.allowed_runners 使 W 经 r 可达：
        r 是 pin 型 worker runner(worker_id=X)  → 可达 ⟺ X == W        （精确，单机场景在此收敛）
        r 是池型 worker runner(type=worker 无 pin) → 可达（候选集在提交时按 job 标签选，server 算不出 → 保守全推）
        r 是 local / peer-http                    → 不是 worker 路由，忽略
    ```
    然后 worker 再用 roots 自筛一道。**有效 = 策略(可达性) ∩ 能力(roots) ∩ 已探测 agent。**
  - **`allowed_runners` 为空 = 不推给任何 worker（v0.6 补，此前是个洞）**：算法里 `∃ r ∈ ∅` 恒假 → 空 `allowed_runners` 的 project 一台 worker 都不推。**这与今天的准入语义一致、不是回归**——`checkRunnerAllowed`（`internal/job/config.go:378-388`）里「空 `allowed_runners`」**只放行 `local`**，任何 worker 型 runner 都会被拒。所以这类 project 今天本来就跑不到 worker 上。P3 的实施者必须知道这条，否则容易"好心"把空列表当通配全推。（核实 §13-C8）
  - v0.2 用 `allowed_runners` 算推送目标方向本就是对的，只是**对池型 runner 算不出**；v0.3 因此整个推翻改成全推 = 把孩子和洗澡水一起倒了。**pin 型精确算、池型才退化成全推**，两全。
  - **「只准一台 worker 跑」怎么配**：`allowed_runners` 里只列那台 worker 的 pin 型 runner。**零新增配置面**，且现网配置一行都不用改。
  - 可选（P4，非必需）：为池型 runner 补 `projects.<key>.worker_labels: [...]` 做**收紧**；不做也不影响正确性（提交时 selectWorker 仍按 project+agent 过滤候选）。
  - 副作用（可接受）：池型 runner 场景下 worker 会看到超出自己能跑范围的 project key 与逻辑路径。单操作者场景无碍。
  - **收益线**：新增 project 到某 worker，只要它的路径落在该 worker **已有的 root** 下、且 `allowed_runners` 列了该 worker 的 runner → **纯 server 侧配置 + SIGHUP，worker 零改动零重启**。只有要暴露一个该机器从未暴露过的目录树时，才必须上机器加 root（D3 推论：故意要求机器访问权）。

- **D6 Policy → worker 本地执行配置的投影**（v0.5 新增；v0.6 按 P2 事实重写）
  - **为什么必须有**：worker 收到 dispatch 后强制 `Runner=local` 再走**本地 `job.Service.Submit`**（`internal/worker/dispatch.go:46`，`Runner: builtinLocalRunner`）。那条链需要的**不是**一个 accepted key 集合，而是一份**完整可喂给 job.Service 的 `config.Config`**。v0.4 只写了「记下本机路径」——按那样实施，job 会在 worker 的二次 validate 处被拒。（核实 §13-C2/C9）
  - **那条链今天实际读的字段**（逐个核实过，@`c3ee6d1`）：

    | 读取点 | 位置 | 读的字段 |
    |---|---|---|
    | cwd 解析 | `internal/job/submit.go:87` `project.SafeJoin(cfg.ExecPath(proj), req.Cwd)` | `ProjectConfig.HostPath`（worker 无 `server.path_view` → `ExecPath` 恒回落 `HostPath`，`internal/config/model.go:732-737`） |
    | 结果目录 | `internal/job/submit.go:96` `project.ResultBaseDir` | `Storage.Root` 或 `HostPath`+`ExchangeSubdir`/`ResultSubdir` |
    | agent 白名单 | `internal/job/config.go:87-91` | `ProjectConfig.AllowedAgents`（**空 = 放行所有已配置 agent**，不是"全禁"） |
    | 交互白名单 | `internal/job/config.go:109-111` | `ProjectConfig.InteractiveAllowedAgents`（**空 = 全禁**，与上面语义相反，别写反） |
    | exec 闸 | `internal/job/config.go:161` | `ProjectConfig.AllowExec`（仅 `!remote` 分支，worker 本地跑正好走这条） |
    | runner 准入 | `internal/job/config.go:378-388` `checkRunnerAllowed` | `ProjectConfig.AllowedRunners` |
    | agent 定义 | `agent.ResolveAgent(cfg, key)` | `config.Config.Agents` |

  - **投影规范**（worker 收到 Policy 后构造本地 `config.Config`，整份**原子替换**）：

    | 字段 | 取值 |
    |---|---|
    | `HostPath` | Policy 的逻辑路径经 `roots` 最长前缀映射后的**本机路径**（映射不到 → 拒绝该 project，不进配置） |
    | `AllowedRunners` | 恒为 `["local"]` —— **不能**原样用 server 的 runner key：`dispatch.go` 强制 `local` 后，非空且不含 `local` 的列表会被 `checkRunnerAllowed` 拒（`config.go:378-388`）。（留空其实也放行 local，但显式 `["local"]` 更难误读） |
    | `AllowExec` | `policy.AllowExec && guards.allow_exec`（护栏只收紧） |
    | `InteractiveAllowedAgents` | `policy.InteractiveAllowedAgents ∩ (投影后 cfg.Agents 中 Interactive=true 的 key)`，且 `guards.allow_interactive=false` 时清空 |
    | `AllowedAgents` | `policy.AllowedAgents ∩ (投影后 cfg.Agents 的 key 集)`；**注意空列表 = 放行全部**，所以「policy 给了个空 allowed_agents」和「交集算出来是空」必须区分——后者要写成一个不可能命中的哨兵或直接拒掉该 project，不能落成空列表 |
    | **agent 定义** | **不投影、不拼装。** 见下方 ⚠️ |
    | `Storage`（result / exchange 子目录） | 取 worker 本地 `storage` 配置（本机事实，不由 server 定；`workerConfigToConfig` 已经这么做） |

  - ⚠️ **「agent 定义」这一行 P2 之后必须这么写（v0.6 更正）**：worker 的 `cfg.Agents` 已经**不需要任何人拼**——`agent.Resolve`（`internal/agent/resolve.go:51`）在 `core.Build`（`core.go:118`）和 `core.ReloadWith`（`core.go:338`）里已经完成「探测 → 把探到的内置模板注入 `cfg.Agents`；operator 手写的 agent 作为逃生舱永不被摘」，并且是**幂等**的（每次 Resolve 先剥离上一轮注入的 key 再重新 gate）。
    - ⇒ **P3 的 Policy 投影必须走 `core.ReloadWith(投影出的 cfg)`**，把 `cfg.Agents` 交给 Resolve 填；**绝不能**自己去 `cfg.Agents = ...` 拼一份（那会绕过 detect gate，并让"探测到的"与"实际配置的"两张表分叉——正是 P2 用 IRON RULE 消灭掉的东西）。
    - ⇒ 投影时 `cfg.Agents` 只放**worker.yaml 里的逃生舱 `agents`**（原样透传），其余留给 Resolve。
    - ⇒ 一个副产品：`ReloadWith` 返回后 `cfg.Agents` 就是「已探测模板 ∪ 逃生舱」的实际集合，**上面两行白名单的交集要在 ReloadWith 之后算**，否则你交的是一个还没物化的空集。这是一条真实的**顺序约束**，实施计划必须写死。
  - **澄清一处误导**：v0.3/v0.4 说「复用 `workerOnlyProject` placeholder」——那是 **host 侧**用于归档/allowlist 的占位（`internal/job/config.go:270-310`，v0.5 写的 `221-263` 已漂），**替代不了 worker 本机的执行 project 配置**。两者不是一回事。
    - 它今天干的事（读注释即知）：`AllowedRunners=[请求的 runner]` + 结果目录回落 `<config-dir>/remote/<project_key>/<job_id>`（`workerOnlyStoreSubdir = "remote"`，`config.go:268`）。触发点是 `config.go:66-71`：**请求的 project key 不在 host 的 `cfg.Projects` 里**时合成。
    - ⇒ P3 说的「配置概念退役、代码路径保留复用」**成立且几乎零改动**：只要 host 侧仍存在「server 没定义、但 worker 能跑」的 project（P3 之后理论上不该有，但 D4′ 的池型全推 + roots 自筛意味着 host 与 worker 的 project 视图仍可能短暂不一致），这条路径就还得在。（核实 §13-C10）
  - **拒绝语义**：Policy 里被拒的 project（`path_outside_roots`）**不进**本地配置，也不出现在能力上报里 → server 的能力视图与 worker 的实际准入天然一致（避免「server 以为能跑、worker 却拒」这类今天已经踩过的坑）。
  - **原子性**：投影出的整份 `config.Config` 一次性替换（同 P1 的原子快照要求），不得逐字段改。`core.ReloadWith` 已经是这个语义（`Cfg` 换指针 + `Projects/Agents/Jobs` 各自 Reload），**直接复用它，不要新造一条应用路径**。

- **D5 web 表单：选定 project 后收窄 runner**（今天 agent 收窄的镜像，`tools-de6` 的自然延伸）
  - 数据已具备 ✅（v0.6 复核）：`/v1/meta` 的 `workers[].projects`（`internal/httpapi/meta_handler.go:85`，仅对**已连接** worker 填充）+ `runners[].worker_id`（`:75`，`:205` 从 `rc.WorkerID` 取 config pin）。**不必等 P3 就能做**。（核实 §13-C11）
  - 规则：worker 型 runner → 解析出目标 worker（显式选择 || pin）→ 其 `projects` 不含当前 project ⇒ 该 runner 不可选，**并给出理由**（"worker w-x 上没有 project X"），不静默消失。
  - **fail-safe（今天踩过的坑）**：worker 离线 / 未上报 projects ⇒ **信息缺失 ≠ 不支持** ⇒ 不排除，照常列出，交后端拒（与 `tools-de6` 的「undefined ≠ false」同一条纪律）。
  - 池型 runner（无 pin）：解析不到唯一 worker ⇒ 只做弱判断——「当前无任何在线 worker 具备该 project」才禁用并说明；否则放行。
  - P3 之后可升级提示精度：worker 回报 `Rejected(path_outside_roots)` ⇒ UI 直接说「w-x 拒绝了 X：路径不在其 roots 内」，用户当场知道该去那台机器加哪个 root，而不是对着灰掉的选项发呆。

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

> ⚠️ **v0.6：下面 `roots` / `guards` 两段是 P3 要新建的字段**（`config.WorkerConfig` 今天没有它们，§13-C5）。`worker_id` / `server_link` / `labels` / `max_concurrent` / `agents` / `storage` 是既有字段；`projects` 是要去掉的既有字段。

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
- `projects.<key>.allowed_runners` → **既是准入、也是放置策略**（D4′）：列了哪台 worker 的 pin 型 runner，就等于「worker 侧只准它跑」。推送目标据此计算。

**两道过滤，各司其职（D4′）**：

```txt
① 策略（server 算）：P 推给 W ⟺ ∃ r ∈ P.allowed_runners，W 经 r 可达
     pin 型 runner(worker_id=X) → 仅 X          ← 「只准一台跑」在这里收敛
     池型 runner(无 pin)        → 全推（候选提交时才定）
② 能力（worker 算）：逐条用 roots 最长前缀映射
     D:/work/inhere/foo  ──[from D:/work/inhere → to /path/to/ws-root]──▶ /path/to/ws-root/foo  ✅ Accepted
     /data/only-on-boxB  ──[无 root 命中]────────────────────────────▶ ❌ Rejected(path_outside_roots)
```

有效 = ① ∩ ② ∩ 已探测 agent。**「能跑」由 worker 说了算，「准跑」由 server 说了算——共享盘上两台机器都能跑同一个 project 时，靠 ① 收敛到你指定的那一台。**

于是「把 project X 放到 worker W 上」= server 声明 X + `allowed_runners` 列上 W 的 runner（路径落在 W 已有 root 下即可）+ SIGHUP；「允许 X 用 tty-claude」= `interactive_allowed_agents` 加一行 + SIGHUP。**worker 零改动、零重启。**

### 6.3 协议（proto v3 → **v4**）

> ⚠️ **v0.6 修正的硬错误**：v0.5 把 Policy 写成 **proto v3**——**v3 已被 P1 的 reload/caps 帧占用**（`internal/wsproto/frames.go`：`MinProtocolVersion=2` / `CurrentProtocolVersion=3` / `ReloadMinProtocolVersion=3`）。**P3 的 Policy 只能是 v4。**（核实 §13-C12）

**版本常量（P3 落地后）**：

```go
const (
    MinProtocolVersion       = 2  // 允许注册的最低版本（兼容下限）—— P3 不动它
    CurrentProtocolVersion   = 4  // 本端实现的版本；P3 从 3 提到 4
    ReloadMinProtocolVersion = 3  // P1 已有，不动
    PolicyMinProtocolVersion = 4  // P3 新增，照 ReloadMinProtocolVersion 的模式
)
func SupportsPolicy(proto int) bool { return proto >= PolicyMinProtocolVersion }
```

✅ **「版本闸拆分」已由 P1 完成，不再是 P3 的任务**：注册闸今天读的就是 `MinProtocolVersion`（`internal/wshub/hub.go:213`），功能闸是**每个能力一个独立常量 + 按对端上报版本判定**（先例：`internal/wshub/reload.go:76` 用 `ReloadMinProtocolVersion` 挡住 v2 worker 并返回「upgrade and restart it」）。P3 只需**照抄这个模式**加 `PolicyMinProtocolVersion=4`：proto<4 的 worker 不下发 Policy 帧（它还在用 worker.yaml 的 `projects`，正好是迁移期的降级行为）。

**新增帧**：

```go
// server → worker (v4)：策略下发。Registered ack 后立即发一次；server SIGHUP reload 后重推所有在线 worker。
type Policy struct {
    Rev      int64            // server 配置代次，单调递增；worker 幂等应用，旧 Rev 丢弃
    Projects []PolicyProject  // key / host_path(逻辑) / allowed_agents / interactive_allowed_agents / allow_exec
    // Agents []PolicyAgent   // ⚠️ 与 D1 冲突，见 §11-Q6：建议 P3 直接不做
}

// worker → server (v4)：应用结果。
type Applied struct {
    Rev      int64
    Caps     *Caps                          // ⚠️ 必须内嵌，见下
    Rejected []struct{ Key, Reason string }  // path_outside_roots / guard_denied / ...
    Degraded []struct{ Key, Gate string }    // 护栏收紧了：如 server 给了 allow_exec，worker guards 拒绝
}
```

> `ReloadCmd` 已由 P1 实现（`wsproto.Reload` / `TypeReload`），**P3 不要重新定义**。

⚠️ **`Applied` 不得另起一条能力上报通路（v0.6 新增）**：v0.5 的 `Applied.Accepted []string` 会让 server 同时拥有**两个** worker-project 真源——既有的 `Caps.Projects`（P1 建的，`internal/wsproto/frames.go:270-276`，经 `TypeCaps` / `ReloadResult.Caps` 两个入口统一收敛到 `h.reg.UpdateCaps`，`hub.go:363` / `:375`）和新的 `Applied.Accepted`。这正是本设计通篇在批判的东西。
> ⇒ **照 `ReloadResult` 的先例**：`Applied` 内嵌 `*Caps`（其 `Projects` 字段即 accepted 集），hub 收到后走**同一个** `reg.UpdateCaps`；`Rejected` / `Degraded` 只作**诊断信息**存在 worker 记录上给 Cluster 页看，不参与路由判定。能力视图更新路径保持**唯一**。

**滚动升级矩阵**（P3 计划必须逐格给出预期，不能只写"兼容"）：v4 server + v3 worker（不下发 Policy，worker 继续用本地 `projects`）/ v4 server + v2 worker（连 reload 都没有）/ v3 server + v4 worker（worker 已删 `projects` 段 → **它会一个 project 都没有**，这是最危险的一格：worker 先升级就等于停摆，必须在文档与告警里写死"server 先升"）/ 多 URL 混合 server 时 worker 轮到旧 server 上的降级行为。

## 7. 关键流程

### 7.1 worker 接入（P3 之后的目标态）

```txt
worker 启动
  → 读 worker.yaml（identity / roots / guards / max_concurrent）——没有 projects
  → [P2 已实现] core.Build → agent.Resolve：对内置模板跑 detect → 只把探到的注入 cfg.Agents
  → Register{proto:4, labels, agents/agent_caps=已探测集}       ← 不再上报 projects（projects=[]）
server
  → 按 runner 可达性算出该 worker 的 project 集（pin 型精确、池型全推，D4′）→ Policy{Rev, Projects}
     ⚠️ Registered ack 之后立刻发，否则出现空窗期（见 §11-Q7）
worker（按能力自筛 → 投影 → 应用）
  → 逐个 project：host_path 经 roots 最长前缀映射
        映射得到 → Accepted（记下本机路径，落 ProjectConfig.HostPath，后续 job 的 cwd 由它解析）
        映射不到 → Rejected(path_outside_roots)
  → 护栏收紧：guards.allow_exec=false → 该 project 的 exec 能力 Degraded（不拒 project，只降能力）
              guards.allow_interactive=false → 同理降交互能力
  → 按 D6 投影出整份 config.Config → core.ReloadWith（原子替换 + 跑 Resolve 物化 agent 定义）
  → Applied{Rev, Caps{projects,agents,...}, Rejected, Degraded}
server
  → reg.UpdateCaps（与 P1 的 Caps/ReloadResult 同一条路径）
  → /v1/meta、/v1/runners、Cluster 页、NewJob 级联全部自动跟上
```

> **P2 已完成的部分**：detect + 内置模板物化（`agent.Resolve`）、agent 能力上报（`Caps.AgentCaps` / `Register.AgentCaps`）、可用性缓存。**P3 不要重做**，只在投影后复用 `core.ReloadWith` 触发同一条 Resolve。

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

### 7.3 P1（首期）：热重载 + 远程 reload ✅ 已完成

```txt
本地： kill -HUP <worker pid>            → 重读 worker.yaml + 重跑 detect + re-register
远程： gofer worker reload <worker_id>   → server 经 hub 下发 reload 帧 → 同上
       POST /v1/workers/{id}/reload      → 同上（web Cluster 页按钮）
不中断在跑的 job：reload 只替换配置快照与能力上报，不动 in-flight 的执行槽。
```

落地位置：`internal/worker/serve.go`（SIGHUP → `enqueueReload`）、`internal/wshub/reload.go`（下发 + `ReloadMinProtocolVersion` 闸 + 等 `ReloadResult`）、`internal/commands/worker.go:313-347`（`newWorkerReloadFn` → `core.ReloadWith` → 回 `Caps`）。

## 8. 安全

**先把威胁模型说清楚（v0.5 修正——原文把安全承诺吹大了）**：worker **信任 server**。这是既有事实，不是本设计引入的：server 本来就能派发 job，一个开了 `allow_exec` 的 project 就等于任意命令执行。所以本设计**不承诺**「挡得住一个被攻破的 server」——那需要 OS 级隔离（容器 / 低权限用户 / seccomp），不是几个 yaml 字段能给的。

护栏的真实作用是**限制爆炸半径 + 防止误配**，按这个尺度理解：

- **护栏是 AND，不是 OR**：有效能力 = server 策略 ∩ worker 护栏 ∩ 实际探测到的 agent。server 只能收紧不能放宽。
- **路径根**：`roots` 是 worker 上唯一可执行范围；server 下发的 `host_path` 映射不进任何 root 就直接拒（不回落到进程 CWD）。这挡住的是「server 端一条配置写错就把 job 跑到不该跑的目录」，**不是**一个存心作恶的 server（它还有 exec agent 这条路）。
- **agent 定义不下发**（D1）：默认只认内置模板 + worker 本地 `agents`。这的确**收窄**了被攻破 server 的活动面（它不能凭空定义任意命令），但 `exec` agent 本身仍是任意命令 —— 所以只有 `guards.allow_exec=false` 的 worker 才真正把这条路堵死。
- **`guards.allow_custom_agents`**：显式的、要在 worker 机器上开的逃生舱；开了就等于把 D1 的收窄让渡回去。
  - ⚠️ **v0.6 质疑：这个开关买到了什么？** worker.yaml 的 `agents:` 段（P2 之后的逃生舱）**已经**能定义任意 agent，而它同样"要上机器改"。`allow_custom_agents` + `Policy.Agents` 只是把同一件事换成"上机器开个开关、然后允许远程定义命令"——它是 D1 边界上**唯一的破口**，却没有 `agents:` 逃生舱给不了的能力。**建议 P3 直接砍掉**（§11-Q6）。
- **worker token ↔ worker_id 绑定**（`internal/wshub/hub.go:185-201` 现有校验，v0.6 复核仍在）：它防的是**未授权客户端冒充 worker**，**不防**一个已被攻破的合法 server 向已认证 worker 派恶意任务。原文由此推出「Policy 不放大被攻破 server 的信任面」是**推理跳跃**，已删。
- **真正要放大信任面时会明说**：本设计不新增 server→worker 的任意命令通道；Policy 只携带 project 元数据与白名单。

## 9. 分阶段

| 阶段 | 状态 | 内容 | 依赖 |
|---|---|---|---|
| **P1** | ✅ **已完成** | 版本闸拆分（`Min`/`Current`/`ReloadMin`）+ worker 热重载：SIGHUP + `gofer worker reload <id>`（经 hub）+ `POST /v1/workers/{id}/reload`；reload = 重读 config + 重跑 detect + re-register，不中断在跑 job。附带建了 `Caps` 帧与 `reg.UpdateCaps` 这条唯一的能力视图更新通路 | — |
| **P2** | ✅ **已完成** | 内置 agent 模板注册表（`agent/templates.go`）+ detect 上报（`agent/resolve.go`，IRON RULE：逃生舱不被探测摘除）；worker.yaml 的 `agents` 降级为逃生舱；agent 可用性缓存 | P1 |
| **P3** | ⏳ 下一步 | Policy 下发（**proto v4**，不是 v3）：**新建** worker 的 `roots` / `guards` 字段；server 按 runner 可达性算推送目标（D4′）；worker 按 roots 最长前缀映射自筛 → 按 D6 投影出 `config.Config` → `core.ReloadWith` 应用 → `Applied{Caps,...}` 回报；worker.yaml 去掉 `projects`（旧字段只读+告警一个版本）；`worker-only project` 配置概念退役（placeholder 代码路径复用） | P1,P2 |
| **P4** | | 管理面：Cluster 页展示每台 worker 的 accepted / rejected(原因) / degraded / detected agents；CLI `gofer worker show` / `worker projects <id>`（**今天都不存在**）；可选 `projects.<key>.worker_labels` 为池型 runner 收紧 | P3 |
| **D5**（独立小项，可插队） | | web 表单按 project 收窄 runner（不可用的 runner 禁用 + 给理由，离线 worker fail-safe 放行）。**不依赖 P1-P3**，现有 `/v1/meta`（`workers[].projects` + `runners[].worker_id`）即可实现 | — |

每阶段单独可发布、可回退。

**~~版本闸拆分是 P1 的第一个任务~~ ✅ 已做完**（`MinProtocolVersion=2` 与 `CurrentProtocolVersion=3` 已分离，注册闸读 `Min`）。**P3 把 `Current` 提到 4 是安全的**——不会踢掉任何 v2/v3 worker。P3 真正的升级风险掉了个个儿：**worker 先升到 v4（已删 `projects` 段）而 server 还是 v3（不发 Policy）→ 那台 worker 会一个 project 都没有、彻底停摆**。所以 P3 的发布纪律是 **server 先升**，且 worker 在「proto≥4 但从未收到过 Policy」时应打醒目告警（§11-Q7）。

## 10. 已定稿的细节（原 Q1-Q5）

> **v0.6 提醒**：本节是「决策定稿」，**不是「代码已存在」**。Q1 的 roots 匹配、Q2/Q3 的应用语义**今天一行代码都没有**（§13-C5）。

- **Q1 roots 匹配语义** ✅ **前缀 + 最长匹配优先**。归一化后比较：统一分隔符为 `/`、Windows 侧大小写不敏感（`D:/work` == `d:/work`），Linux 侧敏感。映射不中任何 root → 拒绝该 project（`path_outside_roots`），**绝不回落到进程 CWD**。
  - 实现提示：拒绝回落这条**必须自己保证**——`workerOnlyProject` 的注释（`internal/job/config.go:288-294`）明确记着一个同类坑：空 `host_path` 经 `filepath.Abs` 会解析成**进程 CWD**，结果散落到随机目录。roots 映射失败时**不能**产出空 `HostPath` 的 ProjectConfig，必须整条不进配置。
- **Q2 worker 侧是否要显式白名单确认** ✅ **自动接受**，靠 `roots` 护栏兜底。加一层"worker 侧确认"就又回到「要上机器改配置」的老路，等于白做；真正的准入边界是 roots + guards，而不是一张要人工维护的清单。
- **Q3 Policy 乱序 / 断连重连** ✅ Rev 单调递增，worker 丢弃旧 Rev；重连时 server 无条件重推当前 Rev（幂等应用）。
- **Q4 worker 本地 `projects` 逃生舱** ❌ **v0.3 推翻：不保留**（见 D4）。理由：与 D3 的「单一真源」直接矛盾——只要 worker 还能自己声明 project，配置就仍是两半，还多一条「同名谁赢」的合并规则要人记。而 `worker-only project` 本就是「配置要写两遍」的绕行方案，本设计根治该痛点后，它反成分裂的唯一来源。**配置概念退役，代码路径（placeholder + `remote/` 结果目录）保留复用**于「server 声明了但本机不可解析路径」的 project。
  - 真正的 worker 主权保留在 `roots` / `guards` / identity —— 那些才是 worker 拥有的东西，且**故意**不可远程改。
  - v0.6 澄清：这里说的 placeholder 是 **host 侧**的 `workerOnlyProject`（`internal/job/config.go:270-310`），复用它**不需要改代码**（触发条件"请求的 key 不在 host 的 `cfg.Projects` 里"天然还在）。它**不是** worker 侧执行配置的来源——那是 D6 的活。
- **Q5 P1 的 reload 是否重连 hub** ✅ **不重连**，只换配置快照 + 重跑 detect + re-register，避免打断在跑的 job。（已按此实现）

## 11. 待确认（v0.6：P3 开工前必须裁决）

- **Q6 `Policy.Agents` + `guards.allow_custom_agents` 要不要做？** ——**建议：砍掉**。它与 D1（"server 不下发命令定义"）**正面冲突**，是 D1 边界上唯一的破口；而它想解决的"内置模板不够用"，P2 的 worker.yaml `agents:` 逃生舱**已经解决了**，且同样要求机器访问权（与 D3 推论一致）。留着它 = 多一条协议、多一个 guard、多一处安全论证，换零新增能力。**需要你拍板**（若确认砍掉，§6.3 的 `Policy.Agents` 与 §6.1 的 `allow_custom_agents`、§8 对应条目一并删）。
- **Q7 register → Policy 之间的空窗期怎么处理？** ——P3 之后 worker 在 Register 时**没有任何 project**（`Register.Projects=[]`），要等 server 的 Policy 到达并 Applied 之后才有。而 server 的准入闸 `capabilitiesFor`（`internal/job/capabilities.go:25-41`）读的正是 worker 上报的 `cand.Projects` —— 这个窗口里提交的 job 会被拒（`ErrProjectNotOnWorker`），错误信息还会指向"worker 上没这个 project"，误导。备选：
  - (a) 窗口内把 worker 标为 `policy_pending`，提交时给**明确**错误（"worker w-x 尚未应用策略 Rev N"）而不是"没有该 project"；
  - (b) hub 在 `Registered` ack 的**同一次写**里带上 Policy，把窗口压到最小（仍非零：worker 要跑 roots 映射 + `ReloadWith`）；
  - (c) 两者都做。**倾向 (c)**。
- **Q8 `allowed_runners` 为空 = 不推给任何 worker，确认接受？** ——见 D4′。语义与今天的 `checkRunnerAllowed` 一致（空列表只放行 local），但意味着**现网所有没写 `allowed_runners` 的 project 在 P3 之后依然只能 local 跑**——这不是回归，但如果有人期待"P3 之后 project 自动就能上 worker"，会失望。**确认这是预期行为**（若要"空=全推"，那是另一个设计，且会与 `checkRunnerAllowed` 打架）。

## 12. 结论

问题的根不在「少了个远程改配置的接口」，而在**职责划错了边界**：策略被钉死在远端机器的静态文件里。把 worker 收敛为能力提供方后，「加 project」「开 pty agent」这类高频操作天然回到 server 侧——那里本来就有热重载和 CLI。

v0.3 的关键修正：**不能只把策略上收、却给 worker 留一个 project 逃生舱**——那等于宣称单一真源却又保留两半配置。worker 端 project 配置归零，而「哪台 worker 持有哪些 project」这个**看起来必须手工维护的东西，其实是从 `roots` 自动推导出来的**：server 全量推目录、worker 按能力自筛。手工维护的清单从两边同时消失，这才是把边界划对了的标志。

P1 的热重载是所有后续阶段的地基，也是唯一一个现在就能立刻缓解疼痛的改动。**P1/P2 已完成**，P3 是本设计的收口。

---

## 13. 代码事实核实表（@`c3ee6d1`，2026-07-14）

> **纪律**：本文任何形如「代码里是 X」的断言都必须在此有一条**可复跑的核实命令**。P1/P2 的教训是——**没附核实命令的"已核实"，一半是假的**（v0.4 的 pin 前提、v0.5 的 proto v3、`roots`/`guards` 的"既有字段"，三条全错）。行号会漂，**命令不会**。
>
> 全部命令在 `tools/gofer/` 下执行。

| ID | 断言 | 核实命令 | 结论 |
|---|---|---|---|
| C1 | server 侧 SIGHUP 热重载存在；worker 侧 P1 之后**也有了** | `grep -n "startReloadLoop" internal/serve/serve.go`<br>`grep -n "SIGHUP\|notifyReloadSignal\|enqueueReload" internal/worker/serve.go` | ✅ serve.go:829（调用点 :193，**非** v0.5 写的 :846）；worker/serve.go 已有 SIGHUP → P1 完成 |
| C2 | worker 收到 dispatch 后强制 `Runner=local` 走本地 `job.Service.Submit` | `grep -n "cl.jobs.Submit\|builtinLocalRunner" internal/worker/dispatch.go` | ✅ `dispatch.go:46-49`（行号未漂） |
| C3 | worker 侧 project 配置今天只来自 worker.yaml；去掉即空 | `grep -n "func workerConfigToConfig" -A 8 internal/commands/worker.go`<br>`grep -n "func workerCaps" -A 8 internal/commands/worker.go` | ✅ `:439` `Projects: wc.Projects`；`:393` `Projects: mapKeys(wc.Projects)` |
| C4 | P2：内置模板 + detect 已物化进 `cfg.Agents`，逃生舱不被摘 | `grep -n "builtinTemplates" internal/agent/templates.go internal/agent/resolve.go`<br>`grep -rn "agent.Resolve" internal/core/core.go` | ✅ `resolve.go:51` `Resolve`；调用点仅 `core.go:118`(Build) / `core.go:338`(ReloadWith)。IRON RULE 见 `resolve.go:31-36` |
| C5 | **`roots` / `guards` 在代码里不存在**（P3 新建） | `grep -n "type WorkerConfig" -A 10 internal/config/model.go`<br>`grep -rn "Roots \|Guards \|\"roots\"\|\"guards\"" internal/ --include=*.go` | ✅ `model.go:686-695` 只有 `worker_id/server_link/projects/agents/runners/max_concurrent/labels/storage`；后一条只匹配到一个同名测试函数，**零字段** |
| C6 | `gofer worker show` 不存在 | `grep -n "Name:" internal/commands/worker.go` | ✅ `worker` 只有 `stop`(:88) / `reload`(:108) 两个子命令 |
| C7 | pin=硬授权（`1e69ff5`）在 P1/P2 后**仍成立** | `grep -n "pinned to worker" -B 12 internal/job/config.go`<br>`go test ./internal/job/ -run TestPinned -v` | ✅ `config.go:185-207` 拒 re-route；`pin_test.go` 三个用例守着。注意 `runner/worker/runner.go:131-137` 的 `f.WorkerID` 优先**未改**——闸在上游 validate |
| C8 | 空 `allowed_runners` **只放行 local**（⇒ 不推给任何 worker） | `grep -n "func checkRunnerAllowed" -A 10 internal/job/config.go` | ✅ `config.go:378-388`：命中列表 → 放行；`runnerKey=="local" && len(AllowedRunners)==0` → 放行；其余全拒 |
| C9 | D6 投影必须覆盖的字段（cwd / 结果目录 / 三张白名单 / runner 准入） | `grep -n "SafeJoin\|ResultBaseDir" internal/job/submit.go`<br>`grep -n "AllowedAgents\|InteractiveAllowedAgents\|AllowExec" internal/job/config.go`<br>`grep -n "func (c \*Config) ExecPath" -A 6 internal/config/model.go` | ✅ submit.go:87/96；config.go:87-91 / 109-111 / 161 / 378-388；ExecPath model.go:732-737（worker 无 `path_view` → 恒取 `HostPath`） |
| C10 | `workerOnlyProject` placeholder 现状 | `grep -n "func workerOnlyProject" -B 26 internal/job/config.go`<br>`grep -n "workerOnlyProject" internal/job/config.go` | ✅ 定义 `config.go:295`（v0.5 写的 `221-263` 已漂）；触发点 `config.go:66-71`（请求 key 不在 host `cfg.Projects` 中）；`remote/` 常量 `:268` |
| C11 | D5 的数据面已具备 | `grep -n "WorkerID\|Projects" internal/httpapi/meta_handler.go` | ✅ `:75` runners[].worker_id、`:85` workers[].projects、`:205` 从 `rc.WorkerID` 取 pin |
| C12 | **proto v3 已被 P1 占用 ⇒ Policy 必须是 v4** | `grep -n "ProtocolVersion" internal/wsproto/frames.go`<br>`grep -n "ReloadMinProtocolVersion" internal/wshub/reload.go` | ✅ `frames.go`：`MinProtocolVersion=2`(:21) / `CurrentProtocolVersion=3`(:27) / `ReloadMinProtocolVersion=3`(:35)；注册闸 `hub.go:213` 读 `Min`；功能闸先例 `reload.go:76` |
| C13 | `Caps` 帧 + `reg.UpdateCaps` 是**唯一**的能力视图更新通路（⇒ `Applied` 须内嵌 Caps） | `grep -n "type Caps" -A 8 internal/wsproto/frames.go`<br>`grep -n "UpdateCaps" internal/wshub/hub.go` | ✅ `frames.go:270-276`；`hub.go:363`（ReloadResult.Caps）与 `:375`（TypeCaps）两个入口收敛到同一个 `reg.UpdateCaps` |
| C14 | server 准入闸读的是 worker 上报的 projects（⇒ 空窗期问题，Q7） | `grep -n "func (s \*Service) capabilitiesFor" -A 16 internal/job/capabilities.go` | ✅ `capabilities.go:25-41`：worker runner → `s.workers.Candidate(wid)` → `cand.Projects` |
