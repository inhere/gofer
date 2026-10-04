<script setup lang="ts">
// 扇出步对比视图（Z4）：每个 fan 一列 —— agent、状态、耗时、diff 摘要 / 领先提交、verify、汇报尾部，
// 可并排展开 diff（复用 UnifiedDiff 与 /v1/jobs/{id}/diff）；join=pick 的步停在"待择优"时
// 每列有「选这个并合并」，选定后标出已选列与合并结果。手机宽度下改为一次一张纵向卡片，可左右切换。
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import StatusBadge from './StatusBadge.vue'
import UnifiedDiff from './UnifiedDiff.vue'
import MergeDialog from './MergeDialog.vue'
import { fetchDiffText, getJob, getJobWorktree, logsTail } from '../api/client'
import { fmtDuration } from '../api/time'
import type { Job, WorkflowStatus, WorkflowStep, WorktreeStatus } from '../api/types'
import {
  awaitingPick,
  buildColumns,
  canPickColumn,
  isPickStep,
  isTerminalStatus,
  columnMergeReason,
  pickedFan,
  type FanColumn,
} from '../utils/compare'
import { runnerLabel } from '../utils/runnerDisplay'

const props = defineProps<{
  workflowId: string
  workflowStatus: WorkflowStatus | undefined
  currentStep: number
  stepIndex: number
  rows: WorkflowStep[]
}>()
const emit = defineEmits<{
  (e: 'open-job', jobId: string): void
  (e: 'changed'): void
}>()

const jobs = ref<Record<string, Job | undefined>>({})
const tails = ref<Record<string, string | undefined>>({})
const worktrees = ref<Record<string, WorktreeStatus | undefined>>({})
const diffs = ref<Record<number, { text: string; loading: boolean; error: string } | undefined>>({})
const diffOpen = ref<Record<number, boolean>>({})
const active = ref(0)
const dialogFan = ref<number | null>(null)
const lastResult = ref('')
let disposed = false

const pickStep = computed(() => isPickStep(props.rows))
const awaiting = computed(() =>
  awaitingPick(props.rows, { workflowStatus: props.workflowStatus, currentStep: props.currentStep, stepIndex: props.stepIndex }),
)
const picked = computed(() => pickedFan(props.rows))
const columns = computed<FanColumn[]>(() => buildColumns(props.rows, jobs.value, tails.value))
const dialogCol = computed(() => columns.value.find((c) => c.fanIndex === dialogFan.value))
const allOpen = computed(() => columns.value.length > 0 && columns.value.every((c) => diffOpen.value[c.fanIndex]))

watch(
  columns,
  (cols) => {
    if (active.value >= cols.length) active.value = 0
  },
  { flush: 'post' },
)

async function refresh(): Promise<void> {
  for (const col of columns.value) {
    if (!col.jobId) continue
    const cached = jobs.value[col.jobId]
    if (!cached || !isTerminalStatus(cached.status) || cached.status !== (col.status as string)) {
      try {
        const j = await getJob(col.jobId)
        if (disposed) return
        jobs.value = { ...jobs.value, [col.jobId]: j }
      } catch {
        // 单列拉取失败不影响其它列；下一轮轮询重试
      }
    }
    const fresh = jobs.value[col.jobId]
    if (fresh && isTerminalStatus(fresh.status) && tails.value[col.jobId] === undefined) {
      tails.value = { ...tails.value, [col.jobId]: '' }
      try {
        const t = await logsTail(col.jobId, 'stdout', 4096)
        if (disposed) return
        tails.value = { ...tails.value, [col.jobId]: t }
      } catch {
        // 没有 stdout 就留空
      }
    }
  }
  await refreshPickedWorktree()
}

// 已选列：显示它的分支是否已合并到基线（实时探测，只对本机 runner 有意义）。
async function refreshPickedWorktree(): Promise<void> {
  const col = columns.value.find((c) => c.fanIndex === picked.value)
  if (!col || !col.jobId || col.remote) return
  try {
    const st = await getJobWorktree(col.jobId)
    if (disposed) return
    worktrees.value = { ...worktrees.value, [col.jobId]: st }
  } catch {
    // worktree 已被清理等：不显示合并状态
  }
}

watch(() => props.rows, () => void refresh(), { deep: true, immediate: true })
onBeforeUnmount(() => {
  disposed = true
})

async function toggleDiff(col: FanColumn): Promise<void> {
  const open = !diffOpen.value[col.fanIndex]
  diffOpen.value = { ...diffOpen.value, [col.fanIndex]: open }
  if (open) await loadDiff(col)
}

async function loadDiff(col: FanColumn): Promise<void> {
  if (!col.jobId || diffs.value[col.fanIndex]?.text) return
  diffs.value = { ...diffs.value, [col.fanIndex]: { text: '', loading: true, error: '' } }
  try {
    const text = await fetchDiffText(col.jobId)
    diffs.value = { ...diffs.value, [col.fanIndex]: { text, loading: false, error: '' } }
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e)
    // 没产出改动的 job 没有 diff：中性说明，不当错误报
    diffs.value = { ...diffs.value, [col.fanIndex]: { text: '', loading: false, error: msg.includes('no diff') ? '' : msg } }
  }
}

async function toggleAll(): Promise<void> {
  const open = !allOpen.value
  const next: Record<number, boolean> = {}
  for (const col of columns.value) next[col.fanIndex] = open
  diffOpen.value = next
  if (open) await Promise.all(columns.value.map((c) => loadDiff(c)))
}

function openDialog(col: FanColumn): void {
  dialogFan.value = col.fanIndex
}

function onDialogDone(r: { merged: boolean; picked: boolean }): void {
  lastResult.value = r.merged ? (r.picked ? '已选择并合并' : '已合并') : r.picked ? '已选择（未合并）' : ''
  emit('changed')
  void refreshPickedWorktree()
}

function onDialogClose(): void {
  dialogFan.value = null
}

function pickDisabledReason(col: FanColumn): string {
  if (!awaiting.value) return ''
  if (col.status !== 'done') return '这一路没有成功完成，不能选'
  return ''
}

function mergedBadge(col: FanColumn): string {
  const st = worktrees.value[col.jobId]
  if (!st) return ''
  return st.merged ? '已合并到基线' : st.exists ? '尚未合并' : '分支已清理'
}

const hint = computed(() => {
  if (!pickStep.value) return ''
  if (picked.value) return `已选 fan ${picked.value}`
  if (awaiting.value) return '待择优：选一路并合并，工作流才会继续'
  if (props.workflowStatus === 'running') return '等待各路结束后择优'
  return '未择优'
})
</script>

<template>
  <div class="cmp" data-test="compare">
    <div class="cmp-bar">
      <span v-if="hint" class="cmp-hint mono" :class="{ 'cmp-hint--attn': awaiting }" data-test="compare-hint">{{ hint }}</span>
      <span v-if="lastResult" class="cmp-hint cmp-hint--ok mono">{{ lastResult }}</span>
      <button class="cmp-link mono" type="button" data-test="compare-diff-all" @click="toggleAll">
        {{ allOpen ? '收起全部 diff' : '并排展开 diff' }}
      </button>
    </div>

    <div class="cmp-tabs" role="tablist" data-test="compare-tabs">
      <button
        v-for="(col, i) in columns"
        :key="col.fanIndex"
        class="cmp-tab mono"
        :class="{ 'cmp-tab--on': i === active }"
        type="button"
        role="tab"
        @click="active = i"
      >
        {{ col.agent || `fan ${col.fanIndex}` }}
        <span v-if="col.picked" class="cmp-tab-pick">✓</span>
      </button>
    </div>

    <div class="cmp-grid" :style="{ '--cols': columns.length }">
      <article
        v-for="(col, i) in columns"
        :key="col.fanIndex"
        class="col"
        :class="{ 'col--active': i === active, 'col--picked': col.picked, 'col--dim': picked > 0 && !col.picked }"
        :data-test="`compare-col-${col.fanIndex}`"
      >
        <header class="col-head">
          <span class="col-agent mono">{{ col.agent || `fan ${col.fanIndex}` }}</span>
          <span class="col-fan mono">f{{ col.fanIndex }}</span>
          <span v-if="col.picked" class="col-badge mono" data-test="picked-badge">已选</span>
          <StatusBadge v-if="col.status !== 'pending'" :status="col.status" />
          <span v-else class="col-pending mono">pending</span>
        </header>

        <dl class="col-meta mono">
          <div class="col-row"><dt>耗时</dt><dd>{{ fmtDuration(col.durationSec) }}</dd></div>
          <div class="col-row"><dt>runner</dt><dd>{{ runnerLabel(col.runner) || '—' }}</dd></div>
          <div class="col-row">
            <dt>领先提交</dt>
            <dd>{{ col.commitsAhead == null ? '—' : col.commitsAhead }}<template v-if="col.branch"> · {{ col.branch }}</template></dd>
          </div>
          <div class="col-row">
            <dt>verify</dt>
            <dd>
              <span v-if="col.verify" class="vr" :class="`vr--${col.verify.status}`">{{ col.verify.text }}</span>
              <template v-else>—</template>
            </dd>
          </div>
          <div v-if="col.retries" class="col-row"><dt>重试</dt><dd>{{ col.retries }} 次</dd></div>
          <div v-if="mergedBadge(col)" class="col-row"><dt>合并</dt><dd data-test="merged-state">{{ mergedBadge(col) }}</dd></div>
        </dl>

        <pre v-if="col.diffSummary" class="col-diffsum mono" data-test="diff-summary">{{ col.diffSummary }}</pre>
        <p v-else class="col-none mono">没有 diff 摘要</p>

        <p v-if="col.error" class="col-error mono">{{ col.error }}</p>
        <pre v-if="col.tail" class="col-tail mono" data-test="tail">{{ col.tail }}</pre>

        <div class="col-actions">
          <button
            v-if="pickStep && !col.picked && (awaiting || !picked)"
            class="btn btn--go mono"
            type="button"
            :disabled="!canPickColumn(col, awaiting)"
            :title="pickDisabledReason(col) || '选这一路'"
            data-test="pick-btn"
            @click="openDialog(col)"
          >
            选这个并合并
          </button>
          <button
            v-else-if="pickStep && col.picked && mergedBadge(col) === '尚未合并' && !columnMergeReason(col)"
            class="btn mono"
            type="button"
            data-test="merge-btn"
            @click="openDialog(col)"
          >
            合并到基线
          </button>
          <button class="btn mono" type="button" data-test="diff-toggle" @click="toggleDiff(col)">
            {{ diffOpen[col.fanIndex] ? '收起 diff' : '展开 diff' }}
          </button>
          <button v-if="col.jobId" class="btn mono" type="button" :title="col.jobId" @click="emit('open-job', col.jobId)">
            job &rarr;
          </button>
        </div>
        <p v-if="pickStep && awaiting && pickDisabledReason(col)" class="col-none mono">{{ pickDisabledReason(col) }}</p>

        <div v-if="diffOpen[col.fanIndex]" class="col-diff" data-test="col-diff">
          <p v-if="diffs[col.fanIndex]?.loading" class="col-none mono">加载 diff…</p>
          <p v-else-if="diffs[col.fanIndex]?.error" class="col-error mono">{{ diffs[col.fanIndex]?.error }}</p>
          <p v-else-if="!diffs[col.fanIndex]?.text" class="col-none mono">没有 diff</p>
          <UnifiedDiff v-else :text="diffs[col.fanIndex]!.text" :download-name="`changes-${col.jobId.slice(0, 8)}.diff`" />
        </div>
      </article>
    </div>

    <div class="cmp-pager mono">
      <button class="btn mono" type="button" :disabled="active <= 0" aria-label="上一路" @click="active--">‹</button>
      <span>{{ columns.length ? active + 1 : 0 }} / {{ columns.length }}</span>
      <button class="btn mono" type="button" :disabled="active >= columns.length - 1" aria-label="下一路" @click="active++">›</button>
    </div>

    <MergeDialog
      v-if="dialogCol"
      :job-id="dialogCol.jobId"
      :branch="dialogCol.branch"
      :pick="picked ? null : { workflowId, step: stepIndex, fan: dialogCol.fanIndex, agent: dialogCol.agent }"
      :can-cleanup="true"
      :remote-reason="columnMergeReason(dialogCol)"
      @close="onDialogClose"
      @done="onDialogDone"
    />
  </div>
</template>

<style scoped>
.cmp {
  margin: 8px 0 4px;
}
.cmp-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
  flex-wrap: wrap;
}
.cmp-hint {
  font-size: 12px;
  color: var(--queue);
}
.cmp-hint--attn {
  color: var(--phosphor);
}
.cmp-hint--ok {
  color: var(--done);
}
.cmp-link {
  margin-left: auto;
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 8px;
  font-size: 12px;
}
.cmp-link:hover {
  border-color: var(--phosphor);
}
.cmp-tabs,
.cmp-pager {
  display: none;
}
.cmp-grid {
  display: grid;
  grid-template-columns: repeat(var(--cols, 2), minmax(0, 1fr));
  gap: 10px;
  align-items: start;
}
.col {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  padding: 10px;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.col--picked {
  border-color: var(--done);
}
.col--dim {
  opacity: 0.7;
}
.col-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.col-agent {
  color: var(--paper);
  font-size: 14px;
  font-weight: 600;
}
.col-fan,
.col-pending {
  color: var(--queue);
  font-size: 11px;
}
.col-badge {
  font-size: 11px;
  color: var(--ink);
  background: var(--done);
  border-radius: var(--radius);
  padding: 1px 6px;
}
.col-meta {
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
}
.col-row {
  display: grid;
  grid-template-columns: 64px minmax(0, 1fr);
  gap: 8px;
}
.col-row dt {
  color: var(--queue);
}
.col-row dd {
  margin: 0;
  color: var(--paper);
  word-break: break-all;
}
.vr--passed {
  color: var(--done);
}
.vr--failed,
.vr--timeout {
  color: var(--fail);
}
.col-diffsum,
.col-tail {
  margin: 0;
  background: var(--term-bg);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 6px 8px;
  font-size: 11px;
  color: var(--paper);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 150px;
  overflow: auto;
}
.col-tail {
  color: var(--queue);
}
.col-none {
  margin: 0;
  font-size: 11px;
  color: var(--queue);
}
.col-error {
  margin: 0;
  font-size: 11px;
  color: var(--fail);
  word-break: break-word;
}
.col-actions {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.btn {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
}
.btn:hover:not(:disabled) {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.btn--go {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.col-diff {
  min-width: 0;
  max-height: 480px;
  overflow: auto;
  border-top: 1px solid var(--line);
  padding-top: 6px;
}

/* 手机：一次一张纵向卡片，顶部 tab / 底部 ‹ › 切换 */
@media (max-width: 720px) {
  .cmp-tabs {
    display: flex;
    gap: 6px;
    overflow-x: auto;
    margin-bottom: 8px;
  }
  .cmp-tab {
    flex: 0 0 auto;
    background: transparent;
    color: var(--queue);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 4px 10px;
    font-size: 12px;
  }
  .cmp-tab--on {
    color: var(--phosphor);
    border-color: var(--phosphor);
  }
  .cmp-grid {
    display: block;
  }
  .col:not(.col--active) {
    display: none;
  }
  .cmp-pager {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 14px;
    margin-top: 8px;
    font-size: 12px;
    color: var(--queue);
  }
}
</style>
