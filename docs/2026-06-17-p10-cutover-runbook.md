# P10 Cutover 执行手册（codex-bridge → dev-agent-bridge）

> 在**主机侧**人工执行。目标：用新的 `agent-bridge serve` 替换仍在运行的旧 `codex-bridge`（端口保持 8765），验证后更新根 `CLAUDE.md` 并退役旧工具。
> 旧服务：`tools/codex-bridge/`（`/run` + `/result/{id}`，`X-Bridge-Token` 头，`mode=codex|exec`）。新服务：`tools/dev-agent-bridge`（`/v1/jobs`，`Authorization: Bearer`，多 agent/项目 + MCP + 运行中交互）。
>
> ⚠️ **顺序铁律**：先起新服务并**容器侧验证通过**，再改 `CLAUDE.md` / 删 `tools/codex-bridge`。验证未过前别动文档（否则文档与存活服务契约不符），且随时可回滚到旧服务。

---

## 0. 前置确认（开工前 5 分钟）

- [ ] 主机已装 `codex` CLI（新 bridge 的 codex agent 仍调它）；按需 `claude` / `opencode`。`codex --version` 能跑。
- [ ] 记下当前 token：旧 `start.sh` 用 `tok-for-docker-dev`（与根 `CLAUDE.md` 一致）。新服务沿用同一值，**容器侧无需改动**。
- [ ] 确认主机平台与构建产物：
  - WSL2 / Linux 主机 → `dist/agent-bridge`
  - 原生 Windows 主机 → `dist/agent-bridge.exe`（用 `scripts/start.ps1`）
- [ ] 确认旧 `codex-bridge` 当前如何启动的（`start.sh` 前台 / nohup / 计划任务），便于第 3 步精确停掉。

---

## 1. 构建新二进制（主机）

```bash
cd /d/work/inhere/hyy-ai-inspect/tools/dev-agent-bridge
make build            # 产出 dist/agent-bridge（make 默认 upx 压缩；无 upx 用下一行）
# go build -o dist/agent-bridge ./cmd/agent-bridge
./dist/agent-bridge --help     # 应列出 serve/project/agent/job/mcp
```

> Windows：`go build -o dist/agent-bridge.exe ./cmd/agent-bridge`。

---

## 2. 准备配置（一次性）

新 bridge 是**通用化**的，不再硬编码 codex —— 需要一份 config 定义 agent 与项目。

### 2.1 token 走 `.env`（推荐，避免进程参数泄漏）

放到默认配置目录 `~/.config/dev-agent-bridge/.env` 或项目当前目录 `./.env`：

```ini
AGENT_BRIDGE_TOKEN=tok-for-docker-dev
```

> 新增能力：服务启动**先于读配置**加载 `<config-dir>/.env` 再 `./.env`；已导出的 OS env 优先。可用 `AGENT_BRIDGE_CFG_DIR` 改配置目录位置。

### 2.2 config.yaml（放 `~/.config/dev-agent-bridge/config.yaml`）

```yaml
server:
  addr: 0.0.0.0:8765
  token_env: AGENT_BRIDGE_TOKEN     # 从上面的 .env / 环境读取
  allow_empty_token: false

projects:
  workspace:
    host_path: /d/work/inhere/hyy-ai-inspect   # 主机真实路径（Windows 用 D:/work/...）
    container_path: /workspace
    default_agent: codex
    allowed_agents: [codex, claude, exec]
    allowed_runners: [local]
    allow_exec: true
    max_concurrent_jobs: 4

agents:
  # ⚠️ Parity 关键：旧 codex-bridge 用 `-s danger-full-access -a never`（codex 全局选项，
  # 必须放在 exec 子命令【之前】，否则 codex 报 unexpected argument）。
  codex:
    type: cli-agent
    command: codex
    args: [-s, danger-full-access, -a, never, exec, "{{prompt}}"]
    detect: { command: codex, args: [--version] }
  claude:
    type: cli-agent
    command: claude
    args: ["-p", "{{prompt}}"]
    detect: { command: claude, args: [--version] }
  exec:
    type: exec
    detect: { command: sh, args: [-c, "true"] }

runners:
  local: { type: local }
```

> 或用 CLI 登记项目（等价 projects 段）：
> ```bash
> ./dist/agent-bridge project add workspace \
>   --host-path /d/work/inhere/hyy-ai-inspect --container-path /workspace \
>   --default-agent codex --allow-agent codex --allow-agent claude --allow-agent exec \
>   --allow-runner local --allow-exec
> ```
> 但 agent 的 codex parity 参数仍需手工编进 config 的 `agents:` 段。

- [ ] `./dist/agent-bridge agent detect` → codex `available`（claude/opencode 未装报 unavailable 不影响）。
- [ ] `./dist/agent-bridge project validate workspace` → 通过。

---

## 3. 停掉旧 codex-bridge

```bash
# 找到占用 8765 的旧进程
ss -ltnp | grep 8765        # 或 lsof -i:8765 / (Windows) netstat -ano | findstr 8765
# 前台启动的：Ctrl-C；nohup/后台的：kill <pid>；Windows：Stop-Process -Id <pid>
```

- [ ] 确认 8765 已无监听（`curl -s localhost:8765/health` 连接失败）。

---

## 4. 启动新服务

```bash
cd /d/work/inhere/hyy-ai-inspect/tools/dev-agent-bridge
# token 已在 .env；start.sh 会优先用 dist/agent-bridge，否则现编译
bash scripts/start.sh
# Windows: pwsh scripts/start.ps1
# 显式指定 config： CONFIG=~/.config/dev-agent-bridge/config.yaml bash scripts/start.sh
```

- [ ] 启动日志显示 `starting on 0.0.0.0:8765 (token auth enabled)`（不打印 token）。
- [ ] 主机本地 `curl -s localhost:8765/health` 返回 200。
- [ ] （可选）浏览器开 `http://localhost:8765/` 看到 Web 控制台。

---

## 5. 容器侧冒烟验证（在 docker 容器内执行）

```bash
BASE=http://host.docker.internal:8765
TOKEN=tok-for-docker-dev

# 5.1 健康
curl -s $BASE/health

# 5.2 exec job（替代旧 mode=exec）
ID=$(curl -s -X POST $BASE/v1/jobs -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"project_key":"workspace","agent":"exec","runner":"local","cmd":["go","version"],"cwd":".","timeout_sec":30}' \
  | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
sleep 2; curl -s $BASE/v1/jobs/$ID -H "Authorization: Bearer $TOKEN"   # status 应 done, exit_code 0

# 5.3 codex job（替代旧 mode=codex；确认 parity 参数生效）
curl -s -X POST $BASE/v1/jobs -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"project_key":"workspace","agent":"codex","runner":"local","prompt":"echo hello from codex","cwd":".","timeout_sec":120}'
# GET /v1/jobs/<id> 跟踪到 done；GET /v1/jobs/<id>/logs/stdout 看输出
```

- [ ] 5.1 health 200。
- [ ] 5.2 exec job → done / exit_code 0。
- [ ] 5.3 codex job 能跑（不报 codex 参数错 = parity 正确）。
- [ ] （可选）MCP：本地 `./dist/agent-bridge mcp --config <cfg>`，用 MCP inspector / 客户端列出 8 个 `bridge_*` tool。

> ⚠️ 任一项失败 → 直接回滚（见第 8 节），不要继续改文档。

---

## 6. 更新根 `CLAUDE.md`（验证通过后，逐行精确改）

当前 `CLAUDE.md`「Docker 内可使用的环境」段：

```text
- 通过 curl 调用主机的 `codex` CLI 或 直接跑需要主机环境的命令
  - access token: tok-for-docker-dev
  - docker access: http://host.docker.internal:8765
  - 详细使用文档查看 tools/codex-bridge/README.md
  - NOTE: 注意引用文档都使用当前工作空间下的相对路径
```

改为：

```text
- 通过主机 bridge 调用主机侧 agent（codex/claude）或直接跑需主机环境的命令
  - access token: tok-for-docker-dev
  - docker access: http://host.docker.internal:8765
  - 鉴权：请求头 Authorization: Bearer <access token>（旧 X-Bridge-Token 已废弃）
  - 接口：POST /v1/jobs 提交、GET /v1/jobs/{id} 查状态、/logs/{stdout|stderr} 取日志
  - 详细使用文档查看 tools/dev-agent-bridge/README.md
  - NOTE: 注意引用文档都使用当前工作空间下的相对路径
```

- [ ] 已核实：全工作空间内仅根 `CLAUDE.md` 引用 `codex-bridge`，无其它文件需改。改完 `grep -rn codex-bridge . --include=*.md` 应只剩 dev-agent-bridge 自身文档里的迁移说明。

---

## 7. 退役旧工具

- [ ] 删除 `tools/codex-bridge/` 目录（已无服务依赖它）。
- [ ] 各项目历史 `tmp/codex-bridge/` 结果目录**不自动删**，留作归档，人工按需清理。
- [ ] 旧接口 `/run`、`/result/{id}`、`mode=codex|exec`、`X-Bridge-Token` 均已废弃。

---

## 8. 回滚预案（新服务异常时）

```bash
# 停新服务（Ctrl-C 或 kill）；重启旧的：
cd /d/work/inhere/hyy-ai-inspect
TOKEN=tok-for-docker-dev bash tools/codex-bridge/start.sh
```

- 回滚后 `CLAUDE.md` 保持未改（铁律保证了这一点），容器侧契约不变。

---

## 9. 验收清单（全勾即完成 Cutover）

- [ ] 新二进制构建 + `--help` 正常
- [ ] config + token(.env) 就绪；`agent detect` / `project validate` 通过
- [ ] 旧 codex-bridge 已停，8765 让位
- [ ] 新 serve 启动，主机 health 200
- [ ] 容器侧 health / exec job / codex job 三连通过
- [ ] 根 `CLAUDE.md` 已更新（路径 + 鉴权头 + 接口）
- [ ] `tools/codex-bridge/` 已删除
- [ ] 旧引用清零（`grep -rn codex-bridge`）

> 完成后建议在主机侧跑一次 `go test -race ./...`（容器无 gcc 跑不了 race）作为最终质量背书。
