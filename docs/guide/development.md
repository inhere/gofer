# 贡献与开发

改 gofer 本身时才需要这一页。用 gofer 不用读。

## 环境

- Go 1.26+（见 `go.mod`）。
- 只在构建 Web 控制台时需要 Node.js 与 pnpm。

## 构建

```bash
make web build          # 构建并内嵌 Web 控制台，生成 dist/gofer（默认用 upx 压缩）
make install            # 同上，再把二进制复制到 $GOPATH/bin
go build -o dist/gofer ./cmd/gofer   # 只含 API/CLI；控制台显示占位页
make build-all          # 交叉编译 linux / darwin / windows × amd64 / arm64，并生成 latest.yaml
```

## 验证

```bash
go build ./... && go vet ./...
go test ./...                                   # 全量；发版前用 -race -count=1
cd web && npx vue-tsc --noEmit && npx vitest run && npx vite build
GOOS=darwin go vet ./...                        # 改动平台相关代码之后
```

测试与前端校验的具体要求（哪些改动跑哪套、并行用 worktree、平台构建标签的注意事项）见仓库根目录的 [`AGENTS.md`](../../AGENTS.md) 与项目记忆；有了新的验证约定就写在那里，不要散落在文档里。

## 改动约定

- **项目规则**（代码分层、CLI 分组与 flag 绑定、兼容策略、公共工具、路由与权限登记、测试稳定性）见 [`AGENTS.md`](../../AGENTS.md)。要点：入口层（`commands`、`httpapi`、`mcpserver`）只做绑定和转发；依赖单向；新的小工具命令放在 `gofer tool` 下；临时兼容代码带 `DEPRECATED(vX): remove in vY` 标记。
- **用户可见的改动**在同一批提交里更新 [`skills/gofer-usage/`](../../skills/gofer-usage/SKILL.md)，并在 [`CHANGELOG.md`](../../CHANGELOG.md) 的「未发布」下加一条。改到这里（`docs/guide/`）的行为也要同步。
- **HTTP 路由**必须在 `internal/httpapi/authz_routes.go` 声明并登记权限，然后 `go generate ./internal/httpapi/...` 更新 [`docs/reference/permissions.md`](../reference/permissions.md)。
- **设计稿**放 `docs/design/YYYY-MM-DD-<主题>-design.md`，文档头写 issue id，实施后在末尾补「实施记录」；操作步骤写 `docs/runbook/`。
- **发版与换二进制**：见 [`docs/runbook/release-and-binary-swap.md`](../runbook/release-and-binary-swap.md)；受管 server 的操作见 [`docs/runbook/2026-10-08-serve-management-runbook.md`](../runbook/2026-10-08-serve-management-runbook.md)。
- 并行 worktree、设计稿命名等仓库自身约定同样在 [`AGENTS.md`](../../AGENTS.md)。

## 文档分工

| 位置 | 读者 | 内容 |
|---|---|---|
| `docs/guide/`（本目录） | 用 gofer 的人 | 概念、常见用法、安全、排障、稳定性 |
| [`skills/gofer-usage/`](../../skills/gofer-usage/SKILL.md) | agent（Claude Code / Codex …） | 命令速查、配置字段、协议门槛，随每个用户可见改动同步 |
| [`docs/reference/`](../reference/) | 要对接的人 | HTTP API、权限目录、Web 控制台细节 |
| [`docs/runbook/`](../runbook/) | 运维的人 | 一步步的操作手册 |
| [`docs/design/`](../design/) | 改这块代码的人 | 设计与权衡记录（历史快照，不随实现回改） |
