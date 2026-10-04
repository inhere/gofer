# Z4：Workflow 对比视图 + 模板向导（web）+ 本机 runner 显示名

分支 z4-web；设计见 docs/design/2026-10-03-multi-agent-compare-and-flow-templates-design.md（Z4 与一期实施说明）。

## 一期已有接口（无需重写）

- `GET /v1/workflow-templates[/{name}]?project=`、`POST /v1/workflows`（`template`+`vars`）
- `POST /v1/workflows/{id}/pick` `{step,fan}`、`POST /v1/jobs/{id}/worktree/merge` `{squash,cleanup_others}`，冲突 409（错误信息含冲突文件）
- 缺口：workflow 步骤行不带 `join` / `picked`，没有"渲染预览"接口 → 本批补 `Step.join/picked` 与 `POST /v1/workflow-templates/{name}/render`

## 步骤

1. 后端小补：Step 增 `join`、`picked`；新增 render 预览接口（只渲染不提交）；各带测试。
2. 前端显示名：`utils/runnerDisplay.ts`（`local`→`server`，值不改）+ 测试；应用到 Runners 页 / runner 下拉 / job 列表与详情 / Sessions / 拓扑图。AGENTS.md G043 与 skill 补一句。
3. 对比视图：`utils/compare.ts`（分组/列数据整理、合并冲突解析）+ `components/CompareColumns.vue` + `MergeDialog.vue`；WorkflowDetail 对扇出步使用；手机纵向卡片可左右切换。
4. 模板向导：`utils/workflowTemplate.ts`（变量类型推断、表单默认值、校验）+ NewWorkflow 页签（从模板新建 / YAML）。
5. JobDetail worktree 区块「合并到基线」（复用 MergeDialog，远程 runner 灰显）。
6. 同步 skills/gofer-usage 与 README；质量门；真实冒烟（临时 serve + 两个假 cli-agent，桌面/手机截图存 tmp/z4-smoke/）。

## 验收

质量门见任务书；前端用 scratch 副本跑 vue-tsc / vitest / vite build（不构建到 web/dist）。
