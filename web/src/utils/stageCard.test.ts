import { beforeEach, describe, expect, it, vi } from 'vitest'
import { setLocale } from '../store/locale'
import type { TodayAction, TodayCard } from '../api/today'

// 「等我」里的流程卡（kind=stage）走的是现有的通用卡片动作：这里只核对动作 → 接口的映射。
const calls: string[] = []
vi.mock('../api/client', () => ({ requestWorkReport: () => Promise.resolve({}) }))
vi.mock('../api/stage', () => ({
  stageApprove: (id: string) => {
    calls.push(`stageApprove(${id})`)
    return Promise.resolve({})
  },
  stageBack: (id: string, reason: string) => {
    calls.push(`stageBack(${id},${reason})`)
    return Promise.resolve({})
  },
}))

import { actionLabel, cardLink, isLinkAction, runCardAction } from './today'
import { itemLink, kindLabelKey } from './inbox'

function card(over: Partial<TodayCard> = {}): TodayCard {
  return {
    key: 'stage:w-1',
    kind: 'stage',
    tag: 'approval',
    urgency: 'normal',
    blocks: { score: 0, items: 0 },
    title: '工作项「导出」的评审等你批准',
    summary: '缺评审 job',
    waiting_since: 100,
    activity_at: 100,
    refs: { work_item_id: 'w-1' },
    actions: [],
    advice: null,
    ...over,
  } as TodayCard
}

function act(id: string, over: Partial<TodayAction> = {}): TodayAction {
  return { id, label: id, ...over }
}

beforeEach(() => {
  setLocale('zh-CN')
  calls.length = 0
})

describe('stage waiting card', () => {
  it('approves in place', async () => {
    await runCardAction(card(), act('stage_approve', { label: '批准' }))()
    expect(calls).toEqual(['stageApprove(w-1)'])
  })

  it('sends the typed note back with the flow', async () => {
    await runCardAction(card(), act('stage_back', { label: '附意见退回', needs_text: true }), '设计还差验收标准')()
    expect(calls).toEqual(['stageBack(w-1,设计还差验收标准)'])
  })

  it('opens the work item for the open action (a link, no write)', () => {
    const open = act('open', { label: '看详情', style: 'link', value: '/work?id=w-1' })
    expect(isLinkAction(card(), open)).toBe(true)
    expect(cardLink(card())).toBe('/work?id=w-1')
  })

  it('translates the flow actions to the interface language', () => {
    expect(actionLabel({ id: 'stage_approve', label: '批准' })).toBe('批准')
    expect(actionLabel({ id: 'stage_back', label: '附意见退回' })).toBe('附意见退回')
  })

  it('shows up in the inbox with its own label and opens that work item', () => {
    expect(kindLabelKey('stage')).toBe('inbox.kind.stage')
    expect(itemLink({ kind: 'stage', refs: { work_item_id: 'w-1' } })).toBe('/work?id=w-1')
    // 服务端给了站内深链就用它（卡片动作的 value 也是 /work?id=…）
    expect(itemLink({ kind: 'stage', deep_link: '/work?id=w-9', refs: {} })).toBe('/work?id=w-9')
  })
})
