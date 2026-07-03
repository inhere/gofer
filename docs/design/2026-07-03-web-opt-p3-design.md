# gofer Web 优化批次 P3 设计（Schedules 一次性/延迟 · Secret env · Workflow 创建）

> 归属 epic `kuwd`（wopt-p3）。P1/P2 为明确小改，见批次 plan；本文只覆盖 3 项需拍板的新能力。方向已确认：schedules 延迟+指定时间都做 / secret env 声明式 env_files / workflow yaml 编辑器页面。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-03 | inhere | 初稿，3 项决策 |

---

## D1 · Schedules 一次性/延迟任务

### 现状
仅支持 cron 重复触发：`ScheduleRecord`(`internal/jobstore/schedules.go:15-28`) 只有 `cron_expr`；建表 `internal/jobstore/store.go:221-236`；创建校验强制合法 cron（`schedule_handler.go:50` `NextCronRun`）；sweep 循环（`serve.go:560-664`）天然假设"执行完还有下一次"。

### 决策
新增触发类型 `schedule_type: cron|once`（默认 `cron`，兼容存量）。`once` 支持两种指定方式（二选一）：延迟 `delay_sec`（相对 now）或绝对时间 `run_at`（Unix 秒）。`once` 执行一次后自动禁用。

1. **模型**（`schedules.go`）：`ScheduleRecord` 加 `ScheduleType string`（默认 `"cron"`）。`once` 时 `cron_expr` 存空串，`next_run_at` 直接是目标时间戳。复用现有 `enabled` 做终态（触发后置 0），不新增 `fired` 列。
2. **建表/迁移**（`store.go`）：`CREATE TABLE` 加列 `schedule_type TEXT NOT NULL DEFAULT 'cron'`；对存量库补 `ALTER TABLE schedules ADD COLUMN schedule_type ...`（存在即跳过，仿现有加列迁移习惯；若无迁移框架则用 `PRAGMA table_info` 探测后 ALTER）。
3. **创建 API**（`schedule_handler.go:17-93`）：`createScheduleReq` 加 `Type string` + `DelaySec int64` + `RunAt int64`。分流：
   - `type=cron`（或缺省）：保持现逻辑，`cron` 必填、`NextCronRun`。
   - `type=once`：`cron` 必须空；`delay_sec>0` 或 `run_at>now` 二选一（都给则 `run_at` 优先，都不给报 400）；`next_run_at = run_at || now+delay_sec`，且必须 `> now`（含最小 grace，如 ≥ now+3s）。
4. **调度执行**（`serve.go:568-599` sweep）：到期项触发后，`cron` 走现有 `nextOf` 重排；`once` **不重排**，改为 `SetScheduleEnabled(id, 0)`（一次性关闭），避免下次 sweep 重复触发。`once` 的 `catch_up`/`miss_grace` 语义：即使 sweep 时已超过目标时间（miss_grace 外）仍**补触发一次**（一次性任务通常仍需执行），触发后即禁用。
5. **CLI**（`commands/schedule.go:106-151`）：`schedule add` 加 `--delay <dur>`（如 `30s`/`5m`）与 `--at <RFC3339|unix>`，二者与 `--cron` 三者互斥；给了 `--delay/--at` 即 `type=once`。
6. **前端**（`NewSchedule.vue` + `Schedules.vue` + `types.ts`）：NewSchedule 顶部加「触发类型」切换：`cron 定时` / `一次性`。一次性下二选一子表单：`延迟`(数字+单位 s/m/h) 或 `指定时间`(datetime-local picker → Unix 秒)。校验（`validationError` L150-181）按类型分支。`types.ts` `Schedule`/`CreateScheduleReq`(L569-594) 加 `type`/`delay_sec`/`run_at`。Schedules 列表展示类型徽标 + once 的目标时间/已触发态。

### 影响文件
`schedules.go`、`store.go`、`schedule_handler.go`、`serve.go`、`commands/schedule.go`、`NewSchedule.vue`、`Schedules.vue`、`types.ts`（8 文件）。

---

## D2 · Secret env 声明式 env_files（安全关键）

### 现状与约束
- job.env（`JobRequest.Env`）**会随 `request_json` 落库并经 API 返回**（`job/model.go:33-34` 明确注释），**不能放 secret**。
- 现只有进程级一次性 `.env`（`config/dotenv.go` → `os.Setenv`，全局，多 caller 并发会污染），不适合按 job 加载。
- env 组装链：`submit.go:141-189` `runReq.Env = goferJobEnv(mergeEnv(resolved.Env, req.Env), ...)`；优先级 `os.Environ < agent.env < job.env < gofer元数据`。

### 决策
新增一层「运行时注入、不落库」的 secret env，经 frontmatter 显式声明。

1. **声明**：job/schedule frontmatter 加 `env_files: [ ... ]`（字符串数组）。此字段**本身**（文件名列表，不敏感）可落库，值不落。
2. **路径解析规则**（防目录穿越，禁 `..` 与绝对路径）：
   - 以 `secret/` 开头（或裸文件名）→ 解析到 `<config-dir>/secret/<name>`（`config.ConfigDir()` + `EnvConfigDir`）。
   - 其余相对路径 → 解析到项目执行目录 `cfg.ExecPath(proj)` 下。
   - 任一项含 `..`/绝对路径/越界 → 拒绝提交（400），不静默忽略。
3. **加载函数**：新增 `LoadEnvFilesMap([]path) (map[string]string, error)`（仿 `dotenv.go` 解析，但**只返回 map、绝不 `os.Setenv`**）。文件缺失策略：声明了却找不到 → 报错（fail-fast，避免以为注入了其实没有）。
4. **注入点与不落库隔离**（核心）：在 `submit.go` 组装 runner 执行 env 时，把 secret map 合并进**传给 runner 的 env**（`runReq.Env` 的执行副本），但**不写回 `JobRequest.Env`**（落库字段）。即：`落库的 req.Env`（用户显式 env，不含 secret） 与 `执行用 env`（含 secret 文件值）分离。优先级：`os.Environ < env_files(secret) < agent.env < job.env < gofer元数据`（secret 作基础凭据，可被更具体的 job.env 覆盖，但通常 key 不冲突）。
5. **安全铁律**（实施必须逐条满足并加断言测试）：
   - secret 的 **值** 不进 `request_json`、不进任何 `/v1/jobs` API 响应、不进 stdout/stderr 落盘外的应用日志。
   - 应用日志（slog）只记录「加载了哪些文件、注入了哪些 **key 名**」，**不记 value**。
   - `env_files` 声明列表可在 job detail 展示（让用户知道用了哪些文件），但对应值永不回显。
6. **目录约定**：`<config-dir>/secret/`（不在仓库、天然隔离；G031：gofer 是独立工具，secret 机制通用不含业务）。schedule 触发的 job 继承其 `env_files`。

### 影响文件
新增 env 文件加载函数（`config/` 下，仿 `dotenv.go`）、`submit.go`（注入点+落库隔离）、`mdreq.go`（frontmatter 解析 `env_files` 已随 yaml unmarshal，若 `JobRequest` 加字段即可）、`job/model.go`（加 `EnvFiles []string`，标注不含 secret 值）、schedule 侧透传。安全断言测试。

### 待确认（非阻断，实施时定）
- `env_files` 是否也允许 caller/agent 级默认（当前设计仅 job/schedule 显式声明，最小面）。

---

## D3 · Workflow 创建 yaml 编辑器页面

### 现状
后端 `POST /v1/workflows` 吃 **JSON**（`workflow_handler.go:34` `c.BindJSON(&workflow.Spec)`）；CLI `wf run <file>` 读 yaml → 解析 Spec → `client.go:464` 序列化 **JSON** POST。前端 API/UI 空白。

### 决策
新增前端创建入口，MVP = yaml 文本编辑器（对齐 CLI `wf run` 体验）。

1. **前端 yaml→JSON**：NewWorkflow.vue 用 textarea/代码框收 yaml，提交前用 yaml parser 转 object 再 POST JSON `/v1/workflows`。**plan 阶段先查 `web/package.json` 是否已有 `js-yaml`/`yaml`**：有则直接用；无则二选一——(a) 加轻量 `js-yaml` 依赖；(b) 退化为「粘贴 JSON spec」输入（最省，但不如 yaml 友好）。倾向 (a)，与 CLI 一致。
2. **API 层**：`client.ts` 加 `submitWorkflow(spec: object): Promise<Workflow>` → POST `/v1/workflows`；`types.ts` 加 `WorkflowSpec`（可宽松 `Record<string,unknown>`，MVP 不强类型化整个 spec）。
3. **页面**：`NewWorkflow.vue` —— yaml 编辑区 + 内置示例模板（一个最小可跑的多步 workflow spec，含注释）+ 客户端 yaml 解析错误提示 + 提交。成功后跳 `/workflows/{id}` detail。
4. **路由/入口**：`router.ts` 加 `/workflows/new`；`App.vue` topbar（`.topbar-right` L197-203，紧邻现有 `+新建 job`/`+新建 cron`）加 `+新建 workflow`；`Workflows.vue` 空态/头部加创建按钮。

### 影响文件
`web/src/views/NewWorkflow.vue`(新建)、`web/src/router.ts`、`web/src/App.vue`、`web/src/views/Workflows.vue`、`web/src/api/client.ts`、`web/src/api/types.ts`、可能 `web/package.json`（js-yaml）。

---

## 实施顺序建议
D3（前端为主、风险低、对齐已有 API）→ D2（安全，需断言测试）→ D1（含 DB 迁移+调度语义，最重，单独一批+运行期冒烟）。各项独立提交。
