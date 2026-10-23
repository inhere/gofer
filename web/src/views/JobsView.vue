<script setup lang="ts">
// 任务详情宿主（/jobs/:id，设计稿 §7.1、§16）：主内容区够宽（≥1280）时有「列表 + 详情」并排，
// 否则只有详情本身（列表占满、点开进完整详情，用 BackLink 回到列表并保留筛选与滚动位置）。
// 「单栏 / 并排」选择记在本地（store/jobsSplit.ts），宽度判断在 utils/jobsLayout.ts。
// 两个方向的开关各在一处：并排时「单栏」在列表头（Board），单栏且宽度够时「并排」在这里，
// 否则选了单栏就再也回不到并排。
import { computed } from 'vue'
import { t } from '../i18n'
import { DETAIL_MIN_WIDTH, splitAllowed, splitLayout } from '../utils/jobsLayout'
import { useElementWidth } from '../utils/useElementWidth'
import { jobsSplitPreference, setJobsSplitPreference } from '../store/jobsSplit'
import Board from './Board.vue'
import JobDetail from './JobDetail.vue'

const props = defineProps<{ id: string }>()

const { el, width } = useElementWidth()
const split = computed(() => splitLayout(width.value, jobsSplitPreference.value))
// 宽度够、但用户选过「单栏」：给一个回「并排」的入口。
const canSplit = computed(() => !split.value && splitAllowed(width.value))
</script>

<template>
  <div
    ref="el"
    class="jobs-view"
    :data-layout="split ? 'split' : 'single'"
    :style="split ? { '--detail-min': `${DETAIL_MIN_WIDTH}px` } : undefined"
    data-test="jobs-view"
  >
    <div v-if="split" class="jobs-view-list" data-test="jobs-view-list">
      <Board embedded />
    </div>
    <div class="jobs-view-detail" data-test="jobs-view-detail">
      <div v-if="canSplit" class="jobs-view-switch">
        <button
          class="jobs-view-switch-btn mono"
          type="button"
          data-test="jobs-split-enable"
          :title="t('jobs.h.splitTitle')"
          @click="setJobsSplitPreference('split')"
        >
          {{ t('jobs.h.split') }}
        </button>
      </div>
      <JobDetail :id="props.id" :embedded="split" />
    </div>
  </div>
</template>

<style scoped>
.jobs-view {
  min-width: 0;
}
/* 并排：左列列表自适应，右列详情不窄于 760px（宽度不够时 utils/jobsLayout 已经判成单栏）。 */
.jobs-view[data-layout='split'] {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(var(--detail-min, 760px), 1.1fr);
  gap: var(--s4);
  align-items: start;
}
.jobs-view-list,
.jobs-view-detail {
  min-width: 0;
}
.jobs-view-switch {
  display: flex;
  justify-content: flex-end;
  margin-bottom: var(--s2);
}
.jobs-view-switch-btn {
  min-height: 32px;
  padding: 0 var(--s3);
  background: none;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  color: var(--ink-2);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.jobs-view-switch-btn:hover {
  color: var(--ink);
  border-color: var(--accent);
}
.jobs-view-switch-btn:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
@media (max-width: 760px) {
  .jobs-view[data-layout='split'] {
    display: block;
  }
  .jobs-view-switch-btn {
    min-height: 40px;
  }
}
</style>
