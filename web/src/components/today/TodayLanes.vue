<script setup lang="ts">
// 「今天」首页「并行中」区（N3 §3）：一行一条并行的工作项 / plan，一眼看完全部 agent 在干什么、
// 谁停了、谁出问题。自带数据：GET /v1/today/lanes，订阅 work / plans / jobs / sessions 推送
// （防抖 1s 重拉；断线回落 30s 轮询，沿用 createLiveTopic）。点行：工作项打开 Works 的工作项抽屉
// （/work?id=，复用 WorkDrawer），plan 跳 plan 详情。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { getTodayLanes, type TodayLane, type TodayLanesResp } from '../../api/todayLanes'
import { createLiveTopic } from '../../utils/useLiveTopic'
import { laneSummaryText, laneTarget, splitLanes } from '../../utils/todayLanes'
import TodayLaneRow from './TodayLaneRow.vue'

const router = useRouter()

const data = ref<TodayLanesResp | null>(null)
const loading = ref(false)
const error = ref('')
const expanded = ref(false)

async function load(): Promise<void> {
  if (!data.value) loading.value = true
  try {
    data.value = await getTodayLanes()
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
    <div class="sh">
      <h2 id="today-lanes-h">并行中</h2>
      <span class="meta mono" data-test="lanes-summary">
        {{ summary.main }}<template v-if="summary.attention"> · <span class="bad" data-test="lanes-attention">{{ summary.attention }}</span></template>
      </span>
    </div>
    <p v-if="error" class="msg mono" data-test="lanes-error">{{ error }}</p>
    <p v-else-if="loading && !data" class="empty mono">加载中…</p>
    <p v-else-if="!lanes.length" class="empty mono" data-test="lanes-empty">没有并行中的工作</p>
    <div v-else class="lanes">
      <TodayLaneRow v-for="l in visible" :key="`${l.kind}:${l.id}`" :lane="l" @open="open" />
      <button
        v-if="split.folded.length"
        type="button"
        class="fold mono"
        :aria-expanded="expanded"
        data-test="lanes-fold"
        @click="expanded = !expanded"
      >{{ expanded ? '收起正常运行的' : `还有 ${split.folded.length} 条正常运行中` }}</button>
    </div>
  </section>
</template>

<style scoped>
.today-lanes { display: flex; flex-direction: column; gap: 8px; min-width: 0; }
.sh { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; }
.sh h2 { margin: 0; font-size: 15px; color: var(--paper); }
.meta { font-size: 12px; color: var(--queue); }
.bad { color: var(--fail); }
.lanes { border: 1px solid var(--line); border-radius: var(--radius); background: var(--panel); overflow: hidden; min-width: 0; }
.fold {
  display: block;
  width: 100%;
  padding: 8px 14px;
  border: 0;
  border-top: 1px solid var(--line);
  background: transparent;
  color: var(--queue);
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}
.fold:hover, .fold:focus-visible { color: var(--phosphor); }
.empty, .msg {
  margin: 0;
  padding: 12px 14px;
  font-size: 13px;
  color: var(--queue);
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
}
.msg { color: var(--fail); border-color: var(--fail); }
</style>
