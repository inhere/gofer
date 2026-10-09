<script setup lang="ts">
// N3 决策队列：/today 页（前 5 张 + 「还有 N 张」）与全局浮层（全部）共用。
// 卡片已由后端排好序；这里只按「会超时 / 卡住别人 / 可稍后」切组。
import { computed } from 'vue'
import DecisionCard from './DecisionCard.vue'
import type { TodayAction, TodayCard } from '../../api/today'
import { groupByTier } from '../../utils/today'

const props = defineProps<{ cards: TodayCard[]; nowSec: number; limit?: number; emptyText?: string }>()
const emit = defineEmits<{
  (e: 'act', card: TodayCard, action: TodayAction, text: string, viaAdvice: boolean): void
  (e: 'more'): void
  (e: 'navigate'): void
}>()

const shown = computed(() => (props.limit && props.limit > 0 ? props.cards.slice(0, props.limit) : props.cards))
const rest = computed(() => props.cards.length - shown.value.length)
const groups = computed(() => groupByTier(shown.value))
</script>

<template>
  <div class="dq" data-test="decision-queue">
    <p v-if="cards.length === 0" class="dq-empty mono">{{ emptyText || '没有等你的事' }}</p>
    <template v-for="g in groups" :key="g.tier">
      <div class="dq-tier mono" :data-tier="g.tier">{{ g.label }}</div>
      <DecisionCard
        v-for="c in g.cards"
        :key="c.key"
        :card="c"
        :now-sec="nowSec"
        @act="(a, text, adv) => emit('act', c, a, text, adv)"
        @navigate="emit('navigate')"
      />
    </template>
    <button v-if="rest > 0" class="dq-more mono" type="button" data-test="dq-more" @click="emit('more')">
      还有 {{ rest }} 张 · 打开全部
    </button>
  </div>
</template>

<style scoped>
.dq {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}
.dq-tier {
  margin-top: 4px;
  color: var(--queue);
  font-size: 11px;
  letter-spacing: 0.06em;
}
.dq-tier[data-tier='now'] {
  color: var(--fail);
}
.dq-tier[data-tier='block'] {
  color: var(--run);
}
.dq-empty {
  margin: 0;
  padding: 14px;
  color: var(--queue);
  font-size: 12px;
  border: 1px dashed var(--line);
  border-radius: var(--radius);
  text-align: center;
}
.dq-more {
  padding: 8px 12px;
  color: var(--phosphor);
  background: transparent;
  border: 1px dashed var(--line);
  border-radius: var(--radius);
  font-size: 12px;
  text-align: left;
}
.dq-more:hover {
  border-color: var(--phosphor);
}
</style>
