import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import UpdateApplyDialog from './UpdateApplyDialog.vue'
import { settings as settingsDict } from '../i18n/pages/settings'

const aboutZh = settingsDict.zh

// gofer-dzv3「空闲时更新」的 web 面：确认弹层的新说明与最长等待选择、关于页 waiting_idle 的
// 进度（在等谁 / 停住多少 / 放弃时间 / 取消等待），以及 paused_for_update 在状态文案、颜色与
// 筛选里的登记。渲染断言只看 SSR 出的 HTML，源码断言用于 pin 住与后端字段/事件的对应关系。
const raw = (glob: Record<string, unknown>) => Object.values(glob)[0] as string
const dialogSrc = raw(import.meta.glob('./UpdateApplyDialog.vue', { eager: true, query: '?raw', import: 'default' }))
const badgeSrc = raw(import.meta.glob('./StatusBadge.vue', { eager: true, query: '?raw', import: 'default' }))
const aboutSrc = raw(import.meta.glob('../views/settings/About.vue', { eager: true, query: '?raw', import: 'default' }))
const boardSrc = raw(import.meta.glob('../views/Board.vue', { eager: true, query: '?raw', import: 'default' }))
const statusSrc = raw(import.meta.glob('../utils/statusText.ts', { eager: true, query: '?raw', import: 'default' }))
const clientSrc = raw(import.meta.glob('../api/client.ts', { eager: true, query: '?raw', import: 'default' }))
const typesSrc = raw(import.meta.glob('../api/types.ts', { eager: true, query: '?raw', import: 'default' }))

async function renderDialog(): Promise<string> {
  const app = createSSRApp({
    render: () => h(UpdateApplyDialog, { current: '0.153.1', latest: '0.160.0', submitting: false, error: '' }),
  })
  return renderToString(app)
}

describe('一键更新：空闲时更新（gofer-dzv3）', () => {
  it('弹层说明先等本机 job 结束、新 job 先停住，并给出最长等待选择', async () => {
    const html = await renderDialog()
    expect(html).toContain('等本机运行中的 job 结束后重启')
    // 有 job 在跑时那句说明（SSR 下 running 还是 null，看源码）与静态的等待说明。
    expect(dialogSrc).toMatch(/会先等它们结束；期间新提交的 job 先停住/)
    expect(html).toContain('等待期间新 job 会先停住')
    expect(html).toContain('worker 上的 job 不受影响')
    // 旧的「最多约 5 分钟，超时则放弃」说法已被推翻。
    expect(html).not.toContain('最多约 5 分钟')
    expect(dialogSrc).not.toContain('最多约 5 分钟')
    for (const label of ['30 分钟', '1 小时', '2 小时', '4 小时']) {
      expect(html).toContain(label)
    }
    for (const sec of [1800, 3600, 7200, 14400]) {
      expect(html).toContain(`data-testid="update-wait-${sec}"`)
    }
    // 默认选中 2 小时。
    expect(html).toMatch(/value="7200" checked/)
    expect(dialogSrc).toMatch(/const waitIdleSec = ref\(7200\)/)
  })

  it('确认时把选中的最长等待一起提交', () => {
    expect(dialogSrc).toMatch(/emit\('confirm', \[\.\.\.picked\], waitIdleSec\)/)
    expect(clientSrc).toMatch(/wait_idle_sec: waitIdleSec/)
    expect(clientSrc).toMatch(/export function applyUpdate\(workers: string\[\], waitIdleSec = 7200\)/)
  })

  it('关于页在 waiting_idle 阶段列出在等的 job、停住数量与放弃时间，并可取消等待', () => {
    expect(aboutSrc).toMatch(/phaseText\(apply\.phase\)/)
    expect(aboutZh['settings.about.phase.waiting_idle']).toBe('等运行中的 job 结束')
    expect(aboutZh['settings.about.phase.cancelled']).toBe('已取消')
    expect(aboutSrc).toMatch(/const activePhases = \['downloading', 'waiting_idle', 'restarting', 'upgrading_workers'\]/)
    expect(aboutSrc).toMatch(/data-testid="update-wait-idle"/)
    // 在等的 job 可点进详情；每个还显示 runner 与状态。
    expect(aboutSrc).toMatch(/:to="`\/jobs\/\$\{j\.id\}`"/)
    expect(aboutSrc).toMatch(/apply\.paused \?\? 0/)
    expect(aboutSrc).toMatch(/apply\.wait_deadline/)
    expect(aboutSrc).toMatch(/data-testid="update-wait-cancel"/)
    expect(aboutSrc).toMatch(/await cancelUpdateWait\(\)/)
    // 取消等待是管理员接口：DELETE /v1/update/apply。
    expect(clientSrc).toMatch(/export function cancelUpdateWait\(\): Promise<UpdateApply \| undefined>/)
    expect(clientSrc).toMatch(/request<UpdateApply \| undefined>\('\/v1\/update\/apply', \{ method: 'DELETE' \}\)/)
  })

  it('paused_for_update 有状态文案、颜色与筛选入口', () => {
    expect(typesSrc).toMatch(/\| 'paused_for_update'/)
    // 状态文案的唯一出处（徽标 / 筛选芯片 / 各页共用一张表）。
    expect(statusSrc).toMatch(/paused_for_update: '⏳ 等更新'/)
    expect(badgeSrc).toMatch(/statusText\(props\.status\)/)
    expect(clientSrc).toMatch(/paused_for_update: 'var\(--queue\)'/)
    expect(statusSrc).toMatch(/'paused_for_update',/)
    expect(boardSrc).toMatch(/jobStatusOptions\(\)/)
    expect(boardSrc).toMatch(/paused_for_update: 0/)
    // 它不是「等人」的状态：徽标不脉冲（不需要人做任何事）。
    expect(badgeSrc).not.toMatch(/attention[\s\S]*paused_for_update/)
  })

  it('UpdateApply 类型带上等空闲的报告字段', () => {
    expect(typesSrc).toMatch(/wait_deadline\?: number/)
    expect(typesSrc).toMatch(/waiting\?: UpdateApplyWaitingJob\[\]/)
    expect(typesSrc).toMatch(/paused\?: number/)
    expect(typesSrc).toMatch(/\| 'waiting_idle'/)
    expect(typesSrc).toMatch(/\| 'cancelled'/)
  })
})
