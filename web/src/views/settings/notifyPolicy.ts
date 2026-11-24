import type { NotificationView, NotifyTestResp } from '../../api/types'

export interface GlobalNotifyForm {
  enabled: boolean
  allowHosts: string[]
  allowHttp: boolean
  maxAttempts: string
  maxTextRunes: string
  sessionReplyDelaySec: string
  reminderAfterMin: string
}

// DEFAULT_REMINDER_AFTER_MIN 与后端的 config.DefaultReminderAfterMin 一致：配置文件没写时用它。
export const DEFAULT_REMINDER_AFTER_MIN = 30

export function globalForm(n?: NotificationView | null): GlobalNotifyForm {
  return {
    enabled: n?.enabled ?? true,
    allowHosts: [...(n?.allow_hosts ?? [])],
    allowHttp: n?.allow_http ?? false,
    maxAttempts: n && n.max_attempts > 0 ? String(n.max_attempts) : '',
    maxTextRunes: n && n.max_text_runes > 0 ? String(n.max_text_runes) : '3000',
    sessionReplyDelaySec: String(n?.session_reply_delay_sec ?? 120),
    reminderAfterMin: String(n?.reminder_after_min ?? DEFAULT_REMINDER_AFTER_MIN),
  }
}

function optionalInt(value: string): number | null {
  const text = value.trim()
  if (!text) return null
  const n = Number(text)
  return Number.isFinite(n) ? Math.trunc(n) : null
}

// reminderAfterMinValue 把输入框转成提交值：留空（或不是数字）= 默认 30；0 是有效选择
// （不再提醒，不能当成缺省）；负数原样发出，由服务端校验拒绝（400），前端不悄悄夹值。
export function reminderAfterMinValue(form: GlobalNotifyForm): number {
  return optionalInt(form.reminderAfterMin) ?? DEFAULT_REMINDER_AFTER_MIN
}

// Only edited members are sent: the server retains webhooks, desktop and secrets.
export function globalPatch(form: GlobalNotifyForm) {
  return { notification: {
    enabled: form.enabled,
    allow_hosts: form.allowHosts.map((host) => host.trim()).filter(Boolean),
    allow_http: form.allowHttp,
    max_attempts: optionalInt(form.maxAttempts) ?? 0,
    max_text_runes: optionalInt(form.maxTextRunes) ?? 0,
    session_reply_delay_sec: optionalInt(form.sessionReplyDelaySec) ?? 120,
    reminder_after_min: reminderAfterMinValue(form),
  } }
}

export function botPolicy(raw: string, hosts: string[], allowHttp: boolean) {
  try {
    const url = new URL(raw.trim())
    const host = url.hostname.replace(/^\[|\]$/g, '')
    return {
      host,
      missing: !!host && !hosts.some((entry) => entry.trim().toLowerCase() === host.toLowerCase()),
      httpBlocked: url.protocol === 'http:' && !allowHttp,
    }
  } catch {
    return { host: '', missing: false, httpBlocked: false }
  }
}

export function hostsWithBot(hosts: string[], host: string): string[] {
  return !host || hosts.some((item) => item.trim().toLowerCase() === host.toLowerCase())
    ? [...hosts] : [...hosts, host]
}

export function testResultMessage(result: NotifyTestResp, translate: (key: string, params?: Record<string, string>) => string): string {
  if (result.ok) return translate('settings.notify.testWebhookOk')
  if (result.code === 'host_not_allowed' && result.host) {
    return translate('settings.notify.hostRejected', { host: result.host })
  }
  return translate('settings.notify.testFailed', { error: result.error ?? '' })
}
