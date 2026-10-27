<!-- template_id: runbook; template_version: 1.0.0 -->
# Gofer 原生受管 server 操作 Runbook

## 目的、范围与所有权

- 所有者：Gofer 服务管理维护者。
- 最后验证：2026-10-08；Windows Task Scheduler 与 Linux systemd 的原生后端和独立升级链已在隔离实例验证。公开管理 CLI 的端到端验收及正式实例切换另在后续阶段完成。
- 范围：管理本机 Gofer server 的登记、启动、状态、日志、预构建程序升级与故障恢复。默认一台机器一个 `serve`；具名实例用于隔离验收和迁移暂态。
- 非目标：构建源码、批量升级 worker、自动移动数据库或 tracker、发布版本、接管未经核对的任务或 unit。

## 前置条件与授权 Gate

准备一份已存在且通过 `gofer config validate -c '<config-file>'` 的配置、可执行的 `<binary>`、独立的 `<config-dir>` 与端口。Windows 以将运行任务的当前交互用户登记；需要最高权限时由已提权的终端显式指定 `--elevated`。Linux `system` scope 需有 unit 管理权限并显式给 `--run-as <user>`；`user` scope 使用当前用户管理器，不自动启用 linger 或提权。

以下流程是操作说明，不授权修改现有服务。隔离测试使用随机 `--name`、独立配置目录、端口、证书和数据库。接管现有任务、切证书或搬仓库前，先取得覆盖具名目标的操作批准并保留回滚点。已有脚本管理的登录任务仍按[旧入口 runbook](2026-07-11-windows-server-selfupdate-runbook.md)操作，直到该任务完成切换验证。

## 安全与 secret 处理

配置中的 token 从 `<config-dir>/.env` 或 `token_env` 读取；不把 token 放入命令参数、计划任务 XML、systemd unit、受管 spec 或日志。证书迁移要复制原 CA、server certificate 和私钥，核对证书/私钥配对并保持目录权限；不要重新生成 CA 破坏客户端现有信任。`{config_dir}` 仅用于受支持的本机路径字段，`-c` 只选择配置文件。CLI 的 `--exe`、`--work-dir` 和 `--config` 均在注册时冻结为绝对路径。

遇到同名未知系统入口、配置/程序身份不符、权限不足、独立升级执行者未接管、drain 超时或停止失败时停止操作。不要根据一个不属于本实例的 `/health 200` 判定启动成功，也不要仅凭 PID 强杀未知进程。

## token 的来源（登录对不上时先看这一节）

server 的鉴权 token 只有两种来源，`--token` 是命令行临时覆盖、不会被服务继承：

- `server.token`：config.yaml 里的字面量。
- `server.token_env` 指名的环境变量（`gofer init` 与 `config/gofer.example.yaml` 写的是 `GOFER_TOKEN`；**不写 `token_env` 时 server 不读 `GOFER_TOKEN`**，没有内置缺省）。

环境变量的值按 dotenv 规则确定：`<config-dir>/.env` 与工作目录的 `./.env` 依次加载（后加载覆盖前者），**启动终端显式 export 的同名变量覆盖两者**。

前台运行与服务方式运行看到的不是同一个环境：

- 前台 `gofer serve` 在注册用的终端里跑，继承该终端的全部环境变量，包括那次 `export GOFER_TOKEN=...`。
- 注册成系统服务后，unit / 计划任务只注入 `GOFER_CONFIG_DIR`（Linux 还有 `GOFER_MANAGED_SPEC`），**不继承注册终端的环境**；服务只读 `<config-dir>/.env` 与 config.yaml。
- **Windows 还有一层：启动账户的持久环境变量**。任务计划程序拉起的服务继承该账户的用户级（`HKCU\Environment`）与机器级（`HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment`）环境变量，而 gofer 让「已存在的环境变量」优先于 `.env` 同名值——用户级持久环境里留着一个旧的 `GOFER_TOKEN` 就压过 `.env`，**改 `.env` 没有任何效果**（现场确认的就是这种情况：两边长度不同）。Linux / macOS 没有这一层（systemd unit / launchd 只给显式声明的环境）。

症状：注册前用终端里的 token 登录 web 正常，`serve start` 之后同一个 token 401（日志里只有 401，没有解释）。

排查：

1. `gofer serve status --json` 的 `auth_token`：`service`（服务会用的来源与掩码）、`current_process`（当前终端会用的）、`match`。`kind` 为 `config` / `env-file` / `process-env` / `persistent-env` / `flag` / `unset`；`masked` 是 token 的展示形式 `len=<长度> <前两位>**<末位>`（长度小于 8 只给 `len=<长度>`）；**任何输出都不含 token 值**。另外三条差异来源也在里面：`persistent_env`（Windows 持久环境里压过 `.env` 的那个变量：`scope` / `name` / `masked` / `shadowed`）、`unread_dotenv`（本次 CLI 加载了、服务不会读的 `.env`）、顶层的 `config_files` 与 `dotenv_files`（本次读到的 config 文件与 `.env` 文件）。
2. `gofer serve logs --name '<name>'` 或 `run/serve.log` 里的 `server.token_source` 行：`kind` / `env_var` / `env_file` / `scope` / `masked`，可与上面对照。
3. `gofer serve register` 在两边不一致（含服务侧没有 token）时会打印警告，指出两边来源与修法；Windows 持久环境变量压过 `.env` 时单独给一段警告（两边掩码 + 删除 / 改值命令）。它不阻止注册。
4. 版本查询 401（`version_error: stats returned HTTP 401`）时，`serve status` 追加一行提示：CLI 用 `GOFER_SERVER_TOKEN`（或 `server.token_env` / `server.token`）访问 server，与服务端认可的 token 不一致时给出两边掩码（`--json` 的 `version_auth_mismatch`）。

修法（让两边一致）：把要用的 token 写进 **配置目录的 `.env`**，例如 `<config-dir>/.env` 里 `GOFER_TOKEN=<token>`，然后 `gofer serve restart`；或登录 web / CLI 时改用服务那份。服务侧的 token 即使写在 config.yaml 的 `server.token` 也一致，但 dotenv 更便于轮换。不要写进 unit 文件、计划任务 XML 或注册命令的参数——受管 spec 只持久化非密参数。

Windows 持久环境变量（只在它确实存在时才要处理；`serve status` / `serve register` 已经指出 `scope` 与变量名）：

```powershell
# 看它现在是什么（只在本地确认用，别贴进聊天 / issue）
[Environment]::GetEnvironmentVariable('GOFER_TOKEN','User')
# 删掉（推荐：让 .env 成为唯一来源）。机器级把 'User' 换成 'Machine'，需要管理员
[Environment]::SetEnvironmentVariable('GOFER_TOKEN',$null,'User')
# 或改成要用的那份；改完要新起的进程才看到，所以随后重启服务
[Environment]::SetEnvironmentVariable('GOFER_TOKEN','<token>','User')
gofer serve restart --name '<name>' -c '<config-file>'
```

## 操作步骤

1. **只读核对目标。** 先运行 `gofer serve status --name '<name>' --json`，核对 `registered`、原生入口、server/supervisor PID、配置文件、程序路径、版本和 `port_owned`。默认名为 `gofer-serve`。确认 `<config-dir>` 与所选 `-c '<config-file>'`；显式 `-c` 时 PID/运行日志在该配置文件同目录的 `run/`。
   预期：目标未登记，或输出的系统入口与计划接管的实例一致。停止条件：同名入口属于其他程序、状态无法核验或仍有未结束的任务。若已登记，顺带看 `auth_token`：`service` 与 `current_process` 的 `masked` 不一致说明登录要用的 token 与服务那份不同（见上文「token 的来源」）。

2. **登记，必要时启动。** Windows 在目标用户的交互终端运行：

   ```powershell
   $env:GOFER_CONFIG_DIR = '<config-dir>'
   gofer serve register --name '<name>' --exe '<binary>' --work-dir '<work-dir>' -c '<config-file>'
   gofer serve start --name '<name>' -c '<config-file>'
   ```

   Linux system scope 在当前有 unit 管理权限的 shell 中运行：

   ```bash
   export GOFER_CONFIG_DIR='<config-dir>'
   gofer serve register --scope system --run-as '<user>' --name '<name>' --exe '<binary>' --work-dir '<work-dir>' -c '<config-file>'
   gofer serve start --name '<name>' -c '<config-file>'
   ```

   当前用户的 user scope 将 `--scope system --run-as '<user>'` 改为 `--scope user`。`register` 默认只登记，需一次完成时可加 `--start`。Windows 的 `--adopt` 仅用于已核对的旧 Gofer 脚本任务，不能覆盖任意同名任务。
   `register` 会按「当前终端看到的 token」与「服务将看到的 token」比对：不一致（含服务侧没有 token）时打印警告并给出修法，但**不阻止注册**——处理方式见上文「token 的来源」。
   预期：系统入口已登记；显式 start 后实例状态为 active。停止条件：权限、TLS 文件、配置或所有权预检失败；运行中的实例若登记内容变化，先停旧实例再更新。

3. **核对状态与日志。** 运行 `gofer serve status --name '<name>' --json` 和 `gofer serve logs --name '<name>' --lines 100`。Linux 的 unit 启动故障可另用 `gofer serve logs --name '<name>' --journal --lines 100`；Windows 默认读取应用日志，supervisor sidecar 日志保留在 `<config-dir>/run/service/`。
   预期：`registered=true`、`active=true`、`server_verified=true`、`port_owned=true`、`health=healthy`，且 PID、执行用户、配置与端口属于该实例。停止条件：任何一个身份核对失败或健康状态不是该实例的实际监听。

4. **预构建候选并升级。** 在停止旧服务前构建并验证对应 OS/架构的 `<candidate-binary>`，然后运行 `gofer serve upgrade --name '<name>' --binary '<candidate-binary>' -c '<config-file>'`。从本机 direct exec job 发起时，命令在独立 helper 接管并得到 server 的持久 drain 许可后返回 upgrade ID；使用 `--no-wait` 的终端也在该许可后返回。之后用 `gofer serve upgrade status '<upgrade-id>' --name '<name>' --json -c '<config-file>'` 查询终态。
   操作要点（2026-10-08 实测）：
   - 必须是 **direct exec job**：`gofer job run -a exec --runner server --env GOFER_CONFIG_DIR=<config-dir> -- <gofer.exe 绝对路径> serve upgrade …`。用 PowerShell/cmd 脚本包一层时，server 认不出发起者，drain 会等这个 job 自己结束，直到 drain 超时（`upgrade drain deadline expired`）。
   - 可执行文件写**绝对路径**：job 里裸写 `gofer` 会因 PATH 解析到相对目录而被拒（`cannot run executable found relative to current directory`）。
   - job 进程不继承 `GOFER_CONFIG_DIR`，要用 `--env` 显式传入，否则找不到 `<config-dir>/run/service/<name>.json`。
   - 候选可以是 UPX 压缩的发布包（如 `make build` / gh release 产物）：文件里读不到 Go build info 时，升级检查会以 `GOFER_PRINT_BUILDINFO=1` 运行候选，让它自报 module 路径与 GOOS/GOARCH。该回退由**执行 `serve upgrade` 的 CLI** 提供，旧版 CLI 仍需未压缩候选，可直接用新候选自身执行 `serve upgrade`。
   预期：`succeeded` 后重新核对版本、进程身份、端口归属和健康；`rolled_back` 表示旧程序已恢复且可用。发起 job 的断连、完成或 orphaned 均不等于升级结果。停止条件：接管许可、drain、切换或回滚失败，结果记 `failed` 时按错误阶段保存证据并停止后续切换。数据库 schema 不随二进制自动回滚。

5. **停止、重启或卸载。** 分别使用 `gofer serve stop --name '<name>' -c '<config-file>'`、`gofer serve restart --name '<name>' -c '<config-file>'` 或 `gofer serve uninstall --name '<name>' -c '<config-file>'`。受管 stop 先记录停止意图，阻止 Windows supervisor 再拉起；Linux 通过 systemctl 停 unit，防止 Restart 策略抵消停止。`uninstall` 移除原生入口，保留用户程序、配置、证书、数据库和日志。
   预期：stop 后受管子进程退出且不复活，restart 只留下一个已核对实例，uninstall 后原生入口不存在。停止条件：优雅停机超时；不要把未知 PID 当本实例强杀。

## 验证

同时核对原生入口、进程可执行文件及创建身份、执行用户或桌面 session、实际监听端口 owner 和 HTTP/HTTPS/Web。受管升级再核对 `upgrade status` 的持久终态与运行版本；源代码构建成功或单独的 `/health 200` 不能替代该结果。需要磁盘 Web 时显式登记 `--web-dir`，标准发布可使用嵌入 Web。

证书改为 `"{config_dir}/certs/server.crt"` 与 `"{config_dir}/certs/server.key"` 时，先对复制出的原 CA/证书/私钥做内容与配对检查，再验证已信任客户端的 HTTPS 访问。搬动源码目录后重新登记 `--exe`，不要假设旧会话 cwd 会自动迁移。

## 回滚与恢复

升级失败优先查 `serve upgrade status` 和 `<config-dir>/run/upgrade/` 结果，确认旧程序是否实际恢复、旧服务是否健康。若 `failed` 且回滚本身失败，保留程序备份与日志，由维护者检查，不能把数据库变化视为已回滚。登记更新失败时先核对原生入口和 spec 是否仍指向旧程序；只有拥有该任务/unit 的目标操作者才执行恢复。实际证书和仓库迁移使用事先保留的源快照回退；tracker 分拆另按离线迁移清单执行。

## 证据保留

记录命令与退出码、`status --json`、升级 ID 与最终 `upgrade status --json`、原生任务/unit 状态、PID/程序版本/端口归属、日志路径、配置与候选的哈希。凭据、私钥、完整 token 和敏感 URL 不进入证据。隔离实例结束后核对其任务/unit 与进程均消失，资产保留情况符合预期。

## 排障与升级

Windows 任务只在用户登录后进入交互 session；Locked/RDP 桌面的 GUI 能力取决于该会话与权限等级。Linux `system` 与 `user` scope 的权限和生命周期不同，`user` 不会自动获得 linger。若 `status` 标明原生入口缺失、身份不匹配或 `port_owned=false`，先读对应应用与原生日志，再按本 runbook 的只读核对重建事实；不要绕过所有权拒绝。当前脚本入口与新入口并存期间，切换顺序及具体机器路径由获批准的迁移操作记录给出。
