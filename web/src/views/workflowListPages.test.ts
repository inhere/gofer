// 工作流列表改版（issue gofer-sg61）：/workflows 是模板列表，执行记录在 /workflows/runs，
// 详情页 /workflows/:id 不变。页面测试在本仓库是结构 / 接线断言（没有 DOM 环境），
// 纯逻辑在 utils/workflowList.test.ts。
import { describe, expect, it } from 'vitest'
import { dicts } from '../i18n'
import { routes } from '../router.routes'

const views = import.meta.glob('./*.vue', { eager: true, query: '?raw', import: 'default' }) as Record<string, string>

function raw(name: string): string {
  const src = views[`./${name}`]
  if (!src) throw new Error(`missing ${name}`)
  return src
}

const list = raw('Workflows.vue')
const runs = raw('WorkflowRuns.vue')
const detail = raw('WorkflowTemplate.vue')
const wizard = raw('NewWorkflow.vue')
const runDetail = raw('WorkflowDetail.vue')
const tplRuns = Object.values(
  import.meta.glob('../components/TemplateRuns.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string
const clientSrc = Object.values(
  import.meta.glob('../api/client.ts', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

describe('路由', () => {
  const entry = (path: string) => routes.find((r) => r.path === path)

  it('/workflows 是列表，/workflows/runs 是执行记录，模板详情独立路由', () => {
    expect(entry('/workflows')?.name).toBe('workflows')
    expect(entry('/workflows/runs')?.name).toBe('workflow-runs')
    expect(entry('/workflows/templates/:name')?.name).toBe('workflow-template')
  })

  it('旧链接 /workflows/:id 与 /workflows/new 仍在，且单次执行详情路由未变', () => {
    expect(entry('/workflows/:id')?.name).toBe('workflow-detail')
    expect(entry('/workflows/:id')?.props).toBe(true)
    expect(entry('/workflows/new')?.name).toBe('new-workflow')
  })

  it('静态段排在 :id 之前，runs / new 不会被当成执行 id', () => {
    const paths = routes.map((r) => r.path)
    const idAt = paths.indexOf('/workflows/:id')
    expect(paths.indexOf('/workflows/runs')).toBeLessThan(idAt)
    expect(paths.indexOf('/workflows/new')).toBeLessThan(idAt)
  })
})

describe('/workflows 工作流列表', () => {
  it('读模板接口并按来源分组，每组一个 group-<层>', () => {
    expect(list).toContain('listWorkflowTemplates(project.value || undefined)')
    expect(list).toContain('groupTemplates(templates.value)')
    expect(list).toContain(':data-test="`group-${g.layer}`"')
  })

  it('每项显示来源徽标、说明、变量、最近一次执行（StatusPill + 时间）与累计次数', () => {
    for (const k of ['data-test="source-badge"', 'tpl.desc', 'data-test="tpl-vars"', 'data-test="tpl-lastrun"', 'data-test="tpl-runs"']) {
      expect(list).toContain(k)
    }
    expect(list).toContain('<StatusPill :status="tpl.last_run.status" />')
    expect(list).toContain('fmtDateTime(tpl.last_run.started_at)')
    expect(list).toContain("t('workflows.list.runCount', { n: runCount(tpl) })")
    expect(list).toContain('data-test="tpl-never"')
  })

  it('操作：运行（只读上限下收起）、查看 YAML（读 /source，显示路径）、执行记录', () => {
    expect(list).toContain('v-if="!readOnly" class="btn btn--primary" :to="runLink(tpl.name, projectOf(tpl))"')
    expect(list).toContain('getWorkflowTemplateSource(tpl.name, project.value || undefined)')
    expect(list).toContain('data-test="tpl-path"')
    expect(list).toContain(':to="runsLink(tpl.name)"')
  })

  it('列表本身不带编辑 / 删除（在详情页），避免两处各一份权限判断', () => {
    expect(list).not.toContain('deleteWorkflowTemplate')
    expect(list).not.toContain('saveWorkflowTemplate')
  })

  it('项目目录里的模板靠 ?project= 筛选才列得出', () => {
    expect(list).toContain('useProjectFilter(\'/workflows\')')
    expect(list).toContain('<ProjectFilter')
  })
})

describe('模板详情页', () => {
  it('YAML 查看对所有人开放，编辑 / 删除要 config.write 且只限全局 / 项目文件', () => {
    expect(detail).toContain("templateEditGate(can('config.write'), whoami.value != null)")
    expect(detail).toContain('gate.value.editable && !readOnly.value && isEditableLayer(src.value?.source)')
    expect(detail).toContain('<div v-if="editable" class="actions">')
    expect(detail).toContain('data-test="tpl-yaml-body"')
    expect(detail).toContain('data-test="tpl-path"')
  })

  it('保存 / 删除走现有 PUT / DELETE，作用在文件所在那一层', () => {
    expect(detail).toContain('saveWorkflowTemplate(name.value, layer, editText.value, project.value || undefined)')
    expect(detail).toContain('deleteWorkflowTemplate(name.value, layer, project.value || undefined)')
  })

  it('删除要二次确认：第一次点只进入确认态，第二次才发请求；可取消', () => {
    expect(detail).toMatch(/if \(!confirmingDelete\.value\) \{\s*confirmingDelete\.value = true\s*return\s*\}/)
    expect(detail).toContain("confirmingDelete ? t('workflows.new.confirmDelete') : t('workflows.new.delete')")
    expect(detail).toContain('data-test="tpl-delete-cancel"')
  })

  it('删除后的回落提示取自接口的 falls_back_to，没有下一层则标记 gone', () => {
    expect(detail).toContain('templateDeleteNotice(r)')
    expect(detail).toContain('if (r.falls_back_to) await load()')
    expect(detail).toContain('else gone.value = true')
    expect(detail).toContain('<Toast')
  })

  it('内嵌该工作流的执行记录（?tab=runs），完整列表链到 /workflows/runs?template=', () => {
    expect(detail).toContain('<TemplateRuns v-else :template="name" />')
    expect(detail).toContain("<Tabs :tabs=\"tabs\"")
    expect(tplRuns).toContain('listWorkflows({ template: props.template, limit: RECENT })')
    expect(tplRuns).toContain(':to="runsLink(template)"')
  })
})

describe('/workflows/runs 执行记录', () => {
  it('原列表原样迁来：状态筛选、分页、useListReturn 指向新路径', () => {
    expect(runs).toContain("useListReturn('workflows', '/workflows/runs')")
    expect(runs).toContain('<FilterChips')
    expect(runs).toContain('const PAGE_SIZE = 50')
  })

  it('?template= 交给服务端过滤，换过滤回第 1 页，可一键清除', () => {
    expect(runs).toContain('const templateFilter = computed(() => queryString(route.query.template))')
    expect(runs).toContain('template: templateFilter.value || undefined')
    expect(runs).toMatch(/watch\(templateFilter, \(\) => \{\s*page\.value = 1/)
    expect(runs).toContain('data-test="template-filter"')
    expect(runs).toContain('@click="clearFilters"')
  })

  it('client.listWorkflows 把 template 写进查询串', () => {
    expect(clientSrc).toContain("if (opts.template) qs.set('template', opts.template)")
  })
})

describe('其他页面的链接', () => {
  it('单次执行详情的返回链接指向 /workflows/runs，并显示来源工作流', () => {
    expect(runDetail).toContain('<BackLink list="workflows" to="/workflows/runs"')
    expect(runDetail).toContain('data-test="meta-template"')
  })

  it('向导读 ?template= / ?project= 预选模板（运行按钮的落点）', () => {
    expect(wizard).toContain('route.query.template')
    expect(wizard).toContain('route.query.project')
    expect(wizard).toContain('wantTemplate')
  })
})

describe('i18n', () => {
  const sources = [list, runs, detail, tplRuns, runDetail]
  const used = new Set<string>()
  for (const src of sources) {
    for (const m of src.matchAll(/\bt\('(workflows\.[A-Za-z0-9_.]+)'/g)) used.add(m[1])
  }

  it('新页面用到的 workflows.* key 在中英字典里都有', () => {
    expect(used.size).toBeGreaterThan(20)
    for (const key of used) {
      expect(dicts['zh-CN'][key], `${key} 缺中文`).toBeTruthy()
      expect(dicts['en-US'][key], `${key} 缺英文`).toBeTruthy()
    }
  })

  it('workflows.list.* / workflows.tpl.* / workflows.runs.* 的英文里没有汉字', () => {
    const han = /[一-鿿]/
    for (const [key, text] of Object.entries(dicts['en-US'])) {
      if (!/^workflows\.(list|tpl|runs)\./.test(key)) continue
      expect(han.test(text), `${key}: ${text}`).toBe(false)
    }
  })
})
