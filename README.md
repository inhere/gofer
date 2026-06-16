# dev-agent-bridge

Bridge local and container CLI agents (Codex / Claude / OpenCode / custom) across allowed projects.

## Build

```bash
go build ./cmd/agent-bridge
# or
make build
```

## Usage

```bash
agent-bridge serve --help
agent-bridge project --help
agent-bridge agent --help
agent-bridge job --help
```

详细设计与实施计划见 `docs/`。
