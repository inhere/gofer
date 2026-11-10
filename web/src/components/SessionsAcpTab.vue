<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { listWorkbenchThreads } from '../api/workbench'
import type { WorkItem, WorkbenchThread } from '../api/types'
import { fmtAgo, fmtDateTime } from '../api/time'
import InfoCard from './InfoCard.vue'
import StatusPill from '../ui/StatusPill.vue'
import { toneFamily } from '../utils/sessionState'
import { toggleExpanded } from '../utils/cardExpand'
import {
  acpThreadCountLabel,
  acpThreadIsEnded,
  acpThreadJobLink,
  acpThreadLink,
  acpThreadStatusKey,
  acpThreadStatusValue,
  acpThreadTone,
} from '../utils/acpThreads'
import { runnerLabel } from '../utils/runnerDisplay'
import { t } from '../i18n'

const props = withDefaults(defineProps<{
  project?: string
  showEnded: boolean
  readOnly?: boolean
  statusFilter?: string
  workBySession: ReadonlyMap<string, WorkItem>
  linkableItems: WorkItem[]
  linkBusy?: string
  linkError?: string
}>(), {
  project: '',
  readOnly: false,
  statusFilter: '',
  linkBusy: '',
  linkError: '',
})

const emit = defineEmits<{
  (event: 'update:showEnded', value: boolean): void
  (event: 'threads', value: WorkbenchThread[]): void
  (event: 'count', count: number, hasMore: boolean): void
  (event: 'link-work', value: { jobID: string; workID: string }): void
}>()

const acpSessions = ref<WorkbenchThread[]>([])
const acpLoading = ref(false)
const acpError = ref('')
const acpCursor = ref('')
const acpHasMore = ref(false)
const expandedAcp = ref<Set<string>>(new Set())
const linkPick = ref<Record<string, string>>({})

const filteredAcp = computed(() => acpSessions.value.filter((thread) => {
  const filter = props.statusFilter
  if (!filter) return true
  const state = acpThreadStatusValue(thread)
  if (filter === 'ended') return state === 'ended'
  if (filter === 'active') return state !== 'ended'
  return state === filter
}))

function flattenThreads(resp: { projects: { threads: WorkbenchThread[] }[] }): WorkbenchThread[] {
  return resp.projects.flatMap((group) => group.threads ?? [])
}

async function loadAcpSessions(reset = true): Promise<void> {
  acpLoading.value = true
  if (reset) {
    acpError.value = ''
  }
  try {
    const resp = await listWorkbenchThreads({
      source: 'acp',
      since: 1,
      limit: 50,
      live: props.showEnded ? undefined : true,
      project: props.project || undefined,
      cursor: reset ? undefined : acpCursor.value || undefined,
    })
    const rows = flattenThreads(resp)
    const next = reset ? rows : [...acpSessions.value, ...rows.filter((row) => !acpSessions.value.some((old) => old.id === row.id))]
    acpSessions.value = next
    acpCursor.value = resp.next_cursor ?? ''
    acpHasMore.value = !!resp.has_more && !!resp.next_cursor
    expandedAcp.value = new Set([...expandedAcp.value].filter((id) => next.some((row) => row.id === id)))
    emit('threads', next)
    emit('count', next.length, acpHasMore.value)
    acpError.value = ''
  } catch (e) {
    acpError.value = e instanceof Error ? e.message : String(e)
  } finally {
    acpLoading.value = false
  }
}

function workForJob(jobID?: string): WorkItem | undefined {
  return jobID ? props.workBySession.get(jobID) : undefined
}

function linkWork(jobID: string): void {
  const workID = linkPick.value[jobID]
  if (workID) emit('link-work', { jobID, workID })
}

function showTime(sec: number): string {
  return fmtDateTime(sec)
}

function updateShowEnded(event: Event): void {
  emit('update:showEnded', (event.target as HTMLInputElement).checked)
}

onMounted(() => { void loadAcpSessions() })
watch(() => [props.project, props.showEnded], () => { void loadAcpSessions() })
</script>

<template>
  <section class="group" data-test="acp-session-tab">
    <header class="group-head">
      <div class="controls">
        <label class="check">
          <input :checked="showEnded" type="checkbox" @change="updateShowEnded" />
          {{ t('sessions.showEnded') }}
        </label>
        <span v-if="acpSessions.length" class="group-count" data-test="acp-count">{{ acpThreadCountLabel(acpSessions.length, acpHasMore) }}</span>
      </div>
      <span class="grow"></span>
      <button class="act" type="button" :disabled="acpLoading" @click="loadAcpSessions()">
        {{ acpLoading ? t('sessions.refreshing') : t('sessions.refresh') }}
      </button>
    </header>
    <p v-if="acpError" class="error mono">{{ acpError }}</p>
    <p v-if="linkError" class="error mono" data-test="link-error">{{ linkError }}</p>
    <div v-if="filteredAcp.length > 0" class="icard-grid" data-test="acp-cards">
      <InfoCard
        v-for="item in filteredAcp"
        :key="item.id"
        :tone="acpThreadTone(item)"
        :dim="acpThreadIsEnded(item)"
        :expanded="expandedAcp.has(item.id)"
        :card-id="item.id"
        @toggle="expandedAcp = toggleExpanded(expandedAcp, item.id)"
      >
        <template #title>{{ item.title || item.id }}</template>
        <template #badges><StatusPill :family="toneFamily(acpThreadTone(item))" :label="t(acpThreadStatusKey(item))" /></template>
        <template #meta>
          <span class="mono">{{ item.agent || '—' }}</span>
          <span class="mono">{{ item.project_key || '—' }}</span>
          <span class="mono">{{ runnerLabel(item.runner) || '—' }}</span>
          <span class="mono">{{ t('sessions.card.turns') }} {{ item.turns }}</span>
          <span class="mono" :title="showTime(item.updated_at)">{{ fmtAgo(item.updated_at) }}</span>
          <RouterLink
            v-if="workForJob(item.latest_job_id)"
            class="icard-chip mono"
            data-test="acp-work-link"
            :to="`/work?id=${encodeURIComponent(workForJob(item.latest_job_id)!.id)}`"
            :title="t('sessions.card.workItemTitle', { title: workForJob(item.latest_job_id)!.title })"
          >{{ t('sessions.card.workItem', { title: workForJob(item.latest_job_id)!.title }) }}</RouterLink>
        </template>
        <template #actions>
          <!-- ACP source=acp; this deep link opens the session in the workbench: /workbench?thread=... -->
          <RouterLink class="icard-btn icard-btn--primary mono" :to="acpThreadLink(item.id)" data-test="acp-open-session">{{ t('sessions.acp.openSession') }}</RouterLink>
          <RouterLink v-if="item.latest_job_id" class="icard-btn mono" :to="acpThreadJobLink(item.latest_job_id)" data-test="acp-open-job">{{ t('sessions.acp.openJob') }}</RouterLink>
        </template>
        <template #details>
          <dl class="icard-kv mono">
            <dt>{{ t('sessions.card.job') }}</dt>
            <dd><RouterLink v-if="item.latest_job_id" :to="acpThreadJobLink(item.latest_job_id)">{{ item.latest_job_id }}</RouterLink><template v-else>—</template></dd>
            <dt>{{ t('sessions.card.started') }}</dt><dd>{{ showTime(item.started_at) }}</dd>
            <dt>{{ t('sessions.card.lastReply') }}</dt><dd>{{ showTime(item.updated_at) }}</dd>
            <template v-if="!workForJob(item.latest_job_id) && linkableItems.length && item.latest_job_id">
              <dt>{{ t('sessions.card.linkWork') }}</dt>
              <dd class="link-pick">
                <select v-model="linkPick[item.latest_job_id]" class="mono" data-test="acp-link-pick"><option value="">{{ t('sessions.card.pickWork') }}</option><option v-for="w in linkableItems" :key="w.id" :value="w.id">{{ w.title }}</option></select>
                <button class="icard-btn mono" type="button" :disabled="!linkPick[item.latest_job_id] || linkBusy === item.latest_job_id" data-test="acp-link-btn" @click="linkWork(item.latest_job_id)">{{ t('sessions.card.link') }}</button>
              </dd>
            </template>
          </dl>
        </template>
      </InfoCard>
    </div>
    <button v-if="acpHasMore" class="act" type="button" :disabled="acpLoading" data-test="acp-load-more" @click="loadAcpSessions(false)">
      {{ acpLoading ? t('sessions.acp.loadingMore') : t('sessions.acp.loadMore') }}
    </button>
    <div v-else-if="!acpLoading && !acpError && filteredAcp.length === 0" class="empty">{{ t('sessions.empty.acp') }}</div>
  </section>
</template>
