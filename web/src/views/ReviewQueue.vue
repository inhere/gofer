<script setup lang="ts">
// 验收台（REV-01 §一.1、设计稿 §7.1 /review）：待验收 job 队列（GET /v1/jobs?status=needs_review）。
// 一行看完验收材料（verify / commits / usage / 等待时长），行内直接 Accept / Reject。
// 列表随 jobs 主题刷新；待验收计数已并入顶栏「待我决策 N」（N3），这里不再维护单独的徽标。
//
// P4：页头改用 ui/ProjectFilter（?project= 是唯一真源）+ ui/FilterChips（验证结果），
// 文案全部走 i18n 的 review.*；手机（≤760）每行变成一张卡片、裁决按钮铺满。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { createLiveTopic } from '../utils/useLiveTopic'
import { useRouter } from 'vue-router'
import RejectDialog from '../components/RejectDialog.vue'
import ProjectFilter from '../ui/ProjectFilter.vue'
import FilterChips from '../ui/FilterChips.vue'
import { type FilterChipItem } from '../ui/filterChips'
import { useProjectFilter } from '../ui/projectFilter'
import { acceptJob, listJobs, rejectJob } from '../api/client'
import { fmtDuration } from '../api/time'
import { usageBadge, verifyClass } from '../utils/jobOutcome'
import { canReviewJob } from '../utils/reviewGate'
import { useWhoami } from '../store/whoami'
import { t } from '../i18n'
import type { Job } from '../api/types'

const router = useRouter()
// 行内「通过 / 拒绝」按与 job 详情同样的规则显示（utils/reviewGate，服务端仍是权威）。
const { whoami } = useWhoami()

const PAGE_SIZE = 100

const jobs = ref<Job[]>([])
const loading = ref(false)
const error = ref('')
const nowSec = ref(Math.floor(Date.now() / 1000))

// 项目 chip：值写进 URL 的 ?project=（可以分享、可以前进后退复原），换值即重拉。
const { project, setProject, options: projectOptions, loadOptions: loadProjectOptions, loadError: projectLoadError } =
  useProjectFilter('/review')

// 验证结果 chip：纯前端维度（一次最多取 100 条待验收），不写 URL。
type VerifyFilter = '' | 'passed' | 'failed' | 'none'
const verifyFilter = ref<VerifyFilter>('')

// 行内裁决的瞬时状态：busy 防重复点击，leaving 是"已裁决、正在淡出"（淡完才从列表移除，
// 期间的新一轮轮询不能再把它塞回来）。
const busyIds = ref<Set<string>>(new Set())
const leavingIds = ref<Set<string>>(new Set())
const rejectTarget = ref<Job | null>(null)
const rejecting = ref(false)
const rejectError = ref('')
const toast = ref<{ text: string; jobId?: string } | null>(null)
// gofer-9b1b：待批 job 不进验收台（它们还没跑），页头只放一个「待批准 N →」入口（N>0 才显示）。
const approvalCount = ref(0)
const APPROVAL_LIST = '/jobs?status=awaiting_approval'

let clockTimer: number | null = null
let toastTimer: number | null = null

// 行内裁决的显示规则与原因文案（与 job 详情同一入口，服务端为准）。
function reviewGate(job: Job) {
  return canReviewJob(whoami.value, job)
}

// 等待时长倒序：等得最久的排最前（ended_at 就是进入 needs_review 的时刻）。
const rows = computed(() =>
  [...jobs.value].sort((a, b) => waitStart(a) - waitStart(b)),
)

// 验证结果分档：passed / failed（含 timeout）/ none（没有 verify 结果）。
function verifyBucket(job: Job): VerifyFilter {
  const status = job.verify?.status
  if (!status) {
    return 'none'
  }
  if (status === 'passed') {
    return 'passed'
  }
  return status === 'skipped' ? 'none' : 'failed'
}

const verifyChipItems = computed<FilterChipItem[]>(() => {
  const counts: Record<string, number> = { passed: 0, failed: 0, none: 0 }
  for (const job of jobs.value) {
    counts[verifyBucket(job)] += 1
  }
  return [
    { value: '', label: t('review.filter.all'), count: jobs.value.length },
    { value: 'passed', label: t('review.filter.passed'), count: counts.passed },
    { value: 'failed', label: t('review.filter.failed'), count: counts.failed },
    { value: 'none', label: t('review.filter.none'), count: counts.none },
  ]
})

const visibleRows = computed(() =>
  verifyFilter.value === '' ? rows.value : rows.value.filter((job) => verifyBucket(job) === verifyFilter.value),
)
// 有没有生效中的筛选（决定空态说「没有待验收」还是「当前筛选下没有」）。
const filtered = computed(() => project.value !== '' || verifyFilter.value !== '')

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

// 裁决成功后让该行淡出再从列表移除（视觉上"这一条办完了"）。
function fadeOut(id: string): void {
  leavingIds.value = new Set(leavingIds.value).add(id)
  window.setTimeout(() => {
    jobs.value = jobs.value.filter((job) => job.id !== id)
    const next = new Set(leavingIds.value)
    next.delete(id)
    leavingIds.value = next
  }, 320)
}

async function fetchJobs(): Promise<void> {
  loading.value = true
  try {
    const [resp] = await Promise.all([
      listJobs({ status: 'needs_review', project: project.value || undefined, limit: PAGE_SIZE }),
      fetchApprovalCount(),
    ])
    // 正在淡出的行不参与回填：否则一轮轮询会把它重新塞回来又立刻被移除，闪一下。
    jobs.value = (resp.jobs ?? []).filter((job) => !leavingIds.value.has(job.id))
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// 待批计数失败只影响页头入口，不顶掉验收列表的错误条。
async function fetchApprovalCount(): Promise<void> {
  try {
    const resp = await listJobs({ status: 'awaiting_approval', limit: PAGE_SIZE })
    approvalCount.value = (resp.jobs ?? []).length
  } catch {
    // 下一轮刷新再试
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
    setToast(
      t('review.toast.acceptFailed', { id: shortId(job.id), msg: e instanceof Error ? e.message : String(e) }),
    )
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
    setToast(res.resume_job_id ? t('review.toast.resumed') : t('review.toast.rejected'), res.resume_job_id)
  } catch (e) {
    rejectError.value = e instanceof Error ? e.message : String(e)
  } finally {
    rejecting.value = false
  }
}

// Q3：`jobs` 主题的失效通知触发重拉；WS 断开超过 15s 才由 30s 兜底轮询接手（恢复后自动停）。
const liveJobs = createLiveTopic('jobs', { initial: false, fetch: fetchJobs })

// 换项目（?project=）重新取数：筛选值以 URL 为准，返回/前进也要跟着变。
watch(project, () => {
  void fetchJobs()
})

onMounted(() => {
  void loadProjectOptions()
  void fetchJobs()
  liveJobs.start()
  // 等待时长按秒走（ended_at 起），1s 一跳与详情页时钟同法。
  clockTimer = window.setInterval(() => {
    nowSec.value = Math.floor(Date.now() / 1000)
  }, 1000)
})

onUnmounted(() => {
  liveJobs.stop()
  if (clockTimer != null) {
    window.clearInterval(clockTimer)
    clockTimer = null
  }
  if (toastTimer != null) {
    window.clearTimeout(toastTimer)
    toastTimer = null
  }
})
</script>

<template>
  <div class="rq" data-test="review-page">
    <div class="rq-head">
      <div class="rq-head-text">
        <h1 class="title">{{ t('review.h.title') }}</h1>
        <p class="subtitle">
          {{
            filtered
              ? t('review.h.summaryFiltered', { n: visibleRows.length, total: rows.length })
              : t('review.h.summary', { n: rows.length })
          }}<span v-if="loading">{{ t('review.h.refreshing') }}</span>
        </p>
        <RouterLink
          v-if="approvalCount > 0"
          class="rq-approvals"
          :to="APPROVAL_LIST"
          data-test="rq-approvals"
        >
          {{ t('review.h.approvals', { n: approvalCount }) }}
        </RouterLink>
      </div>
      <div class="head-actions">
        <button class="head-btn" type="button" :disabled="loading" @click="fetchJobs">
          {{ t('review.h.refresh') }}
        </button>
      </div>
    </div>

    <!-- 列表头（设计稿 §6.4、§7.1）：项目 chip + 验证结果 chip。 -->
    <div class="filter-bar">
      <ProjectFilter
        :model-value="project"
        :options="projectOptions"
        :loading="!projectOptions.length && !projectLoadError"
        :error="projectLoadError"
        @update:model-value="setProject"
      />
      <FilterChips
        :items="verifyChipItems"
        :model-value="verifyFilter"
        :label="t('review.filter.verify')"
        @update:model-value="(v) => (verifyFilter = v as VerifyFilter)"
      />
    </div>

    <p v-if="error" class="error">{{ error }}</p>

    <div class="table">
      <div class="thead">
        <span class="c-job">{{ t('review.col.job') }}</span>
        <span class="c-proj">{{ t('review.col.project') }}</span>
        <span class="c-agent">{{ t('review.col.agent') }}</span>
        <span class="c-plan">{{ t('review.col.plan') }}</span>
        <span class="c-verify">{{ t('review.col.verify') }}</span>
        <span class="c-commits">{{ t('review.col.commits') }}</span>
        <span class="c-usage">{{ t('review.col.usage') }}</span>
        <span class="c-wait">{{ t('review.col.wait') }}</span>
        <span class="c-actions">{{ t('review.col.actions') }}</span>
      </div>

      <div
        v-for="job in visibleRows"
        :key="job.id"
        class="trow"
        :class="{ 'trow--leaving': leavingIds.has(job.id) }"
        role="link"
        tabindex="0"
        :title="job.title || job.id"
        data-test="review-row"
        @click="openJob(job)"
        @keydown.enter="openJob(job)"
      >
        <span class="c-job" :class="{ 'c-job--titled': job.title }">
          <span v-if="job.title" class="job-title">{{ job.title }}</span>
          <span class="job-id mono">{{ shortId(job.id) }}</span>
        </span>
        <span class="c-proj mono">{{ job.project_key }}</span>
        <span class="c-agent mono">{{ job.agent }}</span>
        <span class="c-plan mono">
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
        <span class="c-verify">
          <span class="verify-badge mono" :class="verifyClass(job.verify)">
            {{ job.verify?.status ?? '—' }}
          </span>
        </span>
        <span class="c-commits mono">{{ (job.commits ?? []).length }}</span>
        <span class="c-usage mono" :title="usageBadge(job.usage)">{{ usageBadge(job.usage) }}</span>
        <span class="c-wait mono" :title="job.ended_at ? new Date(job.ended_at * 1000).toLocaleString() : ''">
          {{ waitText(job) }}
        </span>
        <span class="c-actions">
          <template v-if="reviewGate(job).allowed">
            <button
              class="rq-btn rq-btn--go"
              type="button"
              :disabled="busyIds.has(job.id)"
              @click.stop="onAccept(job)"
            >
              {{ busyIds.has(job.id) ? t('review.act.busy') : t('review.act.accept') }}
            </button>
            <button
              class="rq-btn rq-btn--no"
              type="button"
              :disabled="busyIds.has(job.id)"
              @click.stop="rejectTarget = job"
            >
              {{ busyIds.has(job.id) ? t('review.act.busy') : t('review.act.reject') }}
            </button>
          </template>
          <span v-else class="rq-blocked" data-test="rq-blocked">{{ reviewGate(job).reason }}</span>
        </span>
      </div>

      <div v-if="visibleRows.length === 0 && !error" class="empty" data-test="review-empty">
        <template v-if="filtered">
          <p class="empty-line">{{ t('review.empty.filtered') }}</p>
        </template>
        <template v-else>
          <p class="empty-line">{{ t('review.empty.none') }}</p>
          <p class="empty-line">{{ t('review.empty.howTo') }}</p>
          <p class="empty-line">
            {{ t('review.empty.cmdLabel') }}<code>gofer job run … --review</code>{{ t('review.empty.cmdHint') }}
          </p>
          <p class="empty-line">
            {{ t('review.empty.projectPrefix') }} <code>require_review: true</code>{{ t('review.empty.projectHint') }}
          </p>
          <p class="empty-line">
            {{ t('review.empty.cli') }} <code>gofer job review &lt;id&gt;</code>
          </p>
        </template>
      </div>
    </div>

    <RejectDialog
      v-if="rejectTarget"
      :submitting="rejecting"
      :error="rejectError"
      @close="rejectTarget = null"
      @confirm="onReject"
    />

    <div v-if="toast" class="rq-toast" role="status" @click="toast = null">
      <span>{{ toast.text }}</span>
      <RouterLink
        v-if="toast.jobId"
        class="rq-toast-link mono"
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
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--s3);
  flex-wrap: wrap;
  margin-bottom: var(--s3);
}
.title {
  font-size: var(--fs-xl);
  letter-spacing: -0.015em;
  color: var(--fg);
  margin: 0;
}
.subtitle {
  color: var(--fg-2);
  font-size: var(--fs-sm);
  margin: var(--s1) 0 0;
}
/* 待批入口：黄色只代表「需要人」（§3.1），与顶栏「待我决策」同一形态。 */
.rq-approvals {
  display: inline-block;
  min-height: 28px;
  margin-top: var(--s2);
  padding: 4px var(--s2);
  border: 1px solid var(--need);
  border-radius: var(--r-ctl);
  background: var(--need-soft);
  color: var(--need-ink);
  font-size: var(--fs-sm);
  font-weight: 600;
  text-decoration: none;
}
.head-actions {
  display: flex;
  align-items: center;
  gap: var(--s2);
  font-size: var(--fs-sm);
  flex: none;
}
.head-btn {
  min-height: 32px;
  padding: 0 var(--s3);
  background: transparent;
  color: var(--fg);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.head-btn:hover:not(:disabled) {
  color: var(--accent);
  border-color: var(--accent);
}
.head-btn:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.head-btn:disabled {
  color: var(--fg-3);
  cursor: default;
  opacity: 0.65;
}
.filter-bar {
  display: flex;
  align-items: center;
  gap: var(--s2);
  flex-wrap: wrap;
  margin-bottom: var(--s3);
}
.error {
  color: var(--fail);
  font-size: var(--fs-sm);
}

/* 表：窄屏横向滚动（列多，压缩到不可读不如让它滚）；手机改卡片（见 @media）。 */
.table {
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  overflow-x: auto;
  background: var(--surface);
}
.thead,
.trow {
  display: grid;
  grid-template-columns:
    minmax(260px, 2fr) minmax(72px, 0.7fr) minmax(70px, 0.6fr) minmax(110px, 0.9fr)
    62px 48px minmax(120px, 1fr) 66px 132px;
  align-items: center;
  gap: var(--s2);
  padding: var(--s2) var(--s3);
  min-width: 980px;
}
.thead {
  color: var(--fg-3);
  font-size: var(--fs-xs);
  border-bottom: 1px solid var(--line);
}
.trow {
  border-bottom: 1px solid var(--line);
  font-size: var(--fs-sm);
  cursor: pointer;
  transition: opacity var(--dur-slow) ease, background var(--dur-fast) ease;
}
.trow:last-child {
  border-bottom: none;
}
.trow:hover {
  background: var(--sunken);
}
.trow:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: -2px;
}
/* 已裁决：淡出（列表下一拍移除）。 */
.trow--leaving {
  opacity: 0;
  pointer-events: none;
}

.c-job {
  display: flex;
  align-items: center;
  gap: var(--s2);
  min-width: 0;
}
.job-title {
  flex: 1 1 auto;
  min-width: 0;
  color: var(--fg);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.c-job--titled .job-id {
  flex: none;
}
.job-id {
  color: var(--fg-3);
  font-size: var(--fs-xs);
}
.c-proj,
.c-agent,
.c-plan,
.c-commits,
.c-wait {
  color: var(--fg-3);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.c-usage {
  color: var(--fg);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.plan-link {
  color: var(--accent);
}

/* verify 徽标：passed 绿 / failed·timeout 红 / skipped 或无结果 灰。 */
.verify-badge {
  font-size: var(--fs-xs);
}
.verify-badge.verify--ok {
  color: var(--done);
}
.verify-badge.verify--bad {
  color: var(--fail);
}
.verify-badge.verify--skip {
  color: var(--idle);
}

.c-actions {
  display: flex;
  gap: var(--s1);
  align-items: center;
}
/* 不能裁决时（只读身份 / 无审阅权限 / 受限身份审自己的 job）：按钮位置换一行灰字解释。 */
.rq-blocked {
  color: var(--fg-3);
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}
.rq-btn {
  min-height: 26px;
  border-radius: var(--r-ctl);
  padding: 2px var(--s2);
  font: inherit;
  font-size: var(--fs-xs);
  border: 1px solid var(--line-strong);
  background: var(--surface);
  color: var(--fg);
  cursor: pointer;
}
.rq-btn--go {
  border-color: var(--accent);
  color: var(--accent);
}
.rq-btn--go:hover:not(:disabled) {
  background: var(--accent);
  color: var(--accent-ink);
}
.rq-btn--no {
  border-color: var(--fail);
  color: var(--fail);
}
.rq-btn--no:hover:not(:disabled) {
  background: var(--fail);
  color: var(--surface);
}
.rq-btn:disabled {
  opacity: 0.6;
  cursor: default;
}

.empty {
  padding: var(--s6) var(--s4);
  color: var(--fg-3);
  font-size: var(--fs-sm);
  min-width: 480px;
}
.empty-line {
  margin: var(--s1) 0;
}
.empty code {
  color: var(--accent);
  background: var(--sunken);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 0 var(--s1);
}

/* 裁决结果/失败提示：右下角浮层，点一下关掉，8s 自动消失。 */
.rq-toast {
  position: fixed;
  right: var(--s4);
  bottom: var(--s4);
  z-index: 50;
  display: flex;
  align-items: center;
  gap: var(--s2);
  max-width: 460px;
  padding: var(--s2) var(--s3);
  background: var(--surface);
  border: 1px solid var(--accent);
  border-radius: var(--r-panel);
  box-shadow: var(--shadow-pop);
  color: var(--fg);
  font-size: var(--fs-sm);
  cursor: pointer;
}
.rq-toast-link {
  color: var(--accent);
  flex: none;
}

/* 手机（≤760，设计稿 §4.3）：每行一张卡片，裁决按钮等宽铺满；列头隐藏。 */
@media (max-width: 760px) {
  .rq-head {
    margin-bottom: var(--s2);
  }
  .title {
    font-size: var(--fs-2xl);
  }
  .table {
    border: none;
    background: none;
    overflow: visible;
  }
  .thead {
    display: none;
  }
  .trow {
    display: flex;
    flex-wrap: wrap;
    gap: var(--s1) var(--s3);
    min-width: 0;
    margin-bottom: var(--s2);
    padding: var(--s3);
    border: 1px solid var(--line);
    border-radius: var(--r-panel);
    background: var(--surface);
    font-size: var(--fs-body-mobile);
  }
  .trow:last-child {
    border-bottom: 1px solid var(--line);
  }
  .c-job {
    flex: 1 1 auto;
    order: 1;
  }
  .c-wait {
    order: 2;
    flex: none;
  }
  .c-proj,
  .c-agent,
  .c-plan,
  .c-verify,
  .c-commits,
  .c-usage {
    order: 3;
    flex: none;
  }
  .c-actions {
    order: 4;
    flex: 1 1 100%;
    gap: var(--s2);
    margin-top: var(--s1);
  }
  .rq-btn {
    flex: 1 1 0;
    min-height: 40px;
    font-size: var(--fs-md);
  }
  .empty {
    min-width: 0;
    padding: var(--s4);
  }
  .rq-toast {
    left: var(--s2);
    right: var(--s2);
    bottom: var(--s2);
    max-width: none;
  }
}
</style>
