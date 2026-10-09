import { describe, expect, it } from 'vitest'

const source = Object.values(
  import.meta.glob('./Dashboard.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string
const system = Object.values(
  import.meta.glob('../components/DashboardSystem.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

describe('Dashboard statistics wall structure', () => {
  it('reads one aggregate endpoint and refreshes every 60s while visible', () => {
    expect(source).toContain('getStatsOverview(')
    expect(source).toContain('createPoller(load, REFRESH_MS)')
    expect(source).toContain('REFRESH_MS = 60_000')
    // 不订阅 2s 一推的 stats 主题；那只给「系统」折叠区用
    expect(source).not.toContain('createLiveTopic')
  })

  it('renders every section in design order', () => {
    const order = [
      'data-test="summary"',
      'data-test="kpi-jobs"',
      'data-test="kpi-time"',
      'data-test="kpi-git"',
      'data-test="kpi-signal"',
      'data-test="done-chart"',
      'data-test="best"',
      'data-test="heatmap"',
      'data-test="agents"',
      'data-test="projects"',
      'data-test="review"',
      'data-test="workload"',
      'data-test="usage"',
      'data-test="system"',
    ]
    let at = -1
    for (const marker of order) {
      const i = source.indexOf(marker)
      expect(i, marker).toBeGreaterThan(at)
      at = i
    }
  })

  it('has range switch, bucket switch and copy action', () => {
    expect(source).toContain('aria-label="时间范围"')
    expect(source).toContain('aria-label="分桶"')
    expect(source).toContain('复制统计')
    expect(source).toContain('navigator.clipboard.writeText(copyText(')
  })

  it('draws charts with inline SVG, no chart library', () => {
    expect(source).toContain('<svg')
    expect(source).not.toMatch(/from ['"](chart\.js|echarts|d3|apexcharts|recharts)/)
  })

  it('mounts the system cards only when the fold is open', () => {
    expect(source).toContain('<details class="sys panel"')
    expect(source).toContain('<DashboardSystem v-if="sysOpen" />')
    expect(system).toContain("createLiveTopic('stats'")
  })

  it('shows a dash instead of zero for metrics without a source', () => {
    expect(source).toContain('signal ? fmtInt(signal.human) : DASH')
    expect(source).toContain('fmtInt(ov.git.files)')
    expect(source).toContain('v-if="ov.usage"')
  })
})
