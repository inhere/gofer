<script setup lang="ts">
// 信号带（设计稿 §3 记忆点、§6.1）：顶栏下 4px 的分段色带，每段是一个活跃 job / 会话的状态。
//
// 数据源就是现有的任务列表接口（GET /v1/jobs），不新增接口：只取排队 / 运行 / 需要人 / 验收中的 job。
// 运行中的段有缓慢流光，悬停显示名字；系统开了「减少动态效果」时流光关闭（styles/base.css 已兜底，
// 这里也只在 media 查询内声明动画）。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { listJobs } from '../api/client'
import type { Job } from '../api/types'
import { statusFamily, type StatusFamily } from '../utils/statusFamily'
import { createPoller } from '../utils/poller'

// 一条带子上最多这么多段，再多就挤成线了；超出的合并成最后一段的宽度。
const MAX_SEGMENTS = 24
const ACTIVE_STATUSES = [
  'queued',
  'waiting_dir',
  'paused_for_update',
  'running',
  'recovering',
  'awaiting_input',
  'pending_interaction',
  'needs_review',
  'awaiting_approval',
]

interface Segment {
  id: string
  title: string
  family: StatusFamily
  running: boolean
}

const jobs = ref<Job[]>([])

async function refresh(): Promise<void> {
  try {
    const resp = await listJobs({ status: 'running', limit: 100 })
    jobs.value = resp.jobs ?? []
  } catch {
    // 信号带只是装饰性概览：拉不到就保持上一帧
  }
  void refreshQueued()
}

// queued / 需要人的 job 另拉一次（接口一次只认一个 status），合并后按状态排序。
async function refreshQueued(): Promise<void> {
  try {
    const resp = await listJobs({ status: 'awaiting_input', limit: 50 })
    const pending = resp.jobs ?? []
    const known = new Set(jobs.value.map((j) => j.id))
    jobs.value = [...jobs.value, ...pending.filter((j) => !known.has(j.id))]
  } catch {
    // 同上
  }
}

const poller = createPoller(refresh, 20_000)
onMounted(() => poller.start())
onUnmounted(() => poller.stop())

const segments = computed<Segment[]>(() => {
  const seen = new Set<string>()
  const out: Segment[] = []
  for (const j of jobs.value) {
    if (!j || !j.id || seen.has(j.id)) continue
    if (!ACTIVE_STATUSES.includes(String(j.status))) continue
    seen.add(j.id)
    out.push({
      id: j.id,
      title: j.title || j.id,
      family: statusFamily(j.status),
      running: statusFamily(j.status) === 'run',
    })
    if (out.length >= MAX_SEGMENTS) break
  }
  return out
})
</script>

<template>
  <div v-if="segments.length" class="wire" role="img" :aria-label="`${segments.length}`" data-test="wire">
    <span
      v-for="s in segments"
      :key="s.id"
      class="wire-seg"
      :class="[`k-${s.family}`, { 'wire-seg--run': s.running }]"
      :title="s.title"
      :data-family="s.family"
    ></span>
  </div>
  <div v-else class="wire wire--idle" aria-hidden="true"></div>
</template>

<style scoped>
.wire {
  display: flex;
  gap: 2px;
  height: 4px;
  background: var(--sunken);
  overflow: hidden;
  flex: none;
}
.wire-seg {
  flex: 1 1 0;
  min-width: 2px;
  background: var(--c, var(--idle));
  border-radius: 2px;
}
.wire-seg--run {
  background-image: linear-gradient(90deg, transparent 0%, rgba(255, 255, 255, 0.45) 50%, transparent 100%);
  background-size: 200% 100%;
  background-repeat: no-repeat;
  animation: wire-flow 3.2s linear infinite;
}
.wire--idle {
  background: var(--sunken);
}
@keyframes wire-flow {
  from {
    background-position: -100% 0;
  }
  to {
    background-position: 200% 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  .wire-seg--run {
    animation: none;
    background-image: none;
  }
}
</style>
