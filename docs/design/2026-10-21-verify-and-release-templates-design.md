<!-- template_id: design; template_version: 1.1.1 -->
# verify 与 release 通用模板 设计

> 状态：Draft 0.2 / 待用户批准
> issue：gofer-byev、gofer-51b0

- 思考模式：rigorous。
- 核心目标：任何项目都能把「对一个提交做全套检查」和「打 tag 之后把版本发出去」写成仓库里的 gofer workflow（`.gofer/workflows/verify.yaml`、`release.yaml`），跟代码一起版本化；gofer 提供几个内置步骤（`uses: gofer/...`）承担机械部分，并给出起步文件。
- scope freeze：只做内置步骤、起步文件生成、内置的通用 verify / release 模板与汇总展示；不在 server 的 `projects` 配置里新增字段；gofer 自身的发版脚本保持在工作区脚本里。
- review budget：本稿最多一轮独立评审；评审后修订由作者自查。
- 停止条件：用户对本稿给出具名的批准，或明确要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-21 | Claude（监督会话） | 初稿：项目 `checks` 与 `release` 配置；`verify` 模板（并行检查 + 汇总表）；`release` 模板（检查 tag → 推送重试 → 等 CI → 等发布产物 → 追加步骤） |
| 0.2 | 2026-10-21 | Claude（监督会话） | 按用户意见改为仓库内 workflow：去掉 server 项目配置 `checks` / `release`；新增内置步骤 `uses: gofer/summary|push|wait-ci|wait-assets`、步骤 `ref:`（在指定提交的独立检出上执行）、`gofer workflow init verify|release` |

## 背景

每次发版前后，监督者手工做同样的事：

- **verify**：导出提交，在 Linux 跑 `go test -race`；`GOOS=darwin/windows go vet`；在 Windows 主机跑全量测试；web 的类型检查 / 测试 / 构建——四件事分别提交、分别等、分别看结果。
- **release**：推送 main 与 tag（经代理，失败要重试）；等 CI 与 code scanning；等发布产物；项目自己的后续步骤。

gofer 自身的发版已收进工作区脚本，但这些步骤其他项目（suga、eget 等）也在重复。

## 现有能力

- workflow：步骤、依赖、扇出（`fan` / `agents`）、`join`、在 worktree 或指定 ref 上执行、exec 命令、`${steps.<name>.<field>}` 引用结果（`status`、`exit_code`、`result_dir`、`stdout` …）。
- 内置模板：`compare`、`plan-implement`、`review-committee`；项目 `.gofer/workflows/` 与全局目录可放自定义模板并覆盖同名内置模板。
- 项目配置已有 `verify`（agent job 结束后的默认验证步骤）、`delivery`（推送与代理）。

## 方案

用户 2026-10-21 指出：放进 server 的项目配置太复杂，看起来像 GitHub Actions，应该放在项目的 `.gofer/` 里，或者它本身就是一个 gofer workflow。据此：**检查与发布写成仓库里的 workflow 文件**，gofer 只补 workflow 缺的几块能力。

### 一、workflow 补的能力

1. **步骤 `ref:`**：exec 步骤在指定提交的独立检出上执行（server 本机：临时 worktree，结束删除；worker：执行机上的临时 worktree），不碰主 checkout。值可以是 sha、tag、分支或 `${vars.commit}`。
2. **内置步骤 `uses: gofer/<名>`**（不起 agent，由 gofer 自己执行；参数用 `with:`）：
   - `gofer/summary`：等前面列出的步骤（默认全部）结束，生成结果表——步骤名、runner、状态、退出码、耗时、失败用例（从输出抽取 `--- FAIL:`、`FAIL`、`✗`、`error` 等行，最多 20 行）、日志位置；写进 workflow 结果，web workflow 详情与 `gofer workflow show` 展示。
   - `gofer/push`：`with: {refs: [main, "v${vars.version}"], remote: origin, runner: <r>, retries: 24, retry_sec: 300}`；经项目 `delivery.proxy` 推送，按 git 输出确认每个 ref 都已推送（不只看退出码），失败按次数重试。
   - `gofer/wait-ci`：`with: {repo: <owner/name>, commit: <sha|ref>, required: [Unit-Tests, CodeQL], code_scanning: true, timeout_min: 40}`；在执行机上用已登录的 `gh` 查询，直到全部结束；必需的 workflow 失败或有未处理告警即失败。
   - `gofer/wait-assets`：`with: {repo, tag, assets: ["*-linux-amd64.tar.gz"], timeout_min: 30}`；等 GitHub Release 上出现这些产物。
3. **失败策略**：步骤可写 `continue_on_failure: true`（verify 的各项检查用它：一项失败其余照常跑完）；默认仍是失败即停。

### 二、内置模板与起步文件

- 内置 `verify` 与 `release` 模板保持通用、最小（verify：一个 `go test ./...` 或 `npm test` 之类按仓库类型选择的检查 + summary；release：push → wait-ci → summary），供没写自己文件的项目直接用。
- `gofer workflow init verify|release [--force]`：在当前仓库生成 `.gofer/workflows/verify.yaml` / `release.yaml` 起步文件，按仓库内容预填常见检查（有 go.mod → `go test` / `go vet`；有 package.json → 对应脚本；有 `.github/workflows` → release 的 `wait-ci.required` 预填其 workflow 名），并带注释说明每一段。项目里的同名文件覆盖内置模板（现有查找规则）。
- 运行：`gofer workflow run --template verify -p <p> --var commit=<sha>`；`gofer workflow run --template release -p <p> --var version=<X.Y.Z>`。

示例（`.gofer/workflows/verify.yaml`）：

```yaml
title: "verify ${vars.commit}"
vars:
  commit: {required: true, desc: 要检查的提交}
steps:
  - name: unit
    agent: exec
    ref: ${vars.commit}
    continue_on_failure: true
    cmd: [go, test, -race, -count=1, -timeout, 30m, ./...]
  - name: vet-darwin
    agent: exec
    ref: ${vars.commit}
    continue_on_failure: true
    env: {GOOS: darwin}
    cmd: [go, vet, ./...]
  - name: windows
    agent: exec
    runner: <windows worker>
    ref: ${vars.commit}
    continue_on_failure: true
    cmd: [go, test, -count=1, ./...]
  - name: summary
    uses: gofer/summary
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
  - name: ci
    uses: gofer/wait-ci
    with: {repo: owner/name, commit: "v${vars.version}", required: [Unit-Tests], code_scanning: true}
  - name: assets
    uses: gofer/wait-assets
    with: {repo: owner/name, tag: "v${vars.version}", assets: ["*-linux-amd64.tar.gz"]}
  - name: mirror                  # 项目自己的追加步骤
    agent: exec
    runner: local
    cmd: [sh, -c, "./scripts/upload-mirror.sh ${vars.version}"]
  - name: summary
    uses: gofer/summary
```

## 范围与非目标

- 范围：步骤 `ref:` 与 `continue_on_failure`、四个内置步骤、内置 verify / release 模板、`gofer workflow init`、汇总表在 web 与 CLI 的展示、文档。
- 非目标：server 项目配置新增字段；GitHub 以外的平台（内置步骤的 `with` 预留 `provider`）；改写 gofer 自身的发版脚本。

## 测试计划

- 配置解析与校验（argv、runner 合法、`ci_required` 名称、assets glob）。
- verify：并行、失败不影响其余检查、汇总表与失败用例抽取、独立检出与清理。
- release：tag 检查、推送确认与重试、CI 等待（用假的 `gh` 输出）、告警、assets 等待、post 步骤。
- 不连真实 GitHub：`gh` 调用通过可替换的命令前缀在测试里换成假程序（G053）。

## 文档

- skill references：plans-workflows（两个模板的用法）、server-config（`checks` / `release` 配置）；http-api（如有新字段）；CHANGELOG。

## 决策

以下为本稿的提案，待用户确认：

1. **检查与发布写成仓库里的 workflow 文件**（用户 2026-10-21 提出），不在 server 配置里加字段。
2. **内置步骤用 `uses: gofer/<名>` + `with:`**（形似 GitHub Actions，降低上手成本）；第一批四个：summary、push、wait-ci、wait-assets。
3. **verify 的检查用 `continue_on_failure: true`**，一次看到全部失败；其余场景默认失败即停。
4. **CI 与发布产物用执行机上已登录的 `gh` 查询**，gofer 不保存 GitHub token；先只支持 GitHub。
5. **`gofer workflow init` 按仓库内容预填**起步文件，生成后由项目自己维护。
6. **gofer 自身发版暂不改用模板**，等内置步骤稳定后再评估。

## 结论与人工计划 Gate

本稿经用户具名批准后写实施计划；设计批准不授权实施。
