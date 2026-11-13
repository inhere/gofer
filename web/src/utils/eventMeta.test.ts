import { describe, expect, it } from 'vitest'
import { EVENT_META, eventDetailText, eventLabel } from './eventMeta'
import { setLocale } from '../store/locale'

describe('new event metadata', () => {
  it('registers fallback skips and todo completion failures', () => {
    expect(EVENT_META['job.fallback_skipped']).toBeTruthy()
    expect(EVENT_META['todo.complete_failed']).toBeTruthy()
    expect(eventDetailText({ type: 'job.fallback_skipped', detail: '{"agent":"omp","reason":"not installed"}' } as never)).toBe('omp · not installed')
    expect(eventDetailText({ type: 'todo.complete_failed', detail: '{"todo_id":"todo-1","error":"database busy"}' } as never)).toBe('todo-1 · database busy')
  })

  it('has labels in both locales', () => {
    setLocale('zh-CN')
    expect(eventLabel('job.fallback_skipped')).toBe('备用 Agent 已跳过候选')
    expect(eventLabel('todo.complete_failed')).toBe('待办完成标记失败')
    setLocale('en-US')
    expect(eventLabel('job.fallback_skipped')).toBe('Fallback candidate skipped')
    expect(eventLabel('todo.complete_failed')).toBe('Todo completion marking failed')
    setLocale('zh-CN')
  })
})

// 设计 2026-11-12 §1/§5：定时任务的跳跑 / 自动暂停 / 提交失败，以及同 worker_id 两进程互顶。
function ev(type: string, detail?: Record<string, unknown>): never {
  return { type, detail: detail ? JSON.stringify(detail) : '', at: 0 } as never
}

describe('schedule health and worker replacement events', () => {
  it('labels the four events in both locales', () => {
    setLocale('zh-CN')
    expect(eventLabel('schedule.run_skipped')).toBe('本轮跳过')
    expect(eventLabel('schedule.paused')).toBe('已自动暂停')
    expect(eventLabel('schedule.run_failed')).toBe('本轮提交失败')
    expect(eventLabel('worker.replaced')).toBe('worker 被顶替')
    setLocale('en-US')
    expect(eventLabel('schedule.run_skipped')).toBe('Run skipped')
    expect(eventLabel('schedule.paused')).toBe('Paused automatically')
    expect(eventLabel('schedule.run_failed')).toBe('Run could not be submitted')
    expect(eventLabel('worker.replaced')).toBe('Worker displaced')
    // 图标也要登记，否则时间线只剩默认圆点
    expect(EVENT_META['schedule.run_skipped']).toBeTruthy()
    expect(EVENT_META['schedule.paused']).toBeTruthy()
    expect(EVENT_META['schedule.run_failed']).toBeTruthy()
    expect(EVENT_META['worker.replaced']).toBeTruthy()
    setLocale('zh-CN')
  })

  it('explains why a run was skipped', () => {
    setLocale('zh-CN')
    expect(eventDetailText(ev('schedule.run_skipped', { reason: 'previous_running', job_id: 'j-1' }))).toBe(
      '上一轮还在跑 · j-1',
    )
    expect(eventDetailText(ev('schedule.run_skipped', { reason: 'precheck' }))).toBe('前置检查要求跳过')
    expect(eventDetailText(ev('schedule.run_skipped', { reason: 'unchanged' }))).toBe('前置检查输出与上次相同')
    expect(eventDetailText(ev('schedule.run_skipped', { reason: 'check_running' }))).toBe('前置检查还在跑')
    expect(eventDetailText(ev('schedule.run_skipped', { reason: 'server_draining' }))).toBe('server 正在停机')
    // 服务端新增的原因不至于变成空白
    expect(eventDetailText(ev('schedule.run_skipped', { reason: 'brand_new' }))).toBe('brand_new')
  })

  it('says why gofer paused the timer', () => {
    setLocale('zh-CN')
    expect(eventDetailText(ev('schedule.paused', { reason: 'failures', failures: 3, last_job_id: 'j-9' }))).toBe(
      '连续失败 3 次 · j-9',
    )
    expect(eventDetailText(ev('schedule.paused', { reason: 'max_runs', max_runs: 10 }))).toBe('已跑满上限 10 次')
  })

  it('carries the reason of a refused submission', () => {
    setLocale('zh-CN')
    expect(eventDetailText(ev('schedule.run_failed', { reason: 'precheck', error: 'exit 1' }))).toBe('precheck · exit 1')
  })

  it('shows which instance took the worker over', () => {
    setLocale('zh-CN')
    expect(
      eventDetailText(ev('worker.replaced', { old_instance: 'inst-a', new_instance: 'inst-b', new_remote: '10.0.0.2:2' })),
    ).toBe('inst-a → inst-b · 10.0.0.2:2')
    expect(eventDetailText(ev('worker.replaced', { old_instance: 'a', new_instance: 'b', suppressed: 2 }))).toBe(
      'a → b（另 2 次被限频合并）',
    )
  })
})
