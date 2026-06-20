<script setup lang="ts">
// Job 详情：getJob 填头部 + SSE 单一来源（历史回放 + 实时跟随 + 终态 end）。
//  - 日志只走 streamJob（不传 from 获得回放+跟随）；from 仅用于断线重连（已收 stdout 字节数）。
//  - status 事件回填头部/徽标/耗时；end/终态停 live；running 显示 cancel。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import StatusBadge from '../components/StatusBadge.vue'
import Signal from '../components/Signal.vue'
import LogTape from '../components/LogTape.vue'
import InteractionCard from '../components/InteractionCard.vue'
import { answerInteraction, cancelJob, getJob } from '../api/client'
import { appendCapped, streamJob } from '../api/sse'
import { fmtDuration, jobDurationSec, toUnixSec } from '../api/time'
import type {
  Interaction,
  Job,
  JobStatus,
  SSEEvent,
  SSEInteractionData,
  SSELogData,
  SSELogRotatedData,
} from '../api/types'

const props = defineProps<{ id: string }>()

const job = ref<Job | null>(null)
const stdout = ref('')
const stderr = ref('')
const headError = ref('')
const streamError = ref('')
const cancelling = ref(false)

// 运行中交互：按 id upsert（SSE interaction 事件 + answer 返回回填）
const interactions = ref<Map<string, Interaction>>(new Map())
// 每张卡的提交态（防重复提交）
const submittingIds = ref<Set<string>>(new Set())
// 单条交互作答失败文案（按 id）
const interactionErrors = ref<Map<string, string>>(new Map())

function upsertInteraction(it: Interaction): void {
  // 重新赋值 new Map 触发响应式
  const next = new Map(interactions.value)
  next.set(it.id, it)
  interactions.value = next
}

// 待应答（pending），按 created_at 升序排队作答
const pendingInteractions = computed<Interaction[]>(() =>
  Array.from(interactions.value.values())
    .filter((it) => it.status === 'pending')
    .sort((a, b) => a.created_at - b.created_at),
)

// 已应答（answered），按 created_at 升序，折叠淡化展示
const answeredInteractions = computed<Interaction[]>(() =>
  Array.from(interactions.value.values())
    .filter((it) => it.status === 'answered')
    .sort((a, b) => a.created_at - b.created_at),
)

async function onAnswer(iid: string, value: string): Promise<void> {
  if (submittingIds.value.has(iid)) {
    return
  }
  // 标记 submitting + 清旧错误
  submittingIds.value = new Set(submittingIds.value).add(iid)
  if (interactionErrors.value.has(iid)) {
    const e = new Map(interactionErrors.value)
    e.delete(iid)
    interactionErrors.value = e
  }
  try {
    const updated = await answerInteraction(props.id, iid, value)
    // 乐观回填（SSE answered 事件也会回填，幂等）
    upsertInteraction(updated)
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e)
    interactionErrors.value = new Map(interactionErrors.value).set(iid, msg)
  } finally {
    const s = new Set(submittingIds.value)
    s.delete(iid)
    submittingIds.value = s
  }
}

// 终态集合
const TERMINAL: JobStatus[] = ['done', 'failed', 'cancelled', 'timeout']
function isTerminal(s: JobStatus | undefined): boolean {
  return s != null && TERMINAL.includes(s)
}

const status = computed<JobStatus>(() => job.value?.status ?? 'queued')
const live = computed(() => status.value === 'running')
const showCancel = computed(() => status.value === 'running' && !cancelling.value)
// 头部 exit_code 仅在终态展示（运行中无意义）
const isTerminalView = computed(() => isTerminal(job.value?.status))

// 实时秒级时钟（驱动 running 耗时刷新）
const nowSec = ref(Math.floor(Date.now() / 1000))
let clockTimer: number | null = null

const durationSec = computed(() => {
  if (!job.value) {
    return null
  }
  return jobDurationSec(job.value, nowSec.value)
})
const durationText = computed(() => fmtDuration(durationSec.value))

// SSE 速率估算：滑动窗口内收到的 stdout/stderr 文本行数 / 时间 -> 行每秒
const recentLines = ref<Array<{ t: number; n: number }>>([])
const logRate = computed(() => {
  const cutoff = Date.now() - 4000
  const recent = recentLines.value.filter((r) => r.t >= cutoff)
  const lines = recent.reduce((a, r) => a + r.n, 0)
  return lines > 0 ? lines / 4 : 0
})

// 已累计接收的 stdout 字节数（按 UTF-8 字节计，用于断线重连 from）
const encoder = new TextEncoder()
let stdoutBytes = 0

let abortCtrl: AbortController | null = null
let reconnectedOnce = false

function applyStatus(j: Job): void {
  // 合并字段（status 事件可能只带部分信息，但后端给的是完整 Job）
  job.value = { ...(job.value ?? {}), ...j } as Job
}

function onEvent(ev: SSEEvent): void {
  if (ev.type === 'status') {
    applyStatus(ev.data as Job)
    return
  }
  if (ev.type === 'log-rotated') {
    // 后端日志轮转：清空该 stream 的缓冲后续读新文件（不重置 stdoutBytes，
    // 它用于断线重连的 from offset，由后端 offset 语义对齐）。
    const d = ev.data as SSELogRotatedData
    if (d.stream === 'stderr') {
      stderr.value = ''
    } else {
      stdout.value = ''
    }
    return
  }
  if (ev.type === 'log') {
    const d = ev.data as SSELogData
    // 帧按到达顺序（= seq 顺序，单连接 TCP 有序）追加，并窗口化到字节上限：
    // 超大/高频日志只保留最近 N 字节，避免浏览器内存无界增长（C4 前端兜底）。
    if (d.stream === 'stdout') {
      stdout.value = appendCapped(stdout.value, d.text)
      stdoutBytes += encoder.encode(d.text).length
    } else {
      stderr.value = appendCapped(stderr.value, d.text)
    }
    const n = countLines(d.text)
    if (n > 0) {
      recentLines.value.push({ t: Date.now(), n })
      // 限制窗口大小
      if (recentLines.value.length > 200) {
        recentLines.value.splice(0, recentLines.value.length - 200)
      }
    }
    return
  }
  if (ev.type === 'interaction') {
    const d = ev.data as SSEInteractionData
    // action: open/answered/cancelled —— 统一按 id upsert（幂等）
    upsertInteraction(d.interaction)
    return
  }
  // end：无更多事件；终态由 status 事件回填，这里仅停 live 由 status 决定
}

function countLines(text: string): number {
  if (!text) {
    return 0
  }
  let c = 0
  for (const ch of text) {
    if (ch === '\n') {
      c++
    }
  }
  return c
}

async function startStream(from?: number): Promise<void> {
  abortCtrl = new AbortController()
  try {
    await streamJob(props.id, { from, signal: abortCtrl.signal, onEvent })
    // 流正常结束：若非终态且未重连过 -> 自动用 from 重连一次
    if (!isTerminal(job.value?.status) && !reconnectedOnce) {
      reconnectedOnce = true
      void startStream(stdoutBytes)
    }
  } catch (e) {
    if (abortCtrl?.signal.aborted) {
      return
    }
    // 异常结束：非终态自动重连一次，再失败提示手动重连
    if (!isTerminal(job.value?.status) && !reconnectedOnce) {
      reconnectedOnce = true
      void startStream(stdoutBytes)
    } else {
      streamError.value = e instanceof Error ? e.message : String(e)
    }
  }
}

function manualReconnect(): void {
  streamError.value = ''
  reconnectedOnce = false
  void startStream(stdoutBytes)
}

async function doCancel(): Promise<void> {
  cancelling.value = true
  try {
    const j = await cancelJob(props.id)
    // 乐观：以返回回填；最终终态以后续 status 事件为准
    applyStatus(j)
  } catch (e) {
    cancelling.value = false
    streamError.value = e instanceof Error ? e.message : String(e)
  }
}

function startClock(): void {
  clockTimer = window.setInterval(() => {
    nowSec.value = Math.floor(Date.now() / 1000)
  }, 1000)
}

onMounted(async () => {
  startClock()
  // 先取头部（即便 stream 也会回填，但 getJob 让头部更快可见）
  try {
    job.value = await getJob(props.id)
  } catch (e) {
    headError.value = e instanceof Error ? e.message : String(e)
  }
  // 日志单一来源：不传 from，获得「历史回放 + 实时跟随 + 终态 end」
  void startStream()
})

onUnmounted(() => {
  if (abortCtrl) {
    abortCtrl.abort()
  }
  if (clockTimer != null) {
    window.clearInterval(clockTimer)
    clockTimer = null
  }
})

function fmtTime(v: string | number | undefined): string {
  const sec = toUnixSec(v)
  if (sec == null) {
    return '—'
  }
  return new Date(sec * 1000).toLocaleString()
}

function shortId(id: string): string {
  return id.length > 8 ? id.slice(-8) : id
}
</script>

<template>
  <div class="detail">
    <div class="detail-head">
      <RouterLink to="/board" class="back mono">← board</RouterLink>
      <div class="head-right">
        <StatusBadge v-if="job" :status="status" />
        <Signal v-if="job" :status="status" :rate="logRate" :duration-sec="durationSec" />
        <button
          v-if="showCancel"
          class="cancel mono"
          type="button"
          @click="doCancel"
        >
          取消
        </button>
        <span v-else-if="cancelling && live" class="cancelling mono">取消中…</span>
      </div>
    </div>

    <header v-if="job" class="job-header">
      <h1 v-if="job.title" class="job-name" :title="job.title">{{ job.title }}</h1>
      <h1 v-else class="job-name job-name--id mono" :title="job.id">{{ job.id }}</h1>
      <span v-if="job.title" class="job-subid mono" :title="job.id">{{ shortId(job.id) }}</span>
    </header>

    <p v-if="headError" class="error mono">{{ headError }}</p>

    <div v-if="job" class="meta">
      <div class="meta-item">
        <span class="meta-k mono">id</span><span class="meta-v mono id">{{ job.id }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-k mono">project</span><span class="meta-v mono">{{ job.project_key }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-k mono">agent</span><span class="meta-v mono">{{ job.agent }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-k mono">runner</span><span class="meta-v mono">{{ job.runner }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-k mono">cwd</span><span class="meta-v mono">{{ job.cwd }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-k mono">started</span><span class="meta-v mono">{{ fmtTime(job.started_at) }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-k mono">duration</span><span class="meta-v mono">{{ durationText }}</span>
      </div>
      <div class="meta-item">
        <span class="meta-k mono">exit_code</span>
        <span class="meta-v mono" :class="{ bad: job.exit_code !== 0 && isTerminalView }">
          {{ isTerminalView ? job.exit_code : '—' }}
        </span>
      </div>
    </div>

    <p v-if="job?.error" class="error mono">{{ job.error }}</p>

    <p v-if="streamError" class="stream-err mono">
      连接断开：{{ streamError }}
      <button class="reconnect" type="button" @click="manualReconnect">点击重连</button>
    </p>

    <!-- 运行中交互区：待应答卡片（排队作答）+ 已应答折叠 -->
    <section
      v-if="pendingInteractions.length > 0 || answeredInteractions.length > 0"
      class="interactions"
    >
      <h2 class="interactions-title mono">
        <span class="warn">⚠</span> 待应答交互
        <span v-if="pendingInteractions.length > 0" class="count mono"
          >{{ pendingInteractions.length }}</span
        >
      </h2>

      <div v-for="it in pendingInteractions" :key="it.id" class="icard-wrap">
        <InteractionCard
          :interaction="it"
          :submitting="submittingIds.has(it.id)"
          @answer="(v) => onAnswer(it.id, v)"
        />
        <p v-if="interactionErrors.get(it.id)" class="icard-err mono">
          作答失败：{{ interactionErrors.get(it.id) }}
        </p>
      </div>

      <details v-if="answeredInteractions.length > 0" class="answered-fold">
        <summary class="mono">
          已应答 {{ answeredInteractions.length }} 条
        </summary>
        <InteractionCard
          v-for="it in answeredInteractions"
          :key="it.id"
          :interaction="it"
        />
      </details>
    </section>

    <LogTape :stdout="stdout" :stderr="stderr" :live="live" />
  </div>
</template>

<style scoped>
.detail {
  max-width: 1200px;
  margin: 0 auto;
}
.detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.back {
  font-size: 13px;
  color: var(--queue);
}
.back:hover {
  color: var(--phosphor);
}
.head-right {
  display: flex;
  align-items: center;
  gap: 16px;
}
.cancel {
  background: transparent;
  color: var(--fail);
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 4px 12px;
  font-size: 12px;
}
.cancel:hover {
  background: var(--fail);
  color: var(--ink);
}
.cancelling {
  color: var(--run);
  font-size: 12px;
}

/* Prominent job name (the human title) with the short id as secondary; falls
   back to the full id as the name when the job has no title. */
.job-header {
  display: flex;
  align-items: baseline;
  gap: 12px;
  margin: 0 0 12px;
  min-width: 0;
}
.job-name {
  font-size: 18px;
  font-weight: 600;
  color: var(--paper);
  margin: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 0;
}
.job-name--id {
  font-size: 15px;
  color: var(--phosphor);
}
.job-subid {
  font-size: 12px;
  color: var(--phosphor);
  flex: none;
}

.meta {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 6px 24px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 12px 16px;
  margin-bottom: 14px;
}
.meta-item {
  display: flex;
  gap: 12px;
  font-size: 12px;
  min-width: 0;
}
.meta-k {
  color: var(--queue);
  text-transform: uppercase;
  letter-spacing: 0.06em;
  width: 78px;
  flex: none;
}
.meta-v {
  color: var(--paper);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meta-v.id {
  color: var(--phosphor);
}
.meta-v.bad {
  color: var(--fail);
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
.stream-err {
  color: var(--run);
  font-size: 12px;
  margin: 0 0 12px;
}
.reconnect {
  margin-left: 10px;
  background: var(--run);
  color: var(--ink);
  border: none;
  border-radius: var(--radius);
  padding: 3px 10px;
  font-size: 11px;
  font-weight: 600;
}

.interactions {
  margin: 0 0 14px;
}
.interactions-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  letter-spacing: 0.06em;
  color: var(--phosphor);
  text-transform: uppercase;
  margin: 0 0 10px;
}
.interactions-title .warn {
  color: var(--phosphor);
}
.interactions-title .count {
  background: var(--phosphor);
  color: var(--ink);
  border-radius: var(--radius);
  padding: 0 6px;
  font-size: 11px;
  font-weight: 600;
}
.icard-wrap {
  margin-bottom: 4px;
}
.icard-err {
  color: var(--fail);
  font-size: 11px;
  margin: -4px 0 10px;
}
.answered-fold {
  margin-top: 6px;
}
.answered-fold > summary {
  cursor: pointer;
  color: var(--queue);
  font-size: 11px;
  letter-spacing: 0.06em;
  padding: 4px 0;
  list-style: revert;
}
.answered-fold > summary:hover {
  color: var(--phosphor);
}
</style>
