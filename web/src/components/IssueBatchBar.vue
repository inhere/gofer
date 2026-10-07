<script setup lang="ts">
// Issues 批量操作栏：已选计数 + 关闭 / 改状态 / 加标签，执行前统一弹确认框。
// 只负责收集意图并 emit；真正请求与选中状态由 Issues.vue 持有。
import { computed, ref } from 'vue'
import type { TrackerBatchSet } from '../api/types'

const props = defineProps<{ count: number; sampleIds: string[]; running: boolean }>()
const emit = defineEmits<{
  (e: 'clear'): void
  (e: 'run', set: TrackerBatchSet): void
}>()

type Kind = 'close' | 'status' | 'tag'
const status = ref('open')
const tags = ref('')
const pending = ref<Kind | null>(null)
const reason = ref('')

const tagList = computed(() => tags.value.split(/[,\s，]+/).map((t) => t.trim()).filter(Boolean))
const title = computed(() => pending.value === 'close' ? '批量关闭' : pending.value === 'status' ? '批量改状态' : '批量加标签')
const detail = computed(() => {
  if (pending.value === 'close') return `将 ${props.count} 个 issue 标记为 closed`
  if (pending.value === 'status') return `将 ${props.count} 个 issue 的状态改为 ${status.value}`
  return `给 ${props.count} 个 issue 添加标签：${tagList.value.join('、')}`
})

function ask(kind: Kind): void {
  pending.value = kind
  reason.value = ''
}
function confirm(): void {
  const kind = pending.value
  if (!kind) return
  pending.value = null
  if (kind === 'close') emit('run', { status: 'closed', ...(reason.value.trim() ? { close_reason: reason.value.trim() } : {}) })
  else if (kind === 'status') emit('run', { status: status.value })
  else emit('run', { add_tags: tagList.value })
}
</script>

<template>
  <div class="batch-bar mono" data-test="batch-bar">
    <span class="batch-count" data-test="batch-count">已选 {{ count }} 项</span>
    <button type="button" class="batch-link" data-test="batch-clear" @click="emit('clear')">清空</button>
    <span class="batch-sep"></span>
    <button type="button" class="batch-btn danger" :disabled="running" data-test="batch-close" @click="ask('close')">批量关闭</button>
    <span class="batch-group">
      <select v-model="status" class="batch-input" aria-label="目标状态"><option value="open">open</option><option value="in_progress">in_progress</option><option value="blocked">blocked</option></select>
      <button type="button" class="batch-btn" :disabled="running" data-test="batch-status" @click="ask('status')">改状态</button>
    </span>
    <span class="batch-group">
      <input v-model="tags" class="batch-input" placeholder="标签，逗号分隔" aria-label="要添加的标签" />
      <button type="button" class="batch-btn" :disabled="running || tagList.length === 0" data-test="batch-tag" @click="ask('tag')">加标签</button>
    </span>
  </div>

  <div v-if="pending" class="bb-overlay" data-test="batch-confirm" @click.self="pending = null">
    <div class="bb-panel" role="dialog" aria-modal="true" :aria-label="title">
      <p class="bb-title mono">{{ title }}</p>
      <p class="bb-detail mono">{{ detail }}</p>
      <p class="bb-ids mono">{{ sampleIds.join('、') }}<span v-if="count > sampleIds.length"> 等 {{ count }} 项</span></p>
      <label v-if="pending === 'close'" class="bb-field mono"><span>关闭原因（可选，所有条目统一）</span>
        <input v-model="reason" class="batch-input" placeholder="例如：已过时" data-test="batch-reason" @keydown.enter="confirm" />
      </label>
      <p class="bb-note mono">逐条执行；个别失败不影响其它条目。变更会在下次 gofer repo sync 时同步回仓库。</p>
      <div class="bb-actions">
        <button type="button" class="batch-btn" data-test="batch-cancel" @click="pending = null">取消</button>
        <button type="button" class="batch-btn primary" data-test="batch-confirm-ok" @click="confirm">确认执行</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.batch-bar{position:sticky;top:0;z-index:5;display:flex;align-items:center;flex-wrap:wrap;gap:8px 10px;padding:8px 12px;border:1px solid var(--phosphor);border-radius:var(--radius);background:var(--panel);margin-bottom:10px;font-size:12px}
.batch-count{color:var(--phosphor);font-weight:600}
.batch-link{background:transparent;border:0;color:var(--queue);text-decoration:underline;padding:2px;font:inherit;cursor:pointer}
.batch-sep{flex:1 1 0;min-width:0}
.batch-group{display:inline-flex;gap:4px;align-items:center;min-width:0}
.batch-input{background:var(--panel);color:var(--paper);border:1px solid var(--line);border-radius:var(--radius);padding:5px 8px;font-size:12px;min-width:0;max-width:100%}
.batch-input:focus{border-color:var(--phosphor);outline:none}
.batch-btn{border:1px solid var(--line);border-radius:var(--radius);background:transparent;color:var(--phosphor);padding:5px 10px;font:inherit;cursor:pointer}
.batch-btn:disabled{opacity:.5;cursor:not-allowed}
.batch-btn.danger{color:var(--fail);border-color:var(--fail)}
.batch-btn.primary{background:var(--phosphor);color:var(--ink);border-color:var(--phosphor);font-weight:600}
.bb-overlay{position:fixed;inset:0;z-index:50;background:rgba(0,0,0,.55);display:flex;align-items:center;justify-content:center;padding:16px}
.bb-panel{width:min(460px,100%);background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);padding:16px;display:flex;flex-direction:column;gap:10px;max-height:90vh;overflow:auto}
.bb-title{margin:0;color:var(--paper);font-size:14px}.bb-detail{margin:0;color:var(--paper);font-size:12px}
.bb-ids,.bb-note{margin:0;color:var(--queue);font-size:11px;overflow-wrap:anywhere}
.bb-field{display:flex;flex-direction:column;gap:5px;color:var(--queue);font-size:11px}
.bb-actions{display:flex;justify-content:flex-end;gap:8px}
@media (max-width:639px){.batch-bar{gap:6px}.batch-group{flex:1 1 100%}.batch-group .batch-input{flex:1 1 auto}}
</style>
