import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./WorkbenchComposer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

// gofer-jkd2：工作台的新会话表单除了 cwd 还要能声明锁定目录（持续会话整个会话期间持有锁）。
describe('workbench composer lock field (gofer-jkd2)', () => {
  it('adds a lock input next to the working directory', () => {
    expect(source).toContain('const lockPaths = ref(\'\')')
    expect(source).toContain('aria-label="锁定目录"')
    expect(source).toContain('placeholder="锁定目录（留空 = 锁整个 cwd）"')
    expect(source).toContain(':title="DIR_LOCK_HINT"')
  })

  it('warns that a continuous session holds the lock for the whole session', () => {
    expect(source).toContain('持续会话整个会话期间持有目录锁并占 agent 名额')
  })

  it('sends lock_paths only when the user declared one', () => {
    expect(source).toContain('const lockList = parseLockPaths(lockPaths.value)')
    expect(source).toContain('req.lock_paths = lockList')
  })
})
