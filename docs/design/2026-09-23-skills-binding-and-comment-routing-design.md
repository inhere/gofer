<!-- template_id: design; template_version: 1.1.1 -->
# Skills 绑定与评论 / leader 路由设计（JOB-10 / MCP-05 / 小项）

> 状态：Approved 0.2 / 实施中（2026-09-23 人工批准，决策 1–5 照初稿；批准时修正两处：CLI 落 `gofer agent skill …`、协议升 v10）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.2 | 2026-09-23 | Claude | 人工批准。两处修正：① skill 管理命令放 `gofer agent skill …`，不新增顶级命令（用户长期偏好：顶级命令列表保持精简；0.1 写的“`gofer skill` 组不算新增顶级命令”是自相矛盾的）；② `UploadSpec.Base` 走现有“按特性最低协议版本”范式：`CurrentProtocolVersion` 9→10、新增 `SkillsMinProtocolVersion = 10`，hub 对 <10 的 worker 不挂 skills 并记事件（0.1 写的“协议不升”与“对老 worker 降级”互相矛盾——不升版本 hub 无从判断） |
| 0.1 | 2026-09-23 | Claude | 初稿：JOB-10 skills 绑定（server 侧 skill 库 + 派发时物化到 job 私有目录 + 提示词引用，不污染仓库工作树）；MCP-05 评论与 @提及派活（阶段 A 评论 + 触发，阶段 B leader 回合）；小项 S1–S4（SPA 支持 HEAD、三个 server 字段放行热改、`job.session_captured` 镜像、web 里编辑通知块） |

## 背景与目标

到 v0.53 为止，"派活 → 执行 → 验收 → 重试"这条链已经自动化得差不多了：计划链自动推进（PLAN-03）、失败可靠重试（AUTO-03）、配置在 web 改（WEB-04③）、新 agent 免配续接（AGT-04）。**剩下两处还完全靠人**：

1. **每个任务书都要重抄一遍规矩**。我给 omp 的任务书里，"改文件只用 apply_patch、不要用 PowerShell 双引号、测试先写先提交、汇报贴原始输出、G032 兼容策略、smoke 前 unset 真实 server env"这一段是逐字复制的（scratchpad 的 `sup-common.md`）。它是**工作方式知识**，却没有任何地方能让它跟着 job 走：`roles` 只能塞一段 `system_prompt`，模板（JOB-02）只渲染提示词正文，agent 自己的 `.claude/skills` 是那台机器的私产、gofer 看不见也管不了。参考项目（Multica）把这类东西做成 **skills：工作区资产，按 agent 绑定，可从目录/zip/URL 导入**。
2. **链条之间的"谁接手"还是人在决定**。job 跑完我读汇报、决定下一步、再写下一个任务书。今天没有任何"评论"这一层：`plan set-todo --append-note` 是单向便签，`decisions`（gofer_ask_human）是 agent 问人，`presence/inbox` 是 agent 间消息但没有绑到 job/plan 上下文。Multica 的 squad 模型是 **leader + 成员**：leader 用 `@成员` 评论派活，成员汇报后 leader 再被唤醒决定下一步/升级/转验收，leader 不自己实现、也不做最终 done。

目标：

- **JOB-10**：把"工作方式"变成一等资产——存在 server 上、可版本化、可按项目/agent/job 绑定，派发时自动出现在 agent 能读到的地方，**不污染项目工作树**。
- **MCP-05**：给 job 与 plan 加评论层，`@agent` 提及即派活；在此之上做**可选的** leader 回合（一个 agent 看完汇报决定下一步），人随时可以插话接管。
- 顺带清掉四个小项（见 §三）。

非目标：把 skill 做成可执行插件（AUTO-04 的事）；leader 自动 accept（人工验收的边界不动：agent 永远不能 accept 自己的活，GATE-01 §3）；IM 入站（OBS-07b/c，用户暂不做）。

## 已确认事实（代码 / 环境）

- **模板**：`internal/template/template.go` — 项目级 `<project>/.gofer/templates/`、全局 `<config-dir>/templates/`，`job run -t/--var` 渲染成 prompt。**只管正文，不带文件**。
- **roles**：`config.RoleConfig{Agent, SystemPrompt, Project, Tags}`（`model.go:359`），`SystemPrompt` 经 agent 的 `SystemInject` 模板进 argv（`submit.go:404`，claude `--append-system-prompt`，codex `-c developer_instructions=`）。**一段文字，没有文件。**
- **随 job 走的文件已经有了**：`JobRequest.Uploads []UploadSpec`（XFER-01 X2，`model.go:94`）——"已在 server 暂存区的传输 + 执行机要放到 cwd 下的相对路径"，`execute.go:224 materializeUploads` 在 agent 启动**前**物化，失败即 fail job；worker 路径复用同一套 xfer 帧。`Collect` 是反向（结束后按 glob 收回 artifacts）。**skills 完全可以骑在这条路上，但落点不能是 cwd（污染工作树）。**
- **job 私有目录**：每个 job 有 `result_dir`（`<项目根>/tmp/gofer/<日期>/<job-id>/`，SR1209），stdout/stderr/pty.txt/artifacts 都在里面，已被 `.gitignore`。
- **agent 侧读 skill 的方式各不相同**：claude 认 `~/.claude/skills` 与项目 `.claude/skills`；omp/jcode 各有自己的 skills 目录（jcode 启动横幅打印 `skills: 46 loaded`）。**没有统一的"指一个目录给我"开关**，所以 gofer 不能假设某个 env 就能让 agent 加载。
- **评论层今天不存在**：`grep` `r.GET/POST` 里没有任何 `comment` 路由；最接近的是 `decisions`（`server.go:721-724`，agent 问人、人作答）、`presence/inbox`（`:755-756`，agent 间消息 + 心跳）、`plan set-todo --append-note`（单向便签）。
- **supervisor 已有一个"自动回合"的骨架**：`config.SupervisorConfig{Enabled, IntervalSec, AutoAnswer, EscalateTo, MaxRoundsPerJob, AllowPromptRegex, OwnerAnswerTimeoutSec}`（`model.go:317`）+ serve 的 `startSupervisorLoop`/`startSupReconcileLoop`。MCP-05 的 leader 回合**复用这套闸门语义**（轮次上限、升级给人、白名单），不另造一套。
- **派发已经自动化**：PLAN-02 的 `ready`+assignee 自动 `job run`，PLAN-03 的依赖链自动推进。MCP-05 的"@提及派活"要走**同一个** Submit 入口，不新开一条派发路径。

## 一、JOB-10 skills 绑定

### 1. 数据模型：skill 是一个目录

```
<config-dir>/skills/<name>/
  SKILL.md          # 必须；首部 YAML frontmatter: name/description/applies_to(可选)
  <任意附件>         # 脚本、参考文档、模板片段……
```

- 元数据存库（新表 `skills`：`name/title/description/source/source_ref/version/files_json/size/updated_at/updated_by`），**文件存盘**（配置目录下，跟着配置一起备份）。库只存索引与校验值（每个文件的 sha256），便于 `skill ls` 与变更检测。
- `applies_to`（可选）：`agents: [claude, omp]` / `projects: [...]`，仅作**提示**（列出时标注），不做硬拦截——绑定关系由下面的三级配置决定。

### 2. 导入：`gofer agent skill import <src>`（放在已有的 `agent` 组下，不新增顶级命令）

- `<src>` 支持：本地目录、`.zip`、`http(s)://…/xxx.zip`、`git+https://…#subdir`（后两者**只在 server 侧拉取**，不经浏览器）。
- 导入即**解包到 `<config-dir>/skills/<name>/`**，写库，记 `source`/`source_ref`（URL + commit/etag），支持 `gofer agent skill update <name>` 按 source 重新拉取并 diff。
- 安全：解包做路径逃逸校验（复用 xfer 的 `SafeJoin`）、单文件与总大小上限（默认 2MB/10MB，可配）、只接受文本与常见附件类型，**拒绝可执行位**（`chmod -x`，skill 是知识不是程序；要跑脚本由 agent 自己 `bash <path>`）。
- 其它子命令：`agent skill ls`、`agent skill show <name>`、`agent skill rm <name>`、`agent skill export <name> [--out x.zip]`。

### 3. 绑定：三级 + 单 job 覆盖

```yaml
server:
  skills: [house-rules]              # 全局默认（每个 job 都带）
agents:
  omp:
    skills: [windows-apply-patch]    # 该 agent 的怪癖知识
projects:
  hyy-ai-inspect:
    skills: [gofer-repo-conventions]
```

- `job run --skill <name>`（可重复）追加；`--no-skills` 关闭本次的全部绑定。
- 解析顺序：server → agent → project → job，**取并集去重**（与 retry 的"就近整体替换"不同：skills 是叠加的知识，不是互斥策略；这条差异在 runbook 里写明）。
- `exec` agent 不带 skills（它执行的是命令，没有"读文档"的概念）。

### 4. 派发时怎么让 agent 真的读到

这是本项的核心取舍。**不写进项目工作树**（`.claude/skills` 属于仓库、会被 git 看见、并发 job 会互相踩），而是：

1. 物化到 **job 私有目录**：`<result_dir>/skills/<name>/…`。本机 job 直接写；worker job 走**现成的 uploads 通道**（每个文件一个 staged transfer，落点改为 result_dir 相对路径——为此 `UploadSpec` 增一个 `Base` 字段，`cwd`（默认，保持今天的语义）或 `result_dir`）。
2. 在 **prompt 前面**插一段"技能清单"（gofer 生成，位于用户 prompt 之前、与模板渲染结果之间）：

   ```
   ## 可用技能（gofer 挂载，按需阅读）
   - windows-apply-patch：在 Windows 主机改文件的注意事项 → D:\...\tmp\gofer\...\skills\windows-apply-patch\SKILL.md
   - gofer-repo-conventions：本仓的分层与提交约定 → …\SKILL.md
   先读与本任务相关的 SKILL.md，再动手。
   ```

   路径按**执行机**的视图渲染（本机是主机路径，worker 上是该机路径，复用 `ExecPath` 的现有规则）。
3. 同时导出 `GOFER_SKILLS_DIR=<result_dir>/skills`，方便 agent/脚本自行发现。

这样对**任何** cli-agent 都有效（不依赖某个 agent 的 skills 机制），且 job 结束即随 result_dir 一起过期清理；`--collect` 不会把它们收回去（skills 是输入不是产物，物化路径显式排除）。

### 5. 可观测与验收

- 事件 `job.skills_mounted {names, bytes}`；`job show` 列 `skills: a, b`；web job 详情加一行。
- 验收：一个带 `--skill` 的 omp job，`result_dir/skills/` 有文件、prompt 头部有清单、汇报里 agent 引用了 skill 内容；worker job 同样（走 uploads 通道）；`--no-skills` 时三处都没有。

## 二、MCP-05 评论与 @提及派活

分两阶段，**阶段 A 独立可用**，阶段 B 才是 leader。

### 阶段 A：评论层 + @提及触发

1. **模型**：新表 `comments`（`id`(`cm-<8hex>`)/`scope`(`job`|`plan`|`todo`)/`scope_id`/`author`(caller id 或 agent key)/`author_kind`(`user`|`agent`)/`body`/`mentions_json`/`created_at`/`triggered_job_id`）。
2. **接口**：`POST /v1/{jobs|plans}/{id}/comments`、`GET …/comments`；CLI `gofer job comment <id> "…"` / `gofer plan comment <plan> "…"`；web 在 job 详情与 plan 详情各加一个评论区（时间线右侧，不与事件混排）。
3. **@提及即派活**：正文里 `@<agent-or-role>` 被解析为提及。命中**已注册 agent 或 role** 时，server 用同一个 `Submit` 入口起一个 job：
   - `project` = 评论所在 job/plan 的 project；`cwd` 同源 job（或 plan 的默认）；
   - prompt = 「上下文摘要（被评论对象的标题/状态/最近一次汇报摘要）+ 评论正文」；
   - `--todo` 绑定（评论在 todo 上时）；tags 加 `from_comment`、`comment_of:<id>`；
   - 回写 `comments.triggered_job_id`，并在评论区显示"已派发 job xxx"。
4. **闸门**（重要，否则会变成失控的自动派活）：
   - 只有 **user caller**（或 `can_admin`）的评论能触发派活；agent 作者的评论**默认只记录不触发**，除非该 agent 在 `supervisor.leader` 白名单里（阶段 B）。
   - 每个 scope 的触发有频率上限（默认 1 分钟内最多 1 次、每个 job 累计 10 次，可配）；超限记事件 `comment.trigger_throttled`，不派。
   - 被提及的 agent 必须在该项目的 `allowed_agents` 里，否则评论保留、回一条系统说明。
5. **通知**：`comment.created`（可选订阅，不进默认集）、`comment.triggered {job_id}`（同上）。人被 @ 时（`@me`/`@<caller>`）进 `session.waiting` 同款提醒路径。

### 阶段 B：leader 回合（可选开关）

```yaml
supervisor:
  leader:
    enabled: true
    agent: omp-leader        # 一个 role 或 agent key
    scopes: [plan]           # 先只在 plan 上开
    max_rounds_per_scope: 6
    on_member_done: true     # 成员 job 终态后唤醒 leader
```

- **触发**：挂在某 plan 上的成员 job 到达终态（done/failed/needs_review）→ 起一个 **leader job**，prompt = 「plan 目标 + todo 链现状 + 刚结束的 job 的汇报摘要 + 可选动作清单」。
- **leader 能做什么**（全部经 MCP 工具，**不给它直接改库的权力**）：`comment`（含 `@成员` 派下一步）、`plan set-todo --status ready|skipped`、`job wakeup create`、`gofer_ask_human`（升级给人）。**不能** accept/reject（GATE-01 §3 人工验收边界不动）、不能改配置、不能 push。
- **闸门**：`max_rounds_per_scope` 计数（超限即升级给人并停止唤醒）；leader 自己的 job 终态**不**再唤醒 leader（防自激）；plan `paused` 时不唤醒；每轮 leader job 都是普通 job（有 usage、有超时、失败走 AUTO-03）。
- **人优先**：人在评论区说话 → 当前轮次的 leader 唤醒被取消（`comment.created` 由 user 作者 → 清掉待唤醒），由人接管。

### 为什么不是"直接让 leader 调 plan dispatch"

派发权集中在 PLAN-02/03 已有的一条路上（`ready` + assignee → Submit），leader 只是**把某个 todo 置 ready**或**发一条 @评论**。这样任何自动派活都能在 plan 看板上看见、被 `plan pause` 一键叫停，且 leader 失灵时链条仍可由人手动推进。

## 三、小项

| 编号 | 内容 | 理由 |
|---|---|---|
| S1 | SPA 回落支持 `HEAD`（`internal/httpapi/server.go:785` 的 `Method != GET` 放宽为 `GET`/`HEAD`） | 2026-09-23 真机实测 `HEAD /` 与 `HEAD /assets/*.js` 都 404；浏览器不受影响，但反代/健康探测/CDN 会踩 |
| S2 | `server.dir_lock` / `server.xfer` / `server.agent_health` 三块的字段策略从 `restart_required` 放行为可热改 | R3 实测：它们每次读取，标成"需重启"是保守过头（改表数行 + 测试） |
| S3 | `job.session_captured` 是否进 worker→hub 事件镜像白名单 | R1 留的决策：**不加**（交互 job 在 hub 侧已记一条，加了会重复）；本期只把这个结论写进注释与 runbook，不改代码 |
| S4 | web 配置页支持编辑 `notification`（局部字段：`enabled`/`webhooks[].url`/`kind`/`events`，`secret_env` 只编辑名） | R3 遗留：整块替换会丢 webhook 的 secret 引用，做成局部补丁式即可 |

## 横切

- **G032**：`UploadSpec` 增 `Base` 字段是 additive（空 = `cwd`，与今天一致）；`comments`/`skills` 是新表；leader 是 opt-in 开关，默认关闭。无新增兼容分支，无 DEPRECATED 标记。
- **协议**：`UploadSpec.Base`（wire 上 `XferUpload.base`）是新字段，老 worker 会忽略它而把文件落到 cwd——所以按现有范式**升版本**：`CurrentProtocolVersion` 9→10，新增 `SkillsMinProtocolVersion = 10`；hub 给 <10 的 worker 派 job 时**不挂 skills**、记事件 `job.skills_skipped {reason:"worker_protocol"}`，绝不误写 cwd。
- **安全**：skill 导入做逃逸/大小/可执行位校验；评论派活只认 user caller（阶段 A）；leader 权限白名单化、轮次封顶、人可随时接管；skills 内容会进 prompt（可能被 agent 复述），**不得放 secret**——文档明写。
- **存储**：skills 文件随配置目录备份；`result_dir/skills` 跟 job 一起过期清理；comments 随 job/plan prune 一并删。

## 实施分期与验收（全部 omp，测试先写先提交）

| 期 | 内容 | 验收要点 |
|---|---|---|
| **S1** | JOB-10 全部：`skills` 表 + `<config-dir>/skills/` + `gofer agent skill import/ls/show/rm/update/export` + 三级绑定 + `--skill/--no-skills` + 物化（本机直写 / worker 走 uploads，新增 `UploadSpec.Base`）+ prompt 清单 + `GOFER_SKILLS_DIR` + 事件 | 单测：`TestSkillImportRejectsEscape`、`TestSkillImportStripsExecBit`、`TestSkillsUnionAcrossLevels`、`TestNoSkillsDisablesAll`、`TestSkillsMountedToResultDirNotCwd`、`TestSkillPromptListsPathsForExecMachine`、`TestOldWorkerWithoutBaseSkipsSkills`；真机：带 `--skill` 的 omp job 在汇报里引用 skill 内容 |
| **S2** | MCP-05 阶段 A：`comments` 表 + HTTP/CLI/web 评论区 + @提及派活 + 闸门与限流 + 事件 | 单测：`TestCommentMentionSubmitsJob`、`TestAgentCommentDoesNotTrigger`、`TestMentionThrottled`、`TestMentionUnknownAgentExplains`、`TestCommentsPrunedWithJob`；真机：在 web 的 job 评论区 `@omp 帮我把 X 补上` → 新 job 起来并回链 |
| **S3** | MCP-05 阶段 B：leader 配置 + 成员终态唤醒 + 轮次封顶 + 人插话取消 + leader 可用工具白名单 | 单测：`TestLeaderWokenOnMemberTerminal`、`TestLeaderCannotAccept`、`TestLeaderRoundsCapped`、`TestHumanCommentCancelsLeaderWake`、`TestLeaderJobDoesNotWakeLeader`；真机：一个三步 plan 全程由 leader 推进，我只在最后 accept |
| **S4** | 小项 S1/S2/S4（S3 只写文档） | `TestHeadServesShellAndAssets`、`TestDirLockFieldsAreHotEditable`、`TestNotificationPatchKeepsSecretEnv` |

## 风险与限制

- **skills 会撑大 prompt**：清单只放标题 + 路径（不是全文），但 agent 可能一股脑读完所有 SKILL.md。缓解：`skill show` 里给出建议长度（SKILL.md ≤ 200 行），清单里带 `description`，让 agent 自己判断要不要读；job 详情显示挂载体积。
- **skills 与项目工作树的关系**：物化在 result_dir 意味着 agent 读的是**副本**，改它不会回流。这是有意的（skill 是知识资产，改动走 `skill import/update`），但要在文档里说清，避免 agent"就地改 skill"后以为生效了。
- **@提及派活是自动花钱**：限流 + 只认 user caller + 项目 allowlist 三重闸；默认每 job 累计 10 次。仍建议先在一个项目上开。
- **leader 可能空转**（反复评论不推进）：轮次封顶 + 每轮都是普通 job（有 usage 可见）+ `plan pause` 随时叫停；超限升级给人。
- **老 worker**：`UploadSpec.Base` 不认识就会落到 cwd——所以 server 对老 worker **不挂 skills**（记事件说明），而不是冒险写进工作树。

## 决策（已批准 2026-09-23）

1. skills 物化到 **job 私有 result_dir** + prompt 清单 + `GOFER_SKILLS_DIR`，**不写项目 `.claude/skills`**。
2. skills 三级绑定取**并集**（与 retry 的就近覆盖不同），`exec` agent 不带。
3. 评论派活阶段 A **只认 user caller**；agent 评论只记录，除非它是白名单 leader（阶段 B）。
4. leader **不能 accept/reject**，只能评论 / 置 todo ready|skipped / 建 wakeup / 问人；人一插话即接管当轮。
5. 小项 S3（`job.session_captured` 镜像）结论是 **不加**，只补注释与文档。

## S1 实测记录（2026-09-23，收尾 S1b）

S1 落地后留了两处缺口，本期按人工决策改掉，并在真机上实测（临时 server + 临时 config + 随机端口，未碰真实配置目录）。

### 决策 1：清单改由**执行机**渲染

- 提交时不再改 prompt：`req.Prompt` 保持用户原文（`request_json`、`job show`、rerun 都用它），解析后的名单只留在 `req.Skills` 与行上。
- 执行机（本机 = 这台 serve；ws-worker = worker 自己的 `job.Service.Submit`）在挂载/物化之后，把清单 prepend 到**本次运行的 prompt**，路径用本机真实的 `<result_dir>/skills/<name>/SKILL.md`；同时导出 `GOFER_SKILLS_DIR`。
- `{{skills_dir}}` 占位机制与 `substituteSkillsDir` **整体删除**（无外部依赖，G032 直接删）：不再需要"提交机渲染、执行机替换"这套两段式。
- 效果：协议 <10 的 worker 既没有文件也没有清单（它根本不认识 skills）；本机与协议 ≥10 worker 都是"挂了才列"。
- 实现落点：`internal/job/submit.go`（组装 `prompt` 并传给 argv/ACP/Forward）、`internal/job/skills.go`（`skillsPromptPrefix(req, resultDir)`）。

### 决策 2：peer-http runner 明确跳过

- peer 跑在另一台 gofer 上，hub 既没有传输通道也不知道对方路径 → peer job **不挂 skills**、也不列清单，记 `job.skills_skipped {reason:"peer_runner", names, count}`，job 照常跑（不报错）。
- 与之配套：对协议 <10 的 worker，dispatch 里**名字也一并去掉**（`skillsCarried`）——否则一个能渲染清单的 worker 会为它永远收不到的文件列出路径。

### 实测（真机 smoke，`-a omp`）

- 导入 `marker-skill`（SKILL.md 内含「汇报末尾必须原样写出 SKILL-MARKER-42」）→ `job run -p smoke -a omp --skill marker-skill --prompt '读一下技能清单里那份 SKILL.md，然后严格按它的要求回复'`。
- 渲染出的 argv 证明清单在执行机渲染、路径为本机真实路径：`{"command":"omp","args":["-p","## 可用技能（gofer 挂载，按需阅读）\n- marker-skill：… → <result_dir>\\skills\\marker-skill\\SKILL.md\n先读与本任务相关的 SKILL.md，再动手。\n\n<用户原文>"]}`，`env_keys` 含 `GOFER_SKILLS_DIR`。
- omp 汇报末尾出现 `SKILL-MARKER-42`（agent 确实读了挂载副本）；项目 cwd 无任何新增文件。
- 事件：`job.skills_mounted {names, bytes, dir}`；`job show` 多一行 `skills: marker-skill`。

### 实测中发现并修掉的两个 S1 缺陷

1. **本机挂载目录少一层**：`job` 侧把 `skills/` 根交给 seam，而 `skill.Store.Mount` 是"把 skill 树拷进给定目录"，适配器没有补上 skill 自己的目录名 → 文件落在 `<result_dir>/skills/SKILL.md`（两个 skill 还会互相覆盖），与清单/worker 上传路径（`skills/<name>/…`）不一致。修：`core.hubSkillLibrary.Mount` 补 `name`；回归测试 `TestSkillMountLayoutMatchesManifestPath`（断言挂载路径 == `job.SkillDest`）。
2. **`jobs.skills_json` 从未写入**：列与 scan 都在，但 `toRecord`/`fromRecord` 没有投影 `JobResult.Skills` → 行上永远读不到绑定（`job show` 不打印 `skills:`、web 详情为空），尽管挂载与事件都对。修：`persistence.go` 两个方向都补上；回归测试 `TestSkillsPersistedOnJobRow`。
3. 顺带补：`server.skills` / `agents.*.skills` / `server.skill_limits` 在字段策略表里是"可热改"，但 `/v1/config` 的视图与写分支都没实现（于是每次 console 保存 agent 都 500，`TestConfigUpdatedEventRecorded`、`TestConfigValidateDryRunDoesNotWrite` 在干净树上就是红的）——本期补齐视图 + 写分支 + 往返测试。

### 已知不一致（留给人工决策）

- 任务书里的 smoke 示例写的是 `job run -a exec --skill <it> -- bash -lc 'echo $GOFER_SKILLS_DIR; ls -R …'`，但决策 2/§一.3 明确 **`exec` agent 不带 skills**（`config.EffectiveSkills` 对 exec 返回 nil），所以这条命令实测为：`GOFER_SKILLS_DIR` 为空、`ls` 报 `No such file or directory`、job failed(2)，`job show` 也没有 `skills:` 行。这是设计本身的一致结果（exec 执行命令、不读文档），实测改用 cli-agent（`-a probe`/`-a omp`）证明挂载与 env。若确实希望 exec job 也能拿到挂载，需要改决策（去掉 `EffectiveSkills` 的 exec 规则），本期未改。
