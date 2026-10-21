import { describe, expect, it } from 'vitest'

const newWf = Object.values(
  import.meta.glob('./NewWorkflow.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string
const detail = Object.values(
  import.meta.glob('./WorkflowDetail.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

describe('NewWorkflow template source / edit wiring (D7)', () => {
  it('reads the template source and labels where it comes from', () => {
    expect(newWf).toContain('getWorkflowTemplateSource(tplName.value')
    expect(newWf).toContain('data-test="tpl-view-source"')
    expect(newWf).toContain('sourceLabel(src.source)')
    expect(newWf).toContain('data-test="tpl-shadows"')
    expect(newWf).toContain('data-test="tpl-builtin-note"')
  })

  it('edits with a YAML area, picks the layer and shows the server validation error', () => {
    expect(newWf).toContain('saveWorkflowTemplate(name, saveScope.value, editText.value')
    expect(newWf).toContain('data-test="tpl-scope-global"')
    expect(newWf).toContain('data-test="tpl-scope-project"')
    expect(newWf).toContain('data-test="tpl-save-error"')
    expect(newWf).toContain('data-test="tpl-override-note"')
  })

  it('warns that a project template is uncommitted and offers the commit command', () => {
    expect(newWf).toContain('templateSaveNotice(r)')
    expect(newWf).toContain('data-test="tpl-save-notice"')
    expect(newWf).toContain('data-test="tpl-commit-cmd"')
    expect(newWf).toContain('copyCommitCommand(notice.command)')
  })

  it('creates and deletes templates, the delete behind a confirmation', () => {
    expect(newWf).toContain('data-test="tpl-new"')
    expect(newWf).toContain('deleteWorkflowTemplate(tplName.value')
    expect(newWf).toContain('confirmingDelete')
    expect(newWf).toContain('templateDeleteNotice(r)')
  })

  it('hides every write without config.write and says why', () => {
    expect(newWf).toContain("templateEditGate(can('config.write')")
    expect(newWf).toContain('data-test="tpl-readonly-reason"')
  })
})

describe('WorkflowDetail result table / rerun / save-as-template wiring (design 一·6 / 一·7)', () => {
  it('renders one result row per step / fan with runner, status, exit code and duration', () => {
    expect(detail).toContain('resultRows(workflow.value)')
    expect(detail).toContain('data-test="result-table"')
    expect(detail).toContain('data-test="res-runner"')
    expect(detail).toContain('data-test="result-fail-count"')
  })

  it('expands the failure excerpt and links to the job log', () => {
    expect(detail).toContain('hasExcerpt(row)')
    expect(detail).toContain('data-test="res-excerpt-toggle"')
    expect(detail).toContain('data-test="res-excerpt"')
    expect(detail).toContain('data-test="res-log"')
  })

  it('reruns only the failures or from a step and jumps to the new workflow', () => {
    expect(detail).toContain("rerunWorkflow(props.id, mode === 'failed' ? { mode } : { mode, step })")
    expect(detail).toContain('data-test="rerun-failed"')
    expect(detail).toContain("onRerun('from', fromStepValue(row))")
    expect(detail).toContain('window.confirm')
  })

  it('saves the run as a template through the same scopes and shows the uncommitted hint', () => {
    expect(detail).toContain('saveWorkflowAsTemplate(props.id')
    expect(detail).toContain('data-test="save-as-template-open"')
    expect(detail).toContain('data-test="sat-scope-project"')
    expect(detail).toContain('data-test="sat-commit-cmd"')
    expect(detail).toContain('templateSaveNotice(r)')
  })
})
