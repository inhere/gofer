<!-- template_id: design; template_version: 1.1.1 -->
# verify 与 release 通用模板 设计

> 状态：Draft 0.1 / 待用户批准
> issue：gofer-byev、gofer-51b0

- 思考模式：rigorous。
- 核心目标：任何项目都能用两个内置 workflow 模板，把「对一个提交做全套检查」和「打 tag 之后把版本发出去」交给 gofer：并行执行、失败即停、最后给一张结果表；项目只需要在配置里写自己的检查命令与发布步骤。
- scope freeze：只做两个模板与它们需要的项目配置；gofer 自身的发版脚本（mirror、一键更新）保持在工作区脚本里。
- review budget：本稿最多一轮独立评审；评审后修订由作者自查。
- 停止条件：用户对本稿给出具名的批准，或明确要求停止。

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-21 | Claude（监督会话） | 初稿：项目 `checks` 与 `release` 配置；`verify` 模板（并行检查 + 汇总表）；`release` 模板（检查 tag → 推送重试 → 等 CI → 等发布产物 → 追加步骤） |

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

### 一、项目配置

```yaml
projects:
  <project>:
    checks:                          # 命名的检查，verify 模板按名字取用
      unit:
        runner: <worker 或 local>    # 在哪台机器跑；省略 = 项目默认 runner
        cmd: [go, test, -race, -count=1, -timeout, 30m, ./...]
        cwd: .                       # 相对项目根
        timeout_sec: 3600
      vet-darwin:
        cmd: [go, vet, ./...]
        env: {GOOS: darwin}
      web:
        cwd: web
        cmd: [sh, -c, "npx vue-tsc --noEmit && npx vitest run && npx vite build"]
    release:
      repo: <owner>/<name>           # GitHub 仓库（CI 与发布产物）
      branch: main                   # 与 tag 一起推送的分支
      push_runner: local             # 在哪台机器推送（需要能连上远端）
      push_retries: 24               # 推送失败重试次数（间隔 push_retry_sec，默认 300）
      ci_required: [Unit-Tests, CodeQL]   # 必须成功的 workflow 名；省略 = 该提交的全部 run
      code_scanning: true            # 有未处理的告警即失败
      assets: ["*-linux-amd64.tar.gz", "*-windows-amd64.zip"]   # 等这些发布产物出现（glob）；省略 = 不等
      post:                          # 发布之后的追加步骤（可选），与 checks 同形
        - name: mirror
          runner: local
          cmd: [cmd, /c, "scripts\\upload-mirror.cmd", "${vars.version}"]
```

- `cmd` 是 argv 数组（不经 shell）；需要 shell 时显式写 `sh -c` / `cmd /c`。`${vars.commit}`、`${vars.version}` 在模板展开时替换。
- 命令需要项目 `allow_exec: true`；受调用方上限（ceiling）约束与普通 exec job 相同。

### 二、`verify` 模板

```
gofer workflow run --template verify --var project=<p> --var commit=<sha|ref> [--var checks=unit,web]
```

- 每个检查一个并行的 exec 步骤，在该提交的独立检出上执行（server 本机用 worktree；worker 上用 worker 自己的检出 + `git checkout --detach <commit>` 到临时 worktree，结束清理），互不影响主 checkout。
- 汇总步骤（内置，不起 agent）：生成一张表——检查名、runner、状态、退出码、耗时、失败用例（从输出里抽取 `--- FAIL:` / `FAIL` / `✗` 等行，最多 20 行）、日志路径；写进 workflow 的结果，web 的 workflow 详情页展示，CLI `gofer workflow show` 打印。
- 任一检查失败，workflow 结果为失败，但其余检查照常跑完（一次看到全部问题）。

### 三、`release` 模板

```
gofer workflow run --template release --var project=<p> --var version=<X.Y.Z>
```

步骤依次执行，任一步失败即停：

1. **check**：tag `v<version>` 存在且指向 `release.branch` 上的提交；工作区不需要干净（只读检查）。
2. **push**：在 `push_runner` 上推送分支与 tag，经项目 `delivery.proxy`；按输出确认两个 ref 都已推送（不只看退出码）；失败按 `push_retries` 重试。
3. **ci**：在一个 job 里等该提交的 CI（`gh run list`，需要执行机已登录 `gh`）直到全部结束；`ci_required` 有失败即停；`code_scanning: true` 时有未处理告警即停。
4. **assets**（可选）：等 GitHub Release 上出现 `assets` 列出的产物。
5. **post**（可选）：依次执行追加步骤。

结束时汇总同样的表格。

## 范围与非目标

- 范围：两个模板、项目 `checks` / `release` 配置及校验、汇总表、web 展示、文档。
- 非目标：GitHub 以外的托管平台（GitLab / Gitea 先不做，配置里留 `provider` 字段以后扩展）；改写 gofer 自身的发版脚本（它可以以后改成调用 `release` 模板 + post 步骤，另议）。

## 测试计划

- 配置解析与校验（argv、runner 合法、`ci_required` 名称、assets glob）。
- verify：并行、失败不影响其余检查、汇总表与失败用例抽取、独立检出与清理。
- release：tag 检查、推送确认与重试、CI 等待（用假的 `gh` 输出）、告警、assets 等待、post 步骤。
- 不连真实 GitHub：`gh` 调用通过可替换的命令前缀在测试里换成假程序（G053）。

## 文档

- skill references：plans-workflows（两个模板的用法）、server-config（`checks` / `release` 配置）；http-api（如有新字段）；CHANGELOG。

## 决策

以下为本稿的提案，待用户确认：

1. **检查与发布步骤写在项目配置里（推荐）**，模板只负责编排；备选：每个项目自己在 `.gofer/workflows/` 写完整模板——更灵活但每个项目都要重写一遍编排。
2. **verify 失败后其余检查照常跑完**，最后一次性汇总。
3. **release 依赖执行机上已登录的 `gh`** 查询 CI 与发布产物；不在 gofer 里保存 GitHub token。
4. **先只支持 GitHub**，配置预留 `provider`。
5. **gofer 自身发版暂不改用模板**，等模板稳定后再评估。

## 结论与人工计划 Gate

本稿经用户具名批准后写实施计划；设计批准不授权实施。
