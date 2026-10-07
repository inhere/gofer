# Linux systemd 管理后端

Gofer 的 Linux 平台后端支持 `systemd-system` 和 `systemd-user` 两种登记范围。system scope 必须明确指定 `run_as`，并要求该账号的 UID 与登记的进程 owner UID 一致；user scope 使用当前账号的 user manager。两种范围都不自动执行 sudo、启用 linger 或提权。

登记会校验可执行文件、工作目录和配置文件，并在登记的 config-dir 下保存本地描述。unit 只包含允许的 `serve -c <config-file>` 启动参数和 `GOFER_CONFIG_DIR`，不包含 token。登记会 `daemon-reload`、`enable`，默认不启动；启动、停止、重启和卸载均通过 `systemctl` 参数数组调用。显式停止由 systemd 完成，因此 `Restart=on-failure` 不会将已停服务再次拉起。unit 在 60 秒窗口内最多启动五次，停止宽限为 180 秒。卸载会停止、disable 并移除受管 unit 和服务描述，保留用户程序、配置、证书、数据库与日志。

后端读取 status 时核对磁盘 unit 与登记描述、systemd 实际加载的 FragmentPath、MainPID 的可执行文件、UID 和完整启动参数，并用 InvocationID 与进程启动标识记录自主重启后的新身份。应用日志路径按配置中的 `log.file`、`log.dir` 和冻结的 config-dir 解析；原生 journal 可通过后端日志接口读取。受管入口始终传 `-c`，所以 pid 和默认日志的 runtime-dir 固定为配置文件所在目录的 `run/`。

当前平台 API 位于 `internal/servicemgr/platform_linux.go`：`SystemdRegister`、`SystemdStart`、`SystemdStop`、`SystemdRestart`、`SystemdStatusOf`、`SystemdJournal`、`SystemdUninstall`。统一 CLI 入口由后续集成任务绑定；未绑定前本页仅记录已实现的 Linux 平台行为。

原生隔离验证用 `GOFER_SYSTEMD_TEST_EXE=<绝对路径>` 启用 `TestSystemdNativeIntegration`。测试只创建随机命名的 unit，独立配置目录和端口；执行后卸载 unit，保留测试结果。测试环境需要可用的 systemd system/user manager，system scope 需要已有相应写权限；权限不足时直接返回错误。
