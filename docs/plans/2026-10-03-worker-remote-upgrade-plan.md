<!-- template_id: plan; template_version: 1.2.0 -->
# 远程升级 worker 与两项小修实施计划

> 状态：Draft 0.1 / 待人工计划批准（本次请求已明确选择 `DIRECT_CONTINUOUS` 并连续执行）
> 设计：[`../design/2026-10-03-worker-remote-upgrade-design.md`](../design/2026-10-03-worker-remote-upgrade-design.md)

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-03 | Codex | 将已批准 U1–U3 拆成可验证波次：U2 → U3 → U1 |

## 规划可靠性声明

- `thinking_mode=RIGOROUS`
- `core_objective=完成 U2 配置最小写回、U3 传话 job 默认隐藏、U1 worker 远程升级/回滚，并保留现有协议与安全边界`
- `allowed_scope=tools/gofer 代码、测试、skills/gofer-usage 文档、此计划及 tmp 验证证据`
- `non_goals=不重启正式 server/worker，不修改正式配置目录，不 push，不改变未在设计覆盖的协议语义`
- `expansion_policy=DEFER_OR_REQUEST`
- `review_budget=一轮实现内 focused review；设计未覆盖的语义变化立即停止`
- `stop_conditions=设计冲突、所有权冲突、规范指纹变化、临时栈无法隔离、质量门失败且无法在当前 owner 内修复`

## 目标与完成定义

完成 U2、U3、U1 的代码、失败测试、实现和同步文档。完成必须同时满足：

1. 每个功能点有独立 conventional commit，提交前 `git status --short` 只含本功能点文件。
2. 固定测试与新增测试通过；tracked Go 文件 `gofmt -l` 无输出，Windows/Linux build、vet、全量 `go test ./... -count=1` 有原始输出。
3. Web 三命令使用临时 `--outDir` 通过，不生成 `web/dist`。
4. U1 在 Windows 主机以 `GOFER_CONFIG_DIR` 临时目录、随机端口、临时 serve/worker 真实验证升级和启动即退出二进制回滚；正式 8767/server/worker/config 不被触碰。
5. `skills/gofer-usage/` 与 `--help`、实际 API/行为同步。

## 范围、排除项与授权

范围按设计 U1–U3：U2 保存配置只写声明键；U3 给传话 job 加内部标记并在 Board/job list/工作台/CLI 默认隐藏、`--all` 显示；U1 增加 `gofer worker upgrade`、server 暂存/worker token 下载、v15 能力闸、排空、切换、回滚、事件和 Web 操作。

排除：正式环境动作、push、发布、迁移、硬件操作；不新增无标记兼容分支；未由设计覆盖的协议、配置和状态语义不在本批自作主张扩展。

- `host_or_non_offline_action=REQUIRED`

## 输入与批准证据

- 已批准设计：`docs/design/2026-10-03-worker-remote-upgrade-design.md`（U1–U3）。
- 当前执行授权：用户要求“先写实施计划候选，提交后不要停，直接按计划实施”，并指定 `DIRECT_CONTINUOUS`、U2 → U3 → U1、Windows 真实临时栈验证。
- 仓库规则：`tools/gofer/AGENTS.md`；工作区规则：`workspace.md`；house-rules/gofer-repo 注入规则。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | worker 二进制文件传输与临时存储 | `internal/xfer`, HTTP upload/download handlers, existing worker token auth | 复用现有 xfer store、鉴权中间件和下载路径 | 在 worker upgrade service 上扩展元数据/权限校验 | OWNER_EXTENSION | 现有传输没有“仅给指定 worker 的一次性升级物”语义 | 不新建第二传输协议；升级暂存随事件/超时清理 |
| CAP-02 | worker 连接能力与协议版本闸 | `internal/wsproto`, `internal/wshub`, worker register/caps | 复用 register、worker token、connection registry | 增加 v15 capability、upgrade frame/result 与连接交接标记 | OWNER_EXTENSION | 现有协议没有升级帧、交接和旧版本明确拒绝 | 单一 wsproto 常量与现有 registry owner 生命周期一致 |
| CAP-03 | CLI/Web 管理动作 | `internal/commands/worker.go`, worker HTTP handlers, Runners view | 复用 worker admin client、HTTP JSON envelope、现有按钮/状态组件 | 增加 upgrade API/CLI/Web 状态与错误展示 | OWNER_EXTENSION | 没有升级入口和事件进度模型 | 不复制 worker 管理 client；skill 与 help 同批更新 |
| CAP-04 | 传话 job 隐藏与过滤 | messenger job tags/metadata, jobstore ListQuery, Board/list views | 复用 tags、ListQuery、现有送达详情 | 增加内部 messenger 标记、默认过滤和 `--all` | OWNER_EXTENSION | 现有传话 job 被当普通 job 展示 | 详情和会话抽屉继续读取同一 job；不复制 store |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 独立升级传输协议 | CAP-01 | 现有 xfer 已覆盖临时文件传输和鉴权；新增协议会产生双重权限/清理生命周期 |

## 前置检查与 fail-closed 条件

- 在每个 mutation stage 前执行 `idev-std resolve --response verify`，指纹或 scope 变化立即停止。
- 计划 validator、SUPMODE preflight 必须 PASS；计划 candidate 由 runtime 生成，不手填 hash。
- 先运行失败测试再实现；任何测试/实现发现需要改变设计协议、schema、外部动作范围或所有权，归类 Semantic Amendment 并停在 Gate。
- Windows smoke 只使用 `tmp/` 下临时 config、随机端口、临时 pid/log；所有 gofer 命令显式 `--server` 或 `-c`。

## 波次与依赖

| 波次 | 内容 | 依赖 | 结果 |
|---|---|---|---|
| W0 | 计划、validator、preflight、基线记录 | 设计批准、规范 BOUND | 计划 commit |
| W1 | U2：配置最小写回 | W0 | 测试 + 实现 + skill/README（如用户可见） |
| W2 | U3：传话 job 隐藏 | W1 | 后端、CLI、Web、Vitest、skill |
| W3 | U1：协议/存储/worker/server/CLI/Web，按协议→后端→前端→真实 Windows | W2 | 功能 commits 与升级/回滚证据 |
| W4 | 全量质量门与完成报告 | W3 | 原始输出、git log/status、G032 清单 |

## 任务

### U2 配置保存只写声明键

- 文件：由现有注册/配置保存 owner 实际发现并记录；对应测试、`skills/gofer-usage/` 文档。
- 动作：先写 `TestRegisterWorkerSavesOnlyDeclaredKeys` 失败测试，修正序列化/保存白名单，保留无关配置字节。
- 验证：`go test ./internal/... -run TestRegisterWorkerSavesOnlyDeclaredKeys -count=1`、gofmt、相关 build/vet。
- 完成标准：workers/runners/allowed_runners 之外无 diff，测试 PASS，独立 conventional commit。
- 依赖：W0。

### U3 传话 job 默认隐藏

- 文件：messenger job model/store/list handlers、CLI job list、Board/工作台/详情 views 与测试、skill 文档。
- 动作：先写 `TestMessengerJobsHiddenByDefault` 失败测试；给 messenger job 加内部标记；默认过滤，`--all` 显示；详情/送达状态保持可读。
- 验证：Go focused tests；Web Vitest；CLI `--help` 与临时 server smoke。
- 完成标准：默认列表无传话 job，`--all` 可见，详情不变，独立 commit。
- 依赖：U2。

### U1 远程 worker upgrade

- 文件：`internal/wsproto`、`internal/wshub`、worker runtime/commands、server/httpapi/client、job/event store、Web Runners、skills 文档及对应测试。
- 动作：按协议→能力闸→排空/切换/回滚→管理 API/CLI/Web 顺序；先写五项固定失败测试，再实现；Windows 路径使用旧版本 worker、当前构建和启动即退出假 exe。
- 验证：固定五项 Go 测试、`go test ./... -count=1`、Web Vitest；临时 serve/worker 升级后版本/在线/job 证据，假 exe 触发回滚并验证旧版本继续在线。
- 完成标准：v15 worker 可升级并交接，旧协议明确拒绝，checksum/超时/失败注册回滚，事件可见，CLI/Web 与 skill 一致，独立 commits。
- 依赖：U2、U3。

## 回滚与恢复

每个功能点使用独立 atomic commit；失败时保留失败测试和原始输出，不回退他人或无关 dirty work。U1 runtime 回滚仅操作临时 worker 目录：`.old` 恢复、临时子进程终止、临时 pid/log 清理；正式配置和进程不触碰。恢复点记录在 `tmp/` SUPMODE stage result。

## 人工 Gate

计划批准和当前执行授权已由本请求给出；实现/本地提交在授权范围内连续执行。唯一外部动作 Gate 是本机临时 Windows serve/worker smoke（不含正式服务、push、发布、迁移、硬件），其余为本地读写和测试。若设计语义不足以决定实现，立即停止并报告差异。

## 可追溯性

设计 U2 → U2 任务与 `TestRegisterWorkerSavesOnlyDeclaredKeys`；设计 U3 → U3 任务、隐藏默认列表/`--all` 验收与 `TestMessengerJobsHiddenByDefault`；设计 U1 → U1 五项固定测试、协议/回滚/Windows smoke。每项验证命令和 commit 记录写入 SUPMODE stage result。

## 完成 Gate 与剩余工作

只有 W4 的格式、build、vet、全量 Go、Web 三命令、Windows 升级/回滚和 git cleanliness 全部有原始输出时标记完成。未实现项、G032 兼容清单、skill 变更章节和人工决策点必须在最终报告逐项列出；不 push。
