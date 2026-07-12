

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

## 使用 nssm

### 用法(在项目目录、管理员 PowerShell 里)

```bash
# 装并启动服务(默认 manual 启动，端口走 config 的 server.addr)
pwsh -File scripts\start.ps1

# 常用变体
pwsh -File scripts\start.ps1 -Auto                 # 开机自启
pwsh -File scripts\start.ps1 -Addr 0.0.0.0:LIVE-PORT    # 显式指定端口(覆盖 config)
pwsh -File scripts\start.ps1 -Config .\.gofer.yaml # 指定配置
pwsh -File scripts\start.ps1 -Action status        # 看状态/生效参数
pwsh -File scripts\start.ps1 -Action logs          # tail stdout/stderr
pwsh -File scripts\start.ps1 -Action restart       # 重启
pwsh -File scripts\start.ps1 -Action remove        # 停 + 卸载服务(exe/日志保留)
```

关键点:

- 路径全从脚本位置推导:ExeDir=<repo>\serve-run、nssm=serve-run\nssm.exe、AppDirectory=<repo>(所以 --web-dir ./web/dist 用相对路径,规避 PowerShell→nssm 引号地狱,repo 路径含空格也安全)
- 日志 → serve-run\gofer.out.log / gofer.err.log
- AppExit=Restart + 2s 延迟(崩溃/被杀后自动拉起,正是自更新需要的重启器)
- token:-Token 或 $env:GOFER_TOKEN → 注入服务环境(注:存进服务注册表,管理员可读)
- 服务操作需管理员;脚本会检测并给明确提示

⚠️ 自更新在 nssm 下的改动(重要)

换 nssm 后 gofer 的父进程变成 nssm.exe(不再是 win-supervisor.ps1),所以自更新的 F4 守卫要用 -SupervisorMarker 'nssm',否则会被拒:

```bssh
gofer job run -a exec --runner local -- `
  pwsh -NoProfile -File scripts\win-selfupdate.ps1 `
    -RepoDir '<RepoDir>' -ExeDir '<repo>\serve-run' -SupervisorMarker 'nssm'
```

rename-replace 逻辑不变:换二进制文件 → 按 pid 杀 gofer → nssm 的 AppExit=Restart ~2s 拉起新 exe。(这条我已写进 start.ps1 的头部注释。)
