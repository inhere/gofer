import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { eventLabel } from './eventMeta'

describe('job detail on narrow screens', () => {
  it('uses one metadata column and wraps long values at the existing breakpoint', () => {
    const source = readFileSync(new URL('../views/JobDetail.vue', import.meta.url), 'utf8')
    expect(source).toMatch(/@media \(max-width: 640px\) \{[\s\S]*?\.meta \{\s*grid-template-columns: minmax\(0, 1fr\)/)
    expect(source).toMatch(/@media \(max-width: 640px\) \{[\s\S]*?\.meta-v \{[^}]*overflow-wrap: anywhere/)
  })

  it('shows Chinese labels for job timeline events', () => {
    for (const type of [
      'job.title_changed', 'job.needs_review', 'job.reviewed',
      'job.verify_started', 'job.verify_finished', 'job.auto_resumed',
      'job.retry_scheduled', 'job.retry_started', 'job.retry_exhausted',
      'job.wakeup_fired', 'job.wakeup_failed',
    ]) {
      expect(eventLabel(type), type).not.toBe(type)
    }
  })
})
