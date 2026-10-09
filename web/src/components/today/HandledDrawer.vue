<script setup lang="ts">
// 「已处理」抽屉（design §2.3）：近 7 天首页处理记录——时间 / 卡 / 你的动作 / 当时的管家建议。
// 数据来自 today.action 审计（GET /v1/today/handled）。
import { ref, watch } from 'vue'
import { listTodayHandled, type TodayHandled } from '../../api/today'
import { fmtDateTime } from '../../api/time'
import { handledOpen } from '../../store/today'

const rows = ref<TodayHandled[]>([])
const error = ref('')
const loading = ref(false)

watch(handledOpen, async (open) => {
  if (!open) return
  loading.value = true
  try {
    rows.value = (await listTodayHandled(7)).handled
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
})

// 当时的管家建议：建议的操作名，没有操作时用建议原文
function adviceOf(h: TodayHandled): string {
  return h.advice_label || h.advice_text || h.advice_action_id || ''
}

function close(): void {
  handledOpen.value = false
}
</script>

<template>
  <div v-if="handledOpen" class="hd-scrim" aria-hidden="true" @click="close"></div>
  <aside v-if="handledOpen" class="hd" role="dialog" aria-label="已处理" data-test="handled-drawer">
    <header class="hd-head">
      <span class="hd-title">已处理 · 近 7 天</span>
      <button class="hd-x mono" type="button" @click="close">关闭</button>
    </header>
    <p v-if="error" class="hd-err mono">{{ error }}</p>
    <p v-else-if="!loading && rows.length === 0" class="hd-empty mono">近 7 天没有在首页处理过卡片</p>
    <ul class="hd-list">
      <li v-for="h in rows" :key="`${h.at}:${h.card_key}:${h.action_id}`">
        <span class="hd-card">{{ h.title || h.card_key }}</span>
        <span class="hd-meta mono">
          <span>{{ fmtDateTime(h.at) }}</span>
          <span>你：{{ h.label || h.action_id }}</span>
          <span v-if="h.via_advice" class="hd-via">按建议</span>
          <span v-else-if="adviceOf(h)" :title="h.advice_text">管家建议：{{ adviceOf(h) }}</span>
        </span>
      </li>
    </ul>
  </aside>
</template>

<style scoped>
.hd-scrim {
  position: fixed;
  inset: 0;
  z-index: 64;
  background: rgba(0, 0, 0, 0.35);
}
.hd {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  z-index: 66;
  width: min(440px, 100vw);
  overflow-y: auto;
  background: var(--ink);
  border-left: 1px solid var(--line);
}
.hd-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 14px;
  background: var(--panel);
  border-bottom: 1px solid var(--line);
}
.hd-title {
  font-weight: 600;
}
.hd-x {
  color: var(--queue);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 8px;
  font-size: 12px;
}
.hd-err,
.hd-empty {
  padding: 14px;
  color: var(--queue);
  font-size: 12px;
}
.hd-err {
  color: var(--fail);
}
.hd-list {
  margin: 0;
  padding: 0 14px;
  list-style: none;
}
.hd-list li {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 10px 0;
  border-bottom: 1px solid var(--line);
}
.hd-card {
  overflow-wrap: anywhere;
}
.hd-via {
  color: var(--phosphor);
}
.hd-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 10px;
  color: var(--queue);
  font-size: 11px;
}
</style>
