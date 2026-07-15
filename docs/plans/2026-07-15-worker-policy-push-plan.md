# P3：Policy 下发（server 推策略 → worker 投影执行）实施计划

> 设计：[`docs/design/2026-07-13-worker-config-federation-design.md`](../design/2026-07-13-worker-config-federation-design.md)（**v0.8**）
> 总纲：[`docs/plans/2026-07-13-worker-config-federation-plan.md`](2026-07-13-worker-config-federation-plan.md)（P1 ✅）
> 前置：[`docs/plans/2026-07-14-worker-agent-templates-plan.md`](2026-07-14-worker-agent-templates-plan.md)（P2 ✅）
> bd epic：`tools-5pq`　基线：`dcc98dd`

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-07-14 | Claude | 初版。基于设计 v0.7（Q6 砍 `Policy.Agents` / Q7 ack 带 Policy + policy_pending / Q8 空 `allowed_runners` 不推）。**未开工即被对抗式审查推翻，见 v0.2** |
| v0.2 | 2026-07-14 | Claude | **对抗式审查后重写（3 个 BLOCKER + 2 个 HIGH，全部有代码证据）**：<br>① **B1 迁移开关搞错了挂点** —— v0.1 的 T6-B 把开关挂在 **server 的 proto 版本**（`ack.ProtocolVersion>=4 → Policy 权威，哪怕 Projects 为空`）。而 live 的两台 worker.yaml **都有 `projects`、都没有 `roots`** ⇒ 换二进制那一刻 roots 为空 → 所有 project 映射失败 → `cfg.Projects={}` → **两台 worker 双双归零、全部 job 失败**。**且这与 v0.1 自己的验收 1（零改动 → diff 为空）直接矛盾**，矛盾要拖到 T7 才爆。→ **开关改挂 worker 侧 `roots` 的存在性**（见 §2-B1）：换二进制 = 零行为变化，迁移变成 per-worker 可回滚，**"server 必须先升"这条脆弱纪律作废**。<br>② **B2 web 控制台改 project 不会重推 Policy，且 Rev 不是配置代次** —— `project.Registry.Add` **原地改**那个 `*config.Config`（与 `core.Build` 里的是同一个指针），不走 `ReloadWith`；`POST/PUT/DELETE /v1/projects` 全走它。⇒ 计划宣传的头号收益（web 上改白名单/开 exec）**Policy 永不重推**；且 Rev 只在 `ReloadWith` 里 +1 ⇒ **两份不同内容的 cfg 共用同一个 Rev**，整个"rev ≤ lastRev 就丢弃"建立在假前提上。→ T1 新增**唯一写入口**（copy-on-write + Rev++ + 推送，全在 `ReloadWith` 里，结构上无法绕过），顺带解掉 `Add` 的既存 data race。<br>③ **B3 `PushPolicyAll` 会在 ack 之前插帧 → 重连风暴** —— `h.reg.Put(wc)` 在**写 ack 之前**，而 worker 握手只读一帧且 `wsproto.As[Registered](policy帧)` **不校验 `env.Type`、吞掉 decode error** ⇒ 解出 `Accepted=false, Reason=""` ⇒ worker 报"registration rejected（原因为空）"→ 退避重连 → 再撞。→ T0 修：`Put` 挪到 ack 之后 + worker 断言帧类型。<br>④ **H1 `policy_pending` 不得新增拒绝路径** —— v0.1 让准入闸在 pending 时**直接拒**；但 `wc` 是 per-connection、Rev 每次重连清零 ⇒ 每次网络抖动都会重进 pending，而 worker 此刻**仍持有完全有效的上一份 cfg**（`ReloadWith` 原子换指针、从不清空）。硬拒会**打挂在跑的 workflow / cron**（`workflow/advance.go:365` 任一 fan submit 失败 → 整条 workflow fail）。→ pending **只用于替换错误信息**。<br>⑤ **H2 投影漏了两个 worker 真的会读的字段**（`MaxConcurrentJobs` → 不投 = 恒 0 = **无限并发**；`CaptureDiff` → 不投 = nil = **默认开**，`capture_diff:false` 静默失效）。<br>⑥ **H3 roots 前缀映射比 per-project `container_path` 表达力弱**（live 有一个 project 的 container_path 与 host_path **末段不同名**）→ 决策：**不加 per-project 覆盖，用"更具体的 root"表达例外**（最长前缀天然覆盖，理由见 §6）。<br>⑦ M1/M2/M3 修正：漏掉的第三条 `workerConfigToConfig` 路径（启动，`worker.go:268`）+ 依赖图两处错 + 验收命令假红文案。<br>⑧ 重写时新发现 2 条（§7）：**ack 与广播的竞态**（catch-up push）、**ack 写绕过 `writeMu`**。 |
| v0.3 | 2026-07-14 | Claude | **四方独立审查后再修（主机 codex×3 视角 A/B/C + 子代理证伪写回路径）**。存档：`tmp/p3-review-out/{A-blockers,B-protocol,C-exec}-result.md`。B1/B2/B3 方向站住，但**修法本身**被攻出 6 类问题，新增 **B4/B5/B6** 三个 BLOCKER：<br>① **🔴 B1 切换时机（codex-B BLOCKER-1，两个 codex 在此格意见相反，采纳 B）** —— v0.2 的「`roots>0` ⇒ 无条件 Policy 权威、忽略本地」在 **v3 server / server 回滚 / 多 URL 轮转** 下会把 POLICY worker 打到 0 project（v3 ack 不带版本、永不发 Policy）。而这几件事是现网常态与应急手段。→ **`roots` 只表达「迁移意图」，Policy 帧到达才「激活」**；worker **永不因「连了个不给 Policy 的 server」而清空当前有效配置**；冷启动从 policy 缓存恢复 **last-known-good**。改 §2-B1 / T5-A/B。<br>② **🔴 B2 写入口必须串行 + mutation seam（codex-A/C + 子代理）** —— v0.2 的 `Core.Update = Snapshot→Clone→mut→ReloadWith` **没有写锁**：两个并发 POST 从同一快照 clone、后写覆盖前写，两份不同 cfg 还可能拿到**同一个 Rev**。且设计自相矛盾（Registry 自己 clone+save 再 apply，绕过了 `Core.Update`）。子代理证伪三条事实：**`Remove` 与 `Add` 同罪**（B2 结论句只点名 Add，实施者会漏掉 DELETE → **撤白名单撤不掉**）；**`Rev` 全仓根本不存在**（是 P3 从零引入的代次，不是"补 ++"）；**`cfg.Clone()` 不存在**，本仓唯一 clone 先例是**浅拷贝**（照抄会让 `agent.Resolve` 去 delete 与旧 cfg 共享的 `Agents` map → 更严重的 race）。→ T1-B 加 `updateMu` 串行写事务 + Registry 交 **mutation**（不构造 next）+ `Clone` 深拷四张 map 与私有 `injectedAgents` + `config.Save` 写 next。<br>③ **🔴 B4 跨重连旧 Policy 污染新连接（codex-A BLOCKER-4）** —— `reloadCh` 生命周期是**整个 worker**、跨重连不清空。旧连接的 `Policy{Rev:100}` 还在队列 → 断线 → 连到重启后的 server（Rev 从 1）→ register 清零 lastRev → 队列里旧 Rev:100 先执行、顶高 lastRev → 新 server 的 Rev 1..99 **全被当旧帧丢弃**、永不收敛。→ T5-C/D 引入 **session generation**：每次握手产生新 gen，executor 执行前丢弃非当前 gen 的 Policy；register goroutine **不直接改** executor 独占的 lastRev（改投 `sessionStart` 事件）。<br>④ **🔴 B5 有界队列满丢最新 Rev（codex-A BLOCKER-3）** —— `reloadCh` 容量 8、非阻塞发送；Policy apply 最坏 ~2s，连续几次 web 改 project 就填满队列，**被丢的恰是最新 Rev**，catch-up 只在 register 发生 → 在线期间丢的补不回来。→ T5-C：Policy 用 **latest-wins mailbox**（只保留最大 Rev 的待应用 Policy，不塞进有界 FIFO）。<br>⑤ **🔴 B6 MapRoot 可被 `..`/symlink 逃出 roots（codex-B BLOCKER-2）** —— v0.2 只 `Clean(To+suffix)`，`/srv/allowed/../outside`→`/srv/outside` 出界；`SafeJoin` 只做词法检查、不 `EvalSymlinks`。→ T2-B 映射两端做 containment 校验（`..` 词法 + `filepath.Rel` 不以 `..` 开头）+ symlink 加固 + 反向测试。<br>⑥ **🔴 T7 冒烟没真隔离（codex-C BLOCKER-2/3 + codex-B HIGH-6）** —— 只指定 `:18899` 不叫隔离（没锁 `GOFER_CONFIG_DIR`/DB/storage/worker_id/token/`server_link.urls`；复制 live worker.yaml 保留多 URL → 隔离 server 不可达就**连 live**）；复制 live 配置保留 live `host_path` → **job 在真实项目目录执行**。→ T7 重写：隔离栈 preflight 硬断言 + 脱敏合成 fixture + 明确容器内 Linux 执行环境。<br>⑦ **HIGH：二进制回滚不是安全回滚（codex-C HIGH-3）** —— T6-D 删 `projects` 后回滚旧二进制 = 0 project。→ 迁移文档标「删 projects」为**不可二进制回滚点**，观察期 projects 必须保留。<br>⑧ 依赖图漏 **T2→T5**；T1/T4 验收改 spy/fake（不能端到端闭环，端到端归 T7）；关键验收补 `go test -run` 可执行断言（C 的 20 条补强清单存档在 `C-exec-result.md`，实施逐条对照）。<br>⑨ 顺带发现（独立 issue，本期不强解）：`config.Save` 写**活配置**且 `managedTopKeys` 覆盖整个顶层 → 一次 web project 写会把运行期原地写入的 overlay 结果 / `--web-dir` 等 CLI flag **永久固化**进用户配置（P2 `config.Save` 同款 bug 第 3/4 例）。见 §7-N5。 |

---

## 1. 目标

**新增一个 project 到某台 worker、允许它用某个 agent —— 只改 server 配置（CLI / web 控制台 / SIGHUP），不登录 worker、不重启 worker 进程。**

**先把收益说准（别吹大）**：

- ✅ **真实收益 = 高频操作回到 server**：加 project / 改白名单 / 开 exec / 开 pty，**worker 零改动零重启**。前提：该 project 的路径**已落在这台 worker 已有的 `root` 下**，且 `allowed_runners` 已列出该 worker 的 runner。
- ⚠️ **不是"全自动"**：要暴露一个该机器**从未暴露过的目录树**时，**仍必须上机器加 `root`**。这是 D3 推论——**故意**保留为需要机器访问权的操作。
- ⚠️ **不是安全收益的净增**：D1 的"server 无法凭空定义命令"**P2 就已成立**。P3 要做的是**别把它弄丢**（Q6 砍掉 `Policy.Agents` 正是为此）。护栏（`guards`）买到的是"限制爆炸半径 + 防误配"，**不是**"抵御被攻破的 server"（worker 仍然信任 server，设计 §8）。
- ⚠️ **现网存量 project 不会自动上 worker**：没写 `allowed_runners` 的 project，P3 之后依然只能 local 跑（Q8，与今天一致）。
- ⚠️ **P3 之后现网 worker 也不会自动进入新模式**：迁移是 **per-worker opt-in**（在 worker.yaml 写 `roots`），见 §2-B1。换二进制本身**零行为变化**。

---

## 2. 被审查坐实的 BLOCKER（先看这一节，它决定了任务形状）

> v0.2 定了 B1/B2/B3；v0.3 经四方独立审查（主机 codex 的 A/B/C 三视角 + 子代理证伪写回路径），**方向站住，但修法本身被攻出问题**，新增 B4/B5/B6，并**修正 B1 的切换时机与 B2 的串行化**。原始报告存档 `tmp/p3-review-out/`。

### 🔴 B1 迁移开关挂在 **worker 的 `roots`**，但 `roots` 是「意图」、Policy 帧到达才「激活」

**v0.1 错在哪（有证据）**：

```bash
# live worker.yaml：有 projects、无 roots、无 guards
grep -c "^roots:\|^guards:" /path/to/ws-root/config/linux/gofer/worker.yaml   # → 0
grep -n "^projects:" /path/to/ws-root/config/linux/gofer/worker.yaml          # → 命中
```

v0.1 的 T6-B 写着「`ack.ProtocolVersion >= 4` → Policy 权威（**哪怕 Projects 为空**），忽略 `wc.Projects`」。推演一遍：换新二进制 → server 是 v4 → Policy 权威 → worker `roots` 为空 → **每个 project 的路径都映射失败** → 全部 `Rejected` → `cfg.Projects = {}` → **这台 worker 一个 job 都跑不了**。另一台（主机 worker）更直接：没有任何 project 的 `allowed_runners` 列它 ⇒ **Policy 本来就是空的** ⇒ 同样归零。

**⇒ v0.1 的验收 1（"零改动 → diff 为空"）与它自己的 T6-B 自相矛盾**，而这个矛盾要等到 T7 冒烟才爆。

**修法（本计划采纳，v0.3 对切换时机做了关键修正）**：开关挂在 **worker 侧 `roots` 的存在性**，但 **`roots` 只表达「迁移意图」（这台想进 POLICY），不等于 Policy 立刻成为权威**。真正把本地配置换成 server 策略，只发生在**收到一份 proto≥4、Rev 更高的 Policy 帧并投影成功**的那一刻（原子替换）。

> 🔴 **v0.3 收紧的铁律（codex-B BLOCKER-1；两个 codex 在这一格意见相反，采纳 B）**：POLICY 模式的 worker **永远不主动把「当前已经有效的配置」清空**。连到一个不发 Policy 的 server（server 是 v3、或 v4 但这台的 Policy 恰空、或干脆连不上）时，worker **保留手上那份配置**（内存 `lastPolicy` 投影，或冷启动时从 policy 缓存恢复的 last-known-good）。<br>v0.2 的「`roots>0` ⇒ 无条件 Policy 权威、忽略本地」会在 **滚动发布 / server 回滚 / 多 URL 轮转** 时把 POLICY worker 打到 0 project —— 而这几件事恰恰是现网的常态与应急手段（worker 多 URL 轮转、server 回滚是现成应急）。codex-A 认为「计划已把这格写成显式降级、可接受」，codex-B 坚持「不可接受、会全停」；**站 B**，且修法不牺牲 B1 的核心价值（换二进制零行为变化、per-worker opt-in 都还在）。

静态模式（看 worker.yaml）三分支不变，但 **POLICY 分支的「有效配置来源」是运行期动态解析**的：

| worker.yaml 形态 | 静态模式 | 连接 / 收帧状态 | 有效配置来源 |
|---|---|---|---|
| `len(Roots)==0 && len(Projects)>0` | **LEGACY** | 任意 | `wc.Projects`（本地，**逐字不变** = 今天）。仍回 `Applied{Rev, Caps(本地), Degraded:[{"*","legacy_local_projects"}]}` 清 pending |
| `len(Roots)==0 && len(Projects)==0` | **EMPTY** | 任意 | 空（**今天这种 worker 也跑不了任何东西**，doctor 本就 FAIL） |
| `len(Roots)>0` | **POLICY** | 收到 proto≥4、Rev 更高的 Policy | 投影 `lastPolicy`（**唯一的清空/替换时机**，原子换指针） |
| `len(Roots)>0` | **POLICY** | 冷启动、还没收到 Policy、**有** policy 缓存 | 投影缓存里的 **last-known-good**（重启无缝） |
| `len(Roots)>0` | **POLICY** | 冷启动、还没收到 Policy、**无**缓存 | 空 —— 但此刻**尚未注册**，server 不会派 job；等 Policy 到达再投影 |
| `len(Roots)>0` | **POLICY** | 连到 v3 server / v4 但 Policy 恰空 / 掉线重连中 | **保留手上那份**（内存 `lastPolicy` 或缓存恢复）；两者都没有才是空 + `slog.Error`。**绝不因为「连了个不给 Policy 的 server」而清空** |

**买到了什么**：

- 换二进制 = **零行为变化**（LEGACY 逐字不变）—— 验收 1 才真正成立（不是嘴上成立）。
- 迁移变成 **per-worker、可回滚**：某台 worker 迁挂了，把 `roots` 删掉、`projects` 留着就回到 LEGACY。
- **"server 必须先升"这条纪律不再需要**：`roots` 的有无与 server 版本**正交**。回滚 server 到 v3（不发 Policy）时——① LEGACY worker 用本地 projects 照跑；② **已激活的 POLICY worker 保留 last-known-good、不归零**（v0.3 修正；v0.2 会归零）。
- `Registered.ProtocolVersion`（T0）用途从"决定权威归属"降为**告警文案精度**：POLICY 且手上是**空**配置时，`ack.ProtocolVersion<4` ⇒「server 太旧，不支持策略下发」；`>=4` ⇒「server 没给本 worker 推任何 project，检查 `allowed_runners`」。

### 🔴 B2 所有改 cfg 的路径都必须走**同一个串行写事务**（copy-on-write + Rev++ + 重推）

**证据**（子代理证伪：`Add` **不是**唯一原地写入口，`Remove` 同罪；且全仓无 `Rev` 概念）：

```bash
grep -n "func (r \*Registry) Add" -A 13 internal/project/registry.go   # cfg.Projects[key]=proj  ← 原地改
grep -n "func (r \*Registry) Remove" -A 10 internal/project/registry.go # delete(cfg.Projects,key) ← 同罪，B2 结论句漏了它
grep -n "project.NewRegistry" internal/core/core.go                    # core.go:119 —— 与 Core.Cfg 是同一个 *config.Config 指针
grep -n "s.projects.Add\|s.projects.Remove" internal/httpapi/project_handler.go  # :96(POST) / :124(PUT=force-Add) / :139(DELETE)
grep -rn "cr.Reload(" internal/serve/serve.go                          # :839 —— 全仓唯一的 server 侧 reload 调用点（SIGHUP）
grep -rn "\bRev\b|ConfigRev|Generation" internal/ --include=*.go | grep -v _test  # → 0 命中：Rev 是 P3 从零引入，不是"补 ++"
grep -rn "func .*Clone" internal/config/                               # → 0：cfg.Clone() 不存在；唯一先例 writer.go 的 withoutInjectedAgents 是【浅拷贝】
grep -n "config.Save" internal/project/registry.go                     # :107 —— save 写的是 r.cfg.Load()（活配置），不是副本
```

后果四条：

1. **web 控制台 / `gofer project add` 改了 project，`ReloadWith` 根本不跑** ⇒ Policy 永不重推 ⇒ 计划的头号收益是假的（只有 SIGHUP 才生效）。
2. **Rev 不是配置代次**（且**根本不存在**）：P3 要从零引入 Rev；若只在 `ReloadWith` 里 +1、而 web 写路径改了 cfg 内容却不 +1 ⇒ **两份不同内容的 cfg 共用 Rev N** ⇒ worker 侧「rev ≤ lastRev 就丢弃」建立在假前提上。
3. **既存 data race**：`Add`/`Remove` 原地写 `cfg.Projects` map，而 `job.Service` 持有同一指针并发读（`POST /v1/projects` 与 job 提交同时发生即触发）。
4. **🔴 并发写自己也丢更新（codex-A/C）**：即便都走 `ReloadWith`，若 `Snapshot→Clone→mut→ReloadWith` **没有写锁**，两个并发 `POST`/`DELETE` 会从**同一快照** clone、各存一份不同 next、后写覆盖前写；`Rev = Load().Rev+1` 也可能让两份不同 cfg 拿到**同一个 Rev** ⇒ worker 应用了 `{A,B}@Rev11` 后，`{A,C}@Rev11` 因 `rev≤lastRev` 被丢 ⇒ **永久与 server 不一致**。

> ⚠️ **这是 P2 栽过的同一个 registry**（第二次）：P2 是 `config.Save` 把模板固化（T0-A），P3 是 `Add`/`Remove` 原地改指针 + 无锁。**同一个对象，同一类盲区。**

**修法（v0.3 收紧）**：给 core 一个**唯一、串行的写事务**：

- **`Core.Update(mut func(*config.Config) error)`** 全程持 **`updateMu`**：`snap→Clone→mut(next)→config.Save(path,next)→reloadLocked(next)`。**`Core.Reload`（SIGHUP）也走同一把锁**（否则 SIGHUP 与 web 写并发仍撞）。
- **Registry 只交 mutation，不构造 next**（codex-C：v0.2 让 Registry 自己 clone+save 再 apply，那条路径绕过了 `Core.Update`、锁不住）。seam 从「传已构造好的 `*config.Config`」改为「传一个 `func(*config.Config) error`」，由 Core 在锁内 clone 后 apply。**`Add` 与 `Remove` 都走它**（结论句不再只点名 Add）。
- **Rev++ 与 PushPolicy 都放在 `reloadLocked` 里**（`updateMu` 保护下，`Core.Update`/`Core.Reload`/公开 `ReloadWith` 都经它）——任何配置写入路径（现在的、将来的）都不可能"忘记推"。**结构性保证，不是纪律。**
- `config.Save` 写的是 **`next`（副本），不是活配置**（顺带修掉"先改内存再 save、save 失败内存已脏"的既存缺陷；另见 §7-N5 的 `managedTopKeys` 固化问题，本期只保证写 next）。

### 🔴 B3 `PushPolicyAll` 会在 ack 之前插帧 → worker 把 policy 帧当成"注册被拒" → 重连风暴

**证据**：

```bash
grep -n "h.reg.Put(wc)" -A 12 internal/wshub/hub.go     # 步骤 3：Put 在写 ack(步骤 4)之前
grep -n "reg, _ := wsproto.As\[wsproto.Registered\]" -B 3 -A 8 internal/worker/client.go  # 只读一帧；不校验 env.Type；吞掉 err
grep -n "func As\[" -A 8 internal/wsproto/envelope.go   # 裸 json.Unmarshal，无 DisallowUnknownFields
```

`h.reg.Put(wc)` 一旦执行，`PushPolicyAll()`（遍历 `h.reg.All()`）就能给这条连接写帧——而 ack 还没写。worker 握手读到的第一帧是 policy 帧 → `As[Registered](policy帧)` → **`err=nil, Accepted=false, Reason=""`** → 走 `!reg.Accepted` 分支 → 报 "registration rejected"（**原因为空**）→ 断连退避 → 重连 → 再撞。

**修法（两条都做，成本极低）**：

1. `h.reg.Put(wc)` **挪到 ack 成功之后**（ack 失败就不 Put，也不必再 `reg.Remove`）；
2. worker 握手**断言 `env.Type == wsproto.TypeRegistered`** 且**不吞 decode error**。

### 🔴 B4 `reloadCh` 跨重连存活 → 旧 Policy 在新连接上复活（codex-A BLOCKER-4）

**证据**：

```bash
grep -n "go cl.reloadLoop\|func (cl \*Client) reloadLoop\|runSession(" internal/worker/client.go internal/worker/reload.go
# reloadLoop 在 Client.Run 起一次，生命周期 = 整个 worker；runSession 断开只换 URL 重连，不重建/不清空 reloadCh
```

`reloadCh` 是 worker 级、跨重连**不清空**；而 v0.2 想让「register 成功清零 lastRev」防旧帧——但 register goroutine 与 executor 是**两个 goroutine**。时序：① 旧连接 A 已把 `Policy{Rev:100}` 入队、前面还堵着一个耗时 apply，尚未执行；② A 断开，连到**重启后**的 server B（Rev 从 1 数）；③ register 清零 lastRev、B 的 `ack.Policy{Rev:1}` 入队；④ 队列里 A 的 `Rev:100` **先执行**（`100>0`），把 lastRev 顶到 100；⑤ B 的 `Rev:1` 及后续 `2..99` **全部 `≤100` 被丢弃**。⇒ worker 对 B 在线，却长期跑 A 的旧策略、可能**永不收敛**。多 URL 新旧 server 混用同样触发。

**修法**：`reloadReq` 携带**不可复用的 `sessionGeneration`**（每次成功握手 +1）。executor 执行 Policy 前**丢弃非当前 gen** 的 Policy。更稳的结构：把 `sessionStart{gen}` 也做成同一串行 executor 的事件——它负责清该会话的 Rev，**然后**才允许同 gen 的 Policy 执行。**绝不**从 `runSession` goroutine 直接改 executor 独占的 `lastRev/lastPolicy`（那是 race）。（改 T5-C/D。）

### 🔴 B5 有界 `reloadCh` 满 → 丢的恰是最新 Rev（codex-A BLOCKER-3）

**证据**：

```bash
grep -n "make(chan reloadReq\|reloadQueueCap\|func (cl \*Client) enqueueReload" internal/worker/reload.go internal/worker/client.go
# 容量固定 8；enqueueReload 非阻塞发送，满时返回 false（远程 reload 还能回 busy，Policy 没有 request id）
```

Policy apply 要跑 projection + `agent.Resolve` + detect（P2 实测最坏 ~2s）。executor 处理 Rev N 时，Rev N+1…N+8 填满队列，**最新的 N+9 被 `enqueueReload` 直接拒**；worker 最终只应用到 N+8，server 已是 N+9。catch-up **只在 register 时发生**（§7-N1），在线期间丢的 N+9 **不会再来** ⇒ 破坏「web 改后数秒收敛」。

**修法**：Policy **不走**「每版排队」的普通 FIFO。用 **latest-wins mailbox**：executor 持一个 `pendingPolicy`（原子替换、只留**最大 Rev** 的当前 gen Policy），**不塞进有界 `reloadCh`**（所以容量 8 限制不波及 Policy）。SIGHUP/远程 reload 仍走原队列、仍与 Policy 由**同一** executor 串行（定序不变）。（改 T5-C。）

### 🔴 B6 `MapRoot` 可被 `..` / symlink 逃出 roots（codex-B BLOCKER-2）

**证据**：

```bash
grep -n "func SafeJoin" -A 30 internal/project/path.go   # 只做 Abs/Clean/Rel 词法检查，【不】调 EvalSymlinks
grep -rn "EvalSymlinks\|Lstat" internal/project internal/config internal/commands/worker.go  # → 0：全仓没有 realpath 校验
grep -n "p.HostPath ==\|host_path is required" internal/config/loader.go  # server 侧只校验 host_path 非空，不拒 ..
```

v0.2 的 `MapRoot` 是「拼接后直接 `filepath.Clean`」：worker root `{from:/logical/root, to:/srv/allowed}`，server 下发 `HostPath=/logical/root/../outside` → 拼成 `/srv/allowed/../outside` → `Clean` 后 `/srv/outside`，**逃出 `To`**。即便封 `..`，`/srv/allowed/link`（symlink→`/etc`）也能让执行目录落到 root 外。roots 号称是「本机执行范围的边界」——这个边界**当前不成立**。

**修法**：`MapRoot` 两端都做 containment（不是只靠字符串前缀）：① 逻辑路径规范化后**拒绝任何清理后逃出所匹配 `From` 的路径**；② 生成 mapped path 后 `filepath.Rel(cleanTo, cleanMapped)` 检查结果**不以 `..` 开头、不是绝对路径**；③ roots 作为边界，对**已存在**路径用 `filepath.EvalSymlinks` 比较 real `To` 与 real mapped（尾部不存在的部分从最近存在父目录解析）；④ 反向测试至少四例（逻辑含 `..` / `To` 含 `..` / root 内 symlink 出界 / 正常 symlink）。**定位**：roots 主要防**误配**（worker 仍信任 server，设计 §8），但 `..` 逃逸是实打实的落错目录，必须防。（改 T2-B/C。）

---

## 3. 验收（先写死；每条都指出"怎么证明它真的成立"，多条要求**先证伪**）

> P1/P2 的经验：**没被证伪过的验收，一半是测了个寂寞。**"先证伪" = 把修法撤掉，该验收必须**立刻红**；没红就说明测试根本没测到那个东西。

1. **🔴 现网零破坏（B1 的护城河，最高优先级）**：用**当前 live 形态**的 worker.yaml（**有 `projects`、无 `roots`、无 `guards`**）+ **当前 live server config**（server 侧 4 个 project 的 `allowed_runners` 都列了容器 worker 的 runner，**没有一个列主机 worker**）—— **只换二进制、配置零改动**：
   - `/v1/meta` 中该 worker 的 `projects` 与 `agent_caps` **逐条 diff 为空**（新增的诊断字段 `policy_rev`/`rejected`/`degraded` 是**加法**，不计入 diff）
   - 原有 project 提交 exec job / tty-claude 交互 job **仍跑通**
   - 且 **server 确实推了一份非空 Policy 给容器 worker**（4 个 key），worker **必须忽略它**（LEGACY）：断言 worker 日志有 legacy 告警、`Applied.Degraded` 含 `legacy_local_projects`，而 `/v1/meta` 的 `projects` **仍是 worker.yaml 的那 3 个、不是 policy 的 4 个**
   - **证明方式**：旧二进制（`dcc98dd`）起一次记 `/v1/meta`，换 P3 二进制再记一次，`diff` 必须为空。
   - **先证伪（两条都要）**：① 把开关改回 v0.1 的 `ack.ProtocolVersion>=4 → Policy 权威` → 这条必须**立刻红**（roots 为空 ⇒ 全部 Rejected ⇒ projects 变 0）；② 把 `guards` 的 `*bool` 改成裸 `bool` → exec job 必须**当场被护栏拒**（live 3 个 project 全 `allow_exec: true`）。

2. **迁移开关三分支（单测钉死 B1）**：`(roots, projects)` 的三种组合 → LEGACY / POLICY / EMPTY，逐格断言最终 `cfg.Projects` 的来源。**这条是防"好心实施者把开关改回 proto 版本"的墙。**

3. **纯 server 侧加 project（POLICY 模式）**：worker.yaml 换成 `roots` 形态 → server config 新增一个 project（`host_path` 落在该 worker 已有 root 下 + `allowed_runners` 列该 worker 的 runner）→ `kill -HUP <serve pid>` → **不碰 worker**：
   - `/v1/meta` 中该 worker 的 `projects` 出现新 key
   - **worker 进程 PID 前后不变**（`pgrep` 前后逐字比对，不看日志自述）
   - 立刻用该 project 提交 job → 跑通，且 `cwd` 落在**映射后的本机路径**（断言 job stdout 里的 `pwd`）

4. **🔴 web 控制台改 project 也会重推（B2）**：`POST /v1/projects`（或 `PUT` / `DELETE`）→ **不发 SIGHUP** →
   - worker 的 `projects` 在数秒内出现/消失该 key（`/v1/meta` 轮询）
   - **先证伪**：把 `Registry.Add` 改回"原地改 cfg + save"→ 这条必须**立刻红**（worker 永远看不到）。

5. **Rev 是真·配置代次 + 并发写无丢失（B2；v0.3 加并发写事务）**：SIGHUP / web add / web update / web delete **四条写路径**跑完，Rev 都 **+1**；断言**不存在**"两份不同内容的 cfg 共用同一 Rev"。
   - **证明方式**：单测在每条路径后取 `Snapshot()`，断言 `(Rev, Projects指纹)` 一一对应、单调。
   - `-race`：N 个 goroutine 循环 `Snapshot()` + 1 个循环写 → 每次读到的 `(Cfg, Rev)` **同属一代**（用一个可辨识的 project key 打标记）。
   - 🔴 **并发写事务（B2 后果4，codex-A/C）**：并发添加 50 个不同 key + 并发 POST/DELETE 混合 + POST 与 SIGHUP 并发 → **50 个全部存在、无 lost update**；每次成功写的 Rev **唯一且严格递增**（不存在两份不同 cfg 共用一个 Rev）；`config.Save` 失败时磁盘 / Snapshot / Registry / Job Service **全部保持旧代**（不发布）。
     - `go test -race ./internal/core ./internal/httpapi -run 'TestConcurrentProjectWritesNoLostUpdate|TestConcurrentWritesUniqueRevision|TestFailedSaveDoesNotPublish' -count=50`
     - **先证伪**：撤掉 `updateMu` → `TestConcurrentWritesUniqueRevision` 必须红（会出现重复 Rev / 丢 key）。

6. **🔴 register 期间不得插帧（B3）**：worker 握手读到的第一帧**必然**是 `Registered`。
   - **先证伪**：恢复 `Put` 在 ack 之前，并在 register 处理中人为触发一次 `PushPolicyAll` → 必须**真的**复现 worker 日志 `registration rejected`（**reason 为空**）+ 退避重连循环。没复现出来 = 测试没测到东西。
   - 修后：同样的注入下 worker 正常注册；且 worker 收到非 `Registered` 首帧时报**明确**错误（帧类型不符），不再是空 reason。

7. **ack 与广播的竞态收敛（重写时新发现，§7-N1）**：在"ack 已按 Rev=N 算好、`Put` 尚未执行"之间发生一次 server reload（Rev=N+1，广播时该 worker 还不在注册表里）→ worker **最终仍收敛到 N+1**（catch-up push）。
   - **证明方式**：单测在 `Put` 前插一个 hook 触发 reload，断言 worker 最终 `AppliedRev == N+1`。**先证伪**：去掉 catch-up → worker 必须**永久停在 N**（直到下一次配置变更）。

8. **roots 映射失败 = 拒绝，不是"落到随机目录"**：server 推一个路径不在任何 root 下的 project →
   - `Applied.Rejected` 含 `{key, path_outside_roots}`，该 key **不在** `/v1/meta` 的 worker `projects` 里
   - 断言投影结果里**不存在任何 `HostPath==""` 的 ProjectConfig**
   - **先证伪**：让映射失败也产出一个 ProjectConfig → 必须复现 `filepath.Abs("")` = 进程 CWD（`project/path.go:30` `filepath.Abs(execRoot)`；`workerOnlyProject` 注释里记着同款坑）。

9. **🔴 SIGHUP 不得清空 project（POLICY 模式最容易翻车的一条）**：worker 已应用 Policy（N 个 project）→ `kill -HUP <worker pid>` →
   - `/v1/meta` 的该 worker `projects` **仍是那 N 个**（不是 0 个）
   - **先证伪**：不保存 `lastPolicy` 的实现下，这条必须**真的**复现为"projects 变成 0、worker 彻底停摆"。

10. **加 root 立刻生效**：worker.yaml 加一个 root（覆盖之前被拒的 project）→ `gofer worker reload <id>` → 该 project 从 `Rejected` 变成 `projects` 里的一员，**worker PID 不变**。

11. **护栏只收紧、不放宽**：`guards.allow_exec: false` + server 侧 `allow_exec: true` → exec job **被 worker 拒**（明确错误），`Applied.Degraded` 含该 key。反向：`guards.allow_exec: true` + server `allow_exec: false` → **仍然拒**（server 说了不准）。

12. **🔴 `allowed_runners` 为空 ⇒ 不推给任何 worker（Q8，反向测试钉死）**：
    - 单测：`computePolicy(cfg, w)` 对**每台** w 都不含该 key；e2e：`/v1/meta` 的 worker `projects` 里没有它
    - **先证伪**：改成"空 = 全推"→ 该测试必须红。**这是防"好心实施者把空当通配"的唯一保险。**

13. **白名单不做交集（D6，反向测试钉死）**：policy 给 `allowed_agents: [claude, tty-codex]`，而 worker 上**没装 codex** →
    - worker 的 `cfg.Projects[k].AllowedAgents` **逐字等于** `[claude, tty-codex]`（**不是** `[claude]`，更**不是** `[]`）
    - 用 `tty-codex` 提交 → 明确报错（host 侧 `agent not on worker` 或 worker 侧 `unknown agent`），**不是**静默放行
    - **为什么要这条**：一旦有人"优化"成交集，`allowed_agents` 交出空列表就会**静默放开全部 agent**（空 = 放行全部）。这条测试是那道墙。

14. **投影字段完备（H2）**：
    - `max_concurrent_jobs: 1` 的 project → worker 上并发提交 2 个 job，第二个**排队**（不是并行跑）。**先证伪**：从 `PolicyProject` 删掉 `MaxConcurrentJobs` → 该测试必须红（`job/submit.go:331` `s.semaphore(key, proj.MaxConcurrentJobs)` 取到 0 = **无限并发**）。
    - `capture_diff: false` 的 project → worker 侧**不产出** diff（`job/outcomes.go:241`）。**先证伪**：不投 `CaptureDiff` → nil → **默认开** → 该测试必须红。
    - 反向断言（核实过、确实**不用**投）：`exchange_subdir` / `result_subdir` 空值**回落 worker 本地 `Storage` 默认**（`config/model.go` `ResolvedExchangeSubdir` / `ResolvedResultSubdir`）；`container_path` 不投（worker 无 `path_view` ⇒ `ExecPath` 恒取 `HostPath`）；`default_agent` 不投（worker 执行链不读它）。

15. **🔴 `policy_pending` 只换错误信息、**不新增拒绝路径**（H1）**：
    - pending 期间，**project 已在该 worker 的 caps 里** → job **照常受理**（**不得**因 pending 被拒）
    - pending 期间，project **不在** caps 里 → 错误信息是 **"worker w-x 尚未应用策略（policy_pending, rev=N）"**，而不是误导性的 `project "X" not on worker w-x`
    - **先证伪（这条最关键）**：把 pending 实现成"直接拒"→ 构造"worker 断连重连（Rev 清零 → 重进 pending）+ 同时有 workflow fan-out / cron 触发"→ 必须**真的**复现 workflow 被打挂（`job/workflow/advance.go:365`：任一 fan 的 `Submit` 失败 → `return err` → 整条 workflow fail；cron 同理 `serve/serve.go:704`）。**这是 v0.1 会引入的新可用性回归。**

16. **滚动升级矩阵（v0.3 按 B1 新语义改「POLICY×v3」两格）**（逐格给出预期，不能只写"兼容"）：
    | 组合 | worker.yaml 形态 / 状态 | 预期 | 怎么证明 |
    |---|---|---|---|
    | v4 server + v3 worker | 任意 | 不下发 Policy（版本闸）；worker 用本地 `projects`；能连能跑 | 用 `c3ee6d1` 编的 v3 worker 实测 |
    | v4 server + v2 worker | 任意 | 同上（连 reload 都没有） | 复用 P1 的 `4def378` 旧二进制 |
    | **v3 server + v4 worker** | **LEGACY**（有 projects、无 roots） | **完全正常**（Policy 本来就不来；worker 用本地 projects） | v3 server（`c3ee6d1`）+ P3 worker 实测 |
    | **v3 server + v4 worker** | **POLICY**，**冷启动、无缓存** | 手上无配置（projects=0）+ **醒目 `slog.Error`**："server 不支持策略下发（proto=%d<4）"，**不崩溃、仍在线** | 断言文案 + `projects==0` 但进程存活（靠 `Registered.ProtocolVersion`） |
    | 🔴 **v3 server + v4 worker** | **POLICY**，**已激活过（内存 `lastPolicy` / 有缓存）** | **保留 last-known-good，projects 不归零**（B1 v0.3 铁律：连了不给 Policy 的 server 也不清空） | 先给它激活一份 Policy（N 个 project）→ 断连 → 重连到 v3 server → 断言 `/v1/meta` 的 projects **仍是那 N 个**。**先证伪**：让「连 v3 ⇒ 清空」→ 必须红（归零） |
    | v4 server + v4 worker | 两种形态 | LEGACY 走本地、POLICY 走下发 | 主路径 |
    | 多 URL 混合（新旧 server） | POLICY | 轮到旧 server → 保留上一份、不归零；轮回新 server → 拿新 Policy 并应用；**新 server 重启后 Rev 从 1 也能被接受** | 双 server 隔离栈；断言 **Rev 回退/旧帧不会让 worker 永久丢弃新 Policy**（B4 session generation，验收 21） |

17. **hub 边界不破**：`go list -deps ./internal/wshub | grep gofer` 的输出**除 `internal/wshub` 自身外只有 `internal/wsproto`**。
    - **证明方式**：`go list -deps ./internal/wshub | grep gofer | grep -v '/wshub$'` → 输出**恰好一行** `github.com/inhere/gofer/internal/wsproto`。（v0.1 写"只有 wsproto"，逐字比对必**假红**——`go list -deps` 会带上自身。）
    - **先证伪**：把 policy 计算写进 wshub → 该命令必须多出 `internal/config`。

18. **worker 机器上的 CLI 不塌**：POLICY 形态的 worker 上，`gofer project list` 列出**当前生效的 project**（读 policy 缓存），`gofer config validate` **PASS**（不是因 0 projects 而 FAIL）；LEGACY 形态下两者行为**逐字不变**。

19. **原子性 + 无竞态**：`go test -race ./internal/core/... ./internal/wshub/... ./internal/worker/... ./internal/project/...` 绿。并发 `Submit × PolicyApply` 断言每次 Submit 要么完全看到旧配置、要么完全看到新配置（承接 P1 验收 8）；并发 `POST /v1/projects × Submit` 无 race（B2 的既存 race 被解掉）。

20. 全量 `go test ./... -p 1 -count=1 && go vet ./...` 绿；`git diff --check` 干净。

21. **🔴 跨连接 Policy 不复活（B4，session generation）**：executor 里堵着一个旧会话的 `Policy{Rev:100}` 未执行 → 完成新会话握手（新 gen，Rev 从小数）→ 旧 Policy **不得**在新会话生效。
    - **证明方式**：单测人为阻塞第一个 apply、把旧会话 `Rev:100` 排在前面，再完成新会话握手 → 断言新会话最终应用的是**新 server 的最新 Rev**，旧 `Rev:100` 被丢。
    - `go test ./internal/worker -run 'TestPolicyRevPerConnection|TestStaleGenerationPolicyDropped' -race -count=10`
    - **先证伪**：去掉 gen 判定（回到「register goroutine 直接清 lastRev」）→ 必须**真的**复现「新 server 的 Rev 1..99 被丢、worker 卡在旧策略」。

22. **🔴 队列满不丢最新 Rev（B5，latest-wins mailbox）**：阻塞 executor，连续推送**超过 `reloadQueueCap`（8）** 个递增 Rev → 解除阻塞后，worker 实际配置与 `AppliedRev` 必须**收敛到最大 Rev**（不是停在第 8 个）。
    - `go test ./internal/worker -run 'TestPolicyLatestWinsUnderBackpressure' -count=10`
    - **先证伪**：把 Policy 塞回有界 FIFO（满即丢）→ 该测试必须红（停在 N+8、丢 N+9）。

23. **🔴 MapRoot 不逃逸（B6）**：表驱动反向用例——逻辑路径含 `..`、`To` 含 `..`、root 内 symlink 指向 root 外 → 一律 `ok=false`（拒绝映射）；root 内正常 symlink → 命中。断言**任何情况下 mapped host 都在 real `To` 之下**。
    - `go test ./internal/config -run 'TestMapRootContainment|TestMapRootSymlinkEscape' -count=1`；同时 `GOOS=linux go test ./internal/config -run TestMapRoot`（映射是**逻辑路径解析器**，不依赖宿主 `filepath` 语义——Windows 盘符用例的可执行断言由 Windows CI 覆盖，不靠 `GOOS=windows go test`）。
    - **先证伪**：改回 v0.2 的「拼接后直接 `Clean`」→ `..` 与 `To`-含-`..` 两例必须红。

24. **🔴 二进制回滚安全（T6 迁移）**：迁移观察期内（`projects` 段**保留**、`roots` 已加）——把 worker 二进制回滚到 P3 前版本（`dcc98dd`）→ worker 仍用本地 `projects`、能连能跑（旧二进制不认 `roots`，只读 `projects`）。
    - 真实矩阵三格：`P3 worker + roots+projects` ✅ / `旧 worker + roots+projects` ✅（旧码忽略 roots）/ `旧 worker + roots-only` ❌ **预期失败、标为「不可回滚点」**（不能被描述成安全回滚）。

25. **🔴 T7 隔离栈 preflight（不碰 live 的机器护栏，codex-C BLOCKER-2/3）**：冒烟脚本**开头**必须对所有复制出来的配置做硬断言，任一命中即 `exit 1`：不含 `LIVE-PORT`、不含非 `127.0.0.1` 的 `server_link.urls`、不含 live 的 DB/storage/worker_id、`host_path` 全部落在 `<tmp>/projects/*`（脱敏合成 fixture，不复用 live `host_path`）。所有 CLI 显式带 `-c <隔离config> --server http://127.0.0.1:18899`；只 `kill` 脚本自己捕获的 PID。exec job 只跑无副作用命令、tty 用测试 fixture（不启动真实 claude/codex）。收尾断言所有 cwd/result/db/cache 均在隔离根下。

---

## 4. 现状事实（**每条附核实命令**）

> **纪律（P1/P2 三次血的教训）**：不附核实命令的"已核实"，三次里有两次是假的（v0.4 的 pin 前提、v0.5 的 proto v3、P2 的"只有一处 core-less 装配点"——全错）。**行号会漂，命令不会。**
> 设计 §13 的 **C1-C19** 已覆盖大部分。下面只列**直接决定 P3 任务形状**的，其余引用设计。

### 4.1 P3 要新建的东西（不是"复用已有的"）

| 事实 | 核实命令 | 含义 |
|---|---|---|
| **`roots` / `guards` 在代码里不存在** | `grep -n "type WorkerConfig" -A 10 internal/config/model.go` | `WorkerConfig` 只有 `worker_id/server_link/projects/agents/runners/max_concurrent/labels/storage`。**P3 新建**：YAML 结构 + defaults + validate + **最长前缀映射实现**（设计 §10-Q1 只定了语义，**零代码**） |
| **`wshub` 只 import `wsproto`** | `go list -deps ./internal/wshub \| grep gofer \| grep -v '/wshub$'` | 输出恰好一行 `internal/wsproto`。⇒ **推送目标计算（读 `cfg.Projects`/`cfg.Runners`）不能放 hub**。必须注入 seam（照 P1 `hubWorkerReloader` 的先例），实现放 `internal/core`（它已同时持有 cfg / hub / job）|
| **`Registered` ack 不带 server 版本** | `grep -n "type Registered" -A 6 internal/wsproto/frames.go` | 只有 `{Accepted, Reason, ServerTime}`。⇒ POLICY 模式 worker **分不清**「server 是 v3、永远不发 Policy」与「server 是 v4、这台的 Policy 恰好为空」。**必须补 `ProtocolVersion`**（B1 之后它只决定**告警文案**，不再决定权威归属）|
| **`Core.Cfg` 是裸字段，跨包并发读是 race** | `grep -n "c.Cfg = cfg" internal/core/core.go`；`grep -rn "cr.Cfg" internal/serve/serve.go`；`bd show tools-cg4` | `core.go:339` 裸写，`serve.go:390/496/674` 裸读。**已有 bd 记着**。⇒ PolicySource 要读**当前** cfg **+ Rev**，两者必须**一次原子读**（分两次会拿到 `(旧cfg, 新rev)` → worker 记下新 Rev 却应用了旧配置 → 真正的新 Policy 因 Rev 相同被丢弃 → **永久卡在旧配置**）。安全先例：`job.Service.Config()`（atomic）——但它**没有 Rev** |

### 4.2 🔴 B1–B6 的证据（对抗审查坐实）

| 事实 | 核实命令 | 含义 |
|---|---|---|
| **live worker.yaml 有 `projects`、无 `roots`；live server 的 project 没有一个把 `allowed_runners` 指向主机 worker** | `grep -c "^roots:" /path/to/ws-root/config/linux/gofer/worker.yaml`（→0）<br>`grep -n "^projects:" /path/to/ws-root/config/linux/gofer/worker.yaml`（→命中）<br>`grep -n "allowed_runners" -A 3 /path/to/ws-root/config/win-env/gofer/config.yaml` | **B1**：按 v0.1 的开关，换二进制即把两台 worker 双双打到 0 project |
| **`Registry.Add` 原地改 cfg，不走 `ReloadWith`** | `grep -n "func (r \*Registry) Add" -A 13 internal/project/registry.go`<br>`grep -n "project.NewRegistry" internal/core/core.go`（:119 同一指针）<br>`grep -n "s.projects.Add\|s.projects.Remove" internal/httpapi/project_handler.go`（:96/:124/:139）<br>`grep -rn "cr.Reload(" internal/serve/serve.go`（:839 唯一） | **B2**：web 写路径不 Rev++、不重推、还与 `job.Service` 并发写同一个 map |
| **`h.reg.Put(wc)` 在写 ack 之前；worker 握手不校验帧类型** | `grep -n "h.reg.Put(wc)" -A 12 internal/wshub/hub.go`<br>`grep -n "reg, _ := wsproto.As" -B 3 -A 6 internal/worker/client.go`<br>`grep -n "func As\[" -A 8 internal/wsproto/envelope.go` | **B3**：policy 帧插在 ack 之前 → `Accepted=false, Reason=""` → 重连风暴 |
| **ack 用包级 `writeEnvelope(ctx, conn, ...)`，绕过 `wc.writeMu`** | `grep -n "4) Ack" -A 5 internal/wshub/hub.go`<br>`grep -n "writeMu" -B 2 -A 6 internal/wshub/registry.go` | **新发现 N2**：Put-before-ack 下，ack 与被推送的帧是**两个并发 writer**（coder/websocket 明确禁止）。修法顺带解掉 |
| **`agent.Resolve` 原地改 `cfg.Agents`（delete + insert）** | `grep -n "func Resolve" -A 14 internal/agent/resolve.go` | ⇒ B2 的 copy-on-write **必须克隆 `Agents` map**（和 injected 标记），否则 Resolve 会改到旧快照，读者当场撕裂 |
| **`Registry.Remove` 与 `Add` 同罪；`Rev`/`Clone` 全仓不存在**（子代理证伪） | `grep -n "func (r \*Registry) Remove" -A 10 internal/project/registry.go`<br>`grep -rn "\bRev\b\|func .*Clone" internal/ --include=*.go \| grep -v _test`（→ 0） | **B2**：结论句只点名 `Add` ⇒ 实施者会漏掉 `DELETE` 路径（**撤白名单撤不掉**）；`Rev` 是 P3 从零引入；`Clone` 要**新建、深拷四张 map + 私有 `injectedAgents`**（唯一先例是浅拷贝，照抄会 race）|
| **`reloadLoop` 跨重连存活；`reloadCh` 容量固定 8、非阻塞** | `grep -n "go cl.reloadLoop\|func (cl \*Client) reloadLoop\|runSession(" internal/worker/client.go internal/worker/reload.go`<br>`grep -n "make(chan reloadReq\|enqueueReload" internal/worker/reload.go` | **B4**：旧会话 Policy 跨重连复活 → 需 session generation；**B5**：满即丢最新 Rev → Policy 用 latest-wins mailbox |
| **`SafeJoin` 只做词法检查、不 `EvalSymlinks`** | `grep -n "func SafeJoin" -A 30 internal/project/path.go`<br>`grep -rn "EvalSymlinks\|Lstat" internal/project internal/config`（→ 0） | **B6**：`MapRoot` 不能只靠 `SafeJoin` 兜底越界，映射本身必须做 `..`/symlink containment |

### 4.3 P3 会打穿的现网路径（不处理就是回归）

| 事实 | 核实命令 | 含义 |
|---|---|---|
| `workerConfigToConfig` 有**三个**调用点（v0.1 只提了两个） | `grep -rn "workerConfigToConfig" internal/ --include=*.go \| grep -v _test` | `commands/worker.go:268`（**启动**，v0.1 漏）、`:338`（reload）、`commands/config.go:514`（doctor）。**三处都要过模式判定** |
| `workerCaps` 的 projects 来自 `wc.Projects` —— **两个调用点** | `grep -n "func workerCaps" -A 8 internal/commands/worker.go`<br>`grep -n "workerCaps(" internal/commands/worker.go` | `mapKeys(wc.Projects)`。POLICY 模式下必须改成**投影后 `cfg.Projects`**。⚠️ **启动路径 `worker.go:276` 的那次调用 v0.1 没提** |
| `cl.storeCaps(caps)` 是**重连时 register 帧 caps 的唯一来源** | `grep -n "storeCaps\|currentCaps" -A 4 internal/worker/reload.go internal/worker/client.go` | ⇒ **Policy apply 必须复用 `runReload`**（P1 的串行执行器）。若另写一条 apply 路径，重连就会用**过期 caps** 注册 —— 静默、且只在重连时才暴露 |
| **SIGHUP 会重走 `workerConfigToConfig`** | `grep -n "func newWorkerReloadFn" -A 20 internal/commands/worker.go` | POLICY 模式下 `wc.Projects` 是空的 ⇒ **一次 SIGHUP 就把所有 project 清空** ⇒ worker **必须在内存里持有 `lastPolicy`**，两条路径共用同一 projection（验收 9） |
| worker 机器上 `gofer project list` **直读 `wc.Projects`** | `grep -n "func localProjects" -A 12 internal/commands/project.go` | POLICY 模式下**恒空**。CLI 是独立进程，看不到 worker 内存 ⇒ **必须落只读 policy 缓存文件** |
| worker doctor **0 project 直接 FAIL** | `grep -n "no projects (the worker has nothing to run)" -B 4 internal/commands/config.go` | POLICY 模式下**每台正常 worker** 的 `gofer config validate` 都会失败 |
| **pending 硬拒会打挂 workflow / cron** | `grep -n "e.ops.Submit(req)" -B 4 -A 3 internal/job/workflow/advance.go`<br>`grep -n "cr.Jobs.Submit(req)" -B 4 internal/serve/serve.go` | `advance.go:365` 任一 fan 的 submit 失败 → `return err` → 整条 workflow fail；cron（`serve.go:704`）同理。⇒ **H1：pending 不得新增拒绝路径** |

### 4.4 投影必须喂饱的字段（D6 + H2，逐个核实过）

| 读取点 | 核实命令 | 投影取值 |
|---|---|---|
| cwd 解析 | `grep -n "SafeJoin" internal/job/submit.go`；`grep -n "func SafeJoin" -A 12 internal/project/path.go` | `HostPath` = roots 映射后的本机路径。**映射不到 ⇒ 整条不进配置**（`path.go:30` `filepath.Abs("")` = 进程 CWD） |
| **并发上限（H2 新增）** | `grep -n "s.semaphore(req.ProjectKey" internal/job/submit.go` | `MaxConcurrentJobs` **必须投**（`submit.go:331`）。不投 ⇒ 恒 0 ⇒ **无限并发**（今天 worker.yaml 的 `max_concurrent_jobs` 是生效的 → 会静默失效） |
| **diff 采集（H2 新增）** | `grep -n "proj.CaptureDiff" -B 4 internal/job/outcomes.go` | `CaptureDiff *bool` **必须投**（`outcomes.go:241`）。不投 ⇒ nil ⇒ **默认开**（`capture_diff:false` 静默失效） |
| 结果目录 | `grep -n "func ResultBaseDir" -A 14 internal/project/path.go`；`grep -n "func (c \*Config) ResolvedResultSubdir" -A 8 internal/config/model.go` | **不投** `ExchangeSubdir`/`ResultSubdir`：空值回落 worker 本地 `Storage` 默认（这是**本机事实**，不该由 server 定）|
| agent 白名单 | `grep -n "AllowedAgents" internal/job/config.go` | **原样透传**（空 = 放行全部已配置 agent） |
| 交互白名单 | `grep -n "InteractiveAllowedAgents" internal/job/config.go` | **原样透传**；`guards.allow_interactive` 显式 false ⇒ **清空**（空 = 全禁，语义与上一行**相反**） |
| exec 闸 | `grep -n "not allowed: project" -B 2 internal/job/config.go` | `policy.AllowExec && guards.allow_exec`（护栏只收紧） |
| runner 准入 | `grep -n "func checkRunnerAllowed" -A 10 internal/job/config.go` | 恒为 `["local"]`（`dispatch.go` 强制 `Runner=local`；非空且不含 local 的列表会被拒） |
| `container_path` | `grep -n "func (c \*Config) ExecPath" -A 6 internal/config/model.go` | **不投**：worker 无 `server.path_view` ⇒ `ExecPath` 恒取 `HostPath` |
| `default_agent` | `grep -rn "DefaultAgent" internal/job/ internal/agent/` | **不投**：worker 执行链**不读**它（只有 CLI 展示 / httpapi / registry.Validate 读）|
| agent 定义 | `grep -rn "agent.Resolve" internal/core/core.go` | **不投影、不拼装**。交给 `core.ReloadWith` 里的 `agent.Resolve`（P2 建的唯一 merge 点）|

### 4.5 P2/P1 已经建好、P3 只消费的（**不要重做**）

- `agent.Resolve`（探测 → 只把探到的模板注入 `cfg.Agents`；逃生舱永不被摘）——唯一调用点 `core.Build` / `core.ReloadWith`。
- `Caps` 帧 + `reg.UpdateCaps` —— **唯一**的能力视图更新通路（`grep -n "UpdateCaps" internal/wshub/hub.go`）。⇒ **`Applied` 必须内嵌 `*Caps`**，走同一条路；`Rejected`/`Degraded` 只做诊断，**不参与路由判定**。
- P1 的**串行 reload executor**（`grep -n "func (cl \*Client) reloadLoop" -A 10 internal/worker/reload.go`）——⇒ **Policy apply 必须进同一个 `reloadCh` 队列**（并发 `ReloadWith` = 旧配置覆盖新配置，P1 T3 已解过一次）。
- pin=硬授权（`grep -n "pinned to worker" -B 12 internal/job/config.go` + `internal/job/pin_test.go`）——D4′ 的前提，仍成立。
- **白名单原样透传 ≡ 交集**（审查实测复核）：`ReloadWith` 一份 `AllowedAgents:[tty-codex]` 而 `cfg.Agents` 为空的 cfg → **err=nil，逐字保留**；`project.Registry.Validate`（会报 "agent not defined"）**只在 doctor CLI 里调、不在 `ReloadWith` 路径上**。⇒ **T6-D 的"不做交集"是对的。**

---

## 5. 任务分解

### 依赖图（v0.3 补 T2→T5）

```txt
T0 (wsproto v4 帧 + 握手加固) ─┬─→ T1 (core: 快照/串行写入口/seam) ─→ T3 (computePolicy) ─→ T4 (hub 下发 + pending) ─┐
                               │                                                                                  ├─→ T7 (e2e 冒烟)
                               └─────────────┐                                                                    │
T2 (worker.yaml roots + guards + MapRoot) ───┴─→ T5 (worker 模式判定/投影/应用) ─→ T6 (CLI + example + 迁移文档) ─┘
```

- **T5 依赖 T2**（codex-C HIGH-1：`projectPolicy` 调 `wc.MapRoot`，v0.2 依赖图漏了这条）**和 T0**（消费 `wsproto.Policy` 帧）。
- **可并行**：`T0 ∥ T2`（不同包，零重叠）；`T1/T3/T4`（server 链：`core`/`wshub`/`job`）**∥** `T5/T6`（worker 链：`worker`/`commands`）—— 文件不重叠，审查已复核。**但 T5 必须等 T2 的 MapRoot 落地**（否则投影无法映射路径）。
- **T3 不依赖 T1 的全部**，只依赖 T1 的 seam 接口与 `Snapshot()`；`corePolicySource` 的接线在 T3。
- **端到端验收归 T7**（codex-C HIGH-1/2）：T1 阶段只能用 **spy** 证明「web 写调用了 push seam exactly once / 失败不 push」，T4 阶段只能用 **fake worker** 证明协议帧序，真正「web 改 → worker 出现 key」的闭环在 T7 跑。**任务正文里标 "端到端" 的断言都指向 T7，不要求上游任务独立复现。**

---

### 🔴 T0 协议地基：proto v4 帧 + **握手加固（B3）**

**T0-A proto v4 帧**

```go
// internal/wsproto/frames.go
const CurrentProtocolVersion = 4              // 从 3 提到 4（MinProtocolVersion 保持 2，不踢任何存量 worker）
const PolicyMinProtocolVersion = 4            // 照 ReloadMinProtocolVersion 的模式
func SupportsPolicy(proto int) bool { return proto >= PolicyMinProtocolVersion }

type PolicyProject struct {
    Key                      string   `json:"key"`
    HostPath                 string   `json:"host_path"`            // 逻辑路径
    AllowedAgents            []string `json:"allowed_agents"`       // computePolicy 保证【非 nil】(见 T3)；下游把 null 与 [] 视为等价(MEDIUM-1)
    InteractiveAllowedAgents []string `json:"interactive_allowed_agents"`
    AllowExec                bool     `json:"allow_exec"`
    MaxConcurrentJobs        int      `json:"max_concurrent_jobs,omitempty"` // ★ H2：不投 = 恒 0 = 无限并发
    CaptureDiff              *bool    `json:"capture_diff,omitempty"`        // ★ H2：*bool —— 不投 = nil = 默认开
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
// Registered 追加（Q7-b）：
ProtocolVersion int     `json:"protocol_version,omitempty"` // server 实现的版本；旧 server 解出 0
Policy          *Policy `json:"policy,omitempty"`
```

- 新 FrameType：`TypePolicy = "policy"`（s→w）/ `TypeApplied = "applied"`（w→s）。
- **`Policy.Agents` 不存在**（Q6）。`guards.allow_custom_agents` 不存在。**任何"顺手加上以备将来"都是把 D1 的边界重新捅穿。**
- **`Applied` 不得另起能力上报通路**：`Caps` 内嵌，hub 收到后走**同一个** `reg.UpdateCaps`；`Rejected`/`Degraded` 只挂在 worker 记录上给 Cluster 页看，**不参与路由判定**。
- `CaptureDiff` 用 `*bool` 的理由与 P2 的 `AgentBrief.Available` 同款：**"未设" ≠ "显式 false"**。

**T0-B 握手加固（B3；与 Policy 无关，可独立回归、先落地）**

1. **hub**：`h.reg.Put(wc)` **移到 ack 写成功之后**；ack 改用 `wc.writeFrame(...)`（走 `writeMu`，单 writer 语义，见 §7-N2）；ack 失败 → 直接 close，**不再需要** `h.reg.Remove`（从未 Put）。
2. **worker**：register 握手**断言 `env.Type == wsproto.TypeRegistered`**，`wsproto.As` 的 **error 不再丢弃**；不符即返回明确错误（"handshake: expected registered frame, got %q"）。

**验收（T0）**：
- 验收 6（**先证伪**：恢复 Put-before-ack + 注入一次 PushPolicyAll → 必须复现 `registration rejected`、reason 为空、退避重连）。
- `MinProtocolVersion` 仍是 2 → v2/v3 worker 注册**不被踢**（矩阵单测，复用 P1 的 `hub_version_test.go` 模式）。
- 旧 server 的 `Registered` 帧解码后 `ProtocolVersion == 0`（`As[T]` 无 `DisallowUnknownFields`，单测钉死）。
- `PolicyProject` 的空 `allowed_agents`：`computePolicy` 输出**非 nil** 空 slice（`[]string{}`）⇒ wire 上是 `[]`。⚠️ **但下游必须把 `null` 与 `[]` 视为等价**（codex-B MEDIUM-1：Go nil slice **无 `omitempty` 也序列化成 `null`**，v0.2「无 omitempty ⇒ 一定是 []」是假的；当前下游用 `len` 判断，两者语义相同、无害）。单测两种 wire 形态都接受。

**提交**：`feat(wsproto): protocol v4 policy frames and harden the register handshake (P3 T0)`

---

### 🔴 T1 core 地基：`(cfg, rev)` 原子快照 + **串行写事务（B2）** + PolicySource seam

**T1-A 一次原子读拿到 `(cfg, rev)`**

```go
// internal/core
type ConfigSnapshot struct {
    Cfg *config.Config
    Rev int64            // 配置代次；Build=1，每次 ReloadWith +1
}
func (c *Core) Snapshot() ConfigSnapshot   // 单次 atomic.Pointer 读
func (c *Core) Config() *config.Config     // = Snapshot().Cfg（给 serve 的三处裸读用）
```

- `Core.Cfg` 裸字段**改为 accessor**，`serve.go` 的三处裸读（`:390` / `:496` / `:674`）跟着改 → 顺手关掉 bd `tools-cg4`。

**T1-B 🔴 串行写事务 + mutation seam（B2 的修法，v0.3 收紧）**

```go
// internal/config：copy-on-write 的深拷贝（cfg.Clone 今天【不存在】，新建）
func (c *Config) Clone() *Config
//   结构浅拷 + 【深拷四张 map】Projects / Agents / Runners / Roles + 私有 injectedAgents 标记。
//   ⚠️ 唯一先例 writer.go 的 withoutInjectedAgents 是【浅拷贝】——照抄会让 ReloadWith 里的
//      agent.Resolve 去 delete 与旧快照【共享】的 Agents map，正在跑的 Submit 当场撕裂（子代理证伪）。

// internal/core：所有改 cfg 的路径都走它，全程持 updateMu（串行写事务）
func (c *Core) Update(mut func(*config.Config) error) error {
    c.updateMu.Lock(); defer c.updateMu.Unlock()
    next := c.snap.Load().Cfg.Clone()                       // 读旧快照 → 深拷
    if err := mut(next); err != nil { return err }          // 失败即返回，旧配置纹丝不动、不 save、不发布
    if err := config.Save(c.cfgPath, next); err != nil { return err }  // 写【副本】，失败不发布
    c.reloadLocked(next)                                    // 换指针 + Rev++ + 重推（T1-D，已持锁）
    return nil
}
```

- 🔴 **必须有 `updateMu`**（codex-A/C）：否则两个并发 `POST`/`DELETE` 从**同一快照** clone、后写覆盖前写，两份不同 cfg 拿到**同一个 Rev**（验收 5 的并发格）。**`Core.Reload`（SIGHUP）也走 `updateMu`**——否则 `Update` 与 SIGHUP 并发仍撞。
- 🔴 **Registry 只交 mutation、不构造 next**（codex-C：v0.2 让 Registry 自己 clone+save 再 apply，那条路径**绕开了 `updateMu`**、锁不住）。seam 传 `func(*config.Config) error`，clone/save/reload 全由 Core 在锁内做：
  ```go
  // internal/project（G022：数据层不能 import core）
  type Mutate func(mut func(*config.Config) error) error   // 注入 = c.Update；nil = 独立 CLI 进程，退化为原地 clone+save+Store
  func NewRegistry(cfg *config.Config, path string, opts ...Option) *Registry
  func WithMutator(m Mutate) Option
  ```
  🔴 **`Add` 与 `Remove` 都改**（子代理：结论句只点名 `Add` ⇒ 漏 `DELETE` ⇒ **撤白名单撤不掉**）：`r.mutate(func(next){ next.Projects[key]=proj /* 或 delete(next.Projects,key) */; return nil })`。
- 接线：`core.Build` 里 `project.NewRegistry(cfg, cfgPath, project.WithMutator(c.Update))`；Core 持 `cfgPath` 供锁内 `config.Save`。
- **顺带修掉既存缺陷**：今天是"先原地改内存、再 save"，save 失败内存已脏；现在 save 写副本、失败不发布。（`config.Save` 的 `managedTopKeys` 会固化 overlay/CLI-flag，是独立问题，本期只保证写 next，见 §7-N5。）

**T1-C 边界（写死，防实施者发挥）**

- **不许**再有第二条"直接改 `Core.Cfg` / 改 `cfg.Projects` map"的路径。code review checklist：**任何对 `*config.Config` 的写，只能经 `Core.Update`。**
- `Registry.Validate` 等只读路径不动。

**T1-D Rev++ 与 PushPolicy 都放进 `reloadLocked`（结构性保证，`updateMu` 保护下）**

```go
// 已持 updateMu；Core.Update 与 Core.Reload 都调它。避免重入：公开的 ReloadWith = updateMu.Lock() + reloadLocked()。
func (c *Core) reloadLocked(cfg *config.Config) {
    cfg, detected := agent.Resolve(cfg, c.detector)
    rev := c.snap.Load().Rev + 1
    c.snap.Store(&ConfigSnapshot{Cfg: cfg, Rev: rev})   // ★ 一次原子换代
    c.Projects.Reload(cfg); c.Agents.ReloadWith(cfg, detected); c.Jobs.Reload(cfg)
    c.pushPolicyAll()                                    // ★ 任何配置变更都推，路径不可能"忘记"
}
```

- **重入陷阱**：`Core.Update` 已持 `updateMu`，其中调的换代逻辑**不能再自己抢锁** → 抽成 `reloadLocked`（假定已持锁）；`Core.Reload`（SIGHUP）与公开 `ReloadWith` 是 `Lock()` + `reloadLocked()` 的薄封装。
- worker 进程也有自己的 core，`reloadLocked` 同样跑——但它的 hub 是空的（无 worker 连接），`pushPolicyAll()` 天然是 no-op。worker 侧 core 的 Rev **无人读**（worker 用的是 server 的 policy rev），不冲突。
- SIGHUP（`serve.go:839` → `cr.Reload(path)`）**不必**再显式调推送。

**T1-E PolicySource seam（hub 不能 import config —— §4.1）**

```go
// internal/wshub（只依赖 wsproto，边界不破）
type PolicySource interface {
    PolicyFor(workerID string) (wsproto.Policy, bool) // ok=false ⇒ 不下发（source 未接 / 该 worker 无策略）
}
func (h *Hub) SetPolicySource(ps PolicySource)
func (h *Hub) PushPolicyAll()                        // 遍历 reg.All()，只发给 SupportsPolicy 的连接
```

实现 `corePolicySource` 放 `internal/core`（照 `hubWorkerSelector` 的先例，同文件邻位）——T3 接线。

**验收（T1）**：
- 验收 5（Rev 是真代次：四条写路径都 +1；`-race` 断言 `(Cfg,Rev)` 同代）
- 验收 4（web 改 project 也重推）—— **先证伪**：把 `Add` 改回原地改 → 必须红
- 验收 17（`go list -deps ./internal/wshub | grep gofer | grep -v '/wshub$'` **恰好一行** wsproto）—— **先证伪**：把 policy 计算写进 wshub → 必须多出 `internal/config`
- 验收 19（`POST /v1/projects × Submit` 并发无 race）—— **先证伪**：撤掉 copy-on-write → `-race` 必须报 map 并发读写

**提交**：`feat(core): single config write entry with atomic (cfg,rev) snapshot and hub policy seam (P3 T1)`

---

### T2 worker.yaml 新字段：`roots` + `guards`（含最长前缀映射）

**T2-A `config.WorkerConfig` 加字段**

```go
type WorkerRoot struct {
    From string `yaml:"from"` // server 侧逻辑路径前缀
    To   string `yaml:"to"`   // 本机路径前缀
}
type WorkerGuards struct {
    // *bool，不是 bool —— 见 T2-D。nil = 未设 = 不额外收紧（等价于今天的行为）。
    AllowExec        *bool `yaml:"allow_exec,omitempty"`
    AllowInteractive *bool `yaml:"allow_interactive,omitempty"`
}
// WorkerConfig 追加：
Roots  []WorkerRoot `yaml:"roots,omitempty"`
Guards WorkerGuards `yaml:"guards,omitempty"`
```

**T2-B 最长前缀映射（设计 §10-Q1 的语义，第一次落成代码）**

```go
func (wc *WorkerConfig) MapRoot(logical string) (host string, ok bool)
```

- 归一化：`\` → `/`；去尾斜杠；**Windows 侧大小写不敏感**（`D:/work` == `d:/work`），Linux 侧敏感。
- **最长 `From` 优先**；**边界必须对齐**（`/a/b` 不得匹配 `/a/bc` —— 只有 `完全相等` 或 `logical[len(from)] == '/'` 才算命中）。
- 命中后：`host = To + logical[len(From):]`，再 `filepath.Clean`。
- 🔴 **containment 校验（B6，两端都做，不能只靠字符串前缀 / `SafeJoin`）**：
  - **逻辑侧**：`logical` 规范化后若清理出 `..` 逃出所匹配的 `From`（如 `/root/../outside`）⇒ `ok=false`。
  - **本机侧**：拼出 `host` 后 `rel, _ := filepath.Rel(cleanTo, cleanHost)`；`rel` 以 `..` 开头或为绝对路径 ⇒ `ok=false`。
  - **symlink**：`host` 已存在部分用 `filepath.EvalSymlinks`，比较 real `To` 与 real `host`（不存在的尾部从最近存在父目录解析）；real `host` 不在 real `To` 下 ⇒ `ok=false`。定位是**防误配**（worker 信任 server，设计 §8），但 `..` 逃逸会实打实落错目录，必须防。
- **不中任何 root ⇒ `ok=false`。绝不返回空串当"成功"。**
- 🔴 **"更长的 `from` 覆盖更短的 `from`" 是一等场景，不是边角**（H3 的决策靠它承载，见 §6）：
  ```yaml
  roots:
    - { from: D:/work/x,            to: /d/work/x }        # 通配根
    - { from: D:/work/x/proj-a,     to: /d/work/x/proj-b }  # 例外：更长 → 命中它
  ```
  必须有表驱动用例覆盖：重叠 root 的**子路径**也走例外分支（`D:/work/x/proj-a/sub` → `/d/work/x/proj-b/sub`）。

**T2-C 边界（写死，防实施者发挥）**

- roots **不做**远程改写、**不做**任何 API 暴露（D3 推论：加 root = 扩大该机可执行范围，**故意**要求机器访问权）。
- 🔴 `..` / 符号链接逃逸**由 `MapRoot` 自己拦**（T2-B 的 containment，B6）——**不能**只依赖 `project.SafeJoin` 兜底（子代理/codex-B 核实：`SafeJoin` 只做词法 `Abs/Clean/Rel`、**不 `EvalSymlinks`**，兜不住 symlink）。`SafeJoin` 仍作第二道，但**不是**唯一防线。

**T2-D `guards` 缺省语义（⚠️ 与设计 §6.1 的"默认 false"有意偏离，理由写死）**

- 设计 §6.1 写 `guards.allow_exec` **默认 false**（安全默认）。**本计划改为 `*bool`，nil（未设）= 不额外收紧**。
- **理由**：现网 worker.yaml **没有 `guards` 段**、3 个 project 全 `allow_exec: true`。裸 `bool` 零值 = false ⇒ **升级二进制那一刻，所有 exec job 与所有 pty job 立刻被护栏拒掉** —— 直接违反验收 1。
- `*bool` 是本仓既有手法（`ProjectConfig.CaptureDiff` / `NotifyEnabled` / `AgentBrief.Available` 全是这个理由）。
- 代价：护栏是 **opt-in**。⇒ `worker.example.yaml` 里**显式写出 guards** 并注释；worker doctor 在未设时**给 WARN**（不 FAIL）。

**验收（T2）**：表驱动单测：多 root 最长优先 / **重叠 root 的例外分支（含子路径）** / 边界不对齐不命中（`/a/b` vs `/a/bc`）/ Windows 盘符 + 大小写 / 尾斜杠 / 映射失败返回 `ok=false` / **任何情况下不返回空 host** / **🔴 containment 反向用例（验收 23：逻辑含 `..`、`To` 含 `..`、root 内 symlink 出界一律 `ok=false`）**。映射是**逻辑路径解析器**，不依赖宿主 `filepath` 语义 ⇒ 同一组 fixture 在 `GOOS=linux` 下也要过。

**提交**：`feat(config): worker roots + guards with longest-prefix mapping (P3 T2)`

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

- `HostPath` 取 `proj.HostPath`（server 侧的 host_path **就是**逻辑路径）。`ContainerPath` **不下发**。
- `MaxConcurrentJobs` / `CaptureDiff` **随投**（H2）。
- `AllowedAgents` / `InteractiveAllowedAgents` 输出 **非 nil 空 slice**（`[]string{}`，不是 nil）—— wire 上是 `[]`（MEDIUM-1）。
- project key 先过 `checkProjectKey` 同款字符集校验（key 会变成 worker 上的目录名）。
- 接线 `corePolicySource`：`PolicyFor(wid)` = `snap := c.Snapshot(); return computePolicy(snap.Cfg, wid, snap.Rev), true` —— **一次原子读**（T1-A），不许分两次取 cfg 与 rev。

**验收（T3）**：
- **反向测试（验收 12）**：空 `allowed_runners` 的 project → `computePolicy` 对**每台** worker 都返回 0 个该 key。**先证伪**：改成"空=全推" → 必须红。
- pin 到 `w-a` 的 project **不出现**在 `w-b` 的 Policy 里；池型 runner 的 project → 两台 worker 都拿到；`allowed_runners: [local]` → 不推给任何 worker。

**提交**：`feat(core): compute per-worker policy from runner reachability (P3 T3)`

---

### T4 hub 下发（ack 带 Policy + reload 广播 + Applied + policy_pending）

**T4-A ack 同一次写带 Policy（Q7-b）**：`hub.Accept` 的 ack 步骤（**已在 T0-B 里挪到 Put 之前**）：

```go
ack := wsproto.Registered{Accepted: true, ServerTime: ..., ProtocolVersion: wsproto.CurrentProtocolVersion}
ackedRev := int64(0)
if h.policySrc != nil && wsproto.SupportsPolicy(reg.ProtocolVersion) {
    if p, ok := h.policySrc.PolicyFor(reg.WorkerID); ok {
        ack.Policy = &p; ackedRev = p.Rev
        wc.markPolicyPending(p.Rev)          // Q7-a（诊断用；不新增拒绝，见 T4-D）
    }
}
if err := wc.writeFrame(ctx, wsproto.TypeRegistered, "", ack); err != nil { ...close... }
h.reg.Put(wc)                                 // ★ B3：Put 在 ack 之后
h.catchUpPolicy(wc, ackedRev)                 // ★ N1：见下
```

**T4-B 🔴 catch-up push（重写时新发现，§7-N1）**：`Put` 之后**必须**再看一眼当前 Rev —— 因为在"ack 算好"与"Put"之间发生的 `PushPolicyAll` **看不到这条连接**（它还没进注册表），worker 会**永久停在 ack 里那个旧 Rev**。

```go
func (h *Hub) catchUpPolicy(wc *workerConn, ackedRev int64) {
    if p, ok := h.policySrc.PolicyFor(wc.workerID); ok && p.Rev > ackedRev {
        _ = wc.writeFrame(ctx, wsproto.TypePolicy, "", p)   // 幂等；worker 侧 rev>lastRev 才应用
    }
}
```

**T4-C server reload 后重推**：`Hub.PushPolicyAll()` 遍历 `h.reg.All()`，对 `SupportsPolicy()` 的连接发 `TypePolicy`。**调用点唯一：`core.reloadLocked` 末尾**（T1-D，`Update`/`Reload`/`ReloadWith` 都经它）——不在 `serve.startReloadLoop` 里另写一处（那样 web 写路径又会漏）。

**T4-D 收 `TypeApplied`**：`readLoop` 加分支 —— **照 `TypeReloadResult` 的先例**：

```go
case wsproto.TypeApplied:
    a, err := wsproto.As[wsproto.Applied](env); if err != nil { continue }
    if a.Caps != nil { h.reg.UpdateCaps(wc, *a.Caps) }          // ★ 唯一的能力通路
    h.reg.MarkPolicyApplied(wc, a.Rev, a.Rejected, a.Degraded)  // 诊断，非路由
```

- `UpdateCaps` 的"旧连接迟到帧不得污染新连接"检查（`r.conns[wc.workerID] != wc → return`）**天然覆盖** Applied —— `MarkPolicyApplied` **必须走同款检查**，别新写一套。

**T4-E 🔴 policy_pending 只换错误信息（H1；v0.1 在这里新增了一道拒绝，是错的）**：

- `WorkerSnapshot` / `job.WorkerCandidate` 加 `PolicyPending bool` + `PolicyRev` / `AppliedRev`（经 `core.workerCandidate` 填）。
- 落点**只有一处**：`internal/job/config.go` 里**已经存在**的那道闸——
  ```go
  if !slices.Contains(wprojs, req.ProjectKey) {
      if cand.PolicyPending {   // ★ 只改文案
          return ..., fmt.Errorf("%w: worker %q 尚未应用策略（policy_pending, rev=%d）", ErrUnknownProjectOnRunner, wid, rev)
      }
      return ..., fmt.Errorf("%w: project %q not on worker for runner %q", ErrUnknownProjectOnRunner, ...)
  }
  ```
- 🔴 **不得**在 `capabilitiesFor` / 任何地方**新增**"pending ⇒ 直接拒"的分支。理由（审查坐实）：
  - `wc` 是 per-connection、Rev per-connection 清零 ⇒ **每次网络抖动重连都会重进 pending**；
  - 而 worker 在此窗口**仍持有上一份完全有效的 cfg**（`ReloadWith` 原子换指针，**从不清空**），dispatch 过去照样能跑；
  - 硬拒会**打挂在跑的 workflow / cron**（`workflow/advance.go:365` / `serve.go:704`）——**这是新增的可用性回归**（今天 P1/P2 重连后 register 带 `cl.currentCaps()`，job 无缝继续）。
- ⚠️ **只对 `SupportsPolicy(proto)` 的 worker 置 pending**（v3 worker 永远不 pending）；**LEGACY 模式 worker 的 Applied 会立刻清 pending**（T5-A），所以它的错误文案不会变味。

**验收（T4，协议帧序用 fake worker，不依赖 T5 —— codex-C HIGH-2）**：
- 验收 15（**先证伪**：把 pending 实现成硬拒 → 重连 + workflow fan-out 必须**真的**被打挂）
- 验收 7（catch-up：**先证伪**去掉 → fake worker 必须永久停在旧 Rev）—— 用 fake websocket worker 断言帧序：`go test ./internal/wshub -run 'TestRegisterCatchUpPolicy|TestLateAppliedCannotReplaceNewConnection' -count=100`
- `-race`：Applied 与 `WorkerSnapshot` 并发无 race；旧连接迟到的 Applied 不污染新连接（复用 P1 T4 的测试模式）
- v3 worker **不被**标 pending（回归测试）

**提交**：`feat(wshub): push policy on register/reload and surface policy_pending (P3 T4)`

---

### 🔴 T5 worker 侧模式判定 + 投影 + 应用（**本期最容易翻车的任务**）

**T5-A 🔴 模式判定 + POLICY 有效配置解析（B1 v0.3；三条 `workerConfigToConfig` 路径共用）**

静态模式（看 yaml，三分支不变）：
```go
// internal/commands
type workerMode int  // modeLegacy / modePolicy / modeEmpty
func workerModeOf(wc *config.WorkerConfig) workerMode
//   Roots==0 && Projects>0  → modeLegacy  （忽略 Policy，用 wc.Projects；slog.Warn 引导迁移）
//   Roots>0                 → modePolicy  （Policy 权威；wc.Projects 非空则 slog.Warn 提示删）
//   Roots==0 && Projects==0 → modeEmpty   （slog.Error：本 worker 不会接到任何 job）
```

🔴 **但 POLICY 模式的「有效配置来源」是运行期动态的**（B1 v0.3 铁律：**永不因连了不给 Policy 的 server 而清空**）：
```txt
有效 policy = 内存 lastPolicy（本会话已收到的） ?? 冷启动从缓存恢复的 last-known-good ?? nil
    有  ⇒ projectPolicy(wc, effPolicy)
    nil ⇒ 空配置（此刻尚未注册，server 不派 job）+ 按 ack.ProtocolVersion 出告警文案
【关键】清空/替换只发生在 apply 一份【更高 Rev 的新 Policy】时（原子）；连 v3 / 掉线 / Policy 恰空 都保留手上这份。
```

三个调用点**都要过**（§4.3，v0.1 漏了启动那条）：`commands/worker.go:268`（启动）、`:338`（reload）、`commands/config.go:514`（doctor）。

- **LEGACY 收到 Policy**：不应用，但回 `Applied{Rev, Caps:<本地>, Degraded:[{"*","legacy_local_projects"}]}`（清 pending + `/v1/meta` 可见 + caps 逐字不变，验收 1）。
- **EMPTY**：照旧（今天这种 worker 也跑不了东西），告警更响。

**T5-B worker 内存持有 `lastPolicy`（不做这条，验收 9 必挂）**

POLICY 模式下有效配置 = `project(worker.yaml, lastPolicy)`，**两个输入独立变化**：

| 触发 | 动作 |
|---|---|
| 收到 Policy 帧 / ack 里的 Policy | 存 `lastPolicy` → 用**当前 worker.yaml** 重新投影 |
| SIGHUP / `gofer worker reload` | 重读 worker.yaml → 用**内存里的 `lastPolicy`** 重新投影 |

⇒ **两条路径共用同一个 projection 函数、同一个 P1 串行 executor。**
⇒ 不存 `lastPolicy` ⇒ SIGHUP 后 `workerConfigToConfig` 拿到空 `wc.Projects` ⇒ **worker 一个 project 都没有、彻底停摆**（静默、必然）。

**T5-C 进 P1 串行 executor（不新造应用路径 —— M1）+ session generation（B4）+ latest-wins mailbox（B5）**

```go
// internal/worker/reload.go：扩展既有 reloadReq，不新建 goroutine、不新建 executor。
type reqKind int  // sessionStart | sighup | remoteReload   （policyApply 走 mailbox，不入 FIFO）
type reloadReq struct {
    requestID string    // 远程 reload 回执 id（空 = SIGHUP）
    reason    string
    kind      reqKind
    gen       uint64    // ★ B4：本请求所属会话世代
}
// 注入的 Reload seam（G021：commands owns "how to read worker.yaml"）：
//   Reload func(p *wsproto.Policy) (ReloadOutcome, error)
//   ReloadOutcome{ Caps wsproto.Caps; AppliedRev int64; Rejected []...; Degraded []... }
```

🔴 **B4 session generation**：每次成功握手 → `gen++`，投一个 `sessionStart{gen}` 事件进 executor（它清本会话 `lastRev`）。executor 执行 policyApply 前**断言 `mailbox.gen == curGen`**，非当前 gen 一律丢弃。**register goroutine 绝不直接写 executor 独占的 `lastRev/lastPolicy`**（那是 race）——一切经事件/mailbox 入队、由**单** executor 处理。

🔴 **B5 latest-wins mailbox**：Policy **不塞进有界 `reloadCh`**（容量 8，满即丢最新 Rev）。executor 持一个 `pendingPolicy`（`atomic.Pointer` 或小锁保护，只留**当前 gen 的最大 Rev**）+ 唤醒信号。收 `TypePolicy` → `rev>已存 rev` 才换 → 唤醒。executor 每轮：先排空 `reloadCh` 的 sighup/remote/sessionStart（保序），再看 mailbox 有没有待应用 Policy。**SIGHUP 与 Policy 仍同一 executor 串行**（定序不变），只是多个 Policy 之间 latest-wins（不会为每个中间 Rev 各跑一次 ~2s 的 apply）。

- `recvLoop` 收 `TypePolicy` → 只更新 mailbox；收 `TypeReload` → 入 `reloadCh`（P1 同款）。
- 🔴 **`runReload` 是唯一执行体** —— `cl.storeCaps(caps)` 是**重连时 register 帧 caps 的唯一来源**（§4.3）。另写 apply 路径 = 重连用过期 caps 注册（静默，只在重连暴露）。
- `lastPolicy` / `curGen` 由 executor 单 goroutine 独占（写清注释）；mailbox 是**唯一**的跨 goroutine 投递点，只它需要同步。
- 回执：policy apply → `TypeApplied`；SIGHUP → `TypeCaps`（P1 既有）；远程 reload → `TypeReloadResult`（P1 既有）。

**T5-D Rev 语义（per-connection；清零走 `sessionStart` 事件，不是 register goroutine 直接改 —— B4）**

- **Rev 状态 per-connection**：由 `sessionStart{gen}` 事件**在 executor 内**把 `lastRev` 清零 —— **不是** register goroutine 直接写（register goroutine 与 executor 是两个 goroutine，直接写就是 B4 的 race + 旧帧污染）。
- **理由**：Rev 是**某个 server 进程**的配置代次；server 重启后从 1 重数 → 跨连接保留旧 `lastRev` 会把新 server 的 rev 全当"旧的"丢弃、永久卡住（验收 16 最后一格 / 验收 21）。
- 会话内：`rev > lastRev` 才 apply（乱序保护 + catch-up 幂等）；**旧 gen 的 Policy 先按 gen 丢弃**，根本轮不到比 Rev。
- **代价（接受）**：每次重连重跑一次投影 + detect（P2 实测最坏 2.0s、典型 ms）。不做 payload 指纹去重（§8）。

**T5-E 投影函数（放 `internal/commands/worker.go`，与 `workerConfigToConfig` 同处 —— P1 先例）**

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
| **`MaxConcurrentJobs`** | **原样透传**（H2；不投 = 无限并发） |
| **`CaptureDiff`** | **原样透传** `*bool`（H2；不投 = 默认开） |
| `ExchangeSubdir` / `ResultSubdir` / `Storage` | **不投** —— 走 `workerConfigToConfig` 的既有逻辑（空值回落 worker 本地 `Storage` 默认，§4.4 已核实） |
| `ContainerPath` / `DefaultAgent` | **不投**（§4.4 已核实：worker 执行链不读） |
| agent 定义 | **不动** —— `cfg.Agents` 只放 worker.yaml 的逃生舱，其余交给 `ReloadWith` 里的 `agent.Resolve` |

**🔴 边界（写死，防"优化"）**：
- **不做**白名单与 `cfg.Agents` 的**交集**。理由（设计 D6 v0.7 + 审查实测复核）：① 交集必须在 `ReloadWith` 之后算，而那时 cfg 已发布给 `job.Service`，回头改 = data race；② `AllowedAgents` 空 = **放行全部**，`InteractiveAllowedAgents` 空 = **全禁**，语义相反——交集算出空列表会**静默放开所有 agent**。透传的准入结果与交集**完全一样**（`agent.ResolveAgent` 找不到定义 → `unknown agent`），错误信息还更准。**验收 13 是这道墙。**
- **不做** `cfg.Agents = ...` 自己拼装（绕过 P2 的 detect gate）。

**T5-F 应用 + 回报 + last-known-good 缓存**

```txt
cfg, rejected := projectPolicy(wc, effPolicy)   // effPolicy = 内存 lastPolicy ?? 缓存恢复（T5-A）
cr.ReloadWith(cfg)                              // = updateMu.Lock()+reloadLocked；P2 的唯一 merge 点在这跑 agent.Resolve
degraded := diagnose(cfg, effPolicy, wc)        // 【只读】：policy 允许但本机没装的 agent / 被 guards 收紧的 exec/interactive
caps := workerCaps(wc, cfg, det.snapshot())     // ★ POLICY 模式下 Projects 改从 cfg.Projects 取
writeApplied(Applied{Rev, &caps, rejected, degraded})
writePolicyCache(effPolicy)                     // ★ last-known-good：同目录 temp+rename、0600；写失败只 WARN、不回滚已应用配置
```

- 🔴 **`workerCaps` 两个调用点都改**（§4.3）：`worker.go:276`（**启动**）+ reload 路径。LEGACY 取 `mapKeys(wc.Projects)`；POLICY 取 `mapKeys(cfg.Projects)`。
- `degraded` 在 `ReloadWith` **之后**算 —— 只读诊断、不回写配置，无 T5-E 的顺序陷阱。
- 🔴 **冷启动 last-known-good（B1 v0.3）**：POLICY 模式启动、内存无 `lastPolicy` 时，读 `<config-dir>/run/worker-<id>.policy.json`（含 `worker_id` + `rev` + `written_at`）→ 校验 worker_id 一致、JSON 完好 → 作为 effPolicy 投影（**重启无缝**）；损坏 / worker_id 不符 → WARN 后当作 nil（不 panic）。
- **缓存契约（codex-C MEDIUM-3）**：`writePolicyCache` = 同目录 temp 文件 `0600` + 原子 rename；写失败**不**回滚已 Applied 的配置；CLI（T6）读到半文件 / 旧 worker_id → WARN，不 panic。

**验收（T5）**：
- 验收 2（三分支单测）；验收 1（LEGACY 零破坏，**先证伪**：开关改回 proto 版本 → 必须红）
- 🔴 验收 16 的 **POLICY×v3 保留格**（先激活 → 连 v3 → projects 不归零；**先证伪**：让"连 v3 ⇒ 清空" → 必须红）
- 🔴 验收 21（B4 session generation：旧会话 Policy 不复活）+ 验收 22（B5 latest-wins mailbox：队列满不丢最新 Rev），各自先证伪
- 验收 9 **先证伪**：删掉 `lastPolicy` 保存 → SIGHUP 后 `cfg.Projects` 必须**真的**变成 0 个
- 验收 8 **先证伪**：让映射失败也产出 ProjectConfig → 必须**真的**复现 `HostPath==""` → `filepath.Abs` = 进程 CWD
- 验收 13（白名单逐字透传）；验收 14（H2 两个字段，各自先证伪）；验收 23（MapRoot containment，走 T2）
- 验收 19（`-race` 并发 Submit × PolicyApply）

**提交**：`feat(worker): project server policy onto local config behind a roots opt-in switch (P3 T5)`

---

### T6 worker 机器上的 CLI 配套 + example + 迁移文档

**T6-A `gofer project list`**（`commands/project.go:localProjects()`）

- LEGACY → 读 `wc.Projects`（**逐字不变**）
- POLICY → 读 **policy 缓存文件**（`<config-dir>/run/worker-<id>.policy.json`）；文件不存在 → 提示"worker 未运行或尚未收到 Policy"（**不报错、不 panic**）

**T6-B `gofer config validate`（worker doctor，`commands/config.go`）**

- **不再**因 `len(projects)==0` 判 FAIL；改为按模式给判据：
  - LEGACY → 今天的行为 + **WARN**："`projects:` 段已废弃（策略改由 server 下发），迁移见 docs/…；本版本仍然生效"
  - POLICY → INFO："projects 由 server 下发；当前生效 N 个（读自 policy 缓存，worker 未运行时可能为空）"
  - EMPTY → **FAIL**（保持今天的行为：既无 roots 又无 projects = 这台跑不了任何东西）
- 新增 `roots` 检查（`to` 目录存在、`from` 非空、**无重复 `from`**、重叠 root 提示"更具体者优先"）
- `guards` 未设 → **WARN**（不 FAIL）："护栏未设置 = 不额外收紧；建议显式声明"

**T6-C `config/worker.example.yaml` 重写**：`projects` 段标注为**已废弃**（保留一段注释示例），加 `roots` / `guards`（含缺省语义与"更具体 root 覆盖"示例）。

**T6-D 🔴 迁移文档**（`docs/runbook/` 或计划附录，二选一，实施时定）

**迁移是 per-worker、可回滚的**（B1）：

```txt
① 不动 server。在这台 worker.yaml 里：
     - 按现有 projects 的 host_path，逐条算出需要的 roots（见下方核对表）
     - 加 roots + guards（显式写出 allow_exec / allow_interactive，别靠 nil 默认）
     - 【暂时保留 projects 段】—— 此时 roots 非空 ⇒ 已进 POLICY 模式，projects 被忽略（会告警）
②  gofer worker reload <id>   → 看 /v1/meta：projects 集是否与迁移前一致？
     不一致 → 看 Applied.Rejected 的 path_outside_roots，补 root（或补一条更具体的 root）
③  一致后【进入观察期，仍保留 projects 段跑一段】：此时【二进制可安全回滚】——
     旧码不认 roots、只读 projects，照跑（验收 24）。观察期长度按发布纪律定。
④  观察期无异常后，才删 projects 段、再 reload 一次。
     🔴【这一步之后不可再回滚二进制】（codex-C HIGH-3）：旧二进制不认 roots、只读 projects
        ⇒ 删了 projects = 0 project 停摆。若必须回滚，顺序写死：
        先原子恢复 projects + 删 roots → reload 验证 projects/caps 一致 → 再换二进制。
⑤  配置回滚（随时可做，二进制不动）：删 roots、保留/恢复 projects → 立刻回 LEGACY。server 版本无关。
```

**🔴 路径核对表（H3；迁移时必须逐条做，不能想当然）**：

| project | server `host_path`（逻辑） | 按拟定 roots 映射出的本机路径 | 今天 worker.yaml 里的 `host_path` | 一致？ |
|---|---|---|---|---|
| …逐条填… | | | | ✅ / ❌→加一条更具体的 root |

- **必须逐条填**。live server config 里**确实存在**一个 project 的 `container_path` 与 `host_path` **末段不同名**（少一个字符，两个目录都真实存在）—— 纯前缀 root 会把它映到**另一个目录**。用**更具体的 root** 覆盖（§6-H3）。
- **顺带修一个 live 的既存错误**（本次重写发现，§7-N3）：live 容器 worker.yaml 里有一个 project 的 `host_path` 填的是 **Windows 路径**（`D:/...`），而 worker 无 `path_view` ⇒ `ExecPath` 恒取 `HostPath` ⇒ 在 Linux 上 `project.SafeJoin` 的 `filepath.Abs("D:/...")` 会解析成 `<进程CWD>/D:/...`（不是任何真实目录）。**roots 迁移会顺带把这类手写错误消灭掉**（本机路径由映射推导，不再手写）。

**验收（T6）**：验收 18（`gofer project list` / `gofer config validate` 在两种形态下都正常）+ 验收 16 的两个 v3-server 格（断言告警文案）。

**提交**：`feat(worker): policy cache backed CLI + roots migration docs (P3 T6)`

---

### T7 e2e 冒烟（隔离栈，**不碰 live**）

```txt
🔴 执行环境 = 容器内隔离 Linux 栈（codex-B HIGH-6：kill -HUP / pgrep 是 Linux 语义；容器本就是 Linux、
   SIGHUP 可用；live LIVE-PORT 是主机 nssm 服务，容器内够不着）。全程不出容器、不碰主机 server。

🔴 隔离栈 preflight（验收 25，脚本开头硬断言，任一命中 exit 1）：
   - GOFER_CONFIG_DIR=<tmp>/cfgdir、GOFER_CONFIG=<tmp>/server.yaml、storage.db_path=<tmp>/v4.db、
     storage.root=<tmp>/storage、worker_id=smoke-p3-w、独立 token
   - server.addr=127.0.0.1:18899；worker server_link.urls=[ws://127.0.0.1:18899/v1/workers/connect]（【只此一个 URL】）
   - mixed-server 格（步骤 12）另起 127.0.0.1:18900，独立 db/storage/cfgdir
   - 断言所有副本里【不含 LIVE-PORT / 不含非 127.0.0.1 的 url / 不含 live 的 db·root·worker_id】
   - 所有 CLI 显式 `-c <隔离cfg> --server http://127.0.0.1:18899`；只 kill 脚本捕获的 PID（trap 清理）

🔴 数据隔离（codex-C BLOCKER-3：【不复制 live host_path】）：用【脱敏合成 fixture】——
   project 形态保真（3 个 local project、server 推 4 个 policy key、runner 关系相同），
   但所有 host_path 指向 <tmp>/projects/*；exec job 只跑无副作用命令（echo pwd + sleep），
   tty 用测试 PTY fixture（【不启动真实 claude/codex】）。收尾断言所有 cwd/result/db/cache 均在隔离根下。

🔴 承接 P1/P2：【绝不 pkill gofer】（会打死 live LIVE-PORT）、【绝不 pnpm build】（会热更 live 控制台）、
   serve 显式 --web-dir、live 配置文件只读。

1. 【🔴 验收 1】LEGACY 合成 fixture：worker.yaml(有 projects、无 roots/guards，host_path→<tmp>/projects/*)
     + 合成 server config(4 个 project 的 allowed_runners 列该 worker 的 runner)
     → 旧(dcc98dd)二进制起一次记 /v1/meta → 换 P3 二进制、配置零改动
     → 【projects / agent_caps diff 必须为空】；server 确实推了 4 个 key 的 Policy 而 worker 忽略之（LEGACY）
     → Applied.Degraded 含 legacy_local_projects；无副作用 exec job + tty fixture 交互 job → 仍跑通
2. 【验收 3】worker.yaml 换成 POLICY 形态（roots + guards，删 projects）
     → server 加 project（落在 root 下 + allowed_runners 列该 worker 的 runner）→ kill -HUP <serve pid>
     → /v1/meta 出现新 key；【worker PID 不变】（pgrep 前后逐字比对）
     → 提交 job → 跑通；stdout 的 pwd = 【映射后的本机路径】
3. 【🔴 验收 4】POST /v1/projects（web 写路径）→【不发 SIGHUP】→ worker projects 出现新 key；Rev +1
4. 【验收 8】server 加一个路径不在任何 root 下的 project → Applied.Rejected 有它；worker projects 里没有它
5. 【验收 9】kill -HUP <worker pid> → projects 【仍是那 N 个】（不是 0）
6. 【验收 10】worker.yaml 加 root → gofer worker reload → 步骤 4 被拒的 project 变 accepted；PID 不变
7. 【验收 11】guards.allow_exec:false → exec job 被拒 + Degraded 有它；改回 true → 跑通
8. 【验收 12】没写 allowed_runners 的 project → 任何 worker 的 projects 里【都没有】
9. 【验收 13】policy 给 [claude, tty-codex]（本机没装 codex）→ AllowedAgents 逐字相等；提 tty-codex → 明确报错
10.【验收 14】max_concurrent_jobs:1 → 并发 2 个 job 第二个排队；capture_diff:false → 无 diff 产物
11.【验收 15】重连风暴下 workflow 不被打挂：worker 断连重连（Rev 清零→重进 pending）+ 同时跑 workflow fan-out → 【workflow 不 fail】
12.【验收 16】滚动矩阵（隔离双 server 18899/18900）：v3 worker(c3ee6d1) / v2 worker(4def378) / v3 server(c3ee6d1) × {LEGACY, POLICY}
     ← POLICY【冷启动】连 v3 → 手上空配置 + 醒目告警文案、不崩、仍在线；
        🔴 POLICY【已激活】后连 v3 → 【保留 last-known-good、projects 不归零】（B1 v0.3，先证伪：清空必红）
13.【验收 18】gofer project list / gofer config validate 在 LEGACY 与 POLICY 两种形态下都正常
14.【验收 17】go list -deps ./internal/wshub | grep gofer | grep -v '/wshub$' →【恰好一行】wsproto
15.【🔴 验收 24】二进制回滚：观察期形态(roots+projects) 回滚旧(dcc98dd)二进制 → 仍用本地 projects 能跑；
     roots-only 回滚旧二进制 → 【预期 0 project 停摆，标为不可回滚点，不得当安全回滚】

（B4 session generation / B5 latest-wins mailbox 由 T5 的 -race 单测覆盖，验收 21/22；T7 只跑 e2e 可观测的收敛。）
```

**提交**：可复跑冒烟脚本存 **`scripts/smoke/p3/`**（**不放 `tmp/`** —— codex-C MEDIUM-1：`tmp/` 被 gitignore，下一个实施者看不到）；一次性运行产物（日志/快照）才落 `tmp/`。通过后收尾。

---

## 6. H3 决策：roots 前缀映射的表达力缺口怎么办

**问题**：live server config 里有一个 project 的 `container_path` 与 `host_path` **末段不同名**（少一个字符，**两个目录都真实存在**，不是笔误）。纯前缀 root（`D:/work/x → /d/work/x`）会把它映到 `/d/work/x/<host名>`，**与今天 `container_path` 指的不是同一个目录**。这证明"一条前缀规则"并不总够用。

（补充事实：该 project **今天并没有配在容器 worker.yaml 里** ⇒ 容器 worker 今天根本跑不了它 ⇒ **不是当场炸**，但它是一个真实的、无法用单条前缀表达的映射。）

**决策：不引入 per-project 覆盖（`path_overrides`）；例外用「更具体的 root」表达。**

理由：

1. **最长前缀天然就是 per-project 覆盖，而且更强**：
   ```yaml
   roots:
     - { from: D:/work/x,        to: /d/work/x }        # 通配根
     - { from: D:/work/x/proj-a, to: /d/work/x/proj-b }  # 例外：from 更长 → 命中它
   ```
   它不仅映对了 project 根，**子路径也一起映对**（`.../proj-a/sub` → `.../proj-b/sub`）——这是 per-project `container_path`（只给一个点）做不到的。**零新增配置面**。
2. **概念不增殖**：`root` 的语义就是"我这台机器把这棵树暴露出来"（D3）。例外目录本来就是**另一棵树**，用 root 表达它是**同一个概念**。而 `path_overrides` 会在 worker.yaml 里重新长出一个"按 project key 索引的段"——**那正是 D4 要砍掉的东西**，还会变成 policy 的后门（谁写了 override，谁就等于在 worker 侧声明了一个 project）。
3. **代价明确且可控**：迁移时必须**逐条核对**每个 project 的映射结果（T6-D 的核对表 + doctor 的 roots 检查），不一致就加一条更具体的 root。这个代价是**一次性**的，且它逼着你把今天散落在 `container_path` 里的隐式约定**显式化**。
4. **落地要求（否则这个决策就落空）**：T2-B 的映射实现必须把**"更长的 from 覆盖更短的 from"当成一等场景来测**（含子路径），不是边角用例。

**何时推翻**：出现"同一棵子树内、不同 project 要映到前缀规则表达不了的地方"时（例如两个 project 在 server 上共享同一个逻辑父目录，但在 worker 上必须落到互不包含的两处、且 project 根名字也一样）。届时再加 per-project override —— `roots` 已在，补一个覆盖层不破坏兼容。**YAGNI：今天没有这样的实例。**

---

## 7. 重写时新发现的问题（审查未覆盖）

- **N1 ack 与广播的竞态（已并入 T4-B）**：B3 的修法（`Put` 挪到 ack 之后）会带来一个新窗口 —— 在"ack 按 Rev=N 算好"与"`Put`"之间发生的 `PushPolicyAll`（Rev=N+1）**看不到这条连接**（它还没进注册表）⇒ worker **永久停在 N**，直到下一次配置变更。→ `Put` 之后做一次 **catch-up push**（`PolicyFor` 的 Rev > acked ⇒ 再发一帧；worker 侧 `rev>lastRev` 天然幂等）。**验收 7 专门证伪它。**
- **N2 ack 写绕过 `writeMu`（已并入 T0-B）**：ack 用的是**包级** `writeEnvelope(ctx, conn, ...)`，而所有推送帧走 `wc.writeFrame`（持 `wc.writeMu`）。在 Put-before-ack 的现状下，这两者是**同一条连接上的两个并发 writer** —— coder/websocket 明确禁止。B3 的修法顺带解掉；ack 改走 `wc.writeFrame` 更稳。
- **N3 live 容器 worker.yaml 里有一个 project 的 `host_path` 是 Windows 路径**（`D:/...`）：worker 无 `path_view` ⇒ `ExecPath` 恒取 `HostPath` ⇒ 在 Linux 上 `filepath.Abs("D:/...")` = `<进程CWD>/D:/...`（不是任何真实目录）。**今天就是错的**（LEGACY 模式下 P3 不改变它，验收 1 的 diff 仍为空 —— caps 只报 key，不报路径）。**roots 迁移会顺带把这类手写错误消灭掉**（本机路径由映射推导）。已写进 T6-D 的迁移文档。
- **N4 B2 提高了 Policy 推送频率**：web 控制台每次改 project 都会触发**全体 worker 重投影 + 重 detect**（P2 实测最坏 2.0s、典型 ms）。这是人工驱动的低频事件，**接受**；若日后成为问题，优化点是 **server 侧按连接记住"上次发过的 payload 指纹"、相同就不发**（比 worker 侧去重更省，且不影响重连语义）。**本期不做**（§8）。
- **N5 `config.Save` 写活配置 + `managedTopKeys` 固化（子代理发现，独立于 P3）**：`config.Save`（唯一调用者 `registry.go:107`）写的是 `r.cfg.Load()`（**活配置**），而 `managedTopKeys` 覆盖 `server/storage/projects/agents/...` 整个顶层。⇒ 任一次 web project add/delete 会把运行期原地写入的东西**永久固化**进用户配置文件：overlay 合并结果（`ApplyProjectOverlays`）、`--web-dir`/`--no-web` 等**临时 CLI flag**（`mergeServeOpts`）。这是 P2 `config.Save` 固化模板的**同款 bug 第 3/4 例**。**P3 只保证 `config.Save` 写 `next`（副本）**（T1-B）；`managedTopKeys` 只序列化"用户真正设过的顶层键"这件事**范围更大、独立处理** → 建 bd issue，**本期不做**（§8）。

---

## 8. 不做（明确排除）

- **`Policy.Agents` / `guards.allow_custom_agents`** —— Q6 砍掉（设计 §11）。它是 D1 边界上唯一的破口，且 P2 的 worker.yaml `agents:` 逃生舱已覆盖它的全部能力。
- **白名单与 `cfg.Agents` 的交集** —— 见 T5-E 边界。透传的准入结果一样，还少两个陷阱。
- **"空 `allowed_runners` = 全推"** —— Q8：语义与 `checkRunnerAllowed` 一致，改了会打架。
- **worker.yaml 的 `path_overrides` / per-project 覆盖** —— H3 决策：用更具体的 root 表达（§6）。
- **roots 的远程改写 / API 暴露** —— D3 推论：加 root = 扩大该机可执行范围，**故意**要求机器访问权。
- **Policy payload 指纹去重** —— 先按 T5-D 的"每次重连/每次变更重投影"做，简单且正确；**测出来是问题再优化**（优化点见 §7-N4）。
- **`projects` 段在本期删除** —— 本期只"标废弃 + 在 POLICY 模式下忽略"。**LEGACY 模式仍然完全生效**（B1 的护城河）。下一个版本再考虑真正移除。
- **`gofer worker show` / `worker projects <id>` CLI** —— **今天不存在**，P4 建。P3 的可观测面 = policy 缓存文件 + `/v1/meta`。
- **Cluster 页展示 rejected/degraded/policy_rev** —— P4（P3 只把数据吐到 `/v1/meta`）。
- **`projects.<key>.worker_labels` 为池型 runner 收紧** —— P4，不影响正确性。
- **`workerOnlyProject` placeholder 的代码改动** —— 触发条件（请求的 key 不在 host `cfg.Projects` 里）天然还在，**零改动复用**。
- **`config.Save` 的 `managedTopKeys` 固化（§7-N5）** —— P3 只保证写 `next`（副本）；"只序列化用户真正设过的顶层键"范围更大、**另开 bd issue**。
- **server 侧「超时未 Applied 就主动重推」（codex-A/C 提到的兜底）** —— B4 session generation + B5 mailbox + catch-up 已覆盖收敛路径；主动重推是**加固**，测出真有停滞再做。本期靠 `policy_pending` 在 `/v1/meta` 暴露未收敛即可。
- **`删 projects` 段真正移除** —— 本期只"标废弃 + POLICY 忽略"；观察期 projects 必须保留（验收 24 的二进制可回滚前提）。下个版本再删。

---

## 9. 风险与对策

| 风险 | 对策 |
|---|---|
| 🔴 **B1 换二进制归零 / 连 v3 server 归零** | **T5-A/B** 开关挂 `roots`（意图）+ Policy 帧到达才激活 + last-known-good；**永不因连了不给 Policy 的 server 而清空**；**验收 1/2/16 先证伪**；迁移 per-worker 可回滚 |
| 🔴 **B2 web 改 project 不重推 / Rev 不存在 / 既存 race / 并发写丢更新** | **T1-B/T1-D** 串行写事务（`updateMu` + mutation seam + Rev++/推送全在 `reloadLocked` 里）；`Add`+`Remove` 都改；`Clone` 深拷四张 map；**验收 4/5/19 先证伪** |
| 🔴 **B3 policy 帧插在 ack 前 → 重连风暴** | **T0-B** `Put` 挪到 ack 之后 + worker 断言帧类型；**验收 6 先证伪**（必须复现空 reason 的 rejected） |
| 🔴 **B4 旧会话 Policy 跨重连复活 → 卡在旧策略** | **T5-C/D** session generation（executor 按 gen 丢旧帧；register goroutine 不直接改 lastRev）；**验收 21 先证伪** |
| 🔴 **B5 有界队列满 → 丢最新 Rev** | **T5-C** Policy 走 latest-wins mailbox（不塞有界 FIFO）；**验收 22 先证伪** |
| 🔴 **B6 MapRoot `..`/symlink 逃出 roots** | **T2-B** 两端 containment（`..` 词法 + `filepath.Rel` + `EvalSymlinks`）；**验收 23 反向用例** |
| 🔴 **H1 pending 硬拒 → 打挂 workflow / cron** | **T4-E** pending **只换文案、不新增拒绝**；**验收 15 先证伪**（重连 + fan-out 必须真的挂） |
| 🔴 **H2 漏投字段 → 无限并发 / capture_diff 静默复活** | **T0-A / T5-E** `MaxConcurrentJobs` + `CaptureDiff` 随投；**验收 14 逐个先证伪** |
| 🔴 **SIGHUP 把 project 清空 → worker 静默停摆** | **T5-B** 内存持 `lastPolicy`，两条 reload 路径共用同一 projection + 同一串行 executor；**验收 9 先证伪** |
| 🔴 **roots 映射失败产出空 `HostPath` → job 散落到进程 CWD** | **T5-E** 映射不到 = **整条不进配置**；**验收 8 先证伪** |
| 🔴 **白名单交集：`AllowedAgents` 空 = 放行全部** | **T5-E 不做交集**（原样透传 + guards 收紧）；**验收 13** 逐字断言 |
| **N1 ack 与广播竞态 → worker 永久停在旧 Rev** | **T4-B** catch-up push；**验收 7 先证伪** |
| **`guards` 用裸 `bool` → 升级即把现网 exec/pty 全禁** | **T2-D** `*bool`（nil = 不额外收紧）；**验收 1 的第二个先证伪** |
| **Rev 跨 server 重启回退 → worker 永久丢弃新 Policy** | **T5-D** Rev per-connection、register 时清零；验收 16 最后一格实测 |
| **`(cfg, rev)` 分两次读 → `(旧cfg, 新rev)` → 永久卡在旧配置** | **T1-A** 一次原子读；`-race` 测试断言同代 |
| **Policy 计算写进 wshub → 破 G022** | **T1-E** seam 注入；**验收 17** 用 `go list -deps` 证明（注意排除自身，否则假红） |
| **Policy apply 与 SIGHUP reload 并发 → 旧配置覆盖新配置** | **T5-C** 进 P1 既有的串行 `reloadCh`（P1 T3 已解过一次） |
| **另写 apply 路径 → 重连用过期 caps 注册** | **T5-C** 复用 `runReload`（`storeCaps` 是重连 caps 的唯一来源）；写死在计划里 |
| **`Applied` 另起能力上报通路 → server 两个能力真源** | **T0-A/T4-D** `Applied` 内嵌 `*Caps`，走**同一个** `reg.UpdateCaps` |
| **旧连接迟到的 Applied 污染新连接** | **T4-D** 复用 `UpdateCaps` 的 `r.conns[wc.workerID] != wc` 检查，**不新写一套** |
| **v3 worker 被误标 policy_pending** | **T4-E** 只对 `SupportsPolicy(proto)` 的 worker 置 pending；LEGACY worker 也会回 Applied 清 pending |
| **worker 机器上 `gofer project list` / `config validate` 塌掉** | **T6-A/B** policy 缓存文件 + 按模式改判据；验收 18 |
| **`Policy.Agents` 被"顺手"加回来** | **T0-A 边界写死**；code review checklist：Policy 只带 project 元数据与白名单 |
| **H3 前缀映射映错目录** | **T6-D** 迁移核对表逐条核对 + **更具体的 root** 覆盖（§6）；T2-B 把重叠 root 当一等场景测 |
| 🔴 **删 projects 后回滚旧二进制 = 0 project 停摆** | **T6-D** 两阶段迁移：观察期保留 projects（二进制可回滚）、删 projects 标为**不可回滚点**；验收 24 |
| 🔴 **冒烟连到 / 控制 / 落数据到 live** | **T7** 容器内隔离 Linux 栈 + preflight 硬断言（不含 LIVE-PORT/非 loopback/live 资源）+ 脱敏合成 fixture；验收 25 |
| 重连重跑 detect / B2 抬高推送频率 | 接受（P2 实测最坏 2.0s、典型 ms）；不做去重（§8，优化点见 §7-N4） |

---

## 10. 提交节奏（SR1202）

server 链 `T0 → T1 → T3 → T4`；worker 链 `T2 → T5 → T6`（T5 依赖 T2 的 MapRoot + T0 的帧）；两链文件不重叠、可并行；`T7` 最后。
每步单独 commit，每步 `go test ./... -p 1 -count=1` + `go vet` 绿；**T1/T4/T5 额外跑 `-race`**（并发写事务 / Applied 竞态 / generation+mailbox 都在这三个任务）。

---

## 11. 进度跟进

- [ ] **T0 协议地基**：proto v4 帧（`Policy`/`Applied{*Caps}`/`Registered{ProtocolVersion,Policy}`/`PolicyMinProtocolVersion=4`，含 H2 的两个字段）+ **握手加固**（hub `Put` 挪到 ack 后、ack 走 `writeMu`；worker 断言帧类型不吞 error）+ 白名单 wire `null`≡`[]`（MEDIUM-1）— **验收 6 先证伪**
- [ ] **T1 core 地基**：`(cfg,rev)` 原子快照（顺带关 bd `tools-cg4`）+ **串行写事务 `Core.Update`（`updateMu`）** + `Clone` **深拷四张 map + `injectedAgents`** + `project.Registry` **mutation seam**（交 mutation、不构造 next；`Add`+`Remove` 都改）+ **Rev++/PushPolicy 收进 `reloadLocked`** + `config.Save` 写 next + hub `PolicySource` seam — **验收 4/5/17/19 先证伪**
- [ ] **T2** worker.yaml `roots` + `guards`：字段 / defaults / validate / **最长前缀映射（重叠 root 一等场景）+ containment（`..`/symlink，B6）** / `*bool` 缺省语义 — **验收 23 反向用例**
- [ ] **T3** server 推送目标计算（D4′；白名单非 nil）+ `corePolicySource` 接线（一次原子读）— **验收 12 反向测试先证伪**
- [ ] **T4** hub：ack 带 Policy（Q7-b）+ **catch-up push（N1）** + `reloadLocked` 广播 + `TypeApplied` → `reg.UpdateCaps` + **policy_pending 只换文案（H1）**（fake worker 验帧序）— **验收 7/15 先证伪**
- [ ] **T5** worker：**模式判定（意图 vs 激活，B1 v0.3）+ last-known-good 缓存恢复** / **session generation（B4）** / **latest-wins mailbox（B5）** / 进 P1 串行 executor（复用 `runReload`）/ roots 映射 / **白名单透传不交集** / **H2 两个字段** / `Applied` 回报 / policy 缓存原子写 — **验收 1/2/8/9/13/14/16/21/22 先证伪**
- [ ] **T6** worker 机器 CLI 配套（`project list` / `config validate` 按模式）+ `worker.example.yaml` + **迁移文档（两阶段回滚 + 不可回滚点 + 路径核对表 + H3 更具体 root）** — **验收 24**
- [ ] **T7** e2e 冒烟（15 条，**容器内隔离 Linux 栈**；preflight 硬断言 + 脱敏合成 fixture；红线：不碰 live LIVE-PORT、不 `pkill`、不 `pnpm build`、live 配置只读）— **验收 25**
