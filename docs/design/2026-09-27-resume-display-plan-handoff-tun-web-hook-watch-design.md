<!-- template_id: design; template_version: 1.1.1 -->
# 小项批次：续接显示原 agent（JOB-12）、plan 交接说明（PLAN-04）、web 启动隧道（TUN-05）、Stop hook 盯 job（SESS-03）

> 状态：Approved（文档 identity：Draft 0.1；用户 2026-09-28 批准）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-27 | Claude | 初稿：四个用户在使用中提出的小项 |

## 背景与目标

1. **续接的 job 显示成 exec**：codex/omp 的续接按 resume 模板渲染出命令后，放在 exec 载体 job 里执行，job 的 `agent` 落库为 `exec`，原始 agent 只存在请求里（`ResumeSourceAgent` 不落库）。工作台已按会话归到原 agent，但 Board、job 列表、按 agent 筛选都看到的是 exec。
2. **clear 后接不上**：plan 有 todo、评论、job 记录，但没有一处写着"现在做到哪、下一步是什么、有什么没定"。memory 放长期事实，issue 是工作项，都不适合放这种会过时的交接状态。
3. **隧道只能在终端里起**：web 能看到预设和在线转发，但不能按预设点一下就启动；server 本机 `tunnels.yaml` 里没上传的预设 web 也看不到（2026-09-27 实测 `hw-win11` 即如此，已手动上传）。
4. **Stop hook 挡住 job 完成通知**：中继的 Stop hook 挂起会话等 web 回复时，后台任务完成的通知送不进会话；监督 agent 只能在前台轮询 job，不能结束一轮。

## 范围与非目标

- 范围：上述四项。
- 非目标：改变续接的执行机制（仍是 exec 载体、仍按模板渲染）；隧道在 worker 或其他机器上的远程启动（只做 server 本机）；接入 Claude Code 内部的会话间消息通道（非公开接口）。

## 已确认事实

本批次的四项边界与非目标已在上文冻结；各项决策及其批准日期见“决策（已批准 2026-09-28）”。

## 架构

本设计的四个独立落点见下列 JOB-12、PLAN-04、TUN-05、SESS-03 小节；分期关系见“实施分期”。

## 关键流程

JOB-12 的续接解析、落库/回填、查询与显示；其余三项的读写或监听流程均在各自小节定义，未引入跨项依赖。

## 总体方案

### JOB-12 续接 job 记录并显示原 agent

- jobs 表新增 `resume_agent`：续接载体 job 提交时写入解析出的原始 agent（`ResumeJob` 里的 `resumeAgent`，即沿 `ResumedFrom` 链回溯得到的那个）；非续接 job 为空。打开旧库时一次性回填：对 `agent=exec` 且 `resumed_from` 非空的记录沿链回溯。
- API：job 结果增加 `resume_agent`。`job list --agent codex`（及 HTTP 的 agent 过滤）同时匹配 `agent=codex` 与 `resume_agent=codex`。
- 显示：Board、job 列表、job 详情、plan 详情的 agent 列显示 `codex ↻`（悬浮提示"续接，经 exec 载体执行"）；`gofer job ls`/`show` 显示 `codex (resume)`。
- 执行、权限、审批判定一律不变（仍按现有载体逻辑）。

### PLAN-04 plan 交接说明

- 每个 plan 有一份"最新交接说明"（Markdown，≤16 KiB），保留历史版本。新表 `plan_handoffs {plan_id, version, body, by, at}`。
- CLI：`gofer plan handoff <plan>`（显示最新）、`--set <文本>` / `-f <文件>`（写入新版本）、`--history`（列版本）、`--version N`（看旧版）。
- HTTP：`GET /v1/plans/{id}/handoff`、`PUT`（带 `expected_version`，不一致 409）、`GET …/handoff/history`；job caller 可读，写入须是该 plan 挂载的 job 或 user caller（与 todo 更新同一套权限）。MCP 工具 `gofer_plan_handoff`（get/set）。
- web：plan 详情页顶部"交接说明"卡片（渲染 Markdown，可编辑，显示版本与更新人/时间，可展开历史）。
- 会话开场：`gofer repo prime` 增加一段"进行中 plan 的交接说明"——按 cwd 识别 gofer 项目（复用 `ProjectForPath`），尽力向 server 取该项目最近更新的至多 3 个 open plan 的交接说明（2 秒超时，取不到就省略），计入 prime 的 8 KiB 上限。
- 事件 `plan.handoff_updated`（不进默认通知集）。

### TUN-05 web 按预设启动隧道（server 本机）

- 预设列表增加「启动」「停止」按钮：由 server 进程在本机托管一个转发（复用 `internal/tunnel` 的 forwarder，与 `gofer tun forward --name <预设>` 同一实现），登记到在线转发列表并标"server 托管"。
- 端口冲突、worker 不在线等错误直接回显在页面上；停止即关闭监听与连接。
- 预设新增 `autostart: true`：server 启动时自动拉起。托管状态只在内存，server 重启后只恢复 `autostart` 的那些。
- server 本机 `tunnels.yaml` 里尚未上传的预设在页面上单列"本机未上传"，带「导入」按钮（等价 `tun presets push` 单条）。
- 权限：只有 user caller 可启动/停止/导入；job caller 只读。

### SESS-03 Stop hook 等待时盯住本会话派出的 job

- 登记：`gofer job run` / `job resume` 在 Claude Code / Codex 会话里执行时，由 PostToolUse hook 从工具输出里识别 `job <id> submitted` / `gofer job watch <id>`，登记到该会话的"待盯 job"（`POST /v1/sessions/{sid}/watches`）；另提供显式命令 `gofer session watch <job-id>`。
- 放行：中继 Stop hook 挂起等待时，除等 web 回复外，同时轮询待盯 job；任一 job 进入终态就放行，注入一条 `[gofer job 完成] <id> <标题> status=… exit=… 耗时…`，会话据此继续。多个 job 同时完成合并成一条。
- 只在中继已布防时生效（中继关闭时 Stop hook 本来就不挂起，后台通知照常送达）。待盯 job 在注入后移除；会话结束清空。
- web 会话页显示该会话的待盯 job。

## 实施分期

| 期 | 内容 | 固定测试 |
|---|---|---|
| S1 | JOB-12 | `TestResumeCarrierRecordsResumeAgent`、`TestJobListAgentFilterIncludesResumeCarriers`、`TestResumeAgentBackfill` |
| S2 | PLAN-04 | `TestPlanHandoffVersioning`（含 409）、`TestPlanHandoffPermissions`、`TestPrimeIncludesPlanHandoff` |
| S3 | TUN-05 | `TestServerHostedForwardStartStop`、`TestForwardAutostart`、`TestUnpushedLocalPresetsListed` |
| S4 | SESS-03 | `TestPostToolUseRegistersJobWatch`、`TestStopHookReleasesOnWatchedJobTerminal`、`TestStopHookMergesSimultaneousFinishes` |

四项互相独立。S2 的 prime 一段依赖 TRK-01 P3 的 `repo prime`，排在 P3 之后。

## 风险与限制

- SESS-03 靠识别工具输出里的 job id，输出格式改了会漏登记；`gofer session watch` 是兜底，CLI 输出格式视为契约并加测试。
- TUN-05 的托管转发跑在 server 进程里，隧道流量故障可能影响 server；forwarder 已有独立 goroutine 与超时，按现有 forward 同等隔离，必要时后续改为子进程。
- JOB-12 回填对大库要一次扫描 exec 记录，放在打开库时的迁移里，只跑一次。

## 决策（已批准 2026-09-28）

1. 续接执行机制不变，只补记录与显示。
2. 交接说明独立于 memory/issue，按 plan 保留版本。
3. web 启动隧道只做 server 本机托管。
4. Stop hook 盯 job 走 hooks，不接入 Claude Code 内部消息通道。

## 待确认事项

无。

## 结论与人工计划 Gate

批准后按 S1–S4 派发（S2 在 TRK-01 P3 之后），每期测试先行、容器验证、远程升级。

## S1 实测记录（2026-09-28）

- JOB-12 三个固定测试先以目标行为缺失有效呈红，独立提交 `e6da294`；实现后定向四包测试返回 `ok`。持久化/一次性回填 `9d1e0b7`、agent 过滤与 CLI 显示 `1816507`、Web 三处显示 `da7e003`。
- 旧库回填只在旧 jobs 表首次增加 `resume_agent` 列时执行，列增加与回填共用事务；断链留空，二次打开不再扫描。旧库一次性读取分支已标 `// DEPRECATED(v0.68): remove in v0.71`；未新增其他兼容分支，未删除旧路径。
- `gofmt -l` 全仓无输出；Windows/Linux `go build` exit 0，`go vet ./...` exit 0。build 曾输出 Go 模块 stat cache 写入受限警告，但两个目标产物均生成；控制字符扫描为 `control_chars_total=0`。
- 隔离 `GOFER_CONFIG_DIR` 后，四包全量测试只有 Windows 既知基线 `TestGetArtifactManifest`、`TestWorktreeSymlinkedProjectRoot` 失败：`internal/job` FAIL（362.214s）；`internal/jobstore`、`internal/httpapi`、`internal/commands` 均 ok。默认用户配置目录不可创建导致的首轮 `httpapi` 失败已通过隔离配置目录排除。
- `pnpm test` 在已有 Linux ELF esbuild 的 Windows postinstall 阶段失败，改用仓库内现有 Win32 esbuild 与本地 `.bin` 执行等效命令：Vitest 3 文件/14 测试通过，vue-tsc exit 0，Vite build exit 0。未改真实配置或 live server/worker，未 push；源码和离线验证完成，现场运行验收未做。

## S2 实测记录（2026-09-29）

- PLAN-04 固定测试先以目标行为缺失呈红（HTTP 路由返回 404、prime seam 未实现），随后版本化存储、HTTP、caller 权限、CLI、MCP、prime 字节预算和 PlanDetail 交接卡实现；定向 `TestPlanHandoffVersioning`、`TestPlanHandoffPermissions`、`TestPrimeIncludesPlanHandoff` 返回 `ok`。
- Go：跟踪源文件 `gofmt` 无输出；`go build ./cmd/gofer` 的 Windows/Linux 目标均 exit 0；`go vet ./...` exit 0。构建输出包含既有 Go module stat cache 写入受限警告，但两个产物均生成。
- 指定 Go 包测试：`internal/jobstore`、`internal/commands`、`internal/tracker` 通过；`internal/httpapi` 在 Windows 默认用户配置目录不可创建的既有环境问题下失败（`skill: create root C:\Users\KZL\.config\gofer\skills: mkdir C:\Users\KZL\.config: Cannot create a file because that file already exists`），并伴随测试清理时 database is closed；未归因于 PLAN-04 行为。`internal/mcpserver` 初始工具数量断言因新增工具失败，补齐固定工具清单后应重跑。
- Web：原始 `pnpm test`、`pnpm typecheck`、`pnpm build` 均被 Windows 上 `esbuild@0.25.12` 安装脚本执行 ELF 二进制阻断（Node SyntaxError / `ERR_PNPM_EXECUTOR_LIFECYCLE_SCRIPT_FAILED`）；本机未发现可用 Win32 esbuild `.exe`，因此未伪报等效通过。监督者可在容器补跑原命令。
- 补缺验证（2026-09-29）：`repo prime` 已接入 CLI 同源的 config/env client 解析；按 cwd `ProjectForPath` 过滤 open plan，按 `updated_at` 倒序取最多 3 个并逐个读取最新 handoff。httptest 覆盖正常排序/3 条上限和 2 秒超时静默，固定测试通过；不可达/未识别项目沿相同 best-effort 路径省略段落。
- Web 补缺已完成：历史接口按需加载，列出版本/更新人/时间，点选后以只读 Markdown 预览旧版本；最新版本编辑 CAS 语义不变。使用直接 Node 调用本地工具等效验证：`node node_modules/vue-tsc/bin/vue-tsc.js --noEmit` exit 0、`node node_modules/vitest/vitest.mjs run` 3 files/14 tests passed、`node node_modules/vite/bin/vite.js build` exit 0。原始 pnpm 命令仍受 Windows esbuild ELF postinstall 阻断，监督者可在容器补跑。
- 边界：未 push，未启动/重启 live server/worker，未修改真实配置；G032 无新增兼容分支或删除项。prime 超过 8 KiB 时只截断 handoff 段并注明，既有 prime 段落保持不变。
