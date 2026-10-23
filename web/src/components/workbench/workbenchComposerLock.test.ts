import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./WorkbenchComposer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

// gofer-jkd2：工作台的新会话表单除了 cwd 还要能声明锁定目录（持续会话整个会话期间持有锁）。
describe('workbench composer lock field (gofer-jkd2)', () => {
  it('adds a lock input next to the working directory', () => {
    expect(source).toContain('const lockPaths = ref(\'\')')
    expect(source).toContain(":aria-label=\"t('workbench.new.lock')\"")
    expect(source).toContain(":placeholder=\"t('workbench.new.lockPlaceholder')\"")
    expect(source).toContain(':title="DIR_LOCK_HINT"')
  })

  it('warns that a continuous session holds the lock for the whole session', () => {
    // 文案在 i18n/pages/workbench.ts（‘workbench.new.sessionHint’），页面只引用 key。
    expect(source).toContain("t('workbench.new.sessionHint')")
  })

  it('sends lock_paths only when the user declared one', () => {
    expect(source).toContain('const lockList = parseLockPaths(lockPaths.value)')
    expect(source).toContain('req.lock_paths = lockList')
  })
})
