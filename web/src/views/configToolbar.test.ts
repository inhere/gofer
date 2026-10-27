// 设置 › 配置管理 的顶部操作区（用户反馈的小改动）：
// 「重新读取文件」以前被 .mini-btn 的 margin-left:auto 推到工具条中间，现在两个按钮贴右相邻、
// 各带一个 lucide 图标，且「重启 server」点下去只开二次确认（取消不调接口、确认才调）。
//
// 与 configComplete.test.ts 一样用源码断言（仓库没有 DOM 测试环境）。
import { describe, expect, it } from 'vitest'
import { settings as settingsDict } from '../i18n/pages/settings'

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const src = raw(import.meta.glob('./Config.vue', { eager: true, query: '?raw', import: 'default' }))
const zh = settingsDict.zh
const en = settingsDict.en

describe('配置页顶部操作区', () => {
  it('keeps re-read right next to restart, in that order, each with an icon', () => {
    const start = src.indexOf('<div class="toolbar">')
    const end = src.indexOf('<span class="poll', start)
    expect(start).toBeGreaterThan(-1)
    expect(end).toBeGreaterThan(start)
    const toolbar = src.slice(start, end)

    const reload = toolbar.indexOf('data-test="cfg-reload"')
    const restart = toolbar.indexOf('data-test="cfg-restart"')
    expect(reload).toBeGreaterThan(-1)
    expect(restart).toBeGreaterThan(reload)
    // 工具条里就这两个按钮，中间没有别的（两个按钮紧挨着）。
    expect(toolbar.match(/<button/g)).toHaveLength(2)
    // 图标走项目统一的 ui/Icon（lucide）。
    expect(toolbar).toContain('<Icon :icon="RefreshCw" size="sm" />')
    expect(toolbar).toContain('<Icon :icon="Power" size="sm" />')
  })

  it('right-aligns the toolbar instead of pushing the first button to the middle', () => {
    expect(src).toMatch(/\.toolbar \{[^}]*justify-content: flex-end/)
    // .mini-btn 自带 margin-left:auto 会把第一个按钮推到中间，工具条里复位。
    expect(src).toMatch(/\.toolbar \.mini-btn \{[^}]*margin-left: 0/)
  })

  it('opens a confirm dialog first and only calls the API when confirmed', () => {
    // 触发按钮只打开确认框：不在这里调接口。
    const trigger = /data-test="cfg-restart"[\s\S]{0,800}?@click="showRestart = true; restartError = ''"/
    expect(src).toMatch(trigger)
    // 确认框：标题 + 影响说明 + 确认 / 取消两个按钮。
    expect(src).toMatch(/data-test="cfg-restart-dialog"/)
    expect(src).toContain("t('settings.config.restartConfirmTitle')")
    expect(src).toContain("t('settings.config.restartImpact')")
    // 文案说清「等运行中的任务结束」「期间页面会短暂断开」（中英两本字典都有）。
    for (const dict of [zh, en]) {
      expect(dict['settings.config.restartImpact']).toContain('job')
      expect(dict['settings.config.restartImpact']).toMatch(/断开|disconnected/)
    }
    // 取消只关框：不调接口。
    expect(src).toMatch(/data-test="cfg-restart-dismiss"[^>]*@click="showRestart = false"/)
    // 只有确认按钮走 startRestart，restartServer 在整份文件里只被它调用一次。
    expect(src).toMatch(/data-test="cfg-restart-confirm"[^>]*@click="startRestart\(\)"/)
    expect(src.match(/await restartServer\(/g)).toHaveLength(1)
  })
})
