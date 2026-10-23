// P5-a（工作流 + 定时）的文案接线：页面里出现的每个 `t('workflows.…')` / `t('schedules.…')`
// 在 zh 与 en 两本页面字典里都要有，且两本的 key 集合相等（设计稿 §9.3）。
import { describe, expect, it } from 'vitest'
import { dicts } from '../i18n'
import { schedules } from '../i18n/pages/schedules'
import { workflows } from '../i18n/pages/workflows'

const sources = Object.values(
  import.meta.glob(
    ['./Workflows.vue', './NewWorkflow.vue', './WorkflowDetail.vue', './Schedules.vue', './NewSchedule.vue'],
    { eager: true, query: '?raw', import: 'default' },
  ),
) as string[]

const used = new Set<string>()
for (const src of sources) {
  for (const m of src.matchAll(/\bt\('((?:workflows|schedules)\.[A-Za-z0-9_.]+)'/g)) {
    used.add(m[1])
  }
}

describe('P5-a 页面文案', () => {
  it('页面用到的 key 都在 zh 与 en 字典里', () => {
    expect(used.size).toBeGreaterThan(50)
    for (const key of used) {
      expect(dicts['zh-CN'][key], `${key} 缺中文`).toBeTruthy()
      expect(dicts['en-US'][key], `${key} 缺英文`).toBeTruthy()
    }
  })

  it('两本字典的 key 集合相等（zh / en 同名同数）', () => {
    for (const page of [workflows, schedules]) {
      expect(Object.keys(page.zh).sort()).toEqual(Object.keys(page.en).sort())
    }
  })

  it('英文文案里没有汉字（状态名、字段名都是英文）', () => {
    const han = /[\u4e00-\u9fff]/
    for (const page of [workflows, schedules]) {
      for (const [key, text] of Object.entries(page.en)) {
        expect(han.test(text), `${key} 的英文里有汉字：${text}`).toBe(false)
      }
    }
  })
})
