// 中文文案的接线断言（用户 2026-10-19 的界面文字口径）：各页面真的从
// utils/uiText.ts / utils/statusText.ts 取文案，且旧英文标题 / 面包屑 / 状态筛选
// 不再在页面里各写一份。纯逻辑在 utils/uiText.test.ts、utils/statusText.test.ts。
import { describe, expect, it } from 'vitest'

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
// 一次收全部页面源码，按文件名取（vite 的 glob 不接受模板拼接）。
const views = import.meta.glob('./*.vue', { eager: true, query: '?raw', import: 'default' }) as Record<string, string>
const settingsViews = import.meta.glob('./settings/*.vue', { eager: true, query: '?raw', import: 'default' }) as Record<
  string,
  string
>
function pick(all: Record<string, string>, name: string): string {
  const key = Object.keys(all).find((k) => k.endsWith(`/${name}.vue`))
  if (!key) throw new Error(`missing ${name}.vue`)
  return all[key]
}
const view = (name: string): string => pick(views, name)
const settingsView = (name: string): string => pick(settingsViews, name)

// 页面标题改中文后，不该再有这些大写英文标题残留。
const OLD_TITLES = ['PLANS', 'JOBS BOARD', 'WORKFLOWS', 'SESSIONS', 'PROJECTS', 'SCHEDULES', 'REVIEW QUEUE']

describe('页面标题用中文', () => {
  const titled: Array<[string, string]> = [
    ['Plans', "t('plans.list.title')"],
    ['Workflows', "t('workflows.title')"],
    ['Sessions', "t('sessions.title')"],
    ['Projects', '{{ PAGE.projects }}'],
    ['Schedules', "t('schedules.title')"],
    ['Skills', '{{ PAGE.skills }}'],
  ]

  it.each(titled)('%s 的标题走 i18n 或 PAGE', (name, expr) => {
    expect(view(name)).toContain(expr)
  })

  // P3 迁移：任务列表的标题改从 i18n 取（页面专属 key 在 i18n/pages/jobs.ts）。
  it('任务列表标题已迁到 i18n', () => {
    expect(view('Board')).toContain("t('jobs.h.title')")
    expect(view('Board')).not.toContain('{{ PAGE.board }}')
  })

  // P4 迁移：待验收页的标题与全部文案同样走 i18n（i18n/pages/review.ts）。
  it('待验收页标题已迁到 i18n', () => {
    expect(view('ReviewQueue')).toContain("t('review.h.title')")
    expect(view('ReviewQueue')).not.toContain('{{ PAGE.review }}')
  })

  it('不再有大写英文页面标题', () => {
    for (const name of ['Plans', 'Board', 'Workflows', 'Sessions', 'Projects', 'Schedules', 'ReviewQueue']) {
      const src = view(name)
      for (const old of OLD_TITLES) {
        expect(src).not.toContain(`>${old}<`)
      }
    }
  })

  it('新建页与设置区标题也是中文', () => {
    expect(view('NewWorkflow')).toContain("t('workflows.new.title')")
    expect(view('NewJob')).toContain('{{ isRebuild ? \'快速重建\' : fromSession ? \'从会话新开\' : PAGE.newJob }}')
    expect(settingsView('Rules')).toContain('{{ PAGE.rules }}')
    expect(settingsView('SettingsLayout')).toContain('{{ PAGE.settings }}')
    expect(settingsView('SettingsLayout')).not.toContain('>SETTINGS<')
    expect(settingsView('McpServers')).not.toContain('MCP SERVERS')
    expect(settingsView('About')).not.toContain('服务端 / SERVER')
    expect(settingsView('About')).not.toContain('更新 / UPDATE')
  })
})

describe('面包屑用中文', () => {
  it('返回链接取自 BACK 或 ui/BackLink', () => {
    expect(view('NewJob')).toContain('{{ BACK.board }}')
    // 新建调度 / 新建工作流已迁到 ui/BackLink（P5）：回来源列表，文案走 i18n。
    expect(view('NewSchedule')).toContain('<BackLink list="schedules" to="/schedules"')
    expect(view('NewWorkflow')).toContain('<BackLink list="workflows" to="/workflows"')
    expect(view('NewSchedule')).not.toContain('{{ BACK.schedules }}')
    expect(view('NewWorkflow')).not.toContain('{{ BACK.workflows }}')
    // 任务详情已迁到 ui/BackLink（P3）：回来源列表，文案走 i18n。
    expect(view('JobDetail')).toContain('<BackLink v-if="!embedded" list="jobs" to="/jobs"')
    expect(view('JobDetail')).not.toContain('{{ BACK.board }}')
    for (const name of ['NewJob', 'JobDetail', 'NewSchedule', 'NewWorkflow']) {
      expect(view(name)).not.toContain('← board')
      expect(view(name)).not.toMatch(/← (schedules|workflows|plans)</)
    }
  })
})

describe('状态显示集中映射', () => {
  it('plan / workflow 的筛选选项从 statusText 取', () => {
    expect(view('Plans')).toContain('planStatusOptions()')
    expect(view('Workflows')).toContain('workflowStatusOptions()')
    expect(view('Board')).toContain('jobStatusOptions()')
    // 旧的英文选项字面量已删。
    expect(view('Plans')).not.toContain("{ value: 'open', label: 'open' }")
    expect(view('Workflows')).not.toContain("{ value: 'running', label: 'running' }")
    expect(view('Board')).not.toContain("{ value: 'queued', label: 'queued' }")
  })

  it('徽标 / issue 芯片 / issue 编辑框都走同一份映射', () => {
    expect(raw(import.meta.glob('../components/StatusBadge.vue', { eager: true, query: '?raw', import: 'default' }))).toContain(
      'statusText(props.status)',
    )
    expect(
      raw(import.meta.glob('../components/PlanStatusBadge.vue', { eager: true, query: '?raw', import: 'default' })),
    ).toContain('planStatusText(props.status)')
    expect(view('Issues')).toContain('issueStatusText(status)')
    expect(
      raw(import.meta.glob('../components/IssueDrawer.vue', { eager: true, query: '?raw', import: 'default' })),
    ).toContain('issueStatusText(s)')
    expect(
      raw(import.meta.glob('../components/IssueBatchBar.vue', { eager: true, query: '?raw', import: 'default' })),
    ).toContain('issueStatusText(s)')
  })

  it('工作项状态标签也来自中央映射', () => {
    const work = raw(import.meta.glob('../utils/work.ts', { eager: true, query: '?raw', import: 'default' }))
    expect(work).toContain("import { statusText } from './statusText'")
    expect(work).toContain("label: statusText('active')")
  })
})

describe('表头与表单标签用中文', () => {
  it('列表页表头不再用英文列名', () => {
    expect(view('Board')).not.toContain('job · title / id')
    expect(view('Plans')).not.toContain('plan · title / id')
    expect(view('Workflows')).not.toContain('workflow · title / id')
    expect(view('Schedules')).not.toContain('name / id')
    expect(view('Skills')).not.toContain('<span>updated_at</span>')
    expect(view('Agents')).not.toContain('<span class="col-detect">detect</span>')
  })

  it('新建任务页的表单标签用中文，agent / runner / Prompt 作为专有名词保留', () => {
    const src = view('NewJob')
    for (const gone of ['>PROJECT<', '>RUNNER<', '>AGENT<', '>TEMPLATE（任务书模板，可选）<', '>CWD<', '>TAGS（逗号分隔，可选）<']) {
      expect(src).not.toContain(gone)
    }
    expect(src).toContain('任务书模板（可选）')
    expect(src).toContain('{{ COL.project }}')
    expect(src).toContain('{{ COL.runner }}')
    expect(src).toContain('{{ COL.agent }}')
  })
})
