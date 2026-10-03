<!-- template_id: design; template_version: 1.1.1 -->
# 跨 job 查找与脱敏秘密（Y 批）

> 状态：Approved（Draft 0.1；用户 2026-10-03 在 web 中继确认）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-03 | Claude | 按值跨 job 查找、批量脱敏、提交提示覆盖标题 |

## 背景

v0.97 的 `gofer job redact <id>` 只处理指定 job。一次真实清理中，要先人工判断秘密出现在哪些 job 里（泄漏 job、清理 job 的命令行），再逐个处理，并另外用 VACUUM 清理数据库空闲页。秘密往往会扩散到多个 job（排查、重试、清理、评论里引用），需要一个按值全局查找、一次清干净的入口。

## 方案

### Y1 `gofer job secret-scan`（只查不改）

- `gofer job secret-scan --literal-from-stdin [--pattern <RE2>] [-p <project>] [--since <dur>]`：原文只从 stdin 读。
- server 扫描范围：所有已结束 job 的 DB 文本列（与 redact 相同的列清单）、评论、这些 job 的 result_dir 文本文件；可按项目 / 时间窗缩小。
- 输出：命中的 job id、标题（标题本身命中时以 `***` 掩码显示）、各位置命中数；**绝不回显原文**。仍在运行的 job 单独列出（不能脱敏，提示等其结束）。
- HTTP：`POST /v1/jobs/secret-scan`（管理员；body 带 literals / patterns / 过滤条件）。

### Y2 批量脱敏

- `gofer job secret-scan ... --redact [--yes]`：在扫描结果上逐个执行现有 redact（每个 job 一条 `job.redacted` 审计），最后对数据库做一次 WAL 截断；可选 `--vacuum` 额外 VACUUM 一次，清理 v0.97 之前写入的空闲页残留（输出提示耗时与锁影响）。
- 非管理员只能扫描 / 脱敏自己拥有的 job。

### Y3 提交提示覆盖标题

- V2c 的秘密形态提示目前只检查 command / args / prompt，补上 title（`--title`）与 `--tags`。

## 不做

- 远程 worker 本机的副本、已发出的通知、外部日志：输出中照旧提示局限。
- 自动定期扫描。

## 测试与验收

固定测试：`TestSecretScanFindsAcrossJobs`、`TestSecretScanNeverEchoes`、`TestSecretScanRedactAll`、`TestSecretScanOwnerScope`、`TestSubmitSecretWarningCoversTitle`。监督者真实验收：临时 serve 中 3 个 job（命令、输出、评论各含同一假秘密）+ 1 个无关 job → scan 只报 3 个 → `--redact --yes` 后再 scan 为 0，数据库文件字节级无残留。
