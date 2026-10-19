// 手机宽度（≤900px）下设置页里的表格不能横向溢出把页面撑破：规则库的表（7 列）放进可横向
// 滚动的容器；配置管理的调用方表（6 列）同样在 wrapper 里滚。
import { describe, expect, it } from 'vitest'
import { MOBILE_MAX_WIDTH } from '../../utils/mobileLayout'

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const rules = raw(import.meta.glob('./Rules.vue', { eager: true, query: '?raw', import: 'default' }))
const config = raw(import.meta.glob('../Config.vue', { eager: true, query: '?raw', import: 'default' }))
const layout = raw(import.meta.glob('./SettingsLayout.vue', { eager: true, query: '?raw', import: 'default' }))

describe('设置页表格在手机档的处理', () => {
  it('规则库的表包在可横向滚动的容器里', () => {
    expect(rules).toContain('<div class="table-wrap">')
    expect(rules).toContain('.table-wrap {')
    expect(rules).toMatch(/\.table-wrap \{[^}]*overflow-x: auto/)
    // 表格在容器里保持最小宽度，列不会被挤成一团。
    expect(rules).toMatch(/\.table-wrap \.table \{[^}]*min-width: \d+px/)
  })

  it('规则库的横向滚动在 900px 手机档生效', () => {
    expect(rules).toContain(`@media (max-width: ${MOBILE_MAX_WIDTH}px)`)
  })

  it('配置管理的调用方表在 900px 手机档有最小宽度（wrapper 已有 overflow-x: auto）', () => {
    expect(config).toMatch(/\.table-wrap \{\s*overflow-x: auto;/)
    const block = config.slice(config.indexOf(`@media (max-width: ${MOBILE_MAX_WIDTH}px)`))
    expect(block).toMatch(/\.table-wrap \.table \{[^}]*min-width: \d+px/)
  })

  it('设置区的导航条保持横向滚动（不改成换行堆叠）', () => {
    expect(layout).toMatch(/\.settings-nav \{[^}]*overflow-x: auto/)
  })
})
