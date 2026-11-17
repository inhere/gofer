// 统一交互卡片（R11）：多选、其他、推荐项默认选中、只读态与「交给它决定」插槽。
// vitest 不处理 css / 没有 test-utils，用 SSR 渲染看结构、用 utils 的纯函数看提交值。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { Interaction } from '../api/types'
import InteractionCard from './InteractionCard.vue'

const choice: Interaction = {
  id: 'i1',
  job_id: 'j1',
  type: 'choice',
  prompt: '用哪种数据库？',
  options: [
    { value: 'pg', label: 'PostgreSQL', description: '已经有现成实例' },
    { value: 'sqlite', label: 'SQLite' },
    { value: 'mysql', label: 'MySQL' },
  ],
  status: 'pending',
  created_at: 1_700_000_000,
}

async function render(
  props: { interaction: Interaction; otherLabel?: string; submitting?: boolean },
  slots?: Record<string, () => unknown>,
): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(InteractionCard, props, slots as never) }))
}

describe('InteractionCard choices', () => {
  it('keeps the click-to-answer buttons for a plain choice', async () => {
    const plain: Interaction = {
      ...choice,
      options: [
        { value: 'pg', label: 'PostgreSQL' },
        { value: 'sqlite', label: 'SQLite' },
      ],
    }
    const html = await render({ interaction: plain })
    expect(html).toContain('icard-choices')
    expect(html).not.toContain('data-test="icard-choice-list"')
    expect(html).toContain('PostgreSQL')
    expect(html).not.toContain('type="checkbox"')
  })

  it('renders checkboxes and one submit for a multi-select', async () => {
    const html = await render({ interaction: { ...choice, multi: true } })
    expect(html).toContain('data-test="icard-choice-list"')
    expect(html.match(/type="checkbox"/g)).toHaveLength(3)
    expect(html).toContain('data-test="icard-choice-submit"')
  })

  it('marks and preselects the recommended option', async () => {
    const html = await render({ interaction: { ...choice, recommended: 'SQLite' } })
    expect(html).toContain('推荐')
    // 推荐项是唯一勾上的那个（radio checked），且提交按钮可用
    const radios = html.match(/<input[^>]*type="radio"[^>]*>/g) ?? []
    expect(radios).toHaveLength(3)
    expect(radios.filter((r) => r.includes('checked'))).toHaveLength(1)
    expect(radios.find((r) => r.includes('checked'))).toContain('value="sqlite"')
    expect(html).toContain('data-test="icard-choice-submit"')
  })

  it('shows an option description under the label', async () => {
    const html = await render({ interaction: choice })
    expect(html).toContain('icard-opt-desc')
    expect(html).toContain('已经有现成实例')
  })

  it('offers a free-text row only with allow_other', async () => {
    const plain = await render({ interaction: choice })
    expect(plain).not.toContain('data-test="icard-other"')
    const other = await render({ interaction: { ...choice, multi: true, allow_other: true } })
    expect(other).toContain('data-test="icard-other"')
    expect(other).toContain('其他（自由填写）')
    const custom = await render({ interaction: { ...choice, allow_other: true }, otherLabel: '拒绝原因' })
    expect(custom).toContain('placeholder="拒绝原因"')
  })

  it('shows the final answer read-only once answered', async () => {
    const html = await render({
      interaction: { ...choice, multi: true, status: 'answered', answer: '["SQLite","PostgreSQL"]', answered_by: 'human' },
    })
    expect(html).toContain('data-test="icard-final-answer"')
    expect(html).toContain('SQLite, PostgreSQL')
    expect(html).not.toContain('data-test="icard-choice-submit"')
  })

  it('shows expired and cancelled states without any answer control', async () => {
    const expired = await render({ interaction: { ...choice, status: 'expired' } })
    expect(expired).toContain('已过期')
    expect(expired).not.toContain('data-test="icard-choice-submit"')
    const cancelled = await render({ interaction: { ...choice, status: 'cancelled' } })
    expect(cancelled).toContain('data-test="icard-cancelled"')
    expect(cancelled).toContain('已取消')
    expect(cancelled).not.toContain('type="checkbox"')
  })

  it('carries a named delegate slot that stays empty by default', async () => {
    const without = await render({ interaction: choice })
    expect(without).not.toContain('icard-delegate')
    const withSlot = await render({ interaction: choice }, { delegate: () => h('button', { class: 'x' }, '交给它决定') })
    expect(withSlot).toContain('icard-delegate')
    expect(withSlot).toContain('交给它决定')
  })
})
