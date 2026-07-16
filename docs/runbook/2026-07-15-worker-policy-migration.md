# Runbook — worker 从 LEGACY(本地 projects) 迁移到 POLICY(server 下发 + roots)

> 设计与验收见 `docs/plans/2026-07-15-worker-policy-push-plan.md`（§6-H3 表达力缺口 / §7-N3 既存错误 / 验收 24 回滚安全）。
> 本文所有路径/端口/服务名均用 `<占位符>`，按实际部署替换。**迁移是 per-worker、可回滚的**：一台一台改，不动 server，随时能退回。
> **roots 配置场景**（恒等映射 / 跨盘 / Windows / Linux / worker 独有 project / 保持 LEGACY 等，均跟操作系统无关）见 [worker `roots` 配置参考](../design/worker-roots-config-reference.md)。

## 0. 名词 / 前提

- **LEGACY 模式**：worker.yaml 里有 `projects:`、**没有** `roots:`。worker 用**本机这份文件**里的 project 执行 job，**忽略** server 下发的 Policy。
- **POLICY 模式**：worker.yaml 里有 `roots:`（有没有 `projects:` 都算）。server 下发「这台可跑哪些 project + 各自的逻辑 `host_path`」，worker 用 `roots` 把逻辑路径**映射**成本机真实目录再跑；本机 `projects:` 段被忽略（会告警）。
- **模式判定**（`gofer config validate worker` / `config info` 都按它分支）：`roots` 非空 ⇒ POLICY；否则有 `projects` ⇒ LEGACY；两者都无 ⇒ EMPTY（跑不了任何 job，doctor 判 FAIL）。
- **root**：`{ from: <服务端逻辑前缀>, to: <本机目录前缀> }`。匹配按**最长 `from` 优先**；映射结果必须仍落在 `to` 之下（含 symlink），否则该 project 被拒（`path_outside_roots`）。
- 前提：本机能跑 `gofer worker reload <id>`（经 server 转发到活着的 worker），且能看 `/v1/meta`（`gofer project list --remote` 或 web Cluster 页）。

## 1. 迁移五步（回滚安全的两阶段）

核心是**观察期**这道分界：**删 `projects:` 之前**二进制可安全回滚，**删之后**不可（旧二进制不认 `roots`、只读 `projects`）。

```txt
① 不动 server。在这台 worker.yaml 里：
     - 按现有 projects 的 host_path，逐条算出需要的 roots（见 §2 路径核对表）
     - 加 roots + guards（【显式写出】 allow_exec / allow_interactive，别靠 nil 缺省）
     - 【暂时保留 projects 段】—— 此时 roots 非空 ⇒ 已进 POLICY 模式，projects 被忽略（worker 启动会告警）
②  gofer worker reload <id>   → 看 /v1/meta：projects 集是否与迁移前【一致】？
     不一致 → 看 Applied.Rejected 里的 path_outside_roots，补一条（或更具体的）root，再 reload
③  一致后【进入观察期，仍保留 projects 段跑一段】：此时【二进制可安全回滚】——
     旧码不认 roots、只读 projects，照跑（验收 24）。观察期长度按你的发布纪律定
④  观察期无异常后，才【删 projects 段、再 reload 一次】
     🔴【这一步之后不可再回滚二进制】：旧二进制不认 roots、只读 projects ⇒ 删了 projects = 0 project 停摆
        若必须回滚，顺序【写死】：先原子恢复 projects + 删 roots → reload 验证 projects/caps 一致 → 再换二进制
⑤  配置回滚（随时可做，二进制不动）：删 roots、保留/恢复 projects → 立刻回 LEGACY。server 版本无关
```

各步骤要点：

### ① worker.yaml 加 roots + guards（保留 projects）

```yaml
# 之前(LEGACY)：projects 段是真源
# projects:
#   proj-a:
#     host_path: <本机 proj-a 目录>
#     ...

# 之后(过渡期，两者并存)：
roots:
  - { from: <服务端逻辑前缀>,           to: <本机目录前缀> }        # 通配根
  - { from: <服务端逻辑前缀>/<例外名>,  to: <本机例外目录> }        # 更具体 root（§6-H3）
guards:
  allow_exec: true          # 显式声明，别靠缺省
  allow_interactive: false
projects:                    # 【暂时保留】—— roots 已在 ⇒ 本段被忽略并告警，删除见第④步
  proj-a:
    host_path: <本机 proj-a 目录>
```

- `guards` 缺省语义 = **不额外收紧**（exec/interactive 全放行，与升级前一致）；显式声明是为了让能力姿态一目了然，`gofer config validate worker` 对未声明 guards 会 WARN。
- 校验本机配置：`gofer config validate worker`。POLICY 模式下它检查 roots（`to` 存在 / `from` 非空 / 无重复 `from` / 重叠提示「更具体者优先」）+ guards，并报「当前生效 N 个（读自 policy 缓存）」；**不会**再因 0 个本地 project 判 FAIL。

### ② reload 并核对 projects 集一致

```sh
gofer worker reload <id>
gofer project list --remote        # 或看 web Cluster 页 /v1/meta
```

- 逐条比对**迁移前**这台 worker 的 project 集与**迁移后** `/v1/meta` 里的一致。
- 不一致 → `Applied.Rejected` 会列出 `path_outside_roots` 的 key：说明该 project 的逻辑 `host_path` 没落在任何 root 下。回到 §2 核对表补一条更具体的 root，再 reload。
- worker **不重启**（reload 是热更），手上在跑的 job 不受影响。

### ③ 观察期（保留 projects）——【二进制可安全回滚点】

保持 roots + projects 并存跑一段。此时若需回退二进制：直接换旧二进制即可，旧码只读 projects、照跑（这就是保留 projects 的意义）。

### ④ 删 projects 段，再 reload ——【此后不可回滚二进制】

观察期无异常后，从 worker.yaml 删掉整个 `projects:` 段，`gofer worker reload <id>`。至此这台完成迁移。

> 🔴 **不可回滚点**：删了 projects 后，旧二进制（不认 roots）会认为「0 project」而停摆。若在此之后仍必须回滚二进制，**必须按写死的顺序**：先原子恢复 `projects:` + 删 `roots:` → reload 验证 projects/caps 与迁移前一致 → **再**换二进制。顺序反了 = 停摆窗口。

### ⑤ 配置回滚（随时，二进制不动）

删 `roots:`、保留/恢复 `projects:` → `reload` → 立刻回 LEGACY。与 server 版本无关（server 照常下发 Policy，LEGACY worker 忽略之）。

## 2. 🔴 路径核对表（迁移时【必须逐条填】，不能想当然）

纯前缀 root 会把 `<from>/<末段>` 映到 `<to>/<末段>`。当 server 侧 `container_path` 与 `host_path` **末段不同名**（少一个字符、两个目录都真实存在）时，通配根会把它映到**另一个目录**——所以每个 project 都要**逐条核对**映射结果，不一致就加一条**更具体的 root**（§6-H3：更长的 `from` 覆盖更短的，子路径一起改对）。

| project | server `host_path`（逻辑） | 按拟定 roots 映射出的本机路径 | 今天 worker.yaml 里的 `host_path` | 一致？ |
|---|---|---|---|---|
| `<proj-a>` | `<from>/<proj-a>` | `<to>/<proj-a>` | `<本机 proj-a 目录>` | ✅ / ❌→加更具体 root |
| `<proj-例外>` | `<from>/<末段A>` | `<to>/<末段A>`（**通配根算错**） | `<本机 末段B 目录>` | ❌ → 加 `{ from: <from>/<末段A>, to: <本机 末段B 目录> }` |
| … | … | … | … | … |

- **映射本机路径**可用 `gofer config validate worker`（reload 后看 `/v1/meta` 的实际 host）或对着 roots 手算核对。
- 「更具体 root」示例（通配根 + 更长 from 例外）见 `config/worker.example.yaml` 的 `roots:` 段注释。

## 3. 顺带修一个既存错误（§7-N3）

- **现象**：容器/Linux 上的 worker.yaml 里，有的 project 把 `host_path` 写成了 **Windows 路径**（`D:/...`）。worker 没有 `path_view` ⇒ `ExecPath` 恒取 `host_path` ⇒ 在 Linux 上 `filepath.Abs("D:/...")` 会解析成 `<进程CWD>/D:/...`（不是任何真实目录）——**今天就是错的**（LEGACY 模式下 P3 不改变它，caps 只报 key、不报路径，所以平时看不出来）。
- **迁移顺带消灭它**：POLICY 模式下本机路径由 `roots` 映射**推导**（`from` 逻辑前缀 → `to` 本机前缀），不再手写 `host_path`，这类手写错误自然消失。核对表（§2）逐条对一遍就能把隐式约定显式化。

## 4. 快速自检清单

- [ ] worker.yaml 加了 `roots` + 显式 `guards`，`projects` 暂留
- [ ] `gofer config validate worker` PASS（roots 的 `to` 都存在、无重复 `from`；guards 显式；不因 0 project FAIL）
- [ ] `gofer worker reload <id>` 后 `/v1/meta` 的 project 集与迁移前**逐条一致**（不一致查 `Applied.Rejected` 补 root）
- [ ] §2 路径核对表逐条填完、全部 ✅
- [ ] 观察期跑稳（此期间二进制可安全回滚）
- [ ] 删 `projects` 段 + reload（**此后不可回滚二进制**）
- [ ] 需回滚配置时：删 `roots`、恢复 `projects` → reload → 回 LEGACY
