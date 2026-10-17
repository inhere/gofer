import { describe, expect, it } from 'vitest'
import {
  CONTINUE_ERROR,
  continueApiErrorText,
  continueCandidates,
  continueFromValue,
  continuationLabels,
  continuationLines,
  defaultContinueFrom,
} from './todoContinue'

const todos = [
  { todo_id: 't1', title: '读代码' },
  { todo_id: 't2', title: '写实现' },
  { todo_id: 't3', title: '补测试' },
]

describe('continueCandidates / defaultContinueFrom', () => {
  it('lists the other todos of the plan in list order', () => {
    expect(continueCandidates(todos, 't2').map((t) => t.todo_id)).toEqual(['t1', 't3'])
    expect(continueCandidates(todos, '').map((t) => t.todo_id)).toEqual(['t1', 't2', 't3'])
  })

  it('defaults a new item to the last todo (it is appended) and an existing one to its predecessor', () => {
    expect(defaultContinueFrom(todos, '')).toBe('t3')
    expect(defaultContinueFrom(todos, 't2')).toBe('t1')
  })

  it('falls back to the first candidate when the item IS the first one', () => {
    expect(defaultContinueFrom(todos, 't1')).toBe('t2')
  })

  it('returns "" when the plan has no other todo, so the switch can stay off', () => {
    expect(defaultContinueFrom([], '')).toBe('')
    expect(defaultContinueFrom(todos, 't1')).toBe('t2')
    expect(defaultContinueFrom([{ todo_id: 't1', title: 'only' }], 't1')).toBe('')
  })
})

describe('continueFromValue', () => {
  it('sends the predecessor id only while the switch is on, and "" clears it', () => {
    expect(continueFromValue(true, ' t2 ')).toBe('t2')
    expect(continueFromValue(false, 't2')).toBe('')
    expect(continueFromValue(true, '')).toBe('')
  })
})

describe('continuationLabels / continuationLines', () => {
  it('renders nothing for a plain todo', () => {
    expect(continuationLabels({ todo_id: 't2', title: '写实现' }, todos)).toEqual({
      from: '', note: '', by: '',
    })
    expect(continuationLines({ todo_id: 't2', title: '写实现' }, todos)).toEqual([])
  })

  it('names the predecessor, the reason a dispatch fell back, and the successor slot holder', () => {
    const lines = continuationLines(
      { todo_id: 't2', title: '写实现', continue_from: 't1', continue_note: '前序还没完成', continued_by: 't3' },
      todos,
    )
    expect(lines).toEqual([
      { kind: 'from', text: '接续自 读代码' },
      { kind: 'note', text: '未接续：前序还没完成' },
      { kind: 'by', text: '已被 补测试 接续' },
    ])
  })

  it('shows each part on its own', () => {
    expect(continuationLabels({ todo_id: 't2', title: 'x', continue_from: 't1' }, todos).from).toBe('接续自 读代码')
    expect(continuationLines({ todo_id: 't2', title: 'x', continue_note: '换了 agent' }, todos)).toEqual([
      { kind: 'note', text: '未接续：换了 agent' },
    ])
    expect(continuationLines({ todo_id: 't1', title: 'x', continued_by: 't2' }, todos)).toEqual([
      { kind: 'by', text: '已被 写实现 接续' },
    ])
  })

  it('falls back to the raw id when the other item is not in the loaded list', () => {
    expect(continuationLabels({ todo_id: 't2', title: 'x', continue_from: 'gone' }, todos).from).toBe('接续自 gone')
    expect(continuationLabels({ todo_id: 't2', title: 'x', continued_by: 'gone' }, todos).by).toBe('已被 gone 接续')
  })

  it('ignores blank / missing fields (old servers)', () => {
    expect(continuationLines({ todo_id: 't2', title: 'x' }, todos)).toEqual([])
    expect(continuationLines({ todo_id: 't2', title: 'x', continue_from: '  ', continue_note: '' }, todos)).toEqual([])
  })
})

describe('continueApiErrorText', () => {
  it('shows the server reason of a rejected continue_from in the form', () => {
    expect(continueApiErrorText({ code: CONTINUE_ERROR, detail: 'todo t9 is not in this plan' })).toBe(
      '前序项无效：todo t9 is not in this plan',
    )
    expect(CONTINUE_ERROR).toBe('invalid continue_from')
  })

  it('still says something useful when the 400 carries no detail', () => {
    expect(continueApiErrorText({ code: CONTINUE_ERROR })).toContain('前序项无效')
  })

  it('leaves every other error to the generic error line', () => {
    expect(continueApiErrorText({ code: 'invalid budget', detail: 'max_tokens' })).toBe('')
    expect(continueApiErrorText(new Error('boom'))).toBe('')
    expect(continueApiErrorText(null)).toBe('')
  })
})

// 接线断言：纯函数必须真的被表单 / 卡片 / client 用上（?raw 读源码，不做 DOM 渲染）。
const planDetail = Object.values(
  import.meta.glob('../views/PlanDetail.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string
const planBoard = Object.values(
  import.meta.glob('../components/PlanBoard.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string
const client = Object.values(
  import.meta.glob('../api/client.ts', { eager: true, query: '?raw', import: 'default' }),
)[0] as string
const types = Object.values(
  import.meta.glob('../api/types.ts', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

describe('plan page wiring', () => {
  it('the todo form has the switch, the predecessor picker and the inline 400 line', () => {
    expect(planDetail).toContain('接着上一项的会话做')
    expect(planDetail).toContain('defaultContinueFrom')
    expect(planDetail).toContain('continueCandidates')
    expect(planDetail).toContain('continueFromValue')
    expect(planDetail).toContain('continueApiErrorText')
    expect(planDetail).toContain('v-model="newTodoContinueFrom"')
    expect(planDetail).toContain('v-model="editForm.continueFrom"')
    expect(planDetail).toContain('v-if="newTodoContinueError"')
    // 接续信息渲染在列表行（看板卡片在 PlanBoard）
    expect(planDetail).toContain('continuationLines')
  })

  it('board cards render the same continuation lines', () => {
    expect(planBoard).toContain('continuationLines')
    expect(planBoard).toContain('card.cont')
  })

  it('the client can send continue_from on create and the types carry the fields', () => {
    expect(client).toContain('continue_from: continueFrom')
    expect(types).toContain('continue_from?: string')
    expect(types).toContain('continued_by?: string')
    expect(types).toContain('continue_note?: string')
    expect(types).toContain('cache_read_tokens?: number')
    expect(types).toContain('cache_write_tokens?: number')
  })
})
