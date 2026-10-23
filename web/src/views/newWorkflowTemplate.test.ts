import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./NewWorkflow.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('NewWorkflow template wizard wiring', () => {
  it('keeps the YAML tab next to a default "from template" tab', () => {
    // 页签是 P2 的 ui/Tabs，值以 URL 的 ?tab= 为准（设计稿 §8、§13）。
    expect(source).toContain("const mode = computed<'template' | 'yaml'>")
    expect(source).toContain(':tabs="modeTabs"')
    expect(source).toContain("t('workflows.new.tabTemplate')")
    expect(source).toContain("t('workflows.new.tabYaml')")
    expect(source).toContain("t('workflows.new.yaml')")
  })

  it('YAML 编辑器用等宽字体加行号（与 textarea 同步滚动）', () => {
    expect(source).toContain('class="yaml-gutter"')
    expect(source).toContain('{{ lineNumbers }}')
    expect(source).toContain('@scroll="syncScroll"')
    expect(source).toContain('class="yaml-editor mono"')
  })

  it('lists templates for the chosen project, previews the rendered steps and submits template + vars', () => {
    expect(source).toContain('listWorkflowTemplates(tplProject.value || undefined)')
    expect(source).toContain('renderWorkflowTemplate(tplName.value')
    expect(source).toContain('submitWorkflowFromTemplate(tplName.value')
    expect(source).toContain('sourceLabel(t.source)')
  })

  it('builds agent / runner dropdowns from meta, filtered by the project', () => {
    expect(source).toContain('agentOptions(selectedProject.value')
    expect(source).toContain('runnerPickOptions(selectedProject.value')
    expect(source).toContain('computeRunnerBlocks(selectedProject.value')
  })
})
