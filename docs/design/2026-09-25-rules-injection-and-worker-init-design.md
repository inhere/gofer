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
