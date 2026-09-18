<script setup lang="ts">
// 验收台（REV-01 §一.1）：待验收 job 队列（GET /v1/jobs?status=needs_review）。
// 一行看完验收材料（verify / commits / usage / 等待时长），行内直接 Accept / Reject。
// 轮询 5s + Page Visibility 暂停，与 Board 同法；顶栏徽标的计数由 EscalationBell 维护，
// 这里在本地裁决后按列表长度同步一次，避免徽标比列表慢一拍。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import RejectDialog from '../components/RejectDialog.vue'
import { acceptJob, listJobs, rejectJob } from '../api/client'
import { fmtDuration } from '../api/time'
import { needsReviewCount } from '../store/reviewCount'
import { usageBadge, verifyClass } from '../utils/jobOutcome'
import type { Job } from '../api/types'

const router = useRouter()

const POLL_MS = 5000
const PAGE_SIZE = 100

const jobs = ref<Job[]>([])
const loading = ref(false)
const error = ref('')
const nowSec = ref(Math.floor(Date.now() / 1000))

// 行内裁决的瞬时状态：busy 防重复点击，leaving 是"已裁决、正在淡出"（淡完才从列表移除，
// 期间的新一轮轮询不能再把它塞回来）。
const busyIds = ref<Set<string>>(new Set())
const leavingIds = ref<Set<string>>(new Set())
const rejectTarget = ref<Job | null>(null)
const rejecting = ref(false)
const rejectError = ref('')
const toast = ref<{ text: string; jobId?: string } | null>(null)

let pollTimer: number | null = null
let clockTimer: number | null = null
let toastTimer: number | null = null

// 等待时长倒序：等得最久的排最前（ended_at 就是进入 needs_review 的时刻）。
const rows = computed(() =>
  [...jobs.value].sort((a, b) => waitStart(a) - waitStart(b)),
)

function waitStart(job: Job): number {
  return job.ended_at ?? job.started_at ?? 0
}

function waitText(job: Job): string {
  const since = waitStart(job)
  if (!since) {
    return '—'
  }
  return `${fmtDuration(Math.max(0, nowSec.value - since))}`
}

function shortId(id: string): string {
  // 与 EscalationBell 同形：截尾并加省略号前缀，读起来才像缩写而不是被砍断的 id。
  return id.length > 10 ? `...${id.slice(-10)}` : id
}

function setToast(text: string, jobId?: string): void {
  toast.value = { text, jobId }
  if (toastTimer != null) {
    window.clearTimeout(toastTimer)
  }
  toastTimer = window.setTimeout(() => {
    toast.value = null
    toastTimer = null
  }, 8000)
}

function setBusy(id: string, busy: boolean): void {
  const next = new Set(busyIds.value)
  if (busy) {
    next.add(id)
  } else {
    next.delete(id)
  }
  busyIds.value = next
}

// 裁决成功后让该行淡出再从列表移除（视觉上"这一条办完了"），并同步顶栏计数。
function fadeOut(id: string): void {
  leavingIds.value = new Set(leavingIds.value).add(id)
  window.setTimeout(() => {
    jobs.value = jobs.value.filter((job) => job.id !== id)
    const next = new Set(leavingIds.value)
    next.delete(id)
    leavingIds.value = next
    needsReviewCount.value = jobs.value.length
  }, 320)
}

async function fetchJobs(): Promise<void> {
  loading.value = true
  try {
    const resp = await listJobs({ status: 'needs_review', limit: PAGE_SIZE })
    // 正在淡出的行不参与回填：否则一轮轮询会把它重新塞回来又立刻被移除，闪一下。
    jobs.value = (resp.jobs ?? []).filter((job) => !leavingIds.value.has(job.id))
    error.value = ''
    if (leavingIds.value.size === 0) {
      needsReviewCount.value = jobs.value.length
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function openJob(job: Job): void {
  void router.push(`/jobs/${encodeURIComponent(job.id)}`)
}

async function onAccept(job: Job): Promise<void> {
  if (busyIds.value.has(job.id)) {
    return
  }
  setBusy(job.id, true)
  try {
    await acceptJob(job.id)
    fadeOut(job.id)
  } catch (e) {
    setToast(`验收通过失败（${shortId(job.id)}）：${e instanceof Error ? e.message : String(e)}`)
  } finally {
    setBusy(job.id, false)
  }
}

async function onReject(note: string, resume: boolean): Promise<void> {
  const job = rejectTarget.value
  if (!job || rejecting.value) {
    return
  }
  rejecting.value = true
  rejectError.value = ''
  try {
    const res = await rejectJob(job.id, note, resume)
    rejectTarget.value = null
    fadeOut(job.id)
    if (res.resume_job_id) {
      setToast('已拒绝，并已续投新 job', res.resume_job_id)
    } else {
      setToast('已拒绝')
    }
  } catch (e) {
    rejectError.value = e instanceof Error ? e.message : String(e)
  } finally {
    rejecting.value = false
  }
}

function startPolling(): void {
  stopPolling()
  if (document.hidden) {
    return
  }
  pollTimer = window.setInterval(() => {
    void fetchJobs()
  }, POLL_MS)
}

function stopPolling(): void {
  if (pollTimer != null) {
    window.clearInterval(pollTimer)
    pollTimer = null
  }
}

function onVisibility(): void {
  if (document.hidden) {
    stopPolling()
  } else {
    void fetchJobs()
    startPolling()
  }
}

onMounted(() => {
  void fetchJobs()
  startPolling()
  // 等待时长按秒走（ended_at 起），1s 一跳与详情页时钟同法。
  clockTimer = window.setInterval(() => {
    nowSec.value = Math.floor(Date.now() / 1000)
  }, 1000)
  document.addEventListener('visibilitychange', onVisibility)
})

onUnmounted(() => {
  stopPolling()
  if (clockTimer != null) {
    window.clearInterval(clockTimer)
    clockTimer = null
  }
  if (toastTimer != null) {
    window.clearTimeout(toastTimer)
    toastTimer = null
  }
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <div class="rq">
    <div class="rq-head">
      <div>
        <h1 class="title mono">REVIEW QUEUE</h1>
        <p class="subtitle mono">
          {{ rows.length }} 个待验收 job · 等得最久的排最前
          <span v-if="loading"> · 刷新中</span>
        </p>
      </div>
      <div class="head-actions mono">
        <button class="head-btn" type="button" :disabled="loading" @click="fetchJobs">刷新</button>
      </div>
    </div>

    <p v-if="error" class="error mono">{{ error }}</p>

    <div class="table">
      <div class="thead mono">
        <span class="col-job">job · 标题 / id</span>
        <span class="col-proj">project</span>
        <span class="col-agent">agent</span>
        <span class="col-plan">plan · todo</span>
        <span class="col-verify">verify</span>
        <span class="col-commits">提交</span>
        <span class="col-usage">usage</span>
        <span class="col-wait">等待</span>
        <span class="col-actions">裁决</span>
      </div>

      <div
        v-for="job in rows"
        :key="job.id"
        class="trow"
        :class="{ 'trow--leaving': leavingIds.has(job.id) }"
        role="link"
        tabindex="0"
        :title="job.title || job.id"
        @click="openJob(job)"
        @keydown.enter="openJob(job)"
      >
        <span class="col-job" :class="{ 'col-job--titled': job.title }">
          <span v-if="job.title" class="job-title">{{ job.title }}</span>
          <span class="job-id mono">{{ shortId(job.id) }}</span>
        </span>
        <span class="col-proj mono">{{ job.project_key }}</span>
        <span class="col-agent mono">{{ job.agent }}</span>
        <span class="col-plan mono">
          <RouterLink
            v-if="job.plan_id"
            class="plan-link"
            :to="`/plans/${encodeURIComponent(job.plan_id)}`"
            :title="`plan ${job.plan_id}${job.todo_id ? ` · todo ${job.todo_id}` : ''}`"
            @click.stop
          >
            {{ job.plan_id }}<template v-if="job.todo_id"> · {{ job.todo_id }}</template>
          </RouterLink>
          <span v-else>—</span>
        </span>
        <span class="col-verify">
          <span class="verify-badge mono" :class="verifyClass(job.verify)">
            {{ job.verify?.status ?? '—' }}
          </span>
        </span>
        <span class="col-commits mono">{{ (job.commits ?? []).length }}</span>
        <span class="col-usage mono" :title="usageBadge(job.usage)">{{ usageBadge(job.usage) }}</span>
        <span class="col-wait mono" :title="job.ended_at ? new Date(job.ended_at * 1000).toLocaleString() : ''">
          {{ waitText(job) }}
        </span>
        <span class="col-actions">
          <button
            class="rq-btn rq-btn--go mono"
            type="button"
            :disabled="busyIds.has(job.id)"
            @click.stop="onAccept(job)"
          >
            {{ busyIds.has(job.id) ? '…' : 'Accept' }}
          </button>
          <button
            class="rq-btn rq-btn--no mono"
            type="button"
            :disabled="busyIds.has(job.id)"
            @click.stop="rejectTarget = job"
          >
            Reject
          </button>
        </span>
      </div>

      <div v-if="rows.length === 0 && !error" class="empty mono">
        <p class="empty-line">现在没有待验收的 job。</p>
        <p class="empty-line">要让一个 job 停下来等人验收，两种方式：</p>
        <p class="empty-line">
          命令：<code>gofer job run … --review</code>（本次提交要求人验收）
        </p>
        <p class="empty-line">
          项目：项目配置里 <code>require_review: true</code>（该项目所有 job 都要人验收）
        </p>
        <p class="empty-line">
          容器里也可以直接看：<code>gofer job review &lt;id&gt;</code>
        </p>
      </div>
    </div>

    <RejectDialog
      v-if="rejectTarget"
      :submitting="rejecting"
      :error="rejectError"
      @close="rejectTarget = null"
      @confirm="onReject"
    />

    <div v-if="toast" class="rq-toast mono" role="status" @click="toast = null">
      <span>{{ toast.text }}</span>
      <RouterLink
        v-if="toast.jobId"
        class="rq-toast-link"
        :to="`/jobs/${encodeURIComponent(toast.jobId)}`"
        @click.stop
      >
        {{ shortId(toast.jobId) }}
      </RouterLink>
    </div>
  </div>
</template>

<style scoped>
.rq {
  max-width: 1240px;
  margin: 0 auto;
}
.rq-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
}
.subtitle {
  color: var(--queue);
  font-size: 12px;
  margin: 6px 0 0;
}
.head-actions {
  display: flex;
  gap: 8px;
  font-size: 12px;
}
.head-btn {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
}
.head-btn:hover:not(:disabled) {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.head-btn:disabled {
  color: var(--queue);
  cursor: default;
  opacity: 0.65;
}
.error {
  color: var(--fail);
  font-size: 12px;
}

/* 表：窄屏横向滚动（列多，压缩到不可读不如让它滚）。 */
.table {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow-x: auto;
  background: var(--panel);
}
.thead,
.trow {
  display: grid;
  grid-template-columns:
    minmax(280px, 2fr) minmax(78px, 0.7fr) minmax(74px, 0.6fr) minmax(120px, 0.9fr)
    64px 50px minmax(140px, 1fr) 70px 146px;
  align-items: center;
  gap: 10px;
  padding: 6px 10px;
  min-width: 1000px;
}
.thead {
  color: var(--queue);
  font-size: 11px;
  letter-spacing: 0.06em;
  border-bottom: 1px solid var(--line);
}
.trow {
  border-bottom: 1px solid var(--line);
  font-size: 12px;
  cursor: pointer;
  transition: opacity 0.3s ease, background 0.15s ease;
}
.trow:last-child {
  border-bottom: none;
}
.trow:hover {
  background: var(--term-bg);
}
.trow:focus-visible {
  outline: 1px solid var(--phosphor);
}
/* 已裁决：淡出（列表下一拍移除）。 */
.trow--leaving {
  opacity: 0;
  pointer-events: none;
}

.col-job {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.job-title {
  flex: 1 1 auto;
  min-width: 0;
  color: var(--paper);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-job--titled .job-id {
  flex: none;
}
.job-id {
  color: var(--queue);
  font-size: 11px;
}
.col-proj,
.col-agent,
.col-plan,
.col-commits,
.col-wait {
  color: var(--queue);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-usage {
  color: var(--paper);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.plan-link {
  color: var(--phosphor);
}

/* verify 徽标：passed 绿 / failed·timeout 红 / skipped 或无结果 灰。 */
.verify-badge {
  font-size: 11px;
}
.verify-badge.verify--ok {
  color: var(--done);
}
.verify-badge.verify--bad {
  color: var(--fail);
}
.verify-badge.verify--skip {
  color: var(--queue);
}

.col-actions {
  display: flex;
  gap: 6px;
}
.rq-btn {
  border-radius: var(--radius);
  padding: 2px 10px;
  font-size: 11px;
  border: 1px solid var(--line);
  background: transparent;
  color: var(--paper);
}
.rq-btn--go {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.rq-btn--go:hover:not(:disabled) {
  background: var(--phosphor);
  color: var(--ink);
}
.rq-btn--no {
  border-color: var(--fail);
  color: var(--fail);
}
.rq-btn--no:hover:not(:disabled) {
  background: var(--fail);
  color: var(--ink);
}
.rq-btn:disabled {
  opacity: 0.6;
  cursor: default;
}

.empty {
  padding: 22px 14px;
  color: var(--queue);
  font-size: 12px;
  min-width: 480px;
}
.empty-line {
  margin: 4px 0;
}
.empty code {
  color: var(--phosphor);
  background: var(--term-bg);
  border: 1px solid var(--line);
  border-radius: 3px;
  padding: 0 4px;
}

/* 裁决结果/失败提示：右下角浮层，点一下关掉，8s 自动消失。 */
.rq-toast {
  position: fixed;
  right: 16px;
  bottom: 16px;
  z-index: 50;
  display: flex;
  align-items: center;
  gap: 10px;
  max-width: 460px;
  padding: 8px 12px;
  background: var(--panel);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  color: var(--paper);
  font-size: 12px;
  cursor: pointer;
}
.rq-toast-link {
  color: var(--phosphor);
  flex: none;
}
</style>
