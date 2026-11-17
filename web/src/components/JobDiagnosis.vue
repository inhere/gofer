<script setup lang="ts">
// job 诊断抽屉（计划 R10 / R12）：一个「诊断」按钮 + 点开后拉的右侧抽屉。
//  - 数据来自 GET /v1/jobs/{id}/diagnosis：终止原因、续跑链与首错、阶段时间线、证据 / 未知 /
//    下一步。全部由已有字段推导，服务端按请求语言给出标签与建议。
//  - 打开时才请求（诊断要读日志文件的修改时间，没必要在每次进详情页都算一遍）。
// 按钮与抽屉放在同一个组件里，详情页只加一行调用（JobDetail 超过 1000 行，只接线）。
import { ref } from 'vue'
import { getJobDiagnosis } from '../api/client'
import { fmtDateTime, fmtDuration, toUnixSec } from '../api/time'
import type { JobDiagnosis } from '../api/types'
import { t } from '../i18n'
import { diagnosisChainCount, diagnosisErrorText, diagnosisIsFirstFailure, diagnosisRelationLabel } from '../utils/jobDiagnosis'

const props = defineProps<{ jobId: string }>()

const open = ref(false)
const diag = ref<JobDiagnosis | null>(null)
const loading = ref(false)
const error = ref('')

async function toggle(): Promise<void> {
  if (open.value) {
    open.value = false
    return
  }
  open.value = true
  await load()
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    diag.value = await getJobDiagnosis(props.jobId)
  } catch (e) {
    diag.value = null
    error.value = diagnosisErrorText(e)
  } finally {
    loading.value = false
  }
}

// durationText 是某一段的耗时：两端的 unix 秒都在时算出来，否则留空（不猜）。
function durationText(start?: number, end?: number): string {
  const a = toUnixSec(start)
  const b = toUnixSec(end)
  if (!a || !b || b < a) return ''
  return fmtDuration(b - a)
}
</script>

<template>
  <span class="diag-wrap">
    <button
      class="diag-open mono"
      type="button"
      data-test="job-diagnose"
      :aria-expanded="open"
      @click="toggle"
    >
      {{ t('jobs.diagnosis.button') }}
    </button>

    <div v-if="open" class="diag-overlay" @click.self="open = false">
      <div class="diag-panel" role="dialog" :aria-label="t('jobs.diagnosis.title')" data-test="job-diagnosis">
        <div class="diag-head">
          <span class="diag-title mono">{{ t('jobs.diagnosis.title') }}</span>
          <button class="diag-close mono" type="button" @click="open = false">{{ t('jobs.diagnosis.close') }}</button>
        </div>

        <div class="diag-body">
          <p v-if="loading" class="mono diag-dim" data-test="job-diagnosis-loading">{{ t('jobs.diagnosis.loading') }}</p>
          <p v-else-if="error" class="mono diag-error" data-test="job-diagnosis-error">{{ error }}</p>

          <template v-else-if="diag">
            <!-- 终止原因：枚举值 + 服务端按请求语言给的标签。 -->
            <div class="diag-block">
              <span class="diag-k mono">{{ t('jobs.diagnosis.reason') }}</span>
              <span class="diag-reason mono" data-test="job-diagnosis-reason">{{ diag.termination_label || diag.termination_reason }}</span>
              <span class="diag-dim mono">{{ diag.termination_reason }}</span>
              <span class="diag-dim mono">{{ diag.status }}</span>
              <span v-if="!diag.finished" class="diag-flag mono">{{ t('jobs.diagnosis.running') }}</span>
            </div>
            <pre v-if="diag.error" class="diag-pre mono">{{ diag.error }}</pre>

            <!-- 续跑链：本 job 与它上下串起来的续跑 / 切换 / 重试，首错高亮。 -->
            <template v-if="diagnosisChainCount(diag) > 1">
              <h3 class="diag-sub mono">{{ t('jobs.diagnosis.chain', { n: diagnosisChainCount(diag) }) }}</h3>
              <div class="diag-table" data-test="job-diagnosis-chain">
                <div class="diag-thead mono">
                  <span>{{ t('jobs.diagnosis.colJob') }}</span>
                  <span>{{ t('jobs.diagnosis.colRelation') }}</span>
                  <span>{{ t('jobs.diagnosis.colStatus') }}</span>
                  <span>{{ t('jobs.diagnosis.colReason') }}</span>
                  <span>{{ t('jobs.diagnosis.colTime') }}</span>
                </div>
                <div
                  v-for="link in diag.chain"
                  :key="link.job_id"
                  class="diag-trow mono"
                  :class="{ 'diag-trow--first': diagnosisIsFirstFailure(diag, link) }"
                >
                  <span class="diag-job" :title="link.job_id">{{ link.job_id }}</span>
                  <span>{{ diagnosisRelationLabel(link.relation) }}</span>
                  <span>{{ link.status }}</span>
                  <span class="diag-dim">{{ link.termination_reason }}</span>
                  <span class="diag-dim">{{ fmtDateTime(link.started_at) }}<template v-if="durationText(link.started_at, link.ended_at)"> · {{ durationText(link.started_at, link.ended_at) }}</template></span>
                  <span v-if="link.error" class="diag-err mono">{{ link.error }}</span>
                  <span v-if="diagnosisIsFirstFailure(diag, link)" class="diag-flag mono">{{ t('jobs.diagnosis.firstFailure') }}</span>
                </div>
              </div>
            </template>

            <!-- 时间线：五段顺序固定；没有记录的段落显示 note，不借邻近时间补。 -->
            <h3 class="diag-sub mono">{{ t('jobs.diagnosis.timeline') }}</h3>
            <ul class="diag-list diag-timeline" data-test="job-diagnosis-timeline">
              <li v-for="st in diag.timeline" :key="st.stage" class="diag-line mono" :class="{ 'diag-line--missing': !st.present }">
                <span class="diag-label">{{ st.label }}</span>
                <span v-if="st.present" class="diag-value">{{ fmtDateTime(st.at) }}</span>
                <span v-else class="diag-dim">{{ st.note }}</span>
              </li>
            </ul>

            <h3 class="diag-sub mono">{{ t('jobs.diagnosis.evidence') }}</h3>
            <ul class="diag-list" data-test="job-diagnosis-evidence">
              <li v-for="(ev, i) in diag.evidence" :key="i" class="diag-line mono">{{ ev }}</li>
            </ul>

            <template v-if="diag.unknowns.length > 0">
              <h3 class="diag-sub mono">{{ t('jobs.diagnosis.unknowns') }}</h3>
              <ul class="diag-list" data-test="job-diagnosis-unknowns">
                <li v-for="(u, i) in diag.unknowns" :key="i" class="diag-line mono diag-dim">{{ u }}</li>
              </ul>
            </template>

            <h3 class="diag-sub mono">{{ t('jobs.diagnosis.nextSteps') }}</h3>
            <ul class="diag-list" data-test="job-diagnosis-next-steps">
              <li v-for="(n, i) in diag.next_steps" :key="i" class="diag-line mono">{{ n }}</li>
            </ul>
          </template>
        </div>
      </div>
    </div>
  </span>
</template>

<style scoped>
/* 按钮跟在 JobDetail 头部操作区里，其余类名与详情页的抽屉（diff）同一套外观，scoped 各自一份。 */
.diag-wrap { display: inline-flex; }
.diag-open { background: transparent; color: var(--accent); border: 1px solid var(--line); border-radius: var(--r-chip); padding: 4px 10px; font-size: 12px; cursor: pointer; }
.diag-open:hover { border-color: var(--accent); }
.diag-overlay { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.35); display: flex; justify-content: flex-end; z-index: 60; }
.diag-panel { width: min(760px, 100%); height: 100%; display: flex; flex-direction: column; background: var(--surface); border-left: 1px solid var(--line); }
.diag-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 10px 14px; border-bottom: 1px solid var(--line); }
.diag-title { font-size: 12px; letter-spacing: 0.08em; color: var(--ink-3); text-transform: uppercase; }
.diag-close { background: transparent; color: var(--accent); border: 1px solid var(--line); border-radius: var(--r-chip); padding: 3px 10px; font-size: 12px; cursor: pointer; }
.diag-body { flex: 1 1 auto; overflow: auto; padding: 12px 14px 20px; }
.diag-block { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; }
.diag-k { font-size: 11px; letter-spacing: 0.06em; color: var(--ink-3); text-transform: uppercase; }
.diag-reason { font-size: 13px; color: var(--ink); }
.diag-dim { color: var(--ink-3); font-size: 11px; }
.diag-flag { font-size: 11px; padding: 1px 7px; border-radius: 999px; border: 1px solid var(--need, var(--line)); color: var(--need-ink, var(--ink-2)); }
.diag-error { color: var(--fail); font-size: 12px; }
.diag-pre { margin: 10px 0 0; padding: 8px 10px; border: 1px solid var(--line); border-radius: var(--r-chip); font-size: 12px; color: var(--ink-2); overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; }
.diag-sub { margin: 16px 0 6px; font-size: 11px; letter-spacing: 0.06em; color: var(--ink-3); text-transform: uppercase; }
.diag-table { display: flex; flex-direction: column; border: 1px solid var(--line); border-radius: var(--r-chip); overflow: hidden; }
.diag-thead, .diag-trow { display: grid; grid-template-columns: minmax(120px, 1.3fr) minmax(70px, 0.7fr) minmax(64px, 0.6fr) minmax(90px, 0.9fr) minmax(120px, 1.2fr); gap: 8px; padding: 6px 10px; font-size: 12px; align-items: start; }
.diag-thead { background: var(--surface-2, transparent); color: var(--ink-3); font-size: 11px; border-bottom: 1px solid var(--line); }
.diag-trow { border-bottom: 1px solid var(--line); }
.diag-trow:last-child { border-bottom: none; }
.diag-trow--first { background: color-mix(in srgb, var(--fail, #f55) 8%, transparent); }
.diag-job { overflow-wrap: anywhere; color: var(--ink); }
.diag-err, .diag-flag { grid-column: 1 / -1; font-size: 11px; color: var(--fail); overflow-wrap: anywhere; }
.diag-flag { justify-self: start; color: var(--need-ink, var(--ink-2)); border: 1px solid currentColor; border-radius: 999px; padding: 1px 7px; }
.diag-list { margin: 0; padding-left: 16px; }
.diag-timeline { list-style: none; padding-left: 0; }
.diag-line { font-size: 12px; color: var(--ink-2); margin: 2px 0; overflow-wrap: anywhere; }
.diag-line--missing { color: var(--ink-3); }
.diag-label { display: inline-block; min-width: 96px; color: var(--ink-3); }
.diag-value { color: var(--ink); }
@media (max-width: 760px) {
  .diag-thead { display: none; }
  .diag-trow { grid-template-columns: repeat(2, minmax(0, 1fr)); row-gap: 4px; }
  .diag-label { min-width: 0; }
}
</style>
