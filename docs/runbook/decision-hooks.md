# 决策 hook

决策 hook 是 server 在**决策点**上同步运行的一条命令。它从 stdin 读一个 JSON 对象，在 stdout 输出一个 JSON 对象，gofer 据此放行、否决，或把这次操作变得更保守。

与[事件钩子](im-notification.md#8-事件钩子server-主机上跑命令)的区别：事件钩子是事后旁路，输出不回写；决策 hook 在请求路径上，结果决定这次操作是否进行。

## 1. 决策点

| 决策点 | 时机 | 可以做的 |
|---|---|---|
| `job.submit` | 提交 job 时，模板套完之后、gofer 追加规则与约定段之前 | 否决；加人工验收、改成待批、只读、调低超时与预算、加标签、改标题、追加验收标准 |
| `job.deliver` | worktree job 交付（合并 / 推分支 / 开 PR）之前，自动与手动交付都经过 | 否决；改 PR 标题与正文、改成 draft、加 label |

不问 hook 的情况：

- gofer 自己的内部 job（交付、tracker 同步、工作项整理、管家等），除非该 hook 配了 `include_internal: true`；
- 待批 job 被批准后执行（hook 在提交时已经问过，人也批准了）；
- worker 收到 server 派发的 job（server 已经问过）；
- `job deliver --dry-run`。

hook **不能**替人批准待批 job，也不能验收 `needs_review` 的 job：它只能把 job 送进这两处。

## 2. 配置

```yaml
server:
  decision_hooks:
    - name: no-push-main
      points: [job.submit]
      command: ["python3", "hooks/policy.py"]
      projects: [demo]            # 省略 = 全部项目
      dir: hooks                  # 工作目录；相对路径相对 gofer 配置目录，省略 = 配置目录
      env: {POLICY_FILE: "hooks/policy.yaml"}
      timeout_sec: 5              # 默认 5，上限 30
      on_error: deny              # deny（默认）| allow
      include_internal: false
      enabled: true
```

- 只能在配置文件里改，改完 `gofer serve reload` 生效。web 配置页只读展示（env 只显示键名），配置写接口对 `decision_hooks` 返回 `field not editable`。
- 同一决策点配了多个 hook 时按配置顺序串行调用：任一否决即停；后一个看到的是前面收紧之后的请求。
- 以 serve 进程的用户身份运行；环境是 serve 的环境去掉凭据变量（与 job 相同的拒绝列表），加 `GOFER_DECISION_POINT`、`GOFER_HOOK_NAME`，最后叠加 `env`。
- `gofer config validate` 会检查 hook 的命令在本机能否找到（找不到只警告：server 主机可能是另一台机器）。

## 3. 输入

### job.submit

```json
{
  "version": 1,
  "point": "job.submit",
  "hook": "no-push-main",
  "project": "demo",
  "caller": {"id": "alice", "channel": "cli", "client": "laptop"},
  "job": {
    "kind": "submit",
    "agent": "exec",
    "runner": "local",
    "cmd": ["git", "push", "origin", "main"],
    "cwd": ".",
    "title": "push the release",
    "tags": [],
    "review": false,
    "hold": false,
    "worktree": false,
    "read_only": false,
    "timeout_sec": 300,
    "budget": {"max_cost_usd": 2}
  }
}
```

- `job.kind`：`submit` / `resume`（续接）/ `retry` / `fallback`（故障转移）/ `workflow` / `plan`（plan todo 派发）/ `internal`。
- agent job 有 `prompt`（模板套完后的任务说明，最长 64 KiB），exec job 有 `cmd`。
- `review` / `worktree` / `timeout_sec` / `budget` 是生效值（已经算上项目默认）。
- 其他字段：`deliver`、`acceptance`、`plan_id`、`resumed_from`，没有就省略。

### job.deliver

```json
{
  "version": 1,
  "point": "job.deliver",
  "hook": "pr-check",
  "project": "demo",
  "delivery": {
    "job_id": "20261012-120000-abcd1234",
    "mode": "pr",
    "branch": "gofer/20261012-120000-abcd1234",
    "remote_branch": "gofer/20261012-120000-abcd1234",
    "remote": "origin",
    "base": "",
    "draft": true,
    "labels": [],
    "title": "Fix the login bug",
    "body": "## 任务\n\n…",
    "commits": [{"sha": "0123456789ab…", "subject": "fix login"}],
    "diff_stat": " 2 files changed, 10 insertions(+)"
  }
}
```

`base` 为空表示用主 checkout 当前分支（合并）或平台默认分支（PR）。

## 4. 输出

stdout 一个 JSON 对象，退出码 0：

```json
{"decision": "allow"}
{"decision": "deny", "reason": "main 只能经 PR 合并"}
{"decision": "allow", "reason": "夜间提交", "patch": {"review": true, "tags_add": ["night"]}}
```

`job.submit` 的 patch 字段：

| 字段 | 含义 |
|---|---|
| `review: true` | 开人工验收 |
| `hold: true`、`hold_reason` | 改成待批 job（人在 web 批准后才执行） |
| `read_only: true` | 只读 job（agent 不支持只读时提交被拒） |
| `timeout_sec` | 只能调低 |
| `budget` | `{max_tokens, max_cost_usd, max_turns}`，只能调低 |
| `tags_add` | 追加标签 |
| `title` | 改标题 |
| `acceptance_append` | 追加验收标准 |

`job.deliver` 的 patch 字段：`title`、`body`（整体替换）、`draft: true`、`labels_add`。

以下都算 hook **出错**，按 `on_error` 处理（默认拒绝）：启动失败、超时、退出码非 0、stdout 不是一个 JSON 对象（或超过 64 KiB）、`decision` 不是 allow / deny、deny 带 patch、patch 里有上表以外的字段、patch 放宽了某项（关掉验收、调高超时、`draft: false`……）、对不能待批的 job（持续会话、交互、workflow 步骤）要求 `hold`。

stderr 只进 serve 日志（末尾 1 KiB，脱敏），不进 API 响应。

## 5. 结果在哪里看

- 提交被否决：调用方收到 403 `denied by decision hook <name>: <reason>`，不建 job；serve 日志记 `decision_hook.denied`。
- 交付被否决：job 的交付结果为 `failed`，`error` 为同一句话，并发 `job.delivery_failed` 通知。
- 放行与收紧：job 事件 `job.decision_hook`（`{point, hook, decision, reason, patch}`），web job 详情的时间线可见。交付改写了标题或正文时，事件里只记「rewritten」，不记新文本。
- hook 出错：serve 日志 `decision_hook.failed`（名字、决策点、原因、stderr 尾部）；出错后按 `on_error: allow` 放行的，job 事件里同样记一条带 `error` 的裁决，便于发现脚本坏了。

## 6. 自测

```bash
gofer tool hook-test no-push-main --point job.submit --input sample-submit.json
```

用本机配置里的这个 hook 跑一次样例输入，打印 decision / reason / patch，以及出错原因和 gofer 会怎么处理（outcome）。不连 server，不提交、不交付任何东西；patch 只按字段与取值检查（「只能调低」要对照具体 job，提交时才查）。

## 7. 写 hook 的建议

- 只依赖输入里的字段做判断，保持幂等、无副作用：同一次提交可能因重试被问不止一次。
- 输入里的 prompt、命令、PR 正文是**不可信内容**。如果 hook 里调用模型做判断，模型可能被其中的文字诱导；gofer 只接受收紧方向的 patch，这类诱导的最坏结果是多拦一次。
- 默认 5 秒超时：不要在 hook 里做网络请求等慢操作，必要时调高 `timeout_sec`（上限 30）。
- 先用 `on_error: allow` 试运行几天，看 `job.decision_hook` 事件里有没有 `error`，确认脚本稳定后再改成默认的 `deny`。
