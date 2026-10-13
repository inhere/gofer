import { afterEach, describe, expect, it, vi } from 'vitest'
import type { UpdateStatus } from '../api/types'

const getUpdateStatus = vi.fn<() => Promise<UpdateStatus>>()
vi.mock('../api/client', () => ({ getUpdateStatus: () => getUpdateStatus() }))

const store = await import('./updateAvailable')

function status(over: Partial<UpdateStatus>): UpdateStatus {
  return { current: '0.153.1', newer: false, source: 'github', checked_at: 1, ...over }
}

afterEach(() => {
  store.stopUpdateAvailable()
  getUpdateStatus.mockReset()
})

describe('updateAvailable', () => {
  it('shows the latest version only when it is newer', () => {
    store.noteUpdateStatus(status({ latest: '0.160.0', newer: true }))
    expect(store.updateLatest.value).toBe('0.160.0')
    store.noteUpdateStatus(status({ latest: '0.153.1', newer: false }))
    expect(store.updateLatest.value).toBe('')
    store.noteUpdateStatus(null)
    expect(store.updateLatest.value).toBe('')
  })

  it('reads the server result and keeps the last value when the server is unreachable', async () => {
    getUpdateStatus.mockResolvedValueOnce(status({ latest: '0.160.0', newer: true }))
    await store.refreshUpdateAvailable()
    expect(store.updateLatest.value).toBe('0.160.0')
    getUpdateStatus.mockRejectedValueOnce(new Error('restarting'))
    await store.refreshUpdateAvailable()
    expect(store.updateLatest.value).toBe('0.160.0')
  })

  it('clears the badge when stopped', async () => {
    getUpdateStatus.mockResolvedValue(status({ latest: '0.160.0', newer: true }))
    store.startUpdateAvailable()
    await vi.waitFor(() => expect(store.updateLatest.value).toBe('0.160.0'))
    store.stopUpdateAvailable()
    expect(store.updateLatest.value).toBe('')
  })
})
