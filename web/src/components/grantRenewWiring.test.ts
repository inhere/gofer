import { describe, expect, it } from 'vitest'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const dialog = raw(import.meta.glob('./GrantRenewDialog.vue', { eager: true, query: '?raw', import: 'default' }))
const drawer = raw(import.meta.glob('./SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))
const permsPage = raw(import.meta.glob('../views/settings/Permissions.vue', { eager: true, query: '?raw', import: 'default' }))
const permsApi = raw(import.meta.glob('../api/permissions.ts', { eager: true, query: '?raw', import: 'default' }))
const grantRenewUtil = raw(import.meta.glob('../utils/grantRenew.ts', { eager: true, query: '?raw', import: 'default' }))

describe('renewal api', () => {
  it('posts to the renewal endpoint with the expiry alone', () => {
    expect(permsApi).toContain('export function renewAuthGrant(')
    expect(permsApi).toContain('/renew')
    expect(permsApi).toContain('expires_sec: expiresSec')
  })
})

describe('renewal dialog', () => {
  it('offers the granting options, goes through the shared builder and reports the server reason', () => {
    expect(dialog).toContain('renewAuthGrant(')
    expect(dialog).toContain('buildGrantRenewRequest(')
    expect(dialog).toContain('RENEW_EXPIRY_OPTIONS')
    expect(dialog).toContain('renewFailureText(')
    expect(dialog).toContain('data-test="rg-expiry"')
    expect(dialog).toContain('data-test="rg-error"')
    expect(dialog).toContain('data-test="rg-submit"')
  })
  it('is mobile friendly and colours through the theme tokens', () => {
    expect(dialog).toContain('@media (max-width: 640px)')
    expect(dialog).not.toMatch(/#[0-9a-fA-F]{3,8}\b/)
  })
})

describe('renewal wiring', () => {
  it('the sessions tab shows the remaining time and a renewal button on in-force rows', () => {
    expect(permsPage).toContain('canRenewGrant(mode.canManage, g, nowSec)')
    expect(permsPage).toContain('grantRemainingText(g.expires_at, nowSec.value)')
    expect(permsPage).toContain('data-test="perm-session-remaining"')
    expect(permsPage).toContain('data-test="perm-session-renew"')
    expect(permsPage).toContain('data-test="perm-session-needs-grant"')
    expect(permsPage).toContain('<GrantRenewDialog')
  })
  it('the drawer badge offers the same dialog once under an hour is left', () => {
    expect(drawer).toContain('canRenewBadge(whoMode.canManage, grantBadge, nowSec)')
    expect(drawer).toContain('badgeRenewIds(grantBadge)')
    expect(drawer).toContain('data-test="session-grant-remaining"')
    expect(drawer).toContain('data-test="session-grant-renew"')
    expect(drawer).toContain('<GrantRenewDialog')
  })
  it('keeps the renewal rules in one module on the client', () => {
    expect(grantRenewUtil).toContain('RENEW_SOON_SEC = 3600')
    expect(grantRenewUtil).toContain('export function grantRenewState')
    expect(grantRenewUtil).toContain('export function renewFailureText')
  })
})
