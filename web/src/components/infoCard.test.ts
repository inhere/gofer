import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import InfoCard from './InfoCard.vue'

async function render(props: Record<string, unknown>, withDetails = true): Promise<string> {
  const slots: Record<string, () => unknown> = {
    title: () => '修登录页',
    badges: () => h('span', { class: 'sbadge' }, '执行中'),
    meta: () => 'claude · self · 3m',
    actions: () => h('button', { 'data-test': 'main-action' }, '打开'),
  }
  if (withDetails) slots.details = () => h('dl', { class: 'icard-kv' }, [h('dt', 'runner'), h('dd', 'server')])
  return renderToString(createSSRApp({ render: () => h(InfoCard, props, slots) }))
}

describe('InfoCard (shared by Sessions and Work)', () => {
  it('collapsed: shows only the key info and a "详情" toggle, details stay out of the DOM', async () => {
    const html = await render({ tone: 'live', expanded: false })
    expect(html).toContain('修登录页')
    expect(html).toContain('执行中')
    expect(html).toContain('claude · self · 3m')
    expect(html).toContain('data-test="main-action"')
    expect(html).toMatch(/>详情<span class="icard-caret"/)
    expect(html).toContain('aria-expanded="false"')
    expect(html).not.toContain('data-test="card-details"')
    expect(html).not.toContain('runner')
    expect(html).toContain('icard--live')
  })

  it('expanded: renders the details block and flips the toggle to "收起"', async () => {
    const html = await render({ expanded: true })
    expect(html).toContain('data-test="card-details"')
    expect(html).toContain('<dt>runner</dt>')
    expect(html).toMatch(/>收起<span class="[^"]*icard-caret--up/)
    expect(html).toContain('aria-expanded="true"')
    expect(html).toContain('icard--expanded')
  })

  it('has no toggle when there is nothing to expand, and can be made non-expandable', async () => {
    expect(await render({}, false)).not.toContain('card-toggle')
    expect(await render({ expandable: false })).not.toContain('card-toggle')
  })

  it('makes the title a button only when it opens something, and dims without touching actions', async () => {
    const plain = await render({})
    expect(plain).not.toContain('role="button"')
    const open = await render({ openable: true, dim: true, active: true })
    expect(open).toContain('role="button"')
    expect(open).toContain('tabindex="0"')
    expect(open).toContain('icard--dim')
    expect(open).toContain('icard--active')
  })
})

describe('InfoCard source contract', () => {
  const src = Object.values(import.meta.glob('./InfoCard.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
  // 公共样式在 InfoCard.vue 末尾的非 scoped <style> 里（vitest 不处理 css 文件，直接读 SFC 源码）
  const css = src.slice(src.indexOf('<style>'))
  it('does not depend on the router (kept presentational) and carries the shared styles itself', () => {
    expect(src).not.toContain('vue-router')
    expect(css).toContain('.icard-grid')
    expect(css.startsWith('<style>')).toBe(true) // non-scoped: shared by every page that uses a card
  })
  it('lays cards out in one column on a phone and several on a desktop', () => {
    expect(css).toContain('repeat(auto-fill, minmax(330px, 1fr))')
    expect(css).toMatch(/@media \(max-width: 640px\) \{\s*\.icard-grid \{\s*grid-template-columns: minmax\(0, 1fr\);/)
  })
  it('keeps the actions undimmed on a dimmed card', () => {
    expect(css).toContain('.icard--dim > .icard-head')
    expect(css).not.toContain('.icard--dim > .icard-actions')
  })
})
