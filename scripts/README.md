

## 使用 win-supervisor.ps1

```bash
pwsh -NoProfile -File scripts/win-supervisor.ps1 `
  -ExeDir '<ExeDir>' `
  -ServeArgs @('serve','--addr','0.0.0.0:8765','--web-dir','./web/dist') `
  -WorkDir '<WorkDir>'
```

触发一次自更新

自更新脚本必须作为 gofer 的直接子进程跑 —— 用 agent=exec 的 job(经 codex/shell 包装会因父进程不是 gofer 被 F2 守卫拒):

```bash
gofer job run -a exec --runner local -- `
  pwsh -NoProfile -File scripts/win-selfupdate.ps1 `
    -RepoDir '<RepoDir>' -ExeDir '<ExeDir>'
```

## 常驻:`start.ps1`(登录计划任务,桌面会话)

常驻实例注册成**登录计划任务**(默认名 `gofer-serve`,动作 = `conhost --headless pwsh -File scripts\win-supervisor.ps1`),跑在你的**交互会话**里 —— 所以 `--runner local` 的 job 能操作桌面(DTools/CODESYS 自动化、截图)。Windows 服务(nssm/sc)一律在 **session 0**,做不到这点,已废弃(设计 `docs/design/2026-09-22-windows-desktop-session-service-design.md`)。**普通窗口即可**(仅 `-Elevated` 需管理员)。

```powershell
pwsh -File scripts\start.ps1 -Action up      -ConfigDir '<ConfigDir>'        # 注册/更新任务 -> 启动 -> 等 /health
pwsh -File scripts\start.ps1 -Action status  -ConfigDir '<ConfigDir>'        # 任务状态/上次结果/Action 命令行/gofer pid+SessionId/健康
pwsh -File scripts\start.ps1 -Action logs    -ConfigDir '<ConfigDir>'        # tail win-supervisor.log + <ConfigDir>\run\serve.log
pwsh -File scripts\start.ps1 -Action restart -ConfigDir '<ConfigDir>'        # 优雅停 -> 起
pwsh -File scripts\start.ps1 -Action upgrade -ConfigDir '<ConfigDir>'        # 原地升级: 服务不停的 make build -> stop -> 换 exe -> start; 旧 exe 留 gofer.exe.prev
pwsh -File scripts\start.ps1 -Action upgrade -ConfigDir '<ConfigDir>' -Web   # 同上, 但先 make web 重打 web 控制台
pwsh -File scripts\start.ps1 -Action stop    -ConfigDir '<ConfigDir>'        # 落 gofer.stop 标记 + gofer serve stop(看门狗不再拉起)
pwsh -File scripts\start.ps1 -Action remove  -ConfigDir '<ConfigDir>'        # 停 + 注销任务(exe/日志保留)
```

关键点:

- `-ConfigDir` 必给(或 `$env:GOFER_CONFIG_DIR`):任务继承的是**用户注册表**环境,拿不到你 shell 的 env,靠任务动作的 `-EnvExtra GOFER_CONFIG_DIR=…` 注入。token 放 `<ConfigDir>\.env`(`GOFER_TOKEN=…`)或配置的 `token_env`;任务定义里**不写** token。
- 端口/前端:`-Addr 0.0.0.0:8765` 覆盖 config 的 `server.addr`(也是健康探测地址);`-NoWeb` 用 `--no-web`,否则默认 `--web-dir ./web/dist`(相对 `<repo>`,即任务动作的 WorkingDirectory)。
- 路径从脚本位置推导:`ExeDir=<repo>\serve-run`(运行中的 exe + `gofer.stop` + `win-supervisor.log`)、`dist\gofer.exe`(`upgrade` 的构建产物)。
- 停机 = **标记 + 优雅停**:`gofer.stop` 让看门狗退出不再拉起,`gofer serve stop` 走 pidfile + 命名事件(不硬杀)。
- 机器上还有名为 `gofer` 的服务时 `up` 会拒绝(两者抢端口)并打印迁移命令;隔离实例可加 `-AllowServiceConflict` 跳过。
- 自更新链不变:gofer 的父进程仍是 `win-supervisor.ps1`,F4 守卫用默认 marker(无需 `-SupervisorMarker`)。

### 隔离验收

```powershell
pwsh -NoProfile -File scripts\win-selftest.ps1    # 监督 + 自更新(独立 config dir + 端口 9099)
pwsh -NoProfile -File scripts\win-tasktest.ps1    # 任务模式(随机任务名 + 端口 9098/9097)
```
## TCP 隧道冒烟

隔离 serve、worker 与 echo 的 11 项回归检查：[`smoke/tunnel/run-smoke.sh`](smoke/tunnel/run-smoke.sh)。
