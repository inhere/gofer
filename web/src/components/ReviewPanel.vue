<script setup lang="ts">
// job 详情验收面板（REV-01 §一.2）：五个页签（汇报 / 提交 / Diff / 验证 / 用量）+
// 底部固定操作条（Accept / Reject）。
//
// 出现时机由 JobDetail 判定：status ∈ needs_review | rejected，或 done 且 require_review。
// 数据一律走既有接口：汇报 = stdout 尾部 64KB（markdown 渲染），Diff = diff?full=1 原文，
// 验证 = 从 stderr 尾部 64KB 里截最后一段 verify 横幅之间的输出；提交/用量直接用 job 行。
// 页签按需加载（默认停在「汇报」），所以打开一个 needs_review job 只多一次 stdout 请求。
import { computed, onMounted, ref, watch } from 'vue'
import type { Ref } from 'vue'
import MarkdownBlock from './MarkdownBlock.vue'
import RejectDialog from './RejectDialog.vue'
import StatusBadge from './StatusBadge.vue'
import UnifiedDiff from './UnifiedDiff.vue'
import { acceptJob, fetchDiffText, logsTail, rejectJob } from '../api/client'
import { fmtDateTime } from '../api/time'
import { formatTokens, shortSha, usageLine, verifyClass, verifyLabel } from '../utils/jobOutcome'
import type { Job } from '../api/types'

const props = defineProps<{ job: Job }>()
const emit = defineEmits<{
  (e: 'updated', job: Job): void
  (e: 'resumed', jobId: string): void
}>()

// 汇报与 verify 输出都取日志尾部 64KB：一份 agent 汇报/一次 verify 输出远小于它。
const TAIL_BYTES = 65536
// verify 输出最多渲染 200 行（超出只留尾部提示行）。
const MAX_VERIFY_LINES = 200

type Tab = 'report' | 'commits' | 'diff' | 'verify' | 'usage'

const TABS: Array<{ id: Tab; label: string }> = [
  { id: 'report', label: '汇报' },
  { id: 'commits', label: '提交' },
  { id: 'diff', label: 'Diff' },
  { id: 'verify', label: '验证' },
  { id: 'usage', label: '用量' },
]

const tab = ref<Tab>('report')

// 一个按需加载的文本资源（loading 防重入，loaded 成功后才置位 → 失败可再点页签重试）。
interface Resource {
  text: string
  loading: boolean
  error: string
  loaded: boolean
}

function newResource(): Resource {
  return { text: '', loading: false, error: '', loaded: false }
}

const report = ref<Resource>(newResource())
const diff = ref<Resource>(newResource())
const verifyOut = ref<Resource>(newResource())

async function load(res: Ref<Resource>, fetchText: () => Promise<string>): Promise<void> {
  if (res.value.loading || res.value.loaded) {
    return
  }
  res.value = { ...res.value, loading: true, error: '' }
  try {
    res.value = { text: await fetchText(), loading: false, error: '', loaded: true }
  } catch (e) {
    res.value = { ...res.value, loading: false, error: e instanceof Error ? e.message : String(e) }
  }
}

function ensureTab(t: Tab): void {
  if (t === 'report') {
    void load(report, () => logsTail(props.job.id, 'stdout', TAIL_BYTES))
    return
  }
  if (t === 'diff') {
    void load(diff, () => fetchDiffText(props.job.id))
    return
  }
  // skipped 是"agent 没正常结束所以没跑"，不会写横幅，不必拉 stderr。
  if (t === 'verify' && verify.value && verify.value.status !== 'skipped') {
    void load(verifyOut, () => logsTail(props.job.id, 'stderr', TAIL_BYTES))
  }
}

// 失败后重来：清掉 loaded 再走一次 ensureTab（load 只在 loaded 时短路）。
function retry(t: Tab): void {
  const res = t === 'report' ? report : t === 'diff' ? diff : verifyOut
  res.value = { ...res.value, loaded: false }
  ensureTab(t)
}

const commits = computed(() => props.job.commits ?? [])
const verify = computed(() => props.job.verify ?? null)
const usage = computed(() => props.job.usage ?? null)

// 提交页签头部：base_sha → HEAD（HEAD 取最新提交，worktree job 回落到 worktree_head_sha）。
const headSha = computed(() => commits.value[0]?.sha ?? props.job.worktree_head_sha ?? '')

// verify 输出：stderr 里最后一段 `===== gofer verify: <argv> =====` 到它的
// `===== gofer verify: exit=… dur=… =====` 之间（含两端横幅），最多 200 行。
const verifyOutput = computed(() => {
  const lines = verifyOut.value.text.split('\n')
  let start = -1
  for (let i = lines.length - 1; i >= 0; i--) {
    if (lines[i].startsWith('===== gofer verify: ') && !lines[i].includes(' exit=')) {
      start = i
      break
    }
  }
  if (start < 0) {
    return ''
  }
  let end = lines.length - 1
  for (let i = start + 1; i < lines.length; i++) {
    if (lines[i].startsWith('===== gofer verify: ') && lines[i].includes(' exit=')) {
      end = i
      break
    }
  }
  const block = lines.slice(start, end + 1)
  if (block.length <= MAX_VERIFY_LINES) {
    return block.join('\n')
  }
  return [...block.slice(0, MAX_VERIFY_LINES), `…（已截断，本段共 ${block.length} 行）`].join('\n')
})

// 用量页签逐项列出（缺项显示 "—"：agent 没报 ≠ 0，这一页就是要看"哪项没报"）。
const usageRows = computed<Array<{ k: string; v: string }>>(() => {
  const u = usage.value
  const tok = (n: number | undefined): string => (n == null || n <= 0 ? '—' : formatTokens(n))
  return [
    { k: 'input_tokens', v: tok(u?.input_tokens) },
    { k: 'output_tokens', v: tok(u?.output_tokens) },
    { k: 'cache_read_tokens', v: tok(u?.cache_read_tokens) },
    { k: 'cache_write_tokens', v: tok(u?.cache_write_tokens) },
    { k: 'total_tokens', v: tok(u?.total_tokens) },
    { k: 'cost_usd', v: (u?.cost_usd ?? 0) > 0 ? `$${(u?.cost_usd as number).toFixed(4)}` : '—' },
    { k: 'source', v: u?.source || '—' },
  ]
})

const usageOneLine = computed(() => usageLine(usage.value))
const verifyText = computed(() => verifyLabel(verify.value))
const verifyTone = computed(() => verifyClass(verify.value))
const verifyCommand = computed(() => (verify.value?.command ?? []).join(' '))

// 裁决：accept 直接通过；reject 必写理由（可选自动续投 → 跳新 job）。
const reviewed = computed(() => !!props.job.reviewed_by)
const canDecide = computed(() => props.job.status === 'needs_review')
const accepting = ref(false)
const rejecting = ref(false)
const reviewError = ref('')
const rejectOpen = ref(false)
const copiedSha = ref('')

async function copySha(sha: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(sha)
    copiedSha.value = sha
    window.setTimeout(() => {
      if (copiedSha.value === sha) {
        copiedSha.value = ''
      }
    }, 1500)
  } catch {
    // 剪贴板不可用（非安全上下文）时静默：sha 仍可手动选中复制。
  }
}

async function onAccept(): Promise<void> {
  if (accepting.value) {
    return
  }
  accepting.value = true
  reviewError.value = ''
  try {
    emit('updated', await acceptJob(props.job.id))
  } catch (e) {
    reviewError.value = e instanceof Error ? e.message : String(e)
  } finally {
    accepting.value = false
  }
}

async function onReject(note: string, resume: boolean): Promise<void> {
  if (rejecting.value) {
    return
  }
  rejecting.value = true
  reviewError.value = ''
  try {
    const res = await rejectJob(props.job.id, note, resume)
    rejectOpen.value = false
    emit('updated', res)
    if (res.resume_job_id) {
      emit('resumed', res.resume_job_id)
    }
  } catch (e) {
    reviewError.value = e instanceof Error ? e.message : String(e)
  } finally {
    rejecting.value = false
  }
}

// 换 job（详情页内跳转）时重置页签与已加载的文本。
watch(
  () => props.job.id,
  () => {
    tab.value = 'report'
    report.value = newResource()
    diff.value = newResource()
    verifyOut.value = newResource()
    reviewError.value = ''
    rejectOpen.value = false
    ensureTab('report')
  },
)

watch(tab, (t) => ensureTab(t))

onMounted(() => ensureTab(tab.value))
</script>

<template>
  <section class="rp">
    <div class="rp-head">
      <span class="rp-title mono">验收</span>
      <StatusBadge :status="job.status" />
      <span class="rp-hint mono">
        <template v-if="canDecide">agent 已停，交付物等人定论：通过即 done；拒绝必须写明理由。</template>
        <template v-else>已裁决，下面是当时的验收材料。</template>
      </span>
    </div>

    <div class="rp-tabs mono" role="tablist">
      <button
        v-for="t in TABS"
        :key="t.id"
        class="rp-tab"
        :class="{ 'rp-tab--active': tab === t.id }"
        type="button"
        role="tab"
        :aria-selected="tab === t.id"
        @click="tab = t.id"
      >
        {{ t.label }}
        <span v-if="t.id === 'commits'" class="rp-tab-n">{{ commits.length }}</span>
      </button>
    </div>

    <div class="rp-body">
      <!-- 汇报：agent 最终文本（stdout 尾部 64KB），markdown 渲染。 -->
      <div v-if="tab === 'report'">
        <p v-if="report.loading" class="rp-note mono">加载中…</p>
        <p v-else-if="report.error" class="rp-err mono">
          {{ report.error }}
          <button class="rp-retry mono" type="button" @click="retry('report')">重试</button>
        </p>
        <p v-else-if="report.text.trim() === ''" class="rp-note mono">agent 无文本输出</p>
        <MarkdownBlock v-else :text="report.text" />
      </div>

      <!-- 提交：base_sha → HEAD，sha 点击复制。 -->
      <div v-else-if="tab === 'commits'">
        <p v-if="job.base_sha || headSha" class="rp-note mono" :title="`${job.base_sha ?? '?'} → ${headSha || '?'}`">
          基线 {{ shortSha(job.base_sha) }} → HEAD {{ shortSha(headSha) }}
        </p>
        <ul v-if="commits.length > 0" class="rp-commits">
          <li v-for="c in commits" :key="c.sha" class="rp-commit">
            <button
              class="rp-sha mono"
              type="button"
              :title="`复制 ${c.sha}`"
              @click="copySha(c.sha)"
            >
              {{ copiedSha === c.sha ? '已复制' : c.sha }}
            </button>
            <span class="rp-subject" :title="c.subject">{{ c.subject }}</span>
          </li>
        </ul>
        <p v-else class="rp-note mono">无提交（可能只改了工作树，见 Diff）</p>
      </div>

      <!-- Diff：changes.diff 原文就地渲染（UnifiedDiff 解析 + 折叠 + 上限降级）。 -->
      <div v-else-if="tab === 'diff'">
        <p v-if="diff.loading" class="rp-note mono">加载中…</p>
        <p v-else-if="diff.error" class="rp-err mono">
          {{ diff.error }}
          <span class="rp-err-hint">此 job 没有捕获到 diff（无改动、非 git 仓库，或改动全在提交里）</span>
          <button class="rp-retry mono" type="button" @click="retry('diff')">重试</button>
        </p>
        <UnifiedDiff v-else :text="diff.text" :download-name="`changes-${shortSha(job.id)}.diff`" />
      </div>

      <!-- 验证：状态/命令/exit/耗时 + stderr 里最后一段 verify 横幅之间的输出。 -->
      <div v-else-if="tab === 'verify'">
        <p v-if="!verify" class="rp-note mono">本 job 没有验证步骤（提交时未指定 verify）</p>
        <template v-else>
          <div class="rp-verify-head">
            <span class="rp-verify-status mono" :class="verifyTone">{{ verifyText }}</span>
            <span v-if="verify.status !== 'skipped'" class="rp-note mono">
              exit {{ verify.exit_code }} · {{ (verify.duration_ms / 1000).toFixed(1) }}s
            </span>
          </div>
          <pre v-if="verifyCommand" class="rp-pre mono">{{ verifyCommand }}</pre>
          <p v-if="verify.status === 'skipped'" class="rp-note mono">
            未跑验证：{{ verify.reason || 'agent 没正常结束' }}
          </p>
          <template v-else>
            <p v-if="verifyOut.loading" class="rp-note mono">加载中…</p>
            <p v-else-if="verifyOut.error" class="rp-err mono">{{ verifyOut.error }}</p>
            <pre v-else-if="verifyOutput" class="rp-pre rp-pre--out mono">{{ verifyOutput }}</pre>
            <p v-else class="rp-note mono">
              stderr 尾部 64KB 里没有 verify 横幅（输出可能已被更大的日志挤出窗口）
            </p>
          </template>
        </template>
      </div>

      <!-- 用量：tokens 四项 + total + $ + 来源。 -->
      <div v-else>
        <p v-if="!usage" class="rp-note mono">agent 未上报用量</p>
        <template v-else>
          <ul class="rp-usage">
            <li v-for="r in usageRows" :key="r.k" class="rp-usage-row">
              <span class="rp-usage-k mono">{{ r.k }}</span>
              <span class="rp-usage-v mono">{{ r.v }}</span>
            </li>
          </ul>
          <p v-if="usageOneLine" class="rp-note mono">{{ usageOneLine }}</p>
        </template>
      </div>
    </div>

    <div class="rp-actions">
      <template v-if="reviewed">
        <span class="rp-reviewed mono">
          已验收 · {{ job.reviewed_by }} · {{ fmtDateTime(job.reviewed_at) }}
        </span>
        <span class="rp-result"><StatusBadge :status="job.status" /></span>
        <span v-if="job.review_note" class="rp-note mono" :title="job.review_note">
          备注：{{ job.review_note }}
        </span>
      </template>
      <template v-else-if="canDecide">
        <button class="rp-go mono" type="button" :disabled="accepting || rejecting" @click="onAccept">
          {{ accepting ? '处理中…' : 'Accept 通过' }}
        </button>
        <button
          class="rp-reject mono"
          type="button"
          :disabled="accepting || rejecting"
          @click="rejectOpen = true"
        >
          Reject 拒绝…
        </button>
        <span v-if="reviewError" class="rp-err mono">{{ reviewError }}</span>
      </template>
      <span v-else class="rp-note mono">该 job 已终态，但没有验收记录（reviewed_by 为空）</span>
    </div>

    <RejectDialog
      v-if="rejectOpen"
      :submitting="rejecting"
      :error="reviewError"
      @close="rejectOpen = false"
      @confirm="onReject"
    />
  </section>
</template>

<style scoped>
.rp {
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  background: var(--panel);
  margin: 12px 0;
}
.rp-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  padding: 8px 12px;
  border-bottom: 1px solid var(--line);
}
.rp-title {
  color: var(--phosphor);
  font-size: 13px;
  letter-spacing: 0.06em;
}
.rp-hint {
  color: var(--queue);
  font-size: 11px;
}

.rp-tabs {
  display: flex;
  gap: 4px;
  padding: 6px 12px 0;
  border-bottom: 1px solid var(--line);
}
.rp-tab {
  background: transparent;
  color: var(--queue);
  border: 1px solid transparent;
  border-bottom: 2px solid transparent;
  border-radius: var(--radius) var(--radius) 0 0;
  padding: 4px 12px;
  font-size: 12px;
  white-space: nowrap;
}
.rp-tab:hover {
  color: var(--paper);
}
.rp-tab--active {
  color: var(--phosphor);
  border-bottom-color: var(--phosphor);
  background: var(--term-bg);
}
.rp-tab-n {
  color: var(--queue);
  font-size: 11px;
}

.rp-body {
  padding: 10px 12px;
  min-height: 120px;
}
.rp-note {
  color: var(--queue);
  font-size: 12px;
  margin: 4px 0;
}
.rp-err {
  color: var(--fail);
  font-size: 12px;
  margin: 4px 0;
  word-break: break-word;
}
.rp-err-hint {
  display: block;
  color: var(--queue);
  margin-top: 2px;
}
.rp-retry {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 1px 8px;
  font-size: 11px;
  margin-left: 6px;
}

/* 汇报：面板内限高滚动，长汇报不把操作条推远。 */
.rp-body :deep(.md) {
  max-height: 52vh;
  overflow: auto;
}

.rp-commits {
  list-style: none;
  margin: 0;
  padding: 0;
}
.rp-commit {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding: 3px 0;
}
.rp-sha {
  flex: none;
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 0 6px;
  font-size: 11px;
  font-family: var(--font-mono, monospace);
}
.rp-sha:hover {
  border-color: var(--phosphor);
}
.rp-subject {
  color: var(--paper);
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rp-verify-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  flex-wrap: wrap;
}
.rp-verify-status {
  font-size: 13px;
}
/* verify 结论配色（与 JobDetail「验证」块同色）。 */
.rp-verify-status.verify--ok {
  color: var(--done);
}
.rp-verify-status.verify--bad {
  color: var(--fail);
}
.rp-verify-status.verify--skip {
  color: var(--queue);
}

.rp-pre {
  margin: 6px 0;
  padding: 5px 8px;
  background: var(--term-bg);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--paper);
  font-size: 12px;
  white-space: pre;
  overflow: auto;
  max-height: 60vh;
}

.rp-usage {
  list-style: none;
  margin: 0;
  padding: 0;
}
.rp-usage-row {
  display: flex;
  gap: 12px;
  padding: 2px 0;
  font-size: 12px;
}
.rp-usage-k {
  width: 170px;
  flex: none;
  color: var(--queue);
}
.rp-usage-v {
  color: var(--paper);
}

/* 操作条：面板内底部吸底（滚到面板下方时仍可见，方便看完直接裁决）。 */
.rp-actions {
  position: sticky;
  bottom: 0;
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  padding: 8px 12px;
  border-top: 1px solid var(--line);
  background: var(--panel);
}
.rp-go {
  background: var(--phosphor);
  color: var(--ink);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 4px 14px;
  font-size: 12px;
  font-weight: 600;
}
.rp-go:disabled {
  opacity: 0.6;
  cursor: default;
}
.rp-reject {
  background: transparent;
  color: var(--fail);
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 4px 12px;
  font-size: 12px;
}
.rp-reject:hover:not(:disabled) {
  background: var(--fail);
  color: var(--ink);
}
.rp-reviewed {
  color: var(--done);
  font-size: 12px;
}

/* 窄屏（940px 以下）：页签横向可滚，不换行挤成两排。 */
@media (max-width: 940px) {
  .rp-tabs {
    overflow-x: auto;
  }
}
</style>
