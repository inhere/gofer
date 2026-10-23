import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Runners.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('Runners layout', () => {
  it('collapses topology by default on phone widths (design §4.3: ≤760)', () => {
    expect(source).toContain("window.matchMedia('(max-width: 760px)')")
    expect(source).toContain('topologyOpen.value = false')
    expect(source).toContain('.topology-group details[open]')
  })

  it('styles the add-worker form with the shared field/label/control classes', () => {
    expect(source).toContain('class="field"')
    expect(source).toContain('class="control"')
    expect(source).toContain('.add-form .control')
    expect(source).toContain('.add-actions')
  })

  it('shows every runner state through the status family glyphs, not ad-hoc dots', () => {
    // 设计稿 §5：状态字形 + 颜色一起编码；页面不再自己画圆点
    expect(source).toContain('<StatusGlyph')
    expect(source).toContain('<StatusPill')
    expect(source).not.toContain('static-dot')
    expect(source).toContain("family: 'done'")
    expect(source).toContain("family: 'idle'")
    expect(source).toContain("family: 'need'")
    expect(source).toContain("family: 'fail'")
  })
})
