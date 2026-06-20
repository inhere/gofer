# Gofer 提交与调度增强 — 设计方案

> 一句话：把"job 怎么提交、怎么选到执行机、能不能同步拿结果"这条链路一次性增强——
> ① exec 同步等待返回 ② md+yaml 提交格式 ③ 按 worker labels 自动调度 ④ 控制台提交表单。
> 四项高内聚（都改 `JobRequest` / submit handler / runner 选择），合一份方案分阶段落地。
> **多 hub HA 不在本方案**（独立大 Epic，见 [`../TODO.md`](../TODO.md) §大型 Epic）。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-20 | Claude | 初版：范围/分期 + 现状梳理 + 四模块详设 + 数据模型/API + 待确认决策点。bd `example-project-myi`。 |

---

## 1. 概览

### 1.1 背景

当前提交链路是"异步 + 显式路由"的最小形态（事实见 §4）：

- **提交载荷只有 JSON**：`POST /v1/jobs` 收 `JobRequest`（`internal/job/model.go:7`）。
- **只能异步**：`Submit` 立即返回 `id`+`queued`，CLI `--wait` 是**客户端轮询**（300ms/10min，`commands/job.go:194`），跑个 `go version` 这种秒级命令也要轮询往返。
- **执行机只能显式指定**：runner=worker 时**必须**传 `worker_id`，且该 worker runner 在**配置期就绑定**单个 worker（`assemble.go:70` `workerrunner.New(name, rc.WorkerID, hub)`）。worker 已上报 `labels`（`/v1/runners` 可见，`wshub/registry.go:257`）但**无任何按 label 选机**的代码。
- **控制台不能发起 job**：只能看板/详情观测，提交要走 CLI/HTTP。

### 1.2 目标

| 编号 | 目标 | 价值 |
|---|---|---|
| G1 | exec/任意 job 可**同步等待**到终态再返回 | 快速命令一次往返拿到 exit_code/输出 |
| G2 | 支持 **md+yaml 提交格式**（yaml frontmatter 定参数 + 正文即 prompt） | 任务描述可读、可文件化、可版本管理 |
| G3 | runner=worker 时可**按 labels 自动选机**（免手填 worker_id） | 多 worker 时按能力/负载自动落机 |
| G4 | **控制台提交表单**（选 项目/agent/runner/worker_id 或 labels）发起 job | 不依赖 CLI 即可派活 |

### 1.3 非目标

- **不做** 多 hub HA / 跨 hub 接管 / 选主（独立 Epic）。
- **不做** 跨 worker 公平调度 / 优先级队列（超出"按 label 选一台"，留 TODO）。
- **不做** "无可用 worker 时排队等其上线"——本期无可用 worker 直接拒绝（§6.3）。

---

## 2. 名词

- **labels selector**：提交时给出的一组**要求标签**，worker 的 labels 需**全包含**（AND 匹配）才算合格。
- **同步提交（sync submit）**：提交后服务端阻塞等待该 job 到终态再返回完整 `JobResult`，受**服务端等待上限**约束；超限则退回异步（返回 id）。
- **frontmatter**：md 文档开头 `---` 包裹的 yaml 区块，承载 `JobRequest` 参数；其后正文为任务描述（→ `prompt`）。

---

## 3. 范围与分期

| 阶段 | 内容 | 依赖 | 价值/风险 |
|---|---|---|---|
| **P1** | G1 exec 同步等待 + G2 md+yaml 提交 | 无（复用 `Service.Wait`、`goccy/go-yaml`） | 改动小、最快见效；纯提交侧，低风险 |
| **P2** | G3 标签自动调度 | P1 的 `JobRequest.worker_labels` 字段 | 含 worker runner 解绑重构，风险中 |
| **P3** | G4 控制台提交表单 | P1+P2（消费同步/labels） | 纯前端 + 一个 meta 接口；UI 收口 |

> 建议顺序 P1 → P2 → P3，每阶段绿灯即提交（SR1202）。P1 两项独立可并行。

---

## 4. 现状（提交链路事实，带 file:line）

| 环节 | 位置 | 现状 |
|---|---|---|
| 请求结构 `JobRequest` | `internal/job/model.go:7` | 有 project_key/agent/runner/prompt/cmd/cwd/timeout_sec/title/worker_id/caller_id/request_id；**无 labels 字段** |
| HTTP 入口 | `internal/httpapi/job_handler.go:23` `handleCreateJob` | `BindJSON` → 覆盖 `CallerID` → `jobs.Submit(req)` → 立即返回初始 `JobResult` |
| 提交核心 | `internal/job/service.go:142` `Submit` | 校验→幂等(C5)→cwd SafeJoin→建 resultDir→选 runner `s.runners[req.Runner]`→建 entry(queued)→落库→`go execute(...)` |
| worker 校验 | `internal/job/service.go:564` | runner=worker 时 `worker_id` 必填且须在 `cfg.Server.Workers` 内 |
| worker runner | `internal/runner/worker/runner.go:105` | dispatch 用 runner **构造期绑定**的 `r.workerID`（非 req 动态） |
| runner 装配 | `internal/commands/assemble.go:51` `buildCore` | local 内置；按 `cfg.Runners` 建 peer-http/worker；worker runner 绑定 `rc.WorkerID` |
| 同步等待原语 | `internal/job/cancel.go:71` `Service.Wait(id)` | **已存在**：`<-entry.done` 阻塞；已驱逐终态 job 回落查 DB。非轮询 |
| CLI `--wait` | `internal/commands/job.go:194` `waitTerminal` | **客户端轮询** 300ms/10min |
| worker labels | `internal/wshub/registry.go:257` | register 帧上报→内存 `meta.Labels`→`/v1/runners` 读出；**无选机逻辑** |
| exec/cli-agent 分支 | `internal/agent/adapter.go:34` `Build` | exec 用 `cmd` argv；cli-agent 用 `prompt`+模板渲染 |
| yaml 库 | `go.mod` | `github.com/goccy/go-yaml v1.19.2` 已引入 |

> 关键复用点：**G1 同步等待无需新造轮子**——`Service.Wait` 已是 channel 阻塞 + DB 回落，只差一个 HTTP 层入口与服务端超时封顶。

---

## 5. 架构与关键改动

四项都收敛到**提交侧三处**，执行/读路径不动（runner 抽象 + 镜像机制对下游透明）：

```txt
                         ┌─ (G2) md+yaml ──┐
client ─ POST /v1/jobs ─→│  content-type 分支：解析 frontmatter→JobRequest, body→prompt
  (JSON 或 text/markdown) └──────┬──────────┘
                                 ▼
                    handleCreateJob ──(G1 sync?)──→ Submit + Service.Wait(封顶) ─→ 终态 JobResult
                                 │                                   └(超时)→ 退回异步 id(202)
                                 ▼
                            Submit 内：
                    ┌─ (G3) runner=worker 且未给 worker_id 且给了 worker_labels
                    │     → 从 hub 注册表快照按 labels+负载选出 worker_id → 注入 req.WorkerID
                    └─ worker runner 改为按 req.WorkerID 动态派发（解绑配置期单 worker）

control plane(web) ─ GET /v1/meta(项目/agent/runner/worker 选项) → (G4) 提交表单 → POST /v1/jobs
```

**改动面**（按风险）：
- 低：handler content-type 分支（G2）、handler sync 分支调 `Service.Wait`（G1）、`JobResult` 不变。
- 中：`JobRequest` 加 `worker_labels`（G3）；worker runner 解绑（动态 worker_id）；Submit 内加"按 labels 选 worker_id"一步（G3）。
- 低：新增只读 `GET /v1/meta`（G4 表单选项）；前端提交表单组件。

---

## 6. 模块详设

### 6.1 G1 — exec/任意 job 同步等待

**入口形态（决策见 §10-D1，推荐 A）**：`POST /v1/jobs` 增可选 `sync`（body 字段）或 `?wait=1`，并可带 `wait_timeout_sec`。

服务端流程（`handleCreateJob` 内，Submit 成功后）：
```go
res, err := s.jobs.Submit(req)            // 既有：异步起跑，res.ID 已分配
if err != nil { ... }
if syncRequested {
    cap := clampWaitTimeout(waitTimeoutSec) // 封顶（§10-D1，默认 30s、上限 60s）
    final, ok := s.jobs.WaitFor(res.ID, cap) // 复用 Service.Wait + 上下文超时
    if ok && isTerminal(final.Status) {
        c.JSON(200, final); return
    }
    // 超过服务端上限仍未终态：退回异步语义，让客户端转轮询/SSE
    c.SetHeader("X-Gofer-Async", "1")
    c.JSON(202, res); return            // 202 + 初始 res(含 id)
}
c.JSON(200, res)                         // 既有异步路径
```
- 新增 `Service.WaitFor(id, timeout)`：在现有 `Wait` 外包一层 `select { case <-entry.done: ; case <-time.After(timeout): }`，超时返回 `ok=false`（job 仍在后台继续，不取消）。
- **CLI**：`job run` 加 `--sync`（区别于现有客户端轮询 `--wait`）；`--sync` 走服务端等待，命中 202 时自动回落到 `waitTerminal` 轮询（平滑）。
- **语义**：sync 只是"等结果"，**不改变 job 生命周期**；超时不杀 job（与 timeout_sec 业务超时正交）。

**适用范围（§10-D1）**：推荐对**任意 agent**开放（不限 exec），但默认上限短（面向快命令）。

### 6.2 G2 — md+yaml 提交格式

**传输（决策 §10-D2，推荐按 Content-Type 分支）**：`POST /v1/jobs` 收 `Content-Type: text/markdown`（或 `application/x-gofer-md`）时走 md 解析；否则现有 JSON 路径不变。

**格式**：
```markdown
---
project_key: my-project1
agent: codex
runner: worker
worker_labels: [gpu, linux]      # 或 worker_id: w-01
timeout_sec: 600
title: 巡检脚本生成
sync: false
---
帮我在 scripts/ 下生成一个批量巡检脚本，要求：
- 读取 config.yaml 的门店列表
- ...（正文即 prompt）
```

解析（新增 `internal/httpapi/mdreq.go`）：
```go
// 首个 '---\n' 到次个 '\n---' 之间为 yaml frontmatter，其余为正文。
func parseMarkdownRequest(body []byte) (job.JobRequest, error) {
    fm, rest := splitFrontmatter(body)        // 无 frontmatter → 报 400
    var req job.JobRequest
    if err := yaml.Unmarshal(fm, &req); err != nil { return req, err }
    req.Prompt = strings.TrimSpace(string(rest))
    return req, nil
}
```
- **复用 JobRequest 的 tag**：给 `JobRequest` 字段补 `yaml:"..."` tag（与 json 同名 snake_case），`goccy/go-yaml` 直接反序列化。
- **正文落点（§10-D2）**：正文 → `prompt`，**面向 cli-agent**（codex/claude）。exec 类仍走 JSON/`cmd`（argv 语义不适合 md 正文）；若 md 指定 `agent=exec` 则报 400 提示用 JSON。
- **CLI**：`job run -f task.md`（读文件、按 md 提交）。
- **安全**：frontmatter 大小限幅；正文长度上限；解析失败 400 不可恢复。

### 6.3 G3 — 按 worker labels 自动调度

**数据**：`JobRequest` 加 `WorkerLabels []string` `json/yaml:"worker_labels"`。

**worker runner 解绑（关键重构）**：现 `workerrunner.New(name, workerID, hub)` 绑死单 worker。改为：
- worker runner 不再持有固定 workerID（或仅作默认）；`runner.Request` 透传 job 的**已解析 worker_id**，dispatch 用它（`runner/worker/runner.go:105` 的 `r.workerID` → `req.WorkerID`）。
- 这样"一个 worker runner 名"可服务**多台 worker**，选机交给 Submit。

**选机（Submit 内，runner=worker 时）**：
```
若 req.WorkerID != ""        → 显式路由（现状，worker_id 必须已连接/在册）
否则若 len(worker_labels)>0  → 从 hub 注册表快照过滤：
       connected && labels ⊇ worker_labels && 健康(心跳新鲜)
       候选排序：in_flight 升序 → heartbeat_age 升序（§10-D3 策略）
       取首个 → 注入 req.WorkerID
否则                          → 400（worker runner 需 worker_id 或 worker_labels 之一）
无合格候选                    → 503 "no eligible worker for labels [...]"（§10-D3：拒绝不排队）
```
- 选机输入全部现成：`wshub/registry.go` 快照含 `Labels` / `InFlight` / `HeartbeatAge` / connected。
- 选中结果写回 `JobResult.worker_id`（已有字段），看板/详情可见"实际落机"。
- **显式 worker_id 优先**：两者都给时 worker_id 胜，labels 忽略（或报冲突，§10-D3 定）。

### 6.4 G4 — 控制台提交表单

- **后端**：新增只读 `GET /v1/meta`（authed），聚合表单选项：项目列表（含每项目 allowed_agents/allowed_runners）、agent 列表、runner 列表、worker 列表（key+labels+connected，复用 `/v1/runners` 数据）。避免前端多次拼装。
- **前端**：`web/src/views/` 加提交入口（或 Board 上一个"+ 新建 job"抽屉/弹层）：
  - 选 project → 联动可选 agent/runner；
  - runner=worker → 可选"显式选 worker_id"或"填 worker_labels 自动选"；
  - cli-agent 显示 prompt 文本域（支持贴 md）；exec 显示 command 输入；
  - 可选 `sync` 勾选（小命令即时看结果）；
  - 提交走 `POST /v1/jobs`，成功跳详情页。
- 复用既有 token/SSE/状态色板与浅色主题（本轮已落地）。

---

## 7. 数据模型

**`JobRequest`（`internal/job/model.go`）新增一个字段 + 补 yaml tag**：
```go
type JobRequest struct {
    ProjectKey   string   `json:"project_key"   yaml:"project_key"`
    Agent        string   `json:"agent"         yaml:"agent"`
    Runner       string   `json:"runner"        yaml:"runner"`
    Prompt       string   `json:"prompt,omitempty"        yaml:"prompt,omitempty"`
    Cmd          []string `json:"cmd,omitempty"           yaml:"cmd,omitempty"`
    Cwd          string   `json:"cwd,omitempty"           yaml:"cwd,omitempty"`
    TimeoutSec   int      `json:"timeout_sec,omitempty"   yaml:"timeout_sec,omitempty"`
    Title        string   `json:"title,omitempty"         yaml:"title,omitempty"`
    WorkerID     string   `json:"worker_id,omitempty"     yaml:"worker_id,omitempty"`
    WorkerLabels []string `json:"worker_labels,omitempty" yaml:"worker_labels,omitempty"` // 新增(G3)
    Sync         bool     `json:"sync,omitempty"          yaml:"sync,omitempty"`          // 新增(G1，亦可仅 query)
    WaitTimeout  int      `json:"wait_timeout_sec,omitempty" yaml:"wait_timeout_sec,omitempty"` // 新增(G1)
    CallerID     string   `json:"caller_id,omitempty"     yaml:"-"` // 服务端覆盖，不接受外部
    RequestID    string   `json:"request_id,omitempty"    yaml:"request_id,omitempty"`
}
```
- **DB schema**：`worker_labels` 不必单独建列；落库的是请求 JSON 快照（既有 `marshal request`，`service.go:199`）+ 解析后的 `worker_id` 已有列。**无 SQLite 迁移**。
- `caller_id` yaml 标 `-`：防 md frontmatter 伪造调用方（C2 口径，服务端仍覆盖）。

---

## 8. API

| 方法 | 路径 | 变更 | 说明 |
|---|---|---|---|
| POST | `/v1/jobs` | 改 | ① 新增 `Content-Type: text/markdown` 分支（G2）；② body `sync` 或 `?wait=1`+`wait_timeout_sec` → 同步等待（G1），命中超时返回 `202`+id；③ body 可带 `worker_labels`（G3） |
| GET | `/v1/meta` | 新增 | 只读、authed：项目/agent/runner/worker 选项聚合（G4 表单） |
| — | `/v1/runners` | 复用 | worker labels/connected/in-flight 已有，G3 选机与 G4 表单复用 |

响应：同步命中终态 → `200` + 完整 `JobResult`；同步超服务端上限 → `202` + 初始 `JobResult`(含 id) + 头 `X-Gofer-Async:1`；异步（默认）→ `200` + 初始 `JobResult`（现状不变）。

---

## 9. 安全

- md frontmatter **不得**覆盖 `caller_id`（yaml tag `-` + 服务端覆盖）；frontmatter/正文大小限幅防滥用。
- 同步等待服务端**封顶**（§10-D1），避免连接长占用与慢 job 拖垮连接池（呼应 SR603 入口时长约束的精神：sync 仅给快命令）。
- labels 选机只在**已连接且健康**的 worker 中选；无合格候选明确 503，不静默落到错误机器。
- `GET /v1/meta` 与 `/v1/jobs` 同 `/inner` 网关 + bearer/ caller token 口径（SR101/C2），不放开匿名。
- 选中的真实 `worker_id` 落 `JobResult` 审计可见。

---

## 10. 待确认事项（决策点，附推荐）

- **D1（G1 同步语义）**：
  - 入口：**(A 推荐)** `POST /v1/jobs` 加 `sync`/`?wait=1` 同步返回；(B) 另开 `GET /v1/jobs/{id}/wait` 长轮询。A 一次往返更顺手。
  - 服务端上限：推荐默认 `30s`、硬上限 `60s`，超时退 `202`+id。是否接受？
  - 适用范围：推荐**任意 agent**可 sync（非仅 exec），默认上限短。或限定 exec？
- **D2（G2 格式）**：
  - 传输：**(A 推荐)** 按 `Content-Type: text/markdown` 分支；(B) JSON 里塞一个 `markdown` 字段。A 更干净。
  - 正文落点：推荐正文→`prompt`、**仅面向 cli-agent**；exec 走 JSON/argv（md 指定 exec 报 400）。认可？
- **D3（G3 调度）**：
  - 匹配：推荐 `worker_labels` **全包含 AND**；是否需要更复杂选择器（暂不做）？
  - 候选排序：推荐 `in_flight↑ → heartbeat_age↑`（负载优先）。或随机/首个？
  - 无合格候选：推荐 **503 拒绝**（不排队等上线）。认可？
  - worker_id 与 labels 同给：推荐 **worker_id 胜、labels 忽略**；或报 400 冲突？
- **D4（G3 重构边界）**：worker runner 解绑为"按 req.worker_id 动态派发"会影响现有"一个 runner 绑一个 worker"的配置语义——是否保留 `rc.WorkerID` 作为**默认/兜底**（不传 worker_id 且无 labels 时回落该默认），以兼容现网配置？推荐保留兜底。
- **D5（G4 范围）**：提交表单首版是否只覆盖 cli-agent + exec 主路径，labels 选机给"可选高级项"？推荐是。

---

## 11. 结论

四项增强同属"提交与路由"一条轴、共改 `JobRequest`/handler/Submit 三处，合一份方案分 P1→P2→P3 落地最省、风险可控；**最大复用**已有 `Service.Wait`（同步）、`goccy/go-yaml`（md）、`/v1/runners` 注册表（选机）。唯一较重的重构是 G3 的"worker runner 解绑 + Submit 内选机"，单独成 P2 隔离风险。多 hub HA 不并入。

**下一步**：本设计审核（重点过 §10 决策点）→ 通过后按 SR1105 出分阶段 `plan`（P1/P2/P3，细到字段/函数/验收）→ 实施。
