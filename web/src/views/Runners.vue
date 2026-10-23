<script setup lang="ts">
// Runners「运行节点」：server 与各 worker 的在线状态、版本与项目。
//  - 轮询 listRunners（4s），Page Visibility 暂停/恢复。
//  - 轮询间隔之间每秒本地推进心跳/探活年龄，让 worker 行“活着”。
//  - 失败软处理：保留上一帧数据，仅在头部给出 in-voice 错误条，不清空页面。
//  - worker 心跳脉冲为唯一“张扬”元素；peer-http/local 用静态状态字形。
// UI 重设计 P5：页头/分组/状态全部走 i18n 与 StatusGlyph/StatusPill（设计稿 §5、§7.4）。
import { runnerLabelNote } from '../utils/runnerDisplay'
import { fmtDateTime } from '../api/time'
import { useWhoami } from '../store/whoami'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import Heartbeat from '../components/Heartbeat.vue'
import ClusterTopology from '../components/ClusterTopology.vue'
import EnrollAcceptDialog from '../components/EnrollAcceptDialog.vue'
import EnrollRejectDialog from '../components/EnrollRejectDialog.vue'
import MessengerDrawer from '../components/MessengerDrawer.vue'
import RunnerDirs from '../components/RunnerDirs.vue'
import StatusGlyph from '../ui/StatusGlyph.vue'
import StatusPill from '../ui/StatusPill.vue'
import { listEnrollments, listProjects, listRunners, registerWorker, reloadWorker, upgradeWorker, ApiError } from '../api/client'
import { TOPIC_WORKER_ENROLLMENTS } from '../api/live'
import type { Runner, RunnersServerInfo, WorkerEnrollment } from '../api/types'
import { messengerDisplay } from '../utils/messenger'
import { createLiveTopic } from '../utils/useLiveTopic'
import { t } from '../i18n'
import type { StatusFamily } from '../utils/statusFamily'
import { beatOf, fmtAge, fmtUptime, runnerTools, toolTitle, upgradeBlockReason, upgradeStateClass, upgradeSummary, workerAgeMs } from '../utils/runners'
import { ENROLL_ANCHOR, enrollTitle, hostLine, isPending } from '../utils/enrollments'

const runners = ref<Runner[]>([])
const projects = ref<string[]>([])
const loading = ref(false)
const error = ref('')
const loaded = ref(false)
const reloading = ref<string | null>(null)
const reloadNotice = ref('')
const serverInfo = ref<RunnersServerInfo | undefined>(undefined)
const upgrading = ref<string | null>(null)
const upgradeNotice = ref('')
// 传话人抽屉：记 runner 名，数据始终取最新一次轮询的那一行
const messengerOpen = ref('')
const topologyOpen = ref(true)
// 按权限隐藏：升级 worker 需要 worker.upgrade；添加 / 重新加载配置需要 worker.manage。
const { can } = useWhoami()
const addOpen = ref(false)
const addID = ref('')
const addLabels = ref('')
const addProjects = ref('')
const addBusy = ref(false)
const addError = ref('')
const issuedToken = ref('')
const issuedCommand = ref('')
// 本地时钟（毫秒）：用于在两次轮询之间推进“xx ago”年龄，使其逐秒走动。
const nowMs = ref(Date.now())
// 秒级时钟（倒计时用）。
const nowSec = computed(() => Math.floor(nowMs.value / 1000))

// ── 自助注册「待接受」区（worker.manage）──
// 列表走受保护的 GET /v1/worker-enrollments；申请变化由无负载失效主题 worker-enrollments 触发重拉。
// 配对码不在任何响应里（只在 worker 终端上），接受时由人照着输入。
const route = useRoute()
const enrollments = ref<WorkerEnrollment[]>([])
const enrollEnabled = ref(true)
const enrollError = ref('')
const enrollLoaded = ref(false)
const enrollDenied = ref(false)
const enrollNotice = ref('')
const acceptItem = ref<WorkerEnrollment | null>(null)
const rejectItem = ref<WorkerEnrollment | null>(null)
// 从 /runners#enrollments 进来时已经滚过一次锚点就不再抢滚动位置。
let enrollScrolled = false

const enrollAllowed = computed(() => can('worker.manage'))
const pendingEnrollments = computed(() => enrollments.value.filter((e) => isPending(e, nowSec.value)))
const enrollDisabledHint = computed(() => (enrollEnabled.value ? '' : t('runners.enroll.disabled')))
// 本地能看到的 worker id（在线 worker + 已决定的申请）：接受对话框据此当场提示“已存在”。
const knownWorkerIds = computed(() => {
  const ids = new Set<string>()
  for (const w of runners.value) {
    if (w.worker_id) ids.add(w.worker_id)
  }
  for (const e of enrollments.value) {
    if (e.worker_id) ids.add(e.worker_id)
  }
  return [...ids]
})

let tickTimer: number | null = null

const workers = computed(() => runners.value.filter((r) => r.type === 'worker'))
const peers = computed(() => runners.value.filter((r) => r.type === 'peer-http'))
const locals = computed(() => runners.value.filter((r) => r.type === 'local'))
const messengerRunner = computed(() => runners.value.find((r) => r.name === messengerOpen.value))

async function fetchRunners(): Promise<void> {
  loading.value = true
  try {
    const [resp, projectsResp] = await Promise.all([listRunners(), listProjects().catch(() => null)])
    runners.value = resp.runners ?? []
    serverInfo.value = resp.server
    projects.value = projectsResp?.projects ?? []
    error.value = ''
    loaded.value = true
    nowMs.value = Date.now()
  } catch (e) {
    // 401 已由 client 处理（跳转登录）；其余仅给出头部错误条，保留上一帧
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// fetchEnrollments 拉待决定 / 最近 24 小时的申请。403（非个人账号一类）整区隐藏，
// 其余错误只给一行提示，保留上一帧；列表页不因它而清空。
async function fetchEnrollments(): Promise<void> {
  if (!enrollAllowed.value) return
  try {
    const resp = await listEnrollments()
    enrollments.value = resp.items ?? []
    enrollEnabled.value = resp.enabled
    enrollDenied.value = false
    enrollError.value = ''
    enrollLoaded.value = true
    // 从 Today 卡片 / 顶栏提醒过来时补一次锚点滚动（SPA 导航不会自己滚），只滚一次。
    if (!enrollScrolled && route.hash === `#${ENROLL_ANCHOR}`) {
      enrollScrolled = true
      scrollToEnrollments()
    }
  } catch (e) {
    if (e instanceof ApiError && e.status === 403) {
      enrollDenied.value = true
      return
    }
    enrollError.value = e instanceof Error ? e.message : String(e)
  }
}

// 申请 / 决定后重拉一次列表与舰队（接受会登记并热加载，worker 可能马上连上来）。
async function onEnrollmentAccepted(workerID: string): Promise<void> {
  acceptItem.value = null
  enrollNotice.value = t('runners.enroll.accepted', { id: workerID })
  await Promise.all([fetchEnrollments(), fetchRunners()])
}

async function onEnrollmentRejected(): Promise<void> {
  const item = rejectItem.value
  rejectItem.value = null
  enrollNotice.value = item
    ? t('runners.enroll.rejected', { title: enrollTitle(item) })
    : t('runners.enroll.rejectedGeneric')
  await fetchEnrollments()
}

// 申请变化：主题 worker-enrollments 是无负载失效通知，收到就重拉列表。
const liveEnrollments = createLiveTopic(TOPIC_WORKER_ENROLLMENTS, { initial: false, fetch: fetchEnrollments })

// 待接受区只在持有 worker.manage 时显示与拉取（whoami 未取到时 can() 不隐藏，跟全站一致）。
watch(
  enrollAllowed,
  (on) => {
    if (on) {
      void fetchEnrollments()
      liveEnrollments.start()
    } else {
      liveEnrollments.stop()
      enrollments.value = []
    }
  },
  { immediate: true },
)

// Today 卡片 / 顶栏提醒跳 /runners#enrollments：SPA 导航不会自己滚到锚点，这里补一次。
function scrollToEnrollments(): void {
  void nextTick(() => document.getElementById(ENROLL_ANCHOR)?.scrollIntoView({ block: 'start' }))
}

async function reloadWorkerConfig(workerID: string): Promise<void> {
  if (reloading.value) return
  reloading.value = workerID
  reloadNotice.value = ''
  try {
    const result = await reloadWorker(workerID)
    reloadNotice.value = result.applied
      ? t('runners.worker.reloadOk', { id: workerID })
      : t('runners.worker.reloadNotApplied', {
          id: workerID,
          reason: result.error || result.detail || t('runners.worker.reloadUnknown'),
        })
    await fetchRunners()
  } catch (e) {
    reloadNotice.value = t('runners.worker.reloadErr', {
      id: workerID,
      err: e instanceof Error ? e.message : String(e),
    })
  } finally {
    reloading.value = null
  }
}

// 用 server 自身二进制升级 worker。在途任务会先等到结束（最长 10 分钟），期间 worker 不接新任务；
// 最终结果（已升级 / 已回滚）由 4s 轮询带回的 upgrade 记录显示。
async function upgradeWorkerBinary(r: Runner): Promise<void> {
  const id = r.worker_id || r.name
  if (upgrading.value) return
  const from = r.worker?.gofer_version || '?'
  const to = serverInfo.value?.version || t('runners.worker.upgradeToServer')
  if (!window.confirm(t('runners.worker.upgradeConfirm', { id, from, to }))) return
  upgrading.value = id
  upgradeNotice.value = ''
  try {
    const out = await upgradeWorker(id)
    upgradeNotice.value = t('runners.worker.upgradeAccepted', {
      id,
      version: out.upgrade.target_version || '?',
    })
    await fetchRunners()
  } catch (e) {
    upgradeNotice.value = t('runners.worker.upgradeErr', {
      id,
      err: e instanceof Error ? e.message : String(e),
    })
  } finally {
    upgrading.value = null
  }
}

async function addWorker(): Promise<void> {
  const id = addID.value.trim()
  if (!id || addBusy.value) return
  addBusy.value = true
  addError.value = ''
  issuedToken.value = ''
  try {
    const out = await registerWorker(id, addLabels.value.split(',').map((x) => x.trim()).filter(Boolean), addProjects.value.split(',').map((x) => x.trim()).filter(Boolean))
    issuedToken.value = out.worker_token
    issuedCommand.value = `gofer init worker --server ${window.location.origin} --id ${out.worker_id} --token ${out.worker_token} --yes`
    addID.value = ''
    addLabels.value = ''
    addProjects.value = ''
    await fetchRunners()
  } catch (e) {
    addError.value = e instanceof Error ? e.message : String(e)
  } finally {
    addBusy.value = false
  }
}

// Q3：`runners` 主题（worker 上下线 / 升级记录变化）触发重拉；WS 断开 >15s 才由 30s 兜底轮询接手。
const liveRunners = createLiveTopic('runners', { initial: false, fetch: fetchRunners })

function startTick(): void {
  stopTick()
  if (document.hidden) {
    return
  }
  // 逐秒推进本地时钟，使心跳/探活年龄看起来在走动（纯本地显示，不发请求）
  tickTimer = window.setInterval(() => {
    nowMs.value = Date.now()
  }, 1000)
}

function stopTick(): void {
  if (tickTimer != null) {
    window.clearInterval(tickTimer)
    tickTimer = null
  }
}

function onVisibility(): void {
  if (document.hidden) {
    stopTick()
  } else {
    startTick()
  }
}

onMounted(() => {
  try { topologyOpen.value = localStorage.getItem('gofer.runners.topology-open') !== 'false' } catch { /* ignore storage failures */ }
  if (window.matchMedia('(max-width: 760px)').matches) topologyOpen.value = false
  void fetchRunners()
  liveRunners.start()
  startTick()
  document.addEventListener('visibilitychange', onVisibility)
})

watch(topologyOpen, (value) => {
  try { localStorage.setItem('gofer.runners.topology-open', String(value)) } catch { /* ignore storage failures */ }
})

function onTopologyToggle(event: Event): void {
  topologyOpen.value = (event.target as HTMLDetailsElement).open
}

onUnmounted(() => {
  liveRunners.stop()
  liveEnrollments.stop()
  stopTick()
  document.removeEventListener('visibilitychange', onVisibility)
})

// 状态族 + 文案（设计稿 §5：worker 在线 = done、心跳过期 = time、离线 = idle）。
const workerState = computed<Record<string, { family: StatusFamily; label: string }>>(() => {
  const out: Record<string, { family: StatusFamily; label: string }> = {}
  for (const r of workers.value) {
    const beat = beatOf(r, nowMs.value)
    if (beat === 'connected') {
      out[r.name] = { family: 'done', label: t('runners.worker.connected') }
    } else if (beat === 'stale') {
      const s = Math.floor((workerAgeMs(r, nowMs.value) ?? 0) / 1000)
      out[r.name] = { family: 'time', label: t('runners.worker.stale', { s }) }
    } else {
      out[r.name] = { family: 'idle', label: t('runners.worker.offline') }
    }
  }
  return out
})

function workerStateOf(r: Runner): { family: StatusFamily; label: string } {
  return workerState.value[r.name] ?? { family: 'idle', label: t('runners.worker.offline') }
}

// 节点信息行是否有内容（hostname / 来源地址 / os / 版本 / 启动时间任一存在）。
function hasNodeInfo(r: Runner): boolean {
  const w = r.worker
  return !!w && !!(w.hostname || w.remote_addr || w.os || w.gofer_version || w.started_at)
}

// 待接受区一行的「OS / 架构 · gofer 版本 · 协议版本」（文案在 utils/enrollments.ts）。
function enrollHostLine(e: WorkerEnrollment): string {
  return hostLine(e.host)
}

// 申请状态的族与文案（设计稿 §5：待决定 = need，等领取 = queue，已拒绝/过期 = fail）。
const ENROLL_STATE: Record<string, { family: StatusFamily; key: string }> = {
  pending: { family: 'need', key: 'pending' },
  accepting: { family: 'queue', key: 'accepting' },
  accepted: { family: 'queue', key: 'acceptedState' },
  collected: { family: 'done', key: 'collected' },
  rejected: { family: 'fail', key: 'rejectedState' },
  expired: { family: 'fail', key: 'expired' },
  superseded: { family: 'idle', key: 'superseded' },
  cancelled: { family: 'idle', key: 'cancelled' },
}

function enrollState(e: WorkerEnrollment): { family: StatusFamily; label: string } {
  const spec = ENROLL_STATE[e.state]
  if (!spec) return { family: 'idle', label: e.state }
  return { family: spec.family, label: t(`runners.enroll.${spec.key}`) }
}

// 决定窗口倒计时：窗口默认只有 2 分钟，按秒显示。
function decideCountdownText(until: number | undefined, now: number): string {
  if (!until) return ''
  const left = Math.floor(until - now)
  if (left <= 0) return t('runners.enroll.expiredLeft')
  if (left < 60) return t('runners.enroll.leftSeconds', { n: left })
  return t('runners.enroll.leftMinutes', { m: Math.floor(left / 60), s: left % 60 })
}

// 接受之后「worker 领取 token」的剩余时间（过了就自动撤销这次登记）。
function collectCountdownText(until: number | undefined, now: number): string {
  if (until && until - now <= 0) return t('runners.enroll.collectTimeout')
  return decideCountdownText(until, now)
}

// ── peer-http 探活：实时年龄 + 延迟 + 错误 ──
function probeAgeMs(r: Runner): number | null {
  if (!r.probe || r.probe.checked_at <= 0) {
    return null
  }
  return Math.max(0, nowMs.value - r.probe.checked_at)
}

// up / down / 未探测（设计稿 §5：down = fail，未探测 = idle）。
function peerState(r: Runner): { family: StatusFamily; label: string } {
  if (r.status === 'up') return { family: 'done', label: t('runners.peer.up') }
  if (r.status === 'down') return { family: 'fail', label: t('runners.peer.down') }
  return { family: 'idle', label: t('runners.peer.unknown') }
}

function beatLabel(r: Runner): string {
  return workerStateOf(r).label
}
</script>

<template>
  <div class="page">
    <header class="page-head">
      <div>
        <h1 class="title">{{ t('nav.runners') }}</h1>
        <p class="sub">{{ t('runners.sub') }}</p>
      </div>
      <span class="poll-dot" :class="{ 'poll-dot--on': loading }" aria-hidden="true">●</span>
    </header>

    <p v-if="error" class="error" :title="error">{{ t('runners.err.load', { err: error }) }}</p>
    <p v-if="reloadNotice" class="notice">{{ reloadNotice }}</p>
    <p v-if="upgradeNotice" class="notice">{{ upgradeNotice }}</p>

    <!-- 自助注册「待接受」区（只对 worker.manage 的人显示，设计稿 §7.4：置顶）；Today 卡与顶栏提醒都跳到 /runners#enrollments。
         配对码不在这里（也不在任何响应里）：管理员要到 worker 终端上看码，接受时输入。 -->
    <section v-if="enrollAllowed && !enrollDenied" id="enrollments" class="group" data-test="enrollments">
      <header class="group-head">
        <h2 class="group-title">{{ t('runners.enroll.title') }}</h2>
        <span class="group-count">{{ pendingEnrollments.length }}</span>
      </header>

      <p v-if="enrollNotice" class="notice" data-test="enroll-notice">{{ enrollNotice }}</p>
      <p v-if="enrollDisabledHint" class="hint" data-test="enroll-disabled">{{ enrollDisabledHint }}</p>
      <p v-if="enrollError" class="error" data-test="enroll-error">
        {{ t('runners.enroll.err', { err: enrollError }) }}
      </p>

      <div v-if="enrollments.length" class="cards">
        <article v-for="e in enrollments" :key="e.id" class="card card--enroll" :data-state="e.state" data-test="enroll-item">
          <div class="card-pulse">
            <StatusGlyph :family="enrollState(e).family" :label="enrollState(e).label" :size="14" />
          </div>
          <div class="card-main">
            <div class="card-row1">
              <span class="card-name" :title="e.id">{{ enrollTitle(e) }}</span>
              <span class="card-meta">
                <span class="host" :title="e.source ? t('runners.enroll.source', { source: e.source }) : t('runners.enroll.sourceUnknown')">
                  {{ e.source || t('runners.enroll.sourceUnknown') }}
                </span>
                <span class="dot-sep" aria-hidden="true">·</span>
                <span class="enroll-state">{{ enrollState(e).label }}</span>
                <template v-if="isPending(e, nowSec)">
                  <span class="dot-sep" aria-hidden="true">·</span>
                  <span class="enroll-left" data-test="enroll-countdown">
                    {{ decideCountdownText(e.decide_until, nowSec) }}
                  </span>
                </template>
              </span>
            </div>
            <div v-if="enrollHostLine(e)" class="node-line" data-test="enroll-host">{{ enrollHostLine(e) }}</div>
            <div v-if="e.labels?.length" class="chips" data-test="enroll-labels">
              <span class="chips-label">{{ t('runners.enroll.labels') }}</span>
              <span v-for="l in e.labels" :key="l" class="chip">{{ l }}</span>
            </div>
            <div v-if="e.host?.agents?.length" class="chips" data-test="enroll-agents">
              <span class="chips-label">{{ t('runners.enroll.agents') }}</span>
              <span v-for="a in e.host?.agents" :key="a" class="chip">{{ a }}</span>
            </div>
            <div v-if="e.host?.roots?.length" class="chips" data-test="enroll-roots">
              <span class="chips-label">{{ t('runners.enroll.roots') }}</span>
              <span v-for="r in e.host?.roots" :key="r" class="chip" :title="r">{{ r }}</span>
            </div>
            <template v-if="isPending(e, nowSec)">
              <button class="btn" type="button" data-test="enroll-accept" @click="acceptItem = e">
                {{ t('runners.enroll.accept') }}
              </button>
              <button class="btn" type="button" data-test="enroll-reject" @click="rejectItem = e">
                {{ t('runners.enroll.reject') }}
              </button>
            </template>
            <p v-else-if="e.state === 'accepted'" class="enroll-line" data-test="enroll-collect">
              {{ t('runners.enroll.collect', { id: e.worker_id || '—', left: collectCountdownText(e.collect_until, nowSec) }) }}
            </p>
            <p v-else-if="e.reason" class="enroll-line" data-test="enroll-reason">
              {{ t('runners.enroll.reason', { reason: e.reason }) }}
            </p>
          </div>
        </article>
      </div>

      <!-- 功能关闭时不提“去跑 join”：提示已经告诉操作员怎么开启 -->
      <div v-else-if="enrollLoaded && enrollEnabled" class="empty" data-test="enroll-empty">
        {{ t('runners.enroll.empty', { cmd: 'gofer worker join --server <server-address>' }) }}
      </div>
    </section>

    <section v-if="can('worker.manage')" class="group add-worker" data-test="add-worker">
      <header class="group-head">
        <h2 class="group-title">{{ t('runners.add.title') }}</h2>
        <button class="btn" type="button" @click="addOpen = !addOpen">
          {{ addOpen ? t('runners.collapse') : t('runners.add.title') }}
        </button>
      </header>
      <form v-if="addOpen" class="add-form" @submit.prevent="addWorker">
        <div class="field">
          <label class="label" for="aw-id">{{ t('runners.add.id') }}</label>
          <input id="aw-id" v-model="addID" class="control" required pattern="[A-Za-z0-9][A-Za-z0-9._-]{0,63}" autocomplete="off" />
        </div>
        <div class="field">
          <label class="label" for="aw-labels">{{ t('runners.add.labels') }}</label>
          <input id="aw-labels" v-model="addLabels" class="control" placeholder="linux,gpu" autocomplete="off" />
        </div>
        <div class="field">
          <label class="label" for="aw-projects">{{ t('runners.add.projects') }}</label>
          <input id="aw-projects" v-model="addProjects" class="control" placeholder="project-a,project-b" autocomplete="off" />
        </div>
        <div class="add-actions">
          <button class="btn btn--primary" type="submit" :disabled="addBusy">
            {{ addBusy ? t('runners.add.busy') : t('runners.add.submit') }}
          </button>
        </div>
      </form>
      <p v-if="addError" class="error">{{ t('runners.add.err', { err: addError }) }}</p>
      <div v-if="issuedToken" class="issued">
        <p>{{ t('runners.add.token') }}<code>{{ issuedToken }}</code></p>
        <p>{{ t('runners.add.command') }}<code>{{ issuedCommand }}</code></p>
      </div>
    </section>

    <section class="group topology-group">
      <details :open="topologyOpen" @toggle="onTopologyToggle">
        <summary class="group-head">
          <h2 class="group-title">{{ t('runners.group.topology') }}</h2>
          <span class="topology-toggle">{{ topologyOpen ? t('runners.collapse') : t('runners.expand') }}</span>
        </summary>
        <ClusterTopology :runners="runners" :projects="projects" :now-ms="nowMs" />
      </details>
    </section>

    <!-- WORKERS（主角，置顶） -->
    <section class="group" aria-labelledby="grp-workers">
      <header class="group-head">
        <h2 id="grp-workers" class="group-title">{{ t('runners.group.workers') }}</h2>
        <span class="group-count">{{ workers.length }}</span>
      </header>

      <div v-if="workers.length" class="cards">
        <article v-for="w in workers" :key="w.name" class="card card--worker">
          <div class="card-pulse">
            <Heartbeat :beat="beatOf(w, nowMs)" :label="beatLabel(w)" />
          </div>
          <div class="card-main">
            <div class="card-row1">
              <span class="card-name">{{ w.name }}</span>
              <span class="card-meta">
                <span class="wid" :title="w.worker_id">{{ w.worker_id || '—' }}</span>
                <span class="dot-sep" aria-hidden="true">·</span>
                <span
                  class="age"
                  :class="{ 'age--down': w.status !== 'connected' }"
                  :title="w.worker ? t('runners.worker.lastHeartbeat', { time: new Date(w.worker.last_heartbeat).toLocaleString() }) : ''"
                >{{ w.status === 'connected' ? fmtAge(workerAgeMs(w, nowMs)) : t('runners.worker.offline') }}</span>
                <span class="dot-sep" aria-hidden="true">·</span>
                <span class="inflight">{{ t('runners.worker.inFlight', { n: w.worker?.in_flight ?? 0 }) }}</span>
              </span>
              <StatusPill :family="workerStateOf(w).family" :label="workerStateOf(w).label" />
            </div>
            <!-- 节点信息行：hostname（机器标识）· 来源地址 · os/arch · 版本 · 运行时长 -->
            <div v-if="hasNodeInfo(w)" class="node-line">
              <span v-if="w.worker?.hostname" class="node-host" :title="t('runners.worker.hostname', { host: w.worker.hostname })">{{ w.worker.hostname }}</span>
              <span v-if="w.worker?.remote_addr" class="node-item" :title="t('runners.worker.remoteAddr', { addr: w.worker.remote_addr })">{{ w.worker.remote_addr }}</span>
              <span v-if="w.worker?.os" class="node-item">{{ w.worker.os }}/{{ w.worker.arch || '?' }}</span>
              <span v-if="w.worker?.gofer_version" class="node-item" :title="t('runners.worker.goferVersion', { version: w.worker.gofer_version })">v{{ w.worker.gofer_version }}</span>
              <span v-if="w.worker?.started_at" class="node-item">{{ fmtUptime(w.worker.started_at, nowMs) }}</span>
            </div>
            <button
              v-if="w.status === 'connected'"
              class="msgr-chip"
              :class="`mtone--${messengerDisplay(w).tone}`"
              type="button"
              data-test="messenger-chip"
              :title="t('runners.worker.messengerTitle')"
              @click="messengerOpen = w.name"
            >{{ t('runners.worker.messenger', { text: messengerDisplay(w).text }) }}</button>
            <div v-if="w.worker?.labels && w.worker.labels.length" class="chips">
              <span v-for="l in w.worker.labels" :key="l" class="chip">{{ l }}</span>
            </div>
            <!-- 本机声明的外部工具（worker.yaml 的 tools），悬停看说明 -->
            <div v-if="runnerTools(w).length" class="chips" data-test="tool-chips">
              <span class="chips-label">{{ t('runners.worker.tools') }}</span>
              <span v-for="tool in runnerTools(w)" :key="tool.name" class="chip chip--tool" :title="toolTitle(tool)">{{ tool.name }}</span>
            </div>
            <div class="card-actions">
              <button v-if="can('worker.manage')" class="btn" type="button" :disabled="reloading === w.worker_id" @click="reloadWorkerConfig(w.worker_id || w.name)">
                {{ reloading === w.worker_id ? t('runners.worker.reloading') : t('runners.worker.reload') }}
              </button>
              <button
                v-if="can('worker.upgrade')"
                class="btn"
                type="button"
                data-test="upgrade-worker"
                :disabled="!!upgradeBlockReason(w, serverInfo) || upgrading === (w.worker_id || w.name)"
                :title="upgradeBlockReason(w, serverInfo) || t('runners.worker.upgradeTitle')"
                @click="upgradeWorkerBinary(w)"
              >
                {{ upgrading === (w.worker_id || w.name) || w.upgrade?.state === 'pending' ? t('runners.worker.upgrading') : t('runners.worker.upgrade') }}
              </button>
            </div>
            <RunnerDirs v-if="w.status === 'connected'" :runner="w" />
            <p v-if="w.worker?.draining" class="upgrade-line upgrade-line--warn">{{ t('runners.worker.draining') }}</p>
            <p v-if="w.upgrade" class="upgrade-line" :class="upgradeStateClass(w.upgrade)">{{ upgradeSummary(w.upgrade) }}</p>
            <p v-if="upgradeBlockReason(w, serverInfo) && w.upgrade?.state !== 'pending'" class="upgrade-hint">{{ upgradeBlockReason(w, serverInfo) }}</p>
            <details v-if="w.upgrade_history?.length" class="upgrade-history">
              <summary class="upgrade-hint">{{ t('runners.worker.history', { n: Math.min(w.upgrade_history.length, 5) }) }}</summary>
              <ul class="upgrade-history-list">
                <li v-for="item in w.upgrade_history.slice(0, 5)" :key="item.upgrade_id">
                  <span class="upgrade-time">{{ item.started_at ? fmtDateTime(Math.floor(item.started_at / 1000)) : '—' }}</span>
                  <span :class="upgradeStateClass(item)">{{ upgradeSummary(item) }}</span>
                </li>
              </ul>
            </details>
          </div>
        </article>
      </div>

      <!-- 空 workers 态：邀请接入 -->
      <div v-else class="empty empty--invite">
        <p class="empty-line">{{ t('runners.worker.emptyTitle') }}</p>
        <p class="empty-hint">{{ t('runners.worker.emptyHint') }}</p>
        <code class="empty-cmd">gofer worker --config worker.yaml</code>
      </div>
    </section>

    <!-- PEERS（peer-http） -->
    <section class="group" aria-labelledby="grp-peers">
      <header class="group-head">
        <h2 id="grp-peers" class="group-title">{{ t('runners.group.peers') }}</h2>
        <span class="group-count">{{ peers.length }}</span>
      </header>

      <div v-if="peers.length" class="cards">
        <article v-for="p in peers" :key="p.name" class="card card--peer">
          <div class="card-pulse">
            <StatusGlyph :family="peerState(p).family" :label="peerState(p).label" :size="14" />
          </div>
          <div class="card-main">
            <div class="card-row1">
              <span class="card-name">{{ p.name }}</span>
              <span class="card-meta">
                <span class="host" :title="p.base_url">{{ p.base_url || '—' }}</span>
                <template v-if="p.probe && p.probe.checked_at > 0">
                  <span class="dot-sep" aria-hidden="true">·</span>
                  <span class="age">{{ fmtAge(probeAgeMs(p)) }}</span>
                  <span class="dot-sep" aria-hidden="true">·</span>
                  <span class="latency">{{ p.probe.latency_ms }}ms</span>
                </template>
              </span>
              <StatusPill :family="peerState(p).family" :label="peerState(p).label" />
            </div>
            <p v-if="p.status === 'down' && p.probe?.error" class="probe-err">
              {{ p.probe.error }}
            </p>
          </div>
        </article>
      </div>

      <div v-else class="empty">{{ t('runners.peer.empty') }}</div>
    </section>

    <!-- LOCAL（恒在，恒 up） -->
    <section class="group" aria-labelledby="grp-local">
      <header class="group-head">
        <h2 id="grp-local" class="group-title">{{ t('runners.group.server') }}</h2>
        <span class="group-count">{{ locals.length }}</span>
      </header>

      <div v-if="locals.length" class="cards">
        <article v-for="l in locals" :key="l.name" class="card card--local">
          <div class="card-pulse">
            <StatusGlyph family="done" :label="t('runners.server.up')" :size="14" />
          </div>
          <div class="card-main">
            <div class="card-row1">
              <span class="card-name">{{ runnerLabelNote(l.name) }}</span>
              <span class="card-meta">
                <span class="host">{{ t('runners.server.inProcess') }}</span>
              </span>
              <StatusPill family="done" :label="t('runners.server.up')" />
            </div>
            <button
              class="msgr-chip"
              :class="`mtone--${messengerDisplay(l).tone}`"
              type="button"
              data-test="messenger-chip"
              :title="t('runners.worker.messengerTitle')"
              @click="messengerOpen = l.name"
            >{{ t('runners.worker.messenger', { text: messengerDisplay(l).text }) }}</button>
            <!-- server 配置顶层 tools：本机 runner 的外部工具 -->
            <div v-if="runnerTools(l).length" class="chips" data-test="tool-chips">
              <span class="chips-label">{{ t('runners.worker.tools') }}</span>
              <span v-for="tool in runnerTools(l)" :key="tool.name" class="chip chip--tool" :title="toolTitle(tool)">{{ tool.name }}</span>
            </div>
            <RunnerDirs :runner="l" />
          </div>
        </article>
      </div>

      <div v-else-if="loaded" class="empty">{{ t('runners.server.empty') }}</div>
    </section>

    <MessengerDrawer
      v-if="messengerRunner"
      :runner="messengerRunner"
      :now-ms="nowMs"
      @close="messengerOpen = ''"
    />

    <EnrollAcceptDialog
      v-if="acceptItem"
      :item="acceptItem"
      :projects="projects"
      :now-sec="nowSec"
      :existing-worker-ids="knownWorkerIds"
      @close="acceptItem = null"
      @accepted="onEnrollmentAccepted"
    />
    <EnrollRejectDialog
      v-if="rejectItem"
      :item="rejectItem"
      @close="rejectItem = null"
      @rejected="onEnrollmentRejected"
    />
  </div>
</template>

<style scoped>
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--s3);
  margin-bottom: var(--s4);
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
.poll-dot {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
}
.poll-dot--on {
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
.notice {
  color: var(--done);
  font-size: var(--fs-sm);
  margin: 0 0 var(--s2);
}
.hint {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  margin: 0 0 var(--s2);
}

.group {
  margin-bottom: var(--s5);
}
.group-head {
  display: flex;
  align-items: center;
  gap: var(--s2);
  margin: 0 0 var(--s2);
}
.group-title {
  margin: 0;
  font-size: var(--fs-sm);
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--ink-3);
}
.group-count {
  min-width: 18px;
  padding: 0 5px;
  border-radius: 999px;
  background: var(--surface-2);
  color: var(--ink-2);
  font-size: var(--fs-xs);
  text-align: center;
}
.topology-group .group-head {
  cursor: pointer;
}
.topology-group details[open] .topology-toggle {
  color: var(--ink-3);
}
.topology-toggle {
  margin-left: auto;
  color: var(--accent);
  font-size: var(--fs-xs);
}

.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-height: 30px;
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
.btn:hover:not(:disabled) {
  border-color: var(--accent);
}
.btn:disabled {
  color: var(--ink-3);
  cursor: default;
}
.btn--primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}
.btn--primary:hover:not(:disabled) {
  background: var(--accent-hover);
  border-color: var(--accent-hover);
}
.group-head .btn {
  margin-left: auto;
}

.add-form {
  display: flex;
  flex-wrap: wrap;
  gap: var(--s3);
  padding: var(--s3);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
}
.field {
  display: flex;
  flex-direction: column;
  flex: 1 1 180px;
  min-width: 0;
}
.label {
  font-size: var(--fs-xs);
  letter-spacing: 0.06em;
  color: var(--ink-3);
  margin-bottom: var(--s1);
}
.control {
  width: 100%;
  background: var(--sunken);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  padding: 8px 10px;
  font: inherit;
  font-size: var(--fs-sm);
  outline: none;
}
.add-form .control:focus {
  border-color: var(--accent);
}
.add-actions {
  display: flex;
  align-items: flex-end;
  gap: var(--s2);
}
.issued {
  margin-top: var(--s2);
  padding: var(--s2) var(--s3);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  background: var(--surface-2);
  font-size: var(--fs-sm);
}
.issued code {
  margin-left: var(--s1);
  word-break: break-all;
}

.cards {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
}
.card {
  display: flex;
  gap: var(--s3);
  padding: var(--s3);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
}
.card--enroll[data-state='pending'] {
  border-color: var(--need);
}
.card-pulse {
  flex: none;
  display: flex;
  align-items: flex-start;
  padding-top: 3px;
}
.card-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: var(--s1);
  align-items: flex-start;
}
.card-row1 {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: var(--s2);
  width: 100%;
}
.card-name {
  color: var(--ink);
  font-size: var(--fs-md);
  font-weight: 600;
  overflow-wrap: anywhere;
}
.card-meta {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  color: var(--ink-3);
  font-size: var(--fs-xs);
  min-width: 0;
}
.card-row1 .pill {
  margin-left: auto;
}
.dot-sep {
  color: var(--line-strong);
}
.age--down {
  color: var(--ink-3);
}
.node-line {
  display: flex;
  flex-wrap: wrap;
  gap: var(--s2);
  color: var(--ink-2);
  font-size: var(--fs-xs);
  font-family: var(--font-mono);
  overflow-wrap: anywhere;
}
.node-host {
  color: var(--ink);
}
.probe-err {
  margin: 0;
  color: var(--fail);
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s1);
  font-size: var(--fs-xs);
}
.chips-label {
  color: var(--ink-3);
}
.chip {
  padding: 0 6px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  color: var(--ink-2);
  font-size: var(--fs-xs);
  font-family: var(--font-mono);
}
.card-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--s2);
}
.msgr-chip {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  padding: 1px 8px;
  background: transparent;
  color: var(--ink-2);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-chip);
  font: inherit;
  font-size: var(--fs-xs);
  cursor: pointer;
}
.msgr-chip:hover {
  border-color: var(--accent);
}
.mtone--idle {
  color: var(--done);
}
.mtone--busy {
  color: var(--run);
}
.mtone--stopped {
  color: var(--ink-3);
}
.mtone--unknown {
  color: var(--ink-3);
  border-style: dashed;
}
.mtone--need {
  color: var(--need);
  border-color: var(--need);
}
.mtone--ok {
  color: var(--done);
}
.upgrade-line {
  margin: 0;
  font-size: var(--fs-xs);
  color: var(--ink-2);
}
.upgrade-line--warn {
  color: var(--need-ink);
}
.upgrade-hint {
  margin: 0;
  font-size: var(--fs-xs);
  color: var(--ink-3);
}
.upgrade-history summary {
  cursor: pointer;
}
.upgrade-history-list {
  list-style: none;
  margin: var(--s1) 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: var(--fs-xs);
}
.upgrade-history-list li {
  display: flex;
  gap: var(--s2);
  color: var(--ink-2);
}
.upgrade-time {
  color: var(--ink-3);
}

.st--ok {
  color: var(--done);
}
.st--warn {
  color: var(--time);
}
.st--down {
  color: var(--fail);
}

.enroll-line {
  margin: 0;
  color: var(--ink-2);
  font-size: var(--fs-sm);
}
.enroll-left {
  color: var(--need);
}

.empty {
  padding: var(--s5) var(--s3);
  text-align: center;
  color: var(--ink-3);
  font-size: var(--fs-sm);
  border: 1px dashed var(--line-strong);
  border-radius: var(--r-panel);
}
.empty-line {
  margin: 0 0 var(--s1);
  color: var(--ink-2);
}
.empty-hint {
  margin: 0 0 var(--s2);
  font-size: var(--fs-xs);
}
.empty-cmd {
  display: inline-block;
  padding: 3px 8px;
  background: var(--sunken);
  border-radius: var(--r-chip);
  font-size: var(--fs-xs);
}

@media (max-width: 760px) {
  .card {
    padding: var(--s2);
  }
  .card-row1 .pill {
    margin-left: 0;
  }
  .btn {
    min-height: 40px;
    flex: 1 1 auto;
  }
  .add-form {
    flex-direction: column;
  }
  .field {
    flex: 1 1 auto;
  }
}
</style>
