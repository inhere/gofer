# Windows 原生受管服务（内部能力）

受管 Supervisor 在验证登记身份后会脱离自己的控制台，持续运行时不保留 cmd 窗口。运行日志通过 `gofer serve logs` 查看；此行为只作用于受管 Supervisor，普通交互 CLI 仍使用调用者终端。

Gofer 当前已有 Task Scheduler 原生管理后端与隐藏的 `gofer serve supervise --spec <绝对路径>` 内部入口。该入口由 Gofer 创建的登录计划任务调用；它不是日常手动管理命令。面向用户的 `serve register/start/stop/restart/uninstall/status/logs` 公共 CLI 会在后续集成任务提供，在 CLI 完成前不要把这些提议命令当作可用命令。

受管描述保存在有效 `config-dir/run/service/<name>.json`，包含实例名称、当前用户 SID、执行程序、工作目录、配置文件与运行目录等非敏感字段，不保存 token。显式 `-c` 的 server PID/日志目录是配置文件所在目录的 `run/`；登记后冻结这个路径。注册会复制一份 Gofer 程序作为 `config-dir/run/service/<name>-supervisor.exe`，计划任务直接运行此副本，无需仓库中的 PowerShell 脚本。

计划任务使用当前用户的 `InteractiveToken` 登录触发，默认 `LeastPrivilege`，只有显式 elevated 且当前进程已提权时才选择 `HighestAvailable`。注册默认不启动；已存在的同名未知任务会被拒绝。停止先记录停止意图，再向身份核对通过的 server 请求优雅退出，并等待 server 和 supervisor 结束；显式启动会清除停止意图。supervisor 在快速启动失败达到有限次数后退出，留下日志供排查。状态必须核对所登记任务与 PID 的执行程序、用户和创建身份；其他进程的 HTTP `/health` 成功不能证明这个实例健康。

旧 `scripts/start.ps1` 和 `scripts/win-supervisor.ps1` 仍保留给现有实例；在新 CLI 和正式切换验收前，不要移除它们。真正接管现有任务、证书或运行目录属于后续具名迁移操作。
