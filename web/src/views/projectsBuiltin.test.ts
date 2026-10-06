import { describe, expect, it } from 'vitest'

const page = Object.values(import.meta.glob('./Projects.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('Projects page: built-in default project', () => {
  it('marks injected projects in the list and in the detail head', () => {
    expect(page).toContain('injectedKeys.includes(key)')
    expect(page).toContain('data-test="project-builtin-badge"')
    expect(page).toContain('data-test="project-builtin-detail"')
    expect(page).toContain('resp.injected')
  })

  it('warns that saving writes the built-in project into the config, and refuses to delete it', () => {
    expect(page).toContain('data-test="project-builtin-hint"')
    expect(page).toContain('保存后会写入配置，成为声明项目')
    expect(page).toContain('!!detail?.injected')
    expect(page).toContain('内置项目不能删除')
  })
})
