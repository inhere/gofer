import { afterEach, describe, expect, it } from 'vitest'
import { createSSRApp, h, nextTick, ref } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import type { InboxItem, InboxResponse } from '../api/inbox'
import type { TodayAction, TodayCard, TodayCardKind, TodayResponse } from '../api/today'
import type { WaitingItem } from '../api/types'
import type { LiveClient, LiveHandler, LiveMessage } from '../api/live'
import { createWaitingStore, setWaitingStoreForTest } from '../composables/useWaiting'
import { createWaitingPop, setWaitingPopForTest } from '../composables/useWaitingPop'
import type { SnoozeOption } from '../utils/todaySnooze'
import WaitingPopCard from './WaitingPopCard.vue'

// 顶部「等我」弹卡（T5）：推送快照里新出现一个会话主体等回复 / 等批准的项就弹一件，其余 +N；
// 关闭写收件箱 seen（多端同步），另一客户端看到 seen 就不再弹；非会话项不弹。
// 项目里没有 DOM 测试环境，弹卡渲染用 SSR 断言，队列与写接口用 composable 的 seam 断言。

function fakeClient(): { client: LiveClient; push(topic: string, m: LiveMessage): void } {
  const handlers = new Map<string, Set<LiveHandler>>()
  return {
    client: {
      subscribe(topic, h) {
        let set = handlers.get(topic)
        if (!set) {
          set = new Set()
          handlers.set(topic, set)
        }
        set.add(h)
        return () => {
          set!.delete(h)
        }
      },
      noteSeq() {},
      status: () => 'connected',
      close() {},
    },
    push(topic, m) {
      handlers.get(topic)?.forEach((h) => h(m))
    },
  }
}

function item(key: string, kind: TodayCardKind = 'relay', extra: Partial<WaitingItem> = {}): WaitingItem {
  return { key, category: 'answer', kind, waiting_since: 100, title: key, ...extra }
}

function card(over: Partial<TodayCard> = {}): TodayCard {
  return {
    key: 'relay:s1',
    kind: 'relay',
    tag: '会话',
    urgency: 'normal',
    blocks: { score: 0, items: 0 },
    title: '周报会话',
    project_key: 'p1',
    waiting_since: 100,
    activity_at: 100,
    summary: '这周做了什么',
    refs: { session_id: 's1', thread_id: 'r:s1' },
    actions: [
      { id: 'reply', label: '回复', style: 'primary', needs_text: true },
      { id: 'ack', label: '已读' },
    ],
    waiting: 'answer',
    waiting_label: '待你回答',
    advice: null,
    ...over,
  }
}

const APPROVAL_CARD = card({
  key: 'approval:j1',
  kind: 'approval',
  title: '推送 job',
  refs: { job_id: 'j1' },
  waiting: 'approval',
  waiting_label: '待你批准',
  actions: [
    { id: 'approve', label: '批准', style: 'ok' },
    { id: 'reject', label: '拒绝', style: 'bad' },
  ],
})

function today(cards: TodayCard[]): TodayResponse {
  return {
    digest: { since_last: { since: 0, jobs_done: 0, jobs_failed: 0, commits: 0 }, title: '', text: '' },
    waiting: cards,
    running: { sessions: 0, jobs: 0 },
    snoozed: 0,
    status: { runners: { online: 0, total: 0, running_jobs: 0 }, version: 'v0', alerts: [] },
    generated_at: 0,
  }
}

function inboxItem(key: string, state: InboxItem['state'], over: Partial<InboxItem> = {}): InboxItem {
  return {
    key,
    column: 'action',
    kind: 'relay',
    title: key,
    summary: '',
    source_revision: 'rev-1',
    state,
    actions: [],
    refs: { session_id: 's1' },
    activity_at: 0,
    ...over,
  }
}

function inbox(items: InboxItem[]): InboxResponse {
  return {
    columns: { action: items, fyi: [] },
    unread: { action: items.filter((it) => it.state === 'unread').length, fyi: 0 },
    total_unread: 0,
    generated_at: 0,
  }
}

interface Rig {
  push(topic: string, m: LiveMessage): void
  seen: string[]
  refetches: number[]
  boxRefetches: number[]
  box: ReturnType<typeof ref<InboxResponse | null>>
  acts: Array<{ card: TodayCard; action: TodayAction; text?: string }>
  snoozes: Array<{ card: TodayCard; opt: SnoozeOption }>
  pop: ReturnType<typeof createWaitingPop>
}

// rig 搭一套：推送客户端 + 弹卡队列（写接口换成记录用的桩）+ 固定的 today / inbox 快照。
function rig(opts: { cards?: TodayCard[]; box?: InboxResponse | null } = {}): Rig {
  const f = fakeClient()
  const waiting = createWaitingStore({ client: f.client })
  setWaitingStoreForTest(waiting)
  const seen: string[] = []
  const acts: Rig['acts'] = []
  const snoozes: Rig['snoozes'] = []
  const refetches: number[] = []
  const boxRefetches: number[] = []
  const box = ref<InboxResponse | null>(opts.box ?? null)
  const pop = createWaitingPop({
    waiting,
    inbox: box,
    today: ref<TodayResponse | null>(today(opts.cards ?? [card()])),
    refresh: (delayMs?: number) => refetches.push(delayMs ?? 0),
    refreshInbox: (delayMs?: number) => boxRefetches.push(delayMs ?? 0),
    markSeen: async (key: string) => {
      seen.push(key)
      return {}
    },
    act: (c: TodayCard, a: TodayAction, text?: string) => acts.push({ card: c, action: a, text }),
    snooze: (c: TodayCard, opt: SnoozeOption) => snoozes.push({ card: c, opt }),
  })
  setWaitingPopForTest(pop)
  return { push: f.push, seen, refetches, boxRefetches, box, acts, snoozes, pop }
}

function render(): Promise<string> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:p(.*)*', component: { render: () => null } }],
  })
  const app = createSSRApp({ render: () => h(WaitingPopCard as never) })
  app.use(router)
  return renderToString(app)
}

// 首帧只建立基线（不弹），之后新出现的项才触发 onEnter。
function baseline(r: Rig): void {
  r.push('waiting', { type: 'snap', data: [] })
}

afterEach(() => {
  setWaitingPopForTest(null)
  setWaitingStoreForTest(null)
})

describe('WaitingPopCard · 该不该弹', () => {
  it('pops a session that starts waiting for a reply, with its title, summary and buttons', async () => {
    const r = rig()
    baseline(r)
    expect(await render()).not.toContain('data-test="waiting-pop"')

    r.push('waiting', { type: 'snap', data: [item('session:s1')] })
    await nextTick()
    const html = await render()
    expect(html).toContain('data-test="waiting-pop"')
    expect(html).toContain('data-test="waiting-pop-title"')
    expect(html).toContain('周报会话')
    expect(html).toContain('这周做了什么')
    expect(html).toContain('待你回答')
    // 回复卡：作答 / 打开 / 稍后 / 关闭；卡上没有批准动作就没有批准按钮
    expect(html).toContain('data-test="waiting-pop-reply"')
    expect(html).not.toContain('data-test="waiting-pop-approve"')
    expect(html).toContain('data-test="waiting-pop-open"')
    expect(html).toContain('data-test="waiting-pop-snooze"')
    expect(html).toContain('data-test="waiting-pop-close"')
    // 卡已在 today 里：不必重拉
    expect(r.refetches).toEqual([])
  })

  it('refetches the card when the push arrives before GET /v1/today has it', async () => {
    const r = rig({ cards: [] })
    baseline(r)
    r.push('waiting', { type: 'snap', data: [item('session:s1')] })
    await nextTick()
    // 推送只带 key / 类别，按钮与摘要在卡上：没有卡就 0ms 重拉一次
    expect(r.refetches).toEqual([0])
    // 卡还没到时也能弹（标题用推送里的），只是没有作答 / 批准按钮
    const html = await render()
    expect(html).toContain('data-test="waiting-pop"')
    expect(html).not.toContain('data-test="waiting-pop-reply"')
    expect(html).toContain('data-test="waiting-pop-open"')
  })

  it('pops an approval with the approve button and sends it through the card action', async () => {
    const r = rig({ cards: [APPROVAL_CARD] })
    baseline(r)
    r.push('waiting', { type: 'snap', data: [item('job:j1', 'approval', { category: 'approval' })] })
    await nextTick()
    const html = await render()
    expect(html).toContain('data-test="waiting-pop-approve"')
    expect(html).toContain('推送 job')
    expect(html).not.toContain('data-test="waiting-pop-reply"')
    expect(r.pop.approve()).toBe(true)
    expect(r.acts.map((a) => a.action.id)).toEqual(['approve'])
    expect(r.pop.queue.value).toEqual([])
  })

  it('never pops a non-session item: a failed job, a review, an expiring token or a stuck plan', async () => {
    const r = rig()
    baseline(r)
    r.push('waiting', {
      type: 'snap',
      data: [
        item('job:j1', 'job.failed', { category: 'failed' }),
        item('job:j2', 'review', { category: 'review' }),
        item('caller:c1', 'caller.token_expiring', { category: 'attention' }),
        item('plan:p1', 'plan_blocked', { category: 'plan_stuck' }),
      ],
    })
    await nextTick()
    expect(r.pop.queue.value).toEqual([])
    expect(await render()).not.toContain('data-test="waiting-pop"')
  })
})

describe('WaitingPopCard · 一次一件与 +N', () => {
  it('keeps the arrival order, shows the current one and +N for the rest', async () => {
    const r = rig({ cards: [card(), APPROVAL_CARD] })
    baseline(r)
    r.push('waiting', { type: 'snap', data: [item('session:s1')] })
    r.push('waiting', {
      type: 'snap',
      data: [item('session:s1'), item('job:j1', 'approval', { category: 'approval' })],
    })
    await nextTick()
    expect(r.pop.queue.value).toEqual(['session:s1', 'job:j1'])
    // 两件的卡都已在 today 里：不必重拉
    expect(r.refetches).toEqual([])
    expect(r.pop.extra.value).toBe(1)
    const html = await render()
    expect(html).toContain('data-test="waiting-pop-extra"')
    expect(html).toContain('+1')
    expect(html).toContain('周报会话')
    expect(html).not.toContain('推送 job')
  })

  it('switches to the next one once the current leaves the waiting list', async () => {
    const r = rig({ cards: [card(), APPROVAL_CARD] })
    baseline(r)
    r.push('waiting', { type: 'snap', data: [item('session:s1')] })
    r.push('waiting', {
      type: 'snap',
      data: [item('session:s1'), item('job:j1', 'approval', { category: 'approval' })],
    })
    await nextTick()
    // 会话那件被别处答复了：列表里没有它了
    r.push('waiting', { type: 'snap', data: [item('job:j1', 'approval', { category: 'approval' })] })
    await nextTick()
    expect(r.pop.queue.value).toEqual(['job:j1'])
    const html = await render()
    expect(html).toContain('推送 job')
    expect(html).not.toContain('周报会话')
    expect(html).not.toContain('data-test="waiting-pop-extra"')
  })
})

describe('WaitingPopCard · 关闭与多端同步', () => {
  it('closes by writing the inbox seen state of the item and moves on to the next one', async () => {
    const box = inbox([
      inboxItem('relay:s1', 'unread'),
      inboxItem('approval:j1', 'unread', { kind: 'approval', refs: { job_id: 'j1' } }),
    ])
    const r = rig({ box, cards: [card(), APPROVAL_CARD] })
    baseline(r)
    r.push('waiting', { type: 'snap', data: [item('session:s1')] })
    r.push('waiting', {
      type: 'snap',
      data: [item('session:s1'), item('job:j1', 'approval', { category: 'approval' })],
    })
    await nextTick()
    r.pop.close()
    // seen 写的是收件箱条目自己的 key（"relay:s1"），不是主体 key
    expect(r.seen).toEqual(['relay:s1'])
    expect(r.pop.queue.value).toEqual(['job:j1'])
    await nextTick()
    expect(await render()).toContain('推送 job')
  })

  it('does not pop a subject another client already closed (its inbox state is seen)', async () => {
    const r = rig({ box: inbox([inboxItem('relay:s1', 'seen')]) })
    baseline(r)
    r.push('waiting', { type: 'snap', data: [item('session:s1')] })
    await nextTick()
    expect(r.pop.queue.value).toEqual([])
    expect(await render()).not.toContain('data-test="waiting-pop"')
  })

  it('pulls the inbox state first when the snapshot has no entry yet, so a closed item stays closed', async () => {
    const r = rig({ box: null })
    baseline(r)
    r.push('waiting', { type: 'snap', data: [item('session:s1')] })
    await nextTick()
    expect(r.boxRefetches).toEqual([0])
    // 收件箱快照回来后发现别的端已经关过（seen）：当前这件也不再弹
    r.box.value = inbox([inboxItem('relay:s1', 'seen')])
    await nextTick()
    expect(r.pop.queue.value).toEqual([])
    expect(await render()).not.toContain('data-test="waiting-pop"')
  })

  it('still pops when the inbox has no entry for it (inbox switched off)', async () => {
    const r = rig({ box: null })
    baseline(r)
    r.push('waiting', { type: 'snap', data: [item('session:s1')] })
    await nextTick()
    expect(r.pop.queue.value).toEqual(['session:s1'])
    // 收件箱没有这一项（功能关掉 / 旧 server）：快照回来也不该把它撤下
    r.box.value = inbox([])
    await nextTick()
    expect(r.pop.queue.value).toEqual(['session:s1'])
  })
})

describe('WaitingPopCard · 作答 / 打开 / 稍后', () => {
  it('sends a short reply through the card action the today page uses', () => {
    const r = rig()
    baseline(r)
    r.push('waiting', { type: 'snap', data: [item('session:s1')] })
    expect(r.pop.reply('先别推')).toBe(true)
    expect(r.acts).toHaveLength(1)
    expect(r.acts[0].action.id).toBe('reply')
    expect(r.acts[0].text).toBe('先别推')
    expect(r.pop.queue.value).toEqual([])
  })

  it('snoozes with the today page snooze and opens the thread link', () => {
    const r = rig()
    baseline(r)
    r.push('waiting', { type: 'snap', data: [item('session:s1')] })
    expect(r.pop.link.value).toBe('/workbench?thread=r%3As1')
    expect(r.pop.snooze()).toBe(true)
    expect(r.snoozes[0].opt.id).toBe('hour')
    expect(r.pop.queue.value).toEqual([])
  })
})
