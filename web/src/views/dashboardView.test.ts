import { describe, expect, it } from 'vitest'

const source = Object.values(
  import.meta.glob('./Dashboard.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string
const jobStatus = Object.values(
  import.meta.glob('../components/DashboardJobStatus.vue', { eager: true, query: '?raw', import: 'default' }),
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
      'data-test="activity"',
      'data-test="heatmap"',
      '<DashboardJobStatus',
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

  it('defaults to 7d, offers today first and buckets today by hour only', () => {
    expect(source).toContain('ref<OverviewRange>(DEFAULT_RANGE)')
    expect(source).toContain('v-for="r in RANGES"')
    expect(source).toContain("range.value === 'today' ? ['hour'] : ['day', 'week', 'month']")
    expect(source).toContain('hourRows(ov.value?.hourly')
    // 今日的「最高产」不显示按天的数字：换成最佳时段
    expect(source).toContain('最佳时段')
  })

  it('puts the compact job-status distribution inside the activity card', () => {
    const card = source.indexOf('data-test="activity"')
    const status = source.indexOf('<DashboardJobStatus')
    const agents = source.indexOf('data-test="agents"')
    expect(card).toBeGreaterThan(-1)
    expect(status).toBeGreaterThan(card)
    expect(status).toBeLessThan(agents)
    // 全部 job 的当前状态，来自 /v1/stats，不随区间；注明以免和区间 Jobs 卡混淆
    expect(jobStatus).toContain('getStats()')
    expect(jobStatus).toContain('全部 job · 不随区间')
    expect(jobStatus).toContain('data-test="job-status"')
    expect(jobStatus).not.toContain('createLiveTopic')
    // 手机宽度堆叠
    expect(source).toMatch(/@media \(max-width: 640px\)[\s\S]*\.act-body \{\s*flex-direction: column/)
  })

  it('drops the Agent usage card and the old status card from the system fold', () => {
    expect(system).not.toMatch(/<h3>\s*Agent 用量/)
    expect(system).not.toContain('USAGE_WINDOWS')
    expect(system).not.toContain('usageWindow')
    expect(system).not.toContain('session-usage-card')
    expect(system).not.toContain('Jobs 状态分布 · total')
    expect(system).not.toContain('jobStatuses')
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
