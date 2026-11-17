<script setup lang="ts">
// job 概览里的「自报终态」（gofer_submit_result，计划 R9 / R12）：agent 自己怎么判断这次运行。
// 徽标 + 理由 + 需人处理事项 + findings 列表。只展示：自报不改 job 的 status，也没有任何写入口
// （提交是 job 内 agent 的 MCP 工具做的）。解析在 utils/jobSelfResult.ts。
import { computed } from 'vue'
import type { Job } from '../api/types'
import { t } from '../i18n'
import { selfResultView } from '../utils/jobSelfResult'

const props = defineProps<{ job: Job }>()

const view = computed(() => selfResultView(props.job))
</script>

<template>
  <div
    v-if="view"
    class="outcome-block self-result"
    :class="`self-result--${view.decision}`"
    data-test="job-self-result"
  >
    <div class="outcome-head">
      <span class="outcome-k mono">{{ t('jobs.selfResult.label') }}</span>
      <span class="self-decision mono" :class="`self-decision--${view.decision}`" data-test="job-self-result-decision">
        {{ view.label }}
      </span>
      <span v-if="job.result_attempt && job.result_attempt > 1" class="self-attempt mono">
        {{ t('jobs.selfResult.attempt', { n: job.result_attempt }) }}
      </span>
    </div>
    <p v-if="view.reason" class="self-reason mono" data-test="job-self-result-reason">
      <span class="self-k mono">{{ t('jobs.selfResult.reason') }}</span>{{ view.reason }}
    </p>
    <p v-if="view.actionRequired" class="self-action mono" data-test="job-self-result-action">
      <span class="self-k mono">{{ t('jobs.selfResult.actionRequired') }}</span>{{ view.actionRequired }}
    </p>
    <div v-if="view.findings.length > 0" class="self-findings" data-test="job-self-result-findings">
      <p class="self-k mono">{{ t('jobs.selfResult.findings', { n: view.findings.length }) }}</p>
      <ul class="self-list">
        <li v-for="(f, i) in view.findings" :key="i" class="mono">{{ f }}</li>
      </ul>
    </div>
    <p class="self-note mono">{{ t('jobs.selfResult.note') }}</p>
  </div>
</template>

<style scoped>
/* JobDetail 的 outcome-block 规则是 scoped 的，进不到子组件里，这里重复同一套外观。 */
.outcome-block { margin-top: 12px; padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--r-chip); background: var(--surface); }
.outcome-head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.outcome-k { font-size: 11px; letter-spacing: 0.06em; color: var(--ink-3); text-transform: uppercase; }
.self-result--human { border-color: var(--need); }
.self-result--fail { border-color: var(--fail); }
.self-decision { font-size: 11px; padding: 1px 7px; border: 1px solid var(--line); border-radius: 999px; color: var(--ink-2); }
.self-decision--pass { color: var(--done); border-color: currentColor; }
.self-decision--rework { color: var(--need-ink); border-color: currentColor; }
.self-decision--fail { color: var(--fail); border-color: currentColor; }
.self-decision--human { color: var(--need-ink); border-color: currentColor; }
.self-attempt { font-size: 11px; color: var(--ink-3); }
.self-reason, .self-action { margin: 8px 0 0; font-size: 12px; color: var(--ink); overflow-wrap: anywhere; }
.self-k { font-size: 11px; color: var(--ink-3); margin-right: 6px; }
.self-findings { margin-top: 8px; }
.self-findings .self-k { display: block; margin: 0 0 4px; }
.self-list { margin: 0; padding-left: 18px; font-size: 12px; color: var(--ink-2); }
.self-list li { margin: 2px 0; overflow-wrap: anywhere; }
.self-note { margin: 8px 0 0; font-size: 11px; color: var(--ink-3); }
</style>
