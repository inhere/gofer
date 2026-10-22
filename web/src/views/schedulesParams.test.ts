// 定时任务参数与编辑（设计 2026-10-22 §D5）：有参数的任务「立即执行」弹表单——按类型
// 生成控件、预填默认值、底部预览将设置的环境变量；无参数的任务保持一键执行。编辑弹窗
// 里 cron / 标题 / 参数声明 / 启停都在。
import { describe, expect, it } from 'vitest'
import { boolValue, envPreview, hasParams, paramControl, paramDefaults } from '../utils/scheduleParams'
import type { ScheduleParam } from '../api/types'

const source = Object.values(
  import.meta.glob('./Schedules.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

const params: ScheduleParam[] = [
  { name: 'CLEAN_DAYS', type: 'int', default: '7', desc: '保留天数' },
  { name: 'CLEAN_APPLY', type: 'bool', default: '0', desc: '打开即真删' },
  { name: 'CLEAN_ONLY', type: 'choice', default: '', choices: ['', 'a', 'b'], desc: '只清某个仓库' },
  { name: 'CLEAN_NOTE', default: 'hi' },
]

describe('定时任务参数表单辅助', () => {
  it('按声明类型选控件：int 数字框、bool 开关、choice 下拉、string 文本', () => {
    expect(paramControl(params[0])).toBe('number')
    expect(paramControl(params[1])).toBe('toggle')
    expect(paramControl(params[2])).toBe('select')
    expect(paramControl(params[3])).toBe('text')
    // 没写 type 的按 string 处理（与后端 TypeOrString 同一口径）
    expect(paramControl({ name: 'X' })).toBe('text')
  })

  it('表单初始值取默认值；没有参数的任务不需要表单', () => {
    expect(paramDefaults(params)).toEqual({
      CLEAN_DAYS: '7',
      CLEAN_APPLY: '0',
      CLEAN_ONLY: '',
      CLEAN_NOTE: 'hi',
    })
    expect(paramDefaults(undefined)).toEqual({})
    expect(hasParams(undefined)).toBe(false)
    expect(hasParams([])).toBe(false)
    expect(hasParams(params)).toBe(true)
  })

  it('开关换成 env 值 "1"/"0"', () => {
    expect(boolValue(true)).toBe('1')
    expect(boolValue(false)).toBe('0')
  })

  it('预览按声明顺序列出 NAME=value，覆盖值优先、空值也列出', () => {
    const preview = envPreview(params, { ...paramDefaults(params), CLEAN_APPLY: '1' })
    expect(preview.map((e) => e.name)).toEqual(['CLEAN_DAYS', 'CLEAN_APPLY', 'CLEAN_ONLY', 'CLEAN_NOTE'])
    expect(preview[1]).toEqual({ name: 'CLEAN_APPLY', value: '1' })
    expect(preview[2]).toEqual({ name: 'CLEAN_ONLY', value: '' })
  })
})

describe('Schedules.vue 的立即执行与编辑', () => {
  it('有参数时弹表单、无参数直接跑（startRun 里按 hasParams 分支）', () => {
    expect(source).toMatch(/function startRun\(s: Schedule\): void \{[\s\S]*?if \(!hasParams\(s\.params\)\) \{[\s\S]*?void onRun\(s\)/)
    expect(source).toContain('@click="startRun(s)"')
    // 有参数的行在「运行」上带 ⚙
    expect(source).toContain('data-test="schedule-param-gear"')
  })

  it('表单按类型生成控件，并在底部预览环境变量', () => {
    expect(source).toContain(`paramControl(p) === 'toggle'`)
    expect(source).toContain(`paramControl(p) === 'select'`)
    expect(source).toContain(`paramControl(p) === 'number'`)
    expect(source).toContain('data-test="run-param-preview"')
    expect(source).toContain('data-test="run-param-submit"')
    // 每项下面显示 desc
    expect(source).toMatch(/v-if="p\.desc" class="pdesc"/)
  })

  it('提交时把表单值作为本次覆盖参数发给 run-now', () => {
    expect(source).toMatch(/async function submitRun\(\)[\s\S]*?await onRun\(s, runValues\.value\)/)
    expect(source).toMatch(/runSchedule\(s\.id, params\)/)
  })

  it('编辑弹窗可改 cron / 标题 / 参数声明 / 启停，并走 PATCH', () => {
    expect(source).toContain('data-test="edit-cron"')
    expect(source).toContain('data-test="edit-title"')
    expect(source).toContain('data-test="edit-enabled"')
    expect(source).toContain('data-test="edit-param-add"')
    expect(source).toContain('edit-param-row-')
    expect(source).toMatch(/await patchSchedule\(s\.id, \{/)
    // 命令 / agent / runner / 项目不在编辑表单里
    expect(source).not.toMatch(/data-test="edit-(cmd|agent|runner|project)"/)
  })
})
