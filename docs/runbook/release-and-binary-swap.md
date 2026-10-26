# 发版与换二进制流程（gofer 仓库维护者）

> 面向 gofer 仓库维护者；使用者的升级入口是 `gofer serve upgrade` / `gofer worker upgrade`（见各自的 `--help` 与 [受管 server runbook](2026-10-08-serve-management-runbook.md)）。机器相关的路径、worker 清单、代理等保存在项目记忆 `gofer memory show gofer-release-flow`，本文只写通用步骤。维护者工作区可以把第 3 步之后的步骤写成一个脚本串起来，失败即停。

## 版本号与构建

- 从**打了 tag 的提交**构建。`make` 的版本号取自 `git describe --tags --always --dirty`：脏工作树会带 `-dirty` 后缀，tag 之后的提交带 `-N-g<hash>` 后缀。看到这类后缀说明构建的不是干净 tag。
- 构建流程：`git archive <tag>` 到一个干净目录，再 `make web && make build`（有前端改动才需要重建 web；live 控制台目录的 `index.html` 要同步更新）。
- 仅构建 CLI 时可 `go build -ldflags "-X main.Version=X -X main.GitCommit=<hash> -X main.BuildDate=<date>" -o <out> ./cmd/gofer`。
- CLI、server、web 前端要同步升级到同一版本；CLI 比 server 旧会缺命令 / 字段。worker 协议有变化时先升 server，再升 worker。

## 流程

1. 开始前 `gofer job list --status running`：本机 runner 的 job 是 server 的子进程，重启 server 会让它们被判 `failed: orphaned: serve restarted…`（worker 上的 job 能经 `recovering` 重连）。有长 job 先等；受管升级的 drain 会等它们，上限用 `--drain-timeout` 指定（默认 `5m`）；已在 worker 上跑的 job 与空闲的 ACP 持续会话都不计入（本机会话重启后 `session/load` 恢复，worker 会话由 worker 保留、重启后接管）。drain 卡住时看 serve.log 的 `upgrade.drain_waiting`，里面列出在等的 job。开正式发版前先跑一次 `sh scripts/check-private-names.sh`：按不入库的 `.private-names`（格式见 `.private-names.example`）扫 `git ls-files`，确认没有私有项目名 / 主机名 / 内网地址（见 `AGENTS.md` G031）。
2. 更新 `CHANGELOG.md`：把「未发布」整段移入新版本条目，对照 `git log <上一个 tag>..HEAD` 补齐用户可见变化；同时更新路线图里本版落地项的版本列。提交后在该提交打 annotated tag `vX.Y.Z`。
3. 按上一节从 tag 构建候选二进制并验证 `--version`。
4. 升级 server：`gofer serve upgrade --binary <候选> [--name <name>] -c <config> [--no-wait]`。如果从 gofer job 里发起，必须是 **direct exec job**（`gofer job run -a exec --runner server --env GOFER_CONFIG_DIR=<config-dir> -- <gofer 绝对路径> serve upgrade …`），不要包在 shell 脚本里，否则 drain 会等发起 job 自己结束直至超时。用 `gofer serve upgrade status <upgrade-id>` 等到终态，再核对版本。
5. 升级容器 / 本机 CLI：用同一个 tag 构建后覆盖旧二进制（先写 `.new` 再 `mv -f`）；重启本机 worker（`gofer worker stop` 后重新启动）。
6. 升级远程 worker：`gofer worker upgrade <id> [--timeout 秒]`。
7. push 分支与 tag（需要网络 / 凭据的机器上做；按用户授权）；推完 `git ls-remote origin refs/heads/main` 核对。
8. 推完等 CI 跑完再算发版结束：`gh run list -R <owner>/<repo> -L 4 --branch main` 里本次提交的 Unit-Tests 与 CodeQL 都是 `success`，`gh api 'repos/<owner>/<repo>/code-scanning/alerts?state=open'` 没有新增报警。失败先 `gh run view <run-id> --log-failed` 看原因，按失败修复后发补丁版；不要把红灯留到下一次发版（曾有单元测试连续 5 个版本失败、3 个 CodeQL 报警留存数小时无人发现）。

## 关于优雅停机

server（serve / worker / mcp）收到停止信号时先停止接收新 job，取消本机执行的 job 并杀掉其进程树，等后台收尾写完再关库；这些 job 的记录保持未结束，下次启动判为 orphaned。worker 上执行的 job 不会被取消，由新 server 接管（`recovering`）。正在收尾（状态已终态、还没写完）的 job 立即删除会等最多 2 秒，仍未写完返回 409，稍后重试。
