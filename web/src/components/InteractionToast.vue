<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import type { Interaction } from '../api/types'

const props = defineProps<{ interaction: Interaction }>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'goto'): void
}>()

const router = useRouter()

const promptLine = computed(() => {
  const first = props.interaction.prompt.split(/\r?\n/)[0]?.trim() ?? ''
  return first.length > 96 ? `${first.slice(0, 96)}...` : first
})

const shortJobId = computed(() => {
  const id = props.interaction.job_id
  return id.length > 10 ? `...${id.slice(-10)}` : id
})

function gotoJob(): void {
  emit('goto')
  void router.push(`/jobs/${encodeURIComponent(props.interaction.job_id)}`)
}
</script>

<template>
  <div class="toast" role="status" aria-live="polite">
    <button class="close" type="button" aria-label="关闭提示" @click="emit('close')">
      ×
    </button>
    <button class="body" type="button" @click="gotoJob">
      <span class="t1 mono">⚠ 新的人工介入请求 · needs_human</span>
      <span class="tx mono">
        job {{ shortJobId }} — {{ promptLine || '等待人工介入' }}
      </span>
    </button>
  </div>
</template>

<style scoped>
.toast {
  position: fixed;
  right: 18px;
  bottom: 18px;
  z-index: 70;
  width: min(340px, calc(100vw - 36px));
  background: var(--panel);
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.5);
  padding: 12px 14px;
}

.body {
  display: block;
  width: 100%;
  min-width: 0;
  text-align: left;
  background: transparent;
  border: none;
  color: var(--paper);
  padding: 0 18px 0 0;
}

.body:hover .tx {
  color: var(--phosphor);
}

.t1 {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--fail);
  font-size: 12px;
  line-height: 1.35;
}

.tx {
  display: block;
  margin-top: 6px;
  color: var(--paper);
  font-size: 13px;
  line-height: 1.45;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.close {
  position: absolute;
  top: 8px;
  right: 10px;
  z-index: 1;
  background: transparent;
  border: none;
  color: var(--queue);
  font-size: 16px;
  line-height: 1;
  padding: 2px;
}

.close:hover {
  color: var(--paper);
}
</style>
