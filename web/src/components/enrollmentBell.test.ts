import { describe, expect, it } from 'vitest'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const source = raw(import.meta.glob('./EnrollmentBell.vue', { eager: true, query: 'raw', import: 'default' }))
const app = raw(import.meta.glob('../App.vue', { eager: true, query: 'raw', import: 'default' }))

describe('topbar worker-enrollment reminder', () => {
  it('renders only for worker.manage holders with a pending count', () => {
    expect(source).toContain("const allowed = computed(() => can('worker.manage'))")
    expect(source).toContain('v-if="allowed && count > 0"')
    expect(source).toContain('{{ count }}')
    expect(source).toContain('data-test="enroll-bell"')
  })

  it('jumps to the Runners「待接受」section', () => {
    expect(source).toContain(':to="ENROLLMENTS_LINK"')
    expect(app).toContain('<EnrollmentBell />')
  })

  it('counts from the protected list endpoint and follows the invalidation topic', () => {
    expect(source).toContain('await listEnrollments()')
    // 功能关闭时不该有提醒
    expect(source).toContain('count.value = resp.enabled ? resp.pending_count : 0')
    expect(source).toContain('createLiveTopic(TOPIC_WORKER_ENROLLMENTS')
    expect(source).toContain('live.start()')
    expect(source).toContain('live.stop()')
  })

  it('uses the console look (tokens, no hard-coded colours)', () => {
    expect(source).toContain('var(--fail)')
    expect(source).toContain('var(--paper)')
    expect(source).not.toMatch(/#[0-9a-fA-F]{3,6}\b/)
  })
})
