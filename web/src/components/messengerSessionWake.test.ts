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
    for (const part of ['data-test="drawer-status"', 'data-test="deliveries"', 'data-test="stderr"', 'data-test="list-agents"', 'data-test="agents-table"', 'data-test="talk-to"', '已登记', '未登记']) {
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
    expect(dirs.indexOf('data-test="dirs-workspace"')).toBeLessThan(dirs.indexOf('roots 映射（server'))
    expect(dirs.indexOf('roots 映射（server')).toBeLessThan(dirs.indexOf('>项目解析路径<'))
    expect(dirs).toContain('rdirs-missing')
    expect(dirs).toContain('GOFER_WORKSPACE')
    expect(dirs).toContain('data-test="dirs-rejected"')
  })
})

describe('session wake-up entry points', () => {
  it('Sessions rows carry a wake-up button that greys out with the reason, ended rows included', () => {
    const sessions = src('views/Sessions.vue')
    expect(sessions).toContain('data-test="row-wake"')
    expect(sessions).toContain(':disabled="!s.can_resume')
    expect(sessions).toContain(':title="wakeErrors.get(s.session_id)')
    expect(sessions).toContain('resumeSession(s.session_id)')
    expect(sessions).toContain('?attach=1')
    // an ended / offline card is dimmed as a whole, but the dimming must not apply to the
    // wake-up button (the shared card CSS only dims head / meta / details — see infoCard.test.ts)
    expect(sessions).toContain(':dim="agentStateDim(s.state)"')
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
    expect(drawer).toContain('点上方「唤醒」')
  })
})
