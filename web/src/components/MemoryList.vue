<script setup lang="ts">
// 记忆列表：key、类型、摘要、年龄与服务端 doctor 标记（P4）；点一行在右侧抽屉看全文并编辑
// （抽屉是 components/MemoryDrawer.vue，P6-记忆页第二版）。「全部项目」范围下每行另标来源。
import type { SourcedMemoryRow } from '../utils/memories'
import { memorySourceKey, memorySourceLabel } from '../utils/memories'
import type { MemoryDoctorFinding } from '../api/types'
import { doctorLabel, fmtTrackerTime, MEMORY_KIND_LABEL, memoryAge, memoryKind, memorySummary } from '../utils/trackerView'
import { t } from '../i18n'

const props = defineProps<{
  rows: SourcedMemoryRow[]
  doctor?: Record<string, MemoryDoctorFinding[]>
  // 合并视图（全部项目）下才显示来源列。
  showSource?: boolean
  // 抽屉里正打开的那条（高亮用）。
  activeKey?: string
}>()
const emit = defineEmits<{ (event: 'open', row: SourcedMemoryRow): void }>()

function findings(row: SourcedMemoryRow): MemoryDoctorFinding[] {
  return props.doctor?.[memorySourceKey(row)] ?? []
}
</script>

<template>
  <section class="memory-list" :class="{ 'memory-list--sourced': showSource }">
    <article v-for="row in rows" :key="memorySourceKey(row)" class="memory-card" :class="{ picked: activeKey === memorySourceKey(row) }" data-test="memory-row">
      <button type="button" class="memory-head" @click="emit('open', row)">
        <span class="memory-keycell">
          <span class="mono memory-key">{{ row.data.key || row.id }}</span>
          <span class="memory-kind mono" :class="`memory-kind--${memoryKind(row.data)}`" data-test="memory-kind">{{ MEMORY_KIND_LABEL[memoryKind(row.data)] }}</span>
        </span>
        <span class="memory-main">
          <span class="memory-summary" data-test="memory-summary">{{ memorySummary(row.data) || '—' }}</span>
          <span v-if="findings(row).length" class="memory-flags">
            <span v-for="f in findings(row)" :key="f.slug" class="memory-flag mono" data-test="memory-flag" :title="f.detail || ''">⚠ {{ doctorLabel(f.slug) }}</span>
          </span>
        </span>
        <span v-if="showSource" class="memory-source" data-test="memory-source">{{ memorySourceLabel(row.source) }}</span>
        <span class="memory-tags">{{ (row.data.tags ?? []).join(' · ') || t('memory.tagsNone') }}</span>
        <span class="mono memory-time" :title="fmtTrackerTime(row.data.updated_at || row.updated_at)"><span data-test="memory-age">{{ memoryAge(row.data.updated_at || row.updated_at) }}</span> · {{ row.data.by || '—' }}</span>
      </button>
    </article>
    <div v-if="rows.length === 0" class="empty-panel mono">{{ t('memory.empty') }}</div>
  </section>
</template>

<style scoped>
.memory-card{border:1px solid var(--line);border-radius:var(--r-chip);background:var(--surface);padding:10px 12px;margin-bottom:8px}.memory-card.picked{border-color:var(--accent)}.memory-head{width:100%;display:grid;grid-template-columns:190px minmax(180px,1fr) 120px 150px;align-items:center;gap:12px;color:var(--ink);background:transparent;border:0;text-align:left;cursor:pointer}.memory-list--sourced .memory-head{grid-template-columns:190px minmax(180px,1fr) 160px 120px 150px}.memory-keycell{display:flex;align-items:center;gap:6px;min-width:0}.memory-key{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.memory-kind{flex:none;font-size:10px;border:1px solid var(--line);border-radius:999px;padding:1px 6px;color:var(--ink-3)}.memory-kind--rule{color:var(--accent);border-color:var(--accent)}.memory-kind--handoff{color:var(--run);border-color:var(--run)}.memory-main{display:flex;flex-direction:column;gap:3px;min-width:0}.memory-summary{color:var(--ink-3);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.memory-flags{display:flex;flex-wrap:wrap;gap:4px}.memory-flag{font-size:10px;color:var(--run);border:1px solid var(--run);border-radius:999px;padding:0 6px}.memory-source,.memory-tags,.memory-time{color:var(--ink-3);font-size:11px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.empty-panel{display:flex;justify-content:center;padding:28px;color:var(--ink-3)}
@media (max-width:760px){.memory-head,.memory-list--sourced .memory-head{grid-template-columns:minmax(0,1fr);gap:4px}.memory-summary{white-space:normal;overflow-wrap:anywhere}.memory-tags{display:none}.memory-time{font-size:10px}}
</style>
