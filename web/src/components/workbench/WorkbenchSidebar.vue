<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import type { WorkbenchStatus, WorkbenchThread } from '../../api/types'
import { t } from '../../i18n'
import { formatDuration, formatRelative } from '../../utils/format'
import FilterChips from '../../ui/FilterChips.vue'
import ProjectFilter from '../../ui/ProjectFilter.vue'
import StatusGlyph from '../../ui/StatusGlyph.vue'
import { WORKBENCH_THREAD_DRAG_TYPE } from './layoutTree'
import { groupOfThread, groupThreads, THREAD_GROUP_ORDER, threadGroupLabelKey } from './threadGroups'

const props = defineProps<{
  threads: WorkbenchThread[]
  selectedId: string
  query: string
  projectFilter: string
  statusFilter: WorkbenchStatus | ''
  projectOptions: string[]
  showExec: boolean
  hiddenExecCount: number
  // 服务端 counts.orphan_blocked / orphan_review：项目组里不在会话列表上的待处理项
  // （受阻计划 / 待决策）。0 = 不显示提示行。
  orphanWaiting: number
}>()

const emit = defineEmits<{
  (e: 'select', thread: WorkbenchThread): void
  (e: 'update:query', value: string): void
  (e: 'update:projectFilter', value: string): void
  (e: 'update:statusFilter', value: WorkbenchStatus | ''): void
  (e: 'update:showExec', value: boolean): void
  (e: 'new'): void
  (e: 'openOrphans'): void
}>()

// 已结束分组默认折叠（上百条历史会话不一次进场全建 DOM）。
const endedOpen = ref(false)
const searchInput = ref<HTMLInputElement | null>(null)

// nowSec 每分钟刷新一次，让「最近活跃」的会话到时自动移到「空闲与离线」。
const nowSec = ref(Math.floor(Date.now() / 1000))
let nowTimer: ReturnType<typeof setInterval> | undefined
onMounted(() => { nowTimer = setInterval(() => { nowSec.value = Math.floor(Date.now() / 1000) }, 60_000) })
onUnmounted(() => { if (nowTimer) clearInterval(nowTimer) })
const grouped = computed(() => groupThreads(props.threads, nowSec.value))

function rowsOf(group: (typeof THREAD_GROUP_ORDER)[number]): WorkbenchThread[] {
  const rows = grouped.value[group]
  return group === 'ended' && !endedOpen.value ? [] : rows
}

const statusItems = computed(() => [
  { value: '', label: t('workbench.status.all') },
  { value: 'blocked', label: t('status.blocked') },
  { value: 'review', label: t('status.review') },
  { value: 'awaiting_input', label: t('status.awaiting_input') },
  { value: 'working', label: t('status.working') },
  { value: 'idle', label: t('status.idle') },
  { value: 'done', label: t('status.done') },
])

function turnsText(thread: WorkbenchThread): string {
  return thread.turns > 1 ? t('workbench.row.turns', { n: thread.turns }) : ''
}

function usageText(thread: WorkbenchThread): string {
  const tokens = thread.usage?.total_tokens ?? 0
  if (tokens <= 0) return ''
  return t('workbench.row.tokens', { n: formatNumber(tokens) })
}

function formatNumber(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
}

function durationText(thread: WorkbenchThread): string {
  const started = thread.started_at
  const ended = thread.status === 'done' ? thread.updated_at : Math.floor(Date.now() / 1000)
  if (!started || ended < started) return ''
  return formatDuration((ended - started) * 1000)
}

function focusSearch(): void {
  searchInput.value?.focus()
}

function startDrag(event: DragEvent, thread: WorkbenchThread): void {
  if (!event.dataTransfer) return
  event.dataTransfer.effectAllowed = 'copy'
  event.dataTransfer.setData(WORKBENCH_THREAD_DRAG_TYPE, thread.id)
  event.dataTransfer.setData('text/plain', thread.id)
}

defineExpose({ focusSearch })
</script>

<template>
  <aside class="wb-sidebar" :aria-label="t('workbench.list.label')">
    <div class="wb-list-head">
      <h2 class="wb-title">{{ t('workbench.list.title') }}</h2>
      <ProjectFilter
        :model-value="projectFilter"
        :options="projectOptions"
        @update:model-value="emit('update:projectFilter', $event)"
      />
      <button
        class="icon-btn new-thread"
        type="button"
        :title="t('workbench.list.newTitle')"
        :aria-label="t('workbench.list.new')"
        data-test="workbench-new"
        @click="emit('new')"
      >
        ＋
      </button>
    </div>

    <div class="wb-filters">
      <input
        ref="searchInput"
        class="input wb-search"
        type="search"
        :value="query"
        :placeholder="t('workbench.list.searchPlaceholder')"
        :aria-label="t('workbench.list.search')"
        @input="emit('update:query', ($event.target as HTMLInputElement).value)"
      />
      <FilterChips
        :items="statusItems"
        :model-value="statusFilter"
        :label="t('act.filter')"
        @update:model-value="emit('update:statusFilter', ($event as WorkbenchStatus | '') ?? '')"
      />
      <label class="exec-toggle">
        <input
          type="checkbox"
          :checked="showExec"
          @change="emit('update:showExec', ($event.target as HTMLInputElement).checked)"
        />
        {{ t('workbench.list.showExec') }}
        <span v-if="!showExec && hiddenExecCount > 0" class="exec-hidden">
          {{ t('workbench.list.hiddenExec', { n: hiddenExecCount }) }}
        </span>
      </label>
      <!-- 审阅意见：项目不再是分组，原来挂项目行上的「待处理」计数（受阱计划 / 待决策）
           改成列表头一行提示，点一下去看这些项。 -->
      <button
        v-if="orphanWaiting > 0"
        class="orphan-notice"
        type="button"
        :title="t('workbench.orphan.open')"
        data-test="orphan-notice"
        @click="emit('openOrphans')"
      >
        <StatusGlyph family="need" :size="12" />
        <span>{{ t('workbench.orphan.notice', { n: orphanWaiting }) }}</span>
      </button>
    </div>

    <div class="wb-sessions">
      <section v-for="group in THREAD_GROUP_ORDER" :key="group" class="sess-group" :data-group="group">
        <template v-if="group === 'ended'">
          <button
            v-if="grouped.ended.length"
            class="group-head ended-toggle"
            type="button"
            :aria-expanded="endedOpen"
            @click="endedOpen = !endedOpen"
          >
            <span class="caret" aria-hidden="true">{{ endedOpen ? '▾' : '▸' }}</span>
            <span class="group-name">{{ t(threadGroupLabelKey(group)) }}</span>
            <span class="group-count">{{ grouped.ended.length }}</span>
          </button>
        </template>
        <button v-else-if="grouped[group].length" class="group-head" type="button" disabled>
          <span class="group-name">{{ t(threadGroupLabelKey(group)) }}</span>
          <span class="group-count">{{ grouped[group].length }}</span>
        </button>
        <template v-if="group !== 'ended' || endedOpen">
          <button
            v-for="thread in rowsOf(group)"
            :key="thread.id"
            class="sess"
            :class="{ selected: selectedId === thread.id, 'sess--ended': group === 'ended' }"
            type="button"
            :aria-current="selectedId === thread.id ? 'true' : undefined"
            draggable="true"
            :data-thread-group="groupOfThread(thread, nowSec)"
            @click="emit('select', thread)"
            @dragstart="startDrag($event, thread)"
          >
            <StatusGlyph :status="thread.status" :label="t(`status.${thread.status}`)" :size="13" />
            <span class="nm" :title="thread.title">
              <span v-if="thread.pinned" class="pinned" aria-hidden="true">⌖</span>{{ thread.title }}
            </span>
            <span class="tm">{{ formatRelative(thread.updated_at) }}</span>
            <span class="ln">
              <span class="ln-agent">{{ thread.agent || thread.kind }}</span>
              <span v-if="turnsText(thread)" class="ln-meta">{{ turnsText(thread) }}</span>
              <span v-if="durationText(thread)" class="ln-meta">{{ durationText(thread) }}</span>
              <span v-if="usageText(thread)" class="ln-meta">{{ usageText(thread) }}</span>
              <span v-if="thread.stalled" class="ln-stalled" :title="t('workbench.stalled')">stalled</span>
              <span v-if="thread.status === 'awaiting_input' && !thread.seen_at" class="ln-new" aria-hidden="true">●</span>
            </span>
          </button>
        </template>
      </section>
      <p v-if="threads.length === 0" class="empty">{{ t('workbench.list.empty') }}</p>
    </div>
  </aside>
</template>

<style scoped>
.wb-sidebar {
  min-width: 0;
  height: 100%;
  display: flex;
  flex-direction: column;
  background: var(--surface-2);
  border-right: 1px solid var(--line);
}
.wb-list-head {
  display: flex;
  align-items: center;
  gap: var(--s2);
  padding: var(--s3) var(--s3) var(--s2);
}
.wb-title {
  flex: 1;
  min-width: 0;
  margin: 0;
  font-size: var(--fs-md);
  font-weight: 600;
  color: var(--fg);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.icon-btn {
  flex: none;
  display: grid;
  place-items: center;
  width: 32px;
  height: 32px;
  background: var(--surface);
  color: var(--fg);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-md);
  cursor: pointer;
}
.icon-btn:hover {
  border-color: var(--accent);
  color: var(--accent);
}
.icon-btn:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.wb-filters {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  padding: 0 var(--s3) var(--s2);
  border-bottom: 1px solid var(--line);
}
.wb-search {
  width: 100%;
  min-height: 32px;
  padding: 2px var(--s2);
  color: var(--fg);
  background: var(--surface);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
}
.wb-search:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.exec-toggle {
  display: flex;
  align-items: center;
  gap: var(--s1);
  color: var(--fg-3);
  font-size: var(--fs-sm);
  cursor: pointer;
}
.exec-hidden {
  color: var(--fg-3);
}
.orphan-notice {
  display: flex;
  align-items: center;
  gap: var(--s1);
  width: 100%;
  min-height: 28px;
  padding: 2px var(--s2);
  background: var(--need-soft);
  border: 1px solid transparent;
  border-radius: var(--r-chip);
  color: var(--need-ink);
  font: inherit;
  font-size: var(--fs-sm);
  text-align: left;
  cursor: pointer;
}
.orphan-notice:hover {
  border-color: var(--need);
}
.orphan-notice:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.wb-sessions {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding: 0 var(--s2) var(--s3);
}
.sess-group + .sess-group {
  margin-top: var(--s2);
}
.group-head {
  width: 100%;
  display: flex;
  align-items: center;
  gap: var(--s1);
  padding: var(--s1) var(--s2);
  background: none;
  border: 0;
  color: var(--fg-3);
  font: inherit;
  font-size: var(--fs-xs);
  letter-spacing: 0.06em;
  text-transform: uppercase;
  text-align: left;
}
.group-name {
  flex: 1;
}
.group-count {
  font-variant-numeric: tabular-nums;
}
.ended-toggle {
  cursor: pointer;
}
.ended-toggle:hover {
  color: var(--fg);
}
.caret {
  width: 10px;
}
.sess {
  width: 100%;
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  gap: 2px var(--s2);
  align-items: center;
  padding: var(--s2);
  background: none;
  border: 0;
  border-radius: var(--r-ctl);
  color: var(--fg);
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.sess:hover {
  background: var(--sunken);
}
.sess:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: -2px;
}
.sess.selected {
  background: var(--surface);
  box-shadow: var(--shadow-1), inset 0 0 0 1px var(--line);
}
.sess--ended {
  opacity: 0.78;
}
.nm {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: var(--fs-sm);
  font-weight: 500;
}
.pinned {
  margin-right: 3px;
  color: var(--accent);
}
.tm {
  color: var(--fg-3);
  font-size: var(--fs-xs);
  white-space: nowrap;
}
.ln {
  grid-column: 2 / 4;
  display: flex;
  align-items: center;
  gap: var(--s1);
  min-width: 0;
  color: var(--fg-3);
  font-size: var(--fs-xs);
}
.ln-agent {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ln-meta {
  flex: none;
  font-variant-numeric: tabular-nums;
}
.ln-stalled {
  flex: none;
  color: var(--need);
}
.ln-new {
  flex: none;
  color: var(--need);
  font-size: 8px;
}
.empty {
  margin: 0;
  padding: var(--s3);
  color: var(--fg-3);
  font-size: var(--fs-sm);
}
/* 手机：exec 开关收进抽屉的筛选区之外会挤，保持隐藏（旧行为）。 */
@media (max-width: 760px) {
  .exec-toggle {
    display: none;
  }
  .wb-list-head {
    padding: var(--s2);
  }
  .sess {
    min-height: 44px;
  }
}
</style>
