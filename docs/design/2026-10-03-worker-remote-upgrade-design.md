<!-- template_id: design; template_version: 1.1.1 -->
# 远程升级 worker 与两项小修（U 批）

> 状态：Approved（Draft 0.1；用户 2026-10-03 在 web 中继确认）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-03 | Claude | 远程升级 worker（v15 能力）、tools-xmg、传话 job 默认隐藏 |

## 背景

用户常不在 worker 机器旁。一台 Windows worker 因无法现场升级，长期停在很旧的版本，新功能（持续会话、事件上报、常驻传话等）都用不了。server 已有自更新（`scripts/win-selfupdate.ps1`），worker 没有。

## 方案

### U1 远程升级 worker

**入口**：`gofer worker upgrade <id> [--file <worker 二进制>] [--force]`；Runners 页 worker 卡片「升级」按钮（显示当前版本 → 目标版本，进度与结果）。管理员权限。

**二进制来源**：
- 默认：server 自身的可执行文件——仅当 worker 的 os/arch 与 server 相同（worker 注册时已上报 os/arch）。
- `--file` 指定的二进制（如为 linux worker 提供交叉编译产物）：CLI 上传到 server 暂存（复用现有文件传输 / 上传机制），只用于本次升级。
- server 记录目标二进制的 sha256、大小与版本（server 侧运行 `<bin> --version` 不可行时，以 worker 侧校验为准）。

**协议（v15，可选能力 `UpgradeMinProtocolVersion = 15`）**：server 发 `upgrade` 帧（sha256、size、目标版本、下载路径）；worker 用自己的 worker token 通过 HTTP 从 server 拉取二进制（新接口，worker token 鉴权，只能拉给自己的那份），落到运行目录旁的临时文件，校验 sha256，执行 `<new> --version` 确认可运行且版本符合。worker 协议 < v15 的 server 侧直接拒绝并提示"该 worker 版本过旧，需要手动升级一次"。

**切换与回滚（无需守护进程）**：
1. 前置：worker 必须空闲（无在途 job）；否则先进入"排空"状态（拒绝新派发，等在途 job 结束，可设上限），排空超时则放弃升级并恢复接单。
2. 旧进程把新二进制放到位：Windows 上把正在运行的 exe 改名为 `<exe>.old`（运行中允许改名），新文件移到原路径；unix 上原子 rename 覆盖。
3. 旧进程以相同参数、相同配置启动新进程（Windows 用 detached 子进程；带 `--upgrade-from <旧pid>` 标记），自己保持运行但不接新任务。
4. 新进程注册到 server。server 侧把同一 worker_id 的新连接视为"升级交接"（新连接带交接标记，避免按"worker 重启"把任务判失败——前置已保证无在途 job）。
5. 新进程注册成功并通过自检后，通知旧进程退出（本地信号 / 命名事件 / pid 文件约定均可，选最简单可靠的跨平台方式）；旧进程退出，`.old` 保留一个版本用于手动回退。
6. 新进程在 `upgrade_timeout`（默认 60s）内未成功注册：旧进程杀掉新进程，把 `.old` 还原回原路径，恢复接单，并向 server 报告失败原因。
7. daemon 模式（`gofer worker -d`）的 pid 文件、日志文件在交接后指向新进程。

**结果**：server 记录升级事件（目标版本、结果、耗时、失败原因）；CLI 与页面显示"已升级到 vX / 已回滚：原因"。

### U2 tools-xmg

`gofer worker add` / `POST /v1/workers` 保存配置时多写了 `web_enabled: true`（原文件无此键）。保存只写声明过的键与本次改动，补测试覆盖"登记前后配置 diff 只包含 workers / runners / allowed_runners"。

### U3 传话 job 默认隐藏

每条 web 传话都会生成一条 job 记录（送达记录），使 Board 等列表变乱。给这类 job 打内部标记，Board / job 列表 / 工作台默认不显示，提供"显示传话 job"勾选；会话抽屉里的送达状态与 job 详情不受影响；`gofer job list` 默认同样隐藏，`--all` 显示。

## 测试与验收

固定测试：`TestWorkerUpgradeRejectsOldProtocol`、`TestWorkerUpgradeDrainsBeforeSwitch`、`TestWorkerUpgradeHandoverKeepsWorkerOnline`、`TestWorkerUpgradeRollsBackWhenNewFailsToRegister`、`TestWorkerUpgradeVerifiesChecksum`、`TestRegisterWorkerSavesOnlyDeclaredKeys`、`TestMessengerJobsHiddenByDefault`，web Vitest。

真实验收（监督者，容器内 linux + codex 在 Windows 主机上各一遍）：临时 serve + 临时 worker（旧版本二进制）→ `gofer worker upgrade --file <新版本>` → worker 版本变为新版本且一直在线、能派 job；用一个启动即失败的假二进制验证回滚：worker 回到旧版本继续工作并报告失败。
