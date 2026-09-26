<!-- template_id: design; template_version: 1.1.1 -->
# 强制规则注入与 worker 配置向导设计（JOB-06① / CFG-05 / 小项）

> 状态：Approved 0.2 / 实施中（2026-09-25 人工批准，决策 1–5 照写；批准时补 F-g 默认工作空间，决策 6）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.2 | 2026-09-25 | Claude | 人工批准；用户补 F-g：`gofer init` 时建默认工作空间并登记 `default` 项目，路径取 `~/.gofer/workspace`（决策 6）；放 R3 |
| 0.1 | 2026-09-25 | Claude | 初稿：JOB-06① 规则（rules）库 + 四级绑定 + 派发时强制注入 prompt 顶部；web 设置页新增「Rules」；CFG-05 `gofer worker init` 交互向导（拉 server 项目、推 roots、探测 agent、写配置、跑 doctor）；小项：文本类 cli-agent 会话 id 实时落库（F11 遗留）、`job run --env`（CLI 缺口） |

## 背景与目标

- **规则 vs skills**：JOB-10 的 skills 是"按需阅读"的参考资料，agent 可以不读。但有一类约束是**必须遵守**的：改文件只用 apply_patch、不要用 PowerShell 双引号字符串、测试先写先提交、汇报贴原始输出、不许 push、smoke 前 unset 真实 server env……我每份任务书开头都要贴一遍 `sup-common.md`。它应该变成 server 上的资产，按项目/agent 绑定，**每个 job 自动带上**，而不是靠人每次复制。
- **worker 接入靠手写**：新机器要手写 `worker.yaml`（`worker_id`、`server_link.urls`、token、`roots` 映射、agents）。容器 worker 就写错过地址（`host.docker.internal` 在容器里解析不了），`worker doctor` 也查出过失效的 root。需要一条命令完成接入。
- 目标：规则一处维护、自动生效、job 上可见；新 worker 一条命令接入并自检通过。

非目标：JOB-06② 密钥引用（`--secret`，等有需要带密钥的 job 再做）；规则的"执行检查"（规则只是注入给 agent 的约束文本，不做事后自动审计）。

## 已确认事实（代码 / 环境）

- skills（JOB-10）：server 侧库 `<config-dir>/skills/<name>/`、四级**并集**绑定（`EffectiveSkills`）、清单在**执行机**挂载后渲染到 prompt 顶部（`internal/job/skills.go`）。规则可以照搬"库 + 绑定"的模式，但**不需要挂载文件**——规则就是文本。
- `SystemInject`：只有 claude（`--append-system-prompt`）与 codex（`-c developer_instructions=`）有内置模板（`internal/agent/registry.go:238/261`），omp/jcode/acp 没有；角色的 `system_prompt` 走它。
- 设置页（WEB-12，v0.60.2）：`views/settings/SettingsLayout.vue` 的菜单是常量数组，加一页只加一行。
- worker 配置：`config.WorkerConfig{WorkerID, ServerLink{URLs, TokenEnv, Token, Reconnect}, Roots []WorkerRoot{From,To}, Agents, Labels, Guards, Tunnel, …}`（`internal/config/model.go:2081` 起）；POLICY 模式下项目来自 server 下发，`roots` 负责把 server 的 `host_path` 映射到本机路径；`gofer worker doctor` 已能检查连通性、token、roots。
- 项目派发到 worker 的依据：`projects.<k>.allowed_runners`（含 worker id）。
- CLI 缺口：`job run` 没有 `--env`（`JobRequest.Env` 只能走 HTTP，ACP-02 排障时只能用 curl 提交）。

## 一、JOB-06① 强制规则（rules）

### 1. 存储与管理

- 规则 = 一段 markdown，存 `<config-dir>/rules/<name>.md`（首部 frontmatter `name/description`，可选 `agents: [...]` 仅作提示）。库表 `rules {name, description, size, sha256, updated_at, updated_by}` 只做索引。
- CLI：`gofer agent rule ls|show|set <name> -f file.md|rm`（与 `agent skill` 同组，不新增顶级命令）。
- HTTP：`GET /v1/rules`、`GET/PUT/DELETE /v1/rules/{name}`（写操作 `callerMayAdmin`，job caller 只读）。
- web：设置页新增「Rules」：列表 + markdown 编辑器（左编辑右预览）+ 每条规则被哪些 server/agent/project 绑定的反查。

### 2. 绑定（四级并集，与 skills 相同）

```yaml
server:   { rules: [house-rules] }
agents:   { omp: { rules: [windows-host] } }
projects: { hyy-ai-inspect: { rules: [gofer-repo] } }
```

- 另外：项目仓库里若有 `.gofer/RULES.md`，自动作为该项目的一条规则（名为 `project:<key>`），随仓库版本走，**不需要登记**。
- `job run --rule <name>`（可重复）追加；`--no-rules` 关闭本次全部（需要 user caller；job caller 提交的 job 不能关规则）。

### 3. 注入方式

- 提交时解析（同一 cfg 快照）→ 拼成一个「必须遵守的规则」段，放在 prompt **最前面**（在 skills 清单之前）：

  ```
  ## 必须遵守的规则（gofer 注入，优先于本任务的其它说明）
  ### house-rules
  …正文…
  ### gofer-repo
  …正文…
  ```

- 为什么不走 `SystemInject`：只有 claude/codex 有，omp/jcode/acp 没有，行为会因 agent 而异；prompt 顶部对所有 agent 一致、`request_json` 里可审计。**已有 role `system_prompt` 的照旧走 SystemInject**，两者互不影响。
- 体积上限：规则总长默认 16KB（`server.rules_max_bytes`，可热改），超限提交被拒并点名最大的几条——规则应该短而硬，长篇参考资料用 skills。
- job 行记 `rules: [names]` + 每条的 sha256（事后可知当时注入的是哪个版本）；`job show` 与 web job 详情显示；事件 `job.rules_injected {names, bytes}`。
- rerun / resume：rerun 重新解析当前规则（规则更新即生效）；resume 不再注入（续接的会话里已经有了）。

## 二、CFG-05 `gofer worker init` 向导

```
$ gofer worker init --server http://192.168.65.254:8767 --token <worker token> --id w-laptop
✓ 连接 server 0.61.0，worker id w-laptop 未被占用
可派给 w-laptop 的项目（projects.*.allowed_runners 含它）：
  hyy-ai-inspect   host_path D:/work/inhere/hyy-ai-inspect
  zy-bsly-sf-dev   host_path D:/work/inhere/zy-bsly-sf-dev
推断 roots（本机检查目录是否存在）：
  D:/work/inhere  →  /home/me/work/inhere   [✓ 存在]   接受? [Y/n/改]
探测到 agents：claude 2.1.278 ✓   omp ✓   codex ✗   jcode ✓
写入 ~/.config/gofer/worker.yaml、~/.config/gofer/.env（GOFER_WORKER_TOKEN）
运行 doctor … 全部通过
启动：gofer worker -d        （Windows 桌面：pwsh -File scripts\start.ps1 … 或 gofer worker -d）
```

- 需要 server 新增只读接口 `GET /v1/workers/{id}/assignable`：返回 `allowed_runners` 含该 id 的项目（key、host_path），以及 server 版本/协议版本。鉴权：worker token 或 user caller。
- roots 推断：取这些 `host_path` 的最长公共前缀作为 `from`；`to` 的候选依次为：同路径（本机存在则直接用）→ 盘符转换（`D:/x` ↔ `/d/x`）→ 让用户输入。每个项目逐一检查映射后目录是否存在。
- agent 探测：复用 `detect` 配置（`<command> --version`），只写探测成功的 agent；未探测到的列出来提示如何安装。
- 非交互模式：`--yes` 接受全部推断，`--roots 'D:/work/inhere=/home/me/work/inhere'` 显式给映射；已有 `worker.yaml` 时默认拒绝覆盖，`--force` 覆盖前先备份为 `worker.yaml.bak-<时间>`。
- 结束时跑 `gofer worker doctor` 并打印结果；不自动启动（启动方式因平台而异，只打印命令）。

## 三、小项

| 编号 | 内容 |
|---|---|
| F-e | **文本类 cli-agent 会话 id 实时落库**（F11 遗留）：非 ndjson 的 agent（codex 文本头 `session id:`、claude 文本等）在运行中 stdout/stderr 出现可匹配的会话 id 时立即落库，与 ndjson 同一个 `SetJobSessionID` 窄更新 + `job.session_captured{source:"stream"}`；只扫前 64KB 与增量尾部，不影响大输出性能 |
| F-f | **`job run --env K=V`**（可重复）：补 CLI 缺口；值会进 `request_json`，帮助文本明确提示"不要放密钥"（密钥等 JOB-06②） |
| F-g | **默认工作空间**（用户 2026-09-25 补充）：`gofer init server` 与 `gofer worker init` 时创建 `~/.gofer/workspace`（Windows 为 `%USERPROFILE%\.gofer\workspace`；`--workspace <dir>` 或 `GOFER_WORKSPACE` 可改），并在生成的配置里登记项目 `default`（`host_path` 指向它、`allowed_agents` 取探测到的 agent）。`job run` 未给 `-p` 且当前目录匹配不到任何项目时，回落到 `default` 并在输出里提示；已有 `default` 项目或目录时不覆盖。用途：临时、不属于任何仓库的活有个安全的落脚处，不必先建项目 |

## 横切

- G032：全部 additive；无兼容分支。
- 协议：规则在提交时注入 prompt，worker 不需要认识新字段 → 不升协议版本。worker init 只用 HTTP。
- 安全：规则文本会进 prompt 和 `request_json`，**不得放密钥**（文档写明）；job caller 不能 `--no-rules`、不能写规则。

## 实施分期（omp，测试先写先提交）

| 期 | 内容 | 验收 |
|---|---|---|
| **R1** | JOB-06① 后端 + CLI（规则库、四级并集 + `.gofer/RULES.md`、注入、体积上限、job 行记录、事件、`--rule/--no-rules`）+ F-f `--env` | `TestRulesUnionAcrossLevels`、`TestProjectRulesFileAutoIncluded`、`TestRulesInjectedAtPromptTop`（在 skills 清单之前）、`TestRulesSizeLimitRejects`、`TestJobCallerCannotDisableRules`、`TestRulesRecordedWithSha`、`TestResumeDoesNotReinjectRules`、`TestJobRunEnvFlag` |
| **R2** | web 设置页「Rules」（列表、编辑预览、绑定反查）；job 详情显示 rules | `pnpm typecheck && pnpm build`；临时 server 浏览器目视 |
| **R3** | CFG-05 `worker init` + `GET /v1/workers/{id}/assignable` + F-e 文本会话 id 实时落库 + F-g 默认工作空间 | `TestWorkerInitInfersRoots`、`TestWorkerInitNonInteractive`、`TestWorkerInitRefusesOverwriteWithoutForce`、`TestAssignableEndpoint`、`TestTextSessionIDPersistedWhenSeen`、`TestInitCreatesDefaultWorkspace`、`TestJobRunFallsBackToDefaultProject`、`TestInitKeepsExistingDefault`；真机：在容器用 `worker init --yes` 重新生成 `w-docker-claude` 的配置到临时目录，与现有手写版 diff |

真机收尾：把 `sup-common.md` 里的通用约束拆成两条规则（`house-rules`：通用纪律；`gofer-repo`：本仓约定），绑到 hyy-ai-inspect 项目，之后我的任务书就只写"本期要做什么"。

## 决策（已批准 2026-09-25）

1. 规则注入在 **prompt 顶部**（所有 agent 一致），不走 SystemInject。
2. 规则四级**并集** + 仓库内 `.gofer/RULES.md` 自动纳入。
3. 规则总长上限 16KB，超限拒绝提交（逼规则保持短小；长文档用 skills）。
4. job caller 提交的 job **不能关闭规则**。
5. `worker init` 默认交互、`--yes` 非交互；不自动启动 worker。
6. 默认工作空间路径 `~/.gofer/workspace`（短、各平台一致；不用 `~/.local/gofer`，Windows 上没有这个约定），登记为 `default` 项目，`job run` 找不到项目时回落到它。

## R1 实测记录（2026-09-26，omp 实施）

R1 已实现并合入（§一 JOB-06① 后端 + CLI 与 §三 F-f），R3（CFG-05 worker init / F-e / F-g）未开始。

**落成与设计的对照**

- 规则库：`<config-dir>/rules/<name>.md` + `rules` 表索引（`name/description/size/sha256/updated_at/updated_by`）。`sha256` 是**文件原文**的摘要，`rule.BodyOf` 剥掉 frontmatter 后才是注入正文；frontmatter 的 `agents` 仅作提示、不入库（没有消费者）。
- 注入：`job.resolveRules` 在 `Submit` 里、`applyTemplate`（task 正文）之后解析（同一 cfg 快照），段首 `## 必须遵守的规则（gofer 注入，优先于本任务的其它说明）`、段尾固定标记 `<!-- gofer:rules-end -->`；skills 清单改由 `insertSkillsManifest` 按该标记**插在规则段之后**（规则 → 清单 → 正文）。
- `.gofer/RULES.md`：作为 `project:<key>` 排在绑定规则之后；读不到（os.IsNotExist 之外）→ 跳过并记 `job.rules_skipped {reason:"project_file_unreachable"}`。
- 记录：job 行 `rules_json=[{name,sha256}]`、事件 `job.rules_injected {names,bytes}`、`job show` 的 `rules: name@<sha256-12>` 行。
- resume 不注入（两个 resume 分支置 `RulesResolved`）；rerun（`RebuildJob`）经 `resolveRules` 丢弃旧段后按当前库重渲染；worker dispatch 置 `RulesResolved`（hub 已渲染，执行机不再注入）；peer-http 因 prompt 已带规则段而不再注入（无需新 wire 字段，协议版本不变）。
- 体积上限 `server.rules_max_bytes`（默认 16384）：超限是 400，错误点名最大的 3 条及字节数；`PUT /v1/rules/{name}` 对**单条**超限同样拒绝（它永远注入不进去）。
- 顺带修正一个真实缺陷：默认 job 标题原本取 prompt 首行，注入后就会变成规则段标题（每个 job 同名）——`defaultJobTitle` 改为先剥规则段。
- 字段策略表：`server.rules` / `server.rules_max_bytes` / `agents.*.rules` 均可热改，且 `GET /v1/config` 的 view 与写路径同步（否则 console 的下一次保存会把列表抹掉）。

**冒烟（临时 server，随机端口 + 临时 `GOFER_CONFIG_DIR`，未触碰真实配置）**

```
$ gofer agent rule set house -f house.md --server http://127.0.0.1:55954 --token … -c conf.yaml
wrote rule house (74B, sha256 b48d7353266b)
$ gofer agent rule ls …
house                    74B       b48d7353266b smoke house rule
$ gofer job run -p self -a echo --rule house --prompt 'SMOKE-BODY' --sync --server … -c conf.yaml
job … submitted: status=done
# 假 cli-agent 收到的 argv（python 把 sys.argv[1:] 写文件）：
['## 必须遵守的规则（gofer 注入，优先于本任务的其它说明）\n\n### house\nNEVER push; apply_patch only.\n\n<!-- gofer:rules-end -->\n\nSMOKE-BODY']
$ gofer job show <job> …
rules:      house@b48d7353266b
# 事件 API：
job.rules_injected {"bytes":29,"names":["house"]}
```

**测试**：`internal/config`（四级并集/上限默认值）、`internal/job`（项目 RULES.md 自动纳入及顺序、prompt 顺序、超限拒绝、行记录+事件、resume 不注入、rerun 重解析）、`internal/httpapi`（CRUD 与 can_admin/job 凭证只读、job 凭证 `no_rules` → 403）、`internal/commands`（`agent rule ls|show|set -f|rm`、`job run --env` 及帮助文本、`job show` 的 rules 行）。整包 `go test ./internal/job/ ./internal/httpapi/ ./internal/commands/ ./internal/config/ ./internal/jobstore/ -count=1` 全绿。

**待办（本设计剩余）**：R2（web 设置页 Rules + job 详情显示）、R3（CFG-05 worker init、`GET /v1/workers/{id}/assignable`、F-e、F-g）；真机收尾（把 `sup-common.md` 拆成 `house-rules` / `gofer-repo` 两条规则绑到 hyy-ai-inspect）也还没做。

**一处留待人工确认的取舍**：`--no-rules` 之外，规则段的存在与否也由 prompt 是否已带固定结束标记决定（用于 peer-http 的转发语义：hub 注入过就不再注入）。理论上调用方自己拼一个以该标题开头、且含结束标记的 prompt，就能让自己这次不被注入规则——但这只是"自己放弃纪律"（规则本就无事后审计），且不会影响别的 job；若要彻底堵住，需要给 peer 路径引入一个可传输的标记字段（会把语义扩到 wire 上）。

## R2 实测记录（2026-09-26，omp 实施）

R2（web 设置页「Rules」+ job 详情显示 rules + `job run --env` 远端警告）已实现并合入。R1 记录里"待办"的 R2 已完成；**R3（CFG-05 worker init / F-e / F-g）仍未开始**。

**落成与设计的对照**

- 设置页「Rules」（`web/src/views/settings/Rules.vue`，菜单排在「配置管理」之后）：列表（名称/描述/大小/更新时间/更新人/**被谁绑定**/操作）、新建（名字 + 原文）、编辑（左原文、右预览）、删除（二次确认）。
  - 预览复用项目已有的 `MarkdownBlock`（marked → DOMPurify），且只渲染**正文**：frontmatter 既不注入也不预览（前端 `stripFrontmatter` 对齐 `rule.BodyOf`）。冒烟里正文的 `<script>` 被整段剔除、`<img src=x onerror=…>` 只留 `<img src="x">`，`window.__xss` 始终 undefined。
  - 「被谁绑定」由 `GET /v1/config` 的 `server.rules` / `agents.*.rules` / `projects.*.rules` 前端反查，**没有新增端点**。
  - 编辑器收发的是**文件原文**（frontmatter 一起）：页面不认识其它 frontmatter 键（如仅作提示的 `agents:`），原样往返才不会丢。400/403 的文案原样显示在编辑器下方。
  - 删除确认列出绑定位置并写明"删除后这些绑定会在提交时报未知规则"（`resolveRules` 对未知名字就是 400 拒提交）。
- **一处超出设计字面、但为满足 R2 验收所必需的后端改动（additive）**：设计写"数据来自 GET /v1/config 的各 `rules` 字段"，而 R1 只把 `projects.*.rules` 放进了 config model —— `projectView` / `projectWriteReq` 都没有它，即**读不到也写不了**。R2 补上 `projectView.Rules`（读，随 `/v1/projects/{key}` 与 `/v1/config` 一起下发）与 `projectWriteReq.Rules`（指针合并语义：省略保留、`[]` 解绑），`TestProjectRulesBindingRoundTrip` 钉住 create → view → 部分 PUT 保留 → 显式 `[]` 解绑。没有它，"被谁绑定"的 project 一列永远是空，控制台也没有任何入口绑项目级规则。
- 配置页/项目页补上绑定输入（`server.rules` / `agents.<key>.rules` / `projects.<key>.rules`）：agent PUT 是**整体替换**语义，表单不回发就会被"保存一次顺手抹掉"（JOB-10 skills 的同款坑；`editableAgentBody` 从策略表回发全部可编辑字段，rules 现由表单值覆盖）。项目页的 rules 文本框显式下发数组，空数组才表达"取消最后一个绑定"。两个新输入都按服务端字段策略表（`server_policy` / `agent_policy`）决定是否渲染/回发：老 server 的策略表里没有 `rules` 时整条不发，免得被写端点当成未知字段拒掉整个保存。
- job 详情：skills 行旁边 `rules: a, b`，每个名字 hover 显示 sha256 前 8 位；时间线 `job.rules_injected` 标签「已注入规则」+ 摘要 `names · bytes`（顺带给 `job.rules_skipped` 补了标签与 reason）。
- CLI：`job run --env` 与**非本机** runner 同用时，提交成功后打一行
  `warning: --env is only applied to jobs the server runs itself; runner <x> will not see these variables`
  （`c.Printf`，不报错、不改请求）。runner 取**提交结果**的 `res.Runner`（server 解析后的值，模板/role 解析过的也算）；空值（老/假 server 不回显）不猜。`TestJobRunEnvWarnsForRemoteRunner` 覆盖：远端告警一次并点名、内置 runner（含省略 `--runner`）静默、无 `--env` 静默、请求未被改动。

**冒烟（临时 server：随机端口 60592 + 临时 `GOFER_CONFIG_DIR` + 临时目录里另编的 `gofer.exe`；未触碰真实配置目录，每条 CLI 调用都显式 `--server`/`-c` 且 unset 真实 env；真实浏览器目视）**

```
# 设置页新建规则（浏览器）：正文含 XSS 载荷，预览 HTML =
<div class="md"><p>NEVER push; <strong>apply_patch</strong> only.</p>
<img src="x"></div>            # <script> 被剔除、onerror 被剥掉，window.__xss 未赋值
已保存规则 house-rules（139 B · 通用纪律）       # 保存后行内回执
$ ls <config-dir>/rules/
house-rules.md                  # 139B，索引 description=通用纪律 / updated_by=default

# 绑定：项目页 rules 文本框 → 保存 → {...,"rules":["house-rules"]}；配置页 agent echoer
# 与 server 的 rules 输入框各绑一次 → GET /v1/config：
server.rules= ['house-rules']   agents rules: {'echoer': ['house-rules']}   projects rules: {'smoke': ['house-rules']}
# 设置 → Rules 的"被谁绑定"列（浏览器实读）：
server | agent echoer | project smoke

$ gofer job run -p smoke -a echoer --prompt 'SMOKE-R2-BODY' --sync --server … -c …
job 20260926-113958-4f038059 submitted: status=done …
job 20260926-113958-4f038059 finished: status=done exit_code=0
# job 详情（浏览器）meta 行 + hover：
RULES  house-rules, project:smoke
       title="house-rules @ sha256 332336da" / title="project:smoke @ sha256 5da08239"
# 时间线：
§ 已注入规则   house-rules, project:smoke · 192 B
# 编辑器下方原样显示服务端 400：
invalid rule body - the rule body is empty
# 删除确认（仍被绑定）：
删除规则「house-rules」？规则文件会一起删掉。
它仍被绑定：server、agent echoer、project smoke。删除后这些绑定会在提交时报未知规则（unknown rule "house-rules"）…
```

**测试**：`internal/commands`（新增 `TestJobRunEnvWarnsForRemoteRunner`，与既有 `TestJobRunEnvFlag` 同绿）、`internal/httpapi`（新增 `TestProjectRulesBindingRoundTrip`）。整包 `go test ./internal/commands/ -count=1`（ok）与 `./internal/httpapi/ -count=1` 全绿；`gofmt -l` / `go build ./...` / `go vet ./...` 干净；`cd web && pnpm typecheck && pnpm build` 通过（产物里多一个 `Rules-*.js` chunk）。

## R3 实测记录（2026-09-26，omp 实施）

R3（§二 CFG-05 `worker init` + `GET /v1/workers/{id}/assignable`、§三 F-e 文本会话 id、F-g 默认工作空间）已实现并合入。R1/R2 记录里的"待办"只剩**真机收尾**（把 `sup-common.md` 拆成 `house-rules` / `gofer-repo` 两条规则绑到 hyy-ai-inspect）。

**落成与设计的对照**

- 入口两个、实现一份：`gofer worker init`（`worker` 组子命令）与 `gofer init worker --server …`（委托；不带 `--server` 时仍是"写示例模板"的老行为）。参数 `--server/--token/--id/--roots/--yes/--force/--workspace/--timeout`。token 缺省回落到已导出的 `GOFER_WORKER_TOKEN`。
- 服务端新接口 `GET /v1/workers/{id}/assignable`：返回 `{worker_id, projects:[{key,host_path}], server_version, protocol_version}`。判定比设计字面**宽一档**（设计写"allowed_runners 含该 id"）：`allowed_runners` **直接写了该 worker id**，或写了 `type: worker` 且 `worker_id == <id>` 的 runner 名，二者都算可派——后者正是 job 提交时 `checkRunnerAllowed` + `isWorkerRunner` 的准入口径，只按字面实现会漏掉"runner 名与 worker id 不同名"的部署。鉴权：该 worker 自己的 token 或 user caller；**别的 worker token 403**（不用 `callerMayAdmin`：worker id 永远没有 can_admin）。
- roots 推断：最长公共前缀 → `to` 依次尝试**同路径** → **盘符互转**（`D:/x` ↔ `/d/x`）→ 交互输入；逐条显示存在性、回车接受/`n` 跳过/输入即改；映射后不存在的 project 单独告警。`from` 统一写成正斜杠形态（server 配置的写法，映射两侧本来就会归一）。显式 `--roots` **完全跳过推断**。
- agent 探测复用 `agent.Resolve` 的一次 detect pass：**只写探测到的**（`exec` 不写，它是内置的）；报告行按 key 排序（Go map 顺序本来随机）。
- 写文件：`<config-dir>/worker.yaml`（原子写：临时文件 + rename）+ `<config-dir>/.env`（**就地更新** `GOFER_WORKER_TOKEN` 一行，保留其它键）。已有 worker.yaml 时**先拒绝**（在发出任何请求之前，`--force` 才覆盖），`--force` 先备份为 `worker.yaml.bak-<YYYYMMDD-HHMMSS>`。生成的 yaml 带三行头注释（怎么改、roots 只在本地、token 不进本文件）。
- doctor 复用 `buildWorkerDoctorReport` + `renderWorkerDoctor`（不是 `runWorkerDoctor`：那个读 `workerDoctorOpts` 的全局 flag，向导必须指定**刚写的那个文件**）。为此给 `buildWorkerDoctorReport` 加了 `detector` 与 `connect` 两个显式参数（CLI 传 `agent.DefaultDetector()` / `workerDoctorOpts.connect`），向导传自己的探测 seam 与 `true`。**任一 FAIL → 命令非零退出**。
- **一处真机才暴露的坑（已修）**：向导写 .env 后立刻跑 doctor，而 doctor 从环境变量解析 token——新机器还没 export，于是必然报 `token 为空` 且注册探测 401。修法是向导把本次 `--token` `os.Setenv` 到自己进程再跑 doctor（`config.LoadDotenv` 不够：进程若在 job 内，dotenv 会**故意跳过**所有 `*_TOKEN` 键，见它的 leak-1 守卫）。
- 占用检查：`GET /v1/meta` 的 workers 里有该 id 且 `connected` → 打一行 warning（启动第二份会顶掉它的连接并失败其 in-flight job）。best-effort，老/不可达 server 直接跳过。
- F-g：`gofer init server` 生成配置时创建 `~/.gofer/workspace`（`--workspace` / `GOFER_WORKSPACE` 可改）并在模板的 `projects:` 映射**首位插入** `default` 项（文本插入，不重新 marshal —— 模板的价值就是那堆注释）；已有 `default` 项目则**沿用它的 host_path**（不新建、不搬家），目录已存在则原地复用。worker 向导只建目录 + 提示到 server 上登记（worker 是 POLICY，项目由 server 下发）。`job run` 项目解析：`-p` > cwd 匹配 > `default`（stderr 打提示）> 报错；`--role`/`--template` 时**不回落**（那两条的项目由服务端填）。
- F-e：本地 cli-agent 的 stdout/stderr 上挂一个观察器（`job.captureStreamSession`），去 ANSI 后按该 agent 的 `SessionCapture`（含 AGT-04 兜底）匹配，**首次命中即** `SetJobSessionID` + `job.session_captured{source:"stream"}`。前 64KB 头窗口 + 滚动 64KB 尾窗口，且**只扫新增字节 + 1KB 重叠**（1MB 输出扫约 1.3MB，不是每写一次就重扫累积缓冲）。ndjson agent（已有结构化捕获）、远端 runner、交互 job（pty relay 那条路负责）、已知 session id 的 job 都不挂。
- 一处既有测试的**契约变化**（随之更新，未削弱）：`TestFallbackCaptureRecordsEvent` 三个子例原先断言终态扫描的 `source:"stdout"`，现在文本 agent 的 id 由**实时**路径先拿到（`source:"stream"`，`by` 的 fallback/agent_config 区分照旧）；终态扫描仍有自己的覆盖（`TestCaptureCodexSessionIDFromStderrWhenStdoutMisses`、orphan 重扫）。顺带给 `assertSessionCapturedEvent` 加了 3 秒轮询：实时路径是"先写行、再记事件"，而测试先轮询行再断言事件，负载高时会输在微秒级。

**冒烟（临时 server：随机端口 35164 + 临时 config；临时 HOME；每条 CLI 命令 unset 真实 env；未触碰真实配置目录）**

```
$ export GOFER_CONFIG_DIR=<tmp>/cfg-worker HOME=<tmp>/home
$ gofer worker init --yes --server http://127.0.0.1:35164 --token smoke-worker-token \
    --id w-smoke --roots '<tmp>/proj=<tmp>/proj'
✓ 连接 server (dev build: no version stamped)，协议 v11，可派给 w-smoke 的项目 1 个
  smoke                    host_path <tmp>/proj
使用显式 --roots 映射 1 条（跳过推断）
探测到 agents（已装 8 个）：
  ✓ claude       2.1.278 (Claude Code)
  ✓ claude-acp
  ✓ codex        codex-cli 0.155.1
  ✗ codex-acp    未安装
  … (jcode-acp / omp-acp / opencode / tty-claude / tty-codex ✓, gemini-acp ✗)
已写入 <tmp>/cfg-worker/worker.yaml、<tmp>/cfg-worker/.env
默认工作空间 <tmp>/home/.gofer/workspace 已就绪：把它登记为 server 上的 `default` 项目…
运行 doctor：
worker doctor: <tmp>/cfg-worker/worker.yaml
worker_id:     w-smoke
PASS  config / worker_id / url（ws://127.0.0.1:35164/v1/workers/connect 可达）
PASS  token             来自环境变量 GOFER_WORKER_TOKEN（值不打印）
PASS  mode              policy: 1 roots, 当前生效 0 个 project（server 下发，读自 policy 缓存）
PASS  roots[0]          <tmp>/proj -> <tmp>/proj
WARN  guards / max_concurrent   （未设置 = 不额外收紧 / 不限并发）
PASS  agent.claude / claude-acp / codex / exec / jcode-acp / omp-acp / opencode / tty-claude / tty-codex
PASS  connect           ws://127.0.0.1:35164/v1/workers/connect: accepted=true protocol=11
result: OK — 0 failed, 2 warning(s)
启动：gofer worker -d

# 生成的 worker.yaml（节选）
# gofer worker config — generated by `gofer worker init` (CFG-05).
worker_id: w-smoke
server_link:
  urls: [ws://127.0.0.1:35164/v1/workers/connect]
  token_env: GOFER_WORKER_TOKEN
roots:
- from: <tmp>/proj
  to: <tmp>/proj
agents: {claude: …, claude-acp: …, codex: …（只写探测到的）}
# 生成的 .env
GOFER_WORKER_TOKEN=smoke-worker-token

# 覆盖保护（同一 config dir 再跑一次，不带 --force）
ERROR: <tmp>/cfg-worker/worker.yaml already exists; use --force to overwrite (the old file is backed up)
# 带 --force，备份落盘
worker.yaml.bak-20260926-122530

# 委托入口等价（另一个临时 config dir）
$ gofer init worker --server http://127.0.0.1:35164 --token … --id w-smoke --yes --roots '…=…'
# 同一向导输出；--force 亦备份

$ gofer init server -g          # GOFER_CONFIG_DIR=<tmp>/cfg-server HOME=<tmp>/home2
已生成 <tmp>/cfg-server/config.yaml，编辑后运行 `gofer config validate` 校验
$ ls -d <tmp>/home2/.gofer/workspace        → 存在
$ grep -A5 "F-g" <tmp>/cfg-server/config.yaml
  default:
    host_path: "<tmp>/home2\\.gofer\\workspace"
    allowed_agents: [claude, …]
$ gofer -c <tmp>/cfg-server/config.yaml config validate     → config OK

# F-g 回落（server 上登记了 default 项目；cwd 匹配不到任何项目）
$ cd <tmp>/home2 && gofer -c <tmp>/srv/config.yaml job run -a exec --sync -- cmd /c echo hello-from-default
note: current directory matches no project; using the default project "default" (<tmp>/home2/.gofer/workspace)
job 20260926-122618-167fdc42 submitted: status=done …
job 20260926-122618-167fdc42 finished: status=done exit_code=0
$ gofer … job show 20260926-122618-167fdc42
project:    default
cwd:        <tmp>/home2\.gofer\workspace
```

**测试**：`internal/commands`（`TestWorkerInitInfersRoots`〔同路径 + 盘符互转两个子例〕、`TestWorkerInitNonInteractive`〔假 hub 同时服务 assignable 与注册握手 → doctor 全 PASS〕、`TestWorkerInitRefusesOverwriteWithoutForce`、`TestInitCreatesDefaultWorkspace`〔默认路径/`--workspace`/`GOFER_WORKSPACE`〕、`TestInitKeepsExistingDefault`、`TestJobRunFallsBackToDefaultProject`）、`internal/httpapi`（`TestAssignableEndpoint`）、`internal/job`（`TestTextSessionIDPersistedWhenSeen`：运行中落库 + 1MB 输出不回扫 + 尾部窗口三个子例）。`TestInitWritesEmbeddedTemplate` / `TestInitServerGlobalPath` 两处"== 模板逐字节"断言随 F-g 改为"模板 + 插入的 default 项"（新增 `assertServerConfigFromTemplate`），并给它们补了临时 HOME——否则测试会往真实 `~/.gofer/workspace` 写目录。
