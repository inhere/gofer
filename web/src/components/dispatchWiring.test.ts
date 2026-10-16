import { describe, expect, it } from 'vitest'

const mods = import.meta.glob(
  ['./WorkDispatchDialog.vue', './WorkDrawer.vue', '../views/Work.vue', '../views/NewJob.vue', '../views/Issues.vue', '../api/client.ts'],
  { eager: true, query: '?raw', import: 'default' },
) as Record<string, string>
const src = (n: string): string => {
  const k = Object.keys(mods).find((x) => x.endsWith(n))
  if (!k) throw new Error(`missing ${n}`)
  return mods[k]
}

describe('capture box dispatch wiring', () => {
  it('the drawer opens the dialog for captured items and marks pending sync', () => {
    const v = src('WorkDrawer.vue')
    expect(v).toContain('<WorkDispatchDialog')
    expect(v).toContain('data-test="open-dispatch"')
    expect(v).toContain('v-if="isCaptured"')
    expect(v).toContain('isPendingSync(l)')
    expect(v).toContain('data-test="pending-sync"')
  })
  it('the placeholder text is gone from the capture tab', () => {
    expect(src('Work.vue')).not.toContain('随后提供')
  })
  it('the dialog builds requests, shows hits and handles errors through the util', () => {
    const v = src('WorkDispatchDialog.vue')
    expect(v).toContain('getDispatchTargets(props.item.id)')
    expect(v).toContain('buildDispatchRequest(to.value, form)')
    expect(v).toContain('dispatchErrorText(')
    expect(v).toContain('data-test="dispatch-hit"')
    expect(v).toContain('data-test="dispatch-topic-inferred"')
    expect(v).toContain('issueRepos(repos.value)')
    expect(v).toContain('canReplySession(')
    expect(v).toContain('dispatchVisibility(whoami.value)')
    for (const a of ['session', 'job', 'todo', 'issue', 'archive', 'drop']) expect(src('WorkDispatchDialog.vue')).toContain(a)
  })
  it('NewJob prefills from the item and links the job after submit with a retry', () => {
    const v = src('NewJob.vue')
    expect(v).toContain('readFromItemQuery(route.query.from_item)')
    expect(v).toContain('prefillFromItem(fromItem.value)')
    expect(v).toContain('newJobPrefill(it)')
    expect(v).toContain('linkJobAfterSubmit((req) => dispatchWorkItem(fromItem.value, req), submittedJobId.value)')
    expect(v).toContain('data-test="link-retry"')
    // submitted with the item: stay on the page so a failed link can be retried
    expect(v.indexOf('await linkSubmittedJob()')).toBeLessThan(v.indexOf("void router.push(`/jobs/${job.id}`)", v.indexOf('await linkSubmittedJob()')))
  })
  it('Issues has the create form limited to repos with a prefix', () => {
    const v = src('Issues.vue')
    expect(v).toContain('data-test="new-issue-form"')
    expect(v).toContain('issueRepos(repos.value)')
    expect(v).toContain('buildNewIssueRequest(newForm.value, repos.value)')
    expect(v).toContain('createTrackerIssue(newForm.value.trackerId, built.req)')
  })
  it('the client exposes the three routes', () => {
    const v = src('client.ts')
    expect(v).toContain('/dispatch-targets')
    expect(v).toContain('/dispatch`')
    expect(v).toContain('/v1/tracker/repos/${encodeURIComponent(trackerId)}/issues')
  })
})
