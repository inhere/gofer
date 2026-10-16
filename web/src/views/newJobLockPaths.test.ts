import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./NewJob.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

// gofer-jkd2：新建 job 表单要有运行目录之外的「锁定目录 / 等锁时长 / worktree」入口，字段与
// CLI 的 --lock / --lock-wait / --worktree 对应，rebuild（快速重建）也要带回这些字段。
describe('new job directory lock and worktree fields (gofer-jkd2)', () => {
  it('offers the three inputs and explains the lock rule', () => {
    for (const id of ['id="nj-lock"', 'id="nj-lock-wait"', 'v-model="worktree" type="checkbox"']) {
      expect(source).toContain(id)
    }
    expect(source).toContain('锁定目录（可选，可多个）')
    expect(source).toContain('等锁时长（秒，可选）')
    expect(source).toContain('{{ DIR_LOCK_HINT }}')
  })

  it('submits lock_paths / lock_wait_sec / worktree, and omits them when untouched', () => {
    expect(source).toContain('const lockList = parseLockPaths(lockPaths.value)')
    expect(source).toContain('req.lock_paths = lockList')
    expect(source).toContain('req.lock_wait_sec = lockWaitSec.value')
    expect(source).toContain('req.worktree = true')
  })

  it('prefills the fields from the source job and sends only what changed on rebuild', () => {
    expect(source).toContain('lockPaths.value = formatLockPaths(r.lock_paths)')
    expect(source).toContain('if (r.lock_wait_sec != null) lockWaitSec.value = r.lock_wait_sec')
    expect(source).toContain('lock_paths: lockPaths.value, lock_wait_sec: lockWaitSec.value, worktree: worktree.value,')
    expect(source).toContain("chg('lock_paths', lockPaths.value,")
    expect(source).toContain("chg('worktree', worktree.value,")
  })

  it('accepts the Sessions page prefill through the URL (完整配置)', () => {
    expect(source).toContain("route.query.lock")
  })

  it('hints the project sub-repos as lock candidates (E32 listRepos)', () => {
    expect(source).toContain('listRepos')
    expect(source).toContain('data-test="lock-candidates"')
    expect(source).toContain('@click="addLockPath(candidate)"')
  })
})
