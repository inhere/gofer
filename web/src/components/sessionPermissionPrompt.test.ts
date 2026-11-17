// 会话抽屉的两张卡改成统一卡片（R11）：只做适配，渲染交给 InteractionCard。
// SSR 渲染看结构（没有 test-utils 与真 DOM 事件）。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { Decision } from '../api/types'
import SessionPermissionPrompt from './SessionPermissionPrompt.vue'

const perm: Decision = {
  id: 'd1',
  title: 't',
  question: '需要授权：Bash `npm test`',
  state: 'OPEN',
  timeout_sec: 600,
  asked_at: 1_700_000_000,
  session_id: 's1',
  kind: 'permission',
  permission: { tool_name: 'Bash', summary: 'Bash `npm test`', input: '{"command":"npm test"}' },
}

const asked: Decision = {
  id: 'd2',
  title: 't',
  question: '等你回答：颜色？',
  state: 'OPEN',
  timeout_sec: 600,
  asked_at: 1_700_000_000,
  session_id: 's1',
  kind: 'permission',
  permission: {
    tool_name: 'AskUserQuestion',
    summary: '颜色？',
    questions: [
      { question: '颜色？', options: [{ label: '红', description: '热情' }, { label: '蓝' }] },
      { question: '语言？', options: [{ label: 'Go' }, { label: 'TS' }], multiSelect: true },
    ],
  },
}

async function render(decision: Decision): Promise<string> {
  return renderToString(
    createSSRApp({
      render: () => h(SessionPermissionPrompt, { sid: 's1', decision, nowSec: 1_700_000_100 }),
    }),
  )
}

describe('SessionPermissionPrompt adapter', () => {
  it('renders the permission request through the unified card', async () => {
    const html = await render(perm)
    expect(html).toContain('data-test="session-permission"')
    expect(html).toContain('class="icard msg-body')
    expect(html).toContain('需要授权')
    expect(html).toContain('允许')
    expect(html).toContain('拒绝')
    // 「其他」= 附原因拒绝的输入框（提示语说明填了会怎样）
    expect(html).toContain('data-test="icard-other"')
    expect(html).toContain('拒绝原因（会告诉 agent）')
    // 工具参数与策略提示由卡片自己显示
    expect(html).toContain('npm test')
    // 老的按钮渲染已删除
    expect(html).not.toContain('perm-btn')
  })

  it('renders one card per question with the multi-select and description', async () => {
    const html = await render(asked)
    expect(html).toContain('data-test="session-question"')
    expect(html.match(/class="icard msg-body/g)).toHaveLength(2)
    expect(html).toContain('热情')
    // 第二题是多选：它的两个选项都是复选框（第一题是单选，没有复选框）
    expect(html.match(/type="checkbox"/g)).toHaveLength(2)
    expect(html).toContain('等你回答')
  })

  it('says who handled a prompt nobody answered on the web', async () => {
    const html = await render({ ...perm, state: 'EXPIRED', released_by: 'terminal' })
    expect(html).toContain('data-test="permission-outcome"')
    expect(html).toContain('已在终端处理')
    expect(html).toContain('已过期')
  })

  it('shows the final answers of an answered question card', async () => {
    const html = await render({ ...asked, state: 'ANSWERED', answer: 'answers:{"颜色？":"蓝","语言？":"Go, TS"}' })
    expect(html).toContain('蓝')
    expect(html).toContain('Go, TS')
    expect(html).not.toContain('data-test="icard-choice-submit"')
  })
})
