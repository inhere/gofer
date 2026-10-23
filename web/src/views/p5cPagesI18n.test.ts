import { describe, expect, it } from 'vitest'
import { agents } from '../i18n/pages/agents'
import { projects } from '../i18n/pages/projects'
import { runners } from '../i18n/pages/runners'

// P5-c 的三页（运行节点 / Agents / 项目）改用页面字典：页面里引用的每个 key 两本字典都要有，
// 两本字典的 key 集合必须相等，英文文案里不能残留汉字（设计稿 §9、§13）。
const raw = (mods: Record<string, unknown>) => Object.values(mods)[0] as string
const sources = {
  'runners.': [raw(import.meta.glob('./Runners.vue', { eager: true, query: '?raw', import: 'default' }))],
  'agents.': [raw(import.meta.glob('./Agents.vue', { eager: true, query: '?raw', import: 'default' }))],
  'projects.': [raw(import.meta.glob('./Projects.vue', { eager: true, query: '?raw', import: 'default' }))],
}
const dicts = { 'runners.': runners, 'agents.': agents, 'projects.': projects }
const all = [runners, agents, projects]

describe('P5-c page dictionaries', () => {
  it('has a zh and an en entry for every key used by the pages', () => {
    for (const [prefix, files] of Object.entries(sources)) {
      const dict = dicts[prefix as keyof typeof dicts]
      const used = new Set<string>()
      for (const src of files) {
        for (const m of src.matchAll(new RegExp(`t\\(\\s*[\\\`'"](${prefix}[^'\\\`"]*)['\\\`"]`, 'g'))) {
          // 拼出来的 key（如 runners.enroll.${spec.key}）没法在这里展开，靠下面的 key 集合用例。
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
    for (const dict of all) {
      expect(Object.keys(dict.en).sort()).toEqual(Object.keys(dict.zh).sort())
    }
  })

  it('has no Chinese left in the English dictionary', () => {
    for (const dict of all) {
      for (const [key, text] of Object.entries(dict.en)) {
        expect(/[\u4e00-\u9fff]/.test(text), `${key}: ${text}`).toBe(false)
      }
    }
  })
})
