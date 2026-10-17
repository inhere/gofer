import { describe, expect, it } from 'vitest'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const dialog = raw(import.meta.glob('./SessionGrantDialog.vue', { eager: true, query: '?raw', import: 'default' }))
const drawer = raw(import.meta.glob('./SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))
const sessions = raw(import.meta.glob('../views/Sessions.vue', { eager: true, query: '?raw', import: 'default' }))
const permsPage = raw(import.meta.glob('../views/settings/Permissions.vue', { eager: true, query: '?raw', import: 'default' }))
const sessionGrantUtil = raw(import.meta.glob('../utils/sessionGrant.ts', { eager: true, query: '?raw', import: 'default' }))
const permsApi = raw(import.meta.glob('../api/permissions.ts', { eager: true, query: '?raw', import: 'default' }))
const permsUtil = raw(import.meta.glob('../utils/permissions.ts', { eager: true, query: '?raw', import: 'default' }))
const whoamiStore = raw(import.meta.glob('../store/whoami.ts', { eager: true, query: '?raw', import: 'default' }))
const authStore = raw(import.meta.glob('../store/auth.ts', { eager: true, query: '?raw', import: 'default' }))
const config = raw(import.meta.glob('../views/Config.vue', { eager: true, query: '?raw', import: 'default' }))

describe('session grant dialog wiring', () => {
  it('lists only session-grantable permissions and previews through the server endpoint', () => {
    expect(dialog).toContain('sessionGrantable(')
    expect(dialog).toContain('createPreviewScheduler(expandGrantPattern')
    expect(dialog).toContain('sessionSubject(')
    expect(dialog).toContain('SESSION_GRANT_EXPIRY_OPTIONS')
    expect(dialog).toContain('data-test="sg-threat"')
    expect(dialog).toContain('data-test="sg-ends"')
    expect(dialog).toContain('createAuthGrant(')
  })
  it('is mobile friendly', () => {
    expect(dialog).toContain('@media (max-width: 640px)')
  })
  it('is offered on session rows and in the drawer, to managers only, with a granted badge and revoke', () => {
    expect(sessions).toContain('data-test="row-grant"')
    expect(sessions).toContain("canGrantSession(whoMode.canManage, s)")
    expect(sessions).toContain('data-test="session-granted-badge"')
    expect(sessions).toContain('<SessionGrantDialog')
    expect(drawer).toContain('data-test="drawer-grant"')
    expect(drawer).toContain('canGrantSession(whoMode.canManage, session)')
    expect(drawer).toContain('data-test="session-grant-revoke"')
    expect(drawer).toContain('revokeAuthGrant(')
  })
})

describe('single source of truth for the expansion', () => {
  it('the permissions page previews through /v1/auth/expand', () => {
    expect(permsApi).toContain('/v1/auth/expand')
    expect(permsPage).toContain('createPreviewScheduler(expandGrantPattern')
    expect(permsPage).not.toContain('expandPatterns(')
  })
  it('the TypeScript copy of the expansion rules is gone', () => {
    expect(permsUtil).not.toContain('matchPattern')
    expect(permsUtil).not.toContain('function expandPattern')
  })
})

describe('whoami store', () => {
  it('is shared, refreshed on login change and after a config reload', () => {
    expect(whoamiStore).toContain('onAuthChange(resetWhoami)')
    expect(whoamiStore).toContain('export function refreshWhoami')
    expect(authStore).toContain('notifyAuthChange()')
    expect(config).toContain('refreshWhoami()')
    expect(permsPage).toContain('useWhoami()')
  })
})

describe('server-decided effective flag', () => {
  it('the badge and the permissions list read effective, with a fallback only when absent', () => {
    expect(permsUtil).toContain('effective?: boolean')
    expect(sessionGrantUtil).toContain("typeof g.effective === 'boolean'")
    expect(sessionGrantUtil).toContain('export function grantEffectiveLabel')
    expect(permsPage).toContain('grantEffectiveLabel(')
    expect(permsPage).toContain('data-test="perm-session-effective"')
  })
})
