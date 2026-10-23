// 仓库选择器（P6，issue gofer-hez1）：下拉里用「项目 key / 仓库目录名」区分仓库，
// 选中后的信息条显示完整路径（换行、不截断中间）并可复制。
// SSR 只跑到 setup，所以这里断言渲染结果与分组规则；点击后的取值由 httpapi 的接口行为覆盖不到，
// 用源码断言确认复制走的是统一的 copyText 与路径本身。
import { afterEach, describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import TrackerRepoPicker from './TrackerRepoPicker.vue'
import TrackerRepoPath from './TrackerRepoPath.vue'
import { setLocale } from '../store/locale'

const SRC = import.meta.glob('./{TrackerRepoPicker,TrackerRepoPath}.vue', { eager: true, query: '?raw', import: 'default' }) as Record<string, string>
const PathSource = SRC['./TrackerRepoPath.vue'] ?? ''

const REPOS = [
  { tracker_id: 'tracker-bbbb2222', project_key: 'alpha', rel_path: 'D:/work/alpha-api', prefix: 'api' },
  { tracker_id: 'tracker-aaaa1111', project_key: 'alpha', rel_path: 'D:/work/alpha-web', prefix: 'web' },
  { tracker_id: 'tracker-cccc3333', project_key: '', rel_path: 'E:/loose/repo', prefix: '' },
]

async function render(comp: unknown, props: Record<string, unknown>): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(comp as never, props as never) }))
}

afterEach(() => setLocale('zh-CN'))

describe('仓库下拉（TrackerRepoPicker）', () => {
  it('shows the selected repo as project / directory in the chip', async () => {
    setLocale('zh-CN')
    const html = await render(TrackerRepoPicker, { modelValue: 'tracker-aaaa1111', repos: REPOS })
    expect(html).toContain('data-test="tracker-repo-picker"')
    expect(html).toContain('alpha / alpha-web')
    // chip 的 title 带完整信息（项目 · 前缀 · 路径），hover 能看到
    expect(html).toContain('title="alpha · web · D:/work/alpha-web"')
  })

  it('keeps two repositories of the same project apart by their directory name', async () => {
    setLocale('zh-CN')
    const html = await render(TrackerRepoPicker, { modelValue: '', repos: REPOS })
    // 主文字：项目 / 目录
    expect(html).toContain('alpha / alpha-api')
    expect(html).toContain('alpha / alpha-web')
    // 次要文字：tracker id + 路径末两级
    expect(html).toContain('tracker-bbbb2222 · work/alpha-api')
    expect(html).toContain('tracker-aaaa1111 · work/alpha-web')
    // 没有项目 key 的仓库标「未归属」，且排在最后
    expect(html).toContain('未归属 / repo')
    const order = ['alpha / alpha-api', 'alpha / alpha-web', '未归属 / repo'].map((s) => html.indexOf(s))
    expect(order.every((i) => i >= 0)).toBe(true)
    expect([...order].sort((a, b) => a - b)).toEqual(order)
  })

  it('marks the current option for assistive tech', async () => {
    setLocale('zh-CN')
    const html = await render(TrackerRepoPicker, { modelValue: 'tracker-bbbb2222', repos: REPOS })
    expect(html).toContain('role="listbox"')
    expect(html).toContain('aria-selected="true"')
    expect(html).toContain('data-tracker="tracker-bbbb2222"')
  })

  it('speaks English when the locale is en-US', async () => {
    setLocale('en-US')
    const html = await render(TrackerRepoPicker, { modelValue: '', repos: [] })
    expect(html).toContain('Pick a repository')
    expect(html).not.toContain('请选择仓库')
  })
})

describe('仓库信息条（TrackerRepoPath）', () => {
  it('shows the full path instead of truncating it', async () => {
    setLocale('zh-CN')
    const repo = { tracker_id: 'tracker-aaaa1111', project_key: 'alpha', rel_path: 'D:/work/inhere/my-tools-dev/inhere-tools/gofer', prefix: 'gofer' }
    const html = await render(TrackerRepoPath, { repo, placeholder: '请选择已登记仓库' })
    expect(html).toContain('data-test="tracker-repo-info"')
    expect(html).toContain('data-test="tracker-repo-path"')
    // 整条路径都在 DOM 里（不靠 title 兜底）
    expect(html).toContain('D:/work/inhere/my-tools-dev/inhere-tools/gofer')
    expect(html).toContain('alpha · gofer')
    // 路径样式是换行而不是省略号
    const pathRule = PathSource.slice(PathSource.indexOf('.trp-info-path'))
    expect(pathRule.slice(0, pathRule.indexOf('}'))).not.toContain('ellipsis')
    expect(pathRule.slice(0, pathRule.indexOf('}'))).toContain('overflow-wrap: anywhere')
  })

  it('offers a copy button wired to the path through the shared clipboard helper', async () => {
    setLocale('zh-CN')
    const repo = { tracker_id: 'tracker-aaaa1111', project_key: 'alpha', rel_path: 'D:/work/alpha-web', prefix: 'web' }
    const html = await render(TrackerRepoPath, { repo, placeholder: '请选择已登记仓库' })
    expect(html).toContain('data-test="tracker-repo-copy"')
    expect(html).toContain('复制路径')
    expect(PathSource).toContain('copyText(path.value)')
  })

  it('falls back to the page placeholder without a repository', async () => {
    setLocale('zh-CN')
    const html = await render(TrackerRepoPath, { repo: null, placeholder: '请选择已登记仓库' })
    expect(html).toContain('请选择已登记仓库')
    expect(html).not.toContain('data-test="tracker-repo-copy"')
  })
})
