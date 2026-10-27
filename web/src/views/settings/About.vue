<script setup lang="ts">
// WEB-12 关于页：server 的版本 / 地址 / 运行时长等自述信息，全部来自 GET /v1/stats。
//
// 只显示服务端**确实有**的字段（设计 §一：没有的就不显示那一行，不为了这一页加后端）：
//  - version：buildinfo.DisplayVersion()，构建时由 LDFLAGS 注入的 "vX.Y.Z (commit7)"，
//    所以版本与 commit 是同一行（没注入 commit 时就是纯版本号）。
//  - uptime_sec / server_time：进程运行时长与服务器当前时间。
//  - 服务器地址：控制台由 server 自己服务（请求走同源相对路径），当前页面的 origin 就是
//    控制台正在对话的那个地址。
//  - 构建时间 / worker wire 协议版本：/v1/stats 没有这两个字段，故不渲染（本期不改后端）。
// UI 重设计 P5-d：FormSection 分节 + 文案走 i18n（settings.about.*）。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { applyUpdate, cancelUpdateWait, checkUpdate, getStats, getUpdateStatus } from '../../api/client'
import { fmtAgo, fmtDateTime, fmtDuration } from '../../api/time'
import type { Stats, UpdateApply, UpdateStatus } from '../../api/types'
import UpdateApplyDialog from '../../components/UpdateApplyDialog.vue'
import UpdateManualNotice from '../../components/UpdateManualNotice.vue'
import { clearToken, isRemembered } from '../../store/auth'
import { useWhoami } from '../../store/whoami'
import { noteUpdateStatus } from '../../store/updateAvailable'
import { t } from '../../i18n'
import FormSection from '../../ui/FormSection.vue'

const router = useRouter()

const stats = ref<Stats | null>(null)
const error = ref('')
const loading = ref(false)
const loaded = ref(false)

const serverAddr = window.location.origin

// gofer-x5k4：这个浏览器是否记着登录（token 在 localStorage）。退出并忘记 = 两处存储都清掉。
const remembered = isRemembered()
function logoutForget(): void {
  clearToken()
  void router.replace('/access')
}

const version = computed(() => stats.value?.version || t('settings.about.versionUnknown'))
const uptime = computed(() => {
  const sec = stats.value?.uptime_sec
  return sec && sec > 0 ? fmtDuration(sec) : '—'
})
// server_time 是 Unix 毫秒（stats_handler.nowMillis），fmtDateTime 收秒。
const serverTime = computed(() => {
  const ms = stats.value?.server_time
  return ms ? fmtDateTime(Math.floor(ms / 1000)) : '—'
})

// 检查更新（gofer-6x2n）：server 每隔一段时间（默认 2h）自己检查一次，这里读它最近的结果；
// 「检查更新」立即查一次。一键更新（gofer-29gi）只对以服务方式运行的 server 开放
// （managed）：server 下载新版本替换自己，重启后再升级勾选的 worker；进度轮询 apply。
const update = ref<UpdateStatus | null>(null)
const updateError = ref('')
const checking = ref(false)
const sourceLabel = computed(() =>
  update.value?.source === 'mirror' ? t('settings.about.sourceMirror') : 'GitHub',
)
const checkedAt = computed(() => {
  const at = update.value?.checked_at
  return at
    ? t('settings.about.checkedAt', { at: fmtDateTime(at), ago: fmtAgo(at) })
    : t('settings.about.neverChecked')
})

function setUpdate(st: UpdateStatus): void {
  update.value = st
  noteUpdateStatus(st)
  if (st.apply) apply.value = st.apply
}

async function loadUpdate(): Promise<void> {
  updateError.value = ''
  try {
    setUpdate(await getUpdateStatus())
  } catch (e) {
    updateError.value = e instanceof Error ? e.message : String(e)
  }
}

// 一键更新进度。server 重启期间请求会失败，这时显示「重启中」并继续轮询。
// 一键更新需要 update.apply；没有就不摆入口（检查新版本仍可用）。
const { can } = useWhoami()
const apply = ref<UpdateApply | null>(null)
const showApply = ref(false)
const applying = ref(false)
const applyError = ref('')
const unreachable = ref(false)
let pollTimer: ReturnType<typeof setTimeout> | null = null

const activePhases = ['downloading', 'waiting_idle', 'restarting', 'upgrading_workers']
const applyActive = computed(() => !!apply.value && activePhases.includes(apply.value.phase))
// 阶段与 worker 状态都按 i18n 取（settings.about.phase.* / settings.about.worker.*），
// 没登记的取值原样显示（服务端新增阶段时不至于空白）。
function phaseText(phase: string): string {
  return t(`settings.about.phase.${phase}`)
}
function workerStateText(state: string): string {
  return t(`settings.about.worker.${state}`)
}
// gofer-dzv3：等空闲时展示的两个数字——还在跑的 job（挡住重启的）与已经停住的新 job。
const waitingIdle = computed(() => apply.value?.phase === 'waiting_idle')
const waitingRows = computed(() => (waitingIdle.value ? (apply.value?.waiting ?? []) : []))
const waitDeadlineText = computed(() => {
  const at = apply.value?.wait_deadline
  if (!at) return ''
  const left = at - Math.floor(Date.now() / 1000)
  return t('settings.about.deadline', {
    at: fmtDateTime(at),
    left: left > 0 ? t('settings.about.deadlineLeft', { d: fmtDuration(left) }) : t('settings.about.deadlineSoon'),
  })
})
const cancelling = ref(false)
const cancelError = ref('')

// 取消等待（gofer-dzv3）：停住的 job 立即放行，apply 记为 cancelled（已经进入重启时 409）。
async function cancelWait(): Promise<void> {
  cancelling.value = true
  cancelError.value = ''
  try {
    await cancelUpdateWait()
    schedulePoll()
  } catch (e) {
    cancelError.value = e instanceof Error ? e.message : String(e)
  } finally {
    cancelling.value = false
  }
}

function schedulePoll(): void {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = setTimeout(() => void poll(), 3000)
}

async function poll(): Promise<void> {
  try {
    setUpdate(await getUpdateStatus())
    unreachable.value = false
  } catch {
    unreachable.value = true
  }
  const workersPending = apply.value?.workers.some((w) => w.state === 'pending' || w.state === 'accepted')
  if (applyActive.value || unreachable.value || (apply.value?.phase === 'done' && workersPending)) {
    schedulePoll()
  } else {
    void load() // 版本号变了
  }
}

async function startApply(workers: string[], waitIdleSec: number): Promise<void> {
  applying.value = true
  applyError.value = ''
  try {
    apply.value = await applyUpdate(workers, waitIdleSec)
    showApply.value = false
    schedulePoll()
  } catch (e) {
    applyError.value = e instanceof Error ? e.message : String(e)
  } finally {
    applying.value = false
  }
}

async function runCheck(): Promise<void> {
  checking.value = true
  updateError.value = ''
  try {
    setUpdate(await checkUpdate())
  } catch (e) {
    updateError.value = e instanceof Error ? e.message : String(e)
  } finally {
    checking.value = false
  }
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    stats.value = await getStats()
    loaded.value = true
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  void load()
  await loadUpdate()
  if (applyActive.value) schedulePoll()
})
onUnmounted(() => {
  if (pollTimer) clearTimeout(pollTimer)
})
</script>

<template>
  <div class="about">
    <header class="page-head">
      <div class="grow">
        <h1>{{ t('settings.about.title') }}</h1>
        <p class="sub">{{ t('settings.about.sub') }}</p>
      </div>
    </header>

    <p v-if="error" class="msg msg--err" :title="error">
      {{ t('settings.about.loadFailed', { error }) }}
    </p>

    <FormSection :title="t('settings.about.server')" :desc="t('settings.about.serverDesc')">
      <dl class="facts-grid">
        <dt>{{ t('settings.about.version') }}</dt>
        <dd class="brk mono">{{ version }}</dd>

        <dt>{{ t('settings.about.addr') }}</dt>
        <dd class="brk mono">{{ serverAddr }}</dd>

        <dt>{{ t('settings.about.uptime') }}</dt>
        <dd>{{ uptime }}</dd>

        <dt>{{ t('settings.about.serverTime') }}</dt>
        <dd>{{ serverTime }}</dd>
      </dl>

      <p v-if="!loaded && !error" class="placeholder">{{ t('settings.about.reading') }}</p>
      <div class="actions">
        <button class="btn" type="button" :disabled="loading" @click="load()">
          {{ loading ? t('settings.about.refreshing') : t('act.refresh') }}
        </button>
      </div>
    </FormSection>

    <FormSection :title="t('settings.about.update')" :desc="t('settings.about.updateDesc')">
      <p v-if="updateError" class="msg msg--err" :title="updateError">{{ updateError }}</p>

      <dl v-if="update" class="facts-grid">
        <dt>{{ t('settings.about.latest') }}</dt>
        <dd class="brk mono">
          <template v-if="update.latest">
            {{ update.latest }}
            <span v-if="update.newer" class="badge badge--new">{{ t('settings.about.newer') }}</span>
            <span v-else-if="!update.error" class="badge">{{ t('settings.about.uptodate') }}</span>
          </template>
          <template v-else>—</template>
        </dd>

        <template v-if="update.released_at">
          <dt>{{ t('settings.about.releasedAt') }}</dt>
          <dd class="brk">{{ update.released_at }}</dd>
        </template>

        <template v-if="update.description">
          <dt>{{ t('settings.about.description') }}</dt>
          <dd class="brk">{{ update.description }}</dd>
        </template>

        <template v-if="update.download_url">
          <dt>{{ t('settings.about.download') }}</dt>
          <dd class="brk"><a :href="update.download_url" target="_blank" rel="noopener">{{ update.download_url }}</a></dd>
        </template>

        <template v-if="update.release_url">
          <dt>{{ t('settings.about.releasePage') }}</dt>
          <dd class="brk"><a :href="update.release_url" target="_blank" rel="noopener">{{ update.release_url }}</a></dd>
        </template>

        <dt>{{ t('settings.about.source') }}</dt>
        <dd>{{ sourceLabel }}</dd>

        <dt>{{ t('settings.about.checked') }}</dt>
        <dd>{{ checkedAt }}</dd>

        <template v-if="update.error">
          <dt>{{ t('settings.about.checkFailed') }}</dt>
          <dd class="brk fail">{{ update.error }}</dd>
        </template>
      </dl>

      <div v-if="update?.newer && !applyActive && can('update.apply')" class="apply-entry">
        <template v-if="update.managed">
          <button
            class="btn btn--primary"
            type="button"
            data-testid="update-apply-open"
            @click="showApply = true; applyError = ''"
          >
            {{ t('settings.about.applyNow', { version: update.latest ?? '' }) }}
          </button>
        </template>
        <!-- 不是受管服务：一键更新用不了，摆一个警示框说清原因和手动 / 改成服务的步骤。 -->
        <UpdateManualNotice v-else :managed="update.managed" />
      </div>

      <div v-if="apply" class="apply" data-testid="update-apply-progress">
        <div class="apply-head">
          <span>
            {{ applyActive ? t('settings.about.updating') : t('settings.about.lastUpdate') }}：{{
              apply.from
            }}
            → {{ apply.version }}
          </span>
          <span :class="{ fail: apply.phase === 'failed', ok: apply.phase === 'done' }">
            {{ phaseText(apply.phase) }}
          </span>
        </div>
        <p v-if="unreachable && applyActive" class="hint">{{ t('settings.about.restarting') }}</p>
        <p v-if="apply.error" class="fail brk apply-err">{{ apply.error }}</p>
        <!-- gofer-dzv3：等空闲期间说清楚在等谁、停住了多少，可随时取消。 -->
        <div v-if="waitingIdle" class="apply-wait" data-testid="update-wait-idle">
          <p class="hint">
            {{ t('settings.about.waitIdle', { n: waitingRows.length, paused: apply.paused ?? 0 }) }}
          </p>
          <div v-for="j in waitingRows" :key="j.id" class="apply-row">
            <RouterLink class="brk" :to="`/jobs/${j.id}`">{{ j.title || j.id }}</RouterLink>
            <span class="dim">{{ j.runner || '' }}</span>
            <span class="dim">{{ j.status }}</span>
          </div>
          <p v-if="apply.wait_deadline" class="hint">
            {{ t('settings.about.giveUpAt', { text: waitDeadlineText }) }}
          </p>
          <div class="apply-actions">
            <button
              class="btn"
              type="button"
              :disabled="cancelling"
              data-testid="update-wait-cancel"
              @click="cancelWait"
            >
              {{ cancelling ? t('settings.about.cancelling') : t('settings.about.cancelWait') }}
            </button>
            <span class="hint">{{ t('settings.about.cancelHint') }}</span>
          </div>
          <p v-if="cancelError" class="fail brk apply-err">{{ cancelError }}</p>
        </div>
        <div v-for="w in apply.workers" :key="w.id" class="apply-row">
          <span class="brk mono">{{ w.id }}</span>
          <span class="dim">{{ w.os && w.arch ? `${w.os}/${w.arch}` : '' }}</span>
          <span :class="{ fail: w.state === 'failed', ok: w.state === 'succeeded', dim: w.state === 'skipped' }">
            {{ workerStateText(w.state) }}
          </span>
          <span v-if="w.error" class="fail brk apply-werr">{{ w.error }}</span>
        </div>
        <p class="hint">
          {{ t('settings.about.applyTime', { at: fmtDateTime(apply.started_at) }) }}
        </p>
      </div>

      <div class="actions">
        <button class="btn" type="button" :disabled="checking" @click="runCheck()">
          {{ checking ? t('settings.about.checking') : t('settings.about.check') }}
        </button>
      </div>

      <p class="hint">{{ t('settings.about.pollHint') }}</p>
    </FormSection>

    <FormSection :title="t('settings.about.browser')" :desc="t('settings.about.browserDesc')">
      <p class="hint hint--tight" data-testid="about-remember-state">
        {{ remembered ? t('settings.about.remembered') : t('settings.about.notRemembered') }}
      </p>
      <div class="actions">
        <button class="btn" type="button" data-testid="about-logout-forget" @click="logoutForget">
          {{ t('settings.about.logoutForget') }}
        </button>
        <span class="hint hint--inline">{{ t('settings.about.logoutForgetHint') }}</span>
      </div>
    </FormSection>

    <UpdateApplyDialog
      v-if="showApply && update?.latest"
      :current="update.current"
      :latest="update.latest"
      :submitting="applying"
      :error="applyError"
      @close="showApply = false"
      @confirm="startApply"
    />
  </div>
</template>

<style scoped>
.about {
  min-width: 0;
}

.page-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
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

.facts-grid {
  display: grid;
  grid-template-columns: 120px minmax(0, 1fr);
  gap: 8px 14px;
  margin: 0;
  font-size: var(--fs-sm);
}
.facts-grid dt {
  color: var(--ink-3);
}
.facts-grid dd {
  margin: 0;
  color: var(--ink);
}
.brk {
  word-break: break-all;
}

.actions {
  display: flex;
  gap: 8px;
  margin-top: 12px;
}

.btn {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 6px 12px;
  font: inherit;
  font-size: var(--fs-sm);
  background: transparent;
  color: var(--ink);
}
.btn:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--accent);
}
.btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.btn--primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}

.badge {
  display: inline-block;
  margin-left: 8px;
  padding: 0 6px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.badge--new {
  border-color: var(--accent);
  color: var(--accent);
  background: var(--accent-soft);
}

.fail {
  color: var(--fail);
}
.ok {
  color: var(--accent);
}
.dim {
  color: var(--ink-3);
}

.apply-entry {
  margin-top: 12px;
}

.apply {
  margin-top: 14px;
  border-top: 1px dashed var(--line);
  padding-top: 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: var(--fs-sm);
}
.apply-head {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  color: var(--ink);
}
.apply-err {
  margin: 0;
}
.apply-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto;
  gap: 4px 12px;
  color: var(--ink);
  font-size: var(--fs-sm);
}
.apply-werr {
  grid-column: 1 / -1;
  font-size: var(--fs-xs);
}
/* gofer-dzv3：等空闲的进度块——在等谁、停住了多少、放弃时间与「取消等待」。 */
.apply-wait {
  display: flex;
  flex-direction: column;
  gap: 6px;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 10px 12px;
  background: var(--surface);
}
.apply-wait .hint {
  margin: 0;
}
.apply-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.hint {
  color: var(--ink-3);
  font-size: var(--fs-sm);
  margin: 12px 0 0;
  line-height: 1.6;
}
.hint--tight {
  margin: 0;
}
.hint--inline {
  margin: 0;
}

.msg {
  margin: 0 0 12px;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 8px 10px;
  font-size: var(--fs-sm);
}
.msg--err {
  color: var(--fail);
  border-color: var(--fail);
  word-break: break-word;
}

.placeholder {
  color: var(--ink-3);
  font-size: var(--fs-sm);
  margin: 10px 0 0;
}

@media (max-width: 760px) {
  .facts-grid {
    grid-template-columns: minmax(0, 1fr);
  }
  .apply-row {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
