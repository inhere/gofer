# 受管 server 升级的内部执行链

Gofer 的受管服务已实现独立升级执行者。`serve upgrade-helper --receipt <绝对路径>` 是内部隐藏入口，由 Windows 严格脱离调用方 Job Object 后启动，或由 Linux 独立的 systemd transient unit 启动；用户不应直接运行。面向用户的 `serve upgrade` 和结果查询 CLI 由后续命令集成提供，在它们完成前不应作为可用命令使用。

内部 `servicemgr.BeginUpgrade` 先核对受管实例身份、预构建程序的平台、SHA-256、大小和 `--version`，再把候选复制到目标程序所在卷，并在 `config-dir/run/upgrade/` 写入持久结果。发起端可指定当前管理 CLI 的程序映像作为 helper 来源；该映像也要通过 Gofer build info 和 `--version` 校验。独立 helper 记录自身 PID 与创建身份后，等待运行中 server 验证来源 job、关闭新工作准入、完成有界 drain 并写入持久许可。许可前不停止旧服务。

`SourceJobID` 是来自发起命令环境的待核验声明。只有本机 direct exec job 的运行记录、进程 PID/创建身份和持久 job 记录一致时，server 才能在 drain 中排除该来源 job；其他来源无法靠环境变量自行获得豁免。发起 job 应在 `WaitUpgradeAccepted` 得到升级 ID 后退出；helper 会等它的进程退出再停旧 server。job 的结束、断连或 orphaned 状态都不是升级终态，最终结果以持久 receipt 为准。

切换后 helper 核对原生任务或 unit、受管进程身份、目标监听端口的 OS owner 及 `/health`。候选失败时会尝试恢复旧程序、登记版本与 Windows supervisor 副本，再核对旧服务健康；若停机失败或回滚失败，保留文件与错误证据并记为 `failed`。升级不回滚数据库。普通 daemon 与现有 worker upgrade 的使用方式不受此内部链路改变。
