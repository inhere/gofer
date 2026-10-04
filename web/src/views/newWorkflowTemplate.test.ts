import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./NewWorkflow.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('NewWorkflow template wizard wiring', () => {
  it('keeps the YAML tab next to a default "from template" tab', () => {
    expect(source).toContain("const mode = ref<'template' | 'yaml'>('template')")
    expect(source).toContain('data-test="tab-template"')
    expect(source).toContain('data-test="tab-yaml"')
    expect(source).toContain('WORKFLOW YAML')
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
