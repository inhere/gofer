<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { listJobs } from '../api/client'
import { fmtDateTime, fmtDuration } from '../api/time'
import type { Job } from '../api/types'
import ProjectFilter from '../ui/ProjectFilter.vue'
import StatusPill from '../ui/StatusPill.vue'
import { useProjectFilter } from '../ui/projectFilter'
import { t, te } from '../i18n'
import { decisionTone, holdOriginLabel, holdSubmittedAt } from '../utils/approvals'

const PAGE_SIZE = 50
const route = useRoute()
const router = useRouter()
const { project, setProject, options: projectOptions, loadOptions: loadProjectOptions, loadError: projectLoadError } =
  useProjectFilter('/approvals')

const tab = computed<'pending' | 'decided'>(() => (route.query.tab === 'decided' ? 'decided' : 'pending'))
const jobs = ref<Job[]>([])
const loading = ref(false)
const error = ref('')
const offset = ref(0)
const hasMore = ref(false)
const nowSec = ref(Math.floor(Date.now() / 1000))
let clockTimer: number | null = null

const summary = computed(() => t('jobs.approvals.h.summary', { n: jobs.value.length }))

function selectTab(next: 'pending' | 'decided'): void {
  const query: Record<string, string> = {}
  if (project.value) query.project = project.value
  if (next === 'decided') query.tab = next
  void router.push({ path: '/approvals', query })
}

async function loadPage(append: boolean): Promise<void> {
  if (loading.value) return
  loading.value = true
  try {
    const pageOffset = append ? offset.value : 0
    const resp = await listJobs({
      hold: tab.value,
      project: project.value || undefined,
      limit: PAGE_SIZE,
      offset: pageOffset,
    })
    const next = resp.jobs ?? []
    jobs.value = append ? [...jobs.value, ...next] : next
    offset.value = pageOffset + next.length
    hasMore.value = next.length === PAGE_SIZE
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function resetAndLoad(): void {
  offset.value = 0
  jobs.value = []
  hasMore.value = false
  void loadPage(false)
}

function titleOf(job: Job): string {
  return job.title?.trim() || job.id
}

function requestText(job: Job): string {
  const hold = job.hold
  if (hold?.command?.length) return hold.command.join(' ')
  return hold?.prompt_preview?.trim() || t('jobs.approvals.noRequest')
}

function decisionLabel(decision: string | undefined): string {
  const key = `jobs.approvals.decision.${decision ?? ''}`
  return decision && te(key) ? t(key) : decision || t('jobs.approvals.noDecision')
}

function remainingText(job: Job): string {
  const expiresAt = job.hold?.expires_at
  if (!expiresAt) return t('jobs.approvals.noDecision')
  const remaining = expiresAt - nowSec.value
  if (remaining <= 0) return t('jobs.approvals.remainingExpired')
  return t('jobs.approvals.remaining', { time: fmtDuration(remaining) })
}

watch([tab, project], () => resetAndLoad())

onMounted(() => {
  void loadProjectOptions()
  void loadPage(false)
  clockTimer = window.setInterval(() => {
    nowSec.value = Math.floor(Date.now() / 1000)
  }, 1000)
})

onUnmounted(() => {
  if (clockTimer != null) window.clearInterval(clockTimer)
})
</script>

<template>
  <main class="approvals page">
    <header class="approvals-head">
      <div>
        <h1>{{ t('jobs.approvals.h.title') }}</h1>
        <p class="approvals-summary mono">{{ summary }}</p>
      </div>
      <button class="head-btn mono" type="button" :disabled="loading" @click="resetAndLoad">
        {{ loading ? t('jobs.approvals.loading') : t('act.refresh') }}
      </button>
    </header>

    <div class="approvals-toolbar">
      <ProjectFilter
        :model-value="project"
        :options="projectOptions"
        :loading="!projectOptions.length && !projectLoadError"
        :error="projectLoadError"
        @update:model-value="setProject"
      />
      <div class="approvals-tabs" role="tablist" :aria-label="t('jobs.approvals.h.title')">
        <button
          class="approvals-tab mono"
          :class="{ 'approvals-tab--active': tab === 'pending' }"
          type="button"
          role="tab"
          :aria-selected="tab === 'pending'"
          @click="selectTab('pending')"
        >{{ t('jobs.approvals.tab.pending') }}</button>
        <button
          class="approvals-tab mono"
          :class="{ 'approvals-tab--active': tab === 'decided' }"
          type="button"
          role="tab"
          :aria-selected="tab === 'decided'"
          @click="selectTab('decided')"
        >{{ t('jobs.approvals.tab.decided') }}</button>
      </div>
    </div>

    <p v-if="error" class="approvals-error mono" role="alert">{{ error }}</p>
    <div v-if="loading && !jobs.length" class="approvals-empty mono">{{ t('jobs.approvals.loading') }}</div>
    <div v-else-if="!jobs.length" class="approvals-empty mono">{{ t('jobs.approvals.empty') }}</div>

    <section v-else class="approvals-list" :aria-label="t('jobs.approvals.h.title')">
      <div class="approval-row approval-row--head mono">
        <span>{{ t('jobs.approvals.col.job') }}</span>
        <span>{{ t('jobs.approvals.col.project') }}</span>
        <span>{{ t('jobs.approvals.col.source') }}</span>
        <span>{{ t('jobs.approvals.col.submitted') }}</span>
        <span>{{ t('jobs.approvals.col.reason') }}</span>
        <span>{{ t('jobs.approvals.col.request') }}</span>
        <span>{{ tab === 'pending' ? t('jobs.approvals.col.remaining') : t('jobs.approvals.col.result') }}</span>
      </div>
      <article v-for="job in jobs" :key="job.id" class="approval-row">
        <div class="approval-cell approval-job">
          <router-link class="approval-title" :to="`/jobs/${encodeURIComponent(job.id)}`">{{ titleOf(job) }}</router-link>
          <span class="approval-id mono">{{ job.id }}</span>
        </div>
        <div class="approval-cell mono" :data-label="t('jobs.approvals.col.project')">{{ job.project_key }}</div>
        <div class="approval-cell mono" :data-label="t('jobs.approvals.col.source')">{{ holdOriginLabel(job) || t('jobs.approvals.noDecision') }}</div>
        <div class="approval-cell mono" :data-label="t('jobs.approvals.col.submitted')">{{ fmtDateTime(holdSubmittedAt(job)) }}</div>
        <div class="approval-cell" :data-label="t('jobs.approvals.col.reason')" :title="job.hold?.reason || undefined">
          {{ job.hold?.reason || t('jobs.approvals.noReason') }}
        </div>
        <div class="approval-cell approval-request mono" :data-label="t('jobs.approvals.col.request')" :title="requestText(job)">
          {{ requestText(job) }}
        </div>
        <div v-if="tab === 'pending'" class="approval-cell mono" :data-label="t('jobs.approvals.col.remaining')">
          {{ remainingText(job) }}
        </div>
        <div v-else class="approval-cell approval-decision" :data-label="t('jobs.approvals.col.result')">
          <span class="decision-badge mono" :class="`k-${decisionTone(job.hold?.decision)}`">
            {{ decisionLabel(job.hold?.decision) }}
          </span>
          <span v-if="job.hold?.decided_by" class="approval-detail mono">{{ job.hold.decided_by }}</span>
          <span v-if="job.hold?.decided_at" class="approval-detail mono">{{ fmtDateTime(job.hold.decided_at) }}</span>
          <span v-if="job.hold?.note" class="approval-detail approval-note" :title="job.hold.note">{{ job.hold.note }}</span>
          <span v-if="job.hold?.decision === 'approved'" class="approval-detail approval-current mono">
            {{ t('jobs.approvals.col.currentStatus') }}: <StatusPill :status="job.status" />
          </span>
        </div>
      </article>
    </section>

    <button v-if="hasMore" class="load-more mono" type="button" :disabled="loading" @click="loadPage(true)">
      {{ loading ? t('jobs.approvals.loading') : t('jobs.approvals.loadMore') }}
    </button>
  </main>
</template>

<style scoped>
.approvals {
  min-width: 0;
}
.approvals-head,
.approvals-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--s4);
}
.approvals-head {
  margin-bottom: var(--s4);
}
.approvals-head h1 {
  margin: 0;
}
.approvals-summary {
  margin: var(--s1) 0 0;
  color: var(--ink-3);
}
.approvals-toolbar {
  justify-content: flex-start;
  margin-bottom: var(--s4);
  flex-wrap: wrap;
}
.approvals-tabs {
  display: flex;
  gap: var(--s1);
  border-bottom: 1px solid var(--line);
}
.approvals-tab,
.load-more,
.head-btn {
  min-height: 36px;
  padding: 0 var(--s3);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  background: var(--surface);
  color: var(--ink-2);
  cursor: pointer;
}
.approvals-tab {
  border: 0;
  border-bottom: 2px solid transparent;
  border-radius: 0;
}
.approvals-tab--active {
  border-bottom-color: var(--accent);
  color: var(--ink);
}
.approvals-tab:hover,
.load-more:hover,
.head-btn:hover {
  color: var(--ink);
  border-color: var(--accent);
}
.approvals-tab:disabled,
.load-more:disabled,
.head-btn:disabled {
  cursor: wait;
  opacity: 0.6;
}
.approvals-error {
  padding: var(--s3);
  color: var(--fail);
  background: var(--fail-soft);
  border-radius: var(--r-panel);
}
.approvals-empty {
  padding: var(--s6) var(--s3);
  color: var(--ink-3);
  text-align: center;
}
.approvals-list {
  overflow: auto;
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
}
.approval-row {
  display: grid;
  grid-template-columns: minmax(160px, 1.2fr) minmax(100px, 0.8fr) minmax(130px, 1fr) minmax(130px, 1fr) minmax(160px, 1.2fr) minmax(220px, 1.7fr) minmax(150px, 1fr);
  gap: var(--s3);
  align-items: start;
  min-width: 1100px;
  padding: var(--s3);
  border-top: 1px solid var(--line);
}
.approval-row--head {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  border-top: 0;
  background: var(--surface-2);
}
.approval-cell {
  min-width: 0;
  overflow-wrap: anywhere;
}
.approval-job {
  display: flex;
  flex-direction: column;
  gap: var(--s1);
}
.approval-title {
  color: var(--accent);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.approval-id,
.approval-detail {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.approval-request,
.approval-note {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.approval-decision {
  display: flex;
  flex-wrap: wrap;
  gap: var(--s1) var(--s2);
  align-items: center;
}
.decision-badge {
  padding: 2px var(--s2);
  color: var(--c, var(--ink-2));
  background: var(--soft, var(--surface-2));
  border-radius: 999px;
}
.approval-current {
  width: 100%;
}
.load-more {
  display: block;
  margin: var(--s4) auto 0;
}
@media (max-width: 760px) {
  .approvals-head {
    align-items: flex-start;
  }
  .approvals-head,
  .approvals-toolbar {
    flex-direction: column;
    align-items: stretch;
  }
  .approvals-list {
    overflow: visible;
    border: 0;
  }
  .approval-row--head {
    display: none;
  }
  .approval-row {
    display: flex;
    flex-direction: column;
    min-width: 0;
    margin-bottom: var(--s3);
    border: 1px solid var(--line);
    border-radius: var(--r-panel);
  }
  .approval-cell {
    width: 100%;
  }
  .approval-cell[data-label]::before {
    display: block;
    margin-bottom: var(--s1);
    color: var(--ink-3);
    content: attr(data-label);
    font-size: var(--fs-xs);
  }
  .approval-job::before {
    display: none;
  }
}
</style>
