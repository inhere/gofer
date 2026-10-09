<script setup lang="ts">
// N3 决策卡（design §2.3）：固定 4 行——来源·等待·超时·「卡住 N」/ 标题 / 一句话 / 操作。
// 阻塞明细、管家理由、验收数据收在「详情」；「回复」点了才展开输入框。
// 卡片只发出 act 事件，真正的写操作与撤销窗口在 store/today.ts。
// 「稍后」（T3）：菜单选 1 小时 / 明早 9:00 / 等相关 job 结束，发出 snooze 事件；专注模式经
// defineExpose 的方法驱动同一张卡（数字键 / a / r / i / h）。
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import RejectDialog from '../RejectDialog.vue'
import type { TodayAction, TodayCard } from '../../api/today'
import { actionKey, adviceAction, blockShort, cardLink, expiresText, waitText } from '../../utils/today'
import { approveAction, focusActions, replyAction as replyActionOf } from '../../utils/todayFocus'
import { snoozeHints, snoozeOptions, wokeText, type SnoozeOption } from '../../utils/todaySnooze'

const props = defineProps<{ card: TodayCard; nowSec: number; initialInfoOpen?: boolean }>()
const emit = defineEmits<{
  (e: 'act', action: TodayAction, text: string, viaAdvice: boolean): void
  (e: 'navigate'): void
  (e: 'snooze', opt: SnoozeOption): void
}>()

const infoOpen = ref(!!props.initialInfoOpen)
const replyAction = ref<TodayAction | null>(null)
const replyText = ref('')
const rerunOpen = ref(false)
const replyInput = ref<HTMLInputElement | null>(null)
const snoozeOpen = ref(false)
const snoozeWrap = ref<HTMLElement | null>(null)
const woke = computed(() => wokeText(props.card))
const snoozeOpts = computed(() => (snoozeOpen.value ? snoozeOptions(props.card) : []))
const snoozeTips = computed(() => snoozeHints(props.card, props.nowSec))

const advice = computed(() => adviceAction(props.card))
const otherActions = computed(() => props.card.actions.filter((a) => a !== advice.value))
const short = computed(() => blockShort(props.card))
const wait = computed(() => waitText(props.card, props.nowSec))
const expires = computed(() => expiresText(props.card, props.nowSec))
const link = computed(() => cardLink(props.card))
const review = computed(() => props.card.review)
const hasInfo = computed(
  () =>
    !!props.card.blocks.text ||
    !!props.card.advice?.text ||
    !!review.value ||
    (props.card.suggestions?.length ?? 0) > 0,
)

function styleClass(a: TodayAction): string {
  switch (a.style) {
    case 'ok':
      return 'dc-btn--ok'
    case 'bad':
      return 'dc-btn--bad'
    case 'primary':
      return 'dc-btn--pri'
    case 'link':
      return 'dc-btn--link'
  }
  return ''
}

function onAction(a: TodayAction, viaAdvice = false): void {
  if (a.id === 'diff' || a.id === 'open') {
    emit('navigate')
    return
  }
  if (a.id === 'rerun') {
    rerunOpen.value = true
    return
  }
  if (a.needs_text) {
    replyAction.value = a
    void nextTick(() => replyInput.value?.focus())
    return
  }
  emit('act', a, '', viaAdvice)
}

function sendReply(): void {
  const a = replyAction.value
  const text = replyText.value.trim()
  if (!a || !text) return
  emit('act', a, text, false)
  replyText.value = ''
  replyAction.value = null
}

function onReplyKey(ev: KeyboardEvent): void {
  if (ev.key === 'Enter' && !ev.isComposing) {
    ev.preventDefault()
    sendReply()
  } else if (ev.key === 'Escape') {
    replyAction.value = null
  }
}

function onRerun(note: string, resume: boolean): void {
  rerunOpen.value = false
  const a = props.card.actions.find((x) => x.id === 'rerun')
  if (a) emit('act', { ...a, value: resume ? '1' : '0' }, note, false)
}

function suggestionAct(field: string, adopt: boolean): void {
  emit('act', { id: `${adopt ? 'adopt' : 'dismiss'}:${field}`, label: adopt ? '采纳建议' : '忽略建议' }, '', false)
}

function pickSnooze(opt: SnoozeOption): void {
  snoozeOpen.value = false
  emit('snooze', opt)
}

function onDocClick(ev: MouseEvent): void {
  if (snoozeWrap.value && !snoozeWrap.value.contains(ev.target as Node)) snoozeOpen.value = false
}

watch(snoozeOpen, (open) => {
  if (typeof document === 'undefined') return
  if (open) document.addEventListener('click', onDocClick, true)
  else document.removeEventListener('click', onDocClick, true)
})
onUnmounted(() => {
  if (typeof document !== 'undefined') document.removeEventListener('click', onDocClick, true)
})

// 专注模式（Today 页的 FocusMode）按键驱动这张卡；返回 false 表示这张卡没有对应操作。
defineExpose({
  runAction(index: number): boolean {
    if (snoozeOpen.value) {
      const opt = snoozeOptions(props.card)[index]
      if (opt) pickSnooze(opt)
      return !!opt
    }
    const a = focusActions(props.card)[index]
    if (a) onAction(a, a === advice.value)
    return !!a
  },
  approve(): boolean {
    const a = approveAction(props.card)
    if (a) onAction(a, a === advice.value)
    return !!a
  },
  openReply(): boolean {
    const a = replyActionOf(props.card)
    if (a) onAction(a)
    return !!a
  },
  toggleInfo(): void {
    if (hasInfo.value) infoOpen.value = !infoOpen.value
  },
  toggleSnooze(): void {
    snoozeOpen.value = !snoozeOpen.value
  },
  // Esc 先收起卡内的菜单 / 回复框，收起了就返回 true（专注模式不退出）。
  dismiss(): boolean {
    if (snoozeOpen.value) {
      snoozeOpen.value = false
      return true
    }
    if (replyAction.value || rerunOpen.value) {
      replyAction.value = null
      rerunOpen.value = false
      return true
    }
    return false
  },
})

function linkFor(a: TodayAction): string {
  return a.id === 'diff' && props.card.refs.job_id ? `/jobs/${encodeURIComponent(props.card.refs.job_id)}` : link.value
}
</script>

<template>
  <article class="dc" :data-u="card.urgency" :data-key="card.key" data-test="decision-card">
    <div class="dc-stripe" aria-hidden="true"></div>
    <div class="dc-body">
      <div class="dc-top mono" data-test="dc-line-source">
        <span class="dc-tag">{{ card.tag }}</span>
        <span v-if="card.project_key" class="dc-proj">{{ card.project_key }}</span>
        <span v-if="wait">· {{ wait }}</span>
        <span v-if="expires" class="dc-exp">· {{ expires }}</span>
        <span v-if="short" class="dc-blk">· {{ short }}</span>
        <span v-if="woke" class="dc-woke" data-test="dc-woke">· {{ woke }}</span>
      </div>
      <h3 class="dc-title" data-test="dc-line-title">
        <RouterLink :to="link" @click="emit('navigate')">{{ card.title }}</RouterLink>
      </h3>
      <p class="dc-sum" data-test="dc-line-summary">{{ card.summary || '—' }}</p>
      <div class="dc-acts" data-test="dc-line-actions">
        <button
          v-if="advice"
          class="dc-btn dc-btn--pri"
          type="button"
          data-test="dc-advice"
          @click="onAction(advice, true)"
        >按建议：{{ advice.label }}</button>
        <template v-for="a in otherActions" :key="actionKey(a)">
          <RouterLink
            v-if="a.id === 'diff' || a.id === 'open'"
            :to="linkFor(a)"
            class="dc-btn dc-btn--link"
            @click="emit('navigate')"
          >{{ a.label }}</RouterLink>
          <button
            v-else
            class="dc-btn"
            :class="styleClass(a)"
            type="button"
            :data-act="actionKey(a)"
            @click="onAction(a)"
          >{{ a.label }}</button>
        </template>
        <span class="dc-sp"></span>
        <button
          v-if="hasInfo"
          class="dc-btn dc-btn--dim"
          type="button"
          data-test="dc-info-toggle"
          :aria-expanded="infoOpen"
          @click="infoOpen = !infoOpen"
        >{{ infoOpen ? '收起' : '详情' }}</button>
        <span ref="snoozeWrap" class="dc-snz">
          <button
            class="dc-btn dc-btn--dim"
            type="button"
            data-test="dc-snooze"
            aria-haspopup="menu"
            :aria-expanded="snoozeOpen"
            @click="snoozeOpen = !snoozeOpen"
          >稍后</button>
          <div v-if="snoozeOpen" class="dc-menu" role="menu" data-test="dc-snooze-menu">
            <button
              v-for="(o, i) in snoozeOpts"
              :key="o.id"
              class="dc-menu-item"
              type="button"
              role="menuitem"
              :data-snooze="o.id"
              @click="pickSnooze(o)"
            ><span class="dc-menu-n mono">{{ i + 1 }}</span>{{ o.label }}</button>
            <p v-for="t in snoozeTips" :key="t.text" class="dc-menu-hint mono" :class="{ bad: t.bad }">{{ t.text }}</p>
          </div>
        </span>
      </div>
      <div v-if="replyAction" class="dc-reply" data-test="dc-reply">
        <input
          ref="replyInput"
          v-model="replyText"
          class="mono"
          :placeholder="`${replyAction.label}…（Enter 发送，Esc 取消）`"
          :aria-label="replyAction.label"
          @keydown="onReplyKey"
        />
        <button class="dc-btn dc-btn--pri" type="button" :disabled="!replyText.trim()" @click="sendReply">发送</button>
      </div>
      <div v-if="infoOpen && hasInfo" class="dc-info" data-test="dc-info">
        <p v-if="card.blocks.text"><b>卡住</b>{{ card.blocks.text }}</p>
        <p v-if="card.advice?.text"><b>管家</b>{{ card.advice.text }}</p>
        <template v-if="review">
          <p class="mono">
            <b>改动</b>{{ review.commits }} 个提交 · <span class="dc-add">+{{ review.adds }}</span> /
            <span class="dc-del">−{{ review.dels }}</span><template v-if="review.verify"> · verify {{ review.verify }}</template>
          </p>
          <p v-if="review.digest" class="dc-digest">{{ review.digest }}</p>
          <p><RouterLink to="/review" @click="emit('navigate')">看全部待验收</RouterLink></p>
        </template>
        <div v-for="sg in card.suggestions ?? []" :key="sg.field" class="dc-sg">
          <span><b>建议</b>{{ sg.text }}</span>
          <button class="dc-btn dc-btn--ok" type="button" @click="suggestionAct(sg.field, true)">采纳</button>
          <button class="dc-btn" type="button" @click="suggestionAct(sg.field, false)">忽略</button>
        </div>
      </div>
    </div>
    <RejectDialog v-if="rerunOpen" :submitting="false" error="" @close="rerunOpen = false" @confirm="onRerun" />
  </article>
</template>

<style scoped>
.dc {
  display: flex;
  min-width: 0;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.dc-stripe {
  flex: none;
  width: 3px;
  background: var(--queue);
}
.dc[data-u='now'] .dc-stripe {
  background: var(--fail);
}
.dc[data-u='blocking'] .dc-stripe {
  background: var(--run);
}
.dc-body {
  flex: 1;
  min-width: 0;
  padding: 9px 12px 10px;
}
.dc-top {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 6px;
  color: var(--queue);
  font-size: 11px;
}
.dc-tag {
  color: var(--paper);
  font-weight: 600;
}
.dc-exp {
  color: var(--fail);
}
.dc-blk {
  color: var(--run);
}
.dc-woke {
  color: var(--phosphor);
}
.dc-snz {
  position: relative;
}
.dc-menu {
  position: absolute;
  right: 0;
  top: calc(100% + 4px);
  z-index: 20;
  display: flex;
  flex-direction: column;
  width: max-content;
  min-width: 160px;
  max-width: min(260px, calc(100vw - 32px));
  padding: 4px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  box-shadow: 0 6px 20px rgba(0, 0, 0, 0.35);
}
.dc-menu-item {
  display: flex;
  gap: 8px;
  align-items: center;
  padding: 6px 8px;
  color: var(--paper);
  background: transparent;
  border: 0;
  border-radius: var(--radius);
  font-size: 12px;
  text-align: left;
}
.dc-menu-item:hover,
.dc-menu-item:focus-visible {
  background: var(--ink);
}
.dc-menu-n {
  color: var(--queue);
  font-size: 10px;
}
.dc-menu-hint {
  margin: 4px 8px 2px;
  color: var(--queue);
  font-size: 11px;
  overflow-wrap: anywhere;
}
.dc-menu-hint.bad {
  color: var(--fail);
}
.dc-title {
  margin: 3px 0 2px;
  font-size: 14px;
  font-weight: 600;
  overflow-wrap: anywhere;
}
.dc-title a {
  color: var(--paper);
}
.dc-title a:hover {
  color: var(--phosphor);
  text-decoration: none;
}
.dc-sum {
  margin: 0 0 8px;
  color: var(--queue);
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dc-acts {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.dc-sp {
  flex: 1;
}
.dc-btn {
  display: inline-flex;
  align-items: center;
  min-height: 26px;
  padding: 3px 10px;
  color: var(--paper);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  font-size: 12px;
  white-space: nowrap;
}
.dc-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
  text-decoration: none;
}
.dc-btn:disabled {
  opacity: 0.5;
}
.dc-btn--pri {
  color: var(--ink);
  background: var(--phosphor);
  border-color: var(--phosphor);
  font-weight: 600;
}
.dc-btn--ok {
  color: var(--done);
  border-color: var(--done);
}
.dc-btn--bad {
  color: var(--fail);
  border-color: var(--fail);
}
.dc-btn--link {
  color: var(--phosphor);
  border-color: transparent;
}
.dc-btn--dim {
  color: var(--queue);
  border-color: transparent;
}
.dc-reply {
  display: flex;
  gap: 6px;
  margin-top: 8px;
}
.dc-reply input {
  flex: 1;
  min-width: 0;
  padding: 4px 8px;
  color: var(--paper);
  background: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  font-size: 12px;
}
.dc-info {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px dashed var(--line);
  font-size: 12px;
  color: var(--paper);
}
.dc-info p {
  margin: 0 0 4px;
  overflow-wrap: anywhere;
}
.dc-info b {
  display: inline-block;
  min-width: 3em;
  margin-right: 6px;
  color: var(--queue);
  font-weight: 500;
}
.dc-digest {
  white-space: pre-wrap;
}
.dc-add {
  color: var(--done);
}
.dc-del {
  color: var(--fail);
}
.dc-sg {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-top: 4px;
}
.dc-sg span {
  flex: 1 1 200px;
  min-width: 0;
}
</style>
