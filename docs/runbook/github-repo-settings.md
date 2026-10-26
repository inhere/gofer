# GitHub 仓库门面设置（网页操作）

对外推广 gofer 之前，仓库门面里有一部分只能在 GitHub 网页上改：简介、topics、社交预览图、Discussions 开关。这些**不要用 API 或脚本改**——网页设置是最终口径，脚本改动容易被后续手动操作覆盖，也看不出是谁改的。下面是逐项清单，改完截图留个记录即可。

仓库：`github.com/inhere/gofer`。入口都在 **Settings → General**，个别在 **Settings → Security**。

## 1. 简介（Description）

Settings → General → Description，最多 350 字符。用 README 主标题的英文版：

> Self-hosted control plane that runs coding agents — Claude Code, Codex, ACP agents or plain commands — as tracked, reviewable jobs inside your real projects, on your own machines.

Website 留空（没有独立站点）；要填就填 Releases 页面，联系人 / 文档都从 README 走。

## 2. Topics（8–12 个）

Settings → General → Topics，建议这一组：

```
coding-agents · ai-agents · agent-orchestration · claude-code · codex · acp
mcp · self-hosted · job-scheduler · developer-tools · go · web-console
```

挑选口径：前几个是别人真会搜的词（`coding-agents` / `ai-agents` / `agent-orchestration`），中间是能对上具体生态的（`claude-code` / `codex` / `acp` / `mcp`），最后是属性与语言（`self-hosted` / `job-scheduler` / `developer-tools` / `go`）。超过 12 个会互相稀释，少而准比堆词好；换词时优先保 `coding-agents` 与 `self-hosted` 这两个最能定位的词。

## 3. 社交预览图（Social preview）

Settings → General → Social preview。要求：**1280 × 640 px（2:1）**，PNG / JPG / GIF，文件小于 1 MB。它出现在链接预览、Twitter / 微信 / Slack 卡片上。

现有的 `.github/image/gofer-workbench-en.png` 是 1730 × 980（约 16:9），直接上传会被裁掉上下或压扁。按 2:1 重新取景：留 1730 × 865（上下各裁约 57 px），再缩到 1280 × 640。更省事的做法是做一张专用图——左边一行英文主标题（`gofer` + 那一句简介），右边工作台截图裁一块，四周留白，缩到手机尺寸也认得出。中文版另存一张，不要在一张图里中英混排。

## 4. Discussions

Settings → General → Features → Discussions：**建议开启**。

- 提问、用法讨论、想法先走 Discussions；issue 只留能复现的 bug 与明确的功能请求。`.github/ISSUE_TEMPLATE/config.yml` 已经把「Questions and usage help」指向 Discussions，**关掉 Discussions 就是一个死链**，而且空白 issue 已被关闭（`blank_issues_enabled: false`），没开 Discussions 的人会无处提问。
- 开启后建三个分类：`Announcements`（发版通告，只有维护者能发）、`Q&A`、`Ideas`。

## 5. 顺带核对

- **Issues**：开启。模板在 `.github/ISSUE_TEMPLATE/`（bug 表单、feature 表单、提问与文档链接）。
- **Labels**：模板用到 `bug`、`enhancement` 两个标签；标签不存在时 GitHub 会静默忽略，建议先建好。
- **Private vulnerability reporting**：Settings → Security → 确认「Private vulnerability reporting」为开启，`SECURITY.md` 里 **Report a vulnerability** 的链接（`https://github.com/inhere/gofer/security/advisories/new`）才可用。
- **Wiki**：关闭。文档在 `docs/` 与 `skills/`，两处写会漂。
- **Projects**：可选，用来放路线图；不开不影响使用。
- **Dependabot alerts / security updates**：开启（`.github/dependabot.yml` 已存在）。
- **分支保护**：`main` 建议要求 CI 通过再合并；单人开发可以先只要求 CI，避免自助合并被自己挡住。
