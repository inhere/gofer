import { describe, expect, it } from 'vitest'
import { runnerOnline } from './runners'

// gofer-hez1：侧栏底部「节点 N/M 在线」曾只认 status === 'up'，而 worker 的在线值是
// connected（local / peer-http 才是 up），于是 4 个已连接 worker + 1 个 local 显示成 1/5。
describe('runnerOnline', () => {
  it('counts a worker with a live connection as online', () => {
    expect(runnerOnline('connected')).toBe(true)
  })

  it('counts local and a probed peer as online', () => {
    expect(runnerOnline('up')).toBe(true)
  })

  it('treats every other status as offline', () => {
    for (const status of ['down', 'disconnected', 'unknown'] as const) {
      expect(runnerOnline(status), status).toBe(false)
    }
  })
})
