// 关于页「更新」区的 UI：GitHub 栏（外链，地址只有一处常量）、「检查更新」与「一键更新」
// 同一行且主按钮在右、按钮宽度随文案（窄屏整组换行）、主按钮 hover 用主题 token。
//
// 项目里没有 DOM 测试环境（只有 SSR），所以：GitHub 栏用 SSR 渲染断言（它不依赖 /v1/update
// 的返回，取不到更新状态时也照常显示）；按钮顺序与样式用源码断言。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { settings as settingsDict } from '../../i18n/pages/settings'
import { REPO_URL } from '../../utils/repo'

// 页面 setup 时不会拉接口（onMounted 在 SSR 下不跑），但导入链里有 client，给个空实现即可。
vi.mock('../../api/client', () => ({
  getStats: vi.fn(async () => ({})),
  getUpdateStatus: vi.fn(async () => ({})),
  checkUpdate: vi.fn(async () => ({})),
  applyUpdate: vi.fn(async () => ({})),
  cancelUpdateWait: vi.fn(async () => undefined),
}))

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const aboutSrc = raw(import.meta.glob('./About.vue', { eager: true, query: '?raw', import: 'default' }))

const zh = settingsDict.zh
const en = settingsDict.en

// 取某条选择器的声明块（与 styles/msgBody.test.ts 同一读法）。
function rule(selector: string): string {
  const style = aboutSrc.slice(aboutSrc.indexOf('<style'))
  const at = style.indexOf(`${selector} {`)
  if (at < 0) throw new Error(`missing rule: ${selector}`)
  return style.slice(at, style.indexOf('}', at))
}

async function renderAbout(): Promise<string> {
  // 页面 setup 里读 window.location.origin（服务器地址那一栏）；node 下没有 window。
  vi.stubGlobal('window', { location: { origin: 'http://gofer.test:8765' } })
  const About = (await import('./About.vue')).default
  const app = createSSRApp(About)
  // useRouter() / <RouterLink> 由路由提供，这里单页渲染没有装路由；只关心静态结构，
  // 屏蔽这两条注入 / 组件解析警告，其余警告照旧输出。
  const warn = console.warn
  app.config.warnHandler = (msg) => {
    if (msg.includes('injection "Symbol(router)"') || msg.includes('Failed to resolve component: RouterLink')) return
    warn(msg)
  }
  return renderToString(app)
}

describe('关于页 · 更新区', () => {
  it('GitHub 栏渲染成指向仓库地址的外链', async () => {
    const html = await renderAbout()
    expect(html).toContain(zh['settings.about.github'])
    expect(html).toContain(`href="${REPO_URL}"`)
    expect(REPO_URL).toBe('https://github.com/inhere/gofer')
    // 新标签页打开，且不让被打开的页面拿到 window.opener。
    const at = html.indexOf(`href="${REPO_URL}"`)
    const tag = html.slice(html.lastIndexOf('<a ', at), html.indexOf('>', at) + 1)
    expect(tag).toContain('target="_blank"')
    expect(tag).toContain('rel="noopener noreferrer"')
    // 地址不写死在模板里：页面只引用常量（常量本身是唯一出处）。
    expect(aboutSrc).not.toContain('https://github.com/inhere/gofer')
    expect(aboutSrc).toContain("import { REPO_URL } from '../../utils/repo'")
    expect(aboutSrc).toContain(':href="REPO_URL"')
    // 两本字典都有这一栏的标签。
    expect(zh['settings.about.github']).toBeTypeOf('string')
    expect(en['settings.about.github']).toBeTypeOf('string')
  })

  it('「检查更新」与「一键更新」在同一行，主按钮在右', () => {
    const checkAt = aboutSrc.indexOf('@click="runCheck()"')
    const applyAt = aboutSrc.indexOf('data-testid="update-apply-open"')
    expect(checkAt).toBeGreaterThan(-1)
    expect(applyAt).toBeGreaterThan(-1)
    expect(checkAt).toBeLessThan(applyAt)
    // 同一个按钮组（<div class="actions"> 到它的 </div>）里，一键更新不再单独占一块。
    const groupAt = aboutSrc.lastIndexOf('<div class="actions">', checkAt)
    const group = aboutSrc.slice(groupAt, aboutSrc.indexOf('</div>', checkAt))
    expect(group).toContain('@click="runCheck()"')
    expect(group).toContain('data-testid="update-apply-open"')
    expect(aboutSrc).not.toContain('apply-entry')
    // 按钮组的间距与其它按钮组一致（8px）。
    expect(rule('.actions')).toMatch(/gap: 8px/)
    expect(rule('.actions')).toMatch(/flex-wrap: wrap/)
  })

  it('按钮宽度随内容：不被固定宽度或收缩挤掉版本号', () => {
    const btn = rule('.btn')
    expect(btn).toMatch(/flex: none/)
    expect(btn).toMatch(/white-space: nowrap/)
    expect(btn).not.toMatch(/(^|\n)\s*width:/)
    expect(btn).not.toMatch(/text-overflow/)
    // 一键更新的文案带版本号，且来自字典（中英都有 {version} 占位符）。
    expect(aboutSrc).toContain("t('settings.about.applyNow', { version: update?.latest ?? '' })")
    expect(zh['settings.about.applyNow']).toContain('{version}')
    expect(en['settings.about.applyNow']).toContain('{version}')
  })

  it('主按钮 hover 用主题 token：底色 / 描边 --accent-hover，前景 --accent-ink', () => {
    const hover = rule('.btn--primary:hover:not(:disabled)')
    expect(hover).toMatch(/background: var\(--accent-hover\)/)
    expect(hover).toMatch(/border-color: var\(--accent-hover\)/)
    expect(hover).toMatch(/color: var\(--accent-ink\)/)
    // 与同类主按钮（SaveBar / 接入页）同一套写法。
    expect(rule('.btn--primary')).toMatch(/background: var\(--accent\)/)
    expect(rule('.btn--primary')).toMatch(/color: var\(--accent-ink\)/)
    // 主按钮的 hover 必须写在 .btn:hover 之后才能盖住它的 color: var(--accent)
    // （否则前景与底色同色，hover 时文字看不见）。
    expect(aboutSrc.indexOf('.btn--primary:hover:not(:disabled)')).toBeGreaterThan(
      aboutSrc.indexOf('.btn:hover:not(:disabled)'),
    )
    expect(rule('.btn:hover:not(:disabled)')).toMatch(/color: var\(--accent\)/)
  })
})
