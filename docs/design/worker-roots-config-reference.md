# Worker `roots` 配置参考（POLICY 模式）

> 讲清 worker `roots` 的语义、心智模型与**全部配置场景**——这些都**跟操作系统无关**，任何 worker（Windows / Linux / 容器）迁到 POLICY 都会撞上。
>
> - 迁移流程（LEGACY → POLICY 的五步 + 回滚）见 [迁移手册](../runbook/2026-07-15-worker-policy-migration.md)
> - 可运行的示例配置见 [`config/worker.example.yaml`](../../config/worker.example.yaml)
> - 设计背景见 [worker 配置联邦设计](2026-07-13-worker-config-federation-design.md)
>
> 本文所有路径 / 盘符 / 项目名均为 `<占位符>`（`D:/work/x`、`/host/projects/x`、`proj-a` 等），按实际部署替换。

---

## 1. 一句话

`roots` = 把 **server 下发的逻辑路径** 映射到 **这台 worker 的本机真实路径** 的「前缀规则表」。

## 2. 心智模型：两种路径

POLICY 模式下有两种路径，务必分清：

| | 是什么 | 从哪来 |
|---|---|---|
| `from` | **server 眼里的路径**（逻辑标识） | server config 里该 project 的 `host_path` 前缀 |
| `to`   | **这台机器上的真实路径**（执行落点） | 你在这台 worker 的 worker.yaml 里写 |

一个 project 在这台 worker 上的执行目录（cwd）：

```
cwd = MapRoot(server_host_path)
    = 用「最长命中」的 root，把 host_path 的 from 前缀换成 to 前缀
```

例：

```
server 下发 project 的 host_path = D:/work/x/my-service
root = { from: D:/work/x, to: /host/projects/x }
⟶ cwd = /host/projects/x/my-service
```

> `host_path` 是**逻辑标识**，不要求 server 本机真有这个目录；它只是给各 worker 的 roots 做映射的「锚」。

## 3. 模式判定：`roots` 是开关

| `roots` | `projects` | 模式 | project 来源 |
|---|---|---|---|
| 有 | 任意 | **POLICY** | **server 下发**（`projects:` 段被忽略，有则告警） |
| 无 | 有 | **LEGACY** | worker.yaml 本机这份 `projects:` |
| 无 | 无 | EMPTY | 无（跑不了任何 job，`doctor` 判 FAIL） |

🔴 **全有或全无（最关键的约束）**：POLICY 下 project 集合**完全**由 server 下发（完整快照替换，**不与本地 projects 合并**）。所以：

- **一台 worker 要么整台 LEGACY、要么整台 POLICY，不能半边 POLICY 半边本地。**
- 想让某 project 只在某台 worker 跑，用**场景 E**（server 侧 pin），不是在 worker.yaml 里保留它。

## 4. 配置场景速查（OS 无关）

### 场景 A — 恒等映射：本机路径 == server 逻辑路径

**何时**：worker 与 server 同机、或同盘同布局（典型：与 server 同在一台 Windows 主机上的 host worker）。

```yaml
roots:
  - { from: D:/work/x, to: D:/work/x }   # from == to
```

> ⚠️ 即使 `from == to`，**也必须写这条 root**——`roots` 是进 POLICY 的开关，且 server 的 `host_path` 要过 `MapRoot` 的边界/containment 校验才会被接受。

### 场景 B — 跨盘 / 跨路径：本机放在别处

**何时**：这台 worker 把项目放在不同盘或不同目录。

```yaml
roots:
  - { from: D:/work/x, to: E:/projects }   # D:/work/x/<proj> → E:/projects/<proj>
```

### 场景 C — Linux / 容器 worker：server 逻辑路径是 Windows 风格

**何时**：server 在 Windows（`host_path` 是 `D:/...`），worker 是 Linux / 容器。

```yaml
roots:
  - { from: D:/work/x, to: /host/projects/x }
```

### 场景 D — 末段名不一致：更具体 root 覆盖（H3）

**何时**：某 project 的 server `host_path` 末段与本机目录名不同（少个字符、改过名等），通配根会映错。用**更长的 `from`** 精确改写它（含其子路径）。

```yaml
roots:
  - { from: D:/work/x,        to: /host/projects/x }        # 通配根
  - { from: D:/work/x/proj-a, to: /host/projects/proj-b }   # 例外: from 更长 → 命中它
```

> 「最长前缀优先」保证子路径也一起映对：`.../proj-a/sub` → `.../proj-b/sub`。

### 场景 E — worker 独有 project（只在这台跑）

**在 server 定义** + `allowed_runners` **只列这台 worker 的 runner**，`computePolicy` 就只推给它 = 独有；别的 worker 拿不到。

```yaml
# ===== server config.yaml =====
runners:
  w-a:
    type: worker
    worker_id: w-a
projects:
  proj-x:
    host_path: E:/private/proj-x   # 逻辑路径(可直接用它在该 worker 上的真实路径)
    allowed_runners: [w-a]          # ★ 只推给 w-a → 独有
    allowed_agents: [exec, claude]
    allow_exec: true
```

```yaml
# ===== w-a 的 worker.yaml =====
worker_id: w-a
roots:
  - { from: E:/private, to: E:/private }   # 逻辑路径就是本机真实路径 → 恒等
```

### 场景 F — 纯本地 project、不想让 server 知道 → 保持 LEGACY

如果这个 project 敏感 / 临时 / 只此一台，不愿声明到 server：**别给这台 worker 加 roots**，继续用 worker.yaml 的 `projects:`（LEGACY）。代价：这台 worker 整台走 LEGACY，享受不到集中下发。

> 因「全有或全无」，无法「POLICY 下发 + 额外本地独有」并存。要集中管就全上 server（场景 E）；要纯本地就整台 LEGACY。

## 5. 映射规则（`MapRoot`）

1. **最长 `from` 优先**：多条命中取 `from` 最长的（场景 D 靠它）。
2. **边界对齐**：`/a/b` 不匹配 `/a/bc`——必须完全相等或下一字符是 `/`。
3. **归一化**：`\` → `/`、去尾斜杠；**Windows 盘符大小写不敏感**（`D:/` == `d:/`），Linux 敏感。
4. **containment（安全）**：`..`、symlink 逃出 `to` 目录一律拒绝映射。
5. **映射不到 = 拒绝**：不落在任何 root 下 → `Applied.Rejected{path_outside_roots}`，该 project **不进配置**（绝不落到进程 CWD 等随机目录）。

## 6. `guards`（本地收紧，只减不增）

`guards` 是这台 worker 对下发能力的**本地否决**开关（只能收紧、不能放宽）：

```yaml
guards:
  allow_exec: true          # false = 本机拒所有 exec job
  allow_interactive: true   # false = 本机拒所有 pty/交互 job
```

- 缺省语义（字段未写）= **不额外收紧**（与升级前一致）。
- 迁移时**建议显式声明**（别靠缺省），让这台机的能力姿态一目了然（`doctor` 未设会 WARN）。
- server 说 `allow_exec:false` 时，guards 写 `true` 也**没用**——server 说了不准就不准。

## 7. roots 是「能力」、只在本机配

加一条 root = 声明「我这台机器把这棵目录树暴露出来给 server 派活」= **扩大该机可执行范围**，故意要求你有机器访问权，**不能远程改**。

- **worker = 能力提供方**：roots / guards（我这台有什么、放宽到哪）。
- **server = 策略权威**：哪个 project 派给哪台、允许哪些 agent、能否 exec/pty。

所以「加 project 到已有 root 下」= server 一行 + reload，worker 零改动；只有「新增一棵树」才需要上这台机器加 root。

## 8. 校验

```bash
gofer config validate worker      # doctor: 检查每个 to 目录存在、from 不重复、重叠 root 提示"更具体者优先"
gofer project list                # POLICY: 列当前生效 project(读 policy 缓存)及映射后的本机路径
```

## 9. 常见坑

| 坑 | 现象 | 处理 |
|---|---|---|
| 忘写 identity root | 以为进了 POLICY，其实 `roots` 为空 → 仍 LEGACY | 恒等也要写 `{from:X, to:X}`（场景 A） |
| `from`/`to` 写反 | project 全 Rejected 或映到怪路径 | `from`=server 逻辑、`to`=本机 |
| 独有 project 想混本地 | 加了 roots 又想保留某本地 project | 不行（全有或全无）→ 场景 E 上 server |
| Linux worker 的 `host_path` 误写成 Windows 路径 | LEGACY 遗留，job 落到进程 CWD | 迁到 roots 顺带修（本机路径由映射推导） |
| 末段名不一致 | 通配根把 project 映到不存在/错误目录 | 加更具体 root（场景 D） |

## 10. 相关文档

- 迁移流程 + 回滚：[`docs/runbook/2026-07-15-worker-policy-migration.md`](../runbook/2026-07-15-worker-policy-migration.md)
- 示例配置：[`config/worker.example.yaml`](../../config/worker.example.yaml)
- 设计背景（worker=能力 / server=策略）：[`docs/design/2026-07-13-worker-config-federation-design.md`](2026-07-13-worker-config-federation-design.md)
