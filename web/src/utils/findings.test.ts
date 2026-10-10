import { describe, expect, it } from 'vitest'
import { findingIssueCommand, parseFindings } from './findings'

// 与 Go 侧 internal/job/findings_test.go 同一组用例。
describe('parseFindings', () => {
  const cases: Array<[string, string, string[]]> = [
    ['none', '# Report\n\nall done\n', []],
    ['basic', '## 结果\nok\n\n## 发现但不碰\n\n- a.go:12：空指针\n- docs/x.md：过时\n', ['a.go:12：空指针', 'docs/x.md：过时']],
    ['stops at same level', '## 发现但不碰\n- one\n## 下一节\n- not this\n', ['one']],
    ['keeps deeper headings', '## 发现但不碰\n- one\n### 细节\n- two\n# Top\n- no\n', ['one', 'two']],
    ['continuation lines', '### 发现但不碰\n- first line\n  second line\n* [ ] boxed\n1. numbered\n', ['first line second line', 'boxed', 'numbered']],
    ['out of scope alias', '## Out of scope\n- x\n', ['x']],
    ['out-of-scope findings alias', '#### Out-of-Scope Findings:\n+ y\n', ['y']],
    ['decorated title', '## 「发现但不碰」：\n- z\n', ['z']],
    ['last section wins', '## 发现但不碰\n- old\n## 发现但不碰\n- new\n', ['new']],
    ['fenced heading ignored', '```\n## 发现但不碰\n- quoted\n```\nplain\n', []],
    ['inline mention is not a heading', '写进汇报末尾的「## 发现但不碰」小节\n- not an item\n', []],
    ['empty section', '## 发现但不碰\n\nnothing\n', []],
  ]
  for (const [name, report, want] of cases) {
    it(name, () => {
      expect(parseFindings(report)).toEqual(want)
    })
  }
})

describe('findingIssueCommand', () => {
  it('quotes the text for a POSIX shell', () => {
    expect(findingIssueCommand('a.go: "x" costs $1 `now`', 'job-1')).toBe(
      'gofer issue create "a.go: \\"x\\" costs \\$1 \\`now\\`" -d "discovered in job job-1" --tag discovered',
    )
  })
})
