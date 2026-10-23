import { describe, expect, it } from 'vitest'
import { settings as settingsDict } from '../../i18n/pages/settings'

const zh = settingsDict.zh

const read = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const page = read(import.meta.glob('./Permissions.vue', { eager: true, query: '?raw', import: 'default' }))
const layout = read(import.meta.glob('./SettingsLayout.vue', { eager: true, query: '?raw', import: 'default' }))
const router = read(import.meta.glob('../../router.routes.ts', { eager: true, query: '?raw', import: 'default' }))
const client = read(import.meta.glob('../../api/client.ts', { eager: true, query: '?raw', import: 'default' }))
const planDetail = read(import.meta.glob('../PlanDetail.vue', { eager: true, query: '?raw', import: 'default' }))
const api = read(import.meta.glob('../../api/permissions.ts', { eager: true, query: '?raw', import: 'default' }))

describe('Permissions settings page wiring', () => {
  it('is reachable from the settings menu and the router', () => {
    expect(layout).toContain("to: '/settings/permissions'")
    expect(router).toContain("path: 'permissions'")
    expect(router).toContain('Permissions.vue')
  })

  it('has the four tabs', () => {
    for (const k of ['grants', 'sessions', 'catalog', 'audit']) expect(page).toContain(`key: '${k}'`)
    // 页签文案走字典（settings.perm.tab.*），中文在 zh 字典里
    for (const l of ['grants', 'sessions', 'catalog', 'audit']) expect(page).toContain(`label: 'settings.perm.tab.${l}'`)
    const labels: Record<string, string> = { grants: '授权', sessions: '会话授权', catalog: '权限目录', audit: '审计' }
    for (const [k, label] of Object.entries(labels)) expect(zh[`settings.perm.tab.${k}`]).toBe(label)
  })

  it('uses the documented auth endpoints', () => {
    for (const p of ['/v1/auth/whoami', '/v1/auth/catalog', '/v1/auth/grants', '/v1/auth/audit']) expect(api).toContain(p)
    expect(api).toContain('/activate')
    expect(api).toContain("method: 'DELETE'")
  })

  it('previews the expansion, asks for confirmation and reason, and is read-only without a root admin', () => {
    expect(page).toContain('createPreviewScheduler(expandGrantPattern')
    expect(page).toContain('data-test="perm-preview"')
    expect(page).toContain('data-test="perm-confirm"')
    expect(page).toContain('data-test="perm-note"')
    expect(page).toContain('data-test="perm-readonly"')
    expect(page).toContain('useWhoami()')
    expect(page).toContain('data-test="perm-pending"')
    expect(page).toContain('data-test="perm-activate"')
  })

  it('collapses to one column on narrow screens', () => {
    expect(page).toContain('@media (max-width: 760px)')
    expect(page).toContain('overflow-x: auto')
  })
})

describe('403 message and plan notify switch', () => {
  it('client rewrites permission-required 403s', () => {
    expect(client).toContain('permissionDeniedMessage(')
  })
  it('plan detail has the notify switch using PATCH notify', () => {
    expect(planDetail).toContain('data-test="plan-notify"')
    expect(planDetail).toContain('setPlanNotify(')
    expect(client).toContain('JSON.stringify({ notify })')
  })
})
