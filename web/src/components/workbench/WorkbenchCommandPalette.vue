<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import type { WorkbenchThread } from '../../api/types'

const props = defineProps<{
  open: boolean
  threads: WorkbenchThread[]
  current?: WorkbenchThread
  projects: string[]
}>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'select', thread: WorkbenchThread): void
  (e: 'new'): void
  (e: 'switch-project', project: string): void
  (e: 'action', action: 'stop' | 'continue' | 'detail' | 'split-h' | 'split-v' | 'close-pane' | 'maximize' | 'new-tab'): void
}>()
const query = ref('')
const input = ref<HTMLInputElement | null>(null)
const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return props.threads.slice(0, 12)
  return props.threads.filter((thread) => [thread.title, thread.id, thread.project_key, thread.agent ?? ''].some((value) => value.toLowerCase().includes(q))).slice(0, 20)
})

watch(() => props.open, (open) => {
  if (!open) return
  query.value = ''
  void nextTick(() => input.value?.focus())
})

function choose(thread: WorkbenchThread): void {
  emit('select', thread)
  emit('close')
}
</script>

<template>
  <div v-if="open" class="palette-overlay" @click.self="emit('close')">
    <section class="palette" role="dialog" aria-label="命令面板">
      <input ref="input" v-model="query" class="palette-query mono" placeholder="跳会话 / 切项目 / 当前动作" @keydown.esc="emit('close')" />
      <div class="quick-actions">
        <button type="button" @click="emit('new'); emit('close')">＋ 新会话</button>
        <button v-if="current" type="button" @click="emit('action', 'stop'); emit('close')">停止当前</button>
        <button v-if="current?.resumable" type="button" @click="emit('action', 'continue'); emit('close')">续接当前</button>
        <button v-if="current?.latest_job_id" type="button" @click="emit('action', 'detail'); emit('close')">打开详情</button>
        <button type="button" @click="emit('action', 'split-h'); emit('close')">分屏左右</button>
        <button type="button" @click="emit('action', 'split-v'); emit('close')">分屏上下</button>
        <button type="button" @click="emit('action', 'close-pane'); emit('close')">关闭窗格</button>
        <button type="button" @click="emit('action', 'maximize'); emit('close')">最大化/还原</button>
        <button type="button" @click="emit('action', 'new-tab'); emit('close')">新标签</button>
      </div>
      <div class="palette-section">
        <h2 class="mono">会话</h2>
        <button v-for="thread in filtered" :key="thread.id" type="button" class="palette-row" @click="choose(thread)">
          <span>{{ thread.title }}</span><small class="mono">{{ thread.project_key }} · {{ thread.status }}</small>
        </button>
      </div>
      <div class="palette-section project-chips">
        <h2 class="mono">切项目</h2>
        <button type="button" @click="emit('switch-project', ''); emit('close')">全部</button>
        <button v-for="project in projects" :key="project" type="button" @click="emit('switch-project', project); emit('close')">{{ project || '未归属' }}</button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.palette-overlay { position: fixed; inset: 0; z-index: 100; display: grid; place-items: start center; padding-top: 12vh; background: rgba(0,0,0,.62); }
.palette { width: min(680px, 92vw); max-height: 72vh; overflow-y: auto; background: var(--panel); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 18px 50px rgba(0,0,0,.45); padding: 12px; }
.palette-query { width: 100%; color: var(--paper); background: var(--ink); border: 1px solid var(--phosphor); border-radius: var(--radius); padding: 10px; }
.quick-actions { display: flex; flex-wrap: wrap; gap: 7px; padding: 10px 0; }
.quick-actions button, .project-chips button { color: var(--paper); background: var(--ink); border: 1px solid var(--line); border-radius: var(--radius); padding: 6px 9px; }
.palette-section { border-top: 1px solid var(--line); padding-top: 8px; }
.palette-section h2 { color: var(--queue); font-size: 10px; text-transform: uppercase; letter-spacing: .08em; }
.palette-row { width: 100%; display: flex; justify-content: space-between; gap: 10px; color: var(--paper); background: transparent; border: 0; border-top: 1px solid rgba(255,255,255,.03); padding: 8px; text-align: left; }
.palette-row:hover { background: rgba(255,255,255,.05); }
.palette-row small { color: var(--queue); }
.project-chips { display: flex; flex-wrap: wrap; gap: 6px; }
.project-chips h2 { width: 100%; }
</style>
