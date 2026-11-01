// 记忆页的「待整理」区（components/MemoryTidy.vue）：列出需判断的项，每条三个操作
// （保留 / 改写 / 归档），只有仓库记忆能归档；没有任何数字徽标或提醒元素。
import { afterEach, describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import MemoryTidy from './MemoryTidy.vue'
import { tidyItems, memorySourceKey } from '../utils/memories'
import type { SourcedMemoryRow } from '../utils/memories'
import { setLocale } from '../store/locale'

const SRC = import.meta.glob('./MemoryTidy.vue', { eager: true, query: '?raw', import: 'default' }) as Record<string, string>
const source = Object.values(SRC)[0] ?? ''

function row(key: string, src: SourcedMemoryRow['source'], data: SourcedMemoryRow['data'] = {}): SourcedMemoryRow {
  const body = { key, content: `content of ${key}`, kind: 'rule', ...data }
  return { id: key, rev: 2, updated_at: '2026-10-20T00:00:00Z', body, data: body, source: src }
}

const repoRow = row('repo-rule', { scope: 'repo', trackerId: 'trk', label: 'gofer / gofer' })
const projRow = row('proj-rule', { scope: 'project', project: 'gofer' }, { flags: [{ at: '2026-10-21T00:00:00Z', reason: '命令已改名' }] })
const doctor = { [memorySourceKey(repoRow)]: [{ slug: 'flagged', detail: '不再成立' }, { slug: 'duplicate' }] }

async function render(props: InstanceType<typeof MemoryTidy>['$props']): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(MemoryTidy, props) }))
}

afterEach(() => setLocale('zh-CN'))

describe('MemoryTidy', () => {
  it('renders every item with its findings and the three actions', async () => {
    setLocale('zh-CN')
    const items = tidyItems([repoRow, projRow], doctor)
    const html = await render({ items, showSource: true })
    expect(html).toContain('data-test="tidy-panel"')
    expect((html.match(/data-test="tidy-item"/g) ?? []).length).toBe(2)
    expect((html.match(/data-test="tidy-keep"/g) ?? []).length).toBe(2)
    expect((html.match(/data-test="tidy-rewrite"/g) ?? []).length).toBe(2)
    expect((html.match(/data-test="tidy-archive"/g) ?? []).length).toBe(2)
    expect(html).toContain('repo-rule')
    expect(html).toContain('proj-rule')
    expect(html).toContain('被标记失效')
    expect(html).toContain('疑似重复')
    expect(html).toContain('不再成立')
    expect(html).toContain('保留')
    expect(html).toContain('改写')
    expect(html).toContain('归档')
    expect(html).toContain('data-test="tidy-source"')
  })

  it('disables archive for project / global memories only', async () => {
    const html = await render({ items: tidyItems([repoRow, projRow], doctor) })
    // 每个条目各自的归档按钮：仓库记忆可归档，项目记忆置灰（不依赖排序）
    const parts = html.split('data-test="tidy-item"').slice(1)
    expect(parts).toHaveLength(2)
    const archiveOf = (key: string) => {
      const part = parts.find((p) => p.includes(key)) ?? ''
      return part.match(/<button[^>]*data-test="tidy-archive"[^>]*>/)?.[0] ?? ''
    }
    expect(archiveOf('repo-rule')).not.toBe('')
    expect(archiveOf('repo-rule')).not.toContain('disabled')
    expect(archiveOf('proj-rule')).toContain('disabled')
  })

  it('shows the empty state and no error line when there is nothing to tidy', async () => {
    const html = await render({ items: [] })
    expect(html).toContain('data-test="tidy-empty"')
    expect(html).toContain('没有需要整理的记忆')
    expect(html).not.toContain('data-test="tidy-error"')
    expect(html).not.toContain('data-test="tidy-item"')
  })

  it('shows an action error and disables the busy row', async () => {
    const items = tidyItems([repoRow], doctor)
    const html = await render({ items, error: '记忆已变化，请刷新', busyKey: items[0]?.key })
    expect(html).toContain('data-test="tidy-error"')
    expect(html).toContain('记忆已变化，请刷新')
    expect((html.match(/<button[^>]*disabled/g) ?? []).length).toBe(3)
  })

  it('has no badge or reminder element: the count only sits in the section heading', () => {
    expect(source).not.toMatch(/badge/i)
    expect(source).not.toMatch(/notify|toast|remind/i)
  })

  it('renders in English', async () => {
    setLocale('en-US')
    const html = await render({ items: tidyItems([repoRow], doctor) })
    expect(html).toContain('To tidy')
    expect(html).toContain('Keep')
    expect(html).toContain('Rewrite')
    expect(html).toContain('Archive')
    expect(html).toContain('Flagged as out of date')
  })
})
