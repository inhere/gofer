import { describe, expect, it } from 'vitest'
import { dicts } from '../i18n'
import {
  DEFAULT_SHELL_PREFS,
  SHELL_NAV_GROUPS,
  SHELL_PREFS_KEY,
  TAB_BAR_ITEMS,
  hasSidebar,
  hasTabBar,
  moreActive,
  moreItems,
  navItemActive,
  needsMeTotal,
  paletteHits,
  parsePaletteQuery,
  parseShellPrefs,
  readShellPrefs,
  shellForm,
  sidebarExpanded,
  sidebarNavGroups,
  tabBarActive,
  tabBarItems,
  withProject,
  writeShellPrefs,
  type StorageLike,
} from './shell'

function memoryStorage(initial: Record<string, string> = {}): StorageLike & { data: Record<string, string> } {
  const data = { ...initial }
  return {
    data,
    getItem: (k) => (k in data ? data[k] : null),
    setItem: (k, v) => {
      data[k] = v
    },
  }
}

describe('shell form by viewport width (design §6)', () => {
  it('is a tab bar at ≤760, a rail at 761–1099 and the full sidebar from 1100', () => {
    expect(shellForm(390)).toBe('tabs')
    expect(shellForm(760)).toBe('tabs')
    expect(shellForm(761)).toBe('rail')
    expect(shellForm(1024)).toBe('rail')
    expect(shellForm(1099)).toBe('rail')
    expect(shellForm(1100)).toBe('desktop')
    expect(shellForm(1440)).toBe('desktop')
  })

  it('falls back to the desktop shell when the width is unknown', () => {
    expect(shellForm(0)).toBe('desktop')
    expect(shellForm(-1)).toBe('desktop')
    expect(shellForm(Number.NaN)).toBe('desktop')
  })

  it('mounts a sidebar everywhere except the tab-bar width, and a tab bar only there', () => {
    expect(hasSidebar('desktop')).toBe(true)
    expect(hasSidebar('rail')).toBe(true)
    expect(hasSidebar('tabs')).toBe(false)
    expect(hasTabBar('tabs')).toBe(true)
    expect(hasTabBar('rail')).toBe(false)
  })
})

describe('sidebar expansion', () => {
  it('is fully expanded on desktop unless the user collapsed it', () => {
    expect(sidebarExpanded('desktop', { collapsed: false, nodesOpen: false })).toBe(true)
    expect(sidebarExpanded('desktop', { collapsed: true, nodesOpen: false })).toBe(false)
  })

  it('is always the icon rail at 761–1099 and never a sidebar on phones', () => {
    expect(sidebarExpanded('rail', { collapsed: false, nodesOpen: true })).toBe(false)
    expect(sidebarExpanded('tabs', { collapsed: false, nodesOpen: true })).toBe(false)
  })
})

describe('local prefs (gofer.shell)', () => {
  it('round-trips both switches', () => {
    const s = memoryStorage()
    expect(readShellPrefs(s)).toEqual(DEFAULT_SHELL_PREFS)
    writeShellPrefs({ collapsed: true, nodesOpen: true }, s)
    expect(readShellPrefs(s)).toEqual({ collapsed: true, nodesOpen: true })
    expect(Object.keys(s.data)).toEqual([SHELL_PREFS_KEY])
  })

  it('falls back to the defaults for bad or hand-edited values', () => {
    expect(parseShellPrefs(null)).toEqual(DEFAULT_SHELL_PREFS)
    expect(parseShellPrefs('not json')).toEqual(DEFAULT_SHELL_PREFS)
    expect(parseShellPrefs('null')).toEqual(DEFAULT_SHELL_PREFS)
    expect(parseShellPrefs('{"collapsed":"yes","nodesOpen":1}')).toEqual(DEFAULT_SHELL_PREFS)
    expect(parseShellPrefs('{"collapsed":true}')).toEqual({ collapsed: true, nodesOpen: false })
    expect(readShellPrefs(memoryStorage({ [SHELL_PREFS_KEY]: '{' }))).toEqual(DEFAULT_SHELL_PREFS)
  })

  it('keeps working when storage is unavailable (private mode)', () => {
    expect(readShellPrefs(null)).toEqual(DEFAULT_SHELL_PREFS)
    expect(() => writeShellPrefs({ collapsed: true, nodesOpen: false }, null)).not.toThrow()
  })
})

describe('navigation groups', () => {
  it('has the four groups of §6.1, each with the pages it names', () => {
    expect(SHELL_NAV_GROUPS.map((g) => g.key)).toEqual(['nav.group.core', 'nav.group.collab', 'nav.group.auto', 'nav.group.infra'])
    expect(SHELL_NAV_GROUPS[1].items.map((i) => i.key)).toEqual(['nav.mail', 'nav.review', 'nav.issues', 'nav.memory', 'nav.dashboard'])
    expect(SHELL_NAV_GROUPS[2].items.map((i) => i.key)).toEqual(['nav.workflows', 'nav.schedules'])
    expect(SHELL_NAV_GROUPS[3].items.map((i) => i.key)).toEqual(['nav.runners', 'nav.agents', 'nav.projects', 'nav.settings'])
  })

  it('filters hidden navigation entries, preserves today/settings, and fills phone tabs', () => {
    const hidden = ['nav.workbench', 'nav.mail', 'nav.settings', 'nav.unknown']
    const sidebar = sidebarNavGroups(hidden).flatMap((g) => g.items).map((i) => i.key)
    expect(sidebar).not.toContain('nav.workbench')
    expect(sidebar).toEqual(expect.arrayContaining(['nav.today', 'nav.settings']))
    expect(moreItems(hidden).map((i) => i.key)).not.toContain('nav.mail')
    expect(moreItems(hidden).map((i) => i.key)).toContain('nav.settings')
    expect(tabBarItems(['nav.workbench', 'nav.jobs']).map((i) => i.key)).toEqual(['nav.today', 'nav.plans', 'nav.work', 'nav.sessions'])
  })
  it('points 任务 at /jobs and keeps the sidebar free of a project picker', () => {
    const all = SHELL_NAV_GROUPS.flatMap((g) => g.items)
    expect(all.find((i) => i.key === 'nav.jobs')?.to).toBe('/jobs')
    expect(all.some((i) => i.to === '/board')).toBe(false)
    expect(all.every((i) => i.to !== '/projects')).toBe(false)
  })

  it('lights an item on its own route and its subpages', () => {
    expect(navItemActive('/jobs', '/jobs')).toBe(true)
    expect(navItemActive('/jobs/j1', '/jobs')).toBe(true)
    expect(navItemActive('/jobsx', '/jobs')).toBe(false)
    expect(navItemActive('/plans/p1', '/plans')).toBe(true)
  })

  it('gives phones the 5 tabs of §16-3 and puts 工作项 first in 更多', () => {
    expect(TAB_BAR_ITEMS.map((i) => i.to)).toEqual(['/today', '/workbench', '/jobs', '/plans'])
    const more = moreItems()
    expect(more[0].to).toBe('/work')
    // 标签栏已有的页面不再重复出现在「更多」里
    for (const tab of TAB_BAR_ITEMS) expect(more.some((i) => i.to === tab.to)).toBe(false)
    expect(more.map((i) => i.to)).toContain('/settings')
  })

  it('lists 记忆 in the collab group with its own icon and puts it in 更多', () => {
    const item = SHELL_NAV_GROUPS.flatMap((g) => g.items).find((i) => i.to === '/memory')
    expect(item?.key).toBe('nav.memory')
    expect(item?.icon).toBe('memory')
    // 就排在 Issues 下面（协作组）
    const collab = SHELL_NAV_GROUPS[1].items.map((i) => i.to)
    expect(collab.indexOf('/memory')).toBe(collab.indexOf('/issues') + 1)
    // 手机「更多」抽屉里同步出现（它不是底部 5 标签之一）
    expect(moreItems().map((i) => i.to)).toContain('/memory')
  })

  it('activates one tab at a time and falls back to 更多', () => {
    expect(tabBarActive('/today')).toBe('/today')
    expect(tabBarActive('/jobs/j1')).toBe('/jobs')
    expect(tabBarActive('/settings/config')).toBe(null)
    expect(moreActive('/settings/config')).toBe(true)
    expect(moreActive('/plans')).toBe(false)
    expect(moreActive('/access')).toBe(false)
  })
})

describe('needs-me count (merged decision queue + worker requests)', () => {
  it('adds both sources', () => {
    expect(needsMeTotal(3, 2)).toBe(5)
    expect(needsMeTotal(0, 0)).toBe(0)
    expect(needsMeTotal(4, 0)).toBe(4)
  })

  it('ignores negative and non-finite values', () => {
    expect(needsMeTotal(-3, 2)).toBe(2)
    expect(needsMeTotal(Number.NaN, 1)).toBe(1)
    expect(needsMeTotal(1.7, 2.9)).toBe(3)
  })
})

describe('command palette (design §6.5, P1 scope)', () => {
  const label = (key: string): string => key.replace('nav.', '')

  it('marks hidden command palette hits without removing them', () => {
    const hit = paletteHits('', undefined, label, ['nav.mail']).find((h) => h.to === '/mail')
    expect(hit?.hidden).toBe(true)
  })
  it('parses a p:<project> prefix and keeps the rest as keywords', () => {
    expect(parsePaletteQuery('p:my-tools')).toEqual({ project: 'my-tools', text: '' })
    expect(parsePaletteQuery('p:my-tools jobs')).toEqual({ project: 'my-tools', text: 'jobs' })
    expect(parsePaletteQuery(' P:abc  plans ')).toEqual({ project: 'abc', text: 'plans' })
    expect(parsePaletteQuery('jobs')).toEqual({ project: '', text: 'jobs' })
    expect(parsePaletteQuery('')).toEqual({ project: '', text: '' })
  })

  it('lists every page for an empty query', () => {
    expect(paletteHits('', undefined, label).length).toBeGreaterThan(10)
  })

  it('filters pages by keyword, case-insensitively', () => {
    expect(paletteHits('jobs', undefined, label).map((h) => h.to)).toEqual(['/jobs'])
    expect(paletteHits('WORK', undefined, label).map((h) => h.to)).toContain('/work')
  })

  it('keeps only project-filterable pages under p: and puts the project into the URL', () => {
    const hits = paletteHits('p:my-tools', undefined, label)
    expect(hits.length).toBeGreaterThan(0)
    expect(hits.every((h) => h.to.includes('project=my-tools'))).toBe(true)
    expect(hits.some((h) => h.to.startsWith('/settings'))).toBe(false)
    expect(hits.some((h) => h.to.startsWith('/jobs'))).toBe(true)
  })

  it('appends the project with the right separator and encodes it', () => {
    expect(withProject('/jobs', '')).toBe('/jobs')
    expect(withProject('/jobs', 'p1')).toBe('/jobs?project=p1')
    expect(withProject('/jobs?status=done', 'p1')).toBe('/jobs?status=done&project=p1')
    expect(withProject('/jobs', 'a b')).toBe('/jobs?project=a%20b')
  })

  it('finds 记忆 by its nav label and offers it under p:', () => {
    const zh = (key: string): string => dicts['zh-CN'][key] ?? key
    expect(paletteHits('记忆', undefined, zh).map((h) => h.to)).toContain('/memory')
    expect(paletteHits('p:my-tools', undefined, zh).map((h) => h.to)).toContain('/memory?project=my-tools')
  })
})
