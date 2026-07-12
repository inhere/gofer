<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { getStats, statusColor } from '../api/client'
import type { JobStatus, Stats } from '../api/types'

const POLL_MS = 5000

const stats = ref<Stats | null>(null)
const loading = ref(false)
const error = ref('')
const online = ref(false)

const jobStatuses: JobStatus[] = [
  'running',
  'pending_interaction',
  'queued',
  'done',
  'failed',
  'cancelled',
  'timeout',
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

function shortStatus(status: JobStatus): string {
  return status === 'pending_interaction' ? 'pending_i' : status
}

const serviceVersion = computed(() => stats.value?.version || 'unknown')
const serviceUptime = computed(() => formatUptime(stats.value?.uptime_sec))

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
          <img class="service-logo" src="/favicon.svg" alt="Gofer" />
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
            <span class="l mono">{{ shortStatus(status) }}</span>
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
