# Gofer Agent Session 捕获与恢复 设计方案

> 来源：beads `example-project-nnk`。
> 一句话：让 gofer 自动捕获每个 job 里底层 agent CLI 的 `session_id` 并入库，从而支持会话**检查 / 检索 / 恢复续接**。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-26 | claude | 初版设计，待审核 |

## 1. 背景

实测已确认：gofer **本身无 session 概念**，但 `exec` 透传 + 同一 runner（HOME/cwd 一致）下，底层 CLI 自己的会话恢复可跨 job 用——

```
job1: claude -p "记住 4242" --output-format json   → 输出含 session_id=67cc4d00-…
job2: claude --resume 67cc4d00-… -p "数字是多少?"   → 答 4242 ✓（同 session_id）
```

**痛点**：`session_id` 现在要人工从 job1 的 stdout JSON 里抠出来再手填进 job2。若 gofer 自动捕获并入库，即可做：`job show` 直接看到、按 session 检索/串链、`gofer job resume <job-id>` 自动续接。

## 2. 名词

- **session_id**：底层 agent CLI（claude / codex 等）维护的会话标识；会话状态存在该 runner 的文件系统（如主机 `~/.claude`），**不属于 gofer**。
- **捕获(capture)**：job 终态时由 gofer 从 agent 输出/产出中提取 session_id 落库。
- **恢复(resume)**：用已存的 session_id 在**同一 runner** 上发起一个带 agent 续接参数的新 job。

## 3. 范围

**做**：捕获 session_id（local + worker）→ 入库 → `show`/`list` 暴露 → `gofer job resume` 命令。
**不做**：gofer 不实现会话存储本身（那是底层 CLI 的事）；不做跨 runner 会话迁移；不改 agent CLI。

## 4. 已确认事项（前序讨论）

1. 捕获机制以 **A（按 agent 内置提取规则）为主 + C（`$GOFER_RESULT_DIR/session_id` 文件）兜底**。
2. resume **必须落同一 runner**（会话绑 runner 文件系统）；跨 runner 不可行，需校验/默认同 runner。
3. gofer 只记 id + 编排，不碰会话内容。

## 5. 架构与核心流程

### 5.1 捕获（挂在已有终态钩子 `captureOutcomes`）

`internal/job/outcomes.go:captureOutcomes(entry, req, res)` 是 job 终态时采集产出的 best-effort 钩子，天然是捕获点：

```
[local/peer job]  captureOutcomes 读 <result_dir>/stdout.log(store.StdoutFile)
                  → 按 agent.SessionCapture 规则提取 session_id
                  → 若 <result_dir>/session_id 文件存在(选项C)则优先用它
                  → entry.result.SessionID = <captured>

[worker job]      res.Outcome != nil（远端已采集回传）
                  → worker 侧在自己的 captureOutcomes 填 Outcome.SessionID
                  → host applyOutcome 时落 entry.result.SessionID
```

- best-effort：提取失败/无匹配 → SessionID 留空，**绝不**影响 job 终态（沿用 captureOutcomes 既有总闸）。
- 提取规则按 agent 配置（§7）。内置 claude/codex 默认规则，未知 agent 无规则则不捕获。

### 5.2 恢复 `gofer job resume <job-id> [-p "..."]`

```
1. 查 <job-id> 的 JobResult → 取 SessionID / Agent / Runner / WorkerID / Cwd / ProjectKey
2. SessionID 为空 → 报错"该 job 未捕获 session_id"
3. 构造新 job：复用同 project/agent/cwd；runner 默认 = 原 runner(同 worker_id)
   argv = 渲染 agent.SessionResumeArgs 模板(注入 {{session_id}}) + 新 prompt
4. --runner 若被显式改成异机 → 拒绝(或 --force 警告)：会话状态不在目标机
5. 提交为普通 job（异步/--sync 同 run）
```

> resume 本质是"用模板拼一条带 `--resume <id>` 的 exec 并提交"，编排在 commands 层，不进 job 业务层（G021）。

## 6. 数据模型

### 6.1 JobResult（`internal/job/model.go`）

```go
// SessionID 是底层 agent CLI 的会话标识(claude/codex 等)，job 终态时由
// captureOutcomes 从输出/产出捕获(best-effort)。空=未捕获/不支持。持久化到
// jobs.session_id，供 show/list/resume 使用。
SessionID string `json:"session_id,omitempty"`
```

### 6.2 jobstore（`internal/jobstore`）

- `store.go` schema 的 jobs 表加列 `session_id TEXT`；旧库经 `migrate()` 的 additive `ALTER TABLE jobs ADD COLUMN session_id TEXT` 自动补全。
- `jobs.go` selectCols 加 `COALESCE(session_id,'')`、insert/upsert 列同步（参照 tags_json/source 既有模式）。
- 新增检索：`ListQuery.Session`（`WHERE session_id = ?`），供 `job list --session <id>`。

### 6.3 runner.Outcome（`internal/runner/runner.go`，worker 远端回传）

```go
SessionID string `json:"session_id,omitempty"`  // worker/peer 侧捕获回传
```

### 6.4 AgentConfig（`internal/config/model.go`）

```go
// SessionCapture: 从 job stdout 提取 session_id 的规则(选项A)。空=不捕获。
// SessionResumeArgs: resume 时拼接的 argv 模板，{{session_id}} 占位(选项: resume 命令用)。
SessionCapture    string   `yaml:"session_capture"`     // 正则, 第1捕获组为 session_id
SessionResumeArgs []string `yaml:"session_resume_args"` // 如 ["--resume","{{session_id}}"]
```

内置默认（无显式配置时按 agent 名兜底）：

| agent | session_capture(正则) | session_resume_args |
|---|---|---|
| claude | `"session_id"\s*:\s*"([^"]+)"` | `["--resume","{{session_id}}"]` |
| codex | 待定（codex 输出格式需核） | 待定 |

> 选正则而非 jsonpath：format-agnostic、对纯文本/JSON 都适用，实现简单。

## 7. API / CLI

- `gofer job show <id>`：增 `session_id` 行（有值才显示）。
- `gofer job list`：增 `--session <id>` 过滤（列表可选加列，避免过宽，倾向只做过滤+show 展示）。
- `gofer job resume <job-id> [-p ...] [--runner ...]`：见 §5.2；注册到 job 子命令组。
- HTTP：session_id 随 `GET /v1/jobs/{id}` 返回（JobResult 已序列化即可）；list 端点加 `?session=`。

## 8. 安全

- session_id **不是密钥**，但能用于续接会话 → 视作中敏感标识：入库可，**不**打 INFO 日志正文（debug 才记），不外发。
- resume 同 runner 约束即安全边界：避免把会话 id 投到不持有该会话的机器（无效且可能泄漏 id 到他机日志）。
- 沿用 SR403：捕获过程不落 secret；正则只取 session_id 一段，不存整段 stdout。

## 9. 实施分期（建议）

- **P1**：JobResult.SessionID + jobstore 列/迁移/检索 + local 捕获（captureOutcomes + claude 内置规则）+ `job show` 展示。→ 最小可用（看得到）。
- **P2**：`gofer job resume` 命令 + AgentConfig.SessionResumeArgs + 同 runner 校验。→ 可恢复。
- **P3**：worker 远端捕获（Outcome.SessionID + worker 侧 captureOutcomes + wsproto）+ `list --session`。→ 容器 worker 会话也可续。
- codex 规则在 P1/P2 补（需先核 codex 输出/续接格式）。

## 10. 待确认

1. **list 是否加 session 列**：倾向只做 `--session` 过滤 + show 展示（列表已较宽）。是否需要列？
2. **resume 默认同步还是异步**：claude 类慢任务 `--sync` 客户端会超时（已实测）→ 倾向 resume 默认异步、提示用 `watch`。
3. **codex 的 capture 正则 / resume 参数**：需在主机实测 codex 的会话输出与续接 flag 后定（claude 已验证）。
4. **多 session 的 job**：一个 job 若多次调 agent 产生多个 session_id（少见）→ 现设计只存最后/首个匹配；是否需要多值？倾向首版只存 1 个（最后匹配）。
5. **是否同时支持 `-c`/continue（续最近会话）**：倾向只做显式 `--resume <id>`，不做隐式 continue（跨 job 不可靠）。

## 11. 结论

挂已有 `captureOutcomes` 钩子做 best-effort 捕获、additive 加一列、resume 用 agent 配置模板拼 argv——改动集中、与现有产出采集/迁移/远端回传模式同构，风险低。建议按 P1→P2→P3 分期，P1 即可让"看得到 session_id"落地。审核通过后出 plan 实施。
