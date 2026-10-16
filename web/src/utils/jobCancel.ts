// job 的「取消」入口该不该显示（gofer-83fe）：口径与服务端 job cancel 的允许范围一致，
// 免得每加一个排队态（waiting_dir / recovering / paused_for_update …）前端都要补一次枚举。
//
// 服务端规则（internal/job/model.go 的状态常量 + internal/job/cancel.go Cancel，
// 入口 POST /v1/jobs/{id}/cancel）：
//  - 终态 done / failed / cancelled / timeout / rejected（job.IsTerminal）：取消是空操作，不给入口；
//  - needs_review 是非终态也不给：agent 进程已结束、Cancel 返回 409，要走 accept / reject；
//  - 其余非终态都能取消：running / awaiting_input 取消正在执行的进程；queued / waiting_dir /
//    pending_interaction / recovering 这类排队态，Cancel 先记下取消意图（或直接取消排队中的
//    上下文），execute 一拿到 context 就收尾；awaiting_approval / paused_for_update 是纯数据库
//    态，取消 = 提交者撤回（→ cancelled，命令从未执行）。
//
// 未知状态按「可取消」处理：新增的非终态默认给出取消入口（反过来漏一个排队态正是这个 bug），
// 真到了终态服务端也只是空操作。
const TERMINAL: ReadonlySet<string> = new Set(['done', 'failed', 'cancelled', 'timeout', 'rejected'])
// 非终态但服务端拒绝 cancel 的状态。
const UNCANCELLABLE: ReadonlySet<string> = new Set(['needs_review'])

export function canCancelJob(status: string | undefined): boolean {
  return !!status && !TERMINAL.has(status) && !UNCANCELLABLE.has(status)
}
