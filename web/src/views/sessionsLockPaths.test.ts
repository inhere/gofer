import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

// gofer-jkd2：工作台 / Sessions 的新会话表单要有运行目录与锁定目录 —— 会话落在项目根会锁住
// 整个项目根，别的子目录 job 只能排队等锁。
describe('session creation form directory and lock fields (gofer-jkd2)', () => {
  it('offers a working directory and a lock input', () => {
    expect(source).toContain('const sessionCwd = ref(\'.\')')
    expect(source).toContain('const sessionLockPaths = ref(\'\')')
    expect(source).toContain('工作目录（项目根相对）')
    expect(source).toContain('锁定目录（可选，可多个；会话期间一直持有锁，尽量锁子目录）')
  })

  it('sends cwd and lock_paths with the new session job', () => {
    expect(source).toContain('cwd: sessionCwd.value.trim() || \'.\',')
    expect(source).toContain('const lockList = parseLockPaths(sessionLockPaths.value)')
    expect(source).toContain('req.lock_paths = lockList')
  })

  it('carries both fields into the full-config link', () => {
    expect(source).toContain("q.set('cwd', sessionCwd.value.trim())")
    expect(source).toContain("q.set('lock', sessionLockPaths.value.trim())")
  })
})
