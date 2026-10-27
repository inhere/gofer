// 关于页「不能一键更新」说明块（components/UpdateManualNotice.vue）。
//
// 两种情形都真渲染一遍：未托管时给出警示框、手动更新的命令与改成服务的四个步骤；
// 托管时整块不渲染（那时关于页显示的是一键更新按钮）。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import UpdateManualNotice from './UpdateManualNotice.vue'
import { settings } from '../i18n/pages/settings'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const source = raw(import.meta.glob('./UpdateManualNotice.vue', { eager: true, query: '?raw', import: 'default' }))
const about = raw(import.meta.glob('../views/settings/About.vue', { eager: true, query: '?raw', import: 'default' }))
const zh = settings.zh
const en = settings.en

const CJK = /[\u4e00-\u9fff]/

function render(managed: boolean): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(UpdateManualNotice, { managed }) }))
}

describe('UpdateManualNotice', () => {
  it('renders a warning box with the manual steps when the server is not managed', async () => {
    const html = await render(false)
    expect(html).toContain('data-testid="update-manual"')
    // 标题说清「为什么没有一键更新」
    expect(html).toContain(zh['settings.about.manual.title'])
    expect(zh['settings.about.manual.title']).toContain('不是以服务方式运行')
    // 段一：现在手动更新 + 两条命令
    expect(html).toContain(zh['settings.about.manual.now'])
    // 正文里的 <占位符> 在 HTML 里是转义后的样子
    expect(html).toContain('下载上面的二进制替换后重启 server；worker 用下面的命令升级')
    expect(html).toContain('其他平台加 --file &lt;该平台二进制&gt;')
    expect(html).toContain('gofer serve')
    expect(html).toContain('gofer worker upgrade &lt;id&gt;')
    // 段二：改成服务，四步都在，步骤里的命令等宽显示
    expect(html).toContain(zh['settings.about.manual.service'])
    for (const id of ['step1', 'step2', 'step3', 'step4']) {
      expect(html).toContain(`data-testid="update-manual-${id}"`)
      expect(html).toContain(zh[`settings.about.manual.${id}`])
    }
    expect(html).toContain('gofer serve register')
    expect(html).toContain('gofer serve start')
    expect(html).toContain('gofer serve status')
    expect(html).toContain('health=healthy')
    expect(html).toContain('<code')
    // 末尾指向 --help
    expect(html).toContain(zh['settings.about.manual.more'])
    // 每条命令一个复制按钮：手动更新 2 条 + 步骤 3 条
    expect(html.match(/data-testid="update-manual-copy"/g)?.length).toBe(5)
  })

  it('renders nothing when the server is a managed service', async () => {
    const html = await render(true)
    expect(html).not.toContain('data-testid="update-manual"')
    expect(html).not.toContain(zh['settings.about.manual.title'])
    expect(html.trim()).toBe('<!---->')
  })

  it('styles the box with the warning tokens instead of hardcoded colors', () => {
    expect(source).toContain('var(--need)')
    expect(source).toContain('var(--need-soft)')
    expect(source).toContain('var(--need-ink)')
    expect(source).toContain('border-left: 4px solid var(--need)')
    expect(source).toMatch(/@media \(max-width: 760px\)/)
    // 深浅两套主题都靠 token，组件里不写死颜色
    const style = source.slice(source.indexOf('<style'))
    expect(style).not.toMatch(/#[0-9a-fA-F]{3,8}\b/)
    expect(style).not.toMatch(/rgba?\(/)
    // 长命令在自己的框里横滚，不把手机上的 callout 撑宽
    expect(style).toContain('overflow-x: auto')
    expect(style).toContain('overflow-wrap: anywhere')
  })

  it('copies a command and says so when the clipboard is unavailable', () => {
    expect(source).toContain("import { copyText } from '../utils/sessionMessaging'")
    expect(source).toContain('copyText(cmd)')
    expect(source).toContain('@click="copy(item.cmd)"')
    expect(source).toContain('@click="copy(step.cmd)"')
    expect(source).toContain("t('settings.about.manual.copyFailed')")
  })

  it('is wired into the About page only for a server that is not managed', () => {
    expect(about).toContain("import UpdateManualNotice from '../../components/UpdateManualNotice.vue'")
    // 一键更新按钮与说明块互斥：按钮走 canApplyNow（受管服务），说明块走 showManualNotice
    // （有新版本、没在更新、有权限、但不是受管服务）。
    expect(about).toContain('const canApplyNow = computed(() => showApplyEntry.value && update.value?.managed === true)')
    expect(about).toContain('const showManualNotice = computed(() => showApplyEntry.value && update.value?.managed !== true)')
    expect(about).toContain('<UpdateManualNotice v-if="showManualNotice" :managed="update?.managed" />')
    // 旧的一行灰字提示已经换成这个组件
    expect(about).not.toContain("t('settings.about.manual')")
  })

  it('keeps the new keys translated in both dictionaries', () => {
    const keys = [
      'settings.about.manual.title',
      'settings.about.manual.now',
      'settings.about.manual.nowBody',
      'settings.about.manual.service',
      'settings.about.manual.step1',
      'settings.about.manual.step2',
      'settings.about.manual.step3',
      'settings.about.manual.step4',
      'settings.about.manual.more',
      'settings.about.manual.copy',
      'settings.about.manual.copied',
      'settings.about.manual.copyFailed',
    ]
    for (const key of keys) {
      expect(zh[key], `zh 缺 ${key}`).toBeTruthy()
      expect(en[key], `en 缺 ${key}`).toBeTruthy()
      // 一条文案里不中英混杂
      expect(CJK.test(en[key]), `en ${key} 里有中文`).toBe(false)
    }
    // 旧的单条长文案已经拆开，不再留着
    expect(zh['settings.about.manual']).toBeUndefined()
    expect(en['settings.about.manual']).toBeUndefined()
  })
})
