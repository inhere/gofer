<script setup lang="ts">
// Runners「舰队名册」：按真实运行器分类分组 —— Workers（新能力，置顶）/ Peers / Local。
//  - 轮询 listRunners（4s），Page Visibility 暂停/恢复。
//  - 轮询间隔之间每秒本地推进心跳/探活年龄，让 worker 行“活着”。
//  - 失败软处理：保留上一帧数据，仅在头部给出 in-voice 错误条，不清空页面。
//  - worker 心跳脉冲为唯一“张扬”元素；peer-http/local 用静态点，舰队可见地在跳。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import Heartbeat from '../components/Heartbeat.vue'
import { listRunners } from '../api/client'
import type { Runner } from '../api/types'

const POLL_MS = 4000
// 心跳过期阈值（毫秒）：约 2× ping(15s)。超过即 stale。
const STALE_MS = 30_000

const runners = ref<Runner[]>([])
const loading = ref(false)
const error = ref('')
const loaded = ref(false)
// 本地时钟（毫秒）：用于在两次轮询之间推进“xx ago”年龄，使其逐秒走动。
const nowMs = ref(Date.now())

let pollTimer: number | null = null
let tickTimer: number | null = null

const workers = computed(() => runners.value.filter((r) => r.type === 'worker'))
const peers = computed(() => runners.value.filter((r) => r.type === 'peer-http'))
const locals = computed(() => runners.value.filter((r) => r.type === 'local'))

async function fetchRunners(): Promise<void> {
  loading.value = true
  try {
    const resp = await listRunners()
    runners.value = resp.runners ?? []
    error.value = ''
    loaded.value = true
    nowMs.value = Date.now()
  } catch (e) {
    // 401 已由 client 处理（跳转登录）；其余仅给出头部错误条，保留上一帧
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function startPolling(): void {
  stopPolling()
  if (document.hidden) {
    return
  }
  pollTimer = window.setInterval(() => {
    void fetchRunners()
  }, POLL_MS)
  // 逐秒推进本地时钟，使心跳/探活年龄看起来在走动
  tickTimer = window.setInterval(() => {
    nowMs.value = Date.now()
  }, 1000)
}

function stopPolling(): void {
  if (pollTimer != null) {
    window.clearInterval(pollTimer)
    pollTimer = null
  }
  if (tickTimer != null) {
    window.clearInterval(tickTimer)
    tickTimer = null
  }
}

function onVisibility(): void {
  if (document.hidden) {
    stopPolling()
  } else {
    void fetchRunners()
    startPolling()
  }
}

onMounted(() => {
  void fetchRunners()
  startPolling()
  document.addEventListener('visibilitychange', onVisibility)
})

onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})

// ── worker 心跳：实时年龄（轮询年龄 + 本地经过的时间）──
// 后端 heartbeat_age_ms 是上次轮询时刻的快照；叠加本地 now 相对最近一次拉取的增量。
function workerAgeMs(r: Runner): number | null {
  if (!r.worker) {
    return null
  }
  // last_heartbeat 是绝对毫秒时间戳，可直接用本地时钟算实时年龄（避免依赖拉取时刻）
  if (r.worker.last_heartbeat > 0) {
    return Math.max(0, nowMs.value - r.worker.last_heartbeat)
  }
  return Math.max(0, r.worker.heartbeat_age_ms)
}

// 心跳态：connected 且年龄超阈 -> stale；disconnected/unknown -> flatline。
function beatOf(r: Runner): 'connected' | 'stale' | 'flatline' {
  if (r.status !== 'connected') {
    return 'flatline'
  }
  const age = workerAgeMs(r)
  if (age != null && age > STALE_MS) {
    return 'stale'
  }
  return 'connected'
}

// 人类可读年龄：12s ago / 3m20s ago / 1h05m ago。
function fmtAge(ms: number | null): string {
  if (ms == null) {
    return '—'
  }
  const s = Math.floor(ms / 1000)
  if (s < 1) {
    return 'just now'
  }
  if (s < 60) {
    return `${s}s ago`
  }
  if (s < 3600) {
    const m = Math.floor(s / 60)
    const r = s % 60
    return r ? `${m}m${String(r).padStart(2, '0')}s ago` : `${m}m ago`
  }
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  return `${h}h${String(m).padStart(2, '0')}m ago`
}

// worker 状态文案（operator-facing）：connected 12s / no heartbeat 41s / offline。
function workerStatusText(r: Runner): string {
  if (r.status !== 'connected') {
    return 'offline'
  }
  const age = workerAgeMs(r)
  if (age != null && age > STALE_MS) {
    return `no heartbeat ${Math.floor(age / 1000)}s`
  }
  return 'connected'
}

function workerStatusClass(r: Runner): string {
  const beat = beatOf(r)
  if (beat === 'connected') {
    return 'st--ok'
  }
  if (beat === 'stale') {
    return 'st--warn'
  }
  return 'st--down'
}

// ── peer-http 探活：实时年龄 + 延迟 + 错误 ──
function probeAgeMs(r: Runner): number | null {
  if (!r.probe || r.probe.checked_at <= 0) {
    return null
  }
  return Math.max(0, nowMs.value - r.probe.checked_at)
}

function peerStatusText(r: Runner): string {
  if (r.status === 'up') {
    return 'up'
  }
  if (r.status === 'down') {
    return 'down'
  }
  return 'not probed yet'
}

function peerStatusClass(r: Runner): string {
  if (r.status === 'up') {
    return 'st--ok'
  }
  if (r.status === 'down') {
    return 'st--down'
  }
  return 'st--unknown'
}
</script>

<template>
  <div class="runners">
    <div class="head">
      <span class="eyebrow mono">FLEET</span>
      <h1 class="title mono">RUNNERS</h1>
      <span class="poll-hint mono" :class="{ 'poll-hint--on': loading }" aria-hidden="true">●</span>
    </div>

    <p v-if="error" class="error mono" :title="error">舰队状态拉取失败：{{ error }}</p>

    <!-- WORKERS（主角，置顶） -->
    <section class="group" aria-labelledby="grp-workers">
      <header class="group-head">
        <h2 id="grp-workers" class="group-title mono">Workers</h2>
        <span class="group-count mono">{{ workers.length }}</span>
      </header>

      <div v-if="workers.length" class="cards">
        <article v-for="w in workers" :key="w.name" class="card card--worker">
          <div class="card-pulse">
            <Heartbeat :beat="beatOf(w)" :label="workerStatusText(w)" />
          </div>
          <div class="card-main">
            <div class="card-row1">
              <span class="card-name">{{ w.name }}</span>
              <span class="card-meta mono">
                <span class="wid" :title="w.worker_id">{{ w.worker_id || '—' }}</span>
                <span class="dot-sep" aria-hidden="true">·</span>
                <span
                  class="age"
                  :class="{ 'age--down': w.status !== 'connected' }"
                  :title="w.worker ? `last heartbeat ${new Date(w.worker.last_heartbeat).toLocaleString()}` : ''"
                >{{ w.status === 'connected' ? fmtAge(workerAgeMs(w)) : 'offline' }}</span>
                <span class="dot-sep" aria-hidden="true">·</span>
                <span class="inflight">{{ w.worker?.in_flight ?? 0 }} in-flight</span>
              </span>
              <span class="st mono" :class="workerStatusClass(w)">{{ workerStatusText(w) }}</span>
            </div>
            <div v-if="w.worker?.labels && w.worker.labels.length" class="chips">
              <span v-for="l in w.worker.labels" :key="l" class="chip mono">{{ l }}</span>
            </div>
          </div>
        </article>
      </div>

      <!-- 空 workers 态：邀请接入 -->
      <div v-else class="empty empty--invite">
        <p class="empty-line">No workers connected.</p>
        <p class="empty-hint mono">Bring one online to grow the fleet:</p>
        <code class="empty-cmd mono">gofer worker --config worker.yaml</code>
      </div>
    </section>

    <!-- PEERS（peer-http） -->
    <section class="group" aria-labelledby="grp-peers">
      <header class="group-head">
        <h2 id="grp-peers" class="group-title mono">Peers</h2>
        <span class="group-count mono">{{ peers.length }}</span>
      </header>

      <div v-if="peers.length" class="cards">
        <article v-for="p in peers" :key="p.name" class="card card--peer">
          <div class="card-pulse">
            <span class="static-dot" :class="peerStatusClass(p)" aria-hidden="true"></span>
          </div>
          <div class="card-main">
            <div class="card-row1">
              <span class="card-name">{{ p.name }}</span>
              <span class="card-meta mono">
                <span class="host" :title="p.base_url">{{ p.base_url || '—' }}</span>
                <template v-if="p.probe && p.probe.checked_at > 0">
                  <span class="dot-sep" aria-hidden="true">·</span>
                  <span class="age">{{ fmtAge(probeAgeMs(p)) }}</span>
                  <span class="dot-sep" aria-hidden="true">·</span>
                  <span class="latency">{{ p.probe.latency_ms }}ms</span>
                </template>
              </span>
              <span class="st mono" :class="peerStatusClass(p)">{{ peerStatusText(p) }}</span>
            </div>
            <p v-if="p.status === 'down' && p.probe?.error" class="probe-err mono">
              {{ p.probe.error }}
            </p>
          </div>
        </article>
      </div>

      <div v-else class="empty">No peers configured.</div>
    </section>

    <!-- LOCAL（恒在，恒 up） -->
    <section class="group" aria-labelledby="grp-local">
      <header class="group-head">
        <h2 id="grp-local" class="group-title mono">Local</h2>
        <span class="group-count mono">{{ locals.length }}</span>
      </header>

      <div v-if="locals.length" class="cards">
        <article v-for="l in locals" :key="l.name" class="card card--local">
          <div class="card-pulse">
            <span class="static-dot st--ok" aria-hidden="true"></span>
          </div>
          <div class="card-main">
            <div class="card-row1">
              <span class="card-name">{{ l.name }}</span>
              <span class="card-meta mono">
                <span class="host">in-process</span>
              </span>
              <span class="st mono st--ok">up</span>
            </div>
          </div>
        </article>
      </div>

      <div v-else-if="loaded" class="empty">No local runner.</div>
    </section>
  </div>
</template>

<style scoped>
.runners {
  /* 收窄到内容尺度：卡片不再被拉满，状态列不再被甩到远端留大空场 */
  max-width: 760px;
  margin: 0 auto;
}

.head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 18px;
}
.eyebrow {
  font-size: 10px;
  letter-spacing: 0.18em;
  color: var(--queue);
  text-transform: uppercase;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
}
.poll-hint {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
  margin-left: auto;
}
.poll-hint--on {
  color: var(--phosphor);
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0 0 14px;
  word-break: break-word;
}

/* 分组 = 真实运行器分类（结构性，非装饰） */
.group {
  margin-bottom: 26px;
}
.group-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding-bottom: 8px;
  margin-bottom: 12px;
  border-bottom: 1px solid var(--line);
}
.group-title {
  font-size: 13px;
  letter-spacing: 0.06em;
  color: var(--paper);
  margin: 0;
}
.group-count {
  font-size: 11px;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 0 7px;
  line-height: 1.6;
}

.cards {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

/* 安静的卡片：发丝线、低圆角、面板底 */
.card {
  display: flex;
  align-items: flex-start;
  gap: 14px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  padding: 12px 14px;
}

.card-pulse {
  flex: none;
  width: 32px;
  display: flex;
  align-items: center;
  justify-content: center;
  padding-top: 2px;
}

.card-main {
  flex: 1;
  min-width: 0;
}
.card-row1 {
  display: flex;
  align-items: center;
  gap: 10px;
}
.card-name {
  font-size: 14px;
  color: var(--paper);
  font-weight: 600;
  flex: none;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 240px;
}

/* 状态文案（mono），按态着色 */
.st {
  font-size: 11px;
  letter-spacing: 0.03em;
  flex: none;
}
.st--ok {
  color: var(--done);
}
.st--warn {
  color: var(--run);
}
.st--down {
  color: var(--fail);
}
.st--unknown {
  color: var(--queue);
}

/* card-meta now rides inline on row1: a flex:1 middle span that fills the gap
   between name and the far-right status, so the worker card has no dead zone. */
.card-meta {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  color: var(--queue);
  overflow: hidden;
  white-space: nowrap;
}
.dot-sep {
  color: var(--line);
}
.wid {
  color: var(--phosphor);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 220px;
}
.host {
  color: var(--phosphor);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 280px;
}
.age {
  color: var(--paper);
}
.age--down {
  color: var(--fail);
}
.inflight {
  color: var(--paper);
}
.latency {
  color: var(--paper);
}

.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 8px;
}
.chip {
  display: inline-block;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 1px 7px;
  color: var(--phosphor);
  font-size: 10px;
}

.probe-err {
  margin: 7px 0 0;
  color: var(--fail);
  font-size: 11px;
  word-break: break-word;
}

/* peer/local 的静态点（不跳动），与 worker 脉冲对比 */
.static-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  display: inline-block;
}
.static-dot.st--ok {
  background: var(--done);
}
.static-dot.st--down {
  background: var(--fail);
}
.static-dot.st--unknown {
  background: transparent;
  box-shadow: inset 0 0 0 1.5px var(--queue);
}

.empty {
  padding: 18px 14px;
  text-align: center;
  color: var(--queue);
  font-size: 13px;
  border: 1px dashed var(--line);
  border-radius: var(--radius);
}
.empty--invite {
  padding: 26px 16px;
}
.empty-line {
  margin: 0 0 8px;
  color: var(--paper);
  font-size: 14px;
}
.empty-hint {
  margin: 0 0 8px;
  color: var(--queue);
  font-size: 12px;
}
.empty-cmd {
  display: inline-block;
  color: var(--phosphor);
  background: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 5px 10px;
  font-size: 12px;
}

@media (max-width: 768px) {
  /* Narrow screens: let row1 stack (name+meta, then status) instead of
     squeezing everything onto one line. */
  .card-row1 {
    flex-wrap: wrap;
  }
  .card-meta {
    flex-basis: 100%;
    order: 3;
  }
  .card-name {
    font-size: 13px;
  }
  .wid,
  .host {
    max-width: 160px;
  }
}
</style>
