import { describe, expect, it } from 'vitest'
import type { Runner } from '../api/types'
import { compareVersions, fleetNotice, parseVersion } from './fleetNotice'

function worker(name: string, opts: { status?: Runner['status']; version?: string } = {}): Runner {
  return {
    name,
    type: 'worker',
    status: opts.status ?? 'connected',
    worker_id: name,
    worker: opts.version ? ({ gofer_version: opts.version } as Runner['worker']) : undefined,
  }
}

const server = { os: 'windows', arch: 'amd64', version: '0.199.0' }

describe('fleetNotice', () => {
  it('stays hidden when nothing is wrong', () => {
    expect(fleetNotice([worker('w-a', { version: '0.199.0' })], server)).toBeNull()
    expect(fleetNotice([], server)).toBeNull()
    expect(fleetNotice(undefined, server)).toBeNull()
  })

  it('only looks at workers, not the local runner or peers', () => {
    const runners: Runner[] = [
      { name: 'local', type: 'local', status: 'down' },
      { name: 'peer', type: 'peer-http', status: 'down' },
    ]
    expect(fleetNotice(runners, server)).toBeNull()
  })

  it('lists offline workers by display name', () => {
    const notice = fleetNotice(
      [worker('w-office'), worker('w-lab', { status: 'disconnected' }), worker('w-home', { status: 'down' })],
      server,
    )
    expect(notice?.offline).toEqual(['w-home', 'w-lab'])
    expect(notice?.behind).toEqual([])
  })

  it('lists workers whose version is behind the server', () => {
    const notice = fleetNotice(
      [worker('w-a', { version: '0.198.4' }), worker('w-b', { version: 'v0.199.0' }), worker('w-c', { version: '0.200.1' })],
      server,
    )
    expect(notice?.behind).toEqual([{ name: 'w-a', version: '0.198.4' }])
  })

  it('does not claim a worker is behind when either version is unknown', () => {
    expect(fleetNotice([worker('w-a', { version: '0.100.0' })], { os: 'linux', arch: 'amd64' })).toBeNull()
    expect(fleetNotice([worker('w-a', { version: 'dev' })], server)).toBeNull()
    expect(fleetNotice([worker('w-a')], server)).toBeNull()
  })

  it('reports both conditions at once', () => {
    const notice = fleetNotice([worker('w-a', { version: '0.190.0' }), worker('w-b', { status: 'disconnected' })], server)
    expect(notice).toEqual({ offline: ['w-b'], behind: [{ name: 'w-a', version: '0.190.0' }] })
  })
})

describe('version comparison', () => {
  it('parses dotted versions with an optional v prefix', () => {
    expect(parseVersion('v1.2.3')).toEqual([1, 2, 3])
    expect(parseVersion('0.199')).toEqual([0, 199])
    expect(parseVersion('0.199.0-rc1')).toEqual([0, 199, 0])
    expect(parseVersion('dev')).toBeNull()
    expect(parseVersion('')).toBeNull()
  })

  it('compares numerically, not lexically', () => {
    expect(compareVersions('0.9.0', '0.10.0')).toBeLessThan(0)
    expect(compareVersions('0.199', '0.199.0')).toBe(0)
    expect(compareVersions('v1.0.1', '1.0.0')).toBeGreaterThan(0)
    expect(compareVersions('dev', '1.0.0')).toBeNull()
  })
})
