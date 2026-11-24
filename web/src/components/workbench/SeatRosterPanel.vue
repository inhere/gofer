<script setup lang="ts">
// 「常驻会话」名册的取数层：拉 GET /v1/seats，在 sessions / jobs 主题变化时重拉（座位的状态由
// 坐在里面的会话推出来，会话或 job 一变它就可能变），「等我」用全页面共用的 useWaiting。
// 渲染与行菜单在 SeatRoster.vue。
import { ref, watch } from 'vue'
import type { SeatView } from '../../api/types'
import { listSeats, patchSeat, seatOpenPlan } from '../../api/seats'
import { useLiveTopic } from '../../utils/useLiveTopic'
import { useWaiting } from '../../composables/useWaiting'
import { seatWaitingKey } from './seatRoster'
import SeatRoster from './SeatRoster.vue'

const props = defineProps<{
  project?: string
  // 父组件递增这个数就重拉（会话行上绑定成功后）
  reload?: number
}>()

const seats = ref<SeatView[]>([])
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
  } catch {
    // 名册是附加信息：拉不到就保留上一帧，不占侧栏的错误位
  }
}

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
    :fetch-open-plan="seatOpenPlan"
    :patch-seat="patchSeat"
    @refresh="refresh"
  />
</template>
