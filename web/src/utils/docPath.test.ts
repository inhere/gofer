import { describe, expect, it } from 'vitest'
import { docReaderLocation, findDocPaths, isDocPath, parseDocRef, resolveDocHref } from './docPath'

describe('docPath', () => {
  it('parses paths and optional line or anchor suffixes', () => {
    expect(parseDocRef('docs/design/a.md:42')).toEqual({ path: 'docs/design/a.md', line: 42 })
    expect(parseDocRef('AGENTS.md#配置')).toEqual({ path: 'AGENTS.md', anchor: '配置' })
  })

  it('finds markdown paths without consuming adjacent punctuation', () => {
    expect(findDocPaths('见 docs/a.md。以及 `docs/b.markdown:3`')).toEqual([
      { start: 2, end: 11, path: 'docs/a.md' },
      { start: 16, end: 33, path: 'docs/b.markdown', line: 3 },
    ])
  })

  it('rejects unsafe or non-markdown paths', () => {
    for (const value of ['/a.md', '~/a.md', 'C:/a.md', 'a\\b.md', '../a.md', '.env.md', 'docs/.x/a.md', 'secret:x.md', 'a.txt', 'my doc.md', 'https://x/a.md']) {
      expect(isDocPath(value), value).toBe(false)
    }
  })

  it('resolves relative links without leaving the project root', () => {
    expect(resolveDocHref('docs/design', '../plans/a.md')).toBe('docs/plans/a.md')
    expect(resolveDocHref('docs/design', '../../../a.md')).toBeNull()
    expect(resolveDocHref('docs/design', '#heading')).toBeNull()
    expect(resolveDocHref('docs/design', 'https://example.test/a.md')).toBeNull()
  })

  it('builds a shareable reader location', () => {
    expect(docReaderLocation({ projectKey: 'demo', base: 'repo', worktreeBranch: 'feature/x' }, { path: 'docs/a.md', line: 3 })).toEqual({
      path: '/projects/demo/doc',
      query: { path: 'docs/a.md', base: 'repo', line: '3', wtb: 'feature/x' },
    })
  })
})
