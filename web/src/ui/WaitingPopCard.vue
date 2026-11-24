<script setup lang="ts">
// 顶部「等我」弹卡（design 2026-11-19 seats T5）：一次弹一件会话主体在等你回的事，其余显示 +N。
//
//  - 数据与队列在 composables/useWaitingPop（从推送快照判断该不该弹，卡片正文取 store/today 的
//    waiting 卡，与今天页同源）；本组件只管渲染，并把点击转成队列动作。
//  - 按钮就四个：作答（会话等回复 / 提问，短回复直接送）/ 批准（权限请求、待批 job）/ 打开（跳线程
//    或详情）/ 稍后（今天页同一个「稍后」），右上角 × 关闭（写收件箱 seen，多端同步后别的端不再弹）。
//  - 动作都走卡片现有的提交路径（store/today 的 actOnCard / snoozeCard）：撤销窗口与 today.action
//    审计与在今天页上点按钮完全一致。
import { computed, nextTick, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { BellRing } from '@lucide/vue'
import Icon from './Icon.vue'
import { t } from '../i18n'
import { useWaitingPop } from '../composables/useWaitingPop'
import { actionLabel } from '../utils/today'
import { cardActionBlock } from '../utils/todayGate'
import { useWhoami } from '../store/whoami'

const pop = useWaitingPop()
const router = useRouter()
const { whoami } = useWhoami()

const replyOpen = ref(false)
const replyText = ref('')
const replyInput = ref<HTMLInputElement | null>(null)

const item = pop.item
const card = pop.card
const extra = pop.extra
const replyAction = pop.replyAction
const approveAction = pop.approveAction
// 卡还没到（推送先到、today 的重拉在途）时先用推送里的标题顶上。
const title = computed(() => card.value?.title || item.value?.title || '')
const summary = computed(() => card.value?.summary ?? '')
// 类型文案优先用服务端给的「等我」类别（与今天页 / 收件箱同一句）。
const kindLabel = computed(() => card.value?.waiting_label || (item.value ? t(`today.kind.${item.value.kind}`) : ''))
// 只读身份 / 缺权限时按钮灰显并说明原因（与今天页同一套判定）。
const replyBlocked = computed(() => cardActionBlock(whoami.value, card.value, replyAction.value))
const approveBlocked = computed(() => cardActionBlock(whoami.value, card.value, approveAction.value))

// 换到下一件时收起上一件的输入框与草稿。
watch(
  () => item.value?.key,
  () => {
    replyOpen.value = false
    replyText.value = ''
  },
)

function openReply(): void {
  replyOpen.value = true
  void nextTick(() => replyInput.value?.focus())
}

function sendReply(): void {
  const text = replyText.value.trim()
  if (!text) return
  pop.reply(text)
}

function onReplyKey(ev: KeyboardEvent): void {
  if (ev.key === 'Enter' && !ev.isComposing) {
    ev.preventDefault()
    sendReply()
  } else if (ev.key === 'Escape') {
    replyOpen.value = false
    replyText.value = ''
  }
}

// 「打开」：先收起这张（不然切页后它还盖在内容上），再跳到线程 / 详情。
function open(): void {
  const to = pop.link.value
  pop.close()
  if (to) void router.push(to)
}
</script>

<template>
  <div
    v-if="item"
    class="pop"
    role="dialog"
    :aria-label="t('util.pop.title')"
    data-test="waiting-pop"
  >
    <div class="pop-head">
      <Icon class="pop-ico" :icon="BellRing" size="sm" />
      <span class="pop-kind" data-test="waiting-pop-kind">{{ kindLabel }}</span>
      <span v-if="item.project_key" class="pop-proj mono">{{ item.project_key }}</span>
      <span v-if="extra > 0" class="pop-more" data-test="waiting-pop-extra">{{ t('util.pop.extra', { n: extra }) }}</span>
      <button
        class="pop-close"
        type="button"
        :aria-label="t('util.pop.close')"
        :title="t('util.pop.close')"
        data-test="waiting-pop-close"
        @click="pop.close()"
      >
        ×
      </button>
    </div>
    <p class="pop-title" data-test="waiting-pop-title">{{ title }}</p>
    <p v-if="summary" class="pop-sum" data-test="waiting-pop-summary">{{ summary }}</p>

    <div class="pop-acts">
      <button
        v-if="replyAction"
        class="pop-btn pop-btn--pri"
        type="button"
        data-test="waiting-pop-reply"
        :disabled="!!replyBlocked"
        :title="replyBlocked || undefined"
        @click="openReply"
      >{{ actionLabel(replyAction) }}</button>
      <button
        v-if="approveAction"
        class="pop-btn pop-btn--ok"
        type="button"
        data-test="waiting-pop-approve"
        :disabled="!!approveBlocked"
        :title="approveBlocked || undefined"
        @click="pop.approve()"
      >{{ actionLabel(approveAction) }}</button>
      <span class="pop-sp"></span>
      <button class="pop-btn pop-btn--dim" type="button" data-test="waiting-pop-open" @click="open">
        {{ t('util.pop.open') }}
      </button>
      <button
        class="pop-btn pop-btn--dim"
        type="button"
        data-test="waiting-pop-snooze"
        :disabled="!card"
        @click="pop.snooze()"
      >
        {{ t('today.card.snooze') }}
      </button>
    </div>

    <div v-if="replyOpen && replyAction" class="pop-reply" data-test="waiting-pop-reply-box">
      <input
        ref="replyInput"
        v-model="replyText"
        class="mono"
        :placeholder="t('today.card.replyPlaceholder', { label: actionLabel(replyAction) })"
        :aria-label="actionLabel(replyAction)"
        data-test="waiting-pop-input"
        @keydown="onReplyKey"
      />
      <button
        class="pop-btn pop-btn--pri"
        type="button"
        :disabled="!replyText.trim()"
        data-test="waiting-pop-send"
        @click="sendReply"
      >{{ t('today.card.send') }}</button>
    </div>
  </div>
</template>

<style scoped>
.pop {
  position: fixed;
  top: 60px;
  right: 18px;
  z-index: 72;
  width: min(380px, calc(100vw - 36px));
  padding: var(--s3) var(--s3) var(--s3) var(--s4);
  background: var(--surface);
  border: 1px solid var(--need);
  border-radius: var(--r-panel);
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.35);
}
.pop::before {
  content: '';
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 4px;
  border-radius: var(--r-panel) 0 0 var(--r-panel);
  background: var(--need);
}
.pop-head {
  display: flex;
  align-items: center;
  gap: var(--s2);
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.pop-ico {
  flex: none;
  color: var(--need-ink);
}
.pop-kind {
  color: var(--need-ink);
  font-weight: 600;
}
.pop-proj {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pop-more {
  margin-left: auto;
  color: var(--ink-3);
  font-variant-numeric: tabular-nums;
}
.pop-close {
  flex: none;
  background: transparent;
  border: 0;
  color: var(--ink-3);
  font-size: 16px;
  line-height: 1;
  padding: 0 2px;
}
.pop-close:hover {
  color: var(--ink);
}
.pop-title {
  margin: var(--s2) 0 var(--s1);
  color: var(--ink);
  font-size: var(--fs-md);
  font-weight: 600;
  line-height: 1.35;
  overflow-wrap: anywhere;
}
.pop-sum {
  margin: 0 0 var(--s2);
  color: var(--ink-2);
  font-size: var(--fs-sm);
  line-height: 1.45;
  overflow-wrap: anywhere;
}
.pop-acts {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s1);
}
.pop-sp {
  flex: 1;
}
.pop-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-height: 30px;
  padding: 0 var(--s3);
  color: var(--ink);
  background: var(--surface-2);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  white-space: nowrap;
  cursor: pointer;
}
.pop-btn:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--accent);
}
.pop-btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.pop-btn--pri {
  color: var(--bg);
  background: var(--accent);
  border-color: var(--accent);
  font-weight: 600;
}
.pop-btn--pri:hover:not(:disabled) {
  color: var(--bg);
}
.pop-btn--ok {
  color: var(--done);
  border-color: var(--done);
}
.pop-btn--dim {
  color: var(--ink-3);
  background: transparent;
  border-color: transparent;
}
.pop-reply {
  display: flex;
  gap: var(--s1);
  margin-top: var(--s2);
}
.pop-reply input {
  flex: 1;
  min-width: 0;
  min-height: 32px;
  padding: 0 var(--s2);
  color: var(--ink);
  background: var(--surface-2);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font-size: var(--fs-sm);
}
/* 手机：卡片顶到顶栏下面，按钮铺满、点按目标 ≥40px。 */
@media (max-width: 760px) {
  .pop {
    top: 56px;
    right: 12px;
    left: 12px;
    width: auto;
  }
  .pop-acts .pop-btn {
    flex: 1 1 auto;
    min-height: 40px;
  }
}
</style>
