<script setup lang="ts">
import { ref } from 'vue'
import type { WorkbenchProjectGroup, WorkbenchStatus, WorkbenchThread } from '../../api/types'
import { WORKBENCH_THREAD_DRAG_TYPE } from './layoutTree'
import { threadStatusPresentation } from './statusPresentation'

defineProps<{
  projects: WorkbenchProjectGroup[]
  selectedId: string
  query: string
  projectFilter: string
  statusFilter: WorkbenchStatus | ''
  projectOptions: string[]
  showExec: boolean
  hiddenExecCount: number
}>()

const emit = defineEmits<{
  (e: 'select', thread: WorkbenchThread): void
  (e: 'update:query', value: string): void
  (e: 'update:projectFilter', value: string): void
  (e: 'update:statusFilter', value: WorkbenchStatus | ''): void
  (e: 'update:showExec', value: boolean): void
  (e: 'new'): void
}>()

const collapsed = ref<Set<string>>(new Set())
const searchInput = ref<HTMLInputElement | null>(null)

function toggleProject(key: string): void {
  const next = new Set(collapsed.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  collapsed.value = next
}

function onQuery(event: Event): void {
  emit('update:query', (event.target as HTMLInputElement).value)
}

function onProject(event: Event): void {
  emit('update:projectFilter', (event.target as HTMLSelectElement).value)
}

function onStatus(event: Event): void {
  emit('update:statusFilter', (event.target as HTMLSelectElement).value as WorkbenchStatus | '')
}

function ago(ts: number): string {
  const seconds = Math.max(0, Math.floor(Date.now() / 1000) - ts)
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`
  return `${Math.floor(seconds / 86400)}d`
}

function usage(thread: WorkbenchThread): string {
  const tokens = thread.usage?.total_tokens ?? 0
  if (tokens <= 0) return ''
  return tokens >= 1000 ? `${(tokens / 1000).toFixed(1)}k` : String(tokens)
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
  <aside class="wb-sidebar" aria-label="工作台会话列表">
    <div class="sidebar-filters">
      <button class="new-thread mono" type="button" title="发起新会话（Ctrl/Cmd+K → 新会话）" @click="emit('new')">＋ 新会话</button>
      <input
        ref="searchInput"
        class="filter-input mono"
        type="search"
        :value="query"
        placeholder="/ 搜索会话"
        aria-label="搜索会话"
        @input="onQuery"
      />
      <div class="filter-row">
        <select class="filter-select mono" :value="projectFilter" aria-label="筛选项目" @change="onProject">
          <option value="">全部项目</option>
          <option v-for="key in projectOptions" :key="key" :value="key">{{ key || '未归属' }}</option>
        </select>
        <select class="filter-select mono" :value="statusFilter" aria-label="筛选状态" @change="onStatus">
          <option value="">全部状态</option>
          <option value="blocked">blocked</option>
          <option value="working">working</option>
          <option value="awaiting_input">等待输入</option>
          <option value="review">review</option>
          <option value="done">done</option>
          <option value="idle">idle</option>
        </select>
      </div>
      <label class="exec-toggle mono">
        <input type="checkbox" :checked="showExec" @change="emit('update:showExec', ($event.target as HTMLInputElement).checked)" />
        显示 exec 命令会话<span v-if="!showExec && hiddenExecCount > 0">（已隐藏 {{ hiddenExecCount }}）</span>
      </label>
    </div>

    <div class="project-list">
      <section v-for="project in projects" :key="project.project_key" class="project-group">
        <button class="project-row mono" type="button" @click="toggleProject(project.project_key)">
          <span class="caret">{{ collapsed.has(project.project_key) ? '▸' : '▾' }}</span>
          <span class="status-dot" :class="`status--${project.status}`"></span>
          <span class="project-name">{{ project.project_key || '未归属' }}</span>
          <span class="project-count">{{ project.counts.total }}</span>
          <span v-if="project.counts.orphan_blocked" class="orphan-count">+{{ project.counts.orphan_blocked }} 待处理</span>
        </button>
        <div v-if="!collapsed.has(project.project_key)" class="thread-list">
          <button
            v-for="thread in project.threads"
            :key="thread.id"
            class="thread-row"
            :class="{ selected: selectedId === thread.id }"
            type="button"
            draggable="true"
            @click="emit('select', thread)"
            @dragstart="startDrag($event, thread)"
          >
            <span class="status-dot" :class="[`status--${threadStatusPresentation(thread.status).tone}`, { stalled: thread.stalled }]" :title="thread.stalled ? '疑似卡住' : threadStatusPresentation(thread.status).label"></span>
            <span class="thread-main">
              <span class="thread-title" :title="thread.title">{{ thread.pinned ? '⌖ ' : '' }}{{ thread.title }}</span>
              <span class="thread-meta mono">
                {{ thread.agent || thread.kind }}
                <template v-if="thread.turns > 1"> · {{ thread.turns }} turns</template>
              </span>
            </span>
            <span class="thread-side mono">
              <span>{{ ago(thread.updated_at) }}</span>
              <span v-if="usage(thread)">{{ usage(thread) }} tok</span>
            </span>
          </button>
          <p v-if="project.threads.length === 0" class="empty mono">无匹配会话</p>
        </div>
      </section>
      <p v-if="projects.length === 0" class="empty mono">暂无匹配会话</p>
    </div>
  </aside>
</template>

<style scoped>
.wb-sidebar { min-width: 0; height: 100%; display: flex; flex-direction: column; background: var(--panel); border-right: 1px solid var(--line); }
.sidebar-filters { padding: 10px; border-bottom: 1px solid var(--line); }
.new-thread { width: 100%; margin-bottom: 8px; padding: 7px 8px; color: var(--ink); background: var(--phosphor); border: 1px solid var(--phosphor); border-radius: var(--radius); font-weight: 700; cursor: pointer; }
.new-thread:hover { opacity: .9; }
.filter-input, .filter-select { width: 100%; color: var(--paper); background: var(--ink); border: 1px solid var(--line); border-radius: var(--radius); padding: 7px 8px; }
.filter-input:focus, .filter-select:focus { outline: 1px solid var(--phosphor); border-color: var(--phosphor); }
.filter-row { display: grid; grid-template-columns: 1fr 1fr; gap: 6px; margin-top: 7px; }
.filter-select { min-width: 0; font-size: 11px; }
.exec-toggle { display: flex; align-items: center; gap: 6px; margin-top: 7px; font-size: 11px; color: var(--muted, #8a97a3); cursor: pointer; }
.project-list { flex: 1; min-height: 0; overflow-y: auto; overscroll-behavior: contain; }
.project-group { border-bottom: 1px solid var(--line); }
.project-row { width: 100%; display: flex; align-items: center; gap: 7px; padding: 8px 10px; color: var(--paper); background: transparent; border: 0; text-align: left; }
.project-row:hover { background: rgba(255,255,255,.035); }
.caret { color: var(--queue); width: 10px; }
.project-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.project-count { color: var(--queue); }
.orphan-count { color: var(--run); font-size: 10px; }
.thread-row { width: 100%; display: grid; grid-template-columns: 10px minmax(0,1fr) auto; gap: 8px; align-items: center; padding: 8px 10px 8px 24px; color: var(--paper); background: transparent; border: 0; border-top: 1px solid rgba(255,255,255,.025); text-align: left; }
.thread-row:hover { background: rgba(255,255,255,.045); }
.thread-row.selected { background: rgba(79,176,198,.12); box-shadow: inset 2px 0 var(--phosphor); }
.thread-main { min-width: 0; display: flex; flex-direction: column; gap: 3px; }
.thread-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
.thread-meta, .thread-side { color: var(--queue); font-size: 10px; }
.thread-side { display: flex; flex-direction: column; align-items: flex-end; gap: 3px; }
.status-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--queue); flex: none; }
.status--attention { background: var(--fail); }
.status--active { background: var(--phosphor); }
.status--done { background: var(--done); }
.status--neutral { background: var(--queue); }
.status-dot.stalled { box-shadow: 0 0 0 2px var(--run); }
.empty { color: var(--queue); padding: 14px; margin: 0; font-size: 11px; }
</style>
