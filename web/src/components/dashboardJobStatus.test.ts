// gofer-hez1：统计页「任务状态分布」的小格曾显示原始英文状态名并被截断（runni… / recov…），
// 改成与全站一致的状态名（status.<值>），原始值放 title。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import DashboardJobStatus from './DashboardJobStatus.vue'

const source = Object.values(
  import.meta.glob('./DashboardJobStatus.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

const render = (): Promise<string> => renderToString(createSSRApp({ render: () => h(DashboardJobStatus) }))

describe('DashboardJobStatus labels', () => {
  it('shows localized status names instead of the raw values', async () => {
    const html = await render()
    for (const label of ['运行中', '恢复中', '待应答', '待验收', '待批准', '排队中', '已完成', '失败', '已取消', '超时', '已拒绝']) {
      expect(html, label).toContain(label)
    }
    // 原始状态名只在 title 里，不在可见文本里
    expect(html).not.toMatch(/>\s*(running|recovering|pending_interaction|needs_review|awaiting_approval|queued|done|failed|cancelled|timeout|rejected)\s*</)
    expect(html).toContain('title="running · 运行中"')
  })

  it('leaves room for the longest label instead of ellipsizing it', () => {
    // .l 不再 ellipsis + nowrap（那会把「待验收」截成「待验…」），格子最小宽放得下完整标签
    expect(source).not.toMatch(/\.l \{[^}]*text-overflow: ellipsis/)
    expect(source).toMatch(/\.l \{[^}]*white-space: normal/)
    expect(source).toMatch(/minmax\(84px, 1fr\)/)
  })
})
