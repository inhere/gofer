import { describe, expect, it } from 'vitest'
import type { WorkbenchThread } from '../api/types'
import {
  acpThreadCountLabel,
  acpThreadIsEnded,
  acpThreadLink,
  acpThreadStatusKey,
  acpThreadStatusValue,
  acpThreadTone,
} from './acpThreads'

function thread(raw_status: string, status: WorkbenchThread['status'] = 'working'): WorkbenchThread {
  return {
    id: 's:session/one',
    kind: 'agent',
    status,
    raw_status,
    title: 'A session',
    project_key: 'project',
    resumable: true,
    started_at: 1,
    updated_at: 2,
    turns: 2,
  }
}

describe('ACP thread presentation', () => {
  it('uses raw status for ended, waiting, and running cards', () => {
    expect(acpThreadStatusValue(thread('awaiting_input'))).toBe('awaiting_input')
    expect(acpThreadStatusKey(thread('awaiting_input'))).toBe('sessions.acp.awaitingInput')
    expect(acpThreadTone(thread('awaiting_input'))).toBe('hot')
    expect(acpThreadStatusValue(thread('running'))).toBe('running')
    expect(acpThreadStatusValue(thread('done', 'done'))).toBe('ended')
    expect(acpThreadIsEnded(thread('done', 'done'))).toBe(true)
    expect(acpThreadTone(thread('done', 'done'))).toBe('off')
  })

  it('builds deep links and count labels without side effects', () => {
    expect(acpThreadLink('s:session/one')).toBe('/workbench?thread=s%3Asession%2Fone')
    expect(acpThreadCountLabel(50, false)).toBe('50')
    expect(acpThreadCountLabel(50, true)).toBe('50+')
  })
})
