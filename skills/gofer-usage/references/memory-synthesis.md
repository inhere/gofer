# 每日记忆综合（只读查询）

`memory.synthesis` 是可选的 server 侧每日材料综合能力，默认关闭。当前 P1 只收集结构化材料、按预算裁剪并脱敏，计算稳定的 `input_digest`，保存运行记录；不会启动模型，也不会自动写入长期记忆或建立候选队列。

配置示例：

```yaml
memory:
  synthesis:
    enabled: false
    projects: []
    max_input_bytes: 24000
    keep_material_snapshot: false
```

材料来源（只读，只取当前项目；按 server 时区与 `lookback` 取 `[当日 00:00, 次日 00:00)`，`lookback` 超过 24h 时窗口向前延伸）：工作项与里程碑日志、当天结束的 job 的汇报末尾（限长，不含 prompt / argv / 环境）、plan / todo 结论、memory doctor 的发现与待处理建议（不含 memory 全文）、上一份成功综合的摘要。文本先脱敏（令牌、`Authorization` 头、私钥块、大写环境变量赋值、`--token xxx` 参数、邮件地址）再截断。

同一项目同一天输入摘要不变时复用已成功的记录（只更新 `last_checked_at`）；同一天已有进行中的运行时不再新建；材料为空不建记录。`retain_days`（默认 90）之外的已结束记录由 server 的清理循环删除；server 重启时超时仍是 `running` 的记录转 `failed`（`server restart`）。

命令行：

```text
gofer memory synthesis ls [--project <p>] [--day <YYYY-MM-DD>] [--state running|succeeded|failed|skipped|all] [-n <limit>] [--json]
gofer memory synthesis show <run-id> [--json]
```

`show` 默认只提示快照是否保存，不打印快照正文；`--json` 给有权限的人看完整记录。

运行记录可通过 HTTP 查询（读权限与其它 memory 只读接口一致，受调用方项目范围限制）：

```text
GET /v1/memory-syntheses?project=<project>&day=<YYYY-MM-DD>&state=<running|succeeded|failed|skipped>
GET /v1/memory-syntheses/<run-id>
```

默认只保存 digest、计数、状态和来源元数据。启用 `keep_material_snapshot` 时，详情中的快照仍只包含白名单字段、长度受限且已做 `[redacted]` 脱敏；它不包含 prompt、完整命令行、环境变量、日志或附件。把摘要当作有界建议，事实核对仍应回到 job、work、plan 或 finding 的原始记录。
