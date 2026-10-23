<script setup lang="ts">
// 合并确认弹层（Z4）：「合并到基线」与对比视图的「选这个并合并」共用。
//  - 选项：合并方式 merge/squash；清理其余分支（仅扇出步里的 job 有意义）；
//    对比视图里额外可取消"合并到基线"（只记录选择，远程 runner 只能这样）。
//  - 顺序：先合并后择优。冲突（409）时什么都没记录、仓库已复原，可改选另一路；
//    先择优会让下游步骤在没合并的代码上继续跑。
//  - 失败原因就地展示（冲突文件清单 / 主目录不干净等），弹层保持打开。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ApiError, mergeJobWorktree, pickWorkflowFan } from '../api/client'
import type { WorktreeStatus } from '../api/types'
import {
  mergeErrorText,
  mergeRequestBody,
  mergeResultText,
  parseMergeError,
  type MergeErrorInfo,
} from '../utils/compare'
import { t } from '../i18n'

const props = defineProps<{
  jobId: string
  branch?: string
  // 对比视图：选中这一路（先合并再记录选择）；JobDetail 里不传
  pick?: { workflowId: string; step: number; fan: number; agent?: string } | null
  // 是否提供「清理其余分支」选项（只有扇出步里的 job 才有其余分支）
  canCleanup: boolean
  // 非空 = 不能合并（远程 runner），说明原因；对比视图里仍可「仅选择」
  remoteReason: string
}>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'done', result: { merged: boolean; picked: boolean; status?: WorktreeStatus }): void
}>()

const squash = ref(false)
const cleanupOthers = ref(false)
const doMerge = ref(props.remoteReason === '')
const busy = ref(false)
const failure = ref<MergeErrorInfo | null>(null)
const resultText = ref('')
const warnText = ref('')
const finished = ref(false)

const canMerge = computed(() => props.remoteReason === '')
const title = computed(() => (props.pick ? t('util.merge.pickAndMerge') : t('util.merge.toBase')))
const confirmText = computed(() => {
  if (busy.value) return t('util.merge.working')
  if (props.pick && !doMerge.value) return t('util.merge.confirmPick')
  return props.pick ? t('util.merge.confirmPickAndMerge') : t('util.merge.confirmMerge')
})
const summary = computed(() => {
  const br = props.branch ? t('util.merge.branch', { branch: props.branch }) : t('util.merge.thisBranch')
  const how = squash.value ? t('util.merge.howSquash') : t('util.merge.howMerge')
  const parts: string[] = []
  if (doMerge.value) parts.push(t('util.merge.summaryMerge', { br, how }))
  if (props.pick) {
    const who = props.pick.agent ? t('util.merge.whoAgent', { agent: props.pick.agent, fan: props.pick.fan }) : t('util.merge.whoFan', { fan: props.pick.fan })
    parts.push(t('util.merge.summaryPick', { who }))
  }
  if (doMerge.value && props.canCleanup && cleanupOthers.value) parts.push(t('util.merge.summaryCleanup'))
  return parts.join(t('util.merge.sep')) + t('util.merge.end')
})

async function run(): Promise<void> {
  if (busy.value) return
  busy.value = true
  failure.value = null
  warnText.value = ''
  let status: WorktreeStatus | undefined
  let merged = false
  try {
    if (doMerge.value) {
      try {
        status = await mergeJobWorktree(
          props.jobId,
          mergeRequestBody({ squash: squash.value, cleanupOthers: cleanupOthers.value }, props.canCleanup),
        )
        merged = true
      } catch (e) {
        const status409 = e instanceof ApiError ? e.status : undefined
        // ApiError.code 存的是后端 error 原文（含冲突文件清单），message 是 error - detail。
        const raw = e instanceof ApiError ? (e.code ?? e.message) : e instanceof Error ? e.message : String(e)
        failure.value = parseMergeError(status409, raw)
        return
      }
    }
    let picked = false
    if (props.pick) {
      try {
        await pickWorkflowFan(props.pick.workflowId, props.pick.step, props.pick.fan)
        picked = true
      } catch (e) {
        warnText.value = (merged ? t('util.merge.warnMergedPrefix') : '') + t('util.merge.warnPickFailed', { err: e instanceof Error ? e.message : String(e) })
        emit('done', { merged, picked: false, status })
        finished.value = true
        resultText.value = merged ? mergeResultText(squash.value, status?.cleaned) : ''
        return
      }
    }
    resultText.value = merged
      ? mergeResultText(squash.value, status?.cleaned) + (picked ? t('util.merge.pickedContinues') : '')
      : picked
        ? t('util.merge.pickedOnlyContinues')
        : ''
    finished.value = true
    emit('done', { merged, picked, status })
  } finally {
    busy.value = false
  }
}

function onKeydown(ev: KeyboardEvent): void {
  if (ev.key === 'Escape' && !busy.value) emit('close')
}
onMounted(() => document.addEventListener('keydown', onKeydown))
onUnmounted(() => document.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div class="md-overlay" @click.self="!busy && emit('close')">
    <div class="md-panel" role="dialog" aria-modal="true" :aria-label="title" data-test="merge-dialog">
      <div class="md-head">
        <span class="md-title mono">{{ title }}</span>
        <button class="md-x mono" type="button" :disabled="busy" @click="emit('close')">{{ t('util.action.close') }}</button>
      </div>

      <template v-if="!finished">
        <p class="md-summary mono" data-test="merge-summary">{{ summary }}</p>

        <label v-if="pick" class="md-check mono">
          <input v-model="doMerge" type="checkbox" :disabled="!canMerge || busy" data-test="merge-do" />
          <span>{{ t('util.merge.alsoMerge') }}</span>
        </label>
        <p v-if="remoteReason" class="md-note mono" data-test="merge-remote-note">{{ remoteReason }}</p>

        <fieldset v-if="doMerge" class="md-group">
          <legend class="md-label mono">{{ t('util.merge.how') }}</legend>
          <label class="md-check mono">
            <input v-model="squash" type="radio" :value="false" :disabled="busy" name="md-how" data-test="merge-how-merge" />
            <span>{{ t('util.merge.howMergeOption') }}</span>
          </label>
          <label class="md-check mono">
            <input v-model="squash" type="radio" :value="true" :disabled="busy" name="md-how" data-test="merge-how-squash" />
            <span>{{ t('util.merge.howSquashOption') }}</span>
          </label>
        </fieldset>

        <label v-if="doMerge && canCleanup" class="md-check mono">
          <input v-model="cleanupOthers" type="checkbox" :disabled="busy" data-test="merge-cleanup" />
          <span>{{ t('util.merge.cleanupOption') }}</span>
        </label>

        <div v-if="failure" class="md-fail" data-test="merge-failure">
          <p class="md-err mono">{{ mergeErrorText(failure) }}</p>
          <template v-if="failure.kind === 'conflict'">
            <p v-if="failure.files.length" class="md-label mono">{{ t('util.merge.conflictFiles', { n: failure.files.length }) }}</p>
            <ul v-if="failure.files.length" class="md-files mono">
              <li v-for="f in failure.files" :key="f">{{ f }}</li>
            </ul>
          </template>
          <p v-else-if="failure.message && failure.kind !== 'other'" class="md-raw mono">{{ failure.message }}</p>
        </div>

        <div class="md-actions">
          <button class="md-btn mono" type="button" :disabled="busy" @click="emit('close')">{{ t('util.action.cancel') }}</button>
          <button class="md-btn md-btn--go mono" type="button" :disabled="busy" data-test="merge-confirm" @click="run">
            {{ confirmText }}
          </button>
        </div>
      </template>

      <template v-else>
        <p class="md-ok mono" data-test="merge-result">{{ resultText || t('util.merge.finished') }}</p>
        <p v-if="warnText" class="md-err mono">{{ warnText }}</p>
        <div class="md-actions">
          <button class="md-btn md-btn--go mono" type="button" @click="emit('close')">{{ t('util.action.close') }}</button>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.md-overlay {
  position: fixed;
  inset: 0;
  z-index: 40;
  background: rgba(0, 0, 0, 0.55);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
}
.md-panel {
  width: 100%;
  max-width: 520px;
  max-height: 90vh;
  overflow: auto;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.md-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.md-title {
  color: var(--accent);
  font-size: 13px;
  letter-spacing: 0.06em;
}
.md-x,
.md-btn {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 12px;
  font-size: 12px;
}
.md-x:hover:not(:disabled),
.md-btn:hover:not(:disabled) {
  color: var(--accent);
  border-color: var(--accent);
}
.md-x:disabled,
.md-btn:disabled {
  opacity: 0.6;
  cursor: default;
}
.md-btn--go {
  border-color: var(--accent);
  color: var(--accent);
}
.md-summary {
  margin: 0;
  font-size: 12px;
  color: var(--ink);
  line-height: 1.6;
  word-break: break-word;
}
.md-group {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 8px 10px;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.md-label {
  color: var(--ink-3);
  font-size: 11px;
  margin: 0;
}
.md-check {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  color: var(--ink);
  font-size: 12px;
}
.md-note {
  margin: 0;
  color: var(--ink-3);
  font-size: 12px;
  line-height: 1.5;
}
.md-fail {
  border: 1px solid var(--fail);
  border-radius: var(--r-chip);
  padding: 8px 10px;
}
.md-err {
  margin: 0 0 6px;
  color: var(--fail);
  font-size: 12px;
  line-height: 1.5;
  word-break: break-word;
}
.md-files {
  margin: 0;
  padding-left: 18px;
  font-size: 12px;
  color: var(--ink);
  word-break: break-all;
}
.md-raw {
  margin: 0;
  color: var(--ink-3);
  font-size: 11px;
  word-break: break-word;
}
.md-ok {
  margin: 0;
  color: var(--done);
  font-size: 13px;
  line-height: 1.6;
}
.md-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
