# job 密钥引用（`job run --secret`）设计

- issue：gofer-rjmi（JOB-06②）
- 路线图：JOB-06②（[rules-injection 设计](2026-09-25-rules-injection-and-worker-init-design.md) 的非目标「等有需要带密钥的 job 再做」）
- 状态：已实施（v0.152.0；默认值见第六节，实施记录见第八节）

## 一、问题

job 需要令牌时（交付开 PR、调外部 API、拉私有依赖），现在只有两条路，都不好：

- `job run --env TOKEN=…`：值进 `request_json`，job 详情、`job show --request`、重跑都会带着它；
- 放进 server 进程环境：SEC-01 的拒绝列表会把常见凭据变量挡在 job 之外；放行就是放给所有 job。

需要一种方式：**按名字引用** server 上保管的密钥，只有声明允许的项目能用，值不进请求、不进日志。

## 二、配置

```yaml
secrets:                        # 顶层；只写「去哪里取」，值不在这里
  gh-token:
    env: GITHUB_TOKEN           # 从执行机器的 gofer 进程环境读（可放在 <config-dir>/.env）
    as: GH_TOKEN                # job 里的变量名；省略 = 名字转大写、- 换成 _
    description: 推送与开 PR    # 可选，给人看
projects:
  demo:
    secrets: [gh-token]         # 这个项目的 job 可以引用哪些
```

- 名字 `^[a-z0-9][a-z0-9._-]{0,63}$`；`env` 必填，不能是 `GOFER_` 开头的变量（gofer 自己的凭据，如 `GOFER_SERVER_TOKEN`）；`as` 必须是合法变量名，也不能是 `GOFER_` 开头。
- `projects.<p>.secrets` 只能引用已声明的名字。
- **只能改配置文件**：`secrets` 与 `projects.*.secrets` 不可经配置写接口修改——否则任何能改配置的调用方都能把 server 进程的任意环境变量映射成密钥再取走。读视图只展示名字、`as`、`description` 与 `env` 的变量名（不展示值）。
- `gofer config validate`：执行机器上 `env` 指向的变量未设置时警告。

## 三、使用

```bash
gofer job run -p demo -a codex --secret gh-token --prompt "…"
```

- `--secret` 可重复；HTTP `secrets: ["gh-token"]`；MCP `gofer_run_job` 同名字段。
- 提交时校验：名字已声明、项目允许、本次 runner 支持。请求（`request_json`）里只有名字。
- 执行时（job 进程启动前）在执行机器上读取 `env` 的值，作为 `as` 注入 job 环境；这一步覆盖 SEC-01 拒绝列表（拒绝的是「继承」，这里是显式授予）。变量未设置或值短于 4 个字符：提交即被拒（400），错误写明缺哪个变量，不带值。
- 审计：job 事件 `job.secrets_granted`（`{names}`），job 详情可见用了哪些密钥。

## 四、脱敏

- job 的 stdout / stderr 日志在写盘时按字面量替换密钥值为 `***`（跨两次写入的值也能替换：写入器扣留末尾不足一个密钥长度的字节，下次写入或关闭时再输出）。值少于 4 个字符时不替换（太短会误伤正常输出），并在提交时拒绝这种密钥配置。
- 结构化结果、产出采集都读脱敏后的日志，所以 `result_json`、通知正文、PR 正文里同样看不到。
- 不覆盖：job 自己写进工作目录或结果目录的文件、发到网络上的内容——密钥一旦交给 job，job 能做的事 gofer 管不到；这和直接把令牌交给它一样，只是不再落进 gofer 自己的记录。

## 五、继承与限制

- 续接（resume / reject --resume / 自动续投）、重跑、故障转移、重试沿用原请求里的名字，执行时重新取值。
- 第一版只支持 **server 本机 runner**：派到 worker 的 job 带 `--secret` 提交即拒（400，说明原因）。worker 上的密钥需要 worker 自己的声明与协议字段，等有需要再做。
- 不能与 `--interactive` 同用（pty 录屏不经过日志写入器，无法脱敏）。

## 六、默认值（用户可改）

1. 只能改配置文件（与事件钩子、决策 hook 一致）。
2. 只支持本机 runner、不支持交互 job。
3. job 凭据（agent 在 job 里再提交 job）也可以引用本项目允许的密钥：允许列表是运维对「这个项目的 job 可以拿到这个密钥」的授权，与谁提交无关。

## 七、测试

- config：解析、`as` 默认值、校验（名字、`GOFER_` 前缀、项目引用未声明的名字、值过短）、不可编辑。
- job：提交时拒绝未声明 / 项目不允许 / 远程 runner / 交互；执行时注入变量；`request_json` 不含值；日志脱敏（含跨写入边界）；变量未设置时失败且错误不含值；续接继承名字。
- httpapi：配置写接口拒绝 `secrets`；读视图不含值。

## 八、实施记录（2026-10-13）

- config：`internal/config/secrets.go`（`SecretConfig`、`JobVar`、`ValidateSecrets`、`ResolveSecretValue`），`Config.Secrets`、`ProjectConfig.Secrets`；字段策略 `secrets` / `projects.*.secrets` 不可编辑（项目写接口本来就只合并它认识的字段，测试 `TestUpdateProjectKeepsSecretsFileOnly` 固定这一点）。
- job：`internal/job/secrets.go`。Submit 在 validate 之后 `checkSecrets`（项目允许、非远程、非交互）并在本机解析值（待批 job 在批准重入时才解析）；值随 `jobEntry.secretValues` 留在内存，`runReq.Env` 注入；execute 打开日志后最外层包 `secretMasker`（扣留最长值 −1 字节，Close 刷出），所以 ndjson 投影、会话捕获、verify 输出都写脱敏文本；`job.secrets_granted` 事件；续接沿用名字；决策 hook 输入带 `secrets`。
- 入口：CLI `--secret`（`-f` 任务书用 frontmatter `secrets:`），MCP `gofer_run_job.secrets`，HTTP 的 `JobRequest.secrets` 字段。`config validate` 逐个报告变量是否已设置。

**与设计的偏差**

- 变量缺失在提交时就拒绝，而不是让 job 运行后失败：调用方马上知道原因，也不留下一个失败的 job。
- `GET /v1/config` 暂不展示 `secrets`（配置读视图同样不展示 MCP 注册表）；用了哪些密钥看 job 事件。
