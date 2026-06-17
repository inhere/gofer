<script setup lang="ts">
// Agents：listAgents 列表，detect 状态点(available ●/unavailable ◌)、version(available)、error(不可用 红字)。
import { onMounted, ref } from 'vue'
import { listAgents } from '../api/client'
import type { AgentInfo } from '../api/types'

const agents = ref<AgentInfo[]>([])
const loading = ref(false)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const resp = await listAgents()
    agents.value = resp.agents ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="agents">
    <div class="head">
      <h1 class="title mono">AGENTS</h1>
      <span class="poll-hint mono" :class="{ 'poll-hint--on': loading }">●</span>
    </div>

    <p v-if="error" class="error mono">{{ error }}</p>

    <div class="table">
      <div class="thead mono">
        <span class="col-detect">detect</span>
        <span class="col-key">key</span>
        <span class="col-type">type</span>
        <span class="col-info">version / error</span>
      </div>

      <div v-for="a in agents" :key="a.key" class="trow">
        <span class="col-detect">
          <span
            class="detect-dot"
            :class="a.available ? 'detect-dot--on' : 'detect-dot--off'"
            :aria-label="a.available ? 'available' : 'unavailable'"
          ></span>
          <span class="detect-text mono" :class="a.available ? 'detect-text--on' : 'detect-text--off'">
            {{ a.available ? 'available' : 'unavailable' }}
          </span>
        </span>
        <span class="col-key mono">{{ a.key }}</span>
        <span class="col-type mono">{{ a.type }}</span>
        <span class="col-info mono">
          <span v-if="a.available" class="version">{{ a.version || '—' }}</span>
          <span v-else class="err-msg">{{ a.error || 'unavailable' }}</span>
        </span>
      </div>

      <div v-if="agents.length === 0 && !error && !loading" class="empty mono">
        暂无 agent
      </div>
      <div v-if="loading && agents.length === 0" class="empty mono">
        探测中…
      </div>
    </div>
  </div>
</template>

<style scoped>
.agents {
  max-width: 900px;
  margin: 0 auto;
}
.head {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 14px;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
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
  grid-template-columns: 150px 160px 120px 1fr;
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
  font-size: 13px;
}
.trow:last-child {
  border-bottom: none;
}

.col-detect {
  display: inline-flex;
  align-items: center;
  gap: 7px;
}
.detect-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex: none;
}
.detect-dot--on {
  background: var(--done);
}
.detect-dot--off {
  background: transparent;
  box-shadow: inset 0 0 0 1.5px var(--queue);
}
.detect-text {
  font-size: 11px;
  letter-spacing: 0.04em;
}
.detect-text--on {
  color: var(--done);
}
.detect-text--off {
  color: var(--queue);
}

.col-key {
  color: var(--phosphor);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-type {
  color: var(--paper);
}
.col-info {
  overflow: hidden;
  text-overflow: ellipsis;
}
.version {
  color: var(--queue);
  font-size: 12px;
}
.err-msg {
  color: var(--fail);
  font-size: 12px;
  word-break: break-word;
}

.empty {
  padding: 28px 14px;
  text-align: center;
  color: var(--queue);
  font-size: 13px;
}

@media (max-width: 768px) {
  .thead,
  .trow {
    grid-template-columns: 120px 1fr;
    grid-auto-rows: auto;
    gap: 4px 10px;
  }
  .col-type,
  .col-info {
    grid-column: 1 / -1;
  }
}
</style>
