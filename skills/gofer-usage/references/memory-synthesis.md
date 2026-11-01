# 每日记忆综合（材料、显式运行与查询）

`memory.synthesis` 是可选的 server 侧每日综合能力，默认关闭。它收集结构化材料、按预算裁剪并脱敏，计算稳定的 `input_digest`，保存运行记录；**显式 `run`**（P2）才会让一个只读、无工具的模型把脱敏后的材料综合成一份「项目动态」摘要，写进运行记录的 `result_json`。不会自动写入长期记忆；候选队列（采纳 / 拒绝）暂缓，没有每日定时调度、也不进会话开场（P3 未做）：不 `run` 就不会调用模型。

配置示例：

```yaml
memory:
  synthesis:
    enabled: false
    projects: []            # 空 = 所有项目；非空时只允许列出的项目
    agent: ""               # 空 = work.summarizer_agent（默认 claude）；须是 cli-agent，且是 claude / codex / suga（有已知的无工具参数）
    model_args: []          # 模型参数，如 ["--model", "haiku"]；空且 agent 是 work 的整理器时用整理器的低成本预设；不能含 tools / shell / mcp 开关
    timeout: 5m             # 一次模型调用的上限（≤ 1h）
    max_input_bytes: 24000  # 发给模型的材料上限（脱敏、截断之后）
    max_output_bytes: 12000 # 模型回答上限，超出按失败记录
    lookback: 24h
    keep_material_snapshot: false
```

模型调用：显式 `run` 提交一个隐藏的内部 job（与工作项整理器同类：`summarizer` 内部身份、`read_only`、不带项目规则、本机 runner、本机 server 的路径视图），agent 的参数 = `model_args` 之后接 server 强制的无工具参数（claude `--tools ""`、codex 关 shell 工具并只读沙箱、suga `--read-only`）；没有已知无工具参数的 agent 直接拒绝（不留运行记录）。提示词（`memory-synthesis-request`）随项目语言（`projects.<p>.language` → `server.language`），材料只有已脱敏、已截断的 envelope，且发送前再扫一遍，仍含密钥形态就拒绝发送；模型回答里的材料内文本一律当数据，不当指令。

输出（`daily-memory-synthesis.v1`，只含项目动态与来源引用，没有候选）：

```json
{"schema":"daily-memory-synthesis.v1","summary":"一两句话","highlights":[{"text":"一条进展","source_refs":["job:<id>#report"]}],"open_questions":["仍未解决的事"]}
```

server 严格校验：schema、`summary` 非空且 ≤ 600 字、`highlights` ≤ 20 条（每条 ≤ 500 字、≤ 20 个引用）、`open_questions` ≤ 10 条，`source_refs` 必须来自输入材料；回答里的未知字段丢弃；回答含疑似密钥的整份丢弃（`unsafe output`）。任何一步失败（模型调用失败 / 超时、回答超过 `max_output_bytes`、不是 JSON、校验不过）记为 `failed`，`error` 以 `agent call failed` / `invalid output` / `unsafe output` / `unsafe input` 开头并带原因（已脱敏、限长），不保存结果。

材料来源（只读，只取当前项目；按 server 时区与 `lookback` 取 `[当日 00:00, 次日 00:00)`，`lookback` 超过 24h 时窗口向前延伸）：工作项与里程碑日志、当天结束的 job 的汇报末尾（限长，不含 prompt / argv / 环境）、plan / todo 结论、memory doctor 的发现与待处理建议（不含 memory 全文）、上一份成功综合的摘要。文本先脱敏（令牌、`Authorization` 头、私钥块、大写环境变量赋值、`--token xxx` 参数、邮件地址）再截断。

同一项目同一天输入摘要不变时复用已成功的记录（只更新 `last_checked_at`）；同一天已有进行中的运行时不再新建；材料为空不建记录。`retain_days`（默认 90）之外的已结束记录由 server 的清理循环删除；server 重启时超时仍是 `running` 的记录转 `failed`（`server restart`）。

命令行：

```text
gofer memory synthesis ls [--project <p>] [--day <YYYY-MM-DD>] [--state running|succeeded|failed|skipped|all] [-n <limit>] [--json]
gofer memory synthesis show <run-id> [--json]
gofer memory synthesis run [-p <project>] [--day <YYYY-MM-DD>] [--force] [--json]
```

`run` 是同步的：模型返回（或失败 / 超时）后才输出结果。`-p` 缺省取 `$GOFER_PROJECT`；`--day` 缺省是 server 时区的今天。输出结局之一：`succeeded`（新综合，打印摘要、要点与来源引用）、`failed`（记录了失败原因）、`reused`（同项目同日输入摘要没变且已有成功记录，没调用模型；加 `--force` 才重跑，旧记录标 `skipped`）、`running`（同一天已有进行中的运行）、`empty`（那天没有材料，不建记录）。`memory.synthesis.enabled` 为假时拒绝并提示要改的配置项。需要权限 `memory.synthesis.run`（仅根管理员默认持有，可显式授予；不可授予 job 会话，job 凭据一律拒绝），受调用方项目范围限制。显式 run 不受 `daily_limit` / `retry_after` 约束（它们属于 P3 的定时调度）。

`show` 默认只提示快照是否保存，不打印快照正文；`--json` 给有权限的人看完整记录。

运行记录可通过 HTTP 查询（读权限与其它 memory 只读接口一致，受调用方项目范围限制）：

```text
GET /v1/memory-syntheses?project=<project>&day=<YYYY-MM-DD>&state=<running|succeeded|failed|skipped>
GET /v1/memory-syntheses/<run-id>
POST /v1/memory-syntheses/run      body {"project": "...", "day": "YYYY-MM-DD", "force": false}
```

`POST .../run` 返回 `{"outcome": "succeeded|failed|reused|running|empty", "run": {...}}`（`empty` 没有 `run`）；模型失败是 200 + `failed` 记录，不是 HTTP 错误；功能关闭 409、agent 不可用 409、项目 / 日期非法 400、缺权限或越过调用方项目范围被拒。结果在 `run.result_json`，也可用 `GET /v1/memory-syntheses/<run-id>` 取回。

默认只保存 digest、计数、状态和来源元数据。启用 `keep_material_snapshot` 时，详情中的快照仍只包含白名单字段、长度受限且已做 `[redacted]` 脱敏；它不包含 prompt、完整命令行、环境变量、日志或附件。把摘要当作有界建议，事实核对仍应回到 job、work、plan 或 finding 的原始记录。
