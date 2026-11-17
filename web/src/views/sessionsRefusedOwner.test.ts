import { describe, expect, it } from 'vitest'

const sessions = Object.values(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const drawer = Object.values(
  import.meta.glob('../components/SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

// gofer-eh53：切 token 后 hook 以另一个 caller 身份上报被拒，会话状态停更。列表与详情
// 都要「有值才显示」地把这件事说出来，并给一个把会话转给该 caller 的按钮。
describe('refused session owner', () => {
  it('renders the notice, badge and transfer button only when a refusal is recorded', () => {
    // 有被拒记录才渲染：徽标 / 提示行 / 按钮都以 refused_caller_id 为条件。R13 起会话行
    // 行的入口进详情抽屉，徽标与转移按钮随之到抽屉里。
    expect(sessions).toContain('data-test="history-detail"')
    expect(sessions).toContain('<SessionDrawer')
    expect(drawer).toContain('v-if="session?.refused_caller_id"')
    expect(drawer).toContain('refusedOwnerHelp(')
    expect(drawer).toContain('{{ refusedOwnerNotice(session) }}')
    expect(drawer).toContain('@click="transferSessionOwnerToRefused"')
    expect(drawer).toContain("t('sessions.drawer.transferring')")
    expect(drawer).toContain('transferOwnerLabel(session)')
  })

  it('shows the server reason when the transfer is refused', () => {
    expect(drawer).toContain('data-test="refused-owner-error"')
    expect(drawer).toContain("t('sessions.drawer.transferFailed', { err: ownerError })")
    expect(drawer).toContain('transferFailText(e)')
  })

  it('posts the transfer through the shared util (one copy of the wording)', () => {
    expect(drawer).toContain("from '../utils/sessionOwner'")
    expect(drawer).toContain('transferSessionOwner(')
    expect(drawer).toContain('refusedOwnerNotice(')
    expect(drawer).toContain('transferOwnerLabel(')
    // 列表不再自己写一份转移逻辑（旧内联实现已随三份卡片删除）。
    expect(sessions).not.toContain('transferSessionOwner(')
  })

  it('draws the same banner in the session detail', () => {
    expect(drawer).toContain('v-if="session?.refused_caller_id"')
    expect(drawer).toContain('data-test="refused-owner-banner"')
    expect(drawer).toContain('@click="transferSessionOwnerToRefused"')
    expect(drawer).toContain("emit('changed')")
  })
})
