import { describe, expect, it } from 'vitest'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const source = raw(import.meta.glob('./Runners.vue', { eager: true, query: 'raw', import: 'default' }))
const acceptDialog = raw(import.meta.glob('../components/EnrollAcceptDialog.vue', { eager: true, query: 'raw', import: 'default' }))

describe('Runners page: worker enrollment section', () => {
  it('is reachable as the #enrollments anchor and gated on worker.manage', () => {
    expect(source).toContain('id="enrollments"')
    expect(source).toContain('data-test="enrollments"')
    expect(source).toContain('v-if="enrollAllowed && !enrollDenied"')
    expect(source).toContain("const enrollAllowed = computed(() => can('worker.manage'))")
    // Today 卡片 / 顶栏提醒跳过来时补一次锚点滚动（SPA 不会自己滚）
    expect(source).toContain("route.hash === `#${ENROLL_ANCHOR}`")
    expect(source).toContain('scrollIntoView')
  })

  it('lists source, host, versions, proposed id, labels, agents and roots with a countdown', () => {
    for (const s of [
      'data-test="enroll-item"',
      'enrollTitle(e)',
      "e.source || t('runners.enroll.sourceUnknown')",
      'enrollHostLine(e)',
      'hostLine(e.host)',
      'data-test="enroll-countdown"',
      'decideCountdownText(e.decide_until, nowSec)',
      'collectCountdownText(e.collect_until, nowSec)',
      'data-test="enroll-labels"',
      'data-test="enroll-agents"',
      'data-test="enroll-roots"',
      'data-test="enroll-host"',
    ]) {
      expect(source).toContain(s)
    }
  })

  it('never renders a pairing code: the page only carries the two decide buttons', () => {
    expect(source).not.toContain('pairing_code')
    expect(source).not.toContain('pairingCode')
    expect(source).toContain('配对码不在这里（也不在任何响应里）')
    expect(source).toContain('data-test="enroll-accept"')
    expect(source).toContain('data-test="enroll-reject"')
  })

  it('shows how to enable the feature when the server reports enabled=false', () => {
    expect(source).toContain("t('runners.enroll.disabled')")
    expect(source).toContain('data-test="enroll-disabled"')
    expect(source).toContain('enrollEnabled.value = resp.enabled')
  })

  it('subscribes the invalidation topic, refetches on it and rereads after a decision', () => {
    expect(source).toContain("createLiveTopic(TOPIC_WORKER_ENROLLMENTS, { initial: false, fetch: fetchEnrollments })")
    expect(source).toContain('liveEnrollments.start()')
    expect(source).toContain('liveEnrollments.stop()')
    expect(source).toContain('await Promise.all([fetchEnrollments(), fetchRunners()])')
    expect(source).toContain('<EnrollAcceptDialog')
    expect(source).toContain('<EnrollRejectDialog')
    expect(source).toContain('@accepted="onEnrollmentAccepted"')
    expect(source).toContain('@rejected="onEnrollmentRejected"')
  })

  it('styles the section with the console tokens and stays usable on phone widths', () => {
    expect(source).toContain(".card--enroll[data-state='pending']")
    expect(source).toContain('var(--need)')
    expect(source).toContain('var(--fail)')
    expect(source).toContain('.enroll-left')
    // 沿用既有卡片/按钮类，窄屏的既有媒体查询继续生效
    expect(source).toContain('class="card card--enroll"')
    expect(source).toContain('class="btn"')
  })

  it('hands the accept dialog the projects, the clock and the locally known worker ids', () => {
    expect(source).toContain(':projects="projects"')
    expect(source).toContain(':now-sec="nowSec"')
    expect(source).toContain(':existing-worker-ids="knownWorkerIds"')
    expect(source).toContain('const knownWorkerIds = computed(() => {')
  })
})

describe('EnrollAcceptDialog on a phone', () => {
  it('becomes a bottom sheet and keeps the code fields side by side', () => {
    expect(acceptDialog).toContain('@media (max-width: 640px)')
    expect(acceptDialog).toContain('align-items: flex-end')
    expect(acceptDialog).toContain('.ea-code { display: flex;')
  })
})
