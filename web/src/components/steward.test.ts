import { describe, expect, it } from 'vitest'

const read = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const panel = read(import.meta.glob('./StewardPanel.vue', { eager: true, query: '?raw', import: 'default' }))
const notes = read(import.meta.glob('./StewardNotes.vue', { eager: true, query: '?raw', import: 'default' }))
const settings = read(import.meta.glob('../views/settings/WorkSettings.vue', { eager: true, query: '?raw', import: 'default' }))
const work = read(import.meta.glob('../views/Work.vue', { eager: true, query: '?raw', import: 'default' }))

describe('Steward panel (问管家)', () => {
  it('is mounted on the work page with a floating button, quick questions and a composer', () => {
    expect(work).toContain('<StewardPanel />')
    for (const hook of ['steward-fab', 'steward-panel', 'steward-quick', 'steward-input', 'steward-send', 'steward-restart', 'steward-notes-btn', 'steward-disabled']) {
      expect(panel).toContain(`data-test="${hook}"`)
    }
    expect(panel).toContain('QUICK_QUESTIONS')
    expect(panel).toContain('管家未启用，')
    expect(panel).toContain('去设置页开启')
    expect(panel).toContain('发第一条消息会自动启动管家')
  })

  it('sends on Enter, keeps Shift+Enter for a newline and says when the steward is busy', () => {
    expect(panel).toContain("ev.key === 'Enter' && !ev.shiftKey")
    expect(panel).toContain('BUSY_TEXT')
    expect(panel).toContain('e.status === 409')
  })

  it('streams the steward session job over ACP events and resubscribes when the job changes', () => {
    expect(panel).toContain('streamACPJob(jobId')
    expect(panel).toContain('nextSubscription(subscribed.value, reported)')
    expect(panel).toContain('data-test="steward-rebuilt"')
    expect(panel).toContain('管家已重建')
  })

  it('polls the status only while open', () => {
    expect(panel).toContain('window.setInterval')
    expect(panel).toContain('stopPolling()')
    expect(panel).toContain('onUnmounted')
  })

  it('is a full-screen drawer on phones', () => {
    expect(panel).toContain('@media (max-width: 767px)')
    expect(panel).toContain('height: 100dvh')
  })
})

describe('Steward notes drawer', () => {
  it('views, edits with a version, handles a 409 conflict and browses history', () => {
    for (const hook of ['notes-edit', 'notes-textarea', 'notes-save', 'notes-conflict', 'notes-use-latest', 'notes-overwrite', 'notes-history', 'notes-edit-from']) {
      expect(notes).toContain(`data-test="${hook}"`)
    }
    expect(notes).toContain('putStewardNotes(draft.value, baseVersion.value)')
    expect(notes).toContain('notesConflictOf(e, draft.value)')
    expect(notes).toContain('MarkdownBlock')
    expect(notes).toContain('以此为基础编辑')
    expect(notes).toContain('超过 16KB')
  })
})

describe('Steward settings section', () => {
  it('has the switch, agent select, project, review time, idle end, event wake and the actions', () => {
    for (const hook of ['steward-enabled', 'steward-agent', 'steward-project', 'steward-review-time', 'steward-idle', 'steward-event-wake', 'steward-save', 'steward-start', 'steward-restart', 'steward-stop', 'steward-notes-open', 'steward-status', 'steward-switch-warning']) {
      expect(settings).toContain(`data-test="${hook}"`)
    }
    expect(settings).toContain('putConfigSteward(stewardDiff.value)')
    expect(settings).toContain('重启管家')
    expect(settings).toContain('笔记超过 8KB，下次巡检会精简')
    expect(settings).toContain('`/jobs/${encodeURIComponent(stewardStatus.job_id)}`')
  })
})

describe('Work page merge suggestions', () => {
  it('lists the steward merge suggestions with adopt / ignore', () => {
    for (const hook of ['merge-suggestions', 'merge-accept', 'merge-dismiss']) expect(work).toContain(`data-test="${hook}"`)
    expect(work).toContain('listMergeSuggestions()')
  })
})
