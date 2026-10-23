import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Agents.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('Agents layout', () => {
  it('uses a compact three-row card on phones and hides the desktop header', () => {
    // 设计稿 §4.3：手机断点是 ≤760（不再是旧的 900）
    expect(source).toContain('@media (max-width: 760px)')
    expect(source).toContain("grid-template-areas:\n      'key detect'\n      'type type'\n      'health info'")
    expect(source).toContain('.thead {\n    display: none;')
  })

  it('reaches the agent config from the list (design §7.4: view and edit were split)', () => {
    expect(source).toContain('to="/settings/config"')
    expect(source).toContain("t('agents.edit')")
  })

  it('renders detect and health through the status family primitives', () => {
    expect(source).toContain('<StatusGlyph')
    expect(source).toContain('<StatusPill')
    expect(source).toContain('healthFamily')
    expect(source).not.toContain('health-badge')
  })
})
