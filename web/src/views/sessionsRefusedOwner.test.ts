import { describe, expect, it } from 'vitest'

const sessions = Object.values(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const drawer = Object.values(
  import.meta.glob('../components/SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

// gofer-eh53：切 token 后 hook 以另一个 caller 身份上报被拒，会话状态停更。列表与详情
// 都要「有值才显示」地把这件事说出来，并给一个把会话转给该 caller 的按钮。
describe('refused session owner', () => {
  it('renders the notice, badge and transfer button only when a refusal is recorded', () => {
    // 有被拒记录才渲染：徽标 / 提示行 / 按钮都以 refused_caller_id 为条件。
    expect(sessions).toContain('v-if="s.refused_caller_id"')
    expect(sessions).toContain('data-test="refused-owner-badge"')
    expect(sessions).toContain('归属被拒')
    expect(sessions).toContain('data-test="refused-owner-notice"')
    expect(sessions).toContain('{{ refusedOwnerNotice(s) }}')
    expect(sessions).toContain('data-test="refused-owner-transfer"')
    expect(sessions).toContain('{{ ownerBusyIds.has(s.session_id) ? \'…\' : transferOwnerLabel(s) }}')
    expect(sessions).toContain('@click="onTransferOwner(s)"')
  })

  it('shows the server reason when the transfer is refused', () => {
    expect(sessions).toContain('data-test="refused-owner-error"')
    expect(sessions).toContain("t('sessions.card.ownerFailed', { err: ownerErrors.get(s.session_id) })")
    expect(sessions).toContain('transferFailText(e)')
    expect(drawer).toContain('data-test="refused-owner-error"')
  })

  it('posts the transfer through the shared util (one copy of the wording)', () => {
    for (const src of [sessions, drawer]) {
      expect(src).toContain("from '../utils/sessionOwner'")
      expect(src).toContain('transferSessionOwner(')
      expect(src).toContain('refusedOwnerNotice(')
      expect(src).toContain('transferOwnerLabel(')
    }
    // Sessions 列表把转移结果并回列表（徽标随之消失）。
    expect(sessions).toContain('it.session_id === updated.session_id ? updated : it')
  })

  it('draws the same banner in the session detail', () => {
    expect(drawer).toContain('v-if="session?.refused_caller_id"')
    expect(drawer).toContain('data-test="refused-owner-banner"')
    expect(drawer).toContain('@click="transferSessionOwnerToRefused"')
    expect(drawer).toContain("emit('changed')")
  })
})
