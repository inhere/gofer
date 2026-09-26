# 强制规则（rules，JOB-06①）使用 Runbook

> 配套 design [`../design/2026-09-25-rules-injection-and-worker-init-design.md`](../design/2026-09-25-rules-injection-and-worker-init-design.md) §一。**不配就是老行为**：`server.rules` / `agents.*.rules` / `projects.*.rules` / `--rule` 全空、项目里也没有 `.gofer/RULES.md` 时，job 的 prompt 与以前逐字节一致（不插任何段）。

## 是什么：与 skills 的区别

| | rules（本文） | skills（[skills runbook](./2026-09-23-skills-runbook.md)） |
|---|---|---|
| 性质 | **必须遵守**的纪律 | **按需阅读**的参考资料 |
| 怎么到 agent | 提交时**原文注入 prompt 最前面** | 挂到 job 私有目录，prompt 里只给**清单+路径** |
| 形态 | 一段 markdown（短、硬） | 一个目录（SKILL.md + 附件） |
| 体积 | 总量上限 16KB（`server.rules_max_bytes`），超限**拒绝提交** | 单文件 2MB / 单个 10MB |
| 关掉 | `job run --no-rules`（**job 凭证不能关**） | `job run --no-skills` |

规则就是文本（"改文件只用 apply_patch"、"不要 push"、"汇报贴原始输出"），不做任何事后审计——它约束的是 agent 看到的东西。

## 存储与管理

- 一条规则 = `<config-dir>/rules/<name>.md`：首部可选 YAML frontmatter（`description`，以及仅作提示的 `agents`），其余是**注入的正文**（frontmatter 不进 prompt）。名字由 `agent rule set <name>` 的参数决定——frontmatter 里的 `name` 只是注释性文字，不参与命名（规则名同时是文件名、绑定键与 job 行上记的名字，只应有一个来源）。
- 索引在库：`rules` 表只存 `name/description/size/sha256/updated_at/updated_by`；`sha256` 是**文件原文**的摘要，job 行记录的就是它（"这次注入的是哪个版本"）。
- 名字规则同 skill：小写字母、数字、`-`，以字母或数字开头。

```bash
# 写（建或替换，可反复跑）
gofer agent rule set house-rules -f rules/house-rules.md
gofer agent rule set gofer-repo -f rules/gofer-repo.md    # '-' 从 stdin 读

gofer agent rule ls                    # 名字 / 体积 / sha256 前缀 / 描述
gofer agent rule show house-rules      # 元数据 + 正文
gofer agent rule rm house-rules
```

- 命令是**双模式**的：客户端节点（`GOFER_RUN_MODE=client`）走 HTTP 管 server 上的库；本机有配置时直接操作本机库（`--local` 可强制）。
- HTTP：`GET /v1/rules`、`GET /v1/rules/{name}`、`PUT /v1/rules/{name}`、`DELETE /v1/rules/{name}`。
  **写要 `can_admin`**；读对所有已认证调用方开放（**job 凭证也能读**——job 应该能看见管着自己的规矩）。
  `PUT` 接受 `application/json` 的 `{"content":"…"}` 或 `text/markdown` 原文，超过 `server.rules_max_bytes` 的**单条**规则会被拒（它永远注入不进去）。

## 绑定：四级**并集** + 仓库自带一份

```yaml
server:
  rules: [house-rules]                 # 全局纪律：每个 job 都带
  rules_max_bytes: 16384               # 可热改；0/缺省 = 16KB
agents:
  omp:
    rules: [windows-host]              # 该 agent 的约束
projects:
  hyy-ai-inspect:
    rules: [gofer-repo]                # 这个仓的约定
```

```bash
gofer job run -p hyy-ai-inspect -a omp --rule extra --prompt "…"    # 追加（可重复）
gofer job run -p hyy-ai-inspect -a omp --no-rules --prompt "…"      # 本次全关（user caller）
```

解析顺序 server → agent → project → job，**取并集去重**（同 skills，与 retry 的"就近层整体替换"相反）。

**项目的 `.gofer/RULES.md` 自动纳入**：项目仓库根下若有这个文件，它作为规则 `project:<key>` 排在**绑定规则之后**注入，随仓库版本走、**不需要登记**（review 时和代码一起看）。hub 读不到（典型：项目跑在 worker 上、hub 看不到那棵树）就**跳过**，并在时间线记 `job.rules_skipped {reason:"project_file_unreachable"}`；job 照常跑。

两条固定规则：

- **`exec` agent 不带 rules**：它执行的是命令，没有 prompt 可注入。
- **job 凭证不能 `--no-rules`**：job 正是纪律约束的对象（HTTP 提交带 `no_rules` → `403`）；user caller 可以为某一次运行关掉。

## 注入长什么样

提交时（`job.Submit`）解析 → 拼成**一段**放在 prompt **最前面**，随后才是 skills 清单与正文：

```
## 必须遵守的规则（gofer 注入，优先于本任务的其它说明）

### house-rules
…正文…
### gofer-repo
…正文…

<!-- gofer:rules-end -->

## 可用技能（gofer 挂载，按需阅读）
- …
你的原始 prompt / 模板正文
```

- 为什么不走 `SystemInject`：只有 claude（`--append-system-prompt`）和 codex（`-c developer_instructions=`）有那个机制，omp/jcode/acp 没有——那样行为会因 agent 而异。放 prompt 顶部对**所有** agent 一致，而且进了 `request_json`，事后可审计。**已有的 role `system_prompt` 照旧走 SystemInject，两者互不影响。**
- 规则段尾部有固定结束标记 `<!-- gofer:rules-end -->`，执行机插 skills 清单时按它定位（不解析 markdown），所以"规则 → 清单 → 正文"的顺序在任何机器上都一样。
- 远端（worker / peer）：**提交的那台机器**渲染好整段，执行机不再解析、不再注入自己的规则（否则会出现第二段）。worker 的 `RulesResolved` 由此保证。

## 体积上限

- 规则**正文**总量 > `server.rules_max_bytes`（默认 16384）→ **提交被拒（400）**，错误里点名**最大的几条**及其字节数，例如
  `rules total 21504 bytes, over server.rules_max_bytes (16384); largest: gofer-repo (20480 bytes), house-rules (1024 bytes)`。
- 规则应该短而硬；长篇参考资料放 **skill**（agent 按需读），不要靠加长规则。

## 不得放 secret

规则正文**会进 prompt 与 `request_json`**（落库、可被 `job show --request` 读到）。**不要把 token、密码、内部地址、客户数据写进规则**——需要凭据就让 agent 走环境变量 / `agent.env` / `env_files`（那些不进 prompt）。`job run --env` 同理：值随 request_json 落库，帮助文本已写明"never pass secrets here"。

## 观测与排障

```bash
gofer job show <job>       # rules: house-rules@3f2a1b0c9d8e, project:hyy-ai-inspect@aa11bb22cc33
```

- `rules:` 行 = 注入的名字 + 每条**当时那份文本** sha256 的前 12 位（完整摘要在 `jobs.rules_json`）。拿它和 `agent rule show <name>` 的 `sha256` 前缀比，就知道库是不是在 job 跑完之后被改过。
- `job show --request` 的 `prompt` 里能看到**agent 实际读到的原文**（含整个规则段）。
- 事件：`job.rules_injected {names, bytes}`（提交机记的收据）、`job.rules_skipped {reason}`（项目文件读不到）。
- resume **不再注入**（续接的会话里已经有了）；**rerun 重新解析**（规则改了，下一次跑的就是新版本；`job show` 的 sha 也会变）。
- 排障：
  - **agent 说没看到规则** → `gofer job show <job>` 看 `rules:` 行（空 = 没绑上：查四级配置 / `--no-rules` / 项目 `.gofer/RULES.md` 是否存在且非空）；有行就看 `job show --request` 的 prompt 开头。
  - **提交被拒 `unknown rule "x"`** → `agent rule ls` 里没有这个名字；`agent rule set` 后再试。名字写错必须报错，不能静默跳过。
  - **提交被拒 `rules total … over server.rules_max_bytes`** → 按错误里点名的那几条瘦身；长文档改放 skill。
  - **job 凭证提交报 403 `rules cannot be disabled by a job caller`** → 它试图带 `no_rules`；这是设计（决策 4），改由 user caller 提交或去掉该字段。
  - **改配置不生效** → `server.rules` / `server.rules_max_bytes` / `agents.*.rules` 都是**可热改**字段（`PUT /v1/config/...` 或配置页，无需重启）；`projects.*.rules` 与 `.gofer/RULES.md` 随项目配置 / 仓库版本走，下个 job 生效。
