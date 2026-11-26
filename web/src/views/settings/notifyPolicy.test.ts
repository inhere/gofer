import { describe, expect, it } from 'vitest'
import type { NotificationView } from '../../api/types'
import { botPolicy, globalForm, globalPatch, hostsWithBot, reminderAfterMinValue, sessionReplyDelaySecValue, testResultMessage } from './notifyPolicy'

// view 是 GET /v1/config 的 server.notification 视图（只列出本文件用到的成员）。
function view(over: Partial<NotificationView>): NotificationView {
  return {
    webhooks: [],
    max_text_runes: 0,
    allow_hosts: [],
    allow_http: false,
    max_attempts: 0,
    enabled: true,
    session_reply_delay_sec: 120,
    reminder_after_min: 30,
    reminder_after_min_default: true,
    ...over,
  }
}

describe('notification policy helpers', () => {
  it('preserves global defaults and emits only notification members', () => {
    const form = globalForm(view({ enabled: false, allow_hosts: ['a.test'], allow_http: true, max_attempts: 4, max_text_runes: 200, session_reply_delay_sec: 9, reminder_after_min: 9, reminder_after_min_default: false }))
    expect(globalPatch(form)).toEqual({ notification: { enabled: false, allow_hosts: ['a.test'], allow_http: true, max_attempts: 4, max_text_runes: 200, session_reply_delay_sec: 9, reminder_after_min: 9 } })
  })
  it('keeps an explicit 0 reminder delay as the choice "no reminder"', () => {
    const form = globalForm(view({ reminder_after_min: 0, reminder_after_min_default: false }))
    expect(form.reminderAfterMin).toBe('0')
    expect(globalPatch(form).notification.reminder_after_min).toBe(0)
  })
  it('falls back to the default reminder delay when the input is empty or not a number', () => {
    const form = globalForm(view({ reminder_after_min: 30, reminder_after_min_default: true }))
    expect(globalPatch({ ...form, reminderAfterMin: '' }).notification.reminder_after_min).toBe(30)
    expect(globalPatch({ ...form, reminderAfterMin: 'abc' }).notification.reminder_after_min).toBe(30)
    expect(reminderAfterMinValue({ ...form, reminderAfterMin: ' 45 ' })).toBe(45)
  })
  it('keeps an explicit 0 reply delay as the choice "no reminder"', () => {
    // gofer-msm5：saveBots 里曾经是 Number(...) || 120，0 被悄悄改成 120（后端 0 = 关掉提醒）。
    const form = globalForm(view({ session_reply_delay_sec: 0 }))
    expect(form.sessionReplyDelaySec).toBe('0')
    expect(sessionReplyDelaySecValue(form)).toBe(0)
    expect(globalPatch(form).notification.session_reply_delay_sec).toBe(0)
  })
  it('falls back to the default reply delay when the input is empty or not a number', () => {
    const form = globalForm(view({ session_reply_delay_sec: 120 }))
    expect(sessionReplyDelaySecValue({ ...form, sessionReplyDelaySec: '' })).toBe(120)
    expect(sessionReplyDelaySecValue({ ...form, sessionReplyDelaySec: 'abc' })).toBe(120)
    expect(sessionReplyDelaySecValue({ ...form, sessionReplyDelaySec: ' 45 ' })).toBe(45)
  })
  it('extracts host without credentials and adds it once', () => {
    const policy = botPolicy('https://user:secret@Hooks.Example.test/path', [], false)
    expect(policy).toMatchObject({ host: 'hooks.example.test', missing: true })
    expect(hostsWithBot([], policy.host)).toEqual(['hooks.example.test'])
  })
  it('explains structured allowlist refusal', () => {
    expect(testResultMessage({ ok: false, target: 'webhook', code: 'host_not_allowed', host: 'x.test' }, (key, params) => `${key}:${params?.host}`)).toBe('settings.notify.hostRejected:x.test')
  })
})
