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
  return `输入 ${s.bytes_in} / 输出 ${s.bytes_out} 字节`
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
  <div class="sessions-page">
    <header class="sessions-head">
      <div>
        <h1 class="sessions-title mono">Sessions</h1>
        <p class="sessions-sub mono">最近终端会话</p>
      </div>
      <div class="head-actions">
        <RouterLink class="head-action head-action--primary mono" to="/new?mode=session">
          新建会话
        </RouterLink>
        <button
          class="head-action mono"
          type="button"
          :disabled="loading"
          @click="load"
        >
          {{ loading ? '刷新中…' : '刷新' }}
        </button>
      </div>
    </header>

    <p v-if="error" class="error mono">{{ error }}</p>

    <div v-if="hasSessions" class="sessions-list">
      <article
        v-for="s in sessions"
        :key="s.pty_session_id"
        class="session-row"
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
        <span class="flag mono" :class="{ on: s.encrypted }">
          {{ s.encrypted ? '加密' : '明文' }}
        </span>
        <span class="flag mono" :class="{ on: s.has_recording }">
          {{ s.has_recording ? '已录制' : '无录制' }}
        </span>
        <button
          v-if="s.has_recording"
          class="session-action mono"
          type="button"
          :disabled="downloadingRecordingIds.has(s.pty_session_id)"
          @click="onDownloadRecording(s)"
        >
          {{ downloadingRecordingIds.has(s.pty_session_id) ? '下载中' : '下载录制' }}
        </button>
        <span v-else class="session-action session-action--empty mono">—</span>
        <RouterLink
          v-if="canAttachSession(s)"
          class="session-action mono"
          :to="`/jobs/${encodeURIComponent(s.job_id ?? '')}?attach=1`"
        >
          打开终端
        </RouterLink>
        <span v-else class="session-action session-action--empty mono">—</span>
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
.sessions-page {
  max-width: 1200px;
  margin: 0 auto;
}
.sessions-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 14px;
}
.sessions-title {
  margin: 0;
  color: var(--paper);
  font-size: 18px;
  letter-spacing: 0.04em;
}
.sessions-sub {
  margin: 4px 0 0;
  color: var(--queue);
  font-size: 12px;
}
.head-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: none;
}
.head-action {
  flex: none;
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 12px;
  font-size: 12px;
  text-decoration: none;
}
.head-action:hover:not(:disabled) {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.head-action:disabled {
  cursor: default;
  opacity: 0.5;
}
.head-action--primary {
  background: var(--phosphor);
  border-color: var(--phosphor);
  color: var(--ink);
}
.head-action--primary:hover {
  color: var(--ink);
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
.sessions-list {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.session-row {
  display: grid;
  grid-template-columns:
    minmax(96px, 1fr)
    72px
    minmax(150px, 1.2fr)
    minmax(112px, 0.9fr)
    84px
    86px
    64px
    72px
    78px
    76px
    minmax(160px, 1fr);
  align-items: center;
  gap: 10px;
  min-height: 38px;
  padding: 7px 12px;
  border-bottom: 1px solid var(--line);
  font-size: 12px;
}
.session-row:last-child {
  border-bottom: none;
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
  color: var(--phosphor);
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
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 2px 8px;
  font-size: 11px;
  text-align: center;
  text-decoration: none;
  white-space: nowrap;
}
.session-action:hover:not(:disabled) {
  border-color: var(--phosphor);
  color: var(--paper);
}
.session-action:disabled {
  cursor: default;
  opacity: 0.55;
}
.session-action--empty {
  color: var(--queue);
  border-color: transparent;
}
.sessions-note {
  margin: 10px 0 0;
  color: var(--queue);
  font-size: 11px;
}
.empty {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--queue);
  font-size: 12px;
  padding: 24px;
  text-align: center;
}

@media (max-width: 900px) {
  .session-row {
    grid-template-columns: minmax(92px, 1fr) 72px 84px 76px;
  }
  .bytes,
  .session-id,
  .state,
  .started,
  .flag:first-of-type,
  .session-action:first-of-type {
    display: none;
  }
}
</style>
