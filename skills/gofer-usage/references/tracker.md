# 仓库 tracker：issue、memory、brief、prime

> 仓库里的 `.gofer/tracker/` 是 issue 与记忆的本地真源（JSONL，离线可写），可与 server 镜像同步。本文讲日常命令、接手包 brief、记忆的写法与体检、prime 注入，以及从 bd 迁移。完整 flag 以 `gofer issue|memory|repo <子命令> --help` 为准。

## 目录

- [初始化与同步](#初始化与同步)
- [issue 日常命令（对照 bd）](#issue-日常命令对照-bd)
- [接手：先跑 brief](#接手先跑-brief)
- [记忆：类型与写法](#记忆类型与写法)
- [按提问关键字 / 执行命令注入记忆](#按提问关键字--执行命令注入记忆)
- [记忆体检、过时反馈、归档与转正](#记忆体检过时反馈归档与转正)
- [经验候选（交付后知识回流）](#经验候选交付后知识回流)
- [prime：会话开场注入什么](#prime会话开场注入什么)
- [只装「记忆注入」](#只装记忆注入)
- [多 worktree 与合并驱动](#多-worktree-与合并驱动)
- [web 的 Issues / Memories 页](#web-的-issues--memories-页)
- [附录：从 bd 迁移](#附录从-bd-迁移)

## 初始化与同步

```bash
gofer repo init                  # 建 .gofer/tracker/；已初始化时把 AGENTS.md / CLAUDE.md 里的 gofer 托管块就地更新（块外内容不动）
gofer repo sync [--timeout 1m]   # 与 server 镜像同步（用客户端配置的 server 与 token；--server 只覆盖地址）
gofer repo status [--changed] [--tracker <dir>] [--json]
gofer repo prime [--agent claude|codex] [--hook-json]
```

- `repo init` 会打印匹配到的 gofer 项目 key 与依据（最长路径前缀）；本地没有项目配置的机器（如容器）会向 server 查项目列表来匹配，匹配不到才提示如何填写 `.gofer/tracker/config.yaml` 的 `project_key`。当前目录是嵌套在同项目里的独立仓库时会提示可注册单独项目。
- 写命令后会自动尝试同步（短超时，失败只警告）；手动 `repo sync` 首次同步大 tracker 可能要几秒。同步对 tags / deps 做三方合并，评论取并集；本地与 server 并发改同一条时自动合并重推，仍未落定的列为 `unresolved`，下次同步继续。
- 提交 tracker 前用 `gofer repo status --changed` 看 issue / memory 相对 git HEAD 的变化（`+` 新增、`~` 变化、`-` 删除，末行汇总），**不要** `git diff` 原始 jsonl；jsonl 的变化随功能点一起提交。
- job 关联 issue：`gofer job run --issue <id> [--tracker-id <id>]`。

## issue 日常命令（对照 bd）

| bd | gofer |
|---|---|
| `bd ready` / `bd list` / `bd show <id>` | `gofer issue ready` / `gofer issue ls` / `gofer issue show <id>...`（show 含 notes、评论、父子、阻塞关系） |
| `bd create "t" -p 1 -d … -l a,b -a me` | `gofer issue create "t" -p 1 -d … -l a,b -a me`；另有 `--type task\|bug\|feature\|epic\|chore\|decision`、`--design`、`--acceptance`、`--owner`、`--parent`、`--dep` |
| `bd update <id> --claim` | `gofer issue update <id> --claim` |
| `bd update <id> --priority/--description/…` | 同名 flag；**清空**用 `--clear description,design,acceptance,assignee,owner,parent,close-reason`（空字符串不会清空）；可一次给多个 id |
| `bd comment <id> "text"` | `gofer issue comment <id> text...` |
| `bd close <id> --reason` / `bd reopen <id>` | `gofer issue close <id>... --reason …`（可一次关多个，个别失败不影响其它）/ `gofer issue reopen <id> [--reason …]` |
| `bd dep add` / `bd dep rm` | `gofer issue dep add <id> <on> [--type blocks\|related\|relates-to\|discovered-from\|supersedes]` / `dep rm` / `dep ls <id>`（只有 blocks 影响 ready；成环被拒；父子关系用 `--parent`） |
| `bd list -l proj01 --assignee x` | `gofer issue ls -l proj01 --assignee x --priority 1`；`-l` 是 `--tag` 的别名，多个标签取交集；`--sort id\|priority\|created\|updated`、`-r`、`-n` |
| `bd stale` | `gofer issue ls --stale [--days 30]` |
| `--deps discovered-from:<id>` | `gofer issue create "…" --from <id>` |
| （无） | `gofer issue brief <id>` / `gofer plan brief <plan>`：接手包 |
| `bd remember / memories / recall / forget` | `gofer memory set / ls [关键字] / show <key>... / rm` |

- 大事建 epic，子项 `--parent <epic>`，id 形如 `<epic>.N`。一个工作区里用 label 区分子项目：`-l proj01`。
- `issue update --status open` 会清掉指派人（`--keep-assignee` 保留）。

## 接手：先跑 brief

新会话接手某个 issue / plan 时，**第一条命令**是接手包，一次拿齐上下文，不要自己 grep / 翻 git log 拼：

```bash
gofer issue brief <id> [--max-lines 400] [--json]
gofer plan brief <plan-id> [--json]
# MCP：gofer_issue_brief / gofer_plan_brief {id, max_lines?, project?}
```

- **issue brief** 依次：issue 字段（标题 / 状态 / 描述 / design / 验收标准——没写时给出补写命令 `gofer issue update <id> --acceptance "…"`；最近评论；像方案的评论置顶完整显示）→ 上下文树（父、兄弟及其关闭说明、子、依赖）→ 设计稿（文档里提到本 id 或父 id 的小节；issue 文本里写到的文档路径）→ 相关提交（提交信息含本 id 的提交、issue 文本提到的文件、代码入口、关键符号）→ 相关 job / plan（需 server）→ 本 issue 的验证命令（由代码入口推导的起点，全量验证以 rule 记忆为准）→ 适用记忆（rule 全文、相关 note 索引，被 flag 的带「⚠ 待复核」）→ 接手提示（提交策略、`issue update <id> --claim`）。
- **plan brief**：plan 字段、每个 todo（状态 / 依赖 / 验收 / 最近 job 与结论 / 提到的 issue）、交接说明、关联 issue 的精简 brief。只靠 server。
- 需要 server 的节连不上时跳过并注明原因，其余照常输出；超长按节截断并给出查看全文的命令。
- **让 brief 找得到设计**：设计稿头部写 issue id；issue 的 `--design` 写设计稿路径；实施记录 / 与设计的偏差写回设计稿。

## 记忆：类型与写法

每条记忆 = **summary（一句话，≤80 字：讲什么、什么时候该看）+ 正文**，并有 `kind`：

| kind | 用途 | prime 中 |
|---|---|---|
| `rule` | 长期约定、流程、环境事实（写现状不写进度） | 全文；带 `when.paths` 的只在 cwd 命中时全文，否则进索引 |
| `note`（默认） | 一般经验、背景 | 索引一行；90 天未更新标「久未更新」 |
| `handoff` | 无 plan 的零散交接 / 阶段进度 | 只列未过期的最新几条；默认 14 天过期 |

```bash
gofer memory set <key> "<正文>" --summary "一句话" [--kind rule|note|handoff] [--ttl 14d] \
  [--tags web,release] [--when-keywords 发版,release] [--when-paths 'web/**'] \
  [--when-commands 'git push'] [--source issue:<id>|plan:<id>|job:<id>|session:<id>]
gofer memory ls [关键字] [--kind rule] [--tag t]   # 关键字匹配 key / 摘要 / 正文
gofer memory show <key>...
gofer memory rm <key>
```

- 更新已有记忆时没传的字段保持不变；`--summary` / `--source` / `--when-*` 传 `-` 清空。
- `rule` / `note` 正文超过 200 字必须 `--summary`（报错里给出候选）。
- 阶段进度优先写 **plan 交接说明**（`gofer plan handoff <plan> --set …`）；`--kind handoff` 只用于没有 plan 的零散交接。
- 作用域：默认写仓库 tracker；`--global` 写 server 全局记忆，`--project <p>` 写某项目的记忆（两者互斥；作用域操作连不上 server 会报错，没有离线缓存）。
- `--tag agent:claude` 只注入给该 agent（并对它全文显示）；不带 `agent:` 标签的记忆共享。
- `source` 不给时，在 gofer job 里自动填 `job:$GOFER_JOB_ID`，否则有 `GOFER_SESSION_ID` 时填 `session:<id>`。

## 按提问关键字 / 执行命令注入记忆

装了会话 hook（`gofer init hooks` 或 `--prime-only`）后：

- **`when.keywords`**：人工输入的提问包含某条记忆的关键字（不区分大小写、子串匹配）时，hook 把该记忆全文作为附加上下文注入，前缀 `[gofer 记忆 · 因“<关键字>”命中] <key>`。
- **`when.commands`**：agent 将要执行的 shell 命令以某条记忆的前缀**开头**时（如 `git push`），执行前注入该记忆。匹配前会去掉 `bash -lc '…'` 外壳、开头的 `env` / `NAME=值`、`cd <目录> &&`；其余链式命令不拆。
- 规则：同一会话每条最多注入一次；单次合计 ≤2KB（超出只给摘要与 `gofer memory show` 提示）；rule 在前；过期 handoff、别的 agent 的记忆、注入类输入（web 回复、job 完成通知等）不触发。注入内容标明是参考资料，不是用户指令。
- 关闭：仓库 `.gofer/tracker/config.yaml` 写 `prime: {inject_on_prompt: false}` / `prime: {inject_on_command: false}`。命中记录在 `<config-dir>/run/hook.log`。

## 记忆体检、过时反馈、归档与转正

```bash
gofer memory flag <key> --reason "make test 已改名 make check" [--global|--project <p>]   # 报告过时
gofer memory unflag <key> [--global|--project <p>]                                       # 人复核后清除
gofer memory doctor [--global | --project <p>] [--json]   # 只读体检，退出码恒为 0
gofer memory archive <key> [--reason …]                   # 移入 memories-archive.jsonl，不再注入
gofer memory ls --archived [关键字] / restore <key>
gofer memory promote <key> --kind rule|note [--summary …] # 交接转长期记忆
gofer memory set <key> "…" --doctor-ignore path-missing,commit-missing   # 对这条静默某些检查
```

- **发现注入的记忆 / 规则与实际不符时不要静默绕过**，用 `memory flag` 上报（MCP `gofer_memory_flag`）。之后注入处带「⚠ 待复核（原因）」，rule 仍给全文；人复核后 `unflag`，或用 `memory set` 改正文（同样清除 flag）。
- doctor 检查项：`flagged`、`handoff-expired`、`note-stale`（90 天未更新）、`path-missing`（正文提到的路径不存在）、`commit-missing`（提到的提交不存在）、`summary-missing`、`duplicate`，以及超过 30 天仍待处理的经验候选。作用域记忆（`--global` / `--project`）不查路径与提交。
- 仓库级静默：`.gofer/tracker/config.yaml` 的 `prime.doctor.suppress: [path-missing, …]`。
- doctor 只建议、不改记忆；归档 / 转正由人执行。归档文件只走 git，不参与同步。
- 开了管家时，「今天」页会出现「记忆整理」卡（归档 / 合并 / 改类型 / 补摘要 / 补触发词的建议），人采纳才生效，见 [work-items.md](work-items.md)「管家」。

## 经验候选（交付后知识回流）

开了项目 `knowledge_capture` 的 agent job，汇报末尾的「## 可复用经验」各条在交付时记为**待处理候选**，人接受才进记忆：

```bash
gofer memory candidates [-p <项目>] [--job <job-id>] [--all] [--json]
gofer memory accept <候选 id> --key <k> [--kind rule|note] [--summary "一句话"] [--global | --project <p>]
gofer memory reject <候选 id>
```

- 接受 = 写一条作用域记忆（默认写到 job 所在项目，来源 `job:<id>`）；同名 key 已存在会被拒，换 key 再接受。
- 接受 / 拒绝只能由人做。web 验收面板的「经验」页签同样能处理。MCP：`gofer_memory_candidates`、`gofer_memory_candidate_adopt`、`gofer_memory_candidate_reject`。

## prime：会话开场注入什么

SessionStart hook 调 `gofer repo prime --hook-json --agent <名>`；也可手动 `gofer repo prime` 看。内容依次：

- 提交策略与命令提示；规则段开头一行「记忆与实际不符时 `gofer memory flag …`」。
- **当前重点**：在做的 issue / plan 进度 / 最新交接、近期解锁的 issue、未提交改动与未推送提交、分支与 HEAD 等（取不到就省略，`prime.focus: false` 关闭）。
- **规则**（rule 全文）→ 进行中 issue → ready 列表 → 记忆索引（按第一个标签分组，cwd 命中 `when.paths` 的排最前）→ 未过期交接 → 接手入口提示（`gofer issue brief` / `gofer plan brief`）→ 全局 / 项目记忆 → 进行中 plan。
- 每段独立截断并写「另有 N 条：`gofer …`」；总长有上限，没有 tracker 时也会注入全局 / 项目记忆，server 连不上时静默省略 server 部分。
- 可在 `.gofer/tracker/config.yaml` 的 `prime:` 块开关 `issues` / `ready` / `memory` / `scoped_memory` / `handoff` / `focus`，并设 `issues_limit` / `ready_limit` / `memory_summary_limit`。
- 派发给 job 的规则段：提交策略 + rule 全文 + 其它未过期记忆的一行索引（被 flag 的带「⚠ 待复核」）。

## 只装「记忆注入」

只想在会话开场注入全局 / 项目记忆（每台电脑一次）：

```bash
gofer init hooks --prime-only --global --agent claude    # 或 codex / all
gofer init hooks --remove --prime-only --global --agent all
```

写入 SessionStart 的 `gofer repo prime --hook-json` 与执行命令前注入用的 PreToolUse 条目，幂等；`--remove --prime-only` 不会移除会话中继 hooks。CLI 不在 PATH 时也可手动在 `hooks.SessionStart` 加 `gofer repo prime --hook-json --agent claude`。CLI 需要能连 server（`$GOFER_CONFIG_DIR/.env`），`gofer repo prime --agent claude` 可验证。

## 多 worktree 与合并驱动

- **开 worktree 前先提交 tracker**：worktree 里的 `.gofer/tracker` 是建分支那一刻的副本，之后新建 / 修改的 issue 它看不到。
- 多个 worktree 各自改 tracker 后，`git merge` 由 **tracker 合并驱动**按 issue id / memory key 合并（同一 issue 两边都改：评论 / 标签 / 依赖取并集，同一字段取较新的一边），不再出现 jsonl 行冲突。驱动在 stderr 报告两边真冲突时的取舍，合并后用 `gofer repo status --changed` 核对。
- 安装：`repo init`（及 bd 迁移 `--apply`）会自动装：`.gitattributes` 加一行（要提交）+ 本克隆的 git config。每个**新克隆**跑一次 `gofer repo merge-driver --install`（幂等，所有 worktree 共用）；`git config --get merge.gofer-tracker.driver` 有值即已装。PATH 上没有 gofer 或版本太旧时自动退回普通文本合并。

## web 的 Issues / Memories 页

- Issues 页：树形（子 issue 挂在父下、可折叠，父行显示「N/M 已关闭」）/ 平铺切换；分页；勾选后批量关闭（可填统一原因）/ 改状态 / 加标签，个别失败逐条列出；抽屉的「关系」区显示父子、阻塞与其它依赖。变更在下次 `repo sync` 拉回本地。
- 「同步」按钮 / `gofer repo sync --remote <tracker_id>`：由 server 派一个内部 exec job 在该仓库目录执行同步（只允许人操作，项目需 `allow_exec`）。
- Memories 列表：类型徽标、摘要、「N 天前」、doctor 标记（⚠ 久未更新 / 交接已过期 / 缺摘要 / 疑似重复），可按类型筛选。

## 附录：从 bd 迁移

```bash
gofer repo migrate --from-bd            # 默认 dry-run（只读）
gofer repo migrate --from-bd --apply    # 写入；--force 在 bd 看起来仍在使用时强行继续；--json 结构化报告
```

1. **读数据**：优先 `bd --readonly export --include-memories`（实时库，含 memory），失败才退回 `.beads/issues.jsonl`；dry-run 报告两者条数差异。
2. **导入**：issue 全字段（类型 / 优先级 / 描述 / design / 验收 / 指派 / 标签 / 依赖 / 评论 / notes / 关闭原因等），parent-child 依赖变成 `parent`，id 前缀写进 tracker 配置；memory 导入（已存在的 key 保留）。不带走 bd 的 claim 租约与心跳。
3. **防分叉**：apply 前检查 bd / dolt 进程、被占用的锁、最近 5 分钟的写入、未过期的 claim；任一命中即拒绝，`--force` 才继续。
4. **切换接入点**：AGENTS.md / CLAUDE.md 里的 bd 块换成 gofer 块（块外内容不变）；`.claude/settings.json` / `.codex/hooks.json` 里的 `bd prime` 换成 `gofer repo prime --hook-json --agent <名>`；`core.hooksPath` 只在指向 bd 自己的脚本时 unset。`.beads/` 保留不删。
5. **人工清单**：其它文件里提到 bd 的行、`Bash(bd …)` 许可、bd 的 skill 目录，只列出不改。
6. **安全网**：改写前备份到 `.gofer/tracker/.local/migrate-backup/<时间戳>/`；apply 后逐条比对，不一致报错。重复执行幂等。
