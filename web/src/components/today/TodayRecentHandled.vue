<script setup lang="ts">
// 今天页右栏「刚处理过」（设计稿 §7.1）：最近在首页处理过的卡，一眼看到刚才做了什么。
// 数据和「已处理」抽屉同源（GET /v1/today/handled 的 today.action 审计）；每处理完一张
// （handledTodayCount 变化）重拉一次。
import { computed, onMounted, ref, watch } from 'vue'
import { listTodayHandled, type TodayHandled } from '../../api/today'
import { handledTodayCount } from '../../store/today'
import { formatRelative } from '../../utils/format'
import { t } from '../../i18n'
import StatusGlyph from '../../ui/StatusGlyph.vue'
import type { StatusFamily } from '../../utils/statusFamily'

// 只显示当天（本地时区 0 点之后）前 5 条。
const RECENT_N = 5
const rows = ref<TodayHandled[]>([])
const error = ref('')

function midnightSec(): number {
  const d = new Date()
  return Math.floor(new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime() / 1000)
}

async function load(): Promise<void> {
  try {
    const all = (await listTodayHandled(1)).handled
    const from = midnightSec()
    rows.value = all.filter((h) => h.at >= from).slice(0, RECENT_N)
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

// 卡片操作是「采纳 / 通过」还是「打回 / 拒绝」，决定这一行用哪个状态族（§5）。
function familyOf(h: TodayHandled): StatusFamily {
  const action = h.action_id ?? ''
  if (/^(reject|deny|dismiss)/.test(action)) return 'fail'
  if (action === 'snooze') return 'idle'
  if (/^(accept|approve|adopt|answer:allow)/.test(action)) return 'done'
  return 'need'
}

const title = computed(() => t('today.recent'))

onMounted(() => {
  void load()
})
watch(handledTodayCount, () => {
  void load()
})
</script>

<template>
  <section class="recent" data-test="today-recent">
    <h2>
      {{ title }}
      <span v-if="rows.length" class="muted">{{ t('today.recent.count', { n: rows.length }) }}</span>
    </h2>
    <p v-if="error" class="msg mono">{{ error }}</p>
    <p v-else-if="!rows.length" class="empty">{{ t('today.recent.empty') }}</p>
    <ul v-else class="list">
      <li v-for="h in rows" :key="`${h.at}:${h.card_key}:${h.action_id}`">
        <StatusGlyph :family="familyOf(h)" :size="12" />
        <span class="what" :title="h.title || h.card_key">{{ h.title || h.card_key }}</span>
        <span class="when mono">{{ formatRelative(h.at * 1000) }}</span>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.recent {
  margin-bottom: var(--s5);
  min-width: 0;
}
.recent h2 {
  display: flex;
  align-items: center;
  gap: var(--s2);
  margin: 0 0 var(--s2);
  font-size: var(--fs-md);
  font-weight: 600;
}
.recent h2 .muted {
  margin-left: auto;
  color: var(--ink-3);
  font-size: var(--fs-sm);
  font-weight: 400;
}
.list {
  margin: 0;
  padding: 0;
  list-style: none;
}
.list li {
  display: flex;
  align-items: center;
  gap: var(--s2);
  padding: var(--s1) 0;
  color: var(--ink-2);
  font-size: var(--fs-sm);
  border-bottom: 1px dashed var(--line);
}
.what {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.when {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  white-space: nowrap;
}
.empty,
.msg {
  margin: 0;
  padding: var(--s2);
  color: var(--ink-3);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  font-size: var(--fs-sm);
}
.msg {
  color: var(--fail);
}
</style>
