<script setup lang="ts">
import MarkdownBlock from './MarkdownBlock.vue'
import type { TrackerMemory } from '../api/types'
import { fmtTrackerTime } from '../utils/trackerView'

type Row = TrackerMemory & { data: TrackerMemory['body'] }
defineProps<{ rows: Row[]; selected: Row | null; draft: string }>()
const emit = defineEmits<{ (event: 'open', row: Row): void; (event: 'update:draft', value: string): void; (event: 'save'): void; (event: 'remove', row: Row): void }>()
</script>

<template>
  <section class="memory-list">
    <article v-for="row in rows" :key="row.id" class="memory-card">
      <button type="button" class="memory-head" @click="emit('open', row)">
        <span class="mono memory-key">{{ row.data.key || row.id }}</span>
        <span class="memory-summary">{{ String(row.data.content ?? '').replace(/\s+/g, ' ').slice(0, 180) }}</span>
        <span class="memory-tags">{{ (row.data.tags ?? []).join(' · ') || '无标签' }}</span>
        <span class="mono memory-time">{{ fmtTrackerTime(row.data.updated_at || row.updated_at) }} · {{ row.data.by || '—' }}</span>
      </button>
      <div v-if="selected?.id === row.id" class="memory-expanded">
        <MarkdownBlock :text="String(row.data.content ?? '')" />
        <textarea :value="draft" class="editor-input" rows="6" @input="emit('update:draft', ($event.target as HTMLTextAreaElement).value)" />
        <div class="actions"><button class="primary-btn" type="button" @click="emit('save')">保存</button><button class="secondary-btn" type="button" @click="emit('remove', row)">删除</button></div>
      </div>
    </article>
    <div v-if="rows.length === 0" class="empty-panel mono">暂无 memory</div>
  </section>
</template>

<style scoped>
.memory-card{border:1px solid var(--line);border-radius:var(--radius);background:var(--panel);padding:10px 12px;margin-bottom:8px}.memory-head{width:100%;display:grid;grid-template-columns:150px minmax(180px,1fr) 120px 180px;align-items:center;gap:12px;color:var(--paper);background:transparent;border:0;text-align:left}.memory-summary{color:var(--queue);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.memory-tags,.memory-time{color:var(--queue);font-size:11px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.memory-expanded{margin-top:10px}.editor-input{width:100%;background:var(--panel);color:var(--paper);border:1px solid var(--line);border-radius:var(--radius);padding:6px 8px}.actions{display:flex;gap:8px;margin-top:10px}.primary-btn,.secondary-btn{border-radius:var(--radius);padding:5px 10px;font-size:12px}.primary-btn{background:var(--phosphor);color:var(--ink);border:1px solid var(--phosphor)}.secondary-btn{background:transparent;color:var(--phosphor);border:1px solid var(--line)}.empty-panel{display:flex;justify-content:center;padding:28px;color:var(--queue)}
@media (max-width:639px){.memory-head{grid-template-columns:100px minmax(120px,1fr)}.memory-tags,.memory-time{display:none}}
</style>
