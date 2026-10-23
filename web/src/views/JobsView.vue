<script setup lang="ts">
// 任务详情宿主（/jobs/:id，设计稿 §7.1、§16）：主内容区够宽（≥1280）时有「列表 + 详情」并排，
// 否则只有详情本身（列表占满、点开进完整详情，用 BackLink 回到列表并保留筛选与滚动位置）。
// 「单栏」选择记在本地（store/jobsSplit.ts），宽度判断在 utils/jobsLayout.ts。
import { computed } from 'vue'
import { DETAIL_MIN_WIDTH, splitLayout } from '../utils/jobsLayout'
import { useElementWidth } from '../utils/useElementWidth'
import { jobsSplitPreference } from '../store/jobsSplit'
import Board from './Board.vue'
import JobDetail from './JobDetail.vue'

const props = defineProps<{ id: string }>()

const { el, width } = useElementWidth()
const split = computed(() => splitLayout(width.value, jobsSplitPreference.value))
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
@media (max-width: 760px) {
  .jobs-view[data-layout='split'] {
    display: block;
  }
}
</style>
