# 交付闭环（本地合并 / 推分支 / 开 PR）与成本估算

- issue：gofer-5sms（GIT-02 交付闭环）、gofer-2ldy（成本估算）
- 状态：已确认（2026-10-12，五项均按建议），实施中

## 一、背景

`job run --worktree` 让 agent 在独立 worktree 里干活，提交落在分支 `gofer/<job-id>`。之后要人手动收尾：

- 本地合并已有：`gofer job worktree merge <id> [--squash]`、workflow 的 `pick --merge`、job 详情的「合并到基线」按钮。限制是只支持 server 本机 runner；主 checkout 必须干净且在命名分支上；冲突时会中止并恢复原状，不推送。
- 推分支、开 PR 没有。worker 上的 worktree 只在 worker 那台机器上，server 碰不到。
- 用量只记 token。claude / omp 的 job 有 agent 自报的金额；codex job 和终端会话（plan 上的「主会话用量」）没有金额，一直显示 $0。

目标：

1. job（或它的验收）结束后，**按项目或 job 的设置自动交付**：本地合并、只推分支，或推分支并开 PR / MR；结果（分支、PR 链接、失败原因）挂在 job 上，web 和 CLI 都能看到。
2. 本机 runner 和 worker 上的 job 都能交付。
3. PR 平台支持 GitHub、GitLab、Gitea / Forgejo，按 remote 自动识别。
4. 没有自报金额的用量，按一张**模型价格表**估算金额，界面上和自报金额区分开。

## 二、交付（GIT-02）

### 2.1 三种交付方式

| 方式 | 做什么 | 需要 |
|---|---|---|
| `merge` | 把 `gofer/<job>` 合进该仓库当前的基线分支（复用现有 MergeWorktree：`--no-ff` 或 `--squash`、冲突就中止并恢复原状）；可选把合并后的基线推到 remote | 主 checkout 干净且在命名分支上；推送要 `allow_push` |
| `push` | 把 `gofer/<job>` 推到 remote（可改名，如 `agent/<短 id>-<标题>`），不开 PR | `allow_push` |
| `pr` | `push` 之后在平台上开 PR / MR（可选草稿），目标分支默认取 worktree 的基线分支 | `allow_push` + 平台凭据 |

- 默认 `none`，行为和现在一样，不会自动推送任何东西。
- **从不 force push**。远端已有同名分支且不是这次的提交时，交付失败并说明原因。
- 交付完成后可选清理 worktree（`cleanup: true`）：merge 成功、或 push / PR 成功后删除 worktree；`push` / `pr` 会保留本地分支，以便之后在 PR 上继续改。

### 2.2 什么时候交付

- job 没开验收：job `done` 且有新提交（`commits_ahead > 0`）后执行。
- job 开了验收（`--review` / 项目 `require_review`）：**验收通过（accept）后**才执行，拒绝的不交付。web 验收台的「通过」旁边显示这次会做什么（如「通过并开 PR」），可以在那里改成「通过但不交付」。
- 没有新提交、job 失败或被取消，都不交付。事件里记一条 `job.delivery_skipped` 并写明原因。
- 手动补交付：`gofer job deliver <id> [--mode merge|push|pr] [--draft] [--branch <名>]`（web job 详情有同一个按钮），可以对已结束的 worktree job 重试或改换方式。

### 2.3 配置

```yaml
projects:
  <project>:
    delivery:
      mode: pr                 # none（默认）| merge | push | pr
      allow_push: true         # 推送许可；不开时 push / pr 一律拒绝，merge 只合不推
      remote: origin           # 默认 origin
      branch: "gofer/{{job_id}}"   # 推到远端的分支名模板：{{job_id}} {{short_id}} {{slug}}（标题生成）
      base: ""                 # PR 目标分支 / merge 基线，空 = worktree 创建时的基线分支
      draft: true              # 开 PR 时是否为草稿
      squash: false            # merge 方式用 --squash
      push_base: false         # merge 方式合并后是否把基线分支推到 remote（还需 allow_push）
      cleanup: false           # 交付成功后删 worktree
      labels: [agent]          # PR 标签（平台支持时）
      provider: auto           # auto | github | gitlab | gitea（自托管域名识别不了时手动指定）
      token_env: ""            # 平台 token 的环境变量名，空 = 按平台默认（见 2.4）
```

- job 级覆盖：`job run --worktree --deliver pr|push|merge|none [--draft] [--deliver-branch <名>]`；plan todo 和工作流步骤有同名字段。
- `allow_push` 只能在项目配置里打开，job 上不能临时打开。这对应「push 仍需显式授权」：要推送，项目所有者必须先在配置里明确允许。

### 2.4 PR 平台

按 remote URL 识别平台。github.com 识别为 GitHub，gitlab.com 识别为 GitLab；自托管实例的域名识别不了，用 `provider` 手动指定。三家都走 **REST API**，执行交付的机器上不需要额外装命令行工具：

| 平台 | 接口 | token（默认环境变量） | 说明 |
|---|---|---|---|
| GitHub / GitHub Enterprise | `POST /repos/{owner}/{repo}/pulls` | `GITHUB_TOKEN` / `GH_TOKEN`；都没有时运行 `gh auth token` 取 gh CLI 已登录的 token | 草稿用 `draft: true`，标签另调 issues labels 接口 |
| GitLab（含自托管） | `POST /api/v4/projects/{id}/merge_requests` | `GITLAB_TOKEN`；没有时尝试 `glab auth token`（glab 有此命令时） | 草稿用标题前缀 `Draft:` |
| Gitea / Forgejo | `POST /api/v1/repos/{owner}/{repo}/pulls` | `GITEA_TOKEN` / `FORGEJO_TOKEN` | 两者接口相同；草稿用标题前缀 `WIP:` |
| 识别不了 / 没有 token | 只推分支 | — | 结果里给出平台的「新建 PR」网页链接（若能拼出），交付状态记为 `pushed` |

- token 只在执行交付的机器上从环境变量（或 `gh` / `glab`）读取，不进配置文件、记录或日志；`token_env` 只写变量名，和 MCP 注册表的凭据规则一样。
- push 本身走 git 自己的凭据（credential helper / SSH key），gofer 不接管。
- API 地址由 remote 的 host 推出：GitHub Enterprise 是 `https://<host>/api/v3`，其余平台直接用 `https://<host>`。需要代理时，沿用执行机自身的 `HTTPS_PROXY`。

### 2.5 PR 内容

- **标题**：job 标题；没有标题时用 prompt 第一行。
- **正文**（markdown，平台会渲染）：
  - 任务说明（prompt 摘要，长的折叠起来）
  - 验收标准（如果设置了）
  - 提交列表（`base..HEAD`）
  - diff 统计
  - verify 结果（通过 / 失败与输出尾部）
  - 用量（token、自报或估算的金额、工具调用次数）
  - 验收结论与验收人（如果走了验收）
  - 回到 gofer job 详情的链接（`server.web_base_url` 已配置时）
- 正文末尾注明「由 gofer 自动创建」。不写 job 的 env，也不写敏感信息（prompt 会先经过和日志相同的脱敏）。

### 2.6 在哪台机器上执行

- **本机 runner 的 job**：server 进程直接执行（git 命令 + HTTP 调用）。
- **worker 上的 job**：worktree 只在 worker 上，所以 server 往那台 runner 派一个**隐藏的内部 exec job**（tag `delivery`），在仓库目录里执行 `gofer job deliver --local <job-id> …`，执行完把结果回报给 server。这是 Issues 页「同步」（TRK-05）已经在用的做法。worker 那边需要有 git 推送凭据和平台 token。
- 每个仓库同一时间只跑一个交付，排队执行，避免合并时互相踩。

### 2.7 记录与展示

- job 新增 `delivery`：
  - `mode`、`state`（`pending | merged | pushed | pr_opened | failed | skipped`）
  - `branch`、`remote`、`base`
  - `pr_url`、`pr_number`、`provider`
  - `error`、`at`
- 落库为 `jobs.delivery_json`。
- 事件：`job.delivered`、`job.delivery_failed`、`job.delivery_skipped`。其中 `job.delivery_failed` 进入通知默认集，失败的交付需要有人处理。
- 展示位置：
  - `job show` 输出 `delivery:` 一行；
  - web job 详情的 worktree 区显示交付状态、PR 链接，以及「重试 / 改为…」按钮；
  - 验收台的「通过」按钮旁边显示这次的交付方式；
  - plan 看板的卡片上显示 PR 链接图标。

### 2.8 不做

- 合并 PR、自动 rebase、处理 review 评论（PR 开出来之后交给人或另一个 job）。
- force push、改写远端历史。
- 同时推到多个 remote。

## 三、成本估算

### 3.1 价格表

```yaml
pricing:                       # 顶层；每百万 token 的美元价格
  "claude-opus-5-5":   { input: 0, output: 0, cache_read: 0, cache_write: 0 }
  "claude-sonnet-*":   { input: 0, output: 0, cache_read: 0, cache_write: 0 }   # 支持 * 通配
  "gpt-5*":            { input: 0, output: 0, cache_read: 0 }
```

- 模型名先精确匹配，再按通配匹配（最长的模式优先）。匹配不到的模型只显示 token，标「未定价」。
- `config validate` 检查价格非负、模式合法。改了价格表 `serve reload` 即生效。
- 估算发生在**读取时**（列表、详情、统计接口按当前价格表计算），不写进数据库。这样改价后历史数据会按新价格显示，界面上标明「按当前价格表估算」。

### 3.2 用在哪里

| 用量来源 | 有模型名吗 | 怎么算 |
|---|---|---|
| job，agent 自报了金额（claude、omp 等） | — | 用自报金额，不估算 |
| job，没有自报金额（codex 等） | 需要补：ndjson / ACP 采集时记下模型名（`usage.model`）；拿不到时用 `job run --model` 的值 | 按价格表估算，标「估算」 |
| 终端会话用量（plan 的主会话用量、会话页） | 有（`by_model`） | 逐个模型估算后相加 |
| 统计（`/v1/stats` 的 24h / 7d、首页用量条、plan 合计） | — | 自报 + 估算分开合计，展示为「$x（其中估算 $y）」 |

### 3.3 展示

- 金额后面加「≈」或「估」字样，悬停显示「按价格表估算：<模型> in/out/cache 单价」。
- 有未定价模型时，总额旁提示「另有 N 个模型未定价」，并给出设置页 / 配置示例的链接。

## 四、实施分期

1. **T1 交付核心**：
   - 配置 `projects.<k>.delivery` 与校验；
   - job 字段 `delivery`、`delivery_json` 列、事件；
   - 本机 runner 的 `merge`（复用 MergeWorktree，加可选推送）、`push`、`pr`；
   - 三个平台的 REST 客户端，token 取法见 2.4；
   - PR 正文生成；
   - 触发时机：done 后，或 accept 后；
   - `gofer job deliver`。
2. **T2 worker 上交付**：内部 exec job、结果回报、按仓库排队。
3. **T3 界面**：web job 详情、验收台、plan 卡片；`job run --deliver`、todo 和工作流字段；通知。
4. **T4 成本估算**：价格表、`usage.model` 采集、读取时估算、统计和 plan 合计、界面标注。
5. **T5 文档与验证**：
   - 文档：skill references、http-api、示例配置、CHANGELOG；
   - 测试：用 httptest 模拟三个平台的 API；本地 bare repo 作为 remote 测 push 和 merge；
   - 主机实测：用一个测试仓库开一次真实的 GitHub PR，有 GitLab / Gitea 实例时各开一次。

## 五、确认项（2026-10-12 用户确认：全部按建议）

1. **平台范围**：GitHub、GitLab、Gitea / Forgejo 三家，都走 REST API；GitHub 和 GitLab 在没有 token 环境变量时，尝试从已登录的 `gh` / `glab` 取 token。——建议就这三家。Bitbucket、Azure DevOps 暂不支持，识别不了时只推分支并给出网页链接。
2. **触发时机**：开了验收的 job 在验收通过后才交付，没开验收的在 done 且有新提交时交付。——建议如此。
3. **推送许可**：只有项目配置里 `delivery.allow_push: true` 才允许推送，job 上不能临时打开。——建议如此。
4. **merge 方式是否顺带推送基线分支**：建议默认不推（`merge` = 只在本地合并），由 `delivery.push_base: true` 显式开启。
5. **价格表默认值**：建议**不内置任何价格**（价格会变，内置的容易过时，也说不清来源），示例配置给出格式，价格由你按所用平台填写。`agent` 自报的金额照常使用。
