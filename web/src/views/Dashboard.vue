<script setup lang="ts">
// 统计墙（gofer-yelm，design docs/design/2026-10-09-dashboard-redesign-design.md）：回答
// 「这段时间系统和 agent 干得怎么样」。一个接口 GET /v1/stats/overview 给整页数据，周 / 月
// 分桶与热力图在前端算（utils/dashStats）。没有数据来源的指标显示「—」，不显示 0。
// 实时系统卡片收进底部「系统」折叠区（DashboardSystem，展开才挂载、才订阅 stats 推送）；
// 「Jobs 状态分布」（全部 job 的当前状态，不随区间）放在活跃度卡右侧（DashboardJobStatus）。
// UI 重设计 P5：配色改用状态族 token（完成 = --done、失败 = --fail），数字统一等宽（§4.2、§7.3）。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { getStatsOverview, type Overview, type OverviewRange } from '../api/overview'
import DashboardJobStatus from '../components/DashboardJobStatus.vue'
import DashboardSystem from '../components/DashboardSystem.vue'
import {
  allowedBuckets,
  barLayout,
  bucketize,
  copyText,
  DASH,
  DEFAULT_RANGE,
  defaultBucket,
  fmtDur,
  fmtInt,
  fmtK,
  fmtPct,
  fmtUSD,
  heatGrid,
  hourRows,
  localToday,
  perJob,
  RANGES,
  summaryLine,
  topN,
  type Bucket,
} from '../utils/dashStats'
import { createPoller } from '../utils/poller'
import { t, te } from '../i18n'
import { locale } from '../store/locale'

// 每 60s 刷新（页面可见时）；服务端同样按 60s / 5min 缓存。
const REFRESH_MS = 60_000

const range = ref<OverviewRange>(DEFAULT_RANGE)
const bucket = ref<Bucket>(defaultBucket(DEFAULT_RANGE))
const ov = ref<Overview | null>(null)
const loading = ref(false)
const error = ref('')
const sysOpen = ref(false)
const toast = ref('')

async function load(): Promise<void> {
  loading.value = true
  try {
    const want = range.value
    const data = await getStatsOverview(want)
    if (want === range.value) {
      ov.value = data
      error.value = ''
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

const poller = createPoller(load, REFRESH_MS)

function setRange(r: OverviewRange): void {
  if (r === range.value) return
  range.value = r
  bucket.value = defaultBucket(r)
  void load()
}

watch(range, (r) => {
  if (!allowedBuckets(r).includes(bucket.value)) bucket.value = defaultBucket(r)
})

const buckets = computed(() => allowedBuckets(range.value))
// 分桶按钮：今日只有「小时」；其余区间列日 / 周 / 月（不可用的置灰）。
const bucketChoices = computed<Bucket[]>(() => (range.value === 'today' ? ['hour'] : ['day', 'week', 'month']))
// 今日按小时（服务端 hourly，24 桶）；其余区间由按日序列合桶。
const isToday = computed(() => ov.value?.range.key === 'today')
const rows = computed(() =>
  bucket.value === 'hour'
    ? hourRows(ov.value?.hourly ?? [], new Date().getHours())
    : bucketize(ov.value?.daily ?? [], bucket.value),
)
const CHART_W = 600
const CHART_H = 160
const chart = computed(() => barLayout(rows.value, CHART_W, CHART_H))

const today = computed(() => localToday())
const heat = computed(() => {
  const h = ov.value?.heatmap
  return h ? heatGrid(h.days, h.weeks, h.levels, today.value) : []
})
const heatMonths = computed(() =>
  heat.value.map((col, i) => {
    const first = col[0]
    const d = Number(first.day.slice(8))
    return i === 0 || d <= 7 ? `${Number(first.day.slice(5, 7))}月` : ''
  }),
)
// 热力图的星期标尺：一 / 三 / 五 / 日（按语言取短名）。
const heatDayLabels = computed(() => [1, 2, 3, 4, 5, 6, 0].map((dow, i) => (i % 2 === 0 ? t(`stats.weekdayShort.${dow}`) : '')))

const jobsBig = computed(() => (ov.value ? ov.value.jobs.total + ov.value.jobs.in_progress : 0))
const signal = computed(() => ov.value?.signal ?? null)
const signalPartial = computed(() => signal.value != null && signal.value.coverage < 0.9)

const agentMax = computed(() => Math.max(1, ...(ov.value?.agents ?? []).map((a) => a.jobs)))
const projectMax = computed(() => Math.max(1, ...(ov.value?.projects ?? []).map((p) => p.jobs)))
const topJobs = computed(() => topN(ov.value?.projects ?? [], (p) => p.jobs))
const topWall = computed(() => topN(ov.value?.projects ?? [], (p) => p.wall_sec))
const topCommits = computed(() => topN(ov.value?.projects ?? [], (p) => p.commits))

// 口径说明：服务端给的说明 key 走字典，没登记的照原样显示（P6 收尾）。
const notes = computed(() => (ov.value?.notes ?? []).map((n) => (te(`stats.note.${n}`) ? t(`stats.note.${n}`) : n)))
// 说明的拼接符跟随语言：中文用全角分号，英文用「; 」。
const noteSeparator = computed(() => (locale.value === 'zh-CN' ? '；' : '; '))

function projectTail(p: string): string {
  return p.split('/').filter(Boolean).pop() || p || DASH
}

function rangeText(r: OverviewRange): string {
  return t(`stats.range.${r}`)
}

function bucketText(b: Bucket): string {
  return t(`stats.bucket.${b}`)
}

function weekdayText(dow: number): string {
  return t(`stats.weekday.${dow}`)
}

function modelName(m: { model: string; agent?: string }): string {
  if (m.model) return m.model
  return m.agent ? t('stats.model.default', { agent: m.agent }) : t('stats.model.unknown')
}

function sourceLabel(source: string): string {
  if (source === 'job') return t('stats.source.job')
  if (source === 'session') return t('stats.source.session')
  if (source === 'job+session') return t('stats.source.both')
  return source
}

function successTrack(rate: number | null): string {
  return `${Math.round((rate ?? 0) * 100)}%`
}

async function copyStats(): Promise<void> {
  if (!ov.value) return
  try {
    await navigator.clipboard.writeText(copyText(ov.value))
    toast.value = t('stats.copied')
  } catch {
    toast.value = t('stats.copyFailed')
  }
  window.setTimeout(() => (toast.value = ''), 1600)
}

// 悬停提示：任何带 data-tip 的元素（柱、热力格、带口径说明的数字）。
const tip = ref<{ text: string; x: number; y: number } | null>(null)
function onPointer(e: PointerEvent): void {
  const el = (e.target as Element | null)?.closest?.('[data-tip]') as HTMLElement | SVGElement | null
  const text = el?.getAttribute('data-tip')
  tip.value = text ? { text, x: e.clientX, y: e.clientY } : null
}

onMounted(() => poller.start())
onUnmounted(() => poller.stop())
</script>

<template>
  <div class="stats" @pointermove="onPointer" @pointerleave="tip = null">
    <div class="hd">
      <h1>{{ t('stats.title') }}</h1>
      <span v-if="ov" class="sum mono" data-test="summary">{{ summaryLine(ov) }}</span>
      <div class="right">
        <div class="seg" role="group" :aria-label="t('stats.rangeAria')">
          <button
            v-for="r in RANGES"
            :key="r"
            type="button"
            :aria-pressed="r === range"
            :class="{ 'seg--on': r === range }"
            @click="setRange(r)"
          >
            {{ rangeText(r) }}
          </button>
        </div>
        <button class="ghost btn" type="button" :disabled="!ov" @click="copyStats">{{ t('stats.copy') }}</button>
      </div>
    </div>

    <p v-if="error" class="error mono">{{ error }}</p>
    <div v-if="loading && !ov" class="empty">{{ t('stats.loading') }}</div>

    <template v-if="ov">
      <!-- ① 四张主卡 -->
      <section class="grid4" :aria-label="t('stats.overview')">
        <div class="panel kpi" data-test="kpi-jobs">
          <h3>{{ t('stats.kpi.jobs') }}</h3>
          <div class="big">{{ fmtInt(jobsBig) }}</div>
          <div class="line">
            {{ t('stats.kpi.done') }} <b>{{ fmtInt(ov.jobs.done) }}</b> · {{ t('stats.kpi.failed') }} <b>{{ fmtInt(ov.jobs.failed) }}</b> · {{ t('stats.kpi.running') }}
            <b>{{ fmtInt(ov.jobs.in_progress) }}</b>
          </div>
          <div class="rate">
            <div class="row"><span>{{ t('stats.kpi.rate') }}</span><b>{{ fmtPct(ov.jobs.success_rate) }}</b></div>
            <div class="track" :data-tip="t('stats.kpi.rateTip')">
              <template v-if="ov.jobs.success_rate != null">
                <i :style="{ width: successTrack(ov.jobs.success_rate), background: 'var(--done)' }"></i>
                <i :style="{ flex: 1, background: 'var(--fail)' }"></i>
              </template>
            </div>
          </div>
        </div>

        <div class="panel kpi" data-test="kpi-time">
          <h3>{{ t('stats.kpi.time') }}</h3>
          <div class="big" :data-tip="t('stats.kpi.wallTip')">
            {{ fmtDur(ov.time.wall_sec) }}
          </div>
          <div class="line">
            {{ t('stats.kpi.avg') }} <b>{{ fmtDur(ov.time.avg_sec) }}</b> / job · {{ t('stats.kpi.median') }} <b>{{ fmtDur(ov.time.median_sec) }}</b>
          </div>
          <div class="cells">
            <div :data-tip="t('stats.kpi.activeTip')"><span>{{ t('stats.kpi.active') }}</span><b>{{ fmtDur(ov.time.active_sec) }}</b></div>
            <div :data-tip="t('stats.kpi.humanWaitTip')">
              <span>{{ t('stats.kpi.humanWait') }}</span><b>{{ fmtDur(ov.time.human_wait_sec) }}</b>
            </div>
          </div>
        </div>

        <div class="panel kpi" data-test="kpi-git">
          <h3>{{ t('stats.kpi.git') }}</h3>
          <div class="big">{{ fmtInt(ov.git.commits) }}</div>
          <div class="line">{{ t('stats.kpi.gitLine', { n: fmtInt(ov.git.files) }) }}</div>
          <div class="cells" :data-tip="t('stats.kpi.diffTip')">
            <div><span class="add mono">+{{ fmtInt(ov.git.insertions) }}</span></div>
            <div><span class="del mono">−{{ fmtInt(ov.git.deletions) }}</span></div>
          </div>
        </div>

        <div class="panel kpi" data-test="kpi-signal">
          <h3>
            {{ t('stats.kpi.signal') }}
            <span v-if="signalPartial" class="tag" :data-tip="t('stats.kpi.partialTip')">
              {{ t('stats.kpi.partial') }}
            </span>
          </h3>
          <div class="big">{{ fmtInt(signal?.turns) }}</div>
          <div class="line">
            {{ t('stats.kpi.turnsLine', { n: perJob(signal?.turns, signal?.turn_jobs ?? 0) }) }}
          </div>
          <div class="cells">
            <div><span>{{ t('stats.kpi.toolCalls') }}</span><b>{{ fmtK(signal?.tool_calls) }}</b></div>
            <div :data-tip="t('stats.kpi.humanTip')">
              <span>{{ t('stats.kpi.human') }}</span><b>{{ signal ? fmtInt(signal.human) : DASH }}</b>
            </div>
            <div><span>{{ t('stats.kpi.toolsPerJob') }}</span><b>{{ perJob(signal?.tool_calls, signal?.tool_jobs ?? 0, 0) }}</b></div>
            <div :data-tip="t('stats.kpi.humanRateTip')">
              <span>{{ t('stats.kpi.humanRate') }}</span>
              <b>{{ signal && signal.denominator_jobs ? fmtPct(signal.jobs_with_human / signal.denominator_jobs) : DASH }}</b>
            </div>
          </div>
        </div>
      </section>

      <!-- ② 产出 -->
      <div class="sec">{{ t('stats.sec.output') }}</div>
      <section class="g31">
        <div class="panel">
          <h3>
            {{ t('stats.done') }}
            <span class="r legend">
              <span><i style="background: var(--done)"></i>{{ t('stats.legend.done') }}</span>
              <span><i style="background: var(--fail)"></i>{{ t('stats.legend.failed') }}</span>
            </span>
          </h3>
          <div class="ctl">
            <span class="lab">{{ t('stats.bucketLabel') }}</span>
            <div class="seg" role="group" :aria-label="t('stats.bucketAria')">
              <button
                v-for="b in bucketChoices"
                :key="b"
                type="button"
                :disabled="!buckets.includes(b)"
                :aria-pressed="b === bucket"
                :class="{ 'seg--on': b === bucket }"
                @click="bucket = b"
              >
                {{ bucketText(b) }}
              </button>
            </div>
            <span class="lab max mono">{{ t('stats.peak', { n: chart.max }) }}</span>
          </div>
          <div class="chart" data-test="done-chart">
            <svg :viewBox="`0 0 ${CHART_W} ${CHART_H}`" preserveAspectRatio="none" role="img" :aria-label="t('stats.chartAria')">
              <line x1="0" :x2="CHART_W" y1="14" y2="14" stroke="var(--line)" stroke-dasharray="2 3" vector-effect="non-scaling-stroke" />
              <g v-for="(b, i) in chart.bars" :key="i">
                <rect v-if="b.doneH > 0" :x="b.x" :y="b.doneY" :width="b.w" :height="b.doneH" fill="var(--done)" />
                <rect v-if="b.failH > 0" :x="b.x" :y="b.failY" :width="b.w" :height="b.failH" fill="var(--fail)" />
                <rect :x="b.x" y="0" :width="b.w" :height="CHART_H" fill="transparent" :data-tip="b.tip" />
              </g>
              <line x1="0" :x2="CHART_W" :y1="CHART_H" :y2="CHART_H" stroke="var(--line)" vector-effect="non-scaling-stroke" />
            </svg>
            <div v-if="rows.length" class="axis">
              <span>{{ rows[0].label }}</span><span>{{ rows[rows.length - 1].label }}</span>
            </div>
          </div>
        </div>
        <div class="panel">
          <h3>{{ t('stats.best') }}</h3>
          <div v-if="isToday" class="kv" data-test="best">
            <div :data-tip="t('stats.best.hourTip')">
              <span>{{ t('stats.best.hour') }}</span>
              <b>{{ ov.best.hour ? `${String(ov.best.hour.hour).padStart(2, '0')}:00 · ${ov.best.hour.done}` : DASH }}</b>
            </div>
            <div><span>{{ t('stats.best.todayDone') }}</span><b>{{ fmtInt(ov.jobs.done) }}</b></div>
            <div><span>{{ t('stats.best.weekdayOrDay') }}</span><b>{{ DASH }}</b></div>
            <div><span>{{ t('stats.best.streak') }}</span><b>{{ t('stats.best.days', { n: ov.best.streak_days }) }}</b></div>
          </div>
          <div v-else class="kv" data-test="best">
            <div>
              <span>{{ t('stats.best.weekday') }}</span>
              <b>{{ ov.best.weekday ? t('stats.best.weekdayValue', { day: weekdayText(ov.best.weekday.dow), avg: ov.best.weekday.avg.toFixed(1) }) : DASH }}</b>
            </div>
            <div>
              <span>{{ t('stats.best.day') }}</span>
              <b>{{ ov.best.day ? t('stats.best.dayValue', { day: ov.best.day.day.slice(5), n: ov.best.day.done }) : DASH }}</b>
            </div>
            <div v-if="ov.range.key === 'all'">
              <span>{{ t('stats.best.month') }}</span><b>{{ ov.best.month ? t('stats.best.monthValue', { month: ov.best.month.month, n: fmtInt(ov.best.month.done) }) : DASH }}</b>
            </div>
            <div v-else>
              <span>{{ t('stats.best.dailyAvg') }}</span><b>{{ ov.best.daily_avg != null ? ov.best.daily_avg.toFixed(1) : DASH }}</b>
            </div>
            <div><span>{{ t('stats.best.streak') }}</span><b>{{ t('stats.best.days', { n: ov.best.streak_days }) }}</b></div>
          </div>
        </div>
      </section>
      <section class="grid2">
        <div class="panel act" data-test="activity">
          <h3>{{ t('stats.activity', { n: ov.heatmap.weeks }) }}</h3>
          <div class="act-body" :style="{ '--hmax': `${ov.heatmap.weeks * 22}px` }">
            <div class="heat" data-test="heatmap">
              <div class="days"><span v-for="(label, i) in heatDayLabels" :key="i">{{ label }}</span></div>
              <div class="cells">
                <template v-for="(col, w) in heat" :key="w">
                  <i
                    v-for="c in col"
                    :key="c.day"
                    :class="{ f: c.future }"
                    :data-l="c.level"
                    :data-tip="c.future ? undefined : `${c.day} ${weekdayText(new Date(c.day + 'T00:00:00Z').getUTCDay())}\n${t('stats.heat.done', { n: c.done })}`"
                  ></i>
                </template>
              </div>
              <div class="months"><span v-for="(m, i) in heatMonths" :key="i">{{ m }}</span></div>
              <div class="lg">{{ t('stats.heat.less') }} <i></i><i data-l="1"></i><i data-l="2"></i><i data-l="3"></i><i data-l="4"></i> {{ t('stats.heat.more') }}</div>
            </div>
          <DashboardJobStatus class="act-status" />
          </div>
        </div>
        <div class="panel">
          <h3>{{ t('stats.agents') }}<span class="r mono small">{{ t('stats.agentsHint') }}</span></h3>
          <div class="bars" data-test="agents">
            <div v-for="a in ov.agents" :key="a.agent" class="bar">
              <div class="top">
                <span class="nm">{{ a.agent }}</span>
                <span class="n"><b>{{ fmtInt(a.jobs) }}</b> · {{ fmtPct(a.success_rate) }} · {{ fmtDur(a.avg_sec) }}</span>
              </div>
              <div class="tk"><i :style="{ width: `${(a.jobs / agentMax) * 100}%` }"></i></div>
            </div>
            <div v-if="!ov.agents.length" class="skip">{{ t('stats.agentsEmpty') }}</div>
          </div>
        </div>
      </section>

      <!-- ③ 项目 -->
      <div class="sec">{{ t('stats.sec.projects') }}</div>
      <section class="panel" data-test="projects">
        <div class="grid3 top3">
          <div>
            <h3>{{ t('stats.projects.jobs') }}</h3>
            <ol>
              <li v-for="(p, i) in topJobs" :key="p.project">
                <span class="i">{{ i + 1 }}.</span><span class="p" :title="p.project">{{ projectTail(p.project) }}</span><span class="v">{{ fmtInt(p.jobs) }}</span>
              </li>
            </ol>
          </div>
          <div>
            <h3>{{ t('stats.projects.wall') }}</h3>
            <ol>
              <li v-for="(p, i) in topWall" :key="p.project">
                <span class="i">{{ i + 1 }}.</span><span class="p" :title="p.project">{{ projectTail(p.project) }}</span><span class="v">{{ fmtDur(p.wall_sec) }}</span>
              </li>
            </ol>
          </div>
          <div>
            <h3>{{ t('stats.projects.commits') }}</h3>
            <ol>
              <li v-for="(p, i) in topCommits" :key="p.project">
                <span class="i">{{ i + 1 }}.</span><span class="p" :title="p.project">{{ projectTail(p.project) }}</span><span class="v">{{ fmtInt(p.commits) }}</span>
              </li>
            </ol>
            <div v-if="!topCommits.length" class="skip">{{ DASH }}</div>
          </div>
        </div>
        <details v-if="ov.projects.length" class="more">
          <summary>{{ t('stats.projects.all', { n: ov.projects.length }) }}</summary>
          <div class="bars">
            <div v-for="p in ov.projects" :key="p.project" class="bar">
              <div class="top">
                <span class="nm">{{ p.project || DASH }}</span>
                <span class="n">{{ t('stats.projects.line', { jobs: fmtInt(p.jobs), wall: fmtDur(p.wall_sec), commits: fmtInt(p.commits) }) }}</span>
              </div>
              <div class="tk"><i :style="{ width: `${(p.jobs / projectMax) * 100}%` }"></i></div>
            </div>
          </div>
        </details>
      </section>

      <!-- ④ 验收与计划 -->
      <div class="sec">{{ t('stats.sec.review') }}</div>
      <section class="grid4 tiles" data-test="review">
        <div class="panel tile">
          <div class="lbl">{{ t('stats.review.acceptRate') }}</div>
          <div class="big">{{ fmtPct(ov.review.accept_rate) }}</div>
          <div class="sub">{{ t('stats.review.acceptedLine', { accepted: ov.review.accepted, reviewed: ov.review.reviewed }) }}</div>
        </div>
        <div class="panel tile">
          <div class="lbl">{{ t('stats.review.rejectRate') }}</div>
          <div class="big">{{ fmtPct(ov.review.reject_rate) }}</div>
          <div class="sub">{{ t('stats.review.rejectedLine', { rejected: ov.review.rejected, rerun: ov.review.rerun }) }}</div>
        </div>
        <div class="panel tile">
          <div class="lbl">{{ t('stats.review.waitAvg') }}</div>
          <div class="big">{{ fmtDur(ov.review.wait_avg_sec) }}</div>
          <div class="sub">{{ t('stats.review.waitLine', { median: fmtDur(ov.review.wait_median_sec), pending: ov.review.pending_now }) }}</div>
        </div>
        <div class="panel tile" :data-tip="t('stats.review.plansTip')">
          <div class="lbl">{{ t('stats.review.plansDone') }}</div>
          <div class="big">{{ fmtInt(ov.review.plans_done) }}</div>
          <div class="sub">{{ t('stats.review.plansLine', { n: ov.review.todos_done }) }}</div>
        </div>
      </section>

      <!-- ⑤ 耗时分布 -->
      <div class="sec">{{ t('stats.sec.workload') }}</div>
      <section class="grid2" data-test="workload">
        <div class="panel wl">
          <h3>{{ t('stats.workload.longest') }}<span class="r mono small">{{ t('stats.workload.byActive') }}</span></h3>
          <ol>
            <li v-for="(j, i) in ov.workload.longest" :key="j.id">
              <span class="i">{{ i + 1 }}.</span>
              <RouterLink class="t" :to="`/jobs/${j.id}`" :title="j.title || j.id">{{ j.title || j.id }}</RouterLink>
              <span class="d" :data-tip="j.active_sec == null ? t('stats.workload.noActiveTip') : undefined">{{ fmtDur(j.active_sec ?? j.wall_sec) }}</span>
              <span class="m">{{ j.agent }} · {{ projectTail(j.project) }} · {{ j.turns == null ? DASH : t('stats.workload.turns', { n: j.turns }) }}</span>
              <span class="s">{{ t('stats.workload.span', { d: fmtDur(j.wall_sec) }) }}</span>
            </li>
          </ol>
          <div v-if="!ov.workload.longest.length" class="skip">{{ DASH }}</div>
        </div>
        <div class="panel wl">
          <h3>{{ t('stats.workload.quickest') }}<span class="r mono small">{{ t('stats.workload.quickHint') }}</span></h3>
          <ol>
            <li v-for="(j, i) in ov.workload.quickest" :key="j.id">
              <span class="i">{{ i + 1 }}.</span>
              <RouterLink class="t" :to="`/jobs/${j.id}`" :title="j.title || j.id">{{ j.title || j.id }}</RouterLink>
              <span class="d">{{ fmtDur(j.active_sec ?? j.wall_sec) }}</span>
              <span class="m">{{ j.agent }} · {{ projectTail(j.project) }} · {{ j.turns == null ? DASH : t('stats.workload.turns', { n: j.turns }) }}</span>
              <span class="s">{{ t('stats.workload.span', { d: fmtDur(j.wall_sec) }) }}</span>
            </li>
          </ol>
          <div v-if="!ov.workload.quickest.length" class="skip">{{ DASH }}</div>
        </div>
      </section>

      <!-- ⑥ 用量 -->
      <div class="sec">{{ t('stats.sec.usage') }}</div>
      <section class="panel" data-test="usage">
        <div v-if="ov.usage" class="cost">
          <div>
            <h3>{{ t('stats.usage.cost') }}</h3>
            <div class="big">{{ fmtUSD(ov.usage.cost_usd) }}</div>
            <div class="per">{{ t('stats.usage.perTurn', { turn: fmtUSD(ov.usage.per_turn_usd, 4), job: fmtUSD(ov.usage.per_job_usd, 3) }) }}</div>
            <div class="kv">
              <div><span>{{ t('stats.usage.job') }}</span><b>{{ fmtUSD(ov.usage.job_cost_usd) }}</b></div>
              <div><span>{{ t('stats.usage.sessions', { n: ov.usage.sessions }) }}</span><b>{{ fmtUSD(ov.usage.session_cost_usd) }}</b></div>
              <div><span>{{ t('stats.usage.input') }}</span><b>{{ fmtK(ov.usage.input_tokens) }}</b></div>
              <div><span>{{ t('stats.usage.output') }}</span><b>{{ fmtK(ov.usage.output_tokens) }}</b></div>
              <div><span>{{ t('stats.usage.cache') }}</span><b>{{ fmtK(ov.usage.cache_read_tokens) }}</b></div>
            </div>
          </div>
          <div>
            <h3>{{ t('stats.usage.byModel') }}<span class="r mono small">{{ t('stats.usage.byModelHint') }}</span></h3>
            <div class="models">
              <div v-for="m in ov.usage.by_model" :key="m.model + '|' + (m.agent ?? '')" class="model">
                <span class="nm">{{ modelName(m) }}<span class="src">{{ sourceLabel(m.source) }}</span></span>
                <span class="c">{{ fmtUSD(m.cost_usd) }}</span>
                <div class="tk3">
                  <span>{{ t('stats.usage.in', { n: fmtK(m.input_tokens) }) }}</span><span>{{ t('stats.usage.out', { n: fmtK(m.output_tokens) }) }}</span><span>{{ t('stats.usage.cacheShort', { n: fmtK(m.cache_read_tokens) }) }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>
        <div v-else class="skip">{{ t('stats.usage.empty', { dash: DASH }) }}</div>
      </section>

      <p v-if="notes.length" class="note mono">{{ t('stats.note', { notes: notes.join(noteSeparator) }) }}</p>
    </template>

    <!-- ⑦ 系统（原 Dashboard 卡片，默认收起；展开才订阅实时 stats） -->
    <details class="sys panel" data-test="system" @toggle="sysOpen = ($event.target as HTMLDetailsElement).open">
      <summary><span>{{ t('stats.system') }}</span><span class="mut">{{ t('stats.systemHint') }}</span></summary>
      <DashboardSystem v-if="sysOpen" />
    </details>

    <div v-if="tip" class="tip" :style="{ left: `${tip.x}px`, top: `${tip.y}px` }">{{ tip.text }}</div>
    <div v-if="toast" class="toast" role="status" aria-live="polite">{{ toast }}</div>
  </div>
</template>

<style scoped>
/* 统计墙：面板 12px 圆角、无普通阴影，数字一律等宽（设计稿 §4）。 */
.stats {
  max-width: 1120px;
  margin: 0 auto;
  display: grid;
  gap: var(--s5);
  min-width: 0;
}
.stats > * {
  min-width: 0;
}
.mono,
.big,
.v,
.d,
.sum,
.n,
.c,
.lab {
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
}
.mut,
.small,
.skip,
.axis,
.note {
  color: var(--fg-3);
}
.small {
  font-size: var(--fs-xs);
}
.hd {
  display: flex;
  align-items: center;
  gap: var(--s1) var(--s3);
  flex-wrap: wrap;
}
.hd h1 {
  margin: 0;
  font-size: var(--fs-lg);
  font-weight: 600;
  color: var(--fg);
}
.sum {
  font-size: var(--fs-sm);
  color: var(--fg-3);
}
.right {
  margin-left: auto;
  display: flex;
  gap: var(--s2);
  align-items: center;
  flex-wrap: wrap;
}
.seg {
  display: inline-flex;
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  overflow: hidden;
}
.seg button {
  min-height: 30px;
  padding: 0 var(--s2);
  background: transparent;
  color: var(--fg-3);
  border: 0;
  border-right: 1px solid var(--line);
  font: inherit;
  font-size: var(--fs-sm);
  white-space: nowrap;
}
.seg button:last-child {
  border-right: 0;
}
.seg button:hover:not(:disabled) {
  color: var(--fg);
}
.seg button[disabled] {
  opacity: 0.4;
  cursor: not-allowed;
}
.seg .seg--on {
  background: var(--accent-soft);
  color: var(--accent);
  font-weight: 600;
}
.btn,
.ghost {
  display: inline-flex;
  align-items: center;
  min-height: 30px;
  padding: 0 var(--s2);
  background: var(--surface);
  color: var(--fg);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  white-space: nowrap;
}
.btn:hover:not(:disabled) {
  border-color: var(--accent);
}
.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.error {
  color: var(--fail);
  font-size: var(--fs-sm);
  border: 1px solid var(--fail);
  border-radius: var(--r-ctl);
  padding: var(--s2);
  margin: 0;
  overflow-wrap: anywhere;
}
.empty {
  color: var(--fg-3);
  font-size: var(--fs-sm);
}
.panel {
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  padding: var(--s3);
  min-width: 0;
}
.panel h3 {
  margin: 0 0 var(--s2);
  font-size: var(--fs-sm);
  font-weight: 600;
  color: var(--fg);
  display: flex;
  align-items: baseline;
  gap: var(--s2);
  flex-wrap: wrap;
}
.panel h3 .r {
  margin-left: auto;
}
.grid4 {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--s3);
}
.grid3 {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: var(--s3);
}
.grid2 {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--s3);
}
.g31 {
  display: grid;
  grid-template-columns: minmax(0, 3fr) minmax(0, 1fr);
  gap: var(--s3);
}
.sec {
  display: flex;
  align-items: center;
  gap: var(--s2);
  margin: var(--s2) 0 calc(-1 * var(--s2));
  color: var(--fg-3);
  font-size: var(--fs-xs);
  letter-spacing: 0.1em;
  text-transform: uppercase;
}
.sec::after {
  content: '';
  flex: 1;
  height: 1px;
  background: var(--line);
}
.big {
  font-size: var(--fs-2xl);
  font-weight: 600;
  color: var(--fg);
  line-height: 1.2;
}
.line {
  margin-top: 2px;
  color: var(--fg-2);
  font-size: var(--fs-xs);
}
.line b {
  color: var(--fg);
  font-variant-numeric: tabular-nums;
}
.rate {
  margin-top: var(--s2);
}
.rate .row {
  display: flex;
  justify-content: space-between;
  gap: var(--s2);
  font-size: var(--fs-xs);
  color: var(--fg-3);
}
.rate .row b {
  color: var(--fg);
}
.track {
  display: flex;
  height: 6px;
  margin-top: 4px;
  border-radius: 999px;
  overflow: hidden;
  background: var(--sunken);
}
.track i {
  display: block;
  height: 100%;
}
.cells {
  display: flex;
  flex-wrap: wrap;
  gap: var(--s1) var(--s3);
  margin-top: var(--s2);
  font-size: var(--fs-xs);
}
.cells > div {
  display: flex;
  gap: 4px;
  color: var(--fg-3);
}
.cells > div b {
  color: var(--fg);
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
}
.add {
  color: var(--done);
}
.del {
  color: var(--fail);
}
.tag {
  padding: 1px 6px;
  border-radius: var(--r-chip);
  background: var(--need-soft);
  color: var(--need-ink);
  font-size: var(--fs-xs);
  font-weight: 400;
}
.ctl {
  display: flex;
  align-items: center;
  gap: var(--s2);
  flex-wrap: wrap;
  margin-bottom: var(--s2);
}
.ctl .lab {
  color: var(--fg-3);
  font-size: var(--fs-xs);
}
.ctl .max {
  margin-left: auto;
}
.chart svg {
  display: block;
  width: 100%;
  height: 160px;
}
.axis {
  display: flex;
  justify-content: space-between;
  font-size: var(--fs-xs);
  font-family: var(--font-mono);
  margin-top: 2px;
}
.kv {
  display: grid;
  gap: var(--s1);
}
.kv > div {
  display: flex;
  justify-content: space-between;
  gap: var(--s2);
  font-size: var(--fs-xs);
  color: var(--fg-3);
  padding-bottom: 2px;
  border-bottom: 1px dashed var(--line);
}
.kv > div:last-child {
  border-bottom: 0;
}
.kv b {
  color: var(--fg);
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
}
.act-body {
  display: flex;
  gap: var(--s3);
  align-items: flex-start;
  min-width: 0;
}
.heat {
  min-width: 0;
  max-width: 100%;
  overflow: hidden;
}
.heat .days {
  display: grid;
  grid-template-rows: repeat(7, 1fr);
  gap: 2px;
  font-size: 9px;
  color: var(--fg-3);
  float: left;
  margin-right: 4px;
}
.heat .cells {
  display: grid;
  grid-auto-flow: column;
  grid-template-rows: repeat(7, 1fr);
  gap: 2px;
  overflow-x: auto;
  scrollbar-width: thin;
}
.heat .cells i {
  display: block;
  width: 12px;
  height: 12px;
  border-radius: 2px;
  background: var(--sunken);
}
.heat .cells i[data-l='1'] {
  background: color-mix(in srgb, var(--done) 25%, var(--sunken));
}
.heat .cells i[data-l='2'] {
  background: color-mix(in srgb, var(--done) 45%, var(--sunken));
}
.heat .cells i[data-l='3'] {
  background: color-mix(in srgb, var(--done) 70%, var(--sunken));
}
.heat .cells i[data-l='4'] {
  background: var(--done);
}
.heat .cells i.f {
  opacity: 0.25;
}
.heat .months {
  display: flex;
  gap: 2px;
  font-size: 9px;
  color: var(--fg-3);
  margin-top: 4px;
  padding-left: 16px;
  overflow: hidden;
}
.heat .months span {
  flex: none;
  width: 56px;
  overflow: hidden;
  white-space: nowrap;
}
.heat .lg {
  display: flex;
  align-items: center;
  gap: 3px;
  margin-top: var(--s1);
  font-size: 9px;
  color: var(--fg-3);
  justify-content: flex-end;
}
.heat .lg i {
  display: block;
  width: 10px;
  height: 10px;
  border-radius: 2px;
  background: var(--sunken);
}
.heat .lg i[data-l='1'] {
  background: color-mix(in srgb, var(--done) 25%, var(--sunken));
}
.heat .lg i[data-l='2'] {
  background: color-mix(in srgb, var(--done) 45%, var(--sunken));
}
.heat .lg i[data-l='3'] {
  background: color-mix(in srgb, var(--done) 70%, var(--sunken));
}
.heat .lg i[data-l='4'] {
  background: var(--done);
}
.act-status {
  flex: 0 0 260px;
  min-width: 0;
}
.bars {
  display: grid;
  gap: var(--s2);
}
.bar .top {
  display: flex;
  justify-content: space-between;
  gap: var(--s2);
  font-size: var(--fs-xs);
  min-width: 0;
}
.bar .nm {
  color: var(--fg);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.bar .n {
  color: var(--fg-3);
  white-space: nowrap;
}
.bar .n b {
  color: var(--fg);
}
.tk {
  height: 6px;
  margin-top: 3px;
  border-radius: 999px;
  background: var(--sunken);
  overflow: hidden;
}
.tk i {
  display: block;
  height: 100%;
  background: var(--accent);
}
.top3 ol {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: 2px;
}
.top3 li {
  display: flex;
  align-items: baseline;
  gap: var(--s1);
  font-size: var(--fs-xs);
  color: var(--fg-2);
  min-width: 0;
}
.top3 .i {
  color: var(--fg-3);
  font-family: var(--font-mono);
  min-width: 16px;
}
.top3 .p {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
}
.top3 .v {
  color: var(--fg);
}
.more {
  margin-top: var(--s3);
  border-top: 1px dashed var(--line);
  padding-top: var(--s2);
}
.more summary {
  cursor: pointer;
  color: var(--fg-3);
  font-size: var(--fs-xs);
}
.more .bars {
  margin-top: var(--s2);
}
.tiles .tile .lbl {
  color: var(--fg-3);
  font-size: var(--fs-xs);
}
.tile .sub {
  margin-top: 2px;
  color: var(--fg-3);
  font-size: var(--fs-xs);
}
.wl ol {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: var(--s2);
}
.wl li {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 2px var(--s2);
  font-size: var(--fs-xs);
  min-width: 0;
}
.wl .i {
  color: var(--fg-3);
  font-family: var(--font-mono);
}
.wl .t {
  color: var(--accent);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1 1 60%;
}
.wl .d {
  color: var(--fg);
}
.wl .m,
.wl .s {
  color: var(--fg-3);
  flex: 0 0 100%;
}
.cost {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--s4);
}
.cost .kv {
  margin-top: var(--s2);
}
.per {
  color: var(--fg-3);
  font-size: var(--fs-xs);
  font-family: var(--font-mono);
}
.models {
  display: grid;
  gap: var(--s2);
}
.model {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 2px var(--s2);
  font-size: var(--fs-xs);
  min-width: 0;
}
.model .nm {
  color: var(--fg);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.model .src {
  margin-left: var(--s1);
  padding: 0 5px;
  border-radius: var(--r-chip);
  background: var(--sunken);
  color: var(--fg-3);
  font-size: 9px;
}
.model .c {
  color: var(--fg);
  font-variant-numeric: tabular-nums;
}
.tk3 {
  grid-column: 1 / -1;
  display: flex;
  gap: var(--s2);
  color: var(--fg-3);
  font-family: var(--font-mono);
}
.note {
  margin: 0;
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}
.sys summary {
  cursor: pointer;
  display: flex;
  gap: var(--s2);
  align-items: baseline;
  flex-wrap: wrap;
  font-size: var(--fs-sm);
  color: var(--fg);
}
.sys summary .mut {
  font-size: var(--fs-xs);
}
.tip {
  position: fixed;
  z-index: 90;
  max-width: 320px;
  padding: 4px var(--s2);
  background: var(--surface-2);
  color: var(--fg);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  box-shadow: var(--shadow-pop);
  font-size: var(--fs-xs);
  white-space: pre-line;
  pointer-events: none;
  transform: translate(10px, 12px);
}
.toast {
  position: fixed;
  left: 50%;
  bottom: var(--s4);
  transform: translateX(-50%);
  padding: var(--s2) var(--s3);
  background: var(--surface-2);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  box-shadow: var(--shadow-pop);
  font-size: var(--fs-sm);
  color: var(--fg);
  z-index: 95;
}
@media (max-width: 1024px) {
  .grid4 {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .g31,
  .grid2 {
    grid-template-columns: minmax(0, 1fr);
  }
}
/* 手机（≤760）：单列，热力图与状态分布纵向排列。 */
@media (max-width: 760px) {
  .grid4 {
    grid-template-columns: minmax(0, 1fr);
  }
  .tiles.grid4 {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .grid3,
  .cost {
    grid-template-columns: minmax(0, 1fr);
  }
  .act-body {
    flex-direction: column;
  }
  .act-status {
    flex: 0 0 auto;
    width: 100%;
  }
  .right {
    width: 100%;
  }
  .btn,
  .seg button {
    min-height: 40px;
  }
}
@media (max-width: 520px) {
  .tiles.grid4 {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
