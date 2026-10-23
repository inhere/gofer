// i18n 的测试（设计稿 §9.1、§9.2、§13）：字典完整性、缺 key 回退、插值。
import { describe, expect, it } from 'vitest'
import { dicts, interpolate, t, te, translate } from './index'
import { en } from './en'
import { zh } from './zh'

describe('dictionaries', () => {
  it('keeps zh and en key sets identical', () => {
    const zhKeys = Object.keys(zh).sort()
    const enKeys = Object.keys(en).sort()
    // 两边少一个 key 都是漏翻译：这里给差异而不是只给长度
    expect(enKeys.filter((k) => !zhKeys.includes(k))).toEqual([])
    expect(zhKeys.filter((k) => !enKeys.includes(k))).toEqual([])
    expect(enKeys).toEqual(zhKeys)
  })

  it('has no empty values', () => {
    for (const [loc, dict] of Object.entries(dicts)) {
      for (const [key, value] of Object.entries(dict)) {
        expect(value.trim(), `${loc} 的 ${key} 是空的`).not.toBe('')
      }
    }
  })

  it('uses the frozen key naming scheme', () => {
    // 页面专属的 key 用 <page>.<area>.<name>（§9.2 的例子就是 plans.h.progress）
    const pageDomains = ['plans.']
    const domains = ['nav.', 'page.', 'col.', 'status.', 'act.', 'empty.', 'err.', 'toast.', 'confirm.', 'hint.', 'field.', 'help.', 'shell.', 'palette.', ...pageDomains]
    for (const key of Object.keys(zh)) {
      expect(domains.some((d) => key.startsWith(d)), `${key} 不在冻结的域里`).toBe(true)
    }
  })
})

describe('translate', () => {
  it('returns the requested locale', () => {
    expect(translate('nav.jobs', undefined, 'zh-CN')).toBe('任务')
    expect(translate('nav.jobs', undefined, 'en-US')).toBe('Jobs')
  })

  it('falls back to Chinese when the key is missing in the locale', () => {
    // dicts holds merged copies (main + i18n/pages), so the key goes into the merged
    // Chinese dictionary rather than into zh.ts's object.
    dicts['zh-CN']['test.onlyZh'] = '只有中文'
    try {
      expect(translate('test.onlyZh', undefined, 'en-US')).toBe('只有中文')
    } finally {
      delete dicts['zh-CN']['test.onlyZh']
    }
  })

  it('returns the key itself when nobody has it', () => {
    expect(translate('nope.missing', undefined, 'en-US')).toBe('nope.missing')
  })

  it('interpolates named placeholders', () => {
    expect(interpolate('{n} 个任务', { n: 3 })).toBe('3 个任务')
    expect(interpolate('a {x} b {y}', { x: 1 })).toBe('a 1 b {y}')
    expect(interpolate('no placeholder', { n: 1 })).toBe('no placeholder')
  })
})

describe('t / te with the current locale', () => {
  it('reads the current locale store', async () => {
    const { locale, setLocale } = await import('../store/locale')
    const before = locale.value
    try {
      setLocale('en-US')
      expect(t('act.save')).toBe('Save')
      expect(te('status.needs_review')).toBe(true)
      setLocale('zh-CN')
      expect(t('act.save')).toBe('保存')
    } finally {
      setLocale(before)
    }
  })

  it('te is false for unknown keys', () => {
    expect(te('nope.missing')).toBe(false)
  })
})
