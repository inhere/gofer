<!-- template_id: plan; template_version: 1.2.0 -->
# F 批手机端使用反馈实施计划

> 状态：Draft 0.1 / DIRECT_CONTINUOUS 执行

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-10-03 | Claude | F1–F3 拆为测试先行的独立功能点 |

## 规划可靠性声明

- `thinking_mode=RIGOROUS`
- `core_objective=工作台可选 runner、job 详情可选续接方式、运行中 job 切 stderr 不卡顿`
- `allowed_scope=tools/gofer 的 internal(streaming/job/httpapi/agent)、commands、web、docs、README、skills/gofer-usage`
- `non_goals=不动 internal/job/workflow、worktree merge、skill 的 workflow 段；不 push；不碰正式 server(8767)/正式配置目录；不构建到 web/dist`
- `stop_conditions=设计冲突、质量门失败且无法在当前 owner 内修复`

## 目标与完成定义

1. F3：后端 `TailFrom` 分块（每帧 ≤256KB，不切断 UTF-8 rune）；前端初次连接带 `tail`，重连沿用字节偏移；运行中可"加载更早"；`LogTape` 渲染前尾部裁剪并分帧插入；stderr 缓冲单独设小。
2. F1：工作台新建会话加 runner 下拉，与 NewJob 共用 runner 选项/阻塞原因函数；持续会话在 local 或 worker(协议>=v13) 可用，删除硬写 local；Sessions 页放开 worker。
3. F2：`resume` 接口加 `mode`/`agent`；resume.go 抽公共继承逻辑后按 mode 分流；`GET /v1/agents` 返回续接能力；CLI `job resume --mode --agent`；JobDetail 增加"续接方式"。
4. F2 互转结论：以只读调查 ACP 适配器会话存储位置为准；无法确认则不开放 ACP↔CLI 互转，仅开放同 agent 族内的形态切换，并在 skill 写明原因。
5. G045：README 与 skills/gofer-usage 同批更新。

## 波次与提交

| 波次 | 内容 | 提交 |
|---|---|---|
| W0 | 本计划 | docs(plan) |
| W1 | F3 后端分块回放 | perf(streaming) |
| W2 | F3 前端 tail / 分批渲染 / 小缓冲 | perf(web) |
| W3 | F1 runner 下拉与公共函数 | feat(web) |
| W4 | F2 后端 mode/agent、能力标志、CLI | feat(job) |
| W5 | F2 前端续接方式 | feat(web) |
| W6 | 文档/skill、冒烟、质量门 | docs |

## 验证

`gofmt -l`、`go build ./...`、`GOOS=windows go build ./...`、`go vet ./...`、`go test ./... -count=1`；web 副本跑 `vue-tsc --noEmit`、`vitest run`、`vite build --outDir <scratch>`；临时 serve + agent-browser 390x844 冒烟，记录 stderr 切换耗时前后对比，截图存 `tmp/f-smoke/`。
