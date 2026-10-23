// 任务页的布局与默认页签契约（设计稿 §7.1、§16）。
import { describe, expect, it } from 'vitest'
import {
  DETAIL_MIN_WIDTH,
  LIST_MIN_WIDTH,
  SPLIT_MIN_CONTENT_WIDTH,
  SPLIT_STORAGE_KEY,
  detailPaneWidth,
  jobTabForStatus,
  readSplitPreference,
  saveSplitPreference,
  splitAllowed,
  splitLayout,
} from './jobsLayout'

function memoryStorage(initial: Record<string, string> = {}): Storage {
  const map = new Map(Object.entries(initial))
  return {
    get length() {
      return map.size
    },
    clear: () => map.clear(),
    getItem: (k: string) => map.get(k) ?? null,
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    removeItem: (k: string) => void map.delete(k),
    setItem: (k: string, v: string) => void map.set(k, v),
  }
}

describe('列表 + 详情的并排判断', () => {
  it('starts splitting at 1280px of content width', () => {
    expect(splitAllowed(SPLIT_MIN_CONTENT_WIDTH)).toBe(true)
    expect(splitAllowed(SPLIT_MIN_CONTENT_WIDTH - 1)).toBe(false)
    expect(splitAllowed(900)).toBe(false)
  })

  it('never splits when the detail pane would be narrower than 760px', () => {
    expect(SPLIT_MIN_CONTENT_WIDTH - LIST_MIN_WIDTH).toBe(DETAIL_MIN_WIDTH)
    expect(detailPaneWidth(SPLIT_MIN_CONTENT_WIDTH)).toBe(DETAIL_MIN_WIDTH)
    expect(detailPaneWidth(1600)).toBe(1600 - LIST_MIN_WIDTH)
    // 极窄（手机）也不会算出负宽度
    expect(detailPaneWidth(390)).toBe(0)
  })

  it('ignores widths that are not finite numbers (SSR / 没量到)', () => {
    expect(splitAllowed(Number.NaN)).toBe(false)
    expect(splitAllowed(Number.POSITIVE_INFINITY)).toBe(false)
    expect(splitLayout(0, 'split')).toBe(false)
  })

  it('lets the 「单栏」 toggle win over a wide viewport', () => {
    expect(splitLayout(2000, 'split')).toBe(true)
    expect(splitLayout(2000, 'single')).toBe(false)
    expect(splitLayout(1000, 'split')).toBe(false)
  })
})

describe('并排/单栏偏好（记在本地）', () => {
  it('defaults to split and remembers single', () => {
    const storage = memoryStorage()
    expect(readSplitPreference(storage)).toBe('split')
    saveSplitPreference('single', storage)
    expect(storage.getItem(SPLIT_STORAGE_KEY)).toBe('single')
    expect(readSplitPreference(storage)).toBe('single')
    saveSplitPreference('split', storage)
    expect(readSplitPreference(storage)).toBe('split')
  })

  it('survives a storage that throws', () => {
    const broken = memoryStorage()
    broken.getItem = () => {
      throw new Error('blocked')
    }
    broken.setItem = () => {
      throw new Error('blocked')
    }
    expect(readSplitPreference(broken)).toBe('split')
    expect(() => saveSplitPreference('single', broken)).not.toThrow()
  })
})

describe('详情页签的默认值（§16-5）', () => {
  it('sends needs_review to 验收 and failed to 日志', () => {
    expect(jobTabForStatus('needs_review')).toBe('review')
    expect(jobTabForStatus('failed')).toBe('log')
  })

  it('keeps everything else on 概览', () => {
    for (const status of ['running', 'queued', 'done', 'cancelled', 'timeout', 'rejected', 'awaiting_approval', '']) {
      expect(jobTabForStatus(status), status).toBe('overview')
    }
    expect(jobTabForStatus(null)).toBe('overview')
    expect(jobTabForStatus(undefined)).toBe('overview')
  })
})
