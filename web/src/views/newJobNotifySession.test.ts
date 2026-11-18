import { describe, expect, it } from 'vitest'

function raw(value: object): string {
  return Object.values(value)[0] as string
}

const newJob = raw(import.meta.glob('./NewJob.vue', { eager: true, query: '?raw', import: 'default' }))
const types = raw(import.meta.glob('../api/types.ts', { eager: true, query: '?raw', import: 'default' }))
const dict = raw(import.meta.glob('../i18n/pages/newjob.ts', { eager: true, query: '?raw', import: 'default' }))

// 新建任务表单的「完成后通知」（issue gofer-j2ak）：复用已有的 notify / notify_session 字段，
// 不新增权限；下拉只列本调用方还在进行中的 ACP 会话 job。
describe('new job: notify the session when done', () => {
  it('uses the existing submit fields', () => {
    expect(types).toContain('  notify?: boolean\n  notify_session?: string')
    expect(newJob).toContain('req.notify_session = notifySession.value')
  })

  it('lists only the caller own running sessions as targets', () => {
    expect(newJob).toContain("(['awaiting_input', 'running'] as JobStatus[]).map((status) =>")
    expect(newJob).toContain('listJobs({ caller, status, limit: 50 })')
    expect(newJob).toContain('if (!job.session || seen.has(job.id)) continue')
    expect(newJob).toContain('data-test="nj-notify-session"')
  })

  it('keeps the copy in the new-job dictionary', () => {
    for (const key of ['newjob.field.notifySession', 'newjob.notify.none', 'newjob.notify.hint']) {
      expect(dict).toContain(`'${key}'`)
    }
  })
})
