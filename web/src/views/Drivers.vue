<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { listPresence } from '../api/client'
import { fmtDateTime } from '../api/time'
import type { Presence } from '../api/types'

const router = useRouter()

const POLL_MS = 3000
const ONLINE_TTL_SEC = 30

const agents = ref<Presence[]>([])
const loading = ref(false)
const error = ref('')
const roleFilter = ref('')
const projectFilter = ref('')

let timer: number | null = null

async function fetchDrivers(): Promise<void> {
  loading.value = true
  try {
    const resp = await listPresence(
      roleFilter.value || undefined,
      projectFilter.value.trim() || undefined,
    )
    agents.value = resp.agents ?? []
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function startPolling(): void {
  stopPolling()
  if (document.hidden) {
    return
  }
  timer = window.setInterval(() => {
    void fetchDrivers()
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
    void fetchDrivers()
    startPolling()
  }
}

watch([roleFilter, projectFilter], () => {
  void fetchDrivers()
})

function isOnline(a: Presence): boolean {
  const age = Math.floor(Date.now() / 1000) - a.last_seen_at
  return a.status !== 'stale' && age <= ONLINE_TTL_SEC
}

function shortId(id: string): string {
  return id.length > 12 ? `...${id.slice(-12)}` : id
}

function relSeen(sec: number): string {
  const age = Math.max(0, Math.floor(Date.now() / 1000) - sec)
  return `${age} 秒前`
}

function openInbox(a: Presence): void {
  void router.push(`/drivers/${encodeURIComponent(a.agent_id)}`)
}

onMounted(() => {
  void fetchDrivers()
  startPolling()
  document.addEventListener('visibilitychange', onVisibility)
})

onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <div class="board">
    <div class="head">
      <h1 class="title mono">DRIVERS<span class="title-filter"> · presence</span></h1>
      <div class="ctrls mono">
        <label class="filter">
          <span class="filter-label">role</span>
          <select v-model="roleFilter" class="filter-select mono">
            <option value="">全部</option>
            <option value="supervisor">supervisor</option>
          </select>
        </label>
        <label class="filter">
          <span class="filter-label">project</span>
          <input
            v-model.trim="projectFilter"
            class="filter-input mono"
            type="text"
            placeholder="全部"
          />
        </label>
        <span class="poll-hint" :class="{ 'poll-hint--on': loading }">●</span>
      </div>
    </div>

    <p v-if="error" class="error mono">{{ error }}</p>

    <div class="table">
      <div class="thead mono">
        <span class="col-state">状态</span>
        <span class="col-name">name</span>
        <span class="col-role">role</span>
        <span class="col-project">project</span>
        <span class="col-client">client</span>
        <span class="col-agent">agent_id</span>
        <span class="col-seen">last_seen</span>
      </div>

      <div
        v-for="a in agents"
        :key="a.agent_id"
        class="trow"
        role="button"
        tabindex="0"
        @click="openInbox(a)"
        @keydown.enter="openInbox(a)"
        @keydown.space.prevent="openInbox(a)"
      >
        <span class="col-state">
          <span
            class="dot"
            :class="isOnline(a) ? 'dot--on' : 'dot--stale'"
            :title="isOnline(a) ? 'online' : 'stale'"
          ></span>
        </span>
        <span class="col-name">
          <span class="driver-name" :title="a.name || a.agent_id">{{ a.name || shortId(a.agent_id) }}</span>
        </span>
        <span class="col-role">
          <span
            v-if="a.role"
            class="badge mono"
            :class="{ 'badge--sup': a.role === 'supervisor' }"
          >
            {{ a.role }}
          </span>
          <span v-else class="sub mono">—</span>
        </span>
        <span class="col-project sub mono" :title="a.project_key || ''">{{ a.project_key || '—' }}</span>
        <span class="col-client sub mono" :title="a.client || ''">{{ a.client || '—' }}</span>
        <span class="col-agent idp mono" :title="a.agent_id">{{ shortId(a.agent_id) }}</span>
        <span class="col-seen mono">
          <span class="seen-abs">{{ fmtDateTime(a.last_seen_at) }}</span>
          <span class="seen-rel">{{ relSeen(a.last_seen_at) }}</span>
        </span>
      </div>

      <div v-if="agents.length === 0 && !error" class="empty mono">
        无在线 driver（经 gofer mcp --server 注册后出现）
      </div>
    </div>
  </div>
</template>

<style scoped>
.board {
  max-width: 1160px;
  margin: 0 auto;
}
.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  margin-bottom: 14px;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
}
.title-filter {
  color: var(--phosphor);
  font-size: 13px;
}
.ctrls {
  display: flex;
  align-items: center;
  gap: 14px;
  font-size: 12px;
}
.filter {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--queue);
}
.filter-label {
  font-size: 11px;
  letter-spacing: 0.06em;
}
.filter-select,
.filter-input {
  background: var(--panel);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 8px;
  font-size: 12px;
  outline: none;
}
.filter-input {
  width: 150px;
}
.filter-select:focus,
.filter-input:focus {
  border-color: var(--phosphor);
}
.poll-hint {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
}
.poll-hint--on {
  color: var(--phosphor);
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0 0 12px;
  word-break: break-word;
}

.table {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.thead,
.trow {
  display: grid;
  grid-template-columns: 54px minmax(160px, 1fr) 124px 142px 130px 142px 168px;
  align-items: center;
  gap: 12px;
  padding: 9px 14px;
}
.thead {
  background: var(--panel);
  border-bottom: 1px solid var(--line);
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--queue);
  text-transform: uppercase;
}
.trow {
  border-bottom: 1px solid var(--line);
  cursor: pointer;
  font-size: 13px;
  outline: none;
}
.trow:last-child {
  border-bottom: none;
}
.trow:hover {
  background: var(--panel);
}
.trow:focus-visible {
  background: var(--panel);
  box-shadow: inset 2px 0 0 var(--phosphor);
}
.col-state {
  display: flex;
  justify-content: center;
}
.dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  flex: none;
}
.dot--on {
  background: var(--done);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--done) 20%, transparent);
}
.dot--stale {
  background: var(--queue);
  opacity: 0.65;
  box-shadow: 0 0 0 1px var(--line);
}
.driver-name {
  color: var(--paper);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.badge {
  display: inline-block;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 2px 7px;
  font-size: 11px;
}
.badge--sup {
  color: var(--run);
  border-color: var(--run);
}
.sub {
  color: var(--queue);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.idp {
  color: var(--phosphor);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-seen {
  display: flex;
  flex-direction: column;
  gap: 1px;
  font-size: 12px;
}
.seen-abs {
  color: var(--paper);
}
.seen-rel {
  color: var(--queue);
  font-size: 11px;
}
.empty {
  padding: 28px 14px;
  text-align: center;
  color: var(--queue);
  font-size: 13px;
}

@media (max-width: 940px) {
  .head {
    align-items: flex-start;
    flex-direction: column;
  }
  .ctrls {
    flex-wrap: wrap;
    width: 100%;
  }
  .filter-input {
    width: 180px;
  }
  .thead {
    display: none;
  }
  .trow {
    grid-template-columns: 24px 1fr 118px;
    grid-template-areas:
      'state name role'
      'state project agent'
      'state client seen';
    row-gap: 6px;
  }
  .col-state {
    grid-area: state;
    align-items: flex-start;
    padding-top: 4px;
  }
  .col-name {
    grid-area: name;
  }
  .col-role {
    grid-area: role;
  }
  .col-project {
    grid-area: project;
  }
  .col-client {
    grid-area: client;
  }
  .col-agent {
    grid-area: agent;
  }
  .col-seen {
    grid-area: seen;
  }
}
</style>
