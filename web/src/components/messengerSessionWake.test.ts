import { describe, expect, it } from 'vitest'

const src = (name: string): string => {
  const mods = import.meta.glob(['./MessengerDrawer.vue', './SessionDrawer.vue', './RunnerDirs.vue', './workbench/WorkbenchSidebar.vue', '../views/Runners.vue', '../views/Sessions.vue', '../views/Workbench.vue'], { eager: true, query: '?raw', import: 'default' }) as Record<string, string>
  const key = Object.keys(mods).find((k) => k.endsWith(name))
  if (!key) throw new Error(`missing ${name}`)
  return mods[key]
}

describe('Runners page shows the messenger on both local and worker cards', () => {
  const runners = src('views/Runners.vue')
  it('renders a status chip on each card and opens the drawer', () => {
    expect(runners.match(/data-test="messenger-chip"/g)?.length).toBe(2)
    expect(runners).toContain('messengerDisplay(w)')
    expect(runners).toContain('messengerDisplay(l)')
    expect(runners).toContain('<MessengerDrawer')
    expect(runners).toContain('<RunnerDirs')
  })
  it('colours the states', () => {
    for (const tone of ['idle', 'busy', 'stopped', 'unknown']) expect(runners).toContain(`.mtone--${tone}`)
  })
})

describe('MessengerDrawer', () => {
  const drawer = src('MessengerDrawer.vue')
  it('shows status, recent deliveries, stderr and the session list with registration', () => {
    for (const part of ['data-test="drawer-status"', 'data-test="deliveries"', 'data-test="stderr"', 'data-test="list-agents"', 'data-test="agents-table"', 'data-test="talk-to"', "t('runners.messenger.registered')", "t('runners.messenger.unregistered')"]) {
      expect(drawer).toContain(part)
    }
  })
  it('greys the list button out with the reason', () => {
    expect(drawer).toContain(':disabled="!!listBlock || listLoading"')
    expect(drawer).toContain(':title="listBlock ||')
  })
})

describe('RunnerDirs', () => {
  const dirs = src('RunnerDirs.vue')
  it('lists the workspace first, then roots and project paths, flagging missing ones', () => {
    expect(dirs.indexOf('data-test="dirs-workspace"')).toBeLessThan(dirs.indexOf("runners.dirs.roots"))
    expect(dirs.indexOf("runners.dirs.roots")).toBeLessThan(dirs.indexOf("runners.dirs.projects"))
    expect(dirs).toContain('rdirs-missing')
    expect(dirs).toContain('runners.dirs.workspaceMissing')
    expect(dirs).toContain('data-test="dirs-rejected"')
  })
})

describe('session wake-up entry points', () => {
  it('Sessions rows lead to the session detail, which carries the wake-up button', () => {
    const sessions = src('views/Sessions.vue')
    const drawer = src('SessionDrawer.vue')
    expect(sessions).toContain('data-test="history-detail"')
    expect(sessions).toContain('<SessionDrawer')
    // 已结束 / 离线的会话也能唤醒；不能唤醒时灰显并在 title 里写原因。
    expect(drawer).toContain(':disabled="!session?.can_resume || waking || wakePlanLoading"')
    expect(drawer).toContain('resumeSession(props.sid, draft.value.trim())')
    expect(drawer).toContain('?attach=1')
  })

  it('SessionDrawer shows the wake-up control regardless of a failed send, also when embedded', () => {
    const drawer = src('SessionDrawer.vue')
    expect(drawer).toContain('data-test="wake-bar"')
    // not gated on `embedded` or on a failed delivery
    const bar = drawer.slice(drawer.indexOf('data-test="wake-bar"') - 80, drawer.indexOf('data-test="wake-bar"'))
    expect(bar).toContain('showWake')
    expect(bar).not.toContain('embedded')
    expect(drawer).toContain('const showWake = computed(() => !!session.value && session.value.state !== \'handed_off\')')
    expect(drawer).toContain('getSessionTakeoverPlan(props.sid)')
    expect(drawer).toContain('resumeSession(props.sid')
    expect(drawer).toContain("t('sessions.drawer.ph.ended')")
  })
})
