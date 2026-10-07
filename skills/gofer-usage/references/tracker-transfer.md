# 离线拆分 repository tracker

一次性入口：在 Gofer 源码仓库运行 `go run ./scripts/tracker-transfer`。源 `config.yaml` 和 JSONL 记录保持不变；导入协调期间会短暂使用源 `.local/lock`。它只读显式指定的源 tracker；不会自动发现源、调用 `repo sync`、删除源记录或迁移 `.local` 缓存。准备实际迁移前，使用方需自行确定源同步与暂停写入时点。以下路径、issue ID 和 memory key 都由使用方填入，命令不内置业务筛选。

```bash
go run ./scripts/tracker-transfer inspect \
  --source-tracker '<source-tracker>' --tag '<candidate-tag>'

go run ./scripts/tracker-transfer export \
  --source-tracker '<source-tracker>' --bundle '<review-bundle.json>' \
  --prefix gofer --project-key gofer \
  --issue-id '<issue-id-1>' --issue-id '<issue-id-2>' \
  --memory-key '<memory-key-1>'

go run ./scripts/tracker-transfer import \
  --source-tracker '<source-tracker>' --bundle '<review-bundle.json>' \
  --target-root '<existing-target-repository-root>'
```

`inspect` 仅给 tag/query 候选，不能代替 `export` 的精确 `--issue-id`/`--memory-key` 白名单。导出包含完整对象、源 tracker ID、三个源文件的 SHA-256、选择清单、引用边界和新 tracker ID。父子、依赖及未选记录指向选中记录的反向引用一律列为边界；有边界时导出仍留下可审查包，但报错，导入也拒绝。把确实属于目标的记录补进白名单并重新导出；无法封闭的引用需先解决，工具不会自动扩选或丢弃关系。

导入前重新核对源哈希及包内容；源变化后须重新导出。目标须是已存在的 Git 仓库根。目标 `.gofer/tracker` 不存在时，工具在同目录暂存并校验整个 tracker，再发布；已存在时只接受同一包且文件未变的重复导入，否则拒绝覆盖。新 tracker 使用包内新 `tracker_id`、`prefix: gofer`、`project_key: gofer` 和 `auto_sync: false`；历史 issue ID、所有已知与未知 JSON 字段均原样保留。导入后从目标仓库目录运行 `gofer repo prime`、`gofer repo status`，核对最近 tracker 及源/目标记录，再由使用方另行决定同步和正式接入。
