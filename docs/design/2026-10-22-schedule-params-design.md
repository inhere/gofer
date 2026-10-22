# 定时任务参数与编辑 设计

> issue: gofer-15jj ｜ 状态：0.2 已批准（2026-10-22，按推荐）｜ 2026-10-22

## 背景

定时任务现在只能原样提交创建时存下的 job 请求：
- web 的「立即执行」（`POST /v1/schedules/{id}/run-now`）不能临时改任何东西；
- 定时任务也没有编辑接口，改 cron 时间、命令都要删掉重建。

典型场景：一个清理任务平时每周按默认值「只预演」，人想临时「真删一次」或「改成只清 3 天前」。
现在只能另建一个任务，或者到主机上手工跑脚本。

## 目标

1. 定时任务可以**声明参数**：名字、默认值、说明、类型（字符串 / 整数 / 开关 / 选项）。
2. 参数以**环境变量**传给 job：定时触发和 webhook 触发用默认值；「立即执行」时可以改。
3. web「立即执行」：有参数时弹出按声明生成的表单；CLI `gofer schedule run <id> --param K=V`。
4. 定时任务可以**编辑**：cron / 一次性时间、标题、参数声明（含默认值）、启停。

## 不做

- 参数不替换进命令行 argv、也不替换进 prompt：只走环境变量。脚本自己读环境变量，避免拼接命令带来的注入问题。prompt 替换以后有需要再加。
- webhook 触发不接受参数（外部调用方只有 trigger_token，不应能改变行为）。
- 不做参数的密钥类型：参数值会出现在 job 详情里，不要拿它传密码。

## 设计

### D1 参数声明

```yaml
params:
  - name: CLEAN_DAYS        # 环境变量名：^[A-Z_][A-Z0-9_]*$，不能以 GOFER_ 开头
    type: int               # string（默认）| int | bool | choice
    default: "7"
    desc: 保留天数（删最新文件早于 N 天的条目）
  - name: CLEAN_APPLY
    type: bool              # 值为 "1" / "0"
    default: "0"
    desc: 打开即真删；关闭只预演
  - name: CLEAN_ONLY
    type: choice
    choices: ["", "inhere-tools\\gofer", "inhere-tools\\suga"]
    default: ""
    desc: 只清某个仓库的 tmp（空 = 全部）
```

- 存储：schedules 表新增 `params_json` 列（additive 迁移，旧行为空）。
- 最多 20 个参数；值最长 1024 字符，不允许换行和 NUL。int 和 bool 按类型校验，choice 必须是列出的值之一。
- 创建或编辑时校验声明本身：名字合法、不重复、默认值符合类型。

### D2 传给 job

- 提交时，按「存下的请求 env < 参数默认值 < 本次立即执行给的值」叠加进 `JobRequest.Env`。
- job 详情的 env 显示已有脱敏规则，参数值照常显示（不是密钥）。
- 立即执行时给了**未声明**的键，返回 400 并列出可用参数，防止借立即执行注入任意环境变量。

### D3 立即执行

- HTTP：`POST /v1/schedules/{id}/run-now` 接受可选 body `{"params": {"CLEAN_APPLY": "1"}}`；无 body 时等同全部用默认值。权限不变（`job.submit`，他人的任务另需 `schedule.manage.others`）。
- 本次覆盖的参数记进 schedule 事件（`schedule.run_now`，带覆盖的键值），便于事后查「谁什么时候真删过」。
- CLI：`gofer schedule run <id> [--param K=V ...]`。

### D4 编辑

- HTTP：`PATCH /v1/schedules/{id}`，可改 `cron` / `at` / `title` / `params` / `enabled` / `timeout_sec`；**不能**改命令、agent、runner、project（这些属于「换了一个任务」，仍然删掉重建，避免权限检查在编辑路径上被绕开）。改 cron 或时间后重新计算 next_run_at。权限同删除（自己的任务，或 `schedule.manage.others`）。
- CLI：`gofer schedule edit <id> [--cron …] [--title …] [--param-def …]`。
- 路由按 G056 登记。

### D5 web

- 定时任务列表：有参数的行，「立即执行」按钮旁显示「⚙」。点击「立即执行」弹出表单：
  - 按声明生成控件：int 用数字框，bool 用开关，choice 用下拉，string 用文本框；
  - 预填默认值，每项下面显示 desc；
  - 底部预览「本次将设置的环境变量」，确认后提交。
  - 没有参数的任务保持现在的一键执行。
- 定时任务详情 / 编辑：cron 表达式（附下次运行时间预览）、标题、参数声明的增删改（一行一个参数）。

### D6 CLI 声明参数

`gofer schedule add` / `edit` 用可重复的 `--param-def`，格式 `NAME[:type]=default[#desc]`，例如：

```bash
gofer schedule add --name "主机 tmp 清理" -p my-tools-dev -a exec --cron "0 5 * * 0" \
  --param-def 'CLEAN_DAYS:int=7#保留天数' --param-def 'CLEAN_APPLY:bool=0#打开即真删' \
  -- powershell -NoProfile -ExecutionPolicy Bypass -File clean-tmp.ps1
```

choice 的可选值较长，用 `--params-file <yaml>` 给整段声明，格式同 D1。

## 已定（2026-10-22 用户批准）

- D4 编辑放进本批：用户的「调整清理时间」就是改 cron，删掉重建会丢失事件历史和 webhook token。
- D2 叠加顺序：参数 > 存下的 env，同名时参数生效。

## 实施记录（2026-10-22）

按 D1–D6 全部落地：

- **声明与校验**：`internal/job/schedule_params.go`（`ScheduleParam`、`Encode/DecodeScheduleParams`、`ValidateScheduleParams`、`ApplyScheduleParams`、`ParseParamDef` / `ParamDefsToParams`、`ParseParamBindings`）。上限常量 `MaxScheduleParams=20` / `MaxScheduleParamValueLen=1024`，`GOFER_` 前缀拒绝。
- **存储**：`schedules.params_json`（additive，`migrateSchedules` 补列，旧行读回空）；`jobstore.ScheduleUpdate` + `UpdateSchedule` 一次写入可改字段。
- **提交叠加**：`httpapi.handleRunSchedule`（run-now）、`serve.submitDueSchedule`（cron）、`httpapi.handleScheduleTrigger`（webhook）三处都经 `job.ApplyScheduleParams`，因此 cron 与 webhook 只用默认值，run-now 可覆盖。
- **HTTP**：`POST /v1/schedules/{id}/run-now` 可选 body `{"params":{...}}`；`PATCH /v1/schedules/{id}` 新路由 `internal/httpapi/schedule_patch_handler.go`（可改 cron/at/title/params/enabled/timeout_sec，白名单之外的键 400，命令/agent/runner/project 明确提示删掉重建；改时间重算 next_run_at）；`schedule.run_now` 事件带覆盖的键值。路由在 `authz_routes.go` 登记（PATCH 权限同删除，开启时另查 `job.submit`），已 `go generate ./internal/httpapi/...` 更新 `docs/reference/permissions.md`。
- **CLI**：`schedule run --param K=V`、`schedule edit <id> …`、`schedule add|edit --param-def|--params-file`、`schedule show` 列参数声明。
- **Web**：`Schedules.vue` 立即执行参数表单（按类型生成控件、预填默认、desc、底部环境变量预览）+ 编辑弹窗；`utils/scheduleParams.ts` 放纯函数便于单测。
- **测试**：Go 侧 `internal/job/schedule_params_test.go`、`internal/httpapi/schedule_params_test.go`；web `web/src/views/schedulesParams.test.ts`。
- **文档**：`docs/reference/http-api.md`（新节「Schedule parameters and editing」）、`skills/gofer-usage/references/commands.md` 与 `web-console.md`、`CHANGELOG.md`。
- **未做（有意）**：无。（web 新建页 `NewSchedule.vue` 也加了参数声明编辑器。）
