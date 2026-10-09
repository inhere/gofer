<script setup lang="ts">
// 统计页「活跃度」卡右侧的紧凑版「Jobs 状态分布」（原在底部「系统」折叠区）。数据仍是
// GET /v1/stats 的 jobs.by_status：全部 job 的当前状态，不随页面的时间区间变化。
// 统计页不订阅 2s 一推的 stats 主题，这里随页面同频（60s，页面可见时）轮询一次。
import { onMounted, onUnmounted, ref } from 'vue'
import { getStats, statusColor } from '../api/client'
import type { JobStatus, Stats } from '../api/types'
import { createPoller } from '../utils/poller'

const REFRESH_MS = 60_000

const jobStatuses: JobStatus[] = [
  'running',
  'recovering', // RECOV-01：worker 断线 held 中（非终态）
  'pending_interaction',
  // GATE-01 S3：人工验收（非终态，等人裁决）——必须出现，否则等人验收的 job 看起来像"什么都没发生"。
  'needs_review',
  'queued',
  // JOB-11 waiting_dir（等目录锁）不单列：/v1/stats 已把它并进 queued（见 stats_handler）。
  'done',
  'failed',
  'cancelled',
  'timeout',
  'rejected',
]

// 长状态名在格子里显示缩写，完整状态放 title。
const SHORT: Partial<Record<JobStatus, string>> = {
  pending_interaction: 'pending',
  needs_review: 'review',
}

const jobs = ref<Stats['jobs'] | null>(null)
const error = ref('')

async function load(): Promise<void> {
  try {
    jobs.value = (await getStats()).jobs
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

const poller = createPoller(load, REFRESH_MS)

function count(status: JobStatus): number {
  return jobs.value?.by_status[status] ?? 0
}

onMounted(() => poller.start())
onUnmounted(() => poller.stop())
</script>

<template>
  <div class="js" data-test="job-status">
    <h4>
      Jobs 状态分布
      <span class="tag" data-tip="全部 job 的当前状态（来自 /v1/stats），不随上方时间区间变化">全部 job · 不随区间</span>
      <span class="tot mono">{{ jobs ? jobs.total : '—' }}</span>
    </h4>
    <p v-if="error && !jobs" class="err mono">{{ error }}</p>
    <div class="chips">
      <div v-for="s in jobStatuses" :key="s" class="chip">
        <span class="n mono" :style="{ color: statusColor(s) }">{{ jobs ? count(s) : '—' }}</span>
        <span class="l mono" :title="SHORT[s] ? s : undefined">{{ SHORT[s] ?? s }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.js {
  min-width: 0;
  display: grid;
  gap: 8px;
  align-content: start;
}
h4 {
  margin: 0;
  font-size: 12px;
  font-weight: 500;
  color: var(--queue);
  display: flex;
  gap: 6px;
  align-items: baseline;
  flex-wrap: wrap;
}
.tag {
  font-size: 10px;
  border: 1px solid var(--line);
  border-radius: 9px;
  padding: 0 6px;
}
.tot {
  margin-left: auto;
  color: var(--paper);
  font-size: 13px;
}
.err {
  margin: 0;
  color: var(--fail);
  font-size: 11px;
  word-break: break-word;
}
.chips {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(62px, 1fr));
  gap: 4px;
}
.chip {
  min-width: 0;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--ink);
  padding: 4px 6px;
}
.n {
  display: block;
  font-size: 15px;
  font-weight: 700;
  line-height: 1.1;
  font-variant-numeric: tabular-nums;
}
.l {
  display: block;
  color: var(--queue);
  font-size: 10px;
  line-height: 1.2;
  margin-top: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
