<script setup lang="ts">
// 「今天」首页右栏「并行中」区（N3 §3、UI 重设计 §7.1）：一行一条并行的工作项 / plan，
// 一眼看完全部 agent 在干什么、谁停了、谁出问题。自带数据：GET /v1/today/lanes，订阅
// work / plans / jobs / sessions 推送（防抖 1s 重拉；断线回落 30s 轮询，沿用 createLiveTopic）。
// 点行：工作项打开 Works 的工作项抽屉（/work?id=，复用 WorkDrawer），plan 跳 plan 详情。
// 拉到的数据同时 emit 给页面（首屏一行摘要里的 agent 在跑 / 空闲会话数从泳道的 agents 算）。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { getTodayLanes, type TodayLane, type TodayLanesResp } from '../../api/todayLanes'
import { createLiveTopic } from '../../utils/useLiveTopic'
import { laneSummaryText, laneTarget, splitLanes } from '../../utils/todayLanes'
import { t } from '../../i18n'
import TodayLaneRow from './TodayLaneRow.vue'

const emit = defineEmits<{ (e: 'loaded', data: TodayLanesResp): void }>()
const router = useRouter()

const data = ref<TodayLanesResp | null>(null)
const loading = ref(false)
const error = ref('')
const expanded = ref(false)

async function load(): Promise<void> {
  if (!data.value) loading.value = true
  try {
    data.value = await getTodayLanes()
    emit('loaded', data.value)
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

const lanes = computed<TodayLane[]>(() => data.value?.lanes ?? [])
const summary = computed(() => laneSummaryText(data.value?.summary ?? { total: 0, agents_running: 0, attention: 0 }))
const split = computed(() => splitLanes(lanes.value))
const visible = computed(() => (expanded.value ? lanes.value : split.value.shown))

function open(l: TodayLane): void {
  void router.push(laneTarget(l))
}

// 四个主题的推送（以及断线后的兜底轮询）合并成一次防抖 1s 的重拉：一个 job 结束往往同时
// 触发 jobs / work / plans，只拉一次。
const LANE_TOPICS = ['work', 'plans', 'jobs', 'sessions'] as const
const LIVE_DEBOUNCE_MS = 1000
let reloadTimer: ReturnType<typeof setTimeout> | null = null
function scheduleLoad(): void {
  if (reloadTimer) return
  reloadTimer = setTimeout(() => {
    reloadTimer = null
    void load()
  }, LIVE_DEBOUNCE_MS)
}
const topics = LANE_TOPICS.map((topic) => createLiveTopic(topic, { fetch: scheduleLoad, initial: false, debounceMs: 0 }))
onMounted(() => {
  void load()
  topics.forEach((t) => t.start())
})
onUnmounted(() => {
  topics.forEach((t) => t.stop())
  if (reloadTimer) clearTimeout(reloadTimer)
  reloadTimer = null
})

defineExpose({ reload: load })
</script>

<template>
  <section class="today-lanes" aria-labelledby="today-lanes-h" data-test="today-lanes">
    <h2 id="today-lanes-h">
      {{ t('today.lanes') }}
      <span class="muted">{{ t('today.lanes.meta', { n: lanes.length }) }}</span>
    </h2>
    <p class="meta mono" data-test="lanes-summary">
      {{ summary.main }}<template v-if="summary.attention"> · <span class="bad" data-test="lanes-attention">{{ summary.attention }}</span></template>
    </p>
    <p v-if="error" class="msg mono" data-test="lanes-error">{{ error }}</p>
    <p v-else-if="loading && !data" class="empty mono">{{ t('today.lanes.loading') }}</p>
    <p v-else-if="!lanes.length" class="empty mono" data-test="lanes-empty">{{ t('today.lanes.empty') }}</p>
    <div v-else class="lanes">
      <TodayLaneRow v-for="l in visible" :key="`${l.kind}:${l.id}`" :lane="l" @open="open" />
      <button
        v-if="split.folded.length"
        type="button"
        class="fold mono"
        :aria-expanded="expanded"
        data-test="lanes-fold"
        @click="expanded = !expanded"
      >{{ expanded ? t('today.lanes.unfold') : t('today.lanes.fold', { n: split.folded.length }) }}</button>
    </div>
  </section>
</template>

<style scoped>
.today-lanes { display: flex; flex-direction: column; gap: var(--s1); min-width: 0; margin-bottom: var(--s5); }
.today-lanes h2 { display: flex; align-items: center; gap: var(--s2); margin: 0 0 var(--s1); font-size: var(--fs-md); font-weight: 600; }
.today-lanes h2 .muted { margin-left: auto; color: var(--fg-3); font-size: var(--fs-sm); font-weight: 400; }
.meta { font-size: var(--fs-sm); color: var(--fg-3); }
.bad { color: var(--fail); }
.lanes { border: 1px solid var(--line); border-radius: var(--r-panel); background: var(--surface); overflow: hidden; min-width: 0; }
.fold {
  display: block;
  width: 100%;
  min-height: 36px;
  padding: var(--s1) var(--s3);
  border: 0;
  border-top: 1px solid var(--line);
  background: transparent;
  color: var(--fg-3);
  font: inherit;
  font-size: var(--fs-sm);
  text-align: left;
  cursor: pointer;
}
.fold:hover, .fold:focus-visible { color: var(--accent); }
.empty, .msg {
  margin: 0;
  padding: var(--s3) var(--s3);
  font-size: var(--fs-sm);
  color: var(--fg-3);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
}
.msg { color: var(--fail); border-color: var(--fail); }
</style>
