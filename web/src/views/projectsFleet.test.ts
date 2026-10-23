// 项目页「按项目 / 按执行机」视图切换（issue gofer-d1kn）：两种视图、worker 自有项目、
// 离线 worker、默认工作空间与手机断点。结构与文案断言取页面字典，换文案不用改测试。
import { describe, expect, it } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createMemoryHistory, createRouter } from 'vue-router'
import Projects from './Projects.vue'
import { projects as dict } from '../i18n/pages/projects'

const source = Object.values(import.meta.glob('./Projects.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const utils = Object.values(import.meta.glob('../utils/projectFleet.ts', { eager: true, query: '?raw', import: 'default' }))[0] as string

async function render(path: string): Promise<string> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/projects', component: { template: '<div />' } }],
  })
  await router.push(path)
  await router.isReady()
  const app = createSSRApp(Projects)
  app.use(router)
  return renderToString(app)
}

describe('projects page views', () => {
  it('offers a by-project / by-runner switch on the URL', async () => {
    const html = await render('/projects')
    expect(html).toContain('role="tablist"')
    expect(html).toContain('aria-label="' + dict.zh['projects.view.label'] + '"')
    expect(html).toContain(dict.zh['projects.view.project'])
    expect(html).toContain(dict.zh['projects.view.runner'])
    expect(source).toContain('<Tabs :tabs="viewTabs" param="view"')
    expect(source).toContain("value: 'project'")
    expect(source).toContain("value: 'runner'")
  })

  it('defaults to the by-project view', async () => {
    const html = await render('/projects')
    expect(html).toContain('mobile-project-picker')
    expect(html).not.toContain('data-test="projects-runner-view"')
  })

  it('renders the by-runner view behind ?view=runner with one group per runner', async () => {
    const html = await render('/projects?view=runner')
    expect(html).toContain('data-test="projects-runner-view"')
    expect(html).toContain(dict.zh['projects.fleet.hint'])
    expect(html).not.toContain('mobile-project-picker')
    // 首屏只有 server 本机一组（worker 行要等 /v1/runners 回来）
    expect(html).toContain('data-test="fleet-group-server"')
    expect(html).toContain(dict.zh['projects.fleet.local'])
    expect(html).toContain(dict.zh['projects.fleet.online'])
    expect(html).toContain('data-test="fleet-workspace"')
    // 拉数据之前：默认工作空间「未上报」，可用项目空态
    expect(html).toContain(dict.zh['projects.fleet.notReported'])
    expect(html).toContain(dict.zh['projects.fleet.noProjects'])
  })

  it('keeps the worker-owned projects visible in the by-project view', () => {
    expect(source).toContain('data-test="project-worker-own"')
    expect(source).toContain("t('projects.workerOwn.title')")
    expect(source).toContain("t('projects.workerOwn.hint')")
    expect(source).toContain('v-for="row in ownProjects"')
    expect(source).toContain('workerOwnProjects(')
  })

  it('marks the built-in default project as running on the server host', () => {
    expect(source).toContain('data-test="project-default-local"')
    expect(source).toContain("key === 'default'")
    expect(source).toContain("detail.key === 'default'")
    expect(source).toContain("t('projects.defaultLocal')")
  })

  it('shows offline runners greyed out with their reason', () => {
    expect(source).toContain("'group--off': !g.online")
    expect(source).toContain('data-test="fleet-group-error"')
    expect(source).toContain("t('projects.fleet.offline')")
    expect(source).toContain('fleetNote(g.workspaceNote)')
    expect(utils).toContain("workspaceNote: ws ? '' : online ? 'notReported' : 'offline'")
    expect(utils).toContain("ownNote: haveProjects ? '' : online ? 'notReported' : 'offline'")
  })

  it('loads runner data from the existing endpoints only', () => {
    expect(source).toContain('getWorkerView(')
    expect(source).toContain('getWorkerProjects(')
    expect(source).toContain('listRunners(')
    expect(source).toContain('loadProjectAllowed')
  })

  it('stacks the runner groups and lifts the worker-owned block on phones', () => {
    expect(source).toContain('grid-template-columns: repeat(auto-fill, minmax(320px, 1fr))')
    expect(source).toMatch(/@media \(max-width: 760px\) \{\n {2}\.groups \{\n {4}grid-template-columns: 1fr;/)
    expect(source).toMatch(/@media \(max-width: 640px\) \{\n {2}\.worker-own \{\n {4}order: -1;/)
    expect(source).toContain('.group-kv {\n    grid-template-columns: 1fr;')
  })
})
