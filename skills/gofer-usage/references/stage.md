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
gofer stage level record|remind       # 放权等级（只认人；会话里会被拒绝）
gofer stage report [-p <项目>] [--work <id>]  # 「本来会做的」与实际下一步的一致率
```

- 模板：`feature` = design → review → plan → doing → verify → done（默认）；`small` = doing → verify → done。
- 不带 `--work` 时作用于当前会话工作项的流程；没有进行中的流程会提示用 `--work`。
- 在有 tracker 的仓库里，`start` 先查 issue id，tracker 里没有就拒绝（防止敲错 id 开出一个找不到的流程）。
- 门禁只看存储事实和 git，不看模型正文。典型缺项与补法：
  - design：`gofer work link <id> --doc docs/design/xxx.md --role design`，且该文件已提交、前 20 行含 issue id；
  - review：人在 web /「等我」批准，或关联的评审 job（`gofer work link <id> --job <job> --role review`）`result_decision=pass`；
  - plan：`gofer work link <id> --plan <plan>`，plan 至少一条 todo（`feature` 还要 `--doc … --role plan` 已提交）；
  - doing：关联 plan 的 todo 全部 done / skipped；
  - verify：有一次 `stage.verify_templates`（默认 `verify`）里的工作流运行，完成时间晚于 doing、且其 `base_sha` 等于或晚于 doing 完成时的 HEAD。
- `needs_human` 表示这台 server 判不了（项目不在本机、git 读不了）或只有人能给：在 web 控制台或会话外 `gofer stage approve`。
- 项目可用 `projects.<key>.stage.enabled: false` 关掉（关掉后不再提示开流程）。

### 放权等级与「等我」卡

- 等级有两档：`record`（默认）只记录，`remind` 在记录之外，下一步属于 agent（design / plan / doing / verify）时给工作项的 current 会话送一轮「继续下一步：<步骤>」（系统前缀 `[gofer · 流程]`）。review 等人批准、done 没有下一步，都不送；`done` 没推进（门禁没过）时不重复送。合并、推送、发版永远要人，没有「自动推进」档。
- 两档都在步骤变化时把「本来会做的动作」写进工作项日志（`stage_action`），`gofer stage report` 拿它和随后实际发生的下一步对比，按动作、按规则给一致率（最近 20 条已定论的动作）。报告只供人决定要不要放权，不会自动升档。
- 收到「继续下一步」的提醒后照常工作：做完执行 `gofer stage done`，要回退或卡住用 `back` / `blocked`。会话离线时，坐在 seat 上的会话由 seat 的补发队列在上线后补送（步骤已变则丢弃），其余离线会话只在日志里记「未送达」，下一轮的流程提醒行照样会带上当前步。
- 人这一侧：review 等批准、门禁判不了、verify 失败，都会作为「等我」卡出现（批准 / 附意见退回）；agent 不需要也不能替人批准。
