// 接线检查（S9 T6）：「等我」的浏览器通知必须真的挂在 App 上、设置页必须说明三种情况，
// 否则算法与测试绿着、页面上却一条通知都没有。
import { describe, expect, it } from 'vitest'

const mods = import.meta.glob(['./../App.vue', './../views/settings/Notifications.vue'], {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>

function src(name: string): string {
  const key = Object.keys(mods).find((x) => x.endsWith(name))
  if (!key) throw new Error(`missing ${name}`)
  return mods[key]
}

describe('waiting notifier wiring', () => {
  it('App mounts the one notifier exit', () => {
    const app = src('App.vue')
    expect(app).toContain("from './composables/useWaitingNotifier'")
    // 与其它 App 级订阅同在导航壳的起停里（/access 下不订阅「等我」主题）。
    expect(app).toContain('startWaitingNotifier(router)')
    expect(app).toContain('stopWaitingNotifier()')
  })

  it('the notifications settings page explains push / one hidden tab / title counter', () => {
    expect(src('Notifications.vue')).toContain("t('settings.notify.localFallback')")
  })
})
