<script setup lang="ts">
// Schedules 列表（cron 定时调度，AUTO-02）：轮询 listSchedules（2.5s），Page
// Visibility 暂停/恢复，项目筛选（ui/ProjectFilter，?project= 是唯一真源）。行内操作：
// 立即运行 / 编辑 / 启停 / 删除；点击行展开被调度的 JobRequest 摘要。
// UI 重设计 P5：参数「⚙」、立即执行参数表单进 ui/Sheet、编辑进 ui/Drawer（设计稿 §7.2）。
import { runnerLabel } from '../utils/runnerDisplay'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { Cog } from '@lucide/vue'
import Icon from '../ui/Icon.vue'
import { createLiveTopic } from '../utils/useLiveTopic'
import { useRouter } from 'vue-router'
import { useWhoami } from '../store/whoami'
import {
  deleteSchedule,
  listSchedules,
  patchSchedule,
  runSchedule,
  setScheduleEnabled,
} from '../api/client'
import { fmtDateTime } from '../api/time'
import { formatRelative } from '../utils/format'
import { boolValue, envPreview, hasParams, paramControl, paramDefaults } from '../utils/scheduleParams'
import { t } from '../i18n'
import ProjectFilter from '../ui/ProjectFilter.vue'
import Sheet from '../ui/Sheet.vue'
import Drawer from '../ui/Drawer.vue'
import { useProjectFilter } from '../ui/projectFilter'
import { useListReturn } from '../ui/useListReturn'
import type { Schedule, ScheduleParam } from '../api/types'

const router = useRouter()
// 调用方上限只读时，新建入口一律不摆出来（服务端也会 403）。
const { readOnly } = useWhoami()

// 项目筛选：值进 URL 的 ?project=（唯一真源）。
const { project: projectFilter, setProject, options: projectOptions, loadOptions, loadError } = useProjectFilter('/schedules')

useListReturn('schedules', '/schedules')

const schedules = ref<Schedule[]>([])
const loading = ref(false)
const error = ref('')
const notice = ref('')
// 正在执行操作的 schedule id（禁用该行按钮，避免重复点击）
const busy = ref<Set<string>>(new Set())
// 展开显示 request 摘要的 schedule id
const expanded = ref<Set<string>>(new Set())

async function fetchSchedules(): Promise<void> {
  loading.value = true
  try {
    const resp = await listSchedules(projectFilter.value || undefined)
    schedules.value = resp.schedules ?? []
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

watch(projectFilter, () => {
  void fetchSchedules()
})

function setBusy(id: string, on: boolean): void {
  const next = new Set(busy.value)
  if (on) {
    next.add(id)
  } else {
    next.delete(id)
  }
  busy.value = next
}

function toggleExpand(id: string): void {
  const next = new Set(expanded.value)
  if (next.has(id)) {
    next.delete(id)
  } else {
    next.add(id)
  }
  expanded.value = next
}

async function onRun(s: Schedule, params?: Record<string, string>): Promise<void> {
  notice.value = ''
  error.value = ''
  setBusy(s.id, true)
  try {
    const job = await runSchedule(s.id, params)
    notice.value = t('schedules.triggeredNotice', { name: s.name, id: job.id })
    await fetchSchedules()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    setBusy(s.id, false)
  }
}

// ---- 立即执行：有参数时先弹表单（设计 §D5），无参数仍一键执行 ----
const runOpen = ref(false)
const runTarget = ref<Schedule | null>(null)
const runValues = ref<Record<string, string>>({})
const runPreview = computed(() => envPreview(runTarget.value?.params, runValues.value))
const runLabel = computed(() => t('schedules.runForm.title', { name: runTarget.value?.name ?? '' }))

function startRun(s: Schedule): void {
  if (!hasParams(s.params)) {
    void onRun(s)
    return
  }
  error.value = ''
  notice.value = ''
  runTarget.value = s
  runValues.value = paramDefaults(s.params)
  runOpen.value = true
}

function setRunValue(name: string, value: string): void {
  runValues.value = { ...runValues.value, [name]: value }
}

async function submitRun(): Promise<void> {
  const s = runTarget.value
  if (!s) return
  runOpen.value = false
  await onRun(s, runValues.value)
}

// ---- 编辑（设计 §D4/D5）：cron/at、标题、参数声明、启停、超时 ----
const editOpen = ref(false)
const editTarget = ref<Schedule | null>(null)
const editTitle = ref('')
const editCron = ref('')
// editAt 是一次性任务的触发时间（datetime-local 的本地写法）。
const editAt = ref('')
const editTimeout = ref('')
const editEnabled = ref(true)
const editParams = ref<ScheduleParam[]>([])
const editLabel = computed(() => t('schedules.editForm.title', { name: editTarget.value?.name ?? '' }))

function openEdit(s: Schedule): void {
  error.value = ''
  notice.value = ''
  editTarget.value = s
  editTitle.value = s.name
  editCron.value = s.type === 'once' ? '' : s.cron
  editAt.value = s.type === 'once' ? toLocalInput(s.next_run_at) : ''
  editTimeout.value = String(s.request.timeout_sec ?? 0)
  editEnabled.value = s.enabled === 1
  editParams.value = (s.params ?? []).map((p) => ({ ...p, choices: p.choices ? [...p.choices] : undefined }))
  editOpen.value = true
}

// toLocalInput / fromLocalInput 在 unix 秒与 <input type=datetime-local> 的本地写法之间换算。
function toLocalInput(sec: number): string {
  if (!sec) return ''
  const d = new Date(sec * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function fromLocalInput(v: string): number {
  if (!v) return 0
  const ms = new Date(v).getTime()
  return Number.isFinite(ms) ? Math.floor(ms / 1000) : 0
}

function addParamRow(): void {
  editParams.value = [...editParams.value, { name: '', type: 'string', default: '', desc: '' }]
}

function removeParamRow(i: number): void {
  editParams.value = editParams.value.filter((_, idx) => idx !== i)
}

function setParamField(i: number, key: 'name' | 'type' | 'default' | 'desc', value: string): void {
  editParams.value = editParams.value.map((p, idx) => (idx === i ? { ...p, [key]: value } : p))
}

function setParamChoices(i: number, value: string): void {
  const choices = value
    .split(',')
    .map((c) => c.trim())
    .filter((c) => c !== '')
  editParams.value = editParams.value.map((p, idx) =>
    idx === i ? { ...p, choices: choices.length > 0 ? choices : undefined } : p,
  )
}

async function saveEdit(): Promise<void> {
  const s = editTarget.value
  if (!s) return
  error.value = ''
  notice.value = ''
  setBusy(s.id, true)
  try {
    const params = editParams.value.filter((p) => p.name.trim() !== '')
    const timeout = Number(editTimeout.value.trim() || '0')
    // 一次性任务改的是触发时间（at），cron 任务改的是表达式；两者互斥（服务端也是）。
    const at = s.type === 'once' ? fromLocalInput(editAt.value) : 0
    // 只在启停真的改了时才发 enabled：服务端对 enabled:true 会额外要 job.submit，
    // 只改标题的人不应该被这条要求挡住。
    const enabledChanged = editEnabled.value !== (s.enabled === 1)
    const updated = await patchSchedule(s.id, {
      title: editTitle.value.trim(),
      params,
      timeout_sec: Number.isFinite(timeout) ? timeout : 0,
      ...(enabledChanged ? { enabled: editEnabled.value } : {}),
      ...(s.type === 'once' ? (at > 0 ? { at } : {}) : { cron: editCron.value.trim() }),
    })
    editOpen.value = false
    notice.value = t('schedules.saved', { name: updated.name, next: fmtDateTime(updated.next_run_at) })
    await fetchSchedules()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    setBusy(s.id, false)
  }
}

async function onToggleEnabled(s: Schedule): Promise<void> {
  notice.value = ''
  error.value = ''
  setBusy(s.id, true)
  try {
    await setScheduleEnabled(s.id, s.enabled === 0)
    await fetchSchedules()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    setBusy(s.id, false)
  }
}

async function onDelete(s: Schedule): Promise<void> {
  if (!window.confirm(t('schedules.deleteConfirm', { name: s.name }))) {
    return
  }
  notice.value = ''
  error.value = ''
  setBusy(s.id, true)
  try {
    await deleteSchedule(s.id)
    notice.value = t('schedules.deleted', { name: s.name })
    await fetchSchedules()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    setBusy(s.id, false)
  }
}

function openJob(id: string): void {
  void router.push(`/jobs/${encodeURIComponent(id)}`)
}

function shortId(id: string): string {
  return id.length > 12 ? id.slice(-12) : id
}

function scheduleType(s: Schedule): 'cron' | 'once' {
  return s.type === 'once' ? 'once' : 'cron'
}

function isOnceTriggered(s: Schedule): boolean {
  return scheduleType(s) === 'once' && s.enabled === 0 && s.last_run_at > 0
}

// request 的执行摘要（agent/runner/cwd + prompt 首行 / cmd）
function reqSummary(s: Schedule): string {
  const r = s.request
  const parts: string[] = []
  if (r.cmd && r.cmd.length) {
    parts.push(`cmd: ${r.cmd.join(' ')}`)
  } else if (r.prompt) {
    parts.push(`prompt: ${r.prompt.split('\n')[0]}`)
  }
  return parts.join('  ')
}

// Q3：`schedules` 主题的失效通知触发重拉；WS 断开超过 15s 才由 30s 一次的兜底轮询接手（恢复后自动停）。
const liveSchedules = createLiveTopic('schedules', { initial: false, fetch: fetchSchedules })

onMounted(() => {
  void fetchSchedules()
  void loadOptions()
  liveSchedules.start()
})

onUnmounted(() => {
  liveSchedules.stop()
})
</script>

<template>
  <div class="page" data-test="schedules-page">
    <header class="page-head">
      <div class="head-main">
        <h1 class="title">{{ t('schedules.title') }}</h1>
        <p class="sub">
          {{ schedules.length ? t('schedules.subtitle', { n: schedules.length }) : t('schedules.subtitleEmpty') }}
        </p>
      </div>
      <div class="head-actions">
        <span class="poll-dot" :class="{ 'poll-dot--on': loading }" aria-hidden="true">●</span>
        <!-- N3：顶栏「新建 cron」去掉后，新建入口在这里。 -->
        <RouterLink v-if="!readOnly" to="/schedules/new" class="btn btn--primary" data-test="new-schedule">
          + {{ t('schedules.new') }}
        </RouterLink>
      </div>
    </header>

    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="notice" class="notice">{{ notice }}</p>

    <div class="list-bar">
      <ProjectFilter
        :model-value="projectFilter"
        :options="projectOptions"
        :error="loadError"
        @update:model-value="setProject"
      />
    </div>

    <div class="table" role="table">
      <div class="thead" role="row">
        <span class="col-en">{{ t('schedules.col.enabled') }}</span>
        <span class="col-name">{{ t('schedules.col.name') }}</span>
        <span class="col-cron">{{ t('schedules.col.cron') }}</span>
        <span class="col-next">{{ t('schedules.col.next') }}</span>
        <span class="col-last">{{ t('schedules.col.last') }}</span>
        <span class="col-act">{{ t('schedules.col.actions') }}</span>
      </div>

      <template v-for="s in schedules" :key="s.id">
        <div
          class="trow"
          role="row"
          tabindex="0"
          @click="toggleExpand(s.id)"
          @keydown.enter="toggleExpand(s.id)"
        >
          <span class="col-en">
            <button
              class="switch"
              type="button"
              role="switch"
              :aria-checked="s.enabled === 1"
              :aria-label="s.enabled ? t('schedules.disable') : t('schedules.enable')"
              :class="{ 'switch--on': s.enabled === 1 }"
              :disabled="busy.has(s.id)"
              data-test="schedule-enabled"
              @click.stop="onToggleEnabled(s)"
            >
              <span class="switch-knob" aria-hidden="true"></span>
            </button>
          </span>
          <span class="col-name">
            <span class="sch-name-line">
              <span class="type-badge" :class="`type-badge--${scheduleType(s)}`">
                {{ scheduleType(s) === 'cron' ? t('schedules.typeCron') : t('schedules.typeOnce') }}
              </span>
              <span class="sch-name" :title="s.name">{{ s.name }}</span>
            </span>
            <span class="sch-sub">
              <span class="sch-id mono" :title="s.id">{{ s.id }}</span>
              <span class="sch-pr">{{ s.project_key }} · {{ s.request.agent }} · {{ runnerLabel(s.request.runner) }}</span>
            </span>
          </span>
          <span class="col-cron mono" :title="s.cron || t('schedules.typeOnce')">
            {{ scheduleType(s) === 'cron' ? s.cron : t('schedules.typeOnce') }}
          </span>
          <span class="col-next">
            <template v-if="isOnceTriggered(s)">
              <span class="next-off">{{ t('schedules.triggered') }}</span>
              <span class="next-rel">{{ fmtDateTime(s.last_run_at) }}</span>
            </template>
            <template v-else-if="s.enabled">
              <span class="next-abs">{{ fmtDateTime(s.next_run_at) }}</span>
              <span class="next-rel">{{ formatRelative(s.next_run_at) }}</span>
            </template>
            <span v-else class="next-off">{{ t('schedules.disabled') }}</span>
          </span>
          <span class="col-last">
            <template v-if="s.last_job_id">
              <a class="last-link mono" :title="s.last_job_id" @click.stop="openJob(s.last_job_id)">
                {{ shortId(s.last_job_id) }}
              </a>
              <span class="last-abs">{{ fmtDateTime(s.last_run_at) }}</span>
            </template>
            <span v-else class="last-none">{{ t('schedules.never') }}</span>
          </span>
          <span class="col-act" @click.stop>
            <button
              class="act act--run"
              type="button"
              :disabled="busy.has(s.id)"
              :title="hasParams(s.params) ? t('schedules.runWithParams') : t('schedules.runPlain')"
              data-test="schedule-run"
              @click="startRun(s)"
            >
              {{ t('schedules.run') }}<Icon v-if="hasParams(s.params)" class="gear" :icon="Cog" size="sm" data-test="schedule-param-gear" />
            </button>
            <button
              class="act"
              type="button"
              :disabled="busy.has(s.id)"
              :title="t('schedules.editTitle')"
              data-test="schedule-edit"
              @click="openEdit(s)"
            >
              {{ t('schedules.edit') }}
            </button>
            <button
              class="act"
              type="button"
              :disabled="busy.has(s.id)"
              :title="s.enabled ? t('schedules.disable') : t('schedules.enable')"
              data-test="schedule-toggle"
              @click="onToggleEnabled(s)"
            >
              {{ s.enabled ? t('schedules.disable') : t('schedules.enable') }}
            </button>
            <button
              class="act act--del"
              type="button"
              :disabled="busy.has(s.id)"
              :title="t('schedules.delete')"
              data-test="schedule-delete"
              @click="onDelete(s)"
            >
              {{ t('schedules.deleteShort') }}
            </button>
          </span>
        </div>

        <!-- 展开：被调度的 JobRequest 摘要 -->
        <div v-if="expanded.has(s.id)" class="detail" data-test="schedule-detail">
          <div class="detail-grid">
            <span class="dk">{{ t('schedules.detailId') }}</span>
            <span class="dv dv--id mono" :title="s.id">{{ s.id }}</span>
            <span class="dk">{{ t('schedules.detailType') }}</span>
            <span class="dv">{{ scheduleType(s) }}</span>
            <span class="dk">{{ t('schedules.detailCatchUp') }}</span>
            <span class="dv">{{ s.catch_up ? 'true' : 'false' }}</span>
            <span class="dk">{{ t('schedules.detailCwd') }}</span>
            <span class="dv mono">{{ s.request.cwd || '.' }}</span>
            <template v-if="s.request.title">
              <span class="dk">{{ t('schedules.detailTitle') }}</span>
              <span class="dv">{{ s.request.title }}</span>
            </template>
            <span class="dk">{{ t('schedules.detailTimeout') }}</span>
            <span class="dv">
              <template v-if="s.request.timeout_sec">{{ s.request.timeout_sec }}s</template>
              <template v-else>{{ t('schedules.serverDefault') }}</template>
            </span>
            <template v-if="s.request.worker_id">
              <span class="dk">{{ t('schedules.detailWorkerId') }}</span>
              <span class="dv mono">{{ s.request.worker_id }}</span>
            </template>
            <template v-if="s.request.worker_labels && s.request.worker_labels.length">
              <span class="dk">{{ t('schedules.detailWorkerLabels') }}</span>
              <span class="dv mono">{{ s.request.worker_labels.join(', ') }}</span>
            </template>
            <template v-if="s.request.tags && s.request.tags.length">
              <span class="dk">{{ t('schedules.detailTags') }}</span>
              <span class="dv mono">{{ s.request.tags.join(', ') }}</span>
            </template>
            <template v-if="hasParams(s.params)">
              <span class="dk">{{ t('schedules.detailParams') }}</span>
              <span class="dv">
                <span v-for="p in s.params" :key="p.name" class="param-chip mono" :title="p.desc || ''">
                  {{ p.name }}<template v-if="p.type">:{{ p.type }}</template>=<q>{{ p.default }}</q>
                </span>
              </span>
            </template>
          </div>
          <pre v-if="reqSummary(s)" class="detail-cmd mono">{{ reqSummary(s) }}</pre>
        </div>
      </template>

      <div v-if="schedules.length === 0 && !error" class="empty">
        <p>{{ t('schedules.empty') }}</p>
        <p class="empty-hint">{{ t('schedules.emptyHint') }}</p>
        <RouterLink v-if="!readOnly" to="/schedules/new" class="btn btn--primary" data-test="new-schedule-empty">
          + {{ t('schedules.new') }}
        </RouterLink>
      </div>
    </div>

    <!-- 立即执行参数表单（设计 §D5）：按类型生成控件、预填默认、底部预览将设置的环境变量 -->
    <Sheet v-model="runOpen" :label="runLabel" data-test="run-param-form">
      <p class="modal-hint">{{ t('schedules.runForm.hint') }}</p>
      <div v-for="p in runTarget?.params || []" :key="p.name" class="pfield">
        <label class="prow">
          <span class="pname">{{ p.name }}<span class="ptype">{{ p.type || 'string' }}</span></span>
          <template v-if="paramControl(p) === 'toggle'">
            <input
              type="checkbox"
              :data-test="`param-${p.name}`"
              :checked="runValues[p.name] === '1'"
              @change="setRunValue(p.name, boolValue(($event.target as HTMLInputElement).checked))"
            />
          </template>
          <template v-else-if="paramControl(p) === 'select'">
            <select
              :data-test="`param-${p.name}`"
              :value="runValues[p.name]"
              @change="setRunValue(p.name, ($event.target as HTMLSelectElement).value)"
            >
              <option v-for="c in p.choices || []" :key="c" :value="c">
                {{ c === '' ? t('schedules.runForm.empty') : c }}
              </option>
            </select>
          </template>
          <template v-else-if="paramControl(p) === 'number'">
            <input
              type="number"
              :data-test="`param-${p.name}`"
              :value="runValues[p.name]"
              @input="setRunValue(p.name, ($event.target as HTMLInputElement).value)"
            />
          </template>
          <template v-else>
            <input
              type="text"
              :data-test="`param-${p.name}`"
              :value="runValues[p.name]"
              @input="setRunValue(p.name, ($event.target as HTMLInputElement).value)"
            />
          </template>
        </label>
        <p v-if="p.desc" class="pdesc">{{ p.desc }}</p>
      </div>
      <div class="preview" data-test="run-param-preview">
        <div class="preview-head">{{ t('schedules.runForm.env') }}</div>
        <div v-for="e in runPreview" :key="e.name" class="preview-row mono">
          <span class="pk">{{ e.name }}</span>=<span class="pv">{{ e.value }}</span>
        </div>
      </div>
      <div class="modal-act">
        <button class="act" type="button" @click="runOpen = false">{{ t('schedules.runForm.cancel') }}</button>
        <button class="act act--primary" type="button" data-test="run-param-submit" @click="submitRun()">
          {{ t('schedules.runForm.exec') }}
        </button>
      </div>
    </Sheet>

    <!-- 编辑（设计 §D4/D5）：cron + 下次运行预览、标题、参数声明增删改、启停 -->
    <Drawer v-model="editOpen" :label="editLabel" width="clamp(520px, 44vw, 820px)" data-test="schedule-edit-form">
      <p class="modal-hint">
        {{ t('schedules.editForm.hintFixed') }}<br />
        <template v-if="editTarget?.type !== 'once'">
          {{ t('schedules.editForm.hintCron', { next: fmtDateTime(editTarget?.next_run_at ?? 0) }) }}
        </template>
        <template v-else>{{ t('schedules.editForm.hintOnce') }}</template>
      </p>
      <label v-if="editTarget?.type !== 'once'" class="erow">
        <span class="ekey">{{ t('schedules.editForm.cron') }}</span>
        <input v-model="editCron" type="text" data-test="edit-cron" />
      </label>
      <label v-else class="erow">
        <span class="ekey">{{ t('schedules.editForm.at') }}</span>
        <input v-model="editAt" type="datetime-local" data-test="edit-at" />
      </label>
      <label class="erow">
        <span class="ekey">{{ t('schedules.editForm.titleField') }}</span>
        <input v-model="editTitle" type="text" data-test="edit-title" />
      </label>
      <label class="erow">
        <span class="ekey">{{ t('schedules.editForm.timeout') }}</span>
        <input v-model="editTimeout" type="number" data-test="edit-timeout" />
      </label>
      <label class="erow">
        <span class="ekey">{{ t('schedules.editForm.enabled') }}</span>
        <input v-model="editEnabled" type="checkbox" data-test="edit-enabled" />
      </label>

      <div class="params-editor">
        <div class="preview-head">{{ t('schedules.editForm.params') }}</div>
        <div v-for="(p, i) in editParams" :key="i" class="prow prow--edit" :data-test="`edit-param-row-${i}`">
          <input
            class="pcol-name mono"
            type="text"
            :placeholder="t('schedules.editForm.paramName')"
            :value="p.name"
            @input="setParamField(i, 'name', ($event.target as HTMLInputElement).value)"
          />
          <select
            class="pcol-type"
            :value="p.type || 'string'"
            @change="setParamField(i, 'type', ($event.target as HTMLSelectElement).value)"
          >
            <option value="string">string</option>
            <option value="int">int</option>
            <option value="bool">bool</option>
            <option value="choice">choice</option>
          </select>
          <input
            class="pcol-def mono"
            type="text"
            :placeholder="t('schedules.editForm.paramDefault')"
            :value="p.default"
            @input="setParamField(i, 'default', ($event.target as HTMLInputElement).value)"
          />
          <input
            v-if="p.type === 'choice'"
            class="pcol-def mono"
            type="text"
            :placeholder="t('schedules.editForm.paramChoices')"
            :value="(p.choices || []).join(', ')"
            @input="setParamChoices(i, ($event.target as HTMLInputElement).value)"
          />
          <input
            class="pcol-desc mono"
            type="text"
            :placeholder="t('schedules.editForm.paramDesc')"
            :value="p.desc"
            @input="setParamField(i, 'desc', ($event.target as HTMLInputElement).value)"
          />
          <button class="act act--del" type="button" :title="t('schedules.editForm.removeParam')" @click="removeParamRow(i)">
            −
          </button>
        </div>
        <button class="act" type="button" data-test="edit-param-add" @click="addParamRow()">
          {{ t('schedules.editForm.addParam') }}
        </button>
      </div>

      <div class="modal-act">
        <button class="act" type="button" @click="editOpen = false">{{ t('schedules.editForm.cancel') }}</button>
        <button class="act act--primary" type="button" data-test="edit-save" @click="saveEdit()">
          {{ t('schedules.editForm.save') }}
        </button>
      </div>
    </Drawer>
  </div>
</template>

<style scoped>
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
  color: var(--ink);
}
.sub {
  margin: 2px 0 0;
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.head-actions {
  display: flex;
  align-items: center;
  gap: var(--s2);
}
.poll-dot {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
}
.poll-dot--on {
  color: var(--accent);
}
.btn {
  display: inline-flex;
  align-items: center;
  min-height: 32px;
  padding: 0 var(--s3);
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
  white-space: nowrap;
}
.btn:hover {
  text-decoration: none;
  border-color: var(--accent);
}
.btn:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.btn--primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}
.btn--primary:hover {
  background: var(--accent-hover);
  border-color: var(--accent-hover);
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
.notice {
  color: var(--done);
  font-size: var(--fs-sm);
  margin: 0 0 var(--s3);
}
.list-bar {
  display: flex;
  align-items: center;
  gap: var(--s3);
  margin-bottom: var(--s3);
  min-width: 0;
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
  grid-template-columns: 56px minmax(180px, 1fr) 150px 150px 132px 186px;
  align-items: center;
  gap: var(--s3);
  padding: 9px var(--s3);
}
.thead {
  background: var(--surface-2);
  border-bottom: 1px solid var(--line);
  font-size: var(--fs-xs);
  letter-spacing: 0.06em;
  color: var(--ink-3);
  text-transform: uppercase;
}
.trow {
  border-bottom: 1px solid var(--line);
  cursor: pointer;
  font-size: var(--fs-sm);
  outline: none;
}
.trow:hover {
  background: var(--surface-2);
}
.trow:focus-visible {
  box-shadow: inset 2px 0 0 var(--accent);
}
.col-en {
  display: flex;
  justify-content: center;
}
/* 开关：role=switch，轨道 + 圆钮；只靠位置和文字区分，不单靠颜色（§10）。 */
.switch {
  position: relative;
  width: 34px;
  height: 20px;
  padding: 0;
  border: 1px solid var(--line-strong);
  border-radius: 999px;
  background: var(--surface-2);
  cursor: pointer;
  flex: none;
}
.switch-knob {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: var(--ink-3);
  transition: transform var(--dur-fast) var(--ease);
}
.switch--on {
  background: var(--accent-soft);
  border-color: var(--accent);
}
.switch--on .switch-knob {
  transform: translateX(14px);
  background: var(--accent);
}
.switch:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.switch:disabled {
  opacity: 0.45;
  cursor: default;
}
.col-name {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.sch-name-line {
  display: flex;
  align-items: center;
  gap: var(--s2);
  min-width: 0;
}
.type-badge {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 1px 5px;
  font-size: var(--fs-xs);
  line-height: 1.4;
  text-transform: uppercase;
  flex: none;
}
.type-badge--cron {
  color: var(--accent);
  border-color: var(--accent);
}
.type-badge--once {
  color: var(--run);
  border-color: var(--run);
}
.sch-name {
  color: var(--ink);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sch-sub {
  display: flex;
  gap: var(--s2);
  font-size: var(--fs-xs);
  min-width: 0;
}
.sch-id {
  color: var(--accent);
  flex: none;
}
.sch-pr {
  color: var(--ink-3);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-cron {
  color: var(--ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.col-next,
.col-last {
  display: flex;
  flex-direction: column;
  gap: 1px;
  font-size: var(--fs-sm);
}
.next-abs,
.last-link {
  color: var(--ink);
}
.next-rel,
.last-abs {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.next-off,
.last-none {
  color: var(--ink-3);
}
.last-link {
  color: var(--accent);
  cursor: pointer;
}
.last-link:hover {
  text-decoration: underline;
}
.col-act {
  display: flex;
  gap: var(--s1);
  justify-content: flex-end;
}
.act {
  background: transparent;
  color: var(--ink-2);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 4px 8px;
  font-size: var(--fs-xs);
  cursor: pointer;
}
.act:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--accent);
}
.act:disabled {
  opacity: 0.45;
  cursor: default;
}
.act--run:hover:not(:disabled) {
  border-color: var(--done);
  color: var(--done);
}
.act--del:hover:not(:disabled) {
  border-color: var(--fail);
  color: var(--fail);
}
.act--primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}
.detail {
  border-bottom: 1px solid var(--line);
  background: var(--surface-2);
  padding: var(--s2) var(--s3) var(--s3) 70px;
  font-size: var(--fs-sm);
}
.detail-grid {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 3px var(--s3);
  align-items: baseline;
}
.dk {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.dv {
  color: var(--ink);
  word-break: break-word;
}
.dv--id {
  user-select: all; /* click-select the whole schedule id for easy copy */
  word-break: break-all;
}
.detail-cmd {
  margin: var(--s2) 0 0;
  padding: var(--s2) 10px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  color: var(--ink);
  white-space: pre-wrap;
  word-break: break-word;
  font-size: var(--fs-sm);
}
.empty {
  padding: var(--s6) var(--s3);
  text-align: center;
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.empty p {
  margin: 0 0 var(--s2);
}
.empty-hint {
  color: var(--ink-3);
}
.gear {
  margin-left: 4px;
  font-size: 10px;
  opacity: 0.85;
}
.param-chip {
  display: inline-block;
  margin: 0 6px 2px 0;
  padding: 0 5px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  font-size: var(--fs-xs);
  color: var(--ink);
}
.param-chip q {
  color: var(--accent);
}

/* 立即执行参数表单 / 编辑表单的内容（外壳是 Sheet / Drawer） */
.modal-hint {
  margin: 0 0 var(--s3);
  color: var(--ink-3);
  font-size: var(--fs-sm);
  line-height: 1.6;
}
.pfield {
  margin-bottom: var(--s2);
}
.prow {
  display: flex;
  align-items: center;
  gap: var(--s2);
}
.pname {
  flex: 1 1 45%;
  color: var(--ink);
  word-break: break-all;
}
.ptype {
  margin-left: 6px;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.prow input[type='text'],
.prow input[type='number'],
.prow select,
.erow input[type='text'],
.erow input[type='number'],
.prow--edit input,
.prow--edit select {
  flex: 1 1 auto;
  min-width: 0;
  background: var(--surface-2);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  padding: 5px 7px;
  font: inherit;
  font-size: var(--fs-sm);
}
.pdesc {
  margin: 3px 0 0;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.preview {
  margin: var(--s3) 0;
  padding: var(--s2) 10px;
  background: var(--surface-2);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
}
.preview-head {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  margin-bottom: 4px;
}
.preview-row {
  color: var(--ink);
  font-size: var(--fs-xs);
  word-break: break-all;
}
.preview-row .pk {
  color: var(--accent);
}
.preview-row .pv {
  color: var(--done);
}
.erow {
  display: flex;
  align-items: center;
  gap: var(--s2);
  margin-bottom: var(--s2);
}
.ekey {
  flex: 0 0 80px;
  color: var(--ink-3);
}
.params-editor {
  margin-top: var(--s3);
}
.prow--edit {
  margin-bottom: 6px;
}
.pcol-name {
  flex: 1 1 130px;
}
.pcol-type {
  flex: 0 0 84px;
}
.pcol-def {
  flex: 1 1 110px;
}
.pcol-desc {
  flex: 1 1 130px;
}
.modal-act {
  display: flex;
  justify-content: flex-end;
  gap: var(--s2);
  margin-top: var(--s4);
}

/* 手机：列头隐藏，一行调度改成卡片。 */
@media (max-width: 760px) {
  .page-head {
    flex-direction: column;
    gap: var(--s2);
  }
  .btn,
  .act,
  .last-link {
    min-height: 40px;
  }
  /* 开关保持轨道高度（拉成 40px 会变成胶囊），用两侧的内边距凑点按区域。 */
  .switch {
    min-height: 20px;
  }
  .col-en {
    padding: 10px 0;
  }
  .thead {
    display: none;
  }
  .trow {
    grid-template-columns: 44px 1fr;
    grid-template-areas:
      'en name'
      'en cron'
      'en next'
      'en act';
    row-gap: 6px;
  }
  .col-en {
    grid-area: en;
    align-items: flex-start;
    padding-top: 4px;
  }
  .col-name {
    grid-area: name;
  }
  .col-cron {
    grid-area: cron;
  }
  .col-next {
    grid-area: next;
    flex-direction: row;
    gap: var(--s2);
  }
  .col-last {
    display: none;
  }
  .col-act {
    grid-area: act;
    justify-content: flex-start;
    flex-wrap: wrap;
  }
  .detail {
    padding-left: var(--s3);
  }
}
</style>
