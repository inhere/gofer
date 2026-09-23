# Skills（工作方式知识资产，JOB-10）使用 Runbook

> 配套 design [`../design/2026-09-23-skills-binding-and-comment-routing-design.md`](../design/2026-09-23-skills-binding-and-comment-routing-design.md) §一。**不配就是老行为**：`server.skills` / `agents.*.skills` / `projects.*.skills` / `--skill` 全空时，job 的 prompt、result_dir、事件都与以前逐字节一致。

## 是什么

skill 是**工作方式知识**（"改文件只用 apply_patch"、"这个仓的分层约定"、"Windows 主机注意事项"），不是可执行插件：

- **一个目录一个 skill**：`<config-dir>/skills/<name>/SKILL.md`（必需，首部 YAML frontmatter 写 `name`/`description`，可选 `applies_to`）+ 任意附件（参考文档、模板片段、脚本）。
- **索引在库**：`skills` 表只存元数据与每个文件的 sha256（`version` 是文件清单的内容哈希），字节存盘、跟着配置目录一起备份。
- **派发时物化**：job 开跑前，绑定的 skill 被复制到**该 job 私有的 result dir** `<result_dir>/skills/<name>/…`，并在 prompt 前面插一段清单（技能名 + 描述 + 该机可读的 SKILL.md 绝对路径）。
- **不污染仓库**：绝不写 `.claude/skills`、也绝不写进 job 的 cwd。

## 导入 / 更新 / 导出 / 删除

```bash
# 从本地目录（SKILL.md 决定 skill 名）
gofer agent skill import ./skills/house-rules

# 从 zip、http(s) 直链、git（后两者只在 SERVER 侧拉取，不经浏览器）
gofer agent skill import ./house-rules.zip
gofer agent skill import https://example.com/house-rules.zip
gofer agent skill import 'git+https://github.com/acme/skills.git#gofer-repo-conventions'

gofer agent skill ls                     # 名字 / version / 体积 / 描述
gofer agent skill show house-rules       # 元数据 + 文件清单 + SKILL.md 正文
gofer agent skill update house-rules     # 按记录的 source 重新拉取，打印 added/changed/removed
gofer agent skill export house-rules --out house-rules.zip
gofer agent skill rm house-rules
```

- 同名导入 = **替换**（`import` 可反复跑）；替换是原子的：新树先在临时目录建好并校验通过才换上，源里少 SKILL.md / 路径逃逸 / 符号链接 / 超限都会让旧 skill **原样不动**。
- 安全边界：解包做路径逃逸校验（`SafeJoin`），单文件 2MB / 单个 skill 10MB 上限（`server.skill_limits`），只收文本与常见附件，**剥掉可执行位**（skill 是知识不是程序；要跑脚本让 agent 自己 `bash <path>`）。
- 命令是**双模式**的：客户端节点（`GOFER_RUN_MODE=client`）走 HTTP 管 server 上的库；本机有配置时直接操作本机库（`--local` 可强制）。HTTP：`GET /v1/skills`、`GET /v1/skills/{name}`、`POST /v1/skills/import`（multipart zip 或 JSON `{"source":…}`）、`POST /v1/skills/{name}/update`、`DELETE /v1/skills/{name}`、`GET /v1/skills/{name}/export`；**写操作要 `can_admin`**（读不要）。

## 绑定：四级**并集**

```yaml
server:
  skills: [house-rules]              # 全局默认：每个 job 都带
agents:
  omp:
    skills: [windows-apply-patch]    # 该 agent 的怪癖知识
projects:
  hyy-ai-inspect:
    skills: [gofer-repo-conventions] # 这个仓的约定
```

```bash
gofer job run -p hyy-ai-inspect -a omp --skill extra-notes --prompt "..."   # 追加（可重复）
gofer job run -p hyy-ai-inspect -a omp --no-skills --prompt "..."           # 本次全关
```

解析顺序 server → agent → project → job，**取并集去重**。

**与 retry 的关键差异**：retry 是"就近层**整体替换**"（某一层写了策略就用它整条，因为重试策略是互斥的），skills 是"**叠加**"——它是知识，不是策略；上层绑的规矩不该被下层悄悄丢掉。要关掉，只有 `--no-skills`（对整个 job 显式说"这次不读任何 skill"）这一条路。

两条固定规则：

- **`exec` agent 不带 skills**：它执行的是命令，没有"读文档"的概念（绑了也会被 `config.EffectiveSkills` 丢掉）。
- **`--no-skills` 是决策不是省略**：它把本次运行的所有绑定清空（空结果进 request_json，rerun 也重复这个决策）。

## 派发时到底发生了什么（agent 怎么"读到"）

1. **解析**：提交时按四级并集定下名单，**写回请求**（`request_json` / `jobs.skills_json`），名字不在库里 → **提交被拒**（不会跑一个悄悄缺规矩的 job）。
2. **物化**：
   - 本机 job → 直接复制到 `<result_dir>/skills/`；
   - ws-worker job → 每个文件一个 staged transfer，落点 `Base=result_dir`（复用 XFER-01 的通道），**由拥有该 result dir 的机器写**；
   - peer-http job → **不挂**（见下）。
3. **清单**：由**执行机**（真正挂文件的这台机器）在 prompt 前面插一段（名字 + 描述 + 本机真实路径），并且只在挂了之后才有清单；挂载失败 → job 直接 failed（agent 不会启动，所以不会出现"清单指向不存在的文件"）。
   - 因此 `request_json` 里始终是**用户原文**（清单不落库）：审计/rerun 复现的是"当时的请求"，清单是"那次运行"的属性，由 `job.skills_mounted {names, bytes, dir}` 事件与 `job show` 的 `skills:` 行说明。
4. **env**：同时导出 `GOFER_SKILLS_DIR=<result_dir>/skills`，agent 或包装脚本不必解析 prompt 就能找到挂载点。
5. **prompt 清单只放标题 + 路径**，不放正文——agent 自己判断该读哪份（SKILL.md 建议 ≤ 200 行）。

### 为什么落 result_dir，不落 `.claude/skills`

- `.claude/skills` 属于**仓库**：会进 git status、会被别的 job 看见、并发 job 互相踩。
- 各家 cli-agent 的 skills 机制各不相同（claude 认 `~/.claude/skills`，omp/jcode 各有自己的目录），**没有统一开关**；gofer 不能假设某个 env 就能让 agent 加载。写成"清单 + 绝对路径"对**任何** cli-agent 都有效。
- result_dir 是 job 私有的、已被 `.gitignore` 覆盖、job 结束随它一起过期清理；`--collect` 也显式排除这个目录（skill 是**输入**不是产物，不会被收进 artifacts）。

### agent 读的是**副本**

`<result_dir>/skills/…` 是提交那一刻库里的拷贝。agent 就地改它**不会回流**到库，下一个 job 也看不到——skill 的变更走 `agent skill import/update`。看到 agent 说"我把 skill 改了"要当没发生。

## 不得放 secret

skill 的内容会进 prompt 清单并被 agent 读进上下文（还可能被复述进汇报）。**不要把 token、密码、内部地址、客户数据放进 SKILL.md 或附件**——需要凭据就让 agent 走环境变量 / `env_files`（那些不进 prompt）。

## 谁不挂 skills

| 目标 | 结果 |
|---|---|
| 本机（serve-local） | 挂到 result_dir，清单由本机渲染 |
| ws-worker **协议 ≥10** | 文件走 uploads（`Base=result_dir`），**worker 自己**挂载并渲染清单 |
| ws-worker **协议 <10**（或版本未知/离线） | **不挂、也不发名字**：老 worker 不认 `base` 字段，会把文件写进**共享工作树**——宁可没有。job 照常跑，时间线记 `job.skills_skipped {reason:"worker_protocol", names, count}` |
| peer-http runner | **不挂**：peer 跑在另一台 gofer 上，hub 既没有传输通道也不知道对方路径。job 照常跑，记 `job.skills_skipped {reason:"peer_runner", names, count}` |

一句话：**"清单"与"文件"永远由同一台机器决定**，任何拿不到文件的机器也不会拿到路径。

## 观测与排障

```bash
gofer job show <job>       # skills: house-rules, gofer-repo-conventions
```

- 事件（job 时间线）：`job.skills_mounted {names, bytes, dir}`（执行机挂载成功）、`job.skills_skipped {reason, names, count}`（老 worker / peer runner）。
- web：job 详情显示本次 skills；「技能」页可看列表、SKILL.md 正文、导入/更新/删除/导出；配置页的 agent/server 编辑弹窗能改 `skills` 列表（可热改，不用重启）。

排障：

- **agent 说没看到 skill** → `gofer job show <job>` 看 `skills:` 行（空 = 没绑上：查四级配置与 `--no-skills`）；有行就看事件 `job.skills_mounted` 的 `dir`，以及 result_dir 下是否真有 `skills/<name>/SKILL.md`。
- **提交被拒 `unknown skill "x"`** → 库（`agent skill ls`）里没有这个名字；导入后再试。名字拼错必须报错，不能静默跑。
- **远端 job 没有 skills** → 看有没有 `job.skills_skipped`：`worker_protocol` = 目标 worker 版本旧（升级 worker 到协议 ≥10）；`peer_runner` = peer-http 不支持，属预期。
- **改配置不生效** → `server.skills` / `agents.*.skills` 是**可热改**字段（`PUT /v1/config/...` 或 web 配置页，无需重启）；`projects.*.skills` 在项目配置里，同样随 `config reload` 生效。
- **prompt 太长** → 清单只有标题+路径，但 agent 可能一股脑读完所有 SKILL.md。减少绑定、把大参考文档拆成按需读的附件、或给 SKILL.md 写清"什么任务才需要读这份"。
