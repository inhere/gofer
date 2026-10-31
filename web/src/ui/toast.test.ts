// Toast / Switch 的渲染与接线测试（设计稿 §13）。项目的 vitest 没有 DOM，
// 渲染用 SSR 成字符串断言结构与无障碍属性；事件接线（点击 → emit、计时）断言源码。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import Toast from './Toast.vue'
import Switch from './Switch.vue'
import { toastDuration } from './toast'
import { locale } from '../store/locale'

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const toastSrc = raw(import.meta.glob('./Toast.vue', { eager: true, query: '?raw', import: 'default' }))
const switchSrc = raw(import.meta.glob('./Switch.vue', { eager: true, query: '?raw', import: 'default' }))
const undoSrc = raw(import.meta.glob('../components/today/UndoToast.vue', { eager: true, query: '?raw', import: 'default' }))
const needsMeSrc = raw(import.meta.glob('./NeedsMeButton.vue', { eager: true, query: '?raw', import: 'default' }))
const agentsSrc = raw(import.meta.glob('../views/Agents.vue', { eager: true, query: '?raw', import: 'default' }))
const extSrc = raw(import.meta.glob('../views/settings/Extensions.vue', { eager: true, query: '?raw', import: 'default' }))

function render(comp: unknown, props: Record<string, unknown>, slot?: string): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(comp as any, props, slot ? { default: () => slot } : undefined) }))
}

describe('Toast', () => {
  it('is a polite status region with title and text', async () => {
    const html = await render(Toast, { title: '标题', text: '正文' })
    expect(html).toContain('role="status"')
    expect(html).toContain('aria-live="polite"')
    expect(html).toContain('标题')
    expect(html).toContain('正文')
    expect(html).toContain('toast--bottom')
    expect(html).toContain('toast--neutral')
  })

  it('shows the undo-style action button and the counter only when asked', async () => {
    const plain = await render(Toast, { text: 'x' })
    expect(plain).not.toContain('data-testid="toast-action"')
    expect(plain).not.toContain('data-testid="toast-meta"')
    const html = await render(Toast, { text: 'x', actionLabel: '撤销 z', meta: '5s' })
    expect(html).toContain('data-testid="toast-action"')
    expect(html).toContain('撤销 z')
    expect(html).toContain('>5s<')
  })

  it('renders a labelled close button only when closable', async () => {
    locale.value = 'zh-CN'
    expect(await render(Toast, { text: 'x' })).not.toContain('toast-close')
    const html = await render(Toast, { text: 'x', closable: true })
    expect(html).toContain('data-testid="toast-close"')
    expect(html).toContain('aria-label="关闭提示"')
  })

  it('makes the body a button only with bodyAction, and passes the placement and tone through', async () => {
    const div = await render(Toast, { text: 'x' })
    expect(div).toMatch(/<div[^>]*class="body"/)
    const btn = await render(Toast, { text: 'x', bodyAction: true, placement: 'corner', tone: 'danger' })
    expect(btn).toMatch(/<button[^>]*class="body"[^>]*type="button"/)
    expect(btn).toContain('toast--corner')
    expect(btn).toContain('toast--danger')
  })

  it('wires clicks to emits and restarts the timer when the content changes', () => {
    expect(toastSrc).toContain("emit('close')")
    expect(toastSrc).toContain("emit('action')")
    expect(toastSrc).toContain("emit('body')")
    expect(toastSrc).toContain('window.setTimeout')
    expect(toastSrc).toContain('onUnmounted(clearTimer)')
    expect(toastSrc).toContain('watch(() => [props.title, props.text, props.duration], schedule)')
  })

  it('keeps the phone placement: corner toast moves under the top bar, one line, dismissible', () => {
    expect(toastSrc).toContain('@media (max-width: 760px)')
    expect(toastSrc).toMatch(/@media \(max-width: 760px\) \{[\s\S]*\.toast--corner[\s\S]*top:/)
    expect(toastSrc).toMatch(/@media \(max-width: 760px\) \{[\s\S]*\.toast--corner[\s\S]*bottom:\s*auto/)
    expect(toastSrc).toContain("t('util.toast.close')")
  })

  it('has no auto-dismiss outside a browser (SSR counts as wide)', () => {
    expect(toastDuration()).toBe(0)
  })
})

describe('Toast callers', () => {
  it('UndoToast wires the undo queue into the shared Toast and keeps the data-test hook', () => {
    expect(undoSrc).toContain("import Toast from '../../ui/Toast.vue'")
    expect(undoSrc).toContain('@action="undoQueue.undo()"')
    expect(undoSrc).toContain('@action="clearError"')
    expect(undoSrc).toContain('data-test="undo-toast"')
    expect(undoSrc).toContain(':meta="`${current.left}s`"')
  })

  it('the needs-me and agent-probe prompts use Toast, navigate on body click and auto-dismiss on phones', () => {
    for (const src of [needsMeSrc, agentsSrc]) {
      expect(src).toContain('<Toast')
      expect(src).toContain('body-action')
      expect(src).toContain(':duration="toastDuration()"')
      expect(src).toContain('router.push(to)')
      expect(src).not.toContain('InteractionToast')
    }
  })
})

describe('Switch', () => {
  it('is role=switch with aria-checked following the model', async () => {
    const off = await render(Switch, { modelValue: false })
    expect(off).toContain('role="switch"')
    expect(off).toContain('aria-checked="false"')
    expect(off).not.toContain('sw--on')
    const on = await render(Switch, { modelValue: true })
    expect(on).toContain('aria-checked="true"')
    expect(on).toContain('sw--on')
  })

  it('renders the slot as the visible label, or an aria-label when there is none', async () => {
    const withText = await render(Switch, { modelValue: true }, '启用')
    expect(withText).toContain('启用')
    expect(withText).not.toContain('aria-label=')
    const bare = await render(Switch, { modelValue: true, label: '启用' })
    expect(bare).toContain('aria-label="启用"')
    expect(bare).not.toContain('class="text"')
  })

  it('is a native button so Space / Enter work, and disabled blocks both click and keys', async () => {
    const html = await render(Switch, { modelValue: false, disabled: true })
    expect(html).toMatch(/<button[^>]*type="button"/)
    expect(html).toContain('disabled')
    expect(switchSrc).toContain("if (props.disabled) return")
    expect(switchSrc).toContain("emit('update:modelValue', !props.modelValue)")
  })

  it('replaced the hand-written switches on the extension settings page', () => {
    expect(extSrc).toContain("import Switch from '../../ui/Switch.vue'")
    expect(extSrc).toContain("@update:model-value=\"setDraft(it.key, $event)\"")
    expect(extSrc).toContain('@update:model-value="toggleNav(item.key)"')
    expect(extSrc).not.toContain('class="switch"')
    expect(extSrc).not.toContain('type="checkbox"')
  })
})
