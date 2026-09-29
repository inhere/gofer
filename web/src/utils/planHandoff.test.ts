import { describe, expect, it } from 'vitest'
import {
  handoffEditorDraft,
  isHistoricalVersion,
  mergeHandoffs,
} from './planHandoff'

const latest = { plan_id: 'p1', version: 2, body: 'latest', by: 'alice', at: 20 }
const older = { plan_id: 'p1', version: 1, body: 'older', by: 'bob', at: 10 }

describe('plan handoff editor state', () => {
  it('merges latest and history in descending version order without duplicates', () => {
    expect(mergeHandoffs(latest, [older, latest]).map((item) => item.version)).toEqual([2, 1])
  })

  it('allows editing only the latest version and leaves new drafts empty', () => {
    expect(isHistoricalVersion(1, 2)).toBe(true)
    expect(isHistoricalVersion(2, 2)).toBe(false)
    expect(handoffEditorDraft('edit', latest)).toBe('latest')
    expect(handoffEditorDraft('new', latest)).toBe('')
  })
})
