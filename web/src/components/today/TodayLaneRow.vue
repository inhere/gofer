<script setup lang="ts">
// 「并行中」的一行：状态点 / 标题与项目 / agent·状态 / 进度（todo 点阵 + 当前步骤，或最近里程碑）
// / 健康度（正常不显示）/ 用时。整行可点、可 Enter 打开。
import { computed } from 'vue'
import type { TodayLane } from '../../api/todayLanes'
import { agentText, dotTone, elapsedText, healthText, laneAriaLabel, pipsView } from '../../utils/todayLanes'

const props = defineProps<{ lane: TodayLane }>()
const emit = defineEmits<{ (e: 'open', lane: TodayLane): void }>()

const agent = computed(() => agentText(props.lane.agents ?? []))
const pips = computed(() => pipsView(props.lane.progress?.pips))
const health = computed(() => healthText(props.lane.health))

function open(): void {
  emit('open', props.lane)
}
</script>

<template>
  <div
    class="lane"
    role="button"
    tabindex="0"
    :aria-label="laneAriaLabel(lane)"
    :data-test="`lane-${lane.id}`"
    @click="open"
    @keydown.enter.prevent="open"
  >
    <span class="dot" :class="`dot--${dotTone(lane)}`" aria-hidden="true" />
    <span class="t">
      <b :title="lane.title">{{ lane.title }}</b>
      <span class="mono">{{ lane.project_key || (lane.kind === 'plan' ? 'plan' : '—') }}</span>
    </span>
    <span class="ag mono" :class="agent.state && `ag--${agent.state}`" data-test="lane-agent">{{ agent.text }}</span>
    <span class="pg">
      <span v-if="pips.shown.length" class="pips" aria-hidden="true">
        <i v-for="p in pips.shown" :key="p.todo_id" :class="`pip--${p.status}`" :title="p.title" />
        <em v-if="pips.more" class="mono">+{{ pips.more }}</em>
      </span>
      <span class="cur" :title="lane.progress?.current">{{ lane.progress?.current || '—' }}</span>
    </span>
    <span class="hl mono" :class="`hl--${lane.health}`" :title="lane.health_reason" data-test="lane-health">{{ health }}</span>
    <span class="num mono" :title="`开始于 ${new Date(lane.started_at * 1000).toLocaleString()}`">{{ elapsedText(lane.elapsed_sec) }}</span>
  </div>
</template>

<style scoped>
.lane {
  display: grid;
  grid-template-columns: 10px minmax(0, 1.5fr) minmax(0, 1fr) minmax(0, 2fr) 56px 52px;
  gap: 12px;
  align-items: center;
  padding: 9px 14px;
  border-top: 1px solid var(--line);
  cursor: pointer;
  font-size: 13px;
  min-width: 0;
}
.lane:first-child { border-top: 0; }
.lane:hover, .lane:focus-visible { background: var(--ink); outline: none; }
.lane:focus-visible { box-shadow: inset 2px 0 0 var(--phosphor); }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--queue); }
.dot--active { background: var(--phosphor); }
.dot--needs_me { background: var(--run); }
.dot--review { background: var(--done); }
.dot--blocked { background: var(--fail); }
.t { min-width: 0; display: flex; flex-direction: column; gap: 1px; }
.t b { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--paper); }
.t span { font-size: 11px; color: var(--queue); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ag { font-size: 12px; color: var(--queue); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ag--running { color: var(--phosphor); }
.ag--awaiting_input { color: var(--run); }
.pg { min-width: 0; display: flex; gap: 8px; align-items: center; font-size: 12.5px; }
.cur { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; opacity: 0.86; color: var(--paper); }
.pips { display: inline-flex; gap: 3px; flex: 0 0 auto; align-items: center; }
.pips i { width: 8px; height: 8px; border-radius: 2px; border: 1px solid var(--line); display: inline-block; }
.pips em { font-style: normal; font-size: 10px; color: var(--queue); }
.pips i.pip--done { background: var(--done); border-color: var(--done); }
.pips i.pip--running { background: var(--phosphor); border-color: var(--phosphor); }
.pips i.pip--needs_review { background: var(--run); border-color: var(--run); }
.pips i.pip--failed { background: var(--fail); border-color: var(--fail); }
.hl { font-size: 11.5px; white-space: nowrap; }
.hl--blocked, .hl--stalled { color: var(--fail); }
.hl--at_risk { color: var(--run); }
.num { font-size: 12px; color: var(--queue); text-align: right; font-variant-numeric: tabular-nums; white-space: nowrap; }

/* 手机：叠成 3 行——标题+用时 / 进度 / agent+健康度；不出横向滚动 */
@media (max-width: 760px) {
  .lane {
    grid-template-columns: 10px minmax(0, 1fr) auto;
    grid-template-areas: 'd t n' '. p p' '. a h';
    row-gap: 4px;
    padding: 9px 12px;
  }
  .dot { grid-area: d; }
  .t { grid-area: t; }
  .pg { grid-area: p; }
  .ag { grid-area: a; }
  .hl { grid-area: h; text-align: right; }
  .num { grid-area: n; }
}
</style>
