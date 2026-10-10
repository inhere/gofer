import { describe, expect, it } from 'vitest'
import { diffFiles, outOfScope, scopeMatch } from './scope'

// 与 Go 侧 internal/job/scope_check_test.go 同一组用例。
describe('scopeMatch', () => {
  const cases: Array<[string, string, boolean]> = [
    ['internal/job/**', 'internal/job/submit.go', true],
    ['internal/job/**', 'internal/job/workflow/engine.go', true],
    ['internal/job/**', 'internal/jobstore/todos.go', false],
    ['internal/job/*.go', 'internal/job/submit.go', true],
    ['internal/job/*.go', 'internal/job/workflow/engine.go', false],
    ['web/src/components/ReviewPanel.vue', 'web/src/components/ReviewPanel.vue', true],
    ['internal/job', 'internal/job/submit.go', true],
    ['internal/job/', 'internal/job/submit.go', true],
    ['./docs/*.md', 'docs/a.md', true],
    ['**/*.md', 'README.md', true],
    ['**/*.md', 'docs/design/x.md', true],
    ['docs/**/x.md', 'docs/x.md', true],
    ['src/?.ts', 'src/a.ts', true],
    ['src/?.ts', 'src/ab.ts', false],
    ['a.b', 'aXb', false],
    ['', 'anything', false],
  ]
  for (const [glob, file, want] of cases) {
    it(`${glob} ~ ${file} = ${want}`, () => {
      expect(scopeMatch(glob, file)).toBe(want)
    })
  }
})

describe('diffFiles / outOfScope', () => {
  const diff =
    '=== committed (abc..HEAD) ===\n' +
    'diff --git a/internal/job/submit.go b/internal/job/submit.go\n' +
    'index 1..2 100644\n--- a/internal/job/submit.go\n+++ b/internal/job/submit.go\n@@ -1 +1 @@\n-a\n+b\n' +
    'diff --git a/old name.go b/new name.go\nsimilarity index 90%\nrename from old name.go\nrename to new name.go\n' +
    '=== uncommitted ===\n' +
    'diff --git a/internal/job/submit.go b/internal/job/submit.go\n' +
    'diff --git a/README.md b/README.md\ndeleted file mode 100644\n'
  it('lists each file once, rename = new path', () => {
    expect(diffFiles(diff)).toEqual(['internal/job/submit.go', 'new name.go', 'README.md'])
  })
  it('reads the trailing changed-files list of a truncated diff', () => {
    const t = 'diff --git a/a.go b/a.go\n+x\n\n=== changed files ===\na.go\nb/c.go\n'
    expect(diffFiles(t)).toEqual(['a.go', 'b/c.go'])
  })
  it('marks files outside the declared scope', () => {
    const files = diffFiles(diff)
    expect(outOfScope(files, undefined)).toEqual([])
    expect(outOfScope(files, [' internal/job/** ', '*.md'])).toEqual(['new name.go'])
  })
})
