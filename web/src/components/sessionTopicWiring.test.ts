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
  it('the drawer and the session cards embed it', () => {
    expect(src('SessionDrawer.vue')).toContain('<SessionTopic')
    const v = src('Sessions.vue')
    expect(v).toContain('<SessionTopic')
    expect(v).toContain('data-test="card-topic"')
    expect(v).toContain('data-test="maybe-offline"')
    expect(v).toContain('hitReasons(s.hits)')
  })
  it('the find box builds params and clearing returns to the list', () => {
    const v = src('Sessions.vue')
    expect(v).toContain('buildFindParams(findInput.value)')
    expect(v).toContain('...(activeFind.value ?? {})')
    expect(v).toContain('data-test="find-clear"')
  })
  it('client has PATCH and find params', () => {
    const c = src('client.ts')
    expect(c).toContain("method: 'PATCH'")
    expect(c).toContain("params.set('q', opts.q)")
    expect(c).toContain("params.set('path', opts.path)")
  })
})
