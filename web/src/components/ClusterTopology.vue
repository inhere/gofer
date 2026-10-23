<script setup lang="ts">
// Cluster topology rendered inside the Runners page.
//  - 不自己轮询：runners / projects / nowMs 由 Runners 页（listRunners + listProjects，4s）经 props 传入。
//  - SVG 手绘星型（D1，不引重图库）：中心 hub=server；辐射节点均匀分布在圆周（三角函数算坐标）。
//    边用 SVG <line>（viewBox 0..100 + preserveAspectRatio=none + non-scaling-stroke）；
//    节点为绝对定位 HTML（left/top 同百分比对齐边端点），故能直接复用 Heartbeat.vue 而无 foreignObject 命名空间坑。
//  - 点击节点 → 右侧抽屉面板：worker(id/心跳/in_flight/labels/状态) / peer(base_url/latency/error) / local(本机 + server projects 概览)。
//  - 不画「项目→节点」映射边（D2/§10.2：worker.yaml 独立、server 不知 worker 的 projects）。
import { runnerLabelNote } from '../utils/runnerDisplay'
import { computed, ref } from 'vue'
import { X } from '@lucide/vue'
import Icon from '../ui/Icon.vue'
import Heartbeat from '../components/Heartbeat.vue'
import type { Runner } from '../api/types'
import { t } from '../i18n'
import { beatOf, fmtAge, fmtUptime, workerAgeMs, workerStatusText } from '../utils/runners'

const props = defineProps<{ runners: Runner[]; projects: string[]; nowMs: number }>()

// 星型几何（百分比坐标，相对 stage）：容器宽高比 AR；为让节点摆成视觉圆形，水平半径 = 垂直半径 / AR。
const AR = 1.3
const RY = 36
const RX = RY / AR

const runners = computed(() => props.runners)
const projects = computed(() => props.projects)
const nowMs = computed(() => props.nowMs)
const loaded = computed(() => true)

// 当前选中节点：Runner（某辐射节点）/ 'hub'（中心 server）/ null（未选）。
const selected = ref<Runner | 'hub' | null>(null)


// 节点排序：worker（主角）→ peer。
// local（server 进程内 runner）不画成辐射节点：它和中心 hub 是同一个 server，
// 画出来会出现两个「server」节点（gofer-hez1）；它的信息并进 hub 节点与 hub 面板。
const orderedRunners = computed(() => {
  const w = runners.value.filter((r) => r.type === 'worker')
  const p = runners.value.filter((r) => r.type === 'peer-http')
  return [...w, ...p]
})

// hub 节点里展示的本机 runner（GET /v1/runners 的 local 行）。
const localRunner = computed<Runner | null>(() => runners.value.find((r) => r.type === 'local') ?? null)

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

// 节点强调色 token：worker 随心跳态（connected=done / stale=run / flatline=fail），
// peer 随探活（up=done / down=fail / unknown=queue），local 恒 up=done。
function nodeColorVar(r: Runner): string {
  if (r.type === 'worker') {
    const beat = beatOf(r, nowMs.value)
    if (beat === 'connected') {
      return 'var(--done)'
    }
    return beat === 'stale' ? 'var(--run)' : 'var(--fail)'
  }
  if (r.type === 'peer-http') {
    if (r.status === 'up') {
      return 'var(--done)'
    }
    return r.status === 'down' ? 'var(--fail)' : 'var(--ink-3)'
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

      <!-- 中心 hub = server（含本机 in-process runner；local 不再单独画成节点） -->
      <button
        type="button"
        class="node node--hub"
        :class="{ 'node--sel': isHubSelected }"
        style="left: 50%; top: 50%"
        data-test="topo-hub"
        @click="selectHub"
      >
        <span class="hub-core" aria-hidden="true"></span>
        <span class="node-name mono">server</span>
        <span class="node-meta mono" data-test="topo-hub-meta">{{ inFlightTotal }} in-flight</span>
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
            :beat="beatOf(node.runner, nowMs)"
            :label="workerStatusText(node.runner, nowMs)"
          />
          <span v-else class="static-dot" :class="dotClass(node.runner)" aria-hidden="true"></span>
        </span>
        <span class="node-name mono" :title="node.runner.name">{{ runnerLabelNote(node.runner.name) }}</span>
        <span class="node-meta mono">
          <template v-if="node.runner.type === 'worker'">
            {{ node.runner.worker?.in_flight ?? 0 }} ·
            {{ node.runner.status === 'connected' ? fmtAge(workerAgeMs(node.runner, nowMs)) : 'offline' }}
          </template>
          <template v-else-if="node.runner.type === 'peer-http'">
            {{ node.runner.probe && node.runner.probe.checked_at > 0
              ? node.runner.probe.latency_ms + 'ms'
              : peerStatusText(node.runner) }}
          </template>
        </span>
      </button>

      <div v-if="loaded && topoNodes.length === 0" class="stage-empty mono">
        {{ t('runners.topo.empty') }}
      </div>
    </div>

    <!-- 图例 -->
    <div class="legend mono" aria-hidden="true">
      <span class="lg"><i class="sw" style="background: var(--done)"></i>up / connected</span>
      <span class="lg"><i class="sw" style="background: var(--run)"></i>stale</span>
      <span class="lg"><i class="sw" style="background: var(--fail)"></i>down / offline</span>
      <span class="lg"><i class="sw" style="background: var(--ink-3)"></i>unknown</span>
    </div>

    <!-- 节点面板（右侧抽屉） -->
    <div v-if="selected" class="panel-scrim" aria-hidden="true" @click="closePanel"></div>
    <aside v-if="selected" class="panel" :aria-label="t('runners.topo.detail')">
      <header class="panel-head">
        <h2 class="panel-title mono">
          {{ isHubSelected ? 'server' : runnerLabelNote(selectedRunner?.name) }}
        </h2>
        <button type="button" class="panel-close" :aria-label="t('runners.topo.close')" @click="closePanel"><Icon :icon="X" size="sm" /></button>
      </header>

      <!-- hub / server -->
      <div v-if="isHubSelected" class="panel-body">
        <span class="kind mono kind--hub">SERVER · HUB</span>
        <dl class="kv mono">
          <dt>{{ t('runners.topo.inFlightTotal') }}</dt><dd>{{ inFlightTotal }}</dd>
          <dt>workers</dt><dd>{{ workerCount }}</dd>
          <dt>peers</dt><dd>{{ peerCount }}</dd>
          <dt>projects</dt><dd>{{ projects.length }}</dd>
        </dl>
        <!-- 本机 runner（server 进程内执行）：曾画成另一个 server 节点，现并在这里 -->
        <h3 class="sub mono">{{ t('runners.topo.localRunner') }}</h3>
        <dl class="kv mono" data-test="topo-hub-local">
          <dt>{{ t('runners.topo.runOn') }}</dt><dd>{{ t('runners.topo.runInProcess') }}</dd>
          <dt>{{ t('runners.topo.status') }}</dt><dd style="color: var(--done)">{{ localRunner ? 'up' : '—' }}</dd>
        </dl>
        <h3 class="sub mono">{{ t('runners.topo.serverProjects', { n: projects.length }) }}</h3>
        <ul v-if="projects.length" class="proj-list mono">
          <li v-for="p in projects" :key="p" :title="p">{{ p }}</li>
        </ul>
        <p v-else class="muted mono">{{ t('runners.topo.noProjects') }}</p>
      </div>

      <!-- worker -->
      <div v-else-if="selectedRunner?.type === 'worker'" class="panel-body">
        <span class="kind mono kind--worker">WORKER</span>
        <dl class="kv mono">
          <dt>worker id</dt><dd class="brk">{{ selectedRunner.worker_id || '—' }}</dd>
          <dt>{{ t('runners.topo.status') }}</dt>
          <dd :style="{ color: nodeColorVar(selectedRunner) }">{{ workerStatusText(selectedRunner, nowMs) }}</dd>
          <dt>{{ t('runners.topo.heartbeat') }}</dt>
          <dd>{{ selectedRunner.status === 'connected' ? fmtAge(workerAgeMs(selectedRunner, nowMs)) : 'offline' }}</dd>
          <dt>in-flight</dt><dd>{{ selectedRunner.worker?.in_flight ?? 0 }}</dd>
          <template v-if="selectedRunner.worker?.hostname">
            <dt>{{ t('runners.topo.host') }}</dt><dd class="brk">{{ selectedRunner.worker.hostname }}</dd>
          </template>
          <template v-if="selectedRunner.worker?.remote_addr">
            <dt>{{ t('runners.topo.remoteAddr') }}</dt><dd class="brk">{{ selectedRunner.worker.remote_addr }}</dd>
          </template>
          <template v-if="selectedRunner.worker?.os">
            <dt>{{ t('runners.topo.os') }}</dt><dd>{{ selectedRunner.worker.os }}/{{ selectedRunner.worker.arch || '?' }}</dd>
          </template>
          <template v-if="selectedRunner.worker?.gofer_version">
            <dt>{{ t('runners.topo.version') }}</dt><dd class="brk">{{ selectedRunner.worker.gofer_version }}</dd>
          </template>
          <template v-if="selectedRunner.worker?.started_at">
            <dt>{{ t('runners.topo.uptime') }}</dt><dd>{{ fmtUptime(selectedRunner.worker.started_at, nowMs) }}</dd>
          </template>
          <template v-if="selectedRunner.worker?.protocol_version">
            <dt>{{ t('runners.topo.protocol') }}</dt><dd>{{ selectedRunner.worker.protocol_version }}</dd>
          </template>
          <template v-if="selectedRunner.worker?.policy_rev || selectedRunner.worker?.applied_rev">
            <dt>{{ t('runners.topo.policy') }}</dt><dd>rev {{ selectedRunner.worker.policy_rev ?? 0 }} · applied {{ selectedRunner.worker.applied_rev ?? 0 }}<span v-if="selectedRunner.worker.policy_pending"> · {{ t('runners.topo.policyPending') }}</span></dd>
          </template>
        </dl>
        <div v-if="selectedRunner.worker?.policy_rejected?.length || selectedRunner.worker?.policy_degraded?.length || selectedRunner.worker?.policy_pending" class="policy-alert mono">
          <strong>{{ t('runners.topo.policyAlert') }}</strong>
          <span v-if="selectedRunner.worker?.policy_pending">{{ t('runners.topo.policyPendingNote') }}</span>
          <span v-for="item in selectedRunner.worker?.policy_rejected ?? []" :key="`reject-${item.key}-${item.reason}`">{{ t('runners.topo.policyRejected', { key: item.key, reason: item.reason }) }}</span>
          <span v-for="item in selectedRunner.worker?.policy_degraded ?? []" :key="`degrade-${item.key}-${item.gate}`">{{ t('runners.topo.policyDegraded', { key: item.key, gate: item.gate }) }}</span>
        </div>
        <div v-if="selectedRunner.worker?.labels && selectedRunner.worker.labels.length" class="chips">
          <span v-for="l in selectedRunner.worker.labels" :key="l" class="chip mono">{{ l }}</span>
        </div>
        <p v-else class="muted mono">{{ t('runners.topo.noLabels') }}</p>
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
          <dt>{{ t('runners.topo.status') }}</dt>
          <dd :style="{ color: nodeColorVar(selectedRunner) }">{{ peerStatusText(selectedRunner) }}</dd>
          <template v-if="selectedRunner.probe && selectedRunner.probe.checked_at > 0">
            <dt>latency</dt><dd>{{ selectedRunner.probe.latency_ms }}ms</dd>
            <dt>{{ t('runners.topo.probe') }}</dt><dd>{{ fmtAge(probeAgeMs(selectedRunner)) }}</dd>
          </template>
        </dl>
        <p v-if="selectedRunner.status === 'down' && selectedRunner.probe?.error" class="probe-err mono">
          {{ selectedRunner.probe.error }}
        </p>
      </div>

      <!-- local（server 进程内 runner）：不再单独成节点，内容已并入上面的 hub 面板 -->
    </aside>
  </div>
</template>

<style scoped>
.cluster {
  max-width: 880px;
  margin: 0 auto;
}


.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--r-chip);
  padding: 8px 10px;
  margin: 0 0 14px;
  word-break: break-word;
}
.policy-alert {
  display: grid;
  gap: 4px;
  margin: 10px 0;
  padding: 8px 10px;
  color: var(--fail);
  border: 1px solid color-mix(in srgb, var(--fail) 55%, var(--line));
  border-radius: var(--r-chip);
}
.policy-alert strong { color: var(--fail); }

/* 拓扑舞台：固定宽高比，节点用百分比坐标绝对定位，边铺满同坐标系 */
.stage {
  position: relative;
  width: 100%;
  aspect-ratio: 1.3 / 1;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  background: var(--surface);
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
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  cursor: pointer;
  text-align: center;
}
.node:hover {
  border-color: var(--node-color, var(--accent));
}
.node--sel {
  border-color: var(--node-color, var(--accent));
  box-shadow: 0 0 0 1px var(--node-color, var(--accent));
}

.node--hub {
  width: 132px;
  background: var(--surface);
  border-color: var(--accent);
}
.node--hub:hover,
.node--hub.node--sel {
  border-color: var(--accent);
}
.node--hub.node--sel {
  box-shadow: 0 0 0 1px var(--accent);
}
.hub-core {
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: var(--accent);
  box-shadow: 0 0 0 4px color-mix(in srgb, var(--accent) 22%, transparent);
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
  box-shadow: inset 0 0 0 1.5px var(--ink-3);
}

.node-name {
  font-size: 12px;
  color: var(--ink);
  font-weight: 600;
  max-width: 104px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.node-meta {
  font-size: 10px;
  color: var(--ink-3);
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
  color: var(--ink-3);
  font-size: 12px;
}

/* 图例 */
.legend {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  margin-top: 12px;
  font-size: 11px;
  color: var(--ink-3);
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
  background: var(--surface);
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
  color: var(--ink);
  margin: 0;
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.panel-close {
  flex: none;
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  width: 26px;
  height: 26px;
  font-size: 16px;
  line-height: 1;
  cursor: pointer;
}
.panel-close:hover {
  border-color: var(--accent);
  color: var(--accent);
}

.kind {
  display: inline-block;
  font-size: 10px;
  letter-spacing: 0.1em;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 1px 7px;
  margin-bottom: 12px;
}
.kind--hub {
  color: var(--accent);
}
.kind--worker {
  color: var(--done);
}
.kind--peer {
  color: var(--accent);
}
.kind--local {
  color: var(--ink-3);
}

.kv {
  display: grid;
  grid-template-columns: 84px 1fr;
  gap: 6px 10px;
  margin: 0 0 12px;
  font-size: 12px;
}
.kv dt {
  color: var(--ink-3);
}
.kv dd {
  color: var(--ink);
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
  border-radius: var(--r-chip);
  padding: 1px 7px;
  color: var(--accent);
  font-size: 10px;
}

.sub {
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--ink-3);
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
  color: var(--ink);
  padding: 4px 8px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  background: var(--bg);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.muted {
  color: var(--ink-3);
  font-size: 12px;
}
.probe-err {
  margin: 0;
  color: var(--fail);
  font-size: 11px;
  word-break: break-word;
}

@media (max-width: 760px) {
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
