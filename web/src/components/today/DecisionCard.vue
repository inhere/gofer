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
import { actionKey, actionLink, adviceAction, blockShort, cardLink, expiresText, memoryProposalText, waitText } from '../../utils/today'
import { approveAction, focusActions, replyAction as replyActionOf } from '../../utils/todayFocus'
import { commandLine, originText } from '../../utils/approval'
import { snoozeHints, snoozeOptions, wokeText, type SnoozeOption } from '../../utils/todaySnooze'
import { cardActionBlock } from '../../utils/todayGate'
import { CEILING_READ_ONLY_NOTICE } from '../../utils/ceiling'
import { t } from '../../i18n'
import { useWhoami } from '../../store/whoami'

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
// 「按建议：附意见重跑」也要经 RejectDialog；记住它是按建议点的
const rerunViaAdvice = ref(false)
const replyInput = ref<HTMLInputElement | null>(null)
const snoozeOpen = ref(false)
const snoozeWrap = ref<HTMLElement | null>(null)
const woke = computed(() => wokeText(props.card))
const snoozeOpts = computed(() => (snoozeOpen.value ? snoozeOptions(props.card) : []))
const snoozeTips = computed(() => snoozeHints(props.card, props.nowSec))

// 只读身份 / 缺权限时按钮灰显并说明原因（gofer-darr，判定见 utils/todayGate.ts）。
const { whoami, readOnly } = useWhoami()
function actionBlock(a: TodayAction): string {
  return cardActionBlock(whoami.value, props.card, a)
}

const advice = computed(() => adviceAction(props.card))
const otherActions = computed(() => props.card.actions.filter((a) => a !== advice.value))
// 类型 pill（设计稿 §7.1）：卡片种类走 i18n，业务内容（标题、摘要）不翻译。
const kindLabel = computed(() => t(`today.kind.${props.card.kind}`))
const short = computed(() => blockShort(props.card))
const wait = computed(() => waitText(props.card, props.nowSec))
const expires = computed(() => expiresText(props.card, props.nowSec))
const link = computed(() => cardLink(props.card))
const review = computed(() => props.card.review)
// 待批卡（gofer-9b1b）：理由、来源与批了会跑什么进「详情」（完整内容在 job 详情页的待批面板）
const approval = computed(() => props.card.approval)
const approvalCmd = computed(() => commandLine(approval.value?.command))
// 记忆整理卡（P4）：提议 + 记忆现状进「详情」
const memory = computed(() => props.card.memory)
const memoryProposal = computed(() => memoryProposalText(props.card))
// 管家摘要（≤5 行）：待验收卡在改动数据下面（后端已拷进 review.digest），其余卡跟在管家理由后
const adviceDigest = computed(() => (review.value ? '' : (props.card.advice?.digest ?? '')))
const hasInfo = computed(
  () =>
    !!props.card.blocks.text ||
    !!props.card.advice?.text ||
    !!adviceDigest.value ||
    !!review.value ||
    !!approval.value ||
    !!memory.value ||
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
  // 灰显的写操作即使在专注模式里按了数字键 / a / r 也不发请求（服务端反正会 403）。
  if (actionBlock(a)) return
  if (a.id === 'diff' || a.id === 'open') {
    emit('navigate')
    return
  }
  if (a.id === 'rerun') {
    rerunViaAdvice.value = viaAdvice
    rerunOpen.value = true
    return
  }
  if (a.needs_text || a.optional_text) {
    replyAction.value = a
    void nextTick(() => replyInput.value?.focus())
    return
  }
  emit('act', a, '', viaAdvice)
}

function sendReply(): void {
  const a = replyAction.value
  const text = replyText.value.trim()
  // optional_text（待批卡的「拒绝」）：理由可空，空着也照发
  if (!a || (!text && !a.optional_text)) return
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
  if (a) emit('act', { ...a, value: resume ? '1' : '0' }, note, rerunViaAdvice.value)
}

function suggestionAct(field: string, adopt: boolean): void {
  if (readOnly.value) return
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
  return actionLink(props.card, a)
}
</script>

<template>
  <article class="dc" :data-u="card.urgency" :data-kind="card.kind" :data-key="card.key" data-test="decision-card">
    <div class="dc-body">
      <div class="dc-top mono" data-test="dc-line-source">
        <span class="pill k-need dc-kind" data-test="dc-kind">{{ kindLabel }}</span>
        <span v-if="card.tag && card.tag !== kindLabel" class="dc-tag">{{ card.tag }}</span>
        <span v-if="card.agent" class="dc-agent">{{ card.agent }}</span>
        <span v-if="card.project_key" class="dc-proj">{{ card.project_key }}</span>
        <span v-if="wait" class="dc-when">{{ wait }}</span>
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
          :disabled="!!actionBlock(advice)"
          :title="actionBlock(advice) || undefined"
          @click="onAction(advice, true)"
        >{{ t('today.card.advice', { label: advice.label }) }}</button>
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
            :data-blocked="actionBlock(a) ? '1' : undefined"
            :disabled="!!actionBlock(a)"
            :title="actionBlock(a) || undefined"
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
        >{{ infoOpen ? t('today.card.collapse') : t('today.card.info') }}</button>
        <span ref="snoozeWrap" class="dc-snz">
          <button
            class="dc-btn dc-btn--dim"
            type="button"
            data-test="dc-snooze"
            aria-haspopup="menu"
            :aria-expanded="snoozeOpen"
            @click="snoozeOpen = !snoozeOpen"
          >{{ t('today.card.snooze') }}</button>
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
          :placeholder="replyAction.optional_text ? t('today.card.optionalPlaceholder', { label: replyAction.label }) : t('today.card.replyPlaceholder', { label: replyAction.label })"
          :aria-label="replyAction.label"
          @keydown="onReplyKey"
        />
        <button
          class="dc-btn dc-btn--pri"
          type="button"
          :disabled="!replyText.trim() && !replyAction.optional_text"
          @click="sendReply"
        >{{ replyAction.optional_text ? t('today.card.confirm', { label: replyAction.label }) : t('today.card.send') }}</button>
      </div>
      <div v-if="infoOpen && hasInfo" class="dc-info" data-test="dc-info">
        <p v-if="card.blocks.text"><b>{{ t('today.card.blocking') }}</b>{{ card.blocks.text }}</p>
        <p v-if="card.advice?.text" data-test="dc-advice-text"><b>{{ t('today.card.steward') }}</b>{{ card.advice.text }}</p>
        <p v-if="adviceDigest" class="dc-digest" data-test="dc-advice-digest">{{ adviceDigest }}</p>
        <template v-if="review">
          <p class="mono">
            <b>{{ t('today.card.changes') }}</b>{{ t('today.card.commits', { n: review.commits }) }} · <span class="dc-add">+{{ review.adds }}</span> /
            <span class="dc-del">−{{ review.dels }}</span><template v-if="review.verify"> · verify {{ review.verify }}</template>
          </p>
          <p v-if="review.digest" class="dc-digest" data-test="dc-review-digest"><b>{{ t('today.card.summary') }}</b>{{ review.digest }}</p>
          <p><RouterLink to="/review" @click="emit('navigate')">{{ t('today.card.allReviews') }}</RouterLink></p>
        </template>
        <template v-if="approval">
          <p v-if="approval.reason" data-test="dc-approval-reason"><b>{{ t('today.card.reason') }}</b>{{ approval.reason }}</p>
          <p v-if="approval.origin" class="mono"><b>{{ t('today.card.origin') }}</b>{{ originText(approval.origin) }}</p>
          <p v-if="approvalCmd" class="dc-digest mono" data-test="dc-approval-command"><b>{{ t('today.card.command') }}</b>{{ approvalCmd }}</p>
          <p v-else-if="approval.prompt_preview" class="dc-digest" data-test="dc-approval-prompt"><b>{{ t('today.card.task') }}</b>{{ approval.prompt_preview }}</p>
        </template>
        <template v-if="memory">
          <p data-test="dc-memory-proposal"><b>{{ t('today.card.proposal') }}</b>{{ memoryProposal }}</p>
          <p class="mono dc-mem-now" data-test="dc-memory-now">
            <b>{{ t('today.card.current') }}</b>{{ memory.key }} · {{ memory.current_kind || '—' }}<template v-if="memory.age"> · {{ memory.age }}</template>
            <template v-if="memory.current_summary"> · {{ memory.current_summary }}</template>
          </p>
          <p v-if="memory.payload?.content" class="dc-digest" data-test="dc-memory-merged"><b>{{ t('today.card.merged') }}</b>{{ memory.payload.content }}</p>
          <p v-if="memory.content" class="dc-digest dc-mem-body">{{ memory.content }}</p>
        </template>
        <div v-for="sg in card.suggestions ?? []" :key="sg.field" class="dc-sg">
          <span><b>{{ t('today.card.suggestion') }}</b>{{ sg.text }}</span>
          <button
            class="dc-btn dc-btn--ok"
            type="button"
            :disabled="readOnly"
            :title="readOnly ? CEILING_READ_ONLY_NOTICE : undefined"
            @click="suggestionAct(sg.field, true)"
          >{{ t('today.card.adopt') }}</button>
          <button
            class="dc-btn"
            type="button"
            :disabled="readOnly"
            :title="readOnly ? CEILING_READ_ONLY_NOTICE : undefined"
            @click="suggestionAct(sg.field, false)"
          >{{ t('today.card.dismiss') }}</button>
        </div>
      </div>
    </div>
    <RejectDialog v-if="rerunOpen" :submitting="false" error="" @close="rerunOpen = false" @confirm="onRerun" />
  </article>
</template>

<style scoped>
.dc-mem-now {
  overflow-wrap: anywhere;
}
.dc-mem-body {
  max-height: 10em;
  overflow: auto;
  color: var(--fg-3);
}
/* 卡片（设计稿 §7.1）：黄色左边线标「需要人」，面板圆角，不加阴影。 */
.dc {
  position: relative;
  display: flex;
  min-width: 0;
  padding: var(--s3) var(--s3) var(--s3) var(--s5);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  overflow: hidden;
}
.dc::before {
  content: '';
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 4px;
  background: var(--need);
}
/* 会超时 / 卡住别人的卡用红 / 蓝边线，与队列的分组标题一致。 */
.dc[data-u='now']::before {
  background: var(--fail);
}
.dc[data-u='blocking']::before {
  background: var(--run);
}
.dc-body {
  flex: 1;
  min-width: 0;
}
.dc-top {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s1) var(--s2);
  color: var(--fg-3);
  font-size: var(--fs-xs);
}
.dc-kind {
  font-family: var(--font-ui);
}
.dc-tag {
  color: var(--fg-2);
}
.dc-agent {
  color: var(--fg-3);
}
.dc-exp {
  color: var(--fail);
}
.dc-blk {
  color: var(--run);
}
.dc-woke {
  color: var(--accent);
}
.dc-when {
  margin-left: auto;
  white-space: nowrap;
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
  padding: var(--s1);
  background: var(--surface-2);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-panel);
  box-shadow: var(--shadow-pop);
}
.dc-menu-item {
  display: flex;
  gap: var(--s2);
  align-items: center;
  min-height: 32px;
  padding: var(--s1) var(--s2);
  color: var(--fg-2);
  background: transparent;
  border: 0;
  border-radius: var(--r-chip);
  font: inherit;
  font-size: var(--fs-sm);
  text-align: left;
  cursor: pointer;
}
.dc-menu-item:hover,
.dc-menu-item:focus-visible {
  background: var(--sunken);
  color: var(--fg);
}
.dc-menu-n {
  color: var(--fg-3);
  font-size: var(--fs-xs);
}
.dc-menu-hint {
  margin: var(--s1) var(--s2) 2px;
  color: var(--fg-3);
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}
.dc-menu-hint.bad {
  color: var(--fail);
}
.dc-title {
  margin: var(--s2) 0 var(--s1);
  font-size: var(--fs-lg);
  font-weight: 600;
  line-height: 1.35;
  letter-spacing: -0.01em;
  overflow-wrap: anywhere;
}
.dc-title a {
  color: var(--fg);
}
.dc-title a:hover {
  color: var(--accent);
  text-decoration: none;
}
.dc-sum {
  margin: 0 0 var(--s2);
  color: var(--fg-3);
  font-size: var(--fs-sm);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dc-acts {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s1);
}
.dc-sp {
  flex: 1;
}
.dc-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-height: 30px;
  padding: 0 var(--s3);
  color: var(--fg);
  background: var(--surface-2);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  white-space: nowrap;
  cursor: pointer;
}
.dc-btn:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--accent);
  text-decoration: none;
}
.dc-btn:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.dc-btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.dc-btn--pri {
  color: var(--bg);
  background: var(--accent);
  border-color: var(--accent);
  font-weight: 600;
}
.dc-btn--pri:hover:not(:disabled) {
  color: var(--bg);
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
  color: var(--accent);
  background: transparent;
  border-color: transparent;
}
.dc-btn--dim {
  color: var(--fg-3);
  background: transparent;
  border-color: transparent;
}
.dc-reply {
  display: flex;
  gap: var(--s1);
  margin-top: var(--s2);
}
.dc-reply input {
  flex: 1;
  min-width: 0;
  min-height: 32px;
  padding: 0 var(--s2);
  color: var(--fg);
  background: var(--surface-2);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font-size: var(--fs-sm);
}
.dc-info {
  margin-top: var(--s2);
  padding-top: var(--s2);
  border-top: 1px dashed var(--line);
  font-size: var(--fs-sm);
  color: var(--fg);
}
.dc-info p {
  margin: 0 0 var(--s1);
  overflow-wrap: anywhere;
}
.dc-info b {
  display: inline-block;
  min-width: 3em;
  margin-right: var(--s1);
  color: var(--fg-3);
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
  gap: var(--s1);
  margin-top: var(--s1);
}
.dc-sg span {
  flex: 1 1 200px;
  min-width: 0;
}
/* 手机：主操作铺满，点按目标 ≥40px（§6.3、§10）。 */
@media (max-width: 760px) {
  .dc {
    padding: var(--s3) var(--s3) var(--s3) var(--s4);
  }
  .dc-acts .dc-btn {
    flex: 1 1 auto;
    min-height: 40px;
  }
  .dc-when {
    margin-left: 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  .dc::before {
    transition: none;
  }
}
</style>
