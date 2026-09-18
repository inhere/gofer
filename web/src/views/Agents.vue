<script setup lang="ts">
// Agents：listAgents 展示 detect 状态，getConfig 展开只读 agent 关键配置。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { getConfig, listAgents, listPresence, probeAgent } from '../api/client'
import { fmtDateTime } from '../api/time'
import InteractionToast from '../components/InteractionToast.vue'
import type { AgentInfo, ConfigAgentView, Presence } from '../api/types'

const router = useRouter()

const agents = ref<AgentInfo[]>([])
const configAgents = ref<ConfigAgentView[]>([])
const expanded = ref<Set<string>>(new Set())
const loading = ref(false)
const error = ref('')
const presenceAgents = ref<Presence[]>([])
const presenceLoading = ref(false)
const roleFilter = ref('')
const projectFilter = ref('')
// 探针（SUP-01 P3）：正在探测的 agent key + 结果提示条（可跳承载它的 job）。
const probing = ref('')
const probeToast = ref<{ title: string; text: string; to?: string } | null>(null)

const PRESENCE_POLL_MS = 3000
const ONLINE_TTL_SEC = 30
let presenceTimer: number | null = null

const configByKey = computed(() => {
  const out = new Map<string, ConfigAgentView>()
  for (const a of configAgents.value) {
    out.set(a.key, a)
  }
  return out
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [agentsResp, configResp] = await Promise.all([listAgents(), getConfig()])
    agents.value = agentsResp.agents ?? []
    configAgents.value = configResp.agents ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
  void fetchPresence()
  startPresencePolling()
  document.addEventListener('visibilitychange', onPresenceVisibility)
})

onUnmounted(() => {
  stopPresencePolling()
  document.removeEventListener('visibilitychange', onPresenceVisibility)
})

watch([roleFilter, projectFilter], () => {
  void fetchPresence()
})

async function fetchPresence(): Promise<void> {
  presenceLoading.value = true
  try {
    const resp = await listPresence(
      roleFilter.value || undefined,
      projectFilter.value.trim() || undefined,
    )
    presenceAgents.value = resp.agents ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    presenceLoading.value = false
  }
}

function startPresencePolling(): void {
  stopPresencePolling()
  if (document.hidden) return
  presenceTimer = window.setInterval(() => void fetchPresence(), PRESENCE_POLL_MS)
}

function stopPresencePolling(): void {
  if (presenceTimer != null) {
    window.clearInterval(presenceTimer)
    presenceTimer = null
  }
}

function onPresenceVisibility(): void {
  if (document.hidden) {
    stopPresencePolling()
    return
  }
  void fetchPresence()
  startPresencePolling()
}

function isOnline(a: Presence): boolean {
  const age = Math.floor(Date.now() / 1000) - a.last_seen_at
  return a.status !== 'stale' && age <= ONLINE_TTL_SEC
}

function shortId(id: string): string {
  return id.length > 12 ? `...${id.slice(-12)}` : id
}

function relSeen(sec: number): string {
  const age = Math.max(0, Math.floor(Date.now() / 1000) - sec)
  return `${age} 秒前`
}

function openPresenceInbox(a: Presence): void {
  void router.push(`/agents/presence/${encodeURIComponent(a.agent_id)}`)
}

function toggleExpand(key: string): void {
  const next = new Set(expanded.value)
  if (next.has(key)) {
    next.delete(key)
  } else {
    next.add(key)
  }
  expanded.value = next
}

// healthState / healthTitle 渲染 /v1/agents 的 health 块（SUP-01 P3）：unknown 是
// "窗口内没有样本"，不是"健康"——一个从没跑过 job 的 agent 不该显示绿点。
function healthState(a: AgentInfo): string {
  return a.health?.state || 'unknown'
}

function windowLabel(sec: number): string {
  if (!sec || sec <= 0) {
    return '窗口'
  }
  return sec % 3600 === 0 ? `${sec / 3600}h` : `${Math.round(sec / 60)}m`
}

function healthTitle(a: AgentInfo): string {
  const h = a.health
  if (!h || h.state === 'unknown') {
    return `最近 ${h ? windowLabel(h.window_sec) : '窗口'}内没有该 agent 的 job，无法判断`
  }
  const base = `最近 ${windowLabel(h.window_sec)} ${h.transient_fail} 次供应商错误（job ${h.jobs} / 成功 ${h.ok}）`
  return h.state === 'degraded' ? base : `正常：${base}`
}

// runProbe 提交一次探针 job：结果提示条给出状态/耗时/首行，并可跳到那个 job；
// 探测本身就是一次 job，所以随后刷新一次列表把新的健康度带回来。
async function runProbe(key: string): Promise<void> {
  if (probing.value) {
    return
  }
  probing.value = key
  try {
    const res = await probeAgent(key)
    const secs = (res.duration_ms / 1000).toFixed(1)
    const line = res.first_line ? ` · ${res.first_line}` : ''
    probeToast.value = {
      title: `探针 ${key}`,
      text: `${res.status} · exit ${res.exit_code} · ${secs}s${line}`,
      to: `/jobs/${encodeURIComponent(res.job_id)}`,
    }
    await load()
  } catch (e) {
    probeToast.value = {
      title: `探针 ${key} 失败`,
      text: e instanceof Error ? e.message : String(e),
    }
  } finally {
    probing.value = ''
  }
}

function detailFor(key: string): ConfigAgentView | undefined {
  return configByKey.value.get(key)
}

function yesNo(v: boolean): string {
  return v ? '是' : '否'
}

function textValue(v?: string): string {
  return v && v.trim() ? v : '—'
}

function listValue(v?: string[]): string {
  return v && v.length ? v.join(' ') : '—'
}
</script>

<template>
  <div class="agents">
    <div class="head">
      <h1 class="title mono">AGENTS</h1>
      <span class="poll-hint mono" :class="{ 'poll-hint--on': loading }">●</span>
    </div>

    <p class="scope-note mono">
      以下为 <b>serve 主机</b>配置的 agents 及其可用性；worker 节点各自的 agents 见
      <RouterLink to="/runners">Runners</RouterLink>。
    </p>

    <p v-if="error" class="error mono">{{ error }}</p>

    <div class="table">
      <div class="thead mono">
        <span class="col-detect">detect</span>
        <span class="col-key">key</span>
        <span class="col-type">type</span>
        <span class="col-health">health</span>
        <span class="col-info">version / error</span>
      </div>

      <template v-for="a in agents" :key="a.key">
        <div class="trow">
          <span class="col-detect">
            <span
              class="detect-dot"
              :class="a.available ? 'detect-dot--on' : 'detect-dot--off'"
              :aria-label="a.available ? 'available' : 'unavailable'"
            ></span>
            <span class="detect-text mono" :class="a.available ? 'detect-text--on' : 'detect-text--off'">
              {{ a.available ? 'available' : 'unavailable' }}
            </span>
          </span>
          <span class="col-key mono">
            <button
              class="key-btn mono"
              type="button"
              :aria-expanded="expanded.has(a.key)"
              :aria-controls="`agent-detail-${a.key}`"
              :title="expanded.has(a.key) ? '收起配置' : '展开配置'"
              @click="toggleExpand(a.key)"
            >
              <span class="chev" aria-hidden="true">{{ expanded.has(a.key) ? '▾' : '▸' }}</span>
              <span class="key-text">{{ a.key }}</span>
            </button>
          </span>
          <span class="col-type mono">{{ a.type }}</span>
          <span class="col-health mono">
            <span class="health-badge" :class="`health-badge--${healthState(a)}`" :title="healthTitle(a)">
              {{ healthState(a) }}
            </span>
            <button
              class="probe-btn mono"
              type="button"
              :disabled="probing !== ''"
              title="提交一次探针 job（只回复一行 OK）"
              @click="runProbe(a.key)"
            >
              {{ probing === a.key ? '探测中…' : '探针' }}
            </button>
          </span>
          <span class="col-info mono">
            <span v-if="a.available" class="version">{{ a.version || '—' }}</span>
            <span v-else class="err-msg">{{ a.error || 'unavailable' }}</span>
          </span>
        </div>

        <div v-if="expanded.has(a.key)" :id="`agent-detail-${a.key}`" class="detail mono">
          <template v-if="detailFor(a.key)">
            <div class="detail-grid">
              <span class="dk">type</span><span class="dv">{{ detailFor(a.key)?.type || '—' }}</span>
              <span class="dk">interactive</span><span class="dv">{{ yesNo(detailFor(a.key)?.interactive ?? false) }}</span>
              <span class="dk">command</span><span class="dv">{{ textValue(detailFor(a.key)?.command) }}</span>
              <span class="dk">args</span><span class="dv">{{ listValue(detailFor(a.key)?.args) }}</span>
              <span class="dk">session_inject</span><span class="dv">{{ listValue(detailFor(a.key)?.session_inject) }}</span>
              <span class="dk">session_capture</span><span class="dv">{{ textValue(detailFor(a.key)?.session_capture) }}</span>
              <span class="dk">session_resume</span><span class="dv">{{ listValue(detailFor(a.key)?.session_resume) }}</span>
              <span class="dk">system_inject</span><span class="dv">{{ listValue(detailFor(a.key)?.system_inject) }}</span>
              <span class="dk">env_keys</span><span class="dv">{{ listValue(detailFor(a.key)?.env_keys) }}</span>
              <span class="dk">mcp_server_name</span><span class="dv">{{ textValue(detailFor(a.key)?.mcp_server_name) }}</span>
              <span class="dk">detect.command</span><span class="dv">{{ textValue(detailFor(a.key)?.detect.command) }}</span>
              <span class="dk">detect.args</span><span class="dv">{{ listValue(detailFor(a.key)?.detect.args) }}</span>
              <span class="dk">allow_raw_cmd</span><span class="dv">{{ yesNo(detailFor(a.key)?.allow_raw_cmd ?? false) }}</span>
            </div>
          </template>
          <span v-else class="no-detail">无配置详情</span>
        </div>
      </template>

      <div v-if="agents.length === 0 && !error && !loading" class="empty mono">
        暂无 agent
      </div>
      <div v-if="loading && agents.length === 0" class="empty mono">
        探测中…
      </div>
    </div>

    <section class="presence-section">
      <div class="presence-head">
        <div>
          <h2 class="section-title mono">在线 driver / presence</h2>
          <p class="section-note mono">已注册的 driver presence，可点击进入 inbox。</p>
        </div>
        <div class="presence-ctrls mono">
          <label class="filter"><span>role</span><select v-model="roleFilter" class="filter-select mono"><option value="">全部</option><option value="supervisor">supervisor</option></select></label>
          <label class="filter"><span>project</span><input v-model.trim="projectFilter" class="filter-input mono" type="text" placeholder="全部" /></label>
          <span class="poll-hint mono" :class="{ 'poll-hint--on': presenceLoading }">●</span>
        </div>
      </div>

      <div class="presence-table mono">
        <div class="presence-thead"><span>状态</span><span>name</span><span>role</span><span>project</span><span>client</span><span>agent_id</span><span>last_seen</span></div>
        <div
          v-for="a in presenceAgents"
          :key="a.agent_id"
          class="presence-row"
          role="button"
          tabindex="0"
          @click="openPresenceInbox(a)"
          @keydown.enter="openPresenceInbox(a)"
          @keydown.space.prevent="openPresenceInbox(a)"
        >
          <span class="pcol-state"><span class="presence-dot" :class="isOnline(a) ? 'presence-dot--on' : 'presence-dot--stale'" :title="isOnline(a) ? 'online' : 'stale'"></span></span>
          <span class="pcol-name presence-name" :title="a.name || a.agent_id">{{ a.name || shortId(a.agent_id) }}</span>
          <span class="pcol-role"><span v-if="a.role" class="presence-badge" :class="{ 'presence-badge--sup': a.role === 'supervisor' }">{{ a.role }}</span><span v-else class="presence-muted">—</span></span>
          <span class="pcol-project presence-muted" :title="a.project_key || ''">{{ a.project_key || '—' }}</span>
          <span class="pcol-client presence-muted" :title="a.client || ''">{{ a.client || '—' }}</span>
          <span class="pcol-agent presence-id" :title="a.agent_id">{{ shortId(a.agent_id) }}</span>
          <span class="pcol-seen presence-seen"><span>{{ fmtDateTime(a.last_seen_at) }}</span><small>{{ relSeen(a.last_seen_at) }}</small></span>
        </div>
        <div v-if="presenceAgents.length === 0" class="empty mono">暂无在线 driver</div>
      </div>
    </section>

    <InteractionToast
      v-if="probeToast"
      :title="probeToast.title"
      :text="probeToast.text"
      :to="probeToast.to"
      @close="probeToast = null"
      @goto="probeToast = null"
    />
  </div>
</template>

<style scoped>
.agents {
  max-width: 1160px;
  margin: 0 auto;
}
.head {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 14px;
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
}
.poll-hint--on {
  color: var(--phosphor);
}
.scope-note {
  color: var(--queue);
  font-size: 12px;
  margin: 0 0 12px;
}
.scope-note a {
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

.table {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.thead,
.trow {
  display: grid;
  grid-template-columns: 150px 160px 120px 210px 1fr;
  align-items: center;
  gap: 12px;
  padding: 9px 14px;
}
.thead {
  background: var(--panel);
  border-bottom: 1px solid var(--line);
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--queue);
  text-transform: uppercase;
}
.trow {
  border-bottom: 1px solid var(--line);
  font-size: 13px;
}
.trow:hover {
  background: var(--panel);
}

.col-detect {
  display: inline-flex;
  align-items: center;
  gap: 7px;
}
.detect-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex: none;
}
.detect-dot--on {
  background: var(--done);
}
.detect-dot--off {
  background: transparent;
  box-shadow: inset 0 0 0 1.5px var(--queue);
}
.detect-text {
  font-size: 11px;
  letter-spacing: 0.04em;
}
.detect-text--on {
  color: var(--done);
}
.detect-text--off {
  color: var(--queue);
}

.col-key {
  min-width: 0;
}
.key-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
  background: transparent;
  border: 0;
  color: var(--phosphor);
  cursor: pointer;
  font-size: 13px;
  padding: 0;
  overflow: hidden;
}
.key-btn:hover .key-text,
.key-btn:focus-visible .key-text {
  text-decoration: underline;
}
.key-btn:focus-visible {
  outline: 1px solid var(--phosphor);
  outline-offset: 2px;
  border-radius: 2px;
}
.chev {
  color: var(--queue);
  flex: none;
  font-size: 12px;
  line-height: 1;
}
.key-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-type {
  color: var(--paper);
}
.col-info {
  overflow: hidden;
  text-overflow: ellipsis;
}
/* 健康度徽标（SUP-01 P3）：绿=近期正常、橙=窗口内供应商错误达阈值、灰=无样本。 */
.col-health {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.health-badge {
  font-size: 11px;
  letter-spacing: 0.04em;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 1px 7px;
  color: var(--queue);
}
.health-badge--healthy {
  color: var(--done);
  border-color: var(--done);
}
/* degraded 用琥珀色：--run 是本主题唯一的橙位，且与同一行的 error 红（--fail）区分开，
   这样"供应商在挂"和"这个 CLI 没装/配置有错"不会读成同一种红。 */
.health-badge--degraded {
  color: var(--run);
  border-color: var(--run);
}
.health-badge--unknown {
  color: var(--queue);
}
.probe-btn {
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--phosphor);
  cursor: pointer;
  font-size: 11px;
  padding: 2px 8px;
}
.probe-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
}
.probe-btn:disabled {
  color: var(--queue);
  cursor: default;
}
.version {
  color: var(--queue);
  font-size: 12px;
}
.err-msg {
  color: var(--fail);
  font-size: 12px;
  word-break: break-word;
}

.detail {
  border-bottom: 1px solid var(--line);
  background: var(--ink);
  padding: 10px 14px 12px 176px;
  font-size: 12px;
}
.detail-grid {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 4px 12px;
  align-items: baseline;
}
.dk {
  color: var(--queue);
  font-size: 11px;
}
.dv {
  color: var(--paper);
  overflow-wrap: anywhere;
}
.no-detail {
  color: var(--queue);
}

.empty {
  padding: 28px 14px;
  text-align: center;
  color: var(--queue);
  font-size: 13px;
}

/* 下段：driver presence（原 Drivers.vue，样式加 presence- 前缀避免与上段表格撞名） */
.presence-section {
  margin-top: 26px;
}
.presence-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 14px;
  margin-bottom: 10px;
}
.section-title {
  font-size: 13px;
  letter-spacing: 0.04em;
  color: var(--paper);
  margin: 0;
}
.section-note {
  color: var(--queue);
  font-size: 12px;
  margin: 4px 0 0;
}
.presence-ctrls {
  display: flex;
  align-items: center;
  gap: 14px;
  font-size: 12px;
}
.filter {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--queue);
}
.filter-select,
.filter-input {
  background: var(--panel);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 8px;
  font-size: 12px;
  outline: none;
}
.filter-input {
  width: 150px;
}
.filter-select:focus,
.filter-input:focus {
  border-color: var(--phosphor);
}

.presence-table {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.presence-thead,
.presence-row {
  display: grid;
  grid-template-columns: 54px minmax(160px, 1fr) 124px 142px 130px 142px 168px;
  align-items: center;
  gap: 12px;
  padding: 9px 14px;
}
.presence-thead {
  background: var(--panel);
  border-bottom: 1px solid var(--line);
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--queue);
  text-transform: uppercase;
}
.presence-row {
  border-bottom: 1px solid var(--line);
  cursor: pointer;
  font-size: 13px;
  outline: none;
}
.presence-row:last-child {
  border-bottom: none;
}
.presence-row:hover {
  background: var(--panel);
}
.presence-row:focus-visible {
  background: var(--panel);
  box-shadow: inset 2px 0 0 var(--phosphor);
}
.pcol-state {
  display: flex;
  justify-content: center;
}
.presence-dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  flex: none;
}
.presence-dot--on {
  background: var(--done);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--done) 20%, transparent);
}
.presence-dot--stale {
  background: var(--queue);
  opacity: 0.65;
  box-shadow: 0 0 0 1px var(--line);
}
.presence-name {
  color: var(--paper);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.presence-badge {
  display: inline-block;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 2px 7px;
  font-size: 11px;
}
.presence-badge--sup {
  color: var(--run);
  border-color: var(--run);
}
.presence-muted {
  color: var(--queue);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.presence-id {
  color: var(--phosphor);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.presence-seen {
  display: flex;
  flex-direction: column;
  gap: 1px;
  font-size: 12px;
}
.presence-seen > span {
  color: var(--paper);
}
.presence-seen > small {
  color: var(--queue);
  font-size: 11px;
}

@media (max-width: 768px) {
  .thead,
  .trow {
    grid-template-columns: 120px 1fr;
    grid-auto-rows: auto;
    gap: 4px 10px;
  }
  .col-type,
  .col-health,
  .col-info {
    grid-column: 1 / -1;
  }
  .detail {
    padding-left: 14px;
  }
}

@media (max-width: 940px) {
  .presence-head {
    align-items: flex-start;
    flex-direction: column;
  }
  .presence-ctrls {
    flex-wrap: wrap;
    width: 100%;
  }
  .filter-input {
    width: 180px;
  }
  .presence-thead {
    display: none;
  }
  .presence-row {
    grid-template-columns: 24px 1fr 118px;
    grid-template-areas:
      'state name role'
      'state project agent'
      'state client seen';
    row-gap: 6px;
  }
  .pcol-state {
    grid-area: state;
    align-items: flex-start;
    padding-top: 4px;
  }
  .pcol-name {
    grid-area: name;
  }
  .pcol-role {
    grid-area: role;
  }
  .pcol-project {
    grid-area: project;
  }
  .pcol-client {
    grid-area: client;
  }
  .pcol-agent {
    grid-area: agent;
  }
  .pcol-seen {
    grid-area: seen;
  }
}
</style>
