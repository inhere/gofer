<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { listInbox, listPresence } from '../api/client'
import { fmtDateTime } from '../api/time'
import type { InboxMessage, Presence } from '../api/types'

const props = defineProps<{ id: string }>()
const router = useRouter()

const messages = ref<InboxMessage[]>([])
const driver = ref<Presence | null>(null)
const includeRead = ref(true)
const loading = ref(false)
const error = ref('')

const driverTitle = computed(() => driver.value?.name || shortId(props.id))

async function fetchInbox(): Promise<void> {
  loading.value = true
  try {
    const resp = await listInbox(props.id, includeRead.value)
    messages.value = resp.messages ?? []
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function fetchDriver(): Promise<void> {
  try {
    const resp = await listPresence()
    driver.value = (resp.agents ?? []).find((a) => a.agent_id === props.id) ?? null
  } catch {
    driver.value = null
  }
}

function refresh(): void {
  void fetchInbox()
  void fetchDriver()
}

watch(includeRead, () => {
  void fetchInbox()
})

watch(
  () => props.id,
  () => {
    refresh()
  },
)

function shortId(id: string): string {
  return id.length > 12 ? `...${id.slice(-12)}` : id
}

function relSeen(sec: number): string {
  const age = Math.max(0, Math.floor(Date.now() / 1000) - sec)
  return `${age} 秒前`
}

function kindClass(kind: string): string {
  if (kind === 'escalation') {
    return 'kind--esc'
  }
  if (kind === 'assign') {
    return 'kind--assign'
  }
  if (kind === 'notify') {
    return 'kind--notify'
  }
  return 'kind--other'
}

function jobIdFromRef(ref?: string): string | null {
  const raw = ref?.trim()
  if (!raw) {
    return null
  }
  const prefixed = raw.match(/^job[:/ ]+([A-Za-z0-9._:-]+)$/i)
  if (prefixed) {
    return prefixed[1]
  }
  return /^[A-Za-z0-9][A-Za-z0-9._-]{7,}$/.test(raw) ? raw : null
}

function openJob(id: string): void {
  void router.push(`/jobs/${encodeURIComponent(id)}`)
}

onMounted(() => {
  refresh()
})
</script>

<template>
  <div class="board">
    <div class="head">
      <RouterLink to="/drivers" class="back mono">← drivers</RouterLink>
      <h1 class="title mono">INBOX · {{ driverTitle }}</h1>
    </div>

    <div class="summary mono">
      <span>name <b>{{ driver?.name || shortId(id) }}</b></span>
      <span>role <b :class="{ sup: driver?.role === 'supervisor' }">{{ driver?.role || '—' }}</b></span>
      <span>project <b>{{ driver?.project_key || '—' }}</b></span>
      <span>
        last_seen
        <b v-if="driver">{{ fmtDateTime(driver.last_seen_at) }} · {{ relSeen(driver.last_seen_at) }}</b>
        <b v-else>—</b>
      </span>
      <label class="toggle">
        <input v-model="includeRead" type="checkbox" />
        含已读
      </label>
    </div>

    <p v-if="error" class="error mono">{{ error }}</p>

    <div class="msgs">
      <article v-for="m in messages" :key="m.id" class="msg">
        <div class="m1 mono">
          <span class="kind" :class="kindClass(m.kind)">{{ m.kind }}</span>
          <span class="from" :title="m.from_agent">from {{ shortId(m.from_agent) }}</span>
          <span class="created">{{ fmtDateTime(m.created_at) }}</span>
        </div>
        <p class="body">{{ m.body || '—' }}</p>
        <div v-if="m.ref" class="ref mono">
          <span class="ref-label">ref</span>
          <a
            v-if="jobIdFromRef(m.ref)"
            href=""
            class="ref-link"
            :title="m.ref"
            @click.prevent="openJob(jobIdFromRef(m.ref) as string)"
          >
            {{ m.ref }}
          </a>
          <span v-else class="ref-text" :title="m.ref">{{ m.ref }}</span>
        </div>
      </article>

      <div v-if="messages.length === 0 && !error" class="empty mono">
        收件箱为空
      </div>
    </div>
  </div>
</template>

<style scoped>
.board {
  max-width: 980px;
  margin: 0 auto;
}
.head {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 14px;
}
.back {
  color: var(--phosphor);
  font-size: 12px;
  flex: none;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.summary {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px 16px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  color: var(--queue);
  padding: 10px 12px;
  margin-bottom: 12px;
  font-size: 12px;
}
.summary b {
  color: var(--paper);
  font-weight: 600;
}
.summary b.sup {
  color: var(--run);
}
.toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin-left: auto;
  color: var(--paper);
}
.toggle input {
  accent-color: var(--phosphor);
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

.msgs {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.msg {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  padding: 12px 14px;
}
.m1 {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  font-size: 12px;
}
.kind {
  display: inline-block;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 2px 7px;
  font-size: 11px;
  text-transform: uppercase;
}
.kind--esc {
  color: var(--fail);
  border-color: var(--fail);
}
.kind--assign {
  color: var(--run);
  border-color: var(--run);
}
.kind--notify,
.kind--other {
  color: var(--queue);
}
.from {
  color: var(--phosphor);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.created {
  color: var(--queue);
  margin-left: auto;
  flex: none;
}
.body {
  margin: 10px 0 0;
  color: var(--paper);
  font-size: 13px;
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-word;
}
.ref {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-top: 10px;
  font-size: 12px;
}
.ref-label {
  color: var(--queue);
  text-transform: uppercase;
}
.ref-link {
  color: var(--phosphor);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ref-text {
  color: var(--queue);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.empty {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--queue);
  font-size: 13px;
  padding: 28px 14px;
  text-align: center;
}

@media (max-width: 700px) {
  .head {
    align-items: flex-start;
    flex-direction: column;
    gap: 8px;
  }
  .toggle {
    margin-left: 0;
  }
  .m1 {
    align-items: flex-start;
    flex-direction: column;
  }
  .created {
    margin-left: 0;
  }
}
</style>
