import { describe, expect, it } from 'vitest'
import type { MetaAgent, MetaProject, MetaRunner, WorkflowSpec } from '../api/types'
import {
  adaptAgentVars,
  agentOptions,
  cleanVars,
  inferVarKinds,
  initialValues,
  previewSteps,
  runnerPickOptions,
  sourceLabel,
  validateVars,
  varFields,
} from './workflowTemplate'

const compare: WorkflowSpec = {
  title: 'compare: ${vars.task}',
  vars: {
    task: { required: true, desc: 'task prompt' },
    project: { required: true, desc: 'project key' },
    agent_b: { default: 'codex', desc: 'second agent' },
    agent_a: { default: 'claude', desc: 'first agent' },
    runner: { desc: 'runner key' },
    note: {},
  },
  steps: [
    {
      name: 'compare',
      project_key: '${vars.project}',
      agent: '',
      agents: ['${vars.agent_a}', '${vars.agent_b}'],
      runner: '${vars.runner}',
      prompt: '${vars.task}',
      worktree: true,
      join: 'pick',
    },
  ],
}

describe('workflow template form', () => {
  it('infers var kinds from where the variable is used', () => {
    expect(inferVarKinds(compare)).toEqual({
      task: 'textarea',
      project: 'project',
      agent_a: 'agent',
      agent_b: 'agent',
      runner: 'runner',
      note: 'text',
    })
  })

  it('infers from usage even when the name gives no hint, including nested sub-workflows', () => {
    const spec: WorkflowSpec = {
      vars: { who: {}, where: {}, repo: {} },
      steps: [{ project_key: '', agent: '', runner: '', type: 'workflow', sub_workflow: { steps: [{ project_key: '${vars.repo}', agent: '${vars.who}', runner: '${vars.where}' }] } }],
    }
    expect(inferVarKinds(spec)).toEqual({ who: 'agent', where: 'runner', repo: 'project' })
  })

  it('orders fields project first, then required, then optional by name', () => {
    expect(varFields(compare).map((f) => f.name)).toEqual(['project', 'task', 'agent_a', 'agent_b', 'note', 'runner'])
  })

  it('seeds defaults, binds the project var and validates required values', () => {
    const fields = varFields(compare)
    const values = initialValues(fields, 'self')
    expect(values).toMatchObject({ project: 'self', task: '', agent_a: 'claude', agent_b: 'codex', runner: '' })
    expect(validateVars(fields, values)).toEqual({ task: '必填' })
    expect(validateVars(fields, { ...values, task: '  ' })).toEqual({ task: '必填' })
    expect(validateVars(fields, { ...values, task: 'fix' })).toEqual({})
  })

  it('does not send empty values so server defaults apply', () => {
    const fields = varFields(compare)
    const values = { ...initialValues(fields, 'self'), task: 'fix it', agent_b: '' }
    expect(cleanVars(fields, values)).toEqual({ project: 'self', task: 'fix it', agent_a: 'claude' })
  })

  it('filters agent options by project allowlist and keeps an unavailable current value visible', () => {
    const agents = [{ key: 'claude', type: 'cli-agent' }, { key: 'codex', type: 'cli-agent' }, { key: 'exec', type: 'exec' }] as MetaAgent[]
    const proj = { key: 'p', allowed_agents: ['claude', 'exec'], allowed_runners: [] } as MetaProject
    expect(agentOptions(proj, agents, 'claude').map((o) => o.value)).toEqual(['claude', 'exec'])
    const withMissing = agentOptions(proj, agents, 'codex')
    expect(withMissing[0]).toMatchObject({ value: 'codex' })
    expect(withMissing[0].label).toContain('未开放')
    expect(agentOptions(undefined, agents, '').map((o) => o.value)).toEqual(['claude', 'codex', 'exec'])
  })

  it('filters runner options by allowed_runners, adds a project-default entry and greys blocked ones', () => {
    const runners = [{ name: 'local', type: 'local' }, { name: 'w1', type: 'worker' }, { name: 'peer', type: 'peer-http' }] as MetaRunner[]
    const proj = { key: 'p', allowed_agents: [], allowed_runners: ['local', 'w1'] } as MetaProject
    const opts = runnerPickOptions(proj, runners, { w1: { short: '无在线 worker' } }, '', (r) => `${r.name === 'local' ? 'server' : r.name} · ${r.type}`)
    expect(opts.map((o) => o.value)).toEqual(['', 'local', 'w1'])
    expect(opts[1].label).toBe('server · local')
    expect(opts[2]).toMatchObject({ disabled: true })
    expect(opts[2].label).toContain('无在线 worker')
    expect(opts[0].label).toBe('项目默认')
  })

  it('labels template sources', () => {
    expect(sourceLabel('builtin')).toBe('内置')
    expect(sourceLabel('project')).toBe('项目')
    expect(sourceLabel('global')).toBe('全局')
  })

  it('turns a rendered spec into preview rows', () => {
    const rendered: WorkflowSpec = {
      steps: [
        { name: 'compare', project_key: 'self', agent: '', agents: ['claude', 'codex'], runner: '', prompt: 'fix\n  the bug', worktree: true, join: 'pick' },
        { name: 'plan', project_key: 'self', agent: 'claude', runner: 'local', prompt: 'x'.repeat(200), read_only: true },
      ],
    }
    const rows = previewSteps(rendered)
    expect(rows[0]).toMatchObject({ index: 1, name: 'compare', who: 'claude / codex', summary: 'fix the bug' })
    expect(rows[0].badges).toEqual(['并行 ×2', 'join:pick', 'worktree'])
    expect(rows[1].badges).toEqual(['只读'])
    expect(rows[1].summary.endsWith('…')).toBe(true)
  })
})

describe('adaptAgentVars', () => {
  const agents: MetaAgent[] = [
    { key: 'claude', type: 'cli-agent' },
    { key: 'codex', type: 'cli-agent' },
    { key: 'omp', type: 'cli-agent' },
    { key: 'exec', type: 'exec' },
  ]
  const fields = varFields(compare)
  const base = initialValues(fields, 'p')

  it('swaps a default agent the project does not allow for the first allowed same-kind agent', () => {
    const project = { key: 'p', allowed_agents: ['omp', 'exec'], allowed_runners: [] } as unknown as MetaProject
    const r = adaptAgentVars(fields, base, project, agents)
    expect(r.values.agent_a).toBe('omp')
    // codex is not allowed either; omp is already taken by agent_a, so it falls back to the only candidate
    expect(r.values.agent_b).toBe('omp')
    expect(r.notes).toHaveLength(2)
    expect(r.notes[0]).toContain('claude')
  })

  it('prefers an agent not yet used by another variable', () => {
    const project = { key: 'p', allowed_agents: ['codex', 'omp'], allowed_runners: [] } as unknown as MetaProject
    const r = adaptAgentVars(fields, base, project, agents)
    expect(r.values).toMatchObject({ agent_a: 'omp', agent_b: 'codex' })
    expect(r.notes).toHaveLength(1)
  })

  it('leaves everything alone when the project allows the defaults or has no allowlist', () => {
    const open = { key: 'p', allowed_agents: [], allowed_runners: [] } as unknown as MetaProject
    expect(adaptAgentVars(fields, base, open, agents)).toEqual({ values: base, notes: [] })
    const ok = { key: 'p', allowed_agents: ['claude', 'codex'], allowed_runners: [] } as unknown as MetaProject
    expect(adaptAgentVars(fields, base, ok, agents).notes).toEqual([])
  })
})
