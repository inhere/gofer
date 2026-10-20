import { describe, expect, it } from 'vitest'
import { sessionMessageAuthorLabel } from './sessionMessaging'

describe('sessionMessageAuthorLabel', () => {
  it('calls an answer to the open turn a direct reply', () => {
    expect(sessionMessageAuthorLabel({ channel: 'turn' })).toBe('你（回复）')
  })

  it('keeps the relayed label for the other delivery paths', () => {
    for (const channel of ['messenger', 'command', 'tmux', undefined]) {
      expect(sessionMessageAuthorLabel({ channel })).toBe('你（经转达）')
    }
  })
})
