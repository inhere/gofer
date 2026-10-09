import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./SessionDrawer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('SessionDrawer relay switch hints', () => {
  it('says that typing in the terminal turns on back into auto', () => {
    expect(source).toContain('在终端输入一条后自动回到 auto')
  })

  it('explains an automatic on → auto fall-back only while the mode is still auto', () => {
    expect(source).toContain('session.value?.relay_demoted_at')
    expect(source).toContain("(session.value?.relay_mode || 'auto') !== 'auto'")
    expect(source).toContain('data-test="relay-demoted"')
  })
})
