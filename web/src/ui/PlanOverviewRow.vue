<script setup lang="ts">
// 计划总览的一行（设计稿 §7「/plans 计划总览」）：状态、标题/id/项目/tags、分段进度、
// 卡点、主会话、最近活动。手机上一行变成一张卡片（CSS 决定，模板同一份）。
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import PlanStatusBadge from '../components/PlanStatusBadge.vue'
import { t } from '../i18n'
import type { Plan } from '../api/types'
import { formatRelative } from '../utils/format'
import { planProgressModel } from './progress'
import ProgressBar from './ProgressBar.vue'

const props = defineProps<{ plan: Plan }>()
const router = useRouter()

const progress = computed(() => planProgressModel(props.plan))

// 卡点列：卡在哪个 todo、多少待验收、多少在跑、多少 job 失败。列表接口直接给
// needs_review / failed_jobs（gofer-rj5e），不用逐个拉详情。
const blockers = computed(() => {
  const plan = props.plan
  const items: Array<{ key: string; text: string; tone: 'fail' | 'doing' | 'need' }> = []
  if (plan.blocked_todo) {
    items.push({ key: 'blocked', text: t('plans.overview.blockedAt', { todo: plan.blocked_todo }), tone: 'fail' })
  }
  const failed = plan.failed_jobs ?? plan.counts?.failed ?? 0
  if (failed > 0) {
    items.push({ key: 'failed', text: t('plans.overview.failed', { n: failed }), tone: 'fail' })
  }
  const review = plan.needs_review ?? 0
  if (review > 0) {
    items.push({ key: 'review', text: t('plans.overview.review', { n: review }), tone: 'need' })
  }
  const doing = plan.todo_counts?.doing ?? 0
  if (doing > 0) {
    items.push({ key: 'doing', text: t('plans.overview.doing', { n: doing }), tone: 'doing' })
  }
  return items
})

const activity = computed(() => formatRelative(props.plan.updated_at || props.plan.created_at))

function open(): void {
  void router.push(`/plans/${encodeURIComponent(props.plan.plan_id)}`)
}
</script>

<template>
  <div
    class="prow"
    role="button"
    tabindex="0"
    data-test="plan-row"
    :data-plan="plan.plan_id"
    @click="open"
    @keydown.enter="open"
    @keydown.space.prevent="open"
  >
    <span class="prow-status"><PlanStatusBadge :status="plan.status" /></span>

    <span class="prow-main">
      <span v-if="plan.title" class="prow-title" :title="plan.title">{{ plan.title }}</span>
      <span class="prow-id mono" :title="plan.plan_id" lang="en">{{ plan.plan_id }}</span>
      <span class="prow-tags">
        <span v-if="plan.project" class="prow-chip" lang="en">{{ plan.project }}</span>
        <span v-if="plan.paused" class="prow-chip prow-chip--paused">{{ t('plans.overview.paused') }}</span>
        <span v-if="plan.leader === 'on'" class="prow-chip prow-chip--leader">{{ t('plans.overview.leaderOn') }}</span>
        <span v-for="tag in plan.tags ?? []" :key="tag" class="prow-chip">{{ tag }}</span>
      </span>
    </span>

    <span class="prow-progress">
      <ProgressBar
        :parts="progress.parts"
        :total="progress.total"
        :done="progress.done"
        :percent="progress.percent"
        size="sm"
        :legend="false"
      />
    </span>

    <span class="prow-blockers" data-test="plan-blockers">
      <span v-if="blockers.length === 0" class="prow-muted">{{ t('plans.overview.clear') }}</span>
      <span v-for="item in blockers" :key="item.key" class="prow-blocker" :class="`prow-blocker--${item.tone}`">
        {{ item.text }}
      </span>
    </span>

    <span class="prow-supervisor mono" data-test="plan-supervisor">
      <span v-if="plan.supervisor_session_id" lang="en" :title="plan.supervisor_session_id">
        {{ plan.supervisor_session_id.length > 12 ? '…' + plan.supervisor_session_id.slice(-12) : plan.supervisor_session_id }}
      </span>
      <span v-else class="prow-muted">—</span>
    </span>

    <span class="prow-activity">{{ activity }}</span>
  </div>
</template>

<style scoped>
.prow {
  display: grid;
  grid-template-columns: 96px minmax(180px, 1.4fr) 190px minmax(140px, 1fr) 120px 96px;
  align-items: center;
  gap: var(--s3);
  padding: var(--s2) var(--s3);
  border-bottom: 1px solid var(--line);
  font-size: var(--fs-sm);
  cursor: pointer;
}
.prow:last-child {
  border-bottom: none;
}
.prow:hover {
  background: var(--surface);
}
.prow:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: -2px;
}
.prow-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.prow-title {
  color: var(--fg);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.prow-id {
  color: var(--fg-3);
  font-size: var(--fs-xs);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.prow-tags {
  display: flex;
  flex-wrap: wrap;
  gap: var(--s1);
}
.prow-chip {
  padding: 0 var(--s1);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  color: var(--fg-3);
  font-size: var(--fs-xs);
}
/* 已暂停 = 人选了不再自动推进；自动推进 = leader 开着。两者都是标识不是告警。 */
.prow-chip--paused {
  color: var(--time);
  border-color: var(--time);
}
.prow-chip--leader {
  color: var(--run);
  border-color: var(--run);
}
.prow-blockers {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.prow-blocker {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.prow-blocker--fail {
  color: var(--fail);
}
.prow-blocker--doing {
  color: var(--run);
}
.prow-blocker--need {
  color: var(--need);
}
.prow-muted {
  color: var(--fg-3);
}
.prow-supervisor {
  color: var(--fg-2);
  font-size: var(--fs-xs);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.prow-activity {
  color: var(--fg-3);
  font-size: var(--fs-xs);
  text-align: right;
  white-space: nowrap;
}
/* 手机：每行变一张卡片（§7 计划总览） */
@media (max-width: 760px) {
  .prow {
    grid-template-columns: 1fr;
    gap: var(--s1);
    padding: var(--s3);
    border: 1px solid var(--line);
    border-radius: var(--r-panel);
    margin-bottom: var(--s2);
  }
  .prow-activity {
    text-align: left;
  }
}
</style>
