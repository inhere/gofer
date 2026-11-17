import { describe, expect, it } from 'vitest'

const mods = import.meta.glob(
  ['./SessionTopic.vue', './SessionDrawer.vue', '../views/Sessions.vue', '../api/client.ts'],
  { eager: true, query: '?raw', import: 'default' },
) as Record<string, string>
const src = (n: string): string => {
  const k = Object.keys(mods).find((x) => x.endsWith(n))
  if (!k) throw new Error(`missing ${n}`)
  return mods[k]
}

describe('session topic wiring', () => {
  it('SessionTopic edits via PATCH with the permission gate and ended reason', () => {
    const c = src('SessionTopic.vue')
    expect(c).toContain('patchSessionTopic')
    expect(c).toContain('canEditTopic(whoami.value')
    expect(c).toContain('data-test="topic-why"')
    expect(c).toContain('buildTopicPatch')
  })
  it('the drawer embeds it (the history rows lead there)', () => {
    expect(src('SessionDrawer.vue')).toContain('<SessionTopic')
  })
  it('the history page searches through the thread interface and clearing returns to the list', () => {
    const v = src('Sessions.vue')
    // R13：搜索交给线程接口（q 覆盖标题 / 项目 / 最后一条消息），不再自己拼 find 参数。
    expect(v).toContain('q: activeQuery.value')
    expect(v).toContain('data-test="find-clear"')
    expect(v).toContain('activeQuery.value = searchInput.value')
  })
  it('client has PATCH and find params', () => {
    const c = src('client.ts')
    expect(c).toContain("method: 'PATCH'")
    expect(c).toContain("params.set('q', opts.q)")
    expect(c).toContain("params.set('path', opts.path)")
  })
})
