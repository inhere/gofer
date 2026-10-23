<script setup lang="ts">
// 工作台右侧上下文栏（设计稿 §7，≥1280 显示）：会话信息、轮数、未提交数、关联任务、
// 改动文件、计划、权限，以及「过程 / 改动」切换与停止按钮。只读展示 + 三个动作。
import { computed, onUnmounted, ref, watch } from 'vue'
import { getJob } from '../../api/client'
import type { Job, WorkbenchThread } from '../../api/types'
import { t } from '../../i18n'
import { formatNumber, formatRelative } from '../../utils/format'
import StatusPill from '../../ui/StatusPill.vue'

const props = defineProps<{ thread?: WorkbenchThread; view: 'process' | 'changes'; canStop: boolean; stopping: boolean }>()
const emit = defineEmits<{
  (e: 'show-view', view: 'process' | 'changes'): void
  (e: 'stop'): void
  (e: 'open-job', id: string): void
}>()

const job = ref<Job | null>(null)
let loadSeq = 0

watch(
  () => props.thread?.latest_job_id ?? '',
  async (id) => {
    const seq = ++loadSeq
    job.value = null
    if (!id) return
    try {
      const result = await getJob(id)
      if (seq === loadSeq) job.value = result
    } catch {
      // 上下文栏是补充信息：取不到就不显示任务段，不影响会话本身。
    }
  },
  { immediate: true },
)
onUnmounted(() => { loadSeq++ })

const turns = computed(() => props.thread?.turns ?? 0)
const usage = computed(() => {
  const total = props.thread?.usage?.total_tokens ?? 0
  const cost = props.thread?.usage?.cost_usd ?? 0
  if (!total && !cost) return '—'
  return `${formatNumber(total)} tok${cost ? ` · $${cost.toFixed(4)}` : ''}`
})
const uncommitted = computed(() => job.value?.uncommitted_count ?? 0)
const files = computed(() => job.value?.uncommitted_files ?? [])
const pending = computed(() => props.thread?.pending_interactions?.length ?? 0)
const planID = computed(() => job.value?.plan_id ?? '')
const todoID = computed(() => job.value?.todo_id ?? '')
</script>

<template>
  <aside class="ctx-panel" :aria-label="t('workbench.ctx.label')" data-test="workbench-context">
    <section class="ctx-sec">
      <h3>{{ t('workbench.ctx.title') }}</h3>
      <dl class="kv">
        <dt>ID</dt>
        <dd class="mono ellipsis" :title="thread?.id">{{ thread?.id || '—' }}</dd>
        <dt>{{ t('workbench.ctx.agent') }}</dt>
        <dd>{{ thread?.agent || thread?.kind || '—' }}</dd>
        <dt>{{ t('workbench.ctx.project') }}</dt>
        <dd>{{ thread?.project_key || '—' }}</dd>
        <dt>{{ t('workbench.ctx.cwd') }}</dt>
        <dd class="mono ellipsis" :title="thread?.cwd">{{ thread?.cwd || '—' }}</dd>
        <dt>{{ t('workbench.ctx.kind') }}</dt>
        <dd>{{ thread?.kind || '—' }}</dd>
        <dt>{{ t('workbench.ctx.turns') }}</dt>
        <dd class="num">{{ turns }}</dd>
        <dt>{{ t('workbench.ctx.usage') }}</dt>
        <dd class="num">{{ usage }}</dd>
        <dt>{{ t('workbench.ctx.started') }}</dt>
        <dd>{{ thread?.started_at ? formatRelative(thread.started_at) : '—' }}</dd>
      </dl>
      <div v-if="thread" class="state-row">
        <StatusPill :status="thread.status" />
        <span v-if="uncommitted > 0" class="need-chip">{{ t('workbench.ctx.uncommitted', { n: uncommitted }) }}</span>
      </div>
      <div class="ctx-actions">
        <div class="seg" :aria-label="t('workbench.view.label')">
          <button
            type="button"
            :aria-pressed="view === 'process'"
            :class="{ on: view === 'process' }"
            @click="emit('show-view', 'process')"
          >{{ t('workbench.view.process') }}</button>
          <button
            type="button"
            :aria-pressed="view === 'changes'"
            :class="{ on: view === 'changes' }"
            @click="emit('show-view', 'changes')"
          >{{ t('workbench.view.changes') }}</button>
        </div>
        <button class="btn danger sm" type="button" :disabled="!canStop || stopping" data-test="ctx-stop" @click="emit('stop')">
          {{ stopping ? t('workbench.head.stopping') : t('workbench.ctx.stop') }}
        </button>
      </div>
    </section>

    <section class="ctx-sec">
      <h3>{{ t('workbench.ctx.job') }}</h3>
      <button
        v-if="thread?.latest_job_id"
        class="job-link"
        type="button"
        data-test="ctx-job"
        @click="emit('open-job', thread.latest_job_id)"
      >
        <span class="mono ellipsis">{{ thread.latest_job_id }}</span>
        <span v-if="job" class="job-status">{{ job.status }}</span>
      </button>
      <p v-else class="ctx-empty">{{ t('workbench.ctx.noJob') }}</p>
      <p v-if="planID" class="ctx-line mono" :title="todoID || planID">
        <span class="ctx-key">{{ t('workbench.ctx.plan') }}</span>
        {{ planID }}<template v-if="todoID"> / {{ todoID }}</template>
      </p>
    </section>

    <section class="ctx-sec">
      <h3>{{ t('workbench.ctx.files') }}</h3>
      <ul v-if="files.length" class="files">
        <li v-for="file in files" :key="file" class="mono ellipsis" :title="file">{{ file }}</li>
      </ul>
      <p v-else class="ctx-empty">{{ t('workbench.ctx.noFiles') }}</p>
      <p v-if="uncommitted > files.length" class="ctx-note">{{ t('workbench.ctx.uncommitted', { n: uncommitted }) }}</p>
    </section>

    <section class="ctx-sec">
      <h3>{{ t('workbench.ctx.permissions') }}</h3>
      <p v-if="pending > 0" class="ctx-line need-chip">{{ t('workbench.ctx.pending', { n: pending }) }}</p>
      <p v-else class="ctx-empty">{{ t('workbench.ctx.none') }}</p>
    </section>
  </aside>
</template>

<style scoped>
.ctx-panel {
  min-width: 0;
  height: 100%;
  overflow-y: auto;
  padding: var(--s3);
  background: var(--surface-2);
  border-left: 1px solid var(--line);
  display: flex;
  flex-direction: column;
  gap: var(--s4);
}
.ctx-sec h3 {
  margin: 0 0 var(--s2);
  color: var(--ink-3);
  font-size: var(--fs-xs);
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
}
.kv {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  gap: 4px var(--s2);
  margin: 0;
  font-size: var(--fs-sm);
}
.kv dt {
  color: var(--ink-3);
}
.kv dd {
  margin: 0;
  color: var(--ink);
  min-width: 0;
}
.num {
  font-variant-numeric: tabular-nums;
}
.ellipsis {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.state-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--s1);
  margin-top: var(--s2);
}
.need-chip {
  padding: 0 6px;
  border-radius: var(--r-chip);
  background: var(--need-soft);
  color: var(--need-ink);
  font-size: var(--fs-xs);
}
.ctx-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s2);
  margin-top: var(--s3);
}
.seg {
  display: inline-flex;
  padding: 2px;
  background: var(--sunken);
  border-radius: var(--r-ctl);
}
.seg button {
  min-height: 26px;
  padding: 0 var(--s2);
  background: none;
  border: 0;
  border-radius: var(--r-chip);
  color: var(--ink-3);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.seg button.on {
  background: var(--surface);
  color: var(--ink);
  box-shadow: var(--shadow-1);
}
.seg button:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.job-link {
  width: 100%;
  display: flex;
  align-items: center;
  gap: var(--s1);
  padding: var(--s1) var(--s2);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  color: var(--accent);
  font: inherit;
  font-size: var(--fs-sm);
  text-align: left;
  cursor: pointer;
}
.job-link:hover {
  border-color: var(--accent);
}
.job-status {
  flex: none;
  margin-left: auto;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.files {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 3px;
  font-size: var(--fs-xs);
  color: var(--ink-2);
}
.ctx-empty {
  margin: 0;
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.ctx-note {
  margin: var(--s1) 0 0;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.ctx-line {
  margin: var(--s1) 0 0;
  font-size: var(--fs-sm);
  color: var(--ink-2);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ctx-key {
  color: var(--ink-3);
  margin-right: var(--s1);
}
</style>
