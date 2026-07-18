<script setup lang="ts">
// Cluster「集群拓扑」（E31，design §6.1）：把 Runners 名册推成 hub-worker-peer-local 星型拓扑。
//  - 数据源现成、纯前端、无新后端：并发 listRunners() + listProjects()，4s 轮询（复用 Runners 节奏）。
//  - SVG 手绘星型（D1，不引重图库）：中心 hub=server；辐射节点均匀分布在圆周（三角函数算坐标）。
//    边用 SVG <line>（viewBox 0..100 + preserveAspectRatio=none + non-scaling-stroke）；
//    节点为绝对定位 HTML（left/top 同百分比对齐边端点），故能直接复用 Heartbeat.vue 而无 foreignObject 命名空间坑。
//  - 点击节点 → 右侧抽屉面板：worker(id/心跳/in_flight/labels/状态) / peer(base_url/latency/error) / local(本机 + server projects 概览)。
//  - 不画「项目→节点」映射边（D2/§10.2：worker.yaml 独立、server 不知 worker 的 projects）。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import Heartbeat from '../components/Heartbeat.vue'
import { listProjects, listRunners } from '../api/client'
import type { Runner } from '../api/types'

const POLL_MS = 4000
// 心跳过期阈值（毫秒）：约 2× ping(15s)。超过即 stale。
const STALE_MS = 30_000

// 星型几何（百分比坐标，相对 stage）：容器宽高比 AR；为让节点摆成视觉圆形，水平半径 = 垂直半径 / AR。
const AR = 1.3
const RY = 36
const RX = RY / AR

const runners = ref<Runner[]>([])
const projects = ref<string[]>([])
const loading = ref(false)
const error = ref('')
const loaded = ref(false)
// 本地时钟（毫秒）：在两次轮询之间逐秒推进「xx ago」年龄。
const nowMs = ref(Date.now())

// 当前选中节点：Runner（某辐射节点）/ 'hub'（中心 server）/ null（未选）。
const selected = ref<Runner | 'hub' | null>(null)

let pollTimer: number | null = null
let tickTimer: number | null = null

// 节点排序：worker（主角）→ peer → local，与 Runners 名册分组一致。
const orderedRunners = computed(() => {
  const w = runners.value.filter((r) => r.type === 'worker')
  const p = runners.value.filter((r) => r.type === 'peer-http')
  const l = runners.value.filter((r) => r.type === 'local')
  return [...w, ...p, ...l]
})

interface TopoNode {
  runner: Runner
  key: string
  xPct: number
  yPct: number
}

// 均匀分布在圆周：第 i 个节点角度 = 顶部(-90°) + i·(360°/N)，再用三角函数算 (x,y) 百分比坐标。
const topoNodes = computed<TopoNode[]>(() => {
  const list = orderedRunners.value
  const n = Math.max(list.length, 1)
  return list.map((r, i) => {
    const theta = -Math.PI / 2 + (i * 2 * Math.PI) / n
    return {
      runner: r,
      key: `${r.type}:${r.name}`,
      xPct: 50 + RX * Math.cos(theta),
      yPct: 50 + RY * Math.sin(theta),
    }
  })
})

// hub（server）合计指标
const inFlightTotal = computed(() =>
  runners.value.reduce((s, r) => s + (r.worker?.in_flight ?? 0), 0),
)
const workerCount = computed(() => runners.value.filter((r) => r.type === 'worker').length)
const peerCount = computed(() => runners.value.filter((r) => r.type === 'peer-http').length)
const localCount = computed(() => runners.value.filter((r) => r.type === 'local').length)

async function fetchData(): Promise<void> {
  loading.value = true
  try {
    const [runnersResp, projectsResp] = await Promise.all([listRunners(), listProjects()])
    runners.value = runnersResp.runners ?? []
    projects.value = projectsResp.projects ?? []
    error.value = ''
    loaded.value = true
    nowMs.value = Date.now()
  } catch (e) {
    // 401 已由 client 处理（跳转登录）；其余仅给头部错误条，保留上一帧
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
    void fetchData()
  }, POLL_MS)
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
    void fetchData()
    startPolling()
  }
}

onMounted(() => {
  void fetchData()
  startPolling()
  document.addEventListener('visibilitychange', onVisibility)
})

onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})

// ── worker 心跳：实时年龄（同 Runners.vue）──
function workerAgeMs(r: Runner): number | null {
  if (!r.worker) {
    return null
  }
  if (r.worker.last_heartbeat > 0) {
    return Math.max(0, nowMs.value - r.worker.last_heartbeat)
  }
  return Math.max(0, r.worker.heartbeat_age_ms)
}

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

// peer-http 探活年龄
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

// 运行时长（同 Runners.vue）：up 3d4h / up 5h02m / up 12m。
function fmtUptime(startedAtSec: number | undefined): string {
  if (!startedAtSec || startedAtSec <= 0) {
    return '—'
  }
  const s = Math.max(0, Math.floor(nowMs.value / 1000) - startedAtSec)
  if (s < 60) {
    return `up ${s}s`
  }
  if (s < 3600) {
    return `up ${Math.floor(s / 60)}m`
  }
  if (s < 86400) {
    const h = Math.floor(s / 3600)
    const m = Math.floor((s % 3600) / 60)
    return `up ${h}h${String(m).padStart(2, '0')}m`
  }
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  return `up ${d}d${h}h`
}

// 节点强调色 token：worker 随心跳态（connected=done / stale=run / flatline=fail），
// peer 随探活（up=done / down=fail / unknown=queue），local 恒 up=done。
function nodeColorVar(r: Runner): string {
  if (r.type === 'worker') {
    const beat = beatOf(r)
    if (beat === 'connected') {
      return 'var(--done)'
    }
    return beat === 'stale' ? 'var(--run)' : 'var(--fail)'
  }
  if (r.type === 'peer-http') {
    if (r.status === 'up') {
      return 'var(--done)'
    }
    return r.status === 'down' ? 'var(--fail)' : 'var(--queue)'
  }
  return 'var(--done)'
}

function dotClass(r: Runner): string {
  if (r.type === 'peer-http') {
    if (r.status === 'up') {
      return 'st--ok'
    }
    return r.status === 'down' ? 'st--down' : 'st--unknown'
  }
  return 'st--ok'
}

function selectRunner(r: Runner): void {
  selected.value = r
}
function selectHub(): void {
  selected.value = 'hub'
}
function closePanel(): void {
  selected.value = null
}

// 当前选中是否某 Runner（用于面板分支 + 拓扑高亮）。
const selectedRunner = computed<Runner | null>(() =>
  selected.value && selected.value !== 'hub' ? selected.value : null,
)
const isHubSelected = computed(() => selected.value === 'hub')

function isActive(r: Runner): boolean {
  const sel = selectedRunner.value
  return !!sel && sel.type === r.type && sel.name === r.name
}
</script>

<template>
  <div class="cluster">
    <div class="head">
      <span class="eyebrow mono">TOPOLOGY</span>
      <h1 class="title mono">CLUSTER</h1>
      <span class="poll-hint mono" :class="{ 'poll-hint--on': loading }" aria-hidden="true">●</span>
    </div>

    <p v-if="error" class="error mono" :title="error">集群状态拉取失败：{{ error }}</p>

    <!-- 星型拓扑舞台 -->
    <div class="stage">
      <!-- 边：SVG line（viewBox 0..100 + non-scaling-stroke），与 HTML 节点同百分比坐标对齐 -->
      <svg
        class="edges"
        viewBox="0 0 100 100"
        preserveAspectRatio="none"
        aria-hidden="true"
      >
        <line
          v-for="node in topoNodes"
          :key="node.key"
          x1="50"
          y1="50"
          :x2="node.xPct"
          :y2="node.yPct"
          :stroke="nodeColorVar(node.runner)"
          :class="{ 'edge--active': isActive(node.runner) }"
          stroke-width="1.2"
          vector-effect="non-scaling-stroke"
        />
      </svg>

      <!-- 中心 hub = server -->
      <button
        type="button"
        class="node node--hub"
        :class="{ 'node--sel': isHubSelected }"
        style="left: 50%; top: 50%"
        @click="selectHub"
      >
        <span class="hub-core" aria-hidden="true"></span>
        <span class="node-name mono">server</span>
        <span class="node-meta mono">{{ inFlightTotal }} in-flight</span>
      </button>

      <!-- 辐射节点 -->
      <button
        v-for="node in topoNodes"
        :key="node.key"
        type="button"
        class="node"
        :class="{ 'node--sel': isActive(node.runner) }"
        :style="{
          left: node.xPct + '%',
          top: node.yPct + '%',
          '--node-color': nodeColorVar(node.runner),
        }"
        @click="selectRunner(node.runner)"
      >
        <span class="node-ind">
          <!-- worker：心跳脉冲复用 Heartbeat.vue；peer/local：静态点 -->
          <Heartbeat
            v-if="node.runner.type === 'worker'"
            :beat="beatOf(node.runner)"
            :label="workerStatusText(node.runner)"
          />
          <span v-else class="static-dot" :class="dotClass(node.runner)" aria-hidden="true"></span>
        </span>
        <span class="node-name mono" :title="node.runner.name">{{ node.runner.name }}</span>
        <span class="node-meta mono">
          <template v-if="node.runner.type === 'worker'">
            {{ node.runner.worker?.in_flight ?? 0 }} ·
            {{ node.runner.status === 'connected' ? fmtAge(workerAgeMs(node.runner)) : 'offline' }}
          </template>
          <template v-else-if="node.runner.type === 'peer-http'">
            {{ node.runner.probe && node.runner.probe.checked_at > 0
              ? node.runner.probe.latency_ms + 'ms'
              : peerStatusText(node.runner) }}
          </template>
          <template v-else>in-process</template>
        </span>
      </button>

      <div v-if="loaded && topoNodes.length === 0" class="stage-empty mono">
        无运行器：仅 server hub 在线。
      </div>
    </div>

    <!-- 图例 -->
    <div class="legend mono" aria-hidden="true">
      <span class="lg"><i class="sw" style="background: var(--done)"></i>up / connected</span>
      <span class="lg"><i class="sw" style="background: var(--run)"></i>stale</span>
      <span class="lg"><i class="sw" style="background: var(--fail)"></i>down / offline</span>
      <span class="lg"><i class="sw" style="background: var(--queue)"></i>unknown</span>
    </div>

    <!-- 节点面板（右侧抽屉） -->
    <div v-if="selected" class="panel-scrim" aria-hidden="true" @click="closePanel"></div>
    <aside v-if="selected" class="panel" aria-label="节点详情">
      <header class="panel-head">
        <h2 class="panel-title mono">
          {{ isHubSelected ? 'server' : selectedRunner?.name }}
        </h2>
        <button type="button" class="panel-close" aria-label="关闭" @click="closePanel">×</button>
      </header>

      <!-- hub / server -->
      <div v-if="isHubSelected" class="panel-body">
        <span class="kind mono kind--hub">SERVER · HUB</span>
        <dl class="kv mono">
          <dt>in-flight 合计</dt><dd>{{ inFlightTotal }}</dd>
          <dt>workers</dt><dd>{{ workerCount }}</dd>
          <dt>peers</dt><dd>{{ peerCount }}</dd>
          <dt>local</dt><dd>{{ localCount }}</dd>
          <dt>projects</dt><dd>{{ projects.length }}</dd>
        </dl>
      </div>

      <!-- worker -->
      <div v-else-if="selectedRunner?.type === 'worker'" class="panel-body">
        <span class="kind mono kind--worker">WORKER</span>
        <dl class="kv mono">
          <dt>worker id</dt><dd class="brk">{{ selectedRunner.worker_id || '—' }}</dd>
          <dt>状态</dt>
          <dd :style="{ color: nodeColorVar(selectedRunner) }">{{ workerStatusText(selectedRunner) }}</dd>
          <dt>心跳</dt>
          <dd>{{ selectedRunner.status === 'connected' ? fmtAge(workerAgeMs(selectedRunner)) : 'offline' }}</dd>
          <dt>in-flight</dt><dd>{{ selectedRunner.worker?.in_flight ?? 0 }}</dd>
          <template v-if="selectedRunner.worker?.hostname">
            <dt>主机</dt><dd class="brk">{{ selectedRunner.worker.hostname }}</dd>
          </template>
          <template v-if="selectedRunner.worker?.remote_addr">
            <dt>来源地址</dt><dd class="brk">{{ selectedRunner.worker.remote_addr }}</dd>
          </template>
          <template v-if="selectedRunner.worker?.os">
            <dt>系统</dt><dd>{{ selectedRunner.worker.os }}/{{ selectedRunner.worker.arch || '?' }}</dd>
          </template>
          <template v-if="selectedRunner.worker?.gofer_version">
            <dt>版本</dt><dd class="brk">{{ selectedRunner.worker.gofer_version }}</dd>
          </template>
          <template v-if="selectedRunner.worker?.started_at">
            <dt>运行时长</dt><dd>{{ fmtUptime(selectedRunner.worker.started_at) }}</dd>
          </template>
        </dl>
        <div v-if="selectedRunner.worker?.labels && selectedRunner.worker.labels.length" class="chips">
          <span v-for="l in selectedRunner.worker.labels" :key="l" class="chip mono">{{ l }}</span>
        </div>
        <p v-else class="muted mono">无 labels</p>
        <h3 v-if="selectedRunner.worker?.projects?.length" class="sub mono">projects</h3>
        <div v-if="selectedRunner.worker?.projects?.length" class="chips">
          <span v-for="p in selectedRunner.worker.projects" :key="p" class="chip mono">{{ p }}</span>
        </div>
        <h3 v-if="selectedRunner.worker?.agents?.length" class="sub mono">agents</h3>
        <div v-if="selectedRunner.worker?.agents?.length" class="chips">
          <span v-for="a in selectedRunner.worker.agents" :key="a" class="chip mono">{{ a }}</span>
        </div>
      </div>

      <!-- peer-http -->
      <div v-else-if="selectedRunner?.type === 'peer-http'" class="panel-body">
        <span class="kind mono kind--peer">PEER</span>
        <dl class="kv mono">
          <dt>base url</dt><dd class="brk">{{ selectedRunner.base_url || '—' }}</dd>
          <dt>状态</dt>
          <dd :style="{ color: nodeColorVar(selectedRunner) }">{{ peerStatusText(selectedRunner) }}</dd>
          <template v-if="selectedRunner.probe && selectedRunner.probe.checked_at > 0">
            <dt>latency</dt><dd>{{ selectedRunner.probe.latency_ms }}ms</dd>
            <dt>探活</dt><dd>{{ fmtAge(probeAgeMs(selectedRunner)) }}</dd>
          </template>
        </dl>
        <p v-if="selectedRunner.status === 'down' && selectedRunner.probe?.error" class="probe-err mono">
          {{ selectedRunner.probe.error }}
        </p>
      </div>

      <!-- local -->
      <div v-else-if="selectedRunner?.type === 'local'" class="panel-body">
        <span class="kind mono kind--local">LOCAL</span>
        <dl class="kv mono">
          <dt>执行</dt><dd>本机 in-process</dd>
          <dt>状态</dt><dd style="color: var(--done)">up</dd>
        </dl>
        <h3 class="sub mono">server projects（{{ projects.length }}）</h3>
        <ul v-if="projects.length" class="proj-list mono">
          <li v-for="p in projects" :key="p" :title="p">{{ p }}</li>
        </ul>
        <p v-else class="muted mono">无项目</p>
      </div>
    </aside>
  </div>
</template>

<style scoped>
.cluster {
  max-width: 880px;
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

/* 拓扑舞台：固定宽高比，节点用百分比坐标绝对定位，边铺满同坐标系 */
.stage {
  position: relative;
  width: 100%;
  aspect-ratio: 1.3 / 1;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  overflow: hidden;
}

.edges {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  pointer-events: none;
}
.edges line {
  opacity: 0.4;
}
.edges line.edge--active {
  opacity: 0.95;
}

/* 节点（hub + 辐射）：以坐标点为中心 */
.node {
  position: absolute;
  transform: translate(-50%, -50%);
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 3px;
  width: 118px;
  padding: 8px 6px;
  background: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  cursor: pointer;
  text-align: center;
}
.node:hover {
  border-color: var(--node-color, var(--phosphor));
}
.node--sel {
  border-color: var(--node-color, var(--phosphor));
  box-shadow: 0 0 0 1px var(--node-color, var(--phosphor));
}

.node--hub {
  width: 132px;
  background: var(--panel);
  border-color: var(--phosphor);
}
.node--hub:hover,
.node--hub.node--sel {
  border-color: var(--phosphor);
}
.node--hub.node--sel {
  box-shadow: 0 0 0 1px var(--phosphor);
}
.hub-core {
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: var(--phosphor);
  box-shadow: 0 0 0 4px color-mix(in srgb, var(--phosphor) 22%, transparent);
}

.node-ind {
  height: 18px;
  display: flex;
  align-items: center;
  justify-content: center;
}
.static-dot {
  width: 9px;
  height: 9px;
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

.node-name {
  font-size: 12px;
  color: var(--paper);
  font-weight: 600;
  max-width: 104px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.node-meta {
  font-size: 10px;
  color: var(--queue);
  max-width: 104px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.stage-empty {
  position: absolute;
  left: 50%;
  bottom: 16px;
  transform: translateX(-50%);
  color: var(--queue);
  font-size: 12px;
}

/* 图例 */
.legend {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  margin-top: 12px;
  font-size: 11px;
  color: var(--queue);
}
.lg {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.sw {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  display: inline-block;
}

/* 节点面板：右侧抽屉 + 遮罩 */
.panel-scrim {
  position: fixed;
  inset: 0;
  z-index: 20;
  background: rgba(0, 0, 0, 0.4);
}
.panel {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  z-index: 30;
  width: 320px;
  max-width: 88vw;
  background: var(--panel);
  border-left: 1px solid var(--line);
  padding: 16px;
  overflow-y: auto;
}
.panel-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding-bottom: 10px;
  margin-bottom: 12px;
  border-bottom: 1px solid var(--line);
}
.panel-title {
  font-size: 14px;
  color: var(--paper);
  margin: 0;
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.panel-close {
  flex: none;
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  width: 26px;
  height: 26px;
  font-size: 16px;
  line-height: 1;
  cursor: pointer;
}
.panel-close:hover {
  border-color: var(--phosphor);
  color: var(--phosphor);
}

.kind {
  display: inline-block;
  font-size: 10px;
  letter-spacing: 0.1em;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 1px 7px;
  margin-bottom: 12px;
}
.kind--hub {
  color: var(--phosphor);
}
.kind--worker {
  color: var(--done);
}
.kind--peer {
  color: var(--phosphor);
}
.kind--local {
  color: var(--queue);
}

.kv {
  display: grid;
  grid-template-columns: 84px 1fr;
  gap: 6px 10px;
  margin: 0 0 12px;
  font-size: 12px;
}
.kv dt {
  color: var(--queue);
}
.kv dd {
  color: var(--paper);
  margin: 0;
}
.kv dd.brk {
  word-break: break-all;
}

.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.chip {
  display: inline-block;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 1px 7px;
  color: var(--phosphor);
  font-size: 10px;
}

.sub {
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--queue);
  text-transform: uppercase;
  margin: 4px 0 8px;
}
.proj-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.proj-list li {
  font-size: 12px;
  color: var(--paper);
  padding: 4px 8px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.muted {
  color: var(--queue);
  font-size: 12px;
}
.probe-err {
  margin: 0;
  color: var(--fail);
  font-size: 11px;
  word-break: break-word;
}

@media (max-width: 768px) {
  .node {
    width: 100px;
  }
  .node--hub {
    width: 110px;
  }
  .node-name,
  .node-meta {
    max-width: 88px;
  }
}
</style>
