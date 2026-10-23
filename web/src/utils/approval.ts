// 待批 job（gofer-9b1b，--hold / awaiting_approval）的纯逻辑：命令拼接、来源与倒计时文案、
// 错误码到中文提示，以及批准 / 拒绝的操作控制器。ApprovalPanel 与「今天」决策卡共用；
// 组件只管渲染，便于在 node 环境下单测。
import { ref } from 'vue'
import { ApiError } from '../api/client'
import type { Job, JobHold } from '../api/types'
import { t, te } from '../i18n'

// shellQuote：按 POSIX shell 规则给单个参数加引号——安全字符原样，其余用单引号包住，
// 内部的单引号写成 '\''。只用于展示（让人看清每个参数的边界），不拿去执行。
export function shellQuote(arg: string): string {
  if (arg === '') return "''"
  if (/^[A-Za-z0-9_\-+=/.,:@%^]+$/.test(arg)) return arg
  return `'${arg.replace(/'/g, `'\\''`)}'`
}

// commandLine：exec job 的 argv 拼成一行可读命令。
export function commandLine(argv: readonly string[] | undefined): string {
  return (argv ?? []).map(shellQuote).join(' ')
}

// originText：hold.origin 的人话——job:<id> / agent-session:<id> / 提交渠道。
export function originText(origin: string | undefined): string {
  if (!origin) return ''
  if (origin.startsWith('job:')) return t('util.approval.originJob', { id: origin.slice(4) })
  if (origin.startsWith('agent-session:')) return t('util.approval.originSession', { id: origin.slice('agent-session:'.length) })
  return origin
}

// submitterText：面板「提交者」一行——来源、caller、渠道（重复的不写两遍）。
export function submitterText(job: Pick<Job, 'hold' | 'caller_id' | 'channel' | 'client'>): string {
  const parts: string[] = []
  const origin = job.hold?.origin ?? ''
  const o = originText(origin)
  if (o) parts.push(o)
  if (job.caller_id) parts.push(`caller ${job.caller_id}`)
  if (job.channel && job.channel !== origin) parts.push(t('util.approval.viaChannel', { channel: job.channel }))
  if (job.client) parts.push(job.client)
  return parts.join(' · ')
}

// countdownText：距过期还剩多久（秒级精度到分钟）；过了就是「已过期」。
export function countdownText(expiresAt: number | undefined, nowSec: number): string {
  if (!expiresAt) return ''
  const left = Math.floor(expiresAt - nowSec)
  if (left <= 0) return t('util.approval.expired')
  const d = Math.floor(left / 86400)
  const h = Math.floor((left % 86400) / 3600)
  const m = Math.floor((left % 3600) / 60)
  if (d > 0) return t('util.approval.leftDays', { d, h })
  if (h > 0) return t('util.approval.leftHours', { h, m })
  if (m > 0) return t('util.approval.leftMinutes', { m })
  return t('util.approval.leftSeconds', { s: left })
}

// 已经有结论的 hold 在 meta 区回显：决定 → 人话（认不得的原样返回）。
export function decisionText(hold: JobHold | undefined): string {
  const d = hold?.decision
  if (!d) return ''
  const key = `util.approval.decision.${d}`
  return te(key) ? t(key) : d
}

// approvalErrorText：批准 / 拒绝失败按状态码给一句提示（原始信息附在后面便于排查）。
export function approvalErrorText(e: unknown, action: 'approve' | 'reject'): string {
  if (e instanceof ApiError) {
    const raw = e.detail || e.message
    switch (e.status) {
      case 409:
        return t('util.approval.err.conflict', { action: actionText(action), raw })
      case 503:
        return t('util.approval.err.upgrading', { action: actionText(action) })
      case 403:
        return t('util.approval.err.forbidden', { action: actionText(action) })
      case 404:
        return t('util.approval.err.gone', { action: actionText(action) })
      case 400:
        return t('util.approval.err.badRequest', { action: actionText(action), raw })
    }
  }
  return t('util.approval.err.generic', {
    action: actionText(action),
    err: e instanceof Error ? e.message : String(e),
  })
}

// actionText：错误文案里的动词（批准 / 拒绝），跟当前语言。
function actionText(action: 'approve' | 'reject'): string {
  return action === 'approve' ? t('util.approval.action.approve') : t('util.approval.action.reject')
}

export interface ApprovalApi {
  approve: (id: string, note?: string) => Promise<Job>
  reject: (id: string, reason: string, resume?: boolean) => Promise<Job>
}

// createApprovalActions：面板的操作状态机。「批准」一键直发（不二次确认），busy 期间的重复点击
// 被吞掉；「拒绝」先展开理由框，理由可空。成功后回调 onDone 把最新 job 交回页面。
export function createApprovalActions(id: () => string, api: ApprovalApi, onDone: (j: Job) => void) {
  const busy = ref<'' | 'approve' | 'reject'>('')
  const error = ref('')
  const rejectOpen = ref(false)
  const rejectNote = ref('')

  async function approve(): Promise<void> {
    if (busy.value) return
    busy.value = 'approve'
    error.value = ''
    try {
      onDone(await api.approve(id()))
    } catch (e) {
      error.value = approvalErrorText(e, 'approve')
    } finally {
      busy.value = ''
    }
  }

  function openReject(): void {
    error.value = ''
    rejectOpen.value = true
  }

  function cancelReject(): void {
    rejectOpen.value = false
    rejectNote.value = ''
  }

  async function reject(): Promise<void> {
    if (busy.value) return
    busy.value = 'reject'
    error.value = ''
    try {
      // 待批时理由可选：空串照样提交（resume 恒 false，待批的拒绝不能续投）。
      onDone(await api.reject(id(), rejectNote.value.trim(), false))
      rejectOpen.value = false
      rejectNote.value = ''
    } catch (e) {
      error.value = approvalErrorText(e, 'reject')
    } finally {
      busy.value = ''
    }
  }

  return { busy, error, rejectOpen, rejectNote, approve, openReject, cancelReject, reject }
}
