import { describe, expect, it } from 'vitest'
import type { AgentSession, MessengerAgent, Runner } from '../api/types'
import {
  MESSENGER_LIST_MIN_PROTOCOL,
  crossReference,
  idleLeftText,
  messageBlockReason,
  messengerDisplay,
  messengerListBlock,
} from './messenger'

const local = (extra: Partial<Runner> = {}): Runner => ({ name: 'local', type: 'local', status: 'up', ...extra })
const worker = (extra: Partial<Runner> = {}): Runner => ({
  name: 'w1', type: 'worker', status: 'connected', worker_id: 'w1',
  worker: { last_heartbeat: 1, heartbeat_age_ms: 0, in_flight: 0, protocol_version: MESSENGER_LIST_MIN_PROTOCOL },
  ...extra,
})

describe('messengerDisplay', () => {
  it('shows the three Chinese states with a tone each', () => {
    expect(messengerDisplay(local({ messenger_detail: { status: 'stopped' } }))).toEqual({ text: '未启动', tone: 'stopped', known: true })
    expect(messengerDisplay(local({ messenger_detail: { status: 'idle' } }))).toEqual({ text: '空闲', tone: 'idle', known: true })
    expect(messengerDisplay(local({ messenger_detail: { status: 'busy' } }))).toEqual({ text: '处理中', tone: 'busy', known: true })
  })

  it('falls back to the plain status string on the local row', () => {
    expect(messengerDisplay(local({ messenger: 'idle' })).text).toBe('空闲')
  })

  it('shows unknown, not stopped, for a worker that never reported a snapshot', () => {
    // 旧 worker：只有注册时的 messenger_status，没有心跳快照
    const old = worker({ worker: { last_heartbeat: 1, heartbeat_age_ms: 0, in_flight: 0, messenger_status: 'running' } })
    expect(messengerDisplay(old)).toEqual({ text: '未知', tone: 'unknown', known: false })
    expect(messengerDisplay(worker({ status: 'disconnected' })).text).toBe('离线')
  })

  it('keeps an unrecognised status visible instead of hiding it', () => {
    const d = messengerDisplay(local({ messenger_detail: { status: 'weird' } }))
    expect(d.text).toBe('weird')
    expect(d.known).toBe(false)
  })
})

describe('messengerListBlock', () => {
  it('allows local and a v16 worker', () => {
    expect(messengerListBlock(local())).toBe('')
    expect(messengerListBlock(worker())).toBe('')
  })
  it('explains why an old or offline worker cannot list', () => {
    const old = worker({ worker: { last_heartbeat: 1, heartbeat_age_ms: 0, in_flight: 0, protocol_version: 15 } })
    expect(messengerListBlock(old)).toContain('v15')
    expect(messengerListBlock(old)).toContain('升级')
    expect(messengerListBlock(worker({ status: 'disconnected', worker: undefined }))).toContain('离线')
    expect(messengerListBlock({ name: 'p', type: 'peer-http', status: 'up' })).toContain('没有传话人')
  })
})

describe('idleLeftText', () => {
  it('counts down to the idle exit', () => {
    expect(idleLeftText({ status: 'idle', idle_deadline: 1000 + 300 }, 1000)).toBe('5 分钟后空闲退出')
    expect(idleLeftText({ status: 'idle', idle_deadline: 1000 + 20 }, 1000)).toBe('20 秒后空闲退出')
    expect(idleLeftText({ status: 'idle', idle_deadline: 900 }, 1000)).toBe('即将空闲退出')
    expect(idleLeftText({ status: 'stopped' }, 1000)).toBe('')
  })
})

const session = (extra: Partial<AgentSession>): AgentSession => ({
  session_id: 'dc779b00-aaaa', agent: 'claude', state: 'idle', relay_mode: 'auto', auto_armed: false, idle_sec: -1,
  turn_no: 0, last_seen_at: 1, started_at: 1, peer_messaging: true, ...extra,
})

describe('crossReference', () => {
  const agents: MessengerAgent[] = [
    { name: 'proj-a', short_id: 'dc779b' },
    { name: 'other', short_id: 'ffffff' },
    { name: 'named', short_id: '123456' },
  ]
  it('matches a registered session by id prefix or by peer name', () => {
    const rows = crossReference(agents, [session({}), session({ session_id: 'zzz', peer_name: 'named' })])
    expect(rows[0].session?.session_id).toBe('dc779b00-aaaa')
    expect(rows[1].session).toBeNull()
    expect(rows[2].session?.session_id).toBe('zzz')
  })
  it('does not match on a short or empty id', () => {
    const rows = crossReference([{ name: 'x', short_id: 'dc' }, { name: 'y' }], [session({})])
    expect(rows.every((r) => r.session === null)).toBe(true)
  })
})

describe('messageBlockReason', () => {
  it('only lets a registered, live, addressable session be messaged', () => {
    const base = { agent: { name: 'a' } }
    expect(messageBlockReason({ ...base, session: null })).toContain('没有登记')
    expect(messageBlockReason({ ...base, session: session({ state: 'ended' }) })).toContain('已结束')
    expect(messageBlockReason({ ...base, session: session({ peer_messaging: false }) })).toContain('SendMessage')
    expect(messageBlockReason({ ...base, session: session({}) })).toBe('')
  })
})
