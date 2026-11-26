## stage（工作项流程）

一项实现类工作先开流程，再写设计 / 建 plan / 派实施 job：流程让你和人都看得见「现在在哪一步、还差什么」。

```bash
gofer stage start <issue> [--size feature|small] [--work <id>] [-p <项目>]
gofer stage status [--work <id>]      # 当前步、步骤条、门禁缺项
gofer stage done                      # 门禁过了进下一步；没过退出码 2，并列出缺什么
gofer stage back --reason "…"         # 退一步（评审打回）
gofer stage blocked --reason "…"      # 标阻塞（之后再 done 解除）
gofer stage approve                   # 人的批准（会话里会被拒绝）
gofer stage drop [--reason "…"]       # 放弃流程（工作项本身不动）
gofer stage ls [-p <项目>]            # 本项目进行中的流程
```

- 模板：`feature` = design → review → plan → doing → verify → done（默认）；`small` = doing → verify → done。
- 不带 `--work` 时作用于当前会话工作项的流程；没有进行中的流程会提示用 `--work`。
- 门禁只看存储事实和 git，不看模型正文。典型缺项与补法：
  - design：`gofer work link <id> --doc docs/design/xxx.md --role design`，且该文件已提交、前 20 行含 issue id；
  - review：人在 web /「等我」批准，或关联的评审 job（`gofer work link <id> --job <job> --role review`）`result_decision=pass`；
  - plan：`gofer work link <id> --plan <plan>`，plan 至少一条 todo（`feature` 还要 `--doc … --role plan` 已提交）；
  - doing：关联 plan 的 todo 全部 done / skipped；
  - verify：有一次 `stage.verify_templates`（默认 `verify`）里的工作流运行，完成时间晚于 doing、且其 `base_sha` 等于或晚于 doing 完成时的 HEAD。
- `needs_human` 表示这台 server 判不了（项目不在本机、git 读不了）或只有人能给：在 web 控制台或会话外 `gofer stage approve`。
- 项目可用 `projects.<key>.stage.enabled: false` 关掉（关掉后不再提示开流程）。