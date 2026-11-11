# super-agent 式工作流：五个参考项目统一整理（2026-11-11）

> 参考材料，不是事实源；吸收前须按设计流程核实。逐项目记录见 [reference-projects.md](reference-projects.md)。

## 定位

按 `AGENTS.md`「三项目分工」（2026-11-11 修订）：super-agent 式工作流的周边能力由 gofer 承载，需要模型判断的环节由 gofer 派 suga job；suga 是执行引擎；xhen 在其上替人做事。本文按闭环环节整理五个项目（本地副本均在 WP `local/`）：

| 项目 | 形态 | 主要参考价值 |
|---|---|---|
| dsh-superagent-prototype（deepseek-ai） | 单仓库主动式 coding agent 原型 | 闭环模型、注意力原则、PR 跟进、恢复说明、健康分级 |
| LobeHub（lobehub） | 多租户 SaaS，"Super-Agents on Your Team" | PR 闭环细节、准入策略管线、线程跟进调度、打回沉淀为规范 |
| crewbot-connector（super-agent.team 的本地 daemon） | 出站 WS + ACP 进程 | 外部线程 ↔ 会话两层映射、双向可靠投递、运行中插话 |
| Proma（proma-ai） | 单机桌面 agent 应用 | 定时任务运行健康与跨轮上下文、会话记录分步读取 |
| liaison（liaisonio） | 自托管私有服务访问网关 | 管理审计、worker 接入与服务化、token 生命周期 |

## 闭环各环节：gofer 现状与候选

| 环节 | gofer 已有 | 候选（来源） | 落点 |
|---|---|---|---|
| 来源 | 入站触发（HMAC、owner 上限、事件去重） | ① 轮询外部状态：PR / CI / issue（dsh、LobeHub）；② 外部 issue 单向同步为 issue / todo，外部 id 唯一映射、本地改过不覆盖（LobeHub） | gofer |
| 准入 | 发送者白名单、字段条件、权限上限 | ③ 策略逐条 off / shadow / enforce，便宜规则先于模型，决策落库带版本（LobeHub，细化 dsh「只记录模式」）；④ 规则三态，unknown 才派 suga 评估 job（LobeHub） | gofer + suga job |
| 工单与跟进 | 工作项、plan / todo、job follow-up、`job say` | ⑤ 外部线程 → 工作项 → agent 会话 两层映射，resume 失败降级新会话并留痕（crewbot、dsh）；⑥ 线程调度：同一线程同时只跑一次、运行中到达的消息暂存后重放、合批与冷却（LobeHub）；⑦ PR 跟到合并：绑定 PR（分支名 / 上报 / 识别 `gh pr create`）、CI 失败带日志尾唤醒原 job（每 PR 上限、合批）、可信评审人、合并即完成 todo（dsh、LobeHub） | gofer |
| 执行 | job、续跑、worktree、交付、停滞看门狗、故障转移 | ⑧ 中断后续跑带固定恢复说明（dsh）；⑨ 运行中插话 steering，按 agent 能力选插入或排队（crewbot）；⑩ 为人的消息预留执行槽（dsh） | gofer；⑨ 需 suga 支持 |
| 回报 | 出站通知、「等我」 | ⑪ 回复到原线程，按用途过闸（确认 / 回答 / 结果 / 需要人）（dsh）；⑫ 入站接收回执（已收到 / 已插入 / 排队）与按线程序号的出站 outbox（crewbot）；⑬ PR 上只留一条跟踪评论原地更新（LobeHub） | gofer |
| 注意力 | 「等我」、今天、收件箱 | ⑭「无可行动」结局不进「等我」（dsh）；⑮ 巡检无变化不出声（dsh） | gofer |
| 定时 | cron schedule、wakeup | ⑯ 上轮未结束则跳过、连续失败自动暂停、最多跑 N 次（Proma）；⑰ 每轮带上轮结果与滚动笔记（Proma）；⑱ 探针指纹变化才派 agent（Proma 设计稿） | gofer |
| 学习 | 经验候选（agent 自报） | ⑲ 打回 note 派 suga job 提炼为候选规范，由人采纳（LobeHub）；⑳ agent 上报平台摩擦（缺工具、文档不符），去重限流进运行健康（LobeHub） | gofer + suga job |
| 运行健康与审计 | 鉴权审计、`/health` | ㉑ `serve status` 退出码分级、连续 N 次失败才报（dsh）；㉒ 管理操作审计中间件（liaison）；㉓ worker 镜像事件断线补传（crewbot；现为断线即丢，`internal/worker/client.go`） | gofer |
| 机器与凭据 | worker join、远程升级、caller 入库（进行中） | ㉔ worker 一行接入 + 注册为用户级服务（liaison；可用 gookit/ext servicemgr）；㉕ token 到期与最近使用（liaison）；㉖ 被吊销即自清本地凭据、三态核对防断网误删（crewbot） | gofer |
| 查看 | `job logs`、`session tail` | ㉗ 长记录 outline + 按回合导出（Proma） | gofer |

## 建议分批

1. **底层可靠性**（小，彼此独立）：⑧ 恢复说明、⑯ 定时任务运行健康、㉑ 状态退出码、㉒ 管理审计。
2. **闭环主干**（一份总设计、分阶段）：⑤ 线程映射 → ⑥ 线程调度 → ⑦ PR 跟到合并 → ⑪⑫⑬ 回复原线程与可靠投递。①（PR / CI 轮询）随 ⑦ 一起。
3. **准入与判断**：③ 策略管线与只记录、④ 规则三态 + suga 评估 job、⑭ 无可行动结局。
4. **其后**：⑰ 跨轮上下文、⑲ 打回沉淀、⑳ 平台摩擦、㉔ worker 服务化、㉕㉖ 凭据、㉗ 记录分步读取、㉓ 镜像补传、⑨ steering、⑩ 预留槽、② 外部 issue 同步、⑱ 探针。

## 统一不学

- 平台内置模型路由与判断（LobeHub 的插话分类器、dsh 的 LLM 主会话）：gofer 只做确定性规则，判不了派 suga job。
- 放宽权限换无人值守：Proma 定时任务强制放开权限、crewbot 网页一句话给 `full`、crewbot 运行时改写 codex 适配器打开联网。
- 父会话代答子会话的权限请求（Proma）。
- 访问网关、SaaS、桌面应用的产品面：浏览器数据库 / 远程桌面工作区、LLM Playground、Marketplace、多 workspace RBAC、内嵌浏览器与编辑器。
- 归 xhen：打扰策略（何时插话、被嫌吵后静默）、个人日程与提醒、推荐引擎、夜间自省。归 suga：provider 与上下文引擎、探索分支、单轮重试预算。
