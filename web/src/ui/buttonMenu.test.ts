// 统一按钮与下拉菜单（P6 基元）：Button 的三档语义 / 两档尺寸 / 灰显，
// MenuSelect 的触发器 aria 与占位文字，以及下拉的纯逻辑（当前值下标、键盘循环、跳过不可选项）。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import Button from './Button.vue'
import MenuSelect from './MenuSelect.vue'
import { firstEnabledIndex, menuIndex, nextEnabledIndex, NO_INDEX, stepIndex, type MenuOption } from './menuSelect'
import { BUTTON_SIZES, BUTTON_VARIANTS } from './button'

function render(component: unknown, props: Record<string, unknown>, slot = 'ok'): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(component as never, props, () => slot) }))
}

describe('button', () => {
  it('declares the three intents and two sizes', () => {
    expect(BUTTON_VARIANTS).toEqual(['primary', 'secondary', 'ghost'])
    expect(BUTTON_SIZES).toEqual(['sm', 'md'])
  })

  it('renders the intent and size as one class pair, with the text inside', async () => {
    expect(await render(Button, { variant: 'primary', size: 'sm' }, 'Go')).toContain('ubtn--primary')
    expect(await render(Button, { variant: 'ghost', size: 'md' }, 'Go')).toContain('ubtn--ghost')
    expect(await render(Button, { variant: 'secondary' }, 'Go')).toContain('ubtn--sm')
    expect(await render(Button, { size: 'md' }, 'Go')).toContain('ubtn--md')
    expect(await render(Button, { variant: 'primary' }, 'Open session')).toContain('Open session')
  })

  it('keeps a disabled button and its reason readable', async () => {
    const html = await render(Button, { disabled: true, title: 'read only' }, 'Save')
    expect(html).toContain('disabled')
    expect(html).toContain('read only')
  })

  it('fills the row only when asked to', async () => {
    expect(await render(Button, { block: true })).toContain('ubtn--block')
    expect(await render(Button, {})).not.toContain('ubtn--block')
  })
})

describe('menu select', () => {
  it('shows the placeholder in a closed trigger that announces a popup', async () => {
    const html = await render(MenuSelect, {
      modelValue: '',
      options: [{ value: 'done', label: 'Done' }],
      placeholder: 'Set status',
      label: 'Status',
    })
    expect(html).toContain('aria-haspopup="menu"')
    expect(html).toContain('aria-expanded="false"')
    expect(html).toContain('Set status')
    expect(html).toContain('aria-label="Status"')
    // 关闭时只有触发器，没有面板
    expect(html).not.toContain('role="menu"')
  })

  it('shows the current value on the trigger', async () => {
    const html = await render(MenuSelect, {
      modelValue: 'done',
      options: [{ value: 'active', label: 'Running' }, { value: 'done', label: 'Done' }],
      placeholder: 'Set status',
    })
    expect(html).toContain('Done')
    expect(html).not.toContain('Running')
  })
})

describe('menu logic', () => {
  const options: MenuOption[] = [
    { value: 'active', label: 'Running' },
    { value: 'done', label: 'Done', disabled: true },
    { value: 'dropped', label: 'Dropped' },
  ]

  it('finds the current value, or -1 when nothing matches', () => {
    expect(menuIndex(options, 'done')).toBe(1)
    expect(menuIndex(options, 'missing')).toBe(NO_INDEX)
    expect(menuIndex([], 'x')).toBe(NO_INDEX)
  })

  it('steps with wrap-around and starts at an end for the first arrow', () => {
    expect(stepIndex(0, 1, 3)).toBe(1)
    expect(stepIndex(2, 1, 3)).toBe(0)
    expect(stepIndex(0, -1, 3)).toBe(2)
    expect(stepIndex(-1, 1, 3)).toBe(0)
    expect(stepIndex(-1, -1, 3)).toBe(2)
    expect(stepIndex(0, 1, 0)).toBe(NO_INDEX)
  })

  it('skips disabled options in both directions', () => {
    expect(firstEnabledIndex([], 0)).toBe(NO_INDEX)
    expect(firstEnabledIndex([{ value: 'x', label: 'X', disabled: true }], 0)).toBe(NO_INDEX)
    expect(nextEnabledIndex(options, 0, 1)).toBe(2)
    expect(nextEnabledIndex(options, 2, 1)).toBe(0)
    expect(nextEnabledIndex(options, 2, -1)).toBe(0)
    expect(nextEnabledIndex(options, 0, -1)).toBe(2)
  })
})
