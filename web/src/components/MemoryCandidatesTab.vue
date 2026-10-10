<script setup lang="ts">
// 验收面板「经验」页签（gofer-3nxa.2）：本 job 汇报「## 可复用经验」的条目。每条待处理的候选
// 可填 key / kind（默认 note）接受 → 写成该 job 项目作用域的记忆（来源 job:<id>）；或拒绝（只改状态）。
// 已有同名 key 的记忆不会被覆盖（服务端 409），换个 key 再接受。
import { ref } from 'vue'
import { acceptMemoryCandidate, rejectMemoryCandidate } from '../api/memoryCandidates'
import type { MemoryCandidate } from '../api/memoryCandidates'

const props = defineProps<{ items: MemoryCandidate[] }>()
const emit = defineEmits<{ (e: 'changed'): void }>()

const keys = ref<Record<number, string>>({})
const kinds = ref<Record<number, 'note' | 'rule'>>({})
const busy = ref(-1)
const errors = ref<Record<number, string>>({})

async function decide(c: MemoryCandidate, accept: boolean): Promise<void> {
  if (busy.value >= 0) {
    return
  }
  const key = (keys.value[c.id] ?? '').trim()
  if (accept && key === '') {
    errors.value = { ...errors.value, [c.id]: '先填 key' }
    return
  }
  busy.value = c.id
  errors.value = { ...errors.value, [c.id]: '' }
  try {
    if (accept) {
      await acceptMemoryCandidate(c.id, { key, kind: kinds.value[c.id] ?? 'note' })
    } else {
      await rejectMemoryCandidate(c.id)
    }
    emit('changed')
  } catch (e) {
    errors.value = { ...errors.value, [c.id]: e instanceof Error ? e.message : String(e) }
  } finally {
    busy.value = -1
  }
}

function statusLabel(c: MemoryCandidate): string {
  return c.status === 'accepted' ? `已接受 → ${c.memory_key ?? ''}` : '已拒绝'
}
</script>

<template>
  <div>
    <p class="mc-note mono">
      agent 汇报的跨任务经验；接受后写成本 job 项目的记忆（kind 默认 note），拒绝不留痕。不会自动入库。
    </p>
    <ul class="mc-list">
      <li v-for="c in props.items" :key="c.id" class="mc-item">
        <span class="mc-text">{{ c.text }}</span>
        <div v-if="c.status === 'pending'" class="mc-form">
          <input
            v-model="keys[c.id]"
            class="mc-key mono"
            type="text"
            placeholder="memory key"
            :disabled="busy >= 0"
            @keydown.enter="decide(c, true)"
          />
          <select v-model="kinds[c.id]" class="mc-kind mono" :disabled="busy >= 0">
            <option value="note">note</option>
            <option value="rule">rule</option>
          </select>
          <button class="mc-btn mono" type="button" :disabled="busy >= 0" @click="decide(c, true)">
            {{ busy === c.id ? '处理中…' : '接受' }}
          </button>
          <button class="mc-btn mc-btn--ghost mono" type="button" :disabled="busy >= 0" @click="decide(c, false)">
            拒绝
          </button>
          <span v-if="errors[c.id]" class="mc-err mono">{{ errors[c.id] }}</span>
        </div>
        <span v-else class="mc-done mono">{{ statusLabel(c) }}</span>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.mc-note {
  color: var(--queue);
  font-size: 11px;
  margin: 0 0 8px;
}
.mc-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.mc-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 6px 0;
  border-bottom: 1px dashed var(--line);
}
.mc-text {
  white-space: pre-wrap;
}
.mc-form {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.mc-key {
  min-width: 180px;
  font-size: 12px;
}
.mc-kind {
  font-size: 12px;
}
.mc-btn {
  font-size: 11px;
  padding: 2px 8px;
  border: 1px solid var(--phosphor);
  background: transparent;
  color: var(--phosphor);
  border-radius: var(--radius);
  cursor: pointer;
}
.mc-btn--ghost {
  border-color: var(--line);
  color: var(--queue);
}
.mc-btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.mc-err {
  color: var(--fail);
  font-size: 11px;
}
.mc-done {
  color: var(--queue);
  font-size: 11px;
}
</style>
