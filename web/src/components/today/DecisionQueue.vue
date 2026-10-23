<script setup lang="ts">
// N3 决策队列：/today 页（前 5 张 + 「还有 N 张」）与全局浮层（全部）共用。
// 卡片已由后端排好序；这里只按「会超时 / 卡住别人 / 可稍后」切组。
import { computed } from 'vue'
import DecisionCard from './DecisionCard.vue'
import type { TodayAction, TodayCard } from '../../api/today'
import type { SnoozeOption } from '../../utils/todaySnooze'
import { groupByTier } from '../../utils/today'
import { t } from '../../i18n'
import { ceilingReadOnlyNotice } from '../../utils/ceiling'
import { useWhoami } from '../../store/whoami'

const props = defineProps<{ cards: TodayCard[]; nowSec: number; limit?: number; emptyText?: string }>()
const emit = defineEmits<{
  (e: 'act', card: TodayCard, action: TodayAction, text: string, viaAdvice: boolean): void
  (e: 'more'): void
  (e: 'focus'): void
  (e: 'snooze', card: TodayCard, opt: SnoozeOption): void
  (e: 'navigate'): void
}>()

const shown = computed(() => (props.limit && props.limit > 0 ? props.cards.slice(0, props.limit) : props.cards))
const rest = computed(() => props.cards.length - shown.value.length)
const groups = computed(() => groupByTier(shown.value))
// 只读身份把「专注处理」灰显（整页都在做写操作，服务端也会 403）——gofer-darr。
const { readOnly } = useWhoami()
</script>

<template>
  <div class="dq" data-test="decision-queue">
    <p v-if="cards.length === 0" class="dq-empty mono">{{ emptyText || t('today.queue.empty', { n: 0 }) }}</p>
    <template v-for="g in groups" :key="g.tier">
      <div class="dq-tier mono" :data-tier="g.tier">{{ t(`today.tier.${g.tier}`) }}</div>
      <DecisionCard
        v-for="c in g.cards"
        :key="c.key"
        :card="c"
        :now-sec="nowSec"
        @act="(a, text, adv) => emit('act', c, a, text, adv)"
        @navigate="emit('navigate')"
        @snooze="(o) => emit('snooze', c, o)"
      />
    </template>
    <div v-if="rest > 0" class="dq-rest mono">
      <button class="dq-more" type="button" data-test="dq-more" @click="emit('more')">
        {{ t('today.queue.rest', { n: rest }) }}
      </button>
      <button class="dq-focus" type="button" data-test="dq-focus" :disabled="readOnly" :title="readOnly ? ceilingReadOnlyNotice() : undefined" @click="emit('focus')">
        {{ t('today.focus') }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.dq {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  min-width: 0;
}
.dq-tier {
  margin-top: var(--s1);
  color: var(--ink-3);
  font-size: var(--fs-xs);
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
  padding: var(--s5) var(--s4);
  color: var(--ink-3);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  font-size: var(--fs-sm);
  text-align: center;
}
.dq-rest {
  display: flex;
  flex-wrap: wrap;
  gap: var(--s1);
}
.dq-focus {
  padding: var(--s1) var(--s3);
  color: var(--bg);
  background: var(--accent);
  border: 1px solid var(--accent);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  font-weight: 600;
  cursor: pointer;
}
.dq-focus:disabled {
  color: var(--ink-3);
  background: transparent;
  border-color: var(--line);
  cursor: default;
}
.dq-focus:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.dq-more {
  flex: 1 1 auto;
  min-width: 0;
  padding: var(--s1) var(--s3);
  color: var(--accent);
  background: transparent;
  border: 1px dashed var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  text-align: left;
  cursor: pointer;
}
.dq-more:hover {
  border-color: var(--accent);
}
@media (max-width: 760px) {
  .dq-more,
  .dq-focus {
    min-height: 40px;
  }
}
</style>
