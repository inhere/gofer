<script setup lang="ts">
// 统计墙（gofer-yelm，design docs/design/2026-10-09-dashboard-redesign-design.md）：回答
// 「这段时间系统和 agent 干得怎么样」。一个接口 GET /v1/stats/overview 给整页数据，周 / 月
// 分桶与热力图在前端算（utils/dashStats）。没有数据来源的指标显示「—」，不显示 0。
// 实时系统卡片收进底部「系统」折叠区（DashboardSystem，展开才挂载、才订阅 stats 推送）。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { getStatsOverview, type Overview, type OverviewRange } from '../api/overview'
import DashboardSystem from '../components/DashboardSystem.vue'
import {
  allowedBuckets,
  barLayout,
  bucketize,
  copyText,
  DASH,
  defaultBucket,
  fmtDur,
  fmtInt,
  fmtK,
  fmtPct,
  fmtUSD,
  heatGrid,
  localToday,
  NOTE_TEXT,
  perJob,
  RANGE_LABEL,
  summaryLine,
  topN,
  WEEKDAY,
  type Bucket,
} from '../utils/dashStats'
import { createPoller } from '../utils/poller'

const RANGES: OverviewRange[] = ['7d', '30d', 'all']
const BUCKET_LABEL: Record<Bucket, string> = { day: '日', week: '周', month: '月' }
// 每 60s 刷新（页面可见时）；服务端同样按 60s / 5min 缓存。
const REFRESH_MS = 60_000

const range = ref<OverviewRange>('30d')
const bucket = ref<Bucket>(defaultBucket('30d'))
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
const rows = computed(() => bucketize(ov.value?.daily ?? [], bucket.value))
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

const jobsBig = computed(() => (ov.value ? ov.value.jobs.total + ov.value.jobs.in_progress : 0))
const signal = computed(() => ov.value?.signal ?? null)
const signalPartial = computed(() => signal.value != null && signal.value.coverage < 0.9)

const agentMax = computed(() => Math.max(1, ...(ov.value?.agents ?? []).map((a) => a.jobs)))
const projectMax = computed(() => Math.max(1, ...(ov.value?.projects ?? []).map((p) => p.jobs)))
const topJobs = computed(() => topN(ov.value?.projects ?? [], (p) => p.jobs))
const topWall = computed(() => topN(ov.value?.projects ?? [], (p) => p.wall_sec))
const topCommits = computed(() => topN(ov.value?.projects ?? [], (p) => p.commits))

const notes = computed(() => (ov.value?.notes ?? []).map((n) => NOTE_TEXT[n] ?? n))

function projectTail(p: string): string {
  return p.split('/').filter(Boolean).pop() || p || DASH
}

function modelName(m: { model: string; agent?: string }): string {
  if (m.model) return m.model
  return m.agent ? `${m.agent} 默认` : '未知模型'
}

const SOURCE_LABEL: Record<string, string> = { job: 'job', session: '会话', 'job+session': 'job+会话' }

function successTrack(rate: number | null): string {
  return `${Math.round((rate ?? 0) * 100)}%`
}

async function copyStats(): Promise<void> {
  if (!ov.value) return
  try {
    await navigator.clipboard.writeText(copyText(ov.value))
    toast.value = '已复制统计（纯文本）'
  } catch {
    toast.value = '复制失败：浏览器不允许写剪贴板'
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
      <h1>统计</h1>
      <span v-if="ov" class="sum mono" data-test="summary">{{ summaryLine(ov) }}</span>
      <div class="right">
        <div class="seg" role="group" aria-label="时间范围">
          <button
            v-for="r in RANGES"
            :key="r"
            type="button"
            :aria-pressed="r === range"
            @click="setRange(r)"
          >
            {{ RANGE_LABEL[r] }}
          </button>
        </div>
        <button class="ghost" type="button" :disabled="!ov" @click="copyStats">复制统计</button>
      </div>
    </div>

    <p v-if="error" class="error mono">{{ error }}</p>
    <div v-if="loading && !ov" class="empty mono">正在加载统计...</div>

    <template v-if="ov">
      <!-- ① 四张主卡 -->
      <section class="grid4" aria-label="概览">
        <div class="panel kpi" data-test="kpi-jobs">
          <h3>Jobs</h3>
          <div class="big">{{ fmtInt(jobsBig) }}</div>
          <div class="line">
            完成 <b>{{ fmtInt(ov.jobs.done) }}</b> · 失败 <b>{{ fmtInt(ov.jobs.failed) }}</b> · 进行中
            <b>{{ fmtInt(ov.jobs.in_progress) }}</b>
          </div>
          <div class="rate">
            <div class="row"><span>成功率</span><b>{{ fmtPct(ov.jobs.success_rate) }}</b></div>
            <div
              class="track"
              data-tip="成功率 = done ÷ (done + failed + timeout + rejected)&#10;取消的、待验收的不计入"
            >
              <template v-if="ov.jobs.success_rate != null">
                <i :style="{ width: successTrack(ov.jobs.success_rate), background: 'var(--done)' }"></i>
                <i :style="{ flex: 1, background: 'var(--fail)' }"></i>
              </template>
            </div>
          </div>
        </div>

        <div class="panel kpi" data-test="kpi-time">
          <h3>耗时</h3>
          <div class="big" data-tip="运行时长 = Σ(ended_at − started_at)&#10;从开跑算起，不含排队">
            {{ fmtDur(ov.time.wall_sec) }}
          </div>
          <div class="line">
            平均 <b>{{ fmtDur(ov.time.avg_sec) }}</b> / job · 中位 <b>{{ fmtDur(ov.time.median_sec) }}</b>
          </div>
          <div class="cells">
            <div data-tip="活跃 = 运行时长 − 等人时长"><span>活跃</span><b>{{ fmtDur(ov.time.active_sec) }}</b></div>
            <div data-tip="等工具审批 / 提问，以及会话 job 两轮之间等人发话的时长&#10;不含等验收（见验收区）">
              <span>等人</span><b>{{ fmtDur(ov.time.human_wait_sec) }}</b>
            </div>
          </div>
        </div>

        <div class="panel kpi" data-test="kpi-git">
          <h3>Git 活动</h3>
          <div class="big">{{ fmtInt(ov.git.commits) }}</div>
          <div class="line">提交 · 改动 <b>{{ fmtInt(ov.git.files) }}</b> 个文件</div>
          <div class="cells" data-tip="job 的 base 到结束时工作树的 diff（含未提交的已跟踪文件）">
            <div><span class="add mono">+{{ fmtInt(ov.git.insertions) }}</span></div>
            <div><span class="del mono">−{{ fmtInt(ov.git.deletions) }}</span></div>
          </div>
        </div>

        <div class="panel kpi" data-test="kpi-signal">
          <h3>
            信号
            <span v-if="signalPartial" class="tag" data-tip="部分 job 没有采集到这些指标（老 job 可用 gofer tool stats-backfill 补算）">
              部分 job 无数据
            </span>
          </h3>
          <div class="big">{{ fmtInt(signal?.turns) }}</div>
          <div class="line">
            轮次 · 每 job <b>{{ perJob(signal?.turns, signal?.turn_jobs ?? 0) }}</b> 轮
          </div>
          <div class="cells">
            <div><span>工具调用</span><b>{{ fmtK(signal?.tool_calls) }}</b></div>
            <div data-tip="人介入 = 人回答的工具审批 / 提问 + 给会话 job 的追加消息">
              <span>人介入</span><b>{{ signal ? fmtInt(signal.human) : DASH }}</b>
            </div>
            <div><span>每 job 工具</span><b>{{ perJob(signal?.tool_calls, signal?.tool_jobs ?? 0, 0) }}</b></div>
            <div data-tip="有人介入的 job ÷ 非 exec 的结束 job">
              <span>介入率</span>
              <b>{{ signal && signal.denominator_jobs ? fmtPct(signal.jobs_with_human / signal.denominator_jobs) : DASH }}</b>
            </div>
          </div>
        </div>
      </section>

      <!-- ② 产出 -->
      <div class="sec">产出</div>
      <section class="g31">
        <div class="panel">
          <h3>
            完成的 job
            <span class="r legend">
              <span><i style="background: var(--done)"></i>完成</span>
              <span><i style="background: var(--fail)"></i>失败</span>
            </span>
          </h3>
          <div class="ctl">
            <span class="lab">分桶</span>
            <div class="seg" role="group" aria-label="分桶">
              <button
                v-for="b in (['day', 'week', 'month'] as Bucket[])"
                :key="b"
                type="button"
                :disabled="!buckets.includes(b)"
                :aria-pressed="b === bucket"
                @click="bucket = b"
              >
                {{ BUCKET_LABEL[b] }}
              </button>
            </div>
            <span class="lab max mono">峰值 {{ chart.max }}</span>
          </div>
          <div class="chart" data-test="done-chart">
            <svg :viewBox="`0 0 ${CHART_W} ${CHART_H}`" preserveAspectRatio="none" role="img" aria-label="完成 job 随时间">
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
          <h3>最高产</h3>
          <div class="kv" data-test="best">
            <div>
              <span>最佳星期</span>
              <b>{{ ov.best.weekday ? `${WEEKDAY[ov.best.weekday.dow]} · 平均 ${ov.best.weekday.avg.toFixed(1)}` : DASH }}</b>
            </div>
            <div>
              <span>最佳单日</span>
              <b>{{ ov.best.day ? `${ov.best.day.day.slice(5)} · ${ov.best.day.done}` : DASH }}</b>
            </div>
            <div v-if="ov.range.key === 'all'">
              <span>最佳月份</span><b>{{ ov.best.month ? `${ov.best.month.month} · ${fmtInt(ov.best.month.done)}` : DASH }}</b>
            </div>
            <div v-else>
              <span>日均完成</span><b>{{ ov.best.daily_avg != null ? ov.best.daily_avg.toFixed(1) : DASH }}</b>
            </div>
            <div><span>连续有产出</span><b>{{ ov.best.streak_days }} 天</b></div>
          </div>
        </div>
      </section>
      <section class="grid2">
        <div class="panel">
          <h3>活跃度（近 {{ ov.heatmap.weeks }} 周）</h3>
          <div class="heat" data-test="heatmap" :style="{ '--hmax': `${ov.heatmap.weeks * 22}px` }">
            <div class="days"><span>一</span><span></span><span>三</span><span></span><span>五</span><span></span><span>日</span></div>
            <div class="cells">
              <template v-for="(col, w) in heat" :key="w">
                <i
                  v-for="c in col"
                  :key="c.day"
                  :class="{ f: c.future }"
                  :data-l="c.level"
                  :data-tip="c.future ? undefined : `${c.day} ${WEEKDAY[new Date(c.day + 'T00:00:00Z').getUTCDay()]}\n完成 ${c.done}`"
                ></i>
              </template>
            </div>
            <div class="months"><span v-for="(m, i) in heatMonths" :key="i">{{ m }}</span></div>
            <div class="lg">少 <i></i><i data-l="1"></i><i data-l="2"></i><i data-l="3"></i><i data-l="4"></i> 多</div>
          </div>
        </div>
        <div class="panel">
          <h3>Agent<span class="r mono small">job 数 · 成功率 · 平均运行</span></h3>
          <div class="bars" data-test="agents">
            <div v-for="a in ov.agents" :key="a.agent" class="bar">
              <div class="top">
                <span class="nm">{{ a.agent }}</span>
                <span class="n"><b>{{ fmtInt(a.jobs) }}</b> · {{ fmtPct(a.success_rate) }} · {{ fmtDur(a.avg_sec) }}</span>
              </div>
              <div class="tk"><i :style="{ width: `${(a.jobs / agentMax) * 100}%` }"></i></div>
            </div>
            <div v-if="!ov.agents.length" class="skip">范围内没有结束的 job</div>
          </div>
        </div>
      </section>

      <!-- ③ 项目 -->
      <div class="sec">项目</div>
      <section class="panel" data-test="projects">
        <div class="grid3 top3">
          <div>
            <h3>Job 数</h3>
            <ol>
              <li v-for="(p, i) in topJobs" :key="p.project">
                <span class="i">{{ i + 1 }}.</span><span class="p" :title="p.project">{{ projectTail(p.project) }}</span><span class="v">{{ fmtInt(p.jobs) }}</span>
              </li>
            </ol>
          </div>
          <div>
            <h3>运行时长</h3>
            <ol>
              <li v-for="(p, i) in topWall" :key="p.project">
                <span class="i">{{ i + 1 }}.</span><span class="p" :title="p.project">{{ projectTail(p.project) }}</span><span class="v">{{ fmtDur(p.wall_sec) }}</span>
              </li>
            </ol>
          </div>
          <div>
            <h3>提交</h3>
            <ol>
              <li v-for="(p, i) in topCommits" :key="p.project">
                <span class="i">{{ i + 1 }}.</span><span class="p" :title="p.project">{{ projectTail(p.project) }}</span><span class="v">{{ fmtInt(p.commits) }}</span>
              </li>
            </ol>
            <div v-if="!topCommits.length" class="skip">{{ DASH }}</div>
          </div>
        </div>
        <details v-if="ov.projects.length" class="more">
          <summary>全部项目（{{ ov.projects.length }}）</summary>
          <div class="bars">
            <div v-for="p in ov.projects" :key="p.project" class="bar">
              <div class="top">
                <span class="nm">{{ p.project || DASH }}</span>
                <span class="n"><b>{{ fmtInt(p.jobs) }}</b> jobs · {{ fmtDur(p.wall_sec) }} · {{ fmtInt(p.commits) }} 提交</span>
              </div>
              <div class="tk"><i :style="{ width: `${(p.jobs / projectMax) * 100}%` }"></i></div>
            </div>
          </div>
        </details>
      </section>

      <!-- ④ 验收与计划 -->
      <div class="sec">验收与计划</div>
      <section class="grid4 tiles" data-test="review">
        <div class="panel tile">
          <div class="lbl">验收通过率</div>
          <div class="big">{{ fmtPct(ov.review.accept_rate) }}</div>
          <div class="sub">通过 {{ ov.review.accepted }} / 已验收 {{ ov.review.reviewed }}</div>
        </div>
        <div class="panel tile">
          <div class="lbl">退回率</div>
          <div class="big">{{ fmtPct(ov.review.reject_rate) }}</div>
          <div class="sub">退回 {{ ov.review.rejected }} · 其中附意见重跑 {{ ov.review.rerun }}</div>
        </div>
        <div class="panel tile">
          <div class="lbl">平均等待验收</div>
          <div class="big">{{ fmtDur(ov.review.wait_avg_sec) }}</div>
          <div class="sub">中位 {{ fmtDur(ov.review.wait_median_sec) }} · 现在待验收 {{ ov.review.pending_now }}</div>
        </div>
        <div class="panel tile" data-tip="Plan 没有完成时间戳，按最后更新时间近似">
          <div class="lbl">Plan 完成</div>
          <div class="big">{{ fmtInt(ov.review.plans_done) }}</div>
          <div class="sub">todo 完成 {{ ov.review.todos_done }}</div>
        </div>
      </section>

      <!-- ⑤ 耗时分布 -->
      <div class="sec">耗时分布</div>
      <section class="grid2" data-test="workload">
        <div class="panel wl">
          <h3>最长<span class="r mono small">按活跃时长</span></h3>
          <ol>
            <li v-for="(j, i) in ov.workload.longest" :key="j.id">
              <span class="i">{{ i + 1 }}.</span>
              <RouterLink class="t" :to="`/jobs/${j.id}`" :title="j.title || j.id">{{ j.title || j.id }}</RouterLink>
              <span class="d" :data-tip="j.active_sec == null ? '没有活跃时长数据，按运行时长排序' : undefined">{{ fmtDur(j.active_sec ?? j.wall_sec) }}</span>
              <span class="m">{{ j.agent }} · {{ projectTail(j.project) }} · {{ j.turns == null ? DASH : j.turns }} 轮</span>
              <span class="s">跨度 {{ fmtDur(j.wall_sec) }}</span>
            </li>
          </ol>
          <div v-if="!ov.workload.longest.length" class="skip">{{ DASH }}</div>
        </div>
        <div class="panel wl">
          <h3>最快<span class="r mono small">按活跃时长 · 不含 exec / 失败</span></h3>
          <ol>
            <li v-for="(j, i) in ov.workload.quickest" :key="j.id">
              <span class="i">{{ i + 1 }}.</span>
              <RouterLink class="t" :to="`/jobs/${j.id}`" :title="j.title || j.id">{{ j.title || j.id }}</RouterLink>
              <span class="d">{{ fmtDur(j.active_sec ?? j.wall_sec) }}</span>
              <span class="m">{{ j.agent }} · {{ projectTail(j.project) }} · {{ j.turns == null ? DASH : j.turns }} 轮</span>
              <span class="s">跨度 {{ fmtDur(j.wall_sec) }}</span>
            </li>
          </ol>
          <div v-if="!ov.workload.quickest.length" class="skip">{{ DASH }}</div>
        </div>
      </section>

      <!-- ⑥ 用量 -->
      <div class="sec">用量</div>
      <section class="panel" data-test="usage">
        <div v-if="ov.usage" class="cost">
          <div>
            <h3>费用</h3>
            <div class="big">{{ fmtUSD(ov.usage.cost_usd) }}</div>
            <div class="per">{{ fmtUSD(ov.usage.per_turn_usd, 4) }} / 轮 · {{ fmtUSD(ov.usage.per_job_usd, 3) }} / job</div>
            <div class="kv">
              <div><span>job</span><b>{{ fmtUSD(ov.usage.job_cost_usd) }}</b></div>
              <div><span>终端会话（{{ ov.usage.sessions }}）</span><b>{{ fmtUSD(ov.usage.session_cost_usd) }}</b></div>
              <div><span>输入</span><b>{{ fmtK(ov.usage.input_tokens) }}</b></div>
              <div><span>输出</span><b>{{ fmtK(ov.usage.output_tokens) }}</b></div>
              <div><span>缓存读</span><b>{{ fmtK(ov.usage.cache_read_tokens) }}</b></div>
            </div>
          </div>
          <div>
            <h3>按模型<span class="r mono small">job + 终端会话</span></h3>
            <div class="models">
              <div v-for="m in ov.usage.by_model" :key="m.model + '|' + (m.agent ?? '')" class="model">
                <span class="nm">{{ modelName(m) }}<span class="src">{{ SOURCE_LABEL[m.source] ?? m.source }}</span></span>
                <span class="c">{{ fmtUSD(m.cost_usd) }}</span>
                <div class="tk3">
                  <span>入 {{ fmtK(m.input_tokens) }}</span><span>出 {{ fmtK(m.output_tokens) }}</span><span>缓存 {{ fmtK(m.cache_read_tokens) }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>
        <div v-else class="skip">范围内没有 job 或终端会话上报用量（{{ DASH }}）</div>
      </section>

      <p v-if="notes.length" class="note mono">口径：{{ notes.join('；') }}</p>
    </template>

    <!-- ⑦ 系统（原 Dashboard 卡片，默认收起；展开才订阅实时 stats） -->
    <details class="sys panel" data-test="system" @toggle="sysOpen = ($event.target as HTMLDetailsElement).open">
      <summary><span>系统</span><span class="mut">版本 · 运行时长 · runner · DB · Sessions</span></summary>
      <DashboardSystem v-if="sysOpen" />
    </details>

    <div v-if="tip" class="tip mono" :style="{ left: `${tip.x}px`, top: `${tip.y}px` }">{{ tip.text }}</div>
    <div v-if="toast" class="toast" role="status" aria-live="polite">{{ toast }}</div>
  </div>
</template>

<style scoped>
.stats {
  max-width: 1080px;
  margin: 0 auto;
  display: grid;
  gap: 22px;
  min-width: 0;
}
.stats > * {
  min-width: 0;
}
.mono,
.big,
.v,
.d {
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
}
.mut,
.small {
  color: var(--queue);
}
.small {
  font-size: 11px;
}
.hd {
  display: flex;
  align-items: center;
  gap: 10px 14px;
  flex-wrap: wrap;
}
.hd h1 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
  color: var(--paper);
}
.sum {
  font-size: 12.5px;
  color: var(--queue);
}
.right {
  margin-left: auto;
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.seg {
  display: inline-flex;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.seg button {
  background: transparent;
  border: 0;
  border-right: 1px solid var(--line);
  color: var(--queue);
  font-family: var(--font-mono);
  font-size: 12px;
  padding: 4px 10px;
  cursor: pointer;
}
.seg button:last-child {
  border-right: 0;
}
.seg button[aria-pressed='true'] {
  background: var(--phosphor);
  color: var(--ink);
  font-weight: 600;
}
.seg button:disabled {
  opacity: 0.4;
  cursor: default;
}
.ghost {
  background: transparent;
  border: 1px solid var(--line);
  color: var(--paper);
  border-radius: var(--radius);
  padding: 4px 9px;
  font-size: 12px;
  font-family: var(--font-mono);
  cursor: pointer;
}
.ghost:hover:not(:disabled) {
  border-color: var(--queue);
}
.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0;
  word-break: break-word;
}
.empty,
.skip {
  color: var(--queue);
  font-size: 12px;
}
.empty {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 28px 14px;
  text-align: center;
}
.note {
  font-size: 11px;
  color: var(--queue);
  border: 1px dashed var(--line);
  border-radius: var(--radius);
  padding: 6px 10px;
  margin: 0;
  overflow-wrap: anywhere;
}
.sec {
  display: flex;
  align-items: center;
  gap: 10px;
  font-family: var(--font-mono);
  font-size: 10.5px;
  letter-spacing: 0.14em;
  color: var(--queue);
  margin: 4px 0 -10px;
}
.sec::after {
  content: '';
  flex: 1;
  border-top: 1px solid var(--line);
}
.panel {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 14px 16px;
  min-width: 0;
}
.panel h3 {
  margin: 0 0 10px;
  font-size: 13px;
  font-weight: 500;
  color: var(--queue);
  display: flex;
  gap: 8px;
  align-items: baseline;
  flex-wrap: wrap;
}
.panel h3 .r {
  margin-left: auto;
}
.grid4 {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
}
.grid3 {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 12px;
}
.grid2 {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}
.g31 {
  display: grid;
  grid-template-columns: minmax(0, 2.4fr) minmax(0, 1fr);
  gap: 12px;
}
.kpi {
  display: grid;
  gap: 6px;
  align-content: start;
}
.kpi h3 {
  margin: 0;
}
.kpi .big {
  font-size: 30px;
  font-weight: 500;
  line-height: 1.1;
  color: var(--paper);
}
.kpi .line {
  font-size: 12.5px;
  color: var(--queue);
}
.kpi .line b {
  color: var(--paper);
  font-weight: 500;
}
.kpi .cells {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px 14px;
  border-top: 1px solid var(--line);
  padding-top: 8px;
  margin-top: 2px;
}
.kpi .cells div {
  display: flex;
  justify-content: space-between;
  gap: 6px;
  font-size: 12px;
  color: var(--queue);
}
.kpi .cells b {
  font-family: var(--font-mono);
  color: var(--paper);
  font-weight: 500;
}
.rate {
  display: grid;
  gap: 4px;
  font-size: 12px;
  color: var(--queue);
}
.rate .row {
  display: flex;
  justify-content: space-between;
}
.rate .row b {
  font-family: var(--font-mono);
  color: var(--paper);
  font-weight: 500;
}
.track {
  height: 6px;
  background: var(--line);
  border-radius: 3px;
  overflow: hidden;
  display: flex;
  gap: 2px;
}
.track i {
  display: block;
  height: 100%;
  border-radius: 3px;
}
.add {
  color: var(--done);
}
.del {
  color: var(--fail);
}
.tag {
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--run);
  border: 1px dashed var(--run);
  border-radius: 3px;
  padding: 0 5px;
}
.chart svg {
  display: block;
  width: 100%;
  height: 160px;
}
.axis {
  display: flex;
  justify-content: space-between;
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--queue);
  margin-top: 4px;
}
.legend {
  display: flex;
  gap: 12px;
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--queue);
  align-items: center;
}
.legend i {
  display: inline-block;
  width: 9px;
  height: 9px;
  border-radius: 2px;
  margin-right: 4px;
  vertical-align: -1px;
}
.ctl {
  display: flex;
  gap: 10px;
  align-items: center;
  flex-wrap: wrap;
  margin-bottom: 10px;
}
.ctl .lab {
  font-family: var(--font-mono);
  font-size: 10.5px;
  letter-spacing: 0.12em;
  color: var(--queue);
}
.ctl .max {
  margin-left: auto;
  letter-spacing: 0;
}
.kv {
  display: grid;
  gap: 12px;
}
.cost .kv {
  margin-top: 12px;
}
.kv div {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  font-size: 13px;
  color: var(--queue);
}
.kv b {
  font-family: var(--font-mono);
  color: var(--paper);
  font-weight: 500;
  text-align: right;
}
.heat {
  display: grid;
  grid-template-columns: 22px minmax(0, 1fr);
  gap: 4px 6px;
  font-family: var(--font-mono);
  font-size: 10px;
  color: var(--queue);
}
.heat .days {
  display: grid;
  grid-template-rows: repeat(7, 1fr);
  gap: 2px;
}
.heat .cells {
  display: grid;
  grid-auto-flow: column;
  grid-template-rows: repeat(7, 1fr);
  gap: 2px;
  max-width: var(--hmax, 100%);
}
.heat .cells i {
  aspect-ratio: 1;
  border-radius: 2px;
  background: var(--line);
}
.heat .cells i.f {
  visibility: hidden;
}
.heat i[data-l='1'] {
  background: color-mix(in srgb, var(--done) 35%, var(--line));
}
.heat i[data-l='2'] {
  background: color-mix(in srgb, var(--done) 60%, var(--line));
}
.heat i[data-l='3'] {
  background: color-mix(in srgb, var(--done) 82%, var(--line));
}
.heat i[data-l='4'] {
  background: var(--done);
}
.heat .months {
  grid-column: 2;
  display: flex;
  justify-content: space-between;
  max-width: var(--hmax, 100%);
}
.heat .lg {
  grid-column: 2;
  display: flex;
  gap: 3px;
  align-items: center;
  justify-content: flex-end;
  max-width: var(--hmax, 100%);
}
.heat .lg i {
  width: 10px;
  height: 10px;
  border-radius: 2px;
  background: var(--line);
}
.bars {
  display: grid;
  gap: 10px;
}
.bar .top {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  font-size: 13px;
  min-width: 0;
}
.bar .top .n {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--queue);
  white-space: nowrap;
}
.bar .top .n b {
  color: var(--paper);
  font-weight: 500;
}
.bar .nm {
  font-family: var(--font-mono);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--paper);
}
.bar .tk {
  height: 5px;
  background: var(--line);
  border-radius: 3px;
  margin-top: 4px;
  overflow: hidden;
}
.bar .tk i {
  display: block;
  height: 100%;
  background: var(--phosphor);
  border-radius: 3px;
}
.top3 ol,
.wl ol {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
}
.top3 ol {
  gap: 8px;
}
.top3 li {
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr) auto;
  gap: 6px;
  font-size: 13px;
  align-items: baseline;
  color: var(--paper);
}
.top3 .i,
.wl .i {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--queue);
}
.top3 .p {
  font-family: var(--font-mono);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tile .lbl {
  font-size: 12px;
  color: var(--queue);
}
.tile .big {
  font-size: 24px;
  font-weight: 500;
  color: var(--paper);
}
.tile .sub {
  font-size: 11.5px;
  color: var(--queue);
  font-family: var(--font-mono);
  margin-top: 2px;
}
.wl ol {
  gap: 12px;
}
.wl li {
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr) auto;
  gap: 2px 8px;
}
.wl .i {
  grid-row: span 2;
  padding-top: 2px;
}
.wl .t {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13.5px;
}
.wl .d {
  text-align: right;
  color: var(--paper);
}
.wl .m,
.wl .s {
  font-size: 11.5px;
  color: var(--queue);
  font-family: var(--font-mono);
}
.wl .s {
  text-align: right;
  white-space: nowrap;
}
.wl .m {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cost {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1.5fr);
  gap: 18px;
}
.cost .big {
  font-size: 30px;
  font-weight: 500;
  color: var(--run);
}
.cost .per {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--phosphor);
}
.models {
  display: grid;
  gap: 10px;
}
.model {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 2px 8px;
}
.model .nm {
  font-family: var(--font-mono);
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--paper);
}
.model .c {
  font-family: var(--font-mono);
  font-size: 13px;
  text-align: right;
  color: var(--paper);
}
.model .tk3 {
  grid-column: 1 / -1;
  display: flex;
  gap: 14px;
  flex-wrap: wrap;
  font-family: var(--font-mono);
  font-size: 11.5px;
  color: var(--queue);
}
.src {
  font-size: 10.5px;
  border: 1px solid var(--line);
  border-radius: 3px;
  padding: 0 4px;
  color: var(--queue);
  margin-left: 6px;
}
details.more summary {
  cursor: pointer;
  list-style: none;
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--phosphor);
  margin-top: 10px;
}
details.more summary::-webkit-details-marker {
  display: none;
}
details.more[open] summary {
  margin-bottom: 10px;
}
details.sys > summary {
  cursor: pointer;
  list-style: none;
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--paper);
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}
details.sys > summary::-webkit-details-marker {
  display: none;
}
details.sys > summary::after {
  content: '展开';
  color: var(--phosphor);
  margin-left: auto;
}
details.sys[open] > summary::after {
  content: '收起';
}
details.sys[open] > summary {
  margin-bottom: 12px;
}
.tip {
  position: fixed;
  z-index: 80;
  pointer-events: none;
  background: var(--paper);
  color: var(--ink);
  font-size: 12px;
  border-radius: var(--radius);
  padding: 5px 8px;
  white-space: pre;
  max-width: 260px;
  transform: translate(-50%, calc(-100% - 12px));
}
.toast {
  position: fixed;
  left: 50%;
  bottom: 20px;
  transform: translateX(-50%);
  z-index: 70;
  background: var(--paper);
  color: var(--ink);
  border-radius: var(--radius);
  padding: 7px 14px;
  font-size: 13px;
}

@media (max-width: 900px) {
  .grid4 {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .g31,
  .cost {
    grid-template-columns: minmax(0, 1fr);
  }
}
@media (max-width: 640px) {
  .grid3,
  .grid2 {
    grid-template-columns: minmax(0, 1fr);
  }
  .right {
    margin-left: 0;
    width: 100%;
  }
  .kpi .big {
    font-size: 26px;
  }
}
@media (max-width: 520px) {
  .grid4 {
    grid-template-columns: minmax(0, 1fr);
  }
  .tiles.grid4 {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .panel {
    padding: 12px;
  }
}
</style>
