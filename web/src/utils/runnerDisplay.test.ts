import { describe, expect, it } from 'vitest'
import { isLocalRunnerName, runnerLabel, runnerLabelNote, runnerOptionText } from './runnerDisplay'

describe('runnerDisplay', () => {
  it('shows the builtin local runner as server and leaves other names alone', () => {
    expect(runnerLabel('local')).toBe('server')
    expect(runnerLabel('server')).toBe('server')
    expect(runnerLabel('w-docker-claude')).toBe('w-docker-claude')
    expect(runnerLabel('')).toBe('')
    expect(runnerLabel(undefined)).toBe('')
  })

  it('annotates the local runner for cards and group titles', () => {
    expect(runnerLabelNote('local')).toBe('server（本机）')
    expect(runnerLabelNote('w1')).toBe('w1')
  })

  it('renders dropdown option text without the local · local duplication', () => {
    expect(runnerOptionText({ name: 'local', type: 'local' })).toBe('server · 本机')
    expect(runnerOptionText({ name: 'w1', type: 'worker', worker_id: 'wk1' })).toBe('w1 · worker · wk1')
    expect(runnerOptionText({ name: 'peer', type: 'peer-http' })).toBe('peer · peer-http')
  })

  it('treats both spellings as the local runner', () => {
    expect(isLocalRunnerName('local')).toBe(true)
    expect(isLocalRunnerName('server')).toBe(true)
    expect(isLocalRunnerName('w1')).toBe(false)
  })
})
