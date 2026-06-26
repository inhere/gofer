# Gofer serve/worker `-d/--daemon` 后台运行实施计划

> 来源：beads `example-project-c44`（feature, P3, label gofer）。
> 目标：`gofer serve` 与 `gofer worker` 支持 `-d/--daemon`，进程以 detached 方式后台运行，省去外部 `nohup ... &` 包装；配套 pidfile + 重复启动检测 + 优雅停机 + `gofer stop` 子命令。
> 约束：`tools/gofer` 为独立本地 git 仓（无 remote），提交即终点。遵守 CLAUDE.md G021/G022/G023。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-25 | claude | 初版计划，4 项决策待确认 |
| v1.0 | 2026-06-25 | claude | 按推荐锁定 4 项决策（§5），P0 转为「已定」，进入可实施态 |

## 0. 已锁定决策（§5 详）

① 跨平台：win 上 `-d` 报不支持（Linux 真 detach）；② 运行时文件统一 `<config-dir>/run/`；③ 纳入 `gofer stop` 子命令（P4）；④ serve 一并补 SIGINT/SIGTERM 优雅停机（P2）。

## 1. 总纲

| 阶段 | 目标 | 工作量 |
|---|---|---|
| **P0** | ~~约定确认~~ → 已定（决策见 §5，已按推荐锁定）| 已定 |
| **P1** | 新增 `internal/daemon` 包：re-exec 自身 detach + pidfile 读写 + 重复启动检测 + 跨平台 build tag | 中 |
| **P2** | `serve` 接入 `-d/--daemon`；补 serve 优雅停机（SIGTERM/SIGINT → `http.Server.Shutdown` + 关现有 stop 通道）| 中 |
| **P3** | `worker` 接入 `-d/--daemon`（`worker.Serve` 已有优雅停机，仅需 daemonize 包裹 + pidfile）| 小 |
| **P4** | `gofer stop [serve\|worker [<id>]]` 子命令：读 pidfile 发 SIGTERM（**推荐纳入**，否则后台进程难干净停）| 小 |
| **P5** | 测试 + 文档（CLAUDE.md G 规则补充、容器 start-worker.sh 简化说明）+ 真机冒烟 | 小 |

> 总改动预估 >3 文件 / >100 行 → 需用户确认后实施（SR1204）。本文即确认载体。

## 2. 关键落点

| 改动 | 落点 |
|---|---|
| 新包 daemonize 编排 | `internal/daemon/daemon.go`（跨平台）+ `daemon_unix.go`(`//go:build unix`) + `daemon_windows.go`(`//go:build windows`) |
| pidfile / log 路径解析 | `internal/config/loader.go` 新增 `RuntimeFilePath(kind, name)` helper（复用 `ConfigDir()`）|
| serve flag + daemon 接入 | `internal/commands/serve.go`（`-d/--daemon` BoolOpt，`runServe` 早期调 daemon）|
| serve 优雅停机 | `internal/serve/serve.go`（信号 goroutine）+ `internal/httpapi/server.go`（`Server.Run` 支持 `Shutdown`）|
| worker flag + daemon 接入 | `internal/commands/worker.go`（`-d/--daemon` BoolOpt，`runWorker` 早期调 daemon）|
| stop 子命令 | `internal/commands/stop.go` + 在 app 注册 |
| 现状信号事实 | worker：`internal/worker/serve.go` 已处理 SIGINT/SIGTERM；serve：仅 `startReloadLoop` 处理 SIGHUP，**无** SIGINT/SIGTERM 处理（默认信号终止、defer 不执行）|

## 3. 前置检查（P0 PASS 才开工）

- [ ] `go build ./... && go vet ./... && go test ./...` 基线绿。
- [ ] 验 Go re-exec + `Setsid` 在目标内核（WSL2 6.6）detach 正常：最小冒烟 `setsid sleep` 等价行为。
- [ ] **前置依赖**：等后台 cuu/oed 日志清理 agent 完成并提交后再开工，避免同仓工作区并发改动冲突。

> 运行平台 / 落点 / stop / serve 优雅停机 4 项均已在 §5 锁定，无需再确认。

---

## P0：约定已定（决策留痕，详见 §5）

### ① 跨平台策略 → win 报不支持
- daemonize 依赖 `syscall.SysProcAttr{Setsid: true}`（Unix-only）。
- **采用**：`daemon_unix.go` 实现真 detach；`daemon_windows.go` 返回 `errors.New("daemon mode (-d) not supported on windows; run as a service")`，`-d` 在 win 上直接报错退出。gofer 主跑 Linux（容器 + WSL），win 原生后台交服务管理器。
- （备选 `CreateProcess`+`DETACHED_PROCESS` 暂不做，后续有需要再加。）

### ② 运行时文件落点 → 统一 `<config-dir>/run/`
- serve： `<config-dir>/run/serve.pid` ，`<config-dir>/run/serve.log`
- worker：`<config-dir>/run/worker-<id>.pid`，`<config-dir>/run/worker-<id>.log`
- 解析：`config.RuntimeFilePath("run", "serve.pid")` → `<config-dir>/run/serve.pid`；`ConfigDir()` 不可解析时降级到 `./run/...`。调用方 `MkdirAll` 父目录。

### ③ `gofer stop` 子命令 → 纳入（P4）
- 读 pidfile 发 SIGTERM 并等待退出，运维闭环（避免手动 `cat pidfile | xargs kill`）。

### ④ serve 优雅停机 → 一并补（P2）
- 现状 serve 仅处理 SIGHUP，SIGINT/SIGTERM 走默认 kill、defer 不执行；AC「优雅停机仍生效」要求补 `http.Server.Shutdown`。

---

## P1：`internal/daemon` 包（detach + pidfile + 重复检测）

### 设计：env-sentinel 二次执行（Go 不能安全 fork，用 re-exec）

机制：带 `-d` 的「父」进程 re-exec **自身**（同 argv，注入 sentinel env `GOFER_DAEMONIZED=1`），子进程 `Setsid` 脱离终端、stdout/stderr 重定向到 logfile；父进程写 pidfile（=子 PID，Setsid 后子即会话首）并打印 pid 后 `exit 0`。子进程因 env sentinel 跳过再次 daemonize，正常跑业务。

> 不删 argv 里的 `-d`：用 env sentinel 作幂等闸（避免改 argv 的脆弱性）。

### `internal/daemon/daemon.go`（跨平台公共部分）

```go
package daemon

// EnvSentinel 标记「当前进程已是 daemon 子进程」，避免无限 re-exec。
const EnvSentinel = "GOFER_DAEMONIZED"

// Options 描述一次 daemonize 的运行时落点。
type Options struct {
    Name    string // 诊断用：serve / worker-<id>
    PIDPath string // pidfile 绝对路径
    LogPath string // stdout/stderr 重定向目标
}

// Daemonized 报告当前进程是否已是被 detach 的子进程（读 EnvSentinel）。
func Daemonized() bool { return os.Getenv(EnvSentinel) == "1" }

// Spawn 在「父」进程里 re-exec 自身为后台子进程：
//  1. 若 pidfile 指向存活进程 → 返回 ErrAlreadyRunning（重复启动检测）；
//  2. 打开/截断 logfile，re-exec self（Setsid + 重定向，见 *_unix.go）；
//  3. 写 pidfile=child.Pid，打印 "started pid=N log=...”，调用方据返回值 exit 0。
// 仅父进程调用；子进程（Daemonized()==true）不应进入此函数。
func Spawn(o Options) (pid int, err error)

// WritePIDFile / ReadPIDFile / PIDAlive / RemovePIDFile：pidfile 原子读写 + 存活探测（kill -0）。
// 子进程在优雅退出时 RemovePIDFile(o.PIDPath) 清理。
```

### `internal/daemon/daemon_unix.go`（`//go:build unix`）

```go
//go:build unix

// reexecDetached 启动一份自身副本，脱离控制终端并把 std* 接到 logfile。
func reexecDetached(logPath string) (*exec.Cmd, error) {
    self, err := os.Executable()
    if err != nil { return nil, err }
    lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
    if err != nil { return nil, err }
    cmd := exec.Command(self, os.Args[1:]...)
    cmd.Env = append(os.Environ(), EnvSentinel+"=1")
    cmd.Stdin = nil                  // 等价 /dev/null
    cmd.Stdout, cmd.Stderr = lf, lf
    cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // 新会话，脱离 tty
    if err := cmd.Start(); err != nil { _ = lf.Close(); return nil, err }
    // 父进程关闭自己持有的 logfile fd；子进程已继承
    _ = lf.Close()
    return cmd, nil
}

// PIDAlive: syscall.Kill(pid, 0) == nil
```

### `internal/daemon/daemon_windows.go`（`//go:build windows`）

```go
//go:build windows
func reexecDetached(string) (*exec.Cmd, error) {
    return nil, errors.New("daemon mode (-d) not supported on windows; run as a service")
}
```

### `config.RuntimeFilePath`（`internal/config/loader.go`）

```go
// RuntimeFilePath 返回运行时文件路径 <config-dir>/<sub>/<name>（如 run/serve.pid）。
// ConfigDir 不可解析时降级到 ./<sub>/<name>，保证仍可用。调用方负责 MkdirAll 父目录。
func RuntimeFilePath(sub, name string) string {
    dir, err := ConfigDir()
    if err != nil || dir == "" { return filepath.Join(sub, name) }
    return filepath.Join(dir, sub, name)
}
```

### 验收（P1）
- [ ] `go build ./...`（含 unix+windows 交叉：`GOOS=windows go build ./...` 编译过，windows 分支返回错误而非缺符号）。
- [ ] 单测：`Daemonized()` 受 env 控制；`WritePIDFile`/`ReadPIDFile` 往返；`PIDAlive(自身pid)==true`、`PIDAlive(不存在pid)==false`；`RuntimeFilePath` 用 `GOFER_CONFIG_DIR` 解析正确（参照 `dotenv_test.go` 写法）。
- [ ] commit：`feat(gofer): internal/daemon 包(re-exec detach + pidfile + 重复检测)`。

---

## P2：serve 接入 `-d/--daemon` + 优雅停机

### serve flag 与早期 daemonize（`internal/commands/serve.go`）

```go
// serveOpts 增加
daemon bool
// Config 增加
c.BoolOpt(&serveOpts.daemon, "daemon", "d", false, "run in background (detached)")

func runServe(c *gcli.Command, _ []string) error {
    if serveOpts.daemon && !daemon.Daemonized() {
        pid, err := daemon.Spawn(daemon.Options{
            Name:    "serve",
            PIDPath: config.RuntimeFilePath("run", "serve.pid"),
            LogPath: config.RuntimeFilePath("run", "serve.log"),
        })
        if err != nil { return errorx.Failf(serve.ExitErr, "%v", err) }
        c.Printf("gofer serve 已后台启动 pid=%d log=%s\n", pid, config.RuntimeFilePath("run","serve.log"))
        return nil // 父进程退出
    }
    // 子进程(或前台)：原逻辑不变；若 Daemonized() 则在 serve.Start 内写自身 pidfile + 退出时清理
    ...
}
```

### serve 优雅停机（**当前缺口**：serve 不处理 SIGINT/SIGTERM）

`serve.Start` 末尾 `srv.Run(addr)` 阻塞在 `ListenAndServe`，无信号处理 → 默认信号 kill，`defer`（store.Close / 各 stop 通道）**不执行**。daemon 化后 `stop`/`kill` 需优雅退出，故补：

- `internal/httpapi/server.go`：`Server.Run` 改为持有 `*http.Server` 并暴露 `Shutdown(ctx)`；或 `Run` 内 select 信号。**推荐**：`Run` 不变签名，新增 `RunCtx(ctx, addr)`——ctx 取消时 `srv.Shutdown`。

```go
func (s *Server) RunCtx(ctx context.Context, addr string) error {
    srv := &http.Server{Addr: addr, Handler: s.router}
    go func() { <-ctx.Done(); _ = srv.Shutdown(context.Background()) }()
    fmt.Printf("gofer: listening on %s\n", addr)
    if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
        return err
    }
    return nil
}
```

- `internal/serve/serve.go`：建 ctx + SIGINT/SIGTERM 监听，cancel 时走 `RunCtx` 优雅退出；返回后既有 `defer`（store.Close、close 各 stop 通道）正常执行。子进程在此清理 pidfile。

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()
if daemon.Daemonized() { defer daemon.RemovePIDFile(config.RuntimeFilePath("run","serve.pid")) }
return srv.RunCtx(ctx, addr)
```

> 注：SIGHUP reload 既有 goroutine 不变；此处只加 SIGINT/SIGTERM。

### 验收（P2）
- [ ] `gofer serve -d` 后台起：父进程立即返回打印 pid；`run/serve.pid` 存在且进程存活；`run/serve.log` 有启动日志（"listening on ..."）。
- [ ] 前台 `gofer serve`（无 -d）行为与现状一致。
- [ ] `kill -TERM <pid>` 或 `gofer stop serve`（P4）：进程优雅退出、pidfile 被清理、store 正常关闭（无 WAL 警告）。
- [ ] 单测：runServe 在 `Daemonized()` 分支 mock 下不真正起服务；RunCtx ctx 取消即返回 nil。
- [ ] commit：`feat(gofer): serve 支持 -d/--daemon + SIGTERM 优雅停机`。

---

## P3：worker 接入 `-d/--daemon`

`worker.Serve`（`internal/worker/serve.go`）**已**处理 SIGINT/SIGTERM 优雅停机，故 worker 只需 daemonize 包裹 + pidfile：

```go
// worker.go workerOpts 增 daemon bool；Config 增 -d/--daemon
func runWorker(c *gcli.Command, _ []string) error {
    wc, err := loadWorkerConfig(workerOpts.config)   // 已支持缺省 <config-dir>/worker.yaml
    if err != nil { return errorx.Failf(workerExitErr, "%v", err) }
    if workerOpts.daemon && !daemon.Daemonized() {
        name := "worker-" + wc.WorkerID
        pid, err := daemon.Spawn(daemon.Options{
            Name: name,
            PIDPath: config.RuntimeFilePath("run", name+".pid"),
            LogPath: config.RuntimeFilePath("run", name+".log"),
        })
        if err != nil { return errorx.Failf(workerExitErr, "%v", err) }
        c.Printf("gofer worker(%s) 已后台启动 pid=%d\n", wc.WorkerID, pid)
        return nil
    }
    // 子进程：原逻辑；worker.Serve 退出时清理 pidfile（传 pidPath 或在 Serve 外 defer）
    ...
}
```

- pidfile 清理：`runWorker` 子进程分支 `defer daemon.RemovePIDFile(...)`（在 `worker.Serve` 返回后），无需改 `worker.Serve` 签名。

### 验收（P3）
- [ ] `gofer worker -d`（容器内，缺省读 `<config-dir>/worker.yaml`）后台起：打印 pid；`run/worker-w-container-example.pid` 存在；`run/worker-w-container-example.log` 有 "worker starting"；worker 在 host hub 上线（`gofer` host 侧 `/v1/runners` 可见）。
- [ ] `kill -TERM` / `gofer stop worker w-container-example`：going-away 优雅断连、pidfile 清理。
- [ ] commit：`feat(gofer): worker 支持 -d/--daemon`。

---

## P4：`gofer stop` 子命令（推荐）

```go
// internal/commands/stop.go
// gofer stop serve              → 读 run/serve.pid，SIGTERM，等待退出
// gofer stop worker <worker-id> → 读 run/worker-<id>.pid，SIGTERM
func runStop(c *gcli.Command, _ []string) error {
    // target = serve | worker；worker 需 <id> arg
    // pid := daemon.ReadPIDFile(path); 若 !PIDAlive → 提示「未在运行」并清理残留 pidfile
    // syscall.Kill(pid, SIGTERM); 轮询至多 N 秒确认退出；超时提示可 -9
}
```

- 注册到 app（与 serve/worker 同级）。
- 跨平台：windows 分支 `stop` 用 `taskkill` 或同样报不支持（与 P1 策略一致）。

### 验收（P4）
- [ ] `gofer stop serve` / `gofer stop worker <id>` 能停掉对应后台进程、清理 pidfile、未运行时友好提示。
- [ ] commit：`feat(gofer): stop 子命令(读 pidfile 优雅停后台进程)`。

---

## P5：测试 + 文档 + 冒烟

- [ ] 全量 `go test ./...` 绿；`GOOS=windows go build ./...` 通过（windows 分支编译）。
- [ ] CLAUDE.md 补 G 规则：daemonize 编排属进程编排 → 放 `internal/daemon`，**不放 commands**（G021）；commands 仅判断 `-d` 并转发。
- [ ] 文档：`docs/` 更新 serve/worker 后台运行说明；记忆 `gofer-container-worker-setup` 提示 `start-worker.sh` 可简化为 `gofer worker -d`（nohup/& 可移除）。
- [ ] 真机冒烟（host serve -d + 容器 worker -d + 提交 job + stop）→ 交 host codex 联调（涉及跨容器链路）。
- [ ] commit：`docs/test(gofer): daemon 模式文档 + 测试`，并 `bd close example-project-c44`。

---

## 4. 进度跟进

- [ ] **P0** 约定确认（待确认①②③）
- [ ] **P1** internal/daemon 包
- [ ] **P2** serve -d + 优雅停机
- [ ] **P3** worker -d
- [ ] **P4** stop 子命令
- [ ] **P5** 测试 + 文档 + 冒烟

## 5. 已确认决策（2026-06-25 锁定，留痕 SR1101）

| # | 决策点 | 结论 | 说明 |
|---|---|---|---|
| ① | 跨平台 | **win 报不支持** | `daemon_unix.go` 真 detach；`daemon_windows.go` 返回错误。gofer 主跑 Linux |
| ② | 运行时文件落点 | **统一 `<config-dir>/run/`** | serve.{pid,log} / worker-`<id>`.{pid,log}；`config.RuntimeFilePath` 解析 |
| ③ | stop 子命令 | **纳入（P4）** | 读 pidfile 发 SIGTERM 并等待退出 |
| ④ | serve 优雅停机 | **一并补（P2）** | SIGINT/SIGTERM → `http.Server.Shutdown` + 关 stop 通道，满足 AC |
