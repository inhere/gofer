<!-- template_id: design; template_version: 1.1.1 -->
# verify 与 release 通用模板 设计

> 状态：Draft 0.3 / 待用户批准
> issue：gofer-byev、gofer-51b0

- 思考模式：rigorous。
- 核心目标：任何项目都能把「对一个提交做全套检查」和「打 tag 之后把版本发出去」写成仓库里的 gofer workflow（`.gofer/workflows/verify.yaml`、`release.yaml`），跟代码一起版本化；gofer 补齐 workflow 缺的几块能力（每路不同命令的扇出、临时检出、内置步骤、结果表、续跑），并让模板能在 web 上查看与编辑。
- scope freeze：不在 server 的 `projects` 配置里新增字段；gofer 自身的发版脚本保持在工作区脚本里。
- review budget：0.2 已做一轮独立评审（2026-10-21，见「评审吸收」）；0.3 起由作者自查。
- 停止条件：用户对本稿给出具名的批准，或明确要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-21 | Claude（监督会话） | 初稿：项目 `checks` / `release` 配置 |
| 0.2 | 2026-10-21 | Claude（监督会话） | 按用户意见改为仓库内 workflow；内置步骤 `uses:`；`gofer workflow init` |
| 0.3 | 2026-10-21 | Claude（监督会话） | 吸收独立评审（并行改用扇出、删 `continue_on_failure`、内置步骤走内部 exec job、summary 改为结果视图、`ref:` 临时检出、env 下发 worker、push 推 tag 与核对、平台 API 代替 `gh`、顶层 `project:`、信任模型）；用户确认 D1–D6 取推荐值；新增 D7 web 查看 / 编辑模板 |

## 背景

每次发版前后，监督者手工做同样的事：导出提交跑 Linux 测试、多平台 vet、Windows 全量、web 检查（verify）；推送 main 与 tag、等 CI 与 code scanning、等发布产物、项目自己的后续步骤（release）。gofer 自身的发版已收进工作区脚本，其他项目也在重复这些事。

用户 2026-10-21：放进 server 项目配置太复杂，应该放在项目的 `.gofer/` 里，或者它本身就是一个 gofer workflow；写好的 workflow 文件希望能在 web 上看到和修改。

## 现有能力（2026-10-21 核对，评审已对照代码确认）

- workflow 是**严格串行**的步骤列表（一次只有一个活跃步骤，`internal/job/workflow/types.go` Spec 注释），**没有步骤依赖**。
- 步骤内可**扇出**：`fan:`（每路目前只能换 `agent` / `runner`）或 `agents:`；`join: all`（默认）等全部 fan 结束、任一失败则该步失败；`join: pick` 择优。
- 失败策略 `on_failure: fail | continue | retry`（`retry` 带 `max_attempts` / `backoff_sec`）；`continue` 跳过失败步骤继续，最后一步被跳过时 workflow 仍按成功结束。
- 步骤可 `worktree: true` / `worktree_base`（会建分支 `gofer/<job-id>`，保留期清理时才删；base 必须是执行机本地已有的提交）。
- 模板查找：项目 `.gofer/workflows/` → 全局 `<config-dir>/workflows/` → 内置；`GET /v1/workflow-templates`、`POST …/{name}/render`。web「新建 workflow」可从模板生成表单或直接粘贴 YAML，提交后是一条**运行记录**（spec 存数据库，可 `export`），不保存为模板；模板在 web 上**只能列出与预览**，看不到源文件、不能编辑。
- 内部 exec job：交付（`deliverRemote`）以「引擎提交一个 `gofer tool deliver` exec job」的方式在执行机上运行 gofer 自己的逻辑，并有封闭的 `InternalExec` 类别豁免 exec 维度的上限检查。
- delivery：平台识别（github / gitlab / gitea）、token 解析（`GITHUB_TOKEN` / `GH_TOKEN` → `gh auth token`）、代理 HTTP 客户端；`Push` 目前只推 `refs/heads/<dst>`。

## 方案

### 一、workflow 补的能力

1. **顶层 `project:`**：各步 `project_key` 的默认值；从某项目 `.gofer/workflows` 读出的模板自动以该项目为默认；`gofer workflow run` 增加 `-p`。
2. **扇出每路可不同**：`fan[]` 项增加 `name`、`cmd`、`env`、`cwd`、`timeout_sec`（exec 时 `agent` 可省略，默认 `exec`）。verify 的多项检查写成**一个步骤的多路 fan**，配合已有的 `join: all`：全部跑完，任一失败则整体失败。
3. **`ref:` 临时检出**：在指定提交上建「无分支、detached、job 结束即删」的临时 worktree 执行；提交 workflow 时把 ref 解析成 sha 存进 spec，保证各路检查的是同一个提交；与 `worktree: true` 同写时报错。worker 上执行时先 `git fetch` 取该提交，取不到即失败并说明「该提交不在远端，先推送或只在 server 本机检查」（D3）。
4. **`env:`**：`StepSpec` 与 `FanSpec` 增加 `env`，随下发请求带到 worker（worker 协议升到 v26）；env 会随请求落库，文档注明不要放秘密。
5. **内置步骤 `uses: gofer/<名>` + `with:`**：引擎为它提交一个**内部 exec job**（`gofer tool step <名>`，`with` 以 JSON 走 stdin，结果取 stdout 最后一行 JSON），在步骤的 `runner:` 上执行，新增 `InternalExec` 类别 `workflow-step`。日志、取消、超时、`on_failure: retry`、worker、`${steps.X.…}` 引用全部沿用。worker 需支持该子命令（按协议版本门槛判断）。第一批三个：
   - `gofer/push`：`with: {refs: [main, "v${vars.version}"], remote: origin}`；推分支与 tag（tag 用 `refs/tags/`，永不强推），推后用 `ls-remote` 核对每个 ref 的 sha；必须项目 `delivery.allow_push` 且调用方持有新权限 `repo.push`（D4）；代理用项目 `delivery.proxy`；重试用步骤的 `on_failure: retry`（每次尝试是独立 job，不受单 job 时限累积影响）。
   - `gofer/wait-ci`：`with: {ref: "v${vars.version}", required: [Unit-Tests], code_scanning: true, timeout_min: 40}`；仓库从 remote URL 推导；用 delivery 的平台 API 与 token 解析查询（D5），直到全部结束；必需 workflow 失败即失败；code scanning 只看**该 ref 新增**的告警。
   - `gofer/wait-assets`：`with: {tag, assets: ["*-linux-amd64.tar.gz"], timeout_min: 30}`；等 Release 上出现这些产物。
   - 注册表：每个内置步骤登记 `Name`、`with` 的结构（严格解码，未知键报错）、`Validate`、执行位置、最低 worker 协议版本；以后加 notify / upload 只需登记一项。
6. **结果表是视图，不是步骤**：workflow 详情（web `WorkflowDetail`、`gofer wf show`）的每个步骤与每路 fan 显示 runner、状态、退出码、耗时、失败摘录（从日志尾部按规则抽取 `--- FAIL:`、`FAIL`、`✗`、`error` 等，最多 20 行）与日志入口。失败时也完整显示（fail-fast 不影响）。
7. **续跑**：`gofer wf rerun <id> [--failed | --from <step>]`：基于原 spec 裁剪出新的 workflow 提交（只跑失败的 fan / 从某一步起），不改原 workflow 的推进状态；web 详情页对应按钮。
8. **`run: always`**（可选）：步骤即使前面失败也执行（例如 release 失败后的清理 / 通知），不改变 workflow 的最终结论。
9. **渲染与校验覆盖新字段**：`ref`、`env`、`uses`、`with`（递归）、`fan[].cmd` 进入模板渲染、残留占位符检查与 `${steps…}` 静态校验。

### 二、模板与起步文件

- 内置 `verify`：只有一个 `cmd` 变量的最小模板（单路检查）；内置 `release`：push → wait-ci。项目写自己的文件即可覆盖（同名优先）。
- `gofer workflow init verify|release [--force]`：在当前仓库生成起步文件，按仓库内容预填（有 go.mod → `go test` / `go vet` 多路 fan；有 package.json → 其 test / build 脚本；有 `.github/workflows` → wait-ci 的 `required` 预填其 workflow 名），带注释；生成后提示「提交后 server 才能读到」（server 从项目工作区读模板）。

示例（`.gofer/workflows/verify.yaml`）：

```yaml
title: "verify ${vars.commit}"
vars:
  commit: {required: true, desc: 要检查的提交}
steps:
  - name: checks
    agent: exec
    ref: ${vars.commit}          # 解析为 sha；临时检出，结束即删
    join: all                    # 全部跑完，任一失败则整体失败
    fan:
      - {name: unit,       cmd: [go, test, -race, -count=1, ./...]}
      - {name: vet-darwin, cmd: [go, vet, ./...], env: {GOOS: darwin}}
      - {name: windows,    runner: <windows runner>, cmd: [go, test, -count=1, ./...]}
      - {name: web,        cwd: web, cmd: [sh, -c, "npx vue-tsc --noEmit && npx vitest run"]}
```

示例（`.gofer/workflows/release.yaml`）：

```yaml
title: "release v${vars.version}"
vars:
  version: {required: true}
steps:
  - name: push
    uses: gofer/push
    with: {refs: [main, "v${vars.version}"]}
    on_failure: retry
    retry: {max_attempts: 10, backoff_sec: 300}
  - name: ci
    uses: gofer/wait-ci
    with: {ref: "v${vars.version}", required: [Unit-Tests], code_scanning: true}
  - name: assets
    uses: gofer/wait-assets
    with: {tag: "v${vars.version}", assets: ["*-linux-amd64.tar.gz"]}
  - name: mirror                 # 项目自己的追加步骤
    agent: exec
    runner: local
    cmd: [sh, -c, "./scripts/upload-mirror.sh ${vars.version}"]
```

运行：`gofer wf run --template verify -p <p> --var commit=<sha>`；`gofer wf run --template release -p <p> --var version=<X.Y.Z>`。

### 三、与 GitHub Actions 的对照

| 写法 | gofer | GitHub Actions |
|---|---|---|
| `uses` | 只接受 `gofer/<名>`（以后可加 `./.gofer/steps/x.yaml`），版本随 gofer 二进制，没有 `@v1` | 任意 action 仓库 + 版本 |
| `with` | 按每个内置步骤的结构严格校验，未知键报错 | action 自己决定 |
| 失败处理 | `on_failure: fail | continue | retry`；`run: always` | `continue-on-error`、`if: always()` |
| 并行 | 同一步骤内的 `fan` 并行，步骤之间串行 | 多个 job + `needs` |
| 执行位置 | 步骤 / fan 的 `runner:`（server 或某台 worker） | `runs-on` |
| 结果 | workflow 详情的结果表 | 各 job 的日志页 |

### 四、web 查看与编辑模板（D7）

- 「新建 workflow」的模板列表可查看**源文件**（标明来源：内置 / 全局 / 项目）。
- **全局模板**（server `<config-dir>/workflows/`）：可在 web 新建、编辑、删除；保存前校验（解析、渲染、步骤与内置步骤参数检查），校验不过不保存；需要 `config.write` 一类的管理员权限；写入记审计。
- **项目模板**（仓库 `.gofer/workflows/`）：可查看源文件、在 web 编辑；保存时写入 server 主机上该项目仓库的工作区，结果是**未提交的改动**，页面明确提示「提交到仓库才算数」并给出提交命令；不在 web 上替用户提交。需要的权限同上，受调用方上限（项目范围）约束。
- **运行记录另存为模板**：workflow 详情页「另存为模板」（基于现有 `export`），选择全局或项目，走同样的保存与校验。
- 内置模板只读。

### 五、信任模型（安全）

- server 从**项目主 checkout 当前的工作区**读模板，不是从被检查的那个提交读；改了模板的未提交改动会立即生效。
- 能改仓库里 `release.yaml` 的人，就能改发版时执行的命令；普通 exec 步骤仍受项目 `allow_exec` 与调用方上限约束，但有 exec 权限的调用方能在 `runner: local` 上执行任意命令——这是现有 exec 的边界，文档写明。
- 推送 main / tag 这类非 `gofer/*` 分支：项目 `delivery.allow_push` **且**调用方持有新权限 `repo.push`（中风险，不默认发给任何人，登记在权限目录，G056）。
- web 与 CLI 运行 release 前先展示渲染后的步骤（已有 render 接口），确认后再提交。

## 范围与非目标

- 范围：第一节 1–9、第二节、第四节、第五节的权限与提示、文档。
- 非目标：步骤依赖图（DAG）；GitHub 以外平台的实际实现（内置步骤按 provider 分支，先做 github）；改写 gofer 自身的发版脚本；矩阵语法（以后在渲染阶段展开为 fan，不改引擎）；项目自定义可复用步骤（`uses: ./...`，以后做，需先修模板读取不展开 `file:` 的问题）。

## 测试计划

- 扇出每路不同 cmd / env / cwd、`join: all` 的整体判定；`ref:` 解析为 sha、临时检出与清理、worker fetch 成功 / 失败；env 下发 worker。
- 内置步骤：注册与 `with` 严格校验；push 推分支与 tag、`ls-remote` 核对、`allow_push` 与 `repo.push` 门槛、retry；wait-ci / wait-assets 用 httptest 假平台（G053，不连真实 GitHub）、超时、只看新增告警。
- 结果表字段与失败摘录；`wf rerun --failed / --from`；`run: always`。
- 渲染与校验覆盖新字段。
- web：模板源文件查看、全局模板编辑保存与校验失败、项目模板保存为未提交改动与提示、另存为模板、权限与只读。
- `workflow init` 按仓库内容预填。

## 文档

- skill references：plans-workflows（workflow 新能力、内置步骤、对照表、verify / release 示例、信任模型）、web-console（模板查看与编辑）；`docs/reference/http-api.md`（模板读写接口、结果表字段、rerun）；权限文档（`repo.push`，go generate）；CHANGELOG。

## 评审吸收（0.2 → 0.3）

| 评审意见 | 处理 |
|---|---|
| 引擎串行、没有依赖，verify 无法并行 | 并行改用一个步骤内的多路 fan + `join: all`（D1） |
| `continue_on_failure` 与 `on_failure: continue` 重复且语义不对 | 删除；沿用 `on_failure`；另加 `run: always`（D6） |
| 内置步骤执行位置未定义 | 内部 exec job `gofer tool step`（D2） |
| `workflow run` 没有 `-p`、模板未声明 project | 顶层 `project:` + CLI `-p` |
| summary 做成步骤：失败时不跑、workflow 无结果字段 | 改为详情视图的结果表 |
| `ref:` 复用 worktree 会留分支、worker 无未推送提交 | 临时无分支检出、提交时解析 sha、worker 先 fetch（D3） |
| `env` 不下发 worker | `StepSpec` / `FanSpec` / Dispatch 加 env，协议 v26 |
| push 只能推分支、未提 `allow_push`、重试累积超时限 | 推 tag + `ls-remote` 核对 + `allow_push` + `repo.push`；重试用 `on_failure: retry`（D4） |
| 依赖 `gh` | 改用 delivery 的平台 API 与 token 解析（D5）；只看新增告警 |
| 渲染与校验未覆盖新字段 | 第一节第 9 条 |
| release.yaml 来自仓库：提交权限 ≈ 发版权限 | 第五节信任模型 |
| 无法只重跑失败的检查 | `wf rerun --failed / --from` |

## 决策

1. **D1** verify 并行：扩展 fan，每路可有自己的命令（用户已确认推荐）。
2. **D2** 内置步骤走内部 exec job；summary 改为结果视图（已确认）。
3. **D3** worker 只检查远端已有的提交，先 fetch，取不到报错说明（已确认）；server 打包传送以后再议。
4. **D4** 推送 main / tag 需要 `allow_push` + 新权限 `repo.push`（已确认）。
5. **D5** CI 与产物查询复用 delivery 平台 API（已确认）。
6. **D6** 删 `continue_on_failure`，沿用 `on_failure`，另加 `run: always`（已确认）。
7. **D7（新，待确认）** web 查看与编辑模板：全局模板可编辑保存；项目模板保存为工作区里的未提交改动并提示提交；运行记录可另存为模板；内置模板只读。备选：web 只读查看源文件，编辑一律在仓库里改。推荐：前者。

## 结论与人工计划 Gate

本稿经用户具名批准（含 D7）后写实施计划；设计批准不授权实施。
