import { describe, expect, it } from 'vitest'

// gofer-jkd2：新会话表单要有运行目录与锁定目录 —— 会话落在项目根会锁住
// 整个项目根，别的子目录 job 只能排队等锁。P4 起表单在 ui/NewSessionSheet。
const source = Object.values(import.meta.glob('../ui/NewSessionSheet.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('session creation form directory and lock fields (gofer-jkd2)', () => {
  it('offers a working directory and a lock input', () => {
    expect(source).toContain("const cwd = ref('.')")
    expect(source).toContain("const lockPaths = ref('')")
    expect(source).toContain("t('sessions.new.cwd')")
    expect(source).toContain("t('sessions.new.lock')")
  })

  it('sends cwd and lock_paths with the new session job', () => {
    expect(source).toContain("cwd: cwd.value.trim() || '.',")
    expect(source).toContain('const lockList = parseLockPaths(lockPaths.value)')
    expect(source).toContain('req.lock_paths = lockList')
  })

  it('carries both fields into the full-config link', () => {
    expect(source).toContain("q.set('cwd', cwd.value.trim())")
    expect(source).toContain("q.set('lock', lockPaths.value.trim())")
  })
})
