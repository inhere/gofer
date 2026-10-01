import type { WorkbenchStatus } from '../../api/types'

export function threadStatusPresentation(status: WorkbenchStatus): { label: string; tone: string } {
  switch (status) {
    case 'awaiting_input': return { label: '等待输入', tone: 'neutral' }
    case 'blocked': return { label: 'blocked', tone: 'attention' }
    case 'review': return { label: 'review', tone: 'attention' }
    case 'working': return { label: 'working', tone: 'active' }
    case 'done': return { label: 'done', tone: 'done' }
    case 'idle': return { label: 'idle', tone: 'neutral' }
  }
}
