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

运行记录可通过 HTTP 查询：

```text
GET /v1/memory-syntheses?project=<project>&day=<YYYY-MM-DD>&state=<running|succeeded|failed|skipped>
GET /v1/memory-syntheses/<run-id>
```

默认只保存 digest、计数、状态和来源元数据。启用 `keep_material_snapshot` 时，详情中的快照仍只包含白名单字段、长度受限且已做 `[redacted]` 脱敏；它不包含 prompt、完整命令行、环境变量、日志或附件。把摘要当作有界建议，事实核对仍应回到 job、work、plan 或 finding 的原始记录。
