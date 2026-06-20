# Gofer CLI 易用 + job 标签检索 — 实施计划（总纲）

> 设计依据：[`../../design/2026-06-20-cli-usability-tags-design.md`](../../design/2026-06-20-cli-usability-tags-design.md)（v0.1，§10 决策默认全部按推荐采纳）。
> bd epic：`hyy-ai-inspect-4a0`（P1 `hyy-ai-inspect-6n6` / P2 `hyy-ai-inspect-7y5` / P3 `hyy-ai-inspect-lkz`）。本文件只保留**总纲 + 进度跟进 + 阶段简述**（SR1105）；阶段详情见子文档。

## 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-20 | Claude | 初版：P1–P3 拆分 + 全局验收门 + 进度跟进骨架。 |

## 已采纳决策（design §10，全按推荐）

- **D1** rerun 取数走**新增只读端点** `GET /v1/jobs/{id}/request`，不改 `get_job` 主响应。
- **D2** tags 存 `tags_json` JSON 列，单 tag 过滤用 `tags_json LIKE '%"<tag>"%'`（v1 子串近似，精确多 tag 留 v2）。
- **D3** 检索维度加 **tag/agent/runner/since**，并补 caller 的 Web UI（后端已支持）。
- **D4** 抽 `client.StreamJob` 共享 SSE 助手供 CLI `watch`；`runner/peerhttp` 本期**不强制重构**。
- **D5** `rerun` 复跑**清空 `RequestID`**（=新 job，不被幂等去重命中原 job）。
- **D6** `gofer init` 默认写 `./.gofer.yaml`、存在需 `--force`、非交互注释模板。
- **D7** 一个 epic 三阶段（已建）。

## 范围与分期

| 阶段 | 子文档 | 内容 | 依赖 | 风险 |
|---|---|---|---|---|
| **P1** | [`P1-tags-plan.md`](./P1-tags-plan.md) | **E5 tags**：`tags_json` 加列 5 处贯通 + `JobRequest.Tags` + 检索维度（tag/agent/runner/since）后端 + Web Board 过滤器 | 无 | 低 |
| **P2** | [`P2-cli-plan.md`](./P2-cli-plan.md) | **E2 CLI**：`client.ListJobs`/`StreamJob` + `job list`/`watch`/`rerun` + `GET /v1/jobs/{id}/request` + gcli 补全 | P1 | 低-中 |
| **P3** | [`P3-init-validate-plan.md`](./P3-init-validate-plan.md) | **E3**：`gofer init` + `gofer config validate` + `config/gofer.example.yaml` 补全缺失段 | 无 | 低 |

**顺序**：P1 → P2 → P3。每阶段绿灯即 Git 提交（SR1202）。

## 进度跟进

- [ ] **P1-a** `tags_json` 加列 + 5 处贯通（store/jobs/model/service）+ `JobRequest.Tags`/`JobResult.Tags` + submit marshal/unmarshal
- [ ] **P1-b** 检索维度后端（`ListOpts`/`ListQuery` 加 Tag/Agent/Runner/Since + in-memory overlay 同步过滤 + `handleListJobs` query 映射）
- [ ] **P1-c** Web Board 过滤器（tag/agent/runner/since + 补 caller UI）+ 列表 tag 徽标 + types/client
- [ ] **P2-a** `client.ListJobs(opts)` + `client.StreamJob(ctx,id,from,onEvent)`（SSE 帧解析助手）+ `client.GetJobRequest(id)`
- [ ] **P2-b** `GET /v1/jobs/{id}/request` 端点（回原始 JobRequest）+ 路由
- [ ] **P2-c** `job list`/`watch <id>`/`rerun <id>` 子命令 + `gofer completion` 接线
- [ ] **P3-a** `gofer init`（写 starter yaml，`--force` 守卫）+ `gofer config validate`（全局 load+validate+逐项目 reg.Validate）
- [ ] **P3-b** `config/gofer.example.yaml` 补 callers/workers/runner_probe/retention/db_path/worker-runner 段

## 全局验收门（每阶段收尾必过）

```bash
cd tools/gofer
go build ./... && go vet ./... && go test ./... && gofmt -l internal/ cmd/   # 后端
pnpm -C web build                                                            # 含前端阶段(P1-c)
```

- **回归**：现有 job 提交/日志/SSE/list/worker 全绿；`tags_json` 走 `migrate()` 加列、旧 job 读出 `tags` 为空；新过滤参数 omit 即不过滤（不改变现有 list 行为）。
- **真机冒烟**：local 提交带 `--tag` 的 job → `job list --tag x` 命中、`job watch <id>` 跟到终态、`job rerun <id>` 起新 job；`gofer init` 生成的配置过 `gofer config validate`。

## 安全要点（贯穿）

- `GET /v1/jobs/{id}/request` 在 `/v1` 鉴权内、含 cmd/prompt 但**不含 secret**（secret 走 agent env 配置不入 request，SR403）。
- `gofer init` 只写 `token_env` 占位、**不写 token 明文**。
- tag `LIKE` 过滤走预编译占位符（防注入）。

## 结论

三块共用 CLI 命令层 + `internal/client` + job 列表查询面 + jobs 加列模板，分 P1（tags 地基）→P2（CLI 动作）→P3（引导/校验/example）。最大复用：加列 5 处贯通模板、`reg.Validate`/`validate(cfg)` 既有校验、`runner/peerhttp` 既有 Go 端 SSE 消费、`Board.vue` 既有过滤器骨架、gcli 内置补全。本总纲随阶段更新进度与「阶段实施结果」。

## 阶段实施结果

（实施后逐阶段追加）
