# Runbook — Windows gofer server 监督运行 + 自更新

> 配套脚本：`scripts/win-supervisor.ps1`（监督循环）/ `scripts/win-selfupdate.ps1`（自更新）/ `scripts/win-selftest.ps1`（验收）。
> 设计见 `docs/plans/2026-07-09-windows-server-selfupdate-plan.md`（v0.3）。所有路径用 `<占位符>`，按实际部署替换。

## 0. 名词 / 前提

- `<ExeDir>`：**运行中** `gofer.exe` 所在目录（监督循环启动的就是它；rename-replace 的目标）。
- `<RepoDir>`：源码仓目录，`git pull` + `go build` 在此进行。**常与 `<ExeDir>` 不同**（如 `go install` 装到 GOPATH/bin，源码在别处）。
- `<WorkDir>`：server 启动的工作目录；**相对** serve 参数（如 `--web-dir ./web/dist`）按它解析，须与原启动一致。
- `<ServeArgs>`：原来 `gofer serve` 的完整参数（如 `s --web-dir ./web/dist`）。
- 前提：主机装有 `pwsh`(7+)、`go`、`git`；`<ExeDir>` 可写（rename-replace 需要）。

## 1. 起服务：用监督循环替代裸跑 `gofer serve`

在原来手动跑 `gofer serve` 的终端，改为**直接**启动监督脚本（`pwsh -File` 直启，使其命令行含 `win-supervisor` —— 自更新的 F4 守卫据此识别祖父进程）：

```powershell
pwsh -NoProfile -File scripts/win-supervisor.ps1 `
  -ExeDir '<ExeDir>' `
  -ServeArgs @('serve','--addr','0.0.0.0:8765','--web-dir','./web/dist') `
  -WorkDir '<WorkDir>'
```

- 该进程是 gofer 的**父**、处于其进程树之外：更新时杀掉 gofer 换二进制，监督循环仍在、会重启新 exe。
- 附带**崩溃自愈**（gofer 意外退出 2s 后自动重启）。
- 看门狗：新 exe 连续快速失败（启动即退）达 `-FastFailThreshold`(默认3) 次 → 自动回滚 `gofer.old.exe`。
- 日志默认 `<ExeDir>\win-supervisor.log`。

> ⚠️ 不要用包装脚本/别名间接启动监督器，否则其命令行不含 `win-supervisor`，F4 守卫会拒绝自更新（除非用 `-SupervisorMarker` 显式指定标识）。

## 2. 触发一次自更新

自更新脚本**必须作为 gofer 的直接子进程运行** —— 通过 `agent=exec` 的 job（不要用 codex / shell 包装，否则父进程不是 gofer，F2 守卫拒绝）：

```powershell
gofer job run -a exec --runner local -- `
  pwsh -NoProfile -File scripts/win-selfupdate.ps1 `
    -RepoDir '<RepoDir>' -ExeDir '<ExeDir>'
```

流程：
1. **阶段1（安全）**：`<RepoDir>` 内 `git pull` + `go build -o gofer-new.exe ./cmd/gofer` + `-V` 校验。任一失败即中止，**不碰运行中的 exe**。
2. **阶段2（切换）**：`<ExeDir>` 内 `gofer.exe → gofer.old.exe`（运行中可 rename）、`gofer-new.exe → gofer.exe`，然后**按本 job 父 pid 精确杀 gofer**；监督循环约 2s 后拉起新 exe。

> 触发 job 在 gofer 被杀后其输出转发即断，网关侧多半显示为**中断/失败——属正常现象**，不代表更新失败。以下用带外方式确认。

## 3. 确认更新结果（带外）

```powershell
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8765/health   # 200 = 已重启就绪
& '<ExeDir>\gofer.exe' -V                                         # 版本已更新
Get-Content '<ExeDir>\win-supervisor.log' -Tail 20               # 看重启/回滚记录
Test-Path '<ExeDir>\gofer.old.exe'                               # 旧版留底(回滚点)
```

## 4. 回滚

- **自动**：坏 exe 连续快速失败达阈值，监督器自动 `gofer.old.exe → gofer.exe`。
- **手动**：
  ```powershell
  Copy-Item '<ExeDir>\gofer.old.exe' '<ExeDir>\gofer.exe' -Force
  Stop-Process -Id (Get-NetTCPConnection -LocalPort 8765 -State Listen).OwningProcess -Force  # 监督器重启旧版
  ```

## 5. 验收自测（隔离，不碰 live server）

在独立端口 + 独立配置目录起第二实例，验证 supervisor+selfupdate 全流程，自清理：

```powershell
pwsh -NoProfile -File scripts/win-selftest.ps1        # 期望 RESULT: pass=13 fail=0
```

覆盖：崩溃自愈 / rename-replace 换新 exe / 看门狗回滚 / F2 守卫拒绝非 gofer 父进程。

## 6. 排障

| 现象 | 原因 / 处理 |
|---|---|
| `guard(F2): parent is '...'` | 自更新没作为 gofer 直接子进程跑 → 用 `-a exec`，勿经 codex/shell |
| `guard(F4): ... not the supervisor` | server 是裸跑（无监督器）→ 先按 §1 用监督循环起，或用 `-SupervisorMarker` 对齐 |
| 重启后 `--web-dir` 等相对参数失效 | `-WorkDir` 未对齐原启动 cwd → 传正确 `<WorkDir>` 或改用绝对路径参数 |
| 监督器不断重启坏 exe | 达阈值会自动回滚；若无 `gofer.old.exe`，手动放回一份可用 exe |
| 主机同时跑 gofer worker | 自更新按**父 pid**精确杀，不按进程名，不会误伤 worker |

## 7. 常驻 / 开机自启：nssm 服务（推荐，替代 §1 的 pwsh 循环）

§1 的 pwsh 监督循环跑在登录用户终端里，**关终端 / 登出不自启**。生产/常驻用 **nssm** 把 gofer 跑成 Windows 服务——nssm 本身即进程外监督者（`AppExit=Restart` = 崩溃/被杀自愈 = 自更新需要的重启器），**替代**本 pwsh 循环，rename-replace 更新逻辑不变。配套脚本 `scripts/start.ps1`（已封装 install/up/stop/restart/remove/status/logs）。

### 7.1 准备

- 下载 nssm（`win64/nssm.exe`，https://nssm.cc）放 `<repo>\serve-run\`（与 gofer.exe 同目录）；二者均被 `.gitignore`（`*.exe` + `/serve-run/`）。
- gofer.exe 构建到 `<repo>\serve-run\gofer.exe`（`go build -o serve-run\gofer.exe .\cmd\gofer`）。

### 7.2 装并启动（**管理员** PowerShell）

```powershell
pwsh -File scripts\start.ps1 `
  -ConfigDir 'D:/work/inhere/config/win-env/gofer' `
  -Account '.\<user>'          # 强烈建议：以你的账号跑（见下）
```

- **`-ConfigDir` 必给**（或设 `$env:GOFER_CONFIG_DIR`）：服务**看不到你 shell 的 env**，靠它注入 `GOFER_CONFIG_DIR`，让 gofer 找到 `<dir>\config.yaml` + 加载该目录 `.env`（`GOFER_TOKEN`）。缺了 → gofer 报 `refusing to start without a token`。
- **`-Account '.\<user>'` 强烈建议**：默认 **LocalSystem** → `--runner local` 的 job 以 **SYSTEM** 跑，破坏 git 属主 / 你的凭据 / 用户 PATH。传 `-Account` 让服务以**你**运行（安全提示密码，走 nssm `ObjectName`）→ local job 恢复成你（`whoami`=你的账号）。`-Account` 只需设一次；后续 `up` 不带它会保留。
- `-Auto` 开机自启；端口默认走 `config.server.addr`（`-Addr` 可覆盖）。
- 验证：`-Action status`（Running）/ `-Action logs`（tail stdout/stderr）。

### 7.3 常见坑（本次落地实测）

| 现象 | 处理 |
|---|---|
| `Unexpected status SERVICE_PAUSED in response to START` | gofer 启动即退 → nssm 节流。看 `-Action logs`，多半缺 config/token → 补 `-ConfigDir` |
| `refusing to start without a token` | 服务没找到配置 → `-ConfigDir` 指到含 `config.yaml`+`.env` 的目录 |
| local job 里 git `dubious ownership` / push 无凭据 | 服务在以 SYSTEM 跑 → `-Account` 切成你的账号 |
| 服务起不来 **错误 1069**（登录失败） | `secpol.msc` → 用户权限分配 → 「作为服务登录」加上该账号，再 `-Action restart` |
| SSH+ssh-agent 的 push 在服务内失败 | agent 只在交互会话 → 该类 push 仍在你自己终端做（HTTPS+凭据管理器则服务内可用） |

### 7.4 nssm 下的自更新（**与 §2 的差异**）

换 nssm 后 **gofer 的父进程 = `nssm.exe`**（不再是 `win-supervisor.ps1`），故 §2 自更新必须带 `-SupervisorMarker 'nssm'`（否则 F4 守卫拒）：

```powershell
gofer job run -a exec --runner local -- `
  pwsh -NoProfile -File scripts\win-selfupdate.ps1 `
    -RepoDir '<RepoDir>' -ExeDir '<repo>\serve-run' -SupervisorMarker 'nssm'
```

rename-replace 逻辑不变：换二进制 → 按 pid 杀 gofer → nssm `AppExit=Restart` ~2s 拉起新 exe。

> ⚠️ **nssm 无 §4 的 fast-fail 自动回滚看门狗**（那是 win-supervisor.ps1 的逻辑）：若新 exe 能 build+`-V` 通过但**运行时**启动即崩，nssm 只会节流重启、**不会自动回滚**。stage-1（build+`-V`）已挡住构建失败；运行时失败需**手动回滚**：`Copy-Item serve-run\gofer.old.exe serve-run\gofer.exe -Force; serve-run\nssm.exe restart gofer`。
