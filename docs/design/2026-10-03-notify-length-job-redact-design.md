<!-- template_id: design; template_version: 1.1.1 -->
# IM 通知长度可配 + job 脱敏 / 删除（V 批）

> 状态：Approved（Draft 0.1；用户 2026-10-03 在 web 中继确认）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-03 | Claude | V1 通知正文长度可配；V2 job 脱敏 / 删除 / 提交时秘密提示 |

## 背景

- 钉钉 / 飞书群机器人消息正文被硬编码截到 500 字（`internal/notify/render.go` `maxTextRunes` / `clampText`），「会话等你回复」引用的回复预览更是先截到约 200 字（`internal/job/session_notify.go` `sessionReplyPreview`）。手机上看不全，又不能配置。钉钉 markdown 消息实际允许约 2 万字节。
- 一次只读排查 job 把一段密钥打印进了 stdout；随后用于清理的 job 命令行本身又把同一个值存进了 job 元数据。gofer 没有任何手段事后删除或改写 job 记录，秘密会长期留在日志、result_dir 和数据库里。

## V1 通知正文长度可配

- 新配置 `server.notification.max_text_runes`（默认 3000；`<=0` 用默认）；每个 webhook 可设同名字段覆盖（0 = 继承全局）。
- 所有 IM 正文（job 完成 / 失败、审批、会话等你回复等，凡是现在走 `clampText` 的）按「该 webhook 的生效上限」截断；渲染时按目标 webhook 取值，不再用包级常量。
- 「会话等你回复」的回复预览不再先截 200 字：保留完整最后一段（读取仍需有上限，如 64KB），交给上面的统一截断。
- 截断时末尾写「…（已截断，完整内容见链接）」。另设按字节的安全上限（钉钉 / 飞书 18000 字节，按 UTF-8 边界截），无论配置多大都不超过，避免机器人拒收。
- generic webhook 的 JSON 契约不变（不截断或按现状）。
- 热生效：reload 后下一条通知即用新值（渲染时读当前配置）。设置页通知分区可编辑该字段（editable 表登记）。

## V2 job 脱敏与删除

### V2a `gofer job redact <id>`

- 输入：`--literal-from-stdin`（从 stdin 读一行或全部原文，去掉末尾换行；**不接受命令行参数传原文**，避免进 shell 历史与 job 元数据）或 `--pattern <regex>`（Go RE2）。两者至少一个，可同时给。替换为 `***REDACTED***`。
- 范围：该 job 在 server 侧的一切持久内容——stdout / stderr、result_dir 下的文本文件（含会话轮次记录、结果文件；二进制文件跳过并在结果中列出）、数据库中该 job 的元数据字段（command / args / prompt / title / 结果摘要 / 错误信息 / 事件与日志行等，按 schema 实际字段梳理）、该 job 的评论。持续会话 job 的每轮记录一并处理。
- 条件：job 已结束（非 running / queued / awaiting_input）；调用方是 job 所有者或管理员。
- HTTP：`POST /v1/jobs/{id}/redact`，body `{literals:[], patterns:[]}`；响应只给每类位置的命中数与跳过文件列表，**绝不回显原文**。
- 审计：记录一条审计事件（谁、何时、job id、各位置命中数、使用了几个 literal / pattern），不记原值、不记 pattern 文本以外的内容（pattern 本身可能含秘密片段时，用户应使用 literal）。
- 局限（输出中提示）：远程 worker 本机的工作目录 / 缓存副本、已发出的 IM 通知、外部日志不在处理范围内。

### V2b `gofer job delete <id>`

- 删除 job 记录（及其事件、评论、附件、结果目录、与 plan 的关联行），保留一条审计事件（谁、何时、job id、标题被替换为「已删除」）。
- 条件同上（已结束；所有者或管理员）；CLI 需 `--yes` 或交互确认。支持多个 id。
- HTTP：`DELETE /v1/jobs/{id}`。
- web：job 详情页「删除」按钮（二次确认，仅有权限时显示）；删除后返回列表。

### V2c 提交时秘密形态提示（只警告）

- CLI 提交（`job run` 等）前对 command / args / prompt 做常见秘密形态扫描：PEM 私钥头、`AKIA[0-9A-Z]{16}`、`sk-` / `ghp_` / `github_pat_` / `xox[abp]-` 前缀令牌、`(key|secret|token|password)\s*[=:]\s*\S{16,}` 等。命中时在 stderr 警告「命令行疑似包含秘密，会被保存在 job 记录中」并指出位置（不回显完整值），不阻止提交；`--no-secret-check` 关闭。
- server 侧不拦截。

## 测试与验收

固定测试：`TestNotifyMaxTextRunesConfigurable`、`TestNotifyByteCapForDingTalk`、`TestSessionReplyPreviewUsesNotifyLimit`、`TestJobRedactLiteralAllLocations`、`TestJobRedactRejectsRunningJob`、`TestJobRedactNeverEchoes`、`TestJobRedactOwnerOrAdminOnly`、`TestJobDeleteRemovesRecordKeepsAudit`、`TestSubmitSecretWarning`，web Vitest（删除按钮）。

监督者真实验收：临时 serve 配置 max_text_runes，渲染长消息检查长度与截断提示；提交一个打印假秘密的 exec job → `printf 假秘密 | gofer job redact <id> --literal-from-stdin` → 日志 / result_dir / `job show` / 数据库中均查不到原值；`gofer job delete` 后 job 不可见、审计在。
