## Gofer Issue Tracker

本仓库用 `gofer issue` / `gofer memory` 跟踪任务与记忆（数据在 `.gofer/tracker/`，会话开场自动注入上下文）。

```bash
gofer issue ready                       # 可开工的 issue（open 且未被阻塞）
gofer issue show <id>                   # 详情：评论 / 依赖 / 父子
gofer issue create "标题" -p 1 -l 标签   # 新建（-l 标签区分子项目）
gofer issue update <id> --claim         # 认领并开始
gofer issue comment <id> "进展"        # 追加评论
gofer issue close <id> --reason "..."  # 完成
gofer memory set <key> "内容" --summary "一句话"  # 记住经验；gofer memory ls <关键字> / show <key> 召回
```

- 用 `gofer issue` 跟踪全部任务，不要另建 markdown TODO；持久经验用 `gofer memory`。
- memory write hint
- commit policy line
- 查看 tracker 改了什么用 `gofer repo status --changed`（逐条列出 issue / memory 的增删改）；**不要** `git diff` / `cat` `.gofer/tracker/*.jsonl`，整行 JSON 会灌满上下文。
