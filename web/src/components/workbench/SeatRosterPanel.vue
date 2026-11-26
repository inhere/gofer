<script setup lang="ts">
// 「常驻会话」名册的取数层：拉 GET /v1/seats，在 sessions / jobs 主题变化时重拉（座位的状态由
// 坐在里面的会话推出来，会话或 job 一变它就可能变），「等我」用全页面共用的 useWaiting。
// 渲染与行菜单在 SeatRoster.vue。
// 取数层还负责 `?seat=<id>` 的定位：名册落到手之后才能判断那一行在不在，所以判断放在这里，
// 高亮与滚动交给 SeatRoster（它知道分组与 DOM）。
import { ref, watch } from 'vue'
import type { SeatView } from '../../api/types'
import { listSeats, patchSeat, seatHistory, seatOpenPlan } from '../../api/seats'
import { useLiveTopic } from '../../utils/useLiveTopic'
import { useWaiting } from '../../composables/useWaiting'
import { seatWaitingKey } from './seatRoster'
import SeatRoster from './SeatRoster.vue'

const props = defineProps<{
  project?: string
  // 父组件递增这个数就重拉（会话行上绑定成功后）
  reload?: number
  // 要定位的常驻会话 id（工作台 ?seat=，卡片「打开 seat」带过来的）
  focusSeat?: string
}>()

const emit = defineEmits<{
  // 定位有结果：找到并已高亮（ok），或名册里没有这一行（已归档 / 已删除）
  (e: 'located', result: { seatID: string; ok: boolean }): void
}>()

const seats = ref<SeatView[]>([])
// 当前这份名册是按哪个项目筛选拉到的（null = 还没拉到）：换筛选后旧列表不能用来下结论。
const listProject = ref<string | null>(null)
// 这一次要定位的座位；有值时等名册落地后再判断。
const pendingFocus = ref('')
// 交给 SeatRoster 去滚动 / 高亮的那一行（空 = 不高亮）。
const highlightSeat = ref('')
const waiting = useWaiting()
const waitingKeys = ref<string[]>([])

function syncWaitingKeys(): void {
  const keys = new Set(waiting.items.value.map((item) => item.key))
  waitingKeys.value = seats.value.map((seat) => seatWaitingKey(seat)).filter((key) => key && keys.has(key))
}

async function refresh(): Promise<void> {
  try {
    seats.value = await listSeats({ project: props.project || undefined })
    syncWaitingKeys()
    listProject.value = props.project || ''
  } catch {
    // 名册是附加信息：拉不到就保留上一帧，不占侧栏的错误位
  } finally {
    tryLocate()
  }
}

// tryLocate 定位一个座位：名册里这一行在就高亮（找不到 = 已归档 / 已删除，回一条结果，由
// 工作台给轻提示）。只在对某一次请求有结论时回话，之后的定时重拉不再重复回。
// 名册还按旧的项目筛选拉过时不下结论——定位时筛选已被放开，那一行可能就在新列表里。
function tryLocate(): void {
  const seatID = pendingFocus.value
  if (!seatID || listProject.value === null || listProject.value !== (props.project || '')) return
  pendingFocus.value = ''
  if (!seats.value.some((seat) => seat.id === seatID)) {
    highlightSeat.value = ''
    emit('located', { seatID, ok: false })
    return
  }
  highlightSeat.value = seatID
  emit('located', { seatID, ok: true })
}

watch(
  () => props.focusSeat,
  (value) => {
    pendingFocus.value = (value ?? '').trim()
    // 定位成功后父组件会立即把这个值清空（参数已用掉）：高亮留在那一行上，不跟着闪掉
    if (!pendingFocus.value) return
    tryLocate()
  },
  { immediate: true },
)

// 快照型主题（waiting）由 useWaiting 订阅；这里只跟着它换 key 集合。
useLiveTopic('waiting', { initial: false, fetch: syncWaitingKeys, onSnap: syncWaitingKeys })
useLiveTopic('sessions', { fetch: refresh, initial: true })
useLiveTopic('jobs', { fetch: refresh, initial: false })

watch(() => props.project, () => { void refresh() })
watch(() => props.reload, () => { void refresh() })
</script>

<template>
  <SeatRoster
    :seats="seats"
    :project="project"
    :waiting-keys="waitingKeys"
    :highlight="highlightSeat"
    :fetch-open-plan="seatOpenPlan"
    :patch-seat="patchSeat"
    :fetch-history="seatHistory"
    @refresh="refresh"
  />
</template>
