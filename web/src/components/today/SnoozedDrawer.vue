<script setup lang="ts">
// 「已稍后」抽屉（T3，design §2.3）：列出正在稍后的卡，可「放回队列」。数据 GET /v1/today/snoozed。
import { ref, watch } from 'vue'
import { listTodaySnoozed, type TodaySnoozed } from '../../api/today'
import { fmtDateTime } from '../../api/time'
import { includeExec, snoozedOpen, unsnoozeCard } from '../../store/today'
import { snoozedUntilText } from '../../utils/todaySnooze'

const rows = ref<TodaySnoozed[]>([])
const error = ref('')
const loading = ref(false)
const busy = ref('')

async function load(): Promise<void> {
  loading.value = true
  try {
    rows.value = (await listTodaySnoozed(includeExec.value)).snoozed
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

watch(snoozedOpen, (open) => {
  if (open) void load()
})

async function putBack(key: string): Promise<void> {
  busy.value = key
  try {
    await unsnoozeCard(key)
    rows.value = rows.value.filter((r) => r.card_key !== key)
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = ''
  }
}

function close(): void {
  snoozedOpen.value = false
}
</script>

<template>
  <div v-if="snoozedOpen" class="sd-scrim" aria-hidden="true" @click="close"></div>
  <aside v-if="snoozedOpen" class="sd" role="dialog" aria-label="已稍后" data-test="snoozed-drawer">
    <header class="sd-head">
      <span class="sd-title">稍后再看</span>
      <button class="sd-x mono" type="button" @click="close">关闭</button>
    </header>
    <p v-if="error" class="sd-err mono">{{ error }}</p>
    <p v-else-if="!loading && rows.length === 0" class="sd-empty mono">没有稍后的卡</p>
    <ul class="sd-list">
      <li v-for="r in rows" :key="r.card_key">
        <span class="sd-card">{{ r.title || r.card_key }}</span>
        <span class="sd-meta mono">
          <span v-if="r.tag">{{ r.tag }}</span>
          <span>{{ snoozedUntilText(r, fmtDateTime) }} · 有新动静会提前回来</span>
          <span v-if="r.expires_at" class="sd-bad">{{ fmtDateTime(r.expires_at) }} 仍按原规则兜底</span>
          <button
            class="sd-back"
            type="button"
            data-test="unsnooze"
            :disabled="busy === r.card_key"
            @click="putBack(r.card_key)"
          >放回队列</button>
        </span>
      </li>
    </ul>
  </aside>
</template>

<style scoped>
.sd-scrim {
  position: fixed;
  inset: 0;
  z-index: 64;
  background: rgba(0, 0, 0, 0.35);
}
.sd {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  z-index: 66;
  width: min(440px, 100vw);
  overflow-x: hidden;
  overflow-y: auto;
  background: var(--ink);
  border-left: 1px solid var(--line);
}
.sd-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 14px;
  background: var(--panel);
  border-bottom: 1px solid var(--line);
}
.sd-title {
  font-weight: 600;
}
.sd-x,
.sd-back {
  padding: 3px 8px;
  color: var(--queue);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  font-size: 12px;
}
.sd-back:hover:not(:disabled) {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.sd-err,
.sd-empty {
  padding: 14px;
  color: var(--queue);
  font-size: 12px;
}
.sd-err,
.sd-bad {
  color: var(--fail);
}
.sd-list {
  margin: 0;
  padding: 0 14px;
  list-style: none;
}
.sd-list li {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px 0;
  border-bottom: 1px solid var(--line);
}
.sd-card {
  overflow-wrap: anywhere;
}
.sd-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 10px;
  color: var(--queue);
  font-size: 11px;
}
</style>
