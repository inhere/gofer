// 消息正文不许把气泡撑宽（issue gofer-ndub）：代码块 / 宽表格 / 长 URL 只许在自己的框里
// 横滚或断行。这条规则以前散在各组件里（一份 word-break 一份 overflow-wrap，表格基本没人
// 管），现在只有一个入口——全局的 `.msg-body`（styles/msg.css）。这个测试守住「只有一个入口」
// 与「每个消息渲染点都挂上了它」两件事，避免下次新增气泡又漏掉。
import { describe, expect, it } from 'vitest'

const RAW = import.meta.glob('/src/**/*.{vue,ts,css}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

function src(path: string): string {
  const text = RAW[`/src/${path}`]
  if (!text) throw new Error(`missing source: ${path}`)
  return text
}

function cssBlock(text: string, selector: string): string {
  const at = text.indexOf(`${selector} {`)
  if (at < 0) throw new Error(`missing rule: ${selector}`)
  return text.slice(at, text.indexOf('}', at))
}

describe('shared message-body style', () => {
  const msg = src('styles/msg.css')

  it('is one global stylesheet imported by the app entry', () => {
    expect(src('main.ts')).toContain("import './styles/msg.css'")
    // 只在这里定义 .msg-body，组件里不再各写一份同名工具类
    const definitions = Object.entries(RAW).filter(
      ([path, text]) => path.endsWith('.css') && /^\.msg-body\s*\{/m.test(text),
    )
    expect(definitions.map(([path]) => path)).toEqual(['/src/styles/msg.css'])
  })

  it('keeps bubbles from being widened and lets long tokens break', () => {
    const body = cssBlock(msg, '.msg-body')
    expect(body).toContain('min-width: 0')
    expect(body).toContain('overflow-wrap: anywhere')
    expect(body).toContain('max-width: 100%')
  })

  it('scrolls code blocks and tables inside the bubble instead of growing it', () => {
    const pre = cssBlock(msg, '.msg-body pre')
    expect(pre).toContain('overflow-x: auto')
    expect(pre).toContain('max-width: 100%')
    // table 根盒默认不接受 overflow，必须先变成块盒才能横滚
    const table = cssBlock(msg, '.msg-body table')
    expect(table).toContain('display: block')
    expect(table).toContain('overflow-x: auto')
    expect(table).toContain('max-width: 100%')
  })
})

describe('every markdown / message renderer opts into .msg-body', () => {
  it('MarkdownBlock (the shared markdown path) carries it', () => {
    expect(src('components/MarkdownBlock.vue')).toContain('<div class="md msg-body" v-html="html"></div>')
  })

  const sites: [string, string][] = [
    ['ConversationView user bubble', 'components/workbench/ConversationView.vue:class="bubble bubble--user msg-body"'],
    ['ConversationView assistant text', 'components/workbench/ConversationView.vue:class="txt msg-body"'],
    ['SessionDrawer agent reply', 'components/SessionDrawer.vue:class="bubble bubble--agent session-reply msg-body"'],
    ['SessionDrawer relay message', 'components/SessionDrawer.vue:class="bubble bubble--human relay-message msg-body"'],
    ['SessionDrawer markdown body', 'components/SessionDrawer.vue:class="bubble-md msg-body"'],
    ['SessionDrawer plain body', 'components/SessionDrawer.vue:class="bubble-text msg-body"'],
    ['SessionDrawer last message', 'components/SessionDrawer.vue:class="last-message-text bubble-md msg-body"'],
    ['SessionDrawer progress body', 'components/SessionDrawer.vue:class="session-progress-full bubble-md msg-body"'],
    ['Mail letter bubble', 'views/Mail.vue:class="bubble msg-body"'],
    ['Steward panel bubble', 'components/StewardPanel.vue:class="bubble msg-body"'],
    ['Interaction card', 'components/InteractionCard.vue:class="icard msg-body"'],
    ['Comment thread body', 'components/CommentThread.vue:class="cm-body msg-body"'],
    ['Ndjson event body', 'components/NdjsonTimeline.vue:class="event-body msg-body"'],
    ['Job message text', 'views/JobDetail.vue:class="job-message-text msg-body"'],
    ['Job command markdown', 'views/JobDetail.vue:outcome-md msg-body"'],
    ['Log tape markdown view', 'components/LogTape.vue:class="log-md msg-body"'],
  ]

  for (const [label, spec] of sites) {
    const [path, snippet] = spec.split(':') as [string, string]
    it(label, () => {
      expect(src(path)).toContain(snippet)
    })
  }

  it('covers every v-html sink in the app: the injected element itself is a .msg-body', () => {
    for (const [path, text] of Object.entries(RAW)) {
      if (!path.endsWith('.vue')) continue
      for (const hit of text.matchAll(/v-html=/g)) {
        const at = hit.index ?? 0
        const open = text.lastIndexOf('<', at)
        const close = text.indexOf('>', at)
        const tag = text.slice(open, close)
        expect(tag, `${path} v-html sink without msg-body`).toContain('msg-body')
      }
    }
  })
})
