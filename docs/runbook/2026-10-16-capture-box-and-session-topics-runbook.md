# 收集箱回滚与会话主题数据 Runbook

> 设计：[`../design/2026-10-16-session-topics-mailbox-and-inbox-design.md`](../design/2026-10-16-session-topics-mailbox-and-inbox-design.md)（第一期：会话主题与查找、收集箱）。接口见 [`../reference/http-api.md`](../reference/http-api.md)。

## 1. 收集箱：回滚前先 park

### 数据在哪

收集箱条目就是 work item，状态为 `captured`，多三列（`work_items` 表，加列迁移，旧库启动时自动补）：

| 列 | 含义 |
|---|---|
| `kind` | `idea` / `issue` / `request` / `note`；非收集箱来的条目为空串 |
| `capture_source_session` | 记录它的会话 id（取请求头 `X-Gofer-Session-Id`），没有则空 |
| `capture_source_job` | job 凭据记录时的来源 job id，没有则空 |

`captured` 不在 `WorkStatuses` 里（看板列、分组、计数都不含它），只能通过收集箱入口进入；`gofer work ls --status captured` 能看到全部。

### 什么时候用 `gofer work park --captured`

只在**要回到不含收集箱的旧版本二进制之前**用。旧版本不认识 `captured` 状态，条目会显示成未知状态，而且旧版本的整理器 / 汇报可能去动它。回滚步骤：

```bash
gofer work ls --status captured           # 先看有几条
gofer work park --captured                # 全部转为 parked；可加 --until 2d / --note "回滚前归档"
gofer work ls --status captured           # 应为空
```

它做什么：把每个 `captured` 条目的状态改为 `parked`（普通的「搁置」状态，旧版本认识），server 对每条写一行「从收集箱归档」日志；`--until` / `--note` 可选，给出则写入 `park_until` / `park_note`。与 `<id>` 二选一。逐条处理，某条失败会停下并报告已处理多少条，重跑即可（已 `parked` 的不会再出现在 `captured` 列表里）。

它**不丢数据**：条目的标题、目标文本、`kind`、来源列、日志、关联（会话 / job / issue）都原样保留，只改状态。新增的三列旧版本直接忽略。不需要回滚时不要跑它，收集箱里的条目就是要在那里等人整理。

### 其它回滚注意

- server 新建、尚未同步的 tracker issue 留在镜像里，旧版本照常同步（只是不认 `server_created` 标记）。
- 回到新版本后，已 `parked` 的原收集箱条目不会自动回到收集箱；需要时人工 `gofer work set` 处理或重新记录。

## 2. 会话主题数据

### 数据在哪

`agent_sessions` 表四列（加列迁移，旧行读回空 / 0）：

| 列 | 含义 |
|---|---|
| `topic` | 一句话主题，最长 200 字符，无控制字符 |
| `scope_json` | 项目根相对子路径的 JSON 数组（斜杠分隔，最多 20 项，已清理去重） |
| `topic_source` | `self`（会话自己设，请求的 `X-Gofer-Session-Id` 等于该会话）或 `human`（其他人设） |
| `topic_at` | 最近一次设置时间（unix 秒），未设过为 0 |

推断的主题（会话目录相对项目根 + 会话名）**每次读取时现算，从不落库**，视图里 `topic_source` 为 `inferred`；所以库里空 `topic` 的会话在界面上仍可能显示一个灰色的推断值。会话重新登记 / 心跳不会动这四列。

### 怎么清除

CLI 只能清范围，不能清主题（`gofer session topic` 的主题参数省略表示不改）：

```bash
gofer session topic --clear-scope                 # 清空范围（当前会话）
gofer session topic --clear-scope --session <id>  # 指定会话
```

清主题用 Web 或直接调接口：`PATCH /v1/sessions/<sid>` 带 `{"topic":"","scope":[]}` 一次清空主题和范围（带认证头，同其它写接口）。

- `PATCH /v1/sessions/{sid}` 里出现的 `"topic": ""` 清除主题（省略才是不改），`"scope": []` 清除范围；清除后会话回到显示推断值。Web 的会话抽屉有「清除主题」按钮。
- 自己的会话不需要额外权限；清别人的会话需 `session.manage.others`。已结束的会话改不了（409）。
- 要从库里整体抹掉（例如导出前）：停 server 后对 `agent_sessions` 执行 `UPDATE agent_sessions SET topic='', scope_json='', topic_source='', topic_at=0;`，先备份数据库。列本身保留，不需要也不应该删除。
- 回滚到旧版本：新增列被忽略，无需处理。
