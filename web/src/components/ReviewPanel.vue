<script setup lang="ts">
// job 详情验收面板（REV-01 §一.2、设计稿 §7.1 验收页签）：页签（汇报 / 提交 / Diff / 验证 / 用量；
// 有内容时另加「发现」「经验」）+ 底部固定的操作条（Accept / Reject）。
//
// 出现时机由 JobDetail 判定：status ∈ needs_review | rejected，或 done 且 require_review。
// 数据一律走既有接口：汇报 = stdout 尾部 64KB（markdown 渲染），Diff = diff?full=1 原文，
// 验证 = 从 stderr 尾部 64KB 里截最后一段 verify 横幅之间的输出；提交/用量直接用 job 行。
// 页签按需加载（默认停在「汇报」），所以打开一个 needs_review job 只多一次 stdout 请求。
//
// P4：页签换成 P2 的 ui/Tabs（← → Home End、role=tablist/tabpanel、?rtab= 深链——`?tab=`
// 已被任务详情的页签占用，所以面板用独立参数），底部裁决条改成贴底固定（sticky）；
// 文案全部走 i18n 的 review.*。
import { computed, onMounted, ref, watch } from 'vue'
import type { Ref } from 'vue'
import { useRoute } from 'vue-router'
import MarkdownBlock from './MarkdownBlock.vue'
import RejectDialog from './RejectDialog.vue'
import UnifiedDiff from './UnifiedDiff.vue'
import Tabs from '../ui/Tabs.vue'
import StatusPill from '../ui/StatusPill.vue'
import MemoryCandidatesTab from './MemoryCandidatesTab.vue'
import { ApiError, acceptJob, createFindingIssue, fetchDiffText, logsTail, rejectJob } from '../api/client'
import { fmtDateTime } from '../api/time'
import { formatTokens, shortSha, usageLine, verifyClass, verifyLabel } from '../utils/jobOutcome'
import { acceptanceLines } from '../utils/acceptance'
import { diffFiles, outOfScope } from '../utils/scope'
import { findingIssueCommand, parseFindings } from '../utils/findings'
import { copyText } from '../utils/sessionMessaging'
import { deliveryModeLabel } from '../utils/delivery'
import { listMemoryCandidates } from '../api/memoryCandidates'
import type { MemoryCandidate } from '../api/memoryCandidates'
import { fetchReviewHints } from '../api/reviewHints'
import type { ReviewHints } from '../api/reviewHints'
import { hasReviewHints, hotspotLine, removedCommentLine, reviewHintCount } from '../utils/reviewHints'
import { tabFromQuery, type TabItem } from '../ui/tabs'
import { t } from '../i18n'
import type { Job } from '../api/types'

const props = defineProps<{ job: Job; blockedReason?: string }>()
const emit = defineEmits<{
  (e: 'updated', job: Job): void
  (e: 'resumed', jobId: string): void
}>()

// 汇报与 verify 输出都取日志尾部 64KB：一份 agent 汇报/一次 verify 输出远小于它。
const TAIL_BYTES = 65536
// verify 输出最多渲染 200 行（超出只留尾部提示行）。
const MAX_VERIFY_LINES = 200
// 面板页签的 URL 参数：任务详情自己的页签已占用 ?tab=。
const TAB_PARAM = 'rtab'
const TAB_FALLBACK = 'report'

type Tab = 'report' | 'findings' | 'knowledge' | 'commits' | 'diff' | 'verify' | 'usage'

const route = useRoute()

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

function ensureTab(tab: Tab): void {
  if (tab === 'report') {
    void load(report, () => logsTail(props.job.id, 'stdout', TAIL_BYTES))
    void loadHints()
    return
  }
  if (tab === 'diff') {
    void load(diff, () => fetchDiffText(props.job.id))
    return
  }
  // skipped 是"agent 没正常结束所以没跑"，不会写横幅，不必拉 stderr。
  if (tab === 'verify') {
    // 没有 verify 步骤时，这一页展示汇报里「验证」标题下的代码块（agent 自述，仅参考），
    // 数据来自审阅提示接口，所以与「汇报」页签一样先拉 hints。
    void loadHints()
    if (verify.value && verify.value.status !== 'skipped') {
      void load(verifyOut, () => logsTail(props.job.id, 'stderr', TAIL_BYTES))
    }
  }
}

// 失败后重来：清掉 loaded 再走一次 ensureTab（load 只在 loaded 时短路）。
function retry(tab: Tab): void {
  const res = tab === 'report' ? report : tab === 'diff' ? diff : verifyOut
  res.value = { ...res.value, loaded: false }
  ensureTab(tab)
}

// 验收标准（gofer-3nxa.4）：列表项带本地勾选框（只是人工逐条对照用的前端状态，不持久化；
// 换 job 即清空），非列表段落走 markdown 渲染。
const acceptance = computed(() => acceptanceLines(props.job.acceptance))
const acceptanceItems = computed(() => acceptance.value.filter((l) => l.kind === 'item').length)
const acceptanceChecked = ref<Record<number, boolean>>({})
const acceptanceDone = computed(() => Object.values(acceptanceChecked.value).filter(Boolean).length)

// 审阅提示（gofer-hjj1）：服务端从 changes.diff 与 stdout 尾部算出的三块建议，只在「汇报」页签
// 顶部折叠展示；拉不到就不显示卡片（不影响汇报本身）。
const hints = ref<ReviewHints | null>(null)
const hintsJobID = ref('')

async function loadHints(): Promise<void> {
  const id = props.job.id
  if (hintsJobID.value === id) {
    return
  }
  hintsJobID.value = id
  try {
    const got = await fetchReviewHints(id)
    if (props.job.id === id) {
      hints.value = got
    }
  } catch {
    // 提示是锦上添花：接口失败就不显示卡片。
    hints.value = null
  }
}

// 「发现」页签（gofer-3nxa.3）：汇报里「## 发现但不碰」小节的列表项；有发现才出现（带计数）。
// 每条可复制成在当前仓库建 issue 的命令（服务端暂无建 tracker issue 的写接口）。
const findings = computed(() => (report.value.loaded ? parseFindings(report.value.text) : []))
// 「经验」页签（gofer-3nxa.2）：服务端从汇报「## 可复用经验」记下的候选；有候选才出现，计数 = 待处理数。
const candidates = ref<MemoryCandidate[]>([])
const pendingCandidates = computed(() => candidates.value.filter((c) => c.status === 'pending').length)

async function loadCandidates(): Promise<void> {
  const id = props.job.id
  try {
    const rows = await listMemoryCandidates({ job_id: id, status: 'all' })
    if (props.job.id === id) {
      candidates.value = rows
    }
  } catch {
    // 拉不到候选不影响验收：页签不出现即可。
  }
}

const commits = computed(() => props.job.commits ?? [])

// 页签顺序与计数：发现 / 经验夹在「汇报」与「提交」之间（有内容才出现）。
const tabItems = computed<TabItem[]>(() => {
  const extra: TabItem[] = []
  if (findings.value.length > 0) {
    extra.push({ value: 'findings', label: t('review.tab.findings'), count: findings.value.length })
  }
  if (candidates.value.length > 0) {
    extra.push({ value: 'knowledge', label: t('review.tab.knowledge'), count: pendingCandidates.value })
  }
  return [
    { value: 'report', label: t('review.tab.report') },
    ...extra,
    { value: 'commits', label: t('review.tab.commits'), count: commits.value.length },
    { value: 'diff', label: t('review.tab.diff') },
    { value: 'verify', label: t('review.tab.verify') },
    { value: 'usage', label: t('review.tab.usage') },
  ]
})

// 当前页签以 URL 为准（?rtab=）；认不出来的值回落到「汇报」。
const activeTab = computed<string>(() =>
  tabFromQuery(route.query, tabItems.value, TAB_PARAM, TAB_FALLBACK),
)

const copiedFinding = ref(-1)
// 剪贴板全不可用时，把命令就地展开供手动选中复制。
const manualFinding = ref(-1)

// 非安全上下文没有 navigator.clipboard：退回 textarea + execCommand('copy')。
function legacyCopy(text: string): boolean {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  document.body.removeChild(ta)
  return ok
}

async function copyFinding(i: number, text: string): Promise<void> {
  const cmd = findingIssueCommand(text, props.job.id)
  const ok = (await copyText(cmd)) || legacyCopy(cmd)
  if (!ok) {
    manualFinding.value = i
    return
  }
  manualFinding.value = -1
  copiedFinding.value = i
  window.setTimeout(() => {
    if (copiedFinding.value === i) {
      copiedFinding.value = -1
    }
  }, 1500)
}

// 发现一键建 issue：每条的结果（已建的 issue id，或失败原因）。失败时同时展开 CLI 命令。
const findingIssue = ref<Record<number, { id?: string; error?: string; busy?: boolean }>>({})

async function fileFinding(i: number, text: string): Promise<void> {
  findingIssue.value = { ...findingIssue.value, [i]: { busy: true } }
  try {
    const r = await createFindingIssue(props.job.id, text)
    findingIssue.value = { ...findingIssue.value, [i]: { id: r.issue.id } }
  } catch (e) {
    const msg = e instanceof ApiError ? e.detail || e.message : String(e)
    findingIssue.value = { ...findingIssue.value, [i]: { error: msg } }
    manualFinding.value = i
  }
}

// 声明范围（gofer-3nxa.3）：Diff 加载后用文件列表比对 scope，越界文件列出并在文件头标「范围外」。
const scope = computed(() => props.job.scope ?? [])
const outsideScope = computed(() => (diff.value.loaded ? outOfScope(diffFiles(diff.value.text), scope.value) : []))

const verify = computed(() => props.job.verify ?? null)
const usage = computed(() => props.job.usage ?? null)

// 提交页签头部：base_sha → HEAD（HEAD 取最新提交，worktree job 回落到 worktree_head_sha）。
const headSha = computed(() => commits.value[0]?.sha ?? props.job.worktree_head_sha ?? '')

// verify 输出：stderr 里最后一段 `===== gofer verify: <argv> =====` 到它的
// `===== gofer verify: exit=… dur=… =====` 之间（含两端横幅），最多 200 行——超出留**尾部**
// （失败时的报错与退出码在末尾，开头只是命令行）。
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
  const kept = block.length - MAX_VERIFY_LINES
  return [t('review.verify.omitted', { kept, total: block.length }), ...block.slice(kept)].join('\n')
})

// 没有 verify 步骤时这一页的参考输出：汇报里「验证」标题下的代码块，来源是 agent 自述。
const reportVerify = computed(() => hints.value?.report_verify ?? [])

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
    { k: 'tool_calls', v: (u?.tool_calls ?? 0) > 0 ? String(u?.tool_calls) : '—' },
    { k: 'source', v: u?.source || '—' },
  ]
})

const usageOneLine = computed(() => usageLine(usage.value))
const verifyText = computed(() => verifyLabel(verify.value))
const verifyStatusClass = computed(() => verifyClass(verify.value))
const verifyCommand = computed(() => (verify.value?.command ?? []).join(' '))

// 裁决：accept 直接通过；reject 必写理由（可选自动续投 → 跳新 job）。
const reviewed = computed(() => !!props.job.reviewed_by)
// blockedReason 非空 = 调用方不能裁决（只读身份 / 没 job.review.others / 受限身份审自己的 job）：
// 验收材料照常展示，底部裁决改一行灰字说明原因。
const canDecide = computed(() => props.job.status === 'needs_review' && !props.blockedReason)
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

// GIT-02：worktree job 通过后会按 delivery_mode 交付；可勾选「本次不交付」。
const acceptDeliveryMode = computed(() => {
  const m = (props.job as { delivery_mode?: string }).delivery_mode ?? ''
  return props.job.worktree_path && m && m !== 'none' ? m : ''
})
const skipDelivery = ref(false)
const acceptLabel = computed(() => {
  const m = acceptDeliveryMode.value
  if (!m || skipDelivery.value) return t('review.act.acceptPlain')
  return t('review.act.acceptDeliver', { label: deliveryModeLabel(m) })
})

async function onAccept(): Promise<void> {
  if (accepting.value) {
    return
  }
  accepting.value = true
  reviewError.value = ''
  try {
    emit('updated', await acceptJob(props.job.id, undefined, skipDelivery.value))
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

// 换 job（详情页内跳转）时重置已加载的文本（页签留在 URL 里那个）。
watch(
  () => props.job.id,
  () => {
    report.value = newResource()
    diff.value = newResource()
    verifyOut.value = newResource()
    reviewError.value = ''
    rejectOpen.value = false
    acceptanceChecked.value = {}
    copiedFinding.value = -1
    manualFinding.value = -1
    candidates.value = []
    hintsJobID.value = ''
    bootstrap()
  },
)

// 页签切换（点 / 键盘 / 浏览器前进后退）才去拉那一页需要的数据。
watch(activeTab, (value) => ensureTab(value as Tab))

// 汇报总是先拉：它是首页签，也是「发现 / 经验」页签与审阅提示的数据来源——
// 深链到 Diff / 验证时如果只按当前页签加载，那两个页签会一直不出现。
function bootstrap(): void {
  ensureTab('report')
  ensureTab(activeTab.value as Tab)
  void loadCandidates()
}

onMounted(() => {
  bootstrap()
})
</script>

<template>
  <section class="rp">
    <div class="rp-head">
      <span class="rp-title">{{ t('review.panel.title') }}</span>
      <StatusPill :status="job.status" />
      <span class="rp-hint">
        <template v-if="canDecide">{{ t('review.panel.hintDecide') }}</template>
        <template v-else-if="blockedReason">{{ t('review.panel.hintBlocked') }}</template>
        <template v-else>{{ t('review.panel.hintReviewed') }}</template>
      </span>
    </div>

    <div v-if="acceptance.length > 0" class="rp-acc">
      <div class="rp-acc-head">
        {{ t('review.panel.acceptance') }}
        <span v-if="acceptanceItems > 0" class="rp-n">{{ acceptanceDone }}/{{ acceptanceItems }}</span>
        <span class="rp-acc-hint">{{ t('review.panel.acceptanceHint') }}</span>
      </div>
      <template v-for="(l, i) in acceptance" :key="i">
        <label v-if="l.kind === 'item'" class="rp-acc-item">
          <input v-model="acceptanceChecked[i]" type="checkbox" />
          <span :class="{ 'rp-acc-done': acceptanceChecked[i] }">{{ l.text }}</span>
        </label>
        <MarkdownBlock v-else :text="l.text" />
      </template>
    </div>

    <!-- 页签（P2 的 ui/Tabs）：?rtab= 深链、← → Home End 可切换。 -->
    <Tabs :tabs="tabItems" :param="TAB_PARAM" :fallback="TAB_FALLBACK" :label="t('review.panel.tabs')">
      <template #default="{ active }">
        <div class="rp-body">
          <!-- 汇报：agent 最终文本（stdout 尾部 64KB），markdown 渲染。 -->
          <div v-if="active === 'report'">
            <!-- 审阅提示（gofer-hjj1）：先看哪里可疑，再读汇报；默认折叠，纯只读。 -->
            <details v-if="hasReviewHints(hints)" class="rp-hints">
              <summary>{{ t('review.hints.summary', { n: reviewHintCount(hints) }) }}</summary>
              <div v-if="hints?.deletion_hotspots?.length" class="rp-hint-section">
                <h4>{{ t('review.hints.hotspots') }}</h4>
                <ul>
                  <li v-for="d in hints.deletion_hotspots" :key="d.path" class="mono">{{ hotspotLine(d) }}</li>
                </ul>
              </div>
              <div v-if="hints?.removed_comments?.length" class="rp-hint-section">
                <h4>{{ t('review.hints.removedComments') }}</h4>
                <ul>
                  <li v-for="(rc, i) in hints.removed_comments" :key="i" class="mono">{{ removedCommentLine(rc) }}</li>
                </ul>
              </div>
              <div v-if="hints?.verify_lines?.length" class="rp-hint-section">
                <h4>{{ t('review.hints.verifyLines') }}</h4>
                <ul>
                  <li v-for="(line, i) in hints.verify_lines" :key="i" class="mono">{{ line }}</li>
                </ul>
              </div>
            </details>
            <p v-if="report.loading" class="rp-note">{{ t('review.note.loading') }}</p>
            <p v-else-if="report.error" class="rp-err">
              {{ report.error }}
              <button class="rp-retry" type="button" @click="retry('report')">{{ t('review.act.retry') }}</button>
            </p>
            <p v-else-if="report.text.trim() === ''" class="rp-note">{{ t('review.note.noReport') }}</p>
            <MarkdownBlock v-else :text="report.text" />
          </div>

          <!-- 发现：汇报「## 发现但不碰」小节逐条列出，可复制成建 issue 的命令。 -->
          <div v-else-if="active === 'findings'">
            <p class="rp-note">{{ t('review.findings.hint') }}</p>
            <ul class="rp-findings">
              <li v-for="(f, i) in findings" :key="i" class="rp-finding">
                <span class="rp-finding-text">{{ f }}</span>
                <span v-if="findingIssue[i]?.id" class="rp-finding-done">
                  {{ t('review.findings.filed', { id: findingIssue[i]?.id as string }) }}
                </span>
                <button
                  v-else
                  class="rp-retry"
                  type="button"
                  :disabled="findingIssue[i]?.busy"
                  @click="fileFinding(i, f)"
                >
                  {{ findingIssue[i]?.busy ? t('review.findings.filing') : t('review.findings.file') }}
                </button>
                <button class="rp-retry" type="button" @click="copyFinding(i, f)">
                  {{ copiedFinding === i ? t('review.findings.copied') : t('review.findings.copy') }}
                </button>
                <span v-if="findingIssue[i]?.error" class="rp-finding-err">{{ findingIssue[i]?.error }}</span>
                <pre v-if="manualFinding === i" class="rp-pre mono">{{ findingIssueCommand(f, job.id) }}</pre>
              </li>
            </ul>
          </div>

          <!-- 经验：汇报「## 可复用经验」的候选，逐条接受（写成记忆）/ 拒绝。 -->
          <MemoryCandidatesTab v-else-if="active === 'knowledge'" :items="candidates" @changed="loadCandidates" />

          <!-- 提交：base_sha → HEAD，sha 点击复制。 -->
          <div v-else-if="active === 'commits'">
            <p
              v-if="job.base_sha || headSha"
              class="rp-note"
              :title="`${job.base_sha ?? '?'} → ${headSha || '?'}`"
            >
              {{ t('review.commits.baseline', { base: shortSha(job.base_sha), head: shortSha(headSha) }) }}
            </p>
            <ul v-if="commits.length > 0" class="rp-commits">
              <li v-for="c in commits" :key="c.sha" class="rp-commit">
                <button
                  class="rp-sha mono"
                  type="button"
                  :title="t('review.commits.copyTitle', { sha: c.sha })"
                  @click="copySha(c.sha)"
                >
                  {{ copiedSha === c.sha ? t('review.commits.copied') : c.sha }}
                </button>
                <span class="rp-subject" :title="c.subject">{{ c.subject }}</span>
              </li>
            </ul>
            <p v-else class="rp-note">{{ t('review.commits.empty') }}</p>
          </div>

          <!-- Diff：changes.diff 原文就地渲染（UnifiedDiff 解析 + 折叠 + 上限降级）。 -->
          <div v-else-if="active === 'diff'">
            <p v-if="diff.loading" class="rp-note">{{ t('review.note.loading') }}</p>
            <p v-else-if="diff.error" class="rp-err">
              {{ diff.error }}
              <span class="rp-err-hint">{{ t('review.diff.noDiffHint') }}</span>
              <button class="rp-retry" type="button" @click="retry('diff')">{{ t('review.act.retry') }}</button>
            </p>
            <template v-else>
              <p v-if="scope.length > 0" class="rp-note" :title="scope.join('\n')">
                {{ t('review.diff.scope', { paths: scope.join(', ') })
                }}<template v-if="outsideScope.length === 0">{{ t('review.diff.noOutside') }}</template>
              </p>
              <p v-if="outsideScope.length > 0" class="rp-scope-out">
                {{
                  t('review.diff.outside', { n: outsideScope.length, paths: outsideScope.join(', ') })
                }}
              </p>
              <UnifiedDiff
                :text="diff.text"
                :download-name="`changes-${shortSha(job.id)}.diff`"
                :flagged-paths="outsideScope"
              />
            </template>
          </div>

          <!-- 验证：状态/命令/exit/耗时 + stderr 里最后一段 verify 横幅之间的输出（超出留尾部）。
               没有 verify 步骤的 job 展示汇报里「验证」标题下的代码块，明确标为 agent 自述。 -->
          <div v-else-if="active === 'verify'">
            <template v-if="!verify">
              <p class="rp-note">{{ t('review.verify.none') }}</p>
              <div v-if="reportVerify.length > 0" class="rp-report-verify">
                <p class="rp-note">
                  {{ t('review.verify.reportHint', { source: t('review.verify.reportSource') }) }}
                </p>
                <div v-for="(b, i) in reportVerify" :key="i" class="rp-report-verify-block">
                  <div class="rp-report-verify-head">
                    {{
                      t('review.verify.reportHeading', {
                        heading: b.heading,
                        source: b.source === 'agent' ? t('review.verify.fromAgent') : b.source,
                      })
                    }}
                  </div>
                  <pre class="rp-pre rp-pre--out mono">{{ b.text }}</pre>
                </div>
              </div>
            </template>
            <template v-else>
              <div class="rp-verify-head">
                <span class="rp-verify-status" :class="verifyStatusClass">{{ verifyText }}</span>
                <span v-if="verify.status !== 'skipped'" class="rp-note">
                  {{
                    t('review.verify.exit', {
                      code: verify.exit_code,
                      dur: (verify.duration_ms / 1000).toFixed(1),
                    })
                  }}
                </span>
              </div>
              <pre v-if="verifyCommand" class="rp-pre mono">{{ verifyCommand }}</pre>
              <p v-if="verify.status === 'skipped'" class="rp-note">
                {{ t('review.verify.skipped', { reason: verify.reason || t('review.verify.defaultReason') }) }}
              </p>
              <template v-else>
                <p v-if="verifyOut.loading" class="rp-note">{{ t('review.note.loading') }}</p>
                <p v-else-if="verifyOut.error" class="rp-err">{{ verifyOut.error }}</p>
                <pre v-else-if="verifyOutput" class="rp-pre rp-pre--out mono">{{ verifyOutput }}</pre>
                <p v-else class="rp-note">{{ t('review.verify.noBanner') }}</p>
              </template>
            </template>
          </div>

          <!-- 用量：tokens 四项 + total + $ + 来源。 -->
          <div v-else>
            <p v-if="!usage" class="rp-note">{{ t('review.usage.none') }}</p>
            <template v-else>
              <ul class="rp-usage">
                <li v-for="r in usageRows" :key="r.k" class="rp-usage-row">
                  <span class="rp-usage-k mono">{{ r.k }}</span>
                  <span class="rp-usage-v mono">{{ r.v }}</span>
                </li>
              </ul>
              <p v-if="usageOneLine" class="rp-note">{{ usageOneLine }}</p>
            </template>
          </div>
        </div>
      </template>
    </Tabs>

    <!-- 底部裁决条：贴底固定，长汇报不会把「通过 / 拒绝」推远（设计稿 §7.1 验收）。 -->
    <div class="rp-actions">
      <template v-if="reviewed">
        <span class="rp-reviewed">
          {{
            t('review.reviewed.line', {
              by: job.reviewed_by as string,
              at: fmtDateTime(job.reviewed_at),
            })
          }}
        </span>
        <span class="rp-result"><StatusPill :status="job.status" /></span>
        <span v-if="job.review_note" class="rp-note" :title="job.review_note">
          {{ t('review.reviewed.note', { note: job.review_note }) }}
        </span>
      </template>
      <template v-else-if="canDecide">
        <button class="rp-go" type="button" :disabled="accepting || rejecting" @click="onAccept">
          {{ accepting ? t('review.act.processing') : acceptLabel }}
        </button>
        <label v-if="acceptDeliveryMode" class="rp-nodeliver">
          <input v-model="skipDelivery" type="checkbox" /> {{ t('review.act.skipDelivery') }}
        </label>
        <button class="rp-reject" type="button" :disabled="accepting || rejecting" @click="rejectOpen = true">
          {{ t('review.act.rejectOpen') }}
        </button>
        <span v-if="reviewError" class="rp-err">{{ reviewError }}</span>
      </template>
      <span v-else-if="blockedReason" class="rp-note" data-test="rp-blocked">{{ blockedReason }}</span>
      <span v-else class="rp-note">{{ t('review.reviewed.noRecord') }}</span>
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
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  background: var(--surface);
  margin: var(--s3) 0;
}
.rp-head {
  display: flex;
  align-items: center;
  gap: var(--s2);
  flex-wrap: wrap;
  padding: var(--s2) var(--s3);
  border-bottom: 1px solid var(--line);
}
/* 「验收」是面板的名字：需要人拍板，按 §3.1 用万寿菊黄。 */
.rp-title {
  color: var(--need-ink);
  font-size: var(--fs-sm);
  font-weight: 600;
}
.rp-hint {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}

.rp-acc {
  padding: var(--s2) var(--s3);
  border-bottom: 1px solid var(--line);
}
.rp-acc-head {
  display: flex;
  align-items: baseline;
  gap: var(--s2);
  color: var(--ink-2);
  font-size: var(--fs-sm);
  margin-bottom: var(--s1);
}
.rp-acc-hint {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.rp-acc-item {
  display: flex;
  align-items: baseline;
  gap: var(--s2);
  padding: 2px 0;
  color: var(--ink);
  font-size: var(--fs-md);
  cursor: pointer;
}
.rp-acc-done {
  color: var(--ink-3);
  text-decoration: line-through;
}

.rp-body {
  padding: var(--s3);
  min-height: 120px;
}
.rp-n {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.rp-note {
  color: var(--ink-2);
  font-size: var(--fs-sm);
  margin: var(--s1) 0;
}
.rp-err {
  color: var(--fail);
  font-size: var(--fs-sm);
  margin: var(--s1) 0;
  word-break: break-word;
}
.rp-scope-out {
  color: var(--need-ink);
  font-size: var(--fs-sm);
  margin: var(--s1) 0;
  word-break: break-word;
}
.rp-err-hint {
  display: block;
  color: var(--ink-3);
  margin-top: 2px;
}
.rp-retry {
  background: transparent;
  color: var(--accent);
  border: 1px solid var(--accent);
  border-radius: var(--r-ctl);
  padding: 1px var(--s2);
  font: inherit;
  font-size: var(--fs-xs);
  margin-left: var(--s1);
  cursor: pointer;
}
.rp-retry:disabled {
  opacity: 0.6;
  cursor: default;
}

/* 汇报：面板内限高滚动，长汇报不把操作条推远。 */
.rp-body :deep(.md) {
  max-height: 52vh;
  overflow: auto;
}

/* 审阅提示：折叠卡片，凹陷底 + 细边框。 */
.rp-hints {
  margin-bottom: var(--s3);
  padding: var(--s2) var(--s3);
  background: var(--sunken);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
}
.rp-hints summary {
  color: var(--ink-2);
  font-size: var(--fs-sm);
  cursor: pointer;
}
.rp-hint-section h4 {
  margin: var(--s2) 0 var(--s1);
  color: var(--ink-3);
  font-size: var(--fs-xs);
  font-weight: 600;
}
.rp-hint-section ul {
  margin: 0;
  padding-left: var(--s4);
}
.rp-hint-section li {
  color: var(--ink-2);
  font-size: var(--fs-xs);
}

.rp-findings {
  list-style: none;
  margin: 0;
  padding: 0;
}
.rp-finding {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: var(--s2);
  padding: var(--s1) 0;
  border-bottom: 1px dashed var(--line);
}
.rp-finding-done {
  color: var(--done);
  font-size: var(--fs-sm);
}
.rp-finding-err {
  flex-basis: 100%;
  color: var(--fail);
  font-size: var(--fs-sm);
}
.rp-finding-text {
  flex: 1 1 320px;
  color: var(--ink);
  font-size: var(--fs-md);
  word-break: break-word;
}
.rp-finding .rp-pre {
  flex-basis: 100%;
  white-space: pre-wrap;
}

.rp-commits {
  list-style: none;
  margin: 0;
  padding: 0;
}
.rp-commit {
  display: flex;
  align-items: baseline;
  gap: var(--s3);
  padding: 3px 0;
}
.rp-sha {
  flex: none;
  background: var(--surface-2);
  color: var(--accent);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 0 var(--s2);
  font-size: var(--fs-xs);
  cursor: pointer;
}
.rp-subject {
  color: var(--ink);
  font-size: var(--fs-sm);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rp-verify-head {
  display: flex;
  align-items: baseline;
  gap: var(--s2);
  flex-wrap: wrap;
  margin-bottom: var(--s1);
}
.rp-verify-status {
  font-size: var(--fs-sm);
  font-weight: 600;
}
.rp-verify-status.verify--ok {
  color: var(--done);
}
.rp-verify-status.verify--bad {
  color: var(--fail);
}
.rp-verify-status.verify--skip {
  color: var(--idle);
}
.rp-report-verify-block {
  margin: var(--s2) 0;
}
.rp-report-verify-head {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  margin-bottom: var(--s1);
}

/* 输出块：日志底（两种主题下都是深底），限高滚动。 */
.rp-pre {
  margin: var(--s1) 0;
  padding: var(--s2) var(--s3);
  background: var(--log-bg);
  color: var(--log-ink);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  font-family: var(--font-mono);
  font-size: var(--fs-sm);
  overflow: auto;
}
.rp-pre--out {
  max-height: 54vh;
}

.rp-usage {
  list-style: none;
  margin: 0;
  padding: 0;
}
.rp-usage-row {
  display: flex;
  align-items: baseline;
  gap: var(--s3);
  padding: 3px 0;
  border-bottom: 1px solid var(--line);
}
.rp-usage-row:last-child {
  border-bottom: none;
}
.rp-usage-k {
  min-width: 160px;
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.rp-usage-v {
  color: var(--ink);
  font-size: var(--fs-sm);
}

/* 裁决条：贴底固定，滚到面板任何位置都能直接拍板（设计稿 §7.1）。 */
.rp-actions {
  position: sticky;
  bottom: 0;
  z-index: 5;
  display: flex;
  align-items: center;
  gap: var(--s2);
  flex-wrap: wrap;
  padding: var(--s2) var(--s3);
  border-top: 1px solid var(--line);
  border-radius: 0 0 var(--r-panel) var(--r-panel);
  background: var(--surface);
}
.rp-go {
  min-height: 34px;
  padding: 0 var(--s3);
  background: var(--accent);
  color: var(--accent-ink);
  border: 1px solid var(--accent);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  font-weight: 600;
  cursor: pointer;
}
.rp-go:hover:not(:disabled) {
  background: var(--accent-hover);
  border-color: var(--accent-hover);
}
.rp-go:focus-visible,
.rp-reject:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.rp-go:disabled {
  opacity: 0.6;
  cursor: default;
}
.rp-reject {
  min-height: 34px;
  padding: 0 var(--s3);
  background: transparent;
  color: var(--fail);
  border: 1px solid var(--fail);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.rp-reject:hover:not(:disabled) {
  background: var(--fail-soft);
}
.rp-reject:disabled {
  opacity: 0.6;
  cursor: default;
}
.rp-nodeliver {
  font-size: var(--fs-sm);
  color: var(--ink-3);
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
}
.rp-reviewed {
  color: var(--ink-2);
  font-size: var(--fs-sm);
}

/* 手机（≤760）：裁决按钮铺满一行，拇指够得到；输出块少留高度给内容。 */
@media (max-width: 760px) {
  .rp-head {
    padding: var(--s2);
  }
  .rp-body {
    padding: var(--s2);
  }
  .rp-actions {
    padding: var(--s2);
  }
  .rp-go,
  .rp-reject {
    flex: 1 1 0;
    min-height: 40px;
  }
  .rp-usage-k {
    min-width: 120px;
  }
  .rp-pre--out {
    max-height: 44vh;
  }
  .rp-body :deep(.md) {
    max-height: 44vh;
  }
}
</style>
