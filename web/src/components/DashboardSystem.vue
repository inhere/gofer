<script setup lang="ts">
// 「系统」折叠区（gofer-yelm）：原 Dashboard 的实时系统卡片，只在统计页底部展开时挂载，
// 所以 `stats` 推送主题也只在展开期间订阅。「Jobs 状态分布」已移到上方活跃度卡
// （DashboardJobStatus），原「Agent 用量」卡已删除（用量看统计页的「用量」区）。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { getStats } from '../api/client'
import type { AgentSessionRelayMode, AgentSessionState, Stats } from '../api/types'
import { fmtBytes } from '../utils/bytes'
import { createLiveTopic } from '../utils/useLiveTopic'


const stats = ref<Stats | null>(null)
const loading = ref(false)
const error = ref('')
const online = ref(false)

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

// Q3：`stats` 是快照主题（订阅时即推一份，job/interaction 等变化后合并 ≥2s 再推）；
// WS 断开超过 15s 才由 30s 兜底轮询调 fetchStats。
const liveStats = createLiveTopic('stats', {
  initial: false,
  fetch: fetchStats,
  onSnap: (data) => {
    stats.value = data as Stats
    error.value = ''
    online.value = true
    loading.value = false
  },
})

const SESSION_STATES: AgentSessionState[] = [
  'running',
  'waiting_reply',
  'needs_attention',
  'handed_off',
  'idle',
  'ended',
  'offline',
]

const SESSION_STATE_LABELS: Record<AgentSessionState, string> = {
  running: '执行中',
  waiting_reply: '等待回复',
  needs_attention: '需注意',
  handed_off: '已接管',
  idle: '空闲',
  ended: '已结束',
  offline: '离线',
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
  loading.value = true
  liveStats.start()
})

onUnmounted(() => {
  liveStats.stop()
})
</script>

<template>
  <div class="board" data-test="dashboard-system">
    <p v-if="error" class="error mono">{{ error }}</p>
    <div v-if="loading && !hasStats" class="empty mono">正在加载系统状态...</div>

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
}

@media (max-width: 640px) {
  .grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
  }
  .card {
    min-height: 104px;
    padding: 10px;
  }
  .service-card {
    padding-right: 52px;
  }
  .service-logo-link {
    top: 10px;
    right: 10px;
  }
  .service-logo {
    width: 34px;
    height: 34px;
  }
  .big {
    font-size: 28px;
  }
  .service-state {
    font-size: 16px;
  }
  .card h3 {
    margin-bottom: 8px;
  }
  .span2 {
    grid-column: span 2;
  }
  .unit {
    font-size: 11px;
  }
}
</style>
