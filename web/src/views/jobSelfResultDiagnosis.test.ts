// job 详情：自报终态（计划 R9）与诊断面板（计划 R10）—— 计划 R12 的 web 部分。
//
// 仓库没有组件挂载库（无 @vue/test-utils），所以按既有约定分两层：解析与判定在
// utils/jobSelfResult.ts、utils/jobDiagnosis.ts（纯函数用例），组件与详情页的接线
// （渲染哪些段落、调哪个接口）用 ?raw 读源码断言。
import { describe, expect, it } from 'vitest'
import type { Job, JobDiagnosis } from '../api/types'
import { parseSelfReport, selfDecisionLabel, selfResultView } from '../utils/jobSelfResult'
import { diagnosisChainCount, diagnosisErrorText, diagnosisIsFirstFailure, diagnosisRelationLabel } from '../utils/jobDiagnosis'

function raw(modules: Record<string, unknown>): string {
  return Object.values(modules)[0] as string
}

const selfResult = raw(import.meta.glob('../components/JobSelfResult.vue', { eager: true, query: '?raw', import: 'default' }))
const diagnosis = raw(import.meta.glob('../components/JobDiagnosis.vue', { eager: true, query: '?raw', import: 'default' }))
const detail = raw(import.meta.glob('./JobDetail.vue', { eager: true, query: '?raw', import: 'default' }))
const client = raw(import.meta.glob('../api/client.ts', { eager: true, query: '?raw', import: 'default' }))

const job = (patch: Partial<Job> = {}): Job => ({
  id: 'j1',
  project_key: 'p',
  agent: 'suga',
  runner: 'local',
  status: 'done',
  exit_code: 0,
  cwd: '/w',
  result_dir: '/w/.gofer',
  started_at: 1_700_000_000,
  ...patch,
})

const diag = (patch: Partial<JobDiagnosis> = {}): JobDiagnosis => ({
  job_id: 'j2',
  status: 'failed',
  finished: true,
  termination_reason: 'transient',
  termination_label: '瞬时失败',
  chain: [
    { job_id: 'j1', relation: 'target', status: 'failed', termination_reason: 'transient', error: 'provider 529' },
    { job_id: 'j2', relation: 'resume', status: 'failed', termination_reason: 'stalled', error: 'no output' },
  ],
  first_failure: { job_id: 'j1', relation: 'target', status: 'failed', termination_reason: 'transient' },
  timeline: [],
  evidence: [],
  unknowns: [],
  next_steps: [],
  ...patch,
})

describe('self-reported result', () => {
  it('shows nothing when the job never reported', () => {
    expect(selfResultView(job())).toBeNull()
    expect(selfResultView(null)).toBeNull()
  })

  it('reads the decision, the reason and the report document', () => {
    const view = selfResultView(
      job({
        result_decision: 'human',
        result_reason: 'needs a product call',
        result_attempt: 2,
        result_report_json: JSON.stringify({
          decision: 'human',
          reason: 'needs a product call',
          action_required: 'pick option A or B',
          findings: ['A is cheaper', '  ', 'B keeps the old behaviour'],
        }),
      }),
    )
    expect(view).not.toBeNull()
    expect(view?.decision).toBe('human')
    expect(view?.label).toBe('需人决定')
    expect(view?.reason).toBe('needs a product call')
    expect(view?.actionRequired).toBe('pick option A or B')
    // 空白项被丢掉，其余原样保留。
    expect(view?.findings).toEqual(['A is cheaper', 'B keeps the old behaviour'])
  })

  it('falls back to the row when the report document is unreadable', () => {
    const view = selfResultView(job({ result_decision: 'rework', result_reason: 'missing tests', result_report_json: '{not json' }))
    expect(view?.reason).toBe('missing tests')
    expect(view?.findings).toEqual([])
    expect(view?.actionRequired).toBeUndefined()
  })

  it('reads the fields out of the report document on its own', () => {
    expect(parseSelfReport(undefined)).toEqual({})
    expect(parseSelfReport('{"action_required":"do x","findings":["f"]}')).toEqual({ action_required: 'do x', findings: ['f'] })
    expect(parseSelfReport('nope')).toEqual({})
  })

  it('labels all four decisions and keeps an unknown one readable', () => {
    expect(selfDecisionLabel('pass')).toBe('通过')
    expect(selfDecisionLabel('rework')).toBe('需返工')
    expect(selfDecisionLabel('fail')).toBe('失败')
    expect(selfDecisionLabel('human')).toBe('需人决定')
    expect(selfDecisionLabel('blocked')).toBe('blocked')
  })
})

describe('self-reported result panel wiring', () => {
  it('renders the badge, the reason, the action and the findings', () => {
    expect(selfResult).toContain('data-test="job-self-result"')
    expect(selfResult).toContain('data-test="job-self-result-decision"')
    expect(selfResult).toContain('data-test="job-self-result-reason"')
    expect(selfResult).toContain('data-test="job-self-result-action"')
    expect(selfResult).toContain('data-test="job-self-result-findings"')
    expect(selfResult).toContain('v-for="(f, i) in view.findings"')
    // 自报只展示，说明里点明不改任务状态；组件里没有任何写接口。
    expect(selfResult).toContain("t('jobs.selfResult.note')")
    expect(selfResult).not.toContain('submitSelfResult')
  })

  it('sits in the job overview and not in the other tabs', () => {
    expect(detail).toContain('<JobSelfResult v-if="active === \'overview\' && job" :job="job" />')
    expect(detail).toContain("import JobSelfResult from '../components/JobSelfResult.vue'")
  })
})

describe('job diagnosis panel', () => {
  it('labels the chain relations and finds the earliest failure', () => {
    expect(diagnosisRelationLabel('target')).toBe('本次')
    expect(diagnosisRelationLabel('resume')).toBe('续跑')
    expect(diagnosisRelationLabel('source')).toBe('来源')
    expect(diagnosisRelationLabel('failover')).toBe('切换')
    expect(diagnosisRelationLabel('retry')).toBe('重试')
    expect(diagnosisRelationLabel('rewritten')).toBe('rewritten')

    const d = diag()
    expect(diagnosisChainCount(d)).toBe(2)
    expect(diagnosisChainCount(null)).toBe(0)
    expect(diagnosisIsFirstFailure(d, d.chain[0])).toBe(true)
    expect(diagnosisIsFirstFailure(d, d.chain[1])).toBe(false)
    expect(diagnosisIsFirstFailure(null, d.chain[0])).toBe(false)
  })

  it('keeps the server text when the request fails', () => {
    expect(diagnosisErrorText(new Error('unknown job'))).toBe('诊断失败：unknown job')
    expect(diagnosisErrorText('plain')).toBe('诊断失败：plain')
  })

  it('renders termination, chain, timeline, evidence, unknowns and next steps', () => {
    expect(diagnosis).toContain('data-test="job-diagnosis"')
    expect(diagnosis).toContain('data-test="job-diagnosis-reason"')
    expect(diagnosis).toContain('data-test="job-diagnosis-chain"')
    expect(diagnosis).toContain('data-test="job-diagnosis-timeline"')
    expect(diagnosis).toContain('data-test="job-diagnosis-evidence"')
    expect(diagnosis).toContain('data-test="job-diagnosis-unknowns"')
    expect(diagnosis).toContain('data-test="job-diagnosis-next-steps"')
    expect(diagnosis).toContain('diagnosisIsFirstFailure(diag, link)')
  })

  it('shows the stage note instead of a guessed time when a stage has no record', () => {
    expect(diagnosis).toContain('v-if="st.present"')
    expect(diagnosis).toContain('v-else class="diag-dim">{{ st.note }}')
  })

  it('keeps a chain of one (no continuation) out of the panel', () => {
    expect(diagnosis).toContain('v-if="diagnosisChainCount(diag) > 1"')
  })

  it('is a button that loads on open and reports its own failure', () => {
    expect(diagnosis).toContain('data-test="job-diagnose"')
    expect(diagnosis).toContain('data-test="job-diagnosis-loading"')
    expect(diagnosis).toContain('data-test="job-diagnosis-error"')
    expect(diagnosis).toContain('await load()')
    expect(diagnosis).toContain('getJobDiagnosis(props.jobId)')
  })

  it('is mounted in the job detail header and calls the diagnosis endpoint', () => {
    expect(detail).toContain('<JobDiagnosis v-if="job" :job-id="job.id" />')
    expect(detail).toContain("import JobDiagnosis from '../components/JobDiagnosis.vue'")
    expect(client).toContain('export function getJobDiagnosis(')
    expect(client).toContain('/diagnosis`')
  })
})
