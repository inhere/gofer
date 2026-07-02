<script setup lang="ts">
// Schedules 列表（cron 定时调度，AUTO-02）：轮询 listSchedules（2.5s），Page
// Visibility 暂停/恢复，project 过滤。行内操作：立即运行 / 启用停用 / 删除；
// 点击行展开被调度的 JobRequest 摘要。仿 Workflows.vue 结构。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  deleteSchedule,
  listSchedules,
  runSchedule,
  setScheduleEnabled,
} from '../api/client'
import { fmtDateTime, fmtUntil } from '../api/time'
import type { Schedule } from '../api/types'

const router = useRouter()
const route = useRoute()

const POLL_MS = 2500

const schedules = ref<Schedule[]>([])
const loading = ref(false)
const error = ref('')
const notice = ref('')
// 正在执行操作的 schedule id（禁用该行按钮，避免重复点击）
const busy = ref<Set<string>>(new Set())
// 展开显示 request 摘要的 schedule id
const expanded = ref<Set<string>>(new Set())

// 左轨/URL 传入的 project 过滤（与 board 一致，?project=）
const projectFilter = computed(() => {
  const p = route.query.project
  return typeof p === 'string' ? p : ''
})

let timer: number | null = null

async function fetchSchedules(): Promise<void> {
  loading.value = true
  try {
    const resp = await listSchedules(projectFilter.value || undefined)
    schedules.value = resp.schedules ?? []
    error.value = ''
  } catch (e) {
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
  timer = window.setInterval(() => {
    void fetchSchedules()
  }, POLL_MS)
}

function stopPolling(): void {
  if (timer != null) {
    window.clearInterval(timer)
    timer = null
  }
}

function onVisibility(): void {
  if (document.hidden) {
    stopPolling()
  } else {
    void fetchSchedules()
    startPolling()
  }
}

watch(projectFilter, () => {
  void fetchSchedules()
})

function setBusy(id: string, on: boolean): void {
  const next = new Set(busy.value)
  if (on) {
    next.add(id)
  } else {
    next.delete(id)
  }
  busy.value = next
}

function toggleExpand(id: string): void {
  const next = new Set(expanded.value)
  if (next.has(id)) {
    next.delete(id)
  } else {
    next.add(id)
  }
  expanded.value = next
}

async function onRun(s: Schedule): Promise<void> {
  notice.value = ''
  error.value = ''
  setBusy(s.id, true)
  try {
    const job = await runSchedule(s.id)
    notice.value = `已触发 ${s.name}，job ${job.id}`
    await fetchSchedules()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    setBusy(s.id, false)
  }
}

async function onToggleEnabled(s: Schedule): Promise<void> {
  notice.value = ''
  error.value = ''
  setBusy(s.id, true)
  try {
    await setScheduleEnabled(s.id, s.enabled === 0)
    await fetchSchedules()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    setBusy(s.id, false)
  }
}

async function onDelete(s: Schedule): Promise<void> {
  if (!window.confirm(`删除定时调度「${s.name}」？此操作不可恢复。`)) {
    return
  }
  notice.value = ''
  error.value = ''
  setBusy(s.id, true)
  try {
    await deleteSchedule(s.id)
    notice.value = `已删除 ${s.name}`
    await fetchSchedules()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    setBusy(s.id, false)
  }
}

function openJob(id: string): void {
  void router.push(`/jobs/${encodeURIComponent(id)}`)
}

function shortId(id: string): string {
  return id.length > 12 ? id.slice(-12) : id
}

// request 的执行摘要（agent/runner/cwd + prompt 首行 / cmd）
function reqSummary(s: Schedule): string {
  const r = s.request
  const parts: string[] = []
  if (r.cmd && r.cmd.length) {
    parts.push(`cmd: ${r.cmd.join(' ')}`)
  } else if (r.prompt) {
    parts.push(`prompt: ${r.prompt.split('\n')[0]}`)
  }
  return parts.join('  ')
}

onMounted(() => {
  void fetchSchedules()
  startPolling()
  document.addEventListener('visibilitychange', onVisibility)
})

onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <div class="board">
    <div class="board-head">
      <h1 class="title mono">
        SCHEDULES<span v-if="projectFilter" class="title-filter"> · {{ projectFilter }}</span>
      </h1>
      <div class="controls mono">
        <RouterLink to="/schedules/new" class="new-sch">+ 新建 cron</RouterLink>
        <span class="poll-hint" :class="{ 'poll-hint--on': loading }">●</span>
      </div>
    </div>

    <p v-if="error" class="error mono">{{ error }}</p>
    <p v-if="notice" class="notice mono">{{ notice }}</p>

    <div class="table">
      <div class="thead mono">
        <span class="col-en">启用</span>
        <span class="col-name">name / id</span>
        <span class="col-cron">cron</span>
        <span class="col-next">下次触发</span>
        <span class="col-last">上次 job</span>
        <span class="col-act">操作</span>
      </div>

      <template v-for="s in schedules" :key="s.id">
        <div
          class="trow"
          role="button"
          tabindex="0"
          @click="toggleExpand(s.id)"
          @keydown.enter="toggleExpand(s.id)"
        >
          <span class="col-en">
            <span
              class="en-dot"
              :class="s.enabled ? 'en-dot--on' : 'en-dot--off'"
              :title="s.enabled ? 'enabled' : 'disabled'"
            ></span>
          </span>
          <span class="col-name">
            <span class="sch-name" :title="s.name">{{ s.name }}</span>
            <span class="sch-sub mono">
              <span class="sch-id" :title="s.id">{{ shortId(s.id) }}</span>
              <span class="sch-pr">{{ s.project_key }} · {{ s.request.agent }} · {{ s.request.runner }}</span>
            </span>
          </span>
          <span class="col-cron mono" :title="s.cron">{{ s.cron }}</span>
          <span class="col-next mono">
            <template v-if="s.enabled">
              <span class="next-abs">{{ fmtDateTime(s.next_run_at) }}</span>
              <span class="next-rel">{{ fmtUntil(s.next_run_at) }}</span>
            </template>
            <span v-else class="next-off">已停用</span>
          </span>
          <span class="col-last mono">
            <template v-if="s.last_job_id">
              <a class="last-link" :title="s.last_job_id" @click.stop="openJob(s.last_job_id)">
                {{ shortId(s.last_job_id) }}
              </a>
              <span class="last-abs">{{ fmtDateTime(s.last_run_at) }}</span>
            </template>
            <span v-else class="last-none">—</span>
          </span>
          <span class="col-act mono" @click.stop>
            <button
              class="act act--run"
              type="button"
              :disabled="busy.has(s.id)"
              title="立即运行一次"
              @click="onRun(s)"
            >
              运行
            </button>
            <button
              class="act"
              type="button"
              :disabled="busy.has(s.id)"
              :title="s.enabled ? '停用' : '启用'"
              @click="onToggleEnabled(s)"
            >
              {{ s.enabled ? '停用' : '启用' }}
            </button>
            <button
              class="act act--del"
              type="button"
              :disabled="busy.has(s.id)"
              title="删除"
              @click="onDelete(s)"
            >
              删
            </button>
          </span>
        </div>

        <!-- 展开：被调度的 JobRequest 摘要 -->
        <div v-if="expanded.has(s.id)" class="detail mono">
          <div class="detail-grid">
            <span class="dk">catch_up</span><span class="dv">{{ s.catch_up ? 'true' : 'false' }}</span>
            <span class="dk">cwd</span><span class="dv">{{ s.request.cwd || '.' }}</span>
            <template v-if="s.request.title">
              <span class="dk">title</span><span class="dv">{{ s.request.title }}</span>
            </template>
            <template v-if="s.request.timeout_sec">
              <span class="dk">timeout</span><span class="dv">{{ s.request.timeout_sec }}s</span>
            </template>
            <template v-if="s.request.worker_id">
              <span class="dk">worker_id</span><span class="dv">{{ s.request.worker_id }}</span>
            </template>
            <template v-if="s.request.worker_labels && s.request.worker_labels.length">
              <span class="dk">worker_labels</span><span class="dv">{{ s.request.worker_labels.join(', ') }}</span>
            </template>
            <template v-if="s.request.tags && s.request.tags.length">
              <span class="dk">tags</span><span class="dv">{{ s.request.tags.join(', ') }}</span>
            </template>
          </div>
          <pre v-if="reqSummary(s)" class="detail-cmd">{{ reqSummary(s) }}</pre>
        </div>
      </template>

      <div v-if="schedules.length === 0 && !error" class="empty mono">
        暂无定时调度，<RouterLink to="/schedules/new" class="empty-link">新建一个</RouterLink>
      </div>
    </div>
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
  margin-bottom: 14px;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
}
.title-filter {
  color: var(--phosphor);
  font-size: 13px;
}
.controls {
  display: flex;
  align-items: center;
  gap: 14px;
  font-size: 12px;
}
.new-sch {
  background: var(--phosphor);
  color: var(--ink);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
  font-weight: 600;
}
.new-sch:hover {
  text-decoration: none;
  opacity: 0.9;
}
.poll-hint {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
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
  margin: 0 0 12px;
  word-break: break-word;
}
.notice {
  color: var(--done);
  font-size: 12px;
  margin: 0 0 12px;
}

.table {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.thead,
.trow {
  display: grid;
  grid-template-columns: 52px minmax(180px, 1fr) 150px 150px 132px 176px;
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
  cursor: pointer;
  font-size: 13px;
  outline: none;
}
.trow:hover {
  background: var(--panel);
}
.trow:focus-visible {
  background: var(--panel);
  box-shadow: inset 2px 0 0 var(--phosphor);
}

.col-en {
  display: flex;
  justify-content: center;
}
.en-dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  flex: none;
}
.en-dot--on {
  background: var(--done);
}
.en-dot--off {
  background: var(--queue);
  opacity: 0.6;
  box-shadow: 0 0 0 1px var(--line);
}

.col-name {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.sch-name {
  color: var(--paper);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sch-sub {
  display: flex;
  gap: 8px;
  font-size: 11px;
  min-width: 0;
}
.sch-id {
  color: var(--phosphor);
  flex: none;
}
.sch-pr {
  color: var(--queue);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.col-cron {
  color: var(--paper);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.col-next {
  display: flex;
  flex-direction: column;
  gap: 1px;
  font-size: 12px;
}
.next-abs {
  color: var(--paper);
}
.next-rel {
  color: var(--queue);
  font-size: 11px;
}
.next-off {
  color: var(--queue);
}

.col-last {
  display: flex;
  flex-direction: column;
  gap: 1px;
  font-size: 12px;
}
.last-link {
  color: var(--phosphor);
  cursor: pointer;
}
.last-link:hover {
  text-decoration: underline;
}
.last-abs {
  color: var(--queue);
  font-size: 11px;
}
.last-none {
  color: var(--queue);
}

.col-act {
  display: flex;
  gap: 6px;
  justify-content: flex-end;
}
.act {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 8px;
  font-size: 11px;
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
.act--run:hover:not(:disabled) {
  border-color: var(--done);
  color: var(--done);
}
.act--del:hover:not(:disabled) {
  border-color: var(--fail);
  color: var(--fail);
}

.detail {
  border-bottom: 1px solid var(--line);
  background: var(--ink);
  padding: 10px 14px 12px 66px;
  font-size: 12px;
}
.detail-grid {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 3px 12px;
  align-items: baseline;
}
.dk {
  color: var(--queue);
  font-size: 11px;
}
.dv {
  color: var(--paper);
  word-break: break-word;
}
.detail-cmd {
  margin: 8px 0 0;
  padding: 8px 10px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--paper);
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 12px;
}

.empty {
  padding: 28px 14px;
  text-align: center;
  color: var(--queue);
  font-size: 13px;
}
.empty-link,
.empty-link:hover {
  color: var(--phosphor);
}

@media (max-width: 860px) {
  .thead {
    display: none;
  }
  .trow {
    grid-template-columns: 24px 1fr;
    grid-template-areas:
      'en name'
      'en cron'
      'en next'
      'en act';
    row-gap: 6px;
  }
  .col-en {
    grid-area: en;
    align-items: flex-start;
    padding-top: 4px;
  }
  .col-name {
    grid-area: name;
  }
  .col-cron {
    grid-area: cron;
  }
  .col-next {
    grid-area: next;
    flex-direction: row;
    gap: 8px;
  }
  .col-last {
    display: none;
  }
  .col-act {
    grid-area: act;
    justify-content: flex-start;
  }
  .detail {
    padding-left: 38px;
  }
}
</style>
