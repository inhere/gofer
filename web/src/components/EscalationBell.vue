<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { listPendingInteractions } from '../api/client'
import type { Interaction } from '../api/types'
import InteractionToast from './InteractionToast.vue'

const POLL_MS = 5000

const router = useRouter()

const items = ref<Interaction[]>([])
const open = ref(false)
const toast = ref<Interaction | null>(null)
const seenNeedsHuman = new Set<string>()

let timer: number | null = null

const sortedItems = computed(() =>
  [...items.value].sort((a, b) => {
    const hot = Number(b.needs_human === 1) - Number(a.needs_human === 1)
    if (hot !== 0) {
      return hot
    }
    return (b.escalated_at ?? b.created_at) - (a.escalated_at ?? a.created_at)
  }),
)

const badgeCount = computed(() => items.value.length)
const needsHumanCount = computed(
  () => items.value.filter((i) => i.needs_human === 1).length,
)

async function fetchPending(): Promise<void> {
  if (document.hidden) {
    return
  }
  const resp = await listPendingInteractions()
  const next = resp.interactions ?? []
  const freshNeedsHuman = next.find(
    (i) => i.needs_human === 1 && !seenNeedsHuman.has(i.id),
  )
  next.forEach((i) => {
    if (i.needs_human === 1) {
      seenNeedsHuman.add(i.id)
    }
  })
  items.value = next
  if (freshNeedsHuman) {
    toast.value = freshNeedsHuman
  }
}

function startPolling(): void {
  stopPolling()
  if (document.hidden) {
    return
  }
  timer = window.setInterval(() => {
    void fetchPending().catch(() => {
      // 顶栏提示不阻断页面；下一轮继续拉取。
    })
  }, POLL_MS)
}

function stopPolling(): void {
  if (timer != null) {
    window.clearInterval(timer)
    timer = null
  }
}

function onVisibility(): void {
  if (document.hidden) {
    stopPolling()
  } else {
    void fetchPending().catch(() => {})
    startPolling()
  }
}

function toggleOpen(): void {
  open.value = !open.value
}

function close(): void {
  open.value = false
}

function gotoJob(item: Interaction): void {
  close()
  void router.push(`/jobs/${encodeURIComponent(item.job_id)}`)
}

function shortJobId(id: string): string {
  return id.length > 10 ? `...${id.slice(-10)}` : id
}

function promptLine(item: Interaction): string {
  const first = item.prompt.split(/\r?\n/)[0]?.trim() ?? ''
  return first.length > 110 ? `${first.slice(0, 110)}...` : first
}

onMounted(() => {
  void fetchPending().catch(() => {})
  startPolling()
  document.addEventListener('visibilitychange', onVisibility)
})

onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <div class="bell-wrap">
    <button
      class="bell"
      type="button"
      aria-label="人工介入请求"
      :aria-expanded="open"
      @click="toggleOpen"
    >
      🔔
      <span
        v-if="badgeCount > 0"
        class="badge"
        :class="{ 'badge--hot': needsHumanCount > 0 }"
      >
        {{ badgeCount }}
      </span>
    </button>

    <div v-if="open" class="bell-scrim" aria-hidden="true" @click="close"></div>
    <div v-if="open" class="dropdown" role="menu">
      <div class="dh mono">
        <span>ESCALATIONS · pending {{ badgeCount }}</span>
        <span>{{ needsHumanCount }} needs_human</span>
      </div>

      <button
        v-for="item in sortedItems"
        :key="item.id"
        class="esc"
        :class="{ hot: item.needs_human === 1 }"
        type="button"
        role="menuitem"
        @click="gotoJob(item)"
      >
        <span class="e1 mono">
          <span v-if="item.needs_human === 1" class="mark mark--needs">needs_human</span>
          <span v-else-if="(item.escalated_at ?? 0) > 0" class="mark">escalated</span>
          <span class="idp">job {{ shortJobId(item.job_id) }}</span>
          <span class="chan">{{ item.type }}</span>
        </span>
        <span class="p">{{ promptLine(item) || '等待人工介入' }}</span>
      </button>

      <div v-if="sortedItems.length === 0" class="empty mono">
        无 pending interaction
      </div>
    </div>

    <InteractionToast
      v-if="toast"
      :interaction="toast"
      @close="toast = null"
      @goto="toast = null"
    />
  </div>
</template>

<style scoped>
.bell-wrap {
  position: relative;
  display: inline-flex;
  align-items: center;
}

.bell {
  position: relative;
  background: transparent;
  border: 1px solid var(--line);
  color: var(--paper);
  border-radius: var(--radius);
  padding: 4px 9px;
  font-size: 14px;
  line-height: 1.2;
}

.bell:hover {
  border-color: var(--phosphor);
}

.badge {
  position: absolute;
  top: -7px;
  right: -7px;
  background: var(--phosphor);
  color: var(--ink);
  border-radius: 9px;
  font-family: var(--font-mono);
  font-size: 10px;
  font-weight: 600;
  line-height: 16px;
  min-width: 16px;
  padding: 0 5px;
}

.badge--hot {
  background: var(--fail);
  color: #fff;
}

.bell-scrim {
  position: fixed;
  inset: 0;
  z-index: 55;
  background: transparent;
}

.dropdown {
  position: fixed;
  top: 52px;
  right: 18px;
  z-index: 60;
  width: min(420px, calc(100vw - 36px));
  max-height: min(520px, calc(100vh - 70px));
  overflow-y: auto;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.5);
}

.dh {
  display: flex;
  justify-content: space-between;
  gap: 14px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--line);
  color: var(--queue);
  font-size: 12px;
}

.esc {
  display: block;
  width: 100%;
  text-align: left;
  background: transparent;
  color: var(--paper);
  border: none;
  border-bottom: 1px solid var(--line);
  padding: 11px 14px;
}

.esc:last-of-type {
  border-bottom: none;
}

.esc:hover {
  background: var(--ink);
}

.esc.hot {
  background: rgba(200, 85, 61, 0.1);
}

.esc.hot:hover {
  background: rgba(200, 85, 61, 0.16);
}

.e1 {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  font-size: 12px;
}

.idp {
  color: var(--queue);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mark,
.chan {
  display: inline-block;
  flex: none;
  border: 1px solid var(--line);
  border-radius: 9px;
  color: var(--queue);
  font-family: var(--font-mono);
  font-size: 10px;
  line-height: 1.4;
  padding: 1px 6px;
}

.mark--needs {
  background: var(--fail);
  border-color: var(--fail);
  color: #fff;
}

.chan {
  color: var(--phosphor);
}

.p {
  display: block;
  margin-top: 6px;
  color: var(--paper);
  font-size: 13px;
  line-height: 1.45;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.empty {
  padding: 14px;
  color: var(--queue);
  font-size: 12px;
}
</style>
