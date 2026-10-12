# 配置目录本地全局记忆设计

- issue：gofer-j6qn
- 状态：设计已确认（2026-10-12，4 点均按建议）；G1 v0.150.0，G2 / G3 见第八节

## 一、问题

仓库记忆（`.gofer/tracker/memories.jsonl`）是本地文件，不连 server 也能完整使用，连上时经 `gofer repo sync` 与 server 双向同步。全局记忆（`memory --global`）却只存在 server 的数据库里：

- **写**：CLI `memory set/rm/flag --global`、MCP、web、知识沉淀，全部直接写 server 的 `scoped_memories` 表，没有本地副本，也没有离线队列；连不上 server 时 CLI 报错 `scoped memory requires a reachable server`。
- **读**：`repo prime` 在 1.5 秒预算内读 server，超时静默跳过；`issue / plan brief` 跳过并提示「未连接 server」；hook 的提示词记忆注入 250 ms（PreToolUse 100 ms）超时即只用仓库记忆；MCP（client 模式）直接报错；`memory doctor --global` 报错。

结果是：不连 server 的机器、server 暂时不可达，甚至只是 server 回得慢的时候，agent 看不到全局约定（例如「回复用中文」）。

目标：在**配置目录**放一份本地全局记忆，与仓库 tracker 同格式。离线时读写完整可用；连上 server 时与 server 双向同步，多台机器经 server 保持一致。

## 二、现状要点

| | 仓库 tracker | server 全局记忆 |
|---|---|---|
| 记录 | `tracker.Memory`：key、content、tags、updated_at、by + `MemoryMeta`（kind、summary、when、expires_at、source、flags…） | `jobstore.ScopedMemory`：同样的字段，`by` 叫 `updated_by`，多 scope / scope_key |
| 存储 | jsonl，整文件原子重写，文件锁；归档在 `memories-archive.jsonl` | `scoped_memories` 表，软删除 |
| 与 server 同步 | `POST /v1/tracker/sync`：按 `tracker_id` 隔离，每条记录有 `rev`，增量 cursor（`changed_seq`），墓碑（含归档墓碑），客户端三路合并 + 服务端乐观并发，最多 3 轮 | 无版本号、无增量、拿不到墓碑 |
| server 侧表 | `tracker_repos` / `tracker_memories`（rev、deleted、changed_seq） | `scoped_memories` |

仓库 tracker 的同步机制已经覆盖了这里需要的一切（双向、增量、删除、冲突），而两边的记录结构几乎同构。

## 三、方案

**把全局记忆变成一个特殊的 tracker**：本地是配置目录下的 tracker 目录，server 侧是 `tracker_memories` 里一个保留的 `tracker_id`，两者之间用现有的 `/v1/tracker/sync` 同步。

### 3.1 本地：配置目录下的全局 tracker

```
<config-dir>/tracker/
  memories.jsonl            # 全局记忆，格式与仓库 tracker 完全相同
  memories-archive.jsonl    # 归档
  .local/                   # 同步状态（sync-base / sync-revs / sync-status），与仓库 tracker 相同
```

- `<config-dir>` 即 `config.ConfigDir()`（`$GOFER_CONFIG_DIR`，否则 `~/.config/gofer`），每台机器（server 主机上的 CLI、容器、worker 机器）各一份。
- 用同一套 `tracker.Store` 读写（文件锁、原子重写、校验），只是没有 issues。
- `memory set/rm/flag/unflag/archive/restore/promote/doctor --global` 全部改为操作这份本地 store；`archive / restore / promote` 也因此对全局记忆可用（现在不行）。

### 3.2 server：全局记忆改存 `tracker_memories`

- 保留 `tracker_id = "global"`（`tracker_repos` 里一行，`project_key` / `rel_path` 为空），全局记忆以普通 tracker 记忆的形式存在 `tracker_memories`，天然带上 rev、墓碑和增量 cursor。
- 现有的 `/v1/memories/global/...` 接口、MCP、web、知识沉淀（`memory accept --global`）**接口不变**，内部改成读写这个 tracker：写入即 `rev+1`、记 `changed_seq`，删除写墓碑。读者无感知。
- 一次性迁移：server 启动时把 `scoped_memories` 里 `scope = 'global'` 且未删除的行搬进 `tracker_id = "global"`（已存在同 key 时取较新的），迁移完成写标记，不再重复；`scoped_memories` 只保留 project 作用域。
- 备选（不推荐）：给 `scoped_memories` 补 rev / changed_seq / 墓碑，再写一套同步端点。等于把 tracker 同步重写一遍，而两边记录本来同构。

### 3.3 同步

- 客户端用现有的 `tracker.SyncHTTP*`，`tracker_id = "global"`，只同步记忆。冲突处理与仓库 tracker 相同：字段级三路合并，双方都改时取 `updated_at` 较新者，tags 取并集，删除与修改冲突比较时间。
- 触发时机：
  - 写入后自动同步（与仓库 tracker 的 `auto_sync` 一致，2 秒超时，失败只警告，下次再推）；
  - `repo prime`（会话开始）在现有的 server 预算内顺带拉一次增量；
  - 显式 `gofer memory sync --global`（也是首次全量拉取的方式）。
- 离线期间的写入留在本地，下次任一同步时推上去；墓碑同理，所以离线删除也不会被 server 上的旧记录「复活」。

### 3.4 读：本地优先

`repo prime`、brief、hook 提示词注入、MCP（client 模式）、`memory ls/show --global` 一律**读本地副本**：

- 不再受网络预算影响，server 慢或不可达时注入内容不变；hook 注入少一次网络往返。
- 新鲜度靠上面的同步时机保证：会话开始时 prime 拉一次增量，别处（web、其他机器）的修改最迟在下次会话开始时到达。
- 本地副本从未同步过（新机器）且 server 可达时，prime 先做一次全量拉取再读。

server 进程自己（知识沉淀、web、standalone MCP）直接读写 `tracker_id = "global"`，它就是中枢。

### 3.5 权限

保持现状：job 凭据对全局记忆只能读和 flag，不能增删改。同步端点对 `tracker_id = "global"` 的推送要求人身份的凭据；job 凭据只能拉取（或推送只含 flag 变化的记录）。全局记忆会注入所有项目的会话，这一条不能放松。

## 四、不做

- 项目作用域记忆（`--project`）：仍只在 server。项目的约定通常写在该项目仓库的 tracker 里，需要时再按同样方式做。
- 多 server：全局记忆以一台 server 为中枢；同一台机器连不同 server 时，本地副本只与当前 `.env` 指向的 server 同步。

## 五、分期

1. **G1 server 侧**：保留 tracker `global`、迁移、`/v1/memories/global/...` 与知识沉淀改为读写它；同步端点的权限规则。对现有客户端完全兼容。
2. **G2 本地 store 与 CLI**：配置目录 tracker、`memory ... --global` 改走本地、`memory sync --global`、写后自动同步。
3. **G3 读路径**：prime / brief / hook / MCP 改读本地副本，prime 顺带同步；web 设置页显示各机器最近同步时间（可选）。

## 六、测试要点

- 迁移：已有全局记忆搬进 tracker 后，接口读到的内容不变；重复启动不重复迁移。
- 离线：断开 server 时 `memory set/ls/show --global`、prime、hook 注入都能用；恢复后写入被推上去。
- 双机：A 机修改、B 机删除、同 key 两边都改，三种情况同步后两机与 server 一致（复用 tracker 同步测试的写法）。
- 权限：job 凭据推送 `global` 的增删改被拒，flag 允许。

## 七、待确认

1. **server 存储**：全局记忆改存 `tracker_memories`（推荐，复用同步机制），还是给 `scoped_memories` 补版本号另写同步？
2. **读优先级**：本地优先（推荐，离线与在线行为一致、注入更快；代价是别处的修改要等下次会话开始才到达），还是在线时仍读 server、只在离线时读本地？
3. **本地目录名**：`<config-dir>/tracker/`（推荐，与仓库的 `.gofer/tracker/` 对应），还是 `<config-dir>/memory/`？
4. **分期**：G1 → G2 → G3 依次发版，还是合并成一次？

## 八、实施记录（2026-10-13）

**G1（v0.150.0）**：`internal/jobstore/global_memories.go`——作用域记忆的 global 读写改为 `tracker_memories`（`tracker_id = "global"`）上的门面：写入 rev+1、新 `changed_seq`，flag 也是一个 rev，删除写墓碑；`migrateGlobalScopedMemories` 在打开库时一次性搬迁并删除 `scoped_memories` 的 global 行。`ListTrackerRepos` 排除保留 tracker，改名 / 删除返回 `ErrReservedTracker`（HTTP 400）；同步端点对它：推 issues 400，job 凭据推 memories 403、只拉 200。

**G2**：`tracker.OpenGlobal`（建目录与 `config.yaml`：`tracker_id: global`、`auto_sync: true`）、`Store.SyncedBefore`；CLI `memory … --global` 全部改走本地 store，`archive / restore / promote` 也支持 `--global`；`memory sync --global`；第一次使用（从未同步）先拉一次（5 秒）。job 内（`GOFER_JOB_TOKEN`）增删改拒绝，`flag --global` 经 server 的作用域接口记录后拉回本地。

**G3**：prime 在原 1.5 秒预算内并行同步全局副本（1.2 秒）后读本地渲染「全局记忆」段；hook 的提示词 / 命令记忆加载器、`issue / plan brief` 改读本地副本（`hookrelay.GlobalMemorySource`、`brief.Options.GlobalMemories`）。从未同步且为空的副本返回 `tracker.ErrGlobalNotSynced`，调用方退回读 server。

**与设计的偏差及原因**

- **MCP 仍读写 server**：client 模式的 MCP 工具本来就要连 server，写入也走 server；若读改成本地副本，agent 写后立即读会读到旧值。standalone MCP 跑在 server 进程内，直接读写中枢。
- **commands 测试隔离**：测试进程里 `GOFER_CONFIG_DIR` 指向临时目录、清掉 `GOFER_SERVER_ADDR`——实现 G3 时发现测试会在开发机真实配置目录下建 `tracker/` 并尝试与真实 server 同步（未成功、已清理）。
