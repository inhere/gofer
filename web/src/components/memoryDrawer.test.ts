// 记忆详情抽屉（components/MemoryDrawer.vue，issue gofer-icqt）：展示 key / 摘要 / 类型 /
// 来源 / 更新时间 / 过期时间 / 标签，标签 chip 可增删，保存 / 删除 / 标记失效按来源能力出现。
// 抽屉走 ui/Drawer（Teleport 到 body），SSR 里内容在 ctx.teleports 里，断言那一份。
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import MemoryDrawer from './MemoryDrawer.vue'
import type { SourcedMemoryRow } from '../utils/memories'
import { setLocale } from '../store/locale'

// 测试环境没有 DOM，而 MarkdownBlock 的 DOMPurify 需要 window；这里只断言抽屉的
// 字段与文案，正文渲染不是本测试的对象，所以把 sanitize 当成透传。
vi.mock('dompurify', () => ({ default: { sanitize: (html: string) => html } }))

const SRC = import.meta.glob('./MemoryDrawer.vue', { eager: true, query: '?raw', import: 'default' }) as Record<string, string>
const source = Object.values(SRC)[0] ?? ''

const projectData: SourcedMemoryRow['data'] = {
  key: 'release-flow',
  content: '# 发版流程\n先跑全量验证',
  tags: ['release', 'prime'],
  summary: '发版流程',
  kind: 'rule',
  expires_at: '2026-12-01T00:00:00Z',
  by: 'alice',
  flags: [{ at: '2026-10-21T00:00:00Z', by: 'suga', reason: '命令已改名' }],
}

const projectRow: SourcedMemoryRow = {
  id: 'release-flow',
  rev: 3,
  updated_at: '2026-10-20T10:00:00Z',
  body: projectData,
  data: projectData,
  source: { scope: 'project', project: 'gofer' },
}

const repoData: SourcedMemoryRow['data'] = { key: 'release-flow', content: '仓库里的发版流程', tags: [], kind: 'note' }

const repoRow: SourcedMemoryRow = {
  id: 'release-flow',
  rev: 1,
  updated_at: '2026-10-20T10:00:00Z',
  body: repoData,
  data: repoData,
  source: { scope: 'repo', trackerId: 'trk', label: 'gofer / gofer' },
}

async function render(props: Record<string, unknown>): Promise<string> {
  const ctx: { teleports?: Record<string, string> } = {}
  const app = createSSRApp({ render: () => h(MemoryDrawer, props as never) })
  await renderToString(app, ctx)
  return ctx.teleports?.body ?? ''
}

afterEach(() => setLocale('zh-CN'))

describe('记忆抽屉的字段（对齐 issue 要求的字段清单）', () => {
  it('shows key, kind, source, updated, expiry and the tag chips', async () => {
    setLocale('zh-CN')
    const html = await render({ row: projectRow })
    expect(html).toContain('data-test="memory-drawer"')
    expect(html).toContain('data-test="drawer-memory-key"')
    expect(html).toContain('release-flow')
    expect(html).toContain('规则')
    expect(html).toContain('项目 · gofer')
    expect(html).toContain('data-test="drawer-memory-expires"')
    expect(html).toContain('12-01')
    // 标签 chip：每个标签一枚，带删除按钮
    expect(html).toContain('data-test="memory-tag"')
    expect(html).toContain('release')
    expect(html).toContain('data-test="memory-tag-remove"')
    // 可编辑内容与摘要
    expect(html).toContain('data-test="memory-summary-input"')
    expect(html).toContain('data-test="memory-content"')
    expect(html).toContain('data-test="memory-save"')
    expect(html).toContain('data-test="memory-delete"')
  })

  it('offers "flag as stale" only where the API exists (server-stored memories)', async () => {
    const project = await render({ row: projectRow })
    expect(project).toContain('data-test="memory-flag"')
    expect(project).toContain('data-test="memory-flag-reason"')
    expect(project).toContain('data-test="drawer-memory-flag"')
    const repo = await render({ row: repoRow })
    expect(repo).not.toContain('data-test="memory-flag"')
    expect(repo).toContain('仓库记忆不支持标记失效')
  })

  it('renders the source and the labels in English after a locale switch', async () => {
    setLocale('en-US')
    const html = await render({ row: { ...repoRow, data: { ...repoRow.data, expires_at: '' } } })
    expect(html).toContain('Repository · gofer / gofer')
    expect(html).toContain('Never')
    expect(html).toContain('Save')
    expect(html).not.toContain('保存')
  })
})

describe('抽屉的编辑契约', () => {
  it('saves content, summary and tags together, and deletes only on the second click', () => {
    expect(source).toContain("emit('save', { content: edit.value.content, summary: edit.value.summary, tags: [...edit.value.tags] })")
    expect(source).toContain('confirmDelete')
    expect(source).toContain("emit('remove')")
    // 标签 chip 增删走纯函数（utils/memories.ts），不在这里各写一份
    expect(source).toContain('addMemoryTag(edit.value.tags, tag)')
    expect(source).toContain('removeMemoryTag(edit.value.tags, tag)')
    // 标记失效：理由必填，仓库记忆没有这个接口
    expect(source).toContain("props.row.source?.scope !== 'repo'")
    expect(source).toContain("emit('flag', reason)")
  })
})
