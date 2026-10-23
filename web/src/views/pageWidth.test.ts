// P6 内容区宽度统一（issue gofer-hez1）：所有路由页面的根元素都用同一个页面容器
// `class="page"`（规则在 web/src/ui/page.css，宽度上限与页边距来自 tokens.css），
// 页面里不再各自写 max-width / margin 居中；工作台与信箱用 full 变体铺满整个内容区。
//
// 这个用例扫 views 下的页面源码：路由页面清单（router.routes.ts）里每个顶层页面都必须
// 挂上页面容器，且根元素自己的样式规则里不再出现页面级 max-width（px 数字）。
// 全文按 ?raw 读源码断言，口径与其它样式用例一致（不渲染，只看规则）。
import { describe, expect, it } from 'vitest'

const views = import.meta.glob('./**/*.vue', { eager: true, query: '?raw', import: 'default' }) as Record<
  string,
  string
>
const pageCss = import.meta.glob('../ui/page.css', { eager: true, query: '?raw', import: 'default' })[
  '../ui/page.css'
] as string
const appShell = import.meta.glob('../ui/AppShell.vue', { eager: true, query: '?raw', import: 'default' })[
  '../ui/AppShell.vue'
] as string
const routes = import.meta.glob('../router.routes.ts', { eager: true, query: '?raw', import: 'default' })[
  '../router.routes.ts'
] as string

/** 页面根元素的容器类：普通页面 `page`，工作台 / 信箱 `page page--full`。 */
const PAGE_ROOTS: Record<string, string> = {
  './Today.vue': 'page',
  './Dashboard.vue': 'page',
  './Workbench.vue': 'page page--full',
  './Board.vue': 'page',
  './ReviewQueue.vue': 'page',
  './Sessions.vue': 'page',
  './Mail.vue': 'page page--full',
  './Work.vue': 'page',
  './NewJob.vue': 'page',
  './JobsView.vue': 'page',
  './Workflows.vue': 'page',
  './NewWorkflow.vue': 'page',
  './WorkflowDetail.vue': 'page',
  './Plans.vue': 'page',
  './Issues.vue': 'page',
  './Memory.vue': 'page',
  './PlanDetail.vue': 'page',
  './Schedules.vue': 'page',
  './NewSchedule.vue': 'page',
  './Projects.vue': 'page',
  './Agents.vue': 'page',
  './Runners.vue': 'page',
  './settings/SettingsLayout.vue': 'page',
}

// 设置子页（/settings/*）的容器由 SettingsLayout 提供，自己不再包一层页面容器；
// 登录页（/access）是壳外的窄卡片（宽 420px），是本次唯一保留的页面级自定宽度。
const NESTED_OR_NARROW = [
  './Access.vue',
  './Config.vue',
  './Skills.vue',
  './settings/About.vue',
  './settings/Extensions.vue',
  './settings/McpServers.vue',
  './settings/Notifications.vue',
  './settings/Permissions.vue',
  './settings/Rules.vue',
  './settings/Tunnels.vue',
  './settings/WorkSettings.vue',
]

/** 取出 <template> 后的第一个开标签（页面根元素），多行属性也算。 */
function rootTag(src: string): string {
  const start = src.indexOf('<template>')
  expect(start, '没有找到 <template>').toBeGreaterThan(-1)
  const m = /<template>\s*(<[A-Za-z][\w-]*(?:"[^"]*"|'[^']*'|[^>"'])*>)/.exec(src.slice(start))
  expect(m, '没有找到根元素').not.toBeNull()
  return m![1]
}

/** 根元素静态 class 列表（不含 :class 动态绑定里的）。 */
function rootClasses(tag: string): string[] {
  const m = /(?<!:)class="([^"]*)"/.exec(tag)
  return m ? m[1].split(/\s+/).filter(Boolean) : []
}

/** 拆出样式块里的每条规则（@media 条件本身跳过，里面的规则照收）。 */
function cssRules(css: string): Array<{ selector: string; body: string }> {
  const rules: Array<{ selector: string; body: string }> = []
  const stack: string[] = []
  let buf = ''
  for (const ch of css) {
    if (ch === '{') {
      stack.push(buf.trim())
      buf = ''
    } else if (ch === '}') {
      const selector = stack.pop() ?? ''
      if (selector && !selector.startsWith('@')) rules.push({ selector, body: buf })
      buf = ''
    } else {
      buf += ch
    }
  }
  return rules
}

const routed = [...routes.matchAll(/import\('\.\/(views\/[^']+\.vue)'\)/g)].map((m) => m[1].replace(/^views\//, './'))

describe('页面宽度统一：视图根元素', () => {
  it('路由页面清单与测试里的映射一致（新增页面要登记页面容器）', () => {
    const known = new Set([...Object.keys(PAGE_ROOTS), ...NESTED_OR_NARROW])
    const missing = routed.filter((f) => !known.has(f))
    expect(missing, `未登记页面容器的路由页面：${missing.join(', ')}`).toEqual([])
  })

  it('每个路由页面根元素都带 page（工作台 / 信箱带 page--full）', () => {
    for (const [file, classes] of Object.entries(PAGE_ROOTS)) {
      const tag = rootTag(views[file])
      for (const cls of classes.split(' ')) {
        expect(new RegExp(`\\b${cls}\\b`).test(tag), `${file} 根元素缺少 ${cls}：${tag}`).toBe(true)
      }
    }
  })

  it('工作台与信箱用 full 变体铺满，不受页面宽度上限约束', () => {
    expect(rootClasses(rootTag(views['./Workbench.vue']))).toContain('page--full')
    expect(rootClasses(rootTag(views['./Mail.vue']))).toContain('page--full')
    // 其它页面不允许用 full 变体
    for (const [file, classes] of Object.entries(PAGE_ROOTS)) {
      if (file === './Workbench.vue' || file === './Mail.vue') continue
      expect(classes.split(' '), `${file} 不应铺满`).not.toContain('page--full')
    }
  })

  it('页面根元素自己的规则里不再写页面级 max-width', () => {
    for (const file of Object.keys(PAGE_ROOTS)) {
      const src = views[file]
      const cls = rootClasses(rootTag(src)).filter((c) => c !== 'page' && c !== 'page--full')
      const css = src.slice(src.indexOf('<style'))
      if (cls.length === 0) {
        // 根元素只有页面容器类：页面里不允许再定义 .page（样式只在 ui/page.css 一处）
        expect(cssRules(css).map((r) => r.selector), `${file} 里又写了一份 .page`).not.toContain('.page')
        continue
      }
      const rules = cssRules(css).filter((r) =>
        r.selector.split(',').some((s) => cls.some((c) => s.trim() === `.${c}`)),
      )
      for (const r of rules) {
        expect(r.body, `${file} 的 ${r.selector} 仍有页面级 max-width`).not.toMatch(/max-width:\s*\d+px/)
      }
    }
  })

  it('页面容器的宽度上限与页边距只由 tokens + page.css 提供，外壳不再叠加内边距', () => {
    expect(pageCss).toMatch(/\.page \{[\s\S]*?max-width: var\(--page-max\);[\s\S]*?margin-inline: auto;/)
    expect(pageCss).toMatch(/\.page--full \{[\s\S]*?max-width: none;/)
    const content = /\.content \{[\s\S]*?\}/.exec(appShell)![0]
    expect(content).toContain('padding: 0;')
  })
})
