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
