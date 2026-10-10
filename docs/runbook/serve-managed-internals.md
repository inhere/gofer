# 受管 server 的平台后端与升级执行链（内部说明）

> 面向 gofer 仓库维护者，记录 `gofer serve register/start/stop/restart/status/logs/uninstall/upgrade` 背后的平台实现边界。日常操作步骤见 [受管 server 操作 runbook](2026-10-08-serve-management-runbook.md)，命令参数以 `gofer serve --help` 为准。

## Linux：systemd 后端

支持 `systemd-system` 和 `systemd-user` 两种登记范围。system scope 必须明确指定 `run_as`，并要求该账号的 UID 与登记的进程 owner UID 一致；user scope 使用当前账号的 user manager。两种范围都不自动执行 sudo、启用 linger 或提权。

- 登记会校验可执行文件、工作目录和配置文件，并在登记的 config-dir 下保存本地描述。unit 只包含允许的 `serve -c <config-file>` 启动参数和 `GOFER_CONFIG_DIR`，不包含 token。
- 登记会 `daemon-reload`、`enable`，默认不启动；启动、停止、重启和卸载均通过 `systemctl` 参数数组调用。显式停止由 systemd 完成，因此 `Restart=on-failure` 不会把已停服务再次拉起。unit 在 60 秒窗口内最多启动五次，停止宽限为 180 秒。
- 卸载会停止、disable 并移除受管 unit 和服务描述，保留用户程序、配置、证书、数据库与日志。
- status 核对磁盘 unit 与登记描述、systemd 实际加载的 FragmentPath、MainPID 的可执行文件、UID 和完整启动参数，并用 InvocationID 与进程启动标识记录自主重启后的新身份。
- 应用日志路径按配置中的 `log.file`、`log.dir` 和冻结的 config-dir 解析；原生 journal 用 `gofer serve logs --journal` 读取。受管入口始终传 `-c`，所以 pid 和默认日志的 runtime-dir 固定为配置文件所在目录的 `run/`。
- 平台 API 位于 `internal/servicemgr/platform_linux.go`（`SystemdRegister` / `SystemdStart` / `SystemdStop` / `SystemdRestart` / `SystemdStatusOf` / `SystemdJournal` / `SystemdUninstall`）。
- 原生隔离验证：设置 `GOFER_SYSTEMD_TEST_EXE=<绝对路径>` 启用 `TestSystemdNativeIntegration`。测试只创建随机命名的 unit，使用独立配置目录和端口，执行后卸载 unit。需要可用的 systemd system / user manager，system scope 需要相应写权限；权限不足时直接返回错误。

## Windows：Task Scheduler 后端

- 受管 Supervisor 在验证登记身份后会脱离自己的控制台，持续运行时不保留 cmd 窗口；运行日志用 `gofer serve logs` 查看。此行为只作用于受管 Supervisor，普通交互 CLI 仍使用调用者终端。
- 隐藏的 `gofer serve supervise --spec <绝对路径>` 是内部入口，由 gofer 创建的登录计划任务调用，不是日常手动管理命令；日常使用 `gofer serve register/start/stop/restart/uninstall/status/logs`。
- 受管描述保存在 `<config-dir>/run/service/<name>.json`，包含实例名称、当前用户 SID、执行程序、工作目录、配置文件与运行目录等非敏感字段，不保存 token。显式 `-c` 的 server PID / 日志目录是配置文件所在目录的 `run/`，登记后冻结。注册会复制一份 gofer 程序作为 `<config-dir>/run/service/<name>-supervisor.exe`，计划任务直接运行此副本，不依赖仓库里的脚本。
- 计划任务使用当前用户的 `InteractiveToken` 登录触发，默认 `LeastPrivilege`，只有显式 elevated 且当前进程已提权时才选 `HighestAvailable`。注册默认不启动；已存在的同名未知任务会被拒绝。
- 停止先记录停止意图，再向身份核对通过的 server 请求优雅退出，并等待 server 和 supervisor 结束；显式启动会清除停止意图。supervisor 在快速启动失败达到有限次数后退出，留下日志供排查。
- 状态必须核对所登记任务与 PID 的执行程序、用户和创建身份；其他进程的 HTTP `/health` 成功不能证明这个实例健康。
- 旧的 `scripts/start.ps1`、`win-supervisor.ps1`、`win-selfupdate.ps1` 已被原生受管服务取代；若遇到仍指向旧脚本的登录任务，属于旧拓扑，按[旧入口 runbook](2026-07-11-windows-server-selfupdate-runbook.md)或具名迁移操作处理。

## 升级执行链

`gofer serve upgrade` 与 `gofer serve upgrade status` 是用户入口；`serve upgrade-helper --receipt <绝对路径>` 是内部隐藏入口，由 Windows 严格脱离调用方 Job Object 后启动，或由 Linux 独立的 systemd transient unit 启动，用户不应直接运行。

- 内部 `servicemgr.BeginUpgrade` 先核对受管实例身份、预构建程序的平台、SHA-256、大小和 `--version`，再把候选复制到目标程序所在卷，并在 `<config-dir>/run/upgrade/` 写入持久结果。发起端可指定当前管理 CLI 的程序映像作为 helper 来源；该映像也要通过 build info 和 `--version` 校验。
- 独立 helper 记录自身 PID 与创建身份后，等待运行中 server 验证来源 job、关闭新工作准入、完成有界 drain 并写入持久许可。许可前不停止旧服务。
- `SourceJobID` 是来自发起命令环境的待核验声明。只有本机 direct exec job 的运行记录、进程 PID / 创建身份和持久 job 记录一致时，server 才能在 drain 中排除该来源 job；其他来源无法靠环境变量自行获得豁免。发起 job 应在 `WaitUpgradeAccepted` 得到升级 ID 后退出；helper 会等它的进程退出再停旧 server。job 的结束、断连或 orphaned 状态都不是升级终态，最终结果以持久 receipt 为准。
- 切换后 helper 核对原生任务或 unit、受管进程身份、目标监听端口的 OS owner 及 `/health`。候选失败时会尝试恢复旧程序、登记版本与 Windows supervisor 副本，再核对旧服务健康；若停机失败或回滚失败，保留文件与错误证据并记为 `failed`。升级不回滚数据库。普通 daemon 与 `worker upgrade` 的使用方式不受此链路影响。
