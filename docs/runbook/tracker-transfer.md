# 离线拆分 repository tracker（`scripts/tracker-transfer`）

> 面向 gofer 仓库维护者。该工具是 gofer 源码仓库里的一次性脚本（不随二进制发布），把一个 repository tracker 里选定的 issue / memory 拆到另一个仓库。

源 `config.yaml` 和 JSONL 记录保持不变；导入协调期间会短暂使用源 `.local/lock`。它只读显式指定的源 tracker；不会自动发现源、调用 `repo sync`、删除源记录或迁移 `.local` 缓存。准备实际迁移前，使用方需自行确定源同步与暂停写入时点。路径、issue ID 和 memory key 都由使用方填入，命令不内置业务筛选。

```bash
go run ./scripts/tracker-transfer inspect \
  --source-tracker '<source-tracker>' --tag '<candidate-tag>'

go run ./scripts/tracker-transfer export \
  --source-tracker '<source-tracker>' --bundle '<review-bundle.json>' \
  --prefix '<target-prefix>' --project-key '<target-project-key>' \
  --issue-id '<issue-id-1>' --issue-id '<issue-id-2>' \
  --memory-key '<memory-key-1>'

go run ./scripts/tracker-transfer import \
  --source-tracker '<source-tracker>' --bundle '<review-bundle.json>' \
  --target-root '<existing-target-repository-root>'
```

- `inspect` 仅按 tag / query 给出候选，不能代替 `export` 的精确 `--issue-id` / `--memory-key` 白名单。
- 导出包含完整对象、源 tracker ID、三个源文件的 SHA-256、选择清单、引用边界和新 tracker ID。父子、依赖及未选记录指向选中记录的反向引用一律列为边界；有边界时导出仍留下可审查包，但报错，导入也拒绝。把确实属于目标的记录补进白名单并重新导出；无法封闭的引用需先解决，工具不会自动扩选或丢弃关系。
- 导入前重新核对源哈希及包内容；源变化后须重新导出。目标须是已存在的 Git 仓库根。目标 `.gofer/tracker` 不存在时，工具在同目录暂存并校验整个 tracker，再发布；已存在时只接受同一包且文件未变的重复导入，否则拒绝覆盖。
- 新 tracker 使用包内新 `tracker_id`、`--prefix` / `--project-key` 指定的值和 `auto_sync: false`；历史 issue ID、所有已知与未知 JSON 字段均原样保留。
- 导入后从目标仓库目录运行 `gofer repo prime`、`gofer repo status`，核对最近 tracker 及源 / 目标记录，再由使用方另行决定同步和正式接入。

## 提交前核对 tracker 变化

用 `gofer repo status --changed`（可加 `--tracker <dir>`、`--json`）核对 `issues.jsonl` / `memories.jsonl` 相对 git HEAD 的变化，而不是 `git diff` 原始 jsonl：每行一条，`+ <id> <status> <title>` 新增、`~ <id> <旧>→<新> <title>` 状态变化（状态不变但其它字段变化写 `(fields: a,b)`）、`- <id> <title>` 删除，memory 以 key 和首行摘要（60 字）同理，末行汇总计数；无变化输出 `no tracker changes vs HEAD`。tracker 不在 git 里或文件在 HEAD 不存在时视为空；有变化也返回 0。
