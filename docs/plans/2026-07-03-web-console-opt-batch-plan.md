# gofer Web 控制台优化批次 实施计划

> Epic `kuwd`。12 点优化收敛为 11 issue / 3 批。SUPMODE：主控编排 + host codex 实施 + 容器亲验。
> P3 三项方案见 `docs/design/2026-07-03-web-opt-p3-design.md`。

## 进度跟踪

| 批 | issue | 标题 | 状态 |
|---|---|---|---|
| P1 | kuwd.1 | 状态分布卡片文字截断修复 | ✅ 代码完成 fbb8d82(待LIVE眼检) |
| P1 | kuwd.2 | Job board 仅 Title 内容区可点击 | ✅ 代码完成 fbb8d82(待LIVE眼检) |
| P1 | kuwd.3 | Job detail 命令区限高+滚动 | ✅ 代码完成 fbb8d82(待LIVE眼检) |
| P1 | kuwd.4 | 命令区 Markdown 查看按钮 | ✅ 代码完成 fbb8d82(待LIVE眼检) |
| P2a | kuwd.5 | Home server version+uptime | ✅ 代码完成(待LIVE眼检) |
| P2a | kuwd.6 | 查看 diff 中文乱码修复 | ✅ 代码完成(待LIVE眼检) |
| P2b | kuwd.7 | 终端输出分页(最后200L+加载更多)+stdout Markdown | ✅ 代码完成(待LIVE眼检) |
| P2a | kuwd.8 | codex session_id 捕获修复(补扫 stderr) | ✅ 代码完成 |
| P3 | kuwd.9 | Schedules 一次性/延迟任务 | ⬜ design 就绪 |
| P3 | kuwd.10 | Secret env 声明式 env_files | ⬜ design 就绪 |
| P3 | kuwd.11 | Workflow 创建 yaml 编辑器页面 | ⬜ design 就绪 |

## P1 · 纯前端小改（已派单）
任务单 `tmp/wopt/p1-frontend.md`。改 `Dashboard.vue`/`Board.vue`/`JobDetail.vue`。验收：`pnpm build`(vue-tsc) + agent-browser 眼检。

## P2 · 前后端配合（明确改动点）

- **kuwd.5 version+uptime**：后端 `stats_handler.go`(或 `/health` `project_handler.go`) 补 `version`(编译期 ldflags 注入，查现有 version 变量)+`uptime`(记录 serve 启动时刻算差)；前端 `Dashboard.vue`(L98 去硬编码)+`types.ts` Stats 加字段。
- **kuwd.6 diff 乱码**：核对前端 `client.ts` openDiff(L328) 实际打开 URL——若是 diff 端点(`diff_handler.go:35` 已带 charset)则查前端 blob decode；若走 `artifact_handler.go`(ServeFile 嗅探无 charset)则补 `charset=utf-8`。实施时先定位 URL 再定方案。
- **kuwd.7 日志分页**：后端 `store/filestore.go:132 ReadLogTail` 改按行+支持 offset/full，`job_handler.go:183 serveLog` 加 query(`lines`/`offset`/`full`)；前端 `LogTape.vue` 对 done 态 job 走 tail 分页，加「加载前面200L/全部」+stdout Markdown查看。前后端较大，单独提交。
- **kuwd.8 session_id 修复**：`outcomes.go:139 captureSessionID` stdout 未命中时补扫 `stderr.log`（根因：codex banner 打 stderr）。补测试。样本 job `20260703-123439-f2ef474c`(session id 在 stderr.log:11)。

## P3 · 后端新能力
方案见 `docs/design/2026-07-03-web-opt-p3-design.md`。实施顺序 D3(workflow创建) → D2(secret env) → D1(schedules 一次性/延迟，含 DB 迁移+调度，需运行期冒烟)。

## 验收基线（每批）
容器 `go build ./... && go test ./...` 全绿；前端 `pnpm build` 绿；关键功能 agent-browser 眼检。codex 不 commit，主控容器验收后提交。
