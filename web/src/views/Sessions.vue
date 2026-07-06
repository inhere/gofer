<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { listRecentPtySessions } from '../api/client'
import { fmtDuration } from '../api/time'
import type { PtySession } from '../api/types'

const DEFAULT_LIMIT = 50

const sessions = ref<PtySession[]>([])
const loading = ref(false)
const error = ref('')
const nowSec = ref(Math.floor(Date.now() / 1000))

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
  return `in ${s.bytes_in} / out ${s.bytes_out}`
}

function shortId(id?: string): string {
  if (!id) {
    return '—'
  }
  return id.length > 10 ? id.slice(-10) : id
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
        <span class="duration mono">{{ duration(s) }}</span>
        <span class="state mono">{{ s.state }}</span>
        <span class="flag mono" :class="{ on: s.encrypted }">
          {{ s.encrypted ? '加密' : '明文' }}
        </span>
        <span class="flag mono" :class="{ on: s.has_recording }">
          {{ s.has_recording ? '已录制' : '无录制' }}
        </span>
        <span class="started mono">{{ fmtTime(s.started_at) }}</span>
      </article>
    </div>

    <div v-else-if="!loading && !error" class="empty mono">
      暂无终端会话
    </div>
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
    84px
    86px
    64px
    72px
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
.started {
  color: var(--queue);
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
    grid-template-columns: minmax(92px, 1fr) 72px 84px 72px;
  }
  .bytes,
  .state,
  .started,
  .flag:first-of-type {
    display: none;
  }
}
</style>
