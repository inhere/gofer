<script setup lang="ts">
// 通知事件多选（设置 · 通知的共用组件）：IM 机器人与桌面通知两个侧栏都用这一份 ——
// 分组顺序与中英文说明来自后端 GET /v1/notify/events，控制台不自己维护第二份事件表。
// 全部不勾 = 使用后端的默认事件集（语义由后端定义），勾选后只推这些事件。
import { computed } from 'vue'
import type { NotifyEventsResp } from '../api/types'
import { t } from '../i18n'
import { locale } from '../store/locale'

// events 是勾选的事件 key 列表（空 = 默认事件集）。改选时整份替换，让父组件的 v-model 收到更新。
const events = defineModel<string[]>({ required: true })
const props = defineProps<{ catalog: NotifyEventsResp | null }>()

// eventText：事件说明由后端目录给出 zh / en 两份，控制台只挑当前语言。
function eventText(item: { zh: string; en: string }): string {
  return locale.value.startsWith('zh') ? item.zh : item.en
}

// groups 把目录按分组排好序给事件多选框用（顺序由后端 groups 决定，不在这里重排）。
const groups = computed(() => {
  const out: Array<{ key: string; label: string; items: NotifyEventsResp['events'] }> = []
  for (const g of props.catalog?.groups ?? []) {
    const items = (props.catalog?.events ?? []).filter((e) => e.group === g.key)
    if (items.length > 0) out.push({ key: g.key, label: eventText(g), items })
  }
  return out
})

function toggleEvent(key: string, on: boolean): void {
  const next = [...events.value]
  const i = next.indexOf(key)
  if (on && i < 0) next.push(key)
  if (!on && i >= 0) next.splice(i, 1)
  events.value = next
}
</script>

<template>
  <div class="field">
    <span class="field-name">{{ t('settings.notify.eventsTitle') }}</span>
    <p class="hint">{{ t('settings.notify.eventsHint') }}</p>
    <div v-for="g in groups" :key="g.key" class="ev-group">
      <span class="ev-group__name">{{ g.label }}</span>
      <label v-for="e in g.items" :key="e.key" class="ev-item" :title="eventText(e)">
        <input
          type="checkbox"
          :checked="events.includes(e.key)"
          @change="toggleEvent(e.key, ($event.target as HTMLInputElement).checked)"
        />
        <span class="mono">{{ e.key }}</span>
        <span class="muted">{{ eventText(e) }}</span>
      </label>
    </div>
  </div>
</template>

<style scoped>
.field {
  display: grid;
  gap: 4px;
  margin-bottom: 10px;
}
.field-name {
  font-size: var(--fs-xs);
  color: var(--ink-3);
}
.hint {
  margin: 0 0 8px;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.muted {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.ev-group {
  display: grid;
  gap: 2px;
  margin-bottom: 8px;
}
.ev-group__name {
  font-size: var(--fs-xs);
  color: var(--ink-2);
  font-weight: 600;
}
.ev-item {
  display: flex;
  align-items: baseline;
  gap: 8px;
  font-size: var(--fs-xs);
}
</style>
