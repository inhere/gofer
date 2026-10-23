<script setup lang="ts">
// 设置 · 工作项：被动整理（整理器 agent / 参数 / 开关 / 阈值 / 每日上限）、请求超时、
// 自动交接与每日摘要。读 GET /v1/work-items/summarizer（有效值 + 整理器可用性），
// 写 PUT /v1/config/work（部分更新，需要 can_admin）；改动在下一个扫描周期生效。
// UI 重设计 P5-d：FormSection 分节 + 吸底 SaveBar + 文案走 i18n（settings.work.*）。
import { computed, onMounted, reactive, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { ApiError, getWorkSummarizer, listAgents, putConfigWork } from '../../api/client'
import {
  getSteward,
  putConfigSteward,
  restartSteward,
  startSteward,
  stopSteward,
  type StewardSettings,
  type StewardStatus,
} from '../../api/steward'
import StewardNotes from '../../components/StewardNotes.vue'
import type { AgentInfo, WorkSettings, WorkSummarizerStatus } from '../../api/types'
import { t } from '../../i18n'
import FormSection from '../../ui/FormSection.vue'
import SaveBar from '../../ui/SaveBar.vue'
import { acpAgentOptions, agentSwitchWarning, notesSizeLabel, settingsDiff, stateLabel } from '../../utils/steward'

const status = ref<WorkSummarizerStatus | null>(null)
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const notice = ref('')

const form = reactive({
  summarize_enabled: true,
  summarizer_agent: '',
  argsText: '[]',
  summarizer_project: '',
  summarize_idle_min: '15',
  summarize_min_interval_min: '30',
  summarize_daily_limit: '50',
  auto_handoff: true,
  request_timeout_min: '30',
  digest_enabled: true,
  digest_time: '09:00',
  needs_me_notify: false,
  needs_me_throttle_min: '30',
})
let loaded: WorkSettings | null = null

function fill(s: WorkSettings): void {
  loaded = s
  form.summarize_enabled = s.summarize_enabled
  form.summarizer_agent = s.summarizer_agent
  form.argsText = JSON.stringify(s.summarizer_args ?? [])
  form.summarizer_project = s.summarizer_project ?? ''
  form.summarize_idle_min = String(s.summarize_idle_min)
  form.summarize_min_interval_min = String(s.summarize_min_interval_min)
  form.summarize_daily_limit = String(s.summarize_daily_limit)
  form.auto_handoff = s.auto_handoff
  form.request_timeout_min = String(s.request_timeout_min)
  form.digest_enabled = s.digest_enabled
  form.digest_time = s.digest_time
  form.needs_me_notify = s.needs_me_notify ?? false
  form.needs_me_throttle_min = String(s.needs_me_throttle_min ?? 30)
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const r = await getWorkSummarizer()
    status.value = r.status
    fill(r.settings)
  } catch (e) {
    error.value = errText(e)
  } finally {
    loading.value = false
  }
}

function errText(e: unknown): string {
  if (e instanceof ApiError) return e.detail ? `${e.message}：${e.detail}` : e.message
  return e instanceof Error ? e.message : String(e)
}

function parseArgs(text: string): string[] | null {
  const trimmed = text.trim()
  if (trimmed === '') return []
  try {
    const v = JSON.parse(trimmed)
    return Array.isArray(v) && v.every((x) => typeof x === 'string') ? (v as string[]) : null
  } catch {
    return null
  }
}

function intOf(v: string): number | null {
  const n = Number.parseInt(v, 10)
  return Number.isFinite(n) ? n : null
}

const dirty = computed(() => {
  const l = loaded
  if (!l) return false
  return (
    form.summarize_enabled !== l.summarize_enabled || form.summarizer_agent !== l.summarizer_agent ||
    form.argsText !== JSON.stringify(l.summarizer_args ?? []) || form.summarizer_project !== (l.summarizer_project ?? '') ||
    form.summarize_idle_min !== String(l.summarize_idle_min) || form.summarize_min_interval_min !== String(l.summarize_min_interval_min) ||
    form.summarize_daily_limit !== String(l.summarize_daily_limit) || form.auto_handoff !== l.auto_handoff ||
    form.request_timeout_min !== String(l.request_timeout_min) || form.digest_enabled !== l.digest_enabled ||
    form.digest_time !== l.digest_time || form.needs_me_notify !== (l.needs_me_notify ?? false) ||
    form.needs_me_throttle_min !== String(l.needs_me_throttle_min ?? 30)
  )
})

async function save(): Promise<void> {
  if (saving.value) return
  error.value = ''
  notice.value = ''
  const args = parseArgs(form.argsText)
  if (args === null) {
    error.value = t('settings.work.errArgs')
    return
  }
  const idle = intOf(form.summarize_idle_min)
  const gap = intOf(form.summarize_min_interval_min)
  const daily = intOf(form.summarize_daily_limit)
  const timeout = intOf(form.request_timeout_min)
  if (idle === null || gap === null || daily === null || timeout === null || idle < 1 || gap < 1 || timeout < 1) {
    error.value = t('settings.work.errThreshold')
    return
  }
  if (!/^\d{1,2}:\d{2}$/.test(form.digest_time.trim())) {
    error.value = t('settings.work.errDigest')
    return
  }
  const throttle = intOf(form.needs_me_throttle_min)
  if (throttle === null || throttle < 1) {
    error.value = t('settings.work.errThrottle')
    return
  }
  saving.value = true
  try {
    await putConfigWork({
      summarize_enabled: form.summarize_enabled,
      summarizer_agent: form.summarizer_agent.trim(),
      summarizer_args: args,
      summarizer_project: form.summarizer_project.trim(),
      summarize_idle_min: idle,
      summarize_min_interval_min: gap,
      summarize_daily_limit: daily,
      auto_handoff: form.auto_handoff,
      request_timeout_min: timeout,
      digest_enabled: form.digest_enabled,
      digest_time: form.digest_time.trim(),
      needs_me_notify: form.needs_me_notify,
      needs_me_throttle_min: throttle,
    })
    notice.value = t('settings.work.saved')
    await load()
  } catch (e) {
    error.value = errText(e)
  } finally {
    saving.value = false
  }
}

// ---------------- 管家（steward） ----------------
const stewardStatus = ref<StewardStatus | null>(null)
const stewardSettings = ref<StewardSettings | null>(null)
const agents = ref<AgentInfo[]>([])
const notesOpen = ref(false)
const stewardBusy = ref(false)
const stewardError = ref('')
const stewardNotice = ref('')
const sform = reactive({ enabled: false, agent: '', project: '', review_time: '', idle_end_min: '30', event_wake: false })

function fillSteward(r: { status: StewardStatus; settings: StewardSettings }): void {
  stewardStatus.value = r.status
  stewardSettings.value = r.settings
  sform.enabled = r.settings.enabled
  sform.agent = r.settings.agent
  sform.project = r.settings.project === 'default' ? '' : r.settings.project
  sform.review_time = r.settings.review_time_explicit ? r.settings.review_time : ''
  sform.idle_end_min = String(r.settings.idle_end_min)
  sform.event_wake = r.settings.event_wake
}

async function loadSteward(): Promise<void> {
  try {
    fillSteward(await getSteward())
    agents.value = (await listAgents().catch(() => ({ agents: [] as AgentInfo[] }))).agents ?? []
  } catch (e) {
    stewardError.value = errText(e)
  }
}

const agentOptions = computed(() => acpAgentOptions(agents.value, sform.agent))
const switchWarning = computed(() => agentSwitchWarning(stewardStatus.value, sform.agent))
const stewardDiff = computed(() => (stewardSettings.value ? settingsDiff(stewardSettings.value, sform) : {}))
const stewardDirty = computed(() => Object.keys(stewardDiff.value).length > 0)

async function saveSteward(): Promise<void> {
  if (stewardBusy.value || !stewardDirty.value) return
  stewardError.value = ''
  stewardNotice.value = ''
  if (sform.review_time.trim() && !/^\d{1,2}:\d{2}$/.test(sform.review_time.trim())) {
    stewardError.value = t('settings.work.errReviewTime')
    return
  }
  const idle = Number.parseInt(sform.idle_end_min, 10)
  if (!Number.isFinite(idle) || idle < 1) {
    stewardError.value = t('settings.work.errIdle')
    return
  }
  stewardBusy.value = true
  try {
    await putConfigSteward(stewardDiff.value)
    stewardNotice.value = t('toast.saved')
    await loadSteward()
  } catch (e) {
    stewardError.value = errText(e)
  } finally {
    stewardBusy.value = false
  }
}

async function stewardAction(kind: 'start' | 'restart' | 'stop'): Promise<void> {
  if (stewardBusy.value) return
  if (kind === 'restart' && !window.confirm(t('settings.work.confirmRestart'))) return
  stewardError.value = ''
  stewardNotice.value = ''
  stewardBusy.value = true
  try {
    const fn = kind === 'start' ? startSteward : kind === 'restart' ? restartSteward : stopSteward
    const r = await fn()
    stewardStatus.value = r.status
  } catch (e) {
    stewardError.value = errText(e)
  } finally {
    stewardBusy.value = false
  }
}

onMounted(() => {
  void load()
  void loadSteward()
})
</script>

<template>
  <div class="work-settings" data-test="work-settings">
    <header class="page-head">
      <div class="grow">
        <h1>{{ t('settings.work.title') }}</h1>
        <p class="sub">{{ t('settings.work.sub') }}</p>
      </div>
    </header>

    <p v-if="error" class="msg msg--err" data-test="ws-error">{{ error }}</p>
    <p v-if="notice" class="msg msg--ok" data-test="ws-notice">{{ notice }}</p>
    <p v-if="loading && !status" class="muted">{{ t('settings.work.loading') }}</p>

    <template v-if="status">
      <FormSection :title="t('settings.work.summarize')" :desc="t('settings.work.summarizeDesc')">
        <p v-if="status.available" class="ok" data-test="ws-available">
          {{
            status.daily_limit > 0
              ? t('settings.work.availableCapped', { agent: status.agent, used: status.daily_used, cap: status.daily_limit })
              : t('settings.work.available', { agent: status.agent, used: status.daily_used })
          }}
        </p>
        <p v-else class="warn" data-test="ws-unavailable">
          {{
            t('settings.work.unavailable', {
              reason: status.reason || t('settings.work.noReason'),
            })
          }}
        </p>
        <p class="muted">{{ t('settings.work.summarizeHelp') }}</p>

        <label class="check">
          <input v-model="form.summarize_enabled" type="checkbox" data-test="ws-enabled" />
          {{ t('settings.work.enabledToggle') }}
        </label>
        <div class="grid">
          <label class="field">
            <span class="lab">{{ t('settings.work.agentLabel') }}</span>
            <input v-model="form.summarizer_agent" type="text" placeholder="claude" data-test="ws-agent" />
          </label>
          <label class="field">
            <span class="lab">{{ t('settings.work.projectLabel') }}</span>
            <input
              v-model="form.summarizer_project"
              type="text"
              :placeholder="t('settings.work.projectPlaceholder')"
              data-test="ws-project"
            />
          </label>
        </div>
        <p class="muted" data-test="ws-project-hint">
          <template v-if="form.summarizer_project.trim()">
            {{ t('settings.work.projectHintFixed', { project: form.summarizer_project.trim() }) }}
          </template>
          <template v-else>{{ t('settings.work.projectHintEmpty') }}</template>
          <template v-if="status.effective_project">
            {{ ' ' }}{{ t('settings.work.projectHintCurrent', {
              project: status.effective_project,
              dir: status.effective_dir ? `（${status.effective_dir}）` : '',
            }) }}
          </template>
        </p>
        <label class="field">
          <span class="lab">{{ t('settings.work.argsLabel') }}</span>
          <input v-model="form.argsText" type="text" placeholder='["--model","haiku","--tools","","--no-session-persistence"]' data-test="ws-args" />
        </label>
        <div class="grid3">
          <label class="field">
            <span class="lab">{{ t('settings.work.idleLabel') }}</span>
            <input v-model="form.summarize_idle_min" type="number" min="1" data-test="ws-idle" />
          </label>
          <label class="field">
            <span class="lab">{{ t('settings.work.intervalLabel') }}</span>
            <input v-model="form.summarize_min_interval_min" type="number" min="1" />
          </label>
          <label class="field">
            <span class="lab">{{ t('settings.work.dailyLabel') }}</span>
            <input v-model="form.summarize_daily_limit" type="number" data-test="ws-daily" />
          </label>
        </div>
      </FormSection>

      <FormSection :title="t('settings.work.handoff')" :desc="t('settings.work.handoffDesc')">
        <label class="check">
          <input v-model="form.auto_handoff" type="checkbox" data-test="ws-handoff" />
          {{ t('settings.work.handoffToggle') }}
        </label>
        <label class="field">
          <span class="lab">{{ t('settings.work.timeoutLabel') }}</span>
          <input v-model="form.request_timeout_min" type="number" min="1" />
        </label>
      </FormSection>

      <FormSection :title="t('settings.work.digest')" :desc="t('settings.work.digestDesc')">
        <label class="check">
          <input v-model="form.digest_enabled" type="checkbox" />
          {{ t('settings.work.digestToggle') }}
        </label>
        <label class="field">
          <span class="lab">{{ t('settings.work.digestTime') }}</span>
          <input v-model="form.digest_time" type="text" placeholder="09:00" />
        </label>
      </FormSection>

      <FormSection
        :title="t('settings.work.needsMe')"
        :desc="t('settings.work.needsMeDesc')"
        data-test="needs-me-section"
      >
        <label class="check">
          <input v-model="form.needs_me_notify" type="checkbox" data-test="ws-needsme" />
          {{ t('settings.work.needsMeToggle') }}
        </label>
        <label class="field">
          <span class="lab">{{ t('settings.work.needsMeThrottle') }}</span>
          <input v-model="form.needs_me_throttle_min" type="number" min="1" data-test="ws-needsme-throttle" />
        </label>
      </FormSection>

      <FormSection :title="t('settings.work.steward')" :desc="t('settings.work.stewardHelp')">
        <p v-if="stewardError" class="msg msg--err" data-test="steward-error">{{ stewardError }}</p>
        <p v-if="stewardNotice" class="msg msg--ok" data-test="steward-notice">{{ stewardNotice }}</p>
        <template v-if="stewardStatus">
          <p class="status-line" data-test="steward-status">
            {{ t('settings.work.stewardState', { state: stateLabel(stewardStatus.state, stewardStatus.enabled) }) }}
            <template v-if="stewardStatus.job_id">
              {{ ' ' }}{{ t('settings.work.stewardSession') }}
              <RouterLink :to="`/jobs/${encodeURIComponent(stewardStatus.job_id)}`" data-test="steward-job">{{ stewardStatus.job_id }}</RouterLink>
              （{{ stewardStatus.job_agent }}）
            </template>
          </p>
          <p v-if="stewardStatus.agent_error" class="warn" data-test="steward-agent-error">{{ stewardStatus.agent_error }}</p>
        </template>
        <label class="check">
          <input v-model="sform.enabled" type="checkbox" data-test="steward-enabled" />
          {{ t('settings.work.stewardEnabled') }}
        </label>
        <div class="grid">
          <label class="field">
            <span class="lab">{{ t('settings.work.stewardAgent') }}</span>
            <select v-model="sform.agent" data-test="steward-agent">
              <option value="">{{ t('settings.work.stewardNone') }}</option>
              <option v-for="o in agentOptions" :key="o.key" :value="o.key">{{ o.label }}</option>
            </select>
          </label>
          <label class="field">
            <span class="lab">{{ t('settings.work.stewardProject') }}</span>
            <input v-model="sform.project" type="text" :placeholder="t('settings.work.stewardProjectPlaceholder')" data-test="steward-project" />
          </label>
        </div>
        <p v-if="switchWarning" class="warn" data-test="steward-switch-warning">{{ switchWarning }}</p>
        <div class="grid3">
          <label class="field">
            <span class="lab">{{ t('settings.work.stewardReviewTime') }}</span>
            <input
              v-model="sform.review_time"
              type="text"
              :placeholder="stewardSettings
                ? t('settings.work.stewardReviewPlaceholder', { time: stewardSettings.review_time })
                : '08:50'"
              data-test="steward-review-time"
            />
          </label>
          <label class="field">
            <span class="lab">{{ t('settings.work.stewardIdle') }}</span>
            <input v-model="sform.idle_end_min" type="number" min="1" data-test="steward-idle" />
          </label>
        </div>
        <label class="check">
          <input v-model="sform.event_wake" type="checkbox" data-test="steward-event-wake" />
          {{ t('settings.work.stewardEventWake') }}
        </label>
        <p v-if="stewardStatus" class="muted" data-test="steward-notes-info">
          {{ t('settings.work.stewardNotes', { version: stewardStatus.notes_version, size: notesSizeLabel(stewardStatus.notes_bytes) }) }}
          <template v-if="stewardStatus.notes_need_slim">
            <span class="warn"> {{ t('settings.work.stewardNotesSlim') }}</span>
          </template>
          <button class="btn" type="button" data-test="steward-notes-open" @click="notesOpen = true">
            {{ t('settings.work.stewardNotesOpen') }}
          </button>
        </p>
        <div class="actions">
          <button class="btn btn--primary" type="button" :disabled="stewardBusy || !stewardDirty" data-test="steward-save" @click="saveSteward">
            {{ stewardBusy ? t('settings.work.busy') : t('settings.work.stewardSave') }}
          </button>
          <button class="btn" type="button" :disabled="stewardBusy || !stewardStatus?.enabled || stewardStatus?.state !== 'not_started'" data-test="steward-start" @click="stewardAction('start')">
            {{ t('settings.work.stewardStart') }}
          </button>
          <button class="btn" type="button" :disabled="stewardBusy || !stewardStatus?.enabled" data-test="steward-restart" @click="stewardAction('restart')">
            {{ t('settings.work.stewardRestart') }}
          </button>
          <button class="btn" type="button" :disabled="stewardBusy || stewardStatus?.state === 'not_started'" data-test="steward-stop" @click="stewardAction('stop')">
            {{ t('settings.work.stewardStop') }}
          </button>
        </div>
      </FormSection>

      <SaveBar
        :dirty="dirty"
        :busy="saving"
        :save-text="t('settings.work.save')"
        @save="save"
        @discard="load"
      />
    </template>
    <StewardNotes v-if="notesOpen" @close="notesOpen = false" @changed="loadSteward" />
  </div>
</template>

<style scoped>
.work-settings {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.page-head {
  margin-bottom: 6px;
}
.page-head h1 {
  margin: 0 0 4px;
  font-size: var(--fs-xl);
  color: var(--ink);
}
.sub {
  margin: 0;
  font-size: var(--fs-sm);
  color: var(--ink-3);
}

.grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}
.grid3 {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: var(--fs-sm);
  margin-bottom: 12px;
  min-width: 0;
}
.field .lab {
  font-weight: 500;
  color: var(--ink);
}
.field input,
.field select {
  min-width: 0;
  color: var(--ink);
  background: var(--sunken);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 7px 9px;
  font: inherit;
  font-size: var(--fs-sm);
}
.check {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: var(--fs-sm);
  color: var(--ink);
  margin-bottom: 10px;
}
.check input {
  accent-color: var(--accent);
  margin-top: 2px;
}
.muted {
  margin: 0 0 10px;
  font-size: var(--fs-xs);
  color: var(--ink-3);
  line-height: 1.6;
}
.ok {
  margin: 0 0 8px;
  font-size: var(--fs-sm);
  color: var(--accent);
}
.warn {
  margin: 0 0 8px;
  font-size: var(--fs-sm);
  color: var(--need-ink);
}
.status-line {
  margin: 0 0 10px;
  font-size: var(--fs-sm);
  color: var(--ink);
}
.msg {
  margin: 0 0 8px;
  padding: 8px 12px;
  font-size: var(--fs-sm);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
}
.msg--err {
  color: var(--fail);
  border-color: var(--fail);
}
.msg--ok {
  color: var(--accent);
  border-color: var(--accent);
}
.actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.btn {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 7px 12px;
  font: inherit;
  font-size: var(--fs-sm);
  color: var(--ink);
  background: transparent;
}
.btn--primary {
  color: var(--accent-ink);
  background: var(--accent);
  border-color: var(--accent);
  font-weight: 600;
}
.btn:disabled {
  cursor: not-allowed;
  opacity: 0.45;
}
@media (max-width: 760px) {
  .grid,
  .grid3 {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
