// worker 自助注册（`gofer worker join` 发起申请 → 管理员在 web 上限时决定）的纯逻辑：
// 配对码的分段显示 / 规范化 / 校验、倒计时、接受请求构造、错误文案与关闭态提示。
// 组件只管渲染，逻辑都在这里，便于在 node 环境下单测。
import { ApiError } from '../api/client'
import type { EnrollmentHost, WorkerEnrollment } from '../api/types'
import { t, te } from '../i18n'

// 待接受区的位置（Runners 页的锚点）与链接：Today 卡片与顶栏提醒都指到这里。
export const ENROLL_ANCHOR = 'enrollments'
export const ENROLLMENTS_LINK = `/runners#${ENROLL_ANCHOR}`

// PAIRING_ALPHABET 与 internal/workerenroll 的 pairingAlphabet 同口径：A-Z 去掉易混的 O I L U，
// 再加 2-9，共 30 个字符；8 位 ≈ 6.6×10¹¹ 种。显示时分成 4+4 两段。
export const PAIRING_ALPHABET = 'ABCDEFGHJKMNPQRSTVWXYZ23456789'
export const PAIRING_LEN = 8
export const PAIRING_SEGMENT = 4

// normalizePairingCode 转大写并去掉空格与横线（与 Go 侧 NormalizePairingCode 一致）。
export function normalizePairingCode(s: string): string {
  return (s ?? '').toUpperCase().replace(/[ -]/g, '')
}

export function isValidPairingCode(s: string): boolean {
  const code = normalizePairingCode(s)
  if (code.length !== PAIRING_LEN) return false
  for (const ch of code) {
    if (!PAIRING_ALPHABET.includes(ch)) return false
  }
  return true
}

// pairingSegments 把一个码拆成输入框里的两段（第二段不足 4 位时是短的）。
export function pairingSegments(code: string): [string, string] {
  const c = normalizePairingCode(code)
  return [c.slice(0, PAIRING_SEGMENT), c.slice(PAIRING_SEGMENT, PAIRING_LEN)]
}

// cleanSegment 保留允许字符并截到 4 位：输入时顺手吃掉小写与横线（用户直接从终端复制粘贴
// 也可以带着分隔符进来）。
export function cleanSegment(s: string): string {
  return normalizePairingCode(s)
    .split('')
    .filter((ch) => PAIRING_ALPHABET.includes(ch))
    .slice(0, PAIRING_SEGMENT)
    .join('')
}

// pairingFromSegments 把两段拼成提交用的规范值。
export function pairingFromSegments(first: string, second: string): string {
  return cleanSegment(first) + cleanSegment(second)
}

// pairingHint 是本地校验的提示（'' = 可以提交）；真值判定在服务端（常量时间比较，错 5 次作废）。
export function pairingHint(code: string): string {
  const norm = normalizePairingCode(code)
  if (!norm) return t('util.enroll.pairing.empty')
  if (!isValidPairingCode(norm)) {
    return t('util.enroll.pairing.len', { n: norm.length })
  }
  return ''
}

// decideCountdown 是决定窗口的倒计时：窗口默认只有 2 分钟，按秒显示（utils/approval.ts 的
// countdownText 是分钟精度，写「还剩 1 分钟」看不出还剩几秒）。
export function decideCountdown(decideUntil: number | undefined, nowSec: number): string {
  if (!decideUntil) return ''
  const left = Math.floor(decideUntil - nowSec)
  if (left <= 0) return t('util.enroll.expired')
  if (left < 60) return t('util.enroll.leftSec', { s: left })
  return t('util.enroll.leftMin', { m: Math.floor(left / 60), s: left % 60 })
}

// collectCountdown 是接受之后「worker 领取 token」的剩余时间（过了就自动撤销这次登记）。
export function collectCountdown(collectUntil: number | undefined, nowSec: number): string {
  const text = decideCountdown(collectUntil, nowSec)
  return text === t('util.enroll.expired') ? t('util.enroll.collectTimeout') : text
}

// isPending：还在等人决定（到点未处理即过期，列表里按状态显示）。
export function isPending(e: Pick<WorkerEnrollment, 'state' | 'decide_until'>, nowSec: number): boolean {
  return e.state === 'pending' && e.decide_until > nowSec
}

// pendingEnrollments 取出真正要人处理的申请（服务端已按最新在前排序）。
export function pendingEnrollments(items: WorkerEnrollment[], nowSec: number): WorkerEnrollment[] {
  return items.filter((e) => isPending(e, nowSec))
}

// RESERVED_WORKER_IDS：server / local 是保留名（AGENTS.md G043），服务端一律拒绝，这里当场提示。
export const RESERVED_WORKER_IDS = ['server', 'local']

const WORKER_ID_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/
const LABEL_RE = /^[A-Za-z0-9._-]{1,32}$/
const MAX_LABELS = 8

// workerIdError 返回 '' （可以提交）或一句人话；existing 是已知的 worker id（在线 worker 与
// 已接受的申请），命中也是当场提示——服务端同样会以 409 拒绝。
export function workerIdError(id: string, existing: string[] = []): string {
  const v = (id ?? '').trim()
  if (!v) return t('util.enroll.id.empty')
  if (!WORKER_ID_RE.test(v)) {
    return t('util.enroll.id.charset')
  }
  if (RESERVED_WORKER_IDS.includes(v.toLowerCase())) {
    return t('util.enroll.id.reserved', { v })
  }
  if (existing.includes(v)) {
    return t('util.enroll.id.exists', { v })
  }
  return ''
}

// splitList 把「逗号/空白分隔」的一行拆成清单（标签用）。
export function splitList(s: string): string[] {
  return (s ?? '')
    .split(/[,，\s]+/)
    .map((x) => x.trim())
    .filter(Boolean)
}

// labelsError 按服务端的标签限制（≤8 个，每个 ≤32 位 [A-Za-z0-9._-]）做本地校验。
export function labelsError(labels: string[]): string {
  if (labels.length > MAX_LABELS) return t('util.enroll.labels.max', { n: MAX_LABELS })
  for (const l of labels) {
    if (!LABEL_RE.test(l)) return t('util.enroll.labels.charset', { v: l })
  }
  return ''
}

export interface AcceptForm {
  codeFirst: string
  codeSecond: string
  workerId: string
  // 逗号分隔的标签输入框
  labels: string
  projects: string[]
}

export interface AcceptBody {
  pairing_code: string
  worker_id: string
  labels: string[]
  projects: string[]
}

// buildAcceptRequest 组装 POST accept 的请求体；error 非空时不带 body（本地就能看出问题，
// 少一次注定被拒的往返）。
export function buildAcceptRequest(
  form: AcceptForm,
  existingWorkerIds: string[] = [],
): { body: AcceptBody | null; error: string } {
  const code = pairingFromSegments(form.codeFirst, form.codeSecond)
  const codeErr = pairingHint(code)
  if (codeErr) return { body: null, error: codeErr }
  const idErr = workerIdError(form.workerId, existingWorkerIds)
  if (idErr) return { body: null, error: idErr }
  const labels = splitList(form.labels)
  const labelErr = labelsError(labels)
  if (labelErr) return { body: null, error: labelErr }
  return {
    body: { pairing_code: code, worker_id: form.workerId.trim(), labels, projects: [...form.projects] },
    error: '',
  }
}

// acceptErrorText 把接受失败的响应翻成一句人话：400 配对码不符（服务端原话）、409 已处理 / id 已存在、
// 403 无权限……原始 detail 附在括号里便于排查。
export function acceptErrorText(e: unknown): string {
  if (e instanceof ApiError) {
    const raw = e.detail || e.message
    switch (e.status) {
      case 400:
        return e.detail === 'pairing code mismatch'
          ? t('util.enroll.err.codeMismatch')
          : t('util.enroll.err.accept', { msg: raw })
      case 409:
        if (e.code === 'worker already exists') {
          return t('util.enroll.err.acceptExists', { msg: raw })
        }
        return t('util.enroll.err.acceptHandled', { msg: raw })
      case 403:
        return t('util.enroll.err.acceptForbidden')
      case 404:
        return t('util.enroll.err.acceptMissing')
      case 429:
        return t('util.enroll.err.accept', { msg: raw })
    }
  }
  return t('util.enroll.err.accept', { msg: e instanceof Error ? e.message : String(e) })
}

// rejectErrorText 同理（拒绝成功是 204，没有响应体）。
export function rejectErrorText(e: unknown): string {
  if (e instanceof ApiError) {
    const raw = e.detail || e.message
    switch (e.status) {
      case 409:
        return t('util.enroll.err.rejectHandled', { msg: raw })
      case 403:
        return t('util.enroll.err.rejectForbidden')
      case 404:
        return t('util.enroll.err.rejectMissing')
    }
  }
  return t('util.enroll.err.reject', { msg: e instanceof Error ? e.message : String(e) })
}

// disabledHint：列表响应 enabled=false 时显示的一行——功能默认关闭，要人在 server 配置里打开。
export function disabledHint(enabled: boolean): string {
  return enabled ? '' : t('util.enroll.disabled')
}

export function stateLabel(state: string): string {
  const key = `util.enroll.state.${state}`
  return te(key) ? t(key) : state
}

// enrollTitle：一行认人——hostname → 提议的 id（缺哪个用哪个，都没有就说未知）。
export function enrollTitle(e: Pick<WorkerEnrollment, 'host' | 'requested_id'>): string {
  const host = (e.host?.hostname ?? '').trim()
  const id = (e.requested_id ?? '').trim()
  if (host && id) return `${host} → ${id}`
  return host || id || t('util.enroll.unknownHost')
}

// hostLine：OS / 架构 · gofer 版本 · 协议版本（缺的项不占位）。
export function hostLine(host: EnrollmentHost | undefined): string {
  if (!host) return ''
  const parts: string[] = []
  if (host.os || host.arch) parts.push(`${host.os || '?'}/${host.arch || '?'}`)
  if (host.gofer_version) parts.push(`gofer v${host.gofer_version}`)
  if (host.protocol_version) parts.push(t('util.enroll.protocol', { v: host.protocol_version }))
  return parts.join(' · ')
}
