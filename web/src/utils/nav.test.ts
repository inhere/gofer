// 顶栏导航的结构与「更多」激活态（纯逻辑，utils/nav.ts）。
// 渲染侧的接线（App.vue / NavMore.vue / MailButton.vue）在 components/navTopbar.test.ts。
import { describe, expect, it } from 'vitest'
import { drawerNavGroups, isNavMoreActive, navMoreGroups, navPrimary } from './nav'

describe('top nav structure', () => {
  it('keeps the six daily items on the row, in order', () => {
    expect(navPrimary.map((i) => i.label)).toEqual(['今天', '工作台', '工作项', '计划', '任务', '会话'])
    expect(navPrimary.find((i) => i.label === '任务')?.to).toBe('/board')
  })

  it('puts the rest under 更多 with their group titles', () => {
    expect(navMoreGroups.map((g) => g.label)).toEqual(['工作', '配置'])
    expect(navMoreGroups[0].items.map((i) => i.label)).toEqual(['Issues', '统计'])
    expect(navMoreGroups[1].items.map((i) => i.label)).toEqual(['Agents', 'Runners', '项目', '工作流', '定时'])
  })

  it('keeps 信箱 out of the row and out of the 更多 menu (it is a topbar icon button)', () => {
    expect(navPrimary.some((i) => i.to === '/mail')).toBe(false)
    expect(navMoreGroups.some((g) => g.items.some((i) => i.to === '/mail'))).toBe(false)
  })

  it('gives the narrow-screen drawer the full grouped list, nav order kept', () => {
    expect(drawerNavGroups.map((g) => g.label)).toEqual(['工作', '配置'])
    expect(drawerNavGroups[0].items.map((i) => i.label)).toEqual(['今天', '工作台', '工作项', '计划', '任务', '会话', '信箱', 'Issues', '统计'])
    expect(drawerNavGroups[0].items.slice(0, navPrimary.length)).toEqual(navPrimary)
    expect(drawerNavGroups[1].items).toEqual(navMoreGroups[1].items)
  })
})

describe('更多 active state', () => {
  it('lights up for any page inside the menu, subpages included', () => {
    expect(isNavMoreActive('/issues')).toBe(true)
    expect(isNavMoreActive('/dashboard')).toBe(true)
    expect(isNavMoreActive('/agents')).toBe(true)
    expect(isNavMoreActive('/agents/local')).toBe(true)
    expect(isNavMoreActive('/schedules/12/edit')).toBe(true)
  })

  it('stays off for the row, for 信箱, for 设置 and for a look-alike prefix', () => {
    expect(isNavMoreActive('/today')).toBe(false)
    expect(isNavMoreActive('/workbench')).toBe(false)
    expect(isNavMoreActive('/work')).toBe(false)
    expect(isNavMoreActive('/board')).toBe(false)
    expect(isNavMoreActive('/mail')).toBe(false)
    expect(isNavMoreActive('/settings')).toBe(false)
    expect(isNavMoreActive('/agentsx')).toBe(false)
    expect(isNavMoreActive('/workflows-old')).toBe(false)
  })
})
