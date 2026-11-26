// 流程（stage）的展示逻辑：步骤条各格的状态、门禁缺项、能做什么动作、会话头的小标签。
// 组件只管画，判定都在这里，便于穷举单测（step 名字的文案也在这里对上 i18n key）。
import { t } from '../i18n'
import type { SessionStageBrief, StageGate, StageLevel, WorkStageView } from '../api/types'

// StageStepState 是步骤条上一格的状态：已过 / 当前 / 当前但被阻塞 / 还没到。
export type StageStepState = 'passed' | 'current' | 'blocked' | 'future'

export interface StageStepRow {
  name: string
  state: StageStepState
  // 当前这一格才带门禁结论；其余为空
  missing: string[]
  needsHuman: boolean
}

const STEP_KEYS: Record<string, string> = {
  design: 'work.stage.step.design',
  review: 'work.stage.step.review',
  plan: 'work.stage.step.plan',
  doing: 'work.stage.step.doing',
  verify: 'work.stage.step.verify',
  done: 'work.stage.step.done',
}

// stageStepLabel 是步骤名（内置模板的六个步骤走 i18n，其它原样显示模板给的名字）。
export function stageStepLabel(step: string): string {
  const key = STEP_KEYS[step]
  return key ? t(key) : step
}

// stageFinished：流程已结束（done = 走完，dropped = 放弃）。结束的流程只看步骤条，不问门禁。
export function stageFinished(v: Pick<WorkStageView, 'status'>): boolean {
  return v.status === 'done' || v.status === 'dropped'
}

// stageStepRows 把快照下来的步骤名排成步骤条：当前步之前的都算已过，当前步按是否阻塞换样式。
export function stageStepRows(
  v: Pick<WorkStageView, 'steps' | 'step_index' | 'status' | 'gate'>,
): StageStepRow[] {
  const missing = stageMissing(v.gate ?? null)
  const needsHuman = !!v.gate?.needs_human
  const finished = stageFinished(v)
  return v.steps.map((name, i) => {
    let state: StageStepState = 'future'
    if (finished) state = 'passed'
    else if (i < v.step_index) state = 'passed'
    else if (i === v.step_index) state = v.status === 'blocked' ? 'blocked' : 'current'
    const current = state === 'current' || state === 'blocked'
    return { name, state, missing: current ? missing : [], needsHuman: current && needsHuman }
  })
}

// stageMissing 是当前步门禁的缺项（每条都写成「下一步该做什么」）；门禁通过或没有门禁时为空。
export function stageMissing(gate: StageGate | null | undefined): string[] {
  return gate?.missing ?? []
}

// stageGateHolds 表示门禁判不了（needs_human）：明确缺项时仍不放行，批准只记录。
export function stageGateHolds(gate: StageGate | null | undefined): boolean {
  return !!gate?.needs_human
}

// canApprove：当前步是 review（批准就是它的门禁），或者门禁判不了需要人确认。流程结束 / 放弃后不行。
export function canApprove(v: Pick<WorkStageView, 'step' | 'status' | 'gate'>): boolean {
  if (stageFinished(v)) return false
  if (v.step === 'review') return true
  return stageGateHolds(v.gate)
}

// canBack：上一步存在才允许退回（服务端在第一步 400）。
export function canBack(v: Pick<WorkStageView, 'step_index' | 'status'>): boolean {
  return !stageFinished(v) && v.step_index > 0
}

// canDone：结束的流程不能再推进。
export function canDone(v: Pick<WorkStageView, 'status'>): boolean {
  return !stageFinished(v)
}

// stageLevelLabel / stageLevelHint：放权等级的中英文案与一句解释。
export function stageLevelLabel(level: StageLevel): string {
  return level === 'remind' ? t('work.stage.level.remind') : t('work.stage.level.record')
}

export function stageLevelHint(level: StageLevel): string {
  return level === 'remind' ? t('work.stage.level.remindHint') : t('work.stage.level.recordHint')
}

// stageBadgeText 是会话线程头上那个小标签：「<步骤> n/N」；没有进行中的流程时为空串。
export function stageBadgeText(brief: SessionStageBrief | null | undefined): string {
  if (!brief?.step || !brief.step_total) return ''
  const no = brief.step_no ?? 0
  return t('work.stage.badge', { step: stageStepLabel(brief.step), no, total: brief.step_total })
}

// stageBadgeBlocked：被阻塞的标签换个样式（颜色不是唯一信号，文字里也有「阻塞」的说明在 title 上）。
export function stageBadgeBlocked(brief: SessionStageBrief | null | undefined): boolean {
  return brief?.status === 'blocked'
}

// stageWorkItemLink 是标签点过去的目标（工作项详情，抽屉由 ?id= 打开）。
export function stageWorkItemLink(brief: SessionStageBrief | null | undefined): string {
  const id = brief?.work_item_id ?? ''
  return id ? `/work?id=${encodeURIComponent(id)}` : ''
}

// stageDoneNotice 是「推进」没过的提示：把缺项列出来，不当错误弹窗。
export function stageDoneNotice(gate: StageGate | null | undefined): string {
  const missing = stageMissing(gate)
  if (!missing.length) return stageGateHolds(gate) ? t('work.stage.notice.needsHuman') : ''
  return t('work.stage.notice.missing', { items: missing.join('; ') })
}
