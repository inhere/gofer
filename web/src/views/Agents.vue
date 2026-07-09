<script setup lang="ts">
// Agents：listAgents 展示 detect 状态，getConfig 展开只读 agent 关键配置。
import { computed, onMounted, ref } from 'vue'
import { getConfig, listAgents } from '../api/client'
import type { AgentInfo, ConfigAgentView } from '../api/types'

const agents = ref<AgentInfo[]>([])
const configAgents = ref<ConfigAgentView[]>([])
const expanded = ref<Set<string>>(new Set())
const loading = ref(false)
const error = ref('')

const configByKey = computed(() => {
  const out = new Map<string, ConfigAgentView>()
  for (const a of configAgents.value) {
    out.set(a.key, a)
  }
  return out
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [agentsResp, configResp] = await Promise.all([listAgents(), getConfig()])
    agents.value = agentsResp.agents ?? []
    configAgents.value = configResp.agents ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
})

function toggleExpand(key: string): void {
  const next = new Set(expanded.value)
  if (next.has(key)) {
    next.delete(key)
  } else {
    next.add(key)
  }
  expanded.value = next
}

function detailFor(key: string): ConfigAgentView | undefined {
  return configByKey.value.get(key)
}

function yesNo(v: boolean): string {
  return v ? '是' : '否'
}

function textValue(v?: string): string {
  return v && v.trim() ? v : '—'
}

function listValue(v?: string[]): string {
  return v && v.length ? v.join(' ') : '—'
}
</script>

<template>
  <div class="agents">
    <div class="head">
      <h1 class="title mono">AGENTS</h1>
      <span class="poll-hint mono" :class="{ 'poll-hint--on': loading }">●</span>
    </div>

    <p class="scope-note mono">
      以下为 <b>serve 主机</b>配置的 agents 及其可用性；worker 节点各自的 agents 见
      <RouterLink to="/cluster">Cluster</RouterLink>。
    </p>

    <p v-if="error" class="error mono">{{ error }}</p>

    <div class="table">
      <div class="thead mono">
        <span class="col-detect">detect</span>
        <span class="col-key">key</span>
        <span class="col-type">type</span>
        <span class="col-info">version / error</span>
      </div>

      <template v-for="a in agents" :key="a.key">
        <div class="trow">
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
          <span class="col-key mono">
            <button
              class="key-btn mono"
              type="button"
              :aria-expanded="expanded.has(a.key)"
              :aria-controls="`agent-detail-${a.key}`"
              :title="expanded.has(a.key) ? '收起配置' : '展开配置'"
              @click="toggleExpand(a.key)"
            >
              <span class="chev" aria-hidden="true">{{ expanded.has(a.key) ? '▾' : '▸' }}</span>
              <span class="key-text">{{ a.key }}</span>
            </button>
          </span>
          <span class="col-type mono">{{ a.type }}</span>
          <span class="col-info mono">
            <span v-if="a.available" class="version">{{ a.version || '—' }}</span>
            <span v-else class="err-msg">{{ a.error || 'unavailable' }}</span>
          </span>
        </div>

        <div v-if="expanded.has(a.key)" :id="`agent-detail-${a.key}`" class="detail mono">
          <template v-if="detailFor(a.key)">
            <div class="detail-grid">
              <span class="dk">type</span><span class="dv">{{ detailFor(a.key)?.type || '—' }}</span>
              <span class="dk">interactive</span><span class="dv">{{ yesNo(detailFor(a.key)?.interactive ?? false) }}</span>
              <span class="dk">command</span><span class="dv">{{ textValue(detailFor(a.key)?.command) }}</span>
              <span class="dk">args</span><span class="dv">{{ listValue(detailFor(a.key)?.args) }}</span>
              <span class="dk">session_inject</span><span class="dv">{{ listValue(detailFor(a.key)?.session_inject) }}</span>
              <span class="dk">session_capture</span><span class="dv">{{ textValue(detailFor(a.key)?.session_capture) }}</span>
              <span class="dk">session_resume</span><span class="dv">{{ listValue(detailFor(a.key)?.session_resume) }}</span>
              <span class="dk">system_inject</span><span class="dv">{{ listValue(detailFor(a.key)?.system_inject) }}</span>
              <span class="dk">env_keys</span><span class="dv">{{ listValue(detailFor(a.key)?.env_keys) }}</span>
              <span class="dk">mcp_server_name</span><span class="dv">{{ textValue(detailFor(a.key)?.mcp_server_name) }}</span>
              <span class="dk">detect.command</span><span class="dv">{{ textValue(detailFor(a.key)?.detect.command) }}</span>
              <span class="dk">detect.args</span><span class="dv">{{ listValue(detailFor(a.key)?.detect.args) }}</span>
              <span class="dk">allow_raw_cmd</span><span class="dv">{{ yesNo(detailFor(a.key)?.allow_raw_cmd ?? false) }}</span>
            </div>
          </template>
          <span v-else class="no-detail">无配置详情</span>
        </div>
      </template>

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
.scope-note {
  color: var(--queue);
  font-size: 12px;
  margin: 0 0 12px;
}
.scope-note a {
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
.trow:hover {
  background: var(--panel);
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
  min-width: 0;
}
.key-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
  background: transparent;
  border: 0;
  color: var(--phosphor);
  cursor: pointer;
  font-size: 13px;
  padding: 0;
  overflow: hidden;
}
.key-btn:hover .key-text,
.key-btn:focus-visible .key-text {
  text-decoration: underline;
}
.key-btn:focus-visible {
  outline: 1px solid var(--phosphor);
  outline-offset: 2px;
  border-radius: 2px;
}
.chev {
  color: var(--queue);
  flex: none;
  font-size: 12px;
  line-height: 1;
}
.key-text {
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

.detail {
  border-bottom: 1px solid var(--line);
  background: var(--ink);
  padding: 10px 14px 12px 176px;
  font-size: 12px;
}
.detail-grid {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 4px 12px;
  align-items: baseline;
}
.dk {
  color: var(--queue);
  font-size: 11px;
}
.dv {
  color: var(--paper);
  overflow-wrap: anywhere;
}
.no-detail {
  color: var(--queue);
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
  .detail {
    padding-left: 14px;
  }
}
</style>
