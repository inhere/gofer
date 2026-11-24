<script setup lang="ts">
// 工作台会话行上的「设为常驻」（计划 T5）：行尾一个不打扰的 ⋯，菜单里是「设为常驻」——
// 列出这个会话的候选座位（GET /v1/sessions/{sid}/seat-candidates），点一个就 bind。
//
// 会话的 cwd 正好落在某个空座位里时（候选非空），打开的那一行还会多一行「设为 X？」提示；
// 人点了才绑，× 关掉只在本地记住（gofer.workbench.seatHintDismissed），不再打扰。
import { computed, ref, watch } from 'vue'
import type { SeatView, WorkbenchThread } from '../../api/types'
import { bindSeat, seatCandidates } from '../../api/seats'
import { t } from '../../i18n'
import {
  isSeatArchived,
  isSessionThread,
  readDismissedSeats,
  rememberDismissedSeat,
  seatHintOf,
  sessionIDOfThread,
  type SeatHint,
} from './seatRoster'
import { bindSessionToSeat, loadSeatCandidates } from './seatActions'

const props = defineProps<{
  thread: WorkbenchThread
  // 是不是当前打开的那一行：只有它主动去问候选，避免上百行会话各发一次请求。
  active?: boolean
  // 已经有座位坐着的会话（会话视图带 seat）：不再提示
  seated?: boolean
}>()

const emit = defineEmits<{
  (e: 'bound', seat: SeatView): void
  (e: 'refresh'): void
}>()

const sessionID = computed(() => sessionIDOfThread(props.thread.id))
const eligible = computed(() => !!sessionID.value && isSessionThread(props.thread) && !props.seated)
const open = ref(false)
const candidates = ref<SeatView[]>([])
const loaded = ref(false)
const busy = ref(false)
const notice = ref('')
const dismissed = ref<Set<string>>(readDismissedSeats(storage()))

function storage(): Storage | undefined {
  return typeof localStorage === 'undefined' ? undefined : localStorage
}

const options = computed(() => candidates.value.filter((seat) => !isSeatArchived(seat)))
const hint = computed<SeatHint | null>(() => {
  if (!props.active || !eligible.value || !loaded.value) return null
  return seatHintOf(options.value, dismissed.value, props.thread.id)
})

async function ensureCandidates(): Promise<void> {
  if (!eligible.value || busy.value) return
  busy.value = true
  try {
    candidates.value = await loadSeatCandidates({ seatCandidates }, sessionID.value)
    loaded.value = true
    notice.value = ''
  } catch (e) {
    notice.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

// 候选只在「这一行被打开」或「菜单被打开」时问一次。
watch(() => props.active && eligible.value, (on) => { if (on && !loaded.value) void ensureCandidates() }, { immediate: true })

function toggleMenu(): void {
  open.value = !open.value
  notice.value = ''
  if (open.value && !loaded.value) void ensureCandidates()
}

async function bind(seat: SeatView): Promise<void> {
  busy.value = true
  try {
    const got = await bindSessionToSeat({ bindSeat }, seat.id, sessionID.value)
    open.value = false
    candidates.value = []
    loaded.value = false
    emit('bound', got)
    emit('refresh')
  } catch (e) {
    notice.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

async function bindHint(): Promise<void> {
  const seat = options.value.find((item) => item.id === hint.value?.seatID)
  if (seat) await bind(seat)
}

function dismissHint(): void {
  if (!hint.value) return
  dismissed.value = rememberDismissedSeat(storage(), hint.value.seatID)
}
</script>

<template>
  <div v-if="eligible" class="seat-bind">
    <button
      class="bind-btn"
      type="button"
      :aria-label="t('workbench.seat.bindMenu')"
      :aria-expanded="open"
      :data-test="`seat-bind-${thread.id}`"
      @click.stop="toggleMenu"
    >
      ⋯
    </button>
    <div v-if="open" class="bind-menu" role="menu" @click.stop>
      <p class="bind-head">{{ t('workbench.seat.bind') }}</p>
      <button
        v-for="seat in options"
        :key="seat.id"
        class="bind-item"
        type="button"
        role="menuitem"
        :disabled="busy"
        :data-test="`seat-bind-to-${seat.name}`"
        @click="bind(seat)"
      >
        <span class="bind-name">{{ seat.name }}</span>
        <span v-if="seat.purpose" class="bind-purpose">{{ seat.purpose }}</span>
      </button>
      <p v-if="loaded && options.length === 0" class="bind-empty">{{ t('workbench.seat.noCandidate') }}</p>
      <p v-else-if="!loaded" class="bind-empty">{{ t('workbench.seat.loading') }}</p>
      <p v-if="notice" class="bind-error">{{ notice }}</p>
    </div>
    <p v-if="hint" class="bind-hint" :data-test="`seat-hint-${thread.id}`">
      <button class="hint-take" type="button" :disabled="busy" @click.stop="bindHint">
        {{ t('workbench.seat.hint', { name: hint.name }) }}
      </button>
      <button class="hint-close" type="button" :aria-label="t('workbench.seat.hintDismiss')" @click.stop="dismissHint">
        ×
      </button>
    </p>
  </div>
</template>

<style scoped>
.seat-bind {
  position: absolute;
  top: 0;
  right: 0;
  z-index: 10;
}
.bind-btn {
  width: 24px;
  height: 24px;
  display: grid;
  place-items: center;
  padding: 0;
  background: var(--surface);
  border: 1px solid transparent;
  border-radius: var(--r-ctl);
  color: var(--ink-3);
  font: inherit;
  cursor: pointer;
  /* 不打扰：悬停 / 打开 / 键盘聚焦时才出现 */
  opacity: 0;
}
.sess-row:hover .bind-btn,
.sess-row.selected .bind-btn,
.bind-btn:focus-visible,
.bind-btn[aria-expanded='true'] {
  opacity: 1;
}
.bind-btn:hover {
  color: var(--ink);
  border-color: var(--line-strong);
}
.bind-menu {
  position: absolute;
  top: 26px;
  right: 0;
  min-width: 180px;
  padding: var(--s1);
  display: flex;
  flex-direction: column;
  gap: 1px;
  background: var(--surface);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  box-shadow: var(--shadow-1);
}
.bind-head {
  margin: 0;
  padding: 0 var(--s2) var(--s1);
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.bind-item {
  display: flex;
  flex-direction: column;
  gap: 1px;
  padding: var(--s1) var(--s2);
  background: none;
  border: 0;
  border-radius: var(--r-ctl);
  color: var(--ink);
  font: inherit;
  font-size: var(--fs-sm);
  text-align: left;
  cursor: pointer;
}
.bind-item:hover:not(:disabled) {
  background: var(--sunken);
}
.bind-item:disabled {
  color: var(--ink-3);
  cursor: default;
}
.bind-purpose,
.bind-empty {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.bind-empty {
  margin: 0;
  padding: 0 var(--s2);
}
.bind-error {
  margin: 0;
  padding: 0 var(--s2);
  color: var(--fail);
  font-size: var(--fs-xs);
}
.bind-hint {
  position: absolute;
  top: 22px;
  right: 0;
  display: flex;
  align-items: center;
  gap: 2px;
  margin: 0;
  padding: 0 2px 0 var(--s2);
  background: var(--accent-soft);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  white-space: nowrap;
}
.hint-take,
.hint-close {
  padding: 0 2px;
  background: none;
  border: 0;
  color: var(--accent);
  font: inherit;
  font-size: var(--fs-xs);
  cursor: pointer;
}
.hint-close {
  color: var(--ink-3);
}
</style>
