# M7 + M8 计划：runner 拼写全入口统一 / 唤醒用会话原始目录

分支 `m78`。不改内部规范名（`local`），不改 Web 显示（`server`）。

## M7 runner 名全入口规范化审计

规则（G043）：`server` 与 `local` 是同一个本机 runner。所有「接收 runner 名」与「比较已存 runner 标签」的地方，
必须经 `config.ResolveRunnerName(cfg, name)`（declare-wins：`runners:` 里真声明了 `server` 以声明为准）；
布尔判断用 `config.IsLocalRunnerName(cfg, name)`。服务端入口层统一用 `Server.resolveRunnerName`
（背后是 `job.Service.NormalizeRunner`）。

### 入口清单与结论

| 入口 | 结论 / 修正 |
| --- | --- |
| HTTP `POST /v1/jobs`（含 md frontmatter）、模板 runner 默认值 | 已规范化（`job.Submit` / `Validate` / `template`），矩阵测试覆盖 |
| `job resume` / 重跑 | 已规范化；**修**：比较源 job 的已存 runner 前先规范化（旧 `server` 行视为 `local`） |
| `GET /v1/jobs?runner=` 过滤 | 已规范化；**修**：SQL 过滤同时匹配 `server` 旧拼写，Go 层比较也先规范化 |
| workflow spec / 模板渲染后的 step runner、fan runner | **修**：`defaultStepRunners` 把 step 与 fan 的 runner 落库为规范名 |
| plan todo 创建 / 更新（HTTP、MCP local backend） | **修**：落库前规范化（`canonicalTodoRunner`）；派发时 `Submit` 仍会再规范化 |
| schedule 创建 | **修**：落库的 request.runner 规范化；触发时仍经 `Submit` |
| 会话 register | **修**：落库规范化；`GET`/list 视图读路径也规范化（旧 `server` 行） |
| 会话 heartbeat | 不带 runner，无需处理 |
| 会话 deliver / resume / takeover / 传话（`runnerKeyForSession`、`isServerLocalRunner`） | **修**：带 cfg 的 declare-wins；`sessionrelay.Service.IsServerLocalRunner` 取代裸字面量比较，host 通过 `SetRunnerResolver` 注入 |
| `GET /runners/{name}/messenger/agents` | **修**：改用 declare-wins（原先 `NormalizeRunnerName` 会把真声明的 `server` worker 误折叠为本机） |
| `workerProjectDir`（takeover 查 worker 路径） | **修**：同上 declare-wins |
| projects 写接口 `allowed_runners`（POST/PUT）、`project add` | **修**：校验认两种拼写（含 declare-wins），落库为规范名（HTTP 与 `Registry.Add` 两处） |
| `allowed_runners` 校验：`AllowsLocalRunner`、`checkRunnerAllowed` | **修**：改为带 cfg 的 declare-wins（`allowed_runners:[server]` 且 server 是真声明的 worker 时，不再误放行本机） |
| `project validate`（CLI doctor） | **修**：`allowed_runners: [server]` 不再报 not defined |
| config 写接口 | 无 runner 名字段（只有 `runner_probe` 间隔），无需处理 |
| CLI `--runner`（job run/ls/resume、plan、schedule、tool xfer、agent ls） | 一律原样发给 server，由服务端规范化；`schedule add` 默认值改用常量 |
| CLI `worker add` 写配置 | 写 `AllowedRunners: [local]` 常量，已是规范名 |
| MCP 工具参数 | submit 走 `job.Submit`；todo 走 `localBackend.canonicalTodoRunner`；client backend 走 HTTP |
| hook 上报（`resolveHookRunner` → `server`、`GOFER_HOOK_RUNNER`） | 保持 CLI 拼写，服务端 register 规范化 |
| worker 协议携带的 runner 字段 | server → worker 的派发全部来自已规范化的 `JobRequest`，无输入边界 |
| xfer（HTTP meta、CLI） | 已用 `NormalizeRunnerName`；xfer 的 runner 是 **worker id**，不走 `runners:` 声明，已知限制：worker id 字面叫 `server` 的情况不支持 |
| 内部比较（`== "local"` / `== BuiltinLocalRunner` / `IsBuiltinLocalRunnerName`） | 全部核对；`messenger.Manager` 两处改为先规范化；其余保留并进守护测试白名单（比较对象均已规范化） |
| `serve` 的 `reconcile_runner` 默认值 | 改用常量；值经 `Submit` 规范化 |
| Web 前端 | 不改显示；`job.runner !== 'local'` 的比较对象是后端已规范化的值 |

### 存量数据方案：读路径规范化（不做迁移）

选读路径规范化，理由：
1. runner 标签散在 `agent_sessions.runner`、`jobs.runner`、`plan_todos.runner` 与 `schedules.request_json` / `workflows.spec_json` 的 JSON 里，
   迁移要改 JSON 内部，且 declare-wins 取决于**运行时**配置——operator 之后若声明了名为 `server` 的 runner，
   已迁移的行无法还原，读路径规范化则始终以当前配置为准。
2. 所有消费点本来就经 `Submit` / 比较函数，读路径规范化覆盖了「旧行 + 新拼写」两种来源，且不需要停机/迁移幂等性保证。
3. 新写入统一落规范名，旧行只会随时间减少。

落实：会话视图与 relay 比较走 `resolveRunnerName`；jobs 过滤 SQL 兼容 `server`；resume 比较前规范化；
todo / schedule / workflow 在 `Submit` 前再规范化（已有）。

### 测试

- `internal/httpapi/runner_spelling_matrix_test.go`：对 job 提交 / job 列表过滤 / todo 创建+更新 / schedule / 会话 register /
  项目 allowed_runners / workflow spec 各用 `server`、`local` 走一遍，断言落库一致；另有旧 `server` 会话行、旧 `server` job 行、declare-wins 场景。
- `internal/config/runner_literal_guard_test.go`：AST 扫描非测试源码中「runner 相关表达式与 `"local"`/`"server"`/规范名常量的 `==`/`!=`/switch case」，
  白名单现有合法处（均附理由），新增写法会失败。
- `internal/config/runner_resolve_test.go`、`internal/project/registry_runner_test.go`：声明优先的单元测试。

## M8 唤醒使用会话的原始目录

### 现状问题

会话登记 cwd 只在 register 时记录（SessionStart 可能在会话中途再次 register，那时 cwd 可能是临时 worktree），心跳不更新；
唤醒（takeover / resume）用登记 cwd，可能落到已删除目录。`claude --resume <id>` 按**会话启动时 cwd** 编码后的目录
`~/.claude/projects/<编码>/<id>.jsonl` 查找，而登记的 `transcript_path` 正是该文件，可反推原始目录。

### 唤醒 cwd 决策（`PlanTakeover` 返回 `Cwd` 与 `CwdReason`，中文说明）

1. **transcript 验证目录**：候选 = 登记 cwd 及其各级父目录 + 项目根（执行机视角）；取「编码后等于 transcript 所在目录名」且
   在执行机上存在的那个。编码规则：除字母数字外的字符（`/` `\` `:` `.` `_` 空格等）都替换为 `-`；因为反推有歧义，
   **只做验证**（候选编码后比较），不做解码。
2. 登记 cwd（在执行机上存在时）。
3. 项目根（执行机视角）。

执行机视角：server 本机会话直接 `os.Stat`；worker 会话无法远程判断存在性，改用「worker 心跳上报的项目路径 + 字符串推断」，
候选与 transcript 目录名匹配本身即是证据，不做存在性判断，说明里写清「未在 worker 上核对目录是否存在」。

### 心跳 `last_cwd`

hook 心跳携带当前 cwd，服务端存 `agent_sessions.last_cwd`（additive 列），仅用于显示（Sessions 列表 / 抽屉「当前目录」），
不改变登记 cwd 语义，不参与唤醒决策。

### 测试

transcript 目录名验证正反例（Windows 盘符、含 `.` 的目录、容器 `/d/work` 路径、编码歧义）、已删除 worktree 回落到原始目录、
心跳写入 `last_cwd`。

## 质量门 / 冒烟

`gofmt -l`、`go build ./...`、`GOOS=windows go build ./...`、`go vet ./...`、`go test ./... -count=1`；web 有改动时按任务书在 scratch 目录跑
`vue-tsc` / `vitest` / `vite build`。临时 serve 冒烟（临时配置目录、随机端口），两种拼写走通各入口；造「登记 cwd 已删除、transcript 指向原始目录」的会话验证 takeover-plan。

## G045

`skills/gofer-usage/` 补：runner 两种拼写在所有入口等价、declare-wins；唤醒目录选择规则；`last_cwd`。
