<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { acceptJob } from '../../api/client'
import type { WorkbenchThreadDiff } from '../../api/types'
import {
  getWorkbenchThreadDiff,
  patchWorkbenchThread,
  reviewWorkbenchThread,
} from '../../api/workbench'
import UnifiedDiff from '../UnifiedDiff.vue'
import { t } from '../../i18n'
import { classifyDiffFailure } from '../../utils/workbenchDiff'
import {
  REVIEW_DRAFT_STORAGE_KEY,
  addReviewComment,
  buildReviewRequest,
  clearReviewDraft,
  createReviewDraftState,
  normalizeReviewDraftState,
  removeReviewComment,
  reviewDraftFor,
  setReviewSummary,
  updateReviewComment,
  type ReviewDraftState,
  type ReviewDraftSide,
} from './reviewDrafts'

const props = defineProps<{
  threadId: string
  latestJobId: string
  rawStatus: string
  working: boolean
  canReview: boolean
}>()

const emit = defineEmits<{
  (event: 'continued', jobId?: string): void
  (event: 'changed'): void
  (event: 'error', message: string): void
}>()

const diff = ref<WorkbenchThreadDiff | null>(null)
const loading = ref(false)
const sending = ref(false)
const accepting = ref(false)
const error = ref('')
// unavailable：没有 git 基线（或会话不可续接）时后端 409 的原因，在页签里当说明显示
// （gofer-yb5x），不进页面的错误行、也不当加载失败。
const unavailable = ref('')
const locationNotice = ref('')
const selectedPath = ref('')
const drafts = ref<ReviewDraftState>(readStoredDrafts())
const diffRenderer = ref<{ focusFile(path: string): Promise<boolean> } | null>(null)
let refreshPromise: Promise<void> | null = null
let refreshQueued = false
let pollTimer: number | null = null
let commentSequence = 0

const draft = computed(() => reviewDraftFor(drafts.value, props.threadId))
const reviewBody = computed(() => buildReviewRequest(drafts.value, props.threadId))
const hasReviewContent = computed(() => (
  reviewBody.value.summary !== '' || reviewBody.value.comments.length > 0
))
const reviewValid = computed(() => (
  hasReviewContent.value
  && reviewBody.value.comments.length <= 50
  && reviewBody.value.comments.every((comment) => comment.text.length > 0 && [...comment.text].length <= 4000)
))

function readStoredDrafts(fallback: ReviewDraftState = createReviewDraftState()): ReviewDraftState {
  try {
    const raw = window.localStorage.getItem(REVIEW_DRAFT_STORAGE_KEY)
    return raw ? normalizeReviewDraftState(JSON.parse(raw)) : createReviewDraftState()
  } catch {
    return fallback
  }
}

function persistDrafts(next: ReviewDraftState): void {
  drafts.value = next
  try {
    if (Object.keys(next).length === 0) window.localStorage.removeItem(REVIEW_DRAFT_STORAGE_KEY)
    else window.localStorage.setItem(REVIEW_DRAFT_STORAGE_KEY, JSON.stringify(next))
  } catch {
    // Browser storage is a convenience only; in-memory drafts remain usable.
  }
}

function mutateDrafts(mutator: (state: ReviewDraftState) => ReviewDraftState): void {
  persistDrafts(mutator(readStoredDrafts(drafts.value)))
}

function onStorage(event: StorageEvent): void {
  if (event.key === REVIEW_DRAFT_STORAGE_KEY) drafts.value = readStoredDrafts()
}

function setSummary(value: string): void {
  mutateDrafts((state) => setReviewSummary(state, props.threadId, value))
}

function addComment(location: { path: string; line: number; side: ReviewDraftSide }): void {
  if (draft.value.comments.length >= 50) {
    locationNotice.value = t('workbench.changes.commentLimit')
    return
  }
  commentSequence++
  const id = typeof window.crypto?.randomUUID === 'function'
    ? window.crypto.randomUUID()
    : `${Date.now()}-${commentSequence}`
  mutateDrafts((state) => addReviewComment(state, props.threadId, { id, ...location, text: '' }))
}

function updateComment(id: string, text: string): void {
  mutateDrafts((state) => updateReviewComment(state, props.threadId, id, text))
}

function removeComment(id: string): void {
  mutateDrafts((state) => removeReviewComment(state, props.threadId, id))
}

async function performRefresh(): Promise<void> {
  do {
    refreshQueued = false
    loading.value = true
    try {
      diff.value = await getWorkbenchThreadDiff(props.threadId)
      unavailable.value = ''
      error.value = ''
      if (selectedPath.value) await scrollToFile(selectedPath.value)
    } catch (cause) {
      const failure = classifyDiffFailure(cause)
      if (failure.unavailable) {
        // 409：页签内说明原因（"这个会话没有 git 改动基线"），不冒泡成页面错误。
        diff.value = null
        unavailable.value = failure.reason
        error.value = ''
      } else {
        error.value = failure.reason
        emit('error', t('workbench.changes.loadFailed', { err: failure.reason }))
      }
    } finally {
      loading.value = false
    }
  } while (refreshQueued)
}

function refresh(): Promise<void> {
  if (refreshPromise) {
    refreshQueued = true
    return refreshPromise
  }
  refreshPromise = performRefresh().finally(() => { refreshPromise = null })
  return refreshPromise
}

async function scrollToFile(path: string): Promise<boolean> {
  if (unavailable.value) return false
  await nextTick()
  const focused = await diffRenderer.value?.focusFile(path)
  if (!focused) locationNotice.value = t('workbench.changes.noChange')
  else locationNotice.value = ''
  return focused ?? false
}

async function focusFile(path: string): Promise<boolean> {
  locationNotice.value = ''
  if (!diff.value) await refresh()
  const normalized = path.replace(/\\/g, '/').replace(/^\.\//, '')
  const exact = diff.value?.files.find((file) => file.path.replace(/\\/g, '/') === normalized)
  const suffixMatches = diff.value?.files.filter((file) => normalized.endsWith(`/${file.path.replace(/\\/g, '/')}`)) ?? []
  const target = exact?.path ?? (suffixMatches.length === 1 ? suffixMatches[0].path : '')
  selectedPath.value = target || path
  if (!target) {
    locationNotice.value = t('workbench.changes.noChange')
    return false
  }
  return scrollToFile(target)
}

async function sendReview(): Promise<void> {
  if (!props.canReview || !reviewValid.value || sending.value) return
  const count = reviewBody.value.comments.length
  if (!window.confirm(t('workbench.changes.sendConfirm', { n: count }))) return
  sending.value = true
  error.value = ''
  try {
    const result = await reviewWorkbenchThread(props.threadId, reviewBody.value)
    mutateDrafts((state) => clearReviewDraft(state, props.threadId))
    emit('continued', result.job_id)
  } catch (cause) {
    const message = cause instanceof Error ? cause.message : String(cause)
    error.value = message
    emit('error', t('workbench.changes.sendFailed', { err: message }))
  } finally {
    sending.value = false
  }
}

async function acceptChanges(): Promise<void> {
  if (accepting.value) return
  accepting.value = true
  error.value = ''
  try {
    if (props.rawStatus === 'needs_review') {
      await acceptJob(props.latestJobId)
    } else {
      await patchWorkbenchThread(props.threadId, { seen: true })
    }
    emit('changed')
  } catch (cause) {
    const message = cause instanceof Error ? cause.message : String(cause)
    error.value = message
    emit('error', t('workbench.changes.acceptFailed', { err: message }))
  } finally {
    accepting.value = false
  }
}

function startPolling(): void {
  if (pollTimer != null) window.clearInterval(pollTimer)
  pollTimer = null
  // 页面不可见时不刷新（回到前台由 onVisible 立即补一次）。
  if (props.working) pollTimer = window.setInterval(() => { if (!document.hidden) void refresh() }, 10_000)
}

watch(() => props.threadId, () => {
  diff.value = null
  error.value = ''
  unavailable.value = ''
  locationNotice.value = ''
  selectedPath.value = ''
  void refresh()
})
watch(() => props.working, startPolling)

function onVisible(): void {
  if (!document.hidden && props.working) void refresh()
}

onMounted(() => {
  window.addEventListener('storage', onStorage)
  document.addEventListener('visibilitychange', onVisible)
  startPolling()
  void refresh()
})
onUnmounted(() => {
  window.removeEventListener('storage', onStorage)
  document.removeEventListener('visibilitychange', onVisible)
  if (pollTimer != null) window.clearInterval(pollTimer)
})

defineExpose({ refresh, focusFile })
</script>

<template>
  <div class="changes-view">
    <aside class="changes-files">
      <div class="changes-files-head mono">
        <span>{{ t('workbench.changes.fileCount', { n: diff?.files.length ?? 0 }) }}</span>
        <button type="button" :disabled="loading" @click="refresh">{{ loading ? t('workbench.changes.refreshing') : t('workbench.changes.refresh') }}</button>
      </div>
      <p v-if="diff?.source === 'captured'" class="captured-note mono">{{ diff.notice || t('workbench.changes.capturedOnly') }}</p>
      <p v-if="diff?.truncated" class="captured-note mono">{{ t('workbench.changes.truncated') }}</p>
      <button
        v-for="file in diff?.files ?? []"
        :key="file.path"
        type="button"
        class="file-row mono"
        :class="{ 'file-row--active': selectedPath === file.path }"
        @click="focusFile(file.path)"
      >
        <span class="file-status">{{ file.status }}</span>
        <span class="file-path" :title="file.path">{{ file.path }}</span>
        <span class="file-count"><b>+{{ file.additions }}</b><i>−{{ file.deletions }}</i></span>
      </button>
      <p v-if="!loading && !unavailable && diff && diff.files.length === 0" class="empty mono">{{ t('workbench.changes.noChanges') }}</p>
    </aside>

    <main class="changes-main">
      <!-- 没有 git 基线（409）时页签内说明原因（gofer-yb5x）：不给评审 / 接受按钮，
           也不把后端的原因当成加载失败。 -->
      <div v-if="unavailable" class="changes-unavailable" data-test="changes-unavailable">
        <p class="unavailable-title">{{ t('workbench.changes.unavailable') }}</p>
        <p class="unavailable-reason mono">{{ unavailable }}</p>
        <button type="button" :disabled="loading" @click="refresh">
          {{ loading ? t('workbench.changes.refreshing') : t('workbench.changes.refresh') }}
        </button>
      </div>
      <template v-else>
      <p v-if="error" class="changes-error mono">{{ error }}</p>
      <p v-if="locationNotice" class="changes-notice mono">{{ locationNotice }}</p>
      <div class="review-toolbar">
        <textarea
          class="review-summary mono"
          rows="2"
          :value="draft.summary"
          :placeholder="t('workbench.changes.summaryPlaceholder')"
          @input="setSummary(($event.target as HTMLTextAreaElement).value)"
        ></textarea>
        <div class="review-actions mono">
          <span>{{ t('workbench.changes.commentCount', { n: draft.comments.length }) }}</span>
          <button type="button" :disabled="accepting" @click="acceptChanges">
            {{ accepting ? t('workbench.changes.processing') : t('workbench.changes.accept') }}
          </button>
          <button
            class="review-send"
            type="button"
            :disabled="!canReview || !reviewValid || sending"
            @click="sendReview"
          >{{ sending ? t('workbench.changes.sending') : t('workbench.changes.sendReview', { n: draft.comments.length }) }}</button>
        </div>
      </div>
      <div class="diff-scroll">
        <UnifiedDiff
          v-if="diff"
          ref="diffRenderer"
          :text="diff.patch"
          :download-name="`thread-${threadId.replace(/[:/]/g, '-')}.diff`"
          reviewable
          :comments="draft.comments"
          @add-comment="addComment"
          @update-comment="updateComment"
          @remove-comment="removeComment"
        />
        <p v-else-if="loading" class="empty mono">{{ t('workbench.changes.loading') }}</p>
      </div>
      </template>
    </main>
  </div>
</template>

<style scoped>
.changes-view { height: 100%; min-height: 0; display: grid; grid-template-columns: minmax(190px, 26%) minmax(0, 1fr); background: var(--bg); }
.changes-files { min-height: 0; overflow-y: auto; border-right: 1px solid var(--line); background: var(--surface); }
.changes-files-head { position: sticky; top: 0; z-index: 2; display: flex; justify-content: space-between; align-items: center; padding: 8px 9px; border-bottom: 1px solid var(--line); background: var(--surface); color: var(--ink-3); }
.changes-files-head button, .review-actions button { padding: 4px 8px; border: 1px solid var(--line); border-radius: var(--r-chip); background: var(--bg); color: var(--ink); }
.file-row { display: grid; grid-template-columns: 24px minmax(0, 1fr) auto; gap: 6px; align-items: center; width: 100%; padding: 6px 8px; border: 0; border-bottom: 1px solid var(--line); background: transparent; color: var(--ink); text-align: left; }
.file-row:hover, .file-row--active { background: var(--bg); color: var(--accent); }
.file-status { color: var(--ink-3); }
.file-path { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-count { display: inline-flex; gap: 5px; font-size: 10px; }
.file-count b { color: var(--done); font-weight: 400; }
.file-count i { color: var(--fail); font-style: normal; }
.captured-note, .changes-notice, .changes-error, .empty { margin: 0; padding: 7px 9px; color: var(--ink-3); font-size: 11px; }
.changes-notice { color: var(--run); background: rgba(255, 190, 80, .07); }
.changes-error { color: var(--fail); background: rgba(200, 70, 70, .07); }
.changes-main { min-width: 0; min-height: 0; display: flex; flex-direction: column; }
/* 没有基线（409）：一条说明 + 重试，不留空白也不弹错误。 */
.changes-unavailable { display: flex; flex-direction: column; gap: 6px; align-items: flex-start; padding: 14px 12px; margin: 10px; border: 1px solid var(--line); border-radius: var(--r-chip); background: var(--surface); }
.unavailable-title { margin: 0; color: var(--ink); font-size: 12px; font-weight: 600; }
.unavailable-reason { margin: 0; color: var(--ink-3); font-size: 11px; white-space: pre-wrap; overflow-wrap: anywhere; }
.changes-unavailable button { padding: 4px 8px; border: 1px solid var(--line); border-radius: var(--r-chip); background: var(--bg); color: var(--ink); }
.changes-unavailable button:disabled { opacity: .4; }
.review-toolbar { flex: none; display: grid; grid-template-columns: minmax(180px, 1fr) auto; gap: 8px; padding: 8px; border-bottom: 1px solid var(--line); background: var(--surface); }
.review-summary { resize: vertical; min-height: 38px; padding: 6px 8px; border: 1px solid var(--line); border-radius: var(--r-chip); background: var(--bg); color: var(--ink); }
.review-actions { display: flex; align-items: center; gap: 7px; color: var(--ink-3); }
.review-actions .review-send { border-color: var(--accent); background: var(--accent); color: var(--bg); font-weight: 700; }
.review-actions button:disabled, .changes-files-head button:disabled { opacity: .4; }
.diff-scroll { flex: 1; min-height: 0; overflow: auto; padding: 10px; }
@media (max-width: 760px) {
  .changes-view { grid-template-columns: 1fr; grid-template-rows: minmax(110px, 28%) minmax(0, 1fr); }
  .changes-files { border-right: 0; border-bottom: 1px solid var(--line); }
  .review-toolbar { grid-template-columns: 1fr; }
  .review-actions { flex-wrap: wrap; }
}
</style>
