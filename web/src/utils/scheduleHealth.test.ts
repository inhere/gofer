import { describe, expect, it } from 'vitest'
import { ApiError } from '../api/client'
import type { Schedule } from '../api/types'
import {
  NEW_SCHEDULE_HEALTH,
  countValue,
  createHealthFields,
  parseArgvLines,
  patchHealthFields,
  precheckEdited,
  scheduleApiError,
  scheduleHealthForm,
  schedulePrecheck,
} from './scheduleHealth'

function schedule(over: Partial<Schedule> = {}): Schedule {
  return {
    id: 'sch-1',
    name: 'nightly',
    cron: '0 9 * * *',
    enabled: 1,
    catch_up: 1,
    next_run_at: 0,
    last_run_at: 0,
    last_job_id: '',
    project_key: 'p',
    request: { project_key: 'p', agent: 'a', runner: 'local', cwd: '.' },
    ...over,
  }
}

describe('schedule health form', () => {
  it('reads argv one argument per line without splitting on spaces', () => {
    expect(parseArgvLines('  gh pr list --label "needs review"\n\n  echo ok  ')).toEqual([
      'gh pr list --label "needs review"',
      'echo ok',
    ])
    expect(parseArgvLines('')).toEqual([])
    expect(countValue('')).toBe(0)
    expect(countValue(' 12 ')).toBe(12)
    expect(countValue('-3')).toBe(0)
    expect(countValue('abc')).toBe(0)
  })

  it('defaults a new schedule like the server does', () => {
    expect(createHealthFields(NEW_SCHEDULE_HEALTH)).toEqual({
      overlap: 'skip',
      max_runs: 0,
      max_failures: 3,
      context: 'on',
    })
  })

  it('submits a pre-run check with its timeout and skip flag', () => {
    const fields = createHealthFields({
      ...NEW_SCHEDULE_HEALTH,
      overlap: 'allow',
      maxRuns: '20',
      maxFailures: '0',
      context: 'off',
      precheckArgv: 'git\nstatus\n--porcelain',
      precheckTimeout: '90',
      precheckSkipUnchanged: true,
    })
    expect(fields).toEqual({
      overlap: 'allow',
      max_runs: 20,
      max_failures: 0,
      context: 'off',
      precheck: { argv: ['git', 'status', '--porcelain'], timeout_sec: 90, skip_unchanged: true },
    })
    // 超时留空 = 用后端默认值，不写这个字段
    expect(schedulePrecheck({ ...NEW_SCHEDULE_HEALTH, precheckArgv: 'true' })).toEqual({
      argv: ['true'],
      skip_unchanged: false,
    })
    // 命令留空 = 没有前置检查
    expect(schedulePrecheck({ ...NEW_SCHEDULE_HEALTH, precheckArgv: '  \n ' })).toBeUndefined()
    expect(createHealthFields({ ...NEW_SCHEDULE_HEALTH, precheckArgv: '' }).precheck).toBeUndefined()
  })

  it('fills the edit form from the schedule row', () => {
    const form = scheduleHealthForm(
      schedule({
        overlap: 'allow',
        max_runs: 5,
        max_failures: 2,
        run_count: 3,
        consecutive_failures: 1,
        context: 'off',
        precheck: { argv: ['bin/check', '--json'], timeout_sec: 30, skip_unchanged: true },
      }),
    )
    expect(form).toEqual({
      overlap: 'allow',
      maxRuns: '5',
      maxFailures: '2',
      context: 'off',
      precheckArgv: 'bin/check\n--json',
      precheckTimeout: '30',
      precheckSkipUnchanged: true,
    })
    // 旧行没有这些字段时按后端默认回显
    expect(scheduleHealthForm(schedule()).overlap).toBe('allow')
    expect(scheduleHealthForm(schedule()).maxFailures).toBe('0')
    expect(scheduleHealthForm(undefined)).toEqual(NEW_SCHEDULE_HEALTH)
  })

  it('leaves the pre-run check out of PATCH unless it was edited', () => {
    const baseline = scheduleHealthForm(schedule({ precheck: { argv: ['check'] } }))
    expect(precheckEdited(baseline, baseline)).toBe(false)
    expect(patchHealthFields(baseline, baseline)).toEqual({
      overlap: 'allow',
      max_runs: 0,
      max_failures: 0,
      context: 'on',
    })
    // 改了 argv → 下发新声明
    const changed = { ...baseline, precheckArgv: 'check --strict', maxFailures: '4' }
    expect(patchHealthFields(changed, baseline)).toEqual({
      overlap: 'allow',
      max_runs: 0,
      max_failures: 4,
      context: 'on',
      precheck: { argv: ['check --strict'], skip_unchanged: false },
    })
    // 清空 → 发 null（清除不需要 schedule.precheck 权限）
    const cleared = { ...baseline, precheckArgv: '' }
    expect(patchHealthFields(cleared, baseline).precheck).toBeNull()
  })

  it('explains the two refusals this batch introduced', () => {
    const forbidden = new ApiError(403, 'forbidden')
    const conflict = new ApiError(409, 'schedule run skipped')
    expect(scheduleApiError(forbidden, { sendsPrecheck: true })).toContain('schedule.precheck')
    // 同一个 403 与前置检查无关时不改写（别的权限问题照原样报）
    expect(scheduleApiError(forbidden, { sendsPrecheck: false })).toBe('')
    expect(scheduleApiError(conflict)).toContain('跳过')
    expect(scheduleApiError(new Error('boom'))).toBe('')
  })
})
