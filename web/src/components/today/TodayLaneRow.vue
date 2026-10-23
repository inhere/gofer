<script setup lang="ts">
// 「并行中」的一行（gofer-hez1 起两行排布）：
//   第一行 = 状态点 + 标题（超出省略号，title 悬停全文）+ 项目；
//   第二行 = agent · 进度（todo 点阵 + 当前步骤，或最近里程碑）· 健康度 · 用时。
// 右栏只有 ~340px，原来把 6 列挤在一行里，标题只剩 4–5 个字、红色「停滞」压在点阵上；
// 两行后标题独占整行（约 20 字），点阵固定宽度，健康度与用时各占原位、互不重叠。
// 整行可点、可 Enter 打开。
import { computed } from 'vue'
import type { TodayLane } from '../../api/todayLanes'
import { agentText, dotTone, elapsedText, healthText, laneAriaLabel, pipsView } from '../../utils/todayLanes'
import { t } from '../../i18n'

const props = defineProps<{ lane: TodayLane }>()
const emit = defineEmits<{ (e: 'open', lane: TodayLane): void }>()

const agent = computed(() => agentText(props.lane.agents ?? []))
const pips = computed(() => pipsView(props.lane.progress?.pips))
const health = computed(() => healthText(props.lane.health))
const project = computed(() => props.lane.project_key || (props.lane.kind === 'plan' ? 'plan' : '—'))

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
      <span class="proj mono" :title="project">{{ project }}</span>
    </span>
    <span class="sub">
      <span class="ag mono" :class="agent.state && `ag--${agent.state}`" data-test="lane-agent">{{ agent.text }}</span>
      <span class="pg">
        <span v-if="pips.shown.length" class="pips" aria-hidden="true">
          <i v-for="p in pips.shown" :key="p.todo_id" :class="`pip--${p.status}`" :title="p.title" />
          <em v-if="pips.more" class="mono">+{{ pips.more }}</em>
        </span>
        <span class="cur" :title="lane.progress?.current">{{ lane.progress?.current || '—' }}</span>
      </span>
      <span class="hl mono" :class="`hl--${lane.health}`" :title="lane.health_reason" data-test="lane-health">{{ health }}</span>
      <span class="num mono" :title="t('today.lane.startedAt', { time: new Date(lane.started_at * 1000).toLocaleString() })">{{ elapsedText(lane.elapsed_sec) }}</span>
    </span>
  </div>
</template>

<style scoped>
.lane {
  display: grid;
  grid-template-columns: 10px minmax(0, 1fr);
  grid-template-areas:
    'd t'
    's s';
  gap: 2px 10px;
  align-items: start;
  padding: 9px 14px;
  border-top: 1px solid var(--line);
  cursor: pointer;
  font-size: 13px;
  min-width: 0;
}
.lane:first-child { border-top: 0; }
.lane:hover, .lane:focus-visible { background: var(--bg); outline: none; }
.lane:focus-visible { box-shadow: inset 2px 0 0 var(--accent); }
.dot { grid-area: d; width: 8px; height: 8px; border-radius: 50%; background: var(--ink-3); margin-top: 6px; }
.dot--active { background: var(--accent); }
.dot--needs_me { background: var(--run); }
.dot--review { background: var(--done); }
.dot--blocked { background: var(--fail); }
.t { grid-area: t; min-width: 0; display: flex; flex-direction: column; gap: 1px; }
.t b { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); }
.t .proj { font-size: 11px; color: var(--ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 第二行：agent / 进度 / 健康度 / 用时各占一块，点阵固定宽度，永不叠字 */
.sub {
  grid-area: s;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 2px 10px;
  min-width: 0;
}
.ag { flex: 0 1 auto; max-width: 45%; font-size: 12px; color: var(--ink-3); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ag--running { color: var(--accent); }
.ag--awaiting_input { color: var(--run); }
.pg { flex: 1 1 96px; min-width: 0; display: flex; gap: 6px; align-items: center; font-size: 12.5px; }
.cur { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; opacity: 0.86; color: var(--ink); }
.pips { display: inline-flex; gap: 3px; flex: 0 0 auto; align-items: center; }
.pips i { width: 8px; height: 8px; border-radius: 2px; border: 1px solid var(--line); display: inline-block; }
.pips em { font-style: normal; font-size: 10px; color: var(--ink-3); }
.pips i.pip--done { background: var(--done); border-color: var(--done); }
.pips i.pip--running { background: var(--accent); border-color: var(--accent); }
.pips i.pip--needs_review { background: var(--run); border-color: var(--run); }
.pips i.pip--failed { background: var(--fail); border-color: var(--fail); }
.hl { flex: 0 0 auto; font-size: 11.5px; white-space: nowrap; }
.hl--blocked, .hl--stalled { color: var(--fail); }
.hl--at_risk { color: var(--run); }
.num { flex: 0 0 auto; font-size: 12px; color: var(--ink-3); text-align: right; font-variant-numeric: tabular-nums; white-space: nowrap; }

/* 手机：两行照旧，只收窄间距；不会横向滚动 */
@media (max-width: 760px) {
  .lane { padding: 9px 12px; gap: 3px 8px; }
  .sub { gap: 2px 8px; }
  .ag { max-width: 60%; }
}
</style>
