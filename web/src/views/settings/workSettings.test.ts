import { describe, expect, it } from 'vitest'
import { settings } from '../../i18n/pages/settings'

const read = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const page = read(import.meta.glob('./WorkSettings.vue', { eager: true, query: '?raw', import: 'default' }))
const layout = read(import.meta.glob('./SettingsLayout.vue', { eager: true, query: '?raw', import: 'default' }))
const router = read(import.meta.glob('../../router.routes.ts', { eager: true, query: '?raw', import: 'default' }))
const zh = settings.zh

describe('Work settings page', () => {
  it('is reachable from the settings menu and the router', () => {
    expect(layout).toContain("to: '/settings/work'")
    expect(router).toContain("path: 'work'")
    expect(router).toContain('WorkSettings.vue')
  })

  it('edits the summarizer agent / args, switch, thresholds, daily cap, handoff and request timeout', () => {
    for (const hook of ['ws-agent', 'ws-args', 'ws-enabled', 'ws-idle', 'ws-daily', 'ws-handoff']) {
      expect(page).toContain(`data-test="${hook}"`)
    }
    expect(page).toContain('putConfigWork({')
    for (const f of ['summarize_idle_min', 'summarize_min_interval_min', 'summarize_daily_limit', 'request_timeout_min', 'auto_handoff', 'summarizer_args']) {
      expect(page).toContain(f)
    }
  })

  it('saves from the sticky save bar and only while there are unsaved changes', () => {
    expect(page).toContain("import SaveBar from '../../ui/SaveBar.vue'")
    expect(page).toContain('<SaveBar')
    expect(page).toContain(':dirty="dirty"')
    expect(page).toContain('@save="save"')
    expect(page).toContain('@discard="load"')
  })

  it('says plainly when the summarizer is unavailable and shows the usage', () => {
    expect(page).toContain('data-test="ws-unavailable"')
    expect(zh['settings.work.unavailable']).toContain('整理功能暂不可用')
    expect(zh['settings.work.availableCapped']).toContain('今天已自动整理')
  })

  it('validates the args as a JSON string array and the thresholds before writing', () => {
    expect(page).toContain("t('settings.work.errArgs')")
    expect(page).toContain("t('settings.work.errThreshold')")
    expect(zh['settings.work.errArgs']).toContain('JSON 字符串数组')
    expect(zh['settings.work.errThreshold']).toContain('阈值要填正整数')
  })

  it('hints the default project when the summarizer project is left empty and shows the resolved one', () => {
    expect(page).toContain('data-test="ws-project"')
    expect(page).toContain("t('settings.work.projectPlaceholder')")
    expect(zh['settings.work.projectPlaceholder']).toContain('默认使用 default（~/.gofer/workspace）')
    expect(page).toContain('data-test="ws-project-hint"')
    expect(page).toContain('status.effective_project')
    expect(page).toContain('status.effective_dir')
  })

  it('has a needs-me notification switch (default off) with a per-item throttle', () => {
    for (const hook of ['ws-needsme', 'ws-needsme-throttle']) expect(page).toContain(`data-test="${hook}"`)
    expect(page).toContain('needs_me_notify: form.needs_me_notify')
    expect(page).toContain('needs_me_throttle_min: throttle')
    expect(page).toContain('needs_me_notify: false')
  })
})
