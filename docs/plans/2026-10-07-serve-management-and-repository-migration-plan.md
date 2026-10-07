<!-- template_id: plan; template_version: 1.2.0 -->
# Gofer 原生 server 管理与仓库迁移准备实施计划

> 状态：Draft 0.1 / 待人工计划批准

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-07 | Codex | 按设计 0.2 拆分路径解析、原生服务管理、job 自升级、tracker 离线迁移准备及隔离验收 |

## 目标与完成定义

`thinking_mode=RIGOROUS`。核心目标：用户仅用 Gofer CLI 管理 server，配置资产不依赖源码位置，
开发 agent 能通过 job 自升级 server 并继续使用既有 worker upgrade，仓库具备独立 tracker 的迁移能力。
scope freeze 为设计 0.2 的四项；不扩大到发布平台、任意变量模板、worker 协议重写或集群升级调度。
review budget：一次 discovery、一次两轴确认；仅开放核心 blocker 才修订，最多八轮。
停止条件：计划批准前不写产品代码；实施期命中授权边界、核心方案变化、dirty 冲突或失败验收时暂停相应任务。

完成分为两层：T1–T9 为源代码与隔离环境验收；T10 为实际资产/服务/数据切换。
T1–T9 全部通过才能交付可部署候选，T10 未执行时不能宣称实际迁移完成。
每个任务逐项验证、记录证据、按功能点本地提交，进度使用 Gofer issue，不另建 Markdown TODO。

## 范围、排除项与授权

- 范围：本仓的 config、serve/commands、daemon、最小 servicemgr、tracker 离线迁移工具、对应测试与文档。
- 默认允许计划编写、只读核对、独立评审、本任务文档及 tracker 的精确本地提交。
- 计划批准及当前执行请求到位后，代码实施和独立测试实例的创建/启停/卸载才进入执行授权。
  测试对象必须独立 name、config-dir、端口、证书和数据；不得接管已有运行实例。
- T10 涉及现有证书、服务入口、tracker 真源或镜像及目录移动，必须取得覆盖具名目标的外部动作批准。
  本计划不授予 push、tag、release、正式安装、其他项目编辑、设备操作或外部消息权限。
- 文档中不记录其他项目名称、机器路径、账号或生产凭据；部署输入只存在使用方的操作证据中。
- `host_or_non_offline_action=REQUIRED`

## 输入与批准证据

- 设计：[设计 0.2](../design/2026-10-07-serve-management-and-repository-migration-design.md)，候选 commit `e36b59def2e690b16820328baca5c1315399be12`。
- 设计方向确认/准备请求：用户原话“OK 是否可以开始实施了”。这是编制可执行计划的输入，不预填本计划批准。
- 独立评审：设计及本计划的 Standards/Governance 与 Spec/Executability 均待完成；作者自检不代替独立结论。
- 执行模式：等待用户明确选择 DIRECT_CONTINUOUS 或 SUPERVISED_DELEGATION；首次 task 实施前记录到执行状态。
- 规则：G001/G003/G011/G021/G022/G032/G045，SR1103/SR1107/SR1137/SR1204/SR1211/SR1430.2。

workspace baseline：Git root 为本 Gofer 独立仓库根；分支 `main`；准备开始 HEAD 为设计候选 `e36b59d`；
准备开始 `git status --short` 为空。父工作区 tracker 是另一 Git owner，记录与本仓代码分开精确提交。
实施启动时重新记录 HEAD、dirty/untracked 归属和保留的其他改动，不能用当前干净状态推断未来仍干净。
expected owner 为下方各任务模块；同范围新增文件记录 Operational Discovery，不借此吸收无关工作。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 配置目录解析与路径模板 | config.ConfigDir/Load/Save/withoutRuntimeValues | 复用目录选择与原文投影 | config owner 增路径字段解析/校验 | OWNER_EXTENSION | 无精确 config_dir 路径解析且不能原地展开污染 Save | 一个解析入口，业务文本与远端路径不处理 |
| CAP-02 | 证书与 Web 资产 | tool.runToolCert/certutil/httpapi TLS/webui | 复用证书生成与嵌入 Web | 调用路径 helper 并增加配对校验测试 | OWNER_EXTENSION | TLS/Web 消费边界目前直接用配置字符串 | 不增加 CA 服务或第二 Web 分发机制 |
| CAP-03 | 服务生命周期 | serve/stop/daemon、现有 ps1、systemd、Windows Task Scheduler | 复用信号/PID/平台原生能力 | 入口转发与平台调用需单一编排 owner | MINIMAL_NEW_MODULE | 缺跨平台 register/start/status/uninstall 与受管实例描述 | servicemgr 唯一管理服务入口；不用 AI supervisor 包 |
| CAP-04 | Windows 进程隔离 | daemon.StartDetached/proctree Job Object | 复用 Windows 原生事件和进程能力 | daemon owner 增 strict breakaway 模式 | OWNER_EXTENSION | 普通 detach 允许失败降级，不能用于停掉调用方的升级 | 默认 daemon 行为不变，升级必须证明隔离 |
| CAP-05 | 二进制校验与原子切换 | worker.VerifyUpgradeFile/SwitchBinary/UpgradeDeps | 保留 worker 协议实现 | 对文件切换算法按需求提取最小中立 helper | THIN_ADAPTER | worker 注册交接不能直接作为 server 的切换编排 | 只共享文件算法，不耦合 worker wire 与服务管理 |
| CAP-06 | drain 与重启后结果 | job.Service/Submit、jobstore active queries、serve/recovery | 复用任务状态及 PID/健康验证 | job/serve owner 增升级静默准入和状态读取 seam | OWNER_EXTENSION | 无全局升级 drain gate 与跨重启 upgrade_id 回执 | gate 覆盖提交/续接/内部调度，不能读取失败就当空闲 |
| CAP-07 | tracker 发现/存储/同步 | tracker.Discover/Init/Store/SyncHTTP、repo prime | 复用完整对象、锁与原子写 | 一次性迁移工具调用 Store，记录明确清单和新身份 | OWNER_EXTENSION | 现有 repo migrate 面向 bd，不处理现有 tracker 选择拆分 | 不新建通用在线迁移 API，不复制游标/锁 |
| CAP-08 | 用户文档与 Skill | README 两种语言、skills/gofer-usage、runbook、脚本调用 | 使用现有文档与 Skill owner | 新 CLI/路径/升级行为同步修订 | OWNER_EXTENSION | 新行为尚未被现有帮助和使用资料描述 | 删除旧入口要以 job 升级实测为条件 |
| CAP-09 | 平台验证 | Go 工具链、现有 daemon/worker 测试、Windows 主机、Linux systemd 环境 | 原工具链和原生 OS 管理器 | 构建平台测试程序并在隔离实例运行 | DIRECT_REUSE | none | Linux cross-build 不代替 systemd/进程实测 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| MOD-01 | CAP-03 | internal/servicemgr | serve、daemon、AI supervisor、ps1 与 OS 管理器 | daemon 仅管进程，serve 管应用，AI supervisor 管决策 | 放 commands 违反 G021；塞 daemon 混合后台化与持久注册 | 需要具名服务描述、OS入口、升级执行者与回滚的唯一 owner | commands 转发；servicemgr 管系统入口/内部 helper；serve 保持应用 owner | 不使用新包会迫使维护 ps1 或复制生命周期；只建本需求最小包，不建通用 framework |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| 通用配置模板引擎 | CAP-01 | 删除后 config_dir 精确替换仍满足目标；避免 prompt/远端路径误替换 |
| 新 worker 升级 API/协议 | CAP-05 | 删除后现有 worker upgrade 可完成交接；server 只复用文件算法 |
| 通用 repo export/import 平台 | CAP-07 | 一次性离线清单和 Store 已满足本次拆分；避免扩大维护面 |
| 运行期脚本生成器 | CAP-03 | 原生管理器和 Go helper 可完成操作，不保留 ps1 作为隐藏依赖 |

已确认代码图 generation 为 2026-09-05，相关路径存在 metadata_changed/not_tracked/scripts excluded；
已对 config/serve/daemon/worker/tracker 相关路径检查 coverage，实质事实回退当前源码。
这不是完整审计，实施前按具体 owner 重新核对；新包 gap 由既有能力和脚本职责的对照证明。

## 前置检查与 fail-closed 条件

1. validator、设计/计划两轴评审与计划批准均满足；执行模式已明确，当前执行请求仍适用。
2. 同一 clean runtime 严格加载并通过 fingerprint Gate；Git owner/dirty 保护通过。
3. Windows Go 工具链可用；已有 Linux 环境的 PID 1/systemctl/user manager 已只读核对为可用。
   Linux 无本地 Go 时使用主机 cross-build 的测试程序，再原生执行；启动前复核实际权限与独立用户/unit。
4. 测试目录统一在本仓 tmp，所有测试 name、端口、配置和 DB 均独立；系统入口不与现有实例同名。
5. 任何 OS 权限不足、strict breakaway 失败、独立 cgroup 创建失败、配置/证书无效、目标身份冲突立即拒绝切换。
6. 有界 drain：固定超时，排除发起升级 job；计数/准入失败停止升级，不设置“任务数=0”兜底。
7. 运行数据 schema 变化、跨 owner dirty、新的外部 Interface/授权或不可逆操作进入 Semantic Amendment Gate。
8. plan-checking 使用真实检查结果；缺批准/执行模式时不得生成 PASS 或直接开始 T1。

## 波次与依赖

| 波次 | 任务 | 输出 |
|---|---|---|
| W0 | T0 | 独立评审、计划批准、执行模式与 preflight |
| W1 | T1、T2 | 路径解析与最小管理元数据/进程 primitive |
| W2 | T3、T4 | Windows 和 Linux 原生服务入口 |
| W3 | T5、T6 | drain/独立升级执行者与完整 CLI |
| W4 | T7、T8 | tracker 离线迁移工具与资料同步 |
| W5 | T9 | Windows/Linux 隔离端到端验收、可部署候选 |
| W6 | T10 | 经外部动作批准后实际接管/资产迁移 |

若选择 DIRECT_CONTINUOUS，按任务依赖顺序在当前会话执行；若选择 SUPERVISED_DELEGATION，
Supervisor 不实现 task，派发时按模块 owner 隔离。共享 commands/serve/daemon 或 tracker 写入串行，
每次 task 完成必须验证、回写 issue/commit 并选择唯一下一项。

## 任务

### T0 锁定计划与前置条件

- Owner：主 Agent；文件为本计划/设计的中立候选、docs/review 下各轴报告与必要 ledger；执行记录放 tmp/serve-management。
- 动作：独立核对设计及计划的两个轴；只修核心 blocker；记录原话批准、执行模式和精确候选。
  启动前生成 plan-checking 并检查 dirty/工具链/平台权限/测试对象隔离；不预填任何评审 PASS。
- 验证：官方 design/plan/review validator、git_candidate_revision、SUPMODE validate_preflight。
- 完成标准：两轴无开放 CORE_BLOCKING，计划已批准，模式明确，preflight PASS；否则 T1 不可执行。
- 依赖：当前设计 0.2；用户 Gate。

### T1 本机服务路径模板

- Owner：路径任务执行者；internal/config 的 paths.go/tests、loader.go/model.go/writer.go 的最小消费/保存边界；
  internal/commands/serve.go/tool.go、internal/core/core.go、internal/serve/serve.go、internal/httpapi/server.go 的路径取用点。
- 动作：精确替换根前缀 config_dir，校验未知变量；保持无变量路径与 -c 语义；通过 runtime path view/访问函数消费，
  原始 Config 不被展开。覆盖 TLS、Web、log.file/dir、storage.root/db_path 及 CLI web-dir/cert out-dir。
- 验证：`go test ./internal/config ./internal/certutil ./internal/commands ./internal/core ./internal/serve ./internal/httpapi`，
  focused cases 验证环境覆盖、默认目录、空格/Unicode、未知变量、Save/重新加载保留模板、prompt/远端路径未处理。
- 完成标准：所有支持字段在实际消费点解析，原文保存不污染；TLS 成功/错误都可观察。
- 依赖：T0。

### T2 管理 owner 与平台 primitive

- Owner：服务基础执行者；internal/servicemgr/{spec,state,lock,manager}.go 与 tests；internal/daemon 的 strict detach/进程身份能力。
- 动作：定义本机 spec、唯一实例锁、owner 与 pid/exe/配置核对；写入 config-dir 下原子元数据；
  normal daemon 不变，升级提供不允许 breakaway 降级的原生入口；stdio 独立文件，内部 helper 参数不带 token。
- 验证：`go test ./internal/servicemgr ./internal/daemon ./internal/proctree`；Windows strict detach 拒绝测试、
  锁竞争/半写恢复/路径含空格测试；Linux build-tag 构建。
- 完成标准：同名其他实例拒绝；已运行配置变更须先 stop；运行目录遵循现有 -c/pidfile 规则；strict 隔离失败无副作用。
- 依赖：T1。

### T3 Windows Task Scheduler 与 Go supervisor

- Owner：Windows 服务执行者；internal/servicemgr 的 windows 后端/supervise 文件与 tests，commands/serve_management.go 的薄入口。
- 动作：原生 API/COM 注册具名 XML，InteractiveToken/AtLogOn/Limited，明确 elevated 与权限；不调用或生成 ps1。
  管理独立 supervisor 副本，有限重启/退避/停止意图，默认 stop 不强杀；验证旧 ps1 入口后才允许 --adopt。
- 验证：单元用 native seam 验证 XML/所有权/幂等/失败恢复；隔离计划任务注册/启动/stop/restart/uninstall，
  核对 session、PID、exe、配置和实际端口；触发子进程及 supervisor 故障验证预算。
- 完成标准：无 ps1/pwsh 的发布目录可运行；stop 后两个进程退出且不复活；uninstall 保留数据。
- 依赖：T2；主机测试权限。

### T4 Linux systemd 后端

- Owner：Linux 服务执行者；internal/servicemgr 的 linux 后端及 tests。
- 动作：system/user scope 原生 unit 转义与 owner；system 必须显式 run-as；register 不默认 start；
  daemon-reload/enable/start/stop/disable/remove 通过参数数组，unit 不含凭据；原应用 `.env` 读取仍保留。
- 验证：unit 内容和 systemctl 调用 seam；跨编译后 Linux 原生执行隔离 system/user unit，核对执行用户、
  config-dir、Restart/TimeoutStop、显式 stop 不重启、卸载后文件保留；日志同时验证文件与 journal 来源。
- 完成标准：system/user 两种 scope 均通过实测；权限失败无提权、无错误成功回执。
- 依赖：T2；可用 systemd 环境与测试授权。

### T5 job 发起自升级与回滚

- Owner：升级执行者；internal/servicemgr 的 upgrade/upgrade_helper/platform 文件与 tests；internal/daemon strict 分离入口；
  internal/job/service.go/submit.go/resume.go、internal/serve 的升级控制桥接与调度检查、internal/jobstore 只读状态 seam；
  需要共享文件算法时在原 worker owner 或最小中立 util 内抽取，保留 worker tests。
- 动作：预构建候选的平台/hash/执行校验、同卷暂存；持久 upgrade_id、来源 job、状态和版本；
  Windows helper 独立于 runner Job Object，Linux helper 使用独立 transient unit/cgroup。
  确认接管后 job 返回 accepted，终端可等终态；升级 drain 临时关闭新提交/续接与内部自动调度，
  等已有在途工作并排除来源 job，超时/失败恢复准入。停机前再确认隔离和目标身份。
  旧 server/supervisor 退出后 helper 继续替换、刷新 supervisor、启动验证、失败恢复旧文件/spec并确认回滚。
- 验证：候选校验失败、锁竞争、drain 自等待/超时、隔离拒绝、旧父进程结束、候选起不来、回滚失败、
  helper 中断/重读持久状态；worker 原文件切换与升级测试保持绿。原生平台以 exec job 发起全过程并重连查询。
- 完成标准：来源 job 中断不决定终态；无独立执行者时绝不停 server；CLI/server 均可查询跨重启结果；
  并发准入/调度不导致 drain 错判，正常 daemon/job/worker 行为不被全局放宽。
- 依赖：T3、T4；T2 的隔离与锁。

### T6 CLI 集成、状态与日志

- Owner：CLI 执行者；internal/commands/serve.go、serve_management.go/tests、stop.go 的受管转发点；对应 servicemgr 接口。
- 动作：绑定 register/start/stop/restart/uninstall/status/logs/upgrade 与 upgrade status；遵守公共 config flag；
  未登记 stop 仍复用原 pidfile 语义；状态区分系统入口、supervisor、server、健康与安装/运行版本，
  不因另一个进程的 /health 200 报本实例成功。日志默认实际应用文件，Linux 可选 journal。
- 验证：CLI help/解析测试、无效平台参数、未知/已停实例、重复操作、no-wait 和 JSON 输出；
  实际调用 T3/T4/T5 的测试实例，不以 mock CLI 成功代替运行证据。
- 完成标准：设计规定命令均可用，退出码与失败原因一致，敏感配置不进入参数/输出。
- 依赖：T3、T4、T5。

### T7 tracker 一次性离线迁移工具

- Owner：tracker 工具执行者；internal/tracker 的最小 transfer helper/tests，必要 store.go 原子写 seam；scripts 下中立 Go 离线入口。
- 动作：生成只读候选与显式 ID/key allowlist，核对父子/依赖/反向边界、完整对象和 source hashes；
  生成离线包；导入暂存目标后整体发布；新 tracker_id/new issue prefix、旧 ID 保留、不复制 .local。
  目标默认 auto_sync=false；冲突不覆盖；实际源/目标及 key 清单由使用方输入，不固化项目名称或路径。
- 验证：fixture 含未打标签记录、全部状态、评论/notes/时间、跨边界关系、重复/冲突导入、并发 source 变化；
  检查选中记录全字段相等，未选源记录未变；临时仓库 `repo prime/status` 指向自身 tracker。
- 完成标准：离线包可审核，导入可回滚/幂等；实际共享源不被写入，本任务不触发正式 sync。
- 依赖：T1（目录语义）；实际迁移另属 T10。

### T8 文档/Skill 同步与旧脚本收敛

- Owner：资料执行者；README.md/README.zh-CN.md、skills/gofer-usage、config examples、docs/runbook 中相关入口，scripts 的引用。
- 动作：基于已实现 CLI/help 同步 Windows/Linux 管理、路径变量、job 自升级/结果查询及 worker 协同；
  列清旧脚本替代映射。只有 T9 的 job 自升级通过且正式入口切换完成，才能删除旧 start/supervisor/selfupdate 脚本。
  在此之前保留旧入口，不能让正在运行的实例下一次重启找不到 ps1；测试脚本按测试需要保留。
- 验证：双语内容与 help 对齐、链接检查、中立性扫描、配置例子校验；G045 列出实际修改节。
- 完成标准：用户可复现管理与自动升级；旧入口仍在使用时有明确迁移标记，不声称已经退役。
- 依赖：T6、T7。

### T9 隔离实机验证与可部署候选

- Owner：独立验证执行者；本仓 tmp 下隔离样例/日志/结果，必要测试代码/脚本仅验证本功能；不碰正式实例。
- 动作：Windows/Linux 各创建独立 name/config-dir/端口/数据，验证管理、TLS、Web、任务运行、崩溃重启、
  exec job 发起自升级/回滚和持久查询；移走测试源码脚本后再验证无 ps1 依赖；用 fixture 验 tracker 与资料。
  既有 worker 升级协议使用原 tests 加隔离 worker 升级确认，不能选择现有在役 worker。
- 验证：focused tests 完成后跑一次 `go test ./...` 和 `go vet ./...`；Linux native tests 或 cross-built tests 原生运行，
  平台结果记录在实际环境日志；检验进程/端口/配置/版本归属，测试结束卸载全部具名测试对象。
- 完成标准：设计验收矩阵在两平台有实际证据，无开放核心 blocker，候选功能齐全；不将 source PASS 当作正式部署完成。
- 依赖：T1–T8。

### T10 实际资产与目录切换（外部动作 Gate）

- Owner：获授权操作的主 Agent；具体机器/源目标/清单写使用方操作记录；Gofer 通用文档不写具体其他项目/路径。
- 动作：冻结 source 清单并备份；保留 CA 搬证书/切模板配置；独立 tracker 导入核对后绑定独立 project与同步；
  新 CLI 接管现有入口；确认相关任务结束后停机搬仓库，重登记；修复 Skill 链接、容器映射/索引及 worktree。
  保留源 tracker 快照；源清理另核对同步镜像，不能通过关闭业务 issue 伪装迁出。
- 验证：HTTP/HTTPS/Web、运行版本、历史数据、无副作用 job、tracker 全字段/关系/身份及 sync，旧路径引用清单清零或明确历史归档。
- 完成标准：用户具名批准的迁移目标实际可用，回滚点可读；否则状态明确为未执行，不关闭迁移目标。
- 依赖：T9；覆盖具名服务/证书/tracker/目录目标的外部动作批准。

## 回滚与恢复

每 task 按功能点提交，进度记录实际 paths、验证命令/结果、commit、下一 task 与 lifecycle 状态。
产品代码回退使用本任务提交的定向 revert，不重置/清理工作树。失败测试对象由自身 owner 卸载，保留日志。
注册失败恢复原 OS 入口；升级失败恢复已核对的旧程序/spec，不回退 DB；不具备 schema 可逆性时停止自动回滚。
tracker 实际导入前留完整源快照；新目标失败不修改源，源同步缓存不克隆；源删除/镜像处理须另有具名批准。
恢复时重新核对规范、计划 candidate、issue 状态、在途 Agent、HEAD/dirty 与测试进程，不能仅凭旧摘要继续。

## 人工 Gate

1. 设计及计划独立评审结果、批准本计划 0.1 和当前执行请求；明确执行模式。
2. 若 T10 就绪，给出已验证候选和具名目标清单，再取得一个覆盖全部实际切换动作的外部动作批准。
3. 涉及真实私钥/凭据更改或未设计的 schema/权限/生命周期变化，停止对应任务并取得必要批准。

## 可追溯性

| 设计目标/决策 | 任务 | 验收 |
|---|---|---|
| D1/D2 路径模板与原 CA | T1、T9、T10 | 原文保存、TLS 与资产迁移证据 |
| D3/D4/D5 原生服务管理 | T2、T3、T4、T6、T9 | 两 OS 注册/启停/卸载/身份及数据保留 |
| D6/D7 job 自升级 | T2、T5、T6、T9 | 独立执行者、drain、跨重启终态、失败回滚 |
| 既有 worker upgrade 保留 | T5、T8、T9 | 原 tests 与隔离注册交接/回滚 |
| D8/D9 tracker 独立归属 | T7、T9、T10 | 明确清单、字段/引用守恒、新 tracker_id 与自身 prime/sync |
| G045 资料一致 | T8 | README/Skill/help/例子与实际行为一致 |
| 用户中立性与实施边界 | T0、T8、T10 | 文档扫描与具名外部批准，不扩大其他项目 owner |

## 完成 Gate 与剩余工作

计划批准前状态为等待人工计划 Gate，绝不填 preflight PASS。T1–T9完成后报告 source/test 与候选 commit，
T10未批准/执行则另列 runtime/migration 未完成；不能把其中一层绿当作所有功能已验收。
首次唯一 COMPLETE transition 依执行合同检查并记录，不重复询问或生成未经同意的 retrospective。
所有强制任务、评审、测试及适用外部 Gate 实际通过后才关闭实施/迁移 issue。
