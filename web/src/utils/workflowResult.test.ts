import { describe, expect, it } from 'vitest'
import type { Job, Workflow, WorkflowStep } from '../api/types'
import {
  canRerunFailed,
  excerptLines,
  failedCount,
  fanNameForJob,
  fromStepValue,
  hasExcerpt,
  jobWorkflowLabel,
  overridesBuiltinNotice,
  resultRows,
  rowName,
  shadowsNote,
  templateDeleteNotice,
  templateEditGate,
  templateReadOnlyNote,
  templateSaveNotice,
} from './workflowResult'

function step(over: Partial<WorkflowStep>): WorkflowStep {
  return { step_index: 1, name: 'checks', ...over }
}

function wf(steps: WorkflowStep[], status: Workflow['status'] = 'failed'): Workflow {
  return { id: 'wf-1', status, current_step: 1, total_steps: 1, created_at: 0, updated_at: 1, steps }
}

describe('workflow result rows', () => {
  it('builds one row per step-job, showing the server runner by its display name', () => {
    const rows = resultRows(
      wf([
        step({ step_index: 1, name: 'checks', fan_index: 1, runner: 'local', exit_code: 0, duration_sec: 42, status: 'done' }),
        step({ step_index: 1, name: 'checks', fan_index: 2, runner: 'win', exit_code: 1, duration_sec: 7, status: 'failed' }),
      ]),
    )
    expect(rows).toHaveLength(2)
    expect(rows[0]).toMatchObject({
      stepLabel: '1 · checks',
      tag: 'f1',
      runner: 'server',
      exitCode: '0',
      duration: '42s',
      failed: false,
    })
    expect(rows[1]).toMatchObject({ tag: 'f2', runner: 'win', exitCode: '1', failed: true })
  })

  it('leaves the exit code empty when the row never ran, and marks retries', () => {
    const rows = resultRows(wf([step({ attempt: 2, runner: 'local', status: 'timeout' })]))
    expect(rows[0].tag).toBe('a2')
    expect(rows[0].exitCode).toBe('')
    expect(rows[0].duration).toBe('—')
    expect(rows[0].failed).toBe(true)
  })

  it('folds the failure excerpt into lines only for failed / cancelled / timed-out rows', () => {
    const failed = resultRows(wf([step({ status: 'failed', failure_excerpt: '--- FAIL: TestX\n\nFAIL\tpkg\t0.1s\n' })]))
    expect(failed[0].excerpt).toEqual(['--- FAIL: TestX', 'FAIL\tpkg\t0.1s'])
    expect(hasExcerpt(failed[0])).toBe(true)

    const done = resultRows(wf([step({ status: 'done', failure_excerpt: 'noise' })]))
    expect(done[0].excerpt).toEqual([])
    expect(hasExcerpt(done[0])).toBe(false)
  })

  it('splits excerpts on CRLF too and drops blank lines', () => {
    expect(excerptLines('a\r\n\r\nb  \r\n')).toEqual(['a', 'b  '.trimEnd()])
    expect(excerptLines(undefined)).toEqual([])
    expect(excerptLines('')).toEqual([])
  })

  it('counts failures and only reruns the failed ones of a finished workflow', () => {
    const finished = wf([step({ status: 'failed' }), step({ step_index: 2, status: 'done' })])
    expect(failedCount(resultRows(finished))).toBe(1)
    expect(canRerunFailed(finished)).toBe(true)
    // 全成功 / 还在跑 / 无记录都不能「只重跑失败的」
    expect(canRerunFailed(wf([step({ status: 'done' })], 'done'))).toBe(false)
    expect(canRerunFailed(wf([step({ status: 'failed' })], 'running'))).toBe(false)
    expect(canRerunFailed(null)).toBe(false)
  })

  it('reruns from a step by its 1-based number', () => {
    const row = resultRows(wf([step({ step_index: 3 })]))[0]
    expect(fromStepValue(row)).toBe('3')
  })

  // gofer-j5xc: 扇出的每一路显示该路 fan 自己声明的 name，以前每路都只显示步骤名。
  it('shows the fan name of each fan row, falling back to the step name', () => {
    const rows = resultRows(
      wf([
        step({ step_index: 2, name: 'checks', fan_index: 1, fan_name: 'linux-race', status: 'done' }),
        step({ step_index: 2, name: 'checks', fan_index: 2, fan_name: 'web', status: 'failed' }),
        step({ step_index: 3, name: 'report', status: 'done' }),
      ]),
    )
    expect(rows.map((r) => r.stepLabel)).toEqual(['2 · checks · linux-race', '2 · checks · web', '3 · report'])
    // fan 徽标照旧（f1 / f2，非 fan 行没有）
    expect(rows.map((r) => r.tag)).toEqual(['f1', 'f2', ''])
  })

  it('does not repeat a fan name that equals the step name', () => {
    expect(rowName(step({ step_index: 1, fan_index: 1, fan_name: 'checks' }))).toBe('checks')
    expect(rowName(step({ step_index: 1, fan_index: 1 }))).toBe('checks')
  })

  // gofer-plkx: 每一行带上它实际跑在哪（cwd、ref）与何时开始。
  it('carries the run directory, ref and start time of each row', () => {
    const rows = resultRows(
      wf([
        step({ step_index: 1, status: 'done', cwd: '/tmp/ref-checkout', ref: 'abc123def4567890', started_at: 1_700_000_000 }),
        step({ step_index: 2 }),
      ]),
    )
    expect(rows[0].cwd).toBe('/tmp/ref-checkout')
    expect(rows[0].ref).toBe('abc123def4567890')
    expect(rows[0].startedAt).not.toBe('')
    // 未起的行没有工作目录 / 开始时间
    expect(rows[1].cwd).toBe('')
    expect(rows[1].startedAt).toBe('')
  })
})

describe('job ↔ workflow association', () => {
  function job(over: Partial<Job>): Job {
    return { id: 'job-1', status: 'done', ...over } as Job
  }

  it('says where a step-job sits in its workflow', () => {
    expect(jobWorkflowLabel(job({ workflow_id: 'wf-1', step_index: 2, fan_index: 1 }), 'linux-race')).toBe(
      '第 2 步 · 第 1 路（linux-race）',
    )
    expect(jobWorkflowLabel(job({ workflow_id: 'wf-1', step_index: 3 }))).toBe('第 3 步')
    // 重试的 step-job 带上尝试序号
    expect(jobWorkflowLabel(job({ workflow_id: 'wf-1', step_index: 3, attempt: 2 }))).toBe('第 3 步 · 第 2 次尝试')
    // 普通 job 没有工作流关联
    expect(jobWorkflowLabel(job({}))).toBe('')
  })

  it('finds the fan name of the row that matches the job step/attempt/fan', () => {
    const steps: WorkflowStep[] = [
      { step_index: 2, fan_index: 1, fan_name: 'linux-race' },
      { step_index: 2, fan_index: 2, fan_name: 'web' },
    ]
    expect(fanNameForJob(steps, { step_index: 2, fan_index: 2 })).toBe('web')
    expect(fanNameForJob(steps, { step_index: 2 })).toBe('')
    expect(fanNameForJob(undefined, { step_index: 2, fan_index: 1 })).toBe('')
  })
})

describe('workflow template editing gates and notices', () => {
  it('lets a config.write holder edit, and explains the read-only case otherwise', () => {
    expect(templateEditGate(true, true)).toEqual({ editable: true, reason: '' })
    // whoami 还没拿到：不预先隐藏（服务端会拒）
    expect(templateEditGate(false, false).editable).toBe(true)
    const gated = templateEditGate(false, true)
    expect(gated.editable).toBe(false)
    expect(gated.reason).toContain('config.write')
  })

  it('says a builtin template can only be viewed, and warns before overriding it', () => {
    expect(templateReadOnlyNote('builtin')).toContain('只能查看')
    expect(templateReadOnlyNote('global')).toBe('')
    expect(overridesBuiltinNotice('release', 'builtin', 'project')).toBe('将覆盖内置模板 release（保存到当前项目）')
    expect(overridesBuiltinNotice('release', 'global', 'global')).toBe('')
    expect(overridesBuiltinNotice('', 'builtin', 'global')).toBe('')
  })

  it('explains which layers a template shadows', () => {
    expect(shadowsNote(['global', 'builtin'])).toBe('覆盖了 全局 / 内置的模板')
    expect(shadowsNote(['builtin'])).toBe('覆盖了 内置的模板')
    expect(shadowsNote(undefined)).toBe('')
  })

  it('carries the uncommitted warning and the commit command for a project template', () => {
    const n = templateSaveNotice({
      name: 'verify',
      scope: 'project',
      path: '/repo/.gofer/workflows/verify.yaml',
      uncommitted: true,
      commit_command: 'git add .gofer/workflows/verify.yaml && git commit -m "chore"',
    })
    expect(n.tone).toBe('warn')
    expect(n.text).toContain('提交到仓库才算数')
    expect(n.text).toContain('verify.yaml')
    expect(n.command).toContain('git add')
  })

  it('answers a global save plainly and says where a deleted template falls back to', () => {
    const n = templateSaveNotice({ name: 'g', scope: 'global', path: '/cfg/workflows/g.yaml' })
    expect(n.tone).toBe('ok')
    expect(n.command).toBe('')
    expect(templateDeleteNotice({ name: 'g', scope: 'project', falls_back_to: 'builtin' })).toContain('回落到内置模板')
    expect(templateDeleteNotice({ name: 'g', scope: 'global' })).toContain('已不存在')
  })
})
