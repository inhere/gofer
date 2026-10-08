# scripts

Windows 的 `start.ps1`、`win-supervisor.ps1`、`win-selfupdate.ps1` 等旧脚本已删除，已由原生受管服务取代。server 的注册、启动、停止、升级见 [受管服务 runbook](../docs/runbook/2026-10-08-serve-management-runbook.md)（`gofer serve register/start/stop/restart/status/logs/upgrade`）。

本目录现有内容：

- `smoke/`：各子系统的冒烟脚本。
- `tracker-transfer/`：tracker 数据迁移小工具。
