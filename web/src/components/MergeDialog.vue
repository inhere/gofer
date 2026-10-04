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
const title = computed(() => (props.pick ? '选这个并合并' : '合并到基线'))
const confirmText = computed(() => {
  if (busy.value) return '处理中…'
  if (props.pick && !doMerge.value) return '确认选择'
  return props.pick ? '确认选择并合并' : '确认合并'
})
const summary = computed(() => {
  const br = props.branch ? `分支 ${props.branch}` : '该分支'
  const how = squash.value ? 'squash（压成一个提交）' : 'merge（保留合并提交）'
  const parts: string[] = []
  if (doMerge.value) parts.push(`把 ${br} 以 ${how} 方式合并到项目主目录当前所在的基线分支（本地合并，不会 push）`)
  if (props.pick) {
    const who = props.pick.agent ? `${props.pick.agent}（fan ${props.pick.fan}）` : `fan ${props.pick.fan}`
    parts.push(`记录选中 ${who}，工作流继续往下走`)
  }
  if (doMerge.value && props.canCleanup && cleanupOthers.value) parts.push('强制清理其余各路的 worktree 与分支（含其未合并的提交，不可恢复）')
  return parts.join('；') + '。'
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
        warnText.value =
          (merged ? '已合并，但' : '') + `记录选择失败：${e instanceof Error ? e.message : String(e)}。可在 CLI 用 gofer wf pick 重试。`
        emit('done', { merged, picked: false, status })
        finished.value = true
        resultText.value = merged ? mergeResultText(squash.value, status?.cleaned) : ''
        return
      }
    }
    resultText.value = merged
      ? mergeResultText(squash.value, status?.cleaned) + (picked ? '已记录选择，工作流继续推进。' : '')
      : picked
        ? '已记录选择（未合并），工作流继续推进。'
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
        <button class="md-x mono" type="button" :disabled="busy" @click="emit('close')">关闭</button>
      </div>

      <template v-if="!finished">
        <p class="md-summary mono" data-test="merge-summary">{{ summary }}</p>

        <label v-if="pick" class="md-check mono">
          <input v-model="doMerge" type="checkbox" :disabled="!canMerge || busy" data-test="merge-do" />
          <span>同时合并到基线分支</span>
        </label>
        <p v-if="remoteReason" class="md-note mono" data-test="merge-remote-note">{{ remoteReason }}</p>

        <fieldset v-if="doMerge" class="md-group">
          <legend class="md-label mono">合并方式</legend>
          <label class="md-check mono">
            <input v-model="squash" type="radio" :value="false" :disabled="busy" name="md-how" data-test="merge-how-merge" />
            <span>merge：保留分支历史，生成合并提交</span>
          </label>
          <label class="md-check mono">
            <input v-model="squash" type="radio" :value="true" :disabled="busy" name="md-how" data-test="merge-how-squash" />
            <span>squash：压成一个提交</span>
          </label>
        </fieldset>

        <label v-if="doMerge && canCleanup" class="md-check mono">
          <input v-model="cleanupOthers" type="checkbox" :disabled="busy" data-test="merge-cleanup" />
          <span>合并后清理其余分支（强制删除其余各路的 worktree 与分支）</span>
        </label>

        <div v-if="failure" class="md-fail" data-test="merge-failure">
          <p class="md-err mono">{{ mergeErrorText(failure) }}</p>
          <template v-if="failure.kind === 'conflict'">
            <p v-if="failure.files.length" class="md-label mono">冲突文件（{{ failure.files.length }}）</p>
            <ul v-if="failure.files.length" class="md-files mono">
              <li v-for="f in failure.files" :key="f">{{ f }}</li>
            </ul>
          </template>
          <p v-else-if="failure.message && failure.kind !== 'other'" class="md-raw mono">{{ failure.message }}</p>
        </div>

        <div class="md-actions">
          <button class="md-btn mono" type="button" :disabled="busy" @click="emit('close')">取消</button>
          <button class="md-btn md-btn--go mono" type="button" :disabled="busy" data-test="merge-confirm" @click="run">
            {{ confirmText }}
          </button>
        </div>
      </template>

      <template v-else>
        <p class="md-ok mono" data-test="merge-result">{{ resultText || '完成。' }}</p>
        <p v-if="warnText" class="md-err mono">{{ warnText }}</p>
        <div class="md-actions">
          <button class="md-btn md-btn--go mono" type="button" @click="emit('close')">关闭</button>
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
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
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
  color: var(--phosphor);
  font-size: 13px;
  letter-spacing: 0.06em;
}
.md-x,
.md-btn {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 12px;
  font-size: 12px;
}
.md-x:hover:not(:disabled),
.md-btn:hover:not(:disabled) {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.md-x:disabled,
.md-btn:disabled {
  opacity: 0.6;
  cursor: default;
}
.md-btn--go {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.md-summary {
  margin: 0;
  font-size: 12px;
  color: var(--paper);
  line-height: 1.6;
  word-break: break-word;
}
.md-group {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.md-label {
  color: var(--queue);
  font-size: 11px;
  margin: 0;
}
.md-check {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  color: var(--paper);
  font-size: 12px;
}
.md-note {
  margin: 0;
  color: var(--queue);
  font-size: 12px;
  line-height: 1.5;
}
.md-fail {
  border: 1px solid var(--fail);
  border-radius: var(--radius);
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
  color: var(--paper);
  word-break: break-all;
}
.md-raw {
  margin: 0;
  color: var(--queue);
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
