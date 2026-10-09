<script setup lang="ts">
// 首页状态条（design §1.4）：runner / 今日用量 / 管家 / 版本四项，平时低对比；有异常的项
// 变色并排最前（runner 离线、管家巡检失败等，来自 status.alerts）。点各项跳对应页面。
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import type { TodayStatus } from '../../api/today'
import { fmtTokens } from '../../utils/today'

const props = defineProps<{ status: TodayStatus }>()

interface Item {
  key: string
  to: string
  text: string
  alert: boolean
}

const items = computed<Item[]>(() => {
  const st = props.status
  const offline = st.runners.offline ?? []
  const runner: Item = {
    key: 'runner',
    to: '/runners',
    text: `runner ${st.runners.online}/${st.runners.total}${offline.length ? ` · ${offline.join(', ')} 离线` : ''} · 运行 ${st.runners.running_jobs}`,
    alert: offline.length > 0,
  }
  const u = st.usage_today
  const tokens = u.total_tokens + u.session_tokens
  const usage: Item = {
    key: 'usage',
    to: '/dashboard',
    text: `今日 job ${u.jobs} · ${fmtTokens(tokens)} tok${u.cost_usd > 0 ? ` · $${u.cost_usd.toFixed(2)}` : ''}`,
    alert: false,
  }
  const sw = st.steward_today
  const stewardAlert = st.alerts.some((a) => a.startsWith('管家'))
  const steward: Item = {
    key: 'steward',
    to: '/settings/work',
    text: sw.enabled ? `管家 ${sw.notes} 条笔记 · ${sw.summaries} 次整理${stewardAlert ? ' · 异常' : ''}` : `管家未启用 · ${sw.summaries} 次整理`,
    alert: stewardAlert,
  }
  const version: Item = { key: 'version', to: '/settings/about', text: st.version || '—', alert: false }
  const all = [runner, usage, steward, version]
  return [...all.filter((i) => i.alert), ...all.filter((i) => !i.alert)]
})
</script>

<template>
  <footer class="sb mono" aria-label="状态" data-test="today-status">
    <RouterLink
      v-for="it in items"
      :key="it.key"
      :to="it.to"
      class="sb-item"
      :class="{ 'sb-item--alert': it.alert }"
      :data-test="`status-${it.key}`"
    >{{ it.text }}</RouterLink>
  </footer>
</template>

<style scoped>
.sb {
  position: fixed;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 25;
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
  padding: 7px 18px calc(7px + env(safe-area-inset-bottom));
  background: var(--panel);
  border-top: 1px solid var(--line);
  font-size: 11px;
}
.sb-item {
  min-width: 0;
  color: var(--queue);
  opacity: 0.8;
  overflow-wrap: anywhere;
}
.sb-item:hover {
  color: var(--paper);
  opacity: 1;
  text-decoration: none;
}
.sb-item--alert {
  color: var(--fail);
  opacity: 1;
  font-weight: 600;
}
@media (max-width: 768px) {
  .sb {
    padding-left: 76px;
  }
}
</style>
