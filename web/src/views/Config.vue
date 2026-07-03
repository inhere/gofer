<script setup lang="ts">
// Config：脱敏配置总览。只展示后端 bool 化后的 secret 状态，
// 不接收、不缓存、不渲染任何 secret 值。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { getConfig } from '../api/client'
import type { ConfigView } from '../api/types'

const POLL_MS = 5000

const config = ref<ConfigView | null>(null)
const loading = ref(false)
const loaded = ref(false)
const loadError = ref('')

let pollTimer: number | null = null

const agents = computed(() => config.value?.agents ?? [])
const runners = computed(() => config.value?.runners ?? [])
const roles = computed(() => config.value?.roles ?? [])
const callers = computed(() => config.value?.server.callers ?? [])
const workers = computed(() => config.value?.server.workers ?? [])

async function loadConfig(silent = false): Promise<void> {
  if (!silent) {
    loading.value = true
  }
  loadError.value = ''
  try {
    config.value = await getConfig()
    loaded.value = true
  } catch (e) {
    loadError.value = errorMessage(e)
  } finally {
    loading.value = false
  }
}

function startPolling(): void {
  stopPolling()
  pollTimer = window.setInterval(() => {
    if (!document.hidden) {
      void loadConfig(true)
    }
  }, POLL_MS)
}

function stopPolling(): void {
  if (pollTimer != null) {
    window.clearInterval(pollTimer)
    pollTimer = null
  }
}

function onVisibility(): void {
  if (!document.hidden) {
    void loadConfig(true)
  }
}

function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function yesNo(v: boolean): string {
  return v ? '是' : '否'
}

function setText(v: boolean): string {
  return v ? '已配置' : '未配置'
}

function joinOrDash(items?: string[]): string {
  return items && items.length > 0 ? items.join(', ') : '-'
}

onMounted(() => {
  void loadConfig()
  startPolling()
  document.addEventListener('visibilitychange', onVisibility)
})

onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <div class="config-page">
    <div class="head">
      <span class="eyebrow mono">CONFIG</span>
      <h1 class="title mono">系统配置（只读）</h1>
      <span class="poll mono" :class="{ 'poll--on': loading }">●</span>
    </div>

    <p v-if="loadError" class="error mono">{{ loadError }}</p>
    <p v-else-if="loading && !loaded" class="placeholder mono">加载配置中...</p>

    <template v-if="config">
      <section class="section" aria-label="脱敏配置总览">
        <div class="section-head">
          <h2 class="section-title mono">脱敏配置总览</h2>
          <button class="mini-btn mono" type="button" :disabled="loading" @click="loadConfig()">
            {{ loading ? '刷新中...' : '刷新' }}
          </button>
        </div>

        <div class="overview-grid">
          <article class="panel">
            <h3 class="panel-title mono">SERVER</h3>
            <dl class="kv mono">
              <dt>addr</dt><dd>{{ config.server.addr || '-' }}</dd>
              <dt>path_view</dt><dd>{{ config.server.path_view || '-' }}</dd>
              <dt>web_enabled</dt><dd><span class="flag" :class="config.server.web_enabled ? 'flag--yes' : 'flag--no'">{{ yesNo(config.server.web_enabled) }}</span></dd>
              <dt>server token</dt><dd><span class="flag" :class="config.server.token_set ? 'flag--yes' : 'flag--no'">{{ setText(config.server.token_set) }}</span></dd>
              <dt>allow_empty_token</dt><dd><span class="flag" :class="config.server.allow_empty_token ? 'flag--warn' : 'flag--no'">{{ yesNo(config.server.allow_empty_token) }}</span></dd>
            </dl>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">GOVERNANCE / METRICS</h3>
            <dl class="kv mono">
              <dt>require_answer</dt><dd><span class="flag" :class="config.server.governance.require_answer_capability ? 'flag--yes' : 'flag--no'">{{ yesNo(config.server.governance.require_answer_capability) }}</span></dd>
              <dt>require_admin</dt><dd><span class="flag" :class="config.server.governance.require_admin_capability ? 'flag--yes' : 'flag--no'">{{ yesNo(config.server.governance.require_admin_capability) }}</span></dd>
              <dt>caller max</dt><dd>{{ config.server.governance.default_caller_max_concurrent || '-' }}</dd>
              <dt>rate</dt><dd>{{ config.server.governance.default_rate_limit || '-' }} / {{ config.server.governance.default_rate_burst || '-' }}</dd>
              <dt>metrics</dt><dd><span class="flag" :class="config.server.metrics.enabled ? 'flag--yes' : 'flag--no'">{{ config.server.metrics.enabled ? 'enabled' : 'disabled' }}</span></dd>
              <dt>metrics token</dt><dd><span class="flag" :class="config.server.metrics.token_set ? 'flag--yes' : 'flag--no'">{{ setText(config.server.metrics.token_set) }}</span></dd>
            </dl>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">STORAGE</h3>
            <dl class="kv mono">
              <dt>root</dt><dd>{{ config.storage.root || '-' }}</dd>
              <dt>db_path</dt><dd>{{ config.storage.db_path || '-' }}</dd>
              <dt>exchange</dt><dd>{{ config.storage.default_exchange_subdir || '-' }}</dd>
              <dt>result</dt><dd>{{ config.storage.default_result_subdir || '-' }}</dd>
              <dt>retention</dt><dd>{{ config.storage.retention.max_age_days || '-' }}d / {{ config.storage.retention.max_count || '-' }}</dd>
            </dl>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">SUPERVISOR / PRESENCE</h3>
            <dl class="kv mono">
              <dt>supervisor</dt><dd><span class="flag" :class="config.supervisor?.enabled ? 'flag--yes' : 'flag--no'">{{ config.supervisor?.enabled ? 'enabled' : 'disabled' }}</span></dd>
              <dt>desired</dt><dd>{{ config.supervisor?.desired_supervisors ?? '-' }}</dd>
              <dt>auto_answer</dt><dd>{{ config.supervisor ? yesNo(config.supervisor.auto_answer) : '-' }}</dd>
              <dt>presence ttl</dt><dd>{{ config.presence.ttl_sec }}s</dd>
              <dt>schedule sweep</dt><dd>{{ config.schedule.sweep_interval_sec }}s</dd>
              <dt>runner probe</dt><dd>{{ config.server.runner_probe.interval_seconds }}s / {{ config.server.runner_probe.timeout_seconds }}s</dd>
            </dl>
          </article>
        </div>

        <div class="block">
          <h3 class="block-title mono">CALLERS</h3>
          <div class="table-wrap">
            <table class="table mono">
              <thead>
                <tr>
                  <th>id</th>
                  <th>token</th>
                  <th>can_answer</th>
                  <th>can_admin</th>
                  <th>quota</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="c in callers" :key="c.id">
                  <td>{{ c.id }}</td>
                  <td><span class="flag" :class="c.token_set ? 'flag--yes' : 'flag--no'">{{ setText(c.token_set) }}</span></td>
                  <td><span class="flag" :class="c.can_answer ? 'flag--yes' : 'flag--no'">{{ yesNo(c.can_answer) }}</span></td>
                  <td><span class="flag" :class="c.can_admin ? 'flag--yes' : 'flag--no'">{{ yesNo(c.can_admin) }}</span></td>
                  <td>{{ c.max_concurrent_jobs || '-' }} / {{ c.rate_limit || '-' }} / {{ c.rate_burst || '-' }}</td>
                </tr>
                <tr v-if="callers.length === 0"><td colspan="5">无 caller</td></tr>
              </tbody>
            </table>
          </div>
        </div>

        <div class="cards-3">
          <article class="panel">
            <h3 class="panel-title mono">AGENTS</h3>
            <ul class="mini-list">
              <li v-for="a in agents" :key="a.key" class="mini-row mono">
                <span class="item-main">{{ a.key }} · {{ a.type }}</span>
                <span v-if="a.command" class="muted">{{ a.command }}</span>
                <span v-for="k in a.env_keys" :key="`${a.key}:${k}`" class="tag">{{ k }}</span>
              </li>
              <li v-if="agents.length === 0" class="empty mono">无 agent</li>
            </ul>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">RUNNERS</h3>
            <ul class="mini-list">
              <li v-for="r in runners" :key="r.key" class="mini-row mono">
                <span class="item-main">{{ r.key }} · {{ r.type }}</span>
                <span v-if="r.base_url" class="muted">{{ r.base_url }}</span>
                <span class="flag" :class="r.token_set ? 'flag--yes' : 'flag--no'">{{ setText(r.token_set) }}</span>
              </li>
              <li v-if="runners.length === 0" class="empty mono">无 runner</li>
            </ul>
          </article>

          <article class="panel">
            <h3 class="panel-title mono">ROLES / WORKERS</h3>
            <ul class="mini-list">
              <li v-for="r in roles" :key="r.key" class="mini-row mono">
                <span class="item-main">{{ r.key }} · {{ r.agent }}</span>
                <span v-for="k in r.env_keys" :key="`${r.key}:${k}`" class="tag">{{ k }}</span>
              </li>
              <li v-for="w in workers" :key="`worker:${w.id}`" class="mini-row mono">
                <span class="item-main">worker {{ w.id }}</span>
                <span class="flag" :class="w.token_set ? 'flag--yes' : 'flag--no'">{{ setText(w.token_set) }}</span>
                <span class="muted">{{ joinOrDash(w.labels) }}</span>
              </li>
              <li v-if="roles.length === 0 && workers.length === 0" class="empty mono">无 role / worker</li>
            </ul>
          </article>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.config-page {
  max-width: 1180px;
  margin: 0 auto;
}
.head,
.section-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 14px;
}
.eyebrow {
  font-size: 10px;
  letter-spacing: 0.18em;
  color: var(--queue);
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
}
.poll {
  margin-left: auto;
  color: var(--line);
  font-size: 10px;
}
.poll--on {
  color: var(--phosphor);
}
.section {
  margin-bottom: 20px;
}
.section-title,
.panel-title,
.block-title {
  font-size: 12px;
  letter-spacing: 0.08em;
  color: var(--queue);
  margin: 0;
}
.section-title {
  color: var(--paper);
  font-size: 14px;
}
.mini-btn {
  margin-left: auto;
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 11px;
}
.mini-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
}
.overview-grid,
.cards-3 {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}
.cards-3 {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}
.panel {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 14px;
}
.kv {
  display: grid;
  grid-template-columns: 148px 1fr;
  gap: 7px 12px;
  margin: 10px 0 0;
  font-size: 12px;
}
.kv dt {
  color: var(--queue);
}
.kv dd {
  color: var(--paper);
  margin: 0;
  word-break: break-all;
}
.flag,
.tag {
  display: inline-block;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 1px 7px;
  font-size: 11px;
}
.flag--yes {
  color: var(--done);
  border-color: var(--done);
}
.flag--no {
  color: var(--queue);
}
.flag--warn {
  color: var(--run);
  border-color: var(--run);
}
.tag {
  color: var(--phosphor);
  margin: 4px 4px 0 0;
}
.block {
  margin-top: 12px;
  padding: 14px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
}
.table-wrap {
  overflow-x: auto;
}
.table {
  width: 100%;
  border-collapse: collapse;
  margin-top: 10px;
  font-size: 12px;
}
.table th,
.table td {
  border-bottom: 1px solid var(--line);
  padding: 7px 8px;
  text-align: left;
}
.table th {
  color: var(--queue);
  font-weight: 400;
}
.table td {
  color: var(--paper);
}
.mini-list {
  list-style: none;
  margin: 0;
}
.mini-list {
  padding: 0;
}
.mini-row {
  border-top: 1px solid var(--line);
  padding: 8px 0;
  font-size: 12px;
}
.mini-row:first-child {
  border-top: none;
}
.item-main {
  color: var(--paper);
  display: block;
  margin-bottom: 3px;
}
.muted,
.empty,
.placeholder {
  color: var(--queue);
}
.empty,
.placeholder {
  font-size: 12px;
}
.error {
  font-size: 12px;
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0;
  word-break: break-word;
}
.error {
  color: var(--fail);
  border: 1px solid var(--fail);
}
@media (max-width: 900px) {
  .overview-grid,
  .cards-3 {
    grid-template-columns: 1fr;
  }
}
</style>
