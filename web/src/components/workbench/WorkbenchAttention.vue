<script setup lang="ts">
import { ref } from 'vue'
import type { WorkbenchAttentionItem } from '../../api/types'

defineProps<{ items: WorkbenchAttentionItem[] }>()
const emit = defineEmits<{ (e: 'select', item: WorkbenchAttentionItem): void }>()
const open = ref(false)

function choose(item: WorkbenchAttentionItem): void {
  open.value = false
  emit('select', item)
}

function ago(ts: number): string {
  const seconds = Math.max(0, Math.floor(Date.now() / 1000) - ts)
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`
  return `${Math.floor(seconds / 86400)}d`
}
</script>

<template>
  <div class="attention">
    <button class="attention-toggle mono" type="button" :aria-expanded="open" @click="open = !open">
      ⚠ 等你 {{ items.length }}
    </button>
    <div v-if="open" class="attention-menu">
      <p v-if="items.length === 0" class="empty mono">目前没有等待你处理的会话</p>
      <button v-for="item in items" :key="`${item.thread_id}:${item.action}`" class="attention-item" type="button" @click="choose(item)">
        <span class="item-title">{{ item.title }}</span>
        <span class="item-meta mono">{{ item.project_key || '未归属' }} · {{ item.action }} · 等待 {{ ago(item.waiting_since) }}</span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.attention { position: relative; }
.attention-toggle { color: var(--ink); background: var(--run); border: 1px solid var(--run); border-radius: var(--radius); padding: 6px 10px; font-weight: 700; white-space: nowrap; }
.attention-menu { position: absolute; right: 0; top: calc(100% + 6px); z-index: 50; width: min(420px, 90vw); max-height: 55vh; overflow-y: auto; background: var(--panel); border: 1px solid var(--line); border-radius: var(--radius); box-shadow: 0 12px 30px rgba(0,0,0,.35); }
.attention-item { width: 100%; display: flex; flex-direction: column; gap: 4px; padding: 10px 12px; color: var(--paper); background: transparent; border: 0; border-bottom: 1px solid var(--line); text-align: left; }
.attention-item:hover { background: rgba(255,255,255,.05); }
.item-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.item-meta, .empty { color: var(--queue); font-size: 11px; }
.empty { margin: 0; padding: 14px; }
</style>
