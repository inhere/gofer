// 统一对话时间线的文案（设计稿 §2，计划 R12）：工作台、job 详情「消息」、会话抽屉共用，
// 所以单独一份字典，不挂在某一个页面前缀下。key 前缀 `conversation.`。
import type { PageDict } from './types'

export const conversation: PageDict = {
  zh: {
    'conversation.tools': '{n} 个工具调用',
    'conversation.toolsFail': '{n} 失败',
    'conversation.injectedShow': 'gofer 规则 · 展开',
    'conversation.injectedHide': 'gofer 规则 · 收起',
    'conversation.unsent': '未发送',
    'conversation.note.session_restarted': '重启前的提问已失效，请重新发起',
    'conversation.note.turn_interrupted_worker_restart': '上一轮因 worker 重启中断',
    'conversation.resend': '重发',
    'conversation.truncated': '已截断',
    'conversation.earlierOmitted': '更早的消息已省略',
    'conversation.expand': '展开全文',
    'conversation.collapse': '收起',
    'conversation.permission.allowed': '已允许',
    'conversation.permission.denied': '已拒绝',
    'conversation.permission.auto': '自动允许',
  },
  en: {
    'conversation.tools': '{n} tool calls',
    'conversation.toolsFail': '{n} failed',
    'conversation.injectedShow': 'gofer rules · show',
    'conversation.injectedHide': 'gofer rules · hide',
    'conversation.unsent': 'Not sent',
    'conversation.note.session_restarted': 'The question asked before the restart is no longer valid; please ask again',
    'conversation.note.turn_interrupted_worker_restart': 'The last turn was interrupted because the worker restarted',
    'conversation.resend': 'Send again',
    'conversation.truncated': 'Truncated',
    'conversation.earlierOmitted': 'Earlier messages omitted',
    'conversation.expand': 'Show all',
    'conversation.collapse': 'Collapse',
    'conversation.permission.allowed': 'Allowed',
    'conversation.permission.denied': 'Denied',
    'conversation.permission.auto': 'Auto-allowed',
  },
}
