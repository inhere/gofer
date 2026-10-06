<script setup lang="ts">
// 设置 · 工作项：被动整理（整理器 agent / 参数 / 开关 / 阈值 / 每日上限）、请求超时、
// 自动交接与每日摘要。读 GET /v1/work-items/summarizer（有效值 + 整理器可用性），
// 写 PUT /v1/config/work（部分更新，需要 can_admin）；改动在下一个扫描周期生效。
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
  const t = text.trim()
  if (t === '') return []
  try {
    const v = JSON.parse(t)
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
    error.value = '整理器参数要写成 JSON 字符串数组，例如 ["--model","haiku","--tools","","--no-session-persistence"]'
    return
  }
  const idle = intOf(form.summarize_idle_min)
  const gap = intOf(form.summarize_min_interval_min)
  const daily = intOf(form.summarize_daily_limit)
  const timeout = intOf(form.request_timeout_min)
  if (idle === null || gap === null || daily === null || timeout === null || idle < 1 || gap < 1 || timeout < 1) {
    error.value = '阈值要填正整数（每日上限填 -1 表示不限）。'
    return
  }
  if (!/^\d{1,2}:\d{2}$/.test(form.digest_time.trim())) {
    error.value = '摘要时间写成 HH:MM，例如 09:00。'
    return
  }
  const throttle = intOf(form.needs_me_throttle_min)
  if (throttle === null || throttle < 1) {
    error.value = '「等我」通知的节流间隔要填正整数（分钟）。'
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
    notice.value = '已保存，下一个扫描周期生效。'
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
    stewardError.value = '巡检时间写成 HH:MM，例如 08:50（留空 = 摘要前 10 分钟）。'
    return
  }
  const idle = Number.parseInt(sform.idle_end_min, 10)
  if (!Number.isFinite(idle) || idle < 1) {
    stewardError.value = '空闲结束分钟要填正整数。'
    return
  }
  stewardBusy.value = true
  try {
    await putConfigSteward(stewardDiff.value)
    stewardNotice.value = '已保存。'
    await loadSteward()
  } catch (e) {
    stewardError.value = errText(e)
  } finally {
    stewardBusy.value = false
  }
}

async function stewardAction(kind: 'start' | 'restart' | 'stop'): Promise<void> {
  if (stewardBusy.value) return
  if (kind === 'restart' && !window.confirm('重启管家？当前会话会结束并重建。')) return
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
    <div class="head">
      <span class="eyebrow mono">WORK</span>
      <h2>工作项</h2>
    </div>

    <p v-if="error" class="msg msg--err mono" data-test="ws-error">{{ error }}</p>
    <p v-if="notice" class="msg msg--ok mono" data-test="ws-notice">{{ notice }}</p>
    <p v-if="loading && !status" class="muted mono">加载中…</p>

    <template v-if="status">
      <section class="card">
        <h3 class="mono">被动整理</h3>
        <p v-if="status.available" class="ok mono" data-test="ws-available">整理器可用：{{ status.agent }}　今天已自动整理 {{ status.daily_used }} 次<template v-if="status.daily_limit > 0"> / 上限 {{ status.daily_limit }}</template></p>
        <p v-else class="warn mono" data-test="ws-unavailable">整理功能暂不可用：{{ status.reason || '没有可用的整理器 agent' }}。下面指定一个已安装的 cli-agent 后即可恢复。</p>
        <p class="muted mono">会话空闲、离线或结束后，用一次性只读、不带工具的小模型读它最近的对话，补全工作项的目标 / 阻塞 / 下一步。人或会话写过的内容只给「整理建议」，不会被覆盖。</p>

        <label class="check mono"><input v-model="form.summarize_enabled" type="checkbox" data-test="ws-enabled" /> 自动整理（关闭后只能手动点「整理」）</label>
        <div class="grid">
          <label class="field mono">整理器 agent<input v-model="form.summarizer_agent" type="text" placeholder="claude" data-test="ws-agent" /></label>
          <label class="field mono">整理用的项目（可选）<input v-model="form.summarizer_project" type="text" placeholder="留空 = 默认使用 default（~/.gofer/workspace）" data-test="ws-project" /></label>
        </div>
        <p class="muted mono" data-test="ws-project-hint">
          <template v-if="form.summarizer_project.trim()">整理 job 固定在项目 {{ form.summarizer_project.trim() }} 里跑。</template>
          <template v-else>留空：优先用工作项自己的项目（需允许整理器 agent 与本机 runner），否则默认使用 default（~/.gofer/workspace）。</template>
          <template v-if="status.effective_project">　当前解析：{{ status.effective_project }}<template v-if="status.effective_dir">（{{ status.effective_dir }}）</template></template>
        </p>
        <label class="field mono">额外参数（JSON 数组，默认给 claude 配了便宜模型且不带工具）
          <input v-model="form.argsText" type="text" placeholder='["--model","haiku","--tools","","--no-session-persistence"]' data-test="ws-args" />
        </label>
        <div class="grid3">
          <label class="field mono">空闲多久后整理（分钟）<input v-model="form.summarize_idle_min" type="number" min="1" data-test="ws-idle" /></label>
          <label class="field mono">同一会话最小间隔（分钟）<input v-model="form.summarize_min_interval_min" type="number" min="1" /></label>
          <label class="field mono">每日自动整理上限（-1 不限）<input v-model="form.summarize_daily_limit" type="number" data-test="ws-daily" /></label>
        </div>
      </section>

      <section class="card">
        <h3 class="mono">请求与交接</h3>
        <label class="check mono"><input v-model="form.auto_handoff" type="checkbox" data-test="ws-handoff" /> 搁置 / 标「需现场」时自动请会话写交接（只对在运行的会话；不在运行则改为整理）</label>
        <label class="field mono">请求超时（分钟）：会话没回就标超时并改为整理<input v-model="form.request_timeout_min" type="number" min="1" /></label>
      </section>

      <section class="card">
        <h3 class="mono">每日摘要</h3>
        <label class="check mono"><input v-model="form.digest_enabled" type="checkbox" /> 每天推送工作摘要</label>
        <label class="field mono">推送时间（HH:MM）<input v-model="form.digest_time" type="text" placeholder="09:00" /></label>
      </section>

      <section class="card" data-test="needs-me-section">
        <h3 class="mono">「等我」通知</h3>
        <label class="check mono"><input v-model="form.needs_me_notify" type="checkbox" data-test="ws-needsme" /> 工作项进入「等我」时推送通知（work.needs_me，默认关闭；自动或手动进入都算）</label>
        <label class="field mono">同一工作项的最小通知间隔（分钟）<input v-model="form.needs_me_throttle_min" type="number" min="1" data-test="ws-needsme-throttle" /></label>
      </section>

      <section class="card" data-test="steward-section">
        <h3 class="mono">管家</h3>
        <p class="muted mono">管家是一个常驻的 ACP 会话：只调度和整理工作项（读、记、提醒、建议合并、请会话汇报），不替你完成或放弃任何事，也不能提交 job / 改配置。关键信息都落在工作项和笔记里，换 agent 随时可以接着做。</p>
        <p v-if="stewardError" class="msg msg--err mono" data-test="steward-error">{{ stewardError }}</p>
        <p v-if="stewardNotice" class="msg msg--ok mono" data-test="steward-notice">{{ stewardNotice }}</p>
        <template v-if="stewardStatus">
          <p class="mono status-line" data-test="steward-status">
            状态：<strong>{{ stateLabel(stewardStatus.state, stewardStatus.enabled) }}</strong>
            <template v-if="stewardStatus.job_id">　会话 <RouterLink :to="`/jobs/${encodeURIComponent(stewardStatus.job_id)}`" data-test="steward-job">{{ stewardStatus.job_id }}</RouterLink>（{{ stewardStatus.job_agent }}）</template>
          </p>
          <p v-if="stewardStatus.agent_error" class="warn mono" data-test="steward-agent-error">{{ stewardStatus.agent_error }}</p>
        </template>
        <label class="check mono"><input v-model="sform.enabled" type="checkbox" data-test="steward-enabled" /> 启用管家（默认关闭；开启后按需启动，空闲自动结束）</label>
        <div class="grid">
          <label class="field mono">管家 agent（已安装的 acp-agent）
            <select v-model="sform.agent" data-test="steward-agent">
              <option value="">（未选择）</option>
              <option v-for="o in agentOptions" :key="o.key" :value="o.key">{{ o.label }}</option>
            </select>
          </label>
          <label class="field mono">运行项目（可选）<input v-model="sform.project" type="text" placeholder="留空 = 内置 default 项目" data-test="steward-project" /></label>
        </div>
        <p v-if="switchWarning" class="warn mono" data-test="steward-switch-warning">{{ switchWarning }}</p>
        <div class="grid3">
          <label class="field mono">每日巡检时间（HH:MM）<input v-model="sform.review_time" type="text" :placeholder="stewardSettings ? `留空 = 摘要前 10 分钟（${stewardSettings.review_time}）` : '08:50'" data-test="steward-review-time" /></label>
          <label class="field mono">空闲多久结束会话（分钟）<input v-model="sform.idle_end_min" type="number" min="1" data-test="steward-idle" /></label>
        </div>
        <label class="check mono"><input v-model="sform.event_wake" type="checkbox" data-test="steward-event-wake" /> 会话离线 / 到期 / 草稿较多时唤醒管家整理（批量，节流 30 分钟；关闭则只记下，留给下次巡检）</label>
        <p v-if="stewardStatus" class="muted mono" data-test="steward-notes-info">
          管家笔记 v{{ stewardStatus.notes_version }}（{{ notesSizeLabel(stewardStatus.notes_bytes) }}）
          <template v-if="stewardStatus.notes_need_slim">　<span class="warn">笔记超过 8KB，下次巡检会精简。</span></template>
          <button class="btn mono" type="button" data-test="steward-notes-open" @click="notesOpen = true">查看 / 编辑笔记</button>
        </p>
        <div class="actions">
          <button class="btn primary mono" type="button" :disabled="stewardBusy || !stewardDirty" data-test="steward-save" @click="saveSteward">{{ stewardBusy ? '处理中…' : '保存管家设置' }}</button>
          <button class="btn mono" type="button" :disabled="stewardBusy || !stewardStatus?.enabled || stewardStatus?.state !== 'not_started'" data-test="steward-start" @click="stewardAction('start')">启动</button>
          <button class="btn mono" type="button" :disabled="stewardBusy || !stewardStatus?.enabled" data-test="steward-restart" @click="stewardAction('restart')">重启管家</button>
          <button class="btn mono" type="button" :disabled="stewardBusy || stewardStatus?.state === 'not_started'" data-test="steward-stop" @click="stewardAction('stop')">停止</button>
        </div>
      </section>

      <div class="actions">
        <button class="btn primary mono" type="button" :disabled="saving || !dirty" data-test="ws-save" @click="save">{{ saving ? '保存中…' : '保存' }}</button>
        <button class="btn mono" type="button" :disabled="loading" @click="load">重新读取</button>
        <span v-if="dirty" class="muted mono">有未保存的修改</span>
      </div>
    </template>
    <StewardNotes v-if="notesOpen" @close="notesOpen = false" @changed="loadSteward" />
  </div>
</template>

<style scoped>
.work-settings { display: grid; gap: 14px; }
.head { display: flex; align-items: baseline; gap: 10px; }
.eyebrow { color: var(--phosphor); font-size: 10px; letter-spacing: 0.18em; }
h2 { margin: 0; color: var(--paper); font-size: 18px; }
h3 { margin: 0 0 6px; color: var(--paper); font-size: 13px; }
.card { display: grid; gap: 10px; border: 1px solid var(--line); border-radius: var(--radius); background: var(--panel); padding: 14px; }
.grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
.grid3 { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; }
.field { display: flex; flex-direction: column; gap: 4px; font-size: 11px; color: var(--queue); min-width: 0; }
.field input, .field select { min-width: 0; color: var(--paper); background: var(--ink); border: 1px solid var(--line); border-radius: var(--radius); padding: 6px 8px; font: inherit; font-size: 12px; }
.check { display: flex; align-items: flex-start; gap: 8px; font-size: 12px; color: var(--paper); }
.check input { accent-color: var(--phosphor); margin-top: 2px; }
.muted { margin: 0; font-size: 11px; color: var(--queue); }
.ok { margin: 0; font-size: 12px; color: var(--done); }
.warn { margin: 0; font-size: 12px; color: var(--warning, var(--fail)); }
.msg { margin: 0; padding: 8px 12px; font-size: 12px; border: 1px solid var(--line); border-radius: var(--radius); }
.msg--err { color: var(--fail); border-color: var(--fail); }
.msg--ok { color: var(--done); border-color: var(--done); }
.actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.btn { border: 1px solid var(--line); border-radius: var(--radius); padding: 7px 12px; cursor: pointer; color: var(--paper); background: transparent; }
.btn.primary { color: var(--ink); background: var(--phosphor); }
.btn:disabled { cursor: not-allowed; opacity: 0.45; }
@media (max-width: 640px) {
  .grid, .grid3 { grid-template-columns: minmax(0, 1fr); }
}
</style>
