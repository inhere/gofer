# 一键更新设计

- issue：gofer-29gi
- 前置：[检查更新设计](2026-10-13-update-check-design.md)（gofer-6x2n）
- 状态：已实施（2026-10-13）

## 一、目标

- 有新版本时主动提示：web 顶栏徽标，以及 `update.available` 通知（默认触发事件）。
- 以服务方式运行的 server（`gofer serve register`）可以在 web 上一键更新自己，并可选同时更新 worker（逐个勾选，默认勾选全部在线的）。
- 普通 `gofer serve`（前台 / `-d`）不能自我替换，web 只给出手动步骤。
- 其他平台的 worker 从同一来源下载自己平台的发布包。
- `latest.yaml` 记录各平台二进制的 sha256，下载后校验（来源没发布时不校验）；GitHub release 也上传 `latest.yaml`。

## 二、发布物

- `make latest-yaml` 在 `name` / `version` / `released_at` / `description` 之外追加：

  ```yaml
  files:
    gofer-linux-amd64: <sha256>
    gofer-windows-amd64.exe: <sha256>
  ```

  键是 `make build-all` 产出的**二进制**文件名（不是压缩包）。
- `make release` 把 `latest.yaml` 一并放进 `release/`，release workflow 上传 `release/*`，所以 GitHub release 里也有它。
- 检查器：mirror 直接读 `latest_url`；GitHub 读 release 资产里的 `latest.yaml`（没有就没有 sha256，旧 release 照常可用）。

## 三、流程

`POST /v1/update/apply {"workers": ["w1", ...]}`（管理员；job 凭据 403）：

1. **前置**：server 是受管服务（否则 409 并说明手动步骤）；最近一次检查成功且有新版本（否则 409）；同一时间只允许一次。
2. **下载**（旧 server，阶段 `downloading`）：
   - 本平台二进制下到 `<config-dir>/run/update/<version>/`；mirror 是裸二进制，GitHub 从 `.tar.gz` / `.zip` 里取出同名文件；有 sha256 就校验。
   - 运行 `<bin> --version`，确认就是目标版本。
   - 勾选的 worker 中，平台与 server 不同的，也在这一步下载各自平台的二进制（同平台的共用 server 的新二进制）。离线（平台未知）或下载失败的 worker 标为 `skipped`。
   - 失败即 `failed`，server 不动。
3. **重启**（阶段 `restarting`）：用下载的二进制执行 `serve upgrade --name <服务名> --binary <bin> -c <config> --no-wait`——与管理员手动升级同一条路径：排空运行中的 job、交接、失败回滚。命令在排空被接受后返回，输出写 `run/update/serve-upgrade.log`。
4. **新 server 接着做**（启动时读 `run/update/apply.json`）：
   - 版本不是目标版本（升级没生效 / 已回滚）→ `failed`，不动 worker。
   - 否则阶段 `upgrading_workers`：逐个等 worker 重连（每个最多 3 分钟），按现有远程升级流程交给它（同平台：server 自己的可执行文件；其他平台：第 2 步下载的二进制），worker 接受后记 `accepted`，最终结果取该 worker 的升级记录（`succeeded` / `failed`）。
   - 全部处理完为 `done`。

进度保存在 `<config-dir>/run/update/apply.json`，`GET /v1/update` 的 `apply` 字段返回（另有 `managed` 表示能否自我更新）。

## 四、web

- 顶栏：有新版本时显示 `↑ <版本>`，点进「设置 → 关于」。前端每 10 分钟读一次 server 的检查结果。
- 关于页：
  - 受管 server：「立即更新到 X…」→ 确认框（说明会排空 job 并重启、当前运行中的 job 数、worker 勾选列表含平台与版本，离线的不可选）→ 提交后轮询进度；重启期间请求失败显示「server 重启中」并继续轮询。
  - 普通 serve：显示手动步骤（替换二进制后重启；worker 用 `gofer worker upgrade`）。

## 五、取舍

- **不做自动更新**：只提示，由管理员决定何时更新（重启会等运行中的 job）。
- **不新增升级机制**：server 用现有的受管升级（`serve upgrade`），worker 用现有的远程升级；一键更新只负责下载、校验与编排。
- **server 不能只更新 worker**：worker 不能比 server 新，单独更新 worker 仍用 `gofer worker upgrade`。
- **通知每个版本只发一次**（与日志一样）；进程重启后会对同一版本再发一次，可接受。

## 实施记录

- `internal/updatecheck`：`files` 校验和、各平台资产、`AssetFor` / `Download`（解压、校验）、`SetOnAvailable`。
- `internal/notify`：`update.available` 加入默认触发事件；`job.Service.NotifyUpdateAvailable`。
- `internal/httpapi/update_apply.go`：apply 接口与新 server 的续做；`worker_upgrade_handler.go` 抽出 `startWorkerUpgrade` 共用。
- `internal/serve/update_apply.go`：受管 server 的 `serve upgrade` 调用。
- web：`store/updateAvailable.ts`（顶栏）、`components/UpdateApplyDialog.vue`、关于页进度。
