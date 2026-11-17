<script setup lang="ts">
// 推进记录页签（设计 2026-11-16 §1，计划 R12）：一个 plan 的推进引擎状态、预算与已用、
// 以及「记录的动作为主 vs 实际发生的处理」的一致率汇总。
//  - 开关走 PUT / DELETE /v1/plans/{id}/driver；等级固定 record（assist / auto 由服务端
//    400 拒绝并在提示里说明「后续版本」），开时可填三项预算（0 = 不限）。
//  - 数字来自 GET /v1/plans/{id}/driver/report：按动作 / 按规则的一致率、最近 N 次与
//    「建议升级」标记；recent 是逐条动作明细（最新在前，最多 100 条），每条可展开 params。
//  - record 等级下引擎只算不做：这段界面不能、也不会改变 plan / todo / job 的状态。
// 文案、预算换算、一致率与错误分类都在 utils/planDriver.ts（纯函数，有单测）。
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { deletePlanDriver, getPlanDriver, getPlanDriverReport, putPlanDriver } from '../api/client'
import { fmtDateTime } from '../api/time'
import type { PlanDriverAction, PlanDriverReport, PlanDriverStatus } from '../api/types'
import { t } from '../i18n'
import {
  DRIVER_LEVEL_RECORD, driverActionLabel, driverBudgetDraft, driverBudgetFromDraft, driverBudgetLine,
  driverErrorText, driverLevelLabel, driverOutcomeLabel, driverParamsText, driverRateText, driverRecentText,
  driverRiskLabel, driverRiskTone, driverRuleLabel, driverSpentLine, driverState, driverUpgradeHint,
  driverWouldPauseText,
  type DriverBudgetDraft,
} from '../utils/planDriver'

const props = defineProps<{ planId: string; writeBlock?: string }>()

// todo 标识点开后由详情页打开同一条待办抽屉（抽屉的数据是本页那一份 plan，不另取）。
const emit = defineEmits<{ (e: 'open-todo', todoId: string): void }>()

const status = ref<PlanDriverStatus | null>(null)
const report = ref<PlanDriverReport | null>(null)
const loadError = ref('')
const reportError = ref('')
const busy = ref(false)
const formError = ref('')
// 表单在「未启用」时展开（开启要把预算填进去）；启用后收起，想改预算再点开。
const formOpen = ref(false)
const draft = ref<DriverBudgetDraft>(driverBudgetDraft({ max_jobs: 0, max_rework: 0, max_hours: 0 }))

const state = computed(() => driverState(status.value))
const enabled = computed(() => state.value === 'on')
const byAction = computed(() => report.value?.by_action ?? [])
const byRule = computed(() => report.value?.by_rule ?? [])
const total = computed(() => report.value?.total ?? 0)
const pending = computed(() => report.value?.pending ?? 0)
const windowHours = computed(() => report.value?.window_hours ?? 0)
// 写按钮：没有 plan 写权限（别人的 plan 且没有 plan.manage.others）时禁用并给出原因。
const canWrite = computed(() => !props.writeBlock)

// 动作明细：recent 就是最近的记录（最新在前，服务端已截到 100 条），这里不再排序。
const recentRows = computed<PlanDriverAction[]>(() => report.value?.recent ?? [])
// 展开过的行（按记录 id）：默认全收起，避免一百条 params 一次铺满页面。
const expanded = ref<Set<number>>(new Set())

function toggleRow(id: number): void {
  const next = new Set(expanded.value)
  if (next.has(id)) {
    next.delete(id)
  } else {
    next.add(id)
  }
  expanded.value = next
}

function openTodo(todoId: string): void {
  emit('open-todo', todoId)
}

async function load(): Promise<void> {
  loadError.value = ''
  reportError.value = ''
  try {
    status.value = await getPlanDriver(props.planId)
  } catch (e) {
    loadError.value = driverErrorText(e)
    status.value = null
    return
  }
  if (!formOpen.value) draft.value = driverBudgetDraft(status.value.budget)
  await loadReport()
}

async function loadReport(): Promise<void> {
  reportError.value = ''
  try {
    report.value = await getPlanDriverReport(props.planId)
  } catch (e) {
    report.value = null
    reportError.value = driverErrorText(e)
  }
}

function openForm(): void {
  formError.value = ''
  draft.value = driverBudgetDraft(status.value?.budget ?? { max_jobs: 0, max_rework: 0, max_hours: 0 })
  formOpen.value = true
}

async function enable(): Promise<void> {
  formError.value = ''
  const parsed = driverBudgetFromDraft(draft.value)
  if (!parsed.budget) {
    formError.value = parsed.error ?? ''
    return
  }
  busy.value = true
  try {
    status.value = await putPlanDriver(props.planId, { level: DRIVER_LEVEL_RECORD, budget: parsed.budget })
    formOpen.value = false
    await loadReport()
  } catch (e) {
    formError.value = driverErrorText(e)
  } finally {
    busy.value = false
  }
}

async function disable(): Promise<void> {
  formError.value = ''
  busy.value = true
  try {
    status.value = await deletePlanDriver(props.planId)
    await loadReport()
  } catch (e) {
    formError.value = driverErrorText(e)
  } finally {
    busy.value = false
  }
}

onMounted(load)
watch(() => props.planId, load)
</script>

<template>
  <section class="section plan-driver" data-test="plan-driver">
    <div class="section-head">
      <h2 class="section-title mono">{{ t('plans.driver.title') }}</h2>
      <span v-if="enabled" class="pd-level mono" data-test="plan-driver-level">{{ driverLevelLabel(status?.level) }}</span>
      <span v-if="enabled && status?.enabled_at" class="pd-since mono">{{ t('plans.driver.enabledAt', { at: fmtDateTime(status.enabled_at) }) }}</span>
      <!-- 记录在 plan 推进时增长（可能来自其他人的 job），详情页的轮询不会带上它，所以给一个手动的刷新。 -->
      <button class="op-btn pd-refresh" type="button" data-test="plan-driver-refresh" :disabled="busy" @click="load">
        {{ t('plans.driver.refresh') }}
      </button>
    </div>

    <p v-if="loadError" class="error mono" data-test="plan-driver-load-error">{{ loadError }}</p>

    <template v-else-if="status">
      <!-- 未启用：一句说明 + 「开启只记录」。等级固定 record，其余等级本版不提供。 -->
      <div v-if="!enabled" class="pd-body" data-test="plan-driver-off">
        <p class="mono pd-note">{{ t('plans.driver.offHint') }}</p>
        <p class="mono pd-note pd-note--dim">{{ t('plans.driver.levelNote') }}</p>
      </div>
      <div v-else class="pd-body" data-test="plan-driver-on">
        <p class="mono pd-row-line" data-test="plan-driver-budget">
          {{ t('plans.driver.budgetLine', { value: driverBudgetLine(status.budget) }) }}
        </p>
        <p class="mono pd-row-line" data-test="plan-driver-spent">
          {{ t('plans.driver.spentLine', { value: driverSpentLine(report?.spent ?? { jobs: 0, rework: 0, hours: 0 }) }) }}
        </p>
      </div>

      <!-- 预算表单：未启用时随「开启只记录」一起提交，启用后改预算也是同一个 PUT。 -->
      <div v-if="formOpen || !enabled" class="pd-form" data-test="plan-driver-form">
        <label class="pd-field mono">
          <span>{{ t('plans.driver.maxJobs') }}</span>
          <input v-model="draft.maxJobs" class="op-input mono" type="number" min="0" step="1" :placeholder="t('plans.driver.unlimited')" />
        </label>
        <label class="pd-field mono">
          <span>{{ t('plans.driver.maxRework') }}</span>
          <input v-model="draft.maxRework" class="op-input mono" type="number" min="0" step="1" :placeholder="t('plans.driver.unlimited')" />
        </label>
        <label class="pd-field mono">
          <span>{{ t('plans.driver.maxHours') }}</span>
          <input v-model="draft.maxHours" class="op-input mono" type="number" min="0" step="0.5" :placeholder="t('plans.driver.unlimited')" />
        </label>
        <p class="mono pd-note pd-note--dim pd-form-hint">{{ t('plans.driver.budgetZeroHint') }}</p>
      </div>

      <div class="pd-actions">
        <button
          v-if="!enabled"
          class="op-btn pd-enable"
          type="button"
          data-test="plan-driver-enable"
          :disabled="busy || !canWrite"
          :title="writeBlock || undefined"
          @click="enable"
        >
          {{ busy ? t('plans.driver.enabling') : t('plans.driver.enable') }}
        </button>
        <template v-else>
          <button class="op-btn pd-budget" type="button" data-test="plan-driver-edit-budget" :disabled="busy || !canWrite" :title="writeBlock || undefined" @click="openForm">
            {{ t('plans.driver.editBudget') }}
          </button>
          <button class="op-btn pd-save" v-if="formOpen" type="button" data-test="plan-driver-save-budget" :disabled="busy || !canWrite" @click="enable">
            {{ busy ? t('plans.driver.saving') : t('plans.driver.saveBudget') }}
          </button>
          <button class="op-btn pd-disable" type="button" data-test="plan-driver-disable" :disabled="busy || !canWrite" :title="writeBlock || undefined" @click="disable">
            {{ busy ? t('plans.driver.disabling') : t('plans.driver.disable') }}
          </button>
        </template>
        <button v-if="formOpen && enabled" class="op-btn" type="button" :disabled="busy" @click="formOpen = false">
          {{ t('plans.driver.close') }}
        </button>
      </div>
      <p v-if="writeBlock" class="mono pd-note pd-note--dim" data-test="plan-driver-gate">{{ writeBlock }}</p>
      <p v-if="formError" class="error mono" data-test="plan-driver-error">{{ formError }}</p>

      <!-- 一致率汇总：总数 / 未观察 / 观察窗，然后按动作、按规则两张表。 -->
      <p v-if="reportError" class="error mono" data-test="plan-driver-report-error">{{ reportError }}</p>
      <template v-else-if="report">
        <p class="mono pd-summary" data-test="plan-driver-summary">
          {{ t('plans.driver.summary', { total, pending, hours: windowHours }) }}
        </p>
        <p v-if="total === 0" class="mono pd-note pd-note--dim" data-test="plan-driver-empty">{{ t('plans.driver.emptyHint') }}</p>
        <template v-else>
          <h3 class="pd-sub mono">{{ t('plans.driver.byAction') }}</h3>
          <div class="pd-table" data-test="plan-driver-by-action">
            <div class="pd-head mono">
              <span>{{ t('plans.driver.colAction') }}</span>
              <span>{{ t('plans.driver.colTotal') }}</span>
              <span>{{ t('plans.driver.colMatch') }}</span>
              <span>{{ t('plans.driver.colMismatch') }}</span>
              <span>{{ t('plans.driver.colNoFollowup') }}</span>
              <span>{{ t('plans.driver.colPending') }}</span>
              <span>{{ t('plans.driver.colRate') }}</span>
              <span>{{ t('plans.driver.colRecent') }}</span>
            </div>
            <div
              v-for="row in byAction"
              :key="row.action"
              class="pd-trow mono"
              :class="{ 'pd-trow--upgrade': row.upgrade_suggested }"
              :data-test="`plan-driver-action-${row.action}`"
            >
              <span class="pd-act">{{ driverActionLabel(row.action) }}</span>
              <span>{{ row.total }}</span>
              <span>{{ row.match }}</span>
              <span>{{ row.mismatch }}</span>
              <span>{{ row.no_followup }}</span>
              <span>{{ row.pending }}</span>
              <span>{{ driverRateText(row) }}</span>
              <span class="pd-recent">{{ driverRecentText(row) }}</span>
              <span v-if="row.upgrade_suggested" class="pd-upgrade" data-test="plan-driver-upgrade">{{ t('plans.driver.upgradeBadge') }}</span>
              <span class="pd-hint">{{ driverUpgradeHint(row) }}</span>
            </div>
          </div>

          <h3 class="pd-sub mono">{{ t('plans.driver.byRule') }}</h3>
          <div class="pd-table pd-table--rule" data-test="plan-driver-by-rule">
            <div class="pd-head mono">
              <span>{{ t('plans.driver.colAction') }}</span>
              <span>{{ t('plans.driver.colRule') }}</span>
              <span>{{ t('plans.driver.colTotal') }}</span>
              <span>{{ t('plans.driver.colMatch') }}</span>
              <span>{{ t('plans.driver.colMismatch') }}</span>
              <span>{{ t('plans.driver.colNoFollowup') }}</span>
              <span>{{ t('plans.driver.colPending') }}</span>
              <span>{{ t('plans.driver.colRate') }}</span>
            </div>
            <div v-for="row in byRule" :key="`${row.action}:${row.rule}`" class="pd-trow mono">
              <span class="pd-act">{{ driverActionLabel(row.action) }}</span>
              <span class="pd-rule" :title="row.rule">{{ driverRuleLabel(row.rule) }}</span>
              <span>{{ row.total }}</span>
              <span>{{ row.match }}</span>
              <span>{{ row.mismatch }}</span>
              <span>{{ row.no_followup }}</span>
              <span>{{ row.pending }}</span>
              <span>{{ driverRateText(row) }}</span>
            </div>
          </div>
        </template>

        <!-- 动作明细（计划 R12）：引擎记录的每一条「本来会做什么」，最新的在上（服务端已截到
             100 条）。点一行展开 params；todo 点开详情页的待办抽屉，job 链到 /jobs/{id}。 -->
        <h3 class="pd-sub mono">{{ t('plans.driver.recentTitle') }}</h3>
        <p v-if="recentRows.length === 0" class="mono pd-note pd-note--dim" data-test="plan-driver-recent-empty">
          {{ t('plans.driver.recentEmptyHint') }}
        </p>
        <template v-else>
          <p class="mono pd-note pd-note--dim" data-test="plan-driver-recent-hint">
            {{ t('plans.driver.recentHint', { n: recentRows.length }) }}
          </p>
          <div class="pd-table pd-table--recent" data-test="plan-driver-recent">
            <div class="pd-head mono">
              <span>{{ t('plans.driver.colTime') }}</span>
              <span>{{ t('plans.driver.colTodo') }}</span>
              <span>{{ t('plans.driver.colJob') }}</span>
              <span>{{ t('plans.driver.colAction') }}</span>
              <span>{{ t('plans.driver.colRule') }}</span>
              <span>{{ t('plans.driver.colRisk') }}</span>
              <span>{{ t('plans.driver.colWouldPause') }}</span>
              <span>{{ t('plans.driver.colOutcome') }}</span>
            </div>
            <template v-for="row in recentRows" :key="row.id">
              <div
                class="pd-trow pd-trow--recent mono"
                :class="{ 'pd-trow--open': expanded.has(row.id) }"
                data-test="plan-driver-recent-row"
                @click="toggleRow(row.id)"
              >
                <span class="pd-time">{{ fmtDateTime(row.created_at) }}</span>
                <span>
                  <button
                    v-if="row.todo_id"
                    class="pd-link mono"
                    type="button"
                    data-test="plan-driver-recent-todo"
                    @click.stop="openTodo(row.todo_id)"
                  >{{ row.todo_id }}</button>
                  <span v-else class="pd-dim">—</span>
                </span>
                <span>
                  <RouterLink
                    v-if="row.job_id"
                    class="pd-link mono"
                    :to="`/jobs/${encodeURIComponent(row.job_id)}`"
                    data-test="plan-driver-recent-job"
                    @click.stop
                  >{{ row.job_id }}</RouterLink>
                  <span v-else class="pd-dim">—</span>
                </span>
                <span class="pd-act">{{ driverActionLabel(row.action) }}</span>
                <span class="pd-rule" :title="row.rule">{{ driverRuleLabel(row.rule) }}</span>
                <span>
                  <span
                    class="pd-risk"
                    :class="`pd-risk--${driverRiskTone(row.risk)}`"
                    data-test="plan-driver-recent-risk"
                  >{{ driverRiskLabel(row.risk) }}</span>
                </span>
                <span class="pd-pause" :class="{ 'pd-pause--on': row.would_pause }" data-test="plan-driver-recent-pause">
                  {{ driverWouldPauseText(row.would_pause) }}
                </span>
                <span class="pd-outcome" data-test="plan-driver-recent-outcome">{{ driverOutcomeLabel(row.outcome) }}</span>
              </div>
              <pre v-if="expanded.has(row.id)" class="pd-params mono" data-test="plan-driver-recent-params">{{ driverParamsText(row.params) || '—' }}</pre>
            </template>
          </div>
        </template>
      </template>
    </template>
    <p v-else class="mono pd-note">{{ t('plans.driver.loading') }}</p>
  </section>
</template>

<style scoped>
/* JobDetail / PlanDetail 的 section 规则是 scoped 的，进不到子组件里，这里重复一份同样的外观。 */
.section { margin-top: 18px; padding: 12px 14px 14px; border: 1px solid var(--line); border-radius: var(--r-chip); background: var(--surface); }
.section-head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.section-title { font-size: 12px; letter-spacing: 0.08em; color: var(--ink-3); text-transform: uppercase; margin: 0; }
.pd-level { font-size: 11px; padding: 1px 7px; border: 1px solid var(--accent); border-radius: 999px; color: var(--accent); }
.pd-since { font-size: 11px; color: var(--ink-3); }
.pd-refresh { margin-left: auto; }
.pd-body { margin-top: 10px; }
.pd-note { margin: 8px 0 0; font-size: 12px; color: var(--ink-2); }
.pd-note--dim { color: var(--ink-3); }
.pd-row-line { margin: 4px 0; font-size: 12px; color: var(--ink); }
.pd-form { display: flex; gap: 10px; flex-wrap: wrap; align-items: flex-end; margin-top: 10px; }
.pd-field { display: flex; flex-direction: column; gap: 4px; font-size: 11px; color: var(--ink-3); }
.pd-form-hint { flex: 1 1 100%; margin: 0; }
.op-input { min-width: 0; width: 110px; background: var(--surface); color: var(--ink); border: 1px solid var(--line); border-radius: var(--r-chip); padding: 5px 8px; font-size: 12px; outline: none; }
.op-input:focus { border-color: var(--accent); }
.pd-actions { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-top: 10px; }
.op-btn { background: transparent; color: var(--accent); border: 1px solid var(--line); border-radius: var(--r-chip); padding: 4px 10px; font-size: 12px; cursor: pointer; }
.op-btn:hover:not(:disabled) { border-color: var(--accent); }
.op-btn:disabled { opacity: 0.55; cursor: default; }
.error { color: var(--fail); font-size: 12px; word-break: break-word; margin: 8px 0 0; }
.pd-summary { margin: 12px 0 0; font-size: 12px; color: var(--ink-2); }
.pd-sub { margin: 14px 0 6px; font-size: 11px; letter-spacing: 0.06em; color: var(--ink-3); text-transform: uppercase; }
.pd-table { display: flex; flex-direction: column; border: 1px solid var(--line); border-radius: var(--r-chip); overflow: hidden; }
.pd-head, .pd-trow {
  display: grid;
  grid-template-columns: minmax(120px, 1.4fr) repeat(5, minmax(44px, 0.6fr)) minmax(70px, 0.8fr) minmax(120px, 1.4fr);
  gap: 8px;
  padding: 6px 10px;
  align-items: center;
  font-size: 12px;
}
.pd-table--rule .pd-head, .pd-table--rule .pd-trow {
  grid-template-columns: minmax(110px, 1.2fr) minmax(140px, 1.6fr) repeat(5, minmax(44px, 0.6fr)) minmax(56px, 0.7fr);
}
.pd-head { background: var(--surface-2, transparent); color: var(--ink-3); font-size: 11px; border-bottom: 1px solid var(--line); }
.pd-trow { border-bottom: 1px solid var(--line); }
.pd-trow:last-child { border-bottom: none; }
.pd-trow--upgrade { background: color-mix(in srgb, var(--accent) 8%, transparent); }
.pd-act { color: var(--ink); }
.pd-rule { color: var(--ink-2); overflow-wrap: anywhere; }
.pd-recent { color: var(--ink-2); }
.pd-upgrade { grid-column: 1 / -1; justify-self: start; font-size: 11px; padding: 1px 7px; border-radius: 999px; border: 1px solid var(--accent); color: var(--accent); }
.pd-hint { grid-column: 1 / -1; font-size: 11px; color: var(--ink-3); }
/* 动作明细：时间 / todo / job / 动作 / 规则 / 风险 / 本会暂停 / 结果，一共八列。 */
.pd-table--recent .pd-head, .pd-table--recent .pd-trow {
  grid-template-columns:
    minmax(96px, 0.9fr) minmax(0, 0.8fr) minmax(0, 0.9fr) minmax(96px, 1.1fr)
    minmax(100px, 1.2fr) minmax(52px, 0.5fr) minmax(58px, 0.55fr) minmax(64px, 0.7fr);
}
/* 整行可点：展开 / 收起这一条的 params。 */
.pd-trow--recent { cursor: pointer; }
.pd-trow--recent:hover { background: color-mix(in srgb, var(--accent) 6%, transparent); }
.pd-trow--open { background: color-mix(in srgb, var(--accent) 10%, transparent); }
.pd-time { color: var(--ink-2); }
.pd-dim { color: var(--ink-3); }
.pd-link { background: none; border: none; padding: 0; font: inherit; text-align: left; color: var(--accent); cursor: pointer; overflow-wrap: anywhere; }
.pd-link:hover { text-decoration: underline; }
/* 风险徽标：低 = 正常、中 = 需要看一眼、红线 = 不做（与后端风险表同口径）。 */
.pd-risk { font-size: 11px; padding: 1px 7px; border-radius: 999px; border: 1px solid var(--line); color: var(--ink-2); }
.pd-risk--low { color: var(--done); border-color: var(--done); }
.pd-risk--medium { color: var(--need); border-color: var(--need); }
.pd-risk--redline { color: var(--fail); border-color: var(--fail); background: var(--fail-soft); }
.pd-pause { color: var(--ink-3); }
.pd-pause--on { color: var(--need); }
.pd-outcome { color: var(--ink-2); }
/* 展开的 params：等宽体、自动换行，长 JSON 不把表格撑宽。 */
.pd-params {
  margin: 0;
  padding: 8px 10px 10px;
  border-bottom: 1px solid var(--line);
  background: var(--sunken);
  color: var(--ink-2);
  font-size: 11px;
  line-height: 1.5;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
@media (max-width: 760px) {
  .pd-head { display: none; }
  .pd-trow { grid-template-columns: repeat(2, minmax(0, 1fr)); row-gap: 4px; }
  .pd-table--rule .pd-trow { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .pd-table--recent .pd-trow { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
</style>
