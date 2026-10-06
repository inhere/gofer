import { describe, expect, it } from 'vitest'
import type { AgentInfo } from '../api/types'
import type { StewardSettings, StewardStatus } from '../api/steward'
import {
  QUICK_QUESTIONS,
  acpAgentOptions,
  displayPrompt,
  agentSwitchWarning,
  byteLength,
  mergeSuggestionText,
  nextSubscription,
  notesConflictOf,
  notesSizeLabel,
  settingsDiff,
  stateLabel,
} from './steward'

const agent = (key: string, type: string, available = true): AgentInfo => ({ key, type, available })

const loaded: StewardSettings = {
  enabled: false, agent: '', project: 'default', review_time: '08:50', idle_end_min: 30,
  review_max_items: 20, event_wake: false, event_throttle_min: 30, review_time_explicit: false,
}
const form = { enabled: false, agent: '', project: '', review_time: '', idle_end_min: '30', event_wake: false }

describe('steward helpers', () => {
  it('labels the session state in Chinese', () => {
    expect(stateLabel('not_started')).toBe('未启动')
    expect(stateLabel('running')).toBe('运行中')
    expect(stateLabel('idle')).toBe('空闲')
    expect(stateLabel('idle', false)).toBe('未启用')
  })

  it('lists only installed acp-agents but keeps the current value visible', () => {
    const agents = [agent('claude-acp', 'acp-agent'), agent('codex-acp', 'acp-agent', false), agent('claude', 'cli-agent'), agent('omp-acp', 'acp-agent')]
    expect(acpAgentOptions(agents, 'claude-acp').map((o) => o.key)).toEqual(['claude-acp', 'omp-acp'])
    const withMissing = acpAgentOptions(agents, 'codex-acp')
    expect(withMissing[0]).toMatchObject({ key: 'codex-acp', missing: true })
    expect(withMissing[0].label).toContain('本机不可用')
    expect(acpAgentOptions(agents, 'claude')[0].label).toContain('不是 acp-agent')
    expect(acpAgentOptions(agents, 'ghost')[0].label).toContain('未找到')
  })

  it('sends only changed settings', () => {
    expect(settingsDiff(loaded, form)).toEqual({})
    expect(settingsDiff(loaded, { ...form, enabled: true, agent: 'claude-acp', idle_end_min: '45' })).toEqual({ enabled: true, agent: 'claude-acp', idle_end_min: 45 })
    expect(settingsDiff(loaded, { ...form, review_time: '08:15' })).toEqual({ review_time: '08:15' })
    expect(settingsDiff({ ...loaded, review_time_explicit: true }, { ...form, review_time: '' })).toEqual({ review_time: '' })
    expect(settingsDiff(loaded, { ...form, project: 'ops' })).toEqual({ project: 'ops' })
  })

  it('warns that switching agent ends the live session', () => {
    const st = { state: 'idle', agent: 'a', job_agent: 'a' } as StewardStatus
    expect(agentSwitchWarning(st, 'b')).toContain('当前管家会话会结束')
    expect(agentSwitchWarning(st, 'a')).toBe('')
    expect(agentSwitchWarning({ ...st, state: 'not_started' }, 'b')).toBe('')
  })

  it('turns a 409 notes response into a conflict with the latest version', () => {
    const err = { status: 409, body: { current: { version: 3, body: 'theirs' } } }
    expect(notesConflictOf(err, 'mine')).toEqual({ current: { version: 3, body: 'theirs' }, mine: 'mine' })
    expect(notesConflictOf({ status: 500 }, 'mine')).toBeNull()
    expect(notesConflictOf(null, 'mine')).toBeNull()
  })

  it('has the three quick questions', () => {
    expect(QUICK_QUESTIONS).toEqual(['我手上还有什么没完成？', '今天去现场要做什么？', '把等资源的整理成清单'])
  })

  it('decides when the panel must resubscribe and whether the steward was rebuilt', () => {
    expect(nextSubscription('', 'j1')).toEqual({ jobId: 'j1', resubscribe: true, rebuilt: false })
    expect(nextSubscription('j1', 'j1')).toEqual({ jobId: 'j1', resubscribe: false, rebuilt: false })
    expect(nextSubscription('j1', 'j2')).toEqual({ jobId: 'j2', resubscribe: true, rebuilt: true })
    expect(nextSubscription('j1', undefined)).toEqual({ jobId: 'j1', resubscribe: false, rebuilt: false })
  })

  it('formats sizes and merge suggestions', () => {
    expect(notesSizeLabel(512)).toBe('512B')
    expect(notesSizeLabel(2048)).toBe('2.0KB')
    const t = (id: string) => ({ a: '登录问题', b: '登录修复' })[id] ?? id
    expect(mergeSuggestionText({ id: 1, target_id: 'b', source_id: 'a', reason: '同一件事', by: 'steward(x)', at: 0, state: 'pending' }, t)).toBe('管家建议把「登录问题」并入「登录修复」：同一件事')
    expect(mergeSuggestionText({ id: 1, target_id: 'b', source_id: 'a', by: 'x', at: 0, state: 'pending' }, t)).toBe('管家建议把「登录问题」并入「登录修复」')
  })

  it('shows only what the person said, not the 24KB prime or the review instructions', () => {
    const prime = '# 你是工作管家（gofer steward）\n\n很长的角色说明……\n\n## 未结工作项\n- w-1 [进行中] x'
    const ask = '## 来自 human:me 的提问\n\n我手上还有什么没完成？\n\n（先用 gofer_work_list / gofer_work_get 核对最新数据再回答；中文，简洁。）'
    expect(displayPrompt(`${prime}\n\n---\n\n${ask}`)).toBe('（管家已启动：已注入角色说明、笔记和工作项清单）\n我手上还有什么没完成？')
    expect(displayPrompt(`${prime}\n\n---\n\n读完以上内容后，只回复一行：管家已就绪`)).toBe('（管家已启动：已注入角色说明、笔记和工作项清单）')
    expect(displayPrompt(ask)).toBe('我手上还有什么没完成？')
    expect(displayPrompt('## 每日巡检（2026-10-06）\n\n只处理下面这 2 个工作项')).toBe('（系统）每日巡检（2026-10-06）')
    expect(displayPrompt('普通的一句话')).toBe('普通的一句话')
  })

  it('counts notes in UTF-8 bytes like the server', () => {
    expect(byteLength('abc')).toBe(3)
    expect(byteLength('周三')).toBe(6)
  })
})
