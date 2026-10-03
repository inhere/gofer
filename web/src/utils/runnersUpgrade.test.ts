import { describe, expect, it } from 'vitest'
import type { Runner, RunnersServerInfo, WorkerUpgradeRecord } from '../api/types'
import { upgradeBlockReason, upgradeStateClass, upgradeSummary } from './runners'

const server: RunnersServerInfo = { os: 'linux', arch: 'amd64', version: 'v2 (abc)' }

function worker(over: Partial<Runner> = {}, w: Partial<NonNullable<Runner['worker']>> = {}): Runner {
  return {
    name: 'w1',
    type: 'worker',
    status: 'connected',
    worker_id: 'w1',
    worker: { last_heartbeat: 1, heartbeat_age_ms: 0, in_flight: 0, os: 'linux', arch: 'amd64', protocol_version: 15, ...w },
    ...over,
  } as Runner
}

describe('upgradeBlockReason', () => {
  it('allows a connected v15 worker of the same platform', () => {
    expect(upgradeBlockReason(worker(), server)).toBe('')
  })
  it('refuses offline, old protocol, pending and unknown-server cases', () => {
    expect(upgradeBlockReason(worker({ status: 'disconnected' }), server)).toContain('离线')
    expect(upgradeBlockReason(worker({}, { protocol_version: 14 }), server)).toContain('手动升级一次')
    expect(upgradeBlockReason(worker({ upgrade: { state: 'pending' } as WorkerUpgradeRecord }), server)).toContain('进行中')
    expect(upgradeBlockReason(worker(), undefined)).toContain('不支持')
  })
  it('points to the CLI when the platform differs', () => {
    const msg = upgradeBlockReason(worker({}, { os: 'windows', arch: 'amd64' }), server)
    expect(msg).toContain('windows/amd64')
    expect(msg).toContain('gofer worker upgrade w1 --file')
  })
})

describe('upgradeSummary', () => {
  it('describes each state like the CLI', () => {
    const base = { worker_id: 'w1', upgrade_id: 'u', started_at: 1 }
    expect(upgradeSummary({ ...base, state: 'succeeded', target_version: 'v2', duration_ms: 4200 })).toBe('已升级到 v2（耗时 4.2s）')
    expect(upgradeSummary({ ...base, state: 'rolled_back', error: 'new process never registered' })).toBe('已回滚：new process never registered')
    expect(upgradeSummary({ ...base, state: 'failed', error: 'drain timed out' })).toContain('升级失败：drain timed out')
    expect(upgradeSummary({ ...base, state: 'pending', from_version: 'v1', target_version: 'v2' })).toContain('v1 → v2')
    expect(upgradeSummary(undefined)).toBe('')
  })
  it('maps states to status classes', () => {
    expect(upgradeStateClass({ state: 'succeeded' } as WorkerUpgradeRecord)).toBe('st--ok')
    expect(upgradeStateClass({ state: 'rolled_back' } as WorkerUpgradeRecord)).toBe('st--down')
    expect(upgradeStateClass(undefined)).toBe('')
  })
})
