# N 批计划：内置 runner 保留名 + workflow / 唤醒收尾

## N1 `server` / `local` 保留名
- config：`ReservedRunnerName`（去空白、大小写不敏感）；`validate` 拒绝 type 非 local 的 `server`/`local` runner、`worker_id` 为保留名的 runner、`server.workers` 保留名键。`runners: {local: {type: local}}` 与 `server: {type: local}` 保持合法。
- worker id：`worker add`、`POST /v1/workers`、`init worker --id`、worker.yaml `worker_id` 均调 `config.CheckWorkerID`。
- 删除 declare-wins：`ResolveRunnerName` / `IsLocalRunnerName(cfg,..)` cfg 参数 / `job.normalizeRunner` / `Service.NormalizeRunner` / relay `SetRunnerResolver` / `AllowsLocalRunner(cfg,..)` cfg 参数 / `runnerKeyForSession(cfg,..)`，统一 `config.NormalizeRunnerName`；web `runnerDisplay.ts` 去掉 known 分支。
- 测试：删 declare-wins 测试，换成保留名被拒测试；保留拼写矩阵与字面量守护。
- 文档：AGENTS G043、skill、README。

## N2 workflow 收尾
- `wf show` done 后 `current_step` 越界 → `N/N (done)`；web WorkflowDetail 同步。
- `${steps.X.all.stdout}` 超 32KB：聚合内容写到工作流结果目录文件，以路径替换，不再报错。
- 模板默认 agent 项目未开放：web 向导自动换成该项目第一个可用同类 agent 并提示；CLI `wf run --template` 渲染后 agent 不被允许时列出项目可用 agent。

## N3 唤醒说明精简
- takeover-plan 的 message 与 cwd_reason 去重：一句结论 + 一句依据，web / CLI 同步。

## 验收
gofmt / build / windows build / vet / test；web vue-tsc + vitest + vite build（scratch）；临时 serve 冒烟；正式配置只读校验。
