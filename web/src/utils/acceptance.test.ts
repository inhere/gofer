import { describe, expect, it } from 'vitest'
import { acceptanceLines } from './acceptance'

describe('acceptanceLines', () => {
  it('splits list items and drops task-box markers', () => {
    expect(acceptanceLines('- builds\n* [ ] vet clean\n+ [x] tests pass\n1. docs\n2) skill')).toEqual([
      { kind: 'item', text: 'builds' },
      { kind: 'item', text: 'vet clean' },
      { kind: 'item', text: 'tests pass' },
      { kind: 'item', text: 'docs' },
      { kind: 'item', text: 'skill' },
    ])
  })
  it('keeps prose as text blocks and folds indented continuation lines', () => {
    expect(acceptanceLines('Must hold:\nall of it\n\n- one\n  more of one\n- two\n\ntrailing note')).toEqual([
      { kind: 'text', text: 'Must hold:\nall of it' },
      { kind: 'item', text: 'one more of one' },
      { kind: 'item', text: 'two' },
      { kind: 'text', text: 'trailing note' },
    ])
  })
  it('handles empty input', () => {
    expect(acceptanceLines('')).toEqual([])
    expect(acceptanceLines(undefined)).toEqual([])
    expect(acceptanceLines('  \n\n')).toEqual([])
  })
})
