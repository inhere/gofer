// 统计墙（gofer-yelm）的纯函数：分桶、热力图网格、柱图几何、格式化与「复制统计」文本。
// 服务端给按日序列（浏览器时区；今日另给按小时序列），周 / 月分桶都在这里算，切分桶不再请求。
import type { Overview, OverviewDay, OverviewHour, OverviewRange } from '../api/overview'

export type Bucket = 'hour' | 'day' | 'week' | 'month'

// RANGES 是区间切换按钮的顺序；DEFAULT_RANGE 是进入页面时的区间（与服务端空 range 默认一致）。
export const RANGES: OverviewRange[] = ['today', '7d', '30d', 'all']
export const DEFAULT_RANGE: OverviewRange = '7d'

export const RANGE_LABEL: Record<OverviewRange, string> = { today: '今日', '7d': '近 7 天', '30d': '近 30 天', all: '全部' }
export const WEEKDAY = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']

// allowedBuckets：今日只能按小时；7d 只能按日；30d 日 / 周；全部 日 / 周 / 月（design §页面结构）。
export function allowedBuckets(range: OverviewRange): Bucket[] {
  if (range === 'today') return ['hour']
  if (range === '7d') return ['day']
  if (range === '30d') return ['day', 'week']
  return ['day', 'week', 'month']
}

export function defaultBucket(range: OverviewRange): Bucket {
  if (range === 'today') return 'hour'
  return range === 'all' ? 'week' : 'day'
}

// 日期串（YYYY-MM-DD）一律按 UTC 解析与推算：它已经是浏览器时区的「本地日」，再套时区会错一天。
function parseDay(day: string): Date {
  return new Date(`${day}T00:00:00Z`)
}

function fmtDay(d: Date): string {
  return d.toISOString().slice(0, 10)
}

export function addDays(day: string, n: number): string {
  const d = parseDay(day)
  d.setUTCDate(d.getUTCDate() + n)
  return fmtDay(d)
}

// mondayOf 是该日所在周的周一。
export function mondayOf(day: string): string {
  const wd = parseDay(day).getUTCDay()
  return addDays(day, -((wd + 6) % 7))
}

export function weekdayOf(day: string): number {
  return parseDay(day).getUTCDay()
}

function md(day: string): string {
  const [, m, d] = day.split('-')
  return `${Number(m)}/${Number(d)}`
}

function daysInMonth(month: string): number {
  const [y, m] = month.split('-').map(Number)
  return new Date(Date.UTC(y, m, 0)).getUTCDate()
}

export interface BucketRow {
  key: string
  label: string
  full: string // 悬停提示的标题
  done: number
  failed: number
  days: number
  partial: boolean // 首尾不满的桶（悬停提示里注明「不完整」）
}

// bucketize 把按日序列合成日 / 周（周一起）/ 月桶；输入按日期升序。
export function bucketize(daily: OverviewDay[], bucket: Bucket): BucketRow[] {
  const out: BucketRow[] = []
  const idx = new Map<string, BucketRow>()
  for (const d of daily) {
    let key: string
    let label: string
    let full: string
    if (bucket === 'day') {
      key = d.day
      label = md(d.day)
      full = `${d.day} ${WEEKDAY[weekdayOf(d.day)]}`
    } else if (bucket === 'week') {
      key = mondayOf(d.day)
      label = md(key)
      full = `${key} 起一周`
    } else {
      key = d.day.slice(0, 7)
      label = `${Number(key.slice(5))}月`
      full = key
    }
    let row = idx.get(key)
    if (!row) {
      row = { key, label, full, done: 0, failed: 0, days: 0, partial: false }
      idx.set(key, row)
      out.push(row)
    }
    row.done += d.done
    row.failed += d.failed
    row.days++
  }
  if (bucket !== 'day') {
    for (const r of out) {
      const want = bucket === 'week' ? 7 : daysInMonth(r.key)
      r.partial = r.days < want
    }
  }
  return out
}

function hh(h: number): string {
  return `${String(h).padStart(2, '0')}:00`
}

// hourRows 把今日的按小时序列转成柱图桶；nowHour（浏览器本地当前小时）那一桶标「不完整」。
export function hourRows(hourly: OverviewHour[], nowHour = -1): BucketRow[] {
  return hourly.map((h) => ({
    key: `h${h.hour}`,
    label: hh(h.hour),
    full: `今日 ${hh(h.hour)}–${hh(h.hour + 1)}`,
    done: h.done,
    failed: h.failed,
    days: 1,
    partial: h.hour === nowHour,
  }))
}

export interface BarRect {
  x: number
  w: number
  doneY: number
  doneH: number
  failY: number
  failH: number
  tip: string
}

// barLayout 是堆叠柱几何：完成（绿）在下、失败（红）叠在上方，中间留 2px 间隙；
// 纵轴从 0 起，max 为最高桶的总数。
export function barLayout(rows: BucketRow[], width: number, height: number): { bars: BarRect[]; max: number } {
  const max = Math.max(1, ...rows.map((r) => r.done + r.failed))
  const n = Math.max(rows.length, 1)
  const gap = n > 60 ? 1 : 2
  const w = Math.max(1, (width - gap * (n - 1)) / n)
  const top = 14
  const scale = (v: number): number => (v / max) * (height - top)
  const bars = rows.map((r, i) => {
    const doneH = scale(r.done)
    const failH = scale(r.failed)
    const doneY = height - doneH
    const failY = doneY - failH - (doneH > 0 && failH > 0 ? 2 : 0)
    const tip = `${r.full}${r.partial ? '（不完整）' : ''}\n完成 ${r.done} · 失败 ${r.failed}`
    return { x: i * (w + gap), w, doneY, doneH, failY, failH, tip }
  })
  return { bars, max }
}

// heatLevel：0 = 无产出；1–4 按服务端给的三个阈值（有产出日的 25/50/80 分位）切。
export function heatLevel(v: number, levels: number[]): number {
  if (!v) return 0
  if (levels.length < 3) return 4
  if (v <= levels[0]) return 1
  if (v <= levels[1]) return 2
  if (v <= levels[2]) return 3
  return 4
}

export interface HeatCell {
  day: string
  done: number
  level: number
  future: boolean
}

// heatGrid：每列一周（周一在上），最后一列是 today 所在周；today 之后的格子标 future（不画）。
export function heatGrid(days: { day: string; done: number }[], weeks: number, levels: number[], today: string): HeatCell[][] {
  const byDay = new Map(days.map((d) => [d.day, d.done]))
  const start = addDays(mondayOf(today), -7 * (weeks - 1))
  const cols: HeatCell[][] = []
  for (let w = 0; w < weeks; w++) {
    const col: HeatCell[] = []
    for (let r = 0; r < 7; r++) {
      const day = addDays(start, w * 7 + r)
      const done = byDay.get(day) ?? 0
      col.push({ day, done, level: heatLevel(done, levels), future: day > today })
    }
    cols.push(col)
  }
  return cols
}

// localToday 是浏览器时区的今天（YYYY-MM-DD）。
export function localToday(now: Date = new Date()): string {
  const y = now.getFullYear()
  const m = String(now.getMonth() + 1).padStart(2, '0')
  const d = String(now.getDate()).padStart(2, '0')
  return `${y}-${m}-${d}`
}

export const DASH = '—'

// fmtDur：null → 「—」；≥1h 显示「Xh YYm」，≥1m 显示「Ym」，否则「Zs」。
export function fmtDur(sec: number | null | undefined): string {
  if (sec == null || !Number.isFinite(sec)) return DASH
  const s = Math.max(0, Math.round(sec))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (h > 0) return `${h}h ${String(m).padStart(2, '0')}m`
  if (m > 0) return `${m}m`
  return `${s}s`
}

export function fmtInt(v: number | null | undefined): string {
  if (v == null || !Number.isFinite(v)) return DASH
  return Math.round(v).toLocaleString('en-US')
}

// fmtK：大数缩写（12.3k / 1.2M）。
export function fmtK(v: number | null | undefined): string {
  if (v == null || !Number.isFinite(v)) return DASH
  if (v >= 1e6) return `${(v / 1e6).toFixed(v >= 1e7 ? 0 : 1)}M`
  if (v >= 1e3) return `${(v / 1e3).toFixed(v >= 1e4 ? 0 : 1)}k`
  return String(Math.round(v))
}

export function fmtPct(v: number | null | undefined): string {
  if (v == null || !Number.isFinite(v)) return DASH
  return `${Math.round(v * 100)}%`
}

export function fmtUSD(v: number | null | undefined, digits = 2): string {
  if (v == null || !Number.isFinite(v)) return DASH
  return `$${v.toFixed(digits)}`
}

// perJob：分子 ÷ 分母，分母为 0 或分子未知时是「—」。
export function perJob(num: number | null | undefined, den: number, digits = 1): string {
  if (num == null || !den) return DASH
  return (num / den).toFixed(digits)
}

export function topN<T>(rows: T[], key: (r: T) => number, n = 3): T[] {
  return [...rows].sort((a, b) => key(b) - key(a)).slice(0, n).filter((r) => key(r) > 0)
}

export function rangeLabel(ov: Pick<Overview, 'range'>): string {
  if (ov.range.key === 'all' && ov.range.first_job_at > 0) {
    const d = new Date(ov.range.first_job_at * 1000)
    return `全部（${d.getMonth() + 1}/${d.getDate()} 起）`
  }
  return RANGE_LABEL[ov.range.key]
}

// summaryLine 是标题行的一句总计。
export function summaryLine(ov: Overview): string {
  return `${rangeLabel(ov)} · ${fmtInt(ov.totals.jobs)} jobs · ${fmtInt(ov.totals.sessions)} 会话 · 运行 ${fmtDur(ov.totals.wall_sec)}`
}

// copyText 是「复制统计」的纯文本摘要（不含明细）。未知指标写「—」，不写 0。
export function copyText(ov: Overview): string {
  const g = ov.git
  const s = ov.signal
  return [
    `gofer 统计 · ${summaryLine(ov)}`,
    `Jobs ${fmtInt(ov.jobs.total + ov.jobs.in_progress)}（完成 ${ov.jobs.done} / 失败 ${ov.jobs.failed} / 进行中 ${ov.jobs.in_progress}）· 成功率 ${fmtPct(ov.jobs.success_rate)}`,
    `运行 ${fmtDur(ov.time.wall_sec)}（活跃 ${fmtDur(ov.time.active_sec)} · 等人 ${fmtDur(ov.time.human_wait_sec)}）`,
    `Git 提交 ${fmtInt(g.commits)} · 文件 ${fmtInt(g.files)} · +${fmtInt(g.insertions)} −${fmtInt(g.deletions)}`,
    `轮次 ${fmtInt(s?.turns)} · 工具调用 ${fmtInt(s?.tool_calls)} · 人介入 ${s ? fmtInt(s.human) : DASH}`,
    `验收通过率 ${fmtPct(ov.review.accept_rate)} · Plan 完成 ${ov.review.plans_done}`,
    `费用 ${fmtUSD(ov.usage?.cost_usd)}`,
  ].join('\n')
}

// NOTE_TEXT 把接口 notes 的口径代码翻成说明。
export const NOTE_TEXT: Record<string, string> = {
  session_usage_utc_day: '终端会话用量按 UTC 日切分，范围边界最多差一天',
  plans_done_by_updated_at: 'Plan 没有完成时间戳，按最后更新时间近似',
}
