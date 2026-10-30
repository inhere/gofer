// 反向链接面板（components/RefsPanel.vue）的文案。key 前缀 `refs.`。
import type { PageDict } from './types'

export const refs: PageDict = {
  zh: {
    'refs.title': '被引用（{n}）',
    'refs.kind.job': 'Job',
    'refs.kind.plan': '计划',
    'refs.kind.todo': '待办',
    'refs.kind.comment': '评论',
    'refs.kind.mail': '邮件',
    'refs.field.title': '标题',
    'refs.field.description': '描述',
    'refs.field.report': '报告',
    'refs.field.body': '正文',
    'refs.field.other': '字段 {name}',
    'refs.loadFailed': '被引用列表加载失败',
  },
  en: {
    'refs.title': 'Referenced by ({n})',
    'refs.kind.job': 'Job',
    'refs.kind.plan': 'Plan',
    'refs.kind.todo': 'Todo',
    'refs.kind.comment': 'Comment',
    'refs.kind.mail': 'Mail',
    'refs.field.title': 'title',
    'refs.field.description': 'description',
    'refs.field.report': 'report',
    'refs.field.body': 'body',
    'refs.field.other': 'field {name}',
    'refs.loadFailed': 'Failed to load references',
  },
}
