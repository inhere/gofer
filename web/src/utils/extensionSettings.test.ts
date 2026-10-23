import { describe, expect, it } from 'vitest'
import type { SettingItem } from '../api/types'
import {
  boundsHint,
  controlFor,
  defaultText,
  dirty,
  draftOf,
  groupSettings,
  importActionLabel,
  importSummary,
  isPrompt,
  kindLabel,
  listText,
  parseList,
  PROMPT_EMPTY_HINT,
  utf8Len,
  validateDraft,
  valueOfDraft,
  valueText,
} from './extensionSettings'

const base = {
  description: '说明',
  default: '',
  value: '',
  modified: false,
} as const

function item(over: Partial<SettingItem>): SettingItem {
  return { ...(base as unknown as SettingItem), ...over } as SettingItem
}

const minPrompts = item({
  key: 'work.draft.min_prompts',
  group: 'work',
  kind: 'int',
  default: 2,
  value: 2,
  min: 1,
  max: 20,
})

const excludeProjects = item({
  key: 'work.draft.exclude_projects',
  group: 'work',
  kind: 'strings',
  default: [],
  value: ['web', 'docs'],
})

const titlePatterns = item({
  key: 'work.draft.exclude_title_patterns',
  group: 'work',
  kind: 'patterns',
  default: [],
  value: [],
})

const promptExtra = item({
  key: 'prompt.steward.role.extra',
  group: 'prompt',
  kind: 'string',
  multiline: true,
  max_len: 32768,
})

const shortMax = item({
  key: 'work.short_session.max_minutes',
  group: 'work',
  kind: 'int',
  default: 10,
  value: 10,
  min: 0,
  max: 1440,
})

describe('groupSettings', () => {
  it('groups by key prefix in registry order and labels the known groups', () => {
    const groups = groupSettings([minPrompts, excludeProjects, promptExtra, shortMax])
    expect(groups.map((g) => g.group)).toEqual(['work', 'prompt'])
    expect(groups[0].label).toBe('工作项降噪')
    expect(groups[1].label).toBe('提示词')
    expect(groups[0].items.map((i) => i.key)).toEqual([
      'work.draft.min_prompts',
      'work.draft.exclude_projects',
      'work.short_session.max_minutes',
    ])
  })

  it('falls back to the group name for a group the console does not know yet', () => {
    const groups = groupSettings([item({ key: 'mail.retention', group: 'mail', kind: 'int' })])
    expect(groups).toEqual([{ group: 'mail', label: 'mail', items: [expect.objectContaining({ key: 'mail.retention' })] }])
  })
})

describe('controlFor / kindLabel', () => {
  it('maps every registered kind to its editor', () => {
    expect(controlFor(minPrompts)).toBe('int')
    expect(controlFor(item({ key: 'x.on', kind: 'bool' }))).toBe('bool')
    expect(controlFor(excludeProjects)).toBe('list')
    expect(controlFor(titlePatterns)).toBe('list')
    expect(controlFor(promptExtra)).toBe('textarea')
    expect(controlFor(item({ key: 'x.name', kind: 'string' }))).toBe('text')
    expect(kindLabel('int')).toBe('整数')
    expect(kindLabel('patterns')).toBe('正则列表')
  })
})

describe('list parsing', () => {
  it('parses one entry per line, trimming and dropping blank lines', () => {
    expect(parseList(' web \n\n  docs\n')).toEqual(['web', 'docs'])
    expect(parseList('')).toEqual([])
    expect(parseList('  \n \n')).toEqual([])
    expect(listText(['web', 'docs'])).toBe('web\ndocs')
    expect(listText([])).toBe('')
  })

  it('round-trips a list value through the textarea', () => {
    expect(valueOfDraft(excludeProjects, listText(excludeProjects.value))).toEqual(['web', 'docs'])
  })
})

describe('drafts', () => {
  it('renders each control value and converts it back', () => {
    expect(draftOf(minPrompts)).toBe('2')
    expect(draftOf(excludeProjects)).toBe('web\ndocs')
    expect(draftOf(item({ key: 'x.on', kind: 'bool', default: true, value: true }))).toBe(true)
    expect(draftOf(promptExtra)).toBe('')
    expect(valueOfDraft(minPrompts, '7')).toBe(7)
    // 空 / 非数字交给校验拦，不能悄悄写成 0 以外的值。
    expect(valueOfDraft(minPrompts, 'abc')).toBe(0)
    expect(valueOfDraft(item({ key: 'x.on', kind: 'bool' }), false)).toBe(false)
    expect(valueOfDraft(promptExtra, '  两行\n文本 ')).toBe('  两行\n文本 ')
  })

  it('reports dirtiness against the effective value only', () => {
    expect(dirty(minPrompts, '2')).toBe(false)
    expect(dirty(minPrompts, '3')).toBe(true)
    expect(dirty(excludeProjects, 'web\ndocs')).toBe(false)
    expect(dirty(excludeProjects, 'web\n')).toBe(true)
    expect(dirty(promptExtra, '')).toBe(false)
  })
})

describe('value and default rendering', () => {
  it('shows empty values, booleans and the bounds', () => {
    expect(valueText([])).toBe('（空）')
    expect(valueText('')).toBe('（空）')
    expect(valueText(false)).toBe('关')
    expect(valueText(['a', 'b'])).toBe('a，b')
    expect(valueText(null)).toBe('（空）')
    expect(boundsHint(minPrompts)).toBe('范围 1–20')
    expect(boundsHint(promptExtra)).toBe('最多 32768 字节')
    expect(boundsHint(item({ key: 'x.n', kind: 'int', min: 0 }))).toBe('≥ 0')
    expect(boundsHint(item({ key: 'x.n', kind: 'int' }))).toBe('整数')
    expect(boundsHint(excludeProjects)).toBe('')
  })

  it('spells out that a blank prompt means the built-in default', () => {
    expect(isPrompt(promptExtra)).toBe(true)
    expect(isPrompt(minPrompts)).toBe(false)
    expect(defaultText(promptExtra)).toBe(PROMPT_EMPTY_HINT())
    expect(defaultText(minPrompts)).toBe('2')
    expect(defaultText(item({ key: 'prompt.steward.review', kind: 'string', default: '全文' }))).toBe('全文')
  })
})

describe('validateDraft', () => {
  it('checks ints are integers inside the registered bounds', () => {
    expect(validateDraft(minPrompts, '3')).toBe('')
    expect(validateDraft(minPrompts, '')).toBe('要填整数。')
    expect(validateDraft(minPrompts, '2.5')).toBe('要填整数。')
    expect(validateDraft(minPrompts, '0')).toBe('不能小于 1。')
    expect(validateDraft(minPrompts, '21')).toBe('不能大于 20。')
    expect(validateDraft(shortMax, '0')).toBe('')
  })

  it('compiles every pattern line before saving', () => {
    expect(validateDraft(titlePatterns, '^试一试\n^demo-')).toBe('')
    expect(validateDraft(titlePatterns, '^ok\n([a-z')).toBe('正则「([a-z」无法编译。')
    // 普通字符串列表不按正则校验。
    expect(validateDraft(excludeProjects, '([a-z')).toBe('')
  })

  it('bounds a prompt by bytes, as the server does', () => {
    expect(utf8Len('汉')).toBe(3)
    expect(validateDraft(promptExtra, '汉'.repeat(10922))).toBe('')
    expect(validateDraft(promptExtra, '汉'.repeat(11000))).toMatch(/^太长：33000 字节，上限 32768 字节。$/)
  })
})

describe('import preview helpers', () => {
  it('labels the import actions and summarises the preview', () => {
    expect(importActionLabel('set')).toBe('改为')
    expect(importActionLabel('reset')).toBe('恢复默认')
    expect(importActionLabel('unchanged')).toBe('不变')
    expect(
      importSummary([
        { action: 'set' },
        { action: 'reset' },
        { action: 'unchanged' },
        { action: 'set', error: 'bad' },
      ]),
    ).toBe('修改 1 · 恢复默认 1 · 不变 1 · 拒绝 1')
    expect(importSummary([])).toBe('修改 0 · 恢复默认 0 · 不变 0')
  })
})
