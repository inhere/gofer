<script setup lang="ts">
// 今天页「进行中的计划」块（设计稿 2026-11-01 §4.3）：每个 open 计划一行——标题、done/total、
// 进度条，整行链到计划页；没有进行中的计划时整块不出现（健康状态不占地方）。
// 数据读 GET /v1/plans?status=open；今天页每次重拉（推送 / 兜底轮询）时跟着刷新一次。
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { listPlans } from '../../api/client'
import type { Plan } from '../../api/types'
import { todayData } from '../../store/today'
import { todayPlanRows } from '../../utils/todayPlans'
import { t } from '../../i18n'

const plans = ref<Plan[]>([])

async function load(): Promise<void> {
  try {
    plans.value = (await listPlans({ status: 'open', limit: 100 })).plans
  } catch {
    // 拉不到就不显示这一块，今天页其余部分不受影响
    plans.value = []
  }
}

const model = computed(() => todayPlanRows(plans.value))

onMounted(() => void load())
watch(() => todayData.value?.generated_at, () => void load())
</script>

<template>
  <section v-if="model.rows.length" class="tp" data-test="today-plans">
    <h2 class="tp-h mono">{{ t('today.plans.title') }}</h2>
    <RouterLink v-for="r in model.rows" :key="r.id" :to="r.to" class="tp-row" data-test="today-plan-row">
      <span class="tp-name">{{ r.title }}</span>
      <span class="tp-n mono">{{ r.done }}/{{ r.total }}</span>
      <span class="tp-bar" aria-hidden="true"><i :style="{ width: `${r.percent ?? 0}%` }"></i></span>
    </RouterLink>
    <RouterLink v-if="model.more > 0" to="/plans" class="tp-more mono" data-test="today-plans-more">
      {{ t('today.plans.more', { n: model.more }) }}
    </RouterLink>
  </section>
</template>

<style scoped>
.tp {
  display: flex;
  flex-direction: column;
  gap: var(--s1);
  margin: 0 0 var(--s4);
}
.tp-h {
  margin: 0 0 var(--s1);
  color: var(--ink-3);
  font-size: var(--fs-xs);
  font-weight: 500;
}
.tp-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto 96px;
  align-items: center;
  gap: var(--s3);
  padding: var(--s1) 0;
  color: var(--ink);
  font-size: var(--fs-sm);
  text-decoration: none;
}
.tp-row:hover .tp-name {
  color: var(--accent);
}
.tp-name {
  min-width: 0;
  overflow-wrap: anywhere;
}
.tp-n {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  font-variant-numeric: tabular-nums;
}
.tp-bar {
  height: 4px;
  background: var(--line);
  border-radius: 2px;
  overflow: hidden;
}
.tp-bar i {
  display: block;
  height: 100%;
  background: var(--accent);
}
.tp-more {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  text-decoration: underline;
}
</style>
