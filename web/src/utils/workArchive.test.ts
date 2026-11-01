import { describe, expect, it } from 'vitest'
import type { WorkItem } from '../api/types'
import { archiveAutoFlow, autoItemCount } from './workArchive'

const item = (over: Partial<WorkItem>): WorkItem => ({ id: 'w1', status: 'active', source: 'auto', ...over }) as WorkItem

describe('autoItemCount', () => {
  it('counts the open auto items only', () => {
    expect(
      autoItemCount([
        item({ id: 'a' }),
        item({ id: 'b', status: 'needs_me' }),
        item({ id: 'c', status: 'done' }),
        item({ id: 'd', status: 'dropped' }),
        item({ id: 'e', source: 'human' }),
        item({ id: 'f', source: 'report' }),
      ]),
    ).toBe(2)
    expect(autoItemCount([])).toBe(0)
  })
})

describe('archiveAutoFlow', () => {
  function deps(ids: string[], answer: boolean) {
    const calls: string[] = []
    return {
      calls,
      d: {
        preview: async () => {
          calls.push('preview')
          return { ids }
        },
        run: async () => {
          calls.push('run')
          return { archived: ids.length }
        },
        confirm: (n: number) => {
          calls.push(`confirm(${n})`)
          return answer
        },
      },
    }
  }

  it('previews, asks with the count, and only then archives', async () => {
    const { calls, d } = deps(['a', 'b', 'c'], true)
    expect(await archiveAutoFlow(d)).toEqual({ state: 'done', n: 3 })
    expect(calls).toEqual(['preview', 'confirm(3)', 'run'])
  })

  it('archives nothing when the person says no', async () => {
    const { calls, d } = deps(['a', 'b'], false)
    expect(await archiveAutoFlow(d)).toEqual({ state: 'cancelled', n: 2 })
    expect(calls).toEqual(['preview', 'confirm(2)'])
  })

  it('does not even ask when nothing qualifies', async () => {
    const { calls, d } = deps([], true)
    expect(await archiveAutoFlow(d)).toEqual({ state: 'none' })
    expect(calls).toEqual(['preview'])
  })
})
