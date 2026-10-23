// job 详情「继续会话」的续接方式：按后端 /v1/agents 报告的续接能力预判，
// 不可用的方式灰显并写原因（后端对不支持的组合同样会返回 400）。
import type { AgentInfo } from '../api/types'
import { t } from '../i18n'

export type ResumeMode = '' | 'session' | 'interactive' | 'batch'

export interface ResumeChoice {
  value: ResumeMode
  label: string
  disabled: boolean
  // 不可用的原因，或可用时的补充说明
  note: string
  // 需要改用的目标 agent（同会话族的另一个 agent）；空 = 沿用源 agent
  agent: string
}

export interface ResumeSource {
  // 源 job 的 agent（续接载体 exec 时应传 resume_agent）
  agent: string
  interactive: boolean
}

export interface ResumeContext {
  // 源 job 所在 runner 能否承载持续 ACP 会话（local / worker）；未知按可用
  runnerSessionOk?: boolean
  // 项目是否允许交互 job；未知按允许
  projectAllowsInteractive?: boolean
}

function byKey(a: AgentInfo, b: AgentInfo): number {
  return a.key.localeCompare(b.key)
}

// 在「源 agent 自己 + 同会话族的其它 agent」里找第一个满足 pred 的目标，源 agent 优先。
function pickTarget(src: AgentInfo | undefined, agents: AgentInfo[], pred: (a: AgentInfo) => boolean): AgentInfo | undefined {
  if (!src) return undefined
  if (pred(src)) return src
  const family = src.session_family ?? ''
  if (family === '') return undefined
  return agents
    .filter((a) => a.key !== src.key && a.session_family === family && a.available && pred(a))
    .sort(byKey)[0]
}

export function resumeChoices(source: ResumeSource, agents: AgentInfo[], ctx: ResumeContext = {}): ResumeChoice[] {
  const src = agents.find((a) => a.key === source.agent)
  const out: ResumeChoice[] = [
    { value: '', label: t('util.resumeChoice.asIs'), disabled: false, note: t('util.resumeChoice.asIsNote'), agent: '' },
  ]
  const acp = pickTarget(src, agents, (a) => a.type === 'acp-agent' && a.acp_load_session !== false)
  const pty = pickTarget(src, agents, (a) => a.type !== 'acp-agent' && a.type !== 'exec' && a.session_resume_interactive === true)
  const batch = pickTarget(src, agents, (a) => a.type !== 'acp-agent' && a.type !== 'exec' && a.session_resume === true)

  const mk = (value: ResumeMode, label: string, target: AgentInfo | undefined, reasonIfMissing: string, extra?: string): ResumeChoice => {
    if (!src) return { value, label, disabled: false, note: '', agent: '' }
    if (!target) return { value, label, disabled: true, note: reasonIfMissing, agent: '' }
    const switched = target.key !== src.key
    return {
      value,
      label,
      disabled: extra !== undefined && extra !== '',
      note: extra || (switched ? t('util.resumeChoice.switchNote', { agent: target.key }) : ''),
      agent: switched ? target.key : '',
    }
  }
  out.push(mk('session', t('util.resumeChoice.acp'), acp, t('util.resumeChoice.acpMissing'),
    ctx.runnerSessionOk === false ? t('util.resumeChoice.acpRunner') : ''))
  out.push(mk('interactive', t('util.resumeChoice.pty'), pty, t('util.resumeChoice.ptyMissing'),
    ctx.projectAllowsInteractive === false ? t('util.resumeChoice.ptyProject') : ''))
  out.push(mk('batch', t('util.resumeChoice.batch'), batch, t('util.resumeChoice.batchMissing')))
  return out
}

// 各形态下 prompt 的要求：none = 不需要（隐藏）、optional = 选填、required = 必填
export type PromptNeed = 'none' | 'optional' | 'required'

export function resumePromptNeed(mode: ResumeMode, srcInteractive: boolean, srcIsAcp: boolean): PromptNeed {
  switch (mode) {
    case 'session': return 'optional'
    case 'interactive': return 'none'
    case 'batch': return 'required'
    default:
      if (srcInteractive) return 'none'
      return srcIsAcp ? 'optional' : 'required'
  }
}

// 跳转是否带 ?attach=1：以「返回的新 job」是否交互为准，而不是源 job。
export function attachQuery(newJob: { interactive?: boolean }): string {
  return newJob.interactive ? '?attach=1' : ''
}
