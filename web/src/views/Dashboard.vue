<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { getStats, statusColor } from '../api/client'
import type { AgentSessionRelayMode, AgentSessionState, JobStatus, Stats } from '../api/types'

const POLL_MS = 5000

const stats = ref<Stats | null>(null)
const loading = ref(false)
const error = ref('')
const online = ref(false)

const jobStatuses: JobStatus[] = [
  'running',
  'recovering', // RECOV-01：worker 断线 held 中（非终态）
  'pending_interaction',
  // GATE-01 S3：人工验收（非终态，等人裁决）——统计里必须出现，否则一个等人验收的 job
  // 在首页看起来像"什么都没发生"。
  'needs_review',
  'queued',
  'done',
  'failed',
  'cancelled',
  'timeout',
  'rejected',
]

let timer: number | null = null

const hasStats = computed(() => stats.value != null)

async function fetchStats(): Promise<void> {
  loading.value = true
  try {
    stats.value = await getStats()
    error.value = ''
    online.value = true
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    online.value = false
  } finally {
    loading.value = false
  }
}

function startPolling(): void {
  stopPolling()
  if (document.hidden) {
    return
  }
  timer = window.setInterval(() => {
    void fetchStats()
  }, POLL_MS)
}

function stopPolling(): void {
  if (timer != null) {
    window.clearInterval(timer)
    timer = null
  }
}

function onVisibility(): void {
  if (document.hidden) {
    stopPolling()
  } else {
    void fetchStats()
    startPolling()
  }
}

function jobCount(status: JobStatus): number {
  return stats.value?.jobs.by_status[status] ?? 0
}

// 长状态名在芯片里显示缩写（列宽限制），完整状态放 title。
const SHORT: Partial<Record<JobStatus, string>> = {
  pending_interaction: 'pending',
  needs_review: 'review',
}
function shortStatus(status: JobStatus): string {
  return SHORT[status] ?? status
}

// chipTitle 只给被缩写的芯片挂 tooltip；其余状态名本身就是全称，无需悬停提示。
function chipTitle(status: JobStatus): string | undefined {
  const short = shortStatus(status)
  return short === status ? undefined : status
}

const SESSION_STATES: AgentSessionState[] = [
  'running',
  'waiting_reply',
  'needs_attention',
  'handed_off',
  'idle',
  'ended',
]

const SESSION_STATE_LABELS: Record<AgentSessionState, string> = {
  running: '执行中',
  waiting_reply: '等待回复',
  needs_attention: '需注意',
  handed_off: '已接管',
  idle: '空闲',
  ended: '已结束',
}

const RELAY_MODES: AgentSessionRelayMode[] = ['auto', 'on', 'off']

function sessionCount(state: AgentSessionState): number {
  return stats.value?.sessions.by_state[state] ?? 0
}

// dbTables 是 DB 卡片展示的行数前 8 项：按行数降序、同数按表名，顺序稳定可预期。
const dbTables = computed(() => {
  const tables = stats.value?.db.tables ?? {}
  return Object.entries(tables)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .slice(0, 8)
})

// dbTail 只显示 db 路径的末段（完整路径放 title）。
const dbTail = computed(() => {
  const p = stats.value?.db.path ?? ''
  return p.split(/[\\/]/).filter(Boolean).pop() ?? p
})

const dbTotalSize = computed(() => (stats.value?.db.size_bytes ?? 0) + (stats.value?.db.wal_size_bytes ?? 0))

function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) {
    return '0 B'
  }
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

const serviceVersion = computed(() => stats.value?.version || 'unknown')
const serviceUptime = computed(() => formatUptime(stats.value?.uptime_sec))

// 用量卡（SUP-01 E）：服务端只算这两个窗口（httpapi.statsUsageWindows），缺某个窗口 =
// 该窗口没算（partial 说明预算耗尽），这时显示"未统计"而不是 0。
const USAGE_WINDOWS = ['24h', '7d'] as const
type UsageWindowKey = (typeof USAGE_WINDOWS)[number]
const usageWindow = ref<UsageWindowKey>('24h')

const usageTotal = computed(() => stats.value?.usage.windows[usageWindow.value]?.total ?? null)

// usageRows 按 total_tokens 降序（同数按 agent 名），顺序稳定可预期。
const usageRows = computed(() => {
  const byAgent = stats.value?.usage.windows[usageWindow.value]?.by_agent ?? {}
  return Object.entries(byAgent).sort(
    (a, b) => b[1].total_tokens - a[1].total_tokens || a[0].localeCompare(b[0]),
  )
})

// fmtTokens 与后端 job.FormatTokens 同规则：<1000 原样，其余带 k/M 且保留 3 位有效数字。
function fmtTokens(n: number): string {
  if (!Number.isFinite(n) || n <= 0) {
    return '0'
  }
  if (n < 1000) {
    return String(n)
  }
  if (n < 1_000_000) {
    return `${Number((n / 1000).toPrecision(3))}k`
  }
  return `${Number((n / 1_000_000).toPrecision(3))}M`
}

// fmtCost 与 CLI/详情页同款：4 位小数（成本常常小到 0.0032 这一档）。
function fmtCost(v: number): string {
  return `$${(v || 0).toFixed(4)}`
}

function formatUptime(sec?: number): string {
  if (sec == null || !Number.isFinite(sec) || sec < 0) {
    return '-'
  }
  const s = Math.floor(sec)
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  const r = s % 60
  if (d > 0) {
    return `${d}d${h}h`
  }
  if (h > 0) {
    return `${h}h${m}m`
  }
  if (m > 0) {
    return `${m}m`
  }
  return `${r}s`
}

onMounted(() => {
  void fetchStats()
  startPolling()
  document.addEventListener('visibilitychange', onVisibility)
})

onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <div class="board">
    <div class="head">
      <h1 class="title mono">DASHBOARD</h1>
      <div class="ctrls mono">
        <span class="poll" :class="{ 'poll--on': loading }">●</span>
        <span>每 5s 刷新</span>
      </div>
    </div>

    <p v-if="error" class="error mono">{{ error }}</p>
    <div v-if="loading && !hasStats" class="empty mono">正在加载 stats...</div>

    <div v-if="hasStats" class="grid">
      <div class="card service-card">
        <a
          class="service-logo-link"
          href="https://github.com/inhere/gofer"
          target="_blank"
          rel="noopener noreferrer"
          aria-label="在 GitHub 查看 Gofer 仓库"
        >
          <img class="service-logo" src="/gofer-mark-256.png" alt="Gofer" />
        </a>
        <h3>服务</h3>
        <div class="health">
          <span class="dot" :class="online ? 'dot--on' : 'dot--off'"></span>
          <span class="big service-state">{{ online ? 'LIVE' : 'OFFLINE' }}</span>
        </div>
        <div class="unit mono">{{ serviceVersion }}</div>
        <div class="unit mono">uptime {{ serviceUptime }}</div>
      </div>

      <div class="card">
        <h3>Drivers 在线</h3>
        <div class="big mono">{{ stats?.drivers.online ?? 0 }}</div>
        <div class="unit mono">
          含 supervisor <b>{{ stats?.drivers.supervisors ?? 0 }}</b>
        </div>
      </div>

      <div class="card">
        <h3>Runners</h3>
        <div class="big mono">
          {{ stats?.runners.workers_connected ?? 0
          }}<span class="unit"> / {{ stats?.runners.workers_total ?? 0 }} worker</span>
        </div>
        <div class="unit mono">peers up {{ stats?.runners.peers_up ?? 0 }}</div>
      </div>

      <div class="card">
        <h3>需人工介入</h3>
        <div
          class="big mono"
          :class="{ 'big--fail': (stats?.escalations_pending ?? 0) > 0 }"
        >
          {{ stats?.escalations_pending ?? 0 }}
        </div>
        <div class="unit mono">needs_human（pending 子集）</div>
      </div>

      <div class="card span2">
        <h3>Jobs 状态分布 · total <span class="mono">{{ stats?.jobs.total ?? 0 }}</span></h3>
        <div class="statrow">
          <div v-for="status in jobStatuses" :key="status" class="stat">
            <span class="n mono" :style="{ color: statusColor(status) }">
              {{ jobCount(status) }}
            </span>
            <span class="l mono" :title="chipTitle(status)">{{ shortStatus(status) }}</span>
          </div>
        </div>
      </div>

      <div class="card">
        <h3>Schedules</h3>
        <div class="big mono">
          {{ stats?.schedules.total ?? 0 }}<span class="unit"> 条</span>
        </div>
        <div class="unit mono">enabled {{ stats?.schedules.enabled ?? 0 }}</div>
      </div>

      <div class="card">
        <h3>Projects</h3>
        <div class="big mono">{{ stats?.projects ?? 0 }}</div>
        <div class="unit mono">已登记</div>
      </div>

      <div class="card span2">
        <h3>Server DB</h3>
        <div class="big mono">{{ fmtBytes(dbTotalSize) }}</div>
        <div class="unit mono" :title="stats?.db.path ?? ''">
          {{ dbTail }} · wal {{ fmtBytes(stats?.db.wal_size_bytes ?? 0) }}
        </div>
        <div class="unit mono">
          page {{ stats?.db.page_size ?? 0 }} × {{ stats?.db.page_count ?? 0 }}
          <span v-if="stats?.db.partial" class="partial">行数超预算，仅部分</span>
        </div>
        <div class="dbtables">
          <div v-for="[name, rows] in dbTables" :key="name" class="dbtable">
            <span class="dt-n mono">{{ rows }}</span>
            <span class="dt-k mono">{{ name }}</span>
          </div>
        </div>
      </div>

      <div class="card span2">
        <h3>
          Agent 用量
          <span class="usage-tabs">
            <button
              v-for="w in USAGE_WINDOWS"
              :key="w"
              type="button"
              class="usage-tab mono"
              :class="{ 'usage-tab--on': w === usageWindow }"
              @click="usageWindow = w"
            >
              {{ w }}
            </button>
          </span>
        </h3>
        <div class="big mono">{{ fmtTokens(usageTotal?.total_tokens ?? 0) }}<span class="unit"> tokens</span></div>
        <div class="unit mono">
          {{ usageTotal?.jobs ?? 0 }} job
          <template v-if="(usageTotal?.cost_usd ?? 0) > 0"> · {{ fmtCost(usageTotal?.cost_usd ?? 0) }}</template>
          <span v-if="stats?.usage.partial" class="partial">预算耗尽，仅部分窗口</span>
        </div>
        <div class="dbtables">
          <div v-for="[agent, u] in usageRows" :key="agent" class="dbtable">
            <span class="dt-n mono">{{ fmtTokens(u.total_tokens) }}</span>
            <span class="dt-k mono">
              {{ agent }} · {{ u.jobs }} job<template v-if="u.cost_usd > 0"> · {{ fmtCost(u.cost_usd) }}</template>
            </span>
          </div>
        </div>
        <div v-if="usageRows.length === 0" class="unit mono">该窗口内没有采集到用量</div>
      </div>

      <RouterLink to="/sessions" class="card span2 card--link">
        <h3>Sessions</h3>
        <div class="big mono">{{ stats?.sessions.total ?? 0 }}</div>
        <div class="unit mono">
          等待回复 <b>{{ stats?.sessions.waiting_turns ?? 0 }}</b> · 近 1h 活跃
          {{ stats?.sessions.seen_within_1h ?? 0 }}
        </div>
        <div class="states">
          <span
            v-for="state in SESSION_STATES"
            :key="state"
            class="state mono"
            :class="`state--${state}`"
          >
            {{ SESSION_STATE_LABELS[state] }} {{ sessionCount(state) }}
          </span>
        </div>
        <div class="unit mono relay">
          relay
          <span v-for="mode in RELAY_MODES" :key="mode" class="relay-mode">
            {{ mode }} <b>{{ stats?.sessions.by_relay_mode[mode] ?? 0 }}</b>
          </span>
        </div>
      </RouterLink>
    </div>

    <div v-if="!loading && !error && !hasStats" class="empty mono">暂无 stats 数据</div>
  </div>
</template>

<style scoped>
.board {
  max-width: 1160px;
  margin: 0 auto;
}
.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  margin-bottom: 14px;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
}
.ctrls {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--queue);
  font-size: 12px;
}
.poll {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
}
.poll--on {
  color: var(--phosphor);
}
.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0 0 12px;
  word-break: break-word;
}
.grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
}
.card {
  min-height: 132px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  padding: 14px;
}
.service-card {
  position: relative;
  padding-right: 72px;
}
.service-logo-link {
  position: absolute;
  top: 14px;
  right: 14px;
  border-radius: 10px;
  line-height: 0;
}
.service-logo-link:hover {
  text-decoration: none;
}
.service-logo {
  display: block;
  width: 44px;
  height: 44px;
}
.card h3 {
  color: var(--queue);
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.04em;
  margin: 0 0 12px;
}
.span2 {
  grid-column: span 2;
}
.health {
  display: flex;
  align-items: center;
  gap: 10px;
}
.dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  flex: none;
}
.dot--on {
  background: var(--done);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--done) 22%, transparent);
}
.dot--off {
  background: var(--fail);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--fail) 18%, transparent);
}
.big {
  color: var(--paper);
  font-size: 34px;
  font-weight: 700;
  line-height: 1.05;
  word-break: break-word;
}
.service-state {
  font-size: 20px;
  letter-spacing: 0.06em;
}
.big--fail {
  color: var(--fail);
}
.unit {
  color: var(--queue);
  font-size: 12px;
  font-weight: 400;
}
.unit b {
  color: var(--phosphor);
}
.card > .unit,
.big + .unit,
.health + .unit {
  margin-top: 10px;
}
.statrow {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(90px, 1fr));
  gap: 8px;
}
.stat {
  min-width: 90px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--ink);
  padding: 10px 9px;
}
.n {
  display: block;
  font-size: 24px;
  font-weight: 700;
  line-height: 1.05;
}
.l {
  display: block;
  color: var(--queue);
  font-size: 11px;
  line-height: 1.2;
  margin-top: 5px;
  white-space: normal;
  word-break: break-word;
}
.empty {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--queue);
  font-size: 13px;
  padding: 28px 14px;
  text-align: center;
}

/* Server DB 卡：各表行数（前 8 项）两列排布 + 超预算标记 */
.partial {
  color: var(--fail);
  font-size: 11px;
  margin-left: 6px;
}
/* Agent 用量卡（SUP-01 E）：24h/7d 切换——两个小按钮，选中的那个用主题绿。 */
.usage-tabs {
  display: inline-flex;
  gap: 4px;
  margin-left: 8px;
}
.usage-tab {
  background: transparent;
  border: 1px solid var(--line);
  border-radius: 9px;
  color: var(--queue);
  cursor: pointer;
  font-size: 11px;
  padding: 1px 7px;
}
.usage-tab--on {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.dbtables {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 2px 12px;
  margin-top: 10px;
}
.dbtable {
  display: flex;
  align-items: baseline;
  gap: 7px;
  font-size: 12px;
  overflow: hidden;
}
.dt-n {
  color: var(--paper);
  font-weight: 700;
  text-align: right;
  min-width: 42px;
}
.dt-k {
  color: var(--queue);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Sessions 卡：整卡跳 /sessions */
.card--link {
  display: block;
  color: inherit;
}
.card--link:hover {
  border-color: var(--phosphor);
  text-decoration: none;
}
.card--link:focus-visible {
  outline: 1px solid var(--phosphor);
  outline-offset: 2px;
}
.card--link h3 {
  margin-bottom: 10px;
}
.states {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 10px;
}
.state {
  border: 1px solid var(--line);
  border-radius: 9px;
  color: var(--queue);
  font-size: 11px;
  padding: 1px 7px;
  white-space: nowrap;
}
.state--running {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.state--waiting_reply {
  color: var(--run);
  border-color: var(--run);
}
.state--needs_attention {
  color: var(--fail);
  border-color: var(--fail);
}
.relay {
  display: flex;
  align-items: center;
  gap: 8px;
}
.relay-mode {
  color: var(--paper);
}

@media (max-width: 980px) {
  .grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 700px) {
  .head {
    align-items: flex-start;
    flex-direction: column;
  }
  .grid {
    grid-template-columns: 1fr;
  }
  .span2 {
    grid-column: auto;
  }
  .statrow {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
