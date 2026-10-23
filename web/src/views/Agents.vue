<script setup lang="ts">
// Agents：listAgents 展示 detect 状态，getConfig 展开只读 agent 关键配置。
// UI 重设计 P5：页头 / 检测与健康度改用 StatusGlyph 与 StatusPill，文案走 i18n；
// 设计稿 §7.4 追加「编辑」链接，跳到 设置 › 配置管理 的 agent 段（查看与编辑不再割裂）。
import { createLiveTopic } from '../utils/useLiveTopic'
import { extendsBase, extendsLabel } from '../utils/agentExtends'
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { getConfig, listAgents, probeAgent } from '../api/client'
import InteractionToast from '../components/InteractionToast.vue'
import StatusGlyph from '../ui/StatusGlyph.vue'
import StatusPill from '../ui/StatusPill.vue'
import { t } from '../i18n'
import type { StatusFamily } from '../utils/statusFamily'
import type { AgentInfo, ConfigAgentView } from '../api/types'

const agents = ref<AgentInfo[]>([])
const configAgents = ref<ConfigAgentView[]>([])
const expanded = ref<Set<string>>(new Set())
const loading = ref(false)
const error = ref('')
// 探针（SUP-01 P3）：正在探测的 agent key + 结果提示条（可跳承载它的 job）。
const probing = ref('')
const probeToast = ref<{ title: string; text: string; to?: string } | null>(null)

const configByKey = computed(() => {
  const out = new Map<string, ConfigAgentView>()
  for (const a of configAgents.value) {
    out.set(a.key, a)
  }
  return out
})

async function load(silent = false) {
  if (!silent) {
    loading.value = true
    error.value = ''
  }
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

// Q3：agents 的 detect / 健康度随 `meta` 主题（配置热重载、agent 降级/恢复）失效；WS 断开
// 超过 15s 才由 30s 兜底轮询接手。
const liveMeta = createLiveTopic('meta', { initial: false, fetch: () => load(true) })

onMounted(() => {
  void load()
  liveMeta.start()
})

onUnmounted(() => {
  liveMeta.stop()
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

// healthState 渲染 /v1/agents 的 health 块（SUP-01 P3）：unknown 是
// "窗口内没有样本"，不是"健康"——一个从没跑过 job 的 agent 不该显示绿点。
function healthState(a: AgentInfo): string {
  return a.health?.state || 'unknown'
}

function windowLabel(sec: number): string {
  if (!sec || sec <= 0) {
    return t('agents.health.window')
  }
  return sec % 3600 === 0 ? `${sec / 3600}h` : `${Math.round(sec / 60)}m`
}

// 健康度的状态族（设计稿 §5）：正常 = done、降级 = time、无样本 = idle。
function healthFamily(a: AgentInfo): StatusFamily {
  const state = healthState(a)
  if (state === 'healthy') return 'done'
  if (state === 'degraded') return 'time'
  return 'idle'
}

function healthLabel(a: AgentInfo): string {
  const state = healthState(a)
  if (state === 'healthy') return t('agents.health.healthy')
  if (state === 'degraded') return t('agents.health.degraded')
  return t('agents.health.unknown')
}

function healthTitle(a: AgentInfo): string {
  const h = a.health
  if (!h || h.state === 'unknown') {
    return t('agents.health.titleUnknown', { window: h ? windowLabel(h.window_sec) : t('agents.health.window') })
  }
  const params = { window: windowLabel(h.window_sec), fail: h.transient_fail, jobs: h.jobs, ok: h.ok }
  return h.state === 'degraded'
    ? t('agents.health.titleDegraded', params)
    : t('agents.health.titleHealthy', params)
}

// runProbe 提交一次探针 job：结果提示条给出状态/耗时/首行，并可跳到那个 job；
// 探测本身就是一次 job，所以随后刷新一次列表把新的健康度带回来。
async function runProbe(key: string): Promise<void> {
  if (probing.value) {
    return
  }
  probing.value = key
  try {
    const res = await probeAgent(key)
    const secs = (res.duration_ms / 1000).toFixed(1)
    const line = res.first_line ? ` · ${res.first_line}` : ''
    probeToast.value = {
      title: t('agents.probe.title', { key }),
      text: `${res.status} · exit ${res.exit_code} · ${secs}s${line}`,
      to: `/jobs/${encodeURIComponent(res.job_id)}`,
    }
    await load()
  } catch (e) {
    probeToast.value = {
      title: t('agents.probe.failTitle', { key }),
      text: e instanceof Error ? e.message : String(e),
    }
  } finally {
    probing.value = ''
  }
}

function detailFor(key: string): ConfigAgentView | undefined {
  return configByKey.value.get(key)
}

function yesNo(v: boolean): string {
  return v ? t('agents.yes') : t('agents.no')
}

function textValue(v?: string): string {
  return v && v.trim() ? v : '—'
}

function listValue(v?: string[]): string {
  return v && v.length ? v.join(' ') : '—'
}
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="title">{{ t('nav.agents') }}</h1>
        <p class="sub">{{ t('agents.sub') }}</p>
      </div>
      <span class="poll-dot" :class="{ 'poll-dot--on': loading }" aria-hidden="true">●</span>
    </header>

    <p class="scope-note">
      {{ t('agents.scopePrefix') }}
      <RouterLink to="/runners">{{ t('nav.runners') }}</RouterLink>{{ t('agents.scopeSuffix') }}
    </p>

    <p v-if="error" class="error">{{ error }}</p>

    <div class="table">
      <div class="thead">
        <span class="col-detect">{{ t('agents.col.detect') }}</span>
        <span class="col-key">{{ t('agents.col.key') }}</span>
        <span class="col-type">{{ t('agents.col.type') }}</span>
        <span class="col-health">{{ t('agents.col.health') }}</span>
        <span class="col-info">{{ t('agents.col.info') }}</span>
        <span class="col-edit" aria-hidden="true"></span>
      </div>

      <template v-for="a in agents" :key="a.key">
        <div class="trow">
          <span class="col-detect">
            <StatusGlyph :family="a.available ? 'done' : 'idle'" :label="a.available ? t('agents.detect.on') : t('agents.detect.off')" :size="13" />
            <span class="detect-text" :class="a.available ? 'detect-text--on' : 'detect-text--off'">
              {{ a.available ? t('agents.detect.on') : t('agents.detect.off') }}
            </span>
          </span>
          <span class="col-key">
            <button
              class="key-btn"
              type="button"
              :aria-expanded="expanded.has(a.key)"
              :aria-controls="`agent-detail-${a.key}`"
              :title="expanded.has(a.key) ? t('agents.detect.collapse') : t('agents.detect.expand')"
              @click="toggleExpand(a.key)"
            >
              <span class="chev" aria-hidden="true">{{ expanded.has(a.key) ? '▾' : '▸' }}</span>
              <span class="key-text">{{ a.key }}</span>
            </button>
            <!-- 内置模板注入的 agent（config.yaml 里没有它，是本机 PATH 上装了 CLI 后
                 由 agent.Resolve 注入的）。徽标让"我没配过它，它从哪来的"一眼可答。 -->
            <span v-if="a.injected" class="tag" :title="t('agents.builtinTitle')">{{ t('agents.builtin') }}</span>
            <span
              v-else-if="a.extends"
              class="tag"
              :title="t('agents.extendsTitle', { base: extendsBase(a.extends), over: (a.overrides ?? []).join(', ') || '—' })"
              >{{ extendsLabel(a.extends) }}</span
            >
          </span>
          <span class="col-type">
            {{ a.type }}<small v-if="a.session_count"> · {{ t('agents.sessions', { n: a.session_count }) }}</small>
          </span>
          <span class="col-health" :title="healthTitle(a)">
            <StatusPill :family="healthFamily(a)" :label="healthLabel(a)" :status="healthState(a)" />
            <button
              class="btn btn--mini"
              type="button"
              :disabled="probing !== ''"
              :title="t('agents.probeTitle')"
              @click="runProbe(a.key)"
            >
              {{ probing === a.key ? t('agents.probing') : t('agents.probe') }}
            </button>
          </span>
          <span class="col-info">
            <span v-if="a.available" class="version">{{ a.version || '—' }}</span>
            <span v-else class="err-msg">{{ a.error || 'unavailable' }}</span>
          </span>
          <span class="col-edit">
            <!-- 设计稿 §7.4 / §11-17：查看在这里、编辑在配置管理，两处割裂 → 给一个直达编辑的链接。 -->
            <RouterLink class="edit-link" to="/settings/config" :title="t('agents.editTitle')">{{ t('agents.edit') }}</RouterLink>
          </span>
        </div>

        <div v-if="expanded.has(a.key)" :id="`agent-detail-${a.key}`" class="detail">
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
          <span v-else class="no-detail">{{ t('agents.noDetail') }}</span>
        </div>
      </template>

      <div v-if="agents.length === 0 && !error && !loading" class="empty">{{ t('agents.empty') }}</div>
      <div v-if="loading && agents.length === 0" class="empty">{{ t('agents.loading') }}</div>
    </div>

    <InteractionToast
      v-if="probeToast"
      :title="probeToast.title"
      :text="probeToast.text"
      :to="probeToast.to"
      @close="probeToast = null"
      @goto="probeToast = null"
    />
  </div>
</template>

<style scoped>
.page {
  max-width: 1160px;
  margin: 0 auto;
}
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--s3);
  margin-bottom: var(--s3);
}
.title {
  margin: 0;
  font-size: var(--fs-lg);
  color: var(--fg);
}
.sub {
  margin: 2px 0 0;
  color: var(--fg-3);
  font-size: var(--fs-sm);
}
.poll-dot {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
}
.poll-dot--on {
  color: var(--accent);
}
.scope-note {
  color: var(--fg-3);
  font-size: var(--fs-xs);
  margin: 0 0 var(--s3);
  max-width: 76ch;
}
.scope-note a {
  color: var(--accent);
}

.error {
  color: var(--fail);
  font-size: var(--fs-sm);
  border: 1px solid var(--fail);
  border-radius: var(--r-ctl);
  padding: var(--s2);
  margin: 0 0 var(--s3);
  word-break: break-word;
}

.table {
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  overflow: hidden;
  background: var(--surface);
}
.thead,
.trow {
  display: grid;
  grid-template-columns: 130px 190px 130px 200px minmax(0, 1fr) 64px;
  align-items: center;
  gap: var(--s3);
  padding: 9px var(--s3);
}
.thead {
  background: var(--surface-2);
  border-bottom: 1px solid var(--line);
  font-size: var(--fs-xs);
  letter-spacing: 0.06em;
  color: var(--fg-3);
  text-transform: uppercase;
}
.trow {
  border-bottom: 1px solid var(--line);
  font-size: var(--fs-sm);
}
.trow:hover {
  background: var(--surface-2);
}

.col-detect {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
}
.detect-text {
  font-size: var(--fs-xs);
  letter-spacing: 0.04em;
}
.detect-text--on {
  color: var(--done);
}
.detect-text--off {
  color: var(--fg-3);
}

.col-key {
  display: flex;
  align-items: center;
  gap: var(--s1);
  min-width: 0;
}
.key-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
  background: transparent;
  border: 0;
  color: var(--accent);
  cursor: pointer;
  font: inherit;
  font-size: var(--fs-sm);
  padding: 0;
  overflow: hidden;
}
.key-btn:hover .key-text,
.key-btn:focus-visible .key-text {
  text-decoration: underline;
}
.chev {
  color: var(--fg-3);
  flex: none;
  font-size: 11px;
  line-height: 1;
}
.key-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tag {
  flex: none;
  font-size: var(--fs-xs);
  letter-spacing: 0.04em;
  border: 1px solid var(--line-strong);
  border-radius: var(--r-chip);
  padding: 0 5px;
  color: var(--fg-3);
}
.col-type {
  color: var(--fg-2);
  font-size: var(--fs-xs);
}
.col-type small {
  color: var(--fg-3);
}
.col-health {
  display: inline-flex;
  align-items: center;
  gap: var(--s2);
}
.col-info {
  overflow: hidden;
  text-overflow: ellipsis;
  font-size: var(--fs-xs);
}
.version {
  color: var(--fg-3);
}
.err-msg {
  color: var(--fail);
  overflow-wrap: anywhere;
}
.col-edit {
  text-align: right;
}
.edit-link {
  color: var(--accent);
  font-size: var(--fs-xs);
}
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: var(--surface);
  color: var(--fg);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-xs);
  cursor: pointer;
  white-space: nowrap;
}
.btn--mini {
  min-height: 24px;
  padding: 0 8px;
}
.btn:hover:not(:disabled) {
  border-color: var(--accent);
}
.btn:disabled {
  color: var(--fg-3);
  cursor: default;
}

.detail {
  border-bottom: 1px solid var(--line);
  border-top: 1px solid var(--line);
  background: var(--sunken);
  padding: var(--s3) var(--s3) var(--s3) 152px;
  font-size: var(--fs-xs);
}
.detail-grid {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 4px var(--s3);
  align-items: baseline;
}
.dk {
  color: var(--fg-3);
  font-family: var(--font-mono);
}
.dv {
  color: var(--fg);
  font-family: var(--font-mono);
  overflow-wrap: anywhere;
}
.no-detail {
  color: var(--fg-3);
}

.empty {
  padding: var(--s5) var(--s3);
  text-align: center;
  color: var(--fg-3);
  font-size: var(--fs-sm);
}

/* 手机档（设计稿 §4.3：≤760）：列头隐藏，一行 agent 改成卡片 */
@media (max-width: 760px) {
  .thead {
    display: none;
  }
  .trow {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    grid-template-areas:
      'key detect'
      'type type'
      'health info';
    gap: 6px var(--s2);
    padding: 10px;
  }
  .col-key {
    grid-area: key;
    min-width: 0;
  }
  .key-btn {
    max-width: calc(100% - 52px);
  }
  .col-detect {
    grid-area: detect;
    justify-content: flex-end;
  }
  .detect-text {
    display: none;
  }
  .col-type {
    grid-area: type;
  }
  .col-health {
    grid-area: health;
  }
  .col-info {
    grid-area: info;
    min-width: 0;
    text-align: right;
  }
  .col-edit {
    display: none;
  }
  .detail {
    padding: 10px;
  }
  .detail-grid {
    grid-template-columns: minmax(110px, 0.7fr) minmax(0, 1fr);
  }
}
</style>
