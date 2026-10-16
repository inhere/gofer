import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./JobDetail.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

// gofer-jkd2：续接（继续会话 / 续投）要能改锁定目录 —— resume 原样继承源 job 的 cwd 与
// lock_paths，会话锁着整个项目根时只能绕道新建 job；cwd 仍不可改（ACP session/load 需同一 cwd）。
describe('job detail resume lock field (gofer-jkd2)', () => {
  it('adds a lock input to the resume form', () => {
    expect(source).toContain('const resumeLockPaths = ref(\'\')')
    expect(source).toContain('aria-label="锁定目录"')
    expect(source).toContain('placeholder="锁定目录（可选；留空 = 继承源 job 的锁）"')
  })

  it('carries the lock into the resume request', () => {
    expect(source).toContain('lockPaths: parseLockPaths(resumeLockPaths.value),')
  })
})
