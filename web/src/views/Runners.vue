<script setup lang="ts">
// Runners「舰队名册」：按真实运行器分类分组 —— Workers（新能力，置顶）/ Peers / Local。
//  - 轮询 listRunners（4s），Page Visibility 暂停/恢复。
//  - 轮询间隔之间每秒本地推进心跳/探活年龄，让 worker 行“活着”。
//  - 失败软处理：保留上一帧数据，仅在头部给出 in-voice 错误条，不清空页面。
//  - worker 心跳脉冲为唯一“张扬”元素；peer-http/local 用静态点，舰队可见地在跳。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import Heartbeat from '../components/Heartbeat.vue'
import ClusterTopology from '../components/ClusterTopology.vue'
import { listProjects, listRunners, registerWorker, reloadWorker, upgradeWorker } from '../api/client'
import type { Runner, RunnersServerInfo } from '../api/types'
import { beatOf, fmtAge, fmtUptime, upgradeBlockReason, upgradeStateClass, upgradeSummary, workerAgeMs, workerStatusText } from '../utils/runners'

const POLL_MS = 4000

const runners = ref<Runner[]>([])
const projects = ref<string[]>([])
const loading = ref(false)
const error = ref('')
const loaded = ref(false)
const reloading = ref<string | null>(null)
const reloadNotice = ref('')
const serverInfo = ref<RunnersServerInfo | undefined>(undefined)
const upgrading = ref<string | null>(null)
const upgradeNotice = ref('')
const topologyOpen = ref(true)
const addOpen = ref(false)
const addID = ref('')
const addLabels = ref('')
const addProjects = ref('')
const addBusy = ref(false)
const addError = ref('')
const issuedToken = ref('')
const issuedCommand = ref('')
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
    const [resp, projectsResp] = await Promise.all([listRunners(), listProjects().catch(() => null)])
    runners.value = resp.runners ?? []
    serverInfo.value = resp.server
    projects.value = projectsResp?.projects ?? []
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

async function reloadWorkerConfig(workerID: string): Promise<void> {
  if (reloading.value) return
  reloading.value = workerID
  reloadNotice.value = ''
  try {
    const result = await reloadWorker(workerID)
    reloadNotice.value = result.applied
      ? `worker ${workerID} 配置已重新加载`
      : `worker ${workerID} 未应用：${result.error || result.detail || '未知原因'}`
    await fetchRunners()
  } catch (e) {
    reloadNotice.value = `worker ${workerID} 重载请求失败：${e instanceof Error ? e.message : String(e)}`
  } finally {
    reloading.value = null
  }
}

// 用 server 自身二进制升级 worker。在途任务会先等到结束（最长 10 分钟），期间 worker 不接新任务；
// 最终结果（已升级 / 已回滚）由 4s 轮询带回的 upgrade 记录显示。
async function upgradeWorkerBinary(r: Runner): Promise<void> {
  const id = r.worker_id || r.name
  if (upgrading.value) return
  const from = r.worker?.gofer_version || '?'
  const to = serverInfo.value?.version || 'server 当前版本'
  if (!window.confirm(`用 server 自身二进制升级 worker ${id}？\n当前 ${from} → ${to}\n在途任务会先等待结束（最长 10 分钟），期间不接新任务；失败会自动回滚。`)) return
  upgrading.value = id
  upgradeNotice.value = ''
  try {
    const out = await upgradeWorker(id)
    upgradeNotice.value = `worker ${id} 已接受新二进制（${out.upgrade.target_version || '?'}），正在升级…`
    await fetchRunners()
  } catch (e) {
    upgradeNotice.value = `worker ${id} 升级请求失败：${e instanceof Error ? e.message : String(e)}`
  } finally {
    upgrading.value = null
  }
}

async function addWorker(): Promise<void> {
  const id = addID.value.trim()
  if (!id || addBusy.value) return
  addBusy.value = true
  addError.value = ''
  issuedToken.value = ''
  try {
    const out = await registerWorker(id, addLabels.value.split(',').map((x) => x.trim()).filter(Boolean), addProjects.value.split(',').map((x) => x.trim()).filter(Boolean))
    issuedToken.value = out.worker_token
    issuedCommand.value = `gofer init worker --server ${window.location.origin} --id ${out.worker_id} --token ${out.worker_token} --yes`
    addID.value = ''
    addLabels.value = ''
    addProjects.value = ''
    await fetchRunners()
  } catch (e) {
    addError.value = e instanceof Error ? e.message : String(e)
  } finally {
    addBusy.value = false
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
  try { topologyOpen.value = localStorage.getItem('gofer.runners.topology-open') !== 'false' } catch { /* ignore storage failures */ }
  if (window.matchMedia('(max-width: 640px)').matches) topologyOpen.value = false
  void fetchRunners()
  startPolling()
  document.addEventListener('visibilitychange', onVisibility)
})

watch(topologyOpen, (value) => {
  try { localStorage.setItem('gofer.runners.topology-open', String(value)) } catch { /* ignore storage failures */ }
})

function onTopologyToggle(event: Event): void {
  topologyOpen.value = (event.target as HTMLDetailsElement).open
}

onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})

function workerStatusClass(r: Runner): string {
  const beat = beatOf(r, nowMs.value)
  if (beat === 'connected') {
    return 'st--ok'
  }
  if (beat === 'stale') {
    return 'st--warn'
  }
  return 'st--down'
}

// 节点信息行是否有内容（hostname / 来源地址 / os / 版本 / 启动时间任一存在）。
function hasNodeInfo(r: Runner): boolean {
  const w = r.worker
  return !!w && !!(w.hostname || w.remote_addr || w.os || w.gofer_version || w.started_at)
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
    <p v-if="reloadNotice" class="reload-notice mono">{{ reloadNotice }}</p>
    <p v-if="upgradeNotice" class="reload-notice mono">{{ upgradeNotice }}</p>
    <section class="group add-worker">
      <header class="group-head">
        <h2 class="group-title mono">添加 worker</h2>
        <button class="reload-btn mono" type="button" @click="addOpen = !addOpen">{{ addOpen ? '收起' : '添加 worker' }}</button>
      </header>
      <form v-if="addOpen" class="add-form" @submit.prevent="addWorker">
        <label class="mono">id <input v-model="addID" required pattern="[A-Za-z0-9][A-Za-z0-9._-]{0,63}" /></label>
        <label class="mono">labels <input v-model="addLabels" placeholder="linux,gpu" /></label>
        <label class="mono">projects <input v-model="addProjects" placeholder="project-a,project-b" /></label>
        <button class="reload-btn mono" type="submit" :disabled="addBusy">{{ addBusy ? '登记中…' : '登记' }}</button>
      </form>
      <p v-if="addError" class="error mono">登记失败：{{ addError }}</p>
      <div v-if="issuedToken" class="issued mono">
        <p>一次性 worker token（请立即保存）：<code>{{ issuedToken }}</code></p>
        <p>worker 机器运行：<code>{{ issuedCommand }}</code></p>
      </div>
    </section>

    <section class="group topology-group">
      <details :open="topologyOpen" @toggle="onTopologyToggle">
        <summary class="group-head"><h2 class="group-title mono">拓扑 / TOPOLOGY</h2><span class="topology-toggle mono">{{ topologyOpen ? '收起' : '展开' }}</span></summary>
      <ClusterTopology :runners="runners" :projects="projects" :now-ms="nowMs" />
      </details>
    </section>

    <!-- WORKERS（主角，置顶） -->
    <section class="group" aria-labelledby="grp-workers">
      <header class="group-head">
        <h2 id="grp-workers" class="group-title mono">Workers</h2>
        <span class="group-count mono">{{ workers.length }}</span>
      </header>

      <div v-if="workers.length" class="cards">
        <article v-for="w in workers" :key="w.name" class="card card--worker">
          <div class="card-pulse">
            <Heartbeat :beat="beatOf(w, nowMs)" :label="workerStatusText(w, nowMs)" />
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
                >{{ w.status === 'connected' ? fmtAge(workerAgeMs(w, nowMs)) : 'offline' }}</span>
                <span class="dot-sep" aria-hidden="true">·</span>
                <span class="inflight">{{ w.worker?.in_flight ?? 0 }} in-flight</span>
                <span v-if="w.worker?.messenger_status" class="dot-sep">·</span>
                <span v-if="w.worker?.messenger_status" class="messenger-status">
                  messenger {{ w.worker.messenger_status }}
                </span>
              </span>
              <span class="st mono" :class="workerStatusClass(w)">{{ workerStatusText(w, nowMs) }}</span>
            </div>
            <!-- 节点信息行：hostname（机器标识）· 来源地址 · os/arch · 版本 · 运行时长 -->
            <div v-if="hasNodeInfo(w)" class="node-line mono">
              <span v-if="w.worker?.hostname" class="node-host" :title="`hostname ${w.worker.hostname}`">{{ w.worker.hostname }}</span>
              <span v-if="w.worker?.remote_addr" class="node-item" :title="`remote addr ${w.worker.remote_addr}`">{{ w.worker.remote_addr }}</span>
              <span v-if="w.worker?.os" class="node-item">{{ w.worker.os }}/{{ w.worker.arch || '?' }}</span>
              <span v-if="w.worker?.gofer_version" class="node-item" :title="`gofer ${w.worker.gofer_version}`">v{{ w.worker.gofer_version }}</span>
              <span v-if="w.worker?.started_at" class="node-item">{{ fmtUptime(w.worker.started_at, nowMs) }}</span>
            </div>
            <div v-if="w.worker?.labels && w.worker.labels.length" class="chips">
              <span v-for="l in w.worker.labels" :key="l" class="chip mono">{{ l }}</span>
            </div>
            <button class="reload-btn mono" type="button" :disabled="reloading === w.worker_id" @click="reloadWorkerConfig(w.worker_id || w.name)">
              {{ reloading === w.worker_id ? '重新加载中…' : '重新加载配置' }}
            </button>
            <button
              class="reload-btn mono"
              type="button"
              data-test="upgrade-worker"
              :disabled="!!upgradeBlockReason(w, serverInfo) || upgrading === (w.worker_id || w.name)"
              :title="upgradeBlockReason(w, serverInfo) || '用 server 自身二进制升级此 worker'"
              @click="upgradeWorkerBinary(w)"
            >
              {{ upgrading === (w.worker_id || w.name) || w.upgrade?.state === 'pending' ? '升级中…' : '升级' }}
            </button>
            <p v-if="w.worker?.draining" class="upgrade-line mono st--warn">排空中：不再接新任务，等在途任务结束</p>
            <p v-if="w.upgrade" class="upgrade-line mono" :class="upgradeStateClass(w.upgrade)">{{ upgradeSummary(w.upgrade) }}</p>
            <p v-if="upgradeBlockReason(w, serverInfo) && w.upgrade?.state !== 'pending'" class="upgrade-hint mono">{{ upgradeBlockReason(w, serverInfo) }}</p>
            <details v-if="w.upgrade_history?.length" class="upgrade-history">
              <summary class="upgrade-hint mono">升级历史（{{ w.upgrade_history.length }}）</summary>
              <ul class="upgrade-history-list mono">
                <li v-for="item in w.upgrade_history" :key="item.upgrade_id">
                  <span :class="upgradeStateClass(item)">{{ upgradeSummary(item) }}</span>
                </li>
              </ul>
            </details>
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
.upgrade-line { margin: 6px 0 0; font-size: 12px; }
.upgrade-hint { margin: 4px 0 0; font-size: 11px; opacity: 0.7; }
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

.reload-notice {
  color: var(--run);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0 0 14px;
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

.reload-btn {
  margin-top: 10px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: transparent;
  color: var(--queue);
  padding: 4px 8px;
  cursor: pointer;
}
.reload-btn:hover:not(:disabled) { color: var(--paper); border-color: var(--paper); }
.reload-btn:disabled { cursor: wait; opacity: 0.55; }
.reload-btn + .reload-btn { margin-left: 8px; }
.upgrade-history { margin-top: 6px; }
.upgrade-history-list { margin: 4px 0 0; padding-left: 18px; }
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

/* 节点信息行：安静的第二行 meta（hostname 用磷光强调 = 机器标识主键） */
.node-line {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 10px;
  margin-top: 6px;
  font-size: 11px;
  color: var(--queue);
  overflow: hidden;
}
.node-host {
  color: var(--phosphor);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 220px;
}
.node-item {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 200px;
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

@media (max-width: 640px) {
  .runners {
    max-width: none;
  }
  .topology-group {
    margin-bottom: 16px;
  }
  .topology-group details > .group-head {
    margin-bottom: 0;
    cursor: pointer;
  }
  .topology-group details[open] > .group-head {
    margin-bottom: 10px;
  }
  .card {
    gap: 8px;
    padding: 10px;
  }
  .card-pulse {
    width: 20px;
  }
  .card-row1 {
    gap: 5px;
  }
  .card-name {
    max-width: min(52vw, 190px);
  }
  .card-meta {
    flex-basis: 100%;
    order: 3;
    font-size: 10px;
  }
  .node-line {
    font-size: 10px;
  }
  .wid,
  .host,
  .node-host,
  .node-item {
    max-width: 42vw;
  }
}
.topology-toggle {
  margin-left: auto;
  color: var(--phosphor);
  font-size: 12px;
  cursor: pointer;
}
</style>
