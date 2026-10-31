// cron 试算（编辑弹窗）：纯逻辑 + 弹窗接线。
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/client'
import { errorText, runCronPreview } from './cronPreview'
import { dicts } from '../i18n'

const schedulesSource = Object.values(
  import.meta.glob('../views/Schedules.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

describe('runCronPreview', () => {
  it('空白输入不发请求', async () => {
    const fetcher = vi.fn()
    expect(await runCronPreview('   ', fetcher)).toEqual({ kind: 'empty' })
    expect(fetcher).not.toHaveBeenCalled()
  })

  it('合法表达式返回下几次运行时间，请求里带去掉空白的表达式', async () => {
    const fetcher = vi.fn().mockResolvedValue({ cron: '*/5 * * * *', next_runs: [1000, 1300, 1600] })
    expect(await runCronPreview(' */5 * * * * ', fetcher)).toEqual({ kind: 'ok', runs: [1000, 1300, 1600] })
    expect(fetcher).toHaveBeenCalledWith('*/5 * * * *', 5)
  })

  it('非法表达式显示服务端解析器的原话（detail 优先）', async () => {
    const fetcher = vi.fn().mockRejectedValue(new ApiError(400, 'invalid cron', 'jobstore: parse cron "x": expected 5 fields'))
    expect(await runCronPreview('x', fetcher)).toEqual({
      kind: 'error',
      message: 'jobstore: parse cron "x": expected 5 fields',
    })
  })

  it('没有 detail 时退回 message，非 Error 也不抛', () => {
    expect(errorText(new Error('boom'))).toBe('boom')
    expect(errorText('plain')).toBe('plain')
  })
})

describe('Schedules.vue 编辑弹窗的 cron 试算接线', () => {
  it('编辑 cron 时防抖试算，丢弃过期响应，打开/卸载时复位', () => {
    expect(schedulesSource).toContain('previewCron')
    expect(schedulesSource).toContain('runCronPreview(expr, previewCron)')
    expect(schedulesSource).toContain('watch(editCron')
    expect(schedulesSource).toContain('seq === cronPreviewSeq')
    expect(schedulesSource).toContain('data-test="edit-cron-preview"')
    // openEdit 先复位，卸载也清定时器
    expect(schedulesSource).toMatch(/resetCronPreview\(\)\n\s+editCron\.value = /)
    expect(schedulesSource).toMatch(/liveSchedules\.stop\(\)\n\s+resetCronPreview\(\)/)
  })

  it('试算文案中英都有', () => {
    for (const lang of ['zh-CN', 'en-US'] as const) {
      const dict = dicts[lang]
      expect(dict['schedules.editForm.cronNext']).toBeTruthy()
      expect(dict['schedules.editForm.cronInvalid']).toContain('{msg}')
    }
  })
})
