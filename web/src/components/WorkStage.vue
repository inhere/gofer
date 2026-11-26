<script setup lang="ts">
// 工作项详情里的流程条（流程推进 stage，计划 2026-11-26 §2 F5）：模板名、步骤条、当前门禁缺项、
// 放权等级开关，以及批准 / 退回 / 推进三个动作。
// 这里只负责画与收集输入（可 SSR 单测）：取数与调接口在 WorkDrawer，动作经 emit 交回去。
import { computed, ref, watch } from 'vue'
import Button from '../ui/Button.vue'
import { fmtAgo } from '../api/time'
import { t } from '../i18n'
import {
  canApprove,
  canBack,
  canDone,
  stageFinished,
  stageLevelHint,
  stageLevelLabel,
  stageStepLabel,
  stageStepRows,
} from '../utils/stage'
import type { StageLevel, WorkStageView } from '../api/types'

const props = defineProps<{
  stage: WorkStageView
  busy?: boolean
  nowSec?: number
}>()

const emit = defineEmits<{
  (e: 'approve'): void
  (e: 'done'): void
  (e: 'back', reason: string): void
  (e: 'level', level: StageLevel): void
}>()

const rows = computed(() => stageStepRows(props.stage))
const current = computed(() => rows.value.find((r) => r.state === 'current' || r.state === 'blocked') ?? null)
const finished = computed(() => stageFinished(props.stage))
const canApproveNow = computed(() => canApprove(props.stage))
const canBackNow = computed(() => canBack(props.stage))
const canDoneNow = computed(() => canDone(props.stage))

// 退回必须写理由（服务端空理由 400），所以先展开输入框，填了才发。
const backOpen = ref(false)
const backReason = ref('')
watch(() => props.stage.step_index, () => {
  backOpen.value = false
  backReason.value = ''
})
function submitBack(): void {
  const reason = backReason.value.trim()
  if (!reason) return
  emit('back', reason)
}

const LEVELS: StageLevel[] = ['record', 'remind']
</script>

<template>
  <section class="sec stage-block" data-test="stage-block">
    <h4 class="stage-title">
      <span data-test="stage-template">{{ t('work.stage.title', { template: stage.template }) }}</span>
      <span class="stage-step-now" data-test="stage-step-now">{{ stageStepLabel(stage.step) }}</span>
    </h4>

    <ol class="stage-steps" data-test="stage-steps">
      <li
        v-for="(r, i) in rows"
        :key="r.name + i"
        class="stage-step"
        :class="`stage-step--${r.state}`"
        :data-test="`stage-step-${i}`"
        :data-state="r.state"
      >
        <span class="stage-mark" aria-hidden="true">{{ r.state === 'passed' ? '✓' : r.state === 'blocked' ? '⏸' : r.state === 'current' ? '▶' : '·' }}</span>
        <span class="stage-name">{{ stageStepLabel(r.name) }}</span>
      </li>
    </ol>

    <p v-if="stage.status === 'blocked'" class="stage-note stage-note--blocked" data-test="stage-blocked">
      {{ t('work.stage.blocked', { reason: stage.blocked_reason || t('work.stage.blockedNoReason') }) }}
    </p>
    <p v-else-if="finished" class="stage-note" data-test="stage-finished">
      {{ t(stage.status === 'dropped' ? 'work.stage.dropped' : 'work.stage.done') }}
    </p>
    <p class="stage-hint stage-since" data-test="stage-since">
      {{ t('work.stage.since', { d: fmtAgo(stage.step_entered_at, nowSec ?? Math.floor(Date.now() / 1000)) }) }}
    </p>

    <div v-if="current && (current.missing.length || current.needsHuman)" class="stage-gate" data-test="stage-gate">
      <p v-if="current.needsHuman" class="stage-note stage-note--human" data-test="stage-needs-human">{{ t('work.stage.needsHuman') }}</p>
      <ul v-if="current.missing.length" class="stage-missing" data-test="stage-missing">
        <li v-for="(m, i) in current.missing" :key="i">{{ m }}</li>
      </ul>
    </div>

    <div class="stage-level" role="group" :aria-label="t('work.stage.levelLabel')">
      <span class="stage-hint">{{ t('work.stage.levelLabel') }}</span>
      <button
        v-for="lv in LEVELS"
        :key="lv"
        type="button"
        class="stage-level-btn"
        :class="{ on: stage.level === lv }"
        :aria-pressed="stage.level === lv"
        :disabled="busy || finished"
        :title="stageLevelHint(lv)"
        :data-test="`stage-level-${lv}`"
        @click="emit('level', lv)"
      >{{ stageLevelLabel(lv) }}</button>
    </div>

    <div v-if="!finished" class="stage-actions">
      <Button v-if="canApproveNow" variant="primary" size="sm" :disabled="busy" data-test="stage-approve" @click="emit('approve')">
        {{ t('work.stage.approve') }}
      </Button>
      <Button size="sm" :disabled="busy || !canBackNow" data-test="stage-back-toggle" @click="backOpen = !backOpen">
        {{ t('work.stage.back') }}
      </Button>
      <Button variant="primary" size="sm" :disabled="busy || !canDoneNow" data-test="stage-done" @click="emit('done')">
        {{ busy ? t('work.stage.working') : t('work.stage.doneAction') }}
      </Button>
    </div>
    <div v-if="backOpen && !finished" class="stage-back-form">
      <input
        v-model="backReason"
        class="stage-back-input"
        type="text"
        :placeholder="t('work.stage.backReason')"
        :aria-label="t('work.stage.backReason')"
        data-test="stage-back-reason"
        @keydown.enter="submitBack"
      />
      <Button variant="primary" size="sm" :disabled="busy || !backReason.trim()" data-test="stage-back-submit" @click="submitBack">
        {{ t('work.stage.backSubmit') }}
      </Button>
    </div>
  </section>
</template>

<style scoped>
.stage-block { border-top: 1px solid var(--line); }
.stage-title { display: flex; align-items: baseline; flex-wrap: wrap; gap: 6px; margin: 0 0 var(--s2); font-size: var(--fs-sm); font-weight: 600; color: var(--ink); }
.stage-step-now { margin-left: var(--s2); font-size: var(--fs-xs); font-weight: 400; color: var(--accent); }
/* 步骤条：一行横排，窄屏自己横向滑动（不折行，保持「第几步 / 共几步」的阅读顺序）。 */
.stage-steps { display: flex; align-items: center; gap: 6px; margin: 0 0 var(--s2); padding: 0; list-style: none; overflow-x: auto; }
.stage-step { display: inline-flex; align-items: center; gap: 4px; padding: 2px 8px; font-size: var(--fs-xs); color: var(--ink-3); background: var(--surface); border: 1px solid var(--line); border-radius: 999px; white-space: nowrap; }
.stage-mark { font-variant-numeric: tabular-nums; }
.stage-step--passed { color: var(--ink-2); border-color: var(--line); }
.stage-step--current { color: var(--accent); border-color: var(--accent); background: var(--accent-soft); }
.stage-step--blocked { color: var(--fail); border-color: var(--fail); }
.stage-note { margin: 0 0 var(--s2); font-size: var(--fs-sm); color: var(--ink-2); }
.stage-note--blocked { color: var(--fail); }
.stage-note--human { color: var(--run); }
.stage-since { margin: 0 0 var(--s2); }
.stage-hint { font-size: var(--fs-xs); color: var(--ink-3); }
.stage-missing { margin: 0 0 var(--s2); padding-left: 16px; font-size: var(--fs-sm); color: var(--ink-2); list-style: disc; }
.stage-level { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-bottom: var(--s2); font-size: var(--fs-xs); }
.stage-level-btn { padding: 2px 10px; font: inherit; font-size: var(--fs-xs); color: var(--ink-3); background: var(--surface); border: 1px solid var(--line); border-radius: 999px; cursor: pointer; }
.stage-level-btn.on { color: var(--accent); background: var(--accent-soft); border-color: var(--accent); }
.stage-level-btn:disabled { cursor: default; opacity: 0.6; }
.stage-actions { display: flex; align-items: center; flex-wrap: wrap; gap: var(--s2); margin-bottom: var(--s2); }
.stage-back-form { display: flex; align-items: center; flex-wrap: wrap; gap: var(--s2); margin-bottom: var(--s2); }
.stage-back-input { flex: 1 1 220px; min-width: 0; min-height: 30px; padding: 4px var(--s2); font: inherit; font-size: var(--fs-sm); color: var(--ink); background: var(--surface); border: 1px solid var(--line); border-radius: var(--r-ctl); }
</style>
