import { describe, expect, it } from 'vitest'
import { botPolicy, globalForm, globalPatch, hostsWithBot, testResultMessage } from './notifyPolicy'

describe('notification policy helpers', () => {
  it('preserves global defaults and emits only notification members', () => {
    const form = globalForm({ enabled: false, allow_hosts: ['a.test'], allow_http: true, max_attempts: 4, max_text_runes: 200, session_reply_delay_sec: 9, webhooks: [] })
    expect(globalPatch(form)).toEqual({ notification: { enabled: false, allow_hosts: ['a.test'], allow_http: true, max_attempts: 4, max_text_runes: 200, session_reply_delay_sec: 9 } })
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
