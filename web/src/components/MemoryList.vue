<script setup lang="ts">
// 记忆列表：key、类型、摘要、年龄与服务端 doctor 标记（P4）；点开看全文并编辑。
import MarkdownBlock from './MarkdownBlock.vue'
import type { MemoryDoctorFinding, TrackerMemory } from '../api/types'
import { doctorLabel, fmtTrackerTime, MEMORY_KIND_LABEL, memoryAge, memoryKind, memorySummary } from '../utils/trackerView'

type Row = TrackerMemory & { data: TrackerMemory['body'] }
const props = defineProps<{ rows: Row[]; selected: Row | null; draft: string; doctor?: Record<string, MemoryDoctorFinding[]> }>()
const emit = defineEmits<{ (event: 'open', row: Row): void; (event: 'update:draft', value: string): void; (event: 'save'): void; (event: 'remove', row: Row): void }>()

function keyOf(row: Row): string {
  return row.data.key || row.id
}
function findings(row: Row): MemoryDoctorFinding[] {
  return props.doctor?.[keyOf(row)] ?? []
}
</script>

<template>
  <section class="memory-list">
    <article v-for="row in rows" :key="row.id" class="memory-card" data-test="memory-row">
      <button type="button" class="memory-head" @click="emit('open', row)">
        <span class="memory-keycell">
          <span class="mono memory-key">{{ keyOf(row) }}</span>
          <span class="memory-kind mono" :class="`memory-kind--${memoryKind(row.data)}`" data-test="memory-kind">{{ MEMORY_KIND_LABEL[memoryKind(row.data)] }}</span>
        </span>
        <span class="memory-main">
          <span class="memory-summary" data-test="memory-summary">{{ memorySummary(row.data) || '—' }}</span>
          <span v-if="findings(row).length" class="memory-flags">
            <span v-for="f in findings(row)" :key="f.slug" class="memory-flag mono" data-test="memory-flag" :title="f.detail || ''">⚠ {{ doctorLabel(f.slug) }}</span>
          </span>
        </span>
        <span class="memory-tags">{{ (row.data.tags ?? []).join(' · ') || '无标签' }}</span>
        <span class="mono memory-time" :title="fmtTrackerTime(row.data.updated_at || row.updated_at)"><span data-test="memory-age">{{ memoryAge(row.data.updated_at || row.updated_at) }}</span> · {{ row.data.by || '—' }}</span>
      </button>
      <div v-if="selected?.id === row.id" class="memory-expanded">
        <p v-for="f in findings(row)" :key="f.slug" class="memory-finding mono">⚠ {{ doctorLabel(f.slug) }}<template v-if="f.detail">：{{ f.detail }}</template></p>
        <MarkdownBlock :text="String(row.data.content ?? '')" />
        <textarea :value="draft" class="editor-input" rows="6" @input="emit('update:draft', ($event.target as HTMLTextAreaElement).value)" />
        <div class="actions"><button class="primary-btn" type="button" @click="emit('save')">保存</button><button class="secondary-btn" type="button" @click="emit('remove', row)">删除</button></div>
      </div>
    </article>
    <div v-if="rows.length === 0" class="empty-panel mono">暂无 memory</div>
  </section>
</template>

<style scoped>
.memory-card{border:1px solid var(--line);border-radius:var(--radius);background:var(--panel);padding:10px 12px;margin-bottom:8px}.memory-head{width:100%;display:grid;grid-template-columns:190px minmax(180px,1fr) 120px 150px;align-items:center;gap:12px;color:var(--paper);background:transparent;border:0;text-align:left}.memory-keycell{display:flex;align-items:center;gap:6px;min-width:0}.memory-key{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.memory-kind{flex:none;font-size:10px;border:1px solid var(--line);border-radius:999px;padding:1px 6px;color:var(--queue)}.memory-kind--rule{color:var(--phosphor);border-color:var(--phosphor)}.memory-kind--handoff{color:var(--run);border-color:var(--run)}.memory-main{display:flex;flex-direction:column;gap:3px;min-width:0}.memory-summary{color:var(--queue);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.memory-flags{display:flex;flex-wrap:wrap;gap:4px}.memory-flag{font-size:10px;color:var(--run);border:1px solid var(--run);border-radius:999px;padding:0 6px}.memory-tags,.memory-time{color:var(--queue);font-size:11px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.memory-expanded{margin-top:10px}.memory-finding{color:var(--run);font-size:11px;margin:0 0 6px;overflow-wrap:anywhere}.editor-input{width:100%;background:var(--panel);color:var(--paper);border:1px solid var(--line);border-radius:var(--radius);padding:6px 8px}.actions{display:flex;gap:8px;margin-top:10px}.primary-btn,.secondary-btn{border-radius:var(--radius);padding:5px 10px;font-size:12px}.primary-btn{background:var(--phosphor);color:var(--ink);border:1px solid var(--phosphor)}.secondary-btn{background:transparent;color:var(--phosphor);border:1px solid var(--line)}.empty-panel{display:flex;justify-content:center;padding:28px;color:var(--queue)}
@media (max-width:639px){.memory-head{grid-template-columns:minmax(0,1fr);gap:4px}.memory-summary{white-space:normal;overflow-wrap:anywhere}.memory-tags{display:none}.memory-time{font-size:10px}}
</style>
