# AUTO-04b 决策 hook 设计

- issue：gofer-sr0b
- 路线图：AUTO-04b（N4）；前置 AUTO-04 事件钩子（只读旁路，[设计稿](2026-10-11-event-hooks-design.md)）
- 状态：设计已确认（2026-10-12：失败默认值、只收紧不放宽、分期均按建议）；D1 已实施（v0.149.0，见第十一节），D2 / D3 待做

## 一、问题

AUTO-04 的事件钩子只能「事后旁观」：命令输出不回写 gofer。运维想要的另一类自动化是**在决策点上拍板**，例如：

- 提交前：禁止某些项目跑 `git push` / `rm -rf` 一类命令；夜间提交的 job 一律加 `--review`；超过一定超时的 job 改成待批；按规则给 job 补标签。
- 交付前：PR 正文里出现内网地址、密钥形态时拒绝推送或改写正文（v0.146 出过一次把注入规则与内网链接写进公开 PR 的事故）；禁止往某些分支开 PR。
- 交互 / 授权：ACP 工具审批、终端会话的授权请求，按命令内容自动放行明显安全的（只读 `git status`、`go test`），明显危险的直接拒绝，其余照常问人。

这些规则因人因项目而异，写进 gofer 本体既不通用也不够灵活。决策 hook 让运维用一段脚本表达规则，gofer 在决策点同步调用它并按返回结果行事。

## 二、设计原则

1. **只收紧，不放宽权限**。hook 能否决、能加审查 / 待批 / 只读 / 更短超时，但不能改 agent、runner、项目、命令、cwd、环境变量，也不能替人批准待批 job 或验收 job。理由：hook 的输入里有不可信内容（prompt、命令、PR 正文），若 hook 本身调用模型做判断，可能被注入；把它能做的事限定在「更保守」一侧，被注入的最坏结果是多拦一次，而不是越权执行。
2. **同步、有界**。决策点在请求路径上，hook 必须快：默认超时 5 秒，上限 30 秒（交互 / 授权点上限 120 秒）。
3. **失败按配置处理，默认关闭**（fail-closed）：hook 启动失败、超时、退出码非 0、输出不是合法 JSON，按该 hook 的 `on_error` 处理，默认 `deny`（交互 / 授权点默认 `ask`：交回人决定）。
4. **与 AUTO-04 同一信任模型**：hook 只能写在 server 配置文件里，配置写接口一律拒绝；以 serve 进程的用户身份运行，环境去掉凭据变量。
5. **每次裁决都留痕**：job 事件 / 审计日志 / serve.log 都能看到哪个 hook 做了什么决定、理由是什么。

## 三、决策点

| 决策点 | 时机 | hook 可以返回 | 不做 |
|---|---|---|---|
| `job.submit` | Submit 里模板、验收标准、verify 解析之后，规则注入与准入校验之前 | `allow` / `deny` + 收紧补丁（见四） | 改命令、agent、runner、项目、cwd、env |
| `job.deliver` | `deliveryPlan` 生成之后、执行（本机）或派发（worker）之前；自动交付与手动交付都经过 | `allow` / `deny` / 改写 PR 标题与正文、改为 draft、加 label | 改目标分支、remote、交付模式（除了降为 `none`，即 deny） |
| `job.interaction` | ACP 权限请求 / agent 选择题开卡之后、升级给 supervisor 或人之前 | `answer`（选一个 option）/ `reject` / `ask`（照常问人） | 回答自由文本问题 |
| `session.permission` | 终端会话的授权请求与选择题（PermissionRequest / AskUserQuestion）上报 server、开 web 卡之前 | `allow` / `deny` / `ask` | 回答 AskUserQuestion（只能 `ask`） |

明确**不做**：

- 待批 job（`awaiting_approval`）的批准 / 拒绝，job 验收（`needs_review` 的 accept / reject）：这两处的意义就是「人来拍板」，hook 只能在提交时把 job **送进**这两处，不能代人出来。
- 「按命令输出改变流程」中 job 结束后的重试 / 续接决策：verify、自动续投、故障转移已覆盖主要需求，等有具体场景再加。
- codex 等没有 hook 机制的 agent 的终端授权。

## 四、协议

### 配置

```yaml
server:
  decision_hooks:
    - name: no-push                           # 必填，唯一；裁决记录里的来源 = hook:<name>
      points: [job.submit]                    # 必填；job.submit / job.deliver / job.interaction / session.permission
      command: ["python3", "D:/ops/gofer_policy.py"]   # 必填，argv 形式，不经 shell
      projects: [my-tools-dev]                # 可选；空 = 全部项目
      dir: D:/ops                             # 可选；同 event_hooks
      env: {POLICY_FILE: "D:/ops/policy.yaml"}  # 可选；值不进任何 API 视图
      timeout_sec: 5                          # 可选；默认 5，上限 30（交互 / 授权点 120）
      on_error: deny                          # 可选；deny | allow | ask，默认见二.3
      enabled: true
```

同一决策点配了多个 hook 时按配置顺序串行调用：任一返回 `deny` 立即结束（不再调后面的）；补丁依次叠加，后一个看到前一个叠加后的请求；交互 / 授权点第一个给出非 `ask` 结果的 hook 生效。

### 输入（stdin，一个 JSON 对象）

```json
{
  "version": 1,
  "point": "job.submit",
  "hook": "no-push",
  "project": "my-tools-dev",
  "caller": {"id": "inhere", "channel": "cli", "client": "host-a"},
  "job": {"agent": "exec", "runner": "local", "cmd": ["git", "push", "origin", "main"],
          "prompt": "…", "cwd": ".", "title": "…", "tags": [], "review": false, "hold": false,
          "worktree": false, "read_only": false, "timeout_sec": 600, "kind": "submit"}
}
```

- `job.kind`：`submit`（普通提交）/ `resume`（续接）/ `retry` / `fallback` / `workflow` / `plan`（plan todo 派发）/ `internal`（交付、tracker 同步等 gofer 内部 job）。内部 job 默认不调 hook（`include_internal: true` 才调），以免一条坏规则卡死交付本身。
- prompt 是已经套完模板、追加完验收标准的文本，**不含**注入的规则段（规则是运维自己写的，不需要再判）；长度截到 64 KiB。
- `job.deliver` 的 `job` 换成 `delivery`：`{job_id, mode, branch, remote_branch, base, draft, labels, title, body, commits:[{sha, subject}], diff_stat}`。
- `job.interaction`：`{job_id, agent, interaction:{type, prompt, options:[{id, label, kind}], tool_call:{kind, title, raw_input}}}`。
- `session.permission`：`{session_id, agent, cwd, tool_name, tool_input, kind: permission|question}`。

### 输出（stdout，一个 JSON 对象；退出码必须为 0）

```json
{"decision": "allow", "reason": "…", "patch": {"review": true, "tags_add": ["night"], "timeout_sec": 1800}}
{"decision": "deny", "reason": "本项目禁止直接 push main"}
{"decision": "answer", "option_id": "allow_once", "reason": "只读命令"}
{"decision": "ask"}
```

`job.submit` 允许的补丁字段（全部是「更保守」方向，越界字段让整次调用按 `on_error` 处理）：

| 字段 | 含义 |
|---|---|
| `review: true` | 开人工验收 |
| `hold: true` + `hold_reason` | 改成待批 job |
| `read_only: true` | 只读 job（agent 不支持只读时提交被拒，与手动 `--read-only` 一致） |
| `timeout_sec` | 只能调低 |
| `budget` | 只能调低各维度 |
| `tags_add` | 追加标签 |
| `title` | 改标题（标注用） |
| `acceptance_append` | 追加验收标准条目 |

`job.deliver` 允许：`title`、`body`（整体替换）、`draft: true`、`labels_add`。

`stderr` 末尾 1 KiB 经脱敏后进日志，不进 API 响应（避免 hook 的调试输出外泄）。

## 五、行为与可见性

- **deny**：`job.submit` 返回 403 `denied by decision hook <name>: <reason>`，不创建 job；审计日志记一条 `decision_hook.denied`（调用方、项目、命令摘要）。`job.deliver` 记交付结果 `failed`，`error` 为同一句话，发 `job.delivery_failed` 通知。
- **补丁 / 自动应答**：job 事件 `job.decision_hook`（`{point, hook, decision, reason, patch}`）；交互的 `answered_by = hook:<name>`；终端授权回答 `answered_by` 同样记 hook 名，web 会话卡显示「由 hook <name> 放行 / 拒绝」。
- **on_error 触发**：serve.log `decision_hook.failed`（name、point、原因、stderr 尾部），job 事件里也记一条，便于发现规则脚本坏了。
- `gofer config validate`：校验字段，`command[0]` 找不到时警告；新增 `gofer tool hook-test <name> --point <p> < sample.json`：用给定输入跑一次 hook，打印解析后的裁决与被拒的补丁字段，不连 server、不产生任何副作用（G033：小工具放 `gofer tool`）。
- `GET /v1/config` 只读视图展示 hook（env 只给键名），web 配置页只读列表，与 event_hooks 一致。

## 六、worker 与远程

hook 只在 server（hub）上执行：

- 派到 worker 的 job 在 hub 的 Submit 里已裁决；worker 重入 Submit 时不再调（worker 上配的 decision_hooks 只对 worker 本机直接提交的 job 生效）。
- worker 上 job 的交付计划在 hub 生成，裁决后再派发内部交付 job。
- 交互由 hub 统一管理，worker job 的交互同样在 hub 裁决。
- 终端会话授权：hook 上报到 hub，hub 裁决。

## 七、分期

1. **D1**：配置与执行器（复用 AUTO-04 的进程管理、环境过滤、stderr 脱敏）、`job.submit`、`job.deliver`、`gofer tool hook-test`、文档。
2. **D2**：`job.interaction`（与 supervisor 自动作答的先后：hook 先于 supervisor；hook 返回 `ask` 才走现有升级链）。
3. **D3**：`session.permission`（hookrelay 上报时 hub 先裁决，`ask` 才开 web 卡；人在终端前时同样先问 hook，`ask` 交回终端）。

每期单独发版；D1 后先用一两条真实规则（禁止 push main、PR 正文内网地址拦截）跑几天再做 D2。

## 八、测试

- config：解析、校验（重名、points 非法、超时越界、on_error 取值）。
- 执行器：stdin 内容、去凭据环境、超时杀进程树、退出码 / 非法 JSON / 越界补丁 → on_error 三种取值、多 hook 顺序与短路。
- `job.submit`：deny 不建 job 且 403；各补丁字段生效；越界字段整体按 on_error；内部 job 默认跳过；worker 重入不调。
- `job.deliver`：deny → failed + 通知；改写正文后 PR body 为改写结果；worker job 在 hub 裁决。
- httpapi：配置写接口拒绝 `server.decision_hooks`；读视图不含 env 值。
- 测试 hook 用 testcmd 辅助程序（读 stdin、按参数输出固定 JSON / 退出码 / 睡眠），不依赖 shell。

## 九、文档

- skill `references/server-config.md` 加 `decision_hooks`；新增一节「决策 hook」写协议与两个示例脚本（禁止 push、PR 正文检查），面向任意项目（G046）。
- `docs/runbook/im-notification.md` 的事件钩子一节旁加交叉引用；`config/gofer.example.yaml` 注释示例；CHANGELOG；路线图 AUTO-04b。

## 十、待确认

1. **失败默认值**：提交 / 交付点默认 `deny`（脚本坏了所有提交都被拒，但不会漏放），交互 / 授权点默认 `ask`。是否同意？
2. **只收紧**：补丁不允许放宽（不能关 review、不能延长超时、不能换 agent）。是否需要例外，例如「把 job 改派到更便宜的 agent」？
3. **分期**：先做 D1（提交 + 交付），D2 / D3 视使用情况再做。是否同意？

## 十一、实施记录（D1，2026-10-12）

**落地内容**

- 配置：`internal/config/decision_hooks.go`（`DecisionHookConfig`、`DecisionHooksFor` 按顺序 / 项目 / 启用 / 内部 job 过滤、加载校验、深拷贝）；字段策略 `server.decision_hooks` 不可编辑；`GET /v1/config` 只读视图；web 配置页只读列表。与事件钩子共用工作目录解析（`hookWorkDir`）。
- 执行器：`internal/job/decision_hooks.go`。事件钩子的进程管理抽成 `runHookCommand`（多一个 stdout 参数），决策 hook 复用；stdout 上限 64 KiB，`DisallowUnknownFields` 解析答案与补丁；`decisionFailure` 统一按 `on_error` 处理并记 `decision_hook.failed`。
- `job.submit`：`submitAdmitted` 在 `applyTemplate` 之后调用 `decideSubmit`；否决返回 `ErrDecisionDenied`（HTTP 403）；补丁依次应用并同步到待批 job 保存的原始请求（`patchReceived`）；裁决在 job 建立后记 `job.decision_hook` 事件（普通与待批两条路径）。
- `job.deliver`：`Deliver` 在生成计划之后、本机执行或派往 worker 之前调用 `decideDeliver`；否决记交付失败（发 `job.delivery_failed`）。
- `gofer tool hook-test`（`job.TryDecisionHook`）与 `config validate` 的命令检查。
- 测试辅助：testcmd 新增 `decide <answer> [<capture>]`。

**与设计的偏差及原因**

- 调用时机：设计写「模板、验收标准、verify 解析之后」；实际在模板之后、追加验收标准与「交付约定」段之前。这样 hook 看到的 prompt 是提交者写的任务（验收标准作为单独的 `acceptance` 字段给出），`acceptance_append` 也能自然进入随后追加的验收标准段。
- `on_error` 在 D1 只接受 `deny` / `allow`；`ask` 留给 D2 / D3 的交互与授权点。`points` 只接受已实现的两个点，配了 `job.interaction` 等会在加载时报错，避免以为已生效。
- 对不能待批的 job（持续会话、交互、workflow 步骤、内部 job）要求 `hold` 算 hook 出错，而不是让提交以「--hold cannot be combined…」失败（调用方没有要求 hold，那句话会误导）。
- `job deliver --dry-run` 不调用 hook：预览不应有副作用，hook 的裁决以真正交付时为准。
- 交付事件里改写过的标题 / 正文只记 `rewritten`，不记新文本（正文可能很长，也可能正是被清理掉敏感内容之前的版本的替代）。
- worker 重入的识别用已有的 `CredentialExternal` 标记；批准后重入用 `heldJobID`。
