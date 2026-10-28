import { describe, expect, it } from 'vitest'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const page = raw(import.meta.glob('./Extensions.vue', { eager: true, query: '?raw', import: 'default' }))
const layout = raw(import.meta.glob('./SettingsLayout.vue', { eager: true, query: '?raw', import: 'default' }))
const router = raw(import.meta.glob('../../router.routes.ts', { eager: true, query: '?raw', import: 'default' }))
const api = raw(import.meta.glob('../../api/settings.ts', { eager: true, query: '?raw', import: 'default' }))
const gate = raw(import.meta.glob('../../utils/permissionGate.ts', { eager: true, query: '?raw', import: 'default' }))

describe('extension settings page wiring', () => {
  it('is reachable from the settings menu and the router', () => {
    expect(layout).toContain("{ to: '/settings/extensions', key: 'settings.nav.extensions' }")
    expect(router).toContain("name: 'settings-extensions'")
    expect(router).toContain("import('./views/settings/Extensions.vue')")
    expect(router).toContain("path: 'extensions'")
  })

  it('reads the registry and writes per key through the settings API', () => {
    expect(page).toContain('listSettings()')
    expect(page).toContain('updateSetting(it.key, valueOfDraft(it, d))')
    expect(page).toContain('resetSetting(it.key)')
    expect(page).toContain('groupSettings(items.value)')
    // 恢复默认要二次确认，不能一点就删。
    expect(page).toContain('pendingReset === it.key')
    expect(page).toContain("t('settings.ext.confirmReset')")
    expect(api).toContain('`/v1/settings/${encodeURIComponent(key)}`')
    expect(api).toContain("method: 'DELETE'")
    expect(api).toContain("method: 'PUT'")
  })

  it('previews an import before writing it', () => {
    expect(page).toContain('importSettings(yaml, true)')
    expect(page).toContain('importSettings(state.yaml, false)')
    expect(page).toContain('data-test="settings-import-preview"')
    expect(page).toContain('data-test="settings-import-confirm"')
    expect(api).toContain('/v1/settings/import?dry_run=1')
  })

  it('exports the modified keys as a YAML download', () => {
    expect(page).toContain('exportSettings()')
    expect(api).toContain("downloadFile('/v1/settings/export', 'gofer-settings.yaml')")
  })

  it('goes read-only without settings.write or on a read-only ceiling', () => {
    expect(gate).toContain("'settings.write': { perms: ['settings.write']")
    expect(page).toContain("can('settings.write')")
    expect(page).toContain('v-for="group in navGroups"')
    expect(page).toContain("item.key === 'nav.today' || item.key === 'nav.settings'")
    expect(page).toContain('data-test="nav-settings-readonly"')
    expect(page).toContain('invalidateMetaCache()')
  })

  it('renders every kind with its own editor and shows the prompt default note', () => {
    expect(page).toContain("controlFor(it) === 'int'")
    expect(page).toContain("controlFor(it) === 'bool'")
    expect(page).toContain("controlFor(it) === 'list'")
    expect(page).toContain("controlFor(it) === 'textarea'")
    expect(page).toContain('PROMPT_EMPTY_HINT')
    expect(page).toContain('data-test="setting-default"')
    expect(page).toContain("it.updated_by || t('settings.ext.unknown')")
  })
})
