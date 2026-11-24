<script setup lang="ts">
// 工作台侧栏的「常驻会话」名册（计划 T5）：按项目分组，每行名字 / 用途 / 状态点 / 未读点，
// 行菜单是「在终端继续」「打开线程」「编辑」「归档」。
//
// 只渲染传进来的分组（分组与排序在 seatRoster.ts 的 groupSeats 里定，顺序只由名字决定），
// 取数在 SeatRosterPanel.vue。动作走 seatActions.ts，接口同样由父组件给。
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import type { SeatHistory, SeatPatch, SeatOpenPlan, SeatView } from '../../api/types'
import { t } from '../../i18n'
import Icon from '../../ui/Icon.vue'
import { ChevronDown, ChevronRight, Copy, Ellipsis } from '@lucide/vue'
import { formatDateTime } from '../../utils/format'
import { copyText } from '../../utils/sessionMessaging'
import {
  charterBySession,
  charterHistoryRows,
  detectShell,
  groupSeats,
  seatCommand,
  seatDot,
  seatRefLabel,
  seatThreadID,
  type SeatDot,
  type SeatHistoryRow,
  type SeatProjectGroup,
  type SeatShell,
} from './seatRoster'
import { archiveSeat, continueInTerminal, saveSeatEdit, type ContinueResult } from './seatActions'

const props = defineProps<{
  seats: SeatView[]
  // 项目筛选（与侧栏的 ProjectFilter 同一值；空 = 全部）
  project?: string
  // 「等我」快照里出现的 seat 主体 key（useWaiting）
  waitingKeys: string[]
  fetchOpenPlan: (seatID: string) => Promise<SeatOpenPlan>
  patchSeat: (seatID: string, patch: SeatPatch) => Promise<SeatView>
  // 编辑弹窗里的章程历史（GET /v1/seats/{id}/history）
  fetchHistory: (seatID: string) => Promise<SeatHistory>
}>()

const emit = defineEmits<{
  (e: 'refresh'): void
}>()

const router = useRouter()
const open = ref(true)
const groups = computed<SeatProjectGroup[]>(() => groupSeats(props.seats, { project: props.project || undefined }))
const total = computed(() => groups.value.reduce((n, g) => n + g.seats.length, 0))

// 当前打开行菜单的座位 id；空 = 菜单都关着（一次只开一个）。
const menuSeatID = ref('')
const editing = ref<SeatView | null>(null)
const draftPurpose = ref('')
const draftCharter = ref('')
// 章程历史：弹窗打开时拉一次；loading / error 只影响历史那一节，不影响编辑本身。
const history = ref<SeatHistoryRow[]>([])
const historyLoading = ref(false)
const historyError = ref('')
// 展开了完整正文的那一条（0 = 全部折着）
const historyOpenID = ref(0)
const continuing = ref<{ seat: SeatView; result: ContinueResult } | null>(null)
const archiving = ref('')
const busy = ref(false)
const notice = ref('')

const shell = ref<SeatShell>(detectShell(typeof navigator === 'undefined' ? '' : navigator.userAgent))

// dot 按「等我」快照算（实时的那一份），seat 自己的 waiting 字段也算。
function dot(seat: SeatView): SeatDot {
  return seatDot(seat, new Set(props.waitingKeys))
}

function toggleMenu(seat: SeatView): void {
  menuSeatID.value = menuSeatID.value === seat.id ? '' : seat.id
  archiving.value = ''
  notice.value = ''
}

function openThread(seat: SeatView): void {
  menuSeatID.value = ''
  // ACP 位置以 open-plan 给的线程为准（会话重启过时 seat 行上的可能还没刷新）；
  // 终端位置直接由 seat 自己给出 r:<sid>。
  const thread = seatThreadID(seat)
  if (thread) {
    openRoute(thread)
    return
  }
  if (seat.kind !== 'acp') return
  void props
    .fetchOpenPlan(seat.id)
    .then((plan) => { if (plan.thread_id) openRoute(plan.thread_id) })
    .catch(() => { notice.value = t('workbench.seat.reason.unknown') })
}

// 与「等我」弹卡同一条深链：工作台自己定位并补拉打开的线程。
function openRoute(thread: string): void {
  void router.push({ path: '/workbench', query: { thread } })
}

// openPlanThread 是「在终端继续」弹窗里的「打开线程」（ACP 位置没有可复制的命令时）。
function openPlanThread(): void {
  const thread = continuing.value?.result.threadID ?? ''
  continuing.value = null
  if (thread) openRoute(thread)
}

async function continueHere(seat: SeatView): Promise<void> {
  menuSeatID.value = ''
  busy.value = true
  try {
    const result = await continueInTerminal(
      { fetchOpenPlan: props.fetchOpenPlan, copy: copyText, shell: shell.value },
      seat.id,
    )
    continuing.value = { seat, result }
  } catch (e) {
    notice.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

function startEdit(seat: SeatView): void {
  menuSeatID.value = ''
  editing.value = seat
  draftPurpose.value = seat.purpose ?? ''
  draftCharter.value = seat.charter ?? ''
  void loadHistory(seat.id)
}

// loadHistory 拉这个位置的章程历史（最近的在上）。拉不到不挡编辑，只在历史那一节写一句原因。
async function loadHistory(seatID: string): Promise<void> {
  history.value = []
  historyError.value = ''
  historyOpenID.value = 0
  historyLoading.value = true
  try {
    const got = await props.fetchHistory(seatID)
    // 拉的过程中把人已经在编辑的位置换掉了：这份结果不要
    if (editing.value?.id === seatID) history.value = charterHistoryRows(got?.charter)
  } catch (e) {
    if (editing.value?.id === seatID) historyError.value = e instanceof Error ? e.message : String(e)
  } finally {
    historyLoading.value = false
  }
}

function toggleHistory(id: number): void {
  historyOpenID.value = historyOpenID.value === id ? 0 : id
}

// historyAuthor 是历史行的作者：本人（user）或从哪个会话里改的。
function historyAuthor(by: string): string {
  const sid = charterBySession(by)
  return sid ? t('workbench.seat.historyBySession', { sid }) : t('workbench.seat.historyByUser')
}

async function saveEdit(): Promise<void> {
  const seat = editing.value
  if (!seat) return
  busy.value = true
  try {
    const patch = saveSeatEdit({ patchSeat: props.patchSeat }, seat, {
      purpose: draftPurpose.value,
      charter: draftCharter.value,
    })
    if (patch) await patch
    editing.value = null
    emit('refresh')
  } catch (e) {
    notice.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

async function confirmArchive(seat: SeatView): Promise<void> {
  busy.value = true
  try {
    await archiveSeat({ patchSeat: props.patchSeat }, seat.id)
    archiving.value = ''
    menuSeatID.value = ''
    emit('refresh')
  } catch (e) {
    notice.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

async function copyCommand(): Promise<void> {
  const text = continuing.value?.result.command.command ?? ''
  if (!text) return
  const ok = await copyText(text)
  if (continuing.value) continuing.value.result.copied = ok
}

function switchShell(): void {
  if (!continuing.value) return
  shell.value = shell.value === 'sh' ? 'powershell' : 'sh'
  // 换写法不用再问一次服务端：open-plan 两种命令都带回来了。
  const next = seatCommand(continuing.value.result.plan, shell.value)
  if (next.command) continuing.value.result.command = next
}

function reasonText(reason: string): string {
  return reason ? t(`workbench.seat.reason.${reason}`) : ''
}
</script>

<template>
  <section v-if="total > 0" class="seat-roster" data-test="seat-roster">
    <button class="group-head seat-head" type="button" :aria-expanded="open" @click="open = !open">
      <Icon class="caret" :icon="open ? ChevronDown : ChevronRight" size="sm" />
      <span class="group-name">{{ t('workbench.seat.title') }}</span>
      <span class="group-count">{{ total }}</span>
    </button>
    <template v-if="open">
      <div v-for="group in groups" :key="group.project_key" class="seat-group" :data-project="group.project_key">
        <p class="seat-project">{{ group.project_key }}</p>
        <div v-for="seat in group.seats" :key="seat.id" class="seat-row" :class="{ 'seat-row--open': menuSeatID === seat.id }">
          <button class="seat" type="button" :data-test="`seat-row-${seat.name}`" @click="openThread(seat)">
            <span class="dot" :data-state="dot(seat)" :title="t(`workbench.seat.dot.${dot(seat)}`)" aria-hidden="true" />
            <span class="nm" :title="seat.name">{{ seat.name }}</span>
            <span v-if="seat.unread" class="unread" :title="t('workbench.seat.unread')" aria-hidden="true">●</span>
            <span class="use">{{ seat.purpose || t('workbench.seat.noPurpose') }}</span>
            <span v-if="seatRefLabel(seat)" class="ref">{{ seatRefLabel(seat) }}</span>
          </button>
          <button
            class="menu-btn"
            type="button"
            :aria-label="t('workbench.seat.menu')"
            :aria-expanded="menuSeatID === seat.id"
            :data-test="`seat-menu-${seat.name}`"
            @click.stop="toggleMenu(seat)"
          >
            <Icon :icon="Ellipsis" size="sm" />
          </button>
          <div v-if="menuSeatID === seat.id" class="seat-menu" role="menu">
            <button class="menu-item" type="button" role="menuitem" :disabled="busy" @click="continueHere(seat)">
              {{ t('workbench.seat.continue') }}
            </button>
            <button
              class="menu-item"
              type="button"
              role="menuitem"
              :disabled="!seatThreadID(seat)"
              @click="openThread(seat)"
            >
              {{ t('workbench.seat.openThread') }}
            </button>
            <button class="menu-item" type="button" role="menuitem" @click="startEdit(seat)">
              {{ t('workbench.seat.edit') }}
            </button>
            <template v-if="archiving === seat.id">
              <span class="menu-note">{{ t('workbench.seat.archiveConfirm') }}</span>
              <button class="menu-item danger" type="button" role="menuitem" :disabled="busy" @click="confirmArchive(seat)">
                {{ t('workbench.seat.archiveYes') }}
              </button>
              <button class="menu-item" type="button" role="menuitem" @click="archiving = ''">
                {{ t('workbench.seat.cancel') }}
              </button>
            </template>
            <button v-else class="menu-item" type="button" role="menuitem" @click="archiving = seat.id">
              {{ t('workbench.seat.archive') }}
            </button>
          </div>
        </div>
      </div>
    </template>

    <p v-if="notice" class="seat-notice">{{ notice }}</p>

    <!-- 在终端继续：命令、在哪台机器 / 哪个目录里敲；不能续时说原因 -->
    <div v-if="continuing" class="seat-sheet" role="dialog" data-test="seat-continue">
      <p class="sheet-title">{{ t('workbench.seat.continueTitle', { name: continuing.seat.name }) }}</p>
      <p v-if="continuing.result.runner" class="sheet-line">
        {{ t('workbench.seat.runner', { name: continuing.result.runner }) }}
      </p>
      <p v-if="continuing.result.cwd" class="sheet-line mono">{{ continuing.result.cwd }}</p>
      <p v-if="continuing.result.alive" class="sheet-warn">{{ t('workbench.seat.alive') }}</p>
      <p v-if="continuing.result.kind === 'acp'" class="sheet-line">{{ t('workbench.seat.acpNote') }}</p>
      <template v-if="continuing.result.command.command">
        <pre class="seat-cmd" data-test="seat-command">{{ continuing.result.command.command }}</pre>
        <div class="sheet-actions">
          <button class="btn sm" type="button" @click="switchShell">{{ t('workbench.seat.switchShell') }}</button>
          <button class="btn sm" type="button" data-test="seat-copy" @click="copyCommand">
            <Icon :icon="Copy" size="sm" />
            {{ continuing.result.copied ? t('workbench.seat.copied') : t('workbench.seat.copy') }}
          </button>
          <button class="btn sm" type="button" @click="continuing = null">{{ t('workbench.seat.close') }}</button>
        </div>
      </template>
      <template v-else>
        <p class="sheet-warn">
          {{ reasonText(continuing.result.command.reason) || t('workbench.seat.reason.unknown') }}
        </p>
        <div class="sheet-actions">
          <button
            v-if="continuing.result.threadID"
            class="btn sm primary"
            type="button"
            @click="openPlanThread"
          >
            {{ t('workbench.seat.openThread') }}
          </button>
          <button class="btn sm" type="button" @click="continuing = null">{{ t('workbench.seat.close') }}</button>
        </div>
      </template>
    </div>

    <!-- 编辑用途 / 章程：显示章程是谁改的（charter_by），下面是每次修改的历史 -->
    <div v-if="editing" class="seat-sheet" role="dialog" data-test="seat-edit">
      <p class="sheet-title">{{ t('workbench.seat.editTitle', { name: editing.name }) }}</p>
      <p class="sheet-line">
        {{
          charterBySession(editing.charter_by)
            ? t('workbench.seat.charterBySession', { sid: charterBySession(editing.charter_by) })
            : t('workbench.seat.charterByUser')
        }}
      </p>
      <label class="sheet-field">
        <span>{{ t('workbench.seat.purpose') }}</span>
        <input v-model="draftPurpose" class="input" type="text" data-test="seat-purpose" />
      </label>
      <label class="sheet-field">
        <span>{{ t('workbench.seat.charter') }}</span>
        <textarea v-model="draftCharter" class="input" rows="4" data-test="seat-charter" />
      </label>
      <p class="sheet-line">{{ t('workbench.seat.charterHint') }}</p>
      <div class="sheet-actions">
        <button class="btn sm primary" type="button" :disabled="busy" data-test="seat-save" @click="saveEdit">
          {{ t('workbench.seat.save') }}
        </button>
        <button class="btn sm" type="button" @click="editing = null">{{ t('workbench.seat.cancel') }}</button>
      </div>
      <div class="sheet-history" data-test="seat-history">
        <p class="sheet-line">{{ t('workbench.seat.history') }}</p>
        <p v-if="historyLoading" class="sheet-note">{{ t('workbench.seat.historyLoading') }}</p>
        <p v-else-if="historyError" class="sheet-note">
          {{ t('workbench.seat.historyError', { error: historyError }) }}
        </p>
        <p v-else-if="!history.length" class="sheet-note">{{ t('workbench.seat.historyEmpty') }}</p>
        <ul v-else class="history-list">
          <li v-for="row in history" :key="row.id" class="history-row" :data-test="`seat-history-${row.id}`">
            <button class="history-head" type="button" :aria-expanded="historyOpenID === row.id" @click="toggleHistory(row.id)">
              <span class="history-at">{{ formatDateTime(row.at) }}</span>
              <span class="history-by">{{ historyAuthor(row.by) }}</span>
              <span class="history-preview">{{ row.preview || t('workbench.seat.historyBlank') }}</span>
            </button>
            <pre v-if="historyOpenID === row.id" class="history-body">{{ row.body }}</pre>
          </li>
        </ul>
      </div>
    </div>
  </section>
</template>

<style scoped>
.seat-roster {
  margin-top: var(--s2);
}
.seat-head {
  cursor: pointer;
}
.seat-head:hover {
  color: var(--ink);
}
.seat-project {
  margin: var(--s1) var(--s2) 0;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.seat-row {
  position: relative;
}
.seat {
  width: 100%;
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  gap: 2px var(--s2);
  align-items: center;
  padding: var(--s2) var(--s2);
  background: none;
  border: 0;
  border-radius: var(--r-ctl);
  color: var(--ink);
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.seat:hover {
  background: var(--sunken);
}
.seat:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: -2px;
}
.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--ink-3);
}
.dot[data-state='running'] {
  background: var(--run);
}
.dot[data-state='waiting'] {
  background: var(--need);
}
.dot[data-state='awaiting'] {
  background: var(--ink-2);
}
.dot[data-state='offline'] {
  background: var(--line-strong);
}
.dot[data-state='ended'] {
  background: var(--done);
}
.dot[data-state='failed'] {
  background: var(--fail);
}
.dot[data-state='none'] {
  background: transparent;
  box-shadow: inset 0 0 0 1px var(--line-strong);
}
.nm {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: var(--fs-sm);
  font-weight: 500;
}
.unread {
  color: var(--accent);
  font-size: 8px;
}
.use {
  grid-column: 2 / 4;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.ref {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  font-variant-numeric: tabular-nums;
}
.menu-btn {
  position: absolute;
  top: 6px;
  right: 4px;
  width: 22px;
  height: 22px;
  display: grid;
  place-items: center;
  padding: 0;
  background: var(--surface);
  border: 1px solid transparent;
  border-radius: var(--r-ctl);
  color: var(--ink-3);
  font: inherit;
  cursor: pointer;
  /* 不打扰：平时收起来，悬停 / 打开菜单 / 键盘聚焦时才出现 */
  opacity: 0;
}
.seat-row:hover .menu-btn,
.seat-row--open .menu-btn,
.menu-btn:focus-visible {
  opacity: 1;
}
.menu-btn:hover {
  color: var(--ink);
  border-color: var(--line-strong);
}
.seat-menu {
  position: absolute;
  z-index: 20;
  top: 28px;
  right: 4px;
  min-width: 168px;
  padding: var(--s1);
  display: flex;
  flex-direction: column;
  gap: 1px;
  background: var(--surface);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  box-shadow: var(--shadow-1);
}
.menu-item {
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
.menu-item:hover:not(:disabled) {
  background: var(--sunken);
}
.menu-item:disabled {
  color: var(--ink-3);
  cursor: default;
}
.menu-item.danger {
  color: var(--fail);
}
.menu-note {
  padding: 0 var(--s2);
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.seat-notice {
  margin: 0;
  padding: var(--s1) var(--s2);
  color: var(--fail);
  font-size: var(--fs-xs);
}
.seat-sheet {
  margin: var(--s2) 0;
  padding: var(--s2);
  background: var(--surface);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
}
.sheet-title {
  margin: 0 0 var(--s1);
  font-size: var(--fs-sm);
  font-weight: 600;
}
.sheet-line {
  margin: 0 0 var(--s1);
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.sheet-line.mono {
  font-family: var(--font-mono);
  word-break: break-all;
}
.sheet-warn {
  margin: 0 0 var(--s1);
  color: var(--need-ink);
  font-size: var(--fs-xs);
}
.sheet-note {
  margin: 0;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.seat-cmd {
  margin: 0 0 var(--s1);
  padding: var(--s1);
  background: var(--sunken);
  border-radius: var(--r-ctl);
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
  white-space: pre-wrap;
  word-break: break-all;
}
.sheet-field {
  display: block;
  margin: 0 0 var(--s1);
  font-size: var(--fs-xs);
  color: var(--ink-3);
}
.sheet-field .input {
  width: 100%;
  margin-top: 2px;
}
.sheet-actions {
  display: flex;
  gap: var(--s1);
  justify-content: flex-end;
}
.sheet-history {
  margin-top: var(--s2);
}
.history-list {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.history-head {
  width: 100%;
  display: grid;
  grid-template-columns: auto auto minmax(0, 1fr);
  gap: var(--s2);
  align-items: baseline;
  padding: 2px var(--s1);
  background: none;
  border: 0;
  border-radius: var(--r-ctl);
  color: var(--ink);
  font: inherit;
  font-size: var(--fs-xs);
  text-align: left;
  cursor: pointer;
}
.history-head:hover {
  background: var(--sunken);
}
.history-at,
.history-by {
  color: var(--ink-3);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.history-preview {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.history-body {
  margin: var(--s1) 0;
  padding: var(--s1);
  background: var(--sunken);
  border-radius: var(--r-ctl);
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
