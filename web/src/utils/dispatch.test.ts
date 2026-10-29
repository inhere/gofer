import { describe, expect, it } from 'vitest'
import type { AgentSession, TrackerRepo } from '../api/types'
import type { AuthWhoami } from './permissions'
import {
  buildDispatchRequest,
  buildNewIssueRequest,
  canManagePlan,
  canReplySession,
  dispatchErrorText,
  dispatchVisibility,
  EMPTY_DISPATCH_FORM,
  isPendingSync,
  issueRepos,
  linkJobAfterSubmit,
  newJobLink,
  newJobPrefill,
  pendingSyncCount,
  readFromItemQuery,
  targetRows,
} from './dispatch'

const f = (over: Partial<typeof EMPTY_DISPATCH_FORM>) => ({ ...EMPTY_DISPATCH_FORM, ...over })
const repo = (over: Partial<TrackerRepo>): TrackerRepo => ({
  tracker_id: 'r1', project_key: 'self', rel_path: '', prefix: 'zz', last_sync_at: 0, sync_summary: '', ...over,
})
const who = (perms: string[], root = false): AuthWhoami =>
  ({ kind: 'user', caller_id: 'me', root, permissions: perms.map((name) => ({ name, source: 'db' })) }) as unknown as AuthWhoami

describe('dispatch request per branch', () => {
  it('session needs a session id and sends only it', () => {
    expect(buildDispatchRequest('session', f({})).req).toBeNull()
    expect(buildDispatchRequest('session', f({ sessionId: ' s1 ', jobId: 'j' })).req).toEqual({ to: 'session', session_id: 's1' })
  })
  it('job needs a submitted job id', () => {
    expect(buildDispatchRequest('job', f({})).error).toContain('先提交')
    expect(buildDispatchRequest('job', f({ jobId: 'j1' })).req).toEqual({ to: 'job', job_id: 'j1' })
  })
  it('todo: existing plan wins over a new title; both optional', () => {
    expect(buildDispatchRequest('todo', f({ planId: 'p1', newPlanTitle: 'x' })).req).toEqual({ to: 'todo', plan_id: 'p1' })
    expect(buildDispatchRequest('todo', f({ newPlanTitle: ' 新 plan ' })).req).toEqual({ to: 'todo', new_plan_title: '新 plan' })
    expect(buildDispatchRequest('todo', f({})).req).toEqual({ to: 'todo' })
  })
  it('issue needs a tracker, title is optional', () => {
    expect(buildDispatchRequest('issue', f({})).req).toBeNull()
    expect(buildDispatchRequest('issue', f({ trackerId: 'r1' })).req).toEqual({ to: 'issue', tracker_id: 'r1' })
    expect(buildDispatchRequest('issue', f({ trackerId: 'r1', issueTitle: ' T ' })).req).toEqual({ to: 'issue', tracker_id: 'r1', title: 'T' })
  })
  it('archive and drop carry nothing else', () => {
    expect(buildDispatchRequest('archive', f({ sessionId: 's' })).req).toEqual({ to: 'archive' })
    expect(buildDispatchRequest('drop', f({})).req).toEqual({ to: 'drop' })
  })
})

describe('recommended targets', () => {
  const s = (over: Partial<AgentSession>) => ({ session_id: 'abcdef123456', agent: 'claude', state: 'idle', ...over }) as AgentSession
  it('renders hit labels and marks inferred ones', () => {
    const [r] = targetRows([
      s({
        topic: '修登录', topic_source: 'inferred', maybe_offline: true, project_key: 'self',
        hits: [{ kind: 'topic', match: '登录', source: 'inferred' }, { kind: 'project', match: 'self' }, { kind: 'scope', match: 'internal/job', source: 'self' }],
      }),
    ])
    expect(r.title).toBe('abcdef12')
    expect(r.topicInferred).toBe(true)
    expect(r.hits.map((h) => h.text)).toEqual(['主题命中（推断）：登录', '项目：self', '范围命中：internal/job'])
    expect(r.hits.map((h) => h.inferred)).toEqual([true, false, false])
    expect(r.maybeOffline).toBe(true)
    expect(r.offlineLabel).toBe('可能不在线')
  })
  it('handles an empty or missing list', () => {
    expect(targetRows(undefined)).toEqual([])
    expect(targetRows([s({})])[0].hits).toEqual([])
  })
})

describe('409 handling', () => {
  it('uses the server detail when a session refuses', () => {
    const v = dispatchErrorText('session', { status: 409, message: 'conflict', detail: '会话已结束，无法送达' })
    expect(v.text).toBe('会话已结束，无法送达')
    expect(v.refresh).toBe(false)
  })
  it('todo already converted names the todo and plan', () => {
    const v = dispatchErrorText('todo', { status: 409, message: 'x', body: { todo_id: 't1', plan_id: 'p1' } })
    expect(v.text).toBe('已经转过了：todo t1（plan p1）')
    expect(v.refresh).toBe(true)
  })
  it('issue without a prefix keeps the box and shows the detail', () => {
    const v = dispatchErrorText('issue', { status: 409, message: 'no prefix', detail: '先在仓库里 gofer repo sync' })
    expect(v).toEqual({ text: '先在仓库里 gofer repo sync', refresh: false })
  })
  it('not captured any more asks for a refresh; other errors keep message', () => {
    expect(dispatchErrorText('archive', { status: 409, message: 'item is not captured' })).toEqual({ text: 'item is not captured', refresh: true })
    expect(dispatchErrorText('archive', { status: 500, message: 'boom', detail: 'db' }).text).toBe('boom：db')
  })
})

describe('permission-based hiding', () => {
  it('hides new job without job.submit; shows everything while whoami is unknown', () => {
    expect(dispatchVisibility(who(['job.read.others'])).job).toBe(false)
    expect(dispatchVisibility(who(['job.submit'])).job).toBe(true)
    expect(dispatchVisibility(null).job).toBe(true)
    expect(dispatchVisibility(who([], true)).job).toBe(true)
  })
  it("others' sessions and plans need the .others permission", () => {
    expect(canReplySession(who([]), { caller_id: 'other' })).toBe(false)
    expect(canReplySession(who([]), { caller_id: 'me' })).toBe(true)
    expect(canReplySession(who(['session.reply.others']), { caller_id: 'other' })).toBe(true)
    expect(canManagePlan(who([]), 'other')).toBe(false)
    expect(canManagePlan(who(['plan.manage.others']), 'other')).toBe(true)
    expect(canManagePlan(who([]), 'me')).toBe(true)
  })
})

describe('new job from an item', () => {
  it('prefills prompt from title + goal, project, and a relative cwd only', () => {
    expect(newJobPrefill({ title: '修登录', goal: '500 错误', project_key: 'self', workspace: 'svc/auth' })).toEqual({
      prompt: '修登录\n\n500 错误', project: 'self', cwd: 'svc/auth',
    })
    expect(newJobPrefill({ title: '只有标题', goal: '只有标题', workspace: '/abs/path' })).toEqual({ prompt: '只有标题', project: '', cwd: '' })
    expect(newJobPrefill({ title: '', goal: '只有目标' }).prompt).toBe('只有目标')
    const text = '第一行\n\n' + '完整中文正文'.repeat(200)
    expect(newJobPrefill({ title: '摘要', goal: '', capture_text: text }).prompt).toBe(text)
  })
  it('reads the query and builds the link', () => {
    expect(readFromItemQuery(['w-1', 'x'])).toBe('w-1')
    expect(readFromItemQuery(undefined)).toBe('')
    expect(newJobLink('w 1')).toBe('/new?from_item=w%201')
  })
  it('links the job after submit; a failure keeps the job id for a retry', async () => {
    const calls: unknown[] = []
    const ok = await linkJobAfterSubmit(async (r) => void calls.push(r), 'job-1')
    expect(ok).toEqual({ linked: true, error: '' })
    expect(calls).toEqual([{ to: 'job', job_id: 'job-1' }])
    const bad = await linkJobAfterSubmit(async () => { throw Object.assign(new Error('conflict'), { status: 409, detail: '条目已不在收集箱' }) }, 'job-1')
    expect(bad.linked).toBe(false)
    expect(bad.error).toBe('条目已不在收集箱')
    expect((await linkJobAfterSubmit(async () => undefined, '')).linked).toBe(false)
  })
})

describe('issue repos and form', () => {
  const repos = [repo({ tracker_id: 'a' }), repo({ tracker_id: 'b', prefix: '' }), repo({ tracker_id: 'c', prefix: ' ' })]
  it('keeps only repos with an id prefix', () => {
    expect(issueRepos(repos).map((r) => r.tracker_id)).toEqual(['a'])
    expect(issueRepos(undefined)).toEqual([])
  })
  const form = (over = {}) => ({ trackerId: 'a', title: ' 标题 ', description: '', priority: '2', type: 'task', ...over })
  it('validates and builds the create body', () => {
    expect(buildNewIssueRequest(form(), repos).req).toEqual({ title: '标题', priority: 2, type: 'task' })
    expect(buildNewIssueRequest(form({ description: ' d ', priority: '0', type: 'bug' }), repos).req).toEqual({ title: '标题', description: 'd', priority: 0, type: 'bug' })
    expect(buildNewIssueRequest(form({ type: 'weird' }), repos).req?.type).toBe('task')
  })
  it('rejects a missing repo, a repo without prefix, an empty title and a bad priority', () => {
    expect(buildNewIssueRequest(form({ trackerId: '' }), repos).error).toContain('仓库')
    expect(buildNewIssueRequest(form({ trackerId: 'b' }), repos).error).toContain('前缀')
    expect(buildNewIssueRequest(form({ title: '  ' }), repos).error).toContain('标题')
    expect(buildNewIssueRequest(form({ title: 'x'.repeat(301) }), repos).error).toContain('300')
    expect(buildNewIssueRequest(form({ priority: '5' }), repos).error).toContain('0–4')
  })
})

describe('pending sync display rule', () => {
  it('shows only for issue links with pending_sync', () => {
    expect(isPendingSync({ kind: 'issue', pending_sync: true })).toBe(true)
    expect(isPendingSync({ kind: 'issue' })).toBe(false)
    expect(isPendingSync({ kind: 'issue', pending_sync: false })).toBe(false)
    expect(isPendingSync({ kind: 'todo', pending_sync: true })).toBe(false)
    expect(pendingSyncCount([{ kind: 'issue', pending_sync: true }, { kind: 'job' }, { kind: 'issue' }])).toBe(1)
    expect(pendingSyncCount(undefined)).toBe(0)
  })
})
