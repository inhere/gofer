import { describe, expect, it } from 'vitest'
import { hashName, runnerTag, runnerTone } from './runnerTag'

describe('runnerTag', () => {
  it('derives the same tone for the same worker id every time', () => {
    const names = ['w-docker-claude', 'w-office', 'w-lab', 'w-home', 'w-mac-win10']
    for (const name of names) {
      expect(runnerTone(name)).toBe(runnerTone(name))
      expect(hashName(name)).toBe(hashName(name))
    }
    // 跨调用稳定：一组固定输入的色相是快照，改了就是「同一台 worker 换了颜色」
    expect(names.map((n) => runnerTone(n))).toEqual(names.map((n) => runnerTone(n)))
  })

  it('only derives from the documented token tones', () => {
    const allowed = new Set(['run', 'time', 'accent', 'need', 'fail', 'idle'])
    for (const name of ['a', 'b', 'w1', 'w2', 'worker-1', 'worker-2', 'x'.repeat(40)]) {
      expect(allowed.has(runnerTone(name))).toBe(true)
    }
  })

  it('distributes different worker ids over more than one tone', () => {
    const tones = new Set(['w-docker-claude', 'w-office', 'w-lab', 'w-home', 'w-mac-win10'].map((n) => runnerTone(n)))
    expect(tones.size).toBeGreaterThan(1)
  })

  it('keeps both spellings of the builtin runner neutral and labelled server', () => {
    for (const name of ['local', 'server']) {
      const tag = runnerTag(name)
      expect(tag).toEqual({ label: 'server', tone: 'idle', color: 'var(--idle)', soft: 'var(--idle-soft)' })
    }
  })

  it('returns null without a runner name and the plain name otherwise', () => {
    expect(runnerTag('')).toBeNull()
    expect(runnerTag(undefined)).toBeNull()
    const tag = runnerTag('w-docker-claude')
    expect(tag?.label).toBe('w-docker-claude')
    expect(tag?.color).toBe(`var(--${runnerTone('w-docker-claude')})`)
    expect(tag?.soft).toBe(`var(--${runnerTone('w-docker-claude')}-soft)`)
  })
})
