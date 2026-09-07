<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  downloadPtyRecording,
  listAgentSessions,
  listRecentPtySessions,
  setSessionRelay,
} from '../api/client'
import { fmtAgo, fmtDuration } from '../api/time'
import type { AgentSession, AgentSessionState, PtySession } from '../api/types'
import SessionDrawer from '../components/SessionDrawer.vue'

const DEFAULT_LIMIT = 50
// Agent 会话列表轮询间隔（页面可见时）
const AGENT_POLL_MS = 4000

const route = useRoute()
const router = useRouter()

// ---------------- Agent 会话（会话中继 SESS-01） ----------------
const agentSessions = ref<AgentSession[]>([])
const agentLoading = ref(false)
const agentError = ref('')
const showEnded = ref(false)
const relayBusyIds = ref<Set<string>>(new Set())
const relayErrors = ref<Map<string, string>>(new Map())
// 当前打开详情抽屉的会话 id（与 query ?sid= 同步）
const openSid = ref<string>(typeof route.query.sid === 'string' ? route.query.sid : '')

let agentTimer: number | null = null

const hasAgentSessions = computed(() => agentSessions.value.length > 0)
const waitingCount = computed(
  () => agentSessions.value.filter((s) => s.state === 'waiting_reply').length,
)

const AGENT_STATE_LABELS: Record<AgentSessionState, string> = {
  running: '执行中',
  idle: '空闲',
  waiting_reply: '等待回复',
  needs_attention: '需注意',
  ended: '已结束',
}

function agentStateLabel(s: AgentSessionState): string {
  return AGENT_STATE_LABELS[s] ?? s
}

function agentTitle(s: AgentSession): string {
  return s.title || `${s.agent} · ${s.session_id.slice(0, 8)}`
}

async function loadAgentSessions(opts?: { silent?: boolean }): Promise<void> {
  if (!opts?.silent) {
    agentLoading.value = true
  }
  nowSec.value = Math.floor(Date.now() / 1000)
  try {
    const resp = await listAgentSessions({ all: showEnded.value, limit: 100 })
    agentSessions.value = resp.sessions ?? []
    agentError.value = ''
  } catch (e) {
    agentError.value = e instanceof Error ? e.message : String(e)
  } finally {
    agentLoading.value = false
  }
}

function startAgentPolling(): void {
  stopAgentPolling()
  // 仅页面可见时轮询
  if (document.hidden) {
    return
  }
  agentTimer = window.setInterval(() => {
    void loadAgentSessions({ silent: true })
  }, AGENT_POLL_MS)
}

function stopAgentPolling(): void {
  if (agentTimer != null) {
    window.clearInterval(agentTimer)
    agentTimer = null
  }
}

function onVisibility(): void {
  if (document.hidden) {
    stopAgentPolling()
  } else {
    void loadAgentSessions({ silent: true })
    startAgentPolling()
  }
}

// 中继开关（行内）：乐观更新，失败回滚并在行内提示。
async function onToggleRelay(s: AgentSession): Promise<void> {
  if (relayBusyIds.value.has(s.session_id)) {
    return
  }
  const prev = s.relay
  const next = !prev
  relayBusyIds.value = new Set(relayBusyIds.value).add(s.session_id)
  const errs = new Map(relayErrors.value)
  errs.delete(s.session_id)
  relayErrors.value = errs
  s.relay = next
  try {
    const updated = await setSessionRelay(s.session_id, next)
    agentSessions.value = agentSessions.value.map((it) =>
      it.session_id === updated.session_id ? updated : it,
    )
  } catch (e) {
    const cur = agentSessions.value.find((it) => it.session_id === s.session_id)
    if (cur) {
      cur.relay = prev
    }
    relayErrors.value = new Map(relayErrors.value).set(
      s.session_id,
      e instanceof Error ? e.message : String(e),
    )
  } finally {
    const busy = new Set(relayBusyIds.value)
    busy.delete(s.session_id)
    relayBusyIds.value = busy
  }
}

function openDrawer(sid: string): void {
  openSid.value = sid
  if (route.query.sid !== sid) {
    void router.replace({ query: { ...route.query, sid } })
  }
}

function closeDrawer(): void {
  openSid.value = ''
  if (route.query.sid) {
    const q = { ...route.query }
    delete q.sid
    void router.replace({ query: q })
  }
}

function onSessionDeleted(sid: string): void {
  agentSessions.value = agentSessions.value.filter((s) => s.session_id !== sid)
}

// 铃铛 / 外链跳到 /sessions?sid=xxx 时自动打开抽屉
watch(
  () => route.query.sid,
  (v) => {
    openSid.value = typeof v === 'string' ? v : ''
  },
)

watch(showEnded, () => {
  void loadAgentSessions()
})

// ---------------- pty 会话（既有） ----------------

const sessions = ref<PtySession[]>([])
const loading = ref(false)
const error = ref('')
const nowSec = ref(Math.floor(Date.now() / 1000))
const downloadingRecordingIds = ref<Set<string>>(new Set())

const hasSessions = computed(() => sessions.value.length > 0)

function fmtTime(v: number | undefined): string {
  if (!v) {
    return '—'
  }
  return new Date(v * 1000).toLocaleString()
}

function duration(s: PtySession): string {
  const end = s.ended_at && s.ended_at > 0 ? s.ended_at : nowSec.value
  return fmtDuration(Math.max(0, end - s.started_at))
}

function bytesText(s: PtySession): string {
  return `${s.bytes_in} / ${s.bytes_out} B`
}

function shortId(id?: string): string {
  if (!id) {
    return '—'
  }
  return id.length > 10 ? id.slice(-10) : id
}

function shortSessionID(id?: string): string {
  if (!id) {
    return '—'
  }
  return id.length > 12 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id
}

function canAttachSession(s: PtySession): boolean {
  return !!s.job_id && (s.state === 'open' || s.state === 'attached') && !s.ended_at
}

async function onDownloadRecording(s: PtySession): Promise<void> {
  if (!s.job_id || !s.has_recording || downloadingRecordingIds.value.has(s.pty_session_id)) {
    return
  }
  downloadingRecordingIds.value = new Set(downloadingRecordingIds.value).add(s.pty_session_id)
  try {
    await downloadPtyRecording(s.job_id)
  } finally {
    const next = new Set(downloadingRecordingIds.value)
    next.delete(s.pty_session_id)
    downloadingRecordingIds.value = next
  }
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  nowSec.value = Math.floor(Date.now() / 1000)
  try {
    const resp = await listRecentPtySessions(DEFAULT_LIMIT)
    sessions.value = resp.sessions ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
  void loadAgentSessions()
  startAgentPolling()
  document.addEventListener('visibilitychange', onVisibility)
})

onUnmounted(() => {
  stopAgentPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <div class="board">
    <header class="board-head">
      <h1 class="title mono">SESSIONS</h1>
    </header>

    <!-- Agent 会话（会话中继 SESS-01）：hook 登记的 claude/codex 会话，点击行开详情抽屉 -->
    <section class="group">
      <header class="group-head">
        <h2 class="group-title mono">
          AGENT 会话
          <span v-if="waitingCount > 0" class="group-count group-count--hot mono">{{ waitingCount }} 等待回复</span>
          <span v-else-if="hasAgentSessions" class="group-count mono">{{ agentSessions.length }}</span>
        </h2>
        <div class="controls mono">
          <label class="check">
            <input v-model="showEnded" type="checkbox" />
            显示已结束
          </label>
          <button
            class="act mono"
            type="button"
            :disabled="agentLoading"
            @click="loadAgentSessions()"
          >
            {{ agentLoading ? '刷新中…' : '刷新' }}
          </button>
        </div>
      </header>

      <p v-if="agentError" class="error mono">{{ agentError }}</p>

      <div v-if="hasAgentSessions" class="table">
        <div class="thead thead--agent mono">
          <span class="a-title">标题</span>
          <span class="a-agent">Agent</span>
          <span class="a-project">Project</span>
          <span class="a-runner">Runner</span>
          <span class="a-state">状态</span>
          <span class="a-relay">中继</span>
          <span class="a-seen">最后活动</span>
          <span class="a-turns">Turns</span>
        </div>
        <article
          v-for="s in agentSessions"
          :key="s.session_id"
          class="trow trow--agent"
          :class="{
            'trow--waiting': s.state === 'waiting_reply',
            'trow--attn': s.state === 'needs_attention',
            'trow--ended': s.state === 'ended',
            'trow--active': openSid === s.session_id,
          }"
          role="button"
          tabindex="0"
          @click="openDrawer(s.session_id)"
          @keydown.enter="openDrawer(s.session_id)"
        >
          <span class="a-title" :title="`${agentTitle(s)}\n${s.session_id}`">
            <span class="a-title-text">{{ agentTitle(s) }}</span>
            <span v-if="s.last_message" class="a-last mono">{{ s.last_message }}</span>
          </span>
          <span class="a-agent mono">{{ s.agent }}</span>
          <span class="a-project mono" :title="s.project_key">{{ s.project_key || '—' }}</span>
          <span class="a-runner mono" :title="s.runner">{{ s.runner || '—' }}</span>
          <span class="a-state">
            <span class="state-badge mono" :class="`state--${s.state}`">{{ agentStateLabel(s.state) }}</span>
          </span>
          <span class="a-relay" @click.stop>
            <label
              class="relay-toggle mono"
              :class="{ on: s.relay, busy: relayBusyIds.has(s.session_id) }"
              :title="relayErrors.get(s.session_id) ? `切换失败：${relayErrors.get(s.session_id)}` : (s.relay ? '中继开启：会话停下时在此等你回复' : '中继关闭：拨开后下一次停下生效')"
            >
              <input
                type="checkbox"
                :checked="s.relay"
                :disabled="relayBusyIds.has(s.session_id) || s.state === 'ended'"
                @change="onToggleRelay(s)"
              />
              <span class="relay-track"><span class="relay-knob"></span></span>
              <span class="relay-text">{{ s.relay ? 'ON' : 'OFF' }}</span>
            </label>
            <span v-if="relayErrors.get(s.session_id)" class="relay-err mono">!</span>
          </span>
          <span class="a-seen mono" :title="fmtTime(s.last_seen_at)">{{ fmtAgo(s.last_seen_at, nowSec) }}</span>
          <span class="a-turns mono">{{ s.turn_no }}</span>
        </article>
      </div>

      <div v-else-if="!agentLoading && !agentError" class="empty mono">
        暂无 agent 会话。执行 <code>gofer init hooks</code> 装配后，agent 会话会自动登记到这里。
      </div>
    </section>

    <section class="group">
      <header class="group-head">
        <h2 class="group-title mono">
          终端会话
          <span v-if="hasSessions" class="group-count mono">{{ sessions.length }}</span>
        </h2>
        <div class="controls mono">
          <RouterLink class="act act--primary mono" to="/new?mode=session">
            新建会话
          </RouterLink>
          <button class="act mono" type="button" :disabled="loading" @click="load()">
            {{ loading ? '刷新中…' : '刷新' }}
          </button>
        </div>
      </header>

    <p v-if="error" class="error mono">{{ error }}</p>

    <div v-if="hasSessions" class="table">
      <div class="thead mono">
        <span class="job-link">Job</span>
        <span class="size">尺寸</span>
        <span class="bytes">流量(输入/输出)</span>
        <span class="session-id">Session ID</span>
        <span class="duration">时长</span>
        <span class="state">状态</span>
        <span class="flag flag--encrypted">加密</span>
        <span class="flag flag--recording">录制</span>
        <span class="session-action session-action--recording">录制文件</span>
        <span class="session-action session-action--terminal">终端</span>
        <span class="started">开始时间</span>
      </div>
      <article
        v-for="s in sessions"
        :key="s.pty_session_id"
        class="trow"
      >
        <RouterLink
          v-if="s.job_id"
          class="job-link mono"
          :to="`/jobs/${encodeURIComponent(s.job_id)}`"
          :title="s.job_id"
        >
          {{ shortId(s.job_id) }}
        </RouterLink>
        <span v-else class="job-link job-link--empty mono">—</span>

        <span class="size mono">{{ s.cols }}×{{ s.rows }}</span>
        <span class="bytes mono">{{ bytesText(s) }}</span>
        <span class="session-id mono" :title="s.session_id || ''">{{ shortSessionID(s.session_id) }}</span>
        <span class="duration mono">{{ duration(s) }}</span>
        <span class="state mono">{{ s.state }}</span>
        <span class="flag flag--encrypted mono" :class="{ on: s.encrypted }">
          {{ s.encrypted ? '加密' : '明文' }}
        </span>
        <span class="flag flag--recording mono" :class="{ on: s.has_recording }">
          {{ s.has_recording ? '已录制' : '无录制' }}
        </span>
        <button
          v-if="s.has_recording"
          class="session-action session-action--recording mono"
          type="button"
          :disabled="downloadingRecordingIds.has(s.pty_session_id)"
          @click="onDownloadRecording(s)"
        >
          {{ downloadingRecordingIds.has(s.pty_session_id) ? '下载中' : '下载录制' }}
        </button>
        <span v-else class="session-action session-action--recording session-action--empty mono">—</span>
        <RouterLink
          v-if="canAttachSession(s)"
          class="session-action session-action--terminal mono"
          :to="`/jobs/${encodeURIComponent(s.job_id ?? '')}?attach=1`"
        >
          打开终端
        </RouterLink>
        <span v-else class="session-action session-action--terminal session-action--empty mono">—</span>
        <span class="started mono">{{ fmtTime(s.started_at) }}</span>
      </article>
    </div>

    <div v-else-if="!loading && !error" class="empty mono">
      暂无终端会话
    </div>
    <p v-if="hasSessions" class="sessions-note mono">
      输入/输出字节是 relay 计数；只有已录制的会话会保留可下载回放。
    </p>
    </section>

    <SessionDrawer
      v-if="openSid"
      :sid="openSid"
      @close="closeDrawer"
      @changed="loadAgentSessions({ silent: true })"
      @deleted="onSessionDeleted"
    />
  </div>
</template>

<style scoped>
.board {
  max-width: 1160px;
  margin: 0 auto;
}
.board-head {
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
.controls {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--queue);
  font-size: 12px;
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
  grid-template-columns:
    minmax(92px, 0.9fr)
    72px
    minmax(118px, 1fr)
    minmax(108px, 0.9fr)
    84px
    86px
    64px
    72px
    78px
    76px
    minmax(150px, 1fr);
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
.thead .job-link,
.thead .size,
.thead .bytes,
.thead .duration,
.thead .state,
.thead .started,
.thead .session-id,
.thead .flag,
.thead .session-action {
  color: var(--queue);
  border-color: transparent;
  padding: 0;
}
.thead .session-action {
  background: transparent;
  text-align: left;
}
.trow {
  border-bottom: 1px solid var(--line);
  font-size: 13px;
  outline: none;
}
.trow:last-child {
  border-bottom: none;
}
.trow:hover {
  background: var(--panel);
}
.job-link {
  min-width: 0;
  color: var(--phosphor);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.job-link:hover {
  color: var(--paper);
  text-decoration: none;
}
.job-link--empty {
  color: var(--queue);
}
.size {
  color: var(--paper);
}
.bytes {
  color: var(--paper);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.duration,
.state,
.started,
.session-id {
  color: var(--queue);
}
.session-id {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.flag {
  justify-self: start;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: 3px;
  padding: 1px 6px;
  font-size: 11px;
}
.flag.on {
  color: var(--run);
  border-color: var(--run);
}
.session-action {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 8px;
  font-size: 11px;
  text-align: center;
  text-decoration: none;
  white-space: nowrap;
  cursor: pointer;
}
.session-action:hover:not(:disabled) {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.session-action:disabled {
  cursor: default;
  opacity: 0.55;
}
.session-action--empty {
  color: var(--queue);
  border-color: transparent;
}
.act {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 11px;
  text-decoration: none;
  cursor: pointer;
}
.act:hover:not(:disabled) {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.act:disabled {
  opacity: 0.45;
  cursor: default;
}
.act--primary {
  color: var(--phosphor);
}
.sessions-note {
  margin: 10px 0 0;
  color: var(--queue);
  font-size: 11px;
}

/* 分组（Agent 会话 / 终端会话） */
.group {
  margin-bottom: 26px;
}
.group-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  margin-bottom: 10px;
}
.group-title {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 0;
  font-size: 13px;
  letter-spacing: 0.08em;
  color: var(--paper);
  text-transform: uppercase;
}
.group-count {
  border: 1px solid var(--line);
  border-radius: 9px;
  padding: 0 7px;
  font-size: 10px;
  color: var(--queue);
  letter-spacing: 0;
  text-transform: none;
}
.group-count--hot {
  border-color: var(--run);
  color: var(--run);
}
.check {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  cursor: pointer;
  font-size: 11px;
  color: var(--queue);
  user-select: none;
}
.check input {
  accent-color: var(--phosphor);
  margin: 0;
}
.empty code {
  color: var(--phosphor);
}

/* Agent 会话表 */
.thead--agent,
.trow--agent {
  grid-template-columns:
    minmax(180px, 2fr)
    76px
    minmax(100px, 1fr)
    minmax(90px, 0.8fr)
    84px
    78px
    76px
    52px;
}
.trow--agent {
  cursor: pointer;
}
.trow--agent:focus-visible {
  outline: 1px solid var(--phosphor);
  outline-offset: -1px;
}
.trow--active {
  background: var(--panel);
  box-shadow: inset 2px 0 0 var(--phosphor);
}
.trow--waiting {
  background: rgba(224, 162, 74, 0.1);
  box-shadow: inset 2px 0 0 var(--run);
}
.trow--waiting:hover {
  background: rgba(224, 162, 74, 0.16);
}
.trow--attn {
  box-shadow: inset 2px 0 0 var(--fail);
}
.trow--ended {
  opacity: 0.6;
}
.thead .a-title,
.thead .a-agent,
.thead .a-project,
.thead .a-runner,
.thead .a-state,
.thead .a-relay,
.thead .a-seen,
.thead .a-turns {
  color: var(--queue);
}
.a-title {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  color: var(--paper);
}
.a-title-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.a-last {
  color: var(--queue);
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.a-agent {
  color: var(--paper);
}
.a-project,
.a-runner,
.a-seen,
.a-turns {
  color: var(--queue);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.a-turns {
  text-align: right;
}
.thead .a-turns {
  text-align: right;
}
.a-relay {
  display: flex;
  align-items: center;
  gap: 5px;
}
.relay-err {
  color: var(--fail);
  font-weight: 600;
  font-size: 12px;
}
.state-badge {
  display: inline-block;
  border: 1px solid var(--line);
  border-radius: 9px;
  padding: 1px 7px;
  font-size: 10px;
  color: var(--queue);
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

/* 中继 toggle（与 SessionDrawer 同款） */
.relay-toggle {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  font-size: 10px;
  color: var(--queue);
  user-select: none;
}
.relay-toggle input {
  position: absolute;
  opacity: 0;
  width: 0;
  height: 0;
}
.relay-track {
  position: relative;
  width: 28px;
  height: 14px;
  border-radius: 7px;
  border: 1px solid var(--line);
  transition: background 0.15s, border-color 0.15s;
}
.relay-knob {
  position: absolute;
  top: 1px;
  left: 1px;
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: var(--queue);
  transition: transform 0.15s, background 0.15s;
}
.relay-toggle.on .relay-track {
  border-color: var(--phosphor);
  background: rgba(79, 176, 198, 0.18);
}
.relay-toggle.on .relay-knob {
  transform: translateX(14px);
  background: var(--phosphor);
}
.relay-toggle.on .relay-text {
  color: var(--phosphor);
}
.relay-toggle.busy {
  opacity: 0.6;
  cursor: progress;
}
.relay-toggle input:disabled ~ .relay-track {
  opacity: 0.5;
}
.empty {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--queue);
  font-size: 13px;
  padding: 28px 14px;
  text-align: center;
}

@media (max-width: 900px) {
  .thead,
  .trow {
    grid-template-columns: minmax(92px, 1fr) 72px 84px 76px;
  }
  .thead--agent,
  .trow--agent {
    grid-template-columns: minmax(140px, 2fr) 84px 78px 64px;
  }
  .a-agent,
  .a-project,
  .a-runner,
  .a-turns {
    display: none;
  }
  .bytes,
  .session-id,
  .state,
  .started,
  .flag--encrypted,
  .flag--recording,
  .session-action--recording {
    display: none;
  }
}
</style>
