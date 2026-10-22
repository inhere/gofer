// 状态族映射的穷举测试（设计稿 §5、§13）：每个实体的每个状态都必须落到某个族，
// 且需要一个「需要人」的口径核对——黄色只能出现在真的等人处。
import { describe, expect, it } from 'vitest'
import {
  ENTITY_STATUSES,
  STATUS_FAMILIES,
  STATUS_FAMILY_MAP,
  statusFamily,
  statusFamilyClass,
  type StatusFamily,
} from './statusFamily'

const EXPECTED: Record<string, StatusFamily> = {
  // job
  queued: 'queue',
  running: 'run',
  awaiting_input: 'need',
  pending_interaction: 'need',
  needs_review: 'need',
  awaiting_approval: 'need',
  waiting_dir: 'queue',
  paused_for_update: 'idle',
  recovering: 'run',
  done: 'done',
  failed: 'fail',
  cancelled: 'idle',
  timeout: 'time',
  rejected: 'fail',
  // plan
  open: 'run',
  archived: 'idle',
  blocked: 'need',
  // todo
  pending: 'queue',
  ready: 'queue',
  doing: 'run',
  skipped: 'idle',
  // workflow
  // （running / done / failed / cancelled 已在上面）
  // issue（tracker 侧是自由字符串，按接口实际取值列出）
  closed: 'idle',
  in_progress: 'run',
  // 通用别名（会话等）：同一个词在不同实体上是同一族
  waiting_reply: 'need',
  passed: 'done',
  accepted: 'done',
  down: 'fail',
  expired: 'fail',
  stopped: 'idle',
  unknown: 'idle',
  offline: 'idle',
  paused: 'idle',
  draft: 'idle',
  todo: 'queue',
  // work item
  active: 'run',
  needs_me: 'need',
  waiting_resource: 'queue',
  needs_onsite: 'need',
  review: 'need',
  parked: 'idle',
  dropped: 'idle',
  // session
  working: 'run',
  idle: 'idle',
}

describe('statusFamily', () => {
  it('covers every entity status with a family', () => {
    for (const [entity, values] of Object.entries(ENTITY_STATUSES)) {
      for (const value of values) {
        const fam = statusFamily(value)
        expect(STATUS_FAMILIES, `${entity}.${value} 落到未知族 ${fam}`).toContain(fam)
        expect(fam, `${entity}.${value} 的族与预期不符`).toBe(EXPECTED[value])
      }
    }
  })

  it('maps every known value exactly as documented', () => {
    for (const [status, fam] of Object.entries(STATUS_FAMILY_MAP)) {
      expect(statusFamily(status), status).toBe(fam)
      expect(EXPECTED[status], `${status} 没进预期表`).toBe(fam)
    }
  })

  it('falls back to idle for empty and unknown statuses', () => {
    expect(statusFamily(undefined)).toBe('idle')
    expect(statusFamily(null)).toBe('idle')
    expect(statusFamily('')).toBe('idle')
    // 后端新加的状态不能变成空白，也不能误报「需要人」
    expect(statusFamily('brand_new_status')).toBe('idle')
  })

  it('keeps the yellow family for human hand-offs only', () => {
    const needValues = Object.entries(STATUS_FAMILY_MAP)
      .filter(([, fam]) => fam === 'need')
      .map(([status]) => status)
      .sort()
    expect(needValues).toEqual(
      [
        'awaiting_approval',
        'awaiting_input',
        'blocked',
        'needs_me',
        'needs_onsite',
        'needs_review',
        'pending_interaction',
        'review',
        'waiting_reply',
      ].sort(),
    )
  })

  it('has no family outside the documented seven', () => {
    expect(STATUS_FAMILIES).toEqual(['run', 'need', 'done', 'fail', 'time', 'idle', 'queue'])
    for (const fam of Object.values(STATUS_FAMILY_MAP)) {
      expect(STATUS_FAMILIES).toContain(fam)
    }
  })

  it('gives every family a css class hook', () => {
    for (const fam of STATUS_FAMILIES) {
      expect(statusFamilyClass(fam)).toBe(`k-${fam}`)
    }
  })
})
