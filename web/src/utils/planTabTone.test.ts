// 计划详情页签计数徽标的取色（issue gofer-qw2g）：只断言族的选择，颜色仍来自 status.css。
import { describe, expect, it } from 'vitest'
import { decisionsTabTone, jobsTabTone, todoTabTone } from './planTabTone'
import type { Job, Todo } from '../api/types'

const todo = (patch: Partial<Todo>): Todo => ({
  todo_id: 't1',
  plan_id: 'p1',
  title: 'todo',
  status: 'pending',
  done: false,
  created_at: 1_700_000_000,
  updated_at: 1_700_000_000,
  ...patch,
})

const job = (status: Job['status']): Job => ({ id: 'j1', status }) as Job

describe('todo tab tone', () => {
  it('marks a blocked chain as needing a person', () => {
    expect(todoTabTone([todo({ status: 'doing' })], 't1')).toBe('need')
  })

  it('turns red when the latest job of any todo failed', () => {
    for (const status of ['failed', 'timeout', 'rejected'] as const) {
      expect(todoTabTone([todo({ status: 'doing', jobs: [{ id: 'j1', status }] as Todo['jobs'] })])).toBe('fail')
    }
    // 更早的失败不算：只看最近一次 job。
    expect(
      todoTabTone([todo({ status: 'doing', jobs: [{ id: 'j2', status: 'running' }, { id: 'j1', status: 'failed' }] as Todo['jobs'] })]),
    ).toBe('run')
  })

  it('uses the running colour while something is in progress', () => {
    expect(todoTabTone([todo({ status: 'doing' }), todo({ todo_id: 't2', status: 'pending' })])).toBe('run')
  })

  it('goes green only when every todo is done or skipped', () => {
    expect(todoTabTone([todo({ status: 'done', done: true }), todo({ todo_id: 't2', status: 'skipped' })])).toBe('done')
    expect(todoTabTone([todo({ status: 'pending' })])).toBe('queue')
    expect(todoTabTone([])).toBe('queue')
  })
})

describe('jobs tab tone', () => {
  it('prefers the server roll-up and the job statuses for failures', () => {
    expect(jobsTabTone([], 2)).toBe('fail')
    expect(jobsTabTone([job('failed')])).toBe('fail')
  })

  it('uses the running colour while a job is in flight', () => {
    expect(jobsTabTone([job('running'), job('queued')])).toBe('run')
    expect(jobsTabTone([job('recovering')])).toBe('run')
  })

  it('goes green only when every job finished', () => {
    expect(jobsTabTone([job('done'), job('done')])).toBe('done')
    expect(jobsTabTone([job('queued')])).toBe('queue')
    expect(jobsTabTone([])).toBe('queue')
  })
})

describe('decisions tab tone', () => {
  it('is yellow while a decision waits for an answer', () => {
    expect(decisionsTabTone(1)).toBe('need')
    expect(decisionsTabTone(0)).toBe('queue')
  })
})
