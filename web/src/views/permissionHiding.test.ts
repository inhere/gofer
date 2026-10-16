import { describe, expect, it } from 'vitest'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const runners = raw(import.meta.glob('./Runners.vue', { eager: true, query: '?raw', import: 'default' }))
const about = raw(import.meta.glob('./settings/About.vue', { eager: true, query: '?raw', import: 'default' }))
const config = raw(import.meta.glob('./Config.vue', { eager: true, query: '?raw', import: 'default' }))
const projects = raw(import.meta.glob('./Projects.vue', { eager: true, query: '?raw', import: 'default' }))
const jobDetail = raw(import.meta.glob('./JobDetail.vue', { eager: true, query: '?raw', import: 'default' }))
const tunnels = raw(import.meta.glob('./settings/Tunnels.vue', { eager: true, query: '?raw', import: 'default' }))

describe('actions are hidden by whoami permissions', () => {
  it('runners: worker upgrade and worker management', () => {
    expect(runners).toContain("v-if=\"can('worker.upgrade')\"")
    expect(runners).toContain("can('worker.manage')")
  })
  it('about: one-click update', () => {
    expect(about).toContain("can('update.apply')")
  })
  it('config and projects: writes', () => {
    expect(config).toContain("can('config.write')")
    expect(projects).toContain("can('project.write')")
  })
  it('job detail: delete of other callers jobs', () => {
    expect(jobDetail).toContain("can('job.delete', job.caller_id)")
  })
  it('tunnels: hosted forwards, presets and stopping others', () => {
    expect(tunnels).toContain("can('tunnel.hosted')")
    expect(tunnels).toContain("can('tunnel.stopOthers', f.caller_id)")
  })
})
