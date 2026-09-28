<!-- template_id: plan; template_version: 1.2.0 -->
# S3 TUN-05 server 本机托管隧道实施计划

> 状态：Draft 0.1 / 待人工计划批准；执行方式：DIRECT_CONTINUOUS

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-29 | Codex | 将 Approved 设计的 TUN-05 拆为红测试、共用转发组装、托管生命周期/API、预设与自启动、Web/文档和质量门波次 |

> 仅任务合同、范围、风险、验证、生命周期或其他执行语义变化递增版本；纯 identity/provenance/元数据纠正沿用本版本并在 Git/进度中留痕。

## 目标与完成定义

`thinking_mode=RIGOROUS`。核心目标：让 web 能按 server 本机的预设启动、停止和查看托管转发，并继续使用现有 `gofer tun forward --name <预设>` 的同一 `internal/tunnel` 转发实现；本机 `tunnels.yaml` 中尚未上传的预设可单独列出并导入。

`allowed_scope=Approved 小项批次设计 TUN-05`；`non_goals=worker 或其他机器上的远程启动、改变现有 tun forward CLI 行为或隧道协议、真实配置/生产服务/现场硬件操作、S1/S2/S4`；`expansion_policy=DEFER_OR_REQUEST`。评审预算为一轮发现、一轮核对；只有 CORE_BLOCKING 问题才追加阻断修正，发现协议、权限、数据或生命周期语义变化即停止并返回 design/plan Gate。

完成定义：三个固定测试先有效呈红并单独提交，随后随实现转绿；server 托管转发按预设监听、登记带托管标记、停止清理，重复启动/端口冲突/目标 worker 不在线均给出可读错误；job caller 启停/导入返回 403；`autostart` 在 server 启动时恢复且失败只记录事件/日志不阻塞启动；本机未上传预设列表与单条导入复用 push 的冲突判定；Web 按钮、状态和错误回显可用；全仓 gofmt、Windows/Linux build、vet、指定 Go/Web 测试和控制字符扫描均记录 exit code 与原始输出。计划提交本身不代表功能完成。

## 范围、排除项与授权

- 只修改 `tools/gofer` 独立 Git 仓的 TUN-05 owner：`internal/tunnel` 共用组装和托管状态，`internal/commands` 保留 CLI 行为并调用共用组装，`internal/httpapi` 负责托管路由、权限和本机预设视图，`internal/jobstore`/`internal/config` 扩展 `autostart`，`internal/serve` 负责启动/关闭编排，`web` 负责页面操作，README/`gofer-usage` skill 和设计实测记录负责用法与证据。遵守 G021/G022 分层。
- 托管转发只在 server 进程本机运行；登记复用现有 `ForwarderRegistry` 列表但增加明确的 `hosted` 标记。托管项由内存 manager 持有，不能因普通 TTL 心跳缺失被清理；server 关闭时停止所有 listener/连接并移除登记。
- 预设的 `autostart` 必须贯穿本机 `tunnels.yaml`、server 存储、HTTP、`tun presets push` 和 Web；导入单条沿用 push 的同名冲突/跳过规则，不另建一套冲突语义。
- 只有 user caller 可启动、停止、导入；job caller 只读现有预设、未上传预设和在线转发列表。错误正文直接回显端口冲突、目标 worker 不在线等可诊断信息。
- `host_or_non_offline_action=REQUIRED`：验收需要验证 server 本机 listener 的真实绑定和关闭，但只允许 `t.TempDir()`、临时 config、随机端口和 httptest/本地临时进程；不启动或重载 live server/worker，不碰 `D:\work\inhere\config\win-env\gofer`，不部署、不 push、不做真实流量或硬件动作。

## 输入与批准证据

- 唯一设计依据：[Approved 小项批次设计的 TUN-05 节](../design/2026-09-27-resume-display-plan-handoff-tun-web-hook-watch-design.md)，当前起点为 `1042ff07a6bdd9081e0e50f54fc8b96a4df4feab`（v0.71.0 tag 之后）；用户已批准四项批次设计，本计划只消费 S3。
- 现有积木已核对：`internal/tunnel/forwarder.go` 的 `Forwarder.Run`、`forwarders.go` 的登记/TTL、`internal/commands/tunnel.go` 的 `runTunnelForward`/`runTunnelPresetsPush`、`internal/jobstore/tunnel_presets.go`、`internal/httpapi/tunnel_forwarder_handler.go` 与 `tunnel_preset_handler.go`、`internal/config/tunnels.go`、`internal/serve/serve.go`、`web/src/views/settings/Tunnels.vue` 和 `web/src/api/{client.ts,types.ts}`。
- 本 job 明确授权起草并本地提交 S3 计划候选；监督者可代批“由已批准设计派生的实施计划”。候选尚无批准；提交后停止，等待审批和后续明确实施请求。
- 适用约束：工作区 `workspace.md`、本仓 `AGENTS.md` 的 G021/G022/G032、gofer 注入的 `house-rules`/`gofer-repo`、IDEV-STD 0.22.1 plan 合同。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | CLI 与 server 共用转发组装和 Dial 路径 | `internal/commands/tunnel.go` 的 `runTunnelForward`、`internal/tunnel/forwarder.go`、`client.DialTunnel` | 直接复用 `tunnel.Forwarder`、`ForwardSpec` 解析和 Ready 语义 | 将“解析预设→创建每条 Forwarder→等待 Ready→错误收集”的最小组装下沉到 `internal/tunnel` owner；CLI 仅提供 client Dial/登记回调，server 提供本机 Dial | OWNER_EXTENSION | 当前 listener 组装和 Dial 闭包在 commands，server 无可调用入口 | 不复制 forwarder；旧 CLI 行为保持不变，任何旧 fallback 仍按现有 G032 标记处理 |
| CAP-02 | server 托管启动/停止/登记生命周期 | `ForwarderRegistry`、`Forwarder.Run`、`httpapi.Server` 生命周期和 `serve.RunCtx` | 复用 registry、Forwarder Ready/ctx 关闭和 HTTP shutdown | 在 `internal/tunnel` 或既有 server owner 增加内存 hosted manager，保存 preset→运行句柄/取消函数，登记 `hosted=true` 并跳过 TTL 清理 | OWNER_EXTENSION | 当前 registry 不区分 hosted，server 没有停止句柄或关闭钩子 | manager 是唯一托管真源；停止和 server shutdown 必须幂等，失败不得留下登记或半启动状态 |
| CAP-03 | HTTP 启停、未上传列表、单条导入与 caller 权限 | `tunnel_forwarder_handler.go`、`tunnel_preset_handler.go`、`jobcredential.go`、`runTunnelPresetsPush` 冲突分支 | 复用现有 `/v1/tunnels/*` 路由风格、user/job caller 判定和 push 冲突规则 | 增加 hosted start/stop/list-local/import 路由及共享 handler seam，响应直接包含状态/错误 | OWNER_EXTENSION | 现有路由只有登记/CRUD，server 不读本机 tunnels.yaml | 不为 job caller 开放写路由；导入必须调用同一冲突判断，避免 Web 与 CLI 漂移 |
| CAP-04 | `autostart` 存储、文件、push 与 server 启动恢复 | `TunnelProfile`、`TunnelPresetRecord`、schema 初始化、`serve` 启动段、push 的 `TunnelPreset` 映射 | 复用已有 preset 表、YAML loader、push 的规范化/验证和 serve ctx | 增加布尔字段及兼容默认 false；server 启动后按存储记录逐条拉起，失败仅日志/事件；关闭统一停止 | OWNER_EXTENSION | 当前字段链没有 autostart，启动阶段没有 hosted restore | additive 字段不改变旧客户端；如保留一次性兼容读取必须按 G032 标记并列清单 |
| CAP-05 | Web 操作、托管标识、未上传分组和错误回显 | `Tunnels.vue` 现有轮询/错误显示、`client.ts` tunnel API、`types.ts` 类型 | 复用现有列表轮询、ApiError 和编辑表单 | 增加 start/stop/import API、按钮、状态列和本机未上传分组；不改变普通 CLI 复制命令 | OWNER_EXTENSION | 当前页面明确提示“web 不能代为启动”，无本机列表或 hosted 字段 | 只增加 TUN-05 UI 状态；错误显示服务正文，避免页面自行推断 worker/端口状态 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 独立 hosted-forwarder 服务/子进程 | CAP-02 | 设计要求 server 进程内托管且现有 Forwarder 已有 ctx 隔离；新增服务会改变生命周期和部署边界 |
| 新的预设冲突模型 | CAP-03 | 设计要求复用 `tun presets push` 判定；新增模型会造成 CLI/Web 语义漂移 |
| 新的权限模型 | CAP-04 | 设计要求复用现有 user/job caller；新增模型会改变既有权限边界 |

## 前置检查与 fail-closed 条件

- 计划编写基线：Git root `D:/work/inhere/hyy-ai-inspect/tools/gofer`，branch `main`，HEAD `1042ff07a6bdd9081e0e50f54fc8b96a4df4feab`；`git status --short --untracked-files=all` 无输出。工作区 `tmp/idev-std/` 位于独立 Git 根之外。
- T00 只核对上述 owner 的当前源、测试和 server 生命周期；实施前重新读取并确认实际 API/结构体、worker Dial seam、DB schema 迁移和 Web 路由。若设计与代码事实冲突、需要改变现有 tun forward 协议/CLI、命中他人 dirty 文件或权限/数据语义无法复用，停止并报告具体位置。
- 每次 mutation 前重跑 IDEV-STD BOUND/semantic/fingerprint preflight，确认 HEAD、dirty/untracked 与批准范围；新 path/symbol 按 Operational Discovery、Corrective、Semantic Amendment 或 Ownership Conflict 分类，Semantic Amendment/Ownership Conflict 立即停止。
- 测试一律使用 `t.TempDir()`；smoke 若需启动进程只用临时 config、随机端口，且每条 gofer 命令显式带 `--server http://127.0.0.1:<port>` 或 `-c <临时配置>`；禁止重启/reload 正在运行的 server/worker。
- G032：旧 YAML/DB/API 读取路径只在确有现存用户时保留并标 `// DEPRECATED(vX): remove in vY`；无用路径直接删除；最终报告列出新增/保留/删除项。默认旧记录 `autostart=false` 是 additive schema 行为，不得新增无标记兼容分支。
- Windows `internal/job`、`internal/httpapi` 整包测试可能很慢，必须等待终态；已知 `internal/worker/TestPolicyCacheRoundTrip`（0600）不修。

## 波次与依赖

T00 基线和实际落点核查 → T1 三项固定红测试独立提交 → T2 共用组装与 hosted manager → T3 HTTP、权限、预设导入和 autostart → T4 Web、README、skill → T5 全量质量门与设计实测记录。单 Agent 顺序执行，按功能点分别 conventional commit。

## 任务

### T00 基线和实际落点核查

- 文件：只读 Approved 设计 TUN-05、`internal/tunnel/{forwarder.go,forwarders.go,*_test.go}`、`internal/commands/tunnel.go` 及测试、`internal/httpapi/{server.go,tunnel_forwarder_handler.go,tunnel_preset_handler.go,jobcredential.go}` 及测试、`internal/jobstore/{store.go,tunnel_presets.go}`、`internal/config/tunnels.go`、`internal/serve/serve.go`、Web tunnel API/view 和相关测试。
- 动作：确认每条 `ForwardSpec` 的 Dial 所需 worker/target、Ready 和 ctx 关闭语义；确认 registry TTL 扫描点、server shutdown seam、preset schema/冲突跳过逻辑和 user/job caller 判定；确认本机 config 解析如何在 server 侧安全读取 `tunnels.yaml`。记录实际 owner 与任何 G032 旧路径。
- 验证：`git status --short --untracked-files=all`、`git log -1 --oneline`、定向源码/测试核对、IDEV-STD 阶段指纹。完成标准：T1–T4 的字段流、路由、生命周期和权限边界明确，无未裁决核心语义。
- 依赖：计划获批和后续实施请求。

### T1 三项固定测试先行并单独 red 提交

- 文件：优先 `internal/httpapi/tunnel_hosted_test.go`、`internal/httpapi/tunnel_preset_handler_test.go`、`internal/serve/*_test.go`、`internal/commands/tunnel_preset_test.go`；按实际 seam 增补 `internal/tunnel/*_test.go`、`internal/config/*_test.go`，只 stage 测试文件。
- 动作：写固定名 `TestServerHostedForwardStartStop`（预设启动后在线列表 `hosted=true` 且端口监听；重复启动冲突；端口占用可读错误且不登记；停止关闭监听并移除登记；job caller 启停 403）；`TestForwardAutostart`（true 自动拉起、缺省不拉起、失败只记日志/事件且 server 仍启动）；`TestUnpushedLocalPresetsListed`（本机文件未上传分组、单条导入后消失并出现在 server、同名冲突沿 push 规则）。测试只用临时目录/随机端口，不写 `t.Skip`。
- 验证：运行固定测试集合，同时记录 exit code 与 PASS/FAIL 原文；确认 red 原因是目标行为缺失而非夹具/编译错误。提交前检查 `git status --short` 和 `git diff --cached --name-only`。
- 完成标准：三项有效 red 证据和仅含测试的 `test(tun): cover hosted forward lifecycle autostart and local import` 本地 commit。
- 依赖：T00。

### T2 共用转发组装与 server hosted manager

- 文件：`internal/tunnel/` 共用组装/manager/registry 类型与测试；`internal/commands/tunnel.go` 仅改为调用共用组装；必要的 `internal/httpapi/server.go` 注入和 shutdown seam。
- 动作：抽出 CLI 与 server 共用的预设解析、spec 展开、每条 `Forwarder` 创建、Ready 等待和错误回收；保留 CLI 的 Dial/登记/心跳输出行为。实现内存 hosted manager 的 start/stop/list，按预设名防重复，监听成功后再登记 `hosted=true`，任一 listener 失败则关闭已启动项且不登记；停止和 server ctx 取消幂等。托管登记不能被普通 TTL 误删，普通 CLI 登记继续沿原 TTL。
- 验证：T1 中启停/冲突/端口占用断言转绿；`internal/tunnel` 定向测试覆盖 Ready、ctx 关闭、部分启动回收和 hosted TTL；`go test ./internal/tunnel/ -count=1`；改动 Go 文件 `gofmt -l` 无输出。
- 完成标准：CLI 与 server 使用同一 Forwarder 组装，无复制实现；提交 `refactor(tun): share forward assembly and host manager`。
- 依赖：T1。

### T3 HTTP、权限、预设字段/导入与 autostart

- 文件：`internal/httpapi/{server.go,tunnel_forwarder_handler.go,tunnel_preset_handler.go,jobcredential.go}` 及测试；`internal/jobstore/{store.go,tunnel_presets.go}` 及测试；`internal/config/{tunnels.go,model.go}` 及测试；`internal/serve/serve.go` 及测试；`internal/commands/tunnel.go`/client tunnel 类型及测试。
- 动作：在既有 `/v1/tunnels/*` 风格下明确实现 `POST /v1/tunnels/hosted`（按 preset 启动）、`DELETE /v1/tunnels/hosted/{name}`（停止）、`GET /v1/tunnels/local-presets`（列 server 本机未上传）、`POST /v1/tunnels/local-presets/{name}/import`（单条导入）；必要时在现有预设/forwarder 响应增加 `hosted`、`autostart` 和本机来源字段。启停/导入只允许 user caller，job caller 返回 403；重复/未知/worker 不在线/端口冲突按 HTTP 状态和原始错误回显。给存储表、YAML profile、push 请求和 Web 类型增加 `autostart`，旧值默认 false；server 启动在 HTTP 服务可用前/后按现有编排选择最小 seam 自动拉起，失败写日志/事件并继续启动；server shutdown 停止全部 hosted。
- 验证：T1 三项测试转绿；HTTP/存储/config/serve 定向测试；`go test ./internal/httpapi/ ./internal/commands/ ./internal/jobstore/ ./internal/config/ -count=1`；确认 job caller 仍只读、push 冲突 skip 规则未分叉、G021/G022 依赖无环。
- 完成标准：server 本机启停、autostart、单条导入、权限和错误口径可观察；提交 `feat(tun): add hosted forward HTTP lifecycle and autostart`。
- 依赖：T2。

### T4 Web、README、skill 与设计实测记录

- 文件：`web/src/api/{client.ts,types.ts}`、`web/src/views/settings/Tunnels.vue` 及相关 Web 测试；`README.md`、`README.zh-CN.md` 隧道段；`skills/gofer-usage/` 对应 `SKILL.md`；设计文档追加 TUN-05 S3 实测记录（只在 T5 有真实结果后填写）。
- 动作：预设列表增加启动/停止按钮、托管状态和 `autostart` 编辑；在线转发标“server 托管”；增加“本机未上传”分组与单条导入；使用现有 ApiError 显示服务返回错误，轮询后保持状态一致；保留复制 CLI 命令入口。README 与 skill 记录 server 本机范围、权限和命令/API 用法，保持中英文 README 并行。
- 验证：Web 相关测试；`cd web && pnpm test && pnpm typecheck && pnpm build`，若 Windows esbuild postinstall 仍为 ELF/EPERM，使用本地 `.bin` 等效命令并原样记录；检查普通预设/托管预设/未上传分组和 403/409/端口错误回显。
- 完成标准：Web 可完成设计要求的启停/导入操作且无“不能代为启动”的过时文案；按功能点提交 `feat(web): control hosted tunnel presets`，文档可独立时再提交 `docs(tun): document hosted preset usage`。
- 依赖：T3。

### T5 全量质量门与交付证据

- 文件：必要时仅更新设计文档 TUN-05 S3 实测记录；验证产物放 `tools/gofer/tmp/`，不 stage。
- 动作/验证：tracked Go 文件全仓 `gofmt -l` 无输出；Windows/Linux `go build` 产物写入 tmp；`go vet ./...`；`go test ./internal/tunnel/ ./internal/httpapi/ ./internal/commands/ ./internal/jobstore/ ./internal/config/ -count=1`；Web 三命令；控制字符扫描、`git diff --check`。每项同时记录 exit code、PASS/FAIL 原文和已知基线；不以 server 启动成功替代 listener/登记/停止验收。
- 完成标准：三个固定测试及新增行为全绿，质量门无未裁决回归；设计实测记录只写已验证事实；列出 `git log`、`git status`、G032 清单、未完成项和人工决策点。不得 push。
- 依赖：T2–T4。

## 回滚与恢复

每个功能点本地 commit 是恢复点。每次提交前执行 `git status --short`，只 stage 当前 owner 文件并复核 `git diff --cached --name-only`；不得吸收 unrelated dirty work。失败时记录首个失败、当前 HEAD、已验证范围和下一步；撤销仅针对本任务准确 commit 评估 `git revert`，先核对依赖。实施发现新路径按计划合同分类，不以修改计划版本掩盖语义变化。

## 人工 Gate

本计划候选提交后停在人工计划批准 Gate。监督者依代批授权批准后，还需本会话续接明确实施请求；执行方式已定为 DIRECT_CONTINUOUS。发现需要改变 tun forward CLI/协议、caller 权限、preset schema/state、server 生命周期或验收语义时返回 design/plan Gate。push、远程升级、部署、live 服务、真实配置、真实流量和硬件动作另需具名外部动作授权，本候选不含这些动作。

## 可追溯性

| Approved TUN-05 / 验收要求 | 任务 | 可观察验证 |
|---|---|---|
| server 本机按预设启动/停止，共用现有 Forwarder，登记标托管且端口监听 | T1–T2、T3 | `TestServerHostedForwardStartStop`、tunnel/httpapi 定向测试、端口与登记断言 |
| 重复启动、端口占用、worker 不在线错误可读且失败不登记 | T1–T3 | 固定测试、HTTP 原始错误 body/status |
| hosted 不被 TTL 误删，server 关闭全部停止 | T2–T3 | manager/registry 生命周期测试、shutdown 后监听与列表断言 |
| `autostart` 存储、push、启动恢复，失败不阻塞 server | T1、T3 | `TestForwardAutostart`、config/jobstore/serve 测试、日志/事件证据 |
| 本机未上传列表、单条导入和既有冲突规则 | T1、T3、T4 | `TestUnpushedLocalPresetsListed`、CLI/HTTP/Web 交叉断言 |
| user 可写、job caller 只读；Web 启停/托管标识/错误回显 | T1、T3–T4 | 403 测试、Web test/typecheck/build、页面状态检查 |
| G021/G022/G032、离线边界和质量门 | T00、T2–T5 | 依赖检查、gofmt/build/vet/Go+Web 测试、控制字符和 Git 原始输出 |

## 完成 Gate 与剩余工作

S3 仅在三个固定测试从有效 red 到 green、共用组装/托管生命周期/API/权限/autostart/导入/Web 可观察、质量门结果及按功能点本地提交都有证据后，才能标记实施完成。计划候选提交不等于实施完成；本期不做 push、部署、真实 server/worker 或现场验收。S1/S2/S4 仍为独立期，任何跨期依赖需另行批准。
