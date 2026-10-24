// 项目选择处的接线（issue gofer-b8nh）：所有选项目的地方都换成可搜索的共享下拉
// （列表头是 ui/ProjectFilter，表单 / 抽屉里是 ui/SearchSelect），页面里不再有选项目的原生 <select>。
// 项目里没有 DOM 测试环境，所以这里扫描源码接线 + 用 SSR 断言渲染出的 data-test 钩子还在。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import ProjectFilter from './ProjectFilter.vue'

const sources = import.meta.glob(['../**/*.vue'], { eager: true, query: '?raw', import: 'default' }) as Record<
  string,
  string
>

function source(name: string): string {
  const hit = Object.entries(sources).find(([path]) => path.endsWith(`/${name}`))
  if (!hit) throw new Error(`source not found: ${name}`)
  return hit[1]
}

// 表单 / 抽屉里换成 SearchSelect 的项目字段：文件 + 它的 data-test 钩子。
const searchSelectPages: Array<{ file: string; testId: string }> = [
  { file: 'views/NewJob.vue', testId: 'nj-project' },
  { file: 'views/NewSchedule.vue', testId: 'ns-project' },
  { file: 'views/NewWorkflow.vue', testId: 'tpl-project' },
  { file: 'views/WorkflowDetail.vue', testId: 'sdt-project' },
  { file: 'views/Memory.vue', testId: 'memory-project' },
  { file: 'views/Projects.vue', testId: 'projects-picker' },
  { file: 'workbench/WorkbenchComposer.vue', testId: 'composer-project' },
  { file: 'NewSessionSheet.vue', testId: 'session-project' },
]

describe('项目选择处统一成可搜索下拉', () => {
  it.each(searchSelectPages)('$file uses ui/SearchSelect ($testId)', ({ file, testId }) => {
    const src = source(file)
    expect(src).toContain('SearchSelect')
    expect(src).toContain(`test-id="${testId}"`)
  })

  it('keeps no native <select> bound to a project anywhere', () => {
    for (const [path, src] of Object.entries(sources)) {
      // 选项目的原生下拉：选项来自 projects / projectKeys / projectOptions 或绑定 projectKey / tplProject
      expect(
        /<select[^>]*(projectKey|tplProject)/.test(src),
        `${path} 里还有选项目的原生 <select>`,
      ).toBe(false)
      expect(
        /<select[^>]*>[\s\S]{0,400}?v-for="\w+ in (projectOptions|projects|projectKeys)"/.test(src),
        `${path} 里还有用项目列表当选项的原生 <select>`,
      ).toBe(false)
    }
  })

  it('builds the option list from the project keys (searchable by key)', () => {
    expect(source('views/NewJob.vue')).toContain('value: p.key, label: p.key')
    expect(source('views/NewSchedule.vue')).toContain('value: p.key, label: p.key')
    expect(source('views/NewWorkflow.vue')).toContain('value: p.key, label: p.key')
    expect(source('views/WorkflowDetail.vue')).toContain('value: key, label: key')
    expect(source('NewSessionSheet.vue')).toContain('value: p.key, label: p.key')
  })

  it('keeps every project selector wired to its page state', () => {
    // 换项目仍然触发原来的收敛逻辑（agent / runner / worker 跟着变）
    expect(source('views/NewJob.vue')).toContain('@select="onProjectChange()"')
    expect(source('views/NewSchedule.vue')).toContain('@select="onProjectChange()"')
    expect(source('views/Memory.vue')).toContain('@select="changeProject()"')
    // 工作流模板 / 记忆 / 项目页原来就是「值一变就重新拉」，保持 v-model / update 直连
    expect(source('views/NewWorkflow.vue')).toContain('v-model="tplProject"')
    expect(source('views/WorkflowDetail.vue')).toContain('v-model="tplProject"')
    expect(source('workbench/WorkbenchComposer.vue')).toContain('v-model="projectKey"')
    expect(source('NewSessionSheet.vue')).toContain('v-model="projectKey"')
    // 记忆页保留「未选项目」首项（原来那个空 option）
    expect(source('views/Memory.vue')).toContain("label: t('memory.projectPlaceholder')")
    // 项目页的手机选择器保留「选择项目」占位
    expect(source('views/Projects.vue')).toContain("label: t('projects.pickerPlaceholder')")
  })
})

describe('ProjectFilter 内部用同一套可搜索下拉', () => {
  it('delegates to SearchSelect and keeps「全部项目」as the first option', () => {
    const src = source('ProjectFilter.vue')
    expect(src).toContain("import SearchSelect from './SearchSelect.vue'")
    expect(src).toContain('<SearchSelect')
    expect(src).toContain("test-id=\"project-filter\"")
    expect(src).toContain("label: t('field.allProjects')")
  })

  it('still renders the filter chip hooks the list pages rely on', async () => {
    const app = createSSRApp({
      render: () =>
        h(ProjectFilter as never, { modelValue: 'gofer', options: ['gofer', 'acme'], error: 'boom' }),
    })
    const html = await renderToString(app)
    expect(html).toContain('data-test="project-filter"')
    expect(html).toContain('data-test="project-filter-trigger"')
    expect(html).toContain('>gofer<')
    // 加载错误挂在触发器的 title 上（原来就是这样）
    expect(html).toContain('title="boom"')
  })
})
