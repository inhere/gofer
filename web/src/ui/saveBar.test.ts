// SaveBar（设计稿 §7.4）：有未保存修改时吸附在底部并给出「放弃修改 / 保存」；
// 没有修改时停在文档里（不吸附），两个按钮都禁用。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import SaveBar from './SaveBar.vue'

function render(props: { dirty: boolean; busy?: boolean; saveText?: string; unsavedText?: string }): Promise<string> {
  const app = createSSRApp({ render: () => h(SaveBar, props) })
  return renderToString(app)
}

// 属性顺序在 SSR 里是 disabled 在 data-testid 之前，两个方向都允许。
const disabled = (html: string, testid: string): boolean =>
  new RegExp(`<button[^>]*data-testid="${testid}"[^>]*disabled`).test(html) ||
  new RegExp(`<button[^>]*disabled[^>]*data-testid="${testid}"`).test(html)

describe('SaveBar', () => {
  it('sticks to the bottom with a hint and both actions while there are unsaved changes', async () => {
    const html = await render({ dirty: true })
    expect(html).toContain('data-testid="save-bar"')
    expect(html).not.toContain('save-bar--idle')
    expect(html).toContain('data-testid="save-bar-unsaved"')
    expect(html).toContain('有未保存的修改')
    expect(disabled(html, 'save-bar-discard')).toBe(false)
    expect(disabled(html, 'save-bar-save')).toBe(false)
  })

  it('goes back into the flow and disables both actions when nothing changed', async () => {
    const html = await render({ dirty: false })
    expect(html).toContain('save-bar--idle')
    expect(html).not.toContain('data-testid="save-bar-unsaved"')
    expect(disabled(html, 'save-bar-save')).toBe(true)
    expect(disabled(html, 'save-bar-discard')).toBe(true)
  })

  it('lets the page override the hint and the save label, and shows the busy label', async () => {
    const custom = await render({ dirty: true, saveText: '保存管家设置', unsavedText: '待写入' })
    expect(custom).toContain('保存管家设置')
    expect(custom).toContain('待写入')

    const busy = await render({ dirty: true, busy: true })
    expect(busy).toContain('保存中…')
    expect(disabled(busy, 'save-bar-save')).toBe(true)
  })

  it('translates its own labels', async () => {
    const { locale } = await import('../store/locale')
    const before = locale.value
    try {
      locale.value = 'en-US'
      const html = await render({ dirty: true })
      expect(html).toContain('Unsaved changes')
      expect(html).toContain('Discard')
      expect(html).toContain('Save')
    } finally {
      locale.value = before
    }
  })
})
