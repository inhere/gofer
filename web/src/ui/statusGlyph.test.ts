// StatusGlyph / StatusPill 的渲染测试（设计稿 §5、§13）。
// 用 SSR 渲染成字符串断言结构与无障碍属性，不断言 class 名字以外的样式细节。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import StatusGlyph from './StatusGlyph.vue'
import StatusPill from './StatusPill.vue'
import { GLYPHS } from './statusGlyph'
import { STATUS_FAMILIES, type StatusFamily } from '../utils/statusFamily'
import { dicts } from '../i18n'
import { locale } from '../store/locale'

async function render(comp: unknown, props: Record<string, unknown>): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(comp as any, props) }))
}

async function withLocale<T>(loc: 'zh-CN' | 'en-US', fn: () => Promise<T>): Promise<T> {
  const before = locale.value
  locale.value = loc
  try {
    return await fn()
  } finally {
    locale.value = before
  }
}

describe('glyph specs', () => {
  it('gives every family its own shape', () => {
    const kinds = STATUS_FAMILIES.map((fam) => GLYPHS[fam].kind)
    expect(new Set(kinds).size).toBe(STATUS_FAMILIES.length)
  })

  it('gives every family a label key that exists in both dictionaries', () => {
    for (const fam of STATUS_FAMILIES) {
      const key = GLYPHS[fam].labelKey
      expect(dicts['zh-CN'][key], `${key} 缺中文`).toBeTruthy()
      expect(dicts['en-US'][key], `${key} 缺英文`).toBeTruthy()
    }
  })
})

describe('StatusGlyph', () => {
  it('renders an accessible svg for every family', async () => {
    for (const fam of STATUS_FAMILIES) {
      const html = await render(StatusGlyph, { family: fam })
      expect(html).toContain('role="img"')
      expect(html).toContain(`data-family="${fam}"`)
      expect(html).toContain(`data-kind="${GLYPHS[fam].kind}"`)
      // 族的颜色钩子
      expect(html).toContain(`k-${fam}`)
      // 有可读的无障碍名称
      expect(html).toMatch(/aria-label="[^"]+"/)
    }
  })

  it('maps a status value to its family', async () => {
    expect(await render(StatusGlyph, { status: 'needs_review' })).toContain('data-family="need"')
    expect(await render(StatusGlyph, { status: 'timeout' })).toContain('data-family="time"')
    expect(await render(StatusGlyph, { status: 'queued' })).toContain('data-family="queue"')
    expect(await render(StatusGlyph, { status: 'unknown-value' })).toContain('data-family="idle"')
  })

  it('prefers an explicit family and label', async () => {
    const html = await render(StatusGlyph, {
      status: 'done',
      family: 'fail' as StatusFamily,
      label: '自定义',
    })
    expect(html).toContain('data-family="fail"')
    expect(html).toContain('aria-label="自定义"')
  })
})

describe('StatusPill', () => {
  it('shows the translated status text', async () => {
    await withLocale('en-US', async () => {
      const html = await render(StatusPill, { status: 'running' })
      expect(html).toContain('Running')
      expect(html).toContain('k-run')
    })
    await withLocale('zh-CN', async () => {
      const html = await render(StatusPill, { status: 'needs_review' })
      expect(html).toContain('待验收')
      expect(html).toContain('k-need')
    })
  })

  it('falls back to the legacy status text for values without an i18n key', async () => {
    await withLocale('zh-CN', async () => {
      // status.draft 还没进字典，旧映射表里有「草稿」
      const html = await render(StatusPill, { status: 'draft' })
      expect(html).toContain('草稿')
    })
  })

  it('can render the glyph only, and always keeps the label for screen readers', async () => {
    const html = await render(StatusPill, { status: 'queued', glyphOnly: true })
    expect(html).not.toContain('pill-text')
    expect(html).toContain('aria-label=')
  })

  it('honours a custom label', async () => {
    const html = await render(StatusPill, { status: 'open', label: '自定义文案' })
    expect(html).toContain('自定义文案')
  })
})
