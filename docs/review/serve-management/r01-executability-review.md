<!-- template_id: review; template_version: 1.1.0 -->
# Gofer 服务管理设计与计划可执行性评审

## 评审目标与范围

- Git root：Gofer 独立仓库根，逻辑名 gofer；本机物理位置留在候选证明中。
- 设计候选：e36b59def2e690b16820328baca5c1315399be12，docs/design/2026-10-07-serve-management-and-repository-migration-design.md，revision 0.2。
- 计划候选：62b3a9a00b16532e0f2ffa55b96675beda94b750，docs/plans/2026-10-07-serve-management-and-repository-migration-plan.md，revision 0.1。
- 评审者：独立 Agent executability_review；日期 2026-10-07；axis Spec/Executability，RIGOROUS、REPORT_ONLY。
- 排除项：产品实现、动态测试、注册、升级、迁移；不参与候选修订，不与另一轴 reviewer 交流。

## 输入与方法

独立 semantic Load/hash 核对；完整读取两个固定候选、适用 AGENTS/workspace、Windows daemon/proctree、
config writer、tracker model/discovery/store/sync；读取 worker upgrade、job Submit/Service、serve 调度/停机与两份 ps1。
按用户四项需求、job 自动升级保留、中立性及实际执行边界核对最短可执行链。

## 验收标准

原文路径保存、冻结有效目录、停止看门狗、独立升级映像与 Windows/Linux 隔离、接管后停机、持久结果、
drain 不等待自己、身份/端口归属、tracker 全字段与依赖守恒及独立同步身份；实际平台验收不能只交叉编译。

## 发现

### EX-01 [LOW] start 应清除停止意图

- disposition：CORE_CORRECTIVE。
- 证据：原设计第126–128、179–180行，计划 T3。
- 影响：若只在 restart 清除 stop marker，stop 后 start 可能立即退出。
- 建议：启动事务清除并测试 start→stop→start，登录触发仍尊重 stop；落实已有生命周期，无需新增机制。

### EX-02 [MEDIUM] drain 前暂停调度推进

- disposition：CORE_CORRECTIVE。
- 证据：原计划 T5；internal/serve/serve.go 的 schedule 提交前推进/单次禁用段。
- 影响：只拒绝 Submit 会使升级窗口到期单次任务被推进或禁用。
- 建议：在 schedule 推进/领取前暂停，并核对失败恢复后的记录守恒；这是原“暂停内部自动调度”的落实。

## 覆盖缺口与限制

图 generation 2026-09-05；coverage transport 关闭，使用父 evidence packet 与源码补证，不声称图完整。
无动态执行，PASS 仅证明需求可执行；平台权限、计划批准和实施模式不由本报告代替。

## 结论

原设计 PASS、原计划 PASS；没有开放 CORE_BLOCKING。新增管理 owner 及离线工具直接服务核心需求，通用框架已排除。

## 剩余风险与后续

EX-01/02 的关闭与新 candidate 变更范围确认另存 confirmation。所有实现证据由 T3/T5/T9 实际提供。
