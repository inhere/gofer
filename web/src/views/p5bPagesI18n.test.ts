import { describe, expect, it } from 'vitest'
import { issues } from '../i18n/pages/issues'
import { stats } from '../i18n/pages/stats'
import { newjob } from '../i18n/pages/newjob'

// P5-b 的三页（Issues / 统计 / 新建任务）改用页面字典：页面里引用的每个 key 两本字典都要有，
// 两本字典的 key 集合必须相等，英文文案里不能残留汉字（设计稿 §9、§13）。
const raw = (mods: Record<string, unknown>) => Object.values(mods)[0] as string
const sources = {
  'issues.': [
    raw(import.meta.glob('./Issues.vue', { eager: true, query: '?raw', import: 'default' })),
    raw(import.meta.glob('../components/IssueDrawer.vue', { eager: true, query: '?raw', import: 'default' })),
  ],
  'stats.': [
    raw(import.meta.glob('./Dashboard.vue', { eager: true, query: '?raw', import: 'default' })),
    raw(import.meta.glob('../components/DashboardJobStatus.vue', { eager: true, query: '?raw', import: 'default' })),
    raw(import.meta.glob('../components/DashboardSystem.vue', { eager: true, query: '?raw', import: 'default' })),
  ],
  'newjob.': [raw(import.meta.glob('./NewJob.vue', { eager: true, query: '?raw', import: 'default' }))],
}
const dicts = { 'issues.': issues, 'stats.': stats, 'newjob.': newjob }

describe('P5-b page dictionaries', () => {
  it('has a zh and an en entry for every key used by the pages', () => {
    for (const [prefix, files] of Object.entries(sources)) {
      const dict = dicts[prefix as keyof typeof dicts]
      const used = new Set<string>()
      for (const src of files) {
        for (const m of src.matchAll(new RegExp(`t\\(\\s*[\\\`'"](${prefix}[^'\\\`"]*)['\\\`"]`, 'g'))) {
          // 拼出来的 key（如 stats.sys.state.${state}）没法在这里展开，靠下面的 key 集合相等用例。
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
    for (const dict of [issues, stats, newjob]) {
      expect(Object.keys(dict.en).sort()).toEqual(Object.keys(dict.zh).sort())
    }
  })

  it('has no Chinese left in the English dictionary', () => {
    for (const dict of [issues, stats, newjob]) {
      for (const [key, text] of Object.entries(dict.en)) {
        expect(/[\u4e00-\u9fff]/.test(text), `${key}: ${text}`).toBe(false)
      }
    }
  })
})
