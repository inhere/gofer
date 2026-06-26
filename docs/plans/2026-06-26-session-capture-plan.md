# Gofer Agent Session 捕获与恢复 实施计划

> 依据设计：[`design/2026-06-26-session-capture-design.md`](../design/2026-06-26-session-capture-design.md) v1.1（bd `hyy-ai-inspect-nnk` / roadmap E33）。
> 约束：`tools/gofer` 本地仓无 remote，提交即终点；遵守 G021/G022/G023。每个 task 独立 commit。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-26 | claude | 初版，P1-P3 分期 |

## 前置检查（PASS 才开工）

- [ ] `go build ./... && go vet ./... && go test ./...` 基线绿。
- [ ] 主机 claude（`--session-id`/`--resume` 已验）、codex（`session id:`/`exec resume` 已验）可用；容器 worker claude agent 已配（P3 用）。

## 总纲

| 阶段 | 目标 | 关键 task |
|---|---|---|
| **P1** | session_id 存储 + 获取（claude 注入 / codex 捕获）+ `job show` 展示 | T1.1 存储 / T1.2 agent 配置+uuid / T1.3 注入 / T1.4 捕获 / T1.5 show+测试 |
| **P2** | `gofer job resume <id>` 续接（同 runner） | T2.1 resume 模板 / T2.2 endpoint+CLI / T2.3 测试+真机冒烟 |
| **P3** | worker 远端获取 + `list --session` | T3.1 Outcome.SessionID+回传 / T3.2 worker 注入(host 生成 uuid 经 Forward) / T3.3 list 过滤+冒烟 |

---

## P1：存储 + 获取 + 展示

### T1.1 SessionID 字段贯通存储

- `internal/job/model.go` `JobResult` 加：
  ```go
  // SessionID 底层 agent CLI 会话标识(claude/codex)。注入(提交时 gofer 生成)或捕获(终态从输出)。
  // 空=无/未捕获。持久化 jobs.session_id，供 show/list/resume。
  SessionID string `json:"session_id,omitempty"`
  ```
- `internal/jobstore/jobs.go` `JobRecord` 加 `SessionID string`；`selectCols` 加 `COALESCE(session_id,'')`、`insertCols`/`UpsertJob` 占位 + `session_id=excluded.session_id`、`scanJob` 按列序加 `&r.SessionID`（参照 `Source`/`tags_json` 既有写法逐一对齐）。
- `internal/jobstore/store.go`：`schemaStmts` 的 jobs 表 DDL 加 `session_id TEXT`；`migrate()` 的 additive 列清单加 `"session_id TEXT"`（旧库自动 `ALTER TABLE jobs ADD COLUMN session_id TEXT`）。
- job↔record 转换（job 包 toRecord/fromRecord）补 `SessionID` 双向映射。
- **验收**：`go test ./internal/jobstore/... ./internal/job/...` 绿；新建 job 写入再读回 SessionID 往返一致（加 1 个 jobstore 往返单测）；旧库（无该列）Open 经 migrate 不报错。
- commit：`feat(gofer): JobResult.SessionID 贯通 jobstore(additive 加列+迁移)`。

### T1.2 AgentConfig session 字段 + 内置默认 + uuid helper

- `internal/config/model.go` `AgentConfig` 加：
  ```go
  SessionInject  []string `yaml:"session_inject"`  // 注入模式 argv 模板, {{session_id}} 占位(模式①)
  SessionCapture string   `yaml:"session_capture"` // 捕获正则, 第1组=session_id(模式②, 仅 inject 空时用)
  SessionResume  []string `yaml:"session_resume"`  // resume 整条 argv 模板, {{session_id}}/{{prompt}}
  ```
- `internal/agent`：`Vars` 加 `SessionID string`；`Render` 支持 `{{session_id}}`（与现有 `{{prompt}}` 等同写法）。
- 内置默认（`agent` 包按 agent 名兜底，仅当配置未显式设时）——**已实测**：
  | agent | SessionInject | SessionCapture | SessionResume |
  |---|---|---|---|
  | claude | `["--session-id","{{session_id}}"]` | — | `["--resume","{{session_id}}","-p","{{prompt}}"]` |
  | codex | — | `session id:\s*([0-9a-f-]+)` | `["exec","resume","{{session_id}}","{{prompt}}"]` |
- uuid helper（无现成库）：`internal/agent`（或 `internal/job`）加 `newUUID() string` —— `crypto/rand` 取 16 字节，置 v4 版本/variant 位，格式化 `xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx`（claude 要求合法 UUID）。
- **验收**：`Render(["--session-id","{{session_id}}"], Vars{SessionID:"u"})==["--session-id","u"]`；`newUUID()` 匹配 UUID 正则、两次不同；内置默认 Get 返回正确。单测覆盖。
- commit：`feat(gofer): AgentConfig session 字段 + {{session_id}} 渲染 + uuid + claude/codex 内置默认`。

### T1.3 注入（模式①，claude，提交时）

`internal/job/submit.go` 非 remote 分支（`:124-141`，`s.agents.Build` 之后、`runReq.Args=` 之前）：

```go
var sessionID string
if ac, ok := s.agents.Get(req.Agent); ok && len(ac.SessionInject) > 0 {
    sessionID = newUUID()
    resolved.Args = append(resolved.Args, agent.Render(ac.SessionInject, agent.Vars{SessionID: sessionID})...)
}
// 显式 req.SessionID(resume 走) 优先于注入
if req.SessionID != "" { sessionID = req.SessionID }
```

- `JobResult{...}` 构造处加 `SessionID: sessionID`。
- `JobRequest` 加 `SessionID string`（resume 用，T2）；非空时直接用、跳过注入/捕获。
- **验收**：提交 `-a claude` 的 job → `job show` 立即(无需等输出)有 session_id；exec/codex 此阶段不注入。冒烟：`gofer job run -a claude --prompt ... --runner local` 后 show 有 id，且该 id == argv 里 `--session-id` 值（看 rendered_command）。
- commit：`feat(gofer): claude 注入式 session_id(提交时生成 uuid + --session-id)`。

### T1.4 捕获（模式②，codex，终态）

`internal/job/outcomes.go` `captureOutcomes`：local 分支读完产出后，**仅当 `entry.result.SessionID==""`**（未注入）时尝试捕获：

```go
if entry.result.SessionID == "" {
    if ac, ok := s.agents.Get(entry.result.Agent); ok && ac.SessionCapture != "" {
        if sid := captureSessionID(filepath.Join(resultDir, store.StdoutFile), ac.SessionCapture); sid != "" {
            entry.result.SessionID = sid
        }
    }
    if entry.result.SessionID == "" { // 选项C 兜底
        if b, _ := os.ReadFile(filepath.Join(resultDir, "session_id")); len(b)>0 {
            entry.result.SessionID = strings.TrimSpace(string(b))
        }
    }
}
```

- `captureSessionID(path, re)`：编译正则(缓存)、读文件(限 maxResultJSONBytes 量级)、返回第1捕获组；任何失败返 ""（best-effort，沿用总闸）。
- **验收**：`-a codex --runner local` job 终态后 `job show` 有 session_id（== 输出头部 `session id:` 值）；正则不匹配/无输出 → 空、job 仍 done。单测：captureSessionID 命中/不命中。
- commit：`feat(gofer): codex 捕获式 session_id(captureOutcomes 正则 + 文件兜底)`。

### T1.5 job show 展示 + 测试

- `internal/commands/job.go` `runJobShow`：有值时加 `session_id: <v>` 行（参照现有字段打印）。
- `GET /v1/jobs/{id}` 已序列化 JobResult → 自带 session_id（omitempty）。
- **验收**：全量 `go test ./...` 绿 + `GOOS=windows go build ./...`；冒烟 claude(注入)/codex(捕获) 两条 `--runner local` job 的 show 都见 session_id。
- commit：`feat(gofer): job show 展示 session_id + P1 测试`。

---

## P2：`gofer job resume <id>` 续接

### T2.1 resume argv 渲染（复用 exec 路径）

resume = 用 `SessionResume` 模板拼整条 argv，**作为 exec job 在同 runner 提交**（new job 显式带 `SessionID=原 sid`，再可续）：

```
argv = [agentConfig.Command] + Render(SessionResume, {SessionID: sid, Prompt: newPrompt})
       claude → [claude, --resume, <sid>, -p, <prompt>]
       codex  → [codex, exec, resume, <sid>, <prompt>]
```

### T2.2 server endpoint + CLI

- `POST /v1/jobs/{id}/resume`（`internal/httpapi`，body `{prompt, runner?}`）→ handler：
  1. `GetJob(id)` → sid/agent/project/cwd/runner/workerID；`sid==""` → 400「未捕获 session_id」。
  2. `ac=agents.Get(agent)`；`len(ac.SessionResume)==0` → 400「agent 不支持 resume」。
  3. `body.runner` 若给且 ≠ 原 runner → 400「会话绑 <原runner>，不能跨 runner 续」（同 runner 约束，安全 §8）。
  4. 构造 `JobRequest{ProjectKey, Agent:"exec", Cmd: argv, Runner: 原, WorkerID: 原, Cwd: 原, CallerID, SessionID: sid}` → `Submit`。
  5. 返回 new job（其 SessionID=sid，链回同会话）。
- `internal/commands/job.go` 加 `resume` 子命令：`gofer job resume <job-id> [--prompt "..."] [--runner ...]`（`bindServerFlags`），调上面 endpoint；**默认异步**（设计 §10-2，claude 慢任务 sync 会超时），打印 new job id + 提示 `gofer job watch`。
- **验收**：`gofer job resume <claude-job> --prompt "..."` 起的 new job 跑通、其 session_id==原；跨 runner 报错；agent 无 resume 模板报错。
- commit：`feat(gofer): job resume 命令 + /v1/jobs/{id}/resume(同 runner 续接)`。

### T2.3 测试 + 真机冒烟

- 单测：handler 的 4 个错误分支（无 sid / 无模板 / 跨 runner / 未知 job）；argv 渲染正确。
- **真机冒烟**（--runner local）：
  - claude：job1 `-a claude --prompt "记住 X"` → `job resume <job1> --prompt "X 是多少"` → watch 到答 X，session_id 一致。
  - codex：同上（job1 codex 捕获 sid → resume）。
- commit：`test(gofer): session resume 单测 + 真机冒烟记录`。

---

## P3：worker 远端获取 + list 过滤

### T3.1 worker 捕获回传（codex 类）

- `internal/runner/runner.go` `Outcome` 加 `SessionID string json:"session_id,omitempty"`。
- worker 侧 `captureOutcomes` 把捕获到的 sid 填入回传 Outcome；host `applyOutcome` 落 `entry.result.SessionID`（参照 `Source` 既有回传链：`Outcome.Source`→`entry.result.Source`）。
- wsproto/peer 回传 payload 带上 session_id（随 Outcome 序列化，通常无需改帧结构，确认 Outcome 整体已透传）。

### T3.2 worker 注入（claude 类，host 生成 uuid 经 Forward）

- 注入需 host 知道 id：worker job 的 `req.SessionID` 由 **host 在 submit 时**生成（host 端若 agent 有 SessionInject——但 host 可能无该 worker 的 agent 定义）。**取向**：host 对 `remote && 目标 agent 视为注入型` 时生成 uuid 塞 `Forward`（新增 `Forward.SessionID`），worker 执行时 `req.SessionID` 即用它注入 + 落库；host 端 JobResult.SessionID 同步置该 uuid（无需等回传）。
- 简化兜底：若 host 无法判定 agent 注入能力，则 worker 走捕获(T3.1)、host 经 Outcome 拿回 —— 即 **claude-on-worker 退化为捕获**（需 claude agent 配 `--output-format json` 或 session_id 文件）。**决策**：P3 先做 T3.1 捕获回传（通用），claude-on-worker 注入(Forward.SessionID)作 P3 次步，按需要再上。
- **验收**：`-a codex`（若容器装）/ 或 `-a claude --output-format json` 的 `--runner w-docker-claude` job → host `job show` 有 session_id。

### T3.3 list --session + 收尾

- `internal/jobstore` `ListQuery` 加 `Session string` → `WHERE session_id = ?`；`internal/commands/job.go` `job list` 加 `--session <id>`；`GET /v1/jobs?session=`。
- 全量 `go test ./...` 绿 + windows build；真机冒烟容器 worker 会话获取。
- 文档：design 标 P1-P3 落地；记忆/roadmap E33 更新完成态。
- commit：`feat(gofer): worker 远端 session 获取 + job list --session + 收尾`。

---

## 进度跟进

> SUPMODE 实施完成 2026-06-26（编排+子 agent 实现+真机冒烟）。`tools/gofer` 本地仓无 remote，commit 即终点。

- [x] **P1** 存储+获取+show（T1.1–T1.5）— commits `e95bd3b`/`a9554fa`/`e2adc31`/`dc2c792`/`da6d575`
- [x] **P2** resume 命令（T2.1–T2.3）— commits `37a701d`/`9a46f26`/`6976640`
- [x] **P3** worker 远端 + list 过滤（T3.1/T3.3）— commits `400ab57`/`edaf828`

### 实施结果

- **全量 `go test ./...` 绿 + `GOOS=windows go build` 过**（每 task 子阶段背书）。
- **真机 E2E（容器本地 serve + 容器 claude）PASS**：claude job 自动注入 `--session-id`（job.session_id == 实际 argv 值）；`gofer job resume <job1>` → job2 记起上下文（"幸运数字 1234"）、session_id 同链。
- **T3.2 决策（落地变更）**：放弃 plan 原"host 经 Forward 预生成 uuid"——worker 执行 dispatch job 跑的就是 P1 的 Submit/captureOutcomes，worker 端 JobResult.SessionID 已被 P1 填好（claude 注入/codex 捕获），故 **worker 自报、经 `Outcome.SessionID` 回传 host**（T3.1）即覆盖两类 agent，更简单、无 host↔worker 配置耦合。peer 路一并透传。
- **后置（非本期阻断）**：① codex 真机 E2E 需 host（容器无 codex），逻辑由单测 + 早前手测覆盖；② **生产 worker 链路 E2E 需 host server + 容器 worker 双双重建为新二进制**（host server 重部署属主机侧，交 codex），本期以容器本地 serve+worker E2E 或单测背书 worker 回传链。

## 风险 / 注意

- 注入 uuid 必须合法 UUID（claude 校验）——uuid helper 要对。
- captureOutcomes 是 best-effort 总闸内，session 捕获失败绝不可影响 job 终态（在闸内、return 前）。
- resume 走 exec 路径：new job 的 `agent` 字段会显示 `exec`（执行真相），但 `session_id` 链回原会话；若要显示原 agent 名可后续加 resume 标记（非本期）。
- P3 worker 注入需 host 知 agent 能力——本期以"捕获回传"为通用主路径，注入回传作次步，避免 host/worker 配置耦合。
