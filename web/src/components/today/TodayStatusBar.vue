<script setup lang="ts">
// 今天页的贴底状态条（设计稿 §7.1，替代原来的 TodayStatusBar）：在跑 / 今天失败 / 节点在线 /
// 用量 / 管家 / 版本。平时低对比；有异常的项变色并排最前（节点离线、管家巡检失败等，来自
// status.alerts）。点各项跳对应页面，链接目标与迁移前一致。
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import type { TodayStatus } from '../../api/today'
import { fmtTokens } from '../../utils/today'
import { t } from '../../i18n'
import StatusGlyph from '../../ui/StatusGlyph.vue'
import type { StatusFamily } from '../../utils/statusFamily'

const props = withDefaults(defineProps<{ status: TodayStatus; failed?: number }>(), { failed: 0 })

interface Item {
  key: string
  to: string
  text: string
  family: StatusFamily
  alert: boolean
}

const items = computed<Item[]>(() => {
  const st = props.status
  const offline = st.runners.offline ?? []
  const all: Item[] = [
    { key: 'running', to: '/workbench', text: t('today.status.running', { n: st.runners.running_jobs }), family: 'run', alert: false },
    { key: 'failed', to: '/jobs?status=failed', text: t('today.status.failed', { n: props.failed }), family: 'fail', alert: false },
    {
      key: 'nodes',
      to: '/runners',
      text: t('today.status.nodes', { on: st.runners.online, total: st.runners.total }),
      family: offline.length ? 'fail' : 'done',
      alert: offline.length > 0,
    },
  ]
  // 受限调用方（带 ceiling 的 token）拿不到今日用量与管家这两块：字段缺省时整项不摆，
  // 不当 0 用量显示。
  const u = st.usage_today
  if (u) {
    const tokens = u.total_tokens + u.session_tokens
    all.push({
      key: 'usage',
      to: '/dashboard',
      text: `${t('today.status.usage', { jobs: u.jobs, tokens: fmtTokens(tokens) })}${u.cost_usd > 0 ? ` · $${u.cost_usd.toFixed(2)}` : ''}`,
      family: 'idle',
      alert: false,
    })
  }
  const sw = st.steward_today
  const stewardAlert = st.alerts.some((a) => a.startsWith('管家'))
  if (sw) {
    const base = sw.enabled
      ? t('today.status.steward', { notes: sw.notes, summaries: sw.summaries })
      : t('today.status.stewardOff', { summaries: sw.summaries })
    all.push({
      key: 'steward',
      to: '/settings/work',
      text: `${base}${sw.advice ? t('today.status.stewardAdvice', { n: sw.advice }) : ''}${stewardAlert ? t('today.status.stewardAlert') : ''}`,
      family: stewardAlert ? 'fail' : 'idle',
      alert: stewardAlert,
    })
  }
  all.push({ key: 'version', to: '/settings/about', text: st.version || '—', family: 'idle', alert: false })
  const offlineItem: Item[] = offline.length
    ? [{ key: 'offline', to: '/runners', text: t('today.status.offline', { names: offline.join(', ') }), family: 'fail', alert: true }]
    : []
  // 有异常的项排最前（与迁移前一致）
  return [...offlineItem, ...all.filter((i) => i.alert), ...all.filter((i) => !i.alert)]
})
</script>

<template>
  <footer class="statbar" :aria-label="t('today.status.label')" data-test="today-status">
    <RouterLink
      v-for="it in items"
      :key="it.key"
      :to="it.to"
      class="item"
      :class="{ 'item--alert': it.alert }"
      :data-test="`status-${it.key}`"
    >
      <StatusGlyph :family="it.family" :size="12" />
      <span>{{ it.text }}</span>
    </RouterLink>
  </footer>
</template>

<style scoped>
.statbar {
  position: sticky;
  bottom: 0;
  z-index: 20;
  display: flex;
  flex-wrap: wrap;
  gap: var(--s1) var(--s4);
  grid-column: 1 / -1;
  margin: var(--s4) calc(-1 * var(--s2)) 0;
  padding: var(--s2) var(--s3) calc(var(--s2) + env(safe-area-inset-bottom));
  background: var(--surface-2);
  border-top: 1px solid var(--line);
  font-size: var(--fs-sm);
  color: var(--ink-2);
}
.item {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  min-width: 0;
  color: var(--ink-3);
  overflow-wrap: anywhere;
}
.item:hover {
  color: var(--ink);
  text-decoration: none;
}
.item:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.item--alert {
  color: var(--fail);
  font-weight: 600;
}
@media (max-width: 760px) {
  .statbar {
    gap: var(--s1) var(--s3);
    font-size: var(--fs-xs);
  }
}
</style>
