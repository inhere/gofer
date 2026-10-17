import { describe, expect, it } from 'vitest'
import { ApiError } from '../api/client'
import type { WorkerEnrollment } from '../api/types'
import {
  ENROLLMENTS_LINK,
  PAIRING_ALPHABET,
  acceptErrorText,
  buildAcceptRequest,
  cleanSegment,
  collectCountdown,
  decideCountdown,
  disabledHint,
  enrollTitle,
  hostLine,
  isPending,
  isValidPairingCode,
  labelsError,
  normalizePairingCode,
  pairingFromSegments,
  pairingHint,
  pairingSegments,
  pendingEnrollments,
  rejectErrorText,
  splitList,
  stateLabel,
  workerIdError,
} from './enrollments'

const NOW = 1_760_000_000

function enroll(over: Partial<WorkerEnrollment> = {}): WorkerEnrollment {
  return {
    id: 'we_1',
    state: 'pending',
    requested_id: 'gpu1',
    labels: [],
    projects: [],
    host: { hostname: 'box-1', os: 'linux', arch: 'amd64', gofer_version: '0.168.0', protocol_version: '15' },
    source: '10.0.0.9',
    created_at: NOW - 10,
    decide_until: NOW + 80,
    ...over,
  }
}

describe('pairing code', () => {
  it('shares the 30-character alphabet of the server (no 0 O 1 I L U)', () => {
    expect(PAIRING_ALPHABET).toHaveLength(30)
    expect(PAIRING_ALPHABET).not.toMatch(/[0O1ILU]/)
  })

  it('normalizes case, spaces and dashes, and splits into 4+4', () => {
    expect(normalizePairingCode(' k7qx-m3pa ')).toBe('K7QXM3PA')
    expect(pairingSegments('k7q-xm3pa')).toEqual(['K7QX', 'M3PA'])
    expect(pairingFromSegments('k7qx', 'm3-pa')).toBe('K7QXM3PA')
    // 每段超过 4 位就截断，非法字符直接被吃掉
    expect(cleanSegment('k7qxo1')).toBe('K7QX')
  })

  it('accepts only 8 allowed characters and says what is wrong otherwise', () => {
    expect(isValidPairingCode('K7QXM3PA')).toBe(true)
    expect(isValidPairingCode('K7QX')).toBe(false)
    expect(isValidPairingCode('K7QXM3P0')).toBe(false)
    expect(isValidPairingCode('K7QXM3PI')).toBe(false)
    expect(pairingHint('')).toBe('请输入 worker 终端上显示的配对码')
    expect(pairingHint('K7QX')).toContain('当前 4 位')
    expect(pairingHint('K7QXM3PA')).toBe('')
  })
})

describe('countdown', () => {
  it('counts the decide window in seconds (the window is only ~2 minutes)', () => {
    expect(decideCountdown(NOW + 20, NOW)).toBe('还剩 20 秒')
    expect(decideCountdown(NOW + 80, NOW)).toBe('还剩 1 分 20 秒')
    expect(decideCountdown(NOW - 1, NOW)).toBe('已过期')
    expect(decideCountdown(undefined, NOW)).toBe('')
  })

  it('words the collect window timeout differently', () => {
    expect(collectCountdown(NOW + 30, NOW)).toBe('还剩 30 秒')
    expect(collectCountdown(NOW - 5, NOW)).toBe('领取已超时')
  })
})

describe('worker id and labels', () => {
  it('flags the reserved names server / local (case-insensitive) and the id pattern', () => {
    expect(workerIdError('gpu1')).toBe('')
    expect(workerIdError('')).toBe('请填 worker id')
    expect(workerIdError('-gpu')).toContain('worker id 只能')
    expect(workerIdError('server')).toContain('保留名')
    expect(workerIdError('LOCAL')).toContain('保留名')
  })

  it('flags an id that already exists (the server would answer 409)', () => {
    expect(workerIdError('gpu1', ['gpu1'])).toContain('已存在')
    expect(workerIdError('gpu2', ['gpu1'])).toBe('')
  })

  it('splits and validates the label list', () => {
    expect(splitList('linux, gpu  bench')).toEqual(['linux', 'gpu', 'bench'])
    expect(labelsError(['linux', 'gpu-1'])).toBe('')
    expect(labelsError(['lin:ux'])).toContain('标签 lin:ux')
    expect(labelsError(Array.from({ length: 9 }, (_, i) => `l${i}`))).toContain('最多 8 个')
  })
})

describe('accept request', () => {
  const form = { codeFirst: 'k7qx', codeSecond: 'm3pa', workerId: ' gpu1 ', labels: 'linux, gpu', projects: ['p1'] }

  it('builds the body of POST /v1/worker-enrollments/{id}/accept', () => {
    const { body, error } = buildAcceptRequest(form, ['other'])
    expect(error).toBe('')
    expect(body).toEqual({ pairing_code: 'K7QXM3PA', worker_id: 'gpu1', labels: ['linux', 'gpu'], projects: ['p1'] })
  })

  it('refuses locally: short code, reserved id, existing id, bad label', () => {
    expect(buildAcceptRequest({ ...form, codeSecond: 'm3p' }).error).toContain('8 位')
    expect(buildAcceptRequest({ ...form, workerId: 'server' }).error).toContain('保留名')
    expect(buildAcceptRequest(form, ['gpu1']).error).toContain('已存在')
    expect(buildAcceptRequest({ ...form, labels: 'lin:ux' }).error).toContain('标签')
    expect(buildAcceptRequest({ ...form, codeSecond: 'm3p' }).body).toBeNull()
  })

  it('maps the server answers to readable Chinese', () => {
    const mismatch = new ApiError(400, 'invalid request - pairing code mismatch', 'pairing code mismatch', 'invalid request')
    expect(acceptErrorText(mismatch)).toContain('配对码不符')
    const exists = new ApiError(409, 'worker already exists - ...', 'remove the existing worker first or choose another worker id', 'worker already exists')
    expect(acceptErrorText(exists)).toContain('先删除旧 worker')
    const gone = new ApiError(409, 'conflict - ...', 'the enrollment is no longer pending', 'conflict')
    expect(acceptErrorText(gone)).toContain('已被处理或已过期')
    expect(acceptErrorText(new ApiError(403, 'forbidden'))).toContain('worker.manage')
    expect(acceptErrorText(new Error('boom'))).toBe('接受失败：boom')
    expect(rejectErrorText(new ApiError(409, 'conflict'))).toContain('已被处理或已过期')
    expect(rejectErrorText(new ApiError(403, 'forbidden'))).toContain('worker.manage')
  })
})

describe('list rendering helpers', () => {
  it('hints how to enable the feature when the server has it off', () => {
    expect(disabledHint(false)).toContain('server.worker_enrollment.enabled')
    expect(disabledHint(true)).toBe('')
  })

  it('picks the requests that still wait for a decision', () => {
    const rows = [
      enroll({ id: 'a' }),
      enroll({ id: 'b', state: 'accepted', decide_until: NOW + 100 }),
      enroll({ id: 'c', decide_until: NOW - 1 }),
    ]
    expect(isPending(rows[0], NOW)).toBe(true)
    expect(isPending(rows[1], NOW)).toBe(false)
    expect(pendingEnrollments(rows, NOW).map((e) => e.id)).toEqual(['a'])
  })

  it('labels the states and describes the machine', () => {
    expect(stateLabel('pending')).toBe('待决定')
    expect(stateLabel('collected')).toBe('已完成')
    expect(stateLabel('weird')).toBe('weird')
    expect(enrollTitle(enroll())).toBe('box-1 → gpu1')
    expect(enrollTitle(enroll({ requested_id: '' }))).toBe('box-1')
    expect(enrollTitle(enroll({ host: {} }))).toBe('gpu1')
    expect(enrollTitle(enroll({ host: {}, requested_id: '' }))).toBe('(未知主机)')
    expect(hostLine(enroll().host)).toBe('linux/amd64 · gofer v0.168.0 · 协议 v15')
    expect(hostLine({ os: 'windows' })).toBe('windows/?')
    expect(hostLine(undefined)).toBe('')
  })

  it('points every enrollment entry at the Runners page section', () => {
    expect(ENROLLMENTS_LINK).toBe('/runners#enrollments')
  })
})
