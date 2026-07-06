<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { downloadPtyRecording, listRecentPtySessions } from '../api/client'
import { fmtDuration } from '../api/time'
import type { PtySession } from '../api/types'

const DEFAULT_LIMIT = 50

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
})
</script>

<template>
  <div class="board">
    <header class="board-head">
      <h1 class="title mono">SESSIONS</h1>
      <div class="controls mono">
        <RouterLink class="act act--primary mono" to="/new?mode=session">
          新建会话
        </RouterLink>
        <button
          class="act mono"
          type="button"
          :disabled="loading"
          @click="load"
        >
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
