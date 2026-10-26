// 项目页「按执行机」视图的数据整形（gofer-d1kn）：准入匹配、worker 自有项目、离线与默认工作空间。
import { describe, expect, it } from 'vitest'
import type { ProjectDetail, Runner, RunnerDirs, WorkerView } from '../api/types'
import {
  buildFleet,
  normalizeRunnerName,
  projectAllowedFromConfig,
  reachesRunner,
  workerOwnProjects,
  type FleetInput,
} from './projectFleet'

function dirs(workspace?: string, projects?: { key: string; path: string }[]): RunnerDirs {
  return {
    workspace: workspace ? { path: workspace, exists: true } : undefined,
    projects: (projects ?? []).map((p) => ({ key: p.key, path: p.path, exists: true })),
  }
}

function localRunner(runnerDirs?: RunnerDirs): Runner {
  return { name: 'local', type: 'local', status: 'up', dirs: runnerDirs }
}

function workerRunner(name: string, workerID: string, status: Runner['status']): Runner {
  return { name, type: 'worker', status, worker_id: workerID }
}

function workerView(workerID: string, connected: boolean, view?: RunnerDirs): WorkerView {
  return {
    worker_id: workerID,
    connected,
    worker: view ? { last_heartbeat: 0, heartbeat_age_ms: 0, in_flight: 0, dirs: view } : undefined,
  }
}

function projectDetail(key: string, allowed?: string[]): ProjectDetail {
  return { key, host_path: `/srv/${key}`, allowed_runners: allowed, allow_exec: false, allow_interactive: false }
}

function input(over: Partial<FleetInput> = {}): FleetInput {
  return {
    projects: [],
    injected: [],
    allowed: {},
    runners: [],
    workerViews: {},
    workerProjects: {},
    workerErrors: {},
    ...over,
  }
}

describe('reachesRunner', () => {
  it('treats an empty allowlist as local-only, exactly like the server does', () => {
    expect(reachesRunner([], ['local'])).toBe(true)
    expect(reachesRunner(undefined, ['local'])).toBe(true)
    expect(reachesRunner([], ['w-a'])).toBe(false)
    expect(reachesRunner(undefined, ['w-a'])).toBe(false)
  })

  it('accepts the `server` spelling of the built-in local runner', () => {
    expect(normalizeRunnerName('SERVER')).toBe('local')
    expect(reachesRunner(['server'], ['local'])).toBe(true)
    expect(reachesRunner(['local'], ['local'])).toBe(true)
  })

  it('matches a worker by its runner name only', () => {
    expect(reachesRunner(['local', 'w-a'], ['w-a'])).toBe(true)
    expect(reachesRunner(['w-a'], ['w-b'])).toBe(false)
    expect(reachesRunner(['w-a'], ['w-a', 'w-a-2'])).toBe(true)
  })
})

describe('buildFleet', () => {
  const fleet = buildFleet(
    input({
      projects: ['default', 'alpha', 'beta'],
      injected: ['default'],
      allowed: {
        default: ['local'],
        alpha: ['w-home'],
        beta: ['local', 'w-home'],
      },
      runners: [
        localRunner(dirs('C:/Users/k/.gofer/workspace')),
        workerRunner('w-home', 'w-home', 'connected'),
        workerRunner('w-off', 'w-off', 'disconnected'),
      ],
      workerViews: {
        'w-home': workerView(
          'w-home',
          true,
          dirs('D:/gofer/workspace', [{ key: 'demo-app-dev', path: 'D:/work/demo-app' }]),
        ),
        'w-off': workerView('w-off', false),
      },
      workerProjects: {
        'w-home': ['demo-app-dev', 'alpha'],
        'w-off': undefined,
      },
      workerErrors: { 'w-off': 'worker w-off is offline' },
    }),
  )

  const byID = (id: string) => fleet.find((g) => g.id === id)!

  it('puts the server host first with its own default workspace and local-only projects', () => {
    expect(fleet[0].kind).toBe('server')
    expect(fleet[0].label).toBe('server')
    expect(fleet[0].online).toBe(true)
    expect(fleet[0].workspace?.path).toBe('C:/Users/k/.gofer/workspace')
    // default 与 beta 的 allowed_runners 含本机；alpha 只给 worker
    expect(fleet[0].projects.map((p) => p.key)).toEqual(['beta', 'default'])
    expect(fleet[0].projects.find((p) => p.key === 'default')?.injected).toBe(true)
    expect(fleet[0].own).toEqual([])
  })

  it('lists one group per worker with the projects it may run', () => {
    const g = byID('w-home')
    expect(g.kind).toBe('worker')
    expect(g.online).toBe(true)
    // alpha 只给这台 worker，beta 给本机 + 它；default 只给本机
    expect(g.projects.map((p) => p.key)).toEqual(['alpha', 'beta'])
  })

  it('takes the worker default workspace and worker-owned projects from GET /v1/workers/{id}', () => {
    const g = byID('w-home')
    expect(g.workspace?.path).toBe('D:/gofer/workspace')
    // alpha 同时被 server 声明 → 不是 worker 自有；demo-app-dev 才是
    expect(g.own).toHaveLength(1)
    expect(g.own[0]).toMatchObject({ key: 'demo-app-dev', path: 'D:/work/demo-app', exists: true })
    expect(g.ownNote).toBe('')
  })

  it('greys out an offline worker and says why the data is missing', () => {
    const g = byID('w-off')
    expect(g.online).toBe(false)
    expect(g.workspace).toBeUndefined()
    expect(g.workspaceNote).toBe('offline')
    expect(g.ownNote).toBe('offline')
    expect(g.projects).toEqual([])
    expect(g.error).toBe('worker w-off is offline')
  })

  it('does not read an empty project list of an offline worker as “no worker-owned projects”', () => {
    // GET /v1/workers/{id}/projects 对已登记但离线的 worker 也回 200 + 空列表。
    const groups = buildFleet(
      input({
        runners: [localRunner(), workerRunner('w-off', 'w-off', 'disconnected')],
        workerViews: { 'w-off': workerView('w-off', false) },
        workerProjects: { 'w-off': [] },
      }),
    )
    expect(groups.find((x) => x.id === 'w-off')?.ownNote).toBe('offline')
  })

  it('reports a missing default workspace for a connected worker that never reported one', () => {
    const groups = buildFleet(
      input({
        runners: [localRunner(), workerRunner('w-a', 'w-a', 'connected')],
        workerViews: { 'w-a': workerView('w-a', true) },
        workerProjects: { 'w-a': [] },
      }),
    )
    const g = groups.find((x) => x.id === 'w-a')!
    expect(g.workspaceNote).toBe('notReported')
    expect(g.ownNote).toBe('')
  })

  it('still renders a server group when /v1/runners returned nothing', () => {
    const groups = buildFleet(input({ projects: ['alpha'], allowed: { alpha: [] } }))
    expect(groups).toHaveLength(1)
    expect(groups[0].kind).toBe('server')
    expect(groups[0].workspaceNote).toBe('notReported')
    expect(groups[0].projects.map((p) => p.key)).toEqual(['alpha'])
  })
})

describe('workerOwnProjects', () => {
  it('merges the same worker-only key across workers and drops server projects', () => {
    const rows = workerOwnProjects({
      projects: ['alpha'],
      workerProjects: {
        'w-a': ['demo-app-dev', 'alpha'],
        'w-b': ['demo-app-dev', 'zoo'],
        'w-c': undefined,
      },
    })
    expect(rows).toEqual([
      { key: 'demo-app-dev', workers: ['w-a', 'w-b'] },
      { key: 'zoo', workers: ['w-b'] },
    ])
  })
})

describe('projectAllowedFromConfig', () => {
  it('seeds allowed_runners from GET /v1/config', () => {
    expect(projectAllowedFromConfig([projectDetail('alpha', ['w-a']), projectDetail('beta')])).toEqual({
      alpha: ['w-a'],
      beta: [],
    })
    expect(projectAllowedFromConfig(undefined)).toEqual({})
  })
})
