<!-- template_id: plan; template_version: 1.2.0 -->
# gofer tunnel 文件日志与 UDP 延迟优化实施计划

> 状态：Approved 0.3 / 实施中

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-15 | Codex | 初稿：按三端日志关联、测量、低风险 UDP 优化和验收拆分实施波次 |
| 0.2 | 2026-09-15 | Codex | 增加 server/worker 关键运行生命周期日志、错误边界和任务状态观测 |
| 0.3 | 2026-09-15 | Codex | 同步人工评审：T00 修订落文、扩充基线与 T02 daemon 旁路/双 sink 验收，允许 lumberjack 并固定失败与轮转策略 |

## 目标与完成定义

目标是让 server/worker 的关键运行状态可通过文件日志观察，让一次 tunnel 能从 server、worker 和本机 forwarder 的文件日志中按 tunnel_id 重建，并用阶段时延与 bytes_up/bytes_down 解释 UDP 下载慢或无响应；在不改变 server 中继拓扑的前提下完成低风险 UDP 优化。

完成定义：三端日志文件默认可定位、JSONL 字段一致、敏感数据不落盘、日志轮转可验证；TCP/UDP tunnel 回归测试通过；UDP 优化前后有同一测试场景的 dial_ms、first_byte_ms、duration_ms、字节计数和错误率对照；真实 HMI 下载仅作为用户执行的外部验收。

## 范围、排除项与授权

范围是 tools/gofer 的 logx、serve/worker commands、server httpapi、worker 注册/重连/策略/任务生命周期、client、tunnel 及测试；允许新增配置字段和 CLI 日志参数。排除 server bypass、直连 data plane、Kinco 协议重实现、部署、重启、抓包提权和硬件下载。

- `host_or_non_offline_action=REQUIRED`

本计划不授权 push、发布、服务重启、worker reload、真实硬件下载或外部消息。当前 workspace 其他 dirty/untracked 文件必须保留。

## 输入与批准证据

输入设计：[2026-09-15-tunnel-file-logging-and-udp-latency-design.md](../design/2026-09-15-tunnel-file-logging-and-udp-latency-design.md)。已批准实施（2026-09-15），批准证据为当前 job 请求：“两份文档已由人工评审批准实施（含下文评审修订）”。当前执行请求仅覆盖 W0+W1 = T00/T01/T02，逐任务本地提交，不 push。

workspace baseline：Git root=D:/work/inhere/hyy-ai-inspect/tools/gofer；branch=main；HEAD=f310a13299e3ad04724f9659a1bf82aa87dc687e；实施前仅本 design/plan 为 untracked，属于 T00。预期 owner 路径为 internal/logx、internal/config、internal/daemon、internal/commands/serve.go、internal/commands/worker.go、cmd/gofer/main.go、internal/commands/tunnel.go、internal/client/client.go、internal/httpapi/tunnel_handler.go、internal/worker/tunnel.go、internal/tunnel、对应测试、example 配置和文档。依赖为 Go 1.25、coder/websocket、stdlib，评审允许新增 gopkg.in/natefinch/lumberjack.v2 及 go.mod/go.sum 记录；不新增独立 telemetry 服务。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 双 sink 结构化日志与轮转 | internal/logx、slog、daemon log paths、lumberjack.v2 | 复用 logx.Setup、slog、run/*.log；允许 lumberjack 轮转 | logx 增加 file handler/轮转，daemon 使用 .out.log | OWNER_EXTENSION | 当前只有 stderr，没有可配置 file sink | 不在 commands、worker、server 各建 logger；logx 独占主日志 |
| CAP-02 | server/worker 运行生命周期日志 | logx、serve/worker commands、wshub、job lifecycle | 复用 slog、worker_id、job_id、现有状态 | 在既有 owner 增加状态转移事件 | OWNER_EXTENSION | 关键运行路径缺少稳定事件和失败边界 | 不在每个包创建独立 logger |
| CAP-03 | tunnel 跨端关联字段 | TunnelInfo、TunnelOpen、server slog、worker handler | 复用 tunnel_id、TunnelInfo、bytes counters | 现有 handler/bridge 增加事件 | OWNER_EXTENSION | forwarder session 没有持久关联 ID | 不新增第二套 tunnel registry |
| CAP-04 | UDP 延迟与吞吐测量 | Forwarder、DatagramBridge、Splice、tunnel ls | 复用 session、binary WebSocket、counters | 增加阶段计时和可选 trace | OWNER_EXTENSION | 当前只有连接总时长 | 不把逐包日志当 metrics 系统 |
| CAP-05 | 文件查看与轮转验证 | daemon paths、Go stdlib 文件 API | 复用 config dir 和 daemon paths | 增加 CLI 路径提示与轮转测试 | DIRECT_REUSE | forwarder 没有持久输出 | 不创建第二个日志配置真源 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| Prometheus/OTel sidecar | CAP-01 | 删除后仍可用 JSONL、slog 和运行状态事件；本期不引入常驻外部服务 |
| pcap/逐包 payload 日志 | CAP-04 | 删除后仍可用方向、长度、计数和阶段时延；payload 有敏感数据和磁盘风险 |
| client-worker 直连通道 | CAP-04 | 删除后当前 server relay 仍满足范围；直连会改变认证、网络和生命周期 |

## 前置检查与 fail-closed 条件

- design 0.3 / plan 0.3 及六条评审修订已批准实施（2026-09-15）；执行范围受当前 job 限定。
- 确认 Git 根、branch、HEAD 和 dirty 归属；发现 owner 冲突立即停止。
- 先运行 tunnel、worker、logx 定向测试建立基线。
- 超出已批准日志字段/配置范围或改变协议 envelope 时停止，回到 design/plan review。
- 任何部署、重启、reload、真实 HMI 下载或提权抓包都需要单独外部动作批准。

## 波次与依赖

| 波次 | 内容 | 依赖 |
|---|---|---|
| W0 | T00 评审修订落文、T01 baseline | design/plan 已批准实施 |
| W1 | logx 双 sink、轮转、脱敏和 daemon .out.log 旁路 | W0；允许 lumberjack.v2（零传递依赖） |
| W2 | server/worker 运行生命周期与 tunnel 关联日志 | W1 |
| W3 | forwarder 日志、UDP 分段计时和低风险优化 | W2 |
| W4 | 回归、性能对照、文档和交付检查 | W3 |

## 任务

### T00 评审修订落文

- 文件: 本 design/plan。
- 动作: 0.2 升为 Approved 0.3，记录六项已批准决定并同步 T02 范围；单独 docs(design) 提交。
- 验证: 文档 validator、diff check。
- 完成标准: daemon 所有权、失败语义、轮转、run id、trace、sink 默认行为一致；Kinco 源端口仍待确认。
- 依赖: 当前人工评审修订。

### T01 建立日志与性能基线

- 文件: internal/logx/*_test.go、internal/tunnel/*_test.go、internal/httpapi/*tunnel*_test.go。
- 动作: 运行现有定向测试；记录 TCP/UDP 建立、首包、关闭、字节计数现状；不修改运行配置。
- 验证: go test ./internal/logx ./internal/tunnel ./internal/worker ./internal/httpapi ./internal/daemon ./internal/config；逐包记录摘要及耗时，单列 Windows 既有 TestPolicyCacheRoundTrip mode 0600 失败。通读 logx、daemon、serve/worker daemon 入口与 RuntimeFilePath；无文件改动则不创建基线提交。
- 完成标准: 基线可重复，已知失败与本计划失败分离。
- 依赖: design 批准。

### T02 扩展统一文件日志 owner

- 文件: internal/logx/logx.go、internal/logx/logx_test.go、internal/config/model.go、internal/config/loader.go 与配置测试/example、internal/daemon/daemon.go、internal/daemon/daemon_unix.go（Windows 保持 errNotSupported）、internal/commands/serve.go、internal/commands/worker.go、cmd/gofer/main.go（若签名变化）、go.mod/go.sum。
- 动作: 增加 stderr text/file JSONL 双 sink、路径解析、进程级 operation_id 和 component、ReplaceAttr 脱敏及正确转发 Enabled/Handle/WithAttrs/WithGroup 的 multi-handler。默认 server/worker 文件开启，纯 CLI 不开；共用 GOFER_LOG_LEVEL。轮转使用 lumberjack：50 MB / MaxAge 14 天 / 10 份备份，字段可配置，无定点日切。显式路径打开/写入失败使启动失败，隐式默认路径失败 warn 一次并降级 stderr。daemon stdout/stderr 追加写 .out.log（不轮转、只承接 panic/非 slog），主 .log 由 logx 独占，-d 提示保持主日志。
- 验证: 路径、JSONL 必需字段、轮转触发/清理、并发写、脱敏、显式失败/隐式降级、multi-handler WithAttrs/WithGroup 测试；gofmt -l ./internal ./cmd、go build ./...、go vet ./...、go test ./internal/logx ./internal/config ./internal/daemon ./internal/commands、go test ./...；单列 Windows 已知基线失败。
- 完成标准: stderr text 格式保持，JSONL 可关联进程且无敏感字段，未到保留期限且未超份数的备份不被清理；文件失败可见、所有权无双写，不改 tunnel/httpapi/worker 业务日志。
- 依赖: T01。

### T03 接入 server/worker 关键运行生命周期日志

- 文件: internal/commands/serve.go、internal/commands/worker.go、internal/worker/serve.go、internal/worker/reload.go、internal/wshub/hub.go、任务执行 owner 及对应测试。
- 动作: 记录 server 启动/配置加载/ready/shutdown/http error；worker 启动、注册、心跳连续失败、重连、断开、策略接收/应用、任务开始/完成/拒绝。正常高频心跳只记录聚合状态和恢复事件。
- 验证: 启停、注册/断线/重连、策略更新、任务成功/失败/拒绝测试；确认字段包含 component、event、worker_id/job_id/operation_id 和错误边界。
- 完成标准: 不看终端也能从 server/worker 文件日志判断当前状态、最近失败点和是否恢复。
- 依赖: T02。

### T04 接入 server/worker tunnel 生命周期字段

- 文件: internal/httpapi/tunnel_handler.go、internal/worker/tunnel.go、对应测试。
- 动作: 使用已有 tunnel_id 关联 open、worker callback、splice close；补 network、target、阶段时延、bytes、close reason 和 error code；禁止 token/payload。
- 验证: rendezvous、worker reject、TCP/UDP close 测试；检查 JSONL 字段和脱敏。
- 完成标准: 三端可按同一 ID 对齐，拒绝与成功都有终态事件。
- 依赖: T03。

### T05 持久化本机 forwarder 日志

- 文件: internal/commands/tunnel.go、internal/client/client.go、internal/tunnel/forwarder.go、对应测试和 CLI 文档。
- 动作: 将 fmt.Printf 接入 logger；增加 --log-file/--log-dir；记录 forward、UDP session、首上行/首下行、idle close、up/down；--quiet 只影响终端。
- 验证: TCP/UDP forwarder、多实例不覆盖、取消 flush；CLI --help 检查。
- 完成标准: 终端关闭后仍可取得 session 字节和关闭原因。
- 依赖: T02、T04。

### T06 实施低风险 UDP 优化与测量

- 文件: internal/tunnel/forwarder.go、internal/tunnel/datagram.go、internal/worker/tunnel.go、对应测试。
- 动作: 保持 session 复用；减少重复 buffer/复制；增加阶段计时和有界缓冲；保持压缩关闭与 binary message 语义；不改 allowlist 或 server relay。
- 验证: UDP echo、来源隔离、idle/max session、异端口回复、TCP 回归；同一负载对照 p50/p95 first-byte 和总时长。
- 完成标准: 无回归，UDP 指标改善或证明瓶颈在 HMI/协议。
- 依赖: T04、T05。

### T07 收口与验收证据

- 文件: docs/runbook/tcp-tunnel.md、设计/计划链接、tools/gofer/tmp 证据。
- 动作: 更新日志位置、字段、tunnel ls 与 UDP 诊断说明；执行完整测试、diff check 和敏感字段扫描。
- 验证: go test ./...、go vet ./...、git diff --check；真实 HMI 仅在单独授权后执行。
- 完成标准: 代码测试通过、文档可操作、日志链可重建；部署/硬件状态单独报告。
- 依赖: T06。

## 回滚与恢复

每波次保持独立本地提交；回滚优先关闭 file sink 和新增 CLI 选项，恢复旧 stderr/终端输出。UDP 优化必须可关闭。不得 reset/clean 丢弃无关 dirty work；出现协议、owner 或外部动作变化时暂停并回到 design review。

## 人工 Gate

1. design 0.3、plan 0.3（含六条评审修订）已批准实施（2026-09-15）。
2. 当前请求授权直接执行 T00/T01/T02 并逐任务本地提交；后续波次由对应 job 授权，不得提前实施。
3. server/worker 重启、reload、部署、push、提权抓包和真实 HMI 下载属于外部动作，须逐项明确批准。
4. 需要 client-worker 直连或固定 UDP source-port 改变外部协议时，停止并创建语义修订。

## 可追溯性

| 目标/验收 | 任务 | 验证 |
|---|---|---|
| server/worker 关键运行状态可观察 | T02,T03 | 生命周期测试、状态恢复样本 |
| 三端可按 tunnel_id 关联 | T02,T04,T05 | JSONL 字段测试、跨端样本 |
| forward 日志可落盘 | T02,T05 | 文件写入、轮转、取消 flush |
| 解释 UDP 慢/无响应 | T01,T04,T05,T06 | first-byte、阶段时延、up/down |
| 低风险优化且不改拓扑 | T06 | UDP/TCP 回归与前后 p50/p95 |
| 安全与回滚 | T02,T06,T07 | 脱敏扫描、开关回滚、diff check |

## 完成 Gate 与剩余工作

完成必须同时满足 server/worker 运行日志、tunnel 日志、代码质量、性能对照、文档验证和 dirty work 保护。若 HMI 真实下载未授权或未执行，只报告模拟/本地证据，不宣称硬件验收。若 UDP 性能没有改善，保留分段测量结论，后续直连 data plane 另立 design。
## T06 测量结论

全链路 loopback 下 relay + forwarder 的 RTT p50 约 0 ms、p95 约 0.52 ms，最大值约 1.0–2.2 ms；dial/首包通常 0–1 ms。
池化开关在该样本上 p95 基本无可测差异，最大值仅有调度噪声级波动，未显示稳定收益。
因此 HMI 下载慢时应先看 `first_down` 与设备侧响应；当前 loopback 证据不足以认定 relay 是主要瓶颈。
