// 状态显示映射的纯函数断言：接口值不变，界面显示中文（用户 2026-10-19 的口径）。
import { describe, expect, it } from 'vitest'
import {
  ISSUE_STATUS_VALUES,
  JOB_STATUS_VALUES,
  PLAN_STATUS_VALUES,
  WORKFLOW_STATUS_VALUES,
  issueStatusText,
  jobStatusOptions,
  jobStatusTitle,
  jobStatusText,
  planStatusOptions,
  planStatusText,
  statusText,
  workflowStatusOptions,
  workflowStatusText,
} from './statusText'

describe('statusText 状态映射', () => {
  it('plan 的四个状态显示中文', () => {
    expect(planStatusText('open')).toBe('进行中')
    expect(planStatusText('done')).toBe('已完成')
    expect(planStatusText('archived')).toBe('已归档')
    expect(planStatusText('blocked')).toBe('受阻')
  })

  it('workflow / job 的运行态显示中文', () => {
    expect(workflowStatusText('running')).toBe('运行中')
    expect(workflowStatusText('done')).toBe('已完成')
    expect(workflowStatusText('failed')).toBe('失败')
    expect(workflowStatusText('cancelled')).toBe('已取消')
    expect(jobStatusText('queued')).toBe('排队中')
    expect(jobStatusText('timeout')).toBe('超时')
    expect(jobStatusText('rejected')).toBe('已拒绝')
  })

  it('端人等的状态保留原来的符号前缀（符号本身就是信号）', () => {
    expect(statusText('pending_interaction')).toBe('⚠ 待应答')
    expect(statusText('needs_review')).toBe('⚠ 待验收')
    expect(statusText('waiting_dir')).toBe('⏳ 等目录')
    expect(statusText('awaiting_approval')).toBe('⏸ 待批准')
    expect(statusText('paused_for_update')).toBe('⏳ 等更新')
  })

  it('issue 的状态显示中文', () => {
    expect(issueStatusText('open')).toBe('进行中')
    expect(issueStatusText('in_progress')).toBe('处理中')
    expect(issueStatusText('blocked')).toBe('受阻')
    expect(issueStatusText('closed')).toBe('已关闭')
  })

  it('没登记的状态原样显示，不变成空白', () => {
    expect(statusText('some_new_status')).toBe('some_new_status')
    expect(statusText('')).toBe('')
    expect(statusText(undefined)).toBe('')
  })

  it('筛选选项：值与中文文案成对，「全部」排第一', () => {
    const plans = planStatusOptions()
    expect(plans[0]).toEqual({ value: '', label: '全部' })
    expect(plans.map((o) => o.value)).toEqual(['', ...PLAN_STATUS_VALUES])
    expect(plans.find((o) => o.value === 'open')?.label).toBe('进行中')
    expect(plans.every((o) => o.value === '' || !/[a-z]/.test(o.label))).toBe(true)

    const wfs = workflowStatusOptions()
    expect(wfs.map((o) => o.value)).toEqual(['', ...WORKFLOW_STATUS_VALUES])

    const jobs = jobStatusOptions()
    expect(jobs.map((o) => o.value)).toEqual(['', ...JOB_STATUS_VALUES])
    // job 的筛选芯片不能有英文原值（'queued' → '排队中'）。
    expect(jobs.some((o) => o.label === o.value && o.value !== '')).toBe(false)
  })

  it('issue 状态值集合与页面筛选一致', () => {
    expect(ISSUE_STATUS_VALUES).toEqual(['open', 'in_progress', 'blocked', 'closed'])
  })
})

describe('jobStatusTitle', () => {
  it('names the lock holder only for waiting_dir', () => {
    expect(jobStatusTitle('waiting_dir', 'job-x')).toContain('job-x')
    expect(jobStatusTitle('waiting_dir')).toBeUndefined()
    expect(jobStatusTitle('running', 'job-x')).toBeUndefined()
  })
})
