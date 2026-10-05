# L 批：遗留项收尾计划（2026-10-05）

分支 l-batch，不 push、不合并 main。按功能点各自提交。

- L1 SSE 去掉 DB 轮询：job.Service 增 `WatchJob(id)` 变更信号（persist / recordEvent 触发，容量 1 合并突发）；`streaming.StreamJob` 改收 `StreamSource` 接口，status/event/interaction 仅在信号到来（及连接建立回放）时读取，日志仍按 250ms 文件 tail，另加 5s 仅内存的安全网。wire 格式不变。测试：计数 fake 证明空闲零读取、信号后即时 end 帧、安全网。
- L2 init hooks 全局安装项目级重复提示去重：比较前 Clean + 统一分隔符，Windows 大小写不敏感。
- L3 codex-acp 改用 `@agentclientprotocol/codex-acp`（npx），同步 agent 测试与 skill；主机临时 serve 实测 codex 族互转，能通则并入 builtinSessionFamilies，证据写 docs/runbook/session-relay.md。
- L4 omp 扩展 cancelWaiter：先自发 `Interrupt` 再 kill；确认 hookrelay 对 omp Interrupt 关闭 OPEN turn（补测试）；TUI 续跑验证尽力，做不到写明。
- L5 jcode turn_* 事件：pty 驱动 jcode TUI 验证，做不到写明。
- 收尾：质量门（gofmt/build/windows build/vet/test）、G045 同步 skill 与 README。
