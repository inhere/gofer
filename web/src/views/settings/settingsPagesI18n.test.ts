import { describe, expect, it } from 'vitest'
import { settings } from '../../i18n/pages/settings'
import { access } from '../../i18n/pages/access'

// 设置子页与接入页改用页面字典（设计稿 §9、§13）：页面里引用的每个 key 两本字典都要有，
// 两本字典的 key 集合必须相等，英文文案里不能残留汉字；分节与保存条用统一基元。
const raw = (mods: Record<string, unknown>): string => Object.values(mods)[0] as string
const sources = {
  'settings.': [
    raw(import.meta.glob('./SettingsLayout.vue', { eager: true, query: '?raw', import: 'default' })),
    raw(import.meta.glob('./Notifications.vue', { eager: true, query: '?raw', import: 'default' })),
    raw(import.meta.glob('./McpServers.vue', { eager: true, query: '?raw', import: 'default' })),
    raw(import.meta.glob('./About.vue', { eager: true, query: '?raw', import: 'default' })),
    raw(import.meta.glob('./WorkSettings.vue', { eager: true, query: '?raw', import: 'default' })),
    raw(import.meta.glob('./Rules.vue', { eager: true, query: '?raw', import: 'default' })),
    raw(import.meta.glob('./Permissions.vue', { eager: true, query: '?raw', import: 'default' })),
    raw(import.meta.glob('./Extensions.vue', { eager: true, query: '?raw', import: 'default' })),
  ],
  'access.': [raw(import.meta.glob('../Access.vue', { eager: true, query: '?raw', import: 'default' }))],
}
const dicts = { 'settings.': settings, 'access.': access }

describe('P5-d page dictionaries', () => {
  it('has a zh and an en entry for every key used by the pages', () => {
    for (const [prefix, files] of Object.entries(sources)) {
      const dict = dicts[prefix as keyof typeof dicts]
      const used = new Set<string>()
      for (const src of files) {
        for (const m of src.matchAll(new RegExp(`t\\(\\s*[\\\`'"](${prefix}[^'\\\`"]*)['\\\`"]`, 'g'))) {
          // 拼出来的 key（如 settings.about.phase.${phase}）没法在这里展开，
          // 靠下面的两本字典 key 集合相等 + 页面里用到的拼装前缀用例覆盖。
          if (m[1].includes('${')) continue
          used.add(m[1])
        }
      }
      expect(used.size, prefix).toBeGreaterThan(0)
      for (const key of used) {
        expect(dict.zh[key], `${key} 缺中文`).toBeTypeOf('string')
        expect(dict.en[key], `${key} 缺英文`).toBeTypeOf('string')
      }
    }
  })

  it('keeps the zh and en key sets identical', () => {
    for (const dict of [settings, access]) {
      expect(Object.keys(dict.en).sort()).toEqual(Object.keys(dict.zh).sort())
    }
  })

  it('has no Chinese left in the English dictionary', () => {
    for (const dict of [settings, access]) {
      for (const [key, text] of Object.entries(dict.en)) {
        expect(/[\u4e00-\u9fff]/.test(text), `${key}: ${text}`).toBe(false)
      }
    }
  })

  it('wires the settings pages through the shared FormSection and SaveBar', () => {
    const notifications = sources['settings.'][1]
    const mcp = sources['settings.'][2]
    const about = sources['settings.'][3]
    const work = sources['settings.'][4]
    const rules = sources['settings.'][5]
    for (const src of [notifications, mcp, about, work, rules]) {
      expect(src).toContain("import FormSection from '../../ui/FormSection.vue'")
      expect(src).toContain('<FormSection')
    }
    // 有未保存修改的页面用吸底保存条（工作项设置是最典型的表单页）。
    expect(work).toContain("import SaveBar from '../../ui/SaveBar.vue'")
    expect(work).toContain('<SaveBar')
    expect(work).toContain(':dirty="dirty"')
  })

  it('leaves no hard-coded Chinese labels in the migrated templates', () => {
    // 抽取过的几页模板里不该再出现中文字面量（data-test / placeholder 里的命令除外）。
    for (const src of [sources['settings.'][1], sources['access.'][0]]) {
      const template = src.slice(src.indexOf('<template>'))
      const chinese = template.match(/>[^<>{}]*[\u4e00-\u9fff][^<>{}]*</g) ?? []
      expect(chinese, chinese.join(' | ')).toEqual([])
    }
  })
})
