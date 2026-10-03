import { describe, expect, it } from 'vitest'
import type { MetaProject, MetaRunner, MetaWorker } from '../api/types'
import {
  computeRunnerBlocks,
  effectiveRunnerBlocks,
  pickRunner,
  projectRunnerOptions,
  sessionRunnerBlock,
} from './runnerChoice'

const proj = (over: Partial<MetaProject> = {}): MetaProject => ({
  key: 'p', allowed_agents: [], allowed_runners: [], ...over,
}) as MetaProject
const runners: MetaRunner[] = [
  { name: 'local', type: 'local' },
  { name: 'w1', type: 'worker', worker_id: 'wk1' },
  { name: 'pool', type: 'worker' },
  { name: 'peer', type: 'peer-http' } as MetaRunner,
]
const worker = (over: Partial<MetaWorker>): MetaWorker => ({ id: 'wk1', connected: true, projects: ['p'], ...over }) as MetaWorker

describe('runnerChoice', () => {
  it('filters runners by project allowed_runners', () => {
    expect(projectRunnerOptions(proj(), runners)).toHaveLength(4)
    expect(projectRunnerOptions(proj({ allowed_runners: ['w1'] }), runners).map((r) => r.name)).toEqual(['w1'])
  })

  it('blocks a pinned worker that lacks the project, and non-worker runners for worker-only projects', () => {
    const blocks = computeRunnerBlocks(proj(), runners, [worker({ projects: ['other'] })])
    expect(blocks.w1.short).toContain('无此 project')
    const wo = computeRunnerBlocks(proj({ worker_only: true }), runners, [worker({})])
    expect(wo.local.short).toContain('worker')
    expect(wo.w1).toBeUndefined()
  })

  it('session needs local or a worker with protocol >= 13; unknown protocol is let through', () => {
    expect(sessionRunnerBlock(runners[0], [])).toBeNull()
    expect(sessionRunnerBlock(runners[1], [worker({ protocol_version: 12 })])?.short).toContain('v12')
    expect(sessionRunnerBlock(runners[1], [worker({ protocol_version: 13 })])).toBeNull()
    expect(sessionRunnerBlock(runners[1], [worker({ connected: false })])).toBeNull()
    expect(sessionRunnerBlock(runners[3], [])?.short).toContain('不支持')
  })

  it('effective blocks add session reasons only when a session is requested', () => {
    const base = {}
    const ws = [worker({ protocol_version: 12 })]
    expect(effectiveRunnerBlocks(runners, base, ws, false)).toEqual({})
    const b = effectiveRunnerBlocks(runners, base, ws, true)
    expect(Object.keys(b).sort()).toEqual(['peer', 'pool', 'w1'])
  })

  it('picks: keep current if usable, else allowed_runners[0], else first usable', () => {
    const p = proj({ allowed_runners: ['w1', 'local'] })
    const opts = projectRunnerOptions(p, runners)
    expect(pickRunner('local', p, opts, {})).toBe('local')
    expect(pickRunner('', p, opts, {})).toBe('w1')
    expect(pickRunner('', p, opts, { w1: { short: 'x', full: 'x' } })).toBe('local')
    expect(pickRunner('', p, opts, { w1: { short: 'x', full: 'x' }, local: { short: 'y', full: 'y' } })).toBe('local')
  })
})
