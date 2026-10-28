import type { NotificationView, NotifyTestResp } from '../../api/types'

export interface GlobalNotifyForm {
  enabled: boolean
  allowHosts: string[]
  allowHttp: boolean
  maxAttempts: string
  maxTextRunes: string
  sessionReplyDelaySec: string
}

export function globalForm(n?: NotificationView | null): GlobalNotifyForm {
  return {
    enabled: n?.enabled ?? true,
    allowHosts: [...(n?.allow_hosts ?? [])],
    allowHttp: n?.allow_http ?? false,
    maxAttempts: n && n.max_attempts > 0 ? String(n.max_attempts) : '',
    maxTextRunes: n && n.max_text_runes > 0 ? String(n.max_text_runes) : '3000',
    sessionReplyDelaySec: String(n?.session_reply_delay_sec ?? 120),
  }
}

function optionalInt(value: string): number | null {
  const text = value.trim()
  if (!text) return null
  const n = Number(text)
  return Number.isFinite(n) ? Math.trunc(n) : null
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
