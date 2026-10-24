import { describe, expect, it } from 'vitest'
import { settings as settingsDict } from '../i18n/pages/settings'

// gofer-4uk5：设置 › 配置管理 展示完整配置 + 「重新读取文件」旁的「重启 server」。
// 页面本身依赖 GET /v1/config 的实时数据，这里的断言分两类：
//  - 文案与接口契约：本页新 key 两本字典都有（缺一个就是一半英文一半中文）；
//  - 源码 pin：分组清单与后端段一一对应、重启按钮走哪几个接口、非受管时如何降级。
const raw = (glob: Record<string, unknown>) => Object.values(glob)[0] as string
const configSrc = raw(import.meta.glob('./Config.vue', { eager: true, query: '?raw', import: 'default' }))
const clientSrc = raw(import.meta.glob('../api/client.ts', { eager: true, query: '?raw', import: 'default' }))
const typesSrc = raw(import.meta.glob('../api/types.ts', { eager: true, query: '?raw', import: 'default' }))
const zh = settingsDict.zh
const en = settingsDict.en

describe('配置管理：完整配置（gofer-4uk5）', () => {
  it('每个后端顶层段都有一个只读分组，标题两本字典都有', () => {
    const sections = [
      'serverExtra',
      'storageExtra',
      'log',
      'wakeup',
      'session',
      'work',
      'steward',
      'pty',
      'mail',
      'httpProxy',
      'tools',
      'mcpServers',
      'secrets',
      'pricing',
    ]
    for (const key of sections) {
      expect(configSrc).toContain(`settings.config.sec.${key}`)
      expect(zh[`settings.config.sec.${key}`]).toBeTruthy()
      expect(en[`settings.config.sec.${key}`]).toBeTruthy()
    }
    // 分组卡片用原生 details（默认收起），并标「只读」：这些段在本接口上不可写。
    expect(configSrc).toMatch(/<details v-for="sec in extraSections"/)
    expect(configSrc).toContain('settings.config.readonly')
    expect(zh['settings.config.readonly']).toBe('只读')
    expect(en['settings.config.readonly']).toBe('read-only')
  })

  it('值按类型格式化，敏感字段只显示已配置 / 未配置', () => {
    // 分组用 configRows 的纯函数（`*_set` → 已配置 / 未配置 的规则在那里单测）。
    expect(configSrc).toContain("from './configRows'")
    expect(configSrc).toMatch(/cfgLabels/)
  })

  it('TS 的类型跟上了新增的段', () => {
    for (const field of ['mcp_servers', 'secrets', 'pricing', 'http_proxy', 'steward', 'mail', 'tools']) {
      expect(typesSrc).toContain(`${field}?:`)
    }
    // server 段里原本就缺的块也补上了（否则控制台拿到数据也读不到）。
    for (const field of ['comment_trigger', 'dir_lock', 'agent_health', 'skill_limits', 'update']) {
      expect(typesSrc).toContain(`${field}?:`)
    }
  })
})

describe('配置管理：重启 server（gofer-4uk5）', () => {
  it('按钮在「重新读取文件」旁，非受管时禁用并说明原因', () => {
    expect(configSrc).toMatch(/data-test="cfg-reload"/)
    expect(configSrc).toMatch(/data-test="cfg-restart"/)
    expect(configSrc).toMatch(/:disabled="!restartManaged \|\| restartActive \|\| restarting"/)
    expect(configSrc).toMatch(/data-test="cfg-restart-unmanaged"/)
    expect(zh['settings.config.restartUnmanaged']).toContain('gofer serve register')
    expect(en['settings.config.restartUnmanaged']).toContain('gofer serve register')
  })

  it('二次确认写清影响，并把「最多等空闲」一起提交', () => {
    expect(configSrc).toMatch(/data-test="cfg-restart-dialog"/)
    expect(zh['settings.config.restartImpact']).toContain('新提交的 job 进入排队')
    expect(zh['settings.config.restartImpact']).toContain('断开几秒')
    expect(configSrc).toMatch(/data-test="cfg-restart-wait"/)
    // 等待档位与一键更新一致：30 分钟 / 1 / 2 / 4 小时，默认 2 小时。
    for (const sec of [1800, 3600, 7200, 14400]) {
      expect(configSrc).toContain(`:value="${sec}"`)
    }
    expect(configSrc).toMatch(/const restartWaitSec = ref\(7200\)/)
    expect(clientSrc).toMatch(/export function restartServer\(waitIdleSec: number\)/)
    expect(clientSrc).toMatch(/body: JSON\.stringify\(\{ wait_idle_sec: waitIdleSec \}\)/)
    expect(configSrc).toMatch(/await restartServer\(restartWaitSec\.value\)/)
  })

  it('等空闲时列出在等的 job、停放数与放弃时间，并可取消等待', () => {
    expect(configSrc).toMatch(/data-test="cfg-restart-state"/)
    expect(configSrc).toMatch(/restartWaitingRows/)
    expect(configSrc).toMatch(/:to="`\/jobs\/\$\{j\.id\}`"/)
    expect(configSrc).toMatch(/restartState\.paused \?\? 0/)
    expect(configSrc).toMatch(/restartState\.wait_deadline/)
    expect(configSrc).toMatch(/data-test="cfg-restart-cancel"/)
    expect(configSrc).toMatch(/await cancelServerRestart\(\)/)
    expect(clientSrc).toMatch(/export function cancelServerRestart\(\)/)
    expect(clientSrc).toMatch(/method: 'DELETE'/)
    // 阶段文案走字典，服务端新增阶段时不至于空白。
    expect(configSrc).toMatch(/t\(`settings\.config\.restartPhase\.\$\{phase\}`\)/)
    for (const phase of ['waiting_idle', 'restarting', 'done', 'failed', 'cancelled']) {
      expect(zh[`settings.config.restartPhase.${phase}`]).toBeTruthy()
      expect(en[`settings.config.restartPhase.${phase}`]).toBeTruthy()
    }
  })

  it('重启期间连不上就继续轮询，回来后重读配置', () => {
    expect(configSrc).toMatch(/restartUnreachable/)
    expect(configSrc).toMatch(/scheduleRestartPoll/)
    expect(configSrc).toMatch(/await loadConfig\(true\)/)
    // 定时器在离开页面时清掉（不留下轮询）。
    expect(configSrc).toMatch(/if \(restartTimer\) \{\s*clearTimeout\(restartTimer\)/)
  })

  it('接口契约：GET 带 managed，POST/DELETE 回状态', () => {
    expect(clientSrc).toMatch(/export function getServerRestart\(\): Promise<ServerRestartView>/)
    expect(clientSrc).toMatch(/'\/v1\/server\/restart'/)
    expect(typesSrc).toContain('export interface ServerRestartView')
    expect(typesSrc).toContain('export interface ServerRestartState')
    expect(typesSrc).toMatch(/managed: boolean/)
  })
})
